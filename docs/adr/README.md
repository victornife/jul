# Architecture Decision Records

Durable architectural and product decisions for Jul.IA. Each record states the context that forced a
decision, the decision itself with its rejected alternatives, and the consequences accepted alongside
it. ADRs are amended rather than rewritten; superseded reasoning stays visible.

## Conventions

- Filename: `NNNN-kebab-case-title.md`, four digits, numbers unique and contiguous.
- The `# ADR NNNN — Title` heading must match the filename number.
- Every ADR appears in the index below. `scripts/docs-check.py` enforces all four rules.
- A number is a stable identifier, not a timestamp: chronology lives in the `Date:` header. A record is
  never renumbered to restore date ordering.
- Standard sections: metadata block (`Status`, `Date`, `Deciders`, `Applies to`, `Source`), then
  `## Context`, `## Decision`, `## Consequences`, `## Related`.

## Index

| ADR | Title | Status | Date |
| --- | --- | --- | --- |
| [0001](0001-language-strategy.md) | Implementation language strategy (Go-first, native code at the edges) | Accepted | 2026-06-21 |
| [0002](0002-protocol-adaptation.md) | Protocol adaptation strategy (explicit adapters, not universal conversion) | Accepted | 2026-06-21 |
| [0003](0003-maturity-and-ga.md) | Maturity model, GA bar, and evidence gates | Accepted — amended by 0005 and 0013 | 2026-06-21 |
| [0004](0004-console-ui-invariants.md) | Console-first / UI invariants (operable by design) | Accepted — amended by 0014 | 2026-06-21 |
| [0005](0005-soak-post-ga-gate.md) | Soak test reclassified to a post-GA stability gate | Accepted | 2026-06-21 |
| [0006](0006-console-v2-stack.md) | Console v2: build-time SPA substrate, single binary preserved | Accepted | 2026-06-23 |
| [0007](0007-composition-root-monolith.md) | Composition-root monolith (`cmd/jul/main.go`) | Accepted — delivered | 2026-06-30 |
| [0008](0008-gofast-x-tools-technical-debt.md) | `gofast` / `x/tools` dependency pin (technical debt) | Resolved (vendored gofast) | 2026-06-30 |
| [0009](0009-two-tier-editing.md) | Two-tier editing for complex route types (Quick vs. Designer) | Accepted | 2026-06-30 |
| [0010](0010-console-rbac.md) | Console RBAC: local principals, roles, and scoped tokens | Accepted — delivered | 2026-07-08 |
| [0011](0011-reload-plan.md) | ReloadPlan: a single, side-effect-free reload transaction | Accepted — delivered | 2026-07-16 |
| [0012](0012-oss-open-core-boundary.md) | OSS / open-core boundary | Accepted | 2026-07-20 |
| [0013](0013-project-operating-model-and-completeness.md) | Project operating model, core completeness, and technical experiments | Accepted | 2026-08-03 |
| [0014](0014-operability-surfaces.md) | Appropriate operability surfaces for Console, API, CLI, and raw configuration | Accepted | 2026-08-03 |
| [0015](0015-managed-apply-terminal-ledger.md) | Managed-apply terminal ledger, exactly-once finalization, and audit-closure defaults | Accepted — amended by #226 | 2026-07-24 |

## Reading order

- **What the product is and how work enters it:** [0013](0013-project-operating-model-and-completeness.md),
  [0003](0003-maturity-and-ga.md), [0012](0012-oss-open-core-boundary.md).
- **What operators get:** [0014](0014-operability-surfaces.md), [0004](0004-console-ui-invariants.md),
  [0009](0009-two-tier-editing.md), [0010](0010-console-rbac.md), [0006](0006-console-v2-stack.md).
- **How the runtime behaves:** [0011](0011-reload-plan.md),
  [0015](0015-managed-apply-terminal-ledger.md), [0007](0007-composition-root-monolith.md).
- **Technology boundaries:** [0001](0001-language-strategy.md), [0002](0002-protocol-adaptation.md),
  [0008](0008-gofast-x-tools-technical-debt.md), [0005](0005-soak-post-ga-gate.md).

## Numbering history

ADR 0015 was originally numbered 0013 and shared that number with
[ADR 0013](0013-project-operating-model-and-completeness.md). It was renumbered on 2026-08-12 (#257).
Historical audit records under `docs/audit/old/` still cite the old number and are deliberately left
unmodified.
