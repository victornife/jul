# Inspection assessment summary

Plan `inspection-assessment-plan/v3`, profile `qualification`. Runs: ia-a1-after-soak.

> an I5 result shows that the instrument captured a runtime; it is not A1-A18 acceptance. Throughput verdicts between the ADR baseline and post-I1 main are informational because I1 has no request-path enforcement

## Targets

| Label | Adapter | SHA-256 | Go | Revision | Modified |
| --- | --- | --- | --- | --- | --- |
| before | current | `50a49b0bd2689f094b21c329ed3f0a51ef628799ef2c77f1cb4f222e46af51ab` | go1.27.4 | `ffbff4d05316ef6f2e01692cbf0e6582030d2d4e` | false |
| main | current | `13a7660d8fb18ddf3e172088a58d662adea0aa45c043a86fb2c37c1b5714995c` | go1.27.4 | `60415e5a71fd8ec26a7559424e3f43e6406738fe` | false |

## Cells

Goodput counts verified responses only. Spread is (max − min) / median over every repetition.

| Scenario | Variant | Target | Rule | n | instrument-valid | correct | median rps | min | max | spread | CPU fractions | peak cgroup MiB |
| --- | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- | --- |
| s11-arg-amplification-h1 | waf | before | record | 3 | 3 | 0 | 3.2 | 3.0 | 3.6 | 0.188 | 0.52 0.52 0.52 | 599.7 603.0 598.4 |
| s11-arg-amplification-h1 | waf | main | record | 3 | 3 | 0 | 3.7 | 2.9 | 3.8 | 0.243 | 0.52 0.52 0.53 | 599.2 603.1 604.6 |

## Phases and classes

One row per repetition, phase and workload class. Goodput counts verified responses that started and finished in the window; requests crossing in from warm-up and those outstanding at the window end are excluded from it.

