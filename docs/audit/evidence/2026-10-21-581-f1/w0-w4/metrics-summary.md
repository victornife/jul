# Metrics summary: /home/christinjk/jul/docs/audit/evidence/2026-10-21-581-f1/w0-w4/metrics/samples.jsonl.gz

- url: `http://127.0.0.1:9443/metrics`, interval: 5s, labels: `{"config":"examples/sharing/f1-standalone/jul.toml","jul_sha":"52b09adfbccfc3b14a86ce48a2de06395a7fae70","scale":"1.0"}`
- window: 2026-10-21T23:04:30.642763476Z → 2026-10-21T23:43:10.644223739Z, samples: 465, scrape errors: 0, gaps (>2× interval): 0 (0s), end: signal


## Quiescence (first vs last sample)

| Series | first | last | max | max at |
| --- | --- | --- | --- | --- |
| `go_goroutines` | 22 | 20 | 106 | 2026-10-21T23:34:05.644593009Z |
| `process_open_fds` | 12 | 11 | 42 | 2026-10-21T23:33:50.647842022Z |
| `process_resident_memory_bytes` | 1.04472576e+08 | 1.08937216e+08 | 1.38637312e+08 | 2026-10-21T23:41:05.6474056Z |
| `go_memstats_heap_inuse_bytes` | 5.0929664e+07 | 6.0121088e+07 | 9.4216192e+07 | 2026-10-21T23:39:50.643582034Z |
| `jul_listener_conns` | 0 | 0 | 8 | 2026-10-21T23:34:30.647510767Z |
| `jul_http_requests_in_flight` | 0 | 0 | 24 | 2026-10-21T23:31:35.643023641Z |

## All series (changed during the window)

