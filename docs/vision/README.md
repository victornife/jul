# Jul.IA — Vision

> Status snapshot: Year 1 complete; Year 2 in progress (Y2-01 … Y2-05 shipped).
> Maintained alongside the [roadmap](../roadmap/). Update both whenever a feature
> ships or an ADR is added.

## What Jul.IA is

Jul.IA is an NGINX-inspired HTTP edge server written in Go and configured
entirely through TOML. It bundles reverse-proxying, load balancing, static
serving, application gateways, a two-tier cache, TLS, hot reload, and a built-in
admin/observability surface — in a **single static, dependency-free binary**.

## The three pillars

Every decision is weighed against three properties that must hold *together*:

1. **Most powerful** — a protocol gateway and platform: gRPC, L4 stream proxy,
   WASM plugins, service discovery, and (later) an AI gateway, service mesh, and
   global load balancing.
2. **Friendliest** — zero-config to HTTPS in under a minute, a real web Console,
   plain-English operations (AI assist), and 1-click app templates.
3. **Lean** — one static binary, `CGO_ENABLED=0`, trivial cross-compilation, and
   a *lean-by-default* build where every heavy feature is opt-in behind a build
   tag. The default binary stays small.

When these tensions conflict, leanness wins by default and power is added behind
a build tag. See [ADR 0001 — language strategy](adr/0001-language-strategy.md).

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

## The business ladder

Jul.IA is designed to grow along an **OSS → open-core → Cloud** path without
re-architecting:

- **OSS (Years 1–2):** a fully-functional single-node edge server and platform.
- **Open-core (Year 3):** fleet control plane, RBAC/SSO, distributed state,
  Kubernetes-at-scale, audit — gated behind an `enterprise` license seam while
  OSS stays complete for single-node use.
- **AI-native + Edge (Year 4):** AI gateway, semantic cache, guardrails,
  AI-assisted Console, CDN-grade caching.
- **Cloud (Year 5):** multi-tenant hosted Console with bring-your-own nodes,
  global load balancing, service mesh, and usage-based billing (demand-gated).

## Non-goals (for now)

- Replacing dedicated secrets managers, SIEMs, or identity providers — Jul.IA
  *integrates* with them rather than reimplementing them.
- A broad rewrite of hot paths in another language inside the binary
  (see [ADR 0001](adr/0001-language-strategy.md)).
- Running Jul.IA's own global anycast network before Cloud demand is proven.

## Related documents

- [Roadmap](../roadmap/) — what's shipped and what's planned (Years 1–5).
- [Architecture Decision Records](adr/) — durable technical decisions.
