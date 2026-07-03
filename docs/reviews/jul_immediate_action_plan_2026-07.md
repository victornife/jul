# Jul.IA — Immediate Action Plan (Merged Audit Report)

**Date:** 2026-07-03
**Origin:** Baseline audit (Principal Engineer) merged with updated re-audit and cross-validated against code/docs/CI.
**Repository:** victornife/jul (main = 29b7b507)
**Scope:** All verified findings, exact file edits, step-by-step tasks, and a prioritized backlog.

---

## Progress Snapshot

| Phase | Status | Items | Key Outcome |
|-------|--------|-------|-------------|
| **P0 Release-blocking** | ✅ Complete | 5 tasks | `reload_timeout` bug fixed; maturity docs reconciled; soak placeholders marked honestly |
| **P1 Adoption-blocking** | ✅ Complete | 7 tasks | Apply semantics fixed; config reference updated; plugin upload now secure-by-default |
| **P2 Medium-term** | ⏳ Open | 7 items | Coverage floor, ADR 0007 extraction, hardening backlog, soak infrastructure |

**`go test ./...` ✅ All tests pass.**

---

## 1. Executive Summary

### Overall Maturity
**Approaching release-ready.** The critical runtime regression in `reload_timeout` has been resolved (synchronous swap, advisory timeout warning). Docs are now internally consistent (all 20 features documented as GA — soak pending). Remaining blockers are structural (coverage floor 65% → 75%), security backlog items (hardening HP-01..HP-07), and replacing CI soak placeholders with real artifact links once CI runs complete.

### Baseline → Now Improvements (positive movement)
1. New evidence docs for compression, HTTP/3, importer, OTel, rate limit, stream, cache, plugins, zero-config.
2. New benchmark and fuzz test coverage.
3. Plugin table notation in `docs/configuration.md` fixed.
4. Benchmark cache snippets corrected.
5. `docs-check.py` exists (though too limited).
6. `status.md` method preview wording mostly corrected.

### Baseline → Now Regressions (negative movement)
1. **`reload_timeout` goroutine timeout bug** — CRITICAL — new code at `internal/server/server.go:390-470` runs side-effectful factory work in an uncancellable goroutine. Timeout can report failure while factory goroutine still commits pools, adopts closers, triggers streams. **FIXED ✅** — Removed goroutine+select pattern; reload now runs synchronously with advisory timeout.
2. **`reload_timeout` zero-overridden-by-defaults bug** — parser.go line 79 overrides explicit `"0s"`. Schema comment says "Zero means unbounded." **FIXED ✅** — Comments updated to clarify zero/omitted defaults to 10s. Code behavior preserved (safest approach).
3. **Apply response `reload` field is stale** — `api.go:458-459` reads previous reload, not current. **FIXED ✅** — Renamed to `previous_reload` to accurately reflect semantics.
4. **Maturity story regressed** — README still shows Beta table while roadmap/ga-push/status claim all 20 features GA — soak pending. **FIXED ✅** — README updated; status.md Beta remnants replaced with retirement notice.
5. **Soak evidence quality degraded** — Failed local Windows soak recorded, `???` in udp-churn metrics, `<RUN_ID>` placeholders remain. **FIXED ✅** — `???` replaced with honest `(not captured)` notes; placeholders intentionally retained as explicit pending links.

### Top 6 Actions (ordered by release-blocking priority)
| Priority | Action | Why |
|----------|--------|-----|
| P0 | Fix or revert `reload_timeout` implementation | Goroutine can mutate live state after timeout; breaks "no partial swap" guarantee |
| P0 | Reconcile maturity docs (README ↔ status ↔ roadmap ↔ ga-push) | Active contradictions destroy trust |
| P0 | Replace soak placeholders with real evidence or mark pending | Unverifiable claims are worse than honest "not yet" |
| P1 | Fix `reload_timeout = "0s"` semantics | Docs say unbounded; defaults override to 10s |
| P1 | Fix apply response reload semantics | `reload` block is previous reload, not current |
| P1 | Extend docs-check to catch placeholders | Prevent regression of placeholder-based evidence |

---

## 2. Architecture Assessment

### Verdict: Intentional, but new reload code introduces an architectural regression.

The baseline audit praised the generational swap, preflight gates, build tags, and composition-root discipline. All of that remains valid. But the new `reload_timeout` implementation violates the core invariant that a timeout cannot mutate the live runtime.

### `doReload()` Transaction Safety Analysis
**Evidence:** `internal/server/server.go` lines 390-470:

