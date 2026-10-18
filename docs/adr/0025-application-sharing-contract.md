# ADR 0025 — Application-sharing contract: derived view, declared intent and complete withdrawal

- **Status:** Proposed — awaiting maintainer acceptance under [#580](https://github.com/victornife/jul/issues/580). Decisions SD-01 to SD-06 were answered by the maintainer on 2026-10-18; the remaining content is the author's proposal until #580 is accepted.
- **Date:** 2026-10-18
- **Deciders:** Jul.IA maintainer (decision owner); #580 design author
- **Applies to:** the sharing programme [#579](https://github.com/victornife/jul/issues/579) and successors #581–#588, #613; inputs to #627, #633, #634; #562/#563
- **Source:** `main` at `64e4a8b29230134c5dee37dfd8bfa130a9580a2a`; [specification](../specs/application-sharing.md); [design review packet](../reviews/2026-10-18-580-application-sharing-design.md)

## Context

Jul is a standalone gateway. Sharing an application still requires the operator
to coordinate reachability, audience, authentication and operation across the
host, connectors, identity providers and the application. The source baseline
constrains any design:

- There is no exposure, audience or application model. Console **Apps** are
  upstream pools; static sites have none.
- `route_id` is optional and minted only by a managed structured create; server
  `name` is descriptive; ADR 0019 forbids an identity sidecar and forbids Jul
  writing identifiers into configuration it does not own.
- Location checks are CIDR then one of Basic, JWT or forward-auth; WASM request
  plugins and the CORS preflight terminator run before them; client-certificate
  policy is listener-wide; JWTs are read only from `Authorization: Bearer`; an
  unmatched `Host` reaches the first-declared block; the response cache key is
  method + Host + URI.
- A change reaches every new request, including new requests and streams on open
  connections. In-flight exchanges, hijacked WebSockets and certificate identity
  on an open TLS connection continue (a local probe confirmed the last). There
  is no connection-age limit and no termination API. Comparable gateways'
  connection-age limits do not end upgraded streams; Go and quic-go lack
  per-connection graceful close.
- Providers keep established flows (Tailscale) or sessions (Cloudflare Access,
  default 24 h) after a policy change unless explicitly revoked.

## Decision

1. **Derived view (SD-01).** The sharing view is derived from the authoritative
   configuration. Its unit is the *entry* (one HTTP location). Entries are
   grouped by server block for display only. Operations act on explicit,
   previewed entry sets; same-backend lists are advisory, never identity. No
   application resource, mapping store or new identifier namespace.
2. **Declared intent in the configuration, built early (SD-05 "B+").** One
   optional location block, `[servers.locations.sharing]` with `mode` and
   `audience_label`, requires a `route_id` on the same location, has no runtime
   effect, and drives **intent findings** in lint and plans plus an explicit
   Console acknowledgement before any change that widens or contradicts declared
   intent. It is delivered as a bounded tranche before #581's evidence run.
   Evolution is additive only.
3. **Four access modes (SD-03).** `preview`, `private_network`, `restricted`,
   `public`. Private-network membership may be a declared *coarse* audience,
   enforced by the network provider and never presented as a named identity.
   `[[stream]]` listeners are outside the sharing contract.
4. **Withdrawal promise and complete withdrawal (SD-02 "D+E", SD-04, bundle).**
   First journeys promise denial of new access from the published generation and
   display what continues. Withdrawal uses `auth.deny` in place through a
   complete candidate, with plan guards for plugins and CORS. Before #587 offers
   guided withdrawal, a **dedicated outermost, address-independent withdrawal
   gate** with a sparse typed operation is delivered together with #563/#562
   **retirement of connections on narrowing** as one capability. No periodic
   connection age and no general session-termination API.
5. **Withdraw in place.** Withdrawal never deletes a route.
6. **Separation, trust and cache.** One Jul listener per external enforcement
   domain; same-host processes are inside the trusted boundary, otherwise
   provider entries need a Jul-side check (SH-11). No authorization
   decision-maker — application, forward-auth service or validator — trusts
   connector identity headers; provider identity is accepted only as a
   cryptographically validated token (SH-12). Restricted and private entries
   keep Jul's response cache off in the first release.
7. **Three layers never merge.** Declared intent, configured posture and observed
   evidence are reported separately, with freshness and vantage. Ambiguous
   outcomes, including a lost apply ledger after restart, are reported as
   unknown and never retried automatically. File-owned changes stay unconfirmed
   until a stateless postcondition returned by preview is evaluated against the
   serving revision; Jul stores no preview state. The view is versioned
   (`sharing_view_version`) and its closed codes are never repurposed.
8. **Outcome gate (SD-06).** #585 predeclares targets for decisions, handoffs and
   interventions; #587 claims improvement only with zero dangerous failures
   across the six rubric tasks; if provider handoffs dominate, #588 returns GO
   (one provider vertical slice), REDUCE or DEFER on that evidence.
9. **Classification.** Operational enhancements composing existing capabilities,
   plus the two bounded tranches above. Provider lifecycle (#588), group sharing
   (#589), native application login, provider mutation and invitations remain
   separately gated.

## Reversibility and one-way doors

| Decision | Classification | Why | Exit or migration |
| --- | --- | --- | --- |
| Derived view (1) | Reversible until a surface or runbook consumes it; compatibility-relevant afterwards | operators, tests and rubrics encode its semantics | `sharing_view_version`; additive fields; side-by-side replacement; closed codes never repurposed |
| `sharing` block names and `mode` values (2) | **One-way once shipped** | public TOML, generated contracts, GitOps files | additive fields only; a rename needs dual-read and a deprecation window |
| Intent findings as warnings (2) | Reversible to relax; costly to tighten into rejections | file-owned pipelines would break | tightening needs its own decision and notice |
| Coarse private-network mode (3) | Costly to tighten once displayed | deployments rely on network-only audiences | migration notice; Jul checks stay optional |
| Withdrawal promise (4) | **One-way to weaken**; additive to strengthen | security commitment | complete withdrawal strengthens it |
| Withdrawal gate field (4) | **One-way once shipped** | public schema and typed operation | settled by an ADR amendment in its stream |
| Retirement on narrowing (4) | Reversible until merged; costly afterwards | operators will rely on it | if infeasible, gated claims stay unshipped and §8.2 of the spec remains |
| Withdraw in place (5) | Mechanism reversible; "never delete" one-way to weaken | safety invariant | — |
| One listener per enforcement domain (6) | Costly to relax | topologies built around it | needs a separately decided SNI/Host binding guarantee |
| Restricted cache off (6) | Reversible to relax with evidence | a relaxation risks disclosure | cache-owner decision with partitioned keys |
| Vocabulary in the Console | Reversible until #587 freezes it | — | #585 comprehension check first |
| Public exposure of an entry | **Irreversible disclosure** | content seen cannot be unseen | stated in every widening plan |
| External state (provider policy, DNS, sessions) | Not rolled back by Jul | outside Jul's authority | rollback plans show their audience delta; unknown external outcomes stop and reconcile |

## Consequences

**Positive.** The product can answer "who is this for?" from the configuration
and warn before a change contradicts it. Withdrawal becomes complete for
Jul-routed traffic without a session store or provider integration. Successors
implement and verify without a new state owner. File-owned and expert TOML users
lose nothing; intent is optional.

**Negative and accepted.** Two runtime tranches occupy the shared-code stream
before #587. Field names become public before first-use evidence. #581's
evidence uses a build newer than v2.2.0. Until complete withdrawal lands, open
connections of a withdrawn entry continue unless the operator restarts Jul or
stops a connector. Provider paths need more listeners. Restricted entries cannot
use Jul's response cache.

**Neutral.** NGINX migration disposition for this design: not applicable. Each
runtime tranche records its own.

## Alternatives considered

| Alternative | Why not |
| --- | --- |
| First-class application resource or separate store | second desired state; conflicts with ADR 0019 I2 |
| Top-level `[[applications]]` block referencing routes | needs IDs everywhere; dangling references; two edit sites |
| Defer held intent to #587 (SD-01's original timing) | product could not answer "who is this for?" during #581–#585; reviewers A-3/U-1; maintainer chose SD-05 B+ |
| Require `route_id` globally | contradicts ADR 0019 §4.2 |
| Application scope inferred from upstreams or roots | hidden identity; can widen withdrawal blast radius (A-2/U-2) |
| Periodic maximum connection age | disconnects everyone; does not end upgraded streams in comparable gateways |
| Defer all session work | leaves "who can access" permanently partial; maintainer chose D+E |
| `location_set_auth` or the `deny` action for withdrawal | drops the credential or the target; still inside plugins and preflight |
| Native Cloudflare token validation in Jul now | not needed while `cloudflared` validates; left to #583 evidence |
| Host-only separation of provider and public entries | no SNI-to-Host check; first-block fallthrough |

## Related

[ADR 0012](0012-oss-open-core-boundary.md), [ADR 0014](0014-operability-surfaces.md),
[ADR 0015](0015-managed-apply-terminal-ledger.md),
[ADR 0016](0016-inbound-identity-and-backend-peer-trust.md),
[ADR 0018](0018-bounded-route-matching-and-response-policy.md),
[ADR 0019](0019-configuration-authority-generated-contracts-and-resource-identity.md),
[ADR 0022](0022-admin-console-oidc.md); [specification](../specs/application-sharing.md);
[known limitations](../known-limitations.md#long-lived-connections).
