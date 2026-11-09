# #695 aggregate WAF/WASM inspection resource assessment — results

- **Date:** 2026-11-09
- **Owner:** [#695](https://github.com/victornife/jul/issues/695), Step 2 of the [#62](https://github.com/victornife/jul/issues/62) pre-release programme.
- **Assessed runtime:** `main` at `453e62375bdce0302756ff386637832d5fa35bad` (the Step 1 / #694 merge). This was still `origin/main` when the assessment ran. It was built with `make build-full`; `jul` sha256 is `9d39b371…2130`.
- **Plan:** [pre-run plan](2026-11-09-695-waf-wasm-assessment-plan.md), frozen in commit `70c7b4cea67eb8ee0cffcd2623d49b074f66f190` before the final runs.
- **Evidence:** [`evidence/2026-11-09-695/`](evidence/2026-11-09-695/). The `exploratory/` directory holds pre-plan runs and probes. The `final/` directory holds the matrix: raw `results.jsonl`, 500 ms samples, rendered configurations, threshold evaluations, the kernel OOM journal and a supplemental reproducer. Each directory has a `SHA256SUMS` file.
- **Disposition:** **B — material resource-control gaps require follow-up**: [#700](https://github.com/victornife/jul/issues/700), [#697](https://github.com/victornife/jul/issues/697), [#698](https://github.com/victornife/jul/issues/698) and [#699](https://github.com/victornife/jul/issues/699).

This is a bounded characterisation on a single host. It is not a capacity claim, SLA, soak, maturity decision or release qualification. Throughput figures describe one 2 vCPU / 768 MiB envelope with these fixtures. They are not portable sizing constants.

## 1. Baseline and identity

| Item | Value |
| --- | --- |
| Source | `453e6237`. The binary embeds `vcs.revision=453e6237…` and `vcs.modified=true`. The only differences were untracked assessment files that are not in Jul's build graph: `scripts/` is `//go:build ignore`, `examples/plugins` is a separate module, and `_test.go` files are not linked. The plan's "clean tree" statement is corrected by this note. |
| Toolchain | `go1.27.3 linux/arm64`. Full tags: `brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf` |
| Engines | Coraza `v3.8.1`, coraza-coreruleset `v4.25.0`, wazero `v1.12.0`, quic-go `v0.65.0`, `x/net v0.62.0` |
| Host | WSL2 `6.18.54.1-microsoft-standard-WSL2`, 12-core Qualcomm Oryon, 15.7 GiB RAM, `/tmp` tmpfs |
| Envelope | Jul runs in its own systemd user scope: `CPUQuota=200%`, `MemoryMax=768M`, `MemorySwapMax=0`, CPUs 0–1, `GOMAXPROCS=2`, private tmpfs `TMPDIR` (spill counts as cgroup shmem). The load generator (CPUs 4–9) and the origin (CPUs 10–11) run outside the scope. |
| Artefacts | Plugin `inspect-load.wasm` sha256 `2526ed82…8127`, built from `examples/plugins/inspect-load` (`GOOS=wasip1 -buildmode=c-shared`, 4.4 MB Go guest). Harness binary sha256 `a8505b2b…84eb`, built from `scripts/waf-wasm-assessment.go` sha256 `ca3f0c46…2f60`. The `results.jsonl` sha256 is `5af32afa…f173`, which matches `manifest.json`. |
| Run | 2026-11-09 14:55–15:32 UTC, exit 0, 53 result rows. The whole matrix ran once and nothing was re-run selectively. |

## 2. Request and response ordering (traced)

The order is set by `internal/app/factory.go` (chain assembly around lines 872–960).

**Global layers, outermost first:**

1. request ID
2. client address
3. HTTP framing guard
4. tracing
5. metrics and in-flight count
6. access log
7. panic recovery
8. response compression

**Location layers, in order:**

1. Server and location **request plugins**.
2. Client certificate check.
3. **CORS preflight**. This calls `Preflight(cors, rl, wf)` and reuses the same rate limiter and WAF. A preflight runs after the request plugins and is answered before auth, the response-plugin hook and the action.
4. **Auth**.
5. **Rate limit**.
6. **WAF**: request phases 1–2, and response phases 3–4 when they apply.
7. The v2 **response-plugin** hook.
8. **Cache** and the body limit.
9. **Action**. For `proxy_pass`, upstream admission happens here (`internal/handler/proxy.go:174-217`, `internal/upstream/admission.go`).

Consequences:

- Request plugins run before auth and rate limiting.
- The WAF and plugins run before upstream admission and on routes that have no upstream.
- A warm cache hit still runs request plugins, the WAF (including response inspection), response plugins and compression. Measured in s7 `both`: 939 requests produced 939 WAF transactions and 939 invocations of each plugin, but only 469 origin calls.
- The comment in `factory.go` that says the WAF is "outside rate limiting" contradicts the code. This is a minor comment error with no runtime effect.

## 3. Control matrix

| Resource or work | Owner and acquire/release | Default and bound | Scope | At saturation | Waiters and what they retain | Timeout, cancel, retire | Observability and blind spots |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Accepted connections | `internal/server/dynamic_conn_limit.go`. Acquired on accept, released on close. Also gates QUIC (`http3.go:33`). | `[rate_limit] max_conns`, off by default | Per listener | Accept is held or refused | Kernel backlog | Released on close | Connection metrics. **Does not cap H2/H3 streams.** |
| H2/H3 streams | `x/net/http2` and quic-go defaults. Jul sets neither limit. | 250 streams per H2 connection, 100 per QUIC connection | Per connection | Stream refused by the protocol | None | Stream reset | No stream metric |
| Request read time | `server.go:927` | `read_timeout` 0 (unbounded); `read_header_timeout` 10 s | Per connection | — | — | The slow-body read is unbounded by default | — |
| Request rate | Rate-limit layer (after plugins and auth) | Off by default | Location or key | 429 | None | — | Limits rate, not concurrency |
| WAF transaction and body | `internal/waf/outcomes.go:360-415` (`ReadRequestBodyFrom`, `ProcessRequestBody`), released at the end of the request | `request_body_limit` 128 KiB, rejected with 413 at or above the limit in block mode. Bodies above 128 KiB **spill to `$TMPDIR`** (F1). | Per request | Nothing. **There is no concurrency bound.** | Every request runs at once | **Coraza does not observe cancellation.** Abandoned work ran for 8.8–18.2 s after clients disconnected (§5, T3). | `jul_waf_transactions_total{mode,outcome}`. No in-flight, memory or spill metric. |
| WAF argument parse | Coraza `SecArgumentsLimit 1000` | Arguments past 1000 are **dropped silently** (F3) | Per request | Partial inspection (bypass) | — | — | `ARGUMENTS_LIMIT_REACHED` is not alerted by default |
| WAF response body | `outcomes.go:702`. With CRS, Coraza buffers `text/*` responses **even when `response_body_check=false`** (F2). | — | Per response | — | — | — | Posture reports `false` |
| Plugin instances | `internal/plugins/runtime.go` `acquire` (~529), released after each guest call | `max_instances` 64, `memory_limit` 16 MiB | Per plugin name **per generation** | Waits up to `timeout` (100 ms), then `503 plugin busy` (`set.go:127`) | **The number of waiters is unbounded.** Each waiter holds its request, including any buffered body. | The guest call is bounded by `timeout` (~641). Errors give `500` and discard the instance (`set.go:130`). | `jul_plugin_instances`, `jul_plugin_instance_waits_total{result}`, `jul_plugin_invocations_total{result}`. No wait-duration metric. |
| Plugin request body | `host.go:103` `io.ReadAll` **inside the guest call** | `max_request_body` 1 MiB | Per request and layer | A slow upload exceeds the call timeout and gets `500` (P1, #699) | The instance is held while the body arrives | — | Shows only as `result="error"` |
| Plugin response subscription | `internal/plugins/phase.go:171-191` | `max_response_body` 8 MiB per subscribing layer; replacement bodies are extra | Per response and layer | Ineligible or oversize responses pass through | — | — | `jul_plugin_response_*` |
| Upstream admission | `upstream/admission.go` `Acquire` (147), park (~170–245), release (262) | `max_active_requests` / `max_pending_requests` / `pending_timeout` | Per upstream pool | `503` with `Retry-After: 1` | FIFO, **bounded by `max_pending`**. Parked requests keep their already-buffered inspection bodies. | Retirement wakes waiters with `503`. A deadline ends the wait. | `jul_upstream_active_requests`, `_pending_requests`, `_admission_rejected_total` |
| Generations | Each generation owns its WAF engines and plugin runtimes, retired after it drains | One active generation; older ones stay alive while requests drain | Process | Overlapping generations add their instances and engines together | — | Retirement is checked by existing churn tests | **No live-generation count metric** |
| Process memory | Go heap, wazero linear memory and compiled code, host buffers, tmpfs spill | — | Process or cgroup | Kernel OOM kill | — | — | `go_memstats_*` only. No inspection-attributed memory metric. |

## 4. Thresholds and results

The thresholds were predeclared in the plan (§4) and were not changed. Machine evaluations are in [`final/t1-t2-evaluation.txt`](evidence/2026-11-09-695/final/t1-t2-evaluation.txt) and [`final/t3-recovery-evaluation.txt`](evidence/2026-11-09-695/final/t3-recovery-evaluation.txt). Kernel OOM kills come from the systemd journal ([`final/journal-oom.txt`](evidence/2026-11-09-695/final/journal-oom.txt)), because the cgroup's `memory.events` disappears with the scope.

### Throughput and memory (verified responses only)

Rejections, 500s and unverified 2xx responses are excluded. Values are the median across 3 runs, with the range in brackets. "Peak" is the run-wide cgroup `memory.peak`.

| Scenario | none | waf | wasm | both |
| --- | --- | --- | --- | --- |
| s1 H1 16 KiB body / 64 KiB response, C=16 | 5,211/s [4,416–5,642], 24 MiB | 17.4/s [14.6–18.2], 148 MiB | 965/s [554–1,010]¹, 301–362 MiB | 18.6/s, 315–346 MiB, **1× 500 per run** |
| s2 H2, same | 2,736/s, 25 MiB | 18.6/s, 140 MiB | 870/s, **516–608 MiB** | 18.1/s, 360–441 MiB, **500s: 1, 5, 0** |
| s3 H2 256 KiB body / 1 MiB chunked response, C=32 | 208/s, 42–46 MiB | **0/s**, 672–706 MiB | 54/s with **~1,690× 500**, 704–741 MiB | 0.6/s with ~450× 500, 671–680 MiB |
| s10 H3, as s1 (1 run) | 2,319/s, 26 MiB | — | — | 16.1/s, 334 MiB, no errors |

¹ s1 `wasm` r1 is more than 20% below the median, so it is flagged under the noise policy.

**CPU cost per verified request**, from Jul's cgroup CPU over the measured window:

| Variant | CPU per request |
| --- | --- |
| none | about 0.24 ms (H1) |
| wasm | about 2 ms |
| waf | about 106–114 ms on 16 KiB "wide" JSON |
| both | about 107 ms |

The WAF dominates. Combined cost is roughly additive in CPU and is bounded by WAF throughput. In s1–s3 the WAF variants use the whole 2-CPU quota (about 39.7 of 40 CPU-seconds).

### Threshold verdicts

| ID | Verdict | Detail |
| --- | --- | --- |
| T1 memory | **FAIL** | Fails in 3/3 s3 runs for every inspecting variant, in 1/3 s2 `wasm` runs (608 MiB), and by **kernel OOM kill** in s4 `both` (C=64), s5 `both`, s6 `both`, s8 `wasm` and `both`, s9 `both` and s11 `waf` (C=8). Every `none` run passed. |
| T2 correctness | **FAIL** | s1 and s2 `both` have sporadic `500`s from `inspect-a` `result="error"`, a plugin call error under CPU saturation. s3 `wasm` and `both` have hundreds of `500`s. s3 `waf` completed 0 requests. s6 `both` was killed. s7 `both` had 12× `500`. s10 and every `none`, `waf` and `wasm` run in s1/s2 passed. |
| T3 recovery | **FAIL** | As implemented, the harness's last sample overlaps the post-recovery GC and canaries (in-flight 1, heap drop). Both the literal and the corrected evaluations are kept. At the end of the declared recovery window, 11 surviving runs fail. s3 and s8 `waf` still had **17–92 requests in flight** that drained only after 8.8–18.2 s. Post-GC heap stayed high: +55–114 MiB in s2 `wasm`, with 7–12 idle instances retained, and +63–515 MiB in s3 `waf`, which is Coraza pool retention after one GC (exploratory P1 showed release after a second GC). s7 none (+70 MiB) is cache contents. Every post-load canary passed. |
| T4 admission | PASS for the bound (`none`); FAIL (`both`) | `none`: active ≤ 8 and pending ≤ 16 at every sample, and 176,475 excess requests got a fast `503`. However, **admitted goodput collapsed**: only 5 verified responses and 21 origin calls, against roughly 16/s of pool capacity, while all 8 active slots stayed occupied. Jul's in-flight count never exceeded 26, so about 100 of the 128 client streams were queued on the client or shared H2 connection. The likely cause is connection-level flow-control contention: rapidly retried 64 KiB uploads delay the admitted streams' bodies. This was **not isolated**, and the harness's single shared H2 client contributes. It is an open question for #700's H2 fan-out test. `both`: Jul was OOM-killed during warm-up, before admission was reached, because inspection ran ahead of admission. |
| T5 probe ≥ 99% | PASS (`none` 100%, `waf` 99.16%); FAIL (`wasm` 0.05%, `both` 0%) | `waf` probes passed the ratio but fell from 8,383/s to 23.6/s, with p99 3 s. |
| T6 slow uploads | PASS (`none`); FAIL (`waf`, `wasm`, `both`) | `none`: 384 verified with p50 3.9 s, and 96 cut off by the phase deadline (each lasts about 4 s). `waf`: 3 completions in 20 s for 96 uploaders (starved). `wasm`: 0 completions, connections closed and the process killed. Cancelling clients (32 per run) were counted correctly: 288 client cancellations, plus 32 cut off at the deadline, in `none` and `waf`. |
| T7 reload churn | **FAIL** | s9 `both` was OOM-killed between t+18 s and t+20 s, after 9 reload signals; reload 5 deliberately carried an invalid directive. Reload outcomes before the kill were not captured. Lifecycle evidence outside the envelope comes from existing race tests (§6). |

### Other observations

**s4 ramp, 256 KiB H2:**

- `none` held about 225/s up to C=64, then dropped to 160/s at C=128 and 128/s at C=192. Peak was 85 MiB. This is CPU saturation, not memory.
- `both` completed 0 at C=16 (484 MiB), returned 168× `500` at C=32 (659 MiB), and was OOM-killed at C=64.

**s6 static route without an upstream:**

- `none` served 633/s.
- `both` was OOM-killed. Upstream admission does not apply on this route.

**s7 cache:**

- `none`: cold 2,180/s, warm 10,507/s.
- `both`: cold 17.5/s, warm 17.6/s. Warm hits gave **no** throughput gain because inspection still runs on every hit (see §2).

**s11 argument amplification (742-argument JSON, WAF):**

| Concurrency | Throughput | Peak memory |
| --- | --- | --- |
| C=1 | 4.1/s | 256 MiB |
| C=2 | 8.4/s | 343 MiB |
| C=4 | 8.7/s | 524 MiB |
| C=8 | OOM-killed | 765 MiB |

That is about **90 MiB of cgroup memory per additional concurrent request**, consistent with exploratory P2 (about 58 MiB allocated per transaction plus GC headroom). `none` reached 9,683/s.

**Plugin memory:**

- Cgroup memory divided by peak live instances (summed over both plugins) gives 10–33 MiB per instance of this 4.4 MB Go guest. This is an upper-bound attribution, because it includes host buffers.
- In s8 `wasm`, 65 instances were live before the kill: `inspect-a` (which reads the body) was at its 64-instance cap, and `inspect-b` had 1. That cap produced the 108 probe `503 plugin busy` responses.

**WAF working set:** the WAF variants start at about 90 MiB of cgroup memory with CRS loaded and idle. Peak memory then grows by about 3–4 MiB per concurrent 16 KiB request (s1, s2) and about 18–19 MiB per concurrent 256 KiB request (s3).

**H3:** s10 executed real HTTP/3 traffic. It is reported separately and was not repeated.

## 5. Findings

| ID | Finding | Source and reproducer | Observed or inferred | Severity, confidence | Operator consequence and existing mitigation | Release impact and owner |
| --- | --- | --- | --- | --- | --- | --- |
| **G1** | No aggregate or pre-admission bound on inspection work. WAF and WASM concurrency is limited only by connections, streams and rate. | §3; s3, s4, s5, s6, s8, s9, s11 | Observed: OOM kills and starvation in the envelope. Inferred: the same holds in larger envelopes at proportionally higher load. | High, high | Unauthenticated traffic to a WAF or WASM location can exhaust process memory. `max_conns` and rate limits reduce but do not bound this (H2/H3 streams, slow bodies). Interim sizing is in §7. | **Step 7 needs a decision** (fix or recorded acceptance). [#700](https://github.com/victornife/jul/issues/700) |
| **G2** | WAF evaluation continues after client cancellation. | T3 drain (8.8–18.2 s); `outcomes.go` has no context check | Observed | Medium, high | Retries can pile up abandoned work. | Part of [#700](https://github.com/victornife/jul/issues/700) |
| **G3** | Plugin waiters are unbounded. Each holds its request. | `runtime.go` `acquire` | Source-traced. s8 showed 503s at the cap. The waiter count was not separately measured. | Medium, high | Under saturation, many requests each wait one `timeout`. | Part of [#700](https://github.com/victornife/jul/issues/700) |
| **F3** | Arguments past 1000 are not inspected, so block mode can be **bypassed**. | `TestAssessmentArgumentLimitPartialInspection` | Observed | **High (security)**, high | Add the `ARGUMENTS_LIMIT_REACHED` guard (§7). It was verified at 1,003 arguments → 413. | **Blocks Step 7.** [#697](https://github.com/victornife/jul/issues/697) |
| **F2** | With CRS, `response_body_check=false` still inspects `text/*` responses, and posture reports `false`. | `TestAssessmentCRSResponseBodyAccess`; `coraza.conf-recommended:145` | Observed | Medium (contract and posture), high | Response-buffering memory and latency are paid when the setting says they are not. `SecResponseBodyAccess Off` in `inline_rules` disables it. | **Blocks Step 7.** [#698](https://github.com/victornife/jul/issues/698) |
| **P1** | A v2 `read_body` plugin returns 500 for a slow but valid upload, because the body is read inside the 100 ms call timeout. | [`plugin-slow-upload-repro.txt`](evidence/2026-11-09-695/final/plugin-slow-upload-repro.txt): 16 KiB/s gives 500; full speed gives 200 | Observed (single request, no load) | High (correctness), high | Raising `timeout` above the slowest expected upload time works around it, but holds an instance for that long. | **Blocks Step 7 for `read_body` configurations** unless explicitly accepted. [#699](https://github.com/victornife/jul/issues/699) |
| **F1** | With CRS and `request_body_limit` > 128 KiB, request bodies over 128 KiB **spill to temporary files**. | `TestAssessmentRequestBodySpill`; s3 had 32 spill files / 8 MiB of shmem | Observed | Low, high | `TMPDIR` on tmpfs is charged as memory, and on disk it costs I/O. Files are removed on completion. | Guidance (§7). No follow-up. |
| **O1** | CRS cost is superlinear in argument count. | s11; exploratory P2 | Observed | Medium, high | Many-field JSON or forms cost about 90 MiB of cgroup memory per concurrent request. | Guidance. Covered by #697 and #700. |
| **O2** | Warm cache hits still pay for inspection. | s7; §2 | Observed | Low, high | The cache does not shed WAF or WASM cost. | Guidance (documented ordering). |
| **O3** | Observability gaps: no metrics for live generations, plugin wait duration, spill, inspection in-flight work or inspection memory. | §3 | Source-traced | Medium, high | Operators cannot see this pressure before an OOM kill. | Step 3 handoff and #700 metrics. |

The protocol scope is ordinary HTTP/1.1, H2 and H3 request/response bodies. **Not covered:**

- native gRPC (the WAF is not supported there; see `docs/waf.md`)
- SSE and WebSocket frames
- L4 `stream` traffic

These do not gain inspection guarantees or bounds from these results.

## 6. Reproduction and supplemental evidence

The full reproduction steps are in plan §7. In short:

```sh
make build-full
JUL_EFFECTIVE_VERSION_KEY_FILE=<tmp>/effective-version.key ./jul version-key init
(cd examples/plugins && GOOS=wasip1 GOARCH=wasm go build -trimpath -buildvcs=false -buildmode=c-shared -o <tmp>/inspect-load.wasm ./inspect-load)
go build -o <tmp>/harness scripts/waf-wasm-assessment.go
JUL_EFFECTIVE_VERSION_KEY_FILE=<tmp>/effective-version.key <tmp>/harness run -jul ./jul -plugin <tmp>/inspect-load.wasm -out <tmp>/final
python3 docs/reviews/evidence/2026-11-09-695/final/summarize.py <tmp>/final/results.jsonl <journal-extract> > tables.md
```

Supplemental evidence was collected after the plan and is labelled as such. It did not change any threshold:

- **Single slow-upload reproducer for P1** ([`plugin-slow-upload-repro.txt`](evidence/2026-11-09-695/final/plugin-slow-upload-repro.txt)). It runs without a cgroup and with one client.
- **Existing lifecycle race tests**, run without the envelope, all `ok` ([`lifecycle-race-tests.txt`](evidence/2026-11-09-695/final/lifecycle-race-tests.txt)):
  - `go test -race -tags waf -run 'TestWAFReloadChurnNoLeak|TestAssessment' ./internal/waf/`
  - `go test -race -tags wasmplugins -run TestV2ReloadUnderLoadAndChurnQuiesces ./internal/plugins/`
  - `go test -race -tags 'wasmplugins waf' -run TestReloadResultReportsPluginModuleChanges ./internal/server/`

  These tests cover real generation changes, retirement and quiescence under load. They do not cover memory inside the envelope.

**Deviations from the plan:**

1. The plan says "clean tree". The tree was clean for tracked files only (see §1).
2. The harness's final T3 sample overlaps the post-recovery canaries. Both the literal and the corrected evaluations are kept.
3. s9 reload outcomes before the OOM kill were not captured.
4. In T6, `unfinished-at-phase-end` uploads were judged against their expected duration of about 4 s. For `none`, this is a deadline cut-off, not a failure.

An early draft of the summary table misread the client-cancellation field. The raw data was unaffected.

## 7. Operator guidance (tested envelope only)

- **Add the argument-limit guard** to every CRS location until #697 lands:
  `SecRule ARGUMENTS_LIMIT_REACHED "@eq 1" "id:1695190,phase:2,deny,status:413,log"`.
- **Budget memory as concurrency × per-request working set, not per-request limits.** In this envelope the measured working set was:

  | Workload | Memory per concurrent request |
  | --- | --- |
  | 16 KiB string-field JSON | ~3–4 MiB |
  | 256 KiB bodies | ~18–19 MiB |
  | 742-argument JSON | ~90 MiB |

  These figures are on top of a ~90 MiB idle CRS baseline. Go-guest plugins add up to 10–33 MiB per live instance.
- **Cap concurrency where Jul can:**
  - `[rate_limit] max_conns` per listener. It counts H1 connections and QUIC connections, not H2/H3 streams.
  - A lower plugin `max_instances` with a `memory_limit` that fits.
  - Upstream admission. It protects origins but **not** inspection.
  - Rate limits.

  An external connection or stream limit in front of Jul is needed for H2/H3 fan-out until #700 lands.
- **Keep `request_body_limit` as small as the route allows.** Above 128 KiB, put `TMPDIR` on disk rather than tmpfs if memory is tight.
- **Response inspection with CRS:** `response_body_check=false` does not disable it (#698). Add `SecResponseBodyAccess Off` to `inline_rules` if it is not wanted.
- **`read_body` plugins:** set `timeout` above the slowest legitimate upload, and set a finite server `read_timeout` (#699).
- **Watch these metrics:** `jul_plugin_instance_waits_total{result="rejected"}`, `jul_plugin_invocations_total{result="error"}`, `jul_waf_transactions_total`, `go_memstats_heap_inuse_bytes` and the cgroup or container memory.

These conclusions **do not apply** to larger envelopes without re-measurement. They also do not apply to other rule sets, guest languages or body shapes, or to gRPC, SSE, WebSocket or L4 traffic.

## 8. NGINX migration impact

Not applicable. This assessment changes no mapped runtime behaviour. The only code added is a build-ignored harness, an example guest and characterization tests.

## 9. Adversarial review

This is a self-review, not independent. No independent review was performed.

| Challenge | Check |
| --- | --- |
| Unexercised inspection | Pre- and post-load canaries show each layer blocking its marker on proxy and static routes. Metric deltas show one WAF transaction and one invocation per plugin per request (s1, s7). Responses are verified against origin echoes and plugin evidence headers. |
| Cache bypass | s7 origin calls (469 for 939 requests) prove hits were served while inspection still ran. |
| Wrong scope | Memory comes from Jul's own cgroup. Load generator and origin are outside it on separate CPUs. Plugin per-instance figures are stated as upper bounds. |
| Fast rejection counted as throughput | Only verified 200s count. 500s, 503s, refusals and unfinished requests are reported separately. |
| Queued callers and overlapping generations | Included in the control matrix. s9 died before generation overlap could be measured inside the envelope. Existing churn tests cover retirement without the envelope. |
| Finite run treated as a bound | No unconditional bound is claimed. The passes for `none` are observations, not guarantees. |
| Unsupported claims | No maturity, protocol, capacity or release claim is made. H3 results are a single run. |

## 10. Handoff

- **Step 3 (#628 Milestone A).** Usable signals today:
  - process memory (cgroup or `go_memstats_*`)
  - `jul_plugin_instance_waits_total{result="rejected"}`
  - `jul_plugin_invocations_total{result="error"}`
  - `jul_upstream_admission_rejected_total` / `_pending_requests`
  - `jul_waf_transactions_total` rate

  There are demonstrated blind spots (O3): live generations, plugin wait duration, WAF and inspection in-flight work, spill bytes and inspection memory. Recipes must not invent these. If #628-A needs them, the narrow producer dependency is the metric set in #700.
- **#625 (effective configuration explanations).** Explanations must reflect F2: CRS response inspection is effective despite `response_body_check=false`. They must also reflect that the inspection order runs before admission and on cache hits.
- **#627.** This step adds no entry requirement. If the composition enables the WAF or WASM, apply the #697 guard and the §7 sizing, and do not present it as capacity-tested.
- **Step 7 (release qualification).** #697 and #698 block release. #699 blocks for `read_body` configurations. #700 needs a recorded decision. Re-run the affected scenarios with this harness after any fix.
