# Runtime resource ownership

This is the authoritative ownership model for Jul's mutable runtime resources
(#428). It answers, for every major resource family, who owns it, what its
identity is, what decides reuse versus replacement, and how it moves through
**Prepare → Publish → Abort → Retire → drain → close**.

It deliberately does **not** repeat which configuration fields are hot-reloaded
or restart-required. That truth is generated from one registry and lives in:

- [docs/config-lifecycle.yaml](config-lifecycle.yaml) — the classification source;
- [docs/generated/config-lifecycle.md](generated/config-lifecycle.md) — the
  generated field table (`hot_reload`, `restart_required`, subsystem, reason);
- [reload-semantics.md](reload-semantics.md) — the reload transaction and its
  outcomes.

The "Driving config" column below names the lifecycle *subsystem*; look the
field up in the generated table rather than here.

## Vocabulary

| Term | Meaning |
|---|---|
| **Scope** | Who a resource lives as long as: `process` (created once in `app.Serve`), `listener` (one bound address), `generation` (one published handler generation), `pool` (one upstream pool), `instance` (one pooled object), `request`. |
| **Logical identity** | What names the resource across reloads (an address, a pool name, a plugin name, a generation ID). |
| **Replacement fingerprint** | The comparison that decides *reuse* versus *build new*. |
| **Liveness override** | Runtime or external state that forbids reuse even when configuration is equal. Only resources whose usability can diverge from configuration have one. |
| **Prepare** | Candidate construction. Nothing is visible to traffic; failure must not touch live state. |
| **Publish** | The single visibility boundary (`ReloadPlan.Publish`). Everything visible afterwards was prepared before it. |
| **Abort** | Discards candidate resources, closing each exactly once, leaving live resources untouched. |
| **Retire** | Removes a replaced resource from reach of *new* work. Retirement never closes a resource in-flight work still holds. |
| **Drain** | The documented boundary until which old work may keep using a retired resource. |
| **Explicit close** | The resource holds something the GC cannot reclaim (goroutines, sockets, FDs, wazero runtimes) and must be closed exactly once by its owner. |
| **Quiescence** | Proof that repeated churn returns goroutines, FDs, connections and instances to baseline. |

## Invariants

1. A failed candidate cannot damage the serving generation.
2. `Publish` is the only visibility boundary.
3. Old work continues only through its documented drain boundary; new work
   never acquires a retired resource.
4. An explicit-close resource is closed exactly once, by its owner.
5. Configuration equality alone never reuses a resource whose liveness can
   diverge from configuration; those resources carry a liveness override.
6. A reload never resets process-owned or resilience state for convenience.
7. Process-owned resources are never accidentally generation-owned, and
   vice versa.
8. Deliberate restart and process-lifetime boundaries are architecture, not
   debt (see [Deliberate boundaries](#deliberate-boundaries)).

## Ownership matrix — identity

| Resource | Owner (package/type) | Scope | Logical identity | Replacement fingerprint | Liveness override | Driving config |
|---|---|---|---|---|---|---|
| HTTP listener | `server.listenerEntry` | listener | listen address | `listenerBindFingerprint` (bind-time properties); a change is `restart_required` | none — a bound socket is live until closed | listeners |
| HTTP/3 listener | `listenerEntry.h3` | listener | listen address + HTTP/3 enabled | bind-time with the TCP listener | `h3Degraded`: an exited accept loop clears Alt-Svc and is not recovered until restart | listeners |
| Handler generation | `server.handlerGen` | generation | generation ID | `lifecycle.Classify` + `ReloadPlan.AssessServingChange` (semantic no-op proof) | static-cert content, admin TLS content, admin runtime health (#415), WASM module content digests (#429), and fail-closed opaque inputs (`hasOpaqueReloadInputs`) | all hot-reload subsystems |
| Generation closer set | `app.GenerationResources` / `app.Generation` | generation | generation span | none — rebuilt with the generation | — | — |
| Static TLS provider | `server.DynamicCertProvider` | listener | listen address | `tlsIdentityFingerprint` (certificate/key **content**) | content fingerprint: same-path rotation is a change | tls |
| Admin runtime (auth snapshot, audit sink, upload dir) | `admin.PreparedCommit`, `auditFileOwner` | admin generation | admin listener | `auditSinkConfig` equality; admin TLS content | `AdminRuntimeHealthy` (audit sink writable, upload dir usable) — #415 | admin |
| Access-log sinks | `observability.BuildAccessSinks` closers | generation | sink kind + path | rebuilt with the generation | writable-directory probe at Prepare | observability |
| Response cache | `cache.Cache` | process | the process cache | none | none | cache (restart boundary for the store) |
| Cache revalidation | `internal/background` lease on `handlerGen` | generation | (effective key, generation) | — | lease refused by a retiring generation | cache |
| Upstream pool | `upstream.Registry` / `upstream.Pool` | pool (survives reloads while its shape is equal) | `(name, scheme)` | `upstreamMeta` equality: scheme, strategy, health config, discovery, backend-TLS policy signature | none needed: only the registry closes a pool, and it never keeps a closed one | upstreams |
| Active health checker (HTTP/TCP/gRPC) | `upstream.healthChecker` | pool | pool | part of `upstreamMeta` | pool retirement cancels in-flight probes and fences their verdicts (#428) | upstreams |
| gRPC health probe connection | `doProbeGRPCHealth` | request (one probe) | backend address | — | — | upstreams |
| Discovery worker | `upstream.Pool` discovery epoch | pool | pool + egress generation | discovery config + egress generation | epoch fence: a stale-generation result is dropped | upstreams, egress |
| HTTP proxy transport | `handler.proxyHandler` | generation | location | rebuilt with the generation | — | servers/locations |
| gRPC passthrough transport | `handler` gRPC proxy | generation | location | rebuilt with the generation | — | servers/locations |
| gRPC transcoder connection cache | `transcode.Transcoder` | generation; entries per backend | `connectionIdentity{dial, logicalID}` | dial address **and** discovered logical identity | a recycled address with a new logical ID is a different peer; retired entries have a 30 s grace and a hard bound of 256 (#414) | servers/locations |
| Transcoder reflection connection | `transcode.New` | Prepare only | backend | — | — | servers/locations |
| FastCGI/uWSGI connections | `handler` FastCGI pool | generation | location | rebuilt with the generation | — | servers/locations |
| L4 stream listener | `stream.listener` | listener | `proto\|addr` | route swapped atomically; protocol or address change is a different listener | none | stream |
| L4 upstream pools | `stream.Server.reg` (its own `upstream.Registry`) | pool | as upstream pools | as upstream pools | as upstream pools | stream, upstreams |
| WASM plugin manager | `plugins.Manager` (compilation cache, KV store, KV quota ledgers) | process | the process manager | none | none | plugins (process boundary) |
| WASM plugin set | `plugins.Set` (one wazero runtime + compiled module per plugin) | generation | plugin name | rebuilt with the generation; the no-op proof compares every module's current digest with the serving one | module content identity: SHA-256 of the exact compiled bytes, snapshotted once per build (#429) | plugins |
| WASM module instance | `plugins.pooledModule` | instance | — | — | retired after `max_invocations`, a trap, or a full pool (#420) | plugins |
| WAF engine | `waf.Firewall` | generation | location scope | rebuilt with the generation | — | waf |
| Tracing provider/exporter | `observability` OTel provider | process | the process provider | none (restart boundary) | none | observability |
| Tracing sample ratio | `observability` root sampler | process state, published per generation | — | — | — | observability |
| Egress policy | `egress.Manager` / `egress.Generation` | process manager, immutable generations | egress generation ID | egress policy equality (`Prepare` reports `changed`) | none: generations are immutable | egress |
| ACME manager / OCSP | `server.acmeManager` | process | the process manager | ACME config change is `restart_required`; OCSP policy is an atomic value | none | tls |
| Rate-limit buckets | `middleware.RateLimiterStore` | process | scope + client key | rate/burst updated in place on reuse | none | ratelimit |
| Redaction state | `server.redactGens` | generation | generation ID | — | — | secrets |

## Ownership matrix — transitions

| Resource | Prepare | Publish | Abort | Retirement trigger | Drain | Explicit close | Quiescence evidence |
|---|---|---|---|---|---|---|---|
| HTTP listener | `StageListeners` binds new addresses, not serving | `Activate` starts accept loops after Publish | staged sockets closed | `RetireRemovedListeners` | `http.Server.Shutdown`, bounded by `shutdown_timeout` | yes (Shutdown/Close) | `TestReloadNoGoroutineLeak` ([reload_test.go](../internal/server/reload_test.go)) |
| HTTP/3 listener | staged with the TCP listener | activated with it; Alt-Svc state updated at Publish | staged QUIC listener closed | with its TCP listener | QUIC close | yes | [http3_test.go](../internal/server/http3_test.go) |
| Handler generation | factory `Prepare` | atomic `handlers.Store` | factory `abortFn` | a newer Publish | `acquireGen` in-flight count; bounded by `shutdown_timeout`, then force-retired | retire callback, `retireOnce` | `TestReloadDrainsBeforeRetiringClosers`, `TestSemanticNoopPreservesGenerationAndResources` ([semantic_noop_test.go](../internal/server/semantic_noop_test.go)) |
| Generation closer set | `Generation.Stage` | `Generation.Commit` adopts the staged set | `Generation.Abort` closes staged closers once | the next `Commit` returns the retire callback | the server calls it after the previous generation drains | yes, **exactly once** (structural since #428) | `TestGenerationOwnershipInvariants`, `TestFactoryChurnReturnsToQuiescence` ([generation_lifecycle_test.go](../internal/app/generation_lifecycle_test.go)) |
| Static TLS provider | `prepareCertRotation` loads candidate material | `PreparedRuntime.Commit` swaps the provider value | no-op (value never installed) | next swap | handshakes in progress keep their certificate | no | `TestCertRotationComponentAbortDoesNotMutateLiveState` |
| Admin runtime | `PrepareAdmin` / `PrepareAdminRuntime` | `PreparedAdmin.Commit` | `PreparedAdmin.Abort` (once) | next Commit | `RetirePreparedRuntime`, asynchronous and bounded | audit file owner reference-counted | `TestRuntimeHealthyDetectsDegradedAuditSinkAndRepairAfterFix`, `TestServingChangeForcesReloadWhenAdminRuntimeDegraded` |
| Access-log sinks | opened in `buildHandlers`, staged | adopted with the generation | closed by `Generation.Abort` | generation retirement | old generation keeps writing until it drains ([known limitation](known-limitations.md)) | yes | `TestAccessLogCandidateAbortLeavesNoRealFile`, `TestFakeAccessSinkClosesOnlyAfterOldRequestsDrain` |
| Response cache | — | — | — | process exit | — | at shutdown | [churn_test.go](../internal/cache/churn_test.go) |
| Cache revalidation | — | lease admitted by the live generation | — | generation retirement cancels leases | bounded by `MaxOperation` and `shutdown_timeout` | context cancel | `TestRevalidationRefusedByRetiringGeneration`, `TestRepeatedRetirementWithActiveLeases`, `TestDrainCancelsAndBoundsLiveBackgroundWork` |
| Upstream pool | `Registry.Begin`/`For` stage fresh pools; reused pools defer `UpdateBackends` | `Registry.Commit`, then `Activate` starts workers | `Registry.Abort` closes fresh pools only | `Commit` closes pools no longer wanted | old generations keep their pool snapshots; parked admission waiters are rejected | `Pool.Close`, `closeOnce` | `TestRegistryAbortKeepsLiveClosesStaged` |
| Active health checker | health config is part of the pool shape | started by `Activate` | never started for aborted pools | pool `Close` | in-flight probe cancelled and its verdict dropped | goroutine exits on `Done` | `TestHealthProbeCancelledAndFencedOnPoolRetirement` ([health_retirement_test.go](../internal/upstream/health_retirement_test.go)), `TestProbeGRPCChurnQuiescence` |
| gRPC health probe connection | — | — | — | probe end | probe timeout / pool retirement | closed by the probe | `TestProbeGRPCChurnQuiescence` |
| Discovery worker | built with the pool | `StartDiscovery` after Commit; egress change → `StopDiscovery` at Publish | fresh discoverer closed | pool `Close` or egress change | in-flight resolve fenced by epoch | yes | `TestDiscoveryWorkerGenerationChurnNoLeak` |
| HTTP proxy transport | built per location | adopted with the generation | closed by `Generation.Abort` | generation retirement | `CloseIdleConnections`; a connection still active at a forced retirement returns to a closed pool and expires within `IdleConnTimeout` (90 s) | yes | `TestFactoryChurnReturnsToQuiescence` |
| gRPC passthrough transport | built per location | adopted | closed by Abort | generation retirement | `CloseIdleConnections` | yes | [grpc_accounting_test.go](../internal/handler/grpc_accounting_test.go) |
| gRPC transcoder connection cache | descriptors loaded; no backend conns cached | adopted | `Close` releases all | generation retirement; per entry: backend leaves pool or logical ID changes | retired entry usable for 30 s; hard bound 256 | yes, once per connection | `TestConnForSameIdentityRetirementRaceDoesNotCloseEarly`, `TestConnCacheChurnExpiresToLiveBaselineAndCloseReleasesAll` ([cache_identity_race_test.go](../internal/transcode/cache_identity_race_test.go)) |
| FastCGI/uWSGI connections | built per location | adopted | closed by Abort | generation retirement | pooled connections closed | yes | `TestFastCGIHandlerGenerationsDoNotLeak` |
| L4 stream listener | `stream.Server.Reload` builds routes and binds new sockets before mutating | route `atomic.Store`; new listeners start (post-HTTP-Publish, own transaction) | newly bound sockets closed; registry `Abort` | removed from the desired set | stops accepting at once; established TCP sessions drain **in the background** until close/`idle_timeout`; UDP sessions torn down; process shutdown bounds the drain at 30 s (#428) | yes | `TestReloadRemovingListenerDoesNotWaitForActiveSessions` ([removed_listener_drain_test.go](../internal/stream/removed_listener_drain_test.go)), `TestStreamProtocolSwitchRetiresUDPSessions` |
| WASM plugin manager | — | — | — | process exit | — | compilation cache closed at shutdown | — |
| WASM plugin set | `Manager.BuildWithEgress` compiles and pre-instantiates | adopted with the generation | `Set.Close` | generation retirement | in-flight invocations finish on their generation | `Set.Close` (wazero runtime) | `TestKVQuotaSurvivesReload`, `TestCompileUsesTheHashedSnapshot`, `TestPluginReplacementDrainsInFlightRequests` ([plugin_identity_reload_test.go](../internal/app/plugin_identity_reload_test.go)) |
| WASM module instance | one eager instance per plugin | — | closed with the runtime | invocation budget, trap, or full pool | — | yes, never dropped silently | `TestPooledInstanceRetiresAfterMaxInvocations` |
| WAF engine | `waf.New` per location scope | adopted | discarded | generation retirement | stateless | no (documented no-op `Close`) | `TestWAFReloadChurnNoLeak` |
| Tracing sample ratio | validated | `UpdateTracingSampleRatio` (no-fail) | — | — | — | no | [issue99_tracing_reload_otel_test.go](../internal/server/issue99_tracing_reload_otel_test.go) |
| Egress policy | `egress.Manager.Prepare` | `Publish` | idle conns of the candidate closed | next Publish | retired generation `CloseIdleConnections` | yes (idle conns) | `TestHandlerFactoryEgressAbortLeavesLiveGenerationUntouched` |
| ACME / OCSP | — | OCSP policy swapped at Publish | — | process exit | — | at shutdown | [acme_ocsp_test.go](../internal/server/acme_ocsp_test.go) |
| Rate-limit buckets | — | parameters updated on next use | — | TTL janitor | — | no | `TestRateLimiterReloadUpdatesBucketParams` |
| Redaction state | — | `registerRedactionGen` | never registered | generation drain | grace-expired generations move to the retired union | no | `TestRedactionRetiredWhenGenerationDrains` |

## Identity versus liveness review

#428 audited every place where "same configuration" leads to "reuse the
running object":

| Reuse site | Rule | Why |
|---|---|---|
| Semantic no-op reload | config equal **and** every liveness override proves unchanged | certificates, admin TLS, admin runtime health and WASM module bytes can change without a config edit; anything else not exposed to the coordinator is opaque and forces a normal reload |
| Upstream pool reuse | config (shape) equal | pools are closed only by the registry, never kept closed; resilience state (passive health, circuit, admission) survives reuse on purpose |
| Discovery worker reuse | config **and** egress generation equal | a worker built under a superseded egress policy must not refresh again |
| Transcoder connection reuse | dial address **and** logical identity equal, entry not expired | a recycled address is a different workload (#414) |
| Audit sink reuse | sink config equal **and** sink healthy | the path can become unwritable independently (#415) |
| WASM plugin set (no-op) | declaration equal **and** every module's fresh digest equals the serving digest | bytes behind a path can change; a path is not a content identity (#429) |
| WASM compilation cache | exact module bytes + compile-affecting runtime flags | wazero keys compiled code by SHA-256 of the bytes; capabilities and ABI are bound at build/instantiation, not cached |
| Egress generation reuse | policy equal | generations are immutable values; nothing to go stale |
| Rate-limit bucket reuse | key equal | buckets are process state; a reload must not reset limits |
| WASM KV quota | plugin namespace | the ledger is process-owned with the store it accounts for (#428) |

No other reuse site needed a liveness predicate.

## Deliberate boundaries

These are intentional and are **not** technical debt:

- **Process-owned:** response cache store, plugin manager (compilation cache,
  KV store and its quota ledgers), rate-limit store, egress manager, ACME
  manager, tracing provider, stream server instance. They survive reloads so
  that state operators rely on (cached objects, limits, KV data, certificates)
  is not reset by a configuration change.
- **Restart-required:** listener bind-time properties, ACME configuration,
  tracing provider configuration and the cache store — see the generated
  lifecycle table for the exact fields.
- **Separate transactions:** the L4 stream reload runs after the HTTP Publish
  as its own transaction with its own registry; the HTTP and L4 registries are
  intentionally not unified.

## Findings fixed by #428

| Finding | Fix | Regression |
|---|---|---|
| An active health probe in flight at pool retirement ran to its full timeout and reported through name-keyed hooks shared with a same-name replacement pool | probe context bound to pool lifetime; retired verdicts dropped | `TestHealthProbeCancelledAndFencedOnPoolRetirement` |
| WASM KV quota accounting was generation-owned while the store is process-owned, so every reload granted a fresh quota | ledger moved to `plugins.Manager` | `TestKVQuotaSurvivesReload`, `TestKVQuotaConcurrentGenerationsShareBound` |
| Removing an L4 listener blocked the reload coordinator (and shutdown) until every established TCP session ended | background drain; shutdown-bounded | `TestReloadRemovingListenerDoesNotWaitForActiveSessions`, updated `TestStreamProtocolSwitchDrainsEstablishedTCP` |
| `Generation.Abort` re-aborted the registry span on a second call; the retire callback could close predecessors twice | both structurally exactly-once | `TestGenerationOwnershipInvariants` |

## Protected regressions

| Issue | Failure shape | Pinned by |
|---|---|---|
| #414 | same-identity transcoder connection retired early / duplicated under an interleaving | `TestConnForSameIdentityRetirementRaceDoesNotCloseEarly`, `TestConnCacheChurnExpiresToLiveBaselineAndCloseReleasesAll` |
| #415 | semantic no-op skipped repair of a degraded admin runtime | `TestServingChangeForcesReloadWhenAdminRuntimeDegraded`, `TestServingChangeStaysNoChangeWhenAdminRuntimeHealthy`, `TestRuntimeHealthyDetectsDegradedAuditSinkAndRepairAfterFix` |
| #420 | `sync.Pool` dropped wazero instances that needed an explicit Close | `TestPooledInstanceRetiresAfterMaxInvocations` |
| #427 | gRPC health probe connections/checkers must not leak across churn | `TestProbeGRPCChurnQuiescence`, `TestHealthProbeCancelledAndFencedOnPoolRetirement` |

## Test vocabulary

[`internal/lifecycletest`](../internal/lifecycletest/lifecycletest.go) provides:

- `Resource` — an explicit-close fake: `AssertClosedOnce`, `AssertOpen`, and
  `Acquire` that fails once retired;
- `Take` / `AwaitQuiescence` — a bounded goroutine/FD baseline check for churn.

Use deterministic synchronization (channels, hooks, injectable clocks) for
interleavings; `AwaitQuiescence` only waits for asynchronous teardown to
converge.

## Adding a resource

A new mutable runtime resource (for example, the #430 WASM response phase)
must add a row to both matrices and answer, before review:

1. Its scope and owner — and why not a longer or shorter one.
2. Its logical identity and replacement fingerprint.
3. Whether its usability can diverge from configuration; if so, the liveness
   override consulted by `AssessServingChange` or by its reuse site.
4. What Prepare builds, what Publish makes visible, what Abort closes.
5. Its retirement trigger and drain boundary, with a bound.
6. Who closes it, exactly once.
7. A churn test that proves quiescence with `internal/lifecycletest`.
