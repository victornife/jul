# REST/JSON → gRPC transcoding

Jul.IA can expose a gRPC service to REST/JSON clients: it reads the
`google.api.http` annotations carried in a service's protobuf descriptors,
translates each HTTP request into a gRPC call, and renders the protobuf reply
back as JSON. Set `grpc_transcode` on a location and Jul.IA builds the route
table from the descriptors; you do not hand-write the mapping.

This is the inverse of [native gRPC passthrough](grpc-proxy.md): there a gRPC
client is load-balanced to a gRPC backend unchanged; here a **JSON** client
talks to a **gRPC** backend. Both live behind the `grpc` build tag.

```bash
go build -tags grpc -o jul ./cmd/jul
```

## Quick start

```toml
[[servers]]
listen = "127.0.0.1:8080"

  [[servers.locations]]
  match = { type = "prefix", path = "/v1/" }
    [servers.locations.grpc_transcode]
    target         = "echo-backend"   # upstream name or literal host:port
    descriptor_set = "./api.pb"        # protoc FileDescriptorSet
    # use_reflection = true            # alternative: gRPC server reflection
    # tls            = false           # dial backend over TLS (default h2c)
    # streaming      = false           # enable server/client/bidi streaming
    # stream_mode    = "ndjson"        # "ndjson" (default) or "sse"
    # preserve_proto_field_names = false
    # max_message_size = "4MiB"

[[upstreams]]
name = "echo-backend"
servers = ["127.0.0.1:50051"]
```

A runnable end-to-end sample is in
[examples/grpc-gateway](../examples/grpc-gateway).

## Configuration

| Key | Meaning | Default |
| --- | --- | --- |
| `target` | gRPC backend: an `[[upstreams]]` name or a literal `host:port` | — (required) |
| `descriptor_set` | Path to a `protoc` `FileDescriptorSet` (`--descriptor_set_out` with `--include_imports`) | — |
| `use_reflection` | Fetch descriptors from the backend via gRPC server reflection instead of a file | `false` |
| `tls` | Dial the backend over TLS; otherwise cleartext HTTP/2 (h2c) | `false` |
| `streaming` | Transcode streaming methods; when `false` they return `501` | `false` |
| `stream_mode` | Framing for streamed responses: `ndjson` or `sse` | `ndjson` |
| `preserve_proto_field_names` | Emit original proto field names instead of `lowerCamelCase` | `false` |
| `max_message_size` | Cap on a single JSON request frame or gRPC reply | `4MiB` |

Exactly one descriptor source — `descriptor_set` **or** `use_reflection` — must
be set.

## Conformance matrix

What the transcoder supports today, enumerated so the boundary is explicit
(ADR [0003](adr/0003-maturity-and-ga.md) GA criterion 1).

### HTTP method mapping

| `google.api.http` pattern | HTTP method | Supported |
| --- | --- | --- |
| `get` | GET | ✅ |
| `put` | PUT | ✅ |
| `post` | POST | ✅ |
| `delete` | DELETE | ✅ |
| `patch` | PATCH | ✅ |
| `custom { kind, path }` | the custom kind (upper-cased) | ✅ |
| `additional_bindings` | each additional binding becomes its own route | ✅ |

### Path templates

| Template feature | Example | Supported |
| --- | --- | --- |
| Literal segments | `/v1/echo` | ✅ |
| Single-segment wildcard | `/v1/*/info` | ✅ |
| Named single capture | `/v1/echo/{id}` | ✅ |
| Nested field capture | `/v1/{obj.id}` | ✅ |
| Sub-template capture | `/v1/{name=shelves/*/books/*}` | ✅ |
| Trailing multi-segment | `/v1/{name=**}`, `/v1/**` | ✅ |
| Trailing verb | `/v1/echo:watch` | ✅ |

A captured path variable is converted to its target field's type and **overrides**
any value the body supplied for the same field.

### Request body & query mapping

| Mapping | Behavior | Supported |
| --- | --- | --- |
| `body: "*"` | The whole JSON body decodes into the request message | ✅ |
| `body: "<field>"` | The JSON body decodes into that singular message field | ✅ |
| no `body` | Request is built from path + query only | ✅ |
| Query parameters | Fill fields not already set by the path (when `body != "*"`); dotted paths address nested fields; repeated scalars append | ✅ |
| Unmatched query parameter | Ignored leniently (no error) | ✅ |
| Map field from path/query | Not settable from a path or query value | ❌ |

### Scalar / enum types (path & query values)

`bool`, `int32`/`sint32`/`sfixed32`, `int64`/`sint64`/`sfixed64`,
`uint32`/`fixed32`, `uint64`/`fixed64`, `float`, `double`, `string`,
`bytes` (standard base64), and `enum` (by value name **or** number) are all
converted. The full proto3 JSON surface — nested messages, well-known types,
`oneof`, maps in the **body** — is handled by `protojson`.

### Streaming (`streaming = true`)

