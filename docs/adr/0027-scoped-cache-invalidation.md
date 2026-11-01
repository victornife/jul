# ADR 0027 — Scoped cache invalidation, trusted origin tags, and selector fences

- **Status:** Accepted — Beta/unreleased (`#621` / `CACHE-OPS-01`)
- **Date:** 2026-10-25
- **Deciders:** Victor Niharra (`@victornife`)
- **Applies to:** `internal/cache`, `[cache]` configuration, `internal/lifecycle`, `internal/admin`, `internal/adminapi`, `internal/adminclient`, `cmd/jul`, Console v2 (`internal/admin/ui`), `internal/observability`, and successor contracts for `#622` (`CACHE-OPS-02`) and `#624` (`CACHE-OPS-04`)
- **Source:** [#621](https://github.com/victornife/jul/issues/621), [#620](https://github.com/victornife/jul/issues/620), [#62](https://github.com/victornife/jul/issues/62)
- **Related:** [ADR 0010](0010-console-rbac.md), [ADR 0011](0011-reload-plan.md), [ADR 0013](0013-project-operating-model-and-completeness.md), [ADR 0014](0014-operability-surfaces.md), [ADR 0019](0019-configuration-authority-generated-contracts-and-resource-identity.md), [ADR 0023](0023-large-object-disk-streamed-cache.md), [ADR 0024](0024-range-aware-cache-option-a.md), [cache guide](../cache.md)

## Context

Prior to `#621` (`CACHE-OPS-01`), Jul.IA's two-tier HTTP response cache supported only two administrative purge primitives through the unversioned `POST /cache/purge` endpoint: deleting a single opaque internal cache key (`?key=METHOD\nhost\nrequest_uri`) or purging the entire node-wide cache.

For an operator running multiple applications or a content-driven site on a single Jul.IA node, both options are unsatisfactory:

1. Editing an article (`/articles/42`) can simultaneously affect the article page, category listings, homepage summaries, and JSON API representations (`GET`, `HEAD`, and `Vary` variants). Flushing the whole cache evicts unrelated applications and static assets, causing an unnecessary origin traffic spike; computing raw internal keys for every query and `Vary` combination is impractical.
2. Naively scanning and deleting currently stored entries without coordinating with in-flight cold misses (`#525` Part 1), disk-streamed large-object captures (`#525` Part 2, [ADR 0023](0023-large-object-disk-streamed-cache.md)), Option A range fills (`#442`, [ADR 0024](0024-range-aware-cache-option-a.md)), synchronous validations, or background refreshes (`#131`) allows a pre-invalidation origin fetch to finish after the purge returns and publish stale content back into the cache.
3. Successor work in `#622` (`CACHE-OPS-02`, cache metadata inspection) and `#624` (`CACHE-OPS-04`, explicit retain/invalidate cutover on route and backend changes) requires one shared selector vocabulary, safe metadata projection, and commit-time fill-fencing contract rather than separate ad-hoc purge mechanisms.

## Decision

### 1. Canonical selector contract and normalization (`internal/cache` & `internal/adminapi`)

Jul.IA supports five first-class scoped selectors alongside legacy exact-key and full-cache purge compatibility:

| Selector `kind` | Required inputs | Target scope | Query handling | Broad-scope confirmation |
| --- | --- | --- | --- | --- |
| `exact_url` | `url` (`http(s)://host[:port]/path[?query]`) or `host` + `uri` (`/path[?query]`) | One public URL resource on one authority | Exact `RequestURI()` match (query included) | No |
| `prefix` | `host` + `prefix` (`/segment...`, not `/`) | Segment-bounded path prefix on `host` | Matches all query variants on matching paths | When matching `>= 100` resources |
| `glob` | `host` + `pattern` (`/segment/*/...`, not `/**`) | Bounded anchored path glob on `host` | Matches all query variants on matching paths | When matching `>= 100` resources |
| `tag` | `host` + `tags` (`1..16` tags) + optional `tag_match` (`"any"` default or `"all"`) | Cached resources on `host` carrying matching origin tags | Matches all query variants of tagged entries | When matching `>= 100` resources |
| `host` | `host` (or `route_id` resolving to explicit `server_names`) | Every cached resource under the selected `host` authority | Matches all paths and query variants on `host` | **Yes (`confirm_broad = true`)** |
| `key` | `key` (`METHOD\nhost\nrequest_uri`) | Legacy single internal cache key | Exact key | No |
| `all` | `all = true` | Entire node cache across all hosts | All keys | **Yes (`confirm_broad = true` on `/api/v1`)** |

#### 1.1 Authority, scheme, port, and case rules

- Cache keys in Jul.IA are `METHOD + "\n" + strings.ToLower(r.Host) + "\n" + r.URL.RequestURI()`.
- Selector `host` is normalized with `strings.ToLower(strings.TrimSpace(host))` (identical to `keyFor`). It must be a syntactically valid host or bracketed IPv6 literal with an optional `:port` (`1..65535`), maximum `255` bytes, with no userinfo, path, query, fragment, whitespace, or control bytes.
- Only `http` and `https` schemes are accepted in `exact_url`; any other scheme, opaque URI, userinfo, or `#fragment` is rejected with `400 invalid_request`. Because Jul's cache key does not partition by scheme (`METHOD\nhost\nrequestURI`), the scheme in `exact_url` is a syntactic validation gate only and matches cached representations for that `host` and `requestURI` regardless of whether the request arrived over HTTP or HTTPS.
- **Literal port fidelity:** Because HTTP requests carrying an explicit `:80` or `:443` in `Host` produce a distinct cache key from a bare hostname, Jul never silently strips or adds `:80` / `:443`. If both `host` and an absolute `url` with an authority are provided, their normalized `host[:port]` values must be identical or validation fails with `400 invalid_request`.
- **Authoritative route mapping (`route_id`):** Callers may supply an optional `route_id` from the serving configuration (`[servers.locations].route_id`, [ADR 0019](0019-configuration-authority-generated-contracts-and-resource-identity.md)). The server resolves `route_id` against the active configuration's `servers[].server_names` and location `match.path` without introducing a separate application registry. If `host` is also provided, it must belong to the route's configured `server_names`.

#### 1.2 Escaped paths, dot segments, and prefix boundaries

- Paths in `exact_url`, `prefix`, and `glob` must start with `/`, contain no NUL or control characters (`<= 0x1f` or `0x7f`), contain no raw backslashes `\`, and have valid `%XX` hex escapes.
- The selector parser preserves the exact raw escaped `RequestURI` bytes (including the exact case of `%XX` hex escapes and raw query string in `exact_url`) rather than re-encoding through `url.URL.String()`, ensuring byte-for-byte alignment with `r.URL.RequestURI()` in `keyFor`.
- Dot segments (`/.`, `/..`, `/./`, `/../`, and percent-encoded equivalents `%2e` / `%2E`) are rejected in selectors (`400 invalid_request`) to prevent traversal ambiguity.
- **Escaped path separators (`%2F` / `%2f`):** Jul preserves `r.URL.RequestURI()` in cache keys, where `%2F`/`%2f` is an encoded byte sequence within a path segment and `/` is a path-segment delimiter. Selector matching never decodes `%2F` or `%2f` into `/`; `/articles/42` and `/articles%2F42` remain distinct resources.
- **Segment-boundary prefix rule:**
  - `prefix = "/articles/"` (trailing `/`) matches paths starting with `/articles/` (such as `/articles/` and `/articles/42`), and does not match `/articles` or `/articles-archive`.
  - `prefix = "/articles"` (no trailing `/`) matches `/articles` exact and `/articles/...` (where the character immediately following `/articles` in the path is `/`), and does not match `/articles-archive` or `/articles%2F42`.
  - `prefix = "/"` is rejected on `kind = "prefix"` with a clear error directing the caller to `kind = "host"` so a broad host purge is never triggered accidentally as a path prefix.
  - Including `?` in `prefix` or `glob` is rejected (`400 invalid_request`); `prefix` and `glob` always match the path-space across all query strings on `host`.

#### 1.3 Bounded anchored glob syntax

- Glob patterns are anchored to the full path (before `?`) on `host`:
  - A glob pattern may contain zero or more `*` or `**` wildcards. A wildcard-free anchored pattern (e.g. `pattern = "/articles"`) matches that exact path and all of its query variants on `host` without matching subpaths (`/articles/42`).
  - `*` matches **one path segment** (zero or more characters excluding literal `/`; `%2F` stays within the segment).
  - `**` is permitted **only as a standalone path segment** (`/**` at the end or `/**/` between slashes) and matches **zero or more path segments** (for example, `/articles/**/comments` matches `/articles/comments`, `/articles/42/comments`, and `/articles/2026/10/42/comments`). `**` embedded inside a literal segment (e.g. `/a**b`) is rejected.
  - Whole-host `/**` is rejected on `kind = "glob"`, directing the caller to `kind = "host"`.
  - Regex and shell metacharacters (`?`, `#`, `[`, `]`, `{`, `}`, `\`, `^`, `$`, `(`, `)`, `|`) are rejected with `400 invalid_request`; standard RFC 3986 path sub-delimiters such as `+` are matched literally.
- **Complexity bounds:** pattern length `<= 512` bytes, path segments `<= 32`, single-segment `*` wildcards `<= 8`, multi-segment `**` wildcards `<= 2`. Matching (`matchAnchoredGlob`) uses a zero-allocation stack-bounded segment index scan (`[128][2]int`) with two-pointer `**` backtracking bounded by the segment and wildcard caps.

#### 1.4 Logical resource identity across GET, HEAD, Vary, and Range representations

- Every matched logical resource `resKey = host + "\n" + requestURI` owns both `GET\n<host>\n<requestURI>` and `HEAD\n<host>\n<requestURI>` base keys, every variant key listed in a `Vary` stub's `Variants` membership slice (up to 64 variants per base key), and complete cached representations used by Option A range serving ([ADR 0024](0024-range-aware-cache-option-a.md)) across both the memory and disk tiers.
- Tag indexing (`baseIndexedTags[resKey]` and `tagIndex[host][tag][resKey]`) computes the deduplicated tag union across all live `GET`, `HEAD`, and `Vary` representations of `resKey`. When any representation of `resKey` matches a selector at purge time, all owned `GET`, `HEAD`, and `Vary` representations of `resKey` are invalidated together and `resKey` is recorded in `selectorFence.matchedResources` so no pre-invalidation sibling representation (even one carrying a different tag) can republish stale content after the purge.
- An invalid, empty, or cross-scope selector is always rejected with `400 invalid_request` and never falls back to a broader purge.

---

### 2. Trusted origin tag ingestion and namespace isolation

#### 2.1 Explicit opt-in configuration (`[cache].tag_headers` and `[cache].strip_tag_headers`)

- `[cache].tag_headers` (`[]string`, default empty = disabled, lifecycle `hot_reload`): names up to `8` trusted origin response headers (for example `["Cache-Tag"]` or `["Cache-Tag", "Surrogate-Key"]`) from which Jul extracts grouping tags.
  - Header names must be `1..64` byte HTTP tokens and may not name reserved protocol, framing, validator, or credential headers (`ETag`, `Cache-Control`, `CDN-Cache-Control`, `Expires`, `Age`, `Date`, `Last-Modified`, `Vary`, `Location`, `Content-Location`, `Content-Length`, `Content-Type`, `Content-Encoding`, `Content-Range`, `Transfer-Encoding`, `Set-Cookie`, `Authorization`, `WWW-Authenticate`, `Proxy-Authenticate`, `Proxy-Authorization`, `Connection`, `Keep-Alive`, `Upgrade`, `TE`, `Trailer`, `X-Cache`, `X-Request-ID`).
- `[cache].strip_tag_headers` (`*bool`, default `true` when `tag_headers` is non-empty, lifecycle `hot_reload`): when effective `true`, Jul removes the configured `tag_headers` from stored `Entry.Header` maps and from live downstream response headers across all HTTP response paths—including cache hits, cacheable and uncacheable misses, `304` revalidations, `1xx` interim responses (preserving any pre-`1xx` tag headers for final `2xx` capture), `Cache-Control: no-store` and `Range` bypasses, declined protocol-upgrade HTTP responses (`Connection: Upgrade` + `Upgrade` requests where the origin returns an HTTP response rather than hijacking), validation fallbacks, and unsafe/non-GET/HEAD methods—via `serveDownstreamWithStrip` (`respwriter.Wrap`, preserving `http.Flusher` and `http.Hijacker`). Raw bytes after a genuine connection hijack remain outside HTTP response framing.

#### 2.2 Origin-only trust boundary, bounds, and conservative failure policy

- Tags are extracted **exclusively** from the origin response header delta (`snapshot` / `recorder.header`), never from client request headers (`r.Header`) or pre-existing outer response headers.
- Raw header lines and comma-separated segments are checked for credential-like schemes and prefixes (`Bearer ...`, `Basic ...`, `Digest ...`, `Negotiate`, `NTLM`, `OAuth`, JWT `eyJ...`, `token=`, `secret=`, `apikey=`, `cookie=`, `authorization=`) **before** whitespace tokenization so multi-word `Authorization`-style values cannot split into accepted tokens. Recognition of arbitrary unprefixed secrets is heuristic; operators must only configure dedicated grouping-tag headers in `[cache].tag_headers`.
- Header lines are then split on commas and ASCII whitespace into deduplicated, sorted tags.
- **Bounds:**
  - Tag syntax: `1..128` visible ASCII characters (`0x21..0x7e`), excluding `,`, `;`, `"`, `'`, and whitespace, and rejecting credential-like patterns (`bearer `, `basic `, JWT `eyJ...`, `token=`, `secret=`, `password=`, `cookie=`, `authorization=`).
  - Per-entry limits: at most `MaxTagsPerEntry = 32` distinct tags and `MaxTagBytesPerEntry = 1024` total tag bytes per entry; raw tag header lines on a single response may not exceed `MaxRawTagHeaderBytes = 4096` bytes.
  - Process-wide index limit: at most `MaxTotalTagLinks = 65,536` `(host, tag) -> resKey` relationships across the cache.
- **Hot-reload and startup tag-policy transitions (`commitPolicyAndSyncTags` & `onDiskRehydrated`):** Each indexed entry records `retainedTags`, `tagPolicyTracked`, and the `ingestedTagHeaders` policy under which it was stored (persisted in `JULDCH02` `diskEntryMeta` as `TagPolicyTracked` and `TagHeaders`). Disabling `tag_headers` clears `tagIndex` while preserving `retainedTags` and true `ingestedTagHeaders` provenance on resident entries; re-enabling the same `tag_headers` reconstructs `tagIndex` from `retainedTags` across memory and disk without losing invalidation coverage. On both live reloads and startup rehydration when `tag_headers` is non-empty, any entry whose persisted `TagPolicyTracked` provenance is absent or whose `ingestedTagHeaders` differs from the active `tag_headers` policy is conservatively retired (`deleteAtMostGen`) rather than assigned fabricated provenance.
- **Conservative failure policy:** If a cacheable origin response carries a configured tag header that violates syntax, credential-safety, per-entry, or index-capacity bounds:
  1. The client still receives the complete origin response normally (with `tag_headers` stripped when `strip_tag_headers` is enabled).
  2. Jul **does not store** the response in the shared cache (and on a `304 Not Modified` carrying invalid tag headers, discards the stored entry rather than retaining stale or incomplete tags), incrementing `jul_cache_tag_ingest_skipped_total{reason="invalid"|"oversize"|"index_limit"}`. This prevents an entry from ever being cached without the tags the origin expected to invalidate it by.
- **Host namespace isolation:** Every indexed tag is scoped to `(host, tag)` where `host` is the entry's lowercased `r.Host`. A tag emitted for `app-a.example.com` can never match or invalidate cached entries on `app-b.example.com`.
- **`304 Not Modified` tag-delta and Vary-group semantics:**
  - Before `merge304` strips `tag_headers` from the merged response headers, Jul evaluates the `304` response's configured `tag_headers`:
    - If no configured tag header line is present on the `304` response, `refreshed.Tags` retains `stored.Tags`.
    - If one or more configured tag header lines are present on the `304` response, their tags are parsed and validated using the same per-entry and syntax bounds. An explicitly empty header (e.g. `Cache-Tag: `) clears `refreshed.Tags` to `nil`; valid non-empty tags replace `refreshed.Tags`.
    - If the `304` tag headers are malformed, credential-like, or exceed per-entry/index bounds, `merge304` returns `mergeDiscard`, deleting the stored resource group and incrementing `jul_cache_tag_ingest_skipped_total`.
  - For `Vary` resources, each variant entry stores its own `BaseKey` and `Tags`, and the `Vary` stub under `BaseKey` stores the deduplicated union of its live variants' `Tags` under `varyMu`. Updating or evicting a variant recomputes the stub's tag union and updates `tagIndex[host][tag][resKey]` atomically.

---

### 3. Index architecture and concurrent fill/revalidation fencing

#### 3.1 Host-partitioned key catalog + bounded tag index (versus unindexed scans)

- **Rejected alternative — unindexed whole-cache or disk-file scans:** Scanning every key across all hosts or opening disk files on each purge degrades linearly with total cache occupancy and adds disk I/O to administrative operations.
- **Rejected alternative — request-path prefix/glob trees:** Maintaining complex trie/glob structures on every cache read or write adds lock contention and memory overhead without helping warm cache hits.
- **Selected design:**
  - `Cache` separates active/incomplete resident-entry fences (`c.incompleteFenceGen`) from completed fill-publication fences (`c.latestFenceGen`). In steady state when no purge is actively mid-batch or incomplete (`c.incompleteFenceGen.Load() == 0`), warm memory hits (`memStore.getWithGen`) perform **zero** `genMu` lock acquisitions; only when `mgen <= c.incompleteFenceGen.Load()` does `c.isResidentEntryFenced` acquire `genMu` to check active or incomplete fences.
  - `Cache` maintains an in-memory host-partitioned base-key catalog (`byHost[host][baseKey]`) and a bounded logical-resource tag reverse index (`tagIndex[host][tag][resKey]`) updated when entries are stored, replaced, revalidated (`304`), deleted, purged, or rehydrated from `JULDCH02` trailer metadata on startup.
  - `byResourceID[SHA256(resKey)]` resolves the same live catalog to its GET and HEAD base keys independently of current tags. Its entries are reclaimed with the last resident or in-transit representation, reset on global purge, and rebuilt during disk rehydration. Tag retries therefore resolve original targets directly without scanning unrelated host entries or persisting raw request URIs.
  - **Pinned memory-victim retention:** When an entry is evicted from `memStore` into `victims` for asynchronous write-through to `diskStore` (`mem.onEvictItem`), its `keyGenState` remains pinned (`inflight > 0`) and its `byHost` and `tagIndex` entries remain registered until `onEvictItem` either publishes the victim to `diskStore` or finishes `finishKeyOp` without disk residency (`!inMem && !inDisk && inflight == 0`). Thus an entry in transit between memory eviction and disk write-through can never disappear from selector scans or evade a purge.
  - `exact_url` and `key` resolve in `O(1)` lookups; `tag` resolves in `O(matching resources)` via `tagIndex[host][tag]`; `host`, `prefix`, and `glob` iterate `byHost[host]` in memory with bounded candidate collection (`candidateBaseKeysLocked` stops at `maxScan` before sorting so preview and preflight never copy or sort an unbounded host map).
  - Eviction, replacement, expiry cleanup, deletion, full purge, and hot-reload disabling of `tag_headers` immediately reclaim catalog and `tagIndex` entries.

#### 3.2 Linearization boundary, conditional generation deletion, and bounded selector fences

- **Completion boundary, durable high-water generation, and conditional deletion:** Every scoped invalidation acquires a monotonic sequence number `fenceGen = c.genSeq.Add(1)` (or reuses `opts.ReuseFenceGen`) under `c.genMu`, snapshots matched logical resources into `selectorFence.matchedResources`, and persists `fenceGen` to `.jul-cache-gen` in `disk_path` (`diskStore.recordHighWaterGen`). On startup, `cache.New` initializes `c.genSeq` to at least the persisted high-water mark from `.jul-cache-gen`, `.jul-cache-purge-ledger`, and rehydrated `JULDCH02` entries before serving traffic, ensuring cross-boot retry/continuation never invalidates fresh post-restart fills.
  - `opts.CommitFence` durably records the operation's cutoff while `c.genMu` still excludes origin publication, before installing the selector fence or deleting resident entries. There is no separate early cutoff reservation in the API. If the intent write fails, resident entries remain unchanged and a publication-only fence conservatively protects matching earlier origin work against an uncertain durable write.
  - For tag invalidations, the callback also commits the original matched logical-resource IDs. A retry uses that immutable ownership snapshot and its original cutoff, rather than re-evaluating current tag membership. This removes old GET/HEAD/Vary siblings even after tags change while preserving every representation newer than the cutoff.
  - Superseded in-flight `fills` (`c.fills`) and revalidations (`c.calls`) matching the selector are detached from the deduplication maps so any new request arriving after `fenceGen` starts a fresh origin fetch immediately.
  - Tier deletions are **generation-conditional** (`deleteIfGenAtMost(key, fenceGen)`): `memStore.delIfGenAtMost(key, fenceGen)` and `diskStore.deleteBatchAtMostGen(keys, fenceGen)` remove an entry only if its stored generation `gen <= fenceGen`, advancing `keyGen[key].gen` (including mid-publication `inflight > 0` entries) past `fenceGen`. A fresh post-fence fill that publishes with `gen > fenceGen` while the purge worker is still iterating keys is never deleted by the purge.
  - Disk cleanup queues every targeted key, including in-flight publications not yet marked disk-resident at snapshot time. Conditional deletion waits for `d.mu` after a publication already past its generation check and removes its old file before acknowledgement. Advancing the catalog generation does not hide an older physical disk generation from conditional cleanup.
- **Lookup and promotion fence gate (`isResidentEntryFencedLocked`):** In `c.get(key)` (both memory hits when `mgen <= c.incompleteFenceGen.Load()` and disk-to-memory promotions), `c.isResidentEntryFencedLocked(entryGen, key, tags)` checks whether `entryGen < c.purgeGen` or whether any actively executing or incomplete/canceled fence (`f.active || f.remaining > 0`) in `c.fences` with `f.gen >= entryGen` matches the entry's `resKey` (`f.matchedResources[resKey]` or `f.sel.MatchesResource`). Once a purge finishes removing all matched resident entries (`!f.active && f.remaining == 0`), `incompleteFenceGen` drops back to `0` so warm hits incur no lock overhead.
- **Per-operation `startGen` tracking:** Every cold-miss fill (`fetchAndStoreWithPolicy` / `leadFill` / range full-object fill), synchronous validation (`leadValidation`), and background revalidation (`startRevalidate` — including refreshes running under a retired handler generation after a config reload) records `opID, startGen := c.beginInflightOriginOp()` **before** invoking the upstream handler and calls `c.endInflightOriginOp(opID)` on completion.
- **Publication and disk-commit gate (`isPublicationFencedLocked` & `canCommitToDisk`):** Inside `setCoreWithStartGen`, `setCapturedWithStartGen`, `mem.onEvictItem`, and `diskStore.publishStreamWithGenFull` (via `d.canCommit` evaluated under `d.mu` **immediately before** `d.renamePath(tmpPath, targetPath)`), `c.isPublicationFencedLocked(startGen, key, tags, extraTags)` checks:
  1. Whether `startGen < c.purgeGen`, `startGen <= c.fenceFloorGen`, or `startGen <= c.hostFenceFloorGen[host]`.
  2. Whether any fence `f` in `c.fences` with `startGen <= f.gen` matches `key`, `f.matchedResources[resKey]`, or `f.sel.MatchesResource(host, reqURI, effTags)` (where `effTags` merges `tags`, `extraTags`, and `c.baseIndexedTags[resKey]`).
  - `d.canCommit` rejects work already fenced when checked. If a fence installs after that check, the purge's queued in-flight disk keys serialize deletion with the publication under `d.mu`; cleanup does not rely on the writer's later post-publication check before acknowledging success.
  - Because `tags` is evaluated at **publication time** after the origin response headers have been parsed, an in-flight fill that was **not resident at preview** or whose **tag (`article:42`) was first learned when the origin response completed** is deterministically fenced and cannot publish its representation into memory or disk.
  - If a cold-miss fill leader is fenced at publication, it still streams the response to its own requesting client, returns `stored = false` to `fillCall.finish(false)`, and wakes any coalesced singleflight waiters to fetch fresh.
  - Vary transitions check publication fencing before mutating membership. Layout-transition and membership-cap deletions are generation-conditional; failed publication rollback is bounded by that publication's own generation, so an old rejected fill cannot delete a newer variant or stub.
- **Bounded fence lifecycle, coalescing, and per-host saturation isolation:**
  - Active and retained fences are stored in `c.fences` capped at `MaxActiveFences = 256`, with duplicate completed selector digests coalesced in place.
  - A fence is reclaimed once `!f.active`, `f.remaining == 0`, and `c.minInflightStartGenLocked() > f.gen`.
  - If `len(c.fences)` ever exceeds `MaxActiveFences = 256` while long-lived origin operations are open, evicting a host-scoped fence advances only that hostname's `c.hostFenceFloorGen[host]` (reclaimed as soon as `minInflightStartGenLocked() > hostFenceFloorGen[host]`), and only affects pre-fence in-flight origin publications on that host—never evicting warm hits or affecting unrelated hosts.

---

### 4. Disk durability, crash recovery, and active reader leases

- **Persisted resource, tag-policy provenance, and Vary-group identity (`JULDCH02`):**
  - `diskEntryMeta` in `JULDCH02` persists `Key string` (the exact storage key), `BaseKey string` (the owning base resource key `METHOD\nhost\nrequestURI`), `Gen uint64`, `Tags []string`, `TagPolicyTracked bool`, and `TagHeaders []string`.
  - On startup `rehydrate()`, `readDiskEntryMeta` validates `meta.Key != ""`, `hashKey(meta.Key) == fileHash`, and valid `BaseKey`/`Tags`/`TagHeaders`. Any pre-`#621` `JULDCH02` file with an empty `Key` or mismatched hash is unlinked on startup as a clean cold miss so no disk entry can exist outside the selector/tag catalog. When `[cache].tag_headers` is non-empty on startup, `onDiskRehydrated` retires any disk entry whose persisted `TagPolicyTracked` is false or whose `TagHeaders` differs from the active `[cache].tag_headers` policy.
  - `pruneOrphanedVaryStubsLocked()` enforces full Vary-group consistency on startup and eviction: any variant entry (`it.baseKey != "" && it.baseKey != it.key`) whose owning `Vary` stub (`hashKey(it.baseKey)`) is absent or does not list `it.key` in `stub.variants` is retired and unlinked; any `Vary` stub trims missing variants and unlinks itself if no live variants remain.
  - `Cache.New` reconstructs `byHost`, `tagIndex`, and `keyGen` (`inDisk = true`) from the rehydrated `diskStore` items before serving traffic.
- **Crash-consistent prepare/commit tombstone deletion (`diskStore.deleteBatchAtMostGen`):**
  1. Under `d.mu`, `diskStore` identifies all target disk items with `it.gen <= maxGen` (plus any pinned blobs for those hashes), records their hashes in `d.tombstones`, and atomically writes and `fsync`s `.jul-cache-tombstones` (`JULTOMB1`, plus parent-directory `fsync` on Unix) **before** removing in-memory visibility or unlinking files.
  2. Next, `diskStore` removes the matched items from `d.items` (`it.gen <= maxGen`) and retires each backing `diskBlob` via `retireBlobLocked`:
     - If `blob.readers == 0`, the file is unlinked (`os.Remove`) immediately and its tombstone is cleared.
     - If active readers hold leases (`blob.readers > 0`), the file is renamed to `.jul-cache-retired-*` (or pinned with its durable tombstone preserved on Windows/rename failure) so already-admitted readers finish streaming without truncation while new requests never see the invalidated entry.
  3. Finally, `diskStore` persists the updated `.jul-cache-tombstones` manifest (removing it when empty) and invokes `syncDirLocked()` on both non-empty manifest writes and empty-manifest removal. If `syncDirLocked()` or manifest writing fails, `tombstoneDirty` is latched and `deleteBatchAtMostGen` reports `uncertain = true` so the operation returns `state = "partial"` or `"failed"` with `invalidation_effective = "uncertain"`. (Note on verification scope: tests verify crash-state snapshots and injected `write`/`unlink`/`rename`/`syncDir` failures; directory `fsync` is best-effort on Windows where directory `FlushFileBuffers` is not supported by the OS, and tests do not claim physical hardware power-loss certification.)
     Before reporting success, purge execution reconciles outstanding disk tombstones and deferred retired-file cleanup even when the catalog has no remaining matches. Dirty empty-manifest cleanup retries directory sync even if the manifest pathname is already absent. Unresolved disk-wide cleanup or durability errors remain visible in the outcome; durably recorded pinned readers may finish without blocking successful retirement.
  4. **Fail-closed error handling and filesystem boundary:** If pre-unlink tombstone persistence fails, `diskStore` attempts to invalidate `.jul-cache-tombstones` in place via `os.Truncate(tombPath, 0)` (which `loadAndSweepTombstonesLocked` detects as a corrupt tombstone manifest on restart and treats by purging all cache files via `purgeAllCacheFilesOnCorruptTombstoneLocked`), attempts unlink and in-place `os.Truncate(path, 0)` (destroying the 24-byte `JULDCH02` trailer) for idle target files, sets `d.unrecoverableInvalidation.Store(true)` (immediately disabling all in-process disk-tier lookups), and reports the exact `failed_entries` count so the operation returns `state = "partial"` or `"failed"` with `invalidation_effective = "uncertain"` — never `succeeded`. If the underlying filesystem refuses all writes, renames, unlinks, and truncations (for example a read-only remounted disk) and then crashes, `invalidation_effective = "uncertain"` accurately informs the operator that disk state could not be mutated and must be cleared or repaired before restart.

---

### 5. Surface classification, API/CLI/Console workflow, RBAC, and idempotency

#### 5.1 ADR 0014 classification, RBAC, and operational authority

- Scoped cache invalidation is a runtime operator and automation capability ([ADR 0014](0014-operability-surfaces.md)):
  - **External API (`/api/v1`):**
    - `POST /api/v1/cache/purge/preview` (`StabilityExternal`, `Permission: cache:purge`, rate-limit class `limitRead`, errors `invalid_request`, `payload_too_large`, `unsupported_media_type`)
    - `POST /api/v1/cache/purge` (`StabilityExternal`, `Permission: cache:purge`, rate-limit class `limitWrite`, errors `invalid_request`, `not_found`, `idempotency_key_reused`, `idempotency_key_in_flight`, `storage_unavailable`, `payload_too_large`, `unsupported_media_type`)
    - `GET /api/v1/cache/purges/{operation_id}` (`StabilityExternal`, `AnyPermissions: [cache:purge, audit:read, admin:manage]`, rate-limit class `limitRead`, errors `invalid_request`, `not_found`, `storage_unavailable`; restricted to the initiating principal unless the caller holds `admin:manage` or `audit:read`, returning `404 not_found` otherwise)
  - **Why `cache:purge` is required on preview and operation lookup:** Requiring `cache:purge` prevents a read-only `status:read` (`viewer`) principal from probing whether specific URLs or business tags (`article:42`) are cached or inspecting another principal's purge operations.
  - **Retired internal API:** `POST /cache/purge` and its `?key=...` form are removed by #674 / ADR 0030 after Console migration. Use the versioned endpoints above for full or key-selector purges; no legacy HTTP alias remains.
  - **Remote CLI:** `jul cache purge` (`--preview`, `--url`, `--host`, `--prefix`, `--glob`, `--tag`, `--tag-match`, `--route-id`, `--key`, `--all`, `--confirm-broad`, `--preview-token`, `--idempotency-key`, `--operation-id`, `--json`).
  - **Full Console:** Scoped invalidation workflow (`select scope -> preview -> confirm -> execute -> observe result/recover`) inside the Cache Traffic Control drawer.
- **Configuration authority (`managed` vs `file_owned`):** Cache invalidation is an operational runtime action, not a configuration-file mutation. Both `managed` and `file_owned` deployments allow `preview` and `execute` under RBAC (`cache:purge`), while configuration edits to `[cache]` remain read-only in `file_owned` mode.

#### 5.2 Preview consistency, confirmation binding, and live permission re-verification

- Preview is read-only and network-free. It returns `matched_resources`, `matched_representations` (deduplicated across memory and disk tiers), `memory_entries`, `disk_entries`, `matched_bytes`, `inflight_fills_matched`, `count_accuracy` (`"exact"` or `"lower_bound_truncated"`), `truncated`, `as_of`, `cache_sequence`, `execution_mode_contract: "reevaluate_selector_with_fence"`, `broad_scope`, a principal-and-selector-bound `preview_token`, and non-null `warnings` and `samples` arrays (`[]` when empty).
- Execution re-evaluates the normalized selector under a new selector fence at the execution linearization point.
  - When `preview_token` is supplied, the server verifies that the authenticated principal and normalized `selector_digest` match the token (`400 invalid_request` with `details.field = "preview_token"` on mismatch).
  - Server-owned broad-scope confirmation (`confirm_broad = true`) is enforced at execution time whenever `normSel.BroadScope` (`host` or `all`), the supplied `preview_token` was minted for a broad/truncated preview, or execution matches `>= 100` resources (`BroadScopeResourceThr = 100`), returning `400 invalid_request` with `details.field = "confirm_broad"` before installing a fence or mutating cache state.
  - At the execution admission boundary (after reading the request body and acquiring an execution slot), `verifyLivePurgePermission` re-authenticates the request credential against the latest live `s.currentAuth()` snapshot (`authenticateAdminRBAC` in RBAC mode, rejecting legacy/anonymous context identities when RBAC is active, and unconditionally calling `checkLegacyToken` in legacy token mode) so any credential rotation, role revocation, or open-to-secured auth tightening during admission immediately denies execution (`401`/`403`).

#### 5.3 Bounded operation state machine, cancellation, and durable idempotency

- Operation states form a closed set: `"running"` | `"succeeded"` | `"partial"` | `"failed"` | `"canceled"`.
- **Scan/execution bounds and cancellation:**
  - Preview scans at most `MaxPreviewScanEntries = 10,000` candidate entries (`candidateBaseKeysLocked` stops at `scanLimit` before sorting); if more exist in scope, it sets `truncated = true` and `count_accuracy = "lower_bound_truncated"`.
  - Execution preflights narrow selectors (`prefix`, `glob`, `tag`, or route-scoped `host`) against `MaxPurgeScanKeys = 100,000` before installing a fence; whole-hostname (`kind = "host"` with no `route_id`) and `kind = "all"` are the documented escape paths and are never rejected by `MaxPurgeScanKeys`.
  - Cancellation is checked before waiting for `genMu`, after acquiring it, and before starting the cutoff commit, so canceled waiters abort without committing intent or mutating entries. Once the atomic cutoff commit begins, fence installation completes even if cancellation arrives during the durable write. If cancellation or a deadline occurs while physical tier deletion is running, `f.active` is cleared while the selector fence remains retained via `f.remaining > 0` (`incompleteFenceGen >= fenceGen`, so all matching resident entries and in-flight publications `<= fenceGen` remain fenced), the operation records `state = "canceled"`, `remaining_entries > 0`, and `invalidation_effective = "incomplete"`, and a subsequent retry (`retry_operation_id` or `Idempotency-Key`) finishes removing the remaining `<= fenceGen` entries and releases the fence.
- **Durable idempotency and operation ledger:**
  - `POST /api/v1/cache/purge` accepts an optional `Idempotency-Key` (`[A-Za-z0-9_-]{8,128}`).
  - Operations and idempotency bindings (`principal + Idempotency-Key -> request_fingerprint + operation_id + fence_sequence`) are retained in a bounded 256-entry ledger (`min_age_seconds = 900`) and, when `disk_path` is enabled, atomically persisted to `.jul-cache-purge-ledger` in `disk_path`.
  - The private ledger's `matched_resource_ids` stores sorted SHA-256 logical-resource IDs for incomplete tag operations; it never stores raw query values or Vary values. `null` means no snapshot is available, while `[]` means the original scope matched no resident resources. Completed successful operations release this retry-only snapshot. Malformed or oversized durable snapshots fail closed with `503 storage_unavailable`.
  - Concurrency admission (`maxConcurrentCachePurges = 4`) and live RBAC verification run **before** any ledger eviction or mutation. Running operations (`!Terminal`) and keyed bindings younger than `900s` are protected against eviction by any incoming request (whether keyed or unkeyed); if all 256 slots hold protected records, new operations fail closed with `429 rate_limited` (`Retry-After: 1`).
  - `handleV1CachePurge` admits and persists a `"running"` record, then supplies `InvalidateOptions.CommitFence` to `InvalidateScope`. The engine allocates the operation's `fence_sequence` under `c.genMu`; the callback durably updates the running record before the engine installs its fence or releases the generation lock. An initial record with cutoff `0` has not yet linearized and cannot authorize automatic interrupted-operation continuation. When `disk_path` is configured, unreadable/corrupt ledgers or intent write failures fail closed with `503 storage_unavailable` without deleting resident entries. If post-execution ledger persistence fails after cache mutation, the response is downgraded to `state = "partial"`, `invalidation_effective = "uncertain"`.
  - Any `"running"` record found on startup recovery is transitioned to `state = "failed"`, `outcome = "interrupted"`, `invalidation_effective = "uncertain"`, retaining its reserved `fence_sequence`. Because `diskStore.rehydrate()` seeds `highWaterGen` from `.jul-cache-gen`, `.jul-cache-purge-ledger`, and surviving `JULDCH02` entries, retrying an interrupted record with a recorded `fence_sequence` safely continues at the original cutoff without invalidating post-restart fills. If a recovered interrupted/uncertain record has `fence_sequence == 0` (unknown cutoff) or exceeds the cache's startup high-water mark, automatic continuation is refused rather than silently executing a fresh purge.
  - A retry canceled before its cutoff commit retains the original `fence_sequence` and operation `boot_id`, so another retry cannot expand the cutoff or bypass cross-boot high-water validation. Failed ledger cleanup retains the idempotency binding exactly once and replaces an eligible eviction victim rather than exceeding the retention bound. Rollback never restores an expired binding over a newer operation that reused its key while execution was outside the ledger lock.
  - Replaying a request with the same `(principal, Idempotency-Key)` and identical selector after `succeeded` returns the original operation record (`idempotent_replay = true`) without executing a second purge. Reusing an `Idempotency-Key` with a different selector returns `409 idempotency_key_reused`.
  - If an existing `Idempotency-Key` and `retry_operation_id` identify different operations, execution and replay are rejected with `409 idempotency_key_reused` before mutating either record. Legacy tag operations lacking a durable ownership snapshot may upgrade from a retained matching fence; if that ownership is also unavailable, continuation returns `400 invalid_request` rather than declaring cleanup complete. Recovery requires a fresh host or all purge with explicit broad confirmation and a new idempotency key; repeating the current tag selector cannot recover unknown historical membership.
  - An operation-ID retry may omit `Idempotency-Key` or reuse the original bound key. A newly supplied, unbound key is rejected with `400 invalid_request` before execution or replay instead of being silently ignored or added as an alias. Operation identity and its original cutoff remain canonical.

#### 5.4 Secret safety, Console execution authority, and bounded observability

- Raw query strings, `Vary` header values, cookies, tokens, and response bodies are never returned in preview/result DTOs, browser storage, logs, or audit details (in `exact_url` and `key` with a query string, display/retained/audited selector summaries redact query parameter values as `?<redacted>`).
- The Console keeps the unredacted executable `CacheSelector` in transient component memory for confirmation, retry, and POSIX single-quoted CLI snippet generation (`shellQuote`), never echoing the redacted display DTO (`?<redacted>`) or fabricated `route_id` values back as executable authority.
- Prometheus metrics use closed, low-cardinality label sets only:
  - `jul_cache_invalidations_total{selector, outcome}` (`selector` in `exact_url|prefix|glob|tag|host|key|all`, `outcome` in `succeeded|succeeded_no_matches|partial_failure|failed|canceled|cache_disabled`)
  - `jul_cache_invalidation_entries_total{tier}` (`tier` in `memory|disk`)
  - `jul_cache_tag_ingest_skipped_total{reason}` (`reason` in `invalid|oversize|index_limit`)

---

### 6. Reusable contracts for `#622` (`CACHE-OPS-02`) and `#624` (`CACHE-OPS-04`)

1. **For `#622` (`CACHE-OPS-02` — Cache inspection and decision explanations):**
   - `cache.Selector`, `cache.ParseSelector`, and `cache.NormalizedSelector` provide the shared host/route/URL/prefix/glob/tag selector parser and normalizer.
   - `cache.SafeResourceSummary`, `cache.PurgePreview`, and `(*cache.Cache).PreviewScope(sel cache.NormalizedSelector, opts cache.PreviewOptions) cache.PurgePreview` expose deduplicated resource vs representation vs per-tier counts, freshness/stale windows, `Vary` header names (without raw request header values), and origin tags without updating LRU residency or holding reader leases.
2. **For `#624` (`CACHE-OPS-04` — Explicit retain/invalidate cutover on route/backend changes):**
   - `(*cache.Cache).BeginSelectorFenceWithError(sel cache.NormalizedSelector) (fenceGen uint64, complete func(), err error)` (alongside the 2-return convenience wrapper `BeginSelectorFence`) and `(*cache.Cache).InvalidateScope(ctx context.Context, sel cache.NormalizedSelector, opts cache.InvalidateOptions) (cache.InvalidateResult, error)` (with `opts.ReuseFenceGen`) provide the commit-time fencing and durable invalidation primitives, allowing `#624` to observe durable `.jul-cache-gen` marker persistence failures directly.
   - **Route-to-cache cutover derivation contract (`cache.DeriveRouteCutoverSelectors(before, after *config.Config) []cache.NormalizedSelector`):**
     - Compares `before` and `after` configurations (including full `UpstreamConfig` and `LocationConfig` identities):
       - Because router host selection canonicalizes `r.Host` by stripping explicit port suffixes (`middleware.CanonicalHTTPHost`) while cache keys preserve the literal `r.Host` authority, route-derived cutover selectors set `cutoverMatchAnyPort: true` so a named server `portal.example.com` on `:8080` matches cached entries under both bare `portal.example.com` and `portal.example.com:8080` (or any explicit port), whereas explicit public purge selectors retain strict literal-port matching.
       - A changed/deleted route with explicit `server_names` (no wildcard `*`) and `match.type == "exact"` produces a cutover selector with `cutoverExactPath: loc.Match.Path` (matching that exact literal path—treating `*`, `+`, etc. literally—and URL-decoded canonical path equivalents across all query variants).
       - A changed/deleted route with explicit `server_names` (no wildcard `*`) and `match.type == "prefix"` produces a cutover selector with `cutoverRawPrefix: loc.Match.Path` (matching `strings.HasPrefix` on both raw and URL-decoded canonical paths, identical to `internal/router/matcher.go`, or `kind = "host"` when `loc.Match.Path == "/"`).
       - A changed/deleted route with `match.type == "regex"`, wildcard/empty `server_names`, or any unparseable path widens conservatively to `kind = "host"` (or `kind = "all"` when `server_names` is empty/wildcard) and never silently drops a route.
     - **Handler-generation precondition for `#624`:** Because `BeginSelectorFence` fences origin operations by `startGen <= fenceGen` (assigned when an origin fetch/validation begins in `beginInflightOriginOp`), an old handler request admitted before handler swap that has not yet entered `beginInflightOriginOp` would acquire `startGen > fenceGen`. Therefore, when `#624` wires automatic cutover invalidation into reload `Publish`, it must either install the fence / run `InvalidateScope` after retiring/draining pre-swap route admissions or bind request-context handler-generation checks so pre-swap requests cannot publish into the cache after cutover.

## Consequences

- **Positive:** Operators and deployment pipelines can invalidate an exact URL, path prefix, bounded glob, origin grouping tag (`article:42`), or hostname with a non-destructive preview, strong concurrency/fill fencing, crash-safe disk durability, and consistent API/CLI/Console semantics.
- **Trade-offs accepted:**
  - Prefix and glob invalidation perform a bounded in-memory scan over the selected host's key catalog (`O(host_keys)`), capped by `MaxPurgeScanKeys`, rather than maintaining per-segment prefix trees on every cache write.
  - Malformed or oversize origin tag headers cause the response to be served uncached (`jul_cache_tag_ingest_skipped_total`) rather than stored with incomplete tag metadata.

## Related

- [#621](https://github.com/victornife/jul/issues/621) — Scoped URL, prefix, pattern and tag invalidation with preview (`CACHE-OPS-01`)
- [#620](https://github.com/victornife/jul/issues/620) — Cache operations epic (`CACHE-OPS`)
- [#622](https://github.com/victornife/jul/issues/622) — Cache inspection and decision explanations (`CACHE-OPS-02`)
- [#624](https://github.com/victornife/jul/issues/624) — Explicit cache behavior during route and backend changes (`CACHE-OPS-04`)
- [ADR 0023](0023-large-object-disk-streamed-cache.md) — Large-object disk-streamed cache
- [ADR 0024](0024-range-aware-cache-option-a.md) — Range-aware response caching (Option A)
