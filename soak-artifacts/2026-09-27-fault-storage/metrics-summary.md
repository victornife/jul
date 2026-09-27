# Metrics summary: /home/christinjk/jul/soak-artifacts/2026-09-27-fault-storage/metrics/samples.jsonl.gz

- url: `http://127.0.0.1:19901/metrics`, interval: 5s, labels: `{"config":"jul.toml","jul_sha":"d493da1b4b31c8b159d4069a63afbc4acb2f3a20","profile":"storage","workload":"access log on the default 100 MB rotation (larger than the filesystem); 8 workers filling unique 128 KiB cacheable objects into a 16 MiB disk tier plus 48 keep-alive workers on small responses; /api/stats sampled every 0.5s; pressure cleared by truncating the access log, then a restart"}`
- window: 2026-09-27T10:09:29.199021247Z → 2026-09-27T10:09:56.073995634Z, samples: 6, scrape errors: 0, gaps (>2× interval): 0 (0s), end: signal


## Quiescence (first vs last sample)

| Series | first | last | max | max at |
| --- | --- | --- | --- | --- |
| `go_goroutines` | 20 | 53 | 164 | 2026-09-27T10:09:39.202519542Z |
| `process_open_fds` | 13 | 30 | 94 | 2026-09-27T10:09:39.202519542Z |
| `process_resident_memory_bytes` | 3.3501184e+07 | 5.1781632e+07 | 6.4057344e+07 | 2026-09-27T10:09:46.074937361Z |
| `go_memstats_heap_inuse_bytes` | 6.832128e+06 | 1.7670144e+07 | 2.4805376e+07 | 2026-09-27T10:09:51.074032655Z |
| `jul_listener_conns` | 0 | 2 | 56 | 2026-09-27T10:09:34.201991378Z |
| `jul_http_requests_in_flight` | 0 | 2 | 30 | 2026-09-27T10:09:46.074937361Z |

## All series (changed during the window)

