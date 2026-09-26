# Fault evidence: disk (#422)

| Field | Value |
| --- | --- |
| Profile | `disk` |
| Date (UTC) | 2026-09-26T18:26:11Z |
| Jul SHA | `a2e07d76c06b7c0389709af3d3fb164b8ced2610` (tree `894f99d951c642a2f78e4c3e9d71f07d57855c54`) |
| Harness SHA | `5dc63397f24304e988c6819c0e7da782826224fd` |
| Build | `go build -tags "brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf"`; go version go1.26.6 linux/arm64 |
| Host | Linux 6.18.33.2-microsoft-standard-WSL2 aarch64; 12 CPUs; 15737 MiB |
| Isolation | unshare --user --map-root-user --mount: a private 48 MiB tmpfs holds the disk cache tier, the access and audit logs, the managed config file and its history; the host filesystem is never filled |
| Limits | private tmpfs size=48m for cache/log/config/history; cache disk_max_size 24 MiB, memory 2 MiB |
| Workload | disk cache tier 24 MiB cap behind a 2 MiB memory tier; 32 workers filling unique 128 KiB cacheable objects; access+audit log on the same tmpfs; filler file to ~1 MiB free; managed config apply under pressure and after; filler removed |
| Config | [jul.toml](jul.toml) |
| Metrics | [metrics/samples.jsonl.gz](metrics/samples.jsonl.gz) (5s interval), [metrics/metrics-manifest.json](metrics/metrics-manifest.json), [summary](metrics-summary.md) |
| Client results | `load-*.jsonl` (one line per second) |
| Events | [events.log](events.log) |
| Snapshots | [snapshots/](snapshots/) |
| Reproduce | `scripts/fault-evidence.sh disk` |

## Result

See events.log, disk-error-kinds.txt, apply-*.json, snapshots, load-*.jsonl and metrics-summary.md; analysis in docs/soak-evidence.md.
