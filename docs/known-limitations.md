# Jul.IA — Known limitations and current correction notices

This page aggregates bounded product limitations and current known defects so
evaluators can see the full operational picture without reading every feature
document.

> **Current truth notice (2026-08-03).** Some entries below are correctness,
> security or product-contract defects under active remediation; they are not
> intentional scope decisions. The authoritative current register is
> [current-product-truth.md](current-product-truth.md) and the
> [combined audit](audit/combined-audit-2026-08-03.md).

A feature may remain useful while a specific defect or lifecycle limitation is
open. Do not interpret a historical GA label as evidence that an open regression
is harmless or already fixed.

---

## Immediate correctness and security notices

- **HTTP/3 server-level mTLS parity is under correction (#121).** Do not rely on
  QUIC for a listener whose security boundary requires server-level client
  certificates. Disable HTTP/3 on that listener or terminate QUIC in a trusted
  front end until the fix is deployed.
- **ACME challenge selection is not currently a proven exclusive runtime switch
  (#122).** Do not use `challenge = "http-01"` or `"tls-alpn-01"` as a guarantee
  that the other challenge seam is absent.
- **The response cache is under a correctness recertification programme
  (#107, #131-#134).** Open work covers generation-owned revalidation,
  immutable entries, shared-cache semantics, invalidation, `304` metadata,
  Range bypass and transparent WebSocket/stream wrappers.
- **Unknown and invalid configuration values do not yet have the intended
  closed-world failure behavior on every entry path (#120, #123).** Validate
  candidates with `jul check` and review preview output carefully.
- **Compression does not yet have complete `Cache-Control: no-transform`
  enforcement (#125).** Avoid compression on representations that depend on
  that directive until the fix is deployed.
- **Prometheus code and documentation are being reconciled against the released
  contract (#126).** Pin dashboards to the released metric names and labels.

---

## Configuration reload ([reload-semantics.md](reload-semantics.md))

- **Some fields cannot be hot-reloaded.** The current classification is
  implemented in [`internal/lifecycle/lifecycle.go`](../internal/lifecycle/lifecycle.go)
  and mirrored in [config-lifecycle.yaml](config-lifecycle.yaml). #89 will make
  that model closed-world and generated/checkable. Until then, an unlisted field
  must not be assumed hot.
- **Restart-required changes use the planned-restart path.** Structural settings
  such as listener protocol mode, startup-owned providers and several
  process-lifetime resources may deliberately remain restart-bound.
- **Reload is Publish-then-Activate.** New listeners are staged before handler
  publication and activated afterward through `ReloadPlan`.
- **In-flight requests stay on their generation's resources.** New requests use
  the candidate generation; old requests may finish with their captured pool,
  handler and other generation-owned resources.
- **Mixed candidates are not partially applied.** A complete candidate is hot,
  staged or rejected as one operation; a hot subset is not silently published
  while another field remains pending restart.
- **Pending restart is surfaced by the Console.** File-backed effective values
  and startup fingerprints are compared against the running state.

---

## Authentication ([auth.md](auth.md))

- **One request-authentication method per location.** Basic, JWT, forward-auth,
  or CIDR are not combined within one location. mTLS can provide a separate TLS
  layer.
- **No OAuth2 / OIDC flows.** No opaque-token introspection, browser sessions or
  refresh-token flow is built in.
- **Application authorization remains upstream-owned.** Route authentication
  validates caller credentials but does not implement arbitrary application
  scopes/policies for the proxied service.
- **Admin RBAC is shipped and opt-in.** `[admin.rbac]` supports named
  principals, roles, scoped revocable tokens and audit attribution. External
  identity (OIDC/SAML/SCIM) and interactive token management remain future work.
- **One issuer/audience per JWT location.** Multi-issuer validation is not
  supported.
- **Forward-auth is a GET probe.** It follows the `auth_request` pattern rather
  than implementing a complete OAuth exchange.

---

## Response cache ([cache.md](cache.md))

> **Current defect notice:** the current cache documentation and historical GA
> matrix overstate the implementation while #107 remains open.

- **Avoid cache on WebSocket/upgrade routes.** Response-writer capability
  preservation and `101 Switching Protocols` bypass are being corrected in
  #133.
- **Treat authenticated/shared-cache behavior conservatively.** Authorization,
  `no-cache`, `must-revalidate`, `proxy-revalidate`, unsafe-method invalidation
  and `304` metadata merging are being completed in #132.
- **Background revalidation and shared entries are being made generation-safe
  and immutable.** Until #131, do not treat repeated reload/revalidation stress
  as recertified.
- **Range/If-Range will bypass cache in the corrected first tranche.** Cached
  multipart/partial-range serving is not part of the current completeness
  target.
- **No tag or pattern purge.** Purge is exact-key or full-cache only.
- **Orphaned `Vary` variants are not auto-cleaned.** Old variants remain until
  expiry or eviction.
- **Disk tier is best-effort.** A disk-write failure does not fail the client
  request.
- **No distributed cache.** State is local to one process.

---

## Compression ([compression.md](compression.md))

- **`no-transform` enforcement is under correction (#125).** Do not compress
  responses that depend on `Cache-Control: no-transform` until the fixed build
  is deployed.
- **Response body only.** Request body decompression is not implemented.
- **BREACH / CRIME advisory.** Do not compress authenticated secrets alongside
  attacker-controlled reflected input.

---

## gRPC ↔ JSON transcoding ([grpc-transcoding.md](grpc-transcoding.md))

- **No `response_body` field selection.** The complete reply message is
  rendered.
- **Maps are not settable from path/query parameters.** Use the JSON body.
- **No field-mask application.** `google.protobuf.FieldMask` is not applied to
  responses.
- **Reflection needs a live upstream.** Descriptor acquisition can fail when
  the reflection target is unavailable.
- **RPCs may be cut at the retired-connection grace boundary.** Very long calls
  or streams should reconnect after backend-generation retirement.

---

## Native gRPC passthrough ([grpc-proxy.md](grpc-proxy.md))

- **h2c/listener protocol settings are restart-bound.** Dynamic retained-address
  protocol transitions are gated architecture work (#105).
- **No gRPC-Web support.** Jul.IA handles native gRPC framing only.

---

## HTTP/3 over QUIC ([http3.md](http3.md))

- **Server-level mTLS parity is not currently proven (#121).** HTTP/3 must be
  disabled on listeners whose trust boundary depends on handshake-level client
  certificates until the fix is deployed.
- **No WebSocket over HTTP/3.** Use HTTP/1.1 or HTTP/2 for WebSocket upgrades.
- **HTTP/3 listener enable/disable remains restart-bound.** The dynamic
  advertisement/max-age subset is separately tracked by #161; the UDP listener
  transition remains gated in #102.
- **QUIC may be blocked or rate-limited by networks.** Clients need TCP fallback.
- **HTTP/3 requests use the current dynamic request-handler generation.** The
  open defect is TLS/client-auth parity, not stale route-handler publication.

---

## Rate limiting ([ratelimit.md](ratelimit.md))

- **Local only.** Buckets are per process and reset on restart.
- **Canonical trusted-proxy identity is not yet implemented.** IP-keyed policy
  currently starts from the direct connection peer. Do not trust arbitrary
  forwarding headers as client identity. The accepted trusted-proxy model is
  tracked by #115, #135 and #136.
- **In-memory only.** There is no distributed bucket store.

---

## L4 stream proxy ([stream.md](stream.md))

- **No application-layer inspection.** TCP/UDP relay is byte-for-byte; TLS is
  passthrough/SNI routing rather than termination.
- **SNI routing reads only the first TLS record.** Multi-record ClientHello or
  missing SNI uses the default route.
- **UDP sessions are memory-backed.** Monitor the configured cap and active
  session metric.
- **No UDP load balancing.** UDP listeners have one backend; multi-backend
  strategies are TCP-only.

---

## Service discovery ([service-discovery.md](service-discovery.md))

- **Keep-last-good can preserve stale backends indefinitely.** Pair it with
  health checks and monitor discovery errors.
- **DNS TTL is not respected.** Refresh uses the configured polling interval.
- **Kubernetes discovery requires EndpointSlice permissions.** Authentication
  and RBAC failures must be diagnosed from discovery status/logging.

---

## WAF ([waf.md](waf.md))

- **URI/query logging is under security review (#127).** Avoid secrets in URLs
  and restrict log access/retention.
- **Body inspection adds latency and memory.** Tune request/response limits.
- **Detect mode does not block.** It records findings while allowing traffic.
- **CRS updates require a rebuild.** Rules are embedded.
- **SecLang parse errors fail startup.** A malformed policy does not expose the
  location without the configured protection.

---

## WASM plugins ([plugins.md](plugins.md))

- **Request phase only.** The v1 ABI has no response-phase hook.
- **No shared state across plugin names.** Runtimes/KV namespaces are isolated.
- **No streaming bodies.** Request bodies are buffered for the guest.
- **Modules are compiled during startup/generation build.** Invalid modules
  reject the candidate.
- **Fetch requires both plugin-local and global policy.** `allowed_hosts`, SSRF
  checks and optional global egress policy all apply.

---

## Egress allow-list ([egress.md](egress.md))

- **Startup-bound.** Changing `[egress]` requires a restart. The optional live
  transition is gated because every client, worker and pooled connection must
  move coherently (#94).
- **Auxiliary fetches only.** It guards JWKS, forward-auth, discovery, ACME/OCSP
  and plugin fetch; data-plane reverse proxy and active health are separate.
- **DNS/DNS-SRV resolver behavior is separate.** Those paths do not use the
  guarded HTTP client.
- **Name trust and CIDR trust differ.** Use CIDR rules for IP-level policy.
- **Port is not part of a host rule.** A name rule does not constrain port.

---

## NGINX importer ([nginx-importer.md](nginx-importer.md))

- **Current import is a best-effort subset.** It is not a cutover
  certification.
- **Include traversal and durable source provenance are not complete.** The
  assessment/provenance programme is #152-#153.
- **Many directives are unsupported or approximate.** Security-significant
  omissions must be reported as blocking by the new assessment model.
- **No compatibility percentage is authoritative.** #154 will compare only
  explicit selected dimensions in sanitized E2E fixtures.

---

## Admin console and control plane

- **Single shared token by default; local RBAC is opt-in and shipped.** Use
  `[admin.rbac]` for named principals and least privilege.
- **No external identity provider.** OIDC/SAML/SCIM and browser-login flows are
  future work.
- **Admin listener enable/address is structural.** Dynamic self-disable or
  relocation is gated in #97; planned restart remains the supported path.
- **Configuration authority is currently managed behavior.** Explicit
  `managed`/`file_owned` modes and external automation contracts are planned in
  #118 and #148-#151.

---

## Single-node only

- **No fleet management.** Jul.IA is a standalone single-node server.
- **No distributed rate limiting, cache, circuit or configuration state.** A
  multi-node deployment needs external orchestration/shared infrastructure.
- **AI Gateway is a gated experiment, not the active next phase.** See #162.

---

## See also

- [Current product truth](current-product-truth.md)
- [Combined repository audit](audit/combined-audit-2026-08-03.md)
- [Feature status](status.md)
- [Compatibility policy](compatibility.md)
- [Current roadmap programme](https://github.com/victornife/jul/issues/62)
