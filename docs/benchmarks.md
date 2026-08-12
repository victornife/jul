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
| `internal/clientaddr` | `BenchmarkDeriveDirect` | Canonical client derivation with no trusted proxy | Per-request cost on a directly exposed listener (~9 ns/op, 0 allocs — headers are never read) |
| `internal/clientaddr` | `BenchmarkDeriveProxied` | Derivation of a two-hop `X-Forwarded-For` chain from a trusted peer | Per-request cost behind a proxy (~265 ns/op, 2 allocs) |
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

Jul.IA reuses upstream connections by default through Go's `http.Transport`
keep-alive. The exact idle-connection limits and timeouts are internal Go
transport settings; they are **not exposed in the TOML schema** because the
current `GlobalConfig` contract is intentionally small. If you observe
connection exhaustion or high `TIME_WAIT`, the supported levers are:

1. **Reduce per-location `proxy_connect_timeout`** so failed dials fail fast.
2. **Add more upstream backends** and use `least_conn` to spread long-lived
   connections.
3. **Raise OS file-descriptor limits** (`ulimit -n`) so the transport can keep
   more idle sockets.

If a config knob for idle connections becomes necessary, it will be added
through the normal ADR/config-lifecycle process rather than documented as an
unsupported key.

### Worker / thread pool sizing

Jul.IA uses Go's default goroutine-per-request model. There is no fixed worker pool to tune. Two levers matter:

1. **`GOMAXPROCS`** — set to the number of physical CPU cores (or vCPUs) available. The Go scheduler handles the rest.
2. **File descriptor limits** — on Linux, ensure `ulimit -n` covers `(listeners + upstream_backends) × expected_parallelism`. For 10k concurrent connections, `ulimit -n 65536` is typical.

### Response cache recertification baseline