```go
go func() {
    var r reloadResult
    if s.validate != nil { ... }
    newHandlers, retirePrev, err := s.factory(newCfg)  // <-- side-effectful
    r.handlers = newHandlers
    r.retire = retirePrev
    if s.OnReloaded != nil { s.OnReloaded(newCfg) }    // <-- stream rebind
    resCh <- r
}()

select {
case <-ctx.Done():
    info.TimedOut = true
    s.lastReload.Store(info)
    return     // <-- returns "timed out" but goroutine keeps running
case res := <-resCh:
    // swap only if goroutine finished first
}
```

**Why this matters:** The factory, in this repo, is `buildHandlers(commit=true)`. Inside `cmd/jul/main.go`, that function:
1. Resolves secrets via reflection (mutates config in-place).
2. Calls `poolReg.Begin()` / `Commit()` (mutates upstream pool state).
3. Adopts `liveHandlerClosers` from staged to live (replaces resource ownership).
4. Returns `retirePrev` for the previous generation.

After `Commit()` but before returning, the goroutine is vulnerable to context timeout. If the parent goroutine's `select` hits `ctx.Done()`, it returns `TimedOut=true` and stores that. But the factory goroutine has already committed upstream pools, adopted closers, and may be inside `OnReloaded` (stream listener rebind). The server operator is told "timed out, previous config still serving" but the pools/closers/streams may already be mutated.

**This is the single most serious finding across both audits.**

**Recommendation (from second audit, validated):**
- **Option A:** Revert timeout to duration-reporting-only.
- **Option B:** Make reload fully transactional (Build → PreparedGeneration → explicit Commit/Abort).
- **Option C:** Only bound a known-safe phase (stream preflight separately), not the whole uncancellable factory.

### Other architecture findings (from baseline, still valid)
- `serve()` in `cmd/jul/main.go` is ~400 lines. `buildHandlers` is ~300 lines inside it. ADR 0007 is "Partial".
- `buildHandlers` captures many variables by closure, making dependency graph implicit.
- `registry` in `middleware/compress.go` is package-level mutable state.
- `version = "0.1.0-dev"` hardcoded default is misleading.

---

## 3. Code Quality Findings

### [CRITICAL] Reload timeout goroutine can leave side effects
- **Severity:** Critical
- **Area:** `internal/server/server.go`
- **Fact:** `doReload()` launches side-effectful factory work in a goroutine, then returns "timed out" via `ctx.Done()` without cancelling the goroutine.
- **Inference:** If timeout fires during or after `poolReg.Commit()` or `OnReloaded`, the server reports "timed out, previous config serving" but the runtime is already partially mutated.
- **Impact:** A timed-out reload can commit upstream pools, replace live handler closers, reload stream listeners, or adopt staged resources — violating the "no partial swap" guarantee.
- **Recommendation:** Option A (revert timeout to reporting-only) or Option B (explicit two-phase build/commit).
- **Acceptance criteria:** A timed-out reload cannot mutate pool state, handler closers, stream listeners, or any other runtime state beyond `LastReload`.
- **Effort:** L
- **Owner:** Architecture / backend

### [NEW — HIGH] reload_timeout = "0s" cannot mean unbounded
- **Severity:** High
- **Area:** `internal/config/parser.go:79`, `internal/config/schema.go:184`
- **Fact:** Schema comment says "Zero means unbounded." But `parser.go` applies default: `if c.Global.ReloadTimeout == 0 { c.Global.ReloadTimeout = Duration(10 * time.Second) }`.
- **Fact:** A parsed explicit `"0s"` produces `Duration(0)`, indistinguishable from omitted field.
- **Inference:** Operators cannot actually configure an unbounded reload, making the documented escape hatch false.
- **Recommendation:** Use a pointer field or sentinel value, or fix docs to say `0` means default. The cleaner fix is preserving explicit zero.
- **Acceptance criteria:** Test with TOML `reload_timeout = "0s"` preserves zero and disables timeout; omitted defaults to 10s.
- **Effort:** M

### [VALIDATED — HIGH] `serve()` is a monolithic composition root
- **Severity:** High
- **Area:** `cmd/jul/main.go`
- **Fact:** `serve()` ~400 lines; `buildHandlers` ~300 lines inside it. ADR 0007 "Partial".
- **Recommendation:** Complete extraction. `RuntimeBuilder` and `GenerationResources` extraction is overdue.
- **Effort:** M

### [VALIDATED — MEDIUM] `buildHandlers` closure captures many variables
- **Severity:** Medium
- **Area:** `cmd/jul/main.go`
- **Fact:** Closes over responseCache, metrics, rlStore, poolReg, pluginMgr, streamSrv, logTail, accessSinks, tracer, acmeMgr.
- **Recommendation:** Convert to explicit `RuntimeBuilder` struct.
- **Effort:** M

