# ADR 0016 — Inbound client identity and backend peer-trust boundaries

- **Status:** Accepted
- **Date:** 2026-08-12
- **Deciders:** Jul.IA maintainer
- **Applies to:** inbound client-identity derivation, trusted-proxy policy, CIDR authentication, IP rate limiting, WAF source address, access logs, upstream forwarding headers, FastCGI environment, backend HTTP/gRPC/transcoding TLS, active health checks, service discovery
- **Source:** #115 (`[ADR][CGC-02]`), source-level design review of `66c71b2d` and current `main`
- **Related:** [ADR 0013](0013-project-operating-model-and-completeness.md) (portfolio entry), [ADR 0011](0011-reload-plan.md) (reload transaction), [ADR 0014](0014-operability-surfaces.md) (operability surfaces)

## Context

Jul defaults every IP-based security decision to the real transport peer and never implicitly trusts
`X-Forwarded-For`. That is correct for direct deployments and incomplete behind a reverse proxy: CIDR
authentication, rate-limit keys, WAF rules and access logs all describe the proxy rather than the
client, and operators have no safe way to say otherwise.

Separately, outbound data-plane connections use language defaults. `newProxyTransport` sets no
`TLSClientConfig` at all, native gRPC dials with a nil `tls.Config`, transcoding hard-codes
`MinVersion: tls.VersionTLS12`, and active health checks skip verification by documented design. There
is no way to express a private trust root, a backend client certificate, an SNI override for a
discovery-returned IP, or a required peer identity.

Both gaps are about *identity*, but they are proved by different means and fail in different
directions. Deciding them together — and keeping them apart — is the point of this record.

Without an explicit model: operators reach for unsafe header-derived rate-limit keys; logs, auth and
the WAF disagree about who the client is; discovery-backed addresses silently become their own TLS
identity; health checks and live traffic can use different trust; and any future provider transport
invents a third security model.

## Decision

### 1. Five trust boundaries, which may not be collapsed

| | Boundary | Question | Proof | Failure |
| --- | --- | --- | --- | --- |
| **A** | Immediate transport peer | *What is the socket?* | Kernel fact | Cannot fail — ground truth |
| **B** | Asserted original client | *Who does A claim came before it?* | A ∈ `trusted_proxies` | Degrade to A |
| **C** | Auxiliary egress authorization | *May Jul connect outbound at all?* | Destination ∈ allow-list | Refuse the dial |
| **D** | Data-plane backend selection | *Which address do I dial?* | Routing / load balancing / discovery | Retry or eject |
| **E** | Backend peer identity | *Is that address the intended service?* | Certificate chain + name binding | Refuse the handshake |

These are five different verbs — **observe**, **derive**, **authorize**, **select**, **authenticate** —
and a single generic `trusted` flag would have to mean all of them at once. Each collapse is a known
vulnerability class:

- **A ∪ B** is header spoofing. Losing A destroys the fallback anchor, the audit trail, and the ability
  to express *"only accept connections from my proxy network"* separately from *"this end user is in
  this range"*.
- **C ∪ E** is a category error. An egress allow-list authorizes a *destination*; it does not
  authenticate a *peer*. Permitting `10.0.0.0/8` says nothing about which of sixteen million hosts
  answered.
- **D ∪ E** is discovery poisoning. If selection implied identity, a compromised registry would rewrite
  both the address and the name it is verified against, and TLS would succeed against the attacker.
- **B ∪ E** is transitive trust. `trusted_proxies` changes far more often than certificates; one flag
  would let widening the inbound proxy list silently weaken backend verification.

They also differ in blast radius and ownership: A and B are per-listener and change weekly, C is
process-global, E is per-upstream and tied to certificate lifecycle. One flag would force the union of
three change frequencies onto the most-edited surface.

Boundary C is already implemented by `internal/egress` and is unchanged by this record.

### 2. D09 — inbound identity: public configuration

```toml
[[servers]]
listen = ":443"

[servers.client_address]
trusted_proxies   = ["10.0.0.0/8", "2001:db8:100::/48"]
forwarded_headers = ["forwarded", "x-forwarded-for"]
max_hops          = 16
```

Defaults: no trusted proxies; `max_hops = 16`; `forwarded_headers` defaults to the order shown.

`client_identity` was rejected as a block name. "Client identity" already denotes three other things in
Jul — mutual-TLS certificates (`$ssl_client_*`, `docs/mtls.md`), JWT claims, and RBAC principals.
`client_address` is unambiguous: this policy is about a network address, not a principal.

