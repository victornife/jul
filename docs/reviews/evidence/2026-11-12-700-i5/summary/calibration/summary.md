# Inspection assessment summary

Plan `inspection-assessment-plan/v1`, profile `qualification`. Runs: calibration.

> an I5 result shows that the instrument captured a runtime; it is not A1-A18 acceptance. Throughput verdicts between the ADR baseline and post-I1 main are informational because I1 has no request-path enforcement

## Targets

| Label | Adapter | SHA-256 | Go | Revision | Modified |
| --- | --- | --- | --- | --- | --- |
| base | baseline-858590dc | `520febcbad5669da0ea8ad98e29f0bb78304817511cd7ed63bfb48e9d63ea52f` | go1.27.3 | `858590dc7c430d24cd33b096a81ca6051591b427` | false |

## Cells

Goodput counts verified responses only. Spread is (max − min) / median over every repetition.

| Scenario | Variant | Target | Rule | n | instrument-valid | correct | median rps | min | max | spread | CPU fractions | peak cgroup MiB |
| --- | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- | --- |
| cal-normal-h1 | both | base | record | 1 | 1 | 0 | 10.0 | 10.0 | 10.0 | 0.000 | 0.50 | 326.3 |
| cal-normal-h1 | none | base | record | 1 | 1 | 1 | 1532.3 | 1532.3 | 1532.3 | 0.000 | 0.33 | 24.6 |
| cal-normal-h1 | waf | base | record | 1 | 1 | 1 | 10.4 | 10.4 | 10.4 | 0.000 | 0.50 | 138.5 |
| cal-normal-h1 | wasm | base | record | 1 | 1 | 1 | 385.2 | 385.2 | 385.2 | 0.000 | 0.46 | 356.4 |
| cal-normal-h2 | both | base | record | 1 | 1 | 1 | 10.1 | 10.1 | 10.1 | 0.000 | 0.50 | 301.0 |
| cal-normal-h2 | none | base | record | 1 | 1 | 1 | 1252.1 | 1252.1 | 1252.1 | 0.000 | 0.42 | 25.4 |
| cal-normal-h2 | waf | base | record | 1 | 1 | 1 | 10.3 | 10.3 | 10.3 | 0.000 | 0.50 | 139.5 |
| cal-normal-h2 | wasm | base | record | 1 | 1 | 1 | 355.6 | 355.6 | 355.6 | 0.000 | 0.49 | 297.4 |
| cal-normal-h3 | both | base | record | 1 | 1 | 1 | 9.9 | 9.9 | 9.9 | 0.000 | 0.50 | 295.5 |
| cal-normal-h3 | none | base | record | 1 | 1 | 1 | 854.7 | 854.7 | 854.7 | 0.000 | 0.38 | 26.2 |

## Phases and classes

One row per repetition, phase and workload class. Goodput counts verified responses that started and finished in the window; requests crossing in from warm-up and those outstanding at the window end are excluded from it.

