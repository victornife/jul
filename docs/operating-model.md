# Jul.IA project operating model

**Status:** current operating model  
**Effective date:** 2026-08-03  
**Programme authority:** #62 and the [combined repository audit](audit/combined-audit-2026-08-03.md)

> **Document roles:** ADR 0013 decides how work enters the portfolio; ADR 0014 decides the required operator/developer surfaces; `docs/operating-model.md` defines execution discipline; `docs/specs/core-gateway-completeness.md` defines the bounded product; the roadmap and #62 own current order and status.

Jul.IA is a solo-maintained, AI-assisted engineering product. The project is
optimized for architectural coherence, implementation depth, reproducible
evidence and a bounded permanent maintenance surface. It is not managed as a
customer-acquisition funnel, a feature-count competition or an attempt to mimic
every behavior of NGINX, Envoy, Caddy, Kubernetes ingress or a hosted cloud
platform.

The operating objective is:

> Build an unusually complete and trustworthy standalone edge/protocol gateway,
> close correctness and security gaps before expansion, make common operations
> safe and usable, and pursue category expansion only as bounded technical
> experiments.

## 1. Product boundary

The primary product is a **single-node, single-binary edge and protocol gateway**
with:

- HTTP/1.1, HTTP/2 and optional HTTP/3 serving;
- static content, reverse proxy and application gateway behavior;
- TLS, ACME and client-certificate authentication;
- routing, authentication, rate limiting, WAF, compression and cache;
- upstream pools, health and discovery;
- native gRPC, JSON transcoding and L4 streams;
- optional WASM extensions;
- local admin API, Console, audit, history and diagnostics;
- validated configuration, managed apply and planned restart.

The following are not required for standalone product completeness:

- fleet or hosted control plane;
- distributed cache/rate/circuit/configuration state;
- Kubernetes/Gateway API controller;
- service mesh or identity controller;
- OIDC/SAML/SCIM identity service;
- GraphQL composition;
- AI provider gateway;
- universal hot reload of every structural field.

These may become future products or experiments, but they cannot redefine the
core completion bar implicitly.

## 2. Portfolio lanes

Every issue and PR belongs to one lane.

### Lane A — correctness and security

Confirmed defects, regressions, unsafe defaults, standards violations and
misleading product contracts.

Rules:

- highest priority;
- may interrupt any later lane;
- tests and documentation change in the same PR;
- no workaround is presented as full closure;
- release is blocked while an applicable P0/P1 item remains open.

Examples: strict config decoding, HTTP/3 mTLS parity, ACME challenge truth and
cache lifecycle/conformance.

### Lane B — core gateway completeness

Bounded capabilities needed for a coherent standalone gateway after the current
correctness baseline is stable.

Current domains:

- canonical client identity/trusted proxies;
- backend peer trust;
- generic overload/resilience;
- bounded request/response policy;
- configuration authority and automation.

These require durable architecture decisions before implementation when public
schema, security boundaries or state ownership are involved.

### Lane C — selected operational enhancements

High-value changes that reduce common restart, diagnosis or operating pain
without expanding the product category.

Examples:

- admin credential rotation;
- access-log sink generations;
- selected live policy controls;
- support bundle and `jul doctor`.

Selection is value-ranked. A safe restart remains an acceptable outcome for
rare or structural changes.

### Lane D — migrations and compatibility evidence

Tools that help operators understand and move configurations without claiming
universal equivalence.

Rules:

- every source construct is accounted for;
- security-significant omissions are prominent;
- provenance and remediation are first-class;
- no one-dimensional compatibility percentage;
- no automatic cutover certification;
- no proprietary or secret-bearing fixture ingestion.

### Lane E — bounded experiments

Category-expansion work that tests a technical hypothesis.

An experiment is not a roadmap phase merely because an issue exists. It must
have:

- explicit prerequisites;
- fixed first tranche and non-goals;
- dependency/binary/runtime budget;
- deterministic local test strategy;
- maximum implementation tranche;
- mandatory outcome: **promote, freeze, extract, remove or defer**.

Only one major category-expansion experiment should be active at a time.

### Lane F — vision horizon

Long-term concepts preserved for context. Vision documents do not authorize
implementation and may remain unchanged for years.

