# ADR 0016 — Identity and trust boundaries

- **Status:** Accepted
- **Date:** 2026-08-12, amended 2026-08-14
- **Deciders:** Jul.IA maintainer
- **Applies to:** inbound client-identity derivation, trusted-proxy policy, inbound mutual-TLS identity, CIDR authentication, IP rate limiting, WAF source address, access logs, upstream forwarding headers, FastCGI environment, L4 PROXY-protocol ingest, backend HTTP/gRPC/transcoding TLS, active health checks, service discovery and other control-plane clients
- **Source:** #115 (`[ADR][CGC-02]`), source-level design review of `66c71b2d` and current `main`; amended after the adversarial audit of `2578fd4b`
- **Related:** [ADR 0013](0013-project-operating-model-and-completeness.md) (portfolio entry), [ADR 0011](0011-reload-plan.md) (reload transaction), [ADR 0014](0014-operability-surfaces.md) (operability surfaces), [ADR 0017](0017-upstream-resilience-and-overload-control.md) (upstream resilience)

## Revision log

The record is amended in place rather than superseded: no adopted deployment's
reading of the original is invalidated, and one document is easier to reason
about than two. What changed is recorded here so a correction is never mistaken
for the original reasoning.

| Date | Change |
| --- | --- |
| 2026-08-12 | Accepted as *Inbound client identity and backend peer-trust boundaries*. |
| 2026-08-14 | Retitled: the scope is every identity Jul authenticates, not only the inbound and backend ones. |
| 2026-08-14 | **Five boundaries become seven.** A′ (verified transport credential) and F (control-plane peer identity) were identities Jul already authenticated but the model did not name. |
| 2026-08-14 | **§4.3 corrected (security).** Trusting a proxy was treated as trusting every header it forwards. A header may only be enabled if the proxy *overwrites* it; the default is `x-forwarded-for` alone, and the field is required once a proxy is trusted. |
| 2026-08-14 | **§4.9 corrected (security).** Falling back to the direct peer is right for serving and logging and wrong for access decisions, where that peer is a proxy. Attribution is now an explicit property consumers must check. |
| 2026-08-14 | **L4 reclassified (security).** The PROXY-protocol source was called Boundary A. It is an assertion, so it is L4's Boundary B, and ingest now requires a declared trusted proxy. |
| 2026-08-14 | **§10 corrected (truthfulness).** `backend_tls` was still recorded as restart-required after it had become hot reload. |
| 2026-08-14 | **§9 premise corrected.** Probes were said to skip verification "by design"; the shipped code never did, and the claim came from a documentation error. |
| 2026-08-14 | Added: topology matrix, address-family parity, TLS termination model, identity assertion to the backend, PROXY on HTTP listeners, and re-entry conditions for every deferral. |

## Context

Jul defaults every IP-based security decision to the real transport peer and never implicitly trusts
`X-Forwarded-For`. That is correct for direct deployments and incomplete behind a reverse proxy: CIDR
authentication, rate-limit keys, WAF rules and access logs all describe the proxy rather than the
client, and operators have no safe way to say otherwise.

Separately, outbound data-plane connections use language defaults. `newProxyTransport` sets no
`TLSClientConfig` at all, native gRPC dials with a nil `tls.Config`, transcoding hard-codes
`MinVersion: tls.VersionTLS12`, and active health checks verify against the platform trust store with
no way to express anything narrower. (An earlier revision of `docs/health.md` claimed probes used
`InsecureSkipVerify: true` "by design". That was never true of the shipped code; the documentation was
wrong, and neither the documented behaviour nor the actual one is the intended one.) There is no way
to express a private trust root, a backend client certificate, an SNI override for a
discovery-returned IP, or a required peer identity.

Both gaps are about *identity*, but they are proved by different means and fail in different
directions. Deciding them together — and keeping them apart — is the point of this record.

Without an explicit model: operators reach for unsafe header-derived rate-limit keys; logs, auth and
the WAF disagree about who the client is; discovery-backed addresses silently become their own TLS
identity; health checks and live traffic can use different trust; and any future provider transport
invents a third security model.

