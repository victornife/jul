# Fault evidence: mem (#422)

| Field | Value |
| --- | --- |
| Profile | `mem` |
| Date (UTC) | 2026-09-26T18:22:29Z |
| Jul SHA | `a2e07d76c06b7c0389709af3d3fb164b8ced2610` (tree `894f99d951c642a2f78e4c3e9d71f07d57855c54`) |
| Harness SHA | `5dc63397f24304e988c6819c0e7da782826224fd` |
| Build | `go build -tags "brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf"`; go version go1.26.6 linux/arm64 |
| Host | Linux 6.18.33.2-microsoft-standard-WSL2 aarch64; 12 CPUs; 15737 MiB |
| Isolation | transient systemd --user scope (cgroup v2) holding only the Jul process; limits changed live with systemctl --user set-property |
| Limits | cgroup v2 memory.max=192M, swap.max=0; memory.high 176M, lowered live to 96M for the throttled phase; GOMEMLIMIT=144MiB |
| Workload | memory cache 64 MB cap; 48 workers filling unique 256 KiB cacheable objects under MemoryHigh=176M, then cache-hit re-reads under MemoryHigh lowered live to 96M (below the working set), then MemoryHigh restored; MemoryMax=192M throughout, swap 0 |
| Config | [jul.toml](jul.toml) |
| Metrics | [metrics/samples.jsonl.gz](metrics/samples.jsonl.gz) (5s interval), [metrics/metrics-manifest.json](metrics/metrics-manifest.json), [summary](metrics-summary.md) |
| Client results | `load-*.jsonl` (one line per second) |
| Events | [events.log.gz](events.log.gz) |
| Snapshots | [snapshots/](snapshots/) |
| Reproduce | `scripts/fault-evidence.sh mem` |

## Result

See events.log.gz, snapshots (memory.events), load-*.jsonl and metrics-summary.md; analysis in docs/soak-evidence.md.
