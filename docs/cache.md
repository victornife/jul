# Response cache

Jul.IA can cache upstream responses in front of proxied backends and FastCGI/uWSGI
apps. The cache is **two-tier**: a fast in-memory tier backed by an optional
on-disk overflow tier that survives process restarts. Both tiers are part of the
core binary — there is no build tag to enable.

The cache is opt-in per location (`cache = true`) and gated by the global
`[cache].enabled` switch. Cacheability follows the shared-cache rules of RFC 9111
and RFC 5861; the exact contract, including every deliberate conservative
choice, is written out in [Shared-cache contract](#shared-cache-contract). The
cache was recertified by #134 after the #131/#132/#133 correction programme.
The final decision is to retain GA: no unresolved cache P0/P1 correctness defect
was found, and the executable matrix, race/protocol suites, focused soak and
benchmarks below were refreshed against the corrected implementation.

## Contents

- [Two-tier model](#two-tier-model)
- [Configuration](#configuration)
- [Cache key and Vary](#cache-key-and-vary)
- [Cache result values](#cache-result-values)
- [Shared-cache contract](#shared-cache-contract)
- [Freshness and stale-while-revalidate](#freshness-and-stale-while-revalidate)
- [Background revalidation lifecycle](#background-revalidation-lifecycle)
- [Entry immutability](#entry-immutability)
- [Upgrades, streaming and ResponseWriter transparency](#upgrades-streaming-and-responsewriter-transparency)
- [On-disk format](#on-disk-format)
- [Disk-tier safety](#disk-tier-safety)
- [Eviction and overflow](#eviction-and-overflow)
- [Operations](#operations)
- [Deployment](#deployment)

## Two-tier model

| Tier | Backing store | Bound | Lifetime |
| --- | --- | --- | --- |
| Memory | in-process map, LRU by bytes | `memory_max_size` | process lifetime; survives reloads |
| Disk (optional) | files under `disk_path`, LRU by bytes | `disk_max_size` | survives restarts |

A request is served from the memory tier first. On a memory miss the disk tier is
consulted; a disk hit is **promoted** back into the memory tier so subsequent
reads stay hot. When the memory tier overflows, evicted entries spill **down** to
the disk tier (when configured) rather than being dropped, so disk acts as a
larger, slower backstop.

The cache instance is shared across configuration reloads, so a reload does not
cold-start the cache.

## Configuration

See the [`[cache]`](../README.md#cache) reference for the full key list. A typical
block with the disk overflow tier enabled:

```toml
[cache]
enabled                = true
memory_max_size        = "256MB"
disk_path              = "/var/cache/jul/http"   # enables the disk tier
disk_max_size          = "4GB"
default_ttl            = "5m"
stale_while_revalidate = "30s"
stale_if_error         = "300s"   # keep serving stale on upstream 5xx
```

Per-location opt-in is still required:

```toml
[[servers.locations]]
path  = "/static/"
cache = true
```

## Cache key and Vary

The cache key is derived from the request method, the lowercased host, and the
request URI — and from nothing else. No credential, cookie or other request
header is ever part of a key; reuse restrictions are enforced by the stored
entry's recorded policy instead, because a credential-derived key would silently
turn a leak into an unbounded cache.

When an upstream response carries a `Vary` header, each combination of the varied
request-header values is stored as a **distinct variant** under its own key, so
(for example) `Vary: Accept` keeps the JSON and XML representations of one URL
cached at the same time instead of overwriting each other. A pointer entry under
the base key records both the varied field names and the **membership list** of
every variant key the resource owns.

Membership is authoritative on lookup: a variant key the base entry does not
claim is a miss, even if the store still holds something under it. That is what
lets unsafe-method invalidation remove *every* variant, and what stops a deleted
variant from becoming reachable again through a rebuilt pointer entry. A pointer
entry written by a Jul older than the membership record claims nothing, so it
fails closed — one extra miss per `Vary` URL after an upgrade, never a stale or
unaccounted hit.

A request whose varied values match no stored variant is a miss; `Vary: *`
responses are never reused. Membership is capped at 64 variants per base
resource; past the cap the oldest variant is deleted with its membership entry,
so a pathological `Vary` cannot grow one record without bound.

## Cache result values

Every response reports its disposition in the `X-Cache` header. The set is closed:
the same values are the `state` label of `jul_cache_events_total`, so nothing
request-derived may ever appear here.

| `X-Cache` | Meaning |
| --- | --- |
| `MISS` | The body came from the origin. Stored if the shared-cache rules allow it |
| `HIT` | Served from a fresh stored entry without contacting the origin |
| `STALE` | Served from a stored entry past its freshness lifetime, under `stale-while-revalidate` or `stale-if-error` |
| `REVALIDATED` | The origin confirmed the stored entry with a `304` during this request, and the confirmed copy was served |
| `BYPASS` | The cache was not consulted at all (protocol upgrade, `Range`/`If-Range`, or request `no-store`) |

> The `jul_cache_events_total` **help string** still reads
> `(HIT/MISS/STALE/BYPASS)`. That text is frozen by the v1.32.0 released metric
> contract and is intentionally preserved; `REVALIDATED` is an additive label
> value, which the released contract does not freeze. The separate release-pending
> `jul_cache_revalidations_total` description covers both synchronous validation
> and background revalidation.

## Shared-cache contract

This section describes tested behavior. Every rule below is pinned by a named
test in `internal/cache` (unit and policy matrices) or `internal/handler`
(real origin, real proxy hop, real client).

### Request directives

| Request `Cache-Control` | Behavior |
| --- | --- |
| `no-store` | Bypasses lookup **and** storage. `X-Cache: BYPASS`. It is not a purge: an entry another client stored is left exactly as it was |
| `no-cache` | The stored entry may be reused only after a **successful synchronous validation**, however fresh it is. `stale-while-revalidate` is never a substitute |
| `max-age=0` | Handled identically to `no-cache`: no stored response can be zero seconds old |
| `Pragma: no-cache` | Honored as `no-cache`, but **only** when the request carries no `Cache-Control` at all (RFC 9111 §5.4) |
| `min-fresh`, `max-stale`, `only-if-cached` | **Not supported.** Parsed and then ignored entirely. Honoring part of a directive is worse than not honoring it, because the client cannot tell the difference |
| Any other extension | Ignored |

### Response directives

| Response `Cache-Control` | Stored? | Reuse |
| --- | :---: | --- |
| `no-store` | no | — |
| `private` | no | Jul is a shared cache; a private response belongs to one user agent |
| `public` | yes | Normal, and explicitly shareable with authenticated requests |
| `s-maxage=N` | yes | Freshness lifetime `N`; outranks `max-age` and `Expires` |
| `max-age=N` | yes | Freshness lifetime `N`; outranks `Expires` |
| `no-cache` | **yes** | Stored, but **every** reuse requires successful synchronous validation. Storing it is the point: a `304` still saves the body |
| `no-cache="Header"` | yes | Treated as unqualified `no-cache`. Selective header replacement is a separate design; validating the whole representation is its conservative superset |
| `must-revalidate` | yes | Never served stale. Outranks `[cache] stale_while_revalidate` and `[cache] stale_if_error` |
| `proxy-revalidate` | yes | Identical to `must-revalidate` for Jul, which is a shared cache |
| `stale-while-revalidate=N` | yes | Replaces the global stale window for this entry |
| `stale-if-error=N` | yes | Replaces the global `stale_if_error` for this entry, in both directions: an explicit `0` disables it |
| `Expires` | yes | Fallback when no `s-maxage`/`max-age`. Measured against the response's own `Date` when present, so clock skew is not folded into the lifetime. An unparseable value means "already expired" |
| `Set-Cookie` present | no | Conservative shared-cache rule: replaying per-client state to another client is a session-fixation vector |

### Malformed and duplicate directives

- A recognized delta-seconds directive with a **malformed, empty or negative**
  argument resolves to **zero**, not to "directive absent". The origin wrote it,
  so it must not be ignored — but its lifetime cannot be trusted, and zero is the
  shortest one. In practice `max-age=oops` makes the response uncacheable.
- An **out-of-range** argument is clamped upward to ~100 years, as RFC 9111
  §1.2.2 requires, which also keeps the arithmetic inside `time.Duration`.
- A **duplicate** directive resolves to the **smallest** value. RFC 9111 leaves
  this undefined; the shortest lifetime is the safe reading.
- Multiple `Cache-Control` field lines are merged into one directive set, as
  RFC 9110 §5.3 requires.
- Directive tokens are case-insensitive, values may be quoted, and a quoted value
  may contain commas (`no-cache="Set-Cookie, X-Token"` is one directive).

### Age and freshness

Freshness is measured from when the **origin** generated the representation, not
from when Jul received it: `Date` and `Age` are folded into RFC 9111 §4.2.3
corrected initial age. A response that already spent two minutes in an upstream
cache is therefore served for its remaining lifetime, not for a fresh full one.
A `Date` in the future or a negative `Age` never makes an entry look younger than
the moment it arrived.

### Mandatory synchronous validation

When the request said `no-cache`/`max-age=0`, the stored response said
`no-cache`, `must-revalidate` forbids serving the entry stale, or the entry has
aged out of its stale window, Jul validates **before** writing anything to the
client.

- The conditional request prefers the **entity tag** (`If-None-Match`) whenever
  the origin supplied one, and falls back to `Last-Modified`
  (`If-Modified-Since`) — RFC 9110 §13.1.2 validator precedence. Never both.
- The client's own conditional headers are **replaced**, not merged: the cache is
  asking whether *its* stored copy is current.
- With **no validator at all**, Jul fetches a complete new response rather than
  serving the stored one.
- Concurrent validators for the same (effective key, handler generation) join
  **one** origin request through the same call state background
  `stale-while-revalidate` uses. There is no second deduplication mechanism.
- The leader runs on the generation's background lease, so it is bounded by
  `[global] shutdown_timeout` and is **not** cancelled by its own client. A
  waiter that gives up cancels only its own wait; the leader keeps serving the
  waiters that remain.
- Every leader and waiter terminates on every outcome — `304`, a new
  representation, an uncacheable response, an origin error, a timeout, a
  cancellation, and a panic in the downstream handler. No call state is left
  behind, and a panic value never reaches a waiter, a log or a metric.

| Validation outcome | Result |
| --- | --- |
| Origin answers `304` | Metadata merged, entry replaced, stored body served as `REVALIDATED` |
| Origin answers a new cacheable response | Stored and served as `MISS` |
| Origin answers something unstorable | The superseded entry is **deleted**; the origin's response is served as `MISS` |
| Origin answers `5xx` | Stale reuse only if the policy below permits it, otherwise the origin's error is forwarded |
| Origin's answer cannot be buffered (over `memory_max_size`, or a stream) | Re-fetched and streamed; one extra origin request on this path only, so a response is never truncated |
| No generation lease available | A complete origin fetch. Never an unvalidated stored body |

### Stale reuse after a failed validation

Precedence is the origin's, then Jul's:

1. `must-revalidate` / `proxy-revalidate` forbid stale reuse outright. Neither
   `[cache] stale_if_error` nor an explicit `stale-if-error` overrides it.
2. An explicit response `stale-if-error=N` replaces the global setting for that
   entry — longer, shorter, or an explicit `0` that disables it.
3. `[cache] stale_if_error` is a default for responses that said nothing. It is
   **not** permission to serve a `no-cache` response unvalidated, so a `no-cache`
   entry gets a grace window only when the origin explicitly asked for one.

There is no offline or disconnected-mode exception.

### `304 Not Modified` merge

A `304` produces a **new immutable entry** that atomically replaces the stored
one; the published entry is never written through.

- The stored **body and status are preserved** — that is the point of a `304`.
- Every end-to-end field the `304` supplies **replaces** its stored counterpart;
  a field the `304` does not mention is kept.
- `Warning` is removed (RFC 9111 §5.5 obsoleted it, and a stale warn-code must
  not survive a refresh).
- Hop-by-hop fields, everything named in the `304`'s `Connection`, and
  `Content-Length` are never merged.
- Freshness, validators, and the whole policy set (`no-cache`,
  `must-revalidate`, authenticated-reuse permission, `stale-if-error`) are
  **recomputed** from the merged headers, so an origin can tighten its policy
  through a `304` and have it apply immediately.
- The `304`'s `Date`/`Age` restart the age clock. A `304` carrying neither is
  treated as generated now.

The refreshed representation is **discarded** rather than published when the
`304` makes it unstorable (`private`, `no-store`, a new `Set-Cookie`, an expired
`Expires`) or **changes `Vary`**. A changed `Vary` changes which key the
representation belongs under, and publishing it at the old key would leave an
entry reachable through a keying rule that no longer describes it. The request
falls back to a complete fetch, which stores it under the correct key.

### Requests carrying `Authorization`

Jul is a shared cache, so it applies RFC 9111 §3.5 with a deliberately stricter
storage rule:

| Stored response says | May satisfy an authenticated request? | May a response **generated for** an authenticated request be stored? |
| --- | :---: | :---: |
| nothing | no | no |
| `public` | yes | yes |
| `s-maxage=N` | yes | yes |
| `must-revalidate` | yes | **no** |
| `private` / `no-store` | no | no |
| `no-cache` | only after successful validation | no |

`must-revalidate` is listed by §3.5 as a *reuse* permission, and Jul honors it as
one. It is **not** a publication permission: "do not serve me stale" says nothing
about whether the body is user-specific, so it does not authorize putting an
authenticated response into a cache an anonymous client also reads. When an
origin's directives are incomplete, Jul does not share.

Consequences that are tested rather than assumed:

- an unauthenticated stored response is **not** automatically reusable by an
  authenticated request;
- two requests carrying the **same** credential string do not thereby share a
  response — the permission comes from the stored response, never from the
  credential;
- `Vary: Authorization` is not a substitute for the permission;
- credentials never enter a cache key, a stored header, a log, a metric label, a
  tracing attribute or disk metadata.

### Unsafe-method invalidation

After a **successful** unsafe request, Jul invalidates the representations it
made obsolete (RFC 9111 §4.4).

- **Unsafe methods**: everything except `GET`, `HEAD`, `OPTIONS`, `TRACE` and
  `CONNECT`. An unknown extension method counts as unsafe — the cost of an
  unnecessary invalidation is a miss.
- **Success** means a non-error status, that is **2xx or 3xx**. A `4xx`, a `5xx`,
  or an exchange that produced no status at all (canceled, timed out, hijacked)
  proves nothing changed and invalidates nothing.
- **Targets**: the effective request URI, plus same-origin `Location` and
  `Content-Location`. For each target, both the `GET` and the `HEAD` key are
  removed, together with **every `Vary` variant** they own.
- **Cross-origin targets are never invalidated.** A `Location` naming another
  host is skipped, as is a value that does not parse, is opaque (`mailto:`), uses
  a non-HTTP scheme, or spells the authority differently.

A user-controlled header cannot reach the filesystem through this path: an
invalidation target is only ever used to build a cache key, and a disk file is
named by the SHA-256 of that key, so it can neither traverse a directory nor name
a file the cache did not write. Foreign files in `disk_path` are never touched.

### `Range` and `If-Range` (decision D05)

A request carrying `Range` **or** `If-Range` bypasses the cache entirely, before
lookup and before any storage decision:

- no cached complete representation is substituted for the origin's answer;
- the request is forwarded unchanged, and the origin's `206`, `200` or `416`,
  its `Content-Range`, its validators and its exact bytes reach the client
  untouched;
- the result is `X-Cache: BYPASS`;
- **nothing** is stored — not a `206`, not a partial body under a full-object
  key, and not a `200` the origin happened to return;
- an existing complete representation is neither replaced nor evicted.

Only the cache is bypassed. Authentication, authorization, client-certificate
handling, rate limiting, plugins, the WAF, tracing, metrics and the access log
all wrap outside this handler and still run.

Serving RFC-compliant byte ranges from complete cached representations is a
documented future enhancement, not a gap that will be filled implicitly. See
[Known limitations](#known-limitations).

## Executable behaviour matrix

This is the authoritative post-#134 matrix. Every row names executable evidence;
the complete audit record is [the 2026-08-07 cache recertification](audit/2026-08-07-cache-recertification.md).

| Behaviour | Contract | Executable evidence |
| --- | --- | --- |
| Key construction | `METHOD\nhost.lower\nREQUEST_URI`; credentials and cookies never enter the key | `TestKeyConstruction`, `TestCredentialsNeverEnterTheCacheKey` (`internal/cache`) |
| GET/HEAD, unsafe and other methods | GET/HEAD may cache; successful unsafe methods invalidate GET+HEAD; OPTIONS/TRACE/CONNECT do not invalidate | `TestSuccessfulUnsafeMethodsInvalidateTheTarget`, `TestSafeMethodsNeverInvalidate`, `TestInvalidationStatusRules`, `TestHeadRangeRequestBypasses` |
| Cacheable statuses | Only the documented status allow-list is stored; 1xx/101 and origin errors are not | `TestCacheableStatusSet`, `TestProtocolSwitchResponseNeverStored`, `TestResponseDirectiveStorage` |
| Request `no-store` | Bypass lookup and storage without purging an existing entry | `TestRequestNoStoreBypassesLookupAndStorage`, `TestRequestNoStoreResponseIsNotStored` |
| Request `no-cache`, `max-age=0`, `Pragma` | Mandatory synchronous validation, including fresh entries | `TestRequestNoCacheValidatesEvenAFreshEntry`, `TestPragmaNoCacheValidates`, `TestRequestPolicyMatrix` |
| Response storage/freshness directives | `no-store`, `private`, `public`, `s-maxage`, `max-age`, `Expires` and malformed/duplicate values follow the contract above | `TestResponseDirectiveStorage`, `TestFreshnessPrecedence`, `TestResponsePolicyMatrix`, `TestParseCacheControlDirectiveMatrix`, `TestParseCacheControlIsTotal` |
| Response `no-cache` | Stored, but every reuse validates before serving | `TestResponseNoCacheRequiresValidationBeforeEveryReuse`, `TestConcurrentMandatoryValidatorsIssueOneOriginRequest` |
| `must-revalidate` / `proxy-revalidate` | Forbid stale reuse and outrank stale-if-error | `TestMustRevalidateForbidsStaleReuse`, `TestStaleIfErrorRespectsMustRevalidate`, `TestFreshnessStaleWindowIsZeroWhenRevalidationIsMandatory` |
| SWR/SIE | Bounded stale reuse; explicit response values replace global defaults; canceled work never extends SIE | `TestExplicitStaleIfErrorReplacesTheGlobalSetting`, `TestStaleOnErrorWindowContract`, `TestRevalidationCanceledByLeaseCancel`, `TestCacheRecertificationSoak` |
| `Authorization` | Shared reuse only when explicitly permitted; identities and credentials never leak through keys or variants | `TestSharedReusePermissionMatrix`, `TestNoCrossIdentityLeakage`, `TestUnauthenticatedEntryIsNotReusableByAnAuthenticatedRequest`, `TestVaryAuthorizationStillEnforcesTheSharedReuseRule`, `TestRealAuthenticatedIdentityIsolation` |
| `Set-Cookie` | Never stored | `TestResponseDirectiveStorage`, `TestSharedReusePermissionMatrix` |
| `Vary` and membership | Distinct variants coexist; 64-entry membership cap; invalidation removes every owned memory/disk variant | `TestHandlerVaryVariantsCoexist`, `TestUnsafeMethodRemovesEveryVaryVariant`, `TestDeletedVariantCannotBeResurrectedByANewStub`, `TestChangedVaryReplacesTheVariantSet` |
| ETag / Last-Modified / 304 | ETag precedence; immutable metadata merge; changed/unsafe metadata discards | `TestValidatorPrecedence`, `TestMerge304UpdatesMetadata`, `TestMerge304Discards`, `TestMerge304NeverMutatesThePublishedEntry`, `TestMerge304AcrossBothTiers` |
| Range / If-Range | Bypass before lookup/store; 206 is never stored | `TestRangeRequestBypassesLookup`, `TestIfRangeBypassesLookup`, `TestRangeResponsesAreNeverStored`, `TestRealRangePassThrough` |
| WebSocket / 101 | Upgrade requests bypass with the original writer; 101 is never stored | `TestWebSocketThroughCachedProxy`, `TestWebSocketThroughFullMiddlewareChain`, `TestUpgradeRequestBypassesCache`, `TestProtocolSwitchResponseNeverStored`, `TestRepeatedUpgradesThroughCachedProxy` |
| SSE / flushed / oversized | SSE is not buffered or stored; ordinary flushed chunked responses remain cacheable; oversized capture is not stored | `TestSSEThroughCachedProxyStreamsAndIsNotStored`, `TestEventStreamIsNeverStoredOrBuffered`, `TestFlushedChunkedResponseIsStillCached`, `TestOversizedStreamIsNotStored` |
| Memory/disk tiers | Byte-bounded LRU, disk overflow/promotion, restart rehydration, owner-only files and foreign-file isolation | `TestDiskStorePersistence`, `TestDiskStoreConcurrentOverflowEviction`, `TestDiskStoreFileMode`, `TestDiskStoreAtomicNoTempLeftovers`, `TestDiskStoreIgnoresForeignFiles` |
| Entry immutability | Published entries are cloned and replaced, never mutated under readers | `TestStaleIfErrorReplacesRatherThanMutates`, `TestNotModifiedReplacesRatherThanMutates`, `TestConcurrentReadsDuringStaleIfErrorReplacement`, `TestPublishedEntryDoesNotAliasHandlerState` |
| Reload and generation lifetime | Refresh/validation is generation-owned, survives client disconnect, blocks premature retirement and is bounded on forced retirement/shutdown | `TestReloadWaitsForCacheRevalidationHoldingGeneration`, `TestReloadDuringMandatorySynchronousValidation`, `TestRevalidationSurvivesClientDisconnect`, `TestForcedRetirementCancelsLeasedWork`, `TestDrainCancelsAndBoundsLiveBackgroundWork` |
| Real HTTP/admin paths | H1/H2 validation, concurrent singleflight, invalidation, auth isolation and purge/delete use production handlers | `TestRealProxyOriginValidation`, `TestRealHTTP2CacheBehavior`, `TestRealConcurrentValidatorsShareOneOriginRequest`, `TestRealUnsafeMethodInvalidation`, `TestPurgeMethodAndKey` |

## Recertification disposition

#131, #132 and #133 corrected generation ownership/immutability, shared-cache
semantics and protocol-writer transparency. #134 then re-read the integrated
source, replaced the prose-only matrix above with executable evidence, added the
mandatory-validation and variant-invalidation benchmarks, and added the focused
cache correctness soak. No unresolved cache P0/P1 defect was found. The cache
therefore retains **GA**; the limitations below remain explicit product,
performance, conservative or lifecycle constraints rather than hidden
correctness exceptions.

## Freshness and stale-while-revalidate

Freshness comes from the upstream's `Cache-Control`/`Expires`. When the upstream
gives no explicit freshness, `default_ttl` applies. `stale_while_revalidate`
allows a still-recent-but-expired entry to be served immediately (`X-Cache: STALE`)
while a fresh copy is fetched in the background, trading a small staleness window
for lower tail latency. A burst of concurrent stale hits triggers exactly one
background revalidation per variant, so the cache shields the origin from a
thundering herd.

The background refresh is owned by the handler generation that started it; see
[Background revalidation lifecycle](#background-revalidation-lifecycle) for what
cancels it and what does not.

## Stale-if-error

`stale_if_error` extends the stale-serving window when a background
revalidation encounters an upstream error (HTTP 5xx or timeout). If the
revalidation fails, the cached entry remains servable for the configured
`stale_if_error` duration from the point of failure, protecting clients from
backend outages. Once the error window expires or a subsequent revalidation
succeeds, normal freshness rules apply.

The extension **replaces** the stored entry with an updated copy; it never edits
the published entry in place (see [Entry immutability](#entry-immutability)).

A revalidation that is **cancelled** — by process shutdown, by forced generation
retirement, or by the bounded operation deadline — is deliberately *not* treated
as an upstream error and does **not** extend the stale window. Only a real origin
failure does.

Example — tolerate a 5-minute backend outage:

```toml
[cache]
stale_while_revalidate = "60s"
stale_if_error         = "300s"
```

> `stale_if_error` only applies when a stale entry exists and a background
> revalidation is attempted. It does not create cache entries on its own.

## Background revalidation lifecycle

A background refresh is **owned by the handler generation that started it**. This
is what the ownership model guarantees, and what it deliberately does not.

### Ownership

When a stale hit decides to refresh, it acquires a **background lease** on the
current handler generation *before* the originating request returns. The lease is
counted in the same in-flight accounting that keeps a generation's resources
open, so while a refresh is running the reload machinery treats that generation
exactly as if a request were still executing on it:

- the generation's gRPC backend connections, WASM plugin runtimes and
  static-file directory handles stay open;
- a configuration reload still publishes the new generation immediately, and new
  requests use it at once;
- the **old** generation is retired only once its leased work finishes.

A generation that has begun retiring refuses new background work. In that case
the stale entry is served normally and simply expires; no refresh starts.

### Lifetime

| Event | Effect on an in-flight background revalidation |
| --- | --- |
| The originating client disconnects | **No effect.** The refresh continues; that is the point of `stale_while_revalidate`. |
| A configuration reload runs | **No effect** on the refresh. It keeps using the generation it started on. |
| The old generation exceeds `[global] shutdown_timeout` while retiring | **Cancelled.** The server cancels the leased work, then closes the generation's resources. |
| The operation exceeds `[global] shutdown_timeout` in total | **Cancelled.** Every background operation carries that bound as an absolute deadline. |
| Process shutdown | **Cancelled**, then awaited for at most `[global] shutdown_timeout`. Shutdown is never wedged by a refresh. |

A background revalidation therefore **may survive client disconnect**, and
**cannot outlive process shutdown or its bounded lifetime**.

### Context carried into a refresh

The refresh does not inherit the client request's context. It runs on a context
rooted in the process lifetime that carries an explicit allow-list of values:

| Value | Carried | Why |
| --- | --- | --- |
| Generation upstream pool snapshot | yes | The refresh must select from the same backend set as the request that started it. |
| Mutual-TLS client identity | yes | The reverse proxy expands `$ssl_client_*` from it; dropping it would send the origin a different request. |
| Request id | yes | Log correlation; bounded, already-logged value. |
| Trace id | yes | Log correlation; bounded, already-logged value. |
| Authentication claims | **no** | Consumed by the rate limiter, which wraps *outside* the cache and is not re-entered by a refresh. Not retained. |
| Plugin invocation state | **no** | Plugin middleware also wraps outside the cache. |
| Client tracing span | **no** | A refresh is not part of the client request's trace and must not extend its lifetime. |
| Client cancellation / request deadline | **no** | Replaced by process cancellation plus the bounded operation deadline. |

### Deduplication

At most one refresh runs per (effective cache key, handler generation). A burst
of concurrent stale hits therefore produces exactly one origin request.

Generation is part of the key on purpose: a refresh still running on a retiring
generation must never suppress the new generation's refresh of the same key. The
call state is removed, and every waiter released, on **every** outcome —
success, `304`, uncacheable, origin error, cancellation and panic in the
downstream handler.

### Cache data across reloads

The cache instance is created once per process and is captured by every handler
generation, so **cached data persists across ordinary configuration reloads**.
This is the pre-existing, deliberately preserved policy: reloading configuration
does not warm-start or flush the cache.

One consequence follows directly from it: a refresh that started on the old
generation publishes into the process-shared cache even if it completes after a
reload changed routes or backends. The result is the representation the **old**
generation's route would have produced. Changing routing or backends therefore
does not retroactively invalidate entries; use `[cache] enabled = false`, a
restart, or the admin purge endpoint when that matters.

### Observability

`jul_cache_revalidations_total{outcome}` counts refresh decisions with a fixed
label set: `stored`, `not_modified`, `uncacheable`, `origin_error`, `canceled`,
`panic`, `no_lease`, `deduplicated`. No cache key, URL, host, generation id or
error string is ever used as a label. Structured logs carry only the bounded
operation name, the generation id and a bounded reason.

## Entry immutability

A cache entry is **immutable once published**. After an entry is handed to the
memory or disk tier, no field of it is written again — not the body bytes, not
the header map or its value slices, not the `Vary` metadata, and not the
freshness timestamps.

Code that must change timing or metadata (a `304` refreshing freshness, a
`stale_if_error` window extension) builds a deep-enough clone, mutates the clone,
and atomically replaces the stored pointer under the tier's own lock. Cloning
happens only at those publication and update boundaries, so a cache **hit never
pays a body copy**.

This is what makes it safe for a lookup to hand out the stored `*Entry` pointer:
concurrent readers, the disk tier's encoder and a replacing writer can never
observe a half-updated entry.

## Upgrades, streaming and ResponseWriter transparency

Enabling `cache = true` on a route must not change what the route can *do*. Three
rules make that true.

### Protocol upgrades bypass the cache

A request is treated as a protocol upgrade when it carries a non-empty `Upgrade`
header **and** lists the `upgrade` token in `Connection` (RFC 9110 §7.8). Both
halves are required, so a stray client-supplied `Upgrade` header cannot switch
caching off for ordinary traffic.

An upgrade request:

- is **not** looked up in the cache, even if a fresh entry exists for its key;
- reaches the handler with the **untouched** response writer, so the reverse
  proxy can hijack the connection and complete the `101 Switching Protocols`
  handshake;
- is reported as `X-Cache: BYPASS`;
- is **never** stored.

Only the cache is bypassed. Authentication, rate limiting, the WAF, plugins and
mutual-TLS identity all wrap outside the cache and still run.

`CONNECT` and every other method outside `GET`/`HEAD` never reach the cache path
at all.

### Responses that are never stored

| Response | Stored? | Why |
| --- | :---: | --- |
| `101 Switching Protocols` and any other `1xx` | no | An interim or protocol-switch response is not a representation. |
| Anything written after a successful hijack | no | The connection has left HTTP; the wrapper also refuses further writes with `http.ErrHijacked`. |
| `Content-Type: text/event-stream` | no | An event stream never ends. Capture stops at the first byte, so an open SSE connection accumulates nothing. |
| A body larger than `memory_max_size` | no | Existing size bound; capture is discarded when the limit is passed. |

### Flushing does not make a response uncacheable

An ordinary flushed response **is** still cached. This is deliberate: the
standard reverse proxy flushes on every write of any response whose
`Content-Length` is unknown — that is, every chunked response — so treating a
flush as "this is a stream" would silently stop caching most dynamic backends.
Unbounded growth is prevented by the event-stream rule and the size bound above
rather than by the flush itself.

### Optional `ResponseWriter` interfaces

The writer a cached route hands to its handler implements **exactly** the
optional interfaces the real connection implements — `http.Flusher`,
`http.Hijacker`, `http.Pusher`, `io.ReaderFrom` — and no others. It also
implements `Unwrap() http.ResponseWriter`, so `http.ResponseController` reaches
the real writer for capabilities that have no classic interface (read/write
deadlines, full duplex).

The "no others" half matters as much as the first. A wrapper that always
implements `Hijack` and returns an error still makes `w.(http.Hijacker)` succeed,
and handlers branch on the assertion rather than the error. On **HTTP/2 and
HTTP/3 there is no connection to hijack**, and the writer correctly reports that
by not implementing `http.Hijacker` at all; `http.NewResponseController(w).Hijack()`
returns `http.ErrNotSupported`. WebSocket over HTTP/2 (RFC 8441 extended
`CONNECT`) is not supported by the reverse proxy and is unrelated to caching.

The same guarantee holds through the whole middleware chain — metrics, access
log, tracing and compression all use the same wrapper — so composition does not
erode it.

## On-disk format

Each cached response is a single self-contained file under `disk_path`:

- **Filename** — the lowercase hex SHA-256 of the cache key (exactly 64 hex
  characters). Filenames are content-addressed, opaque, and reveal nothing about
  the original URL.
- **Contents** — the gob-encoded entry (status, headers, body, timestamps,
  freshness metadata, and any `Vary` values).

There is no index file or sidecar; on startup the directory is scanned and each
cache file is re-hydrated into the index, ordered by modification time and bounded
by `disk_max_size`.

The encoding is gob, which decodes a field absent from an older file as its zero
value. Every field #132 added was chosen so that **its zero value is the
conservative answer**: an entry written by an older Jul permits no authenticated
reuse, and a `Vary` pointer entry with no membership record claims no variants.
An older Jul reading a newer file ignores the fields it does not know, which
returns it to its own (looser) behaviour but never corrupts the file. No
migration is required in either direction.

## Disk-tier safety

The disk tier is written defensively so that an operator can point `disk_path` at
a directory without risking other data or torn cache entries:

- **Atomic writes.** Entries are written to a same-directory temporary file,
  fsync'd, then renamed over the final name. A crash mid-write leaves either the
  previous complete entry or no entry — never a partially written file. This uses
  the same atomic-write primitive as the config writer
  (see [SECURITY.md](../SECURITY.md#hardening-defaults--recommendations)).
- **Restrictive permissions.** Cache files are created mode `0o600` (owner
  read/write only) and the cache directory is created mode `0o700`. A cached
  response body is never world-readable.
- **Foreign-file safety.** Only files whose names match the content-addressed
  scheme (64 lowercase hex characters) are indexed, served, or eligible for LRU
  eviction. Any other file — an operator's note, a stray archive, a nested
  directory — is **ignored and never deleted**. Their presence is logged once at
  startup with a `WARN` so a misconfigured `disk_path` (for example, a shared
  directory) is visible without endangering the foreign data.

Even so, prefer a **dedicated** directory for `disk_path`. The cache assumes it
owns the directory's hex-named files and may delete any of them at any time under
memory or disk pressure.

## Eviction and overflow

Both tiers track their size in bytes and evict least-recently-used entries when
over budget:

- The **memory tier** evicts to stay within `memory_max_size`. Evicted entries are
  handed to the disk tier (when configured). The disk write runs **outside** the
  memory-tier lock — victims are collected under the lock and persisted after it
  is released — so a slow disk never blocks in-memory reads and writes.
- The **disk tier** evicts whole files to stay within `disk_max_size`, removing the
  backing file as it goes.

An entry larger than a tier's cap is simply not stored in that tier.

## Known limitations

Product limitations — deliberate scope, not defects:

1. **No tag or pattern purge.** The admin API can purge a single exact key or
   the entire cache. There is no way to purge by URL prefix, host, or tag (e.g.
   invalidate all `/api/v1/users/*`). Operators needing selective invalidation
   must do so at the application layer or use short TTLs.

2. **No cached byte-range serving.** Every request carrying `Range` or
   `If-Range` bypasses the cache and reaches the origin (decision D05). Range
   workloads therefore get no cache benefit. Serving RFC-compliant single byte
   ranges from complete cached representations — with `If-Range`,
   `Content-Range` and `416`, and without multipart ranges or partial-object
   storage — is a recorded future enhancement candidate, promoted only when
   representative media/download workloads show material origin or latency cost.

3. **No cross-process / distributed cache.** Each Jul.IA process has one
   process-scoped cache instance shared by its cache-enabled locations. There is
   no shared cache (e.g. Redis) across multiple instances; each node warms
   independently.

4. **Silent oversized-entry drop.** A response body larger than `memory_max_size`
   (or `disk_max_size`) is streamed to the client but not cached. There is no
   log or metric emitted for this; operators must size tiers generously.

Intentionally conservative behavior — correct, but stricter than the letter of
the standard:

5. **`Set-Cookie` responses are never stored**, whatever their directives say.
   An origin cannot currently opt a cookie-bearing response into shared caching.

6. **A response generated for a request carrying `Authorization` is stored only
   with `public` or `s-maxage`.** RFC 9111 §3.5 also lists `must-revalidate` as
   a *reuse* permission, and Jul honors it as one — but not as permission to
   publish an authenticated response into a cache anonymous clients read.

7. **A `304` that changes `Vary` discards the representation** rather than
   rekeying it, and the request re-fetches. Rekeying is possible in principle;
   discarding is the outcome that cannot leave a wrongly-keyed entry reachable.

8. **A `Vary` pointer entry written before the membership record fails closed.**
   After upgrading, the first request for each previously cached `Vary` URL is a
   miss. No data is lost and nothing stale is served.

9. **Variant membership is capped at 64 per base resource.** Past the cap the
   oldest variant is deleted. A resource with more than 64 live variants will
   churn.

10. **Mandatory validation buffers the origin's answer** rather than streaming
    it, because the cache must see the status before deciding whether to serve
    the stored body. An answer that exceeds `memory_max_size` or turns out to be
    a stream costs one extra origin request, and is then streamed normally.

Lifecycle behavior:

11. **A reload does not invalidate cached entries.** The cache is process-scoped
    and survives configuration reloads by design, so a routing or backend change
    does not retroactively drop entries stored under the previous configuration.
    A refresh that was already in flight when the reload ran also completes
    against the *old* generation's route and publishes its result. Use the admin
    purge endpoint or a restart when a configuration change must invalidate
    cached content. Characterized by
    `TestReloadWaitsForCacheRevalidationHoldingGeneration` and
    `TestReloadDuringMandatorySynchronousValidation` (`internal/server`) and the
    generation-isolation tests in `internal/cache`.

12. **A background refresh or a synchronous validation delays retirement of its
    generation.** While either is running, the handler generation that started it
    keeps its gRPC connections, plugin runtimes and static roots open. This is
    bounded: the work is cancelled and the resources closed after
    `[global] shutdown_timeout`. A very slow origin can therefore hold one
    superseded generation's resources for up to that long after a reload.

13. **Without the server's generation-lease seam there is no background
    refresh**, and mandatory validation degrades to a complete origin fetch. It
    never degrades to serving an unvalidated entry.

## Benchmarks

Recertification command (five fixed-iteration samples):

```bash
go test -run '^$' -bench='BenchmarkCache.*' -benchmem -benchtime=100x -count=5 ./internal/cache
```

Environment: GitHub-hosted Ubuntu 24.04, Go 1.26.5, linux/amd64, AMD EPYC 7763,
`GOMAXPROCS=4`. Values below are the median of the five samples from workflow
run `31163489042` on `3a4c982ed42cabaf608de771492402897f2dffac`.

| Benchmark | Scenario | Median ns/op | allocs/op | bytes/op |
| --- | --- | ---: | ---: | ---: |
| `BenchmarkCacheHit` | Fresh memory hit, small body | 2,071 | 15 | 1,224 |
| `BenchmarkCacheMiss` | Unique miss and first store | 10,321 | 49 | 8,266 |
| `BenchmarkCacheVaryHit` | Warm `Vary` variant hit | 2,777 | 19 | 1,328 |
| `BenchmarkCacheMemOverflow` | 512-byte memory cap and disk overflow | 751,820 | 113 | 14,154 |
| `BenchmarkCacheMandatoryValidation304` | Synchronous `no-cache` validation, 304 merge and stored-body serve | 7,703 | 47 | 4,354 |
| `BenchmarkCacheVariantInvalidation` | Delete a populated 32-variant membership set | 5,728 | 1 | 84 |

These are correctness-path baselines, not cross-machine service-level targets.
The fixed `100x` benchtime deliberately excludes automatic calibration that
would repeatedly rebuild the 32-variant setup. Disk overflow remains orders of
magnitude slower than a memory hit because it performs crash-safe file I/O; the
memory lock is released before that I/O.

## Threat note

The cache stores upstream responses and serves them to subsequent clients.
Because the cache is a shared resource, its misuse can affect confidentiality,
integrity, and availability:

1. **Cache poisoning via Host header manipulation.** The cache key includes the
   `Host` header (lowercased). If Jul.IA sits behind a reverse proxy that trusts
   `X-Forwarded-Host` without validation, an attacker may poison the cache with
   a malicious response keyed to a victim's host, causing that response to be
   served to legitimate requests. Counter-measures: validate `Host` in the outer
   proxy; do not forward untrusted `X-Forwarded-Host` to Jul.IA.

2. **Cross-user leakage through `Vary` misconfiguration.** An upstream that sets
   `Vary: Cookie` without `Cache-Control: private` may cause one user's
   authenticated page to be served to another user with the same `Cookie` header.
   The cache does not treat cookies as a cache-bypass signal (only `private`,
   `no-store` or a `Set-Cookie` in the response do). Requests carrying
   `Authorization` are separately protected by the shared-reuse rule above, but
   **cookie-based sessions are not**, because a `Cookie` header is not a
   shared-cache authentication signal in RFC 9111. Counter-measures: upstreams
   must set `private` for user-specific responses; operators should audit `Vary`
   headers before enabling cache on cookie-authenticated routes.

3. **Vary-header evasion (Web Cache Deception).** An attacker requests
   `/profile.jpg` with a `Accept: image/webp` header when the real page is
   `/profile`. If the upstream returns `Vary: Accept` without path validation, the
   attacker may cache a 200 HTML response under the `.jpg` key. Counter-measures:
   upstreams should validate extensions before serving; the cache itself follows
   the upstream's Vary directive faithfully.

4. **Stale-if-error window extension as a DoS vector.** If `stale_if_error` is
   configured very long (e.g. 24 hours) and an attacker keeps the backend down,
   clients may receive stale responses well beyond the intended freshness window.
   An origin can defend itself with `must-revalidate`, `proxy-revalidate` or an
   explicit `stale-if-error=0`, all of which outrank the global setting.
   Counter-measures: keep `stale_if_error` short (minutes, not hours); monitor
   upstream health and alert on prolonged 5xx rates.

5. **Sensitive response caching on disk.** The disk tier persists responses
   across restarts. If a cached response contains PII and the disk is on shared
   or cloud storage without encryption-at-rest, the data may leak. Counter-measures:
   use full-disk encryption or an encrypted volume for `disk_path`; set
   restrictive permissions (`0o700` dir, `0o600` files).

6. **Request smuggling via header injection in cached responses.** An upstream
   that reflects user input into response headers without sanitisation may produce
   a `Set-Cookie` or `Location` header that carries an attack payload. The cache
   stores and replays the header verbatim. Counter-measures: validate and sanitise
   all user input before writing response headers; the cache correctly strips
   hop-by-hop headers but does not inspect application-level headers beyond
   `Cache-Control` semantics.

## GA status — recertified

**Decision: retain/restore GA.** The historical released status is now backed by
the corrected implementation and the post-#134 integrated evidence package. The
focused correctness soak is not presented as production-scale throughput; the
historical one-hour soak remains the long-duration evidence and the new run
specifically recertifies corrected validation, stale, invalidation and overflow
paths.

| Criterion | Status | Evidence |
| --- | --- | --- |
| 1 — Conformance / behaviour matrix | ✅ | [Executable behaviour matrix](#executable-behaviour-matrix) and [audit record](audit/2026-08-07-cache-recertification.md) |
| 2 — Published benchmark numbers | ✅ | Six refreshed benchmarks above, including mandatory validation and populated-variant invalidation |
| 3 — Known-limitations list | ✅ | 13-item list above and [known-limitations.md](known-limitations.md), separated by product/conservative/performance/lifecycle meaning |
| 4 — Semver-guarded config/API contract | ✅ | v1 config freeze and #126 metric compatibility guard; the released `jul_cache_events_total` help remains unchanged |
| 5 — Soak evidence | ✅ | Historical 1h cache soak plus post-correction focused run: 422,042 requests, 0 errors, all five cache result classes, stable goroutine/FD/heap trends — [evidence](soak-evidence.md#2026-08-07--cache-recertification-correctness-soak-linux-30-seconds-16-workers) |
| 6 — Runnable example + docs | ✅ | `testdata/cache.toml`, this guide, benchmark and soak records |
| 7 — Security / threat note | ✅ | Threat note above plus real authenticated identity-isolation and foreign-file tests |
| 8 — Fuzzing/parser robustness | ✅ | `TestParseCacheControlIsTotal` and directive matrices |
| 9 — Self-explanatory Console surface | ✅ | Runtime overview and cache purge/delete surfaces; lifecycle limitations remain explicit |
