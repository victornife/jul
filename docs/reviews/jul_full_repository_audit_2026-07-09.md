# Jul.IA — Full Repository Audit (2026-07-09)

> **Status: HISTORICAL SNAPSHOT — no longer the single source of truth.** Version 1.2 · Audited 2026-07-09 · **Updated 2026-07-16** (v1.1: Wave A/B + RG-1 soak closure; v1.2: Sprints 1–3 + all remaining audit items resolved). Repo state at original audit: `main`, commit `032929b`, Go 1.26.4. Snapshot `main`: commit `6e266bd`, Go 1.26.5.
>
> **Round 6 re-audit (2026-07-17):** A subsequent source-level re-audit identified new critical/high findings (R6-01 through R6-16). Remediation is tracked in live commits on `main` after `6e266bd` and is not yet reflected in the body below. See the Round 6 re-audit section at the end of this document for the finding register and remediation status.
>
> This document **supersedes** the [Full Repository Audit (2026-07-02)](previous_reviews/jul_full_repository_audit_2026-07.md) for the purpose of *current repository state*. The 2026-07-02 audit (and the point reviews it consolidated) is retained under [`previous_reviews/`](previous_reviews/) as historical decision input; where the two overlap, **this file wins**. See [`README.md`](README.md) for the decision-log index.

**Audit method.** Evidence-based review of the whole repository plus **full local re-verification** on Windows/amd64 (go1.26.4): lean + full-tag `go build`/`go test`, `govulncheck` (full tags), Console `typecheck`/`eslint`/`vitest`, `scripts/docs-check.py`, the `jul` CLI (`version`/`check`/`lint`/`fmt`/`healthcheck`), and code/doc reading. `-race` was **not** run locally (Windows box has no CGO/gcc toolchain — see §16); the CI Linux race lane is the authority for data-race evidence. Findings separate **Fact** (directly supported), **Inference** (reasoned interpretation), and **Recommendation**. Every non-trivial finding carries a **provenance tag**:

- **[Prior · Resolved]** — raised in the 2026-07-02 audit, now fixed and verified.
- **[Prior · Carried]** — raised on 2026-07-02, still open/unchanged (or only partially closed).
- **[Prior · Regressed]** — was fixed on 2026-07-02, has returned.
- **[Net-new]** — first identified in this 2026-07-09 audit.

**Benchmarks used for maturity/discipline only:** NGINX (stability, operator trust) and Caddy (ergonomics, automatic HTTPS). They are yardsticks, not templates.

---

## 0. Reconciliation with the 2026-07-02 audit

Every finding from the prior audit, mapped to its current state. This is the exhaustive supersession record.

| Prior ID | Title | 2026-07-02 status | 2026-07-09 status | Provenance |
| --- | --- | --- | --- | --- |
| CQ-1 | `internal/server` flaky hang | ✅ Resolved | **Resolved (holds)** — `goleak.VerifyTestMain` present in `internal/server/main_test.go` (+ cache/plugins/stream/upstream); package green in lean+full | [Prior · Resolved] |
| CQ-2 | Composition-root monolith | ◐ Partial (858 LOC) | **Carried (improved)** — `cmd/jul/main.go` now **739 LOC** (was 858→1087); `<250` target still unmet; ADR-0007 *Partial* | [Prior · Carried] |
| CQ-3 | Admin/validate god-files | ✅ Resolved | **Resolved (holds)** — `admin/api.go` 482, `config/validate.go` 570 (both <600) | [Prior · Resolved] |
| CQ-4 | `x/tools` pin for `gofast` | Open (deferred, ADR-0008) | **Resolved** — `gofast` **vendored** to `third_party/gofast` (`go.mod` `replace … => ./third_party/gofast`); pin removed, `x/tools` now v0.46.0; full build green | [Prior · Resolved] |
| CQ-5 | Error/resource conventions | low (positive) | **Holds (positive)** | [Prior · Carried] |
| REG-1 | Second rollback endpoint unserialized | ✅ Resolved | **Resolved (carried)** — not re-raced locally; single `applyMu` path per prior fix | [Prior · Resolved] |
| UX-1 | `jul lint -json` schema | ✅ Resolved | **Resolved (verified)** — lowercase keys + `"severity":"warning"` confirmed live | [Prior · Resolved] |
| UX-2 | `jul fmt` reserved/empty tables | ✅ Resolved | **Resolved (verified)** — `fmt` output starts at `[global]`, no `mail`/empty arrays | [Prior · Resolved] |
| UI-1 | No backend↔frontend browser e2e | ◐ Partial | **Carried** — Go over-the-wire e2e exists; browser/Playwright smoke still absent | [Prior · Carried] |
| DOC-1 | Soak evidence not published | ✅ Resolved (mechanism) | **Resolved but overtaken** — `soak-evidence.md` now published; superseded by **N-2** (labels overshoot the gate) | [Prior · Resolved] |
| SPEC-1 | Hardening backlog as prose | ✅ Resolved (burndown) | **Resolved (materially)** — the 13 Beta evidence bundles were genuinely worked down (matrix/bench/threat-note/fuzz landed per feature) | [Prior · Resolved] |
| QA-1 | Missing concurrency/negative tests | ◐ Partial | **Carried (mostly closed)** — `cmd/jul` now has `import_test.go`/`version_test.go`/`healthcheck_test.go`; `run --serve/--proxy` runtime smoke and ACME rotation-under-handshake still unverified | [Prior · Carried] |
| SEC-1 | Plugin `.wasm` upload surface | ✅ Resolved (hardening) | **Resolved** — filename/containment validation held; capability-re-validation-on-activation test still open (low) | [Prior · Resolved] |
| SEC-2 | Reload resilience | ◐ Partial | **Carried** — auth + WAF reload-churn leak lanes exist; a reload-under-load-**with-traffic** soak still absent | [Prior · Carried] |
| NEW-2 | Example config drift | ✅ Resolved | **Resolved (holds)** | [Prior · Resolved] |
| NEW-3 | God-files grown | ✅ (folded CQ-3) | **Resolved** | [Prior · Resolved] |

**Net movement since 2026-07-02:** strongly positive on engineering, with one governance regression in the making. Of 16 prior items, **11 are Resolved**, **5 are Carried** (mostly narrowed). CQ-4 — the one "medium" latent supply-chain trap left open on 2026-07-02 — is now genuinely closed by vendoring `gofast`. The Beta→GA evidence push (SPEC-1) was executed in full. Against that, two **[Net-new]** issues dominate this cycle: a fresh pair of standard-library CVEs (**N-1**), and a **maturity-claim overreach** in which the docs declare the soak gate "fully closed" while the project's own final soak gate (issue #39 / RG-1) is still open (**N-2**).

---

## 0.1 Reconciliation with 2026-07-16 work (Version 1.2 — final state)

This section records the status of every finding against all work shipped between 2026-07-09 and 2026-07-16. **v1.2 supersedes v1.1.** The audit body below is preserved verbatim; this table is the single authoritative status record.

### July-9 findings

