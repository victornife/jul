# Metrics summary: /home/victornf/http_server-422/soak-artifacts/2026-09-26-fault-fd/metrics/samples.jsonl.gz

- url: `http://127.0.0.1:19901/metrics`, interval: 5s, labels: `{"config":"jul.toml","jul_sha":"a2e07d76c06b7c0389709af3d3fb164b8ced2610","profile":"fd","workload":"baseline 16 workers; pressure: 400 held idle TCP connections + 64 workers with fresh connections per request; recovery 16 workers"}`
- window: 2026-09-26T18:11:16.160239298Z → 2026-09-26T18:14:26.165324518Z, samples: 39, scrape errors: 0, gaps (>2× interval): 0 (0s), end: signal


## Quiescence (first vs last sample)

| Series | first | last | max | max at |
| --- | --- | --- | --- | --- |
| `go_goroutines` | 20 | 20 | 286 | 2026-09-26T18:11:56.163454246Z |
| `process_open_fds` | 12 | 12 | 256 | 2026-09-26T18:11:41.16448739Z |
| `process_resident_memory_bytes` | 3.1563776e+07 | 4.8685056e+07 | 5.165056e+07 | 2026-09-26T18:12:16.163367899Z |
| `go_memstats_heap_inuse_bytes` | 6.79936e+06 | 1.6760832e+07 | 1.8087936e+07 | 2026-09-26T18:12:01.163428886Z |
| `jul_listener_conns` | 0 | 0 | 226 | 2026-09-26T18:11:41.16448739Z |
| `jul_http_requests_in_flight` | 0 | 0 | 31 | 2026-09-26T18:12:11.163414764Z |

## All series (changed during the window)

| Series | first | last | min | max | max at |
| --- | --- | --- | --- | --- | --- |
| `go_goroutines` | 20 | 20 | 20 | 286 | 2026-09-26T18:11:56.163454246Z |
| `go_memstats_heap_alloc_bytes` | 4.746632e+06 | 1.2366168e+07 | 4.746632e+06 | 1.4732184e+07 | 2026-09-26T18:12:01.163428886Z |
| `go_memstats_heap_inuse_bytes` | 6.79936e+06 | 1.6760832e+07 | 6.79936e+06 | 1.8087936e+07 | 2026-09-26T18:12:01.163428886Z |
| `go_memstats_next_gc_bytes` | 9.684354e+06 | 1.8116274e+07 | 9.684354e+06 | 1.929085e+07 | 2026-09-26T18:12:01.163428886Z |
| `go_memstats_sys_bytes` | 1.9503368e+07 | 4.1675032e+07 | 1.9503368e+07 | 4.1675032e+07 | 2026-09-26T18:11:51.163402559Z |
| `go_threads` | 13 | 33 | 13 | 33 | 2026-09-26T18:12:16.163367899Z |
| `jul_client_addr_derivations_total{result="accepted",source="peer"}` | 102504 | 1.206237e+06 | 102504 | 1.206237e+06 | 2026-09-26T18:12:41.167217596Z |
| `jul_http_request_duration_seconds_count{host="",method="GET"}` | 102496 | 1.206237e+06 | 102496 | 1.206237e+06 | 2026-09-26T18:12:41.167217596Z |
| `jul_http_request_duration_seconds_sum{host="",method="GET"}` | 41.490720098999766 | 664.1641915469933 | 41.490720098999766 | 664.1641915469933 | 2026-09-26T18:12:41.167217596Z |
| `jul_http_requests_in_flight` | 0 | 0 | 0 | 31 | 2026-09-26T18:12:11.163414764Z |
| `jul_http_requests_total{code="200",host="",method="GET"}` | 102496 | 1.206097e+06 | 102496 | 1.206097e+06 | 2026-09-26T18:12:41.167217596Z |
| `jul_http_requests_total{code="499",host="",method="GET"}` | 1 | 114 | 1 | 114 | 2026-09-26T18:12:41.167217596Z |
| `jul_http_response_bytes_total` | 0 | 4.9450963e+07 | 0 | 4.9450963e+07 | 2026-09-26T18:12:41.167217596Z |
| `jul_listener_conns` | 0 | 0 | 0 | 226 | 2026-09-26T18:11:41.16448739Z |
| `jul_upstream_active_requests{pool="app"}` | 0 | 0 | 0 | 37 | 2026-09-26T18:12:11.163414764Z |
| `jul_upstream_connections{pool="app"}` | 0 | 0 | 0 | 64 | 2026-09-26T18:12:01.163428886Z |
| `process_cpu_seconds_total` | 0.03 | 222.83 | 0.03 | 222.83 | 2026-09-26T18:14:21.164535796Z |
| `process_network_receive_bytes_total` | 2.88026672492e+11 | 2.89261528607e+11 | 2.88026672492e+11 | 2.89261528607e+11 | 2026-09-26T18:14:26.165324518Z |
| `process_network_transmit_bytes_total` | 2.89516402746e+11 | 2.9075352469e+11 | 2.89516402746e+11 | 2.9075352469e+11 | 2026-09-26T18:14:26.165324518Z |
| `process_open_fds` | 12 | 12 | 12 | 256 | 2026-09-26T18:11:41.16448739Z |
| `process_resident_memory_bytes` | 3.1563776e+07 | 4.8685056e+07 | 3.1563776e+07 | 5.165056e+07 | 2026-09-26T18:12:16.163367899Z |
| `process_virtual_memory_bytes` | 1.333129216e+09 | 1.4014464e+09 | 1.333129216e+09 | 1.4014464e+09 | 2026-09-26T18:11:26.163369788Z |
