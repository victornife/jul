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

### WS02_FINALIZATION / 02_EXPLICIT_FAILURE_HANDLING.md

- Parent SHA: fac342ad727f74f2e9460c0b0c86879119b597b7
- Resulting SHA: recorded in this slice's completion report (the commit that includes this entry)
- Branch: audit/ws02-finalization
- Agent role/context: implementation agent (Act/Agent mode, highest reasoning); WS02 Slice 2
- Files changed:
  - internal/app/config_apply.go — added the `OnManagedApplyFinalizationError func(applyID string, err error)` coordinator hook; replaced the silent `defer func() { _ = recover() }()` in notifyManagedApplyComplete with EXPLICIT panic handling (WS02 §3.6): the recovered panic is reconstructed into `fin.FinalizationError` ("managed apply finalization panic: %v") via a named return and, when wired, reported to `OnManagedApplyFinalizationError(comp.Result.ApplyID, errors.New(fin.FinalizationError))`.
  - internal/app/serve.go — added a process-lifetime `lastManagedApplyFinalizationErr atomic.Pointer[string]` advisory-health flag; extended deps.AdminHealth to surface it as an advisory `managed_finalization` degradation on /readyz/overview after the existing admin-reload check; wired coordinator.OnManagedApplyFinalizationError to (1) write a structured error log, (2) increment the finalization-error metric, (3) set the advisory health flag, and (4) best-effort write a terminal ledger record carrying FinalizationError.
  - internal/observability/metrics.go — added the unlabeled `jul_managed_apply_finalization_errors_total` counter (registered on the private registry) with the `ObserveManagedApplyFinalizationError()` wrapper; the counter is deliberately label-free so a callback-panic signal cannot leak apply IDs, actors, or config versions as unbounded cardinality.
  - internal/observability/cardinality_test.go — registered `jul_managed_apply_finalization_errors_total` (labels: none) in the frozen TestMetricLabelPolicy map and exercised its hook in exerciseAllMetrics, keeping the cardinality-regression guard authoritative (test strengthened, not weakened).
  - internal/app/config_apply_test.go — strengthened TestCompletionCallbackPanicDoesNotBlockHTTP: it still asserts the committed apply stays OK and the HTTP path is not blocked, and now additionally asserts the recovered panic is threaded onto result.FinalizationError and that OnManagedApplyFinalizationError is invoked exactly once with the apply ID and the reconstructed panic error.
