# Engineering specs

> Version 1.5 · Updated 2026-08-03

Detailed engineering plans behind the [roadmap](../roadmap/). Year 1 and Year 2
specs describe delivered historical capability. **Year 3–5 specs remain concept
horizons, not committed delivery schedules.** Cross-cutting core work is defined
by [Core Gateway Completeness](core-gateway-completeness.md) rather than being
forced into an old year sequence.

For product direction and programme entry rules, see:

- [../vision/](../vision/) — product north star, architectural commitments, and target users;
- [../roadmap/](../roadmap/) — current correctness, core, operations, experiment, and horizon portfolio;
- [../operating-model.md](../operating-model.md) — issue/PR governance, evidence, WIP, and Definition of Done;
- [../adr/0013-project-operating-model-and-completeness.md](../adr/0013-project-operating-model-and-completeness.md) — durable portfolio and experiment decision;
- [../adr/0014-operability-surfaces.md](../adr/0014-operability-surfaces.md) — Console/API/CLI surface decision;
- [../adr/](../adr/) — all architecture decision records.

## Index

| Scope | Theme | Spec |
|------|-------|------|
| **Current core** | Bounded standalone gateway completeness: trust, resilience, routing policy, lifecycle, automation, diagnostics, migration, and selected runtime dynamics | [core-gateway-completeness.md](core-gateway-completeness.md) |
| Year 1 | Credibility & effortlessness (Auto-HTTPS, Console v1, gRPC↔REST MVP, table-stakes proxy) | [year-1.md](year-1.md) |
| Year 2 | Protocol gateway + extensibility moat (gRPC streaming, WASM plugins, L4 stream, discovery, WAF, mTLS) | [year-2.md](year-2.md) |
| Year 3 | Scale, fleet & ecosystem — **concept horizon** (fleet, multi-node config, external identity, plugin registry) | [year-3.md](year-3.md) |
| Year 4 | AI-native + edge platform — **concept horizon / bounded experiments only** | [year-4.md](year-4.md) |
| Year 5 | Global scale, mesh & cloud — **concept horizon** | [year-5.md](year-5.md) |
| — | Hardening & platform (reload transaction, Console RBAC, metric cardinality, container and configuration hardening) | [hardening-platform.md](hardening-platform.md) |
| — | Console RBAC deep-dive | [console-rbac.md](console-rbac.md) |

## Authority and sequencing

- `core-gateway-completeness.md` defines what is required for the standalone
  product; its absence is not inferred from competitor feature lists.
- Year 3–5 documents preserve architectural hypotheses and possible future
  categories. They do not override the active portfolio or #62.
- A horizon becomes implementation only through an ADR/entry decision and
  implementation-ready issues.
- A bounded experiment may start under ADR 0013, but does not become supported
  product without an explicit promotion decision.
- Generic transport, trust, resilience, lifecycle, and automation primitives
  belong in the core specification even when a future experiment will consume
  them.

## Keeping these in sync

When a durable design changes, update the governing ADR and spec in the same
change. When implementation lands, update the feature guide, status/maturity
evidence, compatibility contract, and changelog. Historical year documents
should be amended only where their dependency assumptions or horizon boundaries
would otherwise mislead current work.

## Changelog

| Date | Ver | What changed | What stayed | Source |
| --- | --- | --- | --- | --- |
| 2026-08-03 | 1.5 | Added the Core Gateway Completeness specification as the current cross-cutting product boundary; linked ADR 0013/0014 and the operating model; clarified that AI and other Year 3–5 categories are horizons or bounded experiments rather than automatic next phases. | Year 1–2 historical specifications and the permanent OSS/open-core boundary. | Combined audit, #108, #114 |
| 2026-07-20 | 1.4 | Clarified that Year 3–5 specs are concept horizons, not committed schedules; marked them as such in the index; linked the permanent OSS/open-core boundary ([ADR 0012](../adr/0012-oss-open-core-boundary.md)) in the intro. | Year 1–2 spec rows, hardening-platform row, sync guidance, and prior changelog rows. | Issue #65 |
| 2026-07-20 | 1.3 | Linked the active operating roadmap and evidence gates; noted that the vision includes target users and the product promise. | The per-year index rows, the hardening-platform row, and sync guidance. | Issue #63 |
| 2026-07-08 | 1.2 | Added the [Console RBAC deep-dive spec](console-rbac.md) (HP-02). | The per-year rows, the hardening-platform row, and sync guidance. | [console-rbac.md](console-rbac.md), [#35](https://github.com/victornife/jul/issues/35) |
| 2026-06-28 | 1.1 | Indexed [Hardening & platform](hardening-platform.md). | The per-year index rows and sync guidance. | [hardening-platform.md](hardening-platform.md), [roadmap](../roadmap/) |
| 2026-06-21 | 1.0 | Added a version stamp and corrected the Year-2 maturity wording. | The index, per-spec descriptions, and sync guidance. | [review 2026-06-21](../reviews/); [ADR 0003](../adr/0003-maturity-and-ga.md) |
