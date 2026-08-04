# Changelog

All notable changes to Jul.IA are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Dates are in ISO 8601 format (`YYYY-MM-DD`).

## [Unreleased]

> **Release state:** these changes are **merged to `main`** but **not yet tagged
> or released**. Per the delivery-state vocabulary in [docs/status.md](docs/status.md),
> "merged" is not "released" or "soaked"; the Phase 4 egress hardening becomes
> **GA** only after its first tagged release passes the post-GA soak gate.

### Changed
- **Operating model and roadmap v2.0 (2026-08-04):** added ADR 0013/0014, a bounded Core Gateway Completeness contract, portfolio lanes, appropriate Console/API/CLI surfaces, value-ranked runtime dynamics, and bounded experiment governance. AI/fleet/cloud remain optional experiments or horizons; this documentation change adds no runtime capability.
- **Current product-truth reconciliation (2026-08-04):** aligned README, security, status, configuration, observability, deployment, cache, reload, WAF and known-limitations documentation with the merged runtime and the remaining audited correction work.
- **Post-audit remediation (2026-07-31):** activated the canonical Git hooks
  (`make hooks`) so the `gofmt` pre-commit gate runs locally — a stale
  hand-installed `.git/hooks/pre-commit` with `core.hooksPath` unset had been
  masking it; reformatted `cmd/jul/capabilities.go`. `jul capabilities` now reports
  every optional build tag (not just `waf`/`stream_proxy`/`wasm_plugins`). The
  Console Security panel now labels interactive RBAC token management as
  **Preview** (planned; not yet available) so the surface is honest about what
  ships today.

### Security
- **HTTP/3 mTLS parity:** QUIC listeners now clone the complete prepared sibling TCP TLS policy, preserving client-auth mode, CA verification, SAN/CRL checks, result hooks and verified client identity.
- **Strict configuration decoding:** genuinely unknown TOML fields now fail immediately across the canonical parser; the documented singular `server_name` compatibility alias is canonicalized and conflicts with `server_names` are rejected.
- **CodeQL reflected-output hardening:** FastCGI authorizer error strings are HTML-escaped and the deliberate upstream-response forwarding boundary is documented.
- **Dependency advisory remediation (2026-07-31):** bumped `klauspost/compress`
  1.18.6 → 1.18.7 (GO-2026-5841) and forced patched transitive frontend deps via
  pnpm overrides — `brace-expansion` → 5.0.9 (ReDoS) and `postcss` → ≥8.5.18
  (GHSA-r28c-9q8g-f849, build-time only). Two advisories are **accepted with
  rationale**, not fixable today: `golang.org/x/crypto` GO-2026-5932 (no patched
  release published; `govulncheck` confirms it is **not called**) and
  `react-router` GHSA-qwww-vcr4-c8h2 (CSRF in **RSC/server mode**, which this
  client-only SPA does not use; no compatible `react-router-dom` 8.x release
  exists). `govulncheck` reports **0 called** vulnerabilities.

