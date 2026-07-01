# Reviews & decision log

> Version 1.2 · Updated 2026-07-01

> **📌 Single source of truth for current repository state:**
> [**Full Repository Audit (2026-07)**](jul_full_repository_audit_2026-07.md).
> It consolidates and, for *present-state* purposes, supersedes the point reviews
> below. The documents below remain the authoritative record of *past decisions*
> (what was adopted / reframed / deferred / rejected); the consolidated audit is
> the authoritative record of *where the code and product stand now*.

This folder is the **decision log** for Jul.IA: external/internal reviews that have
been evaluated, plus the explicit record of *what we adopted, reframed, deferred,
or rejected* and *where each decision landed*. It exists so the product's
evolution stays legible — every durable choice is traceable from a review to an
ADR and to the vision / roadmap / specs change it produced.

Reviewed documents stay here verbatim (with a dated **Reviewed** acknowledgement
header); they are never silently edited. Decisions are recorded below and, when
durable, promoted to an [ADR](../adr/).

## Reviewed inputs

| Date reviewed | Document | Disposition |
| --- | --- | --- |
| 2026-07-01 | [Full Repository Audit (2026-07)](jul_full_repository_audit_2026-07.md) | **Authoritative — current-state single source of truth** |
| 2026-06-21 | [Protocol Adapters — Product Strategy](jul_protocol_adapters_product_strategy.md) | Adopted (with scoping) |
| 2026-06-21 | [Roadmap, Vision & Specs — Critical Product Review](jul_roadmap_vision_specs_critical_review.md) | Adopted / Reframed |
| 2026-06-23 | [Console v2 — Self-explanatory operations & configuration UI spike](jul_console_v2_spike.md) | Adopted |
| 2026-06-23 | [Console v2 — Frontend stack recommendation](jul_console_tech_stack_recommendation.md) | Adopted |

## Decisions promoted to ADRs

- [ADR 0002 — Protocol adaptation strategy](../adr/0002-protocol-adaptation.md) —
  explicit adapters, not universal conversion.
- [ADR 0003 — Maturity model, GA bar, and evidence gates](../adr/0003-maturity-and-ga.md).
- [ADR 0004 — Console-first / UI invariants (Operable by design)](../adr/0004-console-ui-invariants.md).
- [ADR 0006 — Console v2: build-time SPA substrate (single-binary preserved)](../adr/0006-console-v2-stack.md).

## Synthesis

Both reviews converge on one theme: Jul.IA's *ambition* is broad and credible, but
the *claims and committed scope* outran a solo, part-time team. The response is not
to shrink the vision — it is to make the vision honest and demand-gated:

1. **Positioning.** Compete as the *leanest serious edge/protocol gateway*, not the
   "most powerful" gateway overall (which invites losing comparisons with Envoy,
   Kong, Apollo, Cloudflare, Istio).
2. **Protocol strategy.** Double down on REST/JSON → gRPC transcoding + native gRPC
   passthrough as the flagship and first GA target; treat GraphQL as an explicit,
   demand-gated composition layer; reject universal any-to-any conversion.
3. **Honesty.** Replace "Delivered" with a maturity ladder (implemented ≠ GA);
   gate every major new category behind real evidence.
4. **Operability.** Make Console-first / *Operable by design* a standing invariant
   and a GA criterion, delivered as continuous per-feature panels.

## Console v2 synthesis (2026-06-23)

*Jul-authored digest distilling the two Console v2 inputs above (the
[operations & configuration UI spike](jul_console_v2_spike.md) and the
[frontend stack recommendation](jul_console_tech_stack_recommendation.md)) into the
decisions Jul committed to. This is a synthesis, not a verbatim review; the
authoritative decision record is [ADR 0006](../adr/0006-console-v2-stack.md) and the
[Console v2 execution spec](../specs/console-v2.md).*

The two inputs separate cleanly: the **spike** defines the *operating model* (a
self-explanatory operations cockpit — validate → diff → apply → reload → rollback,
progressive disclosure, blast-radius preview, human errors), while the
**stack recommendation** defines the *substrate* (TypeScript + React + Vite +
Tailwind, with TanStack Query/Zod/CodeMirror 6). They converge on one constraint
that governs everything: **React is a build-time implementation detail only — Jul
still ships as a single self-contained Go binary with embedded assets and no Node
runtime, npm/pnpm, CDN, external assets, or separate frontend server.**

What Jul committed to:

