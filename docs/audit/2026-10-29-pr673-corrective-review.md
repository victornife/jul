# PR #673 — corrective review and adversarial verification

> **Follow-up:** The predicate cold-on-every-reload/restart design reviewed in
> this historical commit is superseded by the [warm-reuse correction](2026-10-29-pr673-warm-reuse-review.md).
> Its measurements and verdict remain evidence for the original reviewed source.


Date: 2026-10-29. Author/reviewer: OpenAI engineering agent acting on the user's requested review. This is author-side verification, not an independent human or second-reviewer approval.

Original reviewed head: `6c10002aa2cc7e61b315dbb29c1cd01b5e859b0f`.
Comparison baseline: `cbd647a9b144c4458c43d064f9e71f141a7e338c` (`main`, PR base).
Corrected runtime source and evidence are in the commit containing this report; the PR description records its resulting full SHA. No merge or issue closure is performed by this review.

## Verdict

The supplied audit is substantively correct. Both runtime defects were independently reproduced before correction. The identity/privacy objection and Console wording defect were also confirmed. Keep the feature and the additive policy/schema design; adopt revised ADR 0029 §3 with the explicit predicate-route compatibility cost below.

The changes have no remaining reproduced correctness/security defect in the exercised matrix, but **this report does not certify merge readiness**: two independent final-head security/correctness reviews, required hosted/native/browser checks, final Codecov PATCH >90%, and performance trade-off acceptance remain gates. Local author-side passes cannot substitute for independent reviews. The original head had no submitted GitHub reviews when queried.

## Confirmed findings and corrections

1. **P1 startup deadlock:** populated disk + malformed route epochs timed out at 3s. The stack shows `onDiskRehydrated → loadRouteEpochs → cleanup → setKeyDiskState → genMu`. Recovery now runs once before attaching callbacks and before capturing the disk catalog. Retained tests cover malformed, null, oversized and overflowing metadata with a populated disk; the catalog must also be empty after cleanup.
2. **P2 retired-route resurrection:** three runs each returned `HIT version-1` in memory and after disk restart following >1,024 retired scopes. Epoch compaction now advances a persisted global floor above every old epoch before forgetting individual scopes. All affected scoped entries become cold, including disk-only entries. A deterministic blocked-fill test verifies active-route publication fencing during compaction.
3. **Privacy/compatibility:** predicate values were hashed into persistent ownership even with `route_id`. Stable scopes now use non-predicate coordinates; predicate routes use a random process namespace and configured slot, with an epoch advance on every committed reload. No predicate value/digest is persisted as a partition. `route_id` stays optional. Predicate routes deliberately become cold on reload/restart; path-only routes keep warm restart hits. A real child-process regression proves both behaviors. This does not promise URLs, arbitrary Vary fields or operator IDs never contain secrets.
4. **Persistence/recovery:** durable epoch state is written before exposing the new ownership snapshot/floors; disk I/O never holds `genMu`. Failure attempts to corrupt the restart marker, retires disk entries through existing durable cleanup, and disables disk reuse/publication until successful persistence. Ordinary tombstone reconciliation cannot clear this state. Inspection reports `partial`/`disk_unavailable`. Absolute durability is impossible when the filesystem refuses all metadata writes and cleanup; ADR/cache/limitations explain the recovery action and preserve #666 ownership.
5. **Additional P2 resource defect:** 192 route scopes left 192 `RouteVary` records behind the 64-variant cap. Reproduced before correction. Route records and merged Vary fields are now pruned to actual membership; retained churn regression enforces the same cap.
6. **Additional performance regression:** per-hit `fmt.Sprintf("%#v", policy)` formatted the expanded cookie-name array. An initial comparison measured roughly 6.4 µs / 3,690 B / 19 allocs for a plain hit. Versioned binary encoding removes that formatting, with a completeness test for every reuse-policy field. Final measured costs and remaining security/policy overhead are below.
7. **Console:** `max_ttl = "0s"` now says it disables fresh storage; it does not promise conditional revalidation. Updated test, rebuilt assets, and reconciled current asset paths without rewriting historical certification verdicts.

