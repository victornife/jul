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

## Maintainer review follow-up (`5b1e4c83` -> `4914ac91` -> final head) and independent final-head reviews

Reviewed baseline revisions:
- Performance optimization head: `5b1e4c8328b8cc57c5e74f63e4a051d179291e3a`
- First maintainer-review follow-up head: `4914ac918696ae0f983b9dfdede61d1843490eb5`
- Final corrected head: the commit containing this report (exact full SHA pinned in the PR #673 description)
- Fix author: `GitHub Copilot (Gemini 4 Argon)`

### 1. Maintainer review of `5b1e4c8328b8cc57c5e74f63e4a051d179291e3a`

1. **Generation-bound matcher eligibility (P1 fix):**
   - `RouteCacheConfig` binds an atomic generation (`boundGen`) when constructed via `HandlerForRoute` (or upon the next `CommitRoutePolicies` if constructed before commit).
   - `routeStateSnapshot.isRouteEligible` requires an active scope, matching `matcherProof`, identical route policy overrides (`sameRoutePolicyOverrides`), and `boundGen != 0 && boundGen == curGen`.
   - Any handler from a retired or superseded matcher generation receives `pol.RouteEpoch = incompatibleRouteEpoch` (`math.MaxUint64`), preventing both cache hits and cache publication across normal fills, coalesced fills, Range fills, and 304 validations (`isPolicyPublicationFencedLocked`).
   - Verified with `TestIndependent673OldSelectedHandlerCannotPublishIntoReplacementEpoch` (`identified=false/true`, `disk=false/true`, restart, Range requests, and `A -> B -> A` predicate transitions) and `TestCacheMatcherFactoryWarmReloadPreflightAndRestart`.

2. **Documentation alignment:**
   - Updated the cache threat note (`docs/cache.md`), inspection capabilities section (`docs/cache.md`), cookie-session limitation (`docs/known-limitations.md`), and `docs/adr/0029-route-scoped-cache-policy-and-vary-guards.md` to state that `Vary: Cookie` and `Vary: Authorization` are rejected and never reused (`#672`), and that route-scoped `bypass_cookies` / `bypass_any_cookie` bypass shared cache lookup and storage (`#623`), while retaining the explicit warning that cookies alone do not establish application safety when an origin emits cacheable responses without privacy directives or configured bypass.

### 2. Maintainer review of `4914ac918696ae0f983b9dfdede61d1843490eb5` (three P2 functional defects)

1. **P2 #1 — Inspection route-policy resolution for regex routes and fallbacks (`internal/admin/apiv1_cache_inspect.go`, `internal/router/explain.go`):**
   - Defect at `4914ac918696ae0f983b9dfdede61d1843490eb5`: `locationMatchesInspectionSelector` treated an untested regex (`^/assets/.*`) as a match on `/account` and discarded the subsequent `/` fallback.
   - Fix (`GitHub Copilot (Gemini 4 Argon)`): Added `router.PathCandidateIndices(srv, rawPath)` in `internal/router/explain.go` so `SelectorExactURL` and `SelectorKey` use the routing owner's exact four-tier enumeration (`exact` -> longest `prefix` -> compiled `regex` -> `/` fallback), and marked unshadowed `regex` / `exact` routes on non-exact selectors (`SelectorPrefix`, `SelectorGlob`, `SelectorTag`, `SelectorHost`, `SelectorAll`) as `uncertain = true` so untested regex or single-path exact policies are never asserted for broader scopes.
   - Regression test: `TestResolveInspectionRoutePolicyRegexAndFallbackSemantics` (`3` repetitions normal and `-race`).

2. **P2 #2 — Initial and candidate handler binding beyond 4,096 routes (`internal/cache/route_policy.go`, `internal/app/factory.go`):**
   - Defect at `4914ac918696ae0f983b9dfdede61d1843490eb5`: `registerPendingRouteHandler` truncated `pendingRouteHandlers` at `max(4096, len(c.routeConfigs)*2)`, dropping the first handler when 4,097 routes were constructed before initial `CommitRoutePolicies`.
   - Fix (`GitHub Copilot (Gemini 4 Argon)`): Removed the fixed truncation cap so every handler belonging to an accepted candidate is bound on `CommitRoutePolicies`, and bounded abandoned candidates through their lifecycle via `BeginRouteCandidate()` and `DiscardPendingRouteHandlers()` in `HandlerFactory.Build` and `HandlerFactory.Prepare`.
   - Regression test: `TestIndependent673InitialBindingBeyond4096Routes` (`3` repetitions normal and `-race`).

3. **P2 #3 — Matcher-generation compaction preserving active unchanged handlers (`internal/cache/route_policy.go`):**
   - Defect at `4914ac918696ae0f983b9dfdede61d1843490eb5`: `matcherGenOf` and `CommitRoutePolicies` raised an active unchanged route's generation to `routeEpochFloorKey` after >1,024 retired routes compacted (`1,026` reloads in construction-before-commit order), invalidating the unchanged route's handler binding.
   - Fix (`GitHub Copilot (Gemini 4 Argon)`): Preserved `c.routeMatcherGen[scope]` for active compatible routes (`s.matcherGenOf(scope)` returns `s.matcherGen[scope]`) while applying `routeEpochFloorKey` when assigning new generations to newly added, modified, or re-added retired scopes (`max(c.routeMatcherGen[scope], genFloor) + 1`).
   - Regression test: `TestIndependent673GenerationCompactionPreservesActiveHandler` (`3` repetitions normal and `-race`).

### 3. Twelve adversarial review passes and two independent final-head re-reviews

| Review ID / Agent Identifier | Reviewed Revision | Scope | Findings & Resolution Attribution (`GitHub Copilot (Gemini 4 Argon)`) |
| --- | --- | --- | --- |
| `Explore-Adversarial-01` | Post-`4914ac91` working tree | Matcher generation binding & candidate lifecycle | Found that an aborted candidate for an already-active compatible route could retain a prebound generation if invoked after abort. Fixed via `BeginRouteCandidate()` + `DiscardPendingRouteHandlers()` zeroing `boundGen`. |
| `Explore-Adversarial-02` | Post-`4914ac91` working tree | Epoch/generation compaction & resurrection | No defect found; verified multi-compaction active handler preservation, ABA resurrection fencing, and `routePolicyFloor` compaction during in-flight fills. |
| `Explore-Adversarial-03` | Post-`4914ac91` working tree | Admin cache inspection & router semantics | Found `exact` location over-assertion on `SelectorPrefix` and cross-listener wildcard/default host scoring in `resolveInspectionRoutePolicy`. Fixed via `uncertain = true` for `exact` on non-exact selectors and per-listener `inspectionServerHostScore`. |
| `Explore-Adversarial-04` | Post-`4914ac91` working tree | Sensitive `Vary` (`Cookie` / `Authorization`) & non-retention | Found non-canonical `http.Header["vary"]` map keys could bypass `h.Values("Vary")`. Fixed case-insensitive `"Vary"` map scan in `sensitiveVaryHeader`, `varyRejectionHeader`, and `varyFields`. |
| `Explore-Adversarial-05` | Post-`4914ac91` working tree | Proxy, FastCGI, and uWSGI transformed-header `Vary` isolation | Verified `ProxyTransformedRequestHeaders` and `CGITransformedRequestHeaders` bounds and `Connection`-nominated header handling; no defect found. |
| `Explore-Adversarial-06` | Post-`4914ac91` working tree | Request-cookie bypass & RFC 6265 parsing | Found `textproto.TrimString(part[eq+1:])` normalized leading whitespace after `=` (`Cookie: theme= 1`) and `r.Header.Values("Cookie")` missed non-canonical `"cookie"` map keys. Fixed in `evaluateCookieBypass`. |
| `Explore-Adversarial-07` | Post-`4914ac91` working tree | Freshness precedence, `max_ttl`, SWR, SIE & policy memo | Found `sameRoutePolicyOverrides` and `CommitRoutePolicies` floor check omitted `varyGuard` changes. Fixed via precomputed 16-byte `varyTransformPolicy.digest` and `sameVaryGuard`. |
| `Explore-Adversarial-08` | Post-`4914ac91` working tree | Concurrency, coalesced fills, Range requests & 304 validation | No defect found; verified lock ordering (`genMu`, `disk.mu`, `mem.mu`, `fillMu`), follower re-evaluation, Range epoch checks, and 304 fencing. |
| `Explore-Adversarial-09` | Post-`4914ac91` working tree | Disk store persistence, corrupt manifests & fault recovery | No defect found; verified 1 MiB manifest limit, `RouteEpoch >= math.MaxInt64` and `len(RouteVary) > 64` bounds, startup sensitive-`Vary` scrub, and `routeEpochFailed` recovery. |
| `Explore-Adversarial-10` | Post-`4914ac91` working tree | Config validation, patch engine, selector & CLI | Found `loc.Plugin` (handler plugin) vs `loc.Plugins` (middleware) check and 100-year duration cap in `validateLocationCachePolicy`, `BypassCookies` canonicalization in `buildLocationCachePolicy`, `SelectorKey` + `RouteID` single-host check in `selector.go`, and `--operation-id` + `--route-id` check in `remote_cli.go`. All fixed. |
| `Explore-Adversarial-11` | Post-`4914ac91` working tree | Console UI, draft validation, RBAC & accessibility | Found Greek small letter mu (`μs`) in `cachePolicyDraft.ts` `GO_DURATION_RE` and expanded `smoke.spec.ts` `aria-invalid` assertions to cover `stale_while_revalidate` and `stale_if_error`. Fixed and rebuilt Console assets. |
| `Explore-Adversarial-12` | Post-`4914ac91` working tree | Documentation, ADR 0029, known limitations & D11 inventory | Found split sentence in `docs/cache.md`, clarified `max_ttl = "0s"` non-retention for conditional `304` and `0s..876000h` duration ceiling in `docs/cache.md` and `ADR 0029`, and added review provenance table here. |
| `Explore-Security-Lifecycle-Reviewer-A` (Independent Final-Head Review #1) | Post-fix final tree (parent `4914ac918696ae0f983b9dfdede61d1843490eb5`) | Independent security, privacy, and matcher/compaction lifecycle re-review | **Verdict: PASS (0 blocking defects).** Verified P2 #1, P2 #2, P2 #3 fixes and all security/privacy adversarial fixes; added explicit regression assertions for non-canonical `vary`/`cookie` map keys and `varyGuard` tightening on reload. |
| `Explore-Correctness-Surface-Reviewer-B` (Independent Final-Head Review #2) | Post-fix final tree (parent `4914ac918696ae0f983b9dfdede61d1843490eb5`) | Independent correctness, router/inspection, API/CLI, UI, and documentation re-review | **Verdict: PASS (0 blocking defects).** Verified `PathCandidateIndices`, `inspectionServerHostScore`, `SelectorKey` + `RouteID`, config validation, Console UI, and `0s..876000h` duration documentation. |
