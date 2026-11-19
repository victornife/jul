# #700 inspection implementation ledger

This is the single continuation record for the inspection-governor checkpoints. The accepted
authority is [ADR 0032](../adr/0032-aggregate-inspection-resource-governor.md); its
2026-11-13 amendment was **accepted** when #714 merged as `16d98258`. Historical reports and
measurements retain their original identities. Merged source is not a published release,
qualification, soak or maturity change.

## Reduced-scope closeout — 2026-11-19

The maintainer's [closeout amendment](../adr/0032-aggregate-inspection-resource-governor.md#2026-11-19-closeout-amendment--reduced-contract)
supersedes the programme merge gates and the historical HOLD below. #721 delivers
logical managed-guest accounting and the surfaced Coraza body-error guard, not
the original physical-memory or spool-ownership guarantees. A1–A22 is not a
merge prerequisite. A1 and A2 remain failed; original A19/A20 are deferred;
other historical objectives are unqualified where not established. ADR 0032's
A1 specified overload 503s within 50 ms; candidate p99 at C=16 was
60.342–60.814 ms, so that objective remains unmet. It was an internal target,
not a protocol requirement. This accepts the unqualified performance and
resource risk, not a claim that the old requirements passed.

On shared Linux/arm64 WSL2, the real candidate built from
`e8b6c0c42080255b8e5a29ec753651774409df2c` (production code unchanged through
`64bbd5a54fab92b8a70fa4b04380d660ac1a6690`) validated and served a combined
448 MiB WAF/two-plugin configuration. The earlier 320 MiB configuration failed
because the combined route required 406,503,427 bytes. The smoke used four
instances per plugin, a 2 s timeout, `GOMAXPROCS=2` and `GOMEMLIMIT=480MiB`;
it did not run in the historical 768 MiB cgroup.

- `jul-candidate check -config combined.toml` passed before startup.
- Healthy JSON and multipart requests reached the origin with both guest
  request and response echoes. WAF and WASM request denials returned 403
  without incrementing the origin counter. A WAF response denial returned
  403 after the expected origin dispatch.
- Removing the configured upload directory surfaced a stock Coraza multipart
  body error: detect mode with `ctl:ruleRemoveById=900002` returned 400, the
  origin counter stayed unchanged, and `inspection_error` increased by one.
  Restoring the directory and subsequent combined traffic succeeded; all six
  request lease, byte and evaluation gauges drained to zero.

The exact [configuration](evidence/2026-11-19-721-closeout/combined.toml),
[smoke driver](evidence/2026-11-19-721-closeout/smoke.py),
[check output](evidence/2026-11-19-721-closeout/check.txt),
[results](evidence/2026-11-19-721-closeout/smoke.txt) and
[checksums](evidence/2026-11-19-721-closeout/SHA256SUMS) are retained. The
captured paths and loopback ports must be adapted together for a different host.
The test binary SHA-256 was
`c96daf7a91d04f46f99148dd21859f62f6576a45558b255748fe67184090a902`;
the guest SHA-256 was
`27b016a775020dca6cea66fdb98a836d1afbf056b2ac9883c142e4969c41034a`.
This is functional evidence for that configuration, not A2 or a universal
memory-envelope fit.

The executed commands below used existing `upload` and `tmp` directories under
`C`; the origin and server ran as separate owned processes and were stopped
after the driver. The check and smoke driver passed.

```sh
B=/tmp/jul-721-qualification/bin
C=/home/victornf/.copilot/session-state/81ab5976-605d-4542-a731-cd69382adaff/files/pr-721-closeout
"$B/jul-candidate" check -config "$C/combined.toml"
"$B/jul-candidate" version-key init --file "$C/version.key"
"$B/ia-v5" origin -listen 127.0.0.1:32977
GOMAXPROCS=2 GOMEMLIMIT=480MiB TMPDIR="$C/tmp" JUL_EFFECTIVE_VERSION_KEY_FILE="$C/version.key" \
  "$B/jul-candidate" serve -config "$C/combined.toml"
python3 -u "$C/smoke.py"
```

Focused Go race tests for guest reservations, teardown and cancellation,
WAF errors and rule controls, status/settings, and reload passed on the same
production source. The earlier 101,000-call recycling regression and Linux
30-minute functional soak remain applicable. The macOS 30-minute soak remains
evidence for the guest lifecycle it exercised, but predates the independent
body-error guard in `60415e5a` and does not qualify that correction. Existing
real-server API/CLI/Console agreement and guided-settings tests cover the
operator surface.

The p1 run observed pre-existing contained guest-failure 500s on current main
(`16/2607`) and also on candidate (`18/3008`), with unresolved causes. Similar
counts do not establish the same cause; no evidence attributes them to #721.
H1 uninspected main/candidate median goodput was 5,219.6/3,466.0 requests/s
(33.6% lower), but candidate spread was 21.7%; the stabilized control had
36.7%/41.2% spreads and close medians. These shared-host measurements are
inconclusive, not proof of no regression. The
[original results](evidence/2026-11-19-721-current-main/README.md) remain intact.

| Proposed disposition after merge | Reduced-scope record |
| --- | --- |
| #724 | Supersede: its head `6a1a0f6b28caf9549066ab17f5cc6173cfeead6e` is an ancestor of #721, and the shared status, CLI, Console and guided-settings work remains present. |
| #712 | Close only after explicitly rescoping it to the verified existing operator surfaces and documentation; do not claim full A22, spool producers or the separate #625/#628 work. |
| #711 / #713 | Record the logical guest and surfaced-error controls as delivered; defer the original strict last-alias and spool-ownership guarantees, not as passed. |
| #700 | Retire the old umbrella as superseded by this reduced scope, without marking A1–A22 passed. |

These are proposed dispositions only; no issue has been closed. Final-head
required checks and coverage must pass before recommending merge.

### Operational closeout verification — 2026-11-19

The [synthetic guest diagnostic](evidence/2026-11-19-721-guest-diagnostic/README.md)
now reproduces the Go assessment guest's allocator OOM on both main
`155b6d94` and candidate `827be4f3`, using the byte-identical assessment WASM.
With two request-only plugins, 16 MiB per guest and default recycling, both
returned 994 successes and six contained 500s in 1000 requests; failed requests
did not reach the origin. Earlier recycling (`max_invocations = 16`) produced
1000 successes on each. Raw synthetic stderr shows `fatal error: out of memory`
through SDK `readInto`/`Request.Body`; production error logging stays bounded
and unchanged. This identifies a reproduced cause and a workload-specific
mitigation, without assigning a cause to every historical arm64 500 or changing
global defaults. Earlier recycling costs additional instantiations.

The uninspected-path performance gap is separate. At C=16 the reported median
was 33.6% lower; at C=8 the control medians were close (about +0.4% candidate),
with large spreads. Different concurrency cells cannot cancel each other, and
noise does not establish the cause. A concurrency-dependent regression remains
possible; performance is unqualified and explicitly accepted under the reduced
contract, not declared unchanged or explained by the guest OOM.

