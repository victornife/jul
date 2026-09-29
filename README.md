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

**Jul.IA** is a self-contained edge and protocol gateway written in Go and
configured through TOML. It combines reverse proxying, load balancing, static
serving, FastCGI/uWSGI, HTTP and gRPC gateway behavior, optional L4 proxying,
TLS/mTLS, policy, observability, configuration lifecycle, and an embedded
operations Console in a single static, dependency-free binary.

- **Binary / module / service name:** `jul`
- **Product name:** `Jul.IA`
- **Language:** Go 1.26
- **License:** AGPL-3.0

> Current product maturity and delivery are governed by
> [`docs/feature-status.yaml`](docs/feature-status.yaml) and rendered in
> [`docs/status.md`](docs/status.md). Volatile execution state lives in the
> [master programme](https://github.com/victornife/jul/issues/62); the
> [roadmap](docs/roadmap/) keeps the durable portfolio sequence. Dated audit
> disposition lives in the [audit register](docs/audit-register.md), while the
> underlying audits remain historical evidence. The permanent OSS/open-core
> boundary is defined in [ADR 0012](docs/adr/0012-oss-open-core-boundary.md).
>
> New to HTTP, proxies, TLS, caching, or observability? The
> [concepts appendix](docs/vision/appendix.md) walks through how a request travels
> through modern edge infrastructure from first principles.

---

## Who is Jul.IA for?

**Jul.IA is for:** solo operators and small infrastructure teams who want
NGINX-grade routing and reverse proxying — plus protocol gateway capabilities
(gRPC transcoding, L4 TCP/UDP proxying) — in a single binary, configured in
TOML, without NGINX's operational complexity. Operators who want a built-in
observability console without standing up a separate tool. Teams migrating from
NGINX who want a gentler on-ramp.

**Jul.IA is not the right tool if you need:**
- **Fleet or multi-node management** — Jul.IA runs on one node; fleet
  control planes are a demand-gated future milestone.
- **Kubernetes Ingress or Gateway API** — no K8s controller ships today;
  use NGINX Ingress, Envoy Gateway, or Traefik.
- **Full service mesh or xDS** — use Envoy, Istio, or Linkerd.
- **Multi-tenant edge / SaaS CDN** — Jul.IA has no tenant isolation layer.
- **Envoy-level extensibility or ecosystem** — Jul.IA's plugin system is
  WASM-based and purpose-fit, not a general xDS/filter chain.

---

## Current direction

Jul.IA published the bounded standalone gateway as stable v2.0.0.
This tree is the proposed v2.1.0 candidate: Waves 1–6 and the audit fixes are
included, still at their own maturity, and not yet a published GitHub Release.
#434 is no-go for this release and stays CANDIDATE/LATER. Certification is
[release closure #480](https://github.com/victornife/jul/issues/480).
Additional features do not inherit the GA status of older capabilities. Fleet, hosted
cloud, GraphQL composition and AI remain optional horizons or bounded
experiments; none is required for the single-node product to remain useful. See the
[operating model](docs/operating-model.md),
[Core Gateway Completeness](docs/specs/core-gateway-completeness.md), and
[roadmap](docs/roadmap/README.md).

---

## Feature maturity

Jul.IA tracks **maturity** and **delivery** separately. The canonical machine
record is [`docs/feature-status.yaml`](docs/feature-status.yaml); the checked
human view and evidence matrix are in [`docs/status.md`](docs/status.md).

| Classification | Current examples |
| --- | --- |
| **GA · soaked** | Core HTTP, released TLS/ACME and mTLS, authentication, cache, compression, rate limiting, health checks, service discovery, released gRPC/L4/WASM/WAF/observability capabilities, Console, secrets, reload transaction, trusted client address, backend TLS trust, zero-config/lint and the base single-file NGINX importer. The last two have [scoped post-release soak evidence](docs/soak-evidence.md#2026-09-28--feature-specific-exact-head-one-hour-soaks-pr-482-head-9ab87c1--criterion-5-met-for-scoped-features). |
| **Beta · released in v2.0.0** | Auxiliary egress policy, method/header/query routing, response-header policy and CORS, upstream admission/retry/circuit controls, configuration authority/generated contracts, NGINX assessment/provenance/include traversal, diagnostics, admin TLS, external API/CLI, selected hot reload and Unix HTTP upstreams |
| **Beta · in the proposed v2.1.0 tree, not yet published** | gRPC Health/Check probes, resource and storage headroom, serving WAF policy visibility, Console diagnostics guidance, WASM ABI v2 response phase, consistent-hash affinity, client-certificate policy hot reload, and the post-v2.0.0 NGINX translation/E2E evidence |

Published stable
[`v2.0.0`](https://github.com/victornife/jul/releases/tag/v2.0.0)
remains the latest GitHub Release until v2.1.0 is published. The older
[`v2.0.0-rc.1` checkpoint](docs/release-candidates/v2.0.0-rc.1.md) remains a
separate prerelease. A Beta capability does not become GA because it is
included in a stable tag. Check the [status matrix](docs/status.md) for each
capability's maturity and delivery.

The response-cache correction programme is complete: #134 recertified the
feature and the existing released cache record retains GA. Newer additions keep
their own rows so they do not inherit that maturity implicitly.

Many features require an opt-in **build tag** (for example `grpc`, `acme`,
`wasmplugins`, `stream`, `http3`, `waf`, `consul`, or `kubernetes`). The default
`lean` binary ships the core surface plus gzip. Build with `-tags "..."` or use
a `full` release artifact to enable all optional capabilities.

---

## Features

| Area | Capability |
| ---- | ---------- |
| **Static files** | Document root serving, index files, `try_files`, optional directory listing, hidden-file control, `Cache-Control` headers |
| **Reverse proxy** | `proxy_pass` to a concrete URL or a named upstream; named upstreams may use `unix:/path.sock` backends for plaintext HTTP; per-location connect/read/send timeouts; custom upstream headers with variable expansion |
| **WebSocket & SSE** | Transparent passthrough of `Connection: Upgrade` (HTTP `101`) connections — text and binary frames spliced bidirectionally (Apollo GraphQL subscriptions, Socket.IO) — and `text/event-stream` / chunked responses streamed per write, never buffered (Node/Python SSE) |
| **Load balancing** | `round_robin`, `weighted_round_robin`, `least_conn`, and deterministic `consistent_hash` affinity (client IP, header or cookie key) across an upstream pool. `consistent_hash` is Beta and is in the proposed v2.1.0 tree, not in published v2.0.0 |
| **Health & failover** | Released passive/active health checking and Beta resilience controls: bounded admission and pending work, per-backend capacity, retry attempts/deadline/backoff/budget, and an explicit closed/open/half-open circuit model. The gRPC Health/Check probe is Beta and is in the proposed v2.1.0 tree, not in published v2.0.0. See [upstreams.md](docs/upstreams.md) and [health.md](docs/health.md). |
| **Service discovery** | Resolve an upstream's backends dynamically and refresh the pool live without a reload (`[upstreams.discovery]`): **DNS** A/AAAA and **DNS SRV** in every build, plus **Consul** and **Kubernetes** EndpointSlices behind the `consul`/`kubernetes` build tags — failed or empty resolves keep the last-good backends |
| **Backend trust** | One normalized `backend_tls` policy for private/system roots, backend client certificates, SNI/verified names, minimum TLS and peer identities across HTTP, native gRPC, transcoding/reflection and active health probes. GA (#409). |
| **Upstream resilience** | Pool-scoped admission and retry budget state, location-overridable stateless controls, bounded queueing, protocol-aware lifetime accounting, and one per-backend circuit state machine reused by HTTP, gRPC, FastCGI/uWSGI and L4 TCP. Integrated closure remains tracked separately. |
| **App gateways** | `fastcgi_pass` and `uwsgi_pass` accept literal targets or named upstream pools and share load balancing, health, failure accounting and admission; TCP and Unix-socket backends are supported. |
| **gRPC transcoding** | Expose a gRPC service as a RESTful JSON API via `google.api.http` annotations (`grpc_transcode`) — unary and streaming (server/client/bidi, NDJSON or SSE) — from a compiled descriptor set or server reflection, opt-in `grpc` build tag |
| **gRPC passthrough** | Reverse-proxy **native gRPC** end to end over HTTP/2 (`grpc = true`) — trailers preserved, streaming frames flushed immediately, load balancing and health checks applied — with cleartext **h2c** inbound (`h2c = true`) for clients without TLS, opt-in `grpc` build tag |
| **Response cache** | Recertified two-tier memory/disk cache with shared-cache validation, conservative authenticated reuse, invalidation, TTL/stale controls, conditional requests, and exact/all purge. The released cache record retains GA — see [docs/cache.md](docs/cache.md). |
| **Compression** | On-the-fly `gzip` (every build) plus `br`/`zstd` codings (via the `brotli`/`zstd` build tags); `Accept-Encoding` negotiation, `Cache-Control: no-transform`, MIME allow-list, size threshold, and precompressed `.br`/`.gz` sidecars |
| **Rate limiting** | Token-bucket request limiting keyed by client IP, a request header, or a JWT claim, with burst, global or per-location policy, and `429` + `Retry-After`; plus a per-listener concurrent-connection cap |
| **Trusted client identity** | Per-listener `trusted_proxies` and bounded Forwarded/X-Forwarded-For derivation produce one canonical client address used by CIDR auth, rate limiting, WAF, logs, forwarding and FastCGI. Untrusted assertions are ignored. GA (#409). |
| **Access control** | Per-location CIDR allow/deny lists and at most one credential method — HTTP Basic (bcrypt `htpasswd`), JWT bearer tokens validated against a JWKS endpoint (asymmetric algorithms only, `none` rejected), or forward-auth to an external service; a CIDR-only gate is valid |
| **WAF** | ModSecurity-compatible web application firewall ([Coraza](https://github.com/corazawaf/coraza)) with the **OWASP Core Rule Set embedded** in the binary (`[waf]`, global or per-location): `block`/`detect` modes, paranoia levels, your own SecLang files or inline rules, request/response body inspection, and a `jul_waf_events_total` metric — opt-in `waf` build tag ([docs/waf.md](docs/waf.md)) |
| **Secrets references** | Keep credentials out of the config file: any string field accepts `${env:NAME}`, `${file:/path}`, or `${secret:/path}` references resolved at serve time, eligible resolved values receive best-effort log redaction (default minimum length: four characters), and `jul lint` flags literal admin/Consul/Kubernetes tokens — core, no build tag ([docs/secrets.md](docs/secrets.md)) |
| **Egress allow-list** | Optional hardening (`[egress]`) that constrains the server's own config-driven fetches — JWKS, forward-auth, Consul/Kubernetes discovery, ACME/OCSP, and the WASM plugin `fetch` intersection — to an approved set of hosts/CIDRs, refused at connect time; bounds the SSRF blast radius of a misconfigured or compromised config, disabled by default — core, no build tag ([docs/egress.md](docs/egress.md)) |
| **TLS** | TLS 1.2/1.3 termination per server block, configurable minimum version, optional HTTP→HTTPS redirect |
| **Automatic HTTPS** | ACME certificate issuance and auto-renewal using the configured exclusive HTTP-01 or TLS-ALPN-01 challenge, with on-disk account/certificate cache — opt-in `acme` build tag |
| **HTTP/3** | HTTP/3 over QUIC on the same address (UDP), sharing the complete server TLS/mTLS policy and certificate provider, advertised through `Alt-Svc`; static certificate-file changes remain restart-bound — opt-in `http3` build tag |
| **Routing & response policy** | Deterministic exact/prefix/regex precedence, method/header/query predicates, rewrites, ordered response-header add/set/remove operations, and bounded CORS/preflight handling. The predicate/response-policy additions shipped in v2.0.0 as Beta. |
| **Virtual hosts** | Multiple `server_names` per listener; multiple listen addresses |
| **Limits & timeouts** | `client_max_body_size`, header size caps, read/write/idle/header timeouts (per-server, location overrides for body size) |
| **Redirects** | `return`, `redirect`, and `deny` (403) location actions; custom error pages |
| **Configuration lifecycle** | Transactional zero-downtime reload, strict preflight, planned-restart staging, history/rollback, and explicit `managed` versus `file_owned` authority with drift/adoption semantics. Field-level lifecycle truth is generated from the Go registry. |
| **Observability** | Structured logging (text/JSON), pluggable access-log sinks (file/syslog with rotation), Prometheus metrics, OpenTelemetry tracing, health/readiness probes |
| **Diagnostics** | Read-only `jul doctor` checks with deterministic human/JSON output, plus operator-triggered, bounded, secret-safe local support bundles with no automatic upload ([docs/diagnostics.md](docs/diagnostics.md)) |
| **Admin GUI** | Loopback-bound web console with live metrics, upstream/certificate status, config history and rollback, setup and structured editors. Legacy shared-token mode remains available; opt-in local multi-principal RBAC with predefined/custom roles, scoped revocable tokens and per-principal audit attribution is shipped (`console` build tag). External OIDC/SAML/SCIM identity is not shipped. |
| **Developer experience** | Zero-config `jul run --serve`/`--proxy` (no file needed), `jul lint` best-practice checks with CI-friendly exit codes, and `jul fmt` canonical formatting |
| **Generated contracts** | Deterministic JSON Schema, machine metadata, generated field reference and lifecycle reference derive from code-defined authorities; durable `route_id` supports stable route addressability. Released in v2.0.0 with separate Beta maturity. |
| **Migration** | `jul import nginx` can convert supported NGINX constructs and emit deterministic human/JSON assessment with blocking/approximate findings, source provenance, guidance, and opt-in bounded root-confined include traversal — opt-in `importer` build tag. |
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

From the extracted folder (or the repo root if running from source), create a
page and serve it on loopback:

**Windows (PowerShell):**

```powershell
New-Item -ItemType Directory -Force public | Out-Null
Set-Content public/index.html '<h1>Hello from Jul.IA</h1>'
.\jul.exe run --serve .\public --listen 127.0.0.1:8080
```

**Linux / macOS:**

```bash
mkdir -p public
printf '<h1>Hello from Jul.IA</h1>\n' > public/index.html
./jul run --serve ./public --listen 127.0.0.1:8080
```

**From source (Go installed):**

```bash
mkdir -p public
printf '<h1>Hello from Jul.IA</h1>\n' > public/index.html
go run ./cmd/jul run --serve ./public --listen 127.0.0.1:8080
```

Open <http://127.0.0.1:8080/>. The bundled `server.toml` is a configuration
template: its `/srv/www/example` static root and backend addresses must be
adapted to your machine before starting it. See [Getting started](docs/getting-started.md)
for a complete first config.

After adapting the template and creating its static directory, validate it
without starting the server:

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
jul serve [-config f]                         run the server (explicit form)
jul check [-config f] [-json] [-quiet]        full runtime preflight check
jul doctor [-config f] [-json] [-strict] [-check-network]
                                              run read-only local diagnostics
jul support-bundle [-config f] [-output file] [-json] [-include-logs]
                                              write a bounded local diagnostic archive
jul healthcheck [-config f] [-addr h:p | -url u] [-ready] [-timeout d] [-json] [-quiet]
                                              probe a running server's health endpoint
jul lint [-config f] [-strict] [-json] [-quiet]
                                              validate + best-practice checks
jul fmt  [-config f] [-w] [-diff]             rewrite the config in canonical TOML
jul run  --serve <dir> | --proxy <target> [--listen addr]
                                              run a zero-config server (no file)
jul import nginx [-o out.toml] [-strict] <nginx.conf>
                                              translate an NGINX config (importer tag)
jul version [-json]                           print version and build metadata
jul completion <bash|zsh|fish|powershell>     print a shell completion script

Legacy flags (default command, still supported; deprecated — prefer the subcommands above):
  --config string   path to the TOML configuration file (default "server.toml")
  --check           validate the configuration and exit  (prefer "jul check")
  --version         print version and exit               (prefer "jul version")
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

### `jul doctor`

Runs deterministic, read-only checks for strict configuration parsing, semantic validation, configured files and certificates, admin exposure, bounded topology, and process/build state. The default run is network-free; `-check-network` explicitly enables bounded runtime preflight and immediate-close listener bind probes. Use `-json` for the versioned machine contract and `-strict` to make warnings fail CI.

```bash
jul doctor -config server.toml
jul doctor -config server.toml -json -strict
```

Exit codes: `0` no errors, `1` one or more diagnostic errors, and `2` invalid usage or warnings under `-strict`. See [docs/diagnostics.md](docs/diagnostics.md).

### `jul support-bundle`

Creates an owner-only, bounded `tar.gz` containing a versioned manifest, safe build/configuration metadata, and the in-process `jul doctor` report. It never uploads automatically, never accepts arbitrary include paths, and excludes raw configuration, private keys, environment dumps, request/response bodies, and traffic captures. Logs are opt-in and limited to a bounded tail of the configured Jul access-log file.

```bash
jul support-bundle -config server.toml -output jul-support.tar.gz
jul support-bundle -config server.toml -include-logs -json
```

Review every bundle before sharing it. See [docs/diagnostics.md](docs/diagnostics.md) for archive layout, limits, privacy guarantees, and limitations.

### `jul healthcheck`

Probes a **running** server's admin health endpoint and exits with a
deterministic status, so it can drive container, systemd, and Kubernetes
liveness/readiness checks — including from a shell-less distroless image where
`curl`/`wget` are unavailable. It reads the `[admin] listen` address from the
config (or takes `-addr`/`-url`) and GETs `/healthz` (liveness) or, with
`-ready`, `/readyz` (readiness). The admin listener must be enabled.

```bash
jul healthcheck                                       # discover [admin] listen from server.toml
jul healthcheck -config /etc/jul/server.toml -ready   # readiness probe
jul healthcheck -addr 127.0.0.1:9090 -quiet           # exit code only, no output
jul healthcheck -url http://127.0.0.1:9090/healthz -json
```

Exit codes: `0` healthy (endpoint returned `2xx`), `1` unhealthy (non-`2xx`, or
the server was unreachable / timed out), `2` usage or config error (bad flags,
unreadable config, or the admin listener is disabled). The health verdict is
strictly `0`/`1`, so the command is safe to use directly in a Docker
`HEALTHCHECK` — see [deployment.md](docs/deployment.md#health-checks).

### `jul lint`

Parses and validates the configuration and additionally reports best-practice
warnings in a single pass: an unauthenticated off-loopback admin listener,
disabled compression, TLS without an explicit `min_version`, unreachable
(duplicate) locations, directory listing exposure, and servers without
locations. Each finding includes a hint. Exit codes are CI-friendly: `0` when
there are no errors, `1` on validation errors, and `2` when warnings are present
under `-strict`. Parse errors point at the offending line and column.

### `jul fmt`

Validates and rewrites the configuration into canonical TOML. Invalid known
values are rejected before anything is printed or written. By default it prints
to stdout; `-w` writes the result back to the file. `-diff` shows a unified diff
of the changes without writing: exits 0 when nothing would change, 1 when
changes are needed (useful for CI enforcement). Comments and original formatting
are not preserved.

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

### `jul version`

Prints the version and build metadata. Human-readable by default; `-json` emits
a stable machine object (keys: `product`, `version`, `commit`, `build_date`,
`dirty`, `go_version`, `os`, `arch`) for scripts and CI.

```bash
jul version           # human-readable
jul version -json     # machine-readable object
```

The `version` string is stamped by the release pipeline (and `make build`); the
commit, build date, and dirty flag are read from the Go build info the toolchain
embeds automatically, so they populate for any `go build` from the repository and
degrade to `unknown` when VCS metadata is absent. To stamp a custom version:

```bash
go build -ldflags "-X main.version=1.2.3" -o jul ./cmd/jul
```

### `jul completion`

Generates a shell completion script for `bash`, `zsh`, `fish`, or `powershell`
(`pwsh`). Source it for the current session, or install it into the shell's
completion directory:

```bash
source <(jul completion bash)                                # bash, current session
jul completion zsh  > "${fpath[1]}/_jul"                     # zsh, installed
jul completion fish > ~/.config/fish/completions/jul.fish    # fish
jul completion powershell | Out-String | Invoke-Expression   # PowerShell
```

Completion covers the subcommand verbs and the arguments of `completion` and
`version`; file paths complete elsewhere.

### CLI troubleshooting

For a full incident playbook — startup, reloads and restart-required changes,
service discovery, plugins, and soak interpretation — see
[docs/troubleshooting.md](docs/troubleshooting.md).

- **Config rejected on start or reload?** Run `jul lint -config server.toml` to
  see every error and warning at once; the running server keeps its last valid
  configuration on a failed reload.
- **Not sure a change is safe?** `jul lint` before reloading, or rely on the
  admin GUI which validates before applying.
- **Want a quick local server?** `jul run --serve .` needs no config file.

---

## Migrating from NGINX

The `importer` build tag enables a **best-effort migration aid**, not an
NGINX-equivalence certificate. It classifies source directives as translated,
approximated, ignored or blocking, with source locations and guidance. Review
those findings and validate the generated Jul configuration before cutover.

```bash
go build -tags importer -o jul ./cmd/jul
./jul import nginx --assess --follow-includes --root /etc/nginx /etc/nginx/nginx.conf
./jul import nginx --follow-includes --root /etc/nginx \
  --input /etc/nginx/nginx.conf --output server.toml \
  --report migration-assessment.json
./jul check -config server.toml
```

Include traversal is **off by default** and requires `--follow-includes`; the
explicit root confines both direct files and symlink targets. Do not treat a
successful parse or generated TOML as proof of traffic equivalence. Inspect
blocking and approximate findings, then test the Jul candidate against the
behaviour your NGINX estate actually uses. The assessment may exit with code
`3` when manual action is required (as the sample fixture does); resolve that
result before cutover. The assessment schema and exact directive boundaries are
in the [importer guide](docs/nginx-importer.md)
and [migration assessment](docs/nginx-assessment.md); the [migration corpus](docs/nginx-migration-corpus.md)
records the tested scenarios and remaining differences.

Published v2.0.0 includes the base importer and the Beta assessment/provenance/include
surface. The proposed v2.1.0 tree adds the later bounded stream, protocol and
affinity translations and focused NGINX-versus-Jul tests. Those translations
remain approximated or blocking where the guides say so; they are not universal
equivalence. Use the [status matrix](docs/status.md) and the installed binary's
version when assessing a specific deployment. A sample input and walkthrough
live under [`examples/migrate`](examples/migrate).

## Configuration reference

Jul.IA is configured by a single TOML document. The top-level tables are
`[global]`, `[[servers]]`, `[[upstreams]]`, `[cache]`, and `[admin]`.

An illustrative config template (create `/srv/www/example`, start backends on
ports 3000 and 3001, and set `JUL_ADMIN_TOKEN` in the server process environment
before running it; adapt paths, hosts and credentials to your deployment):

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
token = "${env:JUL_ADMIN_TOKEN}"
console = true
```

The full **configuration reference** — every key, type, default, and example —
lives in [`docs/configuration.md`](docs/configuration.md) so it can be updated
independently and deep-linked. An exhaustive, generated field-by-field
reference (regenerated from the schema, never hand-edited) is also available:
[`docs/generated/config-reference.md`](docs/generated/config-reference.md),
[`docs/generated/config.schema.json`](docs/generated/config.schema.json) (JSON
Schema 2020-12), and [`docs/generated/config-metadata.json`](docs/generated/config-metadata.json).

Key sections covered there:

- [`[global]`](docs/configuration.md#global) — worker threads, logging, shutdown
- [`[[servers]]`](docs/configuration.md#servers) — listeners, TLS, timeouts
- [`[[servers.locations]]`](docs/configuration.md#serverslocations) — matching, static files, proxy, auth, rate limits
- [`[[upstreams]]`](docs/configuration.md#upstreams) — load balancing, health checks, service discovery
- [`[cache]`](docs/configuration.md#cache) — two-tier cache, stale-while-revalidate, stale-if-error
- [`[compression]`](docs/configuration.md#compression) — gzip, Brotli, Zstd
- [`[rate_limit]`](docs/configuration.md#rate_limit) — token-bucket limiting
- [`[admin]`](docs/configuration.md#admin) — admin listener and console
- [`[observability.*]`](docs/configuration.md#observabilitytracing) — tracing, metrics, and explicitly enableable/disableable access logs
- [TLS](docs/configuration.md#tls) — static certificates, mTLS
- [ACME](docs/configuration.md#automatic-https-acme) — Let's Encrypt automation
- [HTTP/3](docs/configuration.md#http3-quic) — QUIC listener
- [`[plugins]`](docs/configuration.md#plugins) — WASM plugins
- [`[[stream]]`](docs/configuration.md#stream) — L4 TCP/UDP proxy
