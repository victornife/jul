# Admin security review continuation

| Item | Evidence |
| --- | --- |
| Date | 2026-10-08 |
| Source baseline | Post-#549 main `33e946d0526cac9ed3f5e92bba1e1f0be1e12cfb` |
| Review owner | [#514](https://github.com/victornife/jul/issues/514), reopened after premature automatic closure |
| Disposition | In progress; focused source publication approved, not completion |
| Previous installment | [2026-10-06 route-policy foundation](2026-10-06-admin-security-review.md) |

## Scope and evidence boundary

PR #549 merged the explicit admin route policy inventory and its CI guards.
It did not deliver the entire line-level review requested by #514. The issue
was reopened; no checklist or feature maturity is promoted by that correction.

This continuation inspected the mutation authorization/attribution pipeline,
the shared authority gate, v1 adapters, request-generation policy selection,
and selected Console request construction and validation/error boundaries.
The remaining production inventory is approximately 28,401 Go lines and
32,005 Console TypeScript lines, before the current continuation edits. A file
inventory is not line-review evidence, and no exhaustive certification is made.

## Request-generation consistency

The profiler enabled flag must follow the same immutable request generation
as authentication and admission. `TestPprofPolicyUsesCapturedRequestGeneration`
exercises both policy transitions and confirms that fresh requests use the
published replacement while an already-pinned request keeps its original
policy. The catalog handler now uses `requestAdminSnapshot(r).cfg`.

The regression failed in both directions before the one-line change and
passed after it, in lean and full-tag builds. Neighboring profiler,
admin:manage and route-policy tests passed. This is a request-policy
consistency correction, not a claim of an unauthenticated access finding.
No configuration leaf, lifecycle classification or generated API shape changed.
NGINX migration classification: **not applicable**, admin-plane policy only.

Focused changed/added Go production coverage: **3/3 statements = 100%**, measured
with the repository's HR-07C block-intersection scorer on the owning regression
profile. No statement exclusion or move filtering was used. Documentation
checks passed during development; exact-head CI and full readiness gates are
not claimed for this uncommitted continuation.

## Private security disposition

Two separately confirmed concerns were routed to **unpublished private
advisories**, following [SECURITY.md](../../SECURITY.md). Reproduction, affected
paths and candidate remediation are retained privately and are intentionally
absent from this public record. The candidates have focused regression and
owning-package evidence. The maintainer approved a focused public source/test
PR with high-level text; this does not authorize advisory publication, a CVE
request, automatic merge or release certification. Affected-version assessment
and the private disclosure records remain separate from the public evidence.

No public vulnerability issue, exploit description, advisory publication,
CVE request or speculative follow-up was made. This concern remains a reason
that #514 cannot be reported complete.

## Console action and transport inventory

The current SPA navigation is declared in `ui/src/app/App.tsx`. Pages load
before identity discovery; server authorization, not visibility of a control,
is the enforcement boundary. The 18 explicit non-safe typed-client actions are
checked by the existing `client-write.test.ts` using the TypeScript parser.
A second structural guard rejects named `fetch`, `EventSource` and
`XMLHttpRequest` transports outside the reviewed client. These guards are not
a proof against arbitrary dynamic JavaScript or malicious source changes.

All rows below inherit TLS-or-loopback, same-origin unsafe-method protection,
bearer-header authentication and catalog-derived admission. The listed grant
is the route grant, not a replacement for inner transition/ownership checks.

| Client action | Method / internal endpoint | Required grant | Admission |
| --- | --- | --- | --- |
| `testRoute` | POST `/api/routes/test` | `config:write` | write |
| `purgeCache` | POST `/cache/purge` | `cache:purge` | write |
| `rollback` | POST `/api/config/rollback` | `history:rollback` | apply |
| `discardPendingRestart` | POST `/api/config/pending-restart/discard` | `config:apply` | apply |
| `diffConfig` | POST `/api/config/diff` | `config:write` | apply |
| `previewRawConfig` | POST `/api/config/preview` | `config:raw` | write |
| `patchConfig` | POST `/api/config/patch` | `config:write` | write |
| `patchConfigBatch` | POST `/api/config/patch/preview` | `config:write` | write |
| `fetchPatchCandidate` | POST `/api/config/patch/candidate` | `config:raw` | write |
| `validateConfig` | POST `/api/config/validate` | `config:write` | apply |
| `applyConfig` | POST `/api/config/apply` | `config:apply` | apply |
| `applyPatchBatch` | POST `/api/config/patch/apply` | `config:apply` | apply |
| `generateConfig` | POST `/api/wizard/generate` | `config:write` | write |
| `generateConfigPatches` | POST `/api/wizard/generate?format=patch` | `config:write` | write |
| `reportClientError` | POST `/api/admin/client-errors` | `observability:read` | write |
| `uploadPluginWasm` | POST `/api/plugins/upload` | `plugins:upload` | write |
| `uploadTranscodeDescriptor` | POST `/api/transcode/descriptor-upload` | `config:write` | write |
| `patchListenerClientAddress` | PATCH `/api/listeners/{addr}/client_address` | `config:trust` | write |

The legacy configuration page has three additional declared commands:
POST `/api/config/settings` and `/api/config/raw` require `config:apply` and
apply admission; POST `/reload` requires `reload:trigger` and write admission.
It renders returned data through text/value assignment rather than HTML
insertion and sends credentials in headers, not URLs. Its inline-script CSP
is still a documented limitation, not equal to the SPA's stricter policy.

Ordinary readback and export also remain permissioned: the current identity
endpoint is authenticated-only, audit export requires `audit:export`, and SSE
events/logs have their declared read grants plus shared lease admission.
Raw/full TOML handoffs are memory-only; storage parsing rejects raw handoffs
and requires re-assessment of stale structured drafts. Private confidentiality
dispositions are tracked separately and are not turned into a blanket pass by
these transport/storage observations.

## Attributable operations and response policy

The accepted upload and purge paths now emit one content-free audit event with
the request identity's principal/token identifier and transport peer. They do
not retain the uploaded filename/path/body, a bearer value or a raw cache key.
`TestPluginUploadAuditUsesAuthenticatedPrincipal` and
`TestCachePurgeAuditUsesAuthenticatedPrincipal` failed on missing events before
the additions and pass after them; disabled-cache no-op does not claim a purge.

The shared JSON writer now sets `Cache-Control: no-store` for success and error
responses. `TestJSONResponsesAreNeverStored` covers the status matrix and an
existing cacheable header. Streaming and non-JSON responses retain their own
contracts and are not claimed covered by that writer test.

Combined current public changed/added Go production coverage:
**33/33 statements = 100%**, by the existing HR-07C scorer with no exclusions or
move filtering. Full-tag owning admin tests pass; the touched Console client
and plugin/editor files pass 59 tests, lint and typecheck. The private candidate
frontend coverage gate passed 822 tests with unchanged thresholds before the
authorized source transfer. API contracts were regenerated from the owning
source; all generated authorities verify unchanged or correctly regenerated.
This evidence is local and uncommitted,
not a relabeled exact-head CI or a completed exhaustive review.

## Focused remediation verification

The combined source passed `make ci-fast`, `make ci-pr`,
`make generated-check`, `make console-check`, `python3 scripts/docs-check.py`,
`python3 scripts/test_docs_check.py` and `python3 scripts/test_semantic_drift.py`.
`ci-pr` includes full-tag lint/tests/build, vet, configuration-example checks
and unchanged security-package floors. The final public Console source suite
passed 824 tests across 75 files; docs passed 1,921 checks. Govulncheck reported
no reachable vulnerability and one non-reachable required-module vulnerability.

These are final-source local checks, not fabricated exact-head hosted results.
Hosted CI, reviewer confirmation and merge evidence remain necessary. No race,
conformance, advisory-publication, release or exhaustive-audit completion is
inferred from a local baseline gate.

## Remaining acceptance work

- Review and merge the approved bounded remediation; retain the private
  affected-version/disclosure decisions and do not imply a published advisory.
- Complete the remaining admin/Console action, data-projection, error-path,
  upload/content-identity, support/redaction and shared-sink review ledger.
- Record final checklist verdicts with named tests and exact-head CI evidence.
- Merge completion evidence before requesting issue closure.
- Keep #517/#527 and all other unactivated candidates deferred. The later
  activated Wave 1/2 implementation issues have not been advanced by this
  continuation or private reporting step.