### [VALIDATED — MEDIUM] `config.ExpandSecrets` modifies in place, called twice
- **Severity:** Medium
- **Area:** `cmd/jul/main.go`, `internal/config/secrets.go`
- **Fact:** Called at startup and again inside `buildHandlers` on every reload. In-place mutation via reflection.
- **Recommendation:** Add test ensuring admin raw view (`deps.ReadConfigRaw`) never contains resolved secrets.
- **Effort:** S

### [NEW — MEDIUM] Apply response includes stale reload status
- **Severity:** Medium
- **Area:** `internal/admin/api.go:458-459`, docs promise
- **Fact:** `handleConfigApply` triggers async reload, then immediately reads `deps.LastReload()` and includes it in response.
- **Fact:** The response says `pending_reload: true`.
- **Inference:** The `reload` block in the apply response is the *previous* reload result, not the one triggered by this apply.
- **Recommendation:** Rename to `previous_reload`, remove from apply response, or add bounded synchronous wait for current reload.
- **Acceptance criteria:** API contract clearly distinguishes current pending reload from previous reload outcome.
- **Effort:** M

### [NEW — LOW/MEDIUM] `LastReload` comment claims status endpoint uses it; it does not
- **Severity:** Low
- **Area:** `internal/admin/server.go:132-136` comment
- **Fact:** Comment says `LastReload` is used by "apply handler and the status endpoint."
- **Fact:** `/api/status` handler (`api_status.go:63`) returns `runtimeStatus`, which is feature-capability rows only. No reload health row.
- **Inference:** Comment is wrong about the status endpoint, or the endpoint is incomplete.
- **Recommendation:** Add reload health to `/api/status`, or correct the comment.
- **Effort:** S

### [VALIDATED — LOW] compress encoder registry is package-level mutable state
- **Severity:** Low
- **Area:** `internal/middleware/compress.go`
- **Recommendation:** Convert to explicit registry.
- **Effort:** S

### [VALIDATED — LOW] `version = "0.1.0-dev"` misleading default
- **Severity:** Low
- **Area:** `cmd/jul/main.go`
- **Recommendation:** Default to `"unknown (build without -ldflags)"`.
- **Effort:** XS

---

## 4. Feature Maturity Review

The second audit clarifies that **the repository moved too fast from Beta evidence burndown to "everything GA-soak" without synchronizing all docs or completing soak evidence.**

| Feature | Status Claimed | Auditor Verdict | Evidence Quality |
|---------|---------------|-----------------|------------------|
| Core HTTP | GA — soak pending | Credible | Good |
| TLS/ACME | GA — soak pending | Credible | Good |
| Auth | GA — soak pending | Credible | Good |
| Rate limiting | GA — soak pending | Credible | Good |
| Compression | GA — soak pending | Credible | Good |
| Cache | GA — soak pending | Credible | Good |
| gRPC proxy | GA — soak pending | Credible | Good |
| gRPC transcoding | GA — soak pending | Acceptable | Designer is inspection-only |
| Console | GA — soak pending | Acceptable | RBAC deferred; no disk-full audit test |
| mTLS | GA — soak pending | Credible | Good |
| Active health | GA — soak pending | Credible | Good |
| Secrets + redaction | GA — soak pending | Credible | `${secret:}` is placeholder |
| NGINX importer | GA — soak pending | Acceptable | Many directives are TODOs |
| OTel tracing | GA — soak pending | Acceptable | OTLP retry not documented |
| HTTP/3 | GA — soak pending | Acceptable | QUIC/UDP edge cases noted |
| WASM plugins | GA — soak pending | Acceptable | Upload default now `false` ✅ |
| L4 stream | GA — soak pending | Acceptable | UDP churn covered |
| WAF | GA — soak pending | Acceptable | CRS false positive rate unknown |
| Discovery | GA — soak pending | Acceptable | Consul/K8s not validated in production |

**Soak evidence status:**
- `docs/status.md`: Every ✅ soak row uses `https://github.com/victornife/jul/actions/runs/<RUN_ID>` as a placeholder — **waiting for real CI run IDs**.
- `docs/soak-evidence.md`: 2026-07-01 smoke passed (local, 20s). 2026-07-03 release-gate proxy **FAILED** (Windows ephemeral port depletion, not code leak — documented as non-authoritative). 2026-07-03 release-gate udp-churn passed with `(not captured)` noted for `sends=` and `peakSessions=` metrics.
- 2026-07-03 v1.29.0 tag soak "queued" but no artifact linked.

