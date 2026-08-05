# Observability

Jul.IA exposes three layers of observability: **metrics** (Prometheus at
`/metrics`), **tracing** (OpenTelemetry), and **logging** (structured text/JSON
with pluggable access-log sinks). All three are served from the admin listener
(`[admin]`), which should be bound to loopback in production.

## Prometheus metrics

The `/metrics` endpoint on the admin listener exports a private Prometheus
registry. No build tag is required:

```text
GET http://127.0.0.1:9090/metrics
```

### Compatibility contract

The released baseline was reconstructed from tag `v1.32.0` at commit
`6bb76a08846150663d7eeb9661edb718ef357a7c`: all 28 released `jul_*` families
retain the same names, types, help strings, and label names on current `main`.
Current `main` adds 14 families for egress, reload, staged restart, and managed
apply observability; those additions remain **merged / release pending** until a
tag ships them.

The complete machine-readable contract is
[`docs/metrics-contract.json`](metrics-contract.json). CI compares it with the
actual private registry, separately freezes the `v1.32.0` snapshot in
`internal/observability/testdata/v1.32.0-metrics.json`, and verifies that this
reference and the cardinality table in [core-http.md](core-http.md#label-cardinality-policy)
contain no stale or missing metric names. A released family cannot be removed,
renamed, relabeled, or have its type/help changed accidentally; an intentional
breaking change requires the compatibility/deprecation process rather than a
silent collector edit.

The request `host` label is present in the contract but emitted with an empty
value by default because `Host` is client-controlled. Set
`[observability.metrics].host_label = true` only when the host set is bounded;
changing it requires a restart.

### Exported Jul.IA metric families

| Metric | Type | Labels | Contract | Description |
| --- | --- | --- | --- | --- |
| `jul_acme_renewals_total` | Counter | — | Released `v1.32.0` | ACME certificate renewals observed (expiry advanced for a domain). |
| `jul_auth_decisions_total` | Counter | `method`, `result` | Released `v1.32.0` | Access-control decisions, labeled by method (cidr/basic/jwt/forward) and result (allow/deny). |
| `jul_cache_events_total` | Counter | `state` | Released `v1.32.0` | Response cache outcomes, labeled by state (HIT/MISS/STALE/BYPASS). |
| `jul_config_pending_restart` | Gauge | — | Merged / release pending | 1 when a managed staged-restart candidate is pending (waiting for process restart); 0 otherwise. |
| `jul_config_stage_restart_total` | Counter | `result` | Merged / release pending | Staged-restart apply operations, labeled by result (created/updated/discarded/failed). |
| `jul_discovery_errors_total` | Counter | `pool` | Released `v1.32.0` | Failed or empty service-discovery resolves, labeled by pool (last-good backends are kept). |
| `jul_egress_decisions_total` | Counter | `reason`, `result`, `subsystem` | Merged / release pending | Outbound egress allow-list decisions, labeled by subsystem, result (allow/block), and reason (empty on allow). |
| `jul_egress_dns_answers_total` | Counter | `result`, `subsystem` | Merged / release pending | Egress CIDR-only hostname resolutions evaluated, labeled by subsystem and result (allow/block). |
| `jul_grpc_proxy_streams_total` | Counter | — | Released `v1.32.0` | Native gRPC calls forwarded by the HTTP/2 passthrough proxy (one per call, including each streaming call). |
| `jul_grpc_transcode_requests_total` | Counter | `code`, `method` | Released `v1.32.0` | gRPC-JSON transcoding requests, labeled by gRPC method full name and HTTP status code. |
| `jul_grpc_transcode_stream_msgs_total` | Counter | `direction`, `method` | Released `v1.32.0` | gRPC-JSON transcoding streamed messages, labeled by gRPC method full name and direction (sent to backend / received from backend). |
| `jul_http3_connections` | Gauge | — | Released `v1.32.0` | Current open HTTP/3 (QUIC) connections across all listeners. |
| `jul_http_ratelimited_total` | Counter | `key` | Released `v1.32.0` | Requests rejected by rate limiting, labeled by key kind (ip/header/jwt). |
| `jul_http_request_duration_seconds` | Histogram | `host`, `method` | Released `v1.32.0` | HTTP request latency in seconds. |
| `jul_http_requests_in_flight` | Gauge | — | Released `v1.32.0` | Number of HTTP requests currently being served. |
| `jul_http_requests_total` | Counter | `code`, `host`, `method` | Released `v1.32.0` | Total HTTP requests handled, labeled by method, host, and status code. |
| `jul_http_response_compressed_total` | Counter | `encoding` | Released `v1.32.0` | Responses compressed by the edge, labeled by content coding. |
| `jul_listener_conns` | Gauge | — | Released `v1.32.0` | Current concurrent connections across all listeners. |
| `jul_managed_apply_finalization_errors_total` | Counter | `component` | Merged / release pending | Managed-apply finalization/restoration failures, labeled by the bounded component that failed (restoration/pending/registry/callback_panic). An increment means the failure was made explicit and surfaced through logs/health/ledger rather than silently discarded. |
| `jul_managed_apply_finalized_total` | Counter | `mode`, `operation`, `outcome`, `restored` | Merged / release pending | Terminal async managed-apply outcomes, labeled by operation, mode, outcome and whether restoration succeeded (true/false/n/a). |
| `jul_managed_apply_history_total` | Counter | `operation`, `result` | Merged / release pending | Configuration-history snapshot attempts made by the terminal managed-apply finalizer (WS02 §3.7), labeled by operation and result (recorded/skipped/failed). |
| `jul_managed_apply_terminal_lookup_total` | Counter | `result` | Merged / release pending | Exact-ID managed-apply lookups, labeled by bounded result (pending/finalizing/terminal/missing/invalid). |
| `jul_managed_apply_terminal_registry_entries` | Gauge | — | Merged / release pending | Number of terminal managed-apply records currently retained in the bounded ledger. |
| `jul_mtls_handshakes_total` | Counter | `result` | Released `v1.32.0` | Mutual-TLS handshakes presenting a CA-verified client certificate, labeled by result (verified/rejected). Certificates failing CA-chain verification are rejected by the TLS stack before this counter; a missing certificate denied per location is counted as a 403 in jul_http_requests_total. |
| `jul_plugin_duration_seconds` | Histogram | `plugin` | Released `v1.32.0` | WASM plugin invocation latency in seconds, labeled by plugin name. |
| `jul_plugin_invocations_total` | Counter | `plugin`, `result` | Released `v1.32.0` | WASM plugin invocations, labeled by plugin name and result (continue/stop/error). |
| `jul_plugin_panics_total` | Counter | `plugin` | Released `v1.32.0` | WASM plugin traps/panics contained by the host, labeled by plugin name. |
| `jul_reload_duration_seconds` | Histogram | `outcome`, `source` | Merged / release pending | Configuration reload latency in seconds, labeled by source and outcome. |
| `jul_reload_in_progress` | Gauge | — | Merged / release pending | 1 while a configuration reload transaction is in flight; 0 otherwise. |
| `jul_reload_phase_duration_seconds` | Histogram | `outcome`, `phase` | Merged / release pending | Latency of individual reload phases (resolve/validate/lifecycle/prepare/stage_listeners/publish/activate), labeled by phase and outcome. |
| `jul_reload_timeout_total` | Counter | `phase` | Merged / release pending | Configuration reloads that exceeded their deadline, labeled by the phase that timed out. |
| `jul_reload_total` | Counter | `outcome`, `source` | Merged / release pending | Configuration reloads, labeled by source (admin/sighup/watch) and outcome (applied_live/applied_degraded/not_applied/saved_not_live). |
| `jul_stream_active_conns` | Gauge | `proto` | Released `v1.32.0` | Current active L4 stream connections/sessions, labeled by protocol (tcp/udp). |
| `jul_stream_bytes_total` | Counter | `direction`, `proto` | Released `v1.32.0` | Bytes relayed by the L4 stream proxy, labeled by protocol (tcp/udp) and direction (up to backend / down to client). |
| `jul_stream_udp_sessions_evicted_total` | Counter | `reason` | Released `v1.32.0` | UDP sessions removed by the L4 stream proxy to enforce limits, labeled by reason: 'idle' (reaped after idle_timeout) or 'lru' (reclaimed to admit a new client at the session cap). |
| `jul_stream_udp_sessions_rejected_total` | Counter | — | Released `v1.32.0` | New UDP clients dropped because a listener's max_udp_sessions cap was reached and no session was reclaimable. |
| `jul_tls_cert_expiry_seconds` | Gauge | `domain` | Released `v1.32.0` | Leaf certificate expiry as a Unix timestamp, labeled by domain. |
| `jul_upstream_backends` | Gauge | `pool` | Released `v1.32.0` | Current number of backends in a pool, labeled by pool (tracks dynamic service discovery). |
| `jul_upstream_healthy` | Gauge | `backend`, `pool` | Released `v1.32.0` | Active health-check verdict per backend (1 healthy, 0 unhealthy), labeled by pool and backend. |
| `jul_upstream_probe_duration_seconds` | Histogram | `pool` | Released `v1.32.0` | Active health-check probe latency in seconds, labeled by pool. |
| `jul_upstream_probes_total` | Counter | `pool`, `result` | Released `v1.32.0` | Active health-check probes, labeled by pool and result (success/failure). |
| `jul_waf_events_total` | Counter | `action`, `rule` | Released `v1.32.0` | Web-application-firewall rule matches, labeled by action (block/detect) and matched rule ID. |

Metric labels must remain bounded and must never contain request paths, queries,
client identity, destination URLs, raw errors, or secrets. See the
[cardinality policy and operator playbook](core-http.md#label-cardinality-policy).

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
enabled       = true
sinks         = ["stdout", "file"]
file          = "/var/log/jul/access.log"
format        = "json"          # applies to file and syslog only
rotate_max_mb = 100
rotate_keep   = 7
```

Omitting `enabled` preserves the default-on v1 behavior. Set `enabled = false`
to stop request access records in stdout, file, syslog, and the Console access
record tail. Jul then opens no access-log file or syslog resource. Application,
reload, security/WAF, audit, health, metric, and trace output remain active.
Dormant sink settings are retained and validated for deterministic re-enable.

When enabled, an omitted sink list selects `stdout`; an explicit `sinks = []`
is invalid and the error directs the operator to disable the block instead. The
`file` and `syslog` sinks always record at **info level** regardless of
`[global].log_level`, so a quieter global level never suppresses the access log.
The legacy global/per-server destination fields are deprecated compatibility
no-ops and produce lint warnings.

### Access-log fields

Each access-log entry includes:

- `ts` — ISO 8601 timestamp
- `method`, `path`, `host`, `remote_addr`
- `status`, `bytes_sent`, `duration_ms`
- `upstream_addr`, `upstream_status`, `upstream_duration_ms` (when proxied)
- `cache` — `HIT`, `MISS`, `STALE`, or `BYPASS`
- `trace_id` — when tracing is active
- `error` — when the request failed with an error

### WAF matched-rule logs

A WAF match emits `waf rule matched` at warning level with bounded fields:
`rule_id`, `mode`, `phase`, `severity`, a sanitized `path`, and
`query_omitted`. The path uses the same privacy-preserving sanitizer as Console
request diagnostics: it removes query/fragment text without decoding, normalizes
identifier/email/token-like segments, applies a 256-byte cap, and passes through
the active secret-redaction state.

Jul.IA deliberately does **not** copy Coraza's raw `MatchedRule.URI()` or
macro-expanded `MatchedRule.Message()` into this warning. In Coraza v3.7.0 those
values may contain the full unparsed query and matched request data. WAF metric
labels remain bounded to action/rule metadata and never include path, URI, query,
message, or matched values.

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
- [ ] Monitor `jul_tls_cert_expiry_seconds` for upcoming certificate renewals.
- [ ] Use `/readyz` for orchestrator readiness probes.

## Benchmarks & tuning

For a catalogue of in-tree benchmarks, how to run the harness, and production
tuning recommendations (connection pooling, cache sizing, compression levels,
worker limits), see [benchmarks.md](benchmarks.md).
