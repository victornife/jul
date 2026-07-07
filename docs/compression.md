# Response compression

Jul.IA can **compress responses** on the fly using `gzip` (always available),
`brotli`, or `zstd` (build-tagged).  The best codec is negotiated with the
client via `Accept-Encoding`, and static files can be served from precompressed
sidecar files (`.gz`, `.br`) to avoid recompression cost.

This is **Y1-02**.  `gzip` is in **core**; `brotli` requires the `brotli` build
tag and `zstd` requires the `zstd` build tag.

> **Maturity:** **GA** (see [ADR 0003](adr/0003-maturity-and-ga.md)).

## Contents

- [Supported encoders](#supported-encoders)
- [Negotiation](#negotiation)
- [When compression applies](#when-compression-applies)
- [Precompressed sidecars](#precompressed-sidecars)
- [Config reference](#config-reference)
- [Benchmarks](#benchmarks)
- [Security / threat note](#security--threat-note)
- [GA status](#ga-status)

## Supported encoders

| Encoding | Build tag | Library | Levels | Notes |
| --- | --- | --- | --- | --- |
| `gzip` | — (core) | `compress/gzip` | 1–9 (default 6) | Always available; universal fallback |
| `br` | `brotli` | `github.com/andybalholm/brotli` | 0–11 (default 6) | Best compression ratio; slower than gzip |
| `zstd` | `zstd` | `github.com/klauspost/compress/zstd` | 1–4 mapped to Speed* levels | Fastest; good ratio near gzip-9 |

Configure preference order in `[compression].encoders`.  The first encoder the
client accepts (by q-value) is chosen, with server preference as the tie-break.

## Negotiation

The middleware parses `Accept-Encoding` q-values and selects the best match from
the configured `encoders`.  If the client sends no `Accept-Encoding`, or q=0 for
every configured encoder, the response passes through uncompressed.  The `Vary:
Accept-Encoding` header is always added so caches do not serve a compressed
response to a client that cannot decompress it.

### Behaviour matrix

| Scenario | Expected behaviour | Test coverage |
| --- | --- | --- |
| Client sends `Accept-Encoding: gzip` | Response compressed with gzip | ✅ `TestCompressNegotiation` |
| Client sends no `Accept-Encoding` | Pass-through, `Vary` added | ✅ `TestCompressNegotiation` |
| Client sends `Accept-Encoding: br` but build lacks `brotli` tag | Pass-through (startup would have errored) | ✅ startup validation |
| Response already has `Content-Encoding` | No double-compression | ✅ `TestCompressNoDoubleEncode` |
| Request has `Range` header | No compression (pass-through) | ✅ `TestCompressSkipsRange` |
| Response smaller than `min_size` | Pass-through | ✅ `TestCompressMinSize` |
| Content-Type not in `types` | Pass-through | ✅ `TestCompressMIMEGate` |
| SSE stream with `Flush()` | Decision forced, encoder flushed + underlying flushed | ✅ `TestCompressSSEFlush` |
| WebSocket upgrade (Hijack) | No finalisation, raw connection preserved | ✅ `TestCompressHijack` |
| Empty body (204/304) | No compression, `Vary` still added | ✅ `TestCompressEmptyResponseHasVary` |
| Encoder pool reuse | Same encoder instance reset across requests | ✅ `TestCompressEncoderReuse` |
| Unknown encoder in config | Startup error: "not compiled in this build" | ✅ `TestCompressUnknownEncoderErrors` |

## When compression applies

All gates must pass:

1. **Client accepts** — `Accept-Encoding` includes a configured coding with q > 0.
2. **Body allowed** — not 1xx, 204, or 304.
3. **Not already encoded** — `Content-Encoding` header is absent.
4. **No Range** — request does not carry a `Range` header.
5. **Above min_size** — buffered body ≥ `min_size` (default 1 KiB).
6. **MIME type matches** — response `Content-Type` matches the `types` allow-list.

The middleware buffers the first `min_size` bytes before deciding, so very small
responses are not penalised by compression overhead.

## Precompressed sidecars

The static-file handler (`root` locations) can serve pre-generated `.gz` or `.br`
files when `precompressed = true` and the client accepts the matching encoding.
This avoids on-the-fly compression CPU cost for static assets.

```toml
[compression]
enabled = true
encoders = ["br", "gzip"]
precompressed = true
```

Rules:

- Sidecar files must sit next to the original (`app.js` + `app.js.gz` + `app.js.br`).
- Only `gzip` and `br` sidecars are supported (zstd sidecars are not implemented).
- The sidecar bytes are served verbatim; they must be valid compressed data.

## Config reference

```toml
[compression]
enabled         = true
encoders        = ["zstd", "br", "gzip"]   # server-preference order
level           = 0                         # 0 = encoder default; 1–9 for gzip, 0–11 for br
min_size        = "1k"                      # smallest body to compress
types           = ["text/*", "application/json", "application/javascript", "application/xml", "application/wasm", "image/svg+xml"]
precompressed   = true
```

Validation:

- Each encoder in `encoders` must be compiled into the build (or startup fails).
- `level` is clamped to the encoder's valid range.
- `min_size` must be ≥ 0.

## Benchmarks

From `go test ./internal/middleware/ -bench=. -benchmem` on a modest VM (gzip
only; brotli/zstd tags not enabled):

| Benchmark | ops/sec | time/op | allocs/op |
| --- | --- | --- | --- |
| `CompressionPassThrough` (no Accept-Encoding) | ~150 K | ~7.3 μs | 19 |
| `CompressionGzipSmall` (~80 B JSON) | ~35 K | ~49 μs | 28 |
| `CompressionGzipLarge` (~8 KiB HTML) | ~5.5 K | ~306 μs | 31 |
| `CompressionEncoderReuse` (pool hot path) | ~30 K | ~41 μs | 28 |

**Key take-away:** pass-through is ~7 μs; small-body gzip adds ~42 μs;
large-body gzip is dominated by the compression algorithm (not the middleware
overhead).  Encoder pooling keeps allocation count flat.

## Security / threat note

| Threat | Risk | Mitigation |
| --- | --- | --- |
| **BREACH attack** | Compression + reflection of a secret in the response body can leak the secret byte-by-byte via response-size side channel | Jul.IA has no built-in BREACH mitigation.  Defences: disable compression for pages that echo user input mixed with secrets; use CSRF tokens that rotate per request; deploy TLS with no compression at the record layer |
| **CRIME attack** | Historic TLS-level compression attack | TLS compression is disabled by default in Go's TLS stack; not applicable to HTTP response compression |
| **Compression bomb (zip bomb)** | Attacker sends a tiny request that triggers a huge compression ratio, exhausting memory | Min-size gate prevents tiny triggers; the encoder operates on a streaming basis (bounded memory).  No defence against intentionally-decompressable huge payloads — that is an upstream / application concern |
| **Cache poisoning via Vary** | Incorrect `Vary` handling causes a compressed response to be served to a non-compressible client | `Vary: Accept-Encoding` is set on **every** response, including pass-through and empty-body |
| **Sidecar data leak** | `app.js.gz` contains stale/dev data and is served in production | Sidecars are opt-in (`precompressed = true`); operators control what files exist on disk |
| **CPU exhaustion** | Many unique URLs with large bodies cause continuous recompression | Precompressed sidecars eliminate CPU for static assets; dynamic responses are the application domain |

## GA status

Per [ADR 0003](adr/0003-maturity-and-ga.md), compression is **GA**:
the soak test (criterion 5) was completed on 2026-07-04.

| # | GA criterion | Status |
| --- | --- | --- |
| 1 | Behaviour matrix published | ✅ [Supported encoders + Behaviour matrix](#supported-encoders) |
| 2 | Published benchmark numbers | ✅ [Benchmarks](#benchmarks) |
| 3 | Documented known-limitations | ✅ brotli/zstd build-tagged; zstd sidecars not supported; no BREACH mitigation |
| 4 | Stable config/API contract (semver-guarded) | ✅ `CompressionConfig` frozen under [compatibility policy](compatibility.md) |
| 5 | Long-running soak test passed | ✅ soaked 1h windows 2026-07-04 (11.6M req, 0% err) — [evidence](soak-evidence.md#2026-07-04--compression-soak-local-windows-1-hour-50-workers) |
| 6 | Runnable example + docs | ✅ [testdata/compression.toml](../testdata/compression.toml) + this doc |
| 7 | Security / threat note | ✅ [Security / threat note](#security--threat-note) |
| 8 | Fuzzing where parsing is involved | n/a — uses go-toml parser (Y1-08); q-value parser is covered by unit tests |
| 9 | Self-explanatory Console surface | ✅ Console **Status** panel shows compression enabled + active encoders |
