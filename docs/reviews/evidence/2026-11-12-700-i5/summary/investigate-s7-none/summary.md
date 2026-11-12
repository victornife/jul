# Inspection assessment summary

Plan `inspection-assessment-plan/v1`, profile `qualification`. Runs: investigate-s7-none.

> an I5 result shows that the instrument captured a runtime; it is not A1-A18 acceptance. Throughput verdicts between the ADR baseline and post-I1 main are informational because I1 has no request-path enforcement

## Targets

| Label | Adapter | SHA-256 | Go | Revision | Modified |
| --- | --- | --- | --- | --- | --- |
| base | baseline-858590dc | `520febcbad5669da0ea8ad98e29f0bb78304817511cd7ed63bfb48e9d63ea52f` | go1.27.3 | `858590dc7c430d24cd33b096a81ca6051591b427` | false |
| base4 | baseline-858590dc | `e0f105cbae69939b5b91a5bbfd7165f993fcb181c4d813bfa241486ab3a2291b` | go1.27.4 | `858590dc7c430d24cd33b096a81ca6051591b427` | false |
| main | current | `db6c6378b782b25e41fdfdbaa139517440e7beec44349a5fc6ee8df57800d7c8` | go1.27.4 | `363d6cd4a9fef4c020b25e5c810b2bd34e481732` | false |

## Cells

Goodput counts verified responses only. Spread is (max − min) / median over every repetition.

| Scenario | Variant | Target | Rule | n | instrument-valid | correct | median rps | min | max | spread | CPU fractions | peak cgroup MiB |
| --- | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- | --- |
| s7-cache-h1 | none | base | capacity | 3 | 3 | 3 | 2891.6 | 2816.8 | 2947.9 | 0.045 | 0.69 0.70 0.68 | 209.9 217.6 206.7 |
| s7-cache-h1 | none | base4 | capacity | 3 | 3 | 3 | 2822.7 | 2806.3 | 2873.4 | 0.024 | 0.69 0.69 0.68 | 236.1 225.6 244.2 |
| s7-cache-h1 | none | main | capacity | 3 | 3 | 3 | 2852.1 | 2832.4 | 2950.9 | 0.042 | 0.69 0.69 0.69 | 226.6 223.2 225.0 |

## Phases and classes

One row per repetition, phase and workload class. Goodput counts verified responses that started and finished in the window; requests crossing in from warm-up and those outstanding at the window end are excluded from it.

| Scenario | Variant | Target | Run | Rep | Phase | Class | Proto | C | Attempts | Verified in window | Goodput rps | Crossed in | Outstanding | p99 ms | Outcomes |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| s7-cache-h1 | none | base | investigate-s7-none | 1 | cold | cold | h1 | 16 | 58974 | 58958 | 2947.9 | 14 | 16 | 17.2 | ok ×58974 |
| s7-cache-h1 | none | base | investigate-s7-none | 1 | warm | warm | h1 | 16 | 110973 | 110959 | 5547.9 | 14 | 14 | 8.4 | ok ×110973 |
| s7-cache-h1 | none | base | investigate-s7-none | 2 | cold | cold | h1 | 16 | 57847 | 57831 | 2891.6 | 16 | 16 | 16.9 | ok ×57847 |
| s7-cache-h1 | none | base | investigate-s7-none | 2 | warm | warm | h1 | 16 | 112517 | 112503 | 5625.1 | 15 | 14 | 9.5 | ok ×112517 |
| s7-cache-h1 | none | base | investigate-s7-none | 3 | cold | cold | h1 | 16 | 56353 | 56337 | 2816.8 | 15 | 16 | 17.3 | ok ×56353 |
| s7-cache-h1 | none | base | investigate-s7-none | 3 | warm | warm | h1 | 16 | 125307 | 125291 | 6264.6 | 16 | 16 | 7.7 | ok ×125307 |
| s7-cache-h1 | none | base4 | investigate-s7-none | 1 | cold | cold | h1 | 16 | 57485 | 57469 | 2873.4 | 16 | 16 | 17.6 | ok ×57485 |
| s7-cache-h1 | none | base4 | investigate-s7-none | 1 | warm | warm | h1 | 16 | 123202 | 123187 | 6159.4 | 15 | 15 | 7.9 | ok ×123202 |
| s7-cache-h1 | none | base4 | investigate-s7-none | 2 | cold | cold | h1 | 16 | 56468 | 56453 | 2822.7 | 16 | 15 | 17.0 | ok ×56468 |
| s7-cache-h1 | none | base4 | investigate-s7-none | 2 | warm | warm | h1 | 16 | 122737 | 122723 | 6136.1 | 14 | 14 | 7.8 | ok ×122737 |
| s7-cache-h1 | none | base4 | investigate-s7-none | 3 | cold | cold | h1 | 16 | 56142 | 56126 | 2806.3 | 13 | 16 | 17.5 | ok ×56142 |
| s7-cache-h1 | none | base4 | investigate-s7-none | 3 | warm | warm | h1 | 16 | 126413 | 126399 | 6319.9 | 13 | 14 | 7.7 | ok ×126413 |
| s7-cache-h1 | none | main | investigate-s7-none | 1 | cold | cold | h1 | 16 | 56664 | 56649 | 2832.4 | 16 | 15 | 17.4 | ok ×56664 |
| s7-cache-h1 | none | main | investigate-s7-none | 1 | warm | warm | h1 | 16 | 123703 | 123688 | 6184.4 | 14 | 15 | 7.8 | ok ×123703 |
| s7-cache-h1 | none | main | investigate-s7-none | 2 | cold | cold | h1 | 16 | 59032 | 59018 | 2950.9 | 16 | 14 | 17.0 | ok ×59032 |
| s7-cache-h1 | none | main | investigate-s7-none | 2 | warm | warm | h1 | 16 | 113188 | 113176 | 5658.8 | 14 | 12 | 8.3 | ok ×113188 |
| s7-cache-h1 | none | main | investigate-s7-none | 3 | cold | cold | h1 | 16 | 57058 | 57042 | 2852.1 | 16 | 16 | 17.0 | ok ×57058 |
| s7-cache-h1 | none | main | investigate-s7-none | 3 | warm | warm | h1 | 16 | 121210 | 121196 | 6059.8 | 15 | 14 | 7.9 | ok ×121210 |

## Failed, invalid or inconclusive runs

None.
