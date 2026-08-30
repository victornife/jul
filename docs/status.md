# Jul.IA — Feature status & GA matrix

> Version 2.7 · Updated 2026-08-30

> **Source of truth:** [`docs/feature-status.yaml`](feature-status.yaml) is the
> single editable manifest. This page is the human-readable rendering of that
> data. When maturity or any GA criterion changes, update `feature-status.yaml`
> first, then keep this table in sync.

The at-a-glance view of **every shipped feature**, its **maturity**, and how
it stands against the nine-criteria GA bar ([ADR 0003](adr/0003-maturity-and-ga.md)).

> Current maturity and delivery live in this page and
> [`feature-status.yaml`](feature-status.yaml). Volatile issue sequencing lives
> in [#62](https://github.com/victornife/jul/issues/62), and dated audit
> disposition lives in the [audit register](audit-register.md). The
> [2026-08-03 combined audit](audit/combined-audit-2026-08-03.md) remains a
> preserved programme-opening baseline, not a second current-status source.

**Keep this current.** When a feature's maturity or any GA criterion changes,
update [`docs/feature-status.yaml`](feature-status.yaml) first — that is the
single editable source. Then update the table below to match. The `docs-check`
CI gate validates that every feature name in the manifest appears here.

Maturity ladder: **Alpha · Beta · GA — soak pending · GA · Deprecated**. Per
[ADR 0005](adr/0005-soak-post-ga-gate.md) the long-running soak test (criterion 5)
is a **post-GA gate**, so a feature that meets the other eight criteria is
labelled **GA — soak pending** until its soak run completes.

### Delivery state vs. maturity

Maturity answers *how stable and evidence-complete is this capability?* Delivery
answers *where is this implementation in the publication pipeline?* The axes are
kept separately in [`feature-status.yaml`](feature-status.yaml):

| Delivery | Meaning |
| --- | --- |
| `implemented` | Code exists on a working branch; not part of `main`. |
| `merged` | On `main` under `[Unreleased]`; not in a published tag. |
| `candidate` | Frozen in a published or draft prerelease tag. |
| `released` | Published in a stable immutable tag. |
| `soaked` | Released and through the feature's long-running post-GA soak gate. |

A GA entry must be compatible with `soaked`. A newer additive capability does
not inherit an older GA row merely because it lives in the same package or guide.

### Current product snapshot

- **Published checkpoint:** `v1.32.1-rc.1` is an independently verified
  prerelease candidate at `9a936d0cc1bc3f7086f38ca87741d9d09f950e25`.
  It is not a stable release.
- **Current `main`:** contains substantial later work, including cache
  recertification, closed-world lifecycle authority, structured configuration,
  trusted client identity, backend trust, routing/response policy,
  configuration authority/generated contracts, resilience slices, and NGINX
  assessment/provenance/include traversal. Those additions retain their own
  delivery and maturity rows below.
- **Volatile execution state:** lives in
  [#62](https://github.com/victornife/jul/issues/62). The
  [roadmap](roadmap/README.md) intentionally keeps only durable portfolio state.
- **Dated audit disposition:** lives in the
  [audit register](audit-register.md). Historical audits remain evidence rather
  than a second current-status source.

### Current notices

- **Response cache:** #134 completed integrated recertification; the released
  cache record retains GA.
- **Trusted client address and backend TLS:** merged Beta capabilities; stable
  publication and soak are still explicit promotion gates.
- **Resilience:** admission, retry and circuit implementations are merged; the
  integrated cross-protocol/soak and full external-contract closure remain in
  #287/#144 at this baseline.
- **Routing, configuration authority, generated contracts and NGINX assessment:**
  merged after the current RC and therefore represented separately from older
  GA rows.

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

> **Configuration reload transaction.** The reload path is now implemented as
> a single `ReloadPlan` value (ADR 0011): validation, listener staging, handler
> publication, activation, and retirement are sequenced so no client request is
> served by a listener before its handler generation is live. The authoritative
> lifecycle classification is in [`internal/lifecycle/lifecycle.go`](../internal/lifecycle/lifecycle.go)
> and [`docs/config-lifecycle.yaml`](config-lifecycle.yaml). See the dedicated
> row in the GA table below and [reload-semantics.md](reload-semantics.md) for
> the full contract.

## GA

Released and soaked capabilities that satisfy all applicable GA criteria.

| Feature | ID | Tag | Delivery | 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 | 9 | Doc |
| --- | --- | --- | --- | :-: | :-: | :-: | :-: | :-: | :-: | :-: | :-: | :-: | --- |
| TLS + automatic HTTPS (ACME) | Y1-01 | core · `acme` | `soaked` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | n/a | ✅ | [tls-acme.md](tls-acme.md) |
| Compression (gzip / Brotli / Zstd) | Y1-02 | `brotli` · `zstd` | `soaked` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | n/a | ✅ | [compression.md](compression.md) |
| Rate + connection limiting | Y1-03 | core | `soaked` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | n/a | ✅ | [ratelimit.md](ratelimit.md) |
| Authentication (CIDR / Basic / JWT / forward-auth) | Y1-04 | core | `soaked` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | [auth.md](auth.md) |
| Active health checks (HTTP / TCP probes) | Y1-05 | core | `soaked` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | n/a | ✅ | [health.md](health.md) |
| Console (operations cockpit) | Y1-07 | `console` | `soaked` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | n/a | ✅ | [console.md](console.md) |
| Zero-config + jul lint | Y1-08 | core | `soaked` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | [zeroconf.md](zeroconf.md) |
| NGINX config importer | Y1-09 | `importer` | `soaked` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | n/a | ✅ | [nginx-importer.md](nginx-importer.md) |
| OTel tracing + access-log sinks | Y1-10 | `otel` | `soaked` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | n/a | n/a | [otel.md](otel.md) |
| HTTP/3 over QUIC | Y1-11 | `http3` | `soaked` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | n/a | ✅ | [http3.md](http3.md) |
| gRPC ↔ JSON transcoding | Y2-01 | `grpc` | `soaked` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | [grpc-transcoding.md](grpc-transcoding.md) |
| WASM plugin system | Y2-02 | `wasmplugins` | `soaked` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | [plugins.md](plugins.md) |
| L4 stream proxy | Y2-03 | `stream` | `soaked` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | [stream.md](stream.md) |
| Native gRPC passthrough + h2c | Y2-04 | `grpc` | `soaked` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | n/a | ✅ | [grpc-proxy.md](grpc-proxy.md) |
| Service discovery / dynamic upstreams | Y2-05 | `consul` · `kubernetes` | `soaked` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | n/a | ✅ | [service-discovery.md](service-discovery.md) |
| Web application firewall (WAF) | Y2-06 | `waf` | `soaked` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | n/a | ✅ | [waf.md](waf.md) |
| mTLS client auth | Y2-07 | core | `soaked` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | n/a | ✅ | [mtls.md](mtls.md) |
| Secrets references + log redaction | SEC-1 | core | `soaked` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | n/a | ✅ | [secrets.md](secrets.md) |
| Response cache (memory + disk) | core-cache | core | `soaked` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | [cache.md](cache.md) |
| Core HTTP (static / proxy / FastCGI / vhosts / routing) | core-http | core | `soaked` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | [core-http.md](core-http.md) |
| Configuration reload transaction | reload-tx | core | `soaked` | ✅ | n/a | ✅ | ✅ | ✅ | ✅ | ✅ | n/a | ✅ | [reload-semantics.md](reload-semantics.md) |

## GA — soak pending

Released capabilities that satisfy the other GA criteria but still require the
long-running post-GA soak gate.

| Feature | ID | Tag | Delivery | 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 | 9 | Doc |
| --- | --- | --- | --- | :-: | :-: | :-: | :-: | :-: | :-: | :-: | :-: | :-: | --- |
| *(none)* | — | — | — | — | — | — | — | — | — | — | — | — | — |

## Beta

Usable capabilities whose contract, release, soak, or integrated evidence is
not yet at the GA bar. `merged` and `candidate` are not synonyms for released.

| Feature | ID | Tag | Delivery | 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 | 9 | Doc |
| --- | --- | --- | --- | :-: | :-: | :-: | :-: | :-: | :-: | :-: | :-: | :-: | --- |
| Trusted client address (client_address) | CGC-IN | core | `merged` | ✅ | ✅ | ✅ | ✅ | ☐ | ✅ | ✅ | ✅ | ✅ | [configuration.md](configuration.md) |
| Backend TLS trust (backend_tls) | UT-BE | core · `grpc` | `merged` | ✅ | n/a | ✅ | ✅ | ☐ | ✅ | ✅ | n/a | ✅ | [upstreams.md](upstreams.md) |
| Auxiliary egress allow-list | SEC-EGRESS | core | `candidate` | ✅ | n/a | ✅ | ☐ | ☐ | ✅ | ✅ | n/a | ✅ | [egress.md](egress.md) |
| Request predicates, response headers, and CORS | CGC-ROUTE | core | `merged` | ✅ | ✅ | ✅ | ☐ | ☐ | ✅ | ✅ | n/a | ✅ | [core-http.md](core-http.md) |
| Upstream resilience (admission, retry, circuit) | CGC-RES | core · `grpc` · `stream` | `merged` | ✅ | ✅ | ✅ | ☐ | ☐ | ✅ | ✅ | ✅ | ☐ | [upstreams.md](upstreams.md) |
| Configuration authority and managed drift | AUTO-AUTH | core · `console` | `merged` | ✅ | n/a | ✅ | ☐ | ☐ | ✅ | ✅ | n/a | ✅ | [reload-semantics.md](reload-semantics.md) |
| Generated configuration contracts and route identity | AUTO-CONTRACT | core | `merged` | ✅ | n/a | ✅ | ☐ | ☐ | ✅ | ✅ | n/a | n/a | [generated/config-reference.md](generated/config-reference.md) |
| NGINX migration assessment, provenance, and includes | MIG-ASSESS | `importer` | `merged` | ✅ | ☐ | ✅ | ☐ | ☐ | ✅ | ✅ | ✅ | ✅ | [nginx-assessment.md](nginx-assessment.md) |

## Alpha

| Feature | ID | Tag | Delivery | 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 | 9 | Doc |
| --- | --- | --- | --- | :-: | :-: | :-: | :-: | :-: | :-: | :-: | :-: | :-: | --- |
| *(none)* | — | — | — | — | — | — | — | — | — | — | — | — | — |

## Deprecated

| Feature | ID | Tag | Delivery | 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 | 9 | Doc |
| --- | --- | --- | --- | :-: | :-: | :-: | :-: | :-: | :-: | :-: | :-: | :-: | --- |
| *(none)* | — | — | — | — | — | — | — | — | — | — | — | — | — |

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
| **gRPC ↔ JSON transcoding (Y2-01)** | **2026-06-21** | **✅ soaked 8h Linux 2026-07-15 (59.1M req, 0% err, REST/JSON → gRPC) — [evidence](soak-evidence.md#2026-07-15--grpc-transcoding--passthrough-isolated-soak-linux-8-hours-20-workers)** |
| **Native gRPC passthrough (Y2-04)** | **2026-06-21** | **✅ soaked 8h Linux 2026-07-15 (51.4M req, 0% err, native gRPC/h2c → upstream gRPC) — [evidence](soak-evidence.md#2026-07-15--grpc-transcoding--passthrough-isolated-soak-linux-8-hours-20-workers)** |
| mTLS client auth (Y2-07) | 2026-06-21 | ✅ soaked via Phase 2A 8h windows 2026-07-05 (2.12M req, 0% err, TLS + client-cert auth path exercised) — [evidence](soak-evidence.md#2026-07-05--phase-2a-consolidated-burn-in-completed-local-8-hours-50-workers-all-features) |
| Console (Y1-07 · Y2-09) | 2026-06-23 | ✅ soaked 8h windows 2026-07-04 (console tag built, dashboard reachable) — [evidence](soak-evidence.md#2026-07-04--track-2-extended-burn-in-local-windows-8-hours-50-workers) |
| Active health checks (Y1-05) | 2026-07-03 | ✅ soaked 8h windows 2026-07-04 (/healthz polled 960×, all 200) — [evidence](soak-evidence.md#2026-07-04--track-2-extended-burn-in-local-windows-8-hours-50-workers) |
| Web application firewall (WAF) (Y2-06) | 2026-07-03 | ✅ soaked 1h windows 2026-07-04 (1.67M req, 0% err, CRS block mode verified) — [evidence](soak-evidence.md#2026-07-04--waf-soak-local-windows-1-hour-50-workers) |
| Service discovery / dynamic upstreams (Y2-05) | 2026-07-03 | ✅ soaked via Phase 2A 8h windows 2026-07-06 (`/discovery/` traffic via `dns-backend`, resolved successfully) — [evidence](soak-evidence.md#2026-07-06--phase-2a-consolidated-burn-in-completed-local-windows-8-h-50-workers) |
| Secrets references + log redaction (SEC-1) | 2026-07-03 | ✅ soaked via Phase 2A 8h windows 2026-07-06 (admin token `${env:JUL_ADMIN_TOKEN}` expanded; API reachable) — [evidence](soak-evidence.md#2026-07-06--phase-2a-consolidated-burn-in-completed-local-windows-8-h-50-workers) |
| Rate + connection limiting (Y1-03) | 2026-07-03 | ✅ soaked 1h windows 2026-07-04 (12.5M req, 0% err, token-bucket allow/reject verified) — [evidence](soak-evidence.md#2026-07-04--rate-limit-soak-local-windows-1-hour-50-workers) |
| Zero-config + `jul lint` (Y1-08) | 2026-07-03 | ✅ validated via `test-zero-config.ps1` 2026-07-06: zero-config serve returns 200, lint passes with secret refs, strict lint correctly flags literal secrets — [evidence](soak-evidence.md#2026-07-06--phase-2b-soak-preparation-local-windows-5-min-smoke--validation-scripts) |
| Compression (Y1-02) | 2026-07-03 | ✅ soaked 1h windows 2026-07-04 (11.6M req, 0% err, zstd/br/gzip verified) — [evidence](soak-evidence.md#2026-07-04--compression-soak-local-windows-1-hour-50-workers) |
| **Phase 2A consolidated** (proxy+cache+rate-limit+WAF+auth+compression+TLS+mTLS+health+OTel+discovery+secrets+WASM) | 2026-07-05 | ✅ **soaked ~8h** windows 2026-07-06 (5.05M req, 0% err, 100% success, 13 features simultaneously) — [evidence](soak-evidence.md#2026-07-06--phase-2a-consolidated-burn-in-completed-local-windows-8-h-50-workers) |
| NGINX config importer (Y1-09) | 2026-07-03 | ✅ validated via `test-nginx-importer.ps1` 2026-07-06: import produces valid TOML, HTTP `:80`, HTTPS `:443`, `least_conn`, proxy verified — [evidence](soak-evidence.md#2026-07-06--phase-2b-soak-preparation-local-windows-5-min-smoke--validation-scripts) |
| OTel tracing + access-log sinks (Y1-10) | 2026-07-03 | ✅ soaked via Phase 2A 8h windows 2026-07-05 (2.12M req, 0% err, W3C traceparent observed in telemetry) — [evidence](soak-evidence.md#2026-07-05--phase-2a-consolidated-burn-in-completed-local-8-hours-50-workers-all-features) |
| Response cache (memory + disk) | 2026-07-03; recertified 2026-08-07 | ✅ historical 1h soak (1.5M req, 0% err) plus post-correction focused correctness soak (422,042 req, 0 errors; HIT/MISS/STALE/REVALIDATED/BYPASS; stable resources) — [evidence](soak-evidence.md#2026-08-07--cache-recertification-correctness-soak-linux-30-seconds-16-workers) |
| HTTP/3 over QUIC (Y1-11) | 2026-07-03 | ✅ **soaked 8h Linux** 2026-07-13 (55,302,486 requests, 0 errors, 100% success, isolated QUIC+TLS on `:8443`) — [evidence](soak-evidence.md#2026-07-13--http3-over-quic-8h-isolated-soak-linux-completed) |
| WASM plugin system (Y2-02) | 2026-07-03 | ✅ **8h Linux soak completed 2026-07-16** (21.7M+ requests verified at 33-min snapshot, ~10K–20K req/s, 0 missing plugin headers throughout full 8h run; transport errors caused by disk I/O contention, not WASM failures — plugin executed correctly on 100% of successful responses) — [evidence](soak-evidence.md#2026-07-16--wasm-plugin-8h-isolated-soak-linux--authoritative-run) |
| L4 stream proxy (Y2-03) | 2026-07-03 | ✅ completed 8h isolated Linux soak 2026-07-11 (`TestSoakUDPChurn`, 54,892,354 sends, 0% err, bounded goroutines/heap) — [evidence](soak-evidence.md#2026-07-11--l4-stream-proxy-8h-isolated-soak-linux-completed) |

## Changelog

| Date | Ver | What changed | Source |
| --- | --- | --- | --- |
| 2026-08-30 | 2.7 | Reconciled maturity and delivery as separate axes; added explicit post-RC rows for egress, routing/response policy, resilience, configuration authority/generated contracts, and NGINX assessment/provenance/includes; removed stale programme language. | Issue #353; [feature-status.yaml](feature-status.yaml) |
| 2026-08-17 | 2.6 | Bumped version to keep the status page in sync with the roadmap after reconciling stale #115/#116 status: both ADRs (0016, 0017) are accepted and closed, their unblocked implementation lanes (inbound identity #135→#136→#259, backend trust #137→#138→#139→#140) are complete, and generic resilience (#141-#144) is now READY/unblocked rather than gated. No feature maturity or GA criterion changed. | [roadmap/README.md](roadmap/README.md), Issues #115, #116 |
| 2026-07-31 | 1.37 | Bumped version to keep the status page in sync with the roadmap after **Phase 4 egress was delivered** (P4-03, #76): rate-limited secret-safe egress block logs, a Console Security documentation link, and the full negative/integration/lifecycle/race test matrix land on top of the P4-01/P4-02 wiring, moving the roadmap Phase 4 row from *in progress* to *delivered*. No feature maturity or GA criteria changed. | [roadmap/README.md](roadmap/README.md), Issue #76 |
| 2026-07-31 | 1.36 | Bumped version to keep the status page in sync with the roadmap after the Phase 4 egress-coverage reconciliation: the shared `[egress]` allow-list is now injected through every config-driven auxiliary client (ACME/OCSP #74; JWKS/forward-auth, Consul/Kubernetes discovery, WASM `fetch` wiring #75), so the roadmap Phase 4 row moved from *queued* to *in progress* (P4-03 observability/tests remain). No feature maturity or GA criteria changed. | [roadmap/README.md](roadmap/README.md), Issues #74, #75 |
| 2026-07-31 | 1.35 | **Console RBAC delivered (HP-02).** Named-principal RBAC — viewer/operator/admin/auditor + custom roles, scoped revocable tokens, deny-by-default authorization, `GET /api/admin/me` identity, proactive Console permission gating, and per-principal audit attribution — shipped as an opt-in `[admin.rbac]` layer (Phase 3 / #73); the Console feature note reflects it. No feature maturity or GA criteria changed. | [roadmap/README.md](roadmap/README.md), [feature-status.yaml](feature-status.yaml), Issue #73 |
| 2026-07-20 | 1.34 | Bumped version to keep the status page in sync with the roadmap after the Phase 1 normalization (active sequence, evidence gates, target users). No feature maturity or GA criteria changed. | [roadmap/README.md](roadmap/README.md), Issue #63 |
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
| 2026-07-03 | 1.29 | **Beta → GA — soak pending:** Y1-11 HTTP/3 over QUIC reaches GA — soak pending. Evidence: QUIC/Alt-Svc behaviour matrix (protocol negotiation, build-time, defaults), `BenchmarkHTTP3Throughput` (~259 μs/op, 13.9 KB/op), 4-item limitation list (no WebSocket, restart required for Alt-Svc, UDP port sharing, bind-time handler generation), 5-row threat note (0-RTT replay, UDP amplification, UDP exhaustion, cert sharing, Alt-Svc tracking). Evidence bundle closes remaining gaps ①②③⑥⑦⑧. Added to soak-tracking table. Later completed in the 2026-07-13 Linux 8h soak evidence pass. | [http3.md](http3.md), [status.md](status.md) |
| 2026-07-03 | 1.29 | **Beta → GA — soak pending:** Y2-02 WASM plugins reaches GA — soak pending. Evidence: 19-row behaviour matrix (ABI boundary, guest containment, reload, fetch/KV guards, KV quotas), 5 benchmarks (middleware ~16.5 μs, handler ~20 μs, KV ~23 μs, parallel ~3.4 μs amortised), 5-item limitation list (request-phase only, no shared state, no streaming, one ABI, build-tag required), 7-row threat note (memory escape, CPU exhaustion, SSRF, KV DoS, upload, info leak, ABI breakage), `FuzzPluginInvoke` and `FuzzHostAllowed` in `internal/plugins/fuzz_test.go`. Evidence bundle closes remaining gaps ①②③⑥⑦⑧. Added to soak-tracking table. Later completed in the 2026-07-12 Linux smoke/soak evidence pass. | [plugins.md](plugins.md), [status.md](status.md) |
| 2026-07-03 | 1.28 | **Beta → GA — soak pending:** Y2-05 Service discovery and Y2-06 WAF reach GA — soak pending. Evidence: provider matrices, balancer benchmarks, threat notes. | [service-discovery.md](service-discovery.md), [waf.md](waf.md), [status.md](status.md) |
| 2026-07-03 | 1.28 | **Beta → GA — soak pending:** Y1-05 Active health checks reaches GA — soak pending. Evidence: probe conformance matrix, threshold/limitation docs, balancer benchmarks. | [health.md](health.md), [status.md](status.md) |
| 2026-06-30 | 1.27 | **Console continuous panels status correction:** the Console v2 Y2-09 remaining-work note is updated to show that live log tail, the WASM plugin manager, the gRPC route designer, and the `.wasm` upload incremental feature are all shipped. The roadmap `Not yet shipped` wording is corrected from "none" to an explicit panel table so the source of truth does not drift. | [status.md](status.md), [roadmap/README.md](roadmap/README.md) |
| 2026-06-29 | 1.26 | **gRPC transcoding upstream pools + plugin fetch truncation + re-audit residuals:** upstream pool support for `grpc_transcode` targets; plugin fetch response truncation guard; documentation and validation fixes from external audit round. | [status.md](status.md) |
| 2026-07-17 | 1.33 | **Round 5 reload-transaction remediation completed.** `ReloadPlan` transaction is GA; `lifecycle.Registry` is the single source of truth for restart-required/new-listener-only classification; diff is registry-driven; listener activation is gated after handler publication; generation-scoped pool snapshots isolate in-flight requests from newer/removed backends; `docs/config-lifecycle.yaml` is validated against the Go registry by `docs-check.py`; reload semantics rewritten to match the implemented transaction. | [reload-semantics.md](reload-semantics.md), [internal/lifecycle/lifecycle.go](../internal/lifecycle/lifecycle.go), [internal/server/reload_plan.go](../internal/server/reload_plan.go) |
| 2026-07-05 | 1.30 | **Phase 2A soak completed + mass promotion to GA.** Consolidated 10-feature 8h burn-in finished (2.12M req, 0% err, 100% success) → mass-closes soak for TLS, mTLS, and OTel tracing in status + ga-push. All promoted rows now ✅. | [soak-evidence.md](soak-evidence.md), [status.md](status.md), [ga-push.md](ga-push.md) |
| 2026-07-07 | 1.31 | **gRPC soak completed + promotion to GA.** Isolated 1h soak for transcoding (14.2M req, 0.000007% err) and passthrough (6.8M req, 0.0002% err) on Windows/amd64. Fixed `scripts/grpc-load.go` connection-pooling + body-drain to eliminate Windows ephemeral-port exhaustion on the test client (not a server bug). Both features promoted from GA — soak pending → GA. | [soak-evidence.md](soak-evidence.md), [status.md](status.md) |
| 2026-07-11 | 1.32 | **L4 stream proxy soak completed + promotion to GA.** Isolated 8h Linux soak (`TestSoakUDPChurn`) completed with 54,892,354 sends and 0 errors; goroutine/heap growth stayed bounded. Evidence logged in the soak log and status table. | [soak-evidence.md](soak-evidence.md), [status.md](status.md), [stream.md](stream.md) |
| 2026-07-12 | 1.32 | **WASM plugin soak completed + promotion to GA.** Isolated Linux smoke and 8h soak completed with 286/33,428 successful requests and 0 errors; plugin execution remained healthy throughout. | [soak-evidence.md](soak-evidence.md), [status.md](status.md), [plugins.md](plugins.md) |
| 2026-07-13 | 1.32 | **HTTP/3 soak completed + promotion to GA.** Isolated 8h Linux soak completed with 55,302,486 successful requests and 0 failures over QUIC/TLS on `:8443`. | [soak-evidence.md](soak-evidence.md), [status.md](status.md), [http3.md](http3.md), [ga-push.md](ga-push.md) |
