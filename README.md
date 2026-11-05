# Jul.IA

<p align="center">
  <img src="docs/assets/logo.png" alt="Jul.IA — Build Anything. Share It on Your Terms." width="320" />
</p>

[![Status](https://img.shields.io/badge/status-active-brightgreen.svg)](https://github.com/victornife/jul)
[![Tests & CI](https://github.com/victornife/jul/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/victornife/jul/actions/workflows/ci.yml)
[![CodeQL](https://github.com/victornife/jul/actions/workflows/github-code-scanning/codeql/badge.svg?branch=main)](https://github.com/victornife/jul/actions/workflows/github-code-scanning/codeql)
[![Go Version](https://img.shields.io/badge/go-1.27-blue.svg)](https://github.com/victornife/jul/blob/main/go.mod)
[![License](https://img.shields.io/badge/license-AGPL--3.0-blue.svg)](https://github.com/victornife/jul#license)
[![Codecov](https://codecov.io/gh/victornife/jul/graph/badge.svg?branch=main)](https://codecov.io/gh/victornife/jul)
[![Snyk security](https://snyk.io/test/github/victornife/jul/badge.svg)](https://snyk.io/test/github/victornife/jul)
[![Maintainability](https://qlty.sh/gh/victornife/projects/jul/maintainability.svg)](https://qlty.sh/gh/victornife/projects/jul)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](https://github.com/victornife/jul)

**Jul.IA** is a portable, open-source application and protocol gateway written
in Go and configured through TOML. It brings routing, protection, traffic
controls and an embedded operations Console together in a single static
binary. You can use it to serve websites and manage requests to applications
you already run, in your homelab, on your own server or in the cloud.

## Why Jul.IA?

AI is making software creation faster and more personal. More people can build
useful tools for their own needs, teams and communities.

Sharing them can still become a second infrastructure project. Making an
application reachable, controlling access and keeping it running can demand
more time, expertise and expense than expected. Even a small application may
need advanced protection and traffic controls.

Our mission is to give people and teams the freedom to run and share their own
software, with advanced capabilities and control over their infrastructure and
dependencies, while reducing unnecessary cost and complexity.

**[Our vision](docs/vision/README.md)** ·
**[Examples](docs/examples/README.md)** ·
**[Practical guide](docs/practical-guide.md)** ·
**[Get started](docs/getting-started.md)**

The [brief statement](docs/vision/brief.md) introduces our principles and the
sharing experience we are building toward. The standalone gateway is available
today; an integrated workflow for publishing, invitations and group access is
our longer-term ambition.

---

- **Binary / module / service name:** `jul`
- **Product name:** `Jul.IA`
- **Language:** Go 1.27
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

**Jul.IA is for:** individuals, homelab operators, communities and teams who
want advanced gateway capabilities on infrastructure they choose. It can be
useful when you need routing, access policy, traffic controls, protocol
gateways and operational visibility together, even for a small application.
It can work alongside connectivity tools such as Cloudflare Tunnel and
Tailscale, and help teams assess and migrate supported NGINX configurations.

Choose it when that combination fits your needs and reduces the components
you have to operate. Existing gateways and platforms may already be enough;
our [practical guide](docs/practical-guide.md#how-we-compare-and-combine-with-other-tools)
explains the alternatives and responsibilities that remain.

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

**Where we are today:** Jul.IA is a complete, standalone single-node application
and protocol gateway (latest stable release:
[`v2.2.0`](https://github.com/victornife/jul/releases/tag/v2.2.0)). Its
[core gateway foundation](docs/specs/core-gateway-completeness.md) is in place:
HTTP/1.1–HTTP/3 serving, TLS/ACME and mTLS, trusted client and backend
identities, routing and response policy, upstream resilience, authentication,
WAF, caching, rate limiting, native gRPC and L4 proxying, WASM plugins,
transactional configuration management, NGINX migration assessment, and an
embedded operations Console. On `main`, post-`v2.2.0` work has also landed the
runtime foundation for application sharing (declared route access intent,
audience-change checks, and in-place route withdrawal with active-stream
retirement), alongside targeted additions such as single-provider Console OIDC,
large-object and range-aware caching, and offered-ALPN TCP passthrough routing.

Two operational programmes have since completed on `main`:

- **Cache operations ([#620](https://github.com/victornife/jul/issues/620); children #621–#624)** — scoped invalidation, bounded cache inspection and decision explanations, route-scoped cache controls, and explicit cache handling during route/backend changes.
- **WAF and security operations ([#629](https://github.com/victornife/jul/issues/629), #630–#634)** — bounded outcome investigation, fixture-based policy verification and recovery, scoped rule exceptions, posture/isolation evidence, and security alerting delivered through [#628 Milestone B](https://github.com/victornife/jul/issues/628).

These operational additions are **Beta and unreleased**; completion does not
promote them to GA or soak-certified status. The established base WAF and
response cache retain their separate maturity entries in the
[feature status matrix](docs/status.md).

**Where we are going:** The strategic focus is making powerful gateway controls
understandable and dependable for people and teams running and sharing several
useful applications on one machine—bridging the gap between a capable standalone
gateway and an intentional sharing experience. The completed WAF and cache
programmes are delivered capabilities, not active roadmap tracks. Current
sharing work and remaining operational candidates are tracked separately in
[#62](https://github.com/victornife/jul/issues/62).

- **Application sharing ([#579](https://github.com/victornife/jul/issues/579), [contract](docs/specs/application-sharing.md))** —
  Verify and document how public, private-network (Tailscale), restricted-browser
  (Cloudflare Tunnel/Access and self-hosted login), and local-preview services
  coexist safely on one instance; prove recovery and portability through
  [reproducible application recipes](https://github.com/victornife/jul/issues/613);
  then use that evidence to guide sharing, access-summary, and withdrawal
  workflows in the Console before exploring provider-backed audience and
  invitation management. The next Tailscale composition, [#582](https://github.com/victornife/jul/issues/582),
  is ready but requires an authorized disposable tailnet and intended/excluded
  clients.
- **Cross-Console explanations ([#625](https://github.com/victornife/jul/issues/625))** —
  A candidate, not started. Cache-specific decision and cutover explanations
  have shipped; this issue remains for the broader shared explanation catalog.
- **HTTP download bandwidth controls ([#626](https://github.com/victornife/jul/issues/626))** —
  A separate candidate, not started; it is distinct from request-rate limiting
  and cache operations.
- **External alert recipes ([#628](https://github.com/victornife/jul/issues/628))** —
  Security milestone B is delivered. Operational milestone A, covering core
  availability/certificate/backend/storage alerts, remains open.

The candidate items remain unselected; selection and sequencing for open work
stay in the programme tracker.

**What stays out of scope:** Correctness and security fixes always take priority
over feature work, and new capabilities earn their own maturity rather than
inheriting GA status from older features. Multi-node fleet control planes,
hosted cloud services, Kubernetes controllers, service meshes, GraphQL
composition, and AI gateways remain optional horizons or separately gated
experiments—none is required for the single-node product to remain complete and
useful on its own.

**Where to check more details:**

- **[Master programme tracker (#62)](https://github.com/victornife/jul/issues/62)** —
  live backlog, active workstreams, issue sequencing, effort estimates, and blockers.
- **[Roadmap](docs/roadmap/README.md)** and **[Operating model](docs/operating-model.md)** —
  durable portfolio sequence, investment lanes, and prioritization rules.
- **[Public vision](docs/vision/README.md)** and **[Application sharing programme (#579)](https://github.com/victornife/jul/issues/579)** —
  product direction and how the gateway connects to the longer-term sharing workflow.
- **[Feature status & GA matrix](docs/status.md)** ([`docs/feature-status.yaml`](docs/feature-status.yaml)), **[Release verification](docs/release.md#published-v220-verification)**, and **[Known limitations](docs/known-limitations.md)** —
  per-feature maturity (`GA`, `Beta`, `Alpha`) and delivery state, release/soak evidence, and current technical constraints.

---

## Feature maturity

Jul.IA tracks **maturity** and **delivery** separately. The canonical machine
record is [`docs/feature-status.yaml`](docs/feature-status.yaml); the checked
human view and evidence matrix are in [`docs/status.md`](docs/status.md).

| Classification | Current examples |
| --- | --- |
| **GA · soaked** | Core HTTP, released TLS/ACME and mTLS, authentication, cache, compression, rate limiting, health checks, service discovery, released gRPC/L4/WASM/WAF/observability capabilities, Console, secrets, reload transaction, trusted client address, backend TLS trust, zero-config/lint and the base single-file NGINX importer. The last two have [scoped post-release soak evidence](docs/soak-evidence.md#2026-09-28--feature-specific-exact-head-one-hour-soaks-pr-482-head-9ab87c1--criterion-5-met-for-scoped-features). The bounded routing/CORS, resilience and auxiliary-egress contracts have their own [eight-hour v2.2.0 evidence](docs/reviews/2026-10-14-ga-soak-590-results.md). |
| **GA · soak pending** | None currently recorded in the manifest. |
| **Beta · released in v2.0.0** | Configuration authority/generated contracts, NGINX assessment/provenance/include traversal, diagnostics, admin TLS, external API/CLI, selected hot reload and Unix HTTP upstreams |
| **Beta · released in v2.1.0** | gRPC Health/Check probes, resource and storage headroom, serving WAF policy visibility, Console diagnostics guidance, WASM ABI v2 response phase, consistent-hash affinity, client-certificate policy hot reload, and the post-v2.0.0 NGINX translation/E2E evidence |
| **Beta · released in v2.2.0** | Downstream write-inactivity timeout and bounded per-route metrics/charts; publication does not complete their open GA criteria |

Published stable
[`v2.2.0`](https://github.com/victornife/jul/releases/tag/v2.2.0)
is the latest GitHub Release. The older
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
| **Load balancing** | `round_robin`, `weighted_round_robin`, `least_conn`, and deterministic `consistent_hash` affinity (client IP, header or cookie key) across an upstream pool. `consistent_hash` is Beta, released in v2.1.0 |
| **Health & failover** | Released passive/active health checking and GA/soaked resilience controls: bounded admission and pending work, per-backend capacity, retry attempts/deadline/backoff/budget, and an explicit closed/open/half-open circuit model. The gRPC Health/Check probe remains Beta, released in v2.1.0. See [upstreams.md](docs/upstreams.md) and [health.md](docs/health.md). |
| **Service discovery** | Resolve an upstream's backends dynamically and refresh the pool live without a reload (`[upstreams.discovery]`): **DNS** A/AAAA and **DNS SRV** in every build, plus **Consul** and **Kubernetes** EndpointSlices behind the `consul`/`kubernetes` build tags — failed or empty resolves keep the last-good backends |
| **Backend trust** | One normalized `backend_tls` policy for private/system roots, backend client certificates, SNI/verified names, minimum TLS and peer identities across HTTP, native gRPC, transcoding/reflection and active health probes. GA (#409). |
| **Upstream resilience** | Pool-scoped admission and retry budget state, location-overridable stateless controls, bounded queueing, protocol-aware lifetime accounting, and one per-backend circuit state machine reused by HTTP, gRPC, FastCGI/uWSGI and L4 TCP. Integrated closure remains tracked separately. |
| **App gateways** | `fastcgi_pass` and `uwsgi_pass` accept literal targets or named upstream pools and share load balancing, health, failure accounting and admission; TCP and Unix-socket backends are supported. |
| **gRPC transcoding** | Expose a gRPC service as a RESTful JSON API via `google.api.http` annotations (`grpc_transcode`) — unary and streaming (server/client/bidi, NDJSON or SSE) — from a compiled descriptor set or server reflection, opt-in `grpc` build tag |
| **gRPC passthrough** | Reverse-proxy **native gRPC** end to end over HTTP/2 (`grpc = true`) — trailers preserved, streaming frames flushed immediately, load balancing and health checks applied — with cleartext **h2c** inbound (`h2c = true`) for clients without TLS, opt-in `grpc` build tag |
| **Response cache** | Recertified two-tier memory/disk cache with shared-cache validation, conservative authenticated reuse, invalidation, TTL/stale controls, conditional requests, opt-in per-key cold-miss fill locking (`[cache].lock`), disk-streamed large-object capture and serving (`[cache].max_entry_size`), opt-in Option A range-aware caching (`[cache].range_enabled`), and exact/all purge. The released cache record retains GA — see [docs/cache.md](docs/cache.md). |
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
| **Routing & response policy** | Deterministic exact/prefix/regex precedence, method/header/query predicates, rewrites, ordered response-header add/set/remove operations, and bounded CORS/preflight handling. The existing predicate/response-policy contract is GA/soaked for v2.2.0. |
| **Virtual hosts** | Multiple `server_names` per listener; multiple listen addresses |
| **Limits & timeouts** | `client_max_body_size`, header size caps, read/write/idle/header timeouts (per-server, location overrides for body size) |
| **Redirects** | `return`, `redirect`, and `deny` (403) location actions; custom error pages |
| **Configuration lifecycle** | Transactional zero-downtime reload, strict preflight, planned-restart staging, history/rollback, and explicit `managed` versus `file_owned` authority with drift/adoption semantics. Field-level lifecycle truth is generated from the Go registry. |
| **Observability** | Structured logging (text/JSON), pluggable access-log sinks (file/syslog with rotation), Prometheus metrics, OpenTelemetry tracing, health/readiness probes |
| **Diagnostics** | Read-only `jul doctor` checks with deterministic human/JSON output, plus operator-triggered, bounded, secret-safe local support bundles with no automatic upload ([docs/diagnostics.md](docs/diagnostics.md)) |
| **Admin GUI** | Loopback-bound web console with live metrics, upstream/certificate status, config history and rollback, setup and structured editors. Legacy shared-token mode remains available; opt-in local multi-principal RBAC with predefined/custom roles, scoped revocable tokens and per-principal audit attribution is shipped (`console` build tag). External Console [OIDC login](docs/admin-oidc.md) is an unreleased Beta addition, with existing RBAC, bounded sessions and local-token recovery. SAML/SCIM are not shipped. |
| **Developer experience** | Zero-config `jul run --serve`/`--proxy` (no file needed), `jul lint` best-practice checks with CI-friendly exit codes, and `jul fmt` canonical formatting |
| **Generated contracts** | Deterministic JSON Schema, machine metadata, generated field reference and lifecycle reference derive from code-defined authorities; durable `route_id` supports stable route addressability. Released in v2.0.0 with separate Beta maturity. |
| **Migration** | `jul import nginx` can convert supported NGINX constructs and emit deterministic human/JSON assessment with blocking/approximate findings, source provenance, guidance, and opt-in bounded root-confined include traversal — opt-in `importer` build tag. |
| **WebAssembly plugins** | Sandboxed request middleware and handlers compiled to WASM and run on the embedded [wazero](https://wazero.io) runtime (pure Go, no cgo): per-plugin memory and time limits, panic isolation, capability-gated key/value store, hot-reloadable — opt-in `wasmplugins` build tag |
| **L4 stream proxy** | TCP and UDP reverse proxying (`[[stream]]`) with load balancing and health checks across an upstream pool, TLS **SNI routing** by host without terminating, and HAProxy **PROXY protocol** v1/v2 (in and out) to preserve the client address — survives hot reload, opt-in `stream` build tag |
| **Portability** | Single static binary, no runtime dependencies; Windows, Linux, and macOS on amd64/arm64 |

The [offered-ALPN TCP routing extension](docs/stream-alpn.md) (#434) is Alpha,
merged via [PR #635](https://github.com/victornife/jul/pull/635) and unreleased,
with separate [coverage and verification evidence](docs/audit/434-alpn/README.md).
It routes client offers while backends retain TLS negotiation.

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
go run ./cmd/jul version-key init
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

This section covers local commands. The remote administration commands, including
the WAF event query, are documented in [Remote CLI](docs/remote-cli.md).

```text
jul [flags]                                      run the configured server (default)
jul serve [-config FILE] [-effective-version-key-file PATH]
                                                 explicit configured-server form
jul version-key init [-file PATH]               provision the private version key
jul check [-config FILE] [-json] [-quiet] [-skip-static-roots]
                                                 structural and stateless runtime checks
jul doctor [-config FILE] [-json] [-strict] [-check-network]
           [-timeout D] [-per-check-timeout D]   read-only local diagnostics
jul support-bundle [-config FILE] [-output FILE] [-json] [-check-network]
                   [-include-logs] [limits]      write a bounded diagnostic archive
jul healthcheck [-config FILE] [-addr HOST:PORT | -url URL] [-ready]
                [-timeout D] [-ca-file FILE] [-client-cert FILE -client-key FILE]
                [-json] [-quiet]                 probe a running server
jul lint [-config FILE] [-strict] [-json] [-quiet]
                                                 validate and lint configuration
jul fmt [-config FILE] [-w] [-diff]              format TOML
jul run --serve DIR | --proxy TARGET [--listen ADDR]
        [-effective-version-key-file PATH]        run a zero-config server
jul import nginx [options] CONFIG                 translate or assess NGINX config
jul version [-json]                              print version/build metadata
jul capabilities [-json]                         report compiled features and exit codes
jul posture [-config FILE] [-json] [-timeout D]  evaluate local security posture
jul waf evaluate, manifest, or verify-assets       evaluate or verify local WAF policy
jul completion <bash|zsh|fish|powershell>         generate shell completions
```

### Serving and effective-version key

A private effective-version key is required before **every serving mode**:
the default <code>jul</code> command, <code>jul serve</code>, <code>jul run</code>,
and Windows service startup. Provision it once with
<code>jul version-key init</code>, then preserve the same protected file across
restarts and upgrades. Jul does not create or repair the key during server
startup.

<code>--effective-version-key-file PATH</code> is available on the default
server command, <code>jul serve</code>, and <code>jul run</code>. It takes
precedence over <code>JUL_EFFECTIVE_VERSION_KEY_FILE</code>, which takes
precedence over the platform default. <code>jul version-key init --file PATH</code>
selects the path to initialize; without <code>--file</code>, it uses the
environment override or platform default. See
[Effective-version key provisioning and recovery](docs/effective-versions.md).

The default command keeps these compatibility flags:

| Flag | Effect |
| --- | --- |
| <code>-config FILE</code> | Read the TOML configuration; defaults to <code>server.toml</code>. |
| <code>-effective-version-key-file PATH</code> | Select the protected effective-version key for startup. |
| <code>-check</code> | Validate the configuration and exit. Prefer <code>jul check</code>. |
| <code>-version</code> | Print version and exit. Prefer <code>jul version</code>. |

<code>jul serve</code> accepts <code>-config</code> and
<code>-effective-version-key-file</code>. It is the explicit form of the default
server command.

### jul version-key init

Initializes a protected, cryptographically random 32-byte key file. It does not replace an existing file.
Keep the file private and back it up securely; losing it changes the effective
version context.

| Flag | Meaning |
| --- | --- |
| <code>-file PATH</code> | Key file to initialize. Defaults to <code>JUL_EFFECTIVE_VERSION_KEY_FILE</code> or the platform path. |

Exit codes: <code>0</code> initialized, <code>1</code> initialization or path
error, <code>2</code> invalid usage.

### jul check

Checks configuration structure, stateless runtime prerequisites, and configured
static-root directories. It does **not** build all serving handlers, bind the
actual listeners, or check live backends; a successful check does not guarantee
that startup will succeed. Use it as an early validation step, not as a full
startup rehearsal.

| Flag | Meaning |
| --- | --- |
| <code>-config FILE</code> | Configuration file; defaults to <code>server.toml</code>. |
| <code>-json</code> | Emit the result as JSON. |
| <code>-quiet</code> | Suppress non-error output. |
| <code>-skip-static-roots</code> | Do not open configured static-content directories. |

Exit codes: <code>0</code> checks passed, <code>1</code> configuration/runtime
check failed, <code>2</code> invalid flags or usage. The legacy
<code>jul -check</code> form remains supported.

### jul doctor

Runs deterministic, read-only checks for configuration, configured files and
certificates, admin exposure, bounded topology, and build/process state. It is
network-free by default. <code>-check-network</code> opts into bounded secret
resolution, stateless runtime preflight, and immediate-close local listener
bind probes. Even with that option, doctor does not build every serving handler
or probe live backends; start Jul and exercise routes to establish deployment
readiness. See [Diagnostics and support bundles](docs/diagnostics.md).

| Flag | Default | Meaning |
| --- | --- | --- |
| <code>-config FILE</code> | <code>server.toml</code> | Configuration file. |
| <code>-json</code> | off | Emit the versioned JSON report. |
| <code>-strict</code> | off | Exit 2 when warning results are present. |
| <code>-check-network</code> | off | Enable bounded network-capable checks and local bind probes. |
| <code>-timeout D</code> | <code>20s</code> | Total diagnostic timeout. |
| <code>-per-check-timeout D</code> | <code>5s</code> | Timeout for each check. |

Exit codes: <code>0</code> no errors (warnings allowed unless strict),
<code>1</code> one or more diagnostic error results or output failure, <code>2</code> invalid usage or warnings
under <code>-strict</code>.

### jul support-bundle

Writes a bounded, secret-safe <code>tar.gz</code> archive locally. It never
uploads automatically. Logs are opt-in and limited to a tail of the configured
Jul access-log file. Review a bundle before sharing it; see
[the diagnostics guide](docs/diagnostics.md) for its contents and privacy limits.

| Flag | Default | Meaning |
| --- | --- | --- |
| <code>-config FILE</code> | <code>server.toml</code> | Configuration file. |
| <code>-output FILE</code> | timestamped file in the current directory | Archive destination. |
| <code>-json</code> | off | Emit generation status and manifest as JSON. |
| <code>-check-network</code> | off | Include bounded network-capable doctor checks. |
| <code>-include-logs</code> | off | Include a bounded tail of the configured access log. |
| <code>-log-tail-bytes N</code> | <code>65536</code> | Maximum configured log bytes to inspect. |
| <code>-timeout D</code> | <code>30s</code> | Total collection timeout. |
| <code>-per-collector-timeout D</code> | <code>8s</code> | Timeout for each collector. |
| <code>-max-artifact-bytes N</code> | <code>2097152</code> | Maximum uncompressed size per artifact. |
| <code>-max-uncompressed-bytes N</code> | <code>12582912</code> | Maximum total uncompressed archive content. |
| <code>-max-compressed-bytes N</code> | <code>8388608</code> | Maximum compressed archive size. |

Exit codes: <code>0</code> bundle generated, <code>1</code> collection or
output failure, <code>2</code> invalid usage or limits.

### jul healthcheck

Probes a **running** server's admin health endpoint: <code>/healthz</code> by
default, or <code>/readyz</code> with <code>-ready</code>. The admin listener
must be enabled. HTTPS probes use the system trust roots unless the CA or
client-certificate flags below are supplied.

| Flag | Default | Meaning |
| --- | --- | --- |
| <code>-config FILE</code> | <code>server.toml</code> | Read the admin listen address from this config. |
| <code>-addr HOST:PORT</code> | — | Override the admin address while keeping the selected endpoint path. |
| <code>-url URL</code> | — | Probe this full URL instead of discovering it from config. |
| <code>-ready</code> | off | Probe readiness at <code>/readyz</code> instead of liveness at <code>/healthz</code>. |
| <code>-timeout D</code> | <code>3s</code> | Overall request timeout. |
| <code>-ca-file FILE</code> | — | Additional PEM CA bundle for HTTPS. |
| <code>-client-cert FILE</code> and <code>-client-key FILE</code> | — | Client certificate and private key for an mTLS HTTPS probe. |
| <code>-json</code> | off | Emit the result as JSON. |
| <code>-quiet</code> | off | Suppress output; use the exit code. |

Exit codes: <code>0</code> healthy (HTTP 2xx), <code>1</code> unhealthy or
unreachable, <code>2</code> usage/configuration error. See
[deployment health checks](docs/deployment.md#health-checks).

### jul lint

Parses and validates configuration, then reports lint findings with remediation
hints. Findings include warnings and informational guidance; some security
findings have error severity. Examples include risky backend TLS settings,
unsafe admin exposure, disabled compression, missing TLS minimums, unreachable
locations, directory listing exposure, and configuration-authority issues.
This list is illustrative, not exhaustive.

| Flag | Meaning |
| --- | --- |
| <code>-config FILE</code> | Configuration file; defaults to <code>server.toml</code>. |
| <code>-strict</code> | Return exit code 2 when warnings are present. |
| <code>-json</code> | Emit findings as JSON. |
| <code>-quiet</code> | Suppress warning/info output; errors remain visible. Strict mode still fails on warnings. |

Exit codes: <code>0</code> no errors (warnings allowed unless strict),
<code>1</code> config or error-severity lint findings,
<code>2</code> invalid usage or warnings under <code>-strict</code>. The JSON
contract keeps lint findings under its <code>warnings</code> field with an
individual severity. Parse errors include the source location.

### jul fmt

Validates and rewrites the TOML configuration into canonical form. By default
it writes formatted TOML to stdout; comments and original formatting are not
preserved.

| Flag | Meaning |
| --- | --- |
| <code>-config FILE</code> | File to read; defaults to <code>server.toml</code>. |
| <code>-w</code> | Write the formatted file in place. |
| <code>-diff</code> | Print a unified diff without writing. |

Exit codes: <code>0</code> formatting succeeded (or no diff changes are needed),
<code>1</code> parse/validation or I/O error, or changes are present with
<code>-diff</code>; <code>2</code> invalid usage.

### jul run

Creates a server from a synthesized profile without a configuration file.
Exactly one of <code>--serve DIR</code> and <code>--proxy TARGET</code> is
required. The default listen address is <code>:8080</code>.

| Flag | Default | Meaning |
| --- | --- | --- |
| <code>--serve DIR</code> | — | Serve a directory of static files. |
| <code>--proxy TARGET</code> | — | Reverse-proxy requests to a URL, host:port, or :port target. |
| <code>--listen ADDR</code> | <code>:8080</code> | Listen address. |
| <code>--effective-version-key-file PATH</code> | key path selection rules above | Select the protected key used for this server process. |

The zero-config profile does not configure TLS or admin authentication. Binding
to a non-loopback address makes the service reachable from other hosts and
prints a notice; expose it only behind protections appropriate to your
deployment. For an example, see [Getting started](docs/getting-started.md).

### jul import nginx

Translates or assesses an NGINX configuration. Build the binary with
<code>-tags importer</code>. Include traversal is off by default; enable it
explicitly and confine it to a root. The [NGINX importer guide](docs/nginx-importer.md)
documents the assessment contract and bounded include behavior.

| Flag | Meaning |
| --- | --- |
| <code>-o FILE</code>, <code>-output FILE</code> | Write generated TOML to a file; otherwise write it to stdout. |
| <code>-input FILE</code> | Input file alternative to the positional argument. |
| <code>-strict</code> | Exit 3 if approximated, ignored, blocking, or Jul lint findings exist. |
| <code>-assess</code> | Emit the human migration assessment without generated TOML. |
| <code>-report FILE</code> | Write the versioned JSON assessment to a file. |
| <code>-json</code> | Emit only the versioned JSON assessment to stdout. |
| <code>-path-style</code> | Set to <code>relative</code> (default) or <code>absolute</code> for assessment paths. |
| <code>-source-order</code> | Preserve source order in a human assessment; requires <code>-assess</code>. |
| <code>-follow-includes</code> | Resolve includes using bounded, root-confined traversal. Off by default. |
| <code>-root DIR</code>, <code>-include-root DIR</code> | Include root (aliases); requires <code>-follow-includes</code>. Defaults to the input file directory. |
| <code>-max-include-depth N</code> | Maximum nested include depth; default <code>16</code>. |
| <code>-max-include-files N</code> | Maximum source files including the root; default <code>256</code>. |
| <code>-max-include-file-bytes N</code> | Maximum bytes per source file; default <code>4194304</code>. |
| <code>-max-include-total-bytes N</code> | Maximum bytes across the source tree; default <code>33554432</code>. |
| <code>-max-include-glob-matches N</code> | Maximum matches per include glob; default <code>1024</code>. |

Exit codes: <code>0</code> success, <code>1</code> internal error,
<code>2</code> usage error, <code>3</code> blocking or strict findings,
<code>4</code> NGINX parse error, <code>5</code> generated-candidate validation
error, <code>6</code> file I/O error. The migration overview below includes
examples and links to the migration corpus and assessment schema.

### jul version and jul capabilities

<code>jul version</code> prints version/build metadata. Use <code>-json</code>
for the stable object with product, version, commit, build date, dirty state,
Go version, operating system, and architecture. The legacy
<code>jul -version</code> flag remains supported.

<code>jul capabilities</code> reports compiled features and the canonical exit
code contract. It prints JSON automatically when stdout is not a terminal;
<code>-json</code> forces JSON. See also the build profile in
[diagnostics](docs/diagnostics.md).

### jul posture

Evaluates Jul's seven-area gateway security posture locally from the supplied
configuration. It does not query a running server.

| Flag | Default | Meaning |
| --- | --- | --- |
| <code>-config FILE</code> | <code>server.toml</code> | Configuration file. |
| <code>-json</code> | off | Emit the report as JSON. |
| <code>-timeout D</code> | <code>30s</code> | Evaluation timeout. |

Exit codes: <code>0</code> no error findings, <code>1</code> report contains
errors or evaluation failed, <code>2</code> invalid usage. See
[Security posture](docs/security-posture.md).

### jul waf evaluate, manifest, and verify-assets

These are **local** WAF policy commands. Build with <code>-tags waf</code>.
All accept <code>-timeout D</code> (default <code>30s</code>) and
<code>-json</code>. WAF event inspection is a remote admin API query documented
in [Remote CLI](docs/remote-cli.md).

| Command | Options | Purpose |
| --- | --- | --- |
| <code>jul waf evaluate</code> | <code>-config FILE</code>, <code>-baseline FILE</code>, <code>-fixtures FILE</code>, <code>-scope SCOPE</code>, <code>-version-key-file PATH</code>, <code>-timeout D</code>, <code>-json</code> | Evaluate a candidate policy against built-in or supplied fixtures. |
| <code>jul waf manifest</code> | <code>-config FILE</code>, <code>-scope SCOPE</code>, <code>-out FILE</code>, <code>-version-key-file PATH</code>, <code>-timeout D</code>, <code>-json</code> | Write or print a manifest of the selected policy and external assets. |
| <code>jul waf verify-assets</code> | <code>-config FILE</code>, required <code>-manifest FILE</code>, <code>-version-key-file PATH</code>, <code>-timeout D</code>, <code>-json</code> | Verify the policy and external assets against a saved manifest. |

A scope is <code>global</code>, a durable route ID, or a location path. The
version-key flag is optional for offline operations; see
[WAF policy evaluation and asset recovery](docs/waf.md). Exit codes:
<code>0</code> completed successfully, <code>1</code> evaluation/verification or
configuration error, <code>2</code> invalid usage.

### jul completion

Generates scripts for <code>bash</code>, <code>zsh</code>, <code>fish</code>,
and <code>powershell</code> (also accepted as <code>pwsh</code>). Completion is
currently partial: the generated scripts do not yet list local
<code>posture</code> or <code>waf evaluate</code>, <code>waf manifest</code>, or <code>waf verify-assets</code>
commands. The completion command itself accepts a shell name; install or source
the output according to your shell's conventions.

### CLI troubleshooting

For a full incident playbook covering startup, reloads and restart-required
changes, service discovery, plugins, and soak interpretation, see
[docs/troubleshooting.md](docs/troubleshooting.md).

- **Config rejected on start or reload?** Run <code>jul lint -config server.toml</code>
  for lint findings and <code>jul doctor -config server.toml</code> for read-only
  local diagnostics. A running server keeps its last valid configuration on a
  failed reload.
- **Not sure a change is safe?** Run <code>jul check -config server.toml</code>
  for structural and stateless runtime checks, then exercise the relevant routes
  in a running instance.
- **Want a quick local server?** <code>jul run --serve .</code> needs no config
  file.

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
surface. Published v2.1.0 adds the later bounded stream, protocol and
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
max_entry_size = "64m"
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

HTTP active health probes support bounded secret-bearing headers and virtual-host
routing; gRPC probes can override authority without changing backend TLS trust.
See [health configuration](docs/health.md#probe-headers-and-routing-identity).
For expiring media/download links, see the [signed-URL WASM recipe](examples/plugins/signed-url/README.md).

Unreleased cache operations: [bounded metadata inspection and observed decision evidence](docs/cache.md#bounded-cache-inspection) complement the existing preview-confirmed invalidation workflow.
