# Performance Tuning & Benchmarks

> **Goal:** Understand Jul.IA's performance characteristics, run the benchmark harness, and tune production deployments.

## Running benchmarks

The in-tree benchmark suite lives across 8 packages and is driven by `scripts/bench.sh`:

```bash
# Smoke run (fast, verifies compilation + execution)
make bench

# Measurement run with stable timing
BENCHTIME=2s scripts/bench.sh

# One package, multiple samples for stable numbers
COUNT=6 scripts/bench.sh ./internal/router/

# Full opt-in feature set (includes gRPC, mTLS, HTTP/3, transcoding)
BENCH_TAGS="brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes" scripts/bench.sh
```

Each benchmark is named `Benchmark<Subsystem><Scenario>`. They are end-to-end where possible (real handler, real TLS handshake, real balancer) rather than micro-benchmarks of isolated functions, so the numbers reflect production costs.

## Benchmark catalogue

| Package | Benchmark | What it measures | Typical use |
| --- | --- | --- | --- |
| `internal/auth` | `BenchmarkBasicVerify` | bcrypt cost of a Basic auth check | Tune `bcrypt_cost` vs login latency |
| `internal/auth` | `BenchmarkJWTValidate` | RS256 signature verification (cached JWKS) | JWT overhead per request |
| `internal/handler` | `BenchmarkStaticServe` | End-to-end static file serve (open, MIME, ETag, `ServeContent`) | Static asset throughput baseline |
| `internal/handler` | `BenchmarkGRPCPassthroughUnary` | Unary gRPC call forwarded end-to-end | gRPC proxy overhead vs direct call |
| `internal/router` | `BenchmarkHostScore/exact` | Exact `Host` header match | VHost routing hot path |
| `internal/router` | `BenchmarkMatchLocation/prefix` | Longest-prefix location match | Most common location type |
| `internal/router` | `BenchmarkMatchLocation/regex` | Regex location match | Regex routing cost |
| `internal/upstream` | `BenchmarkBalancerRoundRobin` | Lock-free rotation across backends | Balancer micro-overhead |
| `internal/upstream` | `BenchmarkPoolPick` | Full hot path: health filter + balancer | Per-request backend selection |
| `internal/server` | `BenchmarkSNICertSelection` | SNI cert lookup per TLS handshake | Large cert catalogue performance |
| `internal/server` | `BenchmarkTLSHandshakeServerAuth` | TLS 1.3 handshake baseline | TLS termination capacity |
| `internal/server` | `BenchmarkMTLSHandshake` | Full mutual-TLS handshake cost | mTLS overhead per connection |
| `internal/server` | `BenchmarkHTTP3Throughput` | Raw request throughput over QUIC | HTTP/3 vs HTTP/2 comparison |
| `internal/transcode` | `BenchmarkTranscodeUnaryPostBody` | REST → gRPC transcoding (POST body) | Transcoding overhead |
| `internal/stream` | `BenchmarkUDPAdmitAtCap` | UDP session admission decision | L4 UDP table lookup |

## Reading benchmark output

A typical line:

```
BenchmarkStaticServe-12     123456    9876 ns/op    456 B/op    7 allocs/op
```

| Column | Meaning |
| --- | --- |
| `BenchmarkStaticServe-12` | Name + GOMAXPROCS (12 vCPU) |
| `123456` | Iterations run (determined by `b.N`) |
| `9876 ns/op` | Nanoseconds per iteration |
| `456 B/op` | Bytes allocated per iteration (heap pressure) |
| `7 allocs/op` | Heap allocations per iteration (GC pressure) |

**Allocs/op is usually more important than ns/op** for sustained throughput: fewer allocations mean less GC work and steadier latency percentiles.

## Performance tuning recommendations

### Upstream connection pooling

Jul.IA reuses upstream connections by default (Go `http.Transport` keep-alive). Tune when you see connection exhaustion or high `TIME_WAIT`:

```toml
[global]
# Max simultaneous idle connections per upstream host.
# Default is 100. Raise for high-throughput proxy workloads; lower to reduce
# memory when upstream count is large.
max_idle_conns = 200

# Idle connection timeout. Connections idle longer than this are closed.
# Default is 90s. Match your upstream's keep-alive timeout to avoid racing
# a close against a new request.
idle_conn_timeout = "60s"
```

**Trade-off:** More idle conns → lower latency (no handshake) but higher memory per upstream host.

### Worker / thread pool sizing

Jul.IA uses Go's default goroutine-per-request model. There is no fixed worker pool to tune. Two levers matter:

1. **`GOMAXPROCS`** — set to the number of physical CPU cores (or vCPUs) available. The Go scheduler handles the rest.
2. **File descriptor limits** — on Linux, ensure `ulimit -n` covers `(listeners + upstream_backends) × expected_parallelism`. For 10k concurrent connections, `ulimit -n 65536` is typical.

### Response cache sizing

