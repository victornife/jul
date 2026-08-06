# Jul.IA — Combined repository re-audit and implementation baseline

**Audit date:** 2026-08-03  
**Repository:** `victornife/jul`  
**Audited branch:** `main`  
**Source baseline:** `66c71b2d48f578a770d5c6e5d86a0e5a9dcada9a`  
**Status:** current authoritative audit and implementation-planning baseline  
**Execution status (2026-08-06):** programme/audit truth, the non-cache correction tranche, #129 security-test foundations, and the #226 managed-apply terminal-order correction are included in the current baseline; CACHE-01 and CACHE-02 are corrected by #131, and #133 is next
**Supersedes for current planning:** `2026-07-31-full-repository-audit.md` while preserving that document as historical evidence

> This audit is the source of truth for the current programme. It separates shipped behavior, confirmed defects, probable defects, operational enhancements, architecture decisions and bounded experiments. A linked issue is not implementation approval when its title contains `[DRAFT]` or `[GATED]`.

---

## 1. Executive summary

Jul.IA has a strong single-node edge-server foundation: a coherent Go architecture, a transactional reload path, broad protocol coverage, a substantial automated test estate, an embedded Console and unusually detailed design documentation. The repository is not, however, ready for an unconditional release or category-expansion programme.

The re-audit found four classes of work that must remain distinct:

1. **Current correctness and security defects** that must be fixed before broader feature work: strict configuration decoding and validation, HTTP/3 server-level mTLS parity, ACME challenge-mode truth, cache lifecycle and shared-cache correctness, response-wrapper protocol transparency, compression `no-transform`, metrics-contract drift and WAF logging review.
2. **Documentation and product-contract drift** where current docs overstate or misdescribe behavior, maturity, lifecycle or feature status.
3. **Core Gateway Completeness** gaps that require explicit architecture decisions before implementation: canonical client identity, backend peer trust, generic resilience, bounded routing/response policy and configuration authority/automation.
4. **Optional runtime-dynamics and category-expansion work** that must be value-ranked and may deliberately remain restart-bound or experimental.

The recommended order is therefore:

```text
programme/audit truth
  -> current documentation truth
  -> immediate correctness/security
  -> cache recertification
  -> closed-world lifecycle authority
  -> existing typed-configuration programme
  -> Core Gateway Completeness ADRs
  -> core implementation
  -> selected runtime dynamics
  -> migration/diagnostics hardening
  -> one bounded experiment
  -> integrated release closure
```

**Implementation recommendation:** proceed, but only through the staged programme in #62. Do not treat all open issues as approved, do not begin AI implementation before the entry gate, and do not restore broad GA claims until the relevant evidence issues close.

---

## 2. Scope and methodology

The combined re-audit covered:

- Go application and library code, composition root and package boundaries;
- configuration schema, defaults, parsing, validation, secrets and lifecycle classification;
- HTTP/TLS/HTTP/2/h2c/HTTP/3, gRPC/transcoding, L4 streams and WebSocket paths;
- cache, compression, authentication, RBAC, WAF, plugins, discovery and egress;
- reload, managed apply, planned restart, rollback, resource ownership and shutdown;
- admin API and React Console behavior;
- logging, metrics, tracing, audit and diagnostics;
- unit, integration, race, E2E, soak and security-test coverage;
- build tags, packaging, release and CI workflows;
- README, guides, references, examples, ADRs, specs, roadmap, status and historical reviews;
- open GitHub issues and the previous implementation plan.

The implementation was treated as the source of truth. Previous findings were retained only when current evidence still supported them. Findings were classified as:

- **confirmed defect**;
- **highly probable defect**;
- **targeted validation risk**;
- **documentation/product-contract defect**;
- **core completeness gap**;
- **operational enhancement**;
- **bounded experiment**.

Effort uses the repository programme scale:

- **XS:** less than half a day;
- **S:** approximately one day;
- **M:** two to five focused days;
- **L:** one to two focused weeks;
- **XL:** more than two weeks or requiring decomposition.

Impact is recorded as Critical, High, Medium or Low.

---

## 3. Repository health assessment

