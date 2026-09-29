# Cache, WAF and plugin source continuation — 2026-09-29

> **Scope:** A focused, 20-commit source review stacked after PR #495. This
> records confirmed defects and bounded fixes, not a complete certification of
> those subsystems or of the 112 documentation-ledger rows still unreviewed.

| Boundary | Reproduced failure and correction |
| --- | --- |
| Cache reuse | Conditional fields missed later ETags, weak comparisons and quoted commas. Range, upgrade and stream decisions missed later field lines. Focused regressions now cover the corrected semantics. |
| Cache freshness and invalidation | Later `Age`, `Date`, `Expires`, `Location` and 304 timing fields could be ignored. Ambiguous repeated validators were stored. The cache now uses conservative timing, invalidates all same-origin targets and rejects ambiguous validators. |
| Cache capacity | Vary metadata and request-derived variant keys were omitted from the memory-tier byte account. Both now contribute to eviction. |
| Plugin fetch and request integrity | Local special-use IPv4 DNS answers were dialable; fetch failure logs included guest URL and transport error text. Both ABIs could replay a partially read request body. These paths now block or fail closed and emit bounded log reasons. |
| Plugin module and KV inputs | Nonregular or already-oversized module paths reached Open/read, and a NUL in a plugin name could overlap another KV namespace. These inputs are rejected before use. |
| WAF preparation | External rule/data files were read without a byte limit, and a policy could be returned after the reload deadline. File reads are limited to 16 MiB each, with a post-assembly and post-compile deadline check. |

Focused regressions passed as each fix landed. One final package pass ran **592
non-soak, non-churn tests** with `-tags='waf wasmplugins'` across `internal/cache`,
`internal/waf`, `internal/plugins` and `internal/config`: all four packages
passed. The named hour-long soak and churn lanes were excluded. The parent
stack's CI is separate evidence; this branch still needs its own PR checks.

Remaining work includes broader source and semantic review, production-like
TLS and platform journeys, and the exact-SHA release closure tracked in #480.
No new feature maturity, full-audit score or release-readiness claim follows
from this batch.
