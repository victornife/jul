# Jul.IA — GA push (Beta → GA)

> Version 1.1 · Updated 2026-06-21

A focused, tracked effort to move the **existing** feature set from **Beta** to
**GA** before starting new features. Per [ADR 0005](adr/0005-soak-post-ga-gate.md)
the long-running **soak test** is a *post-GA* gate, so GA here is declared against
the **other eight** criteria of the [ADR 0003](adr/0003-maturity-and-ga.md) bar;
soak is tracked openly per feature and completed after the label lands.

This is the execution log for that push. **Keep it current:** tick a feature's
criteria as they land, flip its row to ✅ when it reaches GA, and add a changelog
row.

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
| Perf-gate benchmark harness + CI job | hosts **2** | M | ☐ |
| Fuzz corpus + CI fuzz job (extend `FuzzParseTemplate`) | hosts **8** | S–M | ☐ |
| `SECURITY.md` umbrella threat model | anchors **7** | S | ☐ |

## Wave 1 — P0 (foundation + quick wins)

| Feature | Maturity | Gaps to GA (excl. soak) | Effort | Status |
| --- | --- | --- | --- | --- |
| gRPC ↔ JSON transcoding (Y2-01) | **GA** | none — ①②③④⑥⑦⑧⑨ done | S | ✅ |
| gRPC passthrough + h2c (Y2-04) | **GA** | none — ①②③④⑥⑦⑧⑨ done | S | ✅ |
| mTLS client auth (Y2-07) | **GA** | none — handshake benchmark + v1 freeze landed | S | ✅ |
| Core HTTP (static, reverse proxy, FastCGI/uWSGI, vhosts, routing) | Beta | ⑥ docs · ① matrix · ② benchmarks · ⑦ path-traversal/SSRF · ⑧ router/path fuzz | L | ☐ |
| TLS + ACME (Y1-01) | Beta | ⑥ docs · ① challenge/OCSP matrix · ③ DNS-01 absent · ⑦ threat note | M | ☐ |
| Auth (Basic, JWT/JWKS, forward-auth) (Y1-04) | Beta | ⑥ docs · ① matrix · ⑧ JWT/JWKS fuzz · ⑦ threat note · ② verify bench | L | ☐ |

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
| Active health checks (Y1-05) | Beta | ⑥ docs · ① probe matrix · ③ limits | S–M | ☐ |
| Console v1 (Y1-07) | Beta | ① endpoint/panel matrix · ⑦ formalize CSRF/CSP/auth (security model ✅) | M | ☐ |

## Wave 3 — P2 (dev-time CLI tools)

| Feature | Maturity | Gaps to GA (excl. soak) | Effort | Status |
| --- | --- | --- | --- | --- |
| NGINX importer (Y1-09) | Beta | ⑥ docs · ① directive-support matrix · ⑧ nginx.conf parser fuzz · ③ unmapped-directive limits | M | ☐ |
| Zero-config + `jul lint` (Y1-08) | Beta | ⑥ docs · ① lint-checks matrix · ⑧ TOML config-parser fuzz | S–M | ☐ |

## Soak tracking (post-GA gate, per ADR 0005)

The one deferred criterion. A GA feature is added here and its soak run tracked;
a soak failure is a release-blocking regression.

| Feature | GA on | Soak status |
| --- | --- | --- |
| gRPC ↔ JSON transcoding (Y2-01) | 2026-06-21 | ☐ pending |
| gRPC passthrough + h2c (Y2-04) | 2026-06-21 | ☐ pending |
| mTLS client auth (Y2-07) | 2026-06-21 | ☐ pending |

## Changelog

| Date | Ver | What changed | What stayed | Source |
| --- | --- | --- | --- | --- |
| 2026-06-21 | 1.1 | **First three GA features.** Flipped Wave 1 quick wins to **GA**: gRPC transcoding (Y2-01), gRPC passthrough (Y2-04), mTLS (Y2-07). Closed mTLS criterion ② with the [handshake-cost benchmark](mtls.md#benchmarks) (`BenchmarkMTLSHandshake`) and the cross-cutting criterion ④ with the semver-guarded [compatibility policy](compatibility.md); added the three to the soak-tracking table (soak pending, post-GA). | The plan, waves, effort sizing, and the remaining ☐ features; soak stays a post-GA gate (ADR 0005). | [compatibility.md](compatibility.md), [mtls.md](mtls.md#benchmarks), [grpc-transcoding.md](grpc-transcoding.md), [grpc-proxy.md](grpc-proxy.md) |
| 2026-06-21 | 1.0 | Created the GA push log: the per-feature Beta→GA plan in three waves with effort + gaps, the cross-cutting tasks, and the soak-tracking table. Records the decision (ADR 0005) to treat the soak test as a post-GA gate so GA is declared against the other eight criteria. | The maturity ladder, the other eight GA criteria, and the Console-first invariant (ADR 0004) are unchanged. | [ADR 0003](adr/0003-maturity-and-ga.md), [ADR 0005](adr/0005-soak-post-ga-gate.md) |
