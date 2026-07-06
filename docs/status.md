# Jul.IA — Feature status & GA matrix

> Version 1.30 · Updated 2026-07-06

The single, canonical at-a-glance view of **every shipped feature**, its
**maturity**, and how it stands against the nine-criteria GA bar
([ADR 0003](adr/0003-maturity-and-ga.md)).

**Keep this current.** When a feature's maturity or any GA criterion changes,
update this file — it is step 2 of the roadmap
[Maintenance](roadmap/README.md#maintenance) checklist. Other documents point
here as the source of truth, so the per-doc tables cannot drift apart (the
condition that left Y2-07 mTLS listed as "remaining" after it had already
shipped).

Maturity ladder: **Alpha · Beta · GA — soak pending · GA · Deprecated**. Per
[ADR 0005](adr/0005-soak-post-ga-gate.md) the long-running soak test (criterion 5)
is a **post-GA gate**, so a feature that meets the other eight criteria is
labelled **GA — soak pending** until its soak run completes.

## GA criteria legend

| # | Criterion |
| --- | --- |
| 1 | Conformance / behaviour matrix |
| 2 | Published benchmark numbers |
| 3 | Known-limitations list |
| 4 | Semver-guarded config/API contract |
| 5 | Long-running soak test (**post-GA gate**) |
| 6 | Runnable example + docs |
| 7 | Security / threat note |
| 8 | Fuzzing where parsing is involved |
| 9 | Self-explanatory Console surface |

Cell key: ✅ met · ☐ open · n/a not applicable (no custom parser).

## GA

All nine criteria met, including the post-GA soak gate in
[ADR 0005](adr/0005-soak-post-ga-gate.md). A soak failure on a GA feature is a
release-blocking regression, not a reason to retract the label.

| Feature | ID | Tag | 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 | 9 | Doc |
| --- | --- | --- | :-: | :-: | :-: | :-: | :-: | :-: | :-: | :-: | :-: | --- |
| TLS + automatic HTTPS | Y1-01 | core · `acme` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | n/a | ✅ | [tls-acme.md](tls-acme.md) |
| Authentication (CIDR/Basic/JWT/forward-auth) | Y1-04 | core | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | [auth.md](auth.md) |
| mTLS client auth + `$ssl_client_*` | Y2-07 | core | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | n/a | ✅ | [mtls.md](mtls.md) |
| Web application firewall (WAF) | Y2-06 | `waf` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | n/a | ✅ | [waf.md](waf.md) |
| Rate + connection limiting | Y1-03 | core | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | n/a | ✅ | [ratelimit.md](ratelimit.md) |
| OTel tracing + access-log sinks | Y1-10 | `otel` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | n/a | n/a | [otel.md](otel.md) |
| Response cache (memory + disk) | — | core | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | n/a | ✅ | [cache.md](cache.md) |
| Core HTTP (static/proxy/FastCGI/vhosts/routing) | — | core | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | [core-http.md](core-http.md) |
| Console (operations cockpit) | Y1-07 · Y2-09 | `console` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | n/a | ✅ | [console.md](console.md) |
| Active health checks (HTTP/TCP probes) | Y1-05 | core | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | n/a | ✅ | [health.md](health.md) |
| Compression (gzip / Brotli / Zstd) | Y1-02 | `brotli`,`zstd` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | n/a | ✅ | [compression.md](compression.md) |

Criterion 8 is **n/a** for features whose parsing/validation is delegated to a
standard or third-party library — see the GA — soak pending section for the
rationale.

## GA — soak pending

Eight criteria met (1–4, 6–9); only the soak test (criterion 5) is open.
Per-feature detail lives in each linked doc's *GA status* table.

| Feature | ID | Tag | 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 | 9 | Doc |
| --- | --- | --- | :-: | :-: | :-: | :-: | :-: | :-: | :-: | :-: | :-: | --- |
| gRPC ↔ JSON transcoding | Y2-01 | `grpc` | ✅ | ✅ | ✅ | ✅ | ☐ | ✅ | ✅ | ✅ | ✅ | [grpc-transcoding.md](grpc-transcoding.md) |
| Native gRPC passthrough + h2c | Y2-04 | `grpc` | ✅ | ✅ | ✅ | ✅ | ☐ | ✅ | ✅ | n/a | ✅ | [grpc-proxy.md](grpc-proxy.md) |
| Service discovery / dynamic upstreams | Y2-05 | `consul`,`kubernetes` | ✅ | ✅ | ✅ | ✅ | ☐ | ✅ | ✅ | n/a | ✅ | [service-discovery.md](service-discovery.md) |
| HTTP/3 over QUIC | Y1-11 | `http3` | ✅ | ✅ | ✅ | ✅ | ☐ | ✅ | ✅ | n/a | ✅ | [http3.md](http3.md) |
| WASM plugin system | Y2-02 | `wasmplugins` | ✅ | ✅ | ✅ | ✅ | ☐ | ✅ | ✅ | ✅ | ✅ | [plugins.md](plugins.md) |
| L4 stream proxy | Y2-03 | `stream` | ✅ | ✅ | ✅ | ✅ | ☐ | ✅ | ✅ | ✅ | ✅ | [stream.md](stream.md) |
| Secrets references + log redaction | SEC-1 | `secrets` | ✅ | ✅ | ✅ | ✅ | ☐ | ✅ | ✅ | n/a | ✅ | [secrets.md](secrets.md) |
| Zero-config + `jul lint` | Y1-08 | core | ✅ | ✅ | ✅ | ✅ | ☐ | ✅ | ✅ | ✅ | n/a | [zeroconf.md](zeroconf.md) |
| NGINX config importer | Y1-09 | `importer` | ✅ | ✅ | ✅ | ✅ | ☐ | ✅ | ✅ | ✅ | n/a | [nginx-importer.md](nginx-importer.md) |

## Soak tracking (post-GA gate)

Criterion 5 for the GA — soak pending features. A soak failure is a
release-blocking regression. Mirrors the
[GA push soak table](ga-push.md#soak-tracking-post-ga-gate-per-adr-0005).
Dated soak runs and where the CI/release artifacts are published are recorded in
the [soak evidence log](soak-evidence.md).

| Feature | GA on | Soak status |
| --- | --- | --- |
| Core HTTP | 2026-06-21 | ✅ soaked 8h windows 2026-07-04 (90.4M req, 0% err) — [evidence](soak-evidence.md#2026-07-04--track-2-extended-burn-in-local-windows-8-hours-50-workers) |
| TLS + automatic HTTPS (Y1-01) | 2026-06-21 | ✅ soaked via Phase 2A 8h windows 2026-07-05 (2.12M req, 0% err, 25% TLS traffic mix) — [evidence](soak-evidence.md#2026-07-05--phase-2a-consolidated-burn-in-completed-local-8-hours-50-workers-all-features) |
| Authentication (Y1-04) | 2026-06-21 | ✅ soaked 1h windows 2026-07-04 (929K req, 0% err, pprof-verified) — [evidence](soak-evidence.md#2026-07-04--auth-soak-local-windows-1-hour-50-workers) |
| gRPC ↔ JSON transcoding (Y2-01) | 2026-06-21 | ⏳ soak pending (v1.29.0 release-gate queued) |
| Native gRPC passthrough (Y2-04) | 2026-06-21 | ⏳ soak pending (v1.29.0 release-gate queued) |
| mTLS client auth (Y2-07) | 2026-06-21 | ✅ soaked via Phase 2A 8h windows 2026-07-05 (2.12M req, 0% err, TLS + client-cert auth path exercised) — [evidence](soak-evidence.md#2026-07-05--phase-2a-consolidated-burn-in-completed-local-8-hours-50-workers-all-features) |
| Console (Y1-07 · Y2-09) | 2026-06-23 | ✅ soaked 8h windows 2026-07-04 (console tag built, dashboard reachable) — [evidence](soak-evidence.md#2026-07-04--track-2-extended-burn-in-local-windows-8-hours-50-workers) |
| Active health checks (Y1-05) | 2026-07-03 | ✅ soaked 8h windows 2026-07-04 (/healthz polled 960×, all 200) — [evidence](soak-evidence.md#2026-07-04--track-2-extended-burn-in-local-windows-8-hours-50-workers) |
| Web application firewall (WAF) (Y2-06) | 2026-07-03 | ✅ soaked 1h windows 2026-07-04 (1.67M req, 0% err, CRS block mode verified) — [evidence](soak-evidence.md#2026-07-04--waf-soak-local-windows-1-hour-50-workers) |
| Service discovery / dynamic upstreams (Y2-05) | 2026-07-03 | ⏳ soak pending (v1.29.0 release-gate queued) |
| Secrets references + log redaction (SEC-1) | 2026-07-03 | ⏳ soak pending (v1.29.0 release-gate queued) |
| Rate + connection limiting (Y1-03) | 2026-07-03 | ✅ soaked 1h windows 2026-07-04 (12.5M req, 0% err, token-bucket allow/reject verified) — [evidence](soak-evidence.md#2026-07-04--rate-limit-soak-local-windows-1-hour-50-workers) |
| Zero-config + `jul lint` (Y1-08) | 2026-07-03 | ⏳ soak pending (v1.29.0 release-gate queued) |
| Compression (Y1-02) | 2026-07-03 | ✅ soaked 1h windows 2026-07-04 (11.6M req, 0% err, zstd/br/gzip verified) — [evidence](soak-evidence.md#2026-07-04--compression-soak-local-windows-1-hour-50-workers) |
| **Phase 2A consolidated** (proxy+cache+rate-limit+WAF+auth+compression+TLS+mTLS+health+OTel) | 2026-07-05 | ✅ **soaked 8h** windows 2026-07-05 (2.12M req, 0% err, 100% success, all 10 features simultaneously) — [evidence](soak-evidence.md#2026-07-05--phase-2a-consolidated-burn-in-completed-local-8-hours-50-workers-all-features) |
| NGINX config importer (Y1-09) | 2026-07-03 | ⏳ soak pending (v1.29.0 release-gate queued) |
| OTel tracing + access-log sinks (Y1-10) | 2026-07-03 | ✅ soaked via Phase 2A 8h windows 2026-07-05 (2.12M req, 0% err, W3C traceparent observed in telemetry) — [evidence](soak-evidence.md#2026-07-05--phase-2a-consolidated-burn-in-completed-local-8-hours-50-workers-all-features) |
| Response cache (memory + disk) | 2026-07-03 | ✅ soaked 1h windows 2026-07-04 (1.5M req, 0% err, hit/miss/evict/revalidate verified) — [evidence](soak-evidence.md#2026-07-04--cache-soak-local-windows-1-hour-50-workers) |
| HTTP/3 over QUIC (Y1-11) | 2026-07-03 | ☐ soak queued on v1.29.0 tag (CI running) |
| WASM plugin system (Y2-02) | 2026-07-03 | ☐ soak queued on v1.29.0 tag (CI running) |
| L4 stream proxy (Y2-03) | 2026-07-03 | ☐ soak queued on v1.29.0 tag (CI running, udp-churn scenario) |


## Recently shipped continuous panels

**Y2-09 Console v2 continuous panels** (tracked above as part of the **Console**
row, Y1-07 · Y2-09):

| Panel | Status | Note |
| --- | --- | --- |
| Live log tail | **Shipped** | Operations panel, SSE stream, filter/pause |
| WASM plugin manager | **Shipped** | Plugins panel, structured CRUD + attach/detach; `.wasm` file upload included |
| gRPC route designer | **Shipped** | Descriptor upload (inspection only), `google.api.http` parse, visual mapping editor |

Deferred / demand-gated: **Y2-08** GraphQL composition. Time-boxed bet:
**AI-MVP** AI Gateway. Years 3–5 are the demand-gated vision horizon. See the
[roadmap](roadmap/README.md) for the full plan.

## Beta (shipped; remaining GA gaps)

> **All shipped features are now GA — soak pending.** This section is retired.
> See the GA — soak pending table above for the canonical maturity matrix, and
> [ga-push.md](ga-push.md) for the per-feature push plan.

## Changelog

| Date | Ver | What changed | Source |
| --- | --- | --- | --- |
| 2026-07-04 | 1.30 | **Auth soak completed:** HTTP Basic auth path soaked 1h Windows local (929K req, 0% err). pprof endpoints added to admin server behind auth; load generator updated with `-authUser`/`-authPassword`/`-compress` flags. Evidence logged in `docs/soak-evidence.md`. | [soak-evidence.md](soak-evidence.md), [status.md](status.md) |
| 2026-07-04 | 1.30 | **Compression soak completed:** gzip/brotli/zstd paths soaked 1h Windows local (11.6M req, ~3,235 rps, 0% err). zstd encoder pool allocations (~48 MiB) verified as legitimate library pre-allocation, not a leak. Evidence logged. | [soak-evidence.md](soak-evidence.md), [status.md](status.md) |
| 2026-07-04 | 1.30 | **Cache soak completed:** response cache (memory + disk) soaked 1h Windows local (1.5M req, 0% err). Hit/miss/evict/revalidate paths exercised with 8 MB memory cap + 10 s TTL. pprof-verified: 16 goroutines, ~1.6 MiB heap. Evidence logged. | [soak-evidence.md](soak-evidence.md), [status.md](status.md) |
| 2026-07-04 | 1.30 | **Phase 2A consolidated burn-in started:** 8-hour soak test running with ALL 10 features simultaneously (proxy, cache, rate-limit, WAF, auth, compression, TLS, mTLS, upstream health-checks, OTel tracing). 5-min pilot: 29,587 req, 0% err. Bugfixes landed: compression auto-enable when block present; OTel semconv `v1.39.0` → `v1.41.0` schema-URL fix. Evidence tracked in `docs/soak-evidence.md`. | [soak-evidence.md](soak-evidence.md), [status.md](status.md), `internal/config/parser.go`, `internal/observability/tracing.go` |
| 2026-07-04 | 1.30 | **pprof support added to admin server:** `net/http/pprof` mounted at `/debug/pprof/` behind existing bearer-token auth, enabling authenticated goroutine/heap snapshots during soak tests. | `internal/admin/server.go` |
| 2026-07-03 | 1.29 | **Beta → GA — soak pending:** SEC-1 Secrets references (`env`/`file`/`secret` refs + log redaction + lint) reaches GA — soak pending. Evidence: 12-row behaviour matrix, 5 redaction benchmarks (0-allocation miss path), 8-row threat note (VCS leak, log exposure, short-secret floor, env/file permissions). | [secrets.md](secrets.md), [status.md](status.md) |
| 2026-07-03 | 1.29 | **Beta → GA — soak pending:** Y1-03 Rate + connection limiting reaches GA — soak pending. Evidence: 12-row behaviour matrix (key strategies, scope rules, eviction), 4 rate-limiter benchmarks (~300 ns Allow), threat note (IP spoofing, key collision, slowloris, bypass). | [ratelimit.md](ratelimit.md), [status.md](status.md) |
| 2026-07-03 | 1.29 | **Beta → GA — soak pending:** Y1-08 Zero-config + `jul lint` reaches GA — soak pending. Evidence: 10-row lint checks matrix, 5 benchmarks (lint ~380ns, synthesiser ~2μs), `FuzzParse` fuzz target for TOML config round-trip, threat note (literal secrets, admin exposure, weak TLS, lint bypass). | [zeroconf.md](zeroconf.md), [status.md](status.md) |
| 2026-07-03 | 1.29 | **Beta → GA — soak pending:** Y1-02 Response compression (gzip/brotli/zstd) reaches GA — soak pending. Evidence: 3-encoder matrix, 4 benchmarks (pass-through ~7μs, small gzip ~49μs, large gzip ~306μs), 6-row threat note (BREACH, CRIME, compression bomb, cache poisoning, sidecar leak, CPU exhaustion). | [compression.md](compression.md), [status.md](status.md) |
| 2026-07-03 | 1.29 | **Beta → GA — soak pending:** Y1-09 NGINX config importer reaches GA — soak pending. Evidence: directive-support matrix (top-level, http, server, location, upstream, modifiers), 2 benchmarks (`BenchmarkParse` ~45 μs, `BenchmarkTranslate` ~6.5 μs), 9-item limitation list, 6-row threat note (craft-conf crash, path traversal, credential leak, info disclosure, translation misconfig, dependency trust), `FuzzTranslate` covering parse+translate+marshal round-trip. | [nginx-importer.md](nginx-importer.md), [status.md](status.md) |
| 2026-07-03 | 1.29 | **Beta → GA — soak pending:** Response cache (memory + disk) reaches GA — soak pending. Evidence: 14-row behaviour matrix (key, Vary, TTL, status codes, conditional requests, eviction), 4 benchmarks (`BenchmarkCacheHit` ~2.4 μs, `Miss` ~10.6 μs, `VaryHit` ~2.9 μs, `MemOverflow` ~4.4 ms), 4-item limitation list, and 6-row threat note (Host poisoning, Vary leakage, Web Cache Deception, SIF DoS, disk PII, header smuggling). Evidence bundle closes remaining gaps ①②③⑥⑦. Added to soak-tracking table. | [cache.md](cache.md), [status.md](status.md) |
| 2026-07-03 | 1.29 | **OTel tracing + access-log sinks (Y1-10) → GA — soak pending.** Published [otel.md](otel.md) with exporter/sink matrix (OTLP-gRPC/HTTP, span types, W3C propagation, access-log sinks/fields), 5 benchmarks (middleware ~10.4 μs, seam child span ~2.5 μs, no-op seam ~20 ns), 4-item limitation list, and 5-row PII threat note (URL tokens, trace id linking, file leakage, insecure collector, error disclosure). Evidence bundle closes remaining gaps ①②③⑥⑦. Added to soak-tracking table. | [otel.md](otel.md), [observability.md](observability.md), [status.md](status.md) |
| 2026-07-03 | 1.29 | **Beta → GA — soak pending:** Y1-11 HTTP/3 over QUIC reaches GA — soak pending. Evidence: QUIC/Alt-Svc behaviour matrix (protocol negotiation, build-time, defaults), `BenchmarkHTTP3Throughput` (~259 μs/op, 13.9 KB/op), 4-item limitation list (no WebSocket, restart required for Alt-Svc, UDP port sharing, bind-time handler generation), 5-row threat note (0-RTT replay, UDP amplification, UDP exhaustion, cert sharing, Alt-Svc tracking). Evidence bundle closes remaining gaps ①②③⑥⑦⑧. Added to soak-tracking table. | [http3.md](http3.md), [status.md](status.md) |
| 2026-07-03 | 1.29 | **Beta → GA — soak pending:** Y2-02 WASM plugins reaches GA — soak pending. Evidence: 19-row behaviour matrix (ABI boundary, guest containment, reload, fetch/KV guards, KV quotas), 5 benchmarks (middleware ~16.5 μs, handler ~20 μs, KV ~23 μs, parallel ~3.4 μs amortised), 5-item limitation list (request-phase only, no shared state, no streaming, one ABI, build-tag required), 7-row threat note (memory escape, CPU exhaustion, SSRF, KV DoS, upload, info leak, ABI breakage), `FuzzPluginInvoke` and `FuzzHostAllowed` in `internal/plugins/fuzz_test.go`. Evidence bundle closes remaining gaps ①②③⑥⑦⑧. Added to soak-tracking table. | [plugins.md](plugins.md), [status.md](status.md) |
| 2026-07-03 | 1.28 | **Beta → GA — soak pending:** Y2-05 Service discovery and Y2-06 WAF reach GA — soak pending. Evidence: provider matrices, balancer benchmarks, threat notes. | [service-discovery.md](service-discovery.md), [waf.md](waf.md), [status.md](status.md) |
| 2026-07-03 | 1.28 | **Beta → GA — soak pending:** Y1-05 Active health checks reaches GA — soak pending. Evidence: probe conformance matrix, threshold/limitation docs, balancer benchmarks. | [health.md](health.md), [status.md](status.md) |
| 2026-06-30 | 1.27 | **Console continuous panels status correction:** the Console v2 Y2-09 remaining-work note is updated to show that live log tail, the WASM plugin manager, the gRPC route designer, and the `.wasm` upload incremental feature are all shipped. The roadmap `Not yet shipped` wording is corrected from "none" to an explicit panel table so the source of truth does not drift. | [status.md](status.md), [roadmap/README.md](roadmap/README.md) |
| 2026-06-29 | 1.26 | **gRPC transcoding upstream pools + plugin fetch truncation + re-audit residuals:** upstream pool support for `grpc_transcode` targets; plugin fetch response truncation guard; documentation and validation fixes from external audit round. | [status.md](status.md) |
| 2026-07-05 | 1.30 | **Phase 2A soak completed + mass promotion to GA.** Consolidated 10-feature 8h burn-in finished (2.12M req, 0% err, 100% success) → mass-closes soak for TLS, mTLS, and OTel tracing in status + ga-push. All promoted rows now ✅. | [soak-evidence.md](soak-evidence.md), [status.md](status.md), [ga-push.md](ga-push.md) |
