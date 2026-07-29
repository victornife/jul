# AI implementation journal

> This is an execution log, not the final closure report. Never mark an audit item formally closed here.

## Program metadata

- Repository: victornife/jul
- Target branch: latest
- Initial remote SHA: 50052f9d5377488a6160935008e9e028fd08eca2
- Bootstrap branch: audit/ws01-managed-ledger
- Bootstrap resulting SHA: recorded in the bootstrap slice completion report (this initialization commit)
- Started at: 2026-07-28
- Coordinator agent identifier: bootstrap agent (Claude Opus 4.8, Act/Agent mode)

## Environment baseline

- OS: Windows 11 (amd64)
- Go version: go1.26.5 windows/amd64
- Node version: v24.4.0
- pnpm version: 11.8.0 (update to 11.17.0 available; non-blocking)
- Git version: 2.51.1.windows.1
- GitHub CLI/connector availability: gh 2.95.0 available
- Full build tags available: standard go test toolchain available; full build-tag matrix not exercised in this bootstrap (only the prescribed baseline packages were run)
- Known environment limitations: none known

## Baseline commands

| Command | Result | Notes |
|---|---|---|
| git rev-parse --show-toplevel | pass | Working tree at repo root |
| git remote -v | pass | origin = https://github.com/victornife/jul.git (fetch/push) |
| git branch --show-current | pass | latest at preflight |
| git rev-parse HEAD | pass | 50052f9d5377488a6160935008e9e028fd08eca2 |
| git status --short | pass | Clean working tree |
| git log -1 --oneline | pass | 50052f9d docs(audit): AC-15/AC-16 configuration audit closure report keyed to green SHAs |
| git fetch origin --no-write-fetch-head | pass | Refs fetched without touching working files |
| git rev-parse origin/latest | pass | 50052f9d5377488a6160935008e9e028fd08eca2 (equals EXPECTED_PARENT_SHA; no drift) |
| git merge-base --is-ancestor HEAD origin/latest | pass | Fast-forward confirmed (FF_OK) |
| git diff --check | pass | No whitespace/conflict markers (DIFF_CHECK_CLEAN) |
| go test -count=1 ./internal/admin/... | pass | ok jul/internal/admin 3.897s |
| go test -count=1 ./internal/app/... | pass | ok jul/internal/app; ok jul/internal/app/apps |
| go test -count=1 ./internal/config/... | pass | ok jul/internal/config 2.088s |
| pnpm --dir internal/admin/ui install --frozen-lockfile | pass | Already up to date |
| pnpm --dir internal/admin/ui run typecheck | pass | tsc --noEmit clean |
| pnpm --dir internal/admin/ui run lint | pass | eslint --max-warnings=0 clean |
| pnpm --dir internal/admin/ui run test | pass | vitest run - 37 files, 451 tests passed |

---

## Workstream / slice entry template

### workstream / slice

- Parent SHA:
- Resulting SHA:
- Branch:
- Agent role/context:
- Files changed:
- Production path verified:
- Behavior implemented:
- Tests added:
- Commands run:
- Commands unavailable:
- Deviations:
- Self-review findings:
- Independent review status:
- Reviewer blockers:
- Blocker-fix SHA:
- Accepted SHA:
- Next execution file:

---

### BOOTSTRAP / 03_REPOSITORY_BOOTSTRAP.md

- Parent SHA: 50052f9d5377488a6160935008e9e028fd08eca2
- Resulting SHA: this journal-initialization commit (see bootstrap slice completion report)
- Branch: audit/ws01-managed-ledger
- Agent role/context: bootstrap agent (Claude Opus 4.8, Act/Agent mode); establishes baseline and journal only
- Files changed: docs/audit/AI-IMPLEMENTATION-JOURNAL.md (new)
- Production path verified: n/a - bootstrap slice does not touch production code
- Behavior implemented: reproducible baseline verification + persistent journal initialization
- Tests added: none (no audit finding implemented in this slice)
- Commands run: see Baseline commands table above
- Commands unavailable: none
- Deviations: none
- Self-review findings: no production code touched; no audit finding implemented; only the journal was added and committed
- Independent review status: not required for bootstrap
- Reviewer blockers: none
- Blocker-fix SHA: n/a
- Accepted SHA: pending coordinator acceptance
- Next execution file: WS01 Slice 01 (audit/ws01-managed-ledger)

---

### WS01_MANAGED_LEDGER / 01_BOOT_SCOPED_IDS_AND_REGISTRY_TYPES.md

