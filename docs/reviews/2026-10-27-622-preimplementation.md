# CACHE-OPS-02: source baseline, proposed contract and acceptance ledger

Status: **pre-implementation; permission decision pending**. References #622,
parent #620 and selection authority #62. This document is a reviewable proposal,
not an accepted ADR, shipped capability, completed acceptance matrix or release
record. No runtime, public API, default grant or retention behavior is changed
by this commit. Continue implementation on the same branch after the narrow
decision below; do not treat this preparation as satisfying #622.

## Source and current truth

- Clean `main`: `78b458b84d8b6015f64d86191b3bd7c157ffbb11`.
- Branch: `feat/622-cache-inspection`.
- #621 is closed and implemented through merged PR #665 at that baseline:
  **Beta / implemented**, without a release, GA or soak claim.
- Read current bodies and comments for #62, #620, #621, #622, #623, #625,
  #586, #666, #667 and #668. Earlier #621 implementation comments predate
  merge; its latest closure comment and current issue bodies supersede them.
- No open PR was returned by the repository PR search at selection time.
  No conflicting selected shared-architecture successor appears in #62's
  current checkpoint. Selection of #622 is explicitly authorized by the prompt.
- Read CONTRIBUTING, vision README, operating model and ADRs 0013, 0014, 0027.
- Source tracing: cache lookup/publication, corrected-age/freshness owner,
  synchronous/background revalidation, fill locking, streamed capture,
  disk rehydration and metadata catalog; RBAC permission/role matching;
  admin purge handlers and route catalog; shared DTOs, client and CLI;
  lazy Console invalidation integration.

## Decision requiring maintainer input

The attached implementation prompt requires: **"Ask before making an uncertain,
consequential one-way-door decision"**, explicitly including permanent API or
permission semantics and changes to default grants. #622 requires an explicit
inspection permission but does not decide which existing principals receive it.

### Recommendation: separate explicit inspection grant

Add `cache:inspect` to the existing permission catalog. Require it for every
inspection request and page. Leave built-in viewer, operator and auditor
permission lists unchanged; administrators retain existing `*` semantics.
Existing `cache:*` custom grants cover the new action through the existing
resource-wildcard matcher. Legacy single-token mode retains its established
all-actions authority. Exact `cache:purge` does **not** imply inspection;
`cache:inspect` does **not** imply purge or preview.

Consequence: existing built-in operators can still purge and preview under
#621, but need an explicitly assigned custom inspection role to use the full
inspector. This avoids silently widening a predefined role's ability to
enumerate cached paths and tags. It is intentionally less convenient than
granting all existing operators inspection by default.

Alternative: grant `cache:inspect` to the built-in operator role as an explicit,
reviewed compatibility change. This improves discoverability but gives existing
operator principals a new generic enumeration capability on upgrade. Granting
viewer/status readers inspection or treating purge as inspection is not
recommended.

**Requested answer:** approve the explicit-grant recommendation, or choose the
built-in-operator grant alternative. Do not interpret selection, branch/push/PR
authorization or this proposal as approval of either grant policy. Pause the
affected external API/RBAC implementation until answered. This is not a request
for merge or issue-closure authorization.

## Proposed bounded design

These are implementation recommendations to validate, not measured guarantees.
Use a feature ADR for the accepted final contract and document any substantive
deviation before publishing the external surface.

### Enumeration and cursor ownership

Prefer bounded detached metadata snapshots over per-tier live iteration.
Reuse `ParseSelector`, `NormalizedSelector.MatchesResource`, the shared
`keyGen`/`byHost`/variant ownership catalog and `SafeResourceSummary` projection.
Do not build a second index, load bodies or call tier `get` methods.

| Option | Benefit | Material cost / decision |
| --- | --- | --- |
| Bounded detached snapshot | Stable pagination within a captured bounded result; no response leases; no response-body I/O on later pages | Retained safe metadata requires aggregate byte/count/TTL limits and principal binding; recommended |
| Bounded per-tier iteration | Less retained page state | Current tier/catalog maps do not supply stable bounded seek positions; rescanning can repeat/omit entries under churn and multiply lock work; reject for first implementation |
| Global immutable cache snapshot | Globally consistent full enumeration | Would require unbounded copying/locking or a new persistent index; reject |

