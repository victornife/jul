# Audit-closure implementation — continuation handoff (session 3)

## Status snapshot
- **Current HEAD: `dfd61608`** — clean working tree, pre-commit gate green (`go test ./...` + `docs-check.py`, 1312 docs checks pass); Console gates green (`typecheck`/`lint`/`build`, 444 vitest tests pass).
- Baseline reviewed at `427e75d2`. Session-1 landed: AC-01, AC-02, AC-03 (hot-finalizer ordering), AC-04, AC-05 (storage), AC-06, AC-07; partial AC-15/AC-16.
- Session-2 landed: AC-05 terminalization wiring + AC-03 finish.
- Session-3 landed: **AC-08 complete** (2 commits) + **AC-14 backend** (finalization provenance wired to ledger + overview).
- Session-4 landed: **AC-09 complete** (Console exact-ID polling), **AC-10 complete** (degraded-rollback committed surface), **AC-14 Console rendering complete** (advisory finalization banner on record + overview schemas).
- Session-5 landed (below): **AC-11 complete** (legacy uncorrelated apply never claims "Applied and live").
- **Remaining Console work: AC-12, AC-13 — same tree (`internal/admin/ui`, TS/pnpm). The Go composition-root files (`serve.go`, `config_apply.go`, `admin/server.go`) are large and NOT needed for the Console work; do not pre-load them.**

## Environment / workflow reminders
- Windows 11, cmd shell; CWD path has spaces — use `powershell -NoProfile -Command "..."` to slice files; `findstr` cannot open spaced paths.
- `-race` cannot run locally (no C compiler; CGO). CI runs race on Linux/macOS. Use `go test -count=N` locally for concurrency confidence.
- Pre-commit hook runs `go test ./...` + `docs-check.py` (~40–60s). The commit command often reports "timed out" while succeeding in the background — read the background log to confirm `[latest <sha>]` and "All checks passed."
- Editor auto-strips unused imports on save; when adding code needing a new import, use `write_to_file` for the whole file OR add the import together with its first use.
- After edits: `gofmt -w <files>` then `gofmt -l <files>` (empty output = clean). LF→CRLF warnings harmless.
- Verify each increment: `go build ./...`; `go vet` changed pkgs; `go test -count=1 ./internal/...` affected pkgs; gofmt clean; then commit.

## Completed in session-2 (committed & green)

### AC-05 wiring — configuration history at terminalization (commit `5127b534`)
- `internal/app/config_apply.go`:
  - Added `ApplyResult.HistorySnapshotID` / `HistoryError` (internal provenance, **NOT** serialized).
  - Added `ConfigApplyCoordinator.WriteManagedHistory func(admin.ApplyRequestContext, admin.ConfigApplyResult, []byte) (string, error)` hook. `previousRaw` is sensitive — forwarded only to the trusted in-process writer; never logged/retained by the coordinator. Nil disables coordinator-side history so handlers keep recording (test fakes).
  - New method `recordManagedHistory(reqCtx, result, previousRaw)` invoked at all three terminal points, each passing `baseline.Raw`:
    1. hot completion inside the finalizer goroutine, AFTER `c.mu.Unlock()` and BEFORE the completion callback;
    2. enqueue-failure branch (after unlock, before `notifyManagedApplyComplete`);
    3. committed stage (create/update) at stage terminal success.
  - No snapshot is written at a provisional 202 (recordManagedHistory only runs at terminal build).
- `internal/admin/managed_history.go` (NEW) — trusted writer `(*Server).RecordManagedHistory(reqCtx, result, previousRaw) (id string, err error)`:
  - Reason matrix decided purely from the terminal result (no history policy in the coordinator):
    - committed apply/stage/rollback (ok=true) → `historyReasonPreApply` snapshot;
    - failed apply with restoration FAILED (`ok=false && !Restored && RestoreError != ""`) → `historyReasonRecovery` snapshot;
    - cleanly restored / empty previousRaw / pre-write rejection → no snapshot.
  - Builds redacted `HistoryMetadata{ApplyID, Operation, Mode, Outcome, Actor, Reason, PreviousVersion, CandidateVersion}` and calls `s.hist.snapshotWithMeta`. Sidecar failure returned as non-fatal degradation while the raw TOML snapshot stays written & roll-back-able.
