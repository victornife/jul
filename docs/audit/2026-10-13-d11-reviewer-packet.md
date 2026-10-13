# D11 maintainer / assigned human reviewer packet

All112 original rows have individual dispositions; seven supplemental rows do not change the denominator. Original230-row TSV and112-row frozen JSON remain immutable. This packet is agent preparation. The actual human review is **pending**; proposed samples below are not recorded as completed review.

## Required acceptance

Review at least12 distinct **original** IDs (ceil(112/10)). The16-row proposed sample spans config/deployment/security/Console/CLI, a filed selected defect and historical limitations. It may be replaced with another stratified original sample. New deployment rows may supplement it but do not count toward12.

Pin the final completion PR head / integrated source before reviewing. Inspect each sampled original claim, current source/test/evidence, verdict and limitation. Record reviewer identity, maintainer assignment if relevant, source SHA, actual sampled original IDs, result and corrections in reviewer_spot_check in the companion JSON. Set review_kind to maintainer-human or maintainer-assigned-human only after that person performs the review. Set state=completed only after actual acceptance and approved integration; do not copy proposed_sampled_rows into the actual field as a substitute for reviewing.

The strengthened checker rejects replaced/unknown original IDs, supplemental duplicate/overlap, missing/escaping current paths, unresolved establishing PRs and fabricated completion without a human-kind original-row sample. CI runs both the checker and13 contradiction fixtures.

## Proposed original sample

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

Selected finding owners: https://github.com/victornife/jul/issues/594.

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
- OTel http.target uses URL.Path without query, while host/path/user-agent may still contain sensitive values; raw Slog access records separately include query
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
- Runtime/Console corrections597/600/603 are separately reviewed and not merged; no new maturity, token-management API or long soak

## D11-104: internal/admin/**; internal/adminapi/**; internal/apicontract/**

Original reason/limit: generated OpenAPI, route classification, targeted admin mutation permission comparison and TLS self-lockout predicate sampled; full authorization matrix and Console UI not exercised

