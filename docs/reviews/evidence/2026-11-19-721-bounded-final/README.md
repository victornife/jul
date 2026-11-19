# Applied guest recycling and targeted performance check

This closes two bounded tasks: apply the already tested Go-guest reuse interval
to the maintained heavy profiles, and check the specific uninspected H1/C=16
performance result. It does not reopen the complete #700 qualification plan.

## Source, controls and limits

Application targets are main `155b6d94dfde580a9332c3a985c8ce156563fd20` and
candidate `91ed8858d114c99608f1e2fb750a5e92d233d244`. The candidate's Jul runtime
source is unchanged from `e8b6c0c4`. Both binaries use Go 1.27.4, Linux/amd64,
`CGO_ENABLED=0`, `-trimpath`, `-buildvcs=false`, and identical
`-ldflags '-s -w -X main.version=721-bounded-comparison'` with tags:

```text
brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf
```

Build records and SHA-256 identities are in [main-build.txt](main-build.txt),
[candidate-build.txt](candidate-build.txt) and [artifacts.json](artifacts.json).
The guest remains the byte-identical assessment artifact
`27b016a775020dca6cea66fdb98a836d1afbf056b2ac9883c142e4969c41034a`.

The initial [matrix](matrix.txt) was declared before measurement. Available CPUs
were 0–8. The temporary first-party [harness patch](diagnostic-harness.patch)
adds a non-qualification `bounded-local` profile (5 s warmup, 20 s measurement,
2 s recovery) and launches Jul through `taskset -c 0,1` with `GOMAXPROCS=2`.
The origin uses CPUs 2,3 and client CPUs 4–8. There were no overlapping builds,
soaks or other local benchmarks. The normal plan-v7 CLI is unchanged by this
temporary patch. Upstream dependencies and Jul runtime code were not patched.

This host has no accessible cgroup controls. The runner's `envelope=none`
metadata therefore says unconstrained: the temporary launcher supplies CPU
affinity, not memory or CPU-quota containment. This is a diagnostic comparison,
not a historical 768 MiB fit, A13 qualification, or cross-platform guarantee.

## H1, C=16, uninspected path

Three paired, interleaved main/candidate repetitions used the maintained
`u1-normal-h1/none` workload. All six runs were instrument-valid and correct,
with every attempted response verified. No inspection or guest mitigation
setting is active on this path.

| Repetition | Main goodput (requests/s) | Candidate goodput | Candidate change |
| --- | ---: | ---: | ---: |
| 1 | 6319.30 | 5954.80 | −5.8% |
| 2 | 6691.15 | 6797.20 | +1.6% |
| 3 | 6911.70 | 6851.15 | −0.9% |
| Median | 6691.15 | 6797.20 | +1.58% |

Main spread was 8.85%; candidate spread was 13.19%, above the old 10% noise
criterion. Jul's measured process CPU fractions were 0.586/0.584/0.592 on main
and 0.575/0.573/0.588 on candidate (two-CPU normalization). The old 33.6% gap
did not reproduce; no individual paired decrease exceeded 10%. These results
do not pass formal throughput qualification and do not establish the cause of
the historical ARM/WSL2 observation. A platform/environment/concurrency-specific
effect remains possible; it is not honest to call the old result fixed or
explain it solely by noise. The bounded repeat provides no reproducible
uninspected-path regression on which to base a runtime change.

## Applied recycling: real heavy-profile traffic

Plan v7 renders `max_invocations = 16` for both plugins in p1, p2/p2o/p2h and
the additional bounded s3/s4 diagnostics. Product defaults, the ordinary normal
profiles and original destructive fixtures stay unchanged. The extra reuse
setting is part of each scenario/plan identity; historical results are retained.

The maintained p1 workload retained 768 KiB request bodies, C=64, four instances
per plugin, the 2 s timeout, default 256 MiB request budget and soft
`GOMEMLIMIT=480MiB`. Both actual binaries accepted the generated configurations
before load. Each configuration contains the reuse setting twice.

| Run/order | Main verified requests | Candidate verified requests | Unexpected responses |
| --- | ---: | ---: | ---: |
| Initial, main then candidate | 2389/2389 | 2025/2025 | 0 |
| Profiled, candidate then main | 2351/2351 | 2300/2300 | 0 |

All **9065** load requests were verified; all pre/post canaries and request
lease/byte/evaluation drainage checks passed. Candidate guest reservations
matched the remaining idle instances at 16 MiB each; no busy, starting, closing
or failed-retained guests remained. Idle reservations are expected ownership,
not a leak. They are distinct from request gauges that must drain to zero.

The initial CPU-saturated sample's goodputs were 116.25/98.05 main/candidate
(−15.7%). A [supplement](profiled-matrix.txt) was declared to examine that
observation, with one reverse-order pair and a 15-second CPU profile per target.
Its goodputs were 114.35/111.80 (−2.2%); there were again no failures. Both
profiles are dominated by external/JIT execution and wazero's call engine
(about 74% flat samples together), followed by memory copying/clearing and
runtime work. Jul accounting was not a leading flat CPU hotspot. This does not
prove zero accounting cost or attribute the original difference to a specific
cause. The order, profiling overhead and small sample count preclude treating
these p1 runs as a throughput qualification or pooling them into a pass.

Earlier recycling adds instantiation work, but removed the reproduced guest
allocation failures in these runs. It is an explicit fixture trade-off, not a
promise that arbitrary plugins cannot run out of memory. See the earlier
[causal diagnostic](../2026-11-19-721-guest-diagnostic/README.md).

## Evidence and reproduction

[bounded-summary.json](bounded-summary.json) contains the calculations and guest
accounting checks. All attempts, configs, outputs, samples, canaries, manifests
and per-run checksums are retained in [perf](perf), [recycled-p1](recycled-p1)
and [profiled-p1](profiled-p1). The maintained verifier accepted all three run
directories. CPU profiles and their decoded summaries are under
[cpu-profiles](cpu-profiles).

To reproduce, build both application binaries with the flags above. In a
temporary candidate worktree, apply the first-party `diagnostic-harness.patch`
and build `./scripts/inspection-assessment` as `ia-diagnostic`. Do not apply the
patch to wazero, Coraza or production sources. Then run from that worktree:

```sh
taskset -c 4-8 "$B/ia-diagnostic" run -out "$B/perf-new" \
  -profile bounded-local -envelope none -scenario u1-normal-h1 -variants none -reps 3 \
  -target main:current:"$B/jul-main" -target candidate:current:"$B/jul-candidate" -origin-cpus 2,3
taskset -c 4-8 "$B/ia-diagnostic" run -out "$B/p1-new" \
  -profile bounded-local -envelope none -scenario p1-large-body-plugin-h1 -variants wasm -reps 1 \
  -target main:current:"$B/jul-main" -target candidate:current:"$B/jul-candidate" -origin-cpus 2,3 \
  -guest "$GUEST" -guest-source examples/plugins/inspect-load
```

Set `B` to the binary/output directory and `GUEST` to the guest built with the
maintained README commands. Output directories must not exist. The archived
[profile_pair.py](profile_pair.py) records the supplemental profiling driver;
its local checkout/output paths must be adapted together. The CPU profile
requests use only the loopback synthetic assessment credential. Builds and
other load must finish before measurement begins.

Conclusion: the mitigation is applied and verified, the specified 33.6% gap was
checked without reproducing it locally, and the supplemental profiles expose
no clear new Jul hotspot. No speculative runtime optimization is justified by
these observations. Historical failures and reduced-contract limitations stay
explicit; no engine forks or global-default changes were introduced.
