from pathlib import Path


def replace_once(path: str, old: str, new: str) -> None:
    p = Path(path)
    text = p.read_text()
    count = text.count(old)
    if count != 1:
        raise SystemExit(f"{path}: expected exactly one replacement target, found {count}")
    p.write_text(text.replace(old, new, 1))


strategy = r'''# Hot-reload strategy and selection criteria

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
'''
Path("docs/hot-reload-strategy.md").write_text(strategy)

# Link the strategy from the documentation index.
replace_once(
    "docs/index.md",
    "- **[Reload, staging, authority and rollback](reload-semantics.md)** —\n  transactional reload, planned restart, managed/file-owned authority and\n  generation lifetimes.\n",
    "- **[Reload, staging, authority and rollback](reload-semantics.md)** —\n  transactional reload, planned restart, managed/file-owned authority and\n  generation lifetimes.\n- **[Hot-reload strategy and selection criteria](hot-reload-strategy.md)** —\n  why a field is selected for live transition or deliberately kept behind a\n  safe restart boundary.\n",
)

# Egress: preserve current truth, record selected future state separately.
replace_once(
    "docs/egress.md",
    "The policy is built once from the **startup** configuration; changing `[egress]`\ntakes effect after a **restart** (like listener bind-time settings). This keeps\nthe guard consistent for the process lifetime, including the discovery refreshers\nthat run for the whole run.\n",
    "The policy is built once from the **startup** configuration; changing `[egress]`\ncurrently takes effect after a **restart**. The machine-authoritative lifecycle\nregistry therefore still classifies `egress.enabled` and `egress.allow` as\n`restart_required`.\n\n**Selected evolution (#94).** Dynamic egress policy is now selected for the final\nruntime-dynamics tranche. Selection does not change current behavior. The field\nwill be promoted only when auth, discovery, WASM fetch, ACME/OCSP and their\nreusable HTTP transports all become generation-correct: work admitted after\nPublish must use the candidate policy and must not reuse a connection pool\ncreated under an older policy. A pointer-only policy swap is explicitly\ninsufficient because it could make the configuration say a destination is\nblocked while a new operation still reaches it through an old keep-alive/H2\nconnection. See [hot-reload strategy](hot-reload-strategy.md) and #94.\n",
)

# OTel: distinguish current restart behavior from the selected ratio-only change.
replace_once(
    "docs/otel.md",
    "1. **Tracing settings require a restart.** Changing `[observability.tracing]`\n   after startup emits a warning and keeps the running tracer; a restart is\n   needed to pick up new endpoint, sample ratio, or exporter type.\n",
    "1. **Tracing settings currently require a restart.** Changing\n   `[observability.tracing]` after startup keeps the running tracer. #99 is now\n   **selected with reduced scope** to make only `sample_ratio` hot-reloadable:\n   new root spans after Publish will use the new ratio while parent sampling\n   decisions remain authoritative, without rebuilding the provider/exporter.\n   Until that implementation lands, `sample_ratio` remains `restart_required`.\n   `enabled`, `endpoint`, `exporter`, `service_name`, and `insecure` remain\n   deliberately restart-bound in this tranche. See\n   [hot-reload strategy](hot-reload-strategy.md).\n",
)

# Reload guide: link the selection rubric near the canonical lifecycle statement.
replace_once(
    "docs/reload-semantics.md",
    "> Mixed candidates remain whole-candidate operations: Jul.IA does not silently\n> publish a hot subset while another field is staged or restart-bound.\n",
    "> Mixed candidates remain whole-candidate operations: Jul.IA does not silently\n> publish a hot subset while another field is staged or restart-bound.\n>\n> Why a field is promoted to `hot_reload` or deliberately left behind a restart\n> boundary is documented in [hot-reload strategy and selection\n> criteria](hot-reload-strategy.md). Selection never changes this document's\n> descriptive runtime truth ahead of implementation.\n",
)

