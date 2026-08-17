# ADR 0017 — Upstream resilience and overload control

- **Status:** Accepted — amended 2026-08-17 (explicit circuit state machine)
- **Date:** 2026-08-13
- **Deciders:** Jul.IA maintainer
- **Applies to:** upstream pools and backends, HTTP reverse proxy, native gRPC passthrough, gRPC
  transcoding, FastCGI and uWSGI, L4 TCP/UDP stream proxy, forward-auth and JWKS, passive and active
  health checking, service discovery, configuration reload, upstream metrics, runtime API and Console
- **Source:** #116 (`[ADR][CGC-03]`), source-level design review of `66c71b2d` and current `main`
- **Related:** [ADR 0016](0016-inbound-identity-and-backend-peer-trust.md) (no TLS downgrade,
  generation-owned transports), [ADR 0011](0011-reload-plan.md) (reload transaction),
  [ADR 0014](0014-operability-surfaces.md) (operability surfaces),
  [ADR 0009](0009-two-tier-editing.md) (Console editing tiers),
  [ADR 0013](0013-project-operating-model-and-completeness.md) (portfolio entry)

## Context

Jul load-balances, health-checks, discovers and retries across backends. It does not bound how much
work it will accept on their behalf. There is no equivalent of `max_active_requests`;
`MaxConnsPerHost` is set nowhere in the repository; retries have neither budget, backoff nor overall
deadline; and the failure an operator sees is a bare `502` or `503` with no vocabulary separating
*the backend is broken* from *Jul is protecting itself*.

Three facts about the existing code shaped this record more than the issue text did.

**Jul already has a circuit breaker.** `Pool.MarkFailure` trips a backend after `max_fails`
consecutive failures and holds it out for `fail_timeout`; `Backend.available` readmits it afterwards,
and its own comment describes that state as half-open because the next failure re-trips it. That is
`CLOSED → OPEN → HALF_OPEN → CLOSED` under a different name. Shipping a second breaker beside it would
give operators two overlapping verdicts about one backend and no way to explain their interaction.

**Jul already has generation ownership with drain-and-retire.** `handlerGen` in
`internal/server/server.go` counts in-flight requests, closes a `drained` channel, and only then runs
`retire`, so no generation's resources are closed while a request may still be using them. Requests
capture an immutable `upstream.SnapshotMap` from their context. Explicit generation tags on resilience
counters would re-solve a solved problem, less safely.

**Passive-health tuning is currently pool identity.** `upstreamMeta` in `internal/upstream/registry.go`
includes `maxFails` and `failTimeout`, so changing either rebuilds the pool, restarts its health
checker and discards every backend's accumulated state. Extending that pattern to resilience limits
would make every limit tweak a silent reset of every breaker in the pool.

