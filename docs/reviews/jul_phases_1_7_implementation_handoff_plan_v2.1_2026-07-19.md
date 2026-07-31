# Jul.IA Phases 1–7 Implementation Handoff Plan

**Document status:** Implementation-ready; all planning decisions in §4 are resolved and normative
**Document revision:** 2.1
**Repository:** `victornife/jul`
**Baseline branch:** `main`
**Baseline commit:** `1daf8c32e49c6a6706becff75ad8e29fecaf39ae`
**Prepared:** 2026-07-19
**Updated:** 2026-07-31 (implementation status added)
**Execution model:** Solo maintainer, AI-assisted implementation, small reviewable changes, minimal process overhead
**Primary scope:** Exhaustive implementation plan for Phases 1–5
**Secondary scope:** Refined but intentionally lighter plans for Phases 6–7

> **Implementation status (2026-07-31).** Phases **1–4 are delivered and merged to `main`**:
> Phase 1 (product/documentation hardening), Phase 2 (correlated reload results, bounded
> managed applies, planned restart), Phase 3 (Console/admin RBAC Phase 1), and Phase 4
> (outbound-egress policy completion + hardening) are implemented with tests. Honest caveats
> per the [delivery-state vocabulary](../status.md): the Phase 4 egress work is *merged; first
> tagged release pending*, and the Phase 2/3 configuration write/apply/reload subsystem is
> *remediated; formal audit closure pending* (exact-SHA CI + two sign-offs) — see the
> [reopened configuration-audit report](../audit/old/2026-07-25-configuration-audit-closure.md)
> and the [2026-07-31 repository audit](../audit/2026-07-31-full-repository-audit.md). One
> Phase 3 item stays **deferred**: interactive RBAC token management (mint/revoke from the
> Console/API), now surfaced in the Console as **Preview**. **Phase 5** is next; Phases 6–7
> stay evidence-gated. The canonical, continuously-updated status lives in
> [docs/status.md](../status.md), [docs/roadmap/README.md](../roadmap/README.md), and
> [CHANGELOG.md](../../CHANGELOG.md).

---

## 1. Purpose

This document is the implementation handoff for the next Jul.IA programme. It converts the agreed product direction into one coherent execution specification that can be followed without reopening product or architectural decisions during coding.

It is written for a solo, AI-assisted project. The plan is detailed where implementation ambiguity would create bugs, incompatible APIs, security regressions, or repeated debugging. It deliberately avoids team-process bureaucracy, ownership matrices, parallel planning systems, duplicated status documents, and ceremonial approval steps.

It does five things:

1. Reconciles the roadmap against the repository so shipped foundations are retained rather than rebuilt.
2. Fixes the target behavior, architecture, file-level changes, API contracts, tests, documentation, rollout, and Definition of Done for Phases 1–5.
3. Records all previously open decisions as final implementation choices.
4. Adds a coherent planned-restart configuration workflow and the first three global-table patch operations.
5. Preserves the AI MVP and evidence-gated horizon strategy as bounded Phases 6–7.

Normative words:

- **MUST**: required for completion.
- **SHOULD**: expected unless repository evidence shows a simpler or safer implementation.
- **MAY**: optional or evidence-dependent.
- **Deferred**: intentionally excluded from the current phase; the required follow-up is documented so it does not need to be redesigned later.

When this plan and older roadmap/spec prose disagree, this plan governs implementation until the repository documentation is updated in Phase 1.

---

## 2. Scope and non-goals

### 2.1 In scope

- **Phase 1:** lean product operating model and documentation hardening.
- **Phase 2:** correlated reload results, bounded managed applies, and the planned-restart configuration workflow.
- **Phase 3:** Console/admin RBAC Phase 1 with named identities, custom and predefined roles, hot-reloadable policy, and attributable audit.
- **Phase 4:** completion and hardening of outbound egress policy across all supported config-driven network clients.
- **Phase 5:** structured Console entity lifecycle, batch patch preview/apply, `global_set`, `compression_set`, `rate_limit_global_set`, planned-restart UI integration, and adoption documentation.
- **Phase 6:** bounded AI Gateway MVP outline.
- **Phase 7:** evidence-based activation of at most one horizon category.

### 2.2 Explicitly deferred from Phases 1–5

- SAML, OIDC, SCIM, external identity providers, or fleet-level identity.
- Fleet control plane, Kubernetes Gateway API controller, distributed cache, or distributed rate limiting.
- API-managed RBAC token issuance and management UI beyond what is explicitly included in Phase 3.
- Semantic AI caching, comprehensive AI guardrails, autonomous config changes, Cloud billing, or full Year 4 scope.
- `cache_set` implementation in the current phase; its restart-only design and prerequisites are documented in Phase 5.
- A monolithic `admin_set`; future admin operations are decomposed and sequenced after RBAC.
- `access_log_set` implementation in the current phase; its restart-only design and possible future hot-swap path are documented in Phase 5.
- Central usage telemetry, phone-home behavior, or a hosted analytics dependency.
- A second roadmap database, owner matrix, target-release registry, or other process system separate from the human-readable roadmap.

---

## 3. Baseline reconciliation: what exists today

The plan starts from the current repository rather than older roadmap wording.

| Area | Current repository state | Planning consequence |
|---|---|---|
| Product operating model | Vision, roadmap, maturity model, compatibility policy, GA evidence, and broad evidence gates exist. Target users, quantitative activation thresholds, lean budgets, and the OSS/open-core boundary need tightening. Some horizon prose still looks more committed than it is. | Phase 1 edits and simplifies existing docs; it does not introduce a new planning platform. |
| Reload observability | `[global].reload_timeout` exists and defaults to 10 seconds. `lastReloadInfo` records `OK`, `TimedOut`, `Duration`, `At`, and `Error`, but timeout is advisory and evaluated after completion. Apply responses expose the previous reload and usually return `pending_reload=true`. | Phase 2 replaces uncorrelated/advisory behavior with one bounded result for the candidate being applied. |
| Reload transaction | `ReloadPlan` already provides resolve, validate, lifecycle, prepare, staged listeners, publish, activate, retire, runtime-state finalization, and post-commit work. Generational resources, redaction, listener staging, and watcher correlation are mature. | Retain this architecture. Add context, results, managed restoration, and staged-for-restart behavior around it. |
| Restart-required handling | Admin apply currently rejects restart-required changes with HTTP 409 and does not write them. External file edits can still leave disk ahead of runtime and trigger the pending-restart banner. | Phase 2 adds an explicit `stage_restart` mode, exact rollback metadata, and a safe discard flow. Default hot apply remains reject-without-write. |
| RBAC | A concrete design exists. Runtime still uses one shared bearer token; protected routes share one auth wrapper; audit actor is `operator`. | Phase 3 implements the design with the resolved choices in §4. |
| Egress policy | `internal/egress` already implements exact/suffix host and CIDR rules, guarded dialing, redirect coverage, and DNS validation for CIDR-only rules. It is wired into authentication and service discovery. | Phase 4 is coverage and hardening, especially ACME/OCSP and WASM fetch intersection. |
| Structured entity CRUD | Backend operations exist for server, location, and upstream add/remove. Batch apply and optimistic concurrency exist. | Phase 5 exposes them through typed batch preview/apply and guided UI flows. |
| Global-table patch operations | No typed patch operations exist for `[global]`, `[compression]`, or global `[rate_limit]`; guided editors still manipulate or upsert TOML. | Phase 5 adds the three agreed near-term operations. |
| Cache/admin/access-log tables | Guided or raw workflows exist, but these settings are mostly restart-bound and admin changes are security-sensitive. | Keep current paths, document follow-ups, and do not add misleading typed operations yet. |
| CI/docs governance | Docs checks, helper tests, full-tag CI, race, coverage, frontend, E2E, fuzz, benchmark, and soak lanes exist. | Extend only existing gates. Do not invent a parallel quality system. |

### 3.1 Required baseline reading

Before changing a phase, read the files directly relevant to that phase. The minimum shared set is:

- `docs/vision/README.md`
- `docs/roadmap/README.md`
- `docs/specs/hardening-platform.md`
- `docs/specs/console-rbac.md`
- `docs/adr/0003-maturity-and-ga.md`
- `docs/adr/0010-console-rbac.md`
- `docs/compatibility.md`
- `docs/status.md`
- `docs/feature-status.yaml`
- `internal/app/serve.go`
- `internal/app/preflight.go`
- `internal/server/server.go`
- `internal/server/reload_plan.go`
- `internal/admin/server.go`
- `internal/admin/routes.go`
- `internal/admin/audit.go`
- `internal/egress/egress.go`
- `internal/admin/patch.go`
- `internal/admin/patch_types.go`
- `internal/admin/patch_http.go`
- `internal/admin/ui/src/api/client.ts`
- `internal/admin/ui/src/lib/applyOutcome.ts`
- `internal/admin/ui/src/features/routes/RouteEditor.tsx`
- `internal/admin/ui/src/features/apps/AppEditor.tsx`

---

## 4. Resolved implementation decisions

These choices are final for this programme. Implementation must follow them unless source-level constraints prove a choice impossible or unsafe; such a contradiction should be documented in the implementing change rather than silently changing behavior.

### D-01 — Product telemetry

- Jul.IA MUST NOT phone home.
- Product signals use local runtime counters, release/download data where available, GitHub issues, voluntary diagnostics, and direct user feedback.
- No central telemetry client, identifier, background upload, or analytics service is added.
- Any future telemetry requires a separate opt-in design and is outside this plan.

### D-02 — Managed apply failure before Publish

For admin-managed writes, a failure or timeout before `ReloadPlan.Publish` MUST restore the previous exact raw bytes atomically. Runtime and disk remain on the previous version. Watcher echoes for both the attempted write and restoration are suppressed exactly once.

SIGHUP and external file-watch reloads never rewrite an externally owned file.

### D-03 — Timeout after Publish

`Publish` remains the point of no return. After Publish, Jul.IA MUST finish the minimum safe activation/finalization path and report the transaction as timed out or degraded. It MUST NOT attempt to roll back committed shared resources.

### D-04 — Apply response model

Admin apply waits synchronously for its own bounded transaction and returns the correlated result for that candidate. SIGHUP and file-watch remain asynchronous and are exposed through runtime status and the latest reload result.

### D-05 — Custom roles in RBAC Phase 1

Phase 3 includes predefined roles and config-defined custom roles. API-managed token/role creation is deferred. The permission catalog, wildcard validation, and deny-by-default route coverage ship in the same phase.

### D-06 — Anonymous loopback compatibility

- RBAC disabled: preserve current no-token loopback behavior for compatibility, with warnings and security guidance.
- RBAC enabled: anonymous access is denied regardless of bind address. At least one admin-capable principal or the explicitly configured legacy compatibility token is required.

### D-07 — RBAC policy lifecycle

Named-principal roles, tokens, disabled state, and custom-role grants are hot-reloadable through an atomic policy pointer. Revocation/disable takes effect after a successful config apply. Listener address, listener construction, and legacy `[admin].token` remain startup-bound.

### D-08 — Egress hostname semantics

Exact and suffix hostname rules authorize the DNS name. CIDR-only rules authorize resolved addresses. Hostnames are normalized for case, trailing dot, IP literals, and IDNA. The trusted DNS answer for an explicitly allowed hostname is part of the trust boundary.

### D-09 — Egress coverage

The global egress policy covers:

- JWKS;
- forward-auth;
- Consul and Kubernetes discovery;
- ACME directory/order/challenge-related outbound HTTP;
- OCSP retrieval used by Jul-managed certificate flows;
- WASM plugin fetch.

WASM fetch must satisfy both plugin-local `allowed_hosts` and the global policy; the result is the intersection.

### D-10 — OSS/open-core boundary

The standalone single-node data plane, configuration format, core proxy/TLS/security behavior, security fixes, and standalone operational surface remain OSS. Commercial value may concentrate in fleet coordination, external identity, compliance automation, hosted control plane, and support. An OSS node never requires Cloud connectivity.

### D-11 — Global-table patch operations and planned restart

The programme will implement:

1. The explicit planned-restart configuration workflow in Phase 2.
2. `global_set` in Phase 5.
3. `compression_set` in Phase 5.
4. `rate_limit_global_set` in Phase 5.

The following remain deferred but are fully specified as follow-ups in Phase 5:

- `cache_set`: implement only after the staged-restart workflow is proven; initial version is restart-only.
- `access_log_set`: implement only after the staged-restart workflow is proven, unless access sinks are first made safely hot-swappable.
- `admin_set`: do not implement as one unrestricted operation. After RBAC, split it into permission-scoped admin listener/auth/limits/history/audit/plugin-upload operations; initial lifecycle is restart-staged unless a component is deliberately made dynamic.

A candidate containing both hot-reloadable and restart-required changes is never partially applied. It is either:

- applied entirely in `hot` mode when every change is hot-applicable; or
- saved entirely in `stage_restart` mode while the current runtime remains unchanged.

### D-12 — Solo-project execution and documentation model

- `docs/roadmap/README.md` is the single active-plan source of truth.
- Do not create `active-plan.yaml`, owner matrices, release-train tables, duplicated scorecard documents, or mandatory approval workflows.
- Do not require named phase owners or separate reviewers. Security-sensitive changes use an explicit self-review checklist, focused tests, and AI-assisted review.
- Durable architecture decisions may use ADRs; ordinary implementation detail stays in the relevant spec and code.
- Evidence gates and lightweight product signals live in the roadmap rather than a separate planning database.
- Lean budgets extend existing benchmark documentation rather than creating a new standalone governance document unless the data becomes too large.
- Git history is the archive. Do not duplicate superseded horizon specs merely to preserve old prose.
- Docs checks enforce objective integrity only: links, duplicate IDs, status consistency, required banners, and generated-file drift. They must not become a prose-management framework.

---

## 5. Cross-phase sequencing and delivery model

### 5.1 Required order

1. **Phase 1 — Product operating model and lean documentation hardening**
2. **Phase 2 — Correlated reloads, bounded applies, and planned restart**
3. **Phase 3 — RBAC Phase 1**
4. **Phase 4 — Egress policy completion**
5. **Phase 5 — Structured CRUD, global patch operations, and adoption foundations**
6. **Phase 6 — AI Gateway MVP**
7. **Phase 7 — Activate one evidence-backed horizon**

### 5.2 Why this order

- Phase 1 removes contradictory roadmap signals and fixes the constraints used by later phases.
- Phase 2 provides the transaction, lifecycle, and staged-restart contract needed by every subsequent config editor.
- Phase 3 changes the admin authorization boundary before new settings operations are exposed.
- Phase 4 completes the outbound security seam before more external-provider work.
- Phase 5 finishes current-product operations and consumes the planned-restart contract before opening a new product category.

### 5.3 Safe overlap

- Phase 1 doc cleanup can overlap Phase 2 internal design.
- Phase 4 policy-unit work can begin while late Phase 3 UI work finishes, but shared `internal/app` changes should land serially.
- Phase 5 reference guides and pure frontend conversion helpers can begin during late Phase 4.
- Phase 6 code does not begin until Phases 2 and 4 are complete and the Phase 3 API contract is stable.

### 5.4 Change slicing

Use small, coherent change sets or commits because they are easier for one maintainer and AI reviewers to reason about. Each change should:

- name the phase/work package in its description;
- state any public/config contract change;
- include success and failure tests;
- update the directly affected docs and changelog;
- avoid unrelated refactors;
- pass the relevant existing CI commands.

No separate ticket hierarchy, owner assignment, or release ceremony is required by this plan.

---

## 6. Cross-phase engineering invariants

### 6.1 Compatibility

- GA configuration and API behavior remain additive within the current major version.
- Existing single-token behavior remains available while RBAC is disabled.
- Raw TOML editing remains supported as the expert escape hatch.
- Lean builds continue to compile and fail loudly when optional features are configured without their tags.
- New JSON fields are additive. Any temporary compatibility field has an explicit removal note.

### 6.2 Configuration truthfulness

- Every managed mutation follows Validate → Preview/Diff → Apply or Stage → Result → History/Rollback.
- `hot` mode never writes a candidate that cannot be fully activated live.
- `stage_restart` writes the complete candidate and does not trigger a live reload.
- Mixed lifecycle changes are never split or partially applied.
- While a planned restart is pending, normal hot applies are blocked until the staged config is restarted, updated in staged mode, or discarded.
- Desired, persisted, staged, and serving versions are distinct whenever they differ.

### 6.3 Security

- Secrets never enter logs, metric labels, audit detail, URLs, or client-visible projections.
- Server-side authorization is authoritative; UI gating is convenience only.
- Every new outbound client uses the shared egress policy.
- Every new admin route is explicitly public or permission-bound.
- New file writes use atomic replacement and restrictive permissions.
- Destructive and admin-reachability changes require explicit confirmation.

### 6.4 Operability

- Every feature exposes enough status to diagnose its actual failure mode.
- Errors distinguish rejected, restored, staged-for-restart, applied-degraded, and applied-live states.
- Console wording describes the current serving state, not merely the desired configuration.
- A staged restart always has a safe path to inspect and discard the staged bytes.

### 6.5 Leanness

- No phone-home.
- No new runtime database or service in Phases 1–5.
- New dependencies require size, license, and supply-chain review.
- Heavy optional behavior stays behind build tags.
- Docs stay consolidated; do not create a new file when a clear existing source can hold the information.

### 6.6 Quality gates

At phase completion, run the applicable subset of the existing gates, including at minimum:

