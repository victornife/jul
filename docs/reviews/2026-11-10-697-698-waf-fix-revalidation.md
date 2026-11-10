# #697 / #698 WAF fix — bounded revalidation

- **Date:** 2026-11-10
- **Owners:** [#697](https://github.com/victornife/jul/issues/697) (argument-limit
  fail-open, security) and [#698](https://github.com/victornife/jul/issues/698)
  (`response_body_check` ignored by CRS). Follow-up to the
  [#695 assessment](2026-11-09-695-waf-wasm-resource-assessment.md), whose
  measurements, thresholds and disposition are unchanged and remain the
  historical record.
- **Fixed source:** commit `47731c045f4cded6240a76ed051da3bc4b4be159` (first
  commit of the fix PR), `make build-full`, `jul` sha256 `08f6f2a4…66c9`, clean
  tree.
- **Comparison source:** `main` at `80a74070cc381651f943ae05c4e62f956fb50803`
  (the #701 merge; same WAF runtime as the assessed `453e6237`), `make build-full`,
  `jul` sha256 `97f08b2a…b11cf`. Its `vcs.modified` flag comes only from the
  restored, untracked harness files.
- **Evidence:** [`evidence/2026-11-10-697-698/`](evidence/2026-11-10-697-698/)
  with `SHA256SUMS`.

This is a bounded, source-bound check of two fixes. It is not an aggregate
recertification, capacity claim, soak or release qualification. #699 and #700
findings are untouched by this change.

## 1. Functional reproducers

`go test -count=1 -v -tags waf -run 'TestAssessmentArgumentLimitPartialInspection|TestAssessmentCRSResponseBodyAccess' ./internal/waf/`

| Case | Before (unfixed) | After (fixed) |
| --- | --- | --- |
| CRS, `response_body_check=false`, `text/plain` phase-4 marker | `403` (inspected) | `200` (not inspected) |
| No CRS, `response_body_check=true`, `text/plain` marker | `200` (no MIME selection) | `403` (inspected) |
| CRS, 1,201 urlencoded arguments, attack last | `200`, forwarded | `413`, 0 origin hits, rule `900001` |
| CRS, 1,003 clean arguments | `200`, forwarded | `413` |
| Interim guard `1695190` configured | `413` | `413` (`900001` fires first; both compile) |

Raw output: [`reproducers-before.txt`](evidence/2026-11-10-697-698/reproducers-before.txt)
(the characterisation tests on unfixed source) and
[`reproducers-after.txt`](evidence/2026-11-10-697-698/reproducers-after.txt).
The converted regressions fail on unfixed `main` at exactly the defect cases
([`regressions-on-unfixed-main.txt`](evidence/2026-11-10-697-698/regressions-on-unfixed-main.txt)).

## 2. Bounded load subset

**Method.** The archived #695 harness and guest were restored as described in
[`tooling/README.md`](evidence/2026-11-09-695/tooling/README.md) (SHA256 checks
passed). The same host, envelope (`CPUQuota=200%`, `MemoryMax=768M`, swap 0,
CPUs 0–1, `GOMAXPROCS=2`), CPU pinning, origin, plugin (`2526ed82…8127`) and
20 s measurement windows were used. Both binaries ran in the same session, one
after the other.

**Harness deviation.** A recorded additive patch
([`harness-revalidation.patch`](evidence/2026-11-10-697-698/harness-revalidation.patch))
adds:

- a `waf-respoff` configuration (`response_body_check = false`) whose response
  canary expects `200`;
- `r1` (the s1 normal H1 workload: 16 KiB request, 64 KiB `text/html` response,
  16 concurrent, 3 reps on `waf` and `waf-respoff`);
- `r2` (4 legitimate 742-argument JSON clients + 8 clients sending
  ~1,490-argument JSON);
- `r3` (the same mix at 1 + 2 clients).

Original scenarios and thresholds are unchanged. Harness binaries: r1/r2
`2494c8d3…1a3`, r3 `833821b7…e15f`.

### r1 — disabling response inspection (3 reps each)

| Build | Config | Verified rps | p99 | Origin calls | Response canary |
| --- | --- | --- | --- | --- | --- |
| unfixed | `waf` (check on) | 16.9 / 18.8 / 18.6 | 1134–1237 ms | 423–484 | `403` ✓ |
| unfixed | `waf-respoff` | 18.6 / 18.6 / 18.4 | 1116–1201 ms | 482–487 | `403` — still inspected (fail ×3) |
| fixed | `waf` (check on) | 18.6 / 18.6 / 18.4 | 1123–1205 ms | 473–484 | `403` ✓ |
| fixed | `waf-respoff` | **24.3 / 24.9 / 24.1** | **872–928 ms** | 621–636 | `200` ✓ |

With the fix, `response_body_check = false` actually stops CRS phase-4
inspection: about +32 % verified throughput and lower p99 for this CPU-bound
workload, all requests verified end-to-end through the origin. With the field
on, the fixed build matches the unfixed one. Peak cgroup memory stayed at
139–157 MiB in every run; this workload is CPU-bound, so the buffering saving is
not visible in peak memory here (the unit test
`TestResponseInspectionOffStreamsWithoutBuffering` covers buffering directly).

### r3 — argument-limit exhaustion with legitimate traffic (3 reps each)

| Build | Legitimate 742-arg (1 client) | Overflow ~1,490-arg (2 clients) | Origin calls | Peak cgroup |
| --- | --- | --- | --- | --- |
| unfixed | 60 / 54 / 42 `200` | 86 / 78 / 62 `200` — **forwarded uninspected past argument 1,000** | 162 / 148 / 114 | 454–461 MiB |
| fixed | 58 / 58 / 60 `200` | 90 / 90 / 88 `413` | 64 / 63 / 66 | 455–462 MiB |

The fixed build forwards no overflow request; origin calls equal the legitimate
requests plus canaries. Legitimate throughput is unchanged within run-to-run
variation. The guard does **not** reduce inspection cost: Coraza still parses
and CRS phase-2 rules still evaluate the first 1,000 arguments before `900001`
interrupts, so CPU (saturated at ~40 s per 20 s window in both) and peak memory
are the same. That cost is the #700 aggregate-bound finding.

### r2 — overflow at 12 concurrent requests

Both builds were OOM-killed in every repetition (`signal: killed`, no verified
requests), consistent with the #695 s11 result (WAF killed from c8) and the
~90 MiB per concurrent many-argument request working set. The guard does not
change this; it remains a #700 finding.

## 3. Remaining limitations

- **#700:** no process-wide inspection concurrency/memory bound; many-argument
  bodies still cost ~90+ MiB each and still OOM the 768 MiB envelope at about
  8+ concurrent requests, whether they are rejected or forwarded.
- **#699:** plugin `read_body` timeout behaviour is unchanged.
- Cancellation, H2/H3 fan-out and throughput findings from #695 are unchanged.
- A full aggregate recertification belongs after #699 and #700.
