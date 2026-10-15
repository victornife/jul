# External OIDC authentication for the admin Console

**Beta · Unreleased · one provider per Jul instance.** OIDC authenticates an
administrator; the existing Jul RBAC catalogue decides what that person can do.
This capability has its own `ADMIN-OIDC` maturity row. It does not inherit the
Console's GA/soak evidence and is absent from published v2.2.0 packages.

The Console uses Authorization Code + PKCE S256 and an opaque HttpOnly session
cookie. Provider ID/access/refresh tokens never enter frontend storage, rendered
identity, audit, logs or metrics. API automation continues to use scoped local
Jul bearer tokens. Application authentication, invitations, audiences and sharing
groups remain separate (#579/#588); SAML and SCIM are not implemented.

## Configure and register the callback

1. Keep a tested local administrator token for recovery. Enable `[admin.rbac]`.
2. Register a provider application using Authorization Code and PKCE S256.
   Register exactly `https://admin.example.com/auth/oidc/callback`, including its
   scheme, host, port and path. Wildcards and user-selected return URLs are not used.
3. Configure the exact issuer, client ID and that same callback below. Use a
   secret reference when the provider requires a confidential client. Do not
   request `offline_access`; Jul never refreshes indefinitely.
4. Add exact subject mappings, or a bounded flat group claim and group mappings.
   Verify the provider's stable `sub` through your controlled provider tooling;
   never substitute email, display name or an email domain.
5. Configure trusted native [admin TLS](console.md#security-model), or a TLS proxy
   connecting to Jul over loopback, and an appropriate network restriction.
   Preserve the configured Host/origin. Jul does not trust forwarded identity or
   scheme headers. A remote plaintext admin connection remains refused.
6. Publish through the existing config workflow, test a mapped least-privilege
   account and an unmapped account, then verify local-token recovery separately.

```toml
[admin]
enabled = true
listen = "127.0.0.1:9090"
token = "${env:JUL_RECOVERY_TOKEN}"

[admin.rbac]
enabled = true

[admin.oidc]
enabled = true
provider_name = "Company identity"
issuer = "https://identity.example.com/realms/company"
client_id = "jul-console"
client_secret = "${env:JUL_OIDC_CLIENT_SECRET}" # omit for a public PKCE client
callback_url = "https://admin.example.com/auth/oidc/callback"
scopes = ["openid"]
group_claim = "groups"
session_idle = "15m"
session_absolute = "8h"
max_sessions = 1024
max_transactions = 128
clock_skew = "60s"

[[admin.oidc.subjects]]
subject = "exact-provider-subject"
principal = "alice"
role = "viewer"

# Optional alternative/fallback for subjects without an exact subject mapping:
[[admin.oidc.groups]]
group = "logistics-operators"
role = "operator"
```

Remove the group rule and `group_claim` when using only subject mappings. Group-only authentication
uses `oidc-<issuer/subject fingerprint>` as the per-person principal, so members
of one group do not collapse into a shared audit identity. A subject rule takes
precedence. Multiple matching groups must select the same role; conflicting
roles deny admission. Missing/malformed/oversized claims or provider overage
indicators never trigger a Graph/directory fetch or a default grant. Claim names
are literal, including namespaced Auth0 names; nested expressions are unsupported.

Local predefined and custom roles are the only authorization vocabulary.
At least one active local admin-capable credential must remain configured.
No provider token can be pasted into Jul's local-token prompt to gain access.

For development, the issuer and callback may use HTTP only for literal loopback
addresses or exactly `localhost`. An HTTP callback additionally requires a
loopback admin listener. Register the exact development URI with the provider;
this exception never permits a remote cleartext admin/provider URL.

The exhaustive [generated reference](generated/config-reference.md#admin)
contains fields, bounds and lifecycle. The
[example configuration](../testdata/admin-oidc/example.toml) uses secret references.
Literal secrets trigger `jul lint` warnings; env/file/secret resolution and
redaction use the existing [secrets mechanism](secrets.md).

## Browser operation and session limits

Open the Console at its configured origin. When authentication is needed, choose
**Sign in with …**. The server performs discovery and redirects to the provider;
JSON/API callers continue to receive a machine-readable 401, never an automatic
external sign-in redirect. The Console displays the mapped principal, Jul role,
current expiry indication and **Sign out**. Expired sessions offer sign-in again;
the local token option remains available for recovery.

Transactions last five minutes. Sessions default to 15-minute inactivity and an
eight-hour absolute lifetime; activity can extend idle expiry but never absolute
expiry. Limits are `1m <= idle <= absolute <= 24h`, 1–65,536 sessions,
1–1,024 transactions and at most 120 seconds of clock skew. Zero adopts each
documented default, including 60 seconds for skew. Session capacity exhaustion
refuses a new login rather than evicting another operator. Repeated sign-in in
the same browser replaces its preceding session. Multiple concurrent login tabs
share the browser-binding cookie: the most recent initiation supersedes earlier
ones, which fail safely.

HTTPS cookies use `__Host-jul\_oidc`, Secure, HttpOnly, SameSite=Lax and Path=/,
without Domain. Development HTTP uses `jul\_oidc\_dev`. Login binding uses a
separate five-minute cookie. JavaScript never reads either secret. Every unsafe
session-authenticated API request requires the exact configured Origin and
`X-Jul-Console: 1`; incompatible fetch metadata is refused. Bearer plus session
credentials are ambiguous and rejected. Switching to a local token first logs
out the browser session. Successful sign-in and local logout expire both secure
and development cookie forms, including retired login bindings. Cookies are
host-scoped rather than port-isolated: use a dedicated admin hostname and trust
other services sharing a development loopback host.

Local sign-out revokes the Jul session immediately. It does **not** terminate
upstream provider sessions or every other browser's Jul session. This tranche
has no provider logout/back-channel logout, refresh tokens or external database.
Restart loses sessions/transactions. Expiration is checked at lookup and stores
are swept on login/session creation/logout; idle expired entries can remain
allocated until the next store operation within the fixed capacity.

## Publish, revocation and outages

OIDC and RBAC use the existing atomic admin snapshot. Rejected candidates retain
the working generation. Changing OIDC configuration, local credentials or the
effective RBAC policy invalidates all prior browser sessions/transactions after
successful Publish. Role reductions and removed mappings therefore cannot leave
stale grants on fresh requests. Unrelated admin settings preserve sessions.
An OIDC operator must acknowledge a change that invalidates their session.
Already admitted requests and established SSE streams retain their captured
policy; this is not active stream termination.

New authentication fails closed during provider failures. Existing local sessions
continue only within their configured idle/absolute limits; provider account
revocation is not instantaneous Jul-session revocation. To revoke sessions now,
change the OIDC/authentication policy and Publish, disable OIDC, or restart under
a controlled recovery procedure. There is no remote session-list/revoke API.

Discovery is cached for 15 minutes and failed refresh attempts are throttled for
five seconds. JWKS uses the existing cache: 15-minute refresh, 30-second minimum
refresh interval and a one-hour stale-key bound from the last successful fetch.
An unknown signing key fails closed until a permitted successful refresh. All
provider exchanges have ten-second timeouts and bounded responses. Discovery and
token responses are limited to 64 KiB, ID tokens to 16 KiB, JWKS to 1 MiB and 128
keys. Redirects are rejected; there is no TLS-verification-disable option.

The OIDC-enabled Console HTML uses a same-origin referrer policy so browsers
preserve Origin on the sign-in form POST. Cross-origin referrers are suppressed;
login redirects and callbacks use no-referrer, and null Origins remain denied.

The [auth egress subsystem](egress.md) covers discovery, token exchange and JWKS,
including cross-host HTTPS endpoints discovered for Google. Allow the issuer,
token host and JWKS host actually advertised by your provider. Tightened egress
uses the new generation for the next exchange; it does not revoke already
validated local sessions. No provider is a mandatory dependency while OIDC is off.

Login outcome metrics reuse `jul_auth_decisions_total{method="oidc",result="allow|deny"}`.
Audit attribution uses configured principals or a stable issuer/subject
fingerprint. These signals never label metrics with issuer, principal, group,
email or subject and never record protocol credentials or complete claim sets.

## Provider compatibility and evidence

This is a standards-based capability, not a claim to work with every provider.
Evidence is dated 2026-10-15. Public contract fixtures do not establish account login.

| Provider | Configuration and caveats | Evidence |
| --- | --- | --- |
| Google | Issuer `https://accounts.google.com`; Web application client; exact authorized redirect URI and confidential client secret. Use exact `(iss, sub)` mapping. Normal ID-token claims do not provide generic Jul group membership. Provider access tokens are not Jul API tokens. | Real public discovery retrieved and committed; provider-contract fixture tested. Account login not tested: provider credentials unavailable. |
| Microsoft Entra ID | Register a Web callback; prefer `https://login.microsoftonline.com/<tenant-id>/v2.0`, not common/organizations issuer templates. Follow Microsoft's ID-token registration instructions. Enable the groups claim explicitly if used. Omitted groups, `hasgroups` and group overage references deny group admission; no Graph calls. | Official contract documented/configuration expected. Tenant-backed login not tested: tenant credentials unavailable. |
| Keycloak | Realm issuer `<base>/realms/<realm>`. Standard Flow enabled, public PKCE or confidential client, exact redirect URI, S256 required. A group-membership protocol mapper must emit a flat string-array ID-token claim, with full path disabled when using short group names. | **Verified by the real pinned Keycloak integration:** version 26.5.7, digest pinned in the launcher. Subject/group admission, unknown user denial, local logout and actual outage/recovery pass in [source-bound CI](https://github.com/victornife/jul/actions/runs/38717822080/job/116364721484). Other provider versions/tenants are not thereby verified. |
| Auth0 | Regular Web Application, exact Allowed Callback URL and client secret; use the exact tenant/custom-domain issuer consistently. Jul supports literal namespaced custom claims such as `https://jul.example/groups`; configure an Action/protocol claim to emit a flat ID-token array. | Official contract documented/configuration expected. Tenant-backed login not tested: tenant credentials unavailable. |
| Meta/Facebook | Meta documents a Code + PKCE OIDC flow marked as testing. Its retrieved discovery document advertises only `id_token`/`token id_token`, lacks a token endpoint and does not declare code support. Jul rejects that contract. No unsafe endpoint override, implicit fallback, group claim or portable API-token compatibility is claimed. | **Unsupported in this tranche.** Real public discovery retrieved; negative provider-contract fixture tested. Account login not tested. |
| Local synthetic provider | Full browser flow, issuer/audience/nonce/signature failures, unknown users/groups, capacity, expiry, CSRF, local logout and outages. | Deterministic fake-provider tests; distinct from real-provider integration. |

The Keycloak fixture uses synthetic users Alice (mapped) and Bob (unmapped).
Run `scripts/admin-oidc-keycloak.sh` with Docker; the required CI lane runs this
without silently skipping. It exercises actual discovery, browser credential
form, Code + PKCE, subject/group mapping, unknown-group denial and local logout.
Keycloak signing-key rotation is represented by the shared verifier's real-key
rotation/throttle/stale-cache tests, rather than changing the provider's key
administration API in this fixture. This is a bounded substitute, not a claim
that native Keycloak key administration was exercised. The fake-provider browser
suite runs against the actual admin router and compiled Console.

Revalidate on relevant provider, protocol, JWT, egress, session, lifecycle or
Console changes before claiming a new provider/release path. The maintainer owns
this matrix; ordinary CI depends on local/pinned fixtures, not public-provider uptime.

Official contracts: [OIDC Core](https://openid.net/specs/openid-connect-core-1_0.html),
[discovery](https://openid.net/specs/openid-connect-discovery-1_0.html),
[Google](https://developers.google.com/identity/openid-connect/openid-connect),
[Entra ID tokens](https://learn.microsoft.com/en-us/entra/identity-platform/id-token-claims-reference),
[Keycloak](https://www.keycloak.org/securing-apps/oidc-layers),
[Auth0 discovery](https://auth0.com/docs/get-started/applications/configure-applications-with-oidc-discovery),
[Auth0 claims](https://auth0.com/docs/secure/tokens/json-web-tokens/create-custom-claims),
[Meta testing flow](https://developers.facebook.com/documentation/facebook-login/guides/advanced/oidc-token.md/).
Sanitized public metadata fixtures carry their exact source URLs and retrieval dates.

## Migration, recovery, rollback and troubleshooting

Keep local admin tokens during rollout; move automation to least-privilege named
tokens and verify its attribution before enabling browser OIDC. Test both a
mapped browser and an unmapped browser in separate clean profiles. Confirm that
local recovery works during an IdP outage without using the OIDC session cookie.
Do not remove the last active local admin-capable credential.

For recovery, connect through the protected admin transport with a local token,
without browser cookies, or edit operator-owned configuration out of band and
use the documented authority/reload procedure. Set `[admin.oidc].enabled = false`
and successfully Publish to retire browser sessions; keep RBAC/local tokens.
The normal configuration rollback path works too. Downgrading to an older binary
requires removing the unknown OIDC block first; old packages do not gain this feature.

A generic sign-in error can mean discovery/egress outage, wrong callback/client,
issuer mismatch, unrecognized subject, missing groups or role conflict. Check
the exact provider configuration and safe egress/auth outcome signals. A changed
policy or restart requires a fresh login. For transaction capacity, wait five
minutes for expiration or restart under local recovery; never increase bounds
without an operating need. Do not paste tokens, codes, cookies, state, nonce,
verifiers, client secrets or complete claims into logs, screenshots or issues.

[ADR 0022](adr/0022-admin-console-oidc.md) records the trust model, rejected
alternatives and OSS decision. This admin-only capability does not affect NGINX
migration or ingress application authentication.
