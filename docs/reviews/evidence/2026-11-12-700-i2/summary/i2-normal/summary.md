# Inspection assessment summary

Plan `inspection-assessment-plan/v3`, profile `qualification`. Runs: i2-normal.

> an I5 result shows that the instrument captured a runtime; it is not A1-A18 acceptance. Throughput verdicts between the ADR baseline and post-I1 main are informational because I1 has no request-path enforcement

## Targets

| Label | Adapter | SHA-256 | Go | Revision | Modified |
| --- | --- | --- | --- | --- | --- |
| base | baseline-858590dc | `520febcbad5669da0ea8ad98e29f0bb78304817511cd7ed63bfb48e9d63ea52f` | go1.27.3 | `858590dc7c430d24cd33b096a81ca6051591b427` | false |
| cand | current | `3edee005bc08754283e77c8b6f1688204d24927c2662697f2f82a1eccbf54e73` | go1.27.4 | `` |  |
| main | current | `65a0f8b9d42909b048fe3fe00ecda7dbf17ae28320bdef28bd82a1ea2f0add37` | go1.27.4 | `` |  |

## Cells

Goodput counts verified responses only. Spread is (max − min) / median over every repetition.

| Scenario | Variant | Target | Rule | n | instrument-valid | correct | median rps | min | max | spread | CPU fractions | peak cgroup MiB |
| --- | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- | --- |
| u1-normal-h1 | both | base | throughput | 3 | 3 | 3 | 9.0 | 8.1 | 9.9 | 0.211 | 0.50 0.50 0.50 | 202.7 205.9 197.9 |
| u1-normal-h1 | both | cand | throughput | 3 | 3 | 3 | 9.8 | 8.1 | 9.8 | 0.174 | 0.50 0.50 0.50 | 218.1 195.8 197.2 |
| u1-normal-h1 | both | main | throughput | 3 | 3 | 3 | 9.9 | 7.4 | 10.0 | 0.263 | 0.51 0.50 0.50 | 215.1 210.8 210.1 |
| u1-normal-h1 | none | base | throughput | 3 | 3 | 3 | 6614.6 | 6096.9 | 6629.1 | 0.080 | 0.63 0.62 0.63 | 23.8 24.0 23.9 |
| u1-normal-h1 | none | cand | throughput | 3 | 3 | 3 | 5698.2 | 4855.4 | 5962.6 | 0.194 | 0.63 0.62 0.63 | 23.3 24.0 23.4 |
| u1-normal-h1 | none | main | throughput | 3 | 3 | 3 | 4256.9 | 3549.8 | 4529.1 | 0.230 | 0.65 0.64 0.64 | 23.1 23.1 23.3 |
| u1-normal-h1 | waf | base | throughput | 3 | 3 | 3 | 9.3 | 9.1 | 9.8 | 0.075 | 0.50 0.50 0.50 | 115.5 112.7 110.0 |
| u1-normal-h1 | waf | cand | throughput | 3 | 3 | 3 | 9.8 | 7.8 | 10.1 | 0.234 | 0.51 0.50 0.51 | 116.4 112.6 108.0 |
| u1-normal-h1 | waf | main | throughput | 3 | 3 | 3 | 10.2 | 9.7 | 10.3 | 0.064 | 0.50 0.50 0.50 | 116.6 116.4 115.5 |
| u1-normal-h1 | wasm | base | throughput | 3 | 3 | 3 | 604.0 | 462.2 | 619.1 | 0.260 | 0.64 0.63 0.63 | 212.8 216.0 204.4 |
| u1-normal-h1 | wasm | cand | throughput | 3 | 3 | 3 | 499.1 | 418.8 | 560.8 | 0.285 | 0.64 0.63 0.64 | 211.7 207.9 199.7 |
| u1-normal-h1 | wasm | main | throughput | 3 | 3 | 3 | 538.7 | 408.0 | 607.5 | 0.370 | 0.64 0.63 0.63 | 207.1 209.3 210.9 |
| u10-normal-h3 | both | base | throughput | 3 | 3 | 3 | 9.8 | 9.6 | 9.9 | 0.036 | 0.50 0.50 0.50 | 198.5 198.3 200.2 |
| u10-normal-h3 | both | cand | throughput | 3 | 3 | 3 | 9.8 | 8.0 | 10.0 | 0.204 | 0.50 0.50 0.51 | 213.8 199.1 201.7 |
| u10-normal-h3 | both | main | throughput | 3 | 3 | 3 | 9.7 | 9.3 | 9.8 | 0.052 | 0.50 0.50 0.50 | 201.5 201.0 198.9 |
| u10-normal-h3 | none | base | throughput | 3 | 3 | 3 | 2910.4 | 2876.4 | 3026.7 | 0.052 | 0.63 0.63 0.62 | 26.4 26.6 26.3 |
| u10-normal-h3 | none | cand | throughput | 3 | 3 | 3 | 2530.3 | 2277.6 | 2563.3 | 0.113 | 0.64 0.64 0.63 | 26.5 26.3 26.0 |
| u10-normal-h3 | none | main | throughput | 3 | 3 | 3 | 2044.6 | 1777.2 | 2132.1 | 0.174 | 0.65 0.66 0.65 | 25.4 25.4 26.1 |
| u10-normal-h3 | wasm | base | throughput | 3 | 3 | 3 | 476.8 | 465.9 | 476.8 | 0.023 | 0.61 0.62 0.62 | 210.4 207.6 206.2 |
| u10-normal-h3 | wasm | cand | throughput | 3 | 3 | 3 | 450.9 | 410.8 | 462.1 | 0.114 | 0.62 0.62 0.61 | 209.0 207.2 219.5 |
| u10-normal-h3 | wasm | main | throughput | 3 | 3 | 3 | 484.9 | 469.9 | 502.9 | 0.068 | 0.61 0.61 0.61 | 204.7 207.4 211.7 |
| u1b-normal-h1-none-c8 | none | base | throughput | 5 | 5 | 5 | 4785.2 | 3949.6 | 6071.4 | 0.443 | 0.60 0.60 0.60 0.61 0.61 | 22.8 23.0 22.6 22.2 22.2 |
| u1b-normal-h1-none-c8 | none | cand | throughput | 5 | 5 | 5 | 5255.1 | 4726.2 | 5358.1 | 0.120 | 0.59 0.60 0.61 0.61 0.61 | 22.6 22.5 22.1 23.5 22.9 |
| u1b-normal-h1-none-c8 | none | main | throughput | 5 | 5 | 5 | 4090.1 | 3778.8 | 5469.4 | 0.413 | 0.61 0.61 0.61 0.60 0.60 | 22.0 22.0 22.8 22.6 22.2 |
| u2-normal-h2 | both | base | throughput | 3 | 3 | 3 | 9.8 | 9.7 | 9.9 | 0.026 | 0.50 0.50 0.50 | 198.7 213.3 201.7 |
| u2-normal-h2 | both | cand | throughput | 3 | 3 | 3 | 9.9 | 9.8 | 9.9 | 0.005 | 0.50 0.51 0.50 | 201.6 202.1 198.7 |
| u2-normal-h2 | both | main | throughput | 3 | 3 | 3 | 9.9 | 9.8 | 9.9 | 0.020 | 0.51 0.50 0.51 | 200.8 197.0 204.7 |
| u2-normal-h2 | none | base | throughput | 3 | 3 | 3 | 2487.7 | 2194.2 | 2615.0 | 0.169 | 0.62 0.62 0.62 | 24.9 25.5 25.8 |
| u2-normal-h2 | none | cand | throughput | 3 | 3 | 3 | 2382.1 | 2334.1 | 2440.1 | 0.045 | 0.62 0.62 0.63 | 24.4 25.3 25.1 |
| u2-normal-h2 | none | main | throughput | 3 | 3 | 3 | 2300.8 | 2275.4 | 2313.5 | 0.017 | 0.62 0.62 0.63 | 25.2 25.8 25.6 |
| u2-normal-h2 | waf | base | throughput | 3 | 3 | 3 | 9.4 | 9.4 | 9.5 | 0.011 | 0.50 0.50 0.51 | 109.3 109.8 108.5 |
| u2-normal-h2 | waf | cand | throughput | 3 | 3 | 3 | 10.2 | 10.1 | 10.2 | 0.020 | 0.50 0.50 0.50 | 108.0 108.1 110.5 |
| u2-normal-h2 | waf | main | throughput | 3 | 3 | 3 | 10.1 | 10.1 | 10.2 | 0.010 | 0.51 0.51 0.50 | 109.2 106.6 111.5 |
| u2-normal-h2 | wasm | base | throughput | 3 | 3 | 3 | 516.5 | 510.9 | 548.8 | 0.073 | 0.64 0.64 0.65 | 208.9 216.5 214.6 |
| u2-normal-h2 | wasm | cand | throughput | 3 | 3 | 3 | 517.5 | 458.9 | 547.9 | 0.172 | 0.63 0.65 0.64 | 210.5 203.9 216.1 |
| u2-normal-h2 | wasm | main | throughput | 3 | 3 | 3 | 455.8 | 432.9 | 555.2 | 0.268 | 0.65 0.64 0.65 | 205.8 221.1 212.2 |

