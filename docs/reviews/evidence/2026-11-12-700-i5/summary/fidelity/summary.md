# Inspection assessment summary

Plan `inspection-assessment-plan/v1`, profile `qualification`. Runs: fidelity.

> an I5 result shows that the instrument captured a runtime; it is not A1-A18 acceptance. Throughput verdicts between the ADR baseline and post-I1 main are informational because I1 has no request-path enforcement

## Targets

| Label | Adapter | SHA-256 | Go | Revision | Modified |
| --- | --- | --- | --- | --- | --- |
| base | baseline-858590dc | `520febcbad5669da0ea8ad98e29f0bb78304817511cd7ed63bfb48e9d63ea52f` | go1.27.3 | `858590dc7c430d24cd33b096a81ca6051591b427` | false |
| main | current | `db6c6378b782b25e41fdfdbaa139517440e7beec44349a5fc6ee8df57800d7c8` | go1.27.4 | `363d6cd4a9fef4c020b25e5c810b2bd34e481732` | false |

## Cells

Goodput counts verified responses only. Spread is (max − min) / median over every repetition.

| Scenario | Variant | Target | Rule | n | instrument-valid | correct | median rps | min | max | spread | CPU fractions | peak cgroup MiB |
| --- | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- | --- |
| f3-admission-pending0 | both | base | record | 1 | 1 | 0 | 0.0 | 0.0 | 0.0 | n/a | 0.99 | 768.0 |
| f3-admission-pending0 | both | main | record | 1 | 1 | 0 | 0.0 | 0.0 | 0.0 | n/a | 1.00 | 759.5 |
| f3-admission-pending0 | none | base | record | 1 | 1 | 1 | 15.6 | 15.6 | 15.6 | 0.000 | 0.52 | 28.5 |
| f3-admission-pending0 | none | main | record | 1 | 1 | 1 | 15.6 | 15.6 | 15.6 | 0.000 | 0.51 | 43.4 |
| f3-admission-pending16 | both | base | record | 1 | 1 | 0 | 0.0 | 0.0 | 0.0 | n/a | 0.99 | 768.0 |
| f3-admission-pending16 | both | main | record | 1 | 1 | 0 | 0.0 | 0.0 | 0.0 | n/a | 1.00 | 767.4 |
| f3-admission-pending16 | none | base | record | 1 | 1 | 1 | 2.6 | 2.6 | 2.6 | 0.000 | 0.57 | 29.3 |
| f3-admission-pending16 | none | main | record | 1 | 1 | 1 | 0.8 | 0.8 | 0.8 | 0.000 | 0.57 | 42.0 |
| f4-stalled-body | both | base | record | 1 | 1 | 1 | 0.0 | 0.0 | 0.0 | n/a | n/a | 182.2 |
| f4-stalled-body | both | main | record | 1 | 1 | 1 | 0.0 | 0.0 | 0.0 | n/a | n/a | 196.5 |
| f4-stalled-body | none | base | record | 1 | 1 | 1 | 0.0 | 0.0 | 0.0 | n/a | n/a | 20.7 |
| f4-stalled-body | none | main | record | 1 | 1 | 1 | 0.0 | 0.0 | 0.0 | n/a | n/a | 36.4 |
| r1-slow-upload-c16-h1 | wasm | base | record | 1 | 1 | 1 | 3.2 | 3.2 | 3.2 | 0.000 | 0.70 | 234.7 |
| r1-slow-upload-c16-h1 | wasm | main | record | 1 | 1 | 1 | 3.2 | 3.2 | 3.2 | 0.000 | 0.69 | 250.0 |
| r2-fast-nobody-h1 | none | base | capacity | 1 | 1 | 1 | 14215.0 | 14215.0 | 14215.0 | 0.000 | 0.70 | 23.3 |
| r2-fast-nobody-h1 | none | main | capacity | 1 | 1 | 1 | 13892.7 | 13892.7 | 13892.7 | 0.000 | 0.70 | 38.4 |
| r2-fast-nobody-h1 | wasm | base | capacity | 1 | 1 | 1 | 4251.8 | 4251.8 | 4251.8 | 0.000 | 0.94 | 311.6 |
| r2-fast-nobody-h1 | wasm | main | capacity | 1 | 1 | 1 | 4304.4 | 4304.4 | 4304.4 | 0.000 | 0.94 | 416.4 |
| s11l-arg-amplification-low-h1 | none | base | record | 1 | 1 | 1 | 1938.5 | 1938.5 | 1938.5 | 0.000 | 0.37 | 20.4 |
| s11l-arg-amplification-low-h1 | none | main | record | 1 | 1 | 1 | 2031.8 | 2031.8 | 2031.8 | 0.000 | 0.34 | 33.7 |
| s11l-arg-amplification-low-h1 | waf | base | record | 1 | 1 | 1 | 5.0 | 5.0 | 5.0 | 0.000 | 0.52 | 343.4 |
| s11l-arg-amplification-low-h1 | waf | main | record | 1 | 1 | 1 | 5.0 | 5.0 | 5.0 | 0.000 | 0.52 | 346.1 |
| s6-static-h2 | both | base | capacity | 1 | 1 | 0 | 14.2 | 14.2 | 14.2 | 0.000 | 0.99 | 529.0 |
| s6-static-h2 | both | main | capacity | 1 | 1 | 0 | 13.4 | 13.4 | 13.4 | 0.000 | 0.99 | 534.8 |
| s6-static-h2 | none | base | capacity | 1 | 1 | 1 | 1380.7 | 1380.7 | 1380.7 | 0.000 | 0.62 | 37.4 |
| s6-static-h2 | none | main | capacity | 1 | 1 | 1 | 1387.5 | 1387.5 | 1387.5 | 0.000 | 0.62 | 56.0 |
| s7-cache-h1 | both | base | capacity | 1 | 1 | 0 | 18.2 | 18.2 | 18.2 | 0.000 | 0.99 | 504.9 |
| s7-cache-h1 | both | main | capacity | 1 | 1 | 0 | 17.7 | 17.7 | 17.7 | 0.000 | 0.99 | 450.6 |
| s7-cache-h1 | none | base | capacity | 1 | 1 | 1 | 2913.2 | 2913.2 | 2913.2 | 0.000 | 0.67 | 204.0 |
| s7-cache-h1 | none | main | capacity | 1 | 1 | 1 | 2560.6 | 2560.6 | 2560.6 | 0.000 | 0.67 | 223.3 |
| s8-slow-clients-h1 | both | base | record | 1 | 1 | 0 | 0.1 | 0.1 | 0.1 | 0.000 | 0.99 | 726.8 |
| s8-slow-clients-h1 | both | main | record | 1 | 1 | 1 | 0.4 | 0.4 | 0.4 | 0.000 | 0.99 | 758.6 |
| s8-slow-clients-h1 | none | base | record | 1 | 1 | 1 | 19.2 | 19.2 | 19.2 | 0.000 | 0.56 | 50.4 |
| s8-slow-clients-h1 | none | main | record | 1 | 1 | 1 | 19.2 | 19.2 | 19.2 | 0.000 | 0.56 | 66.8 |
| s8-slow-clients-h1 | waf | base | record | 1 | 1 | 1 | 0.1 | 0.1 | 0.1 | 0.000 | 0.99 | 583.1 |
| s8-slow-clients-h1 | waf | main | record | 1 | 1 | 1 | 0.1 | 0.1 | 0.1 | 0.000 | 0.99 | 605.4 |
| s8-slow-clients-h1 | wasm | base | record | 1 | 1 | 1 | 19.2 | 19.2 | 19.2 | 0.000 | 0.69 | 302.2 |
| s8-slow-clients-h1 | wasm | main | record | 1 | 1 | 1 | 19.2 | 19.2 | 19.2 | 0.000 | 0.69 | 284.0 |
| s9-reload-churn | both | base | record | 1 | 1 | 0 | 2.5 | 2.5 | 2.5 | 0.000 | n/a | 669.5 |
| s9-reload-churn | both | main | record | 1 | 1 | 0 | 4.2 | 4.2 | 4.2 | 0.000 | n/a | 768.2 |

