# Jul.IA — Full Repository Audit (2026-07)

> **Status: AUTHORITATIVE — Single Source of Truth.** Version 1.2 · Audited 2026-07-01 · **Reaudited 2026-07-02** · Repo state: `main`, release line v1.27.0, Go 1.26.4.
>
> This document consolidates and supersedes the earlier point reviews under `docs/reviews/` for the purpose of *current repository state*. Those documents remain valid as historical decision inputs; where they overlap with this audit, this file wins. See [`README.md`](README.md) for the decision-log index.

**Audit method.** Evidence-based review of the repository plus **targeted local verification** on Windows/amd64 (go1.26.4): lean + full builds, `go test` across critical and tag-gated packages, `jul` CLI exercises, `govulncheck` (full tags), and the Console `typecheck`/`eslint`/`vitest` suite. Findings separate **Fact** (directly supported), **Inference** (reasoned interpretation), and **Recommendation**. Non-trivial findings use the standard finding block. What could not be verified is listed in §16.

**Benchmarks used for maturity/discipline only:** NGINX (stability, operator trust), Caddy (ergonomics, automatic HTTPS). These are yardsticks, not templates to copy.

---

## 0. Current reaudit status (2026-07-02)

**Reaudit date:** 2026-07-02 · **Repo state:** `main`, clean working tree (all prior remediation committed) · **Reauditor scope:** reconciliation of every prior finding against current code + net-new issue hunt.

**Areas re-inspected.** The new `internal/app` factory package (`wiring.go`, `admin_deps.go`, `preflight.go` + tests); `cmd/jul/main.go`; `internal/admin` (both rollback handlers, plugin upload, the split `api_*.go` files); `internal/config` (`lint.go`, `schema.go`, the split `validate*.go` files); `internal/server` leak guard; `internal/transcode`/`internal/plugins` concurrency tests; the admin UI API client; CI/release workflows; docs (`status.md`, `soak-evidence.md`, `troubleshooting.md`, `configuration.md`, `plugins.md`); example configs.

**Checks executed this reaudit.** Lean `go build ./...` + `go test ./...` (**all pass**, 0 fail); full-tag build + `go test` on `config`/`admin`/`cmd/jul` (**pass**); `scripts/docs-check.py` (**866 pass / 0 fail**); file line-count probes; two structured code-exploration passes. **Not re-run this cycle:** the 5-minute release soak, long fuzz, `govulncheck` (unchanged since v1.1; no dependency changes), browser-driven Console test, non-Windows platforms — see §16.

**What changed since v1.1 (net movement).** The repo is **improving**. The prior P0/P1/P2 remediation landed and was extended: `cmd/jul/main.go` is down to **~858 LOC** (from 1087) with a genuine `internal/app` package, and this reaudit's remediation split the two remaining god-files (`admin/api.go` 1214→**502**, `config/validate.go` 1005→**561**), fixed a real regression (REG-1), and cleaned stale examples (NEW-2).

**Most important discovery (now fixed).** The v1.1 rollback-serialization fix (P1-2) was applied to only **one of two** rollback endpoints; the Console's actual rollback path (`POST /api/config/rollback`) was left unserialized. This reaudit found and fixed it (Finding **REG-1**).

**Important assumptions / confidence limits.** Test *pass* status is from local runs on Windows/amd64 only; CI-observed green is assumed. REG-1's race is reasoned from code + closed by serialization and a concurrency test; it was not reproduced as a failing race under `-race` locally (no CGO toolchain on the audit box — CI runs `-race` on Linux).

---

## 1. Executive summary

**Overall maturity: strong engineering foundation, honestly governed, with a small number of real reliability and consistency gaps that must close before any "GA" claim hardens into a promise.**

Jul.IA is materially more mature and more *disciplined* than the typical solo/edge-server project. It has a real maturity model ("implemented ≠ GA"), a published GA evidence bar (ADR-0003/0005), a genuine CI matrix (lean+full, race, coverage floor, bench/fuzz/soak smoke), signed+attested releases with SBOM, a hardened container/systemd story, and a Console that is unusually complete (32 test files / 347 passing frontend tests). The architecture is intentional: generational atomic reload, preflight-before-apply admin writes, build-tag feature gating with loud rejection of unsupported tables, and an explicit SSRF trust model.

**Strongest areas (Fact-backed).**
- **Governance & honesty.** `docs/status.md` splits 7 "GA — soak pending" vs 13 Beta features with per-feature GA gaps; ADR-0003/0005 define the bar; docs are dated and current. This is better maturity hygiene than most OSS servers.
- **Security posture by design.** `SECURITY.md` trust model (request input never selects upstream/JWKS/cert/FastCGI root); plugin egress guards with loopback-always-blocked + DNS-rebinding checks ([`internal/plugins/plugins_hardening_test.go`](../../internal/plugins/plugins_hardening_test.go)); `govulncheck` full-tags = **no vulnerabilities**.
- **Reload safety.** Generational handler swap with in-flight drain ([`internal/server/server.go`](../../internal/server/server.go)); admin apply is validate→preflight→diff→snapshot→write→reload→rollback, serialized by `applyMu`.
- **Console completeness.** 14+ feature panels, review-before-apply, verified locally: typecheck + eslint clean, **347 vitest tests pass**.

**Riskiest areas (Fact-backed, 2026-07-02).** The four risks that dominated v1.0 are now **resolved**: the `internal/server` flaky hang (CQ-1, goleak + keep-alive-free clients), unpublished soak evidence (DOC-1, `docs/soak-evidence.md` + CI artifacts), the CLI JSON contract (UX-1), and the composition-root/admin-API god files (CQ-2 substantially, CQ-3 fully — `main.go` 1087→~858, `admin/api.go` 1214→502, `config/validate.go` 1005→561, all split under 600 LOC). What remains:
- **Residual QA gaps (QA-1, medium).** Still missing: a CLI `import` golden + `run` runtime smoke, and an ACME cert-rotation-under-concurrent-handshake test. The reload-under-load soak (M-4/SEC-2) is not yet added.
- **Composition-root not fully thin (CQ-2, low).** `main.go` is testable via `internal/app` now, but still ~858 LOC — the `<250` target is not met (ADR-0007 is already marked *Partial* as of 2026-07-02).
- **`x/tools` pin (CQ-4, medium, unchanged).** Still deferred by design (ADR-0008); no CVE today, but a latent forced-upgrade trap on the FastCGI path.
- **Beta evidence bundles (SPEC-1 tracker exists; work remains).** 13 Beta features still owe matrix/bench/threat-note/fuzz per the GA-evidence burndown.

**Is core HTTP/Console GA-soak ready?** **Inference (2026-07-02): materially closer than v1.0.** CQ-1 is fixed and soak evidence is published, removing the two blockers named in v1.0. The remaining gate is running (and publishing) the 5-minute release soak per GA candidate and adding the reload-under-load scenario. Treat Core HTTP + Console as "GA candidate; soak-publication and reload-resilience away."

**Which advanced features should remain Beta?** All 13 currently-Beta items should stay Beta: WASM plugins, L4 stream, WAF, HTTP/3, service discovery, OTel, compression codecs, rate limiting, cache, importer, secrets, active health checks, zero-config/lint. None yet meet the full 9-criterion bar (matrix/bench/threat-note gaps per `docs/status.md`). This is the repo's own position and I agree with it.

**Top 5 to do next (2026-07-02).**
1. **Publish the 5-minute release-gate soak artifact per GA candidate** and flip the `☐ pending` rows in `docs/status.md` (DOC-1 follow-through; the harness + smoke evidence already exist). *(P0/docs)*
2. **Add the reload-under-load soak scenario** asserting steady goroutine/heap across repeated reloads under traffic (M-4/SEC-2). *(P1)*
3. **Close the remaining QA gaps** — CLI `import` golden + `run` smoke; ACME rotation-under-handshake concurrency test (QA-1 remainder). *(P1/QA)*
4. **Finish the composition-root trim** — reduce `main.go` toward <250 LOC and finalize ADR-0007 (already *Partial*) once the structural `buildHandlers`/`serve()` extraction lands (CQ-2). *(P2)*
5. **Advance the Beta evidence bundles** highest-signal first (cache poisoning note, WAF FP/bypass, plugin sandbox, HTTP/3 amplification) against the `status.md` burndown. *(P2, demand-gated)*

---

## 2. What Jul.IA is today

