# Inspection assessment summary

Plan `inspection-assessment-plan/v3`, profile `qualification`. Runs: i2-normal, i2-normal-supplement.

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
| u1-normal-h1 | both | base | throughput | 3 | 3 | 3 | 9.0 | 8.1 | 9.9 | 0.211 | 0.50 0.50 0.50 | 202.7 205.9 197.9 |
| u1-normal-h1 | both | cand | throughput | 3 | 3 | 3 | 9.8 | 8.1 | 9.8 | 0.174 | 0.50 0.50 0.50 | 218.1 195.8 197.2 |
| u1-normal-h1 | both | main | throughput | 3 | 3 | 3 | 9.9 | 7.4 | 10.0 | 0.263 | 0.51 0.50 0.50 | 215.1 210.8 210.1 |
| u1-normal-h1 | none | base | throughput | 6 | 6 | 6 | 6032.1 | 2322.6 | 6629.1 | 0.714 | 0.63 0.62 0.63 0.69 0.63 0.64 | 23.8 24.0 23.9 23.4 23.3 23.4 |
| u1-normal-h1 | none | base4 | throughput | 3 | 3 | 3 | 4817.4 | 1798.7 | 4864.5 | 0.636 | 0.70 0.64 0.64 | 41.2 39.9 41.6 |
| u1-normal-h1 | none | cand | throughput | 6 | 6 | 6 | 5630.8 | 3702.2 | 5962.6 | 0.401 | 0.63 0.62 0.63 0.65 0.63 0.63 | 23.3 24.0 23.4 23.7 23.7 23.5 |
| u1-normal-h1 | none | main | throughput | 6 | 6 | 6 | 4393.1 | 3054.6 | 5547.4 | 0.567 | 0.65 0.64 0.64 0.65 0.61 0.63 | 23.1 23.1 23.3 23.5 24.0 23.5 |
| u1-normal-h1 | waf | base | throughput | 3 | 3 | 3 | 9.3 | 9.1 | 9.8 | 0.075 | 0.50 0.50 0.50 | 115.5 112.7 110.0 |
| u1-normal-h1 | waf | cand | throughput | 3 | 3 | 3 | 9.8 | 7.8 | 10.1 | 0.234 | 0.51 0.50 0.51 | 116.4 112.6 108.0 |
| u1-normal-h1 | waf | main | throughput | 3 | 3 | 3 | 10.2 | 9.7 | 10.3 | 0.064 | 0.50 0.50 0.50 | 116.6 116.4 115.5 |
| u1-normal-h1 | wasm | base | throughput | 6 | 6 | 6 | 482.5 | 225.2 | 619.1 | 0.816 | 0.64 0.63 0.63 0.69 0.65 0.63 | 212.8 216.0 204.4 205.6 211.4 212.9 |
| u1-normal-h1 | wasm | base4 | throughput | 3 | 3 | 3 | 516.5 | 382.6 | 554.8 | 0.333 | 0.65 0.64 0.64 | 215.7 221.1 223.9 |
| u1-normal-h1 | wasm | cand | throughput | 6 | 6 | 6 | 463.6 | 340.4 | 560.8 | 0.475 | 0.64 0.63 0.64 0.65 0.64 0.66 | 211.7 207.9 199.7 217.7 210.3 213.0 |
| u1-normal-h1 | wasm | main | throughput | 6 | 6 | 6 | 482.1 | 377.0 | 607.5 | 0.478 | 0.64 0.63 0.63 0.64 0.64 0.65 | 207.1 209.3 210.9 217.1 213.5 210.6 |
| u10-normal-h3 | both | base | throughput | 3 | 3 | 3 | 9.8 | 9.6 | 9.9 | 0.036 | 0.50 0.50 0.50 | 198.5 198.3 200.2 |
| u10-normal-h3 | both | cand | throughput | 3 | 3 | 3 | 9.8 | 8.0 | 10.0 | 0.204 | 0.50 0.50 0.51 | 213.8 199.1 201.7 |
| u10-normal-h3 | both | main | throughput | 3 | 3 | 3 | 9.7 | 9.3 | 9.8 | 0.052 | 0.50 0.50 0.50 | 201.5 201.0 198.9 |
| u10-normal-h3 | none | base | throughput | 6 | 6 | 6 | 2783.0 | 1650.0 | 3026.7 | 0.495 | 0.63 0.63 0.62 0.63 0.63 0.65 | 26.4 26.6 26.3 26.2 26.3 25.7 |
| u10-normal-h3 | none | base4 | throughput | 3 | 3 | 3 | 1945.5 | 1925.5 | 2221.6 | 0.152 | 0.64 0.66 0.65 | 41.9 42.6 44.3 |
| u10-normal-h3 | none | cand | throughput | 6 | 6 | 6 | 2533.0 | 2058.2 | 2682.7 | 0.247 | 0.64 0.64 0.63 0.63 0.63 0.65 | 26.5 26.3 26.0 26.8 26.2 25.7 |
| u10-normal-h3 | none | main | throughput | 6 | 6 | 6 | 2088.3 | 1777.2 | 2690.9 | 0.438 | 0.65 0.66 0.65 0.64 0.63 0.65 | 25.4 25.4 26.1 27.1 26.7 25.8 |
| u10-normal-h3 | wasm | base | throughput | 6 | 6 | 6 | 447.7 | 287.4 | 476.8 | 0.423 | 0.61 0.62 0.62 0.63 0.62 0.64 | 210.4 207.6 206.2 211.5 215.6 205.9 |
| u10-normal-h3 | wasm | base4 | throughput | 3 | 3 | 3 | 359.1 | 357.1 | 500.0 | 0.398 | 0.63 0.61 0.63 | 226.8 229.9 220.8 |
| u10-normal-h3 | wasm | cand | throughput | 6 | 6 | 6 | 412.4 | 303.9 | 462.1 | 0.384 | 0.62 0.62 0.61 0.62 0.63 0.64 | 209.0 207.2 219.5 214.5 207.5 206.4 |
| u10-normal-h3 | wasm | main | throughput | 6 | 6 | 6 | 454.8 | 296.9 | 502.9 | 0.453 | 0.61 0.61 0.61 0.62 0.64 0.63 | 204.7 207.4 211.7 210.0 206.4 204.2 |
| u1b-normal-h1-none-c8 | none | base | throughput | 10 | 10 | 10 | 4440.3 | 3109.0 | 6071.4 | 0.667 | 0.60 0.60 0.60 0.61 0.61 0.61 0.59 0.61 0.61 0.61 | 22.8 23.0 22.6 22.2 22.2 21.9 22.1 22.8 21.8 21.6 |
| u1b-normal-h1-none-c8 | none | base4 | throughput | 5 | 5 | 5 | 4218.3 | 3220.1 | 4687.8 | 0.348 | 0.60 0.61 0.61 0.61 0.62 | 38.7 37.9 38.4 38.9 41.2 |
| u1b-normal-h1-none-c8 | none | cand | throughput | 10 | 10 | 10 | 4542.9 | 3171.5 | 5358.1 | 0.481 | 0.59 0.60 0.61 0.61 0.61 0.61 0.61 0.61 0.63 0.61 | 22.6 22.5 22.1 23.5 22.9 23.0 22.3 21.3 21.8 22.2 |
| u1b-normal-h1-none-c8 | none | main | throughput | 10 | 10 | 10 | 4176.4 | 3275.8 | 5469.4 | 0.525 | 0.61 0.61 0.61 0.60 0.60 0.60 0.61 0.61 0.61 0.62 | 22.0 22.0 22.8 22.6 22.2 22.6 22.2 21.5 21.6 22.1 |
| u2-normal-h2 | both | base | throughput | 3 | 3 | 3 | 9.8 | 9.7 | 9.9 | 0.026 | 0.50 0.50 0.50 | 198.7 213.3 201.7 |
| u2-normal-h2 | both | cand | throughput | 3 | 3 | 3 | 9.9 | 9.8 | 9.9 | 0.005 | 0.50 0.51 0.50 | 201.6 202.1 198.7 |
| u2-normal-h2 | both | main | throughput | 3 | 3 | 3 | 9.9 | 9.8 | 9.9 | 0.020 | 0.51 0.50 0.51 | 200.8 197.0 204.7 |
| u2-normal-h2 | none | base | throughput | 6 | 6 | 6 | 2099.0 | 1709.2 | 2615.0 | 0.432 | 0.62 0.62 0.62 0.64 0.64 0.63 | 24.9 25.5 25.8 25.3 24.8 25.1 |
| u2-normal-h2 | none | base4 | throughput | 3 | 3 | 3 | 1843.5 | 1301.0 | 1874.8 | 0.311 | 0.66 0.64 0.63 | 42.8 43.4 41.5 |
| u2-normal-h2 | none | cand | throughput | 6 | 6 | 6 | 2246.1 | 1872.7 | 2440.1 | 0.253 | 0.62 0.62 0.63 0.63 0.63 0.64 | 24.4 25.3 25.1 24.7 25.2 25.1 |
| u2-normal-h2 | none | main | throughput | 6 | 6 | 6 | 2085.2 | 1659.3 | 2313.5 | 0.314 | 0.62 0.62 0.63 0.63 0.64 0.64 | 25.2 25.8 25.6 24.9 24.5 24.5 |
| u2-normal-h2 | waf | base | throughput | 3 | 3 | 3 | 9.4 | 9.4 | 9.5 | 0.011 | 0.50 0.50 0.51 | 109.3 109.8 108.5 |
| u2-normal-h2 | waf | cand | throughput | 3 | 3 | 3 | 10.2 | 10.1 | 10.2 | 0.020 | 0.50 0.50 0.50 | 108.0 108.1 110.5 |
| u2-normal-h2 | waf | main | throughput | 3 | 3 | 3 | 10.1 | 10.1 | 10.2 | 0.010 | 0.51 0.51 0.50 | 109.2 106.6 111.5 |
| u2-normal-h2 | wasm | base | throughput | 6 | 6 | 6 | 513.7 | 329.6 | 548.8 | 0.427 | 0.64 0.64 0.65 0.65 0.65 0.66 | 208.9 216.5 214.6 205.4 220.2 207.5 |
| u2-normal-h2 | wasm | base4 | throughput | 3 | 3 | 3 | 546.8 | 377.9 | 555.5 | 0.325 | 0.64 0.64 0.65 | 222.9 224.3 223.0 |
| u2-normal-h2 | wasm | cand | throughput | 6 | 6 | 6 | 488.2 | 289.4 | 562.8 | 0.560 | 0.63 0.65 0.64 0.65 0.64 0.67 | 210.5 203.9 216.1 203.1 209.0 203.7 |
| u2-normal-h2 | wasm | main | throughput | 6 | 6 | 6 | 458.4 | 283.2 | 555.2 | 0.593 | 0.65 0.64 0.65 0.65 0.65 0.66 | 205.8 221.1 212.2 205.9 216.6 199.1 |

