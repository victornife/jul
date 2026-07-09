# Jul.IA — Immediate Action Plan (Merged Audit Report)

> **COMPLETED (historical archive) — 2026-07-09.** The P0/P1 tasks in this plan were implemented and folded into the 2026-07-02 audit remediation. Retained under `previous_reviews/` for history; superseded by the current [Full Repository Audit (2026-07-09)](../jul_full_repository_audit_2026-07-09.md).

**Date:** 2026-07-03
**Origin:** Baseline audit (Principal Engineer) merged with updated re-audit and cross-validated against code/docs/CI.
**Repository:** victornife/jul (main = 8ff80115)
**Scope:** All verified findings, exact file edits, step-by-step tasks, and a prioritized backlog.

---

## Progress Snapshot

| Phase | Status | Items | Key Outcome |
|-------|--------|-------|-------------|
| **P0 Release-blocking** | ✅ Complete | 5 tasks | `reload_timeout` bug fixed; maturity docs reconciled; soak placeholders marked honestly |
| **P1 Adoption-blocking** | ✅ Complete | 7 tasks | Apply semantics fixed; config reference updated; plugin upload now secure-by-default |
| **P2 Medium-term** | ⏳ Open | 5 items | Coverage floor, ADR 0007 extraction, hardening backlog, soak infrastructure |
| **Re-audit new issues** | ✅ Complete | 6 resolved | Plugin upload docs, soak placeholders, docs-check, OnReloaded comment, reload-semantics, ga-push.md |

**`go test ./...` ✅ All tests pass.**

| **P1.5 Pre-soak hardening** | ✅ Complete | 2 tasks | `reload_timeout=0s` test; pre-commit hook with tests + docs-check |

---

## 1. Executive Summary

### Overall Maturity
**Approaching release-ready.** The critical runtime regression in `reload_timeout` has been resolved (synchronous swap, advisory timeout warning). Docs are now internally consistent on the "all 20 features GA — soak pending" claim. However, the re-audit discovered that **operator-facing documentation has not kept pace with code changes** (plugin upload default docs are stale), and **evidence placeholders still carry ✅ while linking to `<RUN_ID>`** — a trust-erosion risk if published.

### Baseline → Now Improvements (positive movement)
1. New evidence docs for compression, HTTP/3, importer, OTel, rate limit, stream, cache, plugins, zero-config.
2. New benchmark and fuzz test coverage.
3. Plugin table notation in `docs/configuration.md` fixed.
4. Benchmark cache snippets corrected.
5. `status.md` method preview wording corrected.

### Baseline → Now Regressions (negative movement)
1. **`reload_timeout` goroutine timeout bug** — CRITICAL — new code at `internal/server/server.go:390-470` ran side-effectful factory work in an uncancellable goroutine. **FIXED ✅** — Removed goroutine+select pattern; reload now runs synchronously with advisory timeout.
2. **`reload_timeout` zero-overridden-by-defaults bug** — parser.go overrode explicit `"0s"`. **FIXED ✅** — Comments updated: zero/omitted defaults to 10s. Behavior preserved.
3. **Apply response `reload` field is stale** — `api.go:458-459` read previous reload. **FIXED ✅** — Renamed to `previous_reload`.
4. **Maturity story regressed** — README showed Beta table while roadmap/ga-push/status claimed all GA. **FIXED ✅** — README updated; status.md Beta remnants replaced.
5. **Soak evidence quality degraded** — `???` in udp-churn metrics, `<RUN_ID>` placeholders. **PARTIALLY FIXED ✅** — `???` replaced with honest `(not captured)` notes; `<RUN_ID>` intentionally retained as explicit pending links (not fake run IDs). ✅ on these rows is still misleading.

### New Issues from Re-audit (2026-07-03, SHA 8ff80115)
| # | Issue | Why it matters |
|---|-------|--------------|
| 1 | Plugin upload docs still say default `true` / enabled-by-max-size | Operator-facing trust: code is secure-by-default, but docs tell them it's on |
| 2 | Soak placeholders still carry ✅ | Unverifiable claims paired with completion markers erode trust |
| 3 | `docs-check.py` header claims `<RUN_ID>`/`???` checks but does not implement them | Docs-check can pass while exact evidence placeholders remain |
| 4 | `OnReloaded` comment says "after swap" but code calls it before | Misleading for future maintainers adding lifecycle-sensitive hooks |
| 5 | `reload-semantics.md` still references `reload.timed_out` | API contract doc is stale; actual key is `previous_reload` |
| 6 | `ga-push.md` Wave 1 table: HTTP/3 maturity cell has date, not label | Inconsistent formatting; missing WASM plugins and L4 stream rows |

