# #700 I5 assessment tooling baselines — evidence

Raw evidence for
[the I5 tooling and baseline review](../../2026-11-12-700-i5-assessment-tooling-baselines.md).
It was recorded on 2026-11-12 with the maintained harness in
[`scripts/inspection-assessment/`](../../../../scripts/inspection-assessment/README.md).
Every run is kept, including failed, invalid and fault-injected runs. Later runs supplement
earlier ones and never replace them.

| Path | Content |
| --- | --- |
| `runs/pre-freeze-controlled/` | Runner validation before the plan was frozen. It used a development harness build from a dirty tree. |
| `runs/calibration/` | The calibration suite under the pre-freeze plan, with the same calibration scenario hashes as the frozen plan. |
| `calibration-decision.json` | The `calibrate` output that set the unsaturated normal concurrency. |
| `runs/controlled-*` | Runner validation under the frozen plan (harness `30ff0650`). |
| `runs/campaign-normal/` | The comparison campaign: 10 cells × 3 targets × 3 repetitions. |
| `runs/fidelity/` | The fidelity smokes, ADR baseline and post-I1 main, 1 repetition each. |
| `runs/supplement-u1-none/` | 6 more repetitions of the `u1-normal-h1/none` cell flagged by the campaign. |
| `runs/investigate-s7-none/` | 3 repetitions of `s7-cache-h1/none`, investigating a low single fidelity result. |
| `runs/diagnose-s6-both/` | `s6-static-h2/both` again with the corrected diagnostic counters (harness `21cb2440`). |
| `summary/` | Summaries regenerated from `runs/` by the committed harness. |
| `logs/` | The console logs of the scripts in `scripts/`. |
| `scripts/` | The exact scripts that produced the runs, and `regenerate-summaries.sh`. |

The scripts and manifests record the absolute paths of the measurement host (`/home/victornf/...`,
`/tmp/il.wasm`). Each manifest identifies binaries, the guest and the harness by sha256, so those
paths are provenance only. The admin token in the configurations is the harness's synthetic,
loopback-only constant.

## Verify and regenerate

From the repository root:

```sh
(cd docs/reviews/evidence/2026-11-12-700-i5 && sha256sum -c --quiet SHA256SUMS)
go build -o /tmp/ia ./scripts/inspection-assessment
docs/reviews/evidence/2026-11-12-700-i5/scripts/regenerate-summaries.sh /tmp/ia /tmp/i5-summary
diff -r docs/reviews/evidence/2026-11-12-700-i5/summary /tmp/i5-summary
```

`regenerate-summaries.sh` first runs `verify` on every run directory. `verify` checks each run's
own `SHA256SUMS`, manifest, plan hash and scenario hashes. `summarize` refuses fault-injected runs
because they are runner validation, so those runs are recorded only through their results and
`logs/`.

`SHA256SUMS` in this directory covers every file here except itself.
