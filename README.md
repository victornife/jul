# Jul.IA

[![Status](https://img.shields.io/badge/status-active-brightgreen.svg)](https://github.com/victornife/jul)
[![Go Version](https://img.shields.io/badge/go-1.26-blue.svg)](https://github.com/victornife/jul/blob/main/go.mod)
[![License](https://img.shields.io/badge/license-PolyForm%20Non--Commercial%201.0.0-blue.svg)](https://github.com/victornife/jul#license)
[![Codecov](https://codecov.io/gh/victornife/jul/graph/badge.svg)](https://codecov.io/gh/victornife/jul)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](https://github.com/victornife/jul)

**Jul.IA** is an NGINX-inspired HTTP edge server written in Go and configured
entirely through TOML. It bundles reverse-proxying with load balancing, static
file serving, FastCGI/uWSGI application gateways, a two-tier response cache, TLS
termination, hot configuration reload, and a built-in admin/observability
interface — all in a single static, dependency-free binary.

- **Binary / module / service name:** `jul`
- **Product name:** `Jul.IA`
- **Language:** Go 1.26
- **License:** PolyForm Non-Commercial 1.0.0

> Where this is headed: see the [vision](docs/vision/) and the
> [roadmap](docs/roadmap/) (Years 1–5), with detailed per-feature
> [engineering specs](docs/specs/). Durable technical decisions are
> recorded as [ADRs](docs/adr/), and how the direction evolves is tracked in
> the [reviews & decision log](docs/reviews/).
>
> New to HTTP, proxies, TLS, caching, or observability? The
> [concepts appendix](docs/vision/appendix.md) walks through how a request
> travels through modern edge infrastructure, from first principles.

---

## Table of contents

- [Features](#features)
- [Installation](#installation)
- [Quick start](#quick-start)
- [Command-line usage](#command-line-usage)
- [Migrating from NGINX](#migrating-from-nginx)
- [Configuration reference](#configuration-reference)
  - [`[global]`](#global)
  - [`[[servers]]`](#servers)
  - [`[[servers.locations]]`](#serverslocations)
  - [`[[upstreams]]`](#upstreams)
  - [`[upstreams.discovery]`](#upstreamsdiscovery)
  - [`[cache]`](#cache)
  - [`[compression]`](#compression)
  - [`[rate_limit]`](#rate_limit)
  - [`[servers.locations.auth]`](#serverslocationsauth)
  - [`[admin]`](#admin)
  - [`[observability.tracing]`](#observabilitytracing)
  - [`[observability.metrics]`](#observabilitymetrics)
  - [`[observability.access_log]`](#observabilityaccess_log)
  - [TLS](#tls)
  - [Automatic HTTPS (ACME)](#automatic-https-acme)
  - [HTTP/3 (QUIC)](#http3-quic)
  - [`[plugins]`](#plugins)
  - [`[[stream]]`](#stream)
- [WebAssembly plugins](#webassembly-plugins)
- [L4 stream proxy](#l4-stream-proxy)
- [gRPC passthrough](#grpc-passthrough)
- [Service discovery](#service-discovery)
- [Web application firewall](#web-application-firewall)
- [Secrets references](#secrets-references)
- [Hot reload](#hot-reload)
- [Admin interface & observability](#admin-interface--observability)
- [Running real applications behind Jul.IA](#running-real-applications-behind-julia)
- [Deployment](#deployment)
  - [As a Linux systemd service](#as-a-linux-systemd-service)
  - [As a Windows service](#as-a-windows-service)
  - [With Docker](#with-docker)
- [Building from source & cross-compiling](#building-from-source--cross-compiling)
- [Project layout](#project-layout)
- [Troubleshooting](#troubleshooting)

---

## Features

| Area | Capability |
| ---- | ---------- |
| **Static files** | Document root serving, index files, `try_files`, optional directory listing, hidden-file control, `Cache-Control` headers |
| **Reverse proxy** | `proxy_pass` to a concrete URL or a named upstream; per-location connect/read/send timeouts; custom upstream headers with variable expansion |
| **WebSocket & SSE** | Transparent passthrough of `Connection: Upgrade` (HTTP `101`) connections — text and binary frames spliced bidirectionally (Apollo GraphQL subscriptions, Socket.IO) — and `text/event-stream` / chunked responses streamed per write, never buffered (Node/Python SSE) |
| **Load balancing** | `round_robin`, `weighted_round_robin`, and `least_conn` strategies across an upstream pool |
| **Health & failover** | Passive health checking (`max_fails` / `fail_timeout`) plus optional active HTTP/TCP probes (`[upstreams.health_check]`), with automatic retry of idempotent requests against healthy backends |
| **Service discovery** | Resolve an upstream's backends dynamically and refresh the pool live without a reload (`[upstreams.discovery]`): **DNS** A/AAAA and **DNS SRV** in every build, plus **Consul** and **Kubernetes** EndpointSlices behind the `consul`/`kubernetes` build tags — failed or empty resolves keep the last-good backends |
| **App gateways** | `fastcgi_pass` (e.g. PHP-FPM) and `uwsgi_pass` (Python/WSGI) with full CGI parameter mapping |
| **gRPC transcoding** | Expose a gRPC service as a RESTful JSON API via `google.api.http` annotations (`grpc_transcode`) — unary and streaming (server/client/bidi, NDJSON or SSE) — from a compiled descriptor set or server reflection, opt-in `grpc` build tag |
| **gRPC passthrough** | Reverse-proxy **native gRPC** end to end over HTTP/2 (`grpc = true`) — trailers preserved, streaming frames flushed immediately, load balancing and health checks applied — with cleartext **h2c** inbound (`h2c = true`) for clients without TLS, opt-in `grpc` build tag |
| **Response cache** | Two-tier (in-memory + optional disk overflow) cache with TTL, `stale-while-revalidate`, and admin purge |
| **Compression** | On-the-fly `gzip` (every build) plus `br`/`zstd` codings (via the `brotli`/`zstd` build tags); `Accept-Encoding` negotiation, MIME allow-list, size threshold, and precompressed `.br`/`.gz` sidecar serving for static files |
| **Rate limiting** | Token-bucket request limiting keyed by client IP, a request header, or a JWT claim, with burst, global or per-location policy, and `429` + `Retry-After`; plus a per-listener concurrent-connection cap |
| **Access control** | Per-location CIDR allow/deny lists plus one credential method — HTTP Basic (bcrypt `htpasswd`), JWT bearer tokens validated against a JWKS endpoint (asymmetric algorithms only, `none` rejected), or forward-auth to an external service |
| **WAF** | ModSecurity-compatible web application firewall ([Coraza](https://github.com/corazawaf/coraza)) with the **OWASP Core Rule Set embedded** in the binary (`[waf]`, global or per-location): `block`/`detect` modes, paranoia levels, your own SecLang files or inline rules, request/response body inspection, and a `jul_waf_events_total` metric — opt-in `waf` build tag ([docs/waf.md](docs/waf.md)) |
| **Secrets references** | Keep credentials out of the config file: any string field accepts `${env:NAME}`, `${file:/path}`, or `${secret:/path}` references resolved at serve time, resolved values are **masked from logs**, and `jul lint` flags literal admin/Consul/Kubernetes tokens — core, no build tag ([docs/secrets.md](docs/secrets.md)) |
| **TLS** | TLS 1.2/1.3 termination per server block, configurable minimum version, optional HTTP→HTTPS redirect |
| **Automatic HTTPS** | ACME (Let's Encrypt) certificate issuance and auto-renewal via the HTTP-01 challenge, on-disk cache — opt-in `acme` build tag |
| **HTTP/3** | HTTP/3 over QUIC on the same address (UDP), sharing the server's TLS certificates (static or ACME, including reloads), advertised to clients via an `Alt-Svc` header — opt-in `http3` build tag |
| **Routing** | `exact`, `prefix`, and `regex` location matching; regex rewrites with `last`/`break`/`redirect`/`permanent` flags |
| **Virtual hosts** | Multiple `server_names` per listener; multiple listen addresses |
| **Limits & timeouts** | `client_max_body_size`, header size caps, read/write/idle/header timeouts (per-server, location overrides for body size) |
| **Redirects** | `return`, `redirect`, and `deny` (403) location actions; custom error pages |
| **Hot reload** | Zero-downtime config reload via SIGHUP, file-watch, or the admin API — invalid configs are rejected and the old config keeps serving |
| **Observability** | Structured logging (text/JSON), pluggable access-log sinks (file/syslog with rotation), Prometheus metrics, OpenTelemetry tracing, health/readiness probes |
| **Admin GUI** | Loopback-bound web console (token-auth): live metrics dashboard, upstream health, certificate inventory, config history with one-click rollback, and a setup wizard (`console` build tag) plus health, metrics, cache purge, reload, and config editing |
| **Developer experience** | Zero-config `jul run --serve`/`--proxy` (no file needed), `jul lint` best-practice checks with CI-friendly exit codes, and `jul fmt` canonical formatting |
| **Migration** | `jul import nginx` translates an existing NGINX config to Jul.IA TOML, reporting every directive it could not map — opt-in `importer` build tag |
| **WebAssembly plugins** | Sandboxed request middleware and handlers compiled to WASM and run on the embedded [wazero](https://wazero.io) runtime (pure Go, no cgo): per-plugin memory and time limits, panic isolation, capability-gated key/value store, hot-reloadable — opt-in `wasmplugins` build tag |
| **L4 stream proxy** | TCP and UDP reverse proxying (`[[stream]]`) with load balancing and health checks across an upstream pool, TLS **SNI routing** by host without terminating, and HAProxy **PROXY protocol** v1/v2 (in and out) to preserve the client address — survives hot reload, opt-in `stream` build tag |
| **Portability** | Single static binary, no runtime dependencies; Windows, Linux, and macOS on amd64/arm64 |

> Note: The `[[mail]]` table is reserved for a future version. It is parsed but
> **rejected during validation** in v1 so configs fail loudly rather than
> silently. The `[[stream]]` (L4 proxy) table is active in binaries built with
> the `stream` tag; in a binary without that tag a populated `[[stream]]` table
> is rejected at startup. The `[plugins]` table is active in binaries built with
> the `wasmplugins` tag; in a binary without that tag a populated `[plugins]`
> table is rejected at startup. The `[waf]` table (and per-location `waf`
> override) is active in binaries built with the `waf` tag; in a binary without
> that tag an enabled WAF config is rejected at startup.

---

## Installation

Download the archive for your platform from a [release](https://github.com/example/jul/releases)
(or build it yourself — see [Building from source](#building-from-source--cross-compiling))
and extract it. Each archive contains the binary, a sample `server.toml`, the
SBOM, and `README`/`SECURITY` docs.

Archives are named `jul_<version>_<os>_<arch>_<profile>.(tar.gz|zip)` and ship in
two **profiles**: `lean` (the default build, no optional features) and `full`
(every opt-in feature — Brotli/Zstd, ACME, console, OTel, gRPC, HTTP/3, importer,
WASM plugins, stream proxy, Consul/Kubernetes discovery, WAF). Pick `full` unless
you specifically want the smaller lean binary.

| Platform | Archive (full profile) |
| -------- | ------- |
| Windows (Intel/AMD 64-bit) | `jul_<version>_windows_amd64_full.zip` |
| Windows (ARM64) | `jul_<version>_windows_arm64_full.zip` |
| Linux (Intel/AMD 64-bit) | `jul_<version>_linux_amd64_full.tar.gz` |
| Linux (ARM64) | `jul_<version>_linux_arm64_full.tar.gz` |
| macOS (Apple Silicon) | `jul_<version>_darwin_arm64_full.tar.gz` |
| macOS (Intel) | `jul_<version>_darwin_amd64_full.tar.gz` |

Swap `full` for `lean` for the minimal build. Verify your download and the
build provenance before running it — see [docs/release.md](docs/release.md) for
the `sha256` checksums, SBOM, and `gh attestation verify` steps, plus the full
list of variants and per-platform install notes.

Not sure which architecture you need?

- **Windows:** `echo $env:PROCESSOR_ARCHITECTURE` → `AMD64` or `ARM64`
- **Linux/macOS:** `uname -m` → `x86_64` (amd64) or `aarch64`/`arm64`

---

## Quick start

From the extracted folder (or the repo root if running from source):

**Windows (PowerShell):**

```powershell
.\jul.exe --config .\server.toml
```

**Linux / macOS:**

```bash
chmod +x ./jul
./jul --config ./server.toml
```

**From source (Go installed):**

```bash
go run ./cmd/jul --config server.toml
```

Validate a configuration without starting the server:

```bash
./jul --config server.toml --check
```

Print the version:

```bash
./jul --version
```

---

## Command-line usage

```text
jul [flags]                                   run the server (default)
jul lint [-config f] [-strict]                validate + best-practice checks
jul fmt  [-config f] [-w]                     rewrite the config in canonical TOML
jul run  --serve <dir> | --proxy <target> [--listen addr]
                                              run a zero-config server (no file)
jul import nginx [-o out.toml] [-strict] <nginx.conf>
                                              translate an NGINX config (importer tag)

Flags (default command):
  --config string   path to the TOML configuration file (default "server.toml")
  --check           validate the configuration and exit
  --version         print version and exit
```

### `jul lint`

Parses and validates the configuration and additionally reports best-practice
warnings in a single pass: an unauthenticated off-loopback admin listener,
disabled compression, TLS without an explicit `min_version`, unreachable
(duplicate) locations, directory listing exposure, and servers without
locations. Each finding includes a hint. Exit codes are CI-friendly: `0` when
there are no errors, `1` on validation errors, and `2` when warnings are present
under `-strict`. Parse errors point at the offending line and column.

### `jul fmt`

Rewrites the configuration into canonical TOML. By default it prints to stdout;
`-w` writes the result back to the file. Comments and original formatting are
not preserved.

### `jul run` (zero-config)

Starts a server from a synthesized profile without any config file:

```bash
jul run --serve ./public            # serve a directory of static files
jul run --proxy 127.0.0.1:3000      # reverse-proxy everything to a backend
jul run --proxy :3000 --listen :80  # proxy to loopback :3000, listen on :80
```

Zero-config defaults enable compression and sensible timeouts. The default
listen address is `:8080`.

### `jul import`

Translates an existing NGINX configuration into Jul.IA TOML. It is gated behind
the `importer` build tag (see [Migrating from NGINX](#migrating-from-nginx)):

```bash
jul import nginx /etc/nginx/nginx.conf            # write TOML to stdout
jul import nginx -o server.toml /etc/nginx/nginx.conf
```

Every directive it cannot translate is reported with its source line, both on
stderr and as a comment header in the output, so nothing is dropped silently.
The generated config is re-parsed and validated before it is emitted. Exit
codes: `0` ok, `1` parse/translate error or invalid output, `2` warnings under
`-strict`.

The version string can be stamped at build time:

```bash
go build -ldflags "-X main.version=1.2.3" -o jul ./cmd/jul
```

### CLI troubleshooting

- **Config rejected on start or reload?** Run `jul lint -config server.toml` to
  see every error and warning at once; the running server keeps its last valid
  configuration on a failed reload.
- **Not sure a change is safe?** `jul lint` before reloading, or rely on the
  admin GUI which validates before applying.
- **Want a quick local server?** `jul run --serve .` needs no config file.

---

## Migrating from NGINX

`jul import nginx` reads an NGINX configuration file and produces an equivalent
Jul.IA TOML config. It is a **best-effort migration aid**, not a 1:1 converter:
common directives are translated, and everything it cannot map is reported with
its source line so you can port it by hand. The importer is gated behind the
`importer` build tag to keep the default binary lean:

```bash
go build -tags importer -o jul ./cmd/jul
jul import nginx -o server.toml /etc/nginx/nginx.conf
```

The generated file is re-parsed and validated exactly as the server would load
it, so a successful run always yields a config that passes `jul lint`. Run the
import, then review the `# TODO` comments at the top of the output and the
summary printed to stderr.

### What gets translated

| NGINX | Jul.IA |
| ----- | ------ |
| `http { ... }` | top-level config |
| `server { ... }` | `[[servers]]` |
| `listen 80;` / `listen 443 ssl;` | `listen = ":80"` / `":443"` + `[servers.tls]` |
| `server_name a b;` (`_` dropped) | `server_names = ["a", "b"]` |
| `root` / `index` / `try_files` | location `root` / `index` / `try_files` |
| `location / { ... }` | `[[servers.locations]]` with a `prefix` match |
| `location = /p` / `^~ /p` / `~ re` / `~* re` | `exact` / `prefix` / `regex` / `regex` match |
| `proxy_pass http://name;` | location `proxy_pass` (a bare host gets `http://`) |
| `fastcgi_pass` | location `fastcgi_pass` |
| `return 301 https://h/;` / `return 404;` | location `redirect` + `return` / `return` |
| `rewrite re repl flag;` | location `[[...rewrites]]` |
| `upstream name { server ... }` | `[[upstreams]]` with `servers` |
| `server h weight=3;` | upstream server `weight` (→ `weighted_round_robin`) |
| `least_conn;` | upstream `strategy = "least_conn"` |
| `ssl_certificate` / `ssl_certificate_key` | `[servers.tls]` `cert` / `key` |
| `ssl_protocols TLSv1.2 TLSv1.3;` | `[servers.tls]` `min_version` |
| `gzip on;` | `[compression] enabled = true` |

### What is not translated (reported, not dropped)

- **`include` directives are not followed** — import each included file
  separately, or concatenate them first.
- **Process-level directives** (`worker_processes`, `events`, `pid`, `user`, …)
  have no per-server equivalent and are ignored.
- **`stream` / `mail` modules**, `map` / `geo` / `if` blocks, named locations
  (`@name`), Lua, and any directive without a Jul.IA equivalent
  (`add_header`, `proxy_set_header`, `client_max_body_size`, `autoindex`, …)
  are listed in the report for manual porting.

See [`examples/migrate`](examples/migrate) for a sample `nginx.conf`, the config
it produces, and a walkthrough.

---

## Configuration reference

Jul.IA is configured by a single TOML document. The top-level tables are
`[global]`, `[[servers]]`, `[[upstreams]]`, `[cache]`, and `[admin]`.

A minimal, working example:

```toml
[global]
log_level = "info"
log_format = "text"
shutdown_timeout = "30s"

[[servers]]
listen = "0.0.0.0:8080"
server_names = ["localhost", "example.com"]
client_max_body_size = "1m"

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  root  = "/srv/www/example"
  index = ["index.html", "index.htm"]
  try_files = ["$uri", "$uri/", "/index.html"]

  [[servers.locations]]
  match = { type = "prefix", path = "/api/" }
  proxy_pass = "http://backend"
  cache = true

    [servers.locations.headers]
    Host = "$host"
    X-Real-IP = "$remote_addr"
    X-Forwarded-For = "$proxy_add_x_forwarded_for"

[[upstreams]]
name = "backend"
strategy = "round_robin"
servers = ["127.0.0.1:3000", "127.0.0.1:3001"]

[cache]
enabled = true
memory_max_size = "64m"
default_ttl = "60s"
stale_while_revalidate = "30s"

[admin]
enabled = true
listen = "127.0.0.1:9090"
token = "change-me"
console = true
```

### `[global]`

| Key | Type | Description |
| --- | ---- | ----------- |
| `worker_threads` | string | `"auto"` (default) or a positive integer; `auto` uses Go's `GOMAXPROCS` defaults |
| `log_level` | string | `debug`, `info` (default), `warn`, or `error` |
| `log_format` | string | `text` (human-readable) or `json` |
| `access_log` / `error_log` | string | Log destinations |
| `shutdown_timeout` | duration | Grace period to drain in-flight requests on shutdown (also bounds the HTTP/3 drain) |
| `redact_min_secret_length` | int | Shortest resolved secret value masked from logs; `0` uses the default (4). Lower it (down to 1) for short secrets, accepting possible masking of incidental log text |

Durations use Go syntax: `30s`, `5m`, `1h`. Sizes use `512k`, `1m`, `512m`, etc.

### `[[servers]]`

A virtual host bound to one listen address. Repeat the table for more listeners.

| Key | Type | Description |
| --- | ---- | ----------- |
| `listen` | string | Bind address, e.g. `0.0.0.0:8080` or `127.0.0.1:443` |
| `server_names` | []string | Host names matched against the `Host` header |
| `locations` | array | One or more `[[servers.locations]]` blocks |
| `tls` | table | TLS settings (see [TLS](#tls)) |
| `client_max_body_size` | size | Maximum request body (per-server default) |
| `max_header_bytes` | size | Maximum request header size (default 1 MiB) |
| `read_header_timeout` | duration | Time allowed to read request headers |
| `read_timeout` / `write_timeout` | duration | Hard request/response caps (off by default so SSE/WebSocket/large transfers are not severed) |
| `idle_timeout` | duration | Keep-alive idle timeout |
| `access_log` / `error_log` | string | Per-server log destinations |
| `error_pages` | table | Map of status code → file path or redirect URL |
| `redirect_https` | int | On an HTTP server, redirect to HTTPS with this status (`301` or `308`) |
| `h2c` | bool | On a plaintext listener, also accept cleartext HTTP/2 (h2c) for native gRPC clients without TLS; ignored on a TLS listener (HTTP/2 is negotiated via ALPN) |

### `[[servers.locations]]`

Each location selects requests with a `match` and applies **exactly one**
action: static (`root`), `proxy_pass`, `fastcgi_pass`, `uwsgi_pass`,
`grpc_transcode`, `redirect`/`return`, or `deny`.

**Matching:**

```toml
match = { type = "prefix", path = "/api/" }   # prefix, exact, or regex
```

**Static file serving:**

| Key | Type | Description |
| --- | ---- | ----------- |
| `root` | string | Document root directory |
| `index` | []string | Index file candidates for directory requests |
| `try_files` | []string | Fallback sequence (supports `$uri`) |
| `directory_listing` | bool | Enable auto directory index |
| `allow_hidden` | bool | Serve dotfiles |
| `cache_control` | string | `Cache-Control` header for served files |

**Reverse proxy:**

| Key | Type | Description |
| --- | ---- | ----------- |
| `proxy_pass` | string | `http://upstream-name` or a concrete `http://host:port` |
| `proxy_connect_timeout` | duration | Connection establishment timeout (default 10s) |
| `proxy_read_timeout` | duration | Per-read inactivity bound on the upstream response — the maximum gap between successive reads, covering both the headers (time-to-first-byte) and a slow-trickle body. `0` (default) leaves it unbounded. A steadily streaming response is never interrupted while data keeps flowing |
| `proxy_send_timeout` | duration | Per-write inactivity bound on sending the request to the upstream — the maximum gap between successive writes. `0` (default) leaves it unbounded |
| `grpc` | bool | Proxy `proxy_pass` as **native gRPC** over end-to-end HTTP/2 (trailers preserved, no buffering); `http://` dials the backend over cleartext HTTP/2 (h2c), `https://` over HTTP/2 with TLS — requires the `grpc` build tag |
| `headers` | table | Upstream request headers; values support `$host`, `$remote_addr`, `$scheme`, `$proxy_add_x_forwarded_for` |

**FastCGI / uWSGI:**

| Key | Type | Description |
| --- | ---- | ----------- |
| `fastcgi_pass` | string | `unix:/path.sock`, `tcp://host:port`, or `host:port` |
| `fastcgi_params` | table | Explicit CGI parameter overrides |
| `uwsgi_pass` | string | uWSGI socket address (same address forms as above) |

**gRPC transcoding** (`[servers.locations.grpc_transcode]`, `grpc` build tag):

| Key | Type | Description |
| --- | ---- | ----------- |
| `target` | string | Backend gRPC server: an upstream name or a literal `host:port` |
| `descriptor_set` | string | Path to a compiled `FileDescriptorSet` (`.pb`) describing the service |
| `use_reflection` | bool | Discover the service via gRPC server reflection instead of a descriptor file |
| `tls` | bool | Dial the backend over TLS (default plaintext h2c) |
| `preserve_proto_field_names` | bool | Emit original `snake_case` proto field names instead of `lowerCamelCase` JSON names |

**Redirect / control:**

| Key | Type | Description |
| --- | ---- | ----------- |
| `redirect` | string | Target URL (uses `return` code or 302) |
| `return` | int | Status for a redirect or bare return |
| `deny` | bool | Reject matching requests with 403 |
| `rewrites` | array | Regex rewrite rules (`pattern`, `replacement`, `flag`) |
| `cache` | bool | Enable response caching for this location (requires `[cache].enabled`) |
| `client_max_body_size` | size | Override the server default for this location |
| `rate_limit` | table | Override the global `[rate_limit]` for this location (`enabled`, `key`, `rate`, `burst`; `max_conns` is ignored) |

### `[[upstreams]]`

A named pool of backends referenced by `proxy_pass = "http://name"`.

| Key | Type | Description |
| --- | ---- | ----------- |
| `name` | string | Pool name |
| `strategy` | string | `round_robin`, `weighted_round_robin`, or `least_conn` |
| `servers` | array | Bare addresses (`"127.0.0.1:3000"`) or tables with `address` + `weight` |
| `max_fails` | int | Failures before a backend is marked unhealthy |
| `fail_timeout` | duration | How long a backend stays out of rotation |

```toml
[[upstreams]]
name = "backend"
strategy = "least_conn"
max_fails = 3
fail_timeout = "10s"
servers = [
  { address = "127.0.0.1:3000", weight = 2 },
  { address = "127.0.0.1:3001", weight = 1 },
]
```

#### `[upstreams.health_check]`

Active health checking proactively probes each backend so failures are detected
(and recoveries observed) without waiting for live traffic. A backend leaves
rotation after `unhealthy_threshold` consecutive failed probes and returns after
`healthy_threshold` consecutive successful ones; this active verdict combines
with passive (`max_fails` / `fail_timeout`) health. Probe goroutines have a
defined lifetime: they are started when a pool is built, kept running across
reloads that leave the upstream unchanged, and stopped when the upstream is
removed, reshaped, or on shutdown.

| Key | Type | Description |
| --- | ---- | ----------- |
| `enabled` | bool | Turn active probing on for this pool |
| `type` | string | `http` (default) or `tcp` |
| `path` | string | Request path for `http` probes (required) |
| `interval` | duration | Delay between probe rounds (default `5s`) |
| `timeout` | duration | Per-probe timeout; must be less than `interval` (default `2s`) |
| `healthy_threshold` | int | Consecutive successes to mark a backend healthy (default `2`) |
| `unhealthy_threshold` | int | Consecutive failures to eject a backend (default `3`) |
| `expect_status` | array | Acceptable HTTP status codes for `http` probes (default `[200]`) |
| `expect_body` | string | Optional: `http` probe body must contain this substring |

```toml
[[upstreams]]
name = "api"
servers = ["127.0.0.1:3000", "127.0.0.1:3001"]
  [upstreams.health_check]
  enabled = true
  type = "http"
  path = "/healthz"
  interval = "5s"
  timeout = "2s"
  healthy_threshold = 2
  unhealthy_threshold = 3
  expect_status = [200]
```

Metrics: `jul_upstream_healthy{pool,backend}` (1 healthy / 0 unhealthy),
`jul_upstream_probes_total{pool,result}`, and
`jul_upstream_probe_duration_seconds{pool}`.

#### `[upstreams.discovery]`

Resolve a pool's backends from an external source and refresh them live, with no
config reload. With discovery enabled the static `servers` list is optional (a
seed/fallback until the first resolve). `dns` and `dns_srv` work in every build;
`consul` and `kubernetes` require the matching build tag. See
[Service discovery](#service-discovery) for the full guide.

| Key | Type | Description |
| --- | ---- | ----------- |
| `type` | string | `static` (off), `dns`, `dns_srv`, `consul`, or `kubernetes` |
| `target` | string | `host:port` for `dns`; the SRV name for `dns_srv` |
| `refresh` | duration | Poll interval (default `30s`) |
| `[consul]` | table | `address`, `service`, `tag`, `datacenter`, `token`, `passing_only` |
| `[kubernetes]` | table | `namespace`, `service`, `port`, `api_server`, `token`, `ca_file`, `insecure_skip_tls_verify` |

```toml
[[upstreams]]
name = "api"
strategy = "round_robin"
  [upstreams.discovery]
  type = "dns"
  target = "api.internal.svc:8080"
  refresh = "15s"
```

Metrics: `jul_upstream_backends{pool}` (current backend count) and
`jul_discovery_errors_total{pool}` (failed/empty resolves; last-good kept).

### gRPC ↔ JSON transcoding (`grpc` build tag)

A location with `[servers.locations.grpc_transcode]` exposes a unary gRPC
service as a RESTful JSON API, translating each HTTP request into a gRPC call
and the protobuf reply back into JSON. Method routing follows the
`google.api.http` annotations compiled into your service, exactly like
grpc-gateway or Envoy's `grpc_json_transcoder`. The action is only available in
binaries built with `-tags grpc`; a binary without the tag refuses to start (or
hot-reload) such a config with a clear error, while `jul -check` validates the
schema in any build.

Exactly one of `descriptor_set` or `use_reflection` must be set. Generate a
descriptor set from your `.proto` files with `protoc` (the `--include_imports`
flag bundles `google/api/annotations.proto` and other dependencies):

```bash
protoc \
  --include_imports \
  --descriptor_set_out=api.pb \
  --proto_path=. \
  your/service.proto
```

```toml
[[servers.locations]]
match = { type = "prefix", path = "/v1/" }
[servers.locations.grpc_transcode]
target         = "grpc-backend"     # upstream name or host:port
descriptor_set = "/etc/jul/api.pb"
```

Path variables (`/v1/items/{id}`), the `body` mapping (a named field or `*` for
the whole message), and any leftover query parameters are all mapped onto the
request message. gRPC status codes are translated to the matching HTTP status,
and per-call results are counted in
`jul_grpc_transcode_requests_total{method,code}`.

#### Streaming methods

Set `streaming = true` to transcode the three streaming RPC kinds in addition to
unary calls:

| gRPC method kind | Request | Response |
| --- | --- | --- |
| **Unary** | one JSON object | one JSON object |
| **Server-streaming** | one JSON object | a stream of JSON frames, flushed per message |
| **Client-streaming** | a JSON array *or* newline-delimited JSON objects | one JSON object |
| **Bidirectional** | a JSON array *or* newline-delimited JSON objects | a stream of JSON frames |

Streamed responses are framed per `stream_mode`:

- `ndjson` (default) — `application/x-ndjson`, one JSON object per line.
- `sse` — `text/event-stream`, each message as a `data:` event, a terminal
  `event: end` on success, and `event: error` carrying the gRPC status if the
  stream fails after it has started.

Each frame is flushed immediately (no buffering), so server-streaming and bidi
responses arrive incrementally. The request deadline propagates to the gRPC
call, the `Authorization` header and any `Grpc-Metadata-<key>` request headers
are forwarded as gRPC metadata, and `max_message_size` (default 4 MiB) caps a
single encoded message. Streamed messages are counted in
`jul_grpc_transcode_stream_msgs_total{method,direction}`.

```toml
[servers.locations.grpc_transcode]
target           = "grpc-backend"
descriptor_set   = "/etc/jul/api.pb"
streaming        = true
stream_mode      = "ndjson"   # or "sse"
max_message_size = "4m"
```

> **Scope:** one backend address per target; streaming uses HTTP/2 request/
> response framing (NDJSON or SSE) rather than gRPC-Web. A binary built without
> `-tags grpc` still refuses such a config with a clear error.

### `[cache]`

A two-tier response cache shared across reloads.

| Key | Type | Description |
| --- | ---- | ----------- |
| `enabled` | bool | Master switch |
| `memory_max_size` | size | In-memory tier cap |
| `disk_path` | string | Enables the disk overflow tier when set |
| `disk_max_size` | size | Disk tier cap |
| `default_ttl` | duration | Used when upstream gives no explicit freshness |
| `stale_while_revalidate` | duration | Serve stale entries while refreshing asynchronously |

Per-location caching also requires `cache = true` on the location. See
[docs/cache.md](docs/cache.md) for the two-tier model, on-disk format, file
permissions (`0o600`), atomic writes, and overflow/eviction semantics.

### `[compression]`

On-the-fly response compression negotiated from the request `Accept-Encoding`
header. `gzip` is available in every build; the `br` (Brotli) and `zstd` codings
require the `brotli` and `zstd` build tags respectively. Disabled by default.

| Key | Type | Description |
| --- | ---- | ----------- |
| `enabled` | bool | Master switch (default `false`) |
| `encoders` | list | Allowed encoders in server-preference order; any subset of `gzip`, `br`, `zstd` (default `["gzip"]`) |
| `level` | int | Compression level; `0` selects each encoder's own default |
| `min_size` | size | Smallest response body that is compressed (default `1k`) |
| `types` | list | MIME allow-list; a `type/*` entry matches a whole family. Defaults to text, JSON, JS, XML, SVG, and WASM when omitted |
| `precompressed` | bool | Serve sidecar `.br`/`.gz` files for static responses when present and acceptable |

Compression is skipped for range requests, already-encoded responses, and bodies
below `min_size`. Streaming responses (SSE) and connection upgrades (WebSocket)
pass through untouched, and a `Vary: Accept-Encoding` header is always added so
caches key correctly. Requesting an encoder that is not compiled into the
running binary fails fast at startup/reload with a clear error.

### `[rate_limit]`

Token-bucket request rate limiting plus an optional per-listener concurrent-
connection cap. The global policy applies to every location; any location may
override the rate, burst, and key under `[servers.locations.rate_limit]`.
Disabled by default, and compiled into every build (no build tag).

| Key | Type | Description |
| --- | ---- | ----------- |
| `enabled` | bool | Master switch (default `false`) |
| `key` | string | Bucket identity: `ip` (client address, default), `header:<Name>`, or `jwt:<claim>` |
| `rate` | int | Sustained requests/second allowed per key |
| `burst` | int | Maximum momentary burst above `rate` (defaults to `rate`) |
| `max_conns` | int | Concurrent connections per listener; `0` = unlimited. Active only when the block is `enabled`; listener-global, so it is ignored on per-location overrides |

Rejected requests receive `429 Too Many Requests` with a `Retry-After` header in
whole seconds. The key is derived from the real transport peer address, never an
untrusted `X-Forwarded-For` value; a `header:` or `jwt:` key falls back to the
client IP when the header or claim is absent (a `jwt:<claim>` key reads a claim
validated by the location's `auth` block). Idle buckets are evicted so memory
stays bounded, and the connection cap is applied per listener ahead of the TLS
handshake. A location overrides the policy by setting
`[servers.locations.rate_limit]` with its own `rate`/`burst`/`key`.

### `[servers.locations.auth]`

Per-location access control. An optional CIDR gate runs first, then at most one
credential method. Compiled into every build (no build tag). Authentication runs
ahead of rate limiting, so a `jwt:<claim>` rate-limit key can key on a validated
token claim. Set under a location, e.g. `[servers.locations.auth]`.

| Key | Type | Description |
| --- | ---- | ----------- |
| `allow` | []string | CIDR ranges permitted to proceed. When non-empty, a client must match one |
| `deny` | []string | CIDR ranges blocked. **Deny takes precedence over allow** |

A location may then set **one** of the following sub-tables:

`[servers.locations.auth.basic]` — HTTP Basic against an `htpasswd` file:

| Key | Type | Description |
| --- | ---- | ----------- |
| `file` | string | Path to an `htpasswd` file of **bcrypt** hashes (required) |
| `realm` | string | `WWW-Authenticate` realm (default `Restricted`) |

`[servers.locations.auth.jwt]` — JWT bearer tokens validated against a JWKS endpoint:

| Key | Type | Description |
| --- | ---- | ----------- |
| `jwks_url` | string | **HTTPS** URL of the issuer's JWKS document (required); keys are cached and refreshed, with a stale-grace window during outages |
| `issuer` | string | When set, the token's `iss` claim must match |
| `audience` | string | When set, the token's `aud` claim must contain this value |
| `algorithms` | []string | Allowed signing algorithms (default `RS256/384/512`, `ES256/384/512`, `PS256/384/512`). Symmetric (`HS*`) and `none` are always rejected |

Validated claims are placed in the request context: a `jwt:<claim>` rate-limit
key on the same location keys on them.

`[servers.locations.auth.forward_auth]` — delegate the decision to an external service:

| Key | Type | Description |
| --- | ---- | ----------- |
| `url` | string | `http(s)` URL of the auth service. The request is mirrored with `X-Forwarded-Method/Uri/Host` and identity headers |
| `auth_response_headers` | []string | Response headers copied onto the upstream request on a 2xx decision (clients cannot spoof them) |

A 2xx response authorizes the request; any other status (and its body/headers)
is relayed to the client, so redirect-to-login flows work transparently.

Denied requests receive `403 Forbidden` (CIDR or forward-auth), `401 Unauthorized`
with a `WWW-Authenticate` header (Basic/JWT), or the forward-auth service's own
status. Each decision is counted in the `jul_auth_decisions_total{method,result}`
metric.

### `[admin]`

The admin/observability listener (see [below](#admin-interface--observability)).

| Key | Type | Description |
| --- | ---- | ----------- |
| `enabled` | bool | Master switch |
| `listen` | string | Bind address — keep it on loopback (e.g. `127.0.0.1:9090`) |
| `token` | string | When set, requires `Authorization: Bearer <token>` |
| `console` | bool | Serve the web console dashboard at the admin root (default `true`; requires a binary built with `-tags console`). Set `false` for the basic config page only |
| `history_dir` | string | Directory for configuration snapshots used by the console history/rollback panel (default `./jul-data/config-history`) |
| `history_keep` | int | Maximum number of configuration snapshots to retain; older ones are pruned (default `50`) |

### `[observability.tracing]`

OpenTelemetry distributed tracing (see [below](#admin-interface--observability)
for the operational overview). Disabled by default, and only active in binaries
built with `-tags otel`; an enabled block in a binary without that tag is
rejected at startup.

| Key | Type | Description |
| --- | ---- | ----------- |
| `enabled` | bool | Master switch (default `false`) |
| `exporter` | string | OTLP transport: `otlp-grpc` (default) or `otlp-http` |
| `endpoint` | string | Collector address: `host:port` for gRPC, URL/host for HTTP. Required when enabled |
| `sample_ratio` | float | Head-sampling probability for root spans, `0`..`1` (defaults to `1.0` = sample all) |
| `service_name` | string | Resource `service.name` reported to the trace backend (defaults to `jul`) |
| `insecure` | bool | Export over plaintext instead of TLS (default `false`); set `true` for a local collector without TLS |

Each request emits a server span with `proxy.roundtrip`, `upstream.request`
(one per backend attempt, so failover is visible), and `cache.lookup` child
spans, and W3C `traceparent` is propagated to upstreams so they continue the
trace. Incoming `traceparent` headers are honored, and the active trace id is
added to the access log as `trace_id`. The exporter uses TLS with the host's
root CAs by default. Tracing is configured once at boot; a reload keeps the
running tracer (the server logs a warning if the block changed) — restart to
apply tracing changes.

### `[observability.metrics]`

Tunes the Prometheus metrics exposed at the admin `/metrics` endpoint. Needs no
build tag.

| Key | Type | Description |
| --- | ---- | ----------- |
| `host_label` | bool | Add the request `Host` as the `host` label on `jul_http_requests_total` and `jul_http_request_duration_seconds` (default `false`) |

The `host` label is **off by default**: the Host header is client-controlled, so
recording it unconditionally lets a flood of distinct Host values explode metric
cardinality and exhaust scrape memory. While disabled the label is emitted with
an empty value, so every request folds into one stable series. Enable
`host_label` only when the set of hosts is bounded; on an edge that receives
arbitrary Host headers, pair it with a scrape-time `keep` relabel rule for known
hosts. The setting is read once at boot; a reload keeps the running value —
restart to apply a change.

### `[observability.access_log]`

Controls where the access log is written. Unlike tracing, this needs no build
tag and is always available. By default the access log is emitted through the
server's structured logger (the `stdout` sink), honoring `[global].log_format`.
Add the `file` sink for a dedicated, rotating access-log file and/or the
`syslog` sink for the system log.

| Key | Type | Description |
| --- | ---- | ----------- |
| `sinks` | []string | Active destinations: any of `stdout`, `file`, `syslog` (default `["stdout"]`) |
| `file` | string | Access-log file path. Required when `file` is listed; its parent directory is created if missing |
| `format` | string | Encoding of the `file` and `syslog` sinks: `text` (logfmt, default) or `json`. The `stdout` sink always follows `[global].log_format` |
| `rotate_max_mb` | int | File size in MB at which the file rotates (default `100`) |
| `rotate_keep` | int | Maximum number of rotated files to retain (default `7`) |

The `file` sink rotates by size and retains a bounded number of rotated files.
Dedicated `file`/`syslog` sinks always record at info level regardless of
`[global].log_level`, so a quieter global level never suppresses the access log.
The `syslog` sink uses the local system log (`LOG_LOCAL0`, tag `jul`) and is
**not supported on Windows** (use `file` or `stdout`). Access-log settings are
fixed at startup; changing them needs a restart.

### TLS

Configured per server block:

```toml
[[servers]]
listen = "0.0.0.0:8443"
server_names = ["example.com"]

  [servers.tls]
  enabled = true
  cert = "/etc/jul/tls/example.crt"
  key  = "/etc/jul/tls/example.key"
  min_version = "1.2"   # "1.2" or "1.3"
```

To force HTTPS, add an HTTP server block with `redirect_https = 308`.

### Mutual TLS (client certificates)

Jul can also authenticate the *client* by its certificate. With
`[servers.tls.client_auth]` it verifies the certificate a client presents
against a CA bundle during the handshake, optionally checks a CRL and a SAN
allow-list, and exposes the verified identity to upstreams as `$ssl_client_*`
proxy variables. Set `require_client_cert` on a location to return 403 when no
verified certificate is present. It is in core (no build tag).

```toml
[[servers]]
listen = "0.0.0.0:443"
server_names = ["api.example.com"]

  [servers.tls]
  enabled = true
  cert = "/etc/jul/server.crt"
  key  = "/etc/jul/server.key"

    [servers.tls.client_auth]
    mode = "require"                    # none | request | require
    ca_file = "/etc/jul/clients-ca.pem"

  [[servers.locations]]
  match = { type = "prefix", path = "/api" }
  proxy_pass = "http://127.0.0.1:9000"
  require_client_cert = true
    [servers.locations.headers]
    X-Client-CN = "$ssl_client_cn"
    X-Client-Verify = "$ssl_client_verify"
```

The full reference — modes, identity variables, CRL/SAN checks, metrics, and the
bind-time reload caveat — lives in [docs/mtls.md](docs/mtls.md), with a sample
config in [testdata/mtls.toml](testdata/mtls.toml).

### Automatic HTTPS (ACME)

Jul can obtain and renew certificates automatically from an ACME certificate
authority (Let's Encrypt by default) using the **HTTP-01** or **TLS-ALPN-01**
challenge, so you never manage `cert`/`key` files by hand. This feature is gated
behind the `acme` build tag to keep the default binary lean:

```bash
go build -tags acme -o jul ./cmd/jul
```

A binary built **without** the tag fails fast at startup with a clear message if
a config enables ACME, so the feature is never silently inert.

Configure it under `[servers.tls.acme]` on a `:443` server block, and keep a
plain `:80` block running so the challenge can be answered:

```toml
[[servers]]
listen = "0.0.0.0:443"
server_names = ["example.com", "www.example.com"]

  [servers.tls.acme]
  enabled = true
  email = "ops@example.com"        # required: ACME account contact
  ca = "letsencrypt-staging"        # default; use "letsencrypt" for production
  challenge = "http-01"             # default; or "tls-alpn-01"
  # domains = ["example.com"]       # defaults to server_names
  cache_dir = "./jul-data/certs"    # on-disk certificate cache
  # ocsp_stapling = true            # default; staple OCSP responses

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  root = "/srv/www/example"

[[servers]]
listen = "0.0.0.0:80"
server_names = ["example.com", "www.example.com"]
redirect_https = 308               # HTTP-01 challenges are answered here first
  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  root = "/srv/www/example"
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `enabled` | bool | Turn on ACME for this server block |
| `email` | string | **Required.** ACME account contact address |
| `ca` | string | `letsencrypt-staging` (default), `letsencrypt`, or a custom `https://` directory URL |
| `domains` | []string | Certificate host names; defaults to the block's `server_names` |
| `challenge` | string | `http-01` (default) or `tls-alpn-01`. `dns-01` is reserved for a future release |
| `dns_provider` | string | Reserved for a future DNS-01 release; setting it is rejected |
| `cache_dir` | string | Directory where issued certificates are cached and reused across restarts (default `./jul-data/certs`) |
| `ocsp_stapling` | bool | Staple OCSP responses onto served certificates (default `true`) |

Notes and current limits:

- The **default CA is the staging environment** (untrusted certificates,
  generous rate limits). Switch to `ca = "letsencrypt"` only after staging works
  end to end, to avoid production rate limits.
- The `http-01` challenge is answered on the plain `:80` listener; `tls-alpn-01`
  is answered on the `:443` TLS listener itself (the `acme-tls/1` protocol is
  advertised automatically), so it needs no separate `:80` block.
- **OCSP stapling** is on by default for ACME certificates: Jul fetches and
  refreshes the OCSP response in the background and staples it to handshakes. A
  failed OCSP fetch degrades gracefully — the certificate is served unstapled
  rather than breaking the handshake. Set `ocsp_stapling = false` to disable.
- A single listener address may not mix ACME and static `cert`/`key` server
  blocks; validation rejects that so the certificate source is unambiguous.
- All ACME server blocks share one issuer (one autocert manager per process):
  `email`, `ca`, `challenge`, `cache_dir`, and `ocsp_stapling` must match across
  blocks or validation rejects the apply.
- The ACME domain set is fixed at startup — enabling ACME or adding domains
  needs a restart (a hot reload that newly enables ACME is rejected safely and
  the running config keeps serving).
- The `dns-01` challenge (the only way to issue wildcard certificates) is
  reserved for a future release and rejected today with a clear error;
  `dns_provider` is likewise reserved and rejected if set.
- Certificate expiry and renewals are exported as the
  `jul_tls_cert_expiry_seconds` gauge and `jul_acme_renewals_total` counter.

See [`examples/auto-https`](examples/auto-https) for a runnable walkthrough.

---

### HTTP/3 (QUIC)

Add a `[servers.http3]` block to a **TLS-enabled** server to also serve HTTP/3
over QUIC. The HTTP/3 listener runs on the **same address over UDP**, serves the
same routes and handlers as the TCP (HTTP/1.1 + HTTP/2) listener, and shares the
same TLS certificate — static or ACME, including hot reloads. Clients are told
about it with an `Alt-Svc` response header and upgrade to h3 on a later request.

HTTP/3 is compiled in only with the **`http3` build tag** (it links the
[quic-go](https://github.com/quic-go/quic-go) library); a default binary rejects
an enabled `[servers.http3]` block at startup with a clear error.

```toml
[[servers]]
listen = "0.0.0.0:443"
server_names = ["example.com"]

  [servers.tls]
  enabled = true
  cert = "/etc/jul/tls/example.crt"
  key = "/etc/jul/tls/example.key"

  [servers.http3]
  enabled = true
  # alt_svc_max_age = 86400

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  root = "/srv/www/example"
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `enabled` | bool | Turn on the HTTP/3 (QUIC) listener for this server block |
| `alt_svc_max_age` | int | `Alt-Svc` advertisement lifetime in seconds — how long a client may keep using HTTP/3 before re-checking (default `86400`) |

Build a binary with HTTP/3 support and run it:

```bash
go build -tags http3 -o jul ./cmd/jul
./jul --config server.toml
```

Notes and current limits:

- **TLS is required.** QUIC mandates TLS 1.3, so `[servers.http3]` is only valid
  on a TLS-enabled server block (static `cert`/`key` or ACME); validation
  rejects it on a plain listener. The HTTP/3 handshake always uses TLS 1.3
  regardless of the TCP listener's `min_version`.
- **Open the UDP port.** HTTP/3 listens on UDP at the same port number as the
  TCP listener (usually `443`). Allow inbound **UDP** on that port in your
  firewall and any load balancer, in addition to the existing TCP rule.
- **Shared certificates, including renewals.** The HTTP/3 listener uses the same
  certificate provider as the TCP listener, so a static-cert reload or an ACME
  renewal applies to HTTP/3 automatically — there is no separate HTTP/3
  certificate path.
- **Alt-Svc discovery.** Because the first request from a browser always arrives
  over TCP, every HTTP/1.1/HTTP/2 response carries
  `Alt-Svc: h3=":<port>"; ma=<alt_svc_max_age>`; the client then prefers HTTP/3
  for subsequent requests for `alt_svc_max_age` seconds.
- **Streaming works; hijacking does not.** Server-Sent Events and other
  `http.Flusher`-based streaming work over HTTP/3. Connection-hijacking features
  — **WebSocket** and anything using `http.Hijacker` — are **not** supported over
  HTTP/3; such requests are served over HTTP/2 instead (clients transparently
  fall back, since WebSocket is negotiated over TCP).
- **Settings are fixed at startup.** Enabling or disabling HTTP/3 for an existing
  listener, or changing `alt_svc_max_age`, takes effect only after a restart
  (like the TLS minimum version). A hot reload still starts HTTP/3 on newly added
  listener addresses and stops it on removed ones, alongside their TCP listeners.
- **Graceful shutdown** drains in-flight HTTP/3 requests (bounded) before the UDP
  socket is released, just as the TCP listener drains its connections.
- Open HTTP/3 connections are exported as the `jul_http3_connections` gauge.

---

### `[plugins]`

> Requires a binary built with `-tags wasmplugins`. A populated `[plugins]`
> table in a binary without that tag is rejected at startup.

Each entry under `[plugins]` declares one WebAssembly module by name. Exactly one
of `path` (a `.wasm` file) or `inline` (base64-encoded module bytes) supplies the
code. Capabilities default off and are granted explicitly.

```toml
[plugins.header-inject]
path = "./plugins/header-inject.wasm"
type = "middleware"              # "middleware" (default) or "handler"
memory_limit = "16m"             # guest linear-memory cap (default 16 MiB)
timeout = "100ms"                # per-invocation deadline (default 100ms)
config = { header = "X-Plugin", value = "header-inject" }  # passed to the guest as JSON

[plugins.kv-counter]
path = "./plugins/kv-counter.wasm"
kv = true                        # grant the key/value store host functions

# fetch = true                   # grant guarded outbound HTTP (reserved; v1 host
# allowed_hosts = ["api.example.com"]  #   function is not implemented yet)
```

| Key | Meaning |
| --- | ------- |
| `path` / `inline` | Module source — supply exactly one |
| `type` | `middleware` (wraps a handler, may pass the request through) or `handler` (terminal location action) |
| `config` | String map handed to the guest as a JSON object via `get_config` |
| `memory_limit` | Guest linear-memory ceiling (default 16 MiB) |
| `timeout` | Deadline for a single invocation; the guest is torn down on overrun (default 100ms) |
| `kv` | Grant the key/value store host functions (namespaced per plugin) |
| `fetch` / `allowed_hosts` | Grant guarded outbound HTTP to the listed hosts (validated; host function reserved for a future ABI revision) |

Attach a plugin to traffic by referencing its name. Server- and location-level
`plugins = [...]` lists run as **middleware** (outermost first); a location
`plugin = "name"` is a terminal **handler** action:

```toml
[[servers]]
listen = "0.0.0.0:8080"
plugins = ["header-inject"]      # runs for every location on this server

  [[servers.locations]]
  match = { type = "prefix", path = "/api/" }
  proxy_pass = "http://backend"
  plugins = ["kv-counter"]       # additional middleware for this location

  [[servers.locations]]
  match = { type = "exact", path = "/blocked" }
  plugin = "request-block"        # terminal handler — no root/proxy_pass here
```

See [WebAssembly plugins](#webassembly-plugins) for the runtime model and the
authoring guide.

---

### `[[stream]]`

> Requires a binary built with `-tags stream`. A populated `[[stream]]` table in
> a binary without that tag is rejected at startup.

Each `[[stream]]` block is one L4 (TCP or UDP) reverse-proxy listener that
forwards raw connections — no HTTP parsing. Backends are a named `[[upstreams]]`
pool (load balancing + health checks apply) or a literal `host:port`.

```toml
# Plain TCP load balancing to an upstream pool.
[[stream]]
listen = "0.0.0.0:5432"
proxy_pass = "postgres_pool"        # named upstream or "host:port"
connect_timeout = "10s"
idle_timeout = "5m"

# TLS SNI routing (passthrough — TLS is never terminated).
[[stream]]
listen = "0.0.0.0:443"
  [stream.sni_routes]
  "api.example.com" = "api_pool"
  "db.example.com"  = "10.0.0.7:8443"
  "*"               = "default_pool"  # catch-all

# UDP relay (e.g. DNS) with PROXY protocol to the backend.
[[stream]]
listen = "0.0.0.0:53"
protocol = "udp"
proxy_pass = "dns_pool"

[[stream]]
listen = "0.0.0.0:6379"
proxy_pass = "redis_pool"
proxy_protocol = "out"              # prepend a PROXY v2 header to the backend
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `listen` | string | Bind address `host:port` (**required**) |
| `protocol` | string | `tcp` (default) or `udp` |
| `proxy_pass` | string | Default backend — a named upstream or a literal `host:port`; used when no SNI route matches (and for non-TLS / UDP streams) |
| `sni_routes` | table | TLS server-name → backend map. Enables ClientHello SNI inspection on a TCP listener and routes by host **without terminating TLS**. A `"*"` key is a catch-all that takes precedence over `proxy_pass` |
| `tls_passthrough` | bool | Informational; implied whenever `sni_routes` is set. Jul.IA never terminates TLS on a stream listener in v1 |
| `proxy_protocol` | string | HAProxy PROXY-protocol handling (TCP only): `""` (off), `"in"` (parse a header from the client), `"out"` (emit a v2 header to the backend), or `"both"` |
| `connect_timeout` | duration | Backend dial timeout (default `10s`) |
| `idle_timeout` | duration | Close a relayed connection / UDP session after this idle period (default `5m`) |
| `max_udp_sessions` | int | Cap on concurrent UDP sessions per listener (UDP only, default `10000`); at the cap a new client reclaims an idle session or is dropped, bounding resource use under a source-address flood |

Provide at least one of `proxy_pass` or `sni_routes`. UDP listeners are plain
relays: `sni_routes`, `tls_passthrough`, and `proxy_protocol` are TCP-only and
rejected on a UDP block. See [L4 stream proxy](#l4-stream-proxy) for the runtime
model.

---

## WebAssembly plugins

Jul.IA can run sandboxed extensions compiled to WebAssembly on the embedded
[wazero](https://wazero.io) runtime — pure Go, no cgo, so the binary stays a
single static file. Plugins are opt-in behind the `wasmplugins` build tag.

```bash
go build -tags wasmplugins -o jul ./cmd/jul
```

**Runtime model**

- **Two shapes.** A *middleware* plugin wraps the next handler and may either
  pass the request through (`Continue`) or short-circuit it (`Stop`, writing its
  own response). A *handler* plugin is a terminal location action.
- **Sandbox.** Each plugin runs in its own wazero runtime with a configurable
  linear-memory cap (`memory_limit`) and a per-invocation deadline (`timeout`).
  A guest that exceeds its time budget is torn down; a guest that panics is
  contained and the request fails with `500` while the server keeps serving.
- **Capabilities.** Guests get only what you grant: a namespaced key/value store
  (`kv = true`) and a reserved guarded-fetch capability. Nothing else — no file
  system, no ambient network, no host clock beyond the deadline.
- **Hot reload.** `[plugins]` participates in zero-downtime reload: modules are
  recompiled into a fresh set and swapped in atomically, with no process restart.
  A shared compilation cache keeps re-instantiation cheap.
- **Metrics.** Each invocation updates `jul_plugin_invocations_total{plugin,result}`,
  `jul_plugin_duration_seconds{plugin}`, and `jul_plugin_panics_total{plugin}`.

**Authoring**

Plugins are ordinary Go programs built for `GOOS=wasip1 GOARCH=wasm` against the
guest SDK in [`examples/plugins/sdk`](examples/plugins/sdk). The host/guest
contract is the `jul-abi/v1` ABI. A full authoring guide — the SDK surface, the
host functions, the caller-allocates buffer convention, and build commands —
lives in [docs/plugins.md](docs/plugins.md), with runnable examples and their
build script in [examples/plugins](examples/plugins).

---

## L4 stream proxy

Jul.IA can reverse-proxy raw **TCP** and **UDP** connections (`[[stream]]`)
alongside its HTTP listeners — databases, message brokers, DNS, game servers,
or any TLS service you want to route by SNI without terminating. Stream support
is opt-in behind the `stream` build tag.

```bash
go build -tags stream -o jul ./cmd/jul
```

**Runtime model**

- **Pure L4.** Bytes are relayed unchanged in both directions; there is no HTTP
  parsing. TCP connections are full-duplex; UDP listeners keep a per-client
  session keyed by source address with idle reaping.
- **Load balancing & health.** A backend is a named `[[upstreams]]` pool, so the
  pool's strategy (`round_robin` / `weighted_round_robin` / `least_conn`) and
  passive health (`max_fails` / `fail_timeout`) apply, with automatic failover
  to the next healthy backend on a dial error. A literal `host:port` becomes a
  single-backend pool.
- **SNI routing (TLS passthrough).** On a TCP listener with `sni_routes`, Jul.IA
  peeks the TLS ClientHello (without consuming it), reads the SNI host, and
  routes to the matching backend — **TLS is never terminated**, so certificates
  and keys stay on the backends. A `"*"` entry catches unmatched names.
- **PROXY protocol.** `proxy_protocol` preserves the real client address across
  the proxy hop: `"in"` parses a HAProxy v1/v2 header from the client, `"out"`
  prepends a v2 header to the backend, `"both"` does both. Implemented in-house
  (no extra dependency).
- **Hot reload.** `[[stream]]` participates in zero-downtime reload: routes are
  rebuilt and swapped atomically, new listeners are bound (with rollback if any
  bind fails), and removed listeners are drained and stopped — all without a
  process restart and without disturbing the HTTP listeners.
- **Metrics.** Active connections are exported as
  `jul_stream_active_conns{proto}` and relayed traffic as
  `jul_stream_bytes_total{proto,direction}`.

A full guide — SNI routing, PROXY protocol, UDP semantics, and worked examples
— lives in [docs/stream-proxy.md](docs/stream-proxy.md), with sample configs in
[examples/l4](examples/l4).

---

## gRPC passthrough

Beyond [gRPC ↔ JSON transcoding](#grpc-transcoding), Jul.IA can reverse-proxy
**native gRPC** unchanged: set `grpc = true` on a `proxy_pass` location and the
call is forwarded end to end over HTTP/2. This is opt-in behind the same `grpc`
build tag as transcoding.

```bash
go build -tags grpc -o jul ./cmd/jul
```

**Runtime model**

- **End-to-end HTTP/2.** The request is proxied over HTTP/2 with **trailers**
  (`grpc-status` / `grpc-message`) preserved and response buffering disabled, so
  server-, client-, and bidirectional-streaming calls flush each frame as it
  arrives instead of stalling until the call completes.
- **Backend transport from the scheme.** A `proxy_pass` of `http://…` dials the
  backend over **cleartext HTTP/2 (h2c)**; `https://…` dials over **HTTP/2 with
  TLS**. The backend may be a named `[[upstreams]]` pool or a literal
  `host:port`.
- **Load balancing & health.** The upstream pool's strategy and passive health
  checking apply. gRPC streams are not replayable, so a call is **not** retried
  against another backend mid-stream; a dial failure surfaces as a gateway
  error.
- **Cleartext inbound (h2c).** Set `h2c = true` on a plaintext `[[servers]]`
  block to accept prior-knowledge HTTP/2 from gRPC clients that connect without
  TLS, alongside HTTP/1.1. On a TLS listener this is unnecessary — HTTP/2 is
  negotiated through ALPN. (Inbound h2c uses the standard library's HTTP/2
  support and needs no extra dependency.)
- **Distinct from transcoding.** `grpc_transcode` converts REST/JSON to gRPC;
  `grpc = true` proxies real gRPC traffic untouched. Use transcoding to expose a
  gRPC backend to JSON clients; use passthrough to load-balance gRPC clients.
- **Metrics.** Each forwarded call increments `jul_grpc_proxy_streams_total`.

Sample configs live in [examples/grpc-proxy](examples/grpc-proxy) and
[testdata/grpc-proxy.toml](testdata/grpc-proxy.toml); a full guide is in
[docs/grpc-proxy.md](docs/grpc-proxy.md).

---

## Service discovery

An upstream's backends are normally a static `servers` list. With an
`[upstreams.discovery]` block, Jul.IA instead resolves them from an external
source and refreshes the pool **live** — backends come and go without a config
reload, while load balancing, passive health, and active probes keep applying.

Four providers are available:

| Type | Source | Build |
| --- | --- | --- |
| `dns` | A/AAAA records (port from config) | every build |
| `dns_srv` | SRV records (host + port + weight) | every build |
| `consul` | Consul health API | `consul` tag |
| `kubernetes` | Kubernetes EndpointSlices | `kubernetes` tag |

`dns` and `dns_srv` use the standard-library resolver and are always compiled
in. `consul` and `kubernetes` are opt-in behind build tags and query the
provider's documented REST endpoint directly — no `client-go`, no Consul SDK, so
enabling them adds **no** third-party dependency:

```bash
go build -tags "consul kubernetes" -o jul ./cmd/jul
```

A binary built without a provider's tag still parses and validates a discovery
block of that type but rejects it at load time (startup or reload) with a clear
error — the same fail-loud model as the other tagged features.

```toml
[[upstreams]]
name = "api"
strategy = "round_robin"
  [upstreams.discovery]
  type = "dns"
  target = "api.internal.svc:8080"   # the port is applied to each resolved address
  refresh = "15s"
```

Key behaviours:

- **Keep last-good.** A failed resolve, or one returning zero targets, is logged,
  counted (`jul_discovery_errors_total`), and skipped — the pool keeps its
  previous backends, so a provider blip never black-holes traffic.
- **State-preserving.** Updates merge by address+weight, so surviving backends
  keep their in-flight count and passive-failure cooldown; new backends are
  immediately picked up by active health checks.
- **Atomic reload.** An unchanged discovery block reuses the running pool and its
  discovered backends; changing any field rebuilds the pool and its refresher.

Metrics: `jul_upstream_backends{pool}` and `jul_discovery_errors_total{pool}`. A
sample config is in [testdata/discovery.toml](testdata/discovery.toml) and a full
guide — including the Consul and Kubernetes settings — is in
[docs/service-discovery.md](docs/service-discovery.md).

---

## Web application firewall

Jul.IA can run requests (and optionally responses) through a
ModSecurity-compatible web application firewall built on
[Coraza](https://github.com/corazawaf/coraza) (pure Go). The
[OWASP Core Rule Set](https://coreruleset.org/) is **embedded in the binary**, so
a SQLi/XSS/RCE ruleset is one flag away — nothing to download or mount. The WAF
is opt-in behind the `waf` build tag:

```bash
go build -tags waf -o jul ./cmd/jul
```

A lean binary still parses `[waf]` but refuses to start when it is enabled, so a
missing tag fails loudly instead of serving unprotected.

Configure it globally under `[waf]`, or per location with
`[servers.locations.waf]` (an override replaces the global policy for that
location). The smallest useful config turns on the embedded CRS:

```toml
[waf]
enabled = true
crs_enabled = true        # embedded OWASP Core Rule Set
mode = "block"            # block (default) | detect
paranoia = 1              # CRS paranoia 1-4 (higher = stricter)
```

Key behaviours:

- **Block or detect.** `mode = "block"` returns `block_status` (403 by default)
  and stops the request; `mode = "detect"` records and logs matches but lets it
  through — roll new rules out in detect mode, watch the metric, then switch to
  block.
- **Your rules too.** `directives_files` includes SecLang files and
  `inline_rules` appends a snippet, with or without the CRS; they are applied
  after the CRS so they can tune it, and the mode line is applied last so it
  always wins.
- **Body inspection.** Request bodies are buffered up to `request_body_limit`
  (128 KiB default); set `response_body_check = true` to also inspect responses.
- **Ordering.** The WAF runs after auth and rate limiting and before response
  compression, so it sees the original bodies.

Metric: `jul_waf_events_total{action,rule}` counts logged rule matches. A sample
config is in [testdata/waf.toml](testdata/waf.toml) and the full guide
(tuning, paranoia, custom rules) is in [docs/waf.md](docs/waf.md).

---

## Secrets references

Sensitive values should not be literals in `jul.toml`. Any string field accepts a
reference that is resolved at serve time (and on every reload):

| Reference | Resolves to |
| --------- | ----------- |
| `${env:NAME}` | the environment variable `NAME` |
| `${file:/path}` | the file's contents (one trailing newline trimmed) |
| `${secret:/path}` | same as `${file:}` today; reserved for a future secret-manager backend |

```toml
[admin]
enabled = true
token = "${env:JUL_ADMIN_TOKEN}"

[[upstreams]]
name = "api"
  [upstreams.discovery.consul]
  address = "consul:8500"
  service = "api"
  token = "${file:/run/secrets/consul-token}"
```

Key behaviours:

- **Resolved everywhere.** Every string field is walked recursively; a value may
  mix literal text with references (`"Bearer ${env:API_TOKEN}"`).
- **Masked in logs.** Resolved values are registered with the redactor and
  replaced with `***` wherever they would appear in a log line.
- **Fail loud.** A missing env var, an unreadable file, or an unknown scheme
  (e.g. `${vault:…}`) fails startup/reload with a joined error listing every
  problem — never a silent empty value.
- **Off the surfaces.** Disk, admin API, and Console keep the unexpanded
  references; the Console only *counts* them. `jul lint` flags literal
  `admin.token` and Consul/Kubernetes tokens.

This is core (no build tag). The full guide is in [docs/secrets.md](docs/secrets.md).

---

## Hot reload

Jul.IA reloads configuration **without dropping connections**. A reload can be
triggered three ways:

1. **Signal** — `kill -HUP <pid>` (Linux/macOS), or `systemctl reload jul`.
2. **File watch** — editing the config file on disk is detected automatically.
3. **Admin API** — `POST /reload` on the admin listener.

On reload the new config is fully validated first. **If it is invalid, the
reload is rejected and the previous configuration keeps serving** — there is no
window of downtime. The response cache and metrics counters persist across
reloads.

When a reload is triggered through the admin API, the config is additionally
preflighted — including a bind-probe of every newly added HTTP **and**
`[[stream]]` listen address — before it is written, so an apply that cannot bind
is rejected up front rather than reported as applied. See
[docs/reload-semantics.md](docs/reload-semantics.md) for the full *applied vs
serving* model.

---

## Admin interface & observability

When `[admin].enabled = true`, a **separate** HTTP listener (bound to loopback by
default) exposes operational endpoints. It must never be attached to the main
traffic listeners.

| Endpoint | Auth | Purpose |
| -------- | ---- | ------- |
| `GET /healthz` | none | Liveness probe |
| `GET /readyz` | none | Readiness probe |
| `GET /metrics` | token | Prometheus exposition format |
| `GET /api/stats` | token | Runtime metrics snapshot (JSON) backing the console dashboard |
| `POST /cache/purge` | token | Purge cached responses |
| `POST /reload` | token | Trigger a configuration reload |
| `GET /` | token (in-page) | Web console dashboard (or config GUI when the console is disabled) |
| `GET /config`, `GET /ui` | token (in-page) | Configuration GUI (always available) |
| `GET/POST /api/config*` | token | Live config view/edit backing the GUI |
| `GET /api/upstreams` | token | Live upstream pools and per-backend health |
| `GET /api/certs` | token | Configured-certificate inventory and expiry (no key material) |
| `POST /api/wizard` | token | Generate a starter configuration (serve a directory or proxy a target) |
| `GET /api/history` | token | List saved configuration snapshots, newest first |
| `GET /api/history/get?id=<id>` | token | Fetch the raw TOML of one snapshot |
| `POST /api/history/rollback` | token | Re-apply a snapshot (validates + hot-reloads) |

Authentication uses a constant-time bearer-token comparison. Set
`[admin].token` and send `Authorization: Bearer <token>`.

**Web console:** binaries built with `-tags console` serve a single-page
dashboard at the admin root (`http://127.0.0.1:9090/`) with live request,
latency, cache, and connection metrics that poll `GET /api/stats`. It is enabled
by default when admin is on; set `[admin].console = false` to serve only the
basic configuration page. Builds **without** the `console` tag serve the
configuration page at the root instead. `GET /api/stats` is always available
regardless of the build tag, so it is useful for scripted monitoring too.

The console adds operational panels alongside the dashboard:

- **Upstreams** — live pools with per-backend health, weight, and in-flight
  counts (backed by `GET /api/upstreams`).
- **Certificates** — configured-certificate inventory with expiry countdown and
  an ACME-managed marker; no private-key material is exposed
  (`GET /api/certs`).
- **History & rollback** — every successful configuration save first snapshots
  the previous TOML to `[admin].history_dir`. The History panel lists snapshots
  newest-first and offers one-click **roll back**, which re-applies the snapshot
  through the same validated write path (and hot-reloads). Snapshots are pruned
  to `[admin].history_keep`. A rollback is itself reversible (it snapshots the
  pre-rollback config first).
- **Setup wizard** — generates a starter configuration (serve a directory or
  reverse-proxy a target) via `POST /api/wizard`. The generated TOML is shown
  for review and applied through the standard validated editor path.

All mutating console APIs require the bearer token (when configured) and are
same-origin; snapshot identifiers are validated against a strict charset so the
history endpoints can never read outside `history_dir`.

**Configuration GUI:** browse to `/config` (always available, and the root when
the console is off), enter the token, and you get an editor for the running
configuration (raw TOML and a simple settings form). Saving validates,
persists, and hot-reloads.

**Prometheus metrics exposed:**

- `jul_http_requests_total`
- `jul_http_request_duration_seconds`
- `jul_http_requests_in_flight`
- `jul_cache_events_total`
- `jul_http_response_compressed_total`
- `jul_http_ratelimited_total`
- `jul_listener_conns`
- `jul_grpc_transcode_requests_total` (binaries built with `-tags grpc`)
- `jul_grpc_transcode_stream_msgs_total` (binaries built with `-tags grpc`)

Example scrape with curl:

```bash
curl -H "Authorization: Bearer change-me" http://127.0.0.1:9090/metrics
```

**Distributed tracing (OpenTelemetry):** binaries built with `-tags otel` can
emit OpenTelemetry spans for every request and export them over OTLP to a
collector such as Grafana Tempo, Jaeger, or the OpenTelemetry Collector. Tracing
is configured under `[observability.tracing]` and is **disabled by default**, so
a tracing-capable binary has zero tracing overhead until you switch it on:

```toml
[observability.tracing]
enabled      = true
exporter     = "otlp-grpc"        # or "otlp-http"
endpoint     = "localhost:4317"   # collector address (host:port for gRPC)
sample_ratio = 1.0                # head sampling probability, 0..1
service_name = "jul"              # resource service.name in your trace backend
insecure     = true               # plaintext to a local collector (TLS by default)
```

Each request starts a server span with `proxy.roundtrip`, `upstream.request`
(one per backend attempt, so failover and retries are visible), and
`cache.lookup` child spans, giving an end-to-end view of where time goes.
Incoming W3C `traceparent` headers are honored so Jul.IA joins an existing
distributed trace rather than starting a new one, and `traceparent` is
propagated to upstreams so they continue the same trace. The exporter uses TLS
with the host's root CAs by default; set `insecure = true` for a local
collector without TLS. The active trace id is also added to the structured
access log as `trace_id`, so a log line can be pivoted straight to its trace. A
binary built **without** the `otel` tag rejects an enabled tracing block at
startup, and tracing settings are read once at boot: a reload keeps the running
tracer (and logs a warning if the block changed) — restart to change them.

> Builds without `-tags otel` have no OpenTelemetry code or dependencies
> compiled in at all.

**Access logs (pluggable sinks):** every request is recorded as a structured
access line. By default it goes to the server's structured logger (the `stdout`
sink, honoring `[global].log_format`). Configure `[observability.access_log]` to
fan the same record out to additional sinks — a dedicated rotating `file` and/or
`syslog` — each encoded as `text` (logfmt) or `json`:

```toml
[observability.access_log]
sinks         = ["stdout", "file"]        # any of stdout, file, syslog
file          = "/var/log/jul/access.log"
format        = "json"                    # text (default) or json for file/syslog
rotate_max_mb = 100                       # rotate by size
rotate_keep   = 7                         # rotated files to keep
```

The `file` sink rotates by size and prunes old files automatically. Dedicated
file/syslog sinks always record regardless of `[global].log_level`, so raising
the global level to quiet diagnostics never drops the access log. `syslog` uses
the local system log and is not supported on Windows (use `file`). No build tag
is required, and access-log settings are read once at boot — changing them needs
a restart.

---

## Running real applications behind Jul.IA

The [`examples/`](examples/) folder contains runnable, end-to-end demos of
serving apps in several languages behind Jul.IA:

| Example | What it shows |
| ------- | ------------- |
| [`examples/python-proxy`](examples/python-proxy) | A WSGI app served over HTTP via `proxy_pass` |
| [`examples/python-uwsgi`](examples/python-uwsgi) | A WSGI app via the uWSGI protocol (`uwsgi_pass`) — Linux/WSL/Docker |
| [`examples/node-proxy`](examples/node-proxy) | A dependency-free Node.js server behind `proxy_pass` |
| [`examples/go-proxy`](examples/go-proxy) | A Go `net/http` server behind `proxy_pass` |
| [`examples/rust-proxy`](examples/rust-proxy) | A std-only Rust HTTP server behind `proxy_pass` |

Each example has its own `jul.toml`, app source, and a short `README.md` with
step-by-step run instructions. The pattern is the same: start the backend app,
then start Jul.IA pointing at that example's config
(`jul --config examples/<name>/jul.toml`).

> **Important:** any backend placed behind Jul.IA must handle concurrent and
> keep-alive connections. Single-threaded dev servers (e.g. stock `wsgiref`)
> can deadlock on persistent connections — use a threaded/async server such as
> waitress, gunicorn, uvicorn, or the native servers in the Node/Go/Rust
> examples.

---

## Deployment

> See [docs/deployment.md](docs/deployment.md) for the full guide: the
> **editable** vs **read-only** deployment shapes, the canonical directory
> layout, ownership, container volumes, and what writes where (config, history,
> disk cache, ACME cache, logs).

### As a Linux systemd service

Two hardened units are provided: [`deploy/systemd/jul.service`](deploy/systemd/jul.service)
(**editable** — the admin console can apply config changes and roll back) and
[`deploy/systemd/jul-readonly.service`](deploy/systemd/jul-readonly.service)
(**read-only** — the config is immutable).

```bash
sudo cp jul /usr/local/bin/
sudo install -D -m600 server.toml /etc/jul/server.toml
sudo cp deploy/systemd/jul.service /etc/systemd/system/

sudo systemctl daemon-reload
sudo systemctl enable --now jul

# Reload config without downtime:
sudo systemctl reload jul
```

The editable unit runs as a dynamically-allocated unprivileged user with
`CAP_NET_BIND_SERVICE` (so it can bind ports 80/443) and a strict hardening
profile. systemd creates and owns the four writable directories
(`/etc/jul`, `/var/lib/jul`, `/var/cache/jul`, `/var/log/jul`) via
`ConfigurationDirectory`/`StateDirectory`/`CacheDirectory`/`LogsDirectory`, so
point `acme.cache_dir`, the disk cache, the access-log file sink, and
`history_dir` at them (see [deployment.md](docs/deployment.md#directory-layout)).
The unit also raises `LimitNOFILE` for high socket fan-out, caps `TasksMax`, and
trips a `StartLimitBurst` crash-loop guard; optional `MemoryMax`/`CPUQuota` caps
are included commented-out for you to tune to the host.

### As a Windows service

Use [`deploy/windows/install-service.ps1`](deploy/windows/install-service.ps1)
from an elevated PowerShell prompt. It registers the service under the
least-privilege virtual account `NT SERVICE\jul` and creates an ACL'd data
directory:

```powershell
# Run as Administrator
.\install-service.ps1 `
  -BinaryPath 'C:\Program Files\jul\jul.exe' `
  -ConfigPath 'C:\ProgramData\jul\server.toml' `
  -DataDir    'C:\ProgramData\jul'
Start-Service jul
```

Jul.IA detects when it is launched by the Windows Service Control Manager and
runs under the service protocol automatically.

### With Docker

A multi-stage [`Dockerfile`](Dockerfile) builds a minimal distroless image with
a static binary running as a non-root user. The writable paths (`/etc/jul`,
`/var/lib/jul`, `/var/cache/jul`, `/var/log/jul`) are declared as volumes; mount
named volumes to persist config, history, and the ACME certificate cache:

```bash
docker build --build-arg VERSION=0.1.0 -t jul .

# Editable: named volumes (seeded from the image's baked server.toml on first use)
docker run --rm -p 8080:8080 -p 8443:8443 \
  -v jul-config:/etc/jul -v jul-state:/var/lib/jul \
  -v jul-cache:/var/cache/jul -v jul-log:/var/log/jul \
  jul

# Read-only config: bind-mount your config file read-only
docker run --rm -p 8080:8080 -p 9090:9090 \
  -v "$PWD/server.toml:/etc/jul/server.toml:ro" \
  -v jul-cache:/var/cache/jul \
  jul
```


---

## Building from source & cross-compiling

Requires Go 1.26+.

```bash
# Native build
go build -o jul ./cmd/jul

# Run tests
go test ./...
```

**Stability gates.** Two opt-in soak scenarios (behind the `soak` build tag) drive
sustained load through real proxy data paths and assert the process stays bounded
— no request errors, a steady goroutine count, and bounded heap growth (a leak
gate, per [ADR 0005](docs/adr/0005-soak-post-ga-gate.md)). Run both with
[`scripts/soak.sh`](scripts/soak.sh) (or `make soak`); pick one with
`SOAK_SCENARIO`:

```bash
make soak                                   # both scenarios, 30s each
SOAK_SCENARIO=udp-churn make soak           # only the UDP source-address churn soak
SOAK_DURATION=5m SOAK_WORKERS=32 make soak  # release-style run
```

| Scenario | Asserts |
| -------- | ------- |
| `proxy` | Sustained concurrent HTTP through a reverse-proxy handler: zero request errors, steady goroutines/heap |
| `udp-churn` | Sustained UDP source-address churn through a stream listener: live sessions stay capped at `max_udp_sessions`, every reaped/evicted session tears down fully (see [stream-proxy.md](docs/stream-proxy.md#soak-testing)) |

**Optional build tags** enable heavier features that are left out of the default
binary. Combine them as needed:

| Tag | Enables |
| --- | ------- |
| `brotli` | Brotli response compression (the `br` content-coding) |
| `zstd` | Zstandard response compression |
| `acme` | Automatic HTTPS (ACME / Let's Encrypt) |
| `console` | Web console dashboard at the admin root (live metrics UI) |
| `otel` | OpenTelemetry distributed tracing (OTLP export) |
| `grpc` | gRPC ↔ JSON transcoding locations (`grpc_transcode`) and native gRPC passthrough (`grpc = true`) |
| `http3` | HTTP/3 over QUIC listeners (`[servers.http3]`) |
| `importer` | `jul import nginx` config migration tool |
| `wasmplugins` | WebAssembly plugins (`[plugins]`) on the embedded wazero runtime |
| `waf` | Web application firewall (`[waf]`) on the Coraza engine with the embedded OWASP CRS |
| `stream` | L4 TCP/UDP stream proxy (`[[stream]]`) with SNI routing and PROXY protocol |
| `consul` | Consul service discovery for upstreams (`[upstreams.discovery]` `type = "consul"`) |
| `kubernetes` | Kubernetes EndpointSlice service discovery (`[upstreams.discovery]` `type = "kubernetes"`) |

```bash
# Full-featured build
go build -tags "brotli zstd acme console otel" -o jul ./cmd/jul
```

**Cross-compile** (Go produces a single static binary per platform — the target
machine needs nothing installed):

```bash
# Linux amd64 (static)
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o jul ./cmd/jul

# Windows arm64
GOOS=windows GOARCH=arm64 go build -o jul.exe ./cmd/jul

# macOS Apple Silicon
GOOS=darwin GOARCH=arm64 go build -o jul ./cmd/jul
```

The single static, `CGO_ENABLED=0` binary is a deliberate design constraint.
Jul.IA stays Go-first and admits native (Rust) code only at the edges — as
sandboxed WebAssembly plugins or, where justified, an opt-in out-of-process
sidecar — never via cgo in the core. The reasoning and the per-feature rule are
recorded in [docs/adr/0001-language-strategy.md](docs/adr/0001-language-strategy.md).

**Build all release archives at once** with the bundled script
([`scripts/build-release.ps1`](scripts/build-release.ps1)):

```powershell
./scripts/build-release.ps1 -Version 0.1.0
```

This produces `.zip` (Windows) and `.tar.gz` (Linux/macOS) archives in `dist/`
for amd64/arm64 across all three operating systems, each bundling the binary,
`server.toml`, and the matching deploy asset.

---

## Project layout

```text
cmd/jul/            Entry point (CLI + Windows service integration)
internal/
  admin/            Admin listener, config GUI, API
  cache/            Two-tier response cache (memory + disk)
  config/           TOML schema, parser, validation, hot reload
  handler/          Static, proxy, FastCGI/uWSGI handlers; error pages
  middleware/       Request ID, recover, logging, timeouts, body limits
  observability/    Structured logging + Prometheus metrics
  router/           Host/location matching and dispatch
  server/           Listener lifecycle, reload orchestration, TLS, hardening
  signals/          Cross-platform signal handling
  transcode/        gRPC ↔ JSON transcoding engine (grpc build tag)
  upstream/         Backend pools, balancing strategies, health checks
deploy/             systemd unit and Windows service installer
examples/           Runnable language-integration demos
scripts/            Release build script
testdata/           Sample configs and static assets for tests
```

---

## Troubleshooting

**`invalid configuration` on start or reload** — run `jul --config <file>
--check` to see the validation error. A populated `[[stream]]` or `[plugins]`
table is rejected unless the binary was built with its build tag (`stream` and
`wasmplugins` respectively); the `[[mail]]` table is reserved and always
rejected.

**502 / 504 from a proxied app** — confirm the backend is running, reachable at
the configured address, and able to serve concurrent keep-alive connections (see
the warning under [Running real applications](#running-real-applications-behind-julia)).

**Admin endpoints return 401** — set `[admin].token` and send
`Authorization: Bearer <token>`. In the GUI, enter the token when prompted.

**macOS blocks the binary** — because the release binaries are not notarized,
Gatekeeper quarantines them. Clear it with:

```bash
xattr -d com.apple.quarantine ./jul
```

**Privileged ports (80/443) on Linux** — either run via the provided systemd
unit (which grants `CAP_NET_BIND_SERVICE`) or grant the capability manually:
`sudo setcap cap_net_bind_service=+ep /usr/local/bin/jul`.
