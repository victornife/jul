# #721 current-main bounded qualification

## Frozen matrix and pass criteria (recorded before execution)

Production targets: current `main` `155b6d94dfde580a9332c3a985c8ce156563fd20` and PR candidate
`e8b6c0c42080255b8e5a29ec753651774409df2c`. Build both with Go 1.27.4, Linux/arm64, the same
full-tag set, static linking, `-trimpath`, and matching strip/version flags. Use the maintained
assessment harness and synthetic clients/origin. Run targets interleaved by repetition.

Planned qualification cells use cgroup v2 with two CPUs, `memory.max=805306368`, zero swap,
`GOMAXPROCS=2`, Jul pinned to CPUs 0-1, origin to 2-3, and harness client to 4-9. Do not overlap
the measurements with a build, benchmark, soak, cleanup, or another qualification process. The
host is shared Linux/arm64 WSL2; report remaining host interference and treat noise as inconclusive.
Use Jul's own cgroup `memory.peak`, `memory.events`, and exit status; report the separate parent
hierarchical OOM counter only as sibling-inclusive context.

| Matrix | Planned selection | Repetitions and configuration |
| --- | --- | --- |
| Current-main normal comparison | `campaign-normal`: H1/H2/H3 across its planned `none`, WAF, WASM and combined variants | Three paired main/candidate repetitions; unchanged workload shapes and configurations |
| Stabilized H1 control | `u1b-normal-h1-none-c8` | Five paired repetitions, verified successful goodput only |
| Sized request-only profiles | `p1-large-body-plugin-h1`, `p2-saturation-plugin-h1`, and observe twin `p2o-saturation-plugin-observe-h1` | Preserve plan definitions: four instances per plugin, 2 s timeout, `GOMEMLIMIT=480MiB`; p1 default 256 MiB budget, p2/p2o 128 MiB; no-backoff p2h excluded as record-only |
| Bounded A1/A2 diagnostics | `bounded-envelope-diagnostic`: plan-v4 copies of s11, s3 and s4 | Proposed WAF/combined `max_buffered_bytes=320m`; A2 uses four instances per plugin, 2 s timeout and `GOMEMLIMIT=480MiB`. Validate generated config with each actual binary before load. This is diagnostic only; original A1/A2 verdicts are unchanged. The combined 320m proposal was subsequently rejected; see results. |

Normal comparison passes only if every required cell is instrument-valid and correct, the median
candidate goodput regression is at most 10%, spread on both sides is at most 10%, and no run is
CPU-saturated by the frozen plan rule. A noisy or saturated result is inconclusive, never a pass.
Use the plan's verified status/body/content/origin/plugin checks; rejected work is not goodput.

For A1, preserve the original 576 MiB peak and 50 ms overload-p99 limits. The only expected reject
is the exact governor signature: matching negotiated protocol, HTTP 503, `Retry-After: 1`, and
body `inspection_overloaded`. Any other status/error, Jul exit, OOM event, failed canary, or
nonzero post-recovery governor gauge remains a finding. For A2, preserve its original outcomes and
resource verdicts; the separate 320m run can support only the sized profile and cannot change the
historical result. `GOMEMLIMIT` is a soft runtime target, not a process or cgroup limit.

## Build identities and executed commands

The host was Linux/arm64 WSL2 (`6.18.58.1-microsoft-standard-WSL2`), Go `go1.27.4`.
The 12-CPU host allowed the harness's required CPU affinity and a user-systemd scope with
`MemoryMax=768M`, `MemorySwapMax=0`, and `CPUQuota=200%`. The current shell itself is in an
unlimited shared cgroup; only Jul is measured inside the harness-owned scope.

The initial source manifest comparison found no `go.mod` or `go.sum` changes between the targets.
Both application binaries used:

```sh
FULL_TAGS='brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf'
CGO_ENABLED=0 go build -tags "$FULL_TAGS" -trimpath -buildvcs=false \
  -ldflags '-s -w -X main.version=qual-main-155b6d94' \
  -o /tmp/jul-721-qualification/bin/jul-main ./cmd/jul
CGO_ENABLED=0 go build -tags "$FULL_TAGS" -trimpath -buildvcs=false \
  -ldflags '-s -w -X main.version=qual-candidate-e8b6c0c4' \
  -o /tmp/jul-721-qualification/bin/jul-candidate ./cmd/jul
```

The assessment harness was built from candidate plan v4 with
`go build -trimpath -buildvcs=false -o /tmp/jul-721-qualification/bin/ia ./scripts/inspection-assessment`.
Both WASI guests used Go 1.27.4 and the README's `GOOS=wasip1 GOARCH=wasm go build -trimpath
-buildvcs=false -buildmode=c-shared` commands.

| Artifact | SHA-256 |
| --- | --- |
| `jul-main` | `cbc506c19b48a910342e4c4a07f6e3919f6ff524eeaff384a9b1ce95274da609` |
| `jul-candidate` | `c96daf7a91d04f46f99148dd21859f62f6576a45558b255748fe67184090a902` |
| plan-v4 harness | `e5ee93e02fa0e832f1af9fffccae23612dd75d7e1e57447e0e2da5deba819e81` |
| `inspect-load.wasm` | `27b016a775020dca6cea66fdb98a836d1afbf056b2ac9883c142e4969c41034a` |
| `inspect-nobody.wasm` | `5df6763d8a9fc4ff15d0931376cade46f6d50238fcde412d0a341400f3afa22e` |

Both `go version -m` records identify the same official Coraza v3.8.1 and Wazero v1.12.0
modules, full build-tag set, Linux/arm64 target, `CGO_ENABLED=0`, and `-trimpath`. Plan-v4 SHA-256:
`b2aa9881ffa94c3b32fb9d092da6818a6da46d81cce304aad21c0b6637429530`.

## Execution and decision

**HOLD.** The current-main comparison does not establish the absence of a material regression:
all normal comparisons are noisy under the predeclared spread rule, including the five-repetition
control. The sized plugin workload also produced unexpected 500s on both revisions. The combined
A2 diagnostic never reached load because its configuration was invalid even after the one
targeted harness correction. No PR-specific cause has been established for the 500s or the noisy
throughput. The smallest next action is an isolated, low-noise paired comparison plus diagnosis
of the p1 contained guest failures; validate a combined route budget above the measured floor
before any future A2 run. This is not a request to change production defaults.

The harness used whitespace trimming for the v4/v5 overload body. Its recorded signature checked
status, retry metadata, protocol and trimmed body, but the aggregated raw records cannot prove
byte-exact bodies. V6 corrects this for future runs; do not retroactively label v4 overloads as
byte-exact verified.

| Evidence | Outcome |
| --- | --- |
| [Normal and control](normal-summary/summary.md) | All 76 runs valid and correct. H1 `none` median 5219.6 vs 3466.0 requests/s (33.6% lower), candidate spread 21.7%; H3 `none` 2342.6 vs 1401.1 (40.2% lower), spreads 11.3%/28.1%; H1 WASM 378.1 vs 294.2 (22.2% lower), spreads 32.3%/30.0%. The frozen summary labels these `fail-regression` because it checks regression before noise; under the predeclared requirement that **both** spreads be at most 10%, they are inconclusive for attribution, not passes. Five-rep u1b medians are 4315.9 vs 4333.6, spreads 36.7%/41.2%, also inconclusive. The full summary preserves every cell. |
| [Sized plugins](plugin-summary/summary.md) | p1 WASM returned 16/2607 HTTP 500 on main and 18/3008 on candidate, with corresponding plugin panic/error counters; warning logs say contained guest failure with `reason=unknown`. Both were CPU-saturated (0.981/0.964 of quota). Source and logs do not establish the underlying trap reason; this is a real finding, not overload. Peaks were 487.5/509.5 MiB. p2 enforce returned 514/538 classified overloads and verified goodput 19.9/19.9 rps; overload p99 192.4/176.4 ms, above 50 ms (which is an A1 objective, not a p2 pass rule). p2 observe produced no overload responses and 24.0/24.0 rps. These are one repetition, not a regression proof. No-backoff p2h was not rerun and remains record-only, outside protection claims. |
| [A1 diagnostic](diagnostic-summary/summary.md) | WAF-only 320m was accepted by each actual binary; all six WAF runs completed with canaries and post-recovery checks passing. Main peaks 473.7–476.1 MiB, candidate 467.6–473.0 MiB, below 576 MiB. Candidate overload p99 was 52.8–65.9 ms across C=4/8/16 (main 45.1–61.3 ms), so the 50 ms objective was not met. Candidate C=16 legitimate goodput was 5.3, 7.0, 6.0 rps. CPU was 0.98–1.00 at C=2 and above. This does not pass historical A1. |
| [A2 initial](diagnostic-summary/summary.md), [targeted v5](a2-v5-summary/summary.md) | Initial v4 produced 16 render failures: full fixture did not support the requested per-plugin settings. After fixing the harness, eight `both` cells in v5 failed `jul check` before startup: 320m = 335544320 B, but both binaries require at least 406503427 B for this combined route. Eight `none` cells ran correctly. The v6 448m proposal has not been run or checked and does not establish a supported fit. Historical A2 remains failed. |

Across all five run directories, all 104 actually started Jul processes exited normally; sampled
Jul-cgroup `oom_kill` was zero in every sample, and all 104 parent hierarchical deltas were zero.
This is not an end-of-lifetime own-cgroup event snapshot: the sampler can miss events between
samples. The 24 invalid A2 cells never started Jul. Canaries and recovery results are in each
`result.json`. Shared host/WSL2 interference remains possible; cgroup limits bound Jul, not the
client, origin or whole host.

The original campaign used `/tmp/jul-721-qualification/bin/ia` and the target/fixture flags
below for `-suite campaign-normal -reps 3`, `-scenario u1b-normal-h1-none-c8 -reps 5`,
`-scenario p1-large-body-plugin-h1,p2-saturation-plugin-h1,p2o-saturation-plugin-observe-h1`,
and `-suite bounded-envelope-diagnostic`, respectively. Each invocation used
`taskset -c 4-9`, `run -profile qualification -envelope required`, a distinct `-out`, and:

```sh
-target main:current:/tmp/jul-721-qualification/bin/jul-main \
-target candidate:current:/tmp/jul-721-qualification/bin/jul-candidate \
-guest /tmp/jul-721-qualification/bin/inspect-load.wasm -guest-source examples/plugins/inspect-load \
-guest-nobody /tmp/jul-721-qualification/bin/inspect-nobody.wasm -guest-nobody-source examples/plugins/inspect-nobody
```

The one targeted correction used the same flags and envelope with the v5 harness and
`-scenario s3-large-h2-bounded,s4-ramp-large-h2-bounded`. Its harness SHA-256 was
`1c089233feddf3d66cc5dea82e4b50fbf9f7508fc845500c29c39bc4a15a37ac`; v5 plan SHA-256 was
`28491190ebf71488ab9dc93a9a1fcf3e8f27430bcd4e935f67392d528cce0a07`. All five directories
passed their corresponding `ia verify` command. Plan versions are retained separately; summaries
do not combine them.

## Separate adversarial review

This was a separate self-review, not an independent reviewer. It rechecked the rule-control
bypass tests, retained-ticket threshold and reload/recycling evidence in the existing ledger;
this campaign changes no production code. It found the invalid combined-route assumption and the
whitespace-trimming classification gap above. The latter is corrected for future v6 runs; the
earlier evidence remains qualified rather than rewritten. The assessment does not establish a
physical-memory bound or completed A1–A18/A21/A22 qualification. A19 and A20 remain deferred.