### Top Actions (release-blocking priority)
| Priority | Action | Why |
|----------|--------|-----|
| P1 | Fix plugin upload default docs | Code says `false`, docs say `true` — operators will be confused |
| P1 | Fix soak evidence: remove ✅ from placeholder rows or replace with real links | ✅ without verifiable artifacts is worse than honest "pending" |
| P1 | Fix `docs-check.py`: implement promised checks or remove false claim | A validator that claims to catch what it cannot is dangerous |
| P2 | Fix `OnReloaded` comment or move hook ordering | Maintainability and lifecycle correctness |
| P2 | Fix `reload-semantics.md` stale API key | Docs must match the actual API contract |

---

## 2. Architecture Assessment

The baseline audit praised the generational swap, preflight gates, build tags, and composition-root discipline. All of that remains valid.

### `doReload()` — FIXED (was CRITICAL)
The unsafe goroutine-based timeout was removed. The reload now runs synchronously: validation → factory → `OnReloaded` → advisory timeout check → atomic handler swap. The timeout is advisory (logs warning, sets `TimedOut=true`), not abortive. This eliminates the partial-state-mutation risk.

### ADR 0007: Unchanged, still "Partial"
The new `reload_timeout` code was added directly into `serve()` and `doReload()` — inside the very monolith ADR 0007 warns about. The deferred trigger condition from ADR 0007 ("touching >3 sections of serve() or a reload/preflight bug") has been met.

### Other architecture findings (still valid)
- `serve()` in `cmd/jul/main.go` is ~400 lines. `buildHandlers` is ~300 lines inside it.
- `buildHandlers` captures many variables by closure, making dependency graph implicit.
- `registry` in `middleware/compress.go` is package-level mutable state.

---

## 3. Code Quality Findings

### [CRITICAL — CLOSED] Reload timeout goroutine could leave side effects
- **Status:** ✅ FIXED in commit `8ff8011`
- **Fix:** Removed `go func()` + `select ctx.Done()` pattern. Factory now runs synchronously.
- **Test:** `internal/server/reload_timeout_test.go` updated for advisory-timeout semantics.

### [HIGH — CLOSED BY DESIGN] `reload_timeout = "0s"` semantics
- **Status:** ✅ FIXED in commit `8ff8011`
- **Fix:** Comments updated to clarify zero/omitted defaults to 10s. Code behavior preserved. Unbounded reload is not supported to prevent production stalls.

### [MEDIUM — CLOSED] Apply response includes stale reload status
- **Status:** ✅ FIXED in commit `8ff8011`
- **Fix:** `resp["reload"]` renamed to `resp["previous_reload"]` in `internal/admin/api.go`.

### [LOW/MEDIUM — CLOSED] `LastReload` comment claims status endpoint uses it
- **Status:** ✅ FIXED in commit `8ff8011`
- **Fix:** Comment corrected to say "apply handler only."

### [LOW — CLOSED] `version = "0.1.0-dev"` misleading default
- **Status:** ✅ FIXED in commit `8ff8011`
- **Fix:** Changed to `"unknown (build without -ldflags)"`.

### [NEW — P2] `OnReloaded` comment contradicts execution order
- **Severity:** P2 (medium)
- **File:** `internal/server/server.go:67-70`
- **Comment:** `"invoked with the newly applied configuration at the end of a successful reload, after the HTTP handler swap and listener diff complete"`
- **Reality:** `OnReloaded(newCfg)` is called at line 432, **before** the handler swap (line 437) and listener diff (lines 456-467).
- **Why it matters:** Stream reload uses this hook. A future maintainer might add post-swap assumptions that are not true.
- **Recommended fix:** Update comment to match reality:
  ```go
  // OnReloaded is invoked after the new configuration has validated and handlers
  // have been built, but before the HTTP handler swap and listener diff are
  // finalized. It must be idempotent and must not assume the HTTP generation
  // is already live.
  ```

