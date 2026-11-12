# Inspection assessment summary

Plan `inspection-assessment-plan/v1`, profile `qualification`. Runs: campaign-normal, supplement-u1-none.

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
| u1-normal-h1 | both | base | throughput | 3 | 3 | 3 | 10.2 | 10.1 | 10.2 | 0.020 | 0.50 0.50 0.50 | 199.3 206.5 197.0 |
| u1-normal-h1 | both | base4 | throughput | 3 | 3 | 3 | 10.1 | 10.0 | 10.2 | 0.015 | 0.50 0.50 0.50 | 222.8 212.1 218.2 |
| u1-normal-h1 | both | main | throughput | 3 | 3 | 3 | 10.2 | 10.1 | 10.2 | 0.015 | 0.50 0.50 0.50 | 212.9 212.0 226.8 |
| u1-normal-h1 | none | base | throughput | 9 | 9 | 9 | 7331.7 | 7102.9 | 7576.2 | 0.065 | 0.63 0.62 0.63 0.62 0.63 0.63 0.62 0.63 0.63 | 24.3 24.4 24.7 24.1 23.8 24.7 24.2 24.2 24.6 |
| u1-normal-h1 | none | base4 | throughput | 9 | 9 | 9 | 7217.8 | 6789.4 | 7459.8 | 0.093 | 0.62 0.62 0.62 0.63 0.63 0.63 0.63 0.63 0.63 | 40.6 40.0 40.6 40.4 40.0 44.9 39.9 39.9 40.8 |
| u1-normal-h1 | none | main | throughput | 9 | 9 | 9 | 7189.9 | 6700.9 | 7554.5 | 0.119 | 0.63 0.63 0.63 0.62 0.62 0.63 0.63 0.62 0.62 | 44.9 41.1 40.1 40.7 40.5 40.2 40.3 40.0 41.0 |
| u1-normal-h1 | waf | base | throughput | 3 | 3 | 3 | 10.3 | 10.1 | 10.3 | 0.024 | 0.50 0.50 0.50 | 110.8 108.2 109.3 |
| u1-normal-h1 | waf | base4 | throughput | 3 | 3 | 3 | 10.4 | 10.3 | 10.4 | 0.005 | 0.50 0.50 0.50 | 128.1 129.7 119.4 |
| u1-normal-h1 | waf | main | throughput | 3 | 3 | 3 | 10.3 | 10.3 | 10.3 | 0.005 | 0.50 0.50 0.51 | 128.0 130.1 125.3 |
| u1-normal-h1 | wasm | base | throughput | 3 | 3 | 3 | 631.7 | 619.0 | 638.8 | 0.031 | 0.63 0.63 0.63 | 208.0 211.3 228.6 |
| u1-normal-h1 | wasm | base4 | throughput | 3 | 3 | 3 | 616.3 | 612.6 | 624.5 | 0.019 | 0.63 0.63 0.63 | 235.4 229.1 228.0 |
| u1-normal-h1 | wasm | main | throughput | 3 | 3 | 3 | 628.3 | 619.0 | 629.5 | 0.017 | 0.63 0.64 0.63 | 234.5 225.2 228.6 |
| u10-normal-h3 | both | base | throughput | 3 | 3 | 3 | 9.9 | 9.9 | 10.1 | 0.010 | 0.50 0.51 0.50 | 200.3 200.3 197.0 |
| u10-normal-h3 | both | base4 | throughput | 3 | 3 | 3 | 9.9 | 9.9 | 10.0 | 0.010 | 0.51 0.51 0.50 | 215.7 219.8 212.0 |
| u10-normal-h3 | both | main | throughput | 3 | 3 | 3 | 10.0 | 9.8 | 10.1 | 0.020 | 0.50 0.50 0.50 | 226.3 211.4 225.0 |
| u10-normal-h3 | none | base | throughput | 3 | 3 | 3 | 3303.2 | 3243.2 | 3372.2 | 0.039 | 0.61 0.61 0.63 | 26.7 26.1 26.1 |
| u10-normal-h3 | none | base4 | throughput | 3 | 3 | 3 | 3279.5 | 3247.4 | 3301.1 | 0.016 | 0.62 0.62 0.63 | 42.8 42.8 42.2 |
| u10-normal-h3 | none | main | throughput | 3 | 3 | 3 | 3306.8 | 3266.0 | 3355.8 | 0.027 | 0.62 0.61 0.62 | 42.9 43.7 42.8 |
| u2-normal-h2 | both | base | throughput | 3 | 3 | 3 | 10.1 | 9.8 | 10.2 | 0.030 | 0.51 0.50 0.50 | 196.8 200.8 198.4 |
| u2-normal-h2 | both | base4 | throughput | 3 | 3 | 3 | 10.1 | 9.8 | 10.2 | 0.035 | 0.50 0.50 0.50 | 227.2 220.2 211.5 |
| u2-normal-h2 | both | main | throughput | 3 | 3 | 3 | 10.1 | 10.0 | 10.2 | 0.015 | 0.50 0.50 0.50 | 215.6 223.2 228.1 |
| u2-normal-h2 | none | base | throughput | 3 | 3 | 3 | 2968.2 | 2953.6 | 3006.6 | 0.018 | 0.61 0.61 0.61 | 26.2 25.7 25.1 |
| u2-normal-h2 | none | base4 | throughput | 3 | 3 | 3 | 2968.6 | 2965.7 | 2979.4 | 0.005 | 0.61 0.61 0.61 | 41.0 41.9 41.8 |
| u2-normal-h2 | none | main | throughput | 3 | 3 | 3 | 2991.1 | 2942.1 | 3007.7 | 0.022 | 0.61 0.61 0.61 | 41.8 41.6 44.1 |
| u2-normal-h2 | waf | base | throughput | 3 | 3 | 3 | 10.3 | 10.2 | 10.3 | 0.019 | 0.50 0.50 0.50 | 106.0 109.7 107.7 |
| u2-normal-h2 | waf | base4 | throughput | 3 | 3 | 3 | 10.3 | 10.2 | 10.4 | 0.024 | 0.51 0.50 0.50 | 124.5 124.7 119.1 |
| u2-normal-h2 | waf | main | throughput | 3 | 3 | 3 | 10.3 | 10.1 | 10.4 | 0.034 | 0.50 0.51 0.51 | 123.8 123.2 124.2 |
| u2-normal-h2 | wasm | base | throughput | 3 | 3 | 3 | 577.0 | 543.4 | 587.1 | 0.076 | 0.64 0.64 0.64 | 206.5 213.8 206.5 |
| u2-normal-h2 | wasm | base4 | throughput | 3 | 3 | 3 | 582.2 | 574.0 | 587.5 | 0.023 | 0.64 0.65 0.65 | 226.1 223.3 230.4 |
| u2-normal-h2 | wasm | main | throughput | 3 | 3 | 3 | 583.7 | 571.9 | 585.6 | 0.024 | 0.64 0.64 0.65 | 231.0 224.7 224.5 |

