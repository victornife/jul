# Metrics summary: /home/victornf/http_server-422/soak-artifacts/2026-09-26-fault-disk/metrics/samples.jsonl.gz

- url: `http://127.0.0.1:19901/metrics`, interval: 5s, labels: `{"config":"jul.toml","jul_sha":"a2e07d76c06b7c0389709af3d3fb164b8ced2610","profile":"disk","workload":"disk cache tier 24 MiB cap behind a 2 MiB memory tier; 32 workers filling unique 128 KiB cacheable objects; access+audit log on the same tmpfs; filler file to ~1 MiB free; managed config apply under pressure and after; filler removed"}`
- window: 2026-09-26T18:22:30.551504503Z → 2026-09-26T18:26:10.55594904Z, samples: 45, scrape errors: 0, gaps (>2× interval): 0 (0s), end: signal


## Quiescence (first vs last sample)

| Series | first | last | max | max at |
| --- | --- | --- | --- | --- |
| `go_goroutines` | 20 | 21 | 159 | 2026-09-26T18:23:55.555430823Z |
| `process_open_fds` | 14 | 15 | 90 | 2026-09-26T18:22:50.55541262Z |
| `process_resident_memory_bytes` | 3.21536e+07 | 5.8621952e+07 | 8.9694208e+07 | 2026-09-26T18:22:45.555402368Z |
| `go_memstats_heap_inuse_bytes` | 6.7584e+06 | 1.9603456e+07 | 3.8027264e+07 | 2026-09-26T18:23:00.555440071Z |
| `jul_listener_conns` | 0 | 0 | 33 | 2026-09-26T18:23:15.555586626Z |
| `jul_http_requests_in_flight` | 0 | 0 | 32 | 2026-09-26T18:22:35.555986731Z |

## All series (changed during the window)

