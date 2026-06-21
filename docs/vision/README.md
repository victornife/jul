# Jul.IA — Vision

> Version 1.1 · Updated 2026-06-21
>
> Status snapshot: Year 1 shipped; Year 2 in progress (Y2-01 … Y2-05 shipped at
> **Beta** maturity — see the [roadmap](../roadmap/) maturity column).
> Maintained alongside the [roadmap](../roadmap/). Update both whenever a feature
> ships or an ADR is added.

## What Jul.IA is

Jul.IA is an NGINX-inspired HTTP edge server written in Go and configured
entirely through TOML. It bundles reverse-proxying, load balancing, static
serving, application gateways, a two-tier cache, TLS, hot reload, and a built-in
admin/observability surface — in a **single static, dependency-free binary**.

## The three pillars

Every decision is weighed against three properties that must hold *together*:

1. **Powerful (without bloat)** — a serious protocol gateway and platform: gRPC,
   L4 stream proxy, WASM plugins, service discovery, and — when demand is proven —
   an AI gateway and beyond. The goal is not to be the *most powerful* gateway
   overall (that invites losing comparisons with Envoy, Kong, Apollo, Cloudflare,
   and Istio) but the **leanest serious edge/protocol gateway**: powerful enough
   for modern infrastructure, simple enough to run as one binary.
2. **Friendliest** — zero-config to HTTPS in under a minute and *Operable by
   design*: every capability is reachable from a lean, self-explanatory web
   Console (see [ADR 0004](../adr/0004-console-ui-invariants.md)), with
   plain-English operations (AI assist) and 1-click app templates.
3. **Lean** — one static binary, `CGO_ENABLED=0`, trivial cross-compilation, and
   a *lean-by-default* build where every heavy feature is opt-in behind a build
   tag. The default binary stays small.

When these tensions conflict, leanness wins by default and power is added behind
a build tag. See [ADR 0001 — language strategy](../adr/0001-language-strategy.md).

## Architectural commitments

- **Single binary, no cgo.** Pure-Go dependencies only in the core (wazero for
  WASM, `modernc.org/sqlite`, pure-Go codecs). Native code lives at the edges —
  as sandboxed WASM or an opt-in sidecar — never via cgo in the main binary.
- **Opt-in build tags.** Heavy features (`brotli`, `zstd`, `acme`, `otel`,
  `grpc`, `http3`, `wasmplugins`, `stream`, `consul`, `kubernetes`, …) compile
  out of the default build and fail loud if configured without their tag.
- **Validate-then-atomic-reload.** Configuration changes are validated and then
  applied atomically; a bad config never takes down a running server. This is
  the seam that the fleet control plane (Year 3) and Cloud (Year 5) build on.
- **Stable seams over churn.** Provider/adapter interfaces (discovery, cache
  store, limiter, cert provider, secret provider) isolate the core from vendor
  API drift and let features compose.
- **Operable by design / Console-first.** Every user-facing capability ships with
  a lean, self-explanatory Console surface; no feature is "done" until it can be
  operated and observed from the Console without reading docs. Power is exposed
  through clarity, not more knobs. See
  [ADR 0004](../adr/0004-console-ui-invariants.md).
- **Explicit adapters, not universal conversion.** Jul.IA adapts protocols
  explicitly where the mapping is clear (REST/JSON → gRPC, native gRPC
  passthrough); it does not promise magical conversion between incompatible
  models. GraphQL, if built, is an explicit schema/resolver composition layer.
  See [ADR 0002](../adr/0002-protocol-adaptation.md).
- **Evidence before expansion.** Major new categories are demand-gated, and
  maturity is labeled honestly (Beta until the GA bar is met). See
  [ADR 0003](../adr/0003-maturity-and-ga.md) and the evidence gates below.

## The business ladder

Jul.IA is designed so it *can* grow along an **OSS → open-core → Cloud** path
without re-architecting. The near-term commitment is the OSS core; everything
beyond it is a **Vision horizon — demand-gated**, not a committed plan.