- Production path verified: the async finalizer in applyCandidate (and the applyStageRestart + enqueue-failure terminal paths) call completeManagedApply → notifyManagedApplyComplete. When the composition-root OnManagedApplyComplete callback panics, the deferred recover now (a) sets fin.FinalizationError, which completeManagedApply threads onto the terminal ApplyResult, and (b) invokes the coordinator's OnManagedApplyFinalizationError. In serve.go that hook logs the failure, increments jul_managed_apply_finalization_errors_total, stores the advisory lastManagedApplyFinalizationErr surfaced by deps.AdminHealth, and best-effort completes a per-ID ledger record carrying FinalizationError. The committed apply is never rolled back — the raw configuration stays roll-back-able — and the finalizer goroutine is never wedged.
- Behavior implemented: a managed-apply completion-callback panic is now EXPLICIT rather than silently discarded (WS02 §3.2 defect 3). The recovered panic surfaces four ways: threaded onto the terminal result's FinalizationError, a structured error log, the jul_managed_apply_finalization_errors_total metric, and an advisory managed_finalization admin-health degradation, plus a best-effort ledger record. No serialized ConfigApplyResult gains a new serialized field (FinalizationError remains json:"-"; the new metric is unlabeled), so the AC-05 non-serialization invariant and the metric-cardinality policy are both preserved.
- Tests added: TestCompletionCallbackPanicDoesNotBlockHTTP strengthened (package integration: coordinator, no HTTP server) — proves a finalization-callback panic does not block the HTTP path, does not fail the committed apply, is threaded onto FinalizationError, and invokes OnManagedApplyFinalizationError exactly once with the apply ID and reconstructed error. TestMetricLabelPolicy/exerciseAllMetrics extended (unit: observability policy) to cover the new unlabeled counter so the cardinality guard stays authoritative. No existing test weakened.
- Commands run: go build ./... (pass); go vet ./internal/admin/... ./internal/app/... ./internal/observability/... (pass); gofmt -l internal/app/config_apply.go internal/app/serve.go internal/app/config_apply_test.go internal/observability/metrics.go internal/observability/cardinality_test.go (empty — no formatting diffs); git diff --check (clean, only benign LF→CRLF working-copy warnings, the pre-existing environment condition); go test -count=1 ./internal/admin/... ./internal/app/... ./internal/config/... ./internal/observability/... (all four packages ok: admin, app, config, observability).
- Commands unavailable: go test -race ./internal/admin/... ./internal/app/... ./internal/observability/... — UNVERIFIED: -race requires cgo and no C toolchain (gcc/clang) is installed (KNOWN_ENVIRONMENT_LIMITATION); deferred to the CI race matrix. Residual risk: the lastManagedApplyFinalizationErr atomic pointer and the best-effort ledger Complete are exercised on the finalizer goroutine and read by the admin-health/HTTP goroutine; both use existing concurrency-safe primitives (atomic.Pointer, the registry's internal mutex), so no new unsynchronized shared state was introduced.
- Console commands: none required — this slice is backend Go only. No internal/admin/ui source changed; no typecheck/lint/test/build or asset-drift gate was needed; internal/admin/assets/dist was not touched.
- Deviations:
  1. The §3.6 skeleton references completion.Result.ApplyID; the repository-native completion object is admin.ManagedApplyCompletion, so the implementation uses comp.Result.ApplyID (the same field on the repository-native carrier) → rationale: the skeleton is normative for behavior, not identifiers; the field and semantics match exactly.
  2. "Set an advisory health state" and "best-effort write a terminal ledger record" are realized with repository-native surfaces (deps.AdminHealth via a new atomic.Pointer[string], and ManagedApplyRegistry.Complete) rather than a new subsystem → rationale: reuses existing, semantically-equivalent abstractions and avoids scope creep, per the decision hierarchy (prefer repository-native abstractions, minimize scope).
- Self-review findings: single-purpose diff across 3 production files + 2 test files. No unwired helpers — OnManagedApplyFinalizationError is invoked by notifyManagedApplyComplete and wired in serve.go; ObserveManagedApplyFinalizationError is called by the serve.go hook and the cardinality exerciser. No error swallowing — the previously discarded panic is now surfaced four ways. No secrets serialized — the new metric is unlabeled, the advisory detail and the ledger FinalizationError carry only the panic message (never PreviousRaw, TOML, or credentials); FinalizationError remains json:"-". No authentication/history/transaction truthfulness weakened; no generated bundle or closure report touched.
- Independent review status: pending
- Reviewer blockers: none
- Blocker-fix SHA: n/a
- Accepted SHA: pending coordinator acceptance
- Next execution file: WS02 Slice 03 (or the next coordinator-assigned slice)

---

### WS02_FINALIZATION / 03_FINALIZER_ORCHESTRATOR.md

- Parent SHA: 9807214ee122b068eed495ad3d3fa54d0b5fce29
- Resulting SHA: recorded in this slice's completion report (the commit that includes this entry)
- Branch: audit/ws02-finalization
- Agent role/context: implementation agent (Act/Agent mode, highest reasoning); WS02 Slice 3
- Files changed:
  - internal/app/managed_apply_finalizer.go (new) — the single terminal-finalization orchestrator `managedApplyFinalizer` with `Finalize(admin.ManagedApplyCompletion) admin.ManagedApplyFinalization` (WS02 §3.7 Step 4). It CLAIMS the transaction via `ManagedApplyRegistry.ClaimFinalization` BEFORE the trusted history write (fixing §3.2 defect 2), performs `RecordManagedHistory`, emits `ObserveManagedApplyFinalized`/`ObserveManagedApplyHistory`, records the terminal audit via `RecordManagedApplyOutcome`, publishes the durable per-ID ledger record via `Complete` and — unlike the former inline callback — no longer ignores the `Complete` error (§3.2 defect 4): it threads it onto FinalizationError, reports it, and retries once. A claim error fails closed with an explicit FinalizationError (§3.2 defect 5); FinalizationError is reliably threaded onto the terminal result (§3.2 defect 6). Ships the skeleton helpers `appendFinalizationError`, `managedReloadOutcome`, `managedRestoredLabel`, `projectManagedApplyOutcome`, plus `updateLatestIfNewest` (the AC-04 high-water guard) and `reportFinalizationError` (structured log + finalization-error metric + advisory-health flag).
  - internal/app/serve.go — replaced the ~110-line inline `coordinator.OnManagedApplyComplete` body with construction of the `managedApplyFinalizer` (registry, adminSrv, metrics, log, the existing `lastManagedApply`/`lastManagedApplySeq`/`lastManagedApplyMu` high-water trio, and a `setAdvisoryHealth` sink that stores into the existing `lastManagedApplyFinalizationErr` pointer) and assigned `coordinator.OnManagedApplyComplete = finalizer.Finalize`. The §3.6 `OnManagedApplyFinalizationError` panic-abort hook and `deps.AdminHealth` advisory surface from Slice 2 are unchanged.
  - internal/observability/metrics.go — added the bounded `jul_managed_apply_history_total{operation,result}` counter (registered on the private registry) with the `ObserveManagedApplyHistory(operation, result)` wrapper; `result` is the snapshot disposition (recorded/skipped/failed) and both labels are bounded, low-cardinality values — never an apply ID, actor, path or version.
  - internal/observability/cardinality_test.go — registered `jul_managed_apply_history_total` (labels: operation, result) in the frozen TestMetricLabelPolicy map and exercised its hook in exerciseAllMetrics, keeping the cardinality-regression guard authoritative (test strengthened, not weakened).
  - internal/app/managed_apply_finalizer_test.go (new) — TestManagedApplyFinalizerExactlyOnce.
- Production path verified: HTTP apply → ConfigApplyCoordinator.ApplyRaw persists the candidate and enqueues the reload; the async finalizer goroutine builds the terminal ApplyResult and calls `completeManagedApply → notifyManagedApplyComplete → coordinator.OnManagedApplyComplete`, which is now `managedApplyFinalizer.Finalize`. Finalize claims the ID (ClaimFinalization), writes the configuration-history snapshot (adminSrv.RecordManagedHistory, consuming the sensitive PreviousRaw in-process only), emits the finalized + history metrics, records the terminal audit (adminSrv.RecordManagedApplyOutcome), publishes the durable per-ID ledger record (registry.Complete) carrying HistorySnapshotID/HistoryError/FinalizationError, and advances the singular latest-outcome pointer under the monotonic high-water guard. A duplicate terminal callback is deduplicated by the claim and repeats no side effect. This slice creates and wires the orchestrator only; routing the coordinator's hot/enqueue/stage terminal paths through completeManagedApply is WS02 Slice 4 (§3.8) and was deliberately not started.
- Behavior implemented: every managed-apply terminal callback now flows through ONE orchestrator that claims-before-history and runs each side effect exactly once per apply ID. The terminal-ledger Complete error is surfaced (log + finalization-error metric + advisory health + FinalizationError + one retry) instead of silently ignored (§3.2 defect 4); a claim error fails closed (§3.2 defect 5); history no longer precedes the finalization claim (§3.2 defect 2). No serialized ConfigApplyResult gains a new field (FinalizationError stays json:"-"); the new metric is bounded to operation×result, so the AC-05 non-serialization invariant and the metric-cardinality policy are both preserved.
- Tests added: TestManagedApplyFinalizerExactlyOnce (package integration: real ConfigApplyCoordinator + real admin.Server history writer + real ManagedApplyRegistry; no HTTP server, no sleeps, no manually seeded ledger state) — drives a real applied_live apply through the production wiring (coordinator.OnManagedApplyComplete = finalizer.Finalize) and proves exactly-once terminal finalization: exactly one raw history snapshot on disk whose id is threaded onto the returned finalization AND the durable per-ID ledger record, the latest-outcome pointer advanced, no advisory-health flag set on a clean finalize, and a duplicate terminal callback deduplicated by the ClaimFinalization guard (no second snapshot, no new FinalizationError, same recorded provenance). TestMetricLabelPolicy/exerciseAllMetrics extended (unit: observability policy) to cover the new bounded counter so the cardinality guard stays authoritative. No existing test weakened.
- Commands run: go build ./... (pass); go vet ./internal/admin/... ./internal/app/... ./internal/observability/... (pass); gofmt -l internal/app/managed_apply_finalizer.go internal/app/managed_apply_finalizer_test.go internal/app/serve.go internal/observability/metrics.go internal/observability/cardinality_test.go (empty after gofmt -w on the two new files — no formatting diffs); git diff --check (clean, only benign LF→CRLF working-copy warnings, the pre-existing environment condition); go test -count=1 ./internal/admin/... ./internal/app/... ./internal/config/... ./internal/observability/... (all four packages ok: admin, app, config, observability).
- Commands unavailable: go test -race ./internal/admin/... ./internal/app/... ./internal/observability/... — UNVERIFIED: -race requires cgo and no C toolchain (gcc/clang) is installed (KNOWN_ENVIRONMENT_LIMITATION); deferred to the CI race matrix. Residual risk: the finalizer runs on the coordinator's async finalizer goroutine and touches the latest-outcome pointer/seq (guarded by the existing latestMu + atomic.Uint64 CAS), the registry (its internal mutex), and the advisory-health pointer (atomic.Pointer) — all existing concurrency-safe primitives, so no new unsynchronized shared state was introduced.
- Console commands: none required — this slice is backend Go only. No internal/admin/ui source changed; no typecheck/lint/test/build or asset-drift gate was needed; internal/admin/assets/dist was not touched.
- Deviations:
  1. The §3.7 skeleton types the latest-outcome guard fields as embedded pointers and references `completion.Result.ApplyID`; the implementation uses the repository-native `admin.ManagedApplyOutcome`/`ManagedApplyRegistry`/`ManagedApplyCompletion` carriers and the process-lifetime `lastManagedApply*` trio already present in serve.go → rationale: the skeleton is normative for behavior, not identifiers; reuses existing high-water state instead of introducing a parallel one (prefer repository-native abstractions, minimize scope).
  2. The skeleton calls `f.metrics.ObserveManagedApplyHistory(...)`, which did not exist. Added it as a bounded `jul_managed_apply_history_total{operation,result}` counter rather than inventing per-ID labels → rationale: makes the history side effect observable exactly as the skeleton specifies while keeping the metric-cardinality policy authoritative (registered in the frozen cardinality test).
  3. Advisory health and the terminal ledger record are realized with the repository-native surfaces wired in Slice 2 (the `lastManagedApplyFinalizationErr atomic.Pointer[string]` behind deps.AdminHealth, and `ManagedApplyRegistry.Complete`) via a `setAdvisoryHealth` sink on the finalizer, not a new subsystem → rationale: reuses existing, semantically-equivalent abstractions and avoids scope creep. The §3.6 panic-abort hook remains the writer of the terminal record for the panic path; the finalizer owns the normal-path Complete.
- Self-review findings: single-purpose diff across 2 production files + 1 production-adjacent metric file + 2 test files (1 new). No unwired helpers — managedApplyFinalizer.Finalize is assigned to coordinator.OnManagedApplyComplete in serve.go; ObserveManagedApplyHistory is called by Finalize and the cardinality exerciser; every skeleton helper (appendFinalizationError, managedReloadOutcome, managedRestoredLabel, projectManagedApplyOutcome, updateLatestIfNewest, reportFinalizationError) is reachable from Finalize. No error swallowing — the previously ignored registry.Complete error is now surfaced five ways (log, metric, advisory health, FinalizationError, one retry). No secrets serialized — PreviousRaw is consumed only by the in-process history writer; the new metric is bounded to operation×result; the advisory detail and ledger FinalizationError carry only the bounded error message; FinalizationError stays json:"-". No authentication/history/transaction truthfulness weakened; no generated bundle or closure report touched.
- Independent review status: pending
- Reviewer blockers: none
- Blocker-fix SHA: n/a
- Accepted SHA: pending coordinator acceptance
- Next execution file: WS02 Slice 04 (or the next coordinator-assigned slice)

---

### WS02_FINALIZATION / 04_ROUTE_ALL_TERMINAL_PATHS.md

- Parent SHA: b95f8d642f931700b7fef034fc344e7f0ba4f594
- Resulting SHA: recorded in this slice's completion report (the commit that includes this entry)
- Branch: audit/ws02-finalization
- Agent role/context: implementation agent (Act/Agent mode, highest reasoning); WS02 Slice 4
- Files changed:
  - internal/app/config_apply.go — completed the stage_restart terminal result (WS02 §3.8 "Stage success" / §3.2 defect 8) so a committed stage carries the same first-class persistence truth as the hot path before it is routed through the single `completeManagedApply` helper: added `Persisted: true` (the candidate bytes are on disk), `FinalDiskVersion: persistedVersion` (the staged candidate now on disk), and `FinalServingVersion: liveVersion` (the still-serving live version, unchanged since a stage does not touch the running runtime). The three §3.8 terminal paths — hot completion, enqueue failure, and stage success — were already routed through `completeManagedApply` by the WS02 completion-contract refactor (commit fac342ad); this slice's remaining §3.8 work was the stage result's persistence fields, so no `recordManagedHistory(...)` + `notifyManagedApplyComplete(...)` inline pair remained to replace.
  - internal/app/config_apply_stage_finalize_test.go (new) — TestStageRestartTerminalFinalizedThroughLedger.
- Production path verified: HTTP stage apply → ConfigApplyCoordinator.ApplyRaw → applyStageRestart persists the candidate atomically, writes+promotes the planned-restart sidecar (no live reload is enqueued), builds the complete terminal ApplyResult (OK, Persisted, PersistedVersion/DesiredVersion, FinalDiskVersion = the staged candidate, FinalServingVersion = the still-serving live version), and routes it through the SINGLE `completeManagedApply → notifyManagedApplyComplete → coordinator.OnManagedApplyComplete` helper — the same terminal orchestrator the hot and enqueue-failure paths use. In production OnManagedApplyComplete is `managedApplyFinalizer.Finalize` (WS02 Slice 3), which claims-before-history and publishes exactly one durable per-ID terminal ledger record. Because a stage submits no reload, OnManagedApplyStarted (the provisional saved_not_live pending registration) is not fired; the terminal record is created directly by the completion helper.
- Behavior implemented: every persisted mutation — hot apply, enqueue failure, and committed stage_restart (create and update) — reaches its terminal state through one finalization helper, and the stage terminal result now reports complete persistence truth (Persisted/FinalDiskVersion/FinalServingVersion) instead of omitting it (§3.2 defect 8). No serialized secret or raw config is added; FinalDiskVersion/FinalServingVersion are bounded canonical versions already carried by the hot path. No reload-history separate write precedes the finalization claim on the stage path (there is no separate stage history write; the single completeManagedApply owns it).
- Tests added: TestStageRestartTerminalFinalizedThroughLedger (package integration: real ConfigApplyCoordinator + file-backed PlannedRestartStore + real stage preflight, wired to a real admin.ManagedApplyRegistry through the shared production-fidelity harness wireProductionLedger — the same OnManagedApplyStarted/OnManagedApplyComplete field mapping serve.go installs; no HTTP server, no sleeps, no manually seeded ledger state, no reload goroutine). It drives a real committed stage_restart and proves: the terminal result and the durable per-ID ledger record both report Persisted=true, FinalDiskVersion=the staged candidate, and FinalServingVersion=the still-serving live version (asserting disk≠serving so FinalServingVersion is not a copy of the on-disk candidate); OnManagedApplyStarted did NOT fire (a stage enqueues no reload); the terminal completion callback fired exactly once with no FinalizationError; and exactly one terminal record exists for the apply ID (TerminalCount()==1). No existing test weakened.
- Commands run: go build ./... (pass); go vet ./internal/admin/... ./internal/app/... ./internal/observability/... (pass); gofmt -l internal/app/config_apply.go internal/app/config_apply_stage_finalize_test.go (empty — no formatting diffs); git diff --check (clean, only the benign LF→CRLF working-copy warning, the pre-existing environment condition); go test -count=1 ./internal/admin/... ./internal/app/... ./internal/config/... ./internal/observability/... (all four packages ok: admin, app, config, observability); go test -count=1 -run TestStageRestartTerminalFinalizedThroughLedger ./internal/app/ (PASS).
- Commands unavailable: go test -race ./internal/admin/... ./internal/app/... — UNVERIFIED: -race requires cgo and no C toolchain (gcc/clang) is installed (KNOWN_ENVIRONMENT_LIMITATION); deferred to the CI race matrix. Residual risk: the stage_restart terminal path is synchronous on the caller's goroutine (no reload finalizer goroutine, no restoration), and it only reuses existing concurrency-safe primitives (applyMu/c.mu for the file write, the registry's internal mutex for Complete), so no new unsynchronized shared state was introduced.
- Console commands: none required — this slice is backend Go only. No internal/admin/ui source changed; no typecheck/lint/test/build or asset-drift gate was needed; internal/admin/assets/dist was not touched.
- Deviations:
  1. Requested design (§3.8 "Hot terminal completion"): replace an inline `terminal = c.recordManagedHistory(...)` + `c.notifyManagedApplyComplete(...)` pair with a single `completeManagedApply(...)` call on the hot path. Implemented design: no change was needed — the hot, enqueue-failure, and stage terminal paths already route through `completeManagedApply` (introduced by the WS02 completion-contract refactor, commit fac342ad, before this slice's parent). Rationale: the skeleton is normative for the end-state behavior (one helper owns every terminal path), which is already satisfied; re-introducing and re-replacing an inline history+notify pair would be a pointless churn that violates minimize-scope. The remaining, not-yet-satisfied §3.8 behavior was the stage result's persistence fields (§3.2 defect 8), which this slice implements.
- Self-review findings: single-purpose production diff (three added struct fields + an explanatory comment in applyStageRestart) plus one new package-integration test. No unwired code — the new fields flow through the existing completeManagedApply → toAdminConfigApplyResult → registry.Complete path and are asserted on both the returned result and the durable ledger record. No error swallowing — no error handling was touched. No secrets serialized — FinalDiskVersion/FinalServingVersion are bounded canonical versions already emitted by the hot path; no raw TOML/secret/actor/token added; FinalizationError stays json:"-". No authentication/authorization/history/transaction truthfulness weakened; no generated Console bundle or closure report touched. The new test uses only the production wiring (wireProductionLedger), no sleeps, and no manually seeded ledger state.
- Independent review status: pending
- Reviewer blockers: none
- Blocker-fix SHA: n/a
- Accepted SHA: pending coordinator acceptance
- Next execution file: the next coordinator-assigned slice (WS02 §3.8 terminal routing complete)

---

### WS02_FINALIZATION / 05_ADVISORY_HEALTH_TESTS_REVIEW.md

- Parent SHA: ec2fce451e52e505f2d9d26145124f3f20294294
- Resulting SHA: recorded in this slice's completion report (the commit that includes this entry)
- Branch: audit/ws02-finalization
- Agent role/context: implementation agent (Act/Agent mode, highest reasoning); WS02 Slice 5
- Files changed:
  - internal/admin/server.go — added the `ManagedApplyAdvisory{Healthy, At, ApplyID, Detail}` type (WS02 §3.9) and the `Deps.ManagedApplyFinalizationHealth func() *ManagedApplyAdvisory` hook. The advisory carries only bounded, low-cardinality metadata (never raw TOML, secrets, or actor tokens) and is documented as non-readiness.
  - internal/admin/projection_types.go — added `RuntimeOverview.ManagedApplyFinalization *ManagedApplyAdvisory` with json tag `managed_apply_finalization,omitempty`, rendered as an advisory finalization banner distinct from `admin_health` and `last_managed_apply`.
  - internal/admin/api.go — `handleRuntimeOverview` now populates `out.ManagedApplyFinalization` from `s.deps.ManagedApplyFinalizationHealth()` when wired, independently of `adminHealthProjection()`.
  - internal/app/serve.go — replaced the process-lifetime `lastManagedApplyFinalizationErr atomic.Pointer[string]` with `lastManagedApplyFinalization atomic.Pointer[admin.ManagedApplyAdvisory]`; REMOVED the `managed_finalization` branch from `deps.AdminHealth` (decoupling the finalization advisory from `/readyz`, per §3.9 "Do not connect it to readiness"); wired `deps.ManagedApplyFinalizationHealth` to read that pointer; updated the `OnManagedApplyFinalizationError` panic hook to publish an unhealthy advisory (ApplyID+detail) instead of a `*string`; changed the finalizer field from `setAdvisoryHealth(detail string)` to `setAdvisory(admin.ManagedApplyAdvisory)`.
  - internal/app/managed_apply_finalizer.go — replaced `setAdvisoryHealth func(detail string)` with `setAdvisory func(admin.ManagedApplyAdvisory)`; added `publishAdvisory` (called on every terminalization: healthy after a clean finalize — clearing any prior degradation — else unhealthy) and `advisoryDetail` (FinalizationError precedence, else a `configuration history: <err>` degradation); `reportFinalizationError` now publishes an unhealthy advisory on claim/complete failure. History snapshot/metadata failures are now tracked as an advisory degradation (§3.9's three tracked sources: finalization panic, terminal-ledger completion failure, history snapshot/metadata failure).
  - internal/app/managed_apply_finalizer_test.go — updated the existing exactly-once test to the new `setAdvisory` signature and strengthened its clean-finalize assertion: a clean finalize now publishes a HEALTHY advisory carrying the apply ID and no detail (not nil).
  - internal/admin/managed_apply_advisory_test.go (new) — TestManagedApplyFinalizationAdvisoryNonReadiness.
  - internal/app/managed_apply_advisory_test.go (new) — TestManagedApplyFinalizationAdvisoryTracksHistoryDegradationAndClears.
- Production path verified: async terminal finalization → coordinator.OnManagedApplyComplete = managedApplyFinalizer.Finalize → publishAdvisory → serve.go's `setAdvisory` stores the process-lifetime `lastManagedApplyFinalization` pointer → admin `handleRuntimeOverview` reads it through `deps.ManagedApplyFinalizationHealth` and serializes `managed_apply_finalization`. The finalization-callback panic path (coordinator recover → OnManagedApplyFinalizationError) publishes the same advisory shape. `/readyz` (admin.handleReadyz → Server.AdminHealthStatus → deps.AdminHealth) no longer observes any finalization degradation — the `managed_finalization` reason was removed — so a finalization/history degradation is visible in the Overview without ever failing readiness or rolling back a committed apply.
- Behavior implemented: WS02 §3.9 advisory, non-readiness managed-apply finalization health. It tracks the three degradation sources (finalization panic; terminal-ledger completion failure; configuration-history snapshot/metadata failure), is cleared only by a subsequent managed transaction that finalizes without finalization/history degradation, is exposed through the Runtime Overview as `managed_apply_finalization`, and is NOT connected to readiness. A finalization failure is now visible without falsely rolling back a committed runtime apply (§3.11).
- Tests added: TestManagedApplyFinalizationAdvisoryNonReadiness (package admin, HTTP integration over the real route stack s.routes(): drives GET /api/runtime/overview and GET /readyz through httptest; proves the advisory is omitted before any apply, that a Healthy=false advisory is surfaced in the overview but leaves /readyz=200 and admin_health absent, and that a subsequent Healthy=true advisory is surfaced — the §3.9 non-readiness invariant). TestManagedApplyFinalizationAdvisoryTracksHistoryDegradationAndClears (package app, package integration: real ConfigApplyCoordinator + real admin.Server history writer + real ManagedApplyRegistry, no HTTP server, no sleeps, no manually seeded ledger state, no mocked writer; the history failure is injected deterministically by making the configured history-dir path a regular file so the real RecordManagedHistory os.MkdirAll fails). It proves a history snapshot failure publishes an UNHEALTHY advisory carrying the apply ID and a `configuration history` detail while the committed apply stays OK=true and the durable ledger record carries the history_error, then that un-poisoning the dir and running a second clean apply publishes a HEALTHY advisory that clears the prior degradation. Existing TestManagedApplyFinalizerExactlyOnce updated (not weakened): its clean-finalize assertion now proves the healthy advisory is published. No existing test weakened.
- Commands run: go build ./... (pass); go vet ./internal/admin/... ./internal/app/... ./internal/observability/... (pass); gofmt -l on all changed files (empty after gofmt -w — no formatting diffs); go test -count=1 -run TestManagedApplyFinalizerExactlyOnce|TestManagedApplyFinalizationAdvisoryTracksHistoryDegradationAndClears ./internal/app/ (PASS); go test -count=1 -run TestManagedApplyFinalizationAdvisoryNonReadiness ./internal/admin/ (PASS); go test -count=1 ./internal/admin/... ./internal/app/... ./internal/config/... ./internal/observability/... (all four packages ok); git diff --check (clean, only the benign LF→CRLF working-copy warning, the pre-existing environment condition).
- Commands unavailable: go test -race ./internal/admin/... ./internal/app/... — UNVERIFIED: -race requires cgo and no C toolchain (gcc/clang) is installed (KNOWN_ENVIRONMENT_LIMITATION); deferred to the CI race matrix. Residual risk: the advisory is a single process-lifetime atomic.Pointer[admin.ManagedApplyAdvisory] published under the finalizer's existing terminal path (already serialized per ID by the ledger claim) and read locklessly by the overview handler; it introduces no new unsynchronized shared state (atomic store/load only).
- Console commands: none required — this slice is backend Go only. No internal/admin/ui source changed; the new `managed_apply_finalization` overview field is additive JSON (omitempty) consumed by a future Console banner; no typecheck/lint/test/build or asset-drift gate was needed; internal/admin/assets/dist was not touched.
- Deviations:
  1. Requested design (§3.9 skeleton `ManagedApplyAdvisory`): implemented verbatim (Healthy/At/ApplyID/Detail with the shown json tags). Additional repository-native decisions: (a) the advisory is surfaced through a dedicated `Deps.ManagedApplyFinalizationHealth` hook + a new `RuntimeOverview.ManagedApplyFinalization` field rather than reusing `AdminHealthStatus`, because the parent commit routed the finalization error through `deps.AdminHealth` (which gates `/readyz`) — §3.9 forbids readiness coupling, so this slice DECOUPLED it. Rationale follows the decision hierarchy (make degradation explicit without gating readiness) and §3.9 ("Do not connect it to readiness").
  2. §3.10 test suite: this slice adds the advisory-specific tests (non-readiness surfacing + history-degradation-and-clear). The exactly-once, stage-terminal, callback-panic, and ordering tests required by §3.10 were already added by the accepted WS02 Slices 1–4 (TestManagedApplyFinalizerExactlyOnce, TestStageRestartTerminalFinalizedThroughLedger, the callback-panic and ordering tests in config_apply_test.go) and were preserved, not re-implemented, per the slice-entry condition "Do not reimplement or redesign accepted earlier slices."
- Self-review findings: single-purpose diff — one new advisory type + one dep + one overview field + one handler read, the serve.go readiness decoupling and advisory publish, and the finalizer's publishAdvisory/advisoryDetail. No unwired code: `ManagedApplyFinalizationHealth` is both written (serve.go panic hook + finalizer setAdvisory) and read (api.go overview). No error swallowing — history/complete/claim failures now additionally drive the advisory. No secrets serialized — the advisory carries only Healthy/At/ApplyID/Detail where Detail is a bounded finalization/history error message; no raw TOML/secret/actor/token. No authentication/authorization/history/transaction truthfulness weakened; readiness is now MORE truthful (a committed-but-history-degraded apply no longer fails /readyz). No generated Console bundle or closure report touched. Tests use only production wiring, no sleeps, and no manually seeded ledger state; the history failure is injected through the real filesystem writer.
- Independent review status: pending
- Reviewer blockers: none
- Blocker-fix SHA: n/a
- Accepted SHA: pending coordinator acceptance
- Next execution file: the next coordinator-assigned slice (WS02 finalization workstream — advisory health + tests complete)

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

### Workstream acceptance record - WS02_FINALIZATION

- Workstream: WS02_FINALIZATION - Unified exactly-once terminal finalization
- Branch: audit/ws02-finalization
- Base SHA: bb666a655a14d25b4a3eaeaee5b5d6234b26224e
- Final reviewed SHA: 10c432fafd86de5d33891beada59886e72baccc8
- Independent reviewer verdict: APPROVE
- Reviewer context/identifier: Independent reviewer (fresh Act/Agent task, file edits DISABLED, read + safe-terminal only). Verdict: APPROVE. Range bb666a655a14d25b4a3eaeaee5b5d6234b26224e..10c432fafd86de5d33891beada59886e72baccc8 = exactly 5 commits (S01-S05), base is ancestor of head, 16 files, +1702/-188; branch audit/ws02-finalization; HEAD == Head SHA verified BEFORE and AFTER review, working tree clean throughout (read-only integrity confirmed). Production wiring verified by file:line, not trusted: coordinator.OnManagedApplyComplete = managedApplyFinalizer.Finalize (serve.go:766); all three §3.8 terminal paths route through the single completeManagedApply helper (hot config_apply.go:1084, enqueue-failure :1023, stage-success :836); claim-before-history ordering in managed_apply_finalizer.go (ClaimFinalization:97 -> RecordManagedHistory:126 -> Complete:168); registry.Complete error surfaced five ways and retried once (not ignored, defect 4); callback panic recovered into FinalizationError + OnManagedApplyFinalizationError (committed apply never rolled back, defect); stage-success persistence truth Persisted/FinalDiskVersion/FinalServingVersion (defect 8); §3.9 advisory surfaced only via ManagedApplyFinalizationHealth/managed_apply_finalization overview field and DECOUPLED from /readyz (serve.go:419-425, §3.11). Security: ConfigApplyResult.FinalizationError is json:"-"; PreviousRaw forwarded only to the trusted in-process history writer, never logged/retained; advisory carries only bounded Healthy/At/ApplyID/Detail. Metric cardinality enforced by TestMetricLabelPolicy (finalization-errors counter unlabeled; history counter bounded to {operation,result}). Required blocker fixes: NONE. Regressions: NONE. Recommended accepted SHA: 10c432fafd86de5d33891beada59886e72baccc8.
- Focused test evidence: go build ./... -> OK. go vet ./internal/app/ ./internal/admin/ ./internal/observability/ -> OK. go test ./internal/app/ ./internal/admin/ ./internal/observability/ -run "ManagedApply|Finaliz|StageRestartTerminal|Advisory|Readyz|Cardinality|MetricLabel|History" -count=1 -> all ok. go test ./internal/app/ ./internal/admin/ ./internal/observability/ -count=1 (full packages) -> all ok. Test-evidence quality HIGH: TestManagedApplyFinalizerExactlyOnce, TestManagedApplyFinalizationAdvisoryTracksHistoryDegradationAndClears, and TestStageRestartTerminalFinalizedThroughLedger drive the REAL managedApplyFinalizer.Finalize + real admin.Server history writer + real ManagedApplyRegistry. Exactly-once proven by counting raw history-snapshot files (=1 after a duplicate callback). History failure injected through the REAL filesystem (poisoned history dir -> real os.MkdirAll failure), not a mock. Advisory non-readiness test drives the real s.routes() stack (no HTTP/authorization bypass). Concurrency ordering uses channel synchronization, not sleeps-for-assertions. No manually seeded ledger state; no existing test weakened.
- Race evidence: UNVERIFIED locally. go test -race is genuinely unavailable in this environment: CGO_ENABLED=0 and no C toolchain (go test -race fails with "-race requires cgo"; gcc returns exit 255). Deferral is HONEST (a real environment limitation, not a masked failure). Residual risk is bounded: the finalization advisory is a single process-lifetime atomic.Pointer[admin.ManagedApplyAdvisory] published under the finalizer's already-per-ID-serialized terminal path (ledger claim) and read locklessly by the overview handler (atomic store/load only); no new unsynchronized shared state introduced. Deferred to the WS07/91 exact-SHA CI race matrix.
- Console evidence: none required - WS02 is backend Go only; no internal/admin/ui source changed and internal/admin/assets/dist not touched. The new managed_apply_finalization overview field is additive JSON (omitempty) consumed by a future Console banner; the prior green Console suite baseline remains intact.
- Commands unavailable and deferred to CI: (1) go test -race across internal/app, internal/admin, internal/observability at the exact accepted SHA 10c432fafd86de5d33891beada59886e72baccc8 (WS07/91 CI race matrix); (2) full GitHub Actions certification / multi-OS (macOS + Linux) CI matrix at the exact accepted SHA (final CI-certification task, 91).
- Non-blocking follow-ups: NONE (no required blocker fixes; no regressions).
- Next workstream branch: audit/ws03-absolute-deadline
- Next expected parent SHA: 10c432fafd86de5d33891beada59886e72baccc8

---

## Cline to GitHub Copilot handoff

- Recorded at: 2026-07-30
- Handoff agent: GitHub Copilot (Claude Opus 4.8), Jul Audit Implementer mode; materializes the Cline-to-Copilot bridge only (no WS03 implementation).
- Tool transition: WS01 and WS02 were implemented and independently accepted under Cline; execution continues under GitHub Copilot from WS03 onward.

### Remote historical baseline

- Remote: `https://github.com/victornife/jul.git`
- Baseline branch `latest` / `origin/latest`: `50052f9d5377488a6160935008e9e028fd08eca2` (verified after `git fetch --prune origin`; no drift). It is an ancestor of the current HEAD.

### Verified cumulative workstream SHAs

- WS01_MANAGED_LEDGER
  - Base SHA: `e707c41587a335cc92b643068b1c46053eff2621`
  - Reviewed head SHA: `a1c7a3e11195477601b30a067e8a2ed0cf509b91`
  - Cumulative head SHA (branch `audit/ws01-managed-ledger`, incl. acceptance-record commit): `bb666a655a14d25b4a3eaeaee5b5d6234b26224e`
- WS02_FINALIZATION
  - Base SHA: `bb666a655a14d25b4a3eaeaee5b5d6234b26224e` (= WS01 cumulative head)
  - Reviewed head SHA: `10c432fafd86de5d33891beada59886e72baccc8` (range `bb666a65..10c432fa` = exactly 5 commits, S01–S05)
  - Cumulative head SHA (branch `audit/ws02-finalization`, incl. acceptance-record commit): `d343694efe660a3b5e86be94c6f9fdd34dcebd26`
- Lineage verified linear via `git merge-base --is-ancestor` (all exit 0): `50052f9d → e707c415 → a1c7a3e1 → bb666a65 → 10c432fa → d343694e`. WS01 head is an ancestor of WS02 head; HEAD contains exactly the accepted WS02 work plus its journal-only acceptance-record commit (`d343694e`), with no unknown commits (12 commits `baseline..HEAD`, all identified).

### Review evidence paths

- `docs/audit/AI-IMPLEMENTATION-JOURNAL.md` → "Workstream acceptance record - WS01_MANAGED_LEDGER" (independent verdict APPROVE, 0 blockers).
- `docs/audit/AI-IMPLEMENTATION-JOURNAL.md` → "Workstream acceptance record - WS02_FINALIZATION" (independent verdict APPROVE, 0 blockers).
- `.github/copilot-audit-state/BRIDGE-AUDIT.md` (bridge verdict PASS; excluded from Git — local operational evidence).

### Environment limitations

- `go test -race` UNVERIFIED locally: `CGO_ENABLED=0` and no C toolchain; `-race` fails with "-race requires cgo". Deferred to the CI race matrix at the exact accepted SHA.
- Exact-SHA CI certification (multi-OS GitHub Actions) not yet run; a program-level closure gate, not a bridge blocker.

### Next workstream

- WS03_ABSOLUTE_DEADLINE on branch `audit/ws03-absolute-deadline`, created from the accepted cumulative WS02 head `d343694efe660a3b5e86be94c6f9fdd34dcebd26` (never from `origin/latest`). This handoff commit is WS03's base. Next prompt: `/jul-audit-ws03-s01`.

---

## WS03_ABSOLUTE_DEADLINE / Slice 01 — handler timing and context-aware candidate preparation

- Parent SHA: `4e9f8077b613cb1f803afd8a1b21dcd9b828ccf6`
- Resulting SHA: recorded in the excluded continuity state after commit (not embeddable in the committed entry).
- Branch: `audit/ws03-absolute-deadline`
- Agent role/context: GitHub Copilot (Claude Opus 4.8), Jul Audit Implementer mode; single-slice implementation.
- Files changed:
  - `internal/admin/server.go`: added `ApplyRequestContext.RequestContext context.Context` (`json:"-"`); replaced `prepareMutationCandidate` with `prepareMutationCandidateContext` (bounds secret resolution under an operation context); added `(*Server).bindManagedApplyDeadline` (derives the single absolute deadline from the serving reload_timeout at admission) and `managedApplyPrePersistenceContext` (bounded/cancel-only pre-persistence context); wired both legacy handlers (`handleConfigRaw`, `handleConfigSettings`).
  - `internal/admin/api.go`: `applyRequestContext` now binds `StartedAt` and `RequestContext` at admission; `handleConfigApply` binds the deadline and resolves the candidate under the bounded context.
  - `internal/admin/patch_http.go`: `handleConfigPatchApply` binds the deadline and resolves under the bounded context.
  - `internal/admin/api_history.go`: `rollbackToSnapshot` binds the deadline and resolves under the bounded context.
  - `internal/app/config_apply.go`: `ApplyRaw` clears `ctx.RequestContext` alongside `Baseline`/`Candidate` so the async finalizer copy never retains the request-scoped context.
  - `internal/admin/managed_apply_deadline_test.go`: new focused HTTP-path tests.
- Production path verified: HTTP admission (`applyRequestContext`) → `bindManagedApplyDeadline` (serving reload_timeout, R15-01) → `managedApplyPrePersistenceContext` → `prepareMutationCandidateContext` → `config.NewCandidateContext` → coordinator `ApplyConfigRaw`/`ApplyConfig` (reqCtx carries StartedAt/Deadline/RequestContext/Candidate). Verified for all five managed mutation entry points: config apply, legacy raw, settings, structured-patch apply, and rollback.
- Behavior implemented: one absolute deadline is derived once at admission from the currently serving reload_timeout and carried on the request context; handler-side candidate secret resolution is bounded by that deadline and by client cancellation; the request context is never serialized and is cleared before any async audit/ledger copy.
- Tests added: `TestManagedApplyBindsAbsoluteDeadlineFromServingReloadTimeout` (HTTP integration — proves Deadline = StartedAt + serving reload_timeout with a divergent candidate reload_timeout, RequestContext carried, candidate resolved); `TestManagedApplyDeadlineOmittedWithoutServingTimeout` (HTTP integration — proves StartedAt bound but no deadline when serving reload_timeout is not positive).
- Commands run: `go test -count=1 ./internal/admin/... ./internal/app/... ./internal/config/...` (all ok); `go vet ./internal/admin/... ./internal/app/...` (ok); `gofmt -l` (clean); `git diff --check` (clean).
- Commands unavailable: `go test -race` — `CGO_ENABLED=0`, no C toolchain; deferred to the exact-SHA CI race matrix.
- Deviations: none — helper names, signatures, and wiring follow the runbook skeleton; `config.NewCandidateContext` already existed and was reused.
- Self-review findings: all three new helpers are wired at every call site (no unwired helper); no error swallowing (candidate errors propagate as before); no duplicate persistence-side truth (coordinator deadline derivation unchanged, Slice 02 scope); no test weakened; `RequestContext` is `json:"-"` and cleared in the async copy (no secret/context exposure); struct comment updated.
- Independent review status: pending.
- Reviewer blockers: none recorded.
- Blocker-fix SHA: n/a.
- Accepted SHA: pending independent review and exact-SHA CI.
- Next execution file: `/jul-audit-ws03-s02`.

---

## WS03_ABSOLUTE_DEADLINE / Slice 02 — coordinator deadline propagation and gate checks

- Parent SHA: `e2db3a037fb31eeeb796d45bc0766d2b3d09652c`
- Resulting SHA: recorded in the excluded continuity state after commit (not embeddable in the committed entry).
- Branch: `audit/ws03-absolute-deadline`
- Agent role/context: GitHub Copilot (Claude Opus 4.8), Jul Audit Implementer mode; single-slice implementation.
- Files changed:
  - `internal/app/config_apply.go`:
    - `preflightContext` now takes `admin.ApplyRequestContext` and derives the bounded pre-persistence context from the ONE absolute deadline bound at admission — `context.WithDeadline(base, reqCtx.Deadline)` when a deadline is bound, else the serving `reload_timeout` fallback, else cancel-only. The base is the request context (client cancellation) with `c.BaseCtx`/`context.Background()` fallbacks (§4.7).
    - `ApplyRaw` no longer clears `ctx.RequestContext` early; it now derives `pctx` first, then clears `ctx.RequestContext = nil` immediately after so the request-scoped context is never retained past derivation or handed to terminal callbacks. Previous-config resolution moved below the derivation and now uses `config.NewCandidateContext(pctx, raw)` so a stalled previous-config secret provider is bounded (§4.7).
    - `applyStageRestart` staged-update base resolution switched from `config.NewCandidate` to `config.NewCandidateContext(pctx, raw)` for the same bound.
    - `applyCandidate` no longer restarts the transaction clock or grants a fresh full timeout: `transactionStarted` = `reqCtx.StartedAt` (fallback `time.Now().UTC()`); `deadline` = `reqCtx.Deadline`, and only when unbound falls back to `transactionStarted.Add(servingReloadTimeout)`. The original `deadline` is passed to `server.ReloadRequest` (§4.8). The synchronous wait uses an explicit `time.NewTimer(time.Until(deadline)+waitMargin)`; a zero deadline yields a nil wait channel (block until the finalizer delivers) instead of the previous accidental one-second collapse.
    - `runPreflight` now checks `pctx.Err()` after the gate returns even when the gate error is nil, attributing a deadline breach to the observed phase and aborting before persistence (§4.9).
  - `internal/app/config_apply_deadline_test.go`: new focused coordinator tests.
- Production path verified: HTTP admission binds `StartedAt`/`Deadline`/`RequestContext` (Slice 01) → coordinator `ApplyRaw` → `preflightContext(reqCtx, cfg)` derives one bounded `pctx` from that deadline → previous-config resolution and every preflight gate run under `pctx` → `runPreflight` aborts before persistence on any post-gate expiry → `applyCandidate` reuses the same `reqCtx.Deadline` for `server.ReloadRequest.Deadline` and the synchronous wait. The single admission deadline now governs preflight, persistence, and reload with no reset; a candidate's own `reload_timeout` never affects the transaction that submits it (R15-01).
- Behavior implemented: exactly one absolute deadline flows from admission through preflight and reload. Client cancellation propagates via the request context into pre-persistence work. A deadline breach detected after any gate (even one that returned success) aborts cleanly with disk untouched and no reload enqueued. An unbounded transaction (no serving timeout, no bound deadline) waits for the finalizer rather than timing out after one second.
- Tests added: `TestCoordinatorReloadReusesAdmissionDeadline` (package integration — asserts `ReloadRequest.Deadline` equals the admission deadline verbatim while the serving `reload_timeout` would have produced a different value, proving no reset); `TestCoordinatorBoundDeadlineGovernsPreflightOverServingTimeout` (package integration — a 40 ms bound deadline aborts a wedged handler build within the phase while the serving timeout is 10 s, proving `preflightContext` derives from `reqCtx.Deadline`); `TestCoordinatorAbortsBeforePersistWhenDeadlineExpiredDuringGates` (package integration/failure-injection — an already-expired deadline on the prepared-candidate path, where every gate returns nil, still aborts before persistence with disk untouched and no reload enqueued, proving the §4.9 post-gate expiry check; this test fails under the previous `err != nil && pctx.Err() != nil` guard).
- Commands run: `go test -count=1 ./internal/app/... ./internal/admin/... ./internal/config/...` (EXIT=0: app ok 306.198s, admin ok, config ok); `go test -count=1 -v -run TestCoordinator...` for the three new + two adjacent timeout tests (all PASS); `go vet ./internal/app/... ./internal/admin/...` (ok); `git diff --check` (clean).
- Commands unavailable: `go test -race` — `CGO_ENABLED=0`, no C toolchain (`-race requires cgo`); deferred to the exact-SHA CI race matrix. `gofmt -l` reports repo-wide entries because the Windows/OneDrive working tree is CRLF while gofmt normalizes to LF (untouched files are listed too); `gofmt -d` on the two changed files shows only line-ending differences, no formatting change, and `git diff --check` is clean.
- Deviations: (1) also bound `applyStageRestart`'s staged-update base resolution with `config.NewCandidateContext(pctx, raw)` — the runbook §4.7 names only the `ApplyRaw` previous-config path, but this is the same coordinator "previous-config resolution" defect (§4.2 defect 2) and shares `pctx`, so binding it preserves the single-deadline invariant on the stage path. (2) `reqCtx.RequestContext` is cleared right after `preflightContext` derivation (per §4.7 "before it is passed to terminal callbacks or retained") rather than at the top of `ApplyRaw`; Slice 01 cleared it early, which would have made the request context unavailable to the derivation.
- Self-review findings: no unwired helper — `preflightContext` is the sole derivation site and both `applyStageRestart` and `applyCandidate` consume `pctx`/`reqCtx.Deadline`; no error swallowing — validation errors still propagate, and a bounded-context expiry is surfaced as a phase-specific `TimedOutPhase` (504) rather than hidden; no duplicate truth — the reset-computing block in `applyCandidate` was removed so the deadline has a single origin (admission); a zero deadline is handled explicitly (nil wait channel) rather than collapsing to one second; no secret/context exposure — `RequestContext` remains `json:"-"` and is cleared post-derivation, never serialized or logged; no test weakened; comments updated to describe the single-deadline flow.
- Independent review status: pending.
- Reviewer blockers: none recorded.
- Blocker-fix SHA: n/a.
- Accepted SHA: pending independent review and exact-SHA CI.
- Next execution file: `/jul-audit-ws03-s03`.

---

## WS03_ABSOLUTE_DEADLINE / Slice 03 — post-persistence obligations, Console timeout contract, and timeout audit

- Parent SHA: `b1b7b4fe52a26563bb768843488549db3f2c7308`
- Resulting SHA: recorded in the excluded continuity state after commit (not embeddable in the committed entry).
- Branch: `audit/ws03-absolute-deadline`
- Agent role/context: GitHub Copilot (Claude Opus 4.8), Jul Audit Implementer mode; single-slice implementation.
- Files changed:
  - `internal/admin/config_apply.go`: added `(*Server).recordTimeoutAudit` — the single per-handler helper that records a `config.<operation>` failure audit naming the timed-out preflight phase and, when the coordinator allocated one before the timeout, the apply ID. The detail is transaction metadata only (phase + apply ID); it never carries config bytes, secrets, or token material.
  - `internal/admin/api.go`: `handleConfigApply` records the timeout audit (via `reqCtx.Operation`) after the validation branch; the shared status mapping writes the 504.
  - `internal/admin/patch_http.go`: `handleConfigPatchApply` records the timeout audit after the validation branch.
  - `internal/admin/server.go`: `handleConfigRaw` and `handleConfigSettings` record the timeout audit in the non-OK result branch before writing the status.
  - `internal/admin/api_history.go`: `handleConfigRollback` records the timeout audit in the `!result.OK` branch (via `ApplyOperationRollback`, since `rollbackToSnapshot` builds its own `reqCtx`).
  - `internal/admin/config_apply_timeout_audit_test.go` (new): HTTP-integration table test across all five managed write routes.
  - `internal/admin/ui/src/api/client.ts`: added top-level `timed_out_phase` to `ConfigApplyResultBaseSchema`; added the `"timeout"` `ConfigApplyErrorKind`; `classifyApplyFailure` throws `ConfigApplyOutcomeError(..., "timeout", ...)` when a top-level `timed_out_phase` is present.
  - `internal/admin/ui/src/lib/applyOutcome.ts`: added the `"preflight-timeout"` `ApplyOutcomeKind`, the `preflightTimedOut` input, and the derive branch that renders the phase, "Nothing was persisted", and the raise-`global.reload_timeout` guidance without implying the candidate serves.
  - `internal/admin/ui/src/features/config/ConfigPanel.tsx`: routes `errorKind === "timeout"` to the preflight-timeout outcome, passing the top-level `timed_out_phase`.
  - `internal/admin/ui/src/test/client-write.test.ts`, `internal/admin/ui/src/test/apply-outcome.test.tsx`: new focused tests.
- Production path verified:
  - Step 7 (post-persistence obligations): verified — no code change required. `ApplyRaw` nils `ctx.RequestContext` (config_apply.go) immediately after `preflightContext` derivation (Slice 02), so `applyCandidate` never holds the browser-request context. The finalizer goroutine and synchronous wait use only `resultCh`, `c.mu`, `c.BaseCtx`, and the single admission-deadline timer, so a browser disconnect cannot abandon reload/restoration and no second deadline is created.
  - Steps 8/9: HTTP 504 with top-level `timed_out_phase` → `classifyApplyFailure` → `ConfigApplyOutcomeError("timeout")` → `ConfigPanel` `errorKind==="timeout"` branch → `deriveApplyOutcome({preflightTimedOut})` → `preflight-timeout` banner. Backend: coordinator `timedOutResult` → each handler → `recordTimeoutAudit` → `recordAudit` ring/sink, with the 504 from `configApplyResultStatus`. Proven for all five managed routes (apply, legacy raw, settings, structured-patch apply, rollback).
- Behavior implemented: every managed write route now records a phase-named failure audit when a pre-persistence timeout occurs (apply ID included only when one exists); the Console classifies the 504 distinctly and renders a truthful "configuration not changed — preflight timed out" outcome that never claims the candidate is serving.
- Tests added: `TestManagedWriteRoutesRecordTimeoutAudit` (HTTP integration — five routes: 504 + `config.<op>` failure audit naming the phase, with apply_id present or absent per coordinator allocation); `client-write.test.ts` "classifies a pre-persistence preflight timeout 504 as kind timeout" (Console client contract); `apply-outcome.test.tsx` two `preflight-timeout` cases (phase rendered; phase-absent; message never claims serving).
- Commands run: `go test -count=1 ./internal/admin/... ./internal/config/...` (ok); `go test -count=1 ./internal/app/...` (ok jul/internal/app 306.805s); `pnpm --dir internal/admin/ui run typecheck` (clean); `pnpm --dir internal/admin/ui run lint` (clean); `pnpm --dir internal/admin/ui run test` (37 files, 454 tests pass); `git diff --check` (clean, only benign CRLF notices); `gofmt` on EOL-normalized copies of all changed Go files (clean).
- Commands unavailable: `go test -race` — `CGO_ENABLED=0`, no C toolchain (`-race requires cgo`); deferred to the exact-SHA CI race matrix. `gofmt -l` reports repo-wide entries because the Windows/OneDrive working tree is CRLF while gofmt normalizes to LF; EOL-normalized `gofmt -d` on the changed files is clean and `git diff --check` is clean.
- Deviations: (1) Step 9 enumerates "raw, patch, settings, and rollback" handlers but omits the primary `handleConfigApply`; the timeout audit was added there too so timeout-audit evidence is complete per defect 9 — the primary managed apply path must not be the single silent gap. (2) Introduced the shared `recordTimeoutAudit` helper (used at all five call sites) instead of inlining the runbook snippet five times — one formatting/`apply_id` policy, no duplicate truth. (3) Step 7 required no code change (already satisfied by Slices 01/02); it was verified against the production path and documented rather than re-implemented, to avoid introducing a redundant/second deadline.
- Self-review findings: `recordTimeoutAudit` is wired at all five call sites (no unwired helper) and `preflightTimedOut`/`preflight-timeout` are consumed end-to-end; no error swallowing (a timeout still returns the structured 504); no duplicate truth (top-level `timed_out_phase` is the sole pre-persistence timeout signal; `reload.timed_out_phase` remains the distinct post-persistence signal); no secret exposure (audit detail is phase + apply ID only; the `timed_out_phase` schema field is non-sensitive metadata); no weakened or deleted assertions; no generated assets touched (`internal/admin/assets/dist` untouched); comments explain the pre- vs post-persistence distinction.
- Independent review status: pending.
- Reviewer blockers: none recorded.
- Blocker-fix SHA: n/a.
- Accepted SHA: pending independent review and exact-SHA CI.
- Next execution file: `/jul-audit-ws03-s04`.

---

## WS03_ABSOLUTE_DEADLINE / Slice 04 — vertical timeout and cancellation tests

- Parent SHA: `9a61cc03a2f6f6f9eb6aafd14b437b9855078931`
- Resulting SHA: recorded in the excluded continuity state after commit (not embeddable in the committed entry).
- Branch: `audit/ws03-absolute-deadline`
- Agent role/context: GitHub Copilot (Claude Opus 4.8), Jul Audit Implementer mode; single-slice implementation. Test-only slice — no production code changed.
- Files changed:
  - `internal/app/config_apply_single_budget_test.go` (new): three coordinator-level vertical tests proving the single-budget and cancellation invariants that Slices 01–03 implemented. Reuses the existing package test helpers (`validConfigRaw`, `testPreflight`, `servingSnapshot`, `mockStreamPreflighter`, `mustParse`, `PlannedRestartStore`) and the deterministic `waitMargin`/`SubmitReload` seams; no production helper or test helper was added or duplicated.
- Production path verified: HTTP admission binds `StartedAt`/`Deadline`/`RequestContext` (Slice 01) → coordinator `ApplyRaw` → `preflightContext` derives one bounded `pctx` (Slice 02) → preflight gates → `applyCandidate` reuses `reqCtx.Deadline` verbatim for `server.ReloadRequest.Deadline` and the synchronous wait (Slice 02), and the async finalizer owns reload/restoration on `c.BaseCtx` (Slice 03). The new tests drive the real `ConfigApplyCoordinator.ApplyRaw` end to end (not a hand-seeded ledger or a direct helper call), so they exercise that production path rather than restating it.
- Behavior proved:
  - Single absolute budget: preflight and the reload wait share ONE deadline. A slow-but-successful preflight consumes ~150ms of a 200ms serving budget; the reload never delivers within the budget; the transaction must return near the original deadline (~215ms), not preflight+budget (~365ms, the reset defect).
  - Client cancellation before persistence aborts pre-persistence work (phase attributed), writes nothing, and enqueues no reload — proving cancellation propagates through `reqCtx.RequestContext` into `pctx`.
  - Client cancellation after persistence cannot abandon the reload: because `applyCandidate` runs the finalizer on `c.BaseCtx` (the request context is nil'd after `preflightContext` derivation), the transaction still terminalizes to an `applied_live` result and keeps the persisted write.
- Tests added: `TestManagedApplyEnforcesSingleReloadTimeoutBudget` (package integration — single end-to-end reload-timeout budget; the centerpiece of the commit subject); `TestManagedApplyClientCancelBeforePersistLeavesDiskUntouched` (package integration/failure-injection — barrier-driven client cancel during preflight; disk untouched, no reload); `TestManagedApplyClientCancelAfterPersistStillTerminalizes` (package integration — barrier-driven client cancel after the reload is enqueued; reload still terminalizes on the process context).
- Commands run: `go test -count=1 -v -run <three tests> ./internal/app/` (all PASS); `go test -count=3 -run <three tests> ./internal/app/` (stable, no flakiness); `go test -count=1 ./internal/app/` (ok jul/internal/app 306.105s); `go test -count=1 ./internal/config/... ./internal/admin/...` (ok); `go vet ./internal/app/` (clean); `gofmt -d` on an EOL-normalized copy of the new file (clean); `git diff --check` (clean).
- Commands unavailable: `go test -race` — `CGO_ENABLED=0`, no C toolchain (`-race requires cgo`); deferred to the exact-SHA CI race matrix. `gofmt -l` reports repo-wide entries because the Windows/OneDrive working tree is CRLF while gofmt normalizes to LF; EOL-normalized `gofmt -d` on the new file is clean and `git diff --check` is clean. On Windows/OneDrive the test binary cleanup emits a benign `unlinkat ... app.test.exe: Access is denied` after a passing run (exit 1 from cleanup only); every `ok` package line above is a genuine pass.
- Deviations from the runbook §4.13 test list: (1) The single-budget serving timeout is scaled from the runbook's 150ms to 200ms with ~150ms preflight so the one-budget return (deadline-anchored, ~215ms) and the reset return (~365ms) are far enough apart to assert without wall-clock flakiness; the return is anchored to the absolute admission deadline, so the low side is stable and only scheduling jitter can push it up, hence the generous 300ms upper bound. The single-budget test uses `time.After(preflightSpend)` to *simulate consumed budget* (deliberate slow work), which is distinct from the prohibited "sleep to approximate a concurrency interleaving"; the actual goroutine interleavings in the cancellation tests are barrier-driven (`entered`/`persisted`/`proceed`/`stopReload` channels). (2) The §4.13 handler-level HTTP 504/`timed_out_phase` and Console phase-rendering evidence was already delivered by Slice 03 (`TestManagedWriteRoutesRecordTimeoutAudit` across five routes; the `preflight-timeout` Console outcome tests) and by Slice 01 (`managed_apply_deadline_test.go`); this slice adds the still-missing single-budget and cancellation vertical evidence rather than re-adding covered cases. The per-gate barrier preflight matrix (WASM/stream/listener/startup-resource/admin-policy) was left out of scope: the handler-build gate is already covered by `blockingPreflight` and per-gate barriers would balloon this test-only slice without proving a distinct single-budget invariant.
- Self-review findings: no production code changed, so no unwired helper, no error swallowing, and no duplicate truth introduced; the duplicate `mustParse` I initially drafted was removed in favor of the existing package helper; no existing test was weakened or deleted; no secrets/config bytes are logged (assertions read only `OK`/`Persisted`/`Reload`/`TimedOutPhase`); no generated assets touched; comments describe the one-budget-vs-reset discriminator and the barrier design.
- Independent review status: pending.
- Reviewer blockers: none recorded.
- Blocker-fix SHA: n/a.
- Accepted SHA: pending independent review and exact-SHA CI.
- Next execution file: `/jul-audit-review-ws03`.
</content>
