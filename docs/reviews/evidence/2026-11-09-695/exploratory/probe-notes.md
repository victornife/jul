# Exploratory probe notes (2026-11-09, before plan freeze)

Runtime `453e62375bdce0302756ff386637832d5fa35bad`, full profile, `GOMAXPROCS=2`,
`taskset -c 0,1`, **no memory cgroup** (diagnostic only), config copied from
`explore1/s1-normal-h1/waf/r1/jul.toml` (CRS block mode, `request_body_limit = "1mb"`).
Origin: `harness origin` on CPUs 10–11. These are investigation leads, not final results.

## P1 — RSS and post-GC heap versus concurrency (16,357-byte, 247-item JSON, 64 KiB HTML response)

Driver: closed loop, 6 s per step, `Host: localhost`.

| Step | Completed | Throughput | VmRSS after step |
| --- | ---: | ---: | ---: |
| idle baseline | – | – | 98 MiB |
| C=1 | 27 | 4/s | 270 MiB |
| C=4 | 49 | 8/s | 535 MiB |
| C=16 | 49 | 8/s | 1,301 MiB |

`heap?gc=1` immediately after C=16: `heap_inuse` 744 MiB; 93% (630 MB) allocated by
`coraza/v3/internal/corazawaf.(*Rule).transformArg` under `ProcessRequestBody`.
After a second and third forced GC: `heap_inuse` 56 MiB (RSS released lazily: 1,049 → 297 MiB
within ~5 s). Repeating C=1 three times: 97 → 138 → 139 MiB; C=16 again: 711 MiB then 57 MiB after
the next GC. Interpretation: no monotonic leak; pooled Coraza transactions (`sync.Pool`)
retain their last phase's transformation cache until victim eviction, and the in-flight
working set scales with concurrent many-argument transactions.

## P2 — allocation per request by body shape (`go_memstats_alloc_bytes_total` delta / 10 requests, C=1)

| Body | Bytes | ≈ARGS_POST | alloc/request | single-request latency |
| --- | ---: | ---: | ---: | ---: |
| tiny | 20 | 1 | 0.6 MiB | 12 ms |
| 8 × 2,000-char strings | 16,059 | 9 | 1.2 MiB | 69 ms |
| 60 items | 4,266 | 181 | 9.2 MiB | 59 ms |
| 247 items | 16,357 | 742 | 57.8 MiB | 196 ms |
| 125 items (single request) | 8,931 | 376 | 11 MiB | 109 ms |
| 500 items (single request) | 36,306 | 1,501 | 26 MiB | 284 ms |
| 1000 items (single request) | 72,806 | 3,001 | 27 MiB | 307 ms |

## P3 — partial inspection beyond 1000 arguments (block mode, CRS)

| Items (≈args) | XSS position | Status |
| --- | --- | ---: |
| 100 (301) | first | 403 |
| 100 (301) | last | 403 |
| 400 (1,201) | first | 403 |
| 400 (1,201) | last | **200** |

With `SecRule ARGUMENTS_LIMIT_REACHED "@eq 1" "id:1695190,phase:2,deny,status:413,log"` added to
`inline_rules`: 300 items → 200; 334 items (1,003 args) → 413; 400 items → 413.
Now pinned by `TestAssessmentArgumentLimitPartialInspection` (`internal/waf/resource_assessment_test.go`).
