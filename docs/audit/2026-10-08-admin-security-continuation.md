# Admin security review continuation

| Item | Evidence |
| --- | --- |
| Date | 2026-10-08 |
| Source baseline | Post-#549 main `33e946d0526cac9ed3f5e92bba1e1f0be1e12cfb` |
| Review owner | [#514](https://github.com/victornife/jul/issues/514), reopened after premature automatic closure |
| Disposition | Risk-focused review complete in the reviewed boundaries; closure requires final exact-head CI and merge evidence |
| Previous installment | [2026-10-06 route-policy foundation](2026-10-06-admin-security-review.md) |

## Scope and evidence boundary

PR #549 merged the explicit admin route policy inventory and its CI guards.
It did not deliver the entire line-level review requested by #514. The issue
was reopened; no checklist or feature maturity is promoted by that correction.

This continuation inspected the mutation authorization/attribution pipeline,
the shared authority gate, v1 adapters, request-generation policy selection,
and selected Console request construction and validation/error boundaries.
The production inventory is approximately 28,401 Go lines and
32,005 Console TypeScript lines, before the current continuation edits. A file
inventory is not line-review evidence, and no exhaustive certification is made.

On 2026-10-08 the maintainer accepted a risk-focused close-out after the final
confirmed residual correction, rather than an every-line certification. The
verdicts below cover the inspected enforcement and data-handling boundaries,
their negative tests, and the confirmed findings. They do not certify every
source line or eliminate the documented limitations. Exact-head hosted CI and
merge evidence belong to #514 and the current #62 execution snapshot.

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
checks passed during development. The final-source baseline checks below are
separate from exact-head hosted CI and reviewer/merge evidence.

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
CVE request or speculative follow-up was made. The source corrections and
regressions are included in #550, including the final shared-boundary
consistency correction. The advisories remain draft; affected-version,
release and disclosure decisions are separate work, not silently completed
by closing the review. Restricted finding links are recorded on #514.

## Checklist verdicts

| Original checklist area | Scoped verdict | Named evidence and limits |
| --- | --- | --- |
| Route inventory | Pass in declared route boundary | The 91-pattern inventory, `TestRouteTransportPolicyInventory`, `TestCatalogOwnsMuxRegistrations`, catalog permission/method tests, and origin/transport tests enforce declarations. Only the two probes are transport-exempt; inner transition checks remain additional requirements. |
| Authentication | Reviewed; existing limits retained | `auth_snapshot_test.go`, `internal/rbac/security_negative_test.go`, and the checked typed client cover policy replacement, scoped identities and bearer-header transport. Console tokens remain in session storage; this is not protection against same-origin script compromise. Legacy shared-token identities retain their documented broad authority. |
| Authorization and attribution | Pass in reviewed mutation boundary after corrections | The Console action ledger below, catalog-derived wrappers, authority/ownership and trust checks, `TestPluginUploadAuditUsesAuthenticatedPrincipal`, and `TestCachePurgeAuditUsesAuthenticatedPrincipal`. Route grants do not replace inner apply/adoption/baseline checks. |
| Plugin upload | Reviewed; bounded handling and publication policy retained | `plugin_upload_test.go`, runtime upload-generation tests and `admin_runtime_issue157_concurrency_test.go` cover limits, confined storage and reload interaction. Module content identity remains the #429 contract; storing a module is not authorization to execute it. |
| Support bundles and diagnostics | Pass in reviewed data boundary after corrections | `TestScopedConfigurationDiagnosticRedaction`, `TestScopedPatchAssessmentDiagnosticRedaction`, `TestScopedParameterReadbackRequiresRawGrant`, `TestSafeValidationPreservesFixedClassification`, `TestCollectorErrorsRedactPathsURLsAndSecrets`, and `TestFinalExtractedArchiveSecretScan`. Unknown findings are content-free; recognized classifications retain fixed guidance. Private finding dispositions are separate from publication. |
| pprof | Pass in request-generation boundary after correction | Catalog requires both the profiler flag and `admin:manage`; `TestPprofPolicyUsesCapturedRequestGeneration` tests both policy transitions. Already-pinned requests keep their admitted generation. |
| Admission and rate limiting | Reviewed; existing HR-07A contract retained | `TestAdminRouteCatalogueLimitClassificationMatrix`, `issue158_transition_test.go` and existing admission/lease tests cover declared classes and runtime transitions. No limit floor or admission class was weakened. |
| Browser hardening | Pass with documented legacy-page limitation | Console CSP/frame policy tests, origin guard tests, `TestJSONResponsesAreNeverStored`, and typed-client transport guards. The legacy page's inline-script policy remains weaker than the SPA; structural guards do not prove safety of arbitrary dynamic code. |
| Shared access-log sinks | Reviewed; #502 correction retained | `TestAccessFilePolicyChangeSharesOneWriter` and `TestAccessFilePolicyChangeRotationDuringDrain` exercise shared-owner rotation and generation drain. #502 is complete via #537; immutable v2.1.0 release behavior and earlier soak evidence are not relabeled. |

These are risk-focused verdicts, not independent two-human certification or a
claim that every possible error, platform or malicious-code path was tested.

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
**41/41 statements = 100%**, by the existing HR-07C scorer with no exclusions or
move filtering. Full-tag owning admin tests pass; the touched Console client
and plugin/editor files pass 59 tests, lint and typecheck. The private candidate
frontend coverage gate passed 822 tests with unchanged thresholds before the
authorized source transfer. API contracts were regenerated from the owning
source; all generated authorities verify unchanged or correctly regenerated.
This evidence is measured on the local branch source,
not a relabeled exact-head CI or a completed exhaustive review.

## Focused remediation verification

The pre-residual combined source passed `make ci-fast`, `make ci-pr`,
`make generated-check`, `make console-check`, `python3 scripts/docs-check.py`,
`python3 scripts/test_docs_check.py` and `python3 scripts/test_semantic_drift.py`.
`ci-pr` includes full-tag lint/tests/build, vet, configuration-example checks
and unchanged security-package floors. The final public Console source suite
passed 824 tests across 75 files; docs passed 1,921 checks. Govulncheck reported
no reachable vulnerability and one non-reachable required-module vulnerability.

The final residual source passed the two private sibling regressions, the
public scoped assessment regression, fixed-classification/value-free tests,
the invalid-client-address and lean build-capability policy tests, and both
lean and full-tag owning admin packages.
The complete PR coverage was remeasured as 41/41 statements using the existing
HR-07C scorer. The other full local gates above are historical source-bound
results, not reruns on this final correction. Normal hooks and fresh hosted CI
provide the remaining gates; redundant local race/fuzz/security/Console runs
are intentionally not claimed.

Hosted CI, maintainer confirmation and merge evidence remain necessary. No race,
conformance, advisory-publication, release or exhaustive-audit completion is
inferred from a local baseline gate.

## Closure conditions and retained limits

- Merge #550 only after the final head's hosted checks pass; record its exact
  head and merge SHA on #514 and #62 before closing #514.
- This record supplies all nine scoped checklist verdicts. #549 remains a
  historical partial installment, not retrospective exhaustive certification.
- Keep both private advisories unpublished and retain affected-version,
  release and disclosure decisions in their restricted records.
- Every-line review is not claimed. Known browser/legacy and structural-guard
  limits remain explicit; no speculative implementation issue is activated.
- Keep #517/#527 and all other unactivated candidates deferred. The later
  activated Wave 1/2 implementation issues have not been advanced by this
  continuation or private reporting step.