## Decision

### 1. Seven trust boundaries, which may not be collapsed

| | Boundary | Question | Proof | Failure |
| --- | --- | --- | --- | --- |
| **A** | Immediate transport peer | *What is the socket?* | Kernel fact | Cannot fail — ground truth |
| **A′** | Verified transport credential | *Did the peer prove who it is?* | Certificate chain + CRL + SAN allow-list | Refuse the handshake |
| **B** | Asserted original client | *Who does A claim came before it?* | A ∈ `trusted_proxies`, **on a channel A overwrites** | Degrade to A, or refuse where A is the only admitted sender |
| **C** | Auxiliary egress authorization | *May Jul connect outbound at all?* | Destination ∈ allow-list | Refuse the dial |
| **D** | Data-plane backend selection | *Which address do I dial?* | Routing / load balancing / discovery | Retry or eject |
| **E** | Backend peer identity | *Is that address the intended service?* | Certificate chain + name binding | Refuse the handshake |
| **F** | Control-plane peer identity | *Is that registry or authority the intended one?* | Certificate chain + name binding | Refuse the dial |

These are different verbs — **observe**, **authenticate**, **derive**, **authorize**, **select** — and
a single generic `trusted` flag would have to mean all of them at once. Each collapse is a known
vulnerability class:

- **A ∪ B** is header spoofing. Losing A destroys the fallback anchor, the audit trail, and the ability
  to express *"only accept connections from my proxy network"* separately from *"this end user is in
  this range"*.
- **A ∪ A′** is treating a socket as a credential. A peer address is *observed*; a certificate is
  *proved*. Conflating them lets network position substitute for possession of a private key, which is
  the assumption every flat-network compromise depends on.
- **C ∪ E** is a category error. An egress allow-list authorizes a *destination*; it does not
  authenticate a *peer*. Permitting `10.0.0.0/8` says nothing about which of sixteen million hosts
  answered.
- **C ∪ F** is the same error one layer up, and worse: a poisoned registry answer would be trusted
  precisely because the dial to the registry was permitted. Authorising a destination proves nothing
  about who replied, and D's entire safety argument rests on F holding.
- **D ∪ E** is discovery poisoning. If selection implied identity, a compromised registry would rewrite
  both the address and the name it is verified against, and TLS would succeed against the attacker.
- **B ∪ E** is transitive trust. `trusted_proxies` changes far more often than certificates; one flag
  would let widening the inbound proxy list silently weaken backend verification.

They also differ in blast radius and ownership: A, A′ and B are per-listener and change weekly, C is
process-global, E is per-upstream and tied to certificate lifecycle, F is per-provider and tied to the
cluster or datacentre it talks to. One flag would force the union of four change frequencies onto the
most-edited surface.

**B's proof has two conjuncts, and the second is the one that is easy to lose.** `A ∈ trusted_proxies`
establishes that the peer is a proxy. It does *not* establish that the proxy authored the field being
read. A proxy that writes `X-Forwarded-For` and forwards `Forwarded` untouched is entirely trustworthy
and still carries the client's own bytes in the second header. Believing a channel the proxy does not
overwrite is therefore identical to believing the client, and is the A ∪ B collapse reached by a
different route. §4.3a states the resulting invariant.

Boundary C is implemented by `internal/egress` and is unchanged by this record.

### 2. D09 — inbound identity: public configuration

```toml
[[servers]]
listen = ":443"

[servers.client_address]
trusted_proxies   = ["10.0.0.0/8", "2001:db8:100::/48"]
forwarded_headers = ["x-forwarded-for"]
max_hops          = 16
```

Defaults: no trusted proxies; `max_hops = 16`; `forwarded_headers` defaults to `["x-forwarded-for"]`
alone and is **required** once `trusted_proxies` is non-empty (§4.3a). An explicitly empty list keeps
peer-only identity even for a trusted peer.

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
3. If trusted, select the first *enabled* header **present** on the request. Selection is by presence,
   not by validity: a present but malformed header fails closed and never falls through to another,
   because falling through would let a client choose which header Jul believes.
