# ADR 0029 — Route-scoped cache policy, request-cookie bypass, and Vary transformation guards

- **Status:** Accepted
- **Date:** 2026-10-29
- **Issues:** `#623` (`CACHE-OPS-03`), `#672`

## Context

Jul's two-tier HTTP response cache (`[cache]`) previously provided a single
process-wide policy (`default_ttl`, `stale_while_revalidate`, `stale_if_error`)
with a boolean per-location opt-in (`cache = true`). Operators running mixed
workloads (for example, static assets alongside public catalog APIs and
cookie-authenticated web routes) needed:

1. **Per-route freshness overrides and upper bounds (`#623`)**: the ability to
   override fallback `default_ttl`, `stale_while_revalidate`, and
   `stale_if_error` per location, require explicit origin freshness via
   `default_ttl = "0s"`, and cap overly long upstream lifetimes via `max_ttl`.
2. **Conservative request-cookie bypass (`#623`)**: the ability to skip shared
   cache lookup and storage when specific session cookies (`bypass_cookies`) or
   any `Cookie` header (`bypass_any_cookie = true`) are present, without
   putting cookie values into cache keys, logs, or inspection payloads.
3. **Overlapping route cache isolation (`#623`)**: when multiple routes on the
   same server share a path (differing by route predicates such as headers,
   methods, query parameters, or cookies), cached representations must be
   isolated by route ownership without evicting each other's base-key slot
   ("one-slot route thrashing").
4. **Sensitive and proxy/CGI-transformed `Vary` guards (`#672`)**:
   - `Vary: Cookie` and `Vary: Authorization` must never be stored or reused in
     Jul's shared cache across any path (admission, lookup, outer-layer header
     checks, `304 Not Modified` merges, `5xx` `stale-if-error`, Range fills, or
     legacy disk rehydration).
   - When an HTTP reverse-proxy or FastCGI/uWSGI handler rewrites, synthesizes,
     or strips a request header before forwarding to the upstream (including
     `Forwarded`, `X-Forwarded-*`, `X-Real-IP`, hop-by-hop headers, per-request
     `Connection`-nominated headers, `proxy_headers` `set_*`/`remove`,
     `fastcgi_params` `HTTP_*` overrides, or CGI hyphen/underscore aliases), a
     response carrying `Vary` on that header cannot be matched safely against
     the raw client request and must be rejected from shared caching, while
     preserving normal `Vary` headers (`Accept-Encoding`, `Accept-Language`,
     `Accept`, `User-Agent`).

## Decision

### 1. `[servers.locations.cache_policy]` Schema and Precedence

We add an optional `*LocationCachePolicy` (`[servers.locations.cache_policy]`)
table on `LocationConfig` with hot-reload lifecycle (`SubCache`) and non-negative
durations bounded at `876000h` (100 years):

- `default_ttl` (`*config.Duration`): nil inherits `[cache].default_ttl`;
  explicit `"0s"` disables fallback caching when the origin omits explicit
  freshness (`s-maxage`, `max-age`, `Expires`).
- `max_ttl` (`*config.Duration`): nil applies no route cap; when set, caps
  fresh lifetime from above (`effective_ttl = min(base_ttl, max_ttl)`). Setting
  `max_ttl = "0s"` caps fresh lifetime to zero and disables fresh storage on
  the route without retaining responses for subsequent conditional `304`
  revalidation.
- `stale_while_revalidate` (`*config.Duration`): nil inherits
  `[cache].stale_while_revalidate`; explicit `"0s"` disables fallback SWR.
- `stale_if_error` (`*config.Duration`): nil inherits `[cache].stale_if_error`;
  explicit `"0s"` disables fallback SIE.
- `bypass_cookies` (`[]string`, max 32 RFC 6265 cookie-name tokens, max 128
  bytes each, matched case-insensitively) and `bypass_any_cookie` (`bool`),
  mutually exclusive.

Explicit origin exclusions (`no-store`, `private`, `Set-Cookie`, `Vary: *`,
`Vary: Cookie`, `Vary: Authorization`, and uncacheable `Authorization`) always
take precedence over route overrides.

