# Metrics summary: /home/victornf/http_server-422/soak-artifacts/2026-09-26-fault-mem/metrics/samples.jsonl.gz

- url: `http://127.0.0.1:19901/metrics`, interval: 5s, labels: `{"config":"jul.toml","jul_sha":"99934938a04666345aac3a807d284ff67c109411","profile":"mem","workload":"memory cache 64 MB cap; 48 workers filling unique 256 KiB cacheable objects under MemoryHigh=176M, then cache-hit re-reads under MemoryHigh lowered live to 96M (below the working set), then MemoryHigh restored; MemoryMax=192M throughout, swap 0"}`
- window: 2026-09-26T17:41:25.928110453Z → 2026-09-26T17:41:25.928110453Z, samples: 1, scrape errors: 31, gaps (>2× interval): 0 (0s), end: signal

- scrape error ×31: `Get "http://127.0.0.1:19901/metrics": dial tcp 127.0.0.1:19901: connect: connect`

## Quiescence (first vs last sample)

| Series | first | last | max | max at |
| --- | --- | --- | --- | --- |
| `go_goroutines` | 20 | 20 | 20 | 2026-09-26T17:41:25.928110453Z |
| `process_open_fds` | 11 | 11 | 11 | 2026-09-26T17:41:25.928110453Z |
| `process_resident_memory_bytes` | 3.1690752e+07 | 3.1690752e+07 | 3.1690752e+07 | 2026-09-26T17:41:25.928110453Z |
| `go_memstats_heap_inuse_bytes` | 6.742016e+06 | 6.742016e+06 | 6.742016e+06 | 2026-09-26T17:41:25.928110453Z |
| `jul_listener_conns` | 0 | 0 | 0 | 2026-09-26T17:41:25.928110453Z |
| `jul_http_requests_in_flight` | 0 | 0 | 0 | 2026-09-26T17:41:25.928110453Z |

## All series (changed during the window)

| Series | first | last | min | max | max at |
| --- | --- | --- | --- | --- | --- |