Proposed limits: 10,000 catalog candidates examined, at most 256 logical
resources retained per snapshot, 50 resources per page, 16 concurrent retained
snapshots and 8 MiB aggregate encoded safe metadata. Expire snapshots after
60 seconds and reclaim independently of subsequent inspection traffic. Bound
concurrent builders to two; excess admission returns a retryable rate-limit
error before scanning. Bound representation detail as well as resource count:
do not let 64 Vary variants times two methods defeat the byte budget. Report
both candidate truncation and detail truncation explicitly.

Keep each published snapshot immutable. Cursor records refer to opaque random
snapshot identity and page position, never executable raw query input. Bind
them to normalized selector digest, current configuration scope identity,
authenticated principal and applicable authorization context. Reauthenticate
before lookup and before delivering each result; reject scope/principal
mismatch, revocation, reload invalidation and expiry. A cursor is not a grant.
No raw URI/query needs to be retained in the snapshot or token.

Cancellation must be checked before admission, before and during bounded
catalog work, and before response publication. Cancellation leaves no retained
snapshot. Locks cannot make cancellation instantaneous: measure wait/hold time
and avoid promising that a mutex acquisition itself is cancelable. Establish
lock ordering from existing publication/eviction paths before adding locks.

State that rows are captured metadata observations, not a global transaction
across tiers, configuration, occupancy and request decisions. Later eviction,
reload or purge can invalidate their relevance. Show capture time, expiry and
concurrent-change information; sequence alone must not be described as detecting
all tier churn unless the implementation proves it does.

### API/CLI/Console proposal

- One external, read-only `POST /api/v1/cache/inspect` operation accepts the
  existing selector DTO, page size and optional cursor in a bounded JSON body.
  POST keeps sensitive exact-match input out of URL query parameters; it does
  not authorize mutation. Use the existing read rate-limit class and auth/CSRF
  contracts. Register DTOs/route authority and regenerate OpenAPI.
- Require a configured host/route; reject whole-node `all`. `ParseSelector`
  currently resolves configuration only when `route_id` is provided. Add an
  inspection-specific configured-scope check without changing purge semantics.
  Named hosts, literal ports, wildcard/default-server ambiguity and route path
  scope must be tested against actual routing contracts.
- Exact lookup reuses #621's bounded `exact_url`/key input parsing, query
  redaction and matching. Do not add header-value/body input or raw-query
  output. A materially different sensitive-input contract requires approval.
- The response has safe resources, bounded representation/tier details,
  occupancy versus configured caps, server-owned policy/provenance, observed
  decisions, count accuracy, result/evidence states, warnings and next cursor.
  Empty arrays stay arrays. Separate a missing resource from absent evidence.
- `jul cache inspect` uses the shared admin client and JSON contract. Human
  output names count accuracy, evidence age and limitations. JSON preserves
  machine-readable error/retry/cursor semantics; never automatically rescan on
  expiry or hide partial pages.
- Lazy-load the inspector in the existing Traffic Controls cache drawer.
  Consume server decisions and documentation references. Keep filters and
  cursors in transient component memory; clear results on credential/scope
  changes. Abort superseded requests and reject late responses.
- Continue using `CacheInvalidationPanel` and existing exact-key/full/scoped
  preview-confirm-execute permissions. Inspection access does not enable a
  destructive action or synthesize a second purge endpoint.

### Metadata and effective policy

`Entry.CreatedAt` is corrected origin generation time (`now - initialAge`),
**not insertion time**. The catalog currently copies created/expiry/stale/Vary
metadata, and disk rehydration restores that subset. Do not relabel it.
Add bounded owning metadata for insertion/validation time and policy provenance
at publication/304 replacement; propagate it through catalog and disk metadata
without copying bodies. Older disk formats explicitly lack new evidence and
remain readable under existing cache semantics.

Define logical resource = host + exact request URI, representation = owned
method/Vary member, tier copy = resident memory or disk ownership. A Vary stub
is lookup metadata, not a response representation. Deduplicate copies while
reporting physical occupancy and per-tier bytes separately. Query-distinct
resources may share the same redacted displayed path; use opaque correlation
within the bounded evidence context, not a plain predictable query hash.

