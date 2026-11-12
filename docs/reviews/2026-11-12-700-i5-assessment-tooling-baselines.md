# #700 I5: maintained inspection assessment tooling and baselines

> Date: 2026-11-12 · Issue: #700 (open) · Programme: #62 · ADR:
> [0032](../adr/0032-aggregate-inspection-resource-governor.md) slice I5 · Evidence:
> [`evidence/2026-11-12-700-i5/`](evidence/2026-11-12-700-i5/README.md)

## Status and boundary

This review records I5 for ADR 0032. The archived #695, #699 and #700 ADR instruments are now
maintained and tested tooling in
[`scripts/inspection-assessment/`](../../scripts/inspection-assessment/README.md). Using that
tooling, a frozen, bounded baseline campaign was run against the ADR comparison baseline and
post-I1 main.

It does not:

- implement I2–I4;
- satisfy any of A1–A18;
- change feature maturity;
- qualify a release.

I1 (#706) adds the governor, configuration, compiled-fact validation and reporting. On current
main no WAF or plugin request path takes a lease, charges allocation, holds an evaluation slot,
enforces the response-header ceiling or applies an inspection body deadline. This was re-checked
on `363d6cd4`, and the F4 results below confirm it from outside. An `enforce` configuration or the
exported `jul_inspection_*` series is therefore not request-path protection. Every throughput
verdict here is informational.

Archived #695, #699 and #700 ADR reports, raw data and checksums are unchanged.

## Identities

| Item | Identity |
| --- | --- |
| Original #695 runtime | `453e62375bdce0302756ff386637832d5fa35bad`. Historical only; not rerun. |
| ADR comparison baseline | `858590dc7c430d24cd33b096a81ca6051591b427` (contains #702 and #703) |
| I1 merge (#706) | `a57afa4d0a241dacfa6c76efc814daad8be2dfca` |
| Post-I1 main measured | `363d6cd4a9fef4c020b25e5c810b2bd34e481732` (I1 plus the #707 test-only fix). This was `origin/main` throughout. |
| Harness, frozen plan and measurement | `30ff0650` (`ia` sha256 `3a39975e7849db319b086fde624947b51f6b9d5b867d7fbac2f9ec56b39a3fc2`) |
| Harness, diagnosis and summaries | `21cb2440` (diagnostic counter labels, class and probe tables; see [Harness defects](#harness-defects-found-during-the-campaign)) |
| Frozen plan | `inspection-assessment-plan/v1`, `plan.json` sha256 `8432278801a88346f9a60c118ac41c9ba2d8bb766e23d73b0f9da5915df0db5d`. Interpretation sha256 `a65f6713…`. |
| Synthetic guest | `examples/plugins/inspect-load`. Source tree sha256 `217eb5033fa205bb2ca702c908e03294c59b849d6ba61a0e41b9de8f24ecf3c0`; `wasip1` artifact sha256 `27b016a775020dca6cea66fdb98a836d1afbf056b2ac9883c142e4969c41034a` (go1.27.4, `-trimpath -buildvcs=false -buildmode=c-shared`). The artifact is not committed. |

All three Jul binaries share the same build settings:

- `CGO_ENABLED=0`, linux/arm64, `vcs.modified=false`;
- tags `brotli,zstd,acme,console,otel,grpc,http3,importer,wasmplugins,stream,consul,kubernetes,waf`.

| Label | Source | Toolchain | Binary sha256 | Adapter | Role |
| --- | --- | --- | --- | --- | --- |
| `base` | `858590dc` | go1.27.3 | `520febcbad5669da0ea8ad98e29f0bb78304817511cd7ed63bfb48e9d63ea52f` | `baseline-858590dc` | ADR baseline built as pinned (the historical toolchain) |
| `base4` | `858590dc` | go1.27.4 | `e0f105cbae69939b5b91a5bbfd7165f993fcb181c4d813bfa241486ab3a2291b` | `baseline-858590dc` | Same-toolchain rebuild, labelled separately |
| `main` | `363d6cd4` | go1.27.4 | `db6c6378b782b25e41fdfdbaa139517440e7beec44349a5fc6ee8df57800d7c8` | `current` | Post-I1 secondary baseline |

The `base`→`main` difference combines I1 and the Go 1.27.3→1.27.4 update. `base4`→`main` isolates
the source change, and `base`→`base4` isolates the toolchain.

### Host and envelope

The host is a WSL2 kernel `6.18.54.1-microsoft-standard-WSL2`, aarch64, 12 CPUs, 15.4 GiB RAM,
with 4 GiB host swap. Manifests record `cpus: 6`, which is the harness's own affinity (CPUs 4–9).
Jul ran in a transient systemd user scope.

Each result records the effective envelope:

- `memory.max` = 805306368 (768 MiB);
- `memory.swap.max` = 0;
- `cpu.max` = `200000 100000`;
- `cpus_allowed_list` = `0-1`;
- `GOMAXPROCS=2`.

All of these were read back from the kernel and from the Jul process, and marked `verified`.

The origin ran on CPUs 2–3 and the client on 4–9, both outside the envelope. Side work during
measurement was pinned to CPUs 10–11. The OOM delta reads the parent cgroup's hierarchical
`memory.events`, so it would also count sibling scopes; none ran concurrently.

## What was promoted

The harness (`scripts/inspection-assessment/`, package `main`, Linux build tag) provides these
subcommands:

- `plan`;
- `fixtures`;
- `origin`;
- `run`, which takes a scenario/variant or a suite, `-target label:adapter:path`, `-out`,
  `-profile` and `-envelope`;
- `verify`;
- `summarize`;
- `calibrate`.

It consolidates:

- the #695 client, origin and scenarios s1–s11;
- the #699 r1–r3 patch;
- the F3 admission client and the F4 stalled-body client from the ADR evidence.

The synthetic guest is ported to `examples/plugins/inspect-load`. It uses the supported SDK and
`build.sh`/`build.ps1`, and keeps its body, response and invocation checks; its unit tests run
natively. F2 is not ported, because the I1 governor and its unit, race and benchmark tests
supersede it. The archived F2 WAF-slot patch was never applied to a measured binary.

The [README](../../scripts/inspection-assessment/README.md) and each scenario's
`changes_from_archive` in `plan.json` list every intentional difference. In brief:

- **Verified goodput.** The archived clients counted any 200; every response is now verified
  (status, full length, content, origin and plugin canaries). Archived throughput figures are not
  directly comparable.
- **Unsaturated normal profile.** The comparison cells u1, u2 and u10 reuse the s1, s2 and s10
  shapes at a calibrated concurrency. The inspected variants run at C=1 or C=2 instead of C=16, so
  they are not comparable with the #695 C=16 figures. The C=16 shapes remain as capacity
  scenarios.
- **F3/F4 bodies.** They use the wide JSON shape. Connection counts are measured: H2 and H3
  siblings must share the single measured connection.
- **s9.** In-flight requests on a route whose policy changes are retired by design (#642/#562,
  #653), so they expect `error:stream-reset`.
- **Archived code over plan prose.** The s5 body (256 KiB) and the r1 POST probe follow the
  archived code where the plan prose differed.
- **s11l** is a low-concurrency subset of s11.

### Configuration compatibility

The `baseline-858590dc` adapter emits no `[global.inspection]` section. The `current` adapter adds
`max_buffered_bytes = "512m"` only for the `waf` and `both` variants: route-aware enforce
validation of the unchanged 1 MiB WAF body limit requires 302,596,096 bytes, more than the 256 MiB
default. A test asserts that the WAF, CRS, body limits, response inspection, plugin chain, listener
and cache are identical across adapters. Before measuring, each cell runs the target's own
`jul check`. The 512 MiB setting is a validation requirement, not evidence that the process fits
768 MiB.

## Method

1. **Pre-freeze runner validation.**
   - Run against real post-I1 Jul in the envelope, with the quick profile: success, truncated
     body, corrupted body, stalled origin, a SIGKILL of Jul, and a 5 s watchdog.
   - All six were detected and classified correctly and verified.
   - The 5 s watchdog was longer than the cell, so it did not fire. It was repeated after the
     freeze with 2 s.
2. **Calibration.** The `calibration` suite ran on `base` (qualification, envelope) under the
   pre-freeze plan; the harness was a dirty development build, sha `d6a326f1…`.
   - `calibrate` chooses the largest concurrency with Jul CPU at or below 0.70 of the envelope and
     every response verified.
   - The calibration scenario hashes are identical in the frozen plan, so the decision transfers.
3. **Freeze.**
   - The chosen concurrencies were written into `plan.go`, and `plan.json` was regenerated.
   - Commit `30ff0650` then froze the scenarios, the run plan and the interpretation rules before
     any comparison run. The rules were not changed afterwards.
4. **Frozen campaign.** [`scripts/campaign.sh`](evidence/2026-11-12-700-i5/scripts/campaign.sh)
   ran:
   - controlled validation again (success, truncate, corrupt, stall, kill, 2 s watchdog);
   - `campaign-normal`, 3 repetitions with targets interleaved by repetition;
   - `fidelity` on `base` and `main`.
5. **Supplements and investigation.**
   [`scripts/supplement.sh`](evidence/2026-11-12-700-i5/scripts/supplement.sh) ran after the
   campaign. The original runs remain.

### Interpretation rules (frozen)

Verdicts apply in this order:

| Verdict | Condition |
| --- | --- |
| `insufficient` | Fewer than 3 repetitions |
| `instrument-invalid` | Any invalid run |
| `fail-correctness` | An incorrect candidate run |
| `baseline-invalid` | An incorrect baseline run |
| `inconclusive-saturated` | CPU ≥ 0.85 or unavailable |
| `fail-regression` | Median regression > 10% |
| `inconclusive-noise` | (max−min)/median > 10% on either side |
| `pass` | Otherwise |

A pass is informational and is not A13 acceptance. Runs are never dropped or replaced.

## Commands

From the repository root, with `$B` holding the three Jul binaries:

```sh
# Build the harness and the guest, and record their identities.
go build -o /tmp/ia ./scripts/inspection-assessment
(cd examples/plugins && GOOS=wasip1 GOARCH=wasm go build -trimpath -buildvcs=false \
  -buildmode=c-shared -o /tmp/il.wasm ./inspect-load)
/tmp/ia fixtures -guest /tmp/il.wasm -guest-source examples/plugins/inspect-load
/tmp/ia plan -check scripts/inspection-assessment/plan.json

# One representative comparison cell (u1 H1, WAF variant), qualification profile, in the envelope.
taskset -c 4-9 /tmp/ia run -out /tmp/u1-waf -profile qualification -scenario u1-normal-h1 -variants waf -reps 3 \
  -target base:baseline-858590dc:$B/jul-858590dc-go1.27.3 \
  -target main:current:$B/jul-363d6cd4-go1.27.4 \
  -guest /tmp/il.wasm -guest-source examples/plugins/inspect-load

# Verify the run and summarize it.
/tmp/ia verify /tmp/u1-waf
/tmp/ia summarize -out /tmp/u1-waf-summary -compare base:main /tmp/u1-waf

# Verify the archived evidence and regenerate its summaries.
(cd docs/reviews/evidence/2026-11-12-700-i5 && sha256sum -c --quiet SHA256SUMS)
docs/reviews/evidence/2026-11-12-700-i5/scripts/regenerate-summaries.sh /tmp/ia /tmp/i5-summary
diff -r docs/reviews/evidence/2026-11-12-700-i5/summary /tmp/i5-summary
```

The full campaign commands are in
[`scripts/campaign.sh`](evidence/2026-11-12-700-i5/scripts/campaign.sh) and
[`scripts/supplement.sh`](evidence/2026-11-12-700-i5/scripts/supplement.sh).

## Results

### Runner validation

| Run | Plan | Outcome |
| --- | --- | --- |
| success | both | All cells valid and correct |
| truncate | both | Valid and incorrect, goodput 0. Every response flagged `unverified:resp-len 32768!=65536`. |
| corrupt | both | Valid and incorrect, goodput 0. Every response flagged `unverified:resp-content`. |
| stall | both | Instrument-invalid (no attempt started inside the window). Canaries failed. |
| kill (SIGKILL after 3 s) | both | `jul exited during the run: signal killed`. Refused and reset errors recorded; the run is incorrect. |
| watchdog 5 s | pre-freeze | Did not fire, because the cell was shorter |
| watchdog 2 s | frozen | Instrument-invalid `watchdog: cell exceeded 2s`. 16 requests were unfinished and kept. |

Every run directory verified.

### Calibration (`base`, goodput rps / Jul CPU fraction of the envelope)

| Cell | C=1 | C=2 | C=4 | C=8 | C=16 | Chosen |
| --- | --- | --- | --- | --- | --- | --- |
| H1 none | 1532/0.33 | 3101/0.43 | 5374/0.52 | 6442/0.57 | 7482/0.61 | 16 |
| H1 waf | 10.4/0.50 | 18.8/0.98 | 18.9/0.99 | 18.6/0.99 | 17.7/0.99 | 1 |
| H1 wasm | 385/0.46 | 635/0.63 | 965/0.88 | 1048/0.94 | 1075/0.98 | 2 |
| H1 both | 10.0/0.50 | 18.3/0.98 | 18.6/0.99 | 18.5/0.99 | 17.2/0.99 (2×500) | 1 |
| H2 none | 1252/0.42 | 1990/0.52 | 2593/0.56 | 2834/0.58 | 2886/0.61 | 16 |
| H2 waf | 10.3/0.50 | 18.7/0.98 | 18.7/0.99 | 17.5/0.98 | 17.8/0.99 | 1 |
| H2 wasm | 356/0.49 | 563/0.63 | 786/0.83 | 932/0.94 | 925/0.98 | 2 |
| H2 both | 10.1/0.50 | 18.3/0.98 | 18.5/0.99 | 18.6/0.99 | 17.4/1.00 | 1 |
| H3 none | 855/0.38 | 1658/0.49 | 2459/0.57 | 2894/0.60 | 3070/0.61 | 16 |
| H3 both | 9.9/0.50 | 18.1/0.97 | 18.5/0.99 | 18.1/1.00 | 17.2/0.99 | 1 |

**What the calibration shows:**

- **WAF is serial per request.** One WAF request occupies about one of the two CPUs, so the
  inspected variants saturate at C=2. The #695 C=16 normal cells were saturated, not ordinary
  throughput.
- **Uninspected cells plateau below Jul's CPU limit.** They level off at about 0.6 of the
  envelope, so their bound lies partly outside Jul's CPU, most likely the client, origin or
  loopback. These cells are therefore less sensitive to Jul regressions.
- The rule was not changed. Both observations are carried into the limitations.

### Comparison campaign (`campaign-normal`, qualification, 3 repetitions)

All 90 runs were instrument-valid and correct. Medians are verified goodput in rps, with spread as
(max−min)/median ([full summary](evidence/2026-11-12-700-i5/summary/campaign-normal/summary.md)).

| Cell | base | base4 | main | base→main | base4→main | base→base4 |
| --- | --- | --- | --- | --- | --- | --- |
| u1 H1 none (C=16) | 7559.1 (0.002) | 7269.4 (0.033) | 6784.4 (0.106) | 10.2% **fail-regression** | 6.7% **inconclusive-noise** | 3.8% pass |
| u1 H1 waf (C=1) | 10.3 (0.024) | 10.4 (0.005) | 10.3 (0.005) | pass | pass | pass |
| u1 H1 wasm (C=2) | 631.7 (0.031) | 616.3 (0.019) | 628.3 (0.017) | pass | pass | pass |
| u1 H1 both (C=1) | 10.2 (0.020) | 10.1 (0.015) | 10.2 (0.015) | pass | pass | pass |
| u2 H2 none (C=16) | 2968.2 (0.018) | 2968.6 (0.005) | 2991.1 (0.022) | pass | pass | pass |
| u2 H2 waf | 10.3 (0.019) | 10.3 (0.024) | 10.3 (0.034) | pass | pass | pass |
| u2 H2 wasm | 577.0 (0.076) | 582.2 (0.023) | 583.7 (0.024) | pass | pass | pass |
| u2 H2 both | 10.1 (0.030) | 10.1 (0.035) | 10.1 (0.015) | pass | pass | pass |
| u10 H3 none (C=16) | 3303.2 (0.039) | 3279.5 (0.016) | 3306.8 (0.027) | pass | pass | pass |
| u10 H3 both (C=1) | 9.9 (0.010) | 9.9 (0.010) | 10.0 (0.020) | pass | pass | pass |

Every comparison cell ran at Jul CPU between 0.50 and 0.65 of the envelope: unsaturated, as
calibrated.

**Peak cgroup memory.**

| Cells | base (go1.27.3) | base4 and main (go1.27.4) |
| --- | --- | --- |
| `none` | about 24–27 MiB | about 40–45 MiB |
| inspected | — | about 10–30 MiB higher than base |

`base4` and `main` agree with each other. The step is therefore associated with the toolchain
rebuild, not with I1; its cause was not investigated.

**Investigating `u1/none`.**

- I1 adds no code to the uninspected request handler path. It changes reload and startup limit
  publication, configuration, admin and metrics, and registers one metrics collector.
- The H2 and H3 `none` cells show no comparable change.
- Following the rules, 6 supplementary interleaved repetitions were added for all three targets
  (`supplement-u1-none`).

| u1 H1 none, all 9 repetitions | base | base4 | main |
| --- | --- | --- | --- |
| median rps | 7331.7 | 7217.8 | 7189.9 |
| min–max | 7102.9–7576.2 | 6789.4–7459.8 | 6700.9–7554.5 |
| spread | 0.065 | 0.093 | 0.119 |

With the supplement, base→main is a 1.9% median regression and base4→main 0.4%, but both are
`inconclusive-noise` because main's spread is 0.119
([summary](evidence/2026-11-12-700-i5/summary/campaign-normal-with-supplement/summary.md)). The
first three base repetitions happened to fall at the top of base's range. The cell is recorded as
**inconclusive (noise)**, not as a pass. The original three-repetition `fail-regression` verdict
remains in the campaign summary. This cell is bound partly outside Jul's CPU, and I6 needs a more
stable configuration for it.

### Fidelity smokes (qualification, 1 repetition, `base` and `main`)

These validate the instruments and record observed behaviour. They carry no pass verdict
([summary](evidence/2026-11-12-700-i5/summary/fidelity/summary.md), with every class and probe).

| Scenario | Observed |
| --- | --- |
| r1 slow upload, `wasm`, C=16 | Correct on both. Slow class 3.2 rps; probe class about 2720 rps. |
| s8 slow clients, all variants | Slow, slow-cancel (320 client cancels each) and probe classes recorded. The `waf` and `both` probes ran at saturated CPU (0.99), 15–25 rps. `base` `both`: one `status:500`. |
| s11l argument amplification, C=1/2 | `none` about 2000/4300 rps. `waf` 5.0/9.0 rps, p99 about 225–270 ms (whole-run Jul CPU 0.52). Correct on both. |
| s9 reload churn, `both` | **OOM-killed on both binaries** (`oom_kill` delta 1 each; peak cgroup memory 669.5 MiB `base`, 768.2 MiB `main`). After the kill, requests fail with `error:refused` (millions, from the open-loop client). Before it, the resets and EOFs are recorded separately. |
| F3 admission, pending 16 and 0, `none` | Correct. One H2 connection. Pending 0 fails fast with 503 (about 100k); pending 16 queues (p99 13–17 s). The probe stayed at about 9.8 rps and p99 7–8 ms. |
| F3 admission, pending 16 and 0, `both` | **OOM-killed on both binaries.** 128 admission streams on one connection, each with a body inspected by WAF and plugins. |
| F4 stalled body, H1/H2/H3, `none` and `both` | Every stalled upload ran until the client's 20 s limit (`watchdog_fired=true`). **Neither runtime has an inspection body deadline**; the external watchdog is the only bound, as expected. Slow uploads completed in 7.6–8.1 s. Siblings completed in 1–6 ms; H2/H3 siblings shared the single measured connection (dials 1/1), and H1 used 2/2. |
| s6 static H2, `none`/`both` | `none` about 1380 rps on both. `both` (CPU 0.99): `status:500` ×10 on `base` and ×13 on `main`. |
| s7 cache H1, `none`/`both` | Cold/warm recorded separately. `both` (CPU 0.99): `status:500` ×2+2 on `base` and ×6+4 on `main`. Inspection runs before cache hits. `none` main was low (warm 4688 against 6012); see the investigation. |
| r2 fast no-body H1, `none`/`wasm` | Correct. `none` about 14,000 rps (CPU 0.70); `wasm` about 4280 rps (CPU 0.94). |

**s7 investigation.** Three interleaved repetitions of `s7-cache-h1/none` on all targets
(`investigate-s7-none`) show no main deficit:

| Target | Cold median (rps) | Warm median (rps) |
| --- | --- | --- |
| base | 2892 | 5625 |
| base4 | 2823 | 6136 |
| main | 2852 | 6060 |

The single low fidelity repetition was noise. s7 is a capacity scenario and has no comparison
verdict.

**Saturated `both` 500s.** These appear at about 0.99 CPU on both binaries: calibration H1 C=16,
s6, s7 and s8. With the corrected counters (`diagnose-s6-both`), plugin request errors plus plugin
response errors equal `jul_plugin_panics_total` exactly:

| Target | Request errors | Response errors | Panics |
| --- | --- | --- | --- |
| base | 5 | 7 | 12 |
| main | 4 | 9 | 13 |

The 500s are therefore contained guest traps, not WAF or origin failures. The plugin
per-invocation timeout defaults to 100 ms (`internal/plugins/runtime.go`). A timed-out
`fn.Call` is discarded as a trap and answered with 500
(`internal/plugins/set.go` `writePluginError`, `phase.go` `decide`). Jul logs neither the trap
nor its cause at warn level. The evidence fits invocation timeouts under CPU contention, but the
trap cause is not recorded, so that cause is inferred, not proven.

This is a pre-existing runtime behaviour present in both binaries. It is outside I5's scope. It is
relevant to I2, because guest calls must not fail through CPU contention that admission should
have prevented, and to the observability of contained traps.

## Harness defects found during the campaign

Both fixes are in `21cb2440`. No archived run, verdict or comparison is affected.

1. **Diagnostic counter label filters.**
   - The frozen harness filtered on label values that Jul never emits: WAF outcome `pass`, plugin
     result `ok`, cache `hit`/`miss`, and reload `success`/`failure`. Jul's actual values are
     `clean`, `continue/stop/error`, `HIT/MISS`, and `applied_live/not_applied/…`.
   - When the family existed, those keys read a false 0. They feed no verdict; they appear only
     in `counter_deltas`.
   - The `counter_deltas` keys `waf_tx_pass`, `plugin_invocations_ok`, `cache_hit`, `cache_miss`,
     `reload_success` and `reload_failure` in runs made with `30ff0650` must be ignored.
   - The fix uses the documented values and adds plugin error and trap counters. A new test
     checks every filter against Jul's metric Help enumeration; it fails on the old filters.
2. **Summaries showed only the primary class.**
   - Probe, slow, warm and later-phase classes, and F4 probes, existed only in the raw results.
   - Summaries now include a row for each repetition, phase and class, and an F4 table.
   - Earlier summaries were regenerated from the unchanged raw data.

A lint-only error-string change is also in `21cb2440`.

## Limitations

- **Single host.** One WSL2 arm64 machine, with one interleaved campaign per target. These are
  not capacity, SLA or soak figures, and the absolute numbers do not transfer to other hosts.
- **Calibrated on one binary.** The `none` cells are bound partly outside Jul's CPU, and the
  concurrencies were calibrated on `base` only.
- **Fidelity smokes** are single repetitions. Destructive profiles (s3, s4, full s11) were not
  run. The uncapped runtimes already OOM in s9 and F3 `both`, and need not be OOM-killed again for
  I5.
- **Memory measures are separate.** The cgroup peak is the scope's `memory.peak`. RSS, live heap
  after two GCs (admin pprof) and governor gauges are recorded separately in each result. The
  governor gauges are foundation-only: they stay at 0 because nothing charges them.
- **Not ported.** F1/F1b (I3), F4b (I2/I3) and the F3 `Retry-After` pacing mode. A8 needs a
  two-listener fixture that I6 adds.
- **No historical rerun.** The `453e6237` #695 runtime was not rerun. Its archived results stay
  historical and are not comparable with verified goodput.

## A1–A18 status

The README's [mapping](../../scripts/inspection-assessment/README.md#mapping-to-adr-0032-a1a18)
is the checked-in source. In summary:

- **Instrumentation ready.** A1–A7, A9, A10, A12, A13, A15 and A17 have maintained scenarios or
  measurements, exercised here.
- **Runtime tests required.** A8, A11, A14, A16 and A18 depend on unit, race or two-listener
  tests in I1–I4 and I6, not on this runner.
- **Acceptance still pending.** Every criterion, A1–A18, remains pending until I2–I4 implement it
  and I6 runs it in enforce mode.

## Handoff to I2 (plugin request integration and deadlines)

I2 should change the measured behaviour in the following ways and validate each with the
maintained scenarios.

1. **Body preparation.** Plugin request bodies are prepared before instance acquisition (#699).
   - The governed lease must exist before the body is read.
   - The body read must charge the lease.
   - Validate with r1 (slow class, probe isolation), s8 and `f3-*`.
2. **Grow-before-allocate.** `readBounded` and its successors must reserve from the lease before
   growing a buffer, never after.
   - Validate with s9 and F3 `both`, which OOM today. These must become bounded rejections
     without an OOM delta.
   - Supporting evidence: per-class peak cgroup memory and heap after two GCs (A2 and A15
     support).
3. **Fail-fast admission.** A request that cannot be granted must be refused quickly with 503 and
   `Retry-After`, not queued.
   - F3 pending 0 and 16 with `both` show today's behaviour: OOM.
   - The F3 probe class and the separate-connection probe measure sibling progress (A17).
4. **Sticky deadline ownership.** `body_timeout` (default 60s; `"0s"` disables it) must own the
   stalled-body deadline per stream.
   - F4 shows today's baseline: every stall ends at the client's 20 s limit, and siblings stay
     responsive.
   - After I2, the stalled upload must end at the runtime deadline: `watchdog_fired=false`,
     within the configured bound.
   - H2/H3 siblings must stay on the same connection and complete (A6, A7, A10).
5. **Guest replacement.** A trapped or discarded instance must release its charge, and its
   replacement must be charged.
   - The saturated `both` 500s are contained traps that discard instances. The I2 change should
     report them as `plugin_panics` and `plugin_invocations_error` deltas with no leaked charge.
   - Validate with s6, s7 and s8 `both` and the diagnostic counters.
6. **Regression guard.** u1, u2 and u10 (3 or more repetitions; ADR baseline, `base4` and the I2
   candidate) must not regress by more than 10%. The `u1/none` cell needs added repetitions or a
   more stable configuration (see above). s6, s7 and r2 `none` must show no lease activity (A5a).

## Verification of this record

- `sha256sum -c SHA256SUMS`, run in the evidence directory, passes. It covers every file
  in the evidence directory except itself.
- `ia verify` passes for all 21 run directories after they were copied into the repository.
- `regenerate-summaries.sh` with the `21cb2440` harness reproduces `summary/` byte for byte
  (`diff -r` is empty).
