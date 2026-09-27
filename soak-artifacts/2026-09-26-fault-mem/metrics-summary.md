# Metrics summary: /home/victornf/http_server-422/soak-artifacts/2026-09-26-fault-mem/metrics/samples.jsonl.gz

- url: `http://127.0.0.1:19901/metrics`, interval: 5s, labels: `{"config":"jul.toml","jul_sha":"a2e07d76c06b7c0389709af3d3fb164b8ced2610","profile":"mem","workload":"memory cache 64 MB cap; 48 workers filling unique 256 KiB cacheable objects under MemoryHigh=176M, then cache-hit re-reads under MemoryHigh lowered live to 96M (below the working set), then MemoryHigh restored; MemoryMax=192M throughout, swap 0"}`
- window: 2026-09-26T18:18:19.136877273Z → 2026-09-26T18:22:29.142106829Z, samples: 50, scrape errors: 1, gaps (>2× interval): 1 (10s), end: signal

- scrape error ×1: `Get "http://127.0.0.1:19901/metrics": context deadline exceeded (Client.Timeout `

## Quiescence (first vs last sample)

| Series | first | last | max | max at |
| --- | --- | --- | --- | --- |
| `go_goroutines` | 20 | 21 | 230 | 2026-09-26T18:18:44.139381691Z |
| `process_open_fds` | 11 | 11 | 116 | 2026-09-26T18:18:34.139380264Z |
| `process_resident_memory_bytes` | 3.21536e+07 | 1.63909632e+08 | 1.7057792e+08 | 2026-09-26T18:18:49.140874087Z |
| `go_memstats_heap_inuse_bytes` | 7.028736e+06 | 8.7924736e+07 | 1.3275136e+08 | 2026-09-26T18:19:04.13941743Z |
| `jul_listener_conns` | 0 | 0 | 53 | 2026-09-26T18:20:24.14128895Z |
| `jul_http_requests_in_flight` | 0 | 0 | 48 | 2026-09-26T18:19:04.13941743Z |

## All series (changed during the window)

| Series | first | last | min | max | max at |
| --- | --- | --- | --- | --- | --- |
| `go_goroutines` | 20 | 21 | 20 | 230 | 2026-09-26T18:18:44.139381691Z |
| `go_memstats_heap_alloc_bytes` | 4.771768e+06 | 8.1028144e+07 | 4.771768e+06 | 1.29543048e+08 | 2026-09-26T18:19:04.13941743Z |
| `go_memstats_heap_inuse_bytes` | 7.028736e+06 | 8.7924736e+07 | 7.028736e+06 | 1.3275136e+08 | 2026-09-26T18:19:04.13941743Z |
| `go_memstats_next_gc_bytes` | 9.874978e+06 | 1.24993108e+08 | 9.874978e+06 | 1.31495552e+08 | 2026-09-26T18:18:34.139380264Z |
| `go_memstats_sys_bytes` | 1.8995464e+07 | 1.72886328e+08 | 1.8995464e+07 | 1.72886328e+08 | 2026-09-26T18:19:24.142654677Z |
| `go_threads` | 13 | 65 | 13 | 65 | 2026-09-26T18:20:24.14128895Z |
| `jul_cache_bytes{tier="memory"}` | 0 | 6.6948975e+07 | 0 | 6.6948975e+07 | 2026-09-26T18:18:24.139384536Z |
| `jul_cache_entries{tier="memory"}` | 0 | 255 | 0 | 255 | 2026-09-26T18:18:24.139384536Z |
| `jul_cache_events_total{state="HIT"}` | 172245 | 1.33179e+06 | 172245 | 1.33179e+06 | 2026-09-26T18:20:44.141947666Z |
| `jul_cache_events_total{state="MISS"}` | 28166 | 336226 | 28166 | 336226 | 2026-09-26T18:19:24.142654677Z |
| `jul_cache_evictions_total{tier="memory"}` | 0 | 335898 | 0 | 335898 | 2026-09-26T18:19:24.142654677Z |
| `jul_client_addr_derivations_total{result="accepted",source="peer"}` | 28207 | 1.668026e+06 | 28207 | 1.668026e+06 | 2026-09-26T18:20:44.141947666Z |
| `jul_http_request_duration_seconds_count{host="",method="GET"}` | 28166 | 1.668016e+06 | 28166 | 1.668016e+06 | 2026-09-26T18:20:44.141947666Z |
| `jul_http_request_duration_seconds_sum{host="",method="GET"}` | 185.08722295799828 | 4157.768466189843 | 185.08722295799828 | 4157.768466189843 | 2026-09-26T18:20:44.141947666Z |
| `jul_http_requests_in_flight` | 0 | 0 | 0 | 48 | 2026-09-26T18:19:04.13941743Z |
| `jul_http_requests_total{code="200",host="",method="GET"}` | 28166 | 1.66799e+06 | 28166 | 1.66799e+06 | 2026-09-26T18:20:44.141947666Z |
| `jul_http_response_bytes_total` | 0 | 4.37228000277e+11 | 0 | 4.37228000277e+11 | 2026-09-26T18:20:44.141947666Z |
| `jul_listener_conns` | 0 | 0 | 0 | 53 | 2026-09-26T18:20:24.14128895Z |
| `jul_upstream_active_requests{pool="app"}` | 0 | 0 | 0 | 47 | 2026-09-26T18:18:44.139381691Z |
| `jul_upstream_connections{pool="app"}` | 0 | 0 | 0 | 57 | 2026-09-26T18:18:34.139380264Z |
| `process_cpu_seconds_total` | 0.03 | 417.84 | 0.03 | 417.84 | 2026-09-26T18:22:29.142106829Z |
| `process_network_receive_bytes_total` | 2.89815444869e+11 | 8.17901552583e+11 | 2.89815444869e+11 | 8.17901552583e+11 | 2026-09-26T18:22:29.142106829Z |
| `process_network_transmit_bytes_total` | 2.9131194748e+11 | 8.19398001163e+11 | 2.9131194748e+11 | 8.19398001163e+11 | 2026-09-26T18:22:29.142106829Z |
| `process_open_fds` | 11 | 11 | 11 | 116 | 2026-09-26T18:18:34.139380264Z |
| `process_resident_memory_bytes` | 3.21536e+07 | 1.63909632e+08 | 3.21536e+07 | 1.7057792e+08 | 2026-09-26T18:18:49.140874087Z |
| `process_virtual_memory_bytes` | 1.332621312e+09 | 1.536868352e+09 | 1.332621312e+09 | 1.536868352e+09 | 2026-09-26T18:19:54.139751824Z |