The post-#134 cache baseline was measured on GitHub-hosted Ubuntu 24.04,
Go 1.26.5, linux/amd64, AMD EPYC 7763, using five fixed 100-iteration samples.
Median results: fresh hit 2,071 ns/op; miss/store 10,321 ns/op; Vary hit
2,777 ns/op; crash-safe memory-to-disk overflow 751,820 ns/op; mandatory
304 validation 7,703 ns/op; and 32-variant invalidation 5,728 ns/op. These are
machine-specific correctness-path baselines, not deployment SLOs. Full
allocation data and command are in [cache.md](cache.md#benchmarks).

### Response cache sizing

```toml
[cache]
enabled          = true
memory_max_size  = "64m"
default_ttl      = "60s"
stale_while_revalidate = "30s"
stale_if_error   = "5s"

[[servers]]
listen = "127.0.0.1:8080"

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  proxy_pass = "http://127.0.0.1:3000"
  cache = true
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

[[servers]]
listen = "127.0.0.1:8080"

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  proxy_pass = "http://127.0.0.1:3000"
  cache = true
```

**Rule of thumb:** Size the memory cache to hold your hottest 20% of responses. Disk cache is for larger working sets where memory is too expensive.

**Key design:** The cache key is `(method, Host, path, Vary headers)`. Collisions are rare unless you have many distinct `Vary` values (e.g. per-user caching without a stable surrogate key).

### Compression levels

Jul.IA supports `gzip` (always-on), `brotli`, and `zstd` (tag-gated). The
`[compression]` schema uses a single `level` integer that is clamped to each
encoder's valid range; `0` selects each encoder's default.

| Encoder | Default level | Valid range | Tune when … |
| --- | --- | --- | --- |
| gzip | 6 | 1–9 | High CPU → lower to 4; poor ratio → raise to 8 |
| brotli | 6 | 0–11 | Same logic; Brotli-4 ≈ gzip-9 at lower CPU |
| zstd | 3 | 1–4 (mapped to Speed* levels) | Very fast; raise for better ratio |

```toml
[compression]
enabled   = true
encoders  = ["zstd", "br", "gzip"]
level     = 4          # 0 = encoder default; clamped per encoder
min_size  = "1k"
types     = ["text/*", "application/json", "application/javascript", "application/xml", "application/wasm", "image/svg+xml"]
precompressed = true

[[servers]]
listen = "127.0.0.1:8080"

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  proxy_pass = "http://127.0.0.1:3000"
```

**Trade-off:** Higher level → smaller bytes → lower bandwidth but more CPU per
request. On CPU-bound edges, lowering the level often increases total
throughput. See [docs/compression.md](compression.md) for the full behaviour
matrix and build-tag requirements.

### Rate limiter tuning

The rate limiter uses a 32-shard token bucket. Each unique key (IP, header,
JWT claim) gets its own bucket. `rate` is an integer number of requests per
second.

```toml
[rate_limit]
enabled   = true
key       = "ip"
rate      = 100        # requests per second per key
burst     = 200
max_conns = 1024       # concurrent connections per listener

[[servers]]
listen = "127.0.0.1:8080"

  [[servers.locations]]
  match = { type = "prefix", path = "/api" }
  proxy_pass = "http://127.0.0.1:3000"

    [servers.locations.rate_limit]
    enabled = true
    key     = "header:X-Api-Key"
    rate    = 10
    burst   = 20
```

**Trade-off:** Higher `burst` absorbs traffic spikes but allows larger bursts
through. Lower `burst` is stricter but may reject legitimate staggered
requests. A global rate of `100` r/s is conservative; production APIs often use
`1000` r/s or more. See [docs/ratelimit.md](ratelimit.md) for scope rules and
key strategies.

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

## Lean product budgets

Jul.IA's "lean" promise is enforced as a set of recorded budgets. The numbers
below are the current baselines; they are updated when a deliberate change
moves them. Deterministic checks (binary size, generated-asset size) may gate
CI; shared-runner latency and RSS remain evidence artifacts until stable
runners exist.

### Binary size

Measured on `windows/amd64` with `go1.26.5`, stripped Go binary only:

| Profile | Size | Notes |
| --- | --- | --- |
| Lean (no tags) | ~21 MiB | Core HTTP, TLS, auth, rate limiting, cache, compression (gzip), health checks, discovery (DNS/SRV), secrets, egress |
| Full (all tags) | ~51 MiB | Lean + ACME, console, OTel, gRPC, HTTP/3, NGINX importer, WASM plugins, stream proxy, Consul/Kubernetes discovery, WAF, Brotli, Zstd |

Representative per-tag deltas over the lean binary:

| Tag | Delta | Main contributor |
| --- | --- | --- |
| `waf` | ~8.5 MiB | Coraza + embedded OWASP CRS |
| `otel` | ~5.8 MiB | OpenTelemetry SDK/exporters |
| `console` | ~6.3 MiB | Embedded React SPA assets |
| `wasmplugins` | ~4.1 MiB | wazero runtime |
| `grpc` | ~4.0 MiB | gRPC + protobuf stack |
| `http3` | ~2.4 MiB | quic-go |
| `brotli` | ~1.0 MiB | brotli encoder |
| `acme` | ~0.7 MiB | autocert / x/crypto |
| `zstd` | ~0.4 MiB | zstd encoder |
| `importer` | ~0.3 MiB | NGINX config parser |
| `stream` | ~0.1 MiB | L4 proxy runtime |
| `consul` / `kubernetes` | ~0.02 MiB each | Small REST clients (no vendor SDKs) |

**Gate:** a +10% change to lean or full size requires explanation in the
change notes; a +20% deterministic regression fails CI unless deliberately
accepted in the same change.

### Runtime footprint

Measured on `windows/amd64`, idle process with a minimal HTTP + admin config:

| Metric | Lean | Full | Notes |
| --- | --- | --- | --- |
| Startup to `/healthz` | ~520 ms | ~520 ms | Wall-clock on a local VM; dominated by binary load |
| Idle RSS | ~27 MiB | ~33 MiB | Working set before any traffic |
| `jul check` (warm) p50 | ~62 ms | ~75 ms | Parse + validate a small config |
| `jul check` (warm) p95 | ~69 ms | ~85 ms | — |

These are **evidence artifacts** on shared runners; they are recorded for
trending, not gating, until stable CI hardware exists.

### Hot-path allocations

Stable, allocation-free or near-allocation-free hot paths (measured with
`go test -benchmem`):

| Path | Time/op | Allocs/op | Notes |
| --- | --- | --- | --- |
| `BenchmarkMatchLocation/prefix` | ~39 ns | 0 | Most common location match |
| `BenchmarkHostScore/exact` | ~106 ns | 0 | Virtual-host selection |
| `BenchmarkBalancerRoundRobin` | ~14 ns | 0 | Backend rotation |
| `BenchmarkBalancerLeastConn` | ~16 ns | 0 | Backend selection |
| `BenchmarkPoolPick` | ~89 ns | 1 (32 B) | Full pick including health filter |
| `BenchmarkRateLimiterAllow` | ~300 ns | 1 | Token-bucket admission |

### Console asset budget

The Console v2 SPA is embedded in the binary. The initial route budget is
**~250 KB gzip** ([ADR 0006](adr/0006-console-v2-stack.md)). Current initial
route (HTML shell + entry JS/CSS + runtime + vendor) is **~168 KB gzip**,
leaving headroom for new panels.

### Dependency and license budget

New packages are reviewed for:

- License compatibility with AGPL-3.0.
- Whether the same capability can be achieved with the stdlib or an existing
dependency.
- Build-tag gating so the lean binary is unaffected when the feature is
optional.

Every optional feature records its approximate binary delta above so the cost
of a new dependency is visible before it is merged.

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
