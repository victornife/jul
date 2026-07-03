# OpenTelemetry tracing + access-log sinks

> Feature ID: **Y1-10** · Build tag: `otel` · Since v1.24

Jul.IA supports OpenTelemetry distributed tracing and structured access-log
sinks. Tracing is opt-in (gated by the `otel` build tag) and produces OTLP
spans via gRPC or HTTP. Access-log sinks are part of the core binary and write
to `stdout`, a rotating file, or syslog.

## Usage

Build with tracing support:

```bash
go build -tags otel -o jul ./cmd/jul
```

Configure the OTLP exporter:

```toml
[observability.tracing]
enabled      = true
exporter     = "otlp-grpc"   # or "otlp-http"
endpoint     = "localhost:4317"
sample_ratio = 0.1
service_name = "jul-edge"
insecure     = false
```

Configure access-log sinks (core, no build tag):

```toml
[observability.access_log]
sinks       = ["stdout", "file"]
file        = "/var/log/jul/access.log"
format      = "json"
rotate_max_mb = 100
rotate_keep   = 7
```

## Exporter / sink matrix

### Tracing exporters

| Exporter | Transport | TLS default | Config | Notes |
| --- | --- | --- | --- | --- |
| `otlp-grpc` | gRPC | Enabled (host root CAs) | `endpoint = "host:port"` | Default exporter |
| `otlp-http` | HTTP/protobuf | Enabled (host root CAs) | `endpoint = "host:port"` or full URL | Use for HTTP-only collectors |

Both exporters support `insecure = true` for local development (plaintext).

### Span types

| Span name | Kind | When | Attributes |
| --- | --- | --- | --- |
| `GET /path` | Server | Every HTTP request | `http.request.method`, `url.path`, `url.scheme`, `server.address`, `user_agent.original`, `network.protocol.version`, `http.response.status_code` |
| `proxy.roundtrip` | Internal | Every proxied request | `upstream.name` |
| `upstream.request` | Internal | Each backend attempt (including retries) | `http.response.status_code`, error on failure |
| `cache.lookup` | Internal | When `cache = true` | `cache.status` (HIT/MISS/STALE/BYPASS) |

### Propagation

| Direction | Format | Behaviour |
| --- | --- | --- |
| Incoming | W3C tracecontext | Extracts `traceparent`; server span becomes a child |
| Outgoing | W3C tracecontext | Injects `traceparent` into upstream requests |

### Access-log sinks

| Sink | When active | Format | Rotation |
| --- | --- | --- | --- |
| `stdout` | Always (when listed) | Follows `[global].log_format` | n/a |
| `file` | When listed + `file` set | `text` (logfmt) or `json` | Size-based (`rotate_max_mb` / `rotate_keep`) |
| `syslog` | When listed; Unix only | `text` or `json` | n/a |

All sinks record at **info level** regardless of `[global].log_level`.

### Access-log fields

| Field | Source | PII risk |
| --- | --- | --- |
| `ts` | Server clock | None |
| `method`, `path`, `host` | Request line | **Medium** — `host` is client-controlled; `path` may contain tokens |
| `remote_addr` | TCP peer | **Medium** — client IP |
| `status`, `bytes_sent` | Response | None |
| `duration_ms` | Server timing | None |
| `upstream_addr`, `upstream_status` | Upstream | Low |
| `cache` | Cache disposition | None |
| `trace_id` | Active span context | Low — opaque identifier |
| `error` | Request error | **High** — may leak internal paths or upstream errors |
| `request_id` | `X-Request-ID` or generated | None |

## Benchmarks

Run with `go test -tags otel -bench=. ./internal/observability/`.

| Benchmark | Scenario | ns/op | allocs/op | bytes/op |
| --- | --- | --- | --- | --- |
| `BenchmarkTracingMiddleware` | Full server span + attribute capture + sync export | ~10 400 | 23 | 3 840 |
| `BenchmarkTracingBaseline` | Same handler, no tracer installed | ~391 | 4 | 208 |
| `BenchmarkTracingSeamChildSpan` | Child span via the dependency-free seam | ~2 540 | 7 | 1 264 |
| `BenchmarkTracingSeamBaseline` | No-op seam (default build) | ~20 | 0 | 0 |
| `BenchmarkTracingW3CExtract` | Server span with incoming `traceparent` | ~8 800 | 26 | 3 984 |