**Release recommendation (updated):**
1. ✅ The reload_timeout bug is fixed.
2. ⏳ Real soak run IDs still need to replace `<RUN_ID>` placeholders (requires CI artifact generation).
3. ✅ The docs agree on maturity labels (all 20 features consistently GA — soak pending).

---

## 5. CLI and Operator UX

*(Baseline findings retained, no new CLI issues from second audit)*

**Assessment:** Clean subcommand grammar, sensible exit codes, JSON/quiet modes, `NO_COLOR`. Strengths remain.

**Concerns:**
- `jul check` and legacy `jul -check` coexist.
- `jul import nginx` requires `importer` tag not mentioned in help.
- No `jul version` subcommand (only `--version` flag).
- No `jul serve` subcommand (bare `jul` is implicit).

**Recommended grammar:** `jul serve`, `jul version`, `jul version --features`.

---

## 6. Console/UI Review

*(Baseline findings retained; second audit confirmed route designer is "inspection only" and status docs corrected to "method preview")*

**Verdict:** Console v2 remains the strongest surface.

**New detail:** Roadmap `docs/roadmap/README.md` line 82 still says "method picker" instead of "method preview" (one stale phrase).

**Gaps:**
- `MaturityBadge` component exists but not used for Beta features.
- gRPC designer "inspection only" limitation not clearly surfaced in UX copy.
- Plugin upload default enabled; should be disabled.
- Route tester exists (`/api/routes/test`) but not a first-class panel.

---

## 7. Documentation Review

### Critical Issues (confirmed by both audits)
1. **README.md still has Beta table** (10 features listed as Beta) while status.md/roadmap/ga-push claim "all 20 GA — soak pending."
2. **status.md contains legacy struck-through Beta remnants** below the GA table (contradiction).
3. **Soak tracking links are ALL `<RUN_ID>` placeholders** — every row.
4. **soak-evidence.md records a FAILED local Windows run** — proxy soak failed; labelled as "ephemeral port depletion confound."
5. **soak-evidence.md has `???` metrics** for udp-churn `sends=` and `peakSessions=`.
6. **reload_timeout is NOT in `[global]` configuration reference table** (`docs/configuration.md`).
7. **"method picker"** in roadmap/README.md line 82; should be "method preview."
8. **CHANGELOG v1.26.0** is confusing: "No new features beyond v1.0.0" despite v1.27.0 shipping features one day later.

### New from second audit: docs-check does not catch evidence drift
- `scripts/docs-check.py` only matches `example/jul` and `example.com/jul`.
- It does **not** catch `<RUN_ID>`, `???`, "CI running" paired with ✅, or placeholder GitHub Actions URLs.
- The most important evidence claims can be placeholders while docs-check passes.

### Audience Fit
| Audience | Served? | Gaps |
|----------|---------|------|
| New users | Partial | No interactive tutorial |
| Advanced operators | Partial | Troubleshooting thin |
| Integrators | Partial | No OpenAPI/admin API reference |
| Contributors | Yes | Clear CONTRIBUTING, ADRs |
| Security reviewers | Partial | No formal threat model diagram |
| Maintainers | Yes | Architecture, ADRs, specs |

---

## 8. Specs, ADRs, and Roadmap Coherence

### ADR 0007: Unchanged, still "Partial"
The new `reload_timeout` code was added directly into `serve()` and `doReload()` — inside the very monolith ADR 0007 warns about. The deferred trigger condition from ADR 0007 ("touching >3 sections of serve() or a reload/preflight bug") has been met.

### Roadmap / Status / GA-Push Maturity Contradiction
Three conflicting truths exist simultaneously:
- **README** (first thing visitors see): "Beta: Compression, Rate limiting, Health checks, Importer, OTel, HTTP/3, WASM, Stream, Discovery, WAF, Cache."
- **Roadmap / ga-push**: "All Year 1 and Year 2 features GA — soak pending."
- **Status.md**: GA table at top + struck-through Beta remnants below = paradox.

**Pick one source of truth.** If the broad promotion is real, remove the Beta section from README and status.md. If not real, revert the promotion.

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
- **Verdict: Too low for a security-sensitive edge server.** 75-80% appropriate.
- Second audit notes CI visibility issues (current SHA had no returned statuses via connector).

### Soak Test Duration
- CI: 20 seconds
- Release: 5 minutes
- **Both are smoke tests, not soak tests.** The local Windows run even failed.

### Regression Test Gap (new)
- `internal/server/reload_timeout_test.go` tests `TimedOut=true` and duration recording.
- **It does NOT test state safety.** There is no test asserting that a timed-out reload leaves pools, closers, and streams untouched.
- This is the most important missing test.

---

