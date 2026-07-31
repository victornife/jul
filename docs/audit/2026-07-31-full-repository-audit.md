# Jul.IA — Full Repository Audit

**Date:** 2026-07-31
**Auditor scope:** architecture, code quality, product/roadmap, specs/ADRs, feature maturity,
docs, Console/CLI UX, tests/QA, security/operations, marketing readiness.
**Method:** evidence-based and adversarial. Every non-trivial claim was checked against source,
tests, or a live command. Live toolchain runs were executed on this checkout
(branch `main` @ `e8865615`, Go 1.26.5 linux/arm64). Findings separate **Fact** (directly
supported), **Inference** (reasoned interpretation), and **Recommendation**.

> **Live verification executed for this audit (not doc assertions):**
> lean + full-tag `go build`/`go vet` (clean); lean + full-tag `go test ./...` (all pass);
> `docs-check.py` (1346 pass); `check-full-tags-sync.py` (OK); `gofmt` (1 violation, see F-01);
> CLI exercised (`version`/`capabilities`/`check`/`lint` + negative paths); per-package coverage;
> Console `typecheck`/`eslint`/`vitest` (539 tests pass) + `vite build` + embedded-asset drift check;
> `govulncheck` lean + full (0 called vulns); 15s soak smoke (proxy + UDP, bounded, 0 errors).
> **Not verified (see §16):** golangci-lint/staticcheck/addlicense (not installed locally),
> `-race`, multi-OS matrix, long-hour soak, manual browser walkthrough, penetration testing.

---

## Remediation status (post-audit, 2026-07-31)

This audit was acted on immediately after it was written. The table records what
has landed on `main` since; open items carry forward to the backlog in §13.

| Finding | Status | Commit | Notes |
|---|---|---|---|
| **F-01** gofmt / gate integrity | ✅ Resolved | `1f4488d1` + hooks activated | `cmd/jul/capabilities.go` reformatted; `gofmt -l` is clean tree-wide. **Corrected root cause:** the canonical `.githooks/pre-commit` **already** runs `gofmt` and CI already has a `gofmt` job — but this clone ran a stale hand-installed `.git/hooks/pre-commit` (no gofmt) with `core.hooksPath` unset, so the canonical gate never fired. Fixed by activating it (`make hooks`). |
| **F-02** maturity vocabulary | ✅ Resolved | `1f4488d1` | "Delivery state vs. maturity" table added to [status.md](../status.md) (implemented → merged → released → soaked → audit-closed) and reconciled across roadmap, `CHANGELOG.md`, `feature-status.yaml`, and README. |
| **F-04** SECURITY.md RBAC drift | ✅ Resolved | `1f4488d1` | RBAC now documented as delivered opt-in `[admin.rbac]` (Phase 3); only interactive token management is future. |
| **F-05** capabilities under-reports | ✅ Resolved | `73466263` | `jul capabilities` now reports all 13 optional subsystems (verified: lean all-false, full all-true) via tag-gated files, plus a regression test. |
| **F-06** `lint -strict` exit-code overload | ✅ Resolved (documented) | `73466263` | The global exit-code contract now documents code 2 = usage error **or** `lint -strict` warnings. Behavior is intentionally unchanged: `2 = strict warnings` is documented in configuration.md/getting-started.md/specs, so harmonizing the docs was the proportionate, non-breaking fix. |
| **F-07** ga-push.md version stamp | ✅ Resolved | `1f4488d1` | Bumped 1.32 → 1.37. |
| **F-08** config-audit closure | ◐ Annotated (closure still pending) | `1f4488d1` | status.md/roadmap now mark the subsystem "remediated, closure pending"; the exact-SHA CI + two sign-offs remain the real closing step (unchanged). |
| **F-03** security-pkg coverage | ☐ Open | — | plugins/waf/rbac coverage floors + negative tests — see §13 (P2). |

**Dependabot advisories (triaged 2026-07-31).** GitHub reported **4 high**
dependency advisories on the default branch. Two are fixed — `klauspost/compress`
→ 1.18.7 and, via pnpm overrides, `brace-expansion` → 5.0.8 and `postcss` → ≥8.5.18
(build-time only). Two are **accepted with rationale**: `golang.org/x/crypto`
GO-2026-5932 (no patched release; `govulncheck` confirms it is not called) and
`react-router` GHSA-qwww-vcr4-c8h2 (CSRF in RSC/server mode, unused by this
client-only SPA; no compatible `react-router-dom` 8.x exists). `govulncheck`
reports 0 called vulnerabilities.

**Remaining follow-up specifically for F-01.** The gate itself already exists —
the canonical `.githooks/pre-commit` runs `gofmt` (see `CONTRIBUTING.md`) and CI has
a `gofmt` job. The violation slipped in only because this clone ran a stale
hand-installed `.git/hooks/pre-commit` with `core.hooksPath` unset; the canonical
hooks are now activated (`make hooks`). Residual follow-up: confirm the CI `gofmt`
job is a **required** status check on `main`, and consider a setup guard that warns
when `core.hooksPath` is unset so a stale local hook cannot mask the gate again.

---

## 1. Executive summary

**Overall maturity: high for a single-node edge server; genuinely strong engineering discipline.**
Jul.IA is not a "collection of features" — it is a coherent, well-tested, well-documented Go edge
server with unusually mature governance (ADRs, an audit register, a maturity model, a soak gate).
Live checks corroborate most of its claims: everything builds and tests green on both the lean and
full tag sets, the Console has 539 passing frontend tests and builds to embedded assets that match
the committed tree, `govulncheck` finds no called vulnerabilities, and the soak gate really does
enforce bounded goroutines/heap and zero errors.

**Strongest areas (Fact-backed):**
- **Reload/apply architecture** — a single `ReloadPlan` transaction (ADR-0011), a lifecycle registry
  that single-sources restart classification, and a preflight gate that dry-runs handlers/stream/
  listeners before persisting. Extensively tested in `internal/app` (~20 test files).
- **Phase 4 egress allow-list** — textbook SSRF defense-in-depth: dial-time enforcement, DNS-rebinding
  rejection, secret-safe rate-limited block logs, and a clean architectural seam so `internal/auth`
  and `internal/upstream` never import the egress package.
