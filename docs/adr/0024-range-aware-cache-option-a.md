# ADR 0024 — Range-Aware Response Caching (Option A: Complete-Object Fill and Bounded Slice Serving)

- **Status:** Accepted — Beta/unreleased
- **Date:** 2026-10-15
- **Deciders:** Victor Niharra (`@victornife`)
- **Applies to:** `internal/cache`, `[cache]` configuration, lifecycle classification, observability metrics, and NGINX migration assessment
- **Source:** [#442](https://github.com/victornife/jul/issues/442), [#525](https://github.com/victornife/jul/issues/525), [#505](https://github.com/victornife/jul/issues/505), [#521](https://github.com/victornife/jul/issues/521), [#113](https://github.com/victornife/jul/issues/113), [ADR 0023](0023-large-object-disk-streamed-cache.md)

## Context

Under decision D05 (#107, #132), every HTTP request carrying `Range` or `If-Range` bypassed Jul.IA's response cache unconditionally (`X-Cache: BYPASS`), neither consulting stored representations nor storing `206 Partial Content` responses. While that rule prevented fragment-splicing and partial-body corruption before Jul had bounded capture memory (#505), cold-miss coalescing (#525 Part 1), or disk-streamed large-object storage (#525 Part 2, [ADR 0023](0023-large-object-disk-streamed-cache.md)), it also meant that media, archive, and artifact download workloads issuing `Range: bytes=...` requests gained zero cache benefit—even when the complete representation was already cached or could be streamed to the disk tier.

With all four prerequisites (#505, #521, #525 Part 1, and #525 Part 2) delivered on `main`, [#442](https://github.com/victornife/jul/issues/442) evaluates three options under the [#113](https://github.com/victornife/jul/issues/113) experiment gate:

- **Option A — Complete-object fill and local range serving:** Store only complete cacheable representations (`200 OK`); on an eligible range miss, coordinate one complete-object origin fill and satisfy the triggering range and subsequent single byte ranges via bounded random access (`BodyReader` `io.ReaderAt`/`io.Seeker`).
- **Option B — Fragment/slice caching:** Store and merge partial `206` fragments.
- **Option C — Retain unconditional bypass:** Keep D05 as the only behavior.

## Decision

### 1. Opt-in Option A activation (`[cache].range_enabled` and `[cache].max_ranges`)

Jul.IA adopts **Option A** as an **explicit opt-in** capability controlled by `[cache].range_enabled` (default `false`) and `[cache].max_ranges` (default `1` when `range_enabled = true`, configurable from `1` to `16`, rejected when set with `range_enabled = false`), both classified `hot_reload`:

- When `range_enabled = false` (default), decision D05 remains unchanged: any request carrying `Range` or `If-Range` bypasses the cache before lookup and storage (`X-Cache: BYPASS`).
- Why opt-in is required: under Option A, a cold range miss for a tiny byte slice (for example `bytes=0-1023` on a `4 MiB` file) fetches the complete `4 MiB` representation from the origin. For repeatable media/artifact workloads (`SYNTH-442-MEDIA-V1` Profile R1/R2), that single origin fill eliminates 95–99% of subsequent origin requests and bytes; for one-shot sparse probes (Profile R3), it amplifies origin bandwidth. Keeping `range_enabled = false` by default preserves zero-amplification bypass unless the operator opts in.

### 2. Complete-representation invariant (no fragment storage)

- Only complete cacheable representations (`200 OK`, never `206 Partial Content`) are ever stored in memory or on disk.
- Origin `206` responses are never stored, merged, or spliced under any key.
- Validators (`ETag`, `Last-Modified`), `Content-Encoding`, `Vary` variant keys and membership, and RFC 9111 §3.5 `Authorization` shared-cache reuse rules apply identically to range lookups and full-object lookups.

### 3. RFC 9110 §14 & §13.1.5 range and `If-Range` semantics

When `range_enabled = true`:

1. **Method gate (RFC 9110 §14.2):**
   - `Range` is defined only for `GET`. A `HEAD` request carrying `Range` or `If-Range` ignores range slicing and follows normal `HEAD` cache lookup/bypass semantics without synthesizing a `206`.
2. **`If-Range` without `Range` (RFC 9110 §13.1.5):**
   - A request carrying `If-Range` without `Range` ignores `If-Range` and proceeds as an ordinary full-representation request.
3. **Conditional request precedence (RFC 9110 §13.2.2):**
   - `If-None-Match` and `If-Modified-Since` are evaluated before `If-Range` and `Range`. If the stored representation is not modified for the client, `304 Not Modified` is returned.
4. **`If-Range` validator evaluation (RFC 9110 §13.1.5):**
   - Multiple `If-Range` header lines or an empty value fail closed to serving the complete `200 OK` representation (`jul_cache_range_requests_total{outcome="if_range_full"}`).
   - **Entity-tag `If-Range`:** requires **strong comparison** (RFC 9110 §8.8.3.2) and valid `etagc` syntax (`opaqueETag`). Weak entity tags (`W/"..."`) or syntactically invalid tags on either the request or the stored entry never match; only identical strong quoted tags (`"..."`) satisfy `If-Range`.
   - **HTTP-date `If-Range`:** accepts all three RFC 9110 §5.6.7 HTTP-date formats (`IMF-fixdate`, `RFC 850`, and ANSI C `asctime`), requires exact timestamp equality against the stored `Last-Modified` timestamp, and requires a valid stored `Date` header at least one second later than `Last-Modified` so implicitly weak timestamps (RFC 9110 §8.8.2.2) never satisfy `If-Range` (when an origin response omits `Date`, Jul appends the received-time `Date` on capture per RFC 9110 §6.6.1).
   - When `If-Range` does not match, the complete `200 OK` representation is served from cache and counted as `outcome="if_range_full"`.
5. **Bounded `Range` parsing and multi-range disposition:**
   - Only a single `Range` header line with unit `bytes=` is eligible for cached range evaluation.
   - Non-`bytes` units (for example `items=0-5`) record `outcome="fallback"` and bypass to the origin with original request headers preserved.
   - Comma-separated range specs (`1#range-spec`) are scanned with a hard parser cap (`max(max_ranges, 16) + 1` segments) so hostile `Range` headers with thousands of commas cannot cause unbounded work; empty list elements (for example `bytes=,0-1` or `bytes=0-1, ,`) are ignored per RFC 9110 §5.6.1.2 provided at least one non-empty `range-spec` is present.
   - Syntactically invalid specs (no non-empty spec, non-digit positions, `int64` overflow, `first-pos > last-pos`, missing both positions) or spec counts exceeding `max_ranges` record `outcome="invalid"` and fall back to the origin (`X-Cache: BYPASS`) with original request headers preserved.
   - Syntactically valid multi-range requests (`len(specs) > 1` within `max_ranges`) are not synthesized into `multipart/byteranges` in cache; they record `outcome="fallback"` and bypass to the origin with original headers preserved.
6. **Single byte-range resolution against representation length `L = e.BodyLen()`:**
   - For `L == 0` (empty representation), `Range` is ignored per RFC 9110 §14.1.2 & §14.2 and the complete 0-byte `200 OK` representation is served (`outcome="hit"`), avoiding any contradiction with non-zero suffix ranges (`bytes=-1`) or unrepresentable `first-last/0` ranges.
   - For `L > 0`:
     - `first-last`: unsatisfiable if `first >= L`; otherwise `[first, min(last, L-1)]`.
     - `first-`: unsatisfiable if `first >= L`; otherwise `[first, L-1]`.
     - `-suffix`: unsatisfiable if `suffix == 0`; `[0, L-1]` if `suffix >= L`; otherwise `[L-suffix, L-1]`.
   - **Satisfiable (`206 Partial Content`):** copies stored representation headers (stripping content-scoped integrity digests `Content-Digest` and `Content-MD5` and HTTP message signatures `Signature` and `Signature-Input` while preserving `Repr-Digest`, RFC 9530 & RFC 9421), sets `Accept-Ranges: bytes`, `Content-Range: bytes start-end/L`, `Content-Length: end-start+1`, `Age`, and `X-Cache`, and streams `[start, end]` via `io.NewSectionReader(bodyReader, start, end-start+1)` in 32 KiB pooled chunks (`O(1)` heap allocation), recording `outcome="hit"`.
   - **Unsatisfiable (`416 Range Not Satisfiable`):** copies validator/cache headers, omits `Content-Encoding`, content-scoped digests (`Content-Digest`, `Content-MD5`), and HTTP message signatures (`Signature`, `Signature-Input`), sets `Content-Range: bytes */L`, `Content-Length: 0`, `Accept-Ranges: bytes`, `Age`, and `X-Cache`, and writes no body, recording `outcome="unsatisfiable"`.
7. **`Content-Encoding` representation byte fidelity:**
   - Range offsets apply to the exact stored representation bytes (`e.Body` or `e.diskBody`). Stored `Content-Encoding` is preserved untouched; the cache never decompresses a stored representation while retaining compressed headers, and Jul's outer compression middleware passes `Range` / `206` responses through without re-compressing.

### 4. Cold-miss complete-object fill and origin fallback

When a valid single-range `GET` request misses the cache (and does not carry `Cache-Control: no-store`):

- The cache records `jul_cache_range_requests_total{outcome="fill"}` and initiates a complete-object fill using an internal request clone with `Range` and `If-Range` removed.
- When `[cache].lock = true` (#525 Part 1), concurrent cold range misses (and full-object misses) for the same key coalesce on the per-key fill lock so only one origin fetch runs.
- The fill leader captures the complete origin response through `recorder` (buffering in memory up to `memory_max_size` or streaming to a temporary file in `disk_path` up to `max_entry_size` per [ADR 0023](0023-large-object-disk-streamed-cache.md)). If the origin response status/headers are uncacheable or capture is abandoned for `oversize` or `budget`, the internal fill context is canceled immediately so the cache does not download an unbounded uncacheable body.
- If the complete representation is published into the cache, the leader and any coalesced waiters serve their requested byte ranges from the newly stored entry (`X-Cache: MISS` for the fill leader, `X-Cache: HIT` for coalesced waiters and subsequent warm hits).
- If the fill fails, is uncacheable, exceeds `max_entry_size` or capture budget, or times out waiting on `lock_timeout`, the request records `jul_cache_range_requests_total{outcome="fallback"}` and forwards the client's **original request** (with `Range` and `If-Range` untouched) to the origin as `X-Cache: BYPASS`.

### 5. Freshness, revalidation, and invalidation

- **Fresh entries (`reuseHit`):** serve `206`/`416` (or `200` on `If-Range` mismatch) directly from the stored representation.
- **Stale entries within `stale_while_revalidate` (`reuseStale`):** serve the requested range from the immutable stale `*Entry` (`X-Cache: STALE`) and trigger background revalidation of the full representation.
- **Mandatory synchronous validation (`reuseValidate`):** revalidates the full representation with the origin (`304 Not Modified` or `200 OK` replacement) before slicing and serving the requested range from the validated entry (falling back to the origin with the original `Range`/`If-Range` if the replacement is uncacheable).
- **Unsafe-method invalidation:** removes the base entry and all `Vary` variants from both memory and disk tiers, immediately ending both full-response and range-serving eligibility while active readers finish streaming from their leased `diskBlob`.

### 6. Observability (`jul_cache_range_requests_total{outcome}`)

A new CounterVec `jul_cache_range_requests_total{outcome}` exports range-cache decisions with a closed six-value label set:

- `hit`: range request satisfied from a complete cached representation (`206 Partial Content` byte slice, or complete 0-byte `200 OK` when `Range` is ignored on an empty representation);
- `fill`: cold range miss initiated a complete-object origin fill;
- `fallback`: fell back to the origin with original `Range`/`If-Range` headers;
- `if_range_full`: `If-Range` mismatch served the full `200 OK` cached representation;
- `unsatisfiable`: `416 Range Not Satisfiable` served from a complete cached representation;
- `invalid`: syntactically invalid `Range` header or range count exceeding `max_ranges`.

### 7. NGINX migration disposition

- `slice` (`ngx_http_slice_module`, in `http`, `server`, `location`): classified `blocking` (`NGX_HTTP_SLICE`, `NGX_SERVER_SLICE`, `NGX_LOCATION_SLICE`). Jul implements Option A complete-object range caching, not NGINX fixed-size `slice` fragment caching.
- `proxy_cache_max_range_offset` (`http`, `server`, `location`): classified `blocking` (`NGX_HTTP_CACHE_MAX_RANGE_OFFSET`, `NGX_SERVER_CACHE_MAX_RANGE_OFFSET`, `NGX_LOCATION_CACHE_MAX_RANGE_OFFSET`). NGINX uses an offset threshold to decide whether a range miss triggers a background/full fill, whereas Jul's `[cache].range_enabled` applies uniformly up to `max_entry_size`.

## Consequences

- **Positive:** Repeatable range workloads (video/audio seeking, resumable downloads, large disk-backed artifacts) achieve 95–99% origin request and bandwidth offload after a single cold fill, while warm disk-backed range hits stream in 32 KiB chunks with `O(1)` heap allocation (`~14 KiB/op`).
- **Tradeoff accepted:** A cold range miss under Option A fetches the complete object from the origin. The feature is therefore opt-in (`range_enabled = false` by default) and bounded by `max_entry_size`, `memory_max_size`, `disk_max_size`, and `lock_timeout`.

## Related

- [ADR 0023 — Large-object disk-streamed cache and random-access body contract](0023-large-object-disk-streamed-cache.md)
- [#442](https://github.com/victornife/jul/issues/442) — Range-aware response caching
- [#525](https://github.com/victornife/jul/issues/525) — Cold-miss fill locking and disk-streamed large-object capture
- [#113](https://github.com/victornife/jul/issues/113) — Technical experiment governance