| Scenario | Variant | Target | Run | Rep | Phase | Class | Proto | C | Attempts | Verified in window | Goodput rps | Crossed in | Outstanding | p99 ms | Outcomes |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| cal-normal-h1 | both | base | calibration | 1 | c1 | load | h1 | 1 | 101 | 100 | 10.0 | 1 | 1 | 131.5 | ok ×101 |
| cal-normal-h1 | both | base | calibration | 1 | c2 | load | h1 | 2 | 185 | 183 | 18.3 | 2 | 2 | 155.6 | ok ×185 |
| cal-normal-h1 | both | base | calibration | 1 | c4 | load | h1 | 4 | 190 | 186 | 18.6 | 4 | 4 | 284.9 | ok ×190 |
| cal-normal-h1 | both | base | calibration | 1 | c8 | load | h1 | 8 | 193 | 185 | 18.5 | 8 | 8 | 553.1 | ok ×193 |
| cal-normal-h1 | both | base | calibration | 1 | c16 | load | h1 | 16 | 190 | 172 | 17.2 | 16 | 16 | 1156.5 | ok ×188, status:500 ×2 |
| cal-normal-h1 | none | base | calibration | 1 | c1 | load | h1 | 1 | 15324 | 15323 | 1532.3 | 1 | 1 | 1.5 | ok ×15324 |
| cal-normal-h1 | none | base | calibration | 1 | c2 | load | h1 | 2 | 31011 | 31009 | 3100.9 | 2 | 2 | 1.8 | ok ×31011 |
| cal-normal-h1 | none | base | calibration | 1 | c4 | load | h1 | 4 | 53745 | 53741 | 5374.1 | 3 | 4 | 2.6 | ok ×53745 |
| cal-normal-h1 | none | base | calibration | 1 | c8 | load | h1 | 8 | 64431 | 64423 | 6442.3 | 7 | 8 | 5.8 | ok ×64431 |
| cal-normal-h1 | none | base | calibration | 1 | c16 | load | h1 | 16 | 74840 | 74824 | 7482.4 | 16 | 16 | 8.6 | ok ×74840 |
| cal-normal-h1 | waf | base | calibration | 1 | c1 | load | h1 | 1 | 105 | 104 | 10.4 | 1 | 1 | 112.2 | ok ×105 |
| cal-normal-h1 | waf | base | calibration | 1 | c2 | load | h1 | 2 | 190 | 188 | 18.8 | 2 | 2 | 142.4 | ok ×190 |
| cal-normal-h1 | waf | base | calibration | 1 | c4 | load | h1 | 4 | 193 | 189 | 18.9 | 4 | 4 | 306.3 | ok ×193 |
| cal-normal-h1 | waf | base | calibration | 1 | c8 | load | h1 | 8 | 194 | 186 | 18.6 | 8 | 8 | 567.1 | ok ×194 |
| cal-normal-h1 | waf | base | calibration | 1 | c16 | load | h1 | 16 | 193 | 177 | 17.7 | 16 | 16 | 1185.9 | ok ×193 |
| cal-normal-h1 | wasm | base | calibration | 1 | c1 | load | h1 | 1 | 3853 | 3852 | 385.2 | 1 | 1 | 7.4 | ok ×3853 |
| cal-normal-h1 | wasm | base | calibration | 1 | c2 | load | h1 | 2 | 6356 | 6354 | 635.4 | 2 | 2 | 8.9 | ok ×6356 |
| cal-normal-h1 | wasm | base | calibration | 1 | c4 | load | h1 | 4 | 9649 | 9645 | 964.5 | 4 | 4 | 12.8 | ok ×9649 |
| cal-normal-h1 | wasm | base | calibration | 1 | c8 | load | h1 | 8 | 10487 | 10479 | 1047.9 | 8 | 8 | 21.6 | ok ×10487 |
| cal-normal-h1 | wasm | base | calibration | 1 | c16 | load | h1 | 16 | 10769 | 10753 | 1075.3 | 16 | 16 | 36.7 | ok ×10769 |
| cal-normal-h2 | both | base | calibration | 1 | c1 | load | h2 | 1 | 102 | 101 | 10.1 | 1 | 1 | 118.6 | ok ×102 |
| cal-normal-h2 | both | base | calibration | 1 | c2 | load | h2 | 2 | 185 | 183 | 18.3 | 2 | 2 | 142.3 | ok ×185 |
| cal-normal-h2 | both | base | calibration | 1 | c4 | load | h2 | 4 | 189 | 185 | 18.5 | 4 | 4 | 304.5 | ok ×189 |
| cal-normal-h2 | both | base | calibration | 1 | c8 | load | h2 | 8 | 194 | 186 | 18.6 | 8 | 8 | 561.1 | ok ×194 |
| cal-normal-h2 | both | base | calibration | 1 | c16 | load | h2 | 16 | 190 | 174 | 17.4 | 16 | 16 | 1140.7 | ok ×190 |
| cal-normal-h2 | none | base | calibration | 1 | c1 | load | h2 | 1 | 12522 | 12521 | 1252.1 | 1 | 1 | 2.1 | ok ×12522 |
| cal-normal-h2 | none | base | calibration | 1 | c2 | load | h2 | 2 | 19898 | 19896 | 1989.6 | 2 | 2 | 3.0 | ok ×19898 |
| cal-normal-h2 | none | base | calibration | 1 | c4 | load | h2 | 4 | 25930 | 25926 | 2592.6 | 4 | 4 | 4.8 | ok ×25930 |
| cal-normal-h2 | none | base | calibration | 1 | c8 | load | h2 | 8 | 28345 | 28337 | 2833.7 | 8 | 8 | 7.3 | ok ×28345 |
| cal-normal-h2 | none | base | calibration | 1 | c16 | load | h2 | 16 | 28873 | 28857 | 2885.7 | 16 | 16 | 12.7 | ok ×28873 |
| cal-normal-h2 | waf | base | calibration | 1 | c1 | load | h2 | 1 | 104 | 103 | 10.3 | 1 | 1 | 110.1 | ok ×104 |
| cal-normal-h2 | waf | base | calibration | 1 | c2 | load | h2 | 2 | 189 | 187 | 18.7 | 2 | 2 | 145.2 | ok ×189 |
| cal-normal-h2 | waf | base | calibration | 1 | c4 | load | h2 | 4 | 191 | 187 | 18.7 | 4 | 4 | 310.0 | ok ×191 |
| cal-normal-h2 | waf | base | calibration | 1 | c8 | load | h2 | 8 | 183 | 175 | 17.5 | 8 | 8 | 730.6 | ok ×183 |
| cal-normal-h2 | waf | base | calibration | 1 | c16 | load | h2 | 16 | 194 | 178 | 17.8 | 16 | 16 | 1100.5 | ok ×194 |
| cal-normal-h2 | wasm | base | calibration | 1 | c1 | load | h2 | 1 | 3557 | 3556 | 355.6 | 1 | 1 | 7.8 | ok ×3557 |
| cal-normal-h2 | wasm | base | calibration | 1 | c2 | load | h2 | 2 | 5630 | 5628 | 562.8 | 2 | 2 | 9.4 | ok ×5630 |
| cal-normal-h2 | wasm | base | calibration | 1 | c4 | load | h2 | 4 | 7863 | 7859 | 785.9 | 4 | 4 | 15.0 | ok ×7863 |
| cal-normal-h2 | wasm | base | calibration | 1 | c8 | load | h2 | 8 | 9326 | 9318 | 931.8 | 8 | 8 | 21.7 | ok ×9326 |
| cal-normal-h2 | wasm | base | calibration | 1 | c16 | load | h2 | 16 | 9264 | 9248 | 924.8 | 15 | 16 | 38.1 | ok ×9264 |
| cal-normal-h3 | both | base | calibration | 1 | c1 | load | h3 | 1 | 100 | 99 | 9.9 | 1 | 1 | 117.2 | ok ×100 |
| cal-normal-h3 | both | base | calibration | 1 | c2 | load | h3 | 2 | 183 | 181 | 18.1 | 2 | 2 | 147.7 | ok ×183 |
| cal-normal-h3 | both | base | calibration | 1 | c4 | load | h3 | 4 | 189 | 185 | 18.5 | 4 | 4 | 317.6 | ok ×189 |
| cal-normal-h3 | both | base | calibration | 1 | c8 | load | h3 | 8 | 189 | 181 | 18.1 | 8 | 8 | 559.9 | ok ×189 |
| cal-normal-h3 | both | base | calibration | 1 | c16 | load | h3 | 16 | 188 | 172 | 17.2 | 16 | 16 | 1179.0 | ok ×188 |
| cal-normal-h3 | none | base | calibration | 1 | c1 | load | h3 | 1 | 8548 | 8547 | 854.7 | 1 | 1 | 2.8 | ok ×8548 |
| cal-normal-h3 | none | base | calibration | 1 | c2 | load | h3 | 2 | 16586 | 16584 | 1658.4 | 1 | 2 | 2.8 | ok ×16586 |
| cal-normal-h3 | none | base | calibration | 1 | c4 | load | h3 | 4 | 24598 | 24594 | 2459.4 | 4 | 4 | 4.6 | ok ×24598 |
| cal-normal-h3 | none | base | calibration | 1 | c8 | load | h3 | 8 | 28945 | 28937 | 2893.7 | 7 | 8 | 7.7 | ok ×28945 |
| cal-normal-h3 | none | base | calibration | 1 | c16 | load | h3 | 16 | 30715 | 30699 | 3069.9 | 16 | 16 | 11.9 | ok ×30715 |

## Failed, invalid or inconclusive runs

- calibration cal-normal-h1/both/base rep1: runtime-finding: phase c16 class load: status:500 ×2