- **Console** — sober, consistent, tested; systematic loading/error/empty states, confirm dialogs for
  dangerous operations, and an anti-lockout guard on admin-reachability edits.
- **Governance** — the reopened configuration audit refuses to mark findings "Closed" without
  exact-SHA CI + two human sign-offs. That is a maturity signal, not a weakness.

**Riskiest areas:**
- **A "maturity vocabulary" gap.** The repo advertises everything as "GA / delivered," but the
  newest hardening (Phase 4 egress) is on `main` yet under CHANGELOG `[Unreleased]`, Phase 2/3 are
  "in remediation," and the config audit is not formally Closed. *Implemented ≠ merged ≠ released ≠
  soak-closed ≠ audit-closed.* Operators and reviewers cannot currently tell these apart from the
  headline docs. (§2, §9, F-02)
- **A merge gate is being bypassed.** `cmd/jul/capabilities.go` is committed to `main` in a
  non-`gofmt`-clean state, so `make format-check` (part of `ci-fast`/`ci-full`/`ci-pr`) would fail.
  This directly undercuts the "all-green CI" narrative. (F-01)
- **Security-sensitive packages have the lowest test coverage and no dedicated floors:** plugins
  70.1%, WAF 71.4%, RBAC 75.8% — guarded only by the global 65% floor. (F-03)
- **Documentation drift on a security feature:** `SECURITY.md` still calls RBAC a "future milestone"
  though it shipped in Phase 3. (F-04)

**Is core HTTP + Console GA-soak ready?** *Yes, on evidence.* Core HTTP, TLS/ACME, proxy/routing,
reload, and Console are the most tested and most soaked surfaces; the live soak smoke and full test
run support the GA-soak posture. The blockers below are about *truth-in-labeling and gate hygiene*,
not about core stability.

**Which advanced features should stay Beta-in-spirit?** WASM plugins, WAF, and RBAC *token
management* — not because they are broken, but because their coverage/negative-test depth and (for
RBAC token management) feature-completeness do not yet match the "GA" label's implied bar.

**Top 5 things to fix next** *(updated 2026-07-31 — see the Remediation status section above):*
1. ~~**F-01** — gofmt/format-check gate~~ **Done** (`1f4488d1`; canonical hooks activated via
   `make hooks`); residual: confirm the CI `gofmt` job is a required status check on `main`.
2. ~~**F-02** — maturity vocabulary + doc reconciliation~~ and ~~**F-04** — `SECURITY.md` RBAC
   status~~ **Done** (`1f4488d1`).
3. **F-03** — add dedicated coverage floors + negative tests for `plugins`, `waf`, `rbac` *(now the
   top open engineering item)*.
4. **F-08** — run the reopened config-audit closure (exact-SHA CI + two sign-offs) and land the two
   deferred tests (R9-14.4/R9-14.5). *(Now annotated "closure pending"; formal closure outstanding.)*
5. **Dependabot + UX** — triage/bump the 4 high advisories on the default branch, and add a Console
   "preview" affordance for RBAC token management.

---

## 2. What Jul.IA is today

**Fact.** Jul.IA is a single-binary, TOML-configured, NGINX-inspired HTTP edge server written in pure
Go (CGO-free), with opt-in build tags for advanced protocols. Composition root is `internal/app`
(`cmd/jul/main.go` is ~96 LOC and only wires signals + config + `app.Serve`). The top-level config
tables (`internal/config/schema.go`) are `[global]`, `[[servers]]`, `[[upstreams]]`, `[cache]`,
`[admin]`, `[compression]`, `[rate_limit]`, `[egress]`, `[observability]`, `[waf]`,
`[plugins.<name>]`, `[[stream]]` — and [docs/configuration.md](../configuration.md) documents exactly
these.

**Core value proposition.** "Zero-config HTTPS in under a minute, then grow into a serious protocol
gateway without leaving a single binary." The lean build already gives core HTTP, TLS, auth, rate
limiting, health checks, gzip, response cache, reload transaction, and the Console. The full build
adds brotli/zstd, ACME, OTel, gRPC (passthrough + transcode), HTTP/3, importer, WASM plugins, L4
stream, Consul/Kubernetes discovery, and WAF.

**Target users (from [docs/vision/README.md](../vision/README.md)).** Small-to-medium platform/infra
teams who want NGINX-class capability with Caddy-class ergonomics on one node. Explicit anti-personas:
hyperscale CDN operators, service-mesh seekers, teams already standardized on a larger platform.

**Feature surface & maturity split.** 20 shipped features labeled GA (see §5). Years 3–5 (Fleet,
OIDC/SSO, distributed state, AI Gateway, Cloud, GSLB, GraphQL) are explicitly **demand-gated**. An
AI-Gateway MVP is a time-boxed bet behind an `ai` tag.

**Inference.** The product identity is coherent and refreshingly disciplined — it knows what it is
(single-node edge) and, crucially, what it is *not yet*. The one identity risk is not scope creep;
it is **over-uniform labeling**: the maturity model is strong on paper but collapses several distinct
states ("implemented," "merged," "released," "soaked," "audit-closed") into one public word, "GA."

**What it should not try to be yet.** Distributed/fleet control plane, external-IdP broker, or AI
gateway — all correctly deferred. Do not let the AI-Gateway bet pull core focus before the maturity
vocabulary and gate hygiene issues below are closed.

---

## 3. Architecture assessment

**Entry points.** `cmd/jul/main.go` → `cmd/jul/cli.go` `dispatchSubcommand` (subcommands) or legacy
flags → `app.Serve`. Windows Service Control Manager path is detected first. Clean and minimal.

**Package/domain map (Fact).** Composition in `internal/app` (`serve.go`, `runtime.go`, `wiring.go`,
`factory.go`, `preflight.go`, `admin_deps.go`); config in `internal/config`; runtime listeners/TLS in
`internal/server`; routing in `internal/router`; request handlers in `internal/handler`; upstream
pools/discovery/health in `internal/upstream`; middleware in `internal/middleware`; control plane in
`internal/admin` (+ React SPA in `internal/admin/ui`); features in `internal/{cache,auth,transcode,
stream,plugins,waf,egress,observability,tracing,rbac,redact}`; restart classification in
`internal/lifecycle`.

