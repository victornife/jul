# Configuration reference

Jul.IA is configured by a single TOML document. The top-level tables are
`[global]`, `[[servers]]`, `[[upstreams]]`, `[cache]`, `[admin]`,
`[compression]`, `[rate_limit]`, `[egress]`, `[observability]`, `[waf]`,
`[plugins.<name>]`, and `[[stream]]`. Several tables are only honoured when the matching build tag is
present (for example `[waf]` requires the `waf` tag, `[[stream]]` the `stream`
tag, and `[plugins.<name>]` the `wasmplugins` tag); absent tags are rejected at
preflight rather than silently ignored.

A minimal, working example:

```toml
[global]
log_level = "info"
log_format = "text"
shutdown_timeout = "30s"
reload_timeout = "10s"
redact_min_secret_length = 4

[[servers]]
listen = "0.0.0.0:8080"
server_names = ["localhost", "example.com"]
client_max_body_size = "1m"

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  root  = "/srv/www/example"
  index = ["index.html", "index.htm"]
  try_files = ["$uri", "$uri/", "/index.html"]

  [[servers.locations]]
  match = { type = "prefix", path = "/api/" }
  proxy_pass = "http://backend"
  cache = true

    [servers.locations.headers]
    Host = "$host"
    X-Real-IP = "$remote_addr"
    X-Forwarded-For = "$proxy_add_x_forwarded_for"

[[upstreams]]
name = "backend"
strategy = "round_robin"
servers = ["127.0.0.1:3000", "127.0.0.1:3001"]

[cache]
enabled = true
memory_max_size = "64m"
default_ttl = "60s"
stale_while_revalidate = "30s"
stale_if_error = "5s"

[admin]
enabled = true
listen = "127.0.0.1:9090"
token = "${env:JUL_ADMIN_TOKEN}"
console = true
history_dir = "./jul-data/config-history"
history_keep = 50
plugin_upload_dir = "./jul-data/plugins"
# plugin_upload_enabled defaults to false. Set to true only if you need WASM upload.
plugin_upload_enabled = false
plugin_upload_max_size = 32

```

---

## Strict TOML decoding and compatibility aliases

Jul.IA rejects unknown TOML fields in every path that uses the canonical parser. A misspelled security, routing, TLS or policy key is a fatal configuration error rather than a silent no-op. Errors include contextual field information where the TOML decoder exposes it.

The historical singular `server_name` key remains the one documented compatibility alias: it is accepted, canonicalized immediately to `server_names`, and never emitted by `jul fmt`/marshal. Setting both forms is rejected.

Known fields are also fail-closed. Jul.IA distinguishes three cases:

- **omitted** — apply the documented default;
- **explicit zero/disabled value** — apply that field's documented zero semantics;
- **explicit invalid value** — reject the whole candidate without writing or staging it.

Enums are case-sensitive and use the exact lowercase spellings shown in this
reference. `worker_threads` accepts only `auto` or a canonical positive base-10
integer. Negative durations/sizes, invalid HTTP statuses, out-of-range values,
and sizes that overflow `int64` are rejected before runtime construction. The
same validator governs startup, `jul check`, `jul lint`, `jul fmt`, raw and
structured apply/preview, planned-restart staging, rollback, and importer output.

The machine-readable [configuration value contract](config-value-contract.json)
records every public numeric leaf plus every enum/grammar leaf, including its
bounds, allowed values, zero semantics, and activation condition. A schema drift
test fails when a new public scalar is added without an audited disposition.

---

## `[global]`

The `[global]` block controls process-wide settings: logging, worker parallelism,
and the graceful-shutdown deadline. These values apply to the entire server
instance and are read once at startup.

