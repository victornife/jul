# Round 9 + 10 Audit Register

This page tracks every Round 9 and Round 10 audit finding, the code change that
closes it, the test that demonstrates the fix, and the current status. It is
intended as a lightweight compliance artifact for reviewers and releases.

| Finding | Title | Fix location | Test | Evidence | Status | Date |
|---|---|---|---|---|---|---|
| R9-01 | Startup redaction installed before runtime use | `internal/app/serve.go` | `TestCompositionRootStartupRedactionIsolation` | Unit/integration test output | ✅ Implemented | 2026-07-17 |
| R9-02 | Typed reload requests avoid global candidate slot | `internal/app/serve.go` | `TestAdminWriteAndWatcherEcho`, `TestConcurrentAdminAppliesSerialize` | Integration/race test output | ✅ Implemented | 2026-07-17 |
| R9-03 | Graceful shutdown drains and bounded stop | `internal/server/server.go` | `TestShutdownBoundedByGraceTimeout`, `TestReloadDrainsBeforeRetiringClosers` | Unit test output | ✅ Implemented | Phase 0 |
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

## Round 10 Register

| Finding | Title | Fix location | Test | Evidence | Status | Commit |
|---|---|---|---|---|---|---|
| R10-01 | Durable reload coordinator with sync enqueue ack and watcher echo suppression | `internal/app/serve.go`, `internal/app/wiring.go`, `internal/admin/server.go` | `TestMergeReloadSuppressesWatcherEcho`, `TestReloadReturns503OnEnqueueFailure` | Unit/integration test output | ✅ Implemented | `24de060` |
| R10-02 | Publish coherent runtime snapshot atomically | `internal/server/server.go`, `internal/server/reload_plan.go` | `TestLiveSnapshotCoherentDuringReload` | Race test output | ✅ Implemented | `c80d4e1` |
| R10-03 | One-shot discovery resolution during preflight | `internal/upstream/registry.go` | `TestRegistryPreflightSeedsDiscoveryPool` | Unit test output | ✅ Implemented | `b146d20` |
| R10-04 | Run listener and restart gates with LiveSnapshot even when prev is nil | `internal/app/preflight.go` | `TestPreflightApplyUsesLiveSnapshotWithoutPrev` | Unit test output | ✅ Implemented | `bd87561` |
| R10-05 | Address-aware lifecycle diff and pending-restart alignment | `internal/lifecycle/lifecycle.go`, `internal/app/serve.go` | `TestDiffAddressAwareReaddedAddress` | Unit test output | ✅ Implemented | `6cb1a2d` |
| R10-06 | Evict stale gRPC transcoder connections | `internal/transcode/invoke.go` | `TestTranscoderEvictsStaleConnections` | Unit test output | ✅ Implemented | `957d838` |
| R10-07 | HTTP/3 UDP bind probe in preflight | `internal/server/server.go`, `internal/app/preflight.go` | `TestPreflightListenersHTTP3UDP` | Unit test output | ✅ Implemented | `b7ba281` |
| R10-08 | Round 10 audit register | `docs/audit-register.md` | `scripts/docs-check.py` | Docs check output | ✅ Implemented | *(this commit)* |

## Deferred work rationale

- **R9-14.4 (never-draining shutdown test)** — Timing-sensitive, high flakiness
  risk in CI. Better addressed as a dedicated soak/retirement test with
  generous timeouts after GA. The underlying shutdown draining behavior is
  covered by R9-03 unit tests.
- **R9-14.5 (hot-added TLS rotation)** — Requires cert fixture generation and
  multi-SNI verification; large test with moderate fragility. Deferred to a
  standalone TLS soak or post-GA hardening sprint.

## Running the evidence tests

```bash
# R9-11 + R9-14.1 + R9-14.2
go test ./internal/app -run 'TestPendingRestartCheck|TestCompositionRootStartupRedactionIsolation|TestAdminWriteAndWatcherEcho' -race -count=1

# R9-14.3
go test ./internal/admin -run TestConcurrentAdminAppliesSerialize -race -count=1

# Round 10
go test ./internal/app -run 'TestMergeReloadSuppressesWatcherEcho|TestReloadReturns503OnEnqueueFailure|TestPreflightApplyUsesLiveSnapshotWithoutPrev' -race -count=1
go test ./internal/server -run TestLiveSnapshotCoherentDuringReload -race -count=1
go test ./internal/upstream -run TestRegistryPreflightSeedsDiscoveryPool -race -count=1
go test ./internal/lifecycle -run TestDiffAddressAwareReaddedAddress -race -count=1
go test -tags grpc ./internal/transcode -run TestTranscoderEvictsStaleConnections -race -count=1
go test -tags grpc ./internal/server -run TestPreflightListenersHTTP3UDP -race -count=1

# Full matrix
go test ./... -race -count=1
```

## Semantics reference

For the authoritative reload contract, see [reload-semantics.md](reload-semantics.md).