**Config lifecycle (Fact).** parse (`go-toml/v2`) → `applyDefaults` → `Validate` (accumulates all
errors via `errors.Join`, includes build-tag gating so a `[waf]` block without the `waf` tag is
*rejected at preflight*, not ignored) → `Lint` (best-practice warnings) → preflight → apply.

**Runtime lifecycle (Fact).** Four-phase init (subsystems → handler factory → preflight gate → admin
+ start), then a reload loop on SIGHUP / file-watch / admin API. Graceful shutdown bounded by
`shutdown_timeout`.

**Reload/apply model (Fact, strong).** ADR-0011 `ReloadPlan` is a single side-effect-free transaction
owning candidate state from resolve through publish; `internal/lifecycle` single-sources hot-reload
vs restart-required classification; ADR-0013 defines a managed-apply terminal ledger (single terminal
object per apply, 512-result/1h retention, `applied_degraded` committed, emergency snapshot on
failure). Admin apply result codes are explicit (`internal/admin/config_apply.go`): 200 applied,
202 saved/in-flight, 400 validation, 408/504 timeout, 409 conflict, 503 unavailable.

**Admin/control-plane (Fact).** Admin API + embedded React SPA; managed apply goes through preflight
(resolve/validate/TLS/handlers/stream/listeners dry-run) before persisting; diff highlights
restart-required changes from the lifecycle registry; history + rollback.

**Extensibility points.** WASM plugins (wazero, `jul-abi/v1`), service discovery adapters
(DNS/Consul/K8s), gRPC transcoding descriptors, egress observer seam.

**Intentional vs accidental (Inference).** Overwhelmingly *intentional*. Evidence: ADRs map to code
(0007 composition root — verified `main.go` <100 LOC; 0011 ReloadPlan; 0013 ledger), and the audit
register (Rounds 9–13) ties each fix to a test. The one architectural smell is scale, not design:
`internal/admin` is very large (100+ files) and `internal/app` carries a lot of apply/finalize logic
(~20 test files). These are *decomposed by seam* (per [docs/architecture.md](../architecture.md)) and
well-tested, so this is "large but governed," not a god package — but it is the area most at risk of
becoming one.

**Coupling risk.** The egress seam is a model to emulate: `internal/auth` and `internal/upstream`
depend on a `DialFunc`/client alias and *never import* `internal/egress` (verified by grep — only
`internal/app`, `internal/plugins/host.go`, and test files import it). Keep this discipline as more
cross-cutting policies (e.g., future quotas) are added.

---

## 4. Code quality findings

### Finding F-01: A committed file on `main` is not `gofmt`-clean → advertised merge gate bypassed
- **Severity:** high (process/trust) / low (mechanical)
- **Area:** `cmd/jul/capabilities.go`; `Makefile` `format-check`; `.github/workflows/ci.yml`
- **Evidence:** `gofmt -l` flags `cmd/jul/capabilities.go` (struct-tag alignment on
  `capabilitiesOutput`); `git diff HEAD` is empty for that file (the unformatted version *is* the
  committed state at `e8865615`). `make ci-fast` runs `format-check`.
- **Fact:** `main` contains a `gofmt` violation that `format-check` would reject.
- **Inference:** Either the format gate is not actually enforced in GitHub CI, or commits are landing
  without it (hooks not installed / `--no-verify`). Ironically, the reopened config audit's own
  standard is "a pre-commit hook is not CI."
- **Why it matters:** It falsifies the "all shipped features pass the gate / all-green CI" narrative
  that underpins the GA claims. If one gate can be bypassed silently, reviewers cannot trust the
  others.
- **Recommendation:** Run `gofmt -w cmd/jul/capabilities.go`; add a CI assertion that fails on any
  `gofmt -l` output; confirm the `format-check` job is required for merge to `main`.
- **Acceptance criteria:** `gofmt -l $(git ls-files '*.go')` is empty; a CI run on `main` shows
  `format-check` green and required.
- **Effort:** S
- **Dependencies:** none
- **Status (2026-07-31):** ✅ File reformatted in `1f4488d1`; `gofmt -l` is now clean tree-wide.
  Root cause corrected: the canonical `.githooks/pre-commit` already runs `gofmt` and CI has a
  `gofmt` job; the violation slipped in only because a stale hand-installed `.git/hooks/pre-commit`
  ran with `core.hooksPath` unset. Canonical hooks re-activated via `make hooks`. Residual
  follow-up: confirm the CI `gofmt` job is a required status check on `main`.

### Finding F-03: Security-sensitive packages have the lowest coverage and no dedicated floors
- **Severity:** medium
- **Area:** `internal/plugins`, `internal/waf`, `internal/rbac`, `internal/handler`; CI coverage job
- **Evidence:** measured full-tag coverage this audit — plugins **70.1%**, waf **71.4%**, rbac
  **75.8%**, handler **76.6%**; vs config 85.7, auth 91.6, egress 86.9, server 80.6, upstream 84.1.
  CI defines per-package floors only for config/server/auth/admin; plugins/waf/rbac are guarded only
  by the global 65% floor.
- **Fact:** The three most security-critical opt-in subsystems (sandbox, firewall, access control)
  are the least covered and least floored.
- **Inference:** Negative/abuse-path testing (malicious WASM, WAF evasion, RBAC deny-path) is thinner
  than the "GA" label implies.
- **Why it matters:** These are exactly the packages where an untested branch becomes a security
  incident.
- **Recommendation:** Add dedicated coverage floors (target ≥80% plugins/waf, ≥85% rbac) and
  negative-path tests: oversized/trapping guests and denied `fetch`; WAF bypass/evasion corpora;
  RBAC deny-by-default and scope-escalation attempts.
- **Acceptance criteria:** New floors enforced in `ci.yml`; each package gains ≥5 negative tests;
  coverage meets targets.
- **Effort:** L
- **Dependencies:** none
- **Status (2026-07-31):** ☐ Open — the top remaining engineering item (P2).