- `internal/admin/managed_history_test.go` (NEW): live→pre_apply; degraded→pre_apply; restored→none; restoration-failed→recovery; stage→pre_apply (empty outcome); empty-prev→none; raw-snapshot-always-retrievable. Uses `newHistoryServer(dir)` + mode string literals ("hot"/"stage_restart").
- Handler-side de-dup: added `Deps.ManagedHistoryActive` flag; the eager `s.recordHistory(prev)` calls in `internal/admin/api.go`, `server.go` (raw+settings), `patch_http.go`, and `api_history.go` (rollback) now skip ONLY when the managed coordinator + writer are active — preserving eager recording for the many admin unit tests that build `Deps` with `WriteConfigRaw`/`ApplyConfig` fakes and no `OnManagedApplyComplete`.
- Composition root `internal/app/serve.go`: sets `ManagedHistoryActive` before `admin.New`, then `coordinator.WriteManagedHistory = adminSrv.RecordManagedHistory` after `adminSrv` exists.

### AC-03 finish — single terminalization identity (commit `5e90f4c0`)
- `internal/app/config_apply.go`: allocate `id := c.nextID()` in `ApplyRaw` **before** the hot vs `stage_restart` branch. Threaded through `applyStageRestart(reqCtx, id, …)` (sets `result.ApplyID = id`) and `applyCandidate(reqCtx, id, …)` (removed its internal `c.nextID()`). Every persisted mutation — hot, enqueue-failure, stage create, stage update — now carries a stable ID and is finalized once via the existing terminal ordering (duplicate callbacks still guarded by the registry `BeginFinalization` in `serve.go`).
- Also removed a stray `test-full.toml` byproduct that a test run left in the tree (folded into `5e90f4c0`).

## Completed in session-3 (committed & green)

### AC-08 — reload_timeout bounds the whole managed transaction (commits `accef73e`, `b71a20c9`)
- **Part 1 (`accef73e`)**: bounded secret resolution + candidate build by context. Added `config.NewCandidateContext(ctx, raw)`; `config.NewCandidate` is now the `context.Background()` wrapper. Secret providers observe the deadline. Tests: `internal/config/candidate_context_test.go`.
- **Part 2 (`b71a20c9`)**: the deadline (derived from the SERVING config's `reload_timeout`, per R15-01 — NOT the candidate's) now bounds candidate resolution + every preflight gate, not just `applyCandidate`. Added `ConfigApplyCoordinator.preflightContext(candidate)` + `runPreflight` with a per-phase observer that attributes a deadline breach to the exact gate. A **pre-persistence** breach aborts cleanly (disk untouched) and surfaces as `ApplyResult.TimedOutPhase`; the admin API maps non-empty `TimedOutPhase` → **504** in `configApplyResultStatus` (checked first, before validation/reload). `ConfigApplyResult.TimedOutPhase string json:"timed_out_phase,omitempty"` added. A timeout AFTER persistence stays `saved_not_live` (202), never 504 — disk truth is never lost. Tests: `internal/app/config_apply_timeout_test.go`, `internal/admin/config_apply_timeout_test.go`.

