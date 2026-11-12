# Inspection assessment summary

Plan `inspection-assessment-plan/v3`, profile `qualification`. Runs: i2-normal-supplement.

> an I5 result shows that the instrument captured a runtime; it is not A1-A18 acceptance. Throughput verdicts between the ADR baseline and post-I1 main are informational because I1 has no request-path enforcement

## Targets

| Label | Adapter | SHA-256 | Go | Revision | Modified |
| --- | --- | --- | --- | --- | --- |
| base | baseline-858590dc | `520febcbad5669da0ea8ad98e29f0bb78304817511cd7ed63bfb48e9d63ea52f` | go1.27.3 | `858590dc7c430d24cd33b096a81ca6051591b427` | false |
| base4 | baseline-858590dc | `e0f105cbae69939b5b91a5bbfd7165f993fcb181c4d813bfa241486ab3a2291b` | go1.27.4 | `858590dc7c430d24cd33b096a81ca6051591b427` | false |
| cand | current | `3edee005bc08754283e77c8b6f1688204d24927c2662697f2f82a1eccbf54e73` | go1.27.4 | `` |  |
| main | current | `65a0f8b9d42909b048fe3fe00ecda7dbf17ae28320bdef28bd82a1ea2f0add37` | go1.27.4 | `` |  |

## Cells

Goodput counts verified responses only. Spread is (max − min) / median over every repetition.

