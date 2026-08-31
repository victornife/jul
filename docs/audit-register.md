# Audit Register

This page identifies the current audit disposition and preserves earlier audit
evidence. Issue closure alone is not audit evidence, and a dated audit is not a
live issue tracker.

## Current authoritative records

| Record | Source baseline | Current role | Disposition |
| --- | --- | --- | --- |
| [2026-08-31 NGINX migration corpus closure](audit/2026-08-31-nginx-migration-corpus-closure.md) | PR #352 merge `ec098502` plus the #154 closure tranche | Current bounded migration-corpus and selected-dimension E2E evidence | Closure contract; exact-head CI and merge are recorded on #154 and its closure PR |
| [2026-08-07 response-cache recertification](audit/2026-08-07-cache-recertification.md) | Post-#131/#132/#133 cache tree | Current cache conformance and retained-GA evidence | Complete; #107/#134 closed |
| [2026-08-03 combined repository re-audit](audit/combined-audit-2026-08-03.md) | `66c71b2c...` | Dated programme-opening audit and historical finding source | Superseded for current issue state by #62 and later implementation evidence; not rewritten retrospectively |
| [Stage 0/1 programme closure](audit/2026-08-05-stage-0-1-programme-closure.md) | `0de8541e...` | Exact-SHA programme-foundation disposition | Complete, with residuals transferred explicitly |
| [2026-07-31 full repository audit](audit/2026-07-31-full-repository-audit.md) | `e8865615` plus recorded remediations | Historical audit and remediation evidence | Maintainer-certified and superseded under #130; no independent two-human certification claimed |

## Current programme disposition

- Cache correctness/recertification, closed-world lifecycle authority and
  structured configuration Phase 5 are complete.
- ADRs 0016–0019 are accepted. Canonical client identity, backend trust,
  routing/response policy, configuration authority and generated contracts are
  implemented on `main`.
- Those post-RC capabilities keep their own maturity and delivery rows in
  [status.md](status.md); implementation does not imply stable publication or GA.
- Generic resilience implementations are substantially merged, while #287/#144
  retain integrated evidence/external-contract closure at the issue #353
  baseline.
- NGINX assessment, provenance and bounded include traversal are merged;
  compatibility-corpus and later diagnostics work continue separately.
- The versioned supported external Admin API and remote CLI remain #150/#151.
- Selected runtime dynamics, support bundle, `jul doctor` and the bounded AI
  experiment remain later portfolio decisions.

