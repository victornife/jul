# #695 aggregate WAF/WASM inspection resource assessment — pre-run plan

- **Date:** 2026-11-09
- **Owner:** [#695](https://github.com/victornife/jul/issues/695) (Step 2 of the [#62](https://github.com/victornife/jul/issues/62) pre-release programme)
- **Assessed runtime:** `main` at `453e62375bdce0302756ff386637832d5fa35bad` (Step 1 / #694 merge), clean tree, `make build-full`
- **Status:** frozen before final runs. Results are recorded separately in
  [the assessment report](2026-11-09-695-waf-wasm-resource-assessment.md). This plan is not edited after
  the final runs start; deviations are recorded in the report.

This is a bounded, single-host characterisation. It is not a capacity claim, SLA, soak, or maturity
decision.

## 1. Environment and resource envelope

| Property | Value |
| --- | --- |
| Host | WSL2 kernel `6.18.54.1-microsoft-standard-WSL2`, linux/arm64, 12-core Qualcomm Oryon, 15.7 GiB RAM, 4 GiB swap, `/tmp` tmpfs 7.7 GiB |
| Toolchain | `go1.27.3 linux/arm64`; systemd 259 user manager (cgroup v2) |
| Jul build | full profile: `brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf` |
| Pinned engines | Coraza `v3.8.1`, coraza-coreruleset `v4.25.0` (CRS 4.25.0), wazero `v1.12.0`, quic-go `v0.65.0`, `golang.org/x/net v0.62.0` |
| **Jul envelope** | `systemd-run --user --scope -p CPUQuota=200% -p MemoryMax=768M -p MemorySwapMax=0`, `taskset -c 0,1`, `GOMAXPROCS=2`, private `TMPDIR` |
| Load generator | harness process, `GOMAXPROCS=6`, outside the Jul cgroup |
| Origin | `harness origin`, `GOMAXPROCS=2`, `taskset -c 10,11`, outside the Jul cgroup |

**Rationale:** 2 vCPU / 768 MiB is a common small VM or container slice for an edge proxy and is
small enough that aggregate inspection pressure becomes visible without exhausting the host. Memory,
CPU and kill events are read from the Jul scope's own cgroup files, so client and origin consumption
are excluded. A `tmpfs` `TMPDIR` means WAF spill files are charged as shmem to the Jul cgroup; this is
reported separately as `cg_shmem` and spill bytes.

## 2. Topology and configuration

One Jul process with an H1 listener and a TLS listener (H2 + H3) on loopback, both carrying the same
locations. The admin listener (bearer token, `pprof = true`) serves `/metrics` and
`/debug/pprof/heap`.

| Location | Action | Purpose |
| --- | --- | --- |
| `/proxy/` | `proxy_pass` to pool `origin` (no admission limit) | normal, stress, slow-client, lifecycle and H3 workloads |
| `/admitted/` | `proxy_pass` to pool `limited` (`max_active_requests = 8`, `max_pending_requests = 16`, `pending_timeout = "2s"`) | upstream admission saturation |
| `/cached/` | `proxy_pass` to `origin`, `cache = true` when the scenario enables caching | cold versus warm |
| `/static/` | `root` file server (256 KiB HTML) | no-upstream route |
| `/healthz` | `return = 204` | readiness |

Variants (identical except for inspection):

| Variant | WAF | WASM |
| --- | --- | --- |
| `none` | off | off |
| `waf` | CRS, `mode = "block"`, `request_body_limit = "1mb"`, `response_body_check = true`, inline request (phase 2) and response (phase 4) canary rules | off |
| `wasm` | off | `inspect-a` and `inspect-b` (`examples/plugins/inspect-load`, `jul-abi/v2`, `read_body` + `subscribe`; `inspect-b` also replaces the response body); plugin defaults (`max_instances = 64`, `memory_limit = 16 MiB`, `timeout = 100ms`, `max_request_body = 1 MiB`, `max_response_body = 8 MiB`) |
| `both` | as `waf` | as `wasm` |

Every run starts and stops on its own fresh Jul process. Canaries run before and after load and must
show that each configured layer actually executes: WAF request-body and response-body markers are
blocked, the WASM request marker is blocked on both a proxied and a static route, and clean traffic is
allowed.

## 3. Workloads

Closed-loop clients; each response is **verified** (status 200, exact response length, origin
echo of the request length, and, in WASM variants, both plugins' request and response evidence
headers). Fast rejections and unverified 2xx responses are counted separately and never contribute
to throughput.

JSON body shapes:

- **wide:** 2,000-character string fields (about one argument per 2 KiB). This keeps CRS far below
  the 1,000-argument limit.
- **items:** about three arguments per 66 bytes.

All workloads use `Host: localhost`.

| ID | Protocol | Workload | Variants | Repetitions |
| --- | --- | --- | --- | --- |
| s1 | H1 | 16 KiB wide JSON POST, 64 KiB HTML response, C=16 | all 4 | 3 |
| s2 | H2 | as s1 | all 4 | 3 |
| s3 | H2 | 256 KiB wide JSON POST, 1 MiB chunked HTML response (unknown length), C=32 | all 4 | 3 |
| s4 | H2 | s3 at C = 16 / 32 / 64 / 128 / 192, 2 s warm-up, 10 s per step | none, both | 1 |
| s5 | H2 | `/admitted/`, 64 KiB wide JSON POST, 500 ms origin delay, 64 KiB response, C=128 | none, both | 1 |
| s6 | H2 | `/static/` 256 KiB GET, C=128 | none, both | 1 |
| s7 | H1 | `/cached/` 256 KiB GET: cold (unique keys), then warm (one key) | none, both (with cache) | 1 |
| s8 | H1 | 96 slow uploaders (64 KiB at 16 KiB/s), 32 slow uploaders that cancel at 2 s, 4 fast 2 KiB probes | all 4 | 1 |
| s9 | H1 + H2 | s1-shaped load at C=16 plus 16 in-flight 3 s-delay requests; SIGHUP every 2 s with real WAF rule-ID and plugin-config changes; reload 5 has an invalid WAF directive | both | 1 |
| s10 | H3 | as s1 | none, both | 1 |
| s11 | H1 | 16 KiB **items** JSON (≈742 args), 4 KiB response, C = 1 / 2 / 4 / 8 / 16, 2 s warm-up, 10 s per step | none, waf | 1 |

Windows: 5 s warm-up, 20 s measurement (s9: 40 s) and 10 s recovery (s9: 15 s). These windows are
recorded with each sample. A sampler records the following every 500 ms:

- cgroup `memory.current`, `memory.stat` (anon, shmem, file), `memory.events` (oom_kill) and `cpu.stat`;
- process RSS, threads and open file descriptors;
- spill-file count and bytes in the private `TMPDIR`;
- from `/metrics`: goroutines, `go_memstats_heap_inuse/sys`, GC count, in-flight requests,
  `jul_plugin_instances`, and the `limited` pool's active and pending counts.

After recovery, `heap?gc=1` gives a post-GC heap figure and a metric delta is recorded: WAF
transactions by outcome, plugin invocations, instance waits, admission rejections, cache events and
reload outcomes.

## 4. Thresholds (fixed before final runs)

| ID | Applies to | Threshold |
| --- | --- | --- |
| T1 | every run | cgroup `memory.peak` < 576 MiB (75% of envelope) **and** no `oom_kill` |
| T2 | s1, s2, s3, s6, s7, s10 | 0 unverified 2xx and 0 statuses other than 200 |
| T3 | every run that survives | after recovery: in-flight = 0, goroutines ≤ baseline + 50, fds ≤ baseline + 20, post-GC heap ≤ baseline + 32 MiB, post-load canaries pass |
| T4 | s5 | `limited` pool active ≤ 8 and pending ≤ 16 at every sample; excess load gets 503 |
| T5 | s8 | fast-probe verified success ≥ 99% |
| T6 | s8 | every slow upload that is not cancelled completes with a verified 2xx |
| T7 | s9 | all valid reloads succeed; the invalid candidate is rejected and the previous generation keeps serving with canaries passing; T3 holds after churn |

Overhead and throughput ratios (`waf`, `wasm`, `both` relative to `none`) are reported as sizing
observations, not pass/fail. The report states whether `both` cost is roughly additive.

**Decision rule:**

- **A:** every applicable threshold holds for the envelope.
- **B:** a threshold fails in a configuration an operator could reasonably deploy, and no existing
  configuration bounds it. Guidance alone is not an acceptable resolution.
- **C:** a scenario cannot execute or its evidence is invalid. Example: canaries fail, or the
  control's work cannot be shown to run.

Exploratory results E1–E6 below already suggest that s3, s5, s8 and s11 may fail T1 or T5/T6. These
thresholds are not relaxed to accommodate them.

## 5. Repetition, noise and stop conditions

- s1–s3 run 3 times. The report gives the median and range, and flags any run whose throughput
  differs from the median by more than 20%. Other scenarios run once and are reported as single
  observations.
- An OOM kill ends that run. The kill is a recorded result, not an error. Each run starts on a fresh
  process, so later runs are unaffected.
- Each phase ends at its deadline. Requests still in flight at that point are counted as
  `unfinished-at-phase-end`; they are not dropped. Every client has a finite timeout of 30–60 s. The
  harness has a global `-watchdog` (90 min), and the matrix also runs under an external
  `timeout 5400`.
- If the Jul scope has no `memory.max` limit, the harness refuses to run. If the host itself shows memory
  pressure (another process killed, or swap use outside the scope), the matrix is aborted and
  recorded.
- H3 is reported separately. A UDP or QUIC failure that is local and unrelated to Jul's behaviour is
  recorded as unavailable, not as a pass.

## 6. Exploratory record and plan revisions (before freeze)

Three quick exploratory runs (1–3 s windows) and a no-cgroup diagnostic probe were made on the same
binary. Their raw summaries are retained unchanged under
[`evidence/2026-11-09-695/exploratory/`](evidence/2026-11-09-695/exploratory/).

| ID | Observation | Plan consequence |
| --- | --- | --- |
| E1 | `explore1`: every WAF variant was OOM-killed under the 768 MiB envelope at C=16 with 16 KiB **items** JSON. `none` and `wasm` survived. | The normal workload changed to **wide** JSON. The original many-argument shape is kept as s11, which ramps concurrency to locate the failure rather than hiding it. |
| E2 | Probe P1/P2: CRS transformations allocate about 10–58 MiB per request for 180–750 JSON arguments. Retained heap scales with peak concurrency and is released after two GCs. There is no monotonic leak. | s11 and post-GC heap measurements were added. |
| E3 | Probe P3: arguments beyond Coraza's default `SecArgumentsLimit` (1000) are silently dropped. An XSS payload after argument 1,000 returned **200 in block mode**. A rule on `ARGUMENTS_LIMIT_REACHED` in `inline_rules` rejects such requests with 413. | Not a resource measurement. It is pinned by `TestAssessmentArgumentLimitPartialInspection` and dispositioned in the report as a correctness and security finding. |
| E4 | All clean WAF traffic was `matched_observed` because the client sent `Host: 127.0.0.1`, which matches CRS 920350. | The harness now sends `Host: localhost`. Earlier exploratory WAF outcomes are not representative of clean traffic. |
| E5 | The harness compared the origin echo with the requested body size, not the generated size, so every proxied 2xx was marked unverified. WASM evidence checks were also off. | Verification now uses the actual body length, and WASM evidence is required whenever plugins are configured. |
| E6 | `explore2`/`explore3`: under s3 and s8, `inspect-a` returned `result="error"` (HTTP 500) under CPU pressure. In s8, fast probes received 503 `plugin busy` while 64–66 plugin instances were held by slow uploads. s5 `both` was OOM-killed. The `wasm` variants peaked at about 550–700 MiB with about 30–65 live instances. | No threshold changes. These are the hypotheses the final runs test. |

## 7. Reproduction

```sh
make build-full
( cd examples/plugins/inspect-load && GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o /tmp/a695/inspect-load.wasm . )
go build -o /tmp/a695/harness scripts/waf-wasm-assessment.go
mkdir -p /tmp/a695/key && chmod 700 /tmp/a695/key
export JUL_EFFECTIVE_VERSION_KEY_FILE=/tmp/a695/key/effective-version.key   # disposable source-build key
./jul version-key init
timeout 5400 /tmp/a695/harness run -jul ./jul -plugin /tmp/a695/inspect-load.wasm -out /tmp/a695/final
```

The harness writes `results.jsonl` and `manifest.json` (binary, plugin and config checksums). It also
writes a `samples.json`, `jul.toml` and `jul.log` for each run.