### 2. Fail-Closed Cookie Bypass and Policy Tightening across Hot Reload

- `evaluateCookieBypass` parses request `Cookie` headers with zero allocations
  on the common path, matching cookie names case-insensitively without retaining
  or logging cookie values. If `Cookie` headers exceed `16` lines, `8192`
  bytes, or `128` pairs, or contain invalid token/control bytes while cookie
  bypass is active, Jul fails closed (`cookie_bypass_malformed`) and bypasses
  both lookup and storage.
- Stored entries record `evidenceNoRequestCookies` (`Flags |= 32`) and
  `BypassDigest [16]byte`. When a hot reload tightens cookie bypass or lowers
  `max_ttl`, `default_ttl`, `stale_while_revalidate`, or `stale_if_error`,
  `reuseDecisionWithPolicy` re-evaluates existing memory and disk entries
  immediately against the active route policy (`route_policy_mismatch`), and
  `CommitRoutePolicies` advances the per-route publication floor so in-flight
  origin fills started under the prior policy cannot publish late.

### 3. Multi-Route Isolation without One-Slot Thrashing

- Cache ownership and matching compatibility are separate. `RouteScope` is
  restart-stable and excludes predicate values and their digests. A configured
  `route_id` supplies identity within its listener/normalized-server-name
  boundary and survives declaration reordering. IDs remain optional.
- Without an ID, ordinary routes use match type/path coordinates. Anonymous
  predicate routes additionally use an ordinal among anonymous predicates on
  that same path/type. Inserting unrelated paths or identified routes does not
  shift them. Reordering/inserting ambiguous anonymous same-path predicates can
  make affected slots cold; use IDs to preserve ownership through those edits.
  There is no new persistent identity registry or key provisioning requirement.
- Compilation may reuse normalized server-boundary bytes within a batch, while
  preserving the exact versioned framing for scopes and matcher proofs. The
  HTTP handler retains at most one immutable policy/digest memo. Reuse requires
  equality of the complete effective `CachePolicy` value, including ownership
  epoch and cookie rules; concurrent requests receive their own matching
  snapshot. This memo changes no persistent evidence or request eligibility:
  cookie parsing, ownership, authorization and Vary guards still run per request.
- Every compiled production route carries a separate matcher compatibility
  proof: HMAC-SHA-256 through the existing private `versionid.Context`, domain
  `cache-route-matcher-v1`. Its length-framed input binds scope, normalized match
  type, path, canonical method/header/query predicates and preflight widening.
  Backend targets remain excluded (`#624`). The proof is retained only for
  active routes in the versioned `.jul-cache-route-epochs` manifest, never in
  cache keys, response-entry metadata, logs, inspection output or metrics.
- Matching proofs in the same durable key context preserve the epoch and warm
  memory/disk entries across unchanged or unrelated reloads and process restarts.
  Changed predicates (including changes while stopped), preflight changes,
  incomparable key contexts, or unverifiable previous matcher state advance the
  affected route's epoch before reuse/publication. Returning to an earlier
  predicate never resets its epoch. Each route handler binds to its compatible
  committed matcher generation (`boundGen`) and verifies both matcher-proof and
  policy compatibility before acquiring the active `RouteEpoch` in
  `effectivePolicy`; a request that selected an older handler before a reload
  and resumed after a matcher or policy change receives the sentinel
  `incompatibleRouteEpoch` (`math.MaxUint64`) and is barred from both cache
  reuse and publication, even if its origin operation started after the reload's
  publication floor. Existing route policy tightening is still evaluated
  separately; matching proof equality cannot weaken it.
- Production uses the already-provisioned effective-version key (ADR 0025).
  Missing/unprotected keys still fail startup through that owner's contract.
  Embeddings with no supplied context use `versionid.Process()`: unchanged
  reloads remain warm but restart uses an incomparable context and becomes cold.
  This is an explicit fallback for embeddings, not production's normal behavior.