## 3. Priority model

Priority is decided using:

1. correctness and security risk;
2. user/operational impact;
3. architecture leverage;
4. probability and frequency of the problem;
5. implementation confidence;
6. effort and permanent maintenance cost;
7. regression and migration risk;
8. evidence/learning value.

Default interpretation:

| Priority | Meaning |
| --- | --- |
| P0 | Active security, data-integrity, release-blocking correctness or materially false product contract |
| P1 | High-impact core correctness/completeness or a selected operational capability |
| P2 | Valuable quality, migration, diagnostics or product completeness work after the critical path |
| P3 | Gated experiment, low-frequency enhancement or optional architecture investment |

Impact and effort must be recorded independently. A low-effort item is not
necessarily high priority; a high-effort item is not automatically deferred when
it closes a critical trust boundary.

## 4. Work-in-progress limits

Because the repository is solo-maintained and architecture-heavy:

- one code-heavy shared-architecture workstream is active by default;
- a second workstream may proceed only when changes are demonstrably disjoint;
- documentation-only branches may be stacked ahead of dependent implementation;
- correctness/security fixes may run in parallel when they do not overlap in
  state ownership or release evidence;
- no large refactor is combined with unrelated behavior changes;
- no implementation begins from a `[DRAFT]` issue until its gate is removed;
- no `[GATED]` issue begins without an explicit go/reduce/retain/defer decision.

## 5. Source-of-truth model

Jul.IA distinguishes **descriptive truth** from **normative intent**.

### Descriptive truth — what the current binary actually does

1. observed runtime behavior and executable tests;
2. generated/checkable machine contracts;
3. current feature, reference and operational documentation;
4. the current evidence-based audit.

### Normative intent — what the product is required to do

1. accepted ADRs;
2. approved engineering specifications;
3. compatibility and security contracts;
4. the active roadmap;
5. implementation-ready issues.

When implementation contradicts an ADR, compatibility rule or security contract,
the code remains the factual current behavior but the contradiction is a defect —
not an implicit architectural override. Historical audits, year plans and vision
horizons remain context rather than current authority.

### Configuration and lifecycle

Target authority:

- Go schema and validators define runtime structure/behavior;
- the closed-world Go lifecycle registry defines field disposition;
- JSON Schema, lifecycle metadata and reference Markdown are generated or
  exhaustively checked mirrors;
- Console and CLI consume server-provided contracts;
- human guides explain concepts and operations without re-implementing rules.

### GitHub programme

- #62 is the single current execution tracker;
- parent epics organize outcomes and integrated closure;
- focused issues own implementation contracts;
- PRs contain actual code/docs/tests/evidence;
- historical issues remain evidence but do not override the current tracker.

## 6. Issue standard

Every implementation or decision issue must include:

- purpose and context;
- current evidence and affected files/components;
- classification, impact, effort and priority;
- target contract;
- scope and explicit non-goals;
- alternatives and trade-offs;
- dependencies and sequencing;
- security, lifecycle and compatibility considerations;
- observability and operator surfaces where applicable;
- acceptance criteria;
- required tests and validation commands;
- documentation changes;
- risks of changing and not changing;
- completion-evidence template.

Issue bodies are normative only after their architecture gate is complete. If
source evidence contradicts an issue, update the issue before implementation.

## 7. Pull-request model

### Scope

A PR should close one coherent contract. It may include code, tests,
documentation, generated artifacts and directly required refactors. It should not
include unrelated cleanup.

### Branching

- branches are descriptive and stable;
- stacked documentation/contract PRs are allowed when review order matters;
- code fixes should normally branch independently from `main` unless they have a
  real hard dependency;
- a stacked PR description names its base PR and retargeting plan.

### Draft status

PRs default to draft until:

- implementation and directly required docs are present;
- targeted checks are known;
- the diff has a coherent review boundary;
- open design uncertainty is visible.

### PR description

Every PR states:

- what changed;
- why and root cause;
- user/developer/operational impact;
- compatibility or migration behavior;
- exact checks run and their results;
- unavailable checks;
- issue closure/non-closure relationship.

Never claim a command passed unless it was actually run against the PR SHA.

