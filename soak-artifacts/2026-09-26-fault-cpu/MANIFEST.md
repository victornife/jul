# Fault evidence: cpu (#422)

| Field | Value |
| --- | --- |
| Profile | `cpu` |
| Date (UTC) | 2026-09-26T18:18:18Z |
| Jul SHA | `a2e07d76c06b7c0389709af3d3fb164b8ced2610` (tree `894f99d951c642a2f78e4c3e9d71f07d57855c54`) |
| Harness SHA | `5dc63397f24304e988c6819c0e7da782826224fd` |
| Build | `go build -tags "brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf"`; go version go1.26.6 linux/arm64 |
| Host | Linux 6.18.33.2-microsoft-standard-WSL2 aarch64; 12 CPUs; 15737 MiB |
| Isolation | transient systemd --user scope (cgroup v2) holding only the Jul process |
| Limits | cgroup v2 cpu.max 20000/100000 (CPUQuota=20%) during the pressure phase and at shutdown |
| Workload | 64 workers keep-alive GET /slow?ms=5 (unconstrained baseline, then CPUQuota=20%, then quota lifted live); SIGHUP reload under quota; SIGTERM shutdown timed |
| Config | [jul.toml](jul.toml) |
| Metrics | [metrics/samples.jsonl.gz](metrics/samples.jsonl.gz) (5s interval), [metrics/metrics-manifest.json](metrics/metrics-manifest.json), [summary](metrics-summary.md) |
| Client results | `load-*.jsonl` (one line per second) |
| Events | [events.log.gz](events.log.gz) |
| Snapshots | [snapshots/](snapshots/) |
| Reproduce | `scripts/fault-evidence.sh cpu` |

## Result

See events.log.gz, load-*.jsonl and metrics-summary.md; analysis in docs/soak-evidence.md.
