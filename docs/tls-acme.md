# TLS and automatic HTTPS (ACME)

Jul.IA terminates TLS for HTTP/1.1 and HTTP/2 and, with the `http3` build tag,
can use the same server-certificate provider for HTTP/3. Certificates come from
static PEM files or a process-lifetime ACME manager.

> [!WARNING]
> **Current correction notices:**
>
> - static `cert`/`key` changes on a retained listener require restart; the live
>   provider is not atomically replaced by an ordinary config reload (#100);
> - `acme.challenge` does not currently provide proven exclusive HTTP-01 versus
>   TLS-ALPN-01 runtime behavior (#122);
> - HTTP/3 does not yet have proven parity with TCP TLS for the complete
>   server-level client-auth policy (#121).
>
> Read [current-product-truth.md](current-product-truth.md) before relying on
> historical maturity or hot-reload claims.

## Static certificate

```toml
[[servers]]
listen = "0.0.0.0:443"
server_names = ["example.com", "www.example.com"]

  [servers.tls]
  enabled = true
  cert = "/etc/jul/example.com.crt"
  key = "/etc/jul/example.com.key"
  min_version = "1.2"
```

The certificate/key pair is loaded for the listener's TLS provider. Replacing
the file contents or changing the paths does not rotate the provider on an
already-bound listener. Use a planned restart until #100 is released.

## Automatic HTTPS

ACME requires a binary built with the `acme` tag.

```toml
[[servers]]
listen = "0.0.0.0:443"
server_names = ["example.com", "www.example.com"]

  [servers.tls]
  enabled = true

    [servers.tls.acme]
    enabled = true
    email = "ops@example.com"
    ca = "letsencrypt-staging"
    challenge = "http-01"
    cache_dir = "./jul-data/certs"
```

The default CA is staging. Set `ca = "letsencrypt"` deliberately for trusted
production certificates.

## Configuration reference

### `[servers.tls]`

| Key | Default | Current behavior |
| --- | --- | --- |
| `enabled` | `false` | Listener protocol choice; retained-address transition is restart-bound/gated |
| `cert` / `key` | — | Static PEM pair; restart required to rotate on a retained listener |
| `min_version` | `"1.2"` | Startup/listener-owned; optional live transition remains gated |
| `acme` | — | Startup-owned ACME manager policy |
| `client_auth` | — | See [mtls.md](mtls.md); server-level policy is restart-bound and H3 parity is under correction |

### `[servers.tls.acme]`

| Key | Default | Current behavior |
| --- | --- | --- |
| `enabled` | `false` | Builds/uses the process ACME manager; restart-bound |
| `email` | — | ACME account contact; required by current validation |
| `ca` | `"letsencrypt-staging"` | Staging, production, or HTTPS directory URL |
| `domains` | `server_names` | Startup-owned HostPolicy/domain set |
| `challenge` | `"http-01"` | Accepted values include HTTP-01 and TLS-ALPN-01, but exclusive runtime selection is under correction in #122 |
| `dns_provider` | — | Reserved/unimplemented; must be rejected rather than ignored |
| `cache_dir` | `"./jul-data/certs"` | Stores account and certificate material; owner-restricted |
| `ocsp_stapling` | `true` | ACME-certificate stapling policy; startup-owned |

## Current capability matrix

| Capability | Current status |
| --- | --- |
| TLS 1.2/1.3 termination | supported |
| HTTP/1.1 and HTTP/2 ALPN | supported |
| SNI multi-certificate selection | supported |
| Static PEM certificate serving | supported; rotation requires restart |
| ACME issuance and background renewal | supported with `acme` build tag under the running manager |
| HTTP-01 versus TLS-ALPN-01 exclusivity | **correction pending (#122)** |
| DNS-01 | not implemented; reserved configuration must be rejected |
| ACME domain/account/issuer/cache transition | restart-bound; optional architecture work is gated |
| HTTP/3 certificate provider | supported with `http3`; server-level mTLS parity correction pending (#121) |
| OCSP stapling for ACME certs | supported with graceful unstapled fallback according to current manager behavior |
| Static certificate OCSP stapling | not implemented |
| Custom cipher ordering/session-ticket controls | not exposed |

## ACME challenge warning

The intended product contract is:

- **HTTP-01:** answer only the HTTP challenge route; do not advertise
  `acme-tls/1` solely for ACME;
- **TLS-ALPN-01:** advertise/serve the reserved ALPN challenge; do not install
  the HTTP challenge handler solely for ACME;
- **DNS-01:** reject until implemented.

That exclusive behavior is the target of #122. Until the fix is deployed, do
not use the configured selector as proof that the non-selected challenge surface
is absent. Review listener exposure and firewall rules accordingly.

## Reload and lifecycle semantics

| Change | Current operational action |
| --- | --- |
| Static `cert`/`key` path or contents | validate candidate where supported, stage complete config and restart |
| ACME enable/disable | restart |
| ACME domains, challenge, email, CA, cache or OCSP policy | restart |
| Server-level mTLS mode/CA/SAN/CRL | restart; disable H3 for protected listener until #121 |
| Route/middleware/upstream changes | ordinary handler-generation reload when otherwise hot |
| ACME certificate renewal | performed by the already-running manager; not equivalent to config hot reload |

A mixed candidate is not partially applied: route changes must not be published
while a restart-bound TLS/ACME change is merely recorded as though the complete
candidate were live.

The selected static-certificate issue #100 and gated ACME issues #103-#104 may
change exact field lifecycles later. #89 remains the authority for the final
closed-world lifecycle model.

## Security and operations

- Protect `cache_dir`; it contains ACME account and private-key material.
- Use staging for testing and deterministic local ACME infrastructure in CI.
- Do not run production certificate orders as a validation side effect.
- HTTP-01 requires a reachable plain HTTP challenge path; redirect or constrain
  all other traffic appropriately.
- TLS-ALPN-01 uses the TLS listener and reserved ALPN behavior; treat exclusivity
  as pending #122.
- ACME/OCSP outbound traffic is affected by the optional egress allow-list.
  Ensure CA and responder names are allowed before enabling egress.
- OCSP fetch failure must not expose responder URLs or domains as metric labels.
- ACME configuration applied successfully is not the same as a certificate
  being issued and available for every domain.
- Do not enable HTTP/3 on a server-level mTLS listener until #121.

## Egress example

```toml
[egress]
enabled = true
allow = [
  ".api.letsencrypt.org",
  ".acme-v02.api.letsencrypt.org",
  ".acme-staging-v02.api.letsencrypt.org",
  ".o.lencr.org",
]
```

Actual CA/CDN endpoints can evolve. Treat the configured CA directory and
observed block reason as authoritative rather than copying this list blindly.

## Metrics and status

Certificate-expiry, ACME renewal and mTLS metrics are described in the current
observability reference, but names/labels are subject to the released-contract
reconciliation in #126. Never add domain, responder URL, certificate subject,
SAN, path or raw error as an unbounded metric label.

Runtime status should distinguish:

- TLS configured and listener active;
- ACME manager configured;
- certificate currently available/expiry;
- issuance/renewal/OCSP outcome;
- H3 compiled/active;
- client-auth configured and effective by protocol.

## Current limitations

- static certificate/key rotation requires restart;
- ACME manager policy/domain/account/cache changes require restart;
- challenge selection is not yet a reliable exclusive runtime switch;
- DNS-01 is not implemented;
- server-level mTLS parity on H3 is under correction;
- no static-certificate OCSP stapling;
- no operator-configurable cipher ordering, ticket policy or 0-RTT policy;
- external DNS, firewall and CA reachability cannot be guaranteed by local
  config validation.

## Maturity status

Historical TLS and ACME benchmark/soak evidence remains useful, but the
unconditional GA statement is temporarily qualified while #121 and #122 are
open and static rotation claims are corrected. Re-certification must update the
behavior matrix, security notes, lifecycle reference, status sources, examples
and changelog with actual command and real-protocol evidence.

## Build tags

- static TLS termination is core;
- ACME requires `acme`;
- HTTP/3 requires `http3`;
- enabling a tagged feature in a binary without its tag must fail before
  persistence/live mutation.

## See also

- [Mutual TLS](mtls.md)
- [HTTP/3](http3.md)
- [Current product truth](current-product-truth.md)
- [Combined audit](audit/combined-audit-2026-08-03.md)
- [Compatibility policy](compatibility.md)
- [TLS example](../testdata/tls.toml)
