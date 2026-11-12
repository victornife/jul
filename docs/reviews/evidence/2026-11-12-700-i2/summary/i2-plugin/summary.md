# Inspection assessment summary

Plan `inspection-assessment-plan/v3`, profile `qualification`. Runs: i2-plugin.

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
| p1-large-body-plugin-h1 | none | cand | record | 1 | 1 | 1 | 1773.7 | 1773.7 | 1773.7 | 0.000 | 0.97 | 49.2 |
| p1-large-body-plugin-h1 | none | main | record | 1 | 1 | 1 | 2968.4 | 2968.4 | 2968.4 | 0.000 | 0.96 | 58.8 |
| p1-large-body-plugin-h1 | wasm | cand | record | 1 | 1 | 0 | 158.1 | 158.1 | 158.1 | 0.000 | 0.98 | 460.9 |
| p1-large-body-plugin-h1 | wasm | main | record | 1 | 1 | 0 | 150.1 | 150.1 | 150.1 | 0.000 | 0.98 | 481.3 |
| p2-saturation-plugin-h1 | wasm | cand | record | 1 | 1 | 1 | 19.9 | 19.9 | 19.9 | 0.000 | 0.67 | 517.9 |
| p2-saturation-plugin-h1 | wasm | main | record | 1 | 0 | 1 | 24.0 | 24.0 | 24.0 | 0.000 | 0.68 | 519.7 |
| p2h-saturation-hostile-h1 | wasm | cand | record | 1 | 0 | 0 | 0.0 | 0.0 | 0.0 | n/a | n/a | 680.8 |
| p2h-saturation-hostile-h1 | wasm | main | record | 1 | 0 | 1 | 24.0 | 24.0 | 24.0 | 0.000 | 0.67 | 509.6 |
| p2o-saturation-plugin-observe-h1 | wasm | cand | record | 1 | 1 | 1 | 24.0 | 24.0 | 24.0 | 0.000 | 0.70 | 524.8 |
| p2o-saturation-plugin-observe-h1 | wasm | main | record | 1 | 0 | 1 | 24.0 | 24.0 | 24.0 | 0.000 | 0.66 | 504.1 |
| p3-nobody-plugin-h1 | none | cand | capacity | 1 | 1 | 1 | 3133.1 | 3133.1 | 3133.1 | 0.000 | 0.72 | 24.8 |
| p3-nobody-plugin-h1 | none | main | capacity | 1 | 1 | 1 | 2928.9 | 2928.9 | 2928.9 | 0.000 | 0.71 | 25.3 |
| p3-nobody-plugin-h1 | wasm | cand | capacity | 1 | 1 | 0 | 1695.0 | 1695.0 | 1695.0 | 0.000 | 0.95 | 704.5 |
| p3-nobody-plugin-h1 | wasm | main | capacity | 1 | 1 | 0 | 1646.8 | 1646.8 | 1646.8 | 0.000 | 0.96 | 395.3 |
| p4-reload-budget-plugin | wasm | cand | record | 1 | 1 | 1 | 1593.6 | 1593.6 | 1593.6 | 0.000 | 0.95 | 678.5 |
| p4-reload-budget-plugin | wasm | main | record | 1 | 1 | 0 | 1151.1 | 1151.1 | 1151.1 | 0.000 | 0.96 | 671.8 |
| r1-slow-upload-c16-h1 | wasm | cand | record | 1 | 1 | 1 | 3.2 | 3.2 | 3.2 | 0.000 | 0.74 | 233.9 |
| r1-slow-upload-c16-h1 | wasm | main | record | 1 | 1 | 1 | 3.2 | 3.2 | 3.2 | 0.000 | 0.80 | 225.4 |
| r2-fast-nobody-h1 | none | cand | capacity | 1 | 1 | 1 | 9920.5 | 9920.5 | 9920.5 | 0.000 | 0.75 | 22.2 |
| r2-fast-nobody-h1 | none | main | capacity | 1 | 1 | 1 | 9134.5 | 9134.5 | 9134.5 | 0.000 | 0.72 | 21.8 |
| r2-fast-nobody-h1 | wasm | cand | capacity | 1 | 1 | 1 | 3575.2 | 3575.2 | 3575.2 | 0.000 | 0.95 | 411.5 |
| r2-fast-nobody-h1 | wasm | main | capacity | 1 | 1 | 1 | 3245.4 | 3245.4 | 3245.4 | 0.000 | 0.96 | 282.3 |
| s6-static-h2 | both | cand | capacity | 1 | 1 | 0 | 13.9 | 13.9 | 13.9 | 0.000 | 0.99 | 463.0 |
| s6-static-h2 | both | main | capacity | 1 | 1 | 0 | 12.9 | 12.9 | 12.9 | 0.000 | 1.00 | 499.7 |
| s6-static-h2 | none | cand | capacity | 1 | 1 | 1 | 1050.1 | 1050.1 | 1050.1 | 0.000 | 0.63 | 40.0 |
| s6-static-h2 | none | main | capacity | 1 | 1 | 1 | 1158.4 | 1158.4 | 1158.4 | 0.000 | 0.63 | 39.4 |
| s6-static-h2 | wasm | cand | capacity | 1 | 1 | 0 | 403.4 | 403.4 | 403.4 | 0.000 | 0.97 | 383.7 |
| s6-static-h2 | wasm | main | capacity | 1 | 1 | 0 | 385.2 | 385.2 | 385.2 | 0.000 | 0.97 | 450.2 |
| s7-cache-h1 | both | cand | capacity | 1 | 1 | 0 | 14.8 | 14.8 | 14.8 | 0.000 | 1.00 | 515.9 |
| s7-cache-h1 | both | main | capacity | 1 | 1 | 0 | 18.1 | 18.1 | 18.1 | 0.000 | 0.99 | 445.7 |
| s7-cache-h1 | none | cand | capacity | 1 | 1 | 1 | 1865.2 | 1865.2 | 1865.2 | 0.000 | 0.66 | 192.1 |
| s7-cache-h1 | none | main | capacity | 1 | 1 | 1 | 2292.3 | 2292.3 | 2292.3 | 0.000 | 0.68 | 200.2 |
| s7-cache-h1 | wasm | cand | capacity | 1 | 1 | 0 | 330.6 | 330.6 | 330.6 | 0.000 | 0.99 | 570.2 |
| s7-cache-h1 | wasm | main | capacity | 1 | 1 | 0 | 375.6 | 375.6 | 375.6 | 0.000 | 0.99 | 554.7 |
| s8-slow-clients-h1 | both | cand | record | 1 | 1 | 0 | 0.1 | 0.1 | 0.1 | 0.000 | 0.99 | 637.6 |
| s8-slow-clients-h1 | both | main | record | 1 | 1 | 1 | 0.1 | 0.1 | 0.1 | 0.000 | 1.00 | 651.7 |
| s8-slow-clients-h1 | none | cand | record | 1 | 1 | 1 | 19.2 | 19.2 | 19.2 | 0.000 | 0.56 | 51.2 |
| s8-slow-clients-h1 | none | main | record | 1 | 1 | 1 | 19.2 | 19.2 | 19.2 | 0.000 | 0.57 | 50.9 |
| s8-slow-clients-h1 | waf | cand | record | 1 | 1 | 1 | 0.2 | 0.2 | 0.2 | 0.000 | 1.00 | 549.5 |
| s8-slow-clients-h1 | waf | main | record | 1 | 1 | 1 | 0.1 | 0.1 | 0.1 | 0.000 | 0.99 | 553.3 |
| s8-slow-clients-h1 | wasm | cand | record | 1 | 1 | 1 | 19.2 | 19.2 | 19.2 | 0.000 | 0.71 | 247.5 |
| s8-slow-clients-h1 | wasm | main | record | 1 | 1 | 1 | 19.2 | 19.2 | 19.2 | 0.000 | 0.72 | 260.2 |