Current paths: internal/admin/admin_health.go, internal/admin/admin_runtime_console_full_test.go, internal/admin/admin_runtime_console_lean_test.go, internal/admin/admin_runtime_issue157_concurrency_test.go, internal/admin/admin_runtime_issue157_test.go, internal/admin/admin_runtime_remaining_coverage_test.go, internal/admin/admin_runtime_status.go, internal/admin/admin_runtime_status_coverage_test.go, internal/admin/api.go, internal/admin/api_adopt.go, internal/admin/api_adopt_test.go, internal/admin/api_client_address.go, internal/admin/api_client_address_test.go, internal/admin/api_history.go, internal/admin/api_history_diff_test.go, internal/admin/api_identity.go, internal/admin/api_identity_test.go, internal/admin/api_managed_apply.go, internal/admin/api_managed_apply_authz_test.go, internal/admin/api_managed_apply_rollback_authz_test.go, internal/admin/api_managed_apply_test.go, internal/admin/api_pending_restart.go, internal/admin/api_resilience.go, internal/admin/api_resilience_test.go, internal/admin/api_runtime_preflight_test.go, internal/admin/api_status.go, internal/admin/api_status_external_test.go, internal/admin/api_wizard.go, internal/admin/apiv1.go, internal/admin/apiv1_apply_response.go, internal/admin/apiv1_closure_additional_test.go, internal/admin/apiv1_closure_test.go, internal/admin/apiv1_config.go, internal/admin/apiv1_config_test.go, internal/admin/apiv1_contract_branches_test.go, internal/admin/apiv1_e2e_test.go, internal/admin/apiv1_edge_coverage_test.go, internal/admin/apiv1_export.go, internal/admin/apiv1_export_test.go, internal/admin/apiv1_idempotency.go, internal/admin/apiv1_idempotency_test.go, internal/admin/apiv1_listeners.go, internal/admin/apiv1_listeners_test.go, internal/admin/apiv1_operations.go, internal/admin/apiv1_operations_coverage_test.go, internal/admin/apiv1_request.go, internal/admin/apiv1_resources.go, internal/admin/apiv1_resources_test.go, internal/admin/apiv1_status.go, internal/admin/apiv1_status_test.go, internal/admin/apply_concurrency_legacy_test.go, internal/admin/apply_concurrency_test.go, internal/admin/assets/dist/assets/CodeEditor-UWWiHBzL.js, internal/admin/assets/dist/assets/CodeEditor-UWWiHBzL.js.map, internal/admin/assets/dist/assets/index-BBFvT2bh.css, internal/admin/assets/dist/assets/index-CTCwXmN5.js, internal/admin/assets/dist/assets/index-CTCwXmN5.js.map, internal/admin/assets/dist/assets/query-BdSLN-Ub.js, internal/admin/assets/dist/assets/query-BdSLN-Ub.js.map, internal/admin/assets/dist/assets/rolldown-runtime-Bh1tDfsg.js, internal/admin/assets/dist/assets/vendor-CZGZFHcR.js, internal/admin/assets/dist/assets/vendor-CZGZFHcR.js.map, internal/admin/assets/dist/favicon.png, internal/admin/assets/dist/index.html, internal/admin/audit.go, internal/admin/audit_admin_runtime.go, internal/admin/audit_sink_runtime.go, internal/admin/audit_sink_runtime_test.go, internal/admin/audit_sink_test.go, internal/admin/auth_snapshot_test.go, internal/admin/authority_gate.go, internal/admin/authority_gate_test.go, internal/admin/bounded_body.go, internal/admin/bounded_body_test.go, internal/admin/clientip_test.go, internal/admin/config_alias_test.go, internal/admin/config_apply.go, internal/admin/config_apply_timeout_audit_test.go, internal/admin/config_apply_timeout_test.go, internal/admin/config_candidate_timeout_test.go, internal/admin/console_e2e_test.go, internal/admin/console_rbac_e2e_test.go, internal/admin/console_test.go, internal/admin/console_v2.go, internal/admin/console_v2_stub.go, internal/admin/consolev1_test.go, internal/admin/consolev2_test.go, internal/admin/diff.go, internal/admin/diff_global.go, internal/admin/diff_helpers.go, internal/admin/diff_predicates_test.go, internal/admin/diff_response_policy_test.go, internal/admin/diff_route_id_test.go, internal/admin/diff_test.go, internal/admin/events.go, internal/admin/events_test.go, internal/admin/example_config_test.go, internal/admin/external_schema.go, internal/admin/finalized_audit_operation_test.go, internal/admin/health_headers_test.go, internal/admin/history.go, internal/admin/history_metadata_test.go, internal/admin/history_retention_contract_test.go, internal/admin/history_retention_hot_test.go, internal/admin/history_test.go, internal/admin/humanerrors.go, internal/admin/humanerrors_test.go, internal/admin/issue158_http_contract_test.go, internal/admin/issue158_patch_test.go, internal/admin/issue158_transition_test.go, internal/admin/issue160_acceptance_gap_test.go, internal/admin/issue160_benchmark_test.go, internal/admin/issue160_coverage_branches_test.go, internal/admin/issue160_filesystem_linux_test.go, internal/admin/issue160_leak_test.go, internal/admin/issue160_runtime_contract_test.go, internal/admin/issue160_test_cleanup_test.go, internal/admin/issue160_writer_matrix_test.go, internal/admin/issue81_route_permissions_test.go, internal/admin/issue82_server_target_test.go, internal/admin/logs.go, internal/admin/logs_test.go, internal/admin/managed_apply_advisory_test.go, internal/admin/managed_apply_deadline_test.go, internal/admin/managed_apply_fail_finalization_test.go, internal/admin/managed_apply_registry.go, internal/admin/managed_apply_registry_coverage_test.go, internal/admin/managed_apply_registry_slice1_test.go, internal/admin/managed_apply_registry_slice4_test.go, internal/admin/managed_apply_registry_test.go, internal/admin/managed_history.go, internal/admin/managed_history_test.go, internal/admin/mtls.go, internal/admin/mtls_test.go, internal/admin/normalize.go, internal/admin/normalize_test.go, internal/admin/operation_identity_test.go, internal/admin/operational.go, internal/admin/operational_status_test.go, internal/admin/operational_test.go, internal/admin/origin_guard.go, internal/admin/origin_guard_test.go, internal/admin/patch.go, internal/admin/patch_admin_runtime.go, internal/admin/patch_audit_leak_test.go, internal/admin/patch_batch.go, internal/admin/patch_batch_test.go, internal/admin/patch_builders.go, internal/admin/patch_builders_test.go, internal/admin/patch_compression_lean_test.go, internal/admin/patch_compression_test.go, internal/admin/patch_crud_test.go, internal/admin/patch_global.go, internal/admin/patch_global_stage_test.go, internal/admin/patch_global_test.go, internal/admin/patch_helpers.go, internal/admin/patch_http.go, internal/admin/patch_lifecycle.go, internal/admin/patch_ordinal_test.go, internal/admin/patch_predicates_cors_bench_test.go, internal/admin/patch_predicates_cors_test.go, internal/admin/patch_preview_immutability_test.go, internal/admin/patch_rate_limit_global_test.go, internal/admin/patch_resilience_test.go, internal/admin/patch_secrets_test.go, internal/admin/patch_test.go, internal/admin/patch_types.go, internal/admin/plugin_abi_test.go, internal/admin/plugin_identity_test.go, internal/admin/plugin_limits_test.go, internal/admin/plugin_upload.go, internal/admin/plugin_upload_coverage_test.go, internal/admin/plugin_upload_test.go, internal/admin/plugins.go, internal/admin/plugins_test.go, internal/admin/pprof_capability_test.go, internal/admin/pprof_gate_test.go, internal/admin/preflight.go, internal/admin/projection_issue81.go, internal/admin/projection_issue81_test.go, internal/admin/projection_types.go, internal/admin/projections.go, internal/admin/projections_test.go, internal/admin/ratelimit.go, internal/admin/ratelimit_test.go, internal/admin/raw_preview.go, internal/admin/raw_preview_test.go, internal/admin/rbac.go, internal/admin/rbac_diff_test.go, internal/admin/rbac_header_test.go, internal/admin/reachability.go, internal/admin/reachability_test.go, internal/admin/resilience_types.go, internal/admin/rollback_response_policy_test.go, internal/admin/rollback_version_binding_test.go, internal/admin/route_audit_identity.go, internal/admin/route_catalog.go, internal/admin/route_catalog_test.go, internal/admin/route_catalog_v1_write.go, internal/admin/route_classification.go, internal/admin/route_classification_test.go, internal/admin/route_id.go, internal/admin/routes.go, internal/admin/routetest.go, internal/admin/routetest_predicates_test.go, internal/admin/routetest_test.go, internal/admin/runtime_health_regression_test.go, internal/admin/runtime_snapshot.go, internal/admin/runtime_snapshot_coverage_test.go, internal/admin/runtime_snapshot_test.go, internal/admin/search.go, internal/admin/search_test.go, internal/admin/server.go, internal/admin/server_test.go, internal/admin/stability.go, internal/admin/stability_test.go, internal/admin/storage_targets.go, internal/admin/storage_targets_test.go, internal/admin/streams.go, internal/admin/streams_test.go, internal/admin/testmain_test.go, internal/admin/timeline.go, internal/admin/timeline_test.go, internal/admin/tls.go, internal/admin/tls_test.go, internal/admin/transcode_design.go, internal/admin/transcode_design_test.go, internal/admin/transport_gate.go, internal/admin/transport_gate_test.go, internal/admin/ui.go, internal/admin/ui/.gitignore, internal/admin/ui/.prettierrc, internal/admin/ui/e2e/global-setup.ts, internal/admin/ui/e2e/issue157-admin-runtime.spec.ts, internal/admin/ui/e2e/issue158-admin-runtime.spec.ts, internal/admin/ui/e2e/issue160-audit-sink.spec.ts, internal/admin/ui/e2e/issue160-degraded-health.spec.ts, internal/admin/ui/e2e/issue160-mixed-resources.spec.ts, internal/admin/ui/e2e/issue160-transition-event.spec.ts, internal/admin/ui/e2e/issue82-phase5.spec.ts, internal/admin/ui/e2e/real-server.spec.ts, internal/admin/ui/e2e/smoke.spec.ts, internal/admin/ui/eslint.config.ts, internal/admin/ui/index.html, internal/admin/ui/package.json, internal/admin/ui/playwright.config.ts, internal/admin/ui/pnpm-lock.yaml, internal/admin/ui/pnpm-workspace.yaml, internal/admin/ui/public/favicon.png, internal/admin/ui/src/api/client.ts, internal/admin/ui/src/app/App.tsx, internal/admin/ui/src/app/AuthGate.tsx, internal/admin/ui/src/app/CommandPalette.tsx, internal/admin/ui/src/app/Layout.tsx, internal/admin/ui/src/app/commandPaletteBus.ts, internal/admin/ui/src/app/providers.tsx, internal/admin/ui/src/auth/PermissionProvider.tsx, internal/admin/ui/src/auth/usePermission.ts, internal/admin/ui/src/components/ChartDetailPanel.tsx, internal/admin/ui/src/components/ConfirmDialog.tsx, internal/admin/ui/src/components/Drawer.tsx, internal/admin/ui/src/components/ForbiddenAction.tsx, internal/admin/ui/src/components/PanelError.tsx, internal/admin/ui/src/components/Sparkline.tsx, internal/admin/ui/src/components/ui.tsx, internal/admin/ui/src/features/apps/AppDetail.tsx, internal/admin/ui/src/features/apps/AppEditor.tsx, internal/admin/ui/src/features/apps/AppSettingsEditor.tsx, internal/admin/ui/src/features/apps/AppsPanel.tsx, internal/admin/ui/src/features/apps/HashSettings.tsx, internal/admin/ui/src/features/config/ApplyOutcomeBanner.tsx, internal/admin/ui/src/features/config/CodeEditor.tsx, internal/admin/ui/src/features/config/ConfigPanel.tsx, internal/admin/ui/src/features/config/DiffView.tsx, internal/admin/ui/src/features/config/GlobalSettingsEditor.tsx, internal/admin/ui/src/features/config/configMutationMachine.ts, internal/admin/ui/src/features/config/useConfigMutationMachine.ts, internal/admin/ui/src/features/history/HistoryPanel.tsx, internal/admin/ui/src/features/observability/ConsoleHealthBadge.tsx, internal/admin/ui/src/features/observability/LogTailPanel.tsx, internal/admin/ui/src/features/observability/ObservabilityPanel.tsx, internal/admin/ui/src/features/observability/TimelinePanel.tsx, internal/admin/ui/src/features/operations/OperationsPanel.tsx, internal/admin/ui/src/features/overview/DiagnosticsGuidanceSection.tsx, internal/admin/ui/src/features/overview/OverviewPanel.tsx, internal/admin/ui/src/features/overview/StorageSection.tsx, internal/admin/ui/src/features/plugins/AdminRuntimeSettingsDrawer.test.tsx, internal/admin/ui/src/features/plugins/AdminRuntimeSettingsDrawer.tsx, internal/admin/ui/src/features/plugins/AttachPluginDrawer.tsx, internal/admin/ui/src/features/plugins/PluginCard.test.tsx, internal/admin/ui/src/features/plugins/PluginCard.tsx, internal/admin/ui/src/features/plugins/PluginEditorDrawer.tsx, internal/admin/ui/src/features/plugins/PluginsPanel.tsx, internal/admin/ui/src/features/plugins/UploadPluginDrawer.tsx, internal/admin/ui/src/features/routes/AuthEditor.tsx, internal/admin/ui/src/features/routes/CORSEditor.tsx, internal/admin/ui/src/features/routes/LocationRateLimitEditor.tsx, internal/admin/ui/src/features/routes/PredicatesEditor.tsx, internal/admin/ui/src/features/routes/ResponseHeadersEditor.tsx, internal/admin/ui/src/features/routes/RouteDetail.tsx, internal/admin/ui/src/features/routes/RouteEditor.tsx, internal/admin/ui/src/features/routes/RouteEditors.tsx, internal/admin/ui/src/features/routes/RouteTester.tsx, internal/admin/ui/src/features/routes/RoutesPanel.tsx, internal/admin/ui/src/features/routes/TranscodeQuickEdit.tsx, internal/admin/ui/src/features/search/SearchPanel.tsx, internal/admin/ui/src/features/security/AuditPanel.tsx, internal/admin/ui/src/features/security/ClientAddressEditor.tsx, internal/admin/ui/src/features/security/LocationWAFEditor.tsx, internal/admin/ui/src/features/security/SecretHelper.tsx, internal/admin/ui/src/features/security/SecurityPanel.tsx, internal/admin/ui/src/features/security/WAFEditor.tsx, internal/admin/ui/src/features/security/WAFEffectiveCard.tsx, internal/admin/ui/src/features/streams/StreamsPanel.tsx, internal/admin/ui/src/features/tls/MTLSEditor.tsx, internal/admin/ui/src/features/tls/TLSEditor.tsx, internal/admin/ui/src/features/tls/TLSPanel.tsx, internal/admin/ui/src/features/traffic-controls/AccessLogEditor.tsx, internal/admin/ui/src/features/traffic-controls/TracingEditor.tsx, internal/admin/ui/src/features/traffic-controls/TrafficControlEditor.tsx, internal/admin/ui/src/features/traffic-controls/TrafficControlsPanel.tsx, internal/admin/ui/src/features/traffic-controls/TrafficFormFields.tsx, internal/admin/ui/src/features/transcode/TranscodeDesignerPanel.tsx, internal/admin/ui/src/features/wizard/WizardPanel.tsx, internal/admin/ui/src/lib/accessLogToml.ts, internal/admin/ui/src/lib/appHash.ts, internal/admin/ui/src/lib/appPatch.ts, internal/admin/ui/src/lib/appSettings.ts, internal/admin/ui/src/lib/applyOutcome.ts, internal/admin/ui/src/lib/computeMetricSummary.ts, internal/admin/ui/src/lib/configActionPresentation.ts, internal/admin/ui/src/lib/configDraftHandoff.ts, internal/admin/ui/src/lib/configHandoffGuard.ts, internal/admin/ui/src/lib/configInvalidation.ts, internal/admin/ui/src/lib/corsDraft.ts, internal/admin/ui/src/lib/diagnostics.ts, internal/admin/ui/src/lib/errorReporter.ts, internal/admin/ui/src/lib/featureRoutes.ts, internal/admin/ui/src/lib/finalizationAdvisory.ts, internal/admin/ui/src/lib/formatBytes.ts, internal/admin/ui/src/lib/metricMeta.ts, internal/admin/ui/src/lib/mtls.ts, internal/admin/ui/src/lib/patchPreviewAction.ts, internal/admin/ui/src/lib/plugins.ts, internal/admin/ui/src/lib/predicatesDraft.ts, internal/admin/ui/src/lib/responseHeadersDraft.ts, internal/admin/ui/src/lib/routeEdit.ts, internal/admin/ui/src/lib/routePatch.ts, internal/admin/ui/src/lib/routeToml.ts, internal/admin/ui/src/lib/secretsRef.ts, internal/admin/ui/src/lib/storage.ts, internal/admin/ui/src/lib/streams.ts, internal/admin/ui/src/lib/theme.ts, internal/admin/ui/src/lib/tlsToml.ts, internal/admin/ui/src/lib/tracingToml.ts, internal/admin/ui/src/lib/trafficPatchBuilders.ts, internal/admin/ui/src/lib/trafficToml.ts, internal/admin/ui/src/lib/transcodeToml.ts, internal/admin/ui/src/lib/useDebouncedValue.ts, internal/admin/ui/src/lib/useFocusTrap.ts, internal/admin/ui/src/lib/useManagedApplyRecord.ts, internal/admin/ui/src/lib/useMetricsHistory.ts, internal/admin/ui/src/lib/usePersistentState.ts, internal/admin/ui/src/lib/useRunPatch.ts, internal/admin/ui/src/lib/useRunPatchBatch.ts, internal/admin/ui/src/lib/wafEffective.ts, internal/admin/ui/src/lib/wafOverride.ts, internal/admin/ui/src/lib/wafToml.ts, internal/admin/ui/src/main.tsx, internal/admin/ui/src/styles/globals.css, internal/admin/ui/src/test/access-log-editor.test.tsx, internal/admin/ui/src/test/access-log-toml.test.ts, internal/admin/ui/src/test/api-errors.test.ts, internal/admin/ui/src/test/app-detail.test.tsx, internal/admin/ui/src/test/app-editor.test.tsx, internal/admin/ui/src/test/app-hash.test.tsx, internal/admin/ui/src/test/app-panel-state.test.tsx, internal/admin/ui/src/test/app-patch.test.ts, internal/admin/ui/src/test/app-permissions.test.tsx, internal/admin/ui/src/test/app-settings-editor.test.tsx, internal/admin/ui/src/test/app-settings.test.ts, internal/admin/ui/src/test/apply-outcome.test.tsx, internal/admin/ui/src/test/auth-editor.test.tsx, internal/admin/ui/src/test/auth-gate.test.tsx, internal/admin/ui/src/test/client-address-editor.test.tsx, internal/admin/ui/src/test/client-role-contracts.test.ts, internal/admin/ui/src/test/client-write.test.ts, internal/admin/ui/src/test/code-editor.test.tsx, internal/admin/ui/src/test/command-palette.test.tsx, internal/admin/ui/src/test/computeMetricSummary.test.ts, internal/admin/ui/src/test/config-draft-handoff.test.ts, internal/admin/ui/src/test/config-mutation-machine.test.ts, internal/admin/ui/src/test/console-v2-write.test.tsx, internal/admin/ui/src/test/consolev2.test.tsx, internal/admin/ui/src/test/cors-editor.test.tsx, internal/admin/ui/src/test/diagnostics-guidance.test.tsx, internal/admin/ui/src/test/finalization-advisory.test.ts, internal/admin/ui/src/test/focus-trap.test.tsx, internal/admin/ui/src/test/issue80-patch-contract.test.ts, internal/admin/ui/src/test/issue81-config-action.test.ts, internal/admin/ui/src/test/issue81-handoff-storage.test.ts, internal/admin/ui/src/test/issue81-invalidation.test.ts, internal/admin/ui/src/test/issue81-traffic-patches.test.ts, internal/admin/ui/src/test/issue82-accessibility.test.tsx, internal/admin/ui/src/test/issue82-compatibility.test.tsx, internal/admin/ui/src/test/issue82-handoff-race.test.ts, internal/admin/ui/src/test/issue82-server-target.test.ts, internal/admin/ui/src/test/location-waf-editor.test.tsx, internal/admin/ui/src/test/log-tail.test.tsx, internal/admin/ui/src/test/managed-apply-record.test.tsx, internal/admin/ui/src/test/mtls.test.tsx, internal/admin/ui/src/test/operations.test.tsx, internal/admin/ui/src/test/panel-error.test.tsx, internal/admin/ui/src/test/patch-config.test.ts, internal/admin/ui/src/test/patch-preview-action.test.ts, internal/admin/ui/src/test/permission.test.tsx, internal/admin/ui/src/test/plugins.test.tsx, internal/admin/ui/src/test/predicates-editor.test.tsx, internal/admin/ui/src/test/resource-capacity.test.tsx, internal/admin/ui/src/test/response-headers-editor.test.tsx, internal/admin/ui/src/test/route-auth-toml.test.ts, internal/admin/ui/src/test/route-deletion.test.tsx, internal/admin/ui/src/test/route-edit.test.ts, internal/admin/ui/src/test/route-editor.test.tsx, internal/admin/ui/src/test/route-editors.test.tsx, internal/admin/ui/src/test/route-id-round-trip.test.ts, internal/admin/ui/src/test/route-order-and-a11y.test.tsx, internal/admin/ui/src/test/route-patch.test.ts, internal/admin/ui/src/test/secrets-ref.test.ts, internal/admin/ui/src/test/security.test.tsx, internal/admin/ui/src/test/setup.ts, internal/admin/ui/src/test/storage-headroom.test.tsx, internal/admin/ui/src/test/streams.test.tsx, internal/admin/ui/src/test/timeline-tooltip.test.tsx, internal/admin/ui/src/test/tls-toml.test.ts, internal/admin/ui/src/test/tracing-editor.test.tsx, internal/admin/ui/src/test/tracing-toml.test.ts, internal/admin/ui/src/test/traffic-toml.test.ts, internal/admin/ui/src/test/ui-loading.test.tsx, internal/admin/ui/src/test/use-config-mutation-machine.test.tsx, internal/admin/ui/src/test/use-run-patch-batch.test.tsx, internal/admin/ui/src/test/waf-effective.test.tsx, internal/admin/ui/src/test/waf-override.test.ts, internal/admin/ui/src/test/waf-toml.test.ts, internal/admin/ui/src/vite-env.d.ts, internal/admin/ui/tsconfig.json, internal/admin/ui/tsconfig.node.json, internal/admin/ui/vite.config.ts, internal/admin/ui/vitest.config.ts, internal/admin/unix_backend_projection_test.go, internal/admin/upstream_hash.go, internal/admin/upstream_hash_test.go, internal/admin/waf_effective.go, internal/admin/waf_effective_test.go, internal/admin/wave1_security_test.go, internal/admin/wizard_app_test.go, internal/adminapi/api_test.go, internal/adminapi/dto.go, internal/adminapi/errors.go, internal/adminapi/errors_test.go, internal/adminapi/exit_codes.go, internal/adminapi/exit_codes_test.go, internal/adminapi/outcome.go, internal/adminapi/outcome_test.go, internal/adminapi/v1.go, internal/adminapi/v1_config.go, internal/adminapi/v1_export.go, internal/adminapi/v1_listeners.go, internal/adminapi/v1_mutation.go, internal/adminapi/v1_resources.go, internal/adminapi/v1_test.go, internal/apicontract/apicontractgen/main.go, internal/apicontract/apicontractgen/main_test.go, internal/apicontract/build.go, internal/apicontract/build_internal_test.go, internal/apicontract/contract_test.go, internal/apicontract/openapi.go, internal/apicontract/render.go, internal/apicontract/schema.go, internal/apicontract/schema_test.go. Owner: Single-agent D11 implementation reviewer; maintainer/assigned reviewer owns independent acceptance.

