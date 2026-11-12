# Inspection assessment summary

Plan `inspection-assessment-plan/v3`, profile `qualification`. Runs: i2-deadline.

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
| f4-body-deadline-5s | both | cand | record | 1 | 1 | 1 | 0.0 | 0.0 | 0.0 | n/a | n/a | 178.5 |
| f4-body-deadline-5s | both | main | record | 1 | 1 | 0 | 0.0 | 0.0 | 0.0 | n/a | n/a | 177.3 |
| f4-body-deadline-5s | none | cand | record | 1 | 1 | 1 | 0.0 | 0.0 | 0.0 | n/a | n/a | 20.0 |
| f4-body-deadline-5s | none | main | record | 1 | 1 | 1 | 0.0 | 0.0 | 0.0 | n/a | n/a | 20.6 |
| f4-body-deadline-5s | wasm | cand | record | 1 | 1 | 1 | 0.0 | 0.0 | 0.0 | n/a | n/a | 95.1 |
| f4-body-deadline-5s | wasm | main | record | 1 | 1 | 0 | 0.0 | 0.0 | 0.0 | n/a | n/a | 103.3 |
| f4-stalled-body | both | cand | record | 1 | 1 | 1 | 0.0 | 0.0 | 0.0 | n/a | n/a | 186.2 |
| f4-stalled-body | both | main | record | 1 | 1 | 1 | 0.0 | 0.0 | 0.0 | n/a | n/a | 189.0 |
| f4-stalled-body | none | cand | record | 1 | 1 | 1 | 0.0 | 0.0 | 0.0 | n/a | n/a | 20.9 |
| f4-stalled-body | none | main | record | 1 | 1 | 1 | 0.0 | 0.0 | 0.0 | n/a | n/a | 20.4 |
| f4-stalled-body | wasm | cand | record | 1 | 1 | 1 | 0.0 | 0.0 | 0.0 | n/a | n/a | 110.6 |
| f4-stalled-body | wasm | main | record | 1 | 1 | 1 | 0.0 | 0.0 | 0.0 | n/a | n/a | 102.3 |

## Phases and classes

One row per repetition, phase and workload class. Goodput counts verified responses that started and finished in the window; requests crossing in from warm-up and those outstanding at the window end are excluded from it.

| Scenario | Variant | Target | Run | Rep | Phase | Class | Proto | C | Attempts | Verified in window | Goodput rps | Crossed in | Outstanding | p99 ms | Outcomes |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |

## F4 probes

