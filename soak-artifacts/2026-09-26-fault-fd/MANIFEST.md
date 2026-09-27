# Fault evidence: fd (#422)

| Field | Value |
| --- | --- |
| Profile | `fd` |
| Date (UTC) | 2026-09-26T18:14:26Z |
| Jul SHA | `a2e07d76c06b7c0389709af3d3fb164b8ced2610` (tree `894f99d951c642a2f78e4c3e9d71f07d57855c54`) |
| Harness SHA | `5dc63397f24304e988c6819c0e7da782826224fd` |
| Build | `go build -tags "brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf"`; go version go1.26.6 linux/arm64 |
| Host | Linux 6.18.33.2-microsoft-standard-WSL2 aarch64; 12 CPUs; 15737 MiB |
| Isolation | fresh Jul process under prlimit --nofile=256:256; backends and load generator unconstrained |
| Limits | RLIMIT_NOFILE 256 (soft and hard) on the Jul process only |
| Workload | baseline 16 workers; pressure: 400 held idle TCP connections + 64 workers with fresh connections per request; recovery 16 workers |
| Config | [jul.toml](jul.toml) |
| Metrics | [metrics/samples.jsonl.gz](metrics/samples.jsonl.gz) (5s interval), [metrics/metrics-manifest.json](metrics/metrics-manifest.json), [summary](metrics-summary.md) |
| Client results | `load-*.jsonl` (one line per second) |
| Events | [events.log.gz](events.log.gz) |
| Snapshots | [snapshots/](snapshots/) |
| Reproduce | `scripts/fault-evidence.sh fd` |

## Result

See events.log.gz, emfile-log-kinds.txt, load-*.jsonl and metrics-summary.md; analysis in docs/soak-evidence.md.
