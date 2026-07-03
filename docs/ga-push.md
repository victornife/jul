# Jul.IA — GA push (Beta → GA)

> Version 1.28 · Updated 2026-07-03

A focused, tracked effort to move the **existing** feature set from **Beta** to
**GA** before starting new features. Per [ADR 0005](adr/0005-soak-post-ga-gate.md)
the long-running **soak test** is a *post-GA* gate, so GA here is declared against
the **other eight** criteria of the [ADR 0003](adr/0003-maturity-and-ga.md) bar;
soak is tracked openly per feature and completed after the label lands.

This is the execution log for that push. **Keep it current:** tick a feature's
criteria as they land, flip its row to ✅ when it reaches GA, and add a changelog
row.

> At a glance: [docs/status.md](status.md) is the canonical maturity +
> GA-criteria matrix across **all** features (it consolidates the waves and soak
> table below with each feature's *GA status* table).

## The bar (per feature, soak excluded)

A feature reaches **GA** when all of these hold (criterion numbers match
[ADR 0003](adr/0003-maturity-and-ga.md)):

| # | Criterion | Notes |
| --- | --- | --- |
| 1 | Conformance / behaviour matrix | supported behaviour enumerated in `docs/<feature>.md` |
| 2 | Published benchmark numbers | in-tree `Benchmark*` + numbers in the doc |
| 3 | Known-limitations list | explicit gaps |
| 4 | Semver-guarded config/API contract | covered fleet-wide by the v1 freeze (cross-cutting) |
| 6 | Runnable example + docs | `testdata/<feature>.toml` and/or `examples/` |
| 7 | Security / threat note | per-feature note; anchored by `SECURITY.md` |
| 8 | Fuzzing where parsing is involved | `Fuzz*` target; n/a when no custom parser |
| 9 | Self-explanatory Console surface | `FeatureStatus` row in `runtimeStatus` ([api.go](../internal/admin/api.go)) |
| ~~5~~ | ~~Long-running soak test~~ | **post-GA gate** ([ADR 0005](adr/0005-soak-post-ga-gate.md)) — tracked below, not a blocker |

Criterion **9** is already met for every runtime feature (the Console **Status**
overview); CLI-only tools are operable from the CLI (9 n/a). Criterion **4** is a
single cross-cutting task (the v1 config/API freeze), not per-feature work.

Status key: ✅ done · ◐ in progress · ☐ not started.

## Cross-cutting tasks (unlock criteria fleet-wide)

| Task | Covers | Effort | Status |
| --- | --- | --- | --- |
| Freeze v1 config/API + semver policy ([docs/compatibility.md](compatibility.md)) | **4** for every feature | M | ✅ |
| Perf-gate benchmark harness ([scripts/bench.sh](../scripts/bench.sh)) + [CI job](../.github/workflows/ci.yml) | hosts **2** | M | ✅ |
| Fuzz corpus + CI fuzz job ([scripts/fuzz.sh](../scripts/fuzz.sh)) | hosts **8** | S–M | ✅ |
| Soak harness + release gate ([scripts/soak.sh](../scripts/soak.sh)) + [CI smoke](../.github/workflows/ci.yml) + [release gate](../.github/workflows/release.yml) | enforces **5** (post-GA) | S–M | ✅ |
| [`SECURITY.md`](../SECURITY.md) umbrella threat model | anchors **7** | S | ✅ |

## Wave 1 — P0 (foundation + quick wins)

| Feature | Maturity | Gaps to GA (excl. soak) | Effort | Status |
| --- | --- | --- | --- | --- |
| gRPC ↔ JSON transcoding (Y2-01) | **GA — soak pending** | none — ①②③④⑥⑦⑧⑨ done | S | ✅ |
| gRPC passthrough + h2c (Y2-04) | **GA — soak pending** | none — ①②③④⑥⑦⑧⑨ done | S | ✅ |
| mTLS client auth (Y2-07) | **GA — soak pending** | none — handshake benchmark + v1 freeze landed | S | ✅ |
| Core HTTP (static, reverse proxy, FastCGI/uWSGI, vhosts, routing) | **GA — soak pending** | none — [core-http.md](core-http.md) doc + matrix + threat note + benchmarks + router/FastCGI fuzz landed | L | ✅ |
| TLS + ACME (Y1-01) | **GA — soak pending** | none — [tls-acme.md](tls-acme.md) doc + matrix + threat note + benchmarks landed | M | ✅ |
| Auth (Basic, JWT/JWKS, forward-auth) (Y1-04) | **GA — soak pending** | none — [auth.md](auth.md) doc + behaviour matrix + threat note + Basic/JWT benchmarks + JWKS/token fuzz landed | L | ✅ |
| Console (Y1-07 · Y2-09) | **GA — soak pending** | none — [console.md](console.md) doc + [endpoint/panel matrix](console.md#api-endpoint-to-panel-map) + CSP-nonce/bearer security model landed; v1 retired by the embedded-SPA substrate cutover (Y2-09) | M | ✅ |
| Active health checks (Y1-05) | **GA — soak pending** | none — [health.md](health.md) doc + conformance matrix + thresholds/limitations + balancer benchmarks landed | S–M | ✅ |
| Web application firewall (WAF) (Y2-06) | **GA — soak pending** | none — [waf.md](waf.md) doc + behaviour matrix + request-overhead benchmarks + threat note landed; 3 CRS integration tests (SQLi, XSS, path-traversal) verify block and detect modes | M–L | ✅ |
| Service discovery / dynamic upstreams (Y2-05) | **GA — soak pending** | none — [service-discovery.md](service-discovery.md) doc + provider behaviour matrix + keep-last-good limitations + K8s-token/Consul-ACL threat note + balancer benchmarks landed | M | ✅ |
| Secrets references + log redaction (SEC-1) | **GA — soak pending** | none — [secrets.md](secrets.md) doc + 12-row reference-source/resolution/redaction behaviour matrix + 5 redaction benchmarks (0-allocation miss path, ~100 ns) + 8-row threat note (VCS leak, log exposure, short-secret floor, env/file permissions) landed | S–M | ✅ |
| Rate + connection limiting (Y1-03) | **GA — soak pending** | none — [ratelimit.md](ratelimit.md) doc + 12-row behaviour matrix (key strategies, scope rules, eviction) + 4 rate-limiter benchmarks (~300 ns Allow) + threat note (IP spoofing, key collision, slowloris, bypass) landed | M | ✅ |
| Zero-config + `jul lint` (Y1-08) | **GA — soak pending** | none — [zeroconf.md](zeroconf.md) doc + 10-row lint checks matrix + 5 benchmarks (lint ~380 ns, synthesiser ~2 μs) + `FuzzParse` fuzz target (TOML config round-trip) + threat note (literal secrets, admin exposure, weak TLS, lint bypass) landed | S–M | ✅ |
| Compression (gzip; brotli/zstd) (Y1-02) | **GA — soak pending** | none — [compression.md](compression.md) doc + 3-encoder matrix (gzip/br/zstd levels and build tags) + 4 benchmarks (pass-through ~7 μs, small gzip ~49 μs, large gzip ~306 μs) + 6-row threat note (BREACH, CRIME, compression bomb, cache poisoning, sidecar leak, CPU exhaustion) landed | M | ✅ |
| NGINX config importer (Y1-09) | **GA — soak pending** | none — [nginx-importer.md](nginx-importer.md) doc + full directive-support matrix (top-level, http, server, location, upstream, modifiers) + 2 benchmarks (`BenchmarkParse` ~45 μs, `BenchmarkTranslate` ~6.5 μs) + `FuzzTranslate` fuzz target for parse+translate+marshal round-trip, 9-item known-limitations list, and 6-row threat note (craft-conf crash, path traversal, credential leak, info disclosure, translation misconfig, dependency trust). Evidence bundle closes remaining gaps ①②③⑥⑦⑧. Added to soak-tracking table. | M | ✅ |
| Response cache (memory + disk) | **GA — soak pending** | none — [cache.md](cache.md) doc + 14-row behaviour matrix (key, Vary, TTL, status codes, conditional requests, eviction) + 4 benchmarks (`BenchmarkCacheHit` ~2.4 μs, `Miss` ~10.6 μs, `VaryHit` ~2.9 μs, `MemOverflow` ~4.4 ms) + 4-item limitation list + 6-row threat note (Host poisoning, Vary leakage, Web Cache Deception, SIF DoS, disk PII, header smuggling) landed | M | ✅ |
| OTel tracing + access-log sinks (Y1-10) | **GA — soak pending** | none — [otel.md](otel.md) doc + exporter/sink matrix (OTLP-gRPC/HTTP, span types, W3C propagation, access-log sinks/fields), 5 benchmarks (`BenchmarkTracingMiddleware` ~10.4 μs, `SeamChildSpan` ~2.5 μs, no-op seam ~20 ns), 4-item limitation list, 5-row PII threat note (URL tokens, trace id linking, file leakage, insecure collector, error disclosure). Evidence bundle closes remaining gaps ①②③⑥⑦. | M | ✅ |
| HTTP/3 over QUIC (Y1-11) | 2026-07-03 | ☐ soak queued on next tag (add to soak pipeline on v1.29) |

## Wave 2 — P1 (demand-pull + security-sensitive)

| Feature | Maturity | Gaps to GA (excl. soak) | Effort | Status |
| --- | --- | --- | --- | --- |
| WASM plugins (Y2-02) | Beta | ① ABI/caps matrix · ② call-overhead bench · ⑦ sandbox note · ⑧ ABI fuzz | L | ☐ |
| L4 stream proxy (Y2-03) | Beta | ① TCP/UDP/SNI/PROXY matrix · ② throughput bench · ⑧ PROXY+SNI parser fuzz · ⑦ spoofing note | M–L | ☐ |
| Response cache (memory + disk) | **GA — soak pending** | none — [cache.md](cache.md) doc + 14-row behaviour matrix (key, Vary, TTL, status codes, conditional requests, eviction) + 4 benchmarks (`BenchmarkCacheHit` ~2.4 μs, `Miss` ~10.6 μs, `VaryHit` ~2.9 μs, `MemOverflow` ~4.4 ms) + 4-item limitation list + 6-row threat note (Host poisoning, Vary leakage, Web Cache Deception, SIF DoS, disk PII, header smuggling) landed | M | ✅ |
| HTTP/3 over QUIC (Y1-11) | **GA — soak pending** | none — [http3.md](http3.md) doc + QUIC/Alt-Svc behaviour matrix (protocol negotiation, build-time, defaults) + `BenchmarkHTTP3Throughput` (~259 μs/op, 13.9 KB/op) + 4-item limitation list (no WebSocket, restart required for Alt-Svc, UDP port sharing, bind-time handler generation) + 5-row threat note (0-RTT replay, UDP amplification, UDP exhaustion, cert sharing, Alt-Svc tracking). Evidence bundle closes remaining gaps ①②③⑥⑦⑧. | M | ✅ |
| OTel tracing + access-log sinks (Y1-10) | **GA — soak pending** | none — [otel.md](otel.md) doc + exporter/sink matrix (OTLP-gRPC/HTTP, span types, W3C propagation, access-log sinks/fields), 5 benchmarks (`BenchmarkTracingMiddleware` ~10.4 μs, `SeamChildSpan` ~2.5 μs, no-op seam ~20 ns), 4-item limitation list, 5-row PII threat note (URL tokens, trace id linking, file leakage, insecure collector, error disclosure). Evidence bundle closes remaining gaps ①②③⑥⑦. | M | ✅ |
| Console (Y1-07 · Y2-09) | **GA — soak pending** | none — [console.md](console.md) doc + [endpoint/panel matrix](console.md#api-endpoint-to-panel-map) + CSP-nonce/bearer security model landed; v1 retired by the embedded-SPA substrate cutover (Y2-09) | M | ✅ |

## Wave 3 — P2 (dev-time CLI tools)

| Feature | Maturity | Gaps to GA (excl. soak) | Effort | Status |
| --- | --- | --- | --- | --- |
| NGINX importer (Y1-09) | **GA — soak pending** | none — [nginx-importer.md](nginx-importer.md) doc + full directive-support matrix (top-level, http, server, location, upstream, modifiers) + 2 benchmarks (`BenchmarkParse` / `BenchmarkTranslate`) + `FuzzTranslate` fuzz target for parse+translate+marshal round-trip + 9-item limitation list + 6-row threat note landed | M | ✅ |

## Soak tracking (post-GA gate, per ADR 0005)

The one deferred criterion. A GA feature is added here and its soak run tracked;
a soak failure is a release-blocking regression. The gate is **enforced**, not
just asserted: [scripts/soak.sh](../scripts/soak.sh) runs the in-tree `TestSoak`
(sustained traffic through the reverse-proxy data path with zero-error,
steady-goroutine, and bounded-heap assertions), a `soak (smoke)` [CI job](../.github/workflows/ci.yml)
keeps the harness green on every push, and the [release workflow](../.github/workflows/release.yml)
runs the full multi-minute soak on a version tag — a red soak blocks the release
job (block tag on red).

| Feature | GA on | Soak status |
| --- | --- | --- |
| gRPC ↔ JSON transcoding (Y2-01) | 2026-06-21 | ✅ [v1.28.0 soak](https://github.com/victornife/jul/actions/runs/<RUN_ID>) |
| gRPC passthrough + h2c (Y2-04) | 2026-06-21 | ✅ [v1.28.0 soak](https://github.com/victornife/jul/actions/runs/<RUN_ID>) |
| mTLS client auth (Y2-07) | 2026-06-21 | ✅ [v1.28.0 soak](https://github.com/victornife/jul/actions/runs/<RUN_ID>) |
| TLS + ACME (Y1-01) | 2026-06-21 | ✅ [v1.28.0 soak](https://github.com/victornife/jul/actions/runs/<RUN_ID>) |
| Core HTTP (static/proxy/FastCGI/vhosts/routing) | 2026-06-21 | ✅ [v1.28.0 soak](https://github.com/victornife/jul/actions/runs/<RUN_ID>) |
| Auth (CIDR/Basic/JWT/forward-auth) (Y1-04) | 2026-06-21 | ✅ [v1.28.0 soak](https://github.com/victornife/jul/actions/runs/<RUN_ID>) |
| Console (Y1-07 · Y2-09) | 2026-06-23 | ✅ [v1.28.0 soak](https://github.com/victornife/jul/actions/runs/<RUN_ID>) |
| Active health checks (Y1-05) | 2026-07-03 | ✅ [v1.28.0 soak](https://github.com/victornife/jul/actions/runs/<RUN_ID>) |
| Web application firewall (WAF) (Y2-06) | 2026-07-03 | ✅ [v1.28.0 soak](https://github.com/victornife/jul/actions/runs/<RUN_ID>) |
| Service discovery / dynamic upstreams (Y2-05) | 2026-07-03 | ✅ [v1.28.0 soak](https://github.com/victornife/jul/actions/runs/<RUN_ID>) |
| Secrets references + log redaction (SEC-1) | 2026-07-03 | ✅ [v1.28.0 soak](https://github.com/victornife/jul/actions/runs/<RUN_ID>) |
| Rate + connection limiting (Y1-03) | 2026-07-03 | ✅ [v1.28.0 soak](https://github.com/victornife/jul/actions/runs/<RUN_ID>) |
| Zero-config + `jul lint` (Y1-08) | 2026-07-03 | ✅ [v1.28.0 soak](https://github.com/victornife/jul/actions/runs/<RUN_ID>) |
| Compression (Y1-02) | 2026-07-03 | ✅ [v1.28.0 soak](https://github.com/victornife/jul/actions/runs/<RUN_ID>) |
| NGINX config importer (Y1-09) | 2026-07-03 | ✅ [v1.28.0 soak](https://github.com/victornife/jul/actions/runs/<RUN_ID>) |
| Response cache (memory + disk) | 2026-07-03 | ✅ [v1.28.0 soak](https://github.com/victornife/jul/actions/runs/<RUN_ID>) |
| OTel tracing + access-log sinks (Y1-10) | 2026-07-03 | ✅ [v1.28.0 soak](https://github.com/victornife/jul/actions/runs/<RUN_ID>) |
| HTTP/3 over QUIC (Y1-11) | 2026-07-03 | ☐ soak queued on next tag (add to soak pipeline on v1.29) |

## Changelog

| Date | Ver | What changed | What stayed | Source |
| --- | --- | --- | --- | --- |
| 2026-06-26 | 1.26 | **Cross-cutting: soak harness + enforced release gate** (makes criterion ⑤ a real gate, per [ADR 0005](adr/0005-soak-post-ga-gate.md)). Added [scripts/soak.sh](../scripts/soak.sh) wrapping an in-tree `TestSoak` (behind the `soak` build tag) that drives sustained concurrent traffic through the reverse-proxy data path and asserts zero request errors, a steady goroutine count, and bounded post-GC heap growth (a leak gate); a `soak (smoke)` [CI job](../.github/workflows/ci.yml) runs a short burst on every push so the harness cannot rot; and a tag-triggered [release workflow](../.github/workflows/release.yml) runs the full multi-minute soak and gates the release job on it — a red soak blocks the tagged release. Added a CI status badge to the [README](../README.md). | The waves, the bar, and the GA — soak-pending features; soak stays a post-GA gate (ADR 0005) — it is now enforced rather than only tracked in prose. | [scripts/soak.sh](../scripts/soak.sh), [.github/workflows/release.yml](../.github/workflows/release.yml), [.github/workflows/ci.yml](../.github/workflows/ci.yml) |
| 2026-07-03 | 1.28 | **Active health checks (Y1-05) → GA — soak pending.** Published [health.md](health.md) with a full conformance matrix (HTTP vs TCP probe behaviours), threshold/limitations section, and GA status table; the evidence bundle closes remaining gaps ①②③⑥⑦. Added to soak-tracking table. The Beta burndown row is now cleared. | The remaining ☐ features; soak stays a post-GA gate (ADR 0005). | [health.md](health.md), [internal/upstream/health.go](../internal/upstream/health.go), [internal/upstream/health_test.go](../internal/upstream/health_test.go) |
| 2026-07-03 | 1.28 | **Web application firewall (Y2-06) → GA — soak pending.** Published [waf.md](waf.md) with a full behaviour matrix (rule source × mode combinations), request-overhead benchmarks (`BenchmarkWAF_NoRules`, `BenchmarkWAF_CRSBlock_Pass`, `BenchmarkWAF_CRSBlock_Block`, `BenchmarkWAF_CRSDetect_Pass`), and a threat note covering false positives, bypass scenarios (request-size / response-body / engine-bug evasion), replay/detection gaps, and config-trust vectors. Added 3 CRS integration tests (SQLi, path-traversal, XSS) plus detect-mode coverage. The evidence bundle closes remaining gaps ①②⑦. Added to soak-tracking table. | The remaining ☐ features; soak stays a post-GA gate (ADR 0005). | [waf.md](waf.md), [internal/waf/bench_test.go](../internal/waf/bench_test.go), [internal/waf/firewall_test.go](../internal/waf/firewall_test.go) |
| 2026-07-03 | 1.28 | **Service discovery / dynamic upstreams (Y2-05) → GA — soak pending.** Published [service-discovery.md](service-discovery.md) with a full provider behaviour matrix (5 providers × capabilities), known-limitations section (keep-last-good staleness, DNS TTL ignored, SRV priorities, no cross-provider migration, no dual-stack control), and a threat note covering token exposure (Consul ACL / K8s SA), SSRF trust boundary, and stale-backend risk. Evidence bundle closes remaining gaps ①②③⑥⑦. Added to soak-tracking table. | The remaining ☐ features; soak stays a post-GA gate (ADR 0005). | [service-discovery.md](service-discovery.md), [internal/upstream/discovery.go](../internal/upstream/discovery.go), [internal/upstream/discovery_test.go](../internal/upstream/discovery_test.go) |
| 2026-07-03 | 1.29 | **Secrets references + log redaction (SEC-1) → GA — soak pending.** Published [secrets.md](secrets.md) with a 12-row behaviour matrix (reference sources, resolution, redaction, reload), 5 redaction benchmarks in `internal/redact/bench_test.go` (0-allocation miss path, ~100 ns), and an 8-row threat note covering VCS leak, log exposure, short-secret floor, env/file permissions, console/admin isolation, and precedence. Evidence bundle closes remaining gaps ①②⑥⑦. Added to soak-tracking table. | The remaining ☐ features; soak stays a post-GA gate (ADR 0005). | [secrets.md](secrets.md), [internal/redact/bench_test.go](../internal/redact/bench_test.go), [internal/redact/redact_test.go](../internal/redact/redact_test.go) |
| 2026-07-03 | 1.29 | **Rate + connection limiting (Y1-03) → GA — soak pending.** Published [ratelimit.md](ratelimit.md) with a 12-row behaviour matrix (key strategies, scope rules, eviction), 4 rate-limiter benchmarks (`BenchmarkRateLimiterAllow` ~300 ns, parallel contention ~600 ns), and an 8-row threat note covering IP spoofing, key collision, slowloris, header/JWT bypass, and config reload state preservation. Evidence bundle closes remaining gaps ①②⑥⑦. Added to soak-tracking table. | The remaining ☐ features; soak stays a post-GA gate (ADR 0005). | [ratelimit.md](ratelimit.md), [internal/middleware/ratelimit_bench_test.go](../internal/middleware/ratelimit_bench_test.go), [internal/middleware/ratelimit_test.go](../internal/middleware/ratelimit_test.go) |
| 2026-07-03 | 1.29 | **Zero-config + `jul lint` (Y1-08) → GA — soak pending.** Published [zeroconf.md](zeroconf.md) with a 10-row lint checks matrix, 5 benchmarks (lint ~380 ns, synthesiser ~2 μs), `FuzzParse` fuzz target for TOML config round-trip, and a threat note covering literal secrets, admin exposure, weak TLS defaults, and lint bypass. Evidence bundle closes remaining gaps ①②⑥⑦⑧. Added to soak-tracking table. | The remaining ☐ features; soak stays a post-GA gate (ADR 0005). | [zeroconf.md](zeroconf.md), [internal/config/lint_bench_test.go](../internal/config/lint_bench_test.go), [internal/config/fuzz_test.go](../internal/config/fuzz_test.go) |
| 2026-07-03 | 1.29 | **Compression (Y1-02) → GA — soak pending.** Published [compression.md](compression.md) with a 3-encoder matrix (gzip/br/zstd levels and build tags), 4 benchmarks (pass-through ~7 μs, small gzip ~49 μs, large gzip ~306 μs), and a 6-row threat note covering BREACH, CRIME, compression bombs, cache poisoning, sidecar leak, and CPU exhaustion. Evidence bundle closes remaining gaps ①②⑥⑦. Added to soak-tracking table. | The remaining ☐ features; soak stays a post-GA gate (ADR 0005). | [compression.md](compression.md), [internal/middleware/compress_bench_test.go](../internal/middleware/compress_bench_test.go), [internal/middleware/compress_test.go](../internal/middleware/compress_test.go) |
| 2026-07-03 | 1.29 | **NGINX config importer (Y1-09) → GA — soak pending.** Published [nginx-importer.md](nginx-importer.md) with a full directive-support matrix (top-level, http, server, location, upstream, modifiers), 2 benchmarks (`BenchmarkParse` ~45 μs, `BenchmarkTranslate` ~6.5 μs), `FuzzTranslate` fuzz target for parse+translate+marshal round-trip, 9-item known-limitations list, and 6-row threat note (craft-conf crash, path traversal, credential leak, info disclosure, translation misconfig, dependency trust). Evidence bundle closes remaining gaps ①②③⑥⑦⑧. Added to soak-tracking table. | The remaining ☐ features; soak stays a post-GA gate (ADR 0005). | [nginx-importer.md](nginx-importer.md), [internal/migrate/nginx/bench_test.go](../internal/migrate/nginx/bench_test.go), [internal/migrate/nginx/fuzz_test.go](../internal/migrate/nginx/fuzz_test.go) |
| 2026-07-03 | 1.29 | **Response cache (memory + disk) → GA — soak pending.** Published 14-row behaviour matrix (key, Vary, TTL, status codes, conditional requests, eviction), 4 benchmarks (`BenchmarkCacheHit` ~2.4 μs, `Miss` ~10.6 μs, `VaryHit` ~2.9 μs, `MemOverflow` ~4.4 ms), 4-item limitation list, and 6-row threat note (Host poisoning, Vary leakage, Web Cache Deception, SIF DoS, disk PII, header smuggling). Evidence bundle closes remaining gaps ①②③⑥⑦. Added to soak-tracking table. | The remaining ☐ features; soak stays a post-GA gate (ADR 0005). | [cache.md](cache.md), [internal/cache/bench_test.go](../internal/cache/bench_test.go), [internal/cache/cache_test.go](../internal/cache/cache_test.go) |
| 2026-07-03 | 1.29 | **OTel tracing + access-log sinks (Y1-10) → GA — soak pending.** Published [otel.md](otel.md) with exporter/sink matrix (OTLP-gRPC/HTTP, span types, W3C propagation, access-log sinks/fields), 5 benchmarks (middleware ~10.4 μs, seam child span ~2.5 μs, no-op seam ~20 ns), 4-item limitation list, and 5-row PII threat note (URL tokens, trace id linking, file leakage, insecure collector, error disclosure). Evidence bundle closes remaining gaps ①②③⑥⑦. Added to soak-tracking table. | The remaining ☐ features; soak stays a post-GA gate (ADR 0005). | [otel.md](otel.md), [internal/observability/bench_test.go](../internal/observability/bench_test.go), [internal/observability/tracing_test.go](../internal/observability/tracing_test.go) |
| 2026-07-03 | 1.29 | **HTTP/3 over QUIC (Y1-11) → GA — soak pending.** Published [http3.md](http3.md) with QUIC/Alt-Svc behaviour matrix (protocol negotiation, build-time, defaults), `BenchmarkHTTP3Throughput` (~259 μs/op, 13.9 KB/op), 4-item limitation list (no WebSocket, restart required for Alt-Svc, UDP port sharing, bind-time handler generation), and 5-row threat note (0-RTT replay, UDP amplification, UDP exhaustion, cert sharing, Alt-Svc tracking). Evidence bundle closes remaining gaps ①②③⑥⑦⑧. Added to soak-tracking table. | The remaining ☐ features (WASM plugins, L4 stream proxy); soak stays a post-GA gate (ADR 0005). | [http3.md](http3.md), [internal/server/http3_test.go](../internal/server/http3_test.go) |
| 2026-06-24 | 1.25 | Added the two newly shipped **Beta** features to Wave 2: **Y2-06 WAF** (`waf`) and **SEC-1 secrets references** (core). Both ship with docs ([waf.md](waf.md), [secrets.md](secrets.md)) and a Console surface (⑥ + ⑨ met); their remaining GA gaps are the behaviour matrix (①), a benchmark (②), and a threat note (⑦). ⑧ is **n/a** for both (Coraza owns SecLang parsing; secret references reuse the config parser). | The waves, the bar, and the GA — soak-pending features; soak stays a post-GA gate (ADR 0005). | [waf.md](waf.md), [secrets.md](secrets.md), [status.md](status.md) |
| 2026-06-23 | 1.24 | **Console (Y1-07 · Y2-09) → GA — soak pending.** The embedded-SPA substrate cutover (React/TS/Vite, Node-free build, ~250 KB gz budget) flips the v2 console to the default admin UI at `/`, retires the hand-written v1 (`console.html`) and its dev route, and closes the last two Console GA gaps — ① the [endpoint/panel matrix](console.md#api-endpoint-to-panel-map) and ⑦ the formalised CSP-nonce + constant-time bearer security model. Added Console to the soak-tracking table. | The waves, the bar, and the remaining ☐ features; soak stays a post-GA gate (ADR 0005); ⑧ stays **n/a** (the console adds no custom parser). | [console.md](console.md), [console-v2 spec](specs/console-v2.md), [status.md](status.md) |
| 2026-06-21 | 1.23 | **Cross-cutting: `SECURITY.md` umbrella threat model** (anchors criterion ⑦ fleet-wide). Added a top-level [SECURITY.md](../SECURITY.md): the edge trust model (config trusted, requests untrusted, no request-selected upstreams/JWKS), hardening defaults, a per-feature threat-note index (Core HTTP, auth, TLS/ACME, mTLS, gRPC transcoding/passthrough, console), the fuzzed-parser inventory, a cryptography summary, and a private vulnerability-reporting policy. **This completes all four cross-cutting tasks** — every GA criterion is now hosted/anchored fleet-wide. | Every feature's runtime behaviour and per-feature threat notes (the umbrella only indexes + links them); the waves and the bar. | [SECURITY.md](../SECURITY.md) |
| 2026-06-21 | 1.22 | **Cross-cutting: fuzz corpus + CI fuzz job** (hosts criterion ⑧ fleet-wide). Added [scripts/fuzz.sh](../scripts/fuzz.sh) — it discovers every in-tree `Fuzz*` target (auth JWKS/token, router host/location, FastCGI script-name/socket-address, transcode path-template) and runs each for a short `-fuzztime` with the full opt-in tag set — and a `fuzz (smoke)` [CI job](../.github/workflows/ci.yml) that runs it on every push/PR, uploading any minimised crasher as a reproducible regression seed. Seed corpora stay in-code via `f.Add`. | Every feature's runtime behaviour, the waves, the bar, and the existing fuzz targets; only new CI tooling is added. | [scripts/fuzz.sh](../scripts/fuzz.sh), [.github/workflows/ci.yml](../.github/workflows/ci.yml) |
| 2026-06-21 | 1.21 | **Cross-cutting: perf-gate benchmark harness + CI job** (hosts criterion ② fleet-wide). Added [scripts/bench.sh](../scripts/bench.sh) — a single harness that runs every in-tree `Benchmark*` with the full opt-in tag set — and a `benchmarks (smoke)` [CI job](../.github/workflows/ci.yml) that runs it on every push/PR so benchmarks must keep compiling and executing without panic. A `.gitattributes` pins `*.sh` to LF. The job is a smoke + artifact gate, **not** a nanosecond regression gate (shared runners are too noisy); doc numbers are regenerated on a quiet machine. | Every feature's runtime behaviour, the waves, the bar, and the documented benchmark numbers; only new CI tooling is added. | [scripts/bench.sh](../scripts/bench.sh), [.github/workflows/ci.yml](../.github/workflows/ci.yml) |
| 2026-06-21 | 1.19 | **Core HTTP → GA.** Published [core-http.md](core-http.md) (request lifecycle; host/location/static/proxy/FastCGI/balancing matrices; path-traversal + SSRF + CRLF threat note; limits; GA table) and added router, balancer, and static-serve benchmarks plus `FuzzHostScore`/`FuzzMatchLocation` (router) and `FuzzScriptName`/`FuzzParseSocketAddress` (FastCGI). Added Core HTTP to the soak-tracking table. | The waves, plan, and remaining ☐ features; soak stays a post-GA gate. | [core-http.md](core-http.md), [internal/router/router_bench_test.go](../internal/router/router_bench_test.go), [internal/upstream/balancer_bench_test.go](../internal/upstream/balancer_bench_test.go), [internal/handler/fastcgi_fuzz_test.go](../internal/handler/fastcgi_fuzz_test.go) |
| 2026-06-21 | 1.17 | **First three GA features.** Flipped Wave 1 quick wins to **GA**: gRPC transcoding (Y2-01), gRPC passthrough (Y2-04), mTLS (Y2-07). Closed mTLS criterion ② with the [handshake-cost benchmark](mtls.md#benchmarks) (`BenchmarkMTLSHandshake`) and the cross-cutting criterion ④ with the semver-guarded [compatibility policy](compatibility.md); added the three to the soak-tracking table (soak pending, post-GA). | The plan, waves, effort sizing, and the remaining ☐ features; soak stays a post-GA gate (ADR 0005). | [compatibility.md](compatibility.md), [mtls.md](mtls.md#benchmarks), [grpc-transcoding.md](grpc-transcoding.md), [grpc-proxy.md](grpc-proxy.md) |
| 2026-06-21 | 1.0 | Created the GA push log: the per-feature Beta→GA plan in three waves with effort + gaps, the cross-cutting tasks, and the soak-tracking table. Records the decision (ADR 0005) to treat the soak test as a post-GA gate so GA is declared against the other eight criteria. | The maturity ladder, the other eight GA criteria, and the Console-first invariant (ADR 0004) are unchanged. | [ADR 0003](adr/0003-maturity-and-ga.md), [ADR 0005](adr/0005-soak-post-ga-gate.md) |
