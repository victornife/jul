# ReloadPlan file-level task breakdown

**Owner:** Jul.IA core team
**Date:** 2026-07-16
**Scope:** file-level work required to implement ADR 0011 / `docs/specs/reload-plan.md`

Legend:
- **Impact:** High (H) / Medium (M) / Low (L)
- **Effort:** weeks (w) or days (d)
- **Risk:** R1 = user-visible behavior change; R2 = reload failure mode change; R3 = performance regression risk
- **Deps:** upstream tasks that must land first

## 1. `internal/config` — secret resolution

| # | File | Task | Impact | Effort | Risk | Deps |
|---|------|------|--------|--------|------|------|
| 1.1 | `internal/config/secrets.go` | Replace `ExpandSecrets` with `Resolve(cfg) (redact.State, map[string]string, error)` that expands into a deep-copied config and returns a self-contained `redact.State`; remove all calls to `redact.Replace` / `redact.SetMinLen`. | H | 1.5w | R1, R2 | — |
| 1.2 | `internal/config/secrets.go` | Add file-content digest collection during expansion; include env-var sentinel for non-file secrets. | H | 0.5w | R1 | 1.1 |
| 1.3 | `internal/config/*.go` | Audit every log/error path that redacts values; convert to `redact.Global().Apply` or pass `redact.State` explicitly. | M | 1w | R1 | 1.1 |
| 1.4 | `internal/config/secrets_test.go` | Add tests that a failed reload does not mutate global redaction and that digest changes detect content rotation. | H | 0.5w | — | 1.1 |

## 2. `internal/redact` — immutable state

| # | File | Task | Impact | Effort | Risk | Deps |
|---|------|------|--------|--------|------|------|
| 2.1 | `internal/redact/redact.go` | Introduce `redact.State` struct with `values` and `minLen`; implement `Apply`, `Writer`, `Count`, `Clone`. | H | 0.5w | R1 | — |
| 2.2 | `internal/redact/redact.go` | Replace global mutable maps with `atomic.Pointer[State]`; add `Global() State` and `Install(State)`. | H | 0.5w | R1, R2 | 2.1 |
| 2.3 | `internal/redact/redact.go` | Deprecate then remove `Snapshot`/`Restore`, `Replace`, `SetMinLen`; update all callers. | M | 1w | R1 | 2.2 |
| 2.4 | `internal/redact/redact_test.go` | Verify `Install` is atomic; verify old `State` remains usable after a new install. | M | 0.5w | — | 2.2 |

## 3. `internal/lifecycle` — new package

| # | File | Task | Impact | Effort | Risk | Deps |
|---|------|------|--------|--------|------|------|
| 3.1 | `internal/lifecycle/registry.go` | Create package; define `Entry`, `Class`, `Registry`, `RestartRequired`, `NewListenerOnlyChanged`, `PendingRestarts`, `StartupFields`. | H | 1w | R1, R2 | — |
| 3.2 | `internal/lifecycle/fingerprint.go` | Define `Fingerprint`, `ComputeFingerprint`, comparison helpers; include file digests for startup-bound values. | H | 1w | R1, R2 | 1.2, 3.1 |
| 3.3 | `internal/lifecycle/manifest.go` | Load `docs/config-lifecycle.yaml` and merge with in-code overrides; expose `LoadManifest` and validation. | M | 0.5w | R2 | 3.1 |
| 3.4 | `internal/lifecycle/registry_test.go` | Unit tests for every classified field; tests for registry/docs consistency. | M | 0.5w | — | 3.1–3.3 |

## 4. `internal/app` — composition root

| # | File | Task | Impact | Effort | Risk | Deps |
|---|------|------|--------|--------|------|------|
| 4.1 | `internal/app/serve.go` | Move effective/startup fingerprint storage to `Runtime`/`Server`; store bound fingerprint at startup. | H | 1w | R1, R2 | 3.2 |
| 4.2 | `internal/app/serve.go` | Make `worker_threads = auto` restore previous numeric cap when changed back; include resolved GOMAXPROCS in fingerprint. | M | 0.5w | R1 | 4.1 |
| 4.3 | `internal/app/serve.go` | Move `OnReloaded` log-level/worker policy application into `ReloadPlan.PostCommit`. | M | 0.5w | R1 | 4.2 |
| 4.4 | `internal/app/factory.go` | Convert `HandlerFactory.Prepare` to return a generation, abort callback, and handler map; ensure it captures a pool snapshot at commit time. | H | 1.5w | R1, R2, R3 | 5.1 |
| 4.5 | `internal/app/generation.go` | Add `AddCloser` and `ClosePrevious` helpers for generation-scoped cleanup. | M | 0.5w | R2 | 4.4 |