| Method kind | Request | Response | Supported |
| --- | --- | --- | --- |
| Unary | JSON object | JSON object | ✅ (always) |
| Server-streaming | JSON object | framed: NDJSON or SSE | ✅ |
| Client-streaming | JSON array **or** NDJSON frames | single JSON object | ✅ |
| Bidirectional | JSON array **or** NDJSON frames | framed: NDJSON or SSE | ✅ |

A JSON-array request body must be the **only** top-level value: trailing tokens
after the closing `]` are rejected with `400`. A unary request body larger than
`max_message_size` is rejected with `413` rather than silently truncated.

With `streaming = false`, a request to a streaming method returns
`501 Not Implemented`. A terminal gRPC error that arrives **after** the first
response frame is delivered as an in-band error frame (`{"error":…}` for NDJSON,
`event: error` for SSE), since the HTTP status is already `200`.

### Backend dialing, metadata & errors

| Aspect | Behavior |
| --- | --- |
| Backend transport | h2c (default) or TLS (`tls = true`, verified against system roots) |
| Backend selection | Per-request pick from the resolved upstream pool (load-balanced and health-checked) |
| Connection reuse | gRPC connections are cached per backend address and shared across requests |
| Inbound metadata | `Authorization` and any `Grpc-Metadata-<key>` header → gRPC metadata |
| Deadline | The HTTP request context (and its deadline) is propagated to the call |
| Error mapping | gRPC status code → HTTP status (`InvalidArgument`→400, `NotFound`→404, `PermissionDenied`→403, `Unauthenticated`→401, `ResourceExhausted`→429, `Unavailable`→503, `DeadlineExceeded`→504, …); oversize body → 413; malformed/trailing JSON → 400 |
| Error body | RFC 7807 `application/problem+json` (`status`, `title`, `detail`) |
| Response encoding | `protojson` with unpopulated fields emitted; field-name casing per `preserve_proto_field_names` |

### Retry

Transcoded **unary** calls are retried; **streaming is not**, and neither is native gRPC passthrough.

The asymmetry is not an omission. A unary call's request is an in-memory message, so replaying it
costs nothing and cannot fail, and its response is fully decoded before a byte is written — the whole
call is inside the retry boundary. A streaming call has already written framing by the time it can
fail, and replaying it would deliver a second stream into a client consuming the first. Native gRPC
is proxied as opaque HTTP/2, where Jul cannot frame messages or even tell unary from streaming.

A retry needs **both** halves to agree:

| Half | Rule |
| --- | --- |
| Is the call safe to repeat? | `GET`/`HEAD`/`OPTIONS`/`TRACE`/`PUT`/`DELETE`, **or** the method declares `idempotency_level = NO_SIDE_EFFECTS` or `IDEMPOTENT` |
| Did the backend fail to take it? | `UNAVAILABLE`, or a failure to establish the connection |

The proto annotation is consulted because the HTTP binding alone is too coarse: a method mapped to
`POST` purely because it takes a request body may still be declared `NO_SIDE_EFFECTS`, and ignoring
that would waste an explicit statement by the API's author.

```proto
rpc GetItem(GetItemRequest) returns (Item) {
  option idempotency_level = NO_SIDE_EFFECTS;
  option (google.api.http) = { post: "/v1/items:get" body: "*" };
}
```

**The default, `IDEMPOTENCY_UNKNOWN`, is never retried** — silence is not a promise, and the method
most likely to be silent about it is the one that charges a card.