# Reload guide: PostCommit prose had fallen behind the real OnReloaded hook.
replace_once(
    "docs/reload-semantics.md",
    "9. **PostCommit** — apply dynamic side effects: log level, GOMAXPROCS, and\n    stream-proxy reload.\n",
    "9. **PostCommit** — apply committed dynamic side effects that do not need a\n   prepared resource: log level/format, metrics host-label mode, cache scalar\n   policy/capacity, GOMAXPROCS, and stream-proxy reload.\n",
)

# Reload guide: cert/key are hot since #100; they must not appear in the restart list.
replace_once(
    "docs/reload-semantics.md",
    "- `servers.*.tls.enabled`, `.min_version`, `.cert`, `.key`;\n",
    "- `servers.*.tls.enabled`, `.min_version`;\n",
)
replace_once(
    "docs/reload-semantics.md",
    "All of them are compared per listen address, so adding or removing an unrelated\nlistener never produces a restart-required verdict for an address nobody edited.\n\n`servers.*.http3.alt_svc_max_age` is the one HTTP/3 leaf that is **not** in\n",
    "All restart-bound listener fields above are compared per listen address, so\nadding or removing an unrelated listener never produces a restart-required\nverdict for an address nobody edited. Static `servers.*.tls.cert` and `.key`\nare intentionally absent: #100 prepares and atomically publishes a candidate\ncertificate provider on the retained listener, so both are `hot_reload`.\n\n`servers.*.http3.alt_svc_max_age` is the one HTTP/3 leaf that is **not** in\n",
)

# Remove the obsolete duplicate lifecycle section. The earlier registry-backed
# five-class section is authoritative and remains in place.
stale_lifecycle = """## Lifecycle classification: single source of truth\n\nThe authoritative classification is in\n[`internal/lifecycle/lifecycle.go`](../internal/lifecycle/lifecycle.go) and is\nmirrored in [`docs/config-lifecycle.yaml`](config-lifecycle.yaml). The three\nclasses are:\n\n- **hot_reload** — takes effect on the next successful reload.\n- **restart_required** — takes effect only after a process restart. The admin\n  apply path returns HTTP 409 with `restart_required: true`; SIGHUP/file-watch\n  set `LastReload.OK=false`.\n- **new_listener_only** — honored for a brand-new listen address on reload;\n  changing the property on an already-bound listener is restart-required.\n\nLifecycle checks compare **effective values** (secret references resolved,\nfile-backed secrets digested, `worker_threads` auto resolved to the effective\nGOMAXPROCS cap). This prevents a saved secret-reference change from hiding a\nreal structural change and detects file-content rotation. Hot-reloadable\nfields such as `worker_threads` are diffed against the live effective value so\nthat a change is applied on the next successful reload.\n\n"""
replace_once("docs/reload-semantics.md", stale_lifecycle, "")