| Area | Assessment | Current conclusion |
|---|---|---|
| Architecture | Strong foundation | Clear composition root and package seams; reload/resource ownership remains the highest-risk cross-cutting area. |
| Configuration safety | Material gap | Unknown and invalid values can be accepted or normalized too permissively; strict closed-world behavior is required. |
| Protocol security | Material gap | HTTP/3 server-level mTLS parity must be corrected and proven with real QUIC clients. |
| ACME | Contract defect | Configured challenge selection must have real exclusive runtime semantics. |
| Cache | Release blocker | Generation ownership, immutable entries, shared-cache semantics and protocol transparency require a dedicated correctness programme. |
| Observability | Mixed | Broad capability exists, but released metrics, access-log enablement and sensitive logging need reconciliation. |
| Admin/Console | Strong but contract-heavy | Good foundations; typed lifecycle authority, credential cutover and exact runtime projections remain critical. |
| Tests | Broad but uneven | Strong overall estate; security-package floors, negative tests, real protocol tests and long-running lifecycle evidence need strengthening. |
| Documentation | Material drift | Current behavior, maturity and roadmap are not consistently represented. |
| Operations | Good foundations | Planned restart, history, audit and supportability are strong concepts; diagnostics and generated contracts remain incomplete. |
| Release readiness | Not ready | Current P0/P1 correctness, security, cache and documentation-truth blockers must close first. |

---

## 4. Delta from the previous audit

The 2026-07-31 audit remains valuable historical evidence, but several of its conclusions are no longer sufficient for current planning.

### Still valid

- the repository has a coherent single-node product identity;
- the reload/apply architecture is a major strength;
- the Console and governance model are unusually mature;
- security-sensitive package coverage and negative-test depth need improvement;
- formal audit closure requires exact-SHA evidence rather than issue status alone.

### Broader or more severe than previously recorded

- cache issues are not merely missing conformance polish; they include generation lifetime and shared mutable-entry defects;
- HTTP/3 mTLS parity is a present security/correctness issue, not only a future dynamic-TLS concern;
- ACME challenge configuration is a current runtime-contract defect;
- lifecycle classification cannot remain a manually synchronized code/YAML/documentation model;
- configuration parsing and scalar validation need a closed-world guarantee across every entry path;
- documentation drift includes protocol security, cache GA, static-certificate rotation, access-log semantics, metrics and roadmap sequencing.

### Superseded planning assumptions

- Phase 5 no longer automatically opens an AI implementation phase;
- universal hot reload is not the target; a value-ranked selected tranche is;
- the lifecycle authority must land before Phase 5 preview;
- backend trust and resilience must be generic gateway capabilities before any AI provider layer;
- generated config metadata and external automation must derive from one server-side contract;
- migration value is measured through explicit findings and selected-dimension E2E, not a compatibility percentage.

---

## 5. Confirmed bugs and probable defects

| ID | Finding | Classification | Impact | Effort | Primary issue |
|---|---|---|---|---|---|
| CFG-01 | Unknown TOML fields are not rejected consistently across all configuration entry paths | Confirmed defect | High | M-L | #120 |
| CFG-02 | Invalid enums, durations, workers and scalar values can be normalized or accepted instead of failing | Confirmed defect | High | M | #123 |
| TLS-01 | HTTP/3 does not have proven equivalent server-level client-auth enforcement to TCP TLS | Confirmed security defect | Critical | M-L | #121 |
| ACME-01 | `acme.challenge` does not reliably select one exclusive runtime challenge mechanism | Confirmed contract defect | High | M-L | #122 |
| CACHE-01 | Background stale revalidation can outlive the handler generation/resources it uses | Confirmed lifecycle defect | Critical | L | #131 (corrected) |
| CACHE-02 | Published cache entries can be mutated in place during stale/error handling | Confirmed concurrency defect | Critical | M-L | #131 (corrected) |
| CACHE-03 | Shared-cache directive, authenticated reuse, invalidation and `304` behavior are incomplete | Confirmed standards defect | High | L-XL | #132 |
| CACHE-04 | Cache wrappers do not prove transparent WebSocket/upgrade/stream behavior | Confirmed/probable protocol defect | High | L | #133 |
| COMP-01 | Compression does not consistently respect `Cache-Control: no-transform` | Confirmed HTTP defect | Medium-High | S-M | #125 |
| OBS-01 | Current metrics/docs may drift from the last released Prometheus contract | Contract/compatibility defect | High | M-L | #126 |
| OBS-02 | Access-log disablement relies on ambiguous sink semantics and ignored legacy fields | Confirmed product-contract defect | Medium-High | M | #124 |
| WAF-01 | URI/query logging requires bounded redaction and cardinality review | Targeted security/privacy validation | High | M | #127 |
| LIFE-01 | Unknown lifecycle paths can be classified too permissively and code/docs can drift | Confirmed systemic defect | High | L | #89 |
| DOC-01 | Current protocol, cache, lifecycle, RBAC, metrics and roadmap claims contradict code or current programme | Confirmed documentation defect | High | M | #119 |

