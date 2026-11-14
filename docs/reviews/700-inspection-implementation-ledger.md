# #700 inspection implementation ledger

This is the single continuation record for the inspection-governor checkpoints. The accepted
authority is [ADR 0032](../adr/0032-aggregate-inspection-resource-governor.md); its
2026-11-13 amendment was **accepted** when #714 merged as `16d98258`. Historical reports and
measurements retain their original identities. Merged source is not a published release,
qualification, soak or maturity change.

## Current checkpoint

| Item | State |
| --- | --- |
| Starting `main` | `d06538cecc1398b6e8bf6302b553f873cd44e941` (verified #717 merge, after I4) |
| Checkpoint 1 | **Merged**: #714 merged as `16d98258` from head `faf3ead4` after green hosted checks; the amendment is accepted |
| Checkpoint 2 | **Merged**: I3a WAF request accounting, #715 merged as `def419f5` from head `e7ea422e`; see [I3a implementation record](#i3a-implementation-record) |
| Checkpoint 3 | **Merged**: I4 response accounting, #716 merged as `daa06bdc` from head `d879cba9` after all required hosted checks passed |
| Checkpoint 4 | **Blocked**: I3b spool protection (#713) requires the supported Coraza boundary and ownership/default decisions recorded below |
| Existing foundations | I1 #706 (`a57afa4d`), I5 #708 (`d8d0520b`), I2 #710 (`c8bba7f2`) and I3a #715 (`def419f5`) merged |
| Programme owners | #700 and #62 remain open; no A1–A22 acceptance is claimed |
| Evidence | [2026-11-13 checkpoint-1 probes](evidence/2026-11-13-700-checkpoint-1/README.md); historical [I1](evidence/2026-11-10-700-i1-base-charge/README.md), [I5](evidence/2026-11-12-700-i5/README.md), and [I2](evidence/2026-11-12-700-i2/README.md) |
| Exact next action | Resolve and review the I3b integration and ownership prerequisites; investigate I2b independently without claiming I3b protection |

## Checkpoint schedule and dependencies

| Checkpoint | Scope / status | Actual prerequisite and next owner |
| --- | --- | --- |
| 1 — design | Merged (#714, `16d98258`); amendment accepted | None |
| 2 — I3a | Merged (#715, `def419f5`): WAF request accounting, slots, request/audit calibration | Complete for this checkpoint; response calibration followed in I4 |
| 3 — I4 | Merged (#716, `daa06bdc`): WAF and plugin response paths, response audit calibration | Complete for this checkpoint; I3b remains next in the schedule |
| 4 — I3b | Blocked: inspection spool bytes/files and restart/cleanup ownership; #713 | Accepted storage design, WAF integration, a reviewed Coraza interception boundary, supported finite defaults and safe directory/recovery design; implementation and upstream-integration owners |
| 5 — I2b | Investigation: process-wide per-live-instance guest-capacity admission; #711; runtime implementation not yet started | Accepted guest design, plugin manager and supported finite default; no technical dependency on the WAF parser, though scheduled after I3b |
| 6 — I6a | Not started: operational API/CLI/Console and implementation documentation; #712 | Stable implemented resource contracts and real producer signals; implementation owner |
| I6b — separate prompt | Qualification pending | Integrated source, frozen plan and A1–A22 campaign; qualification owner |

Do not open runtime PRs until their prerequisite amendment and preceding scheduled checkpoint
are confirmed merged. The child issues were created after checking existing tracking and linked
to #700. The scheduling order does not itself create technical dependencies between unrelated
resources.

## Resolved recommendations and explicit blockers

- **Audit:** the R5 reservation precedes WAF transaction entry and covers phase-5 rule work,
  record construction and supported writer ownership. It is charged once under the existing byte
  ceiling. I3a/I4 provide focused calibration and writer handling; integrated A21 qualification
  remains pending.
- **Storage:** the pinned Coraza API calls `os.CreateTemp` directly for both body spill and
  multipart files; `SecTmpDir` is ignored. The design requires a supported public interception
  boundary and does not claim that polling, `WithRootFS`, an incoming-byte counter or a private
  hook enforces the quota. Startup reconciliation and ownership across a directory migration
  require a reviewed cross-platform solution. Retained-upload directives are explicitly
  unsupported in the proposed protected profile until separately owned.
- **Guest:** reserve each starting/live instance's effective page-rounded ceiling across every
  generation, with a documented fail-fast idle-pool policy. No actual-growth callback is assumed.
- **Defaults:** exact omitted-field defaults for guest capacity, spool bytes, spool files and
  directory are **blocked**; the small probes and single-repetition historical cells do not
  support production choices. No unlimited default is concealed. Existing accepted defaults
  remain unchanged.
- **Qualification:** preserve the corrected baseline `858590dc7c430d24cd33b096a81ca6051591b427`,
  at least three repetitions, the 2-vCPU/768-MiB/no-swap envelope, the 576-MiB objective where
  applicable and the 10% unsaturated regression rule. I2's noisy throughput remains inconclusive.
  Final execution belongs to I6b.

## Design review before implementation

This is a source-backed **self-review of the proposal**, not an independent review or a claim that
the future runtime fixes already exist. Independent reviewers must inspect the actual checkpoint-1
diff and relevant owners; their findings and resolution belong below.

| Finding | Severity / counterexample | Proposed resolution and verification |
| --- | --- | --- |
| D1 — phase-5 pressure | High: Coraza `transaction.go:1431,1540` can construct and copy a request-body audit record after response commitment; a writer-only limit is too late. HTTPS output may retain a transport reader. | R5 is reserved before transaction entry and retained through final alias release; exact coefficients and unsupported writers are blocking decisions. The checkpoint-1 part-C probe reproduces additional allocations; I3a/I4 must prove the bound and lifecycle. |
| D2 — hidden file creation | High: Coraza `body_buffer.go:78` and `multipart.go:120` write outside a Jul reader wrapper, and `SecTmpDir` is ignored. | Require a reviewed public integration before I3b, plus explicit restart/directory ownership. The spill probe reproduces simultaneous copies. |
| D3 — file-count exhaustion | High: Coraza `multipart.go:120` creates a file per part, so byte-only accounting admits many zero-byte files. | Separate file-count reservation before creation; the 32-empty-file probe reproduces the counterexample. |
| D4 — generation capacity multiplication | High: `internal/plugins/runtime.go:574` instantiates during Prepare and idle pools survive while other generations serve. | One process reservation per starting/live instance across candidates and retiring generations; failure tears down only candidate resources. I2b must test overlap and failure. |
| D5 — idle monopolization | Medium: `internal/plugins/runtime.go:48` retains idle modules; an idle pool can hold the entire new cap and prevent another plugin from starting. | Deliberately fail fast, expose the condition and document pool/cap/overlap sizing; no busy eviction. I2b must test the policy. |
| D6 — lost storage debt | High: Coraza `body_buffer.go:165` closes and removes its file; an outstanding handle or a root change after a crash can leave physical storage while counters appear zero. | Keep tokens until Jul-owned handles close and removal succeeds; reconcile owned leftovers. Cross-root durable discovery remains an explicit I3b blocker; I3b must inject cleanup and restart failures. |
| D7 — misleading operator signals | Medium: a status-only viewer can infer sensitive route configuration, or observe mode/free disk can appear as enforced quota. `docs/admin-api.md:176` and `internal/observability/storage_metrics.go:60` show the distinct current permission and filesystem-capacity contracts. | Permission-gated route details, separate unavailable/observe/pressure states and separate logical quota vs advisory filesystem headroom; I6a must test permissions and unavailable states. |
| D8 — static disclosure of spool files | High: `internal/handler/static.go:120-179` serves ordinary regular files below each configured root; Coraza's `body*` and `crzmp*` names are not hidden. The probe received HTTP 200 for a synthetic `body*` file placed under a static root. | Require canonical identity/ancestry separation from every current, candidate and retiring static root and upload directory; reject symlink aliases or ambiguous identity before startup/Publish. The invariant is added to the proposal; I3b must test aliases and reload. |
| D9 — parser I/O fail-open | High: Coraza `transaction.go:1174-1184` records `REQBODY_ERROR`, evaluates phase 2 and returns nil error when the body processor fails; Jul `internal/waf/outcomes.go:417-443` forwards when there is no interruption/error. A synthetic create failure did not run a rule on a later multipart field. | Require typed out-of-band quota/I/O results and fail closed before forwarding in both modes; do not rely on generic SecRules/markers. The source probe reproduces this behavior; I3b must verify the supported pinned integration. |
| D10 — mutable audit alias retention | High: Coraza `transaction.go:1573` places `tx.variables.args` in the audit record, then `Close` resets/pools transaction variables (`1712-1735`). A retaining writer observed the next transaction's argument through the earlier record. | Require bounded immutable transaction-detached snapshots before handing records to writers; async sinks may enqueue only separately bounded serialized bytes. Reject unsupported retaining writers in protected profiles. The source probe reproduces alias mutation; I3a/I4 must verify detachment and bounds. |

## Checkpoint-1 requirement-to-evidence matrix

These are design and feasibility checks, not tests of unimplemented controls. The amendment's
checkpoint table specifies the later behavior tests and I6b owns their integrated qualification.

| Requirement | Checkpoint-1 evidence | Later owner |
| --- | --- | --- |
| Resource units, acquisition, aliases and reload lifecycle | ADR ownership/mode/lifecycle tables and D1–D10; source review of current governor and engine boundaries | I3a/I4/I3b/I2b; A9/A15 and I6b |
| Phase-5 pressure before refusal becomes impossible | Part-C audit probe and raw `ProcessLogging` allocations; mutable retained-record probe (D10); existing logging/lease boundary tests | I3a/I4 calibration, snapshot detachment and writer ownership; A21 |
| Spool bytes, file count, parser failures, static separation and durable cleanup | Spill/prefix, empty-file, parser-I/O and static-file exposure probes plus source review; D2/D3/D6/D8/D9 | #713; A19 |
| Guest capacity and generation overlap | Four-instance page-limit probe, pinned wazero source, current guest-statistics tests, D4/D5 | #711; A20 |
| Truthful status, access and external boundaries | ADR status/permission contract and D7; existing APIs and #625/#628 handoffs inspected | #712; A22 |
| Preserve acceptance and historical evidence | ADR A1–A18, proposed A19–A22, recorded historical I1/I5/I2 checksum verification and the evidence manifest | I6b separate prompt |

## Independent reviews and verification

The independent ownership/security review found three High-severity design gaps (confidence
9/10): the spool root could overlap a static-serving tree; Coraza can swallow a multipart file
I/O error and continue phase 2 without Jul seeing a failure; and a writer can retain an audit
record whose mutable transaction collections are reset and reused. All three are in scope.
They were reproduced or confirmed against Jul and pinned Coraza source, corrected in the proposal,
and assigned explicit implementation tests below. The earlier independent final whole-checkpoint
review completed with no actionable findings. A subsequent review of PR #714 head `1cd75931`
requested two bounded design corrections: I2b must own and test a true limits-only guest-capacity
publication path, and A19 must cover pre-existing spool reservations when observe changes to
enforce without a numeric limit change. The amendment now specifies both, including their
acceptance cases.

An earlier direct pass over admission ordering, cap updates, reload overlap, queue/slot behavior
and H1/H2/H3 failure semantics found no additional design gap. It is superseded as a
completeness claim: the separately requested concurrency/protocol reviewer returned later, on
head `29eca6aa`, with four blocking and fourteen non-blocking findings. Each was checked against
current source (`internal/inspection/governor.go`, `respond.go`, `internal/plugins/runtime.go`,
`internal/waf/outcomes.go`, `internal/config/schema.go`, `internal/server/inspection_limits.go`)
and the ADR text, and each is now **resolved in the proposal** by a normative rule and an owning
checkpoint test (ADR "Concurrency, ownership and protocol rules" and the extended checkpoint and
A20/A21 rows):

| Finding | Confirmed against | Proposal resolution and owner |
| --- | --- | --- |
| B1 — no process-scope owner for guest/spool charges | `ByteClass` has only `base`/`buffer`/`waf_state` | Process-scope tickets independent of request leases, released only after confirmed close/removal; I2b/I3b |
| B2 — R5 lifetime for upgrades, streams and CONNECT | Deferred `ProcessLogging` and hijack tracking in `outcomes.go` | Phase 5, snapshot detachment and release of transaction ownership (slot, `waf_state`, transaction-scoped R5) at the commit point; pending audit output stays charged to its owner; many-tunnel test; I3a/I4 |
| B3 — hung synchronous audit writer | ADR "nothing deadlocks" and R5/output rows | Named supported adapters, `SecAuditLog*` rejection, writer deadline with bounded charged abandonment, A21 recovery; "no wait cycle" wording; I3a/I4 |
| B4 — enforce/default gate omits A19–A22 | ADR status note, §7, §10, decision 1, amendment preamble | Gate extended per control (R5→A21, spool→A19, guest→A20, operability→A22) |
| N1 — unserialized limit publication | No publish serialization or version in the reload path | One serialized, versioned publisher; stale candidates re-prepare or fail; race tests; I2b/I3b |
| N2 — instance close/cancel | `runtime.go:717-733` uses `context.Background` and ignores `Close` errors | Instance state machine with `failed_retained`, cancellation propagation; I2b |
| N3 — refusal classification | `IsOverload` covers only byte, `waf_state` and evaluation | New reasons classified as governor overload per protocol; `guest_capacity` distinct from plugin busy |
| N4 — observe slot inflation | `governor.go:677` observe path counts phase-5 tokens as active | Observe/enforce parity and transition carryover; `slots = 1` test; I3a |
| N5 — engine-off R5 | Early return in `outcomes.go` | No R5, or release before return; I3a |
| N6 — sub-page `memory_limit` | `runtime.go:591-595` turns 0 pages into 256 | Reject positive values below 64 KiB; report effective ceiling; I2b |
| N7 — generation charge owner | — | Tickets record generation; I2b/I3b |
| N8 — reconciliation/cleanup bounds | — | Startup sweep before admission; fixed cleanup queue; enforce refuses when full, observe keeps debt for a bounded periodic sweep; I3b |
| N9 — WAF-only `body_timeout` | `schema.go:353`; no H3 deadline forwarding traced | §10 item 4 scoped to plugin routes until I3a proves WAF-only and H3 |
| N10 — R5 during FIFO wait | — | Waiter cap × `R5_max` in sizing; A18 exercise |
| N11 — async queue owner | — | Single bounded process-scope queue under R5; deferred refund; I3a |
| N12 — "abort further inspection work" | ADR spool section | Now "stop further phase 1–4 evaluation (phase 5 still runs)" |
| N13 — §3 says phase 5 needs no reservation | ADR §3 table and bullet | Marked superseded by R5 |
| N14 — stale ledger claims and D4 line | This ledger | This paragraph supersedes them; D4 cites `runtime.go:574` |

Engine behaviors the reviewer could not verify (whether the pinned `ProcessLogging` writer call is
synchronous, built-in writer deadlines and `Module.Close` blocking) are explicitly assigned to
I3a/I2b verification rather than assumed.

The maintainer's re-review of head `04372313` confirmed these resolutions and requested two
wording corrections, now incorporated. First, abandoning a hung writer releases the slot when the
Coraza call returns, but the retained audit charge stays visible until the writer actually
returns. Likewise, long-lived exchanges release transaction ownership while pending output stays
charged. A21 now drains counters to baseline only after that real completion. Second, saturation
of the `abandoned_audit` set and of the spool cleanup queue now has separate per-mode behavior.
Enforce refuses. Observe admits, records would-refuse decisions and uses a bounded fallback:
counted audit drops, or retained spool debt with a bounded periodic sweep. Both modes require
saturation tests. Physical I/O and ownership failures stay fail-closed in both modes. None of
these reviews is an implementation test.

| Check | Result |
| --- | --- |
| `CGO_ENABLED=0 go test -tags waf ./internal/waf -run '^TestAssessmentRequestBodySpill$' -count=1` | Passed on `go1.27.4 darwin/arm64` (4.513 s). The normal CGO path was blocked by the local unaccepted Xcode license; this is not a full-platform test. |
| Checkpoint-1 direct dependency probes | Passed five executions, including three audit samples per boundary execution and a synthetic I/O-failure/audit-alias probe; identities and raw results are in the evidence manifest. Not a qualification envelope. |
| `CGO_ENABLED=0 GOMAXPROCS=2 go build -p 2 ./...` and the full-tag build | Passed on the same host; exact full-tag command is recorded in the PR. |
| Focused inspection/WAF/plugin and instrument `go vet`; focused spill, guest-statistics and logging tests | Passed with CGO disabled; exact selectors and flags are recorded in the PR. |
| `make format-check`; `CGO_ENABLED=0 GOMAXPROCS=1 GOFLAGS=-p=1 make generated-check`; `make config-check` with the same Go environment and a private `TMPDIR` | Passed. |
| `CGO_ENABLED=0 GOMAXPROCS=1 GOFLAGS=-p=1 python3 scripts/docs-check.py` | Final pass: 2,720 passed, zero failed. |
| `sha256sum -c SHA256SUMS` for this checkpoint and referenced I1/I5/I2 evidence | Passed in the working tree and from a clean worktree; referenced I1/I5/I2 evidence checksums also passed. The final candidate is rechecked before publication. |
| Full local `go vet ./...`, `go test ./...`, CGO WAF, race and platform lanes | Not run locally or blocked by the Xcode license as recorded above; do not infer hosted results from these local checks. |
| Hosted checks for PR #714 at code head `e535350b` | 56 successful, two expected website deployment/availability skips, zero failures or pending checks ([run](https://github.com/victornife/jul/actions/runs/43427860229)). The preceding head's coverage-gate failure was fixed by storing standalone probes as `.go.txt`; the gate itself was not changed. |

## I3a implementation record

I3a is described normatively in ADR 0032 "I3a progress". It resolves these design-review items:

| Item | I3a resolution and test |
| --- | --- |
| D1 / R5 before the transaction | Fixed R5 reserved before `NewTransaction`; construction estimate reserved (or the audit engine turned off) before `ProcessLogging`. `TestGovernedWAFRefusesBeforeTransaction`, `TestGovernedWAFDropsAuditRecordOverBudget` |
| D10 / audit aliasing | Jul `serial` writer formats a detached copy; only Native/JSON formatters and this writer are accepted when governed. `TestGovernedWAFAbandonsBlockedAuditWriter` (immutable record after pool reuse), `TestGovernedWAFRejectsUnsupportedAuditWriter` |
| B2 / long-lived exchanges | An H1 hijack ends transaction ownership and the WAF lease at the commit point; an undrained inspected body keeps it until the handler returns. `TestGovernedWAFHijackEndsTransactionOwnership`. Non-hijack commit points move to I4 |
| B3 / hung writer | 5 s write deadline; the abandoned write stays charged until it returns. `TestGovernedWAFAbandonsBlockedAuditWriter` |
| N4 / observe slots | Observe admits on saturation with parity accounting. `TestGovernedWAFSlotSaturation` |
| N5 / engine off | The dead engine-off early return is removed: Jul always appends `SecRuleEngine On` or `DetectionOnly`; per-transaction `ctl:ruleEngine=Off` still builds the record, so R5 stays |
| N9 / WAF `body_timeout` | Governed WAF body reads run under the arrival deadline (408; unenforceable → 500). `TestGovernedWAFSlowBodyTimesOut`, H1/H2 cells in `governed_protocol_test.go`. The WAF H3 cell is measured in I6b |
| N11 / async queue | Not applicable: the only accepted writer is synchronous with charged abandonment |
| Calibration | `TestWAFReservationsCoverMeasuredAllocation` (Coraza v3.8.1, CRS v4.25.0): retained R1/R2 heap, audit construction and Native/JSON formatting within estimates. Fixed the H-before-K `ErrorLog` bound and the first-use MIME-table allocation |

The new WAF outcome `inspection_refused` is added to the metrics contract, admin filters and
docs. The route minimum gains the 32 KiB WAF read buffer (default fit 258,654,208 B). I4 now owns
R3/R4, response buffering, response calibration and non-hijack long-lived commit points. No A1–A22
acceptance is claimed.

## I4 implementation record

I4 was merged in #716 as `daa06bdc` from head `d879cba9`; this record describes merged source, not a
released or qualified control.

- Plugin response-subscription buffers charge capacity before allocation and growth. On budget
  refusal the existing ABI-v2 unavailable-body state reports reason `budget`, then preserves the
  complete original response rather than passing a truncated body.
- The WAF charges its response buffer before Coraza writes, reserves cumulative R3/R4 around
  phase-3/phase-4 evaluation, and measures decoded response-header occurrences as name + value +
  64 bytes. Enforce-mode header-ceiling overflow returns an empty 500; reservation and bounded
  evaluation-queue refusal use overload handling. Observe records would-reject and continues.
  Response-body accounting uses the compiled ceiling even if a transaction ctl raises its limit.
  A refusal after commit aborts the stream without claiming a replacement status.
- Focused tests cover initial and growth budget refusal, exact original-body passthrough, enforce
  and observe header behavior, R3/R4 and response-buffer charges, implicit/explicit phase 4,
  evaluation saturation, admission limit snapshots and compiled response-body ceilings.
- `TestWAFResponseReservationsCoverMeasuredAllocation` now calibrates R3/R4 retained state, Native
  and JSON response-audit construction/formatting, URL-encoded and JSON response parsing,
  ProcessPartial, and repeated `Set-Cookie`/`WWW-Authenticate` headers. Non-hijack commit tests
  cover continued SSE writes and transaction release, H2/H3 sibling reuse, and native gRPC
  streaming; empty request bodies release ownership without waiting for a nonexistent drain.
- Full-stack tests under `wasmplugins && waf` exercise the production WAF/plugin order, transformed
  response inspection, backpressure charge retention, client-disconnect cleanup, and a phase-1 H2
  rejection while the request body remains open.

| Validation command | Result |
| --- | --- |
| `CGO_ENABLED=0 go test -tags waf -count=1 -run '^TestWAFResponseReservationsCoverMeasuredAllocation$' ./internal/waf/` | Passed |
| `CGO_ENABLED=0 go test -tags 'wasmplugins waf grpc http3' -count=1 ./internal/app/ ./internal/plugins/ ./internal/waf/ ./internal/inspection/` | Passed; response calibration, full-stack ordering/backpressure/disconnect, and H1/H2/H3 plus gRPC response paths |
| `go test -race -tags 'wasmplugins waf grpc http3' -count=1 ./internal/app/ ./internal/plugins/ ./internal/waf/ ./internal/inspection/` | Passed |
| `go vet -tags 'wasmplugins waf grpc http3' ./internal/app/ ./internal/plugins/ ./internal/waf/ ./internal/inspection/` | Passed |
| `go build -tags 'wasmplugins waf grpc http3' ./cmd/jul` | Passed |

The extended CONNECT cell is not applicable under Jul's documented protocol boundary (ADR 0018
§17). I4 implementation and focused validation are complete; all required #716 hosted checks passed
before merge. These tests are not I6b qualification or A1–A22 acceptance.

## I3b prerequisite verification

On 2026-11-14, starting from `d06538ce`, the pinned Coraza v3.8.1 and upstream `main` at
`674ec708bc2c52380397db39f81387b67286ef1a` still call `os.CreateTemp` independently for body
spill and multipart files. The public WAF configuration does not expose a common file lifecycle
owner or a typed parser I/O result. The experimental body-processor registry alone does not
intercept the body buffer. An upstream integration or another reviewed supported boundary must
cover create, write/extent, close and removal for both paths and carry quota versus physical
errors to Jul before origin forwarding; neither `WithRootFS` nor post-write counting proves this.

The existing [ownership probe](evidence/2026-11-13-700-checkpoint-1/README.md), rerun with
`CGO_ENABLED=0 JUL_PROBE_TMP="$probe_tmp" go run docs/reviews/evidence/2026-11-13-700-checkpoint-1/instruments/ownership.go`
after the documented temporary copy, again returned no interruption or error on a multipart
create failure while `REQBODY_ERROR` was `1`; the normal request was denied with 403. The probe's
static exposure and audit-alias controls also reproduced. Its nine archived checksums passed;
`CGO_ENABLED=0 go test -tags waf -count=1 -run '^TestAssessmentRequestBodySpill$' ./internal/waf/`
passed. This is a prerequisite recheck, not I3b implementation or A19 acceptance.

The safe root/migration locator and representative finite defaults remain separate blockers.
The recommended next step is a reviewed public engine integration with per-transaction ownership
and typed storage outcomes, followed by the ADR's filesystem, lifecycle and protocol tests.
No upstream change has been submitted, and no logical spool quota is claimed.

## Handoff rules

I3a is merged as #715 and I4 as #716 (`daa06bdc`). I3b is blocked as described above; continue
independent I2b investigation while preserving the scheduled checkpoint merge order. Do not mark
I3b, I2b or I6a implemented merely because their designs or tracking issues exist; do not close
#700/#62 or claim final acceptance before I6b.
