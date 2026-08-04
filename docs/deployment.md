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
- [Health checks](#health-checks)
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
[cache]
disk_path = "/var/cache/jul/cache"

[observability.access_log]
sinks = ["file"]
file  = "/var/log/jul/access.log"

[admin]
history_dir = "/var/lib/jul/history"

# ACME is configured per server, under its TLS block:
[[servers]]
  [servers.tls.acme]
  cache_dir = "/var/cache/jul/acme"
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
  ghcr.io/victornife/jul:latest
```

- **Reproducible base images.** Both build stages pin their base image by tag
  **and** `@sha256` digest, so a rebuild always resolves the same bytes;
  Dependabot (the `docker` ecosystem in
  [.github/dependabot.yml](../.github/dependabot.yml)) bumps the digests on a
  schedule so the pins stay current without manual toil.
- **Runs out of the box.** The image bakes a container-tailored config
  ([deploy/docker/server.toml](../deploy/docker/server.toml)): it enables the
  admin listener on loopback (so the built-in `HEALTHCHECK` works — see
  [Health checks](#health-checks)) and serves a placeholder page from `/var/www`
  so the server starts cleanly with no host mounts.
- **Named volumes** are seeded from the image on first use, so the baked
  `/etc/jul/server.toml` survives; edit it through the console (editable shape).
- For a **read-only** config, bind-mount your config file read-only:
  `-v /host/server.toml:/etc/jul/server.toml:ro` and skip the `jul-config`
  volume. Your file must enable `[admin]` for the `HEALTHCHECK` to pass, or
  override the healthcheck.
- Serve your own site by mounting content over the placeholder root:
  `-v /host/site:/var/www:ro`, or edit the route to `proxy_pass` to a backend.
- The ACME cache lives under `/var/cache/jul`; keep that volume to avoid
  re-issuing certificates (and hitting CA rate limits) on every restart.
- Set an `[admin] token` (and only then map `-p 9090:9090`) before exposing the
  admin API beyond the container; by default it binds to `127.0.0.1` and is
  reachable only by the container's own health probe.
- **Access logs at high throughput.** The default config writes access logs to
  `stdout` (Docker's log driver). At ≥10,000 req/s this generates several MB/s
  of log output; if Docker's log driver buffers to disk it can fill container
  storage quickly. For production deployments at volume, redirect access logs to
  the named volume instead of stdout:
  ```toml
  [observability.access_log]
  sinks = ["file"]
  file  = "/var/log/jul/access.log"
  ```
  Jul.IA does not yet expose an explicit access-log disable switch. Do not use
  `sinks = []` as an off setting: the current defaulting path restores the
  default stdout sink. #124 adds an explicit `enabled` field; until that lands,
  use a dedicated sink and control retention at the deployment/log-driver layer.

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
Point `servers.tls.acme.cache_dir`, the disk cache, the access-log file sink, and
`history_dir` at the matching subdirectories.

## Health checks

The admin listener serves two unauthenticated probe endpoints — `GET /healthz`
(process **liveness**) and `GET /readyz` (**readiness**; returns `503` until the
server can serve traffic, for example while a certificate is expired). Enable the
admin listener to expose them:

```toml
[admin]
enabled = true
listen  = "127.0.0.1:9090"
```

The `jul healthcheck` subcommand polls these endpoints and maps the result to a
deterministic exit code, so an orchestrator can probe the server **without a
shell or `curl`** (the distroless image ships neither):

| Exit | Meaning |
| --- | --- |
| `0` | healthy — the endpoint returned `2xx` |
| `1` | unhealthy — non-`2xx`, unreachable, or the timeout elapsed |
| `2` | usage/config error — bad flags, unreadable config, or admin disabled |

By default it discovers the address from `[admin] listen` in the config; `-addr`
or `-url` override it, and `-ready` probes `/readyz` instead of `/healthz`.

**Docker** — the image already declares this `HEALTHCHECK` (exec form, no shell),
and its baked config enables the admin listener on loopback so the probe passes
out of the box:

```dockerfile
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD ["/usr/local/bin/jul", "healthcheck", "--config", "/etc/jul/server.toml", "--quiet"]
```

`docker inspect --format '{{.State.Health.Status}}' <container>` then reports
`healthy` once the server is up. If you bind-mount your own config, keep `[admin]`
enabled (or override the healthcheck) so the probe can reach a health endpoint.

**systemd** — confirm the admin endpoint is live after start:

```ini
ExecStartPost=/usr/local/bin/jul healthcheck --config /etc/jul/server.toml --ready --quiet
```

**Kubernetes** — use it as an exec probe (no `curl` needed in the image):

```yaml
livenessProbe:
  exec:
    command: ["jul", "healthcheck", "--config", "/etc/jul/server.toml", "--quiet"]
  periodSeconds: 30
readinessProbe:
  exec:
    command: ["jul", "healthcheck", "--config", "/etc/jul/server.toml", "--ready", "--quiet"]
  periodSeconds: 10
```

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
