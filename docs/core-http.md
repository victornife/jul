# Core HTTP

> **Maturity:** GA (see [ADR 0003](adr/0003-maturity-and-ga.md)). TLS termination
> is documented in [tls-acme.md](tls-acme.md); client certificates in
> [mtls.md](mtls.md).

Core HTTP is the foundation every other Jul.IA feature builds on: it accepts a
connection, picks a virtual host by `Host` header, matches a location by path,
and dispatches to a content handler — static files, a reverse proxy, or a
FastCGI/uWSGI application — following a simplified, predictable subset of NGINX
`server`/`location` semantics.

## Quick start

```toml
[[servers]]
listen       = "0.0.0.0:8080"
server_names = ["example.com", "*.example.com"]

  # Serve static assets at the site root.
  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  root  = "/var/www/example"
  index = ["index.html"]

  # Reverse-proxy the API to an upstream pool.
  [[servers.locations]]
  match      = { type = "prefix", path = "/api/" }
  proxy_pass = "http://app"

  # Hand PHP off to a FastCGI worker.
  [[servers.locations]]
  match        = { type = "regex", path = "\\.php$" }
  root         = "/var/www/example"
  fastcgi_pass = "unix:/run/php/php-fpm.sock"

[[upstreams]]
name     = "app"
strategy = "round_robin"
servers  = [
  { address = "10.0.0.1:3000" },
  { address = "10.0.0.2:3000" },
]
```

## Request lifecycle

1. **Listener** accepts the connection on the configured `listen` address.
2. **Virtual host** selection: the `Host` header (lowercased, port-stripped) is
   scored against each server block's `server_names`; the best score wins, with
   the first-declared block as the default.
3. **Location** selection: the request path is matched against the server's
   locations by precedence (exact → longest prefix → regex → `/` fallback).
4. **Rewrites** (if any) run against the matched location's path.
5. **Middleware** wraps the handler (request ID, recover, body limit, optional
   timeout / rate limit / access log).
6. **Handler** serves the response (static, proxy, FastCGI, uWSGI, or a config
   action such as `return`/`redirect`/`deny`).

## Virtual host matching

`Host` is normalized (lowercased, whitespace-trimmed, port and IPv6 brackets
handled) and scored against each block's `server_names`:

| Server name form | Matches | Score |
| --- | --- | --- |
| Exact (`www.example.com`) | only that host | 3 (highest) |
| Leading wildcard (`*.example.com`) | `a.example.com`, **not** `example.com` | 2 |
| No match | — | 0 |

The highest score wins. When nothing scores above 0, the request falls to the
**default server** — the first server block declared for that listen address.

## Location matching

A request path is resolved against the server's locations in this fixed order:

| Step | Match type | Rule |
| --- | --- | --- |
| 1 | `exact` | `match.path` equals the request path exactly |
| 2 | `prefix` | longest `match.path` that is a prefix of the request path (the `/` catch-all is excluded here) |
| 3 | `regex` | first regex (in config order) that matches |
| 4 | fallback | the `prefix` location whose path is `/`, if present |

