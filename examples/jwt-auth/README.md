# JWT-authenticated API gateway

Protect an API behind JSON Web Token (JWT) bearer authentication. Jul validates
each token's signature against the issuer's **JWKS** endpoint, checks the
issuer/audience/expiry, and forwards only authenticated requests to the upstream.
Validated claims also drive a **per-user** rate limit.

Authentication is a **core feature** — no build tag is required.

| Setting | Value |
| --- | --- |
| Public front | `http://localhost:8080/` (static) |
| Protected API | `http://localhost:8080/api` (requires `Authorization: Bearer <token>`) |
| Token validation | JWKS at `jwks_url`, asymmetric algorithms only (`none`/`HS*` rejected) |
| Rate limit | `20 req/s` (burst `40`) **per `sub` claim** |

## How it works

1. A request to `/api` must carry `Authorization: Bearer <jwt>`.
2. Jul reads the token's `kid` header and looks up the matching public key from
   the issuer's JWKS document (fetched once and cached, refreshed periodically,
   with a stale-grace window if the endpoint is briefly unreachable).
3. The signature is verified with an **allow-listed** asymmetric algorithm; the
   `iss`, `aud`, and `exp` claims are validated (with small clock-skew leeway).
4. On success the request proceeds to the upstream and the decoded claims are
   placed in the request context — the `jwt:sub` rate-limit key uses them to
   give each user their own bucket.
5. On failure Jul returns `401 Unauthorized` with a `WWW-Authenticate: Bearer`
   header; the upstream is never reached.

## Run

1. Point `jwks_url` (and `issuer`/`audience`) in [`jul.toml`](jul.toml) at your
   identity provider — Auth0, Keycloak, Okta, AWS Cognito, Google, etc. all
   publish a JWKS document, usually at `/.well-known/jwks.json`.

2. Validate the config:

   ```bash
   ../../jul -check -config jul.toml
   ```

3. Start an upstream on `127.0.0.1:3000` (any app), create a `public/` folder
   with an `index.html`, then start Jul:

   ```bash
   ../../jul -config jul.toml
   ```

4. Call the API with a token obtained from your issuer:

   ```bash
   # No token -> 401
   curl -i http://localhost:8080/api/

   # With a valid token -> proxied to the upstream
   curl -i -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/
   ```

## Notes

- **Algorithms.** `algorithms` defaults to the asymmetric set
  (`RS256/384/512`, `ES256/384/512`, `PS256/384/512`). Symmetric `HS*` and the
  unsigned `none` algorithm are always rejected to prevent token forgery and
  algorithm-confusion attacks. Pin the list to exactly what your issuer uses.
- **HTTPS JWKS.** `jwks_url` must be `https://` so the key material is fetched
  over an authenticated channel.
- **Per-user limiting.** Because authentication runs *before* rate limiting, the
  `jwt:sub` key sees the validated `sub` claim. Swap `sub` for any claim your
  tokens carry (e.g. a tenant or API-key id).
- **Observability.** Every decision is counted in
  `jul_auth_decisions_total{method="jwt",result="allow|deny"}`.

## Other credential methods

The same `[servers.locations.auth]` block also supports:

- **HTTP Basic** against a bcrypt `htpasswd` file:

  ```toml
  [servers.locations.auth.basic]
  file = "/etc/jul/htpasswd"   # htpasswd -B -c /etc/jul/htpasswd alice
  realm = "Restricted"
  ```

- **Forward-auth** to an external service (e.g. oauth2-proxy, Authelia):

  ```toml
  [servers.locations.auth.forward_auth]
  url = "http://127.0.0.1:4181/auth"
  auth_response_headers = ["X-Auth-User", "X-Auth-Groups"]
  ```

- A **CIDR** allow/deny gate that runs before any of them (deny wins):

  ```toml
  [servers.locations.auth]
  allow = ["10.0.0.0/8"]
  deny  = ["10.9.0.0/16"]
  ```

Only one credential method may be set per location; combine it with a CIDR gate
as needed. See the [configuration reference](../../README.md#serverslocationsauth).