### Finding F-05: `jul capabilities` under-reports compiled features
- **Severity:** low
- **Area:** `cmd/jul/capabilities.go`
- **Evidence:** a full-tag binary's `capabilities -json` reports only `waf`, `stream_proxy`,
  `wasm_plugins` as features, though it was built with 13 tags (acme/grpc/http3/otel/brotli/zstd/
  importer/consul/kubernetes not surfaced).
- **Fact:** The machine-readable capability report omits most optional subsystems.
- **Why it matters:** Automation/operators cannot answer "is ACME/HTTP-3/gRPC compiled in?" from the
  documented capability contract — the exact use case `capabilities` exists for.
- **Recommendation:** Emit every optional subsystem as a boolean (present/absent), keeping the
  additive-only JSON contract.
- **Acceptance criteria:** `capabilities -json` lists all build-tag-gated subsystems; a test asserts
  the full-tag set.
- **Effort:** S
- **Dependencies:** none
- **Status (2026-07-31):** ✅ Resolved in `73466263` — all 13 subsystems reported via tag-gated
  `capabilities_tag_*.go` files (verified lean = all-false, full = all-true) with a regression test.

### Finding F-06: CLI exit-code semantics overloaded for `lint -strict`
- **Severity:** low
- **Area:** `cmd/jul/cli.go`
- **Evidence:** the top-level usage/`capabilities` contract defines exit `2 = usage or config error`,
  but `cmdLint` uses exit `2` for "warnings present under `-strict`."
- **Fact:** Two distinct conditions share exit code 2.
- **Why it matters:** A CI script cannot distinguish "you passed a bad flag" from "your config has
  lint warnings" — a real ambiguity for automation.
- **Recommendation:** Either document the `-strict` exception explicitly in the global contract, or
  reserve a distinct non-zero code for strict-warning failures.
- **Acceptance criteria:** Exit-code contract is unambiguous and covered by a CLI test.
- **Effort:** S
- **Dependencies:** none
- **Status (2026-07-31):** ✅ Resolved in `73466263` by documenting the exception (recommendation
  option 1). Code 2 now reads "usage error or `lint -strict` warnings" in the global contract;
  behavior is unchanged because `2 = strict warnings` is already documented in
  configuration.md/getting-started.md/specs and a code change would break that contract.

### Positive code-quality observations (Fact)
- **Egress** (`internal/egress/policy.go`, `blocklog.go`): nil-policy-is-disabled (nil-safe guards),
  dial-time enforcement, `mixed_dns_answers` rejection (DNS-rebinding safe), bounded/rate-limited
  secret-free block logs. Reference-quality.
- **Plugin host** (`internal/plugins/host.go`): bounded request/response buffers (1 MiB/8 MiB),
  fail-closed on oversize, per-request invocation never shared across goroutines, response flushed
  only after the guest returns, distinct `-5` guest code for global-egress denial.
- **Error handling:** `Validate` accumulates all errors; managed apply forbids swallowed callback
  errors (per reopened-audit standards); no `context.Background()` in bounded managed prep.
- **Resource lifecycle:** graceful shutdown drains and is bounded; soak smoke confirms no goroutine
  leak under proxy and UDP churn.

---

## 5. Feature maturity review

Coverage figures below are measured this audit (full tags). "Repo label" is the claim in
[docs/status.md](../status.md)/[feature-status.yaml](../feature-status.yaml).

| Feature | Repo label | Evidence (verified) | Auditor verdict |
|---|---|---|---|
| Core HTTP/static/proxy/routing | GA | full tests pass; soak proxy 30k req/0 err; router/handler tested | **Agree — GA** |
| TLS/ACME | GA | `internal/server` 80.6%; acme/ocsp guarded tests; egress-aware | **Agree — GA** (ACME live-issuance not re-run here) |
| Auth (CIDR/Basic/JWT/forward) | GA | 91.6% cov; fuzz `auth_fuzz_test`; reload-churn | **Agree — GA** |
| Rate limiting | GA | middleware tested; bench | **Agree — GA** |
| Compression (gzip/brotli/zstd) | GA | middleware tests per encoder; full build | **Agree — GA** |
| Cache | GA | disk+memory tests; known-limitations honest (no tag purge) | **Agree — GA** |
| gRPC passthrough (+h2c) | GA | `internal/transcode`/`handler` tests pass full | **Agree — GA** |
| gRPC↔JSON transcoding | GA | path-template fuzz; reflection candidate tests | **Agree — GA** |
| Console/admin | GA | 539 vitest pass; build+embedded in sync; e2e specs | **Agree — GA** (RBAC token mgmt caveat) |
| WASM plugins | GA | sandbox solid; **cov 70.1%**, fuzz present | **Qualify — GA-capable, coverage/negative depth below GA bar (F-03)** |
| WAF | GA | ModSecurity/CRS; **cov 71.4%**; off by default | **Qualify — GA-capable, needs evasion-corpus tests (F-03)** |
| L4 stream proxy | GA | soak UDP cap enforced, no leak; 81.4% | **Agree — GA** |
| Service discovery | GA | DNS/Consul/K8s; candidate isolation tests | **Agree — GA** (live Consul/K8s not re-run here) |
| Observability/logging/tracing | GA | metrics/logtail/stats tests; OTel stub+real | **Agree — GA** |
| Importer (NGINX) | GA | `internal/migrate/nginx` tests pass | **Agree — GA** (breadth of nginx surface unverified) |
| Secrets/redaction | GA | `internal/redact` tests; startup-redaction isolation | **Agree — GA** |
| HTTP/3 | GA | UDP bind preflight; full build/tests | **Agree — GA** |
| mTLS | GA | CRL/SAN allow-list; `$ssl_client_*` | **Agree — GA** |
| RBAC (`[admin.rbac]`) | GA (opt-in, Phase 3) | `internal/rbac` real; e2e; **cov 75.8%**; token mgmt "future" | **Qualify — model GA; token management incomplete; SECURITY.md drift (F-04)** |
| Egress allow-list (Phase 4) | "delivered" | code excellent; 86.9% cov; on `main` but `[Unreleased]` | **Agree on implementation; not yet released (F-02)** |

**Missing GA criteria, concretely:** plugins/waf need negative-path/evasion tests and floors; RBAC
needs token-management completion or an honest "preview" label; egress needs a release (it is merged
but unreleased).

