# Inspection assessment summary

Plan `inspection-assessment-plan/v3`, profile `qualification`. Runs: ia-a1-s11.

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
| s11-arg-amplification-h1 | none | base | record | 3 | 3 | 3 | 1747.0 | 1628.0 | 1747.7 | 0.069 | 0.37 0.36 0.37 | 23.9 23.4 23.9 |
| s11-arg-amplification-h1 | none | main | record | 3 | 3 | 3 | 1056.0 | 1044.3 | 1296.4 | 0.239 | 0.40 0.38 0.39 | 23.6 24.2 24.0 |
| s11-arg-amplification-h1 | waf | base | record | 3 | 3 | 0 | 2.9 | 2.5 | 3.0 | 0.172 | 0.52 0.52 0.52 | 754.4 759.9 753.9 |
| s11-arg-amplification-h1 | waf | main | record | 3 | 3 | 0 | 2.7 | 2.0 | 2.9 | 0.333 | 0.53 0.53 0.52 | 627.1 666.5 529.9 |

## Phases and classes

One row per repetition, phase and workload class. Goodput counts verified responses that started and finished in the window; requests crossing in from warm-up and those outstanding at the window end are excluded from it.

| Scenario | Variant | Target | Run | Rep | Phase | Class | Proto | C | Attempts | Verified in window | Goodput rps | Crossed in | Outstanding | p99 ms | Outcomes |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| s11-arg-amplification-h1 | none | base | ia-a1-s11 | 1 | c1 | load | h1 | 1 | 17478 | 17477 | 1747.7 | 1 | 1 | 1.3 | ok ×17478 |
| s11-arg-amplification-h1 | none | base | ia-a1-s11 | 1 | c2 | load | h1 | 2 | 35712 | 35710 | 3571.0 | 2 | 2 | 1.8 | ok ×35712 |
| s11-arg-amplification-h1 | none | base | ia-a1-s11 | 1 | c4 | load | h1 | 4 | 53733 | 53729 | 5372.9 | 4 | 4 | 3.4 | ok ×53733 |
| s11-arg-amplification-h1 | none | base | ia-a1-s11 | 1 | c8 | load | h1 | 8 | 54798 | 54790 | 5479.0 | 8 | 8 | 7.3 | ok ×54798 |
| s11-arg-amplification-h1 | none | base | ia-a1-s11 | 1 | c16 | load | h1 | 16 | 51223 | 51207 | 5120.7 | 16 | 16 | 12.4 | ok ×51223 |
| s11-arg-amplification-h1 | none | base | ia-a1-s11 | 2 | c1 | load | h1 | 1 | 17471 | 17470 | 1747.0 | 1 | 1 | 1.4 | ok ×17471 |
| s11-arg-amplification-h1 | none | base | ia-a1-s11 | 2 | c2 | load | h1 | 2 | 36125 | 36123 | 3612.3 | 2 | 2 | 1.5 | ok ×36125 |
| s11-arg-amplification-h1 | none | base | ia-a1-s11 | 2 | c4 | load | h1 | 4 | 57001 | 56997 | 5699.7 | 4 | 4 | 3.0 | ok ×57001 |
| s11-arg-amplification-h1 | none | base | ia-a1-s11 | 2 | c8 | load | h1 | 8 | 58797 | 58789 | 5878.9 | 8 | 8 | 7.4 | ok ×58797 |
| s11-arg-amplification-h1 | none | base | ia-a1-s11 | 2 | c16 | load | h1 | 16 | 59804 | 59788 | 5978.8 | 16 | 16 | 11.2 | ok ×59804 |
| s11-arg-amplification-h1 | none | base | ia-a1-s11 | 3 | c1 | load | h1 | 1 | 16281 | 16280 | 1628.0 | 1 | 1 | 1.2 | ok ×16281 |
| s11-arg-amplification-h1 | none | base | ia-a1-s11 | 3 | c2 | load | h1 | 2 | 30931 | 30929 | 3092.9 | 2 | 2 | 1.9 | ok ×30931 |
| s11-arg-amplification-h1 | none | base | ia-a1-s11 | 3 | c4 | load | h1 | 4 | 46169 | 46165 | 4616.5 | 4 | 4 | 4.3 | ok ×46169 |
| s11-arg-amplification-h1 | none | base | ia-a1-s11 | 3 | c8 | load | h1 | 8 | 52465 | 52457 | 5245.7 | 8 | 8 | 8.3 | ok ×52465 |
| s11-arg-amplification-h1 | none | base | ia-a1-s11 | 3 | c16 | load | h1 | 16 | 53084 | 53068 | 5306.8 | 16 | 16 | 13.5 | ok ×53084 |
| s11-arg-amplification-h1 | none | main | ia-a1-s11 | 1 | c1 | load | h1 | 1 | 10561 | 10560 | 1056.0 | 1 | 1 | 2.6 | ok ×10561 |
| s11-arg-amplification-h1 | none | main | ia-a1-s11 | 1 | c2 | load | h1 | 2 | 23733 | 23731 | 2373.1 | 2 | 2 | 3.3 | ok ×23733 |
| s11-arg-amplification-h1 | none | main | ia-a1-s11 | 1 | c4 | load | h1 | 4 | 48447 | 48443 | 4844.3 | 4 | 4 | 4.0 | ok ×48447 |
| s11-arg-amplification-h1 | none | main | ia-a1-s11 | 1 | c8 | load | h1 | 8 | 64231 | 64223 | 6422.3 | 8 | 8 | 6.7 | ok ×64231 |
| s11-arg-amplification-h1 | none | main | ia-a1-s11 | 1 | c16 | load | h1 | 16 | 58655 | 58639 | 5863.9 | 16 | 16 | 13.3 | ok ×58655 |
| s11-arg-amplification-h1 | none | main | ia-a1-s11 | 2 | c1 | load | h1 | 1 | 12965 | 12964 | 1296.4 | 1 | 1 | 2.1 | ok ×12965 |
| s11-arg-amplification-h1 | none | main | ia-a1-s11 | 2 | c2 | load | h1 | 2 | 33119 | 33117 | 3311.7 | 2 | 2 | 1.9 | ok ×33119 |
| s11-arg-amplification-h1 | none | main | ia-a1-s11 | 2 | c4 | load | h1 | 4 | 62856 | 62852 | 6285.2 | 4 | 4 | 2.5 | ok ×62856 |
| s11-arg-amplification-h1 | none | main | ia-a1-s11 | 2 | c8 | load | h1 | 8 | 77890 | 77882 | 7788.2 | 8 | 8 | 5.8 | ok ×77890 |
| s11-arg-amplification-h1 | none | main | ia-a1-s11 | 2 | c16 | load | h1 | 16 | 67584 | 67568 | 6756.8 | 16 | 16 | 10.9 | ok ×67584 |
| s11-arg-amplification-h1 | none | main | ia-a1-s11 | 3 | c1 | load | h1 | 1 | 10444 | 10443 | 1044.3 | 1 | 1 | 2.6 | ok ×10444 |
| s11-arg-amplification-h1 | none | main | ia-a1-s11 | 3 | c2 | load | h1 | 2 | 25375 | 25374 | 2537.4 | 2 | 1 | 3.0 | ok ×25375 |
| s11-arg-amplification-h1 | none | main | ia-a1-s11 | 3 | c4 | load | h1 | 4 | 52758 | 52754 | 5275.4 | 4 | 4 | 3.6 | ok ×52758 |
| s11-arg-amplification-h1 | none | main | ia-a1-s11 | 3 | c8 | load | h1 | 8 | 61856 | 61849 | 6184.9 | 8 | 7 | 8.0 | ok ×61856 |
| s11-arg-amplification-h1 | none | main | ia-a1-s11 | 3 | c16 | load | h1 | 16 | 60289 | 60273 | 6027.3 | 16 | 16 | 13.0 | ok ×60289 |
| s11-arg-amplification-h1 | waf | base | ia-a1-s11 | 1 | c1 | load | h1 | 1 | 31 | 30 | 3.0 | 1 | 1 | 388.5 | ok ×31 |
| s11-arg-amplification-h1 | waf | base | ia-a1-s11 | 1 | c2 | load | h1 | 2 | 73 | 71 | 7.1 | 2 | 2 | 342.9 | ok ×73 |
| s11-arg-amplification-h1 | waf | base | ia-a1-s11 | 1 | c4 | load | h1 | 4 | 73 | 69 | 6.9 | 4 | 4 | 755.6 | ok ×73 |
| s11-arg-amplification-h1 | waf | base | ia-a1-s11 | 1 | c8 | load | h1 | 8 | 397407 | 15 | 1.5 | 8 | 8 | 1355.2 | error:eof ×6, error:refused ×397384, error:reset ×2, ok ×15 |
| s11-arg-amplification-h1 | waf | base | ia-a1-s11 | 1 | c16 | load | h1 | 16 | 565605 | 0 | 0.0 | 13 | 14 | n/a | error:refused ×565605 |
| s11-arg-amplification-h1 | waf | base | ia-a1-s11 | 2 | c1 | load | h1 | 1 | 26 | 25 | 2.5 | 1 | 1 | 469.2 | ok ×26 |
| s11-arg-amplification-h1 | waf | base | ia-a1-s11 | 2 | c2 | load | h1 | 2 | 54 | 52 | 5.2 | 2 | 2 | 483.7 | ok ×54 |
| s11-arg-amplification-h1 | waf | base | ia-a1-s11 | 2 | c4 | load | h1 | 4 | 64 | 60 | 6.0 | 4 | 4 | 765.9 | ok ×64 |
| s11-arg-amplification-h1 | waf | base | ia-a1-s11 | 2 | c8 | load | h1 | 8 | 411246 | 22 | 2.2 | 8 | 8 | 1186.9 | error:eof ×8, error:refused ×411216, ok ×22 |
| s11-arg-amplification-h1 | waf | base | ia-a1-s11 | 2 | c16 | load | h1 | 16 | 797257 | 0 | 0.0 | 16 | 16 | n/a | error:refused ×797257 |
| s11-arg-amplification-h1 | waf | base | ia-a1-s11 | 3 | c1 | load | h1 | 1 | 30 | 29 | 2.9 | 1 | 1 | 373.2 | ok ×30 |
| s11-arg-amplification-h1 | waf | base | ia-a1-s11 | 3 | c2 | load | h1 | 2 | 71 | 69 | 6.9 | 2 | 2 | 363.8 | ok ×71 |
| s11-arg-amplification-h1 | waf | base | ia-a1-s11 | 3 | c4 | load | h1 | 4 | 85 | 81 | 8.1 | 4 | 4 | 556.9 | ok ×85 |
| s11-arg-amplification-h1 | waf | base | ia-a1-s11 | 3 | c8 | load | h1 | 8 | 632812 | 12 | 1.2 | 8 | 7 | 957.1 | error:eof ×7, error:refused ×632792, error:reset ×1, ok ×12 |
| s11-arg-amplification-h1 | waf | base | ia-a1-s11 | 3 | c16 | load | h1 | 16 | 839495 | 0 | 0.0 | 16 | 15 | n/a | error:refused ×839495 |
| s11-arg-amplification-h1 | waf | main | ia-a1-s11 | 1 | c1 | load | h1 | 1 | 21 | 20 | 2.0 | 1 | 1 | 537.4 | ok ×21 |
| s11-arg-amplification-h1 | waf | main | ia-a1-s11 | 1 | c2 | load | h1 | 2 | 53 | 51 | 5.1 | 2 | 2 | 451.9 | ok ×53 |
| s11-arg-amplification-h1 | waf | main | ia-a1-s11 | 1 | c4 | load | h1 | 4 | 628 | 67 | 6.7 | 4 | 4 | 636.2 | ok ×70, status:503-inspection-overloaded ×558 |
| s11-arg-amplification-h1 | waf | main | ia-a1-s11 | 1 | c8 | load | h1 | 8 | 2987 | 78 | 7.8 | 8 | 8 | 512.1 | ok ×81, status:503-inspection-overloaded ×2906 |
| s11-arg-amplification-h1 | waf | main | ia-a1-s11 | 1 | c16 | load | h1 | 16 | 7669 | 76 | 7.6 | 16 | 16 | 542.7 | ok ×79, status:503-inspection-overloaded ×7590 |
| s11-arg-amplification-h1 | waf | main | ia-a1-s11 | 2 | c1 | load | h1 | 1 | 28 | 27 | 2.7 | 1 | 1 | 402.8 | ok ×28 |
| s11-arg-amplification-h1 | waf | main | ia-a1-s11 | 2 | c2 | load | h1 | 2 | 62 | 60 | 6.0 | 2 | 2 | 388.9 | ok ×62 |
| s11-arg-amplification-h1 | waf | main | ia-a1-s11 | 2 | c4 | load | h1 | 4 | 627 | 58 | 5.8 | 4 | 4 | 821.1 | ok ×62, status:503-inspection-overloaded ×565 |
| s11-arg-amplification-h1 | waf | main | ia-a1-s11 | 2 | c8 | load | h1 | 8 | 2862 | 66 | 6.6 | 8 | 8 | 606.6 | ok ×69, status:503-inspection-overloaded ×2793 |
| s11-arg-amplification-h1 | waf | main | ia-a1-s11 | 2 | c16 | load | h1 | 16 | 7267 | 67 | 6.7 | 16 | 16 | 630.9 | ok ×70, status:503-inspection-overloaded ×7197 |
| s11-arg-amplification-h1 | waf | main | ia-a1-s11 | 3 | c1 | load | h1 | 1 | 30 | 29 | 2.9 | 1 | 1 | 368.7 | ok ×30 |
| s11-arg-amplification-h1 | waf | main | ia-a1-s11 | 3 | c2 | load | h1 | 2 | 58 | 56 | 5.6 | 2 | 2 | 538.0 | ok ×58 |
| s11-arg-amplification-h1 | waf | main | ia-a1-s11 | 3 | c4 | load | h1 | 4 | 684 | 53 | 5.3 | 4 | 4 | 800.6 | ok ×56, status:503-inspection-overloaded ×628 |
| s11-arg-amplification-h1 | waf | main | ia-a1-s11 | 3 | c8 | load | h1 | 8 | 2528 | 52 | 5.2 | 8 | 8 | 810.8 | ok ×54, status:503-inspection-overloaded ×2474 |
| s11-arg-amplification-h1 | waf | main | ia-a1-s11 | 3 | c16 | load | h1 | 16 | 5902 | 54 | 5.4 | 16 | 16 | 797.2 | ok ×57, status:503-inspection-overloaded ×5845 |