4. Never merge two different asserted chains.
5. Evaluate the selected chain right to left, removing trusted proxy hops.
6. The first untrusted valid address is the canonical client.
7. If every asserted hop is trusted, use the leftmost valid asserted address.
8. On malformed, oversized, ambiguous or over-hop-limit input, fall back to the direct peer, record
   why, and emit bounded diagnostics (§4.9).
9. Obfuscated identifiers, `unknown`, hostnames and invalid addresses are never canonical clients. No
   DNS resolution occurs at any point.

#### 4.3a — a header may only be enabled if the proxy overwrites it

A forwarding header may appear in `forwarded_headers` **only** if the trusted proxy writes it on every
request. A header the proxy merely passes through carries whatever the client sent, so believing it is
believing the client and Boundary B is gone.

This is not hypothetical: nginx, HAProxy, Cloudflare, CloudFront, ALB and ingress-nginx all write
`X-Forwarded-For` and forward RFC 7239 `Forwarded` untouched. A default that enabled both therefore
handed the canonical client address to anyone behind such a proxy.

Therefore: the default is `["x-forwarded-for"]` alone; `forwarded_headers` is **required** once
`trusted_proxies` is non-empty, so the channels a proxy authors are stated rather than inherited; and
`jul lint` warns when `forwarded` is enabled, because that is an assertion about the deployment that
only the operator can make. The derivation algorithm was never wrong — the default was.

#### 4.9 — failing closed for trust is not failing closed for access

When a trusted peer asserts a chain Jul cannot use, `Client` falls back to that peer. The fallback is
right for *serving* and *logging*: the request stays servable and the log stays truthful. It is wrong
for *access decisions*, because the peer is by construction a proxy, and a proxy network is very often
the same range as an internal allow list.

The address in that state is a transport fact, not an authenticated client. Consumers must therefore
distinguish the two:

- A consumer making an **allow** decision MUST NOT treat an unattributed address as a client. CIDR
  authentication denies instead — an allow list would otherwise admit the proxy network, and a deny
  list would let the real client past a rule aimed at it. Both are decided purely by attacker-supplied
  header bytes.
- A consumer **partitioning state** MUST key an unattributed identity separately, so degraded traffic
  cannot exhaust a bucket shared with correctly attributed traffic.
- Access logs and audit records MUST carry `Source` and `Result`, so a fallback is distinguishable
  from genuine proxy-originated traffic.