| Series | first | last | min | max | max at |
| --- | --- | --- | --- | --- | --- |
| `go_goroutines` | 20 | 21 | 20 | 159 | 2026-09-26T18:23:55.555430823Z |
| `go_memstats_heap_alloc_bytes` | 4.770008e+06 | 1.557544e+07 | 4.770008e+06 | 3.4268952e+07 | 2026-09-26T18:23:00.555440071Z |
| `go_memstats_heap_inuse_bytes` | 6.7584e+06 | 1.9603456e+07 | 6.7584e+06 | 3.8027264e+07 | 2026-09-26T18:23:00.555440071Z |
| `go_memstats_next_gc_bytes` | 9.692786e+06 | 2.3845042e+07 | 9.692786e+06 | 5.0656354e+07 | 2026-09-26T18:22:45.555402368Z |
| `go_memstats_sys_bytes` | 1.8549016e+07 | 8.8144168e+07 | 1.8549016e+07 | 8.8144168e+07 | 2026-09-26T18:22:45.555402368Z |
| `go_threads` | 9 | 45 | 9 | 45 | 2026-09-26T18:24:10.555467751Z |
| `jul_cache_bytes{tier="disk"}` | 0 | 2.514897e+07 | 0 | 2.514897e+07 | 2026-09-26T18:22:35.555986731Z |
| `jul_cache_bytes{tier="memory"}` | 0 | 1.972095e+06 | 0 | 1.972095e+06 | 2026-09-26T18:22:35.555986731Z |
| `jul_cache_entries{tier="disk"}` | 0 | 191 | 0 | 191 | 2026-09-26T18:22:35.555986731Z |
| `jul_cache_entries{tier="memory"}` | 0 | 15 | 0 | 15 | 2026-09-26T18:22:35.555986731Z |
| `jul_cache_events_total{state="MISS"}` | 22164 | 514476 | 22164 | 514476 | 2026-09-26T18:24:25.556024876Z |
| `jul_cache_evictions_total{tier="disk"}` | 0 | 316344 | 0 | 316344 | 2026-09-26T18:24:25.556024876Z |
| `jul_cache_evictions_total{tier="memory"}` | 0 | 514391 | 0 | 514391 | 2026-09-26T18:24:25.556024876Z |
| `jul_client_addr_derivations_total{result="accepted",source="peer"}` | 22196 | 514500 | 22196 | 514500 | 2026-09-26T18:24:25.556024876Z |
| `jul_http_request_duration_seconds_count{host="",method="GET"}` | 22164 | 514476 | 22164 | 514476 | 2026-09-26T18:24:25.556024876Z |
| `jul_http_request_duration_seconds_sum{host="",method="GET"}` | 148.23103461199852 | 3343.52710297485 | 148.23103461199852 | 3343.52710297485 | 2026-09-26T18:24:25.556024876Z |
| `jul_http_requests_in_flight` | 0 | 0 | 0 | 32 | 2026-09-26T18:22:35.555986731Z |
| `jul_http_requests_total{code="200",host="",method="GET"}` | 22164 | 514406 | 22164 | 514406 | 2026-09-26T18:24:25.556024876Z |
| `jul_http_requests_total{code="499",host="",method="GET"}` | 20 | 70 | 20 | 70 | 2026-09-26T18:24:25.556024876Z |
| `jul_http_response_bytes_total` | 0 | 6.7424223582e+10 | 0 | 6.7424223582e+10 | 2026-09-26T18:24:25.556024876Z |
| `jul_listener_conns` | 0 | 0 | 0 | 33 | 2026-09-26T18:23:15.555586626Z |
| `jul_managed_apply_history_total{operation="config.apply",result="recorded"}` | 1 | 2 | 1 | 2 | 2026-09-26T18:23:55.555430823Z |
| `jul_managed_apply_terminal_registry_entries` | 0 | 3 | 0 | 3 | 2026-09-26T18:23:55.555430823Z |
| `jul_reload_duration_seconds_count{outcome="no_change",source="admin"}` | 1 | 2 | 1 | 2 | 2026-09-26T18:23:55.555430823Z |
| `jul_reload_duration_seconds_sum{outcome="no_change",source="admin"}` | 0.002 | 0.003 | 0.002 | 0.003 | 2026-09-26T18:23:55.555430823Z |
| `jul_reload_phase_duration_seconds_count{outcome="no_change",phase="change_assessment"}` | 1 | 2 | 1 | 2 | 2026-09-26T18:23:55.555430823Z |
| `jul_reload_phase_duration_seconds_count{outcome="no_change",phase="lifecycle"}` | 1 | 2 | 1 | 2 | 2026-09-26T18:23:55.555430823Z |
| `jul_reload_phase_duration_seconds_count{outcome="no_change",phase="resolve"}` | 1 | 2 | 1 | 2 | 2026-09-26T18:23:55.555430823Z |
| `jul_reload_phase_duration_seconds_count{outcome="no_change",phase="validate"}` | 1 | 2 | 1 | 2 | 2026-09-26T18:23:55.555430823Z |
| `jul_reload_total{outcome="no_change",source="admin"}` | 1 | 2 | 1 | 2 | 2026-09-26T18:23:55.555430823Z |
| `jul_upstream_active_requests{pool="app"}` | 0 | 0 | 0 | 28 | 2026-09-26T18:23:15.555586626Z |
| `jul_upstream_connections{pool="app"}` | 0 | 0 | 0 | 39 | 2026-09-26T18:23:55.555430823Z |
| `process_cpu_seconds_total` | 0.01 | 467.78 | 0.01 | 467.78 | 2026-09-26T18:26:10.55594904Z |
| `process_network_receive_bytes_total` | 8.17901590932e+11 | 9.54354153944e+11 | 8.17901590932e+11 | 9.54354153944e+11 | 2026-09-26T18:26:10.55594904Z |
| `process_network_transmit_bytes_total` | 8.19398039512e+11 | 9.55855127174e+11 | 8.19398039512e+11 | 9.55855127174e+11 | 2026-09-26T18:26:10.55594904Z |
| `process_open_fds` | 14 | 15 | 14 | 90 | 2026-09-26T18:22:50.55541262Z |
| `process_resident_memory_bytes` | 3.21536e+07 | 5.8621952e+07 | 3.21536e+07 | 8.9694208e+07 | 2026-09-26T18:22:45.555402368Z |
| `process_virtual_memory_bytes` | 1.399287808e+09 | 1.468895232e+09 | 1.399287808e+09 | 1.468895232e+09 | 2026-09-26T18:22:45.555402368Z |