Editions today are deliberately just two: **Core/OSS** (lean, default build) and
**Full** (all OSS build tags, including the Console). Enterprise and Cloud naming
is deferred until their evidence gates trip.

- **OSS (now):** a fully-functional single-node edge server and protocol gateway.
- **Open-core (horizon, demand-gated):** fleet control plane, RBAC/SSO,
  distributed state, Kubernetes-at-scale, audit — gated on multi-node operators.
- **AI-native + Edge (horizon, demand-gated):** AI gateway, semantic cache,
  guardrails, AI-assisted Console — entered via a time-boxed bet (the `ai` MVP).
- **Cloud (horizon, demand-gated):** multi-tenant hosted Console with
  bring-your-own nodes; global load balancing and service mesh only if customers
  pull them in.

## Evidence gates

The long-term vision is broad; the *operating* roadmap stays narrow. A category
is not committed until its evidence exists (see
[ADR 0003](../adr/0003-maturity-and-ga.md)).

| Category | Evidence required before committing |
| --- | --- |
| GraphQL | Users need BFF/composition over existing REST/gRPC |
| Fleet | Multiple users operate N Jul.IA nodes |
| Kubernetes/Gateway API | Real ingress-controller demand |
| Distributed cache/rate-limit | Real fleet users hit local-limit problems |
| AI Gateway | Jul.IA is used as an API/protocol gateway and users ask for AI routing/cost |
| Cloud | Self-hosted control-plane demand exists |
| Service mesh | Customers explicitly ask for east-west identity/policy |
| GSLB | Customers run multi-region Jul.IA fleets |

A **Time-boxed bet** is the sanctioned exception: a thin, time-boxed MVP with an
explicit kill/continue decision may enter a category ahead of its gate (current
example: the AI Gateway MVP behind the `ai` tag).

## Non-goals (for now)

- **Universal REST/gRPC/GraphQL conversion.** Jul.IA offers explicit adapters for
  real migration paths, not a magic any-protocol converter
  (see [ADR 0002](../adr/0002-protocol-adaptation.md)).
- **GraphQL without resolvers.** GraphQL is a composition layer with explicit
  schema/resolvers, never auto-generated from proto/OpenAPI.
- Replacing dedicated secrets managers, SIEMs, or identity providers — Jul.IA
  *integrates* with them rather than reimplementing them.
- A broad rewrite of hot paths in another language inside the binary
  (see [ADR 0001](../adr/0001-language-strategy.md)).
- Running Jul.IA's own global anycast network before Cloud demand is proven.

## Related documents

- [Roadmap](../roadmap/) — what's shipped and what's planned (Years 1–5).
- [Engineering specs](../specs/) — detailed per-feature plans.
- [Architecture Decision Records](../adr/) — durable technical decisions.
- [Reviews & decision log](../reviews/) — how the product direction evolves.

## Changelog

| Date | Ver | What changed | What stayed | Source |
| --- | --- | --- | --- | --- |
| 2026-06-21 | 1.1 | Repositioned pillar 1 from "most powerful" to "leanest serious gateway"; added commitments *Operable by design / Console-first*, *Explicit adapters not universal conversion*, *Evidence before expansion*; added an evidence-gates table; softened the OSS→open-core→Cloud ladder to a demand-gated horizon with a two-edition (Core/OSS · Full) model; extended non-goals; fixed ADR links after the folder move. | The three-pillar model, the *leanness-wins* tie-breaker, and the single-binary / no-cgo / validate-then-atomic-reload / stable-seams commitments. | [review 2026-06-21](../reviews/); [ADR 0002](../adr/0002-protocol-adaptation.md), [ADR 0003](../adr/0003-maturity-and-ga.md), [ADR 0004](../adr/0004-console-ui-invariants.md) |
| 2026-06-21 | 1.0 | Initial vision. | — | — |
