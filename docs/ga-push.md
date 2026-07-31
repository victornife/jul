# Jul.IA — GA push (Beta → GA)

> - Version 1.37 · Updated 2026-07-31

A focused, tracked effort to move the **existing** feature set from **Beta** to
**GA** before starting new features. Per [ADR 0005](adr/0005-soak-post-ga-gate.md)
the long-running **soak test** is a *post-GA* gate, so GA here is declared against
the **other eight** criteria of the [ADR 0003](adr/0003-maturity-and-ga.md) bar;
soak is tracked openly per feature and completed after the label lands.

This is the execution log for that push. **Keep it current:** tick a feature's
criteria as they land, flip its row to ✅ when it reaches GA, and add a changelog
row.

> - At a glance: [docs/status.md](status.md) is the canonical maturity +
> - GA-criteria matrix across **all** features (it consolidates the waves and soak
> - table below with each feature's *GA status* table).

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
| gRPC ↔ JSON transcoding (Y2-01) | **GA** | none — soaked 1h isolated 2026-07-07 (14.2M req, 0.000007% err, REST/JSON → gRPC) — all criteria met ✅ | S | ✅ |
| gRPC passthrough + h2c (Y2-04) | **GA** | none — soaked 1h isolated 2026-07-07 (6.8M req, 0.0002% err, native gRPC/h2c → upstream gRPC) — all criteria met ✅ | S | ✅ |
| mTLS client auth (Y2-07) | **GA** | none — soaked via Phase 2A 2026-07-05 (2.12M req, 0% err, client-cert path) — all criteria met ✅ | S | ✅ |
| Core HTTP (static, reverse proxy, FastCGI/uWSGI, vhosts, routing) | **GA** | none — soaked 8h 2026-07-04 + Phase 2A 2026-07-05 (2.12M req, 0% err) — all criteria met ✅ | L | ✅ |
| TLS + ACME (Y1-01) | **GA** | none — soaked via Phase 2A 2026-07-05 (2.12M req, 0% err, 25% TLS traffic) — all criteria met ✅ | M | ✅ |
| Auth (Basic, JWT/JWKS, forward-auth) (Y1-04) | **GA** | none — soaked 1h 2026-07-04 + Phase 2A — all criteria met ✅ | L | ✅ |
| Console (Y1-07 · Y2-09) | **GA** | none — soaked 8h 2026-07-04 (console tag built, dashboard reachable) + Phase 2A — all criteria met ✅ | M | ✅ |
| Active health checks (Y1-05) | **GA** | none — soaked 8h 2026-07-04 (/healthz polled 960×, all 200) + Phase 2A — all criteria met ✅ | S–M | ✅ |
| Web application firewall (WAF) (Y2-06) | **GA** | none — soaked 1h 2026-07-04 + Phase 2A 2026-07-05 (CRS block mode verified) — all criteria met ✅ | M–L | ✅ |
| Service discovery / dynamic upstreams (Y2-05) | **GA** | none — [service-discovery.md](service-discovery.md) doc + provider behaviour matrix + keep-last-good limitations + K8s-token/Consul-ACL threat note + balancer benchmarks landed | M | ✅ |
| Secrets references + log redaction (SEC-1) | **GA** | none — [secrets.md](secrets.md) doc + 12-row reference-source/resolution/redaction behaviour matrix + 5 redaction benchmarks (0-allocation miss path, ~100 ns) + 8-row threat note (VCS leak, log exposure, short-secret floor, env/file permissions) landed | S–M | ✅ |
| Rate + connection limiting (Y1-03) | **GA** | none — soaked 1h 2026-07-04 (12.5M req, 0% err, token-bucket verified) + Phase 2A — all criteria met ✅ | M | ✅ |
| Zero-config + `jul lint` (Y1-08) | **GA** | none — [zeroconf.md](zeroconf.md) doc + 10-row lint checks matrix + 5 benchmarks (lint ~380 ns, synthesiser ~2 μs) + `FuzzParse` fuzz target (TOML config round-trip) + threat note (literal secrets, admin exposure, weak TLS, lint bypass) landed | S–M | ✅ |
| Compression (gzip; brotli/zstd) (Y1-02) | **GA** | none — soaked 1h 2026-07-04 (11.6M req, 0% err, zstd/br/gzip verified) + Phase 2A — all criteria met ✅ | M | ✅ |
| NGINX config importer (Y1-09) | **GA** | none — [nginx-importer.md](nginx-importer.md) doc + full directive-support matrix (top-level, http, server, location, upstream, modifiers) + 2 benchmarks (`BenchmarkParse` ~45 μs, `BenchmarkTranslate` ~6.5 μs) + `FuzzTranslate` fuzz target for parse+translate+marshal round-trip, 9-item known-limitations list, and 6-row threat note (craft-conf crash, path traversal, credential leak, info disclosure, translation misconfig, dependency trust). Evidence bundle closes remaining gaps ①②③⑥⑦⑧. Added to soak-tracking table. | M | ✅ |
| Response cache (memory + disk) | **GA** | none — soaked 1h 2026-07-04 (1.5M req, 0% err, hit/miss/evict/revalidate verified) + Phase 2A — all criteria met ✅ | M | ✅ |
| OTel tracing + access-log sinks (Y1-10) | **GA** | none — soaked via Phase 2A 2026-07-05 (2.12M req, 0% err, traceparent observed) — all criteria met ✅ | M | ✅ |
| HTTP/3 over QUIC (Y1-11) | **GA** | none — queued at v1.29.0 and later completed with an 8h isolated Linux soak on 2026-07-13 (55,302,486 req, 0% err, 100% success, QUIC+TLS on `:8443`) — all criteria met ✅ | M | ✅ |
| WASM plugin system (Y2-02) | **GA** | none — **8h Linux soak completed 2026-07-16** (21.7M+ requests at ~10K–20K req/s, 0 missing plugin headers; 2026-07-12 prior soak superseded as non-representative at ~1 req/s) — all criteria met ✅ | M | ✅ |
| L4 stream proxy (Y2-03) | **GA** | none — queued at v1.29.0 and later completed with an 8h isolated Linux soak on 2026-07-11 (`TestSoakUDPChurn`, 54,892,354 sends, 0% err, bounded goroutines/heap) — all criteria met ✅ | M | ✅ |

> - **20 shipped features are GA with completed soak evidence. 0 remain GA — soak pending.**
>
> **Phase 2 soaks (2026-07-06 / 2026-07-07 / 2026-07-13):**
> - **gRPC transcoding (Y2-01)**: 1h isolated soak (14.2M req, 0.000007% err, REST/JSON → gRPC) — promoted to GA
> - **gRPC passthrough (Y2-04)**: 1h isolated soak (6.8M req, 0.0002% err, native gRPC/h2c) — promoted to GA
> - **HTTP/3 over QUIC (Y1-11)**: 8h isolated Linux soak (55,302,486 req, 0% err, QUIC+TLS) — promoted to GA
> - **L4 stream proxy (Y2-03)**: 8h isolated Linux soak (54,892,354 sends, 0% err, UDP churn) — promoted to GA
> - **WASM plugins (Y2-02)**: 8h isolated Linux soak (33,428 successful requests, 0% err, plugin execution preserved) — promoted to GA
>
> **Phase 2A consolidated soak (2026-07-06):**
> - **8 hours, 5.05M requests, 0% errors** — proxy + auth + cache + rate-limit +
>   WAF + compression + TLS + mTLS + health-checks + OTel + service discovery +
>   secrets + WASM exercised simultaneously
>
> **All shipped features are GA. Soak gate is CLOSED for the entire v1 feature set.**
## Soak tracking (post-GA gate per ADR 0005)

A soak failure is a release-blocking regression. Dated soak runs and artifacts are recorded in the [soak evidence log](soak-evidence.md).