## Comparisons (informational; not A13 acceptance)

| Scenario | Variant | Baseline | Candidate | Median regression | Verdict |
| --- | --- | --- | --- | ---: | --- |
| u1-normal-h1 | both | main | cand | 0.015 | inconclusive-noise |
| u1-normal-h1 | none | main | cand | -0.281 | inconclusive-noise |
| u1-normal-h1 | waf | main | cand | 0.030 | inconclusive-noise |
| u1-normal-h1 | wasm | main | cand | 0.039 | inconclusive-noise |
| u10-normal-h3 | both | main | cand | -0.009 | inconclusive-noise |
| u10-normal-h3 | none | main | cand | -0.212 | inconclusive-noise |
| u10-normal-h3 | wasm | main | cand | 0.093 | inconclusive-noise |
| u1b-normal-h1-none-c8 | none | main | cand | -0.087 | inconclusive-noise |
| u2-normal-h2 | both | main | cand | 0.000 | pass |
| u2-normal-h2 | none | main | cand | -0.076 | inconclusive-noise |
| u2-normal-h2 | waf | main | cand | -0.004 | pass |
| u2-normal-h2 | wasm | main | cand | -0.064 | inconclusive-noise |
| u1-normal-h1 | both | base | cand | -0.082 | inconclusive-noise |
| u1-normal-h1 | none | base | cand | 0.067 | inconclusive-noise |
| u1-normal-h1 | waf | base | cand | -0.052 | inconclusive-noise |
| u1-normal-h1 | wasm | base | cand | 0.039 | inconclusive-noise |
| u10-normal-h3 | both | base | cand | 0.000 | inconclusive-noise |
| u10-normal-h3 | none | base | cand | 0.090 | inconclusive-noise |
| u10-normal-h3 | wasm | base | cand | 0.079 | inconclusive-noise |
| u1b-normal-h1-none-c8 | none | base | cand | -0.022 | inconclusive-noise |
| u2-normal-h2 | both | base | cand | -0.009 | pass |
| u2-normal-h2 | none | base | cand | -0.069 | inconclusive-noise |
| u2-normal-h2 | waf | base | cand | -0.079 | pass |
| u2-normal-h2 | wasm | base | cand | 0.050 | inconclusive-noise |
| u1-normal-h1 | both | base | main | -0.099 | inconclusive-noise |
| u1-normal-h1 | none | base | main | 0.272 | fail-regression |
| u1-normal-h1 | waf | base | main | -0.085 | pass |
| u1-normal-h1 | wasm | base | main | 0.001 | inconclusive-noise |
| u10-normal-h3 | both | base | main | 0.010 | pass |
| u10-normal-h3 | none | base | main | 0.250 | fail-regression |
| u10-normal-h3 | wasm | base | main | -0.015 | inconclusive-noise |
| u1b-normal-h1-none-c8 | none | base | main | 0.059 | inconclusive-noise |
| u2-normal-h2 | both | base | main | -0.009 | pass |
| u2-normal-h2 | none | base | main | 0.007 | inconclusive-noise |
| u2-normal-h2 | waf | base | main | -0.073 | pass |
| u2-normal-h2 | wasm | base | main | 0.108 | fail-regression |