| Scenario | Variant | Target | Run | Rep | Proto | Mode | Upload | Upload s | Client watchdog ended it | Sibling | Sibling ms | Same connection | Dials/expected |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | --- | --- | ---: | --- | --- |
| f4-body-deadline-5s | both | cand | i2-deadline | 1 | h1 | stall | status:408 | 5.0 | false | ok | 6 | false | 2/2 |
| f4-body-deadline-5s | both | cand | i2-deadline | 1 | h2 | stall | status:408 | 5.0 | false | ok | 6 | true | 1/1 |
| f4-body-deadline-5s | both | cand | i2-deadline | 1 | h3 | stall | status:408 | 5.0 | false | ok | 7 | true | 1/1 |
| f4-body-deadline-5s | both | main | i2-deadline | 1 | h1 | stall | error:timeout | 20.0 | true | ok | 11 | false | 2/2 |
| f4-body-deadline-5s | both | main | i2-deadline | 1 | h2 | stall | error:timeout | 20.0 | true | ok | 7 | true | 1/1 |
| f4-body-deadline-5s | both | main | i2-deadline | 1 | h3 | stall | error:timeout | 20.0 | true | ok | 6 | true | 1/1 |
| f4-body-deadline-5s | none | cand | i2-deadline | 1 | h1 | stall | error:other | 20.0 | true | ok | 4 | false | 2/2 |
| f4-body-deadline-5s | none | cand | i2-deadline | 1 | h2 | stall | error:timeout | 20.0 | true | ok | 3 | true | 1/1 |
| f4-body-deadline-5s | none | cand | i2-deadline | 1 | h3 | stall | error:timeout | 20.0 | true | ok | 3 | true | 1/1 |
| f4-body-deadline-5s | none | main | i2-deadline | 1 | h1 | stall | error:timeout | 20.0 | true | ok | 2 | false | 2/2 |
| f4-body-deadline-5s | none | main | i2-deadline | 1 | h2 | stall | error:timeout | 20.0 | true | ok | 2 | true | 1/1 |
| f4-body-deadline-5s | none | main | i2-deadline | 1 | h3 | stall | error:timeout | 20.0 | true | ok | 3 | true | 1/1 |
| f4-body-deadline-5s | wasm | cand | i2-deadline | 1 | h1 | stall | status:408 | 5.0 | false | ok | 4 | false | 2/2 |
| f4-body-deadline-5s | wasm | cand | i2-deadline | 1 | h2 | stall | status:408 | 5.0 | false | ok | 3 | true | 1/1 |
| f4-body-deadline-5s | wasm | cand | i2-deadline | 1 | h3 | stall | status:408 | 5.0 | false | ok | 4 | true | 1/1 |
| f4-body-deadline-5s | wasm | main | i2-deadline | 1 | h1 | stall | error:timeout | 20.0 | true | ok | 3 | false | 2/2 |
| f4-body-deadline-5s | wasm | main | i2-deadline | 1 | h2 | stall | error:timeout | 20.0 | true | ok | 4 | true | 1/1 |
| f4-body-deadline-5s | wasm | main | i2-deadline | 1 | h3 | stall | error:timeout | 20.0 | true | ok | 5 | true | 1/1 |
| f4-stalled-body | both | cand | i2-deadline | 1 | h1 | stall | error:timeout | 20.0 | true | ok | 8 | false | 2/2 |
| f4-stalled-body | both | cand | i2-deadline | 1 | h1 | slow | ok | 7.7 | false | ok | 11 | false | 2/2 |
| f4-stalled-body | both | cand | i2-deadline | 1 | h2 | stall | error:timeout | 20.0 | true | ok | 7 | true | 1/1 |
| f4-stalled-body | both | cand | i2-deadline | 1 | h2 | slow | ok | 8.1 | false | ok | 6 | true | 1/1 |
| f4-stalled-body | both | cand | i2-deadline | 1 | h3 | stall | error:timeout | 20.0 | true | ok | 6 | true | 1/1 |
| f4-stalled-body | both | cand | i2-deadline | 1 | h3 | slow | ok | 8.1 | false | ok | 6 | true | 1/1 |
| f4-stalled-body | both | main | i2-deadline | 1 | h1 | stall | error:timeout | 20.0 | true | ok | 8 | false | 2/2 |
| f4-stalled-body | both | main | i2-deadline | 1 | h1 | slow | ok | 7.6 | false | ok | 7 | false | 2/2 |
| f4-stalled-body | both | main | i2-deadline | 1 | h2 | stall | error:timeout | 20.0 | true | ok | 6 | true | 1/1 |
| f4-stalled-body | both | main | i2-deadline | 1 | h2 | slow | ok | 8.1 | false | ok | 10 | true | 1/1 |
| f4-stalled-body | both | main | i2-deadline | 1 | h3 | stall | error:timeout | 20.0 | true | ok | 8 | true | 1/1 |
| f4-stalled-body | both | main | i2-deadline | 1 | h3 | slow | ok | 8.1 | false | ok | 8 | true | 1/1 |
| f4-stalled-body | none | cand | i2-deadline | 1 | h1 | stall | error:timeout | 20.0 | true | ok | 2 | false | 2/2 |
| f4-stalled-body | none | cand | i2-deadline | 1 | h1 | slow | ok | 7.6 | false | ok | 1 | false | 2/2 |
| f4-stalled-body | none | cand | i2-deadline | 1 | h2 | stall | error:timeout | 20.0 | true | ok | 2 | true | 1/1 |
| f4-stalled-body | none | cand | i2-deadline | 1 | h2 | slow | ok | 8.0 | false | ok | 2 | true | 1/1 |
| f4-stalled-body | none | cand | i2-deadline | 1 | h3 | stall | error:timeout | 20.0 | true | ok | 2 | true | 1/1 |
| f4-stalled-body | none | cand | i2-deadline | 1 | h3 | slow | ok | 7.6 | false | ok | 2 | true | 1/1 |
| f4-stalled-body | none | main | i2-deadline | 1 | h1 | stall | error:timeout | 20.0 | true | ok | 2 | false | 2/2 |
| f4-stalled-body | none | main | i2-deadline | 1 | h1 | slow | ok | 7.6 | false | ok | 2 | false | 2/2 |
| f4-stalled-body | none | main | i2-deadline | 1 | h2 | stall | error:timeout | 20.0 | true | ok | 2 | true | 1/1 |
| f4-stalled-body | none | main | i2-deadline | 1 | h2 | slow | ok | 8.0 | false | ok | 2 | true | 1/1 |
| f4-stalled-body | none | main | i2-deadline | 1 | h3 | stall | error:timeout | 20.0 | true | ok | 3 | true | 1/1 |
| f4-stalled-body | none | main | i2-deadline | 1 | h3 | slow | ok | 7.6 | false | ok | 2 | true | 1/1 |
| f4-stalled-body | wasm | cand | i2-deadline | 1 | h1 | stall | error:timeout | 20.0 | true | ok | 3 | false | 2/2 |
| f4-stalled-body | wasm | cand | i2-deadline | 1 | h1 | slow | ok | 7.6 | false | ok | 5 | false | 2/2 |
| f4-stalled-body | wasm | cand | i2-deadline | 1 | h2 | stall | error:timeout | 20.0 | true | ok | 3 | true | 1/1 |
| f4-stalled-body | wasm | cand | i2-deadline | 1 | h2 | slow | ok | 8.0 | false | ok | 7 | true | 1/1 |
| f4-stalled-body | wasm | cand | i2-deadline | 1 | h3 | stall | error:timeout | 20.0 | true | ok | 4 | true | 1/1 |
| f4-stalled-body | wasm | cand | i2-deadline | 1 | h3 | slow | ok | 8.0 | false | ok | 4 | true | 1/1 |
| f4-stalled-body | wasm | main | i2-deadline | 1 | h1 | stall | error:timeout | 20.0 | true | ok | 7 | false | 2/2 |
| f4-stalled-body | wasm | main | i2-deadline | 1 | h1 | slow | ok | 7.6 | false | ok | 3 | false | 2/2 |
| f4-stalled-body | wasm | main | i2-deadline | 1 | h2 | stall | error:timeout | 20.0 | true | ok | 3 | true | 1/1 |
| f4-stalled-body | wasm | main | i2-deadline | 1 | h2 | slow | ok | 8.0 | false | ok | 8 | true | 1/1 |
| f4-stalled-body | wasm | main | i2-deadline | 1 | h3 | stall | error:timeout | 20.0 | true | ok | 4 | true | 1/1 |
| f4-stalled-body | wasm | main | i2-deadline | 1 | h3 | slow | ok | 8.0 | false | ok | 4 | true | 1/1 |