| Series | first | last | min | max | max at |
| --- | --- | --- | --- | --- | --- |
| `go_goroutines` | 22 | 20 | 20 | 106 | 2026-10-21T23:34:05.644593009Z |
| `go_memstats_heap_alloc_bytes` | 3.9298928e+07 | 4.3003728e+07 | 3.4830936e+07 | 8.0828312e+07 | 2026-10-21T23:39:50.643582034Z |
| `go_memstats_heap_inuse_bytes` | 5.0929664e+07 | 6.0121088e+07 | 4.784128e+07 | 9.4216192e+07 | 2026-10-21T23:39:50.643582034Z |
| `go_memstats_next_gc_bytes` | 6.6310618e+07 | 7.6141546e+07 | 6.6310618e+07 | 9.1834186e+07 | 2026-10-21T23:33:35.644498653Z |
| `go_memstats_sys_bytes` | 8.4379928e+07 | 1.19925016e+08 | 8.4379928e+07 | 1.19925016e+08 | 2026-10-21T23:36:30.647197997Z |
| `go_threads` | 13 | 17 | 13 | 17 | 2026-10-21T23:20:10.643794377Z |
| `jul_auth_decisions_total{method="basic",result="allow"}` | 7 | 12746 | 7 | 12746 | 2026-10-21T23:39:35.64722436Z |
| `jul_auth_decisions_total{method="basic",result="deny"}` | 2 | 1441 | 2 | 1441 | 2026-10-21T23:37:35.644308587Z |
| `jul_client_addr_derivations_total{result="accepted",source="peer"}` | 11 | 72785 | 11 | 72785 | 2026-10-21T23:39:35.64722436Z |
| `jul_http_backend_dial_failures_total{reason="no_backend"}` | 14 | 792 | 14 | 792 | 2026-10-21T23:39:05.644863707Z |
| `jul_http_backend_dial_failures_total{reason="refused"}` | 4 | 9 | 4 | 9 | 2026-10-21T23:38:55.647548442Z |
| `jul_http_request_duration_seconds_count{host="",method="GET"}` | 10 | 72763 | 10 | 72763 | 2026-10-21T23:39:35.64722436Z |
| `jul_http_request_duration_seconds_sum{host="",method="GET"}` | 0.532480757 | 1172.4241323980004 | 0.532480757 | 1172.4241323980004 | 2026-10-21T23:39:35.64722436Z |
| `jul_http_requests_in_flight` | 0 | 0 | 0 | 24 | 2026-10-21T23:31:35.643023641Z |
| `jul_http_requests_total{code="200",host="",method="GET"}` | 8 | 68366 | 8 | 68366 | 2026-10-21T23:39:35.64722436Z |
| `jul_http_requests_total{code="401",host="",method="GET"}` | 2 | 1441 | 2 | 1441 | 2026-10-21T23:37:35.644308587Z |
| `jul_http_requests_total{code="403",host="",method="GET"}` | 13 | 2150 | 13 | 2150 | 2026-10-21T23:36:30.647197997Z |
| `jul_http_requests_total{code="502",host="",method="GET"}` | 4 | 9 | 4 | 9 | 2026-10-21T23:38:55.647548442Z |
| `jul_http_requests_total{code="503",host="",method="GET"}` | 14 | 792 | 14 | 792 | 2026-10-21T23:39:05.644863707Z |
| `jul_http_response_bytes_total` | 278660 | 2.060770149e+09 | 278660 | 2.060770149e+09 | 2026-10-21T23:39:35.64722436Z |
| `jul_listener_conns` | 0 | 0 | 0 | 8 | 2026-10-21T23:34:30.647510767Z |
| `jul_managed_apply_finalized_total{mode="hot",operation="config.apply",outcome="applied_live",restored="n/a"}` | 5 | 7 | 5 | 7 | 2026-10-21T23:36:30.647197997Z |
| `jul_managed_apply_history_total{operation="config.apply",result="recorded"}` | 5 | 7 | 5 | 7 | 2026-10-21T23:36:30.647197997Z |
| `jul_managed_apply_terminal_registry_entries` | 5 | 7 | 5 | 7 | 2026-10-21T23:36:30.647197997Z |
| `jul_reload_duration_seconds_count{outcome="applied_live",source="admin"}` | 5 | 7 | 5 | 7 | 2026-10-21T23:36:30.647197997Z |
| `jul_reload_duration_seconds_sum{outcome="applied_live",source="admin"}` | 0.11800000000000001 | 0.18500000000000003 | 0.11800000000000001 | 0.18500000000000003 | 2026-10-21T23:36:30.647197997Z |
| `jul_reload_phase_duration_seconds_count{outcome="applied_live",phase="activate"}` | 5 | 7 | 5 | 7 | 2026-10-21T23:36:30.647197997Z |
| `jul_reload_phase_duration_seconds_count{outcome="applied_live",phase="change_assessment"}` | 5 | 7 | 5 | 7 | 2026-10-21T23:36:30.647197997Z |
| `jul_reload_phase_duration_seconds_count{outcome="applied_live",phase="lifecycle"}` | 5 | 7 | 5 | 7 | 2026-10-21T23:36:30.647197997Z |
| `jul_reload_phase_duration_seconds_count{outcome="applied_live",phase="post_commit"}` | 5 | 7 | 5 | 7 | 2026-10-21T23:36:30.647197997Z |
| `jul_reload_phase_duration_seconds_count{outcome="applied_live",phase="prepare"}` | 5 | 7 | 5 | 7 | 2026-10-21T23:36:30.647197997Z |
| `jul_reload_phase_duration_seconds_count{outcome="applied_live",phase="publish"}` | 5 | 7 | 5 | 7 | 2026-10-21T23:36:30.647197997Z |
| `jul_reload_phase_duration_seconds_count{outcome="applied_live",phase="resolve"}` | 5 | 7 | 5 | 7 | 2026-10-21T23:36:30.647197997Z |
| `jul_reload_phase_duration_seconds_count{outcome="applied_live",phase="retire"}` | 5 | 7 | 5 | 7 | 2026-10-21T23:36:30.647197997Z |
| `jul_reload_phase_duration_seconds_count{outcome="applied_live",phase="stage_listeners"}` | 5 | 7 | 5 | 7 | 2026-10-21T23:36:30.647197997Z |
| `jul_reload_phase_duration_seconds_count{outcome="applied_live",phase="validate"}` | 5 | 7 | 5 | 7 | 2026-10-21T23:36:30.647197997Z |
| `jul_reload_phase_duration_seconds_sum{outcome="applied_live",phase="change_assessment"}` | 0.011000000000000001 | 0.017 | 0.011000000000000001 | 0.017 | 2026-10-21T23:36:30.647197997Z |
| `jul_reload_phase_duration_seconds_sum{outcome="applied_live",phase="prepare"}` | 0.049 | 0.069 | 0.049 | 0.069 | 2026-10-21T23:36:30.647197997Z |
| `jul_reload_phase_duration_seconds_sum{outcome="applied_live",phase="publish"}` | 0.002 | 0.004 | 0.002 | 0.004 | 2026-10-21T23:36:30.647197997Z |
| `jul_reload_phase_duration_seconds_sum{outcome="applied_live",phase="validate"}` | 0.05 | 0.085 | 0.05 | 0.085 | 2026-10-21T23:36:30.647197997Z |
| `jul_reload_total{outcome="applied_live",source="admin"}` | 5 | 7 | 5 | 7 | 2026-10-21T23:36:30.647197997Z |
| `jul_storage_bytes{category="config",kind="available"}` | 9.9806656512e+11 | 9.98000222208e+11 | 9.98000222208e+11 | 9.9806656512e+11 | 2026-10-21T23:04:30.642763476Z |
| `jul_storage_bytes{category="config_history",kind="available"}` | 9.9806656512e+11 | 9.98000222208e+11 | 9.98000222208e+11 | 9.9806656512e+11 | 2026-10-21T23:04:30.642763476Z |
| `jul_transport_retired_total{mode="graceful"}` | 5 | 7 | 5 | 7 | 2026-10-21T23:36:30.647197997Z |
| `jul_upstream_active_requests{pool="dashboard"}` | 0 | 0 | 0 | 21 | 2026-10-21T23:34:10.644323346Z |
| `jul_upstream_backends_eligible{pool="dashboard"}` | 1 | 1 | 0 | 1 | 2026-10-21T23:04:30.642763476Z |
| `jul_upstream_circuit_state{pool="dashboard",state="available"}` | 1 | 1 | 0 | 1 | 2026-10-21T23:04:30.642763476Z |
| `jul_upstream_circuit_state{pool="dashboard",state="circuit_open"}` | 0 | 0 | 0 | 1 | 2026-10-21T23:38:05.644515761Z |
| `jul_upstream_circuit_transitions_total{pool="dashboard",to="circuit_half_open"}` | 1 | 6 | 1 | 6 | 2026-10-21T23:39:05.644863707Z |
| `jul_upstream_circuit_transitions_total{pool="dashboard",to="circuit_open"}` | 1 | 6 | 1 | 6 | 2026-10-21T23:38:55.647548442Z |
| `jul_upstream_connections{pool="dashboard"}` | 1 | 0 | 0 | 24 | 2026-10-21T23:33:50.647842022Z |
| `process_cpu_seconds_total` | 1.34 | 1237.25 | 1.34 | 1237.25 | 2026-10-21T23:43:05.648481165Z |
| `process_network_receive_bytes_total` | 4.4881787e+08 | 5.37086889e+08 | 4.4881787e+08 | 5.37086889e+08 | 2026-10-21T23:43:10.644223739Z |
| `process_network_transmit_bytes_total` | 7.16115751e+08 | 2.9237807e+09 | 7.16115751e+08 | 2.9237807e+09 | 2026-10-21T23:43:10.644223739Z |
| `process_open_fds` | 12 | 11 | 11 | 42 | 2026-10-21T23:33:50.647842022Z |
| `process_resident_memory_bytes` | 1.04472576e+08 | 1.08937216e+08 | 8.9772032e+07 | 1.38637312e+08 | 2026-10-21T23:41:05.6474056Z |
| `process_virtual_memory_bytes` | 1.40406784e+09 | 1.406058496e+09 | 1.40406784e+09 | 1.406058496e+09 | 2026-10-21T23:36:30.647197997Z |