Volatile issue-level sequencing is owned by
[#62](https://github.com/victornife/jul/issues/62). Feature maturity/delivery is
owned by [`feature-status.yaml`](feature-status.yaml). The
[roadmap](roadmap/README.md) records only durable portfolio order.

A release closure entry must identify the exact SHA, commands actually run,
workflow evidence, unavailable lanes and residual risk.

---

## Rounds 9-13 evidence register

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
| R9-14.4 | Never-draining shutdown test | final lifecycle/soak closure | Existing bounded-shutdown coverage; final integrated evidence remains in #106 | transferred, not silently closed | ↪ Superseded/non-blocking | 2026-08-05 |
| R9-14.5 | Hot-added TLS rotation test | #100 static certificate generation | Real TCP/QUIC rotation evidence required by #100 | transferred to selected runtime-dynamics work | ↪ Superseded/non-blocking | 2026-08-05 |
| R9-14.6 | Reflection A→B migration acceptance test | — | Covered by R9-06 / R9-10 | — | ✅ N/A (duplicate) | Phase 2 |

### Round 10

| Finding | Title | Fix location | Test | Evidence | Status | Commit |
|---|---|---|---|---|---|---|
| R10-01 | Durable reload coordinator with sync enqueue ack and watcher echo suppression | `internal/app/serve.go`, `internal/app/wiring.go`, `internal/admin/server.go` | `TestMergeReloadSuppressesWatcherEcho`, `TestReloadReturns503OnEnqueueFailure` | Unit/integration test output | ✅ Implemented | `24de060` |
| R10-02 | Publish coherent runtime snapshot atomically | `internal/server/server.go`, `internal/server/reload_plan.go` | `TestLiveSnapshotCoherentDuringReload` | Race test output | ✅ Implemented | `c80d4e1` |
| R10-03 | One-shot discovery resolution during preflight | `internal/upstream/registry.go` | `TestRegistryPreflightSeedsDiscoveryPool` | Unit test output | ✅ Implemented | `b146d20` |
| R10-04 | Run listener and restart gates with LiveSnapshot even when prev is nil | `internal/app/preflight.go`, `internal/server/server.go` | `TestPreflightApplyUsesLiveSnapshotWithoutPrev` | Unit test output | ✅ Implemented | `bd87561` |
| R10-05 | Address-aware lifecycle diff and pending-restart alignment | `internal/lifecycle/lifecycle.go`, `internal/app/serve.go` | `TestDiffAddressAwareReaddedAddress` | Unit test output | ✅ Implemented | `6cb1a2d` |
| R10-06 | Evict stale gRPC transcoder connections | `internal/transcode/invoke.go` | `TestTranscoderEvictsStaleConnections` | Unit test output | ✅ Implemented | `957d838` |
| R10-07 | HTTP/3 UDP bind probe in preflight | `internal/server/server.go`, `internal/app/preflight.go` | `TestPreflightListenersHTTP3UDP` | Unit test output | ✅ Implemented | `b7ba281` |
| R10-08 | Round 10 audit register | `docs/audit-register.md` | `scripts/docs-check.py` | Docs check output | ✅ Implemented | historical audit commit |

### Round 11

| Finding | Title | Fix location | Test | Evidence | Status | Commit |
|---|---|---|---|---|---|---|
| R11-01 | Consume admin digest after suppressing watcher echo | `internal/app/wiring.go` | `TestMergeReloadConsumesAdminDigest` | Unit/integration test output | ✅ Implemented | `e174f0c` |
| R11-02 | Restore live-aware preflight gates when `prev` is `nil` | `internal/app/preflight.go` | `TestPreflightApplyUsesLiveSnapshotWithoutPrev` | Unit test output | ✅ Implemented | `79aa1c0` |
| R11-03 | Compare admin candidate digest against raw source bytes | `internal/server/server.go`, `internal/config/parser.go` | `TestAdminReloadRawDigestMatchesRawBytes` | Unit test output | ✅ Implemented | `ba4ddf5` |
| R11-04 | Discovery-only upstream candidate snapshot for reflection | `internal/upstream/registry.go` | `TestTranscodeReflectionWithDiscoveryUpstream` | Integration test output | ✅ Implemented | `c2549df` |
| R11-05 | Graceful retirement of removed gRPC backend connections | `internal/transcode/invoke.go` | `TestTranscoderRetiresStaleConnectionsDuringRequest`, `TestTranscoderEvictsStaleConnections` | Unit/integration test output | ✅ Implemented | `d1b5b92` |

### Round 12

| Finding | Title | Fix location | Test | Evidence | Status | Commit |
|---|---|---|---|---|---|---|
| R12-01 | Reused discovery pool loses reflection candidate backends | `internal/upstream/registry.go` | `TestTranscodeReflectionWithReusedDiscoveryUpstream` | Integration test output | ✅ Implemented | `a63ad49` |
| R12-02 | Retired connection promotion closes the connection it returns | `internal/transcode/invoke.go` | `TestTranscoderRetiredConnectionReappearsUsable` | Unit test output | ✅ Implemented | `2457aea` |

### Round 13

| Finding | Title | Fix location | Test | Evidence | Status | Commit |
|---|---|---|---|---|---|---|
| R13-01 | Concurrent retired connection promotion closes the shared connection | `internal/transcode/invoke.go` | `TestTranscoderRetiredConnectionConcurrentReappearance` | Race test output | ✅ Implemented | `2bdf43e` |

## Deferred work rationale

- **R9-14.4 (never-draining shutdown test)** — transferred to the final lifecycle/soak closure in #106. Existing bounded-shutdown tests remain evidence; no new pre-cache blocker was inferred.
- **R9-14.5 (hot-added TLS rotation)** — transferred to selected issue #100. HTTP/3 mTLS parity is already corrected by #121; certificate rotation remains later runtime-dynamics work and is not a prerequisite for #131.

## Running the historical evidence tests

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

# Round 11
go test ./internal/app -run 'TestMergeReloadConsumesAdminDigest|TestPreflightApplyUsesLiveSnapshotWithoutPrev' -race -count=1
go test ./internal/server -run TestAdminReloadRawDigestMatchesRawBytes -race -count=1
go test -tags grpc ./internal/transcode -run 'TestTranscodeReflectionWithDiscoveryUpstream|TestTranscoderRetiresStaleConnectionsDuringRequest|TestTranscoderEvictsStaleConnections' -race -count=1

# Round 12
go test -tags grpc ./internal/transcode -run 'TestTranscodeReflectionWithReusedDiscoveryUpstream|TestTranscoderRetiredConnectionReappearsUsable' -race -count=1

# Round 13
go test -tags grpc ./internal/transcode -run TestTranscoderRetiredConnectionConcurrentReappearance -race -count=1
```

For the authoritative reload contract, see [reload-semantics.md](reload-semantics.md). For current programme sequencing, see the [roadmap](roadmap/README.md) and #62; the combined audit remains the planning baseline and historical evidence source.