1. **Adopt the substrate.** React/TS(strict)/Vite/Tailwind + TanStack Query + Zod
   (UI-shape only; Go stays the source of truth) + CodeMirror 6 (lazy). Custom
   SVG charts first; no heavy UI kit. — [ADR 0006](../adr/0006-console-v2-stack.md).
2. **Commit the prebuilt bundle.** The SPA is built under `internal/admin/ui/`,
   its output committed to `internal/admin/assets/dist/`, and embedded via
   `go:embed` so `go build`/`install`/Docker/release tarballs stay Node-free; CI
   carries a drift guard + a ~250 KB gz initial-route size gate. — amends
   [ADR 0004](../adr/0004-console-ui-invariants.md) #4.
3. **Reframe the cutover.** The migration is taken as a single bounded big-bang
   v1→v2 substrate cutover — an explicit, one-time exception to the
   continuous-per-feature-panels invariant, not a reversal of it. — amends
   [ADR 0004](../adr/0004-console-ui-invariants.md) #5.
4. **Reframe the product promise.** "No Node build" becomes "no Node *runtime*,
   no external web assets, embedded release bundle" — maintainable Console without
   compromising the lean single-binary identity.
5. **Keep the security posture.** Same-origin `/api/*` only; CSP tightened toward
   `script-src 'self'` with a style nonce; admin token in memory/session only;
   dangerous changes gated behind confirmation. — [ADR 0006](../adr/0006-console-v2-stack.md).
6. **Adopt the spike's operating model as the spec backbone.** Goal-first wizard,
   guided forms + raw TOML expert mode, human validation errors, diff +
   blast-radius before apply, post-apply runtime confirmation, event log, and the
   desired-vs-runtime-state distinction. — [console-v2 spec](../specs/console-v2.md).
7. **Honor the input non-goals.** Deferred/rejected for Console v2: cluster/fleet,
   RBAC/SSO, web terminal, AI assistant, MCP, i18n, encrypted export, SSR/Next.js,
   Redux, Monaco, and heavy chart/UI libraries.

## Traceability — what changed and where it landed

Disposition key: **Adopted** · **Reframed** (accepted in spirit, altered in form) ·
**Deferred** (accepted, demand-gated) · **Rejected**.

| Recommendation (from reviews) | Disposition | Where it landed |
| --- | --- | --- |
| Make REST/JSON → gRPC + native passthrough the strategic flagship and first GA target | Adopted | [ADR 0002](../adr/0002-protocol-adaptation.md); [roadmap](../roadmap/) Y2-01/Y2-04 |
| Explicit protocol adapters, **not** universal conversion | Adopted | [ADR 0002](../adr/0002-protocol-adaptation.md); [vision](../vision/) non-goals |
| GraphQL only as explicit schema/resolver composition (no federation/subscriptions) | Adopted | [ADR 0002](../adr/0002-protocol-adaptation.md); [year-2 spec](../specs/year-2.md) Y2-08 |
| Stop building GraphQL until BFF/composition demand is real | Deferred | [roadmap](../roadmap/) Y2-08 (deferred); [ADR 0003](../adr/0003-maturity-and-ga.md) gates |
| Distinguish implemented from GA; adopt a maturity ladder | Adopted | [ADR 0003](../adr/0003-maturity-and-ga.md); maturity column in [roadmap](../roadmap/) |
| Define an explicit, evidence-based GA bar | Adopted | [ADR 0003](../adr/0003-maturity-and-ga.md) (9 criteria) |
| Audit "delivered" claims before GA (e.g. stale `unary` comment vs shipped streaming) | Adopted | [year-2 spec](../specs/year-2.md) Y2-01 DoD (code follow-up flagged) |
| Gate categories (fleet, K8s, distributed state, AI, cloud, mesh, GSLB) on real demand | Deferred | [ADR 0003](../adr/0003-maturity-and-ga.md) gates; [vision](../vision/) evidence-gates table |
| Make the admin Console a first-class, self-explanatory invariant | Adopted | [ADR 0004](../adr/0004-console-ui-invariants.md); [vision](../vision/) commitments |
| Reposition away from "most powerful" toward leanest serious gateway | Adopted | [vision](../vision/) pillar 1 |
| Enter AI as a thin, time-boxed bet rather than a full Year-4 program now | Adopted | [ADR 0003](../adr/0003-maturity-and-ga.md); [roadmap](../roadmap/) AI-MVP |
| Pull secrets references earlier (cross-cutting hygiene) | Adopted | [roadmap](../roadmap/) SEC-1; [year-2 spec](../specs/year-2.md) cross-cutting |
| Restructure the roadmap from years into phases/now-next-later | Reframed | Kept years **+** maturity **+** gates (hybrid) — [ADR 0003](../adr/0003-maturity-and-ga.md); [roadmap](../roadmap/) |
| Deliver Console v2/v3 as large milestone projects | Reframed | Continuous per-feature panels — [ADR 0004](../adr/0004-console-ui-invariants.md); [roadmap](../roadmap/) Y2-09 |
| Adopt React/TS/Vite/Tailwind as the Console build-time substrate | Adopted | [ADR 0006](../adr/0006-console-v2-stack.md); [console-v2 spec](../specs/console-v2.md) |
| Commit the prebuilt SPA bundle; keep go build/install/Docker/release Node-free | Adopted | [ADR 0006](../adr/0006-console-v2-stack.md); embed `internal/admin/assets/dist/` |
| Enforce an explicit ~250 KB gz initial-route size budget in CI | Adopted | [ADR 0006](../adr/0006-console-v2-stack.md) amends [ADR 0004](../adr/0004-console-ui-invariants.md) #4 |
| One-time big-bang v1→v2 Console substrate cutover | Reframed | Bounded exception to continuous panels — [ADR 0006](../adr/0006-console-v2-stack.md) amends [ADR 0004](../adr/0004-console-ui-invariants.md) #5 |
| Offer universal REST/gRPC/GraphQL conversion | Rejected | [vision](../vision/) non-goal; [ADR 0002](../adr/0002-protocol-adaptation.md) |
| Auto-generate GraphQL from proto/OpenAPI (GraphQL "without resolvers") | Rejected | [ADR 0002](../adr/0002-protocol-adaptation.md); [year-2 spec](../specs/year-2.md) Y2-08 |
| Build gRPC → GraphQL conversion into the core | Rejected | [ADR 0002](../adr/0002-protocol-adaptation.md) |
| Name Enterprise/Cloud editions now | Deferred | Two editions only — Core/OSS + Full — [vision](../vision/) business ladder |

