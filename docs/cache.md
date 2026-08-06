# Response cache

Jul.IA can cache upstream responses in front of proxied backends and FastCGI/uWSGI
apps. The cache is **two-tier**: a fast in-memory tier backed by an optional
on-disk overflow tier that survives process restarts. Both tiers are part of the
core binary — there is no build tag to enable.

The cache is opt-in per location (`cache = true`) and gated by the global
`[cache].enabled` switch. Cacheability is designed around standard HTTP semantics (`Cache-Control`,
`Expires`, `Vary`), but the shared-cache contract is currently under active
recertification. The historical behavior matrix below is not complete release
evidence until #107 and #131–#134 close.

## Contents

- [Two-tier model](#two-tier-model)
- [Configuration](#configuration)
- [Cache key and Vary](#cache-key-and-vary)
- [Freshness and stale-while-revalidate](#freshness-and-stale-while-revalidate)
- [Background revalidation lifecycle](#background-revalidation-lifecycle)
- [Entry immutability](#entry-immutability)
- [Upgrades, streaming and ResponseWriter transparency](#upgrades-streaming-and-responsewriter-transparency)
- [On-disk format](#on-disk-format)
- [Disk-tier safety](#disk-tier-safety)
- [Eviction and overflow](#eviction-and-overflow)
- [Operations](#operations)
- [Deployment](#deployment)

## Two-tier model

| Tier | Backing store | Bound | Lifetime |
| --- | --- | --- | --- |
| Memory | in-process map, LRU by bytes | `memory_max_size` | process lifetime; survives reloads |
| Disk (optional) | files under `disk_path`, LRU by bytes | `disk_max_size` | survives restarts |

A request is served from the memory tier first. On a memory miss the disk tier is
consulted; a disk hit is **promoted** back into the memory tier so subsequent
reads stay hot. When the memory tier overflows, evicted entries spill **down** to
the disk tier (when configured) rather than being dropped, so disk acts as a
larger, slower backstop.

The cache instance is shared across configuration reloads, so a reload does not
cold-start the cache.

## Configuration

See the [`[cache]`](../README.md#cache) reference for the full key list. A typical
block with the disk overflow tier enabled:

```toml
[cache]
enabled                = true
memory_max_size        = "256MB"
disk_path              = "/var/cache/jul/http"   # enables the disk tier
disk_max_size          = "4GB"
default_ttl            = "5m"
stale_while_revalidate = "30s"
stale_if_error         = "300s"   # keep serving stale on upstream 5xx
```

Per-location opt-in is still required:

```toml
[[servers.locations]]
path  = "/static/"
cache = true
```

## Cache key and Vary

The cache key is derived from the request method, the lowercased host, and the
request URI. When an upstream response carries a `Vary` header, each combination
of the varied request-header values is stored as a **distinct variant** under its
own key, so (for example) `Vary: Accept` keeps the JSON and XML representations of
one URL cached at the same time instead of overwriting each other. A tiny pointer
entry under the base key records which header fields the URL varies on so a
lookup can compute the right variant key. A request whose varied values match no
stored variant is a miss; `Vary: *` responses are never reused.

Responses report their disposition in the `X-Cache` header:

| `X-Cache` | Meaning |
| --- | --- |
| `MISS` | Not in cache (or `Vary` mismatch); fetched from upstream and stored |
| `HIT` | Served fresh from cache |
| `STALE` | Served stale under `stale-while-revalidate` while refreshing |

## Historical behaviour matrix — under recertification

The table records the intended and previously documented behavior. Rows covering
`no-cache`, `must-revalidate`/`proxy-revalidate`, authenticated reuse,
unsafe-method invalidation, `304` metadata and Range requests are being
revalidated and corrected by #107 and #132–#134. Do not treat an unchecked
interaction as a current conformance guarantee.

Background-revalidation ownership and shared-entry immutability (#131), and
upgrade/streaming/`ResponseWriter` transparency (#133), are no longer under
revalidation: they are corrected and evidenced, and described in
[Background revalidation lifecycle](#background-revalidation-lifecycle),
[Entry immutability](#entry-immutability) and
[Upgrades, streaming and ResponseWriter transparency](#upgrades-streaming-and-responsewriter-transparency).

| Scenario | Rule | Detail |
| --- | --- | --- |
| Cache key composition | `METHOD\nhost.lower\nREQUEST_URI` | Host is lowercased; query string is part of the key |
| `Vary` handling | Distinct variant per header-value combo | Base key holds a stub listing varied fields; each variant gets its own storage key |
| `Vary: *` | Never cached | Treated as non-reusable; not stored |
| Cacheable status codes | 200, 203, 301, 404, 410 | Configurable via `cacheableStatus` map in code |
| Non-cacheable status codes | 500, 502, 503, 504, all others | Silently not stored |
| TTL precedence | `s-maxage` → `max-age` → `Expires` → `default_ttl` | First explicit directive wins; `default_ttl` is the fallback |
| `Cache-Control: no-store` | Bypass | Request and response both opt out |
| `Cache-Control: private` | Not stored | Unless combined with `public` on an authorized request |
| `Set-Cookie` present | Not stored | Prevents session leakage |
| Authorized requests (`Authorization` header) | Not stored unless `public` | Protects authenticated responses |
| `stale_while_revalidate` grace | Serve stale immediately, refresh in background | Singleflight per variant prevents thundering herd |
| `stale_if_error` extension | Extend stale window on 5xx/timeout | Measured from point of revalidation failure |
| Conditional requests (`If-None-Match` / `If-Modified-Since`) | 304 if cached ETag/Last-Modified matches | Saves bandwidth on unchanged resources |
| POST / PUT / DELETE / PATCH | Bypass | Only GET and HEAD are cached |
| Protocol upgrade request (`Connection: Upgrade` + `Upgrade`) | Bypass | Handler receives the untouched writer; `X-Cache: BYPASS`; never stored |
| `101 Switching Protocols` and other `1xx` | Not stored | Not a representation; capture is dropped |
| `Content-Type: text/event-stream` | Not stored | Capture stops at the first byte so an open stream accumulates nothing |
| Flushed (chunked) response | Stored normally | A flush alone does not make a response a stream |
| Oversized responses (> `memory_max_size`) | Not stored in that tier | Silently dropped; client still served |
| Memory eviction → disk | Overflow to disk tier (when configured) | Eviction runs outside the memory lock |

## Current correction contract

The active cache programme requires:

- ~~generation-owned, cancellable background revalidation~~ — **delivered by #131**;
- ~~immutable published entries and race-free metadata replacement~~ — **delivered by #131**;
- ~~transparent WebSocket/`101`, SSE and optional `http.ResponseWriter` interface behavior~~ — **delivered by #133**;
- synchronous validation for request/response `no-cache`;
- correct `must-revalidate`, `proxy-revalidate`, authenticated reuse, unsafe-method invalidation and `304` metadata handling;
- initial bypass for requests carrying `Range` or `If-Range` (cached byte-range serving remains a future enhancement).

Until the remaining items close, treat authenticated or user-specific responses conservatively (`private`/`no-store`).

## Freshness and stale-while-revalidate

Freshness comes from the upstream's `Cache-Control`/`Expires`. When the upstream
gives no explicit freshness, `default_ttl` applies. `stale_while_revalidate`
allows a still-recent-but-expired entry to be served immediately (`X-Cache: STALE`)
while a fresh copy is fetched in the background, trading a small staleness window
for lower tail latency. A burst of concurrent stale hits triggers exactly one
background revalidation per variant, so the cache shields the origin from a
thundering herd.

The background refresh is owned by the handler generation that started it; see
[Background revalidation lifecycle](#background-revalidation-lifecycle) for what
cancels it and what does not.

## Stale-if-error

`stale_if_error` extends the stale-serving window when a background
revalidation encounters an upstream error (HTTP 5xx or timeout). If the
revalidation fails, the cached entry remains servable for the configured
`stale_if_error` duration from the point of failure, protecting clients from
backend outages. Once the error window expires or a subsequent revalidation
succeeds, normal freshness rules apply.

The extension **replaces** the stored entry with an updated copy; it never edits
the published entry in place (see [Entry immutability](#entry-immutability)).

A revalidation that is **cancelled** — by process shutdown, by forced generation
retirement, or by the bounded operation deadline — is deliberately *not* treated
as an upstream error and does **not** extend the stale window. Only a real origin
failure does.

Example — tolerate a 5-minute backend outage:

```toml
[cache]
stale_while_revalidate = "60s"
stale_if_error         = "300s"
```

> `stale_if_error` only applies when a stale entry exists and a background
> revalidation is attempted. It does not create cache entries on its own.

## Background revalidation lifecycle

A background refresh is **owned by the handler generation that started it**. This
is what the ownership model guarantees, and what it deliberately does not.

### Ownership

When a stale hit decides to refresh, it acquires a **background lease** on the
current handler generation *before* the originating request returns. The lease is
counted in the same in-flight accounting that keeps a generation's resources
open, so while a refresh is running the reload machinery treats that generation
exactly as if a request were still executing on it:

- the generation's gRPC backend connections, WASM plugin runtimes and
  static-file directory handles stay open;
- a configuration reload still publishes the new generation immediately, and new
  requests use it at once;
- the **old** generation is retired only once its leased work finishes.

A generation that has begun retiring refuses new background work. In that case
the stale entry is served normally and simply expires; no refresh starts.

### Lifetime

| Event | Effect on an in-flight background revalidation |
| --- | --- |
| The originating client disconnects | **No effect.** The refresh continues; that is the point of `stale_while_revalidate`. |
| A configuration reload runs | **No effect** on the refresh. It keeps using the generation it started on. |
| The old generation exceeds `[global] shutdown_timeout` while retiring | **Cancelled.** The server cancels the leased work, then closes the generation's resources. |
| The operation exceeds `[global] shutdown_timeout` in total | **Cancelled.** Every background operation carries that bound as an absolute deadline. |
| Process shutdown | **Cancelled**, then awaited for at most `[global] shutdown_timeout`. Shutdown is never wedged by a refresh. |

A background revalidation therefore **may survive client disconnect**, and
**cannot outlive process shutdown or its bounded lifetime**.

### Context carried into a refresh

The refresh does not inherit the client request's context. It runs on a context
rooted in the process lifetime that carries an explicit allow-list of values:

| Value | Carried | Why |
| --- | --- | --- |
| Generation upstream pool snapshot | yes | The refresh must select from the same backend set as the request that started it. |
| Mutual-TLS client identity | yes | The reverse proxy expands `$ssl_client_*` from it; dropping it would send the origin a different request. |
| Request id | yes | Log correlation; bounded, already-logged value. |
| Trace id | yes | Log correlation; bounded, already-logged value. |
| Authentication claims | **no** | Consumed by the rate limiter, which wraps *outside* the cache and is not re-entered by a refresh. Not retained. |
| Plugin invocation state | **no** | Plugin middleware also wraps outside the cache. |
| Client tracing span | **no** | A refresh is not part of the client request's trace and must not extend its lifetime. |
| Client cancellation / request deadline | **no** | Replaced by process cancellation plus the bounded operation deadline. |

### Deduplication

At most one refresh runs per (effective cache key, handler generation). A burst
of concurrent stale hits therefore produces exactly one origin request.

Generation is part of the key on purpose: a refresh still running on a retiring
generation must never suppress the new generation's refresh of the same key. The
call state is removed, and every waiter released, on **every** outcome —
success, `304`, uncacheable, origin error, cancellation and panic in the
downstream handler.

### Cache data across reloads

The cache instance is created once per process and is captured by every handler
generation, so **cached data persists across ordinary configuration reloads**.
This is the pre-existing, deliberately preserved policy: reloading configuration
does not warm-start or flush the cache.

One consequence follows directly from it: a refresh that started on the old
generation publishes into the process-shared cache even if it completes after a
reload changed routes or backends. The result is the representation the **old**
generation's route would have produced. Changing routing or backends therefore
does not retroactively invalidate entries; use `[cache] enabled = false`, a
restart, or the admin purge endpoint when that matters.

### Observability

`jul_cache_revalidations_total{outcome}` counts refresh decisions with a fixed
label set: `stored`, `not_modified`, `uncacheable`, `origin_error`, `canceled`,
`panic`, `no_lease`, `deduplicated`. No cache key, URL, host, generation id or
error string is ever used as a label. Structured logs carry only the bounded
operation name, the generation id and a bounded reason.

## Entry immutability

A cache entry is **immutable once published**. After an entry is handed to the
memory or disk tier, no field of it is written again — not the body bytes, not
the header map or its value slices, not the `Vary` metadata, and not the
freshness timestamps.

Code that must change timing or metadata (a `304` refreshing freshness, a
`stale_if_error` window extension) builds a deep-enough clone, mutates the clone,
and atomically replaces the stored pointer under the tier's own lock. Cloning
happens only at those publication and update boundaries, so a cache **hit never
pays a body copy**.

This is what makes it safe for a lookup to hand out the stored `*Entry` pointer:
concurrent readers, the disk tier's encoder and a replacing writer can never
observe a half-updated entry.

## Upgrades, streaming and ResponseWriter transparency

Enabling `cache = true` on a route must not change what the route can *do*. Three
rules make that true.

### Protocol upgrades bypass the cache

A request is treated as a protocol upgrade when it carries a non-empty `Upgrade`
header **and** lists the `upgrade` token in `Connection` (RFC 9110 §7.8). Both
halves are required, so a stray client-supplied `Upgrade` header cannot switch
caching off for ordinary traffic.

An upgrade request:

- is **not** looked up in the cache, even if a fresh entry exists for its key;
- reaches the handler with the **untouched** response writer, so the reverse
  proxy can hijack the connection and complete the `101 Switching Protocols`
  handshake;
- is reported as `X-Cache: BYPASS`;
- is **never** stored.

Only the cache is bypassed. Authentication, rate limiting, the WAF, plugins and
mutual-TLS identity all wrap outside the cache and still run.

`CONNECT` and every other method outside `GET`/`HEAD` never reach the cache path
at all.

### Responses that are never stored

| Response | Stored? | Why |
| --- | :---: | --- |
| `101 Switching Protocols` and any other `1xx` | no | An interim or protocol-switch response is not a representation. |
| Anything written after a successful hijack | no | The connection has left HTTP; the wrapper also refuses further writes with `http.ErrHijacked`. |
| `Content-Type: text/event-stream` | no | An event stream never ends. Capture stops at the first byte, so an open SSE connection accumulates nothing. |
| A body larger than `memory_max_size` | no | Existing size bound; capture is discarded when the limit is passed. |

### Flushing does not make a response uncacheable

An ordinary flushed response **is** still cached. This is deliberate: the
standard reverse proxy flushes on every write of any response whose
`Content-Length` is unknown — that is, every chunked response — so treating a
flush as "this is a stream" would silently stop caching most dynamic backends.
Unbounded growth is prevented by the event-stream rule and the size bound above
rather than by the flush itself.

### Optional `ResponseWriter` interfaces

The writer a cached route hands to its handler implements **exactly** the
optional interfaces the real connection implements — `http.Flusher`,
`http.Hijacker`, `http.Pusher`, `io.ReaderFrom` — and no others. It also
implements `Unwrap() http.ResponseWriter`, so `http.ResponseController` reaches
the real writer for capabilities that have no classic interface (read/write
deadlines, full duplex).

The "no others" half matters as much as the first. A wrapper that always
implements `Hijack` and returns an error still makes `w.(http.Hijacker)` succeed,
and handlers branch on the assertion rather than the error. On **HTTP/2 and
HTTP/3 there is no connection to hijack**, and the writer correctly reports that
by not implementing `http.Hijacker` at all; `http.NewResponseController(w).Hijack()`
returns `http.ErrNotSupported`. WebSocket over HTTP/2 (RFC 8441 extended
`CONNECT`) is not supported by the reverse proxy and is unrelated to caching.

The same guarantee holds through the whole middleware chain — metrics, access
log, tracing and compression all use the same wrapper — so composition does not
erode it.

## On-disk format

Each cached response is a single self-contained file under `disk_path`:

- **Filename** — the lowercase hex SHA-256 of the cache key (exactly 64 hex
  characters). Filenames are content-addressed, opaque, and reveal nothing about
  the original URL.
- **Contents** — the gob-encoded entry (status, headers, body, timestamps,
  freshness metadata, and any `Vary` values).

There is no index file or sidecar; on startup the directory is scanned and each
cache file is re-hydrated into the index, ordered by modification time and bounded
by `disk_max_size`.

## Disk-tier safety

The disk tier is written defensively so that an operator can point `disk_path` at
a directory without risking other data or torn cache entries:

- **Atomic writes.** Entries are written to a same-directory temporary file,
  fsync'd, then renamed over the final name. A crash mid-write leaves either the
  previous complete entry or no entry — never a partially written file. This uses
  the same atomic-write primitive as the config writer
  (see [SECURITY.md](../SECURITY.md#hardening-defaults--recommendations)).
- **Restrictive permissions.** Cache files are created mode `0o600` (owner
  read/write only) and the cache directory is created mode `0o700`. A cached
  response body is never world-readable.
- **Foreign-file safety.** Only files whose names match the content-addressed
  scheme (64 lowercase hex characters) are indexed, served, or eligible for LRU
  eviction. Any other file — an operator's note, a stray archive, a nested
  directory — is **ignored and never deleted**. Their presence is logged once at
  startup with a `WARN` so a misconfigured `disk_path` (for example, a shared
  directory) is visible without endangering the foreign data.

Even so, prefer a **dedicated** directory for `disk_path`. The cache assumes it
owns the directory's hex-named files and may delete any of them at any time under
memory or disk pressure.

## Eviction and overflow

Both tiers track their size in bytes and evict least-recently-used entries when
over budget:

- The **memory tier** evicts to stay within `memory_max_size`. Evicted entries are
  handed to the disk tier (when configured). The disk write runs **outside** the
  memory-tier lock — victims are collected under the lock and persisted after it
  is released — so a slow disk never blocks in-memory reads and writes.
- The **disk tier** evicts whole files to stay within `disk_max_size`, removing the
  backing file as it goes.

An entry larger than a tier's cap is simply not stored in that tier.

## Known limitations

1. **No tag or pattern purge.** The admin API can purge a single exact key or
   the entire cache. There is no way to purge by URL prefix, host, or tag (e.g.
   invalidate all `/api/v1/users/*`). Operators needing selective invalidation
   must do so at the application layer or use short TTLs.

2. **Orphaned variants are not auto-cleaned.** If an upstream changes its `Vary`
   header (e.g. from `Vary: Accept` to `Vary: Accept-Encoding`), the old variant
   entries remain in cache until they expire or are evicted by LRU.

3. **Silent oversized-entry drop.** A response body larger than `memory_max_size`
   (or `disk_max_size`) is streamed to the client but not cached. There is no
   log or metric emitted for this; operators must size tiers generously.

4. **No cross-location cache sharing.** Each Jul.IA process has its own isolated
   cache instance. There is no shared cache (e.g. Redis) for multi-instance
   deployments; each node warms independently.

5. **A reload does not invalidate cached entries.** The cache is process-scoped
   and survives configuration reloads by design, so a routing or backend change
   does not retroactively drop entries stored under the previous configuration.
   A refresh that was already in flight when the reload ran also completes
   against the *old* generation's route and publishes its result. Use the admin
   purge endpoint or a restart when a configuration change must invalidate
   cached content. Characterized by
   `TestReloadWaitsForCacheRevalidationHoldingGeneration`
   (`internal/server`) and the generation-isolation tests in `internal/cache`.

6. **A background refresh delays retirement of its generation.** While a refresh
   is running, the handler generation that started it keeps its gRPC
   connections, plugin runtimes and static roots open. This is bounded: the
   refresh is cancelled and the resources closed after `[global]
   shutdown_timeout`. A very slow origin can therefore hold one superseded
   generation's resources for up to that long after a reload.

## Benchmarks

Run with `go test -bench='BenchmarkCache.*' -benchmem ./internal/cache/`.

| Benchmark | Scenario | ns/op | allocs/op | bytes/op |
| --- | --- | --- | --- | --- |
| `BenchmarkCacheHit` | Memory hit, small body | ~2 400 | 15 | 1 192 |
| `BenchmarkCacheMiss` | Memory miss (first store) | ~10 600 | 44 | 7 807 |
| `BenchmarkCacheVaryHit` | Vary variant hit | ~2 900 | 18 | 1 280 |
| `BenchmarkCacheMemOverflow` | 512-byte memory cap → disk overflow per write | ~4 360 000 | 106 | 14 620 |

A memory hit is ~4× faster than a miss (the miss must buffer the response and
allocate an entry). Vary adds ~20% overhead to a hit because the variant key
must be computed. Overflow to disk is orders of magnitude slower due to the
syscall cost of file I/O; the memory lock is released before writing so readers
are never blocked.

## Threat note

The cache stores upstream responses and serves them to subsequent clients.
Because the cache is a shared resource, its misuse can affect confidentiality,
integrity, and availability:

1. **Cache poisoning via Host header manipulation.** The cache key includes the
   `Host` header (lowercased). If Jul.IA sits behind a reverse proxy that trusts
   `X-Forwarded-Host` without validation, an attacker may poison the cache with
   a malicious response keyed to a victim's host, causing that response to be
   served to legitimate requests. Counter-measures: validate `Host` in the outer
   proxy; do not forward untrusted `X-Forwarded-Host` to Jul.IA.

2. **Cross-user leakage through `Vary` misconfiguration.** An upstream that sets
   `Vary: Cookie` without `Cache-Control: private` may cause one user's
   authenticated page to be served to another user with the same `Cookie` header.
   The cache does not treat cookies as a cache-bypass signal (only `private` or
   `no-store` do). Counter-measures: upstreams must set `private` for
   user-specific responses; operators should audit `Vary` headers before enabling
   cache on authenticated routes.

3. **Vary-header evasion (Web Cache Deception).** An attacker requests
   `/profile.jpg` with a `Accept: image/webp` header when the real page is
   `/profile`. If the upstream returns `Vary: Accept` without path validation, the
   attacker may cache a 200 HTML response under the `.jpg` key. Counter-measures:
   upstreams should validate extensions before serving; the cache itself follows
   the upstream's Vary directive faithfully.

4. **Stale-if-error window extension as a DoS vector.** If `stale_if_error` is
   configured very long (e.g. 24 hours) and an attacker keeps the backend down,
   clients may receive stale responses well beyond the intended freshness window.
   Counter-measures: keep `stale_if_error` short (minutes, not hours); monitor
   upstream health and alert on prolonged 5xx rates.

5. **Sensitive response caching on disk.** The disk tier persists responses
   across restarts. If a cached response contains PII and the disk is on shared
   or cloud storage without encryption-at-rest, the data may leak. Counter-measures:
   use full-disk encryption or an encrypted volume for `disk_path`; set
   restrictive permissions (`0o700` dir, `0o600` files).

6. **Request smuggling via header injection in cached responses.** An upstream
   that reflects user input into response headers without sanitisation may produce
   a `Set-Cookie` or `Location` header that carries an attack payload. The cache
   stores and replays the header verbatim. Counter-measures: validate and sanitise
   all user input before writing response headers; the cache correctly strips
   hop-by-hop headers but does not inspect application-level headers beyond
   `Cache-Control` semantics.

## GA status — recertification open

The feature retains its historical release record, but the current correctness findings reopen its conformance evidence. #134 must publish the corrected executable matrix, race/protocol evidence, benchmarks, soak result and final maturity/status decision before the cache is described as recertified.


| Criterion | Status | Evidence |
| --- | --- | --- |
| 1 — Conformance / behaviour matrix | ⚠️ recertification open | #107 and #132–#134 own the corrected executable matrix; the revalidation-lifecycle and entry-immutability rows are settled by #131, and the upgrade/streaming/`ResponseWriter` rows by #133 |
| 2 — Published benchmark numbers | ✅ | `BenchmarkCacheHit` / `Miss` / `VaryHit` / `MemOverflow` in `internal/cache/bench_test.go` |
| 3 — Known-limitations list | ✅ | 6-item limitation list above |
| 4 — Semver-guarded config/API contract | ✅ | v1 config freeze (cross-cutting) |
| 5 — Long-running soak test | ✅ | soaked 1h windows 2026-07-04 (1.5M req, 0% err) — [evidence](soak-evidence.md#2026-07-04--cache-soak-local-windows-1-hour-50-workers) |
| 6 — Runnable example + docs | ✅ | `testdata/cache.toml` + this doc |
| 7 — Security / threat note | ✅ | 6-row threat note above |
| 8 — Fuzzing where parsing is involved | n/a | No custom parser (uses stdlib `http` + `gob`) |
| 9 — Self-explanatory Console surface | ✅ | Status row in runtime overview (tier config + opt-in location count) |
