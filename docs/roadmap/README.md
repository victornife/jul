# Jul.IA — Roadmap

> Version 2.5 · Updated 2026-08-09
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
| **Correctness and security** | Restore current documented, protocol, security, lifecycle, and compatibility contracts | Evidence and severity | Preserve the certified cache, lifecycle, and structured-configuration baseline while architecture decisions proceed |
| **Core Gateway Completeness** | Close material gaps inside the standalone gateway boundary | Architecture and product integrity | Trust boundaries, backend TLS/mTLS, resilience, routing policy, automation contracts |
| **Operational enhancement** | Improve long-running operation without redefining the core | Value × leverage ÷ permanent complexity | Selected hot reload, diagnostics, recovery, migration assessment |
| **Technical experiment** | Explore a new category through a bounded, removable tranche | Hypothesis, prerequisites, budget, evidence, exit decision | AI Gateway candidate only after generic trust/resilience decisions and an explicit portfolio decision |
| **Vision horizon** | Preserve possible large future categories | Separate activation decision | Fleet, Kubernetes controller, distributed state, Cloud, mesh, GSLB, GraphQL composition |

## Current execution sequence

Implementation remains serial around shared architecture even though the
portfolio has parallel lanes.

| Stage | Focus | Exit criteria | Status |
| --- | --- | --- | --- |
| **0 — Programme and truth** | Combined audit, current product truth, operating model and historical-audit disposition | One audit, one tracker, canonical docs synchronized | ✅ reconciled; #114/#119/#130 closed |
| **1 — Immediate non-cache correctness** | Strict configuration, HTTP/3 mTLS, ACME, compression, access logs, metrics and WAF contracts | No known non-cache P0; selected P1 corrections documented and tested | ✅ verified in `v1.32.1-rc.1`; #129 remains a non-blocking quality track |
| **2 — Cache correctness** | Generation-owned revalidation, immutable entries, HTTP semantics, upgrade transparency, recertification | Race-clean, protocol-safe, truthful conformance matrix | ✅ complete: #131, #133 and #132 merged; #134 recertification closed #107; the cache retains GA |
| **3 — Lifecycle authority** | Closed-world field inventory, Go registry authority, generated/checkable mirrors | Every field classified exactly once; no unknown path defaults to hot | ✅ complete: #89 |
| **4 — Structured configuration Phase 5** | Batch preview, entity CRUD, global operations, Console migration, E2E | Preview/apply share one executor and authoritative lifecycle data | ✅ complete: #77 → #78 → #79 → #80 → #81 → #82 |
| **5 — Core architecture decisions** | Trust, resilience, routing, configuration authority/automation | ADRs merge and downstream drafts become implementation-ready | ▶ next: #115 → #116 → #117 → #118; #115 READY / NEXT, not started |
| **6 — Core implementation** | Canonical client identity, backend trust, generic resilience, routing policy, schema/API/CLI | Standalone completeness gaps closed with protocol/operational evidence | ⬜ planned; gated by governing ADRs |
| **7 — Selected runtime dynamics** | Value-ranked certificate, credential, logging, sink, cache-policy, and Alt-Svc transitions | Selected settings are safely dynamic; structural settings retain planned restart | ⬜ planned |
| **8 — Migration and diagnostics** | NGINX assessment, provenance, compatibility corpus, support bundle, `jul doctor` | Operator-safe evidence and recovery workflows | ⬜ planned |
| **9 — One bounded experiment** | AI Gateway or another explicitly approved category | Promote, continue experimental, freeze, extract, remove, or defer | 🔒 gated |
| **10 — Integrated closure** | Exact-SHA audit, protocol matrix, failure injection, E2E, soak, compatibility and release evidence | No unsupported claims or unresolved selected work | ⬜ planned |

### Tracker-numbering crosswalk

The roadmap deliberately consolidates the more granular numbering in #62:

