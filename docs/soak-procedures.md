# Jul.IA — Soak Procedures (Linux)

> Version 2.0 · Updated 2026-09-17
>
> Rewritten from scratch (JUL-AUD-006): the previous version documented only
> the in-tree `go test -tags soak` scenarios on Windows/PowerShell, but every
> authoritative run recorded in [soak-evidence.md](soak-evidence.md) actually
> used the `burn-in-*.toml` + `scripts/burn-in-*.go` real-binary harness on
> Linux. This version documents the harness that is actually used.
>
> How to read the pass/fail signals is summarised in
> [troubleshooting.md](troubleshooting.md#soak--load-test-interpretation).
> Dated run evidence lives in [soak-evidence.md](soak-evidence.md); this page
> is the procedure, not the log.

## Why Linux, not Windows

The proxy load pattern dials a fresh connection at a meaningful rate; on
Windows this exhausts ephemeral client-side ports within about two minutes at
16 workers, which is a client-OS limitation, not a server leak. Windows is
therefore only viable for a ≤20s smoke run. Run every procedure below on
Linux. The in-tree `go test -tags soak` scenarios (Procedure 0) remain a
useful pre-flight smoke and are cross-platform, but they are not a substitute
for a real-binary burn-in: they exercise one handler directly through
`httptest.NewServer`, not the admin API, RBAC, TLS, discovery, or the config
apply/reload path.

## Prerequisites

- [ ] Linux host that can run unattended for the target duration
- [ ] Go 1.26+ installed (`go version`)
- [ ] Repository cloned, working tree clean, exact commit SHA recorded
  (`git rev-parse HEAD`)
- [ ] `make config-check` passes (every shipped `.toml` still loads)
- [ ] `make soak-repro-smoke` passes (the documented reproduction commands
  below actually run)
- [ ] No other process bound to the ports a chosen profile uses (`8080-8082`,
  `8443-8444`, `9090`, `15432`, `55432-55433` across the various profiles —
  check the profile's own header comment)
- [ ] A `soak-artifacts/<date>-<scope>/` directory created via
  `make soak-manifest-init SCOPE=<scope>` (see
  [soak-artifacts/README.md](../soak-artifacts/README.md)) — **do this before
  starting the run**, not after

## The harness

| Component | Role |
| --- | --- |
| `burn-in-*.toml` | Real server configs, one per scenario. `burn-in-current.toml` is the consolidated profile covering every merged-Beta capability (JUL-AUD-004); the others are single-feature or historical-regression profiles. |
| `scripts/burn-in-backend.go` | HTTP backend. `-port N` (TCP), `-unix /path.sock` (HTTP-over-Unix, #407), `-tls` (HTTPS, for `backend_tls`). Also serves `/…/slow?ms=N` (deliberately slow response), `/…/flaky?rate=N` (intermittent 500s), `/…/reset` (mid-body TCP RST via `SO_LINGER 0`), `/…/malformed[?kind=bad-chunk]` (declared-but-unfulfilled `Content-Length`, or an invalid chunk-size line), and `POST /control/kill?duration=Ns` (refuses every path for the window, then auto-restores — a scheduled kill/restore cycle without actually stopping the process) for fault-injection load patterns (JUL-AUD-019). |
| `scripts/stream-echo.go` | TCP echo backend for `[[stream]]` L4 profiles. |
| `scripts/burn-in-load.go` | HTTP/HTTPS load generator. Mode flags select the traffic pattern (see below); `-duration`/`-workers` control load. |
| `scripts/burn-in-stream-load.go` | L4 TCP load generator (`-target host:port`). |
| `scripts/soak.sh` (`make soak`) | The three in-tree `go test -tags soak` scenarios — pre-flight smoke, not the soak itself. |
| `scripts/soak-repro-smoke.sh` (`make soak-repro-smoke`) | Runs the #287 resilience-soak reproduction at trivial duration; a CI gate against this exact class of doc rot. |
| `scripts/soak-manifest-init.sh` (`make soak-manifest-init`) | Creates a dated, pre-filled evidence directory (JUL-AUD-018). |

### Load-generator mode flags (`scripts/burn-in-load.go`)

| Flag | Exercises |
| --- | --- |
| `-full` | The July Phase 2A feature set (cache, rate limit, WAF, auth, compression, TLS/mTLS) — use with `burn-in-full.toml`. |
| `-phase2a` | Transcoding, passthrough, discovery, secrets, zero-config, WASM — use with `burn-in-phase2a.toml`. |
| `-current` | The merged-Beta surface: resilience pools, Unix upstream, DNS discovery, `backend_tls`, routing predicates/response headers/CORS, WASM plugin — use with `burn-in-current.toml` (JUL-AUD-004). |
| `-cache`, `-ratelimit`, `-waf`, `-compress`, `-http3` | Single-feature patterns for the matching `burn-in-<feature>.toml`. |
| `-slow-client` | Paces a POST body over ~3.2s, exercising slow-client/read-timeout handling. |
| `-slow-upstream` | Requests `/bounded/slow?ms=N`, exercising pending-timeout/circuit accounting against a genuinely slow backend. |
| `-fault` | A weighted mix of every failure class the backend can inject against `/bounded/` (5xx storms, slow responses, mid-body TCP resets, malformed framing), plus a separate goroutine that schedules a kill/restore cycle directly against each backend in turn — exercising retry/circuit/admission behavior against a genuinely, alternately unhealthy pool rather than a clean one. |
| `-rbac` | Runs a concurrent allow/deny probe against `-admin` using `burn-in-current.toml`'s viewer/operator/admin principals. |
| `-apply-churn` | Runs a concurrent config-apply churn against `-admin`, resubmitting `-applyConfig` (default `burn-in-current.toml`) as a semantic no-op reload every `-applyEvery` (default 10s). Adopts the on-disk file as the managed baseline automatically on first use. Requires `config_authority = "managed"` in the target config. |

Combine independent modes freely (e.g. run `-current` in one window and
`-rbac` plus `-apply-churn` in another against the same server) — each is a
separate process. Do not combine two request-pattern flags in the *same*
invocation; the tool selects one pattern per process by design.

## Procedure 0 — in-tree smoke (2–10 minutes, any OS)

Not a soak; a pre-flight check that the soak-relevant code paths have not
regressed before spending real wall-clock time.

```sh
make soak                    # 30s default per scenario (proxy, cache, udp-churn)
make soak-repro-smoke        # the #287 reproduction, at trivial duration
make config-check            # every shipped .toml still loads
```

All three must pass before proceeding to a real-binary run.

## Procedure A — 5-minute local validation

**Goal:** reproduce the CI release gate's scale locally before a real run.
**When:** before every version tag, after any reload-timeout, connection-pool,
or middleware/handler change.

```sh
make soak-manifest-init SCOPE=validation-5m
DIR=$(ls -td soak-artifacts/*-validation-5m | head -1)

FULL_TAGS="brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf"
go build -tags "$FULL_TAGS" -o jul ./cmd/jul

go run scripts/burn-in-backend.go -port 8081 &
go run scripts/burn-in-backend.go -port 8082 &
go run scripts/burn-in-backend.go -unix /tmp/jul-burnin-current.sock &
go run scripts/burn-in-backend.go -port 8444 -tls &
go run scripts/stream-echo.go -port 55432 &
sleep 2
./jul -config burn-in-current.toml > "$DIR/jul.log" 2>&1 &

go run scripts/burn-in-load.go -duration 5m -workers 32 -current \
  -health "http://127.0.0.1:8080/bounded/" | tee "$DIR/load-current.log"
go run scripts/burn-in-stream-load.go -duration 5m -workers 16 -target 127.0.0.1:15432 \
  | tee "$DIR/load-stream.log"
```

**Pass criteria:** `errors=0` / `HTTP 5xx = 0`; goroutine growth ≤ `4×workers+32`;
heap growth ≤ 64 MiB. See [Stability indicators](#stability-indicators) below
for what a failure looks like.

## Procedure B — 1-hour stability run

**Goal:** catch slow leaks a 5-minute run misses.
**When:** after any allocator/pooling change (cache, buffer pool), before a
minor release.

Same stack and commands as Procedure A, with `-duration 1h` on both load
generators. `burn-in-current.toml` already includes the bounded/unlimited
resilience pools, so no second profile or port remapping is needed.


## Procedure C — final soak (≥24 hours)

**Goal:** the ADR-0005 release gate. Long enough for memory/goroutine/FD
trends, hundreds of config applies, and multi-hour stream/connection
accounting to become statistically meaningful.
**When:** before a GA declaration or a major version tag.

### Entry criteria

1. `main` green on every gate in this repo (`make ci-pr`, plus `-race` via
   `make test-race`) at the exact soak SHA.
2. Procedure 0 and Procedure A both pass at that SHA.
3. `soak-artifacts/<date>-final/MANIFEST.md` created
   (`make soak-manifest-init SCOPE=final`).
4. A metrics scrape (Prometheus or equivalent) configured against `/metrics`
   at ≤15s interval, retained for the full run — not stdout tailing.
5. RBAC, `[egress]`, `client_address`, and `backend_tls` all enabled for the
   run (i.e. run `burn-in-current.toml`, not a single-feature profile alone),
   so the soak's evidence covers capabilities that have never been soaked.

### Workload

Run every scenario the profiles below cover, concurrently, for the same
≥24h window:

```sh
DIR=$(ls -td soak-artifacts/*-final | head -1)
FULL_TAGS="brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf"
go build -tags "$FULL_TAGS" -o jul ./cmd/jul
cp burn-in-current.toml "$DIR/"
git rev-parse HEAD > "$DIR/build-sha.txt"

# Backends
go run scripts/burn-in-backend.go -port 8081 &
go run scripts/burn-in-backend.go -port 8082 &
go run scripts/burn-in-backend.go -unix /tmp/jul-burnin-current.sock &
go run scripts/burn-in-backend.go -port 8444 -tls &
go run scripts/stream-echo.go -port 55432 &
sleep 2

# Server
./jul -config burn-in-current.toml > "$DIR/jul.log" 2>&1 &

# Load: the merged-Beta surface, sustained
go run scripts/burn-in-load.go -duration 24h -workers 64 -current \
  -health "http://127.0.0.1:8080/bounded/" | tee "$DIR/load-current.log" &

# Load: L4 stream
go run scripts/burn-in-stream-load.go -duration 24h -workers 16 \
  -target 127.0.0.1:15432 | tee "$DIR/load-stream.log" &

# RBAC allow/deny probe, continuous
go run scripts/burn-in-load.go -duration 24h -workers 1 -rbac \
  -admin "https://127.0.0.1:9090" -health "http://127.0.0.1:8080/bounded/" \
  | tee "$DIR/rbac-probe.log" &

# Config-apply churn, continuous (requires config_authority = "managed",
# already set in burn-in-current.toml)
go run scripts/burn-in-load.go -duration 24h -workers 1 -apply-churn \
  -applyEvery 5m -admin "https://127.0.0.1:9090" \
  -health "http://127.0.0.1:8080/bounded/" | tee "$DIR/apply-churn.log" &

# Fault injection, continuous: 5xx storms, slow responses, mid-body resets,
# malformed framing against /bounded/, plus a scheduled kill/restore cycle
# against each backend in turn (JUL-AUD-019)
go run scripts/burn-in-load.go -duration 24h -workers 4 -fault \
  -killEvery 10m -killFor 30s -health "http://127.0.0.1:8080/bounded/" \
  | tee "$DIR/fault.log" &
```

### Fault injection (required — see rationale below)

A clean-path 24-hour run proves memory/goroutine bounds but proves **nothing**
about the resilience capabilities (admission, retry, circuit) the run exists
to certify. `-fault` (added to the workload above) automates the request-path
and backend-outage fault classes continuously for the whole run; the
remaining rows are host-level conditions `-fault` cannot reach from inside the
process and must still be scheduled manually, logged into `$DIR/MANIFEST.md`'s
event log with a UTC timestamp:

| Fault | How |
| --- | --- |
| Backend 5xx storm, mid-body reset, malformed framing, slow response | Automated by `-fault` in the workload above — no manual step |
| Backend kill/restore | Automated by `-fault`'s scheduled kill/restore cycle (`-killEvery`/`-killFor`) — no manual step; for a true process-level kill instead of the in-process kill-switch, `kill` one `burn-in-backend.go` instance for 1–2 minutes and restart it on the same port |
| DNS failure | Point `[[upstreams]] discovery.dns` at a name that stops resolving mid-run (edit `/etc/hosts` or firewall off the resolver), confirm the `discovered` pool holds its last-known-good targets rather than emptying |
| FD-limit reduction | `ulimit -n 512` in the shell that launches `jul` (or `LimitNOFILE=` in a systemd override), confirm admission/backpressure rather than a crash once the limit is approached |
| Disk pressure | Fill `jul-data/cache-disk` toward its configured cap (e.g. `fallocate -l <size> jul-data/cache-disk/filler`) and confirm eviction, not failure |
| cgroup CPU/memory constraint | Run `jul` under `systemd-run --scope -p MemoryMax=256M -p CPUQuota=50%` (or an equivalent container limit) and confirm graceful degradation (GC pressure, slower responses) rather than an OOM kill under the expected workload |
| Admin op during apply | Send a deliberately invalid config via a one-off `curl` with a broken TOML body mid-run; confirm the live config is unaffected |

Do not add fault modes beyond this table "for completeness" — each one must
map to a documented recovery behavior in the exit criteria below, or it is
noise a reviewer has to explain away.

> **Expect a high error rate while `-fault` is running against a small
> (two-backend) pool.** If both backends happen to be marked down at the same
> moment (reset/malformed errors and the kill cycle are independent and can
> overlap), the pool has zero healthy backends and every request fast-fails
> with 503 until one recovers. That is the circuit breaker and admission
> control doing their job, not a regression — see the exit criteria's
> distinction between an *expected* fault-window failure and an *unexplained*
> one.

### Observability

Scrape `/metrics` at ≤15s. At minimum retain:
`jul_http_requests_total`, `jul_http_request_duration_seconds`,
`jul_upstream_active_requests`, `jul_upstream_pending_requests`,
`jul_upstream_admission_rejected_total`, `jul_upstream_circuit_state`,
`jul_upstream_circuit_transitions_total`, `jul_upstream_retry_attempts_total`,
`jul_upstream_retry_budget_denied_total`,
`jul_cache_bytes`, `jul_cache_max_bytes`, `jul_cache_entries`,
`jul_cache_evictions_total` (JUL-AUD-005), `jul_cache_events_total`,
`jul_reload_total`, `jul_reload_duration_seconds`, `jul_reload_in_progress`,
`jul_reload_timeout_total`, `jul_managed_apply_finalized_total`,
`jul_managed_apply_finalization_errors_total`, `jul_transport_retired_total`,
`jul_plugin_invocations_total`, `jul_plugin_panics_total`,
`jul_stream_active_conns`, `jul_stream_udp_sessions_evicted_total`,
`jul_mtls_handshakes_total`, `jul_waf_events_total`, `jul_egress_decisions_total`,
`jul_client_addr_derivations_total`, plus `go_goroutines`, `go_memstats_*`,
`process_resident_memory_bytes`, `process_open_fds`, `process_cpu_seconds_total`.

Capture heap and goroutine pprof profiles at T0, T+2h, T+12h, T+24h into
`$DIR/`:

```sh
for suffix in T0 T2h T12h T24h; do   # run at the appropriate wall-clock time
  curl -sk -H "Authorization: Bearer <admin-role-token>" \
    "https://127.0.0.1:9090/debug/pprof/heap?debug=1" -o "$DIR/heap-$suffix.out"
  curl -sk -H "Authorization: Bearer <admin-role-token>" \
    "https://127.0.0.1:9090/debug/pprof/goroutine?debug=1" -o "$DIR/goroutine-$suffix.out"
done
```

### Stability indicators

| Symptom | Signal |
| --- | --- |
| Memory leak | `process_resident_memory_bytes`/heap rising monotonically after a 2h warm-up, tracking cumulative requests rather than concurrency |
| Goroutine leak | `go_goroutines` trending with cumulative load rather than concurrency |
| FD/socket leak | `process_open_fds` not returning to baseline at a load pause |
| Admission-slot leak | `jul_upstream_active_requests` floor creeping upward across hours |
| Queue unboundedness | any `jul_upstream_pending_requests` excursion above configured `max_pending_requests` |
| Cache runaway | occupancy gauge exceeding configured max, or the disk tier growing without eviction |
| Reload instability | `jul_reload_duration_seconds` p95 drifting upward across applies, or any `jul_reload_timeout_total`/`jul_managed_apply_finalization_errors_total` increment |
| Drain failure | any `jul_transport_retired_total{mode="forced"}` |

### Exit criteria

1. ≥24h continuous, single process, no unplanned restart.
2. Zero unexplained client errors — every 4xx/5xx attributable to an injected
   fault or an expected policy decision (429/403).
3. RSS/heap plateau: last-6h slope ≤ +1%/h, absolute growth after warm-up
   ≤ 64 MiB.
4. `go_goroutines` end-of-run within the bounded gate (`≤ 4×workers+32`) of
   the post-warm-up baseline.
5. `process_open_fds` returns to within 5% of baseline at each load pause.
6. `jul_upstream_active_requests` reaches exactly 0 at every load pause.
7. `jul_upstream_pending_requests` never exceeds configuration.
8. Cache occupancy never exceeds configured maxima; the disk tier
   demonstrably evicts.
9. `-apply-churn` accumulates ≥200 successful applies;
   `jul_reload_timeout_total` = 0; `jul_managed_apply_finalization_errors_total` = 0;
   `jul_transport_retired_total{mode="forced"}` = 0.
10. `-rbac` probe reports `violations=0` for the whole run.
11. Every injected fault produced the expected behavior (circuit opened,
    budget denied, degraded response, or a clean 4xx) **and** full recovery
    afterward.
12. Zero `jul_plugin_panics_total`.
13. Zero secret values in any retained log or artifact (`grep` the directory
    before committing anything).

### After the run

1. Fill in the remaining sections of `$DIR/MANIFEST.md` (event log, metric
   snapshot location, exit-criteria table, conclusion).
2. Append a dated entry to [soak-evidence.md](soak-evidence.md) linking to
   `$DIR/`.
3. If any exit criterion failed, file it as a focused issue with exact
   reproduction before considering the release — do not weaken the criterion
   to make the run "pass" (see ADR 0017 Amendment 4 for the precedent: when an
   acceptance criterion proved unachievable by design, the criterion was
   amended in public with reasoning, not the measurement).

## Interpreting a failure

| Failure mode | Likely cause | Action |
| --- | --- | --- |
| `errors > 0` / `HTTP 5xx > 0` (unexpected) | Handler panic, connection reset, backend failure | Check `jul.log` for a stack trace |
| Goroutine growth beyond bound | Leak in handler, middleware, or connection pool | Compare `goroutine-T0.out` vs the latest capture |
| Heap growth beyond bound | Object retention (cache, buffer pool, session table) | Compare `heap-T0.out` vs the latest capture; check `jul_cache_bytes` |
| Process exits early | Critical bug (panic, deadlock) | Full stack trace in `jul.log`; release blocker |
| `-rbac` reports violations | A permission boundary regressed | Reproduce with a single `curl` against the failing check in the log |
| `-apply-churn` failures | Config-apply/reload regression, or the profile drifted from what `adopt-external` expects | Reproduce manually: `curl` the same `/api/v1/config` → `/api/v1/config/apply` sequence and inspect the error body |

## Related documents

- [soak-evidence.md](soak-evidence.md) — dated run log and artifact links
- [soak-artifacts/README.md](../soak-artifacts/README.md) — the evidence
  retention convention (JUL-AUD-018)
- [docs/audit/2026-09-16-pre-soak-readiness-audit.md](audit/2026-09-16-pre-soak-readiness-audit.md) —
  the audit that identified this document's prior drift from the actual
  harness (JUL-AUD-006) and the full proposed soak plan this page implements
- [ADR 0005](adr/0005-soak-post-ga-gate.md) — why soak is a post-GA gate
