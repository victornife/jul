# Jul.IA — Roadmap

> Version 2.0 · Updated 2026-08-04
>
> This v2.0 roadmap replaces the previous fixed Phase 5 → AI → horizon sequence
> with a portfolio model. Delivered feature maturity remains authoritative in the
> [status matrix](../status.md); #62 owns issue-level execution.

This roadmap translates the [vision](../vision/),
[ADR 0013](../adr/0013-project-operating-model-and-completeness.md),
[ADR 0014](../adr/0014-operability-surfaces.md), and
[Core Gateway Completeness](../specs/core-gateway-completeness.md) into the current
execution portfolio. #62 is the detailed issue-level programme tracker.

The roadmap is not a promise to implement every open issue. An issue may be
ready, draft, gated, selected, deferred, deliberately restart-bound, or complete.

## Portfolio lanes

| Lane | Objective | Entry rule | Current emphasis |
| --- | --- | --- | --- |
| **Correctness and security** | Restore current documented, protocol, security, lifecycle, and compatibility contracts | Evidence and severity | Known-value validation, access-log semantics, cache correctness, metrics and WAF logging truth |
| **Core Gateway Completeness** | Close material gaps inside the standalone gateway boundary | Architecture and product integrity | Trust boundaries, backend TLS/mTLS, resilience, routing policy, automation contracts |
| **Operational enhancement** | Improve long-running operation without redefining the core | Value × leverage ÷ permanent complexity | Selected hot reload, diagnostics, recovery, migration assessment |
| **Technical experiment** | Explore a new category through a bounded, removable tranche | Hypothesis, prerequisites, budget, evidence, exit decision | AI Gateway candidate after generic trust/resilience decisions |
| **Vision horizon** | Preserve possible large future categories | Separate activation decision | Fleet, Kubernetes controller, distributed state, Cloud, mesh, GSLB, GraphQL composition |

## Current execution sequence

Implementation remains serial around shared architecture even though the
portfolio has parallel lanes.

| Stage | Focus | Exit criteria | Status |
| --- | --- | --- | --- |
| **0 — Programme and truth** | Combined audit, current product truth, operating model | One audit, one tracker, canonical docs synchronized | ✅ #165/#166 merged |
| **1 — Immediate correctness** | Access-log semantics and cache correctness | No known P0; selected P1 behavior documented and tested | 🚧 #123/#126/#127 complete; #124 and cache remain |
| **2 — Cache correctness** | Generation-owned revalidation, immutable entries, HTTP semantics, upgrade transparency, recertification | Race-clean, protocol-safe, truthful conformance matrix | ⬜ planned |
| **3 — Lifecycle authority** | Closed-world field inventory, Go registry authority, generated/checkable mirrors | Every field classified exactly once; no unknown path defaults to hot | ⬜ planned |
| **4 — Structured configuration Phase 5** | Batch preview, entity CRUD, global operations, Console migration, E2E | Preview/apply share one executor and authoritative lifecycle data | ⬜ planned |
| **5 — Core architecture decisions** | Trust, resilience, routing, configuration authority/automation | ADRs merge and downstream drafts become implementation-ready | ⬜ planned |
| **6 — Core implementation** | Canonical client identity, backend trust, generic resilience, routing policy, schema/API/CLI | Standalone completeness gaps closed with protocol/operational evidence | ⬜ planned |
| **7 — Selected runtime dynamics** | Value-ranked certificate, credential, logging, sink, cache-policy, and Alt-Svc transitions | Selected settings are safely dynamic; structural settings retain planned restart | ⬜ planned |
| **8 — Migration and diagnostics** | NGINX assessment, provenance, compatibility corpus, support bundle, `jul doctor` | Operator-safe evidence and recovery workflows | ⬜ planned |
| **9 — One bounded experiment** | AI Gateway or another explicitly approved category | Promote, continue experimental, freeze, extract, remove, or defer | 🔒 gated |
| **10 — Integrated closure** | Exact-SHA audit, protocol matrix, failure injection, E2E, soak, compatibility and release evidence | No unsupported claims or unresolved selected work | ⬜ planned |

## Immediate critical path