## 5. `internal/upstream` — pool planning

| # | File | Task | Impact | Effort | Risk | Deps |
|---|------|------|--------|--------|------|------|
| 5.1 | `internal/upstream/registry.go` | Introduce `Plan(cfg) (Plan, error)` that stages pools without mutating live maps. | H | 1.5w | R1, R2, R3 | — |
| 5.2 | `internal/upstream/registry.go` | Implement `Plan.Commit() (*Snapshot, cleanup func())` returning an immutable snapshot and a closer. | H | 1w | R1, R3 | 5.1 |
| 5.3 | `internal/upstream/registry.go` | Remove direct live-map updates from reload path; old map updates move to `Retire`. | H | 0.5w | R2 | 5.2 |
| 5.4 | `internal/upstream/snapshot.go` | Add `Snapshot` type and lookup helpers used by handlers. | H | 0.5w | R3 | 5.2 |
| 5.5 | `internal/upstream/registry_test.go` | Test staged plan, commit isolation, abort cleanup, and snapshot stability. | M | 1w | — | 5.1–5.4 |

## 6. `internal/server` — ReloadPlan orchestration

| # | File | Task | Impact | Effort | Risk | Deps |
|---|------|------|--------|--------|------|------|
| 6.1 | `internal/server/server.go` | Define `ReloadPlan` type and `resolveReload`, `planReload`, `abortReload`, `publishReload`, `activateReload`, `retireReload`, `postCommitReload`. | H | 2w | R1, R2 | 1.1, 2.2, 3.2, 4.4, 5.2 |
| 6.2 | `internal/server/server.go` | Refactor `doReload` to use `ReloadPlan` phases; ensure failure calls `Abort`. | H | 1.5w | R1, R2 | 6.1 |
| 6.3 | `internal/server/listener.go` | Add `StagedListener` with activation barrier and `Abort`/`Activate` methods. | H | 1w | R1, R2 | 6.1 |
| 6.4 | `internal/server/listener_fingerprint.go` | Compare expanded bound state to expanded candidate state using lifecycle fingerprints; remove raw-vs-expanded comparison. | H | 0.5w | R2 | 3.2, 6.1 |
| 6.5 | `internal/server/http3.go` | Add `StagedHTTP3` with `Activate`/`Abort`; defer accept-loop start until activation. | H | 1w | R1, R2 | 6.1, 6.3 |
| 6.6 | `internal/server/tls.go` | Add `cache_dir` and `ocsp_stapling` to ACME startup fingerprint; use lifecycle registry for classification. | M | 0.5w | R2 | 3.2 |
| 6.7 | `internal/server/certprovider.go` | Collect certificate updates into `ReloadPlan.CertUpdates` with Apply/Abort. | M | 0.5w | R2 | 6.1 |
| 6.8 | `internal/server/server_test.go` | Add tests for abort at each phase, activation ordering, HTTP/3 staging, and listener barrier. | H | 1.5w | — | 6.1–6.7 |

## 7. `internal/stream` — L4 plan

| # | File | Task | Impact | Effort | Risk | Deps |
|---|------|------|--------|--------|------|------|
| 7.1 | `internal/stream/*.go` | Wrap existing reload path in `stream.Plan` with `Apply`/`Abort`; make it abortable before commit. | M | 1w | R2 | 6.1 |
| 7.2 | `internal/stream/*_test.go` | Test that a failed reload leaves L4 state unchanged. | M | 0.5w | — | 7.1 |

## 8. `internal/admin` — preflight and diff

| # | File | Task | Impact | Effort | Risk | Deps |
|---|------|------|--------|--------|------|------|
| 8.1 | `internal/admin/preflight.go` | Add `log_format` to restart-required gate; use `lifecycle.RestartRequired` against effective fingerprint. | H | 0.5w | R1 | 3.1 |
| 8.2 | `internal/admin/preflight.go` | Run Resolve + Plan without committing for `/config/preflight`. | H | 0.5w | R1 | 6.1, 8.1 |
| 8.3 | `internal/admin/diff.go` | Replace fallback diff with registry-driven structured diff; completeness guarantee. | M | 1w | R1 | 3.1 |
| 8.4 | `internal/admin/diff_test.go` | Tests for completeness and restart-required classification in diff output. | M | 0.5w | — | 8.3 |