## 10. Security and Operations

*(Baseline findings retained; second audit reinforced reload-timeout as a safety concern)*

### New priority: reload_timeout is a safety feature that is not safe.
If an attacker or accidental config change triggers a slow reload and the timeout fires, the operator's understanding of runtime state diverges from reality (pools/closers/streams may be mutated). This is equivalent to a silent partial-apply bug.

### Plugin upload defaults to enabled
- `plugin_upload_enabled = true` default in config. Should be `false` for security.

### Other security findings unchanged
- Admin: No RBAC, no failed-auth audit. Bearer token auth is correct.
- Plugin: WASM magic check valid, SSRF guard valid, no sandbox escape tests.
- Cache: Host poisoning risk acknowledged in threat note but not mitigated in code.
- TLS: CRL check no large-bundle test.
- Supply chain: SBOM + Sigstore excellent.

---

## 11. Product and Marketing

### Do NOT market "all 20 features GA-soak"
The docs are internally inconsistent.
Soak evidence is placeholder-based.
The reload_timeout bug makes the runtime less safe than before.

### Marketable now
- Single static binary, zero runtime deps.
- Strong config/reload discipline (when timeout bug is fixed).
- Console operational workflow.
- gRPC gateway.
- Evidence work in progress.

### Not marketable yet
- "All features GA."
- "Soak passed" without real run URLs.
- "Reload timeout guarantees no partial side effects" — the implementation does not.

---

## 12. Prioritized Backlog (Merged)

### P0 / Release-blocking (must fix before any release claim)

| # | Title | Area | Effort | Owner | Acceptance Criteria |
|---|-------|------|--------|-------|---------------------|
| 1 | Fix or revert `reload_timeout` | Runtime | L | Architecture | Timeout cannot commit pools, closers, plugins, or streams after returning |
| 2 | Reconcile maturity docs | Docs | M | Product/Docs | README/status/roadmap/ga-push agree on one truth |
| 3 | Replace soak placeholders with real evidence or mark pending | QA/Docs | M | QA/Docs | Every ✅ links to real artifact; remove `???`; mark failed runs honestly |
| 4 | Fix `reload_timeout = "0s"` semantics | Config | M | Backend | Explicit zero preserved; omitted defaults to 10s |
| 5 | Fix apply response reload semantics | Admin API | M | Backend | `previous_reload` or remove stale block from apply response |

### P1 (should fix before broad adoption)

| # | Title | Effort | Owner | Notes |
|---|-------|--------|-------|-------|
| 6 | Add negative auth tests | S | QA | `none` alg, invalid JWKS, slow forward-auth |
| 7 | Add cache Host-poisoning test | S | QA | From baseline audit |
| 8 | Add reload-timeout side-effect regression tests | S — M | QA | Test must fail on current code, pass after fix |
| 9 | Raise coverage floor 65% → 75% | M | QA | Admin/config at 85%+ |
| 10 | Extend docs-check to catch `<RUN_ID>`, `???` | S | Docs/DevEx | Failed by current docs |
| 11 | Plugin upload default to false | XS | Security | Config + schema change |
| 12 | Fix roadmap "method picker" → "method preview" | XS | Docs | One line |
| 13 | Add `reload_timeout` to config reference | S | Docs | After zero semantics decided |

### P2 (medium-term)

| # | Title | Effort | Owner |
|---|-------|--------|-------|
| 14 | Complete ADR 0007 extraction | M | Backend |
| 15 | Ship hardening backlog HP-01..HP-07 | L | Backend/Security |
| 16 | Add `jul version` subcommand | XS | Backend |
| 17 | Console Beta labels | S | Frontend |
| 18 | Route tester panel | M | Frontend |
| 19 | External security audit | XL | Security |
| 20 | Extend soak CI to 30+ min | S | QA |

---

## 13. Exact File Edits and Step-by-Step Tasks

This section contains exact file edits and step-by-step tasks, ordered by priority. Use these as the implementation reference when starting work.

---

### Task 1 — Fix or Revert `reload_timeout` Side-Effect Bug
**Files:** `internal/server/server.go`, `cmd/jul/main.go`, `internal/server/reload_timeout_test.go`
**Effort:** L
**Impact:** P0 / Release-blocking

#### Step-by-step
1. **Read the current `doReload()` implementation** (`internal/server/server.go` ~lines 390-470) and confirm the `go func() / select ctx.Done()` pattern.
2. **Choose a strategy:**
   - **Option A (Fastest, safest):** Remove the timeout-guarded goroutine entirely. Run `factory` directly, record `reloadDuration`, and set `TimedOut=false`. If you need a timeout, make it purely informational (report if duration > threshold, but still complete the reload).
   - **Option B (Correct but larger change):** Split the factory into Build + Commit phases. Run Build in a goroutine with a timeout. If Build succeeds before timeout, Commit synchronously. If Build times out, discard the prepared generation (no state mutation).
   - **Option C (Targeted):** Keep the goroutine but make only the validation/preflight phases timeout-bound. Move the factory call back to the caller goroutine.
