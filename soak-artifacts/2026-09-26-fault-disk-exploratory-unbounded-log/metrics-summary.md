# Metrics summary: /home/victornf/http_server-422/soak-artifacts/2026-09-26-fault-disk/metrics/samples.jsonl.gz

- url: `http://127.0.0.1:19901/metrics`, interval: 5s, labels: `{"config":"jul.toml","jul_sha":"99934938a04666345aac3a807d284ff67c109411","profile":"disk","workload":"disk cache tier 24 MiB cap behind a 2 MiB memory tier; 32 workers filling unique 128 KiB cacheable objects; access+audit log on the same tmpfs; filler file to ~1 MiB free; managed config apply under pressure and after; filler removed"}`
- window: 2026-09-26T17:44:02.344699783Z → 2026-09-26T17:46:07.345116327Z, samples: 26, scrape errors: 0, gaps (>2× interval): 0 (0s), end: signal


## Quiescence (first vs last sample)

| Series | first | last | max | max at |
| --- | --- | --- | --- | --- |
| `go_goroutines` | 20 | 57 | 160 | 2026-09-26T17:45:47.347526449Z |
| `process_open_fds` | 14 | 33 | 86 | 2026-09-26T17:45:22.347382433Z |
| `process_resident_memory_bytes` | 3.1760384e+07 | 5.1150848e+07 | 8.2796544e+07 | 2026-09-26T17:45:47.347526449Z |
| `go_memstats_heap_inuse_bytes` | 6.733824e+06 | 1.3492224e+07 | 3.7298176e+07 | 2026-09-26T17:45:47.347526449Z |
| `jul_listener_conns` | 0 | 0 | 32 | 2026-09-26T17:44:07.34736519Z |
| `jul_http_requests_in_flight` | 0 | 0 | 32 | 2026-09-26T17:44:12.347365617Z |

## All series (changed during the window)

| Series | first | last | min | max | max at |
| --- | --- | --- | --- | --- | --- |
| `go_goroutines` | 20 | 57 | 20 | 160 | 2026-09-26T17:45:47.347526449Z |
| `go_memstats_heap_alloc_bytes` | 4.7666e+06 | 9.787488e+06 | 4.7666e+06 | 3.321904e+07 | 2026-09-26T17:45:47.347526449Z |
| `go_memstats_heap_inuse_bytes` | 6.733824e+06 | 1.3492224e+07 | 6.733824e+06 | 3.7298176e+07 | 2026-09-26T17:45:47.347526449Z |
| `go_memstats_next_gc_bytes` | 9.646482e+06 | 1.8319474e+07 | 9.646482e+06 | 3.7707362e+07 | 2026-09-26T17:45:47.347526449Z |
| `go_memstats_sys_bytes` | 1.459228e+07 | 8.368772e+07 | 1.459228e+07 | 8.368772e+07 | 2026-09-26T17:44:17.347636984Z |
| `go_threads` | 10 | 38 | 10 | 38 | 2026-09-26T17:45:17.344988134Z |
| `jul_cache_bytes{tier="disk"}` | 0 | 2.514897e+07 | 0 | 2.514897e+07 | 2026-09-26T17:44:07.34736519Z |
| `jul_cache_bytes{tier="memory"}` | 0 | 1.972095e+06 | 0 | 1.972095e+06 | 2026-09-26T17:44:07.34736519Z |
| `jul_cache_entries{tier="disk"}` | 0 | 191 | 0 | 191 | 2026-09-26T17:44:07.34736519Z |
| `jul_cache_entries{tier="memory"}` | 0 | 15 | 0 | 15 | 2026-09-26T17:44:07.34736519Z |
| `jul_cache_events_total{state="HIT"}` | 191 | 382 | 191 | 382 | 2026-09-26T17:45:47.347526449Z |
| `jul_cache_events_total{state="MISS"}` | 23740 | 517907 | 23740 | 517907 | 2026-09-26T17:45:57.348929732Z |
| `jul_cache_evictions_total{tier="disk"}` | 0 | 80862 | 0 | 80862 | 2026-09-26T17:44:22.347368654Z |
| `jul_cache_evictions_total{tier="memory"}` | 0 | 518218 | 0 | 518218 | 2026-09-26T17:45:57.348929732Z |
| `jul_client_addr_derivations_total{result="accepted",source="peer"}` | 23771 | 518321 | 23771 | 518321 | 2026-09-26T17:45:57.348929732Z |
| `jul_http_request_duration_seconds_count{host="",method="GET"}` | 23740 | 518289 | 23740 | 518289 | 2026-09-26T17:45:57.348929732Z |
| `jul_http_request_duration_seconds_sum{host="",method="GET"}` | 148.69372377700074 | 3295.989896957071 | 148.69372377700074 | 3295.989896957071 | 2026-09-26T17:45:57.348929732Z |
| `jul_http_requests_in_flight` | 0 | 0 | 0 | 32 | 2026-09-26T17:44:12.347365617Z |
| `jul_http_requests_total{code="200",host="",method="GET"}` | 23740 | 518233 | 23740 | 518233 | 2026-09-26T17:45:57.348929732Z |
| `jul_http_requests_total{code="499",host="",method="GET"}` | 23 | 56 | 23 | 56 | 2026-09-26T17:45:57.348929732Z |
| `jul_http_response_bytes_total` | 0 | 6.7925836056e+10 | 0 | 6.7925836056e+10 | 2026-09-26T17:45:57.348929732Z |
| `jul_listener_conns` | 0 | 0 | 0 | 32 | 2026-09-26T17:44:07.34736519Z |
| `jul_managed_apply_terminal_registry_entries` | 0 | 2 | 0 | 2 | 2026-09-26T17:44:07.34736519Z |
| `jul_upstream_active_requests{pool="app"}` | 0 | 0 | 0 | 29 | 2026-09-26T17:45:47.347526449Z |
| `jul_upstream_connections{pool="app"}` | 0 | 18 | 0 | 38 | 2026-09-26T17:44:52.347364841Z |
| `process_cpu_seconds_total` | 0.02 | 466.1 | 0.02 | 466.1 | 2026-09-26T17:46:07.345116327Z |
| `process_network_receive_bytes_total` | 7.7044347646e+10 | 2.14458444904e+11 | 7.7044347646e+10 | 2.14458444904e+11 | 2026-09-26T17:46:07.345116327Z |
| `process_network_transmit_bytes_total` | 7.8410388879e+10 | 2.15832414651e+11 | 7.8410388879e+10 | 2.15832414651e+11 | 2026-09-26T17:46:07.345116327Z |
| `process_open_fds` | 14 | 33 | 14 | 86 | 2026-09-26T17:45:22.347382433Z |
| `process_resident_memory_bytes` | 3.1760384e+07 | 5.1150848e+07 | 3.1760384e+07 | 8.2796544e+07 | 2026-09-26T17:45:47.347526449Z |
| `process_virtual_memory_bytes` | 1.399525376e+09 | 1.468633088e+09 | 1.399525376e+09 | 1.468633088e+09 | 2026-09-26T17:45:02.347430215Z |