**Non-goal:** no CIDR shorthands (`private`, `rfc1918`, cloud provider lists). A shorthand encourages
exactly the over-broad trust this policy exists to bound.

### 3. D09 — policy scope is per listener, enforced structurally

Server blocks are selected by the **`Host` header** (`internal/router/router.go`,
`hostScore(names, normalizeHost(host))`), and several `[[servers]]` blocks legitimately share one
`listen`. A policy resolved *after* virtual-host selection would let an attacker-chosen `Host` select
the trust policy applied to the attacker's own request — a hardened public vhost and a lenient internal
vhost on `:443` would be one header apart.

Two mechanisms, both required:

1. **Structural.** `internal/app/factory.go` builds one handler per `UniqueListenAddrs`, and the host
   match runs *inside* `rtr.For(addr)`. Identity derivation is installed at **index 1 of that
   per-address chain, immediately after `middleware.RequestID()`** — outside the router, so it precedes
   every `Host` read, and outside the observers, so metrics, access logging, tracing, and every
   per-location middleware can read the result. This makes the attack impossible rather than merely
   detected.
2. **Declarative.** Every `[[servers]]` block sharing a `listen` MUST declare a byte-identical
   `client_address` policy, enforced as a `Validate()` error, so the configuration cannot misrepresent
   what applies. This follows the existing `validateACMEConsistency` and TLS/plaintext-mixing
   precedents in `internal/config/validate.go`.

A top-level `[[listeners]]` block is **deferred**, not rejected. It is the better long-term model, but
its blast radius is roughly 110–130 files. Crucially, the identical-policy rule makes the chosen design
a **lossless prefix** of it: because the value is provably identical across siblings, hoisting it later
is a mechanical, semantics-preserving transformation. This is therefore not a one-way door.
Promotion triggers: two or more further listener-scoped fields are added; or the migration corpus shows
real configurations with four or more virtual hosts per listener; or the divergence lint produces a
reported incident.

### 4. D09 — canonical algorithm

1. Parse and retain the direct socket peer.
2. If the peer is not in `trusted_proxies`, ignore every asserted header and use the peer.
3. If trusted, prefer RFC 7239 `Forwarded` when enabled and syntactically valid.
4. Otherwise use `X-Forwarded-For` when enabled.
5. Never merge two different asserted chains.
6. Evaluate the selected chain right to left, removing trusted proxy hops.
7. The first untrusted valid address is the canonical client.
8. If every asserted hop is trusted, use the leftmost valid asserted address.
9. On malformed, oversized, ambiguous or over-hop-limit input, fail closed to the direct peer and emit
   bounded diagnostics.
10. Obfuscated identifiers, `unknown`, hostnames and invalid addresses are never canonical clients. No
    DNS resolution occurs at any point.

### 5. D09 — request-scoped contract

A new dependency-light package `internal/clientaddr`, importable by `auth`, `middleware`, `waf`,
`handler` and `admin` without cycles:

```go
type Identity struct {
    Client, Peer netip.Addr
    Source       Source // peer | forwarded | xff
    Result       Result // accepted | untrusted_peer | malformed | too_many_hops
}

func FromContext(ctx context.Context) (Identity, bool)
func Client(r *http.Request) netip.Addr // falls back to the peer when absent
func Peer(r *http.Request) netip.Addr
```

- One `*Identity` in `context.Context`; accessors return values so callers cannot mutate shared state.
  Cost is about two allocations per request — the same order as the existing `RequestID()` middleware.
- **No `Chain` field.** `netip.Addr` is comparable and heap-free; a chain would add a slice allocation
  to every request for a value almost nothing reads. `Source` and `Result` answer the diagnostic
  question. Adding `Chain` later is additive and safe; removing it would not be.
- A pooled identity is rejected: `internal/background`'s lease seam deliberately lets contexts outlive
  the request, so a pooled value could be reused under a live reader.
- `r.RemoteAddr` is **never** mutated as an integration mechanism.

The existing `middleware.ClientIdentity` (verified mutual-TLS certificate metadata) is renamed to
`middleware.PeerCertIdentity` so the two concepts stay distinguishable in code and documentation.

### 6. D09 — consumers and scope

The canonical client is used by CIDR allow/deny authentication, IP rate limiting, WAF source address,
access logs and request samples, upstream forwarding headers, the FastCGI environment, and audit or
runtime projections where source identity is relevant. Every consumer retains access to the direct peer.