## Phases and classes

One row per repetition, phase and workload class. Goodput counts verified responses that started and finished in the window; requests crossing in from warm-up and those outstanding at the window end are excluded from it.

| Scenario | Variant | Target | Run | Rep | Phase | Class | Proto | C | Attempts | Verified in window | Goodput rps | Crossed in | Outstanding | p99 ms | Outcomes |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| p1-large-body-plugin-h1 | none | cand | i2-plugin | 1 | load | load | h1 | 64 | 35538 | 35474 | 1773.7 | 64 | 64 | 75.9 | ok ×35538 |
| p1-large-body-plugin-h1 | none | main | i2-plugin | 1 | load | load | h1 | 64 | 59433 | 59369 | 2968.4 | 64 | 64 | 54.3 | ok ×59433 |
| p1-large-body-plugin-h1 | wasm | cand | i2-plugin | 1 | load | load | h1 | 64 | 3247 | 3162 | 158.1 | 64 | 64 | 705.2 | ok ×3225, status:500 ×22 |
| p1-large-body-plugin-h1 | wasm | main | i2-plugin | 1 | load | load | h1 | 64 | 3085 | 3001 | 150.1 | 64 | 64 | 706.0 | ok ×3065, status:500 ×20 |
| p2-saturation-plugin-h1 | wasm | cand | i2-plugin | 1 | load | load | h1 | 160 | 1071 | 399 | 19.9 | 133 | 133 | 5329.9 | ok ×532, status:503-inspection-overloaded ×539 |
| p2-saturation-plugin-h1 | wasm | cand | i2-plugin | 1 | load | probe | h1 | 2 | 35748 | 35746 | 1787.3 | 2 | 2 | 6.5 | ok ×35748 |
| p2-saturation-plugin-h1 | wasm | main | i2-plugin | 1 | load | load | h1 | 160 | 640 | 480 | 24.0 | 160 | 160 | 5311.6 | ok ×640 |
| p2-saturation-plugin-h1 | wasm | main | i2-plugin | 1 | load | probe | h1 | 2 | 37830 | 37828 | 1891.4 | 2 | 2 | 4.0 | ok ×37830 |
| p2h-saturation-hostile-h1 | wasm | cand | i2-plugin | 1 | load | load | h1 | 160 | 1704801 | 0 | 0.0 | 157 | 159 | n/a | error:refused ×1704801 |
| p2h-saturation-hostile-h1 | wasm | cand | i2-plugin | 1 | load | probe | h1 | 2 | 32469 | 0 | 0.0 | 2 | 2 | n/a | error:refused ×32469 |
| p2h-saturation-hostile-h1 | wasm | main | i2-plugin | 1 | load | load | h1 | 160 | 640 | 480 | 24.0 | 160 | 160 | 5257.8 | ok ×640 |
| p2h-saturation-hostile-h1 | wasm | main | i2-plugin | 1 | load | probe | h1 | 2 | 36972 | 36970 | 1848.5 | 2 | 2 | 8.6 | ok ×36972 |
| p2o-saturation-plugin-observe-h1 | wasm | cand | i2-plugin | 1 | load | load | h1 | 160 | 640 | 480 | 24.0 | 160 | 160 | 5365.6 | ok ×640 |
| p2o-saturation-plugin-observe-h1 | wasm | cand | i2-plugin | 1 | load | probe | h1 | 2 | 31486 | 31484 | 1574.2 | 2 | 2 | 5.9 | ok ×31486 |
| p2o-saturation-plugin-observe-h1 | wasm | main | i2-plugin | 1 | load | load | h1 | 160 | 640 | 480 | 24.0 | 160 | 160 | 5174.8 | ok ×640 |
| p2o-saturation-plugin-observe-h1 | wasm | main | i2-plugin | 1 | load | probe | h1 | 2 | 42682 | 42680 | 2134.0 | 2 | 2 | 6.0 | ok ×42682 |
| p3-nobody-plugin-h1 | none | cand | i2-plugin | 1 | load | load | h1 | 16 | 62678 | 62662 | 3133.1 | 15 | 16 | 18.5 | ok ×62678 |
| p3-nobody-plugin-h1 | none | cand | i2-plugin | 1 | load | load | h1 | 16 | 68193 | 68177 | 3408.8 | 16 | 16 | 17.9 | ok ×68193 |
| p3-nobody-plugin-h1 | none | main | i2-plugin | 1 | load | load | h1 | 16 | 58595 | 58579 | 2928.9 | 16 | 16 | 19.4 | ok ×58595 |
| p3-nobody-plugin-h1 | none | main | i2-plugin | 1 | load | load | h1 | 16 | 63561 | 63545 | 3177.2 | 16 | 16 | 19.0 | ok ×63561 |
| p3-nobody-plugin-h1 | wasm | cand | i2-plugin | 1 | load | load | h1 | 16 | 33918 | 33900 | 1695.0 | 16 | 16 | 31.4 | ok ×33916, status:500 ×2 |
| p3-nobody-plugin-h1 | wasm | cand | i2-plugin | 1 | load | load | h1 | 16 | 35077 | 35061 | 1753.0 | 16 | 16 | 31.3 | ok ×35077 |
| p3-nobody-plugin-h1 | wasm | main | i2-plugin | 1 | load | load | h1 | 16 | 32953 | 32936 | 1646.8 | 16 | 16 | 31.7 | ok ×32952, status:500 ×1 |
| p3-nobody-plugin-h1 | wasm | main | i2-plugin | 1 | load | load | h1 | 16 | 33922 | 33906 | 1695.3 | 16 | 16 | 31.8 | ok ×33922 |
| p4-reload-budget-plugin | wasm | cand | i2-plugin | 1 | load | load | h1 | 16 | 63760 | 63744 | 1593.6 | 16 | 16 | 40.0 | ok ×63760 |
| p4-reload-budget-plugin | wasm | cand | i2-plugin | 1 | load | inflight | h2 | 16 | 224 | 160 | 4.0 | 16 | 16 | 3088.0 | error:stream-reset ×48, ok ×176 |
| p4-reload-budget-plugin | wasm | main | i2-plugin | 1 | load | load | h1 | 16 | 46061 | 46044 | 1151.1 | 16 | 16 | 63.0 | error:body-eof ×1, ok ×46060 |
| p4-reload-budget-plugin | wasm | main | i2-plugin | 1 | load | inflight | h2 | 16 | 240 | 144 | 3.6 | 16 | 16 | 3145.5 | error:stream-reset ×80, ok ×160 |
| r1-slow-upload-c16-h1 | wasm | cand | i2-plugin | 1 | load | slow | h1 | 16 | 80 | 64 | 3.2 | 16 | 16 | 4013.8 | ok ×80 |
| r1-slow-upload-c16-h1 | wasm | cand | i2-plugin | 1 | load | probe | h1 | 4 | 45005 | 45001 | 2250.1 | 4 | 4 | 9.4 | ok ×45005 |
| r1-slow-upload-c16-h1 | wasm | main | i2-plugin | 1 | load | slow | h1 | 16 | 80 | 64 | 3.2 | 16 | 16 | 3980.2 | ok ×80 |
| r1-slow-upload-c16-h1 | wasm | main | i2-plugin | 1 | load | probe | h1 | 4 | 35249 | 35245 | 1762.2 | 4 | 4 | 11.0 | ok ×35249 |
| r2-fast-nobody-h1 | none | cand | i2-plugin | 1 | load | load | h1 | 16 | 198427 | 198411 | 9920.5 | 16 | 16 | 8.7 | ok ×198427 |
| r2-fast-nobody-h1 | none | main | i2-plugin | 1 | load | load | h1 | 16 | 182707 | 182691 | 9134.5 | 16 | 16 | 9.6 | ok ×182707 |
| r2-fast-nobody-h1 | wasm | cand | i2-plugin | 1 | load | load | h1 | 16 | 71519 | 71503 | 3575.2 | 16 | 16 | 16.2 | ok ×71519 |
| r2-fast-nobody-h1 | wasm | main | i2-plugin | 1 | load | load | h1 | 16 | 64924 | 64908 | 3245.4 | 16 | 16 | 18.1 | ok ×64924 |
| s6-static-h2 | both | cand | i2-plugin | 1 | load | load | h2 | 128 | 410 | 279 | 13.9 | 128 | 128 | 7841.7 | ok ×406, status:500 ×4 |
| s6-static-h2 | both | main | i2-plugin | 1 | load | load | h2 | 128 | 388 | 258 | 12.9 | 128 | 128 | 9876.6 | ok ×384, status:500 ×4 |
| s6-static-h2 | none | cand | i2-plugin | 1 | load | load | h2 | 128 | 21130 | 21002 | 1050.1 | 128 | 128 | 164.2 | ok ×21130 |
| s6-static-h2 | none | main | i2-plugin | 1 | load | load | h2 | 128 | 23296 | 23168 | 1158.4 | 128 | 128 | 162.0 | ok ×23296 |
| s6-static-h2 | wasm | cand | i2-plugin | 1 | load | load | h2 | 128 | 8199 | 8069 | 403.4 | 128 | 128 | 612.5 | ok ×8197, status:500 ×2 |
| s6-static-h2 | wasm | main | i2-plugin | 1 | load | load | h2 | 128 | 7835 | 7704 | 385.2 | 128 | 128 | 567.0 | ok ×7832, status:500 ×3 |
| s7-cache-h1 | both | cand | i2-plugin | 1 | cold | cold | h1 | 16 | 313 | 295 | 14.8 | 16 | 16 | 1360.1 | ok ×311, status:500 ×2 |
| s7-cache-h1 | both | cand | i2-plugin | 1 | warm | warm | h1 | 16 | 367 | 346 | 17.3 | 16 | 16 | 1159.2 | ok ×362, status:500 ×5 |
| s7-cache-h1 | both | main | i2-plugin | 1 | cold | cold | h1 | 16 | 377 | 361 | 18.1 | 16 | 16 | 1100.6 | ok ×377 |
| s7-cache-h1 | both | main | i2-plugin | 1 | warm | warm | h1 | 16 | 380 | 362 | 18.1 | 16 | 16 | 1161.1 | ok ×378, status:500 ×2 |
| s7-cache-h1 | none | cand | i2-plugin | 1 | cold | cold | h1 | 16 | 37317 | 37303 | 1865.2 | 16 | 14 | 23.7 | ok ×37317 |
| s7-cache-h1 | none | cand | i2-plugin | 1 | warm | warm | h1 | 16 | 94258 | 94244 | 4712.2 | 15 | 14 | 10.2 | ok ×94258 |
| s7-cache-h1 | none | main | i2-plugin | 1 | cold | cold | h1 | 16 | 45861 | 45847 | 2292.3 | 16 | 14 | 19.9 | ok ×45861 |
| s7-cache-h1 | none | main | i2-plugin | 1 | warm | warm | h1 | 16 | 56617 | 56602 | 2830.1 | 15 | 15 | 15.7 | ok ×56617 |
| s7-cache-h1 | wasm | cand | i2-plugin | 1 | cold | cold | h1 | 16 | 6628 | 6612 | 330.6 | 16 | 16 | 99.1 | ok ×6628 |
| s7-cache-h1 | wasm | cand | i2-plugin | 1 | warm | warm | h1 | 16 | 7110 | 7048 | 352.4 | 16 | 16 | 192.8 | ok ×7062, status:500 ×48 |
| s7-cache-h1 | wasm | main | i2-plugin | 1 | cold | cold | h1 | 16 | 7529 | 7513 | 375.6 | 16 | 16 | 82.0 | ok ×7529 |
| s7-cache-h1 | wasm | main | i2-plugin | 1 | warm | warm | h1 | 16 | 7584 | 7526 | 376.3 | 16 | 16 | 214.9 | ok ×7542, status:500 ×42 |
| s8-slow-clients-h1 | both | cand | i2-plugin | 1 | load | slow | h1 | 96 | 99 | 3 | 0.1 | 96 | 96 | 4658.7 | ok ×99 |
| s8-slow-clients-h1 | both | cand | i2-plugin | 1 | load | slow-cancel | h1 | 32 | 320 | 0 | 0.0 | 32 | 32 | n/a | client-cancel ×320 |
| s8-slow-clients-h1 | both | cand | i2-plugin | 1 | load | probe | h1 | 4 | 435 | 431 | 21.6 | 4 | 4 | 2248.2 | ok ×434, status:500 ×1 |
| s8-slow-clients-h1 | both | main | i2-plugin | 1 | load | slow | h1 | 96 | 98 | 2 | 0.1 | 96 | 96 | 4520.9 | ok ×98 |
| s8-slow-clients-h1 | both | main | i2-plugin | 1 | load | slow-cancel | h1 | 32 | 320 | 0 | 0.0 | 32 | 32 | n/a | client-cancel ×320 |
| s8-slow-clients-h1 | both | main | i2-plugin | 1 | load | probe | h1 | 4 | 512 | 508 | 25.4 | 4 | 4 | 2491.6 | ok ×512 |
| s8-slow-clients-h1 | none | cand | i2-plugin | 1 | load | slow | h1 | 96 | 480 | 384 | 19.2 | 96 | 96 | 3930.0 | ok ×480 |
| s8-slow-clients-h1 | none | cand | i2-plugin | 1 | load | slow-cancel | h1 | 32 | 320 | 0 | 0.0 | 32 | 32 | n/a | client-cancel ×320 |
| s8-slow-clients-h1 | none | cand | i2-plugin | 1 | load | probe | h1 | 4 | 143362 | 143358 | 7167.9 | 4 | 4 | 1.9 | ok ×143362 |
| s8-slow-clients-h1 | none | main | i2-plugin | 1 | load | slow | h1 | 96 | 480 | 384 | 19.2 | 96 | 96 | 3948.9 | ok ×480 |
| s8-slow-clients-h1 | none | main | i2-plugin | 1 | load | slow-cancel | h1 | 32 | 320 | 0 | 0.0 | 32 | 32 | n/a | client-cancel ×320 |
| s8-slow-clients-h1 | none | main | i2-plugin | 1 | load | probe | h1 | 4 | 122990 | 122986 | 6149.3 | 4 | 4 | 2.7 | ok ×122990 |
| s8-slow-clients-h1 | waf | cand | i2-plugin | 1 | load | slow | h1 | 96 | 100 | 4 | 0.2 | 96 | 96 | 4670.0 | ok ×100 |
| s8-slow-clients-h1 | waf | cand | i2-plugin | 1 | load | slow-cancel | h1 | 32 | 320 | 0 | 0.0 | 32 | 32 | n/a | client-cancel ×320 |
| s8-slow-clients-h1 | waf | cand | i2-plugin | 1 | load | probe | h1 | 4 | 501 | 497 | 24.9 | 4 | 4 | 2339.3 | ok ×501 |
| s8-slow-clients-h1 | waf | main | i2-plugin | 1 | load | slow | h1 | 96 | 98 | 2 | 0.1 | 96 | 96 | 4368.8 | ok ×98 |
| s8-slow-clients-h1 | waf | main | i2-plugin | 1 | load | slow-cancel | h1 | 32 | 320 | 0 | 0.0 | 32 | 32 | n/a | client-cancel ×320 |
| s8-slow-clients-h1 | waf | main | i2-plugin | 1 | load | probe | h1 | 4 | 568 | 564 | 28.2 | 4 | 4 | 2354.9 | ok ×568 |
| s8-slow-clients-h1 | wasm | cand | i2-plugin | 1 | load | slow | h1 | 96 | 480 | 384 | 19.2 | 96 | 96 | 3977.7 | ok ×480 |
| s8-slow-clients-h1 | wasm | cand | i2-plugin | 1 | load | slow-cancel | h1 | 32 | 320 | 0 | 0.0 | 32 | 32 | n/a | client-cancel ×320 |
| s8-slow-clients-h1 | wasm | cand | i2-plugin | 1 | load | probe | h1 | 4 | 46913 | 46909 | 2345.4 | 4 | 4 | 8.9 | ok ×46913 |
| s8-slow-clients-h1 | wasm | main | i2-plugin | 1 | load | slow | h1 | 96 | 480 | 384 | 19.2 | 96 | 96 | 4008.9 | ok ×480 |
| s8-slow-clients-h1 | wasm | main | i2-plugin | 1 | load | slow-cancel | h1 | 32 | 320 | 0 | 0.0 | 32 | 32 | n/a | client-cancel ×320 |
| s8-slow-clients-h1 | wasm | main | i2-plugin | 1 | load | probe | h1 | 4 | 42311 | 42307 | 2115.3 | 4 | 4 | 9.4 | ok ×42311 |

