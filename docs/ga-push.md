# Jul.IA — GA push (Beta → GA)

> Version 1.11 · Updated 2026-06-26

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

## Wave 2 — P1 (demand-pull + security-sensitive)

| Feature | Maturity | Gaps to GA (excl. soak) | Effort | Status |
| --- | --- | --- | --- | --- |
| WASM plugins (Y2-02) | Beta | ① ABI/caps matrix · ② call-overhead bench · ⑦ sandbox note · ⑧ ABI fuzz | L | ☐ |
| L4 stream proxy (Y2-03) | Beta | ① TCP/UDP/SNI/PROXY matrix · ② throughput bench · ⑧ PROXY+SNI parser fuzz · ⑦ spoofing note | M–L | ☐ |
| Rate + connection limiting (Y1-03) | Beta | ⑥ docs · ① key/algorithm matrix · ② limiter bench · ⑦ bypass note | M | ☐ |
| Response cache (memory + disk) | Beta | ⑥ docs · ① key/TTL/overflow matrix · ② hit/miss bench · ⑦ poisoning/isolation | M | ☐ |
| Compression (gzip; brotli/zstd) (Y1-02) | Beta | ⑥ docs · ① encoder matrix · ② throughput bench · ⑦ BREACH note | M | ☐ |
| HTTP/3 over QUIC (Y1-11) | Beta | ⑥ docs · ① QUIC/Alt-Svc matrix · ② bench · ③ bind-time/no-WS · ⑦ 0-RTT/amplification | M | ☐ |
| OTel tracing + access-log sinks (Y1-10) | Beta | ⑥ docs · ① exporter/sink matrix · ② overhead bench · ⑦ PII note | M | ☐ |
| Service discovery (Y2-05) | Beta | ① provider matrix · ③ keep-last-good limits · ⑦ K8s-token/SSRF (docs ✅) | M | ☐ |
| WAF (Y2-06) | Beta | ① rule/CRS/mode matrix · ② request-overhead bench · ⑦ false-positive/bypass note (docs ✅) | M–L | ☐ |
| Secrets references (SEC-1) | Beta | ① ref-source matrix · ② resolve-cost bench · ⑦ leak/precedence note (docs ✅) | S–M | ☐ |
| Active health checks (Y1-05) | Beta | ⑥ docs · ① probe matrix · ③ limits | S–M | ☐ |
| Console (Y1-07 · Y2-09) | **GA — soak pending** | none — [console.md](console.md) doc + endpoint/panel matrix + CSP-nonce/bearer security model landed; v1 retired by the embedded-SPA substrate cutover (Y2-09) | M | ✅ |

## Wave 3 — P2 (dev-time CLI tools)

| Feature | Maturity | Gaps to GA (excl. soak) | Effort | Status |
| --- | --- | --- | --- | --- |
| NGINX importer (Y1-09) | Beta | ⑥ docs · ① directive-support matrix · ⑧ nginx.conf parser fuzz · ③ unmapped-directive limits | M | ☐ |
| Zero-config + `jul lint` (Y1-08) | Beta | ⑥ docs · ① lint-checks matrix · ⑧ TOML config-parser fuzz | S–M | ☐ |

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
| gRPC ↔ JSON transcoding (Y2-01) | 2026-06-21 | ☐ pending |
| gRPC passthrough + h2c (Y2-04) | 2026-06-21 | ☐ pending |
| mTLS client auth (Y2-07) | 2026-06-21 | ☐ pending |
| TLS + ACME (Y1-01) | 2026-06-21 | ☐ pending |
| Core HTTP (static/proxy/FastCGI/vhosts/routing) | 2026-06-21 | ☐ pending |
| Auth (CIDR/Basic/JWT/forward-auth) (Y1-04) | 2026-06-21 | ☐ pending |
| Console (Y1-07 · Y2-09) | 2026-06-23 | ☐ pending |

## Changelog

