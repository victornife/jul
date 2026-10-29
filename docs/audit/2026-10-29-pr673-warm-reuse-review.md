# PR #673 — warm route reuse with private compatibility proof

Date: 2026-10-29. Parent/source baseline: `dab1c1d0e984383546a11307edc8b56a7e972c63`.
This follow-up supersedes the unconditional predicate reload/restart invalidation
in the [previous corrective review](2026-10-29-pr673-corrective-review.md).
The user requested safe warm reuse and a durable design using Jul's existing
architecture. The containing commit holds the implementation and this evidence;
the PR description pins the resulting full source SHA.

## Decision and rationale

Use **stable route ownership plus a separate, keyed matcher compatibility proof**.
This is the best fit for the existing system: it preserves verified equality,
reuses the confidential-version authority and the bounded epoch manifest, and
does not introduce a new key, desired-config store or persistent identity registry.

| Alternative | Reason for rejecting it |
| --- | --- |
| Unkeyed predicate hash in the persistent partition | Offline guessing verifier and identity coupled to secret values |
| Process-random route partition / unconditional reload invalidation | Discards compatible cache state and restart reuse |
| One HMAC of the entire configuration | Unrelated changes invalidate otherwise compatible routes |
| A new opaque identity registry | Additional identity authority and lifecycle/GC obligations; existing optional route IDs already provide durable identity |
| Selected: non-predicate identity plus per-route HMAC compatibility | Warm unchanged routes, private equality evidence, targeted incompatibility fences and reuse of established owners |

`route_id` stays optional. Identified scopes use listener/normalized server names
and the ID. Anonymous path-only scopes use path/type; anonymous predicate scopes
also use an ordinal among anonymous predicates on the same path/type. Unrelated
paths and newly inserted identified routes do not shift those ordinals. Ambiguous
anonymous same-path edits can cause affected slots to become cold; explicit IDs
preserve ownership through reordering. This is the deliberate remaining identity
limit, not a requirement to add IDs to ordinary configurations.

`WithMatcher` derives an HMAC through the existing `versionid.Context`, domain
`cache-route-matcher-v1`, over length-framed scope, normalized type, path,
`Match.CanonicalPredicates()` and preflight-widening semantics. Resolved values
are read from the existing immutable candidate; no secret provider is read again.
The proof lives only in the versioned epoch manifest. It is excluded from cache
keys, response-entry metadata, policy digests, public inspection, metrics and logs.

Same context and proof preserve epochs. Changed/unverifiable proof, removal or
cache disable advances the epoch, preserving prior retirement and GC floors.
The new durable state precedes live visibility; in-flight work on an older epoch
cannot publish after commit. Returning to a former predicate/key does not reset
history. A proof-only active route is retired even when no response entry exists.

Production already supplies the protected durable version key. Missing or invalid
keys continue to fail startup under ADR 0025. An embedding with no supplied
context uses the existing process context: reload equality works, but restart
gets an incomparable context and becomes cold. No silent key replacement occurs.
Backend/action cutover remains #624; cache-policy tightening and Vary security
are independently enforced and cannot be bypassed by matcher proof equality.

## Operator behavior

| Event | Behavior |
| --- | --- |
| Unchanged route / unrelated reload | Warm reuse; no manifest rewrite or fsync |
| Restart with same durable context and matcher | Warm disk reuse |
| Matcher edit while running or stopped | Affected route advances epoch and misses |
| Predicate/key rollback | New epoch; no resurrection of older entries |
| Identified route reordering | Warm reuse with correct ownership |
| Ambiguous anonymous same-path slot edit | Safe cold miss for affected slots |
| Legacy/missing matcher proof | Establish proof after invalidating unverifiable resident ownership |
| Malformed/oversized/unknown manifest format | Existing fail-closed recovery before disk callback attachment |
| Writes unavailable; evidence unchanged | Existing warm state stays reusable |
| Writes unavailable; compatibility changes | Required write fails, disk is retired/disabled, and later successful persistence repairs it |