## Non-success outcome latency

Completed attempts started in the window, per outcome (for example `status:503` for overload), including those that finished after it.

| Scenario | Variant | Target | Run | Rep | Phase | Class | Outcome | n | p50 ms | p95 ms | p99 ms | max ms |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: |
| p1-large-body-plugin-h1 | wasm | cand | i2-plugin | 1 | load | load | status:500 | 22 | 172.5 | 333.2 | 373.6 | 560.5 |
| p1-large-body-plugin-h1 | wasm | main | i2-plugin | 1 | load | load | status:500 | 20 | 267.5 | 380.7 | 380.7 | 382.2 |
| p2-saturation-plugin-h1 | wasm | cand | i2-plugin | 1 | load | load | status:503-inspection-overloaded | 539 | 1.8 | 157.8 | 240.7 | 244.5 |
| p2h-saturation-hostile-h1 | wasm | cand | i2-plugin | 1 | load | load | error:refused | 1704801 | 1.3 | 5.0 | 7.6 | 334.6 |
| p2h-saturation-hostile-h1 | wasm | cand | i2-plugin | 1 | load | probe | error:refused | 32469 | 0.6 | 4.3 | 6.9 | 331.4 |
| p3-nobody-plugin-h1 | wasm | cand | i2-plugin | 1 | load | load | status:500 | 2 | 133.6 | 133.6 | 133.6 | 156.3 |
| p3-nobody-plugin-h1 | wasm | main | i2-plugin | 1 | load | load | status:500 | 1 | 115.8 | 115.8 | 115.8 | 115.8 |
| p4-reload-budget-plugin | wasm | cand | i2-plugin | 1 | load | inflight | error:stream-reset | 48 | 2250.9 | 2298.4 | 2312.0 | 2369.8 |
| p4-reload-budget-plugin | wasm | main | i2-plugin | 1 | load | load | error:body-eof | 1 | 22.0 | 22.0 | 22.0 | 22.0 |
| p4-reload-budget-plugin | wasm | main | i2-plugin | 1 | load | inflight | error:stream-reset | 80 | 2293.5 | 2948.9 | 2956.0 | 2956.3 |
| s6-static-h2 | both | cand | i2-plugin | 1 | load | load | status:500 | 4 | 2761.9 | 2876.0 | 2876.0 | 3531.6 |
| s6-static-h2 | both | main | i2-plugin | 1 | load | load | status:500 | 4 | 1884.9 | 2049.0 | 2049.0 | 3967.5 |
| s6-static-h2 | wasm | cand | i2-plugin | 1 | load | load | status:500 | 2 | 176.0 | 176.0 | 176.0 | 471.0 |
| s6-static-h2 | wasm | main | i2-plugin | 1 | load | load | status:500 | 3 | 443.4 | 443.4 | 443.4 | 502.4 |
| s7-cache-h1 | both | cand | i2-plugin | 1 | cold | cold | status:500 | 2 | 415.4 | 415.4 | 415.4 | 438.3 |
| s7-cache-h1 | both | cand | i2-plugin | 1 | warm | warm | status:500 | 5 | 231.0 | 255.5 | 255.5 | 270.5 |
| s7-cache-h1 | both | main | i2-plugin | 1 | warm | warm | status:500 | 2 | 127.9 | 127.9 | 127.9 | 292.7 |
| s7-cache-h1 | wasm | cand | i2-plugin | 1 | warm | warm | status:500 | 48 | 240.9 | 455.1 | 496.6 | 532.5 |
| s7-cache-h1 | wasm | main | i2-plugin | 1 | warm | warm | status:500 | 42 | 232.9 | 419.0 | 456.2 | 572.0 |
| s8-slow-clients-h1 | both | cand | i2-plugin | 1 | load | slow-cancel | client-cancel | 320 | 2000.8 | 2002.3 | 2003.4 | 2003.6 |
| s8-slow-clients-h1 | both | cand | i2-plugin | 1 | load | probe | status:500 | 1 | 2836.4 | 2836.4 | 2836.4 | 2836.4 |
| s8-slow-clients-h1 | both | main | i2-plugin | 1 | load | slow-cancel | client-cancel | 320 | 2000.8 | 2002.4 | 2002.9 | 2004.0 |
| s8-slow-clients-h1 | none | cand | i2-plugin | 1 | load | slow-cancel | client-cancel | 320 | 2000.3 | 2001.2 | 2001.5 | 2001.9 |
| s8-slow-clients-h1 | none | main | i2-plugin | 1 | load | slow-cancel | client-cancel | 320 | 2000.2 | 2001.1 | 2001.4 | 2001.8 |
| s8-slow-clients-h1 | waf | cand | i2-plugin | 1 | load | slow-cancel | client-cancel | 320 | 2000.7 | 2002.2 | 2003.1 | 2003.8 |
| s8-slow-clients-h1 | waf | main | i2-plugin | 1 | load | slow-cancel | client-cancel | 320 | 2000.5 | 2002.6 | 2004.1 | 2004.6 |
| s8-slow-clients-h1 | wasm | cand | i2-plugin | 1 | load | slow-cancel | client-cancel | 320 | 2000.2 | 2001.0 | 2001.3 | 2001.5 |
| s8-slow-clients-h1 | wasm | main | i2-plugin | 1 | load | slow-cancel | client-cancel | 320 | 2000.3 | 2001.1 | 2001.3 | 2001.4 |

