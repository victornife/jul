# Jul.IA Roadmap, Vision and Specs — Critical Product Review

> **Reviewed — 2026-06-21 ✅** · Status: **Adopted / Reframed.** This critique was
> evaluated in depth and largely accepted; the resulting decisions live in
> [ADR 0003 — Maturity & GA](../adr/0003-maturity-and-ga.md),
> [ADR 0004 — Console-first invariants](../adr/0004-console-ui-invariants.md), and
> the updated [vision](../vision/) and [roadmap](../roadmap/). The year→phase
> restructure was *reframed* (kept years + maturity + gates), not adopted wholesale.
> See the [reviews & decision log](README.md) for the full mapping. The original
> text is preserved below unchanged.

## Scope

This document captures a critical product/technical review of Jul.IA's vision, roadmap, engineering specs, and declared delivery status.

It is written from the perspective of a senior technical Product Manager evaluating Jul as an edge server, protocol gateway, infrastructure platform, and potential open-core/cloud product.

The review is based on the repository materials inspected during the analysis: README, vision, roadmap, engineering specs, ADRs, configuration samples, CI workflow, and selected implementation files for gRPC passthrough, gRPC transcoding, WASM plugins, L4 stream proxy, and service discovery.

Where the repo declares something as delivered, this review distinguishes between:

- documented roadmap status,
- evidence of implementation in code,
- and true product maturity / GA readiness.

---

# Executive summary

Jul has a strong core thesis:

> A lean, single-binary edge server and protocol gateway for teams outgrowing NGINX but not wanting a heavy platform.

That thesis is good.

The concern is not lack of ambition. The concern is excessive ambition.

The roadmap currently tries to cover too many product categories:

- NGINX-style edge server,
- reverse proxy,
- API gateway,
- gRPC gateway,
- GraphQL gateway,
- WASM plugin platform,
- WAF,
- mTLS ingress,
- Kubernetes ingress controller,
- fleet control plane,
- enterprise RBAC/SSO,
- distributed cache/rate limiting,
- AI gateway,
- semantic cache,
- CDN,
- edge compute,
- service mesh,
- GSLB,
- bot/DDoS mitigation,
- cloud SaaS.

Individually, many of these ideas are reasonable. Together, they create a product risk: Jul may start to look like a compressed clone of Envoy, Kong, Traefik, Apollo Router, Cloudflare, Istio, and LiteLLM.

The recommendation is to keep the long-term vision broad, but make the operating roadmap much narrower and gate each major expansion behind evidence.

Jul should first win a focused category:

> The lean protocol gateway for teams outgrowing NGINX.

Everything else should support that narrative or be delayed.

---

# 1. What the current vision gets right

## 1.1 The core product identity is strong

Jul's strongest identity is:

- NGINX-inspired,
- Go-first,
- TOML-configured,
- single static binary,
- lean by default,
- heavy features behind build tags,
- validate-then-atomic-reload,
- protocol gateway capabilities without a large platform footprint.

That is differentiated.

The single-binary + build-tag discipline is particularly important. It prevents Jul from becoming a permanently heavy distribution where every user pays for every feature.

## 1.2 The three-pillar model is useful

The vision's three pillars are broadly right:

1. Powerful.
2. Friendly.
3. Lean.

The most important rule is that when power and leanness conflict, leanness wins by default and power moves behind opt-in build tags.

That rule should remain one of the product's core constraints.

## 1.3 The Go-first / no-cgo ADR is strategically correct

The ADR that commits to pure Go by default, with native code only through WASM or sidecars, is a good strategic decision.

It protects:

- cross-compilation,
- contributor experience,
- CI simplicity,
- single-binary deployment,
- supply-chain clarity,
- the lean product story.

Do not weaken this unless there is hard benchmark evidence.

---

# 2. What the vision should change

## 2.1 Avoid "most powerful" as the positioning

"Most powerful" is dangerous.

It invites comparison against:

- Envoy for data plane power,
- Kong for API gateway ecosystem,
- Apollo Router for GraphQL,
- Cloudflare for edge/CDN/security,
- Istio/Linkerd for mesh,
- LiteLLM/Portkey for AI gateway.

Jul should not position itself as the most powerful gateway overall.

A better framing:

> Jul is the leanest serious edge/protocol gateway: powerful enough for modern infrastructure, simple enough to run as one binary.

That is more believable and more defensible.

## 2.2 Make "explicit adapters" a product principle

Jul should avoid magic conversion narratives.

A good product principle:

> Jul adapts protocols explicitly where the mapping is clear. It does not promise universal conversion between incompatible models.

