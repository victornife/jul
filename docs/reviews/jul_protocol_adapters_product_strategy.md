# Jul.IA Protocol Adapters: Product Strategy and Roadmap Recommendation

> **See also (2026-07-01).** Current repository state is tracked in the consolidated
> [Full Repository Audit (2026-07)](jul_full_repository_audit_2026-07.md) (single
> source of truth). This document remains a valid historical strategy input.

> **Reviewed — 2026-06-21 ✅** · Status: **Adopted (with scoping).** This external
> review was evaluated and accepted; its recommendations are now durable decisions
> in [ADR 0002 — Protocol adaptation](../adr/0002-protocol-adaptation.md) and
> [ADR 0003 — Maturity & GA](../adr/0003-maturity-and-ga.md). See the
> [reviews & decision log](README.md) for the full adopted / reframed / deferred /
> rejected mapping. The original text is preserved below unchanged.

**Context:** evaluation of whether Jul should support adaptation between REST, gRPC and GraphQL.  
**Audience:** Jul founder / core team.  
**Position:** technical product management review.

---

## Executive recommendation

Jul should **double down on explicit REST/JSON -> gRPC transcoding and native gRPC passthrough** as a core strategic line.

Jul should **not** position itself as a universal bidirectional converter between REST, gRPC and GraphQL. That promise is attractive in marketing, but poor as product architecture: REST, gRPC and GraphQL do not share the same mental model.

GraphQL can enter Jul, but only as a **controlled composition layer with explicit resolvers**, not as a generic conversion system.

The recommended positioning is:

> Jul is a lean edge server and explicit protocol gateway for real migration paths: serve, protect, observe and adapt HTTP/gRPC traffic without becoming a heavyweight platform.

---

## What Jul appears to be today

Jul.IA appears to be an NGINX-inspired HTTP edge server written in Go and configured through TOML. Its natural product shape is a single-binary edge server with reverse proxying, load balancing, static serving, app gateways, response cache, TLS, hot reload, admin/observability, gRPC capabilities, WASM extensibility and L4 stream proxy support.

The important architectural pattern is **location -> action**: each route/location selects a behavior such as static serving, proxying, gRPC transcoding, redirect/return, deny, plugin handler, etc. This model fits protocol adapters well when the adapter is explicit and bounded. It fits GraphQL less naturally because GraphQL is not just a transport action: it is a query execution and composition runtime.

Core strengths identified:

- Single static binary, Go-first, no-cgo posture.
- TOML configuration with validation and hot reload.
- Build tags for heavy features.
- Reverse proxy, upstream pools, health checks and service discovery.
- gRPC transcoding and native gRPC passthrough.
- Admin/observability surface.
- WASM plugin extensibility.

Core risk:

- The roadmap is very ambitious and can easily push Jul from “lean protocol gateway” into “generic infrastructure platform competing with everyone”.

---

## Strategic rule

Use this rule for protocol adaptation:

> Jul should support explicit adapters for common migration paths, not magical conversion between arbitrary protocols.

That means:

- Prefer explicit mappings over inference.
- Prefer small MVPs over broad matrices.
- Prefer schema/config validation over runtime surprise.
- Prefer protocol adaptation at the edge over business-logic orchestration.
- Keep heavy capabilities behind build tags.

---

## A. REST <-> gRPC adapter

### Problem solved

Expose internal gRPC services to REST/JSON clients, partners, frontends or legacy systems without writing a bespoke gateway service for each API.

### User

- Backend teams moving from REST to gRPC.
- Platform teams managing ingress/API gateway.
- API teams publishing REST APIs backed by gRPC services.
- Organizations migrating incrementally.

### Fit with Jul

**High.** This is the cleanest fit with Jul's current architecture and positioning. Jul already has the edge proxy primitives, upstream pools, health checks, auth, metadata propagation, observability and gRPC concepts needed to make this credible.

### Impact

**High.** It is easy to explain, strategically differentiated from a simple NGINX replacement, and directly useful in real migrations.

### Effort

