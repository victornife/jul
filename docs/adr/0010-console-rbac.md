# ADR 0010 — Console RBAC: local principals, roles, and scoped tokens

- **Status:** Accepted — **delivered** (local-token RBAC) in Phase 3 (#73); external identity remains Y3-02 horizon
- **Date:** 2026-07-08
- **Deciders:** Jul.IA maintainers
- **Applies to:** the admin API and Console v2 authorization model
- **Source:** SEQ-09 (#35), extending [docs/specs/hardening-platform.md](../specs/hardening-platform.md) HP-02; full design in [docs/specs/console-rbac.md](../specs/console-rbac.md)

## Context

The admin API and Console authenticate with a **single shared bearer token**
(`[admin].token`), compared in constant time in `internal/admin/server.go`. That
token is an implicit super-user, and the [audit log](../console.md) hard-codes the
actor to `"operator"` — it cannot attribute an action to a person. This blocks
least-privilege operation, separation of duties, and attributable change history,
which an enterprise-facing posture requires.

The roadmap already commits to **HP-02 (Console RBAC + multi-user)** as the
local-token precursor to **Y3-02 (SSO/SAML/OIDC)**. The open question was the
*shape* of that model: how coarse/fine the permissions are, whether custom roles
exist, how tokens are issued/revoked, and how to stay backward-compatible.

## Decision

Adopt a **local, config-and-API-managed RBAC model** enforced at the admin API
boundary. External identity providers stay out of scope (Y3-02).

1. **Principals → one role → a permission set.** A principal is a named identity
   (human or automation). A role is a set of `"<resource>:<action>"` permissions.
   Enforcement is **deny-by-default** at the server; the UI only hides.

2. **Predefined roles cover the common cases:** `viewer` (read), `operator`
   (read + write/apply/operate, but not admin-surface), `admin` (`*`), and an
   optional `auditor`. The read/write ladder maps directly onto the product ask.

3. **Custom roles are first-class:** a name plus an explicit permission list
   (wildcards allowed), validated against a fixed catalog. This lets operators
   compose least-privilege roles (e.g., a CI `deployer`, a read-only `monitor`, a
   `proposer` that authors but cannot apply).

4. **Read vs write is the action axis,** and `write` (author/preview) is separable
   from `apply` (persist) so approval/two-key workflows are expressible.

5. **Scoped, revocable, rotatable tokens, hashed at rest** (SHA-256 of a
   high-entropy secret; constant-time compare; plaintext shown once). Config
   supplies tokens via [secret references](../secrets.md); Phase 3 mints them via
   an `admin:manage`-gated API. Revocation is immediate.

6. **Per-principal audit attribution.** `AuditEvent.Actor` becomes the principal
   name; a `token_id` records the credential. Attribution is server-assigned.

7. **Backward-compatible and phased.** RBAC is opt-in (`admin.rbac.enabled`, off by
   default). When on, the legacy shared token maps to a compatibility `admin`
   principal so nothing breaks during migration. See the
   [phased plan](../specs/console-rbac.md#phased-implementation-plan).

The **exhaustive permission matrix** (every admin endpoint → required permission →
role) lives in the spec and is enforced by a role×endpoint test matrix, including
a guard that fails if any mounted route lacks an explicit permission.

## Alternatives considered

- **Keep the single token, add read-only tokens only.** Rejected: too coarse; no
  attribution, no custom scoping, no separation of duties.
- **Jump straight to OIDC/SSO.** Rejected for HP-02: heavy dependency and IdP
  coupling; the roadmap sequences external identity as Y3-02. Local RBAC is the
  precursor its role model maps onto.
- **Per-endpoint ACLs without roles.** Rejected: roles are the operator-facing
  unit; endpoint ACLs are the *implementation* (the required-permission table),
  not the config surface.
- **bcrypt for token hashing.** Rejected: tokens are high-entropy random secrets,
  so a slow KDF adds per-request latency for no brute-force benefit; SHA-256 +
  constant-time compare is correct here.

## Consequences

- **Positive:** least-privilege multi-operator use; attributable audit; scoped
  automation tokens with rotation/expiry; deny-by-default closes the "new endpoint
  is accidentally open" gap; a clean bridge to Y3-02.
- **Negative / cost:** new config surface (`[admin.rbac]`) and a token store; the
  audit schema gains a `token_id` column (append-only evolution); the Console gains
  role-gated UI (Phase 3). Mitigated by shipping opt-in and phased.
- **Invariant:** the server is the source of truth; the UI never grants. A route
  with no declared permission fails closed to `admin:manage`.

## Related

- [docs/specs/console-rbac.md](../specs/console-rbac.md) — full design + exhaustive matrix + phased plan.
- [docs/specs/hardening-platform.md](../specs/hardening-platform.md) — HP-02 backlog entry.
- [docs/roadmap/README.md](../roadmap/README.md) — HP-02 and Y3-02.
- [ADR 0004](0004-console-ui-invariants.md) — Console UI invariants (server is source of truth).
- [SECURITY.md](../../SECURITY.md) — admin token hardening + self-lockout guard.
