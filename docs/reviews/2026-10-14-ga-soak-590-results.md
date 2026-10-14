# #590: eight-hour v2.2.0 source-bound stability decision

## Decision

Complete criterion 5 for **exactly CGC-ROUTE, CGC-RES and SEC-EGRESS** on
unchanged full v2.2.0 source `b12e051139f652b400cff37d9f063f5637a7d550`.
Their manifest classifications become **GA / soaked**. No other feature,
optional extension, tag or package changes.

This is **not** a claim that the original collector passed. Its immutable
result remains failed solely for sample count: 2,851 rather than 2,881.
The maintainer authorised a separately identified, read-only collection
supplement **before the window ended**. All original gates pass against the
2,888 genuine same-process samples after that supplement. The original failure,
collection identities and independent primary/augmented resource results
remain visible.

## Frozen runtime and evidence identities

| Item | Actual execution |
| --- | --- |
| Product | Downloaded full native ARM64 v2.2.0; source `b12e051139f652b400cff37d9f063f5637a7d550`, clean build |
| Archive SHA-256 | `07d468c3e92232b0174cbdc66cd2abbb507719e332b3f60a5dfc60b22f172998` |
| Package verification | Individual/aggregate checksums, embedded native source/profile identity, SPDX bundle and both binary SLSA/SPDX attestations passed the existing verifier |
| Measured start/end | 2026-10-13 **22:37:42 UTC** to 2026-10-14 **06:37:42 UTC**; **28,800.008 seconds** |
| Final result/quieting | 2026-10-14 **06:39:45 UTC**; all workers joined, long TCP client closed, 120-second quiet window and final profiles completed |
| Original Jul PID | `106073` throughout; no restart or stitched attempts |
| Executed harness SHA-256 | `2d251f12e67065a4b74d72f293f361001456af81f73aeb7cab82bbcc5f4cdd40` |
| Original supplemental collector SHA-256 | `a5049af875c3335db03f5433ecb39bcf2e27d4773cf70b51966ba04c803e1e9e` |
| Supplemental window | 2026-10-14 **06:31:36–06:37:37 UTC**, 37 additional scrapes, stopped before traffic quiescence |
| Host | Authorised shared ARM64 WSL2 Linux, loopback-only fixtures; host boot identity retained privately and hashed in the public record |
| Workload budget | Enforced 200% CPU quota, 2 GiB memory, no swap, 256 tasks, 2 GiB evidence cap; no automatic restart |
| Supplemental instrumentation | Separate authorised read-only observer, capped at 5% CPU and 128 MiB; never changed Jul, configuration or offered load |

The [pre-run declaration](2026-10-14-ga-soak-590.md) remains the fixed contract.
The tested harness and observer are archived by digest; their later corrected
source is not substituted for what actually ran.

## Measurements against unchanged thresholds

The [machine review](../audit/evidence/2026-10-14-ga590/review.json)
contains both independently calculated verdicts and all phase/resource details.

| Check | Observed | Fixed requirement |
| --- | --- | --- |
| Continuous measured workload | 28,800.008 s, one Jul process | At least 28,800 s |
| Original / supplemented samples | 2,851 / **2,888** | At least 2,881 |
| Maximum sample gap | **10.307 s** in both sets | At most 25 s |
| Required phase coverage | All **25** phases; at least **480** repetitions each | Every required phase; no absence over 90 s |
| Maximum phase recurrence gap | **60.420 s** | At most 90 s |
| Unexpected workload events | **0** | 0 |
| RSS warmup maximum / active maximum | 45.91 / **52.03 MiB** | At most 1 GiB; growth at most 128 MiB |
| Final-four-hour RSS slope | Primary **0.228 MiB/h**; augmented **0.225 MiB/h** | At most 16 MiB/h |
| Heap-in-use warmup / active maximum | 12.27 / **15.60 MiB** | Growth at most 128 MiB |
| Goroutines warmup / active maximum | 67 / **71** | Growth at most 96 |
| Non-bucket metric series | **248** warmup and overall | Warmup +128; at most 3,000 |
| Final three quiet samples | **16 FDs, 29 goroutines, zero active/pending admission** in every sample | Warmup maxima +8/+96; no leaked admission |
| Retained access window | **311,167 valid JSON records** | No malformed retained record; not a complete request archive |
| Required profiles | All four nonempty; digested before/after heap and goroutine captures | All four required |
| Artifact integrity | All **47 primary** and **6 supplemental** listed files verified | No digest mismatch |