## Comparisons (informational; not A13 acceptance)

| Scenario | Variant | Baseline | Candidate | Median regression | Verdict |
| --- | --- | --- | --- | ---: | --- |
| u1-normal-h1 | both | base | main | 0.005 | pass |
| u1-normal-h1 | none | base | main | 0.019 | inconclusive-noise |
| u1-normal-h1 | waf | base | main | -0.004 | pass |
| u1-normal-h1 | wasm | base | main | 0.005 | pass |
| u10-normal-h3 | both | base | main | -0.004 | pass |
| u10-normal-h3 | none | base | main | 0.000 | pass |
| u2-normal-h2 | both | base | main | 0.000 | pass |
| u2-normal-h2 | none | base | main | -0.007 | pass |
| u2-normal-h2 | waf | base | main | 0.000 | pass |
| u2-normal-h2 | wasm | base | main | -0.011 | pass |
| u1-normal-h1 | both | base4 | main | -0.004 | pass |
| u1-normal-h1 | none | base4 | main | 0.004 | inconclusive-noise |
| u1-normal-h1 | waf | base4 | main | 0.005 | pass |
| u1-normal-h1 | wasm | base4 | main | -0.018 | pass |
| u10-normal-h3 | both | base4 | main | -0.004 | pass |
| u10-normal-h3 | none | base4 | main | -0.007 | pass |
| u2-normal-h2 | both | base4 | main | 0.000 | pass |
| u2-normal-h2 | none | base4 | main | -0.007 | pass |
| u2-normal-h2 | waf | base4 | main | 0.000 | pass |
| u2-normal-h2 | wasm | base4 | main | -0.002 | pass |
| u1-normal-h1 | both | base | base4 | 0.010 | pass |
| u1-normal-h1 | none | base | base4 | 0.016 | pass |
| u1-normal-h1 | waf | base | base4 | -0.009 | pass |
| u1-normal-h1 | wasm | base | base4 | 0.024 | pass |
| u10-normal-h3 | both | base | base4 | 0.000 | pass |
| u10-normal-h3 | none | base | base4 | 0.007 | pass |
| u2-normal-h2 | both | base | base4 | 0.000 | pass |
| u2-normal-h2 | none | base | base4 | 0.000 | pass |
| u2-normal-h2 | waf | base | base4 | 0.000 | pass |
| u2-normal-h2 | wasm | base | base4 | -0.008 | pass |

