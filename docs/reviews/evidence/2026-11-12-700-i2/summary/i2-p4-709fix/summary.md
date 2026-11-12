# Inspection assessment summary

Plan `inspection-assessment-plan/v3`, profile `qualification`. Runs: i2-p4-709fix.

> an I5 result shows that the instrument captured a runtime; it is not A1-A18 acceptance. Throughput verdicts between the ADR baseline and post-I1 main are informational because I1 has no request-path enforcement

## Targets

| Label | Adapter | SHA-256 | Go | Revision | Modified |
| --- | --- | --- | --- | --- | --- |
| cand2 | current | `8700867efe534b056ec515297796b1088e9f18985e542de370dca4906071572d` | go1.27.4 | `` |  |
| main | current | `65a0f8b9d42909b048fe3fe00ecda7dbf17ae28320bdef28bd82a1ea2f0add37` | go1.27.4 | `` |  |

## Cells

Goodput counts verified responses only. Spread is (max − min) / median over every repetition.

| Scenario | Variant | Target | Rule | n | instrument-valid | correct | median rps | min | max | spread | CPU fractions | peak cgroup MiB |
| --- | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- | --- |
| p4-reload-budget-plugin | wasm | cand2 | record | 3 | 3 | 1 | 2193.3 | 1883.0 | 2342.1 | 0.209 | 0.95 0.94 0.94 | 500.9 501.3 499.5 |
| p4-reload-budget-plugin | wasm | main | record | 3 | 3 | 1 | 1803.9 | 1412.0 | 1987.8 | 0.319 | 0.96 0.95 0.96 | 668.0 658.1 676.9 |

## Phases and classes

One row per repetition, phase and workload class. Goodput counts verified responses that started and finished in the window; requests crossing in from warm-up and those outstanding at the window end are excluded from it.

| Scenario | Variant | Target | Run | Rep | Phase | Class | Proto | C | Attempts | Verified in window | Goodput rps | Crossed in | Outstanding | p99 ms | Outcomes |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| p4-reload-budget-plugin | wasm | cand2 | i2-p4-709fix | 1 | load | load | h1 | 16 | 93701 | 93683 | 2342.1 | 16 | 16 | 22.5 | error:body-eof ×2, ok ×93699 |
| p4-reload-budget-plugin | wasm | cand2 | i2-p4-709fix | 1 | load | inflight | h2 | 16 | 224 | 144 | 3.6 | 16 | 16 | 3043.6 | error:stream-reset ×64, ok ×160 |
| p4-reload-budget-plugin | wasm | cand2 | i2-p4-709fix | 2 | load | load | h1 | 16 | 87750 | 87734 | 2193.3 | 16 | 16 | 24.9 | ok ×87750 |
| p4-reload-budget-plugin | wasm | cand2 | i2-p4-709fix | 2 | load | inflight | h2 | 16 | 224 | 146 | 3.6 | 16 | 16 | 3057.8 | error:stream-reset ×62, ok ×162 |
| p4-reload-budget-plugin | wasm | cand2 | i2-p4-709fix | 3 | load | load | h1 | 16 | 75334 | 75318 | 1883.0 | 16 | 16 | 29.8 | ok ×75334 |
| p4-reload-budget-plugin | wasm | cand2 | i2-p4-709fix | 3 | load | inflight | h2 | 16 | 224 | 148 | 3.7 | 16 | 16 | 3059.1 | error:body-stream-reset ×1, error:stream-reset ×59, ok ×164 |
| p4-reload-budget-plugin | wasm | main | i2-p4-709fix | 1 | load | load | h1 | 16 | 56496 | 56479 | 1412.0 | 16 | 16 | 61.5 | error:reset ×1, ok ×56495 |
| p4-reload-budget-plugin | wasm | main | i2-p4-709fix | 1 | load | inflight | h2 | 16 | 240 | 144 | 3.6 | 16 | 16 | 3147.6 | error:stream-reset ×80, ok ×160 |
| p4-reload-budget-plugin | wasm | main | i2-p4-709fix | 2 | load | load | h1 | 16 | 79526 | 79510 | 1987.8 | 16 | 16 | 34.4 | ok ×79526 |
| p4-reload-budget-plugin | wasm | main | i2-p4-709fix | 2 | load | inflight | h2 | 16 | 224 | 144 | 3.6 | 16 | 16 | 3116.8 | error:stream-reset ×64, ok ×160 |
| p4-reload-budget-plugin | wasm | main | i2-p4-709fix | 3 | load | load | h1 | 16 | 72172 | 72155 | 1803.9 | 16 | 16 | 36.7 | error:reset ×1, ok ×72171 |
| p4-reload-budget-plugin | wasm | main | i2-p4-709fix | 3 | load | inflight | h2 | 16 | 224 | 144 | 3.6 | 16 | 16 | 3069.9 | error:stream-reset ×64, ok ×160 |