## Non-success outcome latency

Completed attempts started in the window, per outcome (for example `status:503` for overload), including those that finished after it.

| Scenario | Variant | Target | Run | Rep | Phase | Class | Outcome | n | p50 ms | p95 ms | p99 ms | max ms |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: |
| s11-arg-amplification-h1 | waf | base | ia-a1-s11 | 1 | c8 | load | error:eof | 6 | 377.9 | 499.1 | 499.1 | 1070.6 |
| s11-arg-amplification-h1 | waf | base | ia-a1-s11 | 1 | c8 | load | error:refused | 397384 | 0.1 | 0.3 | 0.8 | 26.2 |
| s11-arg-amplification-h1 | waf | base | ia-a1-s11 | 1 | c8 | load | error:reset | 2 | 129.9 | 129.9 | 129.9 | 171.7 |
| s11-arg-amplification-h1 | waf | base | ia-a1-s11 | 1 | c16 | load | error:refused | 565605 | 0.2 | 0.7 | 1.5 | 28.4 |
| s11-arg-amplification-h1 | waf | base | ia-a1-s11 | 2 | c8 | load | error:eof | 8 | 722.2 | 1141.9 | 1141.9 | 1303.2 |
| s11-arg-amplification-h1 | waf | base | ia-a1-s11 | 2 | c8 | load | error:refused | 411216 | 0.1 | 0.2 | 0.6 | 30.5 |
| s11-arg-amplification-h1 | waf | base | ia-a1-s11 | 2 | c16 | load | error:refused | 797257 | 0.1 | 0.5 | 0.9 | 59.3 |
| s11-arg-amplification-h1 | waf | base | ia-a1-s11 | 3 | c8 | load | error:eof | 7 | 324.7 | 416.1 | 416.1 | 1051.0 |
| s11-arg-amplification-h1 | waf | base | ia-a1-s11 | 3 | c8 | load | error:refused | 632792 | 0.0 | 0.2 | 0.5 | 60.0 |
| s11-arg-amplification-h1 | waf | base | ia-a1-s11 | 3 | c8 | load | error:reset | 1 | 157.1 | 157.1 | 157.1 | 157.1 |
| s11-arg-amplification-h1 | waf | base | ia-a1-s11 | 3 | c16 | load | error:refused | 839495 | 0.1 | 0.4 | 0.8 | 23.2 |
| s11-arg-amplification-h1 | waf | main | ia-a1-s11 | 1 | c4 | load | status:503-inspection-overloaded | 558 | 20.3 | 47.1 | 55.8 | 58.7 |
| s11-arg-amplification-h1 | waf | main | ia-a1-s11 | 1 | c8 | load | status:503-inspection-overloaded | 2906 | 16.4 | 45.1 | 56.8 | 70.0 |
| s11-arg-amplification-h1 | waf | main | ia-a1-s11 | 1 | c16 | load | status:503-inspection-overloaded | 7590 | 11.9 | 45.4 | 60.8 | 81.2 |
| s11-arg-amplification-h1 | waf | main | ia-a1-s11 | 2 | c4 | load | status:503-inspection-overloaded | 565 | 20.2 | 40.6 | 55.6 | 74.5 |
| s11-arg-amplification-h1 | waf | main | ia-a1-s11 | 2 | c8 | load | status:503-inspection-overloaded | 2793 | 16.3 | 45.9 | 58.0 | 78.3 |
| s11-arg-amplification-h1 | waf | main | ia-a1-s11 | 2 | c16 | load | status:503-inspection-overloaded | 7197 | 12.7 | 53.2 | 60.3 | 75.8 |
| s11-arg-amplification-h1 | waf | main | ia-a1-s11 | 3 | c4 | load | status:503-inspection-overloaded | 628 | 20.2 | 40.9 | 49.9 | 60.0 |
| s11-arg-amplification-h1 | waf | main | ia-a1-s11 | 3 | c8 | load | status:503-inspection-overloaded | 2474 | 20.1 | 50.0 | 60.4 | 78.8 |
| s11-arg-amplification-h1 | waf | main | ia-a1-s11 | 3 | c16 | load | status:503-inspection-overloaded | 5845 | 20.8 | 45.0 | 60.6 | 65.1 |

