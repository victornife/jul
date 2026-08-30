# Upstreams and backend trust

> **Maturity and delivery:** the released pool/balancing/health foundation is GA. `backend_tls` and the newer admission/retry/circuit surface are separate merged Beta capabilities on current `main`; #287/#144 retain integrated resilience closure. See [status.md](status.md) and [Reload behaviour](#reload-behaviour).

An `[[upstreams]]` block is a named pool of backends with a balancing strategy,
passive and optional active health checking, optional dynamic discovery, and —
new — an explicit outbound TLS policy.

```toml
[[upstreams]]
name     = "inventory"
strategy = "round_robin"
servers  = ["10.0.10.41:8443", "10.0.10.42:8443"]

  [upstreams.backend_tls]
  ca_file     = "/etc/jul/backend-ca.pem"
  ca_mode     = "system_and_file"
  server_name = "inventory.internal"
```

Routes reference the pool by name:

```toml
[[servers.locations]]
match      = { type = "prefix", path = "/inventory/" }
proxy_pass = "https://inventory"
```

## Contents

- [Pool basics](#pool-basics)
- [Admission and overload control](#admission-and-overload-control)
- [Sizing the limits](#sizing-the-limits)
- [The accounting model in one place](#the-accounting-model-in-one-place)
- [Retry](#retry)
- [`backend_tls`](#backend-tls)
- [Trust roots](#trust-roots)
- [Client certificates (mutual TLS)](#client-certificates-mutual-tls)
- [The verified name](#the-verified-name)
- [Peer identities](#peer-identities)
- [Disabling verification](#disabling-verification)
- [Where the policy may be declared](#where-the-policy-may-be-declared)
- [Reload behaviour](#reload-behaviour)
- [Health checks and backend trust](#health-checks-and-backend-trust)

## Pool basics

| Key | Type | Description |
| --- | ---- | ----------- |
| `name` | string | The pool's name, referenced as `proxy_pass = "https://name"` |
| `servers` | []string \| []table | Backends, as `"host:port"`, `"unix:/path.sock"`, or `{ address, weight }` |
| `strategy` | string | `round_robin` (default), `weighted_round_robin`, `least_conn` |
| `max_fails` / `fail_timeout` | int / duration | Passive health: park a backend after N consecutive failures |
| `health_check` | table | Active probes — see [health.md](health.md) |
| `discovery` | table | Dynamic backends — see [service-discovery.md](service-discovery.md) |
| `backend_tls` | table | Outbound TLS policy — below |
| `resilience` | table | Admission and overload control — below |

> **Resilience.** `max_fails` and `fail_timeout` are Jul's circuit breaker: N consecutive failures
> open the backend for the cooldown, after which the next request probes it and the next failure
> re-trips it. [ADR 0017](adr/0017-upstream-resilience-and-overload-control.md) makes that model
> explicit and decides the concurrency, pending, connection, retry-budget and half-open controls that
> extend it. The admission controls below are implemented; the retry-budget and half-open controls
> remain an accepted decision under implementation (#142, #143, #144).

> **Who can use a pool.** `proxy_pass`, `grpc_transcode.target`, `fastcgi_pass` and `uwsgi_pass` all
> accept a named upstream, so FastCGI and uWSGI routes are pool members with the same load balancing,
> health checking, failure accounting and admission as an HTTP route. A backend address may be a unix
> socket (`unix:/run/php/php-fpm.sock`); such a backend has no URL, so `health_check.type = "http"`
> cannot probe it and that combination is a validation error — use `type = "tcp"`.

## Admission and overload control

`[upstreams.resilience]` bounds how much work the pool will accept. It answers a different question
from health checking: health decides *which* backend a request may go to, admission decides *whether*
the request is taken on at all.

```toml
[[upstreams]]
name = "api"
servers = [{ address = "10.0.0.11:8080" }, { address = "10.0.0.12:8080" }]

  [upstreams.resilience]
  max_active_requests    = 1000
  max_active_per_backend = 600
  max_pending_requests   = 100
  pending_timeout        = "2s"

  # Stateless: a location may override it.
  max_connections_per_backend = 256
```

| Key | Type | Default | Description |
| --- | ---- | ------- | ----------- |
| `max_active_requests` | int | `0` (unlimited) | Admitted logical requests, streams and connections for the whole pool |
| `max_active_per_backend` | int | `0` (unlimited) | Admitted logical requests per backend, applied as a **selection filter** |
| `max_pending_requests` | int | `0` (**no queue**) | How many requests may wait for a slot |
| `pending_timeout` | duration | `0` (context-bounded) | How long a request may wait before it is rejected |
| `max_connections_per_backend` | int | `0` (unlimited) | Physical sockets to one backend host, per transport |

Every default reproduces Jul's behaviour before these keys existed, so adding an empty block changes
nothing.

**`max_pending_requests = 0` means no queue, not an unlimited one.** An unbounded pending queue is
the out-of-memory failure this control exists to prevent, so it is deliberately not expressible. If
you want requests to wait, say how many.

**`max_active_per_backend` filters, it does not queue.** A backend already at its limit is simply not
a candidate for selection, so traffic moves to a backend with room. Queueing *inside* backend
selection would let one slow backend block requests that another backend could serve, and a request
holding a pool slot while waiting for a backend slot is a deadlock. When every eligible backend is at
capacity the request is rejected with `503`, and the reason is distinct from "no healthy backend"
because the two call for opposite responses.

Size the two limits together. If `max_active_per_backend` multiplied by the number of backends is
less than `max_active_requests`, the pool limit can never be reached: requests are rejected while the
pending queue sits empty. `jul lint` warns about exactly that, for static server lists.

### Requests versus sockets

`max_active_requests` counts **requests and streams**. `max_connections_per_backend` counts
**sockets**. Under HTTP/2, HTTP/3 and gRPC one socket carries many streams, so the request limit
normally binds first; under HTTP/1.1 and WebSocket the two move together.

| Client protocol | Counts against the request limit | Backend sockets | Which limit binds |
| --- | --- | --- | --- |
| HTTP/1.1, no keep-alive | +1 per request | +1 per request | both, in lockstep |
| HTTP/1.1 keep-alive | +1 per request | connections reused; idle ones count until they time out | either — sockets can bind while active requests are few |
| HTTP/2 | +1 per **stream** | typically 1 | request limit |
| HTTP/3 | +1 per **stream** | unaffected: the backend leg is still HTTP/1.1 or HTTP/2 | request limit |
| WebSocket (101) | +1 until the upgraded connection closes | +1 dedicated, hijacked, never pooled | both |
| Server-sent events | +1 for the response's lifetime | as HTTP/1.1 | request limit |
| gRPC unary | +1 per call | shared HTTP/2 connection | request limit |
| gRPC server, client or bidirectional streaming | +1 for the **whole stream lifetime** | shared | request limit |
| gRPC transcoding, unary and streaming | +1 per call or stream | shared | request limit |

A long-lived stream — a WebSocket, an SSE response, a gRPC stream open for hours — holds its slot for
its entire life. That is the point: a gateway with a thousand idle WebSockets is a thousand requests
busy, and a limit that released them at handshake time would report it as idle.

One detail that looks like a leak and is not: an upgraded connection ends when **both** directions
end. A backend that closes its half while the client's half is still open has not ended the tunnel,
so the slot is still held. It is released when the connection actually closes, or on error.

### `max_connections_per_backend`

The bound maps to Go's `MaxConnsPerHost`, the only lever that limits sockets without defeating
connection pooling and that honours the request context while a request waits for a dial. Idle
connections count toward it until they time out, so under keep-alive it can bind while the pool's
active request count is low.

It is **stateless** — a transport is built per route — so it may be set on the pool or on a location,
and the location wins. A location value of `0` inherits the pool's rather than meaning "unlimited":
every other zero in this configuration means "not set", and one field reading its zero the other way
would be a trap.

Two exceptions are worth knowing:

- **It does not apply to native gRPC or gRPC transcoding.** One HTTP/2 connection carries every
  stream there, so a socket bound would not bound concurrency. `jul lint` warns when it is set on
  such a route; use `max_active_requests` instead.
- **Health checks are exempt.** Probes are built on their own client, so a pool whose sockets are all
  busy serving traffic can still dial a probe and notice a backend recovering. Without the exemption
  a pool at its limit could never observe its way out of it.

### What a rejected request looks like

Overload is **`503 Service Unavailable` with a `Retry-After` header**, never `429`. A `429` says the
*client* sent too many requests; a saturated pool is not the client's fault, and `Retry-After` is
defined for `503`.

### Where admission sits

Admission is the innermost step, immediately around the upstream call. The consequences are
deliberate:

- a **cache hit never consumes a slot**, because it never reaches an upstream;
- a request blocked by the WAF or by authentication never consumes one either;
- background cache revalidation *does* consume one, because it really does call the upstream;
- under sustained overload Jul still pays full WAF and authentication cost for requests it then
  rejects. Counting exactly the work that reaches a backend is worth more than saving CPU on the
  rejection path.

This is also why `jul_http_requests_in_flight` and the pool's active count legitimately differ.

### Reload

All four keys are hot. The resolved policy is swapped into the live pool as an atomic pointer, and
the pool is **not** rebuilt — rebuilding would discard the very counters the limits govern. So:

- requests already in flight are never evicted by a lower limit; admission is an *entry* control, and
  the excess drains as those requests finish;
- while the pool is over the new limit, new arrivals are rejected or queued, and recovery is
  monotonic — the overshoot only ever shrinks;
- raising a limit takes effect immediately and wakes requests that are already queued.

`pending_timeout` may not exceed `global.shutdown_timeout`, which bounds handler-generation
retirement: a request allowed to wait longer than the retirement grace would outlive the transport it
is queued for. If a generation is forcibly retired, its queued requests are woken and rejected rather
than admitted onto a closed transport.

### Scope

The four admission keys are **pool-scoped only**. They are stateful — the counters and the queue have
exactly one owner — and a control is location-overridable only if it owns no shared state. A
`[servers.locations.resilience]` block accepts only the stateless controls, so a stateful key written
there is rejected at parse time rather than silently ignored.

`max_connections_per_backend` is the stateless one, and may appear in either place:

```toml
[[servers.locations]]
match = { type = "prefix", path = "/bulk/" }
proxy_pass = "http://api"

  [servers.locations.resilience]
  max_connections_per_backend = 32
```

A literal `proxy_pass = "http://10.0.0.5:8080"` target builds an unregistered pool of one that is
rebuilt on every reload, so **its admission counters reset on reload**. Name the upstream if you need
that state to survive.

### Sizing the limits

Every limit is **per replica**. Ten replicas with `max_active_requests = 100` admit up to a thousand
requests between them, so size from what one process should carry, not from the fleet total.

Start from what the backend can actually absorb. For PHP-FPM that is `pm.max_children`; for a
connection-pooled database it is the pool size. Admitting past that number does not add throughput —
it moves the queue somewhere Jul cannot see, bound or report.

`max_active_per_backend` multiplied by the backend count must reach `max_active_requests`, or the pool
limit is unreachable and requests are rejected with a backend-capacity error while the pending queue
sits empty. `jul lint` warns about this for static server lists. Under discovery it cannot: the
backend count is a runtime property, so the check is necessarily soft and the metric, not the
configuration, is the authority.

**`max_pending_requests` costs request-sized memory, not waiter-sized.** This is the number most
likely to be sized wrongly. Because admission is innermost, a parked request already holds a parsed
`*http.Request`, its header maps, any authentication claims and WAF context — the waiter structure
itself is a rounding error beside it. Measured on Jul's own test harness with a realistic request
(four headers including a bearer token):

| Parked requests | Heap | Per request |
| --- | --- | --- |
| 200 | ~1.5 MB | **~7.5 KB** |

So a queue of 200 is single-digit megabytes, and a queue of 10 000 is tens of megabytes of live heap
that cannot be reclaimed while the queue is full. Size it as a burst absorber, not a buffer.

### The accounting model in one place

Five quantities, never conflated:

| Symbol | Meaning | Bounded by |
| --- | --- | --- |
| `A_p` | Admitted logical requests, streams and connections per pool | `max_active_requests` |
| `A_b` | Admitted logical requests per backend | `max_active_per_backend`, as a selection filter |
| `Q_p` | Parked requests per pool | `max_pending_requests` |
| `C_b` | Physical sockets per backend host, per transport | `max_connections_per_backend` |
| UDP sessions | Per-listener UDP session table | `max_udp_sessions` |

Every protocol contributes to `A_p` the same way, for the whole life of its unit of work:

| Surface | One unit of work is | Held for |
| --- | --- | --- |
| HTTP/1.1, HTTP/2, HTTP/3 | one request or stream | the request |
| WebSocket (101) | the upgraded connection | until both directions close |
| Server-sent events | the streaming response | the whole response |
| gRPC unary and streaming | the call or stream | the stream's lifetime |
| gRPC transcoding | the call or stream | the call |
| FastCGI, uWSGI | the request | the request |
| L4 TCP stream | the **connection** | the connection |
| L4 UDP stream | *not admitted here* — see `max_udp_sessions` | — |

The last two rows are the asymmetry worth remembering: the **TCP cap is per pool** because TCP
connections map onto pool backends like any other request, while the **UDP cap is per listener**
because UDP sessions are listener-owned state reclaimed by idle eviction. Details in
[stream.md](stream.md#bounding-concurrency).

### Why two in-flight numbers disagree
`jul_http_requests_in_flight` and the pool's active count measure different things and will not match:

- the first counts **inbound requests** being served, including static files, redirects, cache hits
  and requests rejected by the WAF or by authentication;
- the second counts **admitted upstream work**, which is only the subset that reaches a backend.

A cache hit raises the first and never the second. Sustained overload raises the first while the
second sits pinned at its limit. Neither is wrong, and forcing them to agree would mean either
counting work that never happened or losing the ability to see it.

## Retry

Retry exists to route around one unlucky backend. Without a bound it does the opposite: with
`retry_attempts` unset Jul tries every distinct backend once, so a **total** outage multiplies
upstream load by the backend count at exactly the moment the upstream can least afford it. These
controls put a ceiling on that.

```toml
[upstreams.resilience]
retry_attempts        = 2
retry_deadline        = "3s"
retry_backoff_initial = "20ms"
retry_backoff_max     = "500ms"
retry_budget_percent  = 10
```

Every default reproduces today's behaviour: no cap beyond the backend count, no deadline beyond the
request context, immediate failover and no budget.

### What is retried, and what is not

A retry needs **all** of these to hold. The first one that does not is the reason the sequence
stopped:

| Condition | Why |
| --- | --- |
| The failure is a transport error | A status code is an answer. Jul does not retry 5xx |
| No response byte has reached the client | Otherwise the request would be executed twice |
| The method is retry-safe | `GET`, `HEAD`, `OPTIONS`, `TRACE`, `PUT`, `DELETE` |
| The body is replayable | Absent, or rewindable via `GetBody` |
| The failure is not deterministic | A TLS identity mismatch is the same mismatch everywhere |
| The client has not cancelled | Nobody is waiting for the answer |
| Attempts remain | `retry_attempts` |
| Deadline remains | Enough for an attempt, not merely for the sleep |
| An untried eligible backend exists | Retrying the backend that just failed is not failover |
| The budget allows it | `retry_budget_percent` |

**`POST` and `PATCH` are never retried**, even with a replayable body. `GetBody != nil` proves the
request *can* be sent again; it does not prove that doing so is safe. A connection-level error does
not prove the backend did not accept the request, commit it and die before answering.

**Failover never downgrades.** A backend whose scheme does not match an `https` route is refused
rather than dialled, and the refusal is terminal — retrying into the next plaintext backend would be
the same downgrade one hop later.

### The deadline dominates

`retry_deadline` bounds the **whole sequence**, not each attempt: the effective deadline is
`min(request deadline, start + retry_deadline)`, and every attempt and every backoff sleep runs under
it. Three attempts against a three-second deadline take at most three seconds, not nine.

Backoff doubles from `retry_backoff_initial`, clamps at `retry_backoff_max`, and applies **full
jitter** — the delay is drawn uniformly from `[0, interval)`. Half-jitter would halve the spread for
no benefit, and spread is the entire point: every client of a backend that just died otherwise
computes the same interval and returns together, turning failover into a synchronised second wave.

If the remaining deadline leaves no room to sleep *and* attempt, the sequence stops instead of
sleeping. Waking up at the deadline only converts a failure into a slower failure.

### The budget is what actually bounds amplification

`retry_attempts` caps one request. It does nothing about a thousand requests all retrying at once,
which is the case that takes an upstream down. `retry_budget_percent` bounds the aggregate: over a
trailing window, retries are permitted while

```text
retries < floor(primaries * retry_budget_percent / 100) + 3
```

so upstream load is bounded at `(1 + p/100) ×` client load plus a small floor — **1.1× at `p = 10`**,
against up to `3×` for an unbudgeted `retry_attempts = 3`. Primary attempts accrue automatically, so
nothing needs to report success.

The floor of three free retries exists so a pool with almost no traffic can still fail over. Without
it the first request after an idle period would be unretryable, which is precisely when a stale
pooled connection is most likely.

Two properties worth knowing before tuning it:

- **The window is not reset by a reload.** The counters live on the pool and a policy change swaps
  only the percentage. Resetting would hand out a fresh burst of retries, and a reload during an
  incident is the least appropriate moment to forgive the retry load that helped cause it.
- **Two locations sharing a pool share one budget window.** `retry_attempts` is location-overridable
  while the budget is not, so a location configured to retry aggressively can consume the allowance
  of a conservative one. This is correct — the budget protects the shared backend, not the route —
  and it is stated here because it is surprising.

### Scope and the deprecated spelling

`retry_attempts`, `retry_deadline`, `retry_backoff_initial` and `retry_backoff_max` own no shared
state, so a location may override them and a location value of `0` **inherits** rather than meaning
"unlimited" — the same rule `max_connections_per_backend` follows. `retry_budget_percent` owns a
window, so it is pool-scoped and a location may not set it.

`proxy_retries` is the deprecated spelling of `retry_attempts` under `[[servers.locations]]`. It
remains valid, because Jul rejects unknown TOML fields strictly and deleting a live spelling would
turn working configurations into startup and reload failures. Setting both on one location is a
validation error rather than a precedence rule: two names for one control that quietly disagree is
how a configuration comes to mean something its author did not intend. Removal is scheduled for the
next major release.

## backend_tls

`backend_tls` is the **outbound** TLS policy. It is a different key from the
inbound `[servers.tls]` block on purpose: `tls` already means *inbound*
termination under `[[servers]]`, and one key with opposite directions in two
places is a mistake waiting to happen.

| Key | Type | Default | Description |
| --- | ---- | ------- | ----------- |
| `ca_file` | string | — | PEM bundle of trust roots. Consulted only when `ca_mode` selects it. |
| `ca_mode` | string | `system` | `system`, `system_and_file`, or `file_only`. |
| `client_cert` / `client_key` | string | — | Client certificate presented to the backend. Both or neither. |
| `server_name` | string | derived | The name the backend certificate is verified against, and the SNI value sent. |
| `min_version` | string | `1.2` | `1.2` or `1.3`. |
| `peer_identities` | []string | — | Prefixed identities (`dns:`, `uri:`) required of the backend certificate, in addition to standard verification. |
| `insecure_skip_verify` | bool | `false` | Disables verification. `jul lint` reports this as an **error**. |

Without a `backend_tls` block an `https://` backend is verified the way any Go
client verifies: the platform trust store, and the hostname from the target.
The block exists for everything that needs more — a private CA, a client
certificate, an SNI that differs from the dial address, or an explicit identity.

## Trust roots

`ca_mode` is an **explicit enum and is never inferred from the presence of
`ca_file`**:

| Mode | Meaning |
| --- | --- |
| `system` *(default)* | The platform trust store only. A `ca_file` set under this mode is a configuration error, not a silent no-op. |
| `system_and_file` | The platform trust store **plus** `ca_file`. |
| `file_only` | `ca_file` **replaces** the platform store. |

Inference is unrevertable: if the presence of `ca_file` implied augmentation,
changing that later to replacement would silently alter which backends verify,
with no error anywhere. An explicit enum makes the choice reviewable.

`file_only` is the stricter setting: a backend signed by a public CA will fail,
which is usually what you want for a purely internal service.

## Client certificates (mutual TLS)

```toml
[upstreams.backend_tls]
client_cert = "/etc/jul/client.pem"
client_key  = "/etc/jul/client.key"
```

Both are required together; either alone is a validation error. The pair is
parsed when the configuration is prepared, so a mismatched certificate and key
fail the reload rather than the first request. The key is read, parsed, and its
bytes dropped: it is never logged, never projected into the Console, and never
represented in status output — not even as a digest.

Protect the key file with `0600` permissions and a service-owned directory, as
with any private key ([deployment.md](deployment.md)).

## The verified name

`server_name` sets both the SNI value and the name the certificate is checked
against. When it is omitted, the name is derived from the configured logical
target — the upstream name or the literal host — with any port removed.

This matters most for a **discovery-backed** pool. Discovery returns addresses;
addresses are dial destinations only. The configured logical name stays the
verified identity, so a compromised registry that returns an attacker's address
cannot also choose the name that address is verified against — the handshake
fails instead.

An IP-literal target with no `server_name` is verified against the certificate's
IP SANs under Go's standard rules. If your backend certificate carries a DNS
name rather than an IP SAN, set `server_name` explicitly.

## Peer identities

Standard verification proves the certificate chains to a trusted root and
carries the expected name. `peer_identities` additionally requires the
certificate to carry one of a set of identities:

```toml
peer_identities = ["dns:inventory.internal", "uri:spiffe://example.org/inventory"]
```

- Entries are **prefixed** (`dns:`, `uri:`) from the first release, so future
  identity types are additive rather than ambiguous.
- Identities are **ORed** — matching any one is enough.
- The check runs **after** standard verification, never instead of it. A
  configuration cannot use an identity check to skip chain validation.
- Matching is exact, using the certificate's own SAN semantics. There is no
  regex, no substring matching, and no invented wildcard grammar; a wildcard in
  a certificate is matched by the certificate's own rules.

## Disabling verification

```toml
insecure_skip_verify = true   # jul lint: ERROR
```

This disables **peer verification only** — the connection is still encrypted,
but Jul no longer knows who is on the other end, which is the property that
matters. It exists as an emergency path, and it is treated accordingly:

- `jul lint` reports it as an **error**, so the command exits 1 even without
  `-strict`.
- The server logs one warning per affected backend at startup.
- The Console security projection counts the affected policies.
- `Validate()` still accepts it, because a field whose only purpose is opting
  into an insecure mode cannot be a validation rejection — it would be unusable.

Two combinations *are* hard errors, because they are self-contradictory:
`insecure_skip_verify` with `peer_identities` (nothing can be proven about a
peer that was never verified), and with a non-`system` `ca_mode` (trust roots
that would never be consulted).

No metric label ever carries an upstream name, so the count is bounded even
when the names are not.

## Where the policy may be declared

The same block, with the same meaning, appears in two places:

| Place | Applies to |
| --- | --- |
| `[upstreams.backend_tls]` | Every route that reaches this pool over TLS |
| `[servers.locations.backend_tls]` | That one route, whether it targets a pool, a literal `https://` address, native gRPC over TLS, or a TLS transcoding target |

When both exist, **the location's policy applies to that route** and `jul lint`
reports the override so it is never silent.

A `backend_tls` block on a route whose backend is *not* reached over TLS is a
validation error rather than a no-op: an operator who writes one believes the
backend is verified, and a silently inert security policy is worse than a
rejected configuration.

## What enforces the policy today

| Consumer | Status |
| --- | --- |
| HTTP reverse proxy (named pools and literal `https://` targets, including WebSocket and streaming upgrades) | **enforced** |
| Native gRPC passthrough | **enforced** |
| gRPC-JSON transcoding, including the reflection fetch | **enforced** |
| Active health probes | **enforced** — see [Health checks and backend trust](#health-checks-and-backend-trust) |

The HTTP proxy builds one `http.Transport` per handler generation from the
resolved policy, so a policy change produces a new connection pool: a request
admitted after the change cannot reuse a keep-alive or HTTP/2 connection
established under the previous trust, and the retiring generation closes its
idle connections when it retires. Requests already in flight finish on their own
connection, which is the generation drain contract rather than an abrupt cut.

A route configured for `https` never dials a plaintext backend, and a TLS gRPC
route never falls back to cleartext h2c. No retry, failover or discovery result
may downgrade the connection; the request fails instead.

For gRPC the policy's TLS config advertises `h2` explicitly, because those
transports speak HTTP/2 only — a backend that negotiated `http/1.1` would break
gRPC framing. The transcoder's connection cache lives on the transcoder, which
is rebuilt with its handler generation, so a policy change produces fresh
connections rather than reusing ones established under the previous trust.

### Failure categories

A backend-trust failure is logged with a bounded `tls_failure` category
alongside the usual proxy error, so it can be grepped and later counted without
the raw error, host, certificate subject or file path becoming a label:

`unknown_authority` · `hostname_mismatch` · `peer_identity_mismatch` ·
`client_certificate` · `certificate_expired` · `certificate_invalid` ·
`tls_version` · `tls_handshake` · `tls_other`

## Reload behaviour

`backend_tls` is **hot reload**, and that classification was earned rather than
assumed: every consumer demonstrably rebuilds from the candidate policy.

- The HTTP proxy, native gRPC and the transcoder build their clients with the
  handler generation that owns them.
- The **resolved policy's fingerprint is part of the pool's identity**, so a
  changed policy rebuilds the pool and with it the probe client. Because the
  fingerprint digests file *contents*, rotating a certificate in place — with no
  configuration edit at all — is detected and applied on the next reload.

A malformed policy fails while the candidate is prepared, so the reload aborts
before anything is published rather than leaving a backend unverifiable.

The classification is conditional on the **backend set**, not on the listener
set:

| Change | Verdict |
| --- | --- |
| Adding a pool or route that carries a policy | applies on this reload — there is no running client to strand |
| Removing one | applies on this reload |
| Editing the policy of a pool or route that survives the reload | restart required |

Trust material is registered as secret-bearing from the first release, so the
lifecycle fingerprint digests the **contents** of `ca_file`, `client_cert` and
`client_key`. Rotating a certificate in place, without editing the
configuration, is therefore detected correctly — even while the action remains a
restart. Detection and action are separable, and getting detection right early
costs nothing. See [reload-semantics.md](reload-semantics.md).

## Health checks and backend trust

An active HTTP probe inherits the pool's scheme, and now also its trust: the
probe client uses the **same resolved policy as live traffic**. A backend is
never reported healthy under weaker verification than the requests Jul will send
it — and, equally, a private-CA backend that live traffic verifies is no longer
reported unhealthy because the probe could not.

The policy that governs probes is the **pool's**. A route-level `backend_tls`
block applies to that route's traffic only; a pool may serve several routes with
different overrides, so there is no single route policy a probe could adopt. Put
the trust roots a probe needs on the pool.

A TCP probe remains a reachability check and never represents identity
verification.

`jul lint` still warns when an `https` pool with an enabled HTTP probe has no
policy at all, because such a pool is verified against the platform store alone.

See [health.md](health.md).
