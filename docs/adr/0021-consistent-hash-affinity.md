# ADR 0021 — Deterministic consistent-hash affinity (`rendezvous_v1`)

- Status: Accepted
- Date: 2026-09-26
- Deciders: Jul.IA maintainers
- Applies to: `internal/affinity`, `internal/upstream` selection and retry, `[upstreams.hash]`,
  HTTP/FastCGI/uWSGI/gRPC/transcoding adapters, `internal/stream` TCP/UDP selection, the NGINX
  importer, the admin API and Console
- Source: #432 (Wave 5). Builds on ADR 0016 (canonical client identity) and ADR 0017 (eligibility,
  circuit and retry rules).

## Context

Jul balanced with `round_robin`, `weighted_round_robin` and `least_conn`. None keeps a user on one
backend, so the importer approximated NGINX `ip_hash` and `hash` as round robin, which silently
breaks applications that keep session state on one instance, WebSocket reconnect locality and
shard-local caches.

The primitive needed is generic affinity: a deterministic function from a bounded request key to a
backend. It must not introduce a per-client table, a protocol parser (MQTT client IDs and the like
stay out), an expression language, or a second notion of backend health. And because the mapping is
observable — it decides which instance holds a session — it is public behaviour: it must not move
because of a restart, a reload, a reordered list or a different CPU.

## Decision

### 1. Algorithm: weighted rendezvous (HRW) hashing

Two candidates were evaluated against Jul's actual pools (typically 2–32 members, up to ~128, often
discovery-driven and filtered per request by health, circuit and capacity).

| Property | Rendezvous / HRW | Consistent-hash ring (ketama-style) |
| --- | --- | --- |
| Determinism | Pure function of key, identities, weights | Pure function, but of the ring build (vnode count, placement) |
| Minimal remapping | Exact: removing a member moves only its keys; adding one moves only keys it now wins | Approximate; depends on vnode count |
| Weights | Exact proportional (logarithmic method) | Quantized by vnode count |
| Unavailable member | Its keys spread over **all** remaining members by their own ranking | Its keys go to ring successors (hot-spot unless many vnodes) |
| Ranked fallback / retry | Natural: the next best score | Walk the ring skipping duplicates |
| Memory | None beyond one 64-bit identity hash per backend | N × V ring entries per pool **and** per generation snapshot |
| Lookup | O(N) score comparisons | O(log NV) |
| Membership update | O(1) per new backend (hash its identity) | Rebuild and sort the ring on every discovery refresh |
| Concurrency | No shared mutable state | Immutable ring swapped on update |
| Complexity | ~100 lines | Ring build, vnode policy, binary search, skip logic |

HRW wins on every property except lookup cost, and the measured O(N) cost is small at Jul's sizes
(§7). The eligible set changes per request (health, open circuits, saturated backends, retry
exclusions); HRW simply ranks what is eligible, whereas a ring must either be rebuilt per request or
walked past ineligible points, which concentrates their load on successors. HRW is chosen.

The algorithm is not shaped to mimic NGINX internals; exact NGINX placement parity is a non-goal
(§8).

### 2. The `rendezvous_v1` mapping — frozen

For key material *k* and backend identity *id* with weight *w* ≥ 1:

```text
Sum(s)        = fmix64(FNV-1a-64(s))                 // MurmurHash3 finalizer over FNV-1a
Score(k, id)  = fmix64(Sum(k) XOR Sum(id))
L(h)          = -log2((h | 1) / 2^64) in Q32 fixed point, never 0
a ranks above b  iff  w_a * L_b > w_b * L_a          // 128-bit integer compare
                 else Score_a > Score_b             // tie
                 else id_a < id_b                   // total order
```

- `L` is computed with integers only: the leading-one position gives the integer part, and a
  1025-entry table of `log2(1 + i/1024)` — itself built at start-up by exact bit-by-bit integer
  squaring — is linearly interpolated for the fraction (error < 2^-22). No `math.Log`, no floating
  point, no FMA contraction, no assembly: the result is bit-identical on every platform.
- Equal weights skip `L`: `L` is monotone non-increasing in the score and ties fall back to the
  score, so score order *is* the weighted order (tested exhaustively on random inputs).
- No seeded hash (`hash/maphash`), map iteration, pointer identity or slice position participates.
- The mapping is frozen by `internal/affinity/testdata/rendezvous_v1.json` (sums, identities,
  fixed-point logs and rankings of three backend sets over 14 keys). The same file is asserted on
  every CI platform.

