# ADR 0020 — `jul-abi/v2`: a bounded post-upstream response phase

- Status: Accepted
- Date: 2026-09-26
- Deciders: Jul.IA maintainers
- Applies to: `internal/plugins`, `internal/app` handler composition, `[plugins.*]` configuration,
  the plugin admin projection and Console, the guest SDK (`examples/plugins/sdk`)
- Source: #430 (Wave 4). Builds on #420 (bounded instance pool), #428 (resource ownership), #429
  (module content identity). #444 (streaming bodies) stays out of scope.

## Context

`jul-abi/v1` is a request-phase contract. A guest's `handle_request` can inspect and mutate the
request, set response headers before the next handler runs, or short-circuit with its own response.
It never sees the response the location action (proxy, FastCGI, static, handler plugin, cache)
actually produced, so it cannot add a header based on the upstream status, redact a small JSON body,
or reject a backend response that leaks an error page.

The tempting answer — a generic streaming response hook — would couple the ABI to HTTP/2 and HTTP/3
flow control, SSE, WebSocket, gRPC framing, retries, compression, cache and WAF all at once. It would
also make every response hook a potential unbounded buffer. v1 is frozen (docs/abi.md), so any
response phase is a new major.

The audit of the real pipeline (internal/app/factory.go `buildHandlers`/`globalChain`,
internal/router/router.go `buildServerRoute`) found this per-request order, outermost first:

```text
RequestID → ClientAddr → Tracing → Metrics → AccessLog → Recover → Compression
  → Router → [location] ResponsePolicy (response_headers + CORS) → Recover(location)
    → plugin middleware (server list, then location list)          ← v1 request hooks
      → ClientCert → CORS preflight → Auth → RateLimit → WAF
        → BodyLimit → Cache → action (proxy | fastcgi | static | handler plugin | …)
```

Three properties of that order drive this ADR:

1. Plugins run *outside* Auth, RateLimit and WAF. A response hook at the same position would see —
   and could rewrite — Jul's own policy denials (401, 429, WAF 403) and would run *after* WAF
   response-body inspection, letting a plugin re-insert what the WAF inspected away.
2. ResponsePolicy and Compression are *outside* the plugins. Anything a plugin does to the response
   is therefore still subject to `response_headers`, CORS header replacement and on-the-wire
   compression, and Metrics/AccessLog count the final bytes.