**Medium-high.** The basic shape is tractable, but production quality requires hardening: streaming, metadata, trailers, error mapping, descriptor/reflection loading, OpenAPI tooling, examples, linting and observability.

### Risk

**Medium.** Risk is manageable if the product avoids promising “universal bidirectional conversion”.

### Recommendation

**Include now. P0.**

But phrase it as:

- REST/JSON -> gRPC transcoding.
- Native gRPC passthrough.
- Optional future gRPC facade over REST only if strong demand appears.

Do not call this “REST <-> gRPC universal conversion”.

---

## B. GraphQL -> gRPC resolver layer

### Problem solved

Expose a GraphQL API where fields resolve through internal gRPC services. Useful for frontend BFF/composition over microservices.

### User

- Frontend platform teams.
- API platform teams.
- Backend teams with gRPC services.
- Organizations wanting a GraphQL facade without a custom BFF service.

### Fit with Jul

**Medium-high, but only as explicit composition.** It aligns with the “protocol gateway” ambition, but it is not a simple protocol mapping. It requires schema execution, resolver semantics, batching, auth, limits and observability per resolver.

### Impact

**Potentially high, but demand-dependent.** Strong in organizations with gRPC-first backends and frontend GraphQL needs. Weak if Jul's adoption is primarily edge/proxy users.

### Effort

**High.** GraphQL adds execution semantics, N+1 risks, DataLoader-like batching, partial failures, field-level authorization, depth/complexity controls and resolver-level tracing.

### Risk

**High.** It can drag Jul into Apollo/Kong/GraphQL Mesh territory.

### Recommendation

**Explore with prototype. P2.**

Scope tightly:

- Schema-first.
- Explicit resolvers.
- Query/Mutation only.
- gRPC unary first.
- No federation.
- No subscriptions.
- No automatic “generate all GraphQL from proto”.
- Depth/complexity limits from day one.
- Resolver-level tracing from day one.

---

## C. GraphQL -> REST resolver layer

### Problem solved

Expose GraphQL over legacy REST APIs, allowing a frontend to query a graph while Jul calls REST backends.

### Fit with Jul

**Medium.** Jul understands HTTP very well, but GraphQL-over-REST is more composition/runtime than proxying.

### Impact

**Medium.** Useful for legacy-heavy organizations, but less differentiating than gRPC transcoding.

### Effort

**Medium-high.** REST endpoints vary widely in errors, pagination, auth, response shape, versioning and caching.

### Risk

**Medium-high.** Declarative mappings can become a fragile integration language.

### Recommendation

**Prototype later. P2/P3.**

Only with explicit resolvers. Do not claim automatic REST-to-GraphQL conversion.

---

## D. REST -> GraphQL facade

### Problem solved

Expose stable REST endpoints that internally execute predefined GraphQL queries or mutations.

### Fit with Jul

**Medium-low.** Reasonable as an edge facade, but it depends on having GraphQL client/execution support first.

### Impact

**Medium.** Useful for clients that cannot consume GraphQL directly.

### Effort

**Medium** if limited to predefined operations.

### Risk

**Medium.** Safe only if arbitrary GraphQL queries are not allowed.

### Recommendation

**Postpone. P3.**

If built, support only predefined/persisted operations with explicit variable mapping.

---

## E. gRPC -> GraphQL client adapter

### Problem solved

Expose a gRPC contract that internally calls GraphQL operations, usually for integration with SaaS GraphQL APIs.

### Fit with Jul

**Low.** This makes Jul behave like an application adapter/gRPC server rather than an edge gateway.

### Impact

**Low-medium.** Real but niche.

### Effort

**High.** Proto request -> GraphQL variables -> GraphQL response -> proto response is brittle, especially around nullability, errors and versioning.

### Risk

**High.** Hard to explain and likely to create product confusion.

### Recommendation

**Discard for now. P4.**

If demand appears, implement through plugin/sidecar or a very narrow experimental adapter.

---

## F. Universal REST <-> gRPC <-> GraphQL conversion

### Problem solved