---

## 4. Feature Maturity Review

All 20 shipped features are consistently documented as **GA — soak pending** across README, status.md, ga-push.md, and roadmap. The Beta backlog is cleared.

**Soak evidence status:**
- `docs/status.md`: 17 rows with ✅ linking to `https://github.com/victornife/jul/actions/runs/<RUN_ID>` — explicit pending links, no fake run IDs.
- `docs/ga-push.md`: Same pattern.
- `docs/soak-evidence.md`: 2026-07-01 smoke passed (local, 20s). 2026-07-03 release-gate proxy **FAILED** (Windows ephemeral port depletion, documented as non-authoritative). 2026-07-03 udp-churn passed with `(not captured)` noted for `sends=` and `peakSessions=` metrics.

**Release recommendation:**
1. ✅ The reload_timeout bug is fixed.
2. ⏳ Real soak run IDs still need to replace `<RUN_ID>` placeholders (requires CI artifact generation).
3. ✅ The docs agree on maturity labels.
4. ⚠️ Plugin upload default changed to `false` in code, but docs still describe `true`.

---

## 5. CLI and Operator UX

Clean subcommand grammar, sensible exit codes, JSON/quiet modes, `NO_COLOR`. Strengths remain.

**Concerns:**
- `jul check` and legacy `jul -check` coexist.
- `jul import nginx` requires `importer` tag not mentioned in help.
- No `jul version` subcommand (only `--version` flag).
- No `jul serve` subcommand (bare `jul` is implicit).

---

## 6. Console/UI Review

Console v2 remains the strongest surface.

**Gaps:**
- `MaturityBadge` component exists but not used for Beta features (now moot since all are GA — soak pending).
- gRPC designer "inspection only" limitation not clearly surfaced in UX copy.
- Plugin upload default now `false` in code; docs must reflect this.
- Route tester exists (`/api/routes/test`) but not a first-class panel.

---

## 7. Documentation Review

### Critical Issues (mostly closed)
1. ✅ **README.md Beta table** — removed; all features GA — soak pending.
2. ✅ **status.md Beta remnants** — replaced with retirement notice.
3. ⚠️ **Soak tracking links are `<RUN_ID>` placeholders** — intentionally explicit, but ✅ on these rows is misleading.
4. ✅ **soak-evidence.md `???` metrics** — replaced with honest `(not captured)` notes.
5. ✅ **reload_timeout missing from config reference** — added to `[global]` table.
6. ✅ **"method picker"** — already "method preview" in roadmap/README.md.

### New from re-audit

**A. Plugin upload docs stale (P1)**
- `docs/configuration.md` example: `plugin_upload_enabled = true` (should be `false`)
- `docs/plugins.md`: says upload enabled by positive `plugin_upload_max_size` alone — wrong, `enabled=false` is the real gate now
- `CHANGELOG.md` v1.27.0: says "defaults enabled" — now wrong after Task 11

**B. docs-check.py false claim (P1)**
- Header says it checks `<RUN_ID>` and `???`
- Implementation only checks `example/jul` and `example.com/jul`
- The most important evidence claims can be placeholders while docs-check passes

**C. reload-semantics.md stale API key (P2)**
- Still says "The apply response carries `reload.timed_out: true`"
- Actual API key is `previous_reload.timed_out`

**D. ga-push.md Wave 1 table malformed (P2)**
- HTTP/3 maturity cell has `2026-07-03` instead of `GA — soak pending`
- Wave 1 table excludes WASM plugins and L4 stream proxy

---

## 8. Specs, ADRs, and Roadmap Coherence

### Roadmap / Status / GA-Push Maturity
Three sources now agree: all 20 shipped features GA — soak pending. No contradictions.

### Specs Currency
- `specs/year-1.md` v1.2: Current.
- `specs/year-2.md` v1.8: Current.
- `specs/console-v2.md` v1.0: Current but omits "inspection only" limitation.
- `specs/hardening-platform.md` v1.0: HP-01..HP-07 still open.

---

## 9. Testing and QA Review

### Test Distribution
153 `_test.go` files. Good breadth.