## Five adversarial passes after corrections

| Pass | Threat/edge cases exercised | Evidence/result |
| --- | --- | --- |
| 1. Persistence and lifecycle | Populated corrupt startup; null/overflow/oversize metadata; retire/recreate; >1,024-scope churn; restart; persistence fault and recovery; blocked fill during compaction | New `route_epoch_regression_test.go` tests pass; original reproductions failed before correction |
| 2. Privacy and route isolation | Predicate value changes with/without IDs; anonymous route slots; reload and actual process restart; ordinary-route warm control; overlapping route isolation | New scope tests and existing app/cache route-isolation tests pass; no persistent predicate verifier remains in RouteScope |
| 3. Security and concurrency | HTTP/FastCGI/uWSGI transformed identities, spoofed input/CGI aliases; Cookie/Authorization Vary; 304/5xx/range/followers; epoch/fill races | Full-tag focused cache/app race matrix passes. Real-origin HTTP/FastCGI/uWSGI fixtures were copied unchanged to main and independently failed with B receiving A's cached body; they pass in the corrected tree |
| 4. Operator surfaces and compatibility | API/patch/permissions/effective policy; Console presets and zero semantics; generation/config examples/docs; historical evidence paths; resource accounting | Affected suites, Console checks/build, schema generation, config examples, docs and D11 checks pass. Browser/native execution is not claimed |
| 5. Performance and resource bounds | Common hit/miss/Vary versus main; public traffic with/without non-bypassed cookie; cookie-parser allocation; 10/1,000 predicate-route construction and reload; route metadata churn | Ten samples per benchmark; raw logs linked below. RouteVary/epoch growth bounded. CPU overhead remains measurable and requires explicit acceptance |

## Executed verification

Toolchain: Go 1.27.3 linux/amd64; Node 24.22.1; pnpm 11.25.0; golangci-lint 2.14.0; full tags `brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf`.

- `go test ./internal/cache ./internal/app -count=1 -coverprofile=...`: pass; package coverage cache 93.0%, app 81.8%.
- Full-tag affected cache/app/admin/config/lifecycle/handler matrix: pass.
- Full-tag `go test -race ./internal/cache ./internal/app -run '^(TestRoute|TestCache|TestProxyVary|TestCGIVary|TestMultiRoute|TestSensitive|TestVary)' -count=1`: pass.
- Local move-aware entire-PR changed-Go coverage: **1,440/1,527 = 94.30%**; corrective diff **139/140 = 99.29%**. Derived from full-suite and affected profiles, including failed environment lanes; these are not hosted Codecov PATCH results.
- Full-tag `go vet`, golangci-lint, license check, generated-check, config-check, security-gates: pass separately. Security floors unchanged (plugins 90.9%, RBAC 89.2%, WAF 84.4%). govulncheck reports zero reachable vulnerabilities (three non-reachable dependency findings remain reported by the scanner).
- `make console-check`: pass; global statement coverage 81.88%, all 10 exact-file targets retained; focused revised test also passes. Build passes; initial-route gzip **254,697 / 256,000 bytes**, within the existing budget.
- docs-check: 2,546 checks pass; docs-check/semantic-drift unit suites and D11 inventory check pass. No generated contract was edited manually.
- `go test ./...`, full-tag all-suite/coverage run, and `make ci-pr` were attempted. This environment denies Unix sockets and process resource probes, so those gates cannot be called green. The same representative Unix-socket and metrics failures were rerun on main. Latest full-suite failure inventory is confined to those surfaces. Broad race reruns also hit an existing 30ms app timeout-phase assertion; the focused assertion passed three repetitions on both main and head. Focused changed-path race tests pass; no broad-race green claim is made.
- Playwright execution was attempted; Chromium is absent and browser download failed. No browser test execution or accessibility certification is claimed. Native Windows/macOS and hosted final-head checks remain required.