Without an explicit model, AI/provider routing (#113) grows a second resilience subsystem, and the
`[DRAFT]` children of #110 cannot be activated.

## Existing architecture

| Concern | Where | Behavior before this record |
| --- | --- | --- |
| Backend state | `internal/upstream/backend.go` | `inflight`, `fails`, `downUntil`, `activeHealthy`, all atomic; `available()` combines the active verdict with passive cooldown |
| Passive health | `internal/upstream/pool.go` `MarkFailure` / `MarkSuccess` | `max_fails` (default 1) consecutive failures place the backend in cooldown for `fail_timeout` (default 10s) |
| Active health | `internal/upstream/health.go` | One goroutine per pool; probe type defaults to `http`; on recovery it also clears passive state via `MarkSuccess` |
| Backend reuse | `Pool.UpdateBackends` | Reuses `*Backend` keyed by `(address, weight)`, preserving in-flight and passive state |
| Selection | `internal/upstream/balancer.go` | `round_robin`, `least_conn` (reads `inflight`), `weighted_round_robin` (reads `Weight` under its own mutex) |
| Snapshots | `internal/upstream/snapshot.go` | Static freeze or live delegation, captured per request from context |
| Pool lifecycle | `internal/upstream/registry.go` | Process-lifetime registry; `Begin`/`For`/`Commit`/`Activate`; reuse gated by `upstreamMeta` |
| HTTP retry | `internal/handler/proxy.go` `balancingTransport.RoundTrip` | Transport errors only; `GET`/`HEAD`/`OPTIONS`/`TRACE`/`PUT`/`DELETE`; requires `GetBody`; excludes tried backends; no backoff, deadline or budget |
| Direct targets | `resolvePool` | Unregistered pool-of-one, hardcoded `MaxFails: 3`, rebuilt every generation |
| HTTP transport | `newProxyTransport` | `MaxIdleConns: 100`, `MaxIdleConnsPerHost: 32`, **no `MaxConnsPerHost`** |
| Native gRPC | `internal/handler/grpcproxy.go` | Single attempt, never retried, by explicit design |
| Transcoding | `internal/transcode/invoke.go` | `ClientConn` cached by bare address, 30s retired grace, 30s eviction reconciler; the `Transcoder` is generation-staged and closes its connections on retirement |
| FastCGI / uWSGI | `internal/handler/fastcgi.go` | Single socket address, no pool, no load balancing, no health, **no bound on concurrent connections** |
| L4 TCP | `internal/stream/tcp.go`, `internal/stream/server.go` | Uses `upstream.Pool`, drives `MarkFailure`/`MarkSuccess` in `dialBackend`, **no connection cap** |
| L4 UDP | `internal/stream/udp.go` | Uses `upstream.Pool`; **already has `max_udp_sessions`** (default 10000) with LRU eviction and a rejection metric |
| Forward-auth / JWKS | `internal/auth/forward.go`, `internal/auth/auth.go` | Per-request outbound dependency with a hardcoded 10s timeout, no breaker, no retry, no pool |
| Error status | `proxyErrorStatus` | `ErrNoAvailableBackend` → 503, deadline/cancel/net-timeout → 504, else 502 |

## Constraints and accepted prior decisions

1. **D11 order** (#116): limits → retry budget, deadline and backoff → deterministic breaker →
   outlier ejection only on evidence.
2. **Compatibility-preserving defaults.** A configuration that does not mention resilience behaves as
   it does today.
3. **One generic subsystem** shared by HTTP, gRPC, FastCGI, uWSGI, L4 and any future provider
   transport. No AI-specific resilience engine.
4. **Deterministic TLS identity failures are non-retryable** (#115, #138, #139).
5. **No retry may downgrade HTTPS/TLS to plaintext, and there is no automatic plaintext gRPC
   fallback** ([ADR 0016](0016-inbound-identity-and-backend-peer-trust.md)).
6. **Bounded metric dimensions.** No metric may be labelled by backend address, route path, error
   string or any other unbounded value.
7. **Outlier ejection requires an objective go/no-go gate.**
8. **Transports and gRPC `ClientConn`s are generation-owned** (ADR 0016, `internal/app/generation.go`).
9. **Every configuration field is classified exactly once** in the closed-world lifecycle registry
   (#89). No unknown path may default to hot.

## Decision

### 1. One resilience policy, resolved once, under `[upstreams.resilience]`

`[upstreams.resilience]` is the resilience surface. There is no global defaults block: a third
precedence level adds permanent complexity for no demonstrated need, and omission already means "the
built-in default", which is today's behavior.

`proxy_pass`, `fastcgi_pass`, `uwsgi_pass` and `grpc_transcode.target` all accept either a named
upstream or a literal target. A literal target normalizes to a **pool-of-one that stays
generation-local and unregistered**, as it does today. Registering it would invent a public identity
for something that has never had one and would attach health checkers and discovery refreshers it does
not need. The consequence is stated rather than hidden: **a literal target's counters and breaker
reset on every reload.** Operators who need continuity across reload name an upstream — that is now a
documented reason to promote a target.

Public configuration is resolved once, at load time, into an immutable `resilience.Policy`, exactly as
`backendtls.Options` resolves into `backendtls.Policy`. The live `Pool` holds it in an
`atomic.Pointer[resilience.Policy]`. The request path performs one pointer load and reads pre-parsed
scalars: no tree traversal, no inheritance merge, no duration parsing, no allocation.

**`resilience.Policy` does not participate in `upstreamMeta`, and `maxFails`/`failTimeout` are removed
from it.** Policy changes swap a pointer; they never rebuild a pool.

### 2. Scope rule — a control is location-overridable if and only if it owns no shared state

Stateful controls are valid wherever the state has a single unambiguous owner:

- For `proxy_pass` and `grpc_transcode` the owner is the **pool**, so stateful controls belong under
  `[[upstreams]]`. Setting one in a location is a **validation error**, never a silent ignore.
- For `fastcgi_pass` and `uwsgi_pass` pointing at a literal socket there is no shared pool; the
  location's handler is the owner, so stateful controls are valid in
  `[servers.locations.resilience]` and only there.

Stateless controls — the retry shape, plus `max_connections_per_backend` because transports are
already built per location by `newProxyTransport(loc, policy)` — may be set at either level, with the
location winning. This preserves the existing location-level `proxy_retries` rather than creating an
unexplainable exception for it.

### 3. Admission is counters plus limits, never a rebuilt primitive

Per admission owner:

```text
active  atomic.Int64   // admitted logical requests, streams and connections
pending atomic.Int64   // parked requests
mu      sync.Mutex     // guards the waiter FIFO only
waiters []*waiter      // bounded by max_pending_requests; each holds chan struct{} cap 1
```

- **Fast path:** a CAS loop on `active` while `active < limit`. No lock is taken below the limit.
- **Slow path:** under `mu`, reject with `proxy_overloaded` if the FIFO is full, otherwise append a
  waiter and `select` on the waiter channel, the request context, and `pending_timeout`.
- **Release with direct handoff:** if a waiter exists and `active-1 < limit`, hand the slot to the FIFO
  head without decrementing. This gives strict FIFO, prevents barging, and makes recovery after a limit
  decrease monotonic.
- **No goroutine is ever created.** The parked goroutine is the inbound `net/http` goroutine that
  already exists.

`Admit` returns a `release` closure guarded by `sync.Once`, capturing the exact counter objects it
incremented.

A counting channel, `x/sync/semaphore` and `sync.Cond` are all rejected: the first two cannot be
resized on reload or cannot bound the waiter list, and the third cannot honor request cancellation.

**Admission is innermost.** It lives in `proxyHandler.ServeHTTP` (a new method; the type currently
promotes `ServeHTTP` from the embedded `*httputil.ReverseProxy`), not in `RoundTrip`, because
`RoundTrip` contains the retry loop and admitting there would count one admission per *attempt*.
Consequences, all intended: a cache hit never consumes admission; a WAF-blocked or unauthenticated
request never consumes admission; and under sustained overload Jul pays full WAF and authentication
cost for requests it then rejects. Accounting correctness outranks CPU savings on the rejection path.

**Per-backend concurrency is a selection filter, not a second queue.** A backend at
`max_active_per_backend` is not eligible in `pickExcluding`. This reuses `Backend.inflight`, composes
with `available()`, and makes nested waiting — and with it deadlock and cross-backend head-of-line
blocking — structurally impossible.

**Waiting uses the request context plus at most one `time.Timer` per queued request.** A timing wheel
is rejected: live timers are bounded by `max_pending_requests` per owner.

### 4. Physical connections are bounded per backend host, per transport

`max_connections_per_backend` maps to `http.Transport.MaxConnsPerHost`, the only lever Go offers that
bounds sockets without defeating connection pooling and that honors the request context while queueing
for a dial. It is **not applicable to native gRPC or transcoding**, where one HTTP/2 connection carries
all streams; validation warns when it is set on such a route. Health-check clients are exempt, so a
saturated pool can still observe recovery. Idle connections count toward the limit until
`IdleConnTimeout`.

The name is deliberate. `max_connections` would be read as pool-wide, which Go cannot enforce, and
`[rate_limit] max_conns` already denotes an **inbound** per-listener cap. Two similar names pointing in
opposite directions is the trap ADR 0016 avoided by keeping `tls` and `backend_tls` distinct.

### 5. Retry gains a deadline, bounded backoff and a budget, and nothing else

Eligibility, all of which must hold: attempts remain; the failure is a transport-level error; no
response byte has been delivered; the body is replayable (`Body == nil || GetBody != nil`); the error
is not a deterministic TLS identity failure; the request is not cancelled; enough of the overall
deadline remains for an attempt; the retry budget allows it; an untried eligible backend exists.

- **The retry boundary is the point before any byte reaches the client**, and it exists in every
  protocol Jul retries: `balancingTransport.RoundTrip`'s return for HTTP, `gofast.Client.Do` versus
  `ResponsePipe.WriteTo` for FastCGI, and everything before `writeCGIResponse` for uWSGI.
- **No status-code retries.** Today Jul retries only transport errors; retrying 5xx would change every
  existing deployment and would double load on a backend that is deliberately shedding.
- **POST and PATCH are not retried.** A connection-level error does not prove the request was not
  processed; a backend may accept, commit and die before responding. `GetBody != nil` proves
  replayability, not safety.
- **gRPC:** native unary and all native streaming calls are not retried, because Jul proxies native
  gRPC as opaque HTTP/2 and cannot frame messages. **Transcoded unary calls are retried**, gated by
  `isIdempotent(route.httpMethod) || method.Options().GetIdempotencyLevel() ∈ {NO_SIDE_EFFECTS,
  IDEMPOTENT}` — both signals are already carried by `internal/transcode/descriptors.go`, and the
  request is an in-memory `dynamicpb.Message`. Transcoded streaming is not retried: framing has already
  been written.
- **The overall deadline dominates.** Every attempt and every backoff sleep runs under a context
  derived from `min(request deadline, start + retry_deadline)`.
- **Backoff** is exponential with a fixed ×2 multiplier and **full jitter**, clamped by the remaining
  deadline. If the clamp leaves no room for an attempt, the loop stops rather than sleeping.
- **`Retry-After` is not honored** in this tranche, because no retried failure currently carries
  headers. When status-code retries land it becomes a lower bound on backoff, hard-clamped by the
  overall deadline.

**Retry budget: sliding two-window ratio with a free floor.** Over a trailing window, retries are
permitted while

```text
retries < floor(primaries * retry_budget_percent / 100) + min_free_retries
```

consumed by CAS so the bound is exact under concurrency. Primaries accrue automatically on first
attempts, so no separate success signal is needed. The window counters live on the pool and are **not
reset by a policy swap**, which is what prevents a reload from granting a fresh retry burst.

This bounds upstream load at `(1 + p/100) × client load + min_free_retries / window` — 1.1× at
`p = 10`, against up to 3× for an unbudgeted `retry_attempts = 3`.

Because `retry_attempts` is location-overridable while the budget is pool-scoped, **two locations
sharing a pool share one budget window**: a location configured to retry aggressively can consume the
allowance of a conservative one. This is correct — the budget protects the shared backend — and it is
stated because it is surprising.

### 6. The circuit breaker absorbs passive health; it does not sit beside it

`max_fails` **is** the failure threshold and `fail_timeout` **is** the open duration. They keep their
names — they are NGINX-familiar and the importer depends on that familiarity — and no
`circuit_failures`/`circuit_open_for` aliases are introduced. One name per concept. They move into
`[upstreams.resilience]` so the block is the whole surface.

What is added is what the existing mechanism lacks: a real **HALF_OPEN** state with a bounded probe
allowance. Today every concurrent request sees `available() == true` the instant `downUntil` elapses, so a
recovering backend takes the full production load.

The circuit is an explicit three-state machine — `CLOSED`, `OPEN`, `HALF_OPEN` — whose transitions are
guarded by a per-backend mutex. Transitions only execute when the backend is already failing, so the lock
is never on the healthy path:

```go
type circuit struct {
    // Read without mu on the healthy path.
    closed atomic.Bool          // true iff state == CLOSED
    fails  atomic.Int32         // consecutive failures while CLOSED

    // Compound state; every transition is serialized by mu.
    mu             sync.Mutex
    state          state        // closed | open | halfOpen
    openUntil      time.Time
    halfOpenUntil  time.Time    // bounds HALF_OPEN itself
    probesInFlight int
    epoch          int64        // incremented on every transition
}
```

Admission, under `mu` and only when `closed.Load()` is false:

| State | Condition | Result |
| --- | --- | --- |
| `CLOSED` | — | admit normally |
| `OPEN` | `now < openUntil` | reject `circuit_open` |
| `OPEN` | `now >= openUntil` | → `HALF_OPEN`, `probesInFlight = 1`, set `halfOpenUntil`, admit as probe |
| `HALF_OPEN` | `now >= halfOpenUntil` | → `OPEN` with a fresh window, reject |
| `HALF_OPEN` | `maxProbes > 0 && probesInFlight >= maxProbes` | reject `circuit_open` |
| `HALF_OPEN` | otherwise | `probesInFlight++`, admit as probe |

Two elements are load-bearing and easy to omit:

- **`halfOpenUntil` bounds HALF_OPEN itself.** A probe may legitimately be a multi-hour gRPC stream or a
  WebSocket. Without it, one outstanding probe pins the state indefinitely — never closing, never
  re-opening. On expiry the circuit returns to `OPEN` regardless of probes still in flight.
- **`epoch` invalidates stale results.** Admission returns the epoch; a result whose epoch no longer
  matches is ignored, so a late probe cannot close a circuit that has since re-opened.

`circuit_half_open_probes` is a genuine allowance. Normatively: **given at least N eligible contenders
during a HALF_OPEN window, exactly `circuit_half_open_probes = N` probes are admitted concurrently; further
requests are rejected until a probe completes or the state changes.** "Exactly N" is conditioned on there
being N contenders — with fewer, fewer are admitted, which is not under-admission but absence of demand.

Crucially, **any** request arriving during the window may take a free slot; a request does not have to have
been racing at the instant of expiry. `0` means unbounded, consistent with `0 = unlimited` elsewhere in the
block.

**`fails` is atomic; the compound state is not.** CLOSED-state failure accounting and the success fast path
read and write `fails` without `mu`, while every state *transition* is serialized by `mu`. When a failure
count reaches the threshold, the failing goroutine takes `mu`, **re-checks the state and the threshold under
the lock**, and only then transitions — the count alone never authorises a transition. This matches the
representation the runtime already uses: `Backend.fails` is `atomic.Int32` today.

The healthy path stays cheap and is still better than today. `MarkSuccess` currently performs two
unconditional stores on *every* successful request; the two atomic reads below reduce the common case to
**`closed.Load()` and `fails.Load()`, and no store**:

```go
if ok && !isProbe && c.closed.Load() && c.fails.Load() == 0 {
    return // closed, healthy, nothing to record
}
```

One consequence worth stating so it is not "fixed" later: `closed` is a hint published under `mu`, so a
reader can observe a stale `true` for the instant between a trip and the store. At most one additional
ordinary request may therefore be admitted to a backend that has just tripped. That is benign and
self-correcting, and it **does not weaken the probe guarantee** — probe admission is decided under `mu`, so
the half-open allowance remains exact.

> **Amendment 1, 2026-08-17.** This section originally specified packing `fails`, `downUntil`, the open
> flag and the probe count into one `atomic.Int64`, with a `1 / 31 / 32` bit layout. That was
> **arithmetically impossible**: the three named fields consume all 64 bits, leaving nothing for
> `downUntil`, which the same pseudocode then read.
>
> **Amendment 2, 2026-08-17.** The two-atomic replacement (`downUntil` CAS plus a published
> `probeSlots`) was *also* wrong, in a subtler way. Because the winner of the CAS re-armed `downUntil`
> to a future deadline, every request arriving afterwards took the `now <= downUntil` branch and was
> rejected as `OPEN` — so the published slots were reachable only by goroutines that had loaded the
> stale deadline *before* the CAS landed. `circuit_half_open_probes > 1` therefore degenerated into
> "however many goroutines happened to race at the instant of expiry", which is scheduler-dependent
> rather than an allowance. The same defect made `= 0` yield exactly one probe rather than the
> documented unbounded behaviour, and it forced the ADR to permit under-admission while the validation
> section demanded a test asserting *exactly* N.
>
> The root cause of both amendments was the same: reaching for a lock-free representation on a path that
> is, by definition, only executed when a backend is already failing. The record now specifies an
> explicit state machine under a mutex, and the "may under-admit" allowance is withdrawn — with an
> explicit `HALF_OPEN` state there is no publication race, so **exactly N** is both the contract and the
> assertion. Packing and the two-atomic variant are recorded under Alternatives considered.
> **Correct transition semantics outrank atomic cleverness.**

`circuit_half_open_probes` **defaults to 1**, not to today's unbounded behavior. An unbounded recovery
burst is bug-shaped rather than a feature, and there is no adoption to preserve it for.

Thresholds are **consecutive failures, per backend**, never per pool. A rolling window needs a window
size, a minimum-sample threshold and a low-traffic policy — three knobs and an ambiguity #116 itself
names — for a benefit no evidence yet demands.

**This breaker is protocol-generic and is shared by the L4 stream proxy**, which already drives
`MarkFailure`/`MarkSuccess` from TCP and UDP dial outcomes and already honors `available()` at
selection.

**Eligibility precedence**, with the first failing gate producing the reason:

1. administrative availability
2. discovery presence
3. active health
4. circuit state
5. admission (per-backend capacity)

A **transition** of an active probe from unhealthy to healthy closes the circuit, preserving today's
behavior: an out-of-band prover of liveness outranks stale traffic failures. A steady-state probe
success does **not**, so a backend that answers `/healthz` while failing real traffic cannot keep its
breaker perpetually reset. A failing probe never touches the circuit; it sets `activeHealthy`, which
already dominates at level 3.

Operators see one state per backend: `available`, `circuit_open`, `circuit_half_open`,
`health_unhealthy`, `at_capacity`. "Passively down" ceases to exist as a separate concept.

### 7. State ownership makes incorrect accounting unrepresentable

There is **one** counter object per admission owner and **one** circuit word per backend identity. A
request's `release` closure decrements the object it incremented. A request from an older generation
therefore cannot touch a newer generation's accounting, because there is no newer object — the pool was
reused. If the pool *was* rebuilt, the old request holds the old object, which is collected when the
last old request finishes. **No generation tags, no reconciliation, no defensive checks.**

Reducing `max_active_requests` from 1000 to 100 with 500 requests active: the policy pointer is
swapped; `active` remains 500 on the same object; the 500 run to completion, because an admission limit
is an entry control and turning a configuration edit into an outage is not acceptable; new arrivals are
queued or rejected; and the direct-handoff rule releases a slot to a waiter only when
`active-1 < limit`, so recovery below the new limit is monotonic.

**Forced generation retirement must wake and reject parked waiters.** A parked request holds a
`handlerGen` in-flight reference, so a generation cannot retire *gracefully* while waiters exist — but
retirement is also bounded by a forced grace timeout, after which the generation's transport is closed.
Without an explicit wakeup a parked request could be admitted onto a closed transport. Waiters
belonging to a retiring generation are therefore woken and rejected with `upstream_unavailable`, and
`pending_timeout` is validated against the retirement grace so the ordinary path never reaches the
forced one.

### 8. Resilience state is local to the process

Local counters and breakers protect *this* Jul process and *this* process's view of a backend. They do
not enforce a cluster-wide quota and are not claimed to. Scaling from N to M replicas moves the
effective cluster ceiling from `N × limit` to `M × limit`; **every limit therefore means "per
replica"**, and the documentation says so. Operators sizing against a backend capacity `C` set
`limit = C/M`.

No Redis, lease, CRD or consensus is introduced. The purpose of these limits is process self-protection
and blast-radius reduction, both inherently per-process; global exactness would place a network round
trip and a new failure domain on the request hot path in order to make a *safety bound* precise.

The seam, if cluster-wide coordination ever becomes objectively necessary — a named vision-horizon lane
in [the roadmap](../roadmap/README.md) — is a `Limiter` interface taking a
`Scope{Pool, Backend, Fairness}`. `Fairness` exists from day one and is always empty, so partitioning
admission by tenant later is a map-key change rather than a data-plane rewrite. The pending queue is an
owned FIFO rather than a runtime primitive for the same reason: it can become per-key sub-FIFOs behind
an unchanged `Admit` contract.

### 9. Every protocol is first-class where the concept applies

| Path | Admission | Retry | Breaker | Connection limit |
| --- | --- | --- | --- | --- |
| HTTP/1.1 | yes | yes | yes | yes |
| HTTP/2 | yes | yes | yes | rarely binds (one socket, many streams) |
| HTTP/3 | yes | yes | yes | not applicable |
| h2c | yes | yes | yes | yes |
| WebSocket / 101 upgrade | yes | no, by protocol | yes | yes |
| Server-sent events | yes | no after the first byte | yes | yes |
| Native gRPC (unary and streaming) | yes | no — opaque HTTP/2, cannot frame messages | yes | not applicable |
| gRPC transcoding, unary | yes | yes, idempotency-gated | yes | not applicable |
| gRPC transcoding, streaming | yes | no — framing already written | yes | not applicable |
| FastCGI | yes | yes | yes | yes |
| uWSGI | yes | yes | yes | yes |
| L4 TCP | yes, new | not applicable | yes | yes |
| L4 UDP | yes, **existing `max_udp_sessions`** | not applicable | yes | not applicable |
| Static, redirect, return, deny, handler plugin | not applicable — no upstream | | | |

**HTTP/3 requires no protocol-specific resilience code.** Jul's HTTP/3 support is inbound only:
`internal/server/http3.go` runs a quic-go `http3.Server` over a `quic.EarlyListener`, and there is no
HTTP/3 backend transport in the data plane. HTTP/3 requests are served by the **same handler tree** as
HTTP/1.1 and HTTP/2, through the same `acquireGen()` and `upstream.WithSnapshot(ctx, g.snapshots)` path,
so admission, per-backend filtering, retry, the breaker and the reason taxonomy apply unchanged.
Inbound QUIC connections are counted by the listener's `onConn` hook and are a listener concern.
`h3Conn.Close(ctx)` performs GOAWAY and drain bounded by `shutdown_timeout`, so a long-lived HTTP/3
stream holds its admission slot and its `handlerGen` reference exactly as an HTTP/2 stream does.
WebSocket over HTTP/3 is not applicable: HTTP/3 uses extended CONNECT, which Jul does not implement.

**FastCGI and uWSGI become full pool members.** They are currently the least protected path in the data
plane: `gofast.NewClientPool(factory, 0, 30s)` retains no clients and uWSGI dials per request, so
concurrent backend connections are unbounded. PHP-FPM's fixed `pm.max_children` makes bounding
concurrency *more* valuable there than for a typical HTTP backend, and multi-backend FPM pools are
standard NGINX. This requires one model extension: `Backend` and `BackendIdentity` gain a
`Network` field (`tcp` or `unix`), `newBackend` derives it from the address form, `Backend.Identity()`
reads a stored scheme instead of `URL.Scheme`, and `probeTCP` dials `b.Network`. `probeHTTP` is
unavailable for unix backends, so `health_check.type = "http"` on a unix-socket upstream is a validation
error. The extension also opens `proxy_pass` to unix sockets later at no additional cost.

**L4 UDP keeps `max_udp_sessions` and gains no second mechanism.** `admitUDPLocked` already enforces a
per-listener session cap with LRU eviction of idle victims, correct `pool.Release` on eviction,
per-client singleflight dialing, a rejection metric and soak coverage; session lifetime is already
defined as created on first datagram, reaped after `idle_timeout` with no traffic in either direction,
or evicted at the cap. Adding `max_active_requests` for UDP would create exactly the overlapping
mechanism this record rejects for the breaker. `max_active_requests` is therefore a validation error on
a UDP-only stream route. The eviction policy stays LRU because dropping a *new* UDP client in favour of
an idle one is protocol-appropriate: UDP has no client to park and no way to signal 503.

**L4 TCP is where the new admission applies**, because there is no TCP equivalent of
`max_udp_sessions` today. The resulting asymmetry is deliberate: the UDP cap is per listener because UDP
sessions are listener-owned state with LRU eviction, while the TCP cap is per pool because TCP
connections map onto pool backends like any other request.

**Stream routes get their own `upstream.Registry` instance.** `Registry.Begin`/`Commit` is a single
global staging transaction driven by `HandlerFactory.Prepare` under one mutex, while stream reload is a
separate fan-in; sharing one registry would mean restructuring the apply transaction. A second registry
instance gives reuse-on-reload, discovery refresh and pool retirement inside `internal/stream` with no
change to the apply path, and costs nothing in fidelity because an upstream referenced by both an HTTP
location and a stream route already has two independent `*Pool` objects. Stream pools get active health
checks with the **probe type forced to `tcp`**, and UDP-only routes get none: the default probe type is
`http`, and an HTTP probe against a Postgres or MQTT backend would fail permanently and take the route
down, while `probeTCP` dials TCP, which a UDP-only backend does not answer.

### 10. Authentication dependencies are protected, and fail closed

The pool-aware RoundTripper — select, admit, retry, mark, release — moves from
`internal/handler/proxy.go` into `internal/upstream` as `upstream.Transport`. It is a pool concern, not
a handler concern: it drives passive health, admission, backend selection and the reason taxonomy, all
of which now live in `internal/upstream`. The relocation is close to free because the retry rewrite
edits that loop anyway, and it is the enabler for every non-HTTP consumer.

`forward_auth.url` and `jwt.jwks_url` then resolve through the same machinery: a **pool-of-one by
default**, with an optional named upstream for load balancing, and a policy-driven timeout replacing the
hardcoded 10s in `forwardHTTPClient`. A hung or failing authentication service is one of the most common
gateway outage modes, and today it has no breaker at all.

**When the forward-auth circuit is open, or admission rejects the subrequest, Jul fails closed.** The
request is rejected; it is never allowed through unauthenticated. A resilience control may never become
an authentication bypass. "Fail open on dependency failure" is a defensible-sounding default elsewhere
in resilience engineering and is a critical vulnerability here.

WASM plugin outbound fetches (`internal/plugins/fetch.go`) remain outside this subsystem; they are
already hardened and egress-gated. Egress policy (`internal/egress`, boundary C of ADR 0016) is
orthogonal throughout: it authorizes *destinations*, while this record bounds *load*.

### 11. Outlier ejection is not built

The activation gate from #116 is recorded: multi-backend pools showing correlated partial failures the
consecutive-failure breaker does not catch; demonstrated insufficiency of circuit state; and
fault-injection evidence of material value. Concretely, the evidence that would reopen it is an incident
or soak report in which a backend's error rate is materially above its peers' while never producing
`max_fails` consecutive failures, and a per-backend success-rate comparison would have ejected it. The
design note stays in #143 and no code ships.

## Accounting model and invariants

Five distinct quantities, never conflated: active logical requests per pool (`A_p`), active logical
requests per backend (`A_b`), pending requests (`Q_p`), physical backend connections (`C_b`), and
long-lived streams, which are counted inside `A_p` and `A_b` for their whole lifetime.

```text
0 <= Q_p <= max_pending_requests
0 <= A_p <= max_active_requests + delta      (delta >= 0 only after a limit decrease, non-increasing)
0 <= A_b <= max_active_per_backend           (enforced as a selection filter)
sum(A_b) <= k * A_p                          (k = 1 today)
C_b <= max_connections_per_backend           (HTTP/1.1 effective)
```

`k` is the maximum number of concurrent attempts a single admitted request may hold. It is 1 today: a
request holds pool admission for its entire life, **including backoff sleeps and backend selection**,
but holds a backend slot only during an attempt, so the sum is less than or equal to `A_p` rather than
equal to it. Future request hedging would raise `k`; it would not invert the inequality.

Every decrement is behind a `sync.Once` or a function-scoped `defer` paired one-to-one with a prior
increment, and no decrement path is reachable without its increment having executed. The existing
release discipline is preserved unchanged: `wrapReleaseBody` for HTTP success and 101 upgrades,
explicit `Release` on transport failure, rewind failure and TLS-downgrade refusal, `releaseBody` on
gRPC streams, and function-scoped `defer` in transcoding.

## State ownership and lifecycle

| State | Identity key | Owner / lifetime | Preserved on reload? | Preserved on discovery update? | Primitive | Reset when |
| --- | --- | --- | --- | --- | --- | --- |
| Resolved `resilience.Policy` | pool key | Pool | replaced atomically | not applicable | `atomic.Pointer` | every commit |
| `admission.active` | pool key, or location for literal FastCGI/uWSGI | Pool (or handler) | yes, on a reused pool | yes | `atomic.Int64` + CAS | pool rebuild only |
| `admission.pending` | same | same | yes | yes | `atomic.Int64` + `mu` | pool rebuild |
| Pending waiter FIFO | same | Pool; entries are request-lifetime | yes, waiters keep waiting | yes | `sync.Mutex` + `chan struct{}` cap 1 | pool close, or forced generation retirement |
| Admission release closure | request | request | not applicable | not applicable | `sync.Once` | request end |
| `Backend.inflight` | `Target.ID` if present, else address | backend identity | yes | yes if the key matches | `atomic.Int64` | backend replaced |
| Circuit state (state, `fails`, `openUntil`, `halfOpenUntil`, `probesInFlight`, `epoch`) | same | backend identity | yes | yes if the key matches | `sync.Mutex` on transitions; `atomic.Bool` closed-hint on the healthy path | backend replaced, success, probe success |
| `Backend.activeHealthy` | same | backend identity | yes | yes if the key matches | `atomic.Bool` | backend replaced, probe threshold |
| Retry-budget window | pool key | Pool | **yes, deliberately** | yes | two `atomic.Int64` pairs plus an epoch; mutex only on rotation | pool rebuild, window expiry |
| Active health checker | pool key | Pool | yes if `upstreamMeta` is equal | not applicable | goroutine plus `pool.done` | pool rebuild |
| Discovery backend set | pool key | Pool | yes if the discovery signature is equal | replaced each refresh | `atomic.Pointer` plus `updateMu` | pool rebuild |
| HTTP `Transport` (with `MaxConnsPerHost`) | location plus TLS fingerprint | transport generation | no — rebuilt, drained, then closed | not applicable | `Generation.Stage`, `handlerGen.retire` | any generation change |
| gRPC `http2.Transport` | location plus TLS fingerprint | transport generation | no | not applicable | same | any generation change |
| Transcoding `*grpc.ClientConn` | `BackendIdentity` | Transcoder (generation-owned) plus an intra-generation reconciler | with the generation | evicted when the identity leaves the pool | `sync.Map` plus a reconcile ticker | identity removed for longer than the grace |
| Snapshot map in request context | request | request | frozen at capture | dynamic pools delegate live | `context.WithValue` | request end |
| `handlerGen.inflight` | generation ID | handler generation | no | not applicable | `atomic.Int64` plus `drained` | retire |
| Metrics collectors | metric name | process | yes | yes | Prometheus internals | never |

**Backend reuse is keyed on identity, not on `(address, weight)`.** `Weight` becomes an unexported
`atomic.Int64` behind a `Weight()` accessor, which costs nothing: it is read on a hot path only inside
`weightedRR.pick`, which already holds its own mutex, and `roundRobin` and `leastConn` never read it.
`weightedRR.updateBackends` clears its current-weight map so smooth weighted round-robin re-converges
immediately after a weight change. Retuning a weight therefore no longer resets a backend's breaker and
in-flight accounting — which was the moment an operator is most likely to be watching them.

**The transcoding connection cache is a two-layer ownership model, not an exception to it.** `Transcoder`
implements `Close` and is generation-staged, so its connections are generation-owned. The eviction loop
solves a problem generation ownership cannot: discovery churn *within* a generation, which would
otherwise accumulate one connection per pod address over days. Reconciling the cache against
`pool.Backends()` on a timer is level-triggered and therefore self-healing, which is strictly more
robust than an edge-triggered "backend removed" callback that would add an observer mechanism to `Pool`
for one consumer and leak permanently on a missed event. The cache is re-keyed from a bare address to
`BackendIdentity`, honors `Target.ID`, and its worst-case reconciliation lag of 60 seconds is a
documented bound.

## Protocol semantics

`max_active_requests` counts **logical requests, streams and connections**.
`max_connections_per_backend` counts **backend sockets**. Under HTTP/2, HTTP/3 and gRPC one socket
carries many streams, so the request limit normally binds first; under HTTP/1.1, WebSocket and L4 TCP
the two move together. A gRPC stream, a WebSocket and a server-sent-events response each hold their slot
for their entire lifetime, which follows from the existing body-close release.

Under HTTP/1.1 keep-alive a connection returns to the idle pool while the request slot is already
released, so `C_b` can bind while `A_b` is low; idle connections count toward
`max_connections_per_backend` until `IdleConnTimeout`.

## Reload semantics

| Event | Pool object | Policy | Counters | Circuit | Transports |
| --- | --- | --- | --- | --- | --- |
| Equivalent configuration | reused | stored anyway | preserved | preserved | reused if the TLS fingerprint is equal |
| Policy-only change | reused | swapped | **preserved** | preserved | reused |
| Limit increase | reused | swapped | preserved; waiters woken | preserved | reused |
| Limit decrease | reused | swapped | preserved; drains down | preserved | reused |
| Backend added | reused | unchanged | new `Backend`, zeroed | fresh `CLOSED` | reused |
| Backend removed | reused | unchanged | old object kept alive by in-flight references, then collected | discarded | idle connections closed on retire |
| Removed then re-added within one `UpdateBackends` | reused | unchanged | preserved | preserved | unchanged |
| Removed then re-added across two updates | reused | unchanged | reset | reset | unchanged |
| Weight changed | reused | unchanged | **preserved** (identity key no longer includes weight) | **preserved** | unchanged |
| `strategy`, health, discovery or backend TLS changed | **rebuilt** | new | reset | reset | new |

Two categories of pool do not get the reuse guarantee and lose their resilience state on reload:
literal `proxy_pass`, `fastcgi_pass` and `uwsgi_pass` targets, which build an unregistered pool-of-one
per generation; and stream routes before they adopt their own registry. Both are pre-existing behavior,
both are documented rather than silently changed, and the seam for fixing either is the same: route the
pool through an `upstream.Registry`.

## Kubernetes and horizontal-scaling semantics

Limits are per replica. Breakers are per replica, which is a feature: independent vantage points cannot
be poisoned by one replica's bad network path, and recovery is probed from several places.

Backend identity is `Target.ID` when discovery supplies one, falling back to the dial address.
Kubernetes supplies `targetRef.uid` from `EndpointSlice`, and Consul supplies `ServiceID`; DNS, DNS SRV
and static configuration supply none. This is what makes a pod-IP reuse by a *different* pod produce a
fresh backend with a fresh breaker rather than inheriting a dead pod's failure history. A Consul
`ServiceID` change on re-registration now resets backend state where it previously persisted, which is
the intended semantic for a logically replaced workload.

`BackendIdentity` — used for retry exclusion within a request and for the transcoding connection cache —
remains address-based, because it concerns *dialing*. The two keys answer different questions and are
deliberately different.

## Public configuration

```toml
[[upstreams]]
name = "api"
strategy = "least_conn"
servers = [
  { address = "10.0.0.11:8080", weight = 1 },
  { address = "10.0.0.12:8080", weight = 1 },
]

# Stateful controls: pool-scoped only. Setting any of these in a location that
# targets a named upstream is a validation error.
[upstreams.resilience]
max_active_requests    = 1000    # 0 = unlimited (default)
max_active_per_backend = 0       # 0 = unlimited (default)
max_pending_requests   = 0       # 0 = no queue, reject immediately (default)
pending_timeout        = "0s"    # 0 = bounded only by the request context
retry_budget_percent   = 0       # 0 = unbudgeted (default)
max_fails              = 1       # circuit failure threshold (unchanged default)
fail_timeout           = "10s"   # circuit open duration (unchanged default)
circuit_half_open_probes = 1     # bounded recovery burst

# Stateless controls: settable here or per location, location wins.
retry_attempts              = 0       # 0 = try every distinct backend once (today)
retry_deadline              = "0s"    # 0 = bounded only by the request context
retry_backoff_initial       = "0s"    # 0 = immediate failover (today)
retry_backoff_max           = "500ms"
max_connections_per_backend = 0       # 0 = unlimited; HTTP/1.1 effective
```

```toml
[[servers]]
listen = "0.0.0.0:443"

  [[servers.locations]]
  match = { type = "prefix", path = "/api/" }
  proxy_pass = "http://api"

    [servers.locations.resilience]
    retry_attempts = 3
    retry_deadline = "2s"
```

Every default is `0` and every `0` means "behave exactly as Jul does today", with one deliberate
exception: `circuit_half_open_probes` defaults to `1`. That is how D11's compatibility requirement is
made structural rather than aspirational.

| Field | Scope | Type | Default | Zero means | Validation | Reload |
| --- | --- | --- | --- | --- | --- | --- |
| `max_active_requests` | pool | int | 0 | unlimited | `0..10000000`; error on a UDP-only stream route | hot; counters preserved |
| `max_active_per_backend` | backend | int | 0 | unlimited | `0..10000000`; warn when `value * len(servers) < max_active_requests` | hot |
| `max_pending_requests` | pool | int | 0 | no queue | `0..100000`; error when set while `max_active_requests = 0` | hot; waiters woken on increase |
| `pending_timeout` | pool | duration | 0 | context-bounded | `0..60s`; must not exceed the retirement grace; warn when `max_pending_requests > 0` and this is 0 | hot; affects new waiters |
| `retry_budget_percent` | pool | int | 0 | unbudgeted | `0..100` | hot; **window not reset** |
| `max_fails` | backend | int | 1 | — | `0..1000` | hot; **state preserved** |
| `fail_timeout` | backend | duration | 10s | — | `0..1h` | hot; state preserved |
| `circuit_half_open_probes` | backend | int | 1 | unbounded | `0..100` | hot |
| `retry_attempts` | pool, location | int | 0 | every backend once | `0..10` | hot |
| `retry_deadline` | pool, location | duration | 0 | context-bounded | `0..5m` | hot |
| `retry_backoff_initial` | pool, location | duration | 0 | no backoff | `0..10s`; `<= retry_backoff_max` | hot |
| `retry_backoff_max` | pool, location | duration | 500ms | — | `0..60s` | hot |
| `max_connections_per_backend` | transport, host | int | 0 | unlimited | `0..100000`; warn on gRPC and transcoding routes | hot; rebuilds the transport |

All paths are registered in the closed-world lifecycle registry and classified **hot**;
`max_connections_per_backend` is hot because transports are already rebuilt every generation.

**Sizing note, because the fail-fast per-backend limit is a footgun:** if
`max_active_per_backend × backend_count < max_active_requests`, the pool limit is unreachable and
requests are rejected with `backend_at_capacity` while the pending queue sits empty. Validation warns
for static server lists; under discovery the backend count is a runtime property, so the check is
necessarily soft and the metric is the authority.

**Memory note:** `max_pending_requests` bounds **request-sized** memory, not waiter-sized memory.
Because admission is innermost, a parked request already holds a parsed `*http.Request`, header maps,
authentication claims and WAF context. The waiter structures are kilobytes per pool; the parked requests
are kilobytes each, so a queue of 200 is closer to single-digit megabytes than to tens of kilobytes.

## Internal constants

These are deliberately not public. Keeping them internal is what allows them to change without a
configuration migration, which only holds if they are findable.

| Constant | Value | Rationale | Revisit when |
| --- | --- | --- | --- |
| Retry backoff multiplier | 2.0 | Standard exponential; a knob has no operational meaning | not expected |
| Jitter algorithm | full jitter | Best fleet de-synchronization with no tuning parameter | fleet-scale herd evidence |
| Retry-budget window | 10s | Long enough to smooth bursts, short enough to react | soak data |
| `min_free_retries` | 3 | Lets small pools fail over; governs low-traffic behavior entirely | **soak on two- and three-backend pools** |
| Circuit state representation | explicit three-state machine under a per-backend mutex, with an `atomic.Bool` closed-hint | Transitions only run when the backend is already failing; obviously correct beats clever | a benchmark shows the closed-hint fast path is insufficient |
| HALF_OPEN lifetime (`halfOpenUntil`) | `fail_timeout` | Bounds the state so one hung probe cannot pin it | streaming probes prove a different bound is needed |
| Queue container | mutex plus FIFO of single-slot channels | See the admission decision | `BenchmarkAdmit_Contended` |
| Timer strategy | one `time.Timer` per queued request | Bounded by `max_pending_requests` | allocation profile |
| `retiredConnGrace` | 30s | Transcoding reconciler grace | pre-existing |
| Transcoding eviction interval | 30s | Worst-case 60s reconciliation lag | pre-existing |
| `MaxIdleConns` / `MaxIdleConnsPerHost` | 100 / 32 | Pre-existing transport tuning | pre-existing |
| Literal-target `max_fails` | 3 | Pre-existing pool-of-one default | pre-existing |
| Health probe interval default | 5s | Pre-existing | pre-existing |

## Failure taxonomy

One closed enum, living in `internal/upstream` so every consumer — `internal/handler`,
`internal/transcode`, `internal/stream`, `internal/auth`, `internal/admin`, `internal/observability` —
imports downward and no package must import a sibling to name a failure.

| Reason | HTTP | gRPC | Retryable | Meaning |
| --- | --- | --- | --- | --- |
| `upstream_unavailable` | 503 | `UNAVAILABLE` | not applicable | No eligible backend: administrative, discovery or active health |
| `circuit_open` | 503 | `UNAVAILABLE` | not applicable | All candidates are circuit-open |
| `proxy_overloaded` | 503 + `Retry-After` | `UNAVAILABLE` | not applicable | Admission rejected: limit reached and the queue is full or timed out |
| `backend_at_capacity` | 503 | `UNAVAILABLE` | not applicable | All candidates at `max_active_per_backend` |
| `upstream_connect_failed` | 502 | `UNAVAILABLE` | yes | Dial, handshake or transport failure |
| `upstream_timeout` | 504 | `DEADLINE_EXCEEDED` | yes | Per-attempt or overall timeout |
| `upstream_tls_identity` | 502 | `UNAVAILABLE` | **no** | Deterministic identity failure |
| `retry_budget_exhausted` | last attempt's status | mapped | — | Retries suppressed by the budget |
| `retry_deadline_exhausted` | 504 | `DEADLINE_EXCEEDED` | — | Overall deadline consumed |
| `request_not_replayable` | last attempt's status | mapped | — | Retry declined: method, body or response already started |
| `client_cancelled` | 499 | `CANCELLED` | — | The client went away |

**The client-facing status and the operator-facing reason are different resolutions of the same event.**
`upstream_unavailable` and `circuit_open` are both 503 to a client and are never conflated for an
operator.

Overload is **503, not 429**: 429 means the *client* sent too many requests, whereas overload is not the
client's fault, and `Retry-After` is defined for 503. Consequently, overload on any gRPC surface returns
`UNAVAILABLE` and not `RESOURCE_EXHAUSTED`, because `httpStatusFromCode` maps `RESOURCE_EXHAUSTED` back
to 429 and would contradict the HTTP path.

Backend address, route path, raw error text, tenant identifiers and hostnames are never encoded as
reasons or as metric labels. Logs may carry unbounded strings; metrics may not.

## Observability

| Metric | Type | Labels |
| --- | --- | --- |
| `jul_upstream_active_requests` | gauge | `pool` |
| `jul_upstream_pending_requests` | gauge | `pool` |
| `jul_upstream_admission_rejected_total` | counter | `pool`, `reason` |
| `jul_upstream_connections` | gauge | `pool` |
| `jul_upstream_retry_attempts_total` | counter | `pool`, `outcome` |
| `jul_upstream_retry_budget_denied_total` | counter | `pool` |
| `jul_upstream_circuit_state` | gauge | `pool`, `state` — counts backends per state |
| `jul_upstream_circuit_transitions_total` | counter | `pool`, `to` |
| `jul_upstream_backends_eligible` | gauge | `pool` |
| `jul_transport_retired_total` | counter | `mode` |

Every label is a configuration-defined pool name or a closed enum, and a test asserts that the metric
label set equals the reason enum. **No `backend` label is introduced**, which is the lesson from an
existing contract violation this record also fixes: `jul_upstream_healthy{pool, backend}` labels by raw
backend address, contradicting the project's own bounded-label rule and growing without bound under
Kubernetes pod churn. It is replaced by `jul_upstream_backends_healthy{pool}`, a count, with per-backend
detail available through the runtime API.

Probe metrics gain a bounded `source` label with two values, `http` and `stream`, so the two registry
instances are decomposable rather than presenting an unexplained doubling.

Tracing gains an `upstream.admission` span, plus `retry.attempt` and `retry.backoff_ms` attributes on
attempt spans, so queue wait and backoff stop appearing as unattributed latency. The tracing seam is a
no-op without the `otel` build tag.

The access log gains a bounded `upstream_reason` field. `jul_http_requests_in_flight` counts *inbound*
requests and legitimately differs from `jul_upstream_active_requests{pool}`, which counts *admitted
upstream* work: static files, cache hits, redirects and blocked requests appear in the first and not the
second.

## API and Console

`BackendProjection.Healthy *bool` becomes a bounded `state` string. A tri-state boolean cannot express
`available`, `circuit_open`, `circuit_half_open`, `health_unhealthy` and `at_capacity`, and #116
requires that distinction. This is a breaking change to the JSON projection, the Zod schema and the
Console health indicator, taken now because there is no adoption to protect.

The pool-level aggregate verdict is served by the API rather than derived in the browser, so the Console
health filter and the API cannot disagree during an incident. The Console renders server-supplied values
and reason strings and computes no resilience logic, per [ADR 0014](0014-operability-surfaces.md).
Console data is polled on an interval, so displayed counters are point-in-time samples and are not an
alerting source.

Editing follows [ADR 0009](0009-two-tier-editing.md): a new `upstream_set_resilience` typed patch
operation with matching diff entries mirrors the existing `upstream_set_health_check`, and the raw
configuration editor accepts the fields immediately regardless. No new RBAC scope is required;
`config:write` gates the editors and all patch operations, and resilience is not a security boundary.

## Security implications

- **No TLS downgrade.** The existing scheme check in the balancing transport is untouched: no retry,
  failover, discovery result or half-open probe may move a request from TLS to plaintext, and there is
  no automatic plaintext gRPC fallback.
- **Deterministic identity failures are terminal**, never retried against the same or another backend.
  Retrying an `unknown_authority` or `peer_identity_mismatch` failure would turn a verification failure
  into a search for a backend that accepts the connection.
- **Authentication dependencies fail closed.** A forward-auth circuit that is open, or an admission
  rejection on the auth subrequest, rejects the request. A resilience control may never become an
  authentication bypass.
- **Admission limits are a denial-of-service control**, bounding memory, goroutines and file descriptors
  under load and converting resource exhaustion into a fast, cheap, bounded 503.
- **The retry budget is an anti-amplification control**, bounding the load Jul can add to a struggling
  backend at `(1 + p/100)×`.
- **Bounded reasons prevent metric-cardinality exhaustion** as a memory-exhaustion vector.
- **No new network dependency** is introduced; local-only state adds no attack surface, and no secret or
  unbounded value appears in the new API projection.

## Consequences

Accepted:

- Configuration that does not mention resilience is unchanged, except that a recovering backend now
  admits one half-open probe instead of the full load.
- Operators must reason about per-replica limits under horizontal scaling.
- Literal `proxy_pass`, `fastcgi_pass` and `uwsgi_pass` targets lose resilience state on reload.
- Native gRPC gets no retry.
- POST and PATCH remain non-retryable even for connect-time failures.
- `max_connections_per_backend` has no effect on gRPC or transcoding routes.
- Under sustained overload Jul pays WAF and authentication cost for requests it then rejects.
- Removing `maxFails` and `failTimeout` from `upstreamMeta` changes reload behavior: tuning them now
  preserves backend state and no longer restarts health checkers. This is an improvement and is
  changelogged.
- Slice 3 is not HTTP-only; a regression in the breaker affects TCP and UDP stream routes.

Two accepted design compromises, articulated rather than hidden:

1. **Two registry instances mean two health views of a shared upstream**, and therefore two probe
   streams. Made decomposable by the `source` label rather than eliminated.
2. **`max_active_per_backend` is fail-fast, not queueing.** Deliberate, because nested pool-then-backend
   admission is a deadlock generator; mitigated by a validation warning and a documented sizing rule.

Gained: a bound on concurrent work, pending work and sockets; a bound on retry amplification; a bounded
recovery burst; one explainable backend state instead of two overlapping ones; a bounded operator
vocabulary; protection for the previously unprotected FastCGI, uWSGI, L4 TCP and forward-auth paths; and
a single subsystem that AI and provider routing must reuse.

## Alternatives considered

| Alternative | Rejected because |
| --- | --- |
| Two atomics (`downUntil` CAS + published `probeSlots`) | The CAS winner re-armed `downUntil`, so every later arrival took the `now <= downUntil` branch and was rejected as OPEN. The published slots were reachable only by goroutines that loaded the stale deadline before the CAS landed, making `circuit_half_open_probes > 1` scheduler-dependent rather than an allowance, and `= 0` yield one probe rather than unbounded |
| Packing circuit state into one `atomic.Int64` | The open flag, probe count and failure count consume all 64 bits, leaving none for `downUntil`. It fits only by re-representing the deadline as a process-relative coarse monotonic value — a bespoke clock source that must also interoperate with the injected-clock test seam — to save one atomic operation on a path that already takes a mutex in the balancer |
| Adaptive rolling-window breaker (#116 Option B) | Three extra knobs and ambiguous low-traffic behavior, with no evidence of need |
| Health checks plus retries only (#116 Option C) | Leaves overload and retry amplification unbounded |
| AI or provider-specific resilience (#116 Option D) | Duplicates transport policy; inconsistent behavior and observability |
| A new breaker beside passive health | Two state machines, one backend, no explainable interaction |
| Dual spelling with `circuit_failures` aliases | Permanent conceptual complexity plus alias-merge validation, for compatibility that does not exist |
| Counting-channel semaphore | Cannot be resized on reload; orphans outstanding tokens |
| `x/sync/semaphore.Weighted` | No dynamic resize; unbounded waiter list |
| `sync.Cond` | No request-cancellation support |
| A goroutine per queued request | Prohibited by #116, and pointless — the inbound goroutine already exists |
| A custom lock-free queue | Complexity without evidence; contention is bounded by the limit itself |
| A central timing wheel | Live timers are bounded by `max_pending_requests` per owner |
| Generation identifiers on counters | `handlerGen` plus a closure-captured release already solves it, more safely |
| Concurrency-based retry budget (Envoy shape) | A retry against a fast-failing backend holds its slot for microseconds, so it barely bounds retry *rate* — vacuous in the scenario the budget exists for |
| Success-earned token bucket (gRFC A6 shape) | Cold-start behavior governed by the initial token count rather than by the operator's percentage |
| Distributed limiter state | A round trip on the hot path and a new failure domain, to make a safety bound exact |
| Registering synthetic pools for literal targets | Invents a public identity for something that has never had one |
| Location overrides for stateful controls on named pools | Pool-scoped counters cannot honor two different limits |
| Blanket L4 admission including UDP | `max_udp_sessions` already exists; a second mechanism is exactly what this record rejects elsewhere |
| An edge-triggered backend-removal callback on `Pool` | Adds an observer mechanism for one consumer and leaks permanently on a missed event; level-triggered reconciliation self-heals |
| Sharing one `upstream.Registry` between HTTP and stream | Requires restructuring the apply transaction for one benefit |
| Excluding FastCGI and uWSGI | Describes the problem — that they have no pool — and uses it as the justification for not solving it |

## One-way doors

1. `[upstreams.resilience]` field names and semantics.
2. `max_connections_per_backend`'s name and scope; a wrong name would be believed.
3. The circuit breaker absorbing passive health, which defines what `max_fails` means permanently.
4. `retry_budget_percent` meaning a ratio of requests rather than of concurrency.
5. In-flight requests surviving a limit decrease.
6. The failure-reason enum, which is simultaneously an API and a metric-label contract.
7. The reason enum and `admission` living in `internal/upstream` — an import-graph commitment.
8. "Per replica" as the documented meaning of every limit.
9. Metric label sets; additive is free, subtractive is not.
10. The eligibility precedence order, which surfaces in the API and Console.
11. `BackendProjection.state` replacing `healthy`.
12. Backend identity being `Target.ID` with an address fallback.
13. Forward-auth failing closed.
14. Excluding stateful location overrides, which is trivially added later and painful to remove.

## Deferred work and explicit non-goals

Outlier ejection (evidence-gated, #143); status-code retries and a `retry_on` field; `Retry-After`
handling; POST and PATCH retry on connect-only failures; native gRPC retry; distributed or cluster-wide
limits; a tenant-facing fairness API; adaptive or rolling-window breakers; request hedging and traffic
mirroring; named reusable resilience profiles; `proxy_pass` to unix sockets, which the `Backend.Network`
extension enables but does not deliver; WASM plugin outbound fetches, which remain hardened and
egress-gated outside this subsystem.

## Validation

Table-driven tests for the retry eligibility matrix, the reason-to-status mapping, policy resolution and
defaulting, and configuration validation. Deterministic state-machine tests for the breaker driven by
the existing injected-clock seam, never wall-clock sleeps. Race-detector tests for concurrent admission
storms asserting non-negative counters and a non-increasing over-limit delta. A dedicated test for the
queue handoff-versus-cancel race, run repeatedly. A test proving forced generation retirement wakes and
rejects parked waiters. A state-machine property test driving the full transition space against a model,
and a concurrent-expiry test asserting that **exactly** `circuit_half_open_probes`
goroutines are admitted. A stream-proxy test proving TCP dial failures drive the same state machine as
HTTP failures. Fuzz tests for retry-budget window rotation under adversarial timestamps and for policy
resolution against arbitrary TOML.

Three gates that only running code can close:

- `BenchmarkAdmit_Uncontended` and `BenchmarkAdmit_Contended` at several `GOMAXPROCS` values, deciding
  whether the CAS fast path suffices before any sharding is considered.
- A `MaxConnsPerHost` integration test that **gates the documented semantics**, covering interaction
  with `MaxIdleConnsPerHost` under HTTP/1.1 keep-alive.
- A soak on two- and three-backend pools deciding whether `min_free_retries = 3` is sufficient for
  reliable failover at low traffic.

Overall performance gate: no more than 2% regression on `BenchmarkProxyRoundTrip` for the unlimited
path. Soak coverage extends the existing burn-in profiles with a resilience profile asserting bounded
memory, bounded goroutine count and correct multi-hour stream accounting.

## Migration and compatibility

| Before | After |
| --- | --- |
| `upstreams.*.max_fails` | `upstreams.*.resilience.max_fails`, same meaning, now named as the circuit threshold |
| `upstreams.*.fail_timeout` | `upstreams.*.resilience.fail_timeout`, same meaning |
| `servers.*.locations.*.proxy_retries` | **`retry_attempts` is the canonical spelling, valid at pool and location level. `proxy_retries` remains accepted as a deprecated alias through the current major; supplying both is a validation error; it is removed at the next major.** |
| Passive cooldown | The same state, surfaced as `circuit_open` |
| Tuning `max_fails` | No longer rebuilds the pool; state preserved, checkers not restarted |
| `BackendProjection.healthy` | `BackendProjection.state` |
| `jul_upstream_healthy{pool,backend}` | `jul_upstream_backends_healthy{pool}` |
| `fastcgi_pass = "name"` | Resolves as a named upstream instead of silently dialling TCP host `name` |
| uWSGI dial timeout | Honors `proxy_connect_timeout` instead of a hardcoded 10s |

Jul has no external adoption, so most of these renames are taken now rather than carried as aliases. The
one exception is `proxy_retries`, which is a live, consumed public field: because Jul rejects unknown TOML
fields strictly, deleting the spelling would turn working configurations into startup and reload failures.
Defaults preserve runtime behaviour, but a field rename does not preserve configuration compatibility, so it
is carried as a deprecated alias with an explicit removal milestone rather than deleted. The NGINX
importer maps the NGINX `max_fails` and `fail_timeout` directives onto the new paths, preserving the
migration story.

## Implementation slices

Following accepted D11.

1. **Limits.** Resolved policy; admission with the CAS fast path and bounded FIFO; per-backend
   selection filter; `max_connections_per_backend`; `proxyHandler.ServeHTTP` seam; `Backend.Network`
   and FastCGI/uWSGI pool membership plus admission; L4 TCP admission; stream registry instance with
   `tcp`-forced probes; `Weight` as an atomic with an address-based reuse key; forced-retirement waiter
   wakeup; lifecycle registry entries.
2. **Retries.** Overall deadline; `retry_attempts`; bounded backoff with full jitter; the sliding-window
   ratio budget; transcoded-unary retry; FastCGI and uWSGI retry; **relocation of the pool-aware
   RoundTripper to `upstream.Transport`**.
   - **2b.** Forward-auth and JWKS through `upstream.Transport`, pool-of-one by default, policy-driven
     timeout, **failing closed**.
3. **Breaker and health.** Packed circuit word with CAS transitions; `circuit_half_open_probes`;
   removal of `maxFails` and `failTimeout` from `upstreamMeta`; `Target.ID` in discovery; transcoding
   cache re-keyed on `BackendIdentity`; `BackendProjection.state`; the metric and Console changes.
4. **Outlier ejection.** Only if the evidence gate in the decision above is triggered.

## Required documentation updates

[upstreams.md](../upstreams.md), [architecture.md](../architecture.md), [core-http.md](../core-http.md),
[grpc-proxy.md](../grpc-proxy.md), [grpc-transcoding.md](../grpc-transcoding.md),
[stream-proxy.md](../stream-proxy.md), [health.md](../health.md),
[reload-semantics.md](../reload-semantics.md), [observability.md](../observability.md) and the metrics
contract, [configuration.md](../configuration.md), [troubleshooting.md](../troubleshooting.md),
[known-limitations.md](../known-limitations.md), [console.md](../console.md), the lifecycle manifest,
and the changelog.

## Downstream issue implications

- **#141** activates as slice 1, extended with FastCGI, uWSGI, L4 TCP and the stream registry.
- **#142** activates as slice 2, extended with transcoded-unary retry and the transport relocation, and
  gains slice 2b for authentication dependencies.
- **#143** activates as slice 3 with its scope narrowed to *evolving* the existing breaker; outlier
  ejection remains a draft behind the evidence gate.
- **#144** activates for the metrics, reason taxonomy, API and Console projection, and soak closure,
  including the metric-cardinality fix.
- **#162** records the prerequisite: the AI experiment must consume this subsystem and is prohibited
  from adding a parallel resilience engine.

## Related

- [ADR 0016](0016-inbound-identity-and-backend-peer-trust.md) — backend peer trust, the no-downgrade
  rule, and generation-owned transports.
- [ADR 0011](0011-reload-plan.md) — the single reload transaction this record does not restructure.
- [ADR 0014](0014-operability-surfaces.md) — why the Console renders and does not compute.
- [ADR 0009](0009-two-tier-editing.md) — the guided-versus-raw editing tiers the new fields follow.
- [ADR 0013](0013-project-operating-model-and-completeness.md) — the portfolio entry that admits this
  work.
