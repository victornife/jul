# ADR 0003 — Maturity model, GA bar, and evidence gates

- **Status:** Accepted — *criterion 5 amended by [ADR 0005](0005-soak-post-ga-gate.md); work-entry mechanisms amended by [ADR 0013](0013-project-operating-model-and-completeness.md)*
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

> **Amendment (ADR 0013):** demand evidence remains a valid activation route for
> fleet, cloud, distributed, and other expensive category commitments. It is not
> a prerequisite for fixing current defects, closing a material standalone
> gateway gap, or beginning a bounded maintainer-sponsored technical experiment.
> Those work-entry rules are defined by [ADR 0013](0013-project-operating-model-and-completeness.md).

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
9. **Appropriate operability surface** — runtime operator features are operable
   and observable from the Console; automation and migration capabilities use
   their appropriate API/CLI-first surfaces. See [ADR 0004](0004-console-ui-invariants.md)
   and [ADR 0014](0014-operability-surfaces.md).

Because this bar is deliberately high and capacity is limited, **most features
will sit at Beta**, and **GA is a rare, earned milestone**. The first GA target is
**REST/JSON → gRPC transcoding + native gRPC passthrough** (it has demand pull and
is the core differentiator). Criteria 1, 6, and 9 also form the minimum
**Definition of Done** for any user-facing feature at Beta.

### 3. Evidence gates (before entering a category)

Major expansions are **demand-gated** when they require a durable supported
category commitment. The long-term vision stays broad; the operating roadmap
stays narrow. ADR 0013 additionally permits core-completeness work and bounded
technical experiments without customer-acquisition evidence.

| Category | Evidence required before committing as supported product |
| --- | --- |
| **GraphQL** | Users need BFF/composition over existing REST/gRPC |
| **Fleet** | Multiple users operate N Jul.IA nodes |
| **Kubernetes/Gateway API** | Real ingress-controller demand |
| **Distributed cache/rate-limit** | Real fleet users hit local-limit problems |
| **AI Gateway** | Promotion beyond a bounded experiment requires technical fit, sustainable maintenance, and a separate support decision |
| **Cloud** | Self-hosted control-plane demand exists |
| **Service mesh** | Users explicitly ask for east-west identity/policy |
| **GSLB** | Users run multi-region Jul.IA fleets |

The operating roadmap publishes the active portfolio and a lightweight product
signal table; see [docs/roadmap/README.md](../roadmap/README.md).

A **bounded technical experiment** is the sanctioned non-demand route: a
category may be explored through a small fixed tranche with explicit hypothesis,
budget, exclusions, evidence, cleanup, and promote/freeze/extract/remove
outcome. See ADR 0013.

## Consequences

**Positive**

- Honest status communication; "Beta" sets correct expectations and protects
  trust.
- A concrete, auditable definition of "done" and "GA".
- Gates convert the roadmap from a wish list into evidence-driven commitments.
- Core correctness is not blocked by an artificial market-demand requirement.
- Technical experiments have an explicit containment and exit contract.

**Negative / trade-offs**

- The roadmap looks "less impressive on paper" (few GA labels) — accepted, in
  exchange for execution credibility.
- Maintaining conformance matrices, benchmarks, and soak tests is real ongoing
  cost; it is the price of the GA label.
- Work-entry classification requires deliberate judgment rather than a single
  universal gate.

## Alternatives considered

- **Keep the binary Planned/Delivered model** — rejected: conflates "implemented"
  with "production-ready" and overstates maturity.
- **Restructure the roadmap from Years into Phases 1–7** — historical proposal,
  superseded for current execution by the portfolio/stage model in ADR 0013 and
  #62 while the year documents remain useful horizons.
- **Gate every category and every capability with external demand** — rejected:
  inappropriate for current defects, core gateway completeness, and a solo
  technical product.
- **Allow unbounded founder bets** — rejected: every experiment needs a fixed
  tranche, budget, evidence, and exit decision.

## Review triggers

- A category's evidence gate trips → consider moving it from vision horizon into
  a supported-product decision.
- A bounded experiment completes → promote, continue as experimental, freeze,
  extract, remove, or defer explicitly.
- A feature meets all GA criteria → promote and record exact evidence.
- The GA bar proves impractical for edge features → consider a tiered bar in a
  superseding ADR.
