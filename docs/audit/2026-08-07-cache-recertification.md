# Response-cache recertification — 2026-08-07

## Decision

The response cache retains **GA** after the #131/#132/#133 correction programme
and the #134 integrated recertification. The fresh source audit found no
unresolved cache P0/P1 correctness defect. Remaining constraints are documented
as product, performance, intentionally conservative or lifecycle limitations.

## Audited scope

The audit re-read middleware order, key/Vary membership, immutable entry
publication, memory/disk tiers, generation leases, synchronous/background
validation, authorization/private responses, unsafe invalidation, Range/upgrade/
stream bypass, purge/delete/admin paths, reload/shutdown, metrics/result headers,
and disk permissions/foreign-file handling. Child-issue conclusions were treated
as hypotheses and checked against the merged source and executable tests.

## Executable evidence

The authoritative row-by-row matrix is in [cache.md](../cache.md#executable-behaviour-matrix).
It covers keying, methods/statuses, request/response directives, mandatory
validation, SWR/SIE, Authorization and Set-Cookie, Vary membership/invalidation,
validators/304, Range, WebSocket/101, SSE/flushing/oversize, two-tier persistence,
entry immutability, generation/reload lifetime and real H1/H2/admin paths.

### Canonical and repeated suites

Audit bootstrap workflow `31160045746` ran these steps successfully on the
post-#236 base before its final artifact-packaging step failed for an unrelated
archive-recursion error:

```text
go test -count=1 -v ./internal/cache ./internal/middleware ./internal/handler ./internal/server ./internal/app

go test -race -count=1 -p 2 -tags "console grpc wasmplugins" -v ./internal/cache ./internal/middleware ./internal/handler ./internal/server ./internal/app

go test -race -count=5 -p 2 -run '<cache lifecycle/protocol regression set>' -v ./internal/cache ./internal/handler ./internal/server

python3 scripts/docs-check.py
python3 scripts/test_docs_check.py
```

The repeated set included revalidation churn and replacement, blocked reload,
forced retirement/drain, client disconnect, WebSocket/upgrade, SSE and mandatory
validation during reload. The packaging-only failure did not invalidate the
completed test, race, repetition, benchmark or docs steps.

### Focused benchmark and soak gate

Workflow `31163489042` on
`3a4c982ed42cabaf608de771492402897f2dffac` passed formatting, focused cache
unit tests, focused cache race, six benchmarks and the dedicated cache soak.

Benchmark command:

```text
go test -run '^$' -bench='BenchmarkCache.*' -benchmem -benchtime=100x -count=5 ./internal/cache
```

Median values and allocation data are recorded in [cache.md](../cache.md#benchmarks).

Soak command:

```text
SOAK_SCENARIO=cache SOAK_DURATION=30s SOAK_WORKERS=16 scripts/soak.sh
```

Result: 422,042 requests, zero errors, all HIT/MISS/STALE/REVALIDATED/BYPASS
classes observed, 37,884 deliberate origin 5xx responses handled by
stale-if-error, no stranded call state, bounded memory/disk usage, and decreasing
goroutine/FD counts. Full numbers are in [soak-evidence.md](../soak-evidence.md#2026-08-07--cache-recertification-correctness-soak-linux-30-seconds-16-workers).

## Metrics decision

The released `jul_cache_events_total` family and frozen v1.32.0 help text remain
unchanged; `REVALIDATED` is an additive bounded label value. The unreleased
`jul_cache_revalidations_total` help text now truthfully describes both
synchronous validation and background revalidation. Its contract state remains
`merged_release_pending`; recertification is not itself a release.

## Residual risk and handoff

- No distributed cache, tag/prefix purge or cached byte-range serving is added.
- Conservative Set-Cookie, Authorization/cookie-session, changed-Vary 304,
  legacy-stub, variant-cap and validation-buffering rules remain explicit.
- #92 may proceed only after its #89/#90 lifecycle dependencies; it must preserve
  one immutable policy snapshot per operation and perform no eviction before
  Publish.
- #93 remains draft/gated. Closing the cache correctness epic does not authorize
  cache enable/disable or disk-path generation replacement.
- Exact PR-head and merged-main workflow IDs are recorded in #134 completion
  evidence, which is the stable closure record for the final repository SHA.
