# #700 inspection implementation ledger

This is the single continuation record for the inspection-governor checkpoints. The accepted
authority is [ADR 0032](../adr/0032-aggregate-inspection-resource-governor.md); its
2026-11-13 expansion is **proposed** until its design PR is merged. Historical reports and
measurements retain their original identities. Merged source is not a published release,
qualification, soak or maturity change.

## Current checkpoint

| Item | State |
| --- | --- |
| Starting `main` | `c8bba7f2c1b6c312aa215f8576014a513710775b` (verified #710 merge) |
| Checkpoint 1 | **Active**: design amendment, feasibility evidence and ledger; no later runtime implementation |
| Design PR / head | Not yet opened; the PR's current head and checks will be the authoritative exact identity |
| Existing foundations | I1 #706 (`a57afa4d`), I5 #708 (`d8d0520b`) and I2 #710 (`c8bba7f2`) merged |
| Programme owners | #700 and #62 remain open; no A1–A22 acceptance is claimed |
| Evidence | [2026-11-13 checkpoint-1 probes](evidence/2026-11-13-700-checkpoint-1/README.md); historical [I1](evidence/2026-11-10-700-i1-base-charge/README.md), [I5](evidence/2026-11-12-700-i5/README.md), and [I2](evidence/2026-11-12-700-i2/README.md) |
| Exact next action | Complete independent review and verification of the design diff, publish its focused PR, then stop at the maintainer's merge boundary |

## Checkpoint schedule and dependencies

| Checkpoint | Scope / status | Actual prerequisite and next owner |
| --- | --- | --- |
| 1 — design | Active; proposed ADR amendment, evidence and this ledger | Independent reviews, validation and design PR; maintainer accepts or requests changes |
| 2 — I3a | Not started: WAF request accounting, slots, request/audit calibration | Accepted amendment, I1/I2 and a defensible R5 bound/writer ownership; implementation owner |
| 3 — I4 | Not started: WAF and plugin response paths, response audit calibration | I3a and the accepted response/header contract; implementation owner |
| 4 — I3b | Blocked: inspection spool bytes/files and restart/cleanup ownership; #713 | Accepted storage design, WAF integration, a reviewed Coraza interception boundary, supported finite defaults and safe directory/recovery design; implementation and upstream-integration owners |
| 5 — I2b | Not started: process-wide per-live-instance guest-capacity admission; #711 | Accepted guest design, plugin manager and supported finite default; no technical dependency on the WAF parser, though scheduled after I3b |
| 6 — I6a | Not started: operational API/CLI/Console and implementation documentation; #712 | Stable implemented resource contracts and real producer signals; implementation owner |
| I6b — separate prompt | Qualification pending | Integrated source, frozen plan and A1–A22 campaign; qualification owner |

Do not open runtime PRs until their prerequisite amendment and preceding scheduled checkpoint
are confirmed merged. The child issues were created after checking existing tracking and linked
to #700. The scheduling order does not itself create technical dependencies between unrelated
resources.

## Resolved recommendations and explicit blockers

- **Audit:** the proposed R5 reservation precedes WAF transaction entry and covers phase-5 rule
  work, record construction and any retained writer/transport aliases. It is charged once under
  the existing byte ceiling. Exact policy-specific terms, formatter coverage and alias ownership
  remain **blocked** pending I3a/I4 calibration or a reviewed upstream boundary.
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
| D4 — generation capacity multiplication | High: `internal/plugins/runtime.go:450` instantiates during Prepare and idle pools survive while other generations serve. | One process reservation per starting/live instance across candidates and retiring generations; failure tears down only candidate resources. I2b must test overlap and failure. |
| D5 — idle monopolization | Medium: `internal/plugins/runtime.go:48` retains idle modules; an idle pool can hold the entire new cap and prevent another plugin from starting. | Deliberately fail fast, expose the condition and document pool/cap/overlap sizing; no busy eviction. I2b must test the policy. |
| D6 — lost storage debt | High: Coraza `body_buffer.go:165` closes and removes its file; an outstanding handle or a root change after a crash can leave physical storage while counters appear zero. | Keep tokens until Jul-owned handles close and removal succeeds; reconcile owned leftovers. Cross-root durable discovery remains an explicit I3b blocker; I3b must inject cleanup and restart failures. |
| D7 — misleading operator signals | Medium: a status-only viewer can infer sensitive route configuration, or observe mode/free disk can appear as enforced quota. `docs/admin-api.md:176` and `internal/observability/storage_metrics.go:60` show the distinct current permission and filesystem-capacity contracts. | Permission-gated route details, separate unavailable/observe/pressure states and separate logical quota vs advisory filesystem headroom; I6a must test permissions and unavailable states. |
| D8 — static disclosure of spool files | High: `internal/handler/static.go:120-179` serves ordinary regular files below each configured root; Coraza's `body*` and `crzmp*` names are not hidden. A spool root nested in a static tree can disclose bodies or uploads through static routes/listing. | Require canonical identity/ancestry separation from every current, candidate and retiring static root and upload directory; reject symlink aliases or ambiguous identity before startup/Publish. The invariant is added to the proposal; I3b must test aliases and reload. |
| D9 — parser I/O fail-open | High: Coraza `transaction.go:1174-1184` records `REQBODY_ERROR`, evaluates phase 2 and returns nil error when the body processor fails; Jul `internal/waf/outcomes.go:417-443` forwards when there is no interruption/error. A synthetic create failure did not run a rule on a later multipart field. | Require typed out-of-band quota/I/O results and fail closed before forwarding in both modes; do not rely on generic SecRules/markers. The source probe reproduces this behavior; I3b must verify the supported pinned integration. |
| D10 — mutable audit alias retention | High: Coraza `transaction.go:1573` places `tx.variables.args` in the audit record, then `Close` resets/pools transaction variables (`1712-1735`). A retaining writer observed the next transaction's argument through the earlier record. | Require bounded immutable transaction-detached snapshots before handing records to writers; async sinks may enqueue only separately bounded serialized bytes. Reject unsupported retaining writers in protected profiles. The source probe reproduces alias mutation; I3a/I4 must verify detachment and bounds. |

## Checkpoint-1 requirement-to-evidence matrix

These are design and feasibility checks, not tests of unimplemented controls. The amendment's
checkpoint table specifies the later behavior tests and I6b owns their integrated qualification.

| Requirement | Checkpoint-1 evidence | Later owner |
| --- | --- | --- |
| Resource units, acquisition, aliases and reload lifecycle | ADR ownership/mode/lifecycle tables and D1–D10; source review of current governor and engine boundaries | I3a/I4/I3b/I2b; A9/A15 and I6b |
| Phase-5 pressure before refusal becomes impossible | Part-C audit probe and raw `ProcessLogging` allocations; mutable retained-record probe (D10); existing logging/lease boundary tests | I3a/I4 calibration, snapshot detachment and writer ownership; A21 |
| Spool bytes, file count, parser failures, static separation and durable cleanup | Spill/prefix, empty-file, parser-I/O probes and static-handler source review; D2/D3/D6/D8/D9 | #713; A19 |
| Guest capacity and generation overlap | Four-instance page-limit probe, pinned wazero source, current guest-statistics tests, D4/D5 | #711; A20 |
| Truthful status, access and external boundaries | ADR status/permission contract and D7; existing APIs and #625/#628 handoffs inspected | #712; A22 |
| Preserve acceptance and historical evidence | ADR A1–A18, proposed A19–A22, recorded historical I1/I5/I2 checksum verification and the evidence manifest | I6b separate prompt |

## Independent reviews and verification

The independent ownership/security review found three High-severity design gaps (confidence
9/10): the spool root could overlap a static-serving tree; Coraza can swallow a multipart file
I/O error and continue phase 2 without Jul seeing a failure; and a writer can retain an audit
record whose mutable transaction collections are reset and reused. All three are in scope.
They were reproduced or confirmed against Jul and pinned Coraza source, corrected in the proposal,
and assigned explicit implementation tests below. The independent concurrency/protocol and final
whole-checkpoint reviews are still pending; no merge recommendation or independent approval is
implied until those complete.

| Check | Result |
| --- | --- |
| `CGO_ENABLED=0 go test -tags waf ./internal/waf -run '^TestAssessmentRequestBodySpill$' -count=1` | Passed on `go1.27.4 darwin/arm64` (4.513 s). The normal CGO path was blocked by the local unaccepted Xcode license; this is not a full-platform test. |
| Checkpoint-1 direct dependency probes | Passed five executions, including three audit samples per boundary execution and a synthetic I/O-failure/audit-alias probe; identities and raw results are in the evidence manifest. Not a qualification envelope. |
| `CGO_ENABLED=0 GOMAXPROCS=2 go build -p 2 ./...` and the full-tag build | Passed on the same host; exact full-tag command is recorded in the PR. |
| Focused inspection/WAF/plugin and instrument `go vet`; focused spill, guest-statistics and logging tests | Passed with CGO disabled; exact selectors and flags are recorded in the PR. |
| `make format-check`; `CGO_ENABLED=0 GOMAXPROCS=1 GOFLAGS=-p=1 make generated-check`; `make config-check` with the same Go environment and a private `TMPDIR` | Passed. |
| `CGO_ENABLED=0 GOMAXPROCS=1 GOFLAGS=-p=1 python3 scripts/docs-check.py` | Initial pass: 2,720 passed, zero failed; rerun after final review edits. |
| `sha256sum -c SHA256SUMS` for this checkpoint and referenced I1/I5/I2 evidence | Passed in the working tree; verify the exact candidate from a clean worktree before publication. |
| Full local `go vet ./...`, `go test ./...`, CGO WAF, race and platform lanes; current-head hosted CI | Not run locally or pending; do not infer a hosted result from the local checks. |

## Handoff rules

After the design PR merges, verify its actual merge SHA and current `main`, update this ledger and
start I3a on a fresh focused branch. If it is not merged, remain at checkpoint 1. Do not mark
I3b, I2b or I6a implemented merely because their designs or tracking issues exist; do not close
#700/#62 or claim final acceptance before I6b.