| Surface | Disposition |
| --- | --- |
| HTTP/1.1, HTTP/2, HTTP/3 | **In scope**, and free: `newStagedHTTP3WithTLS` receives the same handler as the TCP listener, so one middleware chain serves all three. Parity is proven by test, not assumed. |
| FastCGI | **In scope.** `REMOTE_ADDR` becomes the canonical client, a new `JUL_PEER_ADDR` carries the transport peer, and `HTTP_X_FORWARDED_FOR` is overwritten with the canonical chain instead of forwarding attacker input verbatim. |
| Admin listener | **Out of scope**, on security grounds. `adminClientIP` feeds admin rate limiting, SSE connection caps and audit `SourceIP`. Making the highest-privilege surface's attribution depend on an operator-editable CIDR list is a downgrade. Recorded in `docs/known-limitations.md`. |
| `internal/stream` (L4) | **Out of scope.** For L4, Boundary A is the PROXY-protocol source when `proxy_protocol` is `in` or `both`, otherwise the socket peer. It never feeds the HTTP canonical identity. |

**Access-log fields.** `remote` is removed; `client_ip` and `peer_ip` are emitted instead, with
`peer_ip` omitted when equal to `client_ip`. Keeping `remote` was considered and rejected: it was a
pure backwards-compatibility measure, and with no adopted deployments it would have shipped a
permanently redundant field.

**WAF.** Coraza v3.7.0 exports only `WrapHandler`; its `processRequest` parses `req.RemoteAddr` itself
and offers no injection point, while the 297-line response interceptor is unexported, so
reimplementing the middleware is rejected. However `WrapHandler` calls
`ctxwaf.NewTransactionWithOptions(experimental.Options{Context: r.Context()})` whenever the WAF
satisfies `experimental.WAFWithOptions` — and the canonical identity is already in that context. Jul
therefore wraps the WAF, reads the identity from the options context, and returns a transaction that
overrides only `ProcessConnection`. No `RemoteAddr` mutation, no fork, no upstream dependency; it also
sidesteps Coraza's `strings.LastIndexByte(':')` IPv6 mis-split by bypassing the parser entirely.

**Outbound forwarding.** Jul emits `X-Forwarded-For: <canonical client>, <direct peer>`, always
constructed from Jul's trusted context and never by preserving an inbound chain. This is deliberately
lossy: an inbound `client, P1` received from peer `P2` is emitted as `client, P2`, dropping intermediate
trusted proxies. Jul is the last hop before the backend, and reconstructing the full chain would
re-inject third-party data into a channel the backend authenticates. Full fidelity is restorable later,
additively, via `Chain`. `$proxy_add_x_forwarded_for` is redefined in place to these semantics.
`X-Real-IP` remains unsupported.

### 7. D10 — backend peer identity: public configuration

```toml
[[upstreams]]
name = "inventory"

[upstreams.backend_tls]
ca_file            = "/etc/jul/backend-ca.pem"
ca_mode            = "system_and_file"
client_cert        = "/etc/jul/client.pem"
client_key         = "/etc/jul/client.key"
server_name        = "inventory.internal"
min_version        = "1.2"
peer_identities    = ["dns:inventory.internal"]
insecure_skip_verify = false
```

**One key, `backend_tls`, used identically** under `[[upstreams]]`, `[[servers.locations]]` and native
gRPC/transcoding. This resolves the open question of whether gRPC shares the block: it does, because
one normalized internal type is mandatory and divergent public names would guarantee divergent
behaviour.

`[upstreams.tls]` plus a separate `proxy_tls` was rejected: two names for one type, and `tls` already
means *inbound* TLS under `[[servers]]`, so the same key would carry opposite directions in two blocks.

- `ca_mode` is an **explicit enum** — `system` (default), `system_and_file`, `file_only` — never
  inferred from the presence of `ca_file`. Inference is unrevertable: changing augment-versus-replace
  semantics later would silently change which backends verify, with no error.
- `peer_identities` entries are **prefixed** (`dns:`, `uri:`) from the first release, so future identity
  types are purely additive. Identities are ORed, matched after standard verification, never by regex
  or substring.
- `min_version` defaults to **1.2**, matching current transcoding behaviour and Go's client default.
- Discovery-returned addresses are dial destinations only. The configured logical name remains the TLS
  identity; a selected IP never becomes the SNI when a `server_name` is configured.

All consumers receive one immutable resolved policy. Transports never parse public configuration
directly — this is the invariant that keeps a future named-profile feature additive rather than a
transport rewrite. Named profiles are deferred; promotion requires evidence of three or more repeated
policy blocks, a centralized rotation use case, or significant Console duplication.

### 8. D10 — insecure verification bypass

