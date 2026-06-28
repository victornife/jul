# Deployment

> How to run Jul.IA (`jul`) as a long-lived service with the right writable
> state in the right places. Two deployment shapes are supported — **editable**
> (the admin console can apply config changes and roll back) and **read-only**
> (the config is immutable, delivered by your provisioning pipeline). Both keep
> the rest of the host locked down.

## Contents

- [The two shapes](#the-two-shapes)
- [Directory layout](#directory-layout)
- [systemd (Linux)](#systemd-linux)
- [Docker](#docker)
- [Windows service](#windows-service)
- [What writes where](#what-writes-where)

## The two shapes

| Shape | Config source | Admin "Apply" | Use when |
| --- | --- | --- | --- |
| **Editable** | The running server owns and rewrites `server.toml`. | Writes the new config + a history snapshot, then reloads. | You operate the server through the console and want one-click apply + rollback. |
| **Read-only** | A provisioning pipeline (image build, GitOps, config-management) ships `server.toml`; the server only reads it. | Rejected — the config path is mounted read-only. | Golden-image / immutable-infrastructure deployments where config changes go through your pipeline, not the console. |

In both shapes the binary runs **unprivileged** and the host filesystem is
read-only except for a small set of explicitly writable directories.

## Directory layout

The canonical paths (used by the bundled systemd units and Docker image):

| Path | Holds | Editable | Read-only |
| --- | --- | --- | --- |
| `/etc/jul` | `server.toml` | read-write (admin rewrites it) | read-only |
| `/var/lib/jul` | config **history** snapshots | read-write | not used (no history) |
| `/var/cache/jul` | HTTP **disk cache** + **ACME** certificate cache | read-write | read-write |
| `/var/log/jul` | access-log **file** sink output | read-write | read-write |

Point the matching config fields at these paths:

```toml
[acme]
cache_dir = "/var/cache/jul/acme"

[cache]
# ... disk cache directory under /var/cache/jul ...

[observability.access_log]
sinks = ["file"]
file  = "/var/log/jul/access.log"

[admin]
history_dir = "/var/lib/jul/history"
```

Config and history can carry inline credentials. Jul.IA writes both with mode
`0o600` and replaces them atomically (temp file + rename), so a freshly written
file is never world-readable and a crash mid-write leaves the previous complete
file. See [SECURITY.md](../SECURITY.md#hardening-defaults--recommendations).
Prefer [secret references](secrets.md) over inline literals regardless.

## systemd (Linux)

Two units ship in [deploy/systemd](../deploy/systemd/):

### Editable — `jul.service`

Uses `DynamicUser=yes` plus systemd's managed directories, so the four writable
paths above are created, owned by the service user, and survive restarts while
`ProtectSystem=strict` keeps everything else read-only:

```ini
ConfigurationDirectory=jul       # /etc/jul        (0700)
StateDirectory=jul               # /var/lib/jul    (0700)
CacheDirectory=jul               # /var/cache/jul
LogsDirectory=jul                # /var/log/jul
```

Seed the initial config, then start:

```sh
sudo install -D -m600 server.toml /etc/jul/server.toml
sudo systemctl enable --now jul.service
```

The admin console can now apply changes and roll back from history.

### Read-only — `jul-readonly.service`

Pins the config: `/etc/jul` is mounted `ReadOnlyPaths`, so an admin "Apply" is
rejected. It uses a **static** service user (a dynamic UID changes between boots,
which is awkward for an operator-owned config file). Create the user and seed the
immutable config:

```sh
sudo useradd --system --no-create-home --shell /usr/sbin/nologin jul
sudo install -d -m0755 /etc/jul
sudo install -m0640 -o root -g jul server.toml /etc/jul/server.toml
sudo systemctl enable --now jul-readonly.service
```

`CacheDirectory` and `LogsDirectory` remain writable for the disk cache, the
ACME certificate cache, and access logs.

### Verify the units

```sh
systemd-analyze verify deploy/systemd/jul.service
systemd-analyze verify deploy/systemd/jul-readonly.service
```

## Docker

The [Dockerfile](../Dockerfile) builds a static binary into a distroless
`nonroot` image and declares the four writable paths as volumes, owned by the
nonroot user:

```sh
docker run --rm \
  -p 8080:8080 -p 8443:8443 \
  -v jul-config:/etc/jul \
  -v jul-state:/var/lib/jul \
  -v jul-cache:/var/cache/jul \
  -v jul-log:/var/log/jul \
  ghcr.io/example/jul:latest
```

- **Named volumes** are seeded from the image on first use, so the baked
  `/etc/jul/server.toml` survives; edit it through the console (editable shape).
- For a **read-only** config, bind-mount your config file read-only:
  `-v /host/server.toml:/etc/jul/server.toml:ro` and skip the `jul-config`
  volume.
- The ACME cache lives under `/var/cache/jul`; keep that volume to avoid
  re-issuing certificates (and hitting CA rate limits) on every restart.

## Windows service

[deploy/windows/install-service.ps1](../deploy/windows/install-service.ps1)
registers the service under the per-service virtual account `NT SERVICE\jul`
(the Windows analogue of the unprivileged systemd user) and creates an ACL'd
data directory:

```powershell
# Elevated PowerShell
.\install-service.ps1 `
  -BinaryPath 'C:\Program Files\jul\jul.exe' `
  -ConfigPath 'C:\ProgramData\jul\server.toml' `
  -DataDir    'C:\ProgramData\jul'
Start-Service jul
```

It creates `C:\ProgramData\jul\{history,cache,logs}` and grants the service
account **modify** there and **read** on the config; ordinary users get neither.
Point `acme.cache_dir`, the disk cache, the access-log file sink, and
`history_dir` at the matching subdirectories.

## What writes where

| Subsystem | Writes to | Notes |
| --- | --- | --- |
| Admin "Apply" | `/etc/jul/server.toml` | atomic (temp + rename), `0o600`, editable shape only |
| Config history | `/var/lib/jul` | one snapshot per apply, pruned to `history_dir` retention, `0o600` |
| HTTP disk cache | `/var/cache/jul` | cached response bodies |
| ACME | `/var/cache/jul` | account key + issued certificates — back this up / persist it |
| Access log (`file` sink) | `/var/log/jul` | rotated by size/age per `[observability.access_log]` |

See [reload-semantics.md](reload-semantics.md) for what an "Apply" guarantees
and which changes need a restart, and the [status matrix](status.md) for feature
maturity.