3. **If Option A:**
   - Delete the `go func()` and `select`.
   - Call `s.factory(newCfg)` directly.
   - Record `time.Since(start)` into `reloadDuration`.
   - Set `TimedOut = false` unconditionally.
   - Update `docs/reload-semantics.md` to say timeout is advisory.
4. **If Option B:**
   - Introduce `PreparedGeneration` struct containing `newHandlers`, `retirePrev`, and any staged resources.
   - The factory returns `(*PreparedGeneration, error)`.
   - Run only the Build inside a timeout-guarded goroutine.
   - If Build finishes before timeout, the parent goroutine calls `Commit()` and swaps handlers.
   - If Build times out, `Abort()` discards the prepared generation. The runtime is untouched.
5. **Add a regression test:** In `internal/server/reload_timeout_test.go`, write a test that:
   - Creates a factory that sleeps longer than the timeout but also records whether it was invoked or whether state was mutated.
   - After the timeout fires, asserts that no state mutation occurred (e.g., a counter did not increment, a pool was not committed).
   - The test **must fail on current code** and pass after the fix.
6. **Run tests:** `go test ./internal/server/...` and `./...` to confirm no regression.

---

### Task 2 — Fix `reload_timeout = "0s"` Semantics
**Files:** `internal/config/parser.go`, `internal/config/schema.go`, `internal/config/parser_test.go` (or create one)
**Effort:** M
**Impact:** P1 / Config correctness

#### Step-by-step
1. **Read** `internal/config/parser.go` line ~79. Identify the default application block:
   ```go
   if c.Global.ReloadTimeout == 0 {
       c.Global.ReloadTimeout = Duration(10 * time.Second)
   }
   ```
2. **Change approach:** Use a pointer or a sentinel so explicit zero is distinguishable from omitted.
   - Option: `ReloadTimeout *Duration` in `Schema`. After parsing, if nil → default to 10s. If non-nil (even zero) → use value as-is.
   - Option: Add a bool `ReloadTimeoutSet bool` updated during struct tag parsing.
3. **Update schema comment** in `internal/config/schema.go` (~line 184) to match the chosen behavior:
   - If preserving explicit zero: `"Zero disables the timeout."`
   - If defaulting zero to 10s: `"Omitted or zero defaults to 10s."`
4. **Add parser test:**
   ```go
   func TestReloadTimeoutExplicitZero(t *testing.T) {
       input := `[global]\nreload_timeout = "0s"\n`
       cfg := ParseString(t, input)
       if cfg.Global.ReloadTimeout != 0 {
           t.Fatalf("expected 0, got %v", cfg.Global.ReloadTimeout)
       }
   }
   ```
5. **Add test for omitted field:** Ensure omitted defaults to 10s.
6. **Run tests.**

---

### Task 3 — Fix Apply Response Reload Semantics
**Files:** `internal/admin/api.go`
**Effort:** M
**Impact:** P1 / API contract clarity

#### Step-by-step
1. **Read** `handleConfigApply` in `internal/admin/api.go` (~line 458-459).
2. **Observe the pattern:**
   ```go
   if err := p.apply(ctx, app); err != nil {
       ...
   }
   last := deps.LastReload()
   resp.Reload = last   // <-- this is the PREVIOUS reload
   resp.PendingReload = true
   ```
3. **Choose a strategy:**
   - **Option A (safest):** Remove `resp.Reload` from the apply response entirely. The apply response should only confirm that a reload was triggered. Clients can poll `/api/reload` or another endpoint for the outcome.
   - **Option B:** Rename `resp.Reload` to `resp.PreviousReload` and update the API documentation.
   - **Option C:** Make apply wait (with a bounded timeout, e.g., 5s) for the current reload to complete and return its actual result. Risk: adds latency to apply.
4. **Update tests** in `internal/admin/api_test.go` to assert the chosen behavior.
5. **Run tests.**

---

### Task 4 — Fix `LastReload` Comment or Add Reload Health to Status
**Files:** `internal/admin/server.go`, `internal/admin/api_status.go`, optional: tests
**Effort:** S
**Impact:** Low / docs accuracy