```bash
make ci-pr
go test -race -p 2 -tags "brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf" ./...
pnpm --dir internal/admin/ui run typecheck
pnpm --dir internal/admin/ui run lint
pnpm --dir internal/admin/ui run test:coverage
pnpm --dir internal/admin/ui run build
pnpm --dir internal/admin/ui run e2e:ci
python3 scripts/docs-check.py
python3 scripts/test_docs_check.py
```

Targeted tagged, integration, leak, and E2E commands are listed in each phase.

---
# Phase 1 — Product operating model and lean documentation hardening

## 7. Phase 1 objective

Make the current product direction immediately understandable and executable without creating a second planning system.

**Estimated effort:** S–M, primarily documentation and small docs-check changes
**Indicative execution:** 3–7 focused working days
**Dependencies:** none
**Runtime behavior change:** none
**Implementation focus:** roadmap, vision, compatibility, benchmark constraints, and objective documentation checks

## 8. Documentation architecture to retain and simplify

The repository already has the right categories. Phase 1 should make their responsibilities explicit and remove duplicate or misleading content.

| Document | Authoritative responsibility |
|---|---|
| `docs/vision/README.md` | Product identity, target users, pillars, architectural commitments, OSS/open-core principles, and long-term evidence-gated direction. |
| `docs/roadmap/README.md` | The current execution sequence, active backlog, lightweight evidence signals, and horizon activation thresholds. This is the only active-plan source. |
| `docs/specs/*.md` | Implementation design for work that is active or mature enough to design. Horizon specs are clearly labelled non-committed design notes. |
| `docs/status.md` + `docs/feature-status.yaml` | Shipped-feature maturity and GA evidence only. They do not plan future work. |
| `docs/adr/*.md` | Durable architectural or product-boundary decisions only. |
| `CHANGELOG.md` | User-visible shipped changes. |
| Git history | Historical versions; no duplicate archive is required merely to preserve superseded prose. |

Do not create:

- `docs/roadmap/active-plan.yaml`;
- a separate owner/release registry;
- a standalone product-scorecard document;
- a second maturity/status table;
- copied horizon-spec archives unless the content has independent historical value beyond Git history.

## 9. Phase 1 deliverables

### P1-WP1 — Roadmap status and active sequence

#### Required changes

Update `docs/roadmap/README.md` with a prominent `## Active operating roadmap` near the top.

The exact sequence is:

1. Phase 2 — reload results, bounded managed applies, and planned restart;
2. Phase 3 — RBAC Phase 1;
3. Phase 4 — egress coverage and hardening;
4. Phase 5 — structured CRUD plus near-term global-table patch operations;
5. Phase 6 — time-boxed AI Gateway MVP;
6. Phase 7 — activate at most one evidence-backed horizon.

Reconcile HP rows:

- HP-01: partially delivered; Phase 2 completes it.
- HP-02: design complete; Phase 3 implements it.
- HP-03: delivered.
- HP-04: delivered; hooks and local CI parity already exist.
- HP-05: delivered.
- HP-06A backend entity CRUD: delivered.
- HP-06B Console entity CRUD: Phase 5 active.
- HP-06C near-term global tables:
  - `global_set`: Phase 5 active;
  - `compression_set`: Phase 5 active;
  - `rate_limit_global_set`: Phase 5 active;
  - `cache_set`: deferred follow-up;
  - admin operations: deferred until Phase 3 RBAC, then decomposed;
  - `access_log_set`: deferred follow-up.
- HP-07 core egress policy: delivered; Phase 4 completes integration and diagnostics.

The roadmap must explicitly state that planned restart is delivered as a shared Phase 2 foundation and consumed by later restart-bound settings.

#### Files

**Modify**

- `docs/roadmap/README.md`
- `docs/specs/hardening-platform.md`
- `docs/index.md`
- `docs/specs/README.md`
- `README.md` only where the current-next-work summary is stale

#### Acceptance

- A reader can identify current sequence and deferred items in under two minutes.
- No HP item is simultaneously described as active and delivered.
- The three near-term global operations and the three deferred table families are unambiguous.
- No active roadmap information depends on a generated or separate manifest.

### P1-WP2 — Target users, jobs, anti-personas, and product promise

Add a concise section to `docs/vision/README.md`.

#### Required content

**Primary user**

Small-to-medium platform or infrastructure teams that need modern proxy, TLS, policy, gRPC, observability, and extensibility without operating a heavyweight gateway platform.

**Primary jobs**

- modernize or replace an NGINX estate;
- expose internal gRPC services safely;
- run a serious single-node edge/protocol gateway;
- manage routing, security, and observability from one binary;
- extend behavior through sandboxed WASM;
- establish a simple single-node base before any fleet platform.

**Secondary users**

- self-hosters and application teams;
- teams needing lightweight protocol adaptation;
- future AI platform teams if Phase 6 proves demand.

**Anti-personas**

- hyperscale CDN operators;
- organizations seeking a complete service mesh now;
- teams requiring a managed global data plane today;
- teams already standardized on a larger gateway platform with no need for a standalone node.

**Product promise**

> Jul.IA remains fully useful and operable as a standalone single-node product even if fleet, AI, or hosted capabilities are added later.

#### Files

**Modify**

- `docs/vision/README.md`
- `README.md` with only a short linked summary
- `docs/index.md` if navigation does not already expose the vision

#### Acceptance

- Every active item can be tied to a named target user or primary job.
- The vision says who should not choose Jul.IA as clearly as who should.
- No new marketing superlative conflicts with “leanest serious edge/protocol gateway.”

### P1-WP3 — Quantitative horizon gates and lightweight product signals

Keep this material in `docs/roadmap/README.md`; do not create a second scorecard system.

#### Evidence-gate format

For each horizon category, add a compact table containing:

- hypothesis;
- minimum evidence threshold;
- technical prerequisites;
- continue/extend/stop outcome;
- link to the evidence or decision note when the gate is evaluated.

#### Initial thresholds

**Fleet**

- at least three external users operating multiple nodes;
- at least two operating ten or more nodes;
- repeated rollout, drift, or rollback pain;
- at least two willing to pilot a control plane.

**Kubernetes/Gateway API**

- at least three credible requests;
- at least two pilot commitments;
- an agreed bounded Gateway API subset;
- evidence that a controller will not weaken the standalone product.

**AI continuation**

- at least two real users with meaningful traffic;
- three provider adapters demonstrated;
- reliable streaming and failover;
- useful token/cost accounting;
- acceptable `ai` build-tag size/runtime cost;
- a clear reason to choose Jul.IA over a generic reverse proxy or provider-specific client.

**Cloud**

- fleet demand already proven;
- repeated request for a hosted control plane;
- acceptance of the BYO-node/control-traffic-only model;
- credible willingness to pay.

**GraphQL**

- multiple concrete composition cases not cleanly solved by current REST/gRPC support;
- willingness to define explicit schema/resolver mappings and operational limits.

#### Lightweight product-signal table

Add a maintainable table to the roadmap, updated only when real evidence exists:

| Signal | Collection method | Current evidence | Last updated |
|---|---|---|---|
| Known production users | voluntary reports/issues | — | — |
| Multi-node users | voluntary reports | — | — |
| Kubernetes requests | issues/discussions | — | — |
| AI Gateway pilots | direct pilots/issues | — | — |
| Raw-editor friction | local audit/support reports | — | — |

Rules:

- no central telemetry;
- no user payloads;
- no mandatory monthly reporting;
- empty evidence is acceptable;
- update when making a horizon decision, not as ceremony.

#### Files

**Modify**

- `docs/roadmap/README.md`
- `docs/vision/README.md` to link the operational gates
- `docs/adr/0003-maturity-and-ga.md` only if a link is useful; do not duplicate thresholds

### P1-WP4 — Benchmark-document cleanup and lean budgets

Extend the existing benchmark/performance documentation instead of creating `docs/lean-budgets.md`. Before treating `docs/benchmarks.md` as the canonical budget page, reconcile every configuration example in it against the current schema. This cleanup is mandatory because the present page contains historical examples that no longer match the implementation.

#### Mandatory schema reconciliation

Update or remove stale examples before adding any new budget claims:

- remove `[global].max_idle_conns` and `[global].idle_conn_timeout` examples unless those fields are intentionally introduced and implemented in the same change; they are not part of the current `GlobalConfig` contract;
- replace legacy compression examples such as `gzip_level` and `brotli_level` with the current `[compression]` contract (`enabled`, `encoders`, `level`, `min_size`, `types`, `precompressed`);
- replace legacy rate-limit examples such as `rate = "100r/m"`, array/table forms that are no longer accepted, or other obsolete syntax with the current integer requests-per-second schema (`enabled`, `key`, `rate`, `burst`, `max_conns`);
- run every TOML fragment in the page through `config.Parse` and `config.Validate` in a documentation fixture test;
- do not document tuning knobs that are only Go transport internals or design ideas rather than supported configuration.

Add a small table-driven docs/config test, preferably in the existing documentation or config test package, that extracts named benchmark-page TOML fixtures or stores equivalent fixtures under `testdata`. The test must fail when a published example stops parsing or validating.

#### Required budget section

After the page is schema-correct, add `## Lean product budgets` to `docs/benchmarks.md`. Track:

- lean binary size;
- full binary size;
- per-build-tag binary delta;
- idle RSS;
- startup latency;
- config parse/validate latency;
- reload p50/p95/p99;
- reverse-proxy p99 overhead;
- hot-path allocations where stable;
- Console initial-route compressed size;
- dependency/license delta for new packages.

#### Gate behavior

- Record current baselines first.
- Deterministic size and generated-asset budgets may gate CI.
- Shared-runner latency and RSS are evidence artifacts until stable runners exist.
- A +10% change requires explanation; a +20% deterministic regression should fail unless deliberately accepted in the same change.
- Optional features record their build-tag delta.

#### Files

**Modify**

- canonical benchmark documentation, expected `docs/benchmarks.md`
- existing benchmark scripts only if machine-readable output is easy to add
- `.github/workflows/ci.yml` only for deterministic checks already compatible with the current CI structure
- `docs/vision/README.md` to link the budget definition

**Optional small data file**

- `docs/benchmarks/lean-baseline.json`, only if existing scripts can consume it cleanly

#### Tests

- every supported TOML example on `docs/benchmarks.md` parses and validates against the current schema;
- no unsupported tuning key is published as configuration;
- lean and full binary size reporting remains reproducible;
- Console size gate remains active;
- any baseline JSON has a small schema test;
- do not add a fragile latency gate.

### P1-WP5 — OSS/open-core boundary ADR

Create `docs/adr/0012-oss-open-core-boundary.md`.

#### Required commitments

- standalone single-node data plane remains OSS;
- core configuration format and documented standalone admin API remain open;
- security fixes are never commercial-only;
- standalone operation never requires Cloud connectivity;
- users can export config, history, and audit data;
- commercial value may center on fleet coordination, external identity, compliance automation, hosted operations, and support;
- a future packaging change requires an ADR and migration path.

#### Files

**New**

- `docs/adr/0012-oss-open-core-boundary.md`

**Modify**

- ADR index if present
- `docs/vision/README.md`
- `docs/roadmap/README.md`
- `README.md` with one concise link
- `docs/compatibility.md` with a cross-reference only

### P1-WP6 — Horizon spec simplification

The Year 3–5 files may retain technical ideas, but they must not read like scheduled annual execution plans.

#### Required changes

For `docs/specs/year-3.md`, `docs/specs/year-4.md`, and `docs/specs/year-5.md`:

- add a standard `Concept horizon — not committed` banner;
- remove or clearly label squad assignments and quarter schedules as historical design notes;
- prefer deleting stale scheduling prose over copying it into another archive; Git history preserves it;
- retain target user/problem, possible scope, architectural hypotheses, risks, non-goals, and evidence gate;
- state that implementation details must be revalidated when a gate is activated;
- keep the bounded AI MVP separate from the full Year 4 concept.

#### Files

**Modify**

- `docs/specs/year-3.md`
- `docs/specs/year-4.md`
- `docs/specs/year-5.md`
- `docs/specs/README.md`

### P1-WP7 — Minimal objective docs checks

Extend `scripts/docs-check.py` only where a mechanical rule prevents real drift.

Required checks:

- duplicate active roadmap IDs fail;
- active roadmap links resolve;
- Year 3–5 specs contain the standard horizon banner;
- delivered items are not listed under active work;
- feature-status generated/human-readable consistency remains enforced;
- existing audit-register checks continue to run.

Do not add:

- owner validation;
- release-date validation;
- prose completeness scoring;
- a YAML roadmap parser;
- mandatory decision-record links for ordinary changes.

#### Files

**Modify**

- `scripts/docs-check.py`
- `scripts/test_docs_check.py`
- `.github/workflows/ci.yml` only if the existing docs job does not already execute both

#### Tests

- duplicate active ID fixture fails;
- broken link fixture fails;
- missing horizon banner fails;
- valid current docs pass.

## 10. Phase 1 change sequence

1. **P1-CS1:** Roadmap status, active sequence, target users, and evidence gates.
2. **P1-CS2:** Lean budget section and OSS/open-core ADR.
3. **P1-CS3:** Horizon simplification, navigation cleanup, and minimal docs checks.

These labels are sequencing aids, not a requirement to open separate pull requests. A change set may be one commit or a short commit series, but it must remain independently understandable and reversible.

## 11. Phase 1 Definition of Done

- Roadmap sequence and HP statuses are accurate.
- Target users, primary jobs, anti-personas, and standalone product promise are explicit.
- Horizon gates have measurable thresholds.
- Lightweight product signals are documented without telemetry.
- Lean budgets are recorded in existing benchmark documentation.
- OSS/open-core ADR is accepted.
- Year 3–5 specs cannot be mistaken for committed schedules.
- Docs checks enforce only objective integrity.
- No active-plan manifest, owner matrix, separate scorecard, or duplicated archive was introduced.
- No runtime behavior changed.

---
# Phase 2 — Reload observability, bounded managed applies, and planned restart

## 12. Phase 2 objective

Complete HP-01 and establish one truthful configuration-transaction model for both live changes and changes intentionally saved for the next process restart.

At phase completion:

- a managed hot apply waits for and returns the result of its own reload;
- reload work is bounded at phase boundaries;
- pre-Publish failure restores the previous file;
- post-Publish failure is reported without unsafe rollback;
- restart-required configuration can be explicitly staged, inspected, updated, and discarded without changing the running runtime;
- mixed hot/restart changes are never partially applied.

**Estimated effort:** M–L
**Indicative execution:** 5–8 focused weeks
**Dependencies:** Phase 1 documentation contracts
**Implementation focus:** server/runtime, app composition, admin API, Console, tests, and lifecycle documentation

## 13. Existing implementation to retain

Retain without architectural replacement:

- `ReloadPlan` phase structure and prepare/commit/abort semantics;
- immutable `config.Candidate` resolution and redaction state;
- listener staging before activation;
- generational handler/upstream resource retirement;
- typed reload sources and candidate-bearing admin requests;
- one-shot watcher-echo suppression;
- coherent live runtime snapshot;
- preflight validation, bind probes, and lifecycle checks;
- `[global].reload_timeout` schema/default/validation;
- LastReload storage concept;
- apply-outcome UI component structure;
- config history, rollback, and optimistic concurrency;
- pending-restart classification based on startup versus disk/live state.

Replace or extend:

- advisory timeout evaluated only after completion;
- `previous_reload` in apply responses;
- `pending_reload=true` as the normal managed-apply result;
- one undifferentiated `OK/Error` result;
- duplicated raw/structured write closures in `serve.go`;
- restart-required 409 as the only supported managed workflow;
- lack of exact staged-config rollback metadata;
- ambiguous desired, staged, persisted, and serving versions.

## 14. Transaction and result contracts

### 14.1 Reload result

Create `internal/server/reload_result.go`.

```go
type ReloadOutcome string

const (
    ReloadAppliedLive     ReloadOutcome = "applied_live"
    ReloadAppliedDegraded ReloadOutcome = "applied_degraded"
    ReloadNotApplied      ReloadOutcome = "not_applied"
    ReloadSavedNotLive    ReloadOutcome = "saved_not_live" // external file ownership only
)

type ReloadSubsystemStatus string

const (
    ReloadSubsystemOK       ReloadSubsystemStatus = "ok"
    ReloadSubsystemFailed   ReloadSubsystemStatus = "failed"
    ReloadSubsystemTimedOut ReloadSubsystemStatus = "timed_out"
    ReloadSubsystemSkipped  ReloadSubsystemStatus = "skipped"
    ReloadSubsystemNotRun   ReloadSubsystemStatus = "not_run"
)

type ReloadSubsystemResult struct {
    Status     ReloadSubsystemStatus `json:"status"`
    DurationMS int64                 `json:"duration_ms,omitempty"`
    Error      string                `json:"error,omitempty"`
}

type ReloadResult struct {
    ID             string                `json:"id"`
    Source         ReloadSource          `json:"source"`
    DesiredVersion string                `json:"desired_version,omitempty"`
    ServingVersion string                `json:"serving_version,omitempty"`
    StartedAt      time.Time             `json:"started_at"`
    CompletedAt    time.Time             `json:"completed_at"`
    DurationMS     int64                 `json:"duration_ms"`
    Outcome        ReloadOutcome         `json:"outcome"`
    Persisted      bool                  `json:"persisted"`
    Published      bool                  `json:"published"`
    TimedOut       bool                  `json:"timed_out"`
    TimedOutPhase  string                `json:"timed_out_phase,omitempty"`
    FailedPhase    string                `json:"failed_phase,omitempty"`
    HTTP           ReloadSubsystemResult `json:"http"`
    Stream         ReloadSubsystemResult `json:"stream"`
    Error          string                `json:"error,omitempty"`
}
```

