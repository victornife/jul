# WebAssembly plugins

Jul.IA runs sandboxed extensions compiled to WebAssembly on the embedded
[wazero](https://wazero.io) runtime. wazero is a pure-Go WebAssembly runtime with
no cgo, so enabling plugins keeps the server a single static binary.

Plugins are opt-in behind the `wasmplugins` build tag:

```bash
go build -tags wasmplugins -o jul ./cmd/jul
```

A binary built without the tag still parses a `[plugins]` table but rejects it at
startup if it is populated, so misconfiguration fails loudly.

## Contents

- [Concepts](#concepts)
- [Configuration](#configuration)
- [Writing a plugin](#writing-a-plugin)
- [Building a plugin](#building-a-plugin)
- [The guest SDK](#the-guest-sdk)
- [The `jul-abi/v1` ABI](#the-jul-abiv1-abi)
- [Sandbox & capabilities](#sandbox--capabilities)
- [Observability](#observability)
- [Limits and reserved features](#limits-and-reserved-features)

## Concepts

A plugin is a WebAssembly module that exports a single request handler. There are
two shapes, chosen by the `type` field:

- **`middleware`** wraps the next handler in the chain. Its handler returns
  `Continue` to pass the (possibly mutated) request on, or `Stop` to short-circuit
  and serve its own response.
- **`handler`** is a *terminal* location action, like `root` or `proxy_pass`. Its
  response is always sent; there is no next handler.

Each plugin runs in its own wazero runtime with a memory cap and a per-invocation
deadline. A guest that panics or overruns its deadline is contained: the request
fails with `500` and the server keeps serving every other request.

## Configuration

Declare modules under `[plugins]`, then attach them to traffic by name.

```toml
[plugins.header-inject]
path = "./plugins/header-inject.wasm"   # OR inline = "<base64 module bytes>"
type = "middleware"                     # "middleware" (default) or "handler"
memory_limit = "16m"                    # guest linear-memory cap (default 16 MiB)
timeout = "100ms"                       # per-invocation deadline (default 100ms)
config = { header = "X-Plugin", value = "header-inject" }  # JSON to the guest

[plugins.kv-counter]
path = "./plugins/kv-counter.wasm"
kv = true                               # grant the key/value store

[plugins.request-block]
path = "./plugins/request-block.wasm"
type = "handler"

[[servers]]
listen = "0.0.0.0:8080"
plugins = ["header-inject"]             # middleware for every location here

  [[servers.locations]]
  match = { type = "prefix", path = "/api/" }
  proxy_pass = "http://backend"
  plugins = ["kv-counter"]              # additional middleware for this location

  [[servers.locations]]
  match = { type = "exact", path = "/blocked" }
  plugin = "request-block"              # terminal handler — no root/proxy_pass
```

| Key | Meaning |
| --- | ------- |
| `path` / `inline` | Module source — supply exactly one |
| `type` | `middleware` (default) or `handler` |
| `config` | String map handed to the guest as a JSON object via `get_config` |
| `memory_limit` | Guest linear-memory ceiling (default 16 MiB) |
| `timeout` | Deadline for a single invocation (default 100ms) |
| `kv` | Grant the key/value store host functions (namespaced per plugin) |
| `kv_max_entries` / `kv_max_bytes` | Per-plugin KV quota (defaults 1024 keys / 1 MiB); a `kv_set` past either is rejected |
| `fetch` / `allowed_hosts` | Grant guarded outbound HTTP to the allow-listed hosts (SSRF-guarded) |
| `fetch_timeout` / `max_fetch_response` | Per-call deadline and response-size cap for `fetch` (defaults 5s / 1 MiB) |
| `max_request_body` / `max_response_body` | Body buffering caps (defaults 1 MiB / 8 MiB); overflow fails the call, never truncates |

Validation rules:

- exactly one of `path` or `inline` must be set;
- `type` must be `middleware` or `handler`;
- `path`, when set, must exist on disk;
- `fetch = true` requires a non-empty `allowed_hosts`;
- a server/location `plugins = [...]` entry must name a `middleware` plugin;
- a location `plugin = "..."` must name a `handler` plugin, and a location may
  use only one terminal action.

Middleware ordering: server-level `plugins` wrap location-level `plugins`, and
plugin middleware sits outside auth and rate limiting. Within a list, the first
name is the outermost wrapper.

`[plugins]` participates in zero-downtime [hot reload](../README.md#hot-reload):
on reload the modules are recompiled into a fresh set and swapped in atomically,
backed by a shared compilation cache, with no process restart.

## Writing a plugin

A plugin is an ordinary Go program that imports the guest SDK and sets a single
handler in `init`:

```go
package main

import "juliaplugins/sdk"

func init() {
	sdk.Handle = func(req *sdk.Request) sdk.Action {
		req.SetResponseHeader("X-Plugin", "header-inject")
		return sdk.Continue
	}
}

func main() {}
```

A middleware that blocks some requests:

```go
func init() {
	sdk.Handle = func(req *sdk.Request) sdk.Action {
		if v, _ := req.Header("X-Block"); v == "1" {
			sdk.SetResponseStatus(403)
			sdk.WriteResponseBody([]byte("blocked by request-block plugin\n"))
			return sdk.Stop
		}
		return sdk.Continue
	}
}
```

## Building a plugin

Plugins are built for the WASI preview-1 target with the standard Go toolchain
(Go 1.26+). `c-shared` build mode produces a reactor module (one that exposes a
`_initialize` start function instead of `main`), which is how the host runs guest
`init` functions before invoking the export:

```bash
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o header-inject.wasm ./header-inject
```

The runnable [examples/plugins](../examples/plugins) directory is a self-contained
Go module with the SDK, several example plugins, and a build script that emits the
`.wasm` files used by the test suite.

## The guest SDK

The SDK lives at [`examples/plugins/sdk`](../examples/plugins/sdk) (package
`sdk`). It wraps the raw ABI in a small, allocation-light API.

| Symbol | Purpose |
| ------ | ------- |
| `var Handle func(*Request) Action` | Set in `init`; the host calls it per request |
| `Continue`, `Stop` (`Action`) | Pass the request on / short-circuit with the guest's response |
| `LevelDebug/Info/Warn/Error` | Levels for `Log` |
| `Log(level int, msg string)` | Write to the host log |
| `Request.Method() string` | Request method |
| `Request.URI() string` / `SetURI(string)` | Read / rewrite the request URI |
| `Request.Header(name) (string, bool)` | Read a request header |
| `Request.SetHeader(name, value)` | Set a request header for the next handler |
| `Request.SetResponseHeader(name, value)` / `SetResponseHeader(...)` | Set a response header |
| `SetResponseStatus(code int)` | Set the response status |
| `WriteResponseBody(b []byte)` | Append to the response body |
| `Request.Body() []byte` | Buffered request body (up to the host limit) |
| `Request.Config() []byte` | The plugin's `config` table as a JSON object |
| `KVGet(key) ([]byte, bool)` / `KVSet(key, value) bool` | Key/value store (needs `kv`) |
| `Fetch(method, url string, body []byte) (int, []byte, error)` | Guarded outbound HTTP (needs `fetch`); reads the response body via `last_fetch_len`+`fetch_read`. When the response exceeds `max_fetch_response`, the body is truncated and `LastFetchTruncated()` returns `true`. |

## The `jul-abi/v1` ABI

The SDK talks to the host over the `jul` WebAssembly import module. Authors using
the SDK never call these directly, but the contract is stable and versioned so
other languages can target it. The compatibility guarantees (additive-only in
v1, golden-pinned, prebuilt-guest tested) are documented in
[abi.md](abi.md).

- **Export (guest → host):** `handle_request() -> u32` returns the `Action`
  (`0` = Stop, `1` = Continue). The module is a reactor; the host runs
  `_initialize` once at instantiation.
- **Imports (host functions, module `jul`):**

  | Function | Signature | Notes |
  | -------- | --------- | ----- |
  | `log` | `(level, ptr, n u32)` | Level 0/1/2/3 = debug/info/warn/error |
  | `get_method` | `(buf, limit u32) -> u32` | Caller-allocates; returns full length |
  | `get_uri` | `(buf, limit u32) -> u32` | Caller-allocates |
  | `set_uri` | `(ptr, n u32)` | Rewrites the request URI |
  | `get_request_header` | `(namePtr, nameLen, buf, limit u32) -> i32` | `-1` if absent |
  | `set_request_header` | `(namePtr, nameLen, valPtr, valLen u32)` | |
  | `set_response_header` | `(namePtr, nameLen, valPtr, valLen u32)` | |
  | `read_request_body` | `(buf, limit u32) -> u32` | Caller-allocates; body buffered lazily up to `max_request_body`; oversize fails the call |
  | `write_response_body` | `(ptr, n u32)` | Appends to the response body; overflow past `max_response_body` fails the call |
  | `set_response_status` | `(code u32)` | A code outside `100–599` is replaced with `500` |
  | `get_config` | `(buf, limit u32) -> u32` | Caller-allocates; JSON object |
  | `kv_get` | `(keyPtr, keyLen, buf, limit u32) -> i32` | `-1` absent, `-2` capability denied |
  | `kv_set` | `(keyPtr, keyLen, valPtr, valLen u32) -> i32` | `0` ok, `-2` capability denied, `-3` quota exceeded |
  | `fetch` | `(methodPtr,methodLen,urlPtr,urlLen,bodyPtr,bodyLen,buf,limit u32) -> i32` | status on success, `-2` denied, `-3` blocked, `-4` error |
  | `last_fetch_len` | `() -> u32` | Full length of the last `fetch` response |
  | `last_fetch_truncated` | `() -> i32` | `1` if the last `fetch` response was capped at `max_fetch_response`, else `0` |
  | `fetch_read` | `(buf, limit u32) -> u32` | Caller-allocates; re-reads the last `fetch` body so an oversize response is fully retrieved without another call |

**Caller-allocates convention.** Getters never allocate guest memory. The guest
passes a buffer pointer and its size; the host copies up to that many bytes and
always returns the value's *full* length. If the return is larger than the buffer
the guest grows it and calls again. The SDK helpers (`readInto`, `KVGet`,
`Request.Header`) implement this retry loop.

## Sandbox & capabilities

- **Memory** is capped at `memory_limit` (default 16 MiB) of WebAssembly linear
  memory per guest.
- **Time** is bounded by `timeout` (default 100ms) per invocation. A guest that
  overruns is torn down and the request fails with `500`.
- **Panics** in the guest are contained; the instance is discarded, the request
  fails with `500`, and the server keeps running.
- **No ambient authority.** Guests get no file system, no network, and no host
  clock beyond the deadline. The only granted capabilities are:
  - **`kv`** — a per-plugin namespaced key/value store shared across that
    plugin's instances. Without the capability `kv_get`/`kv_set` return "denied".
  - **`fetch`** — a guarded outbound HTTP capability restricted to
    `allowed_hosts`. The host validates each URL against the allow-list, refuses
    addresses that resolve to loopback/private/link-local/CGNAT/multicast ranges
    (SSRF guard), re-checks the allow-list on every redirect, and caps the
    response at `max_fetch_response` within `fetch_timeout`.

## Observability

Every invocation updates Prometheus metrics:

- `jul_plugin_invocations_total{plugin,result}` — `result` is `continue`, `stop`,
  or `error`;
- `jul_plugin_duration_seconds{plugin}` — invocation latency histogram;
- `jul_plugin_panics_total{plugin}` — guest panics/timeouts contained as `500`.

Guest `log` output is emitted on the server log with the plugin name attached.

## Limits and reserved features

The `jul-abi/v1` ABI is request-phase only in v1:

- **No separate response phase.** There is no `handle_response` export in v1.
  Response headers and status set during `handle_request` apply because they are
  written before the next handler runs.
