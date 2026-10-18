# ADR 0025 — Application-sharing contract: derived view, access modes and the withdrawal promise

- **Status:** Proposed — awaiting maintainer acceptance under [#580](https://github.com/victornife/jul/issues/580). SD-01, SD-02 and SD-03 were answered by the maintainer on 2026-10-18; the remaining content is the author's proposal until #580 is accepted.
- **Date:** 2026-10-18
- **Deciders:** Jul.IA maintainer (decision owner); #580 design author
- **Applies to:** the sharing programme [#579](https://github.com/victornife/jul/issues/579) and its successors #581–#588, #613; inputs to #627, #633, #634; #562/#563
- **Source:** `main` at `64e4a8b29230134c5dee37dfd8bfa130a9580a2a`; [specification](../specs/application-sharing.md); [design review packet](../reviews/2026-10-18-580-application-sharing-design.md)

## Context

Jul is a standalone gateway. Sharing an application still requires the
operator to coordinate reachability, audience, authentication and operation
across the host, connectors, identity providers and the application. The
sharing programme needs one contract before successors make architectural or
security assumptions.

The source baseline constrains the design more than the issue text does:

- There is no exposure, audience or application model. Console **Apps** are
  upstream pools; static sites have none.
- Durable route identity (`route_id`) is optional and is minted only by a managed
  structured create. Server `name` is descriptive. ADR 0019 forbids an identity
  sidecar and forbids Jul writing identifiers into configuration it does not own.
- Access checks are per location (CIDR, then one of Basic, JWT or forward-auth),
  plus listener-wide client-certificate policy. JWTs are read only from
  `Authorization: Bearer`.
- A configuration change reaches every new request, including new requests and
  new HTTP/2 or HTTP/3 streams on existing connections. In-flight exchanges,
  hijacked WebSockets and certificate identity bound to an open TLS connection
  continue. A local probe confirmed that a revoked client certificate still
  passes on its already-open connection. There is no connection-age limit and no
  termination API.
- Providers behave similarly: Tailscale keeps established flows after an ACL
  change; Cloudflare Access sessions last their configured duration unless
  revoked. Envoy's and NGINX's connection-age limits do not end upgraded
  streams.

## Decision

1. **Derived view, no new store (SD-01, accepted).** The first sharing view is
   derived from the authoritative configuration. Its unit is the *entry* (one
   location, or one `[[stream]]` listener); entries are grouped by server block
   ("shared service") with a descriptive label. References are existing
   identities only. No new identifier, no application resource, no mapping
   store, no persisted metadata.
2. **Declared intent outside Jul for now.** Fixture and recipe owners keep
   versioned intent manifests as test expectations. A single optional,
   location-scoped `[servers.locations.sharing]` block is pre-specified, not
   accepted. Committing it requires an amendment to this ADR with first-use
   evidence from #581–#585, at the first runtime consumer (#587).
3. **Four access modes (SD-03, accepted).** `preview`, `private_network`,
   `restricted` and `public`, with the semantics in specification §5.
   Private-network membership may be the declared *coarse* audience, enforced by
   the network provider and never presented as a named identity.
4. **Withdrawal promise and session work (SD-02, accepted: "D+E").** The first
   journeys promise denial of new access from the published generation, and
   display that open connections and streams, certificate identity on open
   connections, and provider and application sessions continue. #563 is selected
   with #562 as its WebSocket-ownership component, scoped to **event-driven,
   entry-scoped retirement when a change tightens an entry**. No periodic
   connection age and no general session-termination API is selected. #587's
   "end open connections" and #613's withdrawal claims for WebSocket
   applications are gated on that work. #581 proceeds without it.
5. **Withdraw in place.** Withdrawal keeps the route and its target and adds a
   deny. It never deletes the route, because deletion can fall through to a
   default host or broader prefix.
6. **Separation and trust rules.** Each external enforcement domain gets its own
   Jul listener (SH-11). No supported journey relies on an application trusting
   connector-asserted identity headers (SH-12). Restricted entries keep Jul's
   response cache off unless a recipe proves a safe policy.
7. **Three layers never merge.** Declared intent, configured posture and observed
   evidence are reported separately, with freshness and vantage. Nothing renders
   a single "protected" state.
8. **Classification.** All of the above is an operational enhancement composing
   existing capabilities. Provider lifecycle (#588), group sharing (#589),
   native application login, provider mutation and invitations remain
   separately gated.

## Reversibility and one-way doors

| Decision | Classification | Why | Exit or migration |
| --- | --- | --- | --- |
| Derived view, no store (1) | Readily reversible | nothing persisted; the projection starts with internal API stability | adding held intent later is additive (2); dropping the view affects only internal consumers |
| Location-scoped intent block (2) | **One-way once shipped** — not committed | public TOML names and enum values, generated contracts, GitOps files | deferred; if shipped and later replaced: dual-read and a deprecation window under the compatibility policy |
| Vocabulary ("shared service", "entry", mode identifiers) | Reversible now; costly once in API, Console and the #585 rubric | operators learn it; tests encode it | settle before #587 exposes it; keep internal stability until then |
| Withdrawal promise (4) | **One-way to weaken**; additive to strengthen | security commitment operators rely on | strengthening via #563 is additive; weakening would be a security regression |
| #563/#562 selection (4) | Reversible until merged; costly afterwards | operators will rely on "withdraw ends open connections" | if the design proves infeasible, the gated claims stay unshipped and §8.2 remains |
| Coarse private-network mode (3) | Costly to tighten once displayed by #587 | deployments rely on network-only audiences | tightening later needs an explicit migration notice; adding Jul checks remains optional |
| Withdraw in place (5) | Mechanism reversible; the "never delete" rule is one-way to weaken | safety invariant | — |
| One listener per enforcement domain (6) | Costly to relax | operators build topologies around it | relaxing needs a separately decided SNI/Host binding guarantee |
| Public exposure of an entry (operational) | **Irreversible disclosure** | content seen cannot be unseen | stated in every widening plan |
| External state (provider policy, DNS, sessions) | Not rolled back by Jul | outside Jul's authority | every rollback shows its own audience delta |

## Consequences

**Positive.** Successors can implement and verify without a new state owner.
File-owned and expert TOML users lose nothing. The summary is honest about what
Jul can and cannot see. The selected retirement work turns "withdraw" into a
complete outcome for Jul-routed traffic without a session store or provider
integration.

**Negative and accepted.** Until #587 decides otherwise, Jul cannot display
declared audiences, and intent-aware posture checks (#633) remain
`unanalysed`. Until #563 is delivered, a withdrawn user's open live connections
continue unless the operator restarts Jul or stops a connector. Provider paths
need more listeners. Basic, CIDR and mTLS restricted entries cannot use Jul's
response cache safely without per-application proof.

**Neutral.** The NGINX migration disposition is not applicable: no runtime
behaviour or directive mapping changes.

## Alternatives considered

| Alternative | Why not now |
| --- | --- |
| First-class application resource or separate store | a second desired state; conflicts with ADR 0019 I2 and #580 scope |
| Top-level `[[applications]]` block referencing routes | needs `route_id` everywhere (an identity migration for file-owned users); dangling references; two edit sites |
| Accept the intent block now | granularity chosen before first-use evidence; public schema commitment without a consumer |
| Require `route_id` for sharing | contradicts ADR 0019 §4.2; breaks file-owned deployments |
| Periodic maximum connection age (#563 classic) | disconnects everyone; does not end upgraded streams in comparable gateways; Go and quic-go lack per-connection graceful close |
| Defer all session work | leaves the central "who can access" promise permanently partial; rejected by the maintainer in favour of D+E |
| Native Cloudflare token validation in Jul now | unneeded while `cloudflared` validates; a public schema change; left to #583 evidence |
| Host-based separation of provider and public entries on one listener | no SNI-to-Host check; default-host fallthrough |
| Withdraw by deleting the route | fallthrough risk; loses the target needed for recovery |

## Related

[ADR 0012](0012-oss-open-core-boundary.md), [ADR 0014](0014-operability-surfaces.md),
[ADR 0015](0015-managed-apply-terminal-ledger.md),
[ADR 0016](0016-inbound-identity-and-backend-peer-trust.md),
[ADR 0018](0018-bounded-route-matching-and-response-policy.md),
[ADR 0019](0019-configuration-authority-generated-contracts-and-resource-identity.md),
[ADR 0022](0022-admin-console-oidc.md); [specification](../specs/application-sharing.md);
[known limitations](../known-limitations.md#long-lived-connections).
