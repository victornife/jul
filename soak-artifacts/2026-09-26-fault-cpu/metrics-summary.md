# Metrics summary: /home/victornf/http_server-422/soak-artifacts/2026-09-26-fault-cpu/metrics/samples.jsonl.gz

- url: `http://127.0.0.1:19901/metrics`, interval: 5s, labels: `{"config":"jul.toml","jul_sha":"a2e07d76c06b7c0389709af3d3fb164b8ced2610","profile":"cpu","workload":"64 workers keep-alive GET /slow?ms=5 (unconstrained baseline, then CPUQuota=20%, then quota lifted live); SIGHUP reload under quota; SIGTERM shutdown timed"}`
- window: 2026-09-26T18:14:27.611560832Z → 2026-09-26T18:18:17.616744257Z, samples: 47, scrape errors: 0, gaps (>2× interval): 0 (0s), end: signal


## Quiescence (first vs last sample)

| Series | first | last | max | max at |
| --- | --- | --- | --- | --- |
| `go_goroutines` | 22 | 21 | 289 | 2026-09-26T18:16:22.615411352Z |
| `process_open_fds` | 11 | 11 | 146 | 2026-09-26T18:16:22.615411352Z |
| `process_resident_memory_bytes` | 3.1629312e+07 | 5.1519488e+07 | 5.2666368e+07 | 2026-09-26T18:15:27.615949154Z |
| `go_memstats_heap_inuse_bytes` | 6.561792e+06 | 1.6089088e+07 | 1.8210816e+07 | 2026-09-26T18:15:27.615949154Z |
| `jul_listener_conns` | 0 | 0 | 64 | 2026-09-26T18:14:32.615405018Z |
| `jul_http_requests_in_flight` | 0 | 0 | 64 | 2026-09-26T18:14:32.615405018Z |

## All series (changed during the window)

| Series | first | last | min | max | max at |
| --- | --- | --- | --- | --- | --- |
| `go_goroutines` | 22 | 21 | 21 | 289 | 2026-09-26T18:16:22.615411352Z |
| `go_memstats_heap_alloc_bytes` | 4.731688e+06 | 1.2125576e+07 | 4.731688e+06 | 1.4874752e+07 | 2026-09-26T18:14:52.615420115Z |
| `go_memstats_heap_inuse_bytes` | 6.561792e+06 | 1.6089088e+07 | 6.561792e+06 | 1.8210816e+07 | 2026-09-26T18:15:27.615949154Z |
| `go_memstats_next_gc_bytes` | 9.657682e+06 | 2.0131954e+07 | 9.657682e+06 | 2.0131954e+07 | 2026-09-26T18:17:57.616881361Z |
| `go_memstats_sys_bytes` | 1.8987272e+07 | 4.1605384e+07 | 1.8987272e+07 | 4.1605384e+07 | 2026-09-26T18:14:37.615405959Z |
| `go_threads` | 10 | 30 | 10 | 30 | 2026-09-26T18:16:17.615372284Z |
| `jul_client_addr_derivations_total{result="accepted",source="peer"}` | 48289 | 610679 | 48289 | 610679 | 2026-09-26T18:16:32.615638904Z |
| `jul_http_request_duration_seconds_count{host="",method="GET"}` | 48225 | 610679 | 48225 | 610679 | 2026-09-26T18:16:32.615638904Z |
| `jul_http_request_duration_seconds_sum{host="",method="GET"}` | 290.4861461539999 | 6337.534594981939 | 290.4861461539999 | 6337.534594981939 | 2026-09-26T18:16:32.615638904Z |
| `jul_http_requests_in_flight` | 0 | 0 | 0 | 64 | 2026-09-26T18:14:32.615405018Z |
| `jul_http_requests_total{code="200",host="",method="GET"}` | 48225 | 610574 | 48225 | 610574 | 2026-09-26T18:16:32.615638904Z |
| `jul_http_requests_total{code="499",host="",method="GET"}` | 45 | 105 | 45 | 105 | 2026-09-26T18:16:32.615638904Z |
| `jul_http_response_bytes_total` | 0 | 2.7476355e+07 | 0 | 2.7476355e+07 | 2026-09-26T18:16:32.615638904Z |
| `jul_listener_conns` | 0 | 0 | 0 | 64 | 2026-09-26T18:14:32.615405018Z |
| `jul_upstream_active_requests{pool="app"}` | 0 | 0 | 0 | 64 | 2026-09-26T18:14:32.615405018Z |
| `jul_upstream_connections{pool="app"}` | 0 | 0 | 0 | 71 | 2026-09-26T18:16:22.615411352Z |
| `jul_upstream_probe_duration_seconds_count{pool="app",source="http"}` | 4 | 224 | 4 | 224 | 2026-09-26T18:18:17.616744257Z |
| `jul_upstream_probe_duration_seconds_sum{pool="app",source="http"}` | 0.003292803 | 4.983022987000003 | 0.003292803 | 4.983022987000003 | 2026-09-26T18:18:17.616744257Z |
| `jul_upstream_probes_total{pool="app",result="success",source="http"}` | 4 | 224 | 4 | 224 | 2026-09-26T18:18:17.616744257Z |
| `process_cpu_seconds_total` | 0.04 | 134.72 | 0.04 | 134.72 | 2026-09-26T18:18:17.616744257Z |
| `process_network_receive_bytes_total` | 2.89261563747e+11 | 2.89815399529e+11 | 2.89261563747e+11 | 2.89815399529e+11 | 2026-09-26T18:18:17.616744257Z |
| `process_network_transmit_bytes_total` | 2.9075355983e+11 | 2.9131190214e+11 | 2.9075355983e+11 | 2.9131190214e+11 | 2026-09-26T18:18:17.616744257Z |
| `process_open_fds` | 11 | 11 | 11 | 146 | 2026-09-26T18:16:22.615411352Z |
| `process_resident_memory_bytes` | 3.1629312e+07 | 5.1519488e+07 | 3.1629312e+07 | 5.2666368e+07 | 2026-09-26T18:15:27.615949154Z |
| `process_virtual_memory_bytes` | 1.33261312e+09 | 1.334263808e+09 | 1.33261312e+09 | 1.334263808e+09 | 2026-09-26T18:14:32.615405018Z |
