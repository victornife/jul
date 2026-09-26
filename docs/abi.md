# Plugin ABI compatibility: `jul-abi/v1` and `jul-abi/v2`

Jul.IA's WASM plugins talk to the host through a versioned ABI — a contract of
host import functions and guest exports. Two majors exist and **both are fully
supported**:

- **`jul-abi/v1`** — the request-phase ABI. Frozen.
- **`jul-abi/v2`** — v1's request phase plus an opt-in, bounded **response
  phase** that sees the response the location actually produced.

v2 is not "v1, but newer". It exists for one reason: letting a plugin act on the
real upstream/handler response. A plugin that does not need that should stay on
v1; nothing is deprecated.

This page is the canonical compatibility document for both. How to write,
build and configure a plugin is in [plugins.md](plugins.md); the design record
is [ADR 0020](adr/0020-wasm-abi-v2-response-phase.md).

## Contents

- [Which ABI should I use?](#which-abi-should-i-use)
- [Version comparison](#version-comparison)
- [Selecting an ABI](#selecting-an-abi)
- [Pipeline](#pipeline)
- [The response phase](#the-response-phase)
- [Body availability](#body-availability)
- [Mutation rules](#mutation-rules)
- [Errors](#errors)
- [Resource bounds](#resource-bounds)
- [The jul-abi/v2 host surface](#the-jul-abiv2-host-surface)
- [Migrating from v1 to v2](#migrating-from-v1-to-v2)
- [Compatibility guarantees](#compatibility-guarantees)
- [What is pinned](#what-is-pinned)

## Which ABI should I use?

**Use `jul-abi/v1` when the plugin only needs to:**

- inspect or rewrite the request (method, URI, headers, bounded body);
- block a request or answer it itself (Stop);
- act as a terminal handler (`type = "handler"`);
- set a response header *before* the request is forwarded;
- use the KV store or guarded `fetch`.

**Use `jul-abi/v2` when the plugin needs the actual downstream response**, for
example to:

- add or remove response headers based on the upstream's status or headers;
- inspect the final status (for example, mark 5xx responses `no-store`);
- redact or replace a small buffered text/JSON body;
- reject (fail closed) a backend response after the handler ran.

**Do not migrate without a reason.** v2 is not required because it is newer, a
v1 guest never needs rebuilding, and a request-only v2 plugin behaves (and
costs) the same as its v1 equivalent.

## Version comparison

| Capability | v1 | v2 |
| --- | --- | --- |
| Request hook (`handle_request`) | yes | yes |
| Request mutation (URI, headers, bounded body) | yes | yes |
| Short-circuit response (Stop) | yes | yes |
| KV store / guarded fetch | yes | yes |
| Actual downstream response status and headers | no | yes (per-request subscription) |
| Bounded downstream body inspection | no | yes, when [eligible](#body-availability) |
| Bounded body replacement | no | yes, when eligible |
| Reject the downstream response (fail closed) | no | yes |
| Explicit per-request state between phases | no | yes (≤ 4 KiB) |
| Streaming body transform | no | no |
| WebSocket frame hook | no | no |
| SSE event hook | no | no |
| gRPC message hook | no | no |

**Identical in both:** every v1 request-phase host function exists in v2 with
the same name, signature and semantics (v2 imports them from host module
`jul-abi/v2` instead of `jul`); request-phase `timeout`, `max_request_body`,
`max_response_body` (for Stop responses), KV, fetch, capability grants,
`max_invocations`, trap containment (`500 plugin error`) and the released
metric families.

**Intentionally different in v2 (all fail closed):**

| Behaviour | v1 | v2 |
| --- | --- | --- |
| Out-of-bounds guest pointer/length in a host call | ignored | the invocation fails (`500`, instance discarded) |
| `handle_request` result other than `0`/`1` | treated as Stop | the invocation fails (reserved for future actions) |
| Host module name | `jul` | `jul-abi/v2` |
| Declaration | none | `jul-abi/v2` marker export |

## Selecting an ABI

The ABI is chosen **explicitly** in configuration and **verified against the
module** before anything is published:

```toml
[plugins.redact]
path = "./plugins/v2-redact.wasm"
abi  = "jul-abi/v2"          # omitted = "jul-abi/v1"
```

| Configured | Module | Result |
| --- | --- | --- |
| `jul-abi/v1` (or unset) | v1 module (imports `jul`, exports `handle_request`) | loads |
| `jul-abi/v2` | v2 module (exports `jul-abi/v2` `()->()` and `handle_request` `()->i32`, imports only `jul-abi/v2`) | loads |
| `jul-abi/v1` | module exports `jul-abi/v2` or imports `jul-abi/v2` | rejected: *module declares jul-abi/v2 …* |
| `jul-abi/v2` | module lacks `jul-abi/v2` | rejected: *module does not declare jul-abi/v2 …* |
| `jul-abi/v2` | module imports `jul` | rejected |
| `jul-abi/v2` | wrong type for `jul-abi/v2`, `handle_request` or `handle_response`, or an import `jul-abi/v2` does not provide | rejected |

Rejections fail startup, the admin apply preflight and every reload **before
Publish**, naming the plugin; the serving generation keeps running. The check is
static (import/export tables) — no guest code runs to decide it. It proves host
and guest agree on one contract; it is not an authenticity check (see
[plugins.md](plugins.md#module-content-identity-and-pinning)).

The ABI is a separate dimension from the module digest: `sha256` pins and the
reported digest are unchanged, and changing `abi` is always a configuration
change (never a semantic no-op). Compiled code is ABI-independent — the host
module is linked when an instance is created — so the compilation cache may
reuse compiled bytes across ABIs without ever linking the wrong surface.

The Console shows each plugin's ABI, whether its serving module has a response
phase, and the response-body bound; its editor only sends an ABI when you change
it, and the change appears in the review diff.

## Pipeline

One HTTP request through a location with plugins, outermost first:

```text
client
  → RequestID → ClientAddr → Tracing → Metrics → AccessLog → Recover → Compression
    → response_headers + CORS policy → Recover(location)
      → handle_request   (v1 and v2; server plugins, then location plugins)
        → ClientCert → CORS preflight → Auth → RateLimit → WAF
          → ★ handle_response   (v2 subscriptions; innermost first)
            → BodyLimit → Cache → action (proxy | fastcgi | static | handler plugin | …)
```

What that position means — each row is pinned by a test:

| Concern | Behaviour |
| --- | --- |
| Cache | The hook runs on **every** response — miss, fill, hit, revalidated, stale. The cache stores the **origin** representation, so plugin output never depends on whether a response was cached and is never cached by Jul. |
| WAF | WAF **response** inspection (`response_body_check`) sees the **plugin's output**: a plugin cannot smuggle content past it. WAF request denials never reach the hook. |
| Auth / rate limit / client cert / CORS preflight | Their denials happen before the response point and are never presented; a plugin cannot turn a 401/429/403 into a success. |
| `response_headers` / CORS | Applied **after** the hook: a plugin cannot remove a `set` header, and on a CORS-enabled location Jul replaces every `Access-Control-*` header. |
| Compression | Applied **after** the hook: the guest sees the logical uncompressed body and Jul compresses the final one (gzip, brotli, zstd). |
| Error pages / upstream errors | Action responses (static `error_pages`, a proxy `502`) are presented like any other. |
| Metrics / access log | Count the final bytes on the wire. |
| Tracing | The server span covers both phases; no new span. |
| Several plugins | Nest like middleware: the last-listed plugin sees the action's response first; each outer one sees the inner one's output. |

Responses that do not come from the location's action — a policy denial,
another plugin's Stop, a handler panic — are never presented to
`handle_response`.

## The response phase

1. `handle_request` calls `subscribe_response(mode)` — `0` = headers only, `1` =
   headers + bounded body — and returns Continue. Unsubscribed requests cost
   nothing more. Stop discards the subscription. The last call wins.
2. Optionally it stores per-request state with `set_request_state` (≤ 4096 bytes).
3. The request runs through the location.
4. `handle_response` runs **once** for that request and returns `0` (deliver)
   or `1` (reject).

**The two phases may run on different module instances.** Instances are pooled
and shared across requests; no instance is held while the upstream works.
Package-level variables are therefore *never* per-request state — in v1 or v2.
Pass what the response phase needs through `set_request_state` /
`get_request_state`, or re-read the request with `get_method`, `get_uri`,
`get_request_header` (read-only in the response phase). The state belongs to one
request and one plugin and is released when the request ends.

Each phase is one invocation: it has the full `timeout` of its own and counts
toward the serving instance's `max_invocations`.

**When the hook runs:**

- with a headers-only subscription, or an ineligible body: when the action
  commits its status — **before** anything is sent, so headers and status are
  still mutable and a flush is never delayed;
- with an eligible body: when the action has finished (or when the body passes
  the cap, which makes it `TOO_LARGE`);
- after a protocol switch (`101`/hijack): after the handler returns,
  **read-only** — every mutation returns `COMMITTED` and only deliver is valid.

## Body availability

`resp_body_state()` returns one value from a closed enum:

| Value | Name | When |
| ---: | --- | --- |
| 0 | `AVAILABLE` | Complete identity body within `max_response_body` |
| 1 | `NOT_REQUESTED` | Subscribed headers-only |
| 2 | `NONE` | `HEAD`, `204`, `304` — no body by HTTP semantics |
| 3 | `TOO_LARGE` | Declared `Content-Length` or buffered bytes exceed `max_response_body` |
| 4 | `STREAMING` | `text/event-stream`, `application/grpc*`, `multipart/x-mixed-replace`, `X-Accel-Buffering: no`, or a declared `Trailer` |
| 5 | `ENCODED` | The action returned a non-identity `Content-Encoding` |
| 6 | `PARTIAL` | The action returned `206` |
| 7 | `UPGRADED` | Protocol switch; the response is already committed (read-only) |

A guest must treat any value it does not know as "not available".

| Case | Body for a body subscription |
| --- | --- |
| Ordinary buffered response | `AVAILABLE` |
| Cache hit / stale / revalidated | `AVAILABLE` (the stored origin body) |
| Compressed by Jul's `[compression]` | `AVAILABLE` — compression happens after the hook |
| Compressed by the origin | not requested: Jul removes `Accept-Encoding` from the request it forwards, so the origin sends identity; an origin that compresses anyway yields `ENCODED` |
| Range request | Jul removes `Range`/`If-Range` so the whole representation is presented; an origin `206` anyway yields `PARTIAL` |
| SSE | `STREAMING`, never buffered; headers still mutable |
| WebSocket / upgrade | `UPGRADED`, observed read-only after the fact |
| Native gRPC (`application/grpc*`) | `STREAMING` |
| Oversized | `TOO_LARGE`; the full body still reaches the client untouched |
| `HEAD`, `204`, `304` | `NONE` |

Jul never truncates a body and presents it as complete, never buffers a
declared stream, and never skips a subscribed hook silently. A flush from the
action does **not** end buffering — Go's reverse proxy flushes every response of
unknown length — so an undeclared slow stream is delivered when it completes or
passes the cap. Removing `Range`/`Accept-Encoding` exists so that a client cannot
turn a body a redaction plugin must see into one it cannot inspect.

## Mutation rules

Every read is caller-allocates and bounded; every write is validated before it
touches the response. Return codes (i32): `0 OK`, `-1 NOT_FOUND`, `-2 INVALID`,
`-3 FORBIDDEN`, `-4 UNAVAILABLE`, `-5 TOO_LARGE`, `-6 COMMITTED`,
`-7 WRONG_PHASE`, `-8 UNSUPPORTED`.

- **Status.** 200–599 except 204, 205 and 304 (`INVALID`). A `204`/`304` from the
  action cannot be changed (`FORBIDDEN`).
- **Headers.** Names must be RFC 9110 tokens and values must not contain CR, LF
  or NUL (`INVALID` — no response splitting). `Connection`, `Keep-Alive`,
  `Proxy-Connection`, `Transfer-Encoding`, `TE`, `Trailer`, `Upgrade`,
  `Content-Length`, `Content-Encoding` and `Content-Range` are `FORBIDDEN`.
  Duplicate values are readable by index; `set` replaces all values, `add`
  appends, `del` removes all (and succeeds when absent). At most 64 KiB of
  header names+values per invocation (`TOO_LARGE`).
- **Body.** Replace the whole body with `resp_body_replace` (≤
  `max_response_body`, last call wins).
- **The host owns framing.** For a buffered body Jul sets `Content-Length`,
  removes `Accept-Ranges`, and — if the body was replaced — removes `ETag`,
  `Content-MD5`, `Digest`, `Content-Digest` and `Repr-Digest`. A headers-only hook
  leaves the body and its framing untouched.
- **Reject.** Returning `1` discards the action's response: the header map
  returns to what it was before the action ran, the status is the one set with
  `resp_set_status` when it is 400–599 (else `502`), and the body is empty.

## Errors

| Failure | Result |
| --- | --- |
| `handle_request` trap, panic, timeout or contract violation | `500 plugin error` (v1 behaviour); the next handler is not called |
| `handle_response` trap, panic, timeout, out-of-bounds pointer, request-mutating call, reserved result — before commitment | the whole action response is discarded and the client receives `500 plugin error`; nothing partially mutated is ever sent |
| The same after a protocol switch | recorded as an error; the connection is untouched |
| Client cancelled the request | the invocation fails like a timeout |

In every failure the instance is discarded, never returned to the pool, and the
failure is counted (`jul_plugin_panics_total` for traps/timeouts,
`result="error"` in the invocation families). Jul keeps serving.

## Resource bounds

| Bound | Value |
| --- | --- |
| Buffered response body / replacement | `max_response_body` (default 8 MiB; at most 1 GiB for v2) — the same limit v1 applies to Stop bodies |
| Per-phase deadline | `timeout` per invocation (default 100 ms) |
| Request state | 4096 bytes |
| Added header bytes | 64 KiB per invocation |
| Instances | the bounded pool (#420); none held between phases |

Host memory for a body subscription is at most `max_response_body` per
subscribed response per plugin, released when the response is written;
aggregate pressure follows request concurrency exactly like `max_request_body`.
Reading and replacing a body copies it into and out of guest memory: give a
body-transform plugin a `memory_limit` of roughly `2 × max_response_body` plus
its own working set, or it will trap (and fail closed).

Metrics (closed labels): the released `jul_plugin_invocations_total`,
`jul_plugin_duration_seconds` keep counting `handle_request` only;
`jul_plugin_panics_total` counts contained failures in either phase; new:
`jul_plugin_response_invocations_total{plugin,result=continue|reject|error}`,
`jul_plugin_response_duration_seconds{plugin}`,
`jul_plugin_response_body_unavailable_total{plugin,reason}` (body subscriptions
only; `reason` is the lower-case state name).

## The `jul-abi/v2` host surface

Host module `jul-abi/v2`. All parameters are `i32`.

**Guest exports**

| Export | Type | |
| --- | --- | --- |
| `jul-abi/v2` | `() -> ()` | required ABI declaration; never called |
| `handle_request` | `() -> i32` | required; `0` Stop, `1` Continue |
| `handle_response` | `() -> i32` | optional; `0` deliver, `1` reject |

**Request-phase functions** — identical to v1 (see
[plugins.md](plugins.md#the-jul-abiv1-abi)): `log`, `get_method`, `get_uri`,
`set_uri`, `get_request_header`, `set_request_header`, `set_response_header`,
`read_request_body`, `write_response_body`, `set_response_status`, `get_config`,
`kv_get`, `kv_set`, `fetch`, `last_fetch_len`, `last_fetch_truncated`,
`fetch_read`. The readers (`log`, `get_method`, `get_uri`, `get_request_header`,
`get_config`, KV, fetch) also work in `handle_response`; the mutators fail the
invocation there.

**Phase functions**

| Function | Signature | Phase | Returns |
| --- | --- | --- | --- |
| `subscribe_response` | `(mode) -> i32` | request | `OK`; `INVALID` unknown mode; `UNSUPPORTED` handler plugin or no `handle_response` export |
| `set_request_state` | `(ptr, len) -> i32` | request | `OK`; `TOO_LARGE` over 4096 bytes |
| `get_request_state` | `(buf, limit) -> i32` | both | full length (caller-allocates) |

**Response functions** (`WRONG_PHASE` from `handle_request`)

| Function | Signature | Returns |
| --- | --- | --- |
| `resp_status` | `() -> i32` | current status |
| `resp_set_status` | `(code) -> i32` | `OK`, `INVALID`, `FORBIDDEN`, `COMMITTED` |
| `resp_get_header` | `(namePtr, nameLen, index, buf, limit) -> i32` | full length of the `index`th value; `NOT_FOUND` |
| `resp_header_names` | `(buf, limit) -> i32` | full length of the sorted names joined by `\n` |
| `resp_set_header` | `(namePtr, nameLen, valPtr, valLen) -> i32` | `OK`, `INVALID`, `FORBIDDEN`, `TOO_LARGE`, `COMMITTED` |
| `resp_add_header` | `(namePtr, nameLen, valPtr, valLen) -> i32` | as `resp_set_header` |
| `resp_del_header` | `(namePtr, nameLen) -> i32` | `OK`, `INVALID`, `FORBIDDEN`, `COMMITTED` |
| `resp_body_state` | `() -> i32` | [body state](#body-availability) |
| `resp_body_read` | `(buf, limit) -> i32` | full length; `UNAVAILABLE` |
| `resp_body_replace` | `(ptr, len) -> i32` | `OK`, `UNAVAILABLE`, `TOO_LARGE`, `COMMITTED` |

Guests using the SDK never call these directly: `juliaplugins/sdk/v2`
(`examples/plugins/sdk/v2`) wraps them as `Request.SubscribeResponse`,
`Request.SetState`, `Response.Status`, `Response.Header`, `Response.Body`,
`Response.ReplaceBody`, `sdk.Reject`, and so on.

## Migrating from v1 to v2

Only migrate a plugin that needs the response. A v1 plugin that adds a fixed
header before forwarding:

```go
import "juliaplugins/sdk"

func init() {
	sdk.Handle = func(req *sdk.Request) sdk.Action {
		req.SetResponseHeader("X-Edge", "jul")
		return sdk.Continue
	}
}
```

becomes a v2 plugin that labels the response the upstream actually returned:

```go
import sdk "juliaplugins/sdk/v2"

func init() {
	sdk.HandleRequest = func(req *sdk.Request) sdk.Action {
		req.SetResponseHeader("X-Edge", "jul")     // unchanged request-phase call
		_ = req.SubscribeResponse(sdk.Headers)     // ask for this request's response
		return sdk.Continue
	}
	sdk.HandleResponse = func(resp *sdk.Response) sdk.Verdict {
		if resp.Status() >= 500 {
			_ = resp.SetHeader("Cache-Control", "no-store")
		}
		return sdk.Deliver
	}
}
```

Then rebuild (`GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared …`), set
`abi = "jul-abi/v2"` on the declaration, and apply. Checklist:

1. `sdk.Handle` → `sdk.HandleRequest`; the request API is the same.
2. Subscribe explicitly, per request; choose `sdk.Body` only when you read or
   replace the body (it delays delivery until the response completes).
3. Move any per-request data from package variables to `req.SetState` /
   `resp.State()`.
4. Handle every `BodyState` — decide whether an unavailable body means deliver
   or `sdk.Reject`.
5. Size `memory_limit` for body transforms (≈ 2 × `max_response_body`).

Examples: [`header-inject`](../examples/plugins/header-inject) (v1 request-only),
[`v2-status-header`](../examples/plugins/v2-status-header) (v2 headers-only),
[`v2-redact`](../examples/plugins/v2-redact) (v2 bounded body transform).

## Compatibility guarantees

The ABI identifier is `<name>/v<major>`. A binary-incompatible change is a new
major, never an in-place edit.

| Change | Allowed within a major? |
| --- | --- |
| Add a host function | ✅ additive (old guests ignore it) |
| Add a guest export the host calls only when present | ✅ additive |
| Add a new return code, body state, subscription mode, or result value where the current value is documented as reserved/failing | ✅ additive (v2) |
| Loosen a numeric return-code meaning (new negative code) | ✅ additive (v1) |
| Rename, retype or remove a host function | ❌ new major |
| Repurpose a numeric value | ❌ new major |
| Change when the response hook runs, or hold an instance across phases | ❌ new major (v3) |

- **v1** is frozen: its `jul` host module, `handle_request` semantics and return
  codes never change, and v1 guests never need rebuilding.
- **v2** grows only additively inside `jul-abi/v2`; a v2 guest keeps running on every
  later v2 host. A future bounded streaming mode (#444) would be a new
  subscription mode, not a v3.
- Both are registered side by side; plugins migrate at their own pace.

## What is pinned

- [testdata/plugins/abi-v1.golden](../testdata/plugins/abi-v1.golden) — the v1
  host surface (names and value types), checked by `TestABIV1Golden`; the v2
  work did not touch it (`TestABIV1HostModuleHasNoV2Surface`).
- [testdata/plugins/abi-v2.golden](../testdata/plugins/abi-v2.golden) — the whole
  v2 contract: every `jul-abi/v2` import signature, required/optional exports, every
  result, mode, body-state and return-code value, and the per-invocation bounds,
  checked by `TestABIV2Golden`.

Regenerate a golden only for an intentional additive change:

```bash
UPDATE_ABI_GOLDEN=1 go test -tags wasmplugins -run 'TestABIV[12]Golden' ./internal/plugins/
```

**Prebuilt-guest fleet.** `testdata/plugins/*.wasm` are committed guests run by
the plugin tests on every CI run: the historical v1 guests (`header-inject`,
`request-block`, `kv-counter`, `testguest-*`, built 2026-06 and never rebuilt),
a current-toolchain v1 guest (`v1-current-header-inject`), and the v2 guests
(`v2-status-header`, `v2-redact`, `testguest-v2`). `examples/plugins/build.sh`
refreshes only the current and v2 fixtures, built with `-trimpath -buildvcs=false`
so CI can prove they equal a fresh build of their sources. Keep old fixtures
across releases so backward compatibility stays covered.

## CI

The golden, negotiation, conformance and prebuilt-guest suites run under the
`wasmplugins` tag, which is in `FULL_TAGS`, so they execute in the standard test,
race and coverage jobs. The `plugin-examples` job builds every example from
source for `wasip1` and runs the v2 suites against the fresh builds.
