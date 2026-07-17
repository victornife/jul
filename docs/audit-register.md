# Round 9 Audit Register

This page tracks every Round 9 audit finding, the code change that closes it,
the test that demonstrates the fix, and the current status. It is intended as
a lightweight compliance artifact for reviewers and releases.

| Finding | Title | Fix location | Test | Evidence | Status | Date |
|---|---|---|---|---|---|---|
| R9-01 | Startup redaction installed before runtime use | `internal/app/serve.go` | `TestCompositionRootStartupRedactionIsolation` | Unit/integration test output | ✅ Implemented | 2026-07-17 |
| R9-02 | Typed reload requests avoid global candidate slot | `internal/app/serve.go` | `TestAdminWriteAndWatcherEcho`, `TestConcurrentAdminAppliesSerialize` | Integration/race test output | ✅ Implemented | 2026-07-17 |
| R9-03 | Graceful shutdown drains and bounded stop | `internal/server/server.go` | *(deferred to post-GA soak work)* | — | ⏸ Deferred | — |
| R9-04 | Preflight listener gates use live snapshot | `internal/app/preflight.go`, `internal/server/server.go` | `TestPreflightApplyUsesLiveSnapshotForRebind`, `TestPreflightRebindRequiredUsesLiveSnapshot` | Unit test output | ✅ Implemented | Phase 1 |
| R9-05 | Listener fingerprint detects cert rotation | `internal/server/listener_fingerprint.go` | `TestListenerBindFingerprintDetectsCertRotation` | Unit test output | ✅ Implemented | Phase 1 |
| R9-06 | gRPC reflection uses candidate snapshot | `internal/transcode/reflection.go` | `TestGRPCReflectionUsesCandidate` | Integration test output | ✅ Implemented | Phase 2 |
| R9-07 | Upstream candidate correctness | `internal/upstream/registry.go` | `TestRegistryCandidateIsolation` | Unit test output | ✅ Implemented | Phase 2 |
| R9-08 | Handler factory builds from candidate | `internal/app/factory.go` | `TestHandlerFactoryBuildMinimalConfigDryRun` | Unit test output | ✅ Implemented | Phase 2 |
| R9-09 | Pool snapshot isolation across reloads | `internal/server/generation.go` | `TestDynamicHandlerInstallsGenerationSnapshots` | Unit test output | ✅ Implemented | Phase 2 |
| R9-10 | Reflection A→B migration | `internal/transcode/reflection.go` | `TestGRPCReflectionUsesCandidate` | Integration test output | ✅ Implemented | Phase 2 |
| R9-11 | Pending-restart baseline uses effective values and live snapshot | `internal/app/serve.go`, `internal/admin/api.go` | `TestPendingRestartCheck*` in `internal/app/pending_restart_test.go` | Unit test output | ✅ Implemented | 2026-07-17 |
| R9-12 | Listener candidate lifecycle | `internal/server/server.go` | `TestReloadAddsAndRemovesListener`, `TestDoReloadDegradedOnBindFailure` | Unit test output | ✅ Implemented | Phase 1 |
| R9-13 | Candidate preflight with bound state | `internal/app/preflight.go`, `internal/server/server.go` | `TestPreflightApplyUsesLiveSnapshotForRebind` | Unit test output | ✅ Implemented | Phase 1 |
| R9-14.1 | Composition-root startup redaction test | `internal/app/startup_redaction_test.go` | `TestCompositionRootStartupRedactionIsolation` | Integration test output | ✅ Implemented | 2026-07-17 |
| R9-14.2 | Admin write + watcher echo test | `internal/app/admin_watcher_test.go` | `TestAdminWriteAndWatcherEcho` | Integration test output | ✅ Implemented | 2026-07-17 |
| R9-14.3 | Event-race test for typed reload requests | `internal/admin/operational_test.go` | `TestConcurrentAdminAppliesSerialize` | Race test output | ✅ Implemented | 2026-07-17 |
| R9-14.4 | Never-draining shutdown test | — | *(deferred)* | — | ⏸ Deferred | — |
| R9-14.5 | Hot-added TLS rotation test | — | *(deferred)* | — | ⏸ Deferred | — |
| R9-14.6 | Reflection A→B migration acceptance test | — | Covered by R9-06 / R9-10 | — | ✅ N/A (duplicate) | Phase 2 |

## Deferred work rationale

- **R9-03 / R9-14.4 (shutdown draining)** — Timing-sensitive, high flakiness
  risk in CI. Better addressed as a dedicated soak/retirement test with
  generous timeouts after GA.
- **R9-14.5 (hot-added TLS rotation)** — Requires cert fixture generation and
  multi-SNI verification; large test with moderate fragility. Deferred to a
  standalone TLS soak or post-GA hardening sprint.

## Running the evidence tests

```bash
# R9-11 + R9-14.1 + R9-14.2
go test ./internal/app -run 'TestPendingRestartCheck|TestCompositionRootStartupRedactionIsolation|TestAdminWriteAndWatcherEcho' -race -count=1

# R9-14.3
go test ./internal/admin -run TestConcurrentAdminAppliesSerialize -race -count=1

# Full matrix
go test ./... -race -count=1
```

## Semantics reference

For the authoritative reload contract, see [reload-semantics.md](reload-semantics.md).
