# Engineering specs

> Version 1.2 · Updated 2026-07-08

Detailed, per-feature engineering execution plans behind the [5-year roadmap](../roadmap/).
These are the design source-of-truth: for each feature (`Y#-##`) they capture objective/scope,
design, config (Go + TOML), new files/interfaces, implementation tasks, dependencies, test plan,
acceptance/DoD, risks, rollout/build-tags and docs updates.

For the high-level "why" and the delivery status of each feature, see:

- [../vision/](../vision/) — product north star, architectural commitments, and target users
- [../roadmap/](../roadmap/) — consolidated 5-year plan, the [active operating roadmap](../roadmap/README.md#active-operating-roadmap), and [evidence gates](../roadmap/README.md#evidence-gates-and-product-signals)
- [../adr/](../adr/) — architecture decision records

## Index

| Year | Theme | Spec |
|------|-------|------|
| 1 | Credibility & effortlessness (Auto-HTTPS, Console v1, gRPC↔REST MVP, table-stakes proxy) | [year-1.md](year-1.md) |
| 2 | Protocol gateway + extensibility moat (gRPC streaming, WASM plugins, L4 stream, discovery, WAF, mTLS) | [year-2.md](year-2.md) |
| 3 | Scale, fleet & ecosystem (clustering, multi-node config, RBAC/SSO, plugin registry) | [year-3.md](year-3.md) |
| 4 | AI-native + edge platform (semantic cache, tokenizer-aware routing, image optimization) | [year-4.md](year-4.md) |
| 5 | Global scale, mesh & cloud (geo-routing, service mesh, advanced edge security) | [year-5.md](year-5.md) |
| — | Hardening & platform (pre-1.0 robustness: reload transaction, Console RBAC, metric cardinality, pre-commit gate, container hardening, config-parity patch-ops, SSRF allow-list) | [hardening-platform.md](hardening-platform.md) |
| — | Console RBAC (HP-02 deep-dive: principals, predefined + custom roles, scoped tokens, exhaustive permission matrix, phased plan) | [console-rbac.md](console-rbac.md) |

## Keeping these in sync

When a feature's design changes, update its spec section here in the same change.
When a feature ships, move its roadmap row to **Delivered** and (if a durable decision was made)
add an ADR. See the maintenance note in [../roadmap/](../roadmap/).

## Changelog

| Date | Ver | What changed | What stayed | Source |
| --- | --- | --- | --- | --- |
| 2026-07-20 | 1.3 | Linked the [active operating roadmap](../roadmap/README.md#active-operating-roadmap) and [evidence gates](../roadmap/README.md#evidence-gates-and-product-signals) from the navigation guidance; noted that [../vision/](../vision/) now includes target users and the product promise. | The per-year index rows, the hardening-platform row, and sync guidance. | Issue #63 |
| 2026-07-08 | 1.2 | Added the [Console RBAC deep-dive spec](console-rbac.md) (HP-02): principal/role/permission model, predefined + custom roles, scoped tokens, an exhaustive permission matrix over every admin endpoint, `[admin.rbac]` schema, migration, threat model, and a four-phase plan. Recorded in [ADR 0010](../adr/0010-console-rbac.md). | The per-year rows, the hardening-platform row, and sync guidance. | [console-rbac.md](console-rbac.md), [#35](https://github.com/victornife/jul/issues/35) |
| 2026-06-28 | 1.1 | Indexed the new cross-cutting [Hardening & platform spec](hardening-platform.md) (HP-01..HP-07 + micro-fixes register) — the design source-of-truth behind the roadmap's pre-1.0 robustness backlog. | The per-year index rows and sync guidance. | [hardening-platform.md](hardening-platform.md), [roadmap](../roadmap/) |
| 2026-06-21 | 1.0 | Added a version stamp; corrected the Year-2 index blurb from "gRPC GA" to "gRPC streaming" (shipped at Beta, not GA). | The index, per-spec descriptions, and sync guidance. | [review 2026-06-21](../reviews/); [ADR 0003](../adr/0003-maturity-and-ga.md) |
