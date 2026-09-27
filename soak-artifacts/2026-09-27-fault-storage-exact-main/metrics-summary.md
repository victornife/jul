# Metrics summary: /home/christinjk/jul/soak-artifacts/2026-09-27-fault-storage-exact-main/metrics/samples.jsonl.gz

- url: `http://127.0.0.1:19901/metrics`, interval: 5s, labels: `{"config":"jul.toml","jul_sha":"015e5f4036a571e4cca3c95248748fa3cb90713b","profile":"storage","workload":"access log on the default 100 MB rotation (larger than the filesystem); 8 workers filling unique 128 KiB cacheable objects into a 16 MiB disk tier plus 48 keep-alive workers on small responses; /api/stats sampled every 0.5s; pressure cleared by truncating the access log, then a restart"}`
- window: 2026-09-27T20:43:02.100692889Z → 2026-09-27T20:43:27.103242312Z, samples: 6, scrape errors: 0, gaps (>2× interval): 0 (0s), end: signal


## Quiescence (first vs last sample)

| Series | first | last | max | max at |
| --- | --- | --- | --- | --- |
| `go_goroutines` | 20 | 79 | 179 | 2026-09-27T20:43:07.103331004Z |
| `process_open_fds` | 16 | 46 | 120 | 2026-09-27T20:43:12.10328984Z |
| `process_resident_memory_bytes` | 3.344384e+07 | 5.1707904e+07 | 6.3811584e+07 | 2026-09-27T20:43:07.103331004Z |
| `go_memstats_heap_inuse_bytes` | 7.143424e+06 | 1.1624448e+07 | 2.6886144e+07 | 2026-09-27T20:43:07.103331004Z |
| `jul_listener_conns` | 0 | 2 | 56 | 2026-09-27T20:43:07.103331004Z |
| `jul_http_requests_in_flight` | 0 | 2 | 9 | 2026-09-27T20:43:07.103331004Z |

## All series (changed during the window)

