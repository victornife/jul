# Inspection assessment summary

Plan `inspection-assessment-plan/v1`, profile `quick`. Runs: controlled-watchdog.

> an I5 result shows that the instrument captured a runtime; it is not A1-A18 acceptance. Throughput verdicts between the ADR baseline and post-I1 main are informational because I1 has no request-path enforcement

## Targets

| Label | Adapter | SHA-256 | Go | Revision | Modified |
| --- | --- | --- | --- | --- | --- |
| main | current | `db6c6378b782b25e41fdfdbaa139517440e7beec44349a5fc6ee8df57800d7c8` | go1.27.4 | `363d6cd4a9fef4c020b25e5c810b2bd34e481732` | false |

## Cells

Goodput counts verified responses only. Spread is (max − min) / median over every repetition.

| Scenario | Variant | Target | Rule | n | instrument-valid | correct | median rps | min | max | spread | CPU fractions | peak cgroup MiB |
| --- | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- | --- |
| u1-normal-h1 | none | main | throughput | 1 | 0 | 0 | 1891.3 | 1891.3 | 1891.3 | 0.000 | n/a | 35.5 |

## Phases and classes

One row per repetition, phase and workload class. Goodput counts verified responses that started and finished in the window; requests crossing in from warm-up and those outstanding at the window end are excluded from it.

| Scenario | Variant | Target | Run | Rep | Phase | Class | Proto | C | Attempts | Verified in window | Goodput rps | Crossed in | Outstanding | p99 ms | Outcomes |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| u1-normal-h1 | none | main | controlled-watchdog | 1 | load | load | h1 | 16 | 5690 | 5674 | 1891.3 | 16 | 0 | 8.4 | ok ×5674, unfinished ×16 |

## Failed, invalid or inconclusive runs

- controlled-watchdog u1-normal-h1/none/main rep1: instrument-invalid: watchdog: cell exceeded 2s
- controlled-watchdog u1-normal-h1/none/main rep1: runtime-finding: phase load class load: unfinished ×16
