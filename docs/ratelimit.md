# Rate limiting

Jul.IA applies a **token-bucket** rate limit to every request, keyed by client
identity.  The policy can be set globally and overridden per-location; a
separate **per-listener connection cap** protects against slowloris-style
connection exhaustion.

This is **Y1-03**, in **core** — no build tag.

> **Maturity:** **GA** (see [ADR 0003](adr/0003-maturity-and-ga.md)).

## Contents

- [Policy scope](#policy-scope)
- [Key strategies](#key-strategies)
- [Algorithm](#algorithm)
- [Connection cap](#connection-cap)
- [Config reference](#config-reference)
- [Structured sparse updates](#structured-sparse-updates)
- [Benchmarks](#benchmarks)
- [Security / threat note](#security--threat-note)
- [GA status](#ga-status)

## Policy scope

A global `[rate_limit]` block sets the default for every location.  Each
location can override rate, burst, and key independently:

```toml
[rate_limit]
enabled = true
key = "ip"
rate = 100
burst = 200
max_conns = 1024

[[servers]]
listen = "127.0.0.1:8080"

  [[servers.locations]]
  match = { type = "prefix", path = "/api" }
  proxy_pass = "http://127.0.0.1:3000"

    [servers.locations.rate_limit]
    enabled = true
    key = "header:X-Api-Key"
    rate = 10
    burst = 20
```

Rules:

| Aspect | Global default | Per-location override |
| --- | --- | --- |
| `enabled` | inherited | yes |
| `key` | inherited | yes |
| `rate` | inherited | yes |
| `burst` | inherited | yes |
| `max_conns` | inherited | **no** (listener-global) |

A disabled override (`enabled = false`) turns rate limiting off for that
location even when the global block is on.

## Key strategies

The `key` field selects how requests are bucketed:

| Key spec | Identity source | Fallback | Trust note |
| --- | --- | --- | --- |
| `"ip"` (or `""`) | Client IP (`RemoteAddr`) | — | Always the transport peer; never uses `X-Forwarded-For` |
| `"header:<Name>"` | Value of header `<Name>` | Client IP | Header is untrusted; use only behind a verified proxy |
| `"jwt:<claim>"` | String claim from JWT context | Client IP | Requires auth middleware upstream; non-string claim falls back to IP |

### Behaviour matrix

| Scenario | Expected behaviour | Test coverage |
| --- | --- | --- |
| Request within burst | Allowed (200) | ✅ `TestRateLimiterAllowsBurstThenThrottles` |
| Request beyond burst | Rejected (429) with `Retry-After` | ✅ `TestRateLimiterAllowsBurstThenThrottles` |
| Distinct keys | Independent buckets | ✅ `TestRateLimiterKeysAreIndependent` |
| Distinct scopes | Same key, separate buckets | ✅ `TestRateLimiterScopesDoNotCollide` |
| Config reload with new rate/burst | Existing bucket updated in place | ✅ `TestRateLimiterReloadUpdatesBucketParams` |
| Idle bucket TTL expiry | Bucket evicted, memory bounded | ✅ `TestRateLimiterEviction` |
| IP key extraction | Strips port, uses transport peer | ✅ `TestRateKeyFuncIP` |
| Header key with missing header | Falls back to client IP | ✅ `TestRateKeyFuncHeader` |
| JWT key with no auth context | Falls back to client IP | ✅ `TestRateKeyFuncJWTFallsBackToIP` |
| JWT key with string claim | Uses claim value | ✅ `TestRateKeyFuncJWTReadsClaim` |
| JWT key with non-string claim | Falls back to client IP | ✅ `TestRateKeyFuncJWTReadsClaim` |
| Middleware 429 | Sets `Retry-After` header; calls `onLimited` | ✅ `TestRateLimitMiddleware429WithRetryAfter` |
| Connection cap exceeded | Second connection accepted but not served until slot free | ✅ `TestServerConnectionCap` |

## Algorithm

Request admission uses a **token bucket** (`golang.org/x/time/rate`):

- `rate` tokens are added per second, up to `burst` tokens.
- A request consumes one token; if none are available it is rejected.
- `Retry-After` is computed from the reservation delay and rounded up to whole
  seconds.

The bucket store is **sharded** (32 lock stripes) so concurrent requests for
different keys contend on independent mutexes.  Buckets are created lazily and
evicted by a background janitor after an idle TTL (default 10 min), so memory
stays bounded under churny key spaces such as per-IP limiting.

## Connection cap

`max_conns` caps concurrent TCP connections per listener independently of the
rate limiter.  When the cap is reached, new connections are accepted by the
kernel but not served until a slot frees.  Because the cap is listener-global,
it cannot be overridden per-location (validation rejects that).

Use cases:

- Prevent slowloris / connection exhaustion attacks
- Protect upstream capacity from being swamped by long-lived connections
- Complement the rate limiter (which only counts requests, not open sockets)

## Config reference

```toml
[rate_limit]
enabled   = true        # master switch
rate      = 100         # sustained requests/second per key
burst     = 200         # maximum burst (must be >= rate)
key       = "ip"        # "ip" | "header:<Name>" | "jwt:<claim>"
max_conns = 1024        # concurrent connections per listener (0 = unlimited)

# Per-location override (rate/burst/key only)
[servers.locations.rate_limit]
enabled = true
key     = "header:X-Api-Key"
rate    = 10
burst   = 20
```

Validation rules:

- `rate` > 0 when `enabled = true`
- `burst` ≥ `rate`
- `max_conns` ≥ 0; not allowed on per-location override
- `key` must match `ip`, `header:<Name>`, or `jwt:<claim>`

## Structured sparse updates

`rate_limit_global_set` sparsely updates the global policy. Omitted fields are
preserved. Explicit `enabled: false` retains dormant key/rate/burst settings;
`burst: 0` resets through the canonical rate/default behavior; and
`max_conns: 0` means unlimited. Enabling requires a positive effective rate,
and all supplied values are validated against a copy before assignment.
Changing rate or burst does not replace the `RateLimiterStore`; existing bucket
state is updated in place on subsequent use. Changing the key naturally selects
a new bucket-key space, while old idle buckets expire through the existing
janitor.

The public structured wire key remains `rate_limit` for both route and global
operations. The `op` discriminator is authoritative: `route_set_rate_limit`
keeps its established complete-replacement/default behavior and rejects
`max_conns`; `rate_limit_global_set` is sparse and accepts the listener-global
field.

`enabled`, `key`, `rate`, and `burst` are hot. `max_conns` is listener-bound: a
change requires planned restart if any currently bound desired address is
retained, including a candidate that adds a new listener while retaining an old
one. It may apply live only when every affected desired listener is newly bound
in the same complete candidate. Mixed hot and listener-bound changes stage the
whole candidate. Summaries contain field names only. The current Traffic
Controls form migrates to this operation in #81.

## Benchmarks

From `go test ./internal/middleware/ -bench=. -benchmem` on a modest VM:

| Benchmark | ops/sec | ns/op | allocs/op |
| --- | --- | --- | --- |
| `RateLimiterAllow` (hot path, existing bucket) | ~4 M | ~300 ns | 1 (reservation) |
| `RateLimiterAllowParallel` (distinct keys, 12 workers) | ~9 M | ~170 ns | 1 |
| `RateLimiterAllowParallelContention` (same key, 12 workers) | ~2 M | ~600 ns | 1 |
| `RateLimitMiddleware` (full request path) | ~300 K | ~6.8 μs | 14 (mostly `httptest`) |

The critical insight: **a single Allow call is ~300 ns with one allocation**.
At 10 000 req/s per key the overhead is well under 1 % of request lifecycle.

## Security / threat note

| Threat | Risk | Mitigation |
| --- | --- | --- |
| **IP spoofing via `X-Forwarded-For`** | Attacker bypasses per-IP limiting by faking the forwarded header | The IP key always uses the **transport peer** (`RemoteAddr`); `X-Forwarded-For` is never consulted implicitly.  Place a trusted proxy in front and use `header:X-Real-Ip` if you need client-origin limiting |
| **Key collision across scopes** | A per-location limiter accidentally shares buckets with the global limiter | `Scoped` namespaces keys with a null-terminated prefix; distinct scopes never collide |
| **Memory exhaustion from churny keys** | An attacker rotates keys (e.g. spoofed headers, IPv6 rotation) to create unlimited buckets | Buckets are **evicted after idle TTL** (default 10 min); janitor runs every minute |
| **Slowloris / connection exhaustion** | Attacker opens many idle connections without sending requests | `max_conns` caps concurrent connections per listener; excess connections wait |
| **Header-key bypass** | Attacker omits the header and falls back to IP, sharing the IP bucket | This is correct fallback behaviour; do not rely on header-key limiting for security isolation — pair with auth |
| **JWT-key bypass without auth** | Attacker sends no JWT and falls back to IP bucket | JWT key requires the auth middleware to run first; if auth is not configured the limiter degrades to IP |
| **Rate limiter as DoS amplifier** | Attacker crafts requests that are cheap to send but expensive to rate-limit | Allow is O(1) per request; sharding keeps contention low |
| **Config reload drops limit state** | Reload replaces policy and resets all counters | Existing buckets are **updated in place** (`SetLimit`/`SetBurst`); accumulated tokens are preserved |

## GA status

Per [ADR 0003](adr/0003-maturity-and-ga.md), rate limiting is **GA**:
the soak test (criterion 5) was completed on 2026-07-04.

| # | GA criterion | Status |
| --- | --- | --- |
| 1 | Behaviour matrix published | ✅ [Key strategies + Behaviour matrix](#key-strategies) |
| 2 | Published benchmark numbers | ✅ [Benchmarks](#benchmarks) |
| 3 | Documented known-limitations | ✅ Algorithm (token bucket), eviction TTL, max_conns scope |
| 4 | Stable config/API contract (semver-guarded) | ✅ `RateLimitConfig` frozen under [compatibility policy](compatibility.md) |
| 5 | Long-running soak test passed | ✅ soaked 1h windows 2026-07-04 (12.5M req, 0% err) — [evidence](soak-evidence.md#2026-07-04--rate-limit-soak-local-windows-1-hour-50-workers) |
| 6 | Runnable example + docs | ✅ [testdata/ratelimit.toml](../testdata/ratelimit.toml) + this doc |
| 7 | Security / threat note | ✅ [Security / threat note](#security--threat-note) |
| 8 | Fuzzing where parsing is involved | n/a — uses config parser (Y1-08), no custom parser |
| 9 | Self-explanatory Console surface | ✅ Console **Status** reports rate-limited request count |