---

## 6. CLI and operator UX review

**Fact.** Grammar is clean and scriptable: `serve|check|healthcheck|lint|fmt|run|import|version|
capabilities|completion`, with legacy `-config/-check/-version` preserved behind deprecation notices.
Exit contract (0 success / 1 error / 2 usage-or-config) is documented in `usage()` and re-emitted by
`jul capabilities`. JSON + quiet + strict modes exist. Handlers are testable via package-level
`stdout`/`stderr` swap; `cli_test.go` has 21 test functions.

**Verified live:** `version` (commit/build/go/platform), `capabilities -json`, `check` (valid → 0),
`lint -json` (structured warning → 0), missing file → 1, bad flag → 2, invalid config (unknown
upstream) → 1. Behavior matches the contract.

**Issues:** F-05 (capabilities under-reports features), F-06 (exit-code overload under `-strict`).

**Onboarding.** Strong: a bare `jul` with no config prints zero-config guidance
(`jul run --serve .` / `--proxy`) instead of a raw open error.

**Recommendation.** Adopt this as the canonical grammar (it already is consistent); fix F-05/F-06;
add a `--version`-style machine field to `check`/`lint` JSON for schema evolution; ensure `jul fmt`
is wired into a CI check so config examples in docs stay canonical.

---

## 7. Console/UI review

**Fact.** React 19 + TS + Vite + Tailwind + TanStack Query + CodeMirror + Zod. Feature modules under
`internal/admin/ui/src/features/`: apps, config, history, observability, operations, overview,
plugins, routes, search, security (RBAC/audit/TLS/WAF), streams, tls, traffic-controls, transcode,
wizard. **Verified:** typecheck + ESLint clean; **539 Vitest tests across 41 files pass**; `vite
build` succeeds and the emitted `internal/admin/assets/dist` matches the committed tree (ADR-0006
drift guard holds); 2 Playwright e2e specs exist.

**UX quality (Fact).** Systematic `isLoading`/`isError`/`isPending` handling with shared
`EmptyState`/`Loading` components (171 matches across 30 files); a reused `ConfirmDialog` for
dangerous operations (rollback, and admin-reachability edits that could "lock you out"); consistent
`jul-danger` styling; a focus-trap accessibility test. The reopened-audit work hardened the
apply/rollback truthfulness: the Console polls the exact per-ID ledger record (never the global
`last_managed_apply`), never claims "Applied and live" without correlated terminal proof, and shows a
four-state `sourceView` so a candidate is never mistaken for live config.

**Bundle.** Main 472 kB (106 kB gz), CodeEditor 294 kB (95 kB gz, code-split), vendor 224 kB
(72 kB gz). Reasonable; CodeMirror is correctly split out.

**Gaps.**
- **Preview affordance (added 2026-07-31).** Previously the Console carried zero Beta/experimental/
  preview labels — everything presented as fully GA. The shared `MaturityBadge` now has a `preview`
  level, and the Security panel labels interactive RBAC *token management* as **Preview** (planned;
  not yet available). Extend the same badge to any other not-fully-complete surface.
- **e2e not run in this audit** (specs exist; no live browser). Recommend wiring Playwright e2e into
  CI if not already required.

**Recommendation.** Add a shared `PreviewBadge` and apply it to RBAC token management and any other
not-fully-complete surface; keep the drift guard; require e2e in CI.

---

## 8. Documentation review

**Fact.** Documentation is unusually complete and current: a config reference that matches the schema
and correctly states tag-gated tables are rejected at preflight; per-feature docs; 13 ADRs; specs
(years 1–5, console-v2, console-rbac, reload-plan, hardening-platform); a roadmap; a status/GA matrix
with a machine-readable manifest; `known-limitations.md`; a per-feature threat-note index in
`SECURITY.md`; and a `docs-check.py` gate that passed with **1346 checks**.

**Drift / issues found:**
- **F-04 (medium):** [SECURITY.md](../../SECURITY.md) admin bullet still says full RBAC "remains a
  future milestone," but RBAC shipped in Phase 3 as opt-in `[admin.rbac]` (`internal/rbac` +
  `console_rbac_e2e_test`). The accurate statement is "RBAC roles/scoped tokens/audit delivered;
  interactive token management remains future."
- **F-07 (low):** [docs/ga-push.md](../ga-push.md) header is version 1.32 (2026-07-09) while the
  canonical [docs/status.md](../status.md) is 1.37 (2026-07-31), though its content is current — a
  version-stamp lag.
- **F-02 (see §9):** headline docs use "GA/delivered" uniformly and do not expose the
  implemented/merged/released/soaked/audit-closed distinction.

**Audience fit.** New users (getting-started, zero-config), operators (configuration, troubleshooting,
deployment, reload-semantics), integrators (grpc/transcoding/plugins), contributors (CONTRIBUTING,
ADRs), security reviewers (SECURITY + threat notes) are all served. This is a real strength.

**Recommendation.** Fix F-04/F-07; add a one-page "maturity vocabulary" doc (§9) and link it from
README/status; add a short "release state" line to the changelog's `[Unreleased]` section clarifying
that merged-to-`main` ≠ released.

---

## 9. Specs, ADRs, and roadmap coherence

**ADRs match code (Fact).** Spot-verified: ADR-0007 (composition root — `main.go` <100 LOC, wiring in
`internal/app`) ✅; ADR-0011 (single `ReloadPlan`) ✅; ADR-0013 (managed-apply terminal ledger) ✅
with matching tests; ADR-0006 (build-time SPA, drift guard) ✅ (build output matched committed
assets). ADR-0010 (RBAC) is implemented in `internal/rbac`.

**Audit register (Fact, strength).** [docs/audit-register.md](../audit-register.md) tracks Rounds
9–13 with fix location → test name → commit → status. Two items are explicitly **deferred**:
R9-14.4 (never-draining shutdown test) and R9-14.5 (hot-added TLS rotation test).

