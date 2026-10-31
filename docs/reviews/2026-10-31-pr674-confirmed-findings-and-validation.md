# PR #674 corrective review and validation — 2026-10-31

Reviewed baseline: `86432bd9b34ee88f1f50d983b88d411fa63a2e61`.
Base: `fddc34562a18d4047c27a6e1bcbe9f78917bf304`.
This record supersedes the earlier Slice 1 completeness claims; it does not
claim release, GA, production soak or hardware power-loss certification.

## Findings and fixes

| Feedback | Verification | Corrective behavior |
| --- | --- | --- |
| P1 cutoff/identity commit gap | Reproduced exact-URL and key targets surviving a snapshot at CommitFence; prefix/all controls passed. | Persist executable private intent before cutoff exposure. Atomically include the same identity in the admin ledger cutoff write, covering the journal-completion/terminal-ledger gap too. Ledger-only recovery uses executable identity, never redacted query text. |
| P2 aggregate journal capacity flush | Four retained 4,096-ID intents exceeded 1 MiB and removed an unrelated disk file on the baseline. | Atomically check count and encoded aggregate bytes before committing a cutoff. Reject with 503/backpressure guidance, preserving disk entries and the prior journal. New admissions never coalesce tag snapshots or retained operations into broader scopes. |
| P2 key recovery broadens to HEAD | Baseline recovery removed HEAD while the live GET-key purge preserved it. | Separate key/representation matching from exact-URL resource-group matching. Preserve HEAD, Vary siblings and stubs outside the selected key, including rotated-version-key recovery. |
| Partial performance evidence | Confirmed: synthetic os.WriteFile was not a durable ledger commit; prior 200k concurrent profile was memory-only; no enforced latency budgets. | Remove the misleading synthetic ledger callback. Add actual admin/ledger commits, real disk catalogs, explicit local acceptance budgets, and recovery at both journal limits. |
| Console consumer and generic failure | Confirmed the duplicate shortcut called the unversioned endpoint and collapsed errors. | Remove that shortcut/client helper and retire the unsupported HTTP route/handler/adapter in this PR. All Console purges use the existing versioned preview/confirm/execute/observe panel. Migrate OIDC/RBAC/CSRF/audit/durability tests to v1; rebuild embedded assets. |
| ADR allocation claim | Confirmed the historical report said about 17.4 MB, not <5 MB. | Correct ADR 0030 and mark the old evidence report superseded. |

Retries retain their original normalized recovery identity and target hashes,
even if live catalog generations advance. Recovery uses immutable original tag
snapshots. An independent journal mutex avoids taking the disk-item mutex while
holding the generation lock; actual I/O-failure cleanup runs after generation
unlock. Older unverifiable ledger identities explicitly log and sweep disk
entries at their original cutoff, preserving post-cutoff fills, rather than
reporting success after matching nothing. Actual corrupt-metadata/I/O failures
retain the documented fail-closed policy.

## Two adversarial audit passes

Both passes were performed by the same reviewer from different perspectives;
they are not represented as independent reviewers.

1. **Durability, identity and selector audit:** trace admission → journal write
   → atomic ledger cutoff+identity → fence → deletion/tombstones → journal
   completion → terminal ledger; exercise snapshots at the cutoff and
   ledger-only fallback, redacted queries, GET versus HEAD, Vary ownership,
   version-key rotation, legacy missing/unknown identity, repeated restarts,
   retry identity retention, count/byte rejection before callback, cancellation,
   sensitive-Vary retirement and post-cutoff/unrelated controls. No unresolved
   findings in the corrected implementation.
2. **Concurrency, bounds and acceptance audit:** check generation/intent/item
   lock ordering, in-flight disk publication, concurrent purges and original
   cutoffs, failed/canceled cleanup, active readers, health/error mapping,
   Console RBAC/preview/retry behavior, actual durable persistence, aggregate
   recovery limits, generated assets/contracts and documented claims. Affected
   race tests passed three repetitions after corrections. No additional
   concrete defects or regressions found within this scope.

Focused regression files: `internal/cache/purge_commit_regression_test.go`,
`internal/admin/cache_purge_commit_test.go`, and the existing cache/admin
invalidation and retirement suites.

## Measured performance acceptance

Environment: Linux 6.18.57, amd64, Go 1.27.3, `GOMAXPROCS=2`, container-local
filesystem. Hardware model was unavailable. These are local regression budgets,
not portable deployment SLOs or guarantees about network/distributed filesystems.
Budgets are fixed in the tests rather than inferred from a passing sample.