## Comparisons (informational; not A13 acceptance)

| Scenario | Variant | Baseline | Candidate | Median regression | Verdict |
| --- | --- | --- | --- | ---: | --- |
| u1-normal-h1 | both | main | cand | 0.015 | inconclusive-noise |
| u1-normal-h1 | none | main | cand | -0.338 | inconclusive-noise |
| u1-normal-h1 | waf | main | cand | 0.030 | inconclusive-noise |
| u1-normal-h1 | wasm | main | cand | 0.074 | inconclusive-noise |
| u10-normal-h3 | both | main | cand | -0.009 | inconclusive-noise |
| u10-normal-h3 | none | main | cand | -0.237 | inconclusive-noise |
| u10-normal-h3 | wasm | main | cand | 0.070 | inconclusive-noise |
| u1b-normal-h1-none-c8 | none | main | cand | -0.284 | inconclusive-noise |
| u2-normal-h2 | both | main | cand | 0.000 | pass |
| u2-normal-h2 | none | main | cand | -0.034 | pass |
| u2-normal-h2 | waf | main | cand | -0.004 | pass |
| u2-normal-h2 | wasm | main | cand | -0.134 | inconclusive-noise |
| u1-normal-h1 | both | base | cand | -0.082 | inconclusive-noise |
| u1-normal-h1 | none | base | cand | 0.139 | fail-regression |
| u1-normal-h1 | waf | base | cand | -0.052 | inconclusive-noise |
| u1-normal-h1 | wasm | base | cand | 0.174 | fail-regression |
| u10-normal-h3 | both | base | cand | 0.000 | inconclusive-noise |
| u10-normal-h3 | none | base | cand | 0.131 | fail-regression |
| u10-normal-h3 | wasm | base | cand | 0.054 | inconclusive-noise |
| u1b-normal-h1-none-c8 | none | base | cand | -0.097 | inconclusive-noise |
| u2-normal-h2 | both | base | cand | -0.009 | pass |
| u2-normal-h2 | none | base | cand | 0.042 | inconclusive-noise |
| u2-normal-h2 | waf | base | cand | -0.079 | pass |
| u2-normal-h2 | wasm | base | cand | -0.001 | inconclusive-noise |
| u1-normal-h1 | both | base | main | -0.099 | inconclusive-noise |
| u1-normal-h1 | none | base | main | 0.356 | fail-regression |
| u1-normal-h1 | waf | base | main | -0.085 | pass |
| u1-normal-h1 | wasm | base | main | 0.108 | fail-regression |
| u10-normal-h3 | both | base | main | 0.010 | pass |
| u10-normal-h3 | none | base | main | 0.297 | fail-regression |
| u10-normal-h3 | wasm | base | main | -0.016 | pass |
| u1b-normal-h1-none-c8 | none | base | main | 0.145 | fail-regression |
| u2-normal-h2 | both | base | main | -0.009 | pass |
| u2-normal-h2 | none | base | main | 0.075 | inconclusive-noise |
| u2-normal-h2 | waf | base | main | -0.073 | pass |
| u2-normal-h2 | wasm | base | main | 0.118 | fail-regression |

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
| u1-normal-h1 | none | cand | i2-normal | 1 | load | load | h1 | 16 | 97122 | 97107 | 4855.4 | 16 | 15 | 12.4 | ok ×97122 |
| u1-normal-h1 | none | cand | i2-normal | 2 | load | load | h1 | 16 | 119268 | 119252 | 5962.6 | 16 | 16 | 10.2 | ok ×119268 |
| u1-normal-h1 | none | cand | i2-normal | 3 | load | load | h1 | 16 | 113981 | 113965 | 5698.2 | 15 | 16 | 10.4 | ok ×113981 |
| u1-normal-h1 | none | main | i2-normal | 1 | load | load | h1 | 16 | 71011 | 70995 | 3549.8 | 16 | 16 | 15.6 | ok ×71011 |
| u1-normal-h1 | none | main | i2-normal | 2 | load | load | h1 | 16 | 90599 | 90583 | 4529.1 | 16 | 16 | 12.6 | ok ×90599 |
| u1-normal-h1 | none | main | i2-normal | 3 | load | load | h1 | 16 | 85155 | 85139 | 4256.9 | 14 | 16 | 12.9 | ok ×85155 |
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
| u1-normal-h1 | wasm | cand | i2-normal | 1 | load | load | h1 | 2 | 8377 | 8375 | 418.8 | 2 | 2 | 13.2 | ok ×8377 |
| u1-normal-h1 | wasm | cand | i2-normal | 2 | load | load | h1 | 2 | 11217 | 11215 | 560.8 | 1 | 2 | 11.4 | ok ×11217 |
| u1-normal-h1 | wasm | cand | i2-normal | 3 | load | load | h1 | 2 | 9983 | 9981 | 499.1 | 2 | 2 | 11.2 | ok ×9983 |
| u1-normal-h1 | wasm | main | i2-normal | 1 | load | load | h1 | 2 | 8162 | 8160 | 408.0 | 2 | 2 | 14.9 | ok ×8162 |
| u1-normal-h1 | wasm | main | i2-normal | 2 | load | load | h1 | 2 | 10776 | 10774 | 538.7 | 2 | 2 | 10.3 | ok ×10776 |
| u1-normal-h1 | wasm | main | i2-normal | 3 | load | load | h1 | 2 | 12151 | 12149 | 607.5 | 2 | 2 | 9.0 | ok ×12151 |
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
| u10-normal-h3 | none | cand | i2-normal | 1 | load | load | h3 | 16 | 45567 | 45552 | 2277.6 | 16 | 15 | 15.7 | ok ×45567 |
| u10-normal-h3 | none | cand | i2-normal | 2 | load | load | h3 | 16 | 50623 | 50607 | 2530.3 | 16 | 16 | 15.3 | ok ×50623 |
| u10-normal-h3 | none | cand | i2-normal | 3 | load | load | h3 | 16 | 51283 | 51267 | 2563.3 | 16 | 16 | 14.1 | ok ×51283 |
| u10-normal-h3 | none | main | i2-normal | 1 | load | load | h3 | 16 | 35559 | 35544 | 1777.2 | 15 | 15 | 36.4 | ok ×35559 |
| u10-normal-h3 | none | main | i2-normal | 2 | load | load | h3 | 16 | 42657 | 42641 | 2132.1 | 16 | 16 | 16.5 | ok ×42657 |
| u10-normal-h3 | none | main | i2-normal | 3 | load | load | h3 | 16 | 40908 | 40892 | 2044.6 | 16 | 16 | 17.4 | ok ×40908 |
| u10-normal-h3 | wasm | base | i2-normal | 1 | load | load | h3 | 2 | 9320 | 9318 | 465.9 | 2 | 2 | 10.4 | ok ×9320 |
| u10-normal-h3 | wasm | base | i2-normal | 2 | load | load | h3 | 2 | 9538 | 9536 | 476.8 | 2 | 2 | 10.0 | ok ×9538 |
| u10-normal-h3 | wasm | base | i2-normal | 3 | load | load | h3 | 2 | 9538 | 9536 | 476.8 | 2 | 2 | 10.3 | ok ×9538 |
| u10-normal-h3 | wasm | cand | i2-normal | 1 | load | load | h3 | 2 | 9019 | 9017 | 450.9 | 2 | 2 | 10.3 | ok ×9019 |
| u10-normal-h3 | wasm | cand | i2-normal | 2 | load | load | h3 | 2 | 8217 | 8215 | 410.8 | 2 | 2 | 11.8 | ok ×8217 |
| u10-normal-h3 | wasm | cand | i2-normal | 3 | load | load | h3 | 2 | 9244 | 9242 | 462.1 | 2 | 2 | 10.7 | ok ×9244 |
| u10-normal-h3 | wasm | main | i2-normal | 1 | load | load | h3 | 2 | 9400 | 9398 | 469.9 | 2 | 2 | 10.3 | ok ×9400 |
| u10-normal-h3 | wasm | main | i2-normal | 2 | load | load | h3 | 2 | 10061 | 10059 | 502.9 | 2 | 2 | 9.7 | ok ×10061 |
| u10-normal-h3 | wasm | main | i2-normal | 3 | load | load | h3 | 2 | 9699 | 9697 | 484.9 | 1 | 2 | 10.2 | ok ×9699 |
| u1b-normal-h1-none-c8 | none | base | i2-normal | 1 | load | load | h1 | 8 | 95712 | 95704 | 4785.2 | 8 | 8 | 9.0 | ok ×95712 |
| u1b-normal-h1-none-c8 | none | base | i2-normal | 2 | load | load | h1 | 8 | 121434 | 121427 | 6071.4 | 8 | 7 | 6.1 | ok ×121434 |
| u1b-normal-h1-none-c8 | none | base | i2-normal | 3 | load | load | h1 | 8 | 116996 | 116988 | 5849.4 | 8 | 8 | 6.6 | ok ×116996 |
| u1b-normal-h1-none-c8 | none | base | i2-normal | 4 | load | load | h1 | 8 | 78999 | 78991 | 3949.6 | 7 | 8 | 8.6 | ok ×78999 |
| u1b-normal-h1-none-c8 | none | base | i2-normal | 5 | load | load | h1 | 8 | 92447 | 92439 | 4621.9 | 8 | 8 | 7.1 | ok ×92447 |
| u1b-normal-h1-none-c8 | none | cand | i2-normal | 1 | load | load | h1 | 8 | 107168 | 107161 | 5358.1 | 7 | 7 | 6.7 | ok ×107168 |
| u1b-normal-h1-none-c8 | none | cand | i2-normal | 2 | load | load | h1 | 8 | 105651 | 105643 | 5282.1 | 8 | 8 | 6.5 | ok ×105651 |
| u1b-normal-h1-none-c8 | none | cand | i2-normal | 3 | load | load | h1 | 8 | 94532 | 94524 | 4726.2 | 7 | 8 | 7.3 | ok ×94532 |
| u1b-normal-h1-none-c8 | none | cand | i2-normal | 4 | load | load | h1 | 8 | 97837 | 97829 | 4891.4 | 8 | 8 | 7.7 | ok ×97837 |
| u1b-normal-h1-none-c8 | none | cand | i2-normal | 5 | load | load | h1 | 8 | 105109 | 105101 | 5255.1 | 8 | 8 | 7.0 | ok ×105109 |
| u1b-normal-h1-none-c8 | none | main | i2-normal | 1 | load | load | h1 | 8 | 75584 | 75576 | 3778.8 | 8 | 8 | 8.7 | ok ×75584 |
| u1b-normal-h1-none-c8 | none | main | i2-normal | 2 | load | load | h1 | 8 | 81679 | 81672 | 4083.6 | 7 | 7 | 7.6 | ok ×81679 |
| u1b-normal-h1-none-c8 | none | main | i2-normal | 3 | load | load | h1 | 8 | 81808 | 81801 | 4090.1 | 8 | 7 | 7.7 | ok ×81808 |
| u1b-normal-h1-none-c8 | none | main | i2-normal | 4 | load | load | h1 | 8 | 99680 | 99672 | 4983.6 | 7 | 8 | 6.8 | ok ×99680 |
| u1b-normal-h1-none-c8 | none | main | i2-normal | 5 | load | load | h1 | 8 | 109397 | 109389 | 5469.4 | 8 | 8 | 6.3 | ok ×109397 |
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
| u2-normal-h2 | none | cand | i2-normal | 1 | load | load | h2 | 16 | 48818 | 48802 | 2440.1 | 16 | 16 | 13.8 | ok ×48818 |
| u2-normal-h2 | none | cand | i2-normal | 2 | load | load | h2 | 16 | 47658 | 47642 | 2382.1 | 16 | 16 | 14.4 | ok ×47658 |
| u2-normal-h2 | none | cand | i2-normal | 3 | load | load | h2 | 16 | 46697 | 46681 | 2334.1 | 16 | 16 | 14.6 | ok ×46697 |
| u2-normal-h2 | none | main | i2-normal | 1 | load | load | h2 | 16 | 46286 | 46270 | 2313.5 | 16 | 16 | 15.1 | ok ×46286 |
| u2-normal-h2 | none | main | i2-normal | 2 | load | load | h2 | 16 | 46031 | 46015 | 2300.8 | 16 | 16 | 15.8 | ok ×46031 |
| u2-normal-h2 | none | main | i2-normal | 3 | load | load | h2 | 16 | 45524 | 45508 | 2275.4 | 16 | 16 | 14.9 | ok ×45524 |
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
| u2-normal-h2 | wasm | cand | i2-normal | 1 | load | load | h2 | 2 | 10352 | 10350 | 517.5 | 2 | 2 | 10.0 | ok ×10352 |
| u2-normal-h2 | wasm | cand | i2-normal | 2 | load | load | h2 | 2 | 9180 | 9178 | 458.9 | 2 | 2 | 11.2 | ok ×9180 |
| u2-normal-h2 | wasm | cand | i2-normal | 3 | load | load | h2 | 2 | 10960 | 10958 | 547.9 | 2 | 2 | 9.6 | ok ×10960 |
| u2-normal-h2 | wasm | main | i2-normal | 1 | load | load | h2 | 2 | 8661 | 8659 | 432.9 | 2 | 2 | 11.8 | ok ×8661 |
| u2-normal-h2 | wasm | main | i2-normal | 2 | load | load | h2 | 2 | 11106 | 11104 | 555.2 | 2 | 2 | 9.7 | ok ×11106 |
| u2-normal-h2 | wasm | main | i2-normal | 3 | load | load | h2 | 2 | 9117 | 9115 | 455.8 | 2 | 2 | 11.7 | ok ×9117 |

## Failed, invalid or inconclusive runs

None.
