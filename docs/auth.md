# Authentication & access control

> **Maturity:** GA (soak completed; see [ADR 0003](adr/0003-maturity-and-ga.md)).
> Mutual TLS (client certificates) is a separate feature — see [mtls.md](mtls.md).

Auth is a per-location **modifier**, not an action: it composes with any
location (static, proxy, FastCGI, …). Each location applies a fixed pipeline —
a **CIDR** network gate first, then **exactly one** credential method (HTTP
**Basic**, **JWT** bearer tokens, or **forward-auth** to an external service).
An empty `auth` block authorizes every request.

## Quick start

```toml
[[servers.locations]]
match      = { type = "prefix", path = "/admin" }
proxy_pass = "http://127.0.0.1:3000"

  [servers.locations.auth]
  allow = ["10.0.0.0/8"]      # CIDR gate (evaluated first)
  deny  = ["10.9.0.0/16"]     # deny wins over allow

    [servers.locations.auth.basic]
    file  = "/etc/jul/htpasswd"   # bcrypt entries only
    realm = "Admin Area"

[[servers.locations]]
match      = { type = "prefix", path = "/api" }
proxy_pass = "http://127.0.0.1:3000"

  [servers.locations.auth.jwt]
  jwks_url   = "https://issuer.example/.well-known/jwks.json"
  issuer     = "https://issuer.example/"
  audience   = "https://api.example/"
  algorithms = ["RS256", "ES256"]   # default: asymmetric set; "none" always rejected
```

## Pipeline & precedence