Pressure, denial, fault and cancellation are not an arbitrary allowed error
percentage. The retained phase assertions distinguish their exact expected
outcomes from normal-path failure:

- Four HTTP slow requests produce one success and three overload/pending
  failures while the actual active/pending bounds are observed.
- Reset attempts consume a bounded retry budget; the separate 150 ms Jul-owned
  deadline produces 504 within its fixed bound. Independent unbudgeted replay
  probes reach exactly one backend for unsafe POST and committed truncation.
- Circuit and active-health exclusion both recover. HTTP/native-gRPC
  cancellation returns admission to zero; gRPC overload preserves the
  documented HTTP 503-to-UNAVAILABLE mapping.
- TCP overload closes without an echo; injected backend close recovers; the
  original long-lived connection survives every policy publish/rollback.
- Auth allow/deny, system-DNS allow/deny, redirect non-following, discovery
  last-good fencing, policy tightening, malformed candidate rejection and
  rollback each repeat 480 times. Forbidden auxiliary fixtures receive no bytes.
- Independent routing/auth traffic continues during the fault and reload
  phases. Ordered response headers, routing precedence and CORS grants are
  checked on actual responses, not inferred from configuration.

## Collector correction and impact review

The original scheduler waited ten seconds **after** each scrape. Scrape work
therefore accumulated about 0.106 seconds per interval. It never lost relevant
traffic or a required phase, and no sample gap approached 25 seconds, but it
necessarily missed the declared count. The service correctly exited 1 and its
`result.json`/`failure.json` remain unchanged.