Rules:

- JSON durations are milliseconds, never raw `time.Duration` nanoseconds.
- `Published` says whether the point of no return was crossed.
- `Persisted` is decorated by the app-layer coordinator because the server does not own the file.
- `DesiredVersion` is the candidate canonical version.
- `ServingVersion` is read from the final runtime snapshot.
- No secret, config body, file path, or transaction ID appears in metric labels.

### 14.2 Apply mode and API result

Define the app-layer mode in `internal/app/config_apply.go`:

```go
type ApplyMode string

const (
    ApplyHot          ApplyMode = "hot"
    ApplyStageRestart ApplyMode = "stage_restart"
)
```

The admin API exposes one additive result envelope:

```go
type ConfigApplyResult struct {
    OK             bool                  `json:"ok"`
    Mode           string                `json:"mode"`
    Version        string                `json:"version,omitempty"`
    ServingVersion string                `json:"serving_version,omitempty"`
    Status         []FeatureStatus       `json:"status,omitempty"`
    Reload         *server.ReloadResult  `json:"reload,omitempty"`
    PendingRestart *PendingRestartStatus `json:"pending_restart,omitempty"`
    Message        string                `json:"message,omitempty"`
}
```

Do not represent staged-for-restart as a fake reload outcome. No reload occurs in `stage_restart` mode.

### 14.3 Reload request correlation

Extend `server.ReloadRequest`:

```go
type ReloadRequest struct {
    ID        string
    Source    ReloadSource
    Candidate *config.Candidate
    RawDigest [32]byte
    Deadline  time.Time
    Result    chan<- ReloadResult
}
```

Requirements:

- IDs are generated internally using a monotonic/random or cryptographically random scheme.
- Result channels are buffered with capacity one.
- Sending a result must never block the server.
- HTTP client disconnect does not cancel a transaction after file persistence; the coordinator uses the process context plus internal deadline.
- Server reload processing remains serialized.

## 15. Deadline and point-of-no-return semantics

### 15.1 Context ownership

For each reload request, `server.Run` derives an internal context from:

- the process/server context; and
- the request deadline based on the currently serving configuration’s `reload_timeout`. A candidate that changes `reload_timeout` affects the next transaction, never the transaction applying that change.

Do not use the browser request context as the transaction context.

### 15.2 Phase checks

`ReloadPlan` receives `context.Context`. Every pre-publish phase must:

- check `ctx.Err()` before starting;
- pass the context to operations that can block;
- check after non-preemptible third-party calls;
- abort staged resources on cancellation;
- return its exact phase name and duration.

### 15.3 Point of no return

`ReloadPlan.Publish` remains the point of no return.

- Before Publish: cancellation or failure calls `Abort`; no listener activation, config pointer swap, resource commit, or runtime snapshot update occurs.
- After Publish: complete minimum-safe listener activation, runtime-state finalization, and generation retirement scheduling. Report timeout/degradation; never reconstruct or roll back committed shared state.

### 15.4 Context-aware construction

Modify signatures only where needed:

- `HandlerFactory.Build(ctx, cfg, commit)`;
- `HandlerFactory.Prepare(ctx, cfg)`;
- plugin-set build;
- gRPC reflection/transcoder construction;
- service-discovery initial resolve;
- stream preflight and reload;
- listener staging loops;
- file/TLS preflight loops;
- any outbound startup probe added for staged-restart validation.

When a third-party constructor cannot be interrupted, check context immediately before and after and ensure any returned resource is closed on timeout.

## 16. Managed configuration coordinator

Create `internal/app/config_apply.go` and move managed-write orchestration out of anonymous closures in `serve.go`.

### 16.1 Coordinator responsibilities

```go
type ConfigApplyCoordinator struct {
    BaseCtx          context.Context
    Path             string
    Preflight        *Preflight
    SubmitReload     func(server.ReloadRequest) error
    LiveSnapshot     func() server.LiveSnapshot
    WatchDigest      *atomic.Pointer[[32]byte]
    PlannedRestart   *PlannedRestartStore
    mu               sync.Mutex
}

func (c *ConfigApplyCoordinator) ApplyRaw(data []byte, mode ApplyMode) (ApplyResult, error)
func (c *ConfigApplyCoordinator) ApplyConfig(cfg *config.Config, mode ApplyMode) (ApplyResult, error)
func (c *ConfigApplyCoordinator) DiscardPlannedRestart() (ApplyResult, error)
```

The coordinator owns:

- serialization of every managed config write;
- exact previous raw bytes;
- preflight mode selection;
- atomic write;
- watcher suppression;
- correlated hot reload submission and wait;
- exact restoration on pre-Publish failure;
- planned-restart sidecar/backup state;
- version calculation;
- no UI, audit, or history rendering policy.

### 16.2 Hot apply flow

1. Acquire coordinator mutex.
2. Read current raw disk bytes.
3. Refuse `hot` mode when a managed planned restart is pending; return `pending_restart_exists` and no write.
4. Parse candidate.
5. Run hot preflight and obtain an immutable candidate plus lifecycle classification.
6. If any changed field requires restart, return restart-required with `can_stage=true`; do not write.
7. Compute raw and canonical versions.
8. Create transaction ID, buffered result channel, and internal deadline.
9. Atomically write candidate bytes.
10. Register one-shot watcher suppression for the candidate digest.
11. Submit the typed reload request.
12. Wait for the correlated result or process shutdown.
13. On submit failure or pre-Publish failure/timeout, atomically restore the previous exact bytes and suppress the restoration echo.
14. On post-Publish degradation/timeout, retain the candidate file.
15. Return the decorated result with exact persisted and serving truth.

### 16.3 External source behavior

SIGHUP and file-watch do not own the file and never restore it. On failure:

- previous runtime remains serving;
- disk and runtime may differ;
- latest reload and runtime overview expose desired/serving versions and phase error;
- the Console offers retry or file repair, not an automatic rewrite.

## 17. Planned-restart configuration workflow

### 17.1 Core behavior

`stage_restart` is an explicit managed apply mode.

It:

- validates the complete candidate for next startup;
- creates a new managed pending state only when the candidate cannot be fully applied hot; once a pending state exists, later staged updates may include any valid changes because the whole file is already future-state configuration;
- writes the complete candidate to the active config path;
- suppresses the resulting watcher event;
- does not submit a live reload;
- records exact rollback metadata;
- leaves the current runtime unchanged;
- exposes pending subsystems and desired/serving versions.

A candidate with mixed hot and restart-bound changes is staged as one unit. Jul.IA does not hot-apply a subset.

### 17.2 Pending-state rules

While a planned restart is pending:

- `hot` apply returns HTTP 409 with `pending_restart_exists=true`;
- `stage_restart` may update the staged file and keeps the original rollback base;
- a new managed stage is refused while an unmanaged/external pending restart already exists, unless a future explicit adoption workflow is designed; the operator must first restart, restore, or otherwise reconcile the external file;
- structured patch preview operates against the staged disk configuration;
- apply buttons clearly say `Update staged configuration`;
- generic rollback is blocked unless explicitly run in staged mode;
- the operator may restart the external process supervisor or discard the staged config;
- Jul.IA does not attempt to restart itself.

After a successful process restart with the staged config, startup reconciliation clears the managed pending marker and backup.

### 17.3 Planned restart store

Create `internal/app/planned_restart.go`.

Use files adjacent to the active config so staged admin/history-path edits cannot move the recovery metadata:

- `<config-path>.pending-restart.json`
- `<config-path>.pending-restart.bak`

Both use `0600` and atomic replacement.

Suggested marker:

```go
type PlannedRestartMarker struct {
    Version               int       `json:"version"`
    State                 string    `json:"state"` // prepared | staged
    ConfigPath            string    `json:"config_path"`
    BaseRawSHA256         string    `json:"base_raw_sha256"`
    BaseCanonicalVersion  string    `json:"base_canonical_version"`
    BaseServingVersion    string    `json:"base_serving_version"`
    StagedRawSHA256       string    `json:"staged_raw_sha256"`
    StagedVersion         string    `json:"staged_version"`
    PendingSubsystems     []string  `json:"pending_subsystems"`
    StagedAt              time.Time `json:"staged_at"`
}
```

Never store resolved secrets in the marker. The backup contains the exact previous raw config, which may include secret references but not resolved values.

### 17.4 Crash-consistent staging order

For the first stage:

1. Validate candidate fully.
2. Atomically write the exact previous bytes to `.bak`.
3. Atomically write marker state `prepared` with base/candidate digests.
4. Atomically write candidate to the active config path.
5. Register watcher suppression before the rename becomes visible where practical.
6. Atomically update marker state to `staged`.

For staged updates:

- keep the original `.bak` and base fields;
- atomically write marker state `prepared` with the new candidate digest/version;
- register watcher suppression;
- atomically write the new candidate;
- update marker state to `staged` with the final subsystem set.

Reconciliation rules:

- marker `prepared` + disk equals base digest: remove marker and backup;
- marker `prepared` + disk equals staged digest: promote marker to `staged`;
- marker `staged` + successful startup disk equals staged digest: remove marker and backup;
- any other combination: mark status inconsistent, preserve backup, refuse automatic discard, and provide recovery instructions.

### 17.5 Discard flow

`DiscardPlannedRestart` must:

1. Acquire coordinator mutex.
2. Load marker and backup.
3. Verify marker is managed and consistent.
4. Verify current disk digest equals marker staged digest.
5. Verify current live serving version still equals marker base serving version.
6. Atomically restore backup bytes.
7. Suppress the restoration watcher echo once.
8. Remove marker and backup only after restore succeeds.
9. Return no-reload success because runtime was already serving the restored base.

If any verification fails, return 409 and leave all files untouched.

### 17.6 External pending restart

An external file edit may create a restart-required disk/runtime difference without a managed marker.

The API must report:

- `managed=false`;
- pending subsystems;
- staged/disk and serving versions;
- `discard_available=false`;
- instruction to fix the file, use history, or restart.

Do not invent rollback bytes for an externally owned edit.

### 17.7 Planned-restart preflight mode

Refactor `internal/app/preflight.go`:

```go
type PreflightMode int
const (
    PreflightHot PreflightMode = iota
    PreflightStageRestart
)

type PreflightResult struct {
    Candidate  *config.Candidate
    Lifecycle  lifecycle.ChangeSet
}

func (p *Preflight) Apply(ctx context.Context, c, prev *config.Config, mode PreflightMode) (*PreflightResult, error)
```

Shared gates for both modes:

- candidate resolve;
- structural/runtime validation;
- TLS/certificate-file validation;
- complete handler dry-run;
- stream route dry-run;
- build-tag checks;
- secret/redaction safety.

Hot-only gates:

- new-listener bind probes;
- listener-rebind checks;
- restart-required rejection.

Stage-restart gates:

- classify every lifecycle difference rather than reject it;
- probe only newly introduced addresses not currently held by the process;
- validate same-address listener settings without trying to bind the occupied socket;
- run startup-resource preflights for cache, egress, admin directories/settings, tracing configuration, access-log sinks, metrics options, ACME config/cache path, and other startup consumers;
- return a warning that external conditions can still change before restart.

Add side-effect-minimized preflight helpers where current constructors open long-lived resources:

- `cache.Preflight(config.CacheConfig) error`;
- `observability.PreflightAccessSinks(config.AccessLogConfig) error`;
- `observability.ValidateTracerConfig(config.TracingConfig) error`;
- `admin.PreflightConfig(config.AdminConfig) error`;
- `server.PreflightACMEStartup([]config.ServerConfig) error`;
- `egress.New` remains the compile preflight;
- metrics and lifecycle validation use existing config validation where sufficient.

A preflight may create and remove a temporary file in a target directory to prove writability, but it must not retain handles, start goroutines, contact external services unnecessarily, or mutate live state.

## 18. Server and app file-level changes

### New files

- `internal/server/reload_result.go`
- optional `internal/server/reload_context.go`
- `internal/app/config_apply.go`
- `internal/app/config_apply_test.go`
- `internal/app/planned_restart.go`
- `internal/app/planned_restart_test.go`
- `internal/server/reload_result_test.go`
- startup-preflight files in the owning packages only where required

### Modify `internal/server/server.go`

- extend `ReloadRequest`;
- make `doReload` produce `ReloadResult`;
- derive deadline context;
- store the complete result atomically;
- send the result non-blockingly;
- remove advisory-only post-hoc timeout behavior;
- preserve serialized reload processing;
- expose canonical serving version in `LiveSnapshot` or enough data to derive it without re-resolution.

### Modify `internal/server/reload_plan.go`

- add context and phase timer helper;
- return structured phase failures;
- check cancellation at each boundary;
- track `published` state;
- separate HTTP and stream subsystem results;
- guarantee Abort on every pre-Publish exit;
- guarantee minimum-safe finalization after Publish.

### Modify `internal/app/serve.go`

- construct one `ConfigApplyCoordinator`;
- replace duplicated `WriteConfigRaw` and `SaveConfig` closures;
- provide hot/stage/discard functions to admin dependencies;
- construct `PlannedRestartStore` from the config path;
- reconcile pending marker after successful startup/readiness;
- preserve one-shot watcher suppression and typed reload merging;
- expose planned-restart status to runtime overview.

### Modify `internal/app/wiring.go`

- preserve request IDs, deadlines, and result channels through merge logic;
- keep admin requests durable and ordered;
- retain one-shot file-watch suppression;
- never coalesce away a candidate-bearing admin request.

### Modify `internal/app/preflight.go`

- add context and mode;
- return lifecycle classification;
- split hot applicability from startup-stage validation;
- call the startup preflight helpers.

### Modify `internal/app/factory.go`

- add context parameters;
- pass context into plugin, auth, WAF, discovery, gRPC/transcode builders;
- ensure all staged resources close on cancellation.

### Modify stream files

- `internal/stream/server.go`
- `internal/stream/stub.go`
- related tests

Add context-aware preflight/reload while retaining bind-before-commit behavior.

### Modify builder packages only as required

- `internal/plugins/*`
- `internal/transcode/*`
- `internal/auth/*`
- `internal/upstream/*`
- `internal/waf/*`
- `internal/cache/*`
- `internal/observability/*`
- `internal/server/acme*`

Avoid unrelated refactors.

## 19. Admin API changes

### 19.1 Dependency contract

Modify `internal/admin/server.go` `Deps` to expose explicit operations rather than generic write closures:

```go
ApplyConfigRaw func([]byte, string) (ConfigApplyResult, error) // mode hot|stage_restart
ApplyConfig    func(*config.Config, string) (ConfigApplyResult, error)
DiscardPendingRestart func() (ConfigApplyResult, error)
PendingRestart func() *PendingRestartStatus
LastReload     func() *server.ReloadResult
```

Use typed mode internally; strings are shown only to illustrate the JSON/API boundary.

Deprecate after migration:

- `WriteConfigRaw func([]byte) error`;
- `SaveConfig func(*config.Config) error`;
- admin-local duplicate `ReloadSnapshot`.

### 19.2 Apply endpoints

Keep body formats compatible and add the mode as a query parameter:

- `POST /api/config/apply?mode=hot` — default when omitted;
- `POST /api/config/apply?mode=stage_restart`;
- `POST /api/config/patch/apply?mode=hot|stage_restart`;
- rollback endpoints accept the same mode only where safe; while pending, default hot rollback is rejected and the dedicated discard flow is preferred.

Hot success:

```json
{
  "ok": true,
  "mode": "hot",
  "version": "candidate-version",
  "serving_version": "candidate-version",
  "reload": {
    "id": "rl_...",
    "source": "admin",
    "outcome": "applied_live",
    "persisted": true,
    "published": true,
    "timed_out": false,
    "duration_ms": 148,
    "http": {"status":"ok","duration_ms":92},
    "stream": {"status":"ok","duration_ms":21}
  }
}
```

Restart-required hot rejection:

```json
{
  "ok": false,
  "restart_required": true,
  "can_stage": true,
  "pending_subsystems": ["cache"],
  "message": "This valid change cannot be applied live. Save it for the next restart?"
}
```

Stage success:

```json
{
  "ok": true,
  "mode": "stage_restart",
  "version": "staged-version",
  "serving_version": "current-live-version",
  "message": "Configuration validated and saved for the next process restart. The current runtime is unchanged.",
  "pending_restart": {
    "managed": true,
    "staged": true,
    "staged_at": "2026-07-19T12:00:00Z",
    "staged_version": "staged-version",
    "serving_version": "current-live-version",
    "subsystems": ["cache", "access_log"],
    "discard_available": true,
    "inconsistent": false
  }
}
```

### 19.3 Pending-restart endpoints

Add:

- `GET /api/config/pending-restart`
- `POST /api/config/pending-restart/discard`

Do not add a separate revalidate endpoint in this programme. `stage_restart` updates rerun complete validation, startup preflight, and lifecycle classification; GET status is read-only and discard is the only recovery mutation. A future explicit revalidation endpoint should be added only if a concrete operator workflow cannot be served by updating the staged candidate.

The GET endpoint returns an empty/`pending=false` status rather than 404 when no pending state exists.

### 19.4 HTTP status rules

- 200: hot transaction completed, stage completed, or discard completed.
- 400/422: parse or validation rejection; nothing written.
- 409:
  - optimistic conflict;
  - admin reachability confirmation required;
  - hot candidate requires restart and may be staged;
  - a managed pending restart blocks hot apply;
  - discard verification failed.
