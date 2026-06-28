# ADR 0004 — Console-first / UI invariants (Operable by design)

- **Status:** Accepted
- **Date:** 2026-06-21
- **Deciders:** Jul.IA maintainers
- **Applies to:** the admin Console and every user-facing feature
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
that constrains *how* every other feature ships.

## Decision

**Operable by design / Console-first.** The following invariants hold:

1. **Every user-facing capability ships with a lean, self-explanatory Console
   surface.** A new user understands it on sight, without reading docs.
2. **No feature is "done" until it is operable and observable from the Console.**
   This is part of the Definition of Done and a mandatory **GA** criterion
   (criterion 9 in [ADR 0003](0003-maturity-and-ga.md)). It applies to in-flight
   work too — WAF (Y2-06), mTLS (Y2-07), and the AI Gateway MVP each arrive with
   their own panel.
3. **Power through clarity, not more knobs.** Added capability must not add
   cognitive load; prefer sensible defaults, progressive disclosure, and curated
   forms over exposing every raw setting.
4. **Lean delivery is preserved.** The rich Console remains a small embedded SPA
   served via `go:embed` (single binary, no external assets), stays behind the
   `console` build tag, and is **default in the `Full` edition**; the `Core/OSS`
   build stays lean (minimal admin page only). Embedded UI assets carry a size
   budget so "lean *and* powerful" both hold.
5. **Continuous, not big-bang.** The Console evolves as the accumulated,
   always-coherent surface of each feature's own panel. "Console v2 / v3" are the
   *result* of per-feature panels staying consistent — not separate monolithic UI
   projects.
6. **Build-tag-gated capabilities degrade transparently.** When a capability's
   build tag is absent, its Console surface stays usable for editing but
   **discloses** the limitation up front (a panel banner) and at apply time (a
   diff warning), and the apply preflight rejects a config that would enable it —
   never a silent no-op or an opaque failure. (Plugins, Streams, WAF, and
   tracing follow this rule today; see
   [console.md → Build-tag degradation](../console.md#build-tag-degradation).)
7. **Operable by keyboard, not just by mouse.** Every Console control is a real
   focusable element reachable and actionable from the keyboard, and modal
   surfaces (drawers, confirm dialogs, the command palette, the re-auth prompt)
   trap focus while open and restore it on close (WCAG 2.4.3). "Anyone can operate
   Jul.IA" includes operators who navigate by keyboard or screen reader; see
   [accessibility.md](../accessibility.md).

## Rationale

- **Friendliness is a differentiator**, and it only survives if it is defended on
  every feature rather than periodically retrofitted.
- **A UI obligation per feature raises each feature's cost**, which is a *useful*
  forcing function: it reinforces narrow scope and argues against feature sprawl —
  consistent with [ADR 0003](0003-maturity-and-ga.md)'s gates.
- **Embedded-SPA + build tag** keeps the friendliness pillar from fighting the
  leanness pillar: the cost is opt-in and size-bounded.

## Consequences

**Positive**

- The Console stays simple, current, and trustworthy as the product grows.
- "Console-first" gives a crisp Definition-of-Done and GA criterion.
- Discourages scope sprawl (every feature must also be made operable).

**Negative / trade-offs**

- Higher per-feature cost (each feature needs UI work), which slows raw feature
  throughput — accepted deliberately.
- Embedded assets add binary size to the `Full`/`console` build; bounded by an
  explicit size budget.

## Alternatives considered

- **Config-only / no Console obligation** — rejected: cedes the friendliness
  pillar and pushes operators back to hand-editing TOML.
- **A separate, externally-hosted dashboard** — rejected: breaks the single-binary
  story and the zero-dependency operability promise.
- **Periodic big-bang Console releases** — rejected: produces UI debt and
  feature/UI drift; replaced by continuous per-feature panels.

## Review triggers

- The embedded UI size budget is exceeded → revisit asset strategy (lazy-load,
  trim, or split) rather than weakening the invariant.
- A feature genuinely cannot be made self-explanatory in the Console → treat as a
  design problem to solve, or reconsider whether the feature belongs in core.