| Scenario | Variant | Target | Run | Rep | Phase | Class | Proto | C | Attempts | Verified in window | Goodput rps | Crossed in | Outstanding | p99 ms | Outcomes |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| s11-arg-amplification-h1 | waf | before | ia-a1-after-soak | 1 | c1 | load | h1 | 1 | 31 | 30 | 3.0 | 1 | 1 | 357.5 | ok ×31 |
| s11-arg-amplification-h1 | waf | before | ia-a1-after-soak | 1 | c2 | load | h1 | 2 | 60 | 58 | 5.8 | 2 | 2 | 393.6 | ok ×60 |
| s11-arg-amplification-h1 | waf | before | ia-a1-after-soak | 1 | c4 | load | h1 | 4 | 631 | 52 | 5.2 | 4 | 4 | 767.5 | ok ×55, status:503-inspection-overloaded ×576 |
| s11-arg-amplification-h1 | waf | before | ia-a1-after-soak | 1 | c8 | load | h1 | 8 | 2701 | 48 | 4.8 | 8 | 8 | 842.4 | ok ×51, status:503-inspection-overloaded ×2650 |
| s11-arg-amplification-h1 | waf | before | ia-a1-after-soak | 1 | c16 | load | h1 | 16 | 6197 | 57 | 5.7 | 16 | 16 | 721.7 | ok ×59, status:503-inspection-overloaded ×6138 |
| s11-arg-amplification-h1 | waf | before | ia-a1-after-soak | 2 | c1 | load | h1 | 1 | 33 | 32 | 3.2 | 1 | 1 | 352.1 | ok ×33 |
| s11-arg-amplification-h1 | waf | before | ia-a1-after-soak | 2 | c2 | load | h1 | 2 | 78 | 76 | 7.6 | 2 | 2 | 317.1 | ok ×78 |
| s11-arg-amplification-h1 | waf | before | ia-a1-after-soak | 2 | c4 | load | h1 | 4 | 622 | 81 | 8.1 | 4 | 4 | 504.3 | ok ×84, status:503-inspection-overloaded ×538 |
| s11-arg-amplification-h1 | waf | before | ia-a1-after-soak | 2 | c8 | load | h1 | 8 | 3755 | 84 | 8.4 | 8 | 8 | 522.5 | ok ×86, status:503-inspection-overloaded ×3669 |
| s11-arg-amplification-h1 | waf | before | ia-a1-after-soak | 2 | c16 | load | h1 | 16 | 7590 | 77 | 7.7 | 16 | 16 | 572.2 | ok ×79, status:503-inspection-overloaded ×7511 |
| s11-arg-amplification-h1 | waf | before | ia-a1-after-soak | 3 | c1 | load | h1 | 1 | 37 | 36 | 3.6 | 1 | 1 | 316.3 | ok ×37 |
| s11-arg-amplification-h1 | waf | before | ia-a1-after-soak | 3 | c2 | load | h1 | 2 | 81 | 79 | 7.9 | 2 | 2 | 307.7 | ok ×81 |
| s11-arg-amplification-h1 | waf | before | ia-a1-after-soak | 3 | c4 | load | h1 | 4 | 719 | 77 | 7.7 | 4 | 4 | 533.3 | ok ×79, status:503-inspection-overloaded ×640 |
| s11-arg-amplification-h1 | waf | before | ia-a1-after-soak | 3 | c8 | load | h1 | 8 | 2586 | 75 | 7.5 | 8 | 8 | 591.1 | ok ×77, status:503-inspection-overloaded ×2509 |
| s11-arg-amplification-h1 | waf | before | ia-a1-after-soak | 3 | c16 | load | h1 | 16 | 7164 | 54 | 5.4 | 16 | 16 | 790.1 | ok ×57, status:503-inspection-overloaded ×7107 |
| s11-arg-amplification-h1 | waf | main | ia-a1-after-soak | 1 | c1 | load | h1 | 1 | 38 | 37 | 3.7 | 1 | 1 | 308.4 | ok ×38 |
| s11-arg-amplification-h1 | waf | main | ia-a1-after-soak | 1 | c2 | load | h1 | 2 | 79 | 77 | 7.7 | 2 | 2 | 326.0 | ok ×79 |
| s11-arg-amplification-h1 | waf | main | ia-a1-after-soak | 1 | c4 | load | h1 | 4 | 685 | 77 | 7.7 | 4 | 4 | 543.6 | ok ×80, status:503-inspection-overloaded ×605 |
| s11-arg-amplification-h1 | waf | main | ia-a1-after-soak | 1 | c8 | load | h1 | 8 | 2562 | 69 | 6.9 | 8 | 8 | 616.8 | ok ×72, status:503-inspection-overloaded ×2490 |
| s11-arg-amplification-h1 | waf | main | ia-a1-after-soak | 1 | c16 | load | h1 | 16 | 6977 | 59 | 5.9 | 16 | 16 | 767.8 | ok ×62, status:503-inspection-overloaded ×6915 |
| s11-arg-amplification-h1 | waf | main | ia-a1-after-soak | 2 | c1 | load | h1 | 1 | 39 | 38 | 3.8 | 1 | 1 | 290.6 | ok ×39 |
| s11-arg-amplification-h1 | waf | main | ia-a1-after-soak | 2 | c2 | load | h1 | 2 | 66 | 64 | 6.4 | 2 | 2 | 397.1 | ok ×66 |
| s11-arg-amplification-h1 | waf | main | ia-a1-after-soak | 2 | c4 | load | h1 | 4 | 682 | 62 | 6.2 | 4 | 4 | 645.5 | ok ×65, status:503-inspection-overloaded ×617 |
| s11-arg-amplification-h1 | waf | main | ia-a1-after-soak | 2 | c8 | load | h1 | 8 | 2505 | 61 | 6.1 | 8 | 8 | 641.1 | ok ×64, status:503-inspection-overloaded ×2441 |
| s11-arg-amplification-h1 | waf | main | ia-a1-after-soak | 2 | c16 | load | h1 | 16 | 6089 | 47 | 4.7 | 16 | 16 | 885.3 | ok ×51, status:503-inspection-overloaded ×6038 |
| s11-arg-amplification-h1 | waf | main | ia-a1-after-soak | 3 | c1 | load | h1 | 1 | 30 | 29 | 2.9 | 1 | 1 | 392.8 | ok ×30 |
| s11-arg-amplification-h1 | waf | main | ia-a1-after-soak | 3 | c2 | load | h1 | 2 | 70 | 68 | 6.8 | 2 | 2 | 401.6 | ok ×70 |
| s11-arg-amplification-h1 | waf | main | ia-a1-after-soak | 3 | c4 | load | h1 | 4 | 753 | 72 | 7.2 | 4 | 4 | 531.4 | ok ×74, status:503-inspection-overloaded ×679 |
| s11-arg-amplification-h1 | waf | main | ia-a1-after-soak | 3 | c8 | load | h1 | 8 | 3239 | 72 | 7.2 | 8 | 8 | 477.5 | ok ×75, status:503-inspection-overloaded ×3164 |
| s11-arg-amplification-h1 | waf | main | ia-a1-after-soak | 3 | c16 | load | h1 | 16 | 6852 | 66 | 6.6 | 16 | 16 | 642.1 | ok ×68, status:503-inspection-overloaded ×6784 |

## Non-success outcome latency

Completed attempts started in the window, per outcome (for example `status:503` for overload), including those that finished after it.

