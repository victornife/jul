# Jul.IA — Roadmap

> Version 2.12 · Updated 2026-09-23
>
> This roadmap owns the **durable portfolio sequence**. It deliberately does
> not duplicate volatile READY/NEXT/blocked issue state. The current issue-level
> execution tracker is [#62](https://github.com/victornife/jul/issues/62), while
> feature maturity and delivery live in [status.md](../status.md).

Jul.IA's active objective is a coherent, production-quality standalone
single-node edge and protocol gateway. Correctness and security may interrupt
any later investment. Distributed control planes and category expansion remain
separate decisions rather than implicit completion requirements.

## Sources of truth

| Question | Authority |
| --- | --- |
| What does the binary do? | Runtime code, tests, and generated configuration/lifecycle contracts |
| What is GA, Beta, merged, candidate, released, or soaked? | [Feature status](../status.md) and [`feature-status.yaml`](../feature-status.yaml) |
| What is being worked on now? | [Programme tracker #62](https://github.com/victornife/jul/issues/62) |
| What is the durable order of investment? | This roadmap |
| Which dated audit is current or superseded? | [Audit register](../audit-register.md) |

## Portfolio lanes

| Lane | Objective | Decision rule |
| --- | --- | --- |
| **Correctness and security** | Correct unsafe, misleading, protocol-invalid, or lifecycle-invalid behavior | May pre-empt every other lane |
| **Core Gateway Completeness** | Close material gaps inside the standalone gateway boundary | Architecture and product integrity, not feature-count parity |
| **Operational enhancement** | Improve long-running operation and recovery | Value and leverage must justify permanent complexity |
| **Migration and diagnostics** | Make adoption, evidence and support safer | No compatibility percentage, silent approximation, phone-home, or unsafe replay |
| **Technical experiment** | Test one bounded category hypothesis | Explicit entry gate, time box, and promote/freeze/extract/remove/defer decision |
| **Vision horizon** | Preserve possible distributed or category-expansion futures | Requires a separate activation decision |

## Current execution sequence

The durable current sequence is summarized in the active operating roadmap below. Exact issue-level status remains in #62.

## Active operating roadmap

| Stage | Durable focus | Current snapshot |
| --- | --- | --- |
| **0 — Programme and product truth** | One tracker, audit disposition, operating model and product boundary | Complete; #62 owns current execution and this roadmap owns durable sequence |
| **1 — Correctness foundation** | Strict config, protocol/security corrections, cache recertification and quality gates | Complete for the selected tranche; new defects still interrupt later stages |
| **2 — Lifecycle and structured configuration** | Closed-world lifecycle authority, transactional apply/stage/rollback, typed workflows | Complete |
| **3 — Trust boundaries** | Canonical client identity and consistent backend TLS/mTLS identity | Complete; GA with stable v2.0.0 (#409) |
| **4 — Routing and response policy** | Method/header/query predicates, response headers, CORS and typed operation surfaces | Published in v2.0.0 as a separate Beta capability; older Core HTTP GA does not promote it |
| **5 — Generic resilience** | Admission, queue/connection bounds, retry budget/deadline/backoff, circuit state and bounded operations evidence | #287/#144 closed; published in v2.0.0 as Beta. Further feature-specific GA evidence remains a separate decision |
| **6 — Configuration authority and automation** | Managed/file-owned authority, generated contracts, supported external API, thin remote CLI | #150/#151 closed; authority, versioned external API and remote CLI published in v2.0.0 as separately tracked Beta capabilities |
| **7 — Selected runtime dynamics** | Value-ranked runtime changes and truthful restart boundaries | Bounded tranche complete (#88); selected changes published as Beta. Further hot reload requires an explicit value trigger |
| **8 — Migration and diagnostics** | NGINX assessment, evidence, support bundle and `jul doctor` | Base assessment/diagnostics published in v2.0.0 as Beta; #426/#365 completed on post-release `main`. Focused #366/#367 remain open; #368 owns follow-through guidance and optional full corpus |
| **9 — One bounded experiment** | AI Gateway or another explicitly approved category | #162 remains deferred under #113; no experiment activated by the v2.0.0 release |
| **10 — Integrated closure** | Exact-SHA verification, protocol/failure matrix, lean/full gates, E2E, soak and release evidence | Completed for stable v2.0.0 (#425/#421); new unreleased features require their own evidence and publication decision |

For exact issue state, child decomposition, active pull requests and sequencing,
read #62. This table changes only when the durable portfolio boundary or stage
outcome changes.

## Current programme boundary

### Complete foundations

- The selected cache correction and recertification programme is complete; the
  response cache retains GA.
- Closed-world lifecycle classification and generated lifecycle mirrors are
  complete.
- Structured configuration Phase 5 is complete.
- ADRs 0016–0019 define trust, resilience, routing/response policy, authority,
  generated contracts and resource identity.
- Canonical inbound identity, backend trust, routing/response policy,
  configuration authority and generated configuration contracts are implemented
  on `main`.

### Current follow-through

- Published additions with Beta maturity keep their own entries in
  [feature status](../status.md), separate from older GA rows. Stable publication
  changes delivery, not maturity or unmet GA criteria.
- The versioned external Admin API and remote CLI are published Beta surfaces;
  Console-only routes remain outside the external contract unless explicitly
  classified and generated into OpenAPI.
- #426 and #365 added bounded NGINX import translations and HTTP migration E2E
  on post-v2.0.0 `main`; #366/#367 remain focused migration evidence work.
  #368 owns the migration-impact rule in development guidance and an optional
  heavier public corpus. These do not retroactively change the tagged importer.
- #422 holds non-blocking host-fault/continuous-scrape soak follow-up; no new
  v2.0.0 release gate is implied.

## Published stable checkpoint

Stable [`v2.0.0`](https://github.com/victornife/jul/releases/tag/v2.0.0)
was published 2026-09-21 at
`d56f5ceaf7ddb8a3875cbe6e27c9540db4130f75`, after the #420 WASM fix
and #421 post-fix soak. Exact release evidence is recorded in
[#425](https://github.com/victornife/jul/issues/425).

`v2.0.0-rc.1` remains a distinct immutable prerelease at
`c9ab3a05af6a6088b2721de0a87995ce37d468cf`;
its [candidate evidence](../release-candidates/v2.0.0-rc.1.md) and the older
[`v1.32.1-rc.1` record](../release-candidates/v1.32.1-rc.1.md) are historical
checkpoints. Unreleased changes on `main` require a separate future release.

## Core Gateway Completeness boundary

The standalone product includes:

- HTTP/1.1, HTTP/2/h2c, HTTP/3, TLS/mTLS, gRPC and optional L4 proxying;
- deterministic request routing and bounded response policy;
- trusted client identity and backend peer identity;
- balancing, health, discovery and generic resilience;
- security policy, secrets and auxiliary egress controls;
- strict configuration, lifecycle, apply, stage, rollback and history;
- generated configuration contracts and supported automation surfaces;
- observability, diagnostics and operational recovery;
- explicit NGINX migration assessment;
- bounded WASM extensibility and supported release profiles.

The following remain outside the core boundary unless a later ADR changes it:
production fleet control plane, Kubernetes Gateway API controller, distributed
cache/rate limiting, hosted cloud, service mesh, GSLB/CDN, GraphQL composition,
AI Gateway, and full parity with NGINX/Envoy/Kong/Caddy/Traefik.

## Selected runtime dynamics

The bounded value-ranked tranche is complete (#88). Certificate material,
selected admin/logging/cache policy, Alt-Svc advertisement and other selected
transitions use the existing transactional preparation/publication and resource
lifetime models. The corresponding additive status row remains Beta.

Many structural fields deliberately remain restart-required. A complete and
truthful `stage_restart` path is acceptable; further hot-reload work requires
measured operator value or reusable architectural leverage.

## Migration and diagnostics

The migration lane is evidence-oriented:

- deterministic per-directive assessment rather than a compatibility score;
- source provenance and bounded root-confined include traversal;
- a bounded reviewed corpus with selected-dimension comparison; an optional
  public-derived full tier requires #368 provenance and admission review;
- no automatic production cutover or unsafe traffic replay;
- support bundles and diagnostics that are explicit, bounded and secret-safe;
- no phone-home or automatic upload.

## Experiment governance

At most one major category-expansion experiment is active. It must declare its
hypothesis, prerequisites, dependency/binary budget, test strategy, time box and
exit decision. Generic trust, resilience, streaming ownership, secrets and
observability must be reused rather than duplicated inside the experiment.

## Completion evidence

The stable v2.0.0 closure recorded an exact SHA, cross-platform lean/full
artifacts, CI and release gates, the post-#420 soak and residual risk in #425.
#422 remains a non-blocking follow-up. This evidence is specific to the tagged
release; #426/#365 and other post-release work require their own verification
and publication decision before a later release.

Future selected work must preserve consistent maturity/delivery records,
protocol and failure-boundary evidence, race/leak and privacy review, and
release notes appropriate to its scope.

## Historical relationship

Earlier phase-by-phase roadmaps, audit findings and delivery notes remain in Git
history, issue comments, the changelog and dated audit records. They are
historical evidence, not a second active roadmap. When current issue-level state
changes, update #62; update this document only when the durable portfolio or a
stage outcome changes.
