# Active Health Checks

> **Maturity:** Beta (see [status.md](status.md)).

Jul.IA supports **active** health checking for upstream pools: the server periodically probes each backend and ejects unhealthy backends from the balancer rotation. This complements the built-in **passive** health checking (which parks a backend after `max_fails` consecutive request failures).

## Quick start

```toml
[[upstreams]]
name     = "api"
strategy = "round_robin"
servers  = ["127.0.0.1:3000", "127.0.0.1:3001"]

  [upstreams.health_check]
  enabled  = true
  type     = "http"
  path     = "/healthz"
  interval = "5s"
  timeout  = "2s"
```

## How it works

Each upstream pool with active health checks gets a dedicated goroutine that probes every backend on a timer. A backend is:

- **Ejected** after `unhealthy_threshold` consecutive failed probes.
- **Restored** after `healthy_threshold` consecutive successful probes.

The active verdict combines with passive health: a backend is considered healthy only when **both** signals agree it is healthy. A backend that fails active probes is taken out of rotation even if passive traffic has not yet tripped `max_fails`.

## Configuration reference

Active health checks are configured inside an `[[upstreams]]` block under `[upstreams.health_check]`.

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `enabled` | bool | `false` | Enable active health checking for this pool. |
| `type` | string | `"http"` | Probe protocol: `"http"` or `"tcp"`. |
| `path` | string | `""` | Request path for HTTP probes (required when `type = "http"`). |
| `interval` | duration | `"5s"` | Delay between probe rounds. |
| `timeout` | duration | `"2s"` (or `interval/2`) | Timeout for a single probe. Must be less than `interval`. |
| `healthy_threshold` | int | `2` | Consecutive successes to mark a backend healthy. |
| `unhealthy_threshold` | int | `3` | Consecutive failures to eject a backend. |
| `expect_status` | []int | `[200]` | Acceptable HTTP status codes. Ignored for TCP probes. |
| `expect_body` | string | `""` | Optional substring the HTTP response body must contain. Ignored for TCP probes. |

### TCP probes

A TCP probe opens a connection and considers it passing if the TCP handshake succeeds. No request is sent.

```toml
[upstreams.health_check]
enabled = true
type    = "tcp"
interval = "3s"
timeout  = "1s"
healthy_threshold   = 1
unhealthy_threshold = 2
```

### HTTP probes

An HTTP probe sends a `GET` request to the configured `path`. Redirects are **not** followed — a 3xx is a failure unless it is listed in `expect_status`.

```toml
[upstreams.health_check]
enabled  = true
type     = "http"
path     = "/ready"
interval = "10s"
timeout  = "2s"
expect_status = [200, 204]
expect_body   = "ok"
```

## Defaults and validation

- `type` defaults to `"http"` if omitted or empty.
- `interval` defaults to `5s`; `timeout` defaults to `interval/2` or `2s`, whichever is smaller.
- `timeout` must be **strictly less than** `interval`.
- `healthy_threshold` defaults to `2`; `unhealthy_threshold` defaults to `3`.
- For HTTP probes, `expect_status` defaults to `[200]` when omitted.
- `path` is **required** for HTTP probes.

Validation errors surface during `jul check` or at startup.

## Integration with load balancing

Active health state feeds directly into the balancer:

| Pool strategy | Unhealthy backend behaviour |
| --- | --- |
| `round_robin` | Skipped during rotation |
| `weighted_round_robin` | Skipped; weight is ignored |
| `least_conn` | Skipped; in-flight count is ignored |

The `jul_upstream_healthy` gauge (`pool`, `backend` labels) reflects the active health verdict: `1` when healthy, `0` when not. `jul_upstream_probes_total` counts probes by `pool` and `result` (`success` / `failure`).

## Reload behaviour

Health-check goroutines are bound to the pool lifecycle. On reload:

1. New pools start with the new health-check configuration.
2. Removed pools shut down their checker goroutine.
3. Unchanged pools keep their checker running (no interruption).

Changing `[upstreams.health_check]` on an existing upstream therefore does **not** require a restart; the next reload adopts the new parameters.

## Conformance matrix

The matrix below enumerates every supported behaviour for each probe type so
config authors know what is available and what is not.

