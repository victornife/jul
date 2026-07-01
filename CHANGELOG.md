# Changelog

All notable changes to Jul.IA are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Dates are in ISO 8601 format (`YYYY-MM-DD`).

## [Unreleased]

### Added
- Goroutine-leak detection for the `internal/server` package (`goleak.VerifyTestMain`), plus a Windows CI test lane (lean + full) to catch platform-specific lifecycle bugs.
- Concurrency and negative regression tests: transcode rejects reflection against a non-reflective backend, WASM plugin reload-under-load, and concurrent admin apply/rollback.
- Plugin upload filename hardening: uploads must be a simple `<name>.wasm` (safe charset, no path separators/`..`), with a defense-in-depth check that the stored path stays inside the upload directory. Threat model documented in [docs/plugins.md](docs/plugins.md).
- Soak evidence log ([docs/soak-evidence.md](docs/soak-evidence.md)) with dated runs; CI and release soak jobs now upload a `soak-results` artifact so the ADR-0005 gate is verifiable.
- GA-evidence burndown table in [docs/status.md](docs/status.md) tracking the per-Beta-feature evidence bundle (matrix/bench/threat-note/fuzz/soak).
- Troubleshooting guide ([docs/troubleshooting.md](docs/troubleshooting.md)) and a first-run hint that points to zero-config mode when no `server.toml` is found.
- `internal/app` package with unit-tested composition-root wiring helpers (ADR-0007 testability follow-through).
- CLI JSON output schema documented in [docs/configuration.md](docs/configuration.md).
- `stale_if_error` configuration option in `[cache]` to extend the stale-serving window when a background revalidation encounters an upstream error (5xx or timeout). This protects clients from backend outages by keeping the cached response servable for the configured duration after a failed revalidation.
- Admin config diff support for `stale_if_error` changes in the Console.
- Admin Console plugin manager supports direct `.wasm` upload (`POST /api/plugins/upload`). Validates WASM magic and version, enforces configurable size cap, writes atomically via `atomicfile`, and broadcasts `plugin_uploaded` SSE event so the panel refreshes automatically. Configurable via `[admin]` keys `plugin_upload_dir` and `plugin_upload_max_size`.  
- Admin config fields `plugin_upload_dir` and `plugin_upload_max_size` with defaults (`./jul-data/plugins`, `32` MB). Upload disabled when `plugin_upload_max_size <= 0`.

### Changed
- `jul lint -json` now emits a stable schema: lowercase field names and a string `severity` (`"warning"`/`"error"`) instead of a numeric enum.
- `jul fmt` no longer emits reserved (`mail`) or empty top-level (`upstreams`, `stream`, `plugins`) tables in canonical output.
- History rollback (`POST /api/history/rollback`) now serializes under the same `applyMu` as config apply, closing a read-modify-write race with a concurrent apply.
- `docs/status.md` and `docs/roadmap/README.md` corrected: Console continuous panels status footnote now explicitly lists live log tail (shipped), WASM plugin manager (shipped with upload pending), and gRPC route designer (planned).

### Fixed
- Intermittent hang/timeout in the `internal/server` test suite under parallel load, caused by leaked keep-alive `persistConn` goroutines in the test HTTP clients.

## [1.27.0] – 2026-07-01

### Added
- Admin Console **WASM plugin upload** (`POST /api/plugins/upload`): validates WASM magic and version, enforces configurable size cap, writes atomically via `atomicfile`, broadcasts `plugin_uploaded` SSE event. Configurable via `[admin]` keys `plugin_upload_dir` and `plugin_upload_max_size` (defaults `./jul-data/plugins`, `32` MB). Upload disabled when `plugin_upload_max_size <= 0`.
- Admin Console **gRPC route designer** (new Transcode panel): upload a compiled protobuf FileDescriptorSet (`.pb`), inspect `google.api.http` annotations in a selectable methods table, configure backend target / TLS / streaming / stream framing, and generate a `grpc_transcode` route that flows through Validate → Diff → Apply. Cross-linked from existing `grpc_transcode` route detail drawers.
- Admin API endpoint `POST /api/transcode/descriptor-upload` parses uploaded descriptors and returns methods with HTTP bindings (no `grpc` build tag required on the admin side).

### Changed
- `docs/status.md`: Console continuous panels footnote updated — live log tail ✅ shipped; WASM plugin manager ✅ shipped; gRPC route designer ✅ shipped.
- `docs/roadmap/README.md`: Y2-09 row updated to reflect closed panel backlog (`.wasm` upload + gRPC designer both shipped); backlog is now empty.
- `docs/console.md` capability matrix: added gRPC-JSON transcoding row (Guided-create); Plugins row updated to include `.wasm` upload; API endpoint map updated with `POST /api/transcode/descriptor-upload`.

## [1.26.0] – 2026-06-30