This matters especially for REST, gRPC, and GraphQL.

REST and gRPC can often map operation-to-operation. GraphQL is a query/composition layer and requires explicit schema/resolver design.

## 2.3 Add decision gates to the vision

The vision should define not only what Jul could become, but what evidence is required before entering each category.

Examples:

- GraphQL gateway only if users ask for BFF/composition over existing REST/gRPC.
- Fleet control plane only if single-node users operate multiple Jul instances.
- AI Gateway only if Jul is already used as an API/protocol gateway.
- Cloud only if self-hosted fleet usage exists.
- Mesh only if customers explicitly ask for east-west traffic management.

Without gates, the roadmap becomes a wish list.

---

# 3. Review of delivered work

## 3.1 What appears credible

The roadmap declares Year 1 complete and Year 2 partially complete.

Several claims are supported by code-level evidence:

- Native gRPC passthrough has a dedicated implementation using HTTP/2 transport, h2c/TLS backend handling, no buffering for streams, and pool-based backend selection.
- WASM plugins have a runtime behind a build tag, wazero integration, compilation cache, memory/timeout controls, KV, panic handling, and reload-safe build semantics.
- L4 stream proxy has a server behind a build tag, reload diffing, atomic route swap, TCP/UDP listener handling, SNI route structures, and pool reuse.
- Service discovery has a `Discoverer` interface, refresh loop, last-good behavior, and build-tag provider gating.
- CI builds and tests lean and full profiles, runs race tests, gofmt, linting, and vulnerability checks.

That is a good sign. The roadmap is not only aspirational.

## 3.2 What needs maturity classification

The current roadmap uses "Delivered" too broadly.

A feature being implemented is not the same as:

- production ready,
- documented well,
- benchmarked,
- compatible with expected edge cases,
- supported as a stable contract,
- safe to call GA.

Introduce these states:

| State | Meaning |
| --- | --- |
| Planned | Designed but not implemented |
| Prototype | Works in a narrow lab case |
| Alpha | Usable internally, config/API may change |
| Beta | Usable by early adopters, known limitations |
| GA | Stable, documented, tested, supported |
| Deprecated | Will be removed or replaced |

This is especially important for:

- gRPC transcoding streaming,
- WASM plugin ABI,
- L4 UDP behavior,
- service discovery providers,
- Console capabilities,
- HTTP/3,
- ACME,
- importer fidelity.

## 3.3 Potential inconsistency: gRPC transcoding GA

The README and roadmap describe gRPC transcoding GA with streaming. However, at least one handler comment still describes REST/JSON mapping to unary gRPC calls.

This may be only stale commentary, but it is a signal to audit.

Before calling this GA, verify:

- unary,
- server streaming,
- client streaming,
- bidirectional streaming,
- NDJSON,
- SSE,
- deadlines,
- metadata propagation,
- trailers,
- max message size,
- errors after stream start,
- reflection,
- descriptor sets,
- load balancing,
- shutdown/reload behavior,
- examples and docs.

Recommendation:

> Mark gRPC transcoding as Beta or GA only after a visible conformance matrix exists.

---

# 4. Year 1 review — Credibility & effortlessness

## Assessment

Year 1 is the strongest and most coherent part of the roadmap.

It includes the right baseline features:

- ACME/TLS,
- compression,
- rate limiting,
- auth,
- health checks,
- gRPC transcoding MVP,
- Console v1,
- zero-config/lint,
- NGINX importer,
- OpenTelemetry/access logs,
- HTTP/3.

This is the correct foundation for a serious edge server.

## What is correct

- The focus on credibility is right.
- The mix of DX, observability, and protocol support is good.
- Zero-config plus lint is valuable.
- OTel/access logs matter for platform teams.
- NGINX importer fits the migration narrative.

## What is missing

Before expanding aggressively, Year 1 should be hardened into a foundation release.

Missing or under-emphasized:

- public benchmarks,
- release versioning / semver policy,
- binary size budgets by build tag,
- compatibility matrix,
- fuzzing strategy,
- security threat model,
- known limitations per feature,
- long-running soak tests,
- upgrade compatibility guarantees,
- examples for real apps.

## Recommendation

Do not rush past Year 1.

Turn Year 1 into:

> Jul Foundation GA

Deliverables:

- stable config contract,
- benchmarks,
- conformance tests,
- security baseline,
- examples,
- release artifacts,
- upgrade notes,
- support policy.

---

# 5. Year 2 review — Protocol Gateway + Extensibility

## Assessment

Year 2 is strategically strong.

The theme "Protocol Gateway + Extensibility" is the right next step after Year 1.

The best features are:

- gRPC transcoding GA,
- native gRPC passthrough,
- WASM plugin system,
- L4 stream proxy,
- service discovery,
- WAF,
- mTLS,
- Console v2.

These all reinforce the core thesis.

## What is correct

### gRPC

gRPC belongs in the core narrative.

It differentiates Jul from a simple NGINX replacement and helps teams expose internal gRPC services to REST/JSON clients.

### WASM plugins

WASM plugins are a plausible moat if the ABI is stable and the authoring experience is good.

They provide an extensibility story without compromising the single-binary core.

### L4 stream proxy

L4 stream proxy fits the edge/proxy identity.

It should remain behind a build tag and stay operationally simple.

### Service discovery

Service discovery is necessary for real infrastructure usage.

DNS/SRV in core and Consul/Kubernetes behind tags is a good model.

### WAF and mTLS

Both are valuable to platform/security teams and fit the edge gateway role.

## What is problematic

### GraphQL gateway in Year 2

GraphQL is risky in Year 2.

It is not just another protocol adapter. It is a composition/query execution layer.

The roadmap phrase "GraphQL gateway over REST/gRPC" is acceptable only if scoped as an explicit-resolver prototype. The idea of "without writing resolvers" is dangerous.

GraphQL requires:

- schema management,
- resolver mapping,
- batching,
- N+1 mitigation,
- cost/depth limits,
- field-level auth,
- partial failure semantics,
- tracing by resolver,
- debugging tooling.

That is a lot to add while the gRPC/WASM/L4/discovery features are still maturing.

## Recommendation

Keep Year 2 focused.

Recommended Year 2 priorities:

1. gRPC transcoding and passthrough hardening.
2. WASM plugin ABI stability.
3. L4 stream proxy hardening.
4. Service discovery reliability.
5. WAF.
6. mTLS.
7. Console v2.
8. GraphQL only as research/prototype.

GraphQL should be renamed:

> Y2-08 GraphQL Composition Prototype

Scope:

- build tag `graphql`,
- schema-first,
- explicit resolvers,
- REST/gRPC unary only,
- no federation,
- no subscriptions,
- no automatic universal conversion,
- depth/complexity limits from day one.

---

# 6. Year 3 review — Scale, Fleet & Ecosystem

## Assessment

Year 3 is directionally correct but overstuffed.

It tries to deliver:

- multi-node control plane,
- config sync,
- staged rollout,
- Kubernetes Ingress/Gateway API,
- RBAC/SSO/SAML/OIDC,
- distributed cache/rate limit,
- traffic management,
- hot binary upgrade,
- plugin marketplace,
- audit logging,
- Console v3.

That is too much for one year unless there is a large team.

## What is correct

### Minimal fleet control plane

A fleet control plane is a natural enterprise expansion if Jul wins single-node adoption.

It should focus first on:

- config distribution,
- validation,
- staged rollout,
- rollback,
- node status,
- audit.

### RBAC/SSO/audit

These are required for enterprise use.

They are more important than many technically exciting features.

### Kubernetes Ingress / Gateway API

This is a major adoption channel.

Gateway API support may be more important than a custom control plane because many teams already operate Kubernetes as their control plane.

### Traffic management

Canary, weighted split, blue-green, and mirroring fit Jul's proxy identity.

This is more core than marketplace or hot binary upgrade.

## What is too early

### Plugin marketplace

A marketplace is not useful until there is:

- a stable plugin ABI,
- SDKs,
- examples,
- plugin signing,
- security review,
- community pull.

Move this later.

### Distributed cache/rate limit

This is useful, but should be demand-gated.

Do not build distributed state before there are real fleet users.

### Hot binary upgrade

Useful for some operators, but lower priority than K8s, RBAC, audit, and traffic management.

## Recommendation

Rescope Year 3 to:

1. Kubernetes Gateway API / Ingress controller.
2. RBAC/SSO.
3. Audit logs.
4. Traffic management.
5. Minimal fleet control plane.
6. Console v3 for those features.

Move these later:

- distributed cache/rate limit,
- marketplace,
- hot binary upgrade.

---

# 7. Year 4 review — AI-native + Edge

## Assessment

AI Gateway is a plausible strategic bet, but the year is overloaded.

Year 4 currently includes:

- AI Gateway core,
- semantic caching,
- token rate limit and cost observability,
- guardrails,
- AI-assisted Console,
- edge compute / WASM FaaS,
- CDN-grade caching,
- image optimization,
- ESI,
- app templates,
- Early Hints,
- WebTransport,
- PQ-TLS.

That is too many categories.

## What is correct

### AI Gateway core