| Series | first | last | min | max | max at |
| --- | --- | --- | --- | --- | --- |
| `go_goroutines` | 20 | 53 | 20 | 164 | 2026-09-27T10:09:39.202519542Z |
| `go_memstats_heap_alloc_bytes` | 4.751448e+06 | 1.4369976e+07 | 4.751448e+06 | 2.0955768e+07 | 2026-09-27T10:09:51.074032655Z |
| `go_memstats_heap_inuse_bytes` | 6.832128e+06 | 1.7670144e+07 | 6.832128e+06 | 2.4805376e+07 | 2026-09-27T10:09:51.074032655Z |
| `go_memstats_next_gc_bytes` | 9.71517e+06 | 1.7340338e+07 | 9.71517e+06 | 2.7170818e+07 | 2026-09-27T10:09:34.201991378Z |
| `go_memstats_sys_bytes` | 2.344372e+07 | 5.8452248e+07 | 2.344372e+07 | 5.8452248e+07 | 2026-09-27T10:09:34.201991378Z |
| `go_threads` | 14 | 26 | 14 | 26 | 2026-09-27T10:09:34.201991378Z |
| `jul_cache_bytes{tier="disk"}` | 0 | 1.672209e+07 | 0 | 1.672209e+07 | 2026-09-27T10:09:34.201991378Z |
| `jul_cache_bytes{tier="memory"}` | 0 | 1.972095e+06 | 0 | 1.972493e+06 | 2026-09-27T10:09:34.201991378Z |
| `jul_cache_entries{tier="disk"}` | 0 | 127 | 0 | 127 | 2026-09-27T10:09:34.201991378Z |
| `jul_cache_entries{tier="memory"}` | 0 | 15 | 0 | 16 | 2026-09-27T10:09:34.201991378Z |
| `jul_cache_events_total{state="HIT"}` | 50534 | 197378 | 50534 | 197378 | 2026-09-27T10:09:56.073995634Z |
| `jul_cache_events_total{state="MISS"}` | 3151 | 13790 | 3151 | 13790 | 2026-09-27T10:09:56.073995634Z |
| `jul_cache_evictions_total{tier="disk"}` | 0 | 8040 | 0 | 8040 | 2026-09-27T10:09:56.073995634Z |
| `jul_cache_evictions_total{tier="memory"}` | 0 | 13758 | 0 | 13758 | 2026-09-27T10:09:56.073995634Z |
| `jul_client_addr_derivations_total{result="accepted",source="peer"}` | 53693 | 211172 | 53693 | 211172 | 2026-09-27T10:09:56.073995634Z |
| `jul_http_request_duration_seconds_count{host="",method="GET"}` | 53685 | 211168 | 53685 | 211168 | 2026-09-27T10:09:56.073995634Z |
| `jul_http_request_duration_seconds_sum{host="",method="GET"}` | 43.53678182399974 | 225.95854282300422 | 43.53678182399974 | 225.95854282300422 | 2026-09-27T10:09:56.073995634Z |
| `jul_http_requests_in_flight` | 0 | 2 | 0 | 30 | 2026-09-27T10:09:46.074937361Z |
| `jul_http_requests_total{code="200",host="",method="GET"}` | 53664 | 211164 | 53664 | 211164 | 2026-09-27T10:09:56.073995634Z |
| `jul_http_response_bytes_total` | 0 | 1.813216676e+09 | 0 | 1.813216676e+09 | 2026-09-27T10:09:56.073995634Z |
| `jul_listener_conns` | 0 | 2 | 0 | 56 | 2026-09-27T10:09:34.201991378Z |
| `jul_storage_bytes{category="access_log",kind="available"}` | 5.0327552e+07 | 3.280896e+07 | 0 | 5.0327552e+07 | 2026-09-27T10:09:29.199021247Z |
| `jul_storage_bytes{category="audit_log",kind="available"}` | 5.0327552e+07 | 3.280896e+07 | 0 | 5.0327552e+07 | 2026-09-27T10:09:29.199021247Z |
| `jul_storage_bytes{category="cache",kind="available"}` | 5.0327552e+07 | 3.280896e+07 | 0 | 5.0327552e+07 | 2026-09-27T10:09:29.199021247Z |
| `jul_storage_bytes{category="config",kind="available"}` | 5.0327552e+07 | 3.280896e+07 | 0 | 5.0327552e+07 | 2026-09-27T10:09:29.199021247Z |
| `jul_storage_bytes{category="config_history",kind="available"}` | 5.0327552e+07 | 3.280896e+07 | 0 | 5.0327552e+07 | 2026-09-27T10:09:29.199021247Z |
| `jul_upstream_active_requests{pool="app"}` | 0 | 2 | 0 | 7 | 2026-09-27T10:09:34.201991378Z |
| `jul_upstream_connections{pool="app"}` | 0 | 14 | 0 | 22 | 2026-09-27T10:09:34.201991378Z |
| `process_cpu_seconds_total` | 0.01 | 92.53 | 0.01 | 92.53 | 2026-09-27T10:09:56.073995634Z |
| `process_network_receive_bytes_total` | 5.37014033e+09 | 9.104991177e+09 | 5.37014033e+09 | 9.104991177e+09 | 2026-09-27T10:09:56.073995634Z |
| `process_network_transmit_bytes_total` | 7.035955468e+09 | 1.0760557244e+10 | 7.035955468e+09 | 1.0760557244e+10 | 2026-09-27T10:09:56.073995634Z |
| `process_open_fds` | 13 | 30 | 13 | 94 | 2026-09-27T10:09:39.202519542Z |
| `process_resident_memory_bytes` | 3.3501184e+07 | 5.1781632e+07 | 3.3501184e+07 | 6.4057344e+07 | 2026-09-27T10:09:46.074937361Z |
| `process_start_time_seconds` | 1.79050376889e+09 | 1.79050377089e+09 | 1.79050376889e+09 | 1.79050377089e+09 | 2026-09-27T10:09:46.074937361Z |
| `process_virtual_memory_bytes` | 2.241921024e+09 | 3.217248256e+09 | 2.241921024e+09 | 3.217248256e+09 | 2026-09-27T10:09:34.201991378Z |