| Series | first | last | min | max | max at |
| --- | --- | --- | --- | --- | --- |
| `go_goroutines` | 20 | 79 | 20 | 179 | 2026-09-27T20:43:07.103331004Z |
| `go_memstats_heap_alloc_bytes` | 4.859032e+06 | 8.44568e+06 | 4.859032e+06 | 2.2796144e+07 | 2026-09-27T20:43:07.103331004Z |
| `go_memstats_heap_inuse_bytes` | 7.143424e+06 | 1.1624448e+07 | 7.143424e+06 | 2.6886144e+07 | 2026-09-27T20:43:07.103331004Z |
| `go_memstats_next_gc_bytes` | 9.710354e+06 | 1.650429e+07 | 9.710354e+06 | 2.6192626e+07 | 2026-09-27T20:43:07.103331004Z |
| `go_memstats_sys_bytes` | 1.9519752e+07 | 5.8452248e+07 | 1.9519752e+07 | 5.8452248e+07 | 2026-09-27T20:43:12.10328984Z |
| `go_threads` | 14 | 22 | 14 | 22 | 2026-09-27T20:43:07.103331004Z |
| `jul_cache_bytes{tier="disk"}` | 0 | 1.672209e+07 | 0 | 1.672209e+07 | 2026-09-27T20:43:07.103331004Z |
| `jul_cache_bytes{tier="memory"}` | 0 | 1.972095e+06 | 0 | 1.972493e+06 | 2026-09-27T20:43:07.103331004Z |
| `jul_cache_entries{tier="disk"}` | 0 | 127 | 0 | 127 | 2026-09-27T20:43:07.103331004Z |
| `jul_cache_entries{tier="memory"}` | 0 | 15 | 0 | 16 | 2026-09-27T20:43:07.103331004Z |
| `jul_cache_events_total{state="HIT"}` | 44688 | 197370 | 44688 | 197370 | 2026-09-27T20:43:27.103242312Z |
| `jul_cache_events_total{state="MISS"}` | 2825 | 13104 | 2825 | 13104 | 2026-09-27T20:43:27.103242312Z |
| `jul_cache_evictions_total{tier="disk"}` | 0 | 7275 | 0 | 7275 | 2026-09-27T20:43:27.103242312Z |
| `jul_cache_evictions_total{tier="memory"}` | 0 | 13045 | 0 | 13045 | 2026-09-27T20:43:27.103242312Z |
| `jul_client_addr_derivations_total{result="accepted",source="peer"}` | 47521 | 210477 | 47521 | 210477 | 2026-09-27T20:43:27.103242312Z |
| `jul_http_request_duration_seconds_count{host="",method="GET"}` | 47513 | 210474 | 47513 | 210474 | 2026-09-27T20:43:27.103242312Z |
| `jul_http_request_duration_seconds_sum{host="",method="GET"}` | 40.80547911700025 | 225.90428814900457 | 40.80547911700025 | 225.90428814900457 | 2026-09-27T20:43:27.103242312Z |
| `jul_http_requests_in_flight` | 0 | 2 | 0 | 9 | 2026-09-27T20:43:07.103331004Z |
| `jul_http_requests_total{code="200",host="",method="GET"}` | 47513 | 210467 | 47513 | 210467 | 2026-09-27T20:43:27.103242312Z |
| `jul_http_response_bytes_total` | 0 | 1.719763011e+09 | 0 | 1.719763011e+09 | 2026-09-27T20:43:27.103242312Z |
| `jul_listener_conns` | 0 | 2 | 0 | 56 | 2026-09-27T20:43:07.103331004Z |
| `jul_storage_bytes{category="access_log",kind="available"}` | 5.0327552e+07 | 3.3071104e+07 | 0 | 5.0327552e+07 | 2026-09-27T20:43:02.100692889Z |
| `jul_storage_bytes{category="audit_log",kind="available"}` | 5.0327552e+07 | 3.3071104e+07 | 0 | 5.0327552e+07 | 2026-09-27T20:43:02.100692889Z |
| `jul_storage_bytes{category="cache",kind="available"}` | 5.0327552e+07 | 3.3071104e+07 | 0 | 5.0327552e+07 | 2026-09-27T20:43:02.100692889Z |
| `jul_storage_bytes{category="config",kind="available"}` | 5.0327552e+07 | 3.3071104e+07 | 0 | 5.0327552e+07 | 2026-09-27T20:43:02.100692889Z |
| `jul_storage_bytes{category="config_history",kind="available"}` | 5.0327552e+07 | 3.3071104e+07 | 0 | 5.0327552e+07 | 2026-09-27T20:43:02.100692889Z |
| `jul_upstream_active_requests{pool="app"}` | 0 | 2 | 0 | 6 | 2026-09-27T20:43:07.103331004Z |
| `jul_upstream_connections{pool="app"}` | 0 | 27 | 0 | 46 | 2026-09-27T20:43:07.103331004Z |
| `process_cpu_seconds_total` | 0.02 | 88.09 | 0.02 | 88.09 | 2026-09-27T20:43:27.103242312Z |
| `process_network_receive_bytes_total` | 9.391134349e+09 | 1.2939001268e+10 | 9.391134349e+09 | 1.2939001268e+10 | 2026-09-27T20:43:27.103242312Z |
| `process_network_transmit_bytes_total` | 1.1471314625e+10 | 1.5008204589e+10 | 1.1471314625e+10 | 1.5008204589e+10 | 2026-09-27T20:43:27.103242312Z |
| `process_open_fds` | 16 | 46 | 16 | 120 | 2026-09-27T20:43:12.10328984Z |
| `process_resident_memory_bytes` | 3.344384e+07 | 5.1707904e+07 | 3.344384e+07 | 6.3811584e+07 | 2026-09-27T20:43:07.103331004Z |
| `process_virtual_memory_bytes` | 2.242289664e+09 | 2.915094528e+09 | 2.242289664e+09 | 2.915094528e+09 | 2026-09-27T20:43:07.103331004Z |