An AI Gateway can fit Jul if Jul already has traction as a protocol/API gateway.

The design idea is strong:

- OpenAI-compatible front door,
- Jul-native neutral model,
- provider adapters,
- routing and failover,
- streaming,
- metrics,
- provider keys redacted,
- build tag `ai`.

### Token/cost observability

This is more important than semantic cache.

Users buying AI gateways care about cost control.

### Guardrails

Guardrails fit the gateway role, especially if they can use WASM plugins for custom checks.

### AI-assisted Console

Natural-language-to-config can be valuable if it produces a validated diff and requires human approval.

## What is risky

### Semantic cache

Semantic caching sounds valuable but is dangerous.

A false-positive semantic hit can serve the wrong answer. That is worse than a cache miss.

This should be experimental, conservative, opt-in, and measurable.

### CDN-grade caching and image optimization

This drifts Jul toward Cloudflare/Caddy/CDN territory.

It is not necessary for the core Jul story.

### Edge FaaS

This risks turning Jul into Cloudflare Workers-lite.

WASM middleware/plugins are enough for now.

### WebTransport / PQ-TLS

These are protocol-frontier features, not adoption drivers for most users.

They should not compete with AI Gateway, fleet, or K8s work.

## Recommendation

Rescope Year 4 to:

1. AI Gateway core.
2. Token/cost observability.
3. Guardrails.
4. Provider failover/routing.
5. AI-assisted config diff.
6. Semantic cache as experimental.

Move out:

- CDN-grade caching,
- image optimization,
- ESI,
- WebTransport,
- PQ-TLS,
- full Edge FaaS.

---

# 8. Year 5 review — Global Scale, Mesh & Cloud

## Assessment

The cloud strategy is good if staged correctly.

The mesh/GSLB/security ambitions are too broad.

## What is correct

### Cloud starts thin

The best part of Year 5 is the decision to start Cloud as:

- hosted Console,
- hosted control plane,
- bring-your-own nodes,
- control traffic only,
- no customer data-plane traffic through Jul Cloud initially.

That is the right commercial strategy.

It avoids capital-heavy infrastructure and reduces trust barriers.

### Billing demand-gated

Billing should follow real usage, not precede it.

### RUM/SLO

RUM and SLO dashboards can be a useful Console extension.

## What is risky

### Service mesh

Service mesh is a massive product category.

A real mesh implies:

- identity,
- sidecars or ambient mode,
- transparent interception,
- SPIFFE/SVID,
- policy distribution,
- mTLS rotation,
- xDS or equivalent,
- observability,
- traffic policy,
- integration with Kubernetes,
- years of operational edge cases.

Competing with Istio, Linkerd, Envoy, Cilium, and cloud-native meshes is a major strategic commitment.

Do not put this in the core roadmap unless customers demand it.

### GSLB / authoritative DNS

GSLB can make sense for Cloud eventually, but authoritative DNS is operationally heavy.

It should be demand-gated behind multi-region customers.

### Bot/DDoS

Jul can credibly offer app-layer bot defense.

It should not imply network-layer DDoS scrubbing unless Jul operates edge PoPs or partners with a scrubbing provider.

## Recommendation

Rescope Year 5 to:

1. Hosted Console/control plane.
2. BYO node enrollment.
3. Tenant isolation.
4. Usage metering.
5. Billing.
6. RUM/SLO.
7. Secrets integrations.
8. Optional GSLB only if customers need multi-region.

Move out:

- full service mesh,
- ambient mesh,
- authoritative DNS unless demanded,
- bot/DDoS beyond L7.

---

# 9. Major missing pieces

## 9.1 Product maturity matrix

Every roadmap row needs:

- status,
- owner,
- maturity,
- target user,
- primary use case,
- GA criteria,
- known limitations,
- support level.

## 9.2 Benchmarks

For a proxy/gateway, benchmarks are product evidence.

Add benchmarks for:

- static file serving,
- reverse proxy,
- TLS termination,
- compression,
- gRPC passthrough,
- gRPC transcoding,
- L4 stream,
- service discovery churn,
- WASM plugin overhead,
- reload latency,
- memory under long-lived streams,
- binary size per build tag.

## 9.3 Conformance suites

Needed:

- gRPC transcoding conformance,
- gRPC passthrough trailers/streaming,
- HTTP proxy behavior,
- ACME smoke,
- HTTP/3 smoke,
- WASM ABI compatibility,
- Gateway API conformance when Kubernetes support arrives,
- WAF CRS test set when WAF lands.

## 9.4 Secrets story earlier

Secrets appear too late.

Jul already needs secrets for:

- TLS/ACME,
- upstream credentials,
- JWT/JWKS,
- forward-auth,
- OIDC/SAML,
- AI provider keys,
- Cloud enrollment,
- mTLS.

Introduce a simple secret reference interface earlier:

```toml
[secrets]
provider = "env" # env | file | vault | k8s
```

Start simple:

- env refs,
- file refs,
- redaction in logs,
- config validation that detects accidental literal secrets.

Add Vault/KMS later.

## 9.5 Edition / packaging model

Define product editions early:

- Jul Core / OSS lean,
- Jul Full build,
- Jul Enterprise,
- Jul Cloud Agent,
- Jul AI build.

Without this, build tags, enterprise gating, and cloud capabilities will confuse users.

## 9.6 Roadmap gates

Add explicit gates:

| Area | Evidence required |
| --- | --- |
| GraphQL | Users need BFF/composition over REST/gRPC |
| Fleet | Multiple users operate N Jul nodes |
| Distributed cache/rate limit | Real fleet users hit local-limit problems |
| AI Gateway | Jul is used as API/protocol gateway and users ask for AI routing/cost |
| Cloud | Self-hosted control plane demand exists |
| Mesh | Customers explicitly ask for east-west identity/policy |
| GSLB | Customers run multi-region Jul fleets |

---

# 10. What to cut, postpone, or reframe

## Cut from near-term roadmap

- universal REST/gRPC/GraphQL conversion,
- GraphQL without resolvers,
- full service mesh,
- ambient mesh,
- CDN-grade caching,
- ESI,
- image optimization,
- WebTransport,
- PQ-TLS,
- authoritative DNS/GSLB before multi-region demand,
- plugin marketplace before plugin ABI maturity.

## Postpone

- distributed cache/rate limit,
- hot binary upgrade,
- bot/DDoS beyond app-layer defense,
- semantic cache,
- REST -> GraphQL,
- gRPC -> GraphQL.

## Reframe

- GraphQL gateway -> GraphQL composition prototype.
- AI-native platform -> AI Gateway module.
- Edge FaaS -> richer WASM plugin capabilities.
- Global scale -> Cloud control plane first.
- Service mesh -> possible future integration, not committed core.

---

# 11. Recommended revised roadmap

## Phase 1 — Foundation GA

Goal: make the delivered core boringly reliable.

- stable config contract,
- benchmarks,
- CI/perf gates,
- conformance tests,
- security baseline,
- examples,
- release artifacts,
- docs,
- known limitations,
- semver policy.

## Phase 2 — Protocol Gateway hardening

- gRPC transcoding GA,
- native gRPC passthrough GA,
- WASM plugin ABI v1,
- L4 stream proxy hardening,
- service discovery hardening,
- Console operational improvements.

## Phase 3 — Security and Kubernetes adoption

- WAF,
- mTLS,
- Gateway API / Ingress controller,
- secrets refs,
- audit logs,
- traffic management,
- Console v2/v3 for these workflows.

## Phase 4 — Enterprise control plane

- minimal fleet control plane,
- node enrollment,
- staged rollout,
- rollback,
- RBAC/SSO,
- audit,
- config history,
- optional distributed rate/cache only if demanded.

## Phase 5 — Ecosystem

- plugin SDK,
- ABI compatibility contract,
- signed plugin packaging,
- examples,
- registry only after community pull.

## Phase 6 — AI Gateway

- OpenAI-compatible gateway,
- neutral internal model,
- provider routing/failover,
- streaming,
- token/cost observability,
- guardrails,
- AI-assisted validated config diff,
- semantic cache experimental.

## Phase 7 — Cloud

- hosted Console/control plane,
- BYO nodes,
- tenant isolation,
- metering/billing,
- RUM/SLO,
- control traffic only,
- no anycast/data-plane until demand is proven.

---

# 12. Final recommendation

Jul should not try to become every infra product at once.

The strongest path is:

1. Win as a lean NGINX successor.
2. Differentiate with gRPC/protocol gateway support.
3. Build extensibility through WASM.
4. Become credible for platform teams through security, K8s, observability, and traffic management.
5. Add enterprise fleet control only after users operate multiple nodes.
6. Add AI Gateway only when the protocol gateway story is already trusted.
7. Add Cloud as hosted control plane, not as global data-plane network.
8. Leave service mesh, CDN-grade edge, and universal conversion outside the main roadmap unless users pull them in.

The roadmap should become less impressive on paper and more credible in execution.

A tighter product promise:

> Jul is a lean, single-binary edge and protocol gateway: easy enough for one server, serious enough for platform teams, extensible without becoming heavy.

That is the product worth building.
