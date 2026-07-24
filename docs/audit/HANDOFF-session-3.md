# Audit-closure implementation — continuation handoff (session 3)

## Status snapshot
- **Current HEAD: `5e90f4c0`** — clean working tree, pre-commit gate green (`go test ./...` + `docs-check.py`, 1312 docs checks pass).
- Baseline reviewed at `427e75d2`. Session-1 landed: AC-01, AC-02, AC-03 (hot-finalizer ordering), AC-04, AC-05 (storage), AC-06, AC-07; partial AC-15/AC-16.
- Session-2 landed (below): AC-05 terminalization wiring + AC-03 finish.

## Environment / workflow reminders
- Windows 11, cmd shell; CWD path has spaces — use `powershell -NoProfile -Command "..."` to slice files; `findstr` cannot open spaced paths.
- `-race` cannot run locally (no C compiler; CGO). CI runs race on Linux/macOS. Use `go test -count=N` locally for concurrency confidence.
- Pre-commit hook runs `go test ./...` + `docs-check.py` (~40–60s). The commit command often reports "timed out" while succeeding in the background — read the background log to confirm `[latest <sha>]` and "All checks passed."
- Editor auto-strips unused imports on save; when adding code needing a new import, use `write_to_file` for the whole file OR add the import together with its first use.
- After edits: `gofmt -w <files>` then `gofmt -l <files>` (empty output = clean). LF→CRLF warnings harmless.
- Verify each increment: `go build ./...`; `go vet` changed pkgs; `go test -count=1 ./internal/...` affected pkgs; gofmt clean; then commit.

## Completed THIS session (both committed & green)

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

## Remaining work — implement in this order

### AC-08 (P1, largest, 3–5 d) — reload_timeout bounds the whole transaction
Candidate resolution + full preflight currently use `c.BaseCtx`; only `applyCandidate` derives a deadline (already uses the SERVING config's reload_timeout, not the candidate's — correct). Extend the deadline to bound secret resolution + candidate build + all preflight. Add `config.NewCandidateContext(ctx, raw)` with `config.NewCandidate` as a `context.Background()` wrapper; thread the deadline ctx through secret providers and `Preflight.Apply`/`applyCandidate` (preflight already accepts+propagates ctx). Phase-specific 504 with `timed_out_phase` (phases: resolve, authorize_admin, preflight_validate/tls/handlers/plugins/stream/listeners/startup_resources, persist, enqueue, reload_prepare/publish/post_commit). Request cancellation before persistence aborts; AFTER persistence must NOT abandon restoration — continue under process context bounded by the same absolute deadline. Cleanup: no goroutine/fd/temp/marker leaks. Tests use injected blocking functions per phase; assert 504, exact phase, disk unchanged, no marker, cleanup once, goroutine count returns to baseline (deterministic barriers, not sleeps).

### AC-09–AC-13 (P1) — Console (`internal/admin/ui`, pnpm)
Gate: `pnpm --dir internal/admin/ui run typecheck|lint|test:coverage|build`, embedded asset drift guard must pass.
- AC-09: poll `/api/config/applies/{id}` (exact-ID) not Runtime Overview; add `ManagedApplyRecordSchema` (zod) + `fetchManagedApply`; poll until state=terminal; immediate first read, 1s for 10s then 2s until deadline+margin; stop on terminal/404-after-restart/cancel; missing never becomes success.
- AC-10: degraded rollback (ok=true, applied_degraded) is committed — close dialog, invalidate queries (history/raw/pending-restart/overview/route-app), show separate persistent warning banner, no repeatable rollback action.
- AC-11: legacy uncorrelated results never claim "Applied and live"; say "Saved; runtime status uncorrelated" unless persisted+serving versions match; dev deprecation warning.
- AC-12: config:raw operators → call `/api/config/patch/candidate` with base_version, show candidate read-only; non-config:raw → diff only; label source view truthfully.
- AC-13: extract ConfigPanel mutation state machine into reducer/hook preserving operation kind, candidate/patch ops, base version, mode, admin-confirmed state, operation generation, apply ID.

### AC-14 — health/finalization degradation surface
Admin health component for `managed_apply_finalization` + `config_history`; terminal result exposes `history_snapshot_id`/`history_error`/`finalization_error`; Console shows separately from reload success/failure. Note: coordinator already carries `HistorySnapshotID`/`HistoryError` internally (AC-05) — wire them into the admin health/terminal surface and the ledger record.

### Remaining AC-15/AC-16 + closure
Docs: `docs/reload-semantics.md`, `docs/specs/console-rbac.md`, ADRs (planned-restart linearization, timeout boundary), audit closure report `docs/audit/<date>-configuration-audit-closure.md` (per-finding: id, path, closure commit, summary, test names, workflow job, disposition, reviewer — do NOT write "closed" before the exact green SHA). Comment cleanup: buildRBACPolicy post-Publish rebuild claims, BOM/encoding in Console source. Then PR to main, ensure exact SHA runs all workflows, add `workflow_dispatch` trigger, independent security/concurrency + frontend reviewers, record run IDs, tag only after report signed.

## Standards (plan §15) — keep enforcing
No result reconstructed independently by handler or Console; no global latest-state lookup where an operation ID exists; no callback error silently swallowed; no `context.Background()` in managed preparation work; no mutation of loader-returned objects; no success before stage marker+disk verified coherent; no history at provisional 202; no high-water check suppressing a legitimate audit event; no UI "serving" claim without correlated terminal proof; no "fixed" doc status before exact-head evidence; every concurrency fix needs a deterministic barrier-based test (not sleeps); every API contract change updates Go types + TS schemas + client + component + docs together.

## Commit ledger (this workstream)
```
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

Start next session with AC-08. Repository is clean & green at `5e90f4c0`.