Manifest version 1 carries epochs and active matcher proofs; legacy flat maps
remain readable for invalidation evidence. The existing 1 MiB read/write bound,
retired-epoch compaction floor and 64-variant RouteVary bound are preserved.
Older scope encodings incur a one-time cold miss. Physical erasure and recovery
when all durable filesystem operations fail remain subject to the documented
limits; the proof does not add a filesystem anti-replay guarantee.

## Five adversarial verification passes

1. **Reuse and identity:** unchanged memory/disk reuse, repeated reloads, actual
   child-process restarts, stable ID reordering, anonymous reordering isolation,
   unrelated insertion and batch/inspection identity agreement.
2. **Privacy:** predicate changes do not alter partition; independent key
   contexts change compatibility; manifest stores no raw predicate; proof and
   predicate are absent from response-entry files/keys. Existing confidential
   version key/domain machinery is reused. No public equality oracle is added.
3. **Invalidation and concurrency:** online/offline matcher changes, rollback,
   key changes, proof-only route retirement, deletion/recreation, existing epoch
   churn/compaction and blocked old fills. A changed route misses while its
   unrelated sibling remains warm. Focused full-tag race matrix passes.
4. **Lifecycle/failure recovery:** actual factory reload and reconstruction;
   aborted preflight preserves live cache; legacy/missing/corrupt/future/unknown
   field manifests fail closed; no-op durable commit does not require writes;
   changed proof requires persistence; repair retries despite unchanged state.
5. **Scope and engineering:** matching type/path/method/header/query/preflight
   changes affect compatibility; canonical reordering, route policy and #624
   backend changes do not change matcher proof. Contracts, lint/vet, package
   coverage, documentation and resource/performance checks retain existing gates.

Principal regressions: `TestMatcherCompatibilityWarmReloadAndRestart`,
`TestMatcherProofPrivacyContextAndPersistence`,
`TestMatcherIdentityReorderingAndUnrelatedInsertion`,
`TestMatcherProofTracksMatchingSemantics`,
`TestMatcherProofOnlyRouteRetirementPersists`,
`TestMatcherUnchangedReloadDoesNotRequireDiskWrite`,
`TestMatcherChangeFencesInFlightAndLeavesSiblingWarm`,
`TestMatcherManifestLegacyMissingAndCorruption`,
`TestRouteScopeRestartCompatibility`, and
`TestCacheMatcherFactoryWarmReloadPreflightAndRestart`.
The existing corrupt startup, epoch pruning, RouteVary bounds and security
HTTP/FastCGI/uWSGI regressions remain in the exercised suites.

### Recovery fault found during final verification

A further injected-fault pass against the initial warm implementation
`3912dca205817d568e1aa7f6aea3f22f80aa2b15` reproduced `HIT retired-body`:
epoch metadata was corrupt, unlink/truncate and tombstone persistence failed,
but epoch-manifest writes still worked. Cleanup removed catalog visibility;
new compatibility proof then reset unknown ownership to zero, allowing an old
physical file to return on a later healthy restart.

Recovery now retains a floor above observed decoded entries before cleanup.
Persisting fresh compatibility evidence waits for unrecoverable durable cleanup
to resolve, so a later successful epoch-file write cannot replace the corrupt
restart marker prematurely. Entry epochs exceeding the manifest's numeric bound
are rejected before recovery arithmetic. Retained regressions
`TestMatcherCorruptManifestIncompleteCleanupCannotResurrect` and
`TestDiskRouteEpochRejectsOverflowBeforeRecovery` pass in three repetitions, the
whole cache suite and the focused full-tag race run. The original reproduction
failed before correction. These changes concern metadata recovery; the warm
reuse and privacy architecture above is unchanged.

## Executed checks and limits

Go 1.27.3 linux/amd64, golangci-lint 2.14.0. Full tags:
`brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf`.

