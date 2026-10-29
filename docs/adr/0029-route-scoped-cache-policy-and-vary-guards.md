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
table on `LocationConfig` with hot-reload lifecycle (`SubCache`):

- `default_ttl` (`*config.Duration`): nil inherits `[cache].default_ttl`;
  explicit `"0s"` disables fallback caching when the origin omits explicit
  freshness (`s-maxage`, `max-age`, `Expires`).
- `max_ttl` (`*config.Duration`): nil applies no route cap; when set, caps
  fresh lifetime from above (`effective_ttl = min(base_ttl, max_ttl)`).
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

- Every cached location receives a deterministic `RouteScope` derived from its
  listener, normalized `server_names`, match type, path, canonical predicates,
  preflight widening, and optional `route_id` (excluding backend targets so
  `#624` retains clean ownership of backend-cutover invalidation).
- When multiple cached locations or predicate-bearing routes share a server,
  base-key Vary stubs maintain `RouteVary map[string][]string` and variant keys
  include `route\x1f<RouteScope>`, allowing overlapping routes to coexist in
  memory and on disk without overwriting each other.
- `RouteEpoch` starts at `0` (preserving warm memory and disk hits across
  reloads and restarts) and increments only when a previously active route
  scope is removed or has `cache = false`, preventing a later recreated route
  with a reused `route_id` from inheriting pre-deletion entries.

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