Focused race coverage confirms automatic admission recovery when successful
cleanup brings the unresolved count below 64. The overshoot regression also
covers 65-to-64 remaining blocked and 64-to-63 reopening. Merely re-entering
`closing` does not resolve ownership. Byte/local-instance limits still apply;
idle reuse is distinct from new reservations and retired plugins are not
reactivated. A module-close failure can be resolved by successful runtime
cleanup. A runtime-close failure is cached and not retried; reclaiming its
retained capacity requires process restart, potentially before 64 failures if
the byte ceiling is exhausted. Reload/mode changes do not erase that debt.
See the [operator recovery table](../plugins.md#guest-admission-recovery-and-restart).

| Resource or failure | Explicit boundary |
| --- | --- |
| Logical request/guest accounting | Host-buffer charges and calibrated WAF estimates are separate from page-rounded managed-guest reservations. These are admission controls, not measured process memory. |
| Physical memory | No hard heap/RSS, kernel-buffer, engine last-alias or reclamation-time guarantee. Deployment limits and workload sizing remain necessary. |
| Temporary storage | No complete aggregate byte/file/inode quota or crash-safe Coraza cleanup ownership; use external containment when needed. Memory-backed storage also needs memory headroom. |
| Coraza errors | Surfaced body errors are rejected before dispatch independently of rule controls and detect mode. Hidden/late errors are uncovered; an origin dispatch cannot be undone. |

Validation for this clarification: focused `-race` tests passed in
`internal/inspection`, `internal/plugins` and `internal/waf`, including
`TestFailedGuestTeardownsCanExceedBoundForAdmittedWork`, the existing
`TestFailedGuestTeardownBoundBlocksAdmissionUntilResolved`, recoverable module
close and sticky runtime-close tests, and surfaced-body-error bypass cases.
The archived reproducer driver was also executed successfully. Documentation
validation passed; final-head hosted checks remain the merge gate. Production
Go code, defaults and logging were not changed by this clarification.

### Applied mitigation and bounded performance check — 2026-11-19

Assessment plan v7 now renders `max_invocations = 16` for **both** plugins in
the heavy p1/p2/p2o/p2h and additional bounded s3/s4 scenarios. Runtime defaults
and ordinary/historical profiles remain unchanged. Tests cover scenario scope,
both renderers, default preservation and the complete scenario-to-runner config
path. The [bounded run record](evidence/2026-11-19-721-bounded-final/README.md)
retains the declared matrix, full-tag build identities, configs and all results.

Both actual binaries accepted the mitigated p1 configurations. Across the
initial and reverse-order profiled pairs, all **9065/9065** load requests were
verified with no unexpected responses, canary failures or request-accounting
residue. Candidate guest reservations matched idle ownership with no retained
failed tickets. This applies the mitigation rather than leaving it as advice;
earlier recycling's instantiation cost remains explicit.

The targeted H1/C=16 uninspected repeat had main/candidate median goodputs of
6691.15/6797.20 requests/s (+1.58% candidate); all three paired changes were
within 10%. Candidate spread was 13.19%, so this is **not** a formal A13 pass.
The old 33.6% drop did not reproduce on this CPU-separated Linux/amd64 host;
its historical ARM/WSL2 cause remains unproven. The initial saturated p1 sample
was 15.7% lower on candidate; a declared reverse-order CPU-profiled pair was
2.2% lower. Both profiles were dominated by external/JIT and wazero call-engine
execution, not a leading Jul accounting hotspot. These limited samples do not
justify a speculative runtime optimization or erase the original observations.

The local runner used a temporary first-party diagnostic profile and CPU
affinity, with no accessible cgroup quota/memory controls. No historical
physical-memory or whole-programme qualification is claimed. The maintained
verifier accepted all three result directories. The complete harness suite
passed at 91.3% statement coverage; all 11 measured added Go production
statements were covered (100%, above the 90% gate). Final-head hosted checks
remain the routine merge prerequisite.

## Current recovery status — 2026-11-17

**Historical status:** superseded by the reduced-scope closeout above.

The reduced-scope recovery implementation is published on draft #721; #724
remains a separate partial draft. Earlier head `7f6628c2` passed hosted HR-05
at 508/548 statements (92.7007%) and Codecov patch coverage at 99.06%. Its
Console audit failed on the upstream dependency advisory described below.
A corrective follow-up retains guest capacity on real close failures and
refuses new guest reservations at 64 unresolved guest tickets in every mode;
already-admitted work may add further failed tickets. Candidate prewarming
shares that safety boundary. Terminal runtime failures require a process
restart if the count cannot drain below the boundary.
The follow-up checks Coraza's transaction state independently of rule
execution, preserving existing interruptions; rule `900002` only supplies
diagnostic logging. API, CLI and Console expose the unresolved count and
mode-independent admission gate, and the guided editor supports the logical
guest-capacity limit. #721 already includes much of #724's shared
status and guided-settings work; #724 remains separate and needs its remaining
diff reconciled against this reduced scope rather than being treated as an
independent completed checkpoint.
The 2026-11-17 amendment in
[ADR 0032](../adr/0032-aggregate-inspection-resource-governor.md) governs this
scope; earlier fork-dependent plans and checks below are historical only. No
merge, issue closure or release has occurred.

### Current-main qualification follow-up — 2026-11-19

**HOLD at the time, superseded by the reduced-scope decision above:** the bounded paired comparison is noisy and does not establish no material regression;
the sized p1 guest workload produced unexpected 500s on both current main and the candidate.
The first A2 diagnostic could not render; its single targeted correction still failed the
actual combined-route config check. See the [campaign and all attempts](evidence/2026-11-19-721-current-main/README.md)
for exact SHAs, limitations and the next action. No production code was changed by this follow-up.
Historical A1/A2 remain failed; A1–A18/A21/A22 are not fully qualified. Original A19 and A20
remain deferred.

| Tracking | Reduced-scope evidence and disposition |
| --- | --- |
| #711 / A20 | Guest reservations, failure gate and recycling have focused/race and soak evidence; this is logical accounting, not retained-memory proof. Keep strict A20 deferred. |
| #713 / A19 | Upstream transaction-state body failure guard and control-flow bypass tests are covered; unreported/late failures and spool ownership are outside the contract. Keep strict A19 deferred. |
| #712 / A22 | Existing real-server API/CLI/Console tests cover enforcement, observe, drainage and rejected reload; focused tests cover the teardown gate and guided setting. Full A22 checklist/qualification is not established; keep open. |
| #724 | Separate draft overlaps status, CLI and settings already in #721; reconcile its remaining actual delta before recommending supersession; do not close it. |
| #700 | Remains open: comparison, p1 failure, combined diagnostic and other applicable programme criteria remain unresolved. |

| Disposition | Scope |
| --- | --- |
| KEEP | Existing process governor, request leases, WAF/plugin accounting, audit ownership, reload publication/version checks, candidate isolation, exact no-op comparisons, and #724's shared API/CLI/Console, RBAC, accessibility and navigation work. |
| SIMPLIFY | Guest tickets reserve page-rounded ceilings for Jul-managed starting/live instances, with a 1 GiB parsed-config default. Stock cancellation remains enabled. The reservation excludes asynchronous engine-retained references and is not physical memory/RSS. |
| SIMPLIFY | Jul checks stock Coraza's public request-body error state before origin dispatch, independently of SecLang control flow. The generic flag does not identify a storage cause and does not guarantee all I/O failures are detected. |
| REMOVE | Wazero/Coraza local replacements and copied source, provider-specific temporary-file plumbing, spool controller/debt/recovery machinery, unsupported spool settings/metrics/API/UI, and replacement-only build/coverage machinery. |
| DEFER | Original A20 last-alias/retained-memory proof; original A19 exact aggregate spool byte/file quotas, Jul-owned file lifecycle, typed storage failures and crash recovery. No renamed substitute is accepted. |

| Work item | Current disposition |
| --- | --- |
| #711 / original A20 | Managed logical capacity is implemented in the draft; original A20 is deferred, not passed. |
| #713 / original A19 | Public-transaction-state failure guard is the reduced scope; original A19 is deferred, not passed. |
| #712 / A22 | Real API/CLI/Console agreement has focused coverage; full qualification remains pending. |
| #700 | Revised scope only; A1–A18/A21/A22 qualification and integrated acceptance remain pending. |

The reduced-scope recovery changes are implemented in the recovery branch.
The following checks describe the published recovery head unless otherwise
specified; hosted checks for subsequent changes must be re-run:

| Verification | Result |
| --- | --- |
| `PATH="$HOME/go/bin:$PATH" make ci-pr` | Passed on the corrective production code with CI-pinned tools; includes full-tag lint/tests/build, security gates, `go vet`, generated/config checks and 2,726 documentation checks. The subsequent admitted-work overshoot test and documentation edits were verified separately. |
| `go test -race -count=1 -p 2 -tags 'wasmplugins waf' ./internal/inspection ./internal/plugins ./internal/waf ./internal/server` | Passed on the corrective follow-up. Includes the 101,000-call guest recycling regression and reload/WAF interactions. |
| HR-05 CI-equivalent lean/full added-Go coverage | Hosted `8e151dff`: 493/538 statements (91.6357%). The preceding published head scored 474/538 (88.1041%). |
| Corrective follow-up Go coverage | Hosted `7f6628c2` HR-05 scored 508/548 added statements (92.7007%); the corrective diff scores 54/58 statements (93.10%) locally across inspection, plugins and observability. |
| `go test -race -count=1 ./internal/inspection -run '^TestFailedGuestTeardown'` | Passed; covers the failure threshold, observe/unlimited admission, retained-closing transition and previously admitted failures above the threshold. |
| `JUL_INSPECTION_SOAK_DURATION=5s go test -race -tags 'soak waf wasmplugins' -run '^TestInspectionCombinedSoak$' -count=1 -timeout=120s -v ./internal/app` | Passed as a functional preflight. The combined 30-minute run and Linux envelope qualification must be reported separately. |
| `JUL_INSPECTION_SOAK_DURATION=30m GOMAXPROCS=2 go test -tags 'soak waf wasmplugins' -run '^TestInspectionCombinedSoak$' -count=1 -timeout=40m -v ./internal/app` | Passed on macOS/arm64, Go 1.27.4, source `86968176c476084bcec5da6133c345a293c5f098`: 30m0.0367s, 60 reloads, 43,203 successful requests (14,401 each GET/form/multipart), 14,400 expected denials and zero blocked requests at origin. Maximum successful requests per generation was 719, demonstrating recycling; managed counters drained at shutdown. |
| Guest-refund and body-failure mutations | Both were detected by focused tests using temporary Go overlays. Removing the body-failure check forwarded the reproduced multipart request to the origin, demonstrating that the guard regression is effective. |
| `go test -race -tags wasmplugins ./internal/plugins -run '^TestGuestAccounting(ResolutionPaths\|RejectsInvalidOwnershipTransitions)$' -count=1` | Passed; covers valid resolution, retained teardown, missing ownership and invalid lifecycle transitions. |
| `corepack pnpm test:coverage`; `corepack pnpm typecheck`; `corepack pnpm lint` | Passed: 1,087 UI tests; changed production TS/TSX executable-line coverage is 95/100 (95%). |
| `corepack pnpm e2e e2e/real-server.spec.ts` | Passed: 32 tests. |
| `./node_modules/.bin/playwright test --project=inspection-real-server e2e/inspection-real-server.spec.ts` | Passed on macOS with stock WAF/WASM: compares actual guest reservation and inspection status across API, CLI and Console, including refusal, observe, drainage and a rejected capacity reload. |
| `python3 scripts/check-d11-certification.py` | Passed: 0 errors. |

The focused adversarial review identified unbounded retained owners in
observe/unlimited configurations and an overly aggressive admission gate that
could prevent recovery after a single failure. The corrective gate allows
recovery below the 64-ticket threshold, reports its mode-independent refusal,
and documents the restart boundary and admitted-work overshoot. Its final
accounting re-review found no blocking defect.

The [functional soak output](evidence/2026-11-17-700-functional-soak.txt) has
SHA-256 `03e0accc72b7b6cf7550a96b442332ffea954a25844eb2a036b6e4604e4b9bee`.
Its configuration and guest fixture are constructed by the source test; the
ephemeral Go test binary was not retained or hashed. Wazero v1.12.0 and Coraza
v3.8.1 were unmodified. Later head `adae382e72bd9d3ad956c626496d23dce649f372`
changes only the browser test and this ledger, not production code. The run is
functional evidence, not Linux memory or throughput qualification.

Head `adae382e` had 63 successful hosted checks, two skips and one failure;
the coverage gate passed and Codecov reported 99.06% patch coverage. Its
Console audit failed on the unchanged UI lockfile's
transitive `picomatch@4.0.7` (GHSA-fc5q-39xc-vjpf). The advisory lists `4.0.8`
as patched, but that version is not available from the public npm registry.
The subsequent pnpm configuration accepts only this advisory; a CI check
rejects `picomatch` in the production dependency tree, while other advisories
remain enforced. Untrusted glob patterns can still hang development or build
tools. Remove the exception and production check when a patched release is
available; no dependency patch was added. The [assessment commands](../../scripts/inspection-assessment/README.md#combined-functional-soak)
describe how to reproduce the soak. At that stage Linux/cgroup qualification
was unrun because the host was macOS and its Docker daemon was unavailable.
The later Linux work is recorded below. The integrated campaign remains
outstanding; do not claim original A19/A20 completion or close
#700/#711/#713/#712 based on these checks.

## Linux follow-up — 2026-11-19

The production candidate was built from clean commit
`60415e5a71fd8ec26a7559424e3f43e6406738fe`. Follow-up `79d8797f` changes
only the D11 certification asset inventory. The host is shared Linux/arm64
WSL2 with cgroup v2 and a systemd user manager. The paired assessment binaries
use baseline `858590dc7c430d24cd33b096a81ca6051591b427` and the candidate,
both built with Go 1.27.4, full tags, static linking, `-trimpath` and matching
strip/version flags. The clean harness and both binary identities are recorded
in the [preflight manifest](evidence/2026-11-19-721-linux/preflight/manifest.json).
An earlier one-target smoke used a Go 1.27.3 baseline; it was not used for
comparison. The baseline was rebuilt with the candidate's Go version before
the paired preflight and qualification runs.

The focused test binaries were compiled from the clean `79d8797f` checkout
with Go 1.27.4 and full tags. Their SHA-256 values were
`82a37ccb6bf0701b214a637f401662902993d60f4c5bdf814581b9e54f475d47`
(`internal/inspection`) and
`b528ab7232ede76891f751ba96a4dfcaf32e58b97195ac667e41f48563ff9bb0`
(`internal/waf`); the binaries themselves were not retained as evidence.

| Verification | Result |
| --- | --- |
| `taskset -c 4-9 env PATH="$HOME/go/bin:$PATH" GOMAXPROCS=2 make ci-pr` | Passed: full-tag tests, lint, security coverage, vulnerability checks, vet, generated/config checks and 2,726 documentation checks. The D11-only inventory correction was committed during the run. |
| `GOMAXPROCS=2 go test -race -p 2 -tags 'waf wasmplugins console' -count=1 -run '^(TestFailedGuestTeardown.*\|TestInspectionStatusReportsLiveGuestTeardownGate\|TestGlobalSetGuestCapacityOnly\|TestGovernedWAFBodyProcessingFailure.*\|TestGovernedWAFMissingBodyErrorStateDoesNotDispatch\|TestRequestBodyProcessingFailureRequiresTransactionState\|TestWAFRequestVsResponseInterruptionAndBodyErrors)$' ./internal/inspection ./internal/admin ./internal/waf` | Passed, including rule-removal, skip and allow counterexamples and the live teardown-gate status. |
| `taskset -c 10,11 env JUL_INSPECTION_SOAK_DURATION=30m GOMAXPROCS=2 go test -p 2 -tags 'soak waf wasmplugins' -run '^TestInspectionCombinedSoak$' -count=1 -timeout=40m -v ./internal/app` | Passed: 30m0.035s, 60 reloads, 43,165 successful requests (14,389 GET, 14,388 form, 14,388 multipart), 14,389 expected denials and zero blocked requests at origin. [Output](evidence/2026-11-19-721-linux/functional-soak.txt). This functional run was CPU-pinned, not run in the 768 MiB cgroup. |
| Focused governor and WAF test binaries, three repetitions | Passed in a verified 2-CPU, 768 MiB, zero-swap cgroup with `GOMAXPROCS=2`; includes H1/H2/H3, cancellation, audit abandonment, body-error and teardown-gate cases. [Raw output](evidence/2026-11-19-721-linux/focused-cgroup-tests.txt). These are functional tests, not the integrated acceptance matrix. |
| Assessment harness quick preflight, one `none` cell per target | Passed with `memory.max=805306368`, `memory.swap.max=0`, `cpu.max=200000 100000` and Jul pinned to CPUs 0–1. Both cells and [checksums](evidence/2026-11-19-721-linux/preflight/SHA256SUMS) verified. This three-second profile is smoke evidence, not a throughput comparison. |
| `taskset -c 0-3 node --test scripts/coverage-report.test.mjs scripts/audit-exception.test.mjs && taskset -c 0-3 pnpm exec vitest run --coverage --maxWorkers=2 && taskset -c 0-3 node scripts/coverage-report.mjs` (from `internal/admin/ui`) | Passed, 1,088 UI tests and the coverage contract. An earlier run under concurrent load failed a timing-sensitive Overview test. |
| `xvfb-run -a env CI=1 GOMAXPROCS=2 pnpm exec playwright test --headed --project=inspection-real-server --repeat-each=1 --retries=0 --reporter=list` (from `internal/admin/ui`) | Passed against the packaged server. Headless Chromium could not advance animation frames on this host, even on a minimal page; Xvfb with headed Chromium was used instead. |
| `python3 scripts/check-d11-certification.py`; `python3 scripts/test_d11_certification.py` | Passed with zero inventory errors and 22 tests against a clean tracked-file export containing the inventory correction. |

[Hosted `docs-check`](https://github.com/victornife/jul/actions/runs/44517856150/job/135538442096)
at `60415e5a` failed because the Console rebuild changed 36 content-hashed
asset paths. `79d8797f` reconciles D11-104 current paths and records the
exact mappings without recertifying historical evidence.
[Its hosted D11 stage](https://github.com/victornife/jul/actions/runs/44519048757/job/135542755065)
passed, but the same job later failed the unrelated
`test_dashboard_backend_endpoints_counters_and_idle_websocket_survival` on a
greeting-read timeout. The
[rerun](https://github.com/victornife/jul/actions/runs/44519048757/job/135554050697)
passed. At `79d8797f`, all checks completed with 63 successes and two skips;
Codecov patch coverage was 98.48% (90% target). Checks on the later evidence
commit must still be recorded.

The [A1 `s11` run](evidence/2026-11-19-721-linux/a1-s11/) and its
[summary](evidence/2026-11-19-721-linux/a1-summary/summary.md) were verified
after three interleaved repetitions per variant and target. The frozen
`s11/waf` fixture uses a 512 MiB inspection budget and leaves `GOMEMLIMIT`
unset. The functional soak overlapped the first two A1 repetitions on separate
CPUs; shared-host interference remains possible:

| Candidate `waf` | Rep 1 | Rep 2 | Rep 3 | Median (range) |
| --- | ---: | ---: | ---: | ---: |
| Cgroup peak, MiB | 627.1 | 666.5 | 529.9 | 627.1 (529.9–666.5) |
| C=16 legitimate goodput, requests/s | 7.6 | 6.7 | 5.4 | 6.7 (5.4–7.6) |
| C=16 overload 503 p99, ms | 60.814 | 60.342 | 60.649 | 60.649 (60.342–60.814) |
| C=16 overload 503 count | 7,590 | 7,197 | 5,845 | 7,197 (5,845–7,590) |

The candidate survived without an OOM-kill delta, but two peaks exceeded
576 MiB and all three C=16 overload p99s exceeded 50 ms. C=16 goodput spread
was 32.8% of its median; shared-host noise is not a pass. In all three
baseline `waf` repetitions the process was killed at C=8 and the hierarchical
parent cgroup's OOM-kill count increased by one; that count can include sibling
scopes. The frozen fixture does not list overload 503s as an expected class,
so the harness also records them as findings. Their counts and latencies
remain in the raw evidence. A1 is **not passed**.

The [normal-traffic comparison](evidence/2026-11-19-721-linux/a13-normal/)
completed three repetitions for all 11 cells per target: all 66 results were
instrument-valid and correct. The frozen-plan
[summary](evidence/2026-11-19-721-linux/a13-summary/summary.md) reports six
`fail-regression` and five `inconclusive-noise` verdicts, with no passing
comparisons. For example, H1 `none` median goodput was 5,167.2 requests/s
for the baseline and 3,740.8 for the candidate (27.6% regression); candidate
spread was 90.5%. The verdict order treats a median regression over 10% as a
failure before applying the noise rule. These comparisons are informational,
not A13 acceptance; shared-host variability must be investigated rather than
discarded.

The [post-soak A1 diagnostic](evidence/2026-11-19-721-linux/a1-post-soak/)
interleaved three repetitions of the pre-fix PR head `ffbff4d0` and the
corrected candidate, built with matching flags. No functional soak overlapped
this run. Both heads exceeded the A1 limits in every repetition, without an
OOM-kill delta:

| `s11/waf` | Cgroup peak MiB, median (range) | C=16 overload 503 p99 ms, median (range) | C=16 goodput requests/s, median (range) |
| --- | ---: | ---: | ---: |
| Pre-fix `ffbff4d0` | 599.7 (598.4–603.0) | 60.964 (60.814–61.744) | 5.7 (5.4–7.7) |
| Corrected `60415e5a` | 603.1 (599.2–604.6) | 61.446 (60.609–74.048) | 5.9 (4.7–6.6) |

This reproduces the threshold overruns on both heads in this environment; it
does not identify their cause. The pre-fix head retains the rule-control
fail-open and is not an acceptance baseline.

The A2 [s3](evidence/2026-11-19-721-linux/a2-s3/) and
[s4](evidence/2026-11-19-721-linux/a2-s4/) runs each exercised `none` and
`both` for three repetitions per target. The
[combined summary](evidence/2026-11-19-721-linux/a2-summary/summary.md)
retains invalid and failed cells. On s3 `both`, the candidate survived with
no OOM-kill delta and peaked at 518.7–539.2 MiB; the baseline process was
killed with a parent OOM-kill increment in all three repetitions. The s3
fixture also marks candidate overload 503s as unexpected. On s3 `none`, the
baseline and candidate median legitimate goodput was 318.35 and 232.35
requests/s respectively (27.0% regression); spreads were 15.1% and 65.5%.
Removal of a temporary worktree overlapped the first candidate s3 cell's
startup; the shared host can add other interference.

On s4 `both`, all three candidate processes were killed during C=128 or
C=192 with a parent OOM-kill increment; their cgroup peaks were 712.6, 740.7
and 763.1 MiB. All three baseline processes were also killed with a parent
OOM-kill increment, and two baseline cells were instrument-invalid because
no attempts started in the C=16 and C=32 windows. The hierarchical parent
counter can include sibling scopes. **A2 is not passed.** Reduced-scope
A1–A18/A21/A22 qualification remains incomplete. Original A19/A20 remain
explicitly deferred.

## Historical evidence — fork-dependent #721/#713 draft (superseded)

The following implementation notes and test results describe the earlier
fork-based proposal. They are preserved for traceability and do not qualify or
describe the current recovery implementation.

### Historical I3b-I2b Implementation Review (superseded)

Status updated 2026-11-17.

The current draft implements the selected bounded approach with source-pinned local replacements:
Coraza v3.8.1 provides a transaction-scoped temporary-file seam for body spill and multipart, and
Wazero v1.12.0 joins cancellation-watcher completion and cleans up resources acquired by failed
instantiation. Jul owns the logical spool quotas, tickets, sticky failures, cleanup debt and
restart-bound spool root. Spool byte/file limits are limits-only reloadable; changing `spool_dir`
still requires the documented offline relocation procedure. The upstream tag targets and recorded
commit IDs were verified to match.

The three adversarial passes identified (1) guest teardown and failed-start ownership, (2) spool
root/lock recovery and cleanup-debt boundaries, and (3) operational status and reload contracts.
The implementation now joins the Wazero watcher before refund, closes failed-start resources,
documents fail-closed lock recovery as an offline operator action, and classifies both spool quotas
as limits-only changes. This update adds separate byte/file reload regressions. A final integrated
diff review and the hosted checks for the final published head are still required.

| Verification | Result |
| --- | --- |
| `make security-gates` | Passed, including lean/full-tag security tests and package coverage floors. |
| `go test ./internal/server -run '^TestLimitsOnlyPublicationAvoidsHandlerRebuild$' -count=1` | Passed; independently publishes spool byte and file limits without changing the serving generation or rebuilding handlers. |
| `go test -tags 'brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf' ./...` | Passed. The first run exposed unnormalized programmatic test configs; shared server fixtures now set the parsed spool-root default, and WAF fixtures use their controller's private root. |
| `go test -tags 'brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf' ./internal/server ./internal/app` | Passed after correcting the spool-root fixtures. |
| Coraza provider, Wazero watcher/failed-instantiation, and 100,000-call guest recycling focused tests | Passed. Exact commands are recorded in the dependency patch records. |
| `go test -tags waf ./internal/waf -run '^TestGovernedWAFMultipart(FileQuota\|CreateFailure)RefusesBeforeOriginDispatch$' -count=1` | Passed on macOS: quota refusal/observe and an induced multipart file-creation failure across CRS on/off, WAF block/detect and governor enforce/observe. The creation failure is rejected before origin dispatch in every case. |
| `make generated-check` | Passed. |
| `make format-check` | Passed. |
| `GOMAXPROCS=2 python3 scripts/docs-check.py` | Passed; 2,729 checks, 0 failures. |

These results do not qualify A19 or A20. The 30-minute combined soak and A1–A22 integrated
campaign have not run, and the Codecov threshold for the final diff remains unverified. A full
upstream Coraza test run completed but reports 62.0% aggregate package coverage; the attempted
Wazero all-package test run is incomplete because this source replacement omits CLI and generated
example fixtures. The focused dependency patch tests, rather than those incomplete aggregate
figures, remain the current dependency-test evidence.

### 2026-11-16 follow-up

#725 merged as `dced01786a50454d09ecb0e9ceab1c7b5664ede1` from head `f8d2af27`
with 57 successful checks and two skips. The I3b containment evidence below is
historical research, not A19 completion; #713 remains blocked.

Draft #721 at `0f8d28c4` retains unconfirmed guest reservations for the process
lifetime. This avoids the reproduced premature refund but normal churn can
exhaust a finite enforce limit, so A20 and #711 remain blocked. Its
[public-API close probe](https://github.com/victornife/jul/tree/0f8d28c4706a8231e38283cf14dee49640106b4d/docs/reviews/evidence/2026-11-16-700-i2b-public-close)
observed that closing infinite guests with cancellation watching disabled did
not interrupt them within 750 ms in either engine; this is not a general
last-alias proof. Draft #724 at `0e76f990` remains partial, pending stable guest
and spool producer contracts. These identities supersede older draft and main
identities in the table and historical text below.

| Item | State |
| --- | --- |
| Starting `main` | `d06538cecc1398b6e8bf6302b553f873cd44e941` (verified #717 merge, after I4) |
| Latest verified `main` at this update | `dced01786a50454d09ecb0e9ceab1c7b5664ede1` (#725 I3b containment evidence) |
| Previous I2b head | `81337764ecff8e8637900508c8c29bb2d8273ac4` (conservative debt prototype; draft continues below) |
| Checkpoint 1 | **Merged**: #714 merged as `16d98258` from head `faf3ead4` after green hosted checks; the amendment is accepted |
| Checkpoint 2 | **Merged**: I3a WAF request accounting, #715 merged as `def419f5` from head `e7ea422e`; see [I3a implementation record](#i3a-implementation-record) |
| Checkpoint 3 | **Merged**: I4 response accounting, #716 merged as `daa06bdc` from head `d879cba9` after all required hosted checks passed |
| Checkpoint 4 | **Blocked**: I3b spool protection (#713) requires the supported Coraza boundary and ownership/default decisions recorded below |
| Checkpoint 5 | **Blocked**: the prior I2b draft retained unconfirmed guest capacity until process exit; normal churn could exhaust a finite enforce limit, so A20 remained open |
| Checkpoint 6 | **Partial, blocked**: draft #724 projected existing inspection status; full I6a #712 still required stable guest/spool contracts |
| Existing foundations | I1 #706 (`a57afa4d`), I5 #708 (`d8d0520b`), I2 #710 (`c8bba7f2`) and I3a #715 (`def419f5`) merged |
| Programme owners | #700 and #62 remain open; no A1–A22 acceptance is claimed |
| Evidence | [2026-11-15 I2b watcher reproduction](evidence/2026-11-15-700-i2b-watcher/README.md); [2026-11-13 checkpoint-1 probes](evidence/2026-11-13-700-checkpoint-1/README.md); historical [I1](evidence/2026-11-10-700-i1-base-charge/README.md), [I5](evidence/2026-11-12-700-i5/README.md), and [I2](evidence/2026-11-12-700-i2/README.md) |
| Exact next action | Review the I2b process-lifetime debt tradeoff and contract/default decisions; resolve I3b integration prerequisites |

## Checkpoint schedule and dependencies

| Checkpoint | Scope / status | Actual prerequisite and next owner |
| --- | --- | --- |
| 1 — design | Merged (#714, `16d98258`); amendment accepted | None |
| 2 — I3a | Merged (#715, `def419f5`): WAF request accounting, slots, request/audit calibration | Complete for this checkpoint; response calibration followed in I4 |
| 3 — I4 | Merged (#716, `daa06bdc`): WAF and plugin response paths, response audit calibration | Complete for this checkpoint; I3b remains next in the schedule |
| 4 — I3b | Blocked: inspection spool bytes/files and restart/cleanup ownership; #713 | Accepted storage design, WAF integration, a reviewed Coraza interception boundary, supported finite defaults and safe directory/recovery design; implementation and upstream-integration owners |
| 5 — I2b | Blocked: the unmerged draft did not prove guest-memory ownership and process-lifetime retention could exhaust finite budgets; #711 | Supported last-alias teardown, interrupt behavior, failed-start drainage and a measured finite default |
| 6 — I6a | Partial draft #724: operational API/CLI/Console for existing resources; #712 remains blocked | Stable implemented resource contracts and real producer signals |
| I6b — separate prompt | Qualification pending | Integrated source, frozen plan and A1–A22 campaign; qualification owner |

The maintainer requested publication of the I2b prototype as draft #721 while I3b remains blocked.
This exception does not make the prototype merge-ready or change its acceptance requirements.
The child issues were created after checking existing tracking and linked to #700. The scheduling
order does not itself create technical dependencies between unrelated resources.

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

On 2026-11-16, a bounded investigation on macOS with Coraza v3.8.1 found that
the default `TMPDIR` contained both spill and multipart files in the tested case,
but an explicit `SecUploadDir` redirected multipart files elsewhere. An induced
multipart create failure reached the origin in Jul block mode without CRS and
detect mode with CRS; block mode with CRS returned 400. A private, finite HFS+
image reached ENOSPC for byte writes, but the experiment did not demonstrate a
separate inode quota. Linux was not tested. See the [experiment evidence](evidence/2026-11-16-713-physical-containment/README.md)
for exact identities, cases and limits. These results support only operational
containment, not A19 or reliable fail-closed handling in all profiles. The
`no_fs_access` build tag changed existing body-processing behavior and is not
recommended as a default. #725 merged as `dced0178`, documenting containment
without changing the runtime. #713 remains blocked pending a supported complete ownership boundary
or another conservatively proved solution.

## I2b blocker and reload safety follow-up

Draft #721 contains the guest-capacity prototype and reproducible
[watcher evidence](https://github.com/victornife/jul/tree/14ebcb20/docs/reviews/evidence/2026-11-15-700-i2b-watcher)
and [join experiment](https://github.com/victornife/jul/tree/14ebcb20/docs/reviews/evidence/2026-11-16-700-i2b-join).
The pinned wazero v1.12.0 can retain guest memory in an unjoined cancellation watcher after a
premature refund. Draft head `81337764` retains unconfirmed reservations for the process
lifetime instead, but ordinary churn can exhaust a finite enforce limit. The isolated join experiment does not prove absence of all aliases or
failed-start debt drainage and is not a supported production fix. A20, the finite guest default,
the remaining adversarial review, and integrated qualification remain open.

Independent reload corrections discovered during the I2b investigation were merged in #722 as
`ed775fd5ce9836cafb8c137dab25231fb8ccc10f` from reviewed head `bda851355b46019d4d3e88ad11093917d8e6d206`.
They prevent false no-ops for order-sensitive or duplicate declarations, bind authentication and
WAF to the owning route declaration, preserve duplicate backend state, and correct previews.
Hosted CI reported 66 successful checks and two skips. The draft incorporated these changes at
`14ebcb20`; they do not fix the guest or spool ownership blockers. The authoritative summary
merged in #723 as `9c70b64d`.

## I2b implementation progress

### Local process-lifetime debt alternative

The current local worktree on `cbdc0c17` uses public wazero APIs and keeps every
reserved attempted guest charged after successful module or runtime close. It
does not assume the cancellation watcher has terminated, including when a
failed start returns no module. `teardown_unconfirmed` is an irreversible
process-scope aggregate counter; successful teardown frees the plugin's local
instance slot and drops the ticket object, not its charge. Close failures
remain `failed_retained` and retain their owner/error. Active calls and pool
handoffs remain protected until they finish. Normal cancellation remains
enabled. The counter need not represent memory still allocated.

The local slot can be reused after a successful close or an on-demand failed
start that returned no module, even if a watcher may still retain memory;
the separate process reservation remains charged. An actual close error keeps
the slot as well as the charge. The unconfirmed watcher alone does not block
the local slot.

**B1 — blocking availability defect:** a finite enforce cap may be exhausted
by normal churn, traps and failed candidates; existing pooled instances can continue, but new instances and
reload candidates are refused. A limits-only increase can add headroom without
releasing debt; restarting the process resets it. Observe continues admission
over the cap and therefore does not bound guest capacity. Omission remains
unbounded; there is no evidence for a finite default. Memory outside guest
linear-memory ceilings and RSS are not bounded by this control. This is
not acceptable normal enforce behavior; it does **not** establish original A20
completion, eventual drainage, or an ADR amendment. For example, with one
sequential pool, 16 MiB per guest, `max_invocations=1000` and a 256 MiB cap,
16 retired guests exhaust the cap after approximately 16,000 guest calls;
concurrency, errors or reloads can exhaust it sooner. #721 remains draft;
this finding is unresolved, and integrated qualification remains open.

**NB2 — corrected lifecycle API:** `ResolveTeardown(nil)` previously refunded
from `starting`, `idle`, `busy` and `failed_retained`, and
`ResolveTeardown(err)` could incorrectly move a live ticket to
`failed_retained`, enabling a later close/refund. The focused regressions
reproduced both bypasses. Resolution now requires `closing`; repeated failures
in `failed_retained` and repeated success in `closed` are idempotent. Runtime
close moves idle/retained tickets through `closing` before recording errors,
preserving both module and runtime errors without accumulating duplicates on
retry. Successful closes still enter `teardown_unconfirmed`. This correction
does not resolve B1.

Local validation on macOS arm64, Go 1.27.4 (with `TMPDIR` and `GOTMPDIR`
set to a repository-local scratch directory except where noted):

- `go test -race -count=1 -tags wasmplugins ./internal/inspection ./internal/plugins ./internal/observability`:
  passed after the lifecycle corrections, using the shell's inherited environment.
- After the `ResolveTeardown`/`closeRuntime` state-machine corrections,
  `TMPDIR="$PWD/.nb2-review-scratch" GOTMPDIR="$PWD/.nb2-review-scratch" go test -race -count=3 -tags wasmplugins ./internal/inspection ./internal/plugins -run '^(TestGuestResolveTeardownRejectsPrematureResolution|TestGuestTicketResolveTeardown|TestGuestUnconfirmedTeardownCannotBeRefunded|TestGuestRuntimeCloseFailureFromIdleRetainsDebtOnRetry|TestGuestCapacityRemainsChargedUntilRuntimeCloseAfterModuleCloseFailure|TestFailedGuestCandidateReportsPriorRuntimeCloseFailure|TestGuestCapacityRetainsModuleCloseFailureAfterSuccessfulRuntimeClose|TestGuestCapacityDoesNotForgetFailedRuntimeCloseOnRetry|TestManagerKeepsRuntimeCloseErrorWithoutGuestTickets|TestGuestCapacityRemainsChargedDuringPoolHandoff)$'`:
  passed both packages.
- After those corrections,
  `TMPDIR="$PWD/.nb2-review-scratch" GOTMPDIR="$PWD/.nb2-review-scratch" go test -tags wasmplugins ./internal/inspection ./internal/plugins`:
  passed both packages.
- The following watcher, broader race, full-tag and vet results predate the
  subsequent `ResolveTeardown`/`closeRuntime` state-machine corrections:
- `bash docs/reviews/evidence/2026-11-15-700-i2b-watcher/reproduce-retained.sh`
  and the same command with `PROBE_RACE=1`: passed both paused-watcher cases
  using the original script; the subsequently modified script has not run.
- `go test -race -count=3 -tags wasmplugins ./internal/plugins ./internal/inspection ./internal/observability -run 'TestGuest(Capacity|Retirement|Startup|Cancellation|Unconfirmed|Teardown)|TestFailedGuestCandidate|TestInstanceStartRespectsRequestCancellation'`:
  passed, including repeated failed candidates in enforce and observe modes.
- `go test -race -count=1 -tags 'wasmplugins waf grpc http3' ./internal/inspection ./internal/plugins ./internal/observability ./internal/app ./internal/server ./cmd/jul`:
  passed before the final two focused tests and before the subsequent
  `ResolveTeardown`/`closeRuntime` corrections.
- `go test -p 2 -tags 'brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf' ./...`:
  passed before the final two focused tests and before the subsequent
  `ResolveTeardown`/`closeRuntime` corrections. Its first run failed the
  config-description contract after an edit; restoring the expected first
  sentence fixed it, and the full rerun passed.
- `go vet -tags 'wasmplugins waf grpc http3' ./internal/inspection ./internal/plugins ./internal/observability ./internal/app ./internal/server ./cmd/jul`:
  passed before the state-machine corrections.
- After those corrections, `python3 scripts/docs-check.py`: 2727 passed, 0 failed.

The historical blocker and prior test results below describe the earlier
prototype; they are not claims about the local conservative implementation.

### Historical implementation and blocker

The independent I2b branch includes `bea7ef8b`, which rejects sub-page plugin memory limits and
effective limits above 4 GiB, and `8c4ec5f1`, which propagates request cancellation into on-demand
guest startup and closes an instance that finishes starting after cancellation. A deterministic
host barrier reproduced the cancellation failure before the fix.

The current implementation adds process-wide guest-capacity tickets for each starting/live
instance, records generation ownership, preserves reservations after failed teardown, and reports
bounded state and capacity statistics across live and retained plugin owners. Reuse does not
double-charge. `max_guest_capacity_bytes` remains optional and omission tracks without enforcing;
no finite production default is approved.

Both limits-only reloads and full candidate reloads now use expected-version publication. The
limits-only path avoids rebuilding handlers or guest instances, while a stale full candidate fails
before the serving handler/config commit. Candidate guest startup continues to use only published
capacity. Ownership, cancellation, overlap, retained-close, saturation, and stale-publication tests
cover these paths. Enforce validation also requires the proposed cap to fit one eager instance of
each configured plugin; observe warns instead. This check is compile/metadata-only and runs
before limits-only publication or candidate warm-up. Starting candidate instances are included in
the manager's capacity statistics. The operator and generated configuration descriptions also
distinguish the already-merged WAF/response accounting from still-blocked spool protection.
The operability pass also corrected stale configuration, metric and ownership descriptions that
still said WAF-only body reads did not use `body_timeout`; the WAF H3 acceptance remains deferred.

An earlier independent ownership/security pass found no vulnerabilities in its then-current diff.
The concurrency review found a ticket leak on failed instantiation, a race-prone pool drain, and
related shutdown accounting/metrics issues. A later security pass found the high-severity reload
classification issue below. Further lifecycle review reproduced premature refund while a host call
remained active and loss of the first runtime-close error on a retry. The local fixes retain active
owners and close errors and add deterministic regressions. An intermediate timeout-retention
approach failed existing timeout and slot-drain tests and was replaced by isolating startup from
inherited experimental wazero facilities; the pinned-runtime teardown assumption and compatibility
consequences still require fresh independent confirmation.

The latest concurrency reviewer checked the pool-handoff correction and cleared-module alias fix
against the pre-watcher-probe worktree, and found no blocker in those paths. Its review did not
include the deterministic wazero watcher barrier added afterwards. The barrier supersedes that
review's non-blocking classification of retained closed memory: the measured live heap alias can
outlast the refund and violates A20. A second follow-up reviewer failed before completing its
requested work. A fresh security/ownership review of the final diff is pending; neither earlier
result is approval of the current blocked head.

| Finding and counterexample | Local correction and regression |
| --- | --- |
| **High, confidence 9/10:** keyed lifecycle extraction can miss overlapping-regex order or duplicate route changes. Without a limit change, a security-restricting reload was classified as a no-op; with one, the new config published while the old router still returned `200` rather than `403`. | Require exact equality outside the permitted limits for limits-only publication, and compare server blocks in order for ordinary no-ops. `TestLimitsOnlyReloadDoesNotHideRouteOrderChange` exercises both route changes, with and without a limit change, on a real HTTP server. |
| **Medium:** keyed upstream extraction can hide a weight change in the first of several identical backend addresses. A weighted-round-robin reload was incorrectly reported as a no-op, leaving traffic distributed 6:3 instead of the configured 8:1. | The original multiset comparison was superseded by #722's ordered comparison because backend order can affect tie-breaking. `TestReloadDoesNotHideDuplicateUpstreamWeightChange` checks the applied distribution and publishes the reordered generation. An isolated overlay removing the comparison reproduced the weight failure. |
| A closed runtime could refund guest capacity while a host call still retained guest memory. | Keep the active owner charged until the call returns. `TestGuestCapacityRemainsChargedWhileClosedCallIsRunning` uses a blocked real host call. |
| A pool recipient could hold a module after removing it from the channel but before marking its ticket busy; retirement treated the ticket as idle and refunded its capacity. | Register acquisitions before receiving a pooled module, retain their capacity during retirement and discard a received instance before releasing ownership. `TestGuestCapacityRemainsChargedDuringPoolHandoff` reproduces the interleaving using the existing wait hook and confirms another candidate is refused; `TestGuestRetirementWakesCapacityWaiters` checks prompt wakeup without changing the existing busy response. |
| A caller could still hold a released pool wrapper after retirement refunded its ticket, and that wrapper retained the closed module's linear memory. | Clear each drained wrapper's module reference before refunding its ticket. `TestGuestRetirementClearsReleasedModuleAlias` failed with 2,424,832 bytes still reachable before the correction. |
| A partially built candidate suppressed a previously built plugin's runtime-close error when a later plugin failed, even though its capacity remained charged; generation teardown callers also ignore closer errors. | Return the original build error together with the teardown error, and log non-transient runtime-close failures. `TestFailedGuestCandidateReportsPriorRuntimeCloseFailure` cancels a real two-plugin build, injects a close failure and verifies both errors and retained capacity debt; `TestGuestCapacityRemainsChargedUntilRuntimeCloseAfterModuleCloseFailure` checks the log. |
| Application shutdown discarded `Manager.Close` errors, including the case where a retained owner still has work in flight. | Log the manager close error in `app.Serve`; shutdown does not block indefinitely waiting for guest work. The retained reservation remains visible while the process is running. |
| A second runtime close could hide the first error if no ticket retained it. | Preserve the first close result, including when there are no guest tickets. `TestManagerKeepsRuntimeCloseErrorWithoutGuestTickets` covers the retry. |
| A failed start could inherit an experimental allocator that retains guest memory; `_initialize` could also return an already-closed module as usable. | Isolate startup context values while retaining cancellation and deadlines; reject a closed module. `TestGuestStartupDoesNotInheritRetainingAllocator` and `TestGuestStartupRejectsModuleClosedByInitialize` cover these paths. |
| Wazero can return no module after a failed start even though teardown is not proven. The previous nil-module path refunded the guest ticket, while a non-nil failed module was closed explicitly. | The earlier prototype retained the ticket and slot. The current conservative alternative retains the charge for process lifetime while freeing the local slot. `TestGuestCapacityRetainsFailedInstantiationWithoutModule` exercises a real trapping start. |
| Configured guest potential was absent from status while a candidate was still compiling, because registration happened only after compile and ABI negotiation. | Track candidate potential before compilation and transfer it atomically to the compiled-plugin set. `TestGuestCapacityStatsIncludesCompilingCandidate` checks potential, live count, and drainage on close. |

Temporary Go overlays of earlier local implementations reproduced the route, active-host-call,
no-ticket close-retry, retaining-allocator and closed-initialization failures; the same tests pass
without the overlays. The close-retry test with an existing ticket alone also passed the mutation
because that ticket already retained the error; the additional no-ticket test exposes the actual
failure. Removing guest-capacity overload classification independently made the H1, H2 and H3
enforce tests return `500` instead of `503`; all three pass with it restored. These observations
are local regression evidence, not an independent approval.
Before the pool-handoff correction, the new permanent regression failed with 16 MiB refunded during
the handoff. A separate scheduling-only overlay showed 2,424,832 bytes of guest memory still
reachable in that state; this is a local observation, not a process-wide memory measurement.

**Historical blocking ownership finding — high, confidence 9/10.** Pinned wazero v1.12.0's
`CloseModuleOnCanceledOrTimeout` closes its watcher cancellation channel without waiting for the
goroutine. `Module.Close` does not clear the module's linear-memory buffer. If the watcher is
delayed, it can still reach the module while Jul has already refunded its ticket, allowing new
instances to consume capacity whose previous memory is still retained. This violates A20's
last-alias condition; it does not depend on guest memory being equivalent to process RSS.

The [dated probe and raw output](evidence/2026-11-15-700-i2b-watcher/README.md) force that
interleaving by pausing the watcher **before** its original `select`, where the original
cancellation branch still uses the module. The pre-fix prototype refunded a trapping start's
64 KiB ticket while the watcher held a module with 64 KiB of linear memory. An intermediate
prototype retained both ticket and slot for a nil-module failure when close-on-context-done was
enabled. The current conservative alternative instead retains the process charge but frees the
local slot, as described above; `TestGuestCapacityRetainsFailedInstantiationWithoutModule`
exercises the direct failed-start path. The pre-fix probe also showed
a real guest retirement refunding its 16 MiB ceiling while a weak pointer found 2,424,832 bytes
still reachable after forced GC; that prototype also refunded normal retirement. Both historical failures
reproduced with and without `-race`; the race run reported no data race.
The branch has no established public API to join these watchers, and the same cancellation logic
was present in the latest upstream source checked on 2026-11-15. Timed sleeps, unsafe access,
finalizers and disabling guest cancellation are not acceptable substitutes. A reviewed upstream
fix or another supported solution must establish the last-alias and interruption invariants,
with failed-start, normal-close, overlap, cancellation and drainage tests. The conservative
alternative avoids this early refund by retaining debt until process exit, but B1 blocks I2b
because normal churn exhausts finite enforce limits.

On 2026-11-16, an isolated [watcher-join experiment](evidence/2026-11-16-700-i2b-join/README.md)
prevented the reproduced early startup return on one host and passed its focused reachability
probes under `-race`. The completion signal still precedes goroutine termination, the nil-module
failed-start debt path remains unresolved, and the experiment is neither an upstream change nor
proof of A20. It does not change the merge blocker.

The [public-API close probe](evidence/2026-11-16-700-i2b-public-close/README.md)
tested both wazero engines on macOS with and without cancellation watching. With
watching disabled, explicit close returned but the tested infinite call and
start did not stop within 750 ms. This bounded result does not establish that
the loop can never stop, but it does not support substituting close for
cancellation or establish last-alias teardown.

On macOS arm64 with Go 1.27.4, the local worktree before integrating #719 passed these checks:

- `go test -count=1 -tags wasmplugins ./internal/plugins ./internal/app ./internal/server ./cmd/jul`.
- `go test -count=1 ./internal/inspection ./internal/config ./internal/configcontract ./internal/lifecycle ./internal/observability` and `CGO_ENABLED=0 go test -count=1 -tags waf ./internal/waf`.
- `CGO_ENABLED=0 go test -count=1 -tags 'wasmplugins waf grpc http3' ./internal/app ./internal/plugins ./internal/waf ./internal/inspection`: passed, including real H1/H2/H3 guest-capacity refusal and observe-mode behavior, gRPC `UNAVAILABLE`, connection reuse and post-refusal recovery.
- `go test -race -count=10 -tags wasmplugins ./internal/plugins -run 'TestGuestCapacity|TestGuestStartup|TestManagerKeepsRuntime'`.
- `go test -count=1 -tags 'wasmplugins waf grpc http3' ./internal/inspection ./internal/plugins ./internal/waf ./internal/server ./internal/app ./internal/config ./internal/configcontract ./internal/lifecycle ./internal/observability`: passed with CGO enabled after the ownership corrections.
- `go test -race -count=10 -tags wasmplugins ./internal/plugins -run '^(TestFailedGuestCandidateReportsPriorRuntimeCloseFailure|TestGuestCapacityRemainsChargedUntilRuntimeCloseAfterModuleCloseFailure|TestGuestCapacityRemainsChargedDuringPoolHandoff|TestGuestRetirementClearsReleasedModuleAlias|TestGuestRetirementWakesCapacityWaiters|TestGuestCapacityRemainsChargedWhileClosedCallIsRunning|TestGuestCapacityRemainsChargedDuringRetiredStartup|TestInstanceWaitHoldsLeaseAndBody)$'`: passed after the ownership and teardown-logging corrections.
- `go test -race -tags wasmplugins ./internal/plugins -run '^TestGuestCapacityRemainsChargedDuringPoolHandoff$' -count=10`: passed after adding the overlapping-candidate refusal assertion.
- `go test -tags wasmplugins ./internal/app -run 'Test.*(Plugin|Inspection)'`: passed after surfacing manager shutdown errors.
- `go test -count=1 -tags wasmplugins ./internal/plugins -run '^(TestGuestCapacityRetainsFailedInstantiationWithoutModule|TestGuestCapacityStatsIncludesCompilingCandidate|TestCompileUsesTheHashedSnapshot|TestFailedGuestCandidateReportsPriorRuntimeCloseFailure)$'`: passed after retaining uncertain failed starts and reporting candidate potential before compilation.
- `bash docs/reviews/evidence/2026-11-15-700-i2b-watcher/reproduce.sh` and `PROBE_RACE=1 bash docs/reviews/evidence/2026-11-15-700-i2b-watcher/reproduce.sh`: both scripts passed their assertions on `aa5a8dd3`; the failed-start assertion passed, normal retirement failed as expected, and the race run reported no data race.
- `go test -race -count=5 -tags wasmplugins ./internal/inspection ./internal/server -run 'TestGuestCapacity|TestPublishLimits|TestFullReloadRejectsStaleInspectionVersion|TestLimitsOnly|TestGuestCapacityMinimum'`.
- `go test -race -count=10 -tags 'wasmplugins http3' ./internal/plugins -run 'TestInspectionOutcomesOverH(1H2|3)/(h1|h2|h3)/guest_capacity'`.
- `go test -count=1 ./internal/server -run 'TestReloadDoesNotHideDuplicateUpstreamWeightChange|TestLimitsOnlyReloadDoesNotHideRouteOrderChange|TestSemanticNoopPreservesGenerationAndResources|TestCanonicalEquivalentSourceReloadsAreNoChange'` and `go test -race -count=10 ./internal/server -run 'TestReloadDoesNotHideDuplicateUpstreamWeightChange|TestLimitsOnlyReloadDoesNotHideRouteOrderChange'`.
- `CGO_ENABLED=0 go test -count=1 -tags waf ./internal/waf -run 'TestGovernedWAFSlowBodyTimesOut|TestGovernedWAF.*BodyTimeout|TestGovernedWAF.*Deadline'` and `go test -count=1 ./internal/configcontract ./internal/config`: passed after correcting the timeout descriptions and regenerating their contracts. The first contract-test run expected two schema-description sentences, but the generator intentionally emits only the first; correcting that test expectation made the rerun pass.
- `go vet -tags wasmplugins ./internal/plugins ./internal/inspection ./internal/server ./internal/app ./internal/config`.
- `CGO_ENABLED=0 "$HOME/go/bin/golangci-lint" run ./internal/inspection/... ./internal/plugins/... ./internal/server/... ./internal/config/... ./internal/app/... ./internal/observability/...` using the CI-pinned v2.14.0 binary: zero issues.
- `CGO_ENABLED=0 "$HOME/go/bin/golangci-lint" run ./internal/lifecycle/... ./internal/configcontract/... ./internal/waf/...`: zero issues with the same binary.
- `CGO_ENABLED=0 go build ./...` and `CGO_ENABLED=0 go build -tags 'brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf' ./...`.
- `make format-check`, `CGO_ENABLED=0 make config-check`, `make generated-check`,
  `python3 scripts/docs-check.py` (2,722 passed, 0 failed), and `git diff --check`.

## Post-#720 verification on the published branch

The code tests below ran on `aa5a8dd3`; `e2f1a555` adds only the checksummed current-probe
documentation and raw outputs, and `4af8bfdb` adds focused guest-capacity coverage tests.
`main` was `13126aad` (#720). Environment: macOS arm64, Go 1.27.4, CGO enabled unless specified.
Full-tag hosted CI run 43707306605 passed on `4af8bfdb`. The preceding run 43705663340 on
`e2f1a555` passed every check except the 90% changed-Go coverage gate (84.4%); the new tests
raise the local combined coverage measurement above 90%, and the subsequent hosted coverage gate
passed. Draft #721 is not merge-ready while I2b remains blocked.

PR CI on `1e8d7fa2` reported 123/148 (83.1081%) in the separate HR-07C changed-statement gate.
The regression tests in `4e9b1fb5` exercise cancellation, generation changes and a concurrent
publication conflict. On macOS arm64, the HR-07C full-tag coverage workload and its diff-scoring
calculation passed at 135/148 (91.2162%); the two affected tests also passed with and without
`-race`. Hosted checks for the later head must be verified separately.
PR CI on `efff1288` also found 9/15 (60%) in the #407 changed-statement gate; focused
effective guest-page boundary tests and the two local #407 coverage workloads measured
15/15 (100%). Its full-tag race job failed in the Linux-only assessment harness:
`TestExecuteBudgetChurnAcknowledgesReloads` reported unexpected in-flight request errors.
That failure has not been reproduced locally on macOS and requires a hosted rerun and
investigation if it persists; it is not evidence that the final race suite passed.

- `go test -count=1 -tags wasmplugins ./internal/plugins ./internal/app ./internal/server ./cmd/jul`: passed.
- `go test -count=1 ./internal/inspection ./internal/config ./internal/configcontract ./internal/lifecycle ./internal/observability`: passed.
- `go test -race -count=5 -tags wasmplugins ./internal/plugins -run '^(TestGuestCapacityRetainsFailedInstantiationWithoutModule|TestGuestCapacityStatsIncludesCompilingCandidate|TestGuestCapacityRemainsChargedDuringPoolHandoff|TestGuestCapacityRemainsChargedWhileClosedCallIsRunning|TestGuestRetirementClearsReleasedModuleAlias|TestGuestRetirementWakesCapacityWaiters|TestGuestStartupDoesNotInheritRetainingAllocator|TestGuestStartupRejectsModuleClosedByInitialize)$'`: passed.
- `go test -count=1 -coverprofile=/tmp/jul-inspection-guest.cover ./internal/inspection`: passed, 92.4% package coverage.
- `go test -count=1 -tags wasmplugins -coverprofile=/tmp/jul-plugins-guest.cover ./internal/plugins`: passed, 90.9% package coverage.
- `go test -race -count=5 ./internal/inspection -run 'TestGuest(StateNames|CapacityMinimum|TicketResolveTeardown|CapacityRejectsInvalidAndOverflow)'` and `go test -race -count=3 -tags wasmplugins ./internal/plugins -run '^TestGuestHelpersWithoutGovernorAreNoops$'`: passed.
- `go vet -tags wasmplugins ./internal/plugins ./internal/app ./internal/server ./internal/config`: passed.
- `make generated-check && make config-check && python3 scripts/docs-check.py && git diff --check`: passed; 2,727 documentation checks passed, config fixtures loaded, generated artifacts were current.
- `make format-check`: passed.

The latest security/ownership review found no exploitable security vulnerability; it confirmed the
normal-retirement watcher gap as an availability/resource-ownership blocker (confidence 9/10).
An independent concurrency reviewer confirmed earlier pool-handoff and alias corrections on a
pre-probe diff, but did not review the final diff. A fresh final concurrency review remains pending.

The default PATH's `golangci-lint` was built with Go 1.26 and refused the Go 1.27.4 project;
the separately installed CI-pinned version produced the targeted result above. A combined
`CGO_ENABLED=0` run of the affected packages passed except for the macOS observability RSS test:
the pinned Prometheus Darwin `!cgo` collector does not implement RSS. The full observability
suite passed with the default CGO setting; the failed no-CGO run is not reported as green.
The Linux-only
assessment harness tests cross-compiled but could not be run on this macOS host; no Linux
container daemon was available. No load or A20 result is inferred from the focused lifecycle
tests. Hosted CI run 43707306605 passed, but this does not establish the watcher ownership
invariant or A20. The implementation is not merged; draft #721 is blocked. The watcher ownership
invariant, A20 qualification and a finite supported default
remain explicitly blocked;
do not close #700/#62 or claim final acceptance before I6b.

## I6a partial guided settings review

The #724 draft adds the six existing inspection fields to the shared global
settings editor. Editable values come from the authoritative write baseline;
inspection edits require managed authority, `config:write`, `config:raw`, and a
version from that same baseline. The preview is pinned to the version captured
when the editor opened. File-owned, unknown-authority and incompatible older
servers remain read-only or unavailable. These changes do not implement guest
capacity or spool protection, nor complete I6a.

Independent read-only reviews of the guided-settings diff identified four
functional findings: an unpinned baseline could overwrite concurrent changes;
edits could change while a preview was in flight; clearing a timeout could
silently disable it; and pathless server validation details were hidden. The
version is now permission-gated and pinned, the form is frozen during preview,
changed empty durations require explicit `0s`, and server issues are displayed.
The reviewers confirmed their fixes without further significant findings. A
separate focused security review and follow-up reported no vulnerabilities;
these reviews are scoped to this diff, not the entire checkpoint.

Focused Go and frontend tests cover version permissions and stale baselines,
explicit zero, empty timeouts, missing authority and versions, preview errors,
busy state and navigation. The real-server browser smoke exercised an invalid
slot error and a successful version-pinned preview without applying it. Full
I6a refusal/draining browser acceptance, guest/spool producers and final
whole-checkpoint review remain outstanding.

The real-server Playwright suite now exercises observe-mode publication, the
Overview explanation and navigation to the shared settings editor, including
the explicit zero evaluation-slot value. It restores the original configuration.
The targeted test passed against the I6a branch's embedded Console and failed
against the clean `9c70b64d` baseline because the overview had no inspection
projection. This fixture has no inspected route; it does not demonstrate
observe-mode traffic, a refusal, or draining.

An additional admin integration test wires a real governor into both overview
and v1 status. It checks an enforce-mode admission refusal after a limit
reduction, observe-mode admission above the limit, and the reported draining
transition as leases close. These leases are acquired directly by the test,
not by HTTP inspection traffic; browser and protocol acceptance remain open.
The test failed as expected in a disposable `a68b68ee` worktree when the
status projection was independently mutated to always report `draining=false`.

A further `waf`-tagged integration test sends real HTTP traffic through the
WAF and checks both live status endpoints while an origin request is held.
After a limit reduction, it verifies enforce returns 503 without reaching the
origin, observe allows the next request, and the statuses drain on release.
The test failed with the same independent draining-projection mutation. This
uses an in-process HTTP fixture, not the packaged Jul server or a browser, so
the corresponding browser acceptance remains open.

The isolated `inspection-real-server` Playwright project now builds a
`console waf` Jul binary and holds real WAF requests at a local origin. It
publishes a lower valid limit, checks the resulting 503 and `Retry-After`,
renders the draining state in the browser, switches to observe, confirms
traffic reaches the origin and drains the held requests. The browser then
shows observe without draining. A random fixture token and private temporary
directory isolate it from other Jul processes. Three concurrent repetitions
passed locally; changing only the displayed `Draining` label made the test
fail before restoring it. The existing CI real-server project includes this
test; the isolated project also permits running it independently.
This covers the WAF browser scenario, not the missing guest/spool producers,
all browser compatibility cases, or final I6a review.

| Validation command | Result |
| --- | --- |
| `CI=1 node node_modules/@playwright/test/cli.js test --project=inspection-real-server --repeat-each=3 --reporter=list` (from `internal/admin/ui`) | Passed, three concurrent repetitions on macOS |
| `node node_modules/typescript/bin/tsc --noEmit` and `node node_modules/eslint/bin/eslint.js .` (from `internal/admin/ui`) | Passed; the project configuration excludes the E2E files |
| `GOMAXPROCS=2 python3 scripts/docs-check.py` | Passed, 2,723 checks |
| `CI=1 node node_modules/@playwright/test/cli.js test --project=real-server --project=inspection-real-server --reporter=list` (from `internal/admin/ui`) | Did not start: the existing shared real-server script's macOS `mktemp` path traverses `/var`, a symlink rejected by the version-key directory check. The isolated project passed separately; hosted CI must validate its real-server invocation. |
| `CI=1 node node_modules/@playwright/test/cli.js test --project=real-server inspection-real-server.spec.ts --reporter=list` (from `internal/admin/ui`) | Passed on macOS with a canonical `TMPDIR` and a temporary local change making the existing script pass that directory explicitly to `mktemp`. The script was restored afterwards; the full real-server suite was not rerun. |

The packaged-browser test now also stalls an actual overview poll to check
the stale warning and trend removal, then restores polling. It next removes
the inspection field from otherwise real server responses to simulate an older
backend, verifies the collapsed and expanded unavailable states, and restores
the real response. This is a deliberately injected compatibility fault, not
an actual older-server binary. The test exposed a missing-field case where the
collapsed summary did not warn: the unit regression failed before changing
the summary to treat both `null` and missing inspection as unavailable.

| Validation command | Result |
| --- | --- |
| `corepack pnpm exec vitest run src/test/overview-layout.test.tsx src/test/resource-capacity.test.tsx` (from `internal/admin/ui`) | Passed, 57 tests; the new missing-field assertion failed before the fix. |
| `corepack pnpm run lint`, `corepack pnpm run typecheck`, `corepack pnpm run build` (from `internal/admin/ui`) | Passed; the build reported the existing sourcemap warning. |
| `go test -tags console ./internal/admin -run 'TestConsole\|TestRuntimeOverview' -count=1` | Passed with the updated embedded assets. |
| `CI=1 node node_modules/@playwright/test/cli.js test --project=inspection-real-server --repeat-each=3 --retries=0 --reporter=list` (from `internal/admin/ui`) | Passed, three concurrent macOS repetitions with local dependencies and freshly built assets. An earlier run failed because the test removed its route before the stalled handler finished; the handler is now joined before removal. |
| `CI=1 corepack pnpm exec playwright test --project=inspection-real-server --repeat-each=3 --retries=0 --reporter=list` (from `internal/admin/ui`) | Did not start the browser: Corepack's pnpm attempted a separate `pnpm install`, but the shell's pnpm shim had no configured version. The direct Playwright invocation above used the same installed dependencies. |

The built initial-route assets totaled 242,808 bytes with local `gzip -9 -n`
against the unchanged 256,000-byte budget; hosted CI must verify its own build.

Two independent whole-diff reviews found that a delayed Overview response
could be presented as fresh, lazy-route chunk failures could remove the whole
Console, the inspection trend lacked accessible values, and `jul status`
silently omitted inspection on older servers. The Overview now dates samples
from the start of the request, suppresses stale trends, and exposes first,
latest, minimum and maximum values. The lazy routes report failures with a
reload action and retain navigation; the CLI marks absent inspection status
unavailable. The initial timestamp and CLI regressions failed before their
fixes. A follow-up review identified missing intermediate peaks in the first
accessible summary; minimum and maximum are now included, with a spike
regression. Another follow-up found that two request starts could conceal a
longer possible gap between server samples; history now resets if the latest
possible current sample and earliest possible prior sample are over six
seconds apart. The regression failed before that correction. The UI labels its
window as request-start times, not actual sample times. A browser navigation
check also exposed an error boundary retained across different routes;
route-specific keys correct it. Both reviewers confirmed the corrections
without further significant findings.

The published head `1f78bce9` failed hosted `docs-check`: D11-104 still named
26 obsolete content-hashed assets from an earlier build. The current build
reconciles those paths and records the replacement mapping without changing
historical review evidence. Its local D11 inventory check and all 22 D11 tests
passed. This is path reconciliation, not a recertification of the new code.

| Validation command | Result |
| --- | --- |
| `corepack pnpm exec vitest run src/test/resource-capacity.test.tsx src/test/overview-layout.test.tsx` (from `internal/admin/ui`) | Passed, 59 tests. |
| `corepack pnpm run build` and `corepack pnpm run lint` (from `internal/admin/ui`) | Passed; the build included type checking and reported the existing sourcemap warning. |
| `corepack pnpm test:coverage` (from `internal/admin/ui`) | Passed, 1,086 tests and the coverage contract. |
| `corepack pnpm exec playwright test --project=inspection-real-server e2e/inspection-real-server.spec.ts --repeat-each=3 --workers=3 --retries=0` (from `internal/admin/ui`) | Passed, three concurrent packaged-WAF browser runs, including delayed response and lazy-chunk failure injection. |
| `go test ./cmd/jul -run '^TestRemoteStatus' -count=1` | Passed. |
| `python3 scripts/check-d11-certification.py` and `python3 scripts/test_d11_certification.py` | Passed, zero inventory errors and 22 tests. |

The final initial-route assets totaled 243,162 bytes with local `gzip -9 -n`
against the unchanged 256,000-byte budget. Hosted current-head CI is required.

## Handoff rules

I3a is merged as #715 and I4 as #716 (`daa06bdc`). I3b and I2b remain blocked as described
above; the partial I6a draft still requires stable producer contracts. Do not mark I3b, I2b or I6a
implemented merely because their designs or tracking issues exist; do not close #700/#62 or claim
final acceptance before I6b.
