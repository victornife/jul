# TLS and automatic HTTPS (ACME)

> **Maturity:** GA (see [ADR 0003](adr/0003-maturity-and-ga.md)). Mutual TLS
> is documented separately in [mtls.md](mtls.md).

Jul.IA terminates TLS for HTTP/1.1, HTTP/2, and—with the `http3` tag—HTTP/3.
Certificates come from static PEM files or a process-lifetime ACME manager.
Selection is by SNI, so one listener can serve multiple names.

## Quick start

### Static certificate

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

Static certificate replacement on an already-bound listener remains
restart-bound until the dedicated certificate-rotation lifecycle is implemented.

### Automatic HTTPS

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

The CA defaults to staging. Set `ca = "letsencrypt"` deliberately for trusted
production certificates.

## Configuration reference

### `[servers.tls]`

| Key | Default | Behavior |
| --- | --- | --- |
| `enabled` | `false` | Enables TLS for the listener |
| `cert` / `key` | — | Static PEM pair; mutually exclusive with ACME |
| `min_version` | `"1.2"` | Minimum TLS version: `1.2` or `1.3` |
| `acme` | — | Automatic certificate block |
| `client_auth` | — | Server-level mutual TLS; see [mtls.md](mtls.md) |

### `[servers.tls.acme]`

| Key | Default | Behavior |
| --- | --- | --- |
| `enabled` | `false` | Enables the process ACME manager for this listener |
| `email` | — | Required account contact |
| `ca` | `"letsencrypt-staging"` | Staging, production, or an HTTPS ACME directory URL |
| `domains` | `server_names` | Names permitted by the manager's host policy |
| `challenge` | `"http-01"` | `http-01` or `tls-alpn-01`; the modes are exclusive |
| `dns_provider` | — | Reserved; rejected until DNS-01 is implemented |
| `cache_dir` | `"./jul-data/certs"` | ACME account and certificate cache |
| `ocsp_stapling` | `true` | Staple OCSP responses for ACME-issued certificates |

Every enabled ACME block shares one process manager and therefore must agree on
email, CA, challenge, cache directory, and OCSP policy. Validation rejects a
configuration whose blocks disagree.

## Challenge behavior

Jul.IA activates only the configured challenge surface.

| Challenge | Required listener | Runtime behavior |
| --- | --- | --- |
| `http-01` | Plain HTTP, normally port 80 | Installs autocert's `/.well-known/acme-challenge/*` handler. ACME does not add `acme-tls/1` to TLS ALPN. |
| `tls-alpn-01` | TLS, normally port 443 | Advertises `acme-tls/1` on the ACME-enabled TLS listener. The plain HTTP challenge handler is not installed by ACME. |
| `dns-01` | — | Not implemented; validation rejects it and `dns_provider`. |

The empty programmatic value is treated defensively as the documented HTTP-01
default. Normal parsed configurations receive the same default before runtime
construction.

Non-challenge HTTP requests continue through Jul.IA's normal handler. Ordinary
TLS clients continue to negotiate `h2` or `http/1.1`; only an ACME TLS-ALPN
validation client selects `acme-tls/1`.

## Capability matrix

| Capability | Status |
| --- | --- |
| TLS 1.2/1.3 termination | Supported |
| HTTP/1.1 and HTTP/2 ALPN | Supported |
| HTTP/3 on the same certificate provider | Supported with `http3` |
| SNI exact and wildcard certificate selection | Supported |
| Static PEM certificate serving | Supported; rotation currently requires restart |
| ACME issuance and renewal | Supported with `acme` |
| Exclusive HTTP-01 selection | Supported |
| Exclusive TLS-ALPN-01 selection | Supported |
| DNS-01 | Not implemented; rejected |
| OCSP stapling for ACME certificates | Supported with graceful unstapled fallback |
| OCSP stapling for static certificates | Not implemented |
| Custom cipher ordering / ticket / 0-RTT configuration | Not exposed |

## Lifecycle semantics

| Change | Current action |
| --- | --- |
| ACME certificate renewal under the running manager | Automatic, no configuration reload |
| Static `cert`/`key` path or file contents | Planned restart |
| ACME enablement, domains, challenge, account, issuer, cache, or OCSP policy | Planned restart |
| Server-level mTLS policy | Planned restart; applies to subsequent handshakes |
| Ordinary route, middleware, and upstream changes | Handler-generation reload when otherwise hot |

A mixed candidate is not partially applied: a candidate containing a
restart-bound TLS/ACME change must not publish only its hot subset while reporting
the whole candidate live.

## Security and operations

- Protect `cache_dir`; it contains ACME account and private-key material.
- Use staging or a deterministic local ACME server for testing. Validation must
  not create production orders.
- HTTP-01 exposes a plain HTTP challenge route; redirect or constrain all other
  traffic appropriately.
- TLS-ALPN-01 exposes the reserved ALPN protocol only on the selected TLS
  listener and only when that mode is configured.
- ACME and OCSP outbound traffic is governed by the optional egress allow-list.
- OCSP fetch failure serves the certificate unstapled rather than failing the
  handshake; clients fall back to their own revocation behavior.
- A successfully applied configuration is not proof that external DNS,
  firewall, CA reachability, or certificate issuance is healthy.
- Keep metric labels bounded: never label by domain, responder URL, certificate
  subject, SAN, or raw error.

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

Actual CA/CDN endpoints can evolve. Treat the configured directory URL and
observed bounded block reason as authoritative.

## Current limitations

- No DNS-01 or wildcard issuance through DNS automation.
- Static certificate/key rotation requires restart.
- ACME manager account, issuer, domain, cache, challenge, and OCSP transitions
  require restart.
- No static-certificate OCSP stapling.
- No operator-configurable cipher ordering, session-ticket, or server-side 0-RTT
  policy.
- Local validation cannot guarantee external DNS, firewall, CA, or responder
  availability.

## Verification

Focused tests cover:

- HTTP-01 handler interception and normal-request fall-through;
- TLS-ALPN-01 leaving the HTTP challenge path on the normal handler;
- ALPN advertisement in both modes;
- the defensive HTTP-01 default;
- consistency validation across ACME blocks;
- build-tag behavior.

```bash
go test -tags acme ./internal/server -run 'TestACME|TestAcme|TestListenerNextProtos|TestNewACMEManager|TestDirectoryURL' -count=1
go test -race -tags acme ./internal/server -run 'TestACME|TestAcme|TestListenerNextProtos|TestNewACMEManager|TestDirectoryURL' -count=1
go test ./internal/config -run 'ACME|Acme' -count=1
```

## See also

- [Mutual TLS](mtls.md)
- [HTTP/3](http3.md)
- [Egress policy](egress.md)
- [Compatibility policy](compatibility.md)
- [ACME example](../examples/auto-https/README.md)