- 503: reload queue unavailable and previous file restored.
- 500: unexpected persistence/coordinator failure with truthful persisted state.

### 19.5 Runtime overview

Add:

- latest full reload result;
- planned-restart status;
- desired/staged and serving versions;
- pending subsystems;
- managed/external and discard availability.

Keep the existing simple pending subsystem list as a deprecated compatibility field through the next MINOR release after the structured object ships. The Console must consume the structured object immediately; remove the compatibility field only under the documented deprecation policy.

### Files

**Modify**

- `internal/admin/server.go`
- `internal/admin/api.go`
- `internal/admin/patch_http.go`
- `internal/admin/api_history.go`
- `internal/admin/projection_types.go`
- `internal/admin/routes.go`
- `internal/admin/events.go`
- `internal/admin/timeline.go`
- `internal/admin/operational_test.go`

**New recommended**

- `internal/admin/api_reload.go`
- `internal/admin/api_pending_restart.go`
- do not add a second reload-history store; use the latest result plus the existing timeline. Revisit only if a concrete retention/query requirement appears

## 20. History, audit, metrics, and logs

### 20.1 History

- Hot apply snapshots previous raw bytes only after the transaction’s persistence policy is known.
- A restored pre-Publish failure must not appear as a successful applied history entry.
- First `stage_restart` snapshots the exact running/disk base through existing history and the dedicated `.bak` recovery file.
- Updating staged config may snapshot the previous staged version for ordinary history, but must not replace the original discard base.
- Discard records the staged file in history before restoring the base, making discard itself reversible through expert history tools.

### 20.2 Audit and timeline

Use distinct events:

- `config.apply.accepted`
- `config.apply.live`
- `config.apply.restored`
- `config.apply.degraded`
- `config.apply.timed_out`
- `config.stage_restart.created`
- `config.stage_restart.updated`
- `config.stage_restart.discarded`
- `config.stage_restart.inconsistent`

Include transaction ID or staged version in metadata/detail, never raw config or secrets.

### 20.3 Metrics

Add or extend:

- `jul_reload_total{source,outcome}`
- `jul_reload_duration_seconds{source,outcome}`
- `jul_reload_phase_duration_seconds{phase}`
- `jul_reload_timeout_total{phase,source}`
- `jul_reload_in_progress`
- `jul_config_stage_restart_total{result}`
- `jul_config_pending_restart` gauge, 0/1

No config path, transaction ID, subsystem list, or version in labels.

### 20.4 Logs

Every reload log includes ID, source, desired version, phase, outcome, and duration. Planned-restart logs state explicitly that no live reload was attempted.

## 21. Console changes

### 21.1 API client

Modify `internal/admin/ui/src/api/client.ts`:

- add schemas for `ReloadSubsystemResult`, `ReloadResult`, and structured `PendingRestartStatus`;
- add `ApplyMode = "hot" | "stage_restart"`;
- add mode to raw and patch apply methods;
- add `fetchPendingRestart()` and `discardPendingRestart()`;
- replace `previous_reload` consumption with the correlated `reload` field;
- keep additive fallback parsing for one compatibility release.

### 21.2 Outcome model

Modify `internal/admin/ui/src/lib/applyOutcome.ts` to map direct server state:

- applied live;
- applied degraded;
- not applied/restored;
- timeout before Publish;
- timeout after Publish;
- restart required with stage option;
- staged for restart;
- staged update;
- pending restart blocks hot apply;
- discard success/failure;
- externally managed disk/runtime divergence;
- optimistic conflict.

### 21.3 Config panel behavior

Modify `ConfigPanel.tsx` and shared result components:

- default action is `Apply live`;
- on restart-required response, show `Save for next restart` with exact subsystems;
- staging always requires explicit confirmation that the current runtime will not change;
- when pending exists, show `Update staged configuration`, `Discard staged configuration`, and external restart instructions;
- block normal hot apply while pending;
- show staged and serving versions;
- show marker inconsistency as a blocking recovery state;
- never claim Jul can restart its own process.

### 21.4 Persistent banner

Add a shared banner visible from Overview and Config:

- `Restart required — configuration staged`;
- managed/external distinction;
- pending subsystems;
- staged time/version;
- current serving version;
- discard action only when safe and available.

### 21.5 Operations/timeline

Show exact reload and stage events with phase/result, without forcing users to inspect raw logs.

## 22. Phase 2 tests

### 22.1 Reload unit tests

- result-state table for every outcome;
- timeout before Prepare publishes nothing;
- timeout during listener staging closes every staged socket;
- timeout immediately before Publish aborts;
- timeout after Publish finalizes safely and reports degradation;
- result channel cannot block the server;
- LastReload equals the requester result;
- request source, ID, and versions remain correlated;
- repeated cancellation has no goroutine, file-descriptor, pool, plugin, or redaction leak.

### 22.2 Hot coordinator tests

- preflight rejection writes nothing;
- enqueue failure restores exact bytes including comments;
- pre-Publish failure/timeout restores exact bytes;
- post-Publish degradation retains candidate file;
- write and restore watcher echoes are consumed once;
- concurrent applies serialize;
- server shutdown unblocks a waiter;
- `Persisted`, `Published`, desired version, and serving version are correct.

### 22.3 Planned-restart store tests

- first stage creates backup, marker, and candidate atomically;
- update stage preserves original backup/base fields;
- no live reload request is sent;
- hot apply is blocked while pending;
- discard restores exact bytes and removes sidecars;
- discard refuses when disk digest changed;
- discard refuses when live serving version changed;
- crash reconciliation handles `prepared` with base disk;
- crash reconciliation handles `prepared` with staged disk;
- successful startup clears a matching staged marker;
- inconsistent marker preserves backup and surfaces recovery state;
- file modes are 0600;
- marker never includes raw or resolved secret values.

### 22.4 Stage preflight tests

- restart-required cache/log/admin/tracing/egress changes can be classified and staged;
- invalid startup resource fails before write;
- existing occupied listener address is not incorrectly probed;
- genuinely new occupied address is rejected;
- handler/WAF/plugin/stream dry-run still runs;
- mixed hot+restart candidate returns the full pending subsystem set;
- a first managed stage is refused while an unmanaged external pending state exists;
- lifecycle classification matches `docs/config-lifecycle.yaml`.

### 22.5 API integration tests

- raw hot apply returns its own reload result;
- patch hot apply returns the same contract;
- restart-required hot apply returns `can_stage=true` and writes nothing;
- stage writes candidate, leaves data plane unchanged, and exposes pending status;
- staged update changes disk but not runtime;
- discard returns to exact base and keeps runtime unchanged;
- external restart-required edit reports `managed=false`;
- queue saturation returns 503 and restores file;
- history and audit events match outcome.

### 22.6 Race/leak tests

- concurrent status reads during reload/stage/discard;
- concurrent hot and stage requests;
- stage/discard versus watcher event;
- cancellation while generation retires;
- repeated timeouts under `-race` and existing leak tooling.

### 22.7 Console tests

- schemas parse every response shape;
- stage option appears only when allowed;
- pending banner states and actions;
- no hot apply while pending;
- managed versus external pending copy;
- discard confirmation and failures;
- timeout before/after Publish copy;
- no stale prior-reload warning attached to a new transaction.

### 22.8 Real-server E2E

Extend the existing real-server suite to exercise:

1. successful hot apply and live verification;
2. controlled pre-Publish failure with exact file restoration;
3. restart-required candidate rejected in hot mode;
4. same candidate staged for restart;
5. data plane remains on old behavior;
6. pending banner survives page reload;
7. staged config update;
8. discard restores exact source bytes;
9. simulated process restart adopts staged config and clears marker.

## 23. Phase 2 documentation

Modify only the authoritative pages needed:

- `docs/reload-semantics.md` — hot versus staged transaction model, point of no return, restoration, external ownership;
- `docs/configuration.md` — `reload_timeout`, apply modes, pending restart;
- `docs/console.md` — user workflow and exact states;
- `docs/troubleshooting.md` — inconsistent marker, failed restart, external divergence;
- `docs/config-lifecycle.yaml` — unchanged classifications but cross-link staging semantics;
- `docs/specs/hardening-platform.md` — mark HP-01 and planned restart scope;
- `docs/roadmap/README.md` — status update;
- `docs/status.md`/`feature-status.yaml` only if maturity evidence changes;
- canonical benchmark documentation — reload latency budget;
- `CHANGELOG.md`.

Do not create a second standalone planned-restart design document unless `reload-semantics.md` becomes unmanageably large. The implementation details remain in this handoff and code comments; user-facing semantics belong in the existing reload/config/Console docs.

## 24. Phase 2 change sequence and Definition of Done

### Change sequence

1. **P2-CS1:** Reload result types, correlation, phase timing, LastReload migration.
2. **P2-CS2:** Context propagation through ReloadPlan, factory, stream, and builders.
3. **P2-CS3:** Managed hot-apply coordinator and exact restoration.
4. **P2-CS4:** PlannedRestartStore, stage preflight mode, reconciliation, and discard.
5. **P2-CS5:** Admin API result/mode/pending endpoints and runtime overview.
6. **P2-CS6:** Console outcome, stage/discard workflows, and persistent banner.
7. **P2-CS7:** Metrics, audit/timeline, E2E, race/leak coverage, docs, and compatibility cleanup.

### Definition of Done

- Managed hot apply returns the result for its exact candidate.
- Reload work is bounded at phase boundaries.
- Pre-Publish failure leaves runtime and disk on the exact previous version.
- Post-Publish timeout never attempts unsafe rollback.
- Restart-required hot apply writes nothing and offers an explicit stage action.
- `stage_restart` validates and writes the whole candidate without live reload.
- Mixed lifecycle changes are never partially applied.
- Pending state blocks hot apply until restart, staged update, or safe discard.
- Managed discard restores exact bytes and refuses unsafe restoration.
- Successful restart clears matching managed pending state.
- API, Console, metrics, logs, history, timeline, and audit agree.
- Old `previous_reload` behavior is compatibility-only and scheduled for removal.
- HP-01 and the planned-restart foundation are marked delivered.

---
# Phase 3 — Console/admin RBAC Phase 1

## 25. Phase 3 objective

Replace the all-or-nothing shared admin identity with named principals, explicit permissions, predefined and custom roles, hot-reloadable policy, and attributable audit events—while preserving backward compatibility when RBAC is disabled.

**Estimated effort:** L
**Indicative execution:** 6–10 focused weeks
**Dependencies:** Phase 2 stable hot/stage/result contract
**Implementation focus:** security/control plane, admin API, config/lifecycle, Console, and tests

## 26. Existing implementation to retain

Retain:

- loopback-by-default admin listener;
- bearer token transport in the `Authorization` header;
- constant-time secret comparison principles;
- sessionStorage token handling in the Console;
- admin self-lockout guard;
- admin API rate limiting;
- audit ring and durable sink;
- raw/structured config apply pipeline;
- 401/403 frontend taxonomy;
- `docs/specs/console-rbac.md` permission design as the primary functional source.

Replace or extend:

- single `AdminConfig.Token` as the only identity;
- one `auth` middleware applied uniformly;
- hard-coded audit actor `operator`;
- absence of route-level permission declarations;
- lack of current identity/role projection;
- inability to revoke a named token through config reload.

## 27. Phase 3 target scope

### Included

- RBAC opt-in under `[admin.rbac]`.
- Named principals.
- Predefined roles: `viewer`, `operator`, `admin`, and `auditor`.
- Config-defined custom roles.
- One high-entropy config-defined token per principal in this phase. Multi-token rotation and API-minted tokens remain additive follow-ups.
- Token hashes held in memory; plaintext never persisted by Jul.IA beyond the operator-owned config/secret source.
- Stable public token ID derived without revealing the token.
- Token disable/expiry support.
- Immediate policy update after a successful config apply.
- Route-level permissions and deny-by-default.
- 401 versus 403 machine-readable errors.
- Principal and token ID in audit records.
- Console identity display and permission-aware controls.
- Legacy shared-token migration.

### Excluded

- OIDC/SAML/SSO/SCIM.
- Web UI/API token minting and plaintext-once issuance.
- External database.
- Fleet-wide roles.
- per-object selectors such as `config:apply@listen=:8443`.
- two-person approval queue.

## 28. Permission model

Create package `internal/rbac`.

### 28.1 Permission constants

Create `internal/rbac/permission.go`:

```go
type Permission string

const (
    StatusRead         Permission = "status:read"
    MetricsRead        Permission = "metrics:read"
    ConfigRead         Permission = "config:read"
    ConfigWrite        Permission = "config:write"
    ConfigApply        Permission = "config:apply"
    HistoryRead        Permission = "history:read"
    HistoryRollback    Permission = "history:rollback"
    PluginsUpload      Permission = "plugins:upload"
    ObservabilityRead  Permission = "observability:read"
    AuditRead          Permission = "audit:read"
    AuditExport        Permission = "audit:export"
    CachePurge         Permission = "cache:purge"
    ReloadTrigger      Permission = "reload:trigger"
    AdminManage        Permission = "admin:manage"
)
```

Requirements:

- catalog is the single source for validation and Console display;
- support wildcard `*` and `<resource>:*` for custom roles;
- unknown permissions fail config validation;
- no permission is inferred from endpoint name.

### 28.2 Predefined roles

Create `internal/rbac/role.go`:

- `viewer`: read-only status, metrics, config, history, observability, audit;
- `operator`: viewer plus config write/apply, rollback, cache purge, reload, plugin upload, audit export;
- `admin`: `*`;
- `auditor`: status/observability/audit read/export only.

Role definitions MUST be tested as stable contracts.

### 28.3 Principal and identity model

Create `internal/rbac/identity.go`:

```go
type Identity struct {
    Principal string
    Role      string
    TokenID   string
    Permissions []Permission
    Legacy    bool
}
```

Store identity in request context through unexported context keys and expose safe helpers:

- `IdentityFromContext(ctx)`
- `MustIdentity` only in tests/internal code where absence is impossible.

### 28.4 Token model

Create `internal/rbac/token.go`:

- hash token using SHA-256 because tokens are high entropy;
- compare hashes with `subtle.ConstantTimeCompare`;
- derive `TokenID` from a non-secret prefix of the hash, for example 12 hex characters;
- never use the raw token as a map key or log attribute;
- normalize only the `Bearer ` transport prefix, not token bytes;
- reject empty/short literal tokens according to config validation and lint policy;
- honor `disabled` and `expires_at`.

## 29. Configuration schema and lifecycle

### 29.1 Schema additions

Modify `internal/config/schema.go`.

Recommended structures:

```go
type AdminConfig struct {
    // existing fields...
    RBAC AdminRBACConfig `toml:"rbac"`
}

type AdminRBACConfig struct {
    Enabled     bool             `toml:"enabled"`
    DefaultRole string           `toml:"default_role"`
    Roles       []AdminRole      `toml:"roles"`
    Principals  []AdminPrincipal `toml:"principals"`
}

type AdminRole struct {
    Name        string   `toml:"name"`
    Permissions []string `toml:"permissions"`
}

type AdminPrincipal struct {
    Name      string    `toml:"name"`
    Role      string    `toml:"role"`
    Disabled  bool      `toml:"disabled"`
    Token     string    `toml:"token"`
    ExpiresAt time.Time `toml:"expires_at"`
}
```

Phase 3 exposes exactly one `token` per principal. `disabled` and `expires_at` apply to that credential. A later additive phase may introduce `[[admin.rbac.principals.tokens]]` for rotation overlap; do not ship both shapes now.

### 29.2 Defaults

Modify `internal/config/parser.go`:

- `default_role = "admin"` for the legacy token compatibility principal;
- RBAC disabled by default;
- no implicit named principal;
- expiry timestamps remain zero when absent.

### 29.3 Validation

Modify or split `internal/config/validate.go` into a dedicated helper such as `internal/config/validate_rbac.go`.

Validate:

- unique principal names;
- unique custom role names;
- predefined role names cannot be redefined;
- roles reference known permissions;
- principals reference existing predefined/custom roles;
- token values are non-empty after secret resolution;
- duplicate token IDs/hashes rejected;
- expiry parses and is sensible;
- RBAC enabled requires at least one enabled admin-capable principal or compatible legacy token;
- the last admin cannot be removed by a candidate policy update;
- principal and role names are bounded and safe for audit display;
- legacy token compatibility mode is explicit.

### 29.4 Secret handling and lint

Modify the secret/lint paths so nested principal tokens:

- accept `${env:}`, `${file:}`, `${secret:}`;
- join the existing redaction set;
- are never returned by projections/diff;
- trigger `jul lint` warnings when literal values are used;
- are included in effective-value lifecycle digests without exposing contents.

Likely files:

- `internal/config/candidate.go`
- `internal/config/clone.go` or equivalent deep-copy helpers
- `internal/config/lint.go` / `cmd/jul` lint files
- related config tests

### 29.5 Lifecycle classification

Modify:

- `internal/lifecycle/lifecycle.go`
- `internal/lifecycle/fingerprint.go`
- `docs/config-lifecycle.yaml`

Recommended classes:

- `admin.listen`, `admin.tls`, `admin.token`, Console listener/process settings: restart-required, unchanged.
- `admin.rbac.enabled`, roles, principals, token disable/expiry: hot-reloadable through policy swap.
- policy update must be built/validated before config Publish and installed atomically only after successful Publish.
- `stage_restart` never installs a candidate RBAC policy into the current process; it changes only the staged disk configuration.
- while a planned restart is pending, RBAC edits update the staged policy only and become active after restart.


## 30. Policy construction and hot swap

### New files