| Behaviour | `http` probe | `tcp` probe | Notes |
| --- | :-: | :-: | --- |
| Periodic probe with configurable interval | ✅ | ✅ | Driven by `interval` |
| Per-backend timeout | ✅ | ✅ | Driven by `timeout` (must be `< interval`) |
| Success/failure hysteresis (thresholds) | ✅ | ✅ | `healthy_threshold` / `unhealthy_threshold` |
| Status-code matching | ✅ | n/a | `expect_status` (default `[200]`) |
| Response-body substring matching | ✅ | n/a | `expect_body` (optional, up to 64 KiB) |
| Fresh connection per probe | ✅ | ✅ | HTTP: `DisableKeepAlives`; TCP: dial+close |
| Redirect following | ☐ | n/a | 3xx is treated as failure unless listed in `expect_status` |
| Custom HTTP method (`HEAD`, `POST`, …) | ☐ | n/a | Only `GET` is supported |
| Custom request headers | ☐ | n/a | Not supported |
| TLS certificate verification | ☐ | n/a | `InsecureSkipVerify: true` by design |
| gRPC health-check protocol | ☐ | n/a | Not supported (use TCP probe as a coarse substitute) |
| Prometheus gauge per backend | ✅ | ✅ | `jul_upstream_healthy` |
| Prometheus probe counter + latency histogram | ✅ | ✅ | `jul_upstream_probes_total`, `jul_upstream_probe_duration_seconds` |
| Flapping detection (transition history) | ✅ | ✅ | `healthHistoryTracker` — ≥ 4 transitions in 5 min |
| Console Status integration | ✅ | ✅ | Pool count + per-backend health in Admin API |
| Zero-downtime reload — adopt new params | ✅ | ✅ | Unchanged pools keep running; changed pools restart checker |

## Known limitations

- **No shared state across instances:** Each Jul.IA process probes independently. In a multi-instance deployment, health state is local to each process.
- **HTTP probes use a fresh connection per probe:** Keep-alive is disabled so that a broken pooled connection cannot mask an unhealthy backend.
- **TLS is not verified for HTTP probes:** The probe `http.Client` uses an unverified TLS config (`InsecureSkipVerify: true`) so that a backend with a self-signed or mismatched certificate is not falsely marked unhealthy. If TLS verification is required for your health endpoint, use a separate HTTP server block or a TCP probe.
- **Only `GET` is supported:** HTTP probes always use `GET`. There is no support for `HEAD`, `POST`, or custom headers.

## Metrics

See [observability.md](observability.md) for the full metrics reference. Health-check relevant metrics:

| Metric | Labels | Description |
| --- | --- | --- |
| `jul_upstream_healthy` | `pool`, `backend` | `1` = healthy, `0` = unhealthy (active verdict) |
| `jul_upstream_probes_total` | `pool`, `result` | Active probe outcomes |
| `jul_upstream_probe_duration_seconds` | `pool` | Probe latency histogram |

## Example: full configuration

```toml
[[servers]]
listen = "0.0.0.0:8080"

  [[servers.locations]]
  match      = { type = "prefix", path = "/api/" }
  proxy_pass = "http://api"

[[upstreams]]
name     = "api"
strategy = "round_robin"
servers  = ["10.0.0.1:3000", "10.0.0.2:3000", "10.0.0.3:3000"]

  [upstreams.health_check]
  enabled             = true
  type                = "http"
  path                = "/healthz"
  interval            = "5s"
  timeout             = "2s"
  healthy_threshold   = 2
  unhealthy_threshold = 3
  expect_status       = [200]
```

Validate with `jul check` before starting.

## GA status

Active health checks have reached **GA** against the [ADR 0003](adr/0003-maturity-and-ga.md) bar.

| Criterion | Status | Evidence |
| --- | :-: | --- |
| 1. Conformance / behaviour matrix | ✅ | Table above |
| 2. Published benchmark numbers | ✅ | Upstream `BenchmarkBalancer*` (balancer_bench_test.go) covers picker with health state |
| 3. Known-limitations list | ✅ | Section above |
| 4. Semver-guarded config/API contract | ✅ | Covered by v1 freeze (compatibility.md) |
| 5. Long-running soak test | ✅ | soaked 8h windows 2026-07-04 (/healthz polled 960×, all 200) — [evidence](soak-evidence.md#2026-07-04--track-2-extended-burn-in-local-windows-8-hours-50-workers) |
| 6. Runnable example + docs | ✅ | `testdata/health.toml`, this doc |
| 7. Security / threat note | ✅ | TLS skip-verify rationale in Known limitations |
| 8. Fuzzing where parsing is involved | n/a | No custom parser (uses standard `net/http`, `net` stack) |
| 9. Self-explanatory Console surface | ✅ | Status panel counts + per-backend health |

Soak gate (post-GA, ADR 0005): tracked in [status.md](status.md#soak-tracking-post-ga-gate).