## Phases and classes

One row per repetition, phase and workload class. Goodput counts verified responses that started and finished in the window; requests crossing in from warm-up and those outstanding at the window end are excluded from it.

| Scenario | Variant | Target | Run | Rep | Phase | Class | Proto | C | Attempts | Verified in window | Goodput rps | Crossed in | Outstanding | p99 ms | Outcomes |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| f3-admission-pending0 | both | base | fidelity | 1 | load | admission | h2 | 128 | 129 | 0 | 0.0 | 0 | 128 | n/a | error:reset ×128, status:500 ×1 |
| f3-admission-pending0 | both | base | fidelity | 1 | load | probe | h2 | 1 | 13 | 12 | 0.6 | 0 | 1 | 2470.9 | ok ×13 |
| f3-admission-pending0 | both | main | fidelity | 1 | load | admission | h2 | 128 | 129 | 0 | 0.0 | 0 | 128 | n/a | error:reset ×1, error:timeout ×127, status:500 ×1 |
| f3-admission-pending0 | both | main | fidelity | 1 | load | probe | h2 | 1 | 12 | 11 | 0.6 | 0 | 1 | 3455.9 | ok ×12 |
| f3-admission-pending0 | none | base | fidelity | 1 | load | admission | h2 | 128 | 101149 | 312 | 15.6 | 0 | 128 | 564.2 | ok ×320, status:503 ×100829 |
| f3-admission-pending0 | none | base | fidelity | 1 | load | probe | h2 | 1 | 198 | 198 | 9.9 | 0 | 0 | 8.3 | ok ×198 |
| f3-admission-pending0 | none | main | fidelity | 1 | load | admission | h2 | 128 | 93364 | 312 | 15.6 | 0 | 128 | 562.3 | ok ×320, status:503 ×93044 |
| f3-admission-pending0 | none | main | fidelity | 1 | load | probe | h2 | 1 | 198 | 198 | 9.9 | 0 | 0 | 7.6 | ok ×198 |
| f3-admission-pending16 | both | base | fidelity | 1 | load | admission | h2 | 128 | 129 | 0 | 0.0 | 0 | 128 | n/a | error:reset ×124, status:500 ×5 |
| f3-admission-pending16 | both | base | fidelity | 1 | load | probe | h2 | 1 | 11 | 10 | 0.5 | 0 | 1 | 3034.3 | ok ×11 |
| f3-admission-pending16 | both | main | fidelity | 1 | load | admission | h2 | 128 | 129 | 0 | 0.0 | 0 | 128 | n/a | error:eof ×128, status:500 ×1 |
| f3-admission-pending16 | both | main | fidelity | 1 | load | probe | h2 | 1 | 13 | 12 | 0.6 | 0 | 1 | 2656.7 | ok ×13 |
| f3-admission-pending16 | none | base | fidelity | 1 | load | admission | h2 | 128 | 328211 | 53 | 2.6 | 0 | 128 | 13022.7 | ok ×64, status:503 ×328147 |
| f3-admission-pending16 | none | base | fidelity | 1 | load | probe | h2 | 1 | 197 | 197 | 9.8 | 0 | 0 | 7.7 | ok ×197 |
| f3-admission-pending16 | none | main | fidelity | 1 | load | admission | h2 | 128 | 325551 | 15 | 0.8 | 0 | 128 | 17031.9 | ok ×28, status:503 ×325523 |
| f3-admission-pending16 | none | main | fidelity | 1 | load | probe | h2 | 1 | 197 | 197 | 9.8 | 0 | 0 | 7.2 | ok ×197 |
| r1-slow-upload-c16-h1 | wasm | base | fidelity | 1 | load | slow | h1 | 16 | 80 | 64 | 3.2 | 16 | 16 | 3945.4 | ok ×80 |
| r1-slow-upload-c16-h1 | wasm | base | fidelity | 1 | load | probe | h1 | 4 | 54587 | 54583 | 2729.2 | 4 | 4 | 7.9 | ok ×54587 |
| r1-slow-upload-c16-h1 | wasm | main | fidelity | 1 | load | slow | h1 | 16 | 80 | 64 | 3.2 | 16 | 16 | 3964.8 | ok ×80 |
| r1-slow-upload-c16-h1 | wasm | main | fidelity | 1 | load | probe | h1 | 4 | 54387 | 54383 | 2719.2 | 4 | 4 | 8.1 | ok ×54387 |
| r2-fast-nobody-h1 | none | base | fidelity | 1 | load | load | h1 | 16 | 284315 | 284299 | 14215.0 | 16 | 16 | 6.1 | ok ×284315 |
| r2-fast-nobody-h1 | none | main | fidelity | 1 | load | load | h1 | 16 | 277870 | 277854 | 13892.7 | 16 | 16 | 6.7 | ok ×277870 |
| r2-fast-nobody-h1 | wasm | base | fidelity | 1 | load | load | h1 | 16 | 85051 | 85035 | 4251.8 | 16 | 16 | 13.6 | ok ×85051 |
| r2-fast-nobody-h1 | wasm | main | fidelity | 1 | load | load | h1 | 16 | 86103 | 86087 | 4304.4 | 16 | 16 | 13.7 | ok ×86103 |
| s11l-arg-amplification-low-h1 | none | base | fidelity | 1 | c1 | load | h1 | 1 | 19386 | 19385 | 1938.5 | 1 | 1 | 1.2 | ok ×19386 |
| s11l-arg-amplification-low-h1 | none | base | fidelity | 1 | c2 | load | h1 | 2 | 43531 | 43529 | 4352.9 | 2 | 2 | 1.0 | ok ×43531 |
| s11l-arg-amplification-low-h1 | none | main | fidelity | 1 | c1 | load | h1 | 1 | 20319 | 20318 | 2031.8 | 1 | 1 | 1.2 | ok ×20319 |
| s11l-arg-amplification-low-h1 | none | main | fidelity | 1 | c2 | load | h1 | 2 | 42199 | 42197 | 4219.7 | 2 | 2 | 1.2 | ok ×42199 |
| s11l-arg-amplification-low-h1 | waf | base | fidelity | 1 | c1 | load | h1 | 1 | 51 | 50 | 5.0 | 1 | 1 | 229.6 | ok ×51 |
| s11l-arg-amplification-low-h1 | waf | base | fidelity | 1 | c2 | load | h1 | 2 | 93 | 91 | 9.1 | 2 | 2 | 261.8 | ok ×93 |
| s11l-arg-amplification-low-h1 | waf | main | fidelity | 1 | c1 | load | h1 | 1 | 51 | 50 | 5.0 | 1 | 1 | 224.7 | ok ×51 |
| s11l-arg-amplification-low-h1 | waf | main | fidelity | 1 | c2 | load | h1 | 2 | 91 | 89 | 8.9 | 2 | 2 | 268.2 | ok ×91 |
| s6-static-h2 | both | base | fidelity | 1 | load | load | h2 | 128 | 420 | 283 | 14.2 | 128 | 128 | 8910.2 | ok ×410, status:500 ×10 |
| s6-static-h2 | both | main | fidelity | 1 | load | load | h2 | 128 | 408 | 268 | 13.4 | 128 | 128 | 9274.3 | ok ×395, status:500 ×13 |
| s6-static-h2 | none | base | fidelity | 1 | load | load | h2 | 128 | 27742 | 27614 | 1380.7 | 128 | 128 | 134.1 | ok ×27742 |
| s6-static-h2 | none | main | fidelity | 1 | load | load | h2 | 128 | 27879 | 27751 | 1387.5 | 128 | 128 | 134.4 | ok ×27879 |
| s7-cache-h1 | both | base | fidelity | 1 | cold | cold | h1 | 16 | 383 | 365 | 18.2 | 16 | 16 | 1097.4 | ok ×381, status:500 ×2 |
| s7-cache-h1 | both | base | fidelity | 1 | warm | warm | h1 | 16 | 385 | 367 | 18.4 | 16 | 16 | 1081.8 | ok ×383, status:500 ×2 |
| s7-cache-h1 | both | main | fidelity | 1 | cold | cold | h1 | 16 | 376 | 354 | 17.7 | 16 | 16 | 1141.8 | ok ×370, status:500 ×6 |
| s7-cache-h1 | both | main | fidelity | 1 | warm | warm | h1 | 16 | 383 | 363 | 18.1 | 16 | 16 | 1205.4 | ok ×379, status:500 ×4 |
| s7-cache-h1 | none | base | fidelity | 1 | cold | cold | h1 | 16 | 58281 | 58265 | 2913.2 | 13 | 16 | 16.8 | ok ×58281 |
| s7-cache-h1 | none | base | fidelity | 1 | warm | warm | h1 | 16 | 120249 | 120233 | 6011.6 | 16 | 16 | 9.0 | ok ×120249 |
| s7-cache-h1 | none | main | fidelity | 1 | cold | cold | h1 | 16 | 51227 | 51211 | 2560.6 | 16 | 16 | 21.9 | ok ×51227 |
| s7-cache-h1 | none | main | fidelity | 1 | warm | warm | h1 | 16 | 93781 | 93767 | 4688.4 | 16 | 14 | 10.7 | ok ×93781 |
| s8-slow-clients-h1 | both | base | fidelity | 1 | load | slow | h1 | 96 | 100 | 3 | 0.1 | 96 | 96 | 13315.0 | ok ×99, status:500 ×1 |
| s8-slow-clients-h1 | both | base | fidelity | 1 | load | slow-cancel | h1 | 32 | 320 | 0 | 0.0 | 32 | 32 | n/a | client-cancel ×320 |
| s8-slow-clients-h1 | both | base | fidelity | 1 | load | probe | h1 | 4 | 495 | 491 | 24.6 | 4 | 4 | 2620.5 | ok ×495 |
| s8-slow-clients-h1 | both | main | fidelity | 1 | load | slow | h1 | 96 | 104 | 8 | 0.4 | 96 | 96 | 14133.2 | ok ×104 |
| s8-slow-clients-h1 | both | main | fidelity | 1 | load | slow-cancel | h1 | 32 | 320 | 0 | 0.0 | 32 | 32 | n/a | client-cancel ×320 |
| s8-slow-clients-h1 | both | main | fidelity | 1 | load | probe | h1 | 4 | 310 | 306 | 15.3 | 4 | 4 | 2816.8 | ok ×310 |
| s8-slow-clients-h1 | none | base | fidelity | 1 | load | slow | h1 | 96 | 480 | 384 | 19.2 | 96 | 96 | 3936.6 | ok ×480 |
| s8-slow-clients-h1 | none | base | fidelity | 1 | load | slow-cancel | h1 | 32 | 320 | 0 | 0.0 | 32 | 32 | n/a | client-cancel ×320 |
| s8-slow-clients-h1 | none | base | fidelity | 1 | load | probe | h1 | 4 | 180683 | 180679 | 9034.0 | 4 | 4 | 1.4 | ok ×180683 |
| s8-slow-clients-h1 | none | main | fidelity | 1 | load | slow | h1 | 96 | 480 | 384 | 19.2 | 96 | 96 | 3932.1 | ok ×480 |
| s8-slow-clients-h1 | none | main | fidelity | 1 | load | slow-cancel | h1 | 32 | 320 | 0 | 0.0 | 32 | 32 | n/a | client-cancel ×320 |
| s8-slow-clients-h1 | none | main | fidelity | 1 | load | probe | h1 | 4 | 178253 | 178249 | 8912.5 | 4 | 4 | 1.5 | ok ×178253 |
| s8-slow-clients-h1 | waf | base | fidelity | 1 | load | slow | h1 | 96 | 98 | 2 | 0.1 | 96 | 96 | 4410.8 | ok ×98 |
| s8-slow-clients-h1 | waf | base | fidelity | 1 | load | slow-cancel | h1 | 32 | 320 | 0 | 0.0 | 32 | 32 | n/a | client-cancel ×320 |
| s8-slow-clients-h1 | waf | base | fidelity | 1 | load | probe | h1 | 4 | 513 | 509 | 25.4 | 4 | 4 | 2159.1 | ok ×513 |
| s8-slow-clients-h1 | waf | main | fidelity | 1 | load | slow | h1 | 96 | 99 | 3 | 0.1 | 96 | 96 | 4515.1 | ok ×99 |
| s8-slow-clients-h1 | waf | main | fidelity | 1 | load | slow-cancel | h1 | 32 | 320 | 0 | 0.0 | 32 | 32 | n/a | client-cancel ×320 |
| s8-slow-clients-h1 | waf | main | fidelity | 1 | load | probe | h1 | 4 | 392 | 388 | 19.4 | 4 | 4 | 2824.1 | ok ×392 |
| s8-slow-clients-h1 | wasm | base | fidelity | 1 | load | slow | h1 | 96 | 480 | 384 | 19.2 | 96 | 96 | 3987.5 | ok ×480 |
| s8-slow-clients-h1 | wasm | base | fidelity | 1 | load | slow-cancel | h1 | 32 | 320 | 0 | 0.0 | 32 | 32 | n/a | client-cancel ×320 |
| s8-slow-clients-h1 | wasm | base | fidelity | 1 | load | probe | h1 | 4 | 52252 | 52248 | 2612.4 | 4 | 4 | 8.2 | ok ×52252 |
| s8-slow-clients-h1 | wasm | main | fidelity | 1 | load | slow | h1 | 96 | 480 | 384 | 19.2 | 96 | 96 | 3965.8 | ok ×480 |
| s8-slow-clients-h1 | wasm | main | fidelity | 1 | load | slow-cancel | h1 | 32 | 320 | 0 | 0.0 | 32 | 32 | n/a | client-cancel ×320 |
| s8-slow-clients-h1 | wasm | main | fidelity | 1 | load | probe | h1 | 4 | 51265 | 51261 | 2563.1 | 4 | 4 | 8.7 | ok ×51265 |
| s9-reload-churn | both | base | fidelity | 1 | load | load | h1 | 16 | 1752376 | 101 | 2.5 | 16 | 16 | 1883.6 | error:eof ×11, error:refused ×1752259, error:reset ×5, ok ×101 |
| s9-reload-churn | both | base | fidelity | 1 | load | inflight | h2 | 16 | 1575775 | 1 | 0.0 | 16 | 16 | 4896.6 | error:refused ×1575742, error:reset ×16, error:stream-reset ×16, ok ×1 |
| s9-reload-churn | both | main | fidelity | 1 | load | load | h1 | 16 | 1289175 | 168 | 4.2 | 16 | 16 | 1890.5 | error:eof ×15, error:other ×3, error:refused ×1288975, error:reset ×12, ok ×168, status:500 ×2 |
| s9-reload-churn | both | main | fidelity | 1 | load | inflight | h2 | 16 | 1148686 | 0 | 0.0 | 16 | 16 | n/a | error:eof ×16, error:refused ×1148616, error:stream-reset ×53, status:500 ×1 |

