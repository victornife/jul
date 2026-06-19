# Native gRPC passthrough

Jul.IA can reverse-proxy **native gRPC** traffic end to end over HTTP/2, without
converting it to anything. Set `grpc = true` on a `proxy_pass` location and the
call is forwarded unchanged — trailers preserved, streaming frames flushed
immediately — while the upstream pool's load balancing and passive health checks
still apply.

This is different from gRPC ↔ JSON transcoding (`grpc_transcode`): there
Jul.IA translates REST/JSON into gRPC; here it proxies real gRPC traffic
untouched. Both live behind the same `grpc` build tag.

```bash
go build -tags grpc -o jul ./cmd/jul
```

## Quick start

```toml
# Plaintext front door; h2c accepts gRPC clients that connect without TLS.
[[servers]]
listen = "0.0.0.0:8095"
h2c = true

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  proxy_pass = "http://grpc-backend"   # http:// => cleartext HTTP/2 (h2c)
  grpc = true

[[upstreams]]
name = "grpc-backend"
strategy = "round_robin"
servers = ["127.0.0.1:50051", "127.0.0.1:50052"]
```

Point a gRPC client at `host:8095`. Every method on every service is forwarded
to the backend pool; you do not enumerate methods or supply a descriptor.

## How it works

### End-to-end HTTP/2

gRPC runs over HTTP/2 and relies on **trailers** to carry the final status
(`grpc-status`, `grpc-message`). Jul.IA forwards the request over an HTTP/2
transport and copies trailers back to the client, and it disables response
buffering so each frame of a streaming call is delivered as soon as it arrives.
All four call kinds work: unary, server-streaming, client-streaming, and
bidirectional.

### Choosing the backend transport

The `proxy_pass` scheme selects how Jul.IA dials the backend:

| `proxy_pass` | Backend transport |
| --- | --- |
| `http://…` | Cleartext HTTP/2 (h2c) — prior knowledge, no TLS |
| `https://…` | HTTP/2 over TLS (certificate verified against the system roots) |

The backend may be a named `[[upstreams]]` pool or a literal `host:port`. A
literal target becomes a single-backend pool.

### Inbound h2c

gRPC clients frequently connect **without** TLS inside a cluster. A plaintext
HTTP listener speaks HTTP/1.1 by default, which gRPC cannot use. Set
`h2c = true` on the `[[servers]]` block to also accept prior-knowledge cleartext
HTTP/2 on that listener:

```toml
[[servers]]
listen = "0.0.0.0:8095"
h2c = true
```

On a **TLS** listener you do not need `h2c` — HTTP/2 is negotiated through ALPN
during the handshake. Inbound h2c uses the Go standard library's HTTP/2 server
support, so it adds no dependency.

### Terminating TLS at the edge

A common topology terminates TLS at Jul.IA and reaches the backend over h2c:

```toml
[[servers]]
listen = "0.0.0.0:8443"
server_names = ["grpc.example.com"]

  [servers.tls]
  cert = "/etc/jul/tls/grpc.crt"
  key  = "/etc/jul/tls/grpc.key"

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  proxy_pass = "http://grpc-backend"   # h2c to the backend
  grpc = true
```

Clients speak gRPC over TLS (HTTP/2 via ALPN); Jul.IA forwards to the backend
over cleartext HTTP/2.

## Load balancing & health

Because the backend is an ordinary `[[upstreams]]` pool, the pool's strategy
(`round_robin`, `weighted_round_robin`, `least_conn`) and passive health checks
(`max_fails` / `fail_timeout`) apply, and `least_conn` reflects the full
lifetime of a streaming call. gRPC streams are **not replayable**, so Jul.IA
does not retry a call against another backend once it has started; a connection
failure surfaces to the client as a gateway error and the backend is marked
failed for subsequent calls.

## Routing alongside HTTP

A `grpc = true` location is matched like any other location, so you can host
gRPC and plain HTTP on the same listener by path prefix — for example route
`/grpc.health.v1.Health/` to a gRPC backend and everything else to a web app.
Method routing within a service is transparent: gRPC encodes the method in the
request path, which Jul.IA forwards verbatim.

## Metrics

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `jul_grpc_proxy_streams_total` | counter | — | gRPC calls forwarded (one per call, including each streaming call) |

## gRPC passthrough vs. transcoding

| | Passthrough (`grpc = true`) | Transcoding (`grpc_transcode`) |
| --- | --- | --- |
| Client speaks | gRPC | REST / JSON |
| Backend speaks | gRPC | gRPC |
| Payload | Forwarded unchanged | Converted JSON ↔ protobuf |
| Schema needed | No | Yes (descriptor set or reflection) |
| Streaming | All four kinds, transparently | Server/client/bidi via NDJSON or SSE |
| Use when | Load-balancing native gRPC | Exposing gRPC to JSON clients |

## See also

- [examples/grpc-proxy](../examples/grpc-proxy) — runnable sample config
- [testdata/grpc-proxy.toml](../testdata/grpc-proxy.toml) — `jul -check` sample
- [examples/grpc-gateway](../examples/grpc-gateway) — the REST/JSON transcoding gateway
