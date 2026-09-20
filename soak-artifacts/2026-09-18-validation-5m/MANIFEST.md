# Soak run manifest

> Copy this file to `soak-artifacts/<YYYY-MM-DD>-<scope>/MANIFEST.md` and fill
> in every section. `scripts/soak-manifest-init.sh <scope>` does the copy and
> pre-fills the fields it can determine automatically.

## Identity

| Field | Value |
| --- | --- |
| Scope | validation-5m |
| Date started (UTC) | 2026-09-18T16:34:12Z |
| Date ended (UTC) | |
| Operator | |
| Build SHA | a2c9705adbba7f25cc5557dc787290aa3e9200a7 |
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

## Procedure A result: FAILED — do not proceed to Procedure C yet

- Entry gates before this run: `make ci-pr` PASS, `CGO_ENABLED=1 make test-race` PASS (all packages),
  `make soak` PASS, `make soak-repro-smoke` PASS, `make config-check` PASS (all part of `make ci-pr`).
- 5-minute `burn-in-current.toml` run: HTTP load 1,243,852 req / 0 errors; L4 stream load
  5,736,941 rounds / 0 errors. Clean on the surface.
- **But**: `/debug/pprof/heap` (inuse_space) grew from ~9.7MB (T0) to ~685MB (T+5m) to ~638MB
  (T+5m, post-2x-forced-GC, unchanged across both GCs) to ~669MB after an additional ~24,000
  requests concentrated on `/plugin/` at only 8-way concurrency.
- Top allocator: `github.com/tetratelabs/wazero/internal/wasm.(*MemoryInstance).Grow` (~613-669MB
  flat). Not reclaimed by two consecutive explicit `runtime.GC()` calls (ruled out sync.Pool
  victim-cache timing artifact). Not concurrency-bound (8 concurrent callers should need ~8 pooled
  instances at most; observed retained memory implies far more, or unbounded per-instance growth).
- No plugin panics/errors logged during the burst (`grep -i panic/plugin jul.log` empty) — this is
  clean, successful, pooled reuse, not a panic/reinstantiate churn.
- Root cause hypothesis: `internal/plugins/runtime.go`'s `plugin.pool` (`sync.Pool` of `api.Module`)
  reuses the same long-lived WASM module instance across many invocations for performance
  (documented rationale: "instantiating a Go/wasip1 module... is expensive"). WebAssembly linear
  memory can only grow, never shrink (`memory.grow` is monotonic) — if the guest module's internal
  allocator/GC doesn't reset itself between invocations, each reused instance's memory ratchets
  upward, and nothing in this codebase currently recycles/retires a pooled instance after N
  invocations or a memory high-watermark. Bounded only by `WithMemoryLimitPages` (default 256
  pages = 16MiB per instance) — but a pool holding many such instances has no overall cap.
- **Blocks Procedure C**: this profile (`burn-in-current.toml`) is exactly what Procedure C's
  24h workload uses, driving continuous `/plugin/` traffic for 24h. At this growth rate, RSS
  would almost certainly blow past the ≤64MiB post-warmup / ≤1%/h exit criterion (#3), and
  plausibly threaten host memory over 24h at full `-workers 64` volume.
- **Not launching the 24h final soak until this is triaged.** See chat response for recommended
  next steps (open a tracking issue, consider periodic instance recycling in `internal/plugins`,
  or confirm/measure the actual plateau point before re-attempting Procedure A).