The middleware adds ~10 μs per request when tracing is compiled in and enabled.
The no-op seam (default build) costs ~20 ns and zero allocations, so untagged
binaries pay essentially nothing. The exporter batcher amortises actual send
overhead; these numbers represent the in-process span construction cost.

## Known limitations

1. **Tracing settings require a restart.** Changing `[observability.tracing]`
   after startup emits a warning and keeps the running tracer; a restart is
   needed to pick up new endpoint, sample ratio, or exporter type.

2. **No tail-based sampling.** Only head-based ratio sampling is supported
   (`sample_ratio`). Adaptive or error-biased sampling must be done in the
   collector (e.g. OpenTelemetry Collector tail-sampling processor).

3. **Access-log format changes require a restart.** Like tracing, `[observability.access_log]`
   is built once at startup. Adding/removing sinks or changing file path/format
   needs a process restart.

4. **Syslog sink is Unix-only.** Windows builds silently ignore the `syslog`
   sink if listed.

## Threat note (PII and data leakage)

Tracing and access logs are powerful observability tools, but they are also
persistent data sinks that can leak sensitive information:

1. **PII in span attributes.** The server span records `url.path`, `server.address`,
   and `user_agent.original`. If a request uses a URL like `/reset?token=abc123`,
   the token is recorded verbatim in the span. Counter-measures: sanitise URLs
   before they reach the server (e.g. route tokens in headers); use `sample_ratio`
   < 1.0 to reduce exposure volume; configure the collector to redact sensitive
   attributes.

2. **Trace id in access logs links identities across requests.** A client that
   sends the same `traceparent` header across multiple authenticated sessions
   will have all those requests linked under one trace id in the access log.
   Counter-measures: do not trust client-provided `traceparent` in high-trust
   contexts unless validated; the W3C spec advises ignoring external trace
   context for untrusted clients.

3. **Access-log file leakage.** A rotating access-log file on disk may contain
   years of request history including `Authorization` headers (if logged by a
   custom middleware) or error messages with stack traces. Counter-measures:
   restrict file permissions (`0o640` or tighter); ship logs off-node promptly;
   avoid logging headers that carry credentials.

4. **Collector endpoint exposure.** An `insecure = true` tracing configuration
   sends span data (including PII) over plaintext to the collector. If the
   collector is on a shared network, spans may be intercepted. Counter-measures:
   always use TLS in production; bind the collector to localhost-only when
   co-located; audit collector RBAC to ensure only authorised backends can read
   spans.

5. **Error messages in spans and logs.** A failed upstream connection may produce
   an error message like `dial tcp 10.0.0.1:8080: connection refused`. If
   recorded in a span or access log, this leaks internal IP addresses and
   topology. Counter-measures: enable field redaction in the collector; review
   access-log `error` field usage; avoid exposing raw error strings to untrusted
   consumers.

## Runnable example

`testdata/otel.toml` shows a production-ready tracing + access-log setup:

```bash
go run -tags otel ./cmd/jul -c testdata/otel.toml
```

## GA status

| Criterion | Status | Evidence |
| --- | --- | --- |
| 1 — Conformance / behaviour matrix | ✅ | Exporter/sink matrix above (exporters, span types, propagation, access-log fields with PII risk) |
| 2 — Published benchmark numbers | ✅ | `BenchmarkTracingMiddleware`, `SeamChildSpan`, `W3CExtract` in `internal/observability/bench_test.go` |
| 3 — Known-limitations list | ✅ | 4-item limitation list above |
| 4 — Semver-guarded config/API contract | ✅ | v1 config freeze (cross-cutting) |
| 5 — Long-running soak test | ☐ | Post-GA gate per ADR-0005 |
| 6 — Runnable example + docs | ✅ | `testdata/otel.toml` + this doc + `docs/observability.md` |
| 7 — Security / threat note | ✅ | 5-row PII/data-leakage threat note above |
| 8 — Fuzzing where parsing is involved | n/a | No custom parser (uses stdlib `http` and OTel SDK) |
| 9 — Self-explanatory Console surface | ✅ | Status row shows tracing compiled state + active sinks |