1. **CIDR gate** — `deny` is checked first (deny wins); then, if `allow` is
   non-empty, the client must fall inside one range. The client IP is the
   **canonical client address**: the transport peer unless the listener's
   [`client_address`](configuration.md#client-address-and-trusted-proxies)
   policy explicitly trusts that peer as a proxy. A forwarding header from an
   untrusted sender is never consulted, so it cannot be spoofed; with no policy
   configured the gate sees exactly what it always did.
2. **Credential method** — at most one of `basic`, `jwt`, `forward_auth`. They
   are independent (no AND-chaining of two credential methods).

Within the middleware chain, auth runs **before rate limiting**, so a
`key = "jwt:<claim>"` rate limit can key on a validated token claim (e.g.
`jwt:sub` for per-user limits).

## Configuration reference

### `[servers.locations.auth]`

| Key | Type | Default | Notes |
| --- | --- | --- | --- |
| `allow` | `[]string` | — | CIDR ranges permitted through the gate |
| `deny` | `[]string` | — | CIDR ranges rejected (wins over `allow`) |
| `basic` | table | — | HTTP Basic; mutually exclusive with `jwt`/`forward_auth` |
| `jwt` | table | — | JWT bearer validation |
| `forward_auth` | table | — | delegate to an external endpoint |

### `[servers.locations.auth.basic]`

| Key | Type | Default | Notes |
| --- | --- | --- | --- |
| `file` | string | — | path to an htpasswd file (**bcrypt** entries only) |
| `realm` | string | `Restricted` | challenge realm |

### `[servers.locations.auth.jwt]`

| Key | Type | Default | Notes |
| --- | --- | --- | --- |
| `jwks_url` | string | — | **HTTPS** JWKS endpoint (validated at config load) |
| `issuer` | string | — | when set, must equal the token `iss` |
| `audience` | string | — | when set, must be present in the token `aud` |
| `algorithms` | `[]string` | RS/ES/PS 256·384·512 | accepted signing algorithms; `none` always rejected |

### `[servers.locations.auth.forward_auth]`

| Key | Type | Default | Notes |
| --- | --- | --- | --- |
| `url` | string | — | endpoint receiving the auth subrequest |
| `auth_response_headers` | `[]string` | — | response headers copied onto the upstream request on allow |

## Behaviour matrix

| Method | Credential source | Success | Failure | Identity propagated |
| --- | --- | --- | --- | --- |
| CIDR | canonical client address | in `allow`, not in `deny` | **403** | — (gate only) |
| Basic | `Authorization: Basic` | user in htpasswd + bcrypt match | **401** + `WWW-Authenticate: Basic realm="…"` | — |
| JWT | `Authorization: Bearer` | valid signature + claims | **401** + `WWW-Authenticate: Bearer error="invalid_token"` | claims → request context (`ClaimsFrom`) |
| Forward-auth | subrequest to `url` | endpoint returns **2xx** | endpoint's status relayed (non-error → 403); **503** if unreachable | `auth_response_headers` → upstream request |

## Schemes

### CIDR gate

Parsed once at reload. `deny` evaluated before `allow`; an empty `allow` admits
any non-denied client. Unidentifiable client addresses are rejected whenever an
`allow` list is configured.

The gate matches the canonical client, so behind a declared trusted proxy the
range describes the **end client**, not the proxy. To express "only accept
connections arriving from my proxy network", that is the `trusted_proxies`
policy plus a network-level control — not an end-client CIDR range. Without a
`client_address` policy the canonical client is the transport peer, so nothing
changes for a directly exposed server.

### HTTP Basic

- htpasswd is loaded at reload; **only** bcrypt entries (`$2a$`/`$2b$`/`$2y$`)
  are accepted — any other scheme fails fast with a clear error.
- Password comparison uses `bcrypt.CompareHashAndPassword` (constant-time).
- An **unknown username** still runs a bcrypt comparison against a fixed dummy
  hash, so response timing does not reveal whether a username exists.

### JWT / JWKS

- Tokens are parsed with `golang-jwt/v5` under an **algorithm allow-list**
  (`WithValidMethods`); `none` is always rejected.
- `keyFunc` additionally type-checks that the token's algorithm family matches
  the resolved key type (RSA method ⇒ RSA key, ECDSA method ⇒ EC key),
  closing **algorithm-confusion** attacks.
- Claims: `exp` is **required**; `exp`/`nbf`/`iat` honoured with a 60s leeway
  for clock skew; `iss`/`aud` validated when configured.
- **JWKS cache** (`jwks_url`): keys fetched over HTTPS and cached.
  - Fresh window 15 min; on an unknown `kid` a refresh is attempted.
  - Network fetches are throttled to **at most one per 30s**, so a flood of
    tokens bearing unknown key ids cannot amplify into a storm of JWKS requests.
  - On fetch failure, cached keys are served for a **1h stale grace** window.
  - Response body capped at **1 MiB**; client timeout `timeout` (default **10s**).

### Forward-auth

A `GET` subrequest is sent to `url` carrying the original headers (hop-by-hop
stripped) plus `X-Forwarded-Method`, `X-Forwarded-Uri`, and `X-Forwarded-Host`.
A **2xx** authorizes the request; the listed `auth_response_headers` are copied
onto the upstream request (client-supplied copies are stripped first). Any other
status is relayed to the client (non-error statuses normalized to 403; body
capped at 64 KiB); redirects from the auth service are passed through. One
subrequest is bounded by `timeout` (default **10s**).

### Dependency resilience

Both `forward_auth.url` and `jwt.jwks_url` are on the request path of every
authenticated request, so an unbounded number of subrequests to a struggling
auth service is the same amplification Jul bounds everywhere else. Both now
resolve through the shared upstream primitives:

- if the URL's host **names a configured `[[upstreams]]`**, that pool is used, so
  an auth service can be replicated and load-balanced like any other backend;
- otherwise the host becomes a **pool of one**, which changes no behaviour but
  brings the dependency under the same admission, passive health and retry
  accounting;
- `timeout` replaces what used to be a hardcoded 10 seconds.

> [!IMPORTANT]
> **Every failure denies.** An admission rejection, an exhausted retry budget, a
> pool with no available backend and — when the circuit breaker lands — an open
> circuit all deny the request with `503`. A request is **never** allowed through
> unauthenticated because a resilience control fired.
>
> "Fail open on dependency failure" is a defensible-sounding default in other
> systems and is a critical vulnerability here, so it is asserted by test rather
> than assumed. The shape helps: every one of those conditions reaches the caller
> as an error, and the caller denies on error, so the property is a consequence
> of the structure rather than a rule each call site has to remember.

#### Scope: transport only

Resilience here covers **transport and TLS failures only**. An HTTP response is
an answer, whatever its status:

| Outcome | Retried? | Trips passive health? | Client sees |
| --- | --- | --- | --- |
| Connection refused, reset, timeout | yes | yes | `503` if no replica answers |
| `2xx` | no | no | request allowed |
| `401` / `403` | no | no | the auth service's own response |
| `500` forever | **no** | **no** | the auth service's own `500` |

An auth service returning `500` forever is a received response, so it neither
fails over nor removes a healthy replica from rotation. The two dependencies do
not even agree on what failure means — JWKS treats any non-`200` as a refresh
failure, while forward-auth treats a non-2xx as a legitimate application result,
since `401` and `403` *are* the answer. Making `5xx` or `429` trip a breaker
needs a generic outcome-classification seam, which is one deferred extension
with two consumers rather than an auth special case.

A named upstream with several backends means the subrequest is sent to a
selected backend's address. For an `https` auth service that is the address TLS
verifies against, exactly as for a proxied backend.

## Reload & resource lifecycle

Authenticators are **rebuilt from scratch on every reload**: the server
reconstructs one `*Authenticator` per location, atomically swaps in the new set,
and drops the previous generation. No explicit teardown is required because an
authenticator owns **no background worker, timer, or long-lived socket** — the
CIDR gate and htpasswd set are pure in-memory state, and the JWKS cache refreshes
**lazily on the request path** (throttled to ≤1 fetch / 30s), never from a
background goroutine. Superseded authenticators are therefore simply
garbage-collected.

This rebuild-and-drop model is validated at runtime by `TestReloadChurnNoLeak`
(`internal/auth/reload_churn_test.go`), which drives sustained reload churn
across all permutations and asserts the goroutine count and post-GC heap return
to their pre-churn baseline. A 3,000-cycle run holds the goroutine count exactly
flat for every method (env-tunable via `AUTH_CHURN_ITERS`).

## Metrics

| Metric | Labels |
| --- | --- |
| `jul_auth_decisions_total` | `method` (`cidr`/`basic`/`jwt`/`forward`), `result` (`allow`/`deny`) |

## Benchmarks

Windows, amd64, Virtual CPU @ 3.41GHz (indicative — run `go test -bench` on your
own hardware).

| Benchmark | Time/op | Allocs/op |
| --- | --- | --- |
| `BenchmarkBasicVerify` (bcrypt cost 10) | ~105 ms | 13 (5.3 KB) |
| `BenchmarkJWTValidate` (RS256, pre-warmed JWKS) | ~67 µs | 65 (4.0 KB) |

Basic auth is **deliberately** expensive: bcrypt's work factor is the
brute-force defence, so ~100 ms per verification is by design (use a CIDR gate
or JWT for high-RPS paths). JWT verification is asymmetric-signature bound and
allocation-modest with a warm JWKS cache.

Reproduce:

```
go test -run '^$' -bench 'BenchmarkBasicVerify|BenchmarkJWTValidate' -benchmem ./internal/auth/
```

## Security / threat notes

| Threat | Status | Mechanism |
| --- | --- | --- |
| Algorithm confusion (`alg` swap) | 🟢 safe | allow-list + key/method type check; `none` rejected |
| Username enumeration (timing) | 🟢 safe | constant-time bcrypt + dummy-hash on unknown user |
| Client IP spoofing of CIDR gate | 🟢 safe | gate uses `RemoteAddr`, not `X-Forwarded-For` |
| JWKS SSRF | 🟢 safe by design | `jwks_url` is operator config and must be HTTPS; never request-influenced |
| JWKS fetch amplification | 🟢 mitigated | unknown-kid floods throttled to ≤1 fetch / 30s; 1 MiB body cap |
| Header spoofing (forward-auth) | 🟢 safe | client copies of `auth_response_headers` stripped before the endpoint's values are applied |
| Token leakage in logs | 🟢 safe | tokens are never logged (only "jwt validation failed") |
| Forward-auth response flooding | 🟢 mitigated | response body capped at 64 KiB |

**Defense-in-depth (egress allow-list).** `jwks_url` and the forward-auth `url`
are URLs the server dereferences on its own. When the optional
[`[egress]`](egress.md) allow-list is enabled, both fetches are additionally
constrained to an operator-approved set of hosts/CIDRs and refused at dial time
otherwise (subsystem `auth`), bounding the blast radius of a mistyped or
compromised config. It is disabled by default.

## Limits

- **At most one credential method per location** (no Basic **AND** JWT).
- **No OAuth2 / OIDC flows**, no opaque-token introspection, no cookie sessions,
  no refresh tokens.
- **No RBAC / scope enforcement** — claims are exposed downstream; enforcement is
  the upstream's job.
- **One issuer/audience per location** (no multi-issuer).
- Only standard claims (`iss`/`aud`/`exp`/`nbf`/`iat`) are validated; no custom
  claim assertions.
- Forward-auth is a **probe** (GET, no body), suited to services like Traefik
  ForwardAuth / NGINX `auth_request` — not a full OAuth2 exchange.

## GA status

Per [ADR 0003](adr/0003-maturity-and-ga.md), auth is **GA**: the
soak test (criterion 5) was completed on 2026-07-04.

| # | GA criterion | Status |
| --- | --- | --- |
| 1 | Behaviour matrix published | ✅ [behaviour matrix](#behaviour-matrix) |
| 2 | Published benchmark numbers | ✅ [Benchmarks](#benchmarks) (`BenchmarkBasicVerify`, `BenchmarkJWTValidate`) |
| 3 | Documented known-limitations | ✅ [Limits](#limits) |
| 4 | Stable config/API contract (semver-guarded) | ✅ [compatibility policy](compatibility.md) (v1 tag at release) |
| 5 | Long-running soak test passed | ✅ soaked 1h windows 2026-07-04 (929K req, 0% err) — [evidence](soak-evidence.md#2026-07-04--auth-soak-local-windows-1-hour-50-workers) |
| 6 | Runnable example + docs | ✅ [testdata/auth.toml](../testdata/auth.toml), [examples/jwt-auth](../examples/jwt-auth) + this doc |
| 7 | Security / threat note | ✅ [Security / threat notes](#security--threat-notes) |
| 8 | Fuzzing where parsing is involved | ✅ `FuzzParseJWKS` (JWKS doc), `FuzzValidateToken` (token path) |
| 9 | Self-explanatory Console surface | ✅ Console **Status** → *Security* group reports *Access control (auth)* with a per-location count |

All GA criteria are satisfied.

## Build tags

Auth is **core** — no build tag. All schemes (CIDR, Basic, JWT/JWKS,
forward-auth) are always compiled in.

## See also

- [mtls.md](mtls.md) — mutual TLS / client-certificate authentication
- [core-http.md](core-http.md) — the request stack auth modifies
- [compatibility.md](compatibility.md) — config/API stability policy
- [ga-push.md](ga-push.md) — GA hardening tracking log
