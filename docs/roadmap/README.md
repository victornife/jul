# Jul.IA — Roadmap

> Version 1.12 · Updated 2026-06-24

This is the consolidated 5-year plan. It pairs with the [vision](../vision/) and
the [Architecture Decision Records](../adr/). **Keep this file current:** whenever
a feature ships, move its row to *Delivered* and tick the year checklist; whenever
an ADR is added, link it where relevant.

Detailed per-feature engineering specs (design, config, tasks, DoD) live in
[specs/](../specs/) — one file per year. The product direction behind these
changes is recorded in the [reviews & decision log](../reviews/). New to the
terminology in this plan? The [concepts appendix](../vision/appendix.md) explains
HTTP, proxies, TLS, caching, and observability from first principles.

Effort uses T-shirt sizing: **M** ≈ weeks · **L** ≈ ~a quarter · **XL** ≈
multi-quarter.

**Delivery legend:** ✅ delivered · 🚧 in progress (committed) · ⏳ time-boxed bet ·
🔒 deferred (demand-gated) · ⬜ vision horizon.

**Maturity** (per [ADR 0003](../adr/0003-maturity-and-ga.md)): Planned · Prototype ·
Alpha · **Beta** · **GA** · Deprecated. *Implemented ≠ GA:* a shipped feature is
**Beta** until it meets the full GA bar. The [GA push](../ga-push.md) is hardening
shipped features to GA; the soak test is a post-GA gate per
[ADR 0005](../adr/0005-soak-post-ga-gate.md). GA features so far (all soak
pending): the foundational **Core HTTP** stack (static, reverse proxy,
FastCGI/uWSGI, virtual hosts, routing), gRPC transcoding, native gRPC
passthrough, mTLS, TLS + automatic HTTPS, and authentication.

---

## Delivered

### Year 1 — Credibility & effortlessness ✅

Shipped (feature-complete for the year). Most rows are **Beta**; **Y1-01 (TLS +
automatic HTTPS)** and **Y1-04 (authentication)** have reached **GA — soak
pending** in the [GA push](../ga-push.md) (the soak test is a post-GA gate per
[ADR 0005](../adr/0005-soak-post-ga-gate.md)).

| ID | Feature | Maturity |
| --- | --- | --- |
| Y1-01 | Automatic HTTPS (ACME: HTTP-01, TLS-ALPN-01, OCSP stapling) | **GA — soak pending** |
| Y1-02 | Response compression (gzip core; `brotli`/`zstd` tags) | Beta |
| Y1-03 | Rate limiting + connection limiting | Beta |
| Y1-04 | Authentication (Basic, bearer/JWT, forward-auth) | **GA — soak pending** |
| Y1-05 | Active health checks (HTTP/TCP probes) | Beta |
| Y1-06 | gRPC ↔ JSON transcoding (MVP, `grpc` tag) | Beta |
| Y1-07 | Console v1 (web dashboard, `console` tag) | Beta |
| Y1-08 | Zero-config + `jul lint` | Beta |
| Y1-09 | NGINX config importer (`importer` tag) | Beta |
| Y1-10 | OpenTelemetry tracing + access-log sinks (`otel` tag) | Beta |
| Y1-11 | HTTP/3 over QUIC (`http3` tag) | Beta |

### Year 2 — partial ✅