**Fact (README.md#L1-L17).** Jul.IA is an NGINX-inspired HTTP edge server written in Go, configured entirely through TOML, shipped as a single static `CGO_ENABLED=0` binary, with optional features behind build tags.

**Core value proposition.** A *lean, single-binary* edge server that covers the 80% NGINX/Caddy use cases (static, reverse proxy + LB, TLS/automatic-HTTPS, cache, compression, auth, rate limiting) **plus** a differentiated protocol-gateway core (gRPC transcoding + native gRPC passthrough) **plus** a genuinely usable operations Console — without a runtime dependency footprint.

**Target users (Inference from README + `docs/vision/README.md`).** Operators of single-node edge infrastructure; teams migrating off NGINX (there's an importer); shops that need gRPC↔JSON at the edge; developers who want zero-config HTTPS and a Console rather than hand-edited config.

**Feature surface (Fact, README + `docs/status.md`).** Static/proxy/FastCGI/uWSGI/vhosts/routing; TLS 1.2/1.3 + ACME + mTLS; auth (CIDR/Basic/JWT/forward); rate + connection limiting; compression (gzip core; brotli/zstd tags); two-tier cache (mem+disk, SWR/SIF); gRPC transcoding + passthrough + h2c; L4 TCP/UDP stream proxy with PROXY protocol + SNI routing; WASM plugins (wazero); WAF (Coraza + CRS); service discovery (DNS/SRV core; Consul/K8s tags); observability (structured logs, Prometheus, OTel, access-log sinks); HTTP/3; secrets refs + log redaction; NGINX importer; admin Console.

**Maturity split (Fact, `docs/status.md` v1.27, dated 2026-06-30).** 7 "GA — soak pending" (Core HTTP, TLS+ACME, Auth, gRPC transcoding, gRPC passthrough+h2c, mTLS, Console); 13 Beta. Soak is a *post-GA* gate (ADR-0005). `[[mail]]` is parsed-but-rejected in v1.

**Implicit positioning (Inference).** Per `docs/reviews/README.md` synthesis, the deliberate stance is *"leanest serious edge/protocol gateway"* — not "most powerful" (which invites losing comparisons with Envoy/Kong/Istio). The flagship differentiator is the gRPC gateway + Console operability, not raw feature count.

**What it should *not* try to be yet (Inference/Recommendation).** Not a fleet/mesh control plane, not multi-tenant SaaS, not an AI gateway — all correctly marked "vision horizon / demand-gated" in `docs/roadmap/README.md`. It should also resist marketing the 13 Beta features as if they were core; the honest split is a strength and must be preserved.

---

## 3. Architecture assessment

**Entry points / composition root (Fact).** [`cmd/jul/main.go`](../../cmd/jul/main.go) `run()` dispatches CLI subcommands ([`cmd/jul/cli.go`](../../cmd/jul/cli.go)) or falls through to `serve()`, the composition root, which wires logging, cache, metrics, tracer, ACME manager, stream server, WAF, the handler factory, and the admin server, then calls `server.New()`/`Run()`. Windows service integration is split via build-tagged `service_windows.go` / `service_other.go`.

**Package/domain map (Fact).** Cleanly separated `internal/` packages: `config`, `server`, `router`, `handler`, `upstream`, `middleware`, `cache`, `auth`, `transcode`, `stream`, `plugins`, `waf`, `observability`, `tracing`, `redact`, `atomicfile`, `admin` (+ `admin/ui`). Feature packages are gated by build tags with `*_stub.go` fallbacks so lean builds compile and reject unsupported config at startup.

**Config lifecycle (Fact).** Parse → Defaults → Validate → Preflight → Apply → Persist. Parse/defaults in [`internal/config/parser.go`](../../internal/config/parser.go); `Validate()` + ~20 helpers in [`internal/config/validate.go`](../../internal/config/validate.go) (561 lines after the 2026-07-02 split; location/backend validators moved to `validate_location.go`/`validate_backends.go`); `PreflightClone()` dry-runs the full composition (secret expansion, WAF compile, auth init, pool build) without applying; persistence via [`internal/atomicfile`](../../internal/atomicfile/atomicfile.go) (temp→sync→chmod 0600→rename).

**Runtime/reload model (Fact).** [`internal/server/server.go`](../../internal/server/server.go) uses generational handlers: `dynamicHandler` acquires the current generation per request, `doReload()` builds a new generation via the factory and swaps atomically, old generations retire after in-flight drain or grace timeout. Cache/metrics/tracer/log-sinks persist across reloads; pools/handlers rebuild. Bad edits keep the running config (no downtime).

**Admin/control-plane architecture (Fact).** [`internal/admin`](../../internal/admin) exposes a loopback-bound API (bearer-token auth, constant-time compare) with read projections and a write path: `validate` (side-effect-free) → `diff` → `apply` (snapshot→WriteConfigRaw→reload) → `rollback`, all serialized by `applyMu` with optimistic-concurrency `base_version` tokens. Console data is decoupled via [`internal/admin/projections.go`](../../internal/admin/projections.go).

**Extensibility points (Fact).** Router `Builder` registry (static/proxy/fastcgi/grpc/transcode/plugin actions); per-location `LocationModifier` middleware; WASM plugin ABI ([`internal/plugins/abi.go`](../../internal/plugins/abi.go)); pluggable cache store, access-log sinks, discovery backends.

**Intentional vs accidental (Inference).** Overwhelmingly *intentional*: the generational reload, preflight isolation, stub/tag strategy, and projection layer are deliberate patterns applied consistently. The *accidental* drift is concentrated in size — the composition root and admin API grew into large single files.

**Coupling risks (Fact + Inference; updated 2026-07-02).** `cmd/jul/main.go` (~858 LOC, down from 1087) imports every package; its pure wiring/preflight helpers are now extracted into the testable `internal/app` package (ADR-0007 *Partial*), but the `buildHandlers`/`serve()` body remains inline. The former admin/config god-files were split by concern: `internal/admin/api.go` (502 LOC + `api_status.go`/`api_history.go`/`api_wizard.go`) and `internal/config/validate.go` (561 LOC + `validate_location.go`/`validate_backends.go`), all under ~560 LOC. `internal/handler` still couples to upstream/config/middleware/auth/waf. These are maintainability risks, not correctness bugs.

---

## 4. Code quality findings

### Finding CQ-1: Intermittent hang/timeout in `internal/server` tests under parallel execution

- **Status (2026-07-02): ✅ Resolved.** Root cause confirmed as the `fetch`/`reachable` test helpers pooling keep-alive connections via `http.DefaultTransport` (leaked `persistConn` readLoop goroutines). Fix: a shared keep-alive-free client + `goleak.VerifyTestMain` in [`internal/server/main_test.go`](../../internal/server/main_test.go); `go.uber.org/goleak` promoted to a direct dependency; a Windows CI lane added (P1-4). Validated ≥15 lean + full-tag repeats, no hang.
- **Severity:** high
- **Area:** `internal/server` test suite / server request or reload lifecycle
- **Evidence:** Combined critical run `go test ./internal/config/... ./internal/server/... ./internal/admin/... …` → `FAIL jul/internal/server 601.297s` with a panic dump showing a client `persistConn.readLoop` in "IO wait, 9 minutes" and the server stack at [`internal/server/server.go`](../../internal/server/server.go) `dynamicHandler`→`ServeHTTP`. Isolated re-run `go test -v -timeout 150s ./internal/server/` → `ok … 2.229s`.
- **Fact:** The package deterministically passes in isolation but hung to the 10-minute Go panic-timeout at least once when run in parallel with other listener-binding packages.
- **Inference:** A test leaves an HTTP client/keep-alive connection open (or a drain/reload path blocks) and, under parallel package execution / port pressure, occasionally deadlocks. It is a *test-harness or lifecycle* leak, most likely a proxy/streaming/reload test that doesn't close its client or a drain that waits on a never-closing connection.
- **Why it matters:** A flaky 10-minute hang destroys CI signal, can wedge local runs, and — if it reflects a real drain/keep-alive leak in `dynamicHandler`/`retireGen` — could manifest as stuck connections in production during reload.
- **Recommendation:** Add explicit `t.Deadline()`/`-timeout` per test; adopt `go.uber.org/goleak` in `TestMain` for `internal/server` to fail on leaked goroutines/connections; run `go test -count=20 -race ./internal/server/` and the full parallel set repeatedly in CI to reproduce; ensure every test closes `http.Client`/`httptest.Server` and idle transports; audit `drain()`/`retireGen()` for a path that can block indefinitely.
- **Acceptance criteria:** `internal/server` passes 50 consecutive parallel full-suite runs with goleak enabled and a per-package `-timeout 120s`, zero hangs.
- **Effort:** M
- **Dependencies:** none (may reveal a real reload/drain fix)

### Finding CQ-2: Composition root is a 1087-line untestable monolith

- **Status (2026-07-02): ◐ Partially addressed (severity downgraded high→low).** `cmd/jul/main.go` is now **~858 LOC** (from 1087) and a real [`internal/app`](../../internal/app) package holds the extracted, unit-tested wiring: `wiring.go` (scope/index/reload helpers + `ValidateRuntimeConfig`), `admin_deps.go` (`BuildAdminDeps` + adapters), and `preflight.go` (the admin write-preflight gate sequence), with `wiring_test.go`/`preflight_test.go`/`characterization_test.go`. **Remaining:** extract the `buildHandlers`/`serve()` body to trim `main.go` toward the `<250` target; ADR-0007 is already updated to *Partial* and should be finalized when that lands.
- **Severity:** medium
- **Area:** [`cmd/jul/main.go`](../../cmd/jul/main.go)
- **Evidence:** `main.go` = 1087 lines; imports all feature packages; ADR-0007 "Composition-root monolith" marked **Deferred (technical debt recorded)".
- **Fact:** Wiring, handler factory, stream lifecycle, and preflight all live in one `package main` file with no unit tests.
- **Inference:** Bugs in wiring (e.g., CQ-1-class lifecycle issues) can only be caught by end-to-end tests, which the repo has few of for the composition layer.
- **Why it matters:** Highest-leverage code (what actually assembles the server) is the least testable; change risk and review cost are elevated.
- **Recommendation:** Extract an `internal/app` (or `internal/bootstrap`) package exposing `BuildHandlers(cfg) (http.Handler, retire func, error)` and the factory, leaving `main.go` as thin flag-parsing + call. Add table tests for the factory.
- **Acceptance criteria:** `main.go` < ~250 lines; factory covered by unit tests; ADR-0007 updated from Deferred to In-progress/Accepted with the new package.
- **Effort:** L
- **Dependencies:** none

### Finding CQ-3: Admin API and validate in oversized single files

- **Status (2026-07-02): ✅ Resolved.** Both god-files were split by concern within their packages: [`internal/admin/api.go`](../../internal/admin/api.go) 1214→**502** LOC (extracted `api_status.go` 349, `api_history.go` 192, `api_wizard.go` 221) and [`internal/config/validate.go`](../../internal/config/validate.go) 1005→**561** LOC (extracted `validate_location.go` and `validate_backends.go`). No admin/config file now exceeds ~560 LOC; behavior unchanged (full lean + tagged tests green). *Note: v1.0's counts (1125 / 941) had grown to 1214 / 1005 with the v2 endpoints before this split (was net-new Finding NEW-3).* 
- **Severity:** low
- **Area:** [`internal/admin/api.go`](../../internal/admin/api.go) (1125 LOC), [`internal/config/validate.go`](../../internal/config/validate.go) (941 LOC)
- **Fact:** Both are large single files; exploration confirms they are internally well-factored (per-concern helpers).
- **Inference:** Not a correctness risk today, but they trend toward "god file" and slow navigation/review.
- **Why it matters:** Maintainability and onboarding.
- **Recommendation:** Split `api.go` by resource group (config, observability, history/audit, wizard) into `api_*.go`; split `validate.go` similarly (already helper-based). Cosmetic, low urgency.
- **Acceptance criteria:** No single admin/config file > ~600 LOC; behavior unchanged (tests green).
- **Effort:** M
- **Dependencies:** none

### Finding CQ-4: `x/tools` pinned to v0.6.0 for `gofast` (FastCGI) — latent supply-chain risk

- **Status (2026-07-02): Open (deferred by design, unchanged).** Pin still present in `go.mod`; ADR-0008 records the monitored-trigger decision. No CVE affects the pinned surface today.
- **Severity:** medium
- **Area:** [`go.mod`](../../go.mod) replace/pin; ADR-0008
- **Evidence:** `go.mod` TODO(ADR-0008): `gofast` imports removed `golang.org/x/tools/godoc/vfs`, forcing a pin to the last version shipping `vfs`. `govulncheck` (full tags) currently reports **no vulnerabilities**.
- **Fact:** The pin is documented and deferred; no CVE affects the pinned surface *today*.
- **Inference:** A future CVE in `x/tools ≤ v0.6.0` would create a forced-upgrade-vs-break-build dilemma with no in-repo escape hatch.
- **Why it matters:** FastCGI/uWSGI (PHP/Python gateways) are a marketed feature; the dependency owns a security-relevant transitive pin.
- **Recommendation:** Track upstream `gofast`; evaluate replacing `gofast` or vendoring a minimal FastCGI client without the `vfs` dependency; add a scheduled `govulncheck` on the pinned graph so regressions are caught early.
- **Acceptance criteria:** Either the pin is removed, or ADR-0008 carries a monitored trigger + a rehearsed replacement plan.
- **Effort:** M–L
- **Dependencies:** upstream `gofast`

### Finding CQ-5: Error-handling and resource conventions are consistent (positive)

- **Severity:** low (informational)
- **Area:** repository-wide
- **Fact:** Wrapped errors (`fmt.Errorf("ctx: %w")`) are used pervasively; only 4 sentinel errors exist for control flow (`ErrNoAvailableBackend`, `ErrRestartRequired`, `errFetchBlocked`, `errBodyTooLarge`); resources implement `io.Closer` for generational cleanup.
- **Inference:** Deliberate, uniform conventions — good.
- **Recommendation:** Keep; document the sentinel-error contract in a short `CONTRIBUTING` note.
- **Effort:** S

### Finding REG-1 (net-new, 2026-07-02): The rollback-serialization fix was applied to only one of two rollback endpoints

- **Status (2026-07-02): ✅ Resolved (this reaudit).**
- **Severity:** medium-high (concurrency correctness on the primary operator write path)
- **Priority:** P0 (fixed)
- **Area:** [`internal/admin/api.go`](../../internal/admin/api.go) rollback handlers; [`internal/admin/api_history.go`](../../internal/admin/api_history.go)
- **Evidence:** The v1.1 P1-2 remediation added `applyMu` to `handleHistoryRollback` (`POST /api/history/rollback`). But a **second** rollback handler, `handleConfigRollback` (`POST /api/config/rollback`), performed the same `currentRaw()→WriteConfigRaw()→recordHistory()` read-modify-write **without holding `applyMu`** — and the Console's client calls the *v2* endpoint ([`internal/admin/ui/src/api/client.ts`](../../internal/admin/ui/src/api/client.ts) `rollback()` → `/config/rollback`). The concurrency test (`TestConfigApplyRollbackConcurrent`) exercised only the *fixed-but-unused* v1 path, so CI was green while the operator-facing rollback stayed racy.
- **Fact:** Two routed rollback endpoints existed; only the one the Console does not use was serialized.
- **Inference:** A rollback concurrent with an apply (or another rollback) could interleave snapshot-and-write, defeating the optimistic-concurrency `base_version` guard and corrupting the history chain.
- **Why it matters:** It is a textbook "the test proves the wrong thing" gap on the highest-consequence admin write, and it directly contradicts the v1.1 claim that rollback was serialized.
- **Recommendation (implemented):** Route **both** endpoints through a single `applyMu`-guarded `rollbackToSnapshot` helper so serialization can never again be applied to one endpoint but not the other; extend `TestConfigApplyRollbackConcurrent` to run against **both** `/api/history/rollback` and `/api/config/rollback` (a subtest matrix).
- **Acceptance criteria (met):** Single locked write path shared by both handlers; concurrency test green on both endpoints (validated `-count=3`). The full data-race proof runs under CI `-race` on Linux (no local CGO).
- **Effort:** S (done)
- **Dependencies:** none

### Finding NEW-2 (net-new, 2026-07-02): Example configs drifted from `jul fmt` output — ✅ Resolved

- **Severity:** low · **Area:** [`examples/migrate/jul.toml`](../../examples/migrate/jul.toml), [`server.full.apps.toml`](../../server.full.apps.toml)
- **Evidence/Fact:** After UX-2 added `,omitempty`, `jul fmt` no longer emits `stream = []` / `mail = []`, but both checked-in example configs still carried those lines — the "canonical" examples no longer matched tool output.
- **Recommendation (implemented):** Removed the stale empty-table lines (kept the import-provenance comments in `examples/migrate/jul.toml`, which `jul fmt` would have stripped). Docs-check green (866/0).
- **Effort:** S (done)

### Finding NEW-3 (net-new, 2026-07-02): Admin/validate god-files had grown — folded into CQ-3 (✅ Resolved)

- **Severity:** low · **Area:** `internal/admin/api.go`, `internal/config/validate.go`
- **Fact:** v1.0 recorded `api.go` 1125 / `validate.go` 941; by this reaudit they had grown to 1214 / 1005 as the Console v2 config endpoints landed. Addressed by the CQ-3 split above.

---

## 5. Feature maturity review

Legend: **Repo claim** from `docs/status.md`; **My verdict** = Agree / Agree-with-caveat / Downgrade.

| Feature | Repo claim | Evidence (verified) | Key missing GA criteria | My verdict | Next step |
|---|---|---|---|---|---|
| Core HTTP (static/proxy/FastCGI/vhosts/routing) | GA — soak pending | Full build + `internal/handler` tests pass; `jul check` runtime-valid | Published soak result; CQ-1 hang | **Agree-with-caveat** (fix CQ-1, publish soak) | Reliability + soak evidence |
| TLS + automatic HTTPS (ACME) | GA — soak pending | `internal/server` TLS/ACME tests pass in isolation; OCSP graceful-fail tested | Soak result; CQ-1 | **Agree-with-caveat** | Soak evidence |
| Auth (CIDR/Basic/JWT/forward) | GA — soak pending | `internal/auth` PASS; `FuzzParseJWKS`; JWKS stale-grace | Soak result | **Agree** | Soak evidence |
| gRPC↔JSON transcoding | GA — soak pending | `internal/transcode` PASS (full tags); body-size + error-map tests | Soak; reflection-abuse negative test (§10) | **Agree-with-caveat** | Add negative test |
| Native gRPC passthrough + h2c | GA — soak pending | `internal/handler` gRPC tests + bench PASS | Soak result | **Agree** | Soak evidence |
| mTLS client auth | GA — soak pending | `internal/server/mtls_test.go` PASS + bench | Soak result | **Agree** | Soak evidence |
| Console | GA — soak pending | typecheck+eslint clean; **347 vitest pass**; 14+ panels | Backend↔UI e2e; soak | **Agree-with-caveat** | e2e smoke |
| Compression (gzip/br/zstd) | Beta | middleware tests PASS | encoder matrix, throughput bench, BREACH note | **Agree** | Docs+bench |
| Rate + conn limiting | Beta | middleware + admin + conn tests PASS | key/algo matrix, bench, bypass note | **Agree** | Docs+bench |
| Active health checks | Beta | `internal/upstream/health_test.go` PASS | probe matrix, limits | **Agree** | Docs |
| Zero-config + `jul lint` | Beta | CLI verified; JSON contract flaw (UX-1) | lint-checks matrix, TOML fuzz | **Agree** | Fix UX-1 |
| NGINX importer | Beta | `cmd/jul/import*` behind tag; output re-validated | directive matrix, nginx.conf fuzz | **Agree** | Fuzz + matrix |
| OTel tracing + log sinks | Beta | `internal/observability` PASS (full tags) | exporter/sink matrix, overhead bench, PII note | **Agree** | Docs+bench |
| HTTP/3 over QUIC | Beta | `http3` tests present | QUIC matrix, bench, 0-RTT/amplification note | **Agree** | Threat note |
| WASM plugins | Beta | `internal/plugins` PASS incl. hardening; egress guards | ABI/caps matrix, overhead bench, sandbox note, ABI fuzz | **Agree** | Sandbox note+fuzz |
| L4 stream proxy | Beta | `internal/stream` PASS + soak(UDP churn) | TCP/UDP/SNI/PROXY matrix, bench, spoofing note | **Agree** | Matrix+bench |
| Service discovery | Beta | discovery keep-last-good tested | provider matrix, limits | **Agree** | Docs |
| WAF (Coraza+CRS) | Beta | `internal/waf` PASS (full tags) | rule/mode matrix, overhead bench, FP/bypass note | **Agree** | Bench+matrix |
| Secrets + redaction | Beta | `internal/redact` + secrets tests PASS | ref-source matrix, resolve-cost bench | **Agree** | Docs |
| Response cache (mem+disk) | Beta | `internal/cache` SWR/SIF tested | key/TTL/overflow matrix, poisoning/isolation note | **Agree** | Threat note |

**Overall:** I agree with every maturity label. The consistent gap for GA features is **published soak evidence**; the consistent gap for Beta features is the **evidence bundle** (matrix + bench + threat note), exactly as the repo itself states.

---

## 6. CLI and operator UX review

**Grammar (Fact, verified).** `jul [flags]` (run), `jul check`, `jul lint`, `jul fmt`, `jul run --serve|--proxy`, `jul import nginx`. `-help` prints a clean, well-structured usage block (verified). Exit codes are consistent: 0 ok, 1 error, 2 warnings-under-strict. `check` on a missing file returns 1 with a clear message.

**What works well.** Uniform `-config/-json/-quiet/-strict` flags; `check` reports "valid (structural + runtime)"; importer re-validates its own output so a written config is guaranteed to load; zero-config `run` reuses the real config model.

### Finding UX-1: `jul lint -json` output is not a stable, self-describing contract

- **Status (2026-07-02): ✅ Resolved.** `config.Diagnostic` has lowercase json tags and `Severity.MarshalJSON` emits the string form; golden test `TestCmdLintJSONSchema`; schema documented in [`configuration.md`](../configuration.md#cli-json-output).
- **Severity:** medium
- **Area:** [`cmd/jul/cli.go`](../../cmd/jul/cli.go) `lintOutput` → [`internal/config/lint.go`](../../internal/config/lint.go) `Diagnostic`
- **Evidence (verified):** `jul lint -config testdata/waf.toml -json` →
  `{"source":"…","warnings":[{"Severity":0,"Field":"[compression]","Message":"…","Hint":"…"}]}`. `lintOutput` has json tags (`source/errors/warnings`) but `config.Diagnostic` (lint.go:35) has **none**, so its fields serialize PascalCase and `Severity` emits the raw int `0` even though `Severity.String()` returns `"warning"`/`"error"`.
- **Fact:** The JSON mixes lower-case and PascalCase keys and uses a numeric enum with no documented meaning.
- **Inference:** Any consumer (CI gate, dashboard) must special-case casing and hard-code `0=warning`; a future enum reorder silently breaks them.
- **Why it matters:** `jul lint -json` is explicitly positioned for CI/automation; an unstable schema defeats the purpose.
- **Recommendation:** Add json tags to `Diagnostic` (`severity/field/message/hint`); serialize severity as its string; document the schema in `docs/configuration.md`; add a golden test asserting the exact JSON.
- **Acceptance criteria:** JSON is all-lower-case, `severity` is `"warning"|"error"`, covered by a golden test; schema documented.
- **Effort:** S
- **Dependencies:** none

### Finding UX-2: `jul fmt` emits reserved/empty tables in canonical output

- **Status (2026-07-02): ✅ Resolved.** `,omitempty` on `Config.Upstreams/Streams/Plugins/Mail`; golden test `TestCmdFmtOmitsReservedAndEmptyTables`. Stale example files reconciled (Finding NEW-2).
- **Severity:** low
- **Area:** [`cmd/jul`](../../cmd/jul) fmt path / TOML serialization
- **Evidence (verified):** `jul fmt -config testdata/static.toml` begins with `upstreams = []`, `stream = []`, `mail = []`, then `[global]`. `[[mail]]` is documented as parsed-but-rejected in v1.
- **Fact:** Canonical output surfaces an unsupported (`mail`) and two empty (`stream`, `upstreams`) tables at the top of the file.
- **Inference:** Confusing to operators ("is mail supported?") and noisy in diffs/review.
- **Why it matters:** `fmt` is meant to produce the *canonical, exemplary* config; emitting a rejected table contradicts the "rejected at startup" story.
- **Recommendation:** Omit empty top-level arrays and reserved tables from `fmt` output (or gate `mail` behind an explicit `--include-reserved`).
- **Acceptance criteria:** `fmt` of a minimal static config produces no `mail`/empty-array lines; golden test added.
- **Effort:** S

**Onboarding note (Fact/Inference).** Bare `jul` (no subcommand) boots the server from `server.toml` and, with the repo's sample pointing at `/srv/www/example`, fails with a path error rather than guidance. This is nginx-like and defensible, but a first-run `jul` with no config could helpfully suggest `jul run --serve .` or `jul --help`. **Recommendation (S):** when `server.toml` is absent, print a one-line hint pointing to `jul run` / `jul --help` before exiting.

**Version stamping (Fact).** Source builds report `Jul.IA 0.1.0-dev`; the real version is ldflags-injected only in the release pipeline. **Recommendation (S/docs):** note in `docs/release.md` that from-source builds report `0.1.0-dev` so operators don't mistake it for a stale release.

**CI parity (Fact).** Local `Makefile` `ci-fast`/`ci-full` mirror the CI gates (format/lint/test/vulncheck/build), and `scripts/check-full-tags-sync.py` keeps the tag list synchronized across CI/Makefile/scripts — good local/CI parity.

---

## 7. Console/UI review

**Verified state.** `internal/admin/ui`: `tsc --noEmit` clean, `eslint` clean, **vitest 32 files / 347 tests pass** (38s). React 19 + Vite + TS + Tailwind v4 + TanStack Query + Zod + CodeMirror 6, prebuilt bundle embedded via `go:embed` (ADR-0006), single-binary preserved.

**Navigation / information architecture (Fact).** Task-driven grouping (`App.tsx`/`Layout.tsx`): **Operate** (Overview, Operations workspace with diagnostics/events/logs/timeline tabs), **Configure** (routes, apps, traffic, security, plugins, streams, tls), **Change safely** (wizard, raw config, history, audit), plus global command palette (⌘K) and search. Consolidating observability into one Operations workspace (C-4) is a good density decision.

**Panel consistency (Fact).** All panels use TanStack Query with uniform `isLoading`→spinner, `isError`→`PanelError`+retry, and `EmptyState` for disabled features (e.g., plugins/streams on lean builds show build-tag warnings). Terminology maps `route`=server block+location, `app`=upstream pool (ADR-0004 invariant).

**Safe-change flow (Fact).** Config edits go **validate (side-effect-free) → diff → review → apply**; raw + patch applies share `applyMu` and full preflight; history/rollback with confirm dialog + diff preview; rollback is itself snapshotted. This is exactly the "operable by design" model NGINX/Caddy lack out of the box.

**Dangerous-operation gating (Fact).** Rollback and delete use confirm dialogs; WAF/stream/plugin enablement warns when the build tag is absent; plugin attach validates middleware-vs-handler type.

### Finding UI-1: No verified backend↔frontend end-to-end test

- **Status (2026-07-02): ◐ Partially addressed.** An over-the-wire Go e2e ([`internal/admin/console_e2e_test.go`](../../internal/admin/console_e2e_test.go) `TestConsoleApplyRollbackFlowE2E`) drives load→apply→history→rollback against the live admin router via a real HTTP client. **Remaining:** a browser-level (Playwright) smoke against the built SPA is still absent.
- **Severity:** medium
- **Area:** `internal/admin/ui` + `internal/admin` API
- **Evidence:** 347 vitest tests exercise TOML mapping/patch/validation *client-side*; Go admin tests exercise the API server-side. No test drives the built SPA against a live admin server.
- **Fact:** The two halves are each well tested; their integration is not.
- **Inference:** Contract drift (projection shape vs `client.ts` Zod schemas) could pass both suites yet break the real Console.
- **Why it matters:** The Console is a "GA — soak pending" feature and the primary operator surface.
- **Recommendation:** Add a thin e2e smoke (Playwright headless or a Go test that serves the embedded assets and drives key flows via the API) covering: load overview, edit a route → diff → apply → rollback.
- **Acceptance criteria:** One CI e2e job green covering the core apply/rollback loop.
- **Effort:** M
- **Dependencies:** none

**UX copy / affordance checks (Inference, to verify in browser — see §16).** Recommend a pass for: honest Beta labels on plugins/streams/WAF/HTTP3 panels (a `MaturityBadge` exists — confirm it's shown on every Beta surface); no false affordances (buttons that appear enabled on lean builds); consistent "save & reload" vs "apply" wording. These are likely fine given the discipline elsewhere but were not visually verified.

---

## 8. Documentation review

**Accuracy & currentness (Fact).** Docs are dated and current (`status.md` 2026-06-30, `roadmap` 2026-06-30, `CHANGELOG` 2026-07-01, `ga-push.md` 2026-06-26). `scripts/docs-check.py` validates links, TOML fences, version/date consistency, and flags placeholder URLs and future dates — a real docs CI gate most projects lack.

**Structure & audience fit (Fact).** `docs/index.md` provides learning paths + a build-tag quick reference; per-feature deep dives exist for every major capability (auth, tls-acme, cache, grpc-*, stream-proxy, plugins, waf, secrets, console, observability, deployment, reload-semantics, compatibility, accessibility). `docs/configuration.md` is a full schema reference. Coverage spans new users → operators → integrators → contributors → security reviewers.

**Drift (Inference).** Low overall, but two concrete gaps surfaced by verification: (a) the `jul lint -json` schema is undocumented and inconsistent (UX-1); (b) `fmt` emitting `mail`/empty arrays contradicts the "reserved/rejected" narrative (UX-2). Neither is caught by `docs-check.py`.

### Finding DOC-1: "Soak pending" is stated everywhere but no soak *evidence* is published

- **Status (2026-07-02): ✅ Resolved (publication mechanism); follow-through open.** [`docs/soak-evidence.md`](../soak-evidence.md) publishes dated runs; CI + release soak jobs upload a `soak-results` artifact; linked from `status.md`/`index.md`. **Follow-through:** flip each `☐ pending` row to a dated `☑` only once the 5-minute release-gate artifact exists (see Top-5 #1).
- **Severity:** high (governance/trust)
- **Area:** `docs/status.md`, `docs/ga-push.md`, `docs/release.md`
- **Evidence:** 7 features carry "GA — soak pending" and a `☐ pending` soak checkbox; `release.yml` runs a blocking 5-minute soak; but no dated soak *report/artifact* is linked from the docs.
- **Fact:** The gate exists in CI; the *result* is not discoverable by an operator reading the repo.
- **Inference:** "Soak pending" is currently an unfalsifiable claim from the outside — the opposite of the project's own evidence-based ethos (ADR-0003).
- **Why it matters:** GA credibility rests on published evidence; a gate without a visible artifact reads as a promise, not proof.
- **Recommendation:** Publish a per-feature soak report (date, duration, workers, heap/goroutine deltas, error count) under `docs/` and link it from each `status.md` row; flip `☐ pending` to a dated `☑` only when the artifact exists.
- **Acceptance criteria:** Every "GA — soak pending" row links to a dated soak artifact, or is honestly relabeled.
- **Effort:** S–M (mostly publishing existing CI output)
- **Dependencies:** soak job output capture

**Recommended documentation architecture (Recommendation).** The current tree is already good; refine, don't rebuild: (1) add a top-level **"Evidence"** page aggregating per-feature matrix/bench/threat-note/soak links (the GA bar made browsable); (2) add a **Troubleshooting** page (currently absent) covering common startup/reload/TLS/ACME failures and the "static root not found" first-run error; (3) document the CLI JSON schemas; (4) add a short **migration** note for NGINX importer limitations.

---

## 9. Specs, ADRs, and roadmap coherence

**ADRs match implementation (Fact).** Spot-verified: ADR-0006 (React/Vite embedded SPA) ↔ `internal/admin/ui` + `go:embed`; ADR-0004 (validate→diff→apply→rollback, operable-by-design) ↔ admin write path; ADR-0002 (explicit adapters, GraphQL deferred) ↔ transcode-only + no GraphQL code; ADR-0005 (soak post-GA) ↔ `release.yml` blocking soak; ADR-0007/0008 honestly marked **Deferred** and correspond to the real `main.go` monolith and `x/tools` pin. This ADR↔code fidelity is excellent.

**Specs currentness (Fact).** `docs/specs/`: Year-1 "Shipped", Year-2 "In progress", Years 3–5 "Vision horizon", plus a cross-cutting `hardening-platform.md` "In progress". Roadmap (`docs/roadmap/README.md`) shows Y1 11/11, Y2 8/9 (Y2-08 GraphQL deferred, Console closed in v1.27) — consistent with `CHANGELOG` and `status.md`.

**Delivered/beta/planned/deferred separation (Fact).** Cleanly separated across `status.md` (maturity), `roadmap` (delivery), `ga-push.md` (Beta→GA execution log), and ADRs (deferred debt). This is stronger governance than most projects.

### Finding SPEC-1: The hardening backlog is real work but is tracked as prose, not a burndown

- **Status (2026-07-02): ✅ Resolved.** `docs/status.md` now carries a **GA evidence burndown (Beta)** table (feature × matrix/bench/threat/fuzz/soak with ✅/☐/n a cells + open counts).
- **Severity:** medium
- **Area:** `docs/specs/hardening-platform.md`, `docs/ga-push.md`
- **Evidence:** GA gaps per feature are enumerated in `status.md`; hardening spec is "In progress"; but there's no single tracked list mapping each Beta feature's missing matrix/bench/threat-note/fuzz to an owner/status.
- **Fact:** The *what* is documented; the *burndown* (who/when/done) is diffuse.
- **Inference:** With 13 Beta features each needing a 3–4-item evidence bundle (~45 discrete tasks), prose tracking risks silent stalls.
- **Why it matters:** GA velocity depends on making the evidence backlog executable.
- **Recommendation:** Convert `status.md` gaps into a checklist table (feature × {matrix, bench, threat-note, fuzz, soak}) with status cells; this is the same table used in §13 below.
- **Acceptance criteria:** One authoritative GA-evidence matrix exists and is updated per release.
- **Effort:** S

**Next 3–6 months coherence (Inference).** Coherent *if* the effort is finishing the GA evidence bundles for the 7 candidates and hardening the top Beta features — not starting Year-3+ items. The "vision horizon" framing correctly parks speculative work. Recommend the roadmap explicitly state "no new feature categories until the 7 GA candidates publish full evidence + fix CQ-1."

---

## 10. Testing and QA review

**Distribution (Fact).** 129 Go test files across ~40 packages; frontend 32 files/347 tests (verified). Fuzz: 4 targets (`FuzzParseJWKS`, `FuzzScriptName`, `FuzzHostScore`, `FuzzParseTemplate`). Benchmarks: 15+. Soak: 2 (`TestSoak` HTTP, `TestSoakUDPChurn`). CI enforces a 65% coverage floor + race + vulncheck + bench/fuzz/soak smoke; release adds a blocking 5-minute soak.

**Verified critical-path coverage (Fact).** Config validation, reload swap, admin bearer auth, rate limiting (middleware+admin+conn), cache SWR/SIF, upstream passive health, auth incl. JWKS stale-grace, transcode body-limit/error-map, plugin SSRF guards (loopback-always-blocked, DNS-rebinding), TLS/mTLS, ACME OCSP graceful-fail — all present and passing in isolation.

**Gaps (Fact/Inference — the "merge-safe bar" is not fully met).**

### Finding QA-1: High-value concurrency/negative tests are missing

- **Status (2026-07-02): ◐ Partially addressed.** Added: transcode reflection-negative (`TestTranscodeReflectionRejectsUnreflectiveBackend`), plugin reload-under-load (`TestReloadUnderLoad`), concurrent apply+rollback on **both** endpoints (`TestConfigApplyRollbackConcurrent`, extended for REG-1), and the Console e2e smoke. Cache single-flight already existed. **Remaining:** CLI `import` golden + `run` runtime smoke; ACME cert-rotation-under-concurrent-handshake test; raise the server/admin coverage floor toward 70%.
- **Severity:** high
- **Area:** `internal/server`, `internal/admin`, `internal/plugins`, `internal/transcode`, `internal/cache`
- **Evidence:** No test for: concurrent patch+reload+rollback under load; plugin hot-reload with in-flight requests; gRPC reflection-abuse negative path (descriptor-less/untrusted); cache concurrent-revalidation storm; ACME cert rotation during concurrent handshakes; CLI `import`/`serve` runtime behavior (only lint/check/fmt covered). CQ-1 shows the concurrency surface is under-exercised.
- **Fact:** Individual behaviors are tested; their concurrent/adversarial combinations are not.
- **Inference:** These are exactly where the hardest bugs (and CQ-1) live.
- **Why it matters:** GA/soak claims imply resilience under concurrent operator actions and traffic.
- **Recommendation:** Add: (1) a concurrency test that fires patch/apply/rollback while proxy traffic flows; (2) a plugin-reload-under-load test; (3) a transcode negative test rejecting untrusted/descriptor-less reflection; (4) a cache revalidation-stampede test asserting single-flight; (5) CLI import golden + run smoke; (6) `goleak` in `internal/server` (see CQ-1).
- **Acceptance criteria:** All six exist and are green in CI; coverage floor raised toward 70% for `server`/`admin`.
- **Effort:** L
- **Dependencies:** CQ-1 fix

**CI matrix & gates (Fact).** `.github/workflows/ci.yml` (build/test lean+full, race, coverage-floor, bench/fuzz/soak smoke) and `release.yml` (gate + blocking soak + 8-cell signed/attested matrix + SBOM) are strong. **Caveat:** CI runs on Linux; CQ-1 reproduced on Windows under parallel execution — the matrix should add a Windows test lane to catch platform-specific lifecycle bugs.

**False-confidence risks (Inference).** The 65% floor + smoke soak can be green while CQ-1 lurks (flaky, platform-specific) and while concurrency negatives are absent. Coverage % overstates confidence for `server`/`admin`.

---

## 11. Security and operations review

**Trust model (Fact, `SECURITY.md`).** Config + binary + filesystem trusted; downstream clients untrusted; configured upstreams trusted-by-default. Core invariant: **request input never selects upstream/JWKS/cert/FastCGI root** → no SSRF by design. Verified consistent with code (static config-derived targets).

**Verified protections.**
- **Plugin egress (Fact).** `internal/plugins` hardening tests: allow-list enforcement, **loopback always blocked even if allow-listed**, private/link-local/shared ranges blocked, **DNS-rebinding** re-validation of resolved IP. Strong.
- **Admin auth (Fact).** Loopback-bound, bearer token, constant-time compare; `/healthz|/readyz` open; per-client read/write/apply rate limits + SSE connection cap; audit log of mutations.
- **Vuln scan (Fact, verified).** `govulncheck` full tags → **no vulnerabilities**.
- **Secrets (Fact).** `${env|file|secret}` refs resolved at runtime; unresolved in on-disk/Console config; global log redaction via `internal/redact`; atomic 0600 writes.
- **Hardening (Fact).** Distroless nonroot container (UID 65532, static binary); systemd unit with `ProtectSystem=strict`, `NoNewPrivileges`, `CAP_NET_BIND_SERVICE`-only, `MemoryDenyWriteExecute`, `DynamicUser`, 0700 dirs, `LimitNOFILE=65536`, crash-loop guard.
- **Release integrity (Fact).** Sigstore keyless signing + provenance attestation + SPDX SBOM + SHA256SUMS.

**Prioritized security risks.**

### Finding SEC-1: Plugin `.wasm` upload endpoint is a privileged write surface — confirm defense-in-depth

- **Status (2026-07-02): ✅ Resolved (upload hardening); capability-revalidation note open.** `validPluginFilename` enforces a `<name>.wasm` safe-charset name with no separators/`..`, plus a dest-inside-dir containment check; negative tests in `plugin_upload_test.go`; threat note in [`plugins.md`](../plugins.md#uploading-modules-console-api). **Remaining (low):** an explicit capability-re-validation-on-activation test, as originally recommended.
- **Severity:** medium
- **Area:** [`internal/admin`](../../internal/admin) `POST /api/plugins/upload`, `plugin_upload_dir`, `plugin_upload_max_size`
- **Evidence:** CHANGELOG (Unreleased) + admin API expose `.wasm` upload with atomic writes and a size cap; plugins run in a wazero sandbox with capability gating.
- **Fact:** Upload is bearer-auth'd + size-capped + atomic; execution is sandboxed with egress guards.
- **Inference:** The residual risk is (a) filename/path handling on upload and (b) whether an uploaded module's declared capabilities are re-validated against policy before activation.
- **Why it matters:** Uploading executable code via the admin plane is the highest-consequence write; auth compromise → arbitrary (sandboxed) code.
- **Recommendation:** Verify (and test) that upload rejects path traversal / non-`.wasm` content, enforces `plugin_upload_max_size` before buffering, and that capability grants are re-checked on activation, not just at upload; add a threat note to `docs/plugins.md`/`docs/console.md`; document that admin-token compromise ⇒ sandboxed-RCE and recommend keeping admin loopback-only.
- **Acceptance criteria:** Negative tests for traversal/oversize/non-wasm; documented threat note; capability re-validation test.
- **Effort:** M
- **Dependencies:** none

### Finding SEC-2: CQ-1 connection/goroutine leak is also a resilience concern

- **Status (2026-07-02): ◐ Partially addressed.** The leak itself is fixed (CQ-1: goleak guard + keep-alive-free clients). **Remaining:** the reload-under-load soak scenario (M-4) that asserts steady goroutine/heap across repeated reloads under traffic is not yet added.
- **Severity:** medium (cross-ref CQ-1)
- **Area:** `internal/server` drain/reload
- **Fact/Inference:** If the intermittent hang reflects a real keep-alive/drain leak, sustained reloads under load could accumulate stuck connections/goroutines in production.
- **Recommendation:** Resolve via CQ-1 + add a soak scenario that reloads repeatedly under traffic and asserts steady goroutine/heap.
- **Acceptance criteria:** Reload-under-load soak stable for 5+ minutes.
- **Effort:** M (folds into CQ-1/QA-1)

**Operations readiness (Inference).** Deployment story (Docker + systemd + Windows service) is genuinely production-grade. Gaps are observability-of-evidence (soak publication, DOC-1) and a Troubleshooting doc, not missing controls.

---

## 12. Product and marketing perspective

**Positioning (Recommendation).** Lead with what is *true and differentiated*: **"The lean, single-binary edge server with a first-class operations Console and a built-in gRPC↔JSON gateway."** This is defensible, evidence-backed, and avoids the losing "most powerful" framing the project already rejected internally.

**Differentiation that is real marketing proof (Fact-backed).**
- **Single static binary, no runtime deps** — including a React Console embedded via `go:embed` (no Node at runtime). Strong, verifiable.
- **Operable-by-design Console** — validate→diff→apply→rollback with history; 347 passing UI tests. This is a genuine advantage over NGINX (no UI) and even Caddy.
- **gRPC gateway** — transcoding + passthrough + h2c as a GA-candidate flagship.
- **Honest maturity model** — the "implemented ≠ GA" discipline is itself a trust signal to serious operators.
- **Supply-chain hygiene** — signed, attested, SBOM'd releases; clean `govulncheck`.

**What NOT to overclaim.**
- Do **not** market the 13 Beta features (WAF, plugins, HTTP/3, stream, discovery) as production-ready; label them Beta in the README feature table, matching `status.md`.
- Do **not** claim "GA" until CQ-1 is fixed and soak evidence is published (DOC-1). "GA — soak pending" is fine internally but should not become "GA" in marketing before the artifact exists.
- Do **not** imply AI-gateway/fleet/mesh capabilities — keep them "vision horizon."

**Adoption path / demos (Recommendation).**
- **Demo 1 (ergonomics):** `jul run --serve .` → HTTPS in under a minute; then open the Console and change a route with diff/apply/rollback. Directly showcases the two differentiators.
- **Demo 2 (gateway):** REST client → gRPC backend via transcoding, no code. The flagship.
- **Demo 3 (migration):** `jul import nginx nginx.conf` → run, with the unmapped-directive report. Targets the NGINX-migration audience.

**Release-announcement themes.** "Single binary, real Console, honest maturity." **Community prompts:** solicit which Beta features to prioritize for GA (demand-gating in public). **Roadmap communication:** publish the GA-evidence matrix (§13) so adopters can see exactly what "GA" will mean.

---

## 13. Prioritized backlog

> **Implementation status (updated 2026-07-02).** The immediate and near-term
> backlog has been worked down; the 2026-07-02 reaudit extended it (REG-1 fix,
> CQ-3 file splits, NEW-2 example cleanup) — see the reaudit addendum below the list:
>
> - **P0-1 ✅** `internal/server` flaky hang fixed — root cause was the
>   `fetch`/`reachable` test helpers pooling keep-alive connections via
>   `http.DefaultTransport`; now a keep-alive-free client plus a
>   `goleak.VerifyTestMain` guard (`internal/server/main_test.go`).
> - **P0-2 ✅** Soak evidence published — [`docs/soak-evidence.md`](../soak-evidence.md)
>   with dated runs; CI + release soak jobs now upload a `soak-results` artifact.
> - **P1-1 ✅** `jul lint -json` schema stabilized — lowercase keys + string
>   severity (`config.Diagnostic` tags + `Severity.MarshalJSON`), golden test,
>   documented in [`configuration.md`](../configuration.md#cli-json-output).
> - **P1-2 ✅** Concurrency/negative tests added — transcode reflection-negative,
>   plugin reload-under-load, admin concurrent apply/rollback + Console e2e.
>   Rollback serialization: v1.1 fixed only `handleHistoryRollback`; the
>   **2026-07-02 reaudit found and fixed the second, Console-facing endpoint**
>   `handleConfigRollback` — both now share one `applyMu`-guarded helper (Finding REG-1).
> - **P1-3 ✅** Plugin upload hardening — strict `<name>.wasm` filename validation
>   + containment check + threat note in [`plugins.md`](../plugins.md#uploading-modules-console-api).
> - **P1-4 ✅** Windows CI test lane added (lean + full matrix).
> - **P2-2 ✅** GA-evidence burndown table added to [`status.md`](../status.md#ga-evidence-burndown-beta).
> - **P2-3 ✅** Backend↔Console apply/rollback e2e smoke added.
> - **P2-4 ✅** `jul fmt` no longer emits reserved/empty tables (`omitempty`).
> - **P2-5 ✅** First-run no-config hint + [`troubleshooting.md`](../troubleshooting.md).
> - **P2-1 ◐** `internal/app` factory package seam created with unit-tested wiring
>   helpers; the full `main.go` reduction to <250 LOC remains staged (ADR-0007).
> - **P2-6 ⏸** Beta evidence bundles remain demand-gated; the burndown above is
>   the tracker.
>
> **Reaudit addendum (2026-07-02).**
> - **REG-1 ✅** Both rollback endpoints (`/api/history/rollback`, `/api/config/rollback`)
>   now route through one `applyMu`-guarded `rollbackToSnapshot`; the concurrency
>   test (`TestConfigApplyRollbackConcurrent`) is a subtest matrix over both.
> - **CQ-3 ✅** `admin/api.go` 1214→502 and `config/validate.go` 1005→561, split into
>   `api_status.go`/`api_history.go`/`api_wizard.go` and
>   `validate_location.go`/`validate_backends.go` (all <600 LOC; tests green).
> - **NEW-2 ✅** Stale `stream = []` / `mail = []` lines removed from
>   `examples/migrate/jul.toml` and `server.full.apps.toml`.
> - **CQ-2 ◐** `main.go` down to ~858 LOC; `internal/app` now also holds
>   `admin_deps.go` + `preflight.go` (+ `preflight_test.go`, `characterization_test.go`).
>   The `<250` target remains; ADR-0007 is already updated to *Partial* (2026-07-02).

### Remaining backlog — re-sequenced (2026-07-02)

This is the **current forward plan**; it supersedes the original v1.0 tables below (kept for history — most of their rows are now ✅, per the status block above). Phases are ordered by dependency and risk-reduction.

**Phase A — GA-soak credibility (P0/P1, before any "GA" claim hardens).**

| ID | Title | Area | Sev | Impact | Effort | Deps | Acceptance |
|---|---|---|---|---|---|---|---|
| A-1 (DOC-1 f/u) | Publish the 5-min release-gate soak artifact per GA candidate; flip `☐ pending`→dated `☑` in `status.md` | docs/CI | high | Makes GA claims verifiable | S–M | a tagged run | Every GA row links a dated artifact |
| A-2 (M-4/SEC-2) | Reload-under-load soak scenario (steady goroutine/heap across repeated reloads under traffic) | server/CI | med | Prod reload resilience | M | — | 5-min stable, published |
| A-3 (QA-1 rem.) | CLI `import` golden + `run` runtime smoke; ACME rotation-under-handshake test | cmd/server | med | Closes residual merge-safe gap | M | — | tests green in CI |

**Phase B — Architecture debt & coverage (P2).**

| ID | Title | Area | Sev | Impact | Effort | Deps | Acceptance |
|---|---|---|---|---|---|---|---|
| B-1 (CQ-2 finish) | Trim `main.go` toward <250 LOC; flip ADR-0007 Deferred→In-progress | cmd/jul, docs/adr | low | Full composition-root testability | M | internal/app (done) | main.go <250; ADR updated |
| B-2 (QA-1/M-3) | Raise server/admin coverage floor toward 70% | CI/tests | low | Confidence where thin | M | A-3 | floor raised, green |
| B-3 (SEC-1 rem.) | Plugin capability re-validation-on-activation test | admin/plugins | low | Defense-in-depth completeness | S | — | test green |
| B-4 (UI-1 rem.) | Browser (Playwright) Console smoke against the built SPA | admin/ui | low | Visual/contract e2e | M | — | one headless job green |

**Phase C — Beta→GA evidence & supply chain (P2/medium, demand-gated).**

| ID | Title | Area | Sev | Impact | Effort | Deps | Acceptance |
|---|---|---|---|---|---|---|---|
| C-1 (P2-6/SPEC-1) | Work Beta evidence bundles highest-signal first (cache poisoning, WAF FP/bypass, plugin sandbox, HTTP/3 amplification) | multi | med | Advances Beta→GA | L (per feature) | — | burndown rows close |
| C-2 (M-1/CQ-4) | Resolve/track `x/tools` pin; scheduled `govulncheck` on pinned graph | deps | med | Removes latent supply-chain trap | M–L | upstream gofast | pin removed or monitored trigger |

**Strategic bets (S-1..S-4) are unchanged and remain demand-gated (see the table under "Strategic bets" below).**

**Dependency notes.** A-1 gates flipping the GA labels; A-2 depends on the CQ-1 fix (done); B-2 depends on A-3 landing the new tests; B-1 is unblocked now that `internal/app` exists. C-* are demand-gated and must not precede Phase A.

### Documentation dependency plan (2026-07-02)

Because this audit is the single source of truth, the downstream doc updates each finding/phase requires are tracked here. "Timing" is relative to the related code change.

| Finding / Phase | Docs affected | Required update | Timing | Current state | Canonical until updated |
|---|---|---|---|---|---|
| REG-1 (done) | `CHANGELOG.md`; this audit | Record the rollback-serialization completion + the api_history split | with code (this change) | audit updated; CHANGELOG updated this cycle | audit |
| CQ-3 (done) | `CHANGELOG.md`; this audit | Note the `api.go`/`validate.go` splits | with code | audit updated; CHANGELOG updated | audit |
| NEW-2 (done) | `examples/migrate/jul.toml`, `server.full.apps.toml` | Remove stale empty-table lines | done | reconciled | — |
| CQ-2 / B-1 | `docs/adr/0007-*.md` | Finalize status (already **Partial** as of 2026-07-02) once the `buildHandlers`/`serve()` extraction completes | with the `main.go` trim | ADR current (Partial) | audit + ADR-0007 |
| DOC-1 / A-1 | `docs/status.md` (soak rows), `docs/soak-evidence.md` | Flip `☐ pending` → dated `☑`; append the release-gate run | after a tagged release soak | rows still `☐`; smoke evidence only | audit + `soak-evidence.md` |
| QA-1 / A-3 | `docs/testing`-adjacent notes (none required) + this audit | Record the new CLI/ACME tests | with code | pending | audit |
| SEC-1 rem. / B-3 | `docs/plugins.md` | Add a line on capability re-validation at activation once tested | with code | threat note present; revalidation line pending | audit + `plugins.md` |
| C-1 (Beta bundles) | per-feature docs (`docs/<feature>.md`) + `docs/status.md` burndown | Add matrix/bench/threat-note per feature; close burndown cells | with each bundle | burndown `☐` rows open | `status.md` burndown |
| C-2 (`x/tools`) | `docs/adr/0008-*.md`, `go.mod` | Record trigger firing / resolution | when triggered | deferred by design | ADR-0008 |
| (from §15) README repositioning | `README.md` | "lean single-binary + Console + gRPC gateway"; mark Beta features Beta | product pass | not yet done | audit §12 |

Do **not** update unrelated docs opportunistically; each row above is the explicit, tracked dependency. Until a downstream doc is updated, **this audit remains canonical** for that item.

---

### Immediate / P0–P1 (original v1.0 backlog — historical; mostly ✅, see status block)

| ID | Title | Area | Sev | Impact | Effort | Deps | Acceptance | Owner |
|---|---|---|---|---|---|---|---|---|
| P0-1 | Fix intermittent `internal/server` hang (CQ-1) | server | high | Restores CI/local trust; possible prod drain fix | M | — | 50 parallel runs + goleak, zero hangs | backend |
| P0-2 | Publish per-feature soak evidence (DOC-1) | docs/CI | high | Makes "GA — soak pending" verifiable | S–M | soak output | Each GA row links dated artifact | QA/docs |
| P1-1 | Stabilize `jul lint -json` schema (UX-1) | cmd/config | med | Unblocks CI automation | S | — | lower-case keys, string severity, golden test | backend |
| P1-2 | Add high-value concurrency/negative tests (QA-1) | server/admin/plugins/transcode/cache | high | Closes merge-safe gap | L | P0-1 | 6 tests green in CI | QA/backend |
| P1-3 | Plugin upload hardening + threat note (SEC-1) | admin/plugins | med | Secures highest-consequence write | M | — | traversal/oversize/non-wasm negatives + docs | security |
| P1-4 | Add a Windows CI test lane | CI | med | Catches platform lifecycle bugs (CQ-1 class) | S | — | Windows job green | QA |

### Near term / P2 (original v1.0 backlog — historical; see status block for completion)

| ID | Title | Area | Sev | Impact | Effort | Deps | Acceptance | Owner |
|---|---|---|---|---|---|---|---|---|
| P2-1 | Extract testable factory from `main.go` (CQ-2) | cmd/jul | med | Testability of wiring | L | — | main.go <250 LOC, factory tested | backend |
| P2-2 | GA-evidence matrix (SPEC-1) | docs | med | Executable GA burndown | S | — | feature×evidence table maintained | product/docs |
| P2-3 | Backend↔Console e2e smoke (UI-1) | admin/ui | med | Protects primary operator surface | M | — | apply/rollback e2e green | frontend/QA |
| P2-4 | `jul fmt` omit reserved/empty tables (UX-2) | cmd/jul | low | Cleaner canonical config | S | — | golden test | backend |
| P2-5 | Troubleshooting doc + first-run hint | docs/cmd | low | Onboarding friction | S | — | doc + `jul` no-config hint | docs |
| P2-6 | Beta-feature evidence bundles (per §5) | multi | med | Advances Beta→GA | L (per feature) | — | matrix+bench+threat-note per feature | backend/docs |

### Medium term (foundational)

| ID | Title | Area | Sev | Impact | Effort | Deps | Acceptance | Owner |
|---|---|---|---|---|---|---|---|---|
| M-1 | Resolve/track `x/tools` pin (CQ-4) | deps | med | Removes latent supply-chain trap | M–L | upstream gofast | pin removed or monitored trigger | backend/security |
| M-2 | Split admin/validate files (CQ-3) | admin/config | low | Maintainability | M | — | no file >600 LOC | backend |
| M-3 | Raise coverage floor for server/admin toward 70% | CI/tests | low | Confidence where it's thin | M | P1-2 | floor raised, green | QA |
| M-4 | Reload-under-load soak scenario (SEC-2) | server | med | Prod reload resilience | M | P0-1 | 5-min stable | QA |

### Strategic bets (demand-gated)

| ID | Title | Area | Impact | Effort | Notes |
|---|---|---|---|---|---|
| S-1 | GraphQL composition adapter | transcode | Broadens gateway story | XL | Keep deferred until demand (ADR-0002) |
| S-2 | Fleet/multi-node control plane | new | Opens open-core tier | XL | Vision horizon; do not start pre-GA |
| S-3 | AI gateway / semantic cache MVP | new | Narrative upside | XL | Time-boxed bet only; no core coupling |
| S-4 | Plugin marketplace/registry | plugins | Ecosystem | L–XL | Gate on plugin GA + sandbox threat note |

---

## 14. Recommended roadmap evolution

**Next 1–2 weeks — reliability + evidence (freeze features).**
- *Hardening:* Fix CQ-1 (P0-1); add goleak + Windows CI lane (P1-4).
- *Docs/product clarity:* Publish soak evidence (P0-2); build the GA-evidence matrix (P2-2).
- *UX:* Fix `jul lint -json` (P1-1).

**Next 1–2 months — merge-safe bar + GA candidates.**
- *Hardening:* Concurrency/negative tests (P1-2); plugin upload hardening (P1-3); reload-under-load soak (M-4).
- *Product:* Move the 7 candidates from "soak pending" to GA *only* as each publishes its evidence bundle + passes reload-under-load.
- *UX:* Console e2e smoke (UI-1); `fmt` cleanup (UX-2); troubleshooting doc.
- *Marketing:* Ship Demo 1–3; announce with "single binary, real Console, honest maturity."

**Next quarter — Beta→GA burndown + architecture debt.**
- *Features/hardening:* Work the Beta evidence bundles by demand (P2-6), highest-signal first (cache poisoning note, WAF FP/bypass, plugin sandbox, HTTP/3 amplification).
- *Architecture:* Extract factory from `main.go` (CQ-2); resolve `x/tools` pin (M-1).
- *Docs:* Evidence page; migration notes.

**Longer-term horizon (demand-gated).** GraphQL adapter, fleet control plane, AI-gateway MVP, plugin registry — each entered only after validated demand and only after the 7 GA candidates are truly GA. Keep the "vision horizon" label until then.

Separation to preserve: **hardening** (CQ/QA/SEC), **product clarity** (status/roadmap/evidence), **docs**, **UX** (Console/CLI), **features** (Beta bundles), **marketing/adoption** (demos/announcements) — track them as distinct lanes so reliability work is never traded for feature ambition.

---

## 15. File-by-file / area-by-area action list

- **`cmd/jul/main.go`** — ✅ `internal/app` factory extracted (wiring/admin-deps/preflight) + first-run no-config hint (P2-5); ◐ remaining: extract the `buildHandlers`/`serve()` body to reach <250 LOC (CQ-2).
- **`cmd/jul/cli.go` + `internal/config/lint.go`** — Add json tags to `Diagnostic`, string severity, golden test (UX-1). Fix `fmt` to omit reserved/empty tables (UX-2).
- **`internal/server/server.go` (+ tests)** — Diagnose drain/keep-alive leak (CQ-1); add `goleak` `TestMain`, per-test timeouts, reload-under-load soak (M-4/SEC-2).
- **`internal/admin/api.go`** — ✅ Split by resource group into `api_status.go`/`api_history.go`/`api_wizard.go` (CQ-3). ✅ Concurrent apply/rollback test across both rollback endpoints (QA-1/REG-1).
- **`internal/admin/` (plugin upload)** — Path-traversal/oversize/non-wasm negatives + capability re-validation on activation (SEC-1).
- **`internal/admin/ui/`** — Add backend↔frontend e2e smoke (UI-1); confirm `MaturityBadge` on every Beta panel.
- **`internal/transcode/`** — Negative test for untrusted/descriptor-less reflection (QA-1).
- **`internal/plugins/`** — Reload-under-load atomicity test (QA-1); sandbox threat note.
- **`internal/cache/`** — Concurrent-revalidation single-flight test (QA-1); cache-poisoning/isolation threat note.
- **`internal/config/validate.go`** — ✅ Split by concern into `validate_location.go` and `validate_backends.go` (CQ-3).
- **`go.mod` / ADR-0008** — Track/resolve `x/tools` pin; scheduled `govulncheck` on pinned graph (CQ-4).
- **`docs/status.md`** — Convert GA gaps to an evidence matrix; link soak artifacts (DOC-1/SPEC-1).
- **`docs/roadmap/README.md`** — State "no new categories until 7 GA candidates publish evidence + CQ-1 fixed."
- **`docs/configuration.md`** — Document CLI JSON schemas.
- **`docs/` (new)** — Add Troubleshooting + Evidence pages.
- **`docs/adr/`** — Update ADR-0007 to In-progress once factory extracted; add trigger to ADR-0008.
- **`scripts/docs-check.py`** — Extend to validate CLI `-json` schema stays documented.
- **`.github/workflows/ci.yml`** — Add Windows test lane; capture+publish soak output; consider `-count`/goleak for `server`.
- **`Makefile`** — Add a `test-race-repeat` target for flake hunting; keep `ci-full` parity.
- **`README.md`** — Reposition to "lean single-binary + Console + gRPC gateway"; mark Beta features Beta in the feature table.

---

## 16. Uncertainties and verification gaps

**Verified locally (Windows/amd64, go1.26.4):** lean + full builds (pass); `go test` on config/server/admin/auth/cache/upstream/middleware/router/cmd (server flaky-hung once, passed isolated; rest pass); tag-gated transcode/stream/plugins/waf/handler/observability (pass); `jul check/lint/fmt/-help/-version`; `govulncheck` full tags (no vulns); Console `typecheck`/`eslint`/`vitest` (347 pass); file line counts.

**Not verified / open questions:**
1. **CI logs not accessed** — CI/release workflow *behavior* inferred from YAML, not from actual run logs; green-CI status assumed, not observed.
2. **Coverage % not recomputed** — the 65% floor and per-package coverage are taken from CI config, not re-measured here.
3. **Soak/fuzz not run to completion** — only smoke-scale reasoning; the 5-minute release soak and long fuzz were not executed locally.
4. **CQ-1 root cause not isolated** — reproduced once, characterized as flaky; the exact leaking test and whether it reflects a production drain bug are unconfirmed.
5. **Console not driven in a browser** — UI reviewed via code + tests; visual states, Beta-label placement, false affordances, and copy were not manually exercised.
6. **Security posture inferred, not pen-tested** — trust model and guards read from code/tests + `govulncheck`; no adversarial testing (admin auth bypass, plugin sandbox escape, cache poisoning, WAF bypass, ACME/QUIC edge cases).
7. **Platform coverage partial** — Windows/amd64 only; Linux/macOS and arm64 behavior (esp. HTTP/3 QUIC, UDP stream, syslog sinks) not validated here.
8. **`import`/`serve`/`run` runtime paths** — only `check/lint/fmt` were exercised; importer translation fidelity and `run --serve/--proxy` runtime not tested.
9. **Third-party dep audit** — beyond `govulncheck`, no manual review of `gofast`, `coraza`, `wazero`, quic-go versions for maintenance health.

These gaps are the reason several findings are labeled Inference; none of them soften the two headline items — **fix CQ-1** and **publish soak evidence** — which are the clearest path from "well-built" to "trustably GA."