| Scenario | Variant | Target | Rule | n | instrument-valid | correct | median rps | min | max | spread | CPU fractions | peak cgroup MiB |
| --- | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- | --- |
| u1-normal-h1 | none | base | throughput | 3 | 3 | 3 | 4974.1 | 2322.6 | 5967.4 | 0.733 | 0.69 0.63 0.64 | 23.4 23.3 23.4 |
| u1-normal-h1 | none | base4 | throughput | 3 | 3 | 3 | 4817.4 | 1798.7 | 4864.5 | 0.636 | 0.70 0.64 0.64 | 41.2 39.9 41.6 |
| u1-normal-h1 | none | cand | throughput | 3 | 3 | 3 | 5563.3 | 3702.2 | 5708.4 | 0.361 | 0.65 0.63 0.63 | 23.7 23.7 23.5 |
| u1-normal-h1 | none | main | throughput | 3 | 3 | 3 | 4588.6 | 3054.6 | 5547.4 | 0.543 | 0.65 0.61 0.63 | 23.5 24.0 23.5 |
| u1-normal-h1 | wasm | base | throughput | 3 | 3 | 3 | 450.5 | 225.2 | 502.8 | 0.616 | 0.69 0.65 0.63 | 205.6 211.4 212.9 |
| u1-normal-h1 | wasm | base4 | throughput | 3 | 3 | 3 | 516.5 | 382.6 | 554.8 | 0.333 | 0.65 0.64 0.64 | 215.7 221.1 223.9 |
| u1-normal-h1 | wasm | cand | throughput | 3 | 3 | 3 | 428.1 | 340.4 | 519.1 | 0.418 | 0.65 0.64 0.66 | 217.7 210.3 213.0 |
| u1-normal-h1 | wasm | main | throughput | 3 | 3 | 3 | 448.9 | 377.0 | 515.3 | 0.308 | 0.64 0.64 0.65 | 217.1 213.5 210.6 |
| u10-normal-h3 | none | base | throughput | 3 | 3 | 3 | 2560.2 | 1650.0 | 2689.7 | 0.406 | 0.63 0.63 0.65 | 26.2 26.3 25.7 |
| u10-normal-h3 | none | base4 | throughput | 3 | 3 | 3 | 1945.5 | 1925.5 | 2221.6 | 0.152 | 0.64 0.66 0.65 | 41.9 42.6 44.3 |
| u10-normal-h3 | none | cand | throughput | 3 | 3 | 3 | 2535.7 | 2058.2 | 2682.7 | 0.246 | 0.63 0.63 0.65 | 26.8 26.2 25.7 |
| u10-normal-h3 | none | main | throughput | 3 | 3 | 3 | 2248.8 | 1966.1 | 2690.9 | 0.322 | 0.64 0.63 0.65 | 27.1 26.7 25.8 |
| u10-normal-h3 | wasm | base | throughput | 3 | 3 | 3 | 379.5 | 287.4 | 429.5 | 0.374 | 0.63 0.62 0.64 | 211.5 215.6 205.9 |
| u10-normal-h3 | wasm | base4 | throughput | 3 | 3 | 3 | 359.1 | 357.1 | 500.0 | 0.398 | 0.63 0.61 0.63 | 226.8 229.9 220.8 |
| u10-normal-h3 | wasm | cand | throughput | 3 | 3 | 3 | 370.2 | 303.9 | 414.1 | 0.298 | 0.62 0.63 0.64 | 214.5 207.5 206.4 |
| u10-normal-h3 | wasm | main | throughput | 3 | 3 | 3 | 305.1 | 296.9 | 439.8 | 0.468 | 0.62 0.64 0.63 | 210.0 206.4 204.2 |
| u1b-normal-h1-none-c8 | none | base | throughput | 5 | 5 | 5 | 4239.9 | 3109.0 | 5635.8 | 0.596 | 0.61 0.59 0.61 0.61 0.61 | 21.9 22.1 22.8 21.8 21.6 |
| u1b-normal-h1-none-c8 | none | base4 | throughput | 5 | 5 | 5 | 4218.3 | 3220.1 | 4687.8 | 0.348 | 0.60 0.61 0.61 0.61 0.62 | 38.7 37.9 38.4 38.9 41.2 |
| u1b-normal-h1-none-c8 | none | cand | throughput | 5 | 5 | 5 | 3677.4 | 3171.5 | 4359.5 | 0.323 | 0.61 0.61 0.61 0.63 0.61 | 23.0 22.3 21.3 21.8 22.2 |
| u1b-normal-h1-none-c8 | none | main | throughput | 5 | 5 | 5 | 4262.8 | 3275.8 | 5264.0 | 0.466 | 0.60 0.61 0.61 0.61 0.62 | 22.6 22.2 21.5 21.6 22.1 |
| u2-normal-h2 | none | base | throughput | 3 | 3 | 3 | 1730.3 | 1709.2 | 2003.8 | 0.170 | 0.64 0.64 0.63 | 25.3 24.8 25.1 |
| u2-normal-h2 | none | base4 | throughput | 3 | 3 | 3 | 1843.5 | 1301.0 | 1874.8 | 0.311 | 0.66 0.64 0.63 | 42.8 43.4 41.5 |
| u2-normal-h2 | none | cand | throughput | 3 | 3 | 3 | 1937.3 | 1872.7 | 2158.2 | 0.147 | 0.63 0.63 0.64 | 24.7 25.2 25.1 |
| u2-normal-h2 | none | main | throughput | 3 | 3 | 3 | 1850.5 | 1659.3 | 1895.0 | 0.127 | 0.63 0.64 0.64 | 24.9 24.5 24.5 |
| u2-normal-h2 | wasm | base | throughput | 3 | 3 | 3 | 423.7 | 329.6 | 528.0 | 0.468 | 0.65 0.65 0.66 | 205.4 220.2 207.5 |
| u2-normal-h2 | wasm | base4 | throughput | 3 | 3 | 3 | 546.8 | 377.9 | 555.5 | 0.325 | 0.64 0.64 0.65 | 222.9 224.3 223.0 |
| u2-normal-h2 | wasm | cand | throughput | 3 | 3 | 3 | 371.4 | 289.4 | 562.8 | 0.736 | 0.65 0.64 0.67 | 203.1 209.0 203.7 |
| u2-normal-h2 | wasm | main | throughput | 3 | 3 | 3 | 460.9 | 283.2 | 517.5 | 0.508 | 0.65 0.65 0.66 | 205.9 216.6 199.1 |

## Comparisons (informational; not A13 acceptance)

