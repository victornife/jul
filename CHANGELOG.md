# Changelog

All notable changes to Jul.IA are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Dates are in ISO 8601 format (`YYYY-MM-DD`).

## [Unreleased]

## [1.30.0] – 2026-07-05

### Fixed

- **Compression auto-enable bug** (`internal/config/parser.go`): a `[compression]` block with explicit settings (`encoders`, `min_size`, `types`) but without `enabled = true` was silently skipped, leaving responses uncompressed and the console showing "compression disabled". The parser now auto-enables compression when any setting is present — the block implies intent. Users can still explicitly disable with `enabled = false`.
- **OTel schema-URL conflict** (`internal/observability/tracing.go`): imported `semconv/v1.39.0` while the build pulls `otel v1.44.0` (which uses `semconv/v1.41.0`). `resource.Merge()` failed with mismatched schema URLs, preventing tracer initialization. Fixed by updating the import to `semconv/v1.41.0`.

### Added

- **Phase 2A consolidated burn-in harness**: `burn-in-full.toml` exercises **all 10 shipped features simultaneously** (proxy, cache, rate-limit, WAF, auth, compression, TLS, mTLS, upstream health-checks, OTel tracing) in a single config.
- **Load-generator `-full` mode** (`scripts/burn-in-load.go`): `-full` flag exercises all 10 features in one run with TLS + mTLS client cert support, per-status counters (2xx/401/403/429/5xx), and authenticated traffic mix.
- **TLS certificate generator** (`scripts/gen-certs.go`): generates fresh CA + server (localhost) + client certificates for burn-in mTLS testing (1-year validity).
- **mTLS test certificates**: `testdata/tls/client.crt`, `client.key`, `localhost.ext` — enables end-to-end mTLS soak verification.

### Changed

- `docs/soak-evidence.md`: added Phase 2A 5-minute pilot (29,587 req, 0% err) and **8-hour completed soak** (2,120,299 req, 0% err, 100% success) — the most demanding soak test in the project history.
- `docs/status.md`: updated version stamp to v1.30 / 2026-07-05; added Phase 2A soak-tracking row; added v1.30.0 changelog entry.

## [1.29.0] – 2026-07-03

### Added

**GA evidence bundles — post-GA soak gate**
- All remaining Beta features promoted to **GA — soak pending** (criteria ①②③④⑥⑦⑧⑨ met; criterion ⑤ soak is a post-GA gate per ADR 0005). This clears the Beta backlog entirely — all 20 shipped features are now GA — soak pending.

**Per-feature new evidence**
- **HTTP/3 over QUIC (Y1-11)** — `docs/http3.md` with QUIC/Alt-Svc behaviour matrix, `BenchmarkHTTP3Throughput` (~259 μs/op), 4-item limitation list, 5-row threat note, and `docs/status.md` soak-tracking row. | [http3.md](docs/http3.md)
- **WASM plugin system (Y2-02)** — `docs/plugins.md` expanded with a 19-row behaviour matrix (ABI boundary, guest containment, reload, fetch/KV guards, KV quotas), 5 benchmarks in `internal/plugins/bench_test.go` (`BenchmarkPluginMiddleware` ~16.5 μs, `BenchmarkPluginHandler` ~20 μs, `BenchmarkPluginKVCounterWithCapability` ~23 μs, `BenchmarkPluginParallel` ~3.4 μs amortised), 5-item limitation list, 7-row threat note, fuzz targets `FuzzPluginInvoke` and `FuzzHostAllowed` in `internal/plugins/fuzz_test.go`. | [plugins.md](docs/plugins.md)
- **L4 stream proxy (Y2-03)** — `docs/stream.md` with 23-row behaviour matrix (TCP/UDP relay, SNI routing, PROXY protocol, reload, preflight, UDP sessions), 4 benchmarks in `internal/stream/bench_test.go` (`BenchmarkTCPPassthrough` ~3.2 ms, `BenchmarkTCPParallel` ~3.3 ms, `BenchmarkUDPRelay` ~33 μs, `BenchmarkUDPAdmitAtCap` up to 254 μs for 10k sessions), 5-item limitation list, 6-row threat note, fuzz targets `FuzzReadProxyHeader` and `FuzzPeekSNI` in `internal/stream/fuzz_test.go`. UDP-churn soak test `TestSoakUDPChurn` added behind the `soak` tag. | [stream.md](docs/stream.md)

**Tracking docs**
- `docs/roadmap/README.md` — v1.30, year-completion checklists corrected: Year 1 is 11/11 GA, Year 2 is 9/9 GA; changelog row added.
- `docs/status.md` — v1.30, GA table now lists all 20 shipped features (including HTTP/3, WASM plugins, L4 stream); Beta section replaced with an "all GA" notice; soak tracking table updated with the 3 newest features.
- `docs/ga-push.md` — v1.30, obsolete Wave 2 and Wave 3 tables removed (consolidated into Wave 1); soak tracking table updated.

### Changed
- `docs/soak-evidence.md` — updated run-log with the 2026-07-01 smoke soak (proxy + udp-churn) verifiable artifact.

## [1.28.0] – 2026-07-03