## 9. Documentation and governance

| # | File | Task | Impact | Effort | Risk | Deps |
|---|------|------|--------|--------|------|------|
| 9.1 | `docs/config-lifecycle.yaml` | Add missing paths, classify every field, add reasons. | M | 1w | R1 | 3.1 |
| 9.2 | `docs/reload-semantics.md` | Rewrite reload semantics to match ReloadPlan transaction and activation order. | M | 0.5w | R1 | 9.1 |
| 9.3 | `docs/adr/0011-reload-plan.md` | Landed separately. | H | — | — | — |
| 9.4 | `docs/specs/reload-plan.md` | Landed separately. | H | — | — | — |
| 9.5 | `scripts/docs-check.py` | Compare YAML manifest against Go registry; fail on missing or mismatched entries. | M | 0.5w | R2 | 3.3, 9.1 |
| 9.6 | `scripts/docs-check.py` | Add semantic checks that `reason` is present and non-empty. | L | 0.25w | — | 9.5 |
| 9.7 | `docs/status.md` / `docs/known-limitations.md` | Remove/update statements contradicted by the new design. | L | 0.25w | R1 | 9.2 |

## 10. Tests and E2E

| # | File | Task | Impact | Effort | Risk | Deps |
|---|------|------|--------|--------|------|------|
| 10.1 | `internal/server/reload_*.go` (new) | Add focused integration tests for secret rotation, log_format gating, pool snapshot isolation, HTTP/3 activation order. | H | 2w | — | 6.1–6.7 |
| 10.2 | `internal/admin/ui/e2e/real-server.spec.ts` | Extend E2E to assert serving behavior after reload, not only config persistence. | M | 1w | — | 8.2 |
| 10.3 | Burn-in configs | Update `burn-in*.toml` to exercise reload paths under soak. | M | 0.5w | R3 | 6.2 |
| 10.4 | Fuzz tests | Add config-lifecycle fuzz test that randomizes transitions and asserts monotonic generation behavior. | M | 1w | — | 6.2 |

## 11. Console UI (if applicable)

| # | File | Task | Impact | Effort | Risk | Deps |
|---|------|------|--------|--------|------|------|
| 11.1 | `internal/admin/ui/src/...` | Update pending-restart banner and diff view to consume registry-driven diff API. | M | 1w | R1 | 8.3 |
| 11.2 | `internal/admin/ui/e2e/...` | Add E2E coverage for restart-required diff warnings. | L | 0.5w | — | 11.1 |

## Critical path summary

The minimum viable critical path is:

1. `internal/lifecycle` registry + fingerprint (3.1–3.3)
2. `internal/redact.State` (2.1–2.3)
3. `internal/config/secrets.Resolve` (1.1–1.2)
4. `internal/upstream.Plan` + snapshot (5.1–5.4)
5. `internal/server.ReloadPlan` skeleton (6.1–6.2)
6. Listener + HTTP/3 staging (6.3–6.5)
7. Admin preflight + diff (8.1–8.3)
8. Docs + validation (9.1–9.6)

This path touches every finding except R5-11 (E2E) and R5-14 (docs contradictions), which are handled in parallel.

## Sequencing recommendation

| Phase | Weeks | What lands |
|-------|-------|------------|
| 0 | 1–2 | ADR, design docs, `lifecycle` package skeleton, `redact.State` feature flag. |
| 1 | 2–3 | Secret resolution returns `redact.State`; plumb through logging. |
| 2 | 2 | Lifecycle registry + effective startup fingerprint; ACME fingerprint fix. |
| 3 | 2–3 | `ReloadPlan` skeleton; handler/pool staging; abort semantics. |
| 4 | 2 | TCP barrier + HTTP/3 staged start; activation ordering. |
| 5 | 1–2 | Generation-scoped pool snapshot in request context. |
| 6 | 1–2 | Admin preflight + diff; docs-check validation. |
| 7 | 2–3 | Soak, fuzz, E2E, hardening. |

**Total estimate:** 10–16 engineer-weeks.