```sh
RUN_667_DURABLE_PROFILE=1 GOMAXPROCS=2 go test ./internal/admin \
  -run TestMeasure667DurableAdminBudgets -v -count=1
RUN_667_DURABLE_PROFILE=1 GOMAXPROCS=2 go test ./internal/cache \
  -run TestMeasure667RecoveryJournalBudgets -v -count=1
```

The admin profile seeds 1,000 and 200,000 real resources through the HTTP cache
handler, including sparse tags, separate HEAD entries and Vary representations.
It counts real disk files before purge and asserts every targeted disk entry is
removed. It runs concurrent unrelated warm requests and misses/publications.
The callback invokes the actual `persistPurgeLedgerLocked` implementation:
JSON serialization, atomic file write, file sync, rename and directory-sync
attempt. This is not a synthetic payload or no-op ledger hook.

| 200k-resource disk/admin sample | Cutoff commit | Actual ledger commit | Admin total | Warm-hit p95 | Miss/publication p95 |
| --- | ---: | ---: | ---: | ---: | ---: |
| 1 | 142.406 ms | 50.707 µs | 2.912 s | 23.255 µs | 14.812 ms |
| 2 | 154.019 ms | 51.367 µs | 2.924 s | 18.869 µs | 14.777 ms |
| 3 | 207.869 ms | 49.865 µs | 4.634 s | 35.003 µs | 34.276 ms |
| Enforced local budget | 2 s | included in cutoff | 30 s | 10 ms | 1 s |

All three 1k-resource samples also passed (7–9 ms total). Cutoff timing covers
initial selection, journal persistence and the ledger cutoff callback; it is
not instrumentation of every later generation-lock acquisition. Warm samples
numbered 3,969–4,763 and publication samples 456–503 in the large runs. The disk
filesystem is local/ephemeral; repeat on target deployment storage before
promising those measured timings to operators.

Recovery includes rehydration, selector evaluation, tombstones, unlink and
journal cleanup for 10,000 real disk entries. It tests the limits independently:

| Retained journal | Encoded bytes | Three startup measurements | Enforced local budget |
| --- | ---: | --- | ---: |
| 256 compact operations | 27,563 | 0.901 / 1.504 / 1.692 s | 10 s |
| Four large tag snapshots | 1,045,617 (99.72% of 1 MiB) | 1.324 / 2.064 / 2.317 s | 10 s |

The byte and operation maxima are shared independent constraints, not a promise
that 256 largest snapshots fit. A 4,096-ID capacity regression separately proves
safe rejection at aggregate exhaustion. Startup numbers do not claim a combined
200k-entry/256-intent worst-case budget. Retain the current single generation
owner with bounded admission and batching; these measurements do not justify a
more complex invalidation engine.

## Validation and limits

- Full cache/admin suites pass; package statement coverage: cache 92.8%, admin
  87.8%; app coverage 81.9%.
- `go test -race ./internal/cache ./internal/admin -run 'Purge|Retire|SensitiveVary'
  -count=3` passes after corrections.
- Repository changed-Go block gate (including app, cache and admin; no package
  allowlist or lowered floor): **700/764 = 91.62%**, floor 90%. Hosted Codecov
  patch is a distinct measurement and must be checked on the pushed head.
- Console: typecheck, lint, reporter tests, full coverage contract and build
  pass; **86 files / 1,002 tests**. Statements 81.88%, branches 76.57%, functions
  73.21%, lines 83.29%; all required coverage-contract targets pass.
- `go vet ./...` and generated-contract checks pass. Embedded Console assets
  are rebuilt from a local dependency tree with reproducible relative maps.
- Lean/full repository tests were attempted. Unix-socket listeners are denied
  by this execution environment (`socket: operation not permitted`), so those
  handler tests and two importer/NGINX corpus tests could not pass locally.
  The other package lanes passed; full-tag CLI passed with those socket-bound
  corpus tests excluded. Hosted Linux/macOS/Windows, full race, browser/E2E and
  final Codecov remain merge gates, not claimed from these local results.

## Maintainer-selected HTTP retirement in this PR

The maintainer selected direct retirement in #674 after the Console migration.
`POST /cache/purge`, its `?key=` form, handler, unused `ResultPurger` adapter,
route classification and transport inventory are removed. There is no alias,
extra issue or deferred HTTP cleanup. ADR 0019 explicitly classifies the old
route as internal/unsupported and free to change, so a public-API deprecation
window does not apply. Update any old scripts to `jul cache purge` or the
versioned preview/execute/status flow.

