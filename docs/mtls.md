# Mutual TLS (client-certificate authentication)

Jul.IA can authenticate clients using certificates presented during a TLS
handshake. On the TCP TLS path, the configured client CA, optional SAN allow-list
and CRL are used to verify the peer; the resulting identity can be exposed to
upstreams through `$ssl_client_*` variables.

mTLS is in the core binary and uses Go's `crypto/x509` implementation.

> [!WARNING]
> **HTTP/3 parity correction:** the current QUIC TLS construction does not yet
> have proven equivalent enforcement of the complete server-level
> `tls.client_auth` policy. Do not enable HTTP/3 on an mTLS-protected listener
> until #121 is merged, released and verified with real QUIC clients. The
> configuration and operational behavior below are authoritative for the TCP
> TLS path; location-level HTTP checks are not a substitute for missing
> handshake-level parity.

## How it works on the TCP TLS path

1. A TLS listener configured with `tls.client_auth` requests or requires a
   client certificate.
2. The Go TLS stack validates the certificate chain against `ca_file`.
3. Jul.IA applies configured CRL and SAN restrictions.
4. The verified identity is attached to the request context.
5. A location with `require_client_cert = true` rejects a request that has no
   verified certificate.
6. Proxy headers referencing `$ssl_client_*` are overwritten from the verified
   identity rather than trusted from client input.

## Configuration

```toml
[[servers]]
listen = "0.0.0.0:443"
server_names = ["api.example.com"]

  [servers.tls]
  enabled = true
  cert = "/etc/jul/server.crt"
  key = "/etc/jul/server.key"

    [servers.tls.client_auth]
    mode = "require"                       # none | request | require
    ca_file = "/etc/jul/clients-ca.pem"
    verify_san = ["svc.internal.example.com"]
    crl_file = "/etc/jul/clients.crl"
```

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `mode` | string | `none` | `none`, `request`, or `require` |
| `ca_file` | string | — | PEM CA bundle; required when mode is `request` or `require` |
| `verify_san` | string list | — | Optional accepted DNS, email, URI or IP SAN values |
| `crl_file` | string | — | Optional PEM/DER CRL signed by a configured CA |
| `require_client_cert` | location bool | `false` | HTTP-level requirement for a verified identity on the matched location |

## Modes

- **`none`** — client certificates are not used.
- **`request`** — the server requests a certificate but allows an anonymous
  handshake; a presented certificate must still validate.
- **`require`** — the handshake fails unless a valid certificate is presented.

When server blocks share a listen address, the listener-level policy is
aggregated according to the current implementation. Review shared-address
configuration carefully: independent per-vhost client-CA isolation is better
achieved with separate listen addresses.

## Per-location enforcement

Use `mode = "request"` with `require_client_cert = true` on selected locations
when one listener serves both public and certificate-protected paths:

```toml
[servers.tls.client_auth]
mode = "request"
ca_file = "/etc/jul/clients-ca.pem"

[[servers.locations]]
match = { type = "exact", path = "/health" }
return = 204

[[servers.locations]]
match = { type = "prefix", path = "/admin" }
proxy_pass = "http://127.0.0.1:9000"
require_client_cert = true
```

This location-level check occurs after the TLS handshake. It does not repair a
transport whose handshake policy failed to request/verify the certificate; that
is why #121 must apply the same server-level client-auth bundle to HTTP/3.

## Identity variables

| Variable | Value |
| --- | --- |
| `$ssl_client_verify` | `SUCCESS` when a verified certificate is present, otherwise `NONE` |
| `$ssl_client_s_dn` | Subject distinguished name |
| `$ssl_client_i_dn` | Issuer distinguished name |
| `$ssl_client_cn` | Subject common name |
| `$ssl_client_serial` | Certificate serial number |
| `$ssl_client_fingerprint` | SHA-256 fingerprint of the DER certificate |
| `$ssl_client_san` | Comma-joined SAN values |

Example:

```toml
[[servers.locations]]
match = { type = "prefix", path = "/api" }
proxy_pass = "http://127.0.0.1:9000"
require_client_cert = true

  [servers.locations.headers]
  X-Client-Verify = "$ssl_client_verify"
  X-Client-CN = "$ssl_client_cn"
  X-Client-DN = "$ssl_client_s_dn"
  X-Client-Serial = "$ssl_client_serial"
  X-Client-Fingerprint = "$ssl_client_fingerprint"
```

The upstream must only trust these asserted identity headers from Jul.IA and
must not be directly reachable by untrusted clients.

## CRL and SAN checks

- `crl_file` rejects a configured revoked serial after chain verification. The
  CRL is parsed and its signature checked against the configured CA set.
- `verify_san` narrows acceptance to certificates containing at least one
  configured SAN value.
- CA, CRL and SAN material is currently listener/startup-owned. A planned
  restart is required to apply changes on a retained address.

## Metrics

The current documentation records `jul_mtls_handshakes_total{result=...}` for
verified/rejected client-certificate outcomes. Metric names and labels remain
subject to the released-contract reconciliation in #126. Do not add certificate
subjects, SANs, serials or fingerprints as metric labels.

CA-chain failures may occur before Jul.IA's additional verifier emits a
feature-specific result; location-level missing-certificate rejection appears as
an HTTP response rather than a successful mTLS handshake.

## Current lifecycle

- server-level `client_auth` mode, CA, CRL and SAN policy are restart-bound on a
  retained listener;
- per-location `require_client_cert` follows normal handler-generation reload;
- static server certificate/key files are also restart-bound until #100;
- ordinary route reload does not prove a new handshake policy is active;
- mixed candidates containing server-level client-auth changes must be staged as
  a complete candidate rather than partially applying route changes;
- optional live TLS-policy/connection-epoch work remains gated in #101 after the
  current HTTP/3 correctness issue is fixed.

## Operational guidance

- Disable HTTP/3 on listeners that require server-level mTLS until #121.
- Rotate a client CA by temporarily trusting old and new CA certificates in one
  bundle, restart, migrate clients and later remove the old CA with another
  controlled restart.
- Protect CA, CRL, server key and certificate paths with service-user-only
  permissions.
- Use separate listener addresses when strict per-vhost CA isolation matters.
- Verify missing, valid, unknown-CA, revoked and disallowed-SAN clients with real
  TCP TLS clients after each trust-policy deployment.
- After #121, repeat that matrix with real QUIC clients before re-enabling H3.

## Limits

- No OCSP validation for client certificates; CRL is the supported revocation
  source.
- No built-in mapping from client-certificate identity to application roles or
  Jul.IA admin RBAC.
- No SPIFFE workload attestation/controller; URI SAN values can still be
  matched explicitly.
- Established connections are not retroactively re-handshaken after a trust
  policy change; current policy changes use restart to establish a clean
  connection boundary.

## Maturity status

Historical mTLS behavior, benchmark and soak evidence remains useful for the TCP
TLS path. The unconditional cross-protocol GA statement is temporarily qualified
until #121 proves:

- identical server-level client-auth inputs for TCP TLS and HTTP/3;
- `none`, `request` and `require` behavior over QUIC;
- missing, valid, invalid, revoked and SAN-rejected certificate outcomes;
- request-context identity and metrics parity;
- no secret or high-cardinality telemetry regression;
- real protocol, full-tag and race evidence;
- updated status, security and lifecycle documentation.

See [http3.md](http3.md), [current product truth](current-product-truth.md), the
[combined audit](audit/combined-audit-2026-08-03.md) and issue #121.