- roadmap Stage 0 = #62 Stage 0 programme reconciliation plus Stage 1 audit/documentation truth;
- roadmap Stage 1 = the completed #62 Stage 2 non-cache correctness tranche;
- roadmap Stage 2 = #62 Stage 3 cache correctness and recertification (complete);
- roadmap Stage 3 = #62 lifecycle authority (#89), complete;
- roadmap Stage 4 = #62 structured configuration Phase 5 (#77 → #78 → #79 → #80 → #81 → #82), complete;
- roadmap Stage 5 = the next #62 core architecture-decision stage, ordered #115 → #116 → #117 → #118 and not yet started.

This avoids two competing execution models. #62 owns issue-level status;
this roadmap owns the durable portfolio sequence.

## Immediate critical path

```text
#165/#166 — product truth, operating model and completeness boundary (complete)
    ↓
#123/#124/#126/#127 — selected correction tranche (complete)
    ↓
v1.32.1-rc.1 — independently verified published prerelease (complete; not stable)
    ↓
#131 → #133 → #132 → #134 — cache correctness and recertification (complete; #107 closed)
    ↓
#89 — closed-world lifecycle authority (complete)
    ↓
#77 → #78 → #79 → #80 → #81 → #82 — structured configuration Phase 5 (complete)
    ↓
#115 → #116 → #117 → #118 — core architecture decisions (next; #115 READY / NEXT, not started)
    ↓
selected core implementations, each gated by its governing ADR
```