The apparent problem is “we have many protocols and want everything to talk to everything”. The real problem is usually unclear API ownership and migration strategy.

### Fit with Jul

**Low.** It fights Jul's lean, explicit and build-tagged architecture.

### Impact

**Low real impact, high marketing temptation.**

### Effort

**Very high.** You inherit every edge case from every protocol and every direction.

### Risk

**Very high.** It dilutes Jul, creates unbounded scope and sets impossible expectations.

### Recommendation

**Discard for now. P4.**

Use this wording instead:

> Explicit protocol adapters for common migration paths.

---

## Comparative table

| Functionality | Fit | Impact | Effort | Risk | Recommendation | Priority |
| --- | ---: | ---: | ---: | ---: | --- | ---: |
| REST/JSON -> gRPC + gRPC passthrough | High | High | Medium-high | Medium | Include now | P0 |
| GraphQL -> gRPC resolvers | Medium-high | High if demand exists | High | High | Prototype | P2 |
| GraphQL -> REST resolvers | Medium | Medium | Medium-high | Medium-high | Prototype later | P2/P3 |
| REST -> GraphQL facade | Medium-low | Medium | Medium | Medium | Postpone | P3 |
| gRPC -> GraphQL adapter | Low | Low-medium | High | High | Discard for now | P4 |
| Universal conversion matrix | Low | Low real | Very high | Very high | Discard | P4 |

---

## Recommended roadmap for this product line

### Phase 1: Explicit and safe adapter

Build/harden first:

- REST/JSON -> gRPC via descriptor set or reflection.
- Native gRPC passthrough.
- Path/query/body mapping.
- JSON <-> Protobuf translation.
- HTTP <-> gRPC error mapping.
- Auth/metadata propagation.
- Deadlines/timeouts.
- Streaming opt-in.
- Per-method metrics/tracing.
- `jul lint` checks for adapter config.
- Complete runnable examples.

### Phase 2: Controlled composition

Only after traction:

- GraphQL schema-first runtime behind a `graphql` build tag.
- Explicit GraphQL resolvers.
- GraphQL -> gRPC unary resolvers.
- GraphQL -> REST basic resolvers.
- Depth and complexity limits.
- Resolver-level timeout.
- Resolver-level tracing.
- Auth propagation.
- Optional batching only where explicitly configured.

### Phase 3: Advanced facades and tooling

Later:

- REST -> GraphQL facade over predefined operations.
- Partial config generation from Protobuf/OpenAPI/GraphQL schema.
- Console mapping designer.
- Plugin hooks for transforms.

### Out of roadmap unless strong evidence appears

- Universal REST/gRPC/GraphQL conversion.
- Arbitrary GraphQL as if it were REST.
- Full Apollo federation compatibility.
- gRPC -> GraphQL in core.
- Magic generation without explicit mappings.

---

## Smallest MVP to validate value

The smallest useful MVP is not GraphQL. It is a polished REST/JSON -> gRPC demo:

```toml
[[upstreams]]
name = "users-grpc"
strategy = "round_robin"
servers = ["127.0.0.1:50051"]

[[servers]]
listen = "0.0.0.0:8080"
server_names = ["localhost"]

  [[servers.locations]]
  match = { type = "prefix", path = "/v1/" }

    [servers.locations.grpc_transcode]
    target = "users-grpc"
    descriptor_set = "/etc/jul/users.pb"
    streaming = false
    max_message_size = "4m"
```

Validation criteria:

- `GET /v1/users/{id}` calls `GetUser`.
- `POST /v1/orders` calls `CreateOrder`.
- gRPC errors map to HTTP correctly.
- Authorization propagates as metadata.
- Tracing shows HTTP request and gRPC method.
- The example runs locally with one command.

---

## Final recommendation

Make gRPC transcoding excellent before expanding into GraphQL.

GraphQL should be treated as an explicit composition feature, not a conversion feature.

The strategic mistake to avoid is turning Jul into a universal protocol-conversion platform. The stronger product is narrower and more credible:

> A lean edge server that supports explicit, observable protocol adapters for the migration paths platform teams actually use.
