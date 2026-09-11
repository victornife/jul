# Hot-reload strategy and selection criteria

This document explains **how Jul.IA decides which configuration changes should
become live runtime transitions**. It complements the descriptive, machine-
authoritative lifecycle registry and the transactional mechanics in
[reload-semantics.md](reload-semantics.md).

> **Current truth versus selected work.** The Go lifecycle registry remains the
> sole authority for what the current binary actually hot-reloads. A selected
> issue is normative roadmap intent only: its field stays `restart_required`
> until production code, tests, generated lifecycle artifacts and documentation
> land together.

## Objective

Jul does not optimize for the percentage of configuration fields labelled
`hot_reload`, and it does not pursue NGINX/Caddy/Envoy/HAProxy/Traefik parity as
an end in itself. The objective is narrower:

> Remove operationally meaningful restarts when the live transition can be made
> truthful, state-safe and proportionate to its permanent maintenance cost.

A safe staged restart remains a valid final architecture for rare or structural
changes.

## Current lifecycle baseline

At `main@5b00a26db7450000abf462b079c87c89201723d1`, the generated lifecycle
inventory contains 302 configurable leaves: 250 `hot_reload`, 37
`restart_required`, 8 `new_listener_only`, 4 `ignored_deprecated`, and 3
`validation_rejected_reserved`.

These numbers are descriptive, not a target score. The current counts are always
available from [the generated lifecycle reference](generated/config-lifecycle.md)
and may change as selected work lands.

## Selection rubric

A gated runtime transition is selected only when the aggregate case is strong
across these dimensions:

1. **Operational frequency and restart cost.** Does it remove a real operator
   interruption or merely make an unusual structural edit more convenient?
2. **Security and incident-containment value.** Can changing the setting live
   materially reduce exposure or shorten recovery time?
3. **Truthful transition semantics.** Can Jul state an exact Publish boundary
   after which newly admitted work really observes the new policy, rather than
   merely changing the configuration projection?
4. **State and resource continuity.** Which counters, buckets, connection pools,
   trace decisions, workers, filesystem handles or identities must survive, and
   which belong to the retired generation?
5. **Cross-generation complexity and failure surface.** H1/H2/H3 reuse,
   background workers, session state, post-Publish failure and retirement must
   be accounted for explicitly.
6. **Permanent maintenance and test burden.** A one-time implementation win is
   not sufficient if it creates disproportionate complexity in every future
   change.
7. **Architecture leverage.** Prefer work that strengthens an existing concrete
   generation/Prepare-Publish-Retire seam without creating a speculative second
   runtime framework.
8. **Peer/product evidence.** Mature proxy behavior is evidence that an operator
   workflow matters; it is not a mandate to make every field dynamic.

The reload standard remains unchanged: all fallible preparation happens before
Publish, Publish is bounded/no-fail by construction, Abort leaves the live
runtime untouched, and Retire is bounded and cannot turn an applied change into
`not_applied`.

## Selected final gaps — 2026-09-11

A post-#160 source audit and peer review selected two additional investments.
Neither is hot in the current binary yet.

