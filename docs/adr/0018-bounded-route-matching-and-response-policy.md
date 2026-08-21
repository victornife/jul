# ADR 0018 — Bounded route matching and response policy

- **Status:** Accepted
- **Date:** 2026-08-21
- **Deciders:** Jul.IA maintainer
- **Applies to:** location matching, route selection, the HTTP request pipeline, response-header
  policy, CORS, the response cache, compression, WebSocket upgrade, native gRPC passthrough, gRPC
  transcoding, configuration validation and lint, configuration lifecycle and reload, the typed
  admin patch API, route projections and the route-test surface, the Console, and the NGINX importer
- **Source:** #117 (`[ADR][CGC-04]`), re-audit of `main` at `28e5bd38`
- **Related:** [ADR 0016](0016-inbound-identity-and-backend-peer-trust.md) (forwarded identity is
  constructed, then overridden — an order this record must not disturb),
  [ADR 0011](0011-reload-plan.md) (one reload transaction, one closed-world lifecycle registry),
  [ADR 0017](0017-upstream-resilience-and-overload-control.md) (generation ownership, bounded
  controls), [ADR 0014](0014-operability-surfaces.md) (one backend implementation behind every
  surface), [ADR 0009](0009-two-tier-editing.md) (Quick vs Designer editing),
  [ADR 0013](0013-project-operating-model-and-completeness.md) (portfolio entry, D12)

## Context

Jul selects a location by path alone. `MatchConfig` has exactly two fields, `type` and `path`, and
`serverRoute.matchLocation` resolves a request to one location using exact, then longest non-root
prefix, then regex in declaration order, then the `/` fallback. There is no way to constrain a route
by method, by a request header or by a query parameter; there is no way to add, replace or remove a
response header; and there is no CORS implementation at all. `LocationConfig.Headers` — the field
whose name suggests otherwise — sets headers on the *upstream request*, not on the response to the
client.

D12 (§7 of `docs/specs/core-gateway-completeness.md`) placed a bounded version of all three
capabilities inside the core-completeness boundary and excluded the expression languages, scripting,
body matching and progressive-delivery features that usually arrive with them. #117 recorded that
decision and left the public contract open. This record closes it.

Four facts discovered during the re-audit shaped this record more than the issue text did.

**A response header set by any layer outside the cache handler is stored in the cache entry.**
`Cache.fetchAndStore` runs the downstream handler and then calls
`buildEntry(r, cw.status, w.Header(), cw.buf.Bytes(), now)`. `w.Header()` is not a snapshot: it is
the one header map shared by every wrapper down to the real connection, read *after* the whole stack
has unwound. Compression sits outside the router and therefore outside the cache, and
`compressWriter.startCompress` sets `Content-Encoding` and deletes `Content-Length` on that same map
before the read happens. A characterization test written for this record reproduces the consequence
on current `main`: a cacheable, compressible response is stored with `Content-Encoding: gzip` and an
*uncompressed* body, and the next matching request is served that entry — 2400 bytes of plain text
under a `gzip` label, which no browser can decode. This is a pre-existing defect, tracked separately,
but it is decisive here: a response policy placed anywhere inside the cache, or placed outside it
without fixing the capture, would store `Access-Control-Allow-Origin: https://one-tenant.example`
in an entry keyed without `Origin` and serve it to every other origin. The cache is a shared,
disk-backed, process-lifetime object that outlives handler generations and process restarts, so the
leak would also outlive the configuration that caused it.

**`matchLocation` returns a winner, not a candidate set.** Every tier commits: the exact tier returns
the first exact location whose path is equal, the prefix tier computes one longest match, the regex
tier returns the first pattern that matches. Predicates require the opposite shape — a route that
fails a predicate must not consume the request — so route selection becomes an enumeration with
fallthrough. That is a change to the *shape* of the algorithm, and the record below fixes the exact
ordering rather than leaving it to be discovered during implementation.

**A location's operational identity is `(listen, server_names, match_type, path)`, and predicates
break its uniqueness.** `findLocation` resolves every typed patch through those four coordinates and
*rejects* a target that resolves to more than one location. `locationKey` in the diff, the lint
duplicate-match rule, and `AuthScope`/`WAFScope`/the rate-limit scope in `internal/app` all key on
the same tuple or less. The whole point of method and header predicates is that two locations can
now legitimately share a path, so the addressing scheme needs an explicit, additive extension before
#145 lands, not after.

**The admin route-test endpoint reimplements routing.** `internal/admin/routetest.go` has its own
`bestServer`/`bestLocation` rather than calling the router, and its request type already accepts
`Method` and `Headers` that `testRoute` ignores. A second matching implementation that is merely
stale today becomes a second matching *semantics* the moment predicates exist.

## Existing architecture

