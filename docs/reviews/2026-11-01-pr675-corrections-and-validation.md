# PR #675 corrections and final independent reviews

Original PR head: `d4beb49d41532fac814fd5294a05ffbf442589f7`.
Integrated main: `abe70dfcd5f3bac42264f72cf3969879dd94051b`, including the final merged #674 corrections.

This record supersedes the earlier readiness checkpoint. The interrupted review's corrections were recovered into a local checkout, completed, and independently reviewed again. Earlier test results are not presented as fresh evidence for this revision.

## Corrected behavior

- Cache cleanup and route-epoch persistence failures belong to the publishing generation. Reloads and managed applies preserve the fact that configuration committed while reporting degraded cleanup, effectiveness, counts, and recovery guidance through direct responses, exact-ID polling, CLI, Console, and audit. No-op and aborted candidates cannot inherit another publication's outcome.
- Route-epoch manifest v2 persists previous retain/invalidate posture. Live and restart transitions use previous-or-new invalidation semantics; legacy records receive conservative compatibility handling. Policy-only changes preserve compatible data.
- Final #674 recovery corrections remain integrated: complete private cutoff identity, pre-commit recovery capacity checks, method/route ownership, and exact populated/empty tag recovery.
- Validated one-change `cache_cutover` choices work across raw and structured previews, apply, rollback, CLI, and Config/History controls. Preview and execution use the same choice without rewriting persistent policy. Staging rejects ephemeral overrides, and legacy callbacks that cannot honor them return 501 before mutation.
- Preview comparisons use the captured effective serving configuration and generation-owned compiled plugin identities. Referenced plugin definition changes and same-path module replacements invalidate the affected cache under the chosen policy. Missing effective evidence reports unavailable. Public projections exclude resolved targets, module paths, and private proofs.
- Large HEAD cutovers count deleted resources correctly when GET siblings belong to retained routes. Cache recovery instructions remain visible alongside reload timeouts and other subsystem failures.
- A new P2 found during the final contract review is fixed: malformed query escapes or semicolons cannot silently discard an explicit invalidation choice and fall back to configured retention. Strict query parsing rejects them before mutation. Permanent tests cover body choices and 14 supported endpoints without idempotency keys.

## Final independent reviews

Two fresh read-only reviewers examined the complete corrected source, including untracked companion files and tests, from runtime/durability and operator-contract/privacy perspectives. Both cross-checked beyond those priorities.

- Runtime reviewer: no unresolved concrete findings. Rechecked the final strict-query delta and repeated the binding/endpoint tests three times.
- Contract reviewer: found the malformed-query P2, verified its correction and permanent regressions, and reports no unresolved concrete production findings. A duplicated recovered Console test group was removed rather than counted twice.

These reviews do not certify absence of every defect or replace hosted CI.

## Fresh local validation

- Complete cache and server test binaries pass; cache/server vet passes.
- Complete admin, adminapi, and adminclient suites pass. Focused cutover, recovery, replay, effective-preview, module-preview, and malformed-query regressions pass three repetitions. Config and lifecycle suites pass.
- Full-tag cache/admin/config/lifecycle/server/adminapi/adminclient suites also pass on the final source.
- Console build/typecheck/lint pass. All **1,006 tests in 86 files** pass. Statement coverage is **81.91%**; the existing global and exact-target coverage contract passes. No coverage floor or exclusion was weakened.
- Generated lifecycle/configuration/API contract drift checks pass. The Console was rebuilt from the combined source; public source maps contain no local workspace paths. D11 current asset names are reconciled without changing frozen evidence.

Local app/CLI execution stops in the existing Windows effective-key fixture: the shared temporary directory and user-owned ancestors do not meet its private-key ACL requirements under the execution account. Actual WASM app tests compile, but their execution and race/platform/browser gates require hosted CI. The race detector also lacks a local C compiler. These checks remain required and were not bypassed. Historical Linux validation from the interrupted chat is retained separately and is not substituted for final-head CI.

NGINX migration was reviewed: no importer classification or request-migration semantics change. This work does not merge or release the PR, or independently close #624/#620; acceptance depends on the verified merged revision.
