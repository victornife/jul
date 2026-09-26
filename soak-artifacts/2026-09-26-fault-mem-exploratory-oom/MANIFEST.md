# Fault evidence: mem (#422)

| Field | Value |
| --- | --- |
| Profile | `mem` |
| Date (UTC) | 2026-09-26T17:44:01Z |
| Jul SHA | `99934938a04666345aac3a807d284ff67c109411` |
| Harness SHA | `99934938a04666345aac3a807d284ff67c109411` |
| Build | `go build -tags "brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf"`; go version go1.26.6 linux/arm64 |
| Host | Linux 6.18.33.2-microsoft-standard-WSL2 aarch64; 12 CPUs; 15737 MiB |
| Isolation | transient systemd --user scope (cgroup v2) holding only the Jul process; limits changed live with systemctl --user set-property |
| Limits | cgroup v2 memory.max=192M, swap.max=0; memory.high 176M, lowered live to 96M for the throttled phase |
| Workload | memory cache 64 MB cap; 48 workers filling unique 256 KiB cacheable objects under MemoryHigh=176M, then cache-hit re-reads under MemoryHigh lowered live to 96M (below the working set), then MemoryHigh restored; MemoryMax=192M throughout, swap 0 |
| Config | [jul.toml](jul.toml) |
| Metrics | [metrics/samples.jsonl.gz](metrics/samples.jsonl.gz) (5s interval), [metrics/metrics-manifest.json](metrics/metrics-manifest.json), [summary](metrics-summary.md) |
| Client results | `load-*.jsonl` (one line per second) |
| Events | [events.log](events.log) |
| Snapshots | [snapshots/](snapshots/) |
| Reproduce | `scripts/fault-evidence.sh mem` |

## Result

See events.log, snapshots (memory.events), load-*.jsonl and metrics-summary.md; analysis in docs/soak-evidence.md.

## Exploratory run — kernel OOM kill (recorded, not graceful degradation)

Jul was OOM-killed by the kernel ~3 s into the fill phase (`journalctl --user`:
"jul-fault-mem-221054.scope: The kernel OOM killer killed some processes in this
unit ... 193.5M memory peak"). No `GOMEMLIMIT` was set: the Go runtime does not
derive a heap limit from the cgroup, so with a 64 MB cache cap plus 48
concurrent 256 KiB responses the GC target overshot `memory.max=192M` before a
collection bounded it. Every later phase measured a dead process (all client
errors are `refused`), which is why the bounded profile sets `GOMEMLIMIT` below
`memory.max`. Reproduce this run with `FAULT_GOMEMLIMIT= scripts/fault-evidence.sh mem`
on harness SHA recorded above. Analysis: docs/soak-evidence.md.
