# Inspection assessment summary

Plan `inspection-assessment-plan/v3`, profile `qualification`. Runs: ia-a2-s3-large-h2, ia-a2-s4-ramp-large-h2.

> an I5 result shows that the instrument captured a runtime; it is not A1-A18 acceptance. Throughput verdicts between the ADR baseline and post-I1 main are informational because I1 has no request-path enforcement

## Targets

| Label | Adapter | SHA-256 | Go | Revision | Modified |
| --- | --- | --- | --- | --- | --- |
| base | baseline-858590dc | `99941b713dc26a5a321bf82be689b4b05c42bf08a911b0769714c50e0948ef33` | go1.27.4 | `858590dc7c430d24cd33b096a81ca6051591b427` | false |
| main | current | `13a7660d8fb18ddf3e172088a58d662adea0aa45c043a86fb2c37c1b5714995c` | go1.27.4 | `60415e5a71fd8ec26a7559424e3f43e6406738fe` | false |

## Cells

Goodput counts verified responses only. Spread is (max − min) / median over every repetition.

| Scenario | Variant | Target | Rule | n | instrument-valid | correct | median rps | min | max | spread | CPU fractions | peak cgroup MiB |
| --- | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- | --- |
| s3-large-h2 | both | base | capacity | 3 | 3 | 0 | 0.0 | 0.0 | 0.0 | n/a | 1.00 n/a n/a | 768.0 768.0 709.4 |
| s3-large-h2 | both | main | capacity | 3 | 3 | 0 | 0.2 | 0.1 | 0.3 | 0.600 | 1.00 0.99 1.00 | 539.2 525.3 518.7 |
| s3-large-h2 | none | base | capacity | 3 | 3 | 3 | 318.4 | 276.4 | 324.6 | 0.151 | 0.66 0.65 0.64 | 45.4 45.2 42.9 |
| s3-large-h2 | none | main | capacity | 3 | 3 | 3 | 232.3 | 98.6 | 250.8 | 0.655 | 0.71 0.65 0.66 | 40.8 43.5 42.7 |
| s4-ramp-large-h2 | both | base | capacity | 3 | 1 | 0 | 0.0 | 0.0 | 0.0 | n/a | 1.00 1.00 1.00 | 768.0 764.0 749.5 |
| s4-ramp-large-h2 | both | main | capacity | 3 | 3 | 0 | 0.1 | 0.0 | 0.1 | 1.000 | 0.99 0.99 0.99 | 712.6 740.7 763.1 |
| s4-ramp-large-h2 | none | base | capacity | 3 | 3 | 3 | 317.7 | 240.7 | 362.0 | 0.382 | 0.64 0.65 0.65 | 88.7 85.2 85.7 |
| s4-ramp-large-h2 | none | main | capacity | 3 | 3 | 3 | 310.9 | 305.2 | 358.7 | 0.172 | 0.64 0.63 0.64 | 79.7 79.0 82.1 |

## Phases and classes

One row per repetition, phase and workload class. Goodput counts verified responses that started and finished in the window; requests crossing in from warm-up and those outstanding at the window end are excluded from it.

