# Inspection resource assessment harness (ADR 0032 slice I5)

`scripts/inspection-assessment` is the maintained harness for ADR 0032 (#700). It replaces
these archived scratch tools:

- the #695 harness;
- the #699 harness patch;
- the ADR F3 and F4 clients.

The archived files stay immutable under `docs/reviews/evidence/`.

The harness includes:

- a synthetic origin;
- a verifying H1/H2/H3 load client;
- the F3 and F4 probes;
- an owned Jul process, launched inside a verified resource envelope;
- versioned run manifests with checksums;
- deterministic summaries.

It measures Jul; it does not make Jul enforce anything. Until I2–I4 land, the live WAF and plugin
request paths are not governed. That holds even in post-I1 builds that export governor metrics or
accept `mode = "enforce"`, and no result from this harness says otherwise.

## Prerequisites

- Linux. The package carries the `linux` build tag.
- The Go version pinned in `go.mod`. The guest is built with Go's `wasip1/wasm` target, as in
  `examples/plugins/build.sh`.
- For assessment-labelled runs, the `-envelope required` mode needs:
  - cgroup v2;
  - a systemd user manager (`systemd-run --user --scope`) that can set `MemoryMax`,
    `MemorySwapMax` and `CPUQuota`;
  - `taskset`;
  - enough CPUs to pin Jul (default 0,1), the origin (default 2,3) and the harness client
  (for example `taskset -c 4-9`) apart.

  The harness checks these and refuses to run when they are missing; it never falls back to an
  unconstrained run. Unit tests and `-envelope none` quick smokes need none of it.

## Commands

The examples run from the repository root. `$B` is a directory of Jul binaries.

```sh
# Build the harness and the synthetic guest.
go build -o /tmp/ia ./scripts/inspection-assessment
(cd examples/plugins && GOOS=wasip1 GOARCH=wasm go build -trimpath -buildvcs=false \
  -buildmode=c-shared -o /tmp/inspect-load.wasm ./inspect-load)   # or: examples/plugins/build.sh DIR all
/tmp/ia fixtures -guest /tmp/inspect-load.wasm \
  -guest-source examples/plugins/inspect-load        # prints the guest source/artifact and static-page sha256

# Print or check the frozen plan.
/tmp/ia plan > /tmp/plan.json
/tmp/ia plan -check scripts/inspection-assessment/plan.json

# Run a scenario or a suite. -out must not exist.
taskset -c 4-9 /tmp/ia run -out /tmp/ia-run -profile qualification -suite campaign-normal -reps 3 \
  -target base:baseline-858590dc:$B/jul-858590dc \
  -target main:current:$B/jul-main \
  -guest /tmp/inspect-load.wasm -guest-source examples/plugins/inspect-load

# A quick, unconstrained smoke. It is never comparison or acceptance evidence.
/tmp/ia run -out /tmp/ia-smoke -profile quick -envelope none -scenario u1-normal-h1 -variants none \
  -reps 1 -target main:current:$B/jul-main

# Verify the evidence, then regenerate the summary and comparison.
/tmp/ia verify /tmp/ia-run
/tmp/ia summarize -out /tmp/ia-report -compare base:main /tmp/ia-run [more run dirs]

# Calibrate the unsaturated normal concurrency from a calibration suite run.
/tmp/ia calibrate -target base /tmp/ia-cal
```

### Targets

`-target label:adapter:path` names a binary, its configuration adapter and its label. Each target
runs every selected cell in turn with the same client, origin and guest.

### Validating the runner

Use these flags to check that the harness itself detects failures:

- `-fault truncate|corrupt|stall` injects an origin fault.
- `-kill-jul-after D` sends SIGKILL to Jul inside each cell.
- `-watchdog D` bounds a cell.

A run with a fault never qualifies for comparison: `summarize` refuses to mix it with clean runs.
`summarize -compare` also refuses runs that are not qualification-profile runs with a required
envelope, so smoke and unconstrained runs never receive a verdict.

## Profiles and suites

| Profile | Use |
| --- | --- |
| `qualification` | The archived #695/#700 windows. It requires `-envelope required` and is the only profile eligible as I5/I6 evidence. |
| `quick` | Short windows for exercising the instruments. It has its own identity in every manifest and is never comparison, capacity or acceptance evidence. Expect instrument-invalid classes in saturated cells, for example s8 `waf` with no slow attempt inside a 3 s window. |

| Suite | Scenarios |
| --- | --- |
| `calibration` | `cal-normal-h1/h2/h3`: concurrency steps that choose the unsaturated normal profile. |
| `campaign-normal` | `u1`/`u2` (H1/H2 across `none`/`waf`/`wasm`/`both`) and `u10` (H3 `none`/`both`) at the calibrated concurrency, 3 repetitions. These are the throughput comparison cells. |
| `fidelity` | `r1`, `s8`, `s11l`, `s9`, F3 pending 16 and 0, F4, `s6`, `s7`, `r2`: instrument fidelity smokes. Their outcomes are recorded without a pass verdict. |
| `historical-695`, `historical-699` | The archived s1–s11 and r1–r3 definitions. |
| `destructive` | `s3`, `s4`, full `s11`. They OOM uncapped runtimes and are kept for I6 runs in an isolated envelope. |

`plan.json` is the frozen plan: scenarios, profiles, suites, interpretation rules and the
configuration-template hash. A test fails when the code and `plan.json` disagree. Any change to
scenarios or rules must regenerate `plan.json` (`/tmp/ia plan > scripts/inspection-assessment/plan.json`)
and be reviewed. Runs made under another plan hash are not comparable.

## What a run records

```text
<out>/manifest.json              versioned manifest
<out>/plan.json                  the plan in force (its sha256 is in the manifest)
<out>/fixtures/                  the static page
<out>/cells/<scenario>/<variant>/<target>/rep<N>/
    config.toml  check.txt       generated config and the target's own `jul check` output
    result.json                  per-class outcomes, phases, canaries, envelope, metrics, verdict inputs
    samples.jsonl                cgroup/process/metric samples
    jul-output.txt               Jul's stdout/stderr, kept when Jul crashes
<out>/SHA256SUMS                 every file except itself
```

The manifest records:

- the run ID, timestamps, final state and failure reason;
- the plan, profile, rules and scenario hashes;
- the Jul source commit, dirty state, binary sha256 and `go version -m` build settings;
- the harness module and build identity;
- the guest source and artifact hashes;
- the adapters;
- the platform;
- the requested envelope and client/origin placement.

Each result records the effective envelope that the kernel applied: `memory.max`,
`memory.swap.max`, `cpu.max`, the Jul affinity and `GOMAXPROCS` read from the process.

`verify` rejects a run with any of these problems:

- a missing, extra or modified file;
- a malformed `SHA256SUMS`;
- a manifest that is unfinished or has the wrong schema;
- a `plan.json` that does not match `manifest.plan_sha256`;
- a result whose scenario hash differs from the manifest.

`summarize` rejects any set of runs that mixes plans, rules, profiles, envelope modes, faults,
guests, or a target label with different binaries or adapters. Its report has one row per cell
(the primary class of the first phase), comparisons, a row per repetition, phase and workload class
(including probe, slow, warm and later-phase classes), and the F4 probe outcomes.

`counter_deltas` in each result are diagnostic Jul counters (WAF outcomes, plugin invocations,
errors and traps, instance waits, admission, cache, reloads). They do not affect any verdict. A
test checks each label filter against the values Jul documents, so a filter cannot read a false
zero.

### Interpreting results

Each run is classified on two separate axes.

- **Instrument validity.** The run completed, its canaries ran, the envelope was verified when
  required, and each class started attempts inside its window using the planned protocol and
  connection count.
- **Runtime correctness.** No unexpected status or error, unverified content, failed canary, Jul
  exit or OOM-kill delta.

A valid run with runtime findings is accurate evidence of a failure. It is reported and never
dropped. Goodput counts only responses that are verified (status, full length, content, and the
origin and plugin canaries) and that started and finished inside the measurement window. The
following never count as goodput and are reported separately:

- overloads and security rejections;
- read errors, resets and cancellations;
- requests that cross into the window from warm-up;
- requests still outstanding at the window end.

When a metric is unavailable, it is recorded as unavailable, never as zero. These measurements are
separate:

- cgroup peak and current memory;
- RSS;
- live heap after two forced GCs, through the admin pprof endpoint;
- governor gauges, which exist only on post-I1 binaries.

The OOM delta comes from the parent cgroup's hierarchical `memory.events`, so it also counts
sibling scopes. Governor counters show only that the I1 foundation exported them: the I1
foundation has no request path that uses them, so they are not memory protection.

The comparison rules are frozen in `plan.json` under `interpretation`. Verdicts are applied in this
order:

1. `insufficient`: fewer than 3 repetitions on a side.
2. `instrument-invalid`: any invalid run.
3. `fail-correctness`: an incorrect candidate run.
4. `baseline-invalid`: an incorrect baseline, or a baseline median of 0.
5. `inconclusive-saturated`: any run at 85% or more of the envelope CPU, or with CPU unavailable.
6. `fail-regression`: median regression above 10%, even when the ranges overlap.
7. `inconclusive-noise`: a spread, (max−min)/median, above 10% on either side.
8. `pass`.

Inconclusive cells need more repetitions, which supplement the earlier ones. Nothing is dropped or
replaced. A pass between the ADR baseline and post-I1 main is informational: I1 changes no request
path, so it is not A13 acceptance.

## Configuration adapters

| Adapter | Binaries | Difference |
| --- | --- | --- |
| `baseline-858590dc` | `858590dc` and the archived `453e6237` | No `[global.inspection]`. These runtimes reject that section. |
| `current` | Post-I1 main (`a57afa4d` and later) | Adds `[global.inspection] max_buffered_bytes = "512m"` only to `waf` and `both` variants. Route-aware enforce validation of the unchanged 1 MiB WAF body limit needs 302,596,096 bytes, more than the 256 MiB default. Other variants keep the default. |

Every security and workload setting is identical across adapters, including:

- CRS;
- request and response body inspection and their limits;
- the plugin chain;
- the listener and the cache.

A test checks this. Each cell runs the target binary's own `jul check` on its generated
configuration before measuring, so a binary is never given fields it does not support. A configured
512 MiB budget only passes validation: it is not evidence that the process fits the 768 MiB
envelope.

## Intentional differences from the archived definitions

The `changes_from_archive` field of each scenario in `plan.json` lists its differences. In summary:

- **Verified goodput.** The archived clients counted any 200. Responses are now verified.
- **Unsaturated normal profile.** `u1`/`u2`/`u10` run the s1/s2/s10 shapes at the calibrated
  concurrency. Inspected variants run at C=1 (`waf`, `both`) or C=2 (`wasm`) instead of C=16, which
  saturated them, so they are not comparable with the #695 C=16 numbers. `none` stays at C=16. The
  C=16 shapes remain as capacity scenarios.
- **JSON bodies.** F3 and F4 bodies are the wide JSON shape (`application/json`) instead of `x`,
  `p` or `s` fill, so the `both` variant inspects ordinary JSON.
- **Measured connections.** The F3 and F4 connection counts are measured: each worker or probe
  class, and each F4 H2/H3 sibling, must dial exactly one connection. H3 is checked by the
  negotiated protocol and dial count, not by client configuration.
- **s9.** In-flight requests on the route whose policy changes are retired within 2 s by design
  (#642/#562, implemented in #653; WAF rules and plugin modules are access inputs). The in-flight
  class therefore expects `error:stream-reset`.
- **s5 body size.** s5 uses a 256 KiB body, as in the archived code. The #695 plan text said 64 KiB.
- **r1 probe.** r1 keeps the 2 KiB POST probe that the measured #699 patch used. The review text
  called it GET.
- **s11l.** s11l is a C=1/C=2 subset of s11. The full ramp is destructive.

These are not ported:

- **F2.** The I1 governor (`internal/inspection`) and its unit, race and benchmark tests supersede
  it. The archived scratch WAF-slot patch is never applied to measured binaries.
- **F1/F1b.** Calibrated WAF reservation work belongs to I3.
- **F4b.** The runtime deadline owner test belongs to I2/I3.
- **The F3 `Retry-After` pacing mode.**

## Failure handling

- Jul runs in its own process group and, with `-envelope required`, in a transient systemd scope
  named for the run. The origin is a separate child process that the harness owns, outside the
  envelope. The client and probes run in the harness process.
- Startup, requests, drain and shutdown are bounded. Shutdown sends SIGTERM, then SIGKILL after
  `StopGrace` (15 s by default).
- A crash, OOM kill, watchdog expiry or interrupt is recorded in the result and manifest:
  - Jul's output, partial outcomes and samples are kept;
  - the run is marked `failed` or `interrupted`;
  - checksums are still written.
- The harness removes only scopes and temporary directories it created.
- To resume after an interruption, start a new run directory with the missing cells. `summarize`
  checks that it is compatible. Never edit or delete a failed run directory.

## Mapping to ADR 0032 A1–A18

The harness provides the instrumentation. It does not satisfy any criterion: every row below is
still pending until I2–I4 implement it and I6 runs it.

| # | Maintained scenario or instrument | Also required (later slice) |
| --- | --- | --- |
| A1 | `s11-arg-amplification-h1` (destructive) and `s11l` smoke; overload class timing | I3 slots/reservations; 503 latency check in I6 |
| A2 | `s3-large-h2`, `s4-ramp-large-h2` (destructive) | I2/I3 charging |
| A3 | `s5-admission-h2`; the F3 scenarios set `max_pending_requests` 0 and 16 | I6 runs s5 at both pending values |
| A4 | `s11` with `both`; peak and per-class metrics | I3 per-class limits; combined fixture in I6 |
| A5a | `s6`, `s7`, `r2` with `none` | Lease counter assertion after I2/I3 create leases |
| A5b | `s6`, `s7`, `r2` with `both` | I2/I3 slots and charging |
| A6 | `s8`, `r1`, `f4-stalled-body` (H1/H2/H3, stall and slow) with external watchdog | I2/I3 `body_timeout` deadline owner; drain-to-zero check |
| A7 | `f4` `slow` mode below the limit | I2/I3 deadline |
| A8 | — (needs a two-listener fixture) | I6 scenario plus I1 unit tests (single process governor) |
| A9 | `s9-reload-churn` | I1 reload/limit unit tests; I6 overlap, rollback and mode-toggle variants |
| A10 | `f4-stalled-body` H3 with the sibling connection check | I2/I3 per-stream deadline |
| A11 | — | I3 unit and race tests (cancellation during evaluation) |
| A12 | WAF response inspection in `waf`/`both` cells; plugin subscription in `both` | I4 |
| A13 | `u1`, `u2`, `u10` (calibrated); `r3` as a capacity scenario | I6 in enforce mode; a calibrated `r3` cell is added in I6 |
| A14 | — | I2–I4 observe/enforce deterministic tests |
| A15 | Heap after two GCs, RSS and cgroup memory per sample, governor gauges | I6 burst-then-idle runs after I2–I4 |
| A16 | — | I2/I4 unit tests (fetch, request state, nested subscriptions) and I1 minimum-budget tests |
| A17 | `f3-admission-pending0` and `-pending16` (single connection, separate probe) | I3 fail-fast phase 1 |
| A18 | — | I3/I4 unit and race tests (implicit phases, phase 5) |

## Tests

`go test -race ./scripts/inspection-assessment` runs the following, without privileges:

- unit tests for plan selection and fidelity, adapters, verification, goodput accounting,
  comparison verdicts, manifests and checksums, and the envelope readers (on a fake `/proc` and
  cgroup tree);
- end-to-end tests against a fake Jul (the test binary re-executed), including:
  - truncated and corrupted responses;
  - Jul exits at startup, never becoming ready, check failures, crashes, ignored SIGTERM and kills;
  - interrupts and the watchdog;
  - reload churn.

CI includes the package in the normal test and coverage jobs. Full qualification runs are explicit
assessment commands and are not part of CI.