### Conservative behavior selected by design

- Range and If-Range requests bypass cache initially; cached byte ranges remain a future upgrade candidate.
- `no-cache` responses may be stored, but every reuse requires synchronous validation.
- Structural settings may remain restart-required when the staged-restart path is safer than permanent runtime complexity.

---

## 6. Architecture and code-quality findings

### 6.1 Closed-world lifecycle authority

The Go lifecycle registry must become the machine authority for every public configuration leaf. Generated/checkable YAML, Markdown and machine metadata are human and tooling mirrors, not independent sources. Unknown paths must fail safely instead of defaulting to hot reload. See #89 and decision D07.

### 6.2 Resource ownership by lifetime

Jul has several distinct lifetime classes:

- immutable/atomic process state;
- request/handler generation resources;
- transport/connection generations;
- listener generations;
- background workers;
- persistent filesystem backends.

These must not be forced through one open generic registry. The selected prepared-runtime mechanism in #90 uses a closed component set and is driven only by concrete resource consumers such as certificate providers and access-log sinks.

### 6.3 Canonical client identity

Inbound socket identity, trusted forwarding headers and application-facing client identity need one explicit contract. The accepted direction is per-server trusted proxies, Forwarded-first/X-Forwarded-For fallback, right-to-left trust evaluation, a canonical request-context identity and preservation of the direct peer. See #115, #135 and #136.

### 6.4 Backend peer trust

Backend TLS, private roots, client certificates, SNI and peer identities need one normalized internal policy shared by named upstreams, literal targets, HTTP, native gRPC, transcoding/reflection and health checks. Named reusable profiles are deferred, but consumers must use the normalized type so profiles remain additive. See #109 and #137-#140.

### 6.5 Generic resilience

Connection/request admission, bounded pending work, retry budgets/deadlines/backoff and circuit state must be generic upstream primitives. AI/provider work may consume them later but may not create a parallel engine. Accepted sequence: limits -> retries -> simple circuit -> evidence-gated outlier ejection. See #110 and #141-#144.

### 6.6 Bounded routing and response policy

Core completeness includes method/header/query matching, protected response-header operations and CORS. It excludes a general expression language, body matching, arbitrary scripting, canary and mirroring. See #117 and #145-#147.

### 6.7 Configuration authority and automation

One writer owns desired state at a time:

- `managed`: Jul owns persistence/history and detects external drift;
- `file_owned`: file/GitOps is authoritative and remote/Console mutation is read-only.

Authority transitions are restart-bound. JSON Schema, lifecycle/capability metadata, factual reference docs, external OpenAPI and remote CLI must derive from the same server contracts. See #118 and #148-#151.

---

## 7. Testing and quality-gate findings

The repository already has broad unit and integration coverage, but the following evidence is mandatory before release closure:

- strict parser/validator parity across startup, CLI, raw apply, patch apply, stage, rollback and importer;
- real HTTP/3 mTLS clients and deterministic ACME challenge tests;
- cache race/leak/reload tests with blocked revalidation;
- real WebSocket, SSE, H1/H2 and upgrade behavior through wrappers;
- generated lifecycle/config/API artifact drift checks;
- dedicated security-package floors and negative-test gates for plugins, WAF and RBAC;
- exact metric-name/label compatibility tests;
- browser E2E for credential, lifecycle and pending-restart flows;
- deterministic fault injection for admission, retry and circuit interactions;
- build-tag matrix across lean and full profiles;
- long-running soak with goroutine, FD, socket, heap and resource-retirement trends.

No command should be recorded as passing unless it was run against the exact commit being certified.

Issue #129 adds a separate `Security package gates` workflow and a reproducible
local command for `internal/rbac`, `internal/waf`, and `internal/plugins`. The
machine-readable manifest records exact full-tag baselines and independently
enforced floors; missing packages and malformed profiles fail closed.

Issue #226 closes the scheduler-dependent gap between terminal managed-apply
visibility and mutation admission. Restoration and final disk truth complete
before the in-flight and server-finalization gates are released; only then may
the serialized history/audit/metrics/ledger finalizer publish terminal state.
A terminal record is therefore a reliable signal that the next valid apply can
be admitted, while managed finalization side effects remain strictly ordered.

---

## 8. Documentation findings and target information architecture

### Current documentation defects

Documentation must immediately stop overstating:

- equivalent TCP/HTTP3 server-level mTLS;
- reliable exclusive ACME challenge selection;
- static certificate/key hot reload on retained listeners;
- cache GA/conformance while correctness defects remain open;
- access-log disablement through an empty sink list;
- RBAC as entirely future work;
- Prometheus names/labels before contract reconciliation;
- AI as the automatic next implementation phase;
- universal hot reload as a completeness requirement.

