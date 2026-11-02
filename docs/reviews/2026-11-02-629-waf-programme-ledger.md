# WAF and Security-Operations Programme Acceptance & Evidence Ledger (`#629`, `#630`–`#634`, `#628-B`)

- **Date:** 2026-11-02
- **Programme Epic:** [#629](https://github.com/victornife/jul/issues/629) (`[EPIC][SEC-OPS]`) under master tracker [#62](https://github.com/victornife/jul/issues/62)
- **Child & Selected Issues:** [#632](https://github.com/victornife/jul/issues/632) (`WAF-OPS-03` Milestones A & B), [#630](https://github.com/victornife/jul/issues/630) (`WAF-OPS-01`), [#631](https://github.com/victornife/jul/issues/631) (`WAF-OPS-02`), [#633](https://github.com/victornife/jul/issues/633) (`SEC-POSTURE`), [#634](https://github.com/victornife/jul/issues/634) (`SEC-ISOLATION`), and [#628](https://github.com/victornife/jul/issues/628) Milestone B + `#632` rejection-alert extension (`OPS-ALERT-RECIPES`; `#628` Milestone A remains separately owned)
- **Governing Design:** [ADR 0031](../adr/0031-waf-operations-posture-isolation-and-alerting.md)

---

## 1. Pre-Programme Baseline Identities

| Property | Recorded Value |
| --- | --- |
| **Pre-programme `main` commit SHA** | `70277d9886131b3e88927b150eb43af97ad7c312` |
| **Pre-programme `main` git tree** | `cd460d27b90391b52b27f11910063294e60db9d2` |
| **Working tree state** | Clean (`0` modified/untracked files) |
| **Go toolchain & platform** | `go1.27.3 linux/arm64` (`gcc (Ubuntu 15.2.0-16ubuntu1) 15.2.0` for `CGO_ENABLED=1 -race`) |
| **Build profiles** | `lean` (no build tags) and `full` (`brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf`) |
| **Linked WAF dependencies (`go.mod`)** | `github.com/corazawaf/coraza/v3 v3.8.1`, `github.com/corazawaf/coraza-coreruleset/v4 v4.25.0` (embedded OWASP CRS `4.25.0`, `@coraza.conf-recommended`, `@crs-setup.conf.example`), `github.com/corazawaf/libinjection-go v0.3.3` |
| **Frontend / docs / container tools** | Node `v26.5.0`, `pnpm 11.8.0`, Python `3.14.4`, `golangci-lint 2.14.0`, `govulncheck v1.9.0`, Docker `29.6.2`, Playwright Chromium `1228` |
| **Baseline `main` CI status** | `62/62` push checks green on `70277d98` ([run `41349035322`](https://github.com/victornife/jul/actions/runs/41349035322)) |
| **Baseline `make security-gates`** | PASS — `internal/plugins` `90.8%` (`1124/1238`, floor `69.0%`), `internal/rbac` `89.2%` (`174/195`, floor `75.0%`), `internal/waf` `84.4%` (`178/211`, floor `72.0%`), admin catalog/transport guards PASS |
| **Baseline `CI=true make console-check`** | PASS — `126` production files, `10` required targets; statements `81.91%` (`5871/7167`), branches `76.77%` (`6152/8013`), functions `73.26%` (`1485/2027`), lines `83.31%` (`5307/6370`) |
| **Baseline `make generated-check` & `docs-check.py`** | PASS — `0` drift errors; `2,584` doc checks passed, `0` failed; D11 certification `0` errors |

---

## 2. Predeclared Resource and Performance Budgets

Declared before runtime implementation and comparison runs:

| Metric / Workload | Measured Baseline (`70277d98`, `linux/arm64`) | Predeclared Acceptance Budget |
| --- | --- | --- |
| **Disabled WAF request path** | `0` WAF wrapper allocations (`Middleware` not composed when `EffectiveWAF` is false) | `0` added allocations; median latency within `<= 2%` measurement noise |
| **`BenchmarkWAF_NoRules` (5-sample median)** | `26,771 ns/op`, `3,737 B/op`, `69 allocs/op` | Median latency `<= +10%` (`<= 29,448 ns/op`), `<= +12 allocs/op`, `<= +1,024 B/op` |
| **`BenchmarkWAF_CRSBlock_Pass` (5-sample median)** | `525,810 ns/op`, `98,057 B/op`, `2,108 allocs/op` | Median latency `<= +10%` (`<= 578,391 ns/op`), `<= +16 allocs/op`, `<= +2,048 B/op` |
| **`BenchmarkWAF_CRSDetect_Pass` (5-sample median)** | `523,342 ns/op`, `97,999 B/op`, `2,108 allocs/op` | Median latency `<= +10%` (`<= 575,676 ns/op`), `<= +16 allocs/op`, `<= +2,048 B/op` |
| **`BenchmarkWAF_CRSBlock_Block` (5-sample median)** | `802,745 ns/op`, `148,864 B/op`, `2,953 allocs/op` | Median latency `<= +10%` (`<= 883,020 ns/op`), `<= +24 allocs/op`, `<= +4,096 B/op` (includes `wev_` ID + investigation ring entry) |
| **Investigation event ring memory** | N/A (new in `#632-A`) | Max `256` retained events, max `4,096 B` encoded per event, `<= 1 MiB` total retained memory, `15m` TTL, non-blocking under noisy load |
| **Policy compile & fixture evaluation (`#630`/`#631`)** | Single CRS compile `~110 ms` | CRS + up to `64` structured exceptions compile `<= 250 ms`; `20`-case synthetic fixture pack evaluation `<= 2.0 s` |
| **Retained generations & reload churn** | `TestWAFReloadChurnNoLeak` PASS (`30` cycles) | Flat goroutines (`<= +4` after warm round) and bounded heap across sustained noisy traffic + reload/rollback churn |
| **Console initial-route bundle & coverage** | `<= 256,000 B` gzip; global statements `81.91%` (floor `58%`) | Initial-route gzip `<= 256,000 B` (lazy-load heavy security drawers/tabs); all `#517` exact-file and aggregate floors preserved |
| **Changed-code coverage gates** | `>= 90.0%` move-aware Go; `>= 90.0%` Codecov PATCH | `>= 90.0%` move-aware changed Go statements and `>= 90.0%` Codecov PATCH on every production-changing PR and cumulatively against `70277d98` |

---

## 3. Existing Owner Harnesses Reused (No Suite Duplication)

| Domain | Existing Owner Tests / Harnesses Reused | Added Composition Scope in `#629`–`#634` |
| --- | --- | --- |
| **WAF compilation, provenance & privacy (`#440`, `#127`, `#649`)** | `internal/waf/{firewall,policy,fs,logging_privacy,clientaddr,reload_churn}_test.go`, `internal/app/waf_report_waf_test.go`, `internal/admin/waf_effective_test.go` | Transaction-local outcomes, `nolog` interruptions, response-phase partial delivery, narrow `ctl:` exceptions, separate paranoia levels, candidate evaluation & recovery |
| **Standalone application sharing & withdrawal (`#580`, `#641`, `#642`, `#581`)** | `internal/admin/sharing_test.go`, `internal/config/validate_sharing_test.go`, `scripts/sharing_f1_lab.py`, [`docs/sharing/f1-standalone-evidence.md`](../sharing/f1-standalone-evidence.md) | Intent-aware posture findings (`#633`) and cross-route public/restricted/admin isolation with WAF active (`#634`) |
| **Response cache invalidation, inspection, route policy & cutover (`#620`–`#624`, `#666`–`#668`, `#672`)** | `internal/cache/{scoped_purge,invalidation_regression,authorization,requestid,protocol,conformance}_test.go`, `internal/admin/apiv1_cache*_test.go`, `internal/app/cache_cutover_epic620_test.go` | WAF + cache warm/cold/revalidation/tightening boundaries and non-replay of `X-Jul-WAF-Event` support IDs (`#634`, `#632`) |
| **Doctor, diagnostics & RBAC (`#156`, `#129`, `#514`)** | `internal/doctor/{doctor,hardening,coverage}_test.go`, `internal/diagnostics/diagnostics_test.go`, `internal/rbac/*_test.go`, `internal/admin/route_catalog_test.go` | Unified 7-area gateway security posture (`#633`) and permission-gated investigation/posture endpoints |
| **Cross-protocol admission & H3 mTLS (`#409`, `#513`, `#642`)** | `internal/handler/cross_protocol_test.go`, `internal/server/http3_client_auth_test.go`, `internal/stream/*_test.go` | Protocol applicability boundaries for HTTP WAF vs. WebSocket/SSE/native gRPC/transcoded gRPC/L4 in `docs/security-isolation-matrix.json` (`#634`) |

---

## 4. Stacked PR Plan and Execution Sequence

| Stage / PR | Branch | Base Branch | Primary Scope | Status |
| --- | --- | --- | --- | --- |
| **PR 1** | `feat/629-634-baseline-and-shared-design` | `main` (`70277d98`) | [ADR 0031](../adr/0031-waf-operations-posture-isolation-and-alerting.md), this acceptance ledger, `#634` initial security-isolation manifest (`docs/security-isolation-matrix.json`) and baseline test suite (`internal/app/security_isolation_matrix_test.go`), `#62`/`#629`/`#628` scope reconciliation | `implemented/unverified` |
| **PR 2** | `feat/632a-waf-trusted-outcomes` | `feat/629-634-baseline-and-shared-design` | `#632-A`: transaction-local WAF outcome capture, phase/origin/partial-response tracking, `X-Jul-WAF-Event` (`wev_...`), bounded investigation ring, `jul_waf_transactions_total` & `jul_waf_investigation_events_total` | `not started` |
| **PR 3** | `feat/630-waf-evaluation-and-recovery` | `feat/632a-waf-trusted-outcomes` | `#630`: compiled policy manifest (`waf-compiled-policy-v1`), versioned synthetic fixture pack (`examples/waf-policy/`), `jul waf evaluate`/`manifest`/`verify-assets`, `/api/security/waf/evaluate`, staleness verification, `jul_waf_serving_routes` & `jul_waf_protection_changes_total`, exact asset recovery | `verified` |
| **PR 4** | `feat/631-waf-tuning-exceptions-paranoia` | `feat/630-waf-evaluation-and-recovery` | `#631`: `blocking_paranoia` & `detection_paranoia`, structured `[[waf.exceptions]]` compiled to pre-CRS `phase:1` `ctl:` rules (`890001..890064`), rule-observer existence checks, `review_at` metadata, typed patches/diffs, Console narrow-tuning workflow, generated contracts | `not started` |
| **PR 5** | `feat/632b-633-investigation-and-posture` | `feat/631-waf-tuning-exceptions-paranoia` | `#632-B` (`/api/v1/waf/events`, `jul waf events`, Console `WAFInvestigationPanel` linked to `#631` tuning) + `#633` (7-area posture evaluator, `/api/v1/security/posture`, `jul doctor`/`posture`, `jul_security_posture_findings` & `jul_security_evidence_state`, Console Posture view) | `not started` |
| **PR 6** | `feat/628b-634-629-alerting-isolation-completion` | `feat/632b-633-investigation-and-posture` | `#628-B` + `#632` rejection-alert recipe (`examples/alerting/security/`, `promtool` tests, disposable Jul→Prometheus→Alertmanager→webhook E2E), `#634` complete matrix, `#629` integrated end-to-end operator journey & cumulative verification | `not started` |

---

## 5. Per-Criterion Acceptance & Evidence Matrix

States: `not started` | `implemented/unverified` | `verified` | `unavailable` | `blocked`.

### 5.1 `#632` — Actual WAF Outcomes & Bounded Investigation (`WAF-OPS-03`)

| Criterion ID | Requirement Summary | Owner Surface | Named Test(s) | Docs | Status |
| --- | --- | --- | --- | --- | --- |
| `632-A1` | Characterize Coraza connector/tx APIs; transaction-local integration with no global last-match state or per-request engine build | `internal/waf/outcomes.go`, `internal/waf/firewall.go` | `TestWAFTransactionOutcomesTable`, `TestWAFConcurrentTransactionsAndCanaryPrivacy` | `docs/waf.md`, `docs/adr/0031-waf-operations-posture-isolation-and-alerting.md` | `verified` |
| `632-A2` | Finite actual-outcome contract (`clean`, `matched_observed`, `interrupted_request`, `interrupted_response`, `rejected_body_limit`, `inspection_error`); separate client vs. origin status and partial response delivery | `internal/waf/outcomes.go`, `internal/observability/metrics.go` | `TestWAFRequestVsResponseInterruptionAndBodyErrors`, `TestWAFTransactionOutcomesTable` | `docs/waf.md`, `docs/observability.md`, `docs/metrics-contract.json` | `verified` |
| `632-A3` | Separate logged match events (`jul_waf_events_total`) from final transaction decision (`jul_waf_transactions_total`); cover non-disruptive block matches, detect-only, and `nolog` interruptions | `internal/waf/outcomes.go`, `internal/observability/metrics.go` | `TestWAFTransactionOutcomesTable` | `docs/waf.md`, `docs/metrics-contract.json` | `verified` |
| `632-A4` | Server-owned `event_id` (`wev_...`) and `X-Jul-WAF-Event` on WAF rejections; route/generation/policy attribution; client `X-Request-ID`/`trace_id` kept as untrusted hints; never replayed from cache | `internal/waf/investigation.go`, `internal/cache/http.go` | `TestWAFConcurrentTransactionsAndCanaryPrivacy`, `TestSecurityIsolationCacheAuthAndWAFBoundaries` | `docs/waf.md`, `docs/cache.md` | `verified` |
| `632-A5` | Bounded match aggregation (`<=16`/tx), event size (`<=4KiB`), ring capacity (`256`), TTL (`15m`), drop/truncation counters (`jul_waf_investigation_events_total`), and fail-safe enforcement when disabled/full | `internal/waf/investigation.go` | `TestInvestigationStoreBoundsFiltersAndTTL` | `docs/waf.md`, `docs/observability.md` | `verified` |
| `632-B1` | Versioned API (`GET /api/v1/waf/events`), CLI (`jul waf events`), and Console `WAFInvestigationPanel` with filters, permissions, and links to serving policy/history/narrow tuning | `internal/admin/apiv1_waf.go`, `cmd/jul/waf_cli.go`, `internal/admin/ui/src/features/security/WAFInvestigationPanel.tsx` | `TestAPIV1WAFEventsPermissionsAndFilters`, `waf-investigation.test.tsx` | `docs/admin-api.md`, `docs/console.md`, `docs/waf.md` | `not started` |
| `632-B2` | Strict metadata privacy: canary secrets in query, body, cookies, path tokens, and macro-expanded messages never leak into events, responses, logs, metrics, or exports | `internal/waf/logging_privacy_test.go`, `internal/admin/apiv1_waf_test.go` | `TestWAFInvestigationCanaryPrivacyAllSinks` | `docs/waf.md`, `docs/security-testing.md` | `not started` |

### 5.2 `#630` — Policy Evaluation, Promotion & Reproducible Recovery (`WAF-OPS-01`)

| Criterion ID | Requirement Summary | Owner Surface | Named Test(s) | Docs | Status |
| --- | --- | --- | --- | --- | --- |
| `630-1` | Compiled policy manifest and context-keyed `policy_digest` (`waf-compiled-policy-v1`) vs. private raw digests (`config:raw` / local CLI only, preserving `#649`) | `internal/waf/policy.go`, `internal/waf/manifest.go`, `internal/admin/waf_effective.go` | `TestWAFPolicyManifestAndVerifyAssetsAfterSamePathEdit`, `TestBuildWAFEffective_Confidentiality` | `docs/waf.md`, `docs/effective-versions.md` | `verified` |
| `630-2` | Versioned synthetic regression fixture pack (`examples/waf-policy/fixtures.json`) covering normal forms, JSON, multipart uploads, size/encoding boundaries, and malicious SQLi/XSS/RCE/LFI controls | `internal/waf/eval.go`, `examples/waf-policy/fixtures.json` | `TestWAFFixturePackBaselineAndCandidateComparison` | `docs/waf.md`, `examples/waf-policy/README.md` | `verified` |
| `630-3` | Reproducible candidate evaluation (`jul waf evaluate`, `POST /api/security/waf/evaluate`) comparing baseline vs. candidate verdicts, rules, origin effects, and resource costs; re-verified for same-path file changes | `internal/waf/eval.go`, `cmd/jul/waf_cli.go`, `internal/admin/waf_effective.go`, `internal/app/waf_report.go` | `TestWAFFixturePackBaselineAndCandidateComparison`, `TestWAFCLIEvaluateManifestAndVerifyAssets`, `TestWAFEffectiveEvaluateAndStalenessOnDisk` | `docs/waf.md`, `docs/admin-api.md`, `docs/remote-cli.md` | `verified` |
| `630-4` | Promotion via existing preview/diff/apply/stage/history machinery; failed compile/Prepare leaves prior generation serving; `jul_waf_serving_routes` & `jul_waf_protection_changes_total` signals for `#628-B` | `internal/app/waf_report.go`, `internal/observability/metrics.go`, `internal/admin/ui/src/features/security/WAFEffectiveCard.tsx` | `TestWAFEffectiveEvaluateAndStalenessOnDisk`, `waf-effective.test.tsx` | `docs/waf.md`, `docs/observability.md`, `docs/metrics-contract.json` | `verified` |
| `630-5` | Exact external-asset recovery across same-path file modification and restart (`jul waf manifest`, `jul waf verify-assets`, rollback asset-mismatch detection, `#584` coordination) | `internal/waf/manifest.go`, `cmd/jul/waf_cli.go` | `TestWAFPolicyManifestAndVerifyAssetsAfterSamePathEdit`, `TestWAFCLIEvaluateManifestAndVerifyAssets` | `docs/waf.md`, `docs/reload-semantics.md` | `verified` |

### 5.3 `#631` — Narrow WAF Tuning, Reviewable Exceptions & Separate Paranoia (`WAF-OPS-02`)

| Criterion ID | Requirement Summary | Owner Surface | Named Test(s) | Docs | Status |
| --- | --- | --- | --- | --- | --- |
| `631-1` | Structured narrow exceptions (`[[waf.exceptions]]`, `[[servers.locations.waf.exceptions]]`) with rule ID, bounded target (`ARGS:field`, `REQUEST_HEADERS:hdr`, `REQUEST_COOKIES:ck`), method constraints, and scope | `internal/config/schema.go`, `internal/config/validate.go`, `internal/waf/exceptions.go` | `TestValidateWAFExceptionsAndTargets`, `TestWAFExceptionNarrowFieldPreservesNeighboringProtection` | `docs/waf.md`, `docs/configuration.md` | `not started` |
| `631-2` | Pre-CRS `phase:1` `ctl:ruleRemoveTargetById` / `ctl:ruleRemoveById` compilation (`890001..890064`), phase-1/phase-2/chain-child correctness in Coraza `v3.8.1`, and rule-observer check for missing/removed rule IDs | `internal/waf/firewall.go`, `internal/waf/exceptions.go` | `TestWAFExceptionPhase1Phase2AndChainChildren`, `TestWAFExceptionMissingOrCollidingRuleIDRejected` | `docs/waf.md`, `docs/adr/0031-waf-operations-posture-isolation-and-alerting.md` | `not started` |
| `631-3` | Bounded exception metadata (`id`, `rationale`, `owner`, `reference`, `created_at`, `review_at`, `fixtures`); advisory `review_at` warning (`waf_exception_review_due`) without surprise runtime expiry | `internal/config/lint.go`, `internal/waf/policy.go`, `internal/admin/waf_effective.go` | `TestWAFExceptionReviewDueMetadataAndLint` | `docs/waf.md`, `docs/diagnostics.md` | `not started` |
| `631-4` | Separate `blocking_paranoia` and `detection_paranoia` (`1..4`, `detection >= blocking`), preserving legacy `paranoia` semantics and rejecting ambiguous conflicts | `internal/config/schema.go`, `internal/config/validate.go`, `internal/waf/firewall.go` | `TestWAFSeparateBlockingAndDetectionParanoia` | `docs/waf.md`, `docs/configuration.md` | `not started` |
| `631-5` | Typed patch operations, semantic diff, generated contracts (`lifecycle`, `configcontract`, `openapi`), and Console guided narrow-tuning workflow (`WAFEditor`, `LocationWAFEditor`) preserving advanced fields | `internal/admin/patch*.go`, `internal/admin/diff*.go`, `internal/admin/ui/src/features/security/{WAFEditor,LocationWAFEditor}.tsx` | `TestWAFExceptionPatchAndDiff`, `waf-toml.test.ts`, `location-waf-editor.test.tsx` | `docs/console.md`, `docs/generated/config-reference.md` | `not started` |

### 5.4 `#633` — Actionable Gateway Posture & Exposure Review (`SEC-POSTURE`)

| Criterion ID | Requirement Summary | Owner Surface | Named Test(s) | Docs | Status |
| --- | --- | --- | --- | --- | --- |
| `633-1` | Complete 7-area finite posture catalog (`admin`, `trust`, `waf`, `cache`, `certificates`, `intent`, `maintenance`) reusing `config.Lint`, `doctor`, `WAFEffective`, and `SharingView` | `internal/admin/posture.go`, `internal/doctor/checks.go` | `TestSecurityPostureCatalogAllAreas` | `docs/security-posture.md`, `docs/diagnostics.md` | `not started` |
| `633-2` | Distinguish deliberate `public` sharing intent from `restricted` mismatches, `external_unknown` enforcers/secrets, `unanalysed_custom` SecLang/plugins, and candidate-vs-serving divergence | `internal/admin/posture.go` | `TestSecurityPostureIntentAndEpistemicStates` | `docs/security-posture.md`, `docs/specs/application-sharing.md` | `not started` |
| `633-3` | Read-only network-free API (`GET /api/v1/security/posture`), CLI (`jul posture` / `jul doctor`), Console Posture view, and bounded metrics (`jul_security_posture_findings`, `jul_security_evidence_state`) | `internal/admin/apiv1_posture.go`, `cmd/jul/waf_cli.go`, `internal/admin/ui/src/features/security/SecurityPanel.tsx` | `TestAPIV1SecurityPosturePermissionsAndPrivacy`, `security-posture.test.tsx` | `docs/admin-api.md`, `docs/console.md`, `docs/observability.md` | `not started` |

### 5.5 `#634` — Versioned Cross-Route & Protocol Security Regression Matrix (`SEC-ISOLATION`)

| Criterion ID | Requirement Summary | Owner Surface | Named Test(s) | Docs | Status |
| --- | --- | --- | --- | --- | --- |
| `634-1` | Versioned manifest (`docs/security-isolation-matrix.json`) and validator covering public/restricted/admin route isolation, Host/SNI/encoded-path/rewrite/predicate edges, and forged forwarding headers | `docs/security-isolation-matrix.json`, `internal/app/security_isolation_matrix.go`, `internal/app/security_isolation_matrix_test.go` | `TestSecurityIsolationMatrixManifestValid`, `TestSecurityIsolationPublicRestrictedAdminAndIdentity` | `docs/security-testing.md` | `implemented/unverified` |
| `634-2` | Cache + auth + WAF isolation (warm/cold GET/HEAD, Vary, Range, policy tightening across retain/invalidate/reload/restart, non-replayed `X-Request-ID` and `X-Jul-WAF-Event`) | `internal/app/security_isolation_matrix_test.go` | `TestSecurityIsolationCacheAuthAndWAFBoundaries` | `docs/security-testing.md`, `docs/cache.md` | `implemented/unverified` |
| `634-3` | Lifecycle & protocol applicability boundaries (failed Prepare/reload, concurrent traffic, H1/H2/compiled H3, WebSocket, SSE, native gRPC, transcoded gRPC, L4 stream passthrough) | `internal/app/security_isolation_matrix_test.go`, `internal/server/http3_client_auth_test.go` | `TestSecurityIsolationLifecycleAndProtocolBoundaries` | `docs/security-testing.md`, `docs/known-limitations.md` | `implemented/unverified` |

### 5.6 `#628-B` + `#632` Alert Extension & `#629` Integrated Journey

| Criterion ID | Requirement Summary | Owner Surface | Named Test(s) | Docs | Status |
| --- | --- | --- | --- | --- | --- |
| `628B-1` | Pinned Prometheus + Alertmanager security recipe (`examples/alerting/security/`) covering protection weakening (`#630`), material posture finding & unavailable evidence (`#633`), and WAF rejection symptom (`#632`) | `examples/alerting/security/{prometheus.yml,alertmanager.yml,security-alerts.rules.yml,security-alerts.test.yml}` | `TestSecurityAlertingPromtoolRules`, `TestSecurityAlertingEndToEndLocalReceiver` | `docs/observability.md`, `examples/alerting/security/README.md` | `not started` |
| `629-INT` | Reproducible end-to-end operator journey: baseline upload rejected -> external alert fires to local receiver -> investigate `wev_` outcome & policy -> narrow `#631` exception -> `#630` fixture evaluation -> preview/activate -> verify attack still blocked & upload allowed -> recover exact external assets after same-path change & restart -> alert resolves | `internal/app/waf_programme_epic629_test.go` | `TestWAFProgrammeIntegratedOperatorJourneyEpic629` | `docs/waf.md`, `docs/reviews/2026-11-02-629-waf-programme-ledger.md` | `not started` |

---

## 6. Adversarial Review & Finding Ledger

Populated across stage reviews and the final whole-programme review:

| Finding ID | Stage / PR | Reviewer | Severity | Location & Reproduction | Disposition & Fix SHA | Regression Test |
| --- | --- | --- | --- | --- | --- | --- |
| *(Recorded as each stage review completes)* | — | — | — | — | — | — |