# Restart category descriptions: current truth plus the two selected gaps.
replace_once(
    "docs/reload-semantics.md",
    "- **TLS handshake parameters on an existing listener** — minimum TLS version,\n  certificates, and the **mtls** client-authentication bundle (mode, CA bundle,\n  SAN allow-list, CRL) are baked into the listener's TLS config. **http3**\n  `enabled` and `h2c` are likewise decided when the address binds; `http3`\n  `alt_svc_max_age` is the one exception — see below.\n- **Tracing** — the OpenTelemetry pipeline is wired once at startup.\n",
    "- **TLS handshake parameters on an existing listener** — minimum TLS version\n  and the **mtls** client-authentication bundle (mode, CA bundle, SAN allow-list,\n  CRL) are baked into the listener's TLS config. Static certificate/key content\n  is the deliberate exception: #100 hot-reloads it through a prepared dynamic\n  certificate provider. **http3** `enabled` and `h2c` are likewise decided when\n  the address binds; `http3.alt_svc_max_age` is hot — see below.\n- **Tracing** — the provider/exporter pipeline is wired once at startup, so all\n  tracing fields are currently restart-bound. #99 is selected with reduced\n  scope to make only `observability.tracing.sample_ratio` hot; the other tracing\n  fields remain deliberately restart-required.\n",
)
replace_once(
    "docs/reload-semantics.md",
    "- **Egress allow-list** — the outbound dial policy is built once at startup.\n- **Admin server** — listener, rate limits, history, plugin-upload, and\n  audit-log settings are baked in at startup. `admin.token` and the RBAC policy\n  (including its `admin.rbac.enabled` toggle) are the exception: both hot-reload\n  via the same prepared atomic authentication snapshot, so a rotated token or\n  policy is live for the very next request after a successful reload and the\n  prior token is rejected immediately — no restart, no overlap window (#95;\n  see [config-lifecycle.yaml](config-lifecycle.yaml)).\n",
    "- **Egress allow-list** — the outbound dial policy is currently built once at\n  startup. #94 is selected to make `egress.enabled` and `egress.allow` dynamic,\n  but they remain `restart_required` until every auth/discovery/plugin/PKI\n  consumer and reusable transport is generation-correct.\n- **Admin structural resources** — `admin.enabled`, `admin.listen`, history\n  directory/retention, TLS protocol mode/minimum version and admin mTLS handshake\n  policy remain startup-owned. In contrast, Console/plugin-upload policy, admin\n  request/SSE limits, durable audit sink path/rotation, `admin.token`, RBAC and\n  admin static certificate/key are all hot-reloadable; see the generated\n  lifecycle reference for the exact leaves.\n",
)

# Add an explicit selected-gap section without changing current lifecycle truth.
replace_once(
    "docs/reload-semantics.md",
    "Adding a brand-new `listen` address is *not* restart-required: the reload binds\nit fresh. Only changes to an address the server is already serving are gated.\n",
    "### Selected runtime gaps (not current behavior)\n\nTwo remaining restart-bound gaps are selected for implementation after the\npost-#160 value/peer audit: #99 will hot-reload only\n`observability.tracing.sample_ratio` without replacing the tracing pipeline, and\n#94 will make `[egress]` generation-correct across every auxiliary outbound\nconsumer and reusable connection pool. Their present registry classification is\nunchanged until those implementations land. See\n[hot-reload strategy](hot-reload-strategy.md) for the decision rubric, target\ncontracts and effort.\n\nAdding a brand-new `listen` address is *not* restart-required: the reload binds\nit fresh. Only changes to an address the server is already serving are gated.\n",
)

# Known limitations: fix the obsolete lifecycle authority pointer and expose the
# two selected-but-not-yet-live gaps alongside the current restart boundaries.
replace_once(
    "docs/known-limitations.md",
    "  [`internal/lifecycle/lifecycle.go`](../internal/lifecycle/lifecycle.go) and\n",
    "  [`internal/lifecycle/registry.go`](../internal/lifecycle/registry.go) and\n",
)
replace_once(
    "docs/known-limitations.md",
    "without rebinding the QUIC socket (#161) — `http3.enabled` itself still\nrequires a restart, since it changes whether a UDP listener exists at all.\n",
    "without rebinding the QUIC socket (#161) — `http3.enabled` itself still\nrequires a restart, since it changes whether a UDP listener exists at all.\n\nTwo current restart boundaries are now **selected for removal**, but remain\nlimitations until their implementation PRs merge: #99 is reduced to\n`observability.tracing.sample_ratio` only (**S/M, 4–7 focused engineer-days**),\nand #94 selects generation-correct `egress.enabled`/`egress.allow` (**L, 15–25\nfocused engineer-days / ~3–5 focused weeks**). All other tracing fields stay\nrestart-bound in this tranche, and egress is not promoted until every auxiliary\noutbound consumer and old connection pool obeys the candidate generation. See\n[hot-reload strategy](hot-reload-strategy.md).\n",
)

# Self-clean so the branch contains only durable documentation after the helper
# workflow commits the reconciliation.
Path("scripts/hot_reload_doc_reconcile_20260911.py").unlink(missing_ok=True)
Path(".github/workflows/hot-reload-doc-reconciliation.yml").unlink(missing_ok=True)
