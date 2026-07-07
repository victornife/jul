# Response cache

Jul.IA can cache upstream responses in front of proxied backends and FastCGI/uWSGI
apps. The cache is **two-tier**: a fast in-memory tier backed by an optional
on-disk overflow tier that survives process restarts. Both tiers are part of the
core binary — there is no build tag to enable.

The cache is opt-in per location (`cache = true`) and gated by the global
`[cache].enabled` switch. Cacheability follows standard HTTP semantics
(`Cache-Control`, `Expires`, `Vary`), so an upstream stays in control of what may
be stored and for how long.

## Contents

- [Two-tier model](#two-tier-model)
- [Configuration](#configuration)
- [Cache key and Vary](#cache-key-and-vary)
- [Freshness and stale-while-revalidate](#freshness-and-stale-while-revalidate)
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

## Behaviour matrix

The cache's behaviour for each major scenario:

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
| Oversized responses (> `memory_max_size`) | Not stored in that tier | Silently dropped; client still served |
| Memory eviction → disk | Overflow to disk tier (when configured) | Eviction runs outside the memory lock |

## Freshness and stale-while-revalidate

Freshness comes from the upstream's `Cache-Control`/`Expires`. When the upstream
gives no explicit freshness, `default_ttl` applies. `stale_while_revalidate`
allows a still-recent-but-expired entry to be served immediately (`X-Cache: STALE`)
while a fresh copy is fetched in the background, trading a small staleness window
for lower tail latency. A burst of concurrent stale hits triggers exactly one
background revalidation per variant, so the cache shields the origin from a
thundering herd.

## Stale-if-error

`stale_if_error` extends the stale-serving window when a background
revalidation encounters an upstream error (HTTP 5xx or timeout). If the
revalidation fails, the cached entry remains servable for the configured
`stale_if_error` duration from the point of failure, protecting clients from
backend outages. Once the error window expires or a subsequent revalidation
succeeds, normal freshness rules apply.

Example — tolerate a 5-minute backend outage:

```toml
[cache]
stale_while_revalidate = "60s"
stale_if_error         = "300s"
```

> `stale_if_error` only applies when a stale entry exists and a background
> revalidation is attempted. It does not create cache entries on its own.

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

## GA status

| Criterion | Status | Evidence |
| --- | --- | --- |
| 1 — Conformance / behaviour matrix | ✅ | 14-row matrix above (key, Vary, TTL, status codes, conditional requests, eviction) |
| 2 — Published benchmark numbers | ✅ | `BenchmarkCacheHit` / `Miss` / `VaryHit` / `MemOverflow` in `internal/cache/bench_test.go` |
| 3 — Known-limitations list | ✅ | 4-item limitation list above |
| 4 — Semver-guarded config/API contract | ✅ | v1 config freeze (cross-cutting) |
| 5 — Long-running soak test | ✅ | soaked 1h windows 2026-07-04 (1.5M req, 0% err) — [evidence](soak-evidence.md#2026-07-04--cache-soak-local-windows-1-hour-50-workers) |
| 6 — Runnable example + docs | ✅ | `testdata/cache.toml` + this doc |
| 7 — Security / threat note | ✅ | 6-row threat note above |
| 8 — Fuzzing where parsing is involved | n/a | No custom parser (uses stdlib `http` + `gob`) |
| 9 — Self-explanatory Console surface | ✅ | Status row in runtime overview (tier config + opt-in location count) |
