# D11 maintainer-authorized AI review record

All112 original rows have individual dispositions; seven supplemental rows do not change the denominator. Original230-row TSV and112-row frozen JSON remain immutable. GitHub Copilot checked the16 original rows below at integrated source `c33c6de2743ad8a2a5225d3200138ed920e7f90a`. The maintainer explicitly replaced the human-only closure condition with documented AI review plus maintainer acceptance in [the authorization record](https://github.com/victornife/jul/issues/515#issuecomment-6155828469). **No independent human spot-check is claimed.** Original area-source verdicts and failures remain historical evidence; resolved findings are not reopened by their original defect-linked verdicts.

## Required acceptance

Review at least12 distinct **original** IDs (ceil(112/10)). The16-row actual sample spans config/deployment/security/Console/CLI, resolved selected defects and historical limitations. Supplemental deployment rows do not count toward12.

The companion JSON records the reviewer, exact reviewed source, actual sample, result, corrections and maintainer authorization. Human reviewers retain the `maintainer-human` / `maintainer-assigned-human` kinds. This review uses `maintainer-authorized-ai` and `independent_human_review=false`; the guard requires the recorded maintainer approval. Final closing-change CI and integration are separate from the sampled runtime source and remain required before issue closure.

The checker retains frozen identities, the12-original-row minimum, path/evidence/test checks and exact source-SHA validation. Unauthorized AI completion or a nonhuman identity recorded as human fails. CI runs the checker and22 contradiction fixtures; preparing a packet alone still does not complete review.

## Actual original sample

| ID | Original surface | Area | Disposition |
| --- | --- | --- | --- |
| D11-020 | `burn-in-full.toml` | configuration | fixed in this PR |
| D11-024 | `burn-in-resilience.toml` | configuration | defect filed/linked |
| D11-058 | `docs/config-value-contract.json` | configuration | correct |
| D11-067 | `docs/reload-semantics.md` | configuration | fixed in this PR |
| D11-110 | `server.everything.toml` | configuration | fixed in this PR |
| D11-055 | `docs/audit-sink-hot-reload.md` | security-protocol-observability | fixed in this PR |
| D11-060 | `docs/grpc-transcoding.md` | security-protocol-observability | fixed in this PR |
| D11-066 | `docs/otel.md` | security-protocol-observability | fixed in this PR |
| D11-083 | `docs/unix-http-upstreams.md` | security-protocol-observability | fixed in this PR |
| D11-072 | `docs/specs/console-rbac.md` | console-api-cli | fixed in this PR |
| D11-104 | `internal/admin/**; internal/adminapi/**; internal/apicontract/**` | console-api-cli | defect filed/linked |
| D11-109 | `scripts/* (other operational and test scripts)` | examples-workflows-specifications | defect filed/linked |
| D11-084 | `docs/vision/README.md` | examples-workflows-specifications | correct |
| D11-078 | `docs/specs/year-1.md` | examples-workflows-specifications | fixed in this PR |
| D11-063 | `docs/nginx-assessment.schema.json` | examples-workflows-specifications | correct |
| D11-028 | `cmd/jul/* (other CLI and help source files)` | console-api-cli | correct |

## D11-020: burn-in-full.toml

Original reason/limit: parsed by make config-check if root/example; runtime behavior and secrets not manually verified for every value

Current paths: burn-in-full.toml. Owner: Single-agent D11 implementation reviewer; maintainer/assigned reviewer owns independent acceptance.

Verdict: **fixed in this PR**; [establishing PR](https://github.com/victornife/jul/pull/596).

Reviewed claims:
- Historical Phase2A selected HTTP/TLS workload, active health/cache/auth/WAF/OTel configuration; five minutes is smoke, not all-feature or consolidated criterion-5 proof

Authority: `scripts/burn-in-load.go`, `docs/adr/0005-soak-post-ga-gate.md`, `internal/config/parser.go`.

Named tests: `TestCompressionDefaults`, `TestHealthCheckDefaults`.

Evidence:
- [evidence/2026-10-13-d11/config-check.log](evidence/2026-10-13-d11/config-check.log)
- [evidence/2026-10-13-d11/contract-tests.log](evidence/2026-10-13-d11/contract-tests.log)
- [evidence/2026-10-13-d11/config-runtime-tests.log](evidence/2026-10-13-d11/config-runtime-tests.log)
- [evidence/2026-10-13-d11/feature-tests-with-environment-failures.log](evidence/2026-10-13-d11/feature-tests-with-environment-failures.log)
- [evidence/2026-10-13-d11/focused-feature-tests.log](evidence/2026-10-13-d11/focused-feature-tests.log)

Limits:
- Source/contract review and deterministic tests; config-check loads with full tags and skip-static-roots, not proof of every enabled external integration or long-duration stability
- Synthetic secrets/PKI and CWD-relative example resources are deployment prerequisites, not production certification

## D11-024: burn-in-resilience.toml

Original reason/limit: parsed by make config-check if root/example; runtime behavior and secrets not manually verified for every value

Current paths: burn-in-resilience.toml. Owner: Single-agent D11 implementation reviewer; maintainer/assigned reviewer owns independent acceptance.

Verdict: **defect filed/linked**; [establishing PR](https://github.com/victornife/jul/pull/596).

Reviewed claims:
- Bounded/unlimited HTTP and bounded TCP; corrected -fault invocation reaches /bounded; generator does not prove all admission protocols, multi-hour stream survival or a fixed 7.5KB cost

Authority: `scripts/burn-in-load.go`, `scripts/burn-in-stream-load.go`, `internal/handler/proxy.go`, `internal/upstream/admission.go`.

Named tests: `TestProxyAdmissionRejectsOverLimit`, `TestProxyAdmissionQueuesThenServes`, `TestScenarioLongLivedStreamAccounting`.

Evidence:
- [evidence/2026-10-13-d11/config-check.log](evidence/2026-10-13-d11/config-check.log)
- [evidence/2026-10-13-d11/contract-tests.log](evidence/2026-10-13-d11/contract-tests.log)
- [evidence/2026-10-13-d11/config-runtime-tests.log](evidence/2026-10-13-d11/config-runtime-tests.log)
- [evidence/2026-10-13-d11/tcp-short-read.log](evidence/2026-10-13-d11/tcp-short-read.log)
- [evidence/2026-10-13-d11/feature-tests-with-environment-failures.log](evidence/2026-10-13-d11/feature-tests-with-environment-failures.log)
- [evidence/2026-10-13-d11/focused-feature-tests.log](evidence/2026-10-13-d11/focused-feature-tests.log)

Limits:
- Source/contract review and deterministic tests; config-check loads with full tags and skip-static-roots, not proof of every enabled external integration or long-duration stability
- Synthetic secrets/PKI and CWD-relative example resources are deployment prerequisites, not production certification
- Short valid TCP reads are misclassified by the current load generator; runtime stream contracts are separate from this harness defect

Filed finding owners (selected corrections merged; original source failures retained): https://github.com/victornife/jul/issues/594.

## D11-058: docs/config-value-contract.json

Original reason/limit: structural link/TOML checks where applicable; behavioral assertions require independent semantic review

Current paths: docs/config-value-contract.json. Owner: Single-agent D11 implementation reviewer; maintainer/assigned reviewer owns independent acceptance.

Verdict: **correct**; [establishing PR](https://github.com/victornife/jul/pull/596).

Reviewed claims:
- Sorted public scalar/grammar/enum/bounds/zero-semantics metadata consumed by owning generator; no replacement runtime validator and no hand-edit to generated artifacts

Authority: `internal/config/value_contract_test.go`, `internal/configcontract/valuecontract.go`, `docs/generated/config-metadata.json`.

Named tests: `TestConfigValueContractCoversEveryNumericPublicLeaf`, `TestValueContractPathsResolveAgainstSchema`, `TestParseNumericBoundAgainstEveryAuditedIntegerAndRatio`.

Evidence:
- [evidence/2026-10-13-d11/config-check.log](evidence/2026-10-13-d11/config-check.log)
- [evidence/2026-10-13-d11/contract-tests.log](evidence/2026-10-13-d11/contract-tests.log)
- [evidence/2026-10-13-d11/config-runtime-tests.log](evidence/2026-10-13-d11/config-runtime-tests.log)

Limits:
- Source/contract review and deterministic tests; config-check loads with full tags and skip-static-roots, not proof of every enabled external integration or long-duration stability
- Synthetic secrets/PKI and CWD-relative example resources are deployment prerequisites, not production certification

## D11-067: docs/reload-semantics.md

Original reason/limit: Targeted current/release claim corrected; full behavioral and platform assertions remain unreviewed

Current paths: docs/reload-semantics.md. Owner: Single-agent D11 implementation reviewer; maintainer/assigned reviewer owns independent acceptance.

Verdict: **fixed in this PR**; [establishing PR](https://github.com/victornife/jul/pull/596).

Reviewed claims:
- Managed preflight/restoration versus file_owned external swap; no_change before Prepare, pinned serving reload-timeout budget, exact-ID finalization, stage/discard CAS and resource retirement; fixed opening universal connection-survival claim
- Legacy rl_<sequence> lookup remains accepted; unknown IDs return404; new operations use instance-scoped IDs

Authority: `internal/app/config_apply.go`, `internal/app/config_apply_stage.go`, `internal/server/reload_plan.go`, `internal/admin/managed_apply_registry.go`.

Named tests: `TestCoordinatorApplyRawValidationFailureNoWrite`, `TestCoordinatorApplyRawRestartRequiredCanStage`, `TestCoordinatorApplyRawRestoresOnSubmitFailure`, `TestManagedApplyEnforcesSingleReloadTimeoutBudget`, `TestManagedApplyGet_ResponseRules`, `TestParseManagedApplyIDStructure`.

Evidence:
- [evidence/2026-10-13-d11/config-check.log](evidence/2026-10-13-d11/config-check.log)
- [evidence/2026-10-13-d11/contract-tests.log](evidence/2026-10-13-d11/contract-tests.log)
- [evidence/2026-10-13-d11/config-runtime-tests.log](evidence/2026-10-13-d11/config-runtime-tests.log)
- [evidence/2026-10-13-d11/legacy-id-contract.log](evidence/2026-10-13-d11/legacy-id-contract.log)

Limits:
- Source/contract review and deterministic tests; config-check loads with full tags and skip-static-roots, not proof of every enabled external integration or long-duration stability
- Synthetic secrets/PKI and CWD-relative example resources are deployment prerequisites, not production certification

## D11-110: server.everything.toml

Original reason/limit: parsed by make config-check if root/example; runtime behavior and secrets not manually verified for every value

Current paths: server.everything.toml. Owner: Single-agent D11 implementation reviewer; maintainer/assigned reviewer owns independent acceptance.

Verdict: **fixed in this PR**; [establishing PR](https://github.com/victornife/jul/pull/596).

Reviewed claims:
- Broad synthetic demonstration with env/file references and unshipped ACME/discovery/CGI peers; corrected every-feature/reachable promise without changing any values

Authority: `internal/config/schema.go`, `internal/config/secrets.go`, `scripts/config-check.sh`, `docs/feature-status.yaml`.

Named tests: `TestPluginConfigSecretResolutionTraversesMapStructs`, `TestHealthHeaderSecretResolution`.

Evidence:
- [evidence/2026-10-13-d11/config-check.log](evidence/2026-10-13-d11/config-check.log)
- [evidence/2026-10-13-d11/contract-tests.log](evidence/2026-10-13-d11/contract-tests.log)
- [evidence/2026-10-13-d11/config-runtime-tests.log](evidence/2026-10-13-d11/config-runtime-tests.log)

Limits:
- Source/contract review and deterministic tests; config-check loads with full tags and skip-static-roots, not proof of every enabled external integration or long-duration stability
- Synthetic secrets/PKI and CWD-relative example resources are deployment prerequisites, not production certification

## D11-055: docs/audit-sink-hot-reload.md

Original reason/limit: structural link/TOML checks where applicable; behavioral assertions require independent semantic review

Current paths: docs/audit-sink-hot-reload.md. Owner: Single-agent D11 implementation reviewer; maintainer/assigned reviewer owns independent acceptance.

Verdict: **fixed in this PR**; [establishing PR](https://github.com/victornife/jul/pull/601).

Reviewed claims:
- audit_log/audit_max_size/audit_keep/audit_fail_closed/require_managed_mutations use prepared durable sink transitions and event-time destination selection
- history_keep is independently hot and prunes after Publish, with advisory failure; history_dir and config authority are separate restart-bound fields
- Durable sink failure can degrade health/readiness and gate mutations; it does not retrospectively change a completed traffic request

Authority: `internal/admin/audit_sink_runtime.go`, `internal/admin/audit_admin_runtime.go`, `docs/generated/config-lifecycle.json`, `scripts/semantic_drift.py`.

Named tests: `TestAuditSinkConcurrentCutoverExactlyOneDestination`, `TestHistoryRetentionStagesThenPrunesOnlyAfterPublish`, `TestHistoryRetentionPruneFailureIsAdvisory`, `test_real_audit_history_retention_consumer_rejects_restart_drift`.

Evidence:
- [evidence/2026-10-13-d11/guides-audit-owner.log](evidence/2026-10-13-d11/guides-audit-owner.log)
- [2026-10-13-d11-guides.md](2026-10-13-d11-guides.md)

Limits:
- Source-backed operator-contract review, not every-line code/security certification; named tests are linked to bounded owning contracts rather than blanket feature certification
- Policy/design/history sections are explicitly separated from current implementation and maturity; no new soak, external provider certification or historical evidence relabeling

## D11-060: docs/grpc-transcoding.md

Original reason/limit: structural link/TOML checks where applicable; behavioral assertions require independent semantic review

Current paths: docs/grpc-transcoding.md. Owner: Single-agent D11 implementation reviewer; maintainer/assigned reviewer owns independent acceptance.

Verdict: **fixed in this PR**; [establishing PR](https://github.com/victornife/jul/pull/601).

Reviewed claims:
- Unary retry eligibility combines protobuf idempotency with safe HTTP binding: UNKNOWN alone grants nothing, but GET/PUT/DELETE can retry; unsafe POST/PATCH UNKNOWN cannot
- Backend connection replacement is workload identity/generation-scoped; nominal60s retired-cache eviction is not an instantaneous trust-revocation SLO
- Resolved TLS policy owns roots/name/mTLS; streaming is not replayed as unary traffic

Authority: `internal/transcode`, `internal/backendtls`, `internal/upstream/retry.go`.

Named tests: `TestTranscodedRetryIdempotencyMatrix`, `TestTranscodedUnaryDoesNotRetryApplicationErrors`, `TestTranscodedRetryHonoursAttemptCap`, `TestConnCacheEvictsAReplacedWorkloadOnReconcile`.

Evidence:
- [evidence/2026-10-13-d11/guides-transcode-contracts.log](evidence/2026-10-13-d11/guides-transcode-contracts.log)
- [2026-10-13-d11-guides.md](2026-10-13-d11-guides.md)

Limits:
- Source-backed operator-contract review, not every-line code/security certification; named tests are linked to bounded owning contracts rather than blanket feature certification
- Policy/design/history sections are explicitly separated from current implementation and maturity; no new soak, external provider certification or historical evidence relabeling

## D11-066: docs/otel.md

Original reason/limit: structural link/TOML checks where applicable; behavioral assertions require independent semantic review

Current paths: docs/otel.md. Owner: Single-agent D11 implementation reviewer; maintainer/assigned reviewer owns independent acceptance.

Verdict: **fixed in this PR**; [establishing PR](https://github.com/victornife/jul/pull/601).

Reviewed claims:
- OTel url.path uses URL.Path without query, while host/path/user-agent may still contain sensitive values; raw Slog access records separately include query
- Access-log fields are method,host,path,query,status,bytes,duration_ms,client_ip,request_id,user_agent plus conditional trust/result/reason/trace fields and slog envelope
- stdout obeys global log level; configured file/syslog sinks emit info; Console-tail projection is not a promise of raw-sink redaction
- Runnable check uses -config; collector, trust and writable paths remain external prerequisites

Authority: `internal/observability/tracing.go`, `internal/middleware/logging.go`, `internal/observability/logging.go`, `internal/observability/sinks.go`, `testdata/otel.toml`.

Named tests: `TestSlogSinkOutput`, `TestAccessLogCapturesTraceID`, `TestDynamicRootSamplerUpdateCutover`.

Evidence:
- [evidence/2026-10-13-d11/guides-observation-owner.log](evidence/2026-10-13-d11/guides-observation-owner.log)
- [evidence/2026-10-13-d11/guides-access-fields.log](evidence/2026-10-13-d11/guides-access-fields.log)
- [evidence/2026-10-13-d11/guides-otel-config.log](evidence/2026-10-13-d11/guides-otel-config.log)
- [2026-10-13-d11-guides.md](2026-10-13-d11-guides.md)

Limits:
- Source-backed operator-contract review, not every-line code/security certification; named tests are linked to bounded owning contracts rather than blanket feature certification
- Policy/design/history sections are explicitly separated from current implementation and maturity; no new soak, external provider certification or historical evidence relabeling

## D11-083: docs/unix-http-upstreams.md

Original reason/limit: structural link/TOML checks where applicable; behavioral assertions require independent semantic review

Current paths: docs/unix-http-upstreams.md. Owner: Single-agent D11 implementation reviewer; maintainer/assigned reviewer owns independent acceptance.

Verdict: **fixed in this PR**; [establishing PR](https://github.com/victornife/jul/pull/601).

Reviewed claims:
- Named Unix upstreams implement plaintextHTTP1.1 with preserved Host and isolated opaque transport identity; TLS/h2c/directUnix proxy_pass remain rejected
- Shared balancing/resilience now includes consistent_hash; connect checks dial Unix but HTTP-health checks on Unix pools remain unsupported
- #407 hosted AF_UNIX/platform and #516 unchanged Windows-service evidence are source-bound; local environment AF_UNIX denial is a failure, not a pass

Authority: `internal/handler/proxy_unix_test.go`, `internal/upstream`, `.github/workflows/issue407-quality.yml`, `docs/audit/2026-10-12-deployment-journeys.md`.

Named tests: `TestProxyUnixHTTPPreservesHostAndForwardsBody`, `TestProxyUnixHTTPRejectsHTTPSBeforeTraffic`, `TestProxyUnixHTTPGatewayErrorDoesNotExposePath`.

Evidence:
- [2026-10-13-d11-guides.md](2026-10-13-d11-guides.md)
- [https://github.com/victornife/jul/actions/runs/38377283466](https://github.com/victornife/jul/actions/runs/38377283466)
- [evidence/2026-10-13-d11/feature-tests-with-environment-failures.log](evidence/2026-10-13-d11/feature-tests-with-environment-failures.log)
- [evidence/2026-10-12-deployment/manifest.json](evidence/2026-10-12-deployment/manifest.json)

Limits:
- Source-backed operator-contract review, not every-line code/security certification; named tests are linked to bounded owning contracts rather than blanket feature certification
- Policy/design/history sections are explicitly separated from current implementation and maturity; no new soak, external provider certification or historical evidence relabeling

## D11-072: docs/specs/console-rbac.md

Original reason/limit: structural link/TOML checks where applicable; behavioral assertions require independent semantic review

Current paths: docs/specs/console-rbac.md. Owner: Single-agent D11 implementation reviewer; maintainer/assigned reviewer owns independent acceptance.

Verdict: **fixed in this PR**; [establishing PR](https://github.com/victornife/jul/pull/604).

Reviewed claims:
- Current permission authority is Catalog/v1WriteCatalog with exact grants and object checks, not the old exhaustive-planning-matrix claim
- Correct current token ID/storage/role permissions and lifecycle; #514 review complete without activating future phase targets
- #595 and #602 are separately selected repairs; no accepted bug disposition or draft-as-merged claim

Authority: `internal/admin/route_catalog.go`, `internal/admin/route_catalog_v1_write.go`, `internal/rbac/permission.go`, `internal/rbac/role.go`, `internal/rbac/token.go`, `internal/admin/rbac.go`.

Named tests: `TestCatalogExactlyOneAuthorizationMode`, `TestRouteTransportPolicyInventory`.

Evidence:
- [2026-10-13-d11-console-cli.md](2026-10-13-d11-console-cli.md)
- [evidence/2026-10-13-d11/console-cli-focused.log](evidence/2026-10-13-d11/console-cli-focused.log)
- [evidence/2026-10-13-d11/console-api-owner.log](evidence/2026-10-13-d11/console-api-owner.log)
- [evidence/2026-10-13-d11/console-cli-owner.log](evidence/2026-10-13-d11/console-cli-owner.log)
- [https://github.com/victornife/jul/pull/592](https://github.com/victornife/jul/pull/592)
- [https://github.com/victornife/jul/pull/597](https://github.com/victornife/jul/pull/597)
- [https://github.com/victornife/jul/actions/runs/38377283466](https://github.com/victornife/jul/actions/runs/38377283466)

Limits:
- Source-backed named operator contracts and deterministic tests; not every-line security/GUI/WCAG/native-production certification
- Local broad AF_UNIX corpus failures remain failed; focused CLI execution excludes those scenarios and hosted source-bound evidence is separate
- Runtime/Console corrections597/600/603 are merged and source-bound in the final resolution ledger; no new maturity, token-management API or long soak

## D11-104: internal/admin/**; internal/adminapi/**; internal/apicontract/**

Original reason/limit: generated OpenAPI, route classification, targeted admin mutation permission comparison and TLS self-lockout predicate sampled; full authorization matrix and Console UI not exercised

Current paths: 480 expanded entries preserved in the [companion ledger](2026-10-13-d11-certification.json). Owner: Single-agent D11 implementation reviewer; maintainer/assigned reviewer owns independent acceptance.

Verdict: **defect filed/linked**; [establishing PR](https://github.com/victornife/jul/pull/604).

Reviewed claims:
- Entire original admin/adminapi/apicontract wildcard retained: current/internal-vs-external routes, transport/auth/permissions, DTO/error/OpenAPI contracts and mutation/history/idempotency owner boundaries
- #517 config/history/client coverage proof is path-scoped and source-bound; server authorization remains authoritative
- #595 known-grant confirmation defect and #602 CSV/auth-failure gaps have retained reproducible corrections integrated through PR597/603

Authority: `internal/admin`, `internal/adminapi`, `internal/apicontract`, `docs/admin-api.md`, `SECURITY.md`.

Named tests: `TestEveryExternalRouteAppearsInOpenAPIAndViceVersa`, `TestErrorCatalogueIsFullyPublished`, `TestSchemasComeFromTheGoDTOs`, `TestCatalogExactlyOneAuthorizationMode`.

Evidence:
- [2026-10-13-d11-console-cli.md](2026-10-13-d11-console-cli.md)
- [evidence/2026-10-13-d11/console-cli-focused.log](evidence/2026-10-13-d11/console-cli-focused.log)
- [evidence/2026-10-13-d11/console-api-owner.log](evidence/2026-10-13-d11/console-api-owner.log)
- [evidence/2026-10-13-d11/console-cli-owner.log](evidence/2026-10-13-d11/console-cli-owner.log)
- [https://github.com/victornife/jul/pull/592](https://github.com/victornife/jul/pull/592)
- [https://github.com/victornife/jul/pull/597](https://github.com/victornife/jul/pull/597)
- [https://github.com/victornife/jul/actions/runs/38377283466](https://github.com/victornife/jul/actions/runs/38377283466)

Limits:
- Source-backed named operator contracts and deterministic tests; not every-line security/GUI/WCAG/native-production certification
- Local broad AF_UNIX corpus failures remain failed; focused CLI execution excludes those scenarios and hosted source-bound evidence is separate
- Runtime/Console corrections597/600/603 are merged and source-bound in the final resolution ledger; no new maturity, token-management API or long soak

Filed finding owners (selected corrections merged; original source failures retained): https://github.com/victornife/jul/issues/595, https://github.com/victornife/jul/issues/602.

Four rebuilt content-hashed bundle paths are explicitly mapped old-to-new in path_changes for this row; PR597 build/embedding evidence owns that rename. The frozen original expansion is unchanged.

## D11-109: scripts/* (other operational and test scripts)

Original reason/limit: inventory aggregate excluding individually listed scripts; comments and operational side effects need script-by-script review

Current paths: 57 expanded entries preserved in the [companion ledger](2026-10-13-d11-certification.json). Owner: Single-agent D11 implementation reviewer; maintainer/assigned reviewer owns independent acceptance.

Verdict: **defect filed/linked**; [establishing PR](https://github.com/victornife/jul/pull/607).

Reviewed claims:
- All57 originally expanded operational/test-script paths are retained with hashes, callers and applicable Bash/Python/Node syntax checks
- Scorer/build/deployment/coverage/discovery/signal/load scripts are scoped to their callers; native PowerShell and environment/provider constraints remain distinct from pass
- Reproducible TCP/profile/fuzz-discovery/default-tag defects are selected repairs in #597/#600/#606, with failing originals and regression evidence; burn-in-wasm usage typo corrected

Authority: `scripts`, `Makefile`, `.github/workflows/ci.yml`, `docs/soak-procedures.md`.

Named tests: Source/consumer contract and executions listed below.

Evidence:
- [evidence/2026-10-13-d11/operations-inventory.json](evidence/2026-10-13-d11/operations-inventory.json)
- [evidence/2026-10-13-d11/examples-check.log](evidence/2026-10-13-d11/examples-check.log)
- [evidence/2026-10-13-d11/assessment-schema.log](evidence/2026-10-13-d11/assessment-schema.log)
- [evidence/2026-10-13-d11/assessment-owner.log](evidence/2026-10-13-d11/assessment-owner.log)
- [evidence/2026-10-13-d11/c5-docs.log](evidence/2026-10-13-d11/c5-docs.log)
- [evidence/2026-10-13-d11/c5-semantic.log](evidence/2026-10-13-d11/c5-semantic.log)
- [evidence/2026-10-13-d11/c5-config.log](evidence/2026-10-13-d11/c5-config.log)
- [2026-10-13-d11-examples-workflows.md](2026-10-13-d11-examples-workflows.md)
- [evidence/2026-10-13-d11/c5-consumer-identities.json](evidence/2026-10-13-d11/c5-consumer-identities.json)

Limits:
- This is wildcard consumer/contract reconciliation, not every-line code/security audit or native execution of every script. Selected corrective PRs are merged; original and corrected executions retain separate identities.
- Evidence is source/build/platform scoped; documentation and numeric checks do not certify the whole product or advance feature maturity.

Filed finding owners (selected corrections merged; original source failures retained): https://github.com/victornife/jul/issues/594, https://github.com/victornife/jul/issues/598, https://github.com/victornife/jul/issues/605.

## D11-084: docs/vision/README.md

Original reason/limit: structural link/TOML checks where applicable; behavioral assertions require independent semantic review

Current paths: docs/vision/README.md, docs/vision/engineering-context.md. Owner: Single-agent D11 implementation reviewer; maintainer/assigned reviewer owns independent acceptance.

Verdict: **correct**; [establishing PR](https://github.com/victornife/jul/pull/607).

Reviewed claims:
- Original engineering-vision surface was moved to engineering-context.md; current README is the approved #576 public vision
- Both roles remain explicitly mapped; public words are preserved byte-for-byte against current main and practical/status/model own present engineering claims

Authority: `docs/vision/README.md`, `docs/vision/engineering-context.md`, `docs/practical-guide.md`, `docs/feature-status.yaml`.

Named tests: Source/consumer contract and executions listed below.

Evidence:
- [evidence/2026-10-13-d11/operations-inventory.json](evidence/2026-10-13-d11/operations-inventory.json)
- [evidence/2026-10-13-d11/examples-check.log](evidence/2026-10-13-d11/examples-check.log)
- [evidence/2026-10-13-d11/assessment-schema.log](evidence/2026-10-13-d11/assessment-schema.log)
- [evidence/2026-10-13-d11/assessment-owner.log](evidence/2026-10-13-d11/assessment-owner.log)
- [evidence/2026-10-13-d11/c5-docs.log](evidence/2026-10-13-d11/c5-docs.log)
- [evidence/2026-10-13-d11/c5-semantic.log](evidence/2026-10-13-d11/c5-semantic.log)
- [evidence/2026-10-13-d11/c5-config.log](evidence/2026-10-13-d11/c5-config.log)
- [2026-10-13-d11-examples-workflows.md](2026-10-13-d11-examples-workflows.md)
- [evidence/2026-10-13-d11/c5-consumer-identities.json](evidence/2026-10-13-d11/c5-consumer-identities.json)

Limits:
- Public purpose/aspiration is not a shipped capability inventory; historical engineering sequencing is not current semantic certification.
- Evidence is source/build/platform scoped; documentation and numeric checks do not certify the whole product or advance feature maturity.

## D11-078: docs/specs/year-1.md

Original reason/limit: structural link/TOML checks where applicable; behavioral assertions require independent semantic review

Current paths: docs/specs/year-1.md. Owner: Single-agent D11 implementation reviewer; maintainer/assigned reviewer owns independent acceptance.

Verdict: **fixed in this PR**; [establishing PR](https://github.com/victornife/jul/pull/607).

Reviewed claims:
- Historical quarters/maturity/task proposals do not override current delivered feature status
- Added a current-reading boundary and authority links; retained original proposal body and dated sequencing

Authority: `docs/specs/year-1.md`, `internal/lifecycle`, `docs/generated/config-lifecycle.json`, `docs/feature-status.yaml`, `docs/operating-model.md`.

Named tests: Source/consumer contract and executions listed below.

Evidence:
- [evidence/2026-10-13-d11/operations-inventory.json](evidence/2026-10-13-d11/operations-inventory.json)
- [evidence/2026-10-13-d11/examples-check.log](evidence/2026-10-13-d11/examples-check.log)
- [evidence/2026-10-13-d11/assessment-schema.log](evidence/2026-10-13-d11/assessment-schema.log)
- [evidence/2026-10-13-d11/assessment-owner.log](evidence/2026-10-13-d11/assessment-owner.log)
- [evidence/2026-10-13-d11/c5-docs.log](evidence/2026-10-13-d11/c5-docs.log)
- [evidence/2026-10-13-d11/c5-semantic.log](evidence/2026-10-13-d11/c5-semantic.log)
- [evidence/2026-10-13-d11/c5-config.log](evidence/2026-10-13-d11/c5-config.log)
- [2026-10-13-d11-examples-workflows.md](2026-10-13-d11-examples-workflows.md)
- [evidence/2026-10-13-d11/c5-consumer-identities.json](evidence/2026-10-13-d11/c5-consumer-identities.json)

Limits:
- Historical disposition only, current_semantic_certification=false; no implied activation or certification of proposed types/tasks.
- Evidence is source/build/platform scoped; documentation and numeric checks do not certify the whole product or advance feature maturity.

**Excluded from current shipped-semantic certification**; historical/context disposition only.

## D11-063: docs/nginx-assessment.schema.json

Original reason/limit: structural link/TOML checks where applicable; behavioral assertions require independent semantic review

Current paths: docs/nginx-assessment.schema.json. Owner: Single-agent D11 implementation reviewer; maintainer/assigned reviewer owns independent acceptance.

Verdict: **correct**; [establishing PR](https://github.com/victornife/jul/pull/607).

Reviewed claims:
- Schema2 assessment exposes provenance/root source, migration classification/status and bounded guidance rather than broad parity
- Draft2020-12 schema and synthetic example validate; owning Go regressions cover determinism, mappings, absolute paths, include failure/redaction and source confinement

Authority: `docs/nginx-assessment.schema.json`, `docs/nginx-assessment.example.json`, `internal/migrate/nginx/assessment_types.go`, `internal/migrate/nginx`.

Named tests: `TestAssessmentSchemaV2IncludesRootSourceProvenance`, `TestAssessmentSchemaV2IsDeterministicAcrossDirectories`, `TestImportIncludeRootAndSymlinkEscape`, `TestImportIncludeParseErrorAndSecretRedaction`.

Evidence:
- [evidence/2026-10-13-d11/operations-inventory.json](evidence/2026-10-13-d11/operations-inventory.json)
- [evidence/2026-10-13-d11/examples-check.log](evidence/2026-10-13-d11/examples-check.log)
- [evidence/2026-10-13-d11/assessment-schema.log](evidence/2026-10-13-d11/assessment-schema.log)
- [evidence/2026-10-13-d11/assessment-owner.log](evidence/2026-10-13-d11/assessment-owner.log)
- [evidence/2026-10-13-d11/c5-docs.log](evidence/2026-10-13-d11/c5-docs.log)
- [evidence/2026-10-13-d11/c5-semantic.log](evidence/2026-10-13-d11/c5-semantic.log)
- [evidence/2026-10-13-d11/c5-config.log](evidence/2026-10-13-d11/c5-config.log)
- [2026-10-13-d11-examples-workflows.md](2026-10-13-d11-examples-workflows.md)
- [evidence/2026-10-13-d11/c5-consumer-identities.json](evidence/2026-10-13-d11/c5-consumer-identities.json)

Limits:
- Example/schema consistency and actual owner tests; no fresh paired NGINX runtime or every external estate equivalence.
- Evidence is source/build/platform scoped; documentation and numeric checks do not certify the whole product or advance feature maturity.

## D11-028: cmd/jul/* (other CLI and help source files)

Original reason/limit: CLI help and representative commands sampled; remaining source comments/branches not exhaustively traced

Current paths: 34 expanded entries preserved in the [companion ledger](2026-10-13-d11-certification.json). Owner: Single-agent D11 implementation reviewer; maintainer/assigned reviewer owns independent acceptance.

Verdict: **correct**; [establishing PR](https://github.com/victornife/jul/pull/604).

Reviewed claims:
- Full frozen CLI/help wildcard substantiates local commands and AUTO04 stable /api/v1 remote automation; no private Console fallback
- Credential/profile precedence, normal TLS/mTLS, loopback-only plaintext/no redirects and no raw --token preserve transport/secret boundaries
- Actual process tests enforce exit0–9, terminal polling/Ctrl-C distinction and idempotency/base fences

Authority: `cmd/jul/main.go`, `cmd/jul/remote_cli.go`, `cmd/jul/remote_usage.go`, `docs/remote-cli.md`, `internal/adminapi`.

Named tests: `TestRemoteCLIActualProcessExitContractZeroThroughNine`, `TestRemoteCLIActualBinaryAgainstRealAdminRouteStack`, `TestRemoteExtendedUsage`.

Evidence:
- [2026-10-13-d11-console-cli.md](2026-10-13-d11-console-cli.md)
- [evidence/2026-10-13-d11/console-cli-focused.log](evidence/2026-10-13-d11/console-cli-focused.log)
- [evidence/2026-10-13-d11/console-api-owner.log](evidence/2026-10-13-d11/console-api-owner.log)
- [evidence/2026-10-13-d11/console-cli-owner.log](evidence/2026-10-13-d11/console-cli-owner.log)
- [https://github.com/victornife/jul/pull/592](https://github.com/victornife/jul/pull/592)
- [https://github.com/victornife/jul/pull/597](https://github.com/victornife/jul/pull/597)
- [https://github.com/victornife/jul/actions/runs/38377283466](https://github.com/victornife/jul/actions/runs/38377283466)

Limits:
- Source-backed named operator contracts and deterministic tests; not every-line security/GUI/WCAG/native-production certification
- Local broad AF_UNIX corpus failures remain failed; focused CLI execution excludes those scenarios and hosted source-bound evidence is separate
- Runtime/Console corrections597/600/603 are merged and source-bound in the final resolution ledger; no new maturity, token-management API or long soak

## Supplemental verification

Check new deployment/runbook rows D11-N001/N002 against #516 original identities and limits, and N003 historical evidence without relabeling native/archive reconciliation as fresh execution. N004 Console and N005–N007 vision/practical/soak consumers remain supplemental. These checks do not replace12 original IDs.

## Actual review record

- [x] Reviewer: GitHub Copilot; method explicitly authorized by maintainer `victornife`, not an independent human review.
- [x] Reviewed source: `c33c6de2743ad8a2a5225d3200138ed920e7f90a`.
- [x]16 distinct original IDs checked; no supplemental ID counted.
- [x] Result and metadata corrections recorded in companion JSON.
- [ ] Final closing-change exact-head checks and integration, recorded on the completion PR and #515.

### Observations by original row

| ID | Actual check and result |
| --- | --- |
| D11-020 | Compared the selected HTTP/TLS mix with `burn-in-load.go`; smoke, external collector/PKI prerequisites and non-native-gRPC/L4 limits are explicit. Compression/health defaults passed. |
| D11-024 | Verified bounded HTTP routing and repeated TCP, not multi-hour sessions. Fragmented echo/short-write/corruption regressions passed; #594 resolution remains separate from its original failure. |
| D11-058 | Checked owning metadata coverage, schema-path resolution and numeric bounds tests; all selected contracts passed. Metadata is not a replacement runtime validator. |
| D11-067 | Compared authority/preflight/stage/restoration and legacy-ID contracts with reload source; selected app/admin checks passed. No universal established-stream survival is claimed. |
| D11-110 | Read the synthetic example's external-service and secret-reference prerequisites; no all-capability execution or production certification is claimed. |
| D11-055 | Compared event-time sink selection and post-Publish history pruning with owning source. Concurrent cutover, deferred prune and advisory prune-failure tests passed. |
| D11-060 | Compared unary safe-binding/protobuf retry rules and30-second retirement/timer intervals with source. Idempotency, terminal errors, attempt caps and replacement eviction tests passed. |
| D11-066 | Compared `semconv.URLPath(r.URL.Path)` and raw access-log Query with the guide; corrected the ledger/packet attribute name. Sink/trace-ID/sampler tests passed; query leakage remains explicit. |
| D11-083 | Read named plaintextHTTP1.1/Host/error and health-check boundaries, owning source/test assertions and #516's source-bound Windows evidence. No new local/native AF_UNIX execution is claimed. |
| D11-072 | Compared current route catalogs, one-token model, hash/ID derivation and future API boundary with the spec; catalog and transport-policy checks passed. Resolved repair status is explicit. |
| D11-104 | Inspected token/audit/CSV attribution, config/rollback confirmation guards and external API DTO/catalog contracts. Five permission-loss UI cases and the selected API contracts passed. |
| D11-109 | Preserved all57 script paths; checked inventory source hashes, full-tag/discovery failure propagation and profiler publication/rejection. Eight harness fixtures and selected TCP/profile cases passed. |
| D11-084 | Verified approved public vision, practical guide and moved engineering-context byte hashes; current gateway capabilities and future sharing remain distinct. |
| D11-078 | Checked the current-reading boundary and authority links. Historical targets and quarter plans remain excluded from shipped-semantic certification. |
| D11-063 | Compared schema2 provenance/status constraints and owning assessment/include tests; selected importer checks passed with the required build tag. No paired external NGINX campaign is claimed. |
| D11-028 | Compared preserved CLI/help inventory and automation source with actual-process exit0-9, real-admin-route and help tests; those selected contracts passed. |

### Source and execution boundaries

The old operations inventory remains byte-for-byte unchanged. Its56 baseline hashes match `616f0ca0ec7ef3f7568ec8b11d0fc1c783df14bc`; `scripts/test_semantic_drift.py` instead matches recorded SHA-256 `f3b62a8e3a9911319d5f49ad3a354f9478f0f0a1784df2d6961f0046f9a60875` at committed source `dbca836acf2749f553a0edcb69feb7c1c016d679`. The sampled integrated file has SHA-256 `d45821e0716436964d8ffede97f02296187fa2c4000b5a159f9fba1a15f8b205`. These sources are not interchangeable. The companion JSON records this provenance correction rather than relabeling an older hash as current.

Focused checks actually run in this review:

- `python3 scripts/test_d11_certification.py` (22 fixtures) and `python3 scripts/check-d11-certification.py --complete`.
- Configuration/config-contract tests for public numeric coverage, path resolution, bounds and compression/health defaults.
- `grpc otel` owner tests for unary retry/replacement, audit/history cutover, trace/log contracts, route policy, managed reload, external API and actual-process CLI.
- Explicit-file TCP fragmentation/corruption tests and HTTP profiler status/trust/empty/truncated/publication tests.
- `python3 scripts/test_full_tag_harnesses.py` (eight actual shell-harness regressions).
- Five selected `config-history-hardening.test.tsx` permission-revocation cases; ten unrelated cases intentionally skipped by the test-name filter, not claimed as run.
- Assessment/include owner tests with `-tags importer`. The first mixed-package invocation lacked that tag and failed setup for this package; its corrected focused invocation passed. Missing test-file lookup attempts were corrected before executing the affected checks.

No CI/pre-push-wide race/fuzz/vulnerability/Console gates were duplicated locally. Original hosted native/coverage/903-test evidence retains its measured source. No runtime feature, maturity, public vision, frozen original inventory, release/tag, #590 campaign or external-provider certification is changed. The scoped AI review found no remaining sampled runtime defect after the metadata corrections above; it is not independent human or whole-product certification.