**Compatibility contract.** For a given `algorithm`, the same key, backend identities and weights
map identically across restarts, reloads, reordered configuration, supported operating systems and
Jul versions. Any different mapping — a new hash, a different identity normalization, a different
tie rule — must ship as a new `algorithm` value (`rendezvous_v2`, …) and never as an edit to
`rendezvous_v1`. `[upstreams.hash] algorithm` exists now, defaulting to `rendezvous_v1`, so that
replacement never silently re-places existing sessions.

### 3. Backend identity

The hashed identity is where a backend is reached:

- TCP `host:port`: an IP literal becomes its canonical text (IPv4-mapped IPv6 unmapped, IPv6
  RFC 5952-compressed, zone kept); a hostname is lowercased without a trailing dot; the port is
  plain decimal (`08080` → `8080`). SRV- and A/AAAA-derived members follow the same rule
  (`host:port` from the record).
- Unix `unix:<path>`, byte-for-byte.
- The scheme (fixed per pool) and any provider logical ID (Kubernetes pod UID, Consul ServiceID)
  are excluded: a replacement pod at the same address keeps its keys.

Consequences: a discovery result that only reorders members remaps nothing; a backend whose
address really changes is a new identity and only its share of keys moves. Two static servers with
one canonical identity are a validation error in a `consistent_hash` pool, since they would tie for
every key; if discovery ever returns such duplicates they are the same dial target, so placement is
still order-independent.

### 4. Key sources — closed and bounded

```text
[upstreams.hash]
key = "client_ip" | "header" | "cookie"
name = "<header or cookie name>"          # required for header/cookie, rejected for client_ip
fallback = "round_robin" | "weighted_round_robin" | "least_conn"   # default round_robin
algorithm = "rendezvous_v1"               # default and only value
```

| Source | Normalization | Missing / empty | Invalid |
| --- | --- | --- | --- |
| `client_ip` | ADR 0016 canonical client (`clientaddr.Client`), IPv4-mapped unmapped, zone dropped, port ignored; hashed as canonical text | n/a (a peer always exists) | unparseable peer; identity **not attributed** (trusted proxy sent an unusable chain) |
| `header` | exactly one field line; OWS trimmed; case preserved; read from the request as forwarded | absent, empty or whitespace | more than one field line; > 256 bytes |
| `cookie` | first cookie with that name across all `Cookie` lines; surrounding `"` removed | absent or empty value | > 256 bytes |

No new identity parser: `client_ip` reuses the certified trusted-proxy model unchanged. Values are
never truncated (truncation would collide long keys sharing a prefix). Only a 64-bit sum outlives
extraction; raw keys are never metric labels, log fields or trace attributes. Extraction does not
allocate.

### 5. Missing-key semantics

A request whose key is missing or invalid is placed by `hash.fallback`, a separate ordinary balancer
with its own state. An empty string is never hashed, so keyless requests cannot pile onto one
backend. The default is `round_robin`, which is also what NGINX does with an empty hash key. The
fallback is part of the configuration, projected by the API and Console, and every keyed request's
outcome is counted by `jul_upstream_affinity_keys_total{pool,status}` with `status` ∈
`hashed|missing|invalid`, so a key that stopped arriving is visible.

### 6. Selection, health, retry, discovery, L4

```text
pool backends → health / circuit / capacity / retry-exclusion filter → eligible set
             → rendezvous ranking for the key → first → circuit admission claim
```