### Target information architecture

- `docs/audit/combined-audit-2026-08-03.md` — current audit and implementation baseline;
- `docs/audit-register.md` — historical finding/evidence register plus current-audit pointer;
- `docs/status.md` / feature-status source — current maturity and release evidence;
- `docs/roadmap/README.md` — active operating roadmap;
- `docs/operating-model.md` — contribution, portfolio and evidence model;
- `docs/specs/core-gateway-completeness.md` — bounded standalone product contract;
- generated lifecycle/config/OpenAPI references — factual machine-derived contracts;
- feature guides — conceptual and operational behavior;
- historical audits/specs — preserved with explicit historical banners.

Documentation changes are part of each implementation issue's definition of done, not a final cleanup phase.

---

## 9. CI/CD, dependency, security and operational findings

- CI must fail on stale generated lifecycle/config/API artifacts and provide the exact regeneration command.
- Strict parsing and validation tests must be shared across every configuration entry path.
- Released metrics require a checked inventory and compatibility decision.
- Security-package coverage floors and negative tests must be raised independently from the global floor.
- Dependabot and `govulncheck` results require evidence tied to the exact release SHA; accepted advisories need durable rationale.
- Support bundles and `jul doctor` must be operator-triggered, read-only by default, bounded and secret-safe.
- NGINX migration must report every parsed directive with provenance and risk; no universal compatibility percentage or automatic production certification.
- No phone-home, automatic upload or user tracking is introduced by diagnostics or experiments.

---

## 10. Existing GitHub issue assessment

The current programme is tracked by #62. The new issue set #107-#162 maps every material combined-audit finding to an implementation issue, ADR, parent acceptance criterion, explicit non-goal or gated limitation.

Key parent issues:

- #107 cache correctness;
- #108 Core Gateway Completeness;
- #109 backend peer trust;
- #110 generic resilience;
- #111 configuration authority and automation;
- #112 migration and diagnostics;
- #88 selected runtime dynamics;
- #113 bounded technical experiments.

Existing Phase 5 issues #77-#82 remain valid but are resequenced behind #89 and applicable correctness work. Existing runtime-dynamics issues #88-#106 are reclassified into selected, candidate, draft/gated or closure work. Historical QA issue #53 must be re-evidenced rather than duplicated.

---

## 11. Consolidated findings register

The detailed implementation contracts live in their linked issues. The current portfolio register is:

| Programme | Issues | Priority | Outcome required |
|---|---|---|---|
| Documentation truth | #119, #130 | P0/P2 | Current audit registered; current behavior described accurately. |
| Configuration safety | #120, #123, #124 | P0/P1 | Strict decode/validation and explicit access-log model. |
| Protocol/PKI correctness | #121, #122 | P0/P1 | H3 mTLS parity and truthful exclusive ACME challenge modes. |
| HTTP/observability correctness | #125-#127 | P1 | `no-transform`, released metrics, bounded WAF logging. |
| Security-test foundations | #129, #53 | P1/P2 | Dedicated floors, negative tests and residual QA evidence. |
| Cache correctness | #107, #131-#134 | P0 | Safe lifetimes, immutable entries, conformance, protocol transparency and recertification. |
| Lifecycle authority | #89, #128 | P0/P1 | Closed-world registry and generated/checkable mirrors. |
| Phase 5 typed config | #77-#82 | P1 | Atomic typed CRUD/settings, UI and real-server closure. |
| Client identity | #115, #135-#136 | P1 | Trusted proxy parsing and canonical identity adoption. |
| Backend trust | #109, #137-#140 | P1 | One peer-trust policy across live traffic and health. |
| Resilience | #110, #141-#144 | P1 | Bounded limits, retries, circuit and integrated evidence. |
| Routing/response | #117, #145-#147 | P1/P2 | Bounded matching, headers/CORS and typed closure. |
| Authority/automation | #111, #118, #148-#151 | P1/P2 | One writer, generated contracts, external API and CLI. |
| Runtime dynamics | #88-#106, #157-#161 | P1-P3 | Implement selected tranche; retain/defer structural work truthfully. |
| Migration/diagnostics | #112, #152-#156 | P2 | Provenance, corpus, support bundle and doctor. |
| AI experiment | #113, #162 | P3 | Design/time-box first; promote, freeze, extract, remove or defer. |

---

## 12. Issue creation and update matrix

The issue-creation phase is complete in GitHub. Implementation rules:

- issues with `[DRAFT]` may not start until their governing ADR is merged and the body is synchronized;
- issues with `[GATED]` require an explicit implement/reduce/retain/defer decision;
- each implementation issue includes impact, effort, dependencies, risks, acceptance criteria, tests, documentation and completion evidence;
- parent epics close only after integrated evidence, not because child issue boxes were checked;
- new findings discovered during implementation are added to this audit and mapped before work expands silently.

---

## 13. Updated phased implementation plan

### Stage 0 — programme reconciliation

Complete: issue hierarchy, decisions D01-D16, selected/gated split and master tracker.

### Stage 1 — audit authority and current truth

1. Register this combined audit and preserve the July audit as historical.
2. Correct current product/capability/security/lifecycle/roadmap claims (#119).
3. Establish the operating model and Core Gateway Completeness documents (#114/#108).

### Stage 2 — immediate correctness and security

- #120 strict unknown-field decoding;
- #121 HTTP/3 mTLS parity;
- #125 compression `no-transform`;
- #122 exclusive ACME challenge selection;
- #123 invalid-value rejection;
- #124 access-log enablement;
- #126 released metrics reconciliation;
- #127 WAF logging review;
- #129 security coverage and negative gates.

Independent fixes may proceed in parallel when files and architecture do not overlap.

### Stage 3 — cache correctness

#131 -> #132 and #133 -> #134. #92/#93 remain blocked until recertification closes.

### Stage 4 — lifecycle authority

#89 establishes schema inventory, exact classes, fingerprints and generated mirrors. #128 later orchestrates cross-artifact drift checks.

### Stage 5 — existing Phase 5

#77 -> #78 -> #79 -> #80 -> #81 -> #82.

### Stage 6 — Core Gateway Completeness decisions

#114, #115, #116, #117 and #118.

### Stage 7 — core implementation

Client identity, backend trust, resilience, routing/response policy and authority/automation programmes.

### Stage 8 — selected runtime dynamics

Implement the value-ranked tranche; record retain/defer decisions for structural work. #106 closes selected work only.

### Stage 9 — migration, diagnostics and hardening

#152-#156 plus final security/quality evidence.

### Stage 10 — one bounded experiment

#162 may begin only after its trust, resilience, streaming and dependency-budget gates pass.

### Stage 11 — integrated closure

Fresh source re-audit, exact-SHA command matrix, real protocol suites, build-tag matrix, browser E2E, failure injection, race/leak and soak evidence, final release notes and known limitations.

---

## 14. Material changes from the previous plan

- lifecycle authority moves before Phase 5 preview;
- AI is no longer an automatic next phase;
- cache correctness precedes cache evolution or GA reaffirmation;
- current H3 mTLS and ACME challenge defects are separated from optional dynamic TLS/ACME architecture;
- universal hot reload is replaced by a selected value-ranked tranche;
- structural settings may deliberately retain restart semantics;
- backend trust and resilience are generic prerequisites for provider experiments;
- configuration authority and generated external contracts are explicit core work;
- NGINX migration uses findings/provenance/evidence rather than a compatibility score;
- documentation truth is an immediate stage rather than an end-of-programme cleanup.

---

## 15. Open decisions, assumptions and residual risks

Open architecture decisions are owned by #114-#118. Material residual risks include:

- incorrect lifecycle inventories can cause unsafe hot-apply claims;
- cache fixes can alter origin traffic and latency;
- connection-generation work can leak or prematurely terminate traffic;
- generated contracts can become another source of truth if not code-derived;
- Console/API/config converters can drift on ordering and sparse-value semantics;
- migration reports can overstate equivalence;
- diagnostics can leak sensitive state if structural exclusion and redaction are incomplete;
- provider experiments can create a second gateway architecture if prerequisites are bypassed.

The audit baseline identifies code and issue relationships, but exact release closure must be re-run on the final merged SHA. No current document should infer a future command result or external service behavior.

---

## 16. Readiness recommendation

**Ready to begin implementation through the staged plan: yes.**  
**Ready for unconditional release or feature-expansion sequencing: no.**

The immediate implementation order is:

1. audit registration and current documentation truth;
2. operating model/Core Gateway Completeness documentation;
3. strict TOML decoding;
4. HTTP/3 mTLS parity;
5. compression `no-transform`;
6. ACME challenge selection;
7. remaining Stage 2 correctness work;
8. cache correctness and lifecycle authority.

Every PR must update tests and directly affected documentation, preserve explicit non-goals and record actual validation evidence. This document remains current until a later dated audit explicitly supersedes it.