- Parent SHA: e707c41587a335cc92b643068b1c46053eff2621
- Resulting SHA: recorded in this slice's completion report (the commit that includes this entry)
- Branch: audit/ws01-managed-ledger
- Agent role/context: implementation agent (Claude Opus 4.8, Act/Agent mode, highest reasoning); WS01 Slice 1
- Files changed:
  - internal/app/config_apply.go — boot-scoped apply IDs (rl_<boot-id>_<sequence>) on ConfigApplyCoordinator
  - internal/admin/managed_apply_registry.go — structured ID parser, ManagedApplyFinalizing state, Deadline + OwnerTokenID fields, enriching BeginPending, ClaimFinalization, metadata-preserving Complete
  - internal/app/serve.go — parseReloadSeq accepts boot-scoped + legacy IDs (trailing sequence)
  - internal/app/managed_apply_id_test.go (new) — boot-scoped generation, monotonicity, per-process uniqueness, seq parsing
  - internal/admin/managed_apply_registry_slice1_test.go (new) — ID validity/parse, finalizing lifecycle, enrichment, metadata preservation, non-serialization of OwnerTokenID
- Production path verified: coordinator.nextID() (sole apply-ID source) → server.ReloadRequest.ID → terminal res.ApplyID → serve.go OnManagedApplyComplete → managedApplies.Complete + managedApplySeqGuard(parseReloadSeq). All production IDs now carry the boot-scoped prefix; registry accepts boot-scoped and legacy IDs; the sequence guard sequences on the trailing field.
- Behavior implemented: boot-scoped monotonic apply IDs that prevent cross-restart reuse; legacy rl_<sequence> still accepted; registry gains an explicit finalizing claim state, an absolute Deadline for deadline-aware polling, and private non-serialized OwnerTokenID ownership metadata; BeginPending enriches an existing pending shell without downgrading finalizing/terminal; ClaimFinalization claims finalization exactly once; Complete accepts pending or finalizing and preserves private/lifecycle metadata.
- Tests added: see Files changed (2 new focused test files); all existing managed-apply/seq-guard tests preserved unchanged.
- Commands run: gofmt -w (clean); go build ./... (pass); go vet ./internal/admin/... ./internal/app/... (pass); go test -count=1 ./internal/admin/... ./internal/app/... ./internal/config/... (pass); git diff --check (clean, only benign LF→CRLF notices)
- Commands unavailable: go test -race ./internal/admin/... and ./internal/app/... — UNVERIFIED: -race requires cgo and no C toolchain (gcc/clang) is installed in this environment (KNOWN_ENVIRONMENT_LIMITATION). Residual risk: data-race detection deferred to the CI race matrix; new registry logic reuses the existing mutex discipline and adds no new shared state outside the locked critical sections.
- Deviations:
  1. Requested parseCanonicalApplySequence bound `len(raw) > 20` → implemented `len(raw) > 19` → rationale: the repository's existing TestManagedApplyRegistry_InvalidIDs asserts a 20-digit sequence is invalid; keeping the 19-digit bound preserves that assertion instead of weakening it.
  2. Requested a structured registry-side parser reused by the app-layer sequence guard → implemented the boot-scoped/legacy trailing-sequence parse inside serve.go's existing parseReloadSeq → rationale: parseManagedApplyID is unexported in package admin and importing it would cross package boundaries; the repository-native equivalent extracts the trailing underscore-delimited sequence, which is correct because all IDs in one process share one boot instance.
  3. Retained BeginFinalization and the finalized map alongside the new ClaimFinalization → rationale: production (serve.go) still calls BeginFinalization; this slice introduces the finalizing state and claim API without rewiring that call site (scope minimization); the WS2 finalization-claim wiring is a later slice.
- Self-review findings: single-purpose diff across 3 production files + 2 test files; no generated bundles, closure report, secrets, or bearer/token material touched; OwnerTokenID is json:"-" and a test proves it never serializes; no error swallowing; no unwired helper other than ClaimFinalization which is the deliberate WS2 seam introduced by this slice.
- Independent review status: pending
- Reviewer blockers: none
- Blocker-fix SHA: n/a
- Accepted SHA: pending coordinator acceptance
- Next execution file: WS01 Slice 02 (audit/ws01-managed-ledger)

---

### WS01_MANAGED_LEDGER / 02_PENDING_PRODUCTION_WIRING.md

