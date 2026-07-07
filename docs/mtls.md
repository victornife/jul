# Mutual TLS (client-certificate authentication)

Jul.IA can authenticate clients by their TLS certificate, not just authenticate
itself to clients. When mutual TLS (mTLS) is enabled, the server verifies the
certificate a client presents against a trusted CA bundle during the TLS
handshake, and the verified identity (subject, issuer, serial, fingerprint, SANs)
is exposed to your upstreams as `$ssl_client_*` proxy variables. This is the
foundation for zero-trust ingress: only callers holding a certificate signed by
your CA reach the backend, and the backend can authorize on *who* they are.

mTLS is in **core** — no build tag — and uses only the standard library's
`crypto/x509`.

> **Maturity:** GA (see [ADR 0003](adr/0003-maturity-and-ga.md)). It coexists
> with ACME/static server certificates; the server certificate and the client CA
> are independent.

## Contents

- [How it works](#how-it-works)
- [Configuration](#configuration)
- [Modes](#modes)
- [Per-location enforcement](#per-location-enforcement)
- [Identity variables](#identity-variables)
- [Certificate revocation (CRL)](#certificate-revocation-crl)
- [SAN allow-list](#san-allow-list)
- [Metrics](#metrics)
- [Operational notes](#operational-notes)
- [Limits](#limits)

## How it works

1. A listener with `tls.client_auth` enabled advertises that it accepts (or
   requires) a client certificate in the TLS handshake.
2. The client presents its certificate. The TLS stack verifies the certificate
   chain against `ca_file`. A certificate signed by an unknown CA fails the
   handshake outright.
3. After chain verification, Jul.IA applies optional extra checks: the
   certificate's serial must not appear in the CRL, and — if `verify_san` is set
   — one of its subject alternative names must be in the allow-list.
4. On a request, the verified leaf certificate is turned into an identity and
   placed in the request context. If the matched location sets
   `require_client_cert` and no verified certificate is present, the request is
   rejected with **403**.
5. When the request is proxied, any `$ssl_client_*` variable referenced in a
   header is substituted from that identity.

## Configuration

mTLS is configured per server block, under `tls.client_auth`. The server must
have TLS enabled (a server certificate via `cert`/`key` or ACME).

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
    ca_file = "/etc/jul/clients-ca.pem"    # CA bundle that signs client certs
    verify_san = ["svc.internal.example.com"]   # optional SAN allow-list
    crl_file = "/etc/jul/clients.crl"      # optional revocation list
```

| Key | Type | Default | Description |
| --- | ---- | ------- | ----------- |
| `mode` | string | `none` | `none` (off), `request` (optional), or `require` (mandatory) |
| `ca_file` | string | — | PEM bundle of one or more CAs that client certificates are verified against. **Required** for `request`/`require` |
| `verify_san` | string list | — | If set, a client certificate is accepted only when one of its SANs (DNS, email, URI, or IP) is in this list |
| `crl_file` | string | — | PEM or DER certificate revocation list; must be signed by one of the CAs in `ca_file` |

The location-level switch is a single boolean:

| Key | Type | Default | Description |
| --- | ---- | ------- | ----------- |
| `require_client_cert` | bool | `false` | Reject a request to this location with 403 unless a verified client certificate was presented. Requires the server's `client_auth` to be enabled |

## Modes

- **`none`** — client certificates are ignored (the default; mTLS off).
- **`request`** — the server asks for a certificate but completes the handshake
  whether or not one is presented. A presented certificate **is** verified
  against `ca_file` (an invalid one still fails). Use this when most traffic is
  anonymous but some locations need a certificate — combine it with
  `require_client_cert` on those locations.
- **`require`** — the handshake fails unless the client presents a certificate
  that chains to `ca_file`. Every request on the listener is therefore from a
  verified client.

When multiple server blocks share one listen address, the strongest mode wins
and the CA bundles, SAN allow-lists, and CRLs are unioned, so a client is
admitted if any block on that address would admit it.

## Per-location enforcement

`mode = "require"` is listener-wide. To keep a listener mostly open but lock down
a subtree, use `mode = "request"` and set `require_client_cert` on the protected
locations:

```toml
  [servers.tls.client_auth]
  mode = "request"
  ca_file = "/etc/jul/clients-ca.pem"

  # Public — no certificate needed.
  [[servers.locations]]
  match = { type = "exact", path = "/health" }
  return = 204

  # Locked — a verified client certificate is mandatory here.
  [[servers.locations]]
  match = { type = "prefix", path = "/admin" }
  proxy_pass = "http://127.0.0.1:9000"
  require_client_cert = true
```

A request to `/admin` without a verified certificate gets a 403; `/health`
stays open.

## Identity variables

When a header value references an `$ssl_client_*` variable, Jul.IA substitutes it
from the verified client identity before forwarding the request upstream. This
lets the backend trust an HTTP header instead of re-doing TLS.

| Variable | Value |
| -------- | ----- |
| `$ssl_client_verify` | `SUCCESS` when a verified certificate is present, otherwise `NONE` |
| `$ssl_client_s_dn` | Subject distinguished name (RFC 2253) |
| `$ssl_client_i_dn` | Issuer distinguished name (RFC 2253) |
| `$ssl_client_cn` | Subject common name |
| `$ssl_client_serial` | Certificate serial number (decimal) |
| `$ssl_client_fingerprint` | SHA-256 of the DER certificate, lowercase hex |
| `$ssl_client_san` | Comma-joined subject alternative names (DNS, IP, URI, email) |

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

On a `request`-mode listener where no certificate was presented,
`$ssl_client_verify` resolves to `NONE` and the other variables are empty, so the
upstream can tell verified and anonymous traffic apart.

> **Trust boundary:** because these headers carry an asserted identity, make sure
> the backend only trusts them when they arrive from Jul.IA. Jul.IA always
> overwrites the listed headers from the verified certificate (it does not pass a
> client-supplied `X-Client-*` through), but the upstream should still be
> reachable only via the proxy.

## Certificate revocation (CRL)

Set `crl_file` to reject certificates that have been revoked even though they
still chain to the CA. The file may be PEM (`X509 CRL` block) or raw DER. Its
signature is verified against one of the CAs in `ca_file`; an unsigned or
foreign-signed CRL is rejected at startup so a forged list cannot silently
disable revocation. A handshake presenting a revoked serial is rejected and
counted as `rejected`.

The CRL is read when the listener binds. Refresh it by reloading the process
(see [Operational notes](#operational-notes)).

## SAN allow-list

`verify_san` narrows acceptance beyond "signed by our CA" to "signed by our CA
**and** identifies one of these names". Matching is case-insensitive and covers
DNS names, email addresses, URIs (e.g. SPIFFE IDs), and IP addresses. A
certificate whose SANs are all outside the list is rejected and counted as
`rejected`, even though its chain is valid.

## Metrics

| Metric | Type | Labels | Meaning |
| ------ | ---- | ------ | ------- |
| `jul_mtls_handshakes_total` | counter | `result` | Handshakes presenting a CA-verified client certificate: `verified` (accepted) or `rejected` (revoked serial or disallowed SAN) |

Two cases are intentionally **not** in this counter:

- A certificate that fails CA-chain verification — the TLS stack aborts the
  handshake before Jul.IA's check runs. (These appear as failed connections, not
  as a labeled handshake.)
- A request denied by `require_client_cert` because no certificate was
  presented — that is an HTTP decision and shows up as a `403` in
  `jul_http_requests_total`, not as a handshake.

## Benchmarks

Handshake cost, from the in-tree `BenchmarkMTLSHandshake` and
`BenchmarkTLSHandshakeServerAuth` (loopback, TLS 1.3, P-256 ECDSA certificates;
indicative of relative cost, not an absolute throughput claim):

| Handshake | ns/op | B/op | allocs/op |
| --- | --- | --- | --- |
| Server-auth only (baseline) | ~2,080,000 | ~122,000 | ~866 |
| Mutual TLS (chain verify + SAN allow-list) | ~2,400,000 | ~141,000 | ~1,087 |

Verifying the client certificate adds roughly **15%** to the handshake
(~0.3 ms, ~18 KB, ~220 allocs), and it happens **once per connection**, not per
request. Reproduce with:

```sh
go test -run '^$' -bench 'Handshake|MTLS' -benchmem ./internal/server/
```

## Operational notes

- **Bind-time, not hot-reload.** Like `tls.min_version`, `client_auth` (mode, CA
  bundle, CRL, SAN list) is read when the listener starts. Editing it and
  reloading swaps HTTP routing immediately, but the new client-auth settings
  apply to **newly bound listeners** — restart Jul.IA (or change the listen
  address) to roll a CA or CRL. Per-location `require_client_cert` *is* part of
  normal routing and takes effect on reload.
- **Server vs client certificates are separate.** The server certificate can
  come from ACME or static `cert`/`key`; `ca_file` is only the trust anchor for
  *client* certificates. They do not interfere.
- **Rotation.** To rotate the client CA without downtime, issue the new CA
  alongside the old one in `ca_file` (it is a bundle), restart, then drop the old
  CA once all clients have migrated.
- **Console editing.** The Console's **TLS & Certificates → Mutual TLS** section
  edits `client_auth` in place (mode, CA bundle, CRL, SAN allow-list), and a
  per-route **require client certificate** toggle (also on the Routes detail)
  sets `require_client_cert`. Both go through Validate → Diff → Apply. The editor
  and the diff repeat the bind-time caveat above: saving the server-level block
  reloads routing but applies the new client-auth on the next restart, while the
  per-location toggle takes effect on reload.

## Limits

- **CRL only; no OCSP.** Revocation is checked against a configured CRL file.
  There is no OCSP or OCSP-stapling client-cert check.
- **Bundle is read at bind time.** CA and CRL changes need a restart (above).
- **Identity is delivered via headers.** The verified identity is exposed as
  `$ssl_client_*` proxy variables for the upstream to authorize on; there is no
  built-in mapping from a client certificate to Jul.IA's own auth backends.
- **Cross-vhost union.** When several server blocks share a listen address, their
  CA pools, SAN lists, and CRLs are unioned (strongest mode wins). For strict
  per-vhost isolation, give each vhost its own listen address.
- **SPIFFE/SVID** identity and workload attestation are out of scope (a later
  mesh concern), though SPIFFE URIs in a SAN work with `verify_san` today.

## GA status

Per [ADR 0003](adr/0003-maturity-and-ga.md), mTLS is **GA**. The soak test
(criterion 5) was completed on 2026-07-05 via Phase 2A.

| # | GA criterion | Status |
| --- | --- | --- |
| 1 | Behaviour matrix published | ✅ [modes](#modes) + [identity variables](#identity-variables) tables |
| 2 | Published benchmark numbers | ✅ [Benchmarks](#benchmarks) (`BenchmarkMTLSHandshake`) |
| 3 | Documented known-limitations | ✅ [Limits](#limits) |
| 4 | Stable config/API contract (semver-guarded) | ✅ [compatibility policy](compatibility.md) (v1 tag at release) |
| 5 | Long-running soak test passed | ✅ soaked via Phase 2A 8h windows 2026-07-05 (2.12M req, 0% err) — [evidence](soak-evidence.md#2026-07-05--phase-2a-consolidated-burn-in-completed-local-8-hours-50-workers-all-features) |
| 6 | Runnable example + docs | ✅ [testdata/mtls.toml](../testdata/mtls.toml) + this doc |
| 7 | Security / threat note | ✅ [trust boundary](#identity-variables) + signature-verified CRL |
| 8 | Fuzzing where parsing is involved | n/a — CA/CRL parsing is stdlib `crypto/x509` (no custom parser) |
| 9 | Self-explanatory Console surface | ✅ Console **Status** panel reports *Mutual TLS (client certs)* active |

All GA criteria are satisfied.

## Build tags

