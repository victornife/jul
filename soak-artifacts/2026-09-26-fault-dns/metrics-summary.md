# Metrics summary: /home/victornf/http_server-422/soak-artifacts/2026-09-26-fault-dns/metrics/samples.jsonl.gz

- url: `http://127.0.0.1:19901/metrics`, interval: 5s, labels: `{"config":"jul.toml","jul_sha":"a2e07d76c06b7c0389709af3d3fb164b8ced2610","profile":"dns","workload":"fault-load 16 workers keep-alive GET / through a dns-discovered upstream (refresh 5s)"}`
- window: 2026-09-26T18:06:15.1237082Z → 2026-09-26T18:11:10.128257946Z, samples: 60, scrape errors: 0, gaps (>2× interval): 0 (0s), end: signal


## Quiescence (first vs last sample)

| Series | first | last | max | max at |
| --- | --- | --- | --- | --- |
| `go_goroutines` | 21 | 21 | 120 | 2026-09-26T18:08:55.127586533Z |
| `process_open_fds` | 12 | 12 | 63 | 2026-09-26T18:08:55.127586533Z |
| `process_resident_memory_bytes` | 3.2350208e+07 | 4.8099328e+07 | 4.9770496e+07 | 2026-09-26T18:09:15.127369487Z |
| `go_memstats_heap_inuse_bytes` | 7.241728e+06 | 1.7055744e+07 | 2.0496384e+07 | 2026-09-26T18:07:40.127563819Z |
| `jul_listener_conns` | 0 | 0 | 16 | 2026-09-26T18:06:20.127403498Z |
| `jul_http_requests_in_flight` | 0 | 0 | 16 | 2026-09-26T18:07:05.127367828Z |

## All series (changed during the window)

| Series | first | last | min | max | max at |
| --- | --- | --- | --- | --- | --- |
| `go_goroutines` | 21 | 21 | 21 | 120 | 2026-09-26T18:08:55.127586533Z |
| `go_memstats_heap_alloc_bytes` | 5.035136e+06 | 1.30694e+07 | 5.035136e+06 | 1.7394376e+07 | 2026-09-26T18:07:40.127563819Z |
| `go_memstats_heap_inuse_bytes` | 7.241728e+06 | 1.7055744e+07 | 7.241728e+06 | 2.0496384e+07 | 2026-09-26T18:07:40.127563819Z |
| `go_memstats_next_gc_bytes` | 1.0225138e+07 | 1.8226018e+07 | 1.0225138e+07 | 2.0215698e+07 | 2026-09-26T18:07:40.127563819Z |
| `go_memstats_sys_bytes` | 1.8995464e+07 | 4.1605384e+07 | 1.8995464e+07 | 4.1605384e+07 | 2026-09-26T18:06:30.127390984Z |
| `go_threads` | 13 | 27 | 13 | 27 | 2026-09-26T18:07:15.127386671Z |
| `jul_client_addr_derivations_total{result="accepted",source="peer"}` | 100990 | 3.645586e+06 | 100990 | 3.645586e+06 | 2026-09-26T18:09:25.128013527Z |
| `jul_discovery_errors_total{pool="app"}` | 1 | 17 | 1 | 17 | 2026-09-26T18:08:50.127375691Z |
| `jul_http_request_duration_seconds_count{host="",method="GET"}` | 100980 | 3.645586e+06 | 100980 | 3.645586e+06 | 2026-09-26T18:09:25.128013527Z |
| `jul_http_request_duration_seconds_sum{host="",method="GET"}` | 42.107139191999856 | 1580.3500549998887 | 42.107139191999856 | 1580.3500549998887 | 2026-09-26T18:09:25.128013527Z |
| `jul_http_requests_in_flight` | 0 | 0 | 0 | 16 | 2026-09-26T18:07:05.127367828Z |
| `jul_http_requests_total{code="200",host="",method="GET"}` | 100980 | 3.645583e+06 | 100980 | 3.645583e+06 | 2026-09-26T18:09:25.128013527Z |
| `jul_http_response_bytes_total` | 0 | 1.49468918e+08 | 0 | 1.49468918e+08 | 2026-09-26T18:09:25.128013527Z |
| `jul_listener_conns` | 0 | 0 | 0 | 16 | 2026-09-26T18:06:20.127403498Z |
| `jul_upstream_active_requests{pool="app"}` | 0 | 0 | 0 | 15 | 2026-09-26T18:08:00.127399534Z |
| `jul_upstream_backends_eligible{pool="app"}` | 2 | 1 | 1 | 3 | 2026-09-26T18:06:50.127617431Z |
| `jul_upstream_backends{pool="app"}` | 2 | 1 | 1 | 3 | 2026-09-26T18:06:50.127617431Z |
| `jul_upstream_circuit_state{pool="app",state="available"}` | 2 | 1 | 1 | 3 | 2026-09-26T18:06:50.127617431Z |
| `jul_upstream_connections{pool="app"}` | 0 | 0 | 0 | 35 | 2026-09-26T18:08:55.127586533Z |
| `process_cpu_seconds_total` | 0.04 | 690.17 | 0.04 | 690.17 | 2026-09-26T18:11:10.128257946Z |
| `process_network_receive_bytes_total` | 17130 | 3.187304747e+09 | 17130 | 3.187304747e+09 | 2026-09-26T18:11:10.128257946Z |
| `process_network_transmit_bytes_total` | 17130 | 3.187304747e+09 | 17130 | 3.187304747e+09 | 2026-09-26T18:11:10.128257946Z |
| `process_open_fds` | 12 | 12 | 12 | 63 | 2026-09-26T18:08:55.127586533Z |
| `process_resident_memory_bytes` | 3.2350208e+07 | 4.8099328e+07 | 3.2350208e+07 | 4.9770496e+07 | 2026-09-26T18:09:15.127369487Z |
| `process_virtual_memory_bytes` | 1.332621312e+09 | 1.334263808e+09 | 1.332621312e+09 | 1.334263808e+09 | 2026-09-26T18:06:20.127403498Z |