## Phases and classes

One row per repetition, phase and workload class. Goodput counts verified responses that started and finished in the window; requests crossing in from warm-up and those outstanding at the window end are excluded from it.

| Scenario | Variant | Target | Run | Rep | Phase | Class | Proto | C | Attempts | Verified in window | Goodput rps | Crossed in | Outstanding | p99 ms | Outcomes |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| u1-normal-h1 | both | base | i2-normal | 1 | load | load | h1 | 1 | 162 | 161 | 8.1 | 1 | 1 | 164.8 | ok ×162 |
| u1-normal-h1 | both | base | i2-normal | 2 | load | load | h1 | 1 | 181 | 180 | 9.0 | 1 | 1 | 142.3 | ok ×181 |
| u1-normal-h1 | both | base | i2-normal | 3 | load | load | h1 | 1 | 200 | 199 | 9.9 | 1 | 1 | 127.3 | ok ×200 |
| u1-normal-h1 | both | cand | i2-normal | 1 | load | load | h1 | 1 | 163 | 162 | 8.1 | 1 | 1 | 152.0 | ok ×163 |
| u1-normal-h1 | both | cand | i2-normal | 2 | load | load | h1 | 1 | 196 | 195 | 9.8 | 1 | 1 | 127.1 | ok ×196 |
| u1-normal-h1 | both | cand | i2-normal | 3 | load | load | h1 | 1 | 197 | 196 | 9.8 | 1 | 1 | 129.8 | ok ×197 |
| u1-normal-h1 | both | main | i2-normal | 1 | load | load | h1 | 1 | 149 | 148 | 7.4 | 1 | 1 | 178.2 | ok ×149 |
| u1-normal-h1 | both | main | i2-normal | 2 | load | load | h1 | 1 | 199 | 198 | 9.9 | 1 | 1 | 128.7 | ok ×199 |
| u1-normal-h1 | both | main | i2-normal | 3 | load | load | h1 | 1 | 201 | 200 | 10.0 | 1 | 1 | 120.4 | ok ×201 |
| u1-normal-h1 | none | base | i2-normal | 1 | load | load | h1 | 16 | 121955 | 121939 | 6096.9 | 16 | 16 | 10.7 | ok ×121955 |
| u1-normal-h1 | none | base | i2-normal | 2 | load | load | h1 | 16 | 132596 | 132581 | 6629.1 | 16 | 15 | 9.8 | ok ×132596 |
| u1-normal-h1 | none | base | i2-normal | 3 | load | load | h1 | 16 | 132307 | 132291 | 6614.6 | 16 | 16 | 9.9 | ok ×132307 |
| u1-normal-h1 | none | base | i2-normal-supplement | 1 | load | load | h1 | 16 | 46468 | 46452 | 2322.6 | 15 | 16 | 23.9 | ok ×46468 |
| u1-normal-h1 | none | base | i2-normal-supplement | 2 | load | load | h1 | 16 | 119363 | 119347 | 5967.4 | 16 | 16 | 10.3 | ok ×119363 |
| u1-normal-h1 | none | base | i2-normal-supplement | 3 | load | load | h1 | 16 | 99498 | 99482 | 4974.1 | 16 | 16 | 12.9 | ok ×99498 |
| u1-normal-h1 | none | base4 | i2-normal-supplement | 1 | load | load | h1 | 16 | 35990 | 35974 | 1798.7 | 16 | 16 | 26.8 | ok ×35990 |
| u1-normal-h1 | none | base4 | i2-normal-supplement | 2 | load | load | h1 | 16 | 96363 | 96347 | 4817.4 | 16 | 16 | 13.8 | ok ×96363 |
| u1-normal-h1 | none | base4 | i2-normal-supplement | 3 | load | load | h1 | 16 | 97306 | 97290 | 4864.5 | 16 | 16 | 13.1 | ok ×97306 |
| u1-normal-h1 | none | cand | i2-normal | 1 | load | load | h1 | 16 | 97122 | 97107 | 4855.4 | 16 | 15 | 12.4 | ok ×97122 |
| u1-normal-h1 | none | cand | i2-normal | 2 | load | load | h1 | 16 | 119268 | 119252 | 5962.6 | 16 | 16 | 10.2 | ok ×119268 |
| u1-normal-h1 | none | cand | i2-normal | 3 | load | load | h1 | 16 | 113981 | 113965 | 5698.2 | 15 | 16 | 10.4 | ok ×113981 |
| u1-normal-h1 | none | cand | i2-normal-supplement | 1 | load | load | h1 | 16 | 74059 | 74043 | 3702.2 | 16 | 16 | 15.6 | ok ×74059 |
| u1-normal-h1 | none | cand | i2-normal-supplement | 2 | load | load | h1 | 16 | 114183 | 114167 | 5708.4 | 14 | 16 | 11.3 | ok ×114183 |
| u1-normal-h1 | none | cand | i2-normal-supplement | 3 | load | load | h1 | 16 | 111281 | 111266 | 5563.3 | 16 | 15 | 11.4 | ok ×111281 |
| u1-normal-h1 | none | main | i2-normal | 1 | load | load | h1 | 16 | 71011 | 70995 | 3549.8 | 16 | 16 | 15.6 | ok ×71011 |
| u1-normal-h1 | none | main | i2-normal | 2 | load | load | h1 | 16 | 90599 | 90583 | 4529.1 | 16 | 16 | 12.6 | ok ×90599 |
| u1-normal-h1 | none | main | i2-normal | 3 | load | load | h1 | 16 | 85155 | 85139 | 4256.9 | 14 | 16 | 12.9 | ok ×85155 |
| u1-normal-h1 | none | main | i2-normal-supplement | 1 | load | load | h1 | 16 | 61107 | 61091 | 3054.6 | 15 | 16 | 19.5 | ok ×61107 |
| u1-normal-h1 | none | main | i2-normal-supplement | 2 | load | load | h1 | 16 | 110963 | 110947 | 5547.4 | 16 | 16 | 10.9 | ok ×110963 |
| u1-normal-h1 | none | main | i2-normal-supplement | 3 | load | load | h1 | 16 | 91787 | 91771 | 4588.6 | 16 | 16 | 13.1 | ok ×91787 |
| u1-normal-h1 | waf | base | i2-normal | 1 | load | load | h1 | 1 | 188 | 187 | 9.3 | 1 | 1 | 138.9 | ok ×188 |
| u1-normal-h1 | waf | base | i2-normal | 2 | load | load | h1 | 1 | 183 | 182 | 9.1 | 1 | 1 | 148.6 | ok ×183 |
| u1-normal-h1 | waf | base | i2-normal | 3 | load | load | h1 | 1 | 197 | 196 | 9.8 | 1 | 1 | 130.8 | ok ×197 |
| u1-normal-h1 | waf | cand | i2-normal | 1 | load | load | h1 | 1 | 156 | 155 | 7.8 | 1 | 1 | 160.8 | ok ×156 |
| u1-normal-h1 | waf | cand | i2-normal | 2 | load | load | h1 | 1 | 202 | 201 | 10.1 | 1 | 1 | 132.5 | ok ×202 |
| u1-normal-h1 | waf | cand | i2-normal | 3 | load | load | h1 | 1 | 198 | 197 | 9.8 | 1 | 1 | 173.6 | ok ×198 |
| u1-normal-h1 | waf | main | i2-normal | 1 | load | load | h1 | 1 | 194 | 193 | 9.7 | 1 | 1 | 130.4 | ok ×194 |
| u1-normal-h1 | waf | main | i2-normal | 2 | load | load | h1 | 1 | 204 | 203 | 10.2 | 1 | 1 | 122.5 | ok ×204 |
| u1-normal-h1 | waf | main | i2-normal | 3 | load | load | h1 | 1 | 207 | 206 | 10.3 | 1 | 1 | 117.7 | ok ×207 |
| u1-normal-h1 | wasm | base | i2-normal | 1 | load | load | h1 | 2 | 9247 | 9245 | 462.2 | 2 | 2 | 11.8 | ok ×9247 |
| u1-normal-h1 | wasm | base | i2-normal | 2 | load | load | h1 | 2 | 12083 | 12081 | 604.0 | 2 | 2 | 9.1 | ok ×12083 |
| u1-normal-h1 | wasm | base | i2-normal | 3 | load | load | h1 | 2 | 12384 | 12382 | 619.1 | 2 | 2 | 8.7 | ok ×12384 |
| u1-normal-h1 | wasm | base | i2-normal-supplement | 1 | load | load | h1 | 2 | 4505 | 4503 | 225.2 | 2 | 2 | 24.9 | ok ×4505 |
| u1-normal-h1 | wasm | base | i2-normal-supplement | 2 | load | load | h1 | 2 | 9011 | 9010 | 450.5 | 2 | 1 | 12.5 | ok ×9011 |
| u1-normal-h1 | wasm | base | i2-normal-supplement | 3 | load | load | h1 | 2 | 10057 | 10055 | 502.8 | 2 | 2 | 11.0 | ok ×10057 |
| u1-normal-h1 | wasm | base4 | i2-normal-supplement | 1 | load | load | h1 | 2 | 7653 | 7651 | 382.6 | 2 | 2 | 14.8 | ok ×7653 |
| u1-normal-h1 | wasm | base4 | i2-normal-supplement | 2 | load | load | h1 | 2 | 10332 | 10330 | 516.5 | 2 | 2 | 12.0 | ok ×10332 |
| u1-normal-h1 | wasm | base4 | i2-normal-supplement | 3 | load | load | h1 | 2 | 11098 | 11096 | 554.8 | 2 | 2 | 10.0 | ok ×11098 |
| u1-normal-h1 | wasm | cand | i2-normal | 1 | load | load | h1 | 2 | 8377 | 8375 | 418.8 | 2 | 2 | 13.2 | ok ×8377 |
| u1-normal-h1 | wasm | cand | i2-normal | 2 | load | load | h1 | 2 | 11217 | 11215 | 560.8 | 1 | 2 | 11.4 | ok ×11217 |
| u1-normal-h1 | wasm | cand | i2-normal | 3 | load | load | h1 | 2 | 9983 | 9981 | 499.1 | 2 | 2 | 11.2 | ok ×9983 |
| u1-normal-h1 | wasm | cand | i2-normal-supplement | 1 | load | load | h1 | 2 | 8563 | 8561 | 428.1 | 2 | 2 | 12.7 | ok ×8563 |
| u1-normal-h1 | wasm | cand | i2-normal-supplement | 2 | load | load | h1 | 2 | 10384 | 10382 | 519.1 | 2 | 2 | 11.4 | ok ×10384 |
| u1-normal-h1 | wasm | cand | i2-normal-supplement | 3 | load | load | h1 | 2 | 6809 | 6807 | 340.4 | 2 | 2 | 15.5 | ok ×6809 |
| u1-normal-h1 | wasm | main | i2-normal | 1 | load | load | h1 | 2 | 8162 | 8160 | 408.0 | 2 | 2 | 14.9 | ok ×8162 |
| u1-normal-h1 | wasm | main | i2-normal | 2 | load | load | h1 | 2 | 10776 | 10774 | 538.7 | 2 | 2 | 10.3 | ok ×10776 |
| u1-normal-h1 | wasm | main | i2-normal | 3 | load | load | h1 | 2 | 12151 | 12149 | 607.5 | 2 | 2 | 9.0 | ok ×12151 |
| u1-normal-h1 | wasm | main | i2-normal-supplement | 1 | load | load | h1 | 2 | 8981 | 8979 | 448.9 | 2 | 2 | 12.0 | ok ×8981 |
| u1-normal-h1 | wasm | main | i2-normal-supplement | 2 | load | load | h1 | 2 | 10308 | 10306 | 515.3 | 2 | 2 | 11.5 | ok ×10308 |
| u1-normal-h1 | wasm | main | i2-normal-supplement | 3 | load | load | h1 | 2 | 7542 | 7540 | 377.0 | 2 | 2 | 15.6 | ok ×7542 |
| u10-normal-h3 | both | base | i2-normal | 1 | load | load | h3 | 1 | 197 | 196 | 9.8 | 1 | 1 | 132.3 | ok ×197 |
| u10-normal-h3 | both | base | i2-normal | 2 | load | load | h3 | 1 | 199 | 198 | 9.9 | 1 | 1 | 115.6 | ok ×199 |
| u10-normal-h3 | both | base | i2-normal | 3 | load | load | h3 | 1 | 192 | 191 | 9.6 | 1 | 1 | 128.9 | ok ×192 |
| u10-normal-h3 | both | cand | i2-normal | 1 | load | load | h3 | 1 | 201 | 200 | 10.0 | 1 | 1 | 115.1 | ok ×201 |
| u10-normal-h3 | both | cand | i2-normal | 2 | load | load | h3 | 1 | 197 | 196 | 9.8 | 1 | 1 | 119.4 | ok ×197 |
| u10-normal-h3 | both | cand | i2-normal | 3 | load | load | h3 | 1 | 161 | 160 | 8.0 | 1 | 1 | 170.0 | ok ×161 |
| u10-normal-h3 | both | main | i2-normal | 1 | load | load | h3 | 1 | 195 | 194 | 9.7 | 1 | 1 | 135.1 | ok ×195 |
| u10-normal-h3 | both | main | i2-normal | 2 | load | load | h3 | 1 | 198 | 197 | 9.8 | 1 | 1 | 120.6 | ok ×198 |
| u10-normal-h3 | both | main | i2-normal | 3 | load | load | h3 | 1 | 188 | 187 | 9.3 | 1 | 1 | 161.3 | ok ×188 |
| u10-normal-h3 | none | base | i2-normal | 1 | load | load | h3 | 16 | 60549 | 60533 | 3026.7 | 16 | 16 | 12.4 | ok ×60549 |
| u10-normal-h3 | none | base | i2-normal | 2 | load | load | h3 | 16 | 58224 | 58208 | 2910.4 | 16 | 16 | 13.3 | ok ×58224 |
| u10-normal-h3 | none | base | i2-normal | 3 | load | load | h3 | 16 | 57544 | 57528 | 2876.4 | 16 | 16 | 13.2 | ok ×57544 |
| u10-normal-h3 | none | base | i2-normal-supplement | 1 | load | load | h3 | 16 | 51220 | 51204 | 2560.2 | 16 | 16 | 15.1 | ok ×51220 |
| u10-normal-h3 | none | base | i2-normal-supplement | 2 | load | load | h3 | 16 | 53809 | 53793 | 2689.7 | 16 | 16 | 14.5 | ok ×53809 |
| u10-normal-h3 | none | base | i2-normal-supplement | 3 | load | load | h3 | 16 | 33016 | 33000 | 1650.0 | 16 | 16 | 31.1 | ok ×33016 |
| u10-normal-h3 | none | base4 | i2-normal-supplement | 1 | load | load | h3 | 16 | 44448 | 44432 | 2221.6 | 16 | 16 | 16.1 | ok ×44448 |
| u10-normal-h3 | none | base4 | i2-normal-supplement | 2 | load | load | h3 | 16 | 38527 | 38511 | 1925.5 | 15 | 16 | 18.1 | ok ×38527 |
| u10-normal-h3 | none | base4 | i2-normal-supplement | 3 | load | load | h3 | 16 | 38926 | 38910 | 1945.5 | 16 | 16 | 19.9 | ok ×38926 |
| u10-normal-h3 | none | cand | i2-normal | 1 | load | load | h3 | 16 | 45567 | 45552 | 2277.6 | 16 | 15 | 15.7 | ok ×45567 |
| u10-normal-h3 | none | cand | i2-normal | 2 | load | load | h3 | 16 | 50623 | 50607 | 2530.3 | 16 | 16 | 15.3 | ok ×50623 |
| u10-normal-h3 | none | cand | i2-normal | 3 | load | load | h3 | 16 | 51283 | 51267 | 2563.3 | 16 | 16 | 14.1 | ok ×51283 |
| u10-normal-h3 | none | cand | i2-normal-supplement | 1 | load | load | h3 | 16 | 50729 | 50713 | 2535.7 | 16 | 16 | 14.9 | ok ×50729 |
| u10-normal-h3 | none | cand | i2-normal-supplement | 2 | load | load | h3 | 16 | 53669 | 53653 | 2682.7 | 16 | 16 | 13.7 | ok ×53669 |
| u10-normal-h3 | none | cand | i2-normal-supplement | 3 | load | load | h3 | 16 | 41181 | 41165 | 2058.2 | 16 | 16 | 16.6 | ok ×41181 |
| u10-normal-h3 | none | main | i2-normal | 1 | load | load | h3 | 16 | 35559 | 35544 | 1777.2 | 15 | 15 | 36.4 | ok ×35559 |
| u10-normal-h3 | none | main | i2-normal | 2 | load | load | h3 | 16 | 42657 | 42641 | 2132.1 | 16 | 16 | 16.5 | ok ×42657 |
| u10-normal-h3 | none | main | i2-normal | 3 | load | load | h3 | 16 | 40908 | 40892 | 2044.6 | 16 | 16 | 17.4 | ok ×40908 |
| u10-normal-h3 | none | main | i2-normal-supplement | 1 | load | load | h3 | 16 | 44993 | 44977 | 2248.8 | 16 | 16 | 16.1 | ok ×44993 |
| u10-normal-h3 | none | main | i2-normal-supplement | 2 | load | load | h3 | 16 | 53834 | 53818 | 2690.9 | 16 | 16 | 14.2 | ok ×53834 |
| u10-normal-h3 | none | main | i2-normal-supplement | 3 | load | load | h3 | 16 | 39338 | 39322 | 1966.1 | 16 | 16 | 19.5 | ok ×39338 |
| u10-normal-h3 | wasm | base | i2-normal | 1 | load | load | h3 | 2 | 9320 | 9318 | 465.9 | 2 | 2 | 10.4 | ok ×9320 |
| u10-normal-h3 | wasm | base | i2-normal | 2 | load | load | h3 | 2 | 9538 | 9536 | 476.8 | 2 | 2 | 10.0 | ok ×9538 |
| u10-normal-h3 | wasm | base | i2-normal | 3 | load | load | h3 | 2 | 9538 | 9536 | 476.8 | 2 | 2 | 10.3 | ok ×9538 |
| u10-normal-h3 | wasm | base | i2-normal-supplement | 1 | load | load | h3 | 2 | 7592 | 7590 | 379.5 | 2 | 2 | 13.3 | ok ×7592 |
| u10-normal-h3 | wasm | base | i2-normal-supplement | 2 | load | load | h3 | 2 | 8592 | 8590 | 429.5 | 2 | 2 | 11.8 | ok ×8592 |
| u10-normal-h3 | wasm | base | i2-normal-supplement | 3 | load | load | h3 | 2 | 5751 | 5749 | 287.4 | 2 | 2 | 17.8 | ok ×5751 |
| u10-normal-h3 | wasm | base4 | i2-normal-supplement | 1 | load | load | h3 | 2 | 7144 | 7142 | 357.1 | 2 | 2 | 14.7 | ok ×7144 |
| u10-normal-h3 | wasm | base4 | i2-normal-supplement | 2 | load | load | h3 | 2 | 10002 | 10000 | 500.0 | 2 | 2 | 10.0 | ok ×10002 |
| u10-normal-h3 | wasm | base4 | i2-normal-supplement | 3 | load | load | h3 | 2 | 7184 | 7182 | 359.1 | 2 | 2 | 14.4 | ok ×7184 |
| u10-normal-h3 | wasm | cand | i2-normal | 1 | load | load | h3 | 2 | 9019 | 9017 | 450.9 | 2 | 2 | 10.3 | ok ×9019 |
| u10-normal-h3 | wasm | cand | i2-normal | 2 | load | load | h3 | 2 | 8217 | 8215 | 410.8 | 2 | 2 | 11.8 | ok ×8217 |
| u10-normal-h3 | wasm | cand | i2-normal | 3 | load | load | h3 | 2 | 9244 | 9242 | 462.1 | 2 | 2 | 10.7 | ok ×9244 |
| u10-normal-h3 | wasm | cand | i2-normal-supplement | 1 | load | load | h3 | 2 | 8283 | 8281 | 414.1 | 2 | 2 | 13.3 | ok ×8283 |
| u10-normal-h3 | wasm | cand | i2-normal-supplement | 2 | load | load | h3 | 2 | 7406 | 7404 | 370.2 | 2 | 2 | 13.6 | ok ×7406 |
| u10-normal-h3 | wasm | cand | i2-normal-supplement | 3 | load | load | h3 | 2 | 6080 | 6078 | 303.9 | 2 | 2 | 15.9 | ok ×6080 |
| u10-normal-h3 | wasm | main | i2-normal | 1 | load | load | h3 | 2 | 9400 | 9398 | 469.9 | 2 | 2 | 10.3 | ok ×9400 |
| u10-normal-h3 | wasm | main | i2-normal | 2 | load | load | h3 | 2 | 10061 | 10059 | 502.9 | 2 | 2 | 9.7 | ok ×10061 |
| u10-normal-h3 | wasm | main | i2-normal | 3 | load | load | h3 | 2 | 9699 | 9697 | 484.9 | 1 | 2 | 10.2 | ok ×9699 |
| u10-normal-h3 | wasm | main | i2-normal-supplement | 1 | load | load | h3 | 2 | 8797 | 8795 | 439.8 | 2 | 2 | 11.1 | ok ×8797 |
| u10-normal-h3 | wasm | main | i2-normal-supplement | 2 | load | load | h3 | 2 | 5939 | 5937 | 296.9 | 2 | 2 | 17.3 | ok ×5939 |
| u10-normal-h3 | wasm | main | i2-normal-supplement | 3 | load | load | h3 | 2 | 6105 | 6103 | 305.1 | 2 | 2 | 16.7 | ok ×6105 |
| u1b-normal-h1-none-c8 | none | base | i2-normal | 1 | load | load | h1 | 8 | 95712 | 95704 | 4785.2 | 8 | 8 | 9.0 | ok ×95712 |
| u1b-normal-h1-none-c8 | none | base | i2-normal | 2 | load | load | h1 | 8 | 121434 | 121427 | 6071.4 | 8 | 7 | 6.1 | ok ×121434 |
| u1b-normal-h1-none-c8 | none | base | i2-normal | 3 | load | load | h1 | 8 | 116996 | 116988 | 5849.4 | 8 | 8 | 6.6 | ok ×116996 |
| u1b-normal-h1-none-c8 | none | base | i2-normal | 4 | load | load | h1 | 8 | 78999 | 78991 | 3949.6 | 7 | 8 | 8.6 | ok ×78999 |
| u1b-normal-h1-none-c8 | none | base | i2-normal | 5 | load | load | h1 | 8 | 92447 | 92439 | 4621.9 | 8 | 8 | 7.1 | ok ×92447 |
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
| u1b-normal-h1-none-c8 | none | cand | i2-normal | 1 | load | load | h1 | 8 | 107168 | 107161 | 5358.1 | 7 | 7 | 6.7 | ok ×107168 |
| u1b-normal-h1-none-c8 | none | cand | i2-normal | 2 | load | load | h1 | 8 | 105651 | 105643 | 5282.1 | 8 | 8 | 6.5 | ok ×105651 |
| u1b-normal-h1-none-c8 | none | cand | i2-normal | 3 | load | load | h1 | 8 | 94532 | 94524 | 4726.2 | 7 | 8 | 7.3 | ok ×94532 |
| u1b-normal-h1-none-c8 | none | cand | i2-normal | 4 | load | load | h1 | 8 | 97837 | 97829 | 4891.4 | 8 | 8 | 7.7 | ok ×97837 |
| u1b-normal-h1-none-c8 | none | cand | i2-normal | 5 | load | load | h1 | 8 | 105109 | 105101 | 5255.1 | 8 | 8 | 7.0 | ok ×105109 |
| u1b-normal-h1-none-c8 | none | cand | i2-normal-supplement | 1 | load | load | h1 | 8 | 87197 | 87190 | 4359.5 | 8 | 7 | 7.6 | ok ×87197 |
| u1b-normal-h1-none-c8 | none | cand | i2-normal-supplement | 2 | load | load | h1 | 8 | 80487 | 80479 | 4023.9 | 8 | 8 | 8.8 | ok ×80487 |
| u1b-normal-h1-none-c8 | none | cand | i2-normal-supplement | 3 | load | load | h1 | 8 | 73556 | 73548 | 3677.4 | 8 | 8 | 8.9 | ok ×73556 |
| u1b-normal-h1-none-c8 | none | cand | i2-normal-supplement | 4 | load | load | h1 | 8 | 63438 | 63430 | 3171.5 | 8 | 8 | 10.8 | ok ×63438 |
| u1b-normal-h1-none-c8 | none | cand | i2-normal-supplement | 5 | load | load | h1 | 8 | 69383 | 69375 | 3468.8 | 8 | 8 | 10.2 | ok ×69383 |
| u1b-normal-h1-none-c8 | none | main | i2-normal | 1 | load | load | h1 | 8 | 75584 | 75576 | 3778.8 | 8 | 8 | 8.7 | ok ×75584 |
| u1b-normal-h1-none-c8 | none | main | i2-normal | 2 | load | load | h1 | 8 | 81679 | 81672 | 4083.6 | 7 | 7 | 7.6 | ok ×81679 |
| u1b-normal-h1-none-c8 | none | main | i2-normal | 3 | load | load | h1 | 8 | 81808 | 81801 | 4090.1 | 8 | 7 | 7.7 | ok ×81808 |
| u1b-normal-h1-none-c8 | none | main | i2-normal | 4 | load | load | h1 | 8 | 99680 | 99672 | 4983.6 | 7 | 8 | 6.8 | ok ×99680 |
| u1b-normal-h1-none-c8 | none | main | i2-normal | 5 | load | load | h1 | 8 | 109397 | 109389 | 5469.4 | 8 | 8 | 6.3 | ok ×109397 |
| u1b-normal-h1-none-c8 | none | main | i2-normal-supplement | 1 | load | load | h1 | 8 | 105288 | 105280 | 5264.0 | 7 | 8 | 6.7 | ok ×105288 |
| u1b-normal-h1-none-c8 | none | main | i2-normal-supplement | 2 | load | load | h1 | 8 | 88000 | 87992 | 4399.6 | 8 | 8 | 8.8 | ok ×88000 |
| u1b-normal-h1-none-c8 | none | main | i2-normal-supplement | 3 | load | load | h1 | 8 | 70641 | 70633 | 3531.7 | 8 | 8 | 8.9 | ok ×70641 |
| u1b-normal-h1-none-c8 | none | main | i2-normal-supplement | 4 | load | load | h1 | 8 | 85263 | 85255 | 4262.8 | 8 | 8 | 7.5 | ok ×85263 |
| u1b-normal-h1-none-c8 | none | main | i2-normal-supplement | 5 | load | load | h1 | 8 | 65525 | 65517 | 3275.8 | 8 | 8 | 10.5 | ok ×65525 |
| u2-normal-h2 | both | base | i2-normal | 1 | load | load | h2 | 1 | 194 | 193 | 9.7 | 1 | 1 | 119.0 | ok ×194 |
| u2-normal-h2 | both | base | i2-normal | 2 | load | load | h2 | 1 | 197 | 196 | 9.8 | 1 | 1 | 125.1 | ok ×197 |
| u2-normal-h2 | both | base | i2-normal | 3 | load | load | h2 | 1 | 199 | 198 | 9.9 | 1 | 1 | 127.9 | ok ×199 |
| u2-normal-h2 | both | cand | i2-normal | 1 | load | load | h2 | 1 | 199 | 198 | 9.9 | 1 | 1 | 127.4 | ok ×199 |
| u2-normal-h2 | both | cand | i2-normal | 2 | load | load | h2 | 1 | 199 | 198 | 9.9 | 1 | 1 | 121.1 | ok ×199 |
| u2-normal-h2 | both | cand | i2-normal | 3 | load | load | h2 | 1 | 198 | 197 | 9.8 | 1 | 1 | 134.6 | ok ×198 |
| u2-normal-h2 | both | main | i2-normal | 1 | load | load | h2 | 1 | 199 | 198 | 9.9 | 1 | 1 | 122.7 | ok ×199 |
| u2-normal-h2 | both | main | i2-normal | 2 | load | load | h2 | 1 | 200 | 199 | 9.9 | 1 | 1 | 125.4 | ok ×200 |
| u2-normal-h2 | both | main | i2-normal | 3 | load | load | h2 | 1 | 196 | 195 | 9.8 | 1 | 1 | 124.2 | ok ×196 |
| u2-normal-h2 | none | base | i2-normal | 1 | load | load | h2 | 16 | 43899 | 43883 | 2194.2 | 16 | 16 | 15.6 | ok ×43899 |
| u2-normal-h2 | none | base | i2-normal | 2 | load | load | h2 | 16 | 49770 | 49754 | 2487.7 | 16 | 16 | 13.7 | ok ×49770 |
| u2-normal-h2 | none | base | i2-normal | 3 | load | load | h2 | 16 | 52316 | 52300 | 2615.0 | 16 | 16 | 13.3 | ok ×52316 |
| u2-normal-h2 | none | base | i2-normal-supplement | 1 | load | load | h2 | 16 | 34623 | 34607 | 1730.3 | 14 | 16 | 21.8 | ok ×34623 |
| u2-normal-h2 | none | base | i2-normal-supplement | 2 | load | load | h2 | 16 | 34199 | 34183 | 1709.2 | 14 | 16 | 28.1 | ok ×34199 |
| u2-normal-h2 | none | base | i2-normal-supplement | 3 | load | load | h2 | 16 | 40092 | 40076 | 2003.8 | 15 | 16 | 18.1 | ok ×40092 |
| u2-normal-h2 | none | base4 | i2-normal-supplement | 1 | load | load | h2 | 16 | 26036 | 26020 | 1301.0 | 16 | 16 | 28.5 | ok ×26036 |
| u2-normal-h2 | none | base4 | i2-normal-supplement | 2 | load | load | h2 | 16 | 37512 | 37496 | 1874.8 | 14 | 16 | 18.0 | ok ×37512 |
| u2-normal-h2 | none | base4 | i2-normal-supplement | 3 | load | load | h2 | 16 | 36886 | 36871 | 1843.5 | 15 | 15 | 18.1 | ok ×36886 |
| u2-normal-h2 | none | cand | i2-normal | 1 | load | load | h2 | 16 | 48818 | 48802 | 2440.1 | 16 | 16 | 13.8 | ok ×48818 |
| u2-normal-h2 | none | cand | i2-normal | 2 | load | load | h2 | 16 | 47658 | 47642 | 2382.1 | 16 | 16 | 14.4 | ok ×47658 |
| u2-normal-h2 | none | cand | i2-normal | 3 | load | load | h2 | 16 | 46697 | 46681 | 2334.1 | 16 | 16 | 14.6 | ok ×46697 |
| u2-normal-h2 | none | cand | i2-normal-supplement | 1 | load | load | h2 | 16 | 43179 | 43163 | 2158.2 | 16 | 16 | 17.3 | ok ×43179 |
| u2-normal-h2 | none | cand | i2-normal-supplement | 2 | load | load | h2 | 16 | 38763 | 38747 | 1937.3 | 16 | 16 | 17.1 | ok ×38763 |
| u2-normal-h2 | none | cand | i2-normal-supplement | 3 | load | load | h2 | 16 | 37469 | 37453 | 1872.7 | 16 | 16 | 19.4 | ok ×37469 |
| u2-normal-h2 | none | main | i2-normal | 1 | load | load | h2 | 16 | 46286 | 46270 | 2313.5 | 16 | 16 | 15.1 | ok ×46286 |
| u2-normal-h2 | none | main | i2-normal | 2 | load | load | h2 | 16 | 46031 | 46015 | 2300.8 | 16 | 16 | 15.8 | ok ×46031 |
| u2-normal-h2 | none | main | i2-normal | 3 | load | load | h2 | 16 | 45524 | 45508 | 2275.4 | 16 | 16 | 14.9 | ok ×45524 |
| u2-normal-h2 | none | main | i2-normal-supplement | 1 | load | load | h2 | 16 | 33203 | 33187 | 1659.3 | 16 | 16 | 22.3 | ok ×33203 |
| u2-normal-h2 | none | main | i2-normal-supplement | 2 | load | load | h2 | 16 | 37916 | 37900 | 1895.0 | 16 | 16 | 21.5 | ok ×37916 |
| u2-normal-h2 | none | main | i2-normal-supplement | 3 | load | load | h2 | 16 | 37026 | 37010 | 1850.5 | 16 | 16 | 18.4 | ok ×37026 |
| u2-normal-h2 | waf | base | i2-normal | 1 | load | load | h2 | 1 | 191 | 190 | 9.5 | 1 | 1 | 123.0 | ok ×191 |
| u2-normal-h2 | waf | base | i2-normal | 2 | load | load | h2 | 1 | 189 | 188 | 9.4 | 1 | 1 | 142.0 | ok ×189 |
| u2-normal-h2 | waf | base | i2-normal | 3 | load | load | h2 | 1 | 189 | 188 | 9.4 | 1 | 1 | 134.2 | ok ×189 |
| u2-normal-h2 | waf | cand | i2-normal | 1 | load | load | h2 | 1 | 204 | 203 | 10.2 | 1 | 1 | 120.2 | ok ×204 |
| u2-normal-h2 | waf | cand | i2-normal | 2 | load | load | h2 | 1 | 206 | 205 | 10.2 | 1 | 1 | 114.2 | ok ×206 |
| u2-normal-h2 | waf | cand | i2-normal | 3 | load | load | h2 | 1 | 202 | 201 | 10.1 | 1 | 1 | 126.5 | ok ×202 |
| u2-normal-h2 | waf | main | i2-normal | 1 | load | load | h2 | 1 | 203 | 202 | 10.1 | 1 | 1 | 120.3 | ok ×203 |
| u2-normal-h2 | waf | main | i2-normal | 2 | load | load | h2 | 1 | 202 | 201 | 10.1 | 1 | 1 | 117.0 | ok ×202 |
| u2-normal-h2 | waf | main | i2-normal | 3 | load | load | h2 | 1 | 204 | 203 | 10.2 | 1 | 1 | 119.5 | ok ×204 |
| u2-normal-h2 | wasm | base | i2-normal | 1 | load | load | h2 | 2 | 10219 | 10217 | 510.9 | 2 | 2 | 10.2 | ok ×10219 |
| u2-normal-h2 | wasm | base | i2-normal | 2 | load | load | h2 | 2 | 10977 | 10975 | 548.8 | 2 | 2 | 9.6 | ok ×10977 |
| u2-normal-h2 | wasm | base | i2-normal | 3 | load | load | h2 | 2 | 10332 | 10330 | 516.5 | 2 | 2 | 12.8 | ok ×10332 |
| u2-normal-h2 | wasm | base | i2-normal-supplement | 1 | load | load | h2 | 2 | 8476 | 8474 | 423.7 | 2 | 2 | 12.4 | ok ×8476 |
| u2-normal-h2 | wasm | base | i2-normal-supplement | 2 | load | load | h2 | 2 | 10562 | 10560 | 528.0 | 2 | 2 | 10.5 | ok ×10562 |
| u2-normal-h2 | wasm | base | i2-normal-supplement | 3 | load | load | h2 | 2 | 6595 | 6593 | 329.6 | 2 | 2 | 15.7 | ok ×6595 |
| u2-normal-h2 | wasm | base4 | i2-normal-supplement | 1 | load | load | h2 | 2 | 10937 | 10935 | 546.8 | 2 | 2 | 9.9 | ok ×10937 |
| u2-normal-h2 | wasm | base4 | i2-normal-supplement | 2 | load | load | h2 | 2 | 11113 | 11111 | 555.5 | 2 | 2 | 10.4 | ok ×11113 |
| u2-normal-h2 | wasm | base4 | i2-normal-supplement | 3 | load | load | h2 | 2 | 7561 | 7559 | 377.9 | 2 | 2 | 14.0 | ok ×7561 |
| u2-normal-h2 | wasm | cand | i2-normal | 1 | load | load | h2 | 2 | 10352 | 10350 | 517.5 | 2 | 2 | 10.0 | ok ×10352 |
| u2-normal-h2 | wasm | cand | i2-normal | 2 | load | load | h2 | 2 | 9180 | 9178 | 458.9 | 2 | 2 | 11.2 | ok ×9180 |
| u2-normal-h2 | wasm | cand | i2-normal | 3 | load | load | h2 | 2 | 10960 | 10958 | 547.9 | 2 | 2 | 9.6 | ok ×10960 |
| u2-normal-h2 | wasm | cand | i2-normal-supplement | 1 | load | load | h2 | 2 | 7431 | 7429 | 371.4 | 2 | 2 | 15.0 | ok ×7431 |
| u2-normal-h2 | wasm | cand | i2-normal-supplement | 2 | load | load | h2 | 2 | 11257 | 11255 | 562.8 | 2 | 2 | 10.1 | ok ×11257 |
| u2-normal-h2 | wasm | cand | i2-normal-supplement | 3 | load | load | h2 | 2 | 5791 | 5789 | 289.4 | 2 | 2 | 17.2 | ok ×5791 |
| u2-normal-h2 | wasm | main | i2-normal | 1 | load | load | h2 | 2 | 8661 | 8659 | 432.9 | 2 | 2 | 11.8 | ok ×8661 |
| u2-normal-h2 | wasm | main | i2-normal | 2 | load | load | h2 | 2 | 11106 | 11104 | 555.2 | 2 | 2 | 9.7 | ok ×11106 |
| u2-normal-h2 | wasm | main | i2-normal | 3 | load | load | h2 | 2 | 9117 | 9115 | 455.8 | 2 | 2 | 11.7 | ok ×9117 |
| u2-normal-h2 | wasm | main | i2-normal-supplement | 1 | load | load | h2 | 2 | 9221 | 9219 | 460.9 | 2 | 2 | 11.9 | ok ×9221 |
| u2-normal-h2 | wasm | main | i2-normal-supplement | 2 | load | load | h2 | 2 | 10353 | 10351 | 517.5 | 2 | 2 | 10.4 | ok ×10353 |
| u2-normal-h2 | wasm | main | i2-normal-supplement | 3 | load | load | h2 | 2 | 5666 | 5664 | 283.2 | 2 | 2 | 19.9 | ok ×5666 |

## Failed, invalid or inconclusive runs

None.