| Finding ID | Title | 2026-07-09 status | Current status | Evidence |
| --- | --- | --- | --- | --- |
| **N-1** | go1.26.5 stdlib CVEs (`crypto/tls`, `os`) | ✅ Resolved in v1.2 (see §0.1) | ✅ **Resolved** | `go.mod` `go 1.26.5`; `Dockerfile` digest-pinned `golang:1.26.5-alpine` |
| **N-2 / N-2b** | Soak claim "fully closed" vs RG-1 (#39) | Open — P0 | ✅ **Resolved** | 4 × 8h isolated Linux soaks; `soak-evidence.md`, `status.md`, `ga-push.md` updated; wall-clock/throughput distinction noted (C6) |
| **N-3** | No Console maturity/soak signal | ✅ Resolved in v1.2 (see §0.1) | ✅ **Resolved/Moot** | RG-1 complete; all features soaked; README wording updated |
| **N-4** | Single shared admin token; RBAC design-only | Open — P1 | ⚠️ **Partially addressed — only remaining open item** | Limitation documented; `?token=` removed; RBAC Phase 1 **not yet shipped** (D1 backlog, L effort, Critical impact — see §Remaining below) |
| **N-5** | CI hygiene drift (25 eslint errors, docs-check gap) | ✅ Resolved in v1.2 (see §0.1) | ✅ **Resolved** | 35 [Unreleased] ESLint errors fixed (Week 1); `FLOOR_ADMIN` 75→76→77; macOS CI lane; 955/0 docs-check |
| **CQ-2** | `main.go` composition root 739 LOC | ✅ Resolved in v1.2 (see §0.1) | ✅ **Resolved** | `main.go` = 91 LOC; ADR-0007 closed; `internal/app/serve.go` + `factory.go` + helpers |
| **CQ-4 / CQ-5** | `gofast` vendor; error conventions | ✅ (v1.1) | ✅ **Resolved — holds** | |
| **UI-1** | No Playwright browser/schema E2E | Carried | ✅ **Resolved** — Sprint 2 (C2) added `e2e/real-server.spec.ts`: 6 Playwright `request`-fixture tests against a real `jul -tags console` binary validating Go admin API shapes against Zod schemas. Existing `smoke.spec.ts` covers browser-level SPA rendering. Schema-drift detection is now CI-gated via `console-e2e-real-server` job. | commit `4d760e1` |
| **UX-3** | `jul run --serve` smoke + `import` golden | Carried | ✅ **Resolved** (confirmed in v1.1 — pre-existed) | `TestCmdRunServeSmoke`, `TestCmdRunProxySmoke` in `cmd/jul/run_smoke_test.go` |
| **QA-2 (ACME)** | Cert rotation under concurrent handshakes | Carried | ✅ **Resolved** (confirmed in v1.1) | `TestACMERotationUnderConcurrentHandshakes` |
| **QA-2 (reload-under-load)** | Reload with in-flight traffic | Carried | ✅ **Resolved** (confirmed in v1.1) | `TestReloadDrainsBeforeRetiringClosers` |
| **SEC-1** | Plugin capability re-validation | Carried | ✅ **Resolved** (confirmed in v1.1) | `TestCapabilityGrantsAreRevalidatedOnActivation` |

### Sprint / Week work shipped after v1.1

| Item | What | Impact | Effort | Commit |
| --- | --- | --- | --- | --- |
| **Week-1 / R-1** | Fix 35 ESLint errors in `[Unreleased]` Console Overview code — CI `console-frontend` lint gate was broken (`ChartDetailPanel`, `Sparkline`, `computeMetricSummary`, `metricMeta`, test) | Critical (unblocked CI) | S | `02a0f1b` |
| **Week-1 / R-2** | Unify `cmdServe` / `run()` missing-file error message; `TestCmdServeMissingConfig` | Low | S | `02a0f1b` |
| **Week-1 / R-3** | `TestCmdFmtDiffNoChange` + `TestCmdFmtDiffChanges` | Low | S | `02a0f1b` |
| **Week-1 / R-5** | `docs/getting-started.md`: `jul serve` section + `jul fmt --diff` | Low | S | `02a0f1b` |
| **Sprint-1 / C4** | `docs/zeroconf.md`: `jul fmt` callout box (with `-w`, `-diff`, CI usage) | Low | S | `80915d8` |
| **Sprint-1 / C5** | `FLOOR_ADMIN` 76 → 77 in CI; baseline comment updated to 2026-07-16 | Medium | S | `80915d8` |
| **Sprint-1 / C6** | `docs/soak-evidence.md`: explicit wall-clock vs throughput proof labels on WASM 8h entry | Low | S | `80915d8` |
| **Sprint-2 / R-4** | `TrafficControlEditor.tsx` 821 → 682 lines; `TrafficFormFields.tsx` (154 lines) with `TextField`, `NumberField`, `Toggle`, `CheckboxGroup`, `AffectedRoutes` extracted | Medium | M | `4d760e1` |
| **Sprint-2 / C2 (= UI-1)** | `e2e/real-server.spec.ts` + `console-e2e-real-server` CI job + `testdata/console-e2e.toml`; 6 schema-drift tests against real binary | High | M | `4d760e1` |
| **Sprint-3 / C3** | `scripts/test-discovery-k8s-live.sh` + `discovery-k8s-kind.yml`; K8s EndpointSlice convergence CI lane via kind — closes the last discovery regression gap; also adds admin pool assertion absent from the PS1 lane | High | M | `8391577` |
| **Bonus: TestHealthCheckTCP** | Replaced zero-latency TCP probe assertion with a 50 × 2 ms polling loop; root cause: Linux kernel can complete a queued SYN/SYN-ACK after `ln.Close()` at zero delay. 20/20 stable. | Medium | S | `6e266bd` |

### Remaining open item

| Item | Title | What remains | Impact | Effort | Gate |
| --- | --- | --- | --- | --- | --- |
| **D1 (N-4)** | **Console RBAC Phase 1** | Replace single `[admin].token` superuser with named principals, predefined roles (`viewer`/`operator`/`admin`), scoped hashed tokens, deny-by-default at API boundary. Full design in `docs/specs/console-rbac.md` (ADR 0010). Backward-compatible: RBAC disabled by default. | Critical — blocks multi-user adoption, enterprise credibility, per-principal audit attribution | L | None — design is implementable today |

**Strategic bets (demand-gated — not started, not blocking):**

| Item | What | Gate |
| --- | --- | --- |
| E1 — AI Gateway MVP | Thin OpenAI-compatible front door; multi-provider routing, streaming, cost metrics; kill/continue gate after MVP | Jul.IA used as API/protocol gateway; users ask for AI routing |
| E2 — RBAC Phase 2 | Token management API (`POST /api/admin/tokens`), gated by `admin:manage` | D1 shipped |
| E3 — RBAC Phases 3–4 | Custom roles; proposer/approver split | D1+E2 shipped; multi-operator demand |
| E4 — GraphQL composition | Schema-first resolvers over gRPC/REST unary | Users request BFF/composition |
| E5 — K8s Gateway API + Ingress | Watch Ingress/Gateway API; Helm chart | Real ingress-controller demand validated |

**v1.2 summary:** The repository is in its cleanest state since the project began. Every audit finding from July 9 is resolved except N-4 (RBAC Phase 1, design-complete, runtime unshipped). All 6 sprint items landed. Every CI gate is green: 21 Go packages, 393 frontend tests, 0 ESLint errors, 0 `go vet` issues, 955/0 docs-check, Linux + macOS + Windows CI lanes. All RG-1 soaks done. All large-file decompositions done. K8s discovery CI lane added. Schema-drift E2E added. The "honest maturity" claim is now fully backed by the evidence.
| --- | --- | --- | --- | --- |
| **N-1** | go1.26.5 stdlib CVEs (`crypto/tls`, `os`) | ✅ Resolved in v1.2 (see §0.1) | ✅ **Resolved** — toolchain bumped to go1.26.5 in `go.mod`, `Dockerfile`, README, CI `setup-go`; `govulncheck` passes | `CHANGELOG [Unreleased]` "Bumped the Go toolchain from 1.26.4 to 1.26.5… to clear the newly disclosed stdlib CVEs" |
| **N-2** | Soak claim "fully closed" while RG-1 (#39) open | ✅ Resolved in v1.2 (see §0.1) | ✅ **Resolved** — all four RG-1 isolated 8h Linux soaks completed; `soak-evidence.md`, `status.md`, `ga-push.md`, `plugins.md` updated | gRPC 8h Linux 2026-07-15 (59.1M transcoding + 51.4M passthrough, ~0% err); HTTP/3 8h Linux 2026-07-13 (55.3M req, 0 err); L4 stream 8h Linux 2026-07-11 (54.9M sends, 0 err); WASM 8h Linux 2026-07-16 (21.7M+ req verified, 0 missing plugin headers) |
| **N-2b** | roadmap/vision/status internally inconsistent with RG-1 | ✅ Resolved in v1.2 (see §0.1) | ✅ **Resolved** — status.md/ga-push.md updated with Linux soak evidence for all 4 RG-1 targets; soak tracking table entries updated | `docs/soak-evidence.md` entries dated 2026-07-11 through 2026-07-16; `docs/status.md` soak rows updated |
| **N-3** | Console and README present every feature with no maturity/soak signal | ✅ Resolved in v1.2 (see §0.1) | ✅ **Resolved/Moot** — RG-1 soaks completed; no features are "RG-1 pending" any more; README "Feature maturity" table wording updated to reference the nine-criteria GA bar with a pointer to `docs/status.md` | commit `a708aff` ("wave-A: …Soften 'All features GA' claim in README") |
| **N-4** | Admin control plane is a single shared bearer token; RBAC design-only | Open — P1 | ⚠️ **Partially addressed** — limitation prominently documented in `docs/console.md`, `SECURITY.md`, new `docs/security-posture.md`, new `docs/known-limitations.md`; `?token=` URL parameter removed from the Console frontend; RBAC Phase 1 implementation **not yet shipped** | commits `56f4c40`, `a708aff`; RBAC remains backlog item D1 |
| **N-5** | CI hygiene drift (25 eslint errors, docs-check gap) | ✅ Resolved in v1.2 (see §0.1) | ✅ **Resolved** — eslint errors fixed (v1.32.0); macOS CI test lane added; `docs-check.py` 952/0; console `typecheck` clean; `FLOOR_ADMIN` raised to 76% | `CHANGELOG 1.32.0` "Restored two CI gates to green"; commit `a708aff` (test-macos job, FLOOR_ADMIN 75→76) |
| **CQ-2** | `main.go` composition root 739 LOC; `<250` target unmet | ✅ Resolved in v1.2 (see §0.1) | ✅ **Resolved** — `internal/app/factory.go` and `internal/app/serve.go` extracted (CQ-2 / #54); `main.go` now **91 LOC**; ADR-0007 closed | ADR-0007 "Status: Accepted — composition-root extraction complete; `main.go` < 100 LOC (CQ-2 / #54, 2026-07-15)"; commit `a708aff` |
| **CQ-4** | `gofast` vendor hygiene; ADR-0008 | ◐ (already Resolved in audit) | ✅ **Confirmed Resolved** — ADR-0008 status is "Resolved (vendored gofast)"; `third_party/gofast` carries upstream commit hash | ADR-0008 text confirmed in repo |
| **CQ-5** | Error-handling and resource conventions | Positive (holds) | ✅ **Holds** | unchanged |
| **UI-1** | No Playwright browser smoke against built SPA | Carried | ✅ **Resolved in v1.2** — see §0.1 reconciliation table (Sprint-2 / C2) | backlog item C1 |
| **UX-3** | `jul run --serve` smoke + `import` golden | Carried | ✅ **Resolved** — `cmd/jul/run_smoke_test.go` contains `TestCmdRunServeSmoke` (boot→serve→HTTP request→shutdown) and `TestCmdRunProxySmoke` (boot→proxy→HTTP request→shutdown); both run in the default CI lean lane | `cmd/jul/run_smoke_test.go` `TestCmdRunServeSmoke` / `TestCmdRunProxySmoke` |
| **QA-2 (ACME rotation)** | ACME certificate rotation under concurrent TLS handshakes | Carried | ✅ **Resolved** — `internal/server/acme_test.go` `TestACMERotationUnderConcurrentHandshakes` exercises `dynamicCertProvider` swap under concurrent `GetCertificate` calls, asserting no nil cert during rotation | `internal/server/acme_test.go:157` |
| **QA-2 (reload-under-load)** | Reload while real HTTP traffic is in flight | Carried | ✅ **Resolved** — `internal/server/reload_test.go` `TestReloadDrainsBeforeRetiringClosers` triggers a reload while a gen-1 request is blocked mid-handler, asserts new requests see gen-2 immediately, and asserts gen-1 resources are NOT retired until the in-flight request drains | `internal/server/reload_test.go:207` |
| **SEC-1 (capability revalidation)** | Plugin capability re-checked on config change (kv/fetch grants revoked across reloads) | Carried | ✅ **Resolved** — `internal/plugins/plugins_test.go` `TestCapabilityGrantsAreRevalidatedOnActivation` builds gen-1 with `kv=true`, runs requests, then builds gen-2 *without* `kv`, and asserts the counter resets (capability denied) on the new generation; `TestKVDeniedWithoutCapability` and `plugins_hardening_test.go` cover fetch-allowlist enforcement | `internal/plugins/plugins_test.go:240` `TestCapabilityGrantsAreRevalidatedOnActivation` |

**Additional work shipped 2026-07-09 → 2026-07-16 (not in original audit scope):**

| Item | What | Commits |
| --- | --- | --- |
| HP-01 completion | `reload_timeout` timed-out outcome surfaced in `ApplyOutcomeBanner` (frontend); `previous_reload` parsed from both apply paths; 3 new tests | `a708aff` |
| `jul fmt --diff` | Unified diff output without in-place rewrite; CI-friendly exit 0/1 | `a708aff` |
| `jul serve` alias | Explicit, discoverable `jul serve [-config f]` equivalent to bare `jul` | `a708aff` |
| Legacy flag deprecation | `-check`/`-version` flags print a deprecation notice | `a708aff` |
| `?token=` removed | URL-parameter token bootstrap removed from frontend; Go backend never supported it | `56f4c40` |
| `docs/known-limitations.md` | New page aggregating per-feature limitation lists from 12 feature docs + admin token + single-node | `56f4c40` |
| `docs/security-posture.md` | Operational security reference: admin model, RBAC roadmap, SSRF table, hardening checklist | `56f4c40` |
| B1: `patch.go` decomposition | 1,351 → 740 lines (dispatch only) + `patch_helpers.go` (403) + `patch_http.go` (271) | `1bcf0b8` |
| B2: `PluginsPanel.tsx` decomposition | 763 → 138 lines + 4 sub-components (`PluginEditorDrawer`, `AttachPluginDrawer`, `UploadPluginDrawer`, `PluginCard`) | `1bcf0b8` |
| B3: `projections.go` decomposition | 928 → 601 lines + `projection_types.go` (347) | `1bcf0b8` |
| B3: `diff_helpers.go` decomposition | 913 → 538 lines + `diff_global.go` (392) | `1bcf0b8` |
| WASM burn-in load generator | `scripts/burn-in-wasm.go` — sustained load test with plugin-header assertion, error budget | `bb89459` |
| macOS CI lane | `test-macos` job in `ci.yml` for both lean + full profiles | `a708aff` |
| Frontend coverage threshold (CI) | Confirmed existing `console` job already enforces 70% floor | pre-existing |
| `docs/index.md` improvements | `docs/reviews/` linked; `docs/known-limitations.md` + `docs/security-posture.md` linked | `56f4c40` |

**v1.2 summary:** The repository is in its cleanest state since the project began. Every audit finding is resolved except N-4 (RBAC Phase 1). All CI gates green. See § 0.1 for the full record.

> **Previous v1.1 executive summary** (preserved): The repo has moved from the state described in §1 to a materially stronger position. The two P0 items are resolved. The composition root is finalized. All but one of the original findings are resolved. The single remaining open item was RBAC Phase 1 (N-4) and the Playwright browser smoke (UI-1). The "honest maturity" story is accurate: the soak gate is genuinely closed, not just claimed closed.

---

## 1. Executive summary

**Overall maturity: a genuinely well-engineered, honestly-governed single-binary edge server that has just crossed from "disciplined Beta" into an *over-eager* GA claim its own evidence does not yet fully support.**

> **2026-07-16 update (v1.2):** Every audit finding is resolved except N-4 (RBAC Phase 1, design-complete, runtime not implemented). All sprint items landed. Every CI gate is green. See § 0.1 for the complete status table. The body below is preserved verbatim as the original July 9 findings.

The code is in good shape. Lean and full-tag builds compile; the entire test suite passes locally in both profiles (lean and full); the Console typechecks, lints, and passes **361** unit tests; `docs-check.py` passes **897/0**; the CLI is clean and scriptable. The architecture remains intentional (generational atomic reload, preflight-before-apply admin writes, build-tag gating with loud rejection), and this cycle added real hardening: an opt-in egress allow-list (**N-P1**), bounded metric cardinality (**N-P2**), structured create/delete patch-ops (**N-P3**), explicit apply-outcome signaling (**N-P4**), container digest pinning + a shell-less `HEALTHCHECK` (**N-P7**), and the removal of the `x/tools` supply-chain pin by vendoring `gofast` (CQ-4).

**Strongest areas (Fact-backed).**
- **Engineering discipline.** All prior P0/P1 code findings are closed; the composition root keeps shrinking (main.go 1087→858→**739**); the two god-files stayed split; `goleak` guards the leak-prone packages.
- **Beta→GA evidence execution.** The 13 features that were Beta on 2026-07-02 now each carry a conformance matrix, published benchmarks, a known-limitations list, a threat note, and (where parsing exists) a fuzz target — a real, checked-in evidence bundle, not prose. This is the SPEC-1 burndown actually burned down.
- **Security by construction, extended.** The `SECURITY.md` trust model still holds, and the new `internal/egress` allow-list closes the residual config-driven SSRF shape the prior audit flagged. Plugin egress guards, loopback-always-blocked, and DNS-rebinding checks remain.
- **Operability.** Console apply now resolves to four explicit operator-legible outcomes (applied-and-live / reloading / degraded-subsystem / restart-required), and troubleshooting docs were expanded to match.

**Riskiest areas (Fact-backed, 2026-07-09).**
1. **N-1 (high, security).** `govulncheck` (full tags) now reports **two called standard-library vulnerabilities** — `GO-2026-5856` (`crypto/tls`) and `GO-2026-4970` (`os`) — both **fixed in go1.26.5**; the repo is pinned to go1.26.4. `crypto/tls` is directly on the edge server's data path. The 2026-07-02 scan was clean; this is a fresh disclosure requiring a toolchain bump.
2. **N-2 (high, governance/truthfulness).** `docs/roadmap`, `docs/vision`, and `docs/status.md` all state the soak post-GA gate is **"fully closed"** and mark **every** feature GA with soak-criterion-5 ✅. But the project's own **final** soak gate — issue **#39 [SEQ-13][RG-1]**, "Final long-run isolated soak gate on latest architecture" — is **open**, and its acceptance criteria (four **8-hour isolated** soaks for L4 stream, WASM, HTTP/3, and gRPC on the latest architecture) are **not met** by the cited evidence (1-hour isolated, or consolidated-not-isolated). Two rows (zero-config, importer) are marked soak-✅ on the basis of **validation scripts, not soak runs**, contradicting `soak-evidence.md`'s own "smoke ≠ soak" rule. All soak evidence is **Windows-local single-platform**; the one release-gate-duration proxy run *failed* on Windows. The GA labels are defensible against ADR-0005's 1-hour *minimum*, but the docs overclaim by declaring the gate "fully closed."
3. **N-4 (medium, operations).** The admin control plane is still a **single shared bearer token**. Named-principal RBAC exists only as a design (ADR-0010 / `docs/specs/console-rbac.md`), with no runtime enforcement. For a product whose Console is a headline differentiator, single-token admin (no per-operator attribution, scoping, or revocation) is a real GA-grade operations gap.
4. **CQ-2 (low→medium, carried).** `main.go` is still 739 LOC; the `<250` composition-root target is unmet and ADR-0007 remains *Partial*.

**Is core HTTP / Console GA-soak ready?** **Inference:** the *code* is materially GA-ready (green suites, resolved leaks, hardening landed). The *claim* is ahead of the *evidence*. Core HTTP + Console are best described as **"GA on eight criteria; final RG-1 soak (issue #39) pending"** — which is exactly what the team privately believes, but not what the public docs currently say.

**Which advanced features should be presented with a caveat?** The four RG-1 targets — **L4 stream, WASM plugins, HTTP/3, gRPC** — should carry an explicit "soak: RG-1 pending (isolated 8h)" note until #39 closes, rather than an unqualified soak-✅.

**Top 5 to do next.**
1. **Bump the toolchain to go1.26.5** (go.mod / Dockerfile / README / CI setup-go) to clear N-1; re-run `govulncheck`. *(P0 / security)*
2. **Reconcile the soak claim with RG-1.** Replace "soak gate fully closed" with per-feature soak status that matches issue #39; downgrade the two validation-script rows from soak-✅ to "functional-validated, soak pending." *(P0 / docs-governance)*
3. **Run and publish the four RG-1 isolated 8h soaks** (stream/WASM/HTTP3/gRPC) on the **Linux** target, then update status strictly from those artifacts. *(P1)*
4. **Surface maturity in the operator surface** — restore a per-feature maturity/soak indicator in the Console and README so an operator can see what is RG-1-pending (N-3). *(P1 / UX-docs)*
5. **Land RBAC phase 1** (named principals + scoped tokens per ADR-0010) or explicitly document single-token admin as a known GA limitation (N-4). *(P1/P2 / security)*

---

## 2. What Jul.IA is today

**Fact ([`README.md`](README.md)).** Jul.IA is an NGINX-inspired HTTP edge server written in Go, configured entirely through TOML, shipped as a single static `CGO_ENABLED=0` binary, with optional features behind build tags.

**Core value proposition (unchanged, verified).** A *lean, single-binary* edge server covering the 80% NGINX/Caddy use cases (static, reverse proxy + LB, TLS/automatic-HTTPS, cache, compression, auth, rate/connection limiting) **plus** a differentiated protocol-gateway core (gRPC↔JSON transcoding + native gRPC passthrough + h2c) **plus** a genuinely usable operations Console — with no runtime dependency footprint.

**Target users (Inference).** Operators of single-node edge infrastructure; teams migrating off NGINX (there is an importer); shops needing gRPC↔JSON at the edge; developers who want zero-config HTTPS and a Console rather than hand-edited config.

**Feature surface (Fact, `README.md` + `docs/status.md`).** Static/proxy/FastCGI/uWSGI/vhosts/routing; TLS 1.2/1.3 + ACME (HTTP-01, TLS-ALPN-01, OCSP stapling) + mTLS; auth (CIDR/Basic/JWT/forward); rate + connection limiting; compression (gzip core; brotli/zstd tags); two-tier cache (mem+disk, SWR/SIF); gRPC transcoding + passthrough + h2c; L4 TCP/UDP stream proxy with PROXY protocol + SNI routing; WASM plugins (wazero); WAF (Coraza + CRS); service discovery (DNS/SRV core; Consul/K8s tags) with live-CI convergence; observability (structured logs, bounded-cardinality Prometheus, OTel, access-log sinks); HTTP/3; secrets refs + log redaction; an optional egress allow-list; NGINX importer; and an admin Console. CLI now includes `jul version`, `jul healthcheck`, and `jul completion`.

**Maturity split (Fact, current docs).** `docs/status.md` v1.32 (2026-07-09), `docs/ga-push.md` v1.32, and `docs/roadmap` v1.32 all now classify **every** shipped feature as **GA**, with the soak post-GA gate declared **"fully closed."** This is a change from 2026-07-02 (7 GA-soak-pending + 13 Beta). See **§5** and **§9** for why that classification overshoots the project's own RG-1 evidence gate.

**Implicit positioning (Inference).** Per `docs/reviews/README.md`, the deliberate stance remains *"leanest serious edge/protocol gateway"* — the flagship differentiator is the gRPC gateway + Console operability, not raw feature count.

**What it should *not* try to be yet (Recommendation).** Not a fleet/mesh control plane, not multi-tenant SaaS, not an AI gateway — correctly parked as "vision horizon / demand-gated." It should *also* resist presenting all 20 features as equally battle-tested GA before RG-1 closes; the honest per-feature soak status is a trust asset and should be preserved, not flattened to a blanket "GA."

---

## 3. Architecture assessment

**Entry points / composition root (Fact).** [`cmd/jul/main.go`](../../cmd/jul/main.go) `run()` dispatches CLI subcommands ([`cmd/jul/cli.go`](../../cmd/jul/cli.go)) or falls through to `serve()`, the composition root, which wires logging, cache, metrics, tracer, ACME manager, stream server, WAF, egress policy, the handler factory, and the admin server, then calls `server.New()`/`Run()`. Pure wiring/preflight helpers live in the testable [`internal/app`](../../internal/app) package (`wiring.go`, `admin_deps.go`, `preflight.go`, `runtime.go`, `generation.go`). Windows-service integration is build-tag split.

**Package/domain map (Fact).** Cleanly separated `internal/` packages: `app`, `config`, `server`, `router`, `handler`, `upstream`, `middleware`, `cache`, `auth`, `transcode`, `stream`, `plugins`, `waf`, `observability`, `tracing`, `redact`, `atomicfile`, **`egress`** (net-new), and `admin` (+ `admin/ui`). Feature packages remain gated by build tags with `*_stub.go` fallbacks so lean builds compile and reject unsupported config loudly at startup.

**Config lifecycle (Fact).** Parse → Defaults → Validate → Preflight → Apply → Persist. `Validate()` + helpers in [`internal/config/validate.go`](../../internal/config/validate.go) (570 LOC; location/backend validators in `validate_location.go`/`validate_backends.go`); `PreflightClone()` dry-runs the full composition without applying; persistence via [`internal/atomicfile`](../../internal/atomicfile/atomicfile.go) (temp→sync→chmod 0600→rename).

**Runtime/reload model (Fact).** [`internal/server/server.go`](../../internal/server/server.go) uses generational handlers with atomic swap and in-flight drain; cache/metrics/tracer/log-sinks persist across reloads; pools/handlers rebuild. Bad edits keep the running config. The auth and WAF reload-churn leak lanes (`internal/auth/reload_churn_test.go`, `internal/waf/reload_churn_test.go`) prove the build-drop-on-reload invariant.

**Admin/control-plane architecture (Fact).** [`internal/admin`](../../internal/admin) exposes a loopback-bound, bearer-token API with read projections and a write path: `validate` → `diff` → `apply` (snapshot→WriteConfigRaw→reload) → `rollback`, serialized by `applyMu` with optimistic-concurrency `base_version` tokens. The structured patch API now covers create/delete (`server_add`/`server_remove`, `location_add`/`location_remove`, `upstream_add`/`upstream_remove`) in addition to edits ([`internal/admin/patch.go`](../../internal/admin/patch.go)).

**Extensibility points (Fact).** Router `Builder` registry; per-location `LocationModifier`; WASM plugin ABI; pluggable cache store, access-log sinks, discovery backends; and the new egress `DialFunc` seam that lets subsystems be guarded without importing `internal/egress`.

**Intentional vs accidental (Inference).** Overwhelmingly *intentional* and improving. The one structural debt is size: `main.go` at 739 LOC still holds the `buildHandlers`/`serve()` body inline (CQ-2). `internal/handler` still couples to upstream/config/middleware/auth/waf — a maintainability risk, not a correctness bug.

**Net-new architectural additions this cycle (Fact).** `internal/egress` (SSRF allow-list), bounded metric cardinality in `internal/observability/metrics.go`, the `internal/admin/ui/src/lib/applyOutcome.ts` derivation, and the `third_party/gofast` vendor that removed the `x/tools` pin.

---

## 4. Code quality findings

### Finding N-1: `govulncheck` reports two called standard-library CVEs (crypto/tls, os)

- **Severity:** high
- **Provenance:** [Net-new]
- **Area:** toolchain / [`go.mod`](../../go.mod), [`Dockerfile`](../../Dockerfile), CI `setup-go`
- **Evidence:** `govulncheck -tags "<full>" ./...` (this audit) → `GO-2026-5856` *Found in* `crypto/tls@go1.26.4` *Fixed in* `crypto/tls@go1.26.5`; `GO-2026-4970` *Found in* `os@go1.26.4` *Fixed in* `os@go1.26.5`; "Your code is affected by 2 vulnerabilities from the Go standard library" (plus 1 required-module vuln not on a call path). The 2026-07-02 scan reported no vulnerabilities.
- **Fact:** The pinned toolchain (go1.26.4) is affected on called paths; the fixes ship in go1.26.5.
- **Inference:** A `crypto/tls` advisory on an edge server's TLS path is directly reachable in production; the `os` advisory affects file handling. Both are cleared by a toolchain bump, not a source change (this matches the repository's own history of clearing stdlib advisories by moving to the patched Go).
- **Why it matters:** The project's supply-chain credibility rests on a clean `govulncheck`; an edge server carrying a called `crypto/tls` CVE undercuts the "secure by default" story.
- **Recommendation:** Bump to **go1.26.5** in `go.mod` (`go` directive), `Dockerfile` (`golang:1.26.5-alpine` + digest), `README.md` ("Requires Go 1.26+"), and the CI `setup-go` lane; re-run full suite (both profiles) + `go mod tidy` idempotency + `govulncheck`; expect "No vulnerabilities found."
- **Acceptance criteria:** `govulncheck` (full tags) reports zero called vulnerabilities on CI and locally; CHANGELOG records the bump.
- **Effort:** S
- **Dependencies:** none

### Finding CQ-2: Composition root still above the `<250` LOC target

- **Severity:** medium (was high→low; nudged up because it is now the largest untested-surface residue)
- **Provenance:** [Prior · Carried]
- **Area:** [`cmd/jul/main.go`](../../cmd/jul/main.go)
- **Evidence:** `main.go` = **739 LOC** (1087 at v1.0, 858 on 2026-07-02); the `internal/app` package holds the extracted, unit-tested wiring/preflight/generation helpers, but the `buildHandlers`/`serve()` body remains inline in `package main`. ADR-0007 is *Partial*.
- **Fact:** The trend is good (three consecutive reductions) but the target is unmet.
- **Inference:** The remaining inline factory is the highest-leverage code with the least direct unit coverage; change risk stays elevated.
- **Recommendation:** Extract `buildHandlers`/`serve()` into `internal/app` behind a `BuildHandlers(cfg) (http.Handler, retire func, error)` seam; add table tests; finalize ADR-0007.
- **Acceptance criteria:** `main.go` < ~250 LOC; factory unit-tested; ADR-0007 flipped from *Partial* to *Accepted*.
- **Effort:** L
- **Dependencies:** none

### Finding CQ-4: `x/tools` supply-chain pin removed by vendoring `gofast` — verify vendor hygiene

- **Severity:** low (was medium)
- **Provenance:** [Prior · Resolved]
- **Area:** [`go.mod`](../../go.mod) (`replace github.com/yookoala/gofast => ./third_party/gofast`), [`third_party/gofast`](../../third_party/gofast/go.mod)
- **Evidence:** The v0.6.0 `x/tools` `replace` is gone; `x/tools` is now `v0.46.0 // indirect`; `gofast` resolves to the in-tree vendor; full-tag build is green.
- **Fact:** The latent forced-upgrade trap identified on 2026-07-02 is closed exactly as recommended (vendor a minimal FastCGI client without the `godoc/vfs` dependency).
- **Inference:** Residual maintenance now sits on the vendored copy (it must be patched by hand if `gofast` upstream fixes a bug/CVE).
- **Recommendation:** Update **ADR-0008** to *Resolved (vendored)*; add a short note in `third_party/gofast` recording the upstream commit it derives from and a `govulncheck` reminder for the vendored tree.
- **Acceptance criteria:** ADR-0008 reflects the vendoring; vendor provenance documented.
- **Effort:** S
- **Dependencies:** none

### Finding N-5: CI hygiene gates went red and were fixed same-day; local parity did not catch them

- **Severity:** low
- **Provenance:** [Net-new]
- **Area:** `.githooks/`, `internal/admin/ui` eslint config, CI
- **Evidence:** `CHANGELOG` 1.32.0 (2026-07-09) "Restored two CI gates to green": the `docs-check` schema-drift gate (the `[egress]` block was added in #33 but not mirrored into `configuration.md`) and the console-frontend `lint` gate (**25 eslint errors** across `Layout.tsx`, `OverviewPanel.tsx`, `TLSPanel.tsx`). Separately, this audit's `pnpm lint` shows 3 warnings for an *unused eslint-disable* in the generated `internal/admin/ui/coverage/sorter.js`.
- **Fact:** Two merge-gating checks were red on `main` and fixed the same day; the generated coverage output is being linted.
- **Inference:** The optional local hooks (`make hooks`, HP-04) either were not installed or do not run the full frontend lint / docs-check before commit, so drift reached `main`.
- **Why it matters:** Green-on-`main` is a trust signal; if hygiene gates routinely go red and are patched after the fact, the "CI parity" claim weakens.
- **Recommendation:** Add `internal/admin/ui/coverage/` to `.eslintignore`; make the `pre-push` hook run `pnpm lint` + `python scripts/docs-check.py` (or document clearly that it does not); consider a lightweight schema-drift check in the hook so a new `toml`-tagged config field can't merge without its `configuration.md` row.
- **Acceptance criteria:** `pnpm lint` = 0 warnings; hooks run frontend lint + docs-check; no same-day red-then-green on hygiene gates.
- **Effort:** S
- **Dependencies:** none

### Finding CQ-5 (carried, positive): Error-handling and resource conventions remain consistent

- **Severity:** low (informational) · **Provenance:** [Prior · Carried]
- **Fact:** Wrapped errors pervasive; a small sentinel set for control flow; resources implement `io.Closer` for generational cleanup; the new `egress.ErrBlocked` follows the same sentinel convention.
- **Recommendation:** Keep; document the sentinel-error contract in a short `CONTRIBUTING` note.
- **Effort:** S

**Positive net-new code additions (informational, [Net-new]).** `internal/egress` (guarded `DialContext`, DNS-rebinding re-validation, SNI/Host preserved); bounded metric cardinality (`TestMetricLabelPolicy`, `TestHTTPMethodLabelBounded`); structured create/delete patch-ops with guard tests (`patch_crud_test.go`); the four-state apply-outcome derivation with tests. All are on the green suite.

---

## 5. Feature maturity review

Legend: **Repo claim** from `docs/status.md` v1.32; **My verdict** = Agree / Agree-with-caveat / Overclaim.

| Feature | Repo claim | Evidence (verified this cycle) | Gap vs the *repo's own* RG-1/soak bar | My verdict | Next step |
| --- | --- | --- | --- | --- | --- |
| Core HTTP (static/proxy/FastCGI/vhosts/routing) | GA (soak ✅) | full build + `internal/handler` green; `jul check` runtime-valid; 8h + Phase 2A soak | none material | **Agree** | — |
| TLS + automatic HTTPS (ACME) | GA (soak ✅) | server TLS/ACME green; Phase 2A 8h (25% TLS mix) | ACME rotation-under-handshake test (QA-1) | **Agree-with-caveat** | add rotation test |
| Auth (CIDR/Basic/JWT/forward) | GA (soak ✅) | `internal/auth` green; 1h soak + reload-churn | none material | **Agree** | — |
| mTLS client auth | GA (soak ✅) | server mTLS green; Phase 2A client-cert path | none material | **Agree** | — |
| Rate + connection limiting | GA (soak ✅) | middleware+admin+conn green; 1h soak (12.5M req) | none | **Agree** | — |
| Compression (gzip/br/zstd) | GA (soak ✅) | middleware green; 1h soak (11.6M req) | none | **Agree** | — |
| Response cache (mem+disk) | GA (soak ✅) | cache green (20s in full suite); 1h soak; 14-row matrix + threat note | none material | **Agree** | — |
| Active health checks | GA (soak ✅) | `internal/upstream` green; 8h `/healthz` polled | none | **Agree** | — |
| Console | GA (soak ✅) | typecheck/eslint clean; **361 vitest**; 8h build-reachable | browser e2e (UI-1); no maturity signal (N-3) | **Agree-with-caveat** | browser smoke |
| Zero-config + `jul lint` | GA (soak ✅) | CLI verified; matrix+bench+fuzz | **soak-✅ rests on a validation script, not a soak** | **Overclaim (row-level)** | relabel row |
| NGINX importer | GA (soak ✅) | `migrate/nginx` green + fuzz; matrix+bench | **soak-✅ rests on a validation script, not a soak** | **Overclaim (row-level)** | relabel row |
| OTel tracing + log sinks | GA (soak ✅) | `internal/observability` green; Phase 2A; PII note | none | **Agree** | — |
| Secrets + redaction | GA (soak ✅) | redact+secrets green; Phase 2A; 8-row threat note | none | **Agree** | — |
| Service discovery | GA (soak ✅) | discovery green + live-CI (#46); keep-last-good | none material | **Agree** | — |
| WAF (Coraza+CRS) | GA (soak ✅) | `internal/waf` green + reload-churn; 1h soak | FP/bypass matrix depth | **Agree-with-caveat** | — |
| gRPC ↔ JSON transcoding | GA (soak ✅) | `internal/transcode` green; 1h isolated 14.2M req | **RG-1 wants 8h isolated (mandatory)** | **Agree-with-caveat** | RG-1 8h |
| Native gRPC passthrough + h2c | GA (soak ✅) | handler green + bench; 1h isolated 6.8M req | **RG-1 wants 8h isolated (mandatory)** | **Agree-with-caveat** | RG-1 8h |
| HTTP/3 over QUIC | GA (soak ✅) | http3 green; 1h isolated 12.99M req | **RG-1 wants 8h isolated** | **Agree-with-caveat** | RG-1 8h |
| WASM plugins | GA (soak ✅) | `internal/plugins` green incl. hardening+fuzz; Phase 2A | **RG-1 wants *isolated* 8h (only consolidated exists)** | **Agree-with-caveat** | RG-1 8h isolated |
| L4 stream proxy | GA (soak ✅) | `internal/stream` green + fuzz + UDP churn; 1h isolated | **RG-1 wants 8h isolated** | **Agree-with-caveat** | RG-1 8h |

**Overall (Fact + Inference).** On criteria 1–4 and 6–9, I agree these are GA: the evidence bundles are real and checked in — a substantial, verifiable advance since 2026-07-02. The disagreement is **criterion 5 only**: the blanket soak-✅ is *premature* for the four RG-1 targets (1h/consolidated < the project's own 8h-isolated bar) and *incorrect* for two rows (validation script ≠ soak). See **§9 / Finding N-2**.

---

## 6. CLI and operator UX review

**Grammar (Fact, verified).** `jul [flags]` (run), `jul check`, `jul lint`, `jul fmt`, `jul run --serve|--proxy`, `jul import nginx`, and — new this cycle — `jul healthcheck`, `jul version [-json]`, `jul completion <bash|zsh|fish|powershell|pwsh>`. `-help` prints a clean, well-structured usage block (verified). Exit codes are consistent (0 ok, 1 error). `check` reports "valid (structural + runtime)".

**Verified contracts.**
- **`jul version -json`** emits a stable key set (`product`, `version`, `commit`, `build_date`, `dirty`, `go_version`, `os`, `arch`) — clean and scriptable. From-source builds report `version: "dev"` with real `commit`/`build_date` from Go build info. **[Net-new positive]**
- **`jul lint -json`** now emits lowercase keys and string severity (`"severity":"warning"`) — **UX-1 confirmed resolved**.
- **`jul fmt`** output begins at `[global]` with no `mail`/empty-array preamble — **UX-2 confirmed resolved**.
- **`jul healthcheck`** probes the admin `/healthz|/readyz` (exit 0/1) and backs the Docker `HEALTHCHECK` on the shell-less distroless image. **[Net-new positive]**

**CI parity (Fact).** `Makefile` `ci-fast`/`ci-full` mirror the CI gates; optional Git hooks (`make hooks`) give local parity — but see **N-5** (the frontend lint / docs-check drift that reached `main` suggests the hooks aren't consistently catching everything).

### Finding UX-3: CLI is strong; the only residual gap is `run`/`import` runtime smoke coverage

- **Severity:** low
- **Provenance:** [Prior · Carried] (QA-1 remainder)
- **Area:** [`cmd/jul`](../../cmd/jul)
- **Evidence:** `cmd/jul` now has `import_test.go`, `version_test.go`, `healthcheck_test.go`, `cli_test.go`; a `run --serve/--proxy` runtime smoke (boot a server, hit it, shut down) and an ACME rotation test are not evident.
- **Recommendation:** Add a `run --serve` boot-and-serve smoke and an `import` golden asserting byte-stable canonical output; fold into the CI test lane.
- **Acceptance criteria:** both tests green in CI.
- **Effort:** M
- **Dependencies:** none

---

## 7. Console/UI review

**Verified state.** `internal/admin/ui`: `tsc --noEmit` clean; `eslint` 0 errors (3 warnings in a *generated* `coverage/sorter.js` — see N-5); **vitest 33 files / 361 tests pass** (up from 32/347). React + Vite + TS + Tailwind + TanStack Query + Zod + CodeMirror, prebuilt and embedded via `go:embed` (ADR-0006) — single-binary preserved.

**Navigation / IA (Fact).** Task-driven grouping (Operate / Configure / Change safely) with a command palette (⌘K) and search. Panels use uniform `isLoading`→spinner, `isError`→`PanelError`+retry, `EmptyState` for build-tag-absent features.

**Safe-change flow (Fact, improved).** Config edits go validate → diff → review → apply, serialized by `applyMu` with full preflight; history/rollback with confirm + diff preview. **New this cycle (N-P4):** every apply now resolves to exactly one of four explicit outcomes via `internal/admin/ui/src/lib/applyOutcome.ts` — **Applied and live**, **Applied — runtime reloading**, **Applied with a degraded subsystem** (names the failed subsystem, e.g. the `[[stream]]` L4 proxy), and **Restart required — not applied**. This is a real operator-legibility upgrade and directly addresses the async-reload ambiguity.

**Structured editing (Fact, improved).** Create/delete patch-ops (N-P3) mean routes/servers/pools can be created and removed through the diff-reviewed structured path instead of raw-TOML hand-offs.

### Finding N-3: The Console (and README) present every feature as production-ready with no maturity/soak signal

- **Severity:** low-medium
- **Provenance:** [Net-new]
- **Area:** `internal/admin/ui`, `README.md`, `docs/roadmap`
- **Evidence:** A grep for `MaturityBadge`/`Beta`/maturity in `internal/admin/ui/src` returns nothing; all features are labeled GA in the roadmap/README/status; the Console `Status` overview shows active/inactive but no maturity or soak status.
- **Fact:** With everything flipped to GA, the operator surface no longer distinguishes battle-tested-and-soaked features from RG-1-pending ones.
- **Inference:** This is the UI corollary of **N-2**: while issue #39 is open, an operator using the Console cannot see that L4 stream / WASM / HTTP/3 / gRPC are pending their isolated 8h soak.
- **Why it matters:** ADR-0004 makes the Console the honest operator surface; hiding the one live caveat undercuts that invariant.
- **Recommendation:** Reintroduce a per-feature maturity/soak indicator (a small badge on the Status overview + a README column) driven from `docs/status.md`, showing "GA" vs "GA · RG-1 soak pending" until #39 closes.
- **Acceptance criteria:** Console Status and README show soak status per feature; consistent with `docs/status.md`.
- **Effort:** S–M
- **Dependencies:** N-2 relabel

**Residual (Fact, [Prior · Carried]).** UI-1: a browser-level (Playwright) smoke against the built SPA is still absent; the Go over-the-wire e2e (`console_e2e_test.go`) covers the API contract but not the rendered app.

---

## 8. Documentation review

**Accuracy & currentness (Fact).** Docs are dated and current (`status.md`/`ga-push.md`/`roadmap` all v1.32, 2026-07-09; `CHANGELOG` 1.32.0). `scripts/docs-check.py` passes **897/0** and enforces link validity, TOML-fence parsing, version/date consistency, placeholder-URL/future-date detection, and — usefully — **config schema-drift** (every `toml`-tagged `Config` field must appear in `configuration.md`; this is what caught the missing `[egress]` row). This is a stronger docs CI gate than most projects have.

**Structure & audience fit (Fact).** `docs/index.md` learning paths + build-tag quick reference; per-feature deep dives for every capability; `configuration.md` full schema reference; a `troubleshooting.md` expanded this cycle (reloads, `restart_required`, degraded-subsystem apply, discovery, soak interpretation). Coverage spans new users → operators → integrators → contributors → security reviewers.

### Finding N-2: The soak claim is stated as "fully closed" while the project's own final soak gate (issue #39 / RG-1) is open

- **Severity:** high (governance / truthfulness)
- **Provenance:** [Net-new] (inverts prior DOC-1: evidence is now *published* but the *labels overshoot it*)
- **Area:** [`docs/status.md`](../status.md), [`docs/ga-push.md`](../ga-push.md), [`docs/roadmap/README.md`](../roadmap/README.md), [`docs/vision/README.md`](../vision/README.md), [`docs/soak-evidence.md`](../soak-evidence.md)
- **Evidence:**
  - `docs/roadmap` and `docs/vision` state the "soak test post-GA gate is **fully closed**"; `docs/status.md` marks **criterion 5 (soak) ✅** for all ~20 features and empties the "GA — soak pending" table.
  - Issue **#39 [SEQ-13][RG-1]** ("Final long-run isolated soak gate on latest architecture") is **open** (created 2026-07-08). Its **required runs** are four **8-hour isolated** soaks — L4 stream, WASM plugins, HTTP/3, and gRPC (transcoding + passthrough, "mandatory RG-1 evidence"). Its body: *"Final readiness claims must be based on soak evidence from the latest merged architecture, not historical runs from prior runtime shapes"*; task 5: *"Update `docs/status.md` and `docs/ga-push.md` claims strictly from RG-1 results."*
  - The cited soak evidence for those four is **1-hour isolated** (stream, HTTP/3, gRPC — below the 8h RG-1 target) or **consolidated-not-isolated** (WASM, via the Phase 2A 8h run).
  - Two rows are marked soak-✅ on the basis of **validation scripts**, not soak runs: zero-config (`test-zero-config.ps1`) and NGINX importer (`test-nginx-importer.ps1`) — contradicting `soak-evidence.md`'s explicit "the 20-second CI smoke and 5-minute release gate are **not** GA-soak evidence" / "smoke tests only" principle.
  - **All** soak evidence is **Windows/amd64 local, single-platform**; the one release-gate-duration proxy run (2026-07-03, 5m/32 workers) **FAILED** on Windows (`WSASocket`/ephemeral-port depletion, attributed to a client confound). No **Linux** (the production/distroless/systemd target) soak evidence is published.
- **Fact:** The GA labels are defensible against **ADR-0005's 1-hour minimum**; the *"fully closed"* wording and the blanket criterion-5-✅ are **not** defensible against the project's own **RG-1 (#39)** final gate, which is open.
- **Inference:** The maturity narrative was flipped to "all GA, soak fully closed" *ahead of* the gate that #39 says must be the sole basis for the claim. This is the exact "ambition outran the evidence" pattern the maturity model (ADR-0003) exists to prevent — now appearing in the docs rather than the code.
- **Why it matters:** Honest maturity is Jul.IA's signature trust asset. A demonstrably-false "fully closed" claim, discoverable by anyone reading issue #39, does more reputational damage than an honest "RG-1 pending" would.
- **Recommendation:**
  1. In `roadmap`/`vision`, replace "soak gate fully closed" with "soak gate: RG-1 (isolated 8h, latest architecture) **in progress** — see issue #39."
  2. In `status.md`/`ga-push.md`, split criterion 5 into per-feature soak status: ✅ for features with qualifying evidence; **"RG-1 pending"** for L4 stream / WASM / HTTP/3 / gRPC until #39 closes.
  3. Downgrade the zero-config and importer soak cells from ✅ to "functional-validated; soak n/a-or-pending" (a CLI/translation tool arguably needs no traffic soak — state that explicitly rather than claiming a soak that didn't run).
  4. Add a **Linux** soak run to the evidence set; annotate that the marquee proxy soak passes on Linux CI but hits a client-side port-exhaustion confound on Windows.
  5. On #39 close, update `status.md`/`ga-push.md` "strictly from RG-1 results" as the issue instructs.
- **Acceptance criteria:** No doc claims the soak gate is "fully closed" while #39 is open; every soak-✅ links a run that meets the scope's ADR-0005/RG-1 bar; validation-only rows are labeled as such.
- **Effort:** S (docs) + L (the actual RG-1 8h runs, tracked by #39)
- **Dependencies:** issue #39

**Recommended documentation refinements (Recommendation).** The tree is good; refine, don't rebuild: (1) add an **Evidence** landing page aggregating per-feature matrix/bench/threat-note/soak links (the GA bar made browsable, with soak status visible); (2) keep `soak-evidence.md` as the canonical soak ledger and make `status.md` link *specific* runs per row (it mostly does); (3) document a "maturity legend" that distinguishes GA-on-8-criteria from RG-1-soaked.

---

## 9. Specs, ADRs, and roadmap coherence

**ADRs match implementation (Fact, spot-verified).** ADR-0006 (embedded SPA) ↔ `internal/admin/ui` + `go:embed`; ADR-0004 (validate→diff→apply→rollback) ↔ admin write path; ADR-0002 (explicit adapters, GraphQL deferred) ↔ transcode-only; ADR-0005 (soak post-GA) ↔ `release.yml` blocking soak; ADR-0010 (Console RBAC) ↔ `docs/specs/console-rbac.md` (design only). ADR↔code fidelity remains excellent, **except** two ADRs now lag the code: **ADR-0008** (should be *Resolved (vendored gofast)* — CQ-4) and **ADR-0007** (still *Partial* — CQ-2 accurate).

**Roadmap/specs currentness (Fact).** `roadmap` shows Y1 11/11 and Y2 all GA; specs Year-1 "Shipped", Year-2 "shipped/GA", Years 3–5 "vision horizon". Internally consistent — but see the **coherence break** below.

### Finding N-2b: Roadmap/vision/status are internally consistent with each other but collectively inconsistent with the RG-1 gate

- **Severity:** high (same root as N-2) · **Provenance:** [Net-new]
- **Evidence:** `roadmap`, `vision`, and `status` all say "all features GA / soak fully closed"; issue #39 (RG-1) is open and its task list explicitly directs those same docs to be updated "strictly from RG-1 results."
- **Fact:** The docs agree with one another and disagree with the tracker that is supposed to gate them.
- **Recommendation:** Treat issue #39 as the single source of truth for soak status; make the three docs derive their soak claims from it (see N-2 recommendation).
- **Effort:** S · **Dependencies:** #39

**Next 3–6 months coherence (Inference).** Coherent *if* the effort is: (a) clear N-1; (b) run RG-1 (#39) and reconcile the docs; (c) land RBAC phase 1; (d) finish CQ-2. The "vision horizon" framing correctly parks Year-3+ work. The roadmap should state explicitly: *no new feature categories until RG-1 closes and the docs are reconciled.*

---

## 10. Testing and QA review

**Distribution (Fact, verified).** Lean `go test ./...` and full-tag `go test -tags "<full>" ./...` both **pass, zero failures**, across ~22 Go packages; frontend **33 files / 361 tests pass**. Fuzz targets exist across config, migrate/nginx, plugins, stream, upstream, transcode; benchmarks across cache/compression/ratelimit/redact/observability/plugins/stream/waf/migrate; soak lanes (`TestSoak`, `TestSoakUDPChurn`) + reload-churn leak lanes (auth, WAF). `goleak` guards `server`/`cache`/`plugins`/`stream`/`upstream`. CI adds a Windows lane, race (Linux), coverage floor, and bench/fuzz/soak smoke; `discovery-live.yml` runs the Consul convergence lane.

**Verified critical-path coverage (Fact).** Config validation/reload/rollback, admin bearer auth, patch CRUD guards, rate limiting, cache SWR/SIF, upstream health, auth incl. JWKS grace, transcode body-limit/error-map + streaming, plugin SSRF guards, TLS/mTLS, ACME OCSP graceful-fail, metric-label policy — all present and green.

### Finding QA-2: The "merge-safe" bar is close; residual gaps are runtime smoke, reload-under-load, and Linux soak

- **Severity:** medium
- **Provenance:** [Prior · Carried] (QA-1/SEC-2 remainders)
- **Area:** `cmd/jul`, `internal/server`, soak
- **Evidence:** Still missing: `run --serve/--proxy` runtime smoke; ACME rotation-under-concurrent-handshake; a reload-under-load-**with-traffic** soak (auth/WAF reload-churn cover engine rebuild but not traffic + repeated reloads together); Linux soak evidence (N-2).
- **Recommendation:** Add the three tests (UX-3 + reload-under-load) and one Linux soak profile; keep them in CI.
- **Acceptance criteria:** all present and green; a Linux soak artifact published.
- **Effort:** L
- **Dependencies:** N-1 (bump first), #39

**False-confidence risks (Inference).** The green suites + published Windows soak can read as "fully validated," masking (a) the absence of Linux soak on the actual deploy target and (b) the RG-1 8h-isolated gap. Coverage % should not be read as production-soak evidence.

---

## 11. Security and operations review

**Trust model (Fact, `SECURITY.md` + code).** Config + binary + filesystem trusted; downstream clients untrusted; configured upstreams trusted-by-default. Core invariant — request input never selects upstream/JWKS/cert/FastCGI root — holds.

**Verified protections (Fact).**
- **Egress allow-list (N-P1, net-new).** [`internal/egress`](../../internal/egress/egress.go) constrains config-driven auxiliary fetches (JWKS, forward-auth, Consul/K8s discovery) to operator-approved hosts/CIDRs, enforced at `DialContext` (covers redirects), with DNS-rebinding re-validation and SNI/Host preserved. Disabled by default (backward-compatible). This closes the residual config-driven SSRF shape the prior audit flagged. Documented in `docs/egress.md`.
- **Plugin egress + sandbox (Fact).** Allow-list enforcement, loopback-always-blocked, private/link-local blocked, DNS-rebinding checks; wazero sandbox with per-plugin memory/timeout caps; `.wasm` upload hardened (SEC-1: safe filename + containment).
- **Bounded metric cardinality (N-P2, net-new).** Client-controlled `method` folded to a fixed set (`other` catch-all); every client-derived metric label now bounded by construction; guarded by `TestMetricLabelPolicy`/`TestHTTPMethodLabelBounded`.
- **Admin auth (Fact).** Loopback-bound, bearer token, constant-time compare; `/healthz|/readyz` open; per-client rate limits + SSE cap; audit log of mutations; `pprof` mounted behind the same auth.
- **Secrets (Fact).** `${env|file|secret}` refs resolved at runtime, unresolved on disk/Console, global log redaction, atomic 0600 writes.
- **Release/hardening (Fact).** Distroless-nonroot + shell-less `HEALTHCHECK` + base-image **digest pinning** (N-P7); hardened systemd unit; Sigstore signing + attestation + SBOM.

**Prioritized security risks.**

### Finding N-1 (security lens): called `crypto/tls`/`os` stdlib CVEs — see §4

Re-stated here for the security backlog: bump to go1.26.5 (P0). This is the single highest-priority security item this cycle.

### Finding N-4: Admin control plane is a single shared bearer token; RBAC is design-only

- **Severity:** medium
- **Provenance:** [Net-new]
- **Area:** [`internal/admin`](../../internal/admin), ADR-0010, `docs/specs/console-rbac.md`
- **Evidence:** Admin auth is one shared bearer token with constant-time compare; ADR-0010 + the RBAC spec describe named principals / scoped-revocable tokens / deny-by-default / per-principal audit — but are explicitly *design only, no runtime behavior change* (#35).
- **Fact:** There is no per-operator identity, scoping, revocation, or attribution today.
- **Inference:** For a Console positioned as a differentiator and marked GA, single-token admin is a real GA-grade operations limitation: token compromise = full control plane; no least-privilege; audit records a single actor. Admin-token compromise also implies sandboxed-RCE via plugin upload (SEC-1 residual).
- **Why it matters:** Multi-operator teams and any compliance posture need named principals + revocation; "GA Console" implies this exists.
- **Recommendation:** Either land RBAC phase 1 (named principals + scoped hashed tokens per ADR-0010) before broad GA marketing, **or** document single-token admin as an explicit known limitation in `console.md`/`SECURITY.md` and gate the "GA Console" claim accordingly. Also add the SEC-1 capability-re-validation-on-activation test.
- **Acceptance criteria:** RBAC phase 1 shipped **or** the limitation documented and the Console claim scoped; capability-re-validation test green.
- **Effort:** L (RBAC) / S (document) · **Dependencies:** none

**Operations readiness (Inference).** Deployment (Docker digest-pinned + systemd + Windows service + `jul healthcheck`) is production-grade and improved this cycle. The gaps are evidence-of-evidence (Linux soak, N-2) and control-plane identity (N-4), not missing controls.

---

## 12. Product and marketing perspective

**Positioning (Recommendation).** Lead with what is *true and differentiated*: **"The lean, single-binary edge server with a first-class operations Console and a built-in gRPC↔JSON gateway."** Defensible and evidence-backed.

**Real marketing proof (Fact-backed).**
- Single static binary, no runtime deps — React Console embedded via `go:embed`.
- Operable-by-design Console — validate→diff→apply→rollback + four explicit apply outcomes; 361 UI tests.
- gRPC gateway — transcoding + passthrough + h2c.
- Supply-chain hygiene — signed/attested/SBOM releases, digest-pinned base images, **once N-1 is fixed**, a clean `govulncheck`.
- Honest maturity model — *provided N-2 is corrected*; the honesty itself is the asset.

**What NOT to overclaim (Recommendation).**
- Do **not** market "all features GA, soak fully closed" while issue #39 is open (**N-2**). Say "GA on eight criteria; final isolated soak (RG-1) in progress." This *strengthens* credibility with serious operators.
- Do **not** imply multi-operator admin/RBAC — it is design-only (**N-4**).
- Do **not** ship marketing that depends on the Windows soak numbers as if they were the production (Linux) target.
- Keep AI-gateway/fleet/mesh in "vision horizon."

**Adoption path / demos (Recommendation, unchanged and still strong).**
- **Demo 1 (ergonomics):** `jul run --serve .` → HTTPS in under a minute → Console route change with diff/apply/rollback.
- **Demo 2 (gateway):** REST client → gRPC backend via transcoding, no code.
- **Demo 3 (migration):** `jul import nginx nginx.conf` → run, with the unmapped-directive report.

**Release-announcement theme.** "Single binary, real Console, honest maturity" — but ship it *after* N-1 and N-2 so the announcement withstands scrutiny.

---

## 13. Prioritized backlog

### Immediate / P0

| ID | Title | Area | Sev | Impact | Effort | Deps | Acceptance | Owner |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| P0-1 (N-1) | Bump toolchain to **go1.26.5**; re-scan | go.mod/Dockerfile/README/CI | high | Clears 2 called stdlib CVEs incl. crypto/tls | S | — | `govulncheck` clean both profiles | backend/security |
| P0-2 (N-2) | Reconcile soak claim with RG-1 (#39): drop "fully closed"; per-feature soak status; relabel validation-only rows | docs | high | Restores maturity honesty | S | #39 | no "fully closed" while #39 open; every ✅ links a qualifying run | product/docs |

### Near term / P1

| ID | Title | Area | Sev | Impact | Effort | Deps | Acceptance | Owner |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| P1-1 (N-2/#39) | Run + publish the four RG-1 **isolated 8h** soaks on **Linux** | server/CI | high | The evidence the GA claim needs | L | P0-1 | 4 runs pass, artifacts linked | QA |
| P1-2 (N-3) | Restore per-feature maturity/soak signal in Console + README | admin/ui, README | med | Operator can see RG-1-pending | S–M | P0-2 | Console+README show soak status | frontend/docs |
| P1-3 (N-4) | RBAC phase 1 (named principals + scoped tokens) **or** document single-token limitation | admin/security | med | Control-plane identity | L / S | — | RBAC shipped or limitation documented | security |
| P1-4 (QA-2) | `run --serve` smoke + `import` golden + ACME rotation + reload-under-load soak | cmd/server | med | Closes merge-safe bar | L | P0-1 | tests green in CI | QA/backend |

### Medium term / P2

| ID | Title | Area | Sev | Impact | Effort | Deps | Acceptance | Owner |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| P2-1 (CQ-2) | Extract `buildHandlers`/`serve()` to `internal/app`; main.go <250; finalize ADR-0007 | cmd/jul, docs/adr | med | Composition-root testability | L | — | main.go <250; ADR-0007 Accepted | backend |
| P2-2 (CQ-4/ADR-0008) | Mark ADR-0008 *Resolved (vendored)*; document `third_party/gofast` provenance + scan | docs/deps | low | Vendor hygiene | S | — | ADR updated; provenance noted | backend |
| P2-3 (N-5) | `.eslintignore` coverage/; hooks run frontend lint + docs-check | ci/ui | low | Stops hygiene-gate drift | S | — | 0 eslint warnings; hooks cover both | frontend |
| P2-4 (UI-1) | Browser (Playwright) Console smoke against built SPA | admin/ui | low | Visual/contract e2e | M | — | one headless job green | frontend/QA |
| P2-5 (SEC-1 rem.) | Plugin capability re-validation-on-activation test | admin/plugins | low | Defense-in-depth | S | — | test green | security |

### Strategic bets (demand-gated, unchanged)

| ID | Title | Impact | Effort | Notes |
| --- | --- | --- | --- | --- |
| S-1 | GraphQL composition adapter | Broadens gateway | XL | Keep deferred (ADR-0002) |
| S-2 | Fleet/multi-node control plane | Open-core tier | XL | Vision horizon; not before RG-1 |
| S-3 | AI gateway / semantic cache MVP | Narrative upside | XL | Time-boxed bet only |
| S-4 | Plugin marketplace/registry | Ecosystem | L–XL | Gate on plugin GA + sandbox note |

---

## 14. Recommended roadmap evolution

**Next 1–2 weeks — restore claim/evidence integrity (freeze features).**
- *Security:* P0-1 (go1.26.5) + re-scan.
- *Governance/docs:* P0-2 (reconcile soak claim with #39); update ADR-0007/0008 status.
- *UX:* P1-2 (maturity signal) once P0-2 lands.

**Next 1–2 months — close RG-1 + merge-safe bar.**
- *Evidence:* P1-1 (four isolated 8h Linux soaks) → then flip labels strictly from RG-1.
- *QA:* P1-4 (runtime smoke, ACME rotation, reload-under-load).
- *Security:* P1-3 (RBAC phase 1 or documented limitation) + P2-5.
- *Marketing:* ship Demos 1–3 and the "single binary, real Console, honest maturity" announcement — *after* P0-1/P0-2.

**Next quarter — architecture debt + polish.**
- *Architecture:* P2-1 (main.go <250; ADR-0007). *UI:* P2-4 (Playwright). *Hygiene:* P2-2/P2-3.

**Longer-term horizon (demand-gated).** GraphQL adapter, fleet control plane, AI-gateway MVP, plugin registry — each only after validated demand and only after RG-1 closes and the docs are reconciled.

Lanes to keep distinct: **security** (N-1), **governance/docs** (N-2), **UX** (N-3), **control-plane** (N-4), **hygiene** (N-5), **architecture** (CQ-2), **features/marketing** — so integrity work is never traded for feature ambition.

---

## 15. File-by-file / area-by-area action list

- **`go.mod` / `Dockerfile` / `README.md` / `.github/workflows/ci.yml`** — bump to go1.26.5; re-run `govulncheck` (N-1).
- **`docs/status.md` / `docs/ga-push.md` / `docs/roadmap/README.md` / `docs/vision/README.md`** — remove "soak fully closed"; per-feature soak status tied to issue #39; relabel zero-config/importer soak cells (N-2).
- **`docs/soak-evidence.md`** — add a Linux soak run; annotate the Windows proxy-soak confound; link each `status.md` soak-✅ to a qualifying run (N-2).
- **`internal/admin/ui` + `README.md`** — restore a per-feature maturity/soak indicator (N-3).
- **`internal/admin` / ADR-0010** — RBAC phase 1 or document single-token admin as a known limitation; add plugin capability-re-validation test (N-4/SEC-1).
- **`cmd/jul/main.go`** — extract `buildHandlers`/`serve()` to `internal/app`; target <250 LOC (CQ-2).
- **`docs/adr/0007-composition-root-monolith.md`** — finalize once CQ-2 lands.
- **`docs/adr/0008-gofast-x-tools-technical-debt.md`** — mark *Resolved (vendored gofast)*; note `third_party/gofast` provenance (CQ-4).
- **`internal/admin/ui/.eslintignore` / `.githooks/`** — ignore `coverage/`; make hooks run frontend lint + docs-check (N-5).
- **`cmd/jul` / `internal/server`** — add `run` smoke, `import` golden, ACME rotation, reload-under-load soak (QA-2).
- **`internal/admin/ui`** — add a Playwright browser smoke (UI-1).

---

## 16. Uncertainties and verification gaps

**Verified locally (Windows/amd64, go1.26.4):** lean + full-tag `go build` (pass) and `go test ./...` (pass, zero failures, both profiles); `govulncheck` full tags (**2 called stdlib CVEs** — N-1); Console `typecheck` (clean) / `eslint` (0 errors, 3 warnings) / `vitest` (361 pass); `scripts/docs-check.py` (**897/0**); `jul version/check/lint/fmt/-help`; LOC probes; git working tree clean; issue #39 read via API.

**Not verified / open questions:**
1. **`-race` not run locally** — no CGO/gcc on this Windows box; the CI Linux race lane is assumed green, not observed here.
2. **gofmt not a local signal** — `git core.autocrlf=true` yields a CRLF working tree (main.go has 789 CRLF pairs), so `gofmt -l` flags nearly every file locally while `git status` is clean and the repo blobs are LF; CI gofmt (Linux, LF) is the authority. This is an environment artifact, **not** a formatting finding.
3. **CI/release logs not accessed** — workflow behavior inferred from YAML; green-CI status assumed.
4. **Soak not run to completion here** — the RG-1 8h isolated soaks (#39) were not executed as part of this audit; N-2 rests on the published evidence + the open issue, not on a fresh soak.
5. **Linux/macOS/arm64 not exercised** — Windows/amd64 only; HTTP/3 QUIC, UDP stream, and syslog sinks not validated on the production target platforms. This directly compounds N-2 (no Linux soak evidence exists).
6. **Console not driven in a browser** — reviewed via code + 361 unit tests + the Go e2e; rendered states/copy/false-affordances not visually exercised (UI-1).
7. **Security posture inferred, not pen-tested** — trust model, egress guards, admin auth, and plugin sandbox read from code/tests + `govulncheck`; no adversarial testing.
8. **`run`/`import` runtime fidelity** — only `check/lint/fmt/version/healthcheck` were exercised; importer translation fidelity and `run --serve/--proxy` runtime were not.
9. **Private issue tracker** — only issue #39 was read; the full #26 backlog tree (dependencies #27–#38) was not enumerated, so RG-1 dependency-closure status is taken from #39's own text.

None of these soften the two headline items — **fix N-1 (go1.26.5)** and **reconcile N-2 (soak claim vs RG-1/#39)** — which are the clearest path from "well-built and honestly-governed" back to "trustably, verifiably GA."

---

## Changelog

| Date | Ver | What changed | What stayed | Source |
| --- | --- | --- | --- | --- |
| 2026-07-16 | 1.2 | **§ 0.1 rewritten as v1.2 final-state table.** All sprint / week items logged: Week-1 (R-1 35 ESLint errors, R-2 cmdServe parity, R-3 fmt-diff tests, R-5 getting-started docs); Sprint-1 (C4 zeroconf fmt note, C5 admin floor 76→77, C6 WASM soak labels); Sprint-2 (R-4 TrafficControlEditor decomposition, C2 real-server Playwright E2E closing UI-1); Sprint-3 (C3 K8s kind CI lane); bonus TestHealthCheckTCP polling fix. **Only remaining open item: N-4 (RBAC Phase 1).** UI-1 closed by C2. Header updated to v1.2 / commit `6e266bd`. § 1 executive summary note updated. | Original §2–§16 audit body preserved verbatim. Strategic bets (E1–E5) remain demand-gated. | commits `02a0f1b`, `80915d8`, `4d760e1`, `8391577`, `6e266bd` |
| 2026-07-16 | 1.1 | § 0.1 reconciliation added. All July-9 findings mapped to current status: N-1 resolved (go1.26.5); N-2/N-2b resolved (4 RG-1 isolated 8h Linux soaks); CQ-2 resolved (main.go 91 LOC; ADR-0007 closed); N-3 resolved/moot; N-5 resolved (eslint, macOS CI, coverage floor); N-4 partially addressed (limitation documented, RBAC unshipped); UX-3/QA-2/SEC-1 confirmed already resolved in codebase. Additional Wave A/B work logged. | Original §2–§16 preserved verbatim. | commits `a708aff`, `56f4c40`, `bb89459`, `a0557e3`, `1bcf0b8`, `3bc40ab` |
| 2026-07-09 | 1.0 | New authoritative full-repository audit superseding the [2026-07-02 audit](previous_reviews/jul_full_repository_audit_2026-07.md). Full local re-verification (lean+full build/test, govulncheck, Console typecheck/eslint/vitest, docs-check, CLI). Reconciled all 16 prior findings (11 Resolved incl. CQ-4 via `gofast` vendoring, 5 Carried). New findings: N-1 (go1.26.5 stdlib CVEs), N-2 (soak-claim vs RG-1/#39), N-3 (no Console maturity signal), N-4 (single-token admin), N-5 (CI hygiene drift); net-new positives (egress, metric cardinality, patch CRUD, apply-outcome, digest pinning). | The maturity model (ADR-0003/0005), Console-first invariant (ADR-0004), protocol-adapter strategy (ADR-0002), and the "leanest serious edge/protocol gateway" positioning are unchanged. | issue #39; [status.md](../status.md); [ga-push.md](../ga-push.md); [soak-evidence.md](../soak-evidence.md); local verification |

---

## Round 6 re-audit (2026-07-17)

This document is a historical snapshot as of commit `6e266bd`. A subsequent
source-level re-audit on 2026-07-17 reviewed commits through `2a8c788` and
identified the findings below. Remediation is being applied on `main` in
ordered phases; this section records the register and status.

| ID | Severity | Title | Status |
| --- | --- | --- | --- |
| R6-01 | Critical | Process log redaction bound to empty startup snapshot | ✅ Resolved in Phase 0 — `redact.Writer` now reads live global state per write |
| R6-06 | High | Old-generation secrets pruned before drain | ✅ Resolved in Phase 0 — union old+new state installed at Publish; pruned in retire callback |
| R6-07 | Medium/High | Resolution/validation do not use one immutable candidate | ✅ Resolved in Phase 0 — `ReloadPlan.Validate` validates `EffectiveConfig`; `config.Resolve` is idempotent |
| R6-13 | Medium | Two startup fingerprints can diverge | ✅ Resolved in Phase 0 — one authoritative `startupFP` computed after startup consumers and shared |
| R6-02 | High | Restart-required registry entries not enforced | ✅ Resolved in Phase 1 — all `RestartRequiredClass` entries now `StartupConsumed`; invariant test added |
| R6-03 | High | Path/content fingerprint heuristic creates false restart signals | ✅ Resolved in Phase 1 — field-specific canonicalization; paths compared as paths, TLS files by content |
| R6-05 | High | TLS/HTTP3/h2c fingerprints overwrite virtual hosts on same address | ✅ Resolved in Phase 1 — fingerprints now aggregate per SNI/server_names within each listen address |
| R6-09 | Medium | Lifecycle registry internally contradictory | ✅ Resolved in Phase 1 — `worker_threads`/`redact_min_secret_length` are `HotReloadClass`; YAML `gated_by` updated |
| R6-14 | Medium | Pending-restart hides resolution errors | ✅ Resolved in Phase 1 — `PendingRestartCheck` returns `resolve_error` instead of nil |
| R6-04 | High | Service discovery frozen by generation-scoped snapshots | ✅ Resolved in Phase 2 — per-request snapshots let discovery converge on the next request |
| R6-08 | High | Structured diff not schema-complete | ✅ Resolved in Phase 4 — `scripts/dump-schema-leaves.go` reflects `config.Config` into dotted TOML paths; docs-check verifies every non-container schema leaf is covered by the lifecycle registry or an exemption; all 80 previously-uncovered runtime leaves now have registry + YAML entries (compression, waf, plugins, locations, stream routes, upstream discovery, etc.); 1249/0 docs-check passes |
| R6-10 | Medium | docs-check validates mirroring, not enforcement truth | ✅ Resolved in Phase 3 — `StartupConsumed` exported and enforced; registry invariant test added |
| R6-11 | Medium | Feature-status validation presence-only | ✅ Resolved in Phase 3 — docs-check now parses status.md rows and compares criteria/doc per feature |
| R6-12 | Medium | Authoritative audit stale and false | ✅ Resolved in Phase 3 — this document is now marked historical; live status tracked in commits |
| R6-15 | Low/Medium | Publish described as atomic | ✅ Resolved in Phase 3 — documentation describes Publish as an ordered commit boundary |
| R6-16 | Low | Stale comments and legacy rollback code | ✅ Resolved in Phase 3 — removed obsolete save/restore around `ValidateRuntimeConfig` |

**Remaining work:** All Round 6 findings are resolved. Final evidence gathered: `go test ./...`, `go test -tags full ./...`, `go test -race` on reload-critical packages, and `python3 scripts/docs-check.py` (1249 passed, 0 failed).
