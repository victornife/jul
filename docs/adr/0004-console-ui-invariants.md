# ADR 0004 — Console-first / UI invariants (Operable by design)

- **Status:** Accepted — *scope of the every-capability Console obligation amended by [ADR 0014](0014-operability-surfaces.md)*
- **Date:** 2026-06-21
- **Deciders:** Jul.IA maintainers
- **Applies to:** the admin Console and runtime operator capabilities
- **Source:** [review 2026-06-21](../reviews/) — product direction follow-up (UI invariant)

## Context

Jul.IA's "Friendliest" pillar is embodied by the web Console
(`internal/admin`, `console` build tag). The Console is not a peripheral feature —
it is core to the product identity: *anyone should be able to operate Jul.IA
easily.* As capabilities grow (WAF, mTLS, gRPC, AI gateway, …), there is a
standing risk that the UI accretes knobs, becomes cluttered, or lags behind new
features so that capabilities are reachable only by editing TOML. That would
break the friendliness pillar.

This ADR elevates the Console from "a feature" to a **non-negotiable invariant**
that constrains how runtime operator features ship.

> **Amendment (ADR 0014):** migration, CI, schema-generation, remote automation,
> controller, and diagnostic capabilities use their appropriate API/CLI-first
> surfaces. Runtime behavior remains server-owned and Console-operable; machine-
> oriented tools are not forced into artificial graphical workflows. See
> [ADR 0014](0014-operability-surfaces.md).

## Decision

**Operable by design / Console-first for runtime operation.** The following
invariants hold:

1. **Every runtime operator capability ships with a lean, self-explanatory
   Console surface.** A new operator understands state, risk, permissions and
   the common workflow without reading source code.
2. **No runtime feature is done until it is operable and observable from the
   Console.** This is part of Definition of Done and the appropriate-operability
   GA criterion in [ADR 0003](0003-maturity-and-ga.md). Machine-native tooling is
   governed by ADR 0014 instead.
3. **Power through clarity, not more knobs.** Added capability must not add
   unnecessary cognitive load; prefer sensible defaults, progressive disclosure,
   and curated forms over exposing every raw setting.
4. **Lean delivery is preserved.** The rich Console remains a small embedded SPA
   served via `go:embed`, stays behind the `console` build tag, and is default in
   the Full profile; the lean profile remains free of the SPA. Embedded UI assets
   carry a size budget.
5. **Continuous, not big-bang.** The Console evolves as the accumulated,
   always-coherent surface of each runtime feature. "Console v2 / v3" are the
   result of per-feature panels staying consistent, not separate monolithic UI
   projects.
6. **Build-tag-gated capabilities degrade transparently.** When a capability's
   build tag is absent, its Console surface discloses the limitation and apply
   preflight rejects an enabling candidate before persistence—never a silent
   no-op or opaque failure.
7. **Operable by keyboard, not just mouse.** Every control is focusable and
   actionable from the keyboard; modal surfaces trap focus while open and
   restore it on close. See [accessibility.md](../accessibility.md).
8. **The server owns semantics.** Console forms call the same validation,
   preview, lifecycle, mutation, and error contracts used by API and CLI clients.
   TypeScript field lists or browser-only lifecycle logic are not authoritative.
9. **Raw TOML remains the expert escape hatch.** Curated forms may omit advanced
   settings, but must preserve them and offer a safe complete editor path.

## Rationale

- **Friendliness is a differentiator**, and it only survives if defended on every
  runtime feature rather than periodically retrofitted.
- **A UI obligation per runtime feature raises each feature's cost**, which is a
  useful forcing function against feature sprawl.
- **Embedded SPA + build tag** keeps friendliness from fighting the leanness
  pillar: the cost is opt-in and size-bounded.
- **Appropriate surfaces prevent waste:** importers, schemas, CI validators and
  automation remain clearer and safer as API/CLI-native tools.

## Consequences

**Positive**

- The Console stays simple, current, and trustworthy as the runtime grows.
- Runtime Definition of Done remains crisp.
- Machine-oriented tools can be designed for deterministic automation rather
  than wrapped in unnecessary UI.
- Shared server operations reduce UI/API/CLI drift.

**Negative / trade-offs**

- Higher per-runtime-feature cost, accepted deliberately.
- Embedded assets add binary size to the Full build, bounded by budget.
- Some capabilities require multiple surfaces and contract tests.
- Curated Console forms and complete raw TOML require explicit preservation and
  migration tests.

## Alternatives considered

- **Config-only / no Console obligation** — rejected: cedes the friendliness
  pillar and pushes runtime operation back to hand-edited TOML.
- **A separate externally hosted dashboard** — rejected: breaks the single-binary
  and zero-dependency story.
- **Periodic big-bang Console releases** — rejected: produces UI debt and drift.
- **Force every developer/automation tool into the Console** — rejected by ADR
  0014: creates artificial UI scope and duplicate semantics.

## Review triggers

- The embedded UI size budget is exceeded → revisit delivery strategy rather
  than weakening runtime operability.
- A runtime feature cannot be made understandable in the Console → solve the
  design problem or reconsider whether it belongs in core.
- A machine-native capability gains a genuine operator workflow → add a Console
  entry point without moving its authoritative semantics into the browser.
- Console, API and CLI behavior diverge → treat as a contract defect.
