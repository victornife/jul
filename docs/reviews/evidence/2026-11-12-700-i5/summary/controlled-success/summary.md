# Inspection assessment summary

Plan `inspection-assessment-plan/v1`, profile `quick`. Runs: controlled-success.

> an I5 result shows that the instrument captured a runtime; it is not A1-A18 acceptance. Throughput verdicts between the ADR baseline and post-I1 main are informational because I1 has no request-path enforcement

## Targets

| Label | Adapter | SHA-256 | Go | Revision | Modified |
| --- | --- | --- | --- | --- | --- |
| main | current | `db6c6378b782b25e41fdfdbaa139517440e7beec44349a5fc6ee8df57800d7c8` | go1.27.4 | `363d6cd4a9fef4c020b25e5c810b2bd34e481732` | false |

## Cells

Goodput counts verified responses only. Spread is (max − min) / median over every repetition.

| Scenario | Variant | Target | Rule | n | instrument-valid | correct | median rps | min | max | spread | CPU fractions | peak cgroup MiB |
| --- | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- | --- |
| u1-normal-h1 | none | main | throughput | 1 | 1 | 1 | 6242.3 | 6242.3 | 6242.3 | 0.000 | 0.55 | 39.3 |
| u1-normal-h1 | wasm | main | throughput | 1 | 1 | 1 | 589.3 | 589.3 | 589.3 | 0.000 | 0.63 | 196.5 |
| u10-normal-h3 | none | main | throughput | 1 | 1 | 1 | 2945.7 | 2945.7 | 2945.7 | 0.000 | 0.59 | 39.2 |
| u2-normal-h2 | none | main | throughput | 1 | 1 | 1 | 2892.0 | 2892.0 | 2892.0 | 0.000 | 0.59 | 40.4 |
| u2-normal-h2 | wasm | main | throughput | 1 | 1 | 1 | 573.7 | 573.7 | 573.7 | 0.000 | 0.63 | 209.1 |

## Phases and classes

One row per repetition, phase and workload class. Goodput counts verified responses that started and finished in the window; requests crossing in from warm-up and those outstanding at the window end are excluded from it.

| Scenario | Variant | Target | Run | Rep | Phase | Class | Proto | C | Attempts | Verified in window | Goodput rps | Crossed in | Outstanding | p99 ms | Outcomes |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| u1-normal-h1 | none | main | controlled-success | 1 | load | load | h1 | 16 | 18743 | 18727 | 6242.3 | 14 | 16 | 9.4 | ok ×18743 |
| u1-normal-h1 | wasm | main | controlled-success | 1 | load | load | h1 | 2 | 1770 | 1768 | 589.3 | 2 | 2 | 10.3 | ok ×1770 |
| u10-normal-h3 | none | main | controlled-success | 1 | load | load | h3 | 16 | 8853 | 8837 | 2945.7 | 16 | 16 | 12.9 | ok ×8853 |
| u2-normal-h2 | none | main | controlled-success | 1 | load | load | h2 | 16 | 8692 | 8676 | 2892.0 | 15 | 16 | 12.0 | ok ×8692 |
| u2-normal-h2 | wasm | main | controlled-success | 1 | load | load | h2 | 2 | 1723 | 1721 | 573.7 | 2 | 2 | 9.4 | ok ×1723 |

## Failed, invalid or inconclusive runs

None.