- Parent SHA: cf7bd0211d52e0acaede2cea2d593499ebc56263
- Resulting SHA: recorded in this slice's completion report (the commit that includes this entry)
- Branch: audit/ws01-managed-ledger
- Agent role/context: implementation agent (Claude Opus 4.8, Act/Agent mode, highest reasoning); WS01 Slice 2
- Files changed:
  - internal/admin/server.go — new ManagedApplyStart{Context, Result} carrier type for the pending-registration signal
  - internal/app/config_apply.go — OnManagedApplyStarted hook on ConfigApplyCoordinator; notifyManagedApplyStarted helper; applyCandidate registers the exact-ID pending record after persist+enqueue and before the synchronous 202; notifyManagedApplyComplete now threads a finalizationErr into ManagedApplyFinalization.FinalizationError; the two complete call sites updated
  - internal/app/serve.go — wired coordinator.OnManagedApplyStarted = managedApplies.BeginPending(...) inside the coordinator!=nil block (before admin.New copies deps and starts the listener); added errors import
  - internal/admin/api_managed_apply.go — publicManagedApplyRecord gains Deadline (json:"deadline,omitempty") and toPublic() projects rec.Deadline
  - internal/admin/ui/src/api/client.ts — ManagedApplyRecordSchema gains deadline: z.string().optional()
  - internal/admin/assets/dist/** — regenerated Console bundle (index + lazy CodeEditor chunk rehash) via `pnpm run build`; never hand-edited
  - internal/app/config_apply_pending_test.go (new) — production-wired pending-registration evidence
- Production path verified: HTTP apply → deps.ApplyConfigRaw/ApplyConfig → coordinator.ApplyRaw → applyCandidate persists candidate (atomicfile.Write) → SubmitReload enqueues the correlated reload → notifyManagedApplyStarted projects the provisional saved_not_live result (provisionalResult) → serve.go's OnManagedApplyStarted resolves the ApplyID (Result.ApplyID, falling back to Result.Reload.ID) and calls managedApplies.BeginPending, so the exact-ID pending record exists BEFORE the synchronous wait can hand a 202 back to the caller. Terminal finalizer → notifyManagedApplyComplete(...finalizationErr) → serve.go OnManagedApplyComplete → managedApplies.Complete updates the same record in place. GET /api/config/applies/{id} → publicManagedApplyRecord.toPublic() now surfaces Deadline; the Console client schema accepts it.
- Behavior implemented: a real 202 saved_not_live is always preceded by an exact-ID pending ledger record (closes the 202→404 window that stalls ConfigPanel); the record carries Operation, StartedAt, absolute Deadline (deadline-aware polling), the provisional Result, and private OwnerTokenID; a post-persistence pending-registration failure never rolls back the accepted apply nor reports the apply as failed — it is carried into ManagedApplyFinalization.FinalizationError and surfaced through the ledger/overview; the public API and the TypeScript schema expose deadline.
- Tests added: internal/app/config_apply_pending_test.go — TestApplyRegistersPendingRecordBeforeSavedNotLive (package integration: drives the real coordinator through the saved_not_live path with the production BeginPending/Complete wiring; asserts the exact-ID pending record exists at 202 with correct operation/deadline/owner/result, that the terminal finalizer transitions the SAME record pending→terminal preserving owner/deadline, and that a later unrelated transaction never evicts it), TestApplyStartedErrorSurfacesInFinalization (failure injection: a failing OnManagedApplyStarted does not roll back the persisted candidate nor fail the live apply, and its wrapped error surfaces in ManagedApplyFinalization.FinalizationError). No existing test weakened.
- Commands run: gofmt -w on the two changed files (clean; remaining gofmt -l output is the repository-wide benign LF/CRLF condition, not these edits); go build ./... (pass); go vet ./internal/admin/... ./internal/app/... (pass); go test -count=1 ./internal/admin/... ./internal/app/... ./internal/config/... (pass); pnpm --dir internal/admin/ui run typecheck (pass); run lint (pass); run test (37 files, 451 tests pass); pnpm --dir internal/admin/ui run build (deterministic bundle; asset-drift gate reflects only the schema change); git diff --check (clean, only benign LF→CRLF notices)
- Commands unavailable: go test -race ./internal/admin/... and ./internal/app/... — UNVERIFIED: -race requires cgo and no C toolchain (gcc/clang) is installed in this environment (KNOWN_ENVIRONMENT_LIMITATION). Residual risk: data-race detection deferred to the CI race matrix; the new hook reuses the existing coordinator locking discipline (registration runs after c.mu is released, exactly like the pre-existing enqueue-failure completion path) and the registry's own mutex, adding no new shared state outside locked critical sections.
- Deviations:
  1. Requested `OnManagedApplyStarted func(admin.ManagedApplyStart) error` on ConfigApplyCoordinator plus a helper that projects ApplyResult via toAdminConfigApplyResult → implemented exactly, with the helper named notifyManagedApplyStarted mirroring the existing notifyManagedApplyComplete → rationale: matches the repository-native callback/notify pairing already present on the coordinator.
  2. Requested "the finalizer closure must capture trackingErr and append it to ManagedApplyFinalization.FinalizationError" → implemented by adding a finalizationErr parameter to notifyManagedApplyComplete and threading it into the existing admin.ManagedApplyFinalization{...} construction (the enqueue-failure call site passes "") → rationale: the coordinator builds ManagedApplyFinalization in exactly one place; parameterizing that single constructor avoids duplicating the finalization struct and keeps FinalizationError authoritative, and serve.go already forwards fin.FinalizationError onto both the runtime-overview outcome and the durable ledger record.
- Self-review findings: single-purpose diff — one new carrier type, one coordinator hook + helper, one registration call site, one composition-root wiring, one public-API field + its TS mirror, one regenerated bundle, one new focused test file. No unwired helpers (notifyManagedApplyStarted is called by applyCandidate; OnManagedApplyStarted is wired in serve.go). No error swallowing: the registration error is wrapped and carried to FinalizationError, never dropped; the accepted apply is never rolled back for a tracking failure (decision hierarchy: preserve the currently serving runtime / report exact truth after persistence). No secrets, bearer/token material, or raw TOML added; OwnerTokenID remains json:"-" and Deadline is low-cardinality metadata. The Console bundle was regenerated by `pnpm run build`, not hand-edited. The final audit-closure report was not touched.
- Independent review status: pending
- Reviewer blockers: none
- Blocker-fix SHA: n/a
- Accepted SHA: pending coordinator acceptance
- Next execution file: WS01 Slice 03 (audit/ws01-managed-ledger)

---

### WS01_MANAGED_LEDGER / 03_LOOKUP_API_AND_AUTHORIZATION.md

- Parent SHA: 039bd0ee0e6573bcdb757ed45c561a0de3d8e0e9
- Resulting SHA: recorded in this slice's completion report (the commit that includes this entry)
- Branch: audit/ws01-managed-ledger
- Agent role/context: implementation agent (Claude Opus 4.8, Act/Agent mode, highest reasoning); WS01 Slice 3
- Files changed:
  - internal/admin/route_catalog.go — RouteSpec gains AnyPermissions []rbac.Permission (logical-OR authorization); the /api/config/applies/{id} entry migrated from Permission: rbac.StatusRead to AnyPermissions: {rbac.StatusRead, rbac.ConfigApply} with an updated explanatory comment
  - internal/admin/routes.go — routes() dispatcher converted to a switch; new case `len(spec.AnyPermissions) > 0 → s.requireAnyPermission(...)` inserted before the Permissions and single-Permission cases (Public still first)
  - internal/admin/rbac.go — new requireAnyPermission middleware (authenticate once, authorize by looping over the accepted permissions) mirroring requirePermission's snapshot-mode dispatch (Blocked→503, RBAC→authn+authz-any, Legacy/Open→token/identity); new writeForbiddenAny 403 helper that lists the accepted permissions under required_any without leaking other principals/tokens
  - internal/admin/route_catalog_test.go — TestCatalogNoRouteDefaultsToPublic and TestCatalogPermissionsInCatalog extended to recognize AnyPermissions; new TestCatalogExactlyOneAuthorizationMode guard proving exactly one of Public/Permission/Permissions/AnyPermissions is set per route
  - internal/admin/api_managed_apply_test.go (extended) — RBAC HTTP-integration evidence for the AnyPermissions endpoint
- Production path verified: HTTP GET /api/config/applies/{id} → s.routes() builds the mux from Catalog → the entry's AnyPermissions selects s.requireAnyPermission([]{StatusRead, ConfigApply}, handleManagedApplyGet) → the middleware loads the single immutable authSnapshot and dispatches on mode: RBAC authenticates the bearer once via snap.policy.Authenticate, then authorizes by iterating the accepted permissions with snap.policy.Authorize (first match serves the handler with the identity in context); Legacy/Open reuse checkLegacyToken + legacyIdentity exactly as requirePermission; Blocked fails closed with 503. On no match the request receives the structured writeForbiddenAny 403. handleManagedApplyGet is unchanged and still emits the secret-free public projection (no actor/source IP/token digest) with Cache-Control: no-store.
- Behavior implemented: a principal permitted to APPLY configuration (config:apply) can now retrieve the secret-free result of its own class of operation at /api/config/applies/{id} even without status:read, because a more-privileged mutate capability implies the less-privileged read (AC-02, §2.8 Step 5). status:read continues to authorize the same endpoint. Authentication still happens exactly once; only the authorization decision is OR-combined. A principal holding neither permission receives a structured 403 listing the accepted permissions under required_any; the endpoint still never exposes raw config, actor, source IP, or token digest.
- Tests added: internal/admin/api_managed_apply_test.go — TestManagedApplyGet_AnyPermissionAuthorizes (HTTP integration through the full routes() stack under a real rbac.Policy with four principals: viewer=status:read→200, automation=custom config:apply-only role→200, metrics-only=unrelated permission→403, no token→401) and TestManagedApplyGet_ForbiddenBodyListsAcceptedPermissions (HTTP integration: asserts the 403 body error=forbidden and required_any = {status:read, config:apply} and leaks no token_digest/token_id/principals); internal/admin/route_catalog_test.go — TestCatalogExactlyOneAuthorizationMode (unit guard). No existing test weakened; the two extended catalog guards became strictly more permissive only for the new AnyPermissions mode.
- Commands run: gofmt -w on the five changed files (clean); go build ./... (pass); go vet ./internal/admin/... ./internal/app/... (pass); go test -count=1 ./internal/admin/... ./internal/app/... ./internal/config/... (pass); git diff --check (clean, only benign LF→CRLF notices)
- Commands unavailable: go test -race ./internal/admin/... and ./internal/app/... — UNVERIFIED: -race requires cgo and no C toolchain (gcc/clang) is installed in this environment (KNOWN_ENVIRONMENT_LIMITATION). Residual risk: data-race detection deferred to the CI race matrix; requireAnyPermission introduces no new shared mutable state — it reads the same atomically-loaded authSnapshot as requirePermission and only iterates a read-only permission slice.
- Console commands: none required — this slice is backend route authorization only (§2.8 Step 5). No internal/admin/ui source changed, so no typecheck/lint/test/build or asset-drift gate was needed; internal/admin/assets/dist was not touched.
- Deviations:
  1. Requested a single `snap.policy.Authenticate(r.Header.Get("Authorization"), now)` skeleton with a generic err→401 branch → implemented the repository-native two-branch authentication used by requirePermission, distinguishing rbac.ErrDisabled (structured "principal is disabled or expired" 401) from other errors → rationale: preserves the existing disabled/expired contract and keeps requireAnyPermission behaviourally identical to requirePermission except for the OR-authorization loop (decision hierarchy: prevent security weakening; reuse repository-native abstractions).
  2. Skeleton showed the legacy branch as the switch default with no explicit Open handling → implemented the same `default: // authModeLegacy, authModeOpen` fallthrough requirePermission already uses, where checkLegacyToken returns true for an empty token (Open) → rationale: reuses the exact existing behaviour so Legacy and Open modes are handled identically to every other admin route.
- Self-review findings: single-purpose diff across 3 production files + 2 test files, all under internal/admin. No unwired helpers — requireAnyPermission is reached via the routes() dispatcher and the /api/config/applies/{id} catalog entry; writeForbiddenAny is called only by requireAnyPermission. No error swallowing (authn errors return 401; unauthorized returns the structured 403). No secret/actor/source-IP/token-digest exposure (handler unchanged; the 403 body reports only the accepted permissions and the caller's own role). No generated bundle, closure report, or authentication/history/transaction truthfulness weakened — the change strictly widens which principals may READ an already-secret-free projection and never relaxes authentication or any mutating route.
- Independent review status: pending
- Reviewer blockers: none
- Blocker-fix SHA: n/a
- Accepted SHA: pending coordinator acceptance
- Next execution file: WS01 Slice 04 (audit/ws01-managed-ledger)

---

### WS01_MANAGED_LEDGER / 04_VERTICAL_TESTS_AND_HARDENING.md

- Parent SHA: 51ee2a58d698d032d6e2dee31941be35304d3da4
- Resulting SHA: recorded in this slice's completion report (the commit that includes this entry)
- Branch: audit/ws01-managed-ledger
- Agent role/context: implementation agent (Claude Opus 4.8, Act/Agent mode, highest reasoning); WS01 Slice 4
- Files changed:
  - internal/admin/server.go — new exported `(*Server).Handler() http.Handler` returning `s.routes()`; `New` now builds the listener's `http.Server.Handler` via `s.Handler()` so the exported accessor is the exact production handler, not a test-only shim. This lets the cross-package vertical test drive the real admin route stack (including route authorization) without reconstructing routes or bypassing the auth chokepoint.
  - internal/app/managed_apply_vertical_test.go (new) — the §2.9 production-lifecycle vertical test: a real ConfigApplyCoordinator apply → real 202 saved_not_live → GET /api/config/applies/<id> through the real admin.Server.Handler() observing 202/pending → release the withheld terminal reload → GET observes 200/terminal, without manually seeding the registry.
  - internal/admin/managed_apply_registry_slice4_test.go (new) — two registry hardening tests from §2.9 not previously covered: BeginPending-after-terminal must not downgrade, and Complete preserves start/deadline carried on the pending record.
  - internal/admin/api_managed_apply_authz_test.go (new) — the two remaining §2.9 authorization cases: disabled/expired credential → 401 (fail closed before the handler), and Blocked RBAC (cfg.RBAC.Enabled with nil policy) → 503, both asserting the secret-free projection is never served.
- Production path verified: HTTP GET /api/config/applies/{id} is served by the exact handler the listener runs. New builds `http.Server{Handler: s.Handler()}` and Handler() returns `s.routes()`; the vertical test issues the GET through that same `s.Handler()`. The apply path is unchanged from Slices 1–3: ApplyRaw persists the candidate, enqueues the reload, calls notifyManagedApplyStarted (OnManagedApplyStarted → registry.BeginPending) BEFORE the bounded synchronous wait returns the provisional saved_not_live 202, then the finalizer delivers the terminal result and calls OnManagedApplyComplete → registry.Complete. The test wires OnManagedApplyStarted/OnManagedApplyComplete with the exact field mapping serve.go installs (via the existing wireProductionLedger helper) so it exercises the real composition-root wiring.
- Behavior implemented: no production behavior change beyond exposing the already-constructed admin handler. This slice's substance is closure evidence: it proves end-to-end that a real 202 saved_not_live is immediately observable as 202/pending at the exact-ID endpoint (never a 404), that releasing the reload transitions the same record to 200/terminal with matching id/operation/result, that the generated ID is boot-scoped (rl_<12-hex>_<seq>), that the pending response carries the absolute deadline for deadline-aware polling, and that neither the pending nor terminal projection leaks actor/source IP/token digest/owner token. It also hardens the registry (no terminal→pending downgrade; start/deadline preserved at completion) and the authorization contract (disabled/expired→401, Blocked RBAC→503).
- Tests added: internal/app/managed_apply_vertical_test.go — TestManagedApplyLifecycle_202PendingThen200Terminal (HTTP integration through the full admin handler + real coordinator; no seeded registry; terminal reload withheld behind a channel; no sleeps). internal/admin/managed_apply_registry_slice4_test.go — TestBeginPendingAfterTerminalNoDowngrade, TestCompletePreservesStartAndDeadline (unit). internal/admin/api_managed_apply_authz_test.go — TestManagedApplyGet_DisabledOrExpiredCredential401 (HTTP integration, disabled + expired principals), TestManagedApplyGet_BlockedRBAC503 (HTTP integration, Blocked snapshot). No existing test weakened; existing coordinator-level pending evidence (config_apply_pending_test.go) and Slice-1/3 registry/authorization tests remain intact.
- Commands run: gofmt -w on the four changed files (clean); go build ./... (pass); go vet ./internal/admin/... ./internal/app/... (pass); go test -count=1 ./internal/admin/... ./internal/app/... ./internal/config/... (pass); targeted -v runs of all five new tests (pass); git diff --check (clean, only a benign LF→CRLF notice on server.go).
- Commands unavailable: go test -race ./internal/admin/... and ./internal/app/... — UNVERIFIED: `-race requires cgo` and no C toolchain (gcc/clang) is installed in this environment (KNOWN_ENVIRONMENT_LIMITATION). Residual risk: the vertical test's goroutine interleaving (finalizer vs synchronous waiter) and the registry concurrency test are not race-verified locally; deferred to the CI race matrix. The added tests introduce no new shared mutable production state — Handler() only returns the existing routes() mux and reads no server state beyond it.
- Console commands: none required — this slice is backend + Go test evidence only. The deadline field of the TS ManagedApplyRecord schema was already present from a prior slice; no internal/admin/ui source changed, so no typecheck/lint/test/build or asset-drift gate was needed; internal/admin/assets/dist was not touched.
- Deviations:
  1. The slice's §2.9 vertical test is specified as using "the actual coordinator and admin HTTP handler". There was no exported way to obtain the admin handler (routes() is unexported and admin cannot import app). Requested design (implicit): reach the handler directly → implemented design: add a minimal exported `(*Server).Handler()` and route `New` through it so the accessor IS the production handler → rationale: preserves the single auth chokepoint and avoids an unwired test-only seam (decision hierarchy: prefer repository-native abstractions; minimize scope; do not bypass route authorization).
  2. Several §2.9 checklist items (boot-scoped/legacy/malformed IDs, ClaimFinalization pending→finalizing, duplicate claim, OwnerTokenID preservation/serialization, pending-never-evicted, coordinator-level pending registration) were already implemented and tested by Slices 1–3. Requested design: add them here → implemented design: NOT re-added; only the genuinely-missing evidence (vertical HTTP lifecycle, no-downgrade, start/deadline preservation, disabled/expired-401, Blocked-503) was added → rationale: minimize scope and avoid duplicate truth; do not reimplement accepted earlier slices.
- Self-review findings: single-purpose diff — 1 production file (+11/-1) exposing the existing handler, plus 3 new test files. No unwired helpers: Handler() is called by New (production) and by the vertical test. No error swallowing. No secret/actor/source-IP/token-digest exposure — the tests assert the projections omit them, and Handler() changes no serialization. No authentication/authorization weakening — Handler() returns the same routes() mux that already applies the auth chokepoint; the 401/503 tests prove fail-closed behavior is unchanged. No generated bundle, closure report, or history/transaction truthfulness touched.
- Independent review status: pending
- Reviewer blockers: none
- Blocker-fix SHA: n/a
- Accepted SHA: pending coordinator acceptance
- Next execution file: WS02 (or the next coordinator-assigned slice)

---

### WS02_FINALIZATION / 01_COMPLETION_CONTRACT.md

- Parent SHA: bb666a655a14d25b4a3eaeaee5b5d6234b26224e
- Resulting SHA: recorded in this slice's completion report (the commit that includes this entry)
- Branch: audit/ws02-finalization
- Agent role/context: implementation agent (Act/Agent mode, highest reasoning); WS02 Slice 1
- Files changed:
  - internal/admin/server.go — new ManagedApplyCompletion{Context, Result, PreviousRaw} carrier type unifying the previously split history-write and completion-notification signals into ONE completion object handed to the composition root
  - internal/admin/config_apply.go — ConfigApplyResult gains FinalizationError string `json:"-"` (transport-only provenance; never serialized so the AC-05 invariant that the serialized result carries no history provenance is preserved)
  - internal/app/config_apply.go — removed the WriteManagedHistory hook; OnManagedApplyComplete is now `func(admin.ManagedApplyCompletion) admin.ManagedApplyFinalization`; notifyManagedApplyComplete takes the completion object and returns the fin under panic recovery (nil→zero fin); completeManagedApply forwards the exact previous on-disk bytes via ManagedApplyCompletion.PreviousRaw and threads HistorySnapshotID/HistoryError/FinalizationError from the returned fin onto the terminal result; the finalizer sets terminal.FinalizationError before completeManagedApply so a post-persistence tracking failure is echoed by the callback; toAdminConfigApplyResult projects FinalizationError
  - internal/app/serve.go — removed `coordinator.WriteManagedHistory = adminSrv.RecordManagedHistory`; OnManagedApplyComplete now derives ctx/res from the completion object, keeps the BeginFinalization single-claim guard, itself calls adminSrv.RecordManagedHistory(comp.Context, comp.Result, comp.PreviousRaw) AFTER claiming, and returns a ManagedApplyFinalization carrying the snapshot id / history error / echoed finalization error; toAdminConfigApplyResult projects FinalizationError
  - internal/app/config_apply_pending_test.go — wireProductionLedger and TestApplyStartedErrorSurfacesInFinalization migrated to the unified completion-object callback signature
  - internal/app/config_apply_test.go — all seven completion callbacks migrated to the unified signature; provenance-threading and panic tests updated to return/produce the fin themselves
- Production path verified: the async finalizer in applyCandidate (and the applyStageRestart + enqueue-failure terminal paths) route through completeManagedApply, which now hands the composition root a single ManagedApplyCompletion (request context, serialized terminal result, exact previous on-disk bytes). serve.go's OnManagedApplyComplete claims the transaction once via BeginFinalization, then performs the trusted history write itself (RecordManagedHistory) and returns the ManagedApplyFinalization; the coordinator threads that provenance back onto the terminal ApplyResult. History-writing and terminal finalization are now driven from one claim (H-05); the split WriteManagedHistory/OnManagedApplyComplete hooks are gone.
- Behavior implemented: unified managed-apply completion contract — one completion-object callback returns provenance instead of a void notification plus a separate history-write hook. The composition-root callback is the sole producer of the finalization (it calls RecordManagedHistory by itself after claiming). FinalizationError threads through the app result → admin projection (json:"-") → callback so a post-persistence pending-registration failure ("boom") surfaces in the returned fin. The serialized ConfigApplyResult never carries HistorySnapshotID/HistoryError/FinalizationError (AC-05 preserved); PreviousRaw (sensitive prior bytes) is forwarded only to the trusted in-process history writer.
- Tests added: none net-new; the existing WS01 completion/provenance/panic tests were migrated to the unified callback signature (TestManagedApplyFinalizationProvenanceThreaded, TestCompletionCallbackPanicDoesNotBlockHTTP, TestManagedApplyOutcomeCallbackFired, TestFastRestorationHTTPAndCallbackResultsMatch, TestShutdownReturnsCorrelatedSavedNotLive, TestSlowRestorationReturnsSavedNotLiveThenOneTerminalResult, TestApplyRegistersPendingRecordBeforeSavedNotLive, TestApplyStartedErrorSurfacesInFinalization). No test weakened; the provenance and panic-safety assertions were preserved and re-expressed against the returned fin.
- Commands run: go build ./... (pass); go vet ./internal/admin/... ./internal/app/... (pass); gofmt -d internal/app/config_apply.go internal/app/serve.go internal/admin/server.go internal/admin/config_apply.go (empty — no formatting diffs; repo-wide gofmt -l output is the pre-existing benign CRLF condition, not these edits); go test -count=1 ./internal/admin/... ./internal/app/... ./internal/config/... (all three packages ok: admin, app, config).
- Commands unavailable: go test -race ./internal/admin/... and ./internal/app/... — UNVERIFIED: -race requires cgo and no C toolchain (gcc/clang) is installed in this environment (KNOWN_ENVIRONMENT_LIMITATION). Residual risk: the finalizer/waiter interleaving and the single-claim guard are not race-verified locally; deferred to the CI race matrix. The refactor adds no new shared mutable state — the completion object is built and consumed on the finalizer goroutine, and BeginFinalization retains its existing single-claim discipline.
- Console commands: none required — this slice is backend Go only. No internal/admin/ui source changed; no typecheck/lint/test/build or asset-drift gate was needed; internal/admin/assets/dist was not touched.
- Deviations:
  1. Requested a single completion object returning provenance → the spec code blocks are normative for behavior, not text. Implemented ManagedApplyCompletion with Context/Result/PreviousRaw and OnManagedApplyComplete returning admin.ManagedApplyFinalization; the composition root itself calls RecordManagedHistory after claiming → rationale: this makes the callback the sole producer of the fin and removes the separate WriteManagedHistory hook, satisfying the one-claim requirement (H-05) with repository-native types.
  2. FinalizationError surfacing: admin.ConfigApplyResult.FinalizationError uses `json:"-"` and the coordinator sets terminal.FinalizationError before completeManagedApply; the callbacks echo comp.Result.FinalizationError into the returned fin → rationale: preserves the AC-05 non-serialization invariant while letting the post-persistence "boom" tracking error reach fin.FinalizationError (required by TestApplyStartedErrorSurfacesInFinalization) without weakening the test.
- Self-review findings: single-purpose diff across 4 production files + 2 test files. No unwired helpers (notifyManagedApplyComplete/completeManagedApply are called by the three terminal paths; OnManagedApplyComplete is wired in serve.go and the tests). No error swallowing — the tracking error is threaded to FinalizationError; the callback panic is recovered so it cannot wedge the coordinator. No secrets serialized — PreviousRaw is forwarded only to the in-process history writer; FinalizationError/HistorySnapshotID/HistoryError all stay json:"-" / non-serialized. No generated bundle, closure report, or authentication/history/transaction truthfulness weakened.
- Independent review status: pending
- Reviewer blockers: none
- Blocker-fix SHA: n/a
- Accepted SHA: pending coordinator acceptance
- Next execution file: WS02 Slice 02 (or the next coordinator-assigned slice)

---

## Program-level open items

- Exact-head CI pending: yes - exact-head CI not yet run against the bootstrap commit
- Independent final re-audit pending: yes
- Security/concurrency sign-off pending: yes
- Frontend sign-off pending: yes (baseline Console suite green; per-slice sign-off pending)
- Closure report status: not started (bootstrap only; no audit item closed)

---

## Workstream acceptance records

### Workstream acceptance record - WS01_MANAGED_LEDGER

- Workstream: WS01_MANAGED_LEDGER - Managed apply identity and pending ledger lifecycle
- Branch: audit/ws01-managed-ledger
- Base SHA: e707c41587a335cc92b643068b1c46053eff2621
- Final reviewed SHA: a1c7a3e11195477601b30a067e8a2ed0cf509b91
- Independent reviewer verdict: APPROVE
- Reviewer context/identifier: Independent APPROVE, 0 required blocker fixes; all 5 objectives verified on the real production path (pending-before-202 closed; boot-scoped IDs w/ strict grammar; secret-free projection, OwnerTokenID json:"-"; config:apply OR status:read fail-closed 401/503; registry no-downgrade + start/deadline preserved). Non-blocking: ClaimFinalization/ManagedApplyFinalizing tested-but-unwired, correctly staged for WS02.
- Focused test evidence: go build ./...; go vet ./internal/admin/... ./internal/app/...; go test -count=1 ./internal/admin/... ./internal/app/... ./internal/config/... (all pass); five new WS01 tests PASS (vertical 202->terminal, no-downgrade, start/deadline preserved, disabled/expired-401, Blocked-503).
- Race evidence: UNVERIFIED - go test -race unavailable locally (no cgo/C toolchain); deferred to CI race matrix at exact SHA.
- Console evidence: no additional Console change in this acceptance; the Console client schema (deadline) and the prior green Console suite baseline remain intact from earlier slices; internal/admin/assets/dist not touched during acceptance.
- Commands unavailable and deferred to CI: (1) -race on finalizer/waiter interleaving + registry concurrency; (2) exact-SHA CI green.
- Non-blocking follow-ups: ClaimFinalization/ManagedApplyFinalizing tested-but-unwired, correctly staged for WS02.
- Next workstream branch: audit/ws02-finalization
- Next expected parent SHA: a1c7a3e11195477601b30a067e8a2ed0cf509b91