The selected correction tranche and its published `v1.32.1-rc.1`
prerelease checkpoint are complete and independently verified. The cache
correctness programme is complete and #107 is closed: the response cache retains
GA on the strength of the 2026-08-07 recertification. Closed-world lifecycle
authority (#89), the shared atomic patch assessment (#77), typed Route workflows
(#78), typed App/upstream workflows (#79), sparse global operations (#80), the
Global and Traffic Controls Console migration (#81), and the Phase 5 closure
work (#82) are complete. The next stage is the architecture-decision sequence
#115 → #116 → #117 → #118; #115 is READY / NEXT, but implementation has not
started. Shared edits to configuration authority, trust, resilience, routing,
or downstream implementation remain gated by those decisions.

Phase 5 closure does not authorize universal hot reload, does not imply dynamic
cache backend replacement, and does not automatically continue into an AI
Gateway. Correctness or security findings may still interrupt later work.

## Correctness and security backlog

### Completed in the current correction tranche

- Strict rejection of unknown TOML fields with the documented `server_name` compatibility alias.
- Complete server-level mTLS parity on HTTP/3.
- Exclusive ACME HTTP-01 versus TLS-ALPN-01 challenge selection.
- `Cache-Control: no-transform` enforcement in dynamic compression.
- Deterministic HTTP/3 UDP preflight testing on Windows.
- Deterministic reload/apply deadline tests through an injected clock/timer seam, with race-scaled integration margins and explicit timer-registration synchronization (#185, #219, #220, #222).
- Frontend dependency and CodeQL reflected-output hardening.
- Exact `v1.32.0` Prometheus contract reconstruction, additive-current inventory, and CI drift protection (#126).
- Fail-closed enum, worker, duration, size, status, and scalar validation with a machine-readable value contract (#123).
- Path-only, bounded WAF matched-request logging with query and macro-expanded request data omitted (#127).
- Explicit access-log enablement with restart-truthful sinks and deprecated legacy-field handling (#124).
- Cache concurrency, lifecycle, protocol and HTTP conformance restored, with an integrated source audit, executable behavior matrix, race/protocol evidence, benchmarks and soak (#131, #133, #132, #134; epic #107 closed).
- Closed-world lifecycle authority (#89) and the complete structured-configuration sequence #77 → #78 → #79 → #80 → #81 → #82.

### Remaining immediate correctness

No Phase 5 correctness item remains open. Preserve the certified cache,
lifecycle, and structured-configuration baseline while #115 → #116 → #117 →
#118 proceeds through architecture decisions. Any newly discovered correctness
or security defect may pre-empt that sequence and must be handled on its own
focused correction path.

### Required quality foundation

- Preserve a fully green required CI baseline.
- Add semantic drift guards for schema, defaults, lifecycle, metrics, and claims.
- Add focused security-package coverage and negative-test gates.
- Preserve #130's exact-SHA maintainer certification and historical-supersession record; no independent two-human certification is claimed.

## Release-candidate checkpoint

The immutable **`v1.32.1-rc.1`** checkpoint is complete at
`9a936d0cc1bc3f7086f38ca87741d9d09f950e25`. Its exact-main CI, release-ref
preflight, full-tag gate, five-minute soak-smoke, 12-cell lean/full matrix,
checksums, embedded SPDX SBOMs, and all provenance/SBOM attestations passed. The
GitHub Release is published as a prerelease; see the
[candidate evidence](../release-candidates/v1.32.1-rc.1.md). Stable publication
remains a later explicit decision. The response-cache correctness programme,
closed-world lifecycle authority, and the full #77 → #82 structured-configuration
programme have since completed on `main`. The next programme stage is the
#115 → #116 → #117 → #118 architecture-decision sequence. #115 is accepted as
[ADR 0016](../adr/0016-inbound-identity-and-backend-peer-trust.md) and its
implementation has started: the inbound lane runs #135 → #136 → #259 and the
backend lane #137 → #138 and #139 (both required) → #140.

## Core Gateway Completeness backlog

### Inbound identity

Decided by [ADR 0016](../adr/0016-inbound-identity-and-backend-peer-trust.md);
implemented as #135 → #136 → #259.

- ~~Per-listener trusted proxy CIDRs~~ — delivered by #135 as
  `[servers.client_address].trusted_proxies`, scoped per listen address and
  enforced identical across server blocks sharing a `listen`.
- ~~Standards-aware `Forwarded` and X-Forwarded-For processing~~ — delivered by
  #135, fail-closed and fuzz-tested, with no chain merging.
- ~~Right-to-left trusted-hop evaluation~~ — delivered by #135.
- ~~One canonical effective client identity used by auth, rate limiting, WAF,
  access logs, diagnostics, and upstream forwarding~~ — derived and published by
  #135, adopted by every consumer in #136, and closed out by #259 with the
  listener-granularity API, the Console editor, the NGINX realip import and the
  multi-proxy H1/H2/H3 end-to-end coverage.

Inbound identity is complete. The capability ships as **Beta** (see
[status.md](../status.md)): it is merged but not yet tagged, released or soaked,
so the post-GA soak gate is open by definition.

### Backend transport trust

Decided by [ADR 0016](../adr/0016-inbound-identity-and-backend-peer-trust.md);
implemented as #137 → #138 and #139 (both required) → #140.

- ~~One normalized backend TLS policy supporting private roots, client
  certificates, SNI, minimum version, and peer identity constraints~~ —
  delivered by #137 as `backend_tls` plus `internal/backendtls`, with an
  explicit `ca_mode` enum and prefixed `peer_identities`.
- ~~Equivalent enforcement across HTTP proxy, native gRPC, transcoding, active
  health checks, and discovery-backed targets~~ — delivered by #138 (HTTP
  transports), #139 (native gRPC and transcoding) and #140 (health probes, the
  status surface and the `hot_reload` reclassification the wiring earned).
- Named reusable TLS profiles remain a follow-up only if representative
  configurations demonstrate sufficient repetition. Every consumer accepts only
  the resolved policy type, so adding them changes resolution, not transports.

Backend transport trust is complete. The capability ships as **Beta** (see
[status.md](../status.md)): merged but not yet tagged, released or soaked.

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

### P5-05 / issue #81 delivered boundary

P5-05 migrated Global, Compression, global Rate Limit, and adjacent server
Limits to sparse typed patches, and made the existing complete cache table
stage-only with pinned raw handoff safety. The delivered boundary remains
explicit: no `cache_set`, no dynamic cache hot swap, no combined `admin_set`,
and no `access_log_set` were implied by #81. The exact implementation and closure
evidence remains in #81/#82 and PR #255; this historical boundary does not
start or authorize any Stage 6 work.