| Gap | Current lifecycle | Target | Why selected | Estimated effort | Risk |
| --- | --- | --- | --- | --- | --- |
| `observability.tracing.sample_ratio` (#99) | `restart_required` | Hot-update the root sampling ratio without rebuilding the provider/exporter | High incident/cost-control value with a very small permanent runtime surface | **S/M: 4–7 focused engineer-days** | Low–medium |
| `egress.enabled`, `egress.allow` (#94) | `restart_required` | Generation-correct policy for every auxiliary outbound consumer and its pools | Security containment and truthful policy enforcement without restarting healthy traffic | **L: 15–25 focused engineer-days / ~3–5 focused weeks** | High correctness/security |

### `tracing.sample_ratio`: selected reduced scope

The current OTel provider is created with a parent-based ratio sampler. The
selected design keeps the provider, exporter, propagator and resource stable and
replaces only the root ratio decision with a dynamically updateable sampler.

Target contract:

- a new root span begun after the successful reload boundary uses the new ratio;
- an incoming or local parent sampling decision remains authoritative;
- an already-started trace cannot flip sampled/not-sampled midway through;
- no exporter/provider rebuild, connection churn, flush or retirement is caused
  by a ratio-only change;
- a candidate that also changes another restart-bound tracing field remains a
  whole-candidate restart/stage operation — Jul never partially applies only the
  ratio.

The rest of `[observability.tracing]` — `enabled`, `exporter`, `endpoint`,
`service_name` and `insecure` — remains deliberately restart-bound in this
tranche. Making those fields dynamic would require provider generations,
request-context tracer ownership and bounded exporter retirement, which has a
much worse value/complexity ratio.

Peer evidence supports treating sampling as an operator runtime knob: Envoy has
runtime random sampling, and HAProxy 3.4+ exposes OpenTelemetry rate adjustment
through its Runtime API. Those products do not define Jul's implementation;
they validate the operational use case.

### Dynamic egress policy: selected full security contract

The current `[egress]` policy is constructed once at startup and captured by
multiple lifetimes: JWT/forward-auth clients, service-discovery workers, WASM
fetch clients, ACME/OCSP clients and their reusable HTTP transports.

A pointer-only policy swap is **not acceptable**. If policy B removes a
destination but a new operation can reuse an HTTP/1.1 keep-alive or HTTP/2
connection created under policy A, the configuration would claim a security
boundary that the runtime does not enforce.

Target contract:

- an auth/discovery/plugin/PKI operation admitted after Publish uses the
  candidate policy;
- it cannot reuse a transport/connection pool created under an older policy;
- work already admitted under the old generation may finish under the policy it
  captured;
- old workers/transports retire exactly once and within a bounded lifetime;
- disabled-mode proxy compatibility, DNS-rebinding defenses, redirect checks,
  plugin-local SSRF intersection and bounded telemetry remain intact;
- lifecycle promotion occurs only when **every configured consumer class** has
  generation-correct evidence.

HAProxy's runtime ACL transactions are useful evidence that live policy changes
are an established operator workflow. Jul's egress boundary is broader than one
request ACL, so its completion bar remains the full consumer/transport matrix in
#94.

## Why the remaining structural gaps are different

This selection does not authorize universal hot reload. Listener protocol mode,
admin-listener relocation, cache backend identity, ACME account/cache identity,
history backend replacement and similar structural transitions may remain
restart-bound when their operating frequency is low and a correct live handover
would add disproportionate permanent complexity.

A red `restart_required` row is therefore not automatically technical debt. It
is debt only when the value/risk analysis says the restart boundary no longer
meets the product's operational contract.

## Sequence

The final selected runtime-dynamics edge is:

```text
#99 — sample_ratio-only hot reload
  ↓
#94 — generation-correct egress hot reload
  ↓
explicit retain/defer decisions for remaining gated fields
  ↓
#106 — integrated runtime-dynamics closure
  ↓
#88 — portfolio closure
```

The small tracing change goes first because it can close independently without
introducing tracing-provider generations. Egress follows as the final large
security-sensitive runtime transition so #106 can certify the complete selected
tranche once, rather than repeatedly reopening integrated evidence.

## External reference points

These links are decision evidence, not lifecycle authority:

- Envoy tracing and runtime random sampling:
  <https://www.envoyproxy.io/docs/envoy/latest/intro/arch_overview/observability/tracing>
- HAProxy OpenTelemetry runtime controls:
  <https://www.haproxy.com/documentation/haproxy-configuration-tutorials/alerts-and-monitoring/opentelemetry/overview/>
- HAProxy Runtime API:
  <https://www.haproxy.com/documentation/haproxy-runtime-api/>
- HAProxy transactional ACL updates:
  <https://www.haproxy.com/documentation/haproxy-runtime-api/reference/prepare-acl/>

## Sources of truth

- **Current behavior:** `internal/lifecycle/registry.go` and its generated
  lifecycle mirrors.
- **Reload transaction semantics:** `docs/reload-semantics.md` and
  `internal/server/reload_plan.go`.
- **Selection decisions:** #88, with focused implementation contracts in #94
  and #99.
- **Volatile execution order:** #62.

Do not edit generated lifecycle output to express a future decision. Promote a
field only in the implementation PR that proves its live semantics.