| Scenario | Variant | Target | Run | Rep | Phase | Class | Proto | C | Attempts | Verified in window | Goodput rps | Crossed in | Outstanding | p99 ms | Outcomes |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| s3-large-h2 | both | base | ia-a2-s3-large-h2 | 1 | load | load | h2 | 32 | 6 | 0 | 0.0 | 32 | 1 | n/a | error:eof ×1, status:500 ×5 |
| s3-large-h2 | both | base | ia-a2-s3-large-h2 | 2 | load | load | h2 | 32 | 463812 | 0 | 0.0 | 32 | 32 | n/a | error:refused ×463809, error:reset ×3 |
| s3-large-h2 | both | base | ia-a2-s3-large-h2 | 3 | load | load | h2 | 32 | 357841 | 0 | 0.0 | 32 | 31 | n/a | error:refused ×357838, error:reset ×3 |
| s3-large-h2 | both | main | ia-a2-s3-large-h2 | 1 | load | load | h2 | 32 | 399 | 3 | 0.1 | 32 | 32 | 3846.4 | ok ×6, status:503-inspection-overloaded ×393 |
| s3-large-h2 | both | main | ia-a2-s3-large-h2 | 2 | load | load | h2 | 32 | 383 | 5 | 0.2 | 32 | 32 | 4591.0 | ok ×9, status:503-inspection-overloaded ×374 |
| s3-large-h2 | both | main | ia-a2-s3-large-h2 | 3 | load | load | h2 | 32 | 373 | 6 | 0.3 | 32 | 32 | 3959.9 | ok ×9, status:503-inspection-overloaded ×364 |
| s3-large-h2 | none | base | ia-a2-s3-large-h2 | 1 | load | load | h2 | 32 | 5559 | 5527 | 276.4 | 32 | 32 | 188.6 | ok ×5559 |
| s3-large-h2 | none | base | ia-a2-s3-large-h2 | 2 | load | load | h2 | 32 | 6523 | 6491 | 324.6 | 32 | 32 | 148.2 | ok ×6523 |
| s3-large-h2 | none | base | ia-a2-s3-large-h2 | 3 | load | load | h2 | 32 | 6399 | 6367 | 318.4 | 32 | 32 | 132.7 | ok ×6399 |
| s3-large-h2 | none | main | ia-a2-s3-large-h2 | 1 | load | load | h2 | 32 | 2003 | 1972 | 98.6 | 32 | 31 | 571.8 | ok ×2003 |
| s3-large-h2 | none | main | ia-a2-s3-large-h2 | 2 | load | load | h2 | 32 | 5048 | 5016 | 250.8 | 32 | 32 | 162.6 | ok ×5048 |
| s3-large-h2 | none | main | ia-a2-s3-large-h2 | 3 | load | load | h2 | 32 | 4679 | 4647 | 232.3 | 31 | 32 | 173.0 | ok ×4679 |
| s4-ramp-large-h2 | both | base | ia-a2-s4-ramp-large-h2 | 1 | c16 | load | h2 | 16 | 0 | 0 | 0.0 | 16 | 0 | n/a |  |
| s4-ramp-large-h2 | both | base | ia-a2-s4-ramp-large-h2 | 1 | c32 | load | h2 | 32 | 0 | 0 | 0.0 | 32 | 0 | n/a |  |
| s4-ramp-large-h2 | both | base | ia-a2-s4-ramp-large-h2 | 1 | c64 | load | h2 | 64 | 991090 | 0 | 0.0 | 62 | 63 | n/a | error:refused ×991090 |
| s4-ramp-large-h2 | both | base | ia-a2-s4-ramp-large-h2 | 1 | c128 | load | h2 | 128 | 871671 | 0 | 0.0 | 126 | 128 | n/a | error:refused ×871671 |
| s4-ramp-large-h2 | both | base | ia-a2-s4-ramp-large-h2 | 1 | c192 | load | h2 | 192 | 654436 | 0 | 0.0 | 191 | 188 | n/a | error:refused ×654436 |
| s4-ramp-large-h2 | both | base | ia-a2-s4-ramp-large-h2 | 2 | c16 | load | h2 | 16 | 0 | 0 | 0.0 | 16 | 0 | n/a |  |
| s4-ramp-large-h2 | both | base | ia-a2-s4-ramp-large-h2 | 2 | c32 | load | h2 | 32 | 0 | 0 | 0.0 | 32 | 0 | n/a |  |
| s4-ramp-large-h2 | both | base | ia-a2-s4-ramp-large-h2 | 2 | c64 | load | h2 | 64 | 1009153 | 0 | 0.0 | 61 | 63 | n/a | error:refused ×1009153 |
| s4-ramp-large-h2 | both | base | ia-a2-s4-ramp-large-h2 | 2 | c128 | load | h2 | 128 | 960849 | 0 | 0.0 | 128 | 127 | n/a | error:refused ×960849 |
| s4-ramp-large-h2 | both | base | ia-a2-s4-ramp-large-h2 | 2 | c192 | load | h2 | 192 | 749876 | 0 | 0.0 | 191 | 192 | n/a | error:refused ×749876 |
| s4-ramp-large-h2 | both | base | ia-a2-s4-ramp-large-h2 | 3 | c16 | load | h2 | 16 | 8 | 0 | 0.0 | 16 | 8 | n/a | ok ×8 |
| s4-ramp-large-h2 | both | base | ia-a2-s4-ramp-large-h2 | 3 | c32 | load | h2 | 32 | 1 | 0 | 0.0 | 32 | 1 | n/a | error:eof ×1 |
| s4-ramp-large-h2 | both | base | ia-a2-s4-ramp-large-h2 | 3 | c64 | load | h2 | 64 | 996098 | 0 | 0.0 | 63 | 63 | n/a | error:refused ×996098 |
| s4-ramp-large-h2 | both | base | ia-a2-s4-ramp-large-h2 | 3 | c128 | load | h2 | 128 | 934100 | 0 | 0.0 | 126 | 128 | n/a | error:refused ×934100 |
| s4-ramp-large-h2 | both | base | ia-a2-s4-ramp-large-h2 | 3 | c192 | load | h2 | 192 | 712057 | 0 | 0.0 | 191 | 189 | n/a | error:refused ×712057 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 1 | c16 | load | h2 | 16 | 70 | 1 | 0.1 | 16 | 16 | 3189.2 | ok ×4, status:503-inspection-overloaded ×66 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 1 | c32 | load | h2 | 32 | 188 | 2 | 0.2 | 32 | 32 | 3003.0 | ok ×4, status:503-inspection-overloaded ×184 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 1 | c64 | load | h2 | 64 | 938 | 1 | 0.1 | 64 | 64 | 2423.9 | ok ×3, status:503-inspection-overloaded ×935 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 1 | c128 | load | h2 | 128 | 218586 | 0 | 0.0 | 128 | 128 | n/a | error:eof ×128, error:refused ×218009, status:500 ×1, status:503-inspection-overloaded ×448 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 1 | c192 | load | h2 | 192 | 827248 | 0 | 0.0 | 192 | 192 | n/a | error:refused ×827248 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 2 | c16 | load | h2 | 16 | 56 | 0 | 0.0 | 16 | 16 | n/a | ok ×2, status:503-inspection-overloaded ×54 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 2 | c32 | load | h2 | 32 | 178 | 1 | 0.1 | 32 | 32 | 3510.3 | ok ×3, status:503-inspection-overloaded ×175 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 2 | c64 | load | h2 | 64 | 1003 | 2 | 0.2 | 64 | 64 | 2713.4 | ok ×5, status:503-inspection-overloaded ×998 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 2 | c128 | load | h2 | 128 | 359051 | 1 | 0.1 | 128 | 127 | 2375.3 | error:body-reset ×1, error:refused ×358442, error:reset ×126, ok ×1, status:500 ×2, status:503-inspection-overloaded ×479 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 2 | c192 | load | h2 | 192 | 995130 | 0 | 0.0 | 191 | 192 | n/a | error:refused ×995130 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 3 | c16 | load | h2 | 16 | 84 | 1 | 0.1 | 16 | 16 | 3417.7 | ok ×3, status:503-inspection-overloaded ×81 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 3 | c32 | load | h2 | 32 | 187 | 1 | 0.1 | 32 | 32 | 2738.7 | ok ×5, status:503-inspection-overloaded ×182 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 3 | c64 | load | h2 | 64 | 1048 | 0 | 0.0 | 64 | 64 | n/a | ok ×3, status:500 ×4, status:503-inspection-overloaded ×1041 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 3 | c128 | load | h2 | 128 | 970 | 1 | 0.1 | 128 | 128 | 2683.5 | ok ×4, status:503-inspection-overloaded ×966 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 3 | c192 | load | h2 | 192 | 783022 | 0 | 0.0 | 192 | 190 | n/a | error:refused ×782976, error:reset ×42, status:503-inspection-overloaded ×4 |
| s4-ramp-large-h2 | none | base | ia-a2-s4-ramp-large-h2 | 1 | c16 | load | h2 | 16 | 3636 | 3620 | 362.0 | 16 | 16 | 64.5 | ok ×3636 |
| s4-ramp-large-h2 | none | base | ia-a2-s4-ramp-large-h2 | 1 | c32 | load | h2 | 32 | 2991 | 2959 | 295.9 | 30 | 32 | 136.9 | ok ×2991 |
| s4-ramp-large-h2 | none | base | ia-a2-s4-ramp-large-h2 | 1 | c64 | load | h2 | 64 | 2391 | 2327 | 232.7 | 64 | 64 | 305.0 | ok ×2391 |
| s4-ramp-large-h2 | none | base | ia-a2-s4-ramp-large-h2 | 1 | c128 | load | h2 | 128 | 2108 | 1981 | 198.1 | 128 | 127 | 713.5 | ok ×2108 |
| s4-ramp-large-h2 | none | base | ia-a2-s4-ramp-large-h2 | 1 | c192 | load | h2 | 192 | 2079 | 1887 | 188.7 | 192 | 192 | 1053.2 | ok ×2079 |
| s4-ramp-large-h2 | none | base | ia-a2-s4-ramp-large-h2 | 2 | c16 | load | h2 | 16 | 2423 | 2407 | 240.7 | 16 | 16 | 85.0 | ok ×2423 |
| s4-ramp-large-h2 | none | base | ia-a2-s4-ramp-large-h2 | 2 | c32 | load | h2 | 32 | 2462 | 2430 | 243.0 | 32 | 32 | 190.8 | ok ×2462 |
| s4-ramp-large-h2 | none | base | ia-a2-s4-ramp-large-h2 | 2 | c64 | load | h2 | 64 | 2284 | 2221 | 222.1 | 64 | 63 | 323.6 | ok ×2284 |
| s4-ramp-large-h2 | none | base | ia-a2-s4-ramp-large-h2 | 2 | c128 | load | h2 | 128 | 2375 | 2247 | 224.7 | 128 | 128 | 652.2 | ok ×2375 |
| s4-ramp-large-h2 | none | base | ia-a2-s4-ramp-large-h2 | 2 | c192 | load | h2 | 192 | 2592 | 2400 | 240.0 | 192 | 192 | 866.0 | ok ×2592 |
| s4-ramp-large-h2 | none | base | ia-a2-s4-ramp-large-h2 | 3 | c16 | load | h2 | 16 | 3193 | 3177 | 317.7 | 16 | 16 | 68.7 | ok ×3193 |
| s4-ramp-large-h2 | none | base | ia-a2-s4-ramp-large-h2 | 3 | c32 | load | h2 | 32 | 2617 | 2585 | 258.5 | 31 | 32 | 155.2 | ok ×2617 |
| s4-ramp-large-h2 | none | base | ia-a2-s4-ramp-large-h2 | 3 | c64 | load | h2 | 64 | 2481 | 2418 | 241.8 | 64 | 63 | 385.4 | ok ×2481 |
| s4-ramp-large-h2 | none | base | ia-a2-s4-ramp-large-h2 | 3 | c128 | load | h2 | 128 | 2097 | 1969 | 196.9 | 128 | 128 | 702.8 | ok ×2097 |
| s4-ramp-large-h2 | none | base | ia-a2-s4-ramp-large-h2 | 3 | c192 | load | h2 | 192 | 2389 | 2197 | 219.7 | 192 | 192 | 1027.1 | ok ×2389 |
| s4-ramp-large-h2 | none | main | ia-a2-s4-ramp-large-h2 | 1 | c16 | load | h2 | 16 | 3068 | 3052 | 305.2 | 15 | 16 | 78.8 | ok ×3068 |
| s4-ramp-large-h2 | none | main | ia-a2-s4-ramp-large-h2 | 1 | c32 | load | h2 | 32 | 2969 | 2937 | 293.7 | 31 | 32 | 130.0 | ok ×2969 |
| s4-ramp-large-h2 | none | main | ia-a2-s4-ramp-large-h2 | 1 | c64 | load | h2 | 64 | 2921 | 2857 | 285.7 | 63 | 64 | 263.5 | ok ×2921 |
| s4-ramp-large-h2 | none | main | ia-a2-s4-ramp-large-h2 | 1 | c128 | load | h2 | 128 | 2693 | 2566 | 256.6 | 128 | 127 | 551.8 | ok ×2693 |
| s4-ramp-large-h2 | none | main | ia-a2-s4-ramp-large-h2 | 1 | c192 | load | h2 | 192 | 2413 | 2221 | 222.1 | 192 | 192 | 902.7 | ok ×2413 |
| s4-ramp-large-h2 | none | main | ia-a2-s4-ramp-large-h2 | 2 | c16 | load | h2 | 16 | 3602 | 3587 | 358.7 | 16 | 15 | 69.4 | ok ×3602 |
| s4-ramp-large-h2 | none | main | ia-a2-s4-ramp-large-h2 | 2 | c32 | load | h2 | 32 | 3035 | 3003 | 300.3 | 32 | 32 | 137.4 | ok ×3035 |
| s4-ramp-large-h2 | none | main | ia-a2-s4-ramp-large-h2 | 2 | c64 | load | h2 | 64 | 2548 | 2484 | 248.4 | 63 | 64 | 352.8 | ok ×2548 |
| s4-ramp-large-h2 | none | main | ia-a2-s4-ramp-large-h2 | 2 | c128 | load | h2 | 128 | 2241 | 2113 | 211.3 | 128 | 128 | 799.0 | ok ×2241 |
| s4-ramp-large-h2 | none | main | ia-a2-s4-ramp-large-h2 | 2 | c192 | load | h2 | 192 | 2147 | 1956 | 195.6 | 192 | 191 | 1068.0 | ok ×2147 |
| s4-ramp-large-h2 | none | main | ia-a2-s4-ramp-large-h2 | 3 | c16 | load | h2 | 16 | 3125 | 3109 | 310.9 | 16 | 16 | 73.2 | ok ×3125 |
| s4-ramp-large-h2 | none | main | ia-a2-s4-ramp-large-h2 | 3 | c32 | load | h2 | 32 | 4251 | 4219 | 421.9 | 32 | 32 | 180.6 | ok ×4251 |
| s4-ramp-large-h2 | none | main | ia-a2-s4-ramp-large-h2 | 3 | c64 | load | h2 | 64 | 2783 | 2719 | 271.9 | 64 | 64 | 292.2 | ok ×2783 |
| s4-ramp-large-h2 | none | main | ia-a2-s4-ramp-large-h2 | 3 | c128 | load | h2 | 128 | 2662 | 2534 | 253.4 | 128 | 128 | 828.5 | ok ×2662 |
| s4-ramp-large-h2 | none | main | ia-a2-s4-ramp-large-h2 | 3 | c192 | load | h2 | 192 | 2239 | 2047 | 204.7 | 191 | 192 | 1000.3 | ok ×2239 |

