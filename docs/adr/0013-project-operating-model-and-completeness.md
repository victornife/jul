# ADR 0013 — Project operating model, core completeness, and technical experiments

- **Status:** Accepted
- **Date:** 2026-08-03
- **Deciders:** Jul.IA maintainer
- **Applies to:** roadmap entry, issue classification, implementation sequencing, experiments, and programme closure
- **Supersedes/amends:** the demand-only interpretation of [ADR 0003](0003-maturity-and-ga.md)
- **Related:** [operating model](../operating-model.md), [Core Gateway Completeness](../specs/core-gateway-completeness.md), [combined audit](../audit/combined-audit-2026-08-03.md), #62

> **Document roles:** ADR 0013 decides how work enters the portfolio; ADR 0014 decides the required operator/developer surfaces; `docs/operating-model.md` defines execution discipline; `docs/specs/core-gateway-completeness.md` defines the bounded product; the roadmap and #62 own current order and status.

## Context

Jul.IA is currently a solo, AI-assisted engineering product. The objective is to
build an unusually complete, coherent and production-quality standalone gateway,
learn from difficult systems problems, and preserve a credible product rather
than optimise primarily for customer acquisition or rapid category expansion.

The previous roadmap and maturity model correctly required evidence before large
commitments, but they left three ambiguities:

1. obvious correctness and core gateway gaps could appear to require external
   demand before being fixed;
2. operational enhancements and category-expansion experiments were mixed into
   one sequential roadmap;
3. a time-boxed bet had no sufficiently explicit scope, maintenance, extraction,
   or removal contract.

This made Phase 5, the AI Gateway proposal, and the universal-looking hot-reload
programme compete to be the single next phase.

## Decision

### 1. Use five portfolio lanes

Every material issue belongs to one primary lane:

| Lane | Meaning | Entry rule |
| --- | --- | --- |
| **Correctness and security** | Current behavior violates a documented, protocol, security, compatibility, or operational contract | Evidence and severity; no demand gate |
| **Core Gateway Completeness** | Missing capability materially weakens the bounded standalone gateway | Architecture/product-integrity evidence; no customer-acquisition gate |
| **Operational enhancement** | Improves availability, diagnostics, maintainability, or operator workflow without defining core completeness | Value, learning, effort, and permanent complexity |
| **Technical experiment** | Bounded exploration outside the current core | Written hypothesis, fixed tranche, budget, prerequisites, and exit decision |
| **Vision horizon** | Large category retained as long-term direction but not active delivery | Separate activation decision |

### 2. Define core completeness relative to a bounded product

The canonical boundary is [Core Gateway Completeness](../specs/core-gateway-completeness.md).
It includes coherent single-node answers for ingress protocols, routing,
transport trust, resilience, security policy, configuration lifecycle,
automation, observability, operation, migration, extensibility, and supported
release profiles.

It does **not** require:

- fleet or hosted control planes;
- distributed cache/rate limiting;
- Kubernetes/Gateway API controllers;
- service mesh or GSLB;
- GraphQL composition;
- AI Gateway;
- every structural setting to become hot-reloadable;
- feature parity with NGINX, Envoy, Caddy, Traefik, Kong, or any other product.

### 3. Prioritise value and leverage, not feature count

Prioritisation considers:

- correctness and security impact;
- product-completeness impact;
- architectural leverage and reuse;
- operator and developer value;
- learning and demonstrability;
- implementation effort;
- permanent maintenance, compatibility, test, and documentation cost;
- regression and build-profile surface.

Feature-count growth, competitor parity, or the existence of an open issue are
not sufficient reasons to implement work.

### 4. One major category experiment at a time

A technical experiment requires:

- a precise technical hypothesis;
- fixed first-tranche scope and explicit exclusions;
- architecture prerequisites;
- time, dependency, binary-size, and maintenance budgets;
- evidence to collect;
- success and stop criteria;
- an explicit final outcome: **promote, continue as experimental, freeze,
  extract, remove, or defer**.

An experiment does not automatically enter the supported core. Correctness and
security work may interrupt it.

### 5. Runtime dynamics are value-ranked

The objective is not to make every configuration field hot-reloadable. A field
moves only when the documented transition boundary is real, transactionally
safe, sufficiently valuable, and supportable by the solo-maintainer test matrix.
A complete planned-restart path is a valid product outcome for structural or
low-frequency settings.

### 6. External demand remains useful, not universally mandatory

External users, requests, pilots, and production evidence remain valuable inputs
for fleet, cloud, distributed, or commercially expensive commitments. They are
not prerequisites for:

- fixing a current defect;
- closing a material core gateway gap;
- a bounded maintainer-sponsored technical experiment.

## Consequences

### Positive

- Core product integrity can advance without artificial market gates.
- Experiments are permitted without silently becoming permanent product scope.
- Operational work such as hot reload can be selected by practical value.
- The roadmap can show parallel portfolio lanes without implying parallel
  implementation by one maintainer.
- Programme closure can distinguish implemented, deliberately restart-bound,
  deferred, and experimental work.

### Negative / accepted trade-offs

- Prioritisation requires explicit judgment rather than a single mechanical
  demand threshold.
- Some ambitious issues will remain open but gated, which requires disciplined
  status labels and dependency notes.
- Experiments may be removed after meaningful work; that is accepted as part of
  the learning model.

## Alternatives considered

### Continue a fixed Phase 5 → AI → horizon sequence

**Pros:** simple narrative and sequencing.

**Cons:** treats one experiment as automatic, conflicts with correctness/core
work, and leaves the hot-reload programme competing for the same slot.

**Rejected because:** it does not reflect current repository risk or the
maintainer's actual objective.

### Demand-gate every major capability

**Pros:** strong scope discipline and market orientation.

**Cons:** inappropriate for core correctness and a solo technical product;
prevents valuable architecture experiments without customer evidence.

**Rejected because:** external acquisition is not the current optimisation goal.

### Treat every technically interesting capability as core

**Pros:** maximum freedom and feature breadth.

**Cons:** makes "complete" infinite, encourages duplicated subsystems, and
creates an unsustainable compatibility/test burden.

**Rejected because:** completeness must be bounded to preserve coherence.

## Implementation consequences

- #62 is the single master programme and separates historical delivery from the
  current portfolio.
- #88 is a value-ranked runtime-dynamics portfolio, not a universal completion
  mandate.
- AI remains a `[DRAFT]` bounded experiment until its prerequisites and entry
  decision are complete.
- Core implementation issues remain `[DRAFT]` until their governing ADRs merge.
- [ADR 0003](0003-maturity-and-ga.md) remains authoritative for maturity and GA
  evidence, as amended by this ADR for work-entry mechanisms.
- [ADR 0014](0014-operability-surfaces.md) defines the appropriate Console/API/CLI
  surface for each capability type.

## Review triggers

Revisit this decision when:

- multiple maintainers require a more formal planning process;
- Jul.IA adopts an explicit commercial-growth objective;
- the bounded single-node product is declared complete and only horizons remain;
- experiment cleanup or build-profile cost becomes materially unsustainable.