| Scenario | Variant | Baseline | Candidate | Median regression | Verdict |
| --- | --- | --- | --- | ---: | --- |
| u1-normal-h1 | none | main | cand | -0.211 | inconclusive-noise |
| u1-normal-h1 | wasm | main | cand | 0.047 | inconclusive-noise |
| u10-normal-h3 | none | main | cand | -0.127 | inconclusive-noise |
| u10-normal-h3 | wasm | main | cand | -0.212 | inconclusive-noise |
| u1b-normal-h1-none-c8 | none | main | cand | 0.137 | fail-regression |
| u2-normal-h2 | none | main | cand | -0.046 | inconclusive-noise |
| u2-normal-h2 | wasm | main | cand | 0.194 | fail-regression |
| u1-normal-h1 | none | base4 | cand | -0.154 | inconclusive-noise |
| u1-normal-h1 | wasm | base4 | cand | 0.171 | fail-regression |
| u10-normal-h3 | none | base4 | cand | -0.302 | inconclusive-noise |
| u10-normal-h3 | wasm | base4 | cand | -0.030 | inconclusive-noise |
| u1b-normal-h1-none-c8 | none | base4 | cand | 0.128 | fail-regression |
| u2-normal-h2 | none | base4 | cand | -0.050 | inconclusive-noise |
| u2-normal-h2 | wasm | base4 | cand | 0.321 | fail-regression |
| u1-normal-h1 | none | base4 | main | 0.047 | inconclusive-noise |
| u1-normal-h1 | wasm | base4 | main | 0.131 | fail-regression |
| u10-normal-h3 | none | base4 | main | -0.155 | inconclusive-noise |
| u10-normal-h3 | wasm | base4 | main | 0.150 | fail-regression |
| u1b-normal-h1-none-c8 | none | base4 | main | -0.010 | inconclusive-noise |
| u2-normal-h2 | none | base4 | main | -0.003 | inconclusive-noise |
| u2-normal-h2 | wasm | base4 | main | 0.157 | fail-regression |
| u1-normal-h1 | none | base | base4 | 0.032 | inconclusive-noise |
| u1-normal-h1 | wasm | base | base4 | -0.146 | inconclusive-noise |
| u10-normal-h3 | none | base | base4 | 0.240 | fail-regression |
| u10-normal-h3 | wasm | base | base4 | 0.054 | inconclusive-noise |
| u1b-normal-h1-none-c8 | none | base | base4 | 0.005 | inconclusive-noise |
| u2-normal-h2 | none | base | base4 | -0.064 | inconclusive-noise |
| u2-normal-h2 | wasm | base | base4 | -0.289 | inconclusive-noise |

## Phases and classes

One row per repetition, phase and workload class. Goodput counts verified responses that started and finished in the window; requests crossing in from warm-up and those outstanding at the window end are excluded from it.

