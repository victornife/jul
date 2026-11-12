# Inspection assessment summary

Plan `inspection-assessment-plan/v3`, profile `qualification`. Runs: i2-p3-repeat.

> an I5 result shows that the instrument captured a runtime; it is not A1-A18 acceptance. Throughput verdicts between the ADR baseline and post-I1 main are informational because I1 has no request-path enforcement

## Targets

| Label | Adapter | SHA-256 | Go | Revision | Modified |
| --- | --- | --- | --- | --- | --- |
| cand | current | `3edee005bc08754283e77c8b6f1688204d24927c2662697f2f82a1eccbf54e73` | go1.27.4 | `` |  |
| main | current | `65a0f8b9d42909b048fe3fe00ecda7dbf17ae28320bdef28bd82a1ea2f0add37` | go1.27.4 | `` |  |

## Cells

Goodput counts verified responses only. Spread is (max − min) / median over every repetition.

| Scenario | Variant | Target | Rule | n | instrument-valid | correct | median rps | min | max | spread | CPU fractions | peak cgroup MiB |
| --- | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- | --- |
| p3-nobody-plugin-h1 | wasm | cand | capacity | 5 | 5 | 4 | 1206.5 | 830.6 | 1285.9 | 0.377 | 0.98 0.97 0.96 0.96 0.97 | 313.3 333.2 430.9 410.8 550.1 |
| p3-nobody-plugin-h1 | wasm | main | capacity | 5 | 5 | 4 | 1395.9 | 1134.2 | 1421.0 | 0.205 | 0.95 0.97 0.97 0.96 0.97 | 379.6 339.1 531.7 488.8 399.5 |

## Phases and classes

One row per repetition, phase and workload class. Goodput counts verified responses that started and finished in the window; requests crossing in from warm-up and those outstanding at the window end are excluded from it.

| Scenario | Variant | Target | Run | Rep | Phase | Class | Proto | C | Attempts | Verified in window | Goodput rps | Crossed in | Outstanding | p99 ms | Outcomes |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| p3-nobody-plugin-h1 | wasm | cand | i2-p3-repeat | 1 | load | load | h1 | 16 | 16629 | 16613 | 830.6 | 16 | 16 | 61.5 | ok ×16629 |
| p3-nobody-plugin-h1 | wasm | cand | i2-p3-repeat | 1 | load | load | h1 | 16 | 17225 | 17208 | 860.4 | 15 | 16 | 60.6 | ok ×17224, status:500 ×1 |
| p3-nobody-plugin-h1 | wasm | cand | i2-p3-repeat | 2 | load | load | h1 | 16 | 24147 | 24131 | 1206.5 | 16 | 16 | 45.4 | ok ×24147 |
| p3-nobody-plugin-h1 | wasm | cand | i2-p3-repeat | 2 | load | load | h1 | 16 | 24751 | 24735 | 1236.8 | 16 | 16 | 44.8 | ok ×24751 |
| p3-nobody-plugin-h1 | wasm | cand | i2-p3-repeat | 3 | load | load | h1 | 16 | 23728 | 23712 | 1185.6 | 15 | 16 | 41.8 | ok ×23728 |
| p3-nobody-plugin-h1 | wasm | cand | i2-p3-repeat | 3 | load | load | h1 | 16 | 24471 | 24455 | 1222.8 | 16 | 16 | 40.9 | ok ×24471 |
| p3-nobody-plugin-h1 | wasm | cand | i2-p3-repeat | 4 | load | load | h1 | 16 | 25734 | 25718 | 1285.9 | 16 | 16 | 44.6 | ok ×25734 |
| p3-nobody-plugin-h1 | wasm | cand | i2-p3-repeat | 4 | load | load | h1 | 16 | 26624 | 26608 | 1330.4 | 16 | 16 | 44.0 | ok ×26624 |
| p3-nobody-plugin-h1 | wasm | cand | i2-p3-repeat | 5 | load | load | h1 | 16 | 25613 | 25597 | 1279.8 | 16 | 16 | 42.5 | ok ×25613 |
| p3-nobody-plugin-h1 | wasm | cand | i2-p3-repeat | 5 | load | load | h1 | 16 | 26326 | 26310 | 1315.5 | 16 | 16 | 41.4 | ok ×26326 |
| p3-nobody-plugin-h1 | wasm | main | i2-p3-repeat | 1 | load | load | h1 | 16 | 28435 | 28419 | 1421.0 | 16 | 16 | 38.3 | ok ×28435 |
| p3-nobody-plugin-h1 | wasm | main | i2-p3-repeat | 1 | load | load | h1 | 16 | 29069 | 29053 | 1452.7 | 16 | 16 | 38.1 | ok ×29069 |
| p3-nobody-plugin-h1 | wasm | main | i2-p3-repeat | 2 | load | load | h1 | 16 | 24056 | 24040 | 1202.0 | 16 | 16 | 45.5 | ok ×24056 |
| p3-nobody-plugin-h1 | wasm | main | i2-p3-repeat | 2 | load | load | h1 | 16 | 24581 | 24565 | 1228.2 | 16 | 16 | 45.7 | ok ×24581 |
| p3-nobody-plugin-h1 | wasm | main | i2-p3-repeat | 3 | load | load | h1 | 16 | 22702 | 22685 | 1134.2 | 16 | 16 | 43.9 | ok ×22701, status:500 ×1 |
| p3-nobody-plugin-h1 | wasm | main | i2-p3-repeat | 3 | load | load | h1 | 16 | 23238 | 23222 | 1161.1 | 16 | 16 | 44.1 | ok ×23238 |
| p3-nobody-plugin-h1 | wasm | main | i2-p3-repeat | 4 | load | load | h1 | 16 | 28336 | 28320 | 1416.0 | 16 | 16 | 35.5 | ok ×28336 |
| p3-nobody-plugin-h1 | wasm | main | i2-p3-repeat | 4 | load | load | h1 | 16 | 29172 | 29156 | 1457.8 | 16 | 16 | 35.9 | ok ×29172 |
| p3-nobody-plugin-h1 | wasm | main | i2-p3-repeat | 5 | load | load | h1 | 16 | 27934 | 27918 | 1395.9 | 16 | 16 | 35.4 | ok ×27934 |
| p3-nobody-plugin-h1 | wasm | main | i2-p3-repeat | 5 | load | load | h1 | 16 | 28662 | 28646 | 1432.3 | 16 | 16 | 35.2 | ok ×28662 |

## Non-success outcome latency

Completed attempts started in the window, per outcome (for example `status:503` for overload), including those that finished after it.

| Scenario | Variant | Target | Run | Rep | Phase | Class | Outcome | n | p50 ms | p95 ms | p99 ms | max ms |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: |
| p3-nobody-plugin-h1 | wasm | cand | i2-p3-repeat | 1 | load | load | status:500 | 1 | 124.8 | 124.8 | 124.8 | 124.8 |
| p3-nobody-plugin-h1 | wasm | main | i2-p3-repeat | 3 | load | load | status:500 | 1 | 127.6 | 127.6 | 127.6 | 127.6 |

## Failed, invalid or inconclusive runs

- i2-p3-repeat p3-nobody-plugin-h1/wasm/cand rep1: runtime-finding: phase load class load: status:500 ×1
- i2-p3-repeat p3-nobody-plugin-h1/wasm/main rep3: runtime-finding: phase load class load: status:500 ×1