OIDC, CSRF, RBAC, audit-attribution and durability tests now exercise the
supported versioned endpoint with real structured request bodies. Completed
degraded outcomes are HTTP 200 with explicit terminal failure/uncertainty and
recovery guidance; pre-commit capacity rejection remains a 503 envelope. A
negative regression covers retired POST/key/trailing-slash routes, alternate
mutation methods, token/open modes and enabled/disabled Console builds. The UI
fallback accepts browser navigation only, preventing a removed mutation route
from receiving an HTTP 200 SPA shell.

Keep internal `Cache.Delete()` / `DeleteResult()` and `Purge()` / `PurgeResult()`.
These are Go cache operations, not exposed endpoints or duplicate legacy
engines. Unsafe-method invalidation needs `Delete()` to retire GET/HEAD/Vary
representations; it already delegates to the current durability machinery.

Cleanup audit pass 1 checks route removal, no-mutation controls, v1 success,
RBAC/CSRF and audit attribution. Pass 2 checks durability faults, internal
unsafe-method invalidation, route inventory/contracts, docs and lean/full
Console fallback. No unresolved cleanup findings remain after validation.

## Same-PR cleanup verification

- Repository Go tests pass with Unix-socket tests excluded only for the local
  environment restriction. Affected full-tag cache/admin/app/API-contract
  suites pass without that exclusion.
- Lean and full Console tests verify removed mutations return 404 and never
  invoke cache deletion, while the versioned replacement still executes.
- Versioned durability fault tests retain marker-only health/restart controls
  and uncertain single-key outcomes with recovery guidance and failure audit.
- Console-enabled OIDC/auth/purge/invalidation race tests pass three repetitions.
- Frontend typecheck/lint and 35 targeted cache/retry/OIDC unit tests pass.
  Production browser bundle sources are unchanged by this cleanup.
- Real-provider OIDC browser tests were attempted with Playwright and system
  Chromium. This environment blocks Chromium's process-singleton Unix socket
  (`Operation not permitted`); no browser assertions could run locally. Hosted
  OIDC/browser checks remain required on the published head.
- Full-tag vet, generated contracts and 2,574 documentation checks pass.
- Repository/embedded-asset/script/example searches find no remaining product
  caller of the retired endpoint; remaining literal paths explain retirement,
  describe historical behavior, or assert its absence.

## Historical tag-snapshot recovery correction

The later review of `53910ced` identified a confirmed P2 missed by the earlier
passes: the ledger fallback unconditionally broadened every historical tag
operation to all pre-cutoff disk entries, even when the original snapshot was
valid. Populated snapshots removed unrelated same-host and other-host warm
entries; explicit empty snapshots incorrectly removed their zero-target scope
and unrelated entries. Both reproduced in three normal and three race runs.

The correction removes that unconditional tag-kind fallback. Existing intent
validation already distinguishes non-nil valid snapshots (including `[]`) from
absent/null or invalid IDs. Valid snapshots now reconcile only their original
resource set; broader fallback remains for genuinely unavailable/invalid or
unverifiable identity.

`TestLegacyTagLedgerRecoveryUsesOriginalSnapshot` covers populated, explicit
empty, absent, null and malformed snapshots; host-scoped selectors; running and terminal-uncertain historical records; GET/HEAD targets; unrelated
same-host and other-host entries; post-cutoff fills; and two successive boots.
Tag ingestion is disabled to prove ownership comes from original IDs rather
than current tag membership. The populated/empty cases preserve unrelated warm
entries, while missing/invalid controls retain the conservative cutoff fallback.

All 10 focused regression cases pass three normal and three race repetitions
after the correction. Cache/admin/app suites pass in lean/full builds, affected
Console-enabled race tests pass three repetitions, vet passes, and every step
of the docs-check workflow passes locally. Changed-Go coverage is 700/764
(91.62%); final-head hosted checks remain required before merge.

## Recommendation

The reproduced runtime defects, including the later historical tag-snapshot
finding, have targeted corrections and passing regression coverage. The earlier
audit passes did not exhaustively establish the absence of further defects. Recommend
merge only after the final pushed head passes the hosted checks and maintains
the requested >90% patch coverage; do not reuse the old head's green CI result.