### Finding F-02: Maturity vocabulary collapses distinct states into one public word
- **Severity:** high (product clarity)
- **Area:** README, docs/status.md, docs/roadmap/README.md, CHANGELOG, feature-status.yaml
- **Evidence:** [docs/roadmap/README.md](../roadmap/README.md) (v1.37) shows Phase 2 "in progress
  (remediation pass)" and Phase 3 RBAC "implemented; in security remediation," and Phase 4 egress
  "delivered" — yet egress sits under CHANGELOG `[Unreleased]` and is merged to `main` but not
  released; meanwhile status.md declares "all 20 features GA, soak gate closed."
- **Fact:** The same word ("GA"/"delivered") covers features that are variously implemented,
  merged-but-unreleased, soak-closed, and audit-reopened.
- **Inference:** The maturity model is disciplined internally but under-communicated externally; a
  careful reader must cross-reference four documents to learn that the newest hardening is unreleased
  and one subsystem's audit is not formally closed.
- **Why it matters:** Operator/reviewer trust depends on labels meaning exactly one thing.
- **Recommendation:** Define explicit states — `implemented` → `merged` → `released` → `soaked` →
  `audit-closed` — and render them per feature in status.md/feature-status.yaml. Keep "GA" only for
  released + soaked + audit-closed.
- **Acceptance criteria:** status matrix shows a state per feature; egress reads "merged, release
  pending"; the reopened config subsystem reads "remediated, closure pending" everywhere.
- **Effort:** M
- **Dependencies:** F-08
- **Status (2026-07-31):** ✅ Resolved in `1f4488d1` — "Delivery state vs. maturity" table added to
  status.md and reconciled across roadmap/CHANGELOG/feature-status.yaml/README.

### Finding F-08: Reopened configuration audit is remediated in code but not formally closed
- **Severity:** medium
- **Area:** [docs/audit/old/2026-07-25-configuration-audit-closure.md](old/2026-07-25-configuration-audit-closure.md)
- **Evidence:** all AC-01…AC-16 are "Reopened → remediated" with named workstream SHAs (WS01–WS07)
  and existing test files (`internal/app/managed_apply_finalizer_test.go`, `config_apply_deadline_
  test.go`, `managed_apply_id_test.go`, `promote_verified_test.go`, plus Console tests). Outstanding
  items are **process**: exact-SHA CI green (incl. `-race`/multi-OS) + two human sign-offs. AC-15
  admits RBAC *token management* is unimplemented ("future").
- **Fact:** The code remediation is done and tested; formal closure awaits a CI+sign-off ritual.
- **Inference:** This is governance strength (refusing to fake closure), but the "not final" status
  should be surfaced wherever the configuration subsystem is described as GA.
- **Why it matters:** A reader of status.md would not know the config subsystem's closure is pending.
- **Recommendation:** Run the exact-SHA CI + sign-offs and close, or annotate status/roadmap with
  "config subsystem: remediated, closure pending." Land the two deferred tests (R9-14.4/R9-14.5) as
  part of the closing evidence.
- **Acceptance criteria:** Either AC rows are Closed with SHAs + sign-offs, or every GA reference to
  the config subsystem carries the "closure pending" note.
- **Effort:** M
- **Dependencies:** F-01 (CI must be trustworthy first)
- **Status (2026-07-31):** ◐ Partially addressed in `1f4488d1` — status.md/roadmap now carry the
  "remediated, closure pending" note. Formal closure (exact-SHA CI + two sign-offs) and the two
  deferred tests (R9-14.4/R9-14.5) remain outstanding.

**Roadmap coherence (Inference).** The demand-gated horizon (Years 3–5) is well-separated and
sensible. The near-term is coherent *if* Phase 2/3 remediation and the audit closure are finished
before any new phase (5/6) starts. Do not begin the AI-Gateway bet (Phase 6) until F-01/F-02/F-08 are
closed.

---

## 10. Testing and QA review

**Distribution (Fact, corrected by direct inspection).** Contrary to a first-pass impression, test
coverage is broad and deep: `internal/app` ~20 test files (apply/finalize/deadline/timeout/pending/
race-scale/wiring/factory), `internal/admin` 50+, `internal/server` 20+, `internal/middleware` ~14,
`internal/observability` 8+, plus fuzz (`config`, `auth`, `router`, `plugins`, `handler`, `stream`),
benchmarks across most packages, reload-churn leak tests (`auth`, `waf`), and soak tests
(`handler`, `stream`). Frontend: 41 Vitest files (539 tests) + 2 Playwright specs.

**Critical-path coverage (verified live).** config parse/validate/lint ✅; invalid configs ✅
(unknown upstream rejected); reload/apply/rollback ✅ (extensive `internal/app`); CLI exit codes ✅;
JSON output ✅; admin auth/RBAC ✅ (`console_rbac_e2e_test`); route projections ✅; egress/SSRF ✅
(`internal/egress` integration + policy tests); gRPC transcoding ✅; upstream health ✅; cache ✅;
stream TCP/UDP ✅ (soak). Full and lean `go test ./...` both pass.

**Weak spots.**
- **F-03** — plugins 70.1%, waf 71.4%, rbac 75.8% coverage; no dedicated floors; thin negative/abuse
  tests on exactly the security-critical surfaces.
- `internal/signals` has no tests (platform-specific; low risk but untested).
- Two deferred resilience tests (R9-14.4 never-draining shutdown, R9-14.5 hot TLS rotation).
- e2e/soak/`-race`/multi-OS are CI-gated, not exercised in this local pass (§16).

**CI gates (Fact).** `ci.yml` runs license-check, lean+full build/test, windows/macOS lifecycle,
`-race` (Linux+CGO), coverage floors (global 65; config 82/server 78/auth 87/admin 77), benchmark
smoke, fuzz smoke (20s/target), and a 30s soak smoke; `release.yml` adds a 5-min soak gate that
blocks tags (ADR-0005). This is a strong merge/release bar — **except that F-01 shows `format-check`
is not actually blocking merges to `main`.**

**"Merge-safe" bar recommendation.** Fix F-01 (make format-check truly required), add plugins/waf/rbac
floors (F-03), land the two deferred tests, and require Playwright e2e. Then the local `make ci-pr`
parity is genuinely trustworthy.

---

## 11. Security and operations review

