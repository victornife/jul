# #625 configuration explanations acceptance matrix

This matrix records the first bounded, server-owned catalog and credits
existing owner flows rather than duplicating their policy or outcome logic.
All candidate explanations are advisory: validation, lifecycle classification,
authorization, apply/stage selection, and runtime outcomes remain authoritative
in their existing owners.

| Area | Candidate explanations delivered | Existing-flow credit and boundary |
| --- | --- | --- |
| Cache | Shared-copy and origin-freshness limits; cookie/session bypass; stale-serving trade-off; full-object range fill when enabled. | The #624 `cache_cutover` assessment and apply result own retain/invalidate, staged/live, and committed/degraded outcomes. The #621 purge preview owns broad-scope confirmation and origin-load implications. This catalog does not recalculate either. |
| Access and trusted proxies | Route-auth removal distinguishes application authorization from CORS/VPN reachability; trusted forwarding identity changes warn on widened trust. | Existing sharing/audience assessment reports declared intent deltas and external-enforcer evidence separately; configuration never proves external enforcement. |
| WAF | Detect is not block; inherited effective policy is compared; body inspection notes buffering/streaming cost and logical-vs-physical budget limits; build support remains validation-owned; request outcome attribution is `not_observed`. | Existing validation and WAF evidence owners remain authoritative. No request is claimed blocked, protected, or externally verified from a candidate. |
| TLS and backend trust | Disabling backend verification is distinguished from disabling encryption; inbound weakening, certificate prerequisites, mTLS prerequisites and new-handshake boundaries are explained. | Certificate issuance, external reachability, and live handshake success remain unverified by preview. |
| Rate and limits | Request counts are distinguished from bandwidth; header/JWT key trust and fallback behavior, shared proxy/NAT IPs, listener connection admission, and already-admitted connections are covered. | Existing limiter behavior and validation define enforcement; explanations do not invent user-count or throughput guarantees. |
| Resilience and timeouts | Retry eligibility/repeated-work risk, bounded admission/queue behavior, and retry deadlines are described. Connect timeout is distinct from read/send inactivity timeouts and streaming behavior. | Existing upstream policy is authoritative; preview does not probe backend health or reachability. |
| Routing and lifecycle | Retargeting identifies the affected route and does not claim destination reachability. | Existing diff/lifecycle and authority assessments own hot vs restart-staged, file-owned/read-only, drift, and apply outcome. Declared audience or a successful apply is not proof of external protection/reachability. |

## Contract and cross-surface evidence

- `ConfigDiff.explanations` is an additive, deterministic, deduplicated field
  capped at 64 entries, with an explicit summary entry on truncation.
- Typed and raw candidate previews share the same candidate diff. Console and
  CLI consume server output and explicitly report an omitted explanations
  field, which can mean no entries were generated or an older server lacks the
  field; neither case establishes that the candidate has no consequences.
- Each entry has a stable code, kind, safe scope, evidence source/state,
  message, and optional next action/documentation reference. Candidate-only
  evidence is not runtime evidence; `not_observed` is not a healthy result.
- Explanations do not perform network access, mutate configuration, add
  permissions, or replace validation/lifecycle decisions. Existing
  `config:raw` preview authorization continues to protect full comparisons.

## NGINX migration disposition

**Not applicable.** This change adds explanations to Jul configuration
previews; it changes no configuration syntax, importer translation,
unsupported/approximate classification, or migration assessment. The NGINX
directive matrix, corpus, and migration E2E therefore need no updates. See
[`docs/nginx-importer.md`](../nginx-importer.md) and
[`docs/nginx-migration-corpus.md`](../nginx-migration-corpus.md).

## Remaining acceptance evidence

- A maintainer/reader walkthrough asking what will happen for representative
  changes is still required; no independent reader trial is claimed here.
- Command-level validation results are recorded in the PR description after
  the final checks run.
