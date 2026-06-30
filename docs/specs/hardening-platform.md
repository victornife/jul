<!-- Engineering execution spec for the pre-1.0 Hardening & platform backlog (HP-01..HP-07).
     Source-of-truth for the strategic items deferred out of the pre-1.0 hardening pass
     (Console v2 robustness Phases 1-4) plus the structured-config parity patch-ops.
     Companion to ../roadmap/ and ../specs/README.md. This is DESIGN-AHEAD, not committed
     work: nothing here has shipped. Update when the design changes; bump Version + changelog. -->

# JUL Engineering Execution Plan — Hardening & platform (pre-1.0)

> Version 1.0 · Updated 2026-06-28
>
> Scope note: this is the home for cross-cutting robustness work that was
> **scoped out** of the pre-1.0 hardening pass (the Console v2 robustness phases:
> reload truthfulness, admin self-lockout guard, error taxonomy, build-tag
> degradation, a11y, edge-timeout/observability fixes, and ops/supply-chain
> readiness) so the items are **tracked, not lost**. None of HP-01..HP-07 has
> shipped. Each entry records what the hardening pass **already delivered** versus
> what remains, so this spec does not re-litigate solved problems.

## Relationship to the roadmap

These items are **pre-1.0 robustness**, distinct from the demand-gated
[vision horizon](../roadmap/#vision-horizon--demand-gated). Several are precursors
to committed roadmap features — notably **HP-02 Console RBAC** feeds
[Y3-02 (Console RBAC + SSO/SAML/OIDC)](../roadmap/). The backlog table lives in
[roadmap/README.md](../roadmap/); this spec is the design source-of-truth behind it.

| ID | Item | Precursor to | Effort |
| --- | --- | --- | --- |
| HP-01 | Unified reload transaction + `reload_timeout` | — | M |
| HP-02 | Console RBAC + multi-user | [Y3-02](../roadmap/) | L |
| HP-03 | Metric-cardinality & relabel strategy | — | M |
| HP-04 | Pre-commit hooks / local gate parity | — | M |
| HP-05 | Container & process-supervision hardening | [Y3-06](../roadmap/) | M |
| HP-06 | Structured-config parity patch-ops | [Y2-09](../specs/console-v2.md) | L |
| HP-07 | SSRF allow-list hardening (defense-in-depth) | — | M |

Effort: **M** ≈ weeks · **L** ≈ ~a quarter (per the roadmap T-shirt sizing).

---

## HP-01 — Unified reload transaction + `reload_timeout`

**Objective.** Make a config apply a single, time-bounded transaction whose result
truthfully reports per-subsystem (HTTP + stream) outcome, and which cannot stall
indefinitely.

**Already delivered (pre-1.0 hardening).** Apply is *already* truthful at write
time: the stream config is preflighted and bind-probed symmetrically with HTTP
listeners before the file is written (`stream.PreflightListeners`,
`server.PreflightListeners`, wired through `admin/api.go`'s `WriteConfigRaw`), and
the stream `Reload` is transactional with rollback on bind failure. See
[reload-semantics.md](../reload-semantics.md) and
[stream-proxy.md](../stream-proxy.md#hot-reload). The narrow apply-time
truthfulness gap that motivated the original P1 finding is **closed**.

**Remaining.** Two pieces were deliberately deferred as speculative against the
current preflight design and are parked here:

1. **`[global].reload_timeout`** (default `10s`) — wrap the post-write reload in a
   `context.WithTimeout` so a wedged reload surfaces as an error instead of a UI
   that says *applied* while the old config keeps serving.
2. **`ReloadResult` plumbing + apply-response shape** — extend
   `POST /api/config/apply` from `{ok,pending_reload,message,status,version}` to
   add `reload:{http_ok,stream_ok,error,timed_out,duration_ms}`, and add
   `reload_pending` / `last_reload_error` to `FeatureStatus`/`runtimeStatus`.

**Design.**

```toml
[global]
# Maximum time a hot reload may run before it is reported as failed. The
# previous config keeps serving; the apply result carries timed_out=true.
reload_timeout = "10s"
```

- Thread a single `context.Context` from the apply handler through the HTTP swap
  and `stream.Reload`; cancel on timeout. Because both subsystems already preflight
  and roll back, a timeout leaves the *serving* config unchanged.
- The apply handler returns the structured `reload` block; the Console surfaces
  `timed_out` / `error` distinctly from a clean apply.

**Tasks.** schema field + validation (duration ≥ 0, 0 = unbounded); reload context
plumbing in `cmd/jul/main.go`; `ReloadResult` struct + handler wiring in
`admin/api.go`; client schema (`ValidationResult`/apply result) + Console rendering.

**Tests.** unit: timeout fires and reports `timed_out`; clean reload reports
`http_ok && stream_ok`; partial failure (stream bind race) reports per-subsystem.
UI: apply result renders `timed_out`/`error` states.

**DoD.** apply result is per-subsystem truthful **and** time-bounded; docs in
[reload-semantics.md](../reload-semantics.md) + [core-http.md](../core-http.md)
updated; changelog row.

**Risks.** a too-low timeout on a large config could false-positive; default `10s`
with operator override mitigates.

---

## HP-02 — Console RBAC + multi-user

**Objective.** Replace the single shared admin bearer token with named principals,
roles, and scoped tokens, so multiple operators can use the console under
least-privilege with attributable audit entries.

**Current state.** The console authenticates with one constant-time-compared admin
token (`server.go`); there is one implicit super-user. The
[audit log](../console.md) records actions but cannot attribute them to a person.

**Design (precursor to [Y3-02](../roadmap/)).**

- Principals with roles: at minimum `viewer` (read-only projections), `operator`
  (apply config, no admin-surface changes), `admin` (everything incl. the
  self-lockout-guarded admin edits — see [SECURITY.md](../../SECURITY.md)).
- Scoped, individually revocable tokens (hashed at rest); per-token rate limits.
- Enforce at the API boundary (function-level + object-level checks), not in the
  UI. The UI hides what a role cannot do; the server is the source of truth.
- Audit entries carry the principal id.

**Config sketch.**

```toml
[admin.rbac]
enabled = true
[[admin.rbac.principals]]
name  = "alice"
role  = "operator"
token = "${secret:console/alice}"
```

**Tasks.** principal store + role model; middleware authz (deny-by-default);
per-endpoint required-role table; audit principal attribution; Console: login
identity, role-gated controls, token management panel.

**Tests.** role matrix (each role × each endpoint → allow/deny); object-level
checks (operator cannot touch admin surface); audit attribution; token
revocation takes effect immediately.

**DoD.** documented role matrix; deny-by-default proven by tests; aligns with the
Y3-02 SSO/SAML/OIDC direction (RBAC is the local-token precursor).

**Risks.** scope creep toward full IdP — keep HP-02 to local principals/roles;
external identity stays Y3-02.

---

## HP-03 — Metric-cardinality & relabel strategy

**Objective.** Keep Prometheus cardinality predictable as deployments scale.

**Already delivered.** The `host` label on `jul_http_requests_total` is **opt-in**
(`[observability.metrics] host_label`, default off) with relabel guidance — see
[core-http.md](../core-http.md#metrics). This removed the largest unbounded-label
risk.

**Remaining.** A documented, enforced strategy rather than per-label fixes:

- Inventory every metric's label set and its cardinality driver (route, upstream,
  status class, method).
- Bounded label sets: collapse high-cardinality dimensions (e.g. raw path) to
  bounded ones (route id / status **class**), with explicit opt-in for finer
  labels behind the same pattern as `host_label`.
- Ship a recommended `metric_relabel_configs` snippet and document a soft
  series-count budget per scrape.

**Tasks.** label inventory in [core-http.md](../core-http.md) +
[stream-proxy.md](../stream-proxy.md); apply the opt-in pattern to any remaining
unbounded label; relabel cookbook.

**Tests.** assert default label sets are bounded (no raw path/query in default
labels); opt-in toggles add exactly the documented label.

**DoD.** every default label is bounded by construction; cardinality cookbook
published; no dashboard breakage (additive, opt-in).

---

## HP-04 — Pre-commit hooks / local gate parity

**Objective.** Let contributors run the CI gate locally before pushing to `main`,
so red builds are caught pre-push.

**Already delivered (CI side).** CI enforces gofmt, `go vet`, golangci-lint, race,
govulncheck, a [coverage floor](../../SECURITY.md#dependencies--supply-chain),
benchmark/fuzz/soak smoke, the console size + drift gates, and tagged-release
SBOM/provenance/signing. The gap is purely *local* parity.

**Design.** A repo-managed hook (committed under `scripts/` and installed via a
`make hooks` target, or a `.pre-commit-config.yaml` if adopting the `pre-commit`
framework) that runs the fast subset on commit and the fuller subset on push:

- pre-commit: `gofmt -l` (CRLF-aware on this checkout — see the
  [commit-gate note](../../SECURITY.md)), `go vet`, changed-package `go test`,
  console `pnpm lint`/`typecheck` when `internal/admin/ui` changed.
- pre-push: full `go test` (lean), the dist drift guard, size gate.

**Tasks.** hook scripts; `make hooks` installer; opt-out env for emergencies (never
`--no-verify` silently); doc in README/SECURITY.

**Tests.** hook smoke (a deliberately unformatted file is rejected; a clean tree
passes) in CI so the hook itself does not rot.

**DoD.** one command installs the hooks; the local gate mirrors the CI gate's fast
path; documented.

**Risks.** slow hooks get bypassed — keep pre-commit to the fast subset; heavy
checks run pre-push or in CI only.

---

## HP-05 — Container & process-supervision hardening

**Objective.** Close the container/orchestration gaps left open in the ops pass.

**Already delivered.** The systemd unit gained `LimitNOFILE`/`TasksMax`, a
`StartLimitBurst` crash-loop guard, and commented `MemoryMax`/`CPUQuota` examples;
[Dependabot](../../.github/dependabot.yml) tracks the Docker base image; releases
ship a signed SBOM + provenance. See [README deployment](../../README.md#deployment)
and [SECURITY.md](../../SECURITY.md#dependencies--supply-chain).

**Remaining (deferred from the ops pass, with their blockers).**

1. **Base-image digest pinning** — pin `golang:…` and the distroless runtime by
   `@sha256:` digest, not just tag. *Blocker resolved by Dependabot:* its docker
   ecosystem can raise the digest bumps, so pinning becomes sustainable rather than
   a one-off that rots. Requires fetching the current digests from the registry.
2. **Self health target + HEALTHCHECK** — add a `jul healthcheck` subcommand (or a
   minimal `/healthz`/`/readyz` admin endpoint) so a **shell-less distroless**
   image can declare a `HEALTHCHECK` that execs the binary itself. *Blocker:* jul
   currently exposes **no** self health endpoint/subcommand, so a `HEALTHCHECK`
   today would have no valid target — the feature must exist first.

**Design.** `jul healthcheck` dials the configured admin/health listener and exits
0/1 (no shell needed); `HEALTHCHECK CMD ["/usr/local/bin/jul","healthcheck"]`.
`/readyz` returns ready only after the first successful config load + listener
bind (ties to HP-01's reload result).

**Tasks.** `cmd/jul` `healthcheck` subcommand; optional `/healthz`/`/readyz`
handlers; Dockerfile digest pins + `HEALTHCHECK`; README/Dockerfile docs.

**Tests.** `healthcheck` returns 0 when serving, non-zero when down; `/readyz`
flips only after a successful bind; Dockerfile builds (CI, where Docker is
available) and the healthcheck passes.

**DoD.** distroless image self-reports health; base images digest-pinned and
Dependabot-maintained.

**Risks.** a health endpoint widens the admin surface — keep it unauthenticated
only on a loopback/dedicated port, documented in [SECURITY.md](../../SECURITY.md).

---

## HP-06 — Structured-config parity patch-ops

**Objective.** Let the console **create** servers/routes/upstream pools and edit
global tables through structured patch-ops, instead of falling back to the raw
TOML editor for those operations.

**Current state.** The raw TOML editor has **full** parity (validate → diff → apply
→ reload, optimistic concurrency, history/rollback). The structured patch-op layer
(`internal/admin/patch.go`, `client.ts` `ConfigPatch`) is a curated **edit-existing**
subset: it can retarget routes, toggle cache/rate-limit/WAF, edit locations and
upstream backends, etc., but it has **no create op** for a server/route/upstream
pool (creation is a TOML-fragment hand-off to the raw editor) and **no structured
op** for global tables (`[global]`, `[cache]`, `[compression]`, global
`[rate_limit]`, `[admin]`, `[observability.access_log]`).

**Design — new patch-op shapes.** Reuse existing config structs; error if the
target already exists; the admin op stays guarded (ties to the self-lockout guard).

```
upstream_add          {op, upstream, address, weight?, strategy?}
server_add            {op, listen, server_names, tls?}
location_add          {op, listen, server_names, match_set, action}
global_set            {op, global:{worker_threads?,log_level?,log_format?,access_log?,error_log?,shutdown_timeout?}}   // sparse
cache_set             {op, cache:{enabled,max_size?,max_object_size?,default_ttl?,...}}
compression_set       {op, compression:{enabled,encoders?,level?,min_size?,types?}}
rate_limit_global_set {op, rate_limit:RateLimitPatch}
admin_set             {op, admin:{...}, confirm:true}   // GUARDED — ties to the admin self-lockout guard
access_log_set        {op, access_log:{file?/syslog?/rotation?}}
```

**Tasks.** extend `patchRequest`/`applyPatch` (`patch.go`) with the create + global
ops; mirror the `ConfigPatch` union + Zod schema (`client.ts`); structured Console
forms for create + global tables; route `admin_set` through the existing
`confirm_admin` guard.

**Tests.** Go: each new op round-trips (apply → re-parse → assert), and duplicate
targets error; `admin_set` without `confirm` is rejected. UI: each form emits the
documented shape; create-then-edit flows.

**DoD.** the console can configure everything structurally that the raw editor can,
without dropping to TOML; the raw editor remains the escape hatch. Documented in
[console.md](../console.md).

**Risks.** patch-op surface growth — keep ops minimal and reuse config structs;
the raw editor stays the universal fallback so parity gaps degrade gracefully.

---

## HP-07 — SSRF allow-list hardening (defense-in-depth)

**Objective.** Bound jul's *outbound* reach for config-driven fetches even when a
**trusted-but-mistaken** config points them somewhere internal.

**Threat-model context.** Per [SECURITY.md](../../SECURITY.md), **config is
trusted**; request input never widens the attack surface, so the JWKS, forward-auth,
ACME, and discovery fetchers are SSRF-safe *by design* (their destinations come
from config, not from request data). HP-07 is therefore **defense-in-depth**, not a
fix for a request-driven hole — it limits blast radius from operator error or a
compromised config source.

**Design.** An optional egress allow-list (host/CIDR) consulted by the shared HTTP
client used for JWKS (`auth`), forward-auth, ACME directory/order fetches, and
service discovery. Default off (no behavior change); when set, a fetch to a
non-allow-listed destination is refused and logged.

```toml
[global.egress]
# Optional. When non-empty, outbound config-driven fetches (JWKS, forward-auth,
# ACME, discovery) may only reach these hosts/CIDRs. Empty = unrestricted.
allow = ["auth.example.com", "10.0.0.0/8"]
```

**Tasks.** shared egress guard (resolve + CIDR/host match, block link-local/loopback
by default when the list is set); wire into the JWKS/forward-auth/ACME/discovery
dialers; config + validation.

**Tests.** allow-listed host permitted; non-listed blocked; default-off unchanged;
loopback/link-local blocked when the list is active.

**DoD.** opt-in, default-off, no behavior change unless configured; documented as
defense-in-depth in [SECURITY.md](../../SECURITY.md).

**Risks.** false-positives breaking legitimate fetches — default off; clear error
messages naming the blocked destination.

---

## Deferred micro-fixes register

Small, low-priority items confirmed during the pre-1.0 review. Each is a contained
change; parked here so they are tracked, not lost. **All entries below shipped in
v1.1 (2026-06-28); the table is retained as a record.** HP-m3 and HP-m6 were found
already covered when the register was actioned (see notes).

| ID | Item | Where | Resolution |
| --- | --- | --- | --- |
| HP-m1 | `redact` leaves <4-char resolved secrets unmasked | `internal/redact` | ✅ Floor is configurable via `[global] redact_min_secret_length` (default 4), applied during secret resolution. |
| HP-m2 | No upfront `Content-Length` 413 for body limits | body-limit middleware | ✅ A declared Content-Length over the limit is rejected with 413 before the body is read; unknown length still trips via `MaxBytesReader`. |
| HP-m3 | Invalid HTTPS-redirect status silently coerced to 301 | `router.go` `redirectToHTTPS` | ✅ Already validated at parse time (`config.Validate` rejects any `redirect_https` other than 301/308); the runtime coercion is now documented as belt-and-suspenders. |
| HP-m4 | Proxy retry returns `GetBody` error instead of `lastErr` | `internal/handler/proxy.go` | ✅ The retry-rewind path now surfaces the real upstream failure (`lastErr`) instead of the body-rewind error. |
| HP-m5 | `h3GracePeriod` hardcoded `5s` | `http3.go` | ✅ HTTP/3 Close drains on the shutdown context, so it tracks `shutdown_timeout` like the TCP listeners. |
| HP-m6 | Audit CSV export endpoint not wired in the UI | Console Audit panel | ✅ Already wired: the Audit panel has Export JSON/CSV controls backed by `downloadAuditExport` (auth-aware blob download). |
| HP-m7 | Rate-limit headers not surfaced in the Console | Console | ✅ The API client parses `Retry-After` on 429 and the error taxonomy surfaces the wait time ("Wait N seconds, then retry"). |

---

## Cross-references

- Roadmap backlog table: [roadmap/README.md](../roadmap/)
- Reload model: [reload-semantics.md](../reload-semantics.md)
- Console capability matrix: [console.md](../console.md#capability-matrix)
- Security & threat model: [SECURITY.md](../../SECURITY.md)
- Supply-chain & CI gates: [SECURITY.md](../../SECURITY.md#dependencies--supply-chain)
- Maturity bar: [ADR 0003](../adr/0003-maturity-and-ga.md); Console invariants:
  [ADR 0004](../adr/0004-console-ui-invariants.md)

## Changelog

| Version | Date | Change |
| --- | --- | --- |
| 1.1 | 2026-06-28 | Shipped the HP-m* micro-fixes register: HP-m1 configurable redaction floor (`[global] redact_min_secret_length`), HP-m2 upfront Content-Length 413, HP-m4 proxy retry surfaces the upstream error, HP-m5 HTTP/3 drain tracks `shutdown_timeout`, HP-m7 Console surfaces `Retry-After` on 429. HP-m3 (redirect-code validation) and HP-m6 (audit CSV export control) were already covered and are documented as such. Strategic items HP-01..HP-07 remain design-ahead. |
| 1.0 | 2026-06-28 | Initial backlog spec. Captures the strategic items and deferred work parked out of the pre-1.0 hardening pass (Console v2 robustness Phases 1–4): HP-01 unified reload transaction + `reload_timeout`, HP-02 Console RBAC, HP-03 metric-cardinality strategy, HP-04 pre-commit gate parity, HP-05 container/supervision hardening (digest pinning + health target), HP-06 structured-config parity patch-ops, HP-07 SSRF allow-list hardening, plus the HP-m* micro-fixes register. Design-ahead only — nothing here has shipped. |