```text
#165 — canonical current product truth
    ↓
#166 — operating model, roadmap v2.0 and completeness boundary
    ↓
v1.32.1-rc.1 — unpublished draft release-candidate checkpoint
    ↓
#124 — explicit access-log enablement
    ↓
#131 → #133 / #132 → #134 — cache correctness and recertification
    ↓
#89 — closed-world lifecycle authority
    ↓
#77 → #82 — Phase 5 structured configuration
    ↓
#115 → #118 — core architecture decisions
    ↓
selected core implementations
```

#124 is the remaining shared configuration-schema correction before lifecycle
generation and Phase 5. Shared edits to the configuration schema, lifecycle,
composition root, reload transaction, or Console patch contracts remain serial.

## Correctness and security backlog

### Completed in the current correction tranche

- Strict rejection of unknown TOML fields with the documented `server_name` compatibility alias.
- Complete server-level mTLS parity on HTTP/3.
- Exclusive ACME HTTP-01 versus TLS-ALPN-01 challenge selection.
- `Cache-Control: no-transform` enforcement in dynamic compression.
- Deterministic HTTP/3 UDP preflight testing on Windows.
- Frontend dependency and CodeQL reflected-output hardening.
- Exact `v1.32.0` Prometheus contract reconstruction, additive-current inventory, and CI drift protection (#126).
- Fail-closed enum, worker, duration, size, status, and scalar validation with a machine-readable value contract (#123).
- Path-only, bounded WAF matched-request logging with query and macro-expanded request data omitted (#127).

### Remaining immediate correctness

- Add explicit access-log enablement and retire ignored destination fields (#124).
- Restore cache concurrency, lifecycle, protocol and HTTP conformance (#107, #131–#134).
- Establish closed-world generated lifecycle authority (#89).

### Required quality foundation

- Preserve a fully green required CI baseline.
- Add semantic drift guards for schema, defaults, lifecycle, metrics, and claims.
- Add focused security-package coverage and negative-test gates.
- Close the current audit with exact-SHA evidence.

## Release-candidate checkpoint

After #165 and #166 merge and exact-head CI is green, the next automatic patch
release candidate is **`v1.32.1-rc.1`**. Pushing that tag runs the existing
release gate, soak, lean/full cross-platform build, checksums, SBOM and
attestation workflow, and creates an unpublished draft GitHub Release for human
review. Stable publication remains a later explicit decision while selected
correctness work remains open.

## Core Gateway Completeness backlog

### Inbound identity

- Per-server trusted proxy CIDRs.
- Standards-aware `Forwarded` and X-Forwarded-For processing.
- Right-to-left trusted-hop evaluation.
- One canonical effective client identity used by auth, rate limiting, WAF,
  access logs, diagnostics, and upstream forwarding.

### Backend transport trust

- One normalized `BackendTLSConfig` supporting private roots, client
  certificates, SNI, minimum version, and peer identity constraints.
- Equivalent enforcement across HTTP proxy, native gRPC, transcoding, active
  health checks, and discovery-backed targets.
- Named reusable TLS profiles remain a follow-up only if representative
  configurations demonstrate sufficient repetition.

### Generic resilience

Implement in this order:

1. active-request, pending-request, connection, and concurrency limits;
2. retry budget, attempt deadline, backoff, jitter, and replayability rules;
3. a simple closed/open/half-open circuit breaker;
4. outlier ejection only after evidence justifies additional state.

These primitives must be reused by future AI/provider routing rather than
reimplemented in a category-specific subsystem.

### Routing and response policy

Core selected scope:

- method matching;
- header presence/exact/regex matching;
- query presence/exact matching;
- response-header add/set/remove;
- bounded CORS policy and correct preflight behavior.

Explicitly deferred:

- arbitrary expression language;
- embedded scripting;
- general policy DSL;
- mirroring/canary automation.

### Configuration authority and automation

- `managed` and `file_owned` authority modes.
- Explicit drift and authority-switch behavior.
- Generated JSON Schema, lifecycle/capability metadata, and factual reference.
- Versioned external OpenAPI.
- Thin remote CLI for plan, diff, apply, stage, status, rollback, export, and diagnostics.

## Operational enhancement portfolio

### Selected runtime-dynamics tranche

- Closed-world lifecycle authority.
- Static certificate/key rotation.
- Admin authentication snapshot rotation.
- Global log format and metrics Host-label mode.
- Access-log enablement and sink generations.
- Cache scalar policy/capacity after cache correctness.
- Alt-Svc max-age and clear semantics.

### Candidate after complexity review

- Console mode and plugin-upload policy.
- Admin request/SSE limits.
- Durable audit sink.

### Gated or retained restart by default

- Cache backend/path replacement.
- Dynamic egress across every client and connection pool.
- History backend relocation.
- Admin listener enable/address changes.
- Global tracing-provider replacement.
- Dynamic TLS/mTLS connection epochs.
- HTTP/3 listener activation/deactivation.
- Dynamic ACME manager/account/issuer/cache transitions.
- Retained-address plaintext↔TLS and h2c mode transitions.

A complete planned-restart workflow is an acceptable final contract for these
settings. The programme does not measure success by the percentage of fields
made hot.

## Migration and operational evidence

- Evolve the NGINX importer into an assessor with source provenance and
  supported/approximate/ignored/blocking classifications.
- Add a representative compatibility corpus and real migration E2E.
- Add an operator-triggered, secret-safe support bundle.
- Add `jul doctor` for configuration, filesystem, listener, TLS, build-profile,
  pending-restart, and upstream diagnostics.
- Preserve the no-phone-home policy.

## Technical experiments

### AI Gateway candidate

AI is not the automatic next phase. Its issue remains `[DRAFT]` until backend
trust and generic resilience decisions are accepted, streaming ownership is
reviewed, provider credentials use the normal secret model, metrics are bounded,
and dependency/binary-size budgets are fixed.

The first tranche is limited to an OpenAI-compatible front door, two or three
providers, streaming, model routing, bounded fallback, and token/cost metrics
using existing auth, egress, transport trust, and observability.

Semantic cache, broad guardrails, autonomous configuration, complex tenant
billing, and a large provider catalogue are excluded initially.

### Other horizons

Fleet, Kubernetes/Gateway API, GraphQL composition, Cloud, mesh, GSLB, and
distributed state remain concept horizons. They require separate decisions and
do not define current core completeness.

## Delivered history

Year 1 and Year 2 capabilities are shipped; exact maturity, GA evidence, build
tags, and feature documentation are maintained in:

- [Feature status and GA matrix](../status.md)
- [Machine-readable feature manifest](../feature-status.yaml)
- [Year 1 specification](../specs/year-1.md)
- [Year 2 specification](../specs/year-2.md)
- [Soak evidence](../soak-evidence.md)
- [Changelog](../../CHANGELOG.md)

Historical Phase 1–4 delivery remains recorded in #62 and checked-in handoff
reviews. This roadmap intentionally does not duplicate every closed issue.

## Historical roadmap

The previous v1.37 five-year roadmap remains available through Git history and
the Year 1–5 specifications. Delivered maturity stays authoritative in the
[status matrix](../status.md), [machine-readable feature manifest](../feature-status.yaml),
soak evidence and release history. This document now focuses on active portfolio
and sequencing rather than duplicating every delivered feature row.

## Maintenance

When work changes state:

1. update #62 and the relevant epic;
2. update this roadmap only when portfolio, sequence, or category changes;
3. update ADRs when a durable decision changes;
4. update the governing spec before downstream issues lose `[DRAFT]`;
5. update feature status only when shipped maturity/evidence changes;
6. update documentation and changelog in the same PR as behavior.

## Decision references

- [ADR 0003 — Maturity and GA](../adr/0003-maturity-and-ga.md)
- [ADR 0004 — Console invariants](../adr/0004-console-ui-invariants.md)
- [ADR 0012 — OSS/open-core boundary](../adr/0012-oss-open-core-boundary.md)
- [ADR 0013 — Operating model and completeness](../adr/0013-project-operating-model-and-completeness.md)
- [ADR 0014 — Appropriate operability surfaces](../adr/0014-operability-surfaces.md)
- [Operating model](../operating-model.md)
- [Core Gateway Completeness](../specs/core-gateway-completeness.md)
- [Combined audit](../audit/combined-audit-2026-08-03.md)