### Added
- Core HTTP server engine: static file serving, reverse proxy, FastCGI/uWSGI, virtual hosts, and routing (`exact`, `prefix`, `regex`).
- TLS 1.2/1.3 termination with SNI certificate selection, HTTP→HTTPS redirect, and dynamic certificate reload without rebinding listeners.
- ACME automatic HTTPS (HTTP-01 and TLS-ALPN-01 challenges) with on-disk certificate cache and OCSP stapling — gated behind `acme` build tag.
- mTLS / client certificate authentication with CA bundles, SAN verification, and CRL checking.
- HTTP/3 over QUIC sharing TLS certificates, advertised via `Alt-Svc` — gated behind `http3` build tag.
- h2c (cleartext HTTP/2) support for native gRPC clients without TLS.
- gRPC-JSON transcoding (`grpc_transcode`) from compiled descriptor sets or server reflection — unary and streaming (server/client/bidi) with NDJSON and SSE framing modes — gated behind `grpc` build tag.
- Native gRPC passthrough with trailers preserved and streaming frame flush — gated behind `grpc` build tag.
- Two-tier response cache (memory L1 + optional disk L2) with TTL, `stale-while-revalidate`, Vary variant support, and admin purge endpoint.
- Compression (gzip default; optional Brotli via `brotli` tag and Zstd via `zstd` tag) with Accept-Encoding negotiation, MIME allow-list, minimum size gate, and precompressed `.br`/`.gz` sidecar serving.
- Token-bucket rate limiting keyed by client IP, request header, or JWT claim, with concurrent connection limiting per listener.
- Authentication: CIDR allow/deny gates, HTTP Basic (bcrypt `htpasswd`), JWT bearer validation against JWKS endpoints (asymmetric only, `none` rejected), and forward-auth delegation.
- WebAssembly plugin runtime via wazero with capability-gated KV store and outbound fetch, per-plugin memory/time limits, and panic isolation — gated behind `wasmplugins` build tag.
- L4 stream proxy (TCP/UDP) with TLS SNI passthrough routing and HAProxy PROXY protocol v1/v2 — gated behind `stream` build tag.
- Service discovery for upstream backends: DNS A/AAAA, DNS SRV (all builds), plus Consul and Kubernetes EndpointSlices behind build tags.
- Web Application Firewall (WAF) with embedded OWASP Core Rule Set via Coraza — gated behind `waf` build tag.
- Secrets resolution (`${env:NAME}`, `${file:/path}`, `${secret:/path}`) with log redaction and `jul lint` detection of literal credentials.
- Zero-downtime hot reload via SIGHUP, file watch, or admin API with generational handler swap, config preflight checks, and automatic listener rebinding.
- Graceful shutdown with configurable timeout (default 30s).
- Admin web console (`console` build tag) with live metrics dashboard, upstream health, certificate inventory, config history with one-click rollback, and setup wizard.
- Prometheus metrics (`/metrics`) covering HTTP requests, cache events, compression, rate limiting, auth decisions, WAF events, upstream health, discovery errors, gRPC transcoding/proxy, plugin invocations, listener connections, stream connections, and certificate expiry.
- OpenTelemetry tracing (OTLP gRPC/HTTP) with W3C tracecontext propagation, child spans for proxy, upstream, and cache operations — gated behind `otel` build tag.
- Structured logging (text/JSON) and pluggable access-log sinks (stdout, rotating file, syslog on Unix).
- CLI: `jul lint` (validation + best-practice warnings, CI-friendly exit codes), `jul fmt` (canonical TOML rewrite), `jul run --serve/--proxy` (zero-config server), and `jul import nginx` (NGINX → TOML migration, gated behind `importer` tag).
- NGINX config compatibility guide and migration example.

### Security
- Comprehensive threat model in `SECURITY.md` with per-feature security notes.
- Request parsing hardening, header size caps, slowloris mitigation, HTTP/2 reset flood protection.
- Static file path traversal protection via `os.Root`.
- Admin listener loopback binding by default with bearer token authentication.
- Config snapshotting and audit logging for compliance.

## [1.0.0] – 2026-06-21

### Added
- First stable release of Jul.IA HTTP edge server.
- All foundation features declared GA — soak pending (see `docs/status.md`):
  - Core HTTP (static, proxy, FastCGI/uWSGI, vhosts, routing)
  - TLS + automatic HTTPS
  - Authentication (CIDR/Basic/JWT/forward-auth)
  - gRPC transcoding and passthrough
  - mTLS client auth
  - Console (operations cockpit)
- Multi-architecture release binaries (Windows, Linux, macOS on amd64/arm64) in `lean` and `full` profiles.
- Docker image with distroless runtime.
- systemd and Windows service deployment assets.

### Changed
- Console v1 hand-written dashboard retired and replaced with v2 React/TS/Vite embedded SPA (~250 KB gz initial-route budget).

### Fixed
- gRPC transcoding streaming limits and strict framing for server/client/bidirectional RPCs.
- Cache disk-tier safety: atomic `0o600` writes, foreign-file isolation, lock-free eviction.
- gRPC transcoding passive health marking on backend-failure gRPC status codes.
- Console config patches are now atomic with audit fail-loud behavior.

## [0.9.0] – 2026-05-15

### Added
- Beta release of L4 stream proxy (TCP/UDP), WASM plugins, service discovery (Consul/K8s), WAF, and HTTP/3.
- Soak test harness and fuzz CI jobs.
- Benchmark harness for performance regression detection.

### Changed
- Stream proxy, WASM plugins, WAF, HTTP/3, and Consul/Kubernetes discovery promoted from experimental to Beta.
- TOML schema extended with upstream health checks, service discovery blocks, and plugin configurations.
