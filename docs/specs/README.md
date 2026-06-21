# Engineering specs

> Version 1.0 · Updated 2026-06-21

Detailed, per-feature engineering execution plans behind the [5-year roadmap](../roadmap/).
These are the design source-of-truth: for each feature (`Y#-##`) they capture objective/scope,
design, config (Go + TOML), new files/interfaces, implementation tasks, dependencies, test plan,
acceptance/DoD, risks, rollout/build-tags and docs updates.

For the high-level "why" and the delivery status of each feature, see:

- [../vision/](../vision/) — product north star and architectural commitments
- [../roadmap/](../roadmap/) — consolidated 5-year plan with status (delivered / in progress / planned)
- [../adr/](../adr/) — architecture decision records

## Index

| Year | Theme | Spec |
|------|-------|------|
| 1 | Credibility & effortlessness (Auto-HTTPS, Console v1, gRPC↔REST MVP, table-stakes proxy) | [year-1.md](year-1.md) |
| 2 | Protocol gateway + extensibility moat (gRPC streaming, WASM plugins, L4 stream, discovery, WAF, mTLS) | [year-2.md](year-2.md) |
| 3 | Scale, fleet & ecosystem (clustering, multi-node config, RBAC/SSO, plugin registry) | [year-3.md](year-3.md) |
| 4 | AI-native + edge platform (semantic cache, tokenizer-aware routing, image optimization) | [year-4.md](year-4.md) |
| 5 | Global scale, mesh & cloud (geo-routing, service mesh, advanced edge security) | [year-5.md](year-5.md) |

## Keeping these in sync

When a feature's design changes, update its spec section here in the same change.
When a feature ships, move its roadmap row to **Delivered** and (if a durable decision was made)
add an ADR. See the maintenance note in [../roadmap/](../roadmap/).

## Changelog

| Date | Ver | What changed | What stayed | Source |
| --- | --- | --- | --- | --- |
| 2026-06-21 | 1.0 | Added a version stamp; corrected the Year-2 index blurb from "gRPC GA" to "gRPC streaming" (shipped at Beta, not GA). | The index, per-spec descriptions, and sync guidance. | [review 2026-06-21](../reviews/); [ADR 0003](../adr/0003-maturity-and-ga.md) |
