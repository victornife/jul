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
- Next execution file: WS02 (or the next coordinator-assigned slice)

---

## Program-level open items

- Exact-head CI pending: yes - exact-head CI not yet run against the bootstrap commit
- Independent final re-audit pending: yes
- Security/concurrency sign-off pending: yes
- Frontend sign-off pending: yes (baseline Console suite green; per-slice sign-off pending)
- Closure report status: not started (bootstrap only; no audit item closed)
