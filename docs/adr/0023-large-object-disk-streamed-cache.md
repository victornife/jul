# ADR 0023 — Large-object disk-streamed cache and random-access body contract

- **Status:** Accepted for #525 Part 2; Beta, unreleased
- **Date:** 2026-10-15
- **Deciders:** Jul.IA maintainer
- **Applies to:** `internal/cache`, `[cache]` configuration, lifecycle classification, disk storage format, and the storage prerequisite for #442
- **Source:** [#525](https://github.com/victornife/jul/issues/525), [#505](https://github.com/victornife/jul/issues/505), [#442](https://github.com/victornife/jul/issues/442)
- **Related:** [ADR 0003](0003-maturity-and-ga.md), [ADR 0011](0011-reload-plan.md), [ADR 0013](0013-project-operating-model-and-completeness.md), [ADR 0014](0014-operability-surfaces.md), [cache guide](../cache.md)

## Context

Jul.IA's response cache has two tiers: an in-memory LRU tier (`memory_max_size`,
default 64 MiB) and an optional on-disk overflow tier (`disk_path` and
`disk_max_size`, default 512 MiB). Prior to #525 Part 2, the maximum cacheable
response size was hard-wired to `memory_max_size`, and every cacheable miss or
revalidation buffered the complete response body in memory before publication.
In addition, `diskStore` encoded the entire `Entry`—including the full `Body`
slice—into one `encoding/gob` blob and read the entire file back into memory on
every disk hit, promoting it into the memory tier.

This coupled design had three material consequences:

1. **Objects larger than `memory_max_size` could never be cached**, regardless of
   how much capacity `disk_max_size` provided.
2. **Disk hits allocated proportional to the complete response body** and
   promoted large objects into the memory tier, where a single large object
   would evict the working set of smaller hot entries.
