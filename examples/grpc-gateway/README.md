# gRPC → JSON transcoding gateway

Expose a **unary gRPC** service as a RESTful **JSON** API. Jul reads the
`google.api.http` annotations compiled into your `.proto`, translates each
incoming HTTP request into a gRPC call, and turns the protobuf reply back into
JSON — the same model as [grpc-gateway](https://github.com/grpc-ecosystem/grpc-gateway)
or Envoy's `grpc_json_transcoder`, but driven entirely by configuration.

This feature is gated behind the **`grpc` build tag**:

```bash
go build -tags grpc -o jul ../../cmd/jul
```

| Setting | Value |
| --- | --- |
| Front door | `http://localhost:8080/v1/...` (JSON over HTTP/1.1) |
| Backend | gRPC `echo.EchoService` on `127.0.0.1:50051` |
| Routing | from the `google.api.http` options in [`echo.proto`](echo.proto) |
| Schema source | a compiled descriptor set (`api.pb`) or gRPC server reflection |

## The service

[`echo.proto`](echo.proto) defines one method with two HTTP bindings:

```proto
service EchoService {
  rpc Echo(EchoRequest) returns (EchoReply) {
    option (google.api.http) = {
      post: "/v1/echo"
      body: "*"
      additional_bindings { get: "/v1/echo/{id}" }
    };
  }
}
```

- `POST /v1/echo` — the whole JSON request body (`body: "*"`) becomes the
  `EchoRequest` message.
- `GET /v1/echo/{id}` — `{id}` from the path populates `EchoRequest.id`;
  remaining query parameters (e.g. `?message=hi`) fill the other fields.

## 1. Generate the descriptor set

Jul needs the service schema as a compiled `FileDescriptorSet`. With `protoc`
installed (and the [googleapis](https://github.com/googleapis/googleapis)
protos on your include path for `google/api/annotations.proto`):

```bash
protoc \
  --include_imports \
  --descriptor_set_out=api.pb \
  --proto_path=. \
  --proto_path=/path/to/googleapis \
  echo.proto
```

No `protoc`? A tiny Go helper produces the same `api.pb` from the bundled
descriptors:

```bash
go run gen_descriptor.go        # writes ./api.pb
```

> Prefer zero build steps? Set `use_reflection = true` in
> [`jul.toml`](jul.toml) instead and drop `descriptor_set` — Jul will fetch the
> schema from the backend over gRPC server reflection at startup.

## 2. Validate and run

```bash
../../jul -check -config jul.toml      # schema check (any build)
../../jul -config jul.toml             # needs a -tags grpc binary
```

Start any gRPC server that implements `echo.EchoService` on
`127.0.0.1:50051` (the upstream named `echo-backend`), then call it as JSON:

```bash
# POST with a JSON body
curl -s -X POST http://localhost:8080/v1/echo \
  -H 'Content-Type: application/json' \
  -d '{"message":"hello"}'
# => {"message":"hello"}

# GET with a path variable
curl -s http://localhost:8080/v1/echo/42?message=hi
# => {"message":"..."}
```

## How it maps

| HTTP | gRPC |
| --- | --- |
| Request path → method | matched against each rule's path template (`{var}`, `*`, `**`, `:verb`) |
| Path variables | written onto the request message fields they name |
| `body: "*"` / `body: "field"` | the JSON body becomes the whole message or a sub-field |
| Query string | remaining params map onto request fields (when `body` isn't `*`) |
| `Authorization` header | forwarded to the backend as gRPC metadata |
| gRPC status code | translated to the matching HTTP status |
| protobuf reply | rendered as JSON (`preserve_proto_field_names` keeps `snake_case`) |

## Notes

- **MVP scope.** Unary methods only — streaming RPCs return
  `501 Not Implemented`. One backend address per target. These limits lift in a
  later release.
- **Descriptor vs. reflection.** `descriptor_set` is hermetic and works against
  any backend; `use_reflection` needs the backend to enable gRPC server
  reflection but removes the build step. Set exactly one.
- **TLS.** Set `tls = true` to dial the backend over TLS; the default is
  plaintext h2c, suitable for a co-located backend.
- **Observability.** Every call is counted in
  `jul_grpc_transcode_requests_total{method,code}`.
