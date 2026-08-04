<!-- Concept-horizon specification. Not an active implementation plan. -->


> **Concept horizon — not committed.**
# JUL Engineering Horizon — Year 3: Fleet, controllers, and distributed operation

> Version 1.1 · Updated 2026-08-03 · **Vision horizon — not committed.**
>
> Current standalone-product work is defined by
> [Core Gateway Completeness](core-gateway-completeness.md),
> [ADR 0013](../adr/0013-project-operating-model-and-completeness.md), and
> the [roadmap](../roadmap/). This document preserves possible scale-category
> designs; it does not activate implementation issues by itself.

## Purpose

Year 3 explores what happens **after** the standalone gateway is technically
complete and real multi-node operation justifies a separate supported category.
The horizon covers fleet coordination, controllers, distributed state, external
identity, and ecosystem distribution.

The permanent constraint is that the OSS single-node gateway remains useful and
operable without any control plane, enterprise service, or hosted dependency.
See [ADR 0012](../adr/0012-oss-open-core-boundary.md).

## Prerequisites

No Year-3 implementation should start until the relevant category has:

- an explicit entry ADR under ADR 0013;
- a bounded supported scope and non-goals;
- stable single-node configuration authority and lifecycle contracts;
- backend transport trust and node identity;
- versioned external API/schema contracts;
- a migration and rollback model;
- capacity to maintain the additional release, compatibility, and security
  surface.

A local laboratory prototype may be approved as a technical experiment, but it
must remain removable and may not redefine baseline core completeness.

## Candidate categories

### 1. Fleet control plane and node agent

Possible scope:

- versioned desired configuration;
- node enrollment and mTLS identity;
- cohorts and staged rollout;
- health-gated promotion and rollback;
- actual-versus-desired drift reporting;
- fleet audit and operator Console.

Non-goals for the first supported version:

- service-mesh xDS compatibility;
- global traffic management;
- mandatory control-plane dependency for local startup;
- multi-region active/active control plane.

The node must continue using Jul.IA's existing validation, lifecycle, planned
restart, exact restoration, and runtime publication semantics. The control plane
orchestrates versions; it does not bypass the data-plane transaction.

### 2. Kubernetes / Gateway API controller

Possible scope:

- a deliberately bounded Gateway API subset;
- status conditions and deterministic translation into Jul.IA configuration;
- namespace and secret ownership rules;
- safe reconciliation and rollback;
- explicit unsupported-resource diagnostics.

A controller is not required merely because Kubernetes discovery exists. It
becomes supported product only after its ownership model and conformance subset
are accepted.

### 3. External identity and fleet RBAC

Local-token RBAC is part of the shipped standalone product. A fleet category may
add:

- OIDC and optionally SAML;
- organization and fleet roles;
- scoped automation credentials;
- break-glass local administration;
- tamper-evident fleet audit.

This must not remove the standalone local recovery path.

### 4. Distributed cache and rate limiting

Distributed state remains outside standalone completeness. A future category may
add shared cache, global rate limits, and coordinated purge through Redis or
another explicit backend.

Prerequisites include:

- corrected and recertified local cache semantics;
- generic limiter interfaces with defined fail-open/fail-closed behavior;
- bounded network timeouts and back-pressure;
- explicit consistency and outage contracts.

The generic single-node resilience primitives—request/connection limits, retry
budgets, backoff, and circuit breaking—belong in the current core specification,
not in this horizon.

### 5. Progressive delivery and fleet traffic management

Possible later scope:

- weighted cohorts;
- canary promotion;
- mirroring;
- blue/green assignment;
- policy-based rollback.

This is orchestration built on generic core routing/resilience primitives. It
must not create a second data-plane routing engine.

### 6. Plugin registry and signed distribution

A future registry may provide signed artifacts, compatibility metadata, trust
policy, and release channels. Local WASM loading remains available without it.

## Evidence and entry gates

A supported Year-3 category requires a separate decision that considers:

- repeated multi-node or controller use cases;
- operational pain not solved by existing standalone automation;
- permanent compatibility and security cost;
- deployment and recovery model;
- whether the category belongs in this repository, another component, or a
  separate product.

External demand is especially relevant here because these categories add
ongoing distributed operational responsibility. A laboratory experiment alone
is not sufficient for supported-product promotion.

## Architecture invariants

- Single-node mode remains first-class and requires no fleet service.
- Nodes fail safely when the controller or shared backend is unavailable.
- Desired and actual state are distinct and observable.
- Rollout uses immutable configuration versions.
- Node and operator identity use explicit trust boundaries.
- Every remote mutation is authorized and audited.
- Distributed components never weaken local validation, lifecycle, or rollback.
- Build/profile and licensing boundaries remain explicit.

## Principal risks

- control-plane unavailability becoming data-plane unavailability;
- split-brain or ambiguous configuration authority;
- unsafe enrollment and certificate lifecycle;
- reconciliation loops overwriting emergency local recovery;
- distributed state adding hot-path latency or hidden consistency assumptions;
- combinatorial platform and upgrade support;
- an enterprise category silently becoming a prerequisite for OSS operation.

## Exit outcomes

After a bounded prototype or design review, the category must be explicitly:

- promoted to a supported programme;
- continued as an experiment;
- frozen;
- extracted into another component or repository;
- removed;
- or deferred.

## Changelog

| Date | Version | Change |
| --- | --- | --- |
| 2026-08-03 | 1.1 | Reclassified the document as a pure horizon; moved generic single-node trust, limits, retries, and circuit breaking into Core Gateway Completeness; preserved fleet, controller, distributed-state, external-identity, and ecosystem categories behind explicit entry decisions. |
| 2026-06-21 | 1.0 | Initial Year-3 horizon. |