- The unkeyed-predicate-hash design provides an offline guessing verifier; a
  process nonce loses restart reuse; unconditional reload invalidation discards
  compatible state; full-configuration proof changes invalidate unrelated routes.
  A route-specific keyed proof outside ownership provides the required equality
  with bounded reuse loss and reuses Jul's existing confidentiality authority.
  No raw predicate or unkeyed predicate verifier is persisted. This does not
  claim arbitrary URLs, other Vary inputs, or operator IDs never contain secrets.
- Entries without compatible route ownership (including pre-feature or prior
  scope-encoding entries) incur a one-time cold miss. Freshness evidence uses a
  versioned binary policy digest; older evidence is conservatively recomputed
  from retained response headers. The digest encodes every reuse-policy field
  and excludes collection/overlap bookkeeping and the separately enforced Vary
  guard. This avoids per-hit formatting/allocation of the expanded policy.
- When multiple cached locations or predicate-bearing routes share a server,
  base-key Vary stubs maintain `RouteVary map[string][]string` and variant keys
  include `route\x1f<RouteScope>`, allowing overlapping routes to coexist in
  memory and on disk without overwriting each other.
- `RouteVary` records are pruned with the stub's 64-variant membership cap, so
  route churn cannot accumulate unbounded route records behind a bounded
  representation list. This shares the existing per-resource capacity limit;
  it does not add per-route storage quotas.
- `RouteEpoch` starts at `0` and advances on incompatible/unverifiable matching,
  route removal/cache disable, and bounded-history compaction. The manifest's
  version 1 envelope carries epochs and active keyed matcher proofs; legacy flat
  epoch maps retain invalidation evidence but cannot establish matcher equality,
  so resident scopes incur a one-time cold miss. Changed manifest state is
  persisted before the new epoch snapshot and publication floors become visible.
  Unchanged modern evidence needs no rewrite/fsync; a previous write failure
  still requires a successful repair before the disk tier is re-enabled. Epoch metadata is
  recovered once, before attaching disk callbacks and snapshotting the disk
  catalog; recovery never performs disk cleanup while holding `genMu`.
- Retired history is bounded by 1,024 plus the configured active-route count.
  Compaction first advances a durable global epoch floor above every retained
  epoch. All older scoped entries and in-flight publications become ineligible,
  including disk-only entries. Only then can individual tombstones be forgotten.
  Compaction may make other scoped routes cold; it cannot reset forgotten
  compatibility evidence to zero.
- Unreadable, malformed, null, overflowing, oversized or unknown-version epoch
  metadata, including malformed matcher proofs/unknown envelope fields, invokes
  existing fail-closed disk cleanup before rehydration. Reads are bounded to
  1 MiB. Corruption recovery retains an epoch floor above observed decoded
  entries before dropping catalog visibility. Fresh compatibility state cannot
  replace the restart marker while durable cleanup remains unrecoverable; this
  prevents surviving old files from being reauthorized if later epoch writes
  succeed. Disk-entry epoch decoding uses the manifest's numeric bound so floor
  derivation cannot wrap on corrupt metadata. A persistence/sync failure retires
  the disk dataset through existing
  tombstone recovery, attempts to leave a corrupt restart marker, and disables
  disk reads/publication until a later route commit persists successfully.
  Memory remains protected by the live epochs. This is best-effort durable
  cleanup, not secure erasure: if the filesystem refuses metadata writes,
  invalidation, and cleanup, Jul cannot guarantee recovery after a crash. Disk
  failure is logged; restore filesystem health and clear affected cache files
  before restart. Existing purge-recovery limits remain owned by `#666`.

### 4. Sensitive and Proxy/CGI-Transformed `Vary` Guards (`#672`)

- `vary_security.go` rejects `Vary: Cookie` and `Vary: Authorization`
  (`vary_sensitive_rejected`) across all admission, lookup, `304`, `5xx`,
  Range, and disk-rehydration paths.
- `handler.ProxyTransformedRequestHeaders` and
  `handler.CGITransformedRequestHeaders` derive the exact header sets rewritten
  or stripped by the HTTP reverse proxy and FastCGI/uWSGI handlers, plus
  per-request `Connection`-nominated headers and CGI hyphen/underscore aliases,
  rejecting unsafe `Vary` responses (`vary_proxy_rewrite_rejected`,
  `vary_cgi_rewrite_rejected`).
