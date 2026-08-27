# Jul.IA — Known limitations

This page aggregates both deliberate product limitations and current known
correctness/contract defects. A historical GA label does not make an open
regression harmless or already fixed. The
[combined audit](audit/combined-audit-2026-08-03.md) and linked issues own the
current remediation state; stable feature guides remain the detailed operational
references.

## Current defects and recertification work

- **Response cache:** #131/#132/#133 corrected the confirmed implementation defects and #134 completed the integrated source, protocol, race, benchmark and soak recertification. The cache remains GA. The limitations in the dedicated section below are explicit product, performance, conservative or lifecycle constraints; no residual cache correctness exception is being hidden as a limitation.
- **Access-log lifecycle:** request records can be disabled explicitly, but enablement and sink changes remain restart-required until #98 introduces generation-safe sink replacement.
- **Lifecycle completeness:** #89 will make every public configuration leaf closed-world and generated/checkable.
- **Trust boundaries:** canonical trusted-proxy identity and configurable backend peer trust have shipped ([ADR 0016](adr/0016-inbound-identity-and-backend-peer-trust.md)). The remaining boundaries are documented as deferrals with explicit promotion triggers in that record, not as gaps: proxy-aware Admin identity, QUIC and UDP client preservation, and per-tenant policy.
- **Upstream overload control:** the bounded model decided by [ADR 0017](adr/0017-upstream-resilience-and-overload-control.md) is not yet implemented. Until #141–#144 land, the running binary has: no cap on concurrent upstream requests or on physical backend connections (`MaxConnsPerHost` is unset); no retry budget, overall retry deadline or backoff between attempts; no bound on how many requests probe a backend the instant its `fail_timeout` cooldown elapses; no load balancing, health checking or failure accounting for `fastcgi_pass` and `uwsgi_pass`, whose connection count is unbounded; no connection cap on L4 TCP routes (UDP already has `max_udp_sessions`); and no circuit breaker on the forward-auth and JWKS subrequests, which carry a fixed 10s timeout.
- **Routing and response policy:** the request-matching half of
  [ADR 0018](adr/0018-bounded-route-matching-and-response-policy.md) has shipped (#145): a location
  can be constrained by method, request header and query parameter. The response half has not.
  Until #146 lands there is no way to add, replace or remove a response header, and no CORS or
  preflight handling of any kind. `[servers.locations].headers` sets headers on the *upstream
  request*, not on the response to the client, and a WASM middleware plugin is the only way to set
  a response header today.

---

## Route matching ([configuration.md](configuration.md#request-predicates))

- **The Boolean model is deliberately small.** A list inside one field is an
  OR-set and everything else is ANDed. There is no negation, no grouping, no OR
  across fields, no expression language, no body or cookie matching, and no
  weighted, canary or mirrored routing. These are D12 exclusions, not oversights.
- **There is no automatic 405 and no `Allow` header.** A method mismatch makes a
  route non-matching, and a final no-match is the router's ordinary 404. `Allow`
  is a property of the resource, and a gateway route enumerating `["GET"]` says
  nothing about whether the upstream implements POST. Use the route-test surface
  for "the path matched but the method did not" diagnostics.
- **A predicate mismatch is never logged per request**, and no predicate value
  ever becomes a metric label. The route-test surface is the diagnostic.
- **Query predicates have no `regex` operator** in this tranche, and only the
  first 1024 query pairs of a request are parsed.
- **`cors.enabled` widening is implemented but unreachable.** ADR 0018 §2 widens
  a `methods` predicate to accept that location's own CORS preflight. The
  matcher implements it and the policy-scope fingerprint reserves the bit, but
  the `[servers.locations.cors]` block that turns it on arrives with #146, so no
  configuration can set it yet.
- **`prefix "/"` is consulted after the regex tier, not folded into the prefix
  tier.** ADR 0018 §6 specifies the fold on the stated grounds that a
  length-1 prefix "behaves exactly as the current `sr.fallback` does". It does
  not: the fallback was consulted *after* regex, so folding it in would let a
  `location /` shadow every regex route. The implementation keeps the catch-all
  in its own tier, and the record was amended to match; see the differential gate in
  [`internal/router/precedence_diff_test.go`](../internal/router/precedence_diff_test.go).

---

## Configuration reload ([reload-semantics.md](reload-semantics.md))

- **Some fields cannot be hot-reloaded.** The authoritative classification is
  [`internal/lifecycle/lifecycle.go`](../internal/lifecycle/lifecycle.go) and
  the machine-readable copy in [config-lifecycle.yaml](config-lifecycle.yaml).
  Restart-required fields are rejected by the admin apply path and by
  SIGHUP/file-watch reloads; the new value is saved but takes effect only after
  a process restart. New-listener-only fields (e.g. a different listen address)
  apply to brand-new listeners on reload, but changing them on an already-bound
  listener requires restart.
- **Reload is Publish-then-Activate.** New listeners are bound during staging
  but do not accept traffic until after the new handler generation is
  published, so clients never see requests routed by a listener before the
  handlers that should serve them are live. The transaction is implemented in
  the `ReloadPlan` value in [`internal/server/reload_plan.go`](../internal/server/reload_plan.go).
- **In-flight requests stay on their generation's pool snapshot.** When an
  upstream pool changes, active requests that started on the previous
  generation continue to use the old backend set until they drain; new
  requests use the updated pool. This prevents a request from observing
  backends that were added or removed after it began.
- **Restart-required changes on saved-but-not-yet-active config are surfaced
  by the Console.** `PendingRestartCheck` compares the startup-bound
  effective values (including digests of file-backed secrets) against the
  current on-disk config, so operators see a banner when a saved change needs
  a restart.

---

## Client address derivation ([configuration.md](configuration.md#client-address-and-trusted-proxies))

- **The admin listener keeps peer-only identity, by design.** Admin rate
  limiting, SSE connection caps and audit `SourceIP` always describe the direct
  transport peer. Making the highest-privilege surface's attribution depend on
  an operator-editable CIDR list would be a downgrade, so `client_address` does
  not apply there.
- **`[[stream]]` L4 proxying derives identity separately from HTTP.** A stream
  listener's Boundary A is always the socket peer; an inbound PROXY-protocol
  header is an assertion (Boundary B) and is believed only from a declared
  `trusted_proxies` entry, with a connection from outside that set refused
  rather than degraded. The result never feeds the HTTP canonical identity, and
  a chain there is a single hop, so `max_hops` and header precedence do not
  apply. See [ADR 0016 §6b](adr/0016-inbound-identity-and-backend-peer-trust.md).
- **The policy is per listen address, not per virtual host.** Every
  `[[servers]]` block sharing a `listen` must declare the same effective policy.
  This is deliberate: identity is derived before the `Host` header is read, so a
  per-vhost policy could be selected by the attacker.
- **No `X-Real-IP`, no CIDR shorthands, no PROXY protocol on HTTP listeners.**
  A single-address header cannot be evaluated against a trust boundary, and
  shorthands such as `private` or `rfc1918` encourage over-broad trust.
- **No chain projection.** The identity carries the canonical client, the direct
  peer, and bounded source/result enums — not the full asserted chain.
- **Outbound forwarding is deliberately lossy.** Jul emits
  `X-Forwarded-For: <canonical client>, <direct peer>`, so intermediate trusted
  proxies are dropped: `client, P1` received from `P2` is forwarded as
  `client, P2`. Jul is the last hop before the backend, and replaying a
  third-party chain into a channel the backend authenticates would be worse than
  losing it. Restoring full fidelity later is additive.
- **The access log has no `remote` field.** It is replaced by `client_ip` and a
  conditional `peer_ip`. Log consumers that parsed `remote` must be updated;
  there is no compatibility alias, because keeping one would have shipped a
  permanently ambiguous field.

---

## Backend TLS trust ([upstreams.md](upstreams.md#backend-tls))

- **Health probes use the *pool's* policy, not a route's.** A route-level
  `backend_tls` override governs that route's traffic only; a pool may serve
  several routes with different overrides, so no single one could govern its
  probe. Put the trust roots a probe needs on the upstream.
- **A policy change rebuilds the pool.** Making the resolved policy part of the
  pool's identity is what lets the probe client adopt it, but it also means the
  pool's balancer and health state restart — the same behaviour a health-check
  settings change has always had. Adding or removing a backend applies on the
  reload; editing the policy of one that survives it rebuilds the pool.
- **No named, reusable TLS profiles.** Two pools that share a trust bundle
  repeat the block. Every consumer takes the resolved policy type, so named
  profiles remain an additive change to resolution rather than a transport
  rewrite; the promotion triggers are recorded in ADR 0016.
- **Health probes verify against the system roots only.** An `https` pool behind
  a private CA is reported unhealthy until probes consume the resolved policy;
  `jul lint` warns about the combination.
- **`insecure_skip_verify` disables verification, not encryption.** It exists as
  an emergency path: `jul lint` fails on it, the server warns once per backend
  at startup, and it cannot be combined with `peer_identities` or a non-system
  `ca_mode`.

---

## Authentication ([auth.md](auth.md))

- **One credential method per location.** Basic, JWT, forward-auth, or CIDR —
  not combined. Use multiple layers (mTLS + JWT) at different paths if you need
  defence-in-depth.
- **No OAuth2 / OIDC flows.** No opaque-token introspection, no cookie sessions,
  no refresh tokens. JWT bearer with JWKS validation is the supported pattern.
- **Application authorization remains upstream-owned.** Route authentication forwards validated identity/claims but does not implement arbitrary application scopes. The admin control plane separately ships opt-in local named-principal RBAC; external OIDC/SAML/SCIM identity is not available.
- **One issuer/audience per location.** Multi-issuer validation is not supported.
- **Forward-auth is a GET probe.** It mirrors the Traefik / NGINX `auth_request`
  pattern — not a full OAuth2 exchange.

---

## Response cache ([cache.md](cache.md))

- **No tag or pattern purge.** The admin API purges a single exact key or the
  entire cache. There is no prefix, host, or tag-based purge (e.g. all
  `/api/v1/*`). Use short TTLs or application-layer cache busting for
  selective invalidation.
- **No cached byte-range serving.** Every request carrying `Range` or `If-Range`
  bypasses the cache and reaches the origin (`X-Cache: BYPASS`), and no `206` is
  ever stored. Range workloads get no cache benefit. Serving single byte ranges
  from complete cached representations is a recorded future enhancement, not an
  accidental gap.
- **`Set-Cookie` responses are never stored**, whatever their `Cache-Control`
  says. An origin cannot currently opt a cookie-bearing response into shared
  caching.
- **A response generated for a request carrying `Authorization` is stored only
  with `public` or `s-maxage`.** RFC 9111 §3.5 also lists `must-revalidate` as a
  *reuse* permission, which Jul honors — but not as permission to publish an
  authenticated response into a cache anonymous clients read.
- **Cookie-based sessions are not protected by the authenticated-reuse rule.**
  RFC 9111 §3.5 is defined in terms of `Authorization`; a `Cookie` header is not
  a shared-cache authentication signal. An origin serving user-specific content
  behind a cookie must set `private` or `no-store`.
- **A `304` that changes `Vary` discards the representation** and the request
  re-fetches, rather than rekeying it in place. Discarding is the outcome that
  cannot leave a wrongly-keyed entry reachable.
- **A `Vary` pointer entry written before the variant-membership record fails
  closed.** After upgrading, the first request for each previously cached `Vary`
  URL is a miss. Nothing is lost and nothing stale is served.
- **Variant membership is capped at 64 per base resource.** Past the cap the
  oldest variant is deleted, so a resource with more than 64 live variants will
  churn.
- **Mandatory validation buffers the origin's answer** instead of streaming it,
  because the status must be known before deciding whether to serve the stored
  body. An answer larger than `memory_max_size`, or one that turns out to be a
  stream, costs one extra origin request and is then streamed normally.
- **Disk tier is best-effort.** A disk-write failure does not error the request;
  the entry serves from memory or the upstream.
- **No distributed / shared cache.** Cache state is local to the single Jul.IA
  process. Multi-node setups each maintain independent cache pools.
- **A reload does not invalidate cached entries.** The cache is process-scoped
  and deliberately survives configuration reloads, so a routing or backend change
  does not retroactively drop entries stored under the previous configuration. A
  background refresh that was already in flight when the reload ran also
  completes against the *old* generation's route and publishes its result. Purge
  or restart when a configuration change must invalidate content.
- **A background refresh or a synchronous validation delays retirement of its
  generation.** While either runs, the handler generation that started it keeps
  its gRPC connections, plugin runtimes and static roots open. The delay is
  bounded: the work is cancelled and the resources closed after `[global]
  shutdown_timeout`.
- **Background refresh requires the server's generation-lease seam.** The
  production wiring always installs it. A cache embedded without that seam serves
  stale responses normally but starts no background refresh — unowned work would
  have no resource holder and no shutdown owner. The decision is counted as
  `no_lease` on `jul_cache_revalidations_total`. Mandatory validation without the
  seam degrades to a complete origin fetch; it never degrades to serving an
  unvalidated entry.
- **Server-Sent Events are never cached.** A `text/event-stream` response is
  streamed to the client but never stored, because an event stream has no end
  and capturing it would only grow a buffer that is discarded. Ordinary chunked
  responses, which the reverse proxy also flushes on every write, are still
  cached normally.
- **A cached route cannot hijack on HTTP/2 or HTTP/3.** That is a protocol fact,
  not a cache limitation: there is no connection to take over. The response
  writer reports it honestly by not implementing `http.Hijacker`, and
  `http.ResponseController.Hijack` returns `http.ErrNotSupported`. WebSocket
  upgrades therefore use HTTP/1.1, as they do without the cache.

---

## Compression ([compression.md](compression.md))

- **Response body only.** Request body decompression is not implemented.
- **BREACH / CRIME advisory.** Do not compress responses that contain
  authenticated secrets (e.g. CSRF tokens) alongside attacker-controlled
  reflected input. See the threat note in [compression.md](compression.md).

---

## gRPC ↔ JSON transcoding ([grpc-transcoding.md](grpc-transcoding.md))

- **No `response_body` field selection.** The full reply proto message is
  rendered; mapping a single field to the HTTP body is not implemented.
- **Maps not settable from path/query parameters.** Map fields can only be
  populated from the JSON request body.
- **No field masks.** `google.protobuf.FieldMask` is not applied on
  transcoded responses.
- **Server reflection requires a live upstream.** When `use_reflection = true`,
  the descriptor is fetched from the upstream at startup; a down upstream
  prevents the location from initialising.
- **RPCs may be cut at the retired-connection grace boundary.**
  Dynamic upstream churn retires removed backend connections for 30 seconds so
  in-flight RPCs can drain. RPCs that outlast that grace period may be
  interrupted when the retired connection is closed; this primarily affects
  long-lived streams and unusually long unary calls. Clients should be prepared
  to reconnect.

---

## Native gRPC passthrough ([grpc-proxy.md](grpc-proxy.md))

- **gRPC settings require a restart.** Like TLS, the h2c flag is set at bind
  time; changing `h2c = true` or `grpc = true` on a running server requires a
  restart.
- **No gRPC-Web support.** Clients using the `grpc-web` framing protocol need a
  separate transcoder; Jul.IA passes native gRPC (HTTP/2 + proto framing) only.

---

## HTTP/3 over QUIC ([http3.md](http3.md))

- **No WebSocket over HTTP/3 — or over HTTP/2.** Jul.IA implements the HTTP/1.1 `Upgrade` mechanism
  only. WebSocket over HTTP/2 and HTTP/3 uses extended `CONNECT` (RFC 8441 / RFC 9220), which Jul.IA
  does not implement. Go's bundled HTTP/2 server keeps extended `CONNECT` behind
  `GODEBUG=http2xconnect=1` for a reason that applies directly here: advertising it makes browsers
  *stop* sending HTTP/1.1 `Upgrade` and start sending extended `CONNECT`, which then fails against a
  server whose WebSocket path does not implement it. A WebSocket upgrade over HTTP/3 will be
  rejected. Browsers fall back to an HTTP/1.1 connection for the WebSocket, so nothing is
  unreachable.
- **HTTP/3 settings require a restart.** The QUIC listener is built at bind
  time; changes to `[servers.http3]` take effect only after a full restart.
- **QUIC path MTU discovery.** Some networks drop oversized UDP packets; QUIC
  PMTUD mitigates this, but a few firewall configurations may block or
  rate-limit QUIC traffic, causing clients to fall back to TCP.

---

## Rate limiting ([ratelimit.md](ratelimit.md))

- **Local only.** Rate-limit state is per-process. A fleet of Jul.IA nodes
  each enforce their own bucket independently — there is no distributed token
  bucket.
- **Trusted-proxy identity is not yet first-class.** IP-keyed limiting uses the direct transport peer. Do not switch security identity to an arbitrary forwarding header; canonical trusted-proxy chain handling is tracked by #115, #135 and #136.
- **In-memory only.** Rate-limit state is lost on restart; token buckets reset.

---

## L4 stream proxy ([stream.md](stream.md))

- **No application-layer inspection.** TCP/UDP relay is byte-for-byte; Jul.IA
  cannot read HTTP headers, terminate TLS (only SNI passthrough), or parse
  wire protocols.
- **SNI routing reads only the first TLS record.** A ClientHello that spans
  multiple records or omits SNI falls back to the default route.
- **UDP sessions are memory-backed.** Spoofed source addresses can fill the
  session table up to the configured cap; monitor `jul_stream_active_conns{proto="udp"}`.
- **No UDP load balancing.** UDP streams have a single backend per listener;
  multi-backend round-robin is TCP only.

---

## Service discovery ([service-discovery.md](service-discovery.md))

- **Keep-last-good preserves stale backends indefinitely.** A permanently down
  provider leaves the pool with the last-known backend set. Pair with active
  health checks to eject unreachable backends; monitor
  `jul_discovery_errors_total`.
- **DNS TTL is not respected.** Jul.IA polls on a fixed `refresh` interval
  regardless of TTL; fast DNS changes propagate only when the poll fires.
- **Kubernetes discovery requires a token.** The `kubernetes` tag needs a
  service-account token with EndpointSlice read permission; misconfigured RBAC
  returns an empty pool without an obvious error.

---

## WAF ([waf.md](waf.md))

- **Body inspection adds latency and memory.** Coraza buffers the full request
  (and optionally response) body to inspect it. Large uploads or responses
  consume proportional memory. Tune `request_body_limit` and
  `response_body_check` for your workload.
- **Detect mode does not block.** Requests in `detect` mode always reach the
  upstream; WAF findings are logged/metrics-only.
- **CRS rule updates require a rebuild.** The OWASP Core Rule Set is embedded
  in the binary; updating it requires rebuilding with the `waf` tag.
- **SecLang parser errors fail closed at startup.** A malformed directive file
  prevents the WAF engine from building, and the server refuses to start so the
  misconfigured location cannot be exposed without protection.

---

## WASM plugins ([plugins.md](plugins.md))

- **Request phase only.** The v1 ABI has no `handle_response` export; response
  inspection or mutation after the upstream responds is not possible.
- **No shared state across plugin names.** Each plugin name has its own wazero
  runtime and KV namespace; two plugins cannot share memory or KV keys even if
  they load the same `.wasm` file.
- **No streaming bodies.** The host buffers the full request body before passing
  it to the guest; very large bodies increase per-request memory pressure.
- **WASM binary must be pre-compiled.** There is no JIT — modules are compiled
  at startup, not on first use. A malformed WASM binary will prevent the server
  from starting.
- **Fetch capability requires allow-list.** Plugins with `fetch = true` can only
  call hosts in `allowed_hosts`; unconstrained outbound fetch is not available.

---

## Egress allow-list ([egress.md](egress.md))

- **Startup-bound.** The `[egress]` policy is built once from the startup config;
  changing `enabled`/`allow` takes effect only after a **restart** (it is
  restart-required, staged through `stage_restart`).
- **Auxiliary fetches only.** It guards the server's own config-driven fetches
  (JWKS, forward-auth, discovery, ACME/OCSP, plugin `fetch`). The **data-plane
  reverse proxy** — upstream proxying and active health checks — is out of scope.
- **DNS/DNS-SRV discovery is not guarded.** Those use the system resolver rather
  than an HTTP client.
- **Trust-by-name.** A host listed by name is resolved normally; DNS rebinding of
  an explicitly name-trusted host is out of scope — use CIDR entries for IP-level
  enforcement.
- **Port is not part of a host rule.** A name-allowed host is reachable on any
  port.

---

## NGINX importer ([nginx-importer.md](nginx-importer.md))

- **`include` is not followed.** The importer processes a single file; split
  configs must be concatenated or imported individually.
- **`stream`, `mail`, and Lua are not translated.** Module-specific directives
  are reported as untranslated and must be ported manually.
- **Many directives are not mapped.** `add_header`, `proxy_set_header`,
  `client_max_body_size`, `autoindex`, and others are reported for manual
  porting. Translation is a best-effort aid, not a 1:1 converter.

---

## Admin console — single shared token by default

- **Single shared token by default; RBAC is opt-in.** The **default** admin
  model is a single shared bearer token that grants full access with no per-user
  attribution. Named-principal RBAC — roles, scoped revocable tokens,
  least-privilege enforcement, and per-principal audit attribution — is now
  available as an **opt-in `[admin.rbac]` layer** (HP-02 / ADR 0010); enable it
  for multi-user workflows. External identity (OIDC/SSO) remains a Y3-02 horizon
  item. See [docs/specs/console-rbac.md](specs/console-rbac.md) and the migration
  steps in [docs/console.md](console.md).

---

## Single-node only

- **No fleet management.** Jul.IA operates as a single-node server. Multi-node
  config sync, staged rollout, and a fleet control plane are demand-gated
  roadmap items (Year 3). See [docs/roadmap/README.md](roadmap/README.md).
- **No distributed rate limiting or cache.** Rate-limit buckets and response
  cache are per-process. A multi-node setup requires an external shared store
  (demand-gated Year 3).

---

## See also

- [docs/status.md](status.md) — full GA criteria matrix with per-feature
  evidence
- [docs/compatibility.md](compatibility.md) — versioning and stability policy
- [docs/roadmap/README.md](roadmap/README.md) — what is planned, what is
  deferred, and what is demand-gated
