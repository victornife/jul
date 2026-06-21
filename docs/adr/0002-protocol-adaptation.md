# ADR 0002 — Protocol adaptation strategy (explicit adapters, not universal conversion)

- **Status:** Accepted
- **Date:** 2026-06-21
- **Deciders:** Jul.IA maintainers
- **Applies to:** REST/JSON, gRPC, and GraphQL gateway features (Y1-06, Y2-01, Y2-04, Y2-08, and any future protocol adapter)
- **Source:** [review 2026-06-21](../reviews/) — protocol-adapters product strategy and roadmap critical review

## Context

Jul.IA's routing model is **location → action**: each route selects a bounded
behavior (static serving, proxying, gRPC transcoding, redirect, deny, plugin
handler, …). This fits *explicit, bounded* protocol adapters well. It fits a
generic "any protocol ↔ any protocol" converter poorly, because REST, gRPC, and
GraphQL do not share one mental model: REST/gRPC are largely operation-to-
operation, while GraphQL is a query/composition runtime, not a transport action.

A recurring temptation for a protocol gateway is to market "universal REST ⇄ gRPC
⇄ GraphQL conversion". That promise is attractive but is poor product
architecture: it inherits every edge case of every protocol in every direction,
sets impossible expectations, and fights Jul.IA's lean, build-tagged design. The
2026-06-21 review recommended a narrower, more credible posture. This ADR records
the decision and the rule future protocol features follow.

## Decision

**Support explicit adapters for common migration paths; do not promise magical
conversion between arbitrary protocols.** Concretely:

1. **REST/JSON → gRPC transcoding + native gRPC passthrough is the lead protocol
   line (P0).** It is the cleanest fit with the `location → action` model and the
   strongest differentiator versus a plain NGINX replacement. It is the first
   feature targeted for the **GA** maturity bar (see
   [ADR 0003](0003-maturity-and-ga.md)).
2. **GraphQL enters only as an explicit, schema-first composition layer** behind a
   `graphql` build tag — **never** as auto-generated conversion. Scope when built:
   schema-first, explicit resolvers, Query/Mutation over gRPC/REST **unary**
   first, depth/complexity limits and resolver-level tracing from day one. No
   federation, no subscriptions, no "generate all GraphQL from proto".
3. **Prefer explicit mappings over inference**, schema/config validation over
   runtime surprise, and protocol adaptation at the edge over business-logic
   orchestration. Heavy capabilities stay behind build tags.

**Rejected as core scope** (record as non-goals): a universal REST/gRPC/GraphQL
conversion matrix; "GraphQL without writing resolvers"; and a `gRPC → GraphQL`
client adapter in core (if ever needed, via plugin/sidecar).

**Process rule:** GraphQL and other composition adapters are **demand-gated** —
see the GraphQL evidence gate in [ADR 0003](0003-maturity-and-ga.md). Build them
only when users need BFF/composition over existing REST/gRPC.

## Rationale

- **Architectural fit.** Explicit, bounded adapters compose with `location →
  action`; a generic conversion runtime does not.
- **Credibility over marketing.** A narrower promise ("explicit adapters for real
  migration paths") is believable and deliverable; "universal conversion" is
  neither and would dilute the product.
- **Bounded scope.** Explicit mappings keep the edge-case surface finite, which
  matters acutely at the project's current capacity.
- **Differentiation is already here.** REST→gRPC transcoding + passthrough is the
  feature that separates Jul.IA from "just another reverse proxy".

## Consequences

**Positive**

- A clear, defensible positioning line: *explicit protocol adapters for the
  migration paths platform teams actually use.*
- Finite, testable scope per adapter (enables the conformance-matrix GA bar).
- GraphQL, if built, arrives correctly scoped instead of as an unbounded promise.

**Negative / trade-offs**

- Jul.IA explicitly will **not** advertise universal conversion, conceding that
  marketing line to others.
- Some niche adapters (gRPC→GraphQL, REST→GraphQL facade) are deferred or pushed
  to plugins/sidecars, so they are not turnkey in core.

## Alternatives considered

- **Universal REST/gRPC/GraphQL conversion matrix** — rejected: unbounded scope,
  impossible expectations, fights the lean/build-tag architecture.
- **GraphQL auto-generated from proto/OpenAPI ("without resolvers")** — rejected:
  hides composition semantics (N+1, partial failure, auth) that must be explicit.
- **Ship GraphQL composition now** — deferred: no current demand pull; gated on
  the GraphQL evidence gate in [ADR 0003](0003-maturity-and-ga.md).

## Review triggers

Revisit this ADR if any of the following hold:

- Multiple users need BFF/composition over existing REST/gRPC (trips the GraphQL
  evidence gate) — then promote the GraphQL composition prototype.
- A concrete, recurring need for a deferred adapter (REST→GraphQL facade,
  gRPC→GraphQL) appears with at least one design partner.
