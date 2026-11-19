# #721 Linux qualification evidence

The [implementation ledger](../../700-inspection-implementation-ledger.md#linux-follow-up--2026-11-19)
records the results and limitations. A1 did not pass; this evidence does not
complete A1–A18/A21/A22. Original A19/A20 remain deferred.

The host was shared Linux/arm64 WSL2 with cgroup v2. The assessment harness
verified a 2-CPU, 768 MiB, zero-swap envelope and `GOMAXPROCS=2`; Jul, the
origin and the client were pinned to CPUs 0–1, 2–3 and 4–9 respectively.
The functional soak ran on CPUs 10–11 without that memory envelope and
overlapped the first two A1 repetitions. CPU pinning did not eliminate all
shared-host interference. Temporary worktree cleanup overlapped startup of the
first candidate s3 cell. The test admin token in generated configurations is a
fixed non-secret fixture; the referenced TLS key is the repository's test fixture.

The paired Jul binaries were built with Go 1.27.4, full tags, static linking
and matching `-trimpath` and strip/version flags. Their full identities,
configuration and harness hashes, and effective envelope are in the run
manifests. The candidate contains production commit `60415e5a`; `79d8797f`
only updates the Console asset inventory.

| Evidence | Contents |
| --- | --- |
| [preflight](preflight/) | Verified short envelope and binary-identity smoke, not a comparison or acceptance result. |
| [focused-cgroup-tests.txt](focused-cgroup-tests.txt) | Three repetitions of focused governor and WAF tests in the verified envelope. |
| [functional-soak.txt](functional-soak.txt) | Thirty-minute combined functional soak. |
| [a1-s11](a1-s11/) | Frozen-plan `s11/none` and `s11/waf` runs, including failing and baseline OOM-related observations. |
| [a1-summary](a1-summary/summary.md) and [a1-run.txt](a1-run.txt) | Generated summary and harness console output. |
| [a1-post-soak](a1-post-soak/) | Additional `s11/waf` repetitions comparing the pre-fix and corrected PR heads without the concurrent soak. |
| [a1-post-soak-summary](a1-post-soak-summary/summary.md) and [a1-post-soak-run.txt](a1-post-soak-run.txt) | Generated diagnostic summary and console output. |
| [a13-normal](a13-normal/) | Three-repetition baseline/candidate normal-traffic campaign. |
| [a13-summary](a13-summary/summary.md) and [a13-run.txt](a13-run.txt) | Informational comparison verdicts and harness console output, not A13 acceptance. |
| [a2-s3](a2-s3/) and [a2-s4](a2-s4/) | Large-body and ramp runs, including OOM-related and instrument-invalid cells. |
| [a2-summary](a2-summary/summary.md) and [a2-run.txt](a2-run.txt) | Generated combined summary and harness console output. |

From the repository root, with the matched binaries in `$B` and fresh output
paths, the A1 run used:

```sh
"$B/ia-clean" plan -check scripts/inspection-assessment/plan.json
taskset -c 4-9 "$B/ia-clean" run -out /tmp/ia-a1-s11 \
  -profile qualification -scenario s11-arg-amplification-h1 -variants none,waf \
  -reps 3 -target base:baseline-858590dc:$B/jul-base-go1274 \
  -target main:current:$B/jul-candidate
"$B/ia-clean" verify /tmp/ia-a1-s11
"$B/ia-clean" summarize -out /tmp/ia-a1-summary -compare base:main /tmp/ia-a1-s11
```

For the post-soak diagnostic, `$B/jul-before` was built from the clean pre-fix
head `ffbff4d0` using the same build flags and Go version. It still has the
rule-control fail-open and is not a replacement for the ADR baseline:

```sh
taskset -c 4-9 "$B/ia-clean" run -out /tmp/ia-a1-after-soak \
  -profile qualification -scenario s11-arg-amplification-h1 -variants waf \
  -reps 3 -target before:current:$B/jul-before -target main:current:$B/jul-candidate
"$B/ia-clean" verify /tmp/ia-a1-after-soak
"$B/ia-clean" summarize -out /tmp/ia-a1-after-soak-summary -compare before:main /tmp/ia-a1-after-soak
```

The normal-traffic campaign used the same target binaries and placement:

```sh
taskset -c 4-9 "$B/ia-clean" run -out /tmp/ia-a13-normal \
  -profile qualification -suite campaign-normal -reps 3 \
  -target base:baseline-858590dc:$B/jul-base-go1274 \
  -target main:current:$B/jul-candidate \
  -guest "$B/inspect-load.wasm" -guest-source examples/plugins/inspect-load \
  -guest-nobody "$B/inspect-nobody.wasm" -guest-nobody-source examples/plugins/inspect-nobody
"$B/ia-clean" verify /tmp/ia-a13-normal
"$B/ia-clean" summarize -out /tmp/ia-a13-summary -compare base:main /tmp/ia-a13-normal
```

For A2, the same binaries and guests ran the two frozen scenarios:

```sh
for scenario in s3-large-h2 s4-ramp-large-h2; do
  taskset -c 4-9 "$B/ia-clean" run -out "/tmp/ia-a2-$scenario" \
    -profile qualification -scenario "$scenario" -variants none,both -reps 3 \
    -target base:baseline-858590dc:$B/jul-base-go1274 \
    -target main:current:$B/jul-candidate \
    -guest "$B/inspect-load.wasm" -guest-source examples/plugins/inspect-load \
    -guest-nobody "$B/inspect-nobody.wasm" -guest-nobody-source examples/plugins/inspect-nobody
  "$B/ia-clean" verify "/tmp/ia-a2-$scenario"
done
"$B/ia-clean" summarize -out /tmp/ia-a2-summary -compare base:main \
  /tmp/ia-a2-s3-large-h2 /tmp/ia-a2-s4-ramp-large-h2
```

See the [assessment README](../../../../scripts/inspection-assessment/README.md)
for build and envelope prerequisites. Run `sha256sum -c SHA256SUMS` from this
directory, and `ia-clean verify` on each complete harness run, after checkout.
