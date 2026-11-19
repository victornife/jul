# Inspection assessment summary

Plan `inspection-assessment-plan/v3`, profile `qualification`. Runs: ia-a13-normal.

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
| u1-normal-h1 | both | base | throughput | 3 | 3 | 3 | 9.0 | 8.5 | 9.4 | 0.106 | 0.50 0.51 0.50 | 208.0 205.5 207.5 |
| u1-normal-h1 | both | main | throughput | 3 | 3 | 3 | 8.0 | 7.3 | 9.5 | 0.277 | 0.50 0.51 0.51 | 214.9 207.1 208.1 |
| u1-normal-h1 | none | base | throughput | 3 | 3 | 3 | 5167.2 | 5102.8 | 5346.8 | 0.047 | 0.64 0.63 0.64 | 23.5 23.8 23.3 |
| u1-normal-h1 | none | main | throughput | 3 | 3 | 3 | 3740.8 | 2242.4 | 5627.4 | 0.905 | 0.65 0.62 0.69 | 22.9 23.7 22.9 |
| u1-normal-h1 | waf | base | throughput | 3 | 3 | 3 | 7.5 | 7.4 | 9.6 | 0.295 | 0.51 0.50 0.51 | 111.7 110.3 114.4 |
| u1-normal-h1 | waf | main | throughput | 3 | 3 | 3 | 9.8 | 9.8 | 10.4 | 0.061 | 0.50 0.50 0.51 | 113.9 111.2 109.6 |
| u1-normal-h1 | wasm | base | throughput | 3 | 3 | 3 | 440.4 | 416.1 | 596.5 | 0.409 | 0.64 0.63 0.65 | 208.6 217.4 215.6 |
| u1-normal-h1 | wasm | main | throughput | 3 | 3 | 3 | 326.8 | 317.4 | 590.1 | 0.835 | 0.66 0.63 0.66 | 201.4 212.9 205.0 |
| u10-normal-h3 | both | base | throughput | 3 | 3 | 3 | 6.0 | 5.9 | 6.7 | 0.132 | 0.51 0.51 0.51 | 198.3 201.4 212.4 |
| u10-normal-h3 | both | main | throughput | 3 | 3 | 3 | 8.9 | 8.1 | 9.4 | 0.152 | 0.51 0.51 0.51 | 198.6 211.2 198.8 |
| u10-normal-h3 | none | base | throughput | 3 | 3 | 3 | 1551.5 | 1432.2 | 2043.7 | 0.394 | 0.69 0.68 0.65 | 26.0 25.4 26.3 |
| u10-normal-h3 | none | main | throughput | 3 | 3 | 3 | 1009.8 | 982.2 | 1493.8 | 0.507 | 0.69 0.67 0.70 | 25.5 25.7 25.5 |
| u10-normal-h3 | wasm | base | throughput | 3 | 3 | 3 | 371.7 | 346.2 | 424.2 | 0.210 | 0.62 0.62 0.63 | 220.1 211.0 208.2 |
| u10-normal-h3 | wasm | main | throughput | 3 | 3 | 3 | 329.9 | 311.6 | 395.9 | 0.256 | 0.63 0.64 0.62 | 198.0 214.4 214.7 |
| u2-normal-h2 | both | base | throughput | 3 | 3 | 3 | 9.4 | 9.3 | 9.5 | 0.016 | 0.51 0.51 0.50 | 204.7 212.2 199.6 |
| u2-normal-h2 | both | main | throughput | 3 | 3 | 3 | 8.8 | 8.0 | 9.4 | 0.169 | 0.51 0.51 0.51 | 201.1 203.4 198.8 |
| u2-normal-h2 | none | base | throughput | 3 | 3 | 3 | 1754.5 | 1557.0 | 1848.5 | 0.166 | 0.64 0.65 0.64 | 24.8 24.3 25.7 |
| u2-normal-h2 | none | main | throughput | 3 | 3 | 3 | 1394.7 | 1350.7 | 1744.2 | 0.282 | 0.65 0.65 0.64 | 25.0 24.8 24.5 |
| u2-normal-h2 | waf | base | throughput | 3 | 3 | 3 | 7.1 | 6.2 | 8.8 | 0.373 | 0.51 0.51 0.51 | 108.7 105.0 109.5 |
| u2-normal-h2 | waf | main | throughput | 3 | 3 | 3 | 8.4 | 6.9 | 9.2 | 0.278 | 0.51 0.51 0.51 | 110.4 109.9 107.3 |
| u2-normal-h2 | wasm | base | throughput | 3 | 3 | 3 | 413.7 | 313.6 | 417.6 | 0.251 | 0.65 0.66 0.64 | 203.9 211.0 209.3 |
| u2-normal-h2 | wasm | main | throughput | 3 | 3 | 3 | 419.4 | 352.5 | 467.1 | 0.273 | 0.64 0.66 0.65 | 207.1 208.0 209.5 |