The [authorised deviation record](https://github.com/victornife/jul/issues/590#issuecomment-6162394070)
predates the final workload deadline. The additional collector:

1. Verified the exact running binary digest, Jul PID/start identity, host boot
   identity, original manifest and frozen parser digest.
2. Issued authenticated **GET /metrics** only, retaining genuine time/clock
   values in a separate directory. It did not invent, rescale, overwrite,
   relabel quiet samples or restart a product attempt.
3. Stopped before the original traffic deadline; the original quieting and
   final profile captures stayed separate.
4. Preserved the primary resource result independently. Both primary and
   augmented resource slopes, peaks, continuity, phase coverage and leak gates
   pass. The supplementation changes the count, not a failing product metric.

The review accepts that bounded, maintainer-authorised collection correction:
all **unchanged numeric criteria** are met by measurements of the same original
continuous process. It is not a general waiver for missing phases, interrupted
workloads, source changes or product regressions.

The maintained harness now schedules against **absolute ten-second deadlines**.
A deterministic test adds 250 ms scrape overhead and requires exact
0/10/20/30/40/50/60 sample times. Twenty rejection/fixture/scheduling tests pass.
A corrected 60-second actual-package rerun also passed all phases and quieting,
with seven expected samples and no errors. That rerun verifies the harness
correction; it is not renamed as the eight-hour product run.

PR quality feedback subsequently separated the maintained acceptance contract
and protocol fixtures into small helpers and split the phase/lifecycle
coordinator into bounded methods. Every acceptance test now compares the
maintained verdict with the actual frozen executed evaluator; all twenty tests
and a fresh real-package 60-second workload/quieting rerun passed. The original
archives, executed source bundle and published measurements are unchanged.
Subprocesses use explicit argument vectors with `shell=False`, exclusively for
operator-owned harness executables; no HTTP request supplies a command. Those
three necessary subprocess statements and the negative wildcard-bind assertion
have narrowly scoped, rule-specific false-positive annotations with adjacent
trust-boundary explanations, not a global scanner/category exclusion. Additional
argument-vector tests pass; the source-fingerprint setup check now raises rather
than using an optimisable `assert`. All 22 tests pass normally and under `-O`;
the targeted Bandit scan has no reported findings.

The documentation-only main advance to `7ff7e25e` during the campaign changed
neither the runtime package nor the retained executed harness. No unknown-source
product code inherits these measurements.

## Evidence, limits and publication

- [Sanitised timed metric records](../audit/evidence/2026-10-14-ga590/metrics.jsonl.gz)
  retain the public resource/resilience/egress counters and their original or
  supplemental identity, including the final quiet samples.
- [Review and phase counts](../audit/evidence/2026-10-14-ga590/review.json),
  [raw archive file digests](../audit/evidence/2026-10-14-ga590/raw-artifact-digests.json)
  and [public export digests](../audit/evidence/2026-10-14-ga590/digests.json)
  provide a bounded durable ledger. Full raw access/configuration/history and
  profiles stay in owner-only local archives, not GitHub attachments.
- The [executed source bundle](../audit/evidence/2026-10-14-ga590/executed-harness.tar.gz)
  preserves the original harness, tests, dependency pins, verifier, gRPC fixture
  and actual supplemental collector. Both executed Python source hashes were
  rechecked after packing. The entire public export is under 570 KiB.
- Shared-host loopback/native h2c/TCP and mock Consul-protocol scope remain
  explicit. No production independent-host, UDP/transcoding, JWKS redirect,
  mixed authoritative-DNS/rebinding, backend-egress, global-firewall or full
  NGINX equivalence certification is implied.
- #406 outcome retry and #522 ejection remain deferred. Standard gRPC active
  health, affinity, optional hot reload and all other Beta rows remain unchanged.
  **NGINX migration disposition: not applicable.**
- Failed preparation attempts, original sample-count failure and unsuccessful
  dependency-environment review invocations remain recorded. The successful
  review used the explicitly activated pinned Python environment.

This decision is delivered through the #590 evidence/documentation PR. Its
checks and reviewed/merged identities must be recorded in #590/#62 before final
issue closure; #579 keeps its independent sharing-design entry gate.

## Validation commands and results

The Python harness/review commands use the pinned, activated environment from
`scripts/ga-soak-requirements.txt`. Private artifact locations remain local;
the durable public record above contains their identities and digests.

| Command | Result |
| --- | --- |
| `python -m unittest discover -s scripts -p test_ga_soak.py -v` | Passed: 22 deterministic tests, including scheduling, invalid supplemental identities and argv-only execution |
| `python -O -m unittest discover -s scripts -p test_ga_soak.py` | Passed: all 22 tests; fingerprint guards remain active with optimised bytecode |
| `GOMAXPROCS=2 go test -p 2 -tags 'grpc stream consul' ./internal/configcontract ./internal/config ./internal/egress ./internal/auth ./internal/handler ./internal/upstream ./internal/stream -run 'Test(GA220ExistingConfigurationContract\|Scenario\|TCPAdmission\|Egress\|ClientRedirect\|Registry.*Egress\|ContextCancellation\|Resilience\|CrossProtocol\|GRPC.*Accounting\|Proxy.*Retry)' -count=1` | Passed: all eight scoped packages, including the exact GA configuration contract |
| `python3 -m pytest -q scripts/test_docs_check.py scripts/test_semantic_drift.py` | Passed: 61 tests and two subtests |
| `GOMAXPROCS=2 SOAK_DURATION=1s SOAK_WORKERS=2 make soak` | Passed: cache, HTTP and UDP-churn smoke; not long-run evidence |
| `GOMAXPROCS=2 make soak-repro-smoke` | Passed: HTTP/TCP workload, six retained metrics samples and all four profiler captures |
| `make config-check` with `JUL_BIN` set to the verified v2.2.0 full binary | Passed: every shipped root/example configuration loaded |
| `python3 scripts/docs-check.py` | Passed after synchronising current status/roadmap versions |
| `GOMAXPROCS=2 make generated-check` | Passed: lifecycle, configuration and external API projections unchanged |
| `git diff --check` | Passed |

The corrected real-package smoke passed for 60.002 measured seconds, seven
samples, all phases and complete quieting; its service exited 0. The original
eight-hour service exited 1 **only** for its preserved count failure. The
supplemental collector exited 0. No new Go production statements, Console
behavior or coverage floor changed. Required PR CI still gates integration.
