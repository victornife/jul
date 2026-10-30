# PR #673 — measured performance improvements

Date: 2026-10-30. Baseline: `6888e584faa6f59d70f8d30970bd8bb50ff1a5ad`.
The containing commit holds the optimized source; the PR description pins its SHA.
This supplements the [warm-reuse review](2026-10-29-pr673-warm-reuse-review.md)
and its original feature-to-main evidence.

## What changed and why

A CPU/allocation profile of `BenchmarkCacheVaryHit` attributed 8.56% of sampled
CPU time to policy hashing and about 28% of allocated objects to `parseList`.
The retained changes remove repeated work without removing a safety check:

- Scan Vary members with `strings.SplitSeq`, preserving trimming, empty-member
  handling, malformed/sensitive rejection and existing complexity limits.
- Memoize policy and cookie-policy digests in one immutable snapshot per HTTP
  handler. Compare the **entire effective policy value** before reusing it.
  Global changes, route epochs and all cookie rules participate automatically;
  no manually maintained invalidation key or persistent state is introduced.
  A concurrent request holds its own matching snapshot even if another request
  replaces the memo. The memo contains configuration only, with no request
  headers or cookie values. Replaced snapshots remain live only while referenced.
- Normalize/sort the server boundary once per `RouteScopes` batch, then append
  each route's public coordinates to reusable bytes. Replace formatted decimal
  framing with direct byte appends for scopes and private matcher proofs.
  HMAC remains at compilation; scope/proof encodings are byte-for-byte compatible.

The memo adds one bounded policy snapshot per active handler. It does not add an
entry per response/request or retain historical policies in a registry. Range
and fill-follower paths retain their existing policy snapshots and use the same
decision implementation. Request cookie parsing, ownership/epoch matching,
Authorization checks, Vary transformations and publication fences remain live.

## Same-host comparison

Go 1.27.3 linux/amd64, AMD EPYC 9V74, lean tags, `GOMAXPROCS=2`.
Six samples per benchmark, 300 ms each, in sequential **before/after/after/before**
blocks of three samples. No other test, lint or profile ran concurrently with
these final measurements. Values are medians on a shared host, not production
tail latency or a statistically certified regression budget.

| Benchmark | Before ns/op | After ns/op | Median change | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: | ---: |
| `CacheHit` | 2,232 | 1,817 | −18.6% | 1256 → 1256 | 16 → 16 |
| `CacheMiss` | 18,033 | 17,717.5 | −1.7% | 13158 → 13144 | 79 → 79 |
| `CacheVaryHit` | 3,762.5 | 3,135.5 | −16.7% | 1664 → 1504 | 35 → 25 |
| `RoutePolicyHTTPHit/cookies=false` | 2,351.5 | 2,005 | −14.7% | 1256 → 1256 | 16 → 16 |
| `RoutePolicyHTTPHit/cookies=true` | 2,595 | 2,136 | −17.7% | 1288 → 1256 | 17 → 16 |
| `RoutePolicyManyRoutes/10` | 42,158 | 29,472 | −30.1% | 32307 → 28880 | 373 → 194 |
| `RoutePolicyManyRoutes/1000` | 4,368,549 | 3,100,724 | −29.0% | 3383635 → 3015285 | 35055 → 17053 |

Treat the small miss-time change as flat/noise. The hit and construction
improvements are supported by the removed work and repeatable allocation counts;
these percentages are measurements, not a guarantee for every deployment.
All ten allocations added by the Vary guards are removed. Allowed-cookie hits
also avoid allocating/recomputing their unchanged bypass digest. Construction
of 1,000 routes uses 51.4% fewer allocations and 10.9% fewer allocated bytes.

Raw results: [before](2026-10-30-pr673-perf-before.txt),
[after](2026-10-30-pr673-perf-after.txt).

```sh
GOMAXPROCS=2 go test ./internal/cache -run '^$' \
  -bench '^(BenchmarkCacheHit|BenchmarkCacheMiss|BenchmarkCacheVaryHit|BenchmarkRoutePolicyHTTPHit|BenchmarkRoutePolicyManyRoutes)$' \
  -benchmem -count=3 -benchtime=300ms
```

## Remaining cost relative to main

A separate same-host, six-sample 200 ms run at main
`cbd647a9b144c4458c43d064f9e71f141a7e338c` uses the unchanged common HTTP fixtures:

| Benchmark | Main ns/op | Optimized PR ns/op | Additional median CPU |
| --- | ---: | ---: | ---: |
| `CacheHit` | 1,433 | 1,817 | 384 ns |
| `CacheVaryHit` | 2,152 | 3,135.5 | 983.5 ns |
| `CacheMiss` | 18,134 | 17,717.5 | Within shared-host noise |

Raw [main results](2026-10-30-pr673-perf-main.txt). Main was measured separately,
so this comparison has weaker control for time-dependent host load than the
interleaved parent comparison. Hit allocations now equal main: 16 plain and 25
Vary. Extra eligibility/ownership work still has measurable CPU cost. This is
not a zero-overhead claim or proof that no further optimization exists. The
remaining checks preserve the feature's contract; changing them needs fresh
profiling, a demonstrated benefit and equivalent adversarial verification.

## Correctness and adversarial verification

1. **Persistent compatibility:** parent-generated golden scopes and matcher
   proofs cover anonymous/identified routes, anonymous slots, default/explicit
   match type, normalized server names, IPv6, Unicode, delimiters, long inputs,
   nil/empty methods and preflight widening. Existing actual-process warm
   restart, matcher/key changes and retirement/churn tests remain exercised.
2. **Parser/security equivalence:** differential fuzzing compares streaming
   Vary scans with the former allocating parser, including malformed members,
   sensitive fields, Connection nominations and CGI aliases. Full sensitive
   Vary, cookie, ownership and HTTP/FastCGI/uWSGI suites remain exercised.
   The 10-second fuzz run completed 105,365 executions without a mismatch.
3. **Policy tightening:** a live handler first hits, then misses after a tighter
   global TTL, for both global and route wrappers. The policy completeness test
   checks memo refresh for every field and its cookie-policy digest.
4. **Concurrency/resources:** eight workers alternate immutable snapshots and
   assert both returned digests. Focused full-tag race tests cover the memo,
   matching, epochs, cookies, Vary and retained factory/isolation regressions.
   Retention is one atomic memo reference per handler, independent of churn.
5. **Engineering/performance:** affected cache/app suites pass in lean and full
   profiles; full-tag lint passes. Current full package coverage is cache 93.1%
   and app 83.2%. Move-aware changed-Go coverage is **75/75 = 100%** for this
   optimization, using the current full-tag profile, not old source coordinates.

Tests added: `TestRouteEncodingCompatibility`,
`FuzzVaryListScanCompatibility`, `TestPolicyIdentityMemoConcurrentSnapshots`,
and `TestHandlerIdentityMemoDoesNotMaskPolicyTightening`.
These are author-side passes, not independent final-head reviews. Parent
`6888e58` completed 63 successful hosted checks and two skips. The optimized
head still requires new hosted CI/Codecov results and independent reviews before
merge; no merge is performed.