```toml
[global]
log_level = "info"
log_format = "json"
shutdown_timeout = "30s"
reload_timeout = "10s"
redact_min_secret_length = 4
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `worker_threads` | string | Exact lowercase `"auto"` (default) or a canonical positive base-10 integer (`1`, `2`, …); whitespace, signs, zero, fractions, overflow, and other spellings are invalid |
| `log_level` | string | Exact lowercase `debug`, `info` (default), `warn`, or `error`; any other explicit value is invalid |
| `log_format` | string | Exact lowercase `text` (human-readable, default) or `json`; invalid values are rejected rather than falling back to text |
| `access_log` / `error_log` | string | Deprecated compatibility fields; accepted but ignored by the current runtime. Configure access records under `[observability.access_log]`; application logs use the process logger. |
| `shutdown_timeout` | duration | Grace period to drain in-flight requests on shutdown (also bounds the HTTP/3 drain) |
| `reload_timeout` | duration | Maximum duration for a configuration reload before it is reported as `timed_out`. Zero or omitted defaults to 10s. The timeout is advisory: the swap still completes, but a warning is logged and the apply response includes `previous_reload.timed_out: true`. The Console surfaces this as a distinct "Applied — reload exceeded the configured timeout" warning so the operator knows to investigate slow reload paths (WAF rule compilation, WASM plugin loading, large config) or raise this value. See [reload-semantics.md](reload-semantics.md) |
| `redact_min_secret_length` | int | Shortest resolved secret value masked from logs; `0` uses the default (4). Lower it (down to 1) for short secrets, accepting possible masking of incidental log text |

The legacy `[global].access_log` / `error_log` values are known no-ops retained for compatibility in the current major version. They emit lint warnings, do not cause a restart, and do not select a sink. Use `[observability.access_log].enabled` and `sinks` for request records; route process stderr through the service supervisor.

Durations use Go syntax: `30s`, `5m`, `1h`. Sizes use `512k`, `1m`,
`512m`, etc. Values must fit the signed 64-bit representation used by the
runtime; parsing rejects overflow before unit multiplication. Zero is accepted
only with the meaning documented for the specific field.

Run `jul check -config server.toml` before deployment for structural validation
plus runtime preflight. `jul lint` adds advisory best-practice findings; it never
downgrades a runtime-invalid value to a warning. `jul fmt` validates before
printing or writing canonical TOML, so formatting cannot persist an invalid
candidate.

## Configuration apply modes

The admin API `POST /api/config/apply` accepts an optional `?mode=` query
parameter that controls how a valid candidate is applied:

| Mode | Description |
| ---- | ----------- |
| `hot` (default) | Validates, persists, and immediately triggers a live reload. Restart-required changes are rejected with `restart_required: true` and `can_stage: true`; nothing is written. |
| `stage_restart` | Validates and persists the candidate without triggering a live reload. The running process continues serving the previous configuration. The candidate takes effect on the next process restart. Use this mode for changes to startup-bound settings (cache, egress, admin, tracing, access-log, ACME, log format, listener settings). |

When a candidate is staged:

- `GET /api/config/pending-restart` returns the structured staging state
  (staged version, serving version, pending subsystems, discard availability).
- `POST /api/config/pending-restart/discard` atomically restores the previous
  configuration and clears the staged state. The running process is unaffected.
- Hot applies are refused with `HTTP 409` until the staged candidate is
  discarded or the process is restarted.

See [reload-semantics.md](reload-semantics.md#planned-restart-staging) for the
crash-consistent staging order and reconciliation rules.

---

## `[[servers]]`

A `[[servers]]` block defines a virtual host bound to a single listen address.
You can repeat the table to run multiple listeners (e.g. HTTP on `:80` and HTTPS
on `:443`). Each server matches incoming requests by `Host` header
(`server_names`) or falls back to the default server for that address.

```toml
[[servers]]
listen = "0.0.0.0:8080"
server_names = ["api.example.com"]
client_max_body_size = "8m"
read_timeout = "60s"
write_timeout = "60s"

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  proxy_pass = "http://api_pool"
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `listen` | string | Bind address, e.g. `0.0.0.0:8080` or `127.0.0.1:443` |
| `server_names` | []string | Host names matched against the `Host` header |
| `locations` | array | One or more `[[servers.locations]]` blocks |
| `tls` | table | TLS settings (see [TLS](#tls)) |
| `client_max_body_size` | size | Maximum request body (per-server default) |
| `max_header_bytes` | size | Maximum request header size (default 1 MiB) |
| `read_header_timeout` | duration | Time allowed to read request headers |
| `read_timeout` / `write_timeout` | duration | Hard request/response caps (off by default so SSE/WebSocket/large transfers are not severed) |
| `idle_timeout` | duration | Keep-alive idle timeout |
| `access_log` / `error_log` | string | Deprecated compatibility fields; accepted and linted but ignored. Use the global `[observability.access_log]` block and the process logger instead. |
| `error_pages` | table | Map of status code → file path or redirect URL |
| `redirect_https` | int | On an HTTP server, redirect to HTTPS with this status (`301` or `308`) |
| `h2c` | bool | On a plaintext listener, also accept cleartext HTTP/2 (h2c) for native gRPC clients without TLS; ignored on a TLS listener (HTTP/2 is negotiated via ALPN) |

---

## `[[servers.locations]]`

Each location selects requests with a `match` expression and applies **exactly one**
action. Think of a location as a route: it decides *which* requests it handles
and *what* to do with them. Available actions are static file serving (`root`),
reverse proxy (`proxy_pass`), FastCGI (`fastcgi_pass`), uWSGI (`uwsgi_pass`),
gRPC-JSON transcoding (`grpc_transcode`), redirects (`redirect`/`return`), or an
explicit `deny`.

Only one action may be present per location; validation rejects ambiguous blocks.

**Matching:**

```toml
match = { type = "prefix", path = "/api/" }   # prefix, exact, or regex
```

### Static file serving

Serve files from a local directory. Ideal for SPAs, asset delivery, and simple
sites. Use `try_files` for SPA fallback routing (e.g. send all unmatched paths
to `index.html`).

```toml
[[servers.locations]]
match = { type = "prefix", path = "/" }
root = "/srv/www/myapp"
index = ["index.html"]
try_files = ["$uri", "$uri/", "/index.html"]
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `root` | string | Document root directory |
| `index` | []string | Index file candidates for directory requests |
| `try_files` | []string | Fallback sequence (supports `$uri`) |
| `directory_listing` | bool | Enable auto directory index |
| `allow_hidden` | bool | Serve dotfiles |
| `cache_control` | string | `Cache-Control` header for served files |

### Reverse proxy

Forward requests to an HTTP backend. This is the workhorse action for API
gateways, microservice front-ends, and load-balanced applications. Use
`proxy_pass = "http://upstream-name"` to reference a named `[[upstreams]]` pool
or a literal `http://host:port` for a single backend.

```toml
[[servers.locations]]
match = { type = "prefix", path = "/api/" }
proxy_pass = "http://backend"
proxy_connect_timeout = "5s"
proxy_read_timeout = "30s"
  [servers.locations.headers]
  Host = "$host"
  X-Real-IP = "$remote_addr"
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `proxy_pass` | string | `http://upstream-name` or a concrete `http://host:port` |
| `proxy_connect_timeout` | duration | Connection establishment timeout (default 10s) |
| `proxy_read_timeout` | duration | Per-read inactivity bound on the upstream response — the maximum gap between successive reads, covering both the headers (time-to-first-byte) and a slow-trickle body. `0` (default) leaves it unbounded. A steadily streaming response is never interrupted while data keeps flowing |
| `proxy_send_timeout` | duration | Per-write inactivity bound on sending the request to the upstream — the maximum gap between successive writes. `0` (default) leaves it unbounded |
| `proxy_retries` | int | Maximum retry attempts for idempotent requests on connection failure. `0` (default) tries every distinct backend at most once. A positive value caps attempts to the configured count |
| `grpc` | bool | Proxy `proxy_pass` as **native gRPC** over end-to-end HTTP/2 (trailers preserved, no buffering); `http://` dials the backend over cleartext HTTP/2 (h2c), `https://` over HTTP/2 with TLS — requires the `grpc` build tag |
| `headers` | table | Upstream request headers; values support `$host`, `$remote_addr`, `$scheme`, `$proxy_add_x_forwarded_for` |

### FastCGI / uWSGI

Pass requests to FastCGI or uWSGI application servers (PHP-FPM, Python
applications, etc.). This replaces the need for a separate FastCGI front-end
when Jul sits at the edge.

```toml
[[servers.locations]]
match = { type = "prefix", path = "/" }
fastcgi_pass = "unix:/run/php/php-fpm.sock"
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `fastcgi_pass` | string | `unix:/path.sock`, `tcp://host:port`, or `host:port` |
| `fastcgi_params` | table | Explicit CGI parameter overrides |
| `uwsgi_pass` | string | uWSGI socket address (same address forms as above) |

### gRPC transcoding (`[servers.locations.grpc_transcode]`, `grpc` build tag)

Expose a gRPC service as a RESTful JSON API (unary and streaming). Jul translates
the HTTP request into a gRPC call and the protobuf reply back into JSON. This
lets mobile and web clients consume gRPC backends without a dedicated gateway.

Requires the `grpc` build tag: `go build -tags grpc ./cmd/jul`.

```toml
[[servers.locations]]
match = { type = "prefix", path = "/v1/" }
[servers.locations.grpc_transcode]
target         = "grpc-backend"     # upstream name or host:port
descriptor_set = "/etc/jul/api.pb"
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `target` | string | Backend gRPC server: an upstream name or a literal `host:port` |
| `descriptor_set` | string | Path to a compiled `FileDescriptorSet` (`.pb`) describing the service |
| `use_reflection` | bool | Discover the service via gRPC server reflection instead of a descriptor file |
| `tls` | bool | Dial the backend over TLS (default plaintext h2c) |
| `preserve_proto_field_names` | bool | Emit original `snake_case` proto field names instead of `lowerCamelCase` JSON names |
| `streaming` | bool | Enable server-, client-, and bidirectional-streaming transcoding (default `false`) |
| `stream_mode` | string | Frame format for streamed responses: `ndjson` (default) or `sse` |
| `max_message_size` | string | Maximum per-message body size, e.g. `"4m"` (default `"4MiB"`) |

See [gRPC transcoding deep-dive](./grpc-transcoding.md) for the full streaming
matrix, path-variable mapping, and benchmark notes.

### Redirect / control

Issue redirects, bare status returns, regex rewrites, or explicit denies. Use
`rewrites` for pretty-URL transformation before the action runs. Set `cache =
true` to enable response caching for this location (requires `[cache].enabled`).

```toml
[[servers.locations]]
match = { type = "prefix", path = "/old-path" }
redirect = "/new-path"
return = 301

[[servers.locations]]
match = { type = "exact", path = "/health" }
return = 200
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `redirect` | string | Target URL (uses `return` code or 302) |
| `return` | int | Status for a redirect or bare return |
| `deny` | bool | Reject matching requests with 403 |
| `rewrites` | array | Regex rewrite rules (`pattern`, `replacement`, `flag`) |
| `cache` | bool | Enable response caching for this location (requires `[cache].enabled`) |
| `client_max_body_size` | size | Override the server default for this location |
| `rate_limit` | table | Override the global `[rate_limit]` for this location (`enabled`, `key`, `rate`, `burst`; `max_conns` is ignored) |

---

## `[[upstreams]]`

An upstream is a named pool of backend servers. Locations reference them via
`proxy_pass = "http://name"`. Upstreams decouple routing from backend topology,
so you can change servers, add health checks, or switch to service discovery
without touching the location rules.

```toml
[[upstreams]]
name = "backend"
strategy = "least_conn"
max_fails = 3
fail_timeout = "10s"
servers = [
  { address = "127.0.0.1:3000", weight = 2 },
  { address = "127.0.0.1:3001", weight = 1 },
]
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `name` | string | Pool name |
| `strategy` | string | `round_robin`, `weighted_round_robin`, or `least_conn` |
| `servers` | array | Bare addresses (`"127.0.0.1:3000"`) or tables with `address` + `weight` |
| `max_fails` | int | Failures before a backend is marked unhealthy |
| `fail_timeout` | duration | How long a backend stays out of rotation |

### `[upstreams.health_check]`

Active health checking proactively probes each backend so failures are detected
(and recoveries observed) without waiting for live traffic. A backend leaves
rotation after `unhealthy_threshold` consecutive failed probes and returns after
`healthy_threshold` consecutive successful ones; this active verdict combines
with passive (`max_fails` / `fail_timeout`) health.

```toml
[[upstreams]]
name = "api"
servers = ["127.0.0.1:3000", "127.0.0.1:3001"]
  [upstreams.health_check]
  enabled = true
  type = "http"
  path = "/healthz"
  interval = "5s"
  timeout = "2s"
  healthy_threshold = 2
  unhealthy_threshold = 3
  expect_status = [200]
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `enabled` | bool | Turn active probing on for this pool |
| `type` | string | `http` (default) or `tcp` |
| `path` | string | Request path for `http` probes (required) |
| `interval` | duration | Delay between probe rounds (default `5s`) |
| `timeout` | duration | Per-probe timeout; must be less than `interval` (default `2s`) |
| `healthy_threshold` | int | Consecutive successes to mark a backend healthy (default `2`) |
| `unhealthy_threshold` | int | Consecutive failures to eject a backend (default `3`) |
| `expect_status` | array | Acceptable HTTP status codes for `http` probes (default `[200]`) |
| `expect_body` | string | Optional: `http` probe body must contain this substring |

Metrics: `jul_upstream_healthy{pool,backend}` (1 healthy / 0 unhealthy),
`jul_upstream_probes_total{pool,result}`, and
`jul_upstream_probe_duration_seconds{pool}`.

### `[upstreams.discovery]`

Resolve a pool's backends from an external source and refresh them live, with no
config reload. With discovery enabled the static `servers` list is optional (a
seed/fallback until the first resolve). `dns` and `dns_srv` work in every build;
`consul` and `kubernetes` require the matching build tag.

```toml
[[upstreams]]
name = "api"
strategy = "round_robin"
  [upstreams.discovery]
  type = "dns"
  target = "api.internal.svc:8080"
  refresh = "15s"
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `type` | string | `static` (off), `dns`, `dns_srv`, `consul`, or `kubernetes` |
| `target` | string | `host:port` for `dns`; the SRV name for `dns_srv` |
| `refresh` | duration | Poll interval (default `30s`) |
| `[consul]` | table | `address`, `service`, `tag`, `datacenter`, `token`, `passing_only` |
| `[kubernetes]` | table | `namespace`, `service`, `port`, `api_server`, `token`, `ca_file`, `insecure_skip_tls_verify` |

Metrics: `jul_upstream_backends{pool}` (current backend count) and
`jul_discovery_errors_total{pool}` (failed/empty resolves; last-good kept).

---

## gRPC ↔ JSON transcoding (`grpc` build tag)

A location with `[servers.locations.grpc_transcode]` exposes a gRPC
service as a RESTful JSON API, translating each HTTP request into a gRPC call
and the protobuf reply back into JSON.

Exactly one of `descriptor_set` or `use_reflection` must be set. Generate a
descriptor set from your `.proto` files with `protoc`:

```bash
protoc \
  --include_imports \
  --descriptor_set_out=api.pb \
  --proto_path=. \
  your/service.proto
```

```toml
[[servers.locations]]
match = { type = "prefix", path = "/v1/" }
[servers.locations.grpc_transcode]
target         = "grpc-backend"     # upstream name or host:port
descriptor_set = "/etc/jul/api.pb"
```

Path variables (`/v1/items/{id}`), the `body` mapping, and any leftover query
parameters are all mapped onto the request message. gRPC status codes are
translated to matching HTTP status, and per-call results are counted in
`jul_grpc_transcode_requests_total{method,code}`.

### Streaming methods

Set `streaming = true` to transcode the three streaming RPC kinds in addition to
unary calls:

| gRPC method kind | Request | Response |
| --- | --- | --- |
| **Unary** | one JSON object | one JSON object |
| **Server-streaming** | one JSON object | a stream of JSON frames, flushed per message |
| **Client-streaming** | a JSON array *or* newline-delimited JSON objects | one JSON object |
| **Bidirectional** | a JSON array *or* newline-delimited JSON objects | a stream of JSON frames |

Streamed responses are framed per `stream_mode`:

- `ndjson` (default) — `application/x-ndjson`, one JSON object per line.
- `sse` — `text/event-stream`, each message as a `data:` event.

```toml
[servers.locations.grpc_transcode]
target           = "grpc-backend"
descriptor_set   = "/etc/jul/api.pb"
streaming        = true
stream_mode      = "ndjson"   # or "sse"
max_message_size = "4m"
```

---

## `[cache]`

Jul's two-tier response cache stores upstream responses in memory (L1) and
optionally on disk (L2), reducing backend load and improving latency for
repeatable reads. It respects `Cache-Control`, `Expires`, and `Vary` headers,
and supports background revalidation (`stale_while_revalidate`) and
error-tolerant serving (`stale_if_error`).

Enable `[cache].enabled` globally, then opt individual locations in with
`cache = true`. Cache entries survive config reloads but are lost on restart
unless `disk_path` is configured.

```toml
[cache]
enabled = true
memory_max_size = "128m"
disk_path = "./jul-data/cache"
disk_max_size = "1g"
default_ttl = "120s"
stale_while_revalidate = "60s"
stale_if_error = "30s"
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `enabled` | bool | Master switch |
| `memory_max_size` | size | In-memory tier cap |
| `disk_path` | string | Enables the disk overflow tier when set |
| `disk_max_size` | size | Disk tier cap |
| `default_ttl` | duration | Used when upstream gives no explicit freshness |
| `stale_while_revalidate` | duration | Serve stale entries while refreshing asynchronously |
| `stale_if_error` | duration | Extend stale serving when a background revalidation encounters an upstream error (5xx or timeout) |

Per-location caching also requires `cache = true` on the location. See
[docs/cache.md](cache.md) for the two-tier model, on-disk format, and
overflow/eviction semantics.

---

## `[compression]`

Compress responses on the fly based on the client's `Accept-Encoding` header.
This reduces bandwidth for text-heavy payloads (HTML, JSON, XML) without
pre-generating compressed assets. `gzip` is available in every build; Brotli
(`br`) and Zstd (`zstd`) require the matching build tags and offer better
compression ratios at the cost of CPU.

Enable `precompressed` to serve pre-generated `.br` / `.gz` sidecar files for
static content, skipping runtime compression entirely.

```toml
[compression]
enabled = true
encoders = ["zstd", "br", "gzip"]
min_size = "512"
types = ["text/*", "application/json", "application/javascript"]
precompressed = true
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `enabled` | bool | Master switch (default `false`) |
| `encoders` | list | Allowed encoders in server-preference order; any subset of `gzip`, `br`, `zstd` (default `["gzip"]`) |
| `level` | int | Compression level; `0` selects each encoder's own default |
| `min_size` | size | Smallest response body that is compressed (default `1k`) |
| `types` | list | MIME allow-list; a `type/*` entry matches a whole family. Defaults to text, JSON, JS, XML, SVG, and WASM when omitted |
| `precompressed` | bool | Serve sidecar `.br`/`.gz` files for static responses when present and acceptable |

---

## `[rate_limit]`

Token-bucket rate limiting protects backends from traffic spikes and abusive
clients. The global policy applies to every location; individual locations may
override the rate, burst, and key under `[servers.locations.rate_limit]`. A
per-listener concurrent-connection cap (`max_conns`) is also available.

Rate limiting is compiled into every build — no build tag required.

```toml
[rate_limit]
enabled = true
rate = 100
burst = 150
key = "ip"
max_conns = 1000
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `enabled` | bool | Master switch (default `false`) |
| `key` | string | Bucket identity: `ip` (client address, default), `header:<Name>`, or `jwt:<claim>` |
| `rate` | int | Sustained requests/second allowed per key |
| `burst` | int | Maximum momentary burst above `rate` (defaults to `rate`) |
| `max_conns` | int | Concurrent connections per listener; `0` = unlimited. Active only when the block is `enabled`; listener-global, so it is ignored on per-location overrides |

---

## `[egress]`

An optional outbound-destination **allow-list** that constrains the
config-driven auxiliary fetches the server makes on its own — JWKS retrieval
(`jwks_url`), forward-auth subrequests (`url`), Consul/Kubernetes service
discovery (`address`/`api_server`), ACME/OCSP certificate calls, and WASM plugin
`fetch`. When enabled, those fetches may only reach a
destination that matches an `allow` entry; every other destination is refused at
dial time, before any bytes are sent. This bounds the SSRF blast radius of a
mistyped or maliciously edited config value.

It is **disabled by default** and compiled into every build — no build tag
required — so the block is fully backward-compatible. The **data-plane reverse
proxy** — upstream proxying and active health checks — is intentionally out of
scope: that is the traffic the server exists to carry, not an auxiliary fetch.
See [egress.md](egress.md) for the full trust model and examples.

```toml
[egress]
enabled = true
allow = ["idp.example.com", ".internal.corp", "10.0.0.0/8", "203.0.113.7"]
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `enabled` | bool | Master switch (default `false`). When off, no restriction is applied |
| `allow` | list | Permitted destinations. Each entry is a CIDR (`10.0.0.0/8`, `2001:db8::/32`), a bare IP (`203.0.113.10`, treated as `/32` or `/128`), an exact hostname (`idp.example.com`), or a leading-dot suffix (`.internal.corp`, matching any subdomain). A host listed by name is resolved normally; a host not listed by name is permitted only when every resolved IP falls inside an allowed CIDR. Required (non-empty) when `enabled` |

---

## `[servers.locations.auth]`

Protect a location with access control. An optional CIDR gate runs first
(allow/deny by IP), then at most one credential-based method: HTTP Basic,
JWT validation, or forward-auth delegation.

Authentication is compiled into every build — no build tag required.

```toml
[[servers.locations]]
match = { type = "prefix", path = "/admin/" }
proxy_pass = "http://admin_panel"
  [servers.locations.auth]
  allow = ["10.0.0.0/8"]
  deny  = ["10.0.5.0/24"]
    [servers.locations.auth.basic]
    file = "/etc/jul/htpasswd"
    realm = "Admin Area"
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `allow` | []string | CIDR ranges permitted to proceed. When non-empty, a client must match one |
| `deny` | []string | CIDR ranges blocked. **Deny takes precedence over allow** |

A location may then set **one** of the following sub-tables:

`[servers.locations.auth.basic]` — HTTP Basic against an `htpasswd` file:

| Key | Type | Description |
| --- | ---- | ----------- |
| `file` | string | Path to an `htpasswd` file of **bcrypt** hashes (required) |
| `realm` | string | `WWW-Authenticate` realm (default `Restricted`) |

`[servers.locations.auth.jwt]` — JWT bearer tokens validated against a JWKS endpoint:

| Key | Type | Description |
| --- | ---- | ----------- |
| `jwks_url` | string | **HTTPS** URL of the issuer's JWKS document (required); keys are cached and refreshed |
| `issuer` | string | When set, the token's `iss` claim must match |
| `audience` | string | When set, the token's `aud` claim must contain this value |
| `algorithms` | []string | Allowed signing algorithms (default `RS256/384/512`, `ES256/384/512`, `PS256/384/512`). Symmetric (`HS*`) and `none` are always rejected |

`[servers.locations.auth.forward_auth]` — delegate the decision to an external service:

| Key | Type | Description |
| --- | ---- | ----------- |
| `url` | string | `http(s)` URL of the auth service. The request is mirrored with `X-Forwarded-Method/Uri/Host` |
| `auth_response_headers` | []string | Response headers copied onto the upstream request on a 2xx decision |

---

## `[admin]`

The admin endpoint provides runtime observability and operational control:
Prometheus metrics (`/metrics`), health (`/health`), config history/rollback,
and the optional web console dashboard. Bind it to loopback in production and
protect it with a bearer token.

The console requires the `console` build tag: `go build -tags console ./cmd/jul`.

```toml
[admin]
enabled = true
listen = "127.0.0.1:9090"
token = "${env:JUL_ADMIN_TOKEN}"
console = true
history_dir = "./jul-data/config-history"
history_keep = 50
plugin_upload_dir = "./jul-data/plugins"
# plugin_upload_enabled defaults to false. Set to true only if you need WASM upload.
plugin_upload_enabled = false
plugin_upload_max_size = 32
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `enabled` | bool | Master switch |
| `listen` | string | Bind address — keep it on loopback (e.g. `127.0.0.1:9090`) |
| `token` | string | When set, requires `Authorization: Bearer <token>` |
| `console` | bool | Serve the web console dashboard at the admin root (default `true`; requires `console` build tag) |
| `history_dir` | string | Directory for configuration snapshots used by the console history/rollback panel |
| `history_keep` | int | Maximum number of configuration snapshots to retain; older ones are pruned (default `50`) |
| `plugin_upload_dir` | string | Directory for uploaded `.wasm` modules from the Console Plugins panel (default `./jul-data/plugins`) |
| `plugin_upload_enabled` | bool | Default `false`; set `true` to enable the `.wasm` upload endpoint. Also requires positive `plugin_upload_max_size`. |
| `plugin_upload_max_size` | int | Maximum `.wasm` upload size in megabytes (default `32`) |

---

## `[observability.tracing]`

OpenTelemetry distributed tracing exports request spans to an OTLP collector,
making it easy to diagnose latency across proxy hops, cache hits, and upstream
calls. Tracing is disabled by default and requires a binary built with the
`otel` build tag.

Tracing configuration is read once at boot; a reload keeps the running tracer
(the server logs a warning if the block changed) — restart to apply tracing
changes.

```toml
[observability.tracing]
enabled = true
exporter = "otlp-grpc"
endpoint = "otel-collector:4317"
sample_ratio = 0.1
service_name = "jul-gateway"
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `enabled` | bool | Master switch (default `false`) |
| `exporter` | string | OTLP transport: `otlp-grpc` (default) or `otlp-http` |
| `endpoint` | string | Collector address: `host:port` for gRPC, URL/host for HTTP. Required when enabled |
| `sample_ratio` | float | Head-sampling probability for root spans, `0`..`1` (defaults to `1.0`) |
| `service_name` | string | Resource `service.name` (defaults to `jul`) |
| `insecure` | bool | Export over plaintext instead of TLS (default `false`) |

---

## `[observability.metrics]`

Tune the Prometheus metrics exposed at the admin `/metrics` endpoint. These
metrics cover HTTP requests, cache events, upstream health, rate limiting, and
more. No build tag is required.

The `host_label` setting controls whether the request `Host` header is added as
a Prometheus label. It is **off by default** because unbounded host values can
explode metric cardinality.

```toml
[observability.metrics]
host_label = false
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `host_label` | bool | Add the request `Host` as the `host` label on `jul_http_requests_total` and `jul_http_request_duration_seconds` (default `false`) |

The `host` label is **off by default**: the Host header is client-controlled, so
recording it unconditionally lets a flood of distinct Host values explode metric
cardinality. Enable `host_label` only when the set of hosts is bounded. The
setting is read once at boot; a reload keeps the running value — restart to
apply a change.

Every other metric label is bounded by construction — the request `method` is
folded to a fixed set (unknown tokens become `other`), and no request path,
query, client IP, or user-agent is ever a label. See the full
[label-cardinality policy and relabel cookbook](core-http.md#metrics) for the
authoritative inventory and scale guidance.

---

## `[observability.access_log]`

Control where the HTTP access log is written. You can send logs to `stdout`, a
rotating file, or the local syslog daemon (Unix only). This is useful for
shipping logs to SIEM or audit systems without external agents.

No build tag required.

```toml
[observability.access_log]
enabled = true
sinks = ["stdout", "file"]
file = "/var/log/jul/access.log"
format = "json"
rotate_max_mb = 500
rotate_keep = 14
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `enabled` | bool | Emit request access records. Omitted defaults to `true`; explicit `false` disables stdout/file/syslog and the Console access-record tail only. |
| `sinks` | []string | Destinations: any of `stdout`, `file`, `syslog`. Omitted defaults to `["stdout"]`; an explicit empty list is invalid while enabled. |
| `file` | string | Access-log file path. Required whenever `file` is listed, including dormant disabled configuration. |
| `format` | string | Encoding of the `file` and `syslog` sinks: `text` (logfmt, default) or `json`. The `stdout` sink always follows `[global].log_format`. |
| `rotate_max_mb` | int | File size in MB at which the file rotates (default `100`). |
| `rotate_keep` | int | Maximum number of rotated files to retain (default `7`). |

When `enabled = false`, Jul opens no access-log file or syslog resource and emits no request access records to the Console tail. Process/application, reload, security/WAF, audit, health, metrics, and tracing output remain independent. Sink details remain stored and validated while dormant so re-enabling is deterministic.

The `syslog` sink uses the local system log and is **not supported on Windows**. The whole block is fixed at startup and currently requires a staged restart to change; #98 owns future generation-safe sink reload.

---

## TLS

TLS is configured per server block. Jul supports TLS 1.2 and 1.3, SNI
certificate selection, and dynamic certificate selection at listener startup.
Certificate rotation (static `cert`/`key` files or ACME domain changes) requires a
process restart. Use `redirect_https` on a plain HTTP server to force clients onto HTTPS.

```toml
[[servers]]
listen = "0.0.0.0:8443"
server_names = ["example.com"]

  [servers.tls]
  enabled = true
  cert = "/etc/jul/tls/example.crt"
  key  = "/etc/jul/tls/example.key"
  min_version = "1.2"   # "1.2" or "1.3"
```

To force HTTPS, add an HTTP server block with `redirect_https = 308`:

```toml
[[servers]]
listen = "0.0.0.0:80"
server_names = ["example.com"]
redirect_https = 308
```

### Mutual TLS (client certificates)

Jul can authenticate the *client* by its certificate. With
`[servers.tls.client_auth]` it verifies the certificate against a CA bundle,
optionally checks a CRL and a SAN allow-list, and exposes the verified identity
to upstreams as `$ssl_client_*` proxy variables.

```toml
[[servers]]
listen = "0.0.0.0:443"
server_names = ["api.example.com"]

  [servers.tls]
  enabled = true
  cert = "/etc/jul/server.crt"
  key  = "/etc/jul/server.key"

    [servers.tls.client_auth]
    mode = "require"
    ca_file = "/etc/jul/clients-ca.pem"

  [[servers.locations]]
  match = { type = "prefix", path = "/api" }
  proxy_pass = "http://127.0.0.1:9000"
  require_client_cert = true
    [servers.locations.headers]
    X-Client-CN = "$ssl_client_cn"
```

The full reference lives in [docs/mtls.md](mtls.md).

---

## Automatic HTTPS (ACME)

Jul can obtain and renew certificates automatically from an ACME certificate
authority (Let's Encrypt by default) using the **HTTP-01** or **TLS-ALPN-01**
challenge. This eliminates manual certificate provisioning and renewal.
This feature is gated behind the `acme` build tag.

```bash
go build -tags acme -o jul ./cmd/jul
```

Configure it under `[servers.tls.acme]` on a `:443` server block, and keep a
plain `:80` block running so the challenge can be answered:

```toml
[[servers]]
listen = "0.0.0.0:443"
server_names = ["example.com", "www.example.com"]

  [servers.tls.acme]
  enabled = true
  email = "ops@example.com"
  ca = "letsencrypt-staging"
  challenge = "http-01"
  cache_dir = "./jul-data/certs"

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  root = "/srv/www/example"

[[servers]]
listen = "0.0.0.0:80"
server_names = ["example.com", "www.example.com"]
redirect_https = 308
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `enabled` | bool | Turn on ACME for this server block |
| `email` | string | **Required.** ACME account contact address |
| `ca` | string | `letsencrypt-staging` (default), `letsencrypt`, or a custom `https://` directory URL |
| `domains` | []string | Certificate host names; defaults to `server_names` |
| `challenge` | string | `http-01` (default) or `tls-alpn-01`. `dns-01` is reserved for a future release |
| `cache_dir` | string | Directory where issued certificates are cached (default `./jul-data/certs`) |
| `ocsp_stapling` | bool | Staple OCSP responses onto served certificates (default `true`) |

- The **default CA is staging** (untrusted certificates, generous rate limits).
  Switch to `ca = "letsencrypt"` only after staging works end to end.
- A single listener address may not mix ACME and static `cert`/`key` server
  blocks; validation rejects that.
- The ACME domain set is fixed at startup — enabling ACME or adding domains
  needs a restart.
- Certificate expiry and renewals are exported as `jul_tls_cert_expiry_seconds`
  and `jul_acme_renewals_total`.

See [examples/auto-https](../examples/auto-https) for a runnable walkthrough.

---

## HTTP/3 (QUIC)

Add a `[servers.http3]` block to a **TLS-enabled** server to also serve HTTP/3
over QUIC on the same address (UDP), sharing the same TLS certificate. Clients
discover it via an `Alt-Svc` response header. This requires the `http3` build
tag.

```bash
go build -tags http3 -o jul ./cmd/jul
```

```toml
[[servers]]
listen = "0.0.0.0:443"
server_names = ["example.com"]

  [servers.tls]
  enabled = true
  cert = "/etc/jul/tls/example.crt"
  key = "/etc/jul/tls/example.key"

  [servers.http3]
  enabled = true
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `enabled` | bool | Turn on the HTTP/3 listener for this server block |
| `alt_svc_max_age` | int | `Alt-Svc` advertisement lifetime in seconds (default `86400`) |

Notes:

- **TLS is required.** QUIC mandates TLS 1.3; validation rejects `http3` on a
  plain listener.
- **Open the UDP port.** HTTP/3 listens on UDP at the same port as TCP.
- **Shared certificates, including renewals.** Static-cert reload or ACME
  renewal applies to HTTP/3 automatically.
- **WebSocket is not supported over HTTP/3.** Clients transparently fall back
  to HTTP/2.
- Settings are fixed at startup; changing them needs a restart.

---

## `[plugins.<name>]`

> Requires a binary built with `-tags wasmplugins`.

The WebAssembly plugin runtime lets you extend Jul without recompiling the
server. Each entry under `[plugins]` declares one Wasm module by name.
Plugins can act as middleware (wrapping a location) or as a terminal handler (replacing
a location action). Capabilities — KV store, outbound HTTP fetch — are disabled
by default and granted explicitly per plugin for security.

```toml
[plugins.header-inject]
path = "./plugins/header-inject.wasm"
type = "middleware"
memory_limit = "16m"
timeout = "100ms"
config = { header = "X-Plugin", value = "header-inject" }

[plugins.kv-counter]
path = "./plugins/kv-counter.wasm"
kv = true
```

| Key | Meaning |
| --- | ------- |
| `path` / `inline` | Module source — supply exactly one |
| `type` | `middleware` (wraps a handler) or `handler` (terminal location action) |
| `config` | String map handed to the guest as JSON via `get_config` |
| `memory_limit` | Guest linear-memory ceiling (default 16 MiB) |
| `timeout` | Deadline for a single invocation; guest is torn down on overrun (default 100ms) |
| `kv` | Grant the key/value store host functions (namespaced per plugin) |
| `fetch` / `allowed_hosts` | Grant guarded outbound HTTP to the listed hosts |

Attach a plugin to traffic by referencing its name. Server- and location-level
`plugins = [...]` lists run as **middleware** (outermost first); a location
`plugin = "name"` is a terminal **handler** action.

See [docs/plugins.md](plugins.md) for the authoring guide.

---

## `[[stream]]`

> Requires a binary built with `-tags stream`.

Each `[[stream]]` block is one L4 (TCP or UDP) reverse-proxy listener that
forwards raw connections — no HTTP parsing. Use this for databases, game
servers, or any non-HTTP TCP workload. UDP listeners are stateful relays with
session expiry. TLS SNI passthrough lets you route encrypted traffic by server
name without terminating TLS.

```bash
go build -tags stream -o jul ./cmd/jul
```

```toml
# Plain TCP load balancing.
[[stream]]
listen = "0.0.0.0:5432"
proxy_pass = "postgres_pool"
connect_timeout = "10s"
idle_timeout = "5m"

# TLS SNI routing (passthrough — TLS is never terminated).
[[stream]]
listen = "0.0.0.0:443"
  [stream.sni_routes]
  "api.example.com" = "api_pool"
  "*"               = "default_pool"

# UDP relay with PROXY protocol.
[[stream]]
listen = "0.0.0.0:53"
protocol = "udp"
proxy_pass = "dns_pool"
```

| Key | Type | Description |
| --- | ---- | ----------- |
| `listen` | string | Bind address `host:port` (**required**) |
| `protocol` | string | `tcp` (default) or `udp` |
| `proxy_pass` | string | Default backend — named upstream or literal `host:port` |
| `sni_routes` | table | TLS server-name → backend map; routes by SNI **without terminating TLS** |
| `tls_passthrough` | bool | Informational; implied whenever `sni_routes` is set |
| `proxy_protocol` | string | HAProxy PROXY-protocol handling: `""`, `"in"`, `"out"`, or `"both"` |
| `connect_timeout` | duration | Backend dial timeout (default `10s`) |
| `idle_timeout` | duration | Close relayed connection / UDP session after this idle (default `5m`) |
| `max_udp_sessions` | int | Cap concurrent UDP sessions (default `10000`) |

Provide at least one of `proxy_pass` or `sni_routes`. UDP listeners are plain
relays: `sni_routes`, `tls_passthrough`, and `proxy_protocol` are TCP-only and
rejected on a UDP block.

See [docs/stream-proxy.md](stream-proxy.md) for the full runtime model.

---

## CLI JSON output

`jul lint` and `jul check` accept `-json` for machine-readable output so CI can
parse findings instead of scraping text. Field names are lowercase and stable.

### `jul lint -json`

```json
{
  "source": "server.toml",
  "errors": ["servers[0].locations[0]: match is required"],
  "warnings": [
    {
      "severity": "warning",
      "field": "servers[0] (listen \":8080\")",
      "message": "server has no locations; every request will return 404",
      "hint": "add a [[servers.locations]] block, or set redirect_https for an HTTP->HTTPS redirector"
    }
  ]
}
```

| Field | Type | Description |
| --- | ---- | ----------- |
| `source` | string | Config source name (path or `stdin`) |
| `errors` | string[] | Validation errors; omitted when empty. Any entry ⇒ exit code `1` |
| `warnings` | object[] | Lint findings; omitted when empty |
| `warnings[].severity` | string | `"warning"` or `"error"` — always a string, never a number |
| `warnings[].field` | string | Config path the finding applies to; omitted when empty |
| `warnings[].message` | string | Human-readable description of the finding |
| `warnings[].hint` | string | Suggested fix; omitted when empty |

Exit codes: `0` = no errors, `1` = validation error(s), `2` = warnings present
under `-strict`.

### `jul check -json`

```json
{ "source": "server.toml", "ok": true }
```

On failure `ok` is `false` and either `error` (single message) or `errors`
(string array) is present.