## Non-success outcome latency

Completed attempts started in the window, per outcome (for example `status:503` for overload), including those that finished after it.

| Scenario | Variant | Target | Run | Rep | Phase | Class | Outcome | n | p50 ms | p95 ms | p99 ms | max ms |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: |
| p4-reload-budget-plugin | wasm | cand2 | i2-p4-709fix | 1 | load | load | error:body-eof | 2 | 14.2 | 14.2 | 14.2 | 31.1 |
| p4-reload-budget-plugin | wasm | cand2 | i2-p4-709fix | 1 | load | inflight | error:stream-reset | 64 | 2234.3 | 2867.8 | 2878.3 | 2881.0 |
| p4-reload-budget-plugin | wasm | cand2 | i2-p4-709fix | 2 | load | inflight | error:stream-reset | 62 | 2216.5 | 2989.3 | 3026.5 | 3026.7 |
| p4-reload-budget-plugin | wasm | cand2 | i2-p4-709fix | 3 | load | inflight | error:body-stream-reset | 1 | 3013.3 | 3013.3 | 3013.3 | 3013.3 |
| p4-reload-budget-plugin | wasm | cand2 | i2-p4-709fix | 3 | load | inflight | error:stream-reset | 59 | 2266.7 | 3013.1 | 3013.5 | 3013.5 |
| p4-reload-budget-plugin | wasm | main | i2-p4-709fix | 1 | load | load | error:reset | 1 | 11.7 | 11.7 | 11.7 | 11.7 |
| p4-reload-budget-plugin | wasm | main | i2-p4-709fix | 1 | load | inflight | error:stream-reset | 80 | 2252.9 | 2643.6 | 2644.6 | 2645.0 |
| p4-reload-budget-plugin | wasm | main | i2-p4-709fix | 2 | load | inflight | error:stream-reset | 64 | 2121.8 | 2999.5 | 3024.0 | 3024.2 |
| p4-reload-budget-plugin | wasm | main | i2-p4-709fix | 3 | load | load | error:reset | 1 | 0.6 | 0.6 | 0.6 | 0.6 |
| p4-reload-budget-plugin | wasm | main | i2-p4-709fix | 3 | load | inflight | error:stream-reset | 64 | 2255.1 | 2975.8 | 2982.2 | 2982.4 |

## Failed, invalid or inconclusive runs

- i2-p4-709fix p4-reload-budget-plugin/wasm/cand2 rep1: runtime-finding: phase load class load: error:body-eof ×2
- i2-p4-709fix p4-reload-budget-plugin/wasm/cand2 rep3: runtime-finding: phase load class inflight: error:body-stream-reset ×1
- i2-p4-709fix p4-reload-budget-plugin/wasm/main rep1: runtime-finding: phase load class load: error:reset ×1
- i2-p4-709fix p4-reload-budget-plugin/wasm/main rep3: runtime-finding: phase load class load: error:reset ×1
