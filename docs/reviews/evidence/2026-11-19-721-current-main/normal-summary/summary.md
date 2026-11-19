# Inspection assessment summary

Plan `inspection-assessment-plan/v4`, profile `qualification`. Runs: normal, u1b.

> an I5 result shows that the instrument captured a runtime; it is not A1-A18 acceptance. Throughput verdicts between the ADR baseline and post-I1 main are informational because I1 has no request-path enforcement

## Targets

| Label | Adapter | SHA-256 | Go | Revision | Modified |
| --- | --- | --- | --- | --- | --- |
| candidate | current | `c96daf7a91d04f46f99148dd21859f62f6576a45558b255748fe67184090a902` | go1.27.4 | `` |  |
| main | current | `cbc506c19b48a910342e4c4a07f6e3919f6ff524eeaff384a9b1ce95274da609` | go1.27.4 | `` |  |

## Cells

Goodput counts verified responses only. Spread is (max − min) / median over every repetition.

| Scenario | Variant | Target | Rule | n | instrument-valid | correct | median rps | min | max | spread | CPU fractions | peak cgroup MiB |
| --- | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- | --- |
| u1-normal-h1 | both | candidate | throughput | 3 | 3 | 3 | 8.4 | 7.1 | 8.8 | 0.195 | 0.50 0.51 0.50 | 208.4 208.7 206.5 |
| u1-normal-h1 | both | main | throughput | 3 | 3 | 3 | 8.0 | 7.8 | 9.5 | 0.208 | 0.51 0.50 0.50 | 199.5 200.4 200.0 |
| u1-normal-h1 | none | candidate | throughput | 3 | 3 | 3 | 3466.0 | 2735.1 | 3486.9 | 0.217 | 0.69 0.67 0.67 | 22.6 23.5 23.6 |
| u1-normal-h1 | none | main | throughput | 3 | 3 | 3 | 5219.6 | 4802.9 | 5239.6 | 0.084 | 0.66 0.64 0.66 | 23.3 23.9 23.7 |
| u1-normal-h1 | waf | candidate | throughput | 3 | 3 | 3 | 9.8 | 9.8 | 9.8 | 0.010 | 0.50 0.50 0.50 | 115.7 112.5 113.9 |
| u1-normal-h1 | waf | main | throughput | 3 | 3 | 3 | 8.0 | 7.3 | 8.4 | 0.144 | 0.51 0.51 0.50 | 109.5 110.1 112.8 |
| u1-normal-h1 | wasm | candidate | throughput | 3 | 3 | 3 | 294.2 | 281.2 | 369.4 | 0.300 | 0.68 0.68 0.65 | 215.1 207.9 217.4 |
| u1-normal-h1 | wasm | main | throughput | 3 | 3 | 3 | 378.1 | 344.9 | 466.9 | 0.323 | 0.67 0.66 0.64 | 205.4 202.9 213.2 |
| u10-normal-h3 | both | candidate | throughput | 3 | 3 | 3 | 9.3 | 7.5 | 9.4 | 0.204 | 0.51 0.51 0.50 | 215.6 202.2 207.1 |
| u10-normal-h3 | both | main | throughput | 3 | 3 | 3 | 5.8 | 5.8 | 7.0 | 0.224 | 0.51 0.51 0.51 | 199.7 200.3 218.3 |
| u10-normal-h3 | none | candidate | throughput | 3 | 3 | 3 | 1401.1 | 1023.0 | 1416.1 | 0.281 | 0.68 0.68 0.70 | 25.1 25.6 25.8 |
| u10-normal-h3 | none | main | throughput | 3 | 3 | 3 | 2342.6 | 2085.5 | 2349.4 | 0.113 | 0.65 0.65 0.65 | 26.1 25.9 26.5 |
| u10-normal-h3 | wasm | candidate | throughput | 3 | 3 | 3 | 423.7 | 328.2 | 424.7 | 0.228 | 0.62 0.62 0.64 | 216.6 207.2 205.9 |
| u10-normal-h3 | wasm | main | throughput | 3 | 3 | 3 | 354.4 | 336.6 | 364.4 | 0.078 | 0.63 0.63 0.64 | 207.4 214.1 211.1 |
| u1b-normal-h1-none-c8 | none | candidate | throughput | 5 | 5 | 5 | 4333.6 | 2733.3 | 4517.8 | 0.412 | 0.65 0.60 0.61 0.60 0.60 | 21.6 22.5 21.8 22.6 22.0 |
| u1b-normal-h1-none-c8 | none | main | throughput | 5 | 5 | 5 | 4315.9 | 3200.4 | 4785.1 | 0.367 | 0.62 0.61 0.60 0.64 0.61 | 22.6 22.6 22.3 22.0 22.1 |
| u2-normal-h2 | both | candidate | throughput | 3 | 3 | 3 | 9.2 | 7.3 | 9.6 | 0.243 | 0.51 0.51 0.51 | 201.0 208.1 210.1 |
| u2-normal-h2 | both | main | throughput | 3 | 3 | 3 | 7.8 | 6.8 | 8.4 | 0.217 | 0.51 0.51 0.51 | 196.4 199.0 202.8 |
| u2-normal-h2 | none | candidate | throughput | 3 | 3 | 3 | 1720.8 | 1588.2 | 1808.5 | 0.128 | 0.65 0.65 0.64 | 24.8 24.7 25.2 |
| u2-normal-h2 | none | main | throughput | 3 | 3 | 3 | 1791.6 | 1588.5 | 2278.6 | 0.385 | 0.63 0.65 0.64 | 25.2 25.2 25.1 |
| u2-normal-h2 | waf | candidate | throughput | 3 | 3 | 3 | 8.9 | 7.5 | 9.7 | 0.235 | 0.51 0.51 0.51 | 109.5 107.3 108.2 |
| u2-normal-h2 | waf | main | throughput | 3 | 3 | 3 | 7.5 | 7.5 | 8.4 | 0.133 | 0.51 0.51 0.51 | 109.6 108.4 109.6 |
| u2-normal-h2 | wasm | candidate | throughput | 3 | 3 | 3 | 419.4 | 294.1 | 484.2 | 0.453 | 0.65 0.65 0.67 | 210.9 209.7 208.1 |
| u2-normal-h2 | wasm | main | throughput | 3 | 3 | 3 | 379.7 | 368.9 | 506.4 | 0.362 | 0.66 0.65 0.66 | 208.6 211.3 211.9 |

