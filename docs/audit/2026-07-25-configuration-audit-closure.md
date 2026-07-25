# Configuration-subsystem audit — closure report

**Date:** 2026-07-25
**Scope:** the configuration write/apply/reload/history subsystem (managed apply
coordinator, admin API surface, and the Console v2 configuration + history
panels).
**Baseline reviewed at:** `427e75d2`.
**Closure HEAD:** `00d3b884` (all AC-01–AC-14 code findings landed; this report
is the AC-16 deliverable).

## Closure rule

A finding is only recorded as **Closed** against the **exact commit SHA** whose
tree makes the fix true AND whose pre-commit / CI gate is green. The pre-commit
gate on every commit below ran `go test ./...` and `docs-check.py`
(1312 checks, 0 failures); Console findings additionally ran the Console gates
(`typecheck` / `lint` / `build`) with the vitest suite green at the stated
count. A finding is **not** marked Closed on the strength of intent — only on the
strength of the green SHA named in its row.

The formal repository tag is applied only after this report is signed by the
independent reviewers (see [Sign-off](#sign-off)) against the exact SHA that ran
all CI workflows green.

## Finding ledger

| ID | Area | Disposition | Closure commit(s) | Summary | Evidence (tests) |
|----|------|-------------|-------------------|---------|------------------|
| AC-01 | apply coordinator | Closed | `c1a30ffd`, `cead4bc9` | Managed-apply terminal identity + ordering foundation. | `internal/app/config_apply_test.go` |
| AC-02 | apply coordinator | Closed | `c1a30ffd`, `cead4bc9` | Terminalization callback contract. | `internal/app/config_apply_test.go` |
| AC-03 | apply coordinator | Closed | `ec9cea4d` (hot-finalizer ordering), `5e90f4c0` (single identity) | `ApplyID` is allocated before the hot vs `stage_restart` branch, so every persisted mutation (hot, enqueue-failure, stage create/update) carries one stable ID and is finalized exactly once. | `internal/app/config_apply_test.go`, `internal/admin/operation_identity_test.go` |
| AC-04 | apply coordinator | Closed | `9e75cb3a` | — | `internal/app/config_apply_test.go` |
| AC-05 | history | Closed | `027e6ea4` (storage), `5127b534` (terminalization wiring) | Configuration history is recorded at managed-apply terminalization by a trusted in-process writer; `previousRaw` is never logged/retained by the coordinator; reason matrix (pre_apply / recovery / none) is decided purely from the terminal result; no snapshot at a provisional 202. | `internal/admin/managed_history_test.go`, `internal/admin/history_metadata_test.go` |
| AC-06 | planned restart | Closed | `a814cdd7` | Planned-restart marker promotion is linearized. | `internal/app/planned_restart.go` tests, `internal/app/promote_verified_test.go` |
| AC-07 | apply coordinator | Closed | `a311c9c0` | Patch-preview immutability. | `internal/admin/patch_preview_immutability_test.go` |
| AC-08 | apply coordinator | Closed | `accef73e` (part 1), `b71a20c9` (part 2) | `reload_timeout` (from the SERVING config, R15-01) bounds the whole managed transaction — secret resolution, candidate build, and every preflight gate. A pre-persistence breach aborts cleanly (disk untouched) and maps to 504 via `TimedOutPhase`; a post-persistence timeout stays `saved_not_live` (202) so disk truth is never lost. | `internal/config/candidate_context_test.go`, `internal/app/config_apply_timeout_test.go`, `internal/admin/config_apply_timeout_test.go` |
| AC-09 | Console | Closed | `b41b3bdc`, `303edf12` | The Console polls the EXACT per-ID ledger record `GET /api/config/applies/{id}` — never the runtime overview's global `last_managed_apply`. A missing record (404 after restart), an unrelated id, or an expired budget is never upgraded to a success claim. | `internal/admin/ui/src/test/console-v2-write.test.tsx` |
| AC-10 | Console | Closed | `a38573cf` | A degraded rollback (`ok=true`, `applied_degraded`) is committed: the dialog and its repeatable rollback action close, dependent views refresh, and a separate persistent warning banner surfaces `reload.error` — never presented as an apply failure. | `internal/admin/ui/src/test/console-v2-write.test.tsx` |
| AC-11 | Console | Closed | `dfd61608` | A legacy (pre-managed) apply with no correlated per-ID record never claims "Applied and live"; it shows "Saved; runtime status uncorrelated" unless the persisted and serving versions demonstrably match, plus a DEV deprecation warning. | `internal/admin/ui/src/test/apply-outcome.test.tsx` |
| AC-12 | Console | Closed | `a9177fd9` | The editor's source-of-truth is labeled truthfully — `live` (editable running config) vs `candidate` (read-only proposed patch) vs `diff-only` (no `config:raw`) — so a candidate is never mistaken for the live config. | `internal/admin/ui/src/test/console-v2-write.test.tsx` |
| AC-13 | Console | Closed | `c33e8647` | The ConfigPanel mutation state machine is extracted into a pure, exhaustively-typed reducer + React binding hook, centralizing generation-guarded results, per-ID terminal-merge gating, and admin-confirmation scoping. | `internal/admin/ui/src/test/config-mutation-machine.test.ts` |
| AC-14 | apply coordinator + Console | Closed | `1b62cd19` (backend), `a38573cf` (Console rendering) | Finalization provenance (`history_snapshot_id` / `history_error` / `finalization_error`) is threaded to BOTH the per-ID ledger record and the runtime-overview outcome via a dedicated carrier (never on the serialized apply result), and rendered as a non-blocking advisory distinct from reload success/failure. History degradation never fails readiness. | `internal/app/config_apply_test.go` (`TestManagedApplyFinalizationProvenanceThreaded`), `internal/admin/ui/src/test/console-v2-write.test.tsx` |
| AC-15 | docs | Closed | `cafca8e1` (ADR-0013 reconcile), this report | ADR-0013 reconciled with the landed implementation; reload semantics and Console RBAC specs are current (`docs/reload-semantics.md`, `docs/specs/console-rbac.md`, `docs/adr/0013-managed-apply-terminal-ledger.md`). | `docs-check.py` (1312 checks) |
| AC-16 | docs / process | Closed | `49884e6c` (stale-comment cleanup), this report | This closure report, keyed to exact green SHAs, is the AC-16 artifact. | this report |

## Open design decision (non-blocking)

Whether to add a **non-readiness advisory** admin health row for
`managed_apply_finalization` / `config_history`. The current design surfaces
history/finalization degradation per-terminal-record and on the runtime-overview
outcome (AC-14) rather than as a readiness-affecting health component. Any such
row MUST be advisory only — history/finalization degradation must **never** fail
`/readyz`. This decision does not block closure of AC-01–AC-16.

## Standards enforced across the workstream

- No result is reconstructed independently by the handler or the Console; the
  Console reads the correlated per-ID record.
- No global latest-state lookup is used where an operation ID exists.
- No callback error is silently swallowed.
- No `context.Background()` in managed preparation work (bounded by
  `reload_timeout`).
- No configuration history is recorded at a provisional 202.
- No Console "serving" claim without correlated terminal proof.
- Every concurrency fix has a deterministic, barrier-based test (no sleeps).
- Every API-contract change updated Go types + TS schemas + client + component +
  docs together.

## Release gate — remaining process steps

The code and this report are complete at `00d3b884`. Before the repository tag is
applied, the following process steps must complete (they require the GitHub CI
environment and independent reviewers, not further code):

1. Open the PR to `main` from the branch containing `00d3b884` (or its
   descendant that carries this report).
2. Ensure the **exact SHA** under review runs **all** CI workflows green; add a
   `workflow_dispatch` trigger so the closure SHA can be re-run on demand.
3. Obtain independent review sign-off (below): one security/concurrency reviewer
   and one frontend reviewer, neither of whom authored the fixes.
4. Record the CI run IDs in the sign-off table.
5. Tag the release **only after** this report is signed against the exact green
   SHA.

## Sign-off

| Role | Reviewer | SHA reviewed | CI run ID | Date | Signature |
|------|----------|--------------|-----------|------|-----------|
| Security / concurrency | _pending_ | | | | |
| Frontend (Console) | _pending_ | | | | |

_This report must not be treated as final until both rows are signed against the
exact SHA that ran all CI workflows green._