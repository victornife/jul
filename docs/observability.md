# Observability

Jul.IA exposes three layers of observability: **metrics** (Prometheus at
`/metrics`), **tracing** (OpenTelemetry), and **logging** (structured text/JSON
with pluggable access-log sinks). All three are served from the admin listener
(`[admin]`), which should be bound to loopback in production.

## Prometheus metrics

The `/metrics` endpoint on the admin listener exports a private Prometheus
registry. No build tag is required. The endpoint is served at:

```
GET http://127.0.0.1:9090/metrics
```

### HTTP request metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `jul_http_requests_total` | Counter | `method`, `host`, `code` | Total requests served |
| `jul_http_request_duration_seconds` | Histogram | `method`, `host`, `code` | Request latency distribution |
| `jul_http_requests_in_flight` | Gauge | — | Concurrent requests currently being handled |
| `jul_http_response_compressed_total` | Counter | `encoder` | Responses compressed by encoder (gzip/br/zstd) |

The `host` label is **off by default** because the `Host` header is
client-controlled and unbounded host values can explode metric cardinality. Set
`[observability.metrics].host_label = true` only when the host set is bounded.
Changing this setting requires a restart.

### Cache metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `jul_cache_events_total` | Counter | `result` (HIT / MISS / STALE / BYPASS) | Cache outcome per request |

### Upstream metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `jul_upstream_healthy` | Gauge | `pool`, `backend` | 1 = healthy, 0 = unhealthy |
| `jul_upstream_backends` | Gauge | `pool` | Current number of backends in pool |
| `jul_upstream_probes_total` | Counter | `pool`, `result` (success / failure) | Active health-check probes |
| `jul_upstream_probe_duration_seconds` | Histogram | `pool` | Probe latency |
| `jul_discovery_errors_total` | Counter | `pool` | Failed or empty backend resolves |

### Auth, rate limit, and WAF metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `jul_auth_decisions_total` | Counter | `method`, `result` (allow / deny) | Authentication decisions |
| `jul_http_ratelimited_total` | Counter | `key` (ip / header / jwt) | Requests rejected by rate limiter |
| `jul_waf_events_total` | Counter | `mode`, `severity` | WAF rule matches (block or detect) |
| `jul_egress_decisions_total` | Counter | `subsystem`, `result` (allow / block), `reason` | Outbound egress allow-list decisions (never labelled by destination) |
| `jul_egress_dns_answers_total` | Counter | `subsystem`, `result` | Egress CIDR-only hostname resolutions evaluated |

### gRPC metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `jul_grpc_transcode_requests_total` | Counter | `method`, `code` | Transcoded REST → gRPC calls |
| `jul_grpc_transcode_stream_msgs_total` | Counter | `method`, `direction` | Streaming messages |
| `jul_grpc_proxy_streams_total` | Counter | `upstream` | Native gRPC passthrough calls |

### Plugin metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `jul_plugin_invocations_total` | Counter | `plugin`, `result` | WASM plugin calls |
| `jul_plugin_duration_seconds` | Histogram | `plugin` | Plugin call latency |
| `jul_plugin_panics_total` | Counter | `plugin` | Guest panics contained by the host |

### Listener and certificate metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `jul_listener_conns` | Gauge | `addr`, `proto` | Active connections per listener |
| `jul_http3_connections` | Gauge | — | Active HTTP/3 / QUIC connections |
| `jul_stream_conns` | Gauge | `proto` | Active L4 stream connections |
| `jul_stream_bytes_total` | Counter | `proto`, `direction` | L4 relayed bytes |
| `jul_cert_expiry` | Gauge | `domain` | Certificate expiry timestamp (Unix epoch seconds) |
| `jul_cert_renewals` | Counter | `domain` | ACME renewal events |
| `jul_mtls_handshakes` | Counter | `result` | mTLS handshakes (success / failed) |

## OpenTelemetry tracing

Tracing is gated behind the `otel` build tag. Enable it with:

```bash
go build -tags otel -o jul ./cmd/jul
```

Configure the OTLP exporter:

```toml
[observability.tracing]
enabled      = true
exporter     = "otlp-grpc"   # or "otlp-http"
endpoint     = "localhost:4317"   # "host:port" for gRPC; URL for HTTP
sample_ratio = 0.1                # head-sampling; 1.0 = trace everything
service_name = "jul-edge"
insecure     = false              # set true for a local collector without TLS
```

### What is traced

- **Server span** — one per HTTP request, named after the route.
- **`proxy.roundtrip`** child span — covers the entire upstream interaction.
- **`upstream.request`** child span — one per backend attempt, so failover is
  visible (a retry shows two `upstream.request` spans).
- **`cache.lookup`** child span — cache hit/miss/stale decision and background
  revalidation.

W3C `traceparent` propagation is supported in both directions:

