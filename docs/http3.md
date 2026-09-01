# HTTP/3 over QUIC

> Feature ID: **Y1-11** · Build tag: `http3` · Since v1.25

Jul.IA can serve HTTP/3 (QUIC) on any TLS-enabled server block. HTTP/3 runs on
UDP alongside the existing TCP listener on the same address, shares the same TLS
certificate provider and server-level client-authentication policy, and
advertises itself to clients via an `Alt-Svc` response header so browsers can
upgrade on a later request.

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
  alt_svc_max_age = 86400   # 24 hours (default)
```

## Behaviour matrix

### Protocol negotiation

| Scenario | TCP path | UDP/QUIC path | Notes |
| --- | --- | --- | --- |
| First request (no prior Alt-Svc) | HTTP/1.1 or HTTP/2 via ALPN | Not used yet | Client needs an Alt-Svc hint to try h3 |
| Subsequent request (Alt-Svc cached) | May be skipped by client | HTTP/3 | Browser decides based on cached Alt-Svc |
| TLS ALPN | `h2`, `http/1.1` | `h3` | QUIC handshake includes h3 ALPN |
| Certificate source | `GetCertificate` callback | Same callback | ACME renewal applies to both; static-file rotation hot-applies to both through the same shared `dynamicCertProvider` callback (#100) |
| Server-level client authentication | Complete `tls.client_auth` policy | Same complete policy | `request`/`require`, CA verification, SAN allow-list, CRL checks and result hooks are equivalent |

### Alt-Svc advertisement

| Header | Example | Meaning |
| --- | --- | --- |
| `Alt-Svc` | `h3=":443"; ma=86400` | "HTTP/3 is available on the same port for 24 hours" |
| `Alt-Svc` | `clear` | "Stop trying HTTP/3 on this port" (RFC 7838 §3) |

The header is added to every response on an address whose HTTP/3 state is not
`none`. `ma` is the `alt_svc_max_age` config value. Only one advertisement is
sent per response; there is no multi-port or multi-protocol Alt-Svc list.

Each listener's advertisement is one of three states, held in an atomic
per-address value (`DynamicAltSvc`) rather than baked into the handler at
bind time:

| State | Header sent | When |
| --- | --- | --- |
| `none` | (no `Alt-Svc` header) | Before the QUIC listener's first successful `Activate`, and on any TLS address where HTTP/3 is not compiled in |
| `advertise` | `Alt-Svc: h3="..."; ma=<seconds>` | The QUIC listener is up and accepting connections |
| `clear` | `Alt-Svc: clear` | The QUIC accept loop ended unexpectedly at runtime, or (cold restart) HTTP/3 is compiled in but disabled for this address in the current process |

**`alt_svc_max_age` hot-reloads (#161).** Changing it takes effect on the next
response after a successful reload — `ReloadPlan.Publish()` refreshes every
retained listener's advertised `ma` in place, without rebinding the TCP or UDP
socket. `http3.enabled` itself is unaffected: starting or stopping the QUIC
listener still requires a restart (see
[reload-semantics.md](reload-semantics.md)), because whether a UDP socket
exists at all is a bind-time decision.

**Live failure handling.** If the QUIC accept loop exits for any reason other
than an intentional shutdown, the listener is marked degraded, the
advertisement flips to `clear` on the very next TCP/h2 response, and HTTP/3
stays off for that address for the rest of the process's life — Jul does not
attempt to reopen the QUIC socket. TCP/h2 traffic on the same address is
unaffected.

A client that already cached an `advertise` header from before a `clear` may
still attempt HTTP/3 first and fall back to TCP per its own retry policy;
Jul cannot force an immediate re-check. See
[known-limitations.md](known-limitations.md) for this and the
`alt_svc_max_age = 0` schema caveat.

### Build-time behaviour

| Build | `http3` block in config | Result |
| --- | --- | --- |
| `-tags http3` | `enabled = true` | QUIC listener starts, Alt-Svc advertised |
| `-tags http3` | `enabled = false` or absent | No QUIC listener |
| no tag | `enabled = true` | Startup error: "rebuild with -tags http3" |
| no tag | `enabled = false` or absent | No error; TCP only |

### Configuration defaults

| Field | Default | Range |
| --- | --- | --- |
| `enabled` | `false` | boolean |
| `alt_svc_max_age` | `86400` (24h) | positive integer, seconds |

## TLS and mutual-TLS parity

HTTP/3 is created from a clone of the fully prepared TLS policy used by the
sibling TCP listener. Jul then applies QUIC's mandatory TLS 1.3 floor and h3
ALPN without discarding the rest of the policy. The QUIC handshake therefore
preserves:

- certificate selection through the shared dynamic provider;
- `tls.client_auth.mode` (`none`, `request`, or `require`);
- the configured client CA pool;
- SAN allow-list and CRL verification;
- the mutual-TLS result hook and verified peer certificate exposed to request
  middleware.

Existing HTTP/3 connections are governed by the policy used for their TLS
handshake. Listener-level TLS and mTLS settings remain restart-bound today, so a
policy change applies after the planned restart and to subsequent handshakes.

## Known limitations

1. **No WebSocket upgrade over HTTP/3 — and none over HTTP/2 either.** Jul supports
   the HTTP/1.1 `Upgrade` mechanism only. WebSocket over HTTP/2 and HTTP/3 uses
   extended `CONNECT` ([RFC 8441][rfc8441] / RFC 9220), which Jul does not implement.
   Go's bundled HTTP/2 server keeps extended `CONNECT` behind `GODEBUG=http2xconnect=1`
   for a reason that applies directly here: advertising it makes browsers *stop*
   sending HTTP/1.1 `Upgrade` and start sending extended `CONNECT`, which then fails
   against a server whose WebSocket path does not implement it. A client that connects
   over HTTP/3 and requests a WebSocket upgrade will receive a `400 Bad Request` or the
   upgrade will fail. Browsers fall back to an HTTP/1.1 connection for the WebSocket,
   so nothing is unreachable.

[rfc8441]: https://www.rfc-editor.org/rfc/rfc8441.html

2. **HTTP/3 settings require a restart.** Like tracing, the QUIC listener is
   built once at bind time. Changing `[servers.http3]` after startup has no
   effect until the process is restarted.

3. **UDP port sharing with TCP.** The QUIC listener binds the same address as
   the TCP listener but over UDP. If another process already holds the UDP
   socket, HTTP/3 startup fails with a clear "address already in use" error.
   This is rare in practice because most services bind TCP only.

4. **Static certificate-file replacement remains restart-bound.** HTTP/3 and
   TCP share the same certificate callback, but changing a configured static
   `cert`/`key` file is not published to that callback until the dedicated
   certificate-rotation lifecycle is implemented. ACME renewal continues while
   the process is running.

## Benchmarks

Run with `go test -tags http3 -bench=. ./internal/server/`.

| Benchmark | Scenario | ns/op | allocs/op | bytes/op |
| --- | --- | --- | --- | --- |
| `BenchmarkHTTP3Throughput` | Single QUIC connection, repeated GET / 204 | ~259 000 | 200 | 13 892 |

This measures the per-request overhead of an established HTTP/3 connection,
including stream creation, header compression (QPACK), and response framing. The
QUIC handshake and TLS 1.3 negotiation happen once in setup and are not part of
the measured path. For comparison, the equivalent HTTP/1.1 benchmark on the same
handler is ~3 500 ns/op; QUIC adds ~75× overhead due to crypto framing and the
user-space UDP stack. In practice the benefit is tail-latency reduction on lossy
or high-RTT paths, not raw throughput on localhost.

## Threat note

HTTP/3 shifts the transport from TCP to UDP and from TLS 1.2 to TLS 1.3, which
changes the attack surface:

1. **0-RTT replay vulnerability.** QUIC supports 0-RTT resumption for
   reconnecting clients. Jul.IA delegates QUIC handshake semantics to quic-go,
   which enables 0-RTT by default. A replayed 0-RTT packet may reach the
   handler twice, causing duplicate side effects for non-idempotent requests.
   Counter-measures: configure upstreams to be idempotent where possible; do
   not rely on HTTP/3 for critical mutation endpoints without additional
   idempotency keys; monitor for duplicate trace ids.

2. **UDP amplification / reflection.** QUIC's handshake packets are larger than
   the initiating client hello, creating a potential amplification vector for
   DDoS. Jul.IA limits the QUIC receive buffer to standard sizes via the kernel;
   the quic-go library additionally validates address tokens. Counter-measures:
   deploy behind a UDP-aware DDoS scrubber; rate-limit UDP per source at the
   network edge; do not expose HTTP/3 directly to the open internet without
   upstream filtering.

3. **UDP socket exhaustion.** Each QUIC connection consumes one UDP socket
   (shared across all connections on the same listener port) plus state in the
   quic-go connection manager. A SYN-flood equivalent for QUIC (INIT packets
   without completing the handshake) can exhaust connection table memory.
   Counter-measures: the kernel UDP socket backlog provides basic backpressure;
   deploy behind a CDN or load balancer that terminates QUIC and proxies over
   TCP to Jul.IA for untrusted traffic.

4. **Shared TLS policy affects both paths.** HTTP/3 reuses the complete listener
   TLS policy, including certificate selection and server-level mTLS. A
   compromised server certificate or over-broad client CA/SAN policy therefore
   affects both transports simultaneously. Counter-measures: rotate credentials
   promptly, keep client trust roots and SAN allow-lists narrow, maintain CRLs,
   and monitor TCP and QUIC handshake outcomes.

5. **Alt-Svc as a tracking vector.** A malicious middlebox or CDN that injects
   a forged `Alt-Svc: h3=...` header can redirect clients to an attacker-
   controlled QUIC endpoint while keeping the same origin hostname. Because
   browsers cache Alt-Svc aggressively, the redirection persists.
   Counter-measures: serve Alt-Svc only over HTTPS (which the server already
   does); use HSTS with `includeSubDomains` so downgrade is blocked; monitor
   DNS for unexpected A/AAAA records matching the advertised port.

## Runnable example

`testdata/http3.toml` is a minimal TLS + HTTP/3 config (self-signed cert) that
you can validate with a QUIC-capable client such as curl:

```bash
# Generate a self-signed cert
go run -tags http3 ./cmd/jul -check -config testdata/http3.toml
```

See `internal/server/http3_test.go` and `internal/server/http3_mtls_test.go` for
real QUIC tests covering request/response behavior and client-authentication
parity.

## GA status

| Criterion | Status | Evidence |
| --- | --- | --- |
| 1 — Conformance / behaviour matrix | ✅ | Protocol negotiation + Alt-Svc + build-time matrix above |
| 2 — Published benchmark numbers | ✅ | `BenchmarkHTTP3Throughput` in `internal/server/http3_test.go` |
| 3 — Known-limitations list | ✅ | 4-item limitation list above |
| 4 — Semver-guarded config/API contract | ✅ | v1 config freeze (cross-cutting) |
| 5 — Long-running soak test | ✅ | **8h isolated Linux soak** 2026-07-13 (55,302,486 req, 0% err, 100% success) — [evidence](soak-evidence.md#2026-07-13--http3-over-quic-8h-isolated-soak-linux-completed) |
| 6 — Runnable example + docs | ✅ | `testdata/http3.toml` + this doc |
| 7 — Security / threat note | ✅ | 5-row threat note (0-RTT replay, amplification, UDP exhaustion, shared TLS policy, Alt-Svc tracking) |
| 8 — Fuzzing where parsing is involved | n/a | No custom parser (delegates to quic-go) |
| 9 — Self-explanatory Console surface | ✅ | Status row shows HTTP/3 compiled state + active listener count |
