# #745 saturated caller-store HTTP qualification

## Status and scope

This is a repeatable local production-server qualification. Each run starts
the production `server.Server.Run` path with TLS on a real TCP listener and
HTTP/3 on its paired UDP listener, then proxies to a local `httptest.Server`
backend. It measures HTTP/1.1, HTTP/2 negotiated over TLS/ALPN, and HTTP/3
over QUIC. The test runs the Jul server in its Go test process; it does not
launch the standalone CLI binary, exercise H2C, use a remote network, or use a
production upstream.

## Predeclared workload

- **Environment:** Go 1.27.4, Linux ARM64, WSL2 kernel
  `6.18.58.1-microsoft-standard-WSL2`, 12 reported CPUs.
- **Policy:** 4,096 maximum public callers; public per-caller burst 10 at
  1 request/second; protected CIDR class mapped from a trusted forwarded
  address. Shared application/class rates are configured high enough not to
  be the limiting budget.
- **Fill:** 32 HTTP workers admit ten requests for each of 4,096 distinct
  synthetic public addresses, filling the store with depleted caller buckets.
- **Re-drain:** after baseline sampling, the test revisits the callers in reverse
  fill order to consume any tokens that refilled during setup.
- **Baseline:** 200 sequential protected-class requests over each protocol with
  no public overflow flood.
- **Saturation:** 8 concurrent HTTP/1.1 public workers submit unique addresses
  while 500 sequential protected-class requests are measured over each
  protocol. The store remains full: a new caller may be admitted only when a
  fully refilled entry is safely evicted; otherwise it is refused as
  `caller_capacity`. The test records both outcomes.
- **Run count:** five independent test invocations (`-count=5`).
- **Target:** protected-class saturated HTTP p99 at or below 5 ms in every run.
  All protected requests must succeed and the decision snapshot must include
  `caller_capacity` refusals. Race-instrumented runs still exercise all
  protocols and admission behavior but do not enforce this production latency
  target.

## Results

Command:

```bash
JUL_QUOTA_LATENCY_QUALIFICATION=1 GOMAXPROCS=2 GOFLAGS=-p=2 go test \
-tags 'brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf' \
-v -count=5 \
  -run '^TestQuotaProtectedHTTPAdmissionWithSaturatedPublicCallerStore$' ./internal/app
```

### HTTP/1.1 over TLS

| Run | Baseline p50 / p95 / p99 | Saturated p50 / p95 / p99 | Saturated protected throughput | Public capacity refusals |
| --- | --- | --- | ---: | ---: |
| 1 | 223.8 / 502.4 / 740.2 µs | 121.4 / 303.1 / 1,077.9 µs | 6,358 req/s | 1,222 |
| 2 | 232.4 / 561.8 / 810.1 µs | 111.4 / 383.6 / 1,320.8 µs | 6,251 req/s | 1,048 |
| 3 | 209.8 / 496.1 / 767.7 µs | 122.8 / 195.7 / 1,071.4 µs | 6,435 req/s | 1,192 |
| 4 | 225.6 / 495.0 / 978.8 µs | 111.6 / 208.7 / 1,051.5 µs | 6,931 req/s | 1,194 |
| 5 | 194.2 / 421.2 / 569.8 µs | 122.7 / 266.8 / 1,076.7 µs | 6,693 req/s | 1,138 |

### HTTP/2 over TLS (ALPN)

| Run | Baseline p50 / p95 / p99 | Saturated p50 / p95 / p99 | Saturated protected throughput |
| --- | --- | --- | ---: |
| 1 | 377.5 / 644.8 / 792.4 µs | 140.1 / 219.5 / 754.4 µs | 6,215 req/s |
| 2 | 365.4 / 602.3 / 857.5 µs | 122.3 / 438.9 / 1,035.5 µs | 5,846 req/s |
| 3 | 383.3 / 610.1 / 749.5 µs | 120.6 / 299.8 / 906.1 µs | 6,704 req/s |
| 4 | 355.5 / 599.4 / 676.7 µs | 126.7 / 393.4 / 814.9 µs | 6,177 req/s |
| 5 | 367.5 / 597.3 / 809.0 µs | 135.8 / 366.8 / 1,186.5 µs | 5,695 req/s |

### HTTP/3 over QUIC

| Run | Baseline p50 / p95 / p99 | Saturated p50 / p95 / p99 | Saturated protected throughput |
| --- | --- | --- | ---: |
| 1 | 316.3 / 634.1 / 859.0 µs | 198.5 / 488.7 / 1,271.2 µs | 4,051 req/s |
| 2 | 384.0 / 629.7 / 788.5 µs | 174.8 / 306.8 / 1,244.2 µs | 4,595 req/s |
| 3 | 394.2 / 715.0 / 978.2 µs | 176.5 / 413.2 / 1,314.8 µs | 4,590 req/s |
| 4 | 368.4 / 626.7 / 833.0 µs | 184.5 / 395.9 / 1,033.3 µs | 4,549 req/s |
| 5 | 367.0 / 666.8 / 937.5 µs | 180.0 / 845.6 / 1,545.5 µs | 3,906 req/s |

All five runs passed on all three protocols. Saturated p99 ranged from
1,051.5 to 1,320.8 µs for HTTP/1.1, 754.4 to 1,186.5 µs for HTTP/2, and
1,033.3 to 1,545.5 µs for HTTP/3. Every value is below the predeclared 5 ms
target. Every protected request succeeded, every new public caller was refused
as `caller_capacity` (safe evictions: zero), and the flood recorded 1,048–1,222
capacity refusals per measured interval.

## Limits

The measured envelope is limited to the production Jul server runtime on
localhost, TLS H1/H2, QUIC H3, the local backend fixture, and this Go/WSL2
environment. It does not establish standalone CLI startup, H2C, remote-network,
upstream-saturation, CPU/allocation, or deployment performance. Keep those
protocol and deployment qualifications explicit on #745.
