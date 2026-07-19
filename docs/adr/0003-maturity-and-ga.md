# ADR 0003 — Maturity model, GA bar, and evidence gates

- **Status:** Accepted — *criterion 5 (soak test) amended by [ADR 0005](0005-soak-post-ga-gate.md)* (reclassified to a post-GA gate)
- **Date:** 2026-06-21
- **Deciders:** Jul.IA maintainers
- **Applies to:** every roadmap feature and its status labeling
- **Source:** [review 2026-06-21](../reviews/) — roadmap/vision/specs critical review

## Context

The roadmap previously used a binary label — *Planned* vs **Delivered ✅** — for
every feature. A feature being *implemented* is not the same as being production
ready, documented, benchmarked, edge-case-hardened, contract-stable, or safe to
call GA. Labeling implemented-but-immature work as "Delivered" overstates
maturity and erodes trust the moment a user hits a gap. A live symptom: gRPC
transcoding streaming is implemented in `internal/transcode/streaming.go`
(server/client/bidi, NDJSON/SSE), yet a handler comment still described only
"unary" mapping — exactly the kind of drift a maturity discipline catches.

Separately, the roadmap listed many large categories (GraphQL, fleet control
plane, Kubernetes, distributed state, AI gateway, mesh, CDN, GSLB, Cloud) with no
explicit evidence required before entering each. Without gates, a roadmap becomes
a wish list. This is acute at the project's current capacity (solo, part-time),
where scope discipline is existential.

## Decision

### 1. Maturity ladder

Every feature carries one of these states (shown as a **Maturity** column in the
[roadmap](../roadmap/)):

| State | Meaning |
| --- | --- |
| **Planned** | Designed but not implemented |
| **Prototype** | Works in a narrow lab case |
| **Alpha** | Usable internally; config/API may change |
| **Beta** | Usable by early adopters; known limitations |
| **GA** | Stable, documented, tested, supported |
| **Deprecated** | Will be removed or replaced |

Implemented-but-unhardened features are **Beta**, not GA. Accordingly, the
delivered Year-2 features (Y2-01…Y2-05) are reclassified from "Delivered" to
**Beta** until they meet the GA bar below.

### 2. GA bar (all criteria mandatory)

A feature is labeled **GA** only when **all** of the following are true:

1. **Conformance matrix published** — supported behaviors explicitly enumerated.
2. **Published benchmark numbers.**
3. **Documented known-limitations list.**
4. **Stable config/API contract**, semver-guarded.
5. **Long-running soak test passed.**
6. **Runnable example + docs.**
7. **Security review / threat note** for the feature.
8. **Fuzzing** where parsing is involved.
9. **Self-explanatory Console surface** — the feature is operable and observable
   from the Console without reading docs (the *Operable by design / Console-first*
   invariant; see [ADR 0004](0004-console-ui-invariants.md)).

Because this bar is deliberately high and capacity is limited, **most features
will sit at Beta**, and **GA is a rare, earned milestone**. The first GA target is
**REST/JSON → gRPC transcoding + native gRPC passthrough** (it has demand pull and
is the core differentiator). Criteria 1, 6, and 9 also form the minimum
**Definition of Done** for any user-facing feature at Beta.

### 3. Evidence gates (before entering a category)

Major expansions are **demand-gated**: do not commit roadmap capacity to a
category until its evidence exists. The long-term vision stays broad; the
*operating* roadmap stays narrow.

| Category | Evidence required before committing |
| --- | --- |
| **GraphQL** | Users need BFF/composition over existing REST/gRPC |
| **Fleet** | Multiple users operate N Jul.IA nodes |
| **Kubernetes/Gateway API** | Real ingress-controller demand |
| **Distributed cache/rate-limit** | Real fleet users hit local-limit problems |
| **AI Gateway** | Jul.IA is used as an API/protocol gateway and users ask for AI routing/cost |
| **Cloud** | Self-hosted control-plane demand exists |
| **Service mesh** | Customers explicitly ask for east-west identity/policy |
| **GSLB** | Customers run multi-region Jul.IA fleets |

The operating roadmap now publishes quantitative thresholds and a lightweight
product-signal table for each of these categories; see
[docs/roadmap/README.md#evidence-gates-and-product-signals](../roadmap/README.md#evidence-gates-and-product-signals).

A **Time-boxed bet** is the sanctioned exception: a category may be entered ahead
of its gate as a small, thin, time-boxed MVP with an explicit kill/continue
decision (current example: the AI Gateway MVP behind the `ai` tag).

## Consequences

**Positive**

- Honest status communication; "Beta" sets correct expectations and protects
  trust. This directly resolves the review's top credibility concern.
- A concrete, auditable definition of "done" and "GA".
- Gates convert the roadmap from a wish list into evidence-driven commitments.

**Negative / trade-offs**

- The roadmap looks "less impressive on paper" (few GA labels) — accepted, in
  exchange for execution credibility.
- Maintaining conformance matrices, benchmarks, and soak tests is real ongoing
  cost; it is the price of the GA label.

## Alternatives considered

- **Keep the binary Planned/Delivered model** — rejected: conflates "implemented"
  with "production-ready" and overstates maturity.
- **Restructure the roadmap from Years into Phases 1–7** (review proposal) —
  reframed, not adopted: we keep the year structure (history + 5-year narrative)
  and instead add the Maturity column, gates, and reprioritization within years.
- **Gate every category with no exceptions** — softened: a *Time-boxed bet* escape
  hatch avoids chicken-and-egg paralysis for deliberate founder bets.

## Review triggers

- A category's evidence gate trips → move it from "Vision horizon" into the
  committed roadmap with a Maturity state.
- A feature meets all GA criteria → promote Beta → GA and record it.
- The GA bar proves impractical for edge features → consider a tiered bar
  (flagship vs. peripheral) in a superseding ADR.