`insecure_skip_verify = true` produces a lint **error** (`SeverityError`), so `jul lint` exits 1 even
without `-strict`, plus one startup warning per affected upstream and a bounded count in the Console
security projection. No metric label carries an upstream name.

`Validate()` still **accepts** it. A field whose entire purpose is opting into an insecure mode cannot
be a validation rejection, or the field is unusable, and accepting it preserves an emergency path.
Hard validation errors apply only to self-contradictory combinations: the bypass together with
`peer_identities`, or with `ca_mode` other than `system`.

### 9. D10 — health checks use the same trust as live traffic

`docs/health.md` currently documents `InsecureSkipVerify: true` as intentional design for active health
checks. That is reversed: probes consume the same resolved policy as live traffic. A backend is never
reported healthy under weaker verification than the requests Jul will send it. Raw TCP probes remain
plaintext reachability checks and are never represented as identity verification.

This lands as a single-release change rather than behind a deprecation window. A two-step migration was
considered and rejected: it existed only to protect adopted deployments, of which there are none, and
the direct change is strictly safer because it leaves no interval in which health and live traffic
disagree about trust.

### 10. Lifecycle

`client_address` is **hot reload** through handler generation; the prefix set is compiled during
Prepare so a malformed CIDR aborts before Publish.

`backend_tls` starts **restart required** and is upgraded per consumer only as the HTTP, gRPC and
health integrations land. Claiming hot reload from schema work alone would be untruthful:
`reloadCertificates` is a no-op, transcoding caches connections for the process lifetime, and the
health client is built once. Independently of that, trust material is registered in the candidate
digest set and lifecycle fingerprint from the first release, so same-path content rotation is *detected*
correctly even while the *action* remains a restart. Detection and action are separable, and getting
detection right early is free.

## Consequences

- **Positive.** One canonical client identity replaces several competing definitions. Direct
  deployments are unchanged, because the canonical client equals the peer. Proxied deployments without
  an explicit policy stay peer-based, which is the secure default. Backend private-CA and mutual-TLS
  services become reachable with one policy shared by HTTP, gRPC, transcoding and health. The
  `Host`-header trust-selection weakness is closed structurally rather than by validation alone.
- **Negative.** The forwarding parsers need sustained adversarial and fuzz testing. Operator CIDR
  mistakes remain possible and widen trust. Stricter health verification will surface previously hidden
  certificate misconfiguration as unhealthy backends. Repeating an identical `client_address` block
  across virtual hosts sharing a listener is verbose. Access-log consumers must move from `remote` to
  `client_ip`.
- **Mitigation.** Fail-closed at every step, with the peer as the anchor. Table-driven and fuzz coverage
  for both parsers, and a spoofing matrix per consumer. Documentation states plainly that
  `trusted_proxies` is a security boundary that should be as narrow as possible. The identical-policy
  rule keeps the `[[listeners]]` upgrade path open and mechanical. A lint warns about `https` health
  targets lacking a `backend_tls` block several releases before the verification change.

### Threat model

| Threat | Control |
| --- | --- |
| Untrusted client spoofs `X-Forwarded-For` or `Forwarded` | Headers are ignored unless the immediate peer is explicitly trusted |
| Attacker selects a lenient policy via the `Host` header | Identity is derived per listen address, before routing; policies must be identical across siblings |
| Malformed or oversized chain used to bypass policy | Fail closed to the direct peer; byte, element and hop bounds; no partial trust |
| Header parsing used for allocation or log amplification | Bounded parsing, no DNS, rate-limited diagnostics, no raw headers in metrics or logs |
| Operator over-broad `trusted_proxies` | Narrow-CIDR guidance, no shorthands, visible in Console and status |
| Backend impersonation on a private network | Standard chain verification plus explicit `peer_identities` |
| Discovery returns an attacker-controlled address | Dial address never becomes the TLS identity; configured `server_name` is stable |
| Health check masks an untrusted backend | Health uses the same resolved policy; no weaker mode |
| Ambiguous CA semantics silently weaken trust | Explicit `ca_mode` enum, never inferred |
| Secret leakage through telemetry | Keys and CA contents are never projected; metric labels are bounded enums |

## Related

- Epics #108 (core gateway completeness) and #109 (backend peer identity)
- Implementation: #135, #136, #259 (inbound); #137, #138, #139, #140 (backend)
- Adjacent: #94 (Boundary C lifecycle), #117 (routing and response policy), #126 (metric contract),
  #152 and #154 (NGINX migration), #110 and #116 (resilience), #111 and #118 (generated contracts)
- [ADR 0011](0011-reload-plan.md) for the Prepare/Publish boundary this record relies on
