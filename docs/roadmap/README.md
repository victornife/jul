# Jul.IA — Roadmap

> Version 2.7 · Updated 2026-08-30
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
| **0 — Programme and product truth** | One tracker, audit disposition, operating model and product boundary | Complete; this issue reconciles later documentation drift |
| **1 — Correctness foundation** | Strict config, protocol/security corrections, cache recertification and quality gates | Complete for the selected tranche; new defects still interrupt later stages |
| **2 — Lifecycle and structured configuration** | Closed-world lifecycle authority, transactional apply/stage/rollback, typed workflows | Complete |
| **3 — Trust boundaries** | Canonical client identity and consistent backend TLS/mTLS identity | Implemented on `main`; represented as merged Beta capabilities |
| **4 — Routing and response policy** | Method/header/query predicates, response headers, CORS and typed operation surfaces | Implemented on `main`; represented separately from the older Core HTTP GA row |
| **5 — Generic resilience** | Admission, queue/connection bounds, retry budget/deadline/backoff, circuit state and bounded operations evidence | Core implementations are merged; integrated cross-protocol/soak and complete external-contract closure remain under #287/#144 at this baseline |
| **6 — Configuration authority and automation** | Managed/file-owned authority, generated contracts, supported external API, thin remote CLI | Authority and generated contracts are merged; external OpenAPI #150 and CLI #151 remain separate gates |
| **7 — Selected runtime dynamics** | High-value certificate, credential, logging, sink, cache-policy and Alt-Svc transitions | Planned and value-ranked; universal hot reload is not a requirement |
| **8 — Migration and diagnostics** | NGINX assessment/provenance/includes, compatibility corpus, support bundle and `jul doctor` | Assessment/provenance/includes are merged; corpus work has started; support bundle and doctor remain later work |
| **9 — One bounded experiment** | AI Gateway or another explicitly approved category | Gated; not an automatic continuation of core work |
| **10 — Integrated closure** | Fresh exact-SHA audit, protocol/failure matrix, lean/full gates, E2E, soak and release evidence | Planned after the selected programme |

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

### Active or incomplete closure

- New post-RC capabilities are not promoted through older GA rows; their
  maturity and delivery remain explicit in [status.md](../status.md).
- Resilience still requires the remaining integrated evidence/external-contract
  closure tracked by #287/#144.
- The supported versioned external Admin API and remote CLI remain #150/#151;
  current Console routes are not automatically the stable external contract.
- NGINX compatibility corpus and selected-dimension E2E continue after the
  assessment/provenance/include foundation.

## Release-candidate checkpoint

`v1.32.1-rc.1` is an immutable published prerelease at
`9a936d0cc1bc3f7086f38ca87741d9d09f950e25`. Its release-path checks, platform
matrix, checksums, embedded SBOMs and attestations are recorded in the
[candidate evidence](../release-candidates/v1.32.1-rc.1.md).

Current `main` is intentionally ahead of that checkpoint. A later stable tag is
a separate publication decision and must reconcile the changelog, status,
security posture, limitations and exact artifacts for that SHA.

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

The product may finish with many fields deliberately restart-required. Selected
runtime changes must reuse the existing transactional preparation/publication
and resource-lifetime models rather than introducing a universal callback
framework. Current candidates include certificate material, admin credentials,
access-log sinks, selected cache scalars and Alt-Svc advertisement state.

A complete and truthful `stage_restart` path is an acceptable final design for
unselected or structural transitions.

## Migration and diagnostics

The migration lane is evidence-oriented:

- deterministic per-directive assessment rather than a compatibility score;
- source provenance and bounded root-confined include traversal;
- a sanitized, licensed corpus with selected-dimension comparison;
- no automatic production cutover or unsafe traffic replay;
- support bundles and diagnostics that are explicit, bounded and secret-safe;
- no phone-home or automatic upload.

## Experiment governance

At most one major category-expansion experiment is active. It must declare its
hypothesis, prerequisites, dependency/binary budget, test strategy, time box and
exit decision. Generic trust, resilience, streaming ownership, secrets and
observability must be reused rather than duplicated inside the experiment.

## Completion evidence

The selected programme closes only with:

- a fresh exact-SHA source and documentation audit;
- consistent feature maturity/delivery records;
- lean/full/build-tag and cross-platform verification;
- real H1/H2/h2c/H3/TLS/mTLS/gRPC/L4 protocol suites;
- failure-boundary, race/leak, browser E2E and long-running soak evidence;
- bounded-label, secret/privacy and compatibility review;
- release notes, residual limitations and an explicit publication decision.

## Historical relationship

Earlier phase-by-phase roadmaps, audit findings and delivery notes remain in Git
history, issue comments, the changelog and dated audit records. They are
historical evidence, not a second active roadmap. When current issue-level state
changes, update #62; update this document only when the durable portfolio or a
stage outcome changes.