## 8. Definition of done

A change is complete only when applicable items are satisfied.

### Behavior

- target contract implemented;
- explicit non-goals preserved;
- failure and cancellation paths handled;
- compatibility and migration behavior documented;
- no partial apply or mixed generation outside the documented boundary.

### Tests

- characterization tests for subtle existing behavior;
- success, invalid-input, failure, cancellation and concurrency tests;
- real protocol/filesystem/browser tests where mocks cannot prove the contract;
- race/leak checks for concurrent/resource-owning changes;
- build-tag/stub coverage;
- regression test named after the defect or invariant.

### Documentation

- feature guide and reference updated;
- current correction notice removed/revised;
- lifecycle/status/known limitations updated;
- examples validated;
- changelog updated when behavior changes;
- historical evidence preserved rather than rewritten.

### Operations and security

- bounded logs/metrics/status;
- no secrets or unbounded values in labels/audit/errors;
- rollback/recovery documented;
- pre-/post-Publish failure classification truthful;
- old resources retire exactly once and within their defined lifetime.

### Evidence

- exact commit/PR;
- commands and results;
- unavailable lanes;
- residual limitations;
- issue completion evidence.

## 9. Architecture-change gate

A new abstraction or state manager must show:

- at least one concrete production consumer;
- clear ownership and lifetime;
- publication and rollback boundary;
- cancellation/retirement behavior;
- bounded observability;
- build-tag/stub behavior;
- why existing focused mechanisms are insufficient;
- why the abstraction does not create a second source of truth.

Speculative generic registries, callback graphs and plugin-style runtime
self-registration are rejected by default.

## 10. Reload and runtime-transition standard

Every dynamic change documents four boundaries:

### Prepare

All fallible parsing, validation, file opening, listener binding and resource
construction occurs before publication. Candidate work is not externally
visible.

### Publish

The point of no return. Publication is bounded and no-fail by construction.
No network discovery, file parsing or lazy opening belongs here.

### Abort

Every pre-Publish failure releases candidate resources exactly once and leaves
live runtime/disk semantics unchanged.

### Retire

Old resources finish according to request, connection, listener, worker or
filesystem ownership. Retirement is bounded and cannot turn a committed change
into `not_applied`.

A safe planned restart is used when these boundaries cannot be implemented with
proportionate permanent complexity.

## 11. Documentation model

Documentation categories:

- **conceptual:** why the product behaves this way;
- **tutorial:** complete task-oriented walkthrough;
- **operational:** deployment, monitoring, recovery and limits;
- **reference:** factual fields, APIs, metrics and status values;
- **architecture/ADR:** durable decisions and trade-offs;
- **audit/evidence:** dated findings and exact verification;
- **roadmap/vision:** future direction, never current behavior authority.

Generated factual references supplement human explanations. They do not replace
architecture, security or operational prose.

## 12. Experiment governance

Before implementation, an experiment issue must answer:

- what technical hypothesis is being tested;
- which existing generic components it reuses;
- what would prove it does not belong in core;
- dependency, license, CVE, binary and idle-runtime cost;
- local fake/test infrastructure;
- privacy and telemetry boundaries;
- stop conditions and time box.

After the tranche, record exactly one outcome:

- **promote:** create a bounded supported roadmap;
- **freeze:** retain experimental code/tag with no expansion;
- **extract:** move to another binary/repository/plugin;
- **remove:** delete code/dependencies;
- **defer:** stop before implementation or archive the design.

## 13. Release and audit closure

A release closure is evidence against an exact SHA, not a collection of closed
issues.

Minimum applicable evidence:

- default and full-tag tests;
- race suite;
- frontend type/lint/unit/build/E2E;
- docs and link checks;
- generated artifact checks;
- security/dependency checks;
- real protocol and local external-service fixtures;
- deterministic failure matrix;
- soak and resource trend;
- secret/cardinality review;
- current audit and release notes.

Unavailable lanes are recorded explicitly. A prior passing run cannot be applied
to a different SHA without justification.

## 14. Current sequencing

The operating model intentionally does not duplicate the live critical path.
Current stage status, dependencies and selected work are maintained in the
[roadmap](roadmap/README.md) and master programme #62.