```toml
[cache]
enabled          = true
memory_max_size  = "64m"
default_ttl      = "60s"
stale_while_revalidate = "30s"
stale_if_error   = "5s"
```

Or use a disk tier for larger working sets:

```toml
[cache]
enabled          = true
memory_max_size  = "64m"
disk_path        = "/var/cache/jul"
disk_max_size    = "1g"
default_ttl      = "60s"
stale_while_revalidate = "30s"
stale_if_error   = "5s"
```

**Rule of thumb:** Size the memory cache to hold your hottest 20% of responses. Disk cache is for larger working sets where memory is too expensive.

**Key design:** The cache key is `(method, Host, path, Vary headers)`. Collisions are rare unless you have many distinct `Vary` values (e.g. per-user caching without a stable surrogate key).

### Compression levels

Jul.IA supports `gzip` (always-on), `brotli`, and `zstd` (tag-gated). Default levels balance CPU and ratio:

| Encoder | Default level | Tune when … |
| --- | --- | --- |
| gzip | 6 | High CPU → lower to 4; poor ratio → raise to 8 |
| brotli | 4 | Same logic; Brotli-4 ≈ gzip-9 at lower CPU |
| zstd | 3 | Very fast; raise to 6 for better ratio |

```toml
[compression]
gzip_level  = 6
brotli_level = 4
```

**Trade-off:** Higher level → smaller bytes → lower bandwidth but more CPU per request. On CPU-bound edges, lowering the level often increases total throughput.

### Rate limiter tuning

The rate limiter uses a 32-shard token bucket. Each unique key (IP, header, JWT claim) gets its own bucket.

```toml
[[servers.locations.rate_limit]]
key   = "ip"
rate  = "100r/m"
burst = 20
```

**Trade-off:** Higher `burst` absorbs traffic spikes but allows larger bursts through. Lower `burst` is stricter but may reject legitimate staggered requests. The default `rate = "100r/m"` is conservative; production APIs often use `1000r/m` or `10r/s`.

### TLS / mTLS handshake cost

TLS 1.3 handshakes are the dominant connection cost. Two mitigations:

1. **Session resumption** — TLS 1.3 0-RTT and session tickets are enabled by default. Ensure `tls_config` does not disable them.
2. **Certificate caching** — Jul.IA caches parsed certificates. For very large cert catalogues (1000+ SNI entries), ensure the cache is warm before taking traffic (the first access parses the PEM).

### HTTP/3 over QUIC

HTTP/3 reduces head-of-line blocking and improves lossy-network latency, but QUIC handshakes and encryption add per-packet CPU overhead. Use it when:

- Clients are mobile / over lossy networks.
- You can absorb ~10–20% more CPU for the same throughput.

Benchmark `BenchmarkHTTP3Throughput` against your TCP baseline (`BenchmarkStaticServe` or `BenchmarkPoolPick`) to quantify the delta on your hardware.

### Static file serving

The static handler uses `os.Root` (openat) for path traversal safety, then `http.ServeContent` with `If-Modified-Since` / ETag handling. For highest throughput:

1. Use an in-memory or disk cache for static responses (avoids disk read on cache hit).
2. Enable `sendfile` implicitly: Jul.IA uses `http.ServeContent`, which elides copies when possible.
3. Pre-compress assets offline and serve the `.gz` / `.br` variants if your build pipeline supports it (avoids runtime compression CPU).

### Logging overhead

Structured logging (`json` or `text`) has a small per-request cost. At extreme throughput (>50k req/s), consider:

- Setting `log_level = "warn"` to skip `info`-level lines.
- Using an asynchronous access-log sink (`syslog` or `file` with buffering) rather than `stdout`.

## Interpreting results for capacity planning

1. **Find your bottleneck benchmark.** If `BenchmarkJWTValidate` dominates, JWT verification is your ceiling. If `BenchmarkPoolPick` dominates, backend selection or health-check frequency is the limit.
2. **Profile in production.** Build with `pprof` and collect CPU/memory profiles under load:
   ```bash
   curl http://127.0.0.1:9090/debug/pprof/profile?seconds=30 > cpu.prof
   ```
3. **Correlate with metrics.** `jul_http_request_duration_seconds` and `jul_upstream_probe_duration_seconds` show real-world latency. Benchmarks show ideal-world overhead; the gap is usually I/O wait, lock contention, or GC.

## CI integration

The benchmark smoke gate runs in CI (`.github/workflows/ci.yml` `benchmarks` job). It verifies every benchmark compiles and executes without panicking. It does **not** gate on nanosecond numbers — shared CI runners are too noisy for reliable regression detection. Use the smoke gate to catch compilation rot; use dedicated hardware for regression tracking.

## See also

- `scripts/bench.sh` — harness source and environment overrides
- `docs/core-http.md` — request lifecycle and middleware ordering
- `docs/cache.md` — cache semantics and sizing
- `docs/observability.md` — metrics reference
- `docs/tls-acme.md` — TLS configuration and certificate management
