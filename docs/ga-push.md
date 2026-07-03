# Jul.IA — GA push (Beta → GA)

> Version 1.30 · Updated 2026-07-03

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
| HTTP/3 over QUIC (Y1-11) | 2026-07-03 | ☐ soak queued on v1.29.0 tag (CI running) |

> **All 20 shipped features are now GA — soak pending.** Waves 2 and 3 are retired; all features consolidated into Wave 1 above. The only remaining work is the soak test (post-GA gate per [ADR 0005](adr/0005-soak-post-ga-gate.md)).

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
| HTTP/3 over QUIC (Y1-11) | 2026-07-03 | ☐ soak queued on v1.29.0 tag (CI running) |
| WASM plugins (Y2-02) | 2026-07-03 | ☐ soak queued on v1.29.0 tag (CI running) |
| L4 stream proxy (Y2-03) | 2026-07-03 | ☐ soak queued on v1.29.0 tag (CI running, udp-churn scenario) |

## Changelog

| Date | Ver | What changed | What stayed | Source |
| --- | --- | --- | --- | --- |
| 2026-07-03 | 1.30 | **Beta backlog cleared — all 20 shipped features GA — soak pending.** The last 10 Beta features completed their evidence bundles and moved to GA — soak pending. Year-1 checklist 11/11, Year-2 checklist 9/9 — zero committed Beta features remain. The only remaining GA gate is the soak test (post-GA per ADR 0005). Next work is the Hardening & platform backlog (HP-01..HP-07), the AI-MVP bet, or demand-gated horizon items. | All Year 3–5 vision rows remain horizon-demand-gated; no feature rows or IDs changed, only maturity labels and checklist counts. | [status.md](status.md), [ga-push.md](ga-push.md), http3.md, plugins.md, stream.md |
| 2026-07-03 | 1.29 | **Beta → GA — soak pending:** Y1-11 HTTP/3, Y2-02 WASM plugins, Y2-03 L4 stream proxy reach GA — soak pending. Evidence bundles closed for all three. Added to soak-tracking table. | No remaining ☐ Beta features; soak stays a post-GA gate (ADR 0005). | [http3.md](http3.md), [plugins.md](plugins.md), [stream.md](stream.md) |
| 2026-07-03 | 1.28 | **Active health checks (Y1-05) → GA — soak pending.** Published [health.md](health.md) with full conformance matrix, threshold/limitations section, and GA status table. | The remaining ☐ features; soak stays a post-GA gate (ADR 0005). | [health.md](health.md) |
| 2026-07-03 | 1.28 | **Web application firewall (Y2-06) → GA — soak pending.** Published [waf.md](waf.md) with full behaviour matrix, request-overhead benchmarks, and threat note. | The remaining ☐ features; soak stays a post-GA gate (ADR 0005). | [waf.md](waf.md) |
| 2026-07-03 | 1.28 | **Service discovery / dynamic upstreams (Y2-05) → GA — soak pending.** Published [service-discovery.md](service-discovery.md) with provider behaviour matrix, known-limitations, and threat note. | The remaining ☐ features; soak stays a post-GA gate (ADR 0005). | [service-discovery.md](service-discovery.md) |
| 2026-06-24 | 1.25 | Added the two newly shipped **Beta** features to Wave 2: **Y2-06 WAF** (`waf`) and **SEC-1 secrets references** (core). | The waves, the bar, and the GA — soak-pending features; soak stays a post-GA gate (ADR 0005). | [waf.md](waf.md), [secrets.md](secrets.md), [status.md](status.md) |
| 2026-06-23 | 1.24 | **Console (Y1-07 · Y2-09) → GA — soak pending.** The embedded-SPA substrate cutover closes the last two Console GA gaps. | The waves, the bar, and the remaining ☐ features; soak stays a post-GA gate (ADR 0005). | [console.md](console.md) |
| 2026-06-21 | 1.23 | **Cross-cutting: `SECURITY.md` umbrella threat model** (anchors criterion ⑦ fleet-wide). | Every feature's runtime behaviour and per-feature threat notes; the waves and the bar. | [SECURITY.md](../SECURITY.md) |
| 2026-06-21 | 1.22 | **Cross-cutting: fuzz corpus + CI fuzz job** (hosts criterion ⑧ fleet-wide). | Every feature's runtime behaviour, the waves, the bar, and the existing fuzz targets. | [scripts/fuzz.sh](../scripts/fuzz.sh) |
| 2026-06-21 | 1.21 | **Cross-cutting: perf-gate benchmark harness + CI job** (hosts criterion ② fleet-wide). | Every feature's runtime behaviour, the waves, the bar, and the documented benchmark numbers. | [scripts/bench.sh](../scripts/bench.sh) |
| 2026-06-21 | 1.19 | **Core HTTP → GA.** Published [core-http.md](core-http.md) and added router/balancer benchmarks + fuzz targets. | The waves, plan, and remaining ☐ features; soak stays a post-GA gate. | [core-http.md](core-http.md) |
| 2026-06-21 | 1.17 | **First three GA features.** Flipped Wave 1 quick wins to **GA**: gRPC transcoding (Y2-01), gRPC passthrough (Y2-04), mTLS (Y2-07). | The plan, waves, effort sizing, and the remaining ☐ features; soak stays a post-GA gate (ADR 0005). | [compatibility.md](compatibility.md), [mtls.md](mtls.md) |
| 2026-06-21 | 1.0 | Created the GA push log: the per-feature Beta→GA plan in three waves with effort + gaps, the cross-cutting tasks, and the soak-tracking table. Records the decision (ADR 0005) to treat the soak test as a post-GA gate so GA is declared against the other eight criteria. | The maturity ladder, the other eight GA criteria, and the Console-first invariant (ADR 0004) are unchanged. | [ADR 0003](adr/0003-maturity-and-ga.md), [ADR 0005](adr/0005-soak-post-ga-gate.md) |