- `internal/rbac/policy.go`
- `internal/rbac/policy_test.go`
- `internal/rbac/token.go`
- `internal/rbac/token_test.go`
- `internal/rbac/role.go`
- `internal/rbac/role_test.go`
- `internal/rbac/context.go`

### Policy API

```go
type Policy struct {
    // immutable maps after construction
}

func Build(admin config.AdminConfig, now time.Time) (*Policy, error)
func (p *Policy) Authenticate(bearer string, now time.Time) (Identity, error)
func (p *Policy) Authorize(id Identity, permission Permission) bool
```

Requirements:

- immutable after Build;
- O(1) token ID lookup then constant-time hash comparison;
- no raw tokens retained after policy construction where avoidable;
- legacy token represented as synthetic principal `shared` with `default_role`;
- policy contains the permission catalog version for diagnostics.

### Admin server storage

Modify `internal/admin/server.go`:

- add `policy atomic.Pointer[rbac.Policy]`;
- build initial policy in `admin.New` and fail startup on invalid policy;
- add `UpdatePolicy(*rbac.Policy) error` or `PreparePolicy/InstallPolicy` methods;
- never partially mutate a live policy.

### Reload integration

Modify `internal/app/serve.go` and reload post-publish wiring:

- preflight builds candidate RBAC policy and rejects invalid policy before persistence;
- the candidate policy is carried with the prepared reload transaction or rebuilt from the exact candidate once;
- after config Publish, atomically install the new policy;
- if policy installation somehow fails, the transaction must report admin subsystem degradation and retain the previous policy; this should be prevented by prebuild.

To avoid re-resolving secrets, extend `config.Candidate` or the app-level prepared state with the built policy, rather than reading token sources again.

## 31. Route catalog and authorization boundary

### 31.1 Replace ad hoc route wrapping

Create `internal/admin/route_catalog.go`.

Recommended declaration:

```go
type RouteSpec struct {
    Pattern    string
    Methods    []string
    Permission rbac.Permission
    Public     bool
    Handler    func(*Server) http.Handler
}
```

Or use a registration helper with equivalent static metadata.

### 31.2 Public routes

Recommended public routes:

- `/healthz`
- `/readyz`
- Console shell/static assets, because the SPA needs to load before prompting for a token

Everything else requires authentication. `/metrics` requires `metrics:read` when RBAC or legacy token authentication is configured. When RBAC is disabled, preserve the existing no-token loopback compatibility mode; when RBAC is enabled, anonymous access is denied on every bind address.

### 31.3 Permission mapping

At minimum:

| Route group | Permission |
|---|---|
| Runtime overview/status/stats/certs/TLS/security/apps/routes/streams/plugins projections | `status:read` or `config:read` according to content |
| `/metrics` | `metrics:read` |
| Raw config and projections | `config:read` |
| Validate/diff/patch preview/route test/wizard/descriptor inspection | `config:write` |
| Apply/patch apply/settings save, including `stage_restart` | `config:apply` |
| Pending-restart status | `config:read` |
| Discard managed staged configuration | `config:apply` |
| History list/get | `history:read` |
| Rollback | `history:rollback` |
| Plugin upload | `plugins:upload` |
| Operations logs/events/timeline/history/search | `observability:read` |
| Audit list | `audit:read` |
| Audit export | `audit:export` |
| Cache purge | `cache:purge` |
| Reload trigger | `reload:trigger` |
| pprof and future RBAC management | `admin:manage` |

Resolve borderline assignments in a checked-in route-permission table; do not scatter literals across handlers.

Object-level authorization remains mandatory: a candidate that changes `[admin]` reachability, legacy credentials, RBAC roles/principals, audit sink, or plugin-upload security requires `admin:manage` even when it arrives through the generic config apply or staged-restart endpoint. The route permission is the minimum; candidate inspection may require the stronger permission.

### 31.4 Middleware behavior

Replace `Server.auth` with an authn/authz stack:

1. Parse Bearer header.
2. Authenticate against current immutable policy.
3. Put identity in request context.
4. Authorize required permission.
5. Return:
   - 401 + `WWW-Authenticate: Bearer` for absent/invalid/expired/disabled credentials;
   - 403 JSON for authenticated but unauthorized.

Example 403:

```json
{
  "error": "forbidden",
  "required": "config:apply",
  "principal": "alice",
  "role": "viewer"
}
```

Do not reveal whether another token/principal exists.

### 31.5 Guard test

A test MUST fail if:

- a mounted non-public route has no permission;
- a method is accepted by a handler but not declared in the catalog;
- a new route defaults to public;
- a permission string is not in the catalog;
- planned-restart status/discard routes are missing their declared permissions;
- a generic config apply that changes admin/RBAC settings bypasses the `admin:manage` object-level guard.

## 32. Audit attribution

### Modify `internal/admin/audit.go`

Add:

```go
TokenID string `json:"token_id,omitempty"`
```

Change audit recording to derive identity from the request context:

```go
func (s *Server) recordAudit(r *http.Request, operation, resource, result, detail string)
```

Rules:

- actor is the server-authenticated principal;
- token ID is non-secret and server-derived;
- unauthenticated failures use `anonymous` and may include a safe token ID prefix only if parsing produced one without authenticating;
- source IP remains;
- no client-supplied actor/token ID accepted;
- durable JSONL and CSV schemas add the field additively.

Update every call site in:

- config apply/patch/history handlers;
- cache/reload/plugin upload handlers;
- auth failure middleware;
- future RBAC management endpoints if added later.

### UI/API projection

Modify:

- `internal/admin/ui/src/api/client.ts`
- Audit panel components/tests

Show actor and token ID. Do not expose token labels unless they are explicitly defined as non-secret metadata in the RBAC contract and covered by API/UI tests.

## 33. Current-identity endpoint and Console gating

### New endpoint

Add `GET /api/admin/me` requiring any authenticated identity.

Response:

```json
{
  "principal": "alice",
  "role": "operator",
  "token_id": "9f32a1b4c921",
  "permissions": ["status:read", "config:read", "config:write", "config:apply"],
  "legacy": false
}
```

### Console changes

**New**

- `internal/admin/ui/src/auth/PermissionProvider.tsx`
- `internal/admin/ui/src/auth/usePermission.ts`
- `internal/admin/ui/src/components/ForbiddenAction.tsx` or equivalent

**Modify**

- `internal/admin/ui/src/api/client.ts`
- token prompt/AuthGate
- `internal/admin/ui/src/app/Layout.tsx`
- configuration apply controls
- history rollback controls
- plugin upload controls
- cache purge/reload controls
- audit export controls
- Operations/pprof links if exposed

Behavior:

- display current principal and role;
- hide or disable actions the identity cannot perform;
- show why an action is unavailable;
- handle 403 without triggering a token prompt;
- clear identity cache when token changes or 401 occurs;
- server remains authoritative.

## 34. Admin self-lockout protection

Extend existing lockout guards:

- reject enabling RBAC with no usable admin path;
- reject removing/disabling the last admin-capable principal;
- reject removing the legacy token and last named admin in one change;
- require explicit confirmation for RBAC changes that affect the current caller;
- self-disable, self-token removal, or self-demotion is permitted only when another enabled admin-capable principal remains; the current transaction may complete, subsequent requests must fail, and the UI must require a clear confirmation;
- policy update is atomic at Publish.

Likely files:

- `internal/admin/admin_lockout.go`
- `internal/app/preflight.go`
- config validation helpers
- corresponding tests

## 35. Diff and projections

### Structured diff

Modify `internal/admin/diff_global.go` and diff tests:

Show only:

- RBAC enabled/disabled;
- roles added/removed/permission-count changed;
- principals added/removed/role changed/disabled/expiry changed;
- token count or token ID changes, never token values/hashes.

Warnings:

- enabling RBAC;
- removing last admin;
- changing current principal’s access;
- retaining legacy shared token after RBAC is enabled.

### Projections

Add safe RBAC status to Security/Overview:

- enabled;
- principal count;
- role count;
- legacy token active;
- no secrets or hashes; expose labels or expiry details only when the RBAC API contract explicitly classifies them as non-secret, permission-gates them, and covers them with API/UI tests.

## 36. Phase 3 file-level plan

### New Go files

- `internal/rbac/permission.go`
- `internal/rbac/role.go`
- `internal/rbac/token.go`
- `internal/rbac/policy.go`
- `internal/rbac/context.go`
- `internal/rbac/*_test.go`
- `internal/config/validate_rbac.go`
- `internal/admin/route_catalog.go`
- `internal/admin/api_identity.go`
- optional `internal/admin/rbac_update.go`

### Modify Go files

- `internal/config/schema.go`
- `internal/config/parser.go`
- candidate/deep-copy/secret-resolution files
- config lint files
- `internal/config/config_test.go`
- `internal/lifecycle/lifecycle.go`
- `internal/lifecycle/fingerprint.go`
- `internal/app/preflight.go`
- `internal/app/serve.go`
- `internal/server/reload_plan.go` or prepared-state plumbing
- `internal/admin/server.go`
- `internal/admin/routes.go`
- `internal/admin/audit.go`
- `internal/admin/admin_lockout.go`
- `internal/admin/diff_global.go`
- `internal/admin/projection_types.go`
- `internal/admin/projections.go`
- all handlers that record audits
- `internal/observability/metrics.go` if authz metrics are added

### New/modified frontend files

- `internal/admin/ui/src/auth/PermissionProvider.tsx`
- `internal/admin/ui/src/auth/usePermission.ts`
- `internal/admin/ui/src/api/client.ts`
- `internal/admin/ui/src/app/Layout.tsx`
- token/AuthGate components
- Config, History, Plugins, Operations, Audit, Traffic controls panels
- relevant test fixtures and E2E mocks

### Documentation

**Modify**

- `docs/specs/console-rbac.md`
- `docs/adr/0010-console-rbac.md`
- `docs/console.md`
- `docs/configuration.md`
- `docs/security-posture.md`
- `SECURITY.md`
- `docs/compatibility.md`
- `docs/known-limitations.md`
- `docs/config-lifecycle.yaml`
- `docs/feature-status.yaml`
- `docs/status.md`
- `docs/roadmap/README.md`
- `docs/specs/hardening-platform.md`
- `CHANGELOG.md`

**Migration documentation**

Add a concise, numbered migration section to `docs/console.md`, with the security-critical recovery notes cross-linked from `SECURITY.md`. Do not create a standalone RBAC migration guide by default. Create `docs/guides/admin-rbac-migration.md` only if the tested migration procedure no longer fits clearly in the Console documentation without making that page unwieldy.

## 37. Phase 3 tests

### Config tests

- duplicate role/principal;
- unknown permission/role;
- no admin path;
- disabled/expired tokens;
- literal-secret lint;
- secret-ref expansion/redaction;
- lifecycle hot-reload versus restart-bound fields;
- config round-trip.

### RBAC unit tests

- each predefined role’s exact permissions;
- wildcard/custom-role matching;
- token ID and hash lookup across principals;
- constant-time compare path;
- expiry/disabled behavior;
- legacy synthetic principal;
- duplicate token hash/ID rejection;
- immutable policy concurrency.

### Route matrix tests

Generate a matrix from the route catalog:

- unauthenticated → 401;
- viewer/operator/admin/auditor allow/deny per route/method;
- all mounted routes classified;
- public routes limited to the approved list;
- 403 body contains required permission but no sensitive data.

### Audit tests

- actor/token ID attribution;
- anonymous auth failures;
- CSV/JSONL additive schema;
- secret redaction;
- policy change attribution.

### Hot-reload tests

- add principal and authenticate without process restart;
- disable token and observe immediate rejection after apply;
- rejected candidate leaves old policy active;
- policy swap concurrent with requests under `-race`;
- current caller self-revocation warning/behavior;
- last-admin removal rejected.

### Console tests

- current identity shown;
- viewer sees read panels and cannot apply/purge/upload/rollback;
- operator cannot administer admin settings;
- 403 does not show re-auth prompt;
- token change refreshes identity;
- hidden/disabled controls match permissions;
- direct API bypass still rejected server-side.

### E2E

Real-server scenarios for viewer/operator/admin and legacy token compatibility.

## 38. Phase 3 rollout and migration

### Compatibility modes

1. RBAC disabled: current behavior.
2. RBAC enabled + legacy token present: legacy token maps to `shared` with `default_role`, startup warning emitted.
3. RBAC enabled + named principals only: all API access requires named credentials.

### Migration guide

1. Enable RBAC while retaining legacy token.
2. Add an admin principal and an operator principal.
3. Move automation to scoped credentials.
4. Verify audit attribution.
5. Remove legacy token.
6. Test recovery procedure and keep an out-of-band config rollback path.

### Release communication

- additive minor release;
- no default behavior change;
- explicit deprecation note for long-term shared-token use, but no removal in current major;
- security advisory-style migration checklist.

## 39. Phase 3 change sequence

1. **P3-CS1:** Config schema, validation, lint, lifecycle, and RBAC core package.
2. **P3-CS2:** Route catalog and server-side authn/authz, with generated matrix tests.
3. **P3-CS3:** Hot policy preparation/install and self-lockout protection.
4. **P3-CS4:** Audit attribution and safe diff/projections.
5. **P3-CS5:** `/api/admin/me` and Console permission provider/gating.
6. **P3-CS6:** Migration docs, E2E, race, compatibility, and final status updates.

## 40. Phase 3 Definition of Done

- Every non-public admin route has an explicit permission.
- No token grants more access than its role.
- Named actions are attributable in audit.
- Invalid/disabled/expired token returns 401; insufficient permission returns 403.
- Viewer cannot mutate; operator cannot manage admin/RBAC; admin can.
- Legacy mode remains compatible.
- Named-policy changes take effect atomically after successful config Publish.
- Last-admin lockout is impossible through the supported apply path.
- Console gating matches server behavior.
- HP-02 is marked Delivered for local-token RBAC; external identity remains Y3-02 horizon.

---

# Phase 4 — Egress policy completion and integration hardening

## 41. Phase 4 objective

Complete HP-07 by applying the existing outbound policy consistently to every supported config-driven network client, strengthening normalization and diagnostics, and making policy behavior visible and testable.

**Estimated effort:** M, reduced because the core exists
**Indicative execution:** 3–5 focused weeks
**Dependencies:** Phase 2 planned-restart/result contract and Phase 3 stable admin authorization contract
**Implementation focus:** security/networking, auth/discovery, ACME, plugins, observability, and tests

## 42. Existing implementation to retain

Retain:

- top-level `[egress]` config with `enabled` and `allow`;
- exact hostname, suffix hostname, IP, and CIDR syntax;
- nil/disabled zero-overhead policy;
- dial-time enforcement;
- validation of every resolved IP for CIDR-only hostname authorization;
- direct dialing of validated IP while retaining Host/SNI;
- current auth and service-discovery wiring;
- startup-bound lifecycle classification; changes are applied through Phase 2 `stage_restart` unless a later dedicated hot-reload design is implemented.

Complete or improve:

- ACME and OCSP coverage;
- WASM plugin fetch intersection;
- subsystem-specific diagnostics and metrics;
- hostname normalization;
- redirect/proxy behavior tests;
- typed errors;
- Console visibility;
- documented trust semantics;
- future-client integration contract.

## 43. Outbound-client inventory and required coverage

Create a checked-in inventory in `docs/egress.md` and a test-enforced registration table where practical.

| Subsystem | Current state | Phase 4 target |
|---|---|---|
| JWT JWKS | Uses guarded dial through auth options | Retain; add subsystem attribution and redirect tests |
| Forward-auth | Uses guarded dial through auth options | Retain; add subsystem attribution and redirect tests |
| DNS/SRV discovery | Resolver/network behavior partly separate | Document what is and is not policy-controlled; do not pretend DNS lookup itself is an HTTP fetch |
| Consul discovery | Guarded dial | Retain; add explicit tests/metrics |
| Kubernetes discovery | Guarded dial | Retain; add explicit tests/metrics |
| ACME directory/order/challenge calls | Not wired through global egress client | Add guarded HTTP client to `acme.Client` |
| OCSP retrieval | May use default client | Inject guarded client/transport into stapler |
| WASM plugin `fetch` capability | Has plugin-local `allowed_hosts` | Require plugin local rule **and** global policy |
| Future AI providers | Not implemented | Define integration seam; Phase 6 must use it |
| Console/browser calls | Browser-originated, not server egress | Explicitly out of scope |
| Reverse-proxy data plane | User traffic routing, not auxiliary config fetch | Explicitly out of global egress policy unless a future separate upstream policy is designed |

## 44. Policy API hardening

### 44.1 Refactor without behavior regression

The package may remain in `internal/egress/egress.go` or be split for maintainability:

- `internal/egress/policy.go`
- `internal/egress/normalize.go`
- `internal/egress/error.go`
- `internal/egress/http.go`
- `internal/egress/policy_test.go`

### 44.2 Typed decision/error

Add:

```go
type Reason string

const (
    ReasonHostNotAllowed Reason = "host_not_allowed"
    ReasonIPNotAllowed   Reason = "ip_not_allowed"
    ReasonMixedDNS       Reason = "mixed_dns_answers"
    ReasonNoDNSAnswers   Reason = "no_dns_answers"
    ReasonInvalidAddress Reason = "invalid_address"
)

type BlockError struct {
    Subsystem string
    Host      string
    IP        string
    Reason    Reason
}

func (e *BlockError) Error() string
func (e *BlockError) Unwrap() error { return ErrBlocked }
```

Requirements:

- user-facing error names destination host and subsystem but not credentials/query strings;
- logs use normalized host;
- metrics use bounded subsystem/reason labels, never host/IP labels.

