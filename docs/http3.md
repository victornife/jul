# HTTP/3 over QUIC

> Feature ID: **Y1-11** · Build tag: `http3` · Since v1.25

> [!WARNING]
> **Current security correction:** the audited HTTP/3 TLS construction does not
> yet have proven parity with TCP TLS for the complete server-level
> `tls.client_auth` policy. Do not enable HTTP/3 on a listener whose security
> boundary requires handshake-level client certificates until #121 is merged,
> released and verified with real QUIC clients. See
> [current-product-truth.md](current-product-truth.md).

Jul.IA can serve HTTP/3 (QUIC) on a TLS-enabled server block. HTTP/3 uses UDP
alongside the TCP listener on the same authority and advertises availability via
`Alt-Svc`, allowing clients to attempt QUIC on a later request.

HTTP/3 requests use Jul.IA's current dynamic request-handler generation. The
open security issue is the completeness of server-level TLS client-auth policy on
the QUIC handshake, not stale routing after an ordinary handler reload.

## Usage

Build with HTTP/3 support:

```bash
go build -tags http3 -o jul ./cmd/jul
```

Enable HTTP/3 per server block (requires TLS):

```toml
[[servers]]
listen = ":443"
server_names = ["example.com"]

  [servers.tls]
  enabled     = true
  cert        = "certs/example.crt"
  key         = "certs/example.key"
  min_version = "1.3"

  [servers.http3]
  enabled         = true
  alt_svc_max_age = 86400
```

Do not combine this configuration with server-level required client certificates
until #121 is deployed.

## Behaviour matrix

### Protocol negotiation

| Scenario | TCP path | UDP/QUIC path | Notes |
| --- | --- | --- | --- |
| First request without cached Alt-Svc | HTTP/1.1 or HTTP/2 via ALPN | Usually not used | The client first learns the alternative from a TCP response |
| Later request with cached Alt-Svc | Available as fallback | HTTP/3 | Client policy decides whether to attempt QUIC |
| TLS ALPN | `h2`, `http/1.1` | `h3` | QUIC requires TLS 1.3 |
| Request handler | Current dynamic generation | Current dynamic generation | Ordinary route/config reload applies to both request paths |
| Certificate source | Startup static provider or running ACME manager | Same live provider seam | Static file changes on a retained listener still require restart; ACME renewal is separate |
| Server-level client auth | Enforced by the TCP TLS configuration | **Correction pending** | Complete parity is owned by #121 |

### Alt-Svc advertisement

| Header | Example | Meaning |
| --- | --- | --- |
| `Alt-Svc` | `h3=":443"; ma=86400` | HTTP/3 is available on the same port for the configured cache duration |

The current implementation constructs the advertised value with the listener.
Changing `alt_svc_max_age` therefore remains restart-bound until #161 replaces
that fixed value with live advertise/clear state. A client may continue trying a
cached alternative until it receives `Alt-Svc: clear` or the previous max-age
expires.

### Build-time behaviour

| Build | `http3` block | Result |
| --- | --- | --- |
| `-tags http3` | `enabled = true` | QUIC listener starts and Alt-Svc is advertised |
| `-tags http3` | disabled or absent | TCP only |
| no tag | `enabled = true` | Configuration/startup error requiring the `http3` tag |
| no tag | disabled or absent | TCP only |

### Configuration defaults

| Field | Default | Current lifecycle |
| --- | --- | --- |
| `enabled` | `false` | restart-required on a retained address; optional live transition is gated in #102 |
| `alt_svc_max_age` | `86400` seconds | restart-required until selected issue #161 lands |

## Current limitations and operational guidance

1. **Server-level mTLS parity is under correction.** Disable HTTP/3 on protected
   mTLS listeners until #121.
2. **No WebSocket over HTTP/3.** Use HTTP/1.1 or HTTP/2 for WebSocket upgrades.
3. **Listener enable/disable requires restart.** HTTP/3 uses a distinct UDP
   listener. Live activation/drain is gated architecture work in #102.
4. **Alt-Svc max-age is currently fixed with the listener.** #161 is the
   selected contained change for dynamic max-age and clear state; it does not
   implement live UDP-listener enable/disable.
5. **UDP can be blocked or rate-limited.** Clients must be able to fall back to
   TCP. Monitor both listener health and client-visible failure rates.
6. **Static certificate files are not hot-rotated on retained listeners.** Use a
   planned restart until #100 is released. ACME renewal follows the running
   ACME manager and is a different mechanism.

## Reload semantics

- Route, middleware and upstream changes publish a new dynamic handler
  generation used by subsequent HTTP/3 requests.
- `http3.enabled`, current Alt-Svc state, static certificate files and
  server-level client-auth material are not reclassified as hot by an ordinary
  handler reload.
- A mixed candidate containing a hot route change and a restart-bound HTTP/3 or
  TLS field must not be partially applied by the managed transaction.
- The authoritative target lifecycle will be generated from #89; do not infer
  it from the existence of a dynamic request handler.

## Benchmarks

Run:

```bash
go test -tags http3 -bench=. ./internal/server/
```

The in-tree throughput benchmark measures established-connection request cost.
It does not include the QUIC/TLS handshake and is not a production-capacity
claim. Compare environments and versions before using recorded numbers for
sizing.

## Threat notes

1. **Client-auth inconsistency.** Until #121, TCP and QUIC must not be assumed to
   enforce an identical server-level mTLS trust boundary.
2. **0-RTT/replay semantics.** Treat non-idempotent operations conservatively;
   use application idempotency keys and verify actual quic-go/server behavior
   for the shipped version.
3. **UDP amplification and resource pressure.** Deploy appropriate network-edge
   DDoS controls and monitor QUIC connection/resource state.
4. **Cached Alt-Svc.** Disabling or degrading HTTP/3 cannot immediately erase
   every client cache; clients learn new advertisement/clear state on later TCP
   responses.
5. **Shared certificate trust.** TCP and QUIC depend on the same server
   certificate authority and key material. Protect and rotate it through the
   documented startup/ACME mechanisms.

## Runnable example

`testdata/http3.toml` is a minimal HTTP/3 configuration. Validate the build and
configuration with:

```bash
go run -tags http3 ./cmd/jul -check -config testdata/http3.toml
```

A configuration check does not prove the live mTLS parity that #121 must test.
Use real TCP and QUIC clients with missing, valid and invalid client
certificates for that evidence.

## Maturity status

Historical HTTP/3 benchmark and soak evidence remains useful. The unconditional
GA claim is temporarily qualified because a security-relevant protocol-parity
finding is open. The feature should not be re-certified as fully GA for
mTLS-protected listeners until #121 closes with:

- equivalent TCP/QUIC client-auth construction;
- missing/valid/invalid client-certificate tests;
- SAN and CRL tests where configured;
- request identity and metrics parity;
- full-tag race and real-QUIC evidence;
- updated security, maturity and lifecycle documentation.

See [status.md](status.md), [mtls.md](mtls.md), the
[combined audit](audit/combined-audit-2026-08-03.md) and issue #121.