An *untrusted* peer stays attributed: it asserted a header Jul ignored, and it really is the client.

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
func (i Identity) Attributed() bool // Client names a client, not an unresolved hop
```

- One `*Identity` in `context.Context`; accessors return values so callers cannot mutate shared state.
  Cost is about two allocations per request — the same order as the existing `RequestID()` middleware.
- `Attributed()` is the predicate §4.9 requires. It is false exactly when a trusted peer asserted an
  unusable chain, which is the one case where `Client` names a proxy rather than a client. Exposing it
  as a method rather than leaving each consumer to compare `Result` values keeps the rule in one place.
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
| Admin listener | **Out of scope**, by default. `adminClientIP` feeds admin rate limiting, SSE connection caps and audit `SourceIP`. Making the highest-privilege surface's attribution depend on an operator-editable CIDR list is a downgrade. Recorded in `docs/known-limitations.md`. Re-entry conditions in §6a. |
| `internal/stream` (L4) | **In scope**, under the same boundaries. L4 Boundary A is always the socket peer; the PROXY-protocol source is L4's Boundary B. See §6b. |

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

### 6a. D09 — Admin: peer-only by default, with stated re-entry conditions

Peer-only attribution has one real cost. Behind a shared internal bastion every administrator appears
as the bastion, so per-source admin rate limits and SSE connection caps become a single shared cap and
audit `SourceIP` loses per-administrator attribution. That is a genuine trade-off, not a free win, and
recording it as a bare "out of scope" would read as permanent.

Proxy-aware Admin identity may be added later **only** under all of the following. Reusing the public
listener's policy is explicitly not one of them.

1. A dedicated `[admin.client_address]` block that can never be inherited from, or shared with, a
   `[[servers]]` listener.
2. The admin listener requires mutual TLS or an equivalently authenticated channel from the asserting
   proxy, so the assertion is bound to a credential (A′) rather than to an editable CIDR list.
3. Audit records carry `peer`, `client` and `source` as three distinct fields, never collapsed, so a
   compromised or misconfigured proxy is visible in the trail rather than hidden by it.
4. Rate limits and connection caps apply at **both** the peer and the asserted client, so one bastion
   cannot be used to multiply an attacker's budget.

### 6b. D09 — L4 uses the same boundaries, with one deliberate difference

An earlier revision recorded the PROXY-protocol source as L4's Boundary A: *"a kernel fact, cannot
fail"*. That was a category error. A PROXY header is an assertion made by whoever opened the socket,
exactly like a forwarding header, and treating it as ground truth collapsed A into B for L4 — the
collapse §1 forbids for HTTP. Because `proxy_protocol = "out"` re-emits the result to the backend, and
L4 backends are the ones that authorise by source address (`pg_hba.conf`, Redis, MySQL host grants),
the consequence was that any peer able to reach the listener could choose the address the backend
authenticated on.

The model is therefore uniform: **L4 Boundary A is always the socket peer, and the PROXY-protocol
source is L4's Boundary B**, proved the same way HTTP proves it — the peer is in a declared
`trusted_proxies` set, compiled by the same parser under the same canonical-prefix rule.

Two differences from HTTP are deliberate and are properties of the transport, not exceptions:

- **The chain degenerates to one hop.** PROXY carries a single source address, so there is no
  right-to-left walk, no `max_hops` and no header precedence. §4's steps 5–7 do not apply.
- **An untrusted peer is refused, not degraded.** An HTTP listener legitimately serves proxied and
  direct traffic side by side, so falling back to the peer is correct there. A stream listener with
  `proxy_protocol = "in"` declares that *all* traffic arrives via the proxy; degrading would let a
  direct client bypass the requirement simply by sending no header.

`trusted_proxies` is required whenever a header is ingested and rejected when none is, so the
configuration cannot imply a boundary that is never enforced.

### 6c. D09 — PROXY protocol on HTTP listeners

A TCP load balancer that preserves the client with the PROXY protocol rather than a forwarding header
— AWS NLB, GCP TCP LB, HAProxy in TCP mode — is a common topology with no expression in this design.
It is supported for TCP: `proxy_protocol` on a `[[servers]]` listener supplies **Boundary A**, which
then feeds the existing derivation unchanged, so every consumer, every parity guarantee and the whole
of §4 apply without modification.

**HTTP/3 cannot carry it.** QUIC is datagram-based and negotiates TLS inside the transport, so there
is no byte stream to prepend a header to. Enabling both on one listener is a validation **error**
rather than a documented asymmetry, so the gap cannot be reached by accident: an operator gets a
readable failure instead of a listener that derives identity two different ways depending on the
protocol a client happened to negotiate.

*Re-entry:* a QUIC client-preservation mechanism is adopted when an interoperable one exists — a
standard-track datagram framing, or agreement between at least two of the major L4 balancers — since
inventing a private one would produce a boundary no peer can actually speak.

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
  semantics later would silently change which backends verify, with no error. `system_and_file` is a
  genuine **union**: any publicly trusted CA can authenticate the backend, not only the supplied one,
  because `x509.CertPool` has no ordering or specificity rule that would prefer the configured root. A
  deployment that intends to trust only a private CA must use `file_only`; one that needs both should
  constrain the widened trust with `peer_identities`.
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

`docs/health.md` claimed active probes used `InsecureSkipVerify: true` as intentional design. The
shipped code never did: it set no `TLSClientConfig` at all and therefore verified against the platform
trust store. The documentation was wrong, and neither behaviour is the intended one — the correction
is recorded rather than quietly applied, so no reader infers that verification was once skipped.

The decision is unchanged by that correction: probes consume the same resolved policy as live traffic,
and a backend is never reported healthy under weaker verification than the requests Jul will send it.
Sharing the resolved policy object, rather than building a similar one, is what makes this a property
of the code instead of a convention. Raw TCP probes remain plaintext reachability checks and are never
represented as identity verification.

This lands as a single-release change rather than behind a deprecation window. A two-step migration was
considered and rejected: it existed only to protect adopted deployments, of which there are none, and
the direct change is strictly safer because it leaves no interval in which health and live traffic
disagree about trust.

### 10. Lifecycle

`client_address` is **hot reload** through handler generation; the prefix set is compiled during
so a malformed CIDR aborts before Publish. The same is true of `[[stream]]` `trusted_proxies`.

`backend_tls` is **hot reload**. It was recorded here as restart-required while its consumers were
being wired, and that text outlived the condition it described — an error worth naming, because an
accepted record that misstates shipped behaviour is worse than one that says nothing. The HTTP proxy,
native gRPC and the transcoder build their clients with the handler generation that owns them, and the
resolved policy's fingerprint is part of the upstream pool's identity, so a changed policy rebuilds the
pool and with it the probe client — the last consumer that would otherwise have kept its startup trust.
The retiring generation closes its idle connections, so no connection established under the previous
trust serves a request admitted under the new one.

Because the fingerprint digests file *contents*, rotating a certificate in place with no configuration
edit changes the pool identity and is applied on the next reload: detection and action are now the same
thing for this field, where they were deliberately separated while the consumers were incomplete. A
malformed policy still fails during candidate preparation, so the reload aborts before anything is
published.

(`reloadCertificates` remains a no-op. That concerns **inbound** listener certificates, which are
restart-only under R7-07, and was cited here as a `backend_tls` blocker in error.)

### 11. A′ — TLS termination and the two client credentials

Jul is a **terminating** proxy for HTTP: it always completes the client's TLS handshake and always
originates a separate connection to the backend. `[[stream]]` is always **passthrough** and never
terminates; it peeks the ClientHello for SNI routing and forwards the bytes unread.

The inbound client certificate (A′) and the backend client certificate (E) are therefore two
independent credentials with independent trust roots, and they are **never bridged**: possessing one
never satisfies the other, and no configuration makes the client's certificate the one Jul presents to
a backend. Inbound verification is listener-scoped — `client_auth` mode, CA bundle, CRL and SAN
allow-list — with the strongest mode across blocks sharing a listener applying to the whole listener,
because a handshake happens before any block is selected.

### 12. Identity asserted to the backend: one rule for both kinds

Jul tells the backend two things it cannot observe: who the client was (an address) and what the client
proved (a certificate). Both are assertions the backend must trust Jul for, so both obey one rule.

**Every identity Jul asserts to a backend is constructed solely from Jul's verified context, and the
channel carrying it is sanitized unconditionally — not per configuration.**

Address identity already satisfies this: `X-Forwarded-*` is cleared and rebuilt on every proxied
request regardless of what the operator configured. Certificate identity satisfies it for the
standard channel: `Client-Cert` and `Client-Cert-Chain` ([RFC 9440][rfc9440]) are stripped from every
inbound request — including requests where no client certificate was negotiated, as the RFC requires —
and emitted only when the operator opts in.

RFC 9440 is the recommended mechanism because it is the standard one, and because it declined exactly
the design Jul inherited from NGINX: it carries the whole certificate rather than cherry-picking fields,
having observed that per-field, per-deployment header names make independent implementations
"cumbersome or even impossible" to interoperate. The `$ssl_client_*` variables remain as the
NGINX-compatibility path for migrated configurations.

**Scope limit, stated rather than implied.** Jul cannot sanitize an arbitrary operator-chosen header
name, because it cannot know that a backend trusts `X-Client-DN`. The guarantee covers the RFC 9440
names and any header whose value references `$ssl_client_*`. Beyond that, sanitizing the channel is the
operator's obligation, and `jul lint` flags the common patterns.

[rfc9440]: https://www.rfc-editor.org/rfc/rfc9440.html

### 13. Address-family parity is an invariant, not an outcome

IPv6 is not a variant of IPv4. Every transport (TCP, UDP, QUIC), every protocol (HTTP/1.1, HTTP/2,
HTTP/3, gRPC, FastCGI, uWSGI, L4) and every discovery provider handles IPv6 identically to IPv4:
addresses are compared as `netip.Addr` with IPv4-mapped forms unmapped, dial targets are built with
`net.JoinHostPort`, and CGI-style environments carry the bare address without brackets.

This held before it was written down, which is precisely why it is written down: an invariant that is
true by accident is one review away from being false. Violations are bugs, not gaps.

*Transport capability* differences are a separate matter and are enumerated in the topology matrix
rather than smuggled in here — claiming parity Jul does not have would repeat the §10 mistake.

### 14. F — control-plane peer identity

Discovery providers, JWKS endpoints, forward-auth services, ACME directories and OCSP responders are
outbound connections Jul makes that are not data-plane backends. They pass Boundary C, which authorises
the *destination*, and they must also pass **F**, which authenticates the *peer*. D's safety argument
depends on it: §1 rejects discovery poisoning on the grounds that a selected address never becomes an
identity, which is only meaningful if the registry that supplied the address was itself authenticated.

**§8's contract applies to every verification bypass in the product, not only to `backend_tls`.** Any
field that turns a verified connection into an unverified one carries the same treatment: a
`SeverityError` lint finding, one startup warning per affected surface, a bounded count in the Console
security projection, and no metric label carrying a provider or upstream name. A bypass that is cheap
to reach and invisible in telemetry is the one that survives to production.

### 15. Supported topologies

A topology absent from this table is a gap in the record, not a silent non-goal. Adding a deployment
shape to Jul means adding a row; a reviewer who cannot find the row should reject the change. This is
what makes "we did not think about that topology" a detectable condition rather than a discovery made
in production.

| Topology | Boundary A source | Status |
| --- | --- | --- |
| Direct Internet → Jul | Socket peer | Supported; A = B, no policy needed |
| CDN (Cloudflare, CloudFront) → Jul | Socket peer | Supported; CDN ranges in `trusted_proxies`, `x-forwarded-for` |
| CDN → cloud L7 LB → Jul | Socket peer | Supported; both hops trusted, chain walked right to left |
| Service mesh sidecar (Envoy, Istio, Linkerd) → Jul | Socket peer | Supported; sidecar address trusted, usually loopback |
| Kubernetes ingress → Jul | Socket peer | Supported |
| Internal bastion → Jul (data plane) | Socket peer | Supported |
| Internal bastion → Jul (admin listener) | Socket peer | Peer-only by design; re-entry conditions in §6a |
| Multiple trusted proxies in sequence | Socket peer | Supported; all trusted hops removed right to left |
| Mixed trusted and untrusted hops | Socket peer | Supported; first untrusted hop from the right wins |
| TCP LB with PROXY protocol (NLB, GCP TCP LB, HAProxy TCP) → HTTP listener | PROXY header from a declared proxy | Supported for HTTP/1.1 and HTTP/2 (§6c) |
| TCP LB with PROXY protocol → HTTP/3 listener | — | **Rejected by validation.** QUIC cannot carry the header; re-entry in §6c |
| L4 TCP stream, PROXY ingest | PROXY header from a declared proxy | Supported (§6b) |
| L4 UDP stream | Socket peer | **No client preservation.** Non-goal; re-entry below |
| Discovery-backed upstreams (Consul, Kubernetes) | n/a — Boundary D/E/F | Supported; a selected address is never an identity |

**UDP client preservation is a non-goal.** The PROXY protocol defines a DGRAM form, so this is
implementable, but on a connectionless transport the header must ride on every datagram, which changes
the MTU and fragmentation calculus and interacts with session keying. The security cost today is zero:
no UDP consumer reads the client address — there is no per-source ACL, rate limit or affinity — so
building the framing would populate a value nothing reads.

*Re-entry:* adopted when any one of (1) a UDP stream gains any per-source policy — ACL, rate limit or
session affinity — because at that moment the socket peer stops being sufficient and the gap becomes
security-relevant; (2) the migration corpus shows a real upstream emitting PROXY v2 DGRAM to a Jul
listener; (3) two or more independent operator reports request it. Trigger (1) is self-enforcing:
whoever adds the first per-source UDP policy meets the condition in review.

### 16. Deferred, with the conditions that would promote each

Every deferral states what would bring it back. A deferral without re-entry conditions reads as a
permanent no, and the next person either violates it or is blocked by it.

| Deferred | Promotion trigger |
| --- | --- |
| Top-level `[[listeners]]` block | Two or more further listener-scoped fields; or a migration corpus with four or more virtual hosts per listener; or a reported divergence-lint incident |
| Named `backend_tls` profiles | Three or more repeated policy blocks; a centralized rotation use case; or significant Console duplication |
| Proxy-aware Admin identity | All four prerequisites in §6a |
| QUIC client preservation | An interoperable mechanism exists (§6c) |
| UDP client preservation | Any of the three triggers in §15 |
| Per-tenant policy | See below |

**Multitenancy.** Nothing in this record forecloses it, and the one structural blocker is named here so
a future tenancy model knows exactly what it must change: **`internal/egress` (Boundary C) is
process-global.** Per-tenant egress policy therefore requires re-scoping C, which is the only boundary
whose ownership would have to move. The rest is already compatible — `client_address` is
listener-scoped, `backend_tls` is per-upstream and per-location, and A′ is per-listener — so a tenancy
model that maps a tenant onto listeners and upstreams needs no change to this design.

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
| Client spoofs a header the trusted proxy does not overwrite | Only channels the proxy authors may be enabled; the default is `x-forwarded-for` alone and the field is required once a proxy is trusted (§4.3a) |
| Attacker selects a lenient policy via the `Host` header | Identity is derived per listen address, before routing; policies must be identical across siblings |
| Malformed or oversized chain used to bypass policy | Byte, element and hop bounds; no partial trust; the peer fallback is never authorised as a client and degraded traffic is keyed separately (§4.9) |
| Direct client asserts an L4 PROXY header | Ingest requires a declared `trusted_proxies` set; an untrusted peer is refused (§6b) |
| Client injects a certificate-identity header | `Client-Cert`/`Client-Cert-Chain` are stripped unconditionally, including when no client certificate was negotiated (§12) |
| Header parsing used for allocation or log amplification | Bounded parsing, no DNS, rate-limited diagnostics on both the HTTP and L4 boundaries, no raw headers in metrics or logs |
| Operator over-broad `trusted_proxies` | Narrow-CIDR guidance, no shorthands, visible in Console and status |
| Backend impersonation on a private network | Standard chain verification plus explicit `peer_identities` |
| Discovery returns an attacker-controlled address | Dial address never becomes the TLS identity; configured `server_name` is stable |
| Compromised registry answers a permitted dial | Boundary F authenticates the registry itself; C authorises the destination only (§14) |
| Health check masks an untrusted backend | Health uses the same resolved policy object; no weaker mode |
| Ambiguous CA semantics silently weaken trust | Explicit `ca_mode` enum, never inferred; `system_and_file` documented as a union |
| Secret leakage through telemetry | Keys and CA contents are never projected; metric labels are bounded enums |

## Related

- Epics #108 (core gateway completeness) and #109 (backend peer identity)
- Implementation: #135, #136, #259 (inbound); #137, #138, #139, #140 (backend)
- Adjacent: #94 (Boundary C lifecycle), #117 (routing and response policy), #126 (metric contract),
  #152 and #154 (NGINX migration), #110 and #116 (resilience), #111 and #118 (generated contracts)
- [ADR 0011](0011-reload-plan.md) for the Prepare/Publish boundary this record relies on
