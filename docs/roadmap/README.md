# Jul.IA — Roadmap

This is the consolidated 5-year plan. It pairs with the [vision](../vision/) and
the [Architecture Decision Records](adr/). **Keep this file current:** whenever a
feature ships, move its row to *Delivered* and tick the year checklist; whenever
an ADR is added, link it where relevant.

Detailed per-feature engineering specs (design, config, tasks, DoD) live in
[specs/](specs/) — one file per year.

Effort uses T-shirt sizing: **M** ≈ weeks · **L** ≈ ~a quarter · **XL** ≈
multi-quarter.

Legend: ✅ delivered · 🚧 in progress · ⬜ planned.

---

## Delivered

### Year 1 — Credibility & effortlessness ✅

| ID | Feature |
| --- | --- |
| Y1-01 | Automatic HTTPS (ACME: HTTP-01, TLS-ALPN-01, OCSP stapling) |
| Y1-02 | Response compression (gzip core; `brotli`/`zstd` tags) |
| Y1-03 | Rate limiting + connection limiting |
| Y1-04 | Authentication (Basic, bearer/JWT, forward-auth) |
| Y1-05 | Active health checks (HTTP/TCP probes) |
| Y1-06 | gRPC ↔ JSON transcoding (MVP, `grpc` tag) |
| Y1-07 | Console v1 (web dashboard, `console` tag) |
| Y1-08 | Zero-config + `jul lint` |
| Y1-09 | NGINX config importer (`importer` tag) |
| Y1-10 | OpenTelemetry tracing + access-log sinks (`otel` tag) |
| Y1-11 | HTTP/3 over QUIC (`http3` tag) |

### Year 2 — partial ✅

| ID | Feature | Tag |
| --- | --- | --- |
| Y2-01 | gRPC transcoding GA (server/client/bidi streaming, NDJSON/SSE) | `grpc` |
| Y2-02 | WASM plugin system (wazero) | `wasmplugins` |
| Y2-03 | L4 stream proxy (TCP/UDP, SNI routing, PROXY protocol) | `stream` |
| Y2-04 | Native gRPC passthrough + h2c inbound | `grpc` |
| Y2-05 | Service discovery / dynamic upstreams (DNS/SRV core; Consul/K8s tags) | `consul`, `kubernetes` |

---

## Planned

### Year 2 — remaining 🚧 (Protocol Gateway + Extensibility)

| ID | Feature | Description | Impact / what it unlocks | Effort |
| --- | --- | --- | --- | --- |
| Y2-06 | WAF (Coraza + OWASP CRS) | ModSecurity-compatible WAF; embed CRS; block/detect per-location | Edge security without a separate WAF appliance | L |
| Y2-07 | mTLS client auth + identity vars | Verify client certs; expose `$ssl_client_*`; per-location require | Zero-trust ingress; foundation for fleet (Y3) and mesh (Y5) | M |
| Y2-08 | GraphQL gateway (experimental) | Schema-first GraphQL over REST/gRPC; depth/complexity limits | Unified GraphQL API without writing resolvers | L |
| Y2-09 | Console v2 | Live log tail, WASM plugin manager, gRPC route designer | Admin UI becomes an operations cockpit | L |

### Year 3 — Scale, Fleet & Ecosystem ⬜ (open-core)

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

### Year 4 — AI-native + Edge ⬜

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

### Year 5 — Global scale, Mesh & Cloud ⬜ (commercial completion)

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

- [x] **Year 1** — Credibility & effortlessness (11/11)
- [ ] **Year 2** — Protocol Gateway + Extensibility (5/9: Y2-01…05 done; Y2-06…09 remaining)
- [ ] **Year 3** — Scale, Fleet & Ecosystem (0/9)
- [ ] **Year 4** — AI-native + Edge (0/9)
- [ ] **Year 5** — Global scale, Mesh & Cloud (0/9)

## Maintenance

When a feature ships:

1. Move its row from *Planned* to *Delivered* and note the build tag.
2. Update the year completion checklist count.
3. Update the status snapshot line in [vision](../vision/) if the active year
   changed.
4. If the work involved a durable technical decision, add an ADR under
   [docs/adr/](adr/) and link it here.