Every other status code is terminal. An `InvalidArgument` is the application's answer, not a failure
to reach it; asking a second backend the same question returns the same answer more expensively while
doubling load on a service already saying no. The failed backend is excluded from re-selection, and
the attempt cap, deadline, backoff and budget are the same
[`[upstreams.resilience]` controls](upstreams.md#retry) the HTTP proxy uses, settable per location.

## Known limitations

Documented gaps (ADR [0003](adr/0003-maturity-and-ga.md) GA criterion 3). None
is a correctness bug; each is a bounded scope decision.

- **No `response_body` selection.** The whole reply message is rendered; mapping
  a single response field to the HTTP body (the `HttpRule.response_body` option)
  is not implemented.
- **Maps not settable from path/query.** Map fields can only be populated via the
  JSON body, not from a path variable or query parameter.
- **Descriptors are loaded once.** The route table is built at construction (file
  read or one reflection fetch) and on config reload; it does not hot-refresh
  when a backend's reflection schema changes underneath a running config.
- **No mTLS to the backend for transcoding.** TLS dialing verifies the server
  against system roots; presenting a client certificate to the backend is not
  yet configurable here (passthrough topologies can terminate/originate TLS at
  the edge).
- **HTTP framing only for streams.** Streamed responses are NDJSON or SSE over
  HTTP/1.1+; there is no gRPC-Web or WebSocket bridge.
- **RPCs may be cut at the retired-connection grace boundary.**
  When the upstream pool changes (e.g. service discovery), removed backend
  connections are kept for 30 seconds so in-flight RPCs can drain. RPCs that
  outlast that grace period may be interrupted when the retired connection is
  closed; this primarily affects long-lived streams and unusually long unary
  calls. Clients should be prepared to reconnect.
- **No per-call transform hooks.** Translation is `protojson` in/out; there is no
  scriptable request/response rewriting in the transcoding path.

## Security & threat notes

GA criterion 7. The transcoding path is a parser and a fan-out point, so it is
treated as a trust boundary:

- **Bounded inputs.** Each encoded message (request frame or reply) is capped by
  `max_message_size` (4 MiB default), bounding memory per call. A unary body that
  exceeds the cap returns `413` instead of being truncated into a malformed
  message; the body-limit middleware can impose a smaller ceiling upstream. A
  JSON-array request body is strictly framed — trailing tokens after the closing
  `]` are rejected (`400`) so a single request cannot smuggle silently dropped
  data past the parser.
- **Fuzzed parser.** The `google.api.http` path-template parser is fuzzed
  (`FuzzParseTemplate`) to guarantee it never panics on malformed templates from
  a descriptor set.
- **No descriptor execution.** Descriptors are *data*; they are parsed with
  `protojson`/`protoreflect`, never code-generated or executed. Unknown imports
  fall back to the descriptors linked into the binary, not the filesystem.
- **Credential pass-through, not interpretation.** `Authorization` and
  `Grpc-Metadata-*` headers are forwarded as metadata; Jul.IA does not log or
  store them. Pair transcoding with a location `auth` modifier to authenticate at
  the edge before the call is made.
- **Reflection trust.** `use_reflection` trusts the backend to describe its own
  API; point it only at backends you control. Prefer a pinned `descriptor_set`
  in untrusted topologies.

## Benchmarks

GA criterion 2. Measured with the in-tree benchmarks against an in-process
loopback gRPC echo backend.

```bash
go test -tags grpc -run '^$' -bench . -benchmem -benchtime=3s ./internal/transcode/
```

Environment: `windows/amd64`, Virtual CPU @ 3.41 GHz. Absolute latency is
**loopback-dominated** (the round trip dwarfs the translation); the
allocation deltas are the stable, machine-independent signal.

| Benchmark | Time/op | Bytes/op | Allocs/op |
| --- | --- | --- | --- |
| `TranscodeUnaryPostBody` (POST + JSON body) | ~494 µs | ~19.5 KB | 235 |
| `TranscodeUnaryGetPathVar` (GET, path → message) | ~480 µs | ~19.0 KB | 229 |
| `NativeGRPCUnary` (baseline, same backend) | ~337 µs | ~11.0 KB | 184 |
| `PathTemplateMatch` (routing only, no I/O) | ~0.74 µs | 464 B | 4 |

**Reading the numbers.** Over a native gRPC call to the same backend,
transcoding a unary request adds roughly **50 allocations and ~8.5 KB** per call
— the cost of JSON↔protobuf conversion and the HTTP layer. Pure route matching
(`PathTemplateMatch`) is sub-microsecond, so routing is not a bottleneck; the
translation tax is serialization, not dispatch.

## GA status

Per ADR [0003](adr/0003-maturity-and-ga.md), transcoding is **GA**.
The soak test (criterion 5) was completed on 2026-07-15 (8 h isolated, Linux) and is tracked in
[soak-evidence.md](soak-evidence.md) and [ga-push.md](ga-push.md).

| # | GA criterion | Status |
| --- | --- | --- |
| 1 | Conformance matrix published | ✅ this document |
| 2 | Published benchmark numbers | ✅ this document + `bench_test.go` |
| 3 | Documented known-limitations | ✅ this document |
| 4 | Stable config/API contract (semver-guarded) | ✅ [compatibility policy](compatibility.md) (v1 tag at release) |
| 5 | Long-running soak test passed | ✅ 59.1 M req, 0 % err (8 h Linux, 2026-07-15) — [soak-evidence.md](soak-evidence.md) |
| 6 | Runnable example + docs | ✅ [examples/grpc-gateway](../examples/grpc-gateway) + this doc |
| 7 | Security / threat note | ✅ this document |
| 8 | Fuzzing where parsing is involved | ✅ `FuzzParseTemplate` |
| 9 | Self-explanatory Console surface | ✅ Console **Status** panel reports gRPC transcoding active |

All GA criteria are satisfied.

## See also

- [Native gRPC passthrough](grpc-proxy.md) — load-balanced gRPC, unchanged
- [examples/grpc-gateway](../examples/grpc-gateway) — runnable transcoding sample
- [ADR 0002 — protocol adaptation](adr/0002-protocol-adaptation.md)
- [ADR 0003 — maturity & GA bar](adr/0003-maturity-and-ga.md)

## Backend TLS

With `tls = true` the transcoder dials the backend using the resolved
[`backend_tls`](upstreams.md#backend-tls) policy, taken from the location's own
block or, failing that, from the pool named by `grpc_transcode.target`. The
gRPC **reflection** fetch uses the same policy as the transcoded calls, so
descriptor discovery cannot reach a backend that live traffic would refuse.

Without a block the previous behaviour is unchanged: a TLS 1.2 floor and the
platform trust store.