## F4 probes

| Scenario | Variant | Target | Run | Rep | Proto | Mode | Upload | Upload s | Client watchdog ended it | Sibling | Sibling ms | Same connection | Dials/expected |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | --- | --- | ---: | --- | --- |
| f4-stalled-body | both | base | fidelity | 1 | h1 | stall | error:timeout | 20.0 | true | ok | 6 | false | 2/2 |
| f4-stalled-body | both | base | fidelity | 1 | h1 | slow | status | 7.6 | false | ok | 6 | false | 2/2 |
| f4-stalled-body | both | base | fidelity | 1 | h2 | stall | error:timeout | 20.0 | true | ok | 5 | true | 1/1 |
| f4-stalled-body | both | base | fidelity | 1 | h2 | slow | status | 8.1 | false | ok | 6 | true | 1/1 |
| f4-stalled-body | both | base | fidelity | 1 | h3 | stall | error:timeout | 20.0 | true | ok | 6 | true | 1/1 |
| f4-stalled-body | both | base | fidelity | 1 | h3 | slow | status | 8.1 | false | ok | 6 | true | 1/1 |
| f4-stalled-body | both | main | fidelity | 1 | h1 | stall | error:timeout | 20.0 | true | ok | 5 | false | 2/2 |
| f4-stalled-body | both | main | fidelity | 1 | h1 | slow | status | 7.6 | false | ok | 6 | false | 2/2 |
| f4-stalled-body | both | main | fidelity | 1 | h2 | stall | error:timeout | 20.0 | true | ok | 6 | true | 1/1 |
| f4-stalled-body | both | main | fidelity | 1 | h2 | slow | status | 8.1 | false | ok | 5 | true | 1/1 |
| f4-stalled-body | both | main | fidelity | 1 | h3 | stall | error:timeout | 20.0 | true | ok | 5 | true | 1/1 |
| f4-stalled-body | both | main | fidelity | 1 | h3 | slow | status | 8.1 | false | ok | 6 | true | 1/1 |
| f4-stalled-body | none | base | fidelity | 1 | h1 | stall | error:timeout | 20.0 | true | ok | 2 | false | 2/2 |
| f4-stalled-body | none | base | fidelity | 1 | h1 | slow | status | 7.6 | false | ok | 1 | false | 2/2 |
| f4-stalled-body | none | base | fidelity | 1 | h2 | stall | error:timeout | 20.0 | true | ok | 2 | true | 1/1 |
| f4-stalled-body | none | base | fidelity | 1 | h2 | slow | status | 8.0 | false | ok | 2 | true | 1/1 |
| f4-stalled-body | none | base | fidelity | 1 | h3 | stall | error:timeout | 20.0 | true | ok | 2 | true | 1/1 |
| f4-stalled-body | none | base | fidelity | 1 | h3 | slow | status | 7.6 | false | ok | 2 | true | 1/1 |
| f4-stalled-body | none | main | fidelity | 1 | h1 | stall | error:timeout | 20.0 | true | ok | 1 | false | 2/2 |
| f4-stalled-body | none | main | fidelity | 1 | h1 | slow | status | 7.6 | false | ok | 1 | false | 2/2 |
| f4-stalled-body | none | main | fidelity | 1 | h2 | stall | error:timeout | 20.0 | true | ok | 2 | true | 1/1 |
| f4-stalled-body | none | main | fidelity | 1 | h2 | slow | status | 8.0 | false | ok | 1 | true | 1/1 |
| f4-stalled-body | none | main | fidelity | 1 | h3 | stall | error:timeout | 20.0 | true | ok | 1 | true | 1/1 |
| f4-stalled-body | none | main | fidelity | 1 | h3 | slow | status | 7.6 | false | ok | 2 | true | 1/1 |

