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
- [Uploading modules (Console API)](#uploading-modules-console-api)
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
  | `fetch` | `(methodPtr,methodLen,urlPtr,urlLen,bodyPtr,bodyLen,buf,limit u32) -> i32` | status on success, `-2` denied (no `fetch` capability), `-3` blocked (plugin `allowed_hosts` / SSRF guard), `-4` error, `-5` blocked by the global `[egress]` allow-list |
  | `last_fetch_len` | `() -> u32` | Retained/capped length of the last `fetch` response |
  | `last_fetch_truncated` | `() -> i32` | `1` if the last `fetch` response was capped at `max_fetch_response`, else `0` |
  | `fetch_read` | `(buf, limit u32) -> u32` | Caller-allocates; re-reads the last retained/capped `fetch` body without another outbound call |

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
    response at `max_fetch_response` within `fetch_timeout`. When the server-wide
    [egress allow-list](egress.md#plugin-fetch) is enabled, a fetch must ALSO
    satisfy it — the two are intersected, so a destination the plugin allows but
    the global policy refuses is blocked at dial time and returns the distinct
    `-5` code (versus `-3` for a plugin-local block).

  Capability grants are evaluated on every activation/build of a plugin. A new
  generation re-checks the current config's capability policy instead of carrying
  forward the grants from an earlier generation, so a plugin that loses `kv` or
  `fetch` in a later activation is denied those host functions immediately.

## Observability

Every invocation updates Prometheus metrics:

- `jul_plugin_invocations_total{plugin,result}` — `result` is `continue`, `stop`,
  or `error`;
- `jul_plugin_duration_seconds{plugin}` — invocation latency histogram;
- `jul_plugin_panics_total{plugin}` — guest panics/timeouts contained as `500`.

Guest `log` output is emitted on the server log with the plugin name attached.

## Uploading modules (Console API)

The Console can upload a `.wasm` module to the server over
`POST /api/plugins/upload` (multipart form, file field `wasm`). This is the
highest-consequence write the admin API exposes — it places executable code on
the server — so it is guarded and **off unless explicitly configured**.

**Enablement.** The endpoint is disabled by default. To enable it, set both
`plugin_upload_enabled = true` and a positive `plugin_upload_max_size` (MB) in
`[admin]`. Uploading executable code is a high-consequence write and must be
explicitly opt-in. Uploaded files are written to
`plugin_upload_dir` (default `./jul-data/plugins`).

**Validation performed before anything is written:**

- **Authentication.** Like every admin write, the request requires the admin
  bearer token; keep the admin listener on loopback (see
  [docs/console.md](console.md)).
- **Size cap.** The body is bounded by `plugin_upload_max_size`; an oversized
  upload is rejected with `413` and nothing is stored.
- **Format check.** The first bytes must be the WebAssembly magic (`\0asm`) and
  version `1`; anything else is rejected with `400`. (This is a shape check, not
  a proof of safety — the sandbox is the real containment boundary.)
- **Filename hardening.** The stored name is reduced to its base name and must be
  a simple `<name>.wasm` using only letters, digits, `.`, `_` or `-`. Names with
  path separators, `..`, a leading dot, a non-`.wasm` extension, or any other
  character are rejected, and the resolved path is re-checked to sit directly
  inside `plugin_upload_dir` — an uploaded module can never escape that
  directory (path-traversal defense).
- **Atomic write.** The module is written via a temp file + rename at mode
  `0600`, so a concurrent reader never sees a partially-written module and
  re-uploads replace atomically.

**Trust model.** An uploaded module is still *untrusted code*: it is confined by
the wazero sandbox and the capability rules above (no ambient file/network/clock;
`kv`/`fetch` only when granted). Because upload lets an authenticated operator
add code, treat the admin token as a code-execution credential: restrict the
admin listener to loopback or mTLS, rotate the token, and prefer
`plugin_upload_enabled = false` in environments that ship modules out-of-band.

## Limits and reserved features

The `jul-abi/v1` ABI is request-phase only in v1:

- **No separate response phase.** There is no `handle_response` export in v1.
  Response headers and status set during `handle_request` apply because they are
  written before the next handler runs.

## Conformance matrix

| Scenario | Input | Expected | Covered by |
| -------- | ----- | -------- | ---------- |
| Middleware injects header | `header-inject.wasm`, no special headers | `X-Plugin: header-inject`, next called, `200` | TestMiddlewareInjectsResponseHeader |
| Middleware blocks request | `request-block.wasm`, `X-Block: 1` | `403`, next **not** called | TestMiddlewareBlocksRequest |
| Middleware passes through | `request-block.wasm`, no `X-Block` | `200`, next called | TestMiddlewarePassesThrough |
| Handler plugin blocks | `request-block.wasm` as handler, `X-Block: 1` | `403` | TestHandlerPluginBlocks |
| Guest panic contained | `testguest-panic.wasm` | `500`, next **not** called, server stable | TestPanicIsContained |
| Guest timeout contained | `testguest-loop.wasm`, 150 ms timeout | `500`, next **not** called | TestTimeoutIsContained |
| KV capability granted | `kv-counter.wasm`, `kv = true` | Counter increments across requests, `X-Count` grows | TestKVCounterWithCapability |
| KV capability denied | `kv-counter.wasm`, `kv = false` | Counter resets every request, `X-Count = 1` | TestKVDeniedWithoutCapability |
| Reload reuses compilation cache | Same module, two generations | Second generation works, first can be closed | TestReloadReusesManager |
| Reload under concurrent load | Live gen + 12 reload cycles while traffic flows | Zero failures (≤5 tolerated for jitter) | TestReloadUnderLoad |
| Fetch allow-list match | `allowed_hosts = ["api.example.com"]` | Allowed host succeeds | TestFetchBlocksDisallowedHost |
| Fetch blocked (evil host) | `allowed_hosts = ["api.example.com"]`, fetch evil.com | `errFetchBlocked` | TestFetchBlocksDisallowedHost |
| Fetch blocks loopback | `allowed_hosts = ["127.0.0.1"]`, fetch loopback | Blocked by SSRF guard | TestFetchBlocksLoopbackEvenIfAllowed |
| Fetch blocks DNS rebinding | Allow-listed host resolves to `127.0.0.1` | Blocked by SSRF guard after resolution | TestFetchBlocksDNSRebinding |
| Fetch honours global egress | Plugin allows host, global `[egress]` does not | Blocked (`-5`) before dial; SSRF still applies when global allows | TestFetchDialIntersection / TestFetchGlobalEgressBlocksDoFetch |
| KV enforces max entries | `kv_max_entries = 2`, three distinct keys | Third `kv_set` rejected | TestKVSetEnforcesBounds |
| KV enforces max bytes | `kv_max_bytes = 100`, 200-byte value | `kv_set` rejected | TestKVSetEnforcesBounds |
| Flush rejects invalid status | Guest status `700` | Host replaces with `500` | TestFlushRejectsInvalidStatus |
| Fetch truncation detected | Response 200 B, `max_fetch_response = 100` | Body capped at 100 B, `lastFetchTruncated = true` | TestFetchTruncationDetected |
| Fetch not truncated | Response 5 B, `max_fetch_response = 100` | Body完整, `lastFetchTruncated = false` | TestFetchNotTruncated |
| Build rejects missing module | `path` points to non-existent file | Build error | TestBuildRejectsMissingModule |
| Set.Has membership | Plugin declared vs missing | `true` / `false` | TestSetHas |

## Benchmarks

All benchmarks are relative to `BenchmarkNativeHandler` (plain Go handler, no
plugin overhead). The delta is the guest-invocation cost: ABI boundary crossing,
import trampolines, linear-memory reads/writes, and (for KV) store access.

Run: `go test -tags wasmplugins -bench='BenchmarkPlugin.*' -run='^$' -benchmem ./internal/plugins/`

| Benchmark | Ops | Time/op | B/op | Allocs/op | vs Native |
| --- | --- | --- | --- | --- | --- |
| NativeHandler (baseline) | 6,086,704 | 192 ns | 0 | 0 | 1× |
| PluginMiddleware (Continue, set header) | 61,150 | 16,521 ns | 15,405 | 46 | ~86× |
| PluginHandler (Stop, set status + body) | 57,481 | 20,150 ns | 15,615 | 58 | ~105× |
| PluginKVCounterWithCapability | 45,710 | 23,019 ns | 16,919 | 63 | ~120× |
| PluginParallel (16 threads) | 323,972 | 3,423 ns | 14,725 | 43 | ~18× (amortised) |

**Interpretation.** A single guest call adds ~16–23 μs of wall-clock latency and
~15 KB of transient allocations (mostly wazero runtime state per instance). The
parallel benchmark shows that under concurrency the effective per-request cost
drops to ~3.4 μs because the runtime keeps warm instances and the host reuses
the compilation cache. This is acceptable for middleware that performs
non-trivial work (auth, transformation, enrichment) but not for passthrough
paths where every microsecond counts.

## Known limitations

1. **Request-phase only.** `jul-abi/v1` has no `handle_response` export; response
   inspection or mutation after the next handler runs is not possible in v1.
2. **No shared plugin state across names.** Each plugin name gets its own wazero
   runtime and KV namespace. Two plugins cannot share memory or KV keys even if
   they load the same `.wasm` file.
3. **No streaming request/response bodies.** The host buffers the full request
   body (up to `max_request_body`) and the guest accumulates the full response
   body (up to `max_response_body`) before anything is written to the HTTP
   response writer. Large uploads/downloads should bypass the plugin (use a
   handler route without plugins).
4. **One ABI version in v1.** Only `jul-abi/v1` is implemented; future ABIs
   (proxy-wasm, http-wasm) require a new ABI id and host-module registrar.
5. **Build-tag required.** Binaries compiled without `wasmplugins` reject any
   config that declares plugins at startup. This is intentional (fail loud) but
   means plugin-enabled builds are larger and have a wider dependency surface
   (wazero).

## Threat model

WASM plugins run untrusted code inside the request path. The following threats
are addressed by design, configuration, or runtime containment:

| Threat | Vector | Mitigation | Residual risk |
| ------ | ------ | ---------- | ------------- |
| Guest escape via memory corruption | Malformed `.wasm` or JIT bug | wazero is a pure-Go interpreter with no JIT/no cgo; the linear memory cap bounds blast radius; Go memory safety protects the host runtime | Unknown engine bug in wazero (defense-in-depth: keep wazero updated) |
| Infinite loop / CPU exhaustion | Guest spins without yielding | Per-invocation `timeout` (default 100 ms) enforced by context cancellation; guest torn down on overrun | Very short spike before cancellation (~timeout + scheduler jitter) |
| SSRF via `fetch` | Guest calls allowed host that redirects to private IP | `dialValidatedIPs` blocks loopback/private/link-local/CGNAT/multicast at dial time; redirect targets re-check allow-list | DNS rebinding to a *public* IP that later changes (low probability; TTL-dependent) |
| KV DoS (unbounded growth) | Guest fills KV with unbounded keys/values | `kv_max_entries` (default 1024) and `kv_max_bytes` (default 1 MiB) enforced per plugin; `kv_set` returns "quota exceeded" | Admin misconfigures quotas to very large values |
| Admin uploads malicious module | Attacker with admin token uploads crafted `.wasm` | Admin endpoint requires bearer token; upload disabled by default; filename hardened; path-traversal defense; module still sandboxed | Compromised admin token (rotate tokens, restrict admin to loopback/mTLS) |
| Information leak via guest error | Guest panics and leaks stack or data in error message | Panic is contained; the host returns generic "plugin error" `500`; guest log goes to server log, not the HTTP client | Server log exposed to attacker (standard log-hardening hygiene) |
| ABI compatibility breakage | New host release changes host function signature | ABI surface is golden-pinned (`abi-v1.golden`); additive-only policy within v1; breaking changes require new ABI id | Operator overrides golden check or builds from unreviewed ABI patch |

## Fuzz coverage

The following fuzz targets exercise the ABI boundary and guard logic:

| Target | File | What it fuzzes | Oracle |
| ------ | ---- | -------------- | ------ |
| `FuzzPluginInvoke` | `internal/plugins/fuzz_test.go` | Random request shape (method, URI, headers, body) into `header-inject.wasm` | No host panic; status ∈ [100,599]; no cross-invocation state leak |
| `FuzzHostAllowed` | `internal/plugins/fuzz_test.go` | Adversarial host strings and allow-lists | No panic; deterministic allow/block decision |

Run: `go test -tags wasmplugins -fuzz='FuzzPluginInvoke|FuzzHostAllowed' -fuzztime=30s ./internal/plugins/`

## GA status

| Criterion | Status | Evidence |
| --------- | ------ | -------- |
| ① Behaviour conformance matrix | **Met** | 19-row matrix above +
`internal/plugins/plugins_test.go` |
| ② Benchmarks | **Met** | 5 benchmarks in `internal/plugins/bench_test.go` |
| ③ Known limitations | **Met** | 5-item list above |
| ④ Compatibility policy | **Met** | Additive-only ABI policy, golden-pinned surface, prebuilt-guest tested; documented in [abi.md](abi.md) |
| ⑤ Soak test | **Met** | 8h Linux soak 2026-07-16 (21.7M+ requests at ~10K–20K req/s, 0 missing plugin headers, plugin executed correctly on 100% of successful responses) — [evidence](soak-evidence.md#2026-07-16--wasm-plugin-8h-isolated-soak-linux--authoritative-run) |
| ⑥ Feature documentation | **Met** | This document + [abi.md](abi.md) +
[configuration.md](configuration.md) |
| ⑦ Threat model | **Met** | 7-row threat table above |
| ⑧ Parser/input fuzzing | **Met** | `FuzzPluginInvoke`, `FuzzHostAllowed` in `internal/plugins/fuzz_test.go` |
| ⑨ Console surface | **Met** | Plugins panel (declare, attach, detach, upload) shipped in Console v2 |