- Whole affected cache/app/admin/versionid tests pass in lean and full profiles.
  Full package coverage: cache 93.1%, app 83.3%, admin 87.8%, versionid 90.4%.
- Focused full-tag race matrix passes, including all matching/epoch regressions
  and retained HTTP/FastCGI/uWSGI identity tests.
- Move-aware changed-Go coverage for this follow-up: **122/124 = 98.39%**.
  Entire PR: **1,499/1,586 = 94.51%**, combining current affected profiles with
  previous-head profile data only for files whose source has not changed. Old
  coordinate data is excluded for every modified file. These local numbers do
  not replace final hosted Codecov PATCH >90%.
- Full-tag lint/vet, license, generated/config checks pass. Public config/API
  schema and frontend assets are unchanged; no generated file is hand-edited.
- `make ci-pr` was attempted. It reaches full tests and fails on the established
  environment restrictions (Unix socket creation and process resource probes),
  with no affected-package failure. The prior head has 63 successful hosted
  checks and two skips; this is baseline evidence, not new-head certification.
- Security negative tests and package floors pass unchanged: plugins 90.9%,
  RBAC 89.2%, WAF 84.4%. Documentation checks pass (2,552 checks), as do their
  unit/semantic-drift suites and historical D11 integrity check.
  Final hosted/native/browser checks and the independent reviews required by
  #672 must be evaluated for the newly published head. No merge is performed.

## Performance evidence

Same host, `GOMAXPROCS=2`, lean profile, six samples each, `-benchtime=200ms`.
Baseline then initial warm implementation (`3912dca205817d568e1aa7f6aea3f22f80aa2b15`)
were run sequentially. The subsequent correction changes metadata recovery and
epoch validation; the measured memory-hit paths are unchanged. Shared-host medians are advisory,
not production tail-latency or a statistically certified regression budget.
The HTTP benchmarks have identical fixtures on both revisions. The construction
benchmark now includes creation of the required private matcher proofs.

| Benchmark | Parent ns/op | Follow-up ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| `BenchmarkCacheHit` | 2,283.5 | 2,168.0 | 1256 → 1256 | 16 → 16 |
| `BenchmarkCacheVaryHit` | 3,772.0 | 3,543.5 | 1664 → 1664 | 35 → 35 |
| `BenchmarkRoutePolicyHTTPHit/cookies=false` | 2,301.5 | 2,373.0 | 1256 → 1256 | 16 → 16 |
| `BenchmarkRoutePolicyHTTPHit/cookies=true` | 2,677.5 | 2,570.5 | 1288 → 1288 | 17 → 17 |
| `BenchmarkRoutePolicyManyRoutes/10` | 22,005.5 | 40,993.5 | 18954 → 32307 | 138 → 373 |
| `BenchmarkRoutePolicyManyRoutes/1000` | 2,725,493.0 | 4,441,922.0 | 1905444 → 3384129 | 12928 → 35055 |

HTTP allocation counts are unchanged. HMAC derivation occurs at route compilation,
not per request. Rebuilding/proving 1,000 routes measured about 4.44 ms versus
2.73 ms for the previous implementation, while avoiding its unconditional cache
invalidation. Unchanged disk-backed commits also avoid manifest rewrite/fsync.
This follow-up does not remove or reclassify the original feature's additional
eligibility-check overhead relative to main; that evidence remains in the prior
review rather than being replaced by a different benchmark baseline.

Raw results: [parent](2026-10-29-pr673-warm-bench-before.txt),
[follow-up](2026-10-29-pr673-warm-bench-after.txt).

```sh
GOMAXPROCS=2 go test ./internal/cache -run '^$' \
  -bench '^(BenchmarkCacheHit|BenchmarkCacheVaryHit|BenchmarkRoutePolicyHTTPHit|BenchmarkRoutePolicyManyRoutes)$' \
  -benchmem -count=6 -benchtime=200ms
```

This is author-side verification. Five adversarial passes are not represented
as two independent final-head security/correctness reviews. The published head
and remaining gates are recorded in the PR description.
