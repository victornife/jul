# Soak run manifest

> Copy this file to `soak-artifacts/<YYYY-MM-DD>-<scope>/MANIFEST.md` and fill
> in every section. `scripts/soak-manifest-init.sh <scope>` does the copy and
> pre-fills the fields it can determine automatically.

## Identity

| Field | Value |
| --- | --- |
| Scope | validation-5m-confirmed |
| Date started (UTC) | 2026-09-18T21:24:15Z |
| Date ended (UTC) | |
| Operator | |
| Build SHA | a2c9705adbba7f25cc5557dc787290aa3e9200a7 (working tree NOT clean) |
| Working tree clean at build time? | |
| `go version` | go version go1.26.6 linux/arm64 |
| OS / kernel | Linux AXLZ00399204189 6.18.33.2-microsoft-standard-WSL2 #1 SMP PREEMPT_DYNAMIC Thu Jun 18 21:38:49 UTC 2026 aarch64 GNU/Linux |
| Host spec (CPU, RAM, disk) | |
| `jul capabilities -json` | ```<paste here>``` |

## Configuration

List every config file used, and paste its exact content (or attach a copy in
this directory alongside this manifest).

| File | Copied to |
| --- | --- |
| | |

## Commands executed

Paste every command line, in order, exactly as run (build, backends, server,
load generator(s), any fault-injection commands).

```sh

```

## Event log

A timestamped log of every apply, rollback, restart, and injected fault
during the run. One line per event.

```
<UTC timestamp>  <event>
```

## Metric snapshots

List where scrape snapshots / exported CSVs / dashboards for this run are
stored (attach in this directory, or link to a retained dashboard/TSDB
snapshot). Note the scrape interval used.

## pprof captures

| Timestamp | heap | goroutine |
| --- | --- | --- |
| T0 | | |
| ... | | |
| Tend | | |

## Exit criteria and result

Restate the run's exit criteria (see `docs/soak-procedures.md`) and record the
actual measured value for each — not just pass/fail.

| Criterion | Target | Measured | Pass? |
| --- | --- | --- | --- |
| | | | |

## Conclusion

Free-form: what this run proves, what it does not, any anomaly observed and
how it was investigated, and whether this run's evidence should be cited in
`docs/soak-evidence.md` (if so, add a dated entry there linking back to this
directory).

## Procedure A result (round 2, post-fix): PASS

Fix applied: `internal/plugins/runtime.go` — replaced the implicit `sync.Pool`
of WASM module instances (which let wazero-Runtime-tracked instances leak
uncollected when Go's GC victim-cache silently evicted them, since nothing
called `Close()` on eviction) with a fixed-capacity channel pool
(`poolCapacity = 64`) that always either reuses or explicitly closes an
instance, plus a per-instance invocation cap (`maxInstanceInvocations`,
default 1000, configurable via new `plugins.<name>.max_invocations`).

- Isolated decisive test: 4 consecutive bursts of 24,000 requests each
  (96,000 total) at 8-way concurrency against `/plugin/` alone.
  `MemoryInstance.Grow` retained: ~12.6MB -> ~12.6MB -> ~10.1MB -> ~7.6MB
  (flat/plateaued, not climbing). Before the fix, the same pattern grew
  213MB -> 300MB -> 381MB and counting (unbounded).
- Full Procedure A (5-min `burn-in-current.toml`, 32+16 workers): HTTP
  1,262,437 req / 0 errors; L4 stream 6,155,283 rounds / 0 errors. Heap
  49.33MB (T0) -> 55.30MB (T+5m), `MemoryInstance.Grow` only ~5MB retained.
  Goroutines 19 -> 145 (proportional to 48 concurrent workers, not a leak).
  No unexpected errors in jul.log (only benign discovery-refresh WARN lines).
- Race-clean: `CGO_ENABLED=1 go test -race -tags wasmplugins ./internal/plugins/...`
  passes, including new `TestPooledInstanceRetiresAfterMaxInvocations`.
- `make ci-pr` and `CGO_ENABLED=1 make test-race` both green at the fixed SHA.

**Cleared to proceed to Procedure C (24h final soak).**