## Resolved open questions

The reviews surfaced open questions; these were resolved on 2026-06-21 and now
constrain the roadmap:

1. **Team capacity** — solo, part-time. This is the dominating constraint behind
   every scoping decision below.
2. **Real demand pull today** — REST → gRPC, WASM plugins, L4 stream proxy, WAF,
   mTLS. No current pull for GraphQL, Kubernetes-at-scale, fleet control plane,
   AI, service mesh, CDN, or discovery-at-scale.
3. **AI Gateway** — pursued as a parallel, time-boxed bet: a thin
   OpenAI-compatible MVP behind the `ai` tag with an explicit kill/continue gate;
   not the full Year-4 program.
4. **Roadmap structure** — hybrid: keep the year narrative, add a maturity column
   and evidence gates (do not rewrite into phases).
5. **GA bar** — all GA criteria are mandatory, plus a 9th: a self-explanatory
   Console surface ([ADR 0004](../adr/0004-console-ui-invariants.md)).
6. **Editions** — two only: **Core/OSS** (lean default) and **Full** (all OSS
   build tags incl. Console). Enterprise/Cloud naming deferred until their gates
   trip.

## Changelog

| Date | Ver | What changed | What stayed | Source |
| --- | --- | --- | --- | --- |
| 2026-06-21 | 1.0 | Created the reviews & decision log: relocated the two reviewed documents here with dated acknowledgement headers; recorded the adopted / reframed / deferred / rejected traceability, the synthesis, and the resolved open questions; linked the decisions to ADR 0002/0003/0004 and the vision/roadmap/specs updates. | The two review documents themselves (verbatim, with only an ack header added). | review 2026-06-21; [ADR 0002](../adr/0002-protocol-adaptation.md), [ADR 0003](../adr/0003-maturity-and-ga.md), [ADR 0004](../adr/0004-console-ui-invariants.md) |
| 2026-06-23 | 1.1 | Logged the two Console v2 inputs verbatim ([operations & configuration UI spike](jul_console_v2_spike.md); [frontend stack recommendation](jul_console_tech_stack_recommendation.md)); added the Console v2 synthesis digest and four Console v2 traceability rows; promoted the decisions to [ADR 0006](../adr/0006-console-v2-stack.md) and the [console-v2 spec](../specs/console-v2.md). | The 2026-06-21 reviews, synthesis, and resolved open questions are unchanged. | review 2026-06-23; [ADR 0006](../adr/0006-console-v2-stack.md), [console-v2 spec](../specs/console-v2.md) |