**Trust model (Fact, mature).** `SECURITY.md` defines a clear boundary: config/binary/filesystem
trusted; downstream client requests untrusted; configured upstreams trusted-by-default. Core
invariant: "request input never widens the attack surface" — upstream targets, JWKS URLs, cert sets,
FastCGI roots are operator config, never request-derived (SSRF-safe by design).

**Egress/SSRF (Fact, excellent).** The optional `[egress]` allow-list adds defense-in-depth for the
config-driven auxiliary fetches (JWKS, forward-auth, discovery, ACME/OCSP, plugin `fetch`).
Enforcement at `DialContext` (covers redirects), `Proxy=nil` pinning, IP-literal→CIDR check,
name-trusted vs CIDR-only resolution, and rejection of records that mix allowed/disallowed IPs
(`mixed_dns_answers`). Block logs are rate-limited, memory-bounded, and secret-free. `govulncheck`
(lean + full) reports **0 called vulnerabilities**.

**Plugin sandbox (Fact).** wazero WASM with bounded request/response buffers, fail-closed on
oversize, per-request isolation, response withheld until the guest returns, and egress-intersection
on `fetch` with a distinct `-5` denial code.

**Admin security (Fact + gap).** Constant-time bearer token, strict CSP, `X-Frame-Options: DENY`,
same-origin `/api`, traversal-safe snapshot IDs, validate-before-apply, and an anti-lockout guard
that holds admin-reachability edits for confirmation. RBAC (roles/scoped tokens/audit) ships opt-in;
**token management is still "future"** and **`SECURITY.md` understates the delivered RBAC** (F-04).

**File/permissions (Fact).** Persisted configs/history/imports written `0o600` via temp+fsync+rename
(atomic; preserves an existing stricter mode). Least-privilege systemd unit provided.

**Prioritized security risks:**
1. **F-03** — thin negative testing on plugins/waf/rbac (highest security leverage).
2. **F-04** — security doc understates RBAC → operators may not enable available scoping/audit.
3. **F-02/F-08** — labeling: an unreleased hardening feature (egress) and a not-formally-closed config
   audit are presented as done; a security reviewer needs the precise state.
4. **Release attestations** — Sigstore provenance + SPDX SBOM are documented in
   [docs/release.md](../release.md); not re-verified here (§16).

---

## 12. Product and marketing perspective

**Positioning (recommended).** "The single-binary edge server that goes from zero-config HTTPS to a
real protocol gateway — gRPC, HTTP/3, WASM, L4 — without a control plane." Lead with *ergonomics +
breadth on one node*, not feature count.

**Differentiation (credible, Fact-backed).** (1) gRPC↔JSON transcoding with a Console route designer;
(2) WASM plugins with a real capability/egress model; (3) an operable Console with truthful
apply/rollback semantics; (4) egress SSRF hardening as a first-class, documented feature. These are
genuine, demonstrable proof points.

**Do not overclaim.** Until F-02 is fixed, avoid blanket "everything is GA." Describe WASM plugins and
WAF as "GA-capable, hardening in progress," RBAC token management as "preview," and egress as "landed,
release pending."

**Marketing-grade demos.** (a) zero-config HTTPS in <60s; (b) REST→gRPC transcoding via the Console
designer; (c) a 20-line WASM plugin that rewrites a header, showing the egress-restricted `fetch`;
(d) hot reload with diff → apply → rollback, showing the exact-ID ledger. Each maps to tested code.

**Release-announcement themes.** "Egress hardening: SSRF defense-in-depth for every config-driven
fetch" is a strong, honest headline — once released. Pair with the soak evidence and govulncheck-clean
posture.

**Community prompts.** Ask for discovery adapters, plugin examples, and importer coverage reports —
areas where breadth is claimed but community validation would strengthen credibility.

---

## 13. Prioritized backlog

> **Status note (2026-07-31):** items 1, 2, and 4 below (F-01, F-02, F-04, plus F-07) are **resolved**
> and item 3 (F-08) is **annotated**; see the Remediation status section near the top. The list is
> kept as the original audit backlog for traceability. The live top of this queue is now **F-03**
> (P2), the **F-01 CI-gate follow-up**, **F-08 formal closure**, and the **4 Dependabot** advisories.

### Immediate / P0–P1 (before the next release or any GA-soak claim)
1. **Fix `gofmt` violation + prove `format-check` gates merges (F-01).** Area: build/CI. Sev high.
   Impact high (trust). Effort S. Deps none. AC: `gofmt -l` empty; CI shows required green
   format-check. Owner: backend/CI.
2. **Introduce maturity vocabulary + reconcile docs (F-02).** Area: docs/product. Sev high. Impact
   high. Effort M. Deps F-08. AC: per-feature state in status matrix; egress "release pending".
   Owner: product/docs.
3. **Close or annotate the reopened config audit (F-08).** Area: process/docs. Sev medium. Impact
   high. Effort M. Deps F-01. AC: AC rows Closed with SHAs+sign-offs, or "closure pending" noted
   everywhere. Owner: backend/QA.
4. **Correct SECURITY.md RBAC status (F-04).** Area: docs/security. Sev medium. Effort S. AC: RBAC
   described as delivered opt-in; token management as future. Owner: security/docs.

### Near term / P2 (before broad adoption)
5. **Coverage floors + negative tests for plugins/waf/rbac (F-03).** Sev medium. Effort L. AC: floors
   in CI; ≥5 abuse tests each. Owner: security/QA.
6. **`capabilities` full feature report (F-05)** + **CLI exit-code disambiguation (F-06).** Sev low.
   Effort S each. Owner: backend.
7. **Console "Preview" affordance (F-04 UX).** Sev low. Effort S. AC: PreviewBadge on RBAC token mgmt.
   Owner: frontend.
8. **Land deferred resilience tests** (never-draining shutdown, hot TLS rotation). Sev medium. Effort
   M. Owner: backend/QA.
9. **ga-push.md version stamp (F-07).** Sev low. Effort S. Owner: docs.

### Medium term (foundational)
10. **Decompose/guard `internal/admin` + `internal/app` growth.** Sev low-medium. Effort L. AC:
    module boundaries documented; size budget per seam. Owner: backend.