- Affinity owns no health state. The existing non-consuming eligibility filter runs first; a lost
  half-open claim removes that backend and re-ranks (the key's next backend).
- When the preferred backend recovers, its keys return to it: the mapping is stateless.
- A saturated backend (`max_active_per_backend`) is skipped like an ineligible one, so affinity is
  best-effort under capacity limits rather than a queue.
- Retries use the existing driver (`Pool.Do`): the key travels in `RetryRequest.Key`, and every
  attempt ranks the eligible **untried** set, which is the key's next backend. No affinity-specific
  retry loop exists and retry eligibility (ADR 0017, deferred #406) is unchanged.
- Discovery updates reuse backends by logical ID and address; weights change in place. Last-good
  semantics are unchanged, so a failed resolve keeps the mapping.
- Stream routes accept only `client_ip` (validation rejects header/cookie keys on any upstream a
  stream route names). TCP selects once at connection establishment, from the socket peer or the
  PROXY source a trusted peer asserted; UDP selects at session creation and datagrams follow the
  existing session table. The stream dial loop now excludes a backend that failed to dial for the
  rest of that connection's attempts.
- A change to any `hash.*` field rebuilds the pool on the next reload, like `strategy`
  (lifecycle `hot_reload`, subsystem `upstream`).

### 7. Measured cost (aarch64, 12 cores, full `Pool.PickKeyed` path incl. filter and accounting)

| Pool size | round_robin | least_conn | consistent_hash | consistent_hash weighted |
| --- | --- | --- | --- | --- |
| 2 | 1216 ns | 1067 ns | 1085–1125 ns | 1112 ns |
| 8 | 1301 ns | 1380 ns | 1183–1200 ns | 1236 ns |
| 32 | 1281 ns | 1409 ns | 1462–1484 ns | 1628 ns |
| 128 | 2132 ns | 2183 ns | 2891–3038 ns | 4174 ns |

The ranking alone is 5.5 / 30 / 148 / 657 ns unweighted and 8.5 / 80 / 357 / 1479 ns weighted for
2 / 8 / 32 / 128 backends, zero allocations. Every strategy shares the one existing per-pick
allocation (the eligible slice). Parallel picks at 32 backends scale like `round_robin`
(~350 ns/op across 12 cores) because HRW holds no shared mutable state. A membership update costs
the same as for `round_robin`. The O(N) cost is a small fraction of a proxied request at Jul's
realistic pool sizes, and there is no per-client state to grow.

Measured remapping on controlled sets (100 000 keys): adding one backend to 2 / 8 / 32 moved
33.47 % / 11.28 % / 3.04 % (ideal 33.33 / 11.11 / 3.03); removing one moved 49.93 % / 12.60 % /
3.09 % (ideal 50 / 12.5 / 3.13), only from the removed backend; doubling one weight moved
16.68 % / 9.62 % / 3.02 % (ideal 16.67 / 9.72 / 2.94), only onto that backend. A discovery refresh
8 → 9 moved 11.02 %; a reordered equivalent set moved 0.

### 8. NGINX migration

| NGINX | Classification | Candidate |
| --- | --- | --- |
| `ip_hash` | approximated `NGX_UPSTREAM_IP_HASH` | `consistent_hash`, `key = "client_ip"` |
| `hash $remote_addr` / `$binary_remote_addr` [`consistent`] | approximated `NGX_UPSTREAM_HASH` | `key = "client_ip"` |
| `hash $http_<name>` [`consistent`] | approximated `NGX_UPSTREAM_HASH` | `key = "header"`, `name = "<name>"` (underscores → dashes) |
| `hash $cookie_<name>` [`consistent`] | approximated `NGX_UPSTREAM_HASH` | `key = "cookie"`, `name = "<name>"` |
| `hash` with any other expression (literals, concatenation, `$request_uri`, `$arg_*`, …) | blocking `NGX_UPSTREAM_HASH_KEY` | `round_robin`; affinity not preserved |

Nothing is `supported`: the key source matches, the placement function does not. `ip_hash` hashes
the first three IPv4 octets with NGINX's own function (one /24 shares a backend) where Jul hashes the
full canonical address; `hash … consistent` uses a ketama ring and plain `hash` modular hashing
(which remaps most keys on membership change). Every key is therefore re-placed once at cutover.
The real NGINX-vs-Jul lane (`upstream-hash-affinity-runtime`) asserts stickiness in both runtimes,
equal placement where it happens to agree, and records the tenants NGINX places elsewhere as the
expected difference `NGX_UPSTREAM_HASH`.

## Consequences

- Stateful workloads get deterministic placement without shared session state, a client table or a
  protocol-specific feature.
- The mapping is a compatibility surface: `rendezvous_v1` cannot change; a better mapping is a new
  algorithm value an operator opts into.
- Affinity is best-effort by construction: health, circuit, capacity and retries may place a key
  elsewhere, and the key returns when the preferred backend is eligible again. Applications that
  cannot tolerate a move need replicated state; Jul does not claim otherwise.
- Sticky-cookie issuance, arbitrary key expressions, bounded-load hashing and MQTT/WebSocket-specific
  keys are out of scope and remain so without a new decision.

## Related

- [ADR 0016](0016-inbound-identity-and-backend-peer-trust.md) — canonical client identity.
- [ADR 0017](0017-upstream-resilience-and-overload-control.md) — eligibility, circuit and retry.
- [upstreams.md](../upstreams.md#consistent-hash-affinity) — operator reference.
- [nginx-assessment.md](../nginx-assessment.md#upstream-affinity) — migration guidance.