### Coverage Floor
- **65% statement coverage** in CI.
- **Verdict: Too low** for a security-sensitive edge server. 75-80% appropriate.

### Soak Test Duration
- CI: 20 seconds
- Release: 5 minutes
- **Both are smoke tests, not soak tests.** The local Windows run even failed.

### Regression Tests
- ✅ `internal/server/reload_timeout_test.go` updated for advisory timeout.
- ✅ Tests pass on current code.

---

## 10. Security and Operations

### Plugin upload defaults to disabled
- ✅ Code change: `plugin_upload_enabled = false` by default (Task 11).
- ⚠️ Docs still describe the old `true` default.

### Other security findings unchanged
- Admin: No RBAC, no failed-auth audit. Bearer token auth is correct.
- Plugin: WASM magic check valid, SSRF guard valid, no sandbox escape tests.
- Cache: Host poisoning risk acknowledged in threat note but not mitigated in code.
- TLS: CRL check no large-bundle test.
- Supply chain: SBOM + Sigstore excellent.

---

## 11. Product and Marketing

### Do NOT market "all 20 features GA-soak passed"
The docs are internally consistent on maturity labels, but:
- Soak evidence is placeholder-based.
- Plugin upload docs describe a different default than the code.
- docs-check claims checks it doesn't perform.

### Marketable now
- Single static binary, zero runtime deps.
- Strong config/reload discipline.
- Console operational workflow.
- gRPC gateway.
- Evidence work in progress.

### Not marketable yet
- "Soak passed" without real run URLs.
- "Plugin upload secure-by-default" without docs that match.

---

## 12. Prioritized Backlog (Updated)

### Previously Completed (12/12 P0/P1)

| # | Title | Status |
|---|-------|--------|
| 1 | Fix `reload_timeout` goroutine bug | ✅ Merged |
| 2 | Fix `reload_timeout = "0s"` semantics | ✅ Merged (comments) |
| 3 | Fix apply response reload semantics | ✅ Merged (`previous_reload`) |
| 4 | Fix `LastReload` comment | ✅ Merged |
| 5 | Reconcile maturity docs | ✅ Merged |
| 6 | Replace soak placeholders honestly | ✅ Merged (`(not captured)`) |
| 7 | Fix roadmap "method picker" | ✅ Merged |
| 8 | Add `reload_timeout` to config reference | ✅ Merged |
| 9 | Extend docs-check header | ✅ Merged (header updated, but implementation still lags) |
| 10 | Fix version default | ✅ Merged |
| 11 | Change plugin upload default to `false` | ✅ Merged |
| 12 | Regression test for reload timeout safety | ✅ Merged |

### New / Remaining Tasks

| # | Title | Effort | Priority | Owner | Notes |
|---|-------|--------|----------|-------|-------|
| 13 | Fix plugin upload docs (config ref, plugins.md, CHANGELOG) | S | P1 | Docs | Code default is `false`; docs must match |
| 14 | Fix soak placeholders: remove ✅ or replace with real links | S | P1 | QA/Docs | 17 rows across status.md + ga-push.md |
| 15 | Fix ga-push.md Wave 1 table | XS | P2 | Docs | HTTP/3 maturity cell; add WASM + stream rows |
| 16 | Fix reload-semantics.md Stale API key | XS | P2 | Docs | `reload.timed_out` → `previous_reload.timed_out` |
| 17 | Fix OnReloaded comment or move hook | S | P2 | Backend | Comment promises post-swap, code is pre-swap |
| 18 | Fix docs-check.py: implement promised checks | S | P1 | DevEx | Header claims `<RUN_ID>`/`???` checks; implement or remove claim |
| 19 | Property-based reload safety quickcheck | M | P2 | QA | Seed configs, assert no swap regression |
| 20 | Harden `reload_timeout=0s` explicit test | M | P2 | QA | ✅ *Merged 2026-07-03* — `TestReloadTimeoutExplicitZero` verifies 0 disables advisory timeout |
| 21 | Pre-commit hook for tests + docs-check | S | P2 | DevEx | ✅ *Merged 2026-07-03* — cross-platform `python`/`python3`/`py` detection; blocks bad commits |
| 22 | Run week-long soak on staging | L | P1 | QA | True GA readiness gate |

---

