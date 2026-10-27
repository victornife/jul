# CACHE-OPS-02 implementation and verification checkpoint

Refs [#622](https://github.com/victornife/jul/issues/622), parent
[#620](https://github.com/victornife/jul/issues/620), programme
[#62](https://github.com/victornife/jul/issues/62),
[PR #669](https://github.com/victornife/jul/pull/669).

**Implementation published; final verification in progress. This checkpoint is
not a merge-readiness, completion, release, GA or soak claim.** The PR body and
head-bound checks are the live record for later hosted results. Merge and manual
issue closure remain pending. The maintainer approved the explicit inspection
permission and bounded design on 2026-10-27; built-in grants are unchanged.

## Source and evidence identity

- Baseline `78b458b84d8b6015f64d86191b3bd7c157ffbb11` includes merged #621/#665,
  Beta / implemented. No released/GA/soaked implication.
- Branch `feat/622-cache-inspection`; published implementation
  `a113a39e81a632abd1d0fa326b1f5f42438156ec` has complete tree
  `d101344dc6282bf25230000d8ccddd6c96c20535`, identical to independently reviewed
  local `799d05b38f3f3d900c8abc6d51795330b81ad4f2`. Both reviewers independently
  verified `git diff --exit-code` between these revisions.
- Earlier local implementation/review revisions were `2c2e7cc6c2adca3fc976d79f8cffd5ff4c2c7c71`
  and `a67ba13c51158bb5237a05d54093eba89d33aaee`. They are not final passing
  revisions. Local history is preserved; connector publishing did not rewrite
  remote history or touch main.
- This follow-up adds a sampling-only configuration-diff correction, boundary
  tests, benchmarks and evidence. It reconciles the six renamed generated asset
  paths in the existing D11 inventory, preserving original identities, reviews
  and rebuild mappings. It does not recertify #622 under historical D11 review.
- Runtime toolchain: Go 1.27.3 linux/amd64; Linux 6.18.55; Intel Xeon Platinum
  8573C. Git 2.51.1, Node 24.22.1, pnpm 11.25.0 (repo/hosted pin 11.8.0),
  golangci-lint 2.14.0, govulncheck 1.6.0, addlicense 1.2.0. No repository tool
  requirement or coverage floor changed.

The [pre-implementation record](2026-10-27-622-preimplementation.md) retains
source/issue/ADR review, baseline failures, trade-offs and the approved proposal.
Read this checkpoint for implementation evidence rather than its historical
pending rows.

## Acceptance matrix

“Locally verified” below means the named behavior tests passed, not that all
project-wide gates have passed. Hosted evidence is bound to its actual SHA.

| Criterion | Implementation / semantic owner | Tests / measurements | Documentation / contract | Evidence / remaining gap |
| --- | --- | --- | --- | --- |
| Configured-host / route inspection, exact lookup | `cache.ParseInspectionSelector`, shared #621 matcher; admin active-config scope | Configured host/port/IPv6/normalized-name, route prefix/exact/key intersection; all/unconfigured/regex refusal | Cache, API, CLI, ADR0028 | Locally verified; regex route inspection explicitly unsupported |
| Logical resource / representation / tier counts | Shared `previewScope` catalog projection, `cachemeta` types | Vary stubs, GET/HEAD, memory/disk copies, variants, restart, scan/detail limits | Cache count semantics, Console labels | Locally verified; stub-only row has zero responses and `details: []` |
| Size, origin age, insertion / validation age | Publication evidence, immutable Entry/catalog/disk metadata | Corrected origin Age versus insertion, 304/replace/restart and legacy tests | Cache/API DTO units and unknown provenance | Locally verified; old disk metadata remains readable with unavailable provenance |
| Freshness, SWR/SIE and effective policy | Existing freshness/reuse/merge304 owners; publication evidence + current policy | Source precedence, no-cache/max-age=0, retained policy, recorded versus effective SIE | Cache/configuration/security/limits; server-owned codes | Locally verified; current global inheritance shown, #623 route override capability unsupported |
| Reload / current route cache state | Active config identity and route catalog; stored evidence immutable | Retained entries, config invalidation, disabled route projection | Reload/evidence limits | Locally verified; no entry recomputation or global transaction claim |
| Fresh / stale / validated reuse | `reuseDecision`, `serve`, synchronous/background validation | Owner-linked tables, SWR/SIE, real 304 and rejected private304 | Reason catalogue, cache guide | Locally verified; attempts and completed outcomes distinct |
| Restrictions and protocol bypass | Existing request policy, storageReason, capture/recorder/range owners | Private/no-store/cookie/auth, range on/off, upgrade, foreground and in-progress revalidation SSE | Safe reason and next-step vocabulary | Locally verified; recorder callback records SSE at headers, before stream completion |
| Capture, disk, fill and eviction outcomes | Capture account, disk owners, fill locks, capacity callbacks | Oversize/budget/error, disk open/stream failure, fill timeout/fallback, eviction | Cache/observability/limitations | Locally verified; closed actual-owner events, no missing-entry history inference |
| Minimal bounded evidence | Cache-owned opt-in ring; default false; lifecycle hot reload | Capacity/expiry/disable/timer reclamation/concurrency; worst display retention bound214,048B | Config/lifecycle/schema generation, SECURITY, ADR0028 | Locally verified; 256 records / 60s; memory only, lossy |
| Metadata with sampling off | Inspector independent of ring | Disabled/no-record/expired evidence versus absent entries | Cache/API/CLI/Console states | Locally verified; no “never cached” assertion |
| Default read-only / network-free | Metadata-only catalog path, direct non-sweeping disk counters | Origin request counter, population/LRU/eviction/tier occupancy and reader ownership assertions | Cache/API/diagnostics | Locally verified; no handler/body/file reads and no live probe |
| Permission compatibility | Explicit `cache:inspect`; existing RBAC matcher, live credential verification | Purge-only/read-only isolation, admin/resource wildcards, open-token refusal, stale context identity | SECURITY/API/CLI/Console | Approved design; locally verified; viewer/operator/auditor unchanged |
| Cursor authorization and revocation | Admin live reauthentication; principal/scope/auth/config-bound immutable snapshot | Cross-principal reuse, MAC/offset/scope tamper, every-page revocation/reload/expiry | API errors/restart/retry contract | Locally verified; cursor possession is not authority |
| Bounded pagination / retention / cancellation | Two builders,16 snapshots,8MiB aggregate encoded capture,50 rows/2MiB full envelope | Actual50-resource/1600-representation wide-Vary client traversal; no duplicate/omission; expired/canceled/reloaded captures reclaimed | Cache/API/ADR0028 | Locally verified; request page_size is an upper bound; mutex waits are not instantaneously cancelable |
| Empty / disabled / unavailable / legacy / partial / truncated / churn | Server-owned result, evidence, detail and sequence states | Missing cache owner, disk recovery/legacy, scan/detail truncation, concurrent purge/reload; fault-injected malformed/oversized metadata | All three clients; public DTOs | Locally verified; no sequence change does not prove absence of other churn |
| Privacy and bounded correlation | Cloned safe host/path; random-key HMAC identifiers; HTTP-token Vary names and bounded validated tags | Query/auth/cookie/Vary sentinels; malformed names/inputs/errors; snapshot expiry; no new metric dimensions | SECURITY/observability/diagnostics/limits | Locally verified plus independent security review; raw query/Vary values and bodies omitted |
| Existing destructive actions | Existing invalidation panel/API/client/CLI unchanged in ownership | Permission/confirmation regression suites, reader-only browser test | Console/cache examples | Local and published-head hosted browser checks pass; no second destructive flow |
| CLI / client compatibility and automation | `jul cache inspect`, shared adminclient/DTOs | Actual HTTP/client/dispatch, flags/human/JSON/error/cancel; bounded wide page traversal | Remote CLI/help/runnable inspect.sh | Locally verified; one page per invocation, no silent restart/rescan |
| Accessible Console states / filters / policy links | Lazy `CacheInspectorPanel` in existing cache drawer | 11 inspector unit tests; keyboard/read-only/state browser scenario; existing browser/frontend lanes | Console/cache links and fallback/error text | Published a113a39e browser/real-server/frontend lanes passed; later final head must be checked again |
| Config/public contract completeness | Schema/default/lifecycle/projection/diff/typed patch/API route catalogue owners | Generation drift, config examples, route inventory; sampling-only config-diff regression | Generated schema/metadata/reference/lifecycle/OpenAPI; Unreleased README/changelog | Generated checks pass; no hand-edited generated assets; sampling diff follow-up requires re-review |
| Performance / resource bounds | Reused catalog; no parallel index; compact immutable provenance; ring | Baseline/head paired hit benchmarks;0/256/2000/10001 resources;1/64 variants; saturated two-builder load; memory/reclamation | Limits and benchmark section below | Measured bounds below; timing tails/RSS/production soak not certified |
| Coverage and project gates | Existing move-aware Go / package / security / Console machinery | Local complete changed Go95.0116%(819/862); admin subset95.2381%(200/210); frontend floors; ci-pr/full race/platform/browser | Source-bound raw coverage records below | Final-head Codecov PATCH strictly>90% still pending; earlier a113 report88.67% fails and is not superseded until actual upload/report |
| Independent adversarial reviews | Two non-implementing read-only agents | Initial findings, fixes and independent re-review; SHA/tree verification | Review table below | Both clear a113 runtime tree; follow-up diff/evidence re-review pending |
| Truthful programme status / maturity / closure | #622/#620/#62 updates and draft PR | Actual reviewed/published SHAs and check links | This ledger and live PR description | Selected/in progress; not complete. No merge/manual closure or maturity promotion |

## Commands and local outcomes

Commands run from repository root with Go/tool binaries on PATH. `FULL_TAGS` is
`brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf`.
Profiles and timestamps are separate; a partial aggregate is not a passing gate.

| Exact command | Result / scope |
| --- | --- |
| `go test -coverprofile=/tmp/jul622-coverage-complete.cover ./internal/cache ./internal/admin ./internal/adminclient ./cmd/jul ./internal/rbac ./internal/lifecycle ./internal/config` | PASS; cache92.7%, admin87.4%, client90.6%, CLI88.2%, RBAC89.2%, lifecycle89.1%, config94.0% package statements |
| `go test ./internal/cache ./internal/admin -run '^TestInspection' -count=1` | PASS on reviewed runtime; additional boundary tests also pass |
| `go test -race ./internal/cache ./internal/admin ./internal/adminclient ./cmd/jul` | PASS on follow-up including boundary/config-diff tests |
| `make ci-pr` | FAIL at full-tag tests: Unix corpus/HTTP/FastCGI sockets denied and observability process probes unavailable. Full-tag lint passed0 issues. Targets after test-full did not execute through this aggregate |
| `make test-race` | FAIL overall for the same environment-restricted Unix sockets/process probes; affected cache/admin/client packages pass, no detected data race in the collected log |
| `make security-gates generated-check config-check vulncheck-full build-full license-check` | Security lean/full negative tests and security-package floors PASS; generation/config examples/vulnerability/full build PASS. First aggregate stopped at missing addlicense; installed pinned1.2.0 and `make license-check` separately PASS |
| `go vet -tags "$FULL_TAGS" ./...` | PASS |
| `make console-check` | PASS:85 files /994 tests; typecheck/lint and existing coverage contract pass(123 production files,10 required targets) |
| `pnpm --dir internal/admin/ui run build` | PASS; current emitted initial-route gzip255,668/256,000B, lazy chunks excluded by established ADR0006 rule. Existing chunk/sourcemap warnings disclosed; generated files not hand-edited |
| `pnpm --dir internal/admin/ui exec playwright install chromium` | FAIL locally: downloaded archive truncated/zero-length; browser unavailable |
| `pnpm --dir internal/admin/ui exec playwright test --project=chromium -g 'bounded cache inspection'` | FAIL locally before execution: browser executable missing. Published a113 hosted browser smoke and real-server lanes subsequently PASS |
| `python3 scripts/check-d11-certification.py` | PASS after explicit six-path rebuild reconciliation; initial hosted a113 docs-check failed stale D11 paths |
| `python3 scripts/test_d11_certification.py` | PASS22 tests |
| `python3 scripts/docs-check.py` | Prior runtime checkpoint PASS2538 checks; rerun after final evidence files required |
| `python3 scripts/test_docs_check.py` | PASS; printed FAIL examples are expected negative fixtures, final result OK |
| `bash -n examples/cache-inspection/inspect.sh` | PASS |
| `git diff --check` | PASS at recorded checkpoints; rerun before final commit |

Earlier failed local attempts (missing lint tool, malformed docs invocation,
zero unittest-discovery tests, initial coverage below floor, review regressions,
and two draft boundary-test setup expectations) are superseded only by their
specific corrected checks. They are not counted as passing evidence. The
historical pre-implementation ledger records baseline-specific failures.

### Coverage scope

Complete move-aware production diff commands:

```sh
git diff 78b458b8 --unified=0 -- '*.go' | python3 scripts/strip_moved_lines.py > /tmp/jul622-complete.diff
python3 scripts/check-added-go-coverage.py --diff /tmp/jul622-complete.diff --profile /tmp/jul622-coverage-complete.cover
```

PASS **95.0116009281% =819/862**. The
[raw result](622/changed-go-coverage.json) lists every measured production file
and43 uncovered statements, including entropy/encoding defenses, rare disk/capture
errors and cancellation/revalidation branches. No exclusions/floors changed.
The existing admin-family subset is **95.2380952381%=200/210**;
[raw subset result](622/admin-changed-go-coverage.json). Earlier a113 hosted
HR-07C and semantic-no-op subset gates failed83.93%/83.84%; actual boundary tests
and the independent sampling-diff fix address those gaps, with final rerun pending.

Console coverage from existing tooling: total statements82.23%(5597/6806),
branches77.56%, functions73.27%, lines83.75%. Inspector statements92.30%(72/78),
branches81.08%(60/74); shared client93.29%(668/716), TrafficControlEditor91.82%,
trafficPatchBuilders100%, trafficToml99.32%. Existing floors passed. No new
frontend-wide>90% requirement is imposed.

Main CI uploads six Go reports (full, signed-url, stream/http and native Windows)
to Codecov with search disabled. **Admin OIDC independently uploads Console
lcov**, also with search disabled. Therefore the eventual Codecov aggregate can
include both Go and TypeScript/TSX; verify actual final report files rather than
assuming Go-only measurement. a113's early80.00% report became88.67% after the Go
upload: both fail this prompt's strictly>90% PATCH gate. This ledger does not
claim the later final-head result. Use its actual check/report in the live PR.

## Independent adversarial review record

| Reviewer / scope | Revisions and concrete findings | Disposition / evidence |
| --- | --- | --- |
| `/root/review622_security`: grants/privacy/input abuse/retention/correlation/cardinality | Initial2c2: malformed Vary name leaked arbitrary header text; canceled capture retained snapshot; snapshot extended contained sample TTL. a67: valid wide page exceeded shared client's4MiB cap | Fixed with HTTP-token projection/bounds, cancellation cleanup, earliest evidence expiry, actual full-envelope2MiB pagination. Independent overlays + actual50x32x16 Vary reproduction and maintained inspection race tests PASS at799d05b3. No remaining concrete security findings; independently confirmed identical published a113 tree |
| `/root/review622_correctness`: semantics/concurrency/pagination/ownership/completeness/UX/performance | CR01 no-cache source; CR02 recorded/current SIE; CR03 route host/path widening; CR04 stub phantom response/missing details; CR05 tag evidence correlation; CR06 live SSE; CR07 private304/range owner hooks; CR08 policy/occupancy UI; CR09 provenance heap cost. a67 re-review still found Console SIE labeling, recorder SSE, normalized/IPv6 host scope | All fixed with regressions. Independent old/new probes and cache/admin/client/CLI race suites PASS, inspector UI11/11. CR01–CR09 closed at799d05b3; reviewer verified exact a113 tree. Provenance measured~80B/key versus initial~352B/key. Reviewer did not certify timing/platform/Codecov/browser gates |

Reviewer work was read-only; implementation remained with the primary agent.
Substantive follow-up: sampling-only human config diff is now explicit even when
cache.enabled is unchanged, with regression tests. That small owner change and
this evidence packet require independent re-review before readiness. No review
is presented as a human audit, every-line proof or production soak.

## Benchmarks and resource evidence

Same Go1.27.3/amd64 host; GOMAXPROCS2. Baseline worktree is exact78b458b8.
[Original baseline](622/bench-baseline.txt) and
[original head observations](622/bench-original-head.txt) include noisy unpaired
results: original hit median1683ns versus2017ns(+20% apparent). These alone do
not prove regression or success. Interleaving was added instead of hiding them.

Thirty baseline/head pairs alternate execution order, using precompiled test
binaries pinned to CPUs4/5 with GOMAXPROCS2,500ms per benchmark. Raw values and
source identities: [paired observations](622/paired-bench.json). All samples,
including host/load outliers, are retained. Local test activity overlapped the
last few pairs, so these do not certify idle-host tail latency. The proposed
warm-hit budget concerns median overhead and unchanged allocations.

| Existing benchmark | Baseline median ns/op | Head median ns/op | Median paired change; bootstrap95% interval | Bytes / allocations, both |
| --- | --- | --- | --- | --- |
| CacheHit, one primed response |1739.0|1759.5|+1.628%;[+0.855%,+2.300%]|1256B /16|
| CacheVaryHit, one primed gzip variant |2562.5|2580.5|+0.850%;[-0.793%,+3.333%]|1504B /25|

These paired-median observations support the proposed <=5% median /zero-added-hit-
allocation check in this environment. Bootstrap uses20,000 resamples of all30
paired ratios, seed622, then the2.5/97.5 percentiles of their medians. Geometric-
mean intervals are much wider due outliers; no mean/tail guarantee is claimed.
Revalidate on representative isolated hardware before production-scale promises.

Metadata bounds/measurements (500ms,count5 for resource scales; count3 for new
variant/load fixtures):

| Fixture | Observed median | Allocation / interpretation |
| --- | --- | --- |
|0 resources|494.8ns|96B /1 allocation|
|256 one-response resources|2.162ms|~922kB /8729 allocations|
|2000 one-response resources|6.074ms|~1.75MB /19214 allocations|
|10001 one-response resources,10000 scan cap|9.634ms(max12.911ms)|~5.50MB /67269 allocations; explicit truncation|
|256 resources x1 Vary representation|1.833ms|~0.964MB /10522 allocations|
|256 resources x64 Vary representations|30.945ms(max35.421ms)|~20.77MB transient /135961 allocations; detail cap32 per resource|
|2000 resources, no inspection worker|1621ns/hit|1192B /14 allocations in this separate fixture|
|Same fixture,two continuous inspection workers|2251ns/hit(max2279)|~39% throughput-time cost under saturated metadata/GC load; reported B/op includes background allocations, not a hit-path allocation increase|

[Variant/load raw observations](622/bench-variants-load.txt). Saturated workers
exceed normal operator refresh traffic and share only two CPUs. Conservative
local validation bounds: metadata scans at the declared scales <=250ms and mean
warm hit under the declared saturated2000-resource load <=5us. Observed maxima
are below these bounds; these are measured fixture budgets, not HTTP SLAs,
global snapshot guarantees or a promise that all max-scale lock waits finish
within the API's two-second context deadline. Existing mutex waits remain
non-cancelable; #667 remains separate. Default warm-hit overhead is evaluated
separately above, with sampling disabled and no active builders.

Opt-in sampling cost: median2845ns,1952B/28allocations versus equivalent
sampling-off fixture1696ns,1256B/16allocations. Enable it for bounded diagnostics
with this cost understood. It is disabled by default.

Measured10k real one-byte-entry retained heap: baseline~17,472,544B versus
~18,272,384B, about80B/key(+4.58%). A conservative112B/representation incremental
provenance budget covers the immutable payload and three owner pointers; Entry
capacity accounting adds96B when provenance exists. This is not total RSS or disk
usage certification. No second index is introduced.

Worst admitted512-byte safe sample display,256 records and large raw-query
backing buffers: [retention test](622/sample-memory.txt) bounds retained cloned
strings, HMAC IDs, actual slice capacity/struct size, buffer and timer allowance
at**214,048B <256KiB**. Expiry/disable tests separately prove independent timer
reclamation. Retained snapshots are bounded by encoded bytes/count/TTL, not an
asserted equal decoded-heap or process-RSS limit.

Reproduction commands (benchmark setup/counts are in `inspection*_test.go`):

```sh
GOMAXPROCS=2 go test ./internal/cache -run '^$' -bench '^(BenchmarkCacheHit|BenchmarkCacheVaryHit|BenchmarkCacheHitDecisionSampling|BenchmarkInspectionBounded)$' -benchmem -benchtime=500ms -count=5
GOMAXPROCS=2 go test ./internal/cache -run '^$' -bench '^(BenchmarkInspectionVariants|BenchmarkCacheHitWithInspectionLoad)$' -benchmem -benchtime=500ms -count=3
go test ./internal/cache -run '^TestInspectionMaximumSampleRetentionBudget$' -count=1 -v
# Compile the same CacheHit/CacheVaryHit benchmark in each source worktree:
go test -c -o /tmp/jul622-base-bench.test ./internal/cache
go test -c -o /tmp/jul622-head-bench.test ./internal/cache
# Alternate the two binaries30 times, GOMAXPROCS=2, CPU affinity4/5:
/tmp/jul622-head-bench.test -test.run='^$' -test.bench='^(BenchmarkCacheHit|BenchmarkCacheVaryHit)$' -test.benchmem -test.benchtime=500ms -test.count=1
```

## Compatibility, limits and remaining actions

Additive config/API/CLI and gob provenance preserve old disk reading. Legacy
entries explicitly lack provenance. No built-in role gains inspection, exact
purge grants remain separate, and anonymous/open-token admin mode cannot inspect.
No request-policy evaluator, live probe, persistent evidence store, new metrics
labels or destructive operation is added. Route scope is current host/path
intersection, not historical route-predicate attribution; regex routes and #623
route policy overrides are explicitly unsupported.

NGINX migration disposition: **not applicable** to this Jul-specific management
interface. No imported request/cache policy semantics changed. Existing importer,
corpus and real-server gates still apply; baseline Unix failures are retained
as failures and hosted platform evidence is separate.

Inherited limitations, verified still open at this checkpoint:

- [#666](https://github.com/victornife/jul/issues/666): interrupted/canceled purge
  protection across restart; existing cache-invalidation owner, not implemented here.
- [#667](https://github.com/victornife/jul/issues/667): large purge lock/persistence
  performance; existing follow-up owner. Inspector does not certify those budgets.
- [#668](https://github.com/victornife/jul/issues/668): legacy void purge outcome
  observability/durability; existing follow-up owner. No false structured success.

#623 route policy, #624 cutover, #625 cross-Console catalogue and #586 sharing
remain separate; no invented predecessor, maturity advancement or reopen of #621.

Pre-merge remaining actions: independently re-review the small sampling-diff and
new evidence/test changes; rerun final-head changed/package/security/docs/CI
checks; verify actual final-head Codecov PATCH strictly>90% with measured files
and counts where available; inspect final platform/browser outcomes. Keep draft
status if any required gate remains blocked. Only after all pass may the PR be
reported ready for maintainer review; merge and manual closure remain pending.
After an actual authorized merge, record its actual SHA/evidence in #622 and
request approval before manually closing the issue.
