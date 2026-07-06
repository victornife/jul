# Jul.IA

<p align="center">
  <img src="docs/assets/logo.png" alt="Jul.IA logo" width="320" />
</p>

[![Status](https://img.shields.io/badge/status-active-brightgreen.svg)](https://github.com/victornife/jul)
[![CI](https://github.com/victornife/jul/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/victornife/jul/actions/workflows/ci.yml)
[![Go Version](https://img.shields.io/badge/go-1.26-blue.svg)](https://github.com/victornife/jul/blob/main/go.mod)
[![License](https://img.shields.io/badge/license-AGPL--3.0-blue.svg)](https://github.com/victornife/jul#license)
[![Codecov](https://codecov.io/gh/victornife/jul/graph/badge.svg?branch=main)](https://codecov.io/gh/victornife/jul)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](https://github.com/victornife/jul)

**Jul.IA** is an NGINX-inspired HTTP edge server written in Go and configured
entirely through TOML. It bundles reverse-proxying with load balancing, static
file serving, FastCGI/uWSGI application gateways, a two-tier response cache, TLS
termination, hot configuration reload, and a built-in admin/observability
interface — all in a single static, dependency-free binary.

- **Binary / module / service name:** `jul`
- **Product name:** `Jul.IA`
- **Language:** Go 1.26
- **License:** AGPL-3.0

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

## Feature maturity

The canonical maturity matrix lives in [`docs/status.md`](docs/status.md). At a
 glance:

| Maturity | Features |
|----------|----------|
| **GA** | Core HTTP, TLS & ACME, Authentication, mTLS, Console, Active health checks, WAF, Rate limiting, Compression, OTel tracing, Response cache |
| **GA — soak pending** | gRPC transcoding + passthrough, Service discovery, Secrets references, Zero-config + `jul lint`, NGINX importer, HTTP/3, WASM plugins, L4 stream proxy |

> See [`docs/status.md`](docs/status.md) for the full GA criteria matrix and
> per-feature evidence links. The soak test is the last remaining gate for the
> GA — soak pending features per [ADR 0005](docs/adr/0005-soak-post-ga-gate.md).
> A consolidated **Phase 2A** 8-hour soak (2026-07-05, 2.12M req, 0% err)
> promoted TLS, mTLS, auth, health checks, WAF, rate-limit, compression,
> OTel tracing, and response cache to **GA**.

Several GA -- soak pending features require an opt-in **build tag** (e.g. `grpc`, `acme`,
`wasmplugins`, `stream`, `http3`, `waf`, `consul`, `kubernetes`). The default
`lean` binary ships only the GA surface plus core compression (`gzip`). Build
with `-tags "…"` or download the `full` release profile to enable everything.

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

> Note: The `[[stream]]` (L4 proxy) table is active in binaries built with
> the `stream` tag; in a binary without that tag a populated `[[stream]]` table
> is rejected at startup. The `[plugins]` table is active in binaries built with
> the `wasmplugins` tag; in a binary without that tag a populated `[plugins]`
> table is rejected at startup. The `[waf]` table (and per-location `waf`
> override) is active in binaries built with the `waf` tag; in a binary without
> that tag an enabled WAF config is rejected at startup.

---

## Performance & benchmarks

Jul.IA ships with an in-tree benchmark suite covering the hot path (routing, TLS, auth, proxy, cache, gRPC, and HTTP/3). For how to run benchmarks, what each measures, and tuning recommendations (connection pooling, cache sizing, compression levels, worker limits), see **[docs/benchmarks.md](docs/benchmarks.md)**.

---

## Installation

Download the archive for your platform from a [release](https://github.com/victornife/jul/releases)
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
| Windows (Intel/AMD 64-bit) | [`jul_<version>_windows_amd64_full.zip`](https://github.com/victornife/jul/releases) |
| Windows (ARM64) | [`jul_<version>_windows_arm64_full.zip`](https://github.com/victornife/jul/releases) |
| Linux (Intel/AMD 64-bit) | [`jul_<version>_linux_amd64_full.tar.gz`](https://github.com/victornife/jul/releases) |
| Linux (ARM64) | [`jul_<version>_linux_arm64_full.tar.gz`](https://github.com/victornife/jul/releases) |
| macOS (Apple Silicon) | [`jul_<version>_darwin_arm64_full.tar.gz`](https://github.com/victornife/jul/releases) |
| macOS (Intel) | [`jul_<version>_darwin_amd64_full.tar.gz`](https://github.com/victornife/jul/releases) |

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
jul check -config server.toml
```

Print the version:

```bash
./jul --version
```

---

## Command-line usage

```text
jul [flags]                                   run the server (default)
jul check [-config f] [-json] [-quiet]        full runtime preflight check
jul lint [-config f] [-strict] [-json] [-quiet]
                                              validate + best-practice checks
jul fmt  [-config f] [-w]                     rewrite the config in canonical TOML
jul run  --serve <dir> | --proxy <target> [--listen addr]
                                              run a zero-config server (no file)
jul import nginx [-o out.toml] [-strict] <nginx.conf>
                                              translate an NGINX config (importer tag)

Legacy flags (default command, still supported):
  --config string   path to the TOML configuration file (default "server.toml")
  --check           validate the configuration and exit  (prefer "jul check")
  --version         print version and exit
```

### `jul check`

Performs a **full runtime preflight**: it validates structurally *and* dry-runs
every component that could fail during serve/reload (WAF rule compilation, auth
initialisation, compression encoder availability, plugin compile, etc.).  This
is stronger than `jul lint`, which only checks schema and best-practice
warnings.  Use `check` in CI before deploying, or locally when you want
certainty that the binary you built can actually start with this config.

```bash
jul check -config server.toml
jul check -config server.toml -json   # machine-readable output
```

Exit codes: `0` ok, `1` validation or runtime error.  The legacy `--check` flag
on the default command (`jul -check`) is equivalent but `jul check` is the
canonical subcommand.

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
shutdown_timeout = "30s"

[[servers]]
listen = "0.0.0.0:8080"
server_names = ["localhost", "example.com"]

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
stale_if_error = "300s"

[admin]
enabled = true
listen = "127.0.0.1:9090"
token = "change-me"
console = true
```

The full **configuration reference** — every key, type, default, and example —
lives in [`docs/configuration.md`](docs/configuration.md) so it can be updated
independently and deep-linked.

Key sections covered there:

- [`[global]`](docs/configuration.md#global) — worker threads, logging, shutdown
- [`[[servers]]`](docs/configuration.md#servers) — listeners, TLS, timeouts
- [`[[servers.locations]]`](docs/configuration.md#serverslocations) — matching, static files, proxy, auth, rate limits
- [`[[upstreams]]`](docs/configuration.md#upstreams) — load balancing, health checks, service discovery
- [`[cache]`](docs/configuration.md#cache) — two-tier cache, stale-while-revalidate, stale-if-error
- [`[compression]`](docs/configuration.md#compression) — gzip, Brotli, Zstd
- [`[rate_limit]`](docs/configuration.md#rate_limit) — token-bucket limiting
- [`[admin]`](docs/configuration.md#admin) — admin listener and console
- [`[observability.*]`](docs/configuration.md#observabilitytracing) — tracing, metrics, access logs
- [TLS](docs/configuration.md#tls) — static certificates, mTLS
- [ACME](docs/configuration.md#automatic-https-acme) — Let's Encrypt automation
- [HTTP/3](docs/configuration.md#http3-quic) — QUIC listener
- [`[plugins]`](docs/configuration.md#plugins) — WASM plugins
- [`[[stream]]`](docs/configuration.md#stream) — L4 TCP/UDP proxy