11. **Require Playwright e2e + `jul fmt` doc-example check in CI.** Effort M. Owner: QA.
12. **Re-verify release attestations (Sigstore/SBOM) end-to-end.** Effort M. Owner: security/release.

### Strategic bets (demand-gated)
13. **AI-Gateway MVP (Phase 6)** — only after P0/P1 close; keep the kill/continue gate.
14. **External IdP (OIDC/SSO)** — gate on multi-operator demand; completes RBAC token story.
15. **Distributed state (rate-limit/cache)** — gate on multi-node demand.

---

## 14. Recommended roadmap evolution

**Next 1–2 weeks (hardening + honesty).** F-01 (format gate), F-04/F-07 (doc fixes), F-05/F-06 (CLI),
and draft the maturity vocabulary (F-02). Ship the egress release so "delivered" becomes true.

**Next 1–2 months (trust + coverage).** F-03 (security coverage + negative tests), F-08 (close config
audit), deferred resilience tests, Console preview badges, Playwright-in-CI. Publish the reconciled
status matrix.

**Next quarter (product clarity + proof).** Finish Phase 2/3 remediation to formal closure; complete
RBAC token management (or keep it labeled preview); build the four marketing-grade demos; re-verify
release attestations. Only then open Phase 5/6.

**Longer-term (demand-gated).** AI-Gateway MVP behind its kill gate; external IdP; distributed state —
each activated only on validated demand per the existing evidence-gate discipline.

Streams: **hardening** (F-01/F-03/F-08, deferred tests) · **product clarity** (F-02, status matrix) ·
**docs** (F-04/F-07, vocabulary page) · **UX** (preview badges, e2e-in-CI) · **features** (RBAC token
mgmt, then demand-gated bets) · **marketing** (egress release headline, four demos).

---

## 15. File-by-file / area-by-area action list

- **`cmd/jul/capabilities.go`** — `gofmt -w` (F-01); emit all optional subsystems in `capabilities`
  JSON (F-05).
- **`cmd/jul/cli.go`** — disambiguate `lint -strict` exit code (F-06).
- **`.github/workflows/ci.yml`** — assert `gofmt -l` empty and make `format-check` a required check
  (F-01); add per-package coverage floors for `plugins`/`waf`/`rbac` (F-03); require Playwright e2e.
- **`internal/plugins`, `internal/waf`, `internal/rbac`** — add negative/abuse tests; raise coverage
  (F-03).
- **`internal/admin`, `internal/app`** — document seam boundaries; add a size/complexity budget to
  prevent god-package drift.
- **`internal/admin/ui/`** — add a shared `PreviewBadge`; apply to RBAC token management.
- **`SECURITY.md`** — correct RBAC status to "delivered opt-in; token management future" (F-04).
- **`docs/status.md`, `docs/feature-status.yaml`, `docs/roadmap/README.md`, `CHANGELOG.md`,
  `README.md`** — adopt the maturity vocabulary; mark egress "merged, release pending" (F-02).
- **`docs/ga-push.md`** — bump version stamp to 1.37 (F-07).
- **`docs/audit/old/2026-07-25-configuration-audit-closure.md`** — run exact-SHA CI + sign-offs to close,
  or propagate "closure pending" to status/roadmap (F-08).
- **`docs/audit-register.md`** — land R9-14.4 / R9-14.5 deferred tests.

---

## 16. Uncertainties and verification gaps

- **Full lint not run locally:** `golangci-lint`, `staticcheck`, and `addlicense` are not installed
  in this environment; only `go vet` (clean, lean+full) and `gofmt` were run. The license-header
  gate was therefore not reproduced locally (F-01's format finding stands regardless).
- **`-race` not run locally** (CI covers it; time-prohibitive here). Race-scale tests exist in
  `internal/app`.
- **Multi-OS matrix not run:** verification was Linux/arm64 only; Windows/macOS lifecycle paths were
  not executed.
- **Long-hour soak not run:** only a 15s proxy+UDP soak smoke (passed, bounded). The release 5-min
  gate and any multi-hour runs were not reproduced.
- **Playwright e2e not executed:** specs exist (`e2e/smoke.spec.ts`, `e2e/real-server.spec.ts`) but
  no live browser run; Vitest (539) was run and passed.
- **Live external integrations not exercised:** real ACME issuance, live Consul/Kubernetes discovery,
  and a running admin server against a browser were not performed.
- **Release supply chain not re-verified:** Sigstore attestations and SPDX SBOM are documented but not
  independently validated in this pass.
- **Security posture is code- + `govulncheck`-based, not pen-tested.** `govulncheck` (lean + full)
  found 0 called vulnerabilities; no active exploitation testing was performed.
- **Coverage figures** are single-run, full-tag statement coverage for the sampled packages, not the
  full CI coverage computation.
- **Soak evidence in `docs/ga-push.md`** (multi-million-request historical runs) was not independently
  reproduced; only the local smoke was.

---

### Appendix — live commands executed (evidence)

- Build: `go build ./...` and `go build -tags "<full>" ./...` → clean.
- Vet: `go vet ./...` (lean + full) → clean.
- Tests: `go test ./...` (lean) and `-tags "<full>"` → all packages pass (`internal/app` ~426s).
- Coverage (full): config 85.7, auth 91.6, egress 86.9, admin 78.0, server 80.6, handler 76.6,
  waf 71.4, stream 81.4, plugins 70.1, upstream 84.1, rbac 75.8.
- Docs: `python3 scripts/docs-check.py` → 1346 passed / 0 failed; `check-full-tags-sync.py` → OK.
- Format: `gofmt -l` → `cmd/jul/capabilities.go` (F-01).
- CLI: `version`, `capabilities -json`, `check` (0), `lint -json` (0), missing-file (1), bad-flag (2),
  invalid-config (1).
- Console: `pnpm typecheck` (0), `pnpm lint` (0), `pnpm test` → 539/539 pass, `pnpm build` (0),
  embedded `assets/dist` matches committed tree.
- Security: `govulncheck ./...` lean + full → 0 called vulnerabilities.
- Soak: `SOAK_DURATION=15s scripts/soak.sh` → proxy 30203 req/0 err, UDP cap=256 enforced, no leak.