### 44.3 Normalization

Normalize:

- lowercase DNS names;
- one trailing dot;
- Unicode IDNs to ASCII using a reviewed pure-Go IDNA package if not already available;
- bracketed IPv6 and IPv6 zone handling;
- empty/malformed suffix rules;
- duplicate rules;
- CIDR canonicalization.

Validation should reject ambiguous entries instead of silently treating them as hostnames.

### 44.4 HTTP transport behavior

A guarded HTTP client must enforce:

- initial destination;
- redirects;
- actual dial destination;
- proxy behavior.

Implement the following fixed contract:

1. A `RoundTripper` wrapper validates `req.URL.Hostname()` against policy before dispatch.
2. `DialContext` validates the actual connection target.
3. Redirects are rechecked by the next RoundTrip.
4. Guarded auxiliary clients use a cloned transport with `Proxy = nil`, and therefore ignore `HTTP_PROXY`, `HTTPS_PROXY`, and `NO_PROXY`. A future explicit proxy configuration may be added as a separate, policy-aware feature. This prevents a proxy address from hiding the real target from the allow-list.

## 45. Subsystem-aware policy handles

Change the generic dial-only wiring into named handles:

```go
type Guard struct {
    policy *Policy
    observe func(Decision)
}

func (g *Guard) DialContext(subsystem string, base *net.Dialer) DialFunc
func (g *Guard) Transport(subsystem string, base *http.Transport) *http.Transport
func (g *Guard) Client(subsystem string, timeout time.Duration) *http.Client
```

Or equivalent immutable scoped wrapper:

```go
authGuard := policy.For("auth")
discoveryGuard := policy.For("discovery")
acmeGuard := policy.For("acme")
pluginGuard := policy.For("plugin")
```

Avoid free-form subsystem strings at call sites; define constants.

## 46. File-level integration plan

### `internal/app/serve.go`

- Build the policy/guard before all process-lifetime outbound clients.
- Pass scoped guards/clients to runtime, handler factory, upstream registry, and plugin manager.
- Keep policy startup-bound and fingerprinted.
- Hook decision observation to metrics/logging.

### `internal/app/runtime.go`

- Add generic outbound client/guard dependencies to `RuntimeBuilder`.
- Pass ACME-scoped client into `server.NewACMEManager`.
- Avoid importing app-specific types into server packages; pass `*http.Client` or an interface.

### `internal/server/acme.go` and `acme_stub.go`

Change signature:

```go
func NewACMEManager(
    servers []config.ServerConfig,
    onIssue func(domain string, notAfter time.Time),
    client *http.Client,
) (ACMEManager, error)
```

Set `acme.Client.HTTPClient` to the guarded client. Preserve default behavior when nil.

### OCSP files

Locate the OCSP fetch path (for example `internal/server/ocsp.go` or stapler helper) and inject the same ACME/PKI-scoped client. Add redirect and block tests.

### `internal/app/factory.go`

- Replace or augment raw `EgressDial` with a scoped auth guard/client.
- Ensure JWT and forward-auth share the common policy but retain their own timeouts.

### `internal/auth/*`

- Keep constructors independent of `internal/egress` by accepting generic dial/client options.
- Ensure both JWKS refresh and forward-auth use the injected transport for every request.
- Add typed blocked-error mapping suitable for logs and request errors.

### `internal/upstream/registry.go`, discovery files

- Pass scoped discovery dial/client through all providers.
- Ensure Consul redirects and Kubernetes API calls are guarded.
- Document DNS/SRV resolver behavior separately.

### `internal/plugins/*`

- Extend `plugins.Options` with a global egress guard/dial.
- Plugin `fetch` authorization is an intersection:
  1. plugin capability `fetch=true`;
  2. target matches plugin `allowed_hosts`;
  3. target passes global egress policy when enabled.
- Return a distinct guest-visible error code/message for global policy denial without exposing network details.

### Config/lifecycle

Modify only if required:

- `internal/config/schema.go`
- `internal/config/validate.go`
- `internal/lifecycle/lifecycle.go`
- `internal/lifecycle/fingerprint.go`
- `docs/config-lifecycle.yaml`

Do not add a stricter hostname-plus-CIDR mode or explicit proxy setting in this phase; the fixed semantics above are sufficient.

## 47. Egress observability

### Metrics

Add bounded metrics:

- `jul_egress_decisions_total{subsystem,result,reason}`
- `jul_egress_dns_answers_total{subsystem,result}` if useful and bounded

Do not label by destination.

### Logs

On block:

- level warning or info according to subsystem;
- fields: subsystem, normalized host, optional resolved IP, reason;
- redact query strings and credentials;
- rate-limit repetitive identical logs.

### Console

Add to Security or Operations:

- policy enabled/disabled;
- allow-rule count;
- recent blocked count by subsystem/reason;
- documentation link;
- no destination history unless explicitly designed with privacy bounds.

Likely files:

- `internal/admin/projection_types.go`
- `internal/admin/projections.go`
- `internal/admin/ui/src/api/client.ts`
- `internal/admin/ui/src/features/security/*` or Operations panel
- tests

## 48. Phase 4 tests

### Policy unit tests

- exact hostname;
- suffix hostname excludes apex unless documented otherwise;
- trailing dot/case normalization;
- IDNA normalization;
- IPv4/IPv6 literal;
- CIDR match/miss;
- mixed allowed/disallowed DNS answers rejected;
- no answers;
- direct dial uses validated address;
- duplicate/invalid rules rejected;
- disabled policy passes through unchanged;
- typed error reason/subsystem.

### HTTP integration tests

- initial allowed/blocked URL;
- redirect to blocked host;
- redirect to allowed host;
- environment proxy variables are ignored and guarded clients use the fixed direct-transport contract;
- TLS SNI/Host preserved when dialing an IP;
- connection reuse does not bypass checks for a new host;
- timeout/cancellation propagation.

### Subsystem tests

- JWKS allowed/blocked;
- forward-auth allowed/blocked;
- Consul allowed/blocked;
- Kubernetes allowed/blocked;
- ACME directory fetch allowed/blocked with a local test server;
- OCSP allowed/blocked;
- plugin fetch: local allowed but global blocked, global allowed but plugin blocked, both allowed.

### Lifecycle tests

- change to `[egress]` requires restart if policy remains startup-bound;
- secret-referenced egress entries resolve consistently;
- pending-restart banner includes egress.

### Race/leak

- concurrent guarded dials;
- DNS resolver seam under race;
- repeated blocked requests do not leak connections/goroutines.

## 49. Phase 4 documentation

**Modify**

- `docs/egress.md`
- `docs/configuration.md`
- `docs/security-posture.md`
- `SECURITY.md`
- `docs/auth.md`
- `docs/service-discovery.md`
- `docs/tls-acme.md`
- `docs/plugins.md`
- `docs/troubleshooting.md`
- `docs/known-limitations.md`
- `docs/roadmap/README.md`
- `docs/specs/hardening-platform.md`
- `docs/feature-status.yaml`/`docs/status.md` only if evidence rows change
- `CHANGELOG.md`

Document:

- exact trust semantics;
- redirects;
- DNS behavior;
- proxy behavior;
- ACME prerequisites when policy enabled;
- plugin-policy intersection;
- data-plane reverse proxy is out of scope.

## 50. Phase 4 change sequence

1. **P4-CS1:** Policy normalization, typed errors, scoped guard API, unit tests.
2. **P4-CS2:** Auth/discovery migration and integration tests without behavior change.
3. **P4-CS3:** ACME/OCSP integration.
4. **P4-CS4:** Plugin fetch intersection.
5. **P4-CS5:** Metrics, Console status, docs, lifecycle and full tagged tests.

## 51. Phase 4 Definition of Done

- Every in-scope auxiliary outbound client is inventoried and guarded.
- ACME/OCSP and plugin fetch behavior matches the fixed programme coverage.
- Redirect/proxy/DNS semantics are explicit and tested.
- Block errors are actionable and secret-safe.
- Metrics are bounded.
- Default-off behavior remains unchanged.
- Egress changes remain startup-bound, can be saved through Phase 2 `stage_restart`, and are reported accurately in pending-restart status.
- HP-07 integration hardening is marked Delivered.

---
# Phase 5 — Structured Console lifecycle, global patch operations, and adoption foundations

## 52. Phase 5 objective

Finish the current Console’s structured configuration path so the most common creation, deletion, and global-settings changes use typed, server-side operations rather than browser-generated TOML.

Phase 5 consumes the Phase 2 transaction contract:

- hot-applicable changes are applied live and return their correlated reload result;
- restart-bound changes are explicitly staged for the next restart;
- mixed candidates are never partially applied;
- raw TOML remains the complete expert escape hatch.

**Estimated effort:** M–L
**Indicative execution:** 5–8 focused weeks
**Dependencies:** Phase 2 apply/stage contract and Phase 3 permission model
**Implementation focus:** admin patch API, Console forms, lifecycle-aware preview, destructive workflows, tests, and adoption docs

## 53. Existing implementation to retain

Retain:

- backend entity operations:
  - `server_add` / `server_remove`;
  - `location_add` / `location_remove`;
  - `upstream_add` / `upstream_remove`;
- edit-existing operations for routes, backends, health checks, discovery, WAF, auth, streams, plugins, mTLS, and server limits;
- backend guard and round-trip tests;
- patch apply batching and optimistic concurrency;
- complete diff, preflight, history, audit, and rollback pipeline;
- current Route/App editor inputs and presets;
- current Traffic Controls projections and forms;
- raw editor as the universal fallback;
- Phase 2 hot/stage/discard result model;
- Phase 3 permission-gated controls.

Replace or extend:

- RouteEditor TOML-fragment append;
- AppEditor TOML-fragment append;
- compression and global rate-limit TOML upsert;
- lack of typed `global_set`, `compression_set`, and `rate_limit_global_set`;
- single-op-only preview handoff;
- lack of lifecycle classification in patch preview;
- lack of guided delete workflows;
- incomplete production migration/reference guidance.

Keep the current cache editor on its existing TOML/stage path until `cache_set` is implemented as a follow-up. Do not imply cache is hot-reloadable.

## 54. Target user flows

### 54.1 Create a route on an existing server

1. Operator opens Routes → New route.
2. Selects a server identity using listen address plus exact server-name set.
3. Configures match, action, target, and optional modifiers.
4. Console builds an ordered patch batch:
   - `location_add`;
   - optional `location_set_auth`;
   - optional cache/rate-limit/plugin operations.
5. Server previews the exact batch and returns diff, validation, lifecycle, base version, and candidate.
6. Operator applies in hot mode unless the preview requires staged restart.
7. Phase 2 result confirms live/degraded/staged/not applied.

### 54.2 Create a route on a new server

Batch:

1. `server_add`;
2. `location_add`;
3. optional modifiers.

Any operation failure aborts preview/apply as one unit.

### 54.3 Create an app/upstream

Batch:

1. `upstream_add` with first backend and strategy;
2. `upstream_add_backend` for additional backends;
3. optional `upstream_set_health_check`;
4. optional `upstream_set_discovery`;
5. optional existing/new server and `location_add` to mount it.

Native gRPC preset must generate a true native-gRPC route. If `location_add` cannot express `grpc=true`, extend its action DTO deliberately; do not silently generate a plain HTTP proxy.

### 54.4 Delete workflows

- Route detail: `location_remove`.
- Server card/detail: `server_remove`, with contained-route count and strong confirmation.
- App detail: `upstream_remove`, blocked while any route references the pool.
- No cascading delete in this phase.

### 54.5 Edit global process settings

A guided global settings form emits `global_set`.

Common hot fields:

- log level;
- worker threads;
- shutdown timeout;
- reload timeout;
- redaction minimum secret length.

`log_format` is supported by the operation but preview classifies it as restart-required, so the Console offers staged restart rather than claiming a live change.

Legacy `[global].access_log` and `[global].error_log` are excluded because they are compatibility-only and ignored by current runtime behavior.

### 54.6 Edit compression

Traffic Controls emits `compression_set`. All current compression fields are hot-reloadable through the handler generation and should normally apply live.

### 54.7 Edit global rate limiting

Traffic Controls emits `rate_limit_global_set`.

- `enabled`, `key`, `rate`, and `burst` are hot-reloadable.
- `max_conns` is listener-bound:
  - a newly introduced address is created with the candidate value;
  - if any currently bound address is retained, changing the global value requires staged restart because retained listeners keep their old cap;
  - it can apply fully live only when every affected currently bound listener is removed and all desired listeners are newly bound in the same transaction.

The preview must state this explicitly.

### 54.8 Pending restart interaction

When Phase 2 reports a pending restart:

- entity/global editors operate against the staged disk config;
- preview remains available;
- apply action becomes `Update staged configuration`;
- hot mode is not offered;
- the original rollback base remains unchanged;
- the user can discard the complete staged configuration from the shared banner.

## 55. Batch patch preview and lifecycle API

### 55.1 Current gap

`/api/config/patch/apply` accepts a batch, while guided preview and `useRunPatch` are optimized around one operation. Creation and global-setting workflows need an atomic preview of exactly what will later apply.

### 55.2 Additive endpoint

`POST /api/config/patch/preview`

Request:

```json
{
  "base_version": "...",
  "ops": [
    {"op":"server_add","listen":":8080","server_names":["example.com"]},
    {"op":"location_add","listen":":8080","server_names":["example.com"],"match_set":{"type":"prefix","path":"/"},"action":{"kind":"proxy","target":"http://api"}}
  ]
}
```

Response:

```json
{
  "candidate": "...canonical TOML...",
  "base_version": "...",
  "summary": ["server :8080 added", "route prefix / added"],
  "diff": {"summary":"...","additions":[],"modifications":[],"removals":[]},
  "validation_errors": [],
  "lifecycle": {
    "can_apply_hot": true,
    "can_stage_restart": true,
    "hot_paths": ["[compression].enabled"],
    "restart_required_paths": [],
    "new_listener_only_paths": [],
    "pending_subsystems": []
  }
}
```

### 55.3 Backend behavior

- load a fresh config;
- verify optional base version;
- apply ops in order to an in-memory config;
- on op N failure return `op_index`, `op`, and humanized error;
- marshal/reparse the candidate;
- run side-effect-free validation;
- compare before/after through `lifecycle.DiffAddressAware` and the lifecycle registry;
- return diff and classification;
- persist nothing.

Preview and apply MUST use the same batch executor and operation implementations.

### 55.4 Lifecycle summary

Create a shared server-side helper, likely in `internal/admin/patch_lifecycle.go`:

```go
type PatchLifecycleSummary struct {
    CanApplyHot          bool     `json:"can_apply_hot"`
    CanStageRestart      bool     `json:"can_stage_restart"`
    HotPaths             []string `json:"hot_paths,omitempty"`
    RestartRequiredPaths []string `json:"restart_required_paths,omitempty"`
    NewListenerOnlyPaths []string `json:"new_listener_only_paths,omitempty"`
    PendingSubsystems    []string `json:"pending_subsystems,omitempty"`
}
```

Classification rules:

- use canonical lifecycle registry entries, not a patch-op-specific hard-coded list;
- listener-aware checks use the Phase 2 live snapshot;
- no sensitive value appears in paths or summaries;
- `CanApplyHot=false` when any effective change cannot fully apply live;
- `CanStageRestart=true` for a valid candidate unless the planned-restart store is inconsistent or a startup preflight fails at apply time.

### 55.5 Files

**Modify**

- `internal/admin/patch_http.go`
- `internal/admin/patch.go`
- `internal/admin/patch_types.go`
- `internal/admin/routes.go`
- `internal/admin/diff.go`
- `internal/admin/diff_global.go`
- `internal/admin/operational_test.go`

**New recommended**

- `internal/admin/patch_batch.go`
- `internal/admin/patch_batch_test.go`
- `internal/admin/patch_lifecycle.go`
- `internal/admin/patch_lifecycle_test.go`

## 56. Near-term global-table operations

### 56.1 Transport types

Add pointer-based sparse DTOs in `internal/admin/patch_types.go`. Omitted fields preserve the current value; explicit false/zero/empty values retain their documented meaning.

```go
type globalPatch struct {
    WorkerThreads        *string `json:"worker_threads,omitempty"`
    LogLevel             *string `json:"log_level,omitempty"`
    LogFormat            *string `json:"log_format,omitempty"`
    ShutdownTimeout      *string `json:"shutdown_timeout,omitempty"`
    ReloadTimeout        *string `json:"reload_timeout,omitempty"`
    RedactMinSecretLength *int   `json:"redact_min_secret_length,omitempty"`
}

type compressionPatch struct {
    Enabled       *bool     `json:"enabled,omitempty"`
    Encoders      *[]string `json:"encoders,omitempty"`
    Level         *int      `json:"level,omitempty"`
    MinSize       *string   `json:"min_size,omitempty"`
    Types         *[]string `json:"types,omitempty"`
    Precompressed *bool     `json:"precompressed,omitempty"`
}

type globalRateLimitPatch struct {
    Enabled  *bool   `json:"enabled,omitempty"`
    Key      *string `json:"key,omitempty"`
    Rate     *int    `json:"rate,omitempty"`
    Burst    *int    `json:"burst,omitempty"`
    MaxConns *int    `json:"max_conns,omitempty"`
}
```

Add to `patchRequest`:

```go
Global          *globalPatch          `json:"global,omitempty"`
Compression     *compressionPatch     `json:"compression,omitempty"`
GlobalRateLimit *globalRateLimitPatch `json:"rate_limit,omitempty"`
```