## Non-success outcome latency

Completed attempts started in the window, per outcome (for example `status:503` for overload), including those that finished after it.

| Scenario | Variant | Target | Run | Rep | Phase | Class | Outcome | n | p50 ms | p95 ms | p99 ms | max ms |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: |
| s3-large-h2 | both | base | ia-a2-s3-large-h2 | 1 | load | load | error:eof | 1 | 26198.7 | 26198.7 | 26198.7 | 26198.7 |
| s3-large-h2 | both | base | ia-a2-s3-large-h2 | 1 | load | load | status:500 | 5 | 1065.6 | 1425.4 | 1425.4 | 1509.9 |
| s3-large-h2 | both | base | ia-a2-s3-large-h2 | 2 | load | load | error:refused | 463809 | 0.2 | 0.9 | 1.6 | 27.5 |
| s3-large-h2 | both | base | ia-a2-s3-large-h2 | 2 | load | load | error:reset | 3 | 619.8 | 619.8 | 619.8 | 620.0 |
| s3-large-h2 | both | base | ia-a2-s3-large-h2 | 3 | load | load | error:refused | 357838 | 0.2 | 0.9 | 1.6 | 19.8 |
| s3-large-h2 | both | base | ia-a2-s3-large-h2 | 3 | load | load | error:reset | 3 | 182.6 | 182.6 | 182.6 | 465.4 |
| s3-large-h2 | both | main | ia-a2-s3-large-h2 | 1 | load | load | status:503-inspection-overloaded | 393 | 1312.8 | 3173.3 | 4489.1 | 5149.9 |
| s3-large-h2 | both | main | ia-a2-s3-large-h2 | 2 | load | load | status:503-inspection-overloaded | 374 | 1315.1 | 2687.7 | 3851.9 | 3890.5 |
| s3-large-h2 | both | main | ia-a2-s3-large-h2 | 3 | load | load | status:503-inspection-overloaded | 364 | 1307.6 | 2628.3 | 3914.3 | 5162.1 |
| s4-ramp-large-h2 | both | base | ia-a2-s4-ramp-large-h2 | 1 | c64 | load | error:refused | 991090 | 0.5 | 1.6 | 2.6 | 61.3 |
| s4-ramp-large-h2 | both | base | ia-a2-s4-ramp-large-h2 | 1 | c128 | load | error:refused | 871671 | 1.1 | 3.8 | 5.8 | 72.8 |
| s4-ramp-large-h2 | both | base | ia-a2-s4-ramp-large-h2 | 1 | c192 | load | error:refused | 654436 | 2.1 | 7.9 | 11.8 | 67.1 |
| s4-ramp-large-h2 | both | base | ia-a2-s4-ramp-large-h2 | 2 | c64 | load | error:refused | 1009153 | 0.5 | 1.6 | 2.4 | 79.1 |
| s4-ramp-large-h2 | both | base | ia-a2-s4-ramp-large-h2 | 2 | c128 | load | error:refused | 960849 | 1.0 | 3.4 | 5.4 | 87.6 |
| s4-ramp-large-h2 | both | base | ia-a2-s4-ramp-large-h2 | 2 | c192 | load | error:refused | 749876 | 1.9 | 6.7 | 10.0 | 96.6 |
| s4-ramp-large-h2 | both | base | ia-a2-s4-ramp-large-h2 | 3 | c32 | load | error:eof | 1 | 16907.8 | 16907.8 | 16907.8 | 16907.8 |
| s4-ramp-large-h2 | both | base | ia-a2-s4-ramp-large-h2 | 3 | c64 | load | error:refused | 996098 | 0.5 | 1.6 | 2.5 | 32.4 |
| s4-ramp-large-h2 | both | base | ia-a2-s4-ramp-large-h2 | 3 | c128 | load | error:refused | 934100 | 1.0 | 3.5 | 5.3 | 58.9 |
| s4-ramp-large-h2 | both | base | ia-a2-s4-ramp-large-h2 | 3 | c192 | load | error:refused | 712057 | 1.9 | 7.1 | 10.7 | 117.3 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 1 | c16 | load | status:503-inspection-overloaded | 66 | 1754.0 | 3353.2 | 3737.8 | 3739.4 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 1 | c32 | load | status:503-inspection-overloaded | 184 | 1418.8 | 2703.2 | 3750.4 | 4275.2 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 1 | c64 | load | status:503-inspection-overloaded | 935 | 383.2 | 1780.6 | 2463.7 | 5008.9 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 1 | c128 | load | error:eof | 128 | 1254.9 | 3036.2 | 3926.8 | 4078.0 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 1 | c128 | load | error:refused | 218009 | 1.2 | 9.9 | 18.4 | 64.7 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 1 | c128 | load | status:500 | 1 | 1773.8 | 1773.8 | 1773.8 | 1773.8 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 1 | c128 | load | status:503-inspection-overloaded | 448 | 794.7 | 2274.7 | 3206.8 | 4510.4 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 1 | c192 | load | error:refused | 827248 | 1.7 | 6.1 | 9.5 | 193.1 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 2 | c16 | load | status:503-inspection-overloaded | 54 | 2703.7 | 5386.1 | 5647.7 | 5667.4 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 2 | c32 | load | status:503-inspection-overloaded | 175 | 1530.3 | 2693.9 | 3617.8 | 4272.8 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 2 | c64 | load | status:503-inspection-overloaded | 998 | 312.0 | 2186.0 | 2854.5 | 4575.3 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 2 | c128 | load | error:body-reset | 1 | 151.1 | 151.1 | 151.1 | 151.1 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 2 | c128 | load | error:refused | 358442 | 1.0 | 4.0 | 7.2 | 29.4 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 2 | c128 | load | error:reset | 126 | 947.8 | 3548.1 | 4386.8 | 5614.6 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 2 | c128 | load | status:500 | 2 | 538.7 | 538.7 | 538.7 | 3893.4 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 2 | c128 | load | status:503-inspection-overloaded | 479 | 735.7 | 2579.1 | 3594.9 | 4609.1 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 2 | c192 | load | error:refused | 995130 | 1.4 | 5.1 | 7.6 | 51.6 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 3 | c16 | load | status:503-inspection-overloaded | 81 | 1803.2 | 3559.0 | 4087.1 | 4087.5 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 3 | c32 | load | status:503-inspection-overloaded | 182 | 1490.8 | 2804.2 | 4161.6 | 4705.3 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 3 | c64 | load | status:500 | 4 | 305.7 | 400.1 | 400.1 | 536.1 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 3 | c64 | load | status:503-inspection-overloaded | 1041 | 293.2 | 2063.7 | 2772.0 | 4130.7 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 3 | c128 | load | status:503-inspection-overloaded | 966 | 921.7 | 3370.5 | 4892.3 | 7088.3 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 3 | c192 | load | error:refused | 782976 | 1.6 | 6.4 | 10.4 | 51.6 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 3 | c192 | load | error:reset | 42 | 334.6 | 493.6 | 519.6 | 522.7 |
| s4-ramp-large-h2 | both | main | ia-a2-s4-ramp-large-h2 | 3 | c192 | load | status:503-inspection-overloaded | 4 | 224.3 | 395.8 | 395.8 | 425.3 |

