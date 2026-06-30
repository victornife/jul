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

## Operations

The admin API exposes a purge endpoint (POST only) on the [admin
listener](../README.md#admin-interface--observability), authenticated with the
admin token:

```bash
# Purge the entire cache (memory + disk)
curl -X POST -H "Authorization: Bearer $TOKEN" http://127.0.0.1:9090/cache/purge

# Purge a single key
curl -X POST -H "Authorization: Bearer $TOKEN" \
  'http://127.0.0.1:9090/cache/purge?key=GET%5Cnexample.com%5Cn/a'
```

Purging clears both tiers; the disk files for purged entries are removed.

## Deployment

The bundled systemd unit and Docker image place the disk cache under
`/var/cache/jul`. Point `disk_path` at a subdirectory there and give the service
user ownership:

```toml
[cache]
disk_path = "/var/cache/jul/http"
```

See [deployment.md](deployment.md#directory-layout) for the full canonical
filesystem layout and ownership guidance.
