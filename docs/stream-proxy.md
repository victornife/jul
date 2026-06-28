# L4 stream proxy

Jul.IA can reverse-proxy raw **TCP** and **UDP** connections in addition to its
HTTP listeners. A `[[stream]]` block forwards bytes at layer 4 — no HTTP parsing
— which is what you want for databases (PostgreSQL, MySQL, Redis), message
brokers (Kafka, AMQP), DNS, game servers, or any TLS service you want to route by
SNI without terminating.

Stream support is opt-in behind the `stream` build tag:

```bash
go build -tags stream -o jul ./cmd/jul
```

A binary built without the tag still parses a `[[stream]]` table but rejects it
at startup if it is populated, so misconfiguration fails loudly rather than
silently dropping listeners.

## Contents

- [Concepts](#concepts)
- [Configuration](#configuration)
- [TCP proxying](#tcp-proxying)
- [SNI routing (TLS passthrough)](#sni-routing-tls-passthrough)
- [PROXY protocol](#proxy-protocol)
- [UDP proxying](#udp-proxying)
- [Load balancing and health](#load-balancing-and-health)
- [Hot reload](#hot-reload)
- [Metrics](#metrics)
- [Limits in v1](#limits-in-v1)

## Concepts

- **Layer 4, not 7.** Bytes are relayed unchanged in both directions. Jul.IA
  never looks inside the payload except, optionally, to read the TLS SNI host
  from the ClientHello for routing — and even then it does not consume or modify
  the handshake.
- **Backends are upstreams.** A stream's backend is a named `[[upstreams]]` pool
  (so load balancing and passive health apply) or a literal `host:port` (which
  becomes a single-backend pool). The upstream `scheme` is irrelevant at L4 —
  only the backend address is dialed.
- **Runs alongside HTTP.** Stream listeners live next to the HTTP server in the
  same process and share its lifecycle: they start at boot, reload on SIGHUP /
  file-watch / admin reload, and drain on shutdown.

## Configuration

Each `[[stream]]` block is one listener.

| Key | Type | Default | Description |
| --- | ---- | ------- | ----------- |
| `listen` | string | — | Bind address `host:port` (**required**) |
| `protocol` | string | `tcp` | `tcp` or `udp` |
| `proxy_pass` | string | — | Default backend: a named upstream or a literal `host:port` |
| `sni_routes` | table | — | TLS server-name → backend map (TCP only); enables SNI inspection |
| `tls_passthrough` | bool | `false` | Informational; implied when `sni_routes` is set |
| `proxy_protocol` | string | `""` | PROXY-protocol mode (TCP only): `""`, `in`, `out`, `both` |
| `connect_timeout` | duration | `10s` | Backend dial timeout |
| `idle_timeout` | duration | `5m` | Close a relayed connection / UDP session after this idle period |

Provide at least one of `proxy_pass` or `sni_routes`. A `"*"` key in `sni_routes`
is a catch-all that takes precedence over `proxy_pass`. UDP listeners are plain
relays: `sni_routes`, `tls_passthrough`, and `proxy_protocol` are TCP-only and
are rejected on a UDP block during validation.

## TCP proxying

The simplest stream load-balances TCP to an upstream pool:

```toml
[[upstreams]]
name = "postgres_pool"
strategy = "least_conn"
  [[upstreams.servers]]
  address = "10.0.0.11:5432"
  [[upstreams.servers]]
  address = "10.0.0.12:5432"

[[stream]]
listen = "0.0.0.0:5432"
proxy_pass = "postgres_pool"
connect_timeout = "5s"
idle_timeout = "1h"          # long-lived DB connections
```

A literal target skips the upstream table for one-off backends:

```toml
[[stream]]
listen = "0.0.0.0:6379"
proxy_pass = "10.0.0.20:6379"
```

Jul.IA accepts the client, dials a backend (retrying the next healthy one on a
dial error), and relays both directions until either side closes or the
`idle_timeout` elapses with no traffic.

## SNI routing (TLS passthrough)

On a TCP listener with `sni_routes`, Jul.IA peeks the TLS ClientHello **without
consuming it**, reads the SNI host, and forwards the still-intact handshake to
the matching backend. TLS is **never terminated**, so certificates and private
keys stay on the backends — Jul.IA only steers the connection.

```toml
[[stream]]
listen = "0.0.0.0:443"
  [stream.sni_routes]
  "api.example.com" = "api_pool"        # named upstream
  "db.example.com"  = "10.0.0.7:8443"   # literal host:port
  "*"               = "default_pool"    # catch-all for unmatched names
```

Routing rules:

1. If the SNI host matches a key exactly, that backend is used.
2. Otherwise the `"*"` catch-all is used, if present.
3. Otherwise `proxy_pass` is used, if set.
4. If none apply, the connection is closed.

Because this is passthrough, the backend sees the original ClientHello and
performs the TLS handshake itself. This is the right tool for routing many TLS
services through one `:443` without sharing keys with the proxy.

## PROXY protocol

Layer-4 proxying hides the client's address from the backend (the backend sees
the proxy's address). The HAProxy **PROXY protocol** carries the real client and
destination addresses across the hop. Jul.IA implements both v1 (text) and v2
(binary) in-house — no extra dependency.

```toml
# Receive a PROXY header from an upstream L4 balancer, and forward one onward.
[[stream]]
listen = "0.0.0.0:5432"
proxy_pass = "postgres_pool"
proxy_protocol = "both"
```

| Mode | Behaviour |
| ---- | --------- |
| `""` | Off (default) |
| `in` | Parse a PROXY v1/v2 header from the client and use the advertised client address |
| `out` | Prepend a PROXY v2 header (with the real client address) to the backend connection |
| `both` | Parse inbound **and** emit outbound |

Enable `in` only when the client is a trusted proxy that always prepends a
header — a raw client that does not will fail to parse. Enable `out` only when
the backend understands the PROXY protocol (e.g. NGINX `proxy_protocol`,
PostgreSQL behind a PROXY-aware pooler, etc.).

## UDP proxying

Set `protocol = "udp"` for a connectionless relay. Jul.IA keeps a per-client
**session** keyed by the source address: the first datagram from a client dials a
backend and starts forwarding replies back; the session is reaped after
`idle_timeout` with no traffic in either direction.

```toml
[[stream]]
listen = "0.0.0.0:53"
protocol = "udp"
proxy_pass = "dns_pool"
idle_timeout = "30s"
```

UDP is a plain relay: SNI routing, TLS passthrough, and the PROXY protocol do not
apply and are rejected on a UDP block.

## Load balancing and health

When the backend is a named upstream, the pool's load-balancing strategy and
passive health checking apply exactly as they do for HTTP:

```toml
[[upstreams]]
name = "redis_pool"
strategy = "round_robin"
max_fails = 3
fail_timeout = "10s"
  [[upstreams.servers]]
  address = "10.0.0.31:6379"
  weight = 2
  [[upstreams.servers]]
  address = "10.0.0.32:6379"
```

On a dial failure Jul.IA marks the backend failed and retries the next healthy
one; a successful dial clears the failure counter. (Active `[upstreams.health_check]`
probes are HTTP/TCP oriented; for L4 streams, passive health from dial outcomes
is what gates backends.)

## Hot reload

`[[stream]]` participates in zero-downtime reload. On every successful config
reload Jul.IA:

1. Builds the new route set (validating every backend) **before** touching live
   listeners — a bad target fails the reload and the old config keeps serving.
2. Binds any newly added listen addresses, rolling back if any bind fails.
3. Swaps routes atomically on surviving listeners, starts newly bound ones, and
   drains then stops removed ones.

The HTTP listeners are untouched throughout. Existing connections on a listener
whose route changed continue on their original backend; new connections use the
new route.

Every per-listener setting — `proxy_pass`, `sni_routes`, `proxy_protocol`,
`connect_timeout`, and `idle_timeout` — lives in that swapped route, so editing
one takes effect on the next connection **without a restart**. Unlike the HTTP
listeners, no `[[stream]]` setting is frozen at bind time. The only bind-time
properties are `protocol` (TCP vs UDP) and `listen`; changing either identifies a
*different* listener, so the reload binds the new socket and drains the old one
rather than mutating the running one. See
[Configuration reload semantics](reload-semantics.md#l4-stream-listeners-are-not-affected).

### Apply-time truthfulness

When a reload is triggered by the **admin console / `/api/config/apply`**, the
write path additionally **bind-probes every newly introduced stream listen
address before the configuration is written to disk** — symmetric with the HTTP
listener probe. The stream config is also dry-run-built earlier in the same
preflight. Together this means an apply that adds an unbindable `[[stream]]` port
(already in use, privileged, or invalid) is **rejected up front** with an error,
rather than being recorded as applied while the asynchronous reload's bind fails
and surfaces only in the Overview stream-status panel. See
[Configuration reload semantics](reload-semantics.md).

## Metrics

| Metric | Type | Labels | Meaning |
| ------ | ---- | ------ | ------- |
| `jul_stream_active_conns` | gauge | `proto` | Currently open TCP connections / UDP sessions |
| `jul_stream_bytes_total` | counter | `proto`, `direction` | Bytes relayed (`direction` is `up` to the backend or `down` to the client) |

## Limits in v1

- **No TLS termination on streams.** Stream listeners forward TLS unmodified;
  there is no per-stream `cert`/`key`. Terminate TLS with an HTTP `[[servers]]`
  block, or pass it through with `sni_routes`.
- **Passive health only.** L4 backends are gated by dial outcomes; there is no
  active L4 probe in v1.
- **Pool state resets on reload.** A reload rebuilds pools, so passive health
  counters start fresh for the new generation.
