# Inspection assessment summary

Plan `inspection-assessment-plan/v3`, profile `qualification`. Runs: i2-p1-none-repeat.

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
| p1-large-body-plugin-h1 | none | cand | record | 3 | 3 | 3 | 1858.2 | 1842.4 | 2256.3 | 0.223 | 0.97 0.96 0.96 | 56.0 56.0 54.8 |
| p1-large-body-plugin-h1 | none | main | record | 3 | 3 | 3 | 2331.7 | 1673.0 | 2751.7 | 0.463 | 0.95 0.96 0.96 | 59.7 56.4 57.7 |

## Phases and classes

One row per repetition, phase and workload class. Goodput counts verified responses that started and finished in the window; requests crossing in from warm-up and those outstanding at the window end are excluded from it.

| Scenario | Variant | Target | Run | Rep | Phase | Class | Proto | C | Attempts | Verified in window | Goodput rps | Crossed in | Outstanding | p99 ms | Outcomes |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| p1-large-body-plugin-h1 | none | cand | i2-p1-none-repeat | 1 | load | load | h1 | 64 | 37227 | 37163 | 1858.2 | 64 | 64 | 80.9 | ok ×37227 |
| p1-large-body-plugin-h1 | none | cand | i2-p1-none-repeat | 2 | load | load | h1 | 64 | 45190 | 45126 | 2256.3 | 64 | 64 | 69.5 | ok ×45190 |
| p1-large-body-plugin-h1 | none | cand | i2-p1-none-repeat | 3 | load | load | h1 | 64 | 36912 | 36848 | 1842.4 | 64 | 64 | 95.0 | ok ×36912 |
| p1-large-body-plugin-h1 | none | main | i2-p1-none-repeat | 1 | load | load | h1 | 64 | 55097 | 55033 | 2751.7 | 64 | 64 | 58.3 | ok ×55097 |
| p1-large-body-plugin-h1 | none | main | i2-p1-none-repeat | 2 | load | load | h1 | 64 | 33525 | 33461 | 1673.0 | 64 | 64 | 89.2 | ok ×33525 |
| p1-large-body-plugin-h1 | none | main | i2-p1-none-repeat | 3 | load | load | h1 | 64 | 46697 | 46633 | 2331.7 | 64 | 64 | 62.7 | ok ×46697 |

## Failed, invalid or inconclusive runs

None.
