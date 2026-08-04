# ADR 0014 — Appropriate operability surfaces for Console, API, CLI, and raw configuration

- **Status:** Accepted
- **Date:** 2026-08-03
- **Deciders:** Jul.IA maintainer
- **Applies to:** user-facing runtime capabilities, automation, migration, diagnostics, controllers, and Definition of Done
- **Amends:** [ADR 0004](0004-console-ui-invariants.md)
- **Related:** [operating model](../operating-model.md), [Core Gateway Completeness](../specs/core-gateway-completeness.md), #114, #118

> **Document roles:** ADR 0013 decides how work enters the portfolio; ADR 0014 decides the required operator/developer surfaces; `docs/operating-model.md` defines execution discipline; `docs/specs/core-gateway-completeness.md` defines the bounded product; the roadmap and #62 own current order and status.

## Context

ADR 0004 correctly established the embedded Console as a product invariant for
runtime operation. Its literal wording—every user-facing capability must have a
Console surface—was broader than the actual needs of:

- CI and configuration validation;
- migration and importer tooling;
- remote automation;
- generated JSON Schema and OpenAPI;
- future controllers;
- support-bundle and diagnostic workflows.

Forcing a graphical form onto every machine-oriented tool increases scope,
creates duplicate semantics, and weakens rather than strengthens the product.
At the same time, abandoning Console-first for runtime operation would erode one
of Jul.IA's defining strengths.

## Decision

Use the **appropriate first-class surface** for each capability while maintaining
one server-side semantic implementation.

| Capability class | Required first-class surface | Additional surfaces |
| --- | --- | --- |
| Runtime/operator capability | Console in the Full build + admin API | Raw TOML expert path; CLI where useful |
| Configuration automation | Versioned API + CLI | Console plan/diff/apply using the same server operations |
| Migration/developer tooling | CLI + machine-readable output | Optional Console entry point if it adds real operator value |
| Controller integration | API + generated schema/metadata | Console status and ownership visibility |
| Diagnostics/support | CLI/API export | Console trigger and download where practical |
| Complete expert configuration | Raw TOML | Typed forms for curated common paths |
| Runtime status/health | API + Console | CLI presentation over the API |

### Invariants

1. **One implementation of behavior.** Console, CLI, and controllers call the
   same server-side operations. The browser must not implement lifecycle,
   validation, diff, or mutation semantics independently.
2. **Runtime capabilities remain Console-operable.** A feature affecting live
   routing, security, transport, resilience, cache, or observability is not
   complete until its state, configuration, warnings, permissions, and relevant
   actions are understandable in the Console Full build.
3. **Machine-native tools may remain CLI/API-first.** An importer, schema
   generator, CI validator, or remote automation command is not forced into a
   graphical editor merely to satisfy Definition of Done.
4. **Raw TOML remains the complete escape hatch.** Curated forms need not expose
   every advanced option, but they must preserve unknown/advanced fields and
   provide a safe path to expert editing.
5. **Build-tag degradation is explicit.** Absent capabilities remain visible as
   unavailable and are rejected before persistence; no surface silently accepts
   an unsupported configuration.
6. **Authority is visible.** In file-owned mode, Console and remote mutation
   surfaces become read-only and explain why. In managed mode, drift and pending
   restart remain visible across API, CLI, and Console.
7. **Accessibility and keyboard requirements remain unchanged** for every
   Console surface.

## Consequences

### Positive

- Preserves the embedded Console as the operational cockpit.
- Avoids artificial UI work for CI and developer-native workflows.
- Reduces duplicated validation and lifecycle logic.
- Creates a coherent path for generated schema, OpenAPI, remote CLI, and future
  controllers.
- Makes the Definition of Done proportionate to the capability's real user.

### Negative / accepted trade-offs

- Some capabilities will have several surfaces and therefore require contract
  tests to prove consistency.
- The Console may deliberately expose a curated subset while raw TOML remains
  more complete; this distinction must be documented clearly.
- Contributors must classify the capability before deciding its mandatory UI
  work.

## Alternatives considered

### Keep the literal every-feature Console obligation

**Pros:** very simple rule; maximum visual coverage.

**Cons:** forces migration, CI, schema, controller, and automation tools into an
unnatural UI; increases scope and duplicates semantics.

**Rejected because:** it optimises for surface count rather than operability.

### Make Console optional for all features

**Pros:** lowest implementation cost.

**Cons:** allows runtime operator features to become TOML/API-only and breaks the
friendliness pillar.

**Rejected because:** runtime operation is a core product differentiator.

### Implement independent Console and CLI logic

**Pros:** each surface can move quickly in isolation.

**Cons:** validation, diff, lifecycle, and error contracts drift; security and
compatibility become unreviewable.

**Rejected because:** the server must own semantics.

## Implementation consequences

- ADR 0004 remains authoritative for Console quality, accessibility,
  progressive disclosure, size budget, and runtime-feature completeness, as
  amended by this ADR's capability classification.
- Phase 5 typed operations remain server-owned and shared by preview/apply and
  Console clients.
- Generated JSON Schema, lifecycle/capability metadata, and external OpenAPI
  become automation contracts rather than browser implementation details.
- Remote CLI commands are thin clients over the supported admin API.
- Issue templates must state which surface is primary and which additional
  surfaces are required.

## Review triggers

Revisit this decision when:

- a runtime capability cannot be represented coherently in the Console;
- the Console size budget requires a different delivery architecture;
- a controller becomes a supported product and changes authority semantics;
- CLI/API and Console contracts begin to diverge despite shared operations.