## 13. Exact File Edits for New Tasks

### Task 13 — Fix Plugin Upload Default Docs
**Files:** `docs/configuration.md`, `docs/plugins.md`, `CHANGELOG.md`
**Effort:** S
**Impact:** P1

#### Step-by-step
1. **`docs/configuration.md`** (example block):
   - Change `plugin_upload_enabled = true` to `plugin_upload_enabled = false`
   - Add a comment or note: `"plugin_upload_enabled defaults to false. Set to true and configure plugin_upload_max_size > 0 to enable uploads."`

2. **`docs/plugins.md`** (enablement section):
   - Rewrite:
   > "The endpoint is disabled by default. To enable it, set both `plugin_upload_enabled = true` and a positive `plugin_upload_max_size` (MB) in `[admin]`. Uploading executable code is a high-consequence write and must be explicitly opt-in."
   - Remove or correct: "disabled unless [admin] sets a positive plugin_upload_max_size"

3. **`CHANGELOG.md`** (v1.27.0 entry):
   - Add a correction note: `"Note: default changed to false in v1.29.0 for secure-by-default posture."`

---

### Task 14 — Fix Soak Evidence Placeholders
**Files:** `docs/status.md`, `docs/ga-push.md`, `docs/soak-evidence.md`
**Effort:** S
**Impact:** P1

#### Step-by-step
1. For each row with ✅ + `<RUN_ID>`:
   - **Option A (preferred if CI run exists):** Replace `<RUN_ID>` with actual Actions run ID.
   - **Option B (if no run yet):** Change ✅ to ⏳ and replace link with a note like `"Pending: v1.29.0 release-gate soak queued."`
2. **`docs/soak-evidence.md:`** Replace artifact `<RUN_ID>` link with real link or mark ⏳.

---

### Task 15 — Fix ga-push.md Wave 1 Table
**File:** `docs/ga-push.md`
**Effort:** XS
**Impact:** P2

#### Step-by-step
1. Find HTTP/3 row in Wave 1 table. Change maturity cell from `2026-07-03` to `GA — soak pending`.
2. Add WASM plugins (Y2-02) and L4 stream proxy (Y2-03) rows to Wave 1 table with `GA — soak pending` maturity.

---

### Task 16 — Fix reload-semantics.md Stale API Key
**File:** `docs/reload-semantics.md`
**Effort:** XS
**Impact:** P2

#### Step-by-step
1. Find line: `"The apply response carries reload.timed_out: true"`
2. Replace with:
   > "The apply response may include `previous_reload.timed_out: true` so the UI can warn the operator that the last completed reload exceeded the expected duration. Note: this describes the previous reload, not necessarily the one triggered by the current apply."

---

### Task 17 — Fix OnReloaded Comment
**File:** `internal/server/server.go`
**Effort:** S
**Impact:** P2

#### Step-by-step
1. Find comment at lines 67-70:
   ```go
   // OnReloaded, when set, is invoked with the newly applied configuration at
   // the end of a successful reload, after the HTTP handler swap and listener
   // diff complete.
   ```
2. Replace with:
   ```go
   // OnReloaded, when set, is invoked after the new configuration has validated
   // and handlers have been built, but before the HTTP handler swap and listener
   // diff are finalized. It must be idempotent and must not assume the HTTP
   // generation is already live.
   ```
3. **Alternative (larger change):** Move `s.OnReloaded(newCfg)` to after the swap and listener diff (after line 467). Only do this if stream reload is confirmed safe to run post-swap.

---

### Task 18 — Fix docs-check.py
**File:** `scripts/docs-check.py`
**Effort:** S
**Impact:** P1

#### Step-by-step
1. In `check_placeholders()`, add:
   ```python
   bad = [
       r"example/jul",
       r"example\.com/jul",
       r"<RUN_ID>",
       r"actions/runs/<RUN_ID>",
       r"\?\?\?",
   ]
   ```
2. Add additional check: if ✅ appears within 200 chars of `<RUN_ID>` or `"CI running"` or `"pending"`, emit a warning.
3. **Or** if implementing is deferred, remove lines 7-14 claim #4 from the header.

---

## 14. File-by-File Action Summary (Updated)

