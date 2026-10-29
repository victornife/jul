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
- [Targeted CDN cache control](#targeted-cdn-cache-control)
- [Freshness and stale-while-revalidate](#freshness-and-stale-while-revalidate)
- [Cold-miss fill locking](#cold-miss-fill-locking)
- [Background revalidation lifecycle](#background-revalidation-lifecycle)
- [Entry immutability](#entry-immutability)
- [Upgrades, streaming and ResponseWriter transparency](#upgrades-streaming-and-responsewriter-transparency)
- [On-disk format](#on-disk-format)
- [Disk-tier safety](#disk-tier-safety)
- [Eviction and overflow](#eviction-and-overflow)
- [Scoped cache invalidation and origin grouping tags](#scoped-cache-invalidation-and-origin-grouping-tags)
- [Known limitations](#known-limitations)
- [Benchmarks](#benchmarks)
- [Threat note](#threat-note)

## Two-tier model

| Tier | Backing store | Bound | Lifetime |
| --- | --- | --- | --- |
| Memory | in-process map, LRU by bytes | `memory_max_size` | process lifetime; survives reloads |
| Disk (optional) | files under `disk_path`, LRU by bytes | `disk_max_size` (per-entry ceiling `max_entry_size`) | survives restarts |

A request is served from the memory tier first. Memory-eligible entries
(`body <= min(memory_max_size, max_entry_size)`) are stored in the memory tier
and written through to the disk tier (when `disk_path` is configured) under a
monotonic per-key generation so they survive process restarts and remain backed
by the larger disk LRU if evicted from memory. On a memory miss the disk tier is
consulted: a disk hit whose body fits within `min(memory_max_size, max_entry_size)`
is **promoted** back into the memory tier (provided its per-key generation is
still current) so subsequent reads stay hot, whereas a large disk-backed object
(`> memory_max_size`, up to `max_entry_size`) is streamed directly from its
on-disk file in 32 KiB chunks without entering or evicting the memory tier
([ADR 0023](adr/0023-large-object-disk-streamed-cache.md)).

The cache instance is shared across configuration reloads, so a reload does not
cold-start the cache.

## Configuration

See the [`[cache]`](../README.md#cache) reference for the full key list. A typical
block with the disk overflow tier and large-object streaming enabled:

```toml
[cache]
enabled                = true
memory_max_size        = "256MB"
max_entry_size         = "512MB"                  # defaults to memory_max_size; up to disk_max_size when disk_path is set
disk_path              = "/var/cache/jul/http"   # enables the disk tier
disk_max_size          = "4GB"
default_ttl            = "5m"
stale_while_revalidate = "30s"
stale_if_error         = "300s"   # keep serving stale on upstream 5xx
lock                   = true     # coalesce concurrent cold misses per key
lock_timeout           = "5s"     # fall through uncached after 5s wait
range_enabled          = true     # opt-in Option A range-aware caching (#442)
max_ranges             = 1        # bound accepted range specs (default 1 when range_enabled = true)
tag_headers            = ["Cache-Tag", "Surrogate-Key"] # opt-in trusted origin grouping-tag headers (#621)
strip_tag_headers      = true     # strip configured tag_headers from client responses (default true when tag_headers is set)
```

Per-location opt-in is still required, and each location may optionally override
freshness, caps, stale windows, and request-cookie bypass via
`[servers.locations.cache_policy]` ([ADR 0029](adr/0029-route-scoped-cache-policy-and-vary-guards.md)):

```toml
[[servers.locations]]
route_id = "api-public"
match    = { type = "prefix", path = "/api/catalog" }
proxy_pass = "http://catalog"
cache    = true

  [servers.locations.cache_policy]
  default_ttl            = "0s"     # require explicit origin freshness on this route
  max_ttl                = "2m"     # cap origin s-maxage / max-age / Expires to 2m
  stale_while_revalidate = "30s"
  stale_if_error         = "5m"
  bypass_cookies         = ["session_id", "__Host-auth"] # or bypass_any_cookie = true
```

## Cache key and Vary

The cache key is derived from the request method, the lowercased host, and the
request URI — and from nothing else. No credential, cookie or other request
header is ever part of a key; reuse restrictions are enforced by the stored
entry's recorded policy instead, because a credential-derived key would silently
turn a leak into an unbounded cache.

When an upstream response carries `Vary` on one or more header lines, each
combination of the varied request-header values (including every repeated
request field line) is stored as a **distinct variant** under its own key, so
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

The 2026-09-28 correction to repeated-field handling uses a new variant-value
encoding. Existing disk entries written with the old first-value encoding
cannot match a new lookup, even when a legacy pointer still claims them; the
first request after upgrade fetches and stores a fresh variant. This is a
deliberate miss rather than reuse of a representation whose full request-header
combination was not recorded.

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
| `BYPASS` | The cache was not consulted or fell back to the origin (protocol upgrade, `Range`/`If-Range` when `range_enabled = false` or on range fallback, or request `no-store`) |

> The `jul_cache_events_total` **help string** still reads
> `(HIT/MISS/STALE/BYPASS)`. That text is frozen by the v1.32.0 released metric
> contract and is intentionally preserved; `REVALIDATED` is an additive label
> value, which the released contract does not freeze. The separate
> `jul_cache_revalidations_total` family shipped in v2.0.0; its description
> covers synchronous validation and background revalidation.

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
| `s-maxage=N` | yes | Freshness lifetime `N`; outranks `max-age` and `Expires`; requires validation once stale, even when SWR/SIE is configured |
| `max-age=N` | yes | Freshness lifetime `N`; outranks `Expires` |
| `no-cache` | **yes** | Stored, but **every** reuse requires successful synchronous validation. Storing it is the point: a `304` still saves the body |
| `no-cache="Header"` | yes | Treated as unqualified `no-cache`. Selective header replacement is a separate design; validating the whole representation is its conservative superset |
| `must-revalidate` | yes | Never served stale. Outranks `[cache] stale_while_revalidate` and `[cache] stale_if_error` |
| `proxy-revalidate` | yes | Identical to `must-revalidate` for Jul, which is a shared cache |
| `stale-while-revalidate=N` | yes | Replaces the global stale window for this entry |
| `stale-if-error=N` | yes | Replaces the global `stale_if_error` for this entry, in both directions: an explicit `0` disables it |
| `Expires` | yes | Fallback when no `s-maxage`/`max-age`. Measured against the response's own `Date` when present, so clock skew is not folded into the lifetime. An unparseable value means "already expired" |
| `Set-Cookie` present on any response line | no | Conservative shared-cache rule: replaying per-client state to another client is a session-fixation vector; an empty first line cannot hide a later cookie |

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

`Expires` accepts IMF-fixdate and historical RFC850/asctime HTTP-date forms,
with exact format validation after parsing. One-digit hours, extra spaces,
non-GMT zones, impossible dates, empty values, `0`, and multiple field lines
cannot become fresh. Spelling is compared case-insensitively; historical
two-digit years more than 50 years in the future use the preceding century.
Parsing is bounded to 33 bytes and uses UTC, never the host's local timezone.
`max-age` and shared `s-maxage` still outrank invalid `Expires`.

`Age` follows RFC 9111 section 5.1: select the first member before a comma in
the first field line, trim only optional SP/HTAB, and discard later members and
lines. The selected value must contain only decimal digits. Invalid values,
signs, negative values, decimals and numeric garbage are ignored, not partially
parsed; `Date` still contributes apparent age. Valid overflowing integers
saturate at the same approximately 100-year arithmetic bound as cache
delta-seconds and never wrap. Thus `Age: 7200, 0` is old, while `Age: 0` followed
by `Age: 7200` uses zero. Freshness ends at the exact lifetime boundary.

Freshness is measured from when the **origin** generated the representation, not
from when Jul received it: `Date` and `Age` are folded into RFC 9111 §4.2.3
corrected initial age. A response that already spent two minutes in an upstream
cache is therefore served for its remaining lifetime, not for a fresh full one.
A `Date` in the future or a negative `Age` never makes an entry look younger than
the moment it arrived.

### Targeted CDN cache control

Jul implements RFC 9213 with the fixed target list `[CDN-Cache-Control]`.
There is no extra configuration switch, custom `Jul-CDN-Cache-Control` field,
or inference from arbitrary `*-Cache-Control` names. Selection occurs for every
origin response and again when a `304` updates its stored metadata.

A valid, non-empty Structured Fields dictionary replaces **both** generic
`Cache-Control` and `Expires` for Jul's cache policy. They are not merged.
Absent, empty or syntactically invalid targeted fields fall back to the normal
generic policy. The parser is separate from generic cache-control parsing:
`github.com/dunglas/httpsfv` v1.1.2 implements Structured Fields, including
combined field lines, quoting, parameters, and duplicate-member handling.
Jul limits combined input (including separators) to 16 KiB and 128 field
lines before invoking it. Inputs above those bounds are invalid for selection.

```http
Cache-Control: no-store
CDN-Cache-Control: max-age=600
```

Jul applies a 600-second freshness lifetime. Generic `no-store` still reaches
downstream caches, which need not implement targeted policy. The inverse
prevents Jul from storing the response:

```http
Cache-Control: max-age=600
CDN-Cache-Control: no-store
```

Targeted `max-age=0` also overrides future `Expires`; targeted `max-age=600`
overrides generic `no-cache`. Request `no-store` remains BYPASS: response policy
never changes request policy or middleware ordering.

| Targeted directive | Value and behavior |
| --- | --- |
| `max-age`, `s-maxage` | Structured integer; existing freshness precedence and bounds; negative integers resolve conservatively to zero; `s-maxage` requires validation once stale |
| `no-store` | Boolean true (including a bare member); never stored |
| `private` | Boolean true or field-name string; conservatively never shared |
| `no-cache` | Boolean true or field-name string; every reuse validates the complete representation |
| `must-revalidate`, `proxy-revalidate` | Boolean true; forbid stale SWR/SIE reuse |
| `public` | Boolean true; explicit shared-cache/authenticated-publication permission |
| `stale-while-revalidate`, `stale-if-error` | Structured integers; existing bounded windows and revalidation precedence |
| Unknown extension | Ignored, but the valid non-empty targeted field remains selected |

Parameters are ignored. Values of the wrong inferred directive type are not
coerced or consumed: `max-age="600"` and `max-age=600.5` are not integer
lifetimes, but their syntactically valid dictionary remains selected. Duplicate
dictionary members use Structured Fields' **last member wins** rule, unlike
generic cache-control's conservative minimum-duration rule. Numbers exceeding
the Structured Fields 15-digit integer range invalidate the field and invoke
generic fallback; valid large integers saturate at Jul's duration bound.

`CDN-Cache-Control: none` selects targeted policy although `none` is unknown,
as in RFC 9213's example. Without targeted freshness, Jul uses `default_ttl`,
not generic `Expires` or `max-age`. Origins must put privacy restrictions in
the selected targeted field as well as any generic field when both cache
classes must be restricted. Cookie-authenticated personalized responses must
not rely solely on generic `private` when permissive targeted policy is present.

Authorization, Set-Cookie, status, capture-budget, Vary, memory/disk and
generation-lifetime boundaries remain in force. Targeted `max-age` alone does
not authorize publishing an authenticated response. Targeted fields are stored
and forwarded normally on misses, hits and validation; consuming policy does
not strip them or reset `Age`/`Date`.

`Surrogate-Control` is **not** an alias, not translated to `Cache-Control`,
and not consumed by Jul's cache. Ordinary forwarding and hop-by-hop sanitation
apply, including removal when nominated by `Connection`. Invalid origin
transfer framing remains a proxy error (`502`), never a cached representation.

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

1. `must-revalidate` / `proxy-revalidate` / `s-maxage` forbid stale reuse outright. Neither
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

Any `Authorization` field line marks the request as authenticated for this
policy, including when an earlier repeated line is empty. The cache never
infers that a later credential is absent from the first line's value.

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

### Route-scoped cache policy, cookie bypass, and Vary guards (#623 / #672)

Each cache-enabled location (`cache = true`) inherits global `[cache]` settings
by default and may declare `[servers.locations.cache_policy]` to tune route
freshness, cap upstream TTLs, or bypass caching when request cookies are
present ([ADR 0029](adr/0029-route-scoped-cache-policy-and-vary-guards.md)):

| Field | Type | Default (omitted) | Explicit `"0s"` / `false` |
| --- | --- | --- | --- |
| `default_ttl` | duration | Inherits `[cache].default_ttl` | `"0s"` disables fallback caching when the origin omits explicit freshness (`s-maxage`, `max-age`, `Expires`) |
| `max_ttl` | duration | None (no route cap) | `"0s"` caps fresh lifetime to zero (disabling fresh storage on the route) |
| `stale_while_revalidate` | duration | Inherits `[cache].stale_while_revalidate` | `"0s"` disables fallback SWR when the origin omits `stale-while-revalidate` |
| `stale_if_error` | duration | Inherits `[cache].stale_if_error` | `"0s"` disables fallback `stale-if-error` when the origin omits `stale-if-error` |
| `bypass_cookies` | list of 1..32 cookie names | `[]` | Case-insensitive RFC 6265 cookie-name tokens (`<= 128` bytes each); mutually exclusive with `bypass_any_cookie = true` |
| `bypass_any_cookie` | bool | `false` | When `true`, any request carrying a `Cookie` header bypasses lookup and storage |

#### Precedence model

1. **Explicit origin exclusion always wins**: `Cache-Control: no-store`,
   `private`, `Set-Cookie`, `Vary: *`, `Vary: Cookie`, `Vary: Authorization`,
   and uncacheable `Authorization` responses are never stored regardless of
   route `default_ttl` or `max_ttl`.
2. **Request cookie bypass**: when `bypass_any_cookie = true` or a request
   `Cookie` header contains a cookie name in `bypass_cookies`, Jul skips both
   cache lookup and storage (`X-Cache: BYPASS`, decision `cookie_bypass`).
   Cookie values are never inspected, logged, or included in cache keys. If a
   request's `Cookie` headers are malformed or exceed `16` lines, `8192` bytes,
   or `128` pairs while cookie bypass is configured, Jul fails closed and
   bypasses the cache (`cookie_bypass_malformed`).
3. **Base freshness**: origin `CDN-Cache-Control` (when valid) or
   `Cache-Control: s-maxage` -> `max-age` -> `Expires` -> route `default_ttl`
   (if set) -> global `[cache].default_ttl`.
4. **Route freshness cap (`max_ttl`)**: when `max_ttl` is set on the route,
   `effective_ttl = min(base_ttl, max_ttl)`. A route `max_ttl` caps longer
   origin or fallback lifetimes from above and never extends a shorter origin
   lifetime.
5. **Overlapping route isolation and hot reload**: routes sharing a hostname and
   URL (for example via method, header, query, or cookie route predicates)
   isolate their cached representations by route scope without evicting sibling
   routes' variants. All `[servers.locations.cache_policy]` fields are
   `hot_reload`: tightening `max_ttl`, `default_ttl`, `stale_while_revalidate`,
   `stale_if_error`, or cookie bypass takes effect immediately on stored memory
   and disk entries and fences in-flight fills started under the prior policy.

#### Sensitive and proxy/CGI-transformed `Vary` guards (#672)

- **Sensitive `Vary` rejection (`vary_sensitive_rejected`)**: responses carrying
  `Vary: Cookie` or `Vary: Authorization` (case-insensitively, including on
  `304 Not Modified` merges, `5xx` `stale-if-error` checks, Range fills, and
  legacy on-disk entries across restarts) are never stored or reused in the
  shared cache.
- **Proxy and CGI transformed-header `Vary` rejection
  (`vary_proxy_rewrite_rejected`, `vary_cgi_rewrite_rejected`)**: when an HTTP
  reverse-proxy or FastCGI/uWSGI route rewrites, synthesizes, or strips a
  request header before forwarding to the upstream (including `Forwarded`,
  `X-Forwarded-*`, `X-Real-IP`, hop-by-hop headers, per-request `Connection`
  nominations, `proxy_headers` `set_*`/`remove`, `fastcgi_params` `HTTP_*`
  overrides, or CGI hyphen/underscore aliases such as `X-Custom` vs
  `X_Custom`), a response that varies on that header cannot be matched safely
  against the downstream client's original request header and is excluded from
  shared cache storage and reuse. Standard content-negotiation headers
  (`Accept-Encoding`, `Accept-Language`, `Accept`, `User-Agent`) remain
  cacheable as normal.
  a non-HTTP scheme, or spells the authority differently.

A user-controlled header cannot reach the filesystem through this path: an
invalidation target is only ever used to build a cache key, and a disk file is
named by the SHA-256 of that key, so it can neither traverse a directory nor name
a file the cache did not write. Foreign files in `disk_path` are never touched.

### `Range` and `If-Range` (decision D05 and #442 Option A)

By default (`[cache].range_enabled = false`), a request carrying `Range` **or**
`If-Range` bypasses the cache entirely, before lookup and before any storage
decision (decision D05):

- no cached complete representation is substituted for the origin's answer;
- the request is forwarded unchanged, and the origin's `206`, `200` or `416`,
  its `Content-Range`, its validators and its exact bytes reach the client
  untouched;
- the result is `X-Cache: BYPASS`;
- **nothing** is stored — not a `206`, not a partial body under a full-object
  key, and not a `200` the origin happened to return;
- an existing complete representation is neither replaced nor evicted.

When `[cache].range_enabled = true` (#442 Option A, [ADR 0024](adr/0024-range-aware-cache-option-a.md)),
Jul.IA enables complete-object range-aware caching on cached routes:

- **Complete representations only.** Only complete cacheable `200 OK`
  representations are ever stored. Origin `206 Partial Content` responses are
  never stored or merged as fragments.
- **Single byte-range serving (`206` and `416`).** Valid single byte-range `GET`
  requests (`bytes=first-last`, `bytes=first-`, `bytes=-suffix`, ignoring empty
  `#range-spec` list elements per RFC 9110 §5.6.1.2) against a complete cached
  representation are answered directly from the memory or disk tier (`206
  Partial Content` with exact `Content-Range: bytes start-end/total`,
  `Content-Length`, and `Accept-Ranges: bytes`, or `416 Range Not Satisfiable`
  with `Content-Range: bytes */total` when unsatisfiable; for a 0-byte cached
  representation, `Range` is ignored per RFC 9110 §14.1.2 & §14.2 and the
  complete 0-byte `200 OK` is served; content-scoped digests `Content-Digest`
  and `Content-MD5` and HTTP message signatures `Signature` and `Signature-Input`
  are stripped on synthetic `206`/`416` responses while `Repr-Digest` is
  preserved). Disk-backed range hits slice `[start, end]` via
  `io.NewSectionReader` and stream in 32 KiB pooled chunks without loading the
  full object into memory.
- **Strong `If-Range` validation (RFC 9110 §13.1.5, §5.6.7, and §8.8.2.2).**
  Entity-tag `If-Range` values require syntactically valid strong comparison via
  `opaqueETag` (weak `W/"..."` tags and invalid `etagc` characters never match);
  HTTP-date `If-Range` values accept all three RFC 9110 §5.6.7 formats
  (`IMF-fixdate`, `RFC 850`, and ANSI C `asctime`), require exact timestamp
  equality with `Last-Modified`, and require a valid cached response `Date`
  header at least one second later than `Last-Modified` so implicitly weak
  timestamps are rejected (when an origin response omits `Date`, Jul synthesizes
  the received-time `Date` on capture per RFC 9110 §6.6.1).
  When `If-Range` does not match, the cache serves the complete `200 OK`
  representation (`jul_cache_range_requests_total{outcome="if_range_full"}`).
  `If-Range` without `Range` is ignored per RFC 9110 §13.1.5; `If-None-Match`
  and `If-Modified-Since` are evaluated ahead of `If-Range` and `Range`.
- **Cold-miss complete-object fill and fallback.** On a cache miss for a valid
  single byte-range `GET`, the cache initiates (and when `[cache].lock = true`,
  coalesces) a complete-object origin fetch (`outcome="fill"`), stores the
  complete representation in memory or on disk up to `max_entry_size`, and
  serves the requested byte slice. If the origin response is uncacheable,
  exceeds `max_entry_size` or capture budgets, or a fill-lock waiter times out,
  the request falls back to the origin (`outcome="fallback"`, `X-Cache: BYPASS`)
  with the client's original `Range` and `If-Range` headers preserved untouched.
- **Bounded `max_ranges` and multi-range disposition.** `[cache].max_ranges`
  (default `1` when `range_enabled = true`, maximum `16`) bounds the number of
  range specs accepted by the parser. Malformed `Range` headers or requests
  exceeding `max_ranges` record `outcome="invalid"` and bypass to the origin;
  syntactically valid multi-range requests within `max_ranges` record
  `outcome="fallback"` and bypass to the origin without synthesizing
  `multipart/byteranges` in cache.

Only the cache is bypassed on fallback. Authentication, authorization,
client-certificate handling, rate limiting, plugins, the WAF, tracing, metrics
and the access log all wrap outside this handler and still run.

## Executable behaviour matrix

This is the authoritative post-#134 matrix. Every row names executable evidence;
the complete audit record is [the 2026-08-07 cache recertification](audit/old/2026-08-07-cache-recertification.md).

| Behaviour | Contract | Executable evidence |
| --- | --- | --- |
| Strict expiry and age | Invalid/repeated `Expires` is expired; first `Age` member only; bounds and exact transitions | `TestStrictExpires`, `TestStrictAge`, `TestAgeFreshBoundary`, `TestMerge304IgnoresInvalidFirstAge` (`internal/cache`) |
| RFC 9213 selection | Targeted dictionary replaces generic policy/Expires; malformed fallback, extensions, duplicates, auth and 304 restrictions; forwarding unchanged | `TestTargetedCacheControl`, `TestTargetedFreshnessPrecedence`, `TestTargetedEntryContracts`, `FuzzCachePolicy` (`internal/cache`); `TestRealTargetedCachePolicy`, `TestRealAuthenticatedIdentityIsolation` (`internal/handler`) |
| Key construction | `METHOD\nhost.lower\nREQUEST_URI`; credentials and cookies never enter the key | `TestKeyConstruction`, `TestCredentialsNeverEnterTheCacheKey` (`internal/cache`) |
| GET/HEAD, unsafe and other methods | GET/HEAD may cache; successful unsafe methods invalidate GET+HEAD; OPTIONS/TRACE/CONNECT do not invalidate | `TestSuccessfulUnsafeMethodsInvalidateTheTarget`, `TestSafeMethodsNeverInvalidate`, `TestInvalidationStatusRules`, `TestHeadRangeRequestBypasses` |
| Cacheable statuses | Only the documented status allow-list is stored; 1xx/101 and origin errors are not | `TestCacheableStatusSet`, `TestProtocolSwitchResponseNeverStored`, `TestResponseDirectiveStorage` |
| Stored headers | Multiset difference against the map at cache-handler entry; a field an outer layer pre-set (`X-Request-ID`, `X-Cache`) is excluded unless the handler overwrote it with a different value | `TestHeaderDifference`, `TestCacheHitDoesNotReplayAStaleRequestID`, `TestCacheStoresAHandlerOverriddenRequestIDWithTheHandlersValue`, `TestStoredEntryExcludesOuterLayerOnlyHeaders` |
| Request `no-store` | Bypass lookup and storage without purging an existing entry | `TestRequestNoStoreBypassesLookupAndStorage`, `TestRequestNoStoreResponseIsNotStored` |
| Request `no-cache`, `max-age=0`, `Pragma` | Mandatory synchronous validation, including fresh entries | `TestRequestNoCacheValidatesEvenAFreshEntry`, `TestPragmaNoCacheValidates`, `TestRequestPolicyMatrix` |
| Response storage/freshness directives | `no-store`, `private`, `public`, `s-maxage`, `max-age`, `Expires` and malformed/duplicate values follow the contract above | `TestResponseDirectiveStorage`, `TestFreshnessPrecedence`, `TestResponsePolicyMatrix`, `TestParseCacheControlDirectiveMatrix`, `TestParseCacheControlIsTotal` |
| Response `no-cache` | Stored, but every reuse validates before serving | `TestResponseNoCacheRequiresValidationBeforeEveryReuse`, `TestConcurrentMandatoryValidatorsIssueOneOriginRequest` |
| `must-revalidate` / `proxy-revalidate` | Forbid stale reuse and outrank stale-if-error | `TestMustRevalidateForbidsStaleReuse`, `TestStaleIfErrorRespectsMustRevalidate`, `TestFreshnessStaleWindowIsZeroWhenRevalidationIsMandatory` |
| SWR/SIE | Bounded stale reuse; explicit response values replace global defaults; canceled work never extends SIE | `TestExplicitStaleIfErrorReplacesTheGlobalSetting`, `TestStaleOnErrorWindowContract`, `TestRevalidationCanceledByLeaseCancel`, `TestCacheRecertificationSoak` |
| `Authorization` | Shared reuse only when explicitly permitted, including repeated field lines; identities and credentials never leak through keys or variants | `TestSharedReusePermissionMatrix`, `TestNoCrossIdentityLeakage`, `TestUnauthenticatedEntryIsNotReusableByAnAuthenticatedRequest`, `TestRepeatedAuthorizationDoesNotPublishPrivateResponse`, `TestRepeatedAuthorizationDoesNotReuseAnonymousEntry`, `TestVaryAuthorizationStillEnforcesTheSharedReuseRule`, `TestRealAuthenticatedIdentityIsolation` |
| `Set-Cookie` | Never stored, even when an earlier field line is empty | `TestResponseDirectiveStorage`, `TestRepeatedSetCookieResponseIsNotShared`, `TestSharedReusePermissionMatrix` |
| `Vary` and membership | Distinct variants coexist across repeated request/response field lines; 64-entry membership cap; old first-value entries miss after upgrade; invalidation removes every owned memory/disk variant | `TestHandlerVaryVariantsCoexist`, `TestVaryDistinguishesRepeatedRequestHeaderValues`, `TestVaryHonorsEveryResponseFieldLine`, `TestFirstValueVaryEntriesFailClosedAfterUpgrade`, `TestUnsafeMethodRemovesEveryVaryVariant`, `TestDeletedVariantCannotBeResurrectedByANewStub`, `TestChangedVaryReplacesTheVariantSet` |
| ETag / Last-Modified / 304 | ETag precedence; immutable metadata merge; changed/unsafe metadata discards; every `Connection` line's nominated fields excluded | `TestValidatorPrecedence`, `TestMerge304UpdatesMetadata`, `TestMerge304Discards`, `TestRemoveHopByHopFromEveryConnectionLine`, `TestMerge304NeverMutatesThePublishedEntry`, `TestMerge304AcrossBothTiers` |
| Range / If-Range | Default bypass (`range_enabled = false`) before lookup/store; opt-in Option A (`range_enabled = true`, `max_ranges`) serves single byte ranges (`206`/`416`) from complete stored representations, enforces strong `If-Range`, coalesces complete-object fills on cold misses, and preserves original `Range`/`If-Range` on fallback; `206` is never stored | `TestRangeRequestBypassesLookup`, `TestIfRangeBypassesLookup`, `TestRangeResponsesAreNeverStored`, `TestRealRangePassThrough`, `TestRangeOptionAByteRangeMatrix`, `TestRangeOptionAIfRangeAndConditionalSemantics`, `TestRangeOptionAMalformedMultiRangeAndUnits`, `TestRangeOptionAVaryAuthCompressionStaleAndInvalidation`, `TestRangeOptionAConcurrentCoalescingTimeoutAndBoundedHeap`, `TestRangeOptionAReviewRegressions`, `TestLargeObjectAndRangeAuditRegressions`, `TestRangeGateEvaluationSyntheticWorkload`, `FuzzRangeHeaderAndStateTransitions` (`internal/cache`) |
| WebSocket / 101 | Upgrade requests bypass with the original writer; 101 is never stored | `TestWebSocketThroughCachedProxy`, `TestWebSocketThroughFullMiddlewareChain`, `TestUpgradeRequestBypassesCache`, `TestProtocolSwitchResponseNeverStored`, `TestRepeatedUpgradesThroughCachedProxy` |
| SSE / flushed / oversized | SSE is not buffered or stored; ordinary flushed chunked responses remain cacheable; oversized capture is not stored | `TestSSEThroughCachedProxyStreamsAndIsNotStored`, `TestEventStreamIsNeverStoredOrBuffered`, `TestFlushedChunkedResponseIsStillCached`, `TestOversizedStreamIsNotStored` |
| Memory/disk tiers | Byte-bounded LRU, disk overflow/promotion, restart rehydration, owner-only files and foreign-file isolation | `TestDiskStorePersistence`, `TestDiskStoreConcurrentOverflowEviction`, `TestDiskStoreFileMode`, `TestDiskStoreAtomicNoTempLeftovers`, `TestDiskStoreIgnoresForeignFiles` |
| Large-object disk streaming (#525 Part 2) | `max_entry_size` decouples single-object ceiling from `memory_max_size`; `JULDCH02` trailer-framed disk capture and streaming hits without body-sized heap allocation; active-reader lease retirement across eviction/invalidation/purge/replacement; single-lease metadata+body lookup with generation-checked cleanup; exact-boundary metadata envelope; temp-disk capture budget bounded by `disk_max_size` across misses and `304` rewrites | `TestLargeObjectTierPlacementAndKnownUnknownLengths`, `TestLargeObjectBoundaryOneByteOverAndBoundedHeap`, `TestLargeObjectFailurePathsLeaveNoPartialEntry`, `TestLargeObjectRestartRecoveryAndForeignFileSafety`, `TestLargeObjectActiveReaderDuringEvictionInvalidationAndReplacement`, `TestLargeObjectRevalidation304And200`, `TestLargeObjectConcurrentTempDiskBudgetExhaustion`, `TestLargeObjectDiskWriteAndRenameFailures`, `TestLargeObjectValidation200PublishFailuresStreamBodyToClient`, `TestLargeObjectLookupVsReplacementAndEvictionRaces`, `TestLargeObjectExactMaxEntrySizeWithMinimumDiskMaxSize`, `TestLargeObjectConcurrent304RewriteRespectsTempDiskBudget` (`internal/cache`) |
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

## Cold-miss fill locking

When a popular uncached URL receives a burst of concurrent requests, default
cache behavior forwards every miss to the origin in parallel. Enabling
`[cache].lock = true` coalesces concurrent cold misses per cache key so only one
request fetches and captures from the origin while compatible followers wait up
to `[cache].lock_timeout` (#525 Part 1; combines with `[cache].max_entry_size`
disk-streamed capture from #525 Part 2 and `[cache].range_enabled` Option A
range fills from #442):

- **Leader selection and parallel keys.** The first cold miss for a cache key
  becomes the fill leader and performs the origin fetch and capture. Unrelated
  cache keys execute in parallel and never block each other.
- **Default timeout (`5s`).** When `lock = true` and `lock_timeout` is omitted
  or zero, `lock_timeout` defaults to `5s`. This matches NGINX's
  `proxy_cache_lock_timeout 5s` default and Jul's 5s sub-operation convention
  (such as `plugins.*.fetch_timeout` and `health_check.interval`), while
  remaining below the 10s reload/connect/auth defaults. Setting a positive
  `lock_timeout` when `lock = false` is rejected as an inconsistent
  configuration.
- **Waiter re-evaluation (never blind reuse).** When the leader finishes, every
  woken waiter repeats the full lookup, `Vary` header matching, freshness
  evaluation, and RFC 9111 §3.5 shared-cache `Authorization` checks against the
  published entry:
  - If the entry is compatible and fresh (or servable under `stale_while_revalidate`
    or synchronous validation), the waiter serves it (`X-Cache: HIT`, `STALE`, or
    `REVALIDATED`) and records `jul_cache_fill_locks_total{outcome="reused"}`.
  - If the leader published a response whose `Vary` headers or shared-cache
    `Authorization` policy cannot satisfy the waiter, the waiter never receives
    the leader's representation; it records `outcome="not_reusable"` and
    proceeds to normal miss handling. When a cold fill newly discovers a `Vary`
    header set, waiters that share a different variant key coalesce once on that
    variant key so each distinct variant issues one origin request.
- **Prompt release on non-cacheable or failed fills.** If the leader's response
  status or headers are uncacheable (`no-store`, `private`, `Set-Cookie`,
  `Vary: *`, `5xx`, `text/event-stream`), if capture is abandoned for size or
  in-flight budget (`oversize` or `budget`), if the origin closes early before
  delivering the declared `Content-Length`, or if the leader cancels,
  disconnects, or panics, all waiting followers are released immediately
  (`outcome="uncacheable"`) without waiting for `lock_timeout` or for a slow
  uncacheable body to finish streaming.
- **Timeout fall-through without competing cache publication.** If
  `lock_timeout` elapses while the leader is still fetching, the waiter records
  `outcome="timeout"` and falls through to an uncached origin request
  (`X-Cache: MISS`). That fallback streams directly to the client without
  allocating a capture buffer or publishing to the store, so it cannot overwrite
  or interfere with the active fill leader.
- **Waiter cancellation.** If a waiting client cancels its own request context,
  only that waiter exits (`outcome="canceled"`); the leader and any remaining
  waiters continue normally.
- **Credential-free lock keys and labels.** When no `Vary` stub exists yet, the
  lock key is the primary cache key (`METHOD host URI`), which contains no
  headers. When a `Vary` stub exists, the variant key is hashed with SHA-256
  before indexing the lock table, so raw `Authorization`, `Cookie`, or other
  header values never appear in lock keys, logs, or metric labels.
- **Hot reload coherence.** Both `cache.lock` and `cache.lock_timeout` are
  `hot_reload` fields. Each miss loads one immutable `CachePolicy` snapshot at
  entry and uses that snapshot for the duration of the operation.

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
does not retroactively invalidate entries. After old-generation work drains,
use the admin purge endpoint when the old representation must no longer be
served. A restart alone clears the memory tier but **retains the disk tier**;
changing `[cache].enabled` is itself restart-required. For a cutover that must
exclude old entries, quiesce traffic and in-flight refreshes, restart if needed,
purge both tiers, then resume traffic.

### Observability

`jul_cache_revalidations_total{outcome}` counts refresh decisions with a fixed
label set: `stored`, `not_modified`, `uncacheable`, `origin_error`, `canceled`,
`panic`, `no_lease`, `deduplicated`. `jul_cache_fill_locks_total{outcome}`
counts cold-miss fill-lock events with a fixed label set: `wait`, `reused`,
`not_reusable`, `uncacheable`, `timeout`, `canceled`.
`jul_cache_range_requests_total{outcome}` counts range-aware cache decisions
(#442 Option A) with a fixed label set: `hit`, `fill`, `fallback`,
`if_range_full`, `unsatisfiable`, `invalid`. No cache key, URL, host,
user identity, generation id or error string is ever used as a label. Structured
logs carry only the bounded operation name, the generation id and a bounded
reason.

Live occupancy of each configured tier is exported as four gauges/counters,
labeled by `tier` (`memory`/`disk`; the disk series is absent when no
`disk_path` is configured), read at scrape time rather than pushed from the
request path — the same pattern as the upstream resilience gauges:

- `jul_cache_bytes` — current bytes occupied. For the disk tier this is the
  exact physical size on disk (`body + metadata + 24-byte trailer`) of all
  indexed entries (`physBytes`). When a single exact-boundary entry has
  `body <= disk_max_size` (`max_entry_size == disk_max_size`), its bounded
  `JULDCH02` metadata and trailer envelope (`<= 1 MiB + 24 B`) is permitted
  above `disk_max_size` so the entry does not self-evict, and
  `jul_cache_bytes{tier="disk"}` reports that true physical file size.
- `jul_cache_max_bytes` — the configured byte cap.
- `jul_cache_entries` — current entry count.
- `jul_cache_evictions_total` — cumulative LRU-capacity evictions since
  startup. Explicit invalidation (a purge or a `del`) is a different operation
  and is not counted here.

These are what make "the cache stays within its configured bounds" a
falsifiable, observable claim against a running process rather than something
only an in-tree test can see.

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
| `101 Switching Protocols` | no | A protocol switch, not a representation; the connection is leaving HTTP. |
| Any other `1xx` (e.g. `103 Early Hints`) | n/a | Not evaluated for storage at all: it passes through untouched, without latching the writer, so the real final status that follows it is judged entirely on its own merits (#331). |
| Anything written after a successful hijack | no | The connection has left HTTP; the wrapper also refuses further writes with `http.ErrHijacked`. |
| `Content-Type: text/event-stream` | no | An event stream never ends. Capture stops at the first byte, so an open SSE connection accumulates nothing. |
| A body larger than `max_entry_size` (defaults to `memory_max_size`) | no | The per-entry capture limit. A declared `Content-Length` above it abandons capture before the first byte; a body that crosses it frees its capture buffer or temporary file at that point. Counted as `jul_cache_capture_skipped_total{reason="oversize"}`. |
| A miss that arrives while in-flight memory captures hold `memory_max_size` bytes or in-flight temporary disk captures hold `disk_max_size` bytes | no | The capture budgets below. Streamed to the client uncaptured and counted as `reason="budget"`. |

### Which response headers are stored

A stored entry's headers are the **multiset difference** between the response
header map at commit and the same map as it stood the instant the cache handler
was entered — not the whole map, and not a denylist of field names.

`RequestID` sets `X-Request-ID` on the shared header map before calling the next
handler, and the cache sets its own `X-Cache: MISS` the same way, before the
handler runs. Both are therefore already on the map when the cache takes its
"entry" snapshot; if the handler never touches them, they cancel out of the
commit-time snapshot exactly and never reach the stored entry. A handler that
`Set`s a name to a *different* value — deliberately overriding what an outer
layer put there — has that different value survive, because the difference is
per `(name, value)`, not per name. A field the handler adds an extra value to
(`Add`, not `Set`) keeps only the addition. See `TestCacheHitDoesNotReplayAStaleRequestID`
and `TestHeaderDifference` in `internal/cache` (#332).

This delivers a **no-leak** guarantee — nothing a layer outside the cache
contributed can appear in a stored entry, and a hit can therefore never replay
one client's per-request header to another — but not full **fidelity** to the
origin representation. Two cases are a known, accepted residual rather than a
bug, per [ADR 0018](adr/0018-bounded-route-matching-and-response-policy.md) §11:

- a handler that `Set`s a field to the *exact same value* an outer layer already
  set is indistinguishable, by value, from the outer layer's own contribution,
  so the difference is empty and the field is **not stored** even though the
  handler asserted it (`TestHandlerSettingTheOuterLayersExactValueIsNotStored`);
- a handler that actively deletes a field an outer layer pre-set has no way to
  record that deletion — there is no tombstone — so a later **hit still carries
  whatever value the outer layer sets on that later request**
  (`TestHandlerDeletingAnOuterHeaderIsResurrectedOnAHit`). This is not a leak:
  the outer layer re-derives its own value on every request, hit or miss: never
  a stored one.

A layer outside the cache that wants full fidelity for a header it also sets
must apply it at commit, the same rule §10 states for the response-policy
wrapper — never before calling the next handler.

**The response-header/CORS wrapper (#146) follows exactly this rule and is the
sharpest instance of why it matters.** It sits outside the cache and applies
its operations, including any `Access-Control-*` grant, inside its own
`WriteHeader`, after the cache has already taken its commit-time snapshot — so
a stored entry never contains a CORS grant, and a hit can never replay one
origin's `Access-Control-Allow-Origin` to a different one.
`TestCORSResponseHeadersNeverEnterTheCacheAcrossOrigins` in `internal/app`
proves it end to end: two requests from different allowed origins against the
same cached route get one `MISS` and one `HIT` with byte-identical bodies, and
each response carries its *own* request's origin, never the other's. An
upstream-authored `Vary: Origin`, in contrast, is inside the commit snapshot and
is stored and honoured normally — `TestUpstreamVaryOriginStillCreatesCacheVariants`
pins that Jul's own CORS layer creates no cache variants while an upstream's
does.

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

Each cached response is a single self-contained file under `disk_path` using the
`JULDCH02` trailer-framed layout ([ADR 0023](adr/0023-large-object-disk-streamed-cache.md)):

- **Filename** — the lowercase hex SHA-256 of the cache key (exactly 64 hex
  characters). Filenames are content-addressed, opaque, and reveal nothing about
  the original URL.
- **Contents** — `[raw body bytes: 0 .. bodyLen][gob(diskEntryMeta): bodyLen .. bodyLen+metaLen][24-byte trailer: uint64(metaLen) + uint64(bodyLen) + "JULDCH02"]`.
  Placing raw body bytes at offset `0` and the metadata + fixed 24-byte trailer
  at the tail of the file allows unknown-length (`chunked`) responses to stream
  sequentially to a temporary file without rewriting the body on completion,
  allows startup rehydration and cache lookups to read only the metadata tail
  without loading the body into memory, and exposes an `O(1)` `io.SectionReader`
  over `[0, bodyLen)` for streaming hits.

There is no external index file or sidecar; on startup the directory is scanned
and each valid `JULDCH02` file is re-hydrated into the index by reading only its
metadata trailer, ordered by modification time and bounded by `disk_max_size`.
Unpublished `.jul-cache-tmp-*` and `.jul-cache-retired-*` files left by an
unclean shutdown, as well as truncated, corrupt, or pre-`JULDCH02` hex-named
cache files, are removed during startup rehydration so the cache heals cleanly as
misses.

The metadata block is gob-encoded, which decodes a field absent from an older
`JULDCH02` file as its zero value. Every field #132 added was chosen so that
**its zero value is the conservative answer**: an entry written by an older Jul
permits no authenticated reuse, and a `Vary` pointer entry with no membership
record claims no variants.

## Disk-tier safety

The disk tier is written defensively so that an operator can point `disk_path` at
a directory without risking other data or torn cache entries:

- **Atomic writes.** Entries are streamed to a same-directory temporary file
  (`.jul-cache-tmp-*`), trailer-appended, fsync'd, closed, and then atomically
  renamed over the final content-addressed name. A crash mid-write leaves either
  the previous complete entry or a `.jul-cache-tmp-*` file that is cleaned up on
  the next startup — never a partially written published entry.
- **Active-reader lease safety.** Serving a disk-backed entry acquires a
  reference-counted lease (`diskBlob`) and opens an independent file descriptor
  before response headers are committed. If an entry is evicted, invalidated,
  purged, or replaced while one or more readers are streaming it, the published
  file is renamed to `.jul-cache-retired-*` in `disk_path` and unlinked only
  when the last active reader closes its descriptor, guaranteeing cross-platform
  reader isolation without body buffering.
- **Restrictive permissions.** Cache files are created mode `0o600` (owner
  read/write only) and the cache directory is created mode `0o700`. A cached
  response body is never world-readable.
- **Foreign-file safety.** Only files whose names match the content-addressed
  scheme (64 lowercase hex characters) or Jul's own `.jul-cache-tmp-*` /
  `.jul-cache-retired-*` prefixes are managed by the cache. Any other file — an
  operator's note, a stray archive, a nested directory — is **ignored and never
  deleted**. Their presence is logged once at startup with a `WARN` so a
  misconfigured `disk_path` (for example, a shared directory) is visible without
  endangering the foreign data.

Even so, prefer a **dedicated** directory for `disk_path`. The cache assumes it
owns the directory's hex-named files and may delete any of them at any time under
memory or disk pressure.

## Eviction and overflow

Both tiers track their size in bytes and evict least-recently-used entries when
over budget:

- The **memory tier** evicts to stay within `memory_max_size`. Memory-eligible
  entries are already written through to the disk tier on publication (when
  `disk_path` is configured); if an entry was evicted from disk while staying
  hot in memory, the memory-eviction callback re-persists it outside the
  memory-tier lock so a slow disk never blocks in-memory reads and writes.
- The **disk tier** evicts whole files to stay within `disk_max_size`, retiring
  and removing the backing file as it goes. The disk store tracks both physical
  on-disk bytes (`physBytes + retiredBytes + tombstoneBytes`, reported by
  `Stats()` and `jul_cache_bytes{tier="disk"}`) and logical LRU capacity charge
  (`curBytes + retiredBytes + tombstoneBytes`). Retired `.jul-cache-retired-*`
  files still held open by active readers remain counted in physical occupancy
  and capacity admission until their final reader closes; failed file unlinks
  during deletion, purge, or eviction are retained/recorded in
  `.jul-cache-tombstones` and swept on startup so invalidated entries never
  resurrect across restarts. When an entry's representation body fits within
  `disk_max_size` (`bodyLen <= disk_max_size`) and its `JULDCH02`
  metadata+trailer fits within the bounded metadata envelope
  (`maxDiskMetaEnvelope = 1 MiB + 24 B`), its logical LRU capacity charge is
  capped at `disk_max_size` (and `Vary` membership stubs carry `0` logical
  charge while their variants are live) so an exact-boundary object
  (`body == max_entry_size == disk_max_size`, including `Vary` responses) is
  cached without self-evicting while `Stats()` still reports the exact physical
  file size on disk.

`[cache].max_entry_size` (defaulting to `memory_max_size` when unset or `0`)
sets the maximum cacheable response body size (#525 Part 2):

- Entries with `body <= memory_max_size` are captured in memory and stored in
  the memory tier (and persisted to disk when `disk_path` is enabled).
- Entries with `memory_max_size < body <= max_entry_size` (requires `disk_path`
  and `max_entry_size <= disk_max_size`) are streamed to a temporary file under
  `disk_path` during origin transfer — either immediately on `WriteHeader` when
  `Content-Length > memory_max_size` or by spilling the in-memory buffer to disk
  the instant an unknown-length response crosses `memory_max_size` — and are
  published directly into the disk tier without entering or evicting the memory
  tier.
- Entries with `body > max_entry_size` are never stored and increment
  `jul_cache_capture_skipped_total{reason="oversize"}`.

### Capture memory and temporary disk budgets

A cacheable miss is streamed to the client while a copy is captured for
storage. Two independent byte budgets bound concurrent captures (#505, #525 Part 2):

- **In-memory capture budget (`memory_max_size`).** Captures buffered in memory
  (up to `memory_max_size` per response) share one byte budget equal to
  `memory_max_size`. When a chunked response spills to disk upon crossing
  `memory_max_size`, its in-memory buffer and memory-budget reservation are
  released immediately.
- **Temporary disk capture budget (`disk_max_size`).** Concurrent disk-streamed
  captures (`> memory_max_size` up to `max_entry_size`) reserve bytes from a
  separate temporary-disk budget bounded by `disk_max_size`. If concurrent
  temporary files would exceed `disk_max_size`, the capture is aborted, its
  temporary file is removed immediately, the response continues streaming to the
  client uncaptured, and `jul_cache_capture_skipped_total{reason="budget"}` is
  incremented.

Stored memory entries plus in-flight memory captures stay within twice
`memory_max_size`, and in-flight temporary disk captures never exceed
`disk_max_size`. Both skip reasons (`oversize` and `budget`) are counted in
`jul_cache_capture_skipped_total{reason="oversize"|"budget"}` and logged at
debug level, throttled to one line per ten seconds.

## Scoped cache invalidation and origin grouping tags

Jul.IA supports preview-confirmed scoped cache invalidation across the memory
and disk tiers (#621, [ADR 0027](adr/0027-scoped-cache-invalidation.md)) through
the versioned Admin API (`/api/v1/cache/purge*`), the remote CLI
(`jul cache purge`), and the Full Console (**Global & Traffic Controls → Cache**).

### Supported selectors

Every invalidation request specifies one explicit `selector.kind`. Malformed,
empty, unanchored, or cross-scope inputs are rejected with `400 invalid_request`
and never fall back to a broader purge:

| Selector kind | Required fields | Scope and matching rules |
| --- | --- | --- |
| `exact_url` | `url` (`http(s)://host[:port]/path[?query]`) or `host` + `uri` (`/path[?query]`) | Matches one exact normalized hostname and request URI (including the exact raw query string) across `GET`, `HEAD`, and all `Vary` variants. Scheme does not partition Jul's cache key; explicit ports in `r.Host` (e.g. `example.com:8443` or `example.com:443`) are matched literally. |
| `prefix` | `host` + `prefix` (`/path...`) | Matches all cached resources on `host` under a **segment-bounded** path prefix across all query strings, `GET`/`HEAD`, and `Vary` variants. `/articles` or `/articles/` matches `/articles`, `/articles/42`, and `/articles/42?lang=en`, and never matches `/articles-archive`. Root `/` is rejected on `prefix`; use `kind = "host"` for whole-host invalidation. |
| `glob` | `host` + `pattern` (`/path/...`) | Matches paths on `host` against a bounded anchored glob (linear-time segment state machine, no regex). `*` matches zero or more characters within a single path segment (never `/`); standalone `**` matches zero or more full path segments. Bounded to `<= 16` segments, `<= 8` `*` wildcards, and `<= 2` `**` segments. |
| `tag` | `host` + `tags` (`1..16` tags) + optional `tag_match` (`"any"` default or `"all"`) | Matches cached resources on `host` carrying trusted origin grouping tags (e.g. `article:42`). Tags are case-sensitive, isolated per `host`, and never conflated with HTTP `ETag` validators. |
| `host` | `host` | Matches every cached resource belonging to `host` while preserving other virtual hosts on the same Jul instance. Flagged as `broad_scope = true`. |
| `key` | `key` (`METHOD\nhost\nrequest_uri`) | Legacy compatibility selector for one internal cache key. |
| `all` | `all = true` | Purges the entire node cache across all hostnames. Flagged as `broad_scope = true`. |

Percent-encoded path separators (`%2F` / `%2f`) are preserved distinctly from
literal `/` so `/files/a%2Fb` and `/files/a/b` are never equated. Dot-segments
(`/../`, `/./`, `%2e`) and `#fragment` components are rejected. An optional
`route_id` field constrains or derives the host/path scope from the active
route configuration without introducing a separate application registry.

### Trusted origin grouping-tag ingestion

Tag-based invalidation is opt-in via `[cache].tag_headers`:

- **`[cache].tag_headers`** (`hot_reload`, default `[]`): up to 8 non-reserved
  origin response header names (typically `["Cache-Tag", "Surrogate-Key"]`).
  Reserved HTTP headers (`ETag`, `Cache-Control`, `Vary`, `Set-Cookie`,
  `Authorization`, etc.) are rejected at config validation.
- **`[cache].strip_tag_headers`** (`hot_reload`, defaults to `true` when
  `tag_headers` is non-empty): removes configured tag headers from downstream
  client responses on fresh misses, memory/disk hits, `304` revalidations, `206`
  range responses, declined protocol-upgrade HTTP responses, and `stale_if_error`
  fallbacks so internal grouping topology is not leaked to public clients.
- **Validation and bounds:** each tag must be `1..128` visible ASCII characters
  (`0x21..0x7E` excluding `,`, `;`, quotes, and whitespace) and is checked
  against credential-like patterns (`Bearer`, `token=`, `api_key=`, JWTs). Each
  entry stores at most `32` tags and `1024` total tag bytes (from at most `4096`
  raw tag header bytes), and the process-wide reverse index is bounded to
  `65,536` `(host, tag, resKey)` links.
- **Persisted tag-policy provenance:** `JULDCH02` disk entry metadata records
  whether tag-policy provenance was tracked (`TagPolicyTracked`) and the exact
  `TagHeaders` policy active when the entry was ingested. On both live reloads
  and startup rehydration when `tag_headers` is configured, entries whose
  persisted provenance is absent or differs from the active `tag_headers` set
  are conservatively retired rather than re-indexed under fabricated provenance.
- **Conservative failure policy:** if an origin response carries a malformed,
  credential-like, or oversize tag header, or if adding its tags would exceed
  `MaxTotalTagLinks`, Jul still serves the response normally to the downstream
  client (`X-Cache: MISS`) but **skips storing the response in the cache** and
  increments `jul_cache_tag_ingest_skipped_total{reason="invalid"|"oversize"|"index_limit"}`.
  A misconfigured origin header therefore never causes a cache hit that misses
  a subsequent tag purge.
- **Trust boundary:** tags are extracted exclusively from upstream origin
  responses (and merged on `304 Not Modified`), never from client request
  headers.

### Preview, linearization, concurrency fencing, and durability

1. **Non-destructive preview (`POST /api/v1/cache/purge/preview`):** evaluates
   the normalized selector against the live catalog without fetching from the
   origin, warming the cache, or mutating freshness. Returns deduplicated
   `matched_resources` (URLs) and `matched_representations` (`GET`/`HEAD`/`Vary`
   variants, counted once even when resident in both memory and disk), per-tier
   entry and byte counts, `inflight_fills_matched` (plus `inflight_tag_unknown`
   for tag selectors), `count_accuracy` (`exact` or `lower_bound_truncated` at
   the 10,000-entry preview scan bound), a principal-and-selector-bound HMAC
   `preview_token`, and secret-safe resource samples (`?<redacted>`).
2. **Linearization and in-flight fencing (`POST /api/v1/cache/purge`):**
   allocates and durably records a monotonic `fence_sequence` in `genSeq` (and
   `.jul-cache-purge-ledger` / `.jul-cache-gen`), installs an active
   selector fence, and detaches matching in-flight cold-miss fill locks and
   revalidations. Cutoff allocation, durable intent commit, and fence
   installation exclude origin publication under the same cache generation
   lock; a fill cannot acquire a newer publication generation in between them.
   Broad scopes (`host`, `all`, `>= 100` matched resources, or
   truncated preview scans) require explicit `confirm_broad = true`. Any cold
   fill, singleflight leader, synchronous `304` validation, background
   `stale-while-revalidate` refresh, or retired-handler operation that started
   before `fence_sequence` (`startGen <= fence_sequence`) — including a cold
   miss not yet in cache at preview time or whose origin tag is first learned
   when response headers arrive — is blocked from publishing into memory or disk
   upon completion while still delivering its response to its waiting client.
   Retries retain their original cutoffs even if execution completes out of
   order: an older full purge cannot clear a newer selector fence or lower a
   newer full-purge generation barrier. Fills started after a purge's cutoff
   remain cacheable, including disk entries published while that purge is
   completing its disk cleanup. A failed intent commit leaves resident entries
   unchanged and conservatively fences earlier matching origin publications
   because the failed write may still have reached durable storage.
   Tag-purge retries also retain the original logical-resource group, not just
   the tag selector: opaque resource IDs are committed with the cutoff and
   resolve both GET and HEAD representations, including Vary variants, after
   tag changes, tag-ingestion policy changes, fence eviction, or restart.
   Query values are never stored in this retry snapshot. Older tag operations
   without a durable snapshot can use retained in-memory ownership; if neither
   is available, continuation fails explicitly instead of reporting success
   from the current tag index. Use a fresh host or all purge with explicit
   broad confirmation and a new idempotency key to clean up unknown ownership.
3. **Admitted readers and ordinary reloads:** already-admitted downstream
   readers holding an in-memory `*Entry` reference or a reference-counted
   `.jul-cache-retired-*` disk file lease finish streaming uninterrupted, while
   new requests immediately miss and refetch from the origin. Ordinary
   configuration reloads retain warm cache entries unless an explicit purge is
   executed.
4. **Crash-consistent disk durability and outcomes:** `deleteBatchAtMostGen`
   writes target hashes to `.jul-cache-tombstones` before unlinking `JULDCH02`
   files and prunes orphan `Vary` variants on startup rehydration. If a disk
   unlink fails, the entry remains removed in memory, is tombstoned on disk, and
   the response reports `state = "partial"`, `outcome = "partial_failure"`,
   `invalidation_effective = "incomplete"`, and actionable `recovery_guidance`
   so the operator or automation script can retry using `retry_operation_id` or
   the same `Idempotency-Key`.
   Cleanup includes targeted in-flight disk publications and reconciles pending
   tombstones and retired files even if catalog generations have advanced.
   A retry cannot claim effective success while durable cleanup remains unsafe;
   already-admitted readers may finish once their retirement is durable.
   Old fenced fills cannot mutate Vary membership or delete newer variants.

### Console retry confirmation

The Console retries a terminal incomplete operation using its canonical
`retry_operation_id`, without supplying an `Idempotency-Key`. Initial executions
retain a stable generated key; a looked-up operation never borrows that key.
Every retry requires checking **Confirm retry scope** for the displayed
operation. This acknowledgement permits broad invalidation within that
operation's original selector; it is not inferred from a preview, processed
resource counts, or another operation. If no cutoff was recorded, retry can
evaluate the selector against the current cache, so a formerly narrow operation
may now match a broad set of resources.

Clearing a preview, changing credentials or permissions, checking operation
status, or submitting a retry clears the retry acknowledgement. Running
operations cannot be retried until a terminal status is observed. A new
acknowledgement is required if a retry remains incomplete or fails.

## Known limitations

Product limitations — deliberate scope, not defects:

1. **Scoped invalidation is local to one Jul instance and uses bounded selectors.**
   `/api/v1/cache/purge*`, `jul cache purge`, and the Console invalidate cached
   representations by exact URL, segment-bounded path prefix, bounded anchored
   glob (`*` and `**`), trusted origin grouping tag, hostname, legacy key, or
   full cache (#621, [ADR 0027](adr/0027-scoped-cache-invalidation.md)). Arbitrary
   regular-expression purges, unauthenticated data-plane `PURGE` requests,
   cross-instance distributed invalidation, and downstream browser/external-CDN
   cache eviction are outside scope.

2. **Range-aware caching is opt-in, complete-object only, and single-range.**
   By default (`[cache].range_enabled = false`), every request carrying `Range`
   or `If-Range` bypasses the cache (`X-Cache: BYPASS`, decision D05). When
   `[cache].range_enabled = true` (#442 Option A, [ADR 0024](adr/0024-range-aware-cache-option-a.md)),
   a cold range miss fetches the **complete** representation from the origin
   (amplifying origin bandwidth if a client requests only a one-shot tiny slice
   of a large file), never stores `206` fragments, and bypasses multi-range
   requests (`multipart/byteranges`) to the origin under the `[cache].max_ranges`
   bound.

3. **No cross-process / distributed cache.** Each Jul.IA process has one
   process-scoped cache instance shared by its cache-enabled locations. There is
   no shared cache (e.g. Redis) across multiple instances; each node warms
   independently.

4. **Objects larger than `max_entry_size` are never cached.** A response body
   larger than `max_entry_size` (which defaults to `memory_max_size` when unset,
   and can be configured up to `disk_max_size` when `disk_path` is enabled) is
   streamed to the client but not cached. Each such response increments
   `jul_cache_capture_skipped_total{reason="oversize"}`.

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

10. **Mandatory validation buffers or streams the origin's answer to temporary
    storage** before deciding whether to serve the stored body (`304`) or the
    replacement representation (`200`). An answer that exceeds `max_entry_size`
    or turns out to be an event stream costs one extra origin request, and is
    then streamed normally.

Lifecycle behavior:

11. **A reload does not invalidate cached entries.** The cache is process-scoped
    and survives configuration reloads by design, so a routing or backend change
    does not retroactively drop entries stored under the previous configuration.
    A refresh that was already in flight when the reload ran also completes
    against the *old* generation's route and publishes its result. Purge after
    old-generation refreshes drain; a restart alone does not clear the disk
    tier. See [Cache data across reloads](#cache-data-across-reloads) for the
    cutover sequence. Characterized by
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

### Large-object disk-streaming benchmarks (#525 Part 2)

Command (five fixed-iteration samples):

```bash
go test -run '^$' -bench='BenchmarkCacheLargeDisk(Capture|Hit)$' -benchmem -benchtime=100x -count=5 ./internal/cache
```

Environment: Linux 6.18.54.1-microsoft-standard-WSL2, Go 1.27.2, `linux/arm64`,
Qualcomm Oryon (12 cores), `GOMAXPROCS=12`, on the `#525 Part 2` implementation
branch (PR #617). Values below are the five-sample medians (`ns/op`, `allocs/op`,
`bytes/op`):

| Benchmark | Scenario | Median ns/op | allocs/op | bytes/op |
| --- | --- | ---: | ---: | ---: |
| `BenchmarkCacheLargeDiskCapture` | 4 MiB cold-miss disk-streamed capture (`memory_max_size = 64 KiB`, `max_entry_size = 8 MiB`) | 22,536,279 | 380 | 27,530 |
| `BenchmarkCacheLargeDiskHit` | 4 MiB warm disk-streamed hit (32 KiB pooled chunk copy) | 20,810,422 | 281 | 14,281 |

### Range-aware caching benchmarks (#442 Option A)

Command (five fixed-iteration samples):

```bash
go test -run '^$' -bench='BenchmarkCacheRange(HitMemory|HitDisk|OneShotTinyCold)$' -benchmem -benchtime=100x -count=5 ./internal/cache
```

Environment: Linux 6.18.54.1-microsoft-standard-WSL2, Go 1.27.2, `linux/arm64`,
Qualcomm Oryon (12 cores), `GOMAXPROCS=12`, on the `#442 Option A`
implementation branch. Values below are the five-sample medians, including
`origin_bytes/op` to surface both warm origin offload and cold one-shot
full-object amplification:

| Benchmark | Scenario | Median ns/op | origin_bytes/op | allocs/op | bytes/op |
| --- | --- | ---: | ---: | ---: | ---: |
| `BenchmarkCacheRangeHitMemory/bypass` | Warm 64 KiB range on 256 KiB memory object (`range_enabled = false`) | 250,482 | 65,536 | 8 | 501 |
| `BenchmarkCacheRangeHitMemory/option_a` | Warm 64 KiB range on 256 KiB memory object (`range_enabled = true`) | 251,914 | 0 | 20 | 1,120 |
| `BenchmarkCacheRangeHitDisk/bypass` | Warm 64 KiB range on 4 MiB disk object (`range_enabled = false`) | 302,348 | 65,536 | 8 | 501 |
| `BenchmarkCacheRangeHitDisk/option_a` | Warm 64 KiB range on 4 MiB disk object (`range_enabled = true`, 32 KiB chunks) | 393,525 | 0 | 293 | 14,528 |
| `BenchmarkCacheRangeOneShotTinyCold/bypass` | One-shot cold 1 KiB range on 4 MiB object (`range_enabled = false`) | 11,334 | 1,024 | 29 | 6,774 |
| `BenchmarkCacheRangeOneShotTinyCold/option_a` | One-shot cold 1 KiB range on 4 MiB object (`range_enabled = true`, full-object fill) | 3,899,675 | 4,194,304 | 397 | 29,632 |

### Scoped cache invalidation and tag-index benchmarks (#621)

Command (five samples):

```bash
go test -run '^$' -bench='BenchmarkCacheHit$|BenchmarkCacheHitWithTagIngestionAndFences$|BenchmarkScopedPurge' -benchmem -benchtime=200ms -count=5 ./internal/cache
```

Environment: Linux 6.18.54.1-microsoft-standard-WSL2, Go 1.27.3, `linux/arm64`,
Qualcomm Oryon (12 cores), `GOMAXPROCS=12`, on the `#621` implementation branch.
Values below are the five-sample medians:

| Benchmark | Scenario | Median ns/op | allocs/op | bytes/op |
| --- | --- | ---: | ---: | ---: |
| `BenchmarkCacheHit` | Baseline warm memory hit (1 entry, tag ingestion off) | 2,402 | 16 | 1,256 |
| `BenchmarkCacheHitWithTagIngestionAndFences` | Warm memory hit with `tag_headers = ["Cache-Tag"]`, `strip_tag_headers = true`, and 1,000 indexed tagged resources | 2,783 | 15 | 1,248 |
| `BenchmarkScopedPurgeTagIndex` | Invalidate `article:42` (2 representations) + refill in a 2,000-entry cache via reverse tag index | 7,835 | 36 | 2,208 |
| `BenchmarkScopedPurgePrefixScan` | Invalidate `/articles/hot/` (8 representations) + refill in a 2,000-entry multi-host cache | 251,152 | 62 | 38,256 |
| `BenchmarkScopedPurgePreviewLargeSelection` | Non-destructive `PreviewScope` over 2,000 matched entries with 20 safe resource samples | 1,197,337 | 8,062 | 900,622 |

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
| 1 — Conformance / behaviour matrix | ✅ | [Executable behaviour matrix](#executable-behaviour-matrix) and [audit record](audit/old/2026-08-07-cache-recertification.md) |
| 2 — Published benchmark numbers | ✅ | Six refreshed benchmarks above, including mandatory validation and populated-variant invalidation |
| 3 — Known-limitations list | ✅ | 13-item list above and [known-limitations.md](known-limitations.md), separated by product/conservative/performance/lifecycle meaning |
| 4 — Semver-guarded config/API contract | ✅ | v1 config freeze and #126 metric compatibility guard; the released `jul_cache_events_total` help remains unchanged |
| 5 — Soak evidence | ✅ | Historical 1h cache soak plus post-correction focused run: 422,042 requests, 0 errors, all five cache result classes, stable goroutine/FD/heap trends — [evidence](soak-evidence.md#2026-08-07--cache-recertification-correctness-soak-linux-30-seconds-16-workers) |
| 6 — Runnable example + docs | ✅ | `testdata/cache.toml`, this guide, benchmark and soak records |
| 7 — Security / threat note | ✅ | Threat note above plus real authenticated identity-isolation and foreign-file tests |
| 8 — Fuzzing/parser robustness | ✅ | `TestParseCacheControlIsTotal` and directive matrices |
| 9 — Self-explanatory Console surface | ✅ | Runtime overview and cache purge/delete surfaces; lifecycle limitations remain explicit |

## Bounded cache inspection

The unreleased CACHE-OPS-02 inspector (#622) reads cache metadata without sending
an origin request, invoking an application handler, populating the cache,
changing LRU order, sweeping disk tombstones or leasing response files. Use the
Full Console's Traffic Controls → Cache → **Inspect cached resources**, the
versioned `POST /api/v1/cache/inspect` API, or `jul cache inspect`.

Inspection requires an authenticated administrator and `cache:inspect` in RBAC
mode. This new permission is **not** added to built-in viewer, operator or
auditor roles. Assign it explicitly through a custom role; existing `*` and
`cache:*` grants retain their normal wildcard semantics. Legacy token mode
retains its existing all-actions authority, but an unconfigured/empty token is
not accepted for inspection. `cache:purge` alone does not grant inspection;
inspection does not grant purge or its existing preview. Configuration ownership
(file-owned versus managed) does not prevent read-only inspection.

Select an explicitly configured named host (literal request port is preserved)
or an existing route with explicit server names. Whole-node `all`, unconfigured
hosts and ambiguous unnamed/wildcard-only host enumeration are rejected. The
existing selector parser supports exact URL/URI/key, segment prefix, anchored
glob and trusted origin tags. Exact matching reuses the purge input/redaction
contract; queries are matched exactly but omitted from output. Query-distinct
resources can display the same path and have different opaque resource IDs.

```sh
jul cache inspect --endpoint https://admin.example.com --token-file admin.token \
  --host portal.example.com --prefix /articles --json
jul cache inspect --endpoint https://admin.example.com --token-file admin.token \
  --host portal.example.com --uri /articles/42 --page-size 10
```

The API takes `{ "selector": { "kind": "host", "host": "portal.example.com" },
"page_size": 50 }`. Repeat the **same selector** with the returned `cursor` to
read another page. Keep exact-match secrets out of shell history/process lists;
use a protected API JSON input when an exact URI contains sensitive values.
No live probe is included. Inspection retries never automatically fetch an
origin or retry a purge.

### Bounds, pagination and consistency

- A capture examines at most 10,000 catalog base-key candidates and retains at
  most 256 logical resources, with at most 32 representation details per
  resource. Vary/tag display lists and path lengths have separate bounds.
- A page contains at most 1–50 resources (default limit 50) and at most 2 MiB
  of encoded response metadata. Wide rows can shorten a page; follow its cursor.
  Empty captures return an empty page. At most two builders execute
  concurrently. Retention admits at most 16 snapshots and 8 MiB of aggregate
  encoded metadata. A capture exceeding its byte limit is explicitly rejected.
- Snapshots expire within 60 seconds (earlier when contained decision evidence expires) and are reclaimed by bounded timers even
  without later inspection traffic. Cursor identities and page positions are
  authenticated and bound to principal, normalized scope, authorization
  generation and serving configuration identity. Every page reauthenticates;
  revocation, scope/offset tampering, reload, restart and expiry reject reuse.
- The captured catalog is not a global transaction with tier occupancy, current
  configuration or recent decisions. Tier churn can occur immediately after
  capture. Refresh obtains a new observation; it does not promise a future HIT.
- `count_accuracy=lower_bound_truncated` means candidate enumeration was cut
  short. `exact` describes the bounded catalog count when enumeration completed,
  even if the resource list or detail list is truncated separately. Never sum
  identical snapshot totals from multiple pages.
- Cancellation discards the builder result before retention/publication.
  The two-second build deadline is checked around bounded work; a wait on an
  existing cache mutex is not instantaneously cancelable and can exceed that
  deadline before returning. Large-purge contention remains tracked in #667.

### Reading metadata and policy

A **logical resource** is an authority plus exact request URI. A
**representation** is an owned method/Vary member. Memory and disk can contain
copies of the same representation. Vary lookup stubs count as tier entries,
not response representations. Representation body bytes are distinct from
measured physical tier occupancy (including metadata, retired files and
pending tombstones) and configured byte limits. Memory occupancy uses the
cache's existing footprint estimate; it is not a process RSS measurement.

`origin_age_seconds` uses RFC-corrected origin age, not insertion age. New entries
record insertion time; successful 304 merges record validation time while
preserving known insertion time. Older disk metadata returns
`legacy_unavailable` for missing publication provenance. No inspection body scan
or new disk rehydration is performed. Fields missing on older entries are not
reconstructed as historical facts.

The server records origin s-maxage/max-age/Expires or gateway fallback provenance,
targeted directive use, numeric freshness/SWR/SIE lifetimes and validation/auth
restrictions at the owning policy decision. Current global fallback values and
supported range/fill/tag controls appear separately. Route overrides and gateway
freshness caps remain unsupported (#623). A route's cache boolean controls
activation; cached entries can remain after disabling a route or ordinary
reload. `retained_policy` compares recorded entry policy with current global
policy. A displayed freshness lifetime is not an absolute wall-clock guarantee:
origin age, validation, request restrictions, invalidation and eviction still
matter. A MISS is not proof of storage.

### Recent observed decisions and privacy

Metadata inspection works with sampling disabled. To record actual recent
cache-decision stages, explicitly enable:

```toml
[cache]
enabled = true
memory_max_size = "64m"
default_ttl = "60s"
decision_samples = true # default false; hot-reloadable
```

The buffer is memory-only, capped at 256 records. Each record expires after
60 seconds; a cleanup timer may drop newer records early. Disabling sampling
clears it. It is a lossy record of stages, not every request or permanent
history. Resource correlation uses a process-local keyed HMAC; raw query values,
Vary values, cookies, credentials, request headers, bodies and raw errors are
not retained in samples. Overlong safe display input is not sampled. No URLs,
resource IDs or tags become metric labels and no URL log is added.

Reason codes originate at actual reuse, admission, capture, fill-lock and
capacity-eviction owners. `fresh_hit`, `stale_while_revalidate`, `stale_reuse`,
`stale_if_error`, `revalidation_required` and `validated_reuse` distinguish reuse
and validation stages. Storage restrictions include `response_no_store`,
`response_private`, `response_set_cookie`, `auth_storage_restricted`,
`auth_reuse_restricted` and `vary_star`. Upgrade/range/SSE bypasses, capture
oversize/budget/not-stored, publication failure/fencing, disk read failure,
fill-lock fallbacks and memory/disk capacity evictions have separate reasons.
A failed/fenced publication is not claimed to be a successful store.

`evidence_state=disabled`, `no_record`, `observed` or `unavailable` is separate
from resource availability. A tag-scoped search cannot infer tags on absent
historical responses; use a safe exact lookup for their recent evidence.
No record does not mean no request occurred or a resource was never cached.

### Errors and recovery

API states include available, empty, disabled, unavailable, partial and
truncated; detail provenance can independently be legacy/unavailable. A failed
disk tier is partial even when metadata remains cataloged. Stale/expired
observations remain observations, not current protection evidence.

401/403 require credentials or an explicit grant. 400 (`invalid_request`)
requires correcting scope/page input or starting a new capture after cursor
expiry/reload. 429 (`rate_limited`, `Retry-After: 1`) means builder/retention
admission is full. 503 (`storage_unavailable`) means a build failed, was canceled
or exceeded metadata bounds: narrow scope or retry a new inspection. Existing
body/media-type errors retain the API error envelope. Successful CLI reads exit
zero even for explicit partial/truncated states; automation must inspect those
fields. There is no automatic cursor restart or page accumulation.

Purge uses the existing preview → confirm → execute → observe/recover flow and
its own permission. #666 (unfinished-purge restart protection), #667
(large-purge latency) and #668 (legacy purge outcome visibility) remain inherited
limitations; inspection does not implement them. No release, GA or soak claim is
made. NGINX migration impact is **not applicable**: this is a Jul-specific
operator interface and does not change imported request/cache semantics.


New publication provenance uses one compact immutable record shared by resident
copies and the catalog. The memory-tier footprint estimate charges 96 additional
bytes per new representation (including pointer/allocation allowance); tiny-cache
capacity and small-object resident counts can therefore decrease. Inspection
itself never performs those evictions. Recorded publication SIE values are
historical; `effective_sie_seconds` comes from the current existing stale-error
decision owner and can change on reload. No-cache zero-TTL retention correctly
reports the gateway retention fallback rather than attributing that lifetime to
an origin zero directive.

Route scopes intersect the existing selector with the configured exact/prefix
path, using the shared route-path normalization. This is configured path scope,
not historical request-route attribution: overlapping routes and predicates are
not reconstructed from absent request state. Regex route-path inspection is
explicitly unavailable; configured-host inspection remains available. Tag samples
can be correlated to **current** selected resource membership, labeled
`observed_current_membership`; historical tag membership remains unavailable.
`concurrent_change` distinguishes observed sequence changes from unknown
independent churn; an unchanged publication sequence does not prove no eviction.

### Inspection decision attribution across reuse paths

`fresh_hit`, `validated_reuse` and `stale_reuse` apply to conditional, full and
range responses through the same serve owner. A successful 206 HIT is therefore
observable too. Capture rejection/publication evidence includes background
validation and complete-object range fills; a private/no-store/cookie rejection
is separate from the later range fallback. Recorder-based replacements also
record invalid trusted tags and `vary_request_mismatch` when validation/range
processing modified a header named by Vary. Enabled range requests retain the
`request_no_store` reason, and tag-index admission limits record
`tag_capture_rejected`. `capture_incomplete` records a declared-length/body-length
mismatch at the shared capture builder. These codes retain no header names or
values. Disk lookup open/metadata/body failures and small-response write-through
failures are recorded at their I/O owners. A failed write-through can coexist
with successful memory publication; an ordinary disk miss is not a read failure.

A stored stale deadline can originate from SWR or be extended after an origin
error. New immutable provenance preserves that distinction through cloning and
disk restart. `stale_if_error` records the failed-validation eligibility decision;
`stale_if_error_extended` records selection of the extended deadline;
`stale_if_error_reuse` records actual reuse under that deadline. Only original
SWR deadlines produce `stale_while_revalidate`. Earlier provenance formats
without the discriminator produce `stale_basis_unavailable`, even when their
other publication metadata is available. This changes observation, not cache
reuse/expiry policy. A successful 304 or replacement resets the deadline basis.

Unknown insertion/validation dates are omitted from inspection JSON; a missing
date is not a validation in year 0001. Known timestamps retain RFC3339 encoding.