| Scenario | Variant | Target | Run | Rep | Phase | Class | Proto | C | Attempts | Verified in window | Goodput rps | Crossed in | Outstanding | p99 ms | Outcomes |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| u1-normal-h1 | none | base | i2-normal-supplement | 1 | load | load | h1 | 16 | 46468 | 46452 | 2322.6 | 15 | 16 | 23.9 | ok ×46468 |
| u1-normal-h1 | none | base | i2-normal-supplement | 2 | load | load | h1 | 16 | 119363 | 119347 | 5967.4 | 16 | 16 | 10.3 | ok ×119363 |
| u1-normal-h1 | none | base | i2-normal-supplement | 3 | load | load | h1 | 16 | 99498 | 99482 | 4974.1 | 16 | 16 | 12.9 | ok ×99498 |
| u1-normal-h1 | none | base4 | i2-normal-supplement | 1 | load | load | h1 | 16 | 35990 | 35974 | 1798.7 | 16 | 16 | 26.8 | ok ×35990 |
| u1-normal-h1 | none | base4 | i2-normal-supplement | 2 | load | load | h1 | 16 | 96363 | 96347 | 4817.4 | 16 | 16 | 13.8 | ok ×96363 |
| u1-normal-h1 | none | base4 | i2-normal-supplement | 3 | load | load | h1 | 16 | 97306 | 97290 | 4864.5 | 16 | 16 | 13.1 | ok ×97306 |
| u1-normal-h1 | none | cand | i2-normal-supplement | 1 | load | load | h1 | 16 | 74059 | 74043 | 3702.2 | 16 | 16 | 15.6 | ok ×74059 |
| u1-normal-h1 | none | cand | i2-normal-supplement | 2 | load | load | h1 | 16 | 114183 | 114167 | 5708.4 | 14 | 16 | 11.3 | ok ×114183 |
| u1-normal-h1 | none | cand | i2-normal-supplement | 3 | load | load | h1 | 16 | 111281 | 111266 | 5563.3 | 16 | 15 | 11.4 | ok ×111281 |
| u1-normal-h1 | none | main | i2-normal-supplement | 1 | load | load | h1 | 16 | 61107 | 61091 | 3054.6 | 15 | 16 | 19.5 | ok ×61107 |
| u1-normal-h1 | none | main | i2-normal-supplement | 2 | load | load | h1 | 16 | 110963 | 110947 | 5547.4 | 16 | 16 | 10.9 | ok ×110963 |
| u1-normal-h1 | none | main | i2-normal-supplement | 3 | load | load | h1 | 16 | 91787 | 91771 | 4588.6 | 16 | 16 | 13.1 | ok ×91787 |
| u1-normal-h1 | wasm | base | i2-normal-supplement | 1 | load | load | h1 | 2 | 4505 | 4503 | 225.2 | 2 | 2 | 24.9 | ok ×4505 |
| u1-normal-h1 | wasm | base | i2-normal-supplement | 2 | load | load | h1 | 2 | 9011 | 9010 | 450.5 | 2 | 1 | 12.5 | ok ×9011 |
| u1-normal-h1 | wasm | base | i2-normal-supplement | 3 | load | load | h1 | 2 | 10057 | 10055 | 502.8 | 2 | 2 | 11.0 | ok ×10057 |
| u1-normal-h1 | wasm | base4 | i2-normal-supplement | 1 | load | load | h1 | 2 | 7653 | 7651 | 382.6 | 2 | 2 | 14.8 | ok ×7653 |
| u1-normal-h1 | wasm | base4 | i2-normal-supplement | 2 | load | load | h1 | 2 | 10332 | 10330 | 516.5 | 2 | 2 | 12.0 | ok ×10332 |
| u1-normal-h1 | wasm | base4 | i2-normal-supplement | 3 | load | load | h1 | 2 | 11098 | 11096 | 554.8 | 2 | 2 | 10.0 | ok ×11098 |
| u1-normal-h1 | wasm | cand | i2-normal-supplement | 1 | load | load | h1 | 2 | 8563 | 8561 | 428.1 | 2 | 2 | 12.7 | ok ×8563 |
| u1-normal-h1 | wasm | cand | i2-normal-supplement | 2 | load | load | h1 | 2 | 10384 | 10382 | 519.1 | 2 | 2 | 11.4 | ok ×10384 |
| u1-normal-h1 | wasm | cand | i2-normal-supplement | 3 | load | load | h1 | 2 | 6809 | 6807 | 340.4 | 2 | 2 | 15.5 | ok ×6809 |
| u1-normal-h1 | wasm | main | i2-normal-supplement | 1 | load | load | h1 | 2 | 8981 | 8979 | 448.9 | 2 | 2 | 12.0 | ok ×8981 |
| u1-normal-h1 | wasm | main | i2-normal-supplement | 2 | load | load | h1 | 2 | 10308 | 10306 | 515.3 | 2 | 2 | 11.5 | ok ×10308 |
| u1-normal-h1 | wasm | main | i2-normal-supplement | 3 | load | load | h1 | 2 | 7542 | 7540 | 377.0 | 2 | 2 | 15.6 | ok ×7542 |
| u10-normal-h3 | none | base | i2-normal-supplement | 1 | load | load | h3 | 16 | 51220 | 51204 | 2560.2 | 16 | 16 | 15.1 | ok ×51220 |
| u10-normal-h3 | none | base | i2-normal-supplement | 2 | load | load | h3 | 16 | 53809 | 53793 | 2689.7 | 16 | 16 | 14.5 | ok ×53809 |
| u10-normal-h3 | none | base | i2-normal-supplement | 3 | load | load | h3 | 16 | 33016 | 33000 | 1650.0 | 16 | 16 | 31.1 | ok ×33016 |
| u10-normal-h3 | none | base4 | i2-normal-supplement | 1 | load | load | h3 | 16 | 44448 | 44432 | 2221.6 | 16 | 16 | 16.1 | ok ×44448 |
| u10-normal-h3 | none | base4 | i2-normal-supplement | 2 | load | load | h3 | 16 | 38527 | 38511 | 1925.5 | 15 | 16 | 18.1 | ok ×38527 |
| u10-normal-h3 | none | base4 | i2-normal-supplement | 3 | load | load | h3 | 16 | 38926 | 38910 | 1945.5 | 16 | 16 | 19.9 | ok ×38926 |
| u10-normal-h3 | none | cand | i2-normal-supplement | 1 | load | load | h3 | 16 | 50729 | 50713 | 2535.7 | 16 | 16 | 14.9 | ok ×50729 |
| u10-normal-h3 | none | cand | i2-normal-supplement | 2 | load | load | h3 | 16 | 53669 | 53653 | 2682.7 | 16 | 16 | 13.7 | ok ×53669 |
| u10-normal-h3 | none | cand | i2-normal-supplement | 3 | load | load | h3 | 16 | 41181 | 41165 | 2058.2 | 16 | 16 | 16.6 | ok ×41181 |
| u10-normal-h3 | none | main | i2-normal-supplement | 1 | load | load | h3 | 16 | 44993 | 44977 | 2248.8 | 16 | 16 | 16.1 | ok ×44993 |
| u10-normal-h3 | none | main | i2-normal-supplement | 2 | load | load | h3 | 16 | 53834 | 53818 | 2690.9 | 16 | 16 | 14.2 | ok ×53834 |
| u10-normal-h3 | none | main | i2-normal-supplement | 3 | load | load | h3 | 16 | 39338 | 39322 | 1966.1 | 16 | 16 | 19.5 | ok ×39338 |
| u10-normal-h3 | wasm | base | i2-normal-supplement | 1 | load | load | h3 | 2 | 7592 | 7590 | 379.5 | 2 | 2 | 13.3 | ok ×7592 |
| u10-normal-h3 | wasm | base | i2-normal-supplement | 2 | load | load | h3 | 2 | 8592 | 8590 | 429.5 | 2 | 2 | 11.8 | ok ×8592 |
| u10-normal-h3 | wasm | base | i2-normal-supplement | 3 | load | load | h3 | 2 | 5751 | 5749 | 287.4 | 2 | 2 | 17.8 | ok ×5751 |
| u10-normal-h3 | wasm | base4 | i2-normal-supplement | 1 | load | load | h3 | 2 | 7144 | 7142 | 357.1 | 2 | 2 | 14.7 | ok ×7144 |
| u10-normal-h3 | wasm | base4 | i2-normal-supplement | 2 | load | load | h3 | 2 | 10002 | 10000 | 500.0 | 2 | 2 | 10.0 | ok ×10002 |
| u10-normal-h3 | wasm | base4 | i2-normal-supplement | 3 | load | load | h3 | 2 | 7184 | 7182 | 359.1 | 2 | 2 | 14.4 | ok ×7184 |
| u10-normal-h3 | wasm | cand | i2-normal-supplement | 1 | load | load | h3 | 2 | 8283 | 8281 | 414.1 | 2 | 2 | 13.3 | ok ×8283 |
| u10-normal-h3 | wasm | cand | i2-normal-supplement | 2 | load | load | h3 | 2 | 7406 | 7404 | 370.2 | 2 | 2 | 13.6 | ok ×7406 |
| u10-normal-h3 | wasm | cand | i2-normal-supplement | 3 | load | load | h3 | 2 | 6080 | 6078 | 303.9 | 2 | 2 | 15.9 | ok ×6080 |
| u10-normal-h3 | wasm | main | i2-normal-supplement | 1 | load | load | h3 | 2 | 8797 | 8795 | 439.8 | 2 | 2 | 11.1 | ok ×8797 |
| u10-normal-h3 | wasm | main | i2-normal-supplement | 2 | load | load | h3 | 2 | 5939 | 5937 | 296.9 | 2 | 2 | 17.3 | ok ×5939 |
| u10-normal-h3 | wasm | main | i2-normal-supplement | 3 | load | load | h3 | 2 | 6105 | 6103 | 305.1 | 2 | 2 | 16.7 | ok ×6105 |
| u1b-normal-h1-none-c8 | none | base | i2-normal-supplement | 1 | load | load | h1 | 8 | 84805 | 84797 | 4239.9 | 7 | 8 | 7.7 | ok ×84805 |
| u1b-normal-h1-none-c8 | none | base | i2-normal-supplement | 2 | load | load | h1 | 8 | 112724 | 112716 | 5635.8 | 8 | 8 | 6.3 | ok ×112724 |
| u1b-normal-h1-none-c8 | none | base | i2-normal-supplement | 3 | load | load | h1 | 8 | 85180 | 85172 | 4258.6 | 8 | 8 | 7.4 | ok ×85180 |
| u1b-normal-h1-none-c8 | none | base | i2-normal-supplement | 4 | load | load | h1 | 8 | 70836 | 70828 | 3541.4 | 8 | 8 | 8.9 | ok ×70836 |
| u1b-normal-h1-none-c8 | none | base | i2-normal-supplement | 5 | load | load | h1 | 8 | 62188 | 62180 | 3109.0 | 7 | 8 | 11.0 | ok ×62188 |
| u1b-normal-h1-none-c8 | none | base4 | i2-normal-supplement | 1 | load | load | h1 | 8 | 93763 | 93755 | 4687.8 | 7 | 8 | 7.4 | ok ×93763 |
| u1b-normal-h1-none-c8 | none | base4 | i2-normal-supplement | 2 | load | load | h1 | 8 | 86028 | 86020 | 4301.0 | 8 | 8 | 7.4 | ok ×86028 |
| u1b-normal-h1-none-c8 | none | base4 | i2-normal-supplement | 3 | load | load | h1 | 8 | 83049 | 83041 | 4152.1 | 8 | 8 | 7.7 | ok ×83049 |
| u1b-normal-h1-none-c8 | none | base4 | i2-normal-supplement | 4 | load | load | h1 | 8 | 84374 | 84366 | 4218.3 | 8 | 8 | 7.7 | ok ×84374 |
| u1b-normal-h1-none-c8 | none | base4 | i2-normal-supplement | 5 | load | load | h1 | 8 | 64409 | 64401 | 3220.1 | 8 | 8 | 12.1 | ok ×64409 |
| u1b-normal-h1-none-c8 | none | cand | i2-normal-supplement | 1 | load | load | h1 | 8 | 87197 | 87190 | 4359.5 | 8 | 7 | 7.6 | ok ×87197 |
| u1b-normal-h1-none-c8 | none | cand | i2-normal-supplement | 2 | load | load | h1 | 8 | 80487 | 80479 | 4023.9 | 8 | 8 | 8.8 | ok ×80487 |
| u1b-normal-h1-none-c8 | none | cand | i2-normal-supplement | 3 | load | load | h1 | 8 | 73556 | 73548 | 3677.4 | 8 | 8 | 8.9 | ok ×73556 |
| u1b-normal-h1-none-c8 | none | cand | i2-normal-supplement | 4 | load | load | h1 | 8 | 63438 | 63430 | 3171.5 | 8 | 8 | 10.8 | ok ×63438 |
| u1b-normal-h1-none-c8 | none | cand | i2-normal-supplement | 5 | load | load | h1 | 8 | 69383 | 69375 | 3468.8 | 8 | 8 | 10.2 | ok ×69383 |
| u1b-normal-h1-none-c8 | none | main | i2-normal-supplement | 1 | load | load | h1 | 8 | 105288 | 105280 | 5264.0 | 7 | 8 | 6.7 | ok ×105288 |
| u1b-normal-h1-none-c8 | none | main | i2-normal-supplement | 2 | load | load | h1 | 8 | 88000 | 87992 | 4399.6 | 8 | 8 | 8.8 | ok ×88000 |
| u1b-normal-h1-none-c8 | none | main | i2-normal-supplement | 3 | load | load | h1 | 8 | 70641 | 70633 | 3531.7 | 8 | 8 | 8.9 | ok ×70641 |
| u1b-normal-h1-none-c8 | none | main | i2-normal-supplement | 4 | load | load | h1 | 8 | 85263 | 85255 | 4262.8 | 8 | 8 | 7.5 | ok ×85263 |
| u1b-normal-h1-none-c8 | none | main | i2-normal-supplement | 5 | load | load | h1 | 8 | 65525 | 65517 | 3275.8 | 8 | 8 | 10.5 | ok ×65525 |
| u2-normal-h2 | none | base | i2-normal-supplement | 1 | load | load | h2 | 16 | 34623 | 34607 | 1730.3 | 14 | 16 | 21.8 | ok ×34623 |
| u2-normal-h2 | none | base | i2-normal-supplement | 2 | load | load | h2 | 16 | 34199 | 34183 | 1709.2 | 14 | 16 | 28.1 | ok ×34199 |
| u2-normal-h2 | none | base | i2-normal-supplement | 3 | load | load | h2 | 16 | 40092 | 40076 | 2003.8 | 15 | 16 | 18.1 | ok ×40092 |
| u2-normal-h2 | none | base4 | i2-normal-supplement | 1 | load | load | h2 | 16 | 26036 | 26020 | 1301.0 | 16 | 16 | 28.5 | ok ×26036 |
| u2-normal-h2 | none | base4 | i2-normal-supplement | 2 | load | load | h2 | 16 | 37512 | 37496 | 1874.8 | 14 | 16 | 18.0 | ok ×37512 |
| u2-normal-h2 | none | base4 | i2-normal-supplement | 3 | load | load | h2 | 16 | 36886 | 36871 | 1843.5 | 15 | 15 | 18.1 | ok ×36886 |
| u2-normal-h2 | none | cand | i2-normal-supplement | 1 | load | load | h2 | 16 | 43179 | 43163 | 2158.2 | 16 | 16 | 17.3 | ok ×43179 |
| u2-normal-h2 | none | cand | i2-normal-supplement | 2 | load | load | h2 | 16 | 38763 | 38747 | 1937.3 | 16 | 16 | 17.1 | ok ×38763 |
| u2-normal-h2 | none | cand | i2-normal-supplement | 3 | load | load | h2 | 16 | 37469 | 37453 | 1872.7 | 16 | 16 | 19.4 | ok ×37469 |
| u2-normal-h2 | none | main | i2-normal-supplement | 1 | load | load | h2 | 16 | 33203 | 33187 | 1659.3 | 16 | 16 | 22.3 | ok ×33203 |
| u2-normal-h2 | none | main | i2-normal-supplement | 2 | load | load | h2 | 16 | 37916 | 37900 | 1895.0 | 16 | 16 | 21.5 | ok ×37916 |
| u2-normal-h2 | none | main | i2-normal-supplement | 3 | load | load | h2 | 16 | 37026 | 37010 | 1850.5 | 16 | 16 | 18.4 | ok ×37026 |
| u2-normal-h2 | wasm | base | i2-normal-supplement | 1 | load | load | h2 | 2 | 8476 | 8474 | 423.7 | 2 | 2 | 12.4 | ok ×8476 |
| u2-normal-h2 | wasm | base | i2-normal-supplement | 2 | load | load | h2 | 2 | 10562 | 10560 | 528.0 | 2 | 2 | 10.5 | ok ×10562 |
| u2-normal-h2 | wasm | base | i2-normal-supplement | 3 | load | load | h2 | 2 | 6595 | 6593 | 329.6 | 2 | 2 | 15.7 | ok ×6595 |
| u2-normal-h2 | wasm | base4 | i2-normal-supplement | 1 | load | load | h2 | 2 | 10937 | 10935 | 546.8 | 2 | 2 | 9.9 | ok ×10937 |
| u2-normal-h2 | wasm | base4 | i2-normal-supplement | 2 | load | load | h2 | 2 | 11113 | 11111 | 555.5 | 2 | 2 | 10.4 | ok ×11113 |
| u2-normal-h2 | wasm | base4 | i2-normal-supplement | 3 | load | load | h2 | 2 | 7561 | 7559 | 377.9 | 2 | 2 | 14.0 | ok ×7561 |
| u2-normal-h2 | wasm | cand | i2-normal-supplement | 1 | load | load | h2 | 2 | 7431 | 7429 | 371.4 | 2 | 2 | 15.0 | ok ×7431 |
| u2-normal-h2 | wasm | cand | i2-normal-supplement | 2 | load | load | h2 | 2 | 11257 | 11255 | 562.8 | 2 | 2 | 10.1 | ok ×11257 |
| u2-normal-h2 | wasm | cand | i2-normal-supplement | 3 | load | load | h2 | 2 | 5791 | 5789 | 289.4 | 2 | 2 | 17.2 | ok ×5791 |
| u2-normal-h2 | wasm | main | i2-normal-supplement | 1 | load | load | h2 | 2 | 9221 | 9219 | 460.9 | 2 | 2 | 11.9 | ok ×9221 |
| u2-normal-h2 | wasm | main | i2-normal-supplement | 2 | load | load | h2 | 2 | 10353 | 10351 | 517.5 | 2 | 2 | 10.4 | ok ×10353 |
| u2-normal-h2 | wasm | main | i2-normal-supplement | 3 | load | load | h2 | 2 | 5666 | 5664 | 283.2 | 2 | 2 | 19.9 | ok ×5666 |

## Failed, invalid or inconclusive runs

None.
