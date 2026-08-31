# Deployment

> How to run Jul.IA (`jul`) as a long-lived service with the right writable
> state in the right places. Two deployment shapes are supported — **editable**
> (the admin console can apply config changes and roll back) and **read-only**
> (the config is immutable, delivered by your provisioning pipeline). Both keep
> the rest of the host locked down.

## Contents

- [The two shapes](#the-two-shapes)
- [Configuration authority](#configuration-authority)
- [Directory layout](#directory-layout)
- [systemd (Linux)](#systemd-linux)
- [Docker](#docker)
- [Windows service](#windows-service)
- [Health checks](#health-checks)
- [Behind a reverse proxy or load balancer](#behind-a-reverse-proxy-or-load-balancer)
- [What writes where](#what-writes-where)

## The two shapes

| Shape | Config source | Admin "Apply" | Use when |
| --- | --- | --- | --- |
| **Editable** | The running server owns and rewrites `server.toml`. | Writes the new config + a history snapshot, then reloads. | You operate the server through the console and want one-click apply + rollback. |
| **Read-only** | A provisioning pipeline (image build, GitOps, config-management) ships `server.toml`; the server only reads it. | Rejected — the config path is mounted read-only. | Golden-image / immutable-infrastructure deployments where config changes go through your pipeline, not the console. |

In both shapes the binary runs **unprivileged** and the host filesystem is
read-only except for a small set of explicitly writable directories.

## Configuration authority

The filesystem shape above (writable vs read-only mount) is a *permission*.
`[global].config_authority` is the *logical* declaration of who owns the
file, and it is what the admin API and the file watcher/SIGHUP actually
consult (ADR 0019). The two line up directly:

| Filesystem shape | `config_authority` | Result |
| --- | --- | --- |
| Editable | `managed` | Console/API writes are validated, persisted, and reloaded; an external edit to the mounted file becomes drift, resolved only through an explicit `POST /api/config/adopt-external`. |
| Read-only | `file_owned` (the default) | Every mutating admin endpoint is refused with `409 config_authority_read_only` before any side effect. SIGHUP and the file watcher behave exactly as before: an external edit — from your provisioning pipeline re-rendering the mount — is validated and adopted live or staged, same as today. |

**`config_authority` defaults to `file_owned` when omitted.** This is a fixed
default, not derived from `admin.enabled` or any other field. The practical
consequence: **an Editable deployment that has never declared
`config_authority` becomes read-only at its next restart** after upgrading to
a Jul.IA version that implements ADR 0019. The fix is one line:

```toml
[global]
config_authority = "managed"
```

The runtime overview, the Console banner, and every `409
config_authority_read_only` response name this exact field, so the fix is
never a mystery. `jul lint` also recommends declaring it explicitly whenever
`[admin].enabled` is true. See
[CHANGELOG.md](../CHANGELOG.md) for this as a required migration step and
[reload-semantics.md](reload-semantics.md#configuration-authority-managed-vs-file-owned)
for the full managed-mode behavior (drift detection, adoption, refusal
rules).

A **Read-only** deployment needs no change at all: the default already
matches its shape, and its existing GitOps/pipeline workflow — re-render the
mount, `SIGHUP` or let the watcher pick it up — is completely unaffected.

Managed mode additionally requires the config path to be a real, writable,
non-symlinked file (§11.3): a Kubernetes ConfigMap/Secret mount, which is a
symlink farm, cannot be `managed` — declare `file_owned` for those and let
your controller re-render the mount instead.

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
  enabled = true
  sinks = ["file"]
  file  = "/var/log/jul/access.log"
  ```
  To suppress request access records entirely, set `enabled = false`. This does
  not suppress process, security, audit, health, metric, or trace output. Do not
  use `sinks = []` as an off setting: an enabled block with an explicit empty
  sink list is rejected. Access-log changes take effect after a restart.

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

## Behind a reverse proxy or load balancer

When another proxy, a CDN or a cloud load balancer sits in front of Jul, every
request arrives from *that* device. Jul does not believe forwarding headers by
default, so without configuration the client address is the proxy's address —
correct, but not what you want for logging or per-client policy.

Declare the proxies you operate:

```toml
[[servers]]
listen = ":443"

[servers.client_address]
trusted_proxies = ["10.0.0.0/8"]      # your load balancer subnet, nothing wider
```

Once declared, the same address is used everywhere: CIDR authentication, IP
rate-limit buckets, WAF rules, the access log's `client_ip`, the
`X-Forwarded-For` sent to your backends, and the FastCGI `REMOTE_ADDR`. Until
you declare it, all of those describe the proxy.

Rules of thumb:

- **List addresses you control.** `trusted_proxies` is a security boundary:
  anything it covers may claim to be any client. Prefer the load balancer's
  actual subnet over a whole RFC 1918 range, and never `0.0.0.0/0`.
- **Every server block on the same `listen` must declare the same policy.**
  Identity is derived before the `Host` header selects a block, so validation
  rejects siblings that disagree.
- **Terminate the chain at your edge.** Jul reads the first configured header
  present (`Forwarded`, then `X-Forwarded-For`) and never merges the two. Make
  sure your edge overwrites rather than appends attacker-supplied values.
- **Check it.** `jul lint` warns about an over-broad entry; `jul check` rejects a
  malformed prefix or a policy that disagrees with a sibling block.
- **Direct deployments need nothing.** With no block the canonical client is the
  transport peer, which is what a directly exposed server should use.

The policy covers HTTP/1.1, HTTP/2 and HTTP/3 on that listener. It does not
cover the admin listener or `[[stream]]` L4 proxying; see
[known limitations](known-limitations.md). Full reference:
[configuration.md](configuration.md#client-address-and-trusted-proxies).

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

The admin listener defaults to loopback, and a request that consumes an admin
credential over a non-loopback, non-TLS connection is refused with `403
insecure_transport` (ADR 0019 §28.1) — `/healthz` and `/readyz` are exempt, so
health probes keep working regardless. To reach the admin listener remotely,
terminate TLS with [`[admin.tls]`](configuration.md#admintls) rather than
binding off-loopback in cleartext; see [docs/security-posture.md](security-posture.md)
for the full hardening checklist.

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