Operation names:

- `global_set`
- `compression_set`
- `rate_limit_global_set`

Do not reuse the location `RateLimitPatch` blindly because `max_conns` is valid only at global scope and every scalar needs presence tracking.

### 56.2 Common sparse-update rules

For all three operations:

- payload is required;
- at least one field must be present;
- validate every supplied field before mutating the config;
- do not partially mutate on validation failure;
- copy current struct, apply validated fields to the copy, then assign;
- return a summary listing changed field names, not values that could be sensitive;
- marshal/reparse/validate after batch execution remains authoritative.

### 56.3 `global_set`

Supported fields:

- `worker_threads` — `auto` or positive integer string;
- `log_level` — debug/info/warn/error;
- `log_format` — text/json; restart-required;
- `shutdown_timeout` — valid positive duration;
- `reload_timeout` — valid positive duration; zero/unbounded remains unsupported under current defaults;
- `redact_min_secret_length` — current allowed range.

Excluded fields:

- legacy `[global].access_log`;
- legacy `[global].error_log`.

Important transaction rule:

- the reload that changes `reload_timeout` uses the currently running timeout;
- the candidate’s new timeout governs subsequent reloads only.

Implementation helpers in `patch_builders.go` or a new `patch_global.go` parse durations and worker-thread values without mutating the original config.

### 56.4 `compression_set`

Supported fields:

- `enabled`;
- `encoders`;
- `level`;
- `min_size`;
- `types`;
- `precompressed`.

Semantics:

- omitted field preserves current value;
- explicit `enabled:false` disables compression while retaining other settings for later re-enable;
- supplied arrays replace existing arrays;
- `encoders:[]` means reset to the parser/default encoder set when enabled;
- `types:[]` means reset to the parser/default MIME set;
- `level:0` means encoder default;
- unsupported `br`/`zstd` still fail through build-tag preflight;
- duplicate list entries are normalized or rejected consistently with config validation.

### 56.5 `rate_limit_global_set`

Supported fields:

- `enabled`;
- `key`;
- `rate`;
- `burst`;
- `max_conns`.

Semantics:

- enabled policy requires a valid positive rate;
- burst zero resets to rate/default behavior;
- `max_conns:0` means unlimited;
- key uses existing `config.ValidRateKey`;
- changing rate/burst preserves existing bucket state because `RateLimiterStore` already updates parameters in place on the next use;
- changing key naturally changes the derived bucket key space; old idle buckets expire through the existing janitor;
- `max_conns` lifecycle comes from listener-aware classification, not a blanket operation rule. Merely adding one new listener while retaining existing listeners is not sufficient for a fully live change.

### 56.6 Backend files and tests

**Modify**

- `internal/admin/patch_types.go`
- `internal/admin/patch.go`
- `internal/admin/patch_builders.go`
- `internal/admin/patch_helpers.go` only for shared presence/summary helpers
- `internal/config/validate.go` only if a reusable validation helper is missing
- `internal/admin/diff_global.go` if any new field is not already represented

**New tests**

- `internal/admin/patch_global_test.go`
- `internal/admin/patch_compression_test.go`
- `internal/admin/patch_rate_limit_global_test.go`

Required test cases:

- sparse update preserves omitted fields;
- explicit false/zero/empty arrays;
- invalid field leaves config unchanged;
- multi-op batch all-or-nothing;
- marshal/reparse/validate round trip;
- lifecycle summary for hot-only global change;
- `log_format` requires staged restart;
- compression applies hot;
- rate/burst/key apply hot;
- `max_conns` on existing listener requires stage;
- `max_conns` may apply live only when no affected listener address is retained; retaining any existing address requires staging;
- unsupported encoder build tag fails at authoritative apply;
- audit summary contains field names but no secret values.

## 57. Frontend typed patch model and shared handoff

### 57.1 `client.ts`

Add entity CRUD union members if still absent and the three global operations:

```ts
export type GlobalPatch = {
  worker_threads?: string;
  log_level?: "debug" | "info" | "warn" | "error";
  log_format?: "text" | "json";
  shutdown_timeout?: string;
  reload_timeout?: string;
  redact_min_secret_length?: number;
};

export type CompressionPatch = {
  enabled?: boolean;
  encoders?: string[];
  level?: number;
  min_size?: string;
  types?: string[];
  precompressed?: boolean;
};

export type GlobalRateLimitPatch = {
  enabled?: boolean;
  key?: string;
  rate?: number;
  burst?: number;
  max_conns?: number;
};
```

Union members:

```ts
| { op: "global_set"; global: GlobalPatch }
| { op: "compression_set"; compression: CompressionPatch }
| { op: "rate_limit_global_set"; rate_limit: GlobalRateLimitPatch }
```

Add schemas for:

- batch preview;
- per-op batch error;
- lifecycle summary;
- apply mode inherited from Phase 2.

### 57.2 Shared batch hook

Create `internal/admin/ui/src/lib/useRunPatchBatch.ts`.

Responsibilities:

- accept ordered ops;
- request batch preview;
- store exact ops, base version, candidate, diff, and lifecycle summary;
- navigate to Config panel;
- expose busy/error;
- preserve operation order;
- never claim success before final apply/stage result.

Refactor `useRunPatch` to call `useRunPatchBatch([patch])`.

### 57.3 Pure conversion helpers

Create or extend:

- `internal/admin/ui/src/lib/routePatch.ts`
- `internal/admin/ui/src/lib/appPatch.ts`
- `internal/admin/ui/src/lib/globalPatch.ts`
- `internal/admin/ui/src/lib/trafficPatch.ts`

These are pure, deterministic draft-to-`ConfigPatch[]` converters with near-side validation. They do not fetch, navigate, or mutate global state.

## 58. Route and app editor implementation

### 58.1 Route editor

Modify:

- `internal/admin/ui/src/features/routes/RouteEditor.tsx`
- `internal/admin/ui/src/features/routes/RoutesPanel.tsx`
- `internal/admin/ui/src/features/routes/RouteDetail.tsx`

Replace raw fetch/TOML append for supported new routes with patch batches.

Required behavior:

- existing-server versus new-server mode;
- exact server identity;
- supported action conversion;
- optional auth/cache/rate limit conversion;
- batch lifecycle preview;
- permission gating from Phase 3;
- raw/protocol-specific handoff only for actions the structured DTO does not represent.

### 58.2 App editor

Modify:

- `internal/admin/ui/src/features/apps/AppEditor.tsx`
- `internal/admin/ui/src/features/apps/AppsPanel.tsx`
- `internal/admin/ui/src/features/apps/AppDetail.tsx`

Replace raw TOML generation with ordered upstream/server/location ops.

Required behavior:

- first backend through `upstream_add`;
- additional backends;
- health check;
- optional discovery;
- mount existing or new server;
- correct HTTP versus native-gRPC action;
- dependency-aware delete.

### 58.3 Delete confirmation

Create or reuse one confirmation component that shows:

- exact resource identity;
- affected dependent/contained objects;
- lifecycle outcome;
- no cascading delete;
- typed confirmation only for multi-route server deletion if existing UI patterns support it without excessive complexity.

## 59. Global settings and Traffic Controls UI

### 59.1 Global settings editor

Locate the current settings/global form and migrate supported fields to `global_set`. If no suitable v2 component exists, add a small `GlobalSettingsEditor.tsx` under the existing Config/Settings feature directory rather than creating a new top-level navigation area.

Requirements:

- initialize from a safe projection, never raw secret-bearing config;
- show hot/restart badge per field;
- exclude legacy access/error-log fields;
- preview through shared patch batch;
- `log_format` naturally produces a stage option;
- display that a changed `reload_timeout` applies to later transactions.

Backend projection may require a safe `GlobalSettingsProjection` containing only non-secret fields.

### 59.2 Compression editor

Modify `TrafficControlEditor.tsx`:

- `compression` branch emits `compression_set`;
- remove `fetchRawConfig`, `upsertTopLevelTable`, and `generateCompressionToml` from this path;
- retain warnings and affected-route display;
- add level input if current UI omits it and projection supports it;
- use batch preview/lifecycle and Phase 2 apply result.

Keep TOML generator helpers only if still used by examples or raw fallback; otherwise delete them and their tests deliberately.

### 59.3 Global rate-limit editor

Modify `TrafficControlEditor.tsx`:

- `rate_limit` branch emits `rate_limit_global_set`;
- preserve stats and affected-route display;
- explain `max_conns` as listener-level;
- when `max_conns` creates restart need, show stage action instead of generic failure;
- do not reset omitted current fields because a projection did not expose them.

Ensure the traffic-controls projection includes every editable global rate-limit and compression field so the editor round-trips faithfully.

### 59.4 Cache editor during this phase

Do not add `cache_set` yet.

The existing cache form may continue to generate a complete `[cache]` table, but it must use the Phase 2 planned-restart result:

- preview explicitly says every cache-field change is restart-required;
- default action is `Save for next restart`, not `Apply live`;
- current cache/runtime remains unchanged;
- pending banner appears;
- current raw path remains available.

If adapting the existing cache editor to Phase 2 mode requires less code than leaving its old handoff, do so, but do not claim structured patch parity.

## 60. Config-panel handoff and apply completion

`configDraftHandoff` must carry:

- candidate;
- ordered ops;
- base version;
- diff;
- lifecycle summary;
- recommended/default apply mode;
- whether a managed pending restart already exists.

ConfigPanel rules:

- hot-applicable candidate: primary action `Apply live`;
- restart-required candidate: primary action `Save for next restart` after explicit confirmation;
- mixed candidate: stage complete candidate;
- pending restart: primary action `Update staged configuration`;
- show exact fields/subsystems that force restart;
- show final correlated result rather than inferring from polling;
- invalidate and refetch routes/apps/traffic/global/pending status after completion;
- retain rollback/history navigation.

## 61. Deferred global-table follow-ups

These are not Phase 5 code deliverables. The plan fixes their future shape so they do not need product redesign later.

### 61.1 `cache_set`

#### Why deferred

All cache fields are startup-bound today. The planned-restart foundation makes a truthful operation possible, but the existing guided table already covers the user need and entity/global hot operations have higher value.

#### Future operation

```json
{
  "op": "cache_set",
  "cache": {
    "enabled": true,
    "memory_max_size": "256m",
    "disk_path": "/var/lib/jul/cache",
    "disk_max_size": "4g",
    "default_ttl": "60s",
    "stale_while_revalidate": "30s",
    "stale_if_error": "5m"
  }
}
```

Requirements:

- sparse pointer DTO;
- all changed paths classified restart-required;
- stage-only Console action;
- startup preflight validates size values and disk-path writability;
- no cache runtime mutation during staging;
- no promise of preserving in-memory entries across restart;
- future hot-swappable cache, if desired, is a separate architecture project involving generation ownership, open disk resources, metrics continuity, and in-flight cache operations.

Expected follow-up effort: S for patch/API/UI after Phase 2 is proven; L for true hot-swap.

### 61.2 Admin operations after RBAC

Do not implement one `admin_set` object. It would combine access, credentials, audit, plugin upload, and operational limits under one oversized permission and one risky confirmation.

After Phase 3, implement only if needed, as separate operations:

- `admin_listener_set` — enabled/listen/Console;
- `admin_auth_set` — legacy compatibility/RBAC-related startup settings;
- `admin_limits_set` — read/write/apply rate limits and SSE cap;
- `admin_history_set` — history path/retention;
- `admin_audit_set` — audit sink and rotation;
- `admin_plugin_upload_set` — upload enablement/path/size.

Requirements:

- permission `admin:manage`;
- explicit self-lockout and last-admin guards;
- secret inputs accepted but never projected back;
- actor/token attribution from RBAC;
- initial lifecycle is staged restart for every component still built once by `admin.New`;
- a component becomes hot-reloadable only after its runtime is deliberately moved behind an atomic/dynamic owner;
- operations have separate audit verbs and confirmations.

Expected follow-up effort: M after RBAC for staged operations; larger if dynamic admin reconfiguration is attempted.

### 61.3 `access_log_set`

Future operation:

```json
{
  "op": "access_log_set",
  "access_log": {
    "sinks": ["stdout", "file"],
    "file": "/var/log/jul/access.log",
    "format": "json",
    "rotate_max_mb": 250,
    "rotate_keep": 14
  }
}
```

Initial requirements:

- sparse pointer/list DTO;
- stage-only lifecycle because sinks are opened once at startup;
- startup preflight opens/closes or safely probes the requested sink;
- no confusion with ignored legacy `[global].access_log`;
- permission should be a future observability/admin-management permission, decided with the final RBAC catalog;
- clear warning that the currently open sinks remain active until restart.

A future hot-swap design must build new sinks, prove them healthy, atomically publish the new sink set, drain in-flight writers, and close old sinks. That is separate from the patch operation.

Expected follow-up effort: S–M for staged operation; M–L for hot-swappable sinks.

## 62. Local workflow evidence and support diagnostics

No phone-home is added.

Use existing audit/timeline and optional local support export to answer practical questions:

- structured preview attempted;
- structured apply live;
- structured apply staged;
- structured apply rejected;
- raw apply used;
- resource type affected.

Do not record config values, route names, addresses, paths, or user payloads merely for product analytics.

If a support bundle exists or is added, it may summarize operation counts from local audit data. Export remains operator-initiated.

## 63. Adoption and production documentation

Prefer improving existing pages over creating many overlapping guides.

Required outcomes:

1. NGINX migration path with supported/unsupported directive mapping and rollback.
2. Production deployment checklist covering file permissions, admin exposure, systemd/container, backups, logs, and upgrades.
3. First gRPC gateway walkthrough.
4. WASM plugin quickstart linked to ABI/security docs.
5. Upgrade and rollback procedure, including planned-restart staging and discard.
6. Reference configurations for:
   - single-node public reverse proxy;
   - internal gRPC gateway;
   - WAF + mTLS security gateway;
   - container deployment;
   - discovery-backed service;
   - Console-managed changes.

Before creating a new file, inspect `README.md`, `docs/deployment.md`, `docs/console.md`, `docs/nginx-importer.md`, `docs/grpc-transcoding.md`, `docs/grpc-proxy.md`, `docs/plugins.md`, and examples. Extend them when one clear home already exists.

## 64. Comparative product evidence

Add a concise reproducible section to existing benchmark docs covering:

- startup time;
- lean/full binary size;
- idle memory;
- plain proxy throughput/latency;
- reload latency;
- gRPC capabilities;
- deployment steps/dependencies.

Do not claim universal superiority. State the dimensions Jul.IA is designed to optimize and link exact commands/results.

## 65. Phase 5 tests

### 65.1 Batch backend tests

- preview and apply share executor;
- ordered multi-op success;
- failure reports exact index/op and mutates nothing;
- stale base version rejected;
- full validation after batch;
- lifecycle summary matches before/after;
- preview never persists;
- apply mode hot/stage routes correctly.

### 65.2 Global operation backend tests

All cases listed in §56.6, plus:

- pointer presence distinguishes omitted versus false/zero;
- summaries are stable enough for audit but do not expose sensitive values;
- planned restart applies `log_format`/`max_conns` changes to disk only;
- after simulated restart, staged values become effective.

### 65.3 Pure frontend tests

- route draft to batch;
- app draft to batch;
- global draft to `global_set`;
- compression draft to `compression_set`;
- rate-limit draft to `rate_limit_global_set`;
- existing/new server choice;
- native gRPC action;
- omitted fields not emitted;
- zero/false fields emitted when intentional;
- lifecycle result selects the correct action label.

### 65.4 Component tests

- create route/app;
- delete route/server/app;
- dependency rejection navigation;
- global settings hot versus staged fields;
- compression live flow;
- max-conns stage flow;
- cache editor stage-only wording;
- pending restart update/discard;
- RBAC hides or disables unauthorized controls;
- conflict and validation errors preserve the draft.

### 65.5 E2E

Real-server browser/API tests:

1. create app and route through one batch;
2. verify data-plane response;
3. change compression through typed patch and verify encoding;
4. change global rate/burst and verify 429 behavior;
5. stage `log_format` or existing-listener `max_conns` change;
6. verify runtime unchanged and banner present;
7. update staged config with another patch;
8. discard and verify exact file restoration;
9. delete route/app safely;
10. rollback through history.

### 65.6 Accessibility and size

- drawers/dialogs keyboard operable;
- confirmation focus management;
- status not communicated only by color;
- Console bundle remains inside existing budget.

## 66. Phase 5 file-level plan

### Backend

- `internal/admin/patch_types.go`
- `internal/admin/patch.go`
- `internal/admin/patch_builders.go`
- `internal/admin/patch_helpers.go`
- `internal/admin/patch_http.go`
- `internal/admin/patch_batch.go` (new)
- `internal/admin/patch_lifecycle.go` (new)
- `internal/admin/diff.go`
- `internal/admin/diff_global.go`
- `internal/admin/routes.go`
- `internal/admin/projection_types.go`
- traffic/global projection files
- related unit/integration tests

### Frontend API and helpers

- `internal/admin/ui/src/api/client.ts`
- `internal/admin/ui/src/lib/useRunPatch.ts`
- `internal/admin/ui/src/lib/useRunPatchBatch.ts` (new)
- `internal/admin/ui/src/lib/configDraftHandoff.ts`
- `internal/admin/ui/src/lib/routePatch.ts` (new)
- `internal/admin/ui/src/lib/appPatch.ts` (new)
- `internal/admin/ui/src/lib/globalPatch.ts` (new)
- `internal/admin/ui/src/lib/trafficPatch.ts` (new)
- remove obsolete TOML helpers only when no caller remains

