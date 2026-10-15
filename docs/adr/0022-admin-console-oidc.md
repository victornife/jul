# ADR 0022 — External Console authentication through local RBAC

- **Status:** Accepted for the maintainer-selected #441 implementation; Beta, unreleased
- **Date:** 2026-10-15
- **Related:** [ADR 0010](0010-console-rbac.md), [ADR 0012](0012-oss-open-core-boundary.md), [ADR 0013](0013-project-operating-model-and-completeness.md), [operator guide](../admin-oidc.md)

## Decision and trust boundary

One configured provider authenticates interactive administrators through discovery,
Authorization Code and mandatory PKCE S256. Existing Jul RBAC is the sole permission
authority. Exact `(issuer, sub)` mappings select a configured principal/role;
otherwise a bounded flat group claim selects one role and an issuer/subject
fingerprint supplies the per-person audit principal. Email, email domain and
provider permissions never grant access. Subject rules take precedence. Conflicting
roles, missing/oversized/malformed group claims and Entra overage deny group admission.
No directory, JIT provisioning or new permission catalogue is introduced.

OIDC authenticates Console browser API requests. Automation keeps scoped local Jul
tokens. Provider access tokens have different audiences and formats and are never
accepted as Jul API credentials. SAML, SCIM, device/implicit/password grants,
refresh tokens, multiple providers, invitation/audience management and application
sharing are outside this tranche. #579/#588 retain their independent contracts.

## Protocol and configuration

`[admin.oidc]` is additive, disabled by default and requires enabled RBAC and an
existing local administrator recovery credential. Its exact fields and bounds
are generated from configuration/lifecycle authorities. Production callbacks and
provider endpoints require HTTPS. HTTP is confined to literal loopback/localhost;
a cleartext callback also requires a loopback admin listener. Jul uses the
configured callback exactly, never forwarded headers or a user-supplied return URL.
An operator must register that callback with the provider and access the same
origin. Native admin TLS is the supported remote transport.

Discovery uses the exact issuer's well-known document, bounded at 64 KiB. It
requires code response support, an asymmetric signing algorithm and valid
authorization/token/JWKS endpoints; an advertised PKCE list must contain S256.
Missing PKCE metadata is allowed because Entra/Google contracts may omit it;
requests always use S256. HTTPS issuers cannot downgrade any endpoint to HTTP.
Cross-host HTTPS endpoints are allowed for providers such as Google, subject to
the existing auth egress guard. HTTP redirects are refused for discovery, token
exchange and JWKS, rather than forwarding client secrets to redirected endpoints.
A confidential client uses advertised basic or post authentication; a public
client is selected explicitly by omitting the configured secret and always uses
PKCE S256. It does not require discovery to advertise `none`: Keycloak omits
that value even for public clients. A configured confidential client never
downgrades to public authentication or retries with another method.

The relying party holds random, single-use state/nonce/verifiers for five minutes,
with a separate HttpOnly browser-binding cookie. Callback transactions are consumed
before token exchange. Codes and nonces must match that transaction, PKCE binds
the exchange, and the signed ID token requires exact issuer/client audience,
correct `azp`, expiration, issued-at, not-before when present, a bounded subject
and matching nonce. Callback responses always navigate to a clean Console URL.
Provider errors are replaced with bounded generic categories.

## Verification owner and dependency choice

The OIDC verifier is a strict wrapper around `internal/auth`'s existing JWT
parser, asymmetric algorithm policy and JWKS cache. Both consumers share key
parsing, response/key-count bounds, key-type checks, throttling and stale grace.
JWKS keys refresh after 15 minutes; unknown-key attempts are throttled to 30
seconds; existing keys may remain usable for up to one hour from their successful
fetch during an outage. Discovery is cached for 15 minutes, with failed attempts
throttled for five seconds. Every outbound exchange has a ten-second timeout and
uses the auth egress owner; cancellation reaches verification requests.

A separate go-oidc verifier was considered and rejected because it would introduce
another JWKS/validation lifecycle. An OAuth SDK would still require custom bounded
response reads, redirect policy, server-side transactions and Jul generation
ownership. This tranche uses the maintained, already-shipped golang-jwt parser
and standard-library HTTP/crypto/JSON primitives for the deliberately small
protocol surface. No new module or license is introduced. Its acceptance requires
negative protocol tests, actual pinned Keycloak integration and browser evidence;
this ADR does not confer certification by itself. The bounded in-repository
protocol code is a permanent maintenance responsibility, not a general OAuth SDK.