### Added
- Goroutine-leak detection for the `internal/server` package (`goleak.VerifyTestMain`), plus a Windows CI test lane (lean + full) to catch platform-specific lifecycle bugs.
- Concurrency and negative regression tests: transcode rejects reflection against a non-reflective backend, WASM plugin reload-under-load, and concurrent admin apply/rollback.
- Plugin upload filename hardening: uploads must be a simple `<name>.wasm` (safe charset, no path separators/`..`), with a defense-in-depth check that the stored path stays inside the upload directory. Threat model documented in [docs/plugins.md](docs/plugins.md).
- Soak evidence log ([docs/soak-evidence.md](docs/soak-evidence.md)) with dated runs; CI and release soak jobs now upload a `soak-results` artifact so the ADR-0005 gate is verifiable.
- GA-evidence burndown table in [docs/status.md](docs/status.md) tracking the per-Beta-feature evidence bundle (matrix/bench/threat-note/fuzz/soak).
- Troubleshooting guide ([docs/troubleshooting.md](docs/troubleshooting.md)) and a first-run hint that points to zero-config mode when no `server.toml` is found.
- `internal/app` package with unit-tested composition-root wiring — scope/index/reload helpers and the runtime preflight (`wiring.go`), the admin-deps builder and view adapters (`admin_deps.go`), and the admin write-preflight gate sequence (`preflight.go`) — reducing `cmd/jul/main.go` toward a thin entry point (ADR-0007 testability follow-through).
- CLI JSON output schema documented in [docs/configuration.md](docs/configuration.md).
- `stale_if_error` configuration option in `[cache]` to extend the stale-serving window when a background revalidation encounters an upstream error (5xx or timeout). This protects clients from backend outages by keeping the cached response servable for the configured duration after a failed revalidation.
- Admin config diff support for `stale_if_error` changes in the Console.

### Changed
- `jul lint -json` now emits a stable schema: lowercase field names and a string `severity` (`"warning"`/`"error"`) instead of a numeric enum.
- `jul fmt` no longer emits reserved (`mail`) or empty top-level (`upstreams`, `stream`, `plugins`) tables in canonical output.
- Both configuration rollback endpoints (`POST /api/history/rollback` and the Console-facing `POST /api/config/rollback`) now route through a single `applyMu`-guarded write path, closing a read-modify-write race with a concurrent apply. A v1.1 fix had serialized only the first endpoint; the Console calls the second, so the race remained until this change.
- Split the two largest admin/config source files by concern to keep each under ~600 LOC: `internal/admin/api.go` (1214→502; extracted `api_status.go`, `api_history.go`, `api_wizard.go`) and `internal/config/validate.go` (1005→561; extracted `validate_location.go`, `validate_backends.go`). Behavior unchanged.
- Example configs (`examples/migrate/jul.toml`, `server.full.apps.toml`) no longer carry the empty `stream = []` / `mail = []` tables that `jul fmt` now omits.
- `docs/status.md` and `docs/roadmap/README.md` corrected: Console continuous panels status footnote now explicitly lists live log tail (shipped), WASM plugin manager (shipped with upload pending), and gRPC route designer (planned).

### Fixed
- Intermittent hang/timeout in the `internal/server` test suite under parallel load, caused by leaked keep-alive `persistConn` goroutines in the test HTTP clients.

## [1.27.0] – 2026-07-01

### Added
- Admin Console **WASM plugin upload** (`POST /api/plugins/upload`): validates WASM magic and version, enforces configurable size cap, writes atomically via `atomicfile`, broadcasts `plugin_uploaded` SSE event. Configurable via `[admin]` keys `plugin_upload_enabled`, `plugin_upload_dir`, and `plugin_upload_max_size` (defaults enabled, `./jul-data/plugins`, `32` MB). Set `plugin_upload_enabled = false` to disable the endpoint.

> **Note:** Default changed to `false` (secure-by-default) in v1.29.0 ([`internal/config/parser.go`](internal/config/parser.go)).

- Admin Console **gRPC route designer** (new Transcode panel): upload a compiled protobuf FileDescriptorSet (`.pb`) for inspection-only preview of the `google.api.http` annotations it declares, configure backend target / TLS / streaming / stream framing, then open the generated `grpc_transcode` route in the config editor for the standard Validate → Diff → Apply flow. The generated route exposes all methods from the descriptor set (per-method filtering is not supported). Cross-linked from existing `grpc_transcode` route detail drawers.
- Admin API endpoint `POST /api/transcode/descriptor-upload` parses uploaded descriptors and returns methods with HTTP bindings (no `grpc` build tag required on the admin side).

### Changed
- `docs/status.md`: Console continuous panels footnote updated — live log tail ✅ shipped; WASM plugin manager ✅ shipped; gRPC route designer ✅ shipped.
- `docs/roadmap/README.md`: Y2-09 row updated to reflect closed panel backlog (`.wasm` upload + gRPC designer both shipped); backlog is now empty.
- `docs/console.md` capability matrix: added gRPC-JSON transcoding row (Guided-create); Plugins row updated to include `.wasm` upload; API endpoint map updated with `POST /api/transcode/descriptor-upload`.

## [1.26.0] – 2026-06-30

### Changed
- Consolidated release notes. No new features beyond v1.0.0 (all foundation features were first introduced in v1.0.0 or earlier Betas).

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
- Full feature set with expanded descriptions:
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
- Multi-architecture release binaries (Windows, Linux, macOS on amd64/arm64) in `lean` and `full` profiles.
- Docker image with distroless runtime.
- systemd and Windows service deployment assets.

### Security
- Comprehensive threat model in `SECURITY.md` with per-feature security notes.
- Request parsing hardening, header size caps, slowloris mitigation, HTTP/2 reset flood protection.
- Static file path traversal protection via `os.Root`.
- Admin listener loopback binding by default with bearer token authentication.
- Config snapshotting and audit logging for compliance.

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