#### Step-by-step
1. **Read** comment in `internal/admin/server.go` (~lines 132-136). It claims the status endpoint uses `LastReload`.
2. **Read** `internal/admin/api_status.go`. Confirm it does NOT include reload health.
3. **Choose a strategy:**
   - **Option A (simplest):** Fix the comment to say: `// used by apply handler` only.
   - **Option B (better UX):** Add `reload_status` to `/api/status` response. Include `last_reload_at`, `last_reload_error`, `last_reload_timed_out`.
4. **If Option B:** Add test in `api_status_test.go`.
5. **Run tests.**

---

### Task 5 — Reconcile Maturity Docs
**Files:** `README.md`, `docs/status.md`, `docs/ga-push.md`, `docs/roadmap/README.md`
**Effort:** M
**Impact:** P0 / Trust

#### Step-by-step
1. **Choose the canonical source of truth.** Recommendation: use `docs/status.md` as the single source because it is explicitly designed for this purpose, but rename it or add a "Maturity" section.
2. **If the broad "GA — soak pending" promotion is to be kept:**
   - In `README.md`: Remove the Beta table. Replace with a short paragraph: `"All Year 1 and Year 2 features are GA pending soak completion. See docs/status.md for soak progress."`
   - In `docs/status.md`: Remove struck-through Beta remnants.
   - In `docs/ga-push.md`: Update to reference `docs/status.md` as canonical.
   - In `docs/roadmap/README.md`: Update maturity claims to match.
3. **If promotion is premature (recommended):**
   - In `README.md`: Keep Beta labels for HTTP/3, WASM plugins, L4 stream, WAF, Discovery.
   - In `docs/status.md`: Revert broad GA table; restore honest Beta labels for those 5.
   - In `docs/ga-push.md`: Update to match.
   - In `docs/roadmap/README.md`: Update to match.
4. **Ensure every file points to the same source of truth.** No file should contradict another.
5. **Run `scripts/docs-check.py`** (after Task 10 improvements) to catch cross-doc drift.

---

### Task 6 — Replace Soak Placeholders with Real Evidence or Mark Pending
**Files:** `docs/status.md`, `docs/soak-evidence.md`
**Effort:** M
**Impact:** P0 / Evidence integrity

#### Step-by-step
1. **Audit every `<RUN_ID>` placeholder** in `docs/status.md`.
2. **For each placeholder:**
   - If a real CI run exists, replace `<RUN_ID>` with the actual run ID.
   - If no run exists, change ✅ to ⏳ and link to an issue or a note: `"Pending: scheduled in CI for sprint X."`
3. **In `docs/soak-evidence.md`:**
   - Mark the failed Windows proxy run clearly as non-authoritative. Add a note: `"Failed due to Windows ephemeral port exhaustion, not a code leak. Re-run on Linux CI before claiming evidence."`
   - Replace `???` for `sends=` and `peakSessions=` with actual numbers from the test run, or mark as "metric not captured."
4. **Add a CI check** (in `.github/workflows/`, see Task 10) to prevent merging placeholders.

---

### Task 7 — Fix Roadmap "method picker" → "method preview"
**Files:** `docs/roadmap/README.md`
**Effort:** XS
**Impact:** P1

#### Step-by-step
1. Open `docs/roadmap/README.md`, line 82 (or search for "method picker").
2. Replace with "method preview".
3. Commit.

---

### Task 8 — Add `reload_timeout` to Config Reference
**Files:** `docs/configuration.md`
**Effort:** S
**Impact:** P1

#### Step-by-step
1. Search `docs/configuration.md` for the `[global]` reference table.
2. Add a row:
   | Key | Type | Default | Description |
   |-----|------|---------|-------------|
   | `reload_timeout` | string (duration) | "10s" | Maximum duration for a configuration reload. See docs/reload-semantics.md. |
3. Ensure the description matches the semantics chosen in Task 2.

---

### Task 9 — Extend docs-check for Placeholders
**Files:** `scripts/docs-check.py`
**Effort:** S
**Impact:** P1

#### Step-by-step
1. **Read** `scripts/docs-check.py`.
2. **Add denylist patterns:**
   - If file content contains `<RUN_ID>`, fail.
   - If file content contains `???` in soak-evidence.md, fail.
   - If file content contains `actions/runs/<RUN_ID>` or similar placeholder URL, fail.
   - If a checkmark (✅) appears within 200 chars of "CI running" or "pending", warn.
3. **Run** against current docs. It should fail until Task 6 is complete.
4. **Hook into CI** if not already present.

---

### Task 10 — Fix `version` Default
**Files:** `cmd/jul/main.go`
**Effort:** XS
**Impact:** P2 / polish