### Frontend components

- Routes panel/editor/detail
- Apps panel/editor/detail
- Traffic Controls panel/editor
- current global/settings editor or a small new component under the existing feature area
- Config panel and shared apply/pending-restart components
- shared confirmation component

### Tests/E2E

- backend patch batch/global operation tests
- frontend pure conversion tests
- component tests
- `internal/admin/ui/e2e/real-server.spec.ts`
- `internal/admin/ui/e2e/smoke.spec.ts`

### Docs

- `docs/console.md`
- `docs/configuration.md`
- `docs/compression.md`
- `docs/ratelimit.md`
- `docs/reload-semantics.md`
- `docs/specs/hardening-platform.md`
- `docs/roadmap/README.md`
- relevant deployment/migration/gRPC/plugin pages and examples
- benchmark documentation
- `CHANGELOG.md`

## 67. Phase 5 change sequence

1. **P5-CS1:** Shared batch preview executor, batch errors, and lifecycle summary.
2. **P5-CS2:** Frontend batch types/hook and entity CRUD union members.
3. **P5-CS3:** Route creation/deletion through typed batches.
4. **P5-CS4:** App/upstream creation/deletion through typed batches.
5. **P5-CS5:** Backend `global_set`, `compression_set`, and `rate_limit_global_set` with tests.
6. **P5-CS6:** Global/Traffic Controls UI migration and planned-restart integration.
7. **P5-CS7:** E2E, accessibility, documentation/adoption updates, and obsolete helper cleanup.

## 68. Phase 5 Definition of Done

- Core servers, routes, and upstreams can be created and deleted through typed batch operations.
- Preview and apply execute the same ordered operations.
- `global_set`, `compression_set`, and `rate_limit_global_set` are available through API and Console.
- Global operations preserve omitted fields and correctly handle explicit false/zero/list values.
- Lifecycle preview is sourced from the canonical registry.
- Hot-applicable settings apply live with correlated results.
- Restart-bound settings stage the complete candidate; no partial apply occurs.
- Cache continues to use a truthful stage-only workflow without pretending `cache_set` exists.
- Admin and access-log follow-ups are documented with exact future boundaries.
- Destructive actions are dependency-aware, permission-gated, previewed, and reversible through history.
- Raw TOML remains available.
- Production migration, deployment, upgrade, and rollback guidance is sufficient for a new operator.
- HP-06B and the agreed HP-06C near-term operations are marked delivered.

---
# Phase 6 — Time-boxed AI Gateway MVP

## 69. Phase 6 objective

Test the AI gateway opportunity without committing to the full Year 4 programme.

**Effort:** L
**Time box:** fixed before implementation
**Entry dependencies:** Phases 1–5 complete, especially correlated hot/staged applies, RBAC, egress coverage, typed settings patterns, and lean-budget baselines.

## 70. MVP scope

### Include

- `ai` build tag;
- OpenAI-compatible chat/completions front door;
- two providers at first usable milestone and three by exit;
- provider routing and fallback chain;
- streaming;
- explicit timeouts, retry rules, and circuit behavior;
- token and cost metrics;
- provider credentials through secret references;
- all provider traffic through the Phase 4 direct, scoped egress policy;
- RBAC-gated Console status/routing view;
- configuration through the normal validate/preview/hot-or-stage transaction;
- known limitations and data-governance policy;
- explicit continue/extend/stop decision.

### Exclude

- semantic cache;
- comprehensive guardrails;
- distributed token budgets;
- autonomous configuration changes;
- Cloud billing;
- full Year 4 edge/CDN/FaaS scope;
- JUL-native neutral public protocol unless the MVP demonstrates a concrete need.

## 71. Additional required detail

Before code, create only the two documents that have no existing clear home:

- `docs/specs/ai-gateway-mvp.md`
- `docs/ai-data-governance.md`

Also define:

- provider contract-test fixture format;
- exact continue/extend/stop thresholds copied from the roadmap evidence gate;
- an `ai` build-tag binary/RSS budget in the existing benchmark docs;
- model/provider naming stability rules;
- provider error normalization and streaming partial-failure semantics;
- how a fallback across providers or regions is exposed to operators and audit;
- configuration lifecycle for provider/routing settings;
- safe local diagnostics with no prompt/response payloads by default.

Data governance must define:

- prompt and response logging default-off;
- metadata retention;
- credential isolation;
- fallback across providers/regions;
- retry semantics and duplicate-request risk;
- streaming partial response behavior;
- cost estimate versus provider-reported reconciliation;
- audit fields;
- no payloads in metrics;
- no central telemetry.

## 72. Exit decision

Create one concise decision note under `docs/reviews/` with one outcome:

- **Continue:** activate only the most evidenced follow-up, likely token/cost controls or baseline guardrails.
- **Extend once:** one unanswered hypothesis and one additional fixed time box.
- **Stop:** retain as experimental, deprecate, or remove according to its maturity/compatibility state.

Do not activate the full Year 4 plan automatically.

---

# Phase 7 — Activate one evidence-backed horizon

## 73. Principle

After the AI MVP decision, use the roadmap’s evidence thresholds to activate at most one new strategic category. “No category qualifies” is a valid and healthy result.

## 74. Possible activated paths

### If AI evidence is strongest

1. token budgets and cost controls;
2. baseline policy/guardrails;
3. semantic cache only as experimental after accuracy/privacy evaluation.

### If fleet evidence is strongest

1. node identity/enrollment;
2. central config distribution;
3. staged rollout;
4. health-gated promotion;
5. fleet rollback.

### If Kubernetes evidence is strongest

Deliver a bounded Gateway API subset with conformance, status writeback, and Helm packaging. Do not simultaneously invent a separate fleet control plane without independent evidence.

### If no gate is met

Continue improving migration, reliability, performance, examples, and user-requested capabilities. Do not manufacture a horizon commitment to keep the roadmap busy.

## 75. Activation note

Before horizon implementation, add a short roadmap-linked decision note containing:

- evidence versus threshold;
- target user/problem;
- selected scope and non-goals;
- lean/security/compatibility constraints;
- fixed exit or kill conditions.

Do not add an owner matrix, capacity plan, or new active-plan manifest.

---

# Programme-level implementation controls

## 76. Master dependency graph

```text
Phase 1: lean docs/roadmap constraints
   |
   v
Phase 2: reload result + managed hot apply + planned restart
   |
   v
Phase 3: RBAC policy and permission boundary
   |
   v
Phase 4: complete outbound egress coverage
   |
   v
Phase 5: entity CRUD + global/compression/rate patches + adoption docs
   |
   v
Phase 6: bounded AI MVP
   |
   v
Phase 7: at most one evidence-backed horizon
```

Phase 4’s isolated policy tests may overlap late Phase 3 UI work. Phase 5 pure conversion helpers and docs may overlap late Phase 4. Shared composition-root/API changes should land serially.

## 77. Solo-project execution model

This is a solo, AI-assisted project. Use enough structure to prevent bugs, not enough to create administrative work.

- The maintainer selects the next work package from this plan.
- AI agents may implement, review, test, or document slices, but the repository state and tests are authoritative.
- Keep changes and commits single-purpose and reasonably small.
- Use existing CI and focused commands; do not create a separate tracking system.
- Record a new ADR only for a durable decision not already fixed here.
- Update the existing spec/roadmap/changelog in the same change that alters behavior.
- Stop and update this plan only if code evidence invalidates a normative assumption.

## 78. Definition of Ready for a work package

A work package is ready when:

- its prerequisite phase contract has landed;
- the touched files and public behavior are named;
- success, failure, compatibility, and security tests are listed;
- any required build tags/test services are available;
- no unresolved product choice remains in the work package.

No owner assignment, target-release field, separate approval, or docs-owner signoff is required.

## 79. Definition of Done for every phase

- code and directly affected docs are merged;
- relevant lean/full/tagged builds pass;
- race/leak tests pass for concurrency/resource changes;
- frontend typecheck/lint/unit/E2E pass for UI changes;
- migration and backward compatibility are tested;
- metrics/log/audit labels are reviewed for cardinality and secrets;
- config lifecycle and diff behavior are updated;
- roadmap/status/changelog are updated only where facts changed;
- generated Console assets are current;
- rollout and rollback behavior is documented in the existing canonical page.

## 80. Release slicing and rollback

### 80.1 Release slicing

Each phase should be independently releasable. Do not hold all phases for one release.

Defaults:

- RBAC remains opt-in.
- Egress remains default-off.
- Structured patch operations are additive.
- Planned restart is an explicit mode; existing hot apply remains the default.
- Compatibility fields remain during their documented deprecation window.

### 80.2 Rollback

- retain the prior binary and compatible config;
- do not introduce an irreversible config migration in the same release;
- RBAC retains legacy compatibility while disabled/migrating;
- egress disabled restores prior outbound behavior;
- planned-restart discard restores exact bytes only when verification proves it is safe;
- if a staged config fails on process restart, use the `.bak`/history recovery procedure and prior binary as documented;
- UI and backend schema must ship together.

## 81. Security review checkpoints

A separate named reviewer is not required, but each sensitive change must include a written self-review checklist in the change or commit notes and focused negative tests.

- **Phase 2:** restoration, sidecar permissions, digest/version checks, no partial apply, timeout DoS, secret safety.
- **Phase 3:** token hashing, authn/authz, route coverage, self/last-admin lockout, audit attribution.
- **Phase 4:** DNS, redirect, direct-transport behavior, ACME availability, plugin policy intersection.
- **Phase 5:** destructive operations, object-level RBAC, sparse patch semantics, lifecycle classification, staged versus live truth.

AI-assisted review should be used as an additional check, never as a replacement for tests.

## 82. Performance and lean checkpoints

- Phase 2: reload duration, staged metadata overhead, and resource churn; no request hot-path regression.
- Phase 3: authentication lookup latency/allocation and atomic policy-swap contention.
- Phase 4: disabled-policy near-zero overhead and enabled DNS/dial behavior.
- Phase 5: patch preview latency, Console bundle budget, and no unnecessary raw-config fetches.

Record results in the existing benchmark documentation.

## 83. Master test matrix

| Concern | P1 | P2 | P3 | P4 | P5 |
|---|:---:|:---:|:---:|:---:|:---:|
| Docs structural validation | ✅ | ✅ | ✅ | ✅ | ✅ |
| Config parse/round-trip | — | ✅ | ✅ | ✅ | ✅ |
| Lifecycle classification | — | ✅ | ✅ | ✅ | ✅ |
| Hot/staged apply contract | — | ✅ | permissions | startup-bound | ✅ |
| Exact file restore/discard | — | ✅ | guarded | — | E2E |
| Lean/full builds | ✅ | ✅ | ✅ | ✅ | ✅ |
| Race | — | ✅ | ✅ | ✅ | targeted |
| Leak/resource teardown | — | ✅ | targeted | targeted | — |
| Admin over-the-wire | — | ✅ | ✅ | projections | ✅ |
| Frontend unit/type/lint | — | ✅ | ✅ | ✅ | ✅ |
| Real-server E2E | — | ✅ | ✅ | ✅ local mock endpoints | ✅ |
| Security negative cases | docs | ✅ | ✅ | ✅ | ✅ |
| Compatibility/migration | docs | ✅ | ✅ | ✅ | ✅ |
| Performance/size | baseline | reload | auth lookup | policy overhead | preview/bundle |

## 84. Master file-change index

This is an index, not an instruction to edit every file.

### Phase 1

- `docs/roadmap/README.md`
- `docs/vision/README.md`
- `docs/specs/hardening-platform.md`
- `docs/specs/year-3.md`
- `docs/specs/year-4.md`
- `docs/specs/year-5.md`
- canonical benchmark documentation
- `docs/adr/0012-oss-open-core-boundary.md` (new)
- `docs/index.md`
- `docs/specs/README.md`
- `README.md`
- `scripts/docs-check.py`
- `scripts/test_docs_check.py`

### Phase 2

- new server reload-result types/tests
- new app config-apply coordinator/tests
- new planned-restart store/tests
- server/reload plan/context changes
- app serve/wiring/preflight/factory changes
- stream and context-aware builder changes
- startup preflight helpers in cache/observability/admin/ACME as needed
- admin apply/pending-restart APIs, projections, history/audit/timeline/tests
- Console client/outcome/config/overview components and E2E
- reload/config/Console/troubleshooting docs

### Phase 3

- `internal/rbac/*`
- RBAC config validation/secret/lifecycle changes
- admin route catalog/authz/audit/identity/lockout/diff/projection changes
- app candidate-policy wiring
- Console permission provider and gated controls
- migration/security/config/compatibility docs

### Phase 4

- `internal/egress/*`
- app/runtime/factory wiring
- ACME/OCSP injection
- auth/discovery/plugin integrations
- observability/projections/Console status
- egress/security/auth/discovery/ACME/plugin docs

### Phase 5

- admin patch batch/lifecycle/global operation code/tests
- Console patch union/schemas/batch hook
- route/app/global/traffic pure patch helpers/tests
- Route/App/Traffic/Config/global settings components
- E2E and accessibility tests
- production/migration/benchmark docs

## 85. Risk register

| Risk | Phase | Mitigation |
|---|---:|---|
| Non-cancellable constructor exceeds deadline | 2 | Context checks around call, staged-resource abort, exact phase result, leak tests |
| File restoration races with watcher | 2 | One coordinator, exact digest suppression, serialized writes, echo tests |
| Planned-restart crash leaves incomplete metadata | 2 | Prepared/staged marker states and startup reconciliation |
| Mixed candidate is partially applied | 2/5 | Whole-candidate mode invariant and E2E tests |
| Unsafe discard overwrites external change | 2 | Disk digest and live serving-version verification; refuse on mismatch |
| Staged config passes preflight but environment changes before restart | 2 | Clear limitation, startup failure recovery, exact backup, no false guarantee |
| Post-Publish rollback corrupts generations | 2 | Publish is point of no return |
| RBAC route omitted | 3 | Static catalog, guard test, fail closed |
| RBAC locks out all admins | 3 | validation, current-caller confirmation, last-admin rule, legacy migration |
| Token leaks | 3 | safe projections, hash/token ID only, redaction and negative tests |
| Egress breaks ACME | 4 | default-off, direct-transport local integration tests, clear docs/status |
| Plugin-local and global policies disagree | 4 | intersection semantics and matrix tests |
| Sparse patch overwrites omitted values | 5 | pointer DTOs, validate-copy-assign, round-trip tests |
| Preview differs from apply | 5 | one shared batch executor |
| Destructive UI deletes dependencies | 5 | backend guards, preview, confirmation, no cascade |
| Docs become a planning bureaucracy | 1 | one active roadmap, existing canonical docs, objective checks only |
| Scope expands into full horizon | 6–7 | quantitative gates and fixed time boxes |

## 86. Implementation cautions fixed by this revision

There are no remaining blocking product decisions. Implementation must preserve these details:

1. The current running `reload_timeout`, not the candidate’s new value, governs the transaction that changes `reload_timeout`; the new value governs subsequent transactions.
2. Planned restart stages the entire candidate. There is no automatic hot subset.
3. A managed pending restart blocks hot applies until restart, staged update, or verified discard.
4. An unmanaged/external pending restart cannot be silently adopted into the managed backup/discard workflow.
5. The planned-restart backup contains exact raw bytes and must be `0600`; the marker contains only metadata/digests.
6. Startup preflight improves confidence but cannot guarantee ports, files, DNS, credentials, or external services will remain unchanged until restart.
7. Phase 3 ships one token per principal, custom roles, and predefined `auditor`; multi-token rotation is deferred.
8. Self-revocation/demotion requires another admin-capable principal and explicit confirmation.
9. Guarded egress clients ignore environment proxies and connect directly in Phase 4.
10. Native gRPC route creation must use a true gRPC action or stay on the dedicated path; never emit a plain proxy silently.
11. `global_set` excludes ignored legacy access/error log fields.
12. `rate_limit_global_set.max_conns` remains listener-bound and lifecycle-aware.
13. `cache_set`, decomposed admin operations, and `access_log_set` are follow-ups, not hidden Phase 5 stretch goals.
14. No phone-home and no new planning database.
15. `docs/benchmarks.md` must be corrected against the current schema before it becomes the lean-budget authority; unsupported transport, compression, and rate-limit examples must not survive Phase 1.

## 87. Handoff checklist

Before starting implementation:

- [ ] Confirm the working branch still descends from the documented baseline or review intervening changes.
- [ ] Start with the first incomplete work package in sequence.
- [ ] Attach the relevant file list and test list to the implementation prompt/issue.
- [ ] Preserve compatibility fields and migration behavior.
- [ ] Run focused tests before broad suites.
- [ ] Update affected canonical docs in the same change.
- [ ] Do not reopen resolved decisions unless code evidence makes them unsafe or impossible.

## 88. Final programme outcome

After Phase 5, Jul.IA should have:

- a clear, lean, evidence-gated product strategy;
- one truthful configuration transaction for live and staged changes;
- safe exact restoration before Publish and safe planned-restart discard;
- named, least-privilege administration;
- complete outbound policy coverage for auxiliary fetches;
- structured guided lifecycle management for core entities;
- typed `global_set`, `compression_set`, and `rate_limit_global_set` operations;
- explicit design-ready follow-ups for cache, admin, and access-log tables;
- production-grade migration/deployment/rollback documentation;
- explicit lean, compatibility, evidence, and OSS boundaries.

Only then should the product spend a fixed time box on the AI Gateway MVP and use evidence to decide whether any new horizon deserves commitment.