- **Incoming** — an upstream request carrying a `traceparent` header continues
  the trace; Jul.IA becomes a child span.
- **Outgoing** — the active `traceparent` is forwarded to upstreams so they can
  continue the trace.

The active trace id is added to the access log as `trace_id`.

### Tracing limitations

- Tracing settings are fixed at startup; changing them requires a restart.
- A hot reload that changes tracing emits a warning and keeps the running tracer.
- The `insecure` flag disables TLS to the collector; use only in local/dev
  environments.

## Logging and access logs

Jul.IA uses Go's structured `log/slog` for all logging. The format is controlled
by `[global].log_format`:

- `text` — human-readable key=value lines (default).
- `json` — newline-delimited JSON objects, suitable for log aggregation.

The `[global].log_level` controls the verbosity: `debug`, `info` (default),
`warn`, `error`.

### Access-log sinks

The access log can be written to multiple destinations simultaneously via
`[observability.access_log].sinks`:

| Sink | Platform | Notes |
|------|----------|-------|
| `stdout` | All | Follows `[global].log_format` |
| `file` | All | Dedicated, size-rotating file. Parent directory created if missing. |
| `syslog` | Unix only | Local system log (`LOG_LOCAL0`, tag `jul`). Not supported on Windows. |

```toml
[observability.access_log]
sinks       = ["stdout", "file"]
file        = "/var/log/jul/access.log"
format      = "json"          # applies to file and syslog only
rotate_max_mb = 100
rotate_keep   = 7
```

The `file` and `syslog` sinks always record at **info level** regardless of
`[global].log_level`, so a quieter global level never suppresses the access log.

### Access-log fields

Each access-log entry includes:

- `ts` — ISO 8601 timestamp
- `method`, `path`, `host`, `remote_addr`
- `status`, `bytes_sent`, `duration_ms`
- `upstream_addr`, `upstream_status`, `upstream_duration_ms` (when proxied)
- `cache` — `HIT`, `MISS`, `STALE`, or `BYPASS`
- `trace_id` — when tracing is active
- `error` — when the request failed with an error

### Egress block logs

When the optional `[egress]` allow-list refuses an outbound auxiliary fetch
(JWKS, forward-auth, discovery, ACME/OCSP, or plugin `fetch`), the server emits a
structured, rate-limited log line alongside the `jul_egress_decisions_total`
counter. Each entry carries the `subsystem`, the normalized `host`, an optional
`resolved_ip`, and a bounded `reason` (`host_not_allowed`, `ip_not_allowed`,
`mixed_dns_answers`, `no_dns_answers`, `invalid_address`) — never a URL, query
string, or credential. Identity, discovery, and PKI blocks log at **warning**;
plugin-fetch denials log at **info**. Identical events are collapsed within a
short window so a retry loop cannot flood the log. See
[egress.md](egress.md#metrics-logs-and-diagnostics).

## Health and readiness

The admin listener exposes health endpoints:

| Endpoint | Purpose |
|----------|---------|
| `GET /healthz` | Liveness — returns `200 OK` when the server process is running |
| `GET /readyz` | Readiness — returns `200 OK` when all listeners are bound and serving |
| `POST /api/config/apply` | Apply a new configuration (with optimistic concurrency token) |
| `POST /api/config/rollback` | Roll back to the previous configuration snapshot |
| `GET /api/config/history` | List configuration snapshots |

The Console uses these endpoints for its live dashboard. Kubernetes or other
orchestrators can use `/healthz` and `/readyz` for probes.

## Admin rate limiting

To protect the admin listener from abuse, Jul.IA applies per-client rate limits
when `[admin]` is enabled:

| Resource | Default | Description |
|----------|---------|-------------|
| Read requests | 240/min | GET and other safe methods |
| Write requests | 60/min | POST, PUT, DELETE |
| Config apply | 30/min | High-impact validate/diff/apply endpoints |
| SSE streams | 4 concurrent | `/api/events` WebSocket-like streams |

These defaults apply only when admin is enabled; set to `0` or a negative value
to disable a specific limit.

## Operational checklist

- [ ] Bind admin to loopback (`127.0.0.1:9090`) and require a strong bearer
      token in production.
- [ ] Enable Prometheus scraping from a trusted source; consider a
      `keep` relabel rule if `host_label` is enabled.
- [ ] Use the `file` access-log sink for dedicated, rotatable logs.
- [ ] Enable tracing (`otel` tag) in staging first to validate overhead.
- [ ] Monitor `jul_cert_expiry` for upcoming certificate renewals.
- [ ] Use `/readyz` for orchestrator readiness probes.

## Benchmarks & tuning

For a catalogue of in-tree benchmarks, how to run the harness, and production
tuning recommendations (connection pooling, cache sizing, compression levels,
worker limits), see [benchmarks.md](benchmarks.md).
