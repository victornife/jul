# Fault evidence: disk (#422)

| Field | Value |
| --- | --- |
| Profile | `disk` |
| Date (UTC) | 2026-09-26T17:46:08Z |
| Jul SHA | `99934938a04666345aac3a807d284ff67c109411` |
| Harness SHA | `99934938a04666345aac3a807d284ff67c109411` |
| Build | `go build -tags "brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf"`; go version go1.26.6 linux/arm64 |
| Host | Linux 6.18.33.2-microsoft-standard-WSL2 aarch64; 12 CPUs; 15737 MiB |
| Isolation | unshare --user --map-root-user --mount: a private 48 MiB tmpfs holds the disk cache tier, the access and audit logs, the managed config file and its history; the host filesystem is never filled |
| Limits | private tmpfs size=48m for cache/log/config/history; cache disk_max_size 24 MiB, memory 2 MiB |
| Workload | disk cache tier 24 MiB cap behind a 2 MiB memory tier; 32 workers filling unique 128 KiB cacheable objects; access+audit log on the same tmpfs; filler file to ~1 MiB free; managed config apply under pressure and after; filler removed |
| Config | [jul.toml](jul.toml) |
| Metrics | [metrics/samples.jsonl.gz](metrics/samples.jsonl.gz) (5s interval), [metrics/metrics-manifest.json](metrics/metrics-manifest.json), [summary](metrics-summary.md) |
| Client results | `load-*.jsonl` (one line per second) |
| Events | [events.log.gz](events.log.gz) |
| Snapshots | [snapshots/](snapshots/) |
| Reproduce | `scripts/fault-evidence.sh disk` |

## Result

See events.log.gz, disk-error-kinds.txt, apply-*.json, snapshots, load-*.jsonl and metrics-summary.md; analysis in docs/soak-evidence.md.

## Exploratory run — access log exhausted the filesystem first

With the default access-log rotation (100 MB, larger than the filesystem), the access log alone (~24 MB in ~20 s at
~5 000 req/s) filled the 48 MiB tmpfs during the fill phase, before the harness
filler ran, so "filler removed" freed nothing and the recovery apply still
returned `503 storage_unavailable`. The harness had also applied
`log_level = "error"`, which hid the disk tier's Warn-level write failures. Both
are corrected in the bounded profile (`rotate_max_mb = 4`, `rotate_keep = 1`;
applies cycle `info`/`warn`/`info`). The observation itself — nothing warned
before storage ran out — is the #437 activation evidence in
docs/soak-evidence.md.