## Comparisons (informational; not A13 acceptance)

| Scenario | Variant | Baseline | Candidate | Median regression | Verdict |
| --- | --- | --- | --- | ---: | --- |
| u1-normal-h1 | both | main | candidate | -0.062 | inconclusive-noise |
| u1-normal-h1 | none | main | candidate | 0.336 | fail-regression |
| u1-normal-h1 | waf | main | candidate | -0.224 | inconclusive-noise |
| u1-normal-h1 | wasm | main | candidate | 0.222 | fail-regression |
| u10-normal-h3 | both | main | candidate | -0.602 | inconclusive-noise |
| u10-normal-h3 | none | main | candidate | 0.402 | fail-regression |
| u10-normal-h3 | wasm | main | candidate | -0.194 | inconclusive-noise |
| u1b-normal-h1-none-c8 | none | main | candidate | -0.003 | inconclusive-noise |
| u2-normal-h2 | both | main | candidate | -0.177 | inconclusive-noise |
| u2-normal-h2 | none | main | candidate | 0.040 | inconclusive-noise |
| u2-normal-h2 | waf | main | candidate | -0.192 | inconclusive-noise |
| u2-normal-h2 | wasm | main | candidate | -0.104 | inconclusive-noise |

## Phases and classes

One row per repetition, phase and workload class. Goodput counts verified responses that started and finished in the window; requests crossing in from warm-up and those outstanding at the window end are excluded from it.

