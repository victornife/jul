# Fault evidence: storage (#437)

| Field | Value |
| --- | --- |
| Profile | `storage` |
| Date (UTC) | 2026-09-27T20:43:32Z |
| Jul SHA | `015e5f4036a571e4cca3c95248748fa3cb90713b` (tree `dc8d42c4d201a5633124577e8fe6c4df366d0199`) |
| Harness SHA | `64a30482cb6c1c5b792f0677da3835e596a009b5` |
| Build | `go build -tags "brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf"`; go version go1.26.6 linux/arm64 |
| Host | Linux 6.18.33.2-microsoft-standard-WSL2 aarch64; 12 CPUs; 15739 MiB |
| Isolation | unshare --user --map-root-user --mount: a private 48 MiB tmpfs holds the disk cache tier, the access and audit logs, the managed config file and its history; the host filesystem is never filled |
| Limits | private tmpfs size=48m for cache/log/audit/config/history; cache disk_max_size 16 MiB, memory 2 MiB; access log default 100 MB rotation |
| Workload | access log on the default 100 MB rotation (larger than the filesystem); 8 workers filling unique 128 KiB cacheable objects into a 16 MiB disk tier plus 48 keep-alive workers on small responses; /api/stats sampled every 0.5s; pressure cleared by truncating the access log, then a restart |
| Config | [jul.toml](jul.toml) |
| Metrics | [metrics/samples.jsonl.gz](metrics/samples.jsonl.gz) (5s interval), [metrics/metrics-manifest.json](metrics/metrics-manifest.json), [summary](metrics-summary.md) |
| Client results | `load-*.jsonl` (one line per second) |
| Events | [events.log.gz](events.log.gz) |
| Snapshots | [snapshots/](snapshots/) |
| Reproduce | `scripts/fault-evidence.sh storage` |

## Result

See storage-transitions.txt, storage-headroom.jsonl, events.log.gz, snapshots and metrics-summary.md; analysis in docs/soak-evidence.md.