### AC-14 backend — finalization provenance wired out (commit `1b62cd19`)
- The coordinator already computed `HistorySnapshotID`/`HistoryError` on the app-side `ApplyResult` (AC-05), but they were dropped at the callback boundary. The serialized `admin.ConfigApplyResult` must NOT carry history provenance (AC-05 invariant), so a **dedicated carrier** was added: `admin.ManagedApplyFinalization{HistorySnapshotID, HistoryError, FinalizationError}` (`internal/admin/server.go`).
- `OnManagedApplyComplete` signature gained a third `admin.ManagedApplyFinalization` argument; `notifyManagedApplyComplete` populates it from the terminal `ApplyResult` (`internal/app/config_apply.go`). All four coordinator callback sites in `config_apply_test.go` updated.
- Composition root (`internal/app/serve.go`) routes the provenance into BOTH destinations: (1) the durable per-ID **ledger record** `ManagedApplyRecord.{HistorySnapshotID,HistoryError,FinalizationError}` — already surfaced by `GET /api/config/applies/{id}` via `publicManagedApplyRecord`; and (2) the runtime-overview `ManagedApplyOutcome` (extended with the same three fields, `history_snapshot_id`/`history_error`/`finalization_error`).
- Semantics: a committed apply whose raw snapshot wrote but whose metadata sidecar failed is degraded-but-usable — `HistorySnapshotID` set AND `HistoryError` non-empty, terminal result stays `ok=true` (roll-back-able), never fails readiness. Test: `TestManagedApplyFinalizationProvenanceThreaded` in `internal/app/config_apply_test.go`.
- **NOT yet done (Console side):** rendering these three fields as a finalization/degradation surface distinct from reload success/failure, and (if still wanted) an admin **health component** for `managed_apply_finalization` + `config_history`. The health-component question is open: current design surfaces degradation per-terminal-record + on the overview outcome rather than as a readiness-affecting health component (history degradation must never fail readiness). Decide during the Console pass whether a non-readiness advisory health row is also wanted.

## Completed in session-4 (committed & green)

### AC-09 — Console exact-ID polling (in `HistoryPanel.tsx` + `client.ts`)
- Console now polls `GET /api/config/applies/{id}` (exact ledger record) instead of reconstructing status from the Runtime Overview. Added `ManagedApplyRecordSchema` (zod) + `fetchManagedApply` in `internal/admin/ui/src/api/client.ts`; the panel polls until the record reaches a terminal state (immediate first read, 1s cadence for 10s then 2s until deadline+margin) and stops on terminal / 404-after-restart / cancel. A missing/absent record never becomes a success claim — a 404 after restart resolves to "record gone, not confirmed live". A terminal record for an unrelated apply id is ignored; a provisional rollback stays open until its own correlated terminal record.
- Tests migrated to the exact-ID contract in `console-v2-write.test.tsx`: "finalizes saved-not-live by polling the exact apply-id record", "does not claim success when the exact apply-id record is gone (404 after restart)", "ignores a terminal record for an unrelated apply id", "keeps a provisional rollback open until its correlated terminal record".

### AC-10 — degraded rollback is committed (in `HistoryPanel.tsx`)
- A degraded rollback (`ok=true`, `applied_degraded`) is treated as committed: the dialog closes, queries invalidate (history/raw/pending-restart/overview/route-app), and a **separate persistent warning banner** surfaces the degradation with no repeatable rollback action. The terminal degraded path now surfaces `reload.error` on the banner so the operator sees WHY it degraded (not a generic notice). Deterministic test added for the degraded-rollback path.

### AC-14 Console rendering — advisory finalization surface (commit `a38573cf` folds the rendering into AC-09/AC-10)
- `history_snapshot_id`/`history_error`/`finalization_error` added to `ManagedApplyRecordSchema` (zod) and the overview schema; the panel renders a non-blocking advisory banner **distinct** from reload success/failure. A committed apply that is `ok=true` while its history sidecar degraded is presented as an advisory, never an apply failure.
- Open decision still deferred: whether to also add a non-readiness advisory admin health row for `managed_apply_finalization`/`config_history` (history degradation must NEVER fail `/readyz`).

## Completed in session-5 (committed & green)

### AC-11 — legacy uncorrelated apply never claims "Applied and live" (commit `dfd61608`)
- `internal/admin/ui/src/lib/applyOutcome.ts`: added a new `saved-uncorrelated` outcome kind plus three input fields — `correlated?: boolean`, `persistedVersion?: string`, `servingVersion?: string`. A new branch, placed AFTER `reload-pending` and BEFORE the fully-live return, fires only when `correlated === false` AND the persisted/serving versions do not demonstrably match: it returns an `info`-severity "Saved; runtime status uncorrelated" advisory whose copy never claims live/serving and points the operator at the runtime overview. `correlated === undefined` (the default) preserves back-compat — every existing caller and unit test that reaches `full-live` is unaffected. A version MATCH (persisted === serving, both defined) is treated as correlation and rescues the fully-live claim even without a per-ID record.
- `internal/admin/ui/src/features/config/ConfigPanel.tsx`: the hot-apply outcome projection now marks the legacy path (`reload === undefined`) `correlated: false` and threads `persistedVersion` (response `persisted_version` ?? `version`) and `servingVersion` (response `serving_version` ?? the best-effort overview `last_reload.serving_version`). The managed path (reload present) stays fully correlated. Added a DEV-only one-shot `console.warn` deprecation notice keyed on `outcome?.kind === "saved-uncorrelated"`.
- Tests: `internal/admin/ui/src/test/apply-outcome.test.tsx` — 6 new cases (uncorrelated → saved-uncorrelated; version-mismatch stays uncorrelated; version-match rescues to full-live; correlated default unaffected; reload-pending outranks the gate; banner renders saved-uncorrelated as polite `status` without a live claim). Suite: 444 pass (was 438). Gates green: typecheck/lint/build.