### Added
- **Phase 4 — Egress allow-list completion and integration hardening** (P4-01, P4-02, P4-03, HP-07, #74, #75, #76):
  - `internal/egress` refactored into `policy`/`normalize`/`error`/`http` modules with a
    typed decision contract: `BlockError{Subsystem,Host,IP,Reason}` wrapping the
    `ErrBlocked` sentinel, and bounded `Reason` constants (`host_not_allowed`,
    `ip_not_allowed`, `mixed_dns_answers`, `no_dns_answers`, `invalid_address`).
  - Allow-entry normalization: lowercasing, a single trailing dot, IDNA→ASCII for
    internationalized names, IPv6 bracket/zone handling, CIDR canonicalization,
    de-duplication, and rejection of ambiguous/malformed entries instead of silently
    treating them as hostnames.
  - Immutable subsystem-scoped guards via `Policy.For(subsystem)` (bounded name
    constants), a `Decision` observer seam, and guarded HTTP clients that pin
    `Proxy=nil` (ignoring `HTTP(S)_PROXY`/`NO_PROXY`) plus a `RoundTripper` that
    re-validates the request URL so redirects are re-checked.
  - Consistent coverage across every config-driven auxiliary client: ACME
    directory/order/challenge (`acme.Client.HTTPClient`) and OCSP responder fetches are
    now guarded when `[egress]` is enabled (nil clients preserve the default,
    proxy-aware behavior when it is off); WASM plugin `fetch` is the **intersection** of
    the plugin's `allowed_hosts` + SSRF guard and the global allow-list, with a distinct
    guest return code `-5` for a global-policy denial (additive `jul-abi/v1` change).
  - End-to-end injection wired at the composition root (P4-02, #75): `serve.go` builds
    the policy before every process-lifetime outbound client and passes subsystem-scoped
    guards/clients into the runtime (ACME/OCSP), handler factory (JWKS/forward-auth),
    upstream registry (Consul/Kubernetes), and plugin manager, keeping the policy
    startup-bound and fingerprinted. Auth and discovery constructors accept a generic
    `DialContext`/client so `internal/auth` and `internal/upstream` never import
    `internal/egress`.
  - Observability: `jul_egress_decisions_total{subsystem,result,reason}` and
    `jul_egress_dns_answers_total{subsystem,result}` metrics (never labelled by
    destination), and a Console **Security** panel row showing the enabled state,
    allow-rule count, and a recent-blocked breakdown by subsystem/reason.
  - Egress **block logs** (P4-03, #76): on a block the server emits a rate-limited,
    secret-safe structured log line carrying `subsystem`, normalized `host`, optional
    `resolved_ip`, and bounded `reason` — never a URL, query string, or credential.
    Identity/discovery/PKI blocks (`auth`, `discovery`, `acme`, `ocsp`) log at warning;
    guest-triggered `plugin` fetch denials log at info; identical events are collapsed
    within a short window so a retry loop cannot flood the log. The Console Security
    panel gained an **egress documentation link**.
  - Phase 4 test matrix (P4-03, #76): added negative/integration coverage —
    redirect-to-allowed-host, TLS SNI/Host preserved when dialing a resolved IP,
    connection-reuse re-checks a new host, timeout/cancellation propagation,
    resolver-seam race and no-connection/goroutine-leak on repeated blocks, Consul and
    Kubernetes allow+block, ACME directory allow+block, secret-referenced
    `[egress].allow` resolution, and egress restart-required/pending-restart lifecycle
    classification.
  - Docs: rewritten [egress.md](docs/egress.md) inventory (with block-log behavior and
    an explicit data-plane-reverse-proxy out-of-scope note), egress block logs in
    [observability.md](docs/observability.md), ACME/OCSP prerequisites in
    [tls-acme.md](docs/tls-acme.md), the plugin-intersection and `-5` code in
    [plugins.md](docs/plugins.md), and an egress troubleshooting section. HP-07 marked
    Delivered.
- **Phase 3 — Current-identity endpoint, Console permission gating, and secret-safe RBAC diff/projections** (P3-03, #72):
  - `GET /api/admin/me` (new, `internal/admin/api_identity.go`): returns the caller's
    own server-derived identity — principal, role, public token ID, resolved concrete
    permission set (wildcards expanded), and `legacy` flag. It is authenticated-only
    (any valid credential, no specific permission) via a new `RouteSpec.Authenticated`
    mode wired through `authWithRBAC`. The identity comes solely from the request
    context; no client-supplied identity is trusted, and no secret is ever returned.
  - Console permission layer: `PermissionProvider` (fetches `/api/admin/me` once),
    `usePermission()` hook, and a `ForbiddenAction` note. The app chrome shows the
    current principal/role, and the apply, history-rollback, plugin-upload,
    cache-purge, and audit-export controls are disabled up front for principals that
    lack the permission, each explaining which permission is required. Gating fails
    open until the identity is known and a 403 no longer triggers the token prompt;
    the cached identity is dropped on 401. The server remains authoritative.
  - Structured RBAC diff (`internal/admin/diff_global.go` `diffGlobalRBAC`): the config
    diff now reports `[admin.rbac]` changes at the role/principal/token-ID level —
    enabled/disabled, roles added/removed/permission-count changed, principals
    added/removed/role-changed/disabled/expiry-changed, and credential rotation as a
    fact — and warns on enabling RBAC, leaving no admin-capable principal, or retaining
    a legacy shared token after RBAC is enabled. It never emits a token value or hash.
  - Safe RBAC status projection (`RBACStatusProjection`): `GET /api/security` now
    carries a secret-free `rbac` summary (enabled, principal count, custom-role count,
    legacy-token-active), surfaced on the Security panel.
  - Audit UI: the Audit panel now shows the non-secret **Token ID** column alongside
    the actor, completing the §32 attribution projection (`AuditEvent.token_id`).
  - Tests: `TestIdentityEndpoint*` (admin), `TestDiffRBAC*` / `TestProjectRBAC*`
    (admin, including a secret-leak guard over the serialized diff), and console
    `permission.test.tsx` / Security-panel RBAC-status tests.

### Fixed
- **ACME challenge selection:** HTTP-01 and TLS-ALPN-01 now expose only the configured challenge surface while preserving normal HTTP/TLS routing.
- **Compression transformation contract:** request or response `Cache-Control: no-transform` now bypasses dynamic gzip/Brotli/Zstandard compression.
- **HTTP/3 UDP preflight test:** the success path uses OS-assigned TCP/UDP ephemeral ports instead of reusing a released TCP port on Windows.
- **Post-audit remediation (reload result contract, RBAC lifecycle, context propagation):**
  - `server.ReloadResult` now carries `admin` subsystem status so RBAC policy update
    failures are reported independently of stream-proxy status.
  - `server.ReloadResult.Persisted` is set for every reload that operates on the
    persisted configuration.
  - `server.ReloadResult.PhaseDurations` values are now in milliseconds to match the
    `phase_durations_ms` JSON field name.
  - A reload that times out after `Publish` is reported as `applied_degraded` rather
    than `not_applied`, because Publish is the point of no return.
  - Admin apply uses the currently serving config's `reload_timeout` for the
    transaction; a candidate that changes `reload_timeout` affects the next apply.
  - `ConfigApplyCoordinator` serializes applies with `applyMu` and releases the
    coordinator mutex before waiting for the reload result, eliminating the
    deadlock between the synchronous HTTP path and the async restoration finalizer.
  - Disabling `[admin].rbac.enabled` clears the active policy on the next successful
    hot reload; the server falls back to the configured legacy token.
  - The admin server stores a live copy of `AdminConfig` so `[admin].token` changes
    take effect without restart (the listener address remains startup-bound).
  - Full context propagation through `plugins.Manager.Build`, `auth.New`, `waf.New`,
    `handler.NewGRPCTranscode`, and `transcode.New`; cancelled reloads stop promptly
    during plugin compilation and gRPC reflection.
  - TypeScript `ReloadResultSchema` added and shared between `ApplyResultSchema.reload`
    and `OverviewSchema.last_reload`; includes `phase_durations_ms` and `admin` subsystem.
    `pending_restart_status` schema includes the `external` flag.
  - Tests: `TestReloadResultIncludesAdminSubsystem`,
    `TestReloadPostPublishTimeoutReportsDegraded`, `TestReloadPhaseTimingRecordsDurations`
    (server); `TestCoordinatorApplyRawUsesServingReloadTimeout` (app);
    `TestRBACDisableClearsPolicy`, `TestLiveAdminConfigTokenUpdate` (admin).

### Added
- **Phase 3 — Route catalog and permission-bound authorization** (P3-02, #71):
  - `internal/admin/route_catalog.go` (new): `RouteSpec{Pattern, Methods, Permission, Public, Handler}`
    type; authoritative `Catalog` slice listing every admin route with its permission.
    Public routes (`/healthz`, `/readyz`, `/`); all other routes carry an explicit `rbac.Permission`.
    Permission groups: `status:read` (projections, events), `metrics:read` (/metrics),
    `config:read` (raw/settings/pending-restart/history list-get),
    `config:write` (validate/diff/patch/wizard/transcode),
    `config:apply` (apply/patch-apply/discard), `history:rollback` (rollback),
    `plugins:upload`, `observability:read`, `audit:read`, `audit:export`, `cache:purge`,
    `reload:trigger`, `admin:manage` (pprof).
  - `internal/admin/routes.go`: mux generated from `Catalog` via `requirePermission` or
    public; legacy ad-hoc `s.auth(...)` calls removed; `handleConsoleOrRoot` helper.
  - `internal/admin/rbac.go`: `requirePermission(perm, next)` — full 4-step authn+authz stack
    (parse Bearer → authenticate → store Identity → authorize permission); 401 for
    unauthenticated/expired; 403 structured JSON for forbidden; identity stored in context
    for both RBAC and legacy paths. `writeForbidden` helper with `error/required/principal/role`
    body (does not reveal existence of other principals).
  - `internal/admin/api.go`: object-level `admin:manage` guard in `handleConfigApply`;
    `candidateRequiresAdminManage` detects token/RBAC/audit/plugin-upload changes;
    `rbacIdentityFromRequest` helper; `rbac` added to imports.
  - Guard tests (`route_catalog_test.go`): 12 tests — no non-public route without permission,
    all permissions in catalog, no duplicate patterns, all specs have handlers and methods,
    planned-restart routes carry correct permissions, only approved routes are public,
    requirePermission 401/403 behavior, RBAC allow/deny, pprof requires admin:manage.

- **Phase 3 — RBAC schema, permission catalog, roles, tokens, and policy core** (P3-01, #59):
  - New package `internal/rbac`: `Permission` catalog + `Known`/`Matches` helpers;
    predefined roles `viewer`/`operator`/`admin`/`auditor` with stable permission sets;
    `Identity` struct with context helpers `WithIdentity`/`IdentityFromContext`;
    `Policy` value (immutable after `Build`) with `Authenticate`/`Authorize` + O(1) tokenID
    lookup and constant-time hash comparison; SHA-256 token hashing + 12-hex-char public
    `TokenID` that never exposes secret bytes; `disabled` and `expires_at` honored.
  - `internal/config/schema.go`: `AdminRBACConfig`, `AdminRole`, `AdminPrincipal` types
    added to `AdminConfig.RBAC`; `time.Time` imported for `ExpiresAt`.
  - `internal/config/parser.go`: `default_role = "admin"` defaulted when RBAC is enabled.
  - `internal/config/validate_rbac.go` (new): validates unique principal/role names,
    predefined-role-name protection, permission catalog membership, no duplicate token IDs,
    at least one enabled admin-capable principal, expiry sanity check; `isSecretRef` helper.
  - `internal/config/lint.go`: per-principal literal-token lint warning (SEC-1 parity
    with legacy `admin.token`).
  - `internal/lifecycle/lifecycle.go`: `admin.rbac.enabled` = restart-required;
    `admin.rbac.principals.*`, `admin.rbac.roles.*`, `admin.rbac.default_role` = hot-reloadable.
  - `internal/lifecycle/fingerprint.go`: `admin.rbac.enabled` extractor.
  - `internal/admin/server.go`: `policy atomic.Pointer[rbacPolicy]` field; `auth()` now
    delegates to `authWithRBAC` rather than a hard-coded constant-time comparison.
  - `internal/admin/rbac.go` (new): `rbacPolicy` wrapper; `UpdatePolicy` atomic install;
    `authWithRBAC` middleware — RBAC path stores `Identity` in request context; legacy path
    stores a synthetic `shared/admin` identity; unauthenticated → 401; disabled/expired → 403.
  - `internal/app/serve.go`: `buildRBACPolicy` helper converts `config.AdminConfig` to
    `rbac.Policy`; builds initial policy after `admin.New` when RBAC is enabled; hot-swaps
    policy via `adminSrv.UpdatePolicy` in `srv.OnReloaded` after each successful hot reload;
    `adminSrv` hoisted from inner scope so the reload hook can reach it.
  - `docs/config-lifecycle.yaml`: `[admin].rbac.enabled` in restart_required admin block;
    new hot_reload `rbac` subsystem entry.
  - `docs/reload-semantics.md`: restart-required fields list updated to include
    `admin.rbac.enabled`; hot-swappable RBAC policy note added.
  - Tests: `internal/rbac/*_test.go` — 35 tests covering catalog, predefined-role contract,
    custom-role resolve, policy build/authenticate/authorize, token ID derivation, disabled/
    expired principal, duplicate token rejection, no-admin lockout guard, context round-trip;
    `internal/config/validate_rbac_test.go` — 13 tests covering all validation branches.

### Fixed
- **P2-Remediation Wave 2b: H-02 full context propagation** — context threading through factory and builders:
  - `HandlerFactory` type in `internal/server/server.go` changed to accept `context.Context`
    as first parameter; `server.Run` and `reload_plan.Prepare` pass the reload context.
  - `HandlerFactory.Build` and `HandlerFactory.Prepare` in `internal/app/factory.go` accept
    `context.Context`; `buildHandlers` checks `ctx.Err()` before and after plugin compilation
    (the most expensive pre-Publish step) so a cancelled context is detected promptly.
  - `Preflight.BuildHandlers` field type updated; `Preflight.Apply` now propagates `ctx` to
    `dryRun`; `dryRun` passes it to both `BuildHandlers` and `Stream.PreflightBuild`.
  - `StreamPreflighter` interface methods `PreflightBuild` and `PreflightListeners` both
    receive `context.Context`; implemented in `internal/stream/server.go` and `stub.go`.
  - All test mocks and callers updated.
- **P2-Remediation Wave 2: H-06, H-04, M-02, M-03** — restore bounded apply correctness and API contract:
  - **H-06 (restart-required strips can_stage):** `handleConfigApply` and `handleConfigPatchApply`
    now return the full `ConfigApplyResult` at HTTP 409 on restart-required rejections,
    including `can_stage`, subsystem list, and version context. TypeScript `ConflictBodySchema`
    updated to parse these fields.
  - **H-04 (orphaned pre-Publish restoration):** `applyCandidate` now starts a background
    goroutine that drains the result channel and restores the previous file if the server
    aborts pre-Publish after the coordinator has already returned `saved_not_live`. A
    `sync.Once` ensures exactly one consumer handles restoration.
  - **M-02 (lifecycle diff wrong baseline):** the previous config is now resolved through
    `config.NewCandidate` before passing to `lifecycle.DiffConfig`, ensuring effective
    values are compared on both sides and secret references do not produce false differences.
  - **M-03 (external pending state missing):** the `PendingRestart` dep now returns a
    structured `admin.PendingRestartStatus{Managed:false}` for external (unmanaged)
    disk/runtime divergence, not just the flat subsystem list.
- **P2-Remediation Wave 1: C-01, C-02, H-01, M-05 (L-01)** — critical correctness fixes for the planned-restart and managed-apply layer:
  - **C-01 (staging backup ordering):** `applyStageRestart` now writes the backup
    and prepared marker BEFORE writing the candidate to disk. `StageManaged` signature
    changed to `StageManaged(prevRaw, candidateRaw []byte, marker)` so it receives the
    original pre-stage bytes from the coordinator rather than reading them from disk (where
    the candidate was already written). `BaseCanonicalVersion` now set from the live serving
    config, not the candidate.
  - **C-02 (single store + deferred reconciliation):** a single `PlannedRestartStore` is
    now constructed once and shared by the `ConfigApplyCoordinator` and startup reconciliation.
    Reconciliation moved from `serve.go` (before `srv.Run`) to `server.OnInitialGenerationReady`
    hook (after all startup listeners bind successfully). On any startup failure the hook is
    never called and recovery files are preserved.
  - **H-01 (rollback false-success regression):** `WriteConfigRaw` and `SaveConfig` compatibility
    adapters now inspect `ConfigApplyResult.OK` and convert `RestartRequired`,
    `ValidationErrors`, and `PendingRestart` rejections to typed errors.
    `rollbackToSnapshot` maps `ErrRestartRequired` to HTTP 409 instead of 400.
  - **M-05 (mode validation):** `POST /api/config/apply` and `POST /api/config/patch/apply`
    now return HTTP 400 for any mode value other than `hot` or `stage_restart`.
  - **L-01:** Phase 2 and HP-01 roadmap/hardening-platform status reverted to
    in-progress during remediation.
  - Added `PlannedRestartStore.inconsistent` field exposed via `Status().Inconsistent`
    when `Reconcile` detects an irrecoverable marker/disk mismatch.
  - New regression tests: `TestStageFirstBackupEqualsOriginal`,
    `TestStageDiscardRoundtripRestoresExactBytes`, `TestStageUpdatePreservesOriginalBackup`,
    `TestInconsistentMarkerSetsFlag`.

### Added
- **Phase 2 close: reload metrics, stage_restart tests, docs, and compat cleanup** (P2-05, #70):
  added `jul_reload_total{source,outcome}`, `jul_reload_duration_seconds{source,outcome}`,
  `jul_reload_in_progress`, `jul_config_stage_restart_total{result}`, and
  `jul_config_pending_restart` Prometheus metrics to `internal/observability/metrics.go`;
  added `OnReloadStart` and `OnReloadComplete` hooks to `server.Server` and wired them
  from `serve.go` (no observability import in server package); `ReloadSource.String()`
  added for metric labels. Distinct audit events `config.apply.accepted` and
  `config.stage_restart.created` emitted from `handleConfigApply`. Five new API
  integration tests for `stage_restart`: returns 200 with `pending_restart`, blocks hot
  apply with 409, discard restores bytes, `GET /api/config/pending-restart` reflects
  state, restart-required rejection carries `can_stage`. Seven new vitest tests for
  `staged-for-restart`, `staged-update`, `pending-restart-blocks-hot`, `discard-success`
  outcome kinds. Added `previous_reload` deprecation notice in `api/client.ts`.
  Docs: `docs/configuration.md` apply-modes section; `docs/troubleshooting.md` staged
  restart and inconsistent-marker recovery; `docs/specs/hardening-platform.md` HP-01
  marked delivered; `docs/roadmap/README.md` Phase 2 marked delivered. Phase 2
  Definition of Done fully demonstrated.
- **Correlated apply/pending-restart APIs and Console outcome model** (P2-04, #69):
  added `DiscardPendingRestart` and `PendingRestart` to `admin.Deps`; new
  endpoints `GET /api/config/pending-restart` and `POST /api/config/pending-restart/discard`
  (handlers in `internal/admin/api_pending_restart.go`); extended `RuntimeOverview`
  with `PendingRestartStatus *PendingRestartStatus` (structured) and
  `LastReload *server.ReloadResult`; updated `handleRuntimeOverview` to populate both;
  wired `DiscardPendingRestart`/`PendingRestart` deps via `coordinator.DiscardPlannedRestart()`
  and `coordinator.PlannedRestartStatus()` in `internal/app/serve.go`; added public
  `PlannedRestartStatus()` method on `ConfigApplyCoordinator`.
  TypeScript: extended `OverviewSchema` with `pending_restart_status` and `last_reload`
  fields; extended `ApplyResultSchema` with `mode`, `reload`, and `pending_restart`
  fields; added `ApplyMode` type; updated `applyConfig` to accept `mode` parameter;
  added `PendingRestartStatusSchema`, `fetchPendingRestart()`, and
  `discardPendingRestart()` to `api/client.ts`; extended `ApplyOutcomeKind` in
  `lib/applyOutcome.ts` with `staged-for-restart`, `staged-update`,
  `pending-restart-blocks-hot`, and `discard-success`; updated `deriveApplyOutcome`
  to map all new server states.
- **Planned-restart staging, discard, reconciliation, and startup preflights** (P2-03, #68):
  introduced `PlannedRestartMarker` and file-backed `PlannedRestartStore`
  (`internal/app/planned_restart.go`) using `<config>.pending-restart.json`
  and `<config>.pending-restart.bak` sidecar files (both `0600`, atomic writes).
  Crash-consistent staging order: backup → prepared marker → write config →
  staged marker. Discard (`DiscardSafe`) verifies marker consistency, disk
  digest, and live serving version before atomically restoring the backup.
  Startup `Reconcile` handles all four cases: prepared/staged × disk-equals-base
  vs disk-equals-staged.
  Added `PreflightMode` (`PreflightHot` / `PreflightStageRestart`) and
  `PreflightResult` (Candidate + `lifecycle.ChangeSet`) to `Preflight.Apply`
  (`internal/app/preflight.go`); stage-restart mode classifies lifecycle
  differences instead of rejecting them and runs side-effect-minimized startup
  resource preflights.
  Added startup preflight helpers:
  `cache.Preflight`, `observability.PreflightAccessSinks`,
  `observability.ValidateTracerConfig`, `admin.PreflightConfig`,
  `server.PreflightACMEStartup` (each probes writability with a temp file;
  no long-lived handles).
  Added `lifecycle.ChangeSet` type alias to `internal/lifecycle/diff.go`.
  Wired file-backed store into `ConfigApplyCoordinator` (`serve.go`); added
  startup reconciliation after readiness. Updated docs/reload-semantics.md.
- **Managed hot-apply coordinator with exact restoration** (P2-02, #67):
  introduced `ConfigApplyCoordinator` (`internal/app/config_apply.go`) that
  serializes every managed config write, keeps exact previous raw bytes, runs
  preflight, persists atomically, suppresses file-watcher echoes, submits
  correlated reloads, waits for results, and restores the exact previous bytes
  (including comments/formatting) when the reload fails before `Publish`.
  Hot applies are refused while a planned restart is pending; restart-required
  changes return `restart_required: true` with `can_stage: true`. Added
  `PlannedRestartStore` and `ApplyResult` types; moved orchestration out of
  anonymous `serve.go` closures. Added focused tests in
  `internal/app/config_apply_test.go`. Updated `docs/reload-semantics.md`.
- **Correlated reload results, deadlines, phase timing, and bounded cancellation** (P2-01, #66):
  introduced structured `ReloadResult` (`internal/server/reload_result.go`) with
  transaction ID, source, desired/serving version fingerprints, per-phase
  timing, and per-subsystem (`http`, `stream`) status. `ReloadRequest` now
  carries `ID`, `Deadline`, and a result channel; `Server.doReload` derives a
  per-request bounded context, records `started_at`/`completed_at`/`duration_ms`,
  and cancels before Publish when the deadline expires. Added `ApplyMode`,
  `ConfigApplyResult`, and `PendingRestartStatus` types
  (`internal/app/config_apply.go`, `internal/admin/config_apply.go`); admin
  apply handlers now return the correlated reload outcome in the `reload`
  block and map validation failures to 400 and restart-required rejections to
  409. Composition root in `internal/app/serve.go` wires `ApplyConfigRaw`,
  `ApplyConfig`, `WriteConfigRaw`, and `SaveConfig` closures that persist,
  submit correlated reloads, and wait for results. Updated
  `docs/reload-semantics.md` with deadline/result semantics and added focused
  tests in `internal/server/reload_timeout_test.go`.
- **Schema-correct lean budgets and OSS/open-core boundary** (P1-02, #64):
  reconciled stale TOML examples in [docs/benchmarks.md](docs/benchmarks.md)
  against the current `GlobalConfig`, `[compression]`, and `[rate_limit]`
  schemas; added `## Lean product budgets` with recorded baselines for binary
  size, per-build-tag delta, idle RSS, startup latency, config check latency,
  hot-path allocations, and Console initial-route compressed size; added
  `internal/config/benchmarks_doc_test.go` to parse and validate every
  `docs/benchmarks.md` TOML block against the live schema. Created
  [ADR 0012](docs/adr/0012-oss-open-core-boundary.md) recording the permanent
  OSS floor (single-node data plane, core config format, security fixes,
  no Cloud dependency, data portability) and the legitimate commercial ceiling
  (fleet, identity, compliance, hosted ops, support). Updated cross-references
  in [docs/vision/README.md](docs/vision/README.md),
  [docs/roadmap/README.md](docs/roadmap/README.md), [README.md](README.md),
  and [docs/compatibility.md](docs/compatibility.md).
- **Horizon spec simplification and objective docs checks** (P1-03, #65):
  replaced "Vision horizon — demand-gated" with the standard
  "Concept horizon — not committed" banner in
  [docs/specs/year-3.md](docs/specs/year-3.md),
  [docs/specs/year-4.md](docs/specs/year-4.md), and
  [docs/specs/year-5.md](docs/specs/year-5.md); removed squad assignments and
  quarter schedules (Git history preserves them); added a revalidation note
  and kept the bounded AI Gateway MVP separate from the full Year 4 concept.
  Updated [docs/specs/README.md](docs/specs/README.md) navigation to label
  Year 3–5 as concept horizons. Extended [scripts/docs-check.py](scripts/docs-check.py)
  with `check_horizon_specs()`, `check_active_roadmap_links()`, and
  `check_roadmap_active_ids()`; added matching fixtures in
  [scripts/test_docs_check.py](scripts/test_docs_check.py) for missing banners,
  broken active links, duplicate active IDs, and delivered/active overlap.
- **`jul capabilities [-json]`** (`cmd/jul/capabilities.go`): new subcommand that reports which optional features are compiled into this binary (`waf`, `stream_proxy`, `wasm_plugins`) and the canonical exit-code contract. Outputs human-readable text or JSON. Compiles in both lean and full builds (false/true via existing stubs).
- **`make ci-pr` local gate** (`Makefile`): adds `go vet` and `docs-check.py` on top of `ci-full`, giving the closest local approximation to the merge gate. CONTRIBUTING.md updated to accurately describe what `ci-full` and `ci-pr` do and do not cover.
- **Startup-bound restart-required gates** (`internal/server/startup_restart.go`, `internal/app/preflight.go`): `Preflight.Apply` now rejects changes to `[cache]`, `[egress]`, `[admin]`, and `[observability.metrics]` on admin write paths with `restart_required` (HTTP 409). Previously these fields could be saved without taking effect in the running process.
- **`SaveConfig` loads previous on-disk config** (`internal/app/serve.go`): the structured-patch write path now passes the current file as `prev` to `Preflight.Apply`, enabling all existing restart-required checks (ACME, listener, tracing, access-log) on the structured API path. Previously these checks were skipped.
- **Pending-restart Console banner** (`internal/admin/ui/src/app/Layout.tsx`): persistent amber `RestartRequiredBanner` appears on every page when `overview.pending_restart` lists subsystems that have changed on disk since the server started. Backed by `PendingRestartCheck` in `internal/app/serve.go`.
- **gRPC Transcode designer in nav** (`internal/admin/ui/src/app/Layout.tsx`): `/transcode` added to the Configure nav group; automatically included in the command palette.
- **`docs/config-lifecycle.yaml`**: machine-readable lifecycle classification of all config fields (restart-required, new-listener-only, hot-reload).
- **`docs/feature-status.yaml`**: machine-readable manifest of all 20 shipped features with GA criteria status.
- **Semantic docs-check gates** (`scripts/docs-check.py`): `check_lifecycle_manifest()` and `check_feature_status_manifest()` validate YAML and cross-check reload-semantics.md; `check_finding_uniqueness()` catches conflicting resolved/open status markers in audit docs.

### Fixed
- **Single immutable candidate per transaction** (`internal/config/candidate.go`, `internal/app/serve.go`, `internal/app/preflight.go`, `internal/server/reload_plan.go`, `internal/app/factory.go`): startup, admin preflight, and live reload now resolve secret references exactly once into a `config.Candidate{Raw,Effective,Redaction,Digests}` and pass that immutable object through validation, TLS checks, handler dry-runs, stream dry-runs, bind probes, and restart checks. The handler factory no longer re-resolves secrets, eliminating the window where preflight approval and served bytes could use different secret generations (R7-05).
- **TLS certificate rotation is restart-only** (`internal/server/server.go`, `internal/server/reload_plan.go`, `internal/lifecycle/lifecycle.go`, `docs/tls-acme.md`, `docs/configuration.md`): `reloadCertificates`/`ReloadPlan.RefreshCerts` are now no-ops. Static `cert`/`key` rotation requires a process restart, matching the existing lifecycle registry classification (R7-07).
- **Compression `*bool` tri-state semantics** (`internal/config/schema.go`, `internal/config/parser.go`): `CompressionConfig.Enabled` is now `*bool` with three distinct states — explicit true, explicit false, and omitted/nil (auto-detect). Auto-detect enables compression when any non-zero setting is present, including `precompressed = true` (RA-04). The previously unreachable auto-enable branch is replaced with correct logic.
- **`LastReload` wired before `admin.New`** (`internal/app/serve.go`): `server.New` and `deps.LastReload` are now constructed before `admin.New`. The admin server's apply handlers now see the previous reload snapshot in `previous_reload`.
- **`RateLimitConfig.Enabled` TOML tag typo** (`internal/config/schema.go`): corrected `tomm:"enabled"` → `toml:"enabled"`.
- **Plugin upload default comment** (`internal/config/schema.go`): comment corrected to state default is disabled (secure by default), matching parser behavior.
- **GitHub Actions pinned to immutable SHAs** (`.github/workflows/`): all external actions now use full commit SHAs with version comments.
- **Architecture doc** (`docs/architecture.md`): updated to accurately describe `internal/app.Serve` as the composition root; stale `buildHandlers` reference removed.

### Changed
- **`bench-compare.sh` is advisory-only** (`scripts/bench-compare.sh`): the script no longer claims to gate CI, no longer auto-creates a baseline, and always exits 0. It remains useful for manual comparison on dedicated hardware. See script comments for establishing a real regression gate.

- **`jul serve` subcommand** (`cmd/jul/`): explicit, discoverable form of the default bare `jul` invocation — `jul serve [-config f]` and `jul` are equivalent. Tab-completion, `--help`, and the usage block all surface it. Documented in [getting-started.md](docs/getting-started.md) and [README.md](README.md).
- **`jul fmt --diff`** (`cmd/jul/`): new flag on `jul fmt` that emits a unified diff of the formatting changes without rewriting the file, then exits 0 (no changes needed) or 1 (changes would be made). Useful as a CI formatting gate without in-place rewrites. Documented in [getting-started.md](docs/getting-started.md), [zeroconf.md](docs/zeroconf.md), and [README.md](README.md).
- **`jul serve` and `jul fmt --diff` documented** in [docs/getting-started.md](docs/getting-started.md): new "Starting the server with a config file" section and expanded `jul fmt` section with `-diff` CI usage.
- **`docs/known-limitations.md`** (new): aggregates every feature's documented limitation list from 12 feature docs (auth, cache, compression, gRPC, HTTP/3, rate limiting, L4 stream, service discovery, WAF, WASM plugins, NGINX importer) plus the admin single-token limitation and single-node-only constraint. Linked from [docs/index.md](docs/index.md).
- **`docs/security-posture.md`** (new): operational security companion to SECURITY.md covering the current admin auth model and known gaps, RBAC roadmap, admin listener hardening checklist, sessionStorage token rationale, SSRF posture table, secret handling, file permissions, plugin sandbox controls, TLS/mTLS recommendations, and release supply-chain. Linked from [docs/index.md](docs/index.md).
- **Real-server Playwright E2E** (`internal/admin/ui/e2e/real-server.spec.ts`): 6 API-level tests that run against a real `jul -tags console` binary and validate Go admin API response shapes against the Zod schemas in `client.ts`, catching schema drift that mocked tests cannot detect. Wired into CI as the `console-e2e-real-server` job. Companion test config: `testdata/console-e2e.toml`.
- **Kubernetes EndpointSlice discovery CI lane** (`.github/workflows/discovery-k8s-kind.yml`, `scripts/test-discovery-k8s-live.sh`): Linux/kind analogue of the existing Consul convergence lane. Spins up a `kind` cluster, applies a namespace/RBAC/EndpointSlice, starts jul with K8s discovery, patches the EndpointSlice port 18081 → 18082, and asserts both the K8s API and jul's admin upstream pool (`/api/apps`) converge. Runs nightly and on PRs touching `internal/upstream/**`. Closes the last discovery regression gap (K8s was previously local-runbook-only).
- **`scripts/burn-in-wasm.go`** (new): sustained WASM plugin load generator. Drives concurrent HTTP workers against a wasmplugins-tagged server, asserts the plugin-header (`X-Plugin`) is present on every successful response, tracks an error budget for transport warmup noise, and prints a progress summary every 30 s. Used for the RG-1 WASM soak evidence.

- **Legacy CLI flag deprecation notices**: `jul --check` now prints "Deprecation notice: use `jul check` instead"; `jul --version` prints the equivalent. Both flags continue to work unchanged (backward-compatible). Only `-check` and `-version` are deprecated; `-config` is not.
- **`?token=` URL parameter removed from Console bootstrap** (`internal/admin/ui/src/main.tsx`): the frontend no longer reads `?token=` from the URL, stores it in sessionStorage, or warns about it. The Go backend's `auth()` function has never accepted this parameter; the frontend-only bootstrap leaked credentials to access logs, browser history, and `Referer`. The in-app token prompt (`AuthGate`) is the sole supported authentication path.
- **`TestHealthCheckTCP` polling fix** (`internal/upstream/health_test.go`): replaced a zero-latency TCP dial assertion after `ln.Close()` with a 50 × 2 ms polling loop. Root cause: on Linux the kernel can complete a SYN/SYN-ACK handshake that was already in-flight before `close()` returned, so a dial at zero delay still succeeds. With 1 ms+ of delay the port is refused; the polling approach is robust across Linux kernel versions and runner load levels.
- **WASM plugin 8h soak evidence \u2014 authoritative re-run at production-representative load** (`docs/soak-evidence.md`, `docs/status.md`, `docs/ga-push.md`): the 2026-07-12 soak (33,428 requests at ~1 req/s) is superseded by a 2026-07-16 Linux run at ~10K\u201320K req/s with `scripts/burn-in-wasm.go` (50 workers, `X-Plugin` header assertion). The 8-hour run completed; the 33-minute verified snapshot shows 21,714,527 requests, 0 missing plugin headers (plugin executed correctly on 100% of successful responses). Transport errors (2,544, 0.012%) were caused by `/tmp` disk saturation from server access-log output at 20K req/s \u2014 not WASM failures. A concurrent 10-minute smoke with server output suppressed produced 12,284,991 requests at ~20,475 req/s with 2 warmup errors and 0 missing headers, confirming full throughput when disk I/O is not a factor. Both runs satisfy the ADR-0005 single-feature soak minimum (8h / 10m). Future WASM soak runs should start the server with `> /dev/null 2>&1` or `access_log = \"off\"` to avoid log volume at high RPS.

### Security
- Bumped the Go toolchain from 1.26.4 to 1.26.5 in the main module, example plugin module, and container build image to clear the newly disclosed stdlib CVEs in `crypto/tls` and `os`.

- **Console Overview panel — interactive charts, metric context, and actionable capabilities** (`internal/admin/ui/`): the Overview screen is now a genuine monitoring surface and entry point to configuration rather than a static snapshot.

  **Charts**: clicking any 2-minute trend sparkline opens a `ChartDetailPanel` (replacing the bare min/avg/max modal) with a one-sentence metric description, labelled X/Y axes with units, the exact start–end wall-clock timestamp of the window, an interactive chart with hover and keyboard (← →) readout showing the precise timestamp and formatted value at each data point, warn/critical threshold lines, and a Summary section reporting current value and change versus the start of the window, trend direction (rising/falling/stable), volatility (low/medium/high), spike/drop counts, statistical distribution (min/avg/median/p95/max), and a health status derived from the same thresholds used by the health band. When fewer than ten samples are available, trend and volatility claims are suppressed rather than computed from noise. An **Export CSV** action copies `timestamp_ms,timestamp_local,value` rows to the clipboard. All six charts share one reusable `ChartDetailPanel` component and a single metric metadata record (`src/lib/metricMeta.ts`) — name, description, unit, axis labels, value formatter, thresholds, and optional configure destination — so per-metric strings are defined once and never scattered across component files.

  **Capabilities & Configuration**: every row in the capabilities grid that has a known configuration destination now renders a visible action button ("Configure Cache →", "Configure WAF →", "Manage Apps →", etc.) that navigates directly to the relevant panel. Rows with no known destination remain informational and render no interactive affordance. The route map (`src/lib/featureRoutes.ts`) uses the exact group and feature-name strings returned by the Go backend (`api_status.go`), with name-level lookup taking priority over group-level so that TLS-related features in the Security group correctly navigate to `/tls` rather than `/security`.

  **Accessibility**: all interactive elements — HealthChips, sparkline cards, and Capabilities row actions — are keyboard-reachable (`tabIndex`, `onKeyDown` Enter/Space); the chart SVG carries `role="img"` and `aria-label`; an `aria-live` region announces hovered data-point values to screen readers; `StatusBadge` uses a coloured dot plus the word "active"/"inactive" so status is not conveyed by colour alone; the Modal inner content scrolls independently of its header and footer. Covered by 29 new unit tests in `src/test/computeMetricSummary.test.ts` (stable, rising, falling, spike/drop detection, health status for standard and inverted-scale metrics, all-identical values, NaN filtering, and the insufficient-data guard).

## [1.32.0] – 2026-07-09

### Fixed
- **Restored two CI gates to green** (repo hygiene): documented the top-level **`[egress]`** allow-list block in [configuration.md](docs/configuration.md#egress) — its `enabled`/`allow` keys and entry formats (CIDR, bare IP, exact hostname, leading-dot suffix) — which the `docs-check` schema-drift gate requires for every `toml`-tagged field on the root `Config` struct (the block was added in #33 but never mirrored into the config reference). Also cleared the pre-existing **console-frontend `lint`** failures (25 errors across `app/Layout.tsx`, `features/overview/OverviewPanel.tsx`, and `features/tls/TLSPanel.tsx`): wrapped numeric template-literal interpolations in `String(...)` (`restrict-template-expressions`), routed promise-returning `navigate(...)` handlers through `void` (`no-misused-promises`), and braced void-returning arrow shorthands (`no-confusing-void-expression`). Behaviour-preserving; embedded console dist rebuilt.

### Documentation
- **Operator troubleshooting hardening + top-level messaging alignment** (AUX-04, #45): expanded [troubleshooting.md](docs/troubleshooting.md) with concrete resolution flows for the operational gaps beyond first-run — **reloads** (startup-bound settings that keep the running value and need a restart), an apply rejected with **`restart_required`** (nothing is saved), the Console **"Applied with a degraded subsystem"** async-reload outcome (#47), **service discovery** (empty pool / `502`, stale backends, Consul/Kubernetes auth + RBAC and `[egress]` interplay, the `jul_upstream_backends` / `jul_discovery_errors_total` / `jul_upstream_healthy` signals, the #24 runbook + #46 CI lane), and **soak interpretation** (reading `errors=` / goroutine / heap-growth signals, the zstd pre-allocation "not a leak" caveat, and the Windows proxy-soak port-exhaustion limit). Aligned the README **feature-maturity** table with the canonical [status.md](docs/status.md) / [ga-push.md](docs/ga-push.md): all shipped features — including service discovery, secrets references, WASM plugins, and the L4 stream proxy — are now **GA** (soak gate closed per [ADR 0005](docs/adr/0005-soak-post-ga-gate.md)), with the "GA — soak pending" tier empty. Added cross-links to the troubleshooting paths from the README CLI section, [reload-semantics.md](docs/reload-semantics.md), [service-discovery.md](docs/service-discovery.md), and [soak-procedures.md](docs/soak-procedures.md). Validated by `docs-check` (883 passed, 0 failed) (SEQ-15 / AUX-04, #45).

### Added
- **`jul version` subcommand + shell completion** (AUX-07, #51): first-class CLI ergonomics for operators and automation. `jul version` prints the version and build metadata — human-readable by default and `--json` for scripts/CI with a stable key set (`product`, `version`, `commit`, `build_date`, `dirty`, `go_version`, `os`, `arch`). The version string is the ldflags-stamped release version; the commit, build date, and dirty flag are read from the Go build info the toolchain embeds automatically (`vcs.*` settings), so they populate for any `go build` from the repository — no extra ldflags — and degrade to `unknown` when VCS metadata is absent. `jul completion <bash|zsh|fish|powershell>` (with a `pwsh` alias) emits a shell completion script covering the subcommand verbs and the `completion`/`version` arguments; source it per session or install it into the shell's completion directory. Both verbs are wired into the subcommand dispatcher and `jul --help`; the legacy `jul --version` output is unchanged. Covered by `cmd/jul/version_test.go` (human + JSON contract, all four shells, usage-error paths, dispatch routing) which runs in the default CI test lane. Documented in the README command-line usage and [getting-started.md](docs/getting-started.md#version-and-shell-completion) (SEQ-17 / AUX-07, #51).
- **WAF reload-churn leak/stability validation** (AUX-06, #50): a new runtime lane (`internal/waf/reload_churn_test.go`, `TestWAFReloadChurnNoLeak`, `waf` tag) proves that rebuilding the Coraza + embedded OWASP-CRS engine on every configuration reload leaks neither goroutines nor heap — the WAF analogue of the #31 auth reload-churn proof. On each reload the server compiles a fresh engine and drops the previous generation without an explicit `Close` (a documented no-op, since `Firewall` owns no worker/timer/socket); this lane asserts that build-drop invariant under sustained churn across four permutations — inline-rule block, full-CRS block, CRS detect-mode, and a mixed cycle — each rebuilt and exercised with benign + attack (path-traversal / XSS) traffic every cycle. Flat slack/budget thresholds (goroutine growth ≤ 20, post-GC heap growth ≤ 64 MiB, both independent of the cycle count) trip on a per-reload leak, which scales with iterations. The default `waf`-tagged lane runs 30 cycles (~4s); it is env-tunable via `WAF_CHURN_ITERS` and rerunnable with `make waf-churn`. Validated at 30 and 200 cycles: goroutines flat 3 → 3 across every permutation, heap growth sub-MiB (30 iters) / ≤ ~6 MiB (200 iters), and all enforcement assertions held each cycle. Evidence recorded in [soak-evidence.md](docs/soak-evidence.md) and cross-linked from [waf.md](docs/waf.md#operational-notes) (SEQ-16 / AUX-06, #50).
- **CI automation for live service-discovery convergence** (AUX-03, #46): a dedicated [`discovery-live`](.github/workflows/discovery-live.yml) GitHub Actions workflow now runs the #24 Consul live lane in CI, so the discovery hot-refresh convergence path is continuously enforced instead of only proven on a developer machine. The workflow **reuses the existing #24 script** (`scripts/test-discovery-consul-live.ps1`) unchanged in spirit — no new test framework — invoking it in a non-interactive CI mode (new `-CI` switch that skips the developer-only Kubernetes-context probe; the backends, Consul registration/deregistration, and the **core convergence assertions are identical** to a local run, so there is no local/CI drift). The Consul lane is the CI lane because it needs only Docker + the Go toolchain (both on `ubuntu-latest`); the Kubernetes lane stays a documented **local runbook** because its script depends on Windows-only host-networking cmdlets (an explicit #46 non-goal to rewrite). The lane runs on demand (`workflow_dispatch`), nightly (schedule), and on pull requests/pushes touching `internal/upstream/**` or the lane script, so a discovery change cannot merge without it. Every run — green or red — uploads the full `tmp/issue24/` evidence bundle (mirroring the #24 local evidence style); the script now also captures each container's `docker logs` **before** teardown, and on failure the workflow prints the summary, pre/post response windows, jul logs, and container logs straight into the job log so a flake is actionable without downloading the artifact. Documented in [service-discovery.md](docs/service-discovery.md#ci-automation-for-live-discovery-issue-46) (SEQ-14 / AUX-03, #46).
- **Explicit console apply-outcome signaling** (AUX-02, #47): the console no longer collapses a configuration apply into a bare success/error. A new pure derivation (`internal/admin/ui/src/lib/applyOutcome.ts`) folds the raw apply signals — whether the write was accepted, whether a hot reload is still pending, the polled L4 stream-reload status, and any restart-required rejection — into **one explicit, severity-tagged outcome** rendered by `ApplyOutcomeBanner`. Every apply now resolves to exactly one of four operator-legible states: **Applied and live** (success — the running server was observed serving the change), **Applied — runtime reloading** (info — accepted and saved but not yet confirmed live; self-clears once a runtime overview snapshot confirms it), **Applied with a degraded subsystem** (warning — the HTTP config was accepted but an asynchronous subsystem reload failed, most commonly the `[[stream]]` L4 proxy, whose failed subsystem and error are named inline), and **Restart required — not applied** (blocked — a startup-bound change where nothing was saved). `ConfigPanel` polls the runtime overview after an accepted apply to observe the async stream outcome and routes a restart-required rejection to the blocking banner instead of the generic error panel. Covered by `internal/admin/ui/src/test/apply-outcome.test.tsx` (derivation precedence + banner rendering for all four outcomes) with the existing write-flow tests updated to assert the new banner. Documented in [console.md](docs/console.md#apply-outcomes) (SEQ-13 / AUX-02, #47).
- **Container image digest pinning + Docker `HEALTHCHECK`** ([Dockerfile](Dockerfile), [deploy/docker/server.toml](deploy/docker/server.toml)): both build stages now pin their base image by tag **and** `@sha256` digest (`golang:1.26.4-alpine` and `gcr.io/distroless/static-debian12:nonroot`) for reproducible, tamper-evident builds — Dependabot's docker ecosystem maintains the digests. The image declares a shell-less `HEALTHCHECK` (exec form) that runs `jul healthcheck` against the admin `/healthz` endpoint (the distroless image ships no shell or curl). To make the probe reliable out of the box, the image now bakes a container-tailored config that enables the admin listener on loopback and serves a placeholder site from `/var/www`, so the server starts cleanly with no host mounts — this also fixes a latent bug where the previously-baked config pointed its static root at a non-existent `/srv/www/example`, preventing an unmounted container from starting. The exact container health command was validated end-to-end against a running server (liveness + readiness both exit 0) (SEQ-12 / HP-05, #38).
- **Metric-cardinality policy and enforcement** (`internal/observability/metrics.go`, `docs/core-http.md`): the HTTP request `method` label on `jul_http_requests_total` and `jul_http_request_duration_seconds` — previously the raw, client-controlled `r.Method` (HTTP permits arbitrary method tokens) — is now folded to the fixed set of standard methods, with every other value collapsing to a single `other` series. Together with the already-opt-in `host` label, **every client-derived metric label is now bounded by construction**, with no operator action required. Published an authoritative label-cardinality **policy table** (classifying each label as fixed / config-topology-bounded / client-derived) plus an **operator relabel cookbook** (dropping noisy topology labels, on-demand-TLS `domain` trimming, a series budget, and a `sample_limit` backstop). Enforced by a regression guard: `TestMetricLabelPolicy` exercises every metric hook and asserts each `jul_*` family carries exactly the documented labels (so a new metric or request-derived label fails the build), and `TestHTTPMethodLabelBounded` proves novel method tokens fold to `other` (SEQ-11 / HP-03, #37).
- **Structured create/delete patch-ops for servers, routes, and upstream pools** (`internal/admin/patch.go`): six new configuration patch operations — `server_add` / `server_remove`, `location_add` / `location_remove`, and `upstream_add` / `upstream_remove` — close the entity create/delete gap in the structured-patch API, which previously covered only edit-existing operations and forced whole-block creation through a raw TOML-fragment hand-off. Each op mutates the parsed config model and is previewed as a diff, then applied through the same validated preflight, optimistic-concurrency (`base_version`), atomic-batch, history, and audit pipeline as every other patch-op — no new machinery. The ops guard their targets: a create errors on a duplicate server/route/pool, a delete errors on a missing target, `upstream_remove` refuses a pool a route's `proxy_pass` still references, and `server_remove` refuses the final server block. Reuses existing finders/builders and adds no new wire/DTO fields. Global-table structured ops (`global_set`/`cache_set`/`compression_set`/`rate_limit_global_set`/`admin_set`/`access_log_set`) remain a documented follow-on — their guided validated-TOML-upsert editors already provide a diff-reviewed structured path. Covered by round-trip and guard tests in `internal/admin/patch_crud_test.go` (SEQ-10 / HP-06, #36).
- **Console RBAC design spec** ([docs/specs/console-rbac.md](docs/specs/console-rbac.md), [ADR 0010](docs/adr/0010-console-rbac.md)): an implementable, phased design that replaces the single shared admin token with named principals, predefined roles (viewer/operator/admin + optional auditor) **and** custom roles, scoped/revocable/rotatable hashed tokens, deny-by-default authorization at the API boundary, and per-principal audit attribution. Includes an exhaustive permission matrix over every admin endpoint, a `[admin.rbac]` config schema, a backward-compatible migration path, a threat model, and a four-phase implementation plan. Design only — no runtime behavior change (SEQ-09 / HP-02, #35).
- **Optional Git hooks for local CI gate parity** (`.githooks/`, `scripts/install-hooks.{sh,ps1}`, `make hooks`): repo-managed `pre-commit` (gofmt on staged Go files) and `pre-push` (gofmt + `go vet`/`go build`/`go test` on the lean profile, plus `golangci-lint` and the console frontend checks when those toolchains are present) hooks that mirror the CI fast gates (`make ci-fast`). Installed in one command via `core.hooksPath`; non-destructive (check-only) and bypassable with `--no-verify` or `JUL_SKIP_HOOKS=1`. Documented in CONTRIBUTING.md with the parity/limitations versus full CI (SEQ-08 / HP-04, #34).
- **Optional egress allow-list** (`[egress]`, `internal/egress`): a hardening policy that constrains the server's config-driven auxiliary fetches — JWKS retrieval, forward-auth subrequests, and Consul/Kubernetes discovery — to an operator-approved set of hostnames and CIDRs, refused at connect time. It bounds the SSRF blast radius of a misconfigured or compromised config (`jwks_url`, forward-auth `url`, discovery `address`/`api_server`). Disabled by default and fully backward-compatible; enforcement is at the transport `DialContext` so it also covers redirects, and TLS SNI/Host are preserved. Upstream proxying, active health checks, and ACME are intentionally out of scope (see [docs/egress.md](docs/egress.md)). (SEQ-07 / HP-07, #33)
- **`jul healthcheck` subcommand** (`cmd/jul/cli.go`): a shell-free liveness/readiness probe for containers, systemd, and Kubernetes. It discovers the `[admin] listen` address from the config (or takes `-addr`/`-url`) and GETs the admin `/healthz` (liveness) or, with `-ready`, `/readyz` (readiness). Deterministic exit codes — `0` healthy, `1` unhealthy (non-2xx, unreachable, or timeout), `2` usage/config error — with `-json` and `-quiet` output modes. The health verdict is strictly `0`/`1`, so it is safe in a Docker `HEALTHCHECK` from the distroless image (which ships no `curl`/`wget`). Documented in the README and [deployment.md](docs/deployment.md#health-checks) with Docker/systemd/Kubernetes examples (SEQ-06 / HP-05, #32).
- **Auth reload-churn leak validation** (`internal/auth/reload_churn_test.go`): `TestReloadChurnNoLeak` proves that rebuilding authenticators on every configuration reload — Basic, JWT, forward-auth, and a mixed all-methods permutation — leaks neither goroutines nor heap. It runs in the default CI lane (300 reload cycles per permutation) and is env-tunable via `AUTH_CHURN_ITERS` for a dedicated soak lane; a 3,000-cycle run holds goroutine counts exactly flat with only KB-scale post-GC heap movement (SEQ-05, #31).

### Changed
- **Admin big-file decomposition** (AUX-05, #49): behaviour-preserving, same-package extraction of the largest first-party files, continuing the #20/#30 tranches. `internal/admin/patch.go` shrank 1589 → 1156 lines — its JSON wire/DTO types moved to `patch_types.go` and its pure DTO→config builders + audit formatters to `patch_builders.go` (now directly unit-tested in `patch_builders_test.go`). `internal/admin/server.go` shrank 755 → 656 lines — the admin mux registration table moved to `routes.go`. No public/package API or import changes; both the lean and full build-tag test profiles pass. The reproducible decomposition method and admin file map are documented in [architecture.md](docs/architecture.md#large-file-decomposition-internaladmin).

## [1.31.0] – 2026-07-06

### Fixed
- **L4 stream burn-in harness**: urn-in-stream.toml passive-health cooldown fix (max_fails=10, ail_timeout=3s) + persistent-connection load generator.

### Added
- **scripts/burn-in-stream-load.go**: TCP echo load generator for L4 stream soak testing.

### Changed
- docs/soak-evidence.md: Added L4 stream 1h soak + Phase 2A 8h soak (5.05M req, 0% err).
- docs/status.md + docs/ga-push.md: All 20 features promoted to full GA.

## [1.30.0] – 2026-07-05

### Fixed

- **Compression auto-enable bug** (`internal/config/parser.go`): a `[compression]` block with explicit settings (`encoders`, `min_size`, `types`) but without `enabled = true` was silently skipped, leaving responses uncompressed and the console showing "compression disabled". The parser now auto-enables compression when any setting is present — the block implies intent. Users can still explicitly disable with `enabled = false`.
- **OTel schema-URL conflict** (`internal/observability/tracing.go`): imported `semconv/v1.39.0` while the build pulls `otel v1.44.0` (which uses `semconv/v1.41.0`). `resource.Merge()` failed with mismatched schema URLs, preventing tracer initialization. Fixed by updating the import to `semconv/v1.41.0`.

### Added

- **Phase 2A consolidated burn-in harness**: `burn-in-full.toml` exercises **all 10 shipped features simultaneously** (proxy, cache, rate-limit, WAF, auth, compression, TLS, mTLS, upstream health-checks, OTel tracing) in a single config.
- **Load-generator `-full` mode** (`scripts/burn-in-load.go`): `-full` flag exercises all 10 features in one run with TLS + mTLS client cert support, per-status counters (2xx/401/403/429/5xx), and authenticated traffic mix.
- **TLS certificate generator** (`scripts/gen-certs.go`): generates fresh CA + server (localhost) + client certificates for burn-in mTLS testing (1-year validity).
- **mTLS test certificates**: `testdata/tls/client.crt`, `client.key`, `localhost.ext` — enables end-to-end mTLS soak verification.

### Changed

- `docs/soak-evidence.md`: added Phase 2A 5-minute pilot (29,587 req, 0% err) and **8-hour completed soak** (2,120,299 req, 0% err, 100% success) — the most demanding soak test in the project history.
- `docs/status.md`: updated version stamp to v1.30 / 2026-07-05; added Phase 2A soak-tracking row; added v1.30.0 changelog entry.

## [1.29.0] – 2026-07-03

### Added

**GA evidence bundles — post-GA soak gate**
- All remaining Beta features promoted to **GA — soak pending** (criteria ①②③④⑥⑦⑧⑨ met; criterion ⑤ soak is a post-GA gate per ADR 0005). This clears the Beta backlog entirely — all 20 shipped features are now GA — soak pending.

**Per-feature new evidence**
- **HTTP/3 over QUIC (Y1-11)** — `docs/http3.md` with QUIC/Alt-Svc behaviour matrix, `BenchmarkHTTP3Throughput` (~259 μs/op), 4-item limitation list, 5-row threat note, and `docs/status.md` soak-tracking row. | [http3.md](docs/http3.md)
- **WASM plugin system (Y2-02)** — `docs/plugins.md` expanded with a 19-row behaviour matrix (ABI boundary, guest containment, reload, fetch/KV guards, KV quotas), 5 benchmarks in `internal/plugins/bench_test.go` (`BenchmarkPluginMiddleware` ~16.5 μs, `BenchmarkPluginHandler` ~20 μs, `BenchmarkPluginKVCounterWithCapability` ~23 μs, `BenchmarkPluginParallel` ~3.4 μs amortised), 5-item limitation list, 7-row threat note, fuzz targets `FuzzPluginInvoke` and `FuzzHostAllowed` in `internal/plugins/fuzz_test.go`. | [plugins.md](docs/plugins.md)
- **L4 stream proxy (Y2-03)** — `docs/stream.md` with 23-row behaviour matrix (TCP/UDP relay, SNI routing, PROXY protocol, reload, preflight, UDP sessions), 4 benchmarks in `internal/stream/bench_test.go` (`BenchmarkTCPPassthrough` ~3.2 ms, `BenchmarkTCPParallel` ~3.3 ms, `BenchmarkUDPRelay` ~33 μs, `BenchmarkUDPAdmitAtCap` up to 254 μs for 10k sessions), 5-item limitation list, 6-row threat note, fuzz targets `FuzzReadProxyHeader` and `FuzzPeekSNI` in `internal/stream/fuzz_test.go`. UDP-churn soak test `TestSoakUDPChurn` added behind the `soak` tag. | [stream.md](docs/stream.md)

**Tracking docs**
- `docs/roadmap/README.md` — v1.30, year-completion checklists corrected: Year 1 is 11/11 GA, Year 2 is 9/9 GA; changelog row added.
- `docs/status.md` — v1.30, GA table now lists all 20 shipped features (including HTTP/3, WASM plugins, L4 stream); Beta section replaced with an "all GA" notice; soak tracking table updated with the 3 newest features.
- `docs/ga-push.md` — v1.30, obsolete Wave 2 and Wave 3 tables removed (consolidated into Wave 1); soak tracking table updated.

### Changed
- `docs/soak-evidence.md` — updated run-log with the 2026-07-01 smoke soak (proxy + udp-churn) verifiable artifact.

## [1.28.0] – 2026-07-03

### Added
- Goroutine-leak detection for the `internal/server` package (`goleak.VerifyTestMain`), plus a Windows CI test lane (lean + full) to catch platform-specific lifecycle bugs.
- Concurrency and negative regression tests: transcode rejects reflection against a non-reflective backend, WASM plugin reload-under-load, and concurrent admin apply/rollback.
- Plugin upload filename hardening: uploads must be a simple `<name>.wasm` (safe charset, no path separators/`..`), with a defense-in-depth check that the stored path stays inside the upload directory. Threat model documented in [docs/plugins.md](docs/plugins.md).
- Soak evidence log ([docs/soak-evidence.md](docs/soak-evidence.md)) with dated runs; CI and release soak jobs now upload a `soak-results` artifact so the ADR-0005 gate is verifiable.
- GA-evidence burndown table in [docs/status.md](docs/status.md) tracking the per-Beta-feature evidence bundle (matrix/bench/threat-note/fuzz/soak).
- Troubleshooting guide ([docs/troubleshooting.md](docs/troubleshooting.md)) and a first-run hint that points to zero-config mode when no `server.toml` is found.
- `internal/app` package with unit-tested composition-root wiring — scope/index/reload helpers and the runtime preflight (`wiring.go`), the admin-deps builder and view adapters (`admin_deps.go`), and the admin write-preflight gate sequence (`preflight.go`) — reducing `cmd/jul/main.go` toward a thin entry point (ADR-0007 testability follow-through).
- CLI JSON output schema documented in [docs/configuration.md](docs/configuration.md).
- `stale_if_error` configuration option in `[cache]` to extend the stale-serving window when a background revalidation encounters an upstream error (5xx or timeout). This protects clients from backend outages by keeping the cached response servable for the configured duration after a failed revalidation.
- Admin config diff support for `stale_if_error` changes in the Console.

### Changed
- `jul lint -json` now emits a stable schema: lowercase field names and a string `severity` (`"warning"`/`"error"`) instead of a numeric enum.
- `jul fmt` no longer emits reserved (`mail`) or empty top-level (`upstreams`, `stream`, `plugins`) tables in canonical output.
- Both configuration rollback endpoints (`POST /api/history/rollback` and the Console-facing `POST /api/config/rollback`) now route through a single `applyMu`-guarded write path, closing a read-modify-write race with a concurrent apply. A v1.1 fix had serialized only the first endpoint; the Console calls the second, so the race remained until this change.
- Split the two largest admin/config source files by concern to keep each under ~600 LOC: `internal/admin/api.go` (1214→502; extracted `api_status.go`, `api_history.go`, `api_wizard.go`) and `internal/config/validate.go` (1005→561; extracted `validate_location.go`, `validate_backends.go`). Behavior unchanged.
- Example configs (`examples/migrate/jul.toml`, `server.full.apps.toml`) no longer carry the empty `stream = []` / `mail = []` tables that `jul fmt` now omits.
- `docs/status.md` and `docs/roadmap/README.md` corrected: Console continuous panels status footnote now explicitly lists live log tail (shipped), WASM plugin manager (shipped with upload pending), and gRPC route designer (planned).

### Fixed
- Intermittent hang/timeout in the `internal/server` test suite under parallel load, caused by leaked keep-alive `persistConn` goroutines in the test HTTP clients.

## [1.27.0] – 2026-07-01

### Added
- Admin Console **WASM plugin upload** (`POST /api/plugins/upload`): validates WASM magic and version, enforces configurable size cap, writes atomically via `atomicfile`, broadcasts `plugin_uploaded` SSE event. Configurable via `[admin]` keys `plugin_upload_enabled`, `plugin_upload_dir`, and `plugin_upload_max_size` (defaults enabled, `./jul-data/plugins`, `32` MB). Set `plugin_upload_enabled = false` to disable the endpoint.

> **Note:** Default changed to `false` (secure-by-default) in v1.29.0 ([`internal/config/parser.go`](internal/config/parser.go)).

- Admin Console **gRPC route designer** (new Transcode panel): upload a compiled protobuf FileDescriptorSet (`.pb`) for inspection-only preview of the `google.api.http` annotations it declares, configure backend target / TLS / streaming / stream framing, then open the generated `grpc_transcode` route in the config editor for the standard Validate → Diff → Apply flow. The generated route exposes all methods from the descriptor set (per-method filtering is not supported). Cross-linked from existing `grpc_transcode` route detail drawers.
- Admin API endpoint `POST /api/transcode/descriptor-upload` parses uploaded descriptors and returns methods with HTTP bindings (no `grpc` build tag required on the admin side).

### Changed
- `docs/status.md`: Console continuous panels footnote updated — live log tail ✅ shipped; WASM plugin manager ✅ shipped; gRPC route designer ✅ shipped.
- `docs/roadmap/README.md`: Y2-09 row updated to reflect closed panel backlog (`.wasm` upload + gRPC designer both shipped); backlog is now empty.
- `docs/console.md` capability matrix: added gRPC-JSON transcoding row (Guided-create); Plugins row updated to include `.wasm` upload; API endpoint map updated with `POST /api/transcode/descriptor-upload`.

## [1.26.0] – 2026-06-30

### Changed
- Consolidated release notes. No new features beyond v1.0.0 (all foundation features were first introduced in v1.0.0 or earlier Betas).

## [1.0.0] – 2026-06-21

### Added
- First stable release of Jul.IA HTTP edge server.
- All foundation features declared GA — soak pending (see `docs/status.md`):
  - Core HTTP (static, proxy, FastCGI/uWSGI, vhosts, routing)
  - TLS + automatic HTTPS
  - Authentication (CIDR/Basic/JWT/forward-auth)
  - gRPC transcoding and passthrough
  - mTLS client auth
  - Console (operations cockpit)
- Full feature set with expanded descriptions:
  - Core HTTP server engine: static file serving, reverse proxy, FastCGI/uWSGI, virtual hosts, and routing (`exact`, `prefix`, `regex`).
  - TLS 1.2/1.3 termination with SNI certificate selection, HTTP→HTTPS redirect, and dynamic certificate selection at listener startup.
  - ACME automatic HTTPS (HTTP-01 and TLS-ALPN-01 challenges) with on-disk certificate cache and OCSP stapling — gated behind `acme` build tag.
  - mTLS / client certificate authentication with CA bundles, SAN verification, and CRL checking.
  - HTTP/3 over QUIC sharing TLS certificates, advertised via `Alt-Svc` — gated behind `http3` build tag.
  - h2c (cleartext HTTP/2) support for native gRPC clients without TLS.
  - gRPC-JSON transcoding (`grpc_transcode`) from compiled descriptor sets or server reflection — unary and streaming (server/client/bidi) with NDJSON and SSE framing modes — gated behind `grpc` build tag.
  - Native gRPC passthrough with trailers preserved and streaming frame flush — gated behind `grpc` build tag.
  - Two-tier response cache (memory L1 + optional disk L2) with TTL, `stale-while-revalidate`, Vary variant support, and admin purge endpoint.
  - Compression (gzip default; optional Brotli via `brotli` tag and Zstd via `zstd` tag) with Accept-Encoding negotiation, MIME allow-list, minimum size gate, and precompressed `.br`/`.gz` sidecar serving.
  - Token-bucket rate limiting keyed by client IP, request header, or JWT claim, with concurrent connection limiting per listener.
  - Authentication: CIDR allow/deny gates, HTTP Basic (bcrypt `htpasswd`), JWT bearer validation against JWKS endpoints (asymmetric only, `none` rejected), and forward-auth delegation.
  - WebAssembly plugin runtime via wazero with capability-gated KV store and outbound fetch, per-plugin memory/time limits, and panic isolation — gated behind `wasmplugins` build tag.
  - L4 stream proxy (TCP/UDP) with TLS SNI passthrough routing and HAProxy PROXY protocol v1/v2 — gated behind `stream` build tag.
  - Service discovery for upstream backends: DNS A/AAAA, DNS SRV (all builds), plus Consul and Kubernetes EndpointSlices behind build tags.
  - Web Application Firewall (WAF) with embedded OWASP Core Rule Set via Coraza — gated behind `waf` build tag.
  - Secrets resolution (`${env:NAME}`, `${file:/path}`, `${secret:/path}`) with log redaction and `jul lint` detection of literal credentials.
  - Zero-downtime hot reload via SIGHUP, file watch, or admin API with generational handler swap, config preflight checks, and automatic listener rebinding.
  - Graceful shutdown with configurable timeout (default 30s).
  - Admin web console (`console` build tag) with live metrics dashboard, upstream health, certificate inventory, config history with one-click rollback, and setup wizard.
  - Prometheus metrics (`/metrics`) covering HTTP requests, cache events, compression, rate limiting, auth decisions, WAF events, upstream health, discovery errors, gRPC transcoding/proxy, plugin invocations, listener connections, stream connections, and certificate expiry.
  - OpenTelemetry tracing (OTLP gRPC/HTTP) with W3C tracecontext propagation, child spans for proxy, upstream, and cache operations — gated behind `otel` build tag.
  - Structured logging (text/JSON) and pluggable access-log sinks (stdout, rotating file, syslog on Unix).
  - CLI: `jul lint` (validation + best-practice warnings, CI-friendly exit codes), `jul fmt` (canonical TOML rewrite), `jul run --serve/--proxy` (zero-config server), and `jul import nginx` (NGINX → TOML migration, gated behind `importer` tag).
  - NGINX config compatibility guide and migration example.
- Multi-architecture release binaries (Windows, Linux, macOS on amd64/arm64) in `lean` and `full` profiles.
- Docker image with distroless runtime.
- systemd and Windows service deployment assets.

### Security
- Comprehensive threat model in `SECURITY.md` with per-feature security notes.
- Request parsing hardening, header size caps, slowloris mitigation, HTTP/2 reset flood protection.
- Static file path traversal protection via `os.Root`.
- Admin listener loopback binding by default with bearer token authentication.
- Config snapshotting and audit logging for compliance.

### Changed
- Console v1 hand-written dashboard retired and replaced with v2 React/TS/Vite embedded SPA (~250 KB gz initial-route budget).

### Fixed
- gRPC transcoding streaming limits and strict framing for server/client/bidirectional RPCs.
- Cache disk-tier safety: atomic `0o600` writes, foreign-file isolation, lock-free eviction.
- gRPC transcoding passive health marking on backend-failure gRPC status codes.
- Console config patches are now atomic with audit fail-loud behavior.

## [0.9.0] – 2026-05-15

### Added
- Beta release of L4 stream proxy (TCP/UDP), WASM plugins, service discovery (Consul/K8s), WAF, and HTTP/3.
- Soak test harness and fuzz CI jobs.
- Benchmark harness for performance regression detection.

### Changed
- Stream proxy, WASM plugins, WAF, HTTP/3, and Consul/Kubernetes discovery promoted from experimental to Beta.
- TOML schema extended with upstream health checks, service discovery blocks, and plugin configurations.

