# Console RBAC — design spec (HP-02 / SEQ-09)

> **Status:** Implemented (Phase 3), in security remediation. This document
> remains the authoritative design and permission-matrix reference; the model it
> describes — named principals, predefined **and** custom roles, scoped revocable
> tokens, per-principal audit attribution, and API-boundary enforcement — is now
> built in `internal/rbac` and the admin server. Atomicity of the hot-reload
> policy swap and self-lockout confirmation across every mutation endpoint are
> the subjects of the active remediation pass; do not treat RBAC as
> release-ready until that pass and its exact-head CI evidence close. Precursor
> to roadmap **Y3-02** (SSO/SAML/OIDC); external identity providers remain **out
> of scope** here and deferred to Y3-02.
>
> The permission matrix below is **exhaustive** over every admin endpoint that
> exists today.

## Contents

- [Problem & goals](#problem--goals)
- [Current state](#current-state)
- [Model overview](#model-overview)
- [Principals](#principals)
- [Tokens (credentials)](#tokens-credentials)
- [Permissions](#permissions)
- [Predefined roles](#predefined-roles)
- [Custom roles](#custom-roles)
- [Exhaustive permission matrix](#exhaustive-permission-matrix)
- [Enforcement](#enforcement)
- [Audit attribution](#audit-attribution)
- [Configuration schema](#configuration-schema)
- [Backward compatibility & migration](#backward-compatibility--migration)
- [Threat model & security considerations](#threat-model--security-considerations)
- [Phased implementation plan](#phased-implementation-plan)
- [Open questions](#open-questions)

## Problem & goals

The admin API and Console authenticate with a **single shared bearer token**
(`[admin].token`). That token is an implicit super-user: anyone holding it can do
everything, and the [audit log](../console.md) cannot attribute an action to a
person. This blocks least-privilege operation, separation of duties, and
attributable change history — table stakes for a multi-operator, enterprise-facing
posture.

**Goals**

- **Named principals** (humans and automation) instead of one anonymous token.
- **Predefined roles** — `viewer`, `operator`, `admin` (+ optional `auditor`) —
  covering the common read/write splits out of the box.
- **Custom roles** — an operator can compose a role from an explicit permission
  set (read and/or write per resource).
- **Scoped, revocable, rotatable tokens**, hashed at rest.
- **Authorization at the API boundary** (deny-by-default), never only in the UI.
- **Per-principal audit attribution.**
- **Zero forced migration** — RBAC is opt-in and fully backward-compatible.

**Non-goals (deferred to Y3-02)**

- External IdPs (OIDC/SAML/SSO), group→role mapping, SCIM provisioning.
- Per-object scoping *beyond* resource verbs (e.g., "operator, but only for
  `listen = :8443`"). The model is designed to allow this later (see
  [Open questions](#open-questions)) but Phase 1–3 keep grants resource-wide.
- Multi-node / fleet RBAC (single-node admin surface only).

## Current state

| Concern | Today |
| --- | --- |
| AuthN | One constant-time-compared bearer token (`[admin].token`) or RBAC tokens (`[[admin.principals]]`, `[[admin.rbac.tokens]]`). Zero-token on loopback ⇒ open. |
| AuthZ | RBAC enforced at the API boundary (`admin:manage`, `config:apply`, etc.). Legacy shared token maps to `shared` principal with `default_role`. |
| Identity | Principal name (e.g., `alice` or `shared`) assigned per-request; audit `Actor` reflects the authenticated principal. |
| Token lifecycle | Legacy token: static config; RBAC tokens: issuance via API, immediate revocation, optional expiry. |

## Model overview

```
Request ──Bearer token──▶ Token store (hashed) ──▶ Principal ──▶ Role ──▶ Permission set
                                                                              │
                                              per-route required permission ──┴─▶ allow / 403
```

- A **principal** is a named identity with exactly one **role**.
- A **role** (predefined or custom) is a set of **permissions**.
- A **token** is a credential bound to a principal; a principal may hold several.
- Every admin route declares a **required permission**; the middleware allows the
  request only if the principal's role grants it. **Deny-by-default.**

## Principals

A principal is the unit of identity and attribution.

| Field | Type | Notes |
| --- | --- | --- |
| `name` | string, unique | Stable id; appears verbatim in the audit log (non-secret by design). |
| `role` | string | A predefined or custom role name. Exactly one. |
| `disabled` | bool | A disabled principal is denied regardless of a still-valid token. |

Two kinds of principal, same model:

- **Human operators** — `alice`, `bob`; typically `operator`/`admin`; tokens are
  personal and should carry an expiry + rotation cadence.
- **Automation / service accounts** — `ci-deployer`, `backup-bot`; a **narrow
  custom role**, a short-lived token, and (ideally) mTLS at the listener.

## Tokens (credentials)

Tokens are the only credential in HP-02 (external identity is Y3-02).

- **Shape.** A high-entropy random secret (≥ 256 bits) with a fixed prefix for
  recognizability and a short **public token id** (the first ~8 chars) used for
  lookup and audit **without** revealing the secret, e.g.
  `juladm_<tokenid>_<secret>`.
- **At rest.** Only a **hash** of the secret is stored (SHA-256 is sufficient and
  fast for high-entropy tokens; bcrypt's work factor is unnecessary here and would
  add per-request latency). The plaintext is shown **once** at creation and never
  again. Config-supplied tokens are given as [secret references](../secrets.md)
  and hashed in memory at load.
- **Lookup.** Index by token id, then `subtle.ConstantTimeCompare` the secret
  hash — O(1) and timing-safe.
- **Lifecycle.**
  - **Issuance** — `admin:manage` creates a token for a principal; optional
    `label`, `expires_at`.
  - **Revocation** — disable/delete a token; effect is **immediate** (checked per
    request against the live store), no reload required.
  - **Rotation** — issue a new token, then revoke the old; both are valid during
    an overlap window so automation rotates with zero downtime. `expires_at`
    enables auto-expiry.
  - **Metadata** — `created_at`, `last_used_at` (for stale-token hygiene).

| Field | Type | Notes |
| --- | --- | --- |
| `id` | string | Public, non-secret; used in audit + management APIs. |
| `principal` | string | Owning principal. |
| `hash` | string | SHA-256 of the secret. The secret is never stored. |
| `label` | string | Human note ("alice laptop", "ci"). |
| `expires_at` | RFC 3339 | Optional; past ⇒ denied. |
| `disabled` | bool | Immediate revocation. |
| `created_at` / `last_used_at` | RFC 3339 | Hygiene. |

## Permissions

A permission is a string `"<resource>:<action>"`. **Deny-by-default:** a
permission not granted is denied. Wildcards: `*` (everything), `<resource>:*`
(all actions on a resource).

The split the product requires — **read vs write (create/modify/update)** — is the
`read` vs `write`/`apply` action axis. `write` authors a change (no persistence);
`apply` persists it; the two are separable so an approval workflow can grant one
without the other.

| Permission | Grants |
| --- | --- |
| `status:read` | Runtime state: dashboards, runtime overview, stats, certificate inventory/expiry. |
| `metrics:read` | Prometheus `/metrics` scrape. |
| `config:read` | View configuration: raw TOML + structured projections (routes, apps/upstreams, TLS, security, streams, mTLS, plugins, wizard). |
| `config:write` | **Author/preview** changes without persisting: validate, diff, structured-patch preview, route dry-run test, wizard generation, descriptor upload. |
| `config:apply` | **Persist** configuration changes: apply, patch-apply, settings save. |
| `history:read` | List/view configuration history snapshots. |
| `history:rollback` | Roll back to a prior snapshot (persists). |
| `plugins:upload` | Upload a `.wasm` plugin module. |
| `observability:read` | Logs, request samples, failing routes, timeline, upstream/cert history, live event stream, Console self-health, search. |
| `audit:read` | Read the audit log. |
| `audit:export` | Export the audit log (CSV/JSONL). |
| `cache:purge` | Purge the response cache. |
| `reload:trigger` | Trigger a hot reload. |
| `admin:manage` | Manage RBAC (principals/roles/tokens), change admin-surface settings, and scrape runtime profiles (`/debug/pprof`). The highest-trust permission. |

Design notes:

- **One permission gates one endpoint (per method).** No endpoint requires two
  permissions; this keeps custom roles predictable.
- **`config:write` vs `config:apply`** deliberately separate authoring from
  persisting, enabling a "propose, don't apply" role for review/approval flows.
- **`admin:manage` is intentionally coarse** — RBAC administration, admin-listener
  settings, and profiling are all "keys to the kingdom" and are kept together so a
  role either is trusted with the admin surface or is not.

## Predefined roles

Always available; cannot be deleted. They cover the common cases so most
deployments never need a custom role.

| Role | Intent | Permissions |
| --- | --- | --- |
| **viewer** | Read-only observer | `status:read`, `metrics:read`, `config:read`, `history:read`, `observability:read`, `audit:read` |
| **operator** | Day-2 operations + config changes, **no** admin-surface control | *viewer* **+** `config:write`, `config:apply`, `history:rollback`, `cache:purge`, `reload:trigger`, `plugins:upload`, `audit:export` |
| **admin** | Full control incl. RBAC administration | `*` |
| **auditor** *(optional)* | Compliance read-only, narrower than viewer | `status:read`, `observability:read`, `audit:read`, `audit:export` |

The read/write ladder maps directly onto the product ask:

- **viewer = read.** No `write`, `apply`, `operate`, or `manage`.
- **operator = read + write.** Everything except `admin:manage` (cannot change
  tokens/roles or the admin listener).
- **admin = read + write + manage.**

## Custom roles

A custom role is a **name + an explicit permission list** drawn from the catalog
(wildcards allowed). Deny-by-default means the list is exactly the grant. Examples:

```toml
# A CI account that ships config but cannot purge cache or manage RBAC.
[[admin.roles]]
name        = "deployer"
permissions = ["status:read", "config:read", "config:write", "config:apply", "reload:trigger"]

# A change author who may propose but never persist (approval workflow).
[[admin.roles]]
name        = "proposer"
permissions = ["status:read", "config:read", "config:write", "history:read"]

# A monitoring integration: state + metrics only.
[[admin.roles]]
name        = "monitor"
permissions = ["status:read", "metrics:read", "observability:read"]

# A cache operator: nothing but purge (plus the state to see the result).
[[admin.roles]]
name        = "cache-ops"
permissions = ["status:read", "cache:purge"]
```

Rules:

- A custom role name must not collide with a predefined role.
- Permissions are validated against the catalog at load; an unknown permission is
  a config error (fail-closed).
- A principal referencing an undefined role is a config error.
- No role can grant more than the catalog defines; there is no "inherit" keyword
  in Phase 1–2 (compose explicitly). Wildcards (`config:*`, `*`) are the shorthand.

## Exhaustive permission matrix

Every admin route, its method(s), the required permission, and which predefined
roles satisfy it. `—` = unauthenticated by design. `✓`/`·` = granted / not
granted. Sourced from `internal/admin/routes.go`.

**Unauthenticated (liveness/readiness + static shell):**

| Route | Method | Required | Rationale |
| --- | --- | --- | --- |
| `/healthz` | GET | — | Process liveness probe. |
| `/readyz` | GET | — | Readiness probe. |
| `/`, `/config`, `/ui`, SPA assets | GET | — | Static shell/assets carry no secrets; the data APIs below are protected. The shell is unusable without a token. |

**Read (state & configuration view):**

| Route | Method | Required | viewer | operator | admin | auditor |
| --- | --- | --- | :--: | :--: | :--: | :--: |
| `/metrics` | GET | `metrics:read` | ✓ | ✓ | ✓ | · |
| `/api/stats` | GET | `status:read` | ✓ | ✓ | ✓ | ✓ |
| `/api/status` | GET | `status:read` | ✓ | ✓ | ✓ | ✓ |
| `/api/runtime/overview` | GET | `status:read` | ✓ | ✓ | ✓ | ✓ |
| `/api/certs` | GET | `status:read` | ✓ | ✓ | ✓ | ✓ |
| `/api/config` | GET | `config:read` | ✓ | ✓ | ✓ | · |
| `/api/config/raw` | GET | `config:read` | ✓ | ✓ | ✓ | · |
| `/api/config/settings` | GET | `config:read` | ✓ | ✓ | ✓ | · |
| `/api/upstreams` | GET | `config:read` | ✓ | ✓ | ✓ | · |
| `/api/routes` | GET | `config:read` | ✓ | ✓ | ✓ | · |
| `/api/apps` | GET | `config:read` | ✓ | ✓ | ✓ | · |
| `/api/tls` | GET | `config:read` | ✓ | ✓ | ✓ | · |
| `/api/mtls` | GET | `config:read` | ✓ | ✓ | ✓ | · |
| `/api/security` | GET | `config:read` | ✓ | ✓ | ✓ | · |
| `/api/traffic-controls` | GET | `config:read` | ✓ | ✓ | ✓ | · |
| `/api/streams` | GET | `config:read` | ✓ | ✓ | ✓ | · |
| `/api/plugins` | GET | `config:read` | ✓ | ✓ | ✓ | · |
| `/api/wizard` | GET | `config:read` | ✓ | ✓ | ✓ | · |
| `/api/history` | GET | `history:read` | ✓ | ✓ | ✓ | · |
| `/api/history/get` | GET | `history:read` | ✓ | ✓ | ✓ | · |
| `/api/config/history` | GET | `history:read` | ✓ | ✓ | ✓ | · |
| `/api/config/history/{id}` | GET | `history:read` | ✓ | ✓ | ✓ | · |
| `/api/config/applies/{id}` | GET | `status:read` **or** `config:apply` | ✓ | ✓ | ✓ | ✓ |
| `/api/observability/requests` | GET | `observability:read` | ✓ | ✓ | ✓ | ✓ |
| `/api/observability/failing-routes` | GET | `observability:read` | ✓ | ✓ | ✓ | ✓ |
| `/api/observability/timeline` | GET | `observability:read` | ✓ | ✓ | ✓ | ✓ |
| `/api/observability/upstream-history` | GET | `observability:read` | ✓ | ✓ | ✓ | ✓ |
| `/api/observability/cert-history` | GET | `observability:read` | ✓ | ✓ | ✓ | ✓ |
| `/api/observability/logs` | GET | `observability:read` | ✓ | ✓ | ✓ | ✓ |
| `/api/observability/logs/stream` | GET (SSE) | `observability:read` | ✓ | ✓ | ✓ | ✓ |
| `/api/events` | GET (SSE) | `observability:read` | ✓ | ✓ | ✓ | ✓ |
| `/api/search` | GET | `observability:read` | ✓ | ✓ | ✓ | ✓ |
| `/api/admin/health` | GET | `observability:read` | ✓ | ✓ | ✓ | ✓ |
| `/api/admin/client-errors` | POST | `observability:read` | ✓ | ✓ | ✓ | ✓ |
| `/api/audit` | GET | `audit:read` | ✓ | ✓ | ✓ | ✓ |
| `/api/audit/export` | GET | `audit:export` | · | ✓ | ✓ | ✓ |

**Write — author/preview (no persistence):**

| Route | Method | Required | viewer | operator | admin | auditor |
| --- | --- | --- | :--: | :--: | :--: | :--: |
| `/api/config/validate` | POST | `config:write` | · | ✓ | ✓ | · |
| `/api/config/diff` | POST | `config:write` | · | ✓ | ✓ | · |
| `/api/config/patch` | POST | `config:write` | · | ✓ | ✓ | · |
| `/api/routes/test` | POST | `config:write` | · | ✓ | ✓ | · |
| `/api/wizard/generate` | POST | `config:write` | · | ✓ | ✓ | · |
| `/api/transcode/descriptor-upload` | POST | `config:write` | · | ✓ | ✓ | · |

**Write — persist / operate (mutating):**

| Route | Method | Required | viewer | operator | admin | auditor |
| --- | --- | --- | :--: | :--: | :--: | :--: |
| `/api/config/settings` | POST | `config:apply` | · | ✓ | ✓ | · |
| `/api/config/apply` | POST | `config:apply` | · | ✓ | ✓ | · |
| `/api/config/patch/apply` | POST | `config:apply` | · | ✓ | ✓ | · |
| `/api/history/rollback` | POST | `history:rollback` | · | ✓ | ✓ | · |
| `/api/config/rollback` | POST | `history:rollback` | · | ✓ | ✓ | · |
| `/reload` | POST | `reload:trigger` | · | ✓ | ✓ | · |
| `/cache/purge` | POST | `cache:purge` | · | ✓ | ✓ | · |
| `/api/plugins/upload` | POST | `plugins:upload` | · | ✓ | ✓ | · |

**Admin surface (highest trust):**

| Route | Method | Required | viewer | operator | admin | auditor |
| --- | --- | --- | :--: | :--: | :--: | :--: |
| `/debug/pprof/…` | GET | `admin:manage` | · | · | ✓ | · |
| `/api/admin/rbac/principals` *(new, Phase 3)* | GET/POST/DELETE | `admin:manage` | · | · | ✓ | · |
| `/api/admin/rbac/roles` *(new, Phase 3)* | GET/POST/DELETE | `admin:manage` | · | · | ✓ | · |
| `/api/admin/rbac/tokens` *(new, Phase 3)* | GET/POST/DELETE | `admin:manage` | · | · | ✓ | · |

Method-sensitive endpoints (`/api/config/settings`) require `config:read` on GET
and `config:apply` on POST. The matrix lists the write requirement on its own row.

The exact-ID managed-apply result endpoint (`/api/config/applies/{id}`) is
authorized by **any-of** `status:read` **or** `config:apply`: a principal
privileged enough to *apply* configuration may read the secret-free result of
its own managed-apply transaction without also holding `status:read`. The
projection never exposes actor, source IP, or token digest — those remain
audit-API only.

## Enforcement

- **Where.** A single authorization middleware wraps the mux (replacing/extending
  `auth()`), plus a per-route required-permission lookup. The server is the sole
  source of truth; the Console UI only *hides* what a role cannot do.
- **Flow per request.**
  1. Extract the bearer token; resolve `token id → token → principal`.
  2. Reject expired/disabled tokens and disabled principals.
  3. Compute the principal's effective permission set from its role.
  4. Look up the route's required permission; allow iff the set grants it
     (exact match or a covering wildcard).
- **Status codes.** No/invalid token ⇒ **401** (`WWW-Authenticate: Bearer`).
  Valid token but missing permission ⇒ **403** with a machine-readable reason,
  e.g. `{"error":"forbidden","required":"config:apply","role":"viewer"}`.
- **Object-level guard.** The existing self-lockout guard
  (`adminLockoutChanges`) continues to prevent disabling admin reachability; RBAC
  adds that only `admin:manage` may change RBAC/admin settings, and the store
  refuses to remove the **last** enabled `admin` principal (no lock-out).
- **Deny-by-default everywhere.** A route with no declared permission is treated
  as `admin:manage` (fail-closed), so a newly added endpoint is never
  accidentally world-open. A CI test asserts every mounted route has an explicit
  permission entry.

## Audit attribution

- `AuditEvent.Actor` becomes the **principal name** (e.g., `alice`) instead of the
  constant `"operator"`. A new `token_id` field records **which** credential was
  used (non-secret). Secrets are never logged (existing `redactActor` /
  scrubbing stay).
- Attribution is **server-assigned** from the authenticated principal — never
  client-supplied — so it cannot be spoofed.
- Authentication failures are audited as `actor="anonymous"` with the source IP
  and token id prefix (if any), enabling brute-force detection.
- The CSV/JSONL export schema gains a `token_id` column
  (`id,time,actor,token_id,operation,resource,result,detail,source_ip`); readers
  must tolerate the added column (append-only schema evolution).

## Configuration schema

RBAC is configured under `[admin.rbac]`, with roles and principals as arrays.
Tokens are supplied as [secret references](../secrets.md) in the config path, or
minted via the API (Phase 3).

```toml
[admin]
listen = "127.0.0.1:9090"
# The legacy shared token remains valid while RBAC is off, and (when RBAC is on)
# maps to a compatibility principal — see Migration below.
# token = "${secret:console/legacy}"

[admin.rbac]
enabled      = true
# Role assigned to the legacy [admin].token when RBAC is enabled (compat).
default_role = "admin"

# Optional custom roles (predefined viewer/operator/admin/auditor always exist).
[[admin.roles]]
name        = "deployer"
permissions = ["status:read", "config:read", "config:write", "config:apply", "reload:trigger"]

# Principals + their token(s).
[[admin.principals]]
name  = "alice"
role  = "operator"
token = "${secret:console/alice}"

[[admin.principals]]
name       = "ci-deployer"
role       = "deployer"
token      = "${env:JUL_CI_TOKEN}"
expires_at = "2026-12-31T00:00:00Z"
```

Schema types (Phase 1):

| Field | Type | Default | Notes |
| --- | --- | --- | --- |
| `admin.rbac.enabled` | bool | `false` | Off ⇒ current single-token behavior. |
| `admin.rbac.default_role` | string | `"admin"` | Role for the legacy shared token during migration. |
| `admin.roles[].name` | string | — | Unique; not a predefined name. |
| `admin.roles[].permissions` | []string | — | Validated against the catalog; wildcards allowed. |
| `admin.principals[].name` | string | — | Unique; appears in audit. |
| `admin.principals[].role` | string | — | Predefined or custom. |
| `admin.principals[].token` | string (secret ref) | — | Hashed in memory at load. Multiple via `[[admin.principals.tokens]]` in Phase 2. |
| `admin.principals[].expires_at` | RFC 3339 | — | Optional per-token expiry (Phase 2 for multi-token). |
| `admin.principals[].disabled` | bool | `false` | Deny without deleting. |

Validation (`internal/config/validate.go`): unknown permission ⇒ error; principal
referencing an undefined role ⇒ error; duplicate principal/role names ⇒ error;
`enabled = true` with zero `admin`-capable principals **and** no legacy token ⇒
error (would lock everyone out).

## Backward compatibility & migration

RBAC is **opt-in** and changes nothing until `admin.rbac.enabled = true`.

| State | Behavior |
| --- | --- |
| RBAC off (default) | Exactly today: `[admin].token` is the all-or-nothing super-user; no token + loopback ⇒ open. Audit actor `"operator"`. |
| RBAC on, legacy token still set | The legacy token authenticates as a synthetic principal `shared` with `default_role` (default `admin`), so existing automation keeps working. A startup **warning** recommends migrating to named principals. Audit actor `"shared"`. |
| RBAC on, no legacy token | Only named principals authenticate. Anonymous ⇒ 401 on every API (deny-by-default). |

**Migration path (documented for operators):**

1. Turn on `admin.rbac.enabled = true`, keep the legacy `[admin].token` → nothing
   breaks (everyone is `admin` via `shared`).
2. Add named principals with least-privilege roles; hand out their tokens.
3. Move automation to scoped tokens (`deployer`/custom), add `expires_at`.
4. Remove the legacy `[admin].token`. Now every action is attributable and scoped.

The legacy shared token is **deprecated but supported** through the HP-02 line; a
future major may require named principals when RBAC is enabled.

## Threat model & security considerations

| Threat | Mitigation |
| --- | --- |
| Token theft | Least-privilege blast radius (a `viewer` token cannot apply); immediate revocation; short `expires_at` for automation; hashed at rest; never logged. |
| Privilege escalation | Only `admin:manage` may create/modify roles/principals; the store refuses to remove the last enabled `admin` and refuses to grant a permission the catalog doesn't define (fail-closed). |
| Confused deputy / UI bypass | Enforcement is server-side; UI hiding is cosmetic. Every request is authorized independently. |
| Audit spoofing | Attribution is server-assigned from the authenticated principal, not client input. |
| Credential in logs/URLs | Bearer only in the `Authorization` header (no `?token=`); token secrets never audited; only token **ids** appear. |
| Brute force | Per-principal (per-token) rate limiting extends the existing admin limiter; failed auth is audited with source IP. |
| Timing attacks | Lookup by token id, then constant-time hash compare. |
| Exposure beyond loopback | RBAC composes with mTLS on the admin listener (see [mtls.md](../mtls.md)); the two together are the recommended posture for a non-loopback admin surface. |
| New endpoint added world-open | Deny-by-default: an unannotated route requires `admin:manage`, and a CI test fails if any mounted route lacks an explicit permission. |

Separation of duties is expressible: e.g., a `proposer` role (`config:write`, no
`config:apply`) plus an `approver`/`operator` who applies — the two-key pattern
for high-risk change control.

## Phased implementation plan

Each phase is independently shippable, backward-compatible, and gated by tests.

### Phase 0 — Baseline (done)
Single shared token; audit actor `"operator"`. No change.

### Phase 1 — Authorization boundary + predefined roles (config-defined principals)
**Deliverables**
- `internal/rbac` (or `internal/admin/rbac.go`): permission catalog, predefined
  roles, `Policy` resolving `token → principal → permission set`.
- Config: `[admin.rbac]`, `[[admin.roles]]`, `[[admin.principals]]` +
  validation. Tokens hashed in memory at load from secret refs.
- Middleware: replace `auth()` with authn+authz; per-route required-permission
  table; 401 vs 403; deny-by-default.
- Audit: `Actor` = principal name; add `token_id`.
- Backward-compat: legacy token → `shared`/`default_role`; RBAC off = current.

**Tests**
- Full **role × endpoint** matrix (each predefined role vs every route → allow/deny).
- 401 (no/invalid token) vs 403 (authenticated, unauthorized).
- Legacy-token compatibility; RBAC-off parity with today.
- "Every mounted route has an explicit permission" guard test.
- Config validation: unknown permission/role, lock-out prevention.

**DoD** — documented matrix (this doc) enforced by tests; deny-by-default proven;
no behavior change when RBAC is off.

### Phase 2 — Custom roles, multi-token, rotation/expiry, per-principal limits
**Deliverables**
- `[[admin.roles]]` custom roles fully wired (wildcards); `[[admin.principals.tokens]]`
  multi-token with `label`/`expires_at`/`disabled`; rotation overlap; `last_used_at`.
- Per-principal rate-limit buckets (extend `internal/admin` limiter).

**Tests** — custom-role resolution incl. wildcards; expiry enforced; rotation
overlap; per-principal limiting; revocation immediate.

**DoD** — a custom role restricts exactly to its permission list; expiry/rotation
verified.

### Phase 3 — API-managed RBAC + Console UI
**Deliverables**
- `admin:manage`-gated endpoints: `/api/admin/rbac/{principals,roles,tokens}`
  (list/create/delete). Token creation returns the plaintext **once**; persisted
  hashed to a store alongside config history.
- Console: current-identity + role display; role-gated controls (hide disallowed
  actions); a token-management panel.

**Tests** — runtime issue/revoke takes effect immediately; UI gating matches
server truth; management endpoints reject non-`admin:manage`.

**DoD** — an admin can manage principals/roles/tokens without editing files;
revocation is immediate.

### Phase 4 — External identity (bridge to Y3-02, out of HP-02 scope)
OIDC/SAML/SSO → principal/role mapping; group→role binding. Tracked under
[Y3-02](../roadmap/); RBAC's role model is the local-token precursor it maps onto.

## Open questions

- **Per-object scoping.** Should a permission optionally carry a selector (e.g.,
  `config:apply@listen=:8443`)? The `resource:action` grammar leaves room for an
  `@scope` suffix later without breaking Phase 1–3 grants.
- **Role inheritance.** Phase 1–2 compose explicitly (+ wildcards). A `extends`
  keyword could be added if custom-role duplication becomes painful.
- **Token storage backend for Phase 3.** Reuse the config-history atomic-file
  store, or a dedicated `admin.rbac.store_dir`? Leaning toward the former for a
  single dependency-free store.
- **Two-person apply.** Should high-risk applies optionally require a second
  `approver` principal? Expressible with `config:write` vs `config:apply`; the
  enforcement (pending-change queue) would be a follow-on.