## Remaining work — implement in this order

### AC-12–AC-13 (P1, NEXT) — Console (`internal/admin/ui`, pnpm)
Gate: `pnpm --dir internal/admin/ui run typecheck|lint|test:coverage|build`, embedded asset drift guard must pass.
- AC-12: config:raw operators → call `/api/config/patch/candidate` with base_version, show candidate read-only; non-config:raw → diff only; label source view truthfully.
- AC-13: extract ConfigPanel mutation state machine into reducer/hook preserving operation kind, candidate/patch ops, base version, mode, admin-confirmed state, operation generation, apply ID.

### Remaining AC-15/AC-16 + closure
Docs: `docs/reload-semantics.md`, `docs/specs/console-rbac.md`, ADRs (planned-restart linearization, timeout boundary), audit closure report `docs/audit/<date>-configuration-audit-closure.md` (per-finding: id, path, closure commit, summary, test names, workflow job, disposition, reviewer — do NOT write "closed" before the exact green SHA). Comment cleanup: buildRBACPolicy post-Publish rebuild claims, BOM/encoding in Console source. Then PR to main, ensure exact SHA runs all workflows, add `workflow_dispatch` trigger, independent security/concurrency + frontend reviewers, record run IDs, tag only after report signed.

## Standards (plan §15) — keep enforcing
No result reconstructed independently by handler or Console; no global latest-state lookup where an operation ID exists; no callback error silently swallowed; no `context.Background()` in managed preparation work; no mutation of loader-returned objects; no success before stage marker+disk verified coherent; no history at provisional 202; no high-water check suppressing a legitimate audit event; no UI "serving" claim without correlated terminal proof; no "fixed" doc status before exact-head evidence; every concurrency fix needs a deterministic barrier-based test (not sleeps); every API contract change updates Go types + TS schemas + client + component + docs together.

## Commit ledger (this workstream)
```
dfd61608 AC-11 Console: legacy uncorrelated apply never claims "Applied and live" (saved-uncorrelated outcome + version-match gate)
a38573cf AC-09/AC-10/AC-14 Console: exact-ID polling, degraded-rollback committed surface, advisory finalization banner
1b62cd19 AC-14 backend: thread finalization provenance to ledger + overview
b71a20c9 AC-08 (part 2): bound whole managed apply transaction; phase-specific 504 (timed_out_phase)
accef73e AC-08 (part 1): bound secret resolution + candidate build by context
5e90f4c0 AC-03: allocate ApplyID before hot/stage branch (every persisted managed mutation identified & finalized once)
5127b534 AC-05: record configuration history at managed-apply terminalization
49884e6c app/preflight: correct stale 'ctx reserved for future use' comment (AC-16)
cafca8e1 docs(adr-0013): reconcile with landed audit-closure implementation (AC-15)
a814cdd7 app: linearize planned-restart marker promotion (AC-06)
a311c9c0 AC-07 preview immutability
027e6ea4 AC-05 storage
9e75cb3a AC-04
ec9cea4d AC-03 hot-finalizer ordering
c1a30ffd/cead4bc9 AC-01/AC-02
```

Start next session with the remaining Console findings (AC-12, AC-13), in the `internal/admin/ui` (TS/pnpm) tree — do NOT pre-load the large Go composition-root files. Repository is clean & green at `dfd61608`.