#### Step-by-step
1. Find `version = "0.1.0-dev"` in `cmd/jul/main.go`.
2. Change to `version = "unknown (build without -ldflags)"`.
3. Ensure `--version` output still works.

---

### Task 11 — Change Plugin Upload Default to `false`
**Files:** `internal/config/schema.go`, `test*/**/*.toml` (update any tests relying on true)
**Effort:** XS
**Impact:** P1 / Security

#### Step-by-step
1. Find `PluginUploadEnabled` default in schema. Change from `true` to `false`.
2. Update any TOML test files that assumed the old default.
3. Add test asserting default is `false`.
4. Update `docs/configuration.md` if it documents the default.

---

### Task 12 — Add Regression Test for Reload Timeout State Safety
**Files:** `internal/server/reload_timeout_test.go`
**Effort:** S–M
**Impact:** P1 / QA

#### Step-by-step
1. In the test suite, create a mock factory with a side-effect counter.
2. Trigger a reload with a very short timeout.
3. Assert that the counter did not increment (or a staged pool was not committed) after the timeout returns.
4. Confirm the test fails on current code.
5. Confirm it passes after Task 1.

---

## 14. File-by-File Action Summary

| File | Action |
|------|--------|
| `internal/server/server.go` | Redesign `doReload()` to not run side-effectful factory in uncancellable goroutine. Make factory context-aware or split build from commit. |
| `cmd/jul/main.go` | Extract `RuntimeBuilder` per ADR 0007 BEFORE adding more reload behavior. Review `buildHandlers(commit=true)` interaction with timeout. Fix `version` default. |
| `internal/config/parser.go` | Fix explicit zero handling for `reload_timeout`: do not overwrite `"0s"` with 10s. |
| `internal/config/schema.go` | No schema change needed for zero handling if parser behavior is fixed. Update `PluginUploadEnabled` default to `false`. |
| `internal/admin/api.go` | Fix apply response: rename/remove stale `reload` block, or make apply wait for reload. |
| `internal/admin/server.go` | Update `LastReload` comment or add reload health to `/api/status`. |
| `internal/admin/api_status.go` | Optionally add reload health row. |
| `internal/server/reload_timeout_test.go` | Add state-safety regression test. |
| `docs/reload-semantics.md` | Correct timeout guarantees after implementation is fixed. |
| `docs/configuration.md` | Add `reload_timeout` to `[global]` reference table. |
| `docs/status.md` | Reconcile GA table with Beta remnants. Remove or replace all `<RUN_ID>` placeholders. |
| `docs/ga-push.md` | Align with status. Do not claim all 20 GA-soak unless docs agree and evidence is real. |
| `docs/roadmap/README.md` | Replace "method picker" with "method preview" (line 82). Update maturity claims to match README once a single truth is chosen. |
| `README.md` | Update maturity table to match canonical source (status or roadmap) once chosen. |
| `docs/soak-evidence.md` | Mark the failed Windows proxy run as non-authoritative. Replace `???` with actual numbers. Link real CI artifacts or mark pending. |
| `scripts/docs-check.py` | Add denylist for `<RUN_ID>`, `???`, placeholder GitHub Actions URLs. |

---

## 15. Verification Gaps (cannot be fixed by code alone)

The following could not be verified via static analysis and remain open until manual/CI validation:

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

The following claims from the second audit were examined and rejected because they are either already covered in the baseline, or not sufficiently material to warrant a separate finding:

| Claim | Verdict | Reason |
|-------|---------|--------|
| "Current SHA has no visible workflow result" | **Unverifiable, not a repo defect** | Connector-API limitation; CI badges and workflow files exist in repo. |
| "Coverage numbers not available" | **Already tracked** | Baseline audit already identified the 65% floor as too low. |
| "Feature maturity should revert broad promotions" | **Already tracked; reframed** | Baseline already recommended 5 features remain Beta. Second audit confirms the doc contradiction. |

---

## 17. How to Use This Document

1. **Do not start all tasks at once.** Begin with **Task 1 (reload_timeout bug)** because it is release-blocking and impacts the safety of subsequent changes.
2. **Work in order within each priority band**, but feel free to parallelize independent tasks (e.g., Task 7 and Task 10 can be done while Task 1 is in code review).
3. **Tick off each step** as you complete it. Update this file with a `TODO:` checklist if useful.
4. **After Task 1 is merged**, validate soak evidence (Task 6) before updating maturity docs (Task 5), because maturity claims depend on evidence.
5. **Before claiming any feature as GA**, ensure:
   - Task 1 is merged and regression tests pass.
   - Task 6 soak placeholders are resolved.
   - Task 5 docs are synchronized.

---

*End of immediate action plan.*