## Failed, invalid or inconclusive runs

- ia-a1-s11 s11-arg-amplification-h1/waf/base rep1: runtime-finding: jul exited during the run: signal killed; phase c8 class load: error:eof ×6; phase c8 class load: error:refused ×397384; phase c8 class load: error:reset ×2; phase c16 class load: error:refused ×565605; oom_kill delta 1 in /sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/app.slice (hierarchical memory.events oom_kill; includes sibling scopes)
- ia-a1-s11 s11-arg-amplification-h1/waf/base rep2: runtime-finding: jul exited during the run: signal killed; phase c8 class load: error:eof ×8; phase c8 class load: error:refused ×411216; phase c16 class load: error:refused ×797257; oom_kill delta 1 in /sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/app.slice (hierarchical memory.events oom_kill; includes sibling scopes)
- ia-a1-s11 s11-arg-amplification-h1/waf/base rep3: runtime-finding: jul exited during the run: signal killed; phase c8 class load: error:eof ×7; phase c8 class load: error:refused ×632792; phase c8 class load: error:reset ×1; phase c16 class load: error:refused ×839495; oom_kill delta 1 in /sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/app.slice (hierarchical memory.events oom_kill; includes sibling scopes)
- ia-a1-s11 s11-arg-amplification-h1/waf/main rep1: runtime-finding: phase c4 class load: status:503-inspection-overloaded ×558; phase c8 class load: status:503-inspection-overloaded ×2906; phase c16 class load: status:503-inspection-overloaded ×7590
- ia-a1-s11 s11-arg-amplification-h1/waf/main rep2: runtime-finding: phase c4 class load: status:503-inspection-overloaded ×565; phase c8 class load: status:503-inspection-overloaded ×2793; phase c16 class load: status:503-inspection-overloaded ×7197
- ia-a1-s11 s11-arg-amplification-h1/waf/main rep3: runtime-finding: phase c4 class load: status:503-inspection-overloaded ×628; phase c8 class load: status:503-inspection-overloaded ×2474; phase c16 class load: status:503-inspection-overloaded ×5845
