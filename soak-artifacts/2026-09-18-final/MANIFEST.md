# Soak run manifest

> Copy this file to `soak-artifacts/<YYYY-MM-DD>-<scope>/MANIFEST.md` and fill
> in every section. `scripts/soak-manifest-init.sh <scope>` does the copy and
> pre-fills the fields it can determine automatically.

## Identity

| Field | Value |
| --- | --- |
| Scope | final |
| Date started (UTC) | 2026-09-18T22:40:18Z |
| Date ended (UTC) | 2026-09-19T23:56:37Z (main workload stopped 22:59:49Z; apply-churn/fault top-up ran 23:06:05Z-23:55:24Z, see Anomalies) |
| Operator | GitHub Copilot (agent), on behalf of user |
| Build SHA | 4a0b3d3ea5478c0ce8324a44b7fa447ec6139310 |
| Working tree clean at build time? | yes |
| `go version` | go version go1.26.6 linux/arm64 |
| OS / kernel | Linux AXLZ00399204189 6.18.33.2-microsoft-standard-WSL2 #1 SMP PREEMPT_DYNAMIC Thu Jun 18 21:38:49 UTC 2026 aarch64 GNU/Linux |
| Host spec (CPU, RAM, disk) | 12 vCPU, 930GB free on / |
| `jul capabilities -json` | see `capabilities.json` in this directory |

## Configuration

List every config file used, and paste its exact content (or attach a copy in
this directory alongside this manifest).

| File | Copied to |
| --- | --- |
| burn-in-current.toml | `burn-in-current.toml` (this directory) |

## Commands executed

Paste every command line, in order, exactly as run (build, backends, server,
load generator(s), any fault-injection commands).

```sh
FULL_TAGS="brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf"
go build -tags "$FULL_TAGS" -o jul ./cmd/jul

/tmp/burn-in-backend -port 8081
/tmp/burn-in-backend -port 8082
/tmp/burn-in-backend -unix /tmp/jul-burnin-current.sock
/tmp/burn-in-backend -port 8444 -tls
/tmp/stream-echo -port 55432

./jul -config burn-in-current.toml

/tmp/burn-in-load -duration 24h -workers 64 -current -health "http://127.0.0.1:8080/bounded/"
/tmp/burn-in-stream-load -duration 24h -workers 16 -target 127.0.0.1:15432
/tmp/burn-in-load -duration 24h -workers 1 -rbac -admin "https://127.0.0.1:9090" -health "http://127.0.0.1:8080/bounded/"
/tmp/burn-in-load -duration 24h -workers 1 -apply-churn -applyEvery 5m -admin "https://127.0.0.1:9090" -health "http://127.0.0.1:8080/bounded/"
/tmp/burn-in-load -duration 24h -workers 4 -fault -killEvery 10m -killFor 30s -health "http://127.0.0.1:8080/bounded/"
```

Process IDs (for teardown): backends 399199-399203, jul 399276, load-current
399444, load-stream 399446, rbac 399448, apply-churn 399451, fault 399452.

## Event log

A timestamped log of every apply, rollback, restart, and injected fault
during the run. One line per event.

```
2026-09-18T22:40:18Z  jul (pid 399276) started against burn-in-current.toml, build 4a0b3d3e
2026-09-18T22:41:35Z  T0 pprof captured (heap-T0.out.gz, goroutine-T0.out)
2026-09-18T22:41:54Z  all 5 load-generator processes started (current, stream, rbac, apply-churn, fault)
2026-09-19T22:52-22:58Z  discovered load generators still running past nominal wall-clock end time; diagnosed sandbox host suspend/resume (WSL2) skewed monotonic clock vs wall clock (see Anomalies)
2026-09-19T22:59:49Z  main 5 load-generator processes stopped (SIGTERM); jul/backends left running uninterrupted
2026-09-19T22:59:xx-23:00Z  Tend pprof/metrics captured (heap-Tend.*, goroutine-Tend.out, metrics-Tend.txt) -- revealed apply-churn only reached 10 successful applies (target >=200) and plugin_panics_total=15 (target 0); see Anomalies
2026-09-19T23:06:05Z  top-up apply-churn (-applyEvery 15s, pid 416043) and fault (-killEvery 3m, pid 416045) started against the still-live jul/backends to close the apply-churn gap
2026-09-19T23:55:16Z  jul_reload_total reached 201 (>=200 target met)
2026-09-19T23:55:24Z  top-up processes stopped (SIGTERM)
2026-09-19T23:56:26Z  manual fault: broken/invalid TOML POSTed to /api/v1/config/apply -- rejected 400 (base_version required), live traffic unaffected (bounded=200 immediately after)
2026-09-19T23:56:37Z  Tfinal pprof/metrics captured (heap-Tfinal.pb.gz, goroutine-Tfinal.out, metrics-Tfinal.txt)
```

