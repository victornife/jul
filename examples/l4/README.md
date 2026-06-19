# L4 stream proxy (TCP / UDP)

Reverse-proxy raw **TCP** and **UDP** connections with [`[[stream]]`](../../docs/stream-proxy.md)
blocks — databases, brokers, DNS, or TLS services routed by SNI without
terminating. This feature is gated behind the **`stream` build tag**:

```bash
go build -tags stream -o jul ../../cmd/jul
```

| Setting | Value |
| --- | --- |
| TCP load balancing | `:5432` → `postgres_pool` (least-conn over two backends) |
| TLS SNI routing | `:8443` steered by ClientHello SNI, TLS passed through untouched |
| UDP relay | `:5353` → `dns_pool` |
| PROXY protocol | `:6379` emits a PROXY v2 header to the backend |

See [`jul.toml`](jul.toml) for the full configuration.

## Run it

Validate the config (no build tag needed for linting):

```bash
../../jul lint -config jul.toml
```

Build a stream-enabled binary and start it:

```bash
go build -tags stream -o jul ../../cmd/jul
./jul --config jul.toml
```

## TCP load balancing

```toml
[[stream]]
listen = "0.0.0.0:5432"
proxy_pass = "postgres_pool"
connect_timeout = "5s"
idle_timeout = "1h"
```

Jul.IA accepts each TCP connection, picks a backend from the pool (applying its
load-balancing strategy and passive health), and relays bytes both ways until
either side closes or the connection is idle past `idle_timeout`.

## TLS SNI routing (passthrough)

```toml
[[stream]]
listen = "0.0.0.0:8443"
  [stream.sni_routes]
  "api.example.com" = "api_pool"
  "db.example.com"  = "10.0.0.7:8443"
  "*"               = "default_pool"
```

Jul.IA peeks the TLS ClientHello, reads the SNI host, and forwards the intact
handshake to the matching backend. **TLS is never terminated** — keys stay on
the backends. A `"*"` entry catches unmatched names.

## UDP relay

```toml
[[stream]]
listen = "0.0.0.0:5353"
protocol = "udp"
proxy_pass = "dns_pool"
idle_timeout = "30s"
```

Each client source address gets a session that dials a backend and forwards
replies; idle sessions are reaped after `idle_timeout`.

## PROXY protocol

```toml
[[stream]]
listen = "0.0.0.0:6379"
proxy_pass = "redis_pool"
proxy_protocol = "out"   # "in", "out", or "both"
```

Preserves the real client address across the proxy hop using the HAProxy PROXY
protocol (v1/v2). Use `out` when the backend understands it; `in` when the
client is a trusted proxy that prepends a header.
