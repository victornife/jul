# Jul.IA — Roadmap

> Version 1.3 · Updated 2026-06-21

This is the consolidated 5-year plan. It pairs with the [vision](../vision/) and
the [Architecture Decision Records](../adr/). **Keep this file current:** whenever
a feature ships, move its row to *Delivered* and tick the year checklist; whenever
an ADR is added, link it where relevant.

Detailed per-feature engineering specs (design, config, tasks, DoD) live in
[specs/](../specs/) — one file per year. The product direction behind these
changes is recorded in the [reviews & decision log](../reviews/).

Effort uses T-shirt sizing: **M** ≈ weeks · **L** ≈ ~a quarter · **XL** ≈
multi-quarter.

**Delivery legend:** ✅ delivered · 🚧 in progress (committed) · ⏳ time-boxed bet ·
🔒 deferred (demand-gated) · ⬜ vision horizon.

**Maturity** (per [ADR 0003](../adr/0003-maturity-and-ga.md)): Planned · Prototype ·
Alpha · **Beta** · **GA** · Deprecated. *Implemented ≠ GA:* a shipped feature is
**Beta** until it meets the full GA bar. The first GA target is gRPC transcoding +
native gRPC passthrough.

---

## Delivered

### Year 1 — Credibility & effortlessness ✅

Shipped (feature-complete for the year). Maturity is **Beta** across the board —
none has yet cleared the full GA bar in [ADR 0003](../adr/0003-maturity-and-ga.md).

| ID | Feature | Maturity |
| --- | --- | --- |
| Y1-01 | Automatic HTTPS (ACME: HTTP-01, TLS-ALPN-01, OCSP stapling) | Beta |
| Y1-02 | Response compression (gzip core; `brotli`/`zstd` tags) | Beta |
| Y1-03 | Rate limiting + connection limiting | Beta |
| Y1-04 | Authentication (Basic, bearer/JWT, forward-auth) | Beta |
| Y1-05 | Active health checks (HTTP/TCP probes) | Beta |
| Y1-06 | gRPC ↔ JSON transcoding (MVP, `grpc` tag) | Beta |
| Y1-07 | Console v1 (web dashboard, `console` tag) | Beta |
| Y1-08 | Zero-config + `jul lint` | Beta |
| Y1-09 | NGINX config importer (`importer` tag) | Beta |
| Y1-10 | OpenTelemetry tracing + access-log sinks (`otel` tag) | Beta |
| Y1-11 | HTTP/3 over QUIC (`http3` tag) | Beta |

### Year 2 — partial ✅

