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
| TLS and backend trust | Effective route transport and inherited verification policy are compared across target and policy changes; plaintext is distinguished from unverified TLS. Inbound weakening, certificate prerequisites, mTLS prerequisites and new-handshake boundaries are explained. | Certificate issuance, external reachability, and live handshake success remain unverified by preview. |
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

- The adversarial review of head `a8171797` identified inherited backend TLS
  verification, credential-removal, combined timeout, and decision-point UX
  gaps. The follow-up compares effective route policy, reports remaining CIDR
  gates, preserves both timeout explanations, and surfaces critical and
  truncation entries outside disclosure. The access-control editor can request
  server guidance before handoff; the raw editor continues to preview while
  editing.
- The source review of head `85b5635` found a plaintext downgrade falsely
  reported as verification restored, missed unverified TLS when retargeting to
  another pool, and raw advice cached without its baseline. The follow-up
  compares transport and effective policy and pins raw and cache-override
  previews to the candidate and editable base; a conflict hides stale advice.
  A follow-up self-review also found that a blocked raw handoff still showed
  its old lifecycle assessment; that assessment is now hidden until current.
- Hosted CI on head `85b5635` passed Chromium (24 tests), real-server E2E
  (44 tests), and the coverage gate. These results do not validate later heads.
- Hosted CI on head `6d9dd797` passed Chromium (24 tests), real-server E2E
  (44 tests), and the main coverage gate, but the separate #407 Unix HTTP
  quality gate failed: its narrower changed-statement profile covered 6/9
  (66.67%) statements in the shared transport classifier. Follow-up
  config-package regressions cover the classifier's known and unknown
  transports; the same lean/importer profile scope passes locally at 9/9
  (100%). The new head still requires hosted validation.
- The reviewer accepted the existing Console preview and documented API/CLI
  as the appropriate ADR 0014 surfaces for this preview-only capability; no
  runtime FeatureStatus row is required.
- A real-server browser test covers the candidate response, Console display,
  and keyboard disclosure before apply. Screen-reader and independent operator
  walkthroughs remain unperformed.
- Local Chromium passed 24/24 and the isolated real-server suite passed 44/44
  under Xvfb. The local headless Chromium run failed 10/24 while waiting for
  element stability; a standalone probe produced no animation frames. The
  first isolated real-server run failed 1/44 because the copied fixture lacked
  its reload root; a fresh complete fixture passed. The PR records commands.
- Local changed Go statement coverage on head `85b5635` against `origin/main`,
  combining lean and full-tag profiles, was 279/293 (95.22%). The follow-up
  head `6d9dd797` and the config-package test follow-up each measured 301/315
  (95.56%) with the same method. Final-head hosted validation remains required
  and is recorded on the PR.
- A maintainer/reader walkthrough asking what will happen for representative
  changes is still required; no independent reader trial is claimed here.
- Command-level validation results are recorded in the PR description after
  the final checks run.

## Pending independent walkthrough

Use an isolated managed copy of `testdata/console-e2e.toml`, with its own
history directory and version key. In Configuration, change the `/e2e-upstream`
route target from `http://e2e-pool` to `http://127.0.0.1:8999`, the connect
timeout from `0s` to `1s`, and the read timeout from `0s` to `2s`. Do not apply.

Ask an independent operator what will change, whether the target is known to be
reachable, and what the read timeout means. Before giving hints, record their
answers and any misunderstandings. Using the same preview, check keyboard focus
and disclosure, then have a screen-reader user verify the consequence region,
severity, and expanded supporting detail. Record the browser, assistive
technology, observations, and any corrections without retaining config secrets.
