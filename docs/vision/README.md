# Jul.IA — Vision

> Version 1.5 · Updated 2026-08-04
>
> Shipped maturity is tracked in the canonical [status matrix](../status.md).
> Current execution is tracked in the [roadmap](../roadmap/) and #62. The
> permanent product boundary is defined by [ADR 0013](../adr/0013-project-operating-model-and-completeness.md)
> and [Core Gateway Completeness](../specs/core-gateway-completeness.md).

## What Jul.IA is

Jul.IA is a self-contained edge and protocol gateway written in Go and
configured through TOML. It combines reverse proxying, load balancing, static
serving, HTTP and gRPC adaptation, L4 proxying, TLS, policy, observability,
configuration lifecycle, an embedded Console, and sandboxed extension seams in
a portable single-node product.

The architectural thesis is:

> Explore how much modern gateway capability can be delivered coherently in one
> production-quality node without requiring a distributed control plane,
> mandatory hosted service, or heavyweight operating model.

## Project operating context

Jul.IA is currently maintained as a solo, AI-assisted engineering product. Its
near-term objective is to become unusually complete, coherent, powerful, and
well-tested inside the standalone gateway boundary. It is not currently
optimised primarily for customer acquisition, marketing growth, or revenue.

This context does **not** lower the product bar. Jul.IA continues to require:

- truthful feature and maturity claims;
- fail-closed security and configuration behavior;
- stable documented contracts for GA capabilities;
- realistic protocol, concurrency, failure, platform, and build-profile tests;
- documentation in the same change as behavior;
- bounded permanent complexity suitable for one maintainer;
- a useful standalone product even if every future horizon is never built.

External users, issues, and production evidence remain valuable inputs. They are
not mandatory before fixing a defect, closing a material gateway gap, or running
a bounded technical experiment.

## Who Jul.IA is for

### Primary product users

Small-to-medium platform, infrastructure, and application teams that need a
modern proxy, TLS, policy, gRPC, observability, and extension surface without
operating a distributed gateway platform.

### Primary jobs

- Run a serious standalone HTTP, gRPC, and L4 gateway.
- Modernize or assess an NGINX estate.
- Expose internal services with explicit routing and trust boundaries.
- Apply configuration safely with preview, lifecycle classification, rollback,
  and planned restart.
- Operate routing, security, and observability from one embedded cockpit.
- Extend request behavior through sandboxed WASM.

### Anti-personas and non-goals

Jul.IA is not currently trying to be:

- a hyperscale CDN;
- a complete service mesh;
- a managed global data plane;
- a mandatory Kubernetes controller;
- a fleet or cloud control plane required for standalone use;
- a clone of every feature in NGINX, Envoy, Caddy, Traefik, or Kong;
- an AI platform hidden inside the core gateway.

## Product promise

> Jul.IA remains fully useful, operable, and supportable as a standalone
> single-node gateway even if fleet, AI, GraphQL, Kubernetes, or hosted
> capabilities are never promoted from experiments or horizons.

## The three product pillars

Every decision must preserve all three properties together.

### 1. Powerful without feature-count bloat

Power comes from coherent, reusable primitives: protocol serving, routing,
transport trust, resilience, policy, lifecycle, observability, and extension
seams. Power is not measured by competitor parity or the number of configuration
fields.

Generic gateway mechanisms should be implemented once and reused. An AI or
future category must not create a second transport, retry, security, or
observability architecture inside the product.

### 2. Friendly without hiding reality

Runtime operation should be understandable from the embedded Console. Migration,
automation, diagnostics, and controller integration use the appropriate CLI or
API-first surface. All surfaces reuse one server-side semantic implementation;
raw TOML remains the complete expert escape hatch.

See [ADR 0014](../adr/0014-operability-surfaces.md).

### 3. Lean without becoming simplistic

- One deployable node remains the baseline.
- The lean profile excludes optional heavy capabilities.
- The Full profile remains a tested supported distribution, not an excuse for
  unbounded tag combinations.
- No phone-home dependency is required.
- Structural settings may remain restart-bound when a planned restart is safer
  than permanent runtime-generation complexity.

## What “complete” means

Core completeness is bounded to the standalone gateway. The detailed contract is
[docs/specs/core-gateway-completeness.md](../specs/core-gateway-completeness.md).
At a high level, Jul.IA needs coherent answers for:

- HTTP/1.1, HTTP/2, HTTP/3, TLS/mTLS, gRPC, and L4 ingress;
- deterministic routing and response policy;
- trusted client identity and backend peer identity;
- health, overload, retries, backoff, and circuit behavior;
- authentication, authorization, rate limiting, WAF, secrets, and egress;
- validation, lifecycle, preview, atomic apply, rollback, and planned restart;
- generated machine contracts and safe automation;
- logs, metrics, traces, diagnostics, recovery, and deployment;
- migration assessment and explicit semantic differences;
- stable extension and release-profile boundaries.

Fleet, cloud, distributed state, service mesh, GraphQL composition, AI Gateway,
and universal hot reload are not required for this definition.

## Portfolio model

Work enters the active programme through five lanes:

1. **Correctness and security** — current contract defects and release blockers.
2. **Core Gateway Completeness** — material gaps inside the bounded product.
3. **Operational enhancement** — value-ranked operability and lifecycle work.
4. **Technical experiment** — bounded exploration with explicit exit decision.
5. **Vision horizon** — retained future categories without implementation
   commitment.

The authoritative rules are in [ADR 0013](../adr/0013-project-operating-model-and-completeness.md)
and [the operating model](../operating-model.md).

## Technical experiments

A major experiment requires:

- a precise technical hypothesis;
- fixed first-tranche scope and explicit exclusions;
- architecture prerequisites;
- time, dependency, binary-size, and maintenance budgets;
- evidence to collect;
- success and stop criteria;
- one final outcome: promote, continue experimentally, freeze, extract, remove,
  or defer.

Only one major category experiment should be active at a time. Correctness and
core integrity may interrupt it.

### AI Gateway

AI remains a valid future experiment, not the automatic next phase. Its initial
question is whether Jul.IA can reuse its generic routing, trust, resilience,
streaming, policy, and observability architecture for a small multi-provider
front door without creating a parallel gateway inside the product.

The experiment remains blocked until backend-trust and generic-resilience
architecture decisions are complete. See [Year 4](../specs/year-4.md) and the
experiment epic.

## Architectural commitments

1. **Single-node first.** Distributed control is additive, never required for
   local usefulness.
2. **Server-owned semantics.** Validation, lifecycle, preview, apply, and error
   behavior live on the server, not independently in the Console.
3. **Fail before Publish.** Fallible candidate construction and validation occur
   before the point of no return.
4. **Generation-safe retirement.** Old resources outlive the work that may still
   use them and retire exactly once.
5. **Explicit trust boundaries.** Client assertion trust, outbound destination
   policy, and backend peer authentication remain distinct.
6. **Build-tag honesty.** An absent capability is rejected clearly before
   persistence; it never becomes a silent no-op.
7. **One authoritative source per concept.** ADRs decide, specs design, status
   reports shipped maturity, the roadmap sequences, and issues execute.
8. **Documentation is product behavior.** Claims, examples, migration, and
   operational guidance are part of Definition of Done.

## Possible long-term evolution

The permanent OSS/open-core boundary is defined in
[ADR 0012](../adr/0012-oss-open-core-boundary.md). Fleet, external identity,
hosted control, distributed state, and cloud may become separate supported
categories if their operating and maintenance model is justified. That possible
commercial evolution does not drive current feature selection by itself and
must not weaken the standalone OSS node.

## Success criteria

Jul.IA succeeds in its current phase when:

- confirmed P0/P1 defects are closed with regression evidence;
- the standalone completeness matrix has no unexplained critical gap;
- supported profiles pass their protocol, race, platform, E2E, and security
  gates;
- lifecycle and public contracts are generated or exhaustively checked;
- every GA claim is traceable to code, tests, and documentation;
- experiments remain bounded and removable;
- one maintainer can understand and safely evolve the architecture.

## Changelog

| Date | Version | Change |
| --- | --- | --- |
| 2026-08-03 | 1.5 | Added the solo/AI-assisted operating context, bounded definition of completeness, portfolio lanes, appropriate operability surfaces, experiment governance, and explicit separation between the standalone core and horizons. |
| 2026-07-20 | 1.4 | Previous product/user/roadmap clarification. |