## Comparisons (informational; not A13 acceptance)

| Scenario | Variant | Baseline | Candidate | Median regression | Verdict |
| --- | --- | --- | --- | ---: | --- |
| u1-normal-h1 | both | base | main | 0.117 | fail-regression |
| u1-normal-h1 | none | base | main | 0.276 | fail-regression |
| u1-normal-h1 | waf | base | main | -0.321 | inconclusive-noise |
| u1-normal-h1 | wasm | base | main | 0.258 | fail-regression |
| u10-normal-h3 | both | base | main | -0.470 | inconclusive-noise |
| u10-normal-h3 | none | base | main | 0.349 | fail-regression |
| u10-normal-h3 | wasm | base | main | 0.112 | fail-regression |
| u2-normal-h2 | both | base | main | 0.063 | inconclusive-noise |
| u2-normal-h2 | none | base | main | 0.205 | fail-regression |
| u2-normal-h2 | waf | base | main | -0.189 | inconclusive-noise |
| u2-normal-h2 | wasm | base | main | -0.013 | inconclusive-noise |

## Phases and classes

One row per repetition, phase and workload class. Goodput counts verified responses that started and finished in the window; requests crossing in from warm-up and those outstanding at the window end are excluded from it.

| Scenario | Variant | Target | Run | Rep | Phase | Class | Proto | C | Attempts | Verified in window | Goodput rps | Crossed in | Outstanding | p99 ms | Outcomes |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| u1-normal-h1 | both | base | ia-a13-normal | 1 | load | load | h1 | 1 | 190 | 189 | 9.4 | 1 | 1 | 129.2 | ok ×190 |
| u1-normal-h1 | both | base | ia-a13-normal | 2 | load | load | h1 | 1 | 171 | 170 | 8.5 | 1 | 1 | 146.6 | ok ×171 |
| u1-normal-h1 | both | base | ia-a13-normal | 3 | load | load | h1 | 1 | 181 | 180 | 9.0 | 1 | 1 | 145.5 | ok ×181 |
| u1-normal-h1 | both | main | ia-a13-normal | 1 | load | load | h1 | 1 | 160 | 159 | 8.0 | 1 | 1 | 179.7 | ok ×160 |
| u1-normal-h1 | both | main | ia-a13-normal | 2 | load | load | h1 | 1 | 191 | 190 | 9.5 | 1 | 1 | 138.8 | ok ×191 |
| u1-normal-h1 | both | main | ia-a13-normal | 3 | load | load | h1 | 1 | 147 | 146 | 7.3 | 1 | 1 | 168.9 | ok ×147 |
| u1-normal-h1 | none | base | ia-a13-normal | 1 | load | load | h1 | 16 | 102071 | 102055 | 5102.8 | 16 | 16 | 13.0 | ok ×102071 |
| u1-normal-h1 | none | base | ia-a13-normal | 2 | load | load | h1 | 16 | 106951 | 106935 | 5346.8 | 16 | 16 | 11.1 | ok ×106951 |
| u1-normal-h1 | none | base | ia-a13-normal | 3 | load | load | h1 | 16 | 103361 | 103345 | 5167.2 | 14 | 16 | 12.7 | ok ×103361 |
| u1-normal-h1 | none | main | ia-a13-normal | 1 | load | load | h1 | 16 | 74831 | 74816 | 3740.8 | 16 | 15 | 15.1 | ok ×74831 |
| u1-normal-h1 | none | main | ia-a13-normal | 2 | load | load | h1 | 16 | 112563 | 112547 | 5627.4 | 16 | 16 | 11.0 | ok ×112563 |
| u1-normal-h1 | none | main | ia-a13-normal | 3 | load | load | h1 | 16 | 44864 | 44848 | 2242.4 | 16 | 16 | 23.6 | ok ×44864 |
| u1-normal-h1 | waf | base | ia-a13-normal | 1 | load | load | h1 | 1 | 150 | 149 | 7.5 | 1 | 1 | 184.5 | ok ×150 |
| u1-normal-h1 | waf | base | ia-a13-normal | 2 | load | load | h1 | 1 | 193 | 192 | 9.6 | 1 | 1 | 133.4 | ok ×193 |
| u1-normal-h1 | waf | base | ia-a13-normal | 3 | load | load | h1 | 1 | 149 | 148 | 7.4 | 1 | 1 | 198.7 | ok ×149 |
| u1-normal-h1 | waf | main | ia-a13-normal | 1 | load | load | h1 | 1 | 198 | 197 | 9.8 | 1 | 1 | 131.6 | ok ×198 |
| u1-normal-h1 | waf | main | ia-a13-normal | 2 | load | load | h1 | 1 | 209 | 208 | 10.4 | 1 | 1 | 126.7 | ok ×209 |
| u1-normal-h1 | waf | main | ia-a13-normal | 3 | load | load | h1 | 1 | 197 | 196 | 9.8 | 1 | 1 | 130.6 | ok ×197 |
| u1-normal-h1 | wasm | base | ia-a13-normal | 1 | load | load | h1 | 2 | 8325 | 8323 | 416.1 | 2 | 2 | 14.5 | ok ×8325 |
| u1-normal-h1 | wasm | base | ia-a13-normal | 2 | load | load | h1 | 2 | 11931 | 11929 | 596.5 | 2 | 2 | 9.7 | ok ×11931 |
| u1-normal-h1 | wasm | base | ia-a13-normal | 3 | load | load | h1 | 2 | 8811 | 8809 | 440.4 | 2 | 2 | 12.6 | ok ×8811 |
| u1-normal-h1 | wasm | main | ia-a13-normal | 1 | load | load | h1 | 2 | 6349 | 6347 | 317.4 | 2 | 2 | 17.6 | ok ×6349 |
| u1-normal-h1 | wasm | main | ia-a13-normal | 2 | load | load | h1 | 2 | 11804 | 11802 | 590.1 | 2 | 2 | 9.9 | ok ×11804 |
| u1-normal-h1 | wasm | main | ia-a13-normal | 3 | load | load | h1 | 2 | 6538 | 6536 | 326.8 | 2 | 2 | 17.1 | ok ×6538 |
| u10-normal-h3 | both | base | ia-a13-normal | 1 | load | load | h3 | 1 | 122 | 121 | 6.0 | 1 | 1 | 198.8 | ok ×122 |
| u10-normal-h3 | both | base | ia-a13-normal | 2 | load | load | h3 | 1 | 119 | 118 | 5.9 | 1 | 1 | 207.2 | ok ×119 |
| u10-normal-h3 | both | base | ia-a13-normal | 3 | load | load | h3 | 1 | 135 | 134 | 6.7 | 1 | 1 | 179.7 | ok ×135 |
| u10-normal-h3 | both | main | ia-a13-normal | 1 | load | load | h3 | 1 | 163 | 162 | 8.1 | 1 | 1 | 161.7 | ok ×163 |
| u10-normal-h3 | both | main | ia-a13-normal | 2 | load | load | h3 | 1 | 190 | 189 | 9.4 | 1 | 1 | 129.1 | ok ×190 |
| u10-normal-h3 | both | main | ia-a13-normal | 3 | load | load | h3 | 1 | 179 | 178 | 8.9 | 1 | 1 | 133.9 | ok ×179 |
| u10-normal-h3 | none | base | ia-a13-normal | 1 | load | load | h3 | 16 | 28659 | 28643 | 1432.2 | 16 | 16 | 24.5 | ok ×28659 |
| u10-normal-h3 | none | base | ia-a13-normal | 2 | load | load | h3 | 16 | 31046 | 31030 | 1551.5 | 16 | 16 | 24.8 | ok ×31046 |
| u10-normal-h3 | none | base | ia-a13-normal | 3 | load | load | h3 | 16 | 40890 | 40874 | 2043.7 | 16 | 16 | 20.4 | ok ×40890 |
| u10-normal-h3 | none | main | ia-a13-normal | 1 | load | load | h3 | 16 | 20212 | 20196 | 1009.8 | 16 | 16 | 35.2 | ok ×20212 |
| u10-normal-h3 | none | main | ia-a13-normal | 2 | load | load | h3 | 16 | 29892 | 29876 | 1493.8 | 15 | 16 | 25.9 | ok ×29892 |
| u10-normal-h3 | none | main | ia-a13-normal | 3 | load | load | h3 | 16 | 19660 | 19644 | 982.2 | 16 | 16 | 35.0 | ok ×19660 |
| u10-normal-h3 | wasm | base | ia-a13-normal | 1 | load | load | h3 | 2 | 7436 | 7434 | 371.7 | 2 | 2 | 13.4 | ok ×7436 |
| u10-normal-h3 | wasm | base | ia-a13-normal | 2 | load | load | h3 | 2 | 8487 | 8485 | 424.2 | 2 | 2 | 11.6 | ok ×8487 |
| u10-normal-h3 | wasm | base | ia-a13-normal | 3 | load | load | h3 | 2 | 6926 | 6924 | 346.2 | 2 | 2 | 16.6 | ok ×6926 |
| u10-normal-h3 | wasm | main | ia-a13-normal | 1 | load | load | h3 | 2 | 6233 | 6231 | 311.6 | 2 | 2 | 15.7 | ok ×6233 |
| u10-normal-h3 | wasm | main | ia-a13-normal | 2 | load | load | h3 | 2 | 6600 | 6598 | 329.9 | 2 | 2 | 16.1 | ok ×6600 |
| u10-normal-h3 | wasm | main | ia-a13-normal | 3 | load | load | h3 | 2 | 7920 | 7918 | 395.9 | 2 | 2 | 13.6 | ok ×7920 |
| u2-normal-h2 | both | base | ia-a13-normal | 1 | load | load | h2 | 1 | 190 | 189 | 9.4 | 1 | 1 | 137.3 | ok ×190 |
| u2-normal-h2 | both | base | ia-a13-normal | 2 | load | load | h2 | 1 | 191 | 190 | 9.5 | 1 | 1 | 129.9 | ok ×191 |
| u2-normal-h2 | both | base | ia-a13-normal | 3 | load | load | h2 | 1 | 188 | 187 | 9.3 | 1 | 1 | 138.2 | ok ×188 |
| u2-normal-h2 | both | main | ia-a13-normal | 1 | load | load | h2 | 1 | 160 | 159 | 8.0 | 1 | 1 | 174.5 | ok ×160 |
| u2-normal-h2 | both | main | ia-a13-normal | 2 | load | load | h2 | 1 | 178 | 177 | 8.8 | 1 | 1 | 154.1 | ok ×178 |
| u2-normal-h2 | both | main | ia-a13-normal | 3 | load | load | h2 | 1 | 190 | 189 | 9.4 | 1 | 1 | 136.7 | ok ×190 |
| u2-normal-h2 | none | base | ia-a13-normal | 1 | load | load | h2 | 16 | 35107 | 35091 | 1754.5 | 16 | 16 | 19.5 | ok ×35107 |
| u2-normal-h2 | none | base | ia-a13-normal | 2 | load | load | h2 | 16 | 31155 | 31139 | 1557.0 | 16 | 16 | 23.6 | ok ×31155 |
| u2-normal-h2 | none | base | ia-a13-normal | 3 | load | load | h2 | 16 | 36985 | 36969 | 1848.5 | 16 | 16 | 19.3 | ok ×36985 |
| u2-normal-h2 | none | main | ia-a13-normal | 1 | load | load | h2 | 16 | 27910 | 27894 | 1394.7 | 15 | 16 | 31.6 | ok ×27910 |
| u2-normal-h2 | none | main | ia-a13-normal | 2 | load | load | h2 | 16 | 27029 | 27013 | 1350.7 | 16 | 16 | 31.4 | ok ×27029 |
| u2-normal-h2 | none | main | ia-a13-normal | 3 | load | load | h2 | 16 | 34900 | 34884 | 1744.2 | 15 | 16 | 23.3 | ok ×34900 |
| u2-normal-h2 | waf | base | ia-a13-normal | 1 | load | load | h2 | 1 | 124 | 123 | 6.2 | 1 | 1 | 215.8 | ok ×124 |
| u2-normal-h2 | waf | base | ia-a13-normal | 2 | load | load | h2 | 1 | 177 | 176 | 8.8 | 1 | 1 | 138.4 | ok ×177 |
| u2-normal-h2 | waf | base | ia-a13-normal | 3 | load | load | h2 | 1 | 143 | 142 | 7.1 | 1 | 1 | 206.9 | ok ×143 |
| u2-normal-h2 | waf | main | ia-a13-normal | 1 | load | load | h2 | 1 | 170 | 169 | 8.4 | 1 | 1 | 143.3 | ok ×170 |
| u2-normal-h2 | waf | main | ia-a13-normal | 2 | load | load | h2 | 1 | 186 | 185 | 9.2 | 1 | 1 | 151.8 | ok ×186 |
| u2-normal-h2 | waf | main | ia-a13-normal | 3 | load | load | h2 | 1 | 139 | 138 | 6.9 | 1 | 1 | 171.6 | ok ×139 |
| u2-normal-h2 | wasm | base | ia-a13-normal | 1 | load | load | h2 | 2 | 8355 | 8353 | 417.6 | 2 | 2 | 12.7 | ok ×8355 |
| u2-normal-h2 | wasm | base | ia-a13-normal | 2 | load | load | h2 | 2 | 6275 | 6273 | 313.6 | 2 | 2 | 16.6 | ok ×6275 |
| u2-normal-h2 | wasm | base | ia-a13-normal | 3 | load | load | h2 | 2 | 8276 | 8274 | 413.7 | 2 | 2 | 13.1 | ok ×8276 |
| u2-normal-h2 | wasm | main | ia-a13-normal | 1 | load | load | h2 | 2 | 9345 | 9343 | 467.1 | 2 | 2 | 12.3 | ok ×9345 |
| u2-normal-h2 | wasm | main | ia-a13-normal | 2 | load | load | h2 | 2 | 7051 | 7050 | 352.5 | 2 | 1 | 16.1 | ok ×7051 |
| u2-normal-h2 | wasm | main | ia-a13-normal | 3 | load | load | h2 | 2 | 8390 | 8388 | 419.4 | 2 | 2 | 13.8 | ok ×8390 |

## Failed, invalid or inconclusive runs

None.
