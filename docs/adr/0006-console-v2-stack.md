# ADR 0006 — Console v2: build-time SPA substrate (React/Vite/Tailwind), single-binary preserved

- **Status:** Proposed
- **Date:** 2026-06-23
- **Deciders:** Jul.IA maintainers
- **Applies to:** [ADR 0004](0004-console-ui-invariants.md) invariants #4 (no external assets / size budget) and #5 (continuous, not big-bang); the Console surface GA criterion in [ADR 0003](0003-maturity-and-ga.md) (criterion 9)
- **Source:** the Console v2 reviews — [operations & configuration UI spike](../reviews/jul_console_v2_spike.md) and [frontend stack recommendation](../reviews/jul_console_tech_stack_recommendation.md)

## Context

[docs/console.md](../console.md) promises that the Console "ships **inside the single
binary** (no external assets, no Node build) and is gated by the `console` build
tag." Today that promise is met by a single hand-written
`internal/admin/assets/console.html`, embedded via `go:embed` behind the `console`
tag and served same-origin from the admin listener. The admin API is already
decoupled (a static shell talking to same-origin `/api/*`, with the token sent in
the `Authorization` header — not a cookie — so CSRF is largely N/A, and every
composition hook degrades gracefully when nil).

The roadmap entry **Y2-09 Console v2** ([roadmap](../roadmap/) ·
[year-2 spec](../specs/year-2.md)) grows the Console into an operations cockpit:
live log tail, a WASM plugin manager, a gRPC route designer, and richer dashboards.
A single hand-authored HTML/JS file does not scale to that surface: no type-safety,
no componentisation, no test harness, and no way to keep "every feature has a
self-explanatory Console panel" (ADR 0003 criterion 9) honest as the matrix grows.

[ADR 0004](0004-console-ui-invariants.md) constrains how the Console may evolve.
Invariant #4 forbids external runtime assets and implies a no-build, hand-authored
surface; invariant #5 mandates that the Console grow as **continuous per-feature
panels**, "not a big-bang rewrite." [year-2 spec](../specs/year-2.md) Y2-09 still
names a **Preact/Svelte** SPA (from Y1-07). Adopting a different substrate, and
performing a one-time migration, therefore requires amending ADR 0004 and
rejustifying the technology choice here.

## Decision

1. **Adopt React + TypeScript (strict) + Vite + Tailwind** as the *build-time*
   Console substrate, shipped as a **client-rendered SPA** embedded via `go:embed`
   and served same-origin from the admin listener. Supporting libraries: TanStack
   Query (server state), Zod (validates UI-shape at the API boundary only — Go
   remains the source of truth), and CodeMirror 6 (lazily loaded) for config
   editing. Charts are custom token-styled SVG (no charting library).
2. **The prebuilt bundle is committed** to `internal/admin/assets/dist/` and
   embedded. `go build`, `go install`, the Docker image, and release binaries
   remain **100% Node-free** — only Console maintainers run pnpm. CI rebuilds the
   bundle and runs a **drift guard** (the committed `dist/` must equal a fresh
   build) plus a **size gate**.
3. **Amend ADR 0004 invariant #4.** Retain "no external web assets at runtime"
   (every JS/CSS/font is embedded; no CDN, no external fonts, no network fetch for
   the app shell), and make the size budget explicit: **~250 KB gzip for the
   initial route**, enforced in CI. Heavy or rarely used surfaces (CodeMirror, the
   diff view) load lazily and do not count against the initial-route budget.
4. **Amend ADR 0004 invariant #5.** Permit a **single, one-time substrate cutover**
   (hand-written v1 → SPA v2). After the cutover, per-feature panels resume
   **continuous** evolution — the invariant's intent (no perpetual, separate
   monolithic UI projects) stands. This is a bounded exception, not a new pattern.
5. **Revise the product promise** in [docs/console.md](../console.md): "no Node
   **runtime**, no external web assets, embedded release bundle" replaces "no Node
   build." React/Vite/Tailwind are a build-time implementation detail only.
6. **Security (closes GA criterion ⑦).** Keep the token in the `Authorization`
   header (no cookies → CSRF N/A). Tighten the CSP from `script-src 'self'
   'unsafe-inline'` to **`script-src 'self'`** (drop `unsafe-inline`), and serve
   `style-src 'self'` with a per-response **nonce** for the inline styles injected
   by CodeMirror/React. Documented fallback if the nonce path proves impractical:
   `style-src 'self' 'unsafe-inline'`.
7. **Migration shape.** A **big-bang cutover in a single release**, developed behind
   the `console` build tag and a dev-only side route so trunk stays releasable on
   v1 until the one cutover PR. There is **no** post-cutover v1 fallback.

This amends **only** invariants #4 and #5 of [ADR 0004](0004-console-ui-invariants.md)
and refines the Console surface (ADR 0003 criterion 9). The *Operable by design*
invariant and every other GA criterion stand as written.

## Rationale

- **The matrix needs an engineering substrate.** Type-safety, componentisation,
  and a test harness are prerequisites for delivering criterion-9
  "self-explanatory surface per feature" sustainably; a hand-written file cannot.
- **Single-binary identity is preserved.** Committing the prebuilt bundle keeps the
  build/release path Node-free — the substrate is purely a build-time concern.
- **Honesty via enforcement.** An explicit size budget plus a drift guard converts
  "no external assets / stays lean" from an aspiration into an auditable CI gate.
- **Bounded risk.** A single, gated cutover is lower-risk than interleaving two UI
  stacks indefinitely; the tag + dev-route gating keeps trunk shippable throughout.

## Consequences

**Positive**

- A scalable, testable, type-safe Console; honest, enforced size and no-Node
  guarantees; closes the two open Console GA gaps — ① (endpoint/panel matrix) and
  ⑦ (CSP/CSRF/auth) — enabling the Beta → **GA — soak pending** bump.

**Negative / trade-offs**

- Adds a maintainer-only Node/pnpm toolchain and a CI frontend job, plus a
  committed `dist/` that must be kept fresh (mitigated by the drift guard).
- One larger cutover PR instead of many small ones (mitigated by tag + dev-route
  gating, so the change lands de-risked and trunk stays releasable until then).

## Alternatives considered

- **Keep hand-written HTML/JS, grow incrementally** — rejected: no type-safety or
  tests; does not scale to the Y2-09 matrix and erodes criterion 9 over time.
- **Preact / Svelte (as named in Y1-07)** — considered; React chosen for ecosystem
  and familiarity. `preact/compat` is retained as the size-budget escape hatch
  (consistent with the invariant-#4 review trigger).
- **Per-feature rewrite *inside* the current `console.html` (strict invariant-#5
  continuity)** — rejected: pays the migration cost repeatedly with no shared
  substrate and reaches parity more slowly.
- **SSR / Next.js** — rejected: reintroduces a Node runtime at serve time, breaking
  the single-binary identity.

## Review triggers

- The initial-route bundle exceeds **250 KB gzip** → revisit (preact/compat, more
  code-splitting, or trim scope) before merge.
- Any Node/JS footprint leaks into `go build` / `go install` / Docker / release →
  treat as a release blocker.
- A second "big-bang" UI rewrite is ever proposed → this exception does **not**
  generalise; reaffirm invariant #5 and require a fresh superseding ADR.