If no location matches and there is no `/` fallback, the request is unhandled
(the router's default action returns **501 Not Implemented**).

### Rewrites

Each location may carry `rewrites` (regex → replacement). Flags follow a
simplified NGINX model:

| Flag | Behaviour |
| --- | --- |
| *(none)* | rewrite the path and continue evaluating further rewrites |
| `last` / `break` | rewrite and stop; the already-matched location handles it (no fresh location search) |
| `redirect` | external 302 to the target |
| `permanent` | external 301 to the target |

## Static file serving

| Capability | Behaviour |
| --- | --- |
| Root confinement | `root` opened via `os.Root` (Go chroot-style); `..` cannot escape |
| Index files | `index` (default `["index.html"]`) tried for directory requests |
| `try_files` | ordered fallbacks before the final URI |
| Directory listing | off by default (`directory_listing`) |
| Hidden files | dotfiles blocked unless `allow_hidden` |
| Range requests | served via `http.ServeContent` (partial content / `If-Range`) |
| Caching validators | `ETag` = `"<mtime-unixnano>-<size>"`; conditional GETs honoured |
| MIME types | `mime.TypeByExtension` |
| Precompressed assets | `.br` then `.gz` sidecars served when the client accepts them |
| Error pages | per-status file (served) or URL (302 redirect) |

### Listener-scoped server fields

A `[[servers]]` block is a virtual host, but a few of its fields describe the
listening socket that several virtual hosts share. Those are resolved once per
`listen` address: the timeouts and `max_header_bytes` take the **first** block's
value, while `h2c` and `http3.enabled` are enabled if **any** block on the
address enables them. `jul lint` warns when blocks disagree, naming the winning
and the ignored block; see
[the configuration reference](configuration.md#fields-resolved-once-per-listen-address).

`client_max_body_size` is *not* in that set — the router applies the matched
virtual host's limit per request.

## Reverse proxy

Built on the standard library's `httputil.ReverseProxy` over a balancing
transport that picks a backend from the named (or anonymous) upstream pool.

| Aspect | Behaviour |
| --- | --- |
| Target | `proxy_pass` references a named upstream or a single hardcoded backend (static config only) |
| Forwarded headers | `X-Forwarded-*` regenerated from Jul's own view of the request (see [Forwarded headers to the backend](#forwarded-headers-to-the-backend)); `$proxy_add_x_forwarded_for`, `$remote_addr`, `$realip_remote_addr`, `$host`, `$scheme`, `$ssl_client_*` expandable in custom `headers` |
| Failover | one retry per backend, **idempotent methods only** (GET/HEAD/OPTIONS/TRACE/PUT/DELETE) and only when the body is re-readable |
| Timeouts | `proxy_connect_timeout` (default 10s); `proxy_read_timeout` / `proxy_send_timeout` are per-read / per-write **inactivity** bounds (NGINX semantics) — they cap the gap between successive reads of the response (headers and slow-trickle body) or writes of the request, not the total transfer, so a steady stream is never cut off; both default to unbounded. 90s idle keep-alive |
| Connection reuse | `MaxIdleConns` 100, `MaxIdleConnsPerHost` 32, HTTP/2 attempted. One transport per handler generation, so a `backend_tls` change never lets a new request reuse a connection verified under the old policy |
| Backend TLS | `https://` targets use the resolved [`backend_tls`](upstreams.md#backend-tls) policy — private roots, client certificate, verified name, minimum version, peer identities. A route configured for `https` never downgrades to plaintext |
| WebSocket / SSE | `Connection: Upgrade` (HTTP `101`) spliced bidirectionally; `text/event-stream` and chunked responses streamed (flushed per write, never buffered). Both work with `cache = true`: an upgrade bypasses the cache and an event stream is never stored |
| Error mapping | 503 no eligible backend / circuit open / overloaded / at capacity, 504 timeout, 502 connection or backend-TLS-identity failure, **499** client disconnected. See [the failure taxonomy](#upstream-failure-taxonomy) |

### Upstream failure taxonomy

The status a client sees and the reason an operator sees are different
resolutions of the same event. Four reasons are all `503` to a client and are
never merged for an operator: "the backends are gone" and "Jul is deliberately
not calling them" call for opposite responses.

| Reason | HTTP | gRPC | Meaning |
| --- | --- | --- | --- |
| `upstream_unavailable` | 503 | `UNAVAILABLE` | No eligible backend (admin, discovery or active health) |
| `circuit_open` | 503 | `UNAVAILABLE` | Every candidate's circuit is open |
| `proxy_overloaded` | 503 + `Retry-After` | `UNAVAILABLE` | Admission rejected the request |
| `backend_at_capacity` | 503 | `UNAVAILABLE` | Every candidate is at `max_active_per_backend` |
| `upstream_connect_failed` | 502 | `UNAVAILABLE` | Dial, handshake or transport failure |
| `upstream_timeout` | 504 | `DEADLINE_EXCEEDED` | A per-attempt or overall timeout elapsed |
| `upstream_tls_identity` | 502 | `UNAVAILABLE` | The backend failed to prove its identity (deterministic, never retried) |
| `retry_budget_exhausted` | last attempt's status | last attempt's code | Retries suppressed by the pool budget |
| `retry_deadline_exhausted` | 504 | `DEADLINE_EXCEEDED` | The overall retry deadline was consumed |
| `request_not_replayable` | last attempt's status | last attempt's code | Method, body or an already-started response forbade another attempt |
| `client_cancelled` | 499 | `CANCELLED` | The inbound request context was cancelled — the client went away |

Retry suppression never overwrites the status a backend actually produced:
reporting 503 because a budget was spent would hide a 500 the client needs.

**Overload is 503, not 429.** `429` means the *client* sent too many requests and
is already Jul's rate-limiter status; overload is not the client's fault, and
`Retry-After` is defined for 503. The gRPC consequence follows: overload is
`UNAVAILABLE`, not `RESOURCE_EXHAUSTED`, because `RESOURCE_EXHAUSTED` maps back
to 429 and would contradict the HTTP path.

**Client cancellation records 499**, nginx's non-IANA status, and this is a
deliberate change: it was previously recorded as 504. The client has already
disconnected, so nothing is transmitted either way and this is purely the
recorded status — but recording 504 inflated "gateway timeout" with requests
where nothing timed out.

Telling the two cancellations apart matters. `context.Canceled` reaches the
classifier both from a client going away *and* from Jul's own retry deadline,
which derives its context from the inbound one. Only a cancellation of the
**inbound** request context is a client disconnect; one of Jul's own is
`retry_deadline_exhausted`, and 504.

A reason is a closed enum, safe as a metric label and an access-log field. A
backend address, route path, tenant identifier or raw error text is never one.

### Forwarded headers to the backend

Every inbound `X-Forwarded-*` header is discarded and replaced. What Jul sends
is built from its own trusted knowledge — the canonical client derived by the
listener's [`client_address`](configuration.md#client-address-and-trusted-proxies)
policy, and the peer the request actually arrived from — never by appending to
what the client sent.

```
X-Forwarded-For: <canonical client>, <direct peer>
```

The peer is omitted when it equals the client, which is the direct case. Exact
emission per deployment shape:

| Inbound | Emitted `X-Forwarded-For` |
| --- | --- |
| Direct client `C`, no policy | `C` |
| Direct client `C` sending `X-Forwarded-For: 10.9.9.9` | `C` — the spoofed chain is discarded |
| Client `C` → trusted proxy `P` → Jul | `C, P` |
| Client `C` → trusted `P1` → trusted `P2` → Jul | `C, P2` — intermediate trusted hops are dropped |
| Untrusted peer `X` asserting `C` | `X` |
| Trusted peer `P` sending a malformed chain | `P` |

**The multi-proxy case is deliberately lossy.** Jul is the last hop before the
backend, and reconstructing the full inbound chain would re-inject third-party
data into a channel the backend authenticates. Full fidelity is restorable later
without a breaking change.

The NGINX-compatible variables follow the same model:

| Variable | Value |
| --- | --- |
| `$remote_addr` | the **canonical client**, matching NGINX with its realip module configured — which is what `client_address` expresses. With no trusted proxy it is the transport peer, so a direct deployment is unchanged. |
| `$realip_remote_addr` | the **direct transport peer**, NGINX's own name for the address the connection came from |
| `$proxy_add_x_forwarded_for` | the same trusted chain shown above |

Custom `headers` are applied **after** the forwarding headers are constructed,
so an explicit operator value wins — and only an operator can set one. Nothing a
client sends can reintroduce itself into a forwarded header.

### WebSocket & streaming passthrough

The reverse proxy transparently carries long-lived streaming protocols with no
special configuration:

- **WebSocket** — an `Upgrade` request that the backend answers with `101
  Switching Protocols` is hijacked and spliced bidirectionally, so text and
  binary frames flow through untouched. This is the transport behind Apollo
  GraphQL subscriptions (`graphql-ws`) and Socket.IO / engine.io.
- **Server-Sent Events** — `text/event-stream` (and any chunked or
  unknown-length) response is flushed to the client per write rather than
  buffered, so Node/Python SSE endpoints deliver events in real time.

Both are pinned by passthrough conformance tests
(`TestProxyWebSocketPassthrough` drives a real WebSocket echo with text and
binary frames; `TestProxyServerSentEventsStreaming` proves events are streamed,
not buffered). WebSocket upgrades are not available on HTTP/3 listeners (clients
transparently fall back to HTTP/2 — see [HTTP/3 (QUIC)](../docs/configuration.md#http3-quic)).

Both also work with `cache = true` on the same location. An upgrade request
bypasses the cache before lookup and reaches the handler with the untouched
response writer, and an event stream is streamed but never stored; see
[Upgrades, streaming and ResponseWriter transparency](cache.md#upgrades-streaming-and-responsewriter-transparency).
`TestWebSocketThroughCachedProxy` and `TestWebSocketThroughFullMiddlewareChain`
drive a real handshake through a cached route, the second through the complete
production middleware chain.

### Optional `ResponseWriter` interfaces

Every response-writer wrapper in the chain — metrics, access log, tracing,
compression and the response cache — is built with `internal/respwriter`, which
exposes **exactly** the optional interfaces the underlying writer implements:
`http.Flusher`, `http.Hijacker`, `http.Pusher`, `io.ReaderFrom`, plus
`Unwrap() http.ResponseWriter` for `http.ResponseController`.

Neither half of that is optional. Hiding an interface stalls SSE and breaks
upgrades; advertising one the connection does not have is worse, because a
handler branches on the type assertion rather than on the error it gets back. On
**HTTP/2 and HTTP/3 there is no connection to hijack**, and `w.(http.Hijacker)`
correctly fails; `http.NewResponseController(w).Hijack()` returns
`http.ErrNotSupported`. The behaviour is pinned by the 16-combination interface
matrix in `internal/respwriter`.

## FastCGI / uWSGI

| Aspect | FastCGI | uWSGI |
| --- | --- | --- |
| Transport | `gofast` with a small client pool | custom uWSGI packet protocol (modifier1 = 0) |
| Address | `fastcgi_pass` (`unix:…` or `tcp://…`) | `uwsgi_pass` (`unix:…` or `tcp://…`) |
| Connection reuse | pooled | reconnect per request |
| Script name | `scriptNameFor` appends the index file (default `index.php`) for directory requests | n/a |
| Param overrides | `fastcgi_params` (last write wins) | — |

> **SCGI is not implemented.**

### Client address in the CGI environment

| Param | Value |
| --- | --- |
| `REMOTE_ADDR` | the canonical client derived by the listener's [`client_address`](configuration.md#client-address-and-trusted-proxies) policy — the transport peer unless the peer is a trusted proxy |
| `REMOTE_PORT` | the transport port. An address asserted by a proxy carries no port, so this always describes the real connection. |
| `JUL_PEER_ADDR` | the direct transport peer, always available as a separate fact |
| `HTTP_X_FORWARDED_FOR` | **overwritten** with Jul's own trusted chain, exactly as sent to a proxied backend |

The inbound `X-Forwarded-For` is never passed through: the application cannot
tell a trusted chain from an attacker-supplied one, so Jul replaces it with what
it knows rather than laundering client input into the CGI environment.

## Load balancing

Configured per upstream via `strategy`:

| Strategy | Behaviour |
| --- | --- |
| `round_robin` *(default)* | lock-free rotation, ignores weight |
| `weighted_round_robin` | smooth weighted round-robin (NGINX algorithm), proportional to `weight` |
| `least_conn` | fewest in-flight requests |

Each backend has a circuit breaker: after `max_fails` (default 3) consecutive
failures it is taken out of rotation for `fail_timeout` (default 10s), then
tested by at most `circuit_half_open_probes` requests (default 1) before it is
returned to full traffic. There is **no** `ip_hash` / `random` strategy and no
outlier ejection.

## Core middleware

| Middleware | Trigger | Behaviour |
| --- | --- | --- |
| Request ID | always | `X-Request-ID` (incoming preserved or generated) |
| Recover | always | converts a panic to 500; re-panics `http.ErrAbortHandler` |
| Body limit | always | `client_max_body_size`: an oversized declared `Content-Length` is rejected with 413 before the body is read; an unknown length trips via `MaxBytesReader` → 413 |
| Timeout | when configured | `http.TimeoutHandler` → 503 |
| Access log | when a sink is set | structured `slog` access records |
| Rate limit | when enabled | 32-shard token bucket |
| Compression | `compression` build tag | response compression |

## Metrics

jul exposes Prometheus metrics on the admin `/metrics` endpoint from a private
registry (only `jul_*` series plus the standard Go/process collectors). Every
label is **bounded by construction** — the label set below is a policy, enforced
by the machine-readable [metric contract](metrics-contract.json) and regression
tests in `internal/observability`. CI freezes the released `v1.32.0` families,
checks every current name/type/help/label tuple, and fails if documentation or
collectors drift. This keeps Prometheus series counts predictable as the feature
set grows.

### Label cardinality policy

Labels fall into three classes by what bounds them:

- **Fixed** — a small, closed enum fixed in the code (e.g. `code`, `state`,
  `result`, `action`, `proto`, `direction`, `reason`, `encoding`, `key`). These
  cannot grow with traffic or config.
- **Config/topology-bounded** — one series per configured or discovered object
  (`pool`, `backend`, `plugin`, `domain`, `rule`, and the gRPC `method` full
  name). These grow only with your configuration, not with client input; on very
  large or highly dynamic pools/domain sets they are the labels to watch (see the
  [operator playbook](#operator-playbook-keeping-cardinality-bounded)).
- **Client-derived** — values a client can influence. There are exactly two, and
  both are capped by design: the HTTP request `method` (folded to a fixed set,
  see below) and `host` (opt-in, empty by default, see below).

| Metric | Labels | Bound |
| --- | --- | --- |
| `jul_http_requests_total` | `method`, `host`, `code` | method folded to a fixed set · host opt-in · code = status |
| `jul_http_request_duration_seconds` | `method`, `host` | as above (buckets fixed) |
| `jul_http_requests_in_flight` | — | single series |
| `jul_cache_events_total` | `state` | fixed (`HIT`/`MISS`/`STALE`/`REVALIDATED`/`BYPASS`) |
| `jul_cache_revalidations_total` | `outcome` | fixed (`stored`/`not_modified`/`uncacheable`/`origin_error`/`canceled`/`panic`/`no_lease`/`deduplicated`) |
| `jul_http_response_compressed_total` | `encoding` | fixed content codings |
| `jul_http_ratelimited_total` | `key` | fixed (`ip`/`header`/`jwt`) |
| `jul_http_backend_dial_failures_total` | `reason` | fixed (`timeout`/`refused`/`no_backend`/`other`) |
| `jul_client_addr_derivations_total` | `source`, `result` | fixed (3 × 4 enums, never an address) |
| `jul_auth_decisions_total` | `method`, `result` | fixed gate × `allow`/`deny` |
| `jul_waf_events_total` | `action`, `rule` | `block`/`detect` × loaded rule IDs |
| `jul_egress_decisions_total` | `subsystem`, `result`, `reason` | fixed subsystem/result/reason enums |
| `jul_egress_dns_answers_total` | `subsystem`, `result` | fixed subsystem/result enums |
| `jul_upstream_backends_healthy` | `pool` | configured pools |
| `jul_upstream_active_requests` | `pool` | configured pools |
| `jul_upstream_pending_requests` | `pool` | configured pools |
| `jul_upstream_connections` | `pool` | configured pools |
| `jul_upstream_backends_eligible` | `pool` | configured pools |
| `jul_upstream_circuit_state` | `pool`, `state` | pools × 5 backend states |
| `jul_upstream_admission_rejected_total` | `pool`, `reason` | pools × the failure-reason enum |
| `jul_upstream_retry_attempts_total` | `outcome`, `pool` | pools × the retry stop-reason enum |
| `jul_upstream_retry_budget_denied_total` | `pool` | configured pools |
| `jul_upstream_circuit_transitions_total` | `pool`, `to` | pools × 5 backend states |
| `jul_transport_retired_total` | `mode` | `graceful`/`forced` |
| `jul_upstream_backends` | `pool` | configured pools |
| `jul_discovery_errors_total` | `pool` | configured pools |
| `jul_upstream_probes_total` | `pool`, `result` | pools × `success`/`failure` |
| `jul_upstream_probe_duration_seconds` | `pool` | configured pools |
| `jul_grpc_transcode_requests_total` | `method`, `code` | proto methods × status |
| `jul_grpc_transcode_stream_msgs_total` | `method`, `direction` | proto methods × `sent`/`recv` |
| `jul_grpc_proxy_streams_total` | — | single series |
| `jul_plugin_invocations_total` | `plugin`, `result` | configured plugins × `continue`/`stop`/`error` |
| `jul_plugin_duration_seconds` | `plugin` | configured plugins |
| `jul_plugin_panics_total` | `plugin` | configured plugins |
| `jul_listener_conns` | — | single series |
| `jul_http3_connections` | — | single series |
| `jul_stream_active_conns` | `proto` | `tcp`/`udp` |
| `jul_stream_backend_dial_failures_total` | `proto`, `reason` | `tcp`/`udp` × fixed (`timeout`/`refused`/`no_backend`/`other`) |
| `jul_stream_bytes_total` | `proto`, `direction` | `tcp`/`udp` × `up`/`down` |
| `jul_stream_udp_sessions_evicted_total` | `reason` | `idle`/`lru` |
| `jul_stream_udp_sessions_rejected_total` | — | single series |
| `jul_tls_cert_expiry_seconds` | `domain` | configured/served domains |
| `jul_acme_renewals_total` | — | single series |
| `jul_mtls_handshakes_total` | `result` | `verified`/`rejected` |
| `jul_reload_total` | `source`, `outcome` | fixed trigger/outcome enums |
| `jul_reload_duration_seconds` | `source`, `outcome` | fixed trigger/outcome enums; buckets fixed |
| `jul_reload_in_progress` | — | single series |
| `jul_reload_phase_duration_seconds` | `phase`, `outcome` | fixed phase/outcome enums; buckets fixed |
| `jul_reload_timeout_total` | `phase` | fixed reload-phase enum |
| `jul_config_stage_restart_total` | `result` | fixed stage/discard result enum |
| `jul_config_pending_restart` | — | single series |
| `jul_managed_apply_finalized_total` | `operation`, `mode`, `outcome`, `restored` | fixed managed-operation/lifecycle enums |
| `jul_managed_apply_finalization_errors_total` | `component` | fixed finalizer-component enum |
| `jul_managed_apply_history_total` | `operation`, `result` | fixed managed-operation/history-result enums |
| `jul_managed_apply_terminal_registry_entries` | — | single bounded count |
| `jul_managed_apply_terminal_lookup_total` | `result` | fixed lookup-result enum |

Notably absent from every label set — deliberately — are the request **path**,
**query string**, **client IP**, **user-agent**, and raw **Host**: those are
unbounded, client-controlled dimensions. Per-path and per-host detail is instead
exposed through the bounded, in-memory Console projections (Top Failing Routes,
Request Samples, Traffic Sources), never as Prometheus labels.

### The request `method` label is folded to a fixed set

HTTP permits arbitrary request-method tokens, and the method is client-controlled,
so a hostile or buggy client could otherwise mint one `jul_http_requests_total`
series per novel method (times host, times code). jul therefore records only the
standard methods verbatim — `GET`, `HEAD`, `POST`, `PUT`, `PATCH`, `DELETE`,
`CONNECT`, `OPTIONS`, `TRACE` — and folds every other value to a single `other`
series. The `method` label is thus bounded to at most ten values by construction;
no configuration is required.

### The `host` label is opt-in

`host` is recorded on `jul_http_requests_total` and
`jul_http_request_duration_seconds` only when explicitly enabled. The Host header
is client-controlled, so populating it unconditionally lets a flood of distinct
Host values explode metric cardinality (one series per host, per method, per
code) and exhaust scrape memory. By default the label is therefore emitted with
an **empty value**, collapsing every request into one stable series.

Enable it only when the set of hosts is bounded (e.g. a handful of configured
`server_names`):

```toml
[observability.metrics]
host_label = true
```

The setting is read once at startup (like `[observability.tracing]`); a reload
does not change it. If you enable `host_label` on an edge that receives
arbitrary Host headers, pair it with a scrape-time relabel rule that keeps only
known hosts and drops the rest, for example:

```yaml
metric_relabel_configs:
  - source_labels: [host]
    regex: (app\.example\.com|api\.example\.com)
    action: keep
```

### Operator playbook: keeping cardinality bounded

The client-derived labels are safe by default (method is capped; host is
opt-in). The dimension to plan for at scale is the **config/topology-bounded**
group — chiefly `backend` on large or rapidly-churning discovery pools, and
`domain` when serving on-demand ACME certificates for many hostnames. A working
budget: total series ≈ Σ(per-metric label-value products); the request metrics
dominate at roughly `methods (≤10) × hosts (1 unless opted-in) × status codes`.

Guidance:

1. **Leave `host_label` off** on any edge exposed to arbitrary Host headers; rely
   on the Console Traffic Sources panel for per-host visibility instead.
2. **Scrape-drop noisy topology labels** you do not alert on. For example, to
   drop the on-demand TLS expiry gauge for domains you do not track:

   ```yaml
   metric_relabel_configs:
     - source_labels: [__name__, domain]
       regex: jul_tls_cert_expiry_seconds;.+\.example\.net
       action: drop
   ```

3. **Bound on-demand TLS** `domain` growth by dropping the expiry gauge for
   hostnames you do not track, or by keeping only your apex/wildcard domains:

   ```yaml
   metric_relabel_configs:
     - source_labels: [__name__, domain]
       regex: jul_tls_cert_expiry_seconds;(.+\.)?example\.com
       action: keep
   ```

4. **Set a per-target sample limit** as a backstop so a mistaken config cannot
   overwhelm the TSDB:

   ```yaml
   scrape_configs:
     - job_name: jul
       sample_limit: 5000
       static_configs: [{ targets: ["jul.internal:9090"] }]
   ```

The `jul_*` label names are part of the compatibility surface
([compatibility.md](compatibility.md)); a change to the policy table above is a
documented, tested change, so your relabel rules stay stable across upgrades.


## Benchmarks

Representative CPU costs of the hot path (Windows, amd64, Virtual CPU @ 3.41GHz;
numbers are indicative — run `go test -bench` on your own hardware).

| Benchmark | Time/op | Allocs/op |
| --- | --- | --- |
| `BenchmarkHostScore/exact` | ~115 ns | 0 |
| `BenchmarkHostScore/wildcard` | ~103 ns | 0 |
| `BenchmarkMatchLocation/exact` | ~6 ns | 0 |
| `BenchmarkMatchLocation/prefix` | ~39 ns | 0 |
| `BenchmarkMatchLocation/regex` | ~187 ns | 0 |
| `BenchmarkMatchLocation/fallback` | ~96 ns | 0 |
| `BenchmarkBalancerRoundRobin` (8 backends) | ~15 ns | 0 |
| `BenchmarkBalancerWeightedRR` (8 backends) | ~36 ns | 0 |
| `BenchmarkBalancerLeastConn` (8 backends) | ~13 ns | 0 |
| `BenchmarkPoolPick` (full hot path, 4 backends) | ~91 ns | 1 (32 B) |
| `BenchmarkStaticServe` (small file, end to end) | ~450 µs | 53 (7.6 KB) |

Routing and balancer selection are allocation-free. The static-serve number is
dominated by per-request filesystem `open`/`stat` syscalls (filesystem-bound,
not CPU-bound) and varies widely by OS and disk.

Reproduce:

```
go test -run '^$' -bench . -benchmem ./internal/router/ ./internal/upstream/ ./internal/handler/
```

## Security / threat notes

| Threat | Status | Mechanism |
| --- | --- | --- |
| Path traversal | 🟢 safe | static `root` opened with `os.Root`; `..` cannot escape (`TestStaticTraversalBlocked`) |
| FastCGI `SCRIPT_NAME` traversal | 🟢 safe | `scriptNameFor` cleans to an absolute path with no `..` segment (`FuzzScriptName`) |
| SSRF | 🟢 safe by design | `proxy_pass` is static config; no request input selects the upstream target |
| Header injection / CRLF | 🟢 safe | Go `net/http` rejects embedded CR/LF; custom headers use simple variable substitution (no eval) |
| Request smuggling | 🟢 safe | strict `net/http` request parsing |
| DoS (large bodies) | mitigated | `client_max_body_size` → 413 (default unlimited; set per server/location) |
| DoS (slow clients) | partial | `read_timeout` / `write_timeout` mitigate; default unset |

## Limits

- **No SCGI** (FastCGI and uWSGI only).
- **No `ip_hash` / `random`** load-balancing strategies.
- **No `try_files` at the FastCGI level** (`try_files` is static-only).
- **No outlier ejection** — the circuit breaker is consecutive-failure based, so a
  backend failing intermittently while its peers succeed is not ejected.
- **No per-backend rate limiting** (rate limiting is per-listener).
- `read_timeout` / `write_timeout` are **unset by default** (no slow-client
  protection until configured).
- An unmatched request with no `/` fallback returns **501**, by design.

## GA status

Per [ADR 0003](adr/0003-maturity-and-ga.md), Core HTTP is **GA**. The soak test
(criterion 5) was completed on 2026-07-04.

| # | GA criterion | Status |
| --- | --- | --- |
| 1 | Behaviour matrix published | ✅ [host](#virtual-host-matching), [location](#location-matching), [static](#static-file-serving), [proxy](#reverse-proxy) (incl. [WebSocket/SSE passthrough](#websocket--streaming-passthrough)), [FastCGI/uWSGI](#fastcgi--uwsgi), [balancing](#load-balancing) tables |
| 2 | Published benchmark numbers | ✅ [Benchmarks](#benchmarks) (routing, balancing, static serve) |
| 3 | Documented known-limitations | ✅ [Limits](#limits) |
| 4 | Stable config/API contract (semver-guarded) | ✅ [compatibility policy](compatibility.md) (v1 tag at release) |
| 5 | Long-running soak test passed | ✅ soaked 8h windows 2026-07-04 (90.4M req, 0% err) — [evidence](soak-evidence.md#2026-07-04--track-2-extended-burn-in-local-windows-8-hours-50-workers) |
| 6 | Runnable example + docs | ✅ [testdata/static.toml](../testdata/static.toml), [testdata/vhosts.toml](../testdata/vhosts.toml) + this doc |
| 7 | Security / threat note | ✅ [Security / threat notes](#security--threat-notes) |
| 8 | Fuzzing where parsing is involved | ✅ `FuzzHostScore`, `FuzzMatchLocation` (router), `FuzzScriptName`, `FuzzParseSocketAddress` (FastCGI) |
| 9 | Self-explanatory Console surface | ✅ Console **Status** → *Traffic* group reports *Virtual hosts*, *Static file serving*, *Reverse proxy*, *FastCGI / uWSGI* |

All GA criteria are satisfied.

## Build tags

Core HTTP — virtual hosts, location matching, static serving, reverse proxy,
FastCGI/uWSGI, and load balancing — is **core**: it requires no build tags.
Optional middleware (e.g. compression) and protocols (HTTP/3, h2c, gRPC) are
documented separately and gated behind their own tags.

## See also

- [tls-acme.md](tls-acme.md) — TLS termination and automatic HTTPS
- [mtls.md](mtls.md) — mutual TLS / client-certificate authentication
- [compatibility.md](compatibility.md) — config/API stability policy
- [ga-push.md](ga-push.md) — GA hardening tracking log