Capture origin policy through the existing response-policy/freshness owners.
Return closed safe provenance codes and numeric effective windows, not raw
headers or an independent browser evaluator. Distinguish s-maxage/max-age/
Expires/fallback, targeted directives, validation requirements, configured
limits, disabled capabilities and unsupported route overrides (#623).
An existing entry can retain prior computed freshness after reload; show the
entry's recorded policy versus current configuration without claiming every
entry is purged/recomputed. Stale-if-error and SWR have different decision
owners and cannot be merged into one absolute expiry guarantee.

### Actual decision evidence

Recommended first mechanism: opt-in memory-only recent decisions, disabled by
default, fixed 256-record capacity and 60-second expiry, at most 256 KiB of
retained safe metadata. Add a boolean configuration leaf through validation,
lifecycle and generated contract owners; disable/reload clears retained samples
and cancels reclamation work. Sampling availability is separate from metadata
availability. No persistent ledger, telemetry exporter or live probe is added.

Record only closed outcome/reason codes at actual owners: reuse decisions,
synchronous/background validations, header storage eligibility, capture
abandonment/disk errors, fill-lock fallbacks and actual capacity eviction.
Never infer a reason from an absent entry, final HTTP code or X-Cache alone.
Distinguish a reuse restriction from a completed not-stored outcome. A single
request can produce multiple bounded stage events; establish the per-request
bound and aggregation before implementation. Record an attempt as an attempt,
not a completed storage success.

Correlate exact resources with a process-local random-key HMAC of canonical
resource identity; expose neither that key nor raw query/Vary values. Retain
only bounded safe host/path display, reason, stage, time and correlation.
Reject oversized display input or omit its display rather than truncate into
an ambiguous asserted identity. Do not retain Authorization, cookies, body,
request header values, principal/user identity, credentials or raw errors.
Sampling is lossy and never a request history, proof of non-occurrence or
guarantee of a future HIT. No new high-cardinality metric labels.

## Acceptance matrix

All implementation/test rows below are **pending**. Paths identify semantic
owners and planned verification, not code already added or tests already run.

| Criterion | Implementation / semantic owner | Required tests and performance evidence | Docs / contract | Current evidence / gap |
| --- | --- | --- | --- | --- |
| Configured host/route pagination and exact lookup | `internal/cache` selectors/catalog; admin active config | Host/port/route/wildcard isolation; all rejected; exact query match | API, CLI, cache guide; generated OpenAPI | Parser traced; inspector pending |
| Resource / representation / tier distinction | Catalog ownership; shared safe projection; tier stats | GET/HEAD, Vary stub/membership, dual copies, large disk objects | Cache/count semantics; Console labels | Existing ownership traced; detail pending |
| Correct size and insertion/validation age | Publication, `Entry`, catalog, disk metadata | Deterministic clock, corrected Age, 304, replacement/restart | DTO units and unknown timestamps | `CreatedAt` distinction confirmed |
| Freshness, stale windows, safe Vary names/tags | Existing policy/reuse/validation owners | Boundaries, mandatory validation, SIE vs SWR; supported tag limits | Policy provenance/reason table | Current fields traced; new provenance pending |
| Origin/fallback/caps and inherited/route policy | Policy owner + current active config projection | Precedence, targeted directives, unsupported overrides, disabled routes | Policy guide, config and limitations | #623 remains separate |
| Retained entries after reload | Immutable recorded entry policy + current config | Reload/rollback, old entries, concurrent replace/revalidation | Reload semantics, stale evidence | Cache survives reload; no new cutover claim |
| Fresh hit, stale reuse, validated reuse | `reuseDecision`, `serve`, `validate`, background refresh | Owner-linked tables; actual success vs attempted reuse | Closed observed reason codes | Existing owners identified |
| Private/no-store/cookie/auth and protocol bypass | Header eligibility, request policy, upgrade/range/SSE owners | Restriction tables, range-enabled/disabled, SSE, unsafe methods | Security/limitations; next actions | Instrumentation pending |
| Oversize/capture/disk failure and fill fallback | Capture/streaming, fill and publication owners | Actual errors/cancel/budget/disabled tier/timeout/follower paths | Reason contract and unavailable evidence | Instrumentation pending |
| Eviction observation | Actual memory/disk capacity eviction callbacks | Replacement vs eviction; overflow; failure; exact scope correlation | Evidence limitations and tier semantics | No eviction history currently claimed |
| Opt-in bounded expiring evidence | Cache-owned bounded ring and lifecycle | Capacity/bytes/TTL, disable/reload/restart, reclamation, races | Config/lifecycle/schema generation, retention/threat notes | Mechanism proposed; not implemented |
| Metadata useful without sampling | Inspector independent of ring | Off, no-record, expired, dropped sample; absence != never cached | API/CLI/Console state vocabulary | Pending |
| Network-free, read-only inspection | Catalog-only path | Origin counter zero; no population; LRU order/caps/reader lease unchanged | API contract, cache guide | Existing preview establishes reusable pattern |
| Explicit permission boundary | RBAC catalog and existing matcher | Purge-only denied; inspect-only purge denied; legacy/wildcards | RBAC/security/API/Console docs | **Maintainer grant decision pending** |
| Every page reauthorized and context-bound | Admin live auth; retained snapshot ownership | Cross-principal/scope reuse, credential rotation/revocation during admission | Auth/cursor/retry error contract | Pending decision and implementation |
| Empty/disabled/unavailable/legacy/partial/truncated/churn | Server-owned result states | Disk legacy/unavailable vs absent; injected I/O/cancel; per-detail truncation | DTO enums; all three presentations | Pending |
| Query/Vary/secrets do not leak | Shared safe projection, opaque keyed correlation | Query/auth/cookie/Vary sentinels in response/errors/audit/logs/storage | Security, observability, support guidance | Reuse #621 redaction; new surfaces unverified |
| Existing purge remains usable | Existing invalidation panel/API/client/CLI | Exact key/full/scoped/retry/confirmation grants regression | Console/admin/CLI examples | Existing baseline tests pass; integration pending |
| Accessible Console and usable automation | Lazy drawer panel and shared client | Unit/coverage, browser/real-server, keyboard/a11y, errors and stale results | Console/CLI/docs links and runnable example | No frontend changes yet |
| Bounded overhead, cancellation and consistency | Shared catalog + bounded snapshot/ring ownership | Declared entry/variant scales, p50/p95, allocations/bytes, retention; race/purge/reload | Limits and benchmark record | Initial historical preview baseline below |
| Complete generated/public contracts | Owning config/lifecycle/API and CLI help | Generation/config/docs checks; error/compatibility tests | Cache/Console/API/CLI/config/observability/diagnostics/security/limits, Unreleased | Pending; no generated edits yet |
| Coverage and platform gates | Existing quality machinery | Final Codecov patch >90%; move-aware Go >=90%; separate Console floors; lean/full/race/platform | Final review ledger with report links/counts | Final-head coverage entirely unverified |
| Independent adversarial review | Two non-implementing read-only agents/reviewers | Security/privacy; correctness/resilience/completeness; SHA-bound re-review | Reviewer identities/findings/dispositions | Not run; preparation is not implementation review |
| Truthful issue/PR status and closure | #622/#620/#62, relevant downstream only | Verify actual reviewed/merged SHAs and hosted evidence | Final acceptance matrix and inherited limits | Selected/preparing; no completion/merge/closure |

## Baseline commands and evidence

Environment: Go **1.27.3 linux/amd64**, Linux 6.18.55, Intel Xeon Platinum 8573C;
benchmark Go runner selected `GOMAXPROCS=8`. Git 2.51.1, Node v24.22.1,
npm 11.9.0, available pnpm 11.25.0 (repository requests pnpm 11.8.0).
Toolchain was installed in temporary scratch; no repository dependency or
toolchain requirement changed. No `gh` or Docker executable was found.

Commands below use `PATH=/tmp/jul-toolchain/go/bin:$PATH` in this environment.

| Command | Result at unchanged baseline |
| --- | --- |
| `go test ./internal/cache ./internal/adminapi ./internal/rbac` | PASS |
| `go test ./internal/admin ./internal/adminclient ./cmd/jul` | PASS |
| `go test -tags 'brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf' ./internal/cache ./internal/adminapi ./internal/rbac` | PASS |
| `go test -tags 'brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf' ./internal/admin ./internal/adminclient ./cmd/jul` | FAIL overall; admin/client PASS; CLI fails `TestNGINXCorpusHashAffinityRealE2E` and Unix-socket subtests of `TestNGINXCorpusAssessmentCandidateAndRealJul`: `listen unix ...: socket: operation not permitted` in this environment |
| `go test -race ./internal/cache` | PASS |
| `make ci-fast` | FAIL before tests: `golangci-lint` executable missing (Makefile lint, error 127); format-check completed first |
| `go test ./internal/cache -run '^$' -bench '^(BenchmarkCacheHit\|BenchmarkCacheVaryHit\|BenchmarkScopedPurgePreviewLargeSelection)$' -benchmem -benchtime=250ms -count=3` | PASS; source-bound results below |
| `python3 scripts/docs-check.py --help` without the temporary Go PATH | FAIL: checker has no help mode and attempted CLI authority loading; `go` was not on PATH. Correct invocation below supersedes this attempt |
| `PATH=/tmp/jul-toolchain/go/bin:$PATH python3 scripts/docs-check.py` | PASS: 2,525 checks, zero failures |
| `python3 -m unittest discover -s scripts -p test_docs_check.py` | FAIL/no evidence: zero tests discovered; this script uses its own runner, not unittest discovery |
| `python3 scripts/test_docs_check.py` | PASS (exit 0, final `OK`; printed failure examples are expected negative fixtures) |
| `git diff --check` | PASS |

No final-head Codecov, added/edited production-statement, Console coverage,
Windows/macOS, browser/accessibility, `make ci-pr`, `make console-check` or
`make test-race` result exists for an implementation. Do not substitute these
baseline results for those gates. Missing lint is an environment preparation
item, not a request to weaken/bypass checks. Install required tools before the
implementation gate run.

Baseline measurements (three samples; illustrative local observations, not
portable budgets or proof of inspection performance):

| Existing benchmark / declared fixture | ns/op, three samples | Bytes/op | Allocs/op |
| --- | --- | --- | --- |
| CacheHit; one primed resource | 1,654 / 1,882 / 1,691 | 1,256 | 16 |
| CacheVaryHit; one resource, one gzip representation | 2,435 / 2,408 / 2,434 | 1,504 | 25 |
| ScopedPurgePreviewLargeSelection; 2,000 memory entries, 20 samples | 1,550,591 / 1,491,403 / 1,518,445 | 900,613–900,616 | 8,062 |

Before accepting the new implementation, add declared 0/256/2,000/10,000+
resource and 1/64-variant fixtures, memory/disk/copy cases, enabled/disabled
sampling and active inspection load. Repeat baseline/final benchmarks under
the same environment without competing build/test load. Proposed initial
regression check: disabled sampling adds zero hit-path allocations and no
statistically material >5% median warm-hit regression; if noise cannot support
that conclusion, extend runs rather than call it a pass. Set measured
inspection/lock-latency budgets using those fixtures before readiness, as the
prompt requires. Snapshot/ring caps above are proposed hard resource limits,
not latency claims.

## Inherited limits, migration and completion boundary

- #666 (open): unfinished/canceled purge protection does not automatically
  survive restart. Inspection must explain operation uncertainty; it does not
  implement recovery or claim an absent entry stayed invalidated across boot.
- #667 (open, P2): large purge lock/persistence latency and budgets remain a
  separate investigation. Measure inspector contention against that reality;
  do not fold in a purge algorithm rewrite or claim all lock waits are bounded
  by inspector scan limits.
- #668 (open, P3): legacy void purge outcome/durability consistency remains
  separate. Reuse its existing workflow without inventing a structured success.
- #623 route policy, #624 configuration cutover, #625 cross-Console catalog
  and #586 sharing diagnostics retain their own scope. No new dependency is
  inferred and no related issue is marked delivered by this proposal.
- Proposed NGINX migration disposition: **not applicable** for the new
  Jul-specific inspection/evidence management interface, provided existing
  request/cache semantics remain unchanged. Reassess actual implementation:
  any changed imported cache behavior requires its own supported/approximated/
  blocking classification and applicable corpus/E2E evidence.
- No live probe, origin traffic, persistence of recent decisions, new
  destructive operation, default role widening, release or maturity promotion
  is authorized by this proposal.
- Pre-merge endpoint remains a verified implementation PR ready for maintainer
  review, with merge and issue closure pending. This preparation does not meet
  that endpoint. After actual merge, record merge/acceptance/final CI evidence
  and request approval before manually closing #622.