| Scenario | Variant | Target | Run | Rep | Phase | Class | Proto | C | Attempts | Verified in window | Goodput rps | Crossed in | Outstanding | p99 ms | Outcomes |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| u1-normal-h1 | both | candidate | normal | 1 | load | load | h1 | 1 | 170 | 169 | 8.4 | 1 | 1 | 148.0 | ok ×170 |
| u1-normal-h1 | both | candidate | normal | 2 | load | load | h1 | 1 | 143 | 142 | 7.1 | 1 | 1 | 196.7 | ok ×143 |
| u1-normal-h1 | both | candidate | normal | 3 | load | load | h1 | 1 | 176 | 175 | 8.8 | 1 | 1 | 150.5 | ok ×176 |
| u1-normal-h1 | both | main | normal | 1 | load | load | h1 | 1 | 160 | 159 | 8.0 | 1 | 1 | 161.9 | ok ×160 |
| u1-normal-h1 | both | main | normal | 2 | load | load | h1 | 1 | 158 | 157 | 7.8 | 1 | 1 | 149.8 | ok ×158 |
| u1-normal-h1 | both | main | normal | 3 | load | load | h1 | 1 | 191 | 190 | 9.5 | 1 | 1 | 138.3 | ok ×191 |
| u1-normal-h1 | none | candidate | normal | 1 | load | load | h1 | 16 | 54718 | 54702 | 2735.1 | 16 | 16 | 18.5 | ok ×54718 |
| u1-normal-h1 | none | candidate | normal | 2 | load | load | h1 | 16 | 69336 | 69320 | 3466.0 | 16 | 16 | 15.7 | ok ×69336 |
| u1-normal-h1 | none | candidate | normal | 3 | load | load | h1 | 16 | 69755 | 69739 | 3486.9 | 16 | 16 | 15.3 | ok ×69755 |
| u1-normal-h1 | none | main | normal | 1 | load | load | h1 | 16 | 96073 | 96057 | 4802.9 | 15 | 16 | 14.0 | ok ×96073 |
| u1-normal-h1 | none | main | normal | 2 | load | load | h1 | 16 | 104806 | 104791 | 5239.6 | 16 | 15 | 11.7 | ok ×104806 |
| u1-normal-h1 | none | main | normal | 3 | load | load | h1 | 16 | 104407 | 104392 | 5219.6 | 16 | 15 | 12.2 | ok ×104407 |
| u1-normal-h1 | waf | candidate | normal | 1 | load | load | h1 | 1 | 197 | 196 | 9.8 | 1 | 1 | 122.1 | ok ×197 |
| u1-normal-h1 | waf | candidate | normal | 2 | load | load | h1 | 1 | 196 | 195 | 9.8 | 1 | 1 | 127.2 | ok ×196 |
| u1-normal-h1 | waf | candidate | normal | 3 | load | load | h1 | 1 | 198 | 197 | 9.8 | 1 | 1 | 131.8 | ok ×198 |
| u1-normal-h1 | waf | main | normal | 1 | load | load | h1 | 1 | 161 | 160 | 8.0 | 1 | 1 | 193.8 | ok ×161 |
| u1-normal-h1 | waf | main | normal | 2 | load | load | h1 | 1 | 147 | 146 | 7.3 | 1 | 1 | 182.1 | ok ×147 |
| u1-normal-h1 | waf | main | normal | 3 | load | load | h1 | 1 | 170 | 169 | 8.4 | 1 | 1 | 161.1 | ok ×170 |
| u1-normal-h1 | wasm | candidate | normal | 1 | load | load | h1 | 2 | 5626 | 5624 | 281.2 | 2 | 2 | 19.8 | ok ×5626 |
| u1-normal-h1 | wasm | candidate | normal | 2 | load | load | h1 | 2 | 5886 | 5884 | 294.2 | 2 | 2 | 20.6 | ok ×5886 |
| u1-normal-h1 | wasm | candidate | normal | 3 | load | load | h1 | 2 | 7389 | 7387 | 369.4 | 2 | 2 | 15.5 | ok ×7389 |
| u1-normal-h1 | wasm | main | normal | 1 | load | load | h1 | 2 | 6899 | 6897 | 344.9 | 2 | 2 | 16.7 | ok ×6899 |
| u1-normal-h1 | wasm | main | normal | 2 | load | load | h1 | 2 | 7565 | 7563 | 378.1 | 2 | 2 | 14.7 | ok ×7565 |
| u1-normal-h1 | wasm | main | normal | 3 | load | load | h1 | 2 | 9341 | 9339 | 466.9 | 2 | 2 | 13.0 | ok ×9341 |
| u10-normal-h3 | both | candidate | normal | 1 | load | load | h3 | 1 | 151 | 150 | 7.5 | 1 | 1 | 172.0 | ok ×151 |
| u10-normal-h3 | both | candidate | normal | 2 | load | load | h3 | 1 | 187 | 186 | 9.3 | 1 | 1 | 136.3 | ok ×187 |
| u10-normal-h3 | both | candidate | normal | 3 | load | load | h3 | 1 | 189 | 188 | 9.4 | 1 | 1 | 144.0 | ok ×189 |
| u10-normal-h3 | both | main | normal | 1 | load | load | h3 | 1 | 116 | 115 | 5.8 | 1 | 1 | 197.8 | ok ×116 |
| u10-normal-h3 | both | main | normal | 2 | load | load | h3 | 1 | 142 | 141 | 7.0 | 1 | 1 | 173.9 | ok ×142 |
| u10-normal-h3 | both | main | normal | 3 | load | load | h3 | 1 | 117 | 116 | 5.8 | 1 | 1 | 213.3 | ok ×117 |
| u10-normal-h3 | none | candidate | normal | 1 | load | load | h3 | 16 | 28038 | 28022 | 1401.1 | 16 | 16 | 26.5 | ok ×28038 |
| u10-normal-h3 | none | candidate | normal | 2 | load | load | h3 | 16 | 28338 | 28322 | 1416.1 | 16 | 16 | 25.8 | ok ×28338 |
| u10-normal-h3 | none | candidate | normal | 3 | load | load | h3 | 16 | 20475 | 20460 | 1023.0 | 16 | 15 | 34.5 | ok ×20475 |
| u10-normal-h3 | none | main | normal | 1 | load | load | h3 | 16 | 47005 | 46989 | 2349.4 | 16 | 16 | 18.2 | ok ×47005 |
| u10-normal-h3 | none | main | normal | 2 | load | load | h3 | 16 | 46867 | 46851 | 2342.6 | 16 | 16 | 16.2 | ok ×46867 |
| u10-normal-h3 | none | main | normal | 3 | load | load | h3 | 16 | 41726 | 41710 | 2085.5 | 16 | 16 | 24.3 | ok ×41726 |
| u10-normal-h3 | wasm | candidate | normal | 1 | load | load | h3 | 2 | 8476 | 8474 | 423.7 | 2 | 2 | 11.0 | ok ×8476 |
| u10-normal-h3 | wasm | candidate | normal | 2 | load | load | h3 | 2 | 8496 | 8494 | 424.7 | 2 | 2 | 11.5 | ok ×8496 |
| u10-normal-h3 | wasm | candidate | normal | 3 | load | load | h3 | 2 | 6566 | 6564 | 328.2 | 2 | 2 | 17.2 | ok ×6566 |
| u10-normal-h3 | wasm | main | normal | 1 | load | load | h3 | 2 | 7290 | 7288 | 364.4 | 2 | 2 | 13.6 | ok ×7290 |
| u10-normal-h3 | wasm | main | normal | 2 | load | load | h3 | 2 | 7091 | 7089 | 354.4 | 2 | 2 | 13.9 | ok ×7091 |
| u10-normal-h3 | wasm | main | normal | 3 | load | load | h3 | 2 | 6734 | 6732 | 336.6 | 2 | 2 | 15.3 | ok ×6734 |
| u1b-normal-h1-none-c8 | none | candidate | u1b | 1 | load | load | h1 | 8 | 54674 | 54666 | 2733.3 | 8 | 8 | 11.1 | ok ×54674 |
| u1b-normal-h1-none-c8 | none | candidate | u1b | 2 | load | load | h1 | 8 | 89016 | 89008 | 4450.4 | 7 | 8 | 7.5 | ok ×89016 |
| u1b-normal-h1-none-c8 | none | candidate | u1b | 3 | load | load | h1 | 8 | 79949 | 79941 | 3997.1 | 8 | 8 | 8.8 | ok ×79949 |
| u1b-normal-h1-none-c8 | none | candidate | u1b | 4 | load | load | h1 | 8 | 90363 | 90355 | 4517.8 | 8 | 8 | 7.8 | ok ×90363 |
| u1b-normal-h1-none-c8 | none | candidate | u1b | 5 | load | load | h1 | 8 | 86678 | 86671 | 4333.6 | 6 | 7 | 7.4 | ok ×86678 |
| u1b-normal-h1-none-c8 | none | main | u1b | 1 | load | load | h1 | 8 | 86326 | 86318 | 4315.9 | 8 | 8 | 8.8 | ok ×86326 |
| u1b-normal-h1-none-c8 | none | main | u1b | 2 | load | load | h1 | 8 | 90918 | 90910 | 4545.5 | 8 | 8 | 8.5 | ok ×90918 |
| u1b-normal-h1-none-c8 | none | main | u1b | 3 | load | load | h1 | 8 | 95711 | 95703 | 4785.1 | 8 | 8 | 7.2 | ok ×95711 |
| u1b-normal-h1-none-c8 | none | main | u1b | 4 | load | load | h1 | 8 | 64017 | 64009 | 3200.4 | 8 | 8 | 11.7 | ok ×64017 |
| u1b-normal-h1-none-c8 | none | main | u1b | 5 | load | load | h1 | 8 | 78176 | 78168 | 3908.4 | 7 | 8 | 9.9 | ok ×78176 |
| u2-normal-h2 | both | candidate | normal | 1 | load | load | h2 | 1 | 147 | 146 | 7.3 | 1 | 1 | 165.0 | ok ×147 |
| u2-normal-h2 | both | candidate | normal | 2 | load | load | h2 | 1 | 186 | 185 | 9.2 | 1 | 1 | 135.0 | ok ×186 |
| u2-normal-h2 | both | candidate | normal | 3 | load | load | h2 | 1 | 192 | 191 | 9.6 | 1 | 1 | 136.6 | ok ×192 |
| u2-normal-h2 | both | main | normal | 1 | load | load | h2 | 1 | 158 | 157 | 7.8 | 1 | 1 | 156.4 | ok ×158 |
| u2-normal-h2 | both | main | normal | 2 | load | load | h2 | 1 | 136 | 135 | 6.8 | 1 | 1 | 177.5 | ok ×136 |
| u2-normal-h2 | both | main | normal | 3 | load | load | h2 | 1 | 170 | 169 | 8.4 | 1 | 1 | 148.4 | ok ×170 |
| u2-normal-h2 | none | candidate | normal | 1 | load | load | h2 | 16 | 31780 | 31764 | 1588.2 | 16 | 16 | 23.7 | ok ×31780 |
| u2-normal-h2 | none | candidate | normal | 2 | load | load | h2 | 16 | 36187 | 36171 | 1808.5 | 16 | 16 | 21.8 | ok ×36187 |
| u2-normal-h2 | none | candidate | normal | 3 | load | load | h2 | 16 | 34431 | 34415 | 1720.8 | 16 | 16 | 24.4 | ok ×34431 |
| u2-normal-h2 | none | main | normal | 1 | load | load | h2 | 16 | 45587 | 45571 | 2278.6 | 16 | 16 | 16.6 | ok ×45587 |
| u2-normal-h2 | none | main | normal | 2 | load | load | h2 | 16 | 31786 | 31770 | 1588.5 | 16 | 16 | 21.5 | ok ×31786 |
| u2-normal-h2 | none | main | normal | 3 | load | load | h2 | 16 | 35848 | 35832 | 1791.6 | 16 | 16 | 19.2 | ok ×35848 |
| u2-normal-h2 | waf | candidate | normal | 1 | load | load | h2 | 1 | 180 | 179 | 8.9 | 1 | 1 | 137.1 | ok ×180 |
| u2-normal-h2 | waf | candidate | normal | 2 | load | load | h2 | 1 | 152 | 151 | 7.5 | 1 | 1 | 172.2 | ok ×152 |
| u2-normal-h2 | waf | candidate | normal | 3 | load | load | h2 | 1 | 194 | 193 | 9.7 | 1 | 1 | 133.5 | ok ×194 |
| u2-normal-h2 | waf | main | normal | 1 | load | load | h2 | 1 | 150 | 149 | 7.5 | 1 | 1 | 167.9 | ok ×150 |
| u2-normal-h2 | waf | main | normal | 2 | load | load | h2 | 1 | 151 | 150 | 7.5 | 1 | 1 | 163.0 | ok ×151 |
| u2-normal-h2 | waf | main | normal | 3 | load | load | h2 | 1 | 170 | 169 | 8.4 | 1 | 1 | 152.6 | ok ×170 |
| u2-normal-h2 | wasm | candidate | normal | 1 | load | load | h2 | 2 | 8391 | 8389 | 419.4 | 2 | 2 | 13.2 | ok ×8391 |
| u2-normal-h2 | wasm | candidate | normal | 2 | load | load | h2 | 2 | 9687 | 9685 | 484.2 | 2 | 2 | 11.1 | ok ×9687 |
| u2-normal-h2 | wasm | candidate | normal | 3 | load | load | h2 | 2 | 5885 | 5883 | 294.1 | 2 | 2 | 20.1 | ok ×5885 |
| u2-normal-h2 | wasm | main | normal | 1 | load | load | h2 | 2 | 7380 | 7378 | 368.9 | 2 | 2 | 14.5 | ok ×7380 |
| u2-normal-h2 | wasm | main | normal | 2 | load | load | h2 | 2 | 7596 | 7594 | 379.7 | 2 | 2 | 14.5 | ok ×7596 |
| u2-normal-h2 | wasm | main | normal | 3 | load | load | h2 | 2 | 10130 | 10128 | 506.4 | 2 | 2 | 11.0 | ok ×10130 |

## Failed, invalid or inconclusive runs

None.