## Failed, invalid or inconclusive runs

- ia-a2-s3-large-h2 s3-large-h2/both/base rep1: runtime-finding: jul exited during the run: signal killed; phase load class load: error:eof ×1; phase load class load: status:500 ×5; oom_kill delta 1 in /sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/app.slice (hierarchical memory.events oom_kill; includes sibling scopes)
- ia-a2-s3-large-h2 s3-large-h2/both/base rep2: runtime-finding: jul exited during the run: signal killed; phase load class load: error:refused ×463809; phase load class load: error:reset ×3; oom_kill delta 1 in /sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/app.slice (hierarchical memory.events oom_kill; includes sibling scopes)
- ia-a2-s3-large-h2 s3-large-h2/both/base rep3: runtime-finding: jul exited during the run: signal killed; phase load class load: error:refused ×357838; phase load class load: error:reset ×3; oom_kill delta 1 in /sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/app.slice (hierarchical memory.events oom_kill; includes sibling scopes)
- ia-a2-s3-large-h2 s3-large-h2/both/main rep1: runtime-finding: phase load class load: status:503-inspection-overloaded ×393
- ia-a2-s3-large-h2 s3-large-h2/both/main rep2: runtime-finding: phase load class load: status:503-inspection-overloaded ×374
- ia-a2-s3-large-h2 s3-large-h2/both/main rep3: runtime-finding: phase load class load: status:503-inspection-overloaded ×364
- ia-a2-s4-ramp-large-h2 s4-ramp-large-h2/both/base rep1: instrument-invalid: phase c16 class load: no attempt started inside the window; phase c32 class load: no attempt started inside the window
- ia-a2-s4-ramp-large-h2 s4-ramp-large-h2/both/base rep1: runtime-finding: jul exited during the run: signal killed; phase c64 class load: error:refused ×991090; phase c128 class load: error:refused ×871671; phase c192 class load: error:refused ×654436; oom_kill delta 1 in /sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/app.slice (hierarchical memory.events oom_kill; includes sibling scopes)
- ia-a2-s4-ramp-large-h2 s4-ramp-large-h2/both/base rep2: instrument-invalid: phase c16 class load: no attempt started inside the window; phase c32 class load: no attempt started inside the window
- ia-a2-s4-ramp-large-h2 s4-ramp-large-h2/both/base rep2: runtime-finding: jul exited during the run: signal killed; phase c64 class load: error:refused ×1009153; phase c128 class load: error:refused ×960849; phase c192 class load: error:refused ×749876; oom_kill delta 1 in /sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/app.slice (hierarchical memory.events oom_kill; includes sibling scopes)
- ia-a2-s4-ramp-large-h2 s4-ramp-large-h2/both/base rep3: runtime-finding: jul exited during the run: signal killed; phase c32 class load: error:eof ×1; phase c64 class load: error:refused ×996098; phase c128 class load: error:refused ×934100; phase c192 class load: error:refused ×712057; oom_kill delta 1 in /sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/app.slice (hierarchical memory.events oom_kill; includes sibling scopes)
- ia-a2-s4-ramp-large-h2 s4-ramp-large-h2/both/main rep1: runtime-finding: jul exited during the run: signal killed; phase c16 class load: status:503-inspection-overloaded ×66; phase c32 class load: status:503-inspection-overloaded ×184; phase c64 class load: status:503-inspection-overloaded ×935; phase c128 class load: error:eof ×128; phase c128 class load: error:refused ×218009; phase c128 class load: status:500 ×1; phase c128 class load: status:503-inspection-overloaded ×448; phase c192 class load: error:refused ×827248; oom_kill delta 1 in /sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/app.slice (hierarchical memory.events oom_kill; includes sibling scopes)
- ia-a2-s4-ramp-large-h2 s4-ramp-large-h2/both/main rep2: runtime-finding: jul exited during the run: signal killed; phase c16 class load: status:503-inspection-overloaded ×54; phase c32 class load: status:503-inspection-overloaded ×175; phase c64 class load: status:503-inspection-overloaded ×998; phase c128 class load: error:body-reset ×1; phase c128 class load: error:refused ×358442; phase c128 class load: error:reset ×126; phase c128 class load: status:500 ×2; phase c128 class load: status:503-inspection-overloaded ×479; phase c192 class load: error:refused ×995130; oom_kill delta 1 in /sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/app.slice (hierarchical memory.events oom_kill; includes sibling scopes)
- ia-a2-s4-ramp-large-h2 s4-ramp-large-h2/both/main rep3: runtime-finding: jul exited during the run: signal killed; phase c16 class load: status:503-inspection-overloaded ×81; phase c32 class load: status:503-inspection-overloaded ×182; phase c64 class load: status:500 ×4; phase c64 class load: status:503-inspection-overloaded ×1041; phase c128 class load: status:503-inspection-overloaded ×966; phase c192 class load: error:refused ×782976; phase c192 class load: error:reset ×42; phase c192 class load: status:503-inspection-overloaded ×4; oom_kill delta 1 in /sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/app.slice (hierarchical memory.events oom_kill; includes sibling scopes)