| Scenario | Variant | Target | Run | Rep | Phase | Class | Outcome | n | p50 ms | p95 ms | p99 ms | max ms |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: |
| s11-arg-amplification-h1 | waf | before | ia-a1-after-soak | 1 | c4 | load | status:503-inspection-overloaded | 576 | 20.3 | 46.0 | 55.9 | 60.0 |
| s11-arg-amplification-h1 | waf | before | ia-a1-after-soak | 1 | c8 | load | status:503-inspection-overloaded | 2650 | 19.8 | 45.6 | 57.0 | 62.2 |
| s11-arg-amplification-h1 | waf | before | ia-a1-after-soak | 1 | c16 | load | status:503-inspection-overloaded | 6138 | 20.5 | 45.2 | 61.0 | 66.0 |
| s11-arg-amplification-h1 | waf | before | ia-a1-after-soak | 2 | c4 | load | status:503-inspection-overloaded | 538 | 20.2 | 40.8 | 55.2 | 140.8 |
| s11-arg-amplification-h1 | waf | before | ia-a1-after-soak | 2 | c8 | load | status:503-inspection-overloaded | 3669 | 10.0 | 40.6 | 45.2 | 70.6 |
| s11-arg-amplification-h1 | waf | before | ia-a1-after-soak | 2 | c16 | load | status:503-inspection-overloaded | 7511 | 10.6 | 50.9 | 60.8 | 85.9 |
| s11-arg-amplification-h1 | waf | before | ia-a1-after-soak | 3 | c4 | load | status:503-inspection-overloaded | 640 | 20.1 | 40.4 | 44.5 | 60.9 |
| s11-arg-amplification-h1 | waf | before | ia-a1-after-soak | 3 | c8 | load | status:503-inspection-overloaded | 2509 | 20.1 | 41.9 | 53.0 | 67.8 |
| s11-arg-amplification-h1 | waf | before | ia-a1-after-soak | 3 | c16 | load | status:503-inspection-overloaded | 7107 | 15.3 | 45.3 | 61.7 | 83.6 |
| s11-arg-amplification-h1 | waf | main | ia-a1-after-soak | 1 | c4 | load | status:503-inspection-overloaded | 605 | 20.2 | 40.6 | 44.2 | 68.0 |
| s11-arg-amplification-h1 | waf | main | ia-a1-after-soak | 1 | c8 | load | status:503-inspection-overloaded | 2490 | 20.1 | 41.3 | 50.2 | 78.8 |
| s11-arg-amplification-h1 | waf | main | ia-a1-after-soak | 1 | c16 | load | status:503-inspection-overloaded | 6915 | 14.7 | 55.6 | 74.0 | 96.2 |
| s11-arg-amplification-h1 | waf | main | ia-a1-after-soak | 2 | c4 | load | status:503-inspection-overloaded | 617 | 20.1 | 40.5 | 45.7 | 60.9 |
| s11-arg-amplification-h1 | waf | main | ia-a1-after-soak | 2 | c8 | load | status:503-inspection-overloaded | 2441 | 20.3 | 45.6 | 59.5 | 65.7 |
| s11-arg-amplification-h1 | waf | main | ia-a1-after-soak | 2 | c16 | load | status:503-inspection-overloaded | 6038 | 20.4 | 48.3 | 60.6 | 88.5 |
| s11-arg-amplification-h1 | waf | main | ia-a1-after-soak | 3 | c4 | load | status:503-inspection-overloaded | 679 | 20.2 | 40.7 | 48.6 | 59.1 |
| s11-arg-amplification-h1 | waf | main | ia-a1-after-soak | 3 | c8 | load | status:503-inspection-overloaded | 3164 | 13.6 | 41.3 | 50.8 | 66.9 |
| s11-arg-amplification-h1 | waf | main | ia-a1-after-soak | 3 | c16 | load | status:503-inspection-overloaded | 6784 | 15.3 | 55.4 | 61.4 | 90.6 |

## Failed, invalid or inconclusive runs

- ia-a1-after-soak s11-arg-amplification-h1/waf/before rep1: runtime-finding: phase c4 class load: status:503-inspection-overloaded ×576; phase c8 class load: status:503-inspection-overloaded ×2650; phase c16 class load: status:503-inspection-overloaded ×6138
- ia-a1-after-soak s11-arg-amplification-h1/waf/before rep2: runtime-finding: phase c4 class load: status:503-inspection-overloaded ×538; phase c8 class load: status:503-inspection-overloaded ×3669; phase c16 class load: status:503-inspection-overloaded ×7511
- ia-a1-after-soak s11-arg-amplification-h1/waf/before rep3: runtime-finding: phase c4 class load: status:503-inspection-overloaded ×640; phase c8 class load: status:503-inspection-overloaded ×2509; phase c16 class load: status:503-inspection-overloaded ×7107
- ia-a1-after-soak s11-arg-amplification-h1/waf/main rep1: runtime-finding: phase c4 class load: status:503-inspection-overloaded ×605; phase c8 class load: status:503-inspection-overloaded ×2490; phase c16 class load: status:503-inspection-overloaded ×6915
- ia-a1-after-soak s11-arg-amplification-h1/waf/main rep2: runtime-finding: phase c4 class load: status:503-inspection-overloaded ×617; phase c8 class load: status:503-inspection-overloaded ×2441; phase c16 class load: status:503-inspection-overloaded ×6038
- ia-a1-after-soak s11-arg-amplification-h1/waf/main rep3: runtime-finding: phase c4 class load: status:503-inspection-overloaded ×679; phase c8 class load: status:503-inspection-overloaded ×3164; phase c16 class load: status:503-inspection-overloaded ×6784