| Concern | Where | Behavior before this record |
| --- | --- | --- |
| Match schema | `internal/config/schema.go` `MatchConfig` | `type` (`exact`/`prefix`/`regex`) and `path`, nothing else |
| Match validation | `internal/config/validate_location.go` `validateMatch` | type is one of three, path non-empty, `regex` compiles |
| Host selection | `internal/router/host.go` `hostScore`, `addrRouter.match` | 3 exact, 2 leading wildcard, 0 none; `sc > bestScore` so ties go to the first declared; falls back to the first server block on the address |
| Location selection | `internal/router/matcher.go` `matchLocation` | exact (first declared) → longest non-root prefix (`len > bestLen`, so ties go to the first declared) → regex in declaration order → `sr.fallback` |
| `/` fallback | `internal/router/router.go` `buildServerRoute` | `sr.fallback` is reassigned on every `prefix "/"` location, so the **last** declared wins |
| No match | `internal/router/router.go` `Router.For` | `http.NotFound` — 404, no `Allow`, no 405 anywhere on the user path |
| Method handling | — | none; `internal/handler/static.go` alone returns 405 for methods it cannot serve |
| Query parsing | — | never parsed on the user request path; `RawQuery` is forwarded verbatim |
| Response headers | — | no configuration surface; only a WASM plugin can call `set_response_header` |
| CORS | `internal/observability/trafficsources.go` | `Origin` values and OPTIONS counts are observed; no CORS logic exists |
| Upstream request headers | `internal/handler/proxy.go` `applyProxyHeaders` | `loc.Headers` with `$var` expansion, applied **after** `setCanonicalXForwarded` so an explicit operator value wins (ADR 0016, #136) |
| Global chain | `internal/app/factory.go` `globalChain` | RequestID → ClientAddress → Tracing → Metrics → AccessLog → Recover → Compression, wrapping the per-address router |
| Per-location chain | `internal/app/factory.go` `locModifier`, `internal/router/router.go` | plugins → ClientCert → Auth → RateLimit → WAF, wrapping BodyLimit, wrapping the action |
| Cache placement | `internal/app/factory.go` `withCache` | applied by the action builders, i.e. **inside** the WAF and inside everything else; gRPC passthrough and transcoding are never cached |
| Cache capture | `internal/cache/cache.go` `fetchAndStore`, `internal/cache/http.go` `cacheWriter` | body buffered by the writer; headers re-read from the shared `w.Header()` after the stack unwinds; hop-by-hop and `X-Cache` stripped at build time |
| Cache key | `internal/cache/cache.go` `key`, `variantKey` | `method \n lower(host) \n RequestURI`, plus a `Vary` stub and per-variant suffixes, max 64 variants |
| Cache lifetime | `internal/cache/diskstore.go`, `internal/app/factory.go` | process-lifetime singleton, gob-encoded on disk, rehydrated at startup, **no generation or policy stamp**, never purged by reload |
| Writer wrappers | `internal/respwriter/respwriter.go` | `Wrap` reflects the underlying writer's optional interfaces into one of sixteen variants and implements `Unwrap` for `http.ResponseController` |
| Patch addressing | `internal/admin/patch_helpers.go` `findLocation` | `(listen, server_names, match_type, path)`; more than one match is an error |
| Policy scopes | `internal/app/wiring.go` `AuthScope`, `WAFScope`; `factory.go` rate-limit scope | `listen \| names \| match.path` — match *type* is already omitted, so an exact and a prefix location on the same path already share one scope |
| Lint duplicates | `internal/config/lint.go` | warns that a repeated `(type, path)` is "unreachable", naming the **first** as the winner |
| Route test | `internal/admin/routetest.go` | independent `bestServer`/`bestLocation`; `Method` and `Headers` accepted and discarded |
| Importer | `internal/migrate/nginx/translate.go` | `if`, `limit_except` and `add_header` are recorded as skipped findings with a source line and a reason |

## Constraints from accepted decisions

1. **ADR 0016 §12 and #136.** `setCanonicalXForwarded` clears every client-supplied `X-Forwarded-*`
   and rebuilds the chain from Jul's own trusted context; `applyProxyHeaders` runs *after* it so an
   explicit operator value wins. `$remote_addr`, `$realip_remote_addr` and
   `$proxy_add_x_forwarded_for` keep their accepted meanings. This record adds no stage to that
   sequence and moves none of it. Response policy operates on the *response*; it has no access to
   the outbound request, and CORS never writes a request header.
2. **ADR 0011.** One reload transaction, one closed-world lifecycle registry. Every field introduced
   here gets exactly one registry entry, compiles during `Prepare`, and can only become live through
   `Publish`.
3. **ADR 0017.** Generation ownership already guarantees that a request keeps the handler tree it
   started with. This record adds no second generation concept.
4. **ADR 0014 and #108.** One server-side implementation behind raw TOML, the typed API, the Console
   and the importer. The Console does not re-derive matching or CORS semantics.
5. **D12 exclusions.** No expression language, no scripting, no body matching, no arbitrary origin
   regex, no weighted/canary/mirroring routing, no response-body rewriting, no automatic 405 unless
   this record designs one, and no universal NGINX `if` emulation.

## Decision

### 1. The match block grows three predicate fields, and nothing else

```toml
[[servers.locations]]
proxy_pass = "http://api"

[servers.locations.match]
type = "prefix"
path = "/api/"
methods = ["GET", "POST"]

[[servers.locations.match.headers]]
name = "X-Tenant"
op = "exact"
value = "public"

[[servers.locations.match.query]]
name = "version"
op = "exact"
value = "v2"
```

`methods` is a list of strings. `headers` and `query` are **arrays of tables**, not maps.

Arrays of tables are chosen for three reasons that a map cannot satisfy. Declaration order is
preserved, which the typed API must round-trip and the diff must render. A name may carry more than
one predicate — `X-Tenant` present *and* matching a pattern — which a map keyed by name forbids.
And a Go map has no iteration order, so any map-shaped predicate set would either need a sort
imposed on it at every use or would leak iteration order into validation messages, diffs and
Console forms. `§7` of the completeness spec requires determinism; the representation should make
non-determinism unrepresentable rather than repair it later.

Absent and empty are distinct and both are meaningful. `match.methods` omitted means *this route
does not constrain the method*; `methods = []` is a validation error, because a route that can never
match is a configuration mistake, not a way to disable a route. The same rule applies to `headers`
and `query`: omitted means unconstrained, an empty array of tables is not expressible in TOML anyway,
and a table entry missing its required field is an error rather than a silently ignored no-op.

### 2. Method semantics

- `methods` entries must be valid RFC 9110 tokens.
- Comparison against `r.Method` is **byte-exact**. HTTP methods are case-sensitive (RFC 9110 §9.1);
  Jul does not case-fold them.
- Validation **rejects** a configured method whose ASCII-uppercase form is an IANA-registered method
  but which is not itself uppercase. `methods = ["get"]` is an error naming `"GET"`. This catches the
  only realistic mistake without mangling a genuinely lowercase extension method, which mechanical
  uppercasing would silently break.
- Duplicates are a validation error, not a silently collapsed set.
- Extension methods are accepted if they are valid tokens.
- **A route listing `GET` also matches `HEAD`.** RFC 9110 §9.3.2 requires a general-purpose server to
  support HEAD wherever it supports GET; Go's own `ServeMux` adopted the same rule for pattern
  matching; Jul's static handler, cache and access log already treat HEAD as a GET-shaped request.
  A route that answers GET and 404s HEAD is a defect an operator would have to discover in
  production. Listing `HEAD` explicitly is permitted and redundant. Listing `HEAD` *without* `GET`
  matches HEAD only. A future `strict_head = true` opt-out is additive if evidence ever demands one.
- `CONNECT` may be configured but never matches: Go's server does not route `CONNECT` requests to
  the handler with a normal request target. Validation rejects it with that reason rather than
  accepting a predicate that can never fire.

### 3. Header predicate semantics

| Field | Meaning |
| --- | --- |
| `name` | field name, required, canonicalized with `textproto.CanonicalMIMEHeaderKey` at compile time |
| `op` | `present`, `exact` or `regex`, required |
| `value` | required for `exact` and `regex`, forbidden for `present` |

- Lookup is `r.Header.Values(name)` after canonicalization, so header **names** are matched
  case-insensitively, as HTTP requires, on every protocol version.
- `present` is true when the field is present at all, including a field with an empty value. Absent
  and present-empty are therefore distinguishable: `present` alone distinguishes them from absent,
  and `op = "exact", value = ""` matches only the present-empty case.
- `exact` matches when **any one** field value is byte-equal to `value`. Go has already stripped
  optional whitespace around field values on every protocol version, so no trimming is applied and
  none is needed. Jul does **not** split comma-combined values: `Accept: a, b` is one value, and an
  `exact` predicate for `a` does not match it. This is stated because the alternative — splitting on
  commas — is wrong for `Date`, `Set-Cookie` and any field whose grammar admits a comma.
- `regex` is Go RE2, **unanchored**, applied to each field value independently, matching when any
  value matches. Unanchored is chosen for consistency with the existing `match.type = "regex"` path
  matcher, which is also `re.MatchString`. Operators who mean the whole value write `^…$`, and the
  reference documentation says so at the point of use.
- Patterns are compiled once, during `router.New` inside `Prepare`. A pattern that does not compile
  fails the reload. Nothing is compiled per request, ever.
- **`Host` is rejected** with an error pointing at `server_names`. Go removes the Host header from
  `r.Header` and puts it in `r.Host`, so a `Host` predicate would silently never match — the worst
  possible failure mode for a routing rule.
- Names beginning with `:` are rejected. HTTP/2 and HTTP/3 pseudo-headers are not fields, and Go
  never exposes them in `r.Header`.
- Hop-by-hop names (`Connection`, `Keep-Alive`, `TE`, `Trailer`, `Transfer-Encoding`, `Upgrade`,
  `Proxy-Connection`, `Proxy-Authenticate`, `Proxy-Authorization`) are accepted but produce a lint
  warning: they are connection-scoped, so a predicate on them behaves differently on HTTP/1.1 and on
  HTTP/2 for reasons the operator did not choose.
- **`Forwarded`, `X-Forwarded-*` and the RFC 9440 certificate-assertion names produce a
  `SeverityError` lint finding.** Route selection happens before `setCanonicalXForwarded`, so at
  matching time these fields still hold whatever the client sent. A predicate such as
  `X-Forwarded-Proto == https` reads as a security control and is trivially forged. Matching on them
  is not *rejected*, because a deployment behind a trusted proxy may have a legitimate reason, but it
  is never silent. This is the concrete expression of ADR 0016 §12 in the matching layer.

### 4. Query predicate semantics

| Field | Meaning |
| --- | --- |
| `name` | parameter name, required, non-empty, compared after decoding |
| `op` | `present` or `exact`, required |
| `value` | required for `exact`, forbidden for `present` |

- The query string is parsed with `url.ParseQuery(r.URL.RawQuery)` semantics: `&` separates pairs,
  `;` is not a separator, `+` decodes to a space, and `%XX` is percent-decoded.
- **Parsing happens at most once per request, lazily, and only when a candidate route actually
  carries a query predicate.** A configuration with no query predicates never parses a query string,
  so the existing path-only fast path is untouched.
- A malformed escape makes only that pair unusable; the successfully parsed pairs are still used, and
  the request is **not** rejected. A gateway must not turn a request it would otherwise forward into
  a 400 because a route it did not select had a query predicate. Behaviour is deterministic:
  malformed pairs are absent.
- Parsing is bounded to the first **1024** pairs. `max_header_bytes` bounds the request line at a
  size that still permits hundreds of thousands of empty pairs, which is an allocation amplifier
  a routing decision must not expose. Predicates evaluate against the bounded set.
- `present` is true when the key appears, including `?x` and `?x=`. `exact` matches when any
  occurrence of the key has a decoded value equal to `value`; `op = "exact", value = ""` matches
  `?x=` and `?x`.
- There is no `regex` operator for query parameters in this tranche.

### 5. Boolean composition: one rule, stated once

> **A list inside one field is an OR-set. Separate fields and separate table entries are ANDed.**

`methods = ["GET", "POST"]` matches GET or POST. Every `[[match.headers]]` entry, every
`[[match.query]]` entry, `methods`, and the path must all match for the route to match. Two entries
naming the same header are two predicates and are ANDed. There is no negation, no grouping, no OR
across fields and no expression syntax. This is the whole Boolean model, and it is deliberately small
enough to state in one sentence to an operator.

### 6. Route precedence: a fixed, tiered enumeration with fallthrough

Selection is unchanged in structure and unchanged in outcome for every configuration that has no
predicates. It becomes an enumeration so that a predicate failure can fall through.

**Server selection** is untouched: `hostScore` (3 exact, 2 leading wildcard, 0 none), highest score
wins, ties go to the first declared server block, no match falls back to the first server block
declared on the address.

**Location selection** within the chosen server:

```
candidates := []
  tier 1  every location with match.type = "exact" and path == r.URL.Path,
          in declaration order
  tier 2  every location with match.type = "prefix" whose path is a prefix of r.URL.Path,
          ordered by descending len(path), ties in declaration order
  tier 3  every location with match.type = "regex" whose compiled pattern matches r.URL.Path,
          in declaration order

for candidate in candidates (tier 1, then tier 2, then tier 3):
    if candidate has no predicates            -> select it
    if every predicate of candidate matches   -> select it
    otherwise                                 -> continue

no candidate selected -> 404 (Router.For, unchanged)
```

Four properties are frozen by that algorithm.

**Path specificity outranks predicates, always.** A `prefix "/api/v2/"` route with no predicates
beats a `prefix "/api/"` route with three. Predicates *filter* candidates within a tier; they never
promote a candidate across tiers or across prefix lengths. Any other rule requires a scoring system,
and the completeness spec forbids a hidden one.

**Declaration order is the only tie-breaker**, at every tier, in every case. No map is iterated
anywhere in selection.

**The `/` fallback stops being a special case.** `prefix "/"` becomes an ordinary tier-2 candidate of
length 1, which sorts last among prefixes and therefore behaves exactly as the current `sr.fallback`
does — with one difference. Today `sr.fallback` is reassigned on every `prefix "/"` location, so the
*last* one wins, while `lint.go` tells the operator the *first* one wins and the rest are
"unreachable". The router and the lint disagree today. Unifying `/` into tier 2 makes the first
declared win, which is what every other tier already does and what the lint already claims. The
behaviour change is confined to configurations that lint already flags.

**Rewrites still run after selection, on the selected location only.** `applyRewrites` is unchanged;
a rewritten path does not trigger a second location search, exactly as today.

### 7. No automatic 405

A method mismatch makes a route non-matching. When no candidate remains, the response is the existing
404, with no `Allow` header and no 405 anywhere.

This is a decision, not an omission. Emitting 405 would require enumerating every candidate that
matched the path in order to compute `Allow`, which turns a first-match search into a full scan of
the tier on the miss path and publishes the route topology of a host to an unauthenticated client.
Worse, the header would usually be a lie: `Allow` is a property of the *resource*, and a gateway
route enumerating `["GET"]` says nothing about whether the upstream implements POST. A gateway that
answers 405 on behalf of an upstream it never consulted is asserting something it does not know.

Re-entry trigger: revisit if an operator-visible requirement appears for method-aware diagnostics —
for example a route-test surface that must explain "the path matched but the method did not" — in
which case the *diagnostic* can be built on the enumeration above without changing the client-visible
status.

### 8. Response-header policy is an ordered list of operations

```toml
[[servers.locations.response_headers]]
op = "set"
name = "X-Frame-Options"
value = "DENY"

[[servers.locations.response_headers]]
op = "add"
name = "Set-Cookie"
value = "flavour=chocolate; Path=/; Secure; HttpOnly"

[[servers.locations.response_headers]]
op = "remove"
name = "X-Powered-By"
```

| `op` | Effect |
| --- | --- |
| `add` | append one field line (`Header.Add`); `value` required |
| `set` | replace all field lines with exactly one (`Header.Set`); `value` required |
| `remove` | delete every field line (`Header.Del`); `value` forbidden |

Order is semantically meaningful and is therefore preserved: operations apply top to bottom, and a
later operation observes the effect of the earlier ones, so `set` followed by two `add`s is the
canonical way to write a deterministic multi-value header. A map of `add`/`set`/`remove` was rejected
for the same three reasons as the match predicates — lost order, no repeated names, and Go map
iteration.

`value = ""` is legal on `add` and `set` and emits an empty field value, which RFC 9110 permits.
Omitting `value` on `add` or `set` is an error. Omitted and explicit-empty never collapse.

**Validation rejects, rather than ignores:**

- a `name` that is not a valid RFC 9110 field-name token, including any name beginning with `:`;
- a `value` containing CR, LF or NUL. Go's `net/http` silently drops an invalid header at write time,
  which would make a CRLF attempt look like a working configuration that quietly does nothing.
  Rejecting at configuration time is the only place the operator finds out;
- framing and connection-scoped names, which Jul owns: `Connection`, `Content-Length`,
  `Transfer-Encoding`, `Upgrade`, `Keep-Alive`, `Proxy-Connection`, `TE`, `Trailer`,
  `Proxy-Authenticate`, `Proxy-Authorization`;
- `Content-Encoding`, which the compression middleware owns; a configured value would label a body
  that Jul did not encode;
- `set` or `remove` on `Vary`. `Vary` is co-owned: compression appends `Accept-Encoding`, CORS
  appends `Origin`, and the cache derives variants from it. `add` on `Vary` is permitted and merges;
  replacing or deleting it is rejected with that reason.

`Server` and `X-Powered-By` are ordinary mutable headers and removing them is a supported use case.

Bound: 32 operations per location.

### 9. CORS is a separate, explicit block

```toml
[servers.locations.cors]
enabled = true
allowed_origins = ["https://app.example.test"]
allowed_methods = ["GET", "POST"]
allowed_headers = ["Content-Type", "Authorization"]
exposed_headers = ["X-Request-Id"]
allow_credentials = true
max_age = "10m"
```

**Origins are exact, normalized, byte-compared.**

- A configured origin must be `scheme "://" host [ ":" port ]` with an ASCII-lowercase scheme and
  host, no path, no trailing slash, and no explicitly-written default port. `https://a.example:443`
  is a validation error naming `https://a.example`, because browsers never send the default port and
  the configured form would never match. Comparison at request time is then a byte comparison
  against the `Origin` field value.
- `"*"` is permitted **only** when `allow_credentials = false`. `"*"` together with
  `allow_credentials = true` is a validation error, not a silently downgraded reflection: the Fetch
  standard makes that combination inert, and a gateway that quietly reinterprets it is teaching the
  operator a false model of their own policy.
- `"null"` is matched only when listed literally. `"*"` does not cover it. Listing it produces a lint
  warning, because the `null` origin is what sandboxed iframes, `data:` documents and some
  cross-origin redirects send, and it is not an authentication of anything.
- Scheme and port are significant. There is **no** wildcard subdomain syntax and **no** regex in this
  tranche.
- A request carrying more than one `Origin` field line is not a valid CORS request and is treated as
  having no origin. A syntactically malformed `Origin` matches nothing.
- Origin matching performs no DNS resolution and no network access.

**Two questions, kept separate.** Whether Jul *processes* the request and whether Jul *tells the
browser the response may be read* are different questions. CORS is a browser policy, not server
authorization: a request from a disallowed origin is still routed, authenticated, rate-limited and
served exactly as it would be without a CORS block; it simply receives no `Access-Control-*` headers,
so the browser refuses to hand the response to the script. Operators who need origin-based *access
control* use auth or a header predicate, and the reference documentation says so where `cors` is
defined.

**Preflight.** A request is a preflight when it is `OPTIONS`, carries exactly one `Origin`, and
carries `Access-Control-Request-Method`.

- An **approved** preflight — allowed origin, requested method in `allowed_methods`, and every
  comma-separated token of `Access-Control-Request-Headers` in `allowed_headers` compared
  case-insensitively — is answered by Jul with **204 No Content**, an empty body, and no upstream
  contact and no cache interaction.
- A **denied** preflight is *not* short-circuited. It continues down the ordinary chain and receives
  whatever that route would return for an `OPTIONS` request, with no `Access-Control-*` headers. Jul
  does not invent a 403 that discloses that a CORS policy exists, and it does not exempt the request
  from authentication, rate limiting or the WAF.
- `Access-Control-Request-Headers` is bounded at 64 tokens; a longer list is treated as not approved.
- Preflight responses carry `Access-Control-Allow-Origin`, `Access-Control-Allow-Methods`,
  `Access-Control-Allow-Headers`, `Access-Control-Max-Age` when `max_age` is set,
  `Access-Control-Allow-Credentials: true` when credentials are enabled, and
  `Vary: Origin, Access-Control-Request-Method, Access-Control-Request-Headers`.

**Actual requests.** For an allowed origin, Jul emits `Access-Control-Allow-Origin` — the request's
origin, or the literal `*` when the policy is the credential-free wildcard —
`Access-Control-Allow-Credentials: true` when enabled, and `Access-Control-Expose-Headers` when
`exposed_headers` is non-empty. Preflight-only headers are not emitted on actual responses.

**`Vary: Origin` is appended on every response from a CORS-enabled location**, including responses to
requests that carry no `Origin` at all and responses to disallowed origins. This is the rule that
prevents a shared downstream cache from storing the no-origin response and replaying it to a
cross-origin request. The single exception is the policy `allowed_origins = ["*"]` with
`allow_credentials = false` and no other origin-dependent output, whose emitted headers are constant:
there `Vary: Origin` is suppressed because it would cost cache efficiency and buy nothing. `Vary` is
always *appended*, never replaced, so compression's `Accept-Encoding` survives.

**CORS headers are emitted on error responses.** A browser cannot read the body or status of a
cross-origin 401, 403, 429 or 502 without `Access-Control-Allow-Origin`, and an operator debugging a
gateway through a browser console is the exact person this affects. Because the policy is applied by
the outermost per-location layer (§10), this happens for every response the location produces —
including auth denials, rate-limit rejections, WAF blocks, upstream failures and cache hits — rather
than only for the ones that reach the action.

Private Network Access (`Access-Control-Request-Private-Network`) is out of scope; see §17.

Bounds: 64 `allowed_origins`, 16 `allowed_methods`, 64 `allowed_headers`, 64 `exposed_headers`.

### 10. Execution order

The per-location chain gains exactly two positions. Nothing existing moves.

```
request
  RequestID
  ClientAddress                     (ADR 0016 canonical client, per listener)
  Tracing / Metrics / AccessLog     (observers)
  Recover
  Compression
  router: host -> location (§6)     <- method/header/query predicates evaluated here
    BodyLimit
    >> response policy + CORS response headers        (NEW, outermost per-location)
    plugins
    ClientCert
    >> CORS preflight termination                     (NEW)
    Auth
    RateLimit
    WAF
    cache
    action (static / proxy / gRPC / transcode / fastcgi / redirect / return / deny)
```

**Response policy is the outermost per-location layer** so that it decorates every response the
location can produce: an auth 401, a rate-limit 429, a WAF 403, an upstream 502, a cache hit, a
static 404, a redirect. It is implemented as a `ResponseWriter` wrapper that applies its operations
inside `WriteHeader`, immediately before delegating outward — never by pre-setting headers before
calling `next`, which would put them in the shared header map where the cache would capture them
(§11). It is composed through `respwriter.Wrap`, so `Flusher`, `Hijacker`, `Pusher`, `ReaderFrom` and
`Unwrap` remain exactly as truthful as they are without it. After a hijack it is inert. A location
with neither `response_headers` nor `cors` installs no wrapper at all.

**Preflight termination sits immediately outside `Auth`**, which is the minimum bypass that makes the
feature work. The CORS-preflight fetch is defined to be sent with credentials omitted, so a route
with Jul-level authentication can never complete a credentialed CORS exchange if the preflight has to
authenticate first. Because `Auth` currently wraps `RateLimit` and the WAF — deliberately, so that
`jwt:<claim>` rate-limit keys and identity-aware WAF rules work — terminating outside `Auth` also
skips those two for approved preflights. That exposure is accepted and bounded, and it is bounded
*by the operator's own policy*: only a preflight carrying an origin the operator explicitly allowed
is terminated, a denied preflight traverses the full chain, the response is a fixed ~200-byte 204
computed from three request headers with no allocation of consequence, no upstream is contacted, and
the listener-level protections (`client_max_body_size`, `max_header_bytes`, read timeouts, ADR 0017
admission control) still apply. Moving `Auth` inside `RateLimit`/`WAF` to close it would break two
documented, load-bearing behaviours to defend against an attacker who must already know an allowed
origin; that trade is not worth making.

**Responses produced outside route selection carry no location policy**, by construction and by
design: the router's own 404, the HTTP→HTTPS redirect, the ACME challenge handler, and the 500 that
`Recover` writes after a panic are all emitted outside the per-location chain. This is stated so it
is a documented boundary rather than a surprise.

### 11. Cache × response policy: the cache stores the origin representation

Two rules, and the second is what makes the first true.

1. **Response policy and CORS are applied outside the cache**, so nothing they produce is ever
   eligible for storage.
2. **`cacheWriter` snapshots the response header map inside its own `WriteHeader`, before delegating
   outward, and `buildEntry` consumes that snapshot instead of re-reading `w.Header()` after the
   stack unwinds.**

Rule 1 alone is not sufficient and this record must say so explicitly, because the natural reading of
"place it outside the cache" is that placement settles it. It does not. Every wrapper in the chain
shares one header map; a layer outside the cache mutates that map during its own `WriteHeader`, which
runs *before* `fetchAndStore` reads it. That is not a hypothesis: it is the reproduced compression
defect described in the Context, and a response policy would inherit it exactly.

The consequences of rule 2 are what this record wants:

- A stored entry contains the upstream/origin representation and nothing Jul's own outer layers
  added. No `Access-Control-Allow-Origin`, no operator header operations, no `Content-Encoding` that
  Jul applied.
- **No policy generation stamp is needed anywhere in the cache.** A gob-encoded entry rehydrated from
  disk after a restart, written under a configuration nobody can inspect any more, cannot carry a
  CORS or operator header because it never contained one. This is why the simplest architecture is
  also the correct one: the alternative — versioning entries by policy generation, or purging the
  cache on policy change — requires the cache to understand policy identity, survives neither disk
  persistence nor a config edit made while the process was stopped, and would still be wrong for the
  compression case.
- **Jul's cache does not vary by `Origin`, and must not.** `Vary: Origin` is appended outside the
  cache, so it is not stored, so it does not create variants. One entry serves every origin, and the
  per-request CORS layer decorates each hit correctly. Varying by `Origin` would multiply every
  cached resource by the tenant count against a 64-variant ceiling, to store headers that are cheaper
  to recompute than to look up.
- Approved preflights never reach the cache: they terminate above it, and `OPTIONS` is not a stored
  method in any case.
- Cache invalidation, `Vary` stubs, variant membership and the disk store are otherwise untouched.

**Invariant, testable as stated:** *no header written by a layer outside the cache handler may appear
in a stored cache entry.*

### 12. Protocol parity

Predicates, response policy and CORS are properties of a *location*, so they apply identically on
HTTP/1.1, h2c, HTTP/2 and HTTP/3, and to every action. There is exactly one matcher and exactly one
response-policy wrapper. Specifically:

- **WebSocket upgrade.** Response-header operations apply to the 101 response like any other, which
  is safe because `Connection` and `Upgrade` are rejected at validation, so the handshake cannot be
  broken by configuration. After the hijack the wrapper is inert. CORS headers are not emitted on a
  101: the WebSocket handshake has its own origin model and `Access-Control-*` means nothing on it.
- **Native gRPC passthrough.** Predicates apply (the method is always `POST`; `content-type`,
  `te` and gRPC metadata are ordinary header fields). Response-header operations apply to the HTTP
  response headers. **gRPC trailers are not touched** — `Trailer` is a rejected name and trailer
  policy is out of scope. Mutating `Content-Type` on a gRPC location produces a lint warning because
  it will break clients.
- **gRPC transcoding.** Identical treatment; the transcoded HTTP response is an ordinary response.
- **CORS on a native gRPC location** is accepted but lint-warned: gRPC-Web is not implemented, so the
  configuration is almost certainly not doing what the operator expects.

### 13. Lifecycle

Every field here is `hot_reload`. Registry entries are required for
`servers.*.locations.*.match.methods`, `servers.*.locations.*.match.headers.*`,
`servers.*.locations.*.match.query.*` under `SubRouting`;
`servers.*.locations.*.response_headers.*` under `SubHeaders`; and `servers.*.locations.*.cors.*`
under a new `cors` subsystem. None is startup-consumed; none appears in the startup fingerprint.

The properties ADR 0011 already guarantees are the ones this record depends on: predicate regexes,
origin normalization and header-operation validation all happen inside `router.New` during
`Prepare`, so an invalid policy fails the reload transaction before `Publish` and can never partially
activate; candidate preparation is side-effect-free; publication is atomic; and a request keeps the
handler generation it started with. No second lifecycle mechanism is introduced.

### 14. Location identity, and what predicates break

`(listen, server_names, match_type, path)` stops being unique the moment a path can carry different
predicates. This record fixes the addressing scheme rather than leaving #145 to discover it.

- The typed patch request gains an optional **`match_ordinal`**: the 0-based index among the
  locations sharing the other four coordinates, in declaration order. Omitted means "there must be
  exactly one", which is precisely today's behaviour, so every existing client and every existing
  payload keeps working. `findLocation` continues to reject an ambiguous target rather than guess.
- Route projections and the diff emit `match_ordinal` alongside the existing coordinates, and the
  diff renders a predicate summary so two same-path routes are distinguishable in a preview.
- `AuthScope`, `WAFScope` and the per-location rate-limit scope must include the match type and the
  ordinal. They key on `listen | names | match.path` today, which already collides between an exact
  and a prefix location on the same path — a pre-existing defect that predicates would turn from
  unlikely into ordinary. Changing the rate-limit scope string resets those buckets once, on upgrade;
  that is acceptable and must be in the changelog.
- **The admin route-test surface must call the router's selection function, not its own.**
  `bestServer`/`bestLocation` are deleted and `routeTestRequest.Method`/`Headers` — already present
  and already ignored — become real inputs. Two matching implementations are how the Console acquires
  semantics the server does not have, which ADR 0014 forbids.

### 15. Validation, lint and the difference between them

**Validation** (rejects the configuration) covers everything in §2, §3, §4, §8 and §9 stated as an
error: invalid tokens, empty `methods`, duplicates, missing or forbidden `value`, uncompilable
regexes, `Host` and pseudo-header predicates, `CONNECT`, invalid or protected header names, CR/LF in
values, non-normalized origins, wildcard-with-credentials, and every bound in §16.

**Lint** (accepts, warns) covers the configurations that are valid HTTP but probably not what the
operator meant:

- `SeverityError`: a predicate on `Forwarded`, `X-Forwarded-*` or an RFC 9440 name (§3).
- Warning: a hop-by-hop header predicate; `"null"` in `allowed_origins`; `cors` on a native gRPC
  location; a response-header operation on `Content-Type` at a gRPC location.
- Warning: **unreachable routes.** The existing duplicate-match rule extends to predicates. Two
  locations with the same `(type, path)` are duplicates only when their normalized predicate sets are
  equal. A later location is unreachable when an earlier location with the same `(type, path)` has a
  predicate set that is a *subset* of its own — including the empty set, which is the common case of
  a predicate-free route declared above a predicated one.

The distinction is the existing one: Jul does not reject a valid HTTP construct for being unusual,
but it never lets an unusual one be silent.

### 16. Bounds

| Bound | Limit |
| --- | --- |
| `match.methods` entries | 16 |
| `match.headers` entries per location | 16 |
| `match.query` entries per location | 16 |
| `regex` header predicates per location | 8 |
| Header regex pattern length | 512 bytes |
| Query pairs parsed per request | 1024 |
| `response_headers` operations per location | 32 |
| `cors.allowed_origins` | 64 |
| `cors.allowed_methods` | 16 |
| `cors.allowed_headers` | 64 |
| `cors.exposed_headers` | 64 |
| `Access-Control-Request-Headers` tokens honoured | 64 |

These are validation limits, checked before `Publish`. They exist so that a pathological or
adversarial configuration cannot make route selection or preflight evaluation superlinear, and so
that the cost of a request is bounded by a number an operator can read off their own configuration.

### 17. What is not built

| Excluded | Re-entry trigger |
| --- | --- |
| Expression language, scripting, policy DSL | none — permanently out of scope per D12 |
| OR across predicate fields, negation, grouping | a second real configuration that cannot be expressed as two locations |
| Request-body/content matching | none — permanently out of scope per D12 |
| Cookie-specific predicates | evidence that `Cookie` header predicates are materially harder to use than a dedicated form |
| Origin patterns, wildcard subdomains, origin regex | a deployment with an unbounded but structurally constrained origin set, plus a bounded grammar proposal |
| CORS Private Network Access | the specification stabilizes and a supported deployment needs it |
| Automatic 405 / `Allow` | §7 |
| Response-body rewriting, response trailer policy | none in the core boundary |
| Status-conditional header emission (NGINX `add_header` without `always`) | a migration corpus showing it is common enough to justify the semantics |
| Weighted, canary, mirrored or split routing | ADR-level traffic-management decision, not this record |
| `strict_head` opt-out | a route that must answer GET and refuse HEAD |

### 18. Importer

`internal/migrate/nginx` gains truthful classification, not translation ambition.

| NGINX | Result |
| --- | --- |
| `add_header X Y always;` outside any `if` | translated to `op = "add"` |
| `add_header X Y;` (no `always`) | **unsupported finding** — NGINX emits it only for a specific status set, which §8 has no way to express; silently making it unconditional would change security-header behaviour on error responses |
| `add_header` inside `if` | unsupported finding |
| `limit_except` | **unsupported finding**, with a specific reason — the directive is a negation ("methods other than these") whose deny branch produces 403, whereas §2 plus §7 produce 404, so no faithful mapping exists |
| `if ($http_origin …)` CORS idioms | unsupported finding naming `[servers.locations.cors]` |
| `more_set_headers` (headers-more) | unsupported finding |

Every finding keeps its source line and reason and appears both in the `Report` and as a `TODO`
comment in the generated TOML, exactly as the existing skipped directives do. Nothing is
approximated silently.

## Reversibility and one-way doors

| Decision | Reversibility | Why | Reversal cost |
| --- | --- | --- | --- |
| Match predicates as arrays of tables with an `op` enum (§1) | **One-way door** | public TOML, typed API, JSON Schema, Console forms, importer output | schema migration, dual-read parsing, deprecation window |
| Absent vs empty semantics; `methods = []` rejected (§1) | **One-way door** | changing it later silently reinterprets deployed configurations | breaking, or a new field |
| Byte-exact method comparison; non-uppercase registered methods rejected (§2) | Expensive two-way door | loosening is additive, tightening is breaking | one-directional only |
| **`GET` also matches `HEAD` (§2)** | **One-way door** | directly observable routing behaviour | breaking; only escapable via an additive opt-out |
| Header name canonicalization; no comma splitting; unanchored regex (§3) | **One-way door** | changes which requests a deployed route matches | breaking |
| `present` includes present-empty (§3) | **One-way door** | same | breaking |
| Query parsing semantics; malformed pairs absent, never a 400 (§4) | **One-way door** | client-visible behaviour under malformed input | breaking |
| AND composition, OR only within a list (§5) | One-way door to *narrow*, two-way to *extend* | adding a bounded OR later is additive | low if additive |
| Tiered enumeration with fallthrough; path specificity over predicates (§6) | **One-way door** | the single most observable behaviour in the record | breaking; traffic moves |
| `/` folded into tier 2, first-declared wins (§6) | Expensive two-way door | affects only configurations lint already flags | small, documented |
| No automatic 405 (§7) | Two-way door **into** 405, one-way back out | adding 405 later is a behaviour change; removing it after shipping is worse | adding later is the cheap direction |
| Ordered operation list for response headers (§8) | **One-way door** | public schema and typed API | schema migration |
| Protected-header denylist, including `Vary` set/remove (§8) | Expensive two-way door | relaxing is additive, tightening breaks deployed configs | one-directional |
| Exact-only origins; `*`+credentials rejected (§9) | One-way door to *narrow*, additive to extend | a bounded pattern grammar can be added compatibly | low if additive |
| CORS is not authorization; disallowed origins are still served (§9) | **One-way door** | security-model contract operators build on | breaking, and dangerous to change quietly |
| CORS headers on error responses (§9) | **One-way door** | observable, and browsers depend on it | breaking |
| `Vary: Origin` policy (§9) | **One-way door** | downstream shared caches store what Jul emits | cache poisoning during any transition |
| Response policy outermost per-location (§10) | Expensive two-way door | which responses carry policy is observable | breaking for error-path headers |
| Preflight terminated outside `Auth`, skipping rate limit and WAF (§10) | **One-way door** | durable security contract | tightening breaks credentialed CORS on authenticated routes |
| Cache stores the origin representation; snapshot at commit (§11) | Two-way door in mechanism, **one-way in guarantee** | the *guarantee* is a security contract; the snapshot is private | mechanism: local refactor. Guarantee: not reversible |
| `match_ordinal` addressing (§14) | **One-way door** | public API field | additive now; removing it later is breaking |
| Rate-limit/auth/WAF scope keys include type and ordinal (§14) | Expensive two-way door | one-time bucket reset on upgrade | documented changelog entry |
| Numeric bounds (§16) | Two-way door upward, one-way downward | raising a limit is additive | low upward |
| Compiled matcher representation, package layout, predicate structs | **Two-way door** | entirely private | local refactor |
| Where the parsed query is memoized | **Two-way door** | private | local refactor |

The three decisions that would cost the most to discover wrong in twelve months are **route
precedence (§6)**, **the cache guarantee (§11)** and **preflight ordering (§10)**. §6 is frozen as an
algorithm rather than prose for that reason; §11 is stated as a testable invariant rather than a
placement instruction; §10 records the exposure it accepts and why the alternative was rejected,
rather than leaving it to be inferred.

## Security considerations

1. **Attacker-controlled forwarded headers cannot reappear.** This record adds no stage between
   `setCanonicalXForwarded` and `applyProxyHeaders` and touches neither. Response policy has no
   access to the outbound request; CORS writes no request header. The one new adjacency is that route
   *matching* reads `X-Forwarded-*` before sanitization, which §3 makes a `SeverityError` lint
   finding rather than a silent footgun.
2. **Header injection is impossible through configuration.** CR, LF and NUL in a value, and any
   non-token name, are rejected before `Publish` (§8) — not dropped at write time, where the operator
   would never learn.
3. **Framing cannot be mutated.** The `Content-Length`/`Transfer-Encoding`/`Connection`/`Upgrade`
   family is rejected, which also means request smuggling cannot be configured into existence and a
   WebSocket handshake cannot be broken by a header operation.
4. **Credentialed CORS cannot emit `*`.** Rejected at validation, not repaired at runtime (§9).
5. **A cached response cannot carry another origin's CORS grant.** §11 makes it unrepresentable
   rather than merely unlikely, including for entries rehydrated from disk under a configuration that
   no longer exists.
6. **`Vary: Origin` is emitted even when no `Origin` is present**, so a shared downstream cache cannot
   store the no-origin variant and replay it cross-origin (§9).
7. **Route topology is not disclosed.** No 405, no `Allow`, no CORS-specific rejection status for a
   denied preflight (§7, §9).
8. **No request value becomes a metric label.** Predicate values, header values, query values and
   origins never appear in telemetry labels; this is the existing rule and it is not relaxed.
9. **Accepted exposure:** an approved preflight skips per-location authentication, rate limiting and
   the WAF (§10). Stated, bounded, and justified there.

## Performance

The path-only fast path must not regress, because it is every existing deployment. A location with no
predicates holds a nil predicate pointer and costs one nil check. A location with no
`response_headers` and no `cors` installs no `ResponseWriter` wrapper and allocates nothing. Query
parsing is lazy and per-request, not per-candidate, and does not happen at all in a configuration
without query predicates. Regexes and origin sets are compiled and normalized during `Prepare`.

No route index is built. The tiers are linear scans over a slice, which is what they are today, and
realistic per-server location counts are in the tens. Building an index would be a private
optimization (a two-way door) that can be added later against a benchmark; adding it now would be
speculation. The benchmark set below is what would justify it.

Required benchmarks, all in `internal/router` and `internal/middleware`: path-only exact and prefix
(the regression gate); method-only; one exact header; one regex header; one query predicate; many
candidates with predicate mismatches forcing fallthrough; the no-policy response path; static
add/set/remove; an exact-origin actual response; an approved and a denied preflight. The path-only
regression threshold is set from the measured baseline in the implementing pull request; no claim of
zero overhead is made without a measurement.

## Observability

- Predicate mismatch is normal control flow and is not logged per request.
- The access log and metrics are unchanged. No new label, and specifically no label carrying a
  method, header, query or origin value.
- The route-test surface (§14) becomes the diagnostic: it explains which server matched, which
  candidates the path produced, which predicate failed on each rejected candidate, and which location
  was selected. That is where "why did my route not match" is answered, under admin authentication,
  rather than in a per-request log line.
- Validation and lint messages carry the exact predicate index (`servers[0].locations[2].match.headers[1]`)
  so the Console can attach an error to the right form field.

## API, Console and generated contracts

The broader generated-contract architecture belongs to ADR 0019. This record fixes only what those
systems need in order to represent routing without inventing semantics:

- declaration order of `match.headers`, `match.query` and `response_headers` is part of the contract
  and must survive every round trip;
- omitted and explicit-empty are distinct everywhere (§1, §8) and must not collapse through JSON;
- `op` is a closed enum in each position;
- `match_ordinal` (§14) is the addressing extension;
- server-side validation is authoritative; the Console renders errors by path, and implements no
  matching, precedence or CORS logic of its own;
- raw TOML remains the complete escape hatch;
- every new field carries a lifecycle registry entry, so `make generated-check` regenerates
  `docs/config-lifecycle.yaml`, `docs/generated/config-lifecycle.md` and
  `docs/generated/config-lifecycle.json` without drift.

## Required tests

Grouped by the decision each pins, because a test that does not pin a decision in this record is not
required by it.

- **§2 methods:** exact-case comparison; lowercase registered method rejected; extension method
  accepted; duplicate rejected; empty list rejected; `CONNECT` rejected; `GET` matches HEAD; `HEAD`
  alone does not match GET.
- **§3 headers:** name case-insensitivity across HTTP/1.1, HTTP/2 and HTTP/3; absent vs present-empty;
  repeated field lines; comma-combined value not split; unanchored regex; regex compile failure fails
  the reload; `Host` rejected; pseudo-header rejected; `X-Forwarded-For` predicate produces the
  `SeverityError` finding; all §16 bounds.
- **§4 query:** absent, `?x`, `?x=`, repeated, percent-encoded, `+`, malformed escape (deterministic,
  no 400), `;` not a separator, the 1024-pair bound, and parsed exactly once per request.
- **§5/§6 selection:** AND across kinds; fallthrough within the exact, prefix and regex tiers;
  descending prefix length then declaration order; path specificity beating predicates; `/` folded
  into tier 2 with first-declared winning; no map iteration anywhere in selection; order stable
  across reload; unreachable/duplicate lint.
- **§7:** method mismatch yields 404 and no `Allow` header.
- **§8 response headers:** add/set/remove and their ordering; multi-value; empty value vs omitted;
  every rejected name; CR/LF/NUL rejected; `Vary` add permitted, set/remove rejected.
- **§9 CORS:** exact, `*`, `null`, malformed, duplicate `Origin`, disallowed; `*`+credentials
  rejected; non-normalized origin rejected; approved and denied preflight; requested-header casing
  and the 64-token bound; `Vary` emission including the no-`Origin` case and the constant-`*`
  suppression; CORS headers present on 401, 403, 429 and 502.
- **§10 ordering:** policy applied to auth, rate-limit, WAF, upstream-error and cache-hit responses;
  not applied to the router 404, the HTTPS redirect or the `Recover` 500; approved preflight reaches
  neither auth nor the action; denied preflight does.
- **§11 cache — the invariant test:** a response stored under a location with a CORS policy and
  header operations contains none of them; a second request from a different origin receives its own
  correct headers; an entry rehydrated from disk carries no policy headers. **Plus the regression
  test for the pre-existing compression defect**, which the same snapshot fixes: a compressible
  cacheable response served on a hit must have a body whose encoding matches its
  `Content-Encoding`.
- **§12 protocols:** identical predicate and policy behaviour on HTTP/1.1, h2c, HTTP/2 and HTTP/3;
  WebSocket upgrade unaffected and the wrapper inert after hijack; native gRPC and transcoding
  policy applied without touching trailers.
- **§13/§14 lifecycle and API:** invalid predicate or policy aborts the reload before `Publish`;
  order preserved across reload; typed patch round-trip including clear; `match_ordinal` addressing
  including the ambiguity rejection; route-test uses the router's selection.
- Fuzz the header and query predicate parsers and the origin normalizer. Race and E2E as usual.

## Consequences

1. Route selection becomes a candidate enumeration. This is the largest internal change and the one
   with the most observable blast radius; §6 exists so it is implemented rather than designed.
2. `cacheWriter` must snapshot headers at commit. This also fixes a reproduced, client-visible
   compression corruption bug that exists today, tracked as its own defect issue.
3. `sr.fallback` disappears as a special case; duplicate `prefix "/"` locations change from
   last-declared to first-declared, aligning the router with the lint.
4. `AuthScope`, `WAFScope` and the rate-limit scope gain match type and ordinal. Rate-limit buckets
   reset once on upgrade.
5. `internal/admin/routetest.go` loses its private matcher and gains real method/header inputs.
6. The lifecycle registry, `docs/config-lifecycle.yaml` and both generated mirrors grow entries; a
   new `cors` subsystem is added.
7. `docs/configuration.md`, `docs/core-http.md`, `docs/cache.md`, `docs/compression.md`,
   `docs/security-posture.md`, `docs/nginx-importer.md`, `docs/known-limitations.md`,
   `docs/console.md`, `docs/reload-semantics.md`, the generated configuration reference and
   `CHANGELOG.md` all require updates when #145 and #146 land.
8. #145 and #146 become implementable without any further public decision. #147 remains gated on
   ADR 0019.

## Alternatives considered

**A general expression language for predicates.** Rejected, per D12 and again here on evidence: it
would require a parser, an evaluator, a sandbox, a security model, a debugger story and a Console
representation, in exchange for expressiveness that two locations already provide. Its permanent
maintenance cost is not repaid by any current requirement.

**Map-shaped predicates and a map-shaped response-header policy.** Rejected: loses declaration order,
cannot express two predicates on one name or two `add`s of one header, and makes Go map iteration a
latent source of non-determinism in validation output, diffs and Console forms.

**Automatic 405 with `Allow`.** Rejected in §7: it forces a full tier scan on the miss path,
discloses route topology, and asserts a resource property the gateway does not know.

**A scoring system that lets predicate count promote a route.** Rejected: it is exactly the "hidden
scoring" the completeness spec forbids, and it makes the effect of adding a predicate to one route
non-local.

**Storing post-policy responses in the cache, keyed by a policy generation stamp.** Rejected in §11:
it requires the cache to model policy identity, does not survive disk persistence or an edit made
while the process is stopped, multiplies variants by origin against a 64-variant ceiling, and would
still leave the compression defect in place. Storing the origin representation is both simpler and
strictly more correct.

**Purging the cache whenever routing policy changes.** Rejected: it discards unrelated cached work on
every reload, and it cannot help entries rehydrated from disk at startup.

**Reordering the per-location chain so preflight can run inside rate limiting and the WAF.**
Rejected in §10: it would move `Auth` inside `RateLimit` and the WAF, breaking `jwt:<claim>`
rate-limit keys and identity-aware WAF rules, which are documented and load-bearing.

**A request-context flag set outermost that instructs `Auth` to skip approved preflights.** Rejected:
it closes the §10 exposure but creates a permanent cross-layer contract in which an outer layer
disables an inner security layer. The residual risk it removes does not justify that shape.

**Wildcard-subdomain or regex origins.** Rejected for this tranche: over-matching an origin is a
cross-origin data-disclosure bug, and the bounded exact list covers every use case currently
evidenced. Additive later, with a grammar, if a real deployment needs it.

**Deriving `limit_except` and bare `add_header` translations in the importer.** Rejected in §18: both
change semantics silently, which is worse than an explicit unsupported finding.

## Related

- #117 — this record's decision issue
- #145 — `[ROUTE-01]` request matching; implements §1–§7, §13–§16
- #146 — `[ROUTE-02]` response policy and CORS; implements §8–§12, §15, §16
- #147 — `[ROUTE-03]` typed API, Console, importer, E2E; also gated on ADR 0019
- #108 — Core Gateway Completeness epic; `docs/specs/core-gateway-completeness.md` §7 (D12)
- [ADR 0016](0016-inbound-identity-and-backend-peer-trust.md) §12 — identity asserted to the backend
- [ADR 0011](0011-reload-plan.md) — reload transaction and the closed-world lifecycle registry
- [ADR 0014](0014-operability-surfaces.md) — one backend implementation behind every surface