## F4 upload outcomes

Plan v2 verifies the upload response. A mismatch with the expectation is a runtime finding listed below.

| Scenario | Variant | Target | Run | Rep | Proto | Mode | Expected | Outcome | Upload s |
| --- | --- | --- | --- | ---: | --- | --- | --- | --- | ---: |
| f4-body-deadline-5s | both | cand | i2-deadline | 1 | h1 | stall | body-timeout-408 | runtime-ended:status:408 | 5.0 |
| f4-body-deadline-5s | both | cand | i2-deadline | 1 | h2 | stall | body-timeout-408 | runtime-ended:status:408 | 5.0 |
| f4-body-deadline-5s | both | cand | i2-deadline | 1 | h3 | stall | body-timeout-408 | runtime-ended:status:408 | 5.0 |
| f4-body-deadline-5s | both | main | i2-deadline | 1 | h1 | stall | body-timeout-408 | client-watchdog | 20.0 |
| f4-body-deadline-5s | both | main | i2-deadline | 1 | h2 | stall | body-timeout-408 | client-watchdog | 20.0 |
| f4-body-deadline-5s | both | main | i2-deadline | 1 | h3 | stall | body-timeout-408 | client-watchdog | 20.0 |
| f4-body-deadline-5s | none | cand | i2-deadline | 1 | h1 | stall | no-inspection-deadline | client-watchdog | 20.0 |
| f4-body-deadline-5s | none | cand | i2-deadline | 1 | h2 | stall | no-inspection-deadline | client-watchdog | 20.0 |
| f4-body-deadline-5s | none | cand | i2-deadline | 1 | h3 | stall | no-inspection-deadline | client-watchdog | 20.0 |
| f4-body-deadline-5s | none | main | i2-deadline | 1 | h1 | stall | no-inspection-deadline | client-watchdog | 20.0 |
| f4-body-deadline-5s | none | main | i2-deadline | 1 | h2 | stall | no-inspection-deadline | client-watchdog | 20.0 |
| f4-body-deadline-5s | none | main | i2-deadline | 1 | h3 | stall | no-inspection-deadline | client-watchdog | 20.0 |
| f4-body-deadline-5s | wasm | cand | i2-deadline | 1 | h1 | stall | body-timeout-408 | runtime-ended:status:408 | 5.0 |
| f4-body-deadline-5s | wasm | cand | i2-deadline | 1 | h2 | stall | body-timeout-408 | runtime-ended:status:408 | 5.0 |
| f4-body-deadline-5s | wasm | cand | i2-deadline | 1 | h3 | stall | body-timeout-408 | runtime-ended:status:408 | 5.0 |
| f4-body-deadline-5s | wasm | main | i2-deadline | 1 | h1 | stall | body-timeout-408 | client-watchdog | 20.0 |
| f4-body-deadline-5s | wasm | main | i2-deadline | 1 | h2 | stall | body-timeout-408 | client-watchdog | 20.0 |
| f4-body-deadline-5s | wasm | main | i2-deadline | 1 | h3 | stall | body-timeout-408 | client-watchdog | 20.0 |
| f4-stalled-body | both | cand | i2-deadline | 1 | h1 | stall | no-inspection-deadline | client-watchdog | 20.0 |
| f4-stalled-body | both | cand | i2-deadline | 1 | h1 | slow | verified | verified | 7.7 |
| f4-stalled-body | both | cand | i2-deadline | 1 | h2 | stall | no-inspection-deadline | client-watchdog | 20.0 |
| f4-stalled-body | both | cand | i2-deadline | 1 | h2 | slow | verified | verified | 8.1 |
| f4-stalled-body | both | cand | i2-deadline | 1 | h3 | stall | no-inspection-deadline | client-watchdog | 20.0 |
| f4-stalled-body | both | cand | i2-deadline | 1 | h3 | slow | verified | verified | 8.1 |
| f4-stalled-body | both | main | i2-deadline | 1 | h1 | stall | no-inspection-deadline | client-watchdog | 20.0 |
| f4-stalled-body | both | main | i2-deadline | 1 | h1 | slow | verified | verified | 7.6 |
| f4-stalled-body | both | main | i2-deadline | 1 | h2 | stall | no-inspection-deadline | client-watchdog | 20.0 |
| f4-stalled-body | both | main | i2-deadline | 1 | h2 | slow | verified | verified | 8.1 |
| f4-stalled-body | both | main | i2-deadline | 1 | h3 | stall | no-inspection-deadline | client-watchdog | 20.0 |
| f4-stalled-body | both | main | i2-deadline | 1 | h3 | slow | verified | verified | 8.1 |
| f4-stalled-body | none | cand | i2-deadline | 1 | h1 | stall | no-inspection-deadline | client-watchdog | 20.0 |
| f4-stalled-body | none | cand | i2-deadline | 1 | h1 | slow | verified | verified | 7.6 |
| f4-stalled-body | none | cand | i2-deadline | 1 | h2 | stall | no-inspection-deadline | client-watchdog | 20.0 |
| f4-stalled-body | none | cand | i2-deadline | 1 | h2 | slow | verified | verified | 8.0 |
| f4-stalled-body | none | cand | i2-deadline | 1 | h3 | stall | no-inspection-deadline | client-watchdog | 20.0 |
| f4-stalled-body | none | cand | i2-deadline | 1 | h3 | slow | verified | verified | 7.6 |
| f4-stalled-body | none | main | i2-deadline | 1 | h1 | stall | no-inspection-deadline | client-watchdog | 20.0 |
| f4-stalled-body | none | main | i2-deadline | 1 | h1 | slow | verified | verified | 7.6 |
| f4-stalled-body | none | main | i2-deadline | 1 | h2 | stall | no-inspection-deadline | client-watchdog | 20.0 |
| f4-stalled-body | none | main | i2-deadline | 1 | h2 | slow | verified | verified | 8.0 |
| f4-stalled-body | none | main | i2-deadline | 1 | h3 | stall | no-inspection-deadline | client-watchdog | 20.0 |
| f4-stalled-body | none | main | i2-deadline | 1 | h3 | slow | verified | verified | 7.6 |
| f4-stalled-body | wasm | cand | i2-deadline | 1 | h1 | stall | no-inspection-deadline | client-watchdog | 20.0 |
| f4-stalled-body | wasm | cand | i2-deadline | 1 | h1 | slow | verified | verified | 7.6 |
| f4-stalled-body | wasm | cand | i2-deadline | 1 | h2 | stall | no-inspection-deadline | client-watchdog | 20.0 |
| f4-stalled-body | wasm | cand | i2-deadline | 1 | h2 | slow | verified | verified | 8.0 |
| f4-stalled-body | wasm | cand | i2-deadline | 1 | h3 | stall | no-inspection-deadline | client-watchdog | 20.0 |
| f4-stalled-body | wasm | cand | i2-deadline | 1 | h3 | slow | verified | verified | 8.0 |
| f4-stalled-body | wasm | main | i2-deadline | 1 | h1 | stall | no-inspection-deadline | client-watchdog | 20.0 |
| f4-stalled-body | wasm | main | i2-deadline | 1 | h1 | slow | verified | verified | 7.6 |
| f4-stalled-body | wasm | main | i2-deadline | 1 | h2 | stall | no-inspection-deadline | client-watchdog | 20.0 |
| f4-stalled-body | wasm | main | i2-deadline | 1 | h2 | slow | verified | verified | 8.0 |
| f4-stalled-body | wasm | main | i2-deadline | 1 | h3 | stall | no-inspection-deadline | client-watchdog | 20.0 |
| f4-stalled-body | wasm | main | i2-deadline | 1 | h3 | slow | verified | verified | 8.0 |

## Failed, invalid or inconclusive runs

- i2-deadline f4-body-deadline-5s/both/main rep1: runtime-finding: f4 h1/stall: upload error:timeout after 20.0 s (client-watchdog): expected 408 from body_timeout 5 s within 4.5–8.0 s; f4 h2/stall: upload error:timeout after 20.0 s (client-watchdog): expected 408 from body_timeout 5 s within 4.5–8.0 s; f4 h3/stall: upload error:timeout after 20.0 s (client-watchdog): expected 408 from body_timeout 5 s within 4.5–8.0 s
- i2-deadline f4-body-deadline-5s/wasm/main rep1: runtime-finding: f4 h1/stall: upload error:timeout after 20.0 s (client-watchdog): expected 408 from body_timeout 5 s within 4.5–8.0 s; f4 h2/stall: upload error:timeout after 20.0 s (client-watchdog): expected 408 from body_timeout 5 s within 4.5–8.0 s; f4 h3/stall: upload error:timeout after 20.0 s (client-watchdog): expected 408 from body_timeout 5 s within 4.5–8.0 s