3. **Range-aware caching (#442 Option A)** requires serving arbitrary byte
   ranges from a complete cached representation in bounded memory via random
   access (`io.ReaderAt`), which an opaque `gob`-encoded whole-entry blob cannot
   provide without decoding the full body into heap memory.

#505 bounded in-flight memory captures to `memory_max_size` and introduced
`jul_cache_capture_skipped_total{reason="oversize"|"budget"}`. #525 Part 1 added
opt-in per-key cold-miss fill locking (`[cache].lock` and `[cache].lock_timeout`)
so concurrent cold misses do not stampede the origin. #525 Part 2 decouples the
per-entry size cap from `memory_max_size` and introduces bounded disk-streamed
capture and reading.

## Decision

### 1. Configuration and tier placement (`max_entry_size`)

Add `[cache].max_entry_size` (`Size`, classified `hot_reload`):

- **Default when omitted or zero:** `max_entry_size` defaults to
  `memory_max_size` (preserving existing behavior unless explicitly configured).
- **Validation rules:**
  - `max_entry_size` must be non-negative.
  - When `disk_path` is empty (disk tier disabled), `max_entry_size` must not
    exceed `memory_max_size`: there is no disk tier to hold entries larger than
    the memory tier.
  - When `disk_path` is set (disk tier enabled), `max_entry_size` must not
    exceed `disk_max_size`.
  - `max_entry_size` may be smaller than `memory_max_size` when an operator
    wants a large memory tier for many small objects while capping individual
    entry sizes.
- **Tier placement rule:**
  - Let `memCap = min(memory_max_size, max_entry_size)`.
  - Responses with body length `<= memCap` are captured in memory, stored in the
    memory tier, and written through to the disk tier (when `disk_path` is
    configured) under a monotonic per-key generation so they survive process
    restarts and remain backed by the larger disk LRU if evicted from memory.
    On a memory miss, a disk hit within `memCap` is promoted back into the
    memory tier only if its per-key generation is still current.
  - Responses with body length `> memory_max_size` and `<= max_entry_size`
    (which requires an enabled disk tier) are captured via a temporary file in
    `disk_path` and published directly to the disk tier.
  - Disk-backed entries whose body length exceeds `memCap` remain in the disk
    tier on cache hits; they are **not** promoted into the memory tier and their
    bodies are **not** loaded into heap memory.

### 2. Capture-memory and temporary-disk budgets

Two independent, process-wide in-flight capture budgets bound resource use
during concurrent misses and revalidations:

1. **In-memory capture budget (`memory_max_size`):** unchanged from #505.
   In-memory captures reserve bytes from `Cache.capture` up to `memory_max_size`.
2. **Temporary-disk capture budget (`disk_max_size`):** in-flight disk writes
   (large-object streamed captures, memory write-throughs, `Vary` stubs, and
   `304` disk rewrites, including body, gob metadata, and 24-byte trailer bytes)
   reserve bytes from `diskStore.tempBudget` up to the available disk capacity
   (`availableBudgetCap = disk_max_size - retiredBytes - tombstoneBytes`). When
   a **single** exact-boundary capture (`bodyLen <= availableBudgetCap` with no
   other concurrent temporary capture in flight: `tempBudget.inflight == bodyLen`)
   or a standalone `Vary` stub (`bodyLen == 0` with `tempBudget.inflight == 0`)
   appends its bounded `JULDCH02` metadata and 24-byte trailer
   (`len(tail) <= maxDiskMetaEnvelope = 1 MiB + 24 B`), `publishStreamWithGen`
   permits `tempBudget` to reserve `len(tail)` up to
   `availableBudgetCap + len(tail)` for the duration of the `Write`/`Sync`/`Close`/`Rename`
   step so an exact-boundary object (`body == max_entry_size == disk_max_size`)
   can be published without deadlocking against its own metadata trailer. No
   second concurrent capture can exceed `availableBudgetCap`.
3. **Spill transition for unknown-length responses:**
   - A response whose declared `Content-Length` exceeds `max_entry_size` is
     rejected at `WriteHeader` before any buffer or temporary file is allocated
     (`reason="oversize"`).
   - A response with a declared `Content-Length` in `(memory_max_size, max_entry_size]`
     and cacheable headers streams directly to a temporary disk file from the
     first byte, allocating zero in-memory capture buffer.
   - A response with unknown `Content-Length` (or `<= memory_max_size`) begins
     capturing in memory under the memory capture budget. If a write causes the
     accumulated body to cross `memory_max_size` while remaining `<= max_entry_size`
     (and `disk_path` is enabled), the buffered bytes and new chunk spill to a
     temporary file in `disk_path`, the in-memory `bytes.Buffer` is replaced
     with an empty value, and the in-memory budget reservation is released
     immediately.
   - If a response crosses `max_entry_size` (`reason="oversize"`) or cannot
     reserve from the active memory or temporary-disk budget (`reason="budget"`),
     capture is abandoned immediately, any temporary buffer or file and its
     budget reservation are released at that point, fill-lock waiters are woken,
     and the response continues streaming to the client.

### 3. On-disk framing format (`JULDCH02`), atomic publication, and versioning

Each published entry in `disk_path` remains a single self-contained file named
by the 64-character lowercase hex SHA-256 of its cache key (`<sha256(key)>`),
created with mode `0o600` inside the `0o700` cache directory.

To allow streaming body capture from offset `0`, metadata-only reads on lookup
and startup rehydration, and `O(1)` random-access slicing without sidecar files,
files use the `JULDCH02` trailer-framed layout:

```text
+-------------------------------------------------------------------+
| Raw response body bytes                 [0 .. bodyLen)            |
+-------------------------------------------------------------------+
| gob-encoded diskEntryMeta               [bodyLen .. bodyLen+meta) |
+-------------------------------------------------------------------+
| 24-byte fixed trailer                   [fileSize-24 .. fileSize) |
|   bytes 0..7   : uint64 big-endian metaLen                        |
|   bytes 8..15  : uint64 big-endian bodyLen                        |
|   bytes 16..23 : ASCII magic "JULDCH02"                           |
+-------------------------------------------------------------------+
```

- **Why a trailer rather than a header:** during streaming capture of a chunked
  response, `bodyLen` is known only when the stream completes. Writing raw body
  bytes at offset `0` to a same-directory temporary file (`.jul-cache-tmp-*`),
  then appending the encoded metadata and the 24-byte trailer on completion,
  requires a single sequential write pass with no post-capture body copy.
- **Atomic publication:** an entry is published only after the complete response
  body is received, its length matches any declared `Content-Length`, the
  request context has not been canceled, and `buildEntryWithPolicy` validates
  cacheability. The temporary file is `Sync()`ed, `Close()`d, and atomically
  renamed (`os.Rename`) to `<sha256(key)>`.
- **Crash and corruption recovery:** an interrupted write or crash before the
  rename leaves only a `.jul-cache-tmp-*` file. Even if a file is truncated,
  corrupted, or written by the legacy unframed `v1` gob format, its final 24
  bytes either lack the `"JULDCH02"` magic or fail the exact size invariant
  `bodyLen + metaLen + 24 == fileSize` (with `metaLen <= 1 MiB`). On startup
  `rehydrate()` and on `diskStore.get()`, any 64-hex-char file that fails
  trailer or metadata validation is unlinked and ignored, and any stale
  `.jul-cache-tmp-*`, `.jul-cache-retired-*`, or `.<hash>.tmp-*` file is
  removed. Unrelated foreign files (names not matching the 64-hex-char scheme or
  Jul's temp prefixes) are never deleted.

### 4. Internal body abstraction and reader lifetime leases

`Entry` exposes its payload through a leased `BodyReader` contract:

```go
type BodyReader interface {
    io.Reader
    io.ReaderAt
    io.Seeker
    io.Closer
    Len() int64
}
```

- **Memory-backed entries (`e.Body != nil`, `e.diskBody == nil`):**
  `e.OpenBody()` returns a zero-cost reader over `bytes.NewReader(e.Body)`.
- **Disk-backed entries (`e.diskBody != nil`):**
  `e.diskBody` is a reference-counted `*diskBlob` recording the file path,
  `bodyLen`, and total `fileSize`. Lookups (`diskStore.getWithMemCap`) acquire
  a reader lease on `diskBlob` while still holding `diskStore.mu`, then open a
  single `*os.File` handle from which both the trailer/metadata and (when
  promoting a small entry) the body are read; any failure cleanup (`delIfBlob`)
  removes the index entry only if the currently indexed blob is still the same
  generation. `e.OpenBody()` similarly acquires a lease under `blob.mu`, opens
  an independent `*os.File` handle, and returns a `BodyReader` backed by
  `io.NewSectionReader(file, 0, bodyLen)`. `Cache.serve` streams from
  `BodyReader` using a pooled 32 KiB buffer, allocating `O(1)` memory regardless
  of body size.
- **Metadata-envelope capacity and physical occupancy for exact-boundary objects:**
  Each `JULDCH02` file adds a bounded metadata and trailer envelope
  (`metaLen + 24 <= maxDiskMetaEnvelope`, where `maxDiskMetaBytes = 1 MiB`) to
  the representation body. `diskStore` maintains two distinct counters:
  `physBytes` (the exact physical `fileSize = bodyLen + metaLen + 24` summed
  across all indexed entries, exported via `Stats()` and
  `jul_cache_bytes{tier="disk"}`) and `curBytes` (the logical LRU capacity
  charge summed across `diskItem.effectiveSize(maxBytes)`). When an entry's
  `bodyLen <= disk_max_size` and its metadata+trailer fits within
  `maxDiskMetaEnvelope`, its logical capacity charge in `curBytes` is capped at
  `disk_max_size`, so an object with `body == max_entry_size == disk_max_size`
  is stored without self-evicting while `Stats()` reports its true physical file
  size on disk; `publishStream` also verifies the entry remains indexed after
  `evictLocked` before reporting success.
- **Budgeted `304` rewrites and validation fallback:**
  Rewriting a disk-backed entry during a `304 Not Modified` refresh reserves
  `bodyLen` from `tempBudget` (`<= disk_max_size`) before copying; if the
  temporary-disk budget is exhausted, the rewrite is skipped with
  `reason="budget"` and the previous disk entry is retained. During mandatory
  synchronous validation, if a large `200` replacement fails to publish to disk,
  the temporary file remains readable until `writeRecorded` finishes streaming
  the captured body to the client (falling back to an origin refetch before
  committing headers if the temporary file is unreadable).
- **Eviction, invalidation, and replacement while readers are active
  (cross-platform safety):**
  When `diskStore` evicts, deletes, purges, or overwrites a `diskBlob`:
  - `diskStore` marks `blob.unlinked = true` and drops the store's own reference.
  - If no readers are active (`blob.readers == 0`), `os.Remove(blob.path)`
    deletes the file immediately.
  - If active readers hold leases (`blob.readers > 0`), `blob.path` is renamed
    to a unique same-directory `.jul-cache-retired-*` path before the new entry
    (if any) is renamed to `<sha256(key)>`. Active readers continue reading
    their open `*os.File` uninterrupted, any concurrent reader acquiring a lease
    on the same in-flight `*Entry` opens the retired path rather than the new
    replacement file, and the canonical `<sha256(key)>` path is immediately free
    on all platforms (including Windows, where `os.Open` sets `FILE_SHARE_DELETE`).
    If renaming `blob.path` fails while readers are active, `diskStore` pins the
    retired blob (`d.pinned[hash]`) and immediately persists a durable tombstone
    in `.jul-cache-tombstones` (preserving active-reader contents without
    truncating the backing file if manifest persistence fails) until the last
    reader closes, removes the old canonical file, and promotes any deferred
    replacement. Recovery cleanup (`purgeAllCacheFilesOnCorruptTombstoneLocked`)
    similarly retires indexed entries through their reader leases and preserves
    active in-flight temporary captures (`d.activeTempFiles`).
    When the last active reader calls `Close()`, it closes its `*os.File` handle
    first and then removes `blob.path`.
- **Random-access contract for #442:**
  Because `BodyReader` implements `io.ReaderAt`, `io.Seeker`, and `Len() int64`
  over raw representation bytes `[0, bodyLen)` with lease-managed lifetime,
  #442 Option A (if its activation gate passes) can serve validated single byte
  ranges via `io.NewSectionReader(bodyReader, start, length)` without loading
  the full object into memory or introducing a second storage engine. Range
  behavior remains disabled in #525 Part 2.

### 5. LRU, byte accounting, and reload behavior

- **Single disk LRU by bytes with physical occupancy reporting:** `diskStore`
  retains a single strict LRU queue bounded by `disk_max_size`. Physical
  occupancy `diskStore.physBytes` sums the complete on-disk `fileSize`
  (`bodyLen + metaLen + 24`) of every indexed file and is reported through
  `Stats()` and `jul_cache_bytes{tier="disk"}`. Logical eviction capacity
  `diskStore.curBytes` sums `diskItem.effectiveSize(maxBytes)` (equal to
  `fileSize`, except capped at `maxBytes` when `bodyLen <= maxBytes` and
  `metaLen + 24 <= maxDiskMetaEnvelope` so a single exact-boundary entry does
  not self-evict).
- **304 and 200 revalidation of disk-backed entries:**
  - On a `304 Not Modified`, `merge304` clones the entry and updates its
    metadata; `diskStore.set` writes the refreshed file by streaming the body
    from the existing `diskBlob` in 32 KiB chunks and appending the updated
    metadata trailer, without loading the body into heap memory.
  - On a `200 OK`, `recorder` uses the same `max_entry_size` and disk-spill
    machinery as `cacheWriter`, publishing the replacement atomically and
    retiring the superseded `diskBlob`.
- **Hot reload:** `cache.max_entry_size` hot-reloads via `CachePolicy`
  alongside `memory_max_size` and `disk_max_size`. Lowering `disk_max_size` on
  reload immediately evicts the oldest disk entries in strict LRU order.

### 6. Rejected alternatives

1. **Separate metadata and body sidecar files (`<hash>.meta` + `<hash>.body`):**
   Rejected because publishing or replacing an entry would require two
   filesystem renames, creating a crash window where metadata from one
   generation could pair with body bytes from another, and complicating
   `rehydrate()` and foreign-file detection.
2. **Keeping `encoding/gob` for the entire file and streaming gob byte slices:**
   Rejected because `encoding/gob` materializes `[]byte` fields in memory on
   decode and does not support `O(1)` random-access `ReadAt` into the body payload.
3. **Separate configured budget knob for temporary disk captures or separate
   large-object disk tier:**
   Rejected for the same reason #505 derived the in-flight memory budget from
   `memory_max_size`: bounding temporary disk captures by `disk_max_size` and
   sharing the disk LRU by actual byte size enforces the operator's declared
   disk cap without introducing redundant capacity knobs.

## Consequences

### Positive

- Objects larger than `memory_max_size` up to `max_entry_size` are cached on
  disk with `O(1)` heap allocation on both capture and hit paths.
- Disk hits on large objects no longer thrash the in-memory tier.
- Crashes, cancellations, early EOFs, and disk errors never leave partial or
  corrupt entries reachable.
- Active readers are protected across eviction, invalidation, and replacement on
  Linux, macOS, and Windows.
- The storage layer provides the exact random-access `BodyReader` seam required
  to evaluate and (if approved) implement #442 Option A.

### Negative / accepted trade-offs

- Switching the on-disk format to `JULDCH02` invalidates pre-existing `v1` disk
  cache files on upgrade: `rehydrate()` safely removes unframed `v1` files as
  incompatible cache artifacts, causing a one-time cold miss per disk-cached URL
  after upgrade.
- Retiring an open file while a slow client streams a large disk hit briefly
  retains the `.jul-cache-retired-*` file on disk until that reader finishes.

## Related

- [#525](https://github.com/victornife/jul/issues/525) — Cold-miss fill lock and disk-streamed capture
- [#505](https://github.com/victornife/jul/issues/505) — Capture memory bounds and oversize signal
- [#442](https://github.com/victornife/jul/issues/442) — Range-aware response caching (Option A gate)
- [docs/cache.md](../cache.md)