## Failed, invalid or inconclusive runs

- fidelity f3-admission-pending0/both/base rep1: runtime-finding: jul exited during the run: signal killed; phase load class admission: error:reset ×128; phase load class admission: status:500 ×1; oom_kill delta 1 in /sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/app.slice (hierarchical memory.events oom_kill; includes sibling scopes)
- fidelity f3-admission-pending0/both/main rep1: runtime-finding: jul exited during the run: signal killed; phase load class admission: error:reset ×1; phase load class admission: error:timeout ×127; phase load class admission: status:500 ×1; oom_kill delta 1 in /sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/app.slice (hierarchical memory.events oom_kill; includes sibling scopes)
- fidelity f3-admission-pending16/both/base rep1: runtime-finding: jul exited during the run: signal killed; phase load class admission: error:reset ×124; phase load class admission: status:500 ×5; oom_kill delta 1 in /sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/app.slice (hierarchical memory.events oom_kill; includes sibling scopes)
- fidelity f3-admission-pending16/both/main rep1: runtime-finding: jul exited during the run: signal killed; phase load class admission: error:eof ×128; phase load class admission: status:500 ×1; oom_kill delta 1 in /sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/app.slice (hierarchical memory.events oom_kill; includes sibling scopes)
- fidelity s6-static-h2/both/base rep1: runtime-finding: phase load class load: status:500 ×10
- fidelity s6-static-h2/both/main rep1: runtime-finding: phase load class load: status:500 ×13
- fidelity s7-cache-h1/both/base rep1: runtime-finding: phase cold class cold: status:500 ×2; phase warm class warm: status:500 ×2
- fidelity s7-cache-h1/both/main rep1: runtime-finding: phase cold class cold: status:500 ×6; phase warm class warm: status:500 ×4
- fidelity s8-slow-clients-h1/both/base rep1: runtime-finding: phase load class slow: status:500 ×1
- fidelity s9-reload-churn/both/base rep1: runtime-finding: jul exited during the run: signal killed; phase load class load: error:eof ×11; phase load class load: error:refused ×1752259; phase load class load: error:reset ×5; phase load class inflight: error:refused ×1575742; phase load class inflight: error:reset ×16; oom_kill delta 1 in /sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/app.slice (hierarchical memory.events oom_kill; includes sibling scopes)
- fidelity s9-reload-churn/both/main rep1: runtime-finding: jul exited during the run: signal killed; phase load class load: error:eof ×15; phase load class load: error:other ×3; phase load class load: error:refused ×1288975; phase load class load: error:reset ×12; phase load class load: status:500 ×2; phase load class inflight: error:eof ×16; phase load class inflight: error:refused ×1148616; phase load class inflight: status:500 ×1; oom_kill delta 1 in /sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/app.slice (hierarchical memory.events oom_kill; includes sibling scopes)