Verdict: **defect filed/linked**; [establishing PR](https://github.com/victornife/jul/pull/604).

Reviewed claims:
- Entire original admin/adminapi/apicontract wildcard retained: current/internal-vs-external routes, transport/auth/permissions, DTO/error/OpenAPI contracts and mutation/history/idempotency owner boundaries
- #517 config/history/client coverage proof is path-scoped and source-bound; server authorization remains authoritative
- #595 known-grant confirmation defect and #602 CSV/auth-failure gaps have retained reproducible corrections in separate PR597/603; integration still pending

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
- Runtime/Console corrections597/600/603 are separately reviewed and not merged; no new maturity, token-management API or long soak

Selected finding owners: https://github.com/victornife/jul/issues/595, https://github.com/victornife/jul/issues/602.

## D11-109: scripts/* (other operational and test scripts)

Original reason/limit: inventory aggregate excluding individually listed scripts; comments and operational side effects need script-by-script review

Current paths: scripts/audit-closure-check.sh, scripts/bench-compare.sh, scripts/bench.sh, scripts/build-site.py, scripts/burn-in-backend.go, scripts/burn-in-load.go, scripts/burn-in-load.ps1, scripts/burn-in-stream-load.go, scripts/burn-in-wasm.go, scripts/check-auto04-coverage.py, scripts/check-full-tags-sync.py, scripts/check-license-headers.sh, scripts/check-package-coverage.py, scripts/config-check.sh, scripts/conformance/run.py, scripts/conformance/test_run.py, scripts/deployment_lab.py, scripts/docker-network-journey.py, scripts/fault-backend.go, scripts/fault-dnsd.go, scripts/fault-evidence.sh, scripts/fault-load.go, scripts/fix_config.py, scripts/fix_test_config.py, scripts/fuzz.sh, scripts/gen-certs.go, scripts/grpc-echo-server.go, scripts/grpc-load.go, scripts/install-hooks.ps1, scripts/install-hooks.sh, scripts/macos-package-first-run.py, scripts/nginx-corpus-check.sh, scripts/nginx-corpus-report.go, scripts/nginx-migration-e2e.sh, scripts/pre-commit-hook.sh, scripts/prepare-deployment-package.py, scripts/reconcile-deployment-packages.py, scripts/security-gates.sh, scripts/security-package-coverage.json, scripts/soak-manifest-init.sh, scripts/soak-repro-smoke.sh, scripts/soak-scrape.go, scripts/soak.sh, scripts/stream-echo.go, scripts/strip_moved_lines.py, scripts/test-discovery-consul-live.ps1, scripts/test-discovery-k8s-live.sh, scripts/test-discovery-live.ps1, scripts/test-site.cjs, scripts/test_build_site.py, scripts/test_check_package_coverage.py, scripts/test_deployment_lab.py, scripts/test_semantic_drift.py, scripts/test_strip_moved_lines.py, scripts/verify-release-packages.py, scripts/windows-deployment-mime.py, scripts/write_test_config.py. Owner: Single-agent D11 implementation reviewer; maintainer/assigned reviewer owns independent acceptance.

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
- This is full wildcard consumer/contract reconciliation, not every-line code/security audit or native execution of every script. Selected corrective PRs are drafts, not integrated passes.
- Evidence is source/build/platform scoped; documentation and numeric checks do not certify the whole product or advance feature maturity.

Selected finding owners: https://github.com/victornife/jul/issues/594, https://github.com/victornife/jul/issues/598, https://github.com/victornife/jul/issues/605.

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

Current paths: cmd/jul/capabilities.go, cmd/jul/capabilities_test.go, cmd/jul/cli_test.go, cmd/jul/completion.go, cmd/jul/corpus_affinity_runtime_test.go, cmd/jul/corpus_fastcgi_uwsgi_runtime_test.go, cmd/jul/corpus_grpc_runtime_test.go, cmd/jul/corpus_idioms_runtime_test.go, cmd/jul/corpus_runtime_test.go, cmd/jul/corpus_stream_runtime_test.go, cmd/jul/diagnostics_cli.go, cmd/jul/diagnostics_cli_hardening_test.go, cmd/jul/diagnostics_cli_test.go, cmd/jul/healthcheck_test.go, cmd/jul/import.go, cmd/jul/import_assessment_test.go, cmd/jul/import_assessment_v2_test.go, cmd/jul/import_corpus_test.go, cmd/jul/import_helpers_test.go, cmd/jul/import_include_test.go, cmd/jul/import_stub.go, cmd/jul/import_stub_test.go, cmd/jul/import_test.go, cmd/jul/remote_cli_more_test.go, cmd/jul/remote_cli_test.go, cmd/jul/remote_process_e2e_test.go, cmd/jul/remote_usage.go, cmd/jul/run_smoke_test.go, cmd/jul/service_other.go, cmd/jul/service_windows.go, cmd/jul/service_windows_test.go, cmd/jul/testdata/import-nginx-golden.toml, cmd/jul/version.go, cmd/jul/version_test.go. Owner: Single-agent D11 implementation reviewer; maintainer/assigned reviewer owns independent acceptance.

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
- Runtime/Console corrections597/600/603 are separately reviewed and not merged; no new maturity, token-management API or long soak

## Supplemental verification

Check new deployment/runbook rows D11-N001/N002 against #516 original identities and limits, and N003 historical evidence without relabeling native/archive reconciliation as fresh execution. N004 Console and N005–N007 vision/practical/soak consumers remain supplemental. These checks do not replace12 original IDs.

## Actual review record

- [ ] Actual reviewer / maintainer assignment identified.
- [ ] Reviewed source SHA pinned.
- [ ] At least12 actual distinct original IDs inspected.
- [ ] Result and corrections recorded in companion JSON.
- [ ] Final exact-head checks passed and integration recorded.

None of these boxes is completed by preparing this document. #515 remains open until the actual review and final completion work are accepted/merged.