## Baseline comparison

Same host (AMD EPYC 9V74), lean profile, `GOMAXPROCS=2`, ten samples, `-benchtime=200ms -benchmem`, baseline first then corrected tree. These shared-host medians are advisory, not production p95/p99 or a dedicated-runner regression budget.

| Benchmark | Main ns/op | Fixed ns/op | Change | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: | ---: |
| `BenchmarkCacheHit` | 1,372.0 | 2,282.5 | +66.4% | 1256 → 1256 | 16 → 16 |
| `BenchmarkCacheMiss` | 16,808.0 | 17,103.5 | +1.8% | 12700 → 13192 | 82 → 79 |
| `BenchmarkCacheVaryHit` | 2,158.5 | 3,520.0 | +63.1% | 1504 → 1664 | 25 → 35 |
| `BenchmarkRoutePolicyHTTPHit/cookies=false` | 1,487.5 | 2,258.0 | +51.8% | 1256 → 1256 | 16 → 16 |
| `BenchmarkRoutePolicyHTTPHit/cookies=true` | 1,601.0 | 2,508.5 | +56.7% | 1256 → 1288 | 16 → 17 |

The binary digest eliminates the earlier allocation/formatting regression, but the additional eligibility/policy checks still cost approximately 0.8–1.4 µs per common hit in this sample; Vary has ten additional small allocations. This is a quantified performance trade-off, not a zero-regression claim. No repository threshold was weakened. Performance approval remains open rather than silently treating this as a pass.

No-cookie bypass parsing: 25.1 ns/op, zero allocations; selected-cookie parsing: 145.3 ns/op, zero allocations. Predicate-route construction plus reload: 25.0 µs for 10 routes; 3.03 ms for 1,000 routes. Compilation supplies slot indexes directly to avoid scanning all configured routes per location. Results exclude disk fsync latency; disk epoch persistence durability is exercised by functional/fault tests, not a disk-throughput benchmark.

Raw data: [main](2026-10-29-pr673-bench-main.txt), [fixed](2026-10-29-pr673-bench-fixed.txt).
Common benchmark command:

```sh
GOMAXPROCS=2 go test ./internal/cache -run '^$' \
  -bench '^(BenchmarkCacheHit|BenchmarkCacheVaryHit|BenchmarkCacheMiss|BenchmarkRoutePolicyHTTPHit)$' \
  -benchmem -count=10 -benchtime=200ms
```

For the route-public-traffic baseline only, copy `BenchmarkRoutePolicyHTTPHit` from the retained test into main, replacing route compilation/commit/`HandlerForRoute` with `h := c.Handler(benchNext)`; all request, Cookie, origin, prime, recorder and HIT assertions remain identical. Main has no route-policy API. The baseline tests for #672 are the unchanged two app fixture files from this PR copied into main; they are deliberately failing evidence, not changes to main.

## Remaining gates and scope boundaries

- Two genuinely independent security/correctness reviews of the final runtime source and revised ADR §3; record exact reviewed SHAs and re-review any subsequent production edits.
- Required final-head CI, Windows/macOS, browser/accessibility execution and Codecov PATCH strictly >90%. Local statement scoring and old-head green checks do not replace them.
- Accept the documented cold predicate-route reload/restart behavior, bounded-compaction cold misses, and measured CPU/alloc trade-off (or optimize further under a separate measured review).
- #624 retains atomic route/backend cutover; #666 retains unfinished purge recovery and #667/#668 their existing purge limits. No distributed cache, secure-erasure guarantee, release/GA/soak promotion, mandatory route IDs or new persistent identity registry is introduced.
- #623/#672 remain open until their reviewed fix actually merges. This report does not close related work or alter its completion evidence.
