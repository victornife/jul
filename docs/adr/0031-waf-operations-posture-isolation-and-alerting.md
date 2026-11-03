# ADR 0031 — WAF operations, trustworthy transaction outcomes, narrow tuning, posture review, isolation matrix, and external security alerting

- **Status:** Accepted — in progress (`#629`, `#630`, `#631`, `#632`, `#633`, `#634`, `#628` Milestone B)
- **Date:** 2026-11-02
- **Deciders:** Victor Niharra (`@victornife`)
- **Applies to:** `internal/waf`, `internal/config`, `internal/lifecycle`, `internal/configcontract`, `internal/apicontract`, `internal/observability`, `internal/doctor`, `internal/diagnostics`, `internal/admin`, `internal/adminapi`, `internal/adminclient`, `internal/app`, `cmd/jul`, Console v2 (`internal/admin/ui`), `examples/waf-policy/`, and `examples/alerting/security/`
- **Source:** [#629](https://github.com/victornife/jul/issues/629), [#630](https://github.com/victornife/jul/issues/630), [#631](https://github.com/victornife/jul/issues/631), [#632](https://github.com/victornife/jul/issues/632), [#633](https://github.com/victornife/jul/issues/633), [#634](https://github.com/victornife/jul/issues/634), [#628](https://github.com/victornife/jul/issues/628), [#62](https://github.com/victornife/jul/issues/62)
- **Related:** [ADR 0010](0010-console-rbac.md), [ADR 0011](0011-reload-plan.md), [ADR 0013](0013-project-operating-model-and-completeness.md), [ADR 0014](0014-operability-surfaces.md), [ADR 0015](0015-managed-apply-terminal-ledger.md), [ADR 0016](0016-inbound-identity-and-backend-peer-trust.md), [ADR 0018](0018-bounded-route-matching-and-response-policy.md), [ADR 0019](0019-configuration-authority-generated-contracts-and-resource-identity.md), [ADR 0025](0025-effective-version-confidentiality.md), [ADR 0026](0026-application-sharing-contract.md), [ADR 0028](0028-bounded-cache-inspection.md), [ADR 0029](0029-route-scoped-cache-policy-and-vary-guards.md), [ADR 0030](0030-cache-purge-recovery-latency-and-cutover.md)

## Context

Jul.IA ships a Coraza (`v3.8.1`) and embedded OWASP Core Rule Set (`v4.25.0`) WAF behind the `waf` build tag, with serving provenance (`#440`), path-only redacted match logging (`#127`), confidential external-source digests (`#649`, [ADR 0025](0025-effective-version-confidentiality.md)), and standalone application-sharing intent and withdrawal contracts (`#580`, `#641`, `#642`, `#581`, [ADR 0026](0026-application-sharing-contract.md)).

Six coordinated operational-security gaps remain across `#629`:

1. **Logged matches vs. actual transaction outcomes (`#632`):**
   - `errorCallback` fires only for matched rules with `log` enabled and labels `jul_waf_events_total` with the configured policy `mode` (`block` or `detect`).
   - In `block` mode, a non-disruptive rule match (such as an individual CRS anomaly rule below the blocking threshold or a higher detection-paranoia rule) increments `jul_waf_events_total{action="block"}` even though the request is allowed to the origin.
   - Conversely, a disruptive `nolog` rule or a `SecRequestBodyLimitAction Reject` (`413`) interrupts the transaction without invoking `errorCallback`.
   - Furthermore, upstream `coraza/v3/http.WrapHandler` silently returns from `ServeHTTP` without writing a status code (defaulting to `200 OK` with an empty body) if `ReadRequestBodyFrom` or `RequestBodyReader` returns an I/O error without a Coraza interruption, and ignores non-`deny` interruption actions (`drop`).
2. **Candidate policy evaluation, promotion, and external-asset recovery (`#630`):**
   - Operators updating WAF rules, paranoia levels, or exceptions need a reproducible way to evaluate a candidate policy against synthetic legitimate and malicious fixtures before activation, verify that the candidate being applied matches the exact bytes evaluated, and recover exact external SecLang/dataset files after same-path edits and restarts (`#584`).
3. **Narrow structured tuning and separate detection/blocking paranoia (`#631`):**
   - `[waf].paranoia` sets `tx.blocking_paranoia_level` and `tx.detection_paranoia_level` to the same value, preventing operators from observing higher-paranoia rules without blocking on them.
   - Appending exclusions in `inline_rules` (which runs *after* `Include @owasp_crs/*.conf`) cannot safely express request-time `ctl:ruleRemoveTargetById` / `ctl:ruleRemoveById` exclusions for phase-1 rules or method-constrained routes, and Coraza `v3.8.1`'s `SecRuleUpdateTargetById` only mutates parent rule variables rather than chain children (`rule.Chain`).
   - Additionally, in Coraza `v3.8.1`'s `parseCtl` / `GetField`, an empty target key (`ARGS:` or `ARGS`) sets `KeyStr == ""`, which matches *every* key in the collection and silently removes protection from all fields.
4. **Actionable gateway posture and exposure review (`#633`):**
   - Existing checks in `config.Lint`, `jul doctor`, `WAFEffective`, and `SharingView` are spread across separate surfaces. Operators need one read-only, network-free posture review across administration, trust, WAF, cache, certificates/credentials, declared sharing intent, and candidate-vs-serving maintenance state that distinguishes verified weaknesses from intentional public exposure and external unknowns.
5. **Versioned cross-route and protocol security-isolation matrix (`#634`):**
   - Unit tests and individual feature suites do not by themselves prove that public static routes, restricted backends, and private administration stay isolated across host/path/rewrite/predicate edges, forged forwarding headers, warm/cold cache transitions, reloads, and H1/H2/H3/WebSocket/SSE/gRPC/L4 boundaries.
6. **Verified external security alerting (`#628` Milestone B + `#632` rejection-alert extension):**
   - Operators need an optional, version-pinned Prometheus + Alertmanager recipe that fires and resolves notifications into a local receiver without keeping the Console open, covering serving protection changes (`#630`), material posture findings and lost/stale evidence (`#633`), and configurable WAF rejection symptoms (`#632`), while keeping `#628` Milestone A's general operational alerts separately tracked.

## Decision

### 1. Transaction-Local WAF Connector and Outcome Contract (`#632-A`)

#### 1.1 Alternatives compared

| Alternative | Mechanism | Why selected or rejected |
| --- | --- | --- |
| **Option A — Transaction-local HTTP connector over `coraza.WAF` / `types.Transaction`** | Own the small HTTP request/response lifecycle directly in `internal/waf` using supported `types.Transaction` methods, per-transaction state, and response interception. | **Selected:** gives exact control over phase progression, request vs. response interruption, origin invocation tracking, partial-response delivery detection, body-read error status (`400`/`413` instead of silent `200`), and per-transaction match aggregation without global mutable state or engine forks. |
| **Option B — Correlate `WithErrorCallback` via global mutable last-match state** | Store matched rules in a firewall-global slice/map keyed by URI or goroutine state around `corazahttp.WrapHandler`. | **Rejected:** races under concurrent traffic, misses `nolog` interruptions and body-limit rejections, and cannot observe whether the origin ran or partially wrote a response. |
| **Option C — Compile a fresh `coraza.WAF` per request** | Construct a new engine per transaction to attach a per-request error callback. | **Rejected:** compiling CRS per request costs tens of milliseconds and megabytes of heap, violating hot-path resource budgets. |

#### 1.2 Transaction outcomes and invariants

Every request reaching an enabled WAF middleware executes within a transaction-owned `txState` attached to the transaction context (`experimental.WAFWithOptions`):

1. **Preserved logged-match counter:** `jul_waf_events_total{action, rule}` retains its exact v1/v2 meaning: it increments once per matched rule with `log` enabled in `errorCallback`, labeled by configured policy `mode` (`block` or `detect`) and `rule` ID.
2. **Closed transaction outcome enum (`WAFOutcome`):** Every transaction that enters the WAF middleware increments `jul_waf_transactions_total{mode, outcome}` exactly once upon completion with one of six mutually exclusive outcomes:
   - `clean`: WAF inspected the transaction; zero logged or disruptive rules matched, no body/inspection error occurred, and the request was not interrupted by WAF.
   - `matched_observed`: One or more rules matched (either in `detect` mode, or non-disruptive/below-threshold/detection-paranoia rules in `block` mode), no WAF interruption was enforced, and the transaction proceeded to the inner handler/origin. `would_interrupt` records whether a disruptive action would have triggered in `block` mode (`DetectionOnlyInterruption() != nil` or disruptive rule match).
   - `interrupted_request`: WAF interrupted the transaction during request processing (phase 1 request headers or phase 2 request body, including `nolog` rules) **before** the inner handler/origin was called (`origin_reached = false`).
   - `interrupted_response`: The inner handler/origin was called (`origin_reached = true`), and WAF interrupted during response processing (phase 3 response headers or phase 4 response body). `partial_response_delivered` records whether response bytes had already been flushed downstream before the interruption.
   - `rejected_body_limit`: The request body exceeded `request_body_limit` (`413 Content Too Large`, `origin_reached = false`) or the response body exceeded `SecResponseBodyLimit` under `Reject` (`500 Internal Server Error`, `origin_reached = true`).
   - `inspection_error`: Request or response body reading/buffering failed with an I/O or parser error before or during inspection. If a request body read fails before origin execution without a prior rule interruption, the WAF middleware fails closed with `400 Bad Request` (or `413 Content Too Large` for `http.MaxBytesError`) rather than silently returning `200 OK`.
3. **Separate client and origin observations:**
   - Earlier middleware rejections (`withdrawn`, `client_cert`, `auth`, `rate_limit`) return before the WAF middleware and never produce WAF transactions.
   - An origin `403` that passes through WAF without a WAF interruption is recorded with `origin_reached = true`, `origin_status = 403`, `client_status = 403`, and `outcome = "clean"` (or `"matched_observed"`), never `interrupted_request` or `interrupted_response`.
   - Non-`101` informational `1xx` responses (such as `103 Early Hints`) strip any origin-supplied `X-Jul-WAF-Event` header and forward immediately without latching final response status or triggering phase-3 response-header inspection; phase-3 inspection runs once against the actual final response status. `101 Switching Protocols` preserves `http.Hijacker` upgrade passthrough.
   - Under `SecResponseBodyLimitAction ProcessPartial`, once response-body buffering crosses `SecResponseBodyLimit` without an interruption or inspection error and releases the buffered prefix downstream, subsequent `Flush()` calls are permitted immediately so streaming responses continue without blocking on handler completion.

### 2. Trusted Event Identity vs. Client Correlation Hints (`#632-A/B`)

1. **Server-owned event ID (`event_id`):** Whenever a WAF transaction records an investigation event (any non-`clean` outcome: `matched_observed`, `interrupted_request`, `interrupted_response`, `rejected_body_limit`, `inspection_error`), Jul mints a server-owned opaque identifier `wev_<32-hex-chars>` derived from a per-boot random seed and monotonic sequence.
2. **Rejection support header (`X-Jul-WAF-Event`):** When WAF interrupts a transaction (`interrupted_request`, `rejected_body_limit`, or `interrupted_response` before response headers are flushed), Jul sets `X-Jul-WAF-Event: <event_id>` on the HTTP response. The header is opaque and discloses no rule ID, policy digest, or route configuration. The response cache never stores WAF-interrupted responses and strips `X-Jul-WAF-Event` on admission so cached responses can never replay a WAF support reference.
3. **Untrusted hints:** Client-supplied or entry-generated `X-Request-ID` (`request_id_hint`) and OpenTelemetry `trace_id` (`trace_id_hint`) are stored strictly as untrusted correlation hints. They are never used as primary event identities or Prometheus labels.

### 3. Policy Manifest, Confidential Digests, and Candidate Evaluation (`#630`, `#649`)

1. **Compiled policy identity (`policy_digest` vs. private raw digests):**
   - A compiled WAF policy's canonical identity input frames: linked Coraza version, embedded CRS version, normalized `WAFConfig` scalars (`mode`, `block_status`, `crs_enabled`, `blocking_paranoia`, `detection_paranoia`, `request_body_limit`, `response_body_check`), canonical structured `exceptions`, exact read-once bytes of external files/includes/operator datasets recorded by `sourceRecorder`, and `inline_rules`.
   - Public/status surfaces (`GET /api/security`, `GET /api/v1/status`, `/api/v1/waf/events`, `/api/v1/security/posture`) expose **only** the context-keyed `ev1:<ctx>:<tag>` HMAC-SHA-256 digest under the server's durable `versionid.Context` (`domain = "waf-compiled-policy-v1"` for full policy identity and `"waf-external-source"` for external files, `#649`).
   - Unkeyed SHA-256 digests (`raw_policy_sha256`, per-file `sha256` entries in `WAFPolicyManifest`) are restricted to local CLI invocation (`jul waf manifest`, `jul waf evaluate`, `jul waf verify-assets`) and admin endpoints gated by `config:raw`.
   - CLI WAF and posture commands (`jul waf evaluate`, `jul waf manifest`, `jul waf verify-assets`, `jul posture`) resolve `${env:...}` and `${file:...}` references through `config.NewCandidateContext` (preserving raw and effective configuration domains) and load the target node's durable `versionid.Context` from `-version-key-file` / `JUL_EFFECTIVE_VERSION_KEY_FILE` / `versionid.DefaultPath()` when readable so separate-process CLI evaluation reports bind to the intended server's `ev1:<ctx>:<tag>` policy identity after `POST /api/security/waf/evaluate` strips private raw digests; genuinely offline or cross-node runs fall back to an ephemeral process context whose reports remain explicitly `stale_policy_changed` if imported onto a different node.
2. **Offline/disposable-fixture evaluation (`jul waf evaluate` and `POST /api/security/waf/evaluate`):**
   - We select **in-process real Jul handler/WAF evaluation against a versioned synthetic fixture pack** (`examples/waf-policy/fixtures.json`) rather than live traffic mirroring, remote URL scanning, or external `go-ftw` container dependencies.
   - `POST /api/security/waf/evaluate` is gated by `AnyPermissions: {rbac.ConfigWrite, rbac.ConfigApply}`. Supplied and generated reports are validated for internal consistency (non-negative in-range totals, unique `case_id`s, valid `legitimate`/`malicious`/`boundary` categories, and exact agreement between per-case outcomes and aggregate `passed_cases`/`failed_cases`/`all_passed`/`legitimate_blocked`/`malicious_allowed` counts). Supplied reports have unkeyed `raw_policy_sha256` and `raw_external_sha256` stripped on admission so evaluation binding uses context-keyed `ev1:` digests only (`#649`).
   - Each fixture case declares: `id`, `description`, `category` (`legitimate`, `malicious`, or `boundary`), request `method` (validated as a legal RFC 9110 HTTP token), `host`, `path` (validated with `http.NewRequestWithContext` so malformed inputs return structured errors without panicking), `headers`, synthetic `body` (or multipart/form/JSON specification), optional simulated `origin_status` (validated in `200..599` when non-zero so malformed status codes never panic `ResponseRecorder.WriteHeader`)/headers/body (for response-phase cases), and expected `outcome`, `status`, `origin_reached`, and expected matched/excluded rule IDs.
   - Evaluation compiles the baseline (optional) and candidate policies once from their exact in-memory recorded bytes (closing every compiled engine upon completion), runs every applicable fixture case through the real Jul WAF middleware and synthetic origin counter, and emits a bounded `WAFEvaluationReport` (`schema_version: 1`) containing per-case results, baseline-vs-candidate regressions (e.g., legitimate case newly blocked or malicious control newly allowed), compile/eval duration, and the bound policy identity.
   - Both live server evaluations and imported CLI reports resolve their target scope (`"global"`, `route_id`, unambiguous path/coordinates, or single-override default) through one authoritative serving-route resolver over `(liveConfig, liveWAF)`, so an inheriting route's CLI report resolves to its serving global policy on import just as on live evaluation. Completion is fenced against the serving generation and active scope identity: if a concurrent reload disables, removes, or changes the target scope while evaluation or import is in flight, Jul never substitutes another scope's summary or records a report for a retired scope, and generation publication prunes any retired scope keys from `lastWAFEvalByScope`. Every `POST /api/security/waf/evaluate` response is rebound against the target scope's serving `policy_digest`, `external_digest`, and `diskAssetsMatchRecorded` state so a same-path on-disk rule modification without reload immediately reports `stale_asset_changed` rather than `current`. On `/metrics` scrapes, Jul checks the compiled policy identity and on-disk external asset bytes across both global and per-location override policies; if any external file changed on disk at the same path since evaluation or compilation, the evaluation state transitions to `stale_asset_changed` and `jul_security_evidence_state{producer="waf_effective",state="stale"}` is set to `1` without requiring any Console or admin API read. Evaluation is advisory guidance, not a mandatory gate that blocks emergency `config:apply`.
3. **Reproducible external-asset recovery (`#630`, `#584`):**
   - Because configuration history (`history:rollback`) stores and restores the managed TOML configuration snapshot rather than external SecLang/dataset file contents on disk, operators using `directives_files` or external datasets pair immutable versioned asset paths with a local asset manifest (`jul waf manifest -config server.toml -out manifest.json` and `jul waf verify-assets -config server.toml -manifest manifest.json`).
   - When an external rule file is modified in place on disk without updating its path or reloading, runtime `CheckExternalAssetsStale` and `jul waf verify-assets` flag the byte/digest mismatch (`stale_asset_changed` in `GET /api/security` and `/api/v1/security/posture`), preventing a TOML-only rollback or stale reload from silently assuming external rule files match the evaluated baseline.

### 4. Narrow Tuning, Reviewable Exceptions, and Separate Paranoia Levels (`#631`)

#### 4.1 Separate blocking and detection paranoia levels

1. `WAFConfig` adds optional `blocking_paranoia` (`1..4`) and `detection_paranoia` (`1..4`), both classified `hot_reload` (`SubWAF`).
2. **Precedence and validation:**
   - When `blocking_paranoia == 0` and `paranoia > 0`, effective blocking paranoia is `paranoia` (preserving 100% backward compatibility with existing configs).
   - Setting both `paranoia > 0` and `blocking_paranoia > 0` to *different* values fails validation as ambiguous.
   - When both `paranoia == 0` and `blocking_paranoia == 0`, effective blocking paranoia defaults to `1` when `crs_enabled = true`.
   - When `detection_paranoia == 0`, effective detection paranoia defaults to effective blocking paranoia.
   - When `detection_paranoia > 0`, validation requires `detection_paranoia >= EffectiveBlockingParanoia()` and `crs_enabled = true`.
3. **SecLang emission:** When `crs_enabled = true` and any paranoia field is set (or `blocking_paranoia > 1` or `detection_paranoia > 1`), Jul emits:
   ```seclang
   SecAction "id:900000,phase:1,nolog,pass,t:none,setvar:tx.blocking_paranoia_level=<B>,setvar:tx.detection_paranoia_level=<D>"
   ```
   Under CRS `v4.25.0`, rules up to `<D>` execute and log (`matched_observed`), while only rules up to `<B>` contribute to `tx.blocking_inbound_anomaly_score` and `tx.blocking_outbound_anomaly_score`.

#### 4.2 Structured narrow exceptions (`WAFException`)

1. **Schema (`[[waf.exceptions]]` and `[[servers.locations.waf.exceptions]]`):**
   - `id` (`string`, optional, `1..64` ASCII `[a-zA-Z0-9._-]` chars, unique within the `WAFConfig` policy).
   - `rule_id` (`int`, required, `1..99999999`, excluding reserved Jul generated IDs `890000..900001`).
   - `scope` (`string`, optional, default `"narrow_target"`; allowed `"narrow_target"` for named field/variable exclusion or `"rule_remove"` for explicit whole-rule exclusion on the selected route/methods; an omitted `scope` always defaults to `"narrow_target"` so an empty `target` fails validation rather than broadening to whole-rule removal).
   - `target` (`string`, required when `scope == "narrow_target"`, forbidden when `scope == "rule_remove"`): must match `<COLLECTION>:<key>` where `<COLLECTION>` is in the closed allowlist `{ARGS, ARGS_GET, ARGS_POST, ARGS_NAMES, REQUEST_HEADERS, REQUEST_COOKIES}` and `<key>` is `1..64` safe literal token bytes (`[a-zA-Z0-9_.\-\[\]]`, never empty, containing no whitespace, quotes, `,`, `;`, `/`, or `\`).
   - `methods` (`[]string`, optional, non-duplicate canonical HTTP methods from `{GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS}`).
   - `rationale` (`string`, optional, `<= 256` printable UTF-8 bytes, no control characters).
   - `owner` (`string`, optional, `<= 128` printable UTF-8 bytes, no control characters).
   - `reference` (`string`, optional, `<= 128` printable UTF-8 bytes, no control characters).
   - `created_at` (`string`, optional `YYYY-MM-DD`).
   - `review_at` (`string`, optional `YYYY-MM-DD`): advisory review-due date. A past `review_at` produces a posture/lint warning (`waf_exception_review_due`) and Console badge; it **never** causes automatic runtime expiry or surprise re-blocking.
   - `fixtures` (`[]string`, optional, up to 16 fixture IDs associated with this exception).
2. **Pre-CRS `phase:1` compilation and chain/phase correctness:**
   - Each structured exception at index `i` (`0 <= i < 64`) compiles to a deterministic `phase:1` rule with ID `890001 + i` emitted **before** `Include @owasp_crs/*.conf`, `directives_files`, and `inline_rules`.
   - When `methods` is empty, it emits `SecAction "id:<gen_id>,phase:1,nolog,pass,t:none,<ctl_action>"`.
   - When `len(methods) == 1`, it emits `SecRule REQUEST_METHOD "@streq <METHOD>" "id:<gen_id>,phase:1,nolog,pass,t:none,<ctl_action>"`.
   - When `len(methods) > 1`, it emits `SecRule REQUEST_METHOD "@rx ^(?:<M1>|<M2>|...)$" "id:<gen_id>,phase:1,nolog,pass,t:none,<ctl_action>"` (anchored exact regex match; `@pm` is never used for HTTP methods because Coraza `v3.8.1`'s `@pm` performs case-insensitive substring matching that would falsely match e.g. `XPOST`).
   - For `scope == "narrow_target"`, `<ctl_action>` is `ctl:ruleRemoveTargetById=<rule_id>;<COLLECTION>:<key>`. For `scope == "rule_remove"`, `<ctl_action>` is `ctl:ruleRemoveById=<rule_id>`.
   - Because Coraza `v3.8.1` evaluates `phase:1` rules in syntactic order and looks up `tx.ruleRemoveTargetByID[rid]` via `r.ParentID_` on chain children (`internal/corazawaf/rule.go:225`), this placement works across phase-1 rules, phase-2 rules, and chained rules.
   - During `waf.New`, after `coraza.NewWAF` compiles all embedded CRS, external directive files, and `inline_rules` in syntactic order (executing Coraza's own `SecRuleRemoveById`, `SecRuleRemoveByTag`, and `SecRuleRemoveByMsg` mutations, including backslash line continuations, arbitrary ID ranges, and sequential remove-before-add vs. remove-after-add order), an isolated compatibility adapter (`compiledRuleIDs` in `internal/waf/firewall.go`) inspects the surviving rules in Coraza's compiled `RuleGroup` (`w.waf.Rules.rules`). If Coraza's internal struct layout does not match the expected fields (`waf`, `Rules`, `rules`, `ID_`), `compiledRuleIDs` fails closed with an explicit `"waf: incompatible coraza.WAF internal layout"` compatibility error distinct from a missing `rule_id`; any Coraza version upgrade must execute `TestWAFExceptionMissingOrCollidingRuleIDRejected` (and should migrate to an upstream public rule-inspection API when Coraza provides one). If an exception's `rule_id` is absent from the compiled engine's final rule set, `waf.New` closes the candidate engine and returns a descriptive compile error before `Publish` rather than silently installing a dead exception.

### 5. Bounded Investigation Buffer and API/CLI/Console Workflow (`#632-B`)

1. **In-memory ring buffer bounds:**
   - Capacity: `256` recent non-`clean` WAF transaction events per process (`MaxInvestigationEvents = 256`), max `16` matched rule entries per event (`MaxMatchedRulesPerEvent = 16`), max `4 KiB` serialized size per event (`MaxEventBytes = 4096`), and a `15m` TTL (`InvestigationEventTTL = 15 * time.Minute`).
   - All retained request-derived strings (`path`, `method`, `host`, `request_id_hint`, `trace_id_hint`, route coordinates, and rule metadata) are explicitly cloned (`strings.Clone`) after truncation, sanitization, and redaction so substrings sliced from large request targets never pin the underlying request buffer in the ring.
   - Noisy-traffic backpressure: bounded ring append; if collection is disabled, events are not retained and the status is reported explicitly. Overflow overwrites the oldest slot and increments `jul_waf_investigation_events_total{result="dropped"}`; per-event match truncation sets `matches_truncated = true` and increments `jul_waf_investigation_events_total{result="truncated"}`.
2. **Privacy invariants:**
   - Events retain **only**: `event_id` (`wev_...`), `time`, `outcome`, `would_interrupt`, `mode`, `client_status`, `origin_reached`, `origin_status`, `partial_response_delivered`, `phases_evaluated`, `hijacked`, `method`, sanitized/redacted `path` (`observability.SanitizePath` + `redact.Apply`), `query_omitted`, `host` (canonical configured server name or `_unmatched`), route coordinates and non-disclosing predicate identity (`route_id`, `listen`, `server_names`, `match_type`, `path_pattern`, `match_ordinal`, `scope`, `methods`, value-free `predicates` display summary, and context-keyed `canonical_predicates` HMAC-SHA-256 digest under domain `"waf-route-predicates-v1"` so secret-backed header/query predicate values are never exposed), serving `generation`, context-keyed `policy_digest`, `crs_version`, `paranoia`, `stale_policy` (comparing `ev.PolicyDigest` against the serving policy for that event's exact `route` and `scope`), bounded matched rules (`rule_id`, `phase`, `severity`, `source_class`, `disruptive`, safe `tags`, and safe `matched_targets` containing only `<COLLECTION>:<safe_token_key>` with **no values, no raw URI, and no macro-expanded message/logdata**), and untrusted hints (`request_id_hint`, `trace_id_hint`).
3. **Surfaces and permissions:**
   - `GET /api/v1/waf/events` (external versioned API) and `jul waf events` CLI, gated by `rbac.ObservabilityRead`, supporting filters by `event_id`, `request_id`, `trace_id`, `route_id`, `path`, `outcome`, `rule_id`, and `limit`.
   - Full Console `WAFInvestigationDrawer` in the Security view: filters recent events, explains the difference between `interrupted_request`, `interrupted_response`, `matched_observed`, and origin/auth `403`s, and offers a **"Tune `<target>` →"** prefill into the `#631` per-location exception editor (`LocationWAFEditor`). Both `LocationWAFEditor` and the global `WAFEditor` capture an immutable editing snapshot (`target` selector, `selector_version: "global-v1"`, `base_version`, and initial draft/TOML) at open time that stays frozen across 15-second background polling refreshes until the operator explicitly reloads or reopens the editor, preventing background query updates from renewing an old draft's CAS fence or silently retargeting reordered same-path predicate routes. Initial investigation handoff resolves the serving event's route against the authoritative editable route inventory (`waf_routes` projected on `/api/security` with `base_version`, `match_ordinal`, `methods`, `predicates`, and `canonical_predicates`, plus `waf_effective.serving_base_version`) before creating the immutable draft snapshot, rejecting missing, reordered, reassigned, or ambiguous targets rather than trusting a stale ordinal. When the investigated route currently inherits the global `[waf]` policy (`scope == "global"`), default tuning constructs a new route-local override seeded with the global policy's effective and advanced fields so sibling routes inheriting `[waf]` are never weakened by default; broader global `[waf]` tuning is an explicit secondary action (`"Tune globally"`).

### 6. Actionable Gateway Posture and Exposure Review (`#633`)

1. **Single server-owned evaluator (`internal/admin/apiv1_waf_posture.go`):**
   - Evaluates the live serving configuration (`LiveSnapshot().EffectiveConfig` when running, while comparing the persisted raw configuration against `LiveSnapshot().RawConfig` and carrying `rawCfg` explicitly alongside `effCfg` through `config.Candidate` and `HandlerFactory` generation publication—without any global lookup ring—so healthy `${env:...}` / `${file:...}` secret expansion never produces false literal-secret or divergence warnings) across all seven finite catalog areas:
     1. `admin`: off-loopback bind, TLS/client-auth, legacy shared token vs. RBAC, audit log sink health.
     2. `trust`: broad `trusted_proxies` (`0.0.0.0/0`, `::/0`), `backend_tls.insecure_skip_verify`, discovery TLS verification, outbound `[egress]` allow-list.
     3. `waf`: lean binary capability gap, unprotected routes, `detect`-only mode, `request_body_limit < client_max_body_size`, WAF on native gRPC, unanalysed manual SecLang tuning (`has_custom_tuning_directives`), `review_at` review-due exceptions (`#631`), and fixture evaluation staleness/failures.
     4. `cache`: authenticated routes with shared caching (`cache_authenticated_route_review` / `cache_authenticated_route_cookie_bypass_configured`).
     5. `certificates`: local leaf certificate expiry (`not_after`), validity (`not_before`), and SAN coverage alongside literal-secret checks.
     6. `intent`: declared sharing intent across all four canonical modes (`public`, `restricted`, `preview` on non-loopback listeners, and `private_network` on broad/wildcard listeners mapped from `config.LintWithRaw`) vs. actual Jul checks, unverified `external_enforcer` (`external_unknown`), and `withdrawn` routes.
     7. `maintenance`: candidate/persisted vs. serving generation divergence (`maintenance_external_drift_detected`, `maintenance_staged_restart_pending`, `maintenance_serving_differs_from_persisted`).
2. **Honest epistemic states:**
   - Every finding and area summary carries `epistemic_state` (`observed`, `configured`, `inferred`, `external_unknown`, `unanalysed_custom`, `unavailable`), `serving_generation`, `policy_digest`, and actionable `remediation`.
   - Deliberately `public` routes (`sharing.mode = "public"`) without auth are reported as `sharing_public_intentional` (`info`/`configured`), never as vulnerabilities.
3. **Surfaces:**
   - `GET /api/v1/security/posture` (`rbac.StatusRead`, with `rbac.ConfigRead` required for detailed route/scope strings), `jul posture` CLI, Console `SecurityPostureDrawer`, and bounded Prometheus gauges `jul_security_posture_findings{area, severity}` (published on reloads and `/api/v1/security/posture` evaluations using the generation's explicit `rawCfg` + `effCfg`) and `jul_security_evidence_state{producer, state}` (refreshed on reloads, posture evaluations, audit-sink health transitions, and `/metrics` scrapes via the pre-gather hook across `waf_effective`, `waf_investigation`, and `audit_sink`).

### 7. Cross-Route/Protocol Isolation Matrix (`#634`) and External Security Alerting (`#628-B`)

1. **Versioned security-isolation matrix (`#634`):**
   - Declared in `docs/security-isolation-matrix.json` and executed by `internal/app/security_isolation_matrix_test.go` (plus protocol-specific owners in `internal/server`, `internal/handler`, and `internal/stream`).
   - Exercises one Jul instance with an intentional-public static service, a restricted HTTP backend, and private administration across:
     - Public/private route isolation (Host/SNI/authority, encoded/normalized paths, rewrites, fallback, predicates).
     - Trusted identity (forged `X-Forwarded-For`/`Forwarded`/client-cert headers from untrusted peers vs. trusted proxy, direct-origin/admin attempts).
     - WAF + Cache composition (warm hits, misses, `304`/SWR revalidation, `GET`/`HEAD`, `Vary`, Range, request/response WAF inspection, policy tightening across retain/invalidate/reload/restart, and proof that `X-Jul-WAF-Event` / `X-Request-ID` are never replayed from cache).
     - Policy lifecycle (failed `Prepare`/reload, tightening/exception activation, rollback/restart under concurrent traffic).
     - Protocol applicability boundaries (real H1/H2, compiled H3, WebSocket upgrade, SSE, native gRPC, transcoded gRPC, and L4 stream passthrough — explicitly verifying what is and is not HTTP-inspected).
2. **External security alerting recipe (`#628-B` + `#632` rejection extension):**
   - Shipped under `examples/alerting/security/` with pinned Prometheus (`prom/prometheus`) and Alertmanager (`prom/alertmanager`) configuration, declarative alert rules (`security-alerts.rules.yml`), native `promtool test rules` unit test suites (`security-alerts.test.yml`), and a reproducible local E2E verification harness.
   - Covers bounded security alert rules:
     1. `JulWAFProtectionWeakened` (`#630` producer: `jul_waf_serving_routes`, `jul_waf_protection_changes_total`).
     2. `JulSecurityPostureErrorPresent` and `JulSecurityPostureWarningSustained` (`#633` producer: `jul_security_posture_findings{area, severity}`).
     3. `JulSecurityEvidenceUnavailableOrDegraded` (`#633`/`#632` producer: `jul_security_evidence_state{producer, state=~"stale|degraded|unavailable"}`), with Alertmanager inhibition scoped strictly to `source_matchers: [alertname = "JulSecurityEvidenceUnavailableOrDegraded", producer =~ "waf_.*", state = "unavailable"]` inhibiting `target_matchers: [alertname = "JulSecurityPostureWarningSustained", area = "waf"]` on `equal: ['category']` so unavailable WAF evidence on a lean binary never suppresses independent `trust`, `certificates`, `admin`, `cache`, `intent`, or `maintenance` warnings.
     4. `JulWAFRejectionSymptomElevated` (`#632` extension producer: `jul_waf_transactions_total{mode="block", outcome=~"interrupted_request|interrupted_response|rejected_body_limit|inspection_error"}` relative to total inspected transactions with a minimum-traffic guard, framed strictly as an operational rejection symptom with an investigation link, never an automatic false-positive claim).

## Consequences

### Positive

- Operators get truthful WAF transaction outcomes, safe opaque support references, reproducible offline fixture evaluation, narrow field/method tuning that works on phase-1/phase-2/chained rules in Coraza `v3.8.1`, unified posture review, and verified external alerting without keeping Console open.
- Zero global mutable WAF state, zero new databases or sidecar daemons, and zero high-cardinality Prometheus labels.
- Preserves `#649` (`EX01`) confidentiality for external rule/dataset files across all lower-privilege APIs, metrics, logs, and UI views.

### Negative / accepted trade-offs

- Replacing `corazahttp.WrapHandler` with Jul's transaction-local connector moves ~180 lines of HTTP request/response glue into `internal/waf`, which Jul must maintain across Coraza upgrades; this is justified because `corazahttp.WrapHandler` silently turns body-read errors into `200 OK` and hides response-phase partial-delivery state.
- Structured exceptions cover the common single-rule / named-target / method-constrained cases (`<= 64` per policy); complex multi-rule correlation or custom transformations remain in `directives_files` / `inline_rules` and are surfaced as `unanalysed_custom` in posture/evaluation reports.

## Related

- [WAF guide](../waf.md)
- [Security posture](../security-posture.md)
- [Security testing gates](../security-testing.md)
- [Observability](../observability.md)
- [Diagnostics and support bundles](../diagnostics.md)
- [Programme acceptance and evidence ledger](../reviews/2026-11-02-629-waf-programme-ledger.md)
