# Response cache

Jul.IA includes an opt-in response cache for proxied backends and FastCGI/uWSGI
applications. It has an in-memory tier and an optional on-disk tier.

> [!WARNING]
> **Current correctness programme:** the cache is under active recertification in
> #107 and #131-#134. Confirmed work covers background revalidation lifetime,
> shared mutable entries, shared-cache directives, authenticated reuse, unsafe
> method invalidation, `304` metadata, Range bypass and protocol-transparent
> response wrapping. The historical GA/conformance matrix is not current release
> evidence while that programme is open.

Use the cache conservatively until the corrected implementation is released:

- do not enable it on WebSocket or other upgrade routes;
- prefer `private` or `no-store` for user-specific/authenticated responses;
- avoid relying on complete RFC shared-cache semantics not covered by an
  executable test in the recertification matrix;
- test reload and stale-revalidation behavior under your workload;
- use a dedicated, access-controlled directory for the disk tier.

## Configuration

```toml
[cache]
enabled                = true
memory_max_size        = "256MB"
disk_path              = "/var/cache/jul/http"
disk_max_size          = "4GB"
default_ttl            = "5m"
stale_while_revalidate = "30s"
stale_if_error         = "300s"
```

Each location must opt in:

```toml
[[servers.locations]]
match = { type = "prefix", path = "/static/" }
cache = true
```

## Storage model

| Tier | Backing store | Bound | Lifetime |
| --- | --- | --- | --- |
| Memory | in-process cache | `memory_max_size` | process lifetime; current instance survives ordinary handler reloads |
| Disk | files below `disk_path` | `disk_max_size` | survives restart when the same path is reused |

Memory is consulted first. The disk tier is an optional slower backstop. Disk
write failure is best-effort and should not fail the client response.

Only use a directory dedicated to Jul.IA cache data. Cache-owned files may be
evicted and deleted as capacity is enforced. Foreign-file safety and pre-Publish
filesystem behavior are included in the later backend-generation review for #93
if that gated work is selected.

## Current behavior that remains useful

The current cache implementation includes:

- per-location opt-in and a global enable switch;
- memory and optional disk storage;
- host, method and request-target participation in the key;
- representation variants based on `Vary`;
- freshness from response directives and a configured fallback TTL;
- stale-while-revalidate and stale-if-error concepts;
- conditional response support for stored validators;
- exact-key and full-cache administration;
- bounded memory/disk capacity and LRU-style eviction.

These capabilities do not imply that every interaction is currently complete or
race-free. The final supported matrix is owned by #134 after source re-audit and
real-protocol evidence.

## Corrected target contract

The cache correctness programme is implementing the following non-negotiable
contract.

### Generation and concurrency

- background revalidation is owned by the handler/cache generation whose
  upstreams, plugins or transports it uses;
- reload/shutdown cancels or drains that work before retirement;
- published entries are immutable;
- stale/error metadata is replaced atomically rather than mutated in place;
- one request uses one coherent cache generation and policy snapshot;
- repeated reload/revalidation is race- and leak-free.

### Shared-cache semantics

- request and response `no-cache` force successful synchronous validation before
  reuse;
- `must-revalidate` and `proxy-revalidate` prohibit serving unvalidated stale
  content outside their allowed contract;
- authenticated reuse follows explicit shared-cache authorization rules;
- successful unsafe methods invalidate affected stored representations;
- a `304 Not Modified` merges allowed metadata and recomputes freshness without
  corrupting the stored response;
- malformed directives fail safely/conservatively.

### Range behavior

The first corrected contract deliberately bypasses cache for requests containing
`Range` or `If-Range`:

- forward the request unchanged to the origin;
- do not substitute a cached full response;
- do not store `206 Partial Content`;
- report a bounded cache-bypass result.

Cached byte-range serving is a separate future enhancement, not part of current
recertification.

### Protocol transparency

- WebSocket/`101 Switching Protocols` bypasses cache and is never stored;
- response wrappers preserve only the optional interfaces supported by the
  underlying writer;
- `Flusher`, `Hijacker`, `Pusher`, `ReaderFrom` and `ResponseController`
  behavior is tested explicitly;
- SSE flushes promptly;
- normal response status, headers and byte accounting remain correct.

## Current limitations

1. **Open correctness findings.** See #107 and children before treating the
   feature as GA/conformant.
2. **No tag/prefix/host purge.** Purge is exact-key or full cache only.
3. **No distributed cache.** Each process warms independently.
4. **Orphaned variants can persist.** A changed `Vary` policy can leave old
   variants until expiry or eviction.
5. **Oversized entries are not stored.** The client can still receive the
   response.
6. **Disk persistence is not a durability guarantee.** The cache is disposable
   acceleration state, not an authoritative database.
7. **Cache configuration remains startup/restart-bound except where a later
   selected issue proves the exact transition.** #92 is blocked until
   recertification; #93 is gated.

## Security guidance

- never rely on cache as an authorization boundary;
- set `private`/`no-store` for user-specific data;
- audit `Authorization`, cookies, `Set-Cookie`, `Vary` and validators before
  enabling a route;
- keep disk cache on an encrypted/restricted volume when responses can contain
  sensitive data;
- prevent direct untrusted access to backends that trust headers inserted by the
  proxy;
- monitor upstream and cache-result metrics without adding raw host/path/user
  values as labels;
- purge after a security-policy change when old representations may be unsafe.

## Operations

Administration can remove an exact key or purge the current cache. A later
cache-manager/backend-generation issue may change how enablement and disk-path
replacement are owned; it must not migrate or delete an old path implicitly.

For current reload behavior, see [reload-semantics.md](reload-semantics.md). For
the source-backed correction register, see
[current-product-truth.md](current-product-truth.md).

## Validation and maturity closure

#134 must record the final executable matrix and actual commands, including:

- targeted cache tests;
- `go test -race` with repeated stale hits and reload;
- shutdown/reload with blocked background revalidation;
- authenticated/directive/invalidation/`304`/Range cases;
- real WebSocket and SSE through a cache-enabled proxy location;
- H1/H2 behavior;
- disk and administration behavior;
- a focused traffic/reload soak and resource trend;
- updated benchmarks, examples, status and known limitations.

Only that evidence can restore or revise the cache maturity claim.