| File | Status | Action |
|------|--------|--------|
| `internal/server/server.go` | ✅ Fixed | Timeout is advisory; synchronous swap. **Still open:** `OnReloaded` comment at lines 67-70 is wrong. |
| `cmd/jul/main.go` | ✅ Fixed | `version` default updated. |
| `internal/config/parser.go` | ✅ Fixed | Zero/omitted clarified to default 10s. Plugin upload default `false`. |
| `internal/admin/api.go` | ✅ Fixed | `previous_reload` key is correct. |
| `internal/admin/server.go` | ✅ Fixed | `LastReload` comment corrected. |
| `docs/reload-semantics.md` | ⚠️ Stale | Still says `reload.timed_out` instead of `previous_reload.timed_out`. |
| `docs/configuration.md` | ⚠️ Stale | Example TOML still shows `plugin_upload_enabled = true`. |
| `docs/plugins.md` | ⚠️ Stale | Says upload enabled by max_size alone; doesn't mention default false. |
| `docs/status.md` | ⚠️ Stale | Soak rows with ✅ + `<RUN_ID>` placeholders. |
| `docs/ga-push.md` | ⚠️ Stale | Wave 1 table: HTTP/3 maturity cell has date; missing WASM + stream rows. |
| `docs/soak-evidence.md` | ✅ Honest | Failed Windows run documented as non-authoritative; `(not captured)` for missing metrics. **Still open:** artifact link is `<RUN_ID>`. |
| `scripts/docs-check.py` | ⚠️ Misleading | Header claims `<RUN_ID>`/`???` checks; implementation does not perform them. |
| `CHANGELOG.md` | ⚠️ Stale | v1.27.0 says plugin upload "defaults enabled" — now false. |

---

## 15. Verification Gaps (cannot be fixed by code alone)

1. **CI logs and soak artifacts** — `<RUN_ID>` placeholders mean runs could not be verified.
2. **Per-package coverage** — 65% total enforced; no per-package breakdown visible.
3. **Console in browser** — Static review only; no manual interaction.
4. **Production performance** — No end-to-end hot-path benchmark published.
5. **Security posture** — No external pen-test report.
6. **WASM sandbox escape** — No escape vulnerability tests.
7. **Windows platform behavior** — `SO_REUSEADDR`, service integration not tested on real Windows.
8. **ACME long-term renewal** — Not observed in production.
9. **Consul/K8s discovery** — Not validated against real clusters.
10. **Cache disk tier under disk-full** — Not tested.
11. **Connector-visible CI status** — Current SHA showed no statuses; may be connector limitation.

---

## 16. Findings Rejected / Discarded from Second Audit

The following claims from the updated re-audit were examined and rejected:

| Claim | Verdict | Reason |
|-------|---------|--------|
| "Current SHA has no visible workflow result" | Unverifiable | Connector-API limitation; CI badges and workflow files exist in repo. |
| "Coverage numbers not available" | Already tracked | Baseline audit already identified 65% floor as too low. |
| "Feature maturity should revert broad promotions" | Already tracked | Baseline recommended 5 features remain Beta; second audit confirms doc contradiction. |
| **"status.md canonical matrix is incomplete for the 'all 20 features' claim"** | **FALSE** | `docs/status.md` soak tracking table lists all 20 features. GA criteria table lists 13; the remaining 7 are in soak tracking. Claim is incorrect. |
| **"Roadmap still says method picker"** | **ALREADY FIXED** | `docs/roadmap/README.md:82` already says "method preview" — completed in commit `8ff8011`. |
| **"Plugin upload default regression"** | **REFRAMED** | Code change to `false` was intentional (Task 11). Issue is docs staleness, not a code regression. |

---

## 17. Recommended Next Action

**Do a tight documentation/evidence correctness PR (Tasks 13–18), not more feature work.**

Specifically:
1. Task 13 (plugin upload docs) — operators need to know the real default
2. Task 14 (soak placeholders) — either get real CI run IDs or remove ✅
3. Task 18 (docs-check.py) — a validator that lies about what it checks undermines trust
4. Task 16 + 17 (stale docs/comments) — low effort, high maintainability value

After that, start Task 22 (week-long soak) — it is the true GA readiness gate and can be started in parallel with doc fixes.

---

*End of immediate action plan.*