3. The cache sits *inside* BodyLimit, directly around the action, and stores the handler's own
   representation (it snapshots headers relative to the map it was entered with, #332).

## Decision

### 1. ABI identity and version negotiation

The ABI is selected **explicitly per plugin** in configuration and **verified against the module**
before Publish. Neither side alone is enough: auto-detection would let a rebuilt `.wasm` silently
change a plugin's phase model behind an unchanged config, and a config-only switch would let a v1
binary be run under v2 rules.

- New leaf `[plugins.NAME] abi = "jul-abi/v1" | "jul-abi/v2"`. Empty means `jul-abi/v1`, so every
  existing configuration keeps its meaning. Any other value fails validation.
- A **v2 module** must:
  - export the marker function `jul-abi/v2` with type `() -> ()` (never called);
  - export `handle_request` with type `() -> i32`;
  - import host functions only from the host module **`jul-abi/v2`**, never from `jul`;
  - optionally export `handle_response` with type `() -> i32`.
- A **v1 module** must not export `jul-abi/v2` and must not import from `jul-abi/v2`. The existing v1
  check (it exports `handle_request`) is unchanged.
- Every mismatch — v1 config with a v2 module, v2 config with a v1 module, a module importing both
  `jul` and `jul-abi/v2`, a missing or wrongly-typed required export, an import of a `jul-abi/v2` function the
  host does not provide — fails the plugin build and therefore startup, the admin apply preflight and
  every reload **before Publish**, with an error naming the plugin and the declared/observed ABI.
- The negotiation is static (export/import tables and function types). No guest code runs to decide
  the ABI.

The v2 host module and the marker export are both named after the ABI identifier (`jul-abi/v2`), a
separate host module so the v1 surface (`jul`) and its golden stay byte-for-byte frozen and a v2
guest can never bind a v1 function by accident; a future v3 would use `jul-abi/v3`. (The name also
stays outside the `jul_*` Prometheus metric namespace the docs tooling reserves.) The check is a *compatibility*
check, not an authentication: a module author can always claim v2. What the check guarantees is that
host and guest agree on one contract, never that the guest is trustworthy (that remains #439).

### 2. Request-phase compatibility

Every v1 request-phase host function exists in `jul-abi/v2` with the **same name, signature and
semantics** (`log`, `get_method`, `get_uri`, `set_uri`, `get_request_header`, `set_request_header`,
`set_response_header`, `read_request_body`, `write_response_body`, `set_response_status`,
`get_config`, `kv_get`, `kv_set`, `fetch`, `last_fetch_len`, `last_fetch_truncated`, `fetch_read`).
Migrating a request-only plugin is a recompile against the v2 SDK. Intentional differences, all
fail-closed:

| Behaviour | v1 | v2 |
| --- | --- | --- |
| Out-of-bounds guest pointer or length in any host call | ignored (call is a no-op / returns 0) | the invocation fails: `500`, instance discarded |
| `handle_request` result other than `0`/`1` | treated as Stop | the invocation fails (`2..` reserved for future actions) |
| Request-mutating calls (`set_uri`, `set_request_header`, `set_response_header`, `read_request_body`, `write_response_body`, `set_response_status`) from `handle_response` | n/a | the invocation fails (contract violation) |
| Response-phase calls from `handle_request` | n/a | return `WRONG_PHASE` (`-7`) |

### 3. Response hook and export model

The response phase is **subscribed per request**, not implied by an export:

- `handle_request` calls `subscribe_response(mode)` to ask for a response callback for *this*
  request: `mode 0 = METADATA` (status + headers), `mode 1 = BODY` (status + headers + the bounded
  buffered body when eligible). The last call wins. A request that is not subscribed costs nothing
  after `handle_request`.
- `subscribe_response` returns `UNSUPPORTED` (`-8`) when the plugin is `type = "handler"` or the
  module does not export `handle_response`, and `INVALID` (`-2`) for an unknown mode.
- The subscription is honoured only if `handle_request` returns Continue (`1`). A Stop discards it:
  the guest produced the response itself.
- `handle_response() -> i32` returns `0 = CONTINUE` (deliver the possibly-mutated response) or
  `1 = REJECT` (discard it; see §7). Any other value fails the invocation (reserved).

Per-request subscription keeps the unsubscribed path at v1 cost, lets a plugin buffer only the
responses it actually needs, and gives future modes (for example a bounded streaming mode for #444)
an additive slot without a v3.

### 4. Request ↔ response guest-instance relationship

**The two phases are separate invocations that may run on different instances.** An instance is
acquired from the plugin's bounded pool for `handle_request`, released, and a (possibly different)
instance is acquired for `handle_response`. No instance is held across upstream I/O.

Holding one instance per in-flight request would make live guest memory proportional to in-flight
requests (a slow upstream or a WebSocket would pin a whole Go/wasip1 heap for its duration), which
breaks the bounded-resource model #420 established. Guest globals are per *instance*, never per
request — true in v1 already — so the SDK and docs state it plainly and v2 adds an explicit,
host-owned channel instead:

- `set_request_state(ptr, len) -> i32` (request phase only) stores up to **4096 bytes** of opaque
  per-request state; `TOO_LARGE` beyond that. `get_request_state(buf, limit) -> i32` (either phase)
  returns it (caller-allocates, returns the full length; `0` when unset).
- The state lives in the request's phase record, is scoped to that request and that plugin, and is
  released when the request completes. Nothing survives to the next request.
- The response phase can also read (never mutate) the request as the action received it:
  `get_method`, `get_uri`, `get_request_header`, `get_config`.

### 5. Body eligibility — one function

`BODY` subscriptions are classified by a single eligibility function (`classifyResponse`,
internal/plugins) at the moment the action commits its status, and again if the buffered body grows
past the cap. The result is a closed enum returned by `resp_body_state()`:

| Value | Name | Meaning | Hook runs | Body |
| ---: | --- | --- | --- | --- |
| 0 | `AVAILABLE` | Complete identity body within `max_response_body` | after the action completes | read / replace |
| 1 | `NOT_REQUESTED` | Subscribed with `METADATA` | when the action commits its status | — |
| 2 | `NONE` | No body by HTTP semantics: `HEAD`, `204`, `304` | when the action commits its status | — |
| 3 | `TOO_LARGE` | Declared `Content-Length`, or buffered bytes, exceed `max_response_body` | at the status, or at the first byte past the cap | — |
| 4 | `STREAMING` | `text/event-stream`, `application/grpc*`, `multipart/x-mixed-replace`, `X-Accel-Buffering: no`, or a declared `Trailer` | when the action commits its status | — |
| 5 | `ENCODED` | The action returned a non-identity `Content-Encoding` | when the action commits its status | — |
| 6 | `PARTIAL` | The action returned `206` | when the action commits its status | — |
| 7 | `UPGRADED` | Protocol switch (`101` / hijack) — already committed | after the handler returns, **read-only** | — |

Rules that make the enum trustworthy:

- Jul never truncates and presents the prefix as the body; `TOO_LARGE` means *no* body.
- Jul never buffers a declared stream. A `Flush` from the action while buffering does **not** end
  buffering, because Go's reverse proxy flushes every response of unknown length (the cache made the
  same decision, internal/cache/http.go); buffering is bounded by `max_response_body` instead.
- For a `BODY` subscription Jul asks the action for the whole identity representation: it removes
  `Range`, `If-Range` and `Accept-Encoding` from the request handed to the action (a header clone;
  outer layers — compression in particular — still see the client's headers). `ENCODED` and
  `PARTIAL` therefore only occur when an origin ignores those rules, and a client cannot turn a body
  it may not see into one it can by sending `Range` or `Accept-Encoding`.
- A metadata-only hook for an unavailable body still runs **before** commitment, so headers and
  status stay mutable; only `UPGRADED` is observed after the fact.
- The response phase is not invoked for a response that never reaches the response point (§6): a
  denial produced by ClientCert, CORS preflight, Auth, RateLimit or WAF request inspection, another
  plugin's Stop, or a handler panic.

### 6. Pipeline position

```text
RequestID → ClientAddr → Tracing → Metrics → AccessLog → Recover → Compression
  → ResponsePolicy (response_headers + CORS) → Recover(location)
    → v1/v2 request hooks: handle_request (server plugins, then location plugins)
      → ClientCert → CORS preflight → Auth → RateLimit → WAF (request + response inspection)
        → ★ v2 RESPONSE POINT: handle_response (innermost subscription first)
          → BodyLimit → Cache → action
```

The response point is the innermost per-location modifier: **inside WAF, outside BodyLimit and the
cache**. Consequences, each pinned by a test:

| Concern | Decision |
| --- | --- |
| Cache | The hook runs on **every** response the location serves — miss, fill, hit, revalidated, stale — because the cache is inside it. The cache stores the **pre-plugin** (origin) representation; a plugin's output is never cached by Jul's cache and never depends on whether the entry was cached. The hook sees the cache's `X-Cache` header like any other header. |
| WAF | WAF request inspection runs before the action and is not visible to the hook. WAF **response** inspection (`response_body_check`) inspects the **post-plugin** response, so a plugin cannot re-introduce content the WAF blocks. A WAF denial is not presented to the hook. |
| Auth / RateLimit / ClientCert / preflight | Their denials happen before the response point and are never presented; a plugin cannot turn a 401/429/403 into a success. |
| CORS / `response_headers` | Applied **after** the hook (ResponsePolicy is outside). A plugin cannot remove a `response_headers` `set`, and on a CORS-enabled location every `Access-Control-*` header a plugin sets is replaced by the policy. Where no policy exists, a plugin has the same authority over those headers as the origin. |
| Compression | Applied **after** the hook: the guest always sees the logical, uncompressed body Jul's action produced, and compression encodes the final body. |
| Error pages | `error_pages` are rendered by the static action, so they are ordinary action responses and are presented. |
| Metrics / access log | Count the final bytes on the wire, after the hook and compression. |
| Tracing | The server span covers both phases; no new span or attribute is added. |

Multiple response subscriptions nest like middleware: the plugin listed last (innermost) sees the
action's response first, and each outer subscription sees the output of the inner one.

### 7. Status, header and body mutation semantics

All reads are caller-allocates and bounded; all writes are validated before they touch the response.
Return codes (closed, i32): `0 OK`, `-1 NOT_FOUND`, `-2 INVALID`, `-3 FORBIDDEN`, `-4 UNAVAILABLE`,
`-5 TOO_LARGE`, `-6 COMMITTED`, `-7 WRONG_PHASE`, `-8 UNSUPPORTED`.

- **Status.** `resp_status() -> i32`. `resp_set_status(code) -> i32` accepts 200–599 except
  204, 205 and 304 (`INVALID`); it is `FORBIDDEN` when the action's status is 204 or 304 (their
  meaning depends on having no body) and `COMMITTED` after a protocol switch.
- **Headers.** `resp_get_header(namePtr, nameLen, index, buf, limit) -> i32` returns the `index`th
  value (`NOT_FOUND` past the end), so duplicate headers are fully readable.
  `resp_header_names(buf, limit) -> i32` returns the canonical names, sorted, joined by `\n`.
  `resp_set_header` replaces all values, `resp_add_header` appends one, `resp_del_header` removes all.
  Names must be RFC 9110 tokens and values must not contain CR, LF or NUL (`INVALID` — no response
  splitting). Framing and hop-by-hop headers are `FORBIDDEN`: `Connection`, `Keep-Alive`,
  `Proxy-Connection`, `Transfer-Encoding`, `TE`, `Trailer`, `Upgrade`, `Content-Length`,
  `Content-Encoding`, `Content-Range`, and any trailer-prefixed name. One invocation may add at most
  64 KiB of header names and values (`TOO_LARGE`).
- **Body.** `resp_body_state() -> i32` (§5). `resp_body_read(buf, limit) -> i32` returns the full
  length (`UNAVAILABLE` unless `AVAILABLE`). `resp_body_replace(ptr, len) -> i32` replaces the whole
  body (last call wins; `UNAVAILABLE` unless `AVAILABLE`; `TOO_LARGE` beyond `max_response_body`).
- **Host normalization.** The host, never the guest, owns framing. For a buffered body Jul sets
  `Content-Length` to the final length, removes `Accept-Ranges`, and — when the body was replaced —
  removes the validators and digests that described the old bytes (`ETag`, `Content-MD5`,
  `Digest`, `Content-Digest`, `Repr-Digest`; `Last-Modified` describes the resource, not the bytes,
  and is kept). A metadata-only hook leaves the body
  stream and its framing untouched.
- **REJECT.** Returning `1` discards the action's response: the header map returns to what it was
  before the action ran (headers the action and the guest added are dropped; headers outer layers
  set earlier, such as `X-Request-Id`, stay), the status is the one set by `resp_set_status` when
  it is 400–599, otherwise `502`, and the body is empty.
  REJECT is available whenever the response is not committed, including for unavailable bodies, so a
  policy plugin can fail closed on a body it could not inspect.

### 8. Failure semantics

| Failure | Behaviour |
| --- | --- |
| `handle_request` trap, panic, timeout, host-side violation | Identical to v1: `500 plugin error`, the next handler is not called, the instance is discarded. |
| `handle_response` trap, panic, timeout, host-side violation (out-of-bounds pointer, request-mutating call) or reserved result, **before commitment** | The action's response — buffered bytes, headers and status alike — is discarded and the client receives `500 plugin error`. Nothing partially mutated is ever sent. The instance is discarded. |
| The same, **after commitment** (`UPGRADED`) | Recorded (`result="error"`, panic counter) and the instance discarded; the connection is already owned by the upgraded protocol and is not touched. |
| REJECT | As §7. Not an error. |
| Request cancelled by the client | The invocation runs under the request context; a cancelled context fails it like a timeout (v1 behaviour). |
| Instance acquisition failure | Counted as a contained failure; before commitment the client receives `500 plugin error`. |

When Jul discards an in-progress response (REJECT or failure), it cancels the context handed to the
action so an upstream stop reading promptly; the resulting reverse-proxy abort
(`http.ErrAbortHandler`) is absorbed by the response point, which alone knows it caused it.

### 9. Timeout and invocation accounting

- `timeout` applies **per invocation**: `handle_request` and `handle_response` each get the full
  budget. There is no combined budget, so a slow upstream never eats the guest's time.
- Each invocation is one call on the instance that served it and counts toward that instance's
  `max_invocations` retirement bound (#420).
- The released families keep their meaning: `jul_plugin_invocations_total{plugin,result}` and
  `jul_plugin_duration_seconds{plugin}` count **`handle_request`** invocations only, so enabling v2 does
  not double any existing series; `jul_plugin_panics_total{plugin}` counts every contained guest
  failure in either phase. New families, closed labels only:
  - `jul_plugin_response_invocations_total{plugin,result}` — `result` ∈ `continue`, `reject`, `error`;
  - `jul_plugin_response_duration_seconds{plugin}`;
  - `jul_plugin_response_body_unavailable_total{plugin,reason}` — counted for `BODY` subscriptions
    only; `reason` ∈ `none`, `too_large`, `streaming`, `encoded`, `partial`, `upgraded`.

### 10. Resource bounds

- **`max_response_body` is reused, not duplicated.** In v1 it caps the body a guest writes with
  `write_response_body`; in v2 it additionally caps the action body Jul buffers for a `BODY`
  subscription and the replacement body. Same meaning — the largest response body a plugin handles —
  same default (8 MiB). No new limit leaf exists.
- Host buffering: at most `max_response_body` per `BODY`-subscribed response per plugin layer,
  released when the response is written. Aggregate pressure is bounded by request concurrency and
  upstream admission, exactly like request-body buffering (`max_request_body`); operators size the
  two together.
- Guest memory: reading a body copies it into guest linear memory and a replacement copies it out, so
  a body transform needs roughly `2 × max_response_body` of headroom inside `memory_limit`. A guest
  that exceeds `memory_limit` traps and the §8 rule applies. The docs state the sizing rule.
- Request state ≤ 4096 bytes; added headers ≤ 64 KiB per invocation; every getter is caller-allocates.

### 11. Pooling and lifetime (#428)

v2 adds no owner. Per docs/resource-ownership.md: the process owns the `Manager` (compilation cache,
KV store and ledger); the generation owns the `Set`, each plugin's `wazero.Runtime`, its compiled
module and its instance pool. An instance is owned by exactly one invocation between acquire and
release, and is either returned to the pool or closed — never dropped. A trap, timeout, host-side
violation or invocation-cap hit closes it. A response invocation acquires from the pool of the
**same generation** that ran the request hook (the phase record carries the plugin pointer), so a
reload mid-request cannot run the response phase on a newer module; the old generation's runtime is
closed only after its in-flight requests drain, which includes their response phase. The phase record
(subscription + ≤4 KiB state) is request-scoped heap memory, not a runtime resource.

### 12. Content identity (#429) and the compilation cache

The module digest is unchanged: it identifies bytes, not the ABI. The ABI is a separate, explicit
configuration dimension:

- A change of `abi` is a configuration change and therefore never a semantic no-op; the admin diff
  reports it as its own field and the projection reports it as its own field.
- wazero's compilation cache keys compiled code by the bytes' SHA-256 plus compile-affecting runtime
  flags. Compiled code is ABI-independent — the host module is linked at instantiation, and each
  generation builds a fresh runtime whose host module matches its configured ABI — so the same bytes
  under a different ABI legitimately reuse compiled code and can never link the wrong host surface.

### 13. Evolution inside v2

Additive within `jul-abi/v2` (no v3): new `jul-abi/v2` host functions; new optional guest exports the
host calls only when present; new `subscribe_response` modes; new `resp_body_state` values, new
return codes and new `handle_request`/`handle_response` result values *only* where the current value
is documented as reserved/failing. Guests must treat an unknown body state as "not available". Every
existing name, signature, numeric value and action meaning is frozen by
[testdata/plugins/abi-v2.golden](../../testdata/plugins/abi-v2.golden). A **v3** is required to
rename, retype or remove a function, change a numeric value's meaning, hold an instance across
phases, or change the pipeline position.

### What v2 deliberately does not support

Chunk callbacks or streaming transforms, WebSocket frames, SSE events, gRPC messages, response
trailers, cross-plugin shared state, async guest execution, responses from `handler`-type plugins
passing through their own response hook, and any body beyond `max_response_body`. #444 remains the
place to research bounded streaming.

## Rejected alternatives

- **Response hook at the request-hook position (outside Auth/WAF).** Simplest wiring, but it lets a
  plugin rewrite Jul's policy denials and runs after WAF response inspection — middleware order would
  define security policy.
- **Same instance for both phases.** Simplest SDK story, but live guest memory would scale with
  in-flight requests and long-lived streams would pin instances.
- **`handle_response` export presence alone subscribes.** The SDK must export the function
  unconditionally, so every request would pay a second guest call and every body plugin would buffer
  every response.
- **Flush ends buffering.** Makes every chunked proxied response ineligible.
- **A new response-buffer limit.** Overlaps `max_response_body` with no distinct meaning.
- **Adding a `phase` label to the released plugin families.** Changes released series in place.
- **Presenting the encoded body when upstream compressed it.** Forces authors to parse gzip/brotli and
  would be an attacker-controlled bypass through `Accept-Encoding`.

## Consequences

- v1 guests, the v1 host module and `abi-v1.golden` are untouched; existing configurations keep
  running with `abi` unset.
- A `BODY` subscription trades time-to-first-byte for inspection: the client receives the response
  once it is complete (or once it passes the cap). Streams that declare themselves are never delayed;
  undeclared slow chunked responses are, and should not be attached to body plugins.
- Operators get one new configuration leaf (`abi`), three bounded metric families and new projection
  fields; nothing existing changes meaning.

## Required tests

Golden: v1 unchanged; separate v2 golden for host imports, required/optional exports, every numeric
constant. Compatibility: historical and current v1 fixtures, a v2 fixture, malformed/mismatched v2
modules. Response semantics: status/header/body read and write, every error code, empty and
oversized bodies, every unavailable reason. Pipeline: cache miss/fill/hit, gzip/brotli/zstd, WAF
response inspection, CORS and `response_headers`, access-log/metrics bytes, error responses.
Protocol exclusions: SSE, WebSocket, gRPC, 206. Failure: trap, timeout, bad pointer, oversized
write, reserved result, cancellation. Lifecycle: reload under active requests, v1↔v2 replacement,
same digest with a different ABI, pool retirement, shutdown quiescence, `-race`. Benchmarks: v1
request-only, v2 request-only, v2 metadata, v2 body read, v2 body replace.

## Reversibility

| Decision | Reversible within v2? |
| --- | --- |
| Separate `jul-abi/v2` host module | No (v3) |
| Per-phase instances + request state | No (v3) |
| Response point inside WAF, outside cache | No (v3) |
| Per-request subscription modes | Extensible (new modes) |
| Body-state and return-code values | Extensible (new values) |
| `max_response_body` reuse and 4 KiB / 64 KiB bounds | Bounds may be raised by configuration in a later additive change |

## Related

- [docs/abi.md](../abi.md) — the canonical v1/v2 compatibility document.
- [docs/plugins.md](../plugins.md) — authoring and configuration.
- [docs/resource-ownership.md](../resource-ownership.md) — owners and lifetimes (#428).
- [ADR 0018](0018-bounded-route-matching-and-response-policy.md) — ResponsePolicy placement.
- #420, #428, #429, #430, #439, #444.
