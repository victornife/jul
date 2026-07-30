# Configuration-subsystem audit — closure report

**Status:** REOPENED — implementation verification found incomplete production
wiring; the premature closure at `00d3b884` is superseded. This report is rebuilt
on the audit-continuation lineage and is **not final**.
**Date:** 2026-07-25 (original) · **Reopened & rebuilt:** 2026-07-30
**Scope:** the configuration write/apply/reload/history subsystem (managed apply
coordinator, admin API surface, and the Console v2 configuration + history
panels).
**Original (superseded) closure HEAD:** `00d3b884` — its "Closed" claims rested
on the local pre-commit gate, not a complete CI run at an independently reviewed
SHA, so every finding below is reopened and re-anchored to the workstream that
actually wired it.
**Remediation lineage:** WS01–WS07 (`audit/ws0*` branches), each independently
reviewed; the accepted per-workstream SHA is named in every ledger row. Review
reports live under `.github/copilot-audit-state/reviews/` (local operational
evidence, excluded from Git).

## Closure rule

A finding is recorded as **Closed** only against the **exact commit SHA** whose
tree makes the fix true AND whose **complete CI** — every `.github/workflows/ci.yml`
job, including the `-race` and multi-OS matrix — is green at that exact SHA, AND
which two independent human reviewers have signed in the [Sign-off](#sign-off)
table. A local **pre-commit hook is not CI** and never substitutes for it;
`scripts/audit-closure-check.sh` is only a local mirror of the gate.

No finding is marked Closed in this report. Each was independently reviewed and
remediated on its workstream, but the exact-SHA CI run and the two human sign-offs
remain outstanding, so every row's disposition is **Reopened → remediated**.

## Finding ledger

Every row is **reopened** and re-anchored to the audit-continuation workstream
that actually wired the fix, at that workstream's independently reviewed accepted
SHA. Nine findings — AC-02, AC-03, AC-04, AC-08, AC-09, AC-12, AC-14, AC-15,
AC-16 — were reopened specifically because implementation verification found
**incomplete production wiring** behind the original closure; the remainder are
reopened because the original disposition rested on a pre-commit gate rather than
complete CI. No row is formally **Closed**: the exact-SHA CI run and the two human
sign-offs below remain outstanding.

| ID | Area | Disposition | Remediation (workstream · accepted SHA) | Summary | Evidence (tests) |
|----|------|-------------|------------------------------------------|---------|------------------|
| AC-01 | apply coordinator | Reopened → remediated | WS01_MANAGED_LEDGER · `a1c7a3e1` | Managed-apply pending ledger with boot-scoped IDs (`rl_<instance>_<sequence>`), the pending record written before the 202, a secret-free projection (`OwnerTokenID` `json:"-"`), and `config:apply` OR `status:read` fail-closed access. | `internal/admin` WS01 vertical suite (202→terminal, registry no-downgrade, start/deadline preserved, disabled/expired 401, Blocked 503), `internal/admin/operation_identity_test.go` |
| AC-02 | apply coordinator | Reopened → remediated | WS02_FINALIZATION · `10c432fa` | Single completion contract: every terminal path invokes one `completeManagedApply`; callback errors are surfaced (retried once), never swallowed. | `internal/app/managed_apply_finalizer_test.go` (`TestManagedApplyFinalizerExactlyOnce`) |
| AC-03 | apply coordinator | Reopened → remediated | WS02_FINALIZATION · `10c432fa` | One terminalization identity is allocated before the hot vs `stage_restart` branch, so every persisted mutation (hot, enqueue-failure, stage create/update) carries one stable ID and is finalized exactly once. | `internal/app/managed_apply_finalizer_test.go` (`TestManagedApplyFinalizerExactlyOnce`, `TestStageRestartTerminalFinalizedThroughLedger`) |
| AC-04 | apply coordinator | Reopened → remediated | WS02_FINALIZATION · `10c432fa` | All three §3.8 terminal paths (hot, enqueue-failure, stage-success) route through the single finalizer with disk-truth fields (`Persisted` / `FinalDiskVersion` / `FinalServingVersion`). | `internal/app/managed_apply_finalizer_test.go` (`TestStageRestartTerminalFinalizedThroughLedger`) |
| AC-05 | history | Reopened → remediated | WS02_FINALIZATION · `10c432fa`; WS06_HISTORY_OBSERVABILITY · `970cfee1` | Configuration history is recorded at terminalization by a trusted in-process writer (claim-before-history ordering); `previousRaw` is never logged/retained by the coordinator; low-cardinality provenance is projected per row with a `MetadataError` degrade; no snapshot at a provisional 202. | `internal/app/managed_apply_finalizer_test.go`, `internal/admin/managed_history_test.go` |
| AC-06 | planned restart | Reopened → remediated | `a814cdd7` (in-tree), re-verified WS02/WS03 | Planned-restart marker promotion is linearized; the base serving/canonical versions are preserved as the rollback/diff base across a staged update. | `internal/app/promote_verified_test.go` |
| AC-07 | apply coordinator | Reopened → remediated | WS05_PATCH_CANDIDATE · `e9232e1c` | Structured-patch preview is immutable and honest: the real candidate is fetched only from the `config:raw`-gated `POST /api/config/patch/candidate` endpoint and presented read-only; a stale (409) or forbidden (403) candidate never shows candidate bytes. | `internal/admin/ui/src/test/patch-config.test.ts` |
| AC-08 | apply coordinator | Reopened → remediated | WS03_ABSOLUTE_DEADLINE · `e22e06e1` | One absolute deadline (from the SERVING config's `reload_timeout`, R15-01) bounds secret resolution, candidate build, and every preflight gate. A pre-persistence breach aborts cleanly (disk untouched) and maps to 504 via `TimedOutPhase`; a post-persistence timeout stays `saved_not_live` (202) so disk truth is never lost. | `internal/app/config_apply_timeout_test.go`, `internal/admin/config_apply_timeout_test.go` (`TestManagedWriteRoutesRecordTimeoutAudit`) |
| AC-09 | Console | Reopened → remediated | WS04_CONSOLE_EXACT_ID · `4c3333e8` | Both panels poll the EXACT per-ID ledger record `GET /api/config/applies/{id}` through one shared deadline-bounded hook — never the runtime overview's global `last_managed_apply`. A missing record (404 after restart), an unrelated id, or an expired budget is never upgraded to a success claim. | `internal/admin/ui/src/test/managed-apply-record.test.tsx` |
| AC-10 | Console | Reopened → remediated | WS04_CONSOLE_EXACT_ID · `4c3333e8` | A degraded rollback (`ok=true`, `applied_degraded`) is committed: the dialog and its repeatable rollback action close, dependent views refresh, and a separate persistent warning banner surfaces `reload.error` — never presented as an apply failure. | `internal/admin/ui/src/test/console-v2-write.test.tsx` |
| AC-11 | Console | Reopened → remediated | WS04_CONSOLE_EXACT_ID · `4c3333e8` | A legacy (pre-managed) apply with no correlated per-ID record never claims "Applied and live"; it shows "Saved; runtime status uncorrelated" unless the persisted and serving versions demonstrably match, plus a DEV deprecation warning. | `internal/admin/ui/src/test/apply-outcome.test.tsx` |
| AC-12 | Console | Reopened → remediated | WS05_PATCH_CANDIDATE · `e9232e1c` | The editor's source-of-truth is labeled truthfully via a four-state `sourceView` (persisted-editable / candidate-readonly / persisted-baseline / diff-only) so a candidate is never mistaken for the live config. | `internal/admin/ui/src/test/console-v2-write.test.tsx`, `internal/admin/ui/src/test/patch-config.test.ts` |
| AC-13 | Console | Reopened → remediated | WS06_HISTORY_OBSERVABILITY · `970cfee1` | The ConfigPanel mutation state machine is a single authoritative reducer + binding hook (generation-guarded results, per-ID terminal-merge gating, admin-confirmation scoping); duplicate mutation-machine truth removed. | `internal/admin/ui/src/test/use-config-mutation-machine.test.tsx` |
| AC-14 | apply coordinator + Console | Reopened → remediated | WS02_FINALIZATION · `10c432fa`; WS04_CONSOLE_EXACT_ID · `4c3333e8`; WS06_HISTORY_OBSERVABILITY · `970cfee1` | Finalization provenance (`history_snapshot_id` / `history_error` / `finalization_error`) is threaded to BOTH the per-ID ledger record and the runtime-overview outcome via a dedicated carrier (never the serialized apply result), rendered as a non-blocking advisory, and counted by the bounded `jul_managed_apply_finalization_errors_total{component}` metric. History degradation never fails `/readyz`. | `internal/app/managed_apply_finalizer_test.go`, `internal/app/config_apply_report_error_test.go`, `internal/admin/api_managed_apply_test.go` |
| AC-15 | docs | Reopened → remediated | WS07_DOCS_CERTIFICATION Slice 01 · `1486e8e7` | `docs/reload-semantics.md`, `docs/adr/0013-managed-apply-terminal-ledger.md`, and `docs/specs/console-rbac.md` reconciled with the landed implementation (RBAC hot-reload, exact-ID ledger, single absolute deadline, advisory finalization; unimplemented RBAC token management marked *future*). | `scripts/docs-check.py` (1474 checks) |
| AC-16 | docs / process | Reopened → rebuilt | WS07_DOCS_CERTIFICATION Slice 02 · this slice (SHA in the excluded continuity state) | This report reopened and re-anchored to the workstream lineage; `.github/workflows/ci.yml` gained a `workflow_dispatch` trigger so the closure SHA can be re-run on demand; `scripts/audit-closure-check.sh` added as the local certification mirror. | this report; `scripts/audit-closure-check.sh`; `.github/workflows/ci.yml` |

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

The code is remediated across WS01–WS07 and independently reviewed per workstream
(reports under `.github/copilot-audit-state/reviews/`). Before any finding is
moved from **Reopened → remediated** to formally **Closed** and the repository is
tagged, the following process steps — which require the GitHub CI environment and
independent human reviewers, not further code — must complete:

1. Open the PR to `main` from the branch carrying this rebuilt report.
2. Run **all** `.github/workflows/ci.yml` jobs green at the **exact SHA** under
   review. The workflow now carries a `workflow_dispatch` trigger, so the closure
   SHA can be dispatched on demand; `scripts/audit-closure-check.sh` mirrors the
   gate locally. The `-race` and multi-OS matrix run only in CI — the local
   environment has no C toolchain, so `go test -race` is unavailable there and is
   reported unverified, never green.
3. Obtain independent human sign-off (below): one security/concurrency reviewer
   and one frontend reviewer, neither of whom authored the fixes.
4. Record the exact-SHA CI run IDs in the sign-off table.
5. Move each ledger row to **Closed** and tag the release **only after** both
   sign-off rows are signed against the exact green SHA.

A local pre-commit hook or a green local `audit-closure-check.sh` is necessary
but **not** sufficient: neither is CI and neither satisfies step 2.

## Sign-off

| Role | Reviewer | SHA reviewed | CI run ID | Date | Signature |
|------|----------|--------------|-----------|------|-----------|
| Security / concurrency | _pending_ | | | | |
| Frontend (Console) | _pending_ | | | | |

_This report must not be treated as final until both rows are signed against the
exact SHA that ran all CI workflows green._