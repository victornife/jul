# Inspection assessment summary

Plan `inspection-assessment-plan/v1`, profile `qualification`. Runs: diagnose-s6-both.

> an I5 result shows that the instrument captured a runtime; it is not A1-A18 acceptance. Throughput verdicts between the ADR baseline and post-I1 main are informational because I1 has no request-path enforcement

## Targets

| Label | Adapter | SHA-256 | Go | Revision | Modified |
| --- | --- | --- | --- | --- | --- |
| base | baseline-858590dc | `520febcbad5669da0ea8ad98e29f0bb78304817511cd7ed63bfb48e9d63ea52f` | go1.27.3 | `858590dc7c430d24cd33b096a81ca6051591b427` | false |
| main | current | `db6c6378b782b25e41fdfdbaa139517440e7beec44349a5fc6ee8df57800d7c8` | go1.27.4 | `363d6cd4a9fef4c020b25e5c810b2bd34e481732` | false |

## Cells

Goodput counts verified responses only. Spread is (max − min) / median over every repetition.

| Scenario | Variant | Target | Rule | n | instrument-valid | correct | median rps | min | max | spread | CPU fractions | peak cgroup MiB |
| --- | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- | --- |
| s6-static-h2 | both | base | capacity | 1 | 1 | 0 | 13.7 | 13.7 | 13.7 | 0.000 | 0.99 | 475.7 |
| s6-static-h2 | both | main | capacity | 1 | 1 | 0 | 13.7 | 13.7 | 13.7 | 0.000 | 1.00 | 538.1 |

## Phases and classes

One row per repetition, phase and workload class. Goodput counts verified responses that started and finished in the window; requests crossing in from warm-up and those outstanding at the window end are excluded from it.

| Scenario | Variant | Target | Run | Rep | Phase | Class | Proto | C | Attempts | Verified in window | Goodput rps | Crossed in | Outstanding | p99 ms | Outcomes |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| s6-static-h2 | both | base | diagnose-s6-both | 1 | load | load | h2 | 128 | 407 | 274 | 13.7 | 128 | 128 | 8436.2 | ok ×402, status:500 ×5 |
| s6-static-h2 | both | main | diagnose-s6-both | 1 | load | load | h2 | 128 | 411 | 274 | 13.7 | 128 | 128 | 11166.4 | ok ×401, status:500 ×10 |

## Failed, invalid or inconclusive runs

- diagnose-s6-both s6-static-h2/both/base rep1: runtime-finding: phase load class load: status:500 ×5
- diagnose-s6-both s6-static-h2/both/main rep1: runtime-finding: phase load class load: status:500 ×10
