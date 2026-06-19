# Native gRPC passthrough + h2c

Reverse-proxy **native gRPC** end to end over HTTP/2 — no transcoding. Jul
forwards each call unchanged, preserving trailers (`grpc-status` /
`grpc-message`) and flushing streaming frames as they arrive, while still
applying load balancing and passive health checks across backends.

This is distinct from the [gRPC → JSON transcoding](../grpc-gateway) gateway:
there Jul converts REST/JSON to gRPC; here it proxies real gRPC traffic.

The passthrough handler is gated behind the **`grpc` build tag**:

```bash
go build -tags grpc -o jul ../../cmd/jul
```

| Setting | Value |
| --- | --- |
| Front door (cleartext) | `h2c` on `:8095` for gRPC clients without TLS |
| Front door (TLS) | `:8443`, HTTP/2 negotiated via ALPN |
| Backend (h2c) | `proxy_pass = "http://grpc-backend"` → cleartext HTTP/2 |
| Backend (TLS) | `proxy_pass = "https://grpc-backend-tls"` → HTTP/2 over TLS |
| Trigger | `grpc = true` on a `proxy_pass` location |

See [`jul.toml`](jul.toml) for the full configuration.

## How it works

- **`grpc = true`** turns a `proxy_pass` location into a native gRPC
  passthrough. The `proxy_pass` scheme selects the backend transport:
  `http://` dials cleartext HTTP/2 (h2c), `https://` dials HTTP/2 over TLS.
- **`h2c = true`** on a plaintext `[[servers]]` block lets gRPC clients that
  connect without TLS speak prior-knowledge HTTP/2. On a TLS listener you don't
  need it — HTTP/2 is negotiated through ALPN.
- Response buffering is disabled, so a server-streaming call delivers each
  message immediately instead of stalling until the call completes.

## Run it

Build with the `grpc` tag and start a gRPC backend (for example any gRPC server
listening on `127.0.0.1:50051`), then:

```bash
go build -tags grpc -o jul ../../cmd/jul
./jul -config jul.toml
```

Point a gRPC client at `localhost:8095` (plaintext / h2c) or `localhost:8443`
(TLS). Unary and streaming calls are forwarded to the backend pool unchanged.

## Metrics

Each forwarded call increments `jul_grpc_proxy_streams_total`.