## Phases and classes

One row per repetition, phase and workload class. Goodput counts verified responses that started and finished in the window; requests crossing in from warm-up and those outstanding at the window end are excluded from it.

| Scenario | Variant | Target | Run | Rep | Phase | Class | Proto | C | Attempts | Verified in window | Goodput rps | Crossed in | Outstanding | p99 ms | Outcomes |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| u1-normal-h1 | both | base | campaign-normal | 1 | load | load | h1 | 1 | 206 | 205 | 10.2 | 1 | 1 | 112.4 | ok ×206 |
| u1-normal-h1 | both | base | campaign-normal | 2 | load | load | h1 | 1 | 205 | 204 | 10.2 | 1 | 1 | 114.8 | ok ×205 |
| u1-normal-h1 | both | base | campaign-normal | 3 | load | load | h1 | 1 | 202 | 201 | 10.1 | 1 | 1 | 121.7 | ok ×202 |
| u1-normal-h1 | both | base4 | campaign-normal | 1 | load | load | h1 | 1 | 201 | 200 | 10.0 | 1 | 1 | 130.4 | ok ×201 |
| u1-normal-h1 | both | base4 | campaign-normal | 2 | load | load | h1 | 1 | 203 | 202 | 10.1 | 1 | 1 | 125.5 | ok ×203 |
| u1-normal-h1 | both | base4 | campaign-normal | 3 | load | load | h1 | 1 | 204 | 203 | 10.2 | 1 | 1 | 123.8 | ok ×204 |
| u1-normal-h1 | both | main | campaign-normal | 1 | load | load | h1 | 1 | 204 | 203 | 10.2 | 1 | 1 | 122.8 | ok ×204 |
| u1-normal-h1 | both | main | campaign-normal | 2 | load | load | h1 | 1 | 203 | 202 | 10.1 | 1 | 1 | 127.1 | ok ×203 |
| u1-normal-h1 | both | main | campaign-normal | 3 | load | load | h1 | 1 | 206 | 205 | 10.2 | 1 | 1 | 115.4 | ok ×206 |
| u1-normal-h1 | none | base | campaign-normal | 1 | load | load | h1 | 16 | 151199 | 151183 | 7559.1 | 12 | 16 | 9.4 | ok ×151199 |
| u1-normal-h1 | none | base | campaign-normal | 2 | load | load | h1 | 16 | 151195 | 151179 | 7558.9 | 16 | 16 | 9.3 | ok ×151195 |
| u1-normal-h1 | none | base | campaign-normal | 3 | load | load | h1 | 16 | 151540 | 151525 | 7576.2 | 15 | 15 | 9.2 | ok ×151540 |
| u1-normal-h1 | none | base | supplement-u1-none | 1 | load | load | h1 | 16 | 146501 | 146485 | 7324.2 | 16 | 16 | 9.6 | ok ×146501 |
| u1-normal-h1 | none | base | supplement-u1-none | 2 | load | load | h1 | 16 | 142074 | 142058 | 7102.9 | 15 | 16 | 10.0 | ok ×142074 |
| u1-normal-h1 | none | base | supplement-u1-none | 3 | load | load | h1 | 16 | 142640 | 142624 | 7131.2 | 16 | 16 | 9.5 | ok ×142640 |
| u1-normal-h1 | none | base | supplement-u1-none | 4 | load | load | h1 | 16 | 149180 | 149164 | 7458.2 | 16 | 16 | 9.3 | ok ×149180 |
| u1-normal-h1 | none | base | supplement-u1-none | 5 | load | load | h1 | 16 | 143100 | 143084 | 7154.2 | 16 | 16 | 9.4 | ok ×143100 |
| u1-normal-h1 | none | base | supplement-u1-none | 6 | load | load | h1 | 16 | 146650 | 146634 | 7331.7 | 16 | 16 | 9.3 | ok ×146650 |
| u1-normal-h1 | none | base4 | campaign-normal | 1 | load | load | h1 | 16 | 149212 | 149196 | 7459.8 | 16 | 16 | 9.3 | ok ×149212 |
| u1-normal-h1 | none | base4 | campaign-normal | 2 | load | load | h1 | 16 | 145403 | 145387 | 7269.4 | 16 | 16 | 9.7 | ok ×145403 |
| u1-normal-h1 | none | base4 | campaign-normal | 3 | load | load | h1 | 16 | 144371 | 144355 | 7217.8 | 16 | 16 | 9.7 | ok ×144371 |
| u1-normal-h1 | none | base4 | supplement-u1-none | 1 | load | load | h1 | 16 | 147525 | 147510 | 7375.5 | 16 | 15 | 9.6 | ok ×147525 |
| u1-normal-h1 | none | base4 | supplement-u1-none | 2 | load | load | h1 | 16 | 143863 | 143847 | 7192.4 | 15 | 16 | 9.6 | ok ×143863 |
| u1-normal-h1 | none | base4 | supplement-u1-none | 3 | load | load | h1 | 16 | 137200 | 137185 | 6859.2 | 16 | 15 | 10.8 | ok ×137200 |
| u1-normal-h1 | none | base4 | supplement-u1-none | 4 | load | load | h1 | 16 | 139901 | 139885 | 6994.2 | 16 | 16 | 9.8 | ok ×139901 |
| u1-normal-h1 | none | base4 | supplement-u1-none | 5 | load | load | h1 | 16 | 148365 | 148349 | 7417.4 | 15 | 16 | 9.4 | ok ×148365 |
| u1-normal-h1 | none | base4 | supplement-u1-none | 6 | load | load | h1 | 16 | 135805 | 135789 | 6789.4 | 16 | 16 | 9.8 | ok ×135805 |
| u1-normal-h1 | none | main | campaign-normal | 1 | load | load | h1 | 16 | 135704 | 135688 | 6784.4 | 16 | 16 | 11.5 | ok ×135704 |
| u1-normal-h1 | none | main | campaign-normal | 2 | load | load | h1 | 16 | 134032 | 134017 | 6700.9 | 11 | 15 | 10.0 | ok ×134032 |
| u1-normal-h1 | none | main | campaign-normal | 3 | load | load | h1 | 16 | 148456 | 148440 | 7422.0 | 16 | 16 | 9.3 | ok ×148456 |
| u1-normal-h1 | none | main | supplement-u1-none | 1 | load | load | h1 | 16 | 144146 | 144131 | 7206.6 | 16 | 15 | 9.5 | ok ×144146 |
| u1-normal-h1 | none | main | supplement-u1-none | 2 | load | load | h1 | 16 | 151106 | 151090 | 7554.5 | 16 | 16 | 9.2 | ok ×151106 |
| u1-normal-h1 | none | main | supplement-u1-none | 3 | load | load | h1 | 16 | 139116 | 139100 | 6955.0 | 16 | 16 | 9.7 | ok ×139116 |
| u1-normal-h1 | none | main | supplement-u1-none | 4 | load | load | h1 | 16 | 150231 | 150215 | 7510.8 | 16 | 16 | 9.5 | ok ×150231 |
| u1-normal-h1 | none | main | supplement-u1-none | 5 | load | load | h1 | 16 | 142671 | 142655 | 7132.8 | 11 | 16 | 9.5 | ok ×142671 |
| u1-normal-h1 | none | main | supplement-u1-none | 6 | load | load | h1 | 16 | 143813 | 143798 | 7189.9 | 13 | 15 | 9.4 | ok ×143813 |
| u1-normal-h1 | waf | base | campaign-normal | 1 | load | load | h1 | 1 | 208 | 207 | 10.3 | 1 | 1 | 118.1 | ok ×208 |
| u1-normal-h1 | waf | base | campaign-normal | 2 | load | load | h1 | 1 | 203 | 202 | 10.1 | 1 | 1 | 123.0 | ok ×203 |
| u1-normal-h1 | waf | base | campaign-normal | 3 | load | load | h1 | 1 | 207 | 206 | 10.3 | 1 | 1 | 123.3 | ok ×207 |
| u1-normal-h1 | waf | base4 | campaign-normal | 1 | load | load | h1 | 1 | 209 | 208 | 10.4 | 1 | 1 | 115.4 | ok ×209 |
| u1-normal-h1 | waf | base4 | campaign-normal | 2 | load | load | h1 | 1 | 209 | 208 | 10.4 | 1 | 1 | 119.4 | ok ×209 |
| u1-normal-h1 | waf | base4 | campaign-normal | 3 | load | load | h1 | 1 | 208 | 207 | 10.3 | 1 | 1 | 131.3 | ok ×208 |
| u1-normal-h1 | waf | main | campaign-normal | 1 | load | load | h1 | 1 | 207 | 206 | 10.3 | 1 | 1 | 113.3 | ok ×207 |
| u1-normal-h1 | waf | main | campaign-normal | 2 | load | load | h1 | 1 | 208 | 207 | 10.3 | 1 | 1 | 115.7 | ok ×208 |
| u1-normal-h1 | waf | main | campaign-normal | 3 | load | load | h1 | 1 | 208 | 207 | 10.3 | 1 | 1 | 125.3 | ok ×208 |
| u1-normal-h1 | wasm | base | campaign-normal | 1 | load | load | h1 | 2 | 12636 | 12634 | 631.7 | 2 | 2 | 8.9 | ok ×12636 |
| u1-normal-h1 | wasm | base | campaign-normal | 2 | load | load | h1 | 2 | 12383 | 12381 | 619.0 | 2 | 2 | 9.1 | ok ×12383 |
| u1-normal-h1 | wasm | base | campaign-normal | 3 | load | load | h1 | 2 | 12778 | 12776 | 638.8 | 2 | 2 | 9.0 | ok ×12778 |
| u1-normal-h1 | wasm | base4 | campaign-normal | 1 | load | load | h1 | 2 | 12328 | 12326 | 616.3 | 2 | 2 | 9.1 | ok ×12328 |
| u1-normal-h1 | wasm | base4 | campaign-normal | 2 | load | load | h1 | 2 | 12493 | 12491 | 624.5 | 2 | 2 | 9.5 | ok ×12493 |
| u1-normal-h1 | wasm | base4 | campaign-normal | 3 | load | load | h1 | 2 | 12254 | 12252 | 612.6 | 2 | 2 | 10.1 | ok ×12254 |
| u1-normal-h1 | wasm | main | campaign-normal | 1 | load | load | h1 | 2 | 12383 | 12381 | 619.0 | 2 | 2 | 9.2 | ok ×12383 |
| u1-normal-h1 | wasm | main | campaign-normal | 2 | load | load | h1 | 2 | 12592 | 12590 | 629.5 | 2 | 2 | 8.8 | ok ×12592 |
| u1-normal-h1 | wasm | main | campaign-normal | 3 | load | load | h1 | 2 | 12568 | 12566 | 628.3 | 2 | 2 | 9.0 | ok ×12568 |
| u10-normal-h3 | both | base | campaign-normal | 1 | load | load | h3 | 1 | 200 | 199 | 9.9 | 1 | 1 | 121.8 | ok ×200 |
| u10-normal-h3 | both | base | campaign-normal | 2 | load | load | h3 | 1 | 202 | 201 | 10.1 | 1 | 1 | 126.7 | ok ×202 |
| u10-normal-h3 | both | base | campaign-normal | 3 | load | load | h3 | 1 | 200 | 199 | 9.9 | 1 | 1 | 122.4 | ok ×200 |
| u10-normal-h3 | both | base4 | campaign-normal | 1 | load | load | h3 | 1 | 199 | 198 | 9.9 | 1 | 1 | 117.4 | ok ×199 |
| u10-normal-h3 | both | base4 | campaign-normal | 2 | load | load | h3 | 1 | 201 | 200 | 10.0 | 1 | 1 | 123.8 | ok ×201 |
| u10-normal-h3 | both | base4 | campaign-normal | 3 | load | load | h3 | 1 | 200 | 199 | 9.9 | 1 | 1 | 125.6 | ok ×200 |
| u10-normal-h3 | both | main | campaign-normal | 1 | load | load | h3 | 1 | 201 | 200 | 10.0 | 1 | 1 | 122.7 | ok ×201 |
| u10-normal-h3 | both | main | campaign-normal | 2 | load | load | h3 | 1 | 198 | 197 | 9.8 | 1 | 1 | 126.6 | ok ×198 |
| u10-normal-h3 | both | main | campaign-normal | 3 | load | load | h3 | 1 | 202 | 201 | 10.1 | 1 | 1 | 122.5 | ok ×202 |
| u10-normal-h3 | none | base | campaign-normal | 1 | load | load | h3 | 16 | 66079 | 66063 | 3303.2 | 16 | 16 | 11.2 | ok ×66079 |
| u10-normal-h3 | none | base | campaign-normal | 2 | load | load | h3 | 16 | 64879 | 64863 | 3243.2 | 16 | 16 | 11.7 | ok ×64879 |
| u10-normal-h3 | none | base | campaign-normal | 3 | load | load | h3 | 16 | 67461 | 67445 | 3372.2 | 16 | 16 | 11.2 | ok ×67461 |
| u10-normal-h3 | none | base4 | campaign-normal | 1 | load | load | h3 | 16 | 64965 | 64949 | 3247.4 | 16 | 16 | 11.8 | ok ×64965 |
| u10-normal-h3 | none | base4 | campaign-normal | 2 | load | load | h3 | 16 | 65606 | 65590 | 3279.5 | 16 | 16 | 11.3 | ok ×65606 |
| u10-normal-h3 | none | base4 | campaign-normal | 3 | load | load | h3 | 16 | 66037 | 66021 | 3301.1 | 16 | 16 | 11.4 | ok ×66037 |
| u10-normal-h3 | none | main | campaign-normal | 1 | load | load | h3 | 16 | 66152 | 66136 | 3306.8 | 16 | 16 | 11.0 | ok ×66152 |
| u10-normal-h3 | none | main | campaign-normal | 2 | load | load | h3 | 16 | 65336 | 65320 | 3266.0 | 16 | 16 | 11.3 | ok ×65336 |
| u10-normal-h3 | none | main | campaign-normal | 3 | load | load | h3 | 16 | 67132 | 67116 | 3355.8 | 16 | 16 | 10.9 | ok ×67132 |
| u2-normal-h2 | both | base | campaign-normal | 1 | load | load | h2 | 1 | 203 | 202 | 10.1 | 1 | 1 | 120.8 | ok ×203 |
| u2-normal-h2 | both | base | campaign-normal | 2 | load | load | h2 | 1 | 198 | 197 | 9.8 | 1 | 1 | 135.5 | ok ×198 |
| u2-normal-h2 | both | base | campaign-normal | 3 | load | load | h2 | 1 | 204 | 203 | 10.2 | 1 | 1 | 119.8 | ok ×204 |
| u2-normal-h2 | both | base4 | campaign-normal | 1 | load | load | h2 | 1 | 197 | 196 | 9.8 | 1 | 1 | 127.6 | ok ×197 |
| u2-normal-h2 | both | base4 | campaign-normal | 2 | load | load | h2 | 1 | 204 | 203 | 10.2 | 1 | 1 | 120.9 | ok ×204 |
| u2-normal-h2 | both | base4 | campaign-normal | 3 | load | load | h2 | 1 | 203 | 202 | 10.1 | 1 | 1 | 114.1 | ok ×203 |
| u2-normal-h2 | both | main | campaign-normal | 1 | load | load | h2 | 1 | 201 | 200 | 10.0 | 1 | 1 | 123.6 | ok ×201 |
| u2-normal-h2 | both | main | campaign-normal | 2 | load | load | h2 | 1 | 203 | 202 | 10.1 | 1 | 1 | 124.2 | ok ×203 |
| u2-normal-h2 | both | main | campaign-normal | 3 | load | load | h2 | 1 | 204 | 203 | 10.2 | 1 | 1 | 117.8 | ok ×204 |
| u2-normal-h2 | none | base | campaign-normal | 1 | load | load | h2 | 16 | 59087 | 59071 | 2953.6 | 16 | 16 | 12.2 | ok ×59087 |
| u2-normal-h2 | none | base | campaign-normal | 2 | load | load | h2 | 16 | 60148 | 60132 | 3006.6 | 15 | 16 | 11.9 | ok ×60148 |
| u2-normal-h2 | none | base | campaign-normal | 3 | load | load | h2 | 16 | 59379 | 59363 | 2968.2 | 15 | 16 | 11.7 | ok ×59379 |
| u2-normal-h2 | none | base4 | campaign-normal | 1 | load | load | h2 | 16 | 59329 | 59313 | 2965.7 | 16 | 16 | 11.8 | ok ×59329 |
| u2-normal-h2 | none | base4 | campaign-normal | 2 | load | load | h2 | 16 | 59387 | 59371 | 2968.6 | 16 | 16 | 12.1 | ok ×59387 |
| u2-normal-h2 | none | base4 | campaign-normal | 3 | load | load | h2 | 16 | 59604 | 59588 | 2979.4 | 15 | 16 | 12.0 | ok ×59604 |
| u2-normal-h2 | none | main | campaign-normal | 1 | load | load | h2 | 16 | 58858 | 58842 | 2942.1 | 16 | 16 | 11.9 | ok ×58858 |
| u2-normal-h2 | none | main | campaign-normal | 2 | load | load | h2 | 16 | 60169 | 60153 | 3007.7 | 16 | 16 | 11.7 | ok ×60169 |
| u2-normal-h2 | none | main | campaign-normal | 3 | load | load | h2 | 16 | 59837 | 59821 | 2991.1 | 16 | 16 | 11.9 | ok ×59837 |
| u2-normal-h2 | waf | base | campaign-normal | 1 | load | load | h2 | 1 | 204 | 203 | 10.2 | 1 | 1 | 130.1 | ok ×204 |
| u2-normal-h2 | waf | base | campaign-normal | 2 | load | load | h2 | 1 | 208 | 207 | 10.3 | 1 | 1 | 117.5 | ok ×208 |
| u2-normal-h2 | waf | base | campaign-normal | 3 | load | load | h2 | 1 | 207 | 206 | 10.3 | 1 | 1 | 118.9 | ok ×207 |
| u2-normal-h2 | waf | base4 | campaign-normal | 1 | load | load | h2 | 1 | 205 | 204 | 10.2 | 1 | 1 | 130.8 | ok ×205 |
| u2-normal-h2 | waf | base4 | campaign-normal | 2 | load | load | h2 | 1 | 207 | 206 | 10.3 | 1 | 1 | 114.2 | ok ×207 |
| u2-normal-h2 | waf | base4 | campaign-normal | 3 | load | load | h2 | 1 | 210 | 209 | 10.4 | 1 | 1 | 121.2 | ok ×210 |
| u2-normal-h2 | waf | main | campaign-normal | 1 | load | load | h2 | 1 | 207 | 206 | 10.3 | 1 | 1 | 116.7 | ok ×207 |
| u2-normal-h2 | waf | main | campaign-normal | 2 | load | load | h2 | 1 | 210 | 209 | 10.4 | 1 | 1 | 111.8 | ok ×210 |
| u2-normal-h2 | waf | main | campaign-normal | 3 | load | load | h2 | 1 | 203 | 202 | 10.1 | 1 | 1 | 119.5 | ok ×203 |
| u2-normal-h2 | wasm | base | campaign-normal | 1 | load | load | h2 | 2 | 10869 | 10867 | 543.4 | 2 | 2 | 11.7 | ok ×10869 |
| u2-normal-h2 | wasm | base | campaign-normal | 2 | load | load | h2 | 2 | 11542 | 11540 | 577.0 | 2 | 2 | 9.9 | ok ×11542 |
| u2-normal-h2 | wasm | base | campaign-normal | 3 | load | load | h2 | 2 | 11744 | 11742 | 587.1 | 2 | 2 | 9.8 | ok ×11744 |
| u2-normal-h2 | wasm | base4 | campaign-normal | 1 | load | load | h2 | 2 | 11753 | 11751 | 587.5 | 2 | 2 | 9.2 | ok ×11753 |
| u2-normal-h2 | wasm | base4 | campaign-normal | 2 | load | load | h2 | 2 | 11481 | 11479 | 574.0 | 2 | 2 | 9.7 | ok ×11481 |
| u2-normal-h2 | wasm | base4 | campaign-normal | 3 | load | load | h2 | 2 | 11646 | 11644 | 582.2 | 2 | 2 | 9.7 | ok ×11646 |
| u2-normal-h2 | wasm | main | campaign-normal | 1 | load | load | h2 | 2 | 11440 | 11438 | 571.9 | 2 | 2 | 9.8 | ok ×11440 |
| u2-normal-h2 | wasm | main | campaign-normal | 2 | load | load | h2 | 2 | 11676 | 11674 | 583.7 | 2 | 2 | 10.0 | ok ×11676 |
| u2-normal-h2 | wasm | main | campaign-normal | 3 | load | load | h2 | 2 | 11715 | 11713 | 585.6 | 2 | 2 | 9.8 | ok ×11715 |

## Failed, invalid or inconclusive runs

None.
