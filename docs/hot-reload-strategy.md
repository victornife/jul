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

The final bounded tranche was audited from
`main@bb95de16119102c8fd23e11c908f131d2323a6ab`, whose generated inventory was
302 configurable leaves: 253 `hot_reload`, 34 `restart_required`, 8
`new_listener_only`, 4 `ignored_deprecated`, and 3
`validation_rejected_reserved`.

After the bounded #106 implementation, the same 302 leaves are intentionally
classified as **256 `hot_reload`, 32 `restart_required`, 7 `new_listener_only`,
4 `ignored_deprecated`, and 3 `validation_rejected_reserved`**. The three live
promotions are `rate_limit.max_conns`, `admin.history_keep`, and
`servers.*.tls.acme.ocsp_stapling`; no structural/security-heavy field was
promoted merely to improve a percentage.

These numbers are descriptive, not a target score. The authoritative current
counts remain the generated lifecycle reference and machine registry.

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

## Delivered final bounded tranche — 2026-09-14

The source audit selected only transitions with a clean ownership seam. #99 and
#94 landed first; #106 then closes the programme with three deliberately small
live policies and no listener/provider-generation expansion.

| Gap | Final lifecycle | Implemented contract | Architectural cost |
| --- | --- | --- | --- |
| `observability.tracing.sample_ratio` (#99) | `hot_reload` | Atomic root-ratio update inside one stable provider/exporter pipeline | small/two-way |
| `egress.enabled`, `egress.allow` (#94) | `hot_reload` | Immutable Prepare→Publish egress generations across all Boundary-C consumers | justified high-cost security tranche |
| `rate_limit.max_conns` (#106) | `hot_reload` | Stable listener-owned admission limiter; cap changes affect new admissions only and never terminate admitted connections | small/two-way implementation; admission semantics are a higher-cost compatibility contract |
| `admin.history_keep` (#106/#159) | `hot_reload` | Atomic scalar retention on the existing history backend; tightening prunes only after Publish and failure is advisory | small/two-way; directory identity remains restart-bound |
| `tls.acme.ocsp_stapling` (#106) | `hot_reload` | Stable OCSP wrapper + atomic enable policy around the existing ACME provider/cache | small/two-way; no ACME manager replacement |

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

### Dynamic egress policy: implemented full security contract

#94 implements the stronger generation model selected above rather than a pointer
swap. The process owns one egress `Manager`; Prepare compiles an immutable candidate
and Publish performs the single authoritative generation change. Each consumer
either belongs to that generation (auth and WASM), owns a replaceable worker/client
generation while preserving unrelated backend state (Consul/Kubernetes), or uses a
stable process-lifetime client that dispatches each exchange through the current
generation (ACME/OCSP).

The completion evidence covers the one-way security doors explicitly: real H1 and
H2 reuse attempts after tightening, redirect hops across Publish, concurrent policy
churn under `-race`, discovery cancellation plus epoch-fenced late results, repeated
worker retirement under `goleak`, plugin-local/global SSRF intersection, abort/no-
publish behavior, disabled-mode proxy compatibility, and an enforced ≥90% statement
coverage floor over production code added by the tranche.

The remaining cross-generation behavior is intentional and bounded: work already
admitted by generation A may drain on A's resources, while work admitted after B's
Publish cannot acquire or reuse an A transport/worker. No configuration projection
can therefore claim a destination is blocked while newly admitted work silently
reaches it through a superseded keep-alive/H2 pool.

## Final disposition of the remaining structural gaps

The runtime-dynamics programme is **finished, not paused**. The final source
audit makes an explicit distinction between a potentially useful feature that
loses today's value/complexity contest and a restart boundary that is itself the
preferred architecture.

**Deferred for the current programme:**

- **#93 cache backend identity (`cache.enabled`, `cache.disk_path`)** — route-level
  enablement and scalar cache policy are already hot; backend/filesystem
  generations are not justified now. Revisit if operators repeatedly require
  backend/path changes without restart or reusable state-backend generation
  infrastructure emerges.
- **#101 TLS minimum-version / mTLS policy dynamics** — current TCP and HTTP/3
  already share complete mTLS policy; the stale parity concern is resolved.
  Revisit only if live security-policy tightening becomes a product requirement
  and Jul gains reusable H1/H2/H3 connection-epoch plus session invalidation.
- **#103 broader ACME runtime policy** — HTTP-01 and TLS-ALPN-01 are already
  exclusive/correct, #94 solved ACME/OCSP egress generations, and #106 makes
  OCSP stapling itself hot. Revisit if domain/challenge/provider changes become
  operationally frequent or reusable ACME-manager generation infrastructure is
  justified elsewhere.

**Retained as intentional restart boundaries:**

- **#97 `admin.enabled` / `admin.listen`** — management-plane listener identity,
  self-lockout and dual-endpoint handover are structural and rare.
- **#102 `http3.enabled`** — only UDP/QUIC listener existence remains; Alt-Svc
  max-age is already hot via `DynamicAltSvc`.
- **#104 ACME account/issuer/email/cache identity** — restart provides a useful
  ownership boundary for private account state, issuer/rate-limit domain and
  certificate-cache ownership.
- **#105 TLS/plaintext + h2c transitions** — changing how an already-bound socket
  interprets bytes would require a permanent raw-listener supervisor.
- **#159 `admin.history_dir`** — retention-only is complete; storage relocation
  stays restart-bound. Revisit only if operators need live storage relocation
  and safe filesystem-generation infrastructure exists for another reason.

A `restart_required` row is therefore not automatically technical debt. The
programme intentionally avoids connection epochs, listener supervisors, ACME
manager generations and filesystem generations unless future operational demand
pays for their permanent complexity.

## Sequence

The final selected runtime-dynamics edge is:

```text
#99 — sample_ratio-only hot reload COMPLETE
  ↓
#94 — generation-correct egress hot reload COMPLETE
  ↓
#106 — max_conns + history retention + OCSP policy; final dispositions
  ↓
#88 — portfolio closure; runtime-dynamics programme COMPLETE
```

The final boundary is intentional: future hot-reload work requires a new
operational value signal or architectural leverage. Jul does not continue toward
100% lifecycle coverage for its own sake.

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
