# ADR 0018 — Bounded route matching and response policy

- **Status:** Accepted
- **Date:** 2026-08-21 (revised 2026-08-24 after external review)
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

## Revision log

| Date | Change |
| --- | --- |
| 2026-08-21 | Initial record. |
| 2026-08-21 | §2/§9: `cors.enabled` widens a `methods` predicate for preflights; header predicates get a lint warning instead of a hidden exemption. Found by adversarial review — a CORS route with a method predicate could not be selected for its own preflight. |
| 2026-08-24 | External review. Seven substantive changes: §8 rejects `Vary` operations on cached locations (an outer-layer `Vary` cannot protect Jul's own cache); §8a fixes CORS/`response_headers` ownership and order; §10 adds a coarse pre-authentication guard for approved preflights and drops two weak justifications; §9 makes the credential-free wildcard unconditional so suppressing `Vary: Origin` is actually sound; §14 demotes `match_ordinal` to a CAS-bound selector and moves internal scopes to a predicate fingerprint; §9/§16 complete the CORS and field-validation bounds; §10 specifies `1xx` handling. Corrected three factual errors: `CONNECT` *is* routed to Go handlers, RFC 9110's HEAD requirement is in §9.1, and the performance argument against 405 was wrong. |
| 2026-08-24 | Second review round. §11's invariant was **falsified** and is narrowed to the commit boundary, with the stronger form required from #332: `RequestID` pre-sets `X-Request-ID` before `next`, so a cache hit replays a stale id beside the current one. §12 renamed to *HTTP semantic parity* with a transport/action matrix, because the previous blanket claim was untrue for WebSocket, native gRPC and the L4 stream proxy. §10's WAF justification was **factually wrong** — Coraza inspects URI, query, headers, method and client address without a body — so the preflight terminator now runs the WAF as well as the rate guard, restructured as decide-then-guard so nothing is evaluated twice. §9 states CORS defaults, empty-list semantics and `enabled = false` validation. §15's unreachable-route rule is reduced to the provable cases. §10's diagram had `BodyLimit` outermost, contradicting this record's own *Existing architecture* table. |
| 2026-08-24 | Third review round. §12's WebSocket row said HTTP/1.1 **and HTTP/2**, taken from a stale line in `docs/http3.md` rather than verified; Jul implements no RFC 8441 extended `CONNECT`, as `docs/cache.md` already said, so it is HTTP/1.1 only and §17 records the cost and re-entry trigger for changing that. §14 adds `preflight_widening` to the canonical fingerprint and §15's subsumption model, because §2 made `cors.enabled` a *matcher* input and two routes differing only in it were collapsing to one scope. §14 freezes the route-test request extension (`raw_query`, `header_values`), which the existing `map[string]string` could not express. §11 stops prescribing a multiset difference as a proof: it delivers the no-leak property this record needs but not header fidelity, and the mechanism is #332's to choose. Coherence: `architecture.md`, the `Access-Control-Allow-Headers` emission condition, and `enabled = false` validating present *values* rather than requiredness. |
| 2026-08-24 | Final read-through before merge. Three handoff gaps closed, no decisions changed: §9 makes the wildcard/`Vary` coupling structural (one derived predicate plus a named coupling test) rather than relying on a paragraph a future editor may not read; §10 states that both preflight guards compose as ordinary middleware around the 204 emitter, so nobody invents a parallel `Check()` API with its own metrics and failure modes; §8a names the capability the `Vary` restriction costs — an operator whose uncontrollable upstream varies silently must set `cache = false` on that route — instead of leaving it to be discovered. |
| 2026-08-24 | §17's RFC 8441 re-entry trigger said "golang/go#71128 resolves". Wrong twice: that issue closed in January 2025, and it closed **by disabling** extended `CONNECT` — it produced the current state rather than tracking its removal. The trigger is now state-based. The same passage misattributed Go's rationale: *"package doesn't support extended CONNECT"* refers to **the server's WebSocket package**, not `net/http`, and the real hazard is sharper — advertising `SETTINGS_ENABLE_CONNECT_PROTOCOL` makes browsers *stop* sending HTTP/1.1 `Upgrade`, so a partial implementation breaks WebSocket for clients that work today. `docs/http3.md` and `docs/known-limitations.md` carried the same misquote. |
| 2026-08-24 | Review of the record's own stated doubts. One design change: §10 adds a **location-scoped recover**, so a panic after route selection produces a 500 carrying the location's response headers and CORS — the earlier draft documented that hole as a boundary, which was wrong, since §9 already argues a cross-origin 401/403/429/502 must be readable and 500 is not different. Three accuracy corrections: §16's bounds are labelled conservative and **unmeasured**, with worst-case benchmarks at the maxima required of #145/#146; §14's reversibility is downgraded to an *internal* two-way door with transient impact, because `authByScope`/`wafByScope` are rebuilt per generation and only rate-limit buckets are keyed across reloads (evicted by the store's idle TTL); and #331/#332 become **hard merge gates** for #146 rather than sequencing preferences. §9's `allowed_methods` default is **kept**, with the rule that makes it safe now stated exactly: every `Access-Control-Request-Headers` token must be listed, with **no safelist exemption**, because a browser lists a name there precisely when it is *not* safelisted. |

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
under a `gzip` label, which no browser can decode. This is a pre-existing defect, tracked and fixed
as #326 (independently of this record, by #327, since a shared-cache correctness bug does not wait
on an unrelated routing decision), but it is decisive here: a response policy placed anywhere inside
the cache, or placed outside it without fixing the capture, would store
`Access-Control-Allow-Origin: https://one-tenant.example`
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
- **A route listing `GET` also matches `HEAD`.** RFC 9110 §9.1 requires every general-purpose server
  to support GET and HEAD ("All general-purpose servers MUST support the methods GET and HEAD"),
  §9.3.2 defines HEAD as GET without the body, and Go's own `ServeMux` adopted the same rule for
  pattern matching. Jul's static handler, cache and access log already treat HEAD as a GET-shaped
  request. A route that answers GET and 404s HEAD is a defect an operator would have to discover in
  production. Listing `HEAD` explicitly is permitted and redundant. Listing `HEAD` *without* `GET`
  matches HEAD only. A future `strict_head = true` opt-out is additive if evidence ever demands one.
- `CONNECT` is rejected by validation. It is *not* rejected because Go withholds it from the handler
  — Go routes an authority-form `CONNECT` to the handler with `r.Method = "CONNECT"`,
  `r.URL.Host = "example.test:443"` and an **empty** `r.URL.Path`, which is how one writes a tunnel
  in Go. It is rejected because Jul implements no tunnelling and because an empty path matches no
  tier of §6 — no `exact` path is empty, no configured prefix is a prefix of `""`, not even `/` —
  so the request 404s before any predicate is consulted. A `CONNECT` predicate could therefore never
  fire, and validation says so rather than accepting a rule that silently never matches.
- **On a location with `cors.enabled = true`, a `methods` predicate additionally accepts a CORS
  preflight** — `OPTIONS` carrying exactly one `Origin` and an `Access-Control-Request-Method`.
  Without this rule a CORS-enabled route with `methods = ["GET", "POST"]` could never be selected
  for its own preflight, so the preflight would fall through to another route or to 404 and the
  feature would silently not work on precisely the routes most likely to use it. A plain `OPTIONS`
  that is not a preflight still obeys `methods`. Listing `OPTIONS` explicitly is permitted and
  redundant. This is the same kind of rule as GET⊇HEAD and is stated for the same reason: the
  alternative is a footgun discovered in a browser console.

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
- **`Forwarded`, `X-Forwarded-*` and the RFC 9440 certificate-assertion names require a provable
  trusted-proxy precondition.** Route selection happens before `setCanonicalXForwarded`, so at
  matching time these fields still hold whatever the client sent. A predicate such as
  `X-Forwarded-Proto == https` reads as a security control and is trivially forged. A lint finding
  alone is not enough, because a finding still admits the configuration:

  > **Validation rejects a predicate on these names unless the listen address this location is
  > served on has a non-empty `[servers.client_address].trusted_proxies` policy.** When that
  > precondition holds the predicate is accepted, and still produces a `SeverityError` lint finding.

  `trusted_proxies` is exactly the precondition ADR 0016 already requires before Jul will believe a
  forwarded field at all, so this reuses an existing, operator-declared trust boundary rather than
  inventing a second one. It stays a `SeverityError` even when accepted, because the trust extends to
  the declared proxy, not to the client behind it.

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

This is a decision, not an omission, and it rests on one reason rather than several.

**`Allow` would usually be a lie.** `Allow` is a property of the *resource*, and a gateway route
enumerating `["GET"]` says nothing about whether the upstream implements POST. A gateway that answers
405 on behalf of an upstream it never consulted is asserting something it does not know, and it is
the kind of assertion clients act on. A secondary cost is that computing the header publishes a
host's route topology to an unauthenticated client.

A performance argument appeared in an earlier draft and is **withdrawn as incorrect**: §6's
enumeration already visits every path candidate on the miss path, so accumulating an `Allow` set
would add bookkeeping, not a scan. The truthfulness argument does not need it.

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

- a `name` that is not a valid RFC 9110 §5.1 field-name token, including any name beginning with `:`;
- a `value` that is not a valid RFC 9110 §5.5 field value — that is, any byte outside
  `VCHAR / SP / HTAB / obs-text`. Rejecting only CR, LF and NUL would still admit the other C0
  controls and DEL. Go's `net/http` silently drops an invalid header at write time, which would make
  such a value look like a working configuration that quietly does nothing; configuration time is the
  only place the operator finds out;
- framing and connection-scoped names, which Jul owns: `Connection`, `Content-Length`,
  `Transfer-Encoding`, `Upgrade`, `Keep-Alive`, `Proxy-Connection`, `TE`, `Trailer`,
  `Proxy-Authenticate`, `Proxy-Authorization`;
- `Content-Encoding`, which the compression middleware owns; a configured value would label a body
  that Jul did not encode;
- **any operation on `Vary` when the location has `cache = true`** (§8a), and `set`/`remove` on
  `Vary` always;
- **any operation on an `Access-Control-*` name when the location has `cors.enabled = true`** (§8b).

`Server` and `X-Powered-By` are ordinary mutable headers and removing them is a supported use case.

Bound: 32 operations per location, and 8 KiB total serialized length of the operations a single
response may add, so a bounded count cannot produce an unbounded response.

### 8a. `Vary` may not be operated on a cached location

An earlier draft permitted `add` on `Vary`. That is unsafe, and the reason generalizes into the rule
this record needs.

Response policy runs **outside** the cache, and §11 deliberately makes the cache snapshot its headers
before any outer layer runs. So Jul's own cache never observes an operator-added `Vary` and never
keys on it. The failure is silent and it is a cross-tenant data leak:

1. the upstream returns tenant-dependent content and, being misconfigured, does not send `Vary`;
2. Jul stores tenant A's representation under a key that does not include `X-Tenant`;
3. response policy adds `Vary: X-Tenant` to the client-visible response;
4. tenant B's request hits that entry and is served tenant A's body — under a header that truthfully
   tells every *downstream* cache the response varies.

The operator wrote the correct-looking thing and got a leak. RFC 9110 §12.5.5 defines `Vary` as
expanding the cache key required for safe reuse; a `Vary` announced after the storage decision cannot
do that for the store it was announced too late to reach.

> **The governing rule: an outer layer may announce `Vary` if and only if the variance it announces
> is introduced by that outer layer itself.**

That is what separates the three cases, and it is why two of them stay:

| `Vary` field | Introduced by | Stored body depends on it? | Safe outside the cache? |
| --- | --- | --- | --- |
| `Accept-Encoding` (compression) | the compression layer, at emission | no — the stored body is uncompressed | **yes** |
| `Origin` (CORS, §9) | the CORS layer, at emission | no — the stored body is origin-independent | **yes** |
| anything an operator writes | the *upstream*, before storage | **yes, by assumption** | **no** |

Therefore:

- a `Vary` operation on a location with `cache = true` is a **validation error**, naming `cache` as
  the conflicting field;
- on a location without the cache, `add` on `Vary` is permitted and documented as a directive to
  *downstream* caches only, with no effect on Jul;
- `set` and `remove` on `Vary` remain rejected everywhere, because compression and CORS co-own the
  field.

**The condition is the location's own `cache = true`, not the effective cache state.** A location is
only really cached when the global `[cache]` block is also enabled, but validation keys on the
location's declaration alone. Keying on the effective state would mean toggling a distant block
retroactively invalidates a location the operator did not touch, and — worse — that a configuration
which validates today starts leaking the day someone enables the global cache. Keying on the
declaration fails at the moment the operator writes the contradiction, which is the only moment they
can act on it.

Making this conditional on `cache` means enabling the cache later fails the reload loudly instead of
turning a working route into a leaking one. That is the correct direction for the failure to point.

**What an operator with genuine variance does instead.** Jul's cache *does* honour a `Vary` that
arrives on the upstream response: it is inside the §11 snapshot, so `buildEntry` records it, and
`variantKey`/`matchesVary` key on it exactly as RFC 9110 requires. The supported answer to "my
backend varies by `X-Tenant`" is therefore *the correct one* — make the backend say so — and it needs
no feature from this record. What is rejected is only the case where the operator asserts variance
Jul's own store did not observe. The reference documentation must state this at the point of use,
because "rejected" without "here is what to do instead" is a bad error message.

**The capability this costs, stated rather than glossed.** An operator whose upstream genuinely
varies but does not send `Vary`, and which they cannot change, has exactly one option under this
record: **`cache = false` on that route**. That is a real narrowing — NGINX would let them write
`proxy_cache_key` — and it is accepted deliberately, because the alternative on offer was a header
operation that *looks* like it protects Jul's cache and does not. Losing caching on one route is
recoverable; serving tenant A's body to tenant B is not. §17 records the contract that would lift the
restriction and who owns it.

Expressing variance to Jul's own cache *without* the upstream's cooperation needs a cache-key contract
consumed inside the cache. That is deferred (§17) rather than smuggled in through a header operation.

### 8b. CORS owns every `Access-Control-*` field

Generic operations and CORS live in the same wrapper (§10), so their interaction is a public security
contract and not an implementation detail.

**Order within the wrapper: generic `response_headers` operations run first, then CORS.** CORS is
therefore authoritative and last-writer. With the ownership rule below the order is very nearly
unobservable, which is the point — but it is fixed so that it is never discovered empirically.

When `cors.enabled = true`:

- a `response_headers` operation naming any `Access-Control-*` field is a **validation error**,
  naming `cors` as the conflicting block. There is exactly one way to configure CORS;
- **every `Access-Control-*` field on the upstream response is removed** before Jul emits its own
  set. An upstream that also implements CORS would otherwise produce two `Access-Control-Allow-Origin`
  field lines, which the Fetch standard treats as a failure — so a working upstream plus a working Jul
  policy would compose into a broken one;
- **an upstream `Vary: Origin` is *not* removed**, even though it makes Jul's cache create one variant
  per origin against the 64-variant ceiling. Stripping it is the tempting optimization and it is the
  §8a leak with the sign flipped: if the upstream's *body* genuinely varies by origin, removing its
  `Vary` stops Jul keying on something the stored representation actually depends on. The variant cost
  is accepted; correctness is not negotiable against it. Jul strips the *grant* headers, which it is
  replacing, and never the *variance* declaration, which it did not author;
- **generic `response_headers` operations do not apply to a Jul-generated preflight response.** Its
  header set is exactly the CORS set of §9 plus `Vary`, nothing else. A preflight is a protocol
  artefact Jul manufactures, not a representation of the operator's resource; keeping it closed is
  also what keeps its size bounded, which §10 relies on.

When `cors.enabled = false`, `Access-Control-*` operations are permitted — a migrated NGINX
`add_header Access-Control-Allow-Origin` must remain expressible — and produce a lint warning
pointing at `[servers.locations.cors]`.

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

**Origins are exact, normalized, byte-compared, with one deliberately unconditional wildcard.**

- A configured origin must be `scheme "://" host [ ":" port ]` with an ASCII-lowercase scheme and
  host, no path, no trailing slash, and no explicitly-written default port. `https://a.example:443`
  is a validation error naming `https://a.example`, because browsers never send the default port and
  the configured form would never match. Comparison at request time is then a byte comparison
  against the `Origin` field value.
- `"*"` is permitted **only** when `allow_credentials = false`. `"*"` together with
  `allow_credentials = true` is a validation error, not a silently downgraded reflection: the Fetch
  standard makes that combination inert, and a gateway that quietly reinterprets it is teaching the
  operator a false model of their own policy.
- **`allowed_origins = ["*"]` is unconditional.** `Access-Control-Allow-Origin: *` is emitted on
  every response from the location — including requests carrying no `Origin`, and including
  `Origin: null` — and `"*"` may not be combined with any other entry.

  An earlier draft said `"*"` did not cover `null` while also suppressing `Vary: Origin` for the
  wildcard because "the output is constant". Those two statements cannot both hold: if a `null`
  origin received no grant while every other origin received `*`, the output *would* vary by origin
  and suppressing `Vary` would be unsafe. The wildcard is also not enforceable against `null`
  anyway — Fetch's CORS check succeeds whenever `Access-Control-Allow-Origin` is `*` and the
  request is not credentialed, **without comparing the serialized origin at all** — so a browser
  would honour the grant Jul emitted to some other origin regardless of what Jul intended about
  `null`. Making the wildcard unconditional is the only model that is simultaneously true,
  enforceable and cache-safe, and it is what Fetch's HTTP-cache guidance assumes: when `Vary` is
  omitted for `*`, the header must also be sent on non-CORS responses.
- `"null"` is meaningful only in a **non-wildcard** policy, where it is matched when listed
  literally. Listing it produces a lint warning: the `null` origin is what sandboxed iframes,
  `data:` documents and some cross-origin redirects send, and it is not an authentication of
  anything.
- Scheme and port are significant. There is **no** wildcard subdomain syntax and **no** regex in this
  tranche.
- A request carrying more than one `Origin` field line is not a valid CORS request and is treated as
  having no origin. A syntactically malformed `Origin` matches nothing.
- Origin matching performs no DNS resolution and no network access.

**The other three lists.**

- `allowed_methods` follows §2's token and uppercase rules, rejects duplicates, and is emitted in
  declaration order. It governs **preflight approval only**. It does not gate ordinary requests —
  that is what `match.methods` is for — and this record says so explicitly because conflating the
  two is the most common CORS misconfiguration there is.
- `allowed_headers` and `exposed_headers` must be valid RFC 9110 §5.1 field-name tokens, are
  canonicalized for comparison, are compared case-insensitively, reject duplicates after
  canonicalization, and are emitted in declaration order so the response is byte-stable across
  reloads. `allowed_headers` likewise governs **preflight approval only**.
- **`"*"` is not accepted in `allowed_methods`, `allowed_headers` or `exposed_headers`** in this
  tranche. Under Fetch, a wildcard in `Access-Control-Allow-Headers` does *not* cover `Authorization`,
  which is precisely the header an operator writing `"*"` is trying to allow. Rather than ship a
  wildcard with a single silent exception, the lists stay explicit. Re-entry trigger in §17.

**`max_age`.** Omitted means `Access-Control-Max-Age` is not emitted and the browser applies its own
default. A configured value must be a whole number of seconds — a sub-second duration such as
`"500ms"` is a validation error rather than a silent truncation — non-negative, and at most 24 hours,
which is the ceiling every major browser clamps to. `max_age = "0s"` is legal and emits `0`, which
means "do not cache this preflight". The header is serialized as an integer count of seconds.

**Defaults, empties and `enabled = false`.** Each of these has exactly one answer, because "unstated"
is how a security feature acquires two:

| Field | Omitted | Explicitly `[]` | Emitted when the effective list is empty |
| --- | --- | --- | --- |
| `allowed_origins` | **validation error** when `enabled = true` | validation error | n/a |
| `allowed_methods` | defaults to `["GET", "HEAD", "POST"]` | validation error | n/a |
| `allowed_headers` | defaults to `[]` | validation error | header **omitted**, not emitted empty |
| `exposed_headers` | defaults to `[]` | validation error | header **omitted**, not emitted empty |

- `allowed_origins` has no default. A CORS block that allows nothing is a mistake, and the way to
  turn CORS off is `enabled = false`, not an empty allow-list that silently denies every request
  while looking configured.
- `allowed_methods` defaults to the CORS-safelisted methods. Those are the methods a browser will
  send *without* a preflight at all, so defaulting to them makes an operator who writes only
  `allowed_origins` get the behaviour they expected rather than a policy that denies every preflight.
  Defaulting to empty — deny everything — would be defensible in isolation and is rejected because it
  makes the minimal configuration silently useless.
- An explicit `[]` is a **validation error** on all four, for the same reason `match.methods = []` is
  (§1): a list that can never match is a mistake, not a way to express a policy.
- **Every token in `Access-Control-Request-Headers` must appear in `allowed_headers`. There is no
  implicit safelist exemption**, so an empty `allowed_headers` approves a preflight only when the
  request lists no headers at all. This is both simpler and safer than carving out the
  CORS-safelisted names, and it is correct rather than merely strict: a browser includes a name in
  `Access-Control-Request-Headers` *precisely when it is not safelisted*. `Content-Type` appears
  there only when its value is outside `application/x-www-form-urlencoded`, `multipart/form-data`
  and `text/plain` — a JSON body, in other words — so treating a listed `Content-Type` as safelisted
  would approve exactly the preflight the operator did not authorize. `Access-Control-Allow-Headers`
  is omitted from the response when the list is empty, rather than emitted with an empty value.
  Jul never reflects `Access-Control-Request-Headers` back: reflecting is how a bounded policy
  becomes an unbounded one.

  This rule is what makes the `allowed_methods` default above safe. An origins-only policy permits a
  *simple* cross-origin `POST` — which the browser would send without a preflight anyway, and which
  already receives an allow-origin header — while a JSON `POST` or an `Authorization`-bearing `POST`
  is denied, because each puts a non-safelisted name in `Access-Control-Request-Headers` that
  `allowed_headers` does not list. The method default and the header rule are a pair: permitting the
  method is not permitting the request.
- **`enabled = false` does not skip validation of the values that are present.** Every field the
  operator wrote is validated exactly as if the block were enabled, and the block is inert at
  runtime. *Requiredness* stays conditional: `allowed_origins` is required only when
  `enabled = true`, so a disabled block may legitimately omit it, but a disabled block that *does*
  list an origin has that origin normalized and checked. This is the same principle as §8a's
  cached-location rule: an operator who flips `enabled = true` must not discover then that the values
  they wrote were never valid. A populated block with `enabled = false` is accepted and lint-warned,
  so a deliberately-parked policy is possible and a forgotten one is visible.

**Two questions, kept separate.** Whether Jul *processes* the request and whether Jul *tells the
browser the response may be read* are different questions. CORS is a browser policy, not server
authorization: a request from a disallowed origin is still routed, authenticated, rate-limited and
served exactly as it would be without a CORS block; it simply receives no `Access-Control-*` headers,
so the browser refuses to hand the response to the script. Operators who need origin-based *access
control* use auth or a header predicate, and the reference documentation says so where `cors` is
defined.

**Preflight.** A request is a preflight when it is `OPTIONS`, carries exactly one `Origin`, and
carries `Access-Control-Request-Method`.

A preflight is deliberately stripped by the browser of nearly everything a route might predicate on:
it carries no `Authorization`, no cookies and none of the application headers the actual request will
carry. Two consequences follow, and both are contract, not implementation detail.

- §2's rule applies: a `methods` predicate on a `cors.enabled` location also accepts the preflight,
  so the route can be selected for it.
- **A `cors.enabled` location that also carries header predicates produces a lint warning.** The
  browser will not send those headers on the preflight, so the preflight will not select that route,
  and the operator would otherwise debug an intermittent-looking CORS failure that is really a
  routing outcome. Jul does not silently exempt header predicates for preflights: that would create
  a second, invisible matching mode, and §6 exists to prevent exactly that.

- An **approved** preflight — allowed origin, requested method in `allowed_methods`, and every
  comma-separated token of `Access-Control-Request-Headers` in `allowed_headers` compared
  case-insensitively — is answered by Jul with **204 No Content**, an empty body, and no upstream
  contact and no cache interaction.
- A **denied** preflight is *not* short-circuited. It continues down the ordinary chain and receives
  whatever that route would return for an `OPTIONS` request, with no `Access-Control-*` headers. Jul
  does not invent a 403 that discloses that a CORS policy exists, and it does not exempt the request
  from authentication, rate limiting or the WAF.
- **`Access-Control-Request-Method` must be exactly one field line carrying exactly one method
  token.** Repeated field lines, or a single line carrying a comma-separated list, are not a
  well-formed preflight: the request is not approved and is passed down the chain like any other
  `OPTIONS`. Fetch specifies a single method, and accepting a list would let a client widen its own
  preflight.
- `Access-Control-Request-Headers` is bounded at 64 tokens; a longer list is not approved.
- Preflight responses carry `Access-Control-Allow-Origin`, `Access-Control-Allow-Methods`,
  `Access-Control-Allow-Headers` **when `allowed_headers` is non-empty**, `Access-Control-Max-Age`
  when `max_age` is set, `Access-Control-Allow-Credentials: true` when credentials are enabled, and
  `Vary: Origin, Access-Control-Request-Method, Access-Control-Request-Headers` — except under the
  unconditional wildcard, where `Origin` is omitted from that list for the reason given above. No
  other header is emitted: §8b excludes generic `response_headers` operations from generated
  preflights.

**Actual requests.** Under the unconditional wildcard, Jul emits `Access-Control-Allow-Origin: *` on
every response, whatever the request's `Origin` was or whether it had one. Under a non-wildcard
policy, an allowed origin gets `Access-Control-Allow-Origin` set to that origin. In both cases,
`Access-Control-Allow-Credentials: true` when enabled and `Access-Control-Expose-Headers` when
`exposed_headers` is non-empty. Preflight-only headers are not emitted on actual responses.

**`Vary: Origin`.** Exactly two cases, and the split follows §8a's rule that an outer layer may
announce only the variance it introduces:

| Policy | Emitted allow-origin | `Vary: Origin` |
| --- | --- | --- |
| `allowed_origins = ["*"]`, `allow_credentials = false` | `*`, unconditionally, on every response | **omitted** — the output genuinely does not vary |
| anything else | request origin when allowed, nothing when not | **always appended**, including for requests with no `Origin` and for disallowed origins |

The second row's "including requests with no `Origin`" is the part that matters: without it a shared
downstream cache stores the no-origin response and replays it to a cross-origin request. The first
row is only sound because the wildcard was made unconditional above — a wildcard with any exception
carved out of it varies by origin, and suppressing `Vary` would then be a cache-poisoning bug rather
than an optimization.

**These two rules are load-bearing for each other, and prose is not a strong enough guard.** A later
change that carves any exception out of the wildcard, without also restoring `Vary: Origin`, is a
cache-poisoning bug that no test of either rule *in isolation* would catch. Two structural
requirements, so the coupling survives an editor who has not read this paragraph:

1. **One derived decision, not two.** The emission of `Access-Control-Allow-Origin` and the decision
   to suppress `Vary: Origin` are computed from a single predicate over the compiled policy — the
   policy is a constant-output wildcard, or it is not. They are never two independent branches.
2. **A coupling test**, named as such, asserting the property rather than the pair of behaviours:
   *for every request shape, `Vary: Origin` is absent only if the emitted
   `Access-Control-Allow-Origin` is byte-identical across all of them.* A future exception to the
   wildcard fails that test at the point it is introduced, which is the only place it is cheap to
   fix.

`Vary` is always *appended*, never replaced, so compression's `Accept-Encoding` survives. This is the
same field §8 forbids operators from setting or removing, and §8a forbids them from adding on a
cached location — CORS may append it precisely because CORS is what introduced the variance.

**CORS headers are emitted on error responses.** A browser cannot read the body or status of a
cross-origin 401, 403, 429 or 502 without `Access-Control-Allow-Origin`, and an operator debugging a
gateway through a browser console is the exact person this affects. Because the policy is applied by
the outermost per-location layer (§10), this happens for every response the location produces —
including auth denials, rate-limit rejections, WAF blocks, upstream failures and cache hits — rather
than only for the ones that reach the action.

Private Network Access (`Access-Control-Request-Private-Network`) is out of scope; see §17.

### 10. Execution order

The per-location chain gains three positions. Nothing existing moves.

```
request
  RequestID
  ClientAddress                     (ADR 0016 canonical client, per listener)
  Tracing / Metrics / AccessLog     (observers)
  Recover
  Compression
  router: host -> location (§6)     <- method/header/query predicates evaluated here
    >> response policy + CORS response headers        (NEW, outermost per-location)
    >> location recover                               (NEW, routed panics get the policy)
    plugins
    ClientCert
    >> CORS preflight termination                     (NEW: decide, then guard, then 204)
    Auth
    RateLimit
    WAF
    BodyLimit
    cache
    action (static / proxy / gRPC / transcode / fastcgi / redirect / return / deny)
```

`BodyLimit` is inside the location modifiers, not outside them: `buildServerRoute` applies it to the
action and the modifier chain then wraps the result, deliberately, so rate limiting runs before the
request body is read. An earlier draft of this diagram placed it outermost, which contradicted this
record's own *Existing architecture* table. The correction matters for exactly one decision, and it
falls the right way — see the 413 in the list below.

**Response policy is the outermost per-location layer** so that it decorates every response the
location can produce: an auth 401, a rate-limit 429, a WAF 403, a **BodyLimit 413**, an upstream 502,
a cache hit, a static 404, a redirect. The 413 is worth naming: a cross-origin upload that exceeds
`client_max_body_size` is precisely a case where the browser must be able to read the error, and it
is readable only because the policy wrapper sits outside `BodyLimit`. It is implemented as a
`ResponseWriter` wrapper that applies its operations inside `WriteHeader`, immediately before
delegating outward — never by pre-setting headers before calling `next`, which would put them in the
shared header map where the cache would capture them (§11). It is composed through `respwriter.Wrap`,
so `Flusher`, `Hijacker`, `Pusher`, `ReaderFrom` and `Unwrap` remain exactly as truthful as they are
without it. After a hijack it is inert. A location with neither `response_headers` nor `cors`
installs no wrapper at all.

**Informational responses do not trigger the policy.** Go permits any number of `1xx` calls to
`WriteHeader` followed by exactly one final status. The wrapper passes `1xx` straight through without
applying anything, without latching, and without recording a status; it applies its operations
exactly once, on the first status `>= 200`. `101` keeps its own treatment as a protocol switch, not
an interim response.

This is stated because Jul's three existing wrappers — `Recorder`, `compressWriter` and `cacheWriter`
— all latch on the *first* `WriteHeader` regardless of code, so a `103 Early Hints` swallows the real
status and the client is served `200`. `Recorder` is on the global chain unconditionally, so that
affects every deployment. It is filed as **#331** and is not this record's to fix; the rule is written
down here so the response-policy wrapper does not become a fourth instance of the same defect.

**Preflight termination sits immediately outside `Auth`**, which is the minimum bypass that makes the
feature work. The CORS-preflight fetch is defined to be sent with credentials omitted, so a route
with Jul-level authentication can never complete a credentialed CORS exchange if the preflight has to
authenticate first. Because `Auth` currently wraps `RateLimit` and the WAF — deliberately, so that
`jwt:<claim>` rate-limit keys and identity-aware WAF rules work — terminating outside `Auth` would
also skip those two, and an earlier draft simply accepted that. It should not have. `Origin` is
attacker-controlled outside a browser and permitted frontend origins are normally public, so "the
attacker must know an allowed origin" is not a control.

**The terminator therefore decides first and guards second**, in one layer rather than two:

```
if the request is not a preflight            -> pass through untouched
approval := evaluate CORS policy             -> pure, three header fields, no side effects
if not approved                              -> pass through untouched; the ordinary chain
                                                (Auth, RateLimit, WAF) handles it as any OPTIONS
if approved:
    coarse rate-limit check                  -> 429 on reject
    WAF check                                -> the firewall's own verdict on reject
    emit 204 with the CORS header set
```

Deciding before guarding is what keeps the guards from running twice: a denied preflight is guarded
by the ordinary chain, an approved one by the terminator, never both. Authentication is the only
thing genuinely skipped.

- **Rate limiting.** When the location has an effective rate-limit policy — its own `[rate_limit]` or
  the global one — the terminator evaluates a second instance of that same policy, in its own scope,
  for approved preflights only. No other request pays for it, and it shares no bucket with the
  identity-aware limiter that still guards actual requests after authentication. There is no new
  configuration surface: the operator's own rate and burst govern it.

  **The guard overrides the policy's `key` to the canonical client address**, unconditionally. A
  policy keyed `jwt:<claim>` or `header:<name>` cannot be evaluated here — authentication has not run
  and a preflight carries neither — and silently falling back to an unkeyed global bucket would make
  one client's preflights limit everyone's. Reusing a per-user rate as a per-client-address rate is a
  change of meaning, and a deliberate one: it is the conservative direction, and it applies only to
  preflights. The guard reports through the existing rate-limit counter with the existing `ip` kind;
  no new metric and no new label value is introduced.

  A location with no rate-limit policy at all gets no guard, which is consistent — a preflight is no
  cheaper to abuse there than any other request on a route the operator chose not to limit.

- **The WAF also runs**, reusing the location's already-compiled firewall from `wafByScope`. An
  earlier draft skipped it on the claim that "a preflight's entire inspectable surface is three
  fields Jul has already parsed and bounded". **That claim was wrong and is withdrawn.** A preflight
  is a complete HTTP request: it carries the full request target, the query string, every other
  request header, the method and the canonical client address, and Coraza's request-phase rules
  operate on exactly those without ever needing a body. Skipping the WAF would have created a
  request shape that reaches a Jul-generated response having passed no rule at all — and an attacker
  chooses the shape. Running it costs a request-phase evaluation on approved preflights only, which
  is what the operator asked for by enabling the WAF on that route.

**Neither guard needs a new API.** Both are already `middleware.Middleware`, so "guard" means
composing them around the 204 emitter in the ordinary way — roughly
`rateGuard(wafGuard(emit204))` — and letting each either write its own rejection or call through.
This is stated because "apply a rate-limit check and a WAF check" invites inventing a boolean
`Check()` entry point on both subsystems, which would be a second code path with a second set of
metrics, logs and failure modes beside the one that already works. It also means the WAF's
response-phase rules see the generated 204, which is correct.

Moving `Auth` inside `RateLimit`/`WAF` remains rejected: it would break two documented, load-bearing
behaviours to solve a problem the terminator's own guards already solve without moving anything.

**A panic *after* route selection produces a 500 that carries the location's policy.** An earlier
draft documented the opposite as a boundary, which was the wrong call. The global `Recover` sits
outside the router, so a panic in a plugin, an authenticator, the WAF or the action produced a 500
emitted outside the per-location chain — no response headers, and crucially no
`Access-Control-Allow-Origin`. For a browser client that turns an informative generic 500 into an
opaque CORS failure, on the request most likely to need diagnosing. §9 already argues that a
cross-origin 401, 403, 429 or 502 must be readable; there is no principled reason 500 is different,
and "it happened to sit outside the router" is not one.

So route-scoped recovery is added, at exactly one place:

```
    >> response policy + CORS response headers        (outermost per-location)
    >> location recover                               (NEW)
    plugins ... action
```

| Panic occurs | Recovered by | Location policy applied? |
| --- | --- | --- |
| Before route selection — router, observers, compression | global `Recover` | no — no location is known |
| After selection, **before** the response commits | **location recover** | **yes** — the generic 500 is written through the policy wrapper |
| After the response commits | neither can help | the status is already on the wire; the stream ends incomplete |
| In the policy wrapper itself | global `Recover` | no — the fallback stays |

The global `Recover` is **not** removed: it remains the final backstop for the router, the policy
wrapper and everything before selection, and it is what keeps the observers recording a 500 rather
than losing the request. The location recover writes a generic 500 and adds no detail the global one
does not — this is about which headers accompany it, not about leaking panic text. Cost is one
deferred recovery per routed request.

**Responses produced outside route selection still carry no location policy**, by construction: the
router's own 404, the HTTP→HTTPS redirect and the ACME challenge handler are all emitted before a
location exists. That remains a documented boundary — it is now a boundary about *what could not have
a policy*, rather than one that also swallowed a case that could.

### 11. Cache × response policy: the cache stores the origin representation

Two rules, and the second is what makes the first true.

1. **Response policy and CORS are applied outside the cache**, so nothing they produce is ever
   eligible for storage.
2. **`cacheWriter` snapshots the response header map inside its own `WriteHeader`, before delegating
   outward, and `buildEntry` consumes that snapshot instead of re-reading `w.Header()` after the
   stack unwinds.** A header set after `WriteHeader` is excluded, which is correct: it never reached
   the client either.

Rule 1 alone is not sufficient and this record must say so explicitly, because the natural reading of
"place it outside the cache" is that placement settles it. It does not. Every wrapper in the chain
shares one header map; a layer outside the cache mutates that map during its own `WriteHeader`, which
runs *before* `fetchAndStore` reads it. That is not a hypothesis: it is the reproduced compression
defect described in the Context, and a response policy would inherit it exactly.

The consequences of rule 2 are what this record wants:

- A stored entry contains the origin representation and nothing an outer layer added **after the
  handler committed its response**. No `Access-Control-Allow-Origin`, no operator header operations,
  no `Content-Encoding` that Jul applied.
- **The guarantee stops at the commit boundary, and this record says so rather than rounding up.**
  A layer outside the cache that writes a response header *before* calling `next` is already in the
  shared map when the snapshot is taken, so the snapshot cannot exclude it. That class is not
  hypothetical: `RequestID` does exactly this, so a hit is served a stale `X-Request-ID` beside the
  current one (#332), and the cache does it to itself with `X-Cache`, which is why
  `buildEntry`'s `stored.Del("X-Cache")` is load-bearing rather than vestigial.
- **What this record needs is narrower than "stores exactly the origin representation", and the
  difference matters.** The property §8 and §9 depend on is one-directional:

  > **No header contributed by a layer outside the cache may appear in a stored entry.**

  That is a *no-leak* property. It is what makes a stored entry safe to serve to a different origin,
  a different tenant and a different request. An entry that is missing something the origin sent is a
  **fidelity** defect, not a leak, and nothing in §8, §9 or §10 rests on fidelity.

  The distinction is worth drawing because the obvious mechanism — storing the multiset difference
  between the commit snapshot and the map as it stood on entry — delivers the no-leak property but
  **does not** deliver fidelity, and this record should not claim it does. Two cases survive it: a
  handler that `Set`s a name to the same value an outer layer already set (the difference is empty,
  so the entry under-stores), and a handler that *deletes* an outer field (there is no tombstone, so
  a hit resurrects the outer layer's current value). Neither is a cross-request leak — an outer layer
  re-derives its own value per request — but both are real, and calling the difference a proof of
  "only what the handler contributed" would be false.

  **The mechanism is #332's to choose**, and a genuine inner-header ownership boundary is the shape
  that would prove the stronger property. This record requires only the no-leak property, states the
  residual fidelity gap so nobody rediscovers it as a surprise, and requires #332 to test
  same-value replacement and deletion rather than only different-value overwrite.
- **Nothing in §8, §9 or §10 is a member of the leaking class.** The response-policy and CORS wrapper
  applies at commit, never before `next` (§10), which is the same rule stated from the other side.
  This is why the ordering rule in §10 is a contract and not a style preference: it is what keeps the
  per-origin CORS argument true.
- **No policy generation stamp is needed anywhere in the cache.** A gob-encoded entry rehydrated from
  disk after a restart, written under a configuration nobody can inspect any more, cannot carry a
  CORS or operator header because it never contained one. This is why the simplest architecture is
  also the correct one: the alternative — versioning entries by policy generation, or purging the
  cache on policy change — requires the cache to understand policy identity, survives neither disk
  persistence nor a config edit made while the process was stopped, and would still be wrong for the
  compression case.
- **Jul-generated CORS variance creates no internal variants; an upstream `Vary: Origin` remains
  authoritative and still does.** `Vary: Origin` that *Jul* appends is added outside the cache, is not
  stored, and therefore creates no variants — correctly, because the stored representation is
  origin-independent when CORS introduced the variance at emission (§8a). A `Vary: Origin` that
  arrived *on the upstream response* is inside the snapshot, is stored, and does create one variant
  per origin against the 64-variant ceiling. That cost is accepted: the upstream is asserting that its
  own body varies, and §8b therefore strips upstream `Access-Control-*` grants while never touching
  upstream `Vary`.
- Approved preflights never reach the cache: they terminate above it, and `OPTIONS` is not a stored
  method in any case.
- Cache invalidation, `Vary` stubs, variant membership and the disk store are otherwise untouched.

**Invariant, as the implementation can state it today:** *no header written by a layer outside the
cache handler **after the handler commits its response** may appear in a stored cache entry.*

**Invariant this record requires, and #332 must deliver:** *no header contributed by a layer outside the
cache may appear in a stored entry, whenever it was written.* The second subsumes the first and is
the one §8's and §9's reasoning should be read against; the first is what is true on `main` today.
Neither claims fidelity — that a stored entry reproduces the origin's headers exactly — and no
decision in this record depends on fidelity.

### 12. HTTP semantic parity — not capability parity

Predicates, response policy and CORS are properties of a *location*, so there is exactly one matcher
and exactly one response-policy wrapper, and their semantics never differ by transport. What differs
is which actions a given inbound transport can carry at all, and an earlier draft of this section
claimed "identically on HTTP/1.1, h2c, HTTP/2 and HTTP/3, and to every action", which is not true and
would have produced a test matrix full of impossible combinations.

The precise claim:

> **Wherever an action is supported on an inbound transport, this record's matching and policy
> semantics are identical on that transport. Where the action is unsupported, this record changes
> nothing about that.**

| Inbound | Predicates | `response_headers` | CORS | Notes |
| --- | --- | --- | --- | --- |
| HTTP/1.1 | yes | yes | yes | — |
| h2c / HTTP/2 | yes | yes | yes | — |
| HTTP/3 | yes | yes | yes | — |
| WebSocket upgrade | yes (it is an HTTP request) | on the 101 only | **no** | **HTTP/1.1 only** — see below |
| Native gRPC | yes | yes, on the HTTP response headers | accepted, lint-warned | end-to-end **HTTP/2 only** by design (`docs/grpc-proxy.md`) |
| gRPC transcoding | yes | yes | yes | the transcoded response is an ordinary HTTP response |
| L4 `[[stream]]` TCP/UDP | **none** | **none** | **none** | out of scope — see below |

- **WebSocket upgrade is HTTP/1.1 only.** Response-header operations apply to the 101 like any other
  response, which is safe because `Connection` and `Upgrade` are rejected at validation, so the
  handshake cannot be broken by configuration. After the hijack the wrapper is inert. CORS headers
  are **not** emitted on a 101: the WebSocket handshake has its own origin model and
  `Access-Control-*` means nothing on it.

  All three of those statements describe the HTTP/1.1 `Upgrade` mechanism and only it. WebSocket over
  HTTP/2 is [RFC 8441](https://www.rfc-editor.org/rfc/rfc8441.html) extended `CONNECT`: it answers
  **200, not 101**, and it never hijacks, because an HTTP/2 stream is not a connection to take over.
  HTTP/3 is the same mechanism via RFC 9220. **Jul implements neither**, which `docs/cache.md`
  already states and [ADR 0017](0017-upstream-resilience-and-overload-control.md) repeats for HTTP/3.
  An earlier draft of this matrix said "HTTP/1.1 and HTTP/2", taken from a stale line in
  `docs/http3.md` rather than verified — exactly the mistake this record warns against elsewhere.
  Both documents are corrected alongside this one.

  §17 records what adopting RFC 8441/9220 would cost and what would trigger revisiting it.
- **Native gRPC passthrough.** Predicates apply — the method is always `POST`, and `content-type`,
  `te` and gRPC metadata are ordinary header fields. Response-header operations apply to the HTTP
  response headers. **gRPC trailers are not touched**: `Trailer` is a rejected name and trailer policy
  is out of scope, so `grpc-status` and `grpc-message` are untouched end to end. Mutating
  `Content-Type` on a gRPC location produces a lint warning because it will break clients. `cors` on
  a native gRPC location is accepted but lint-warned: gRPC-Web is not implemented, so the
  configuration is almost certainly not doing what the operator expects.
- **The L4 stream proxy is explicitly out of scope.** `[[stream]]` routes TCP and UDP by listener,
  not by request: there is no method, no header, no query and no response header to operate on,
  because there is no HTTP message. This record adds nothing there and no part of it should be read
  as implying otherwise. Stating it matters because "routing" is the same word in both subsystems and
  the omission would otherwise look like an oversight rather than a boundary.

**Consequence for #145 and #146:** the required cross-protocol tests use a *supported*
transport/action matrix. A test asserting CORS behaviour on a WebSocket 101, WebSocket over HTTP/2 or
HTTP/3, or predicates on an L4 stream route, is asserting something this record does not claim.

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
predicates. This record fixes the addressing scheme rather than leaving #145 to discover it — but it
deliberately fixes *two different problems* with two different mechanisms, because an earlier draft
used one mechanism for both and the weaker property leaked into the stronger role.

**Selecting a location in a patch — an ordinal, bound to a version.**

- The typed patch request gains an optional **`match_ordinal`**: the 0-based index among the
  locations sharing the other four coordinates, in declaration order. Omitted means "there must be
  exactly one", which is precisely today's behaviour, so every existing client and every existing
  payload keeps working. `findLocation` continues to reject an ambiguous target rather than guess.
- **A patch that carries `match_ordinal` requires `base_version`**, and is rejected with `409` when
  it is absent or stale. `base_version` is optional today, and an empty one is an explicit
  force-apply — which is safe for a coordinate tuple that names a route, and *not* safe for an
  ordinal. An ordinal is only meaningful relative to a specific configuration revision: inserting a
  same-path route above the target shifts every later ordinal, so a force-applied ordinal patch
  silently edits a different route than the operator previewed. Requiring the CAS binding turns that
  race into a `409`.
- `match_ordinal` is therefore a **revision-relative selector, and explicitly not an identity.** It
  must not be persisted, exported as a resource name, or used to correlate anything across revisions.

**Naming a location's state across revisions — a predicate fingerprint.**

`AuthScope`, `WAFScope` and the per-location rate-limit scope key on
`listen | names | match.path` today, which already collides between an exact and a prefix location on
the same path — a pre-existing defect that predicates would turn from unlikely into ordinary. The
obvious repair is to append the match type and the ordinal, and that is wrong: a rate-limit bucket
carries live state, and an ordinal-keyed bucket **transfers to a different predicate set** the moment
an operator inserts or reorders a same-path route. An operator adding a route would silently hand one
route's accumulated limiter state to another.

So these scopes key on a **canonical predicate fingerprint**: a deterministic digest over the listen
address, the normalized `server_names` set, the match type, the path, the normalized predicate set
(methods sorted, header and query predicates sorted by `(name, op, value)`), **and the effective
matcher bit defined below**. It is stable across insertion and reordering, changes exactly when the
route's matching behaviour changes — which is also exactly when resetting the state is correct — and
it is derived, so there is nothing for an operator to keep in sync.

**The fingerprint must include `preflight_widening`, because §2 made CORS part of matching.**

```
preflight_widening := cors.enabled && match.methods is present
```

§2 widens a `methods` predicate on a `cors.enabled` location to accept preflights. That makes
`cors.enabled` a *matcher* input, not only a policy input, and omitting it from the fingerprint is a
correctness bug rather than an aesthetic one. Two routes with the same type, path and predicates but
different `cors.enabled` are **not** the same route:

```toml
# route A — rejects OPTIONS
[[servers.locations]]
proxy_pass = "http://api"

[servers.locations.match]
type = "prefix"
path = "/api/"
methods = ["GET"]

# route B — same coordinates, but reachable for a preflight, which A rejects
[[servers.locations]]
proxy_pass = "http://api"

[servers.locations.match]
type = "prefix"
path = "/api/"
methods = ["GET"]

[servers.locations.cors]
enabled = true
allowed_origins = ["https://app.example.test"]
```

Without the bit, A and B collapse to one fingerprint and therefore share an auth, WAF and rate-limit
scope — recreating precisely the collision §14 exists to remove.

**A durable external route identity is deferred to ADR 0019.** An optional stable `route_id` is the
right long-term answer for API resource naming, diff correlation and Console deep links, but external
resource identity is #118's subject, and freezing one here would pre-empt it. §17 records the
re-entry trigger. Until then, no surface presents `match_ordinal` as an identity.

**Other consequences:**

- Route projections and the diff emit `match_ordinal` — labelled as revision-relative — alongside the
  existing coordinates, and the diff renders a predicate summary so two same-path routes are
  distinguishable in a preview. The diff correlates routes by fingerprint, not by ordinal, so
  reordering renders as a move rather than as a mutation of every route below it.
- Changing the rate-limit scope derivation resets those buckets once, on upgrade; that is acceptable
  and must be in the changelog.
- **The admin route-test surface must call the router's selection function, not its own.**
  `bestServer`/`bestLocation` are deleted and `routeTestRequest.Method`/`Headers` — already present
  and already ignored — become real inputs. Two matching implementations are how the Console acquires
  semantics the server does not have, which ADR 0014 forbids.

**The route-test request type cannot express this record's model, so its extension is frozen here.**
Today it is:

```go
type routeTestRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Host    string            `json:"host"`
	Headers map[string]string `json:"headers,omitempty"`
}
```

A `map[string]string` cannot carry repeated header field lines, and there is no query input at all —
which are exactly the cases §3 and §4 spend most of their text specifying. A diagnostic that cannot
reproduce the semantics it is diagnosing is not a diagnostic. Two additive fields, both optional, no
existing field changed:

```go
RawQuery     string             `json:"raw_query,omitempty"`
HeaderValues []routeTestHeader  `json:"header_values,omitempty"`   // {name, value}
```

- **`raw_query`** is the query string *exactly* as it would appear after `?`, parsed with §4's rules.
  It carries repeated keys, percent-encoding, `+` and malformed escapes without JSON flattening any
  of them. It is **not** derived by splitting `path`: `path` is the path, `?` in it stays a literal,
  and today's callers keep working unchanged.
- **`header_values`** is an ordered list of `{name, value}` pairs, appended to whatever `headers`
  supplied, so a caller can express two `X-Tenant` field lines. `headers` is retained verbatim for
  compatibility and remains the convenient form for the single-value case.
- Both are optional and omitted-means-absent, so every existing Console call and every existing test
  payload is unaffected.

The result gains the matching explanation §14 requires: which candidates the path produced, which
predicate failed on each rejected candidate, and which location was selected. Freezing this here is
the point of the record — otherwise #145 would have to invent a public API shape, which is precisely
what an accepted ADR is supposed to make unnecessary.

### 15. Validation, lint and the difference between them

**Validation** (rejects the configuration) covers everything in §2, §3, §4, §8, §8a, §8b and §9
stated as an error: invalid tokens, empty `methods`, duplicates, missing or forbidden `value`,
uncompilable regexes, `Host` and pseudo-header predicates, `CONNECT`, a forwarded-header predicate
without a `trusted_proxies` precondition, invalid or protected header names, field values outside
RFC 9110 §5.5, any `Vary` operation on a cached location, any `Access-Control-*` operation on a
CORS-enabled location, non-normalized origins, `"*"` combined with credentials or with another
origin, `"*"` in the header and method lists, a sub-second/negative/over-24h `max_age`, and every
bound in §16.

**Lint** (accepts, warns) covers the configurations that are valid HTTP but probably not what the
operator meant:

- `SeverityError`: a predicate on `Forwarded`, `X-Forwarded-*` or an RFC 9440 name that passed the
  `trusted_proxies` precondition (§3).
- Warning: a hop-by-hop header predicate; `"null"` in a non-wildcard `allowed_origins`; `cors` on a
  native gRPC location; a response-header operation on `Content-Type` at a gRPC location; a
  `cors.enabled` location that also carries header predicates (§9); an `Access-Control-*` operation
  on a location where `cors.enabled = false` (§8b); an `add` on `Vary` at a location without the
  cache, noting it reaches downstream caches only (§8a).
- Warning: **unreachable routes, reported only where shadowing is provable.** The existing
  duplicate-match rule extends to predicates, conservatively. An earlier draft said "a later location
  is unreachable when an earlier location's predicate set is a subset of its own", which is not
  sound: `methods` is an OR-set, so a *subset* of methods shadows nothing, and regex implication is
  undecidable in general. A later location with the same `(type, path)` is reported unreachable only
  when **every** predicate of the earlier location provably subsumes the later one's, by one of these
  rules and no other:

  | Case | Provable when |
  | --- | --- |
  | Earlier has no predicates at all | always — it matches everything the later one could |
  | Structural equality | the two normalized predicate sets are identical **and their `preflight_widening` bits are equal** (this is the duplicate case) |
  | Methods | the earlier method set **contains** the later's, or the earlier omits `methods` entirely |
  | Header/query `present` vs anything | the earlier has `present` on name *N* and the later has any predicate on *N* |
  | Header/query `exact` | both are `exact` on the same name with byte-equal values |
  | Header regex | both are `regex` on the same name with byte-equal patterns |

  **`preflight_widening` (§14) participates in subsumption, not only in equality.** An earlier route
  with `preflight_widening = false` does not shadow a later one with `preflight_widening = true`,
  because the later route is still reachable for the preflight the earlier one rejects. Reporting it
  as a duplicate would tell an operator to delete a route that is the only thing answering their
  CORS preflight.

  Anything else — two different regexes, a regex against an exact, disjoint names — is **not
  reported**. A false "this route is unreachable" on a route that is in fact reachable is worse than
  silence: an operator who deletes it loses traffic. The rule is deliberately incomplete and says so.

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
| Header predicate `value` length | 1 KiB |
| Query predicate `name` / `value` length | 1 KiB |
| Query pairs parsed per request | 1024 |
| `response_headers` operations per location | 32 |
| `response_headers` — single `value` length | 4 KiB |
| `response_headers` — total bytes added to one response | 8 KiB |
| `cors.allowed_origins` | 64 |
| `cors.allowed_origins` — single entry length | 256 bytes |
| `cors.allowed_methods` | 16 |
| `cors.allowed_headers` | 64 |
| `cors.exposed_headers` | 64 |
| `cors.max_age` | 0 ≤ whole seconds ≤ 86400 |
| Generated CORS header set — total serialized bytes | 4 KiB |
| `Access-Control-Request-Headers` tokens honoured | 64 |

**These are conservative initial safety ceilings, not benchmark-derived capacity limits.** They were
chosen to bound configuration size, compilation cost and per-request work, not measured against a
workload — and this record says so rather than implying a rigour it does not have. Conservative is
the right starting point because the asymmetry is one-directional: raising a limit later is additive,
lowering an advertised one is breaking, and setting one too high initially can expose CPU, memory or
response-amplification problems in production rather than in review.

**#145 and #146 must benchmark the worst case at each maximum** — a location holding 16 header
predicates of which 8 are 512-byte regexes, 32 response-header operations totalling 8 KiB, and a CORS
policy at 64 origins / 64 allowed headers / 64 exposed headers — and record the numbers. If a ceiling
turns out to be far below what the implementation comfortably sustains, raise it then, with evidence.

Count limits and byte limits are both present on purpose: 64 `allowed_headers` of unbounded length
is not a bound on the response Jul generates, and the preflight-response size argument in §10 depends
on one existing.

All are checked before `Publish`, except the per-request query-pair cap and the
`Access-Control-Request-Headers` token cap, which bound request-time work. They exist so that a
pathological or adversarial configuration cannot make route selection or preflight evaluation
superlinear, and so that the cost of a request is bounded by a number an operator can read off their
own configuration.

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
| **A cache-variance contract** — declaring, *inside* the cache, that a stored representation varies by a request field (§8a) | an upstream that genuinely varies and cannot be made to send its own `Vary`; owned by the cache portfolio (#107), not by this record |
| **A durable external `route_id`** (§14) | ADR 0019 / #118 decides external resource identity; this record deliberately does not pre-empt it |
| `"*"` in `allowed_headers` / `exposed_headers` (§9) | a bounded design for Fetch's `Authorization` exception that does not require an operator to know about it |
| **WebSocket over HTTP/2 and HTTP/3** (RFC 8441 / RFC 9220 extended `CONNECT`) | see below |

**On RFC 8441/9220 specifically**, because "why not just support it" is the obvious question and the
answer is not "it is hard". It is feasible and it is currently not worth its cost:

- **Advertising it changes what browsers send, so a partial implementation is worse than none.** Go
  1.26's bundled HTTP/2 server implements extended `CONNECT` and then deliberately disables it,
  `disableExtendedConnectProtocol = true`, reachable only through the process-global
  `GODEBUG=http2xconnect=1`; there is no `Server` field. The reason Go gives is the important part,
  and it is not "our plumbing is unfinished":

  > *"Enabling extended CONNECT [...] causes browsers to attempt to use WebSockets-over-HTTP/2. This
  > results in problems when the server's websocket package doesn't support extended CONNECT."*

  Emitting `SETTINGS_ENABLE_CONNECT_PROTOCOL` is a promise. Browsers that see it **stop** sending
  HTTP/1.1 `Upgrade` and start sending extended `CONNECT`. Jul's entire WebSocket path is
  `Upgrade` + hijack, so advertising the setting before the whole path supports it would break
  WebSocket for exactly the clients that today work fine. This is not a flag to flip and then
  incrementally improve behind.
- **It buys almost no capability.** A browser that cannot use RFC 8441 opens an ordinary HTTP/1.1
  connection for the WebSocket. Nothing becomes possible that is impossible today; connection count
  and head-of-line behaviour improve. That is an optimization, not a gap.
- **The backend side is a real adapter, not a flag.** Inbound extended `CONNECT` proxied to an
  HTTP/1.1 backend means translating an HTTP/2 stream to an `Upgrade` + hijack and back, with
  flow-control and half-close semantics Jul would own. [ADR 0002](0002-protocol-adaptation.md)
  governs exactly this and requires it to be an explicit adapter. quic-go does support extended
  `CONNECT` for HTTP/3, so building h3 first without h2 would be the *inverse* of the useful order.
- **It would reopen the wrapper-composition defect class.** An extended `CONNECT` carries neither an
  `Upgrade` header nor `Connection: upgrade`, so `isUpgradeRequest` would not fire: the cache would
  see a `200` with an unbounded body and buffer it to `maxEntry`, compression would try to compress a
  WebSocket stream, and nothing would mark the stream as "not a representation". That is the same
  class as #326, #331 and #332, and adopting RFC 8441 without first revisiting all four wrappers
  would create a fourth instance.

> **Re-entry trigger — a state of the world, not a ticket.** Revisit when **Go exposes a supported,
> per-`Server` API for extended `CONNECT` and enables it by default**, and Jul's WebSocket path can
> serve an extended-`CONNECT` stream end to end.
>
> An earlier revision made the trigger "golang/go#71128 resolves", which was wrong twice over: that
> issue closed in January 2025, and it closed *by disabling* extended `CONNECT` — it is the issue
> that produced the current state, not one tracking its removal. A re-entry trigger has to be a
> condition someone can evaluate against the world, because a ticket can close in the direction
> opposite to the one the trigger assumed.
>
> When it fires, the decision reopens as an ADR 0002 protocol-adapter question, and the cache,
> compression, `Recorder` and `respwriter` upgrade/hijack assumptions are re-derived *before* any
> implementation, not after.

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
| **`GET` also matches `HEAD`; `cors.enabled` widens `methods` for preflights (§2)** | **One-way door** | directly observable routing behaviour | breaking; only escapable via an additive opt-out |
| Header name canonicalization; no comma splitting; unanchored regex (§3) | **One-way door** | changes which requests a deployed route matches | breaking |
| `present` includes present-empty (§3) | **One-way door** | same | breaking |
| Query parsing semantics; malformed pairs absent, never a 400 (§4) | **One-way door** | client-visible behaviour under malformed input | breaking |
| AND composition, OR only within a list (§5) | One-way door to *narrow*, two-way to *extend* | adding a bounded OR later is additive | low if additive |
| Tiered enumeration with fallthrough; path specificity over predicates (§6) | **One-way door** | the single most observable behaviour in the record | breaking; traffic moves |
| `/` folded into tier 2, first-declared wins (§6) | Expensive two-way door | affects only configurations lint already flags | small, documented |
| No automatic 405 (§7) | Two-way door **into** 405, one-way back out | adding 405 later is a behaviour change; removing it after shipping is worse | adding later is the cheap direction |
| Ordered operation list for response headers (§8) | **One-way door** | public schema and typed API | schema migration |
| Protected-header denylist, including §8a's `Vary` rule (§8) | Expensive two-way door | relaxing is additive, tightening breaks deployed configs | one-directional |
| CORS owns `Access-Control-*`; upstream copies stripped; generic ops excluded from generated preflights (§8b) | **One-way door** | security contract, and observable on the wire | breaking |
| Exact-only origins; `*`+credentials rejected; no `*` in header/method lists (§9) | One-way door to *narrow*, additive to extend | a bounded pattern grammar can be added compatibly | low if additive |
| **The wildcard is unconditional and suppresses `Vary: Origin` (§9)** | **One-way door** | downstream shared caches store what Jul emits | cache poisoning during any transition |
| CORS is not authorization; disallowed origins are still served (§9) | **One-way door** | security-model contract operators build on | breaking, and dangerous to change quietly |
| CORS headers on error responses (§9) | **One-way door** | observable, and browsers depend on it | breaking |
| `max_age` grammar and 24h ceiling (§9) | Two-way door upward | raising the ceiling is additive | low |
| Response policy outermost per-location (§10) | Expensive two-way door | which responses carry policy is observable | breaking for error-path headers |
| `1xx` passes through without applying policy (§10) | Two-way door | no sane alternative; the other behaviour is #331 | local |
| Preflight terminated outside `Auth`, guarded by its own rate and WAF passes (§10) | **One-way door** | durable security contract | tightening breaks credentialed CORS on authenticated routes |
| CORS defaults: no `allowed_origins` default, safelisted `allowed_methods`, `[]` rejected (§9) | **One-way door** | changes what a deployed minimal configuration does | breaking |
| HTTP semantic parity scoped to supported transport/action pairs (§12) | Two-way door | a description of existing capability, not a new constraint | none |
| Cache stores the origin representation; snapshot at commit (§11) | Two-way door in mechanism, **one-way in guarantee** | the *guarantee* is a security contract; the snapshot is private | mechanism: local refactor. Guarantee: not reversible |
| `match_ordinal` as a CAS-bound **selector**, never an identity (§14) | **One-way door** on the field, two-way on the policy | public API field; the CAS requirement can only be relaxed, not added, later | additive now |
| Internal scopes keyed by predicate fingerprint, including `preflight_widening` (§14) | **Internal two-way door, transient operational impact** | `authByScope`/`wafByScope` are rebuilt every generation, so nothing migrates; only rate-limit buckets are keyed across reloads, and orphaned ones are evicted by the store's idle TTL | changelog note; buckets refill within the TTL |
| Route-test `raw_query` + `header_values` (§14) | **One-way door** | public admin API fields | additive now; removing them later is breaking |
| WebSocket is HTTP/1.1 only; RFC 8441/9220 deferred (§12, §17) | Two-way door | adding extended `CONNECT` later is additive capability | new protocol adapter, plus re-deriving the wrapper assumptions |
| Location-scoped recover so a routed panic 500 carries policy (§10) | Expensive two-way door | observable: a cross-origin client can read a routed 500 | breaking for clients that came to rely on it |
| Every `Access-Control-Request-Headers` token must be listed; no safelist exemption (§9) | One-way door to *loosen* only | relaxing is additive; it is what makes the `allowed_methods` default safe | one-directional |
| Numeric and byte bounds (§16) | Two-way door upward, one-way downward | raising a limit is additive | low upward |
| Compiled matcher representation, package layout, predicate structs | **Two-way door** | entirely private | local refactor |
| Where the parsed query is memoized; the fingerprint's digest algorithm | **Two-way door** | private and derived | local refactor |

The three decisions that would cost the most to discover wrong in twelve months are **route
precedence (§6)**, **the cache guarantee (§11 together with §8a)** and **the CORS emission contract
(§9: the unconditional wildcard, the `Vary` split, and headers on errors)**. §6 is frozen as an
algorithm rather than prose for that reason; §11 is stated as a testable invariant rather than a
placement instruction, and §8a gives the rule that decides which layers may announce `Vary` at all;
§9 is a table rather than a paragraph because its earlier prose form was internally inconsistent and
nobody noticed until review.

## Security considerations

1. **Attacker-controlled forwarded headers cannot reappear.** This record adds no stage between
   `setCanonicalXForwarded` and `applyProxyHeaders` and touches neither. Response policy has no
   access to the outbound request; CORS writes no request header. The one new adjacency is that route
   *matching* reads `X-Forwarded-*` before sanitization, which §3 gates behind a declared
   `trusted_proxies` policy and still reports as a `SeverityError`.
2. **Header injection is impossible through configuration.** Any name that is not an RFC 9110 §5.1
   token, and any value carrying a byte outside RFC 9110 §5.5's `VCHAR / SP / HTAB / obs-text`, are
   rejected before `Publish` (§8) — not dropped at write time, where the operator would never learn.
3. **Framing cannot be mutated.** The `Content-Length`/`Transfer-Encoding`/`Connection`/`Upgrade`
   family is rejected, which also means request smuggling cannot be configured into existence and a
   WebSocket handshake cannot be broken by a header operation.
4. **Credentialed CORS cannot emit `*`.** Rejected at validation, not repaired at runtime (§9).
5. **A cached response cannot carry another origin's CORS grant.** §11 makes it unrepresentable
   rather than merely unlikely, including for entries rehydrated from disk under a configuration that
   no longer exists. The guarantee holds at the commit boundary today and unconditionally once #332
   lands; §11 states both forms rather than the flattering one.
6. **An operator cannot announce a variance Jul's own cache does not honour.** §8a rejects `Vary`
   operations on a cached location, which closes a cross-tenant leak in which Jul serves one tenant's
   stored body while truthfully telling downstream caches the response varies.
7. **Two CORS policies cannot compose into a broken one.** §8b strips upstream `Access-Control-*`
   fields before Jul emits its own, so a CORS-implementing upstream behind a CORS-configured route
   cannot produce the duplicate `Access-Control-Allow-Origin` that Fetch treats as a failure.
8. **The `Vary: Origin` contract has no gap.** Under any non-wildcard policy it is emitted even when
   no `Origin` is present, so a shared downstream cache cannot store the no-origin variant and replay
   it cross-origin. Under the wildcard it is omitted, which is sound *only* because §9 makes that
   policy unconditional — the two rules are load-bearing for each other and must not be changed
   independently.
9. **An approved preflight is still rate-limited and still passes the WAF.** §10's terminator decides
   approval first, then applies the location's own rate policy keyed by canonical client address and
   the location's already-compiled firewall, before emitting the 204. Authentication is the only
   layer skipped, and only because the Fetch standard makes authenticating a preflight impossible.
   A denied preflight is guarded by the ordinary chain instead, so neither shape reaches a
   Jul-generated response unguarded and nothing is evaluated twice.
10. **Route topology is not disclosed.** No 405, no `Allow`, no CORS-specific rejection status for a
    denied preflight (§7, §9).
11. **No request value becomes a metric label.** Predicate values, header values, query values and
    origins never appear in telemetry labels; this is the existing rule and it is not relaxed.

**Residual exposure, stated plainly:** an approved preflight skips per-location authentication, for
the reason in item 9. It skips nothing else.

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
- `match_ordinal` (§14) is a revision-relative selector that requires `base_version`, and is never
  presented as a resource identity. A durable `route_id` is ADR 0019's to decide;
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
  alone does not match GET; a `cors.enabled` route with `methods = ["GET"]` is selected for its own
  preflight, and a non-preflight `OPTIONS` to the same route is not.
- **§3 headers:** name case-insensitivity across HTTP/1.1, HTTP/2 and HTTP/3; absent vs present-empty;
  repeated field lines; comma-combined value not split; unanchored regex; regex compile failure fails
  the reload; `Host` rejected; pseudo-header rejected; an `X-Forwarded-For` predicate is **rejected**
  without a `trusted_proxies` policy on the listener and produces a `SeverityError` finding with one;
  all §16 bounds.
- **§4 query:** absent, `?x`, `?x=`, repeated, percent-encoded, `+`, malformed escape (deterministic,
  no 400), `;` not a separator, the 1024-pair bound, and parsed exactly once per request.
- **§5/§6 selection:** AND across kinds; fallthrough within the exact, prefix and regex tiers;
  descending prefix length then declaration order; path specificity beating predicates; `/` folded
  into tier 2 with first-declared winning; no map iteration anywhere in selection; order stable
  across reload; unreachable/duplicate lint.
- **§7:** method mismatch yields 404 and no `Allow` header.
- **§8 response headers:** add/set/remove and their ordering; multi-value; empty value vs omitted;
  every rejected name; a value carrying a C0 control or DEL rejected, not only CR/LF/NUL; the 8 KiB
  per-response byte bound.
- **§8a `Vary`:** any `Vary` operation on a location declaring `cache = true` is rejected — including
  when the global `[cache]` block is disabled, so the rejection does not depend on distant state;
  `add` is accepted without the cache; `set`/`remove` rejected either way; enabling `cache` on a
  location that already has a `Vary` operation fails the reload rather than silently changing
  behaviour; an upstream-supplied `Vary` still produces cache variants, which is the supported way to
  express real variance.
- **§8b ownership:** an `Access-Control-*` operation is rejected when `cors.enabled = true` and
  lint-warned when it is false; an upstream `Access-Control-Allow-Origin` is stripped and replaced,
  leaving exactly one field line; **an upstream `Vary: Origin` survives and still creates variants**;
  a generated preflight carries the CORS set and `Vary` and nothing a `response_headers` operation
  added; generic operations run before CORS.
- **§9 CORS:** exact, `null`, malformed, duplicate `Origin`, disallowed; `*`+credentials rejected;
  `*` combined with another origin rejected; `*` in the header/method lists rejected; non-normalized
  origin rejected; `max_age` sub-second, negative and over-24h rejected, `"0s"` emits `0`, omitted
  emits nothing; `Access-Control-Request-Method` repeated or comma-combined is not approved;
  requested-header casing and the 64-token bound; the generated-header byte bound; declaration order
  of the emitted lists is stable across reloads. **The wildcard table specifically:** under
  `["*"]`+no-credentials, `*` is emitted for an allowed origin, for `Origin: null`, and for a request
  with no `Origin` at all, and `Vary: Origin` is absent in all three; under every other policy
  `Vary: Origin` is present in all three. **Plus the §9 coupling test**, named as such: `Vary: Origin`
  is absent only when the emitted `Access-Control-Allow-Origin` is byte-identical across every
  request shape — so a future exception carved out of the wildcard fails at the point it is
  introduced. CORS headers present on 401, 403, 429 and 502.
- **§9 defaults:** `allowed_origins` omitted with `enabled = true` is rejected; an explicit `[]` is
  rejected on all four lists; empty `allowed_headers`/`exposed_headers` **omit** their response header
  rather than emitting it empty; `Access-Control-Request-Headers` is never reflected; a populated
  block with `enabled = false` validates fully, is inert, and lint-warns. **The `allowed_methods`
  default is safe only in combination with the header rule, so test the pair:** with an origins-only
  policy, a *simple* cross-origin `POST` succeeds and receives allow-origin; a **JSON `POST`
  preflight is denied** unless `Content-Type` is listed; an **`Authorization`-bearing `POST`
  preflight is denied** unless `Authorization` is listed. Also: a listed `Content-Type` in
  `Access-Control-Request-Headers` is **not** treated as safelisted.
- **§10 ordering:** policy applied to auth, rate-limit, WAF, **BodyLimit 413**, upstream-error and
  cache-hit responses; **and to the 500 from a panic after route selection** — a cross-origin request
  to a route whose action panics must receive a 500 carrying `Access-Control-Allow-Origin`, while a
  panic in the router or before selection still yields the global `Recover` 500 with no location
  policy, and a panic *after* the response commits changes neither status nor headers; not applied to
  the router 404, the HTTPS redirect or the ACME challenge handler; approved preflight reaches
  neither auth nor the action; denied preflight does; **an approved preflight is rate-limited and
  WAF-inspected by the terminator**, keys on the client address even when the policy says
  `jwt:<claim>`, and shares no bucket with the identity-aware limiter; a denied preflight is guarded
  by the ordinary chain and **not** by the terminator, so neither is evaluated twice; a location
  without a rate policy installs no rate guard.
- **§10 informational responses:** `1xx` passes through without applying policy and without latching;
  the final status reaches the client after one or more `103`s; an implicit `200` via `Write` with no
  explicit `WriteHeader` still gets policy applied exactly once; `ReaderFrom` and `Flusher` paths
  behave after an interim response; `101` is unchanged.
- **§11 cache — the invariant test:** a response stored under a location with a CORS policy and
  header operations contains none of them; a second request from a different origin receives its own
  correct headers; an entry rehydrated from disk carries no policy headers. **Plus the boundary the
  invariant actually has:** a hit must carry exactly one `X-Request-ID`, and it must be the current
  request's — that test belongs to #332 and fails today. (The regression test for the compression
  defect the same snapshot fixes landed with the snapshot itself in #327.)
- **§12 parity:** the cross-protocol matrix runs only *supported* transport/action pairs — predicates
  and policy on HTTP/1.1, h2c, HTTP/2 and HTTP/3; **WebSocket upgrade on HTTP/1.1 only**, with the
  wrapper inert after hijack and **no CORS headers on the 101**; native gRPC over HTTP/2 with
  trailers untouched; transcoding as an ordinary response. No test asserts CORS on a 101, WebSocket
  over HTTP/2 or HTTP/3, or predicates on an L4 `[[stream]]` route, because this record claims none
  of them.
- **§14 identity:** the fingerprint changes when `preflight_widening` flips, so two routes differing
  only in `cors.enabled` do **not** share an auth, WAF or rate-limit scope; it does not change when a
  same-path route is inserted above the target; it does change when the predicate set changes.
- **§14 route-test contract:** `raw_query` reproduces §4's semantics end to end — repeated keys,
  percent-encoding, `+`, malformed escapes — and a `?` inside `path` stays a literal; `header_values`
  expresses two field lines of the same name and composes with the legacy `headers` map; a payload
  using neither behaves exactly as today; the result names the rejected candidates and the failing
  predicate on each.
- **§15 lint:** each provable shadowing case is reported; **two different regexes on the same header,
  a regex against an exact, and disjoint predicate names are NOT reported** — a false unreachable
  warning costs an operator real traffic; **a route with `preflight_widening = true` is not reported
  as shadowed by an otherwise identical route with it false**, because the later route is the only
  one answering the preflight.
- **§13/§14 lifecycle and API:** invalid predicate or policy aborts the reload before `Publish`;
  order preserved across reload; typed patch round-trip including clear; `match_ordinal` resolution
  including the ambiguity rejection and the `409` when `base_version` is absent or stale; the
  predicate fingerprint is stable when a same-path route is inserted above the target and changes
  when the predicate set changes; route-test uses the router's selection.
- Fuzz the header and query predicate parsers and the origin normalizer. Race and E2E as usual.

## Consequences

1. Route selection becomes a candidate enumeration. This is the largest internal change and the one
   with the most observable blast radius; §6 exists so it is implemented rather than designed.
2. `cacheWriter` must snapshot headers at commit. That snapshot already shipped independently, in
   #327, which also fixed the reproduced, client-visible compression corruption bug tracked as #326;
   #146's response-policy wrapper builds on the same mechanism rather than introducing it.
3. `sr.fallback` disappears as a special case; duplicate `prefix "/"` locations change from
   last-declared to first-declared, aligning the router with the lint.
4. `AuthScope`, `WAFScope` and the rate-limit scope move to a canonical predicate fingerprint (§14).
   Rate-limit buckets reset once on upgrade.
5. The typed patch API gains `match_ordinal` and, with it, a required `base_version` for any patch
   that uses it.
6. `internal/admin/routetest.go` loses its private matcher and gains real method/header inputs.
7. The per-location chain gains three positions (§10): the response-policy wrapper, a **location
   recover** so a routed panic's 500 carries the policy, and the CORS preflight terminator carrying
   its own rate and WAF passes. The guards reuse the location's existing policy and compiled
   firewall; none of the three adds a configuration surface.
8. The lifecycle registry, `docs/config-lifecycle.yaml` and both generated mirrors grow entries; a
   new `cors` subsystem is added.
9. **#146 must not merge until #331 and #332 have landed**, or fixes them atomically in the same
   change. This is a hard gate, not a sequencing preference: #331 gives §10's informational-response
   rule one implementation to follow rather than a fourth divergent one, and #332 is what upgrades
   §11's invariant from the commit-boundary form to the unconditional one §8's and §9's reasoning is
   written against. Merging #146 first would ship CORS on a cache whose stated guarantee is not yet
   true.
10. `docs/configuration.md`, `docs/core-http.md`, `docs/cache.md`, `docs/compression.md`,
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

**Automatic 405 with `Allow`.** Rejected in §7: the header would assert a resource property the
gateway does not know, and computing it discloses route topology. The performance objection an
earlier draft also raised is withdrawn as incorrect.

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
disables an inner security layer. With §10's coarse guard restoring rate limiting without moving
anything, the residual risk it would remove no longer justifies that shape at all.

**Permitting `add` on `Vary`.** Rejected in §8a after review, having been permitted in the first
draft. Response policy runs outside the cache, so an operator-added `Vary` is invisible to Jul's own
store: the operator writes the correct-looking thing, Jul keeps serving one tenant's stored body, and
the header truthfully tells every downstream cache the response varies. The rule that replaced it —
an outer layer may announce only the variance it introduced — is what keeps compression's
`Accept-Encoding` and CORS's `Origin` legitimate while rejecting the operator case.

**Letting `response_headers` and `cors` both write `Access-Control-*`, resolved by ordering.**
Rejected in §8b: any ordering rule still permits a configuration in which the generic operations and
the CORS block disagree, and "whichever runs last wins" is not a security contract anyone should have
to reason about. Single ownership plus a validation error is smaller and has no undefined case.

**A wildcard origin that excludes `null`.** Rejected in §9: it is both internally inconsistent with
suppressing `Vary: Origin` and unenforceable, because Fetch's CORS check succeeds on a `*` grant
without comparing the origin at all. An exclusion Jul cannot enforce is worse than no exclusion,
because it misdescribes the policy in the operator's own configuration file.

**`match_ordinal` as durable identity, and ordinal-keyed internal scopes.** Rejected in §14 after
review. An ordinal is only meaningful within one configuration revision: inserting a same-path route
shifts every later ordinal, so an ordinal-keyed rate-limit bucket transfers its accumulated state to
a different predicate set, and a force-applied ordinal patch edits a route the operator never
previewed. Splitting the two roles — a CAS-bound selector for patches, a predicate fingerprint for
state — costs one derived value and removes both failure modes.

**Wildcard-subdomain or regex origins.** Rejected for this tranche: over-matching an origin is a
cross-origin data-disclosure bug, and the bounded exact list covers every use case currently
evidenced. Additive later, with a grammar, if a real deployment needs it.

**Skipping the WAF for approved preflights.** Rejected in §10 on the second review round, having been
proposed in the first. The justification offered — that a preflight has nothing a rule could inspect
— was simply false: a preflight carries a full request target, query string, every non-CORS request
header, the method and the client address, all of which Coraza's request-phase rules read without
ever needing a body. The rejected shape would have handed an attacker a request shape that reaches a
Jul-generated response having passed no rule at all.

**A denylist in `buildEntry` for Jul-owned response headers.** Rejected in §11 in favour of a
constructive boundary chosen by #332. `stored.Del("X-Cache")` already exists and nobody called it a
denylist, which is exactly how `X-Request-ID` joined the class unnoticed. This record does not
prescribe the replacement mechanism: it states the no-leak property it needs, and records that a
simple multiset difference delivers that property but not header fidelity, so #332 can choose
knowingly rather than inherit a claim.

**Claiming protocol parity across all transports and actions.** Withdrawn in §12. WebSocket upgrade
is unavailable over HTTP/3, native gRPC is end-to-end HTTP/2 by design, and the L4 stream proxy has
no HTTP message to match on at all. The honest claim — identical semantics wherever the action is
supported — is both true and testable; the blanket one would have generated a matrix of impossible
cases.

**Reporting unreachable routes on predicate-set subset.** Rejected in §15: `methods` is an OR-set, so
a subset shadows nothing, and regex implication is undecidable. A false unreachable warning costs an
operator traffic when they act on it, so the rule is restricted to provable cases and is deliberately
incomplete.

**Deriving `limit_except` and bare `add_header` translations in the importer.** Rejected in §18: both
change semantics silently, which is worse than an explicit unsupported finding.

## Related

- #117 — this record's decision issue
- #145 — `[ROUTE-01]` request matching; implements §1–§7, §13–§16
- #146 — `[ROUTE-02]` response policy and CORS; implements §8–§12, §15, §16
- #147 — `[ROUTE-03]` typed API, Console, importer, E2E; also gated on ADR 0019
- #326 — the pre-existing cache × compression corruption §11 also fixes; already resolved by #327
- #331 — `1xx` makes every response-writer wrapper drop the final status; §10's informational-response
  rule exists so this record does not add a fourth
- #332 — a cache hit replays a stale `X-Request-ID`; §11's invariant is written against the boundary
  this defect exposes, and is upgraded when it lands
- #108 — Core Gateway Completeness epic; `docs/specs/core-gateway-completeness.md` §7 (D12)
- [ADR 0016](0016-inbound-identity-and-backend-peer-trust.md) §12 — identity asserted to the backend
- [ADR 0011](0011-reload-plan.md) — reload transaction and the closed-world lifecycle registry
- [ADR 0014](0014-operability-surfaces.md) — one backend implementation behind every surface