Shipped at **Beta**, except the first **GA** features from the
[GA push](../ga-push.md). gRPC transcoding (Y2-01) + passthrough (Y2-04) and mTLS
(Y2-07) are now **GA — soak pending**: published
[conformance matrices](../grpc-transcoding.md#conformance-matrix), benchmark
numbers (including the [mTLS handshake cost](../mtls.md#benchmarks)),
known-limitations lists, threat notes, parser fuzzing where applicable, a
semver-guarded [compatibility policy](../compatibility.md), and Console **Status**
surfaces. The only open item is the long-running **soak test**, reclassified to a
post-GA gate per [ADR 0005](../adr/0005-soak-post-ga-gate.md).

| ID | Feature | Tag | Maturity |
| --- | --- | --- | --- |
| Y2-01 | gRPC ↔ JSON transcoding (server/client/bidi streaming, NDJSON/SSE) | `grpc` | **GA — soak pending** |
| Y2-02 | WASM plugin system (wazero) | `wasmplugins` | Beta |
| Y2-03 | L4 stream proxy (TCP/UDP, SNI routing, PROXY protocol) | `stream` | Beta |
| Y2-04 | Native gRPC passthrough + h2c inbound | `grpc` | **GA — soak pending** |
| Y2-05 | Service discovery / dynamic upstreams (DNS/SRV core; Consul/K8s tags) | `consul`, `kubernetes` | Beta |
| Y2-06 | WAF (Coraza + OWASP CRS; block/detect per-location) | `waf` | Beta |
| Y2-07 | mTLS client auth + `$ssl_client_*` identity vars | core | **GA — soak pending** |
| SEC-1 | Secrets references (`env`/`file`/`secret` refs + log redaction + lint) | core | Beta |

---

## Planned

### Year 2 — committed remaining 🚧 (Protocol Gateway + Extensibility)

The near-term, demand-backed work. Each ships at **Beta** with its own Console
panel ([ADR 0004](../adr/0004-console-ui-invariants.md)) as part of Definition of
Done.

| ID | Feature | Description | Impact / what it unlocks | Effort |
| --- | --- | --- | --- | --- |
| Y2-09 | Console v2 (reframed) | Live log tail, WASM plugin manager, gRPC route designer — delivered as **continuous per-feature Console panels** ([ADR 0004](../adr/0004-console-ui-invariants.md)). The hand-written Console v1 is migrated **once** to a prebuilt **React/TS/Vite/Tailwind** SPA, embedded in the binary (**no Node runtime, no external web assets**) — a bounded substrate cutover, after which panels resume continuous evolution ([ADR 0006](../adr/0006-console-v2-stack.md); [spec](../specs/console-v2.md)). **In progress:** a read-only **Status** overview (capabilities active in the running config) shipped as the first continuous panel. | Admin UI grows into an operations cockpit on a typed, testable substrate without an ongoing big-bang | L |

### Near-term bet ⏳

| ID | Feature | Description | Impact / what it unlocks | Effort |
| --- | --- | --- | --- | --- |
| AI-MVP | AI Gateway MVP (`ai` tag) — **time-boxed bet** | Thin OpenAI-compatible front door: multi-provider routing, failover, streaming, token/cost metrics. **No** semantic cache or guardrails yet. **Kill/continue gate** after the MVP. Sequenced after Y2-07. | Tests the AI-gateway market window without committing the full Year-4 program (gated per [ADR 0003](../adr/0003-maturity-and-ga.md)) | L |

### Deferred — demand-gated 🔒

| ID | Feature | Description | Gate | Effort |
| --- | --- | --- | --- | --- |
| Y2-08 | GraphQL composition prototype (`graphql` tag) | Schema-first, **explicit resolvers**, Query/Mutation over gRPC/REST unary, depth/complexity limits + resolver tracing from day one. **Not** "GraphQL without resolvers" ([ADR 0002](../adr/0002-protocol-adaptation.md)). | Users need BFF/composition over existing REST/gRPC | L |

---

## Vision horizon — demand-gated ⬜

Years 3–5 below remain the **broad long-term vision**, kept for narrative and
direction. **None is a committed plan:** each category enters the operating
roadmap only when its [evidence gate](../adr/0003-maturity-and-ga.md) trips. The
tables are intentionally preserved (not deleted) to show where Jul.IA *can* go.

### Year 3 — Scale, Fleet & Ecosystem ⬜ (horizon · open-core)

| ID | Feature | Description | Impact / what it unlocks | Effort |
| --- | --- | --- | --- | --- |
| Y3-01 | Multi-node control plane | Central config, staged/canary rollout, health-gated promotion, fleet rollback | The single-node → fleet pivot; core of monetization | XL |
| Y3-02 | Console RBAC + SSO/SAML/OIDC | Multi-user auth, roles, scoped API tokens | Enterprise access control + audit-ready identity | L |
| Y3-03 | Distributed cache + rate limit | Redis-shared cache + distributed token buckets; pub/sub purge | A fleet behaves as one cache/limiter | XL |
| Y3-04 | K8s Ingress + Gateway API + Helm | Watch Ingress/Gateway API, hot-apply, status writeback, chart | Drop-in Kubernetes ingress controller | XL |
| Y3-05 | Traffic management | Weighted split, canary, blue-green, request mirroring | Progressive delivery at the proxy | L |
| Y3-06 | Hot binary upgrade | Zero-downtime binary swap via FD handoff (unix; Windows fallback) | 24/7 operation across version upgrades | M |
| Y3-07 | Plugin marketplace (signed) | `jul add` to verify/install signed WASM plugins | Safe plugin distribution; ecosystem growth | L |
| Y3-08 | Audit logging / compliance | Tamper-evident hash-chained audit trail; SIEM forward | SOC2/ISO evidence; regulated-industry sales | M |
| Y3-09 | Console v3 | Fleet dashboard, RBAC admin, traffic UI, audit viewer | UI surface for everything in Year 3 | L |

### Year 4 — AI-native + Edge ⬜ (horizon)

| ID | Feature | Description | Impact / what it unlocks | Effort |
| --- | --- | --- | --- | --- |
| Y4-01 | AI Gateway core | OpenAI-compatible + JUL-native LLM gateway: multi-provider routing, failover, streaming | The Year 4 flagship; AI-gateway category | XL |
| Y4-02 | Semantic caching | Cache LLM responses by embedding similarity, namespaced by model/params | Cuts token spend + latency | L |
| Y4-03 | Token rate-limit + cost observability | Per-key/model token budgets, USD caps, cost dashboards | Cost control + AI chargeback | M |
| Y4-04 | Prompt/response guardrails | PII redaction, injection detection, moderation, custom WASM guardrails | Safety + compliance on AI traffic | L |
| Y4-05 | AI-assisted Console | NL → validated config diff (human-approved), anomaly detection, incidents | Operate Jul.IA in plain English | L |
| Y4-06 | Edge compute / WASM FaaS | Richer plugin ABI: persistent KV, fetch, cron, secrets | Plugins become mini edge-functions | L→XL |
| Y4-07 | CDN-grade caching | Tiered/origin-shield, tag purge, image-opt (webp/avif), ESI | Run Jul.IA as a CDN node | L |
| Y4-08 | 1-click app templates | Signed catalog (`jul template apply`) with auto-HTTPS | Popular apps behind Jul.IA in one command | M |
| Y4-09 | Standards: Early Hints, WebTransport, PQ-TLS | HTTP 103, WebTransport over H3, hybrid post-quantum TLS | Protocol frontier; future-proof TLS | M |

### Year 5 — Global scale, Mesh & Cloud ⬜ (horizon · commercial completion)

| ID | Feature | Description | Impact / what it unlocks | Effort |
| --- | --- | --- | --- | --- |
| Y5-01 | JUL Cloud platform | Multi-tenant hosted Console + BYO-node enrollment; control-only traffic | The commercial endgame (SaaS) | XL |
| Y5-02 | GSLB geo-routing | Authoritative-DNS + HTTP geo-steering; multi-region failover | Global availability + latency routing | XL |
| Y5-03 | Service mesh mode | Sidecar + ambient, east-west mTLS with SPIFFE/SVID, identity policy | Jul.IA as a mesh data plane | XL |
| Y5-04 | Bot management + DDoS mitigation | JA3/H2 fingerprinting, JS/PoW challenges, adaptive rate limiting, L7 defense | App-layer abuse defense without a scrubber | L |
| Y5-05 | RUM + synthetic + SLO | Web-vitals beacon, synthetic probes, SLO/error-budget tracking | Real-user observability + SLO dashboards | L |
| Y5-06 | Secrets/identity integrations | Vault/cloud KMS/SPIFFE via rotating refs | Centralized secrets with hot rotation | M |
| Y5-07 | Ecosystem maturity | Certification + signing, gallery, learning hub, `jul publish` | Durable ecosystem + community moat | L |
| Y5-08 | Cloud usage metering + billing | Per-tenant metering + Stripe (demand-gated) | Monetization of JUL Cloud | L |
| Y5-09 | Global perf + final hardening | Multi-region load tests, per-tag size budgets, pen-test, SOC2/ISO, SBOM | 5-year GA readiness | M |

---

## Year completion checklist

Counts are *shipped* features, regardless of maturity. Most ship at **Beta**;
some have since cleared the GA bar to **GA — soak pending** (the canonical
[status matrix](../status.md) is the source of truth). They are not GA counts.

- [x] **Year 1** — Credibility & effortlessness (11/11 shipped; Y1-01 + Y1-04 at
  **GA — soak pending**, the rest **Beta**)
- [ ] **Year 2** — Protocol Gateway + Extensibility (8/9 shipped; Y2-01 + Y2-04 +
  Y2-07 at **GA — soak pending**, the rest **Beta**). Committed remaining: Y2-09
  Console. Y2-08 GraphQL **deferred** (demand-gated); AI-MVP is a **time-boxed
  bet**.
- [ ] **Years 3–5** — **Vision horizon (demand-gated)** — not committed; entered
  per evidence gates ([ADR 0003](../adr/0003-maturity-and-ga.md)).

## Maintenance

When a feature ships:

1. Move its row from *Planned* to *Delivered*, note the build tag, and set its
   **Maturity** (usually **Beta**; **GA** only after the full GA bar in
   [ADR 0003](../adr/0003-maturity-and-ga.md), including a Console surface).
2. Update the year completion checklist count **and the canonical
   [status matrix](../status.md)** (the feature's maturity + GA-criteria row).
3. Update the status snapshot line in [vision](../vision/) if the active year
   changed.
4. If the work involved a durable technical decision, add an ADR under
   [docs/adr/](../adr/) and link it here.
5. Bump this file's version and add a **Changelog** row (what changed / what
   stayed / source).

When a category's evidence gate trips, move it from *Vision horizon* into the
committed roadmap with a Maturity state.

## Changelog

| Date | Ver | What changed | What stayed | Source |
| --- | --- | --- | --- | --- |
| 2026-06-24 | 1.12 | Shipped **Y2-06 WAF** (Coraza + OWASP CRS, `waf` tag) and **SEC-1 secrets references** (core), both **Beta**. WAF adds block/detect engines per-location, embedded CRS (paranoia 0–4), inline SecLang rules, request/response body inspection, the `jul_waf_events_total` metric, and a Console **Status**/**Security** surface. SEC-1 adds `${env:}`/`${file:}`/`${secret:}` references resolved across all string config, automatic log redaction of resolved values, a `jul lint` rule for literal secrets, and a Console secret-reference count. Year-2 checklist **6/9 → 8/9**; committed remaining is now just Y2-09 Console. | All other feature rows, IDs, and maturity states; Y2-08 GraphQL stays deferred and AI-MVP stays a time-boxed bet. | [waf.md](../waf.md), [secrets.md](../secrets.md), [year-2.md](../specs/year-2.md), [status.md](../status.md) |
| 2026-06-23 | 1.11 | Recorded the **Console v2 substrate migration** under Y2-09: a one-time cutover from the hand-written v1 to a prebuilt, embedded **React/TS/Vite/Tailwind** SPA (Node-free build, no external assets, ~250 KB gz budget), closing Console GA gaps ① + ⑦ and targeting **GA — soak pending**. | All other feature rows, IDs, and maturity states; the Y2-09 continuous-panels framing stands (the cutover is a bounded exception). | [ADR 0006](../adr/0006-console-v2-stack.md); [console-v2 spec](../specs/console-v2.md) |
| 2026-06-22 | 1.10 | Linked the new beginner-friendly [concepts appendix](../vision/appendix.md) (HTTP, proxies, TLS, caching, observability from first principles) from the intro. | All feature rows, IDs, maturity states, and the 5-year plan. | [appendix.md](../vision/appendix.md) |
| 2026-06-22 | 1.9 | Fixed **Y2-07 mTLS checklist drift**: the Year-2 completion line still listed mTLS as *committed remaining* and counted **5/9** even though it shipped and reached **GA — soak pending** — corrected to **6/9 shipped**, removed Y2-07 from the remaining list, and recorded which shipped features are GA. Added the canonical [status matrix](../status.md) as the single source of truth for maturity + GA criteria and wired it into the Maintenance steps. | All feature rows, IDs, descriptions, and maturity states; only the stale checklist counts/labels change, plus a new cross-reference. | [status.md](../status.md) |
| 2026-06-21 | 1.8 | **Y1-04 authentication → GA** (GA push) and **relabeled every soak-pending GA feature `GA` → `GA — soak pending`** for honesty (Core HTTP, gRPC transcoding/passthrough, mTLS, TLS+ACME, auth). Published [docs/auth.md](../auth.md) (CIDR/Basic/JWT/forward-auth behaviour matrix, JWKS + algorithm-confusion threat note, limits, GA table); added `BenchmarkBasicVerify`/`BenchmarkJWTValidate` and `FuzzParseJWKS`/`FuzzValidateToken`. | All feature rows, IDs, and the soak post-GA gate ([ADR 0005](../adr/0005-soak-post-ga-gate.md)); only the label wording and the Y1-04 maturity change. | [auth.md](../auth.md), [ga-push.md](../ga-push.md) |
| 2026-06-21 | 1.7 | **Core HTTP → GA** (GA push). The foundational request stack — static serving, reverse proxy, FastCGI/uWSGI, virtual hosts, and location routing — reaches **GA**: published [docs/core-http.md](../core-http.md) (host/location/static/proxy/FastCGI/balancing matrices, path-traversal + SSRF + CRLF threat note, limits), added router/balancer/static benchmarks and router + FastCGI fuzz targets, contract frozen under the [compatibility policy](../compatibility.md). Soak stays a post-GA gate. | All feature rows, IDs, and (Beta) maturity states; runtime behaviour is unchanged — only the new doc, tests, and the GA label. | [core-http.md](../core-http.md), [ga-push.md](../ga-push.md) |
| 2026-06-21 | 1.6 | **Y1-01 TLS + automatic HTTPS → GA** (GA push). Published [docs/tls-acme.md](../tls-acme.md) with a behaviour matrix, SNI/ACME/OCSP semantics, a threat note, and benchmark numbers (`BenchmarkTLSHandshakeServerAuth`, `BenchmarkSNICertSelection` — 0-alloc selection); contract frozen under the [compatibility policy](../compatibility.md). Soak stays a post-GA gate ([ADR 0005](../adr/0005-soak-post-ga-gate.md)). | All other rows, IDs, and (Beta) maturity states; runtime behaviour is unchanged — only the maturity label and the new doc. | [tls-acme.md](../tls-acme.md), [ga-push.md](../ga-push.md), [compatibility.md](../compatibility.md) |
| 2026-06-21 | 1.5 | Declared the **first GA features** in the [GA push](../ga-push.md): **Y2-01 gRPC transcoding**, **Y2-04 gRPC passthrough**, and **Y2-07 mTLS** move Beta → **GA** — closing the mTLS handshake benchmark and adding the semver-guarded [compatibility policy](../compatibility.md). The soak test is reclassified to a **post-GA gate** ([ADR 0005](../adr/0005-soak-post-ga-gate.md)), so it no longer blocks GA. | All other rows, IDs, descriptions, and (Beta) maturity states; feature behaviour is unchanged — only labels and the contract doc. | [ga-push.md](../ga-push.md), [compatibility.md](../compatibility.md), [mtls.md](../mtls.md#benchmarks); [ADR 0005](../adr/0005-soak-post-ga-gate.md) |
| 2026-06-21 | 1.4 | Moved **Y2-07 mTLS** from committed-remaining to **Delivered (Beta)**: client-certificate verification against a CA bundle (request/require), per-location `require_client_cert`, `$ssl_client_*` identity proxy variables, signature-verified CRL + SAN allow-list, and the `jul_mtls_handshakes_total` metric — shipped in core (no build tag). | All other rows, IDs, and maturity states; the AI-MVP bet stays sequenced after mTLS (now satisfied). | [mtls.md](../mtls.md), [year-2.md](../specs/year-2.md); [ADR 0003](../adr/0003-maturity-and-ga.md) |
| 2026-06-21 | 1.3 | Advanced the **first GA target** (Y2-01 transcoding + Y2-04 passthrough): published conformance matrices, benchmark numbers, known-limitations lists, a threat note, path-template fuzzing, and confirmed the Console Status surface — leaving the **soak test** as the only remaining hard GA gate. | Both features stay **Beta**; all other rows, IDs, and maturity states unchanged. | [grpc-transcoding.md](../grpc-transcoding.md), [grpc-proxy.md](../grpc-proxy.md); [ADR 0003](../adr/0003-maturity-and-ga.md) |
| 2026-06-21 | 1.2 | Recorded the first **continuous Console v2 panel** under Y2-09: a read-only **Status** overview (which capabilities are active in the running config) plus a back-link from the standalone config page to the Console, keeping all screens navigable. | All feature rows, IDs, maturity states, and the Y2-09 framing as continuous per-feature panels. | [ADR 0004](../adr/0004-console-ui-invariants.md); [console.md](../console.md) |
| 2026-06-21 | 1.1 | Added a **Maturity** column and delivery legend; reclassified all shipped Year 1–2 features from "Delivered" to **Beta** (gRPC transcoding + passthrough named first GA target); demoted Y2-08 to a **deferred, demand-gated** GraphQL *composition* prototype with explicit resolvers; reframed Y2-09 Console v2 as continuous per-feature panels; pulled secrets references earlier (SEC-1); added a **time-boxed** AI Gateway MVP bet; relabeled Years 3–5 as the **Vision horizon (demand-gated)**; fixed `adr/` and `specs/` links after the folder move. | All Year 1–5 feature rows, IDs, descriptions, impact and effort sizing (Years 3–5 preserved verbatim under the horizon banner). | [review 2026-06-21](../reviews/); [ADR 0002](../adr/0002-protocol-adaptation.md), [ADR 0003](../adr/0003-maturity-and-ga.md), [ADR 0004](../adr/0004-console-ui-invariants.md) |
| 2026-06-21 | 1.0 | Initial consolidated 5-year roadmap. | — | — |