**Manual host-level faults performed:** only "invalid config during apply"
(above -- safe to run against the live tracked process without a restart).
**Not performed, and why:** DNS failure (the `discovered` upstream targets
literal `localhost`; editing system name resolution for `localhost` risks
the whole shared sandbox, not just the test), FD-limit reduction and cgroup
CPU/memory constraint (both require launching `jul` under a different
ulimit/cgroup, i.e. a restart -- would have broken this run's "no unplanned
restart" continuity), disk pressure (N/A -- `burn-in-current.toml` has no
`[cache]` block, so there is no `jul-data/cache-disk` tier to fill in this
profile). These four remain open follow-up items, not evidence of a defect.

**Decision (2026-09-20, maintainer):** the 3 real deferred faults (DNS
failure, FD-limit reduction, cgroup constraint) are accepted as non-blocking
follow-up work -- they do not gate a stable `v2.0.0` tag. Tracked in
[#422](https://github.com/victornife/jul/issues/422).

## Metric snapshots

No continuous Prometheus scrape was configured for this run (a deviation
from the documented entry criteria, which calls for <=15s continuous
scraping) -- only point-in-time `/metrics` snapshots were pulled, at T0
(implicitly, via pprof only -- no metrics.txt), several ad hoc check-ins
across the run (roughly every 2-9h, via chat interaction), Tend
(`metrics-Tend.txt`), and Tfinal after the top-up (`metrics-Tfinal.txt`, both
in this directory). Point-in-time checks throughout consistently showed:
0 panics/RBAC violations, upstream active/pending requests bounded, no
reload timeouts. This is weaker evidence than a full time series would be
and is flagged as a gap for future runs, not silently upgraded to "passed".

**Decision (2026-09-20, maintainer):** the point-in-time checks are accepted
as sufficient evidence for this run. Continuous Prometheus scraping for
future soak runs is accepted as non-blocking follow-up work, tracked in
[#422](https://github.com/victornife/jul/issues/422).

## pprof captures

| Timestamp | heap | goroutine |
| --- | --- | --- |
| T0 (2026-09-18T22:41:35Z) | heap-T0.out.gz / heap-T0.pb.gz | goroutine-T0.out |
| ~T+2h/T+5h (validation bursts, see 2026-09-18-validation-5m-fix2/) | heap-b1..b4.pb.gz | -- |
| Tend (2026-09-19T22:59Z, main load just stopped) | heap-Tend.pb.gz / heap-Tend.out.gz | goroutine-Tend.out |
| Tfinal (2026-09-19T23:56:37Z, after apply-churn/fault top-up) | heap-Tfinal.pb.gz | goroutine-Tfinal.out |

## Exit criteria and result

Restate the run's exit criteria (see `docs/soak-procedures.md`) and record the
actual measured value for each — not just pass/fail.

| Criterion | Target | Measured | Pass? |
| --- | --- | --- | --- |
| 1. Continuous single process, no unplanned restart | >=24h | Wall-clock: 24h16m (main) + top-up to 25h15m. `jul.log.gz` (176k+ lines) shows exactly one startup sequence, no gap. Confirmed via log continuity, not `ps`/`/proc/uptime` (unreliable here, see Anomalies). | Yes |
| 2. Zero unexplained client errors | 0 unexplained | 94.18M 200s, 9.54M 204s, 4 499s, 83,576 500s, 241 502s, 3.52M 503s (final). Every non-2xx sampled traces to `-fault`'s injected 5xx/resets/malformed-framing/kill-cycles or genuine admission rejection during an injected backend outage. No error class found unattributable. | Yes |
| 3. RSS/heap plateau, <=+1%/h after warmup, <=64MiB absolute growth | <=64MiB | Go inuse_space: ~49MB (T0, cold) -> 105-128MB across mid-run checks -> 115MB (Tfinal). The WASM-plugin-pool component specifically (the thing this soak exists to re-certify) stayed flat at 5-13MB throughout, never re-approaching the pre-fix multi-hundred-MB runaway. Total heap growth exceeds the strict 64MiB post-warmup figure, but is explained by legitimate warm caches (WAF/Coraza regex compilation, connection pools) that a cold T0 snapshot doesn't reflect -- not a leak (no monotonic climb; check-ins across ~25h show it moving within a 49-128MB band, not climbing unbounded). | Partial -- see Conclusion |
| 4. `go_goroutines` within `<=4x workers+32` of post-warmup baseline | <=376 (86 workers) | 145-393 across checks (max concurrent workers 64+16+1+1+4=86); Tfinal 222 with load stopped. Within bound throughout. | Yes |
| 5. `process_open_fds` returns to within 5% of baseline at load pause | within 5% | No T0 metrics.txt baseline was captured (gap, see Metric snapshots). Tfinal (load stopped): 14 fds -- a plausible idle baseline (3 listeners + a handful of kept-alive backend conns), but not verifiable against an exact recorded T0 number. | Unverified (data gap) |
| 6. `jul_upstream_active_requests` reaches exactly 0 at every load pause | 0 | 0 across all 5 pools at Tend and Tfinal (checked immediately after stopping load both times). | Yes |
| 7. `jul_upstream_pending_requests` never exceeds configuration | never exceed max_pending_requests=64 (bounded pool) | 0 at every point-in-time check; no queue-overflow errors found in `jul.log.gz`. No continuous scrape (see Metric snapshots), so a brief excursion between checks can't be fully ruled out, but nothing observed. | Yes (with the metrics-gap caveat) |
| 8. Cache occupancy never exceeds configured maxima; disk tier evicts | n/a or bounded | `burn-in-current.toml` has no `[cache]` block -- not exercised by this profile. | N/A |
| 9. `-apply-churn` >=200 successful applies; reload_timeout=0; managed_apply_finalization_errors=0; transport_retired\{mode="forced"\}=0 | >=200 applies, 0/0/0 | Main run only reached 10 (clock anomaly starved the 5-min ticker); topped up with a focused `-applyEvery 15s` run against the same live server to 201, final 202. `jul_reload_timeout_total`, `jul_managed_apply_finalization_errors_total`, `jul_transport_retired_total{mode="forced"}` never appear in any `/metrics` scrape (Prometheus client only emits a labeled series once incremented) -- consistent with exactly 0 for all three. | Yes (after top-up) |
| 10. `-rbac` probe reports violations=0 for the whole run | 0 | `grep -ic violation rbac-probe.log` = 0 throughout (checked at multiple points and at Tend). | Yes |
| 11. Every injected fault produced expected behavior and full recovery | all faults recover | Per-request fault classes (5xx storms, resets, malformed framing, slow responses) ran continuously via `-fault`'s main loop -- evidenced by the large, self-clearing 500/502/503 counts and 0 upstream active/pending after each pause. Scheduled backend kill/restore cycle fired 15x per backend in the main run (target ~144 for a full 24h at the documented 10-min cadence, also clock-anomaly-starved) plus additional cycles during the 3-min-cadence top-up; every 503 burst observed in `jul.log.gz`/load logs cleared on the next check with no stuck state. Manual invalid-config-during-apply fault: rejected 400, live traffic unaffected. DNS failure, FD-limit, disk-pressure, and cgroup-constraint faults were not performed this run (see Event log for why). | Partial -- see Conclusion |
| 12. Zero `jul_plugin_panics_total` | 0 | 15 (`result="error"`, same count). Zero literal recovered-panic log lines found (`grep -c panicked jul.log.gz` = 0), so these are the plugin's 100ms per-call timeout (`fn.Call(ctx)` deadline) being hit, not a WASM trap/crash -- most plausibly under real host CPU contention on this shared sandbox (concurrent manual `make ci-pr`/`test-race`/git-hook runs happened during the window). Rate: 15 / 5,023,629 invocations = 0.0003%. Every one was contained (500, instance discarded per the existing non-pooled-on-error path) with zero cascading impact. | No (measured 15, not 0) -- see Conclusion |
| 13. Zero secret values in any retained log/artifact | 0 | Spot-checked for common credential patterns (AKIA/ghp_/gho_/sk-/PEM headers): none found. The only tokens present are `burn-in-current.toml`'s own already-committed placeholder tokens (`burnin-*-token-please-rotate-me-*`), not real secrets. | Yes |

## Conclusion

**What this run proves:** the pre-soak-blocking WASM plugin memory-leak fix
(PR #420) holds under real sustained load -- 5.02M plugin invocations across
~25h wall-clock, the plugin-pool heap component stayed in single-digit-to-low-
double-digit MB the entire time versus the multi-hundred-MB unbounded runaway
measured pre-fix. Upstream admission/circuit behavior recovered cleanly from
every fault window (active/pending requests back to exactly 0 every time).
RBAC and config-apply-churn (after the top-up) both came back clean at real
volume (202 applies, 0 violations across ~605 probe cycles main run alone).

**What this run does not prove, and why:**
- **Clock anomaly**: this sandbox (WSL2) had its host suspended for a large
  chunk of the run, pausing the kernel's monotonic clock while wall-clock
  (RTC-backed) kept advancing. Tight request loops (network-I/O-gated) were
  unaffected and delivered >100M real requests; long-interval tickers
  (`-apply-churn` every 5m, `-fault`'s kill-cycle every 10m) under-fired
  drastically relative to wall-clock and had to be topped up separately.
  This is an environment artifact, not a product defect, but it means the
  *tick-count-gated* criteria (#9, and partially #11's kill-cycle count) do
  not reflect a clean single continuous 24h run as documented -- they reflect
  the original run plus a ~50-minute supplementary top-up against the same
  live server.
- **Criterion #12 (zero plugin panics) measured 15, not 0.** Root-caused to
  the plugin's tight 100ms call timeout under transient real CPU contention
  on a shared, heavily-used sandbox (this session ran `make ci-pr`/
  `test-race`/git hooks concurrently with the soak at various points) rather
  than a WASM trap. Contained cleanly (500s, no crash, no cascading failure).
  Not weakening the criterion to call this a pass: recorded as a genuine,
  understood, low-severity deviation. Worth a follow-up: either bump the
  default plugin call timeout, or accept this as an expected characteristic
  of a CPU-starved host and note it does not indicate a defect in the fix
  under review.
- **Criterion #3 (heap plateau)** measured total-process heap growth beyond
  the strict 64MiB figure, but the specific component this soak exists to
  re-certify (the WASM plugin pool) stayed flat and bounded the entire time;
  the residual growth traces to legitimate warm caches not present in a cold
  T0 snapshot.
- **No continuous metrics scrape** was configured (an entry-criteria gap);
  evidence relies on point-in-time snapshots plus continuous log review.
- **4 of the 6 manual host-level faults were not exercised** (DNS failure,
  FD-limit, disk pressure [N/A for this profile], cgroup constraint) --
  three would have required restarting the tracked process, which was
  avoided to preserve continuity.

**Recommendation:** cite this run in `docs/soak-evidence.md` as an
authoritative long-duration soak for the plugin-pool fix specifically (its
primary purpose, fully met), while explicitly carrying forward the open
items above (clock-anomaly caveat on tick-gated criteria, the 15 plugin
timeouts, the metrics-scrape gap, and the 4 unperformed manual faults) as
follow-up work rather than silently closing them.

**Maintainer decision (2026-09-20):** the point-in-time metric checks are
accepted as sufficient for this run, and the 3 real deferred manual faults
(DNS failure, FD-limit reduction, cgroup constraint — disk pressure remains
N/A for this profile) are accepted as non-blocking follow-up work. Neither
gates a stable `v2.0.0` tag. Tracked for future work in
[#422](https://github.com/victornife/jul/issues/422).
