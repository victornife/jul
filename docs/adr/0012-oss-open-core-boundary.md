# ADR 0012 — OSS / open-core boundary

- **Status:** Accepted
- **Date:** 2026-07-20
- **Deciders:** Jul.IA maintainers
- **Applies to:** product packaging, licensing, and commercial strategy
- **Source:** Roadmap Phase 1 issue #64 (P1-02)

## Context

Jul.IA is released under the AGPL-3.0 and is designed to grow along an
**OSS → open-core → Cloud** path without re-architecting. The near-term
commitment is the OSS core; everything beyond it is a demand-gated horizon
([roadmap](../roadmap/README.md), [vision](../vision/README.md)).

Without an explicit boundary, later commercial work could accidentally place
basic single-node operation, security fixes, or data portability behind a
paywall, eroding the trust that makes the OSS project useful. This ADR records
the permanent OSS floor and the legitimate commercial ceiling.

## Decision

### What stays OSS forever

The following are invariants of the project. They may be extended, but never
restricted to a commercial edition:

1. **Standalone single-node data plane.** The HTTP/TCP/UDP edge server,
reverse proxy, static file serving, load balancing, TLS termination, rate
limiting, caching, compression, auth, health checks, and the documented
standalone admin API remain open-source and fully functional on a single node
without any external service.

2. **Core configuration format.** The TOML schema documented in
[configuration.md](../configuration.md), the config lifecycle semantics
([reload-semantics.md](../reload-semantics.md)), and the structured admin patch
API used by the Console remain open and stable under the
[compatibility policy](../compatibility.md).

3. **Security fixes.** Security patches are never commercial-only. A fix for a
vulnerability in the OSS code ships in the OSS repository and releases at the
same time as, or before, any commercial build that contains it.

4. **No Cloud dependency for standalone operation.** A lean or full build of
Jul.IA must start, serve traffic, reload config, and expose the admin surface
without connecting to a hosted control plane, telemetry endpoint, or license
server. Optional Cloud integration, if built, is exactly that — optional.

5. **Data portability.** Users can export their configuration, config-history
snapshots, and audit trail in documented, machine-readable formats. No feature
locks these artifacts to a vendor-controlled store.

### Where commercial value may appear

Commercial offerings may add value around the open core without removing
capabilities from it:

- **Fleet coordination** — multi-node config distribution, staged rollout,
health-gated promotion, and fleet rollback.
- **External identity** — SSO/SAML/OIDC, directory sync, and advanced RBAC for
the admin Console.
- **Compliance automation** — tamper-evident audit chains, SIEM forwarding,
retention policies, and signed plugin marketplace.
- **Hosted operations** — a managed control plane that operators choose to
enroll in (bring-your-own-node model).
- **Support, training, and certification** — professional services layered on
top of the open-source binary.

These layers are allowed to be commercial because they are additive: a user who
never pays still has a complete, secure, standalone edge server.

### Packaging changes

If the project ever changes its license, edition names, or what ships in the
free vs paid artifact, that change requires:

1. A new ADR superseding this one.
2. A documented migration path for existing users and their data.
3. At least one MINOR release of advance notice before the change takes effect.

## Consequences

**Positive**

- Users and contributors can rely on a stable, free floor.
- Commercial work is clearly scoped, reducing scope creep and trust erosion.
- The boundary is auditable: any proposal that violates the invariants can be
rejected by pointing to this ADR.

**Negative / trade-offs**

- Some convenience features (e.g., fleet dashboard, hosted backup) may only be
available commercially or as demand-gated horizon work.
- The project must maintain discipline to keep security fixes and core
functionality out of commercial-only releases.

## Related

- [docs/vision/README.md](../vision/README.md) — product promise and three pillars
- [docs/roadmap/README.md](../roadmap/README.md) — demand-gated horizon
- [docs/compatibility.md](../compatibility.md) — stable config/API contract
- [LICENSE](../../LICENSE) — AGPL-3.0