On linux/amd64 with Go 1.27.2, `-trimpath -buildvcs=false` and the repository's
full tag set, the initial implementation increases the lean binary from
25,314,775 to 25,454,076 bytes (+139,301, 0.55%) and the full binary from
60,452,641 to 60,608,561 bytes (+155,920, 0.26%), compared with starting main
`de1a81a9bac976be7408e889dadc39a9619cfdd6`. These are unstripped local builds,
not release artifact sizes; embedded Console assets are included only in full.

## Session lifecycle and publication

An opaque random cookie references an in-memory session. No provider token is
retained after callback validation. Session cookies are HttpOnly, SameSite=Lax,
Path=/ and Secure with the `__Host-` prefix on HTTPS; development loopback HTTP
uses a distinct name. No Domain attribute is set. Browser JavaScript cannot read
session secrets. Cookie-authenticated unsafe requests require the exact configured
Origin, compatible fetch metadata and the non-secret `X-Jul-Console: 1` header.
A bearer header combined with a session cookie is ambiguous and denied.

Defaults are 15-minute idle, eight-hour absolute lifetime, 1,024 sessions and 128
login transactions. Maximums are 24 hours, 65,536 sessions and 1,024 transactions.
Capacity exhaustion rejects new login; it does not evict another active operator.
Same-browser login replaces the preceding session. Session maps are sharded for
constant-time authenticated lookup. Expiration is checked on lookup and bounded
stores are swept during transaction/session creation and logout. No background
worker or external database is needed. Idle entries may remain allocated until
the next store operation; both count and lifetime acceptance remain bounded.
Restart discards all sessions and transactions.

The existing immutable admin snapshot owns the complete OIDC runtime and RBAC
policy. Prepare builds local candidate state without discovery or live mutation.
Publish selects it through the existing single atomic swap. Any OIDC settings,
local credential or effective RBAC policy change creates a fresh session owner:
old sessions/transactions fail closed for newly admitted requests. Unrelated
settings preserve it. Rejected candidates retain the working owner. Requests
already pinned to an old snapshot may complete under that snapshot, matching
existing reload semantics. Session policy changes do not drain established SSE
streams; future requests reauthenticate. Self-lockout confirmation covers OIDC
policy changes made by the current OIDC operator.

The application-owned auth egress client selects the current egress generation
for each exchange, using its pool/allow-list. Retired admin runtimes own no
separate transport, goroutine or disk resource. Tightening egress affects the next
exchange; it does not erase an already validated local session.

## Outage, logout, audit and packaging

New provider authentication fails closed. Existing sessions continue within their
idle/absolute lifetime even during IdP outages; IdP account disable/logout is not
immediate Jul-session revocation. Local logout removes the Jul session at once;
it does not claim upstream global logout. Provider logout and refresh-token
sessions are deliberately omitted. Local named/legacy tokens remain available for
recovery under their own existing secure-transport and RBAC checks.

Mutation and login/logout audit uses Jul principals, roles through identity and a
stable non-secret subject fingerprint, never codes, nonce/state, cookies, tokens,
secrets or raw claims. Login metrics reuse bounded auth method/outcome labels;
no issuer, email, subject, group or principal is a metric label.

ADR 0012 permits additive commercial external identity but does not require it.
The maintainer selected this bounded capability for the existing AGPL open-source
binary; the license, free standalone floor and published APIs are unchanged.
Advanced identity governance remains unselected. NGINX migration is unaffected:
this is Jul's separate administrator identity surface, not ingress route auth.

## Interoperability and review triggers

Provider evidence is maintained in [the guide](../admin-oidc.md), with dated
contract fixtures separate from account-backed integration. Meta's documented
testing flow and its current incomplete discovery contract are not equated:
there is no endpoint override or fallback to implicit login. Other providers are
not called verified without execution evidence. Keycloak uses a pinned synthetic
realm/client/user/group fixture in the dedicated integration lane.

Revisit this decision before adding a second provider, API JWT credentials,
persistent sessions, provider logout, group retrieval or admission predicates,
or when provider/protocol/JWT/egress/lifecycle contracts change.