| Date | Ver | What changed | What stayed | Source |
| --- | --- | --- | --- | --- |
| 2026-06-26 | 1.11 | **Cross-cutting: soak harness + enforced release gate** (makes criterion ⑤ a real gate, per [ADR 0005](adr/0005-soak-post-ga-gate.md)). Added [scripts/soak.sh](../scripts/soak.sh) wrapping an in-tree `TestSoak` (behind the `soak` build tag) that drives sustained concurrent traffic through the reverse-proxy data path and asserts zero request errors, a steady goroutine count, and bounded post-GC heap growth (a leak gate); a `soak (smoke)` [CI job](../.github/workflows/ci.yml) runs a short burst on every push so the harness cannot rot; and a tag-triggered [release workflow](../.github/workflows/release.yml) runs the full multi-minute soak and gates the release job on it — a red soak blocks the tagged release. Added a CI status badge to the [README](../README.md). | The waves, the bar, and the GA — soak-pending features; soak stays a post-GA gate (ADR 0005) — it is now enforced rather than only tracked in prose. | [scripts/soak.sh](../scripts/soak.sh), [.github/workflows/release.yml](../.github/workflows/release.yml), [.github/workflows/ci.yml](../.github/workflows/ci.yml) |
| 2026-06-24 | 1.10 | Added the two newly shipped **Beta** features to Wave 2: **Y2-06 WAF** (`waf`) and **SEC-1 secrets references** (core). Both ship with docs ([waf.md](waf.md), [secrets.md](secrets.md)) and a Console surface (⑥ + ⑨ met); their remaining GA gaps are the behaviour matrix (①), a benchmark (②), and a threat note (⑦). ⑧ is **n/a** for both (Coraza owns SecLang parsing; secret references reuse the config parser). | The waves, the bar, and the GA — soak-pending features; soak stays a post-GA gate (ADR 0005). | [waf.md](waf.md), [secrets.md](secrets.md), [status.md](status.md) |
| 2026-06-23 | 1.9 | **Console (Y1-07 · Y2-09) → GA — soak pending.** The embedded-SPA substrate cutover (React/TS/Vite, Node-free build, ~250 KB gz budget) flips the v2 console to the default admin UI at `/`, retires the hand-written v1 (`console.html`) and its dev route, and closes the last two Console GA gaps — ① the endpoint/panel matrix and ⑦ the formalised CSP-nonce + constant-time bearer security model. Added Console to the soak-tracking table. | The waves, the bar, and the remaining ☐ features; soak stays a post-GA gate (ADR 0005); ⑧ stays **n/a** (the console adds no custom parser). | [console.md](console.md), [console-v2 spec](../specs/console-v2.md), [status.md](status.md) |
| 2026-06-21 | 1.7 | **Cross-cutting: `SECURITY.md` umbrella threat model** (anchors criterion ⑦ fleet-wide). Added a top-level [SECURITY.md](../SECURITY.md): the edge trust model (config trusted, requests untrusted, no request-selected upstreams/JWKS), hardening defaults, a per-feature threat-note index (Core HTTP, auth, TLS/ACME, mTLS, gRPC transcoding/passthrough, console), the fuzzed-parser inventory, a cryptography summary, and a private vulnerability-reporting policy. **This completes all four cross-cutting tasks** — every GA criterion is now hosted/anchored fleet-wide. | Every feature's runtime behaviour and per-feature threat notes (the umbrella only indexes + links them); the waves and the bar. | [SECURITY.md](../SECURITY.md) |
| 2026-06-21 | 1.6 | **Cross-cutting: fuzz corpus + CI fuzz job** (hosts criterion ⑧ fleet-wide). Added [scripts/fuzz.sh](../scripts/fuzz.sh) — it discovers every in-tree `Fuzz*` target (auth JWKS/token, router host/location, FastCGI script-name/socket-address, transcode path-template) and runs each for a short `-fuzztime` with the full opt-in tag set — and a `fuzz (smoke)` [CI job](../.github/workflows/ci.yml) that runs it on every push/PR, uploading any minimised crasher as a reproducible regression seed. Seed corpora stay in-code via `f.Add`. | Every feature's runtime behaviour, the waves, the bar, and the existing fuzz targets; only new CI tooling is added. | [scripts/fuzz.sh](../scripts/fuzz.sh), [.github/workflows/ci.yml](../.github/workflows/ci.yml) |
| 2026-06-21 | 1.5 | **Cross-cutting: perf-gate benchmark harness + CI job** (hosts criterion ② fleet-wide). Added [scripts/bench.sh](../scripts/bench.sh) — a single harness that runs every in-tree `Benchmark*` with the full opt-in tag set — and a `benchmarks (smoke)` [CI job](../.github/workflows/ci.yml) that runs it on every push/PR so benchmarks must keep compiling and executing without panic. A `.gitattributes` pins `*.sh` to LF. The job is a smoke + artifact gate, **not** a nanosecond regression gate (shared runners are too noisy); doc numbers are regenerated on a quiet machine. | Every feature's runtime behaviour, the waves, the bar, and the documented benchmark numbers; only new CI tooling is added. | [scripts/bench.sh](../scripts/bench.sh), [.github/workflows/ci.yml](../.github/workflows/ci.yml) |
| 2026-06-21 | 1.4 | **Auth (Y1-04) → GA** — the last Wave 1 feature. Published [auth.md](auth.md) (CIDR/Basic/JWT/forward-auth behaviour matrix; JWKS + algorithm-confusion + username-enum threat note; limits; GA table) and added `BenchmarkBasicVerify`/`BenchmarkJWTValidate` plus `FuzzParseJWKS`/`FuzzValidateToken`. Added Auth to the soak-tracking table. Also **relabeled every GA feature `GA` → `GA — soak pending`** here and across the roadmap/specs/feature docs, since the soak test is still open for all of them. | The waves, plan, the bar, and remaining ☐ features; soak stays a post-GA gate (ADR 0005). | [auth.md](auth.md), [internal/auth/auth_bench_test.go](../internal/auth/auth_bench_test.go), [internal/auth/auth_fuzz_test.go](../internal/auth/auth_fuzz_test.go) |
| 2026-06-21 | 1.3 | **Core HTTP → GA.** Published [core-http.md](core-http.md) (request lifecycle; host/location/static/proxy/FastCGI/balancing matrices; path-traversal + SSRF + CRLF threat note; limits; GA table) and added router, balancer, and static-serve benchmarks plus `FuzzHostScore`/`FuzzMatchLocation` (router) and `FuzzScriptName`/`FuzzParseSocketAddress` (FastCGI). Added Core HTTP to the soak-tracking table. | The waves, plan, and remaining ☐ features; soak stays a post-GA gate. | [core-http.md](core-http.md), [internal/router/router_bench_test.go](../internal/router/router_bench_test.go), [internal/upstream/balancer_bench_test.go](../internal/upstream/balancer_bench_test.go), [internal/handler/fastcgi_fuzz_test.go](../internal/handler/fastcgi_fuzz_test.go) |
| 2026-06-21 | 1.2 | **TLS + ACME (Y1-01) → GA.** Published [tls-acme.md](tls-acme.md) (behaviour matrix, SNI/ACME/OCSP semantics, threat note, limits, GA table) and added `BenchmarkSNICertSelection` (0-alloc) alongside the existing handshake benchmark. Added Y1-01 to the soak-tracking table. | The waves, plan, and remaining ☐ features; soak stays a post-GA gate. | [tls-acme.md](tls-acme.md), [internal/server/tls_bench_test.go](../internal/server/tls_bench_test.go) |
| 2026-06-21 | 1.1 | **First three GA features.** Flipped Wave 1 quick wins to **GA**: gRPC transcoding (Y2-01), gRPC passthrough (Y2-04), mTLS (Y2-07). Closed mTLS criterion ② with the [handshake-cost benchmark](mtls.md#benchmarks) (`BenchmarkMTLSHandshake`) and the cross-cutting criterion ④ with the semver-guarded [compatibility policy](compatibility.md); added the three to the soak-tracking table (soak pending, post-GA). | The plan, waves, effort sizing, and the remaining ☐ features; soak stays a post-GA gate (ADR 0005). | [compatibility.md](compatibility.md), [mtls.md](mtls.md#benchmarks), [grpc-transcoding.md](grpc-transcoding.md), [grpc-proxy.md](grpc-proxy.md) |
| 2026-06-21 | 1.0 | Created the GA push log: the per-feature Beta→GA plan in three waves with effort + gaps, the cross-cutting tasks, and the soak-tracking table. Records the decision (ADR 0005) to treat the soak test as a post-GA gate so GA is declared against the other eight criteria. | The maturity ladder, the other eight GA criteria, and the Console-first invariant (ADR 0004) are unchanged. | [ADR 0003](adr/0003-maturity-and-ga.md), [ADR 0005](adr/0005-soak-post-ga-gate.md) |