## Failed, invalid or inconclusive runs

- i2-plugin p1-large-body-plugin-h1/wasm/cand rep1: runtime-finding: phase load class load: status:500 ×22
- i2-plugin p1-large-body-plugin-h1/wasm/main rep1: runtime-finding: phase load class load: status:500 ×20
- i2-plugin p2-saturation-plugin-h1/wasm/main rep1: instrument-invalid: unexercised: inspection_rejected_enforce = 0, want at least 1; unexercised: class status:503-inspection-overloaded = 0, want at least 1
- i2-plugin p2h-saturation-hostile-h1/wasm/cand rep1: instrument-invalid: unexercised: inspection_rejected_enforce unavailable; unexercised: class status:503-inspection-overloaded = 0, want at least 1
- i2-plugin p2h-saturation-hostile-h1/wasm/cand rep1: runtime-finding: jul exited during the run: signal killed; phase load class load: error:refused ×1704801; phase load class probe: error:refused ×32469; oom_kill delta 1 in /sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/app.slice (hierarchical memory.events oom_kill; includes sibling scopes)
- i2-plugin p2h-saturation-hostile-h1/wasm/main rep1: instrument-invalid: unexercised: inspection_rejected_enforce = 0, want at least 1; unexercised: class status:503-inspection-overloaded = 0, want at least 1
- i2-plugin p2o-saturation-plugin-observe-h1/wasm/main rep1: instrument-invalid: unexercised: inspection_rejected_observe = 0, want at least 1
- i2-plugin p3-nobody-plugin-h1/wasm/cand rep1: runtime-finding: phase load class load: status:500 ×2
- i2-plugin p3-nobody-plugin-h1/wasm/main rep1: runtime-finding: phase load class load: status:500 ×1
- i2-plugin p4-reload-budget-plugin/wasm/main rep1: runtime-finding: phase load class load: error:body-eof ×1
- i2-plugin s6-static-h2/both/cand rep1: runtime-finding: phase load class load: status:500 ×4
- i2-plugin s6-static-h2/both/main rep1: runtime-finding: phase load class load: status:500 ×4
- i2-plugin s6-static-h2/wasm/cand rep1: runtime-finding: phase load class load: status:500 ×2
- i2-plugin s6-static-h2/wasm/main rep1: runtime-finding: phase load class load: status:500 ×3
- i2-plugin s7-cache-h1/both/cand rep1: runtime-finding: phase cold class cold: status:500 ×2; phase warm class warm: status:500 ×5
- i2-plugin s7-cache-h1/both/main rep1: runtime-finding: phase warm class warm: status:500 ×2
- i2-plugin s7-cache-h1/wasm/cand rep1: runtime-finding: phase warm class warm: status:500 ×48
- i2-plugin s7-cache-h1/wasm/main rep1: runtime-finding: phase warm class warm: status:500 ×42
- i2-plugin s8-slow-clients-h1/both/cand rep1: runtime-finding: phase load class probe: status:500 ×1
