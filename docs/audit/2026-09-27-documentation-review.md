# Documentation review — 2026-09-27

> **Scope and decision:** Partial, evidence-based remediation; **not an exhaustive certification and not 10/10**. This report is a dated record at the baseline below. The [item-level coverage ledger](2026-09-27-documentation-coverage.tsv) makes unreviewed work explicit. Do not treat its scores as a product maturity decision.

## Baseline and authority

- Review date: 2026-09-27 (Europe/Madrid). Clean default branch `main` at `36e0411a8e9c7877d9fc015534a2b6fa21c528b1`; no `AGENTS.md` found. Isolated branch: `docs/comprehensive-audit-2026-09-27`; no merge.
- Latest stable release: `v2.0.0` at `d56f5ceaf7ddb8a3875cbe6e27c9540db4130f75`, published 2026-09-21. GitHub latest-release API returned 25 assets. `v2.0.0-rc.1` is a distinct earlier prerelease.
- Last substantial dated audit: [pre-soak readiness audit](2026-09-16-pre-soak-readiness-audit.md), a historical baseline. Since it and the stable tag, the git log includes #426/#365/#366/#367/#368 NGINX work, #427 gRPC probes, #431 resources, #428/#429 ownership/WASM identity, #430 ABI v2, #432 affinity, #422 fault evidence and Wave 6 #437/#440/#445, plus CI and docs corrections.
- Authorities: implementation, tests and generated contracts for behavior; `docs/feature-status.yaml` for maturity/delivery; [#62](https://github.com/victornife/jul/issues/62) for execution; [#480](https://github.com/victornife/jul/issues/480) for selected next-release closure; roadmap for durable sequence; accepted ADRs/spec for decisions; audit register for dated disposition. #434, #438, #456 and #162 remain candidate/later/draft, not commitments.

## Executive assessment

| Dimension | Score / 10 | Evidence and limit |
| --- | ---: | --- |
| Accuracy | 7.5 | Verified release, metric, migration and onboarding claims corrected; many behavioral claims unreviewed. |
| Completeness | 5.5 | Source-to-doc review is sampled; 150 inventoried items still need semantic or platform review. |
| Coherence | 7.5 | Main status projections now agree in checked areas; deeper cross-guide tracing remains. |
| Currency | 8.0 | Wave 6 and stable-tag boundaries corrected; unreviewed pages may retain dated claims. |
| Usability | 7.0 | Basic first-run and assessment journeys improved and sampled; Windows, deployment and privileged rollback E2E remain. |
| Maintainability | 7.5 | Index status guard added; release-state metrics still rely on a manual tag reconciliation. |
| **Overall** | **6.8** | Provisional score reflects limited semantic coverage, not just the passing gates. |

## Coverage ledger

The [TSV ledger](2026-09-27-documentation-coverage.tsv) enumerates 213 tracked files or grouped source surfaces: **36 reviewed in a stated scope, 20 historical/context only, 7 generated/verified against source, 150 not fully reviewed**. A `reviewed` row means only its reason's claims were assessed; it does not certify the whole file. All Markdown was structurally scanned by `docs-check.py` except its documented exclusions, and root/example TOML was parsed by `config-check`; these checks do not prove runtime semantics. Accepted ADRs, current specs, most active feature guides, deployment variants, workflows, issue templates, historical archives and broad user-facing code comments are not line-by-line audited. The 20 historical rows preserve their dated meaning; the accepted ADR/spec documents are listed as not reviewed where their active decisions were not traced.

## Capability reconciliation

All rows below come from the 2026-09-27 feature manifest. `yes` means present in the v2.0.0 tagged manifest, `main` means the implementation is post-tag only. `core` is not a build tag. The source column names the primary implementation area, not proof that every behavior was executed. The guide and limits column is a documentation destination and review lead, not a blanket certification.

| ID / capability | Implementation / build profile | Maturity / delivery | Stable v2.0.0 | Guide and key limit | Evidence |
| --- | --- | --- | --- | --- | --- |
| Y1-01 — TLS + automatic HTTPS (ACME) | `internal/server; internal/config`; `core`, `acme` | GA / `soaked` | yes | [tls-acme.md](../tls-acme.md); See guide for operating limits | manifest; tag tree |
| Y1-02 — Compression (gzip / Brotli / Zstd) | `internal/middleware; internal/config`; `brotli`, `zstd` | GA / `soaked` | yes | [compression.md](../compression.md); See guide for operating limits | manifest; tag tree |
| Y1-03 — Rate + connection limiting | `internal/middleware; internal/config`; `core` | GA / `soaked` | yes | [ratelimit.md](../ratelimit.md); See guide for operating limits | manifest; tag tree |
| Y1-04 — Authentication (CIDR / Basic / JWT / forward-auth) | `internal/auth`; `core` | GA / `soaked` | yes | [auth.md](../auth.md); See guide for operating limits | manifest; tag tree |
| Y1-05 — Active health checks (HTTP / TCP probes) | `internal/upstream`; `core` | GA / `soaked` | yes | [health.md](../health.md); See guide for operating limits | manifest; tag tree |
| Y1-07 — Console (operations cockpit) | `internal/admin`; `console` | GA / `soaked` | yes | [console.md](../console.md); See guide for operating limits | manifest; tag tree |
| Y1-08 — Zero-config + jul lint | `cmd/jul/cli.go; internal/config`; `core` | GA / `soaked` | yes | [zeroconf.md](../zeroconf.md); See guide for operating limits | manifest; tag tree |
| Y1-09 — NGINX config importer | `internal/migrate/nginx`; `importer` | GA / `soaked` | yes | [nginx-importer.md](../nginx-importer.md); Base importer GA; no full NGINX parity | manifest; tag tree |
| Y1-10 — OTel tracing + access-log sinks | `internal/observability; internal/tracing`; `otel` | GA / `soaked` | yes | [otel.md](../otel.md); See guide for operating limits | manifest; tag tree |
| Y1-11 — HTTP/3 over QUIC | `internal/server`; `http3` | GA / `soaked` | yes | [http3.md](../http3.md); See guide for operating limits | manifest; tag tree |
| Y2-01 — gRPC ↔ JSON transcoding | `internal/transcode`; `grpc` | GA / `soaked` | yes | [grpc-transcoding.md](../grpc-transcoding.md); See guide for operating limits | manifest; tag tree |
| Y2-02 — WASM plugin system | `internal/plugins`; `wasmplugins` | GA / `soaked` | yes | [plugins.md](../plugins.md); v1 only; v2 is separate Beta row | manifest; tag tree |
| Y2-03 — L4 stream proxy | `internal/stream`; `stream` | GA / `soaked` | yes | [stream.md](../stream.md); Generic TCP/UDP; no protocol-specific MQTT semantics | manifest; tag tree |
| Y2-04 — Native gRPC passthrough + h2c | `internal/handler; internal/upstream`; `grpc` | GA / `soaked` | yes | [grpc-proxy.md](../grpc-proxy.md); See guide for operating limits | manifest; tag tree |
| Y2-05 — Service discovery / dynamic upstreams | `internal/upstream`; `consul`, `kubernetes` | GA / `soaked` | yes | [service-discovery.md](../service-discovery.md); See guide for operating limits | manifest; tag tree |
| Y2-06 — Web application firewall (WAF) | `internal/waf`; `waf` | GA / `soaked` | yes | [waf.md](../waf.md); See guide for operating limits | manifest; tag tree |
| Y2-07 — mTLS client auth | `internal/server; internal/auth`; `core` | GA / `soaked` | yes | [mtls.md](../mtls.md); See guide for operating limits | manifest; tag tree |
| SEC-1 — Secrets references + log redaction | `internal/config; internal/redact`; `core` | GA / `soaked` | yes | [secrets.md](../secrets.md); See guide for operating limits | manifest; tag tree |
| core-cache — Response cache (memory + disk) | `internal/cache`; `core` | GA / `soaked` | yes | [cache.md](../cache.md); See guide for operating limits | manifest; tag tree |
| core-http — Core HTTP (static / proxy / FastCGI / vhosts / routing) | `internal/handler; internal/router`; `core` | GA / `soaked` | yes | [core-http.md](../core-http.md); See guide for operating limits | manifest; tag tree |
| reload-tx — Configuration reload transaction | `internal/app; internal/lifecycle`; `core` | GA / `soaked` | yes | [reload-semantics.md](../reload-semantics.md); See guide for operating limits | manifest; tag tree |
| CGC-IN — Trusted client address (client_address) | `internal/clientaddr`; `core` | GA / `soaked` | yes | [configuration.md](../configuration.md); Admin listener keeps peer identity | manifest; tag tree |
| UT-BE — Backend TLS trust (backend_tls) | `internal/backendtls`; `core`, `grpc` | GA / `soaked` | yes | [upstreams.md](../upstreams.md); Trust policy per consumer; not global TLS bypass | manifest; tag tree |
| SEC-EGRESS — Auxiliary egress allow-list | `internal/egress`; `core` | Beta / `released` | yes | [egress.md](../egress.md); Off by default; Beta | manifest; tag tree |
| CGC-ROUTE — Request predicates, response headers, and CORS | `internal/router; internal/handler`; `core` | Beta / `released` | yes | [core-http.md](../core-http.md); See guide for operating limits | manifest; tag tree |
| CGC-RES — Upstream resilience (admission, retry, circuit) | `internal/resilience; internal/upstream`; `core`, `grpc`, `stream` | Beta / `released` | yes | [upstreams.md](../upstreams.md); Feature-specific GA/soak open | manifest; tag tree |
| AUTO-AUTH — Configuration authority and managed drift | `internal/app; internal/admin`; `core`, `console` | Beta / `released` | yes | [reload-semantics.md](../reload-semantics.md); See guide for operating limits | manifest; tag tree |
| AUTO-CONTRACT — Generated configuration contracts and route identity | `internal/configcontract; internal/lifecycle`; `core` | Beta / `released` | yes | [generated/config-reference.md](../generated/config-reference.md); See guide for operating limits | manifest; tag tree |
| MIG-ASSESS — NGINX migration assessment, provenance, and includes | `internal/migrate/nginx`; `importer` | Beta / `released` | yes | [nginx-assessment.md](../nginx-assessment.md); Bounded assessment; post-tag translations absent | manifest; tag tree |
| OPS-DIAG — Local diagnostics and support bundles | `internal/doctor; internal/supportbundle`; `core` | Beta / `released` | yes | [diagnostics.md](../diagnostics.md); See guide for operating limits | manifest; tag tree |
| ADMIN-TLS — Admin listener TLS and client authentication | `internal/admin`; `core` | Beta / `released` | yes | [deployment.md](../deployment.md); See guide for operating limits | manifest; tag tree |
| AUTO-API — Versioned external admin API | `internal/adminapi; internal/apicontract`; `core` | Beta / `released` | yes | [admin-api.md](../admin-api.md); Only generated external operations; Console endpoints internal | manifest; tag tree |
| AUTO-CLI — Remote automation CLI | `cmd/jul/remote_cli.go; internal/adminclient`; `core` | Beta / `released` | yes | [remote-cli.md](../remote-cli.md); See guide for operating limits | manifest; tag tree |
| HR-SELECTED — Selected runtime policy hot reload | `internal/app; internal/lifecycle`; `core` | Beta / `released` | yes | [hot-reload-strategy.md](../hot-reload-strategy.md); See guide for operating limits | manifest; tag tree |
| HTTP-UNIX — HTTP proxy over Unix-domain upstreams | `internal/upstream`; `core` | Beta / `released` | yes | [unix-http-upstreams.md](../unix-http-upstreams.md); Named plaintext HTTP/1.1 only | manifest; tag tree |
| GRPC-HC — Standard gRPC Health Checking Protocol active probes | `internal/upstream`; `core`, `grpc` | Beta / `merged` | main only | [health.md](../health.md); Full `grpc` tag; stable tag lacks probe | manifest; #62 and merged PR |
| OPS-RESOURCES — Runtime resources, capacity headroom, and HTTP bandwidth | `internal/observability; internal/storagefs`; `core`, `console` | Beta / `merged` | main only | [observability.md](../observability.md); Storage addition #437; no host-wide disk view | manifest; #62 and merged PR |
| WAF-PROVENANCE — Serving WAF policy and provenance visibility | `internal/waf; internal/admin`; `waf`, `console` | Beta / `merged` | main only | [waf.md](../waf.md); Serving compiled policy; no rule text or updater | manifest; #62 and merged PR |
| OPS-DIAG-UX — Resource-pressure diagnostics guidance in Console | `internal/admin/ui`; `core`, `console` | Beta / `merged` | main only | [diagnostics.md](../diagnostics.md); Guidance only; no automatic capture | manifest; #62 and merged PR |
| WASM-ABI2 — WASM plugin response phase (jul-abi/v2) | `internal/plugins`; `wasmplugins` | Beta / `merged` | main only | [abi.md](../abi.md); Bounded response phase; no streaming body hooks | manifest; #62 and merged PR |
| LB-AFFINITY — Deterministic consistent-hash affinity | `internal/affinity; internal/upstream`; `core`, `stream` | Beta / `merged` | main only | [upstreams.md](../upstreams.md); Rendezvous placement; NGINX hash approximated | manifest; #62 and merged PR |

**Reconciled discrepancies.** The index had two GA capabilities mislabeled Beta/merged, twelve v2.0.0 Beta capabilities mislabeled merged/candidate, and omitted four post-release Beta rows. The status manifest and tagged tree were correct; the guide/prose projections were corrected. Of 69 current metrics, 25 retain v1.32.0 provenance, 38 were already in v2.0.0, and six are post-tag; the metric contract and table now reflect those groups. New additive rows do not inherit a subsystem's GA rating.

## Findings, resolutions and dispositions

Severity: **P0** immediate exploitable/unsafe instruction or critical false trust claim; **P1** major release/security/operational falsehood or broken principal journey; **P2** material ambiguity, missing evidence or partial journey; **P3** minor clarity/cosmetic issue. No P0 was confirmed in reviewed scope. `Fixed` means the changed claim and relevant check were verified; it does not imply the rest of that page was fully audited.

| ID / severity | Original claim or gap and reader impact | Primary evidence | Concrete resolution, affected surfaces and verification | Disposition / confidence |
| --- | --- | --- | --- | --- |
| D01 / P1 | `docs/index.md:97-110` labeled GA trust as Beta/merged and twelve v2.0.0 Beta capabilities as unreleased; install decisions wrong. Four Wave 6/main rows absent. | Manifest CGC-IN/UT-BE and twelve `released` rows; v2.0.0 tag; #62. | Correct every index row and add four rows in `docs/index.md`; add manifest-to-index name, guide, maturity and delivery comparison in `scripts/docs-check.py` plus negative fixture in `scripts/test_docs_check.py`. Verify docs-check and induced mismatch failure. | **Fixed**, high. |
| D02 / P1 | `docs/known-limitations.md:18-38` grouped v2.0.0 features as merged/unreleased and said stable resilience release remained open. | Tagged manifest and `docs/status.md`. | Replace publication section with stable Beta, stable GA, post-tag Beta and real remaining boundaries. Verify each ID against manifest/tag. | **Fixed**, high. |
| D03 / P1 | `docs/metrics-contract.json` and `docs/observability.md:54-151` called 38 already stable metric families release pending; alerting/release compatibility reasoning wrong. | Set difference of current and `git show v2.0.0:docs/metrics-contract.json`: 25 v1, 38 stable additions, six post-tag. | Reclassify exactly 38 contract states as `released_v2.0.0`, synchronize all 69 table rows and compatibility prose, update `internal/observability/metric_contract_test.go` to expect 25/38/6. Verify Go contract test and table-to-contract comparison. | **Fixed**, high. |
| D04 / P2 | `docs/roadmap/README.md:52,89`, `docs/audit-register.md:35,43` still selected Wave 6 and called #422/#366/#367 open; readers would chase closed work. | #62 Wave 6 complete; issue states and merged commits. | Mark completed on post-tag main and point to #434 decision/#480 release closure; keep historical audit records intact. Verify issue/API snapshot and current wording. | **Fixed**, high. |
| D05 / P2 | `docs/status.md:168` implied every Beta row shipped; README and feature-guide banners implied backend TLS, routing, NGINX assessment or resilience were merged only, and new ABI/Storage/WAF features could look stable. | Manifest status rows and stable tag. | Correct `docs/status.md`, `README.md`, `docs/core-http.md`, `docs/upstreams.md`, `docs/nginx-importer.md`, `docs/nginx-assessment.md`; put tag/maturity prerequisites at point of use in `docs/abi.md`, `docs/plugins.md`, `docs/waf.md`, `docs/console.md`, `docs/diagnostics.md`, `docs/observability.md`. Verify manifest/index guard and exact tag presence. | **Fixed for scoped claims**, high. |
| D06 / P1 | `README.md:429-480` called migration output equivalent, denied include traversal and stream support, and listed several now-translated directives as unsupported. An NGINX operator could abandon or misjudge migration. | `docs/nginx-importer.md`, `docs/nginx-assessment.md`, importer code and real fixture result (manual action required). | Replace stale directive summary with `--assess`, bounded `--follow-includes --root`, report, validation and stable/main distinction; link canonical directive guide/corpus. Verify CLI assessment/conversion on `examples/migrate/nginx.conf` (both exit 3 with blocking result, report and TOML written). | **Fixed**, high. |
| D07 / P2 | `docs/getting-started.md:20-35` ran a static server against nonexistent `./public` and a proxy without explaining the backend prerequisite. | Repository has no `public/`; `cmd/jul/cli.go` and local HTTP smoke. | Create `public/index.html` in bash/PowerShell steps and name backend prerequisite; verify zero-config GET returns expected page. | **Fixed on Linux**, high; Windows commands unverified. |
| D08 / P1 | `docs/getting-started.md:150-180` promised Console apply/history/rollback under default `file_owned`, and showed a literal token. | `docs/deployment.md` authority contract, `internal/app`, successful `jul check`/`lint` with managed config and env secret. | Add `config_authority = "managed"` to the existing global table, require writable nonsymlink path, use `${env:JUL_ADMIN_TOKEN}` and restart. Validate merged sample with console-tagged CLI. | **Fixed for configuration**, high; browser rollback E2E open. |
| D09 / P2 | `docs/deployment.md:41` presented internal unversioned adoption route beside supported API wording; external clients could depend on an unsupported endpoint. | `docs/generated/openapi.json` lists `POST /api/v1/config/adopt-external`; route classification and API guide distinguish internal Console routes. | Name the versioned external route and Console workflow separately. Verify exact method/path against OpenAPI. | **Fixed**, high. |
| D10 / P2 | `docs/diagnostics.md:236` said remote diagnostics implementation was local despite the shipped reduced `jul diagnostics` command. | `cmd/jul/remote_cli.go`, `docs/remote-cli.md`, OpenAPI status/capabilities. | State local doctor/bundle vs remote reduced status/capabilities and explicit requirements for a future remote bundle. Verify CLI help and API contract. | **Fixed**, high. |
| D11 / P2 | Many active examples, config leaves, platform instructions and code-to-doc destinations remain unassessed beyond mechanical checks. A false security/behavior claim may still exist. | [Coverage ledger](2026-09-27-documentation-coverage.tsv): 150 unreviewed items; docs-check's checks only cover structural/selected semantic claims. | **Proposed issue: Documentation semantic certification** — assign each not-reviewed ledger row an owner; for every material config/API/CLI/Console/deploy surface trace implementation+tests → guide → examples → known limitations → tagged release; execute failure/recovery/rollback cases and update exact affected guides/contract sources. Verify ledger has no unexplained material `not reviewed`, journey evidence and CI. | **Open**, high confidence in coverage gap; unknown defect count. |
| D12 / P2 | Full NGINX real-Jul corpus, Windows/PowerShell, Docker/systemd and privileged Console rollback E2E could not be certified here. | Corpus gate failed binding Unix sockets (`socket: operation not permitted`); Docker absent; only Linux local CLI/HTTP samples ran. | **Proposed issue: Cross-platform docs journey evidence** — run `make nginx-corpus-check` and `make nginx-migration-e2e` on a network/socket-enabled host, Windows PowerShell first-run, packaged lean/full installs, editable/read-only deployment and authorized apply/stage/rollback; record exact SHA, commands and outcomes in a dated evidence record, correct `docs/getting-started.md`, `docs/deployment.md`, `docs/nginx-*.md`, `docs/console.md` on discrepancies. Verify all listed journeys on the final SHA. | **Open**, high confidence in missing evidence, not evidence of code failure. |

Every finding has an actionable resolution. D11/D12 are ready-to-file issue proposals, not activated product features. They can be combined into one documentation-certification issue if one owner can run both streams; do not close them based on a passing link check.

## Changes and diff

- `README.md`, `docs/index.md`, `docs/status.md`, `docs/known-limitations.md`, `docs/roadmap/README.md`, `docs/audit-register.md`: stable/main/product and programme truth; migration README rewritten.
- `docs/core-http.md`, `docs/upstreams.md`, `docs/nginx-importer.md`, `docs/nginx-assessment.md`, `docs/abi.md`, `docs/plugins.md`, `docs/waf.md`, `docs/console.md`, `docs/diagnostics.md`: maturity, tag and operating prerequisites at point of use.
- `docs/getting-started.md`, `docs/deployment.md`: copyable first-run, authority, secret and supported API path.
- `docs/metrics-contract.json`, `docs/observability.md`, `internal/observability/metric_contract_test.go`: metric publication provenance.
- `scripts/docs-check.py`, `scripts/test_docs_check.py`: regression guard for index status projection.
- This report and its TSV ledger record what was and was not assessed. Review with `git diff origin/main...docs/comprehensive-audit-2026-09-27` once the branch is published; no generated contract was hand-edited.

## Verification log and reader journeys

| Command / environment | Result | Interpretation |
| --- | --- | --- |
| Baseline: `python3 scripts/docs-check.py` with Go 1.26.6 and Python 3.12 | 1,739 passed, 0 failed | Baseline gate missed real status/release claims. |
| Baseline: `make generated-check`; `make config-check` with full tag set | Passed; 18 root + 11 example TOML configs load | Does not prove runtime behavior of every config. |
| Final pre-commit: `python3 scripts/docs-check.py`; `python3 scripts/test_docs_check.py`; `python3 scripts/test_semantic_drift.py` | 1,796 passed/0 failed; docs helper tests pass (their synthetic negative fixtures intentionally print FAIL); 24 semantic-drift tests pass | Structural, generated and selected semantic checks only. |
| `go test ./internal/observability -run '^TestMetricContract' -count=1`; independent 69-row table-to-contract comparison | Passed after 25/38/6 reclassification; all table states match | Collector metadata and publication groups checked. |
| Lean `go build`, console/importer tagged builds; `jul check` and `jul lint` on a combined managed, env-secret config | Passed on Linux; lint reported two nonfatal advice items | Confirms parser/runtime preflight, not browser workflow. |
| Zero-config Linux first run against temporary `public/index.html` | HTTP 200, exact page body | First-run static journey exercised. |
| Importer `--assess --follow-includes --root` and conversion `--report` on repository migration fixture | Both exit 3 (`manual_action_required`), report and TOML produced; blocking `proxy_set_header` surfaced | Correctly requires manual porting; no equivalence claim. |
| `make nginx-corpus-check` | Contract report and corpus package tests passed; real-Jul test blocked by Unix socket `operation not permitted` | Environment constraint, not a passing E2E. |
| Secret-pattern scan on tracked text; reviewed matching test fixtures without reproducing contents | No GitHub/AWS token patterns; PEM/Bearer hits were synthetic tests | Limited scan, not a full credential/history audit. |
| Important GitHub references | #62, #480 and latest stable release queried through GitHub API | Other external links not exhaustively checked. |

**Journey dispositions:** basic Linux proxy/static setup sampled; optional tagged feature documented but deployment/failure not replayed; NGINX assessment sampled with a blocking fixture; contributor authority/generator/update workflow traced through Makefile and checks; privileged Console authorization/rollback described and config-checked but not browser-tested. PowerShell, macOS, Windows, Docker, systemd, actual failure/recovery and full release artifacts remain unverified.

## Acceptance decision

| Required condition | Result |
| --- | --- |
| All material surfaces assessed or justified immaterial exclusions | **Fail:** 150 items/aggregates need semantic or platform review. |
| No unresolved known P0/P1 documentation finding | **Pass within reviewed scope; cannot certify repository-wide absence.** |
| No known false security, maturity, delivery or release claim | **Pass within reconciled claims; unreviewed pages remain.** |
| Major commands/examples and implementation destinations verified | **Partial:** first-run/importer/config samples; many negative and platform paths outstanding. |
| Active documents agree with authorities; history distinguished | **Partial:** sampled paths corrected; full bidirectional audit incomplete. |
| Required checks and reader journeys pass on final branch | **Partial:** mechanical checks pass; real-Jul NGINX corpus and platform/privileged journeys unavailable. |
| Every finding has concrete resolution, evidence, verification and disposition | **Pass for findings recorded here.** |

**Decision: not 10/10.** D11/D12 and the explicit unreviewed ledger prevent gold-standard certification. Neither #434 nor the AI Gateway draft was promoted by this documentation work. The next stable release remains governed by #480 and its own exact-final-SHA gates.
