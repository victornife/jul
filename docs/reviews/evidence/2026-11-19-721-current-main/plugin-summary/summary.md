# Inspection assessment summary

Plan `inspection-assessment-plan/v4`, profile `qualification`. Runs: plugin-sized.

> an I5 result shows that the instrument captured a runtime; it is not A1-A18 acceptance. Throughput verdicts between the ADR baseline and post-I1 main are informational because I1 has no request-path enforcement

## Targets

| Label | Adapter | SHA-256 | Go | Revision | Modified |
| --- | --- | --- | --- | --- | --- |
| candidate | current | `c96daf7a91d04f46f99148dd21859f62f6576a45558b255748fe67184090a902` | go1.27.4 | `` |  |
| main | current | `cbc506c19b48a910342e4c4a07f6e3919f6ff524eeaff384a9b1ce95274da609` | go1.27.4 | `` |  |

## Cells

Goodput counts verified responses only. Spread is (max − min) / median over every repetition.

| Scenario | Variant | Target | Rule | n | instrument-valid | correct | median rps | min | max | spread | CPU fractions | peak cgroup MiB |
| --- | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- | --- |
| p1-large-body-plugin-h1 | none | candidate | record | 1 | 1 | 1 | 2262.3 | 2262.3 | 2262.3 | 0.000 | 0.96 | 50.2 |
| p1-large-body-plugin-h1 | none | main | record | 1 | 1 | 1 | 2433.9 | 2433.9 | 2433.9 | 0.000 | 0.95 | 55.2 |
| p1-large-body-plugin-h1 | wasm | candidate | record | 1 | 1 | 0 | 146.3 | 146.3 | 146.3 | 0.000 | 0.96 | 509.5 |
| p1-large-body-plugin-h1 | wasm | main | record | 1 | 1 | 0 | 126.3 | 126.3 | 126.3 | 0.000 | 0.98 | 487.5 |
| p2-saturation-plugin-h1 | wasm | candidate | record | 1 | 1 | 1 | 19.9 | 19.9 | 19.9 | 0.000 | 0.71 | 524.9 |
| p2-saturation-plugin-h1 | wasm | main | record | 1 | 1 | 1 | 19.9 | 19.9 | 19.9 | 0.000 | 0.67 | 540.1 |
| p2o-saturation-plugin-observe-h1 | wasm | candidate | record | 1 | 1 | 1 | 24.0 | 24.0 | 24.0 | 0.000 | 0.73 | 510.3 |
| p2o-saturation-plugin-observe-h1 | wasm | main | record | 1 | 1 | 1 | 24.0 | 24.0 | 24.0 | 0.000 | 0.71 | 506.1 |

## Phases and classes

One row per repetition, phase and workload class. Goodput counts verified responses that started and finished in the window; requests crossing in from warm-up and those outstanding at the window end are excluded from it.

| Scenario | Variant | Target | Run | Rep | Phase | Class | Proto | C | Attempts | Verified in window | Goodput rps | Crossed in | Outstanding | p99 ms | Outcomes |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| p1-large-body-plugin-h1 | none | candidate | plugin-sized | 1 | load | load | h1 | 64 | 45311 | 45247 | 2262.3 | 64 | 64 | 67.3 | ok ×45311 |
| p1-large-body-plugin-h1 | none | main | plugin-sized | 1 | load | load | h1 | 64 | 48743 | 48679 | 2433.9 | 64 | 64 | 78.8 | ok ×48743 |
| p1-large-body-plugin-h1 | wasm | candidate | plugin-sized | 1 | load | load | h1 | 64 | 3008 | 2927 | 146.3 | 64 | 64 | 677.6 | ok ×2990, status:500 ×18 |
| p1-large-body-plugin-h1 | wasm | main | plugin-sized | 1 | load | load | h1 | 64 | 2607 | 2527 | 126.3 | 64 | 64 | 685.8 | ok ×2591, status:500 ×16 |
| p2-saturation-plugin-h1 | wasm | candidate | plugin-sized | 1 | load | load | h1 | 160 | 1070 | 399 | 19.9 | 133 | 133 | 5402.6 | ok ×532, status:503-inspection-overloaded ×538 |
| p2-saturation-plugin-h1 | wasm | candidate | plugin-sized | 1 | load | probe | h1 | 2 | 22073 | 22071 | 1103.5 | 2 | 2 | 19.4 | ok ×22073 |
| p2-saturation-plugin-h1 | wasm | main | plugin-sized | 1 | load | load | h1 | 160 | 1046 | 399 | 19.9 | 133 | 133 | 5750.7 | ok ×532, status:503-inspection-overloaded ×514 |
| p2-saturation-plugin-h1 | wasm | main | plugin-sized | 1 | load | probe | h1 | 2 | 35800 | 35798 | 1789.9 | 2 | 2 | 5.2 | ok ×35800 |
| p2o-saturation-plugin-observe-h1 | wasm | candidate | plugin-sized | 1 | load | load | h1 | 160 | 640 | 480 | 24.0 | 160 | 160 | 5410.2 | ok ×640 |
| p2o-saturation-plugin-observe-h1 | wasm | candidate | plugin-sized | 1 | load | probe | h1 | 2 | 20839 | 20837 | 1041.8 | 2 | 2 | 20.6 | ok ×20839 |
| p2o-saturation-plugin-observe-h1 | wasm | main | plugin-sized | 1 | load | load | h1 | 160 | 640 | 480 | 24.0 | 160 | 160 | 5193.7 | ok ×640 |
| p2o-saturation-plugin-observe-h1 | wasm | main | plugin-sized | 1 | load | probe | h1 | 2 | 27270 | 27268 | 1363.4 | 2 | 2 | 17.4 | ok ×27270 |

## Non-success outcome latency

Completed attempts started in the window, per outcome (for example `status:503` for overload), including those that finished after it.

| Scenario | Variant | Target | Run | Rep | Phase | Class | Outcome | n | p50 ms | p95 ms | p99 ms | max ms |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: |
| p1-large-body-plugin-h1 | wasm | candidate | plugin-sized | 1 | load | load | status:500 | 18 | 262.5 | 376.7 | 376.7 | 397.4 |
| p1-large-body-plugin-h1 | wasm | main | plugin-sized | 1 | load | load | status:500 | 16 | 413.7 | 485.9 | 485.9 | 501.9 |
| p2-saturation-plugin-h1 | wasm | candidate | plugin-sized | 1 | load | load | status:503-inspection-overloaded | 538 | 2.5 | 82.1 | 176.4 | 177.0 |
| p2-saturation-plugin-h1 | wasm | main | plugin-sized | 1 | load | load | status:503-inspection-overloaded | 514 | 1.9 | 170.4 | 192.4 | 222.3 |

## Failed, invalid or inconclusive runs

- plugin-sized p1-large-body-plugin-h1/wasm/candidate rep1: runtime-finding: phase load class load: status:500 ×18
- plugin-sized p1-large-body-plugin-h1/wasm/main rep1: runtime-finding: phase load class load: status:500 ×16