Shipped at **Beta**. The previous "gRPC transcoding GA" label drops the premature
*GA* claim — streaming is implemented (server/client/bidi, NDJSON/SSE) but the
feature is Beta until the full GA bar lands. gRPC transcoding + passthrough is
the **first GA target**, and most of the bar is now met: published
[conformance matrices](../grpc-transcoding.md#conformance-matrix), benchmark
numbers, known-limitations lists, a threat note, parser fuzzing, and a Console
**Status** surface. The remaining hard gate is the long-running **soak test**
(the semver tag is cut at the GA release).

| ID | Feature | Tag | Maturity |
| --- | --- | --- | --- |
| Y2-01 | gRPC ↔ JSON transcoding (server/client/bidi streaming, NDJSON/SSE) | `grpc` | Beta → first GA target (soak test pending) |
| Y2-02 | WASM plugin system (wazero) | `wasmplugins` | Beta |
| Y2-03 | L4 stream proxy (TCP/UDP, SNI routing, PROXY protocol) | `stream` | Beta |
| Y2-04 | Native gRPC passthrough + h2c inbound | `grpc` | Beta → first GA target (soak test pending) |
| Y2-05 | Service discovery / dynamic upstreams (DNS/SRV core; Consul/K8s tags) | `consul`, `kubernetes` | Beta |

---

## Planned

### Year 2 — committed remaining 🚧 (Protocol Gateway + Extensibility)

The near-term, demand-backed work. Each ships at **Beta** with its own Console
panel ([ADR 0004](../adr/0004-console-ui-invariants.md)) as part of Definition of
Done.

| ID | Feature | Description | Impact / what it unlocks | Effort |
| --- | --- | --- | --- | --- |
| Y2-06 | WAF (Coraza + OWASP CRS) | ModSecurity-compatible WAF; embed CRS; block/detect per-location | Edge security without a separate WAF appliance | L |
| Y2-07 | mTLS client auth + identity vars | Verify client certs; expose `$ssl_client_*`; per-location require | Zero-trust ingress; foundation for fleet and mesh later | M |
| SEC-1 | Secrets references (pulled earlier from Y5-06) | `env`/`file` secret refs + log redaction + lint for literal secrets; Vault/KMS later | Removes scattered secret handling across ACME/JWT/forward-auth/mTLS/AI keys | M |
| Y2-09 | Console v2 (reframed) | Live log tail, WASM plugin manager, gRPC route designer — delivered as **continuous per-feature Console panels**, not a monolithic release ([ADR 0004](../adr/0004-console-ui-invariants.md)). **In progress:** a read-only **Status** overview (capabilities active in the running config) shipped as the first continuous panel. | Admin UI grows into an operations cockpit without a big-bang rewrite | L |

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

Counts are *shipped* features (all at **Beta** until they clear the GA bar); they
are not GA counts.

- [x] **Year 1** — Credibility & effortlessness (11/11 shipped · Beta)
- [ ] **Year 2** — Protocol Gateway + Extensibility (5/9 shipped · Beta). Committed
  remaining: Y2-06 WAF, Y2-07 mTLS, SEC-1 secrets, Y2-09 Console. Y2-08 GraphQL
  **deferred** (demand-gated); AI-MVP is a **time-boxed bet**.
- [ ] **Years 3–5** — **Vision horizon (demand-gated)** — not committed; entered
  per evidence gates ([ADR 0003](../adr/0003-maturity-and-ga.md)).

## Maintenance

When a feature ships:

1. Move its row from *Planned* to *Delivered*, note the build tag, and set its
   **Maturity** (usually **Beta**; **GA** only after the full GA bar in
   [ADR 0003](../adr/0003-maturity-and-ga.md), including a Console surface).
2. Update the year completion checklist count.
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
| 2026-06-21 | 1.3 | Advanced the **first GA target** (Y2-01 transcoding + Y2-04 passthrough): published conformance matrices, benchmark numbers, known-limitations lists, a threat note, path-template fuzzing, and confirmed the Console Status surface — leaving the **soak test** as the only remaining hard GA gate. | Both features stay **Beta**; all other rows, IDs, and maturity states unchanged. | [grpc-transcoding.md](../grpc-transcoding.md), [grpc-proxy.md](../grpc-proxy.md); [ADR 0003](../adr/0003-maturity-and-ga.md) |
| 2026-06-21 | 1.2 | Recorded the first **continuous Console v2 panel** under Y2-09: a read-only **Status** overview (which capabilities are active in the running config) plus a back-link from the standalone config page to the Console, keeping all screens navigable. | All feature rows, IDs, maturity states, and the Y2-09 framing as continuous per-feature panels. | [ADR 0004](../adr/0004-console-ui-invariants.md); [console.md](../console.md) |
| 2026-06-21 | 1.1 | Added a **Maturity** column and delivery legend; reclassified all shipped Year 1–2 features from "Delivered" to **Beta** (gRPC transcoding + passthrough named first GA target); demoted Y2-08 to a **deferred, demand-gated** GraphQL *composition* prototype with explicit resolvers; reframed Y2-09 Console v2 as continuous per-feature panels; pulled secrets references earlier (SEC-1); added a **time-boxed** AI Gateway MVP bet; relabeled Years 3–5 as the **Vision horizon (demand-gated)**; fixed `adr/` and `specs/` links after the folder move. | All Year 1–5 feature rows, IDs, descriptions, impact and effort sizing (Years 3–5 preserved verbatim under the horizon banner). | [review 2026-06-21](../reviews/); [ADR 0002](../adr/0002-protocol-adaptation.md), [ADR 0003](../adr/0003-maturity-and-ga.md), [ADR 0004](../adr/0004-console-ui-invariants.md) |
| 2026-06-21 | 1.0 | Initial consolidated 5-year roadmap. | — | — |
