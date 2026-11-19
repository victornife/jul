# Inspection assessment summary

Plan `inspection-assessment-plan/v3`, profile `qualification`. Runs: ia-a2-s3-large-h2.

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
| s3-large-h2 | both | base | capacity | 3 | 3 | 0 | 0.0 | 0.0 | 0.0 | n/a | 1.00 n/a n/a | 768.0 768.0 709.4 |
| s3-large-h2 | both | main | capacity | 3 | 3 | 0 | 0.2 | 0.1 | 0.3 | 0.600 | 1.00 0.99 1.00 | 539.2 525.3 518.7 |
| s3-large-h2 | none | base | capacity | 3 | 3 | 3 | 318.4 | 276.4 | 324.6 | 0.151 | 0.66 0.65 0.64 | 45.4 45.2 42.9 |
| s3-large-h2 | none | main | capacity | 3 | 3 | 3 | 232.3 | 98.6 | 250.8 | 0.655 | 0.71 0.65 0.66 | 40.8 43.5 42.7 |

## Phases and classes

One row per repetition, phase and workload class. Goodput counts verified responses that started and finished in the window; requests crossing in from warm-up and those outstanding at the window end are excluded from it.

| Scenario | Variant | Target | Run | Rep | Phase | Class | Proto | C | Attempts | Verified in window | Goodput rps | Crossed in | Outstanding | p99 ms | Outcomes |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| s3-large-h2 | both | base | ia-a2-s3-large-h2 | 1 | load | load | h2 | 32 | 6 | 0 | 0.0 | 32 | 1 | n/a | error:eof ×1, status:500 ×5 |
| s3-large-h2 | both | base | ia-a2-s3-large-h2 | 2 | load | load | h2 | 32 | 463812 | 0 | 0.0 | 32 | 32 | n/a | error:refused ×463809, error:reset ×3 |
| s3-large-h2 | both | base | ia-a2-s3-large-h2 | 3 | load | load | h2 | 32 | 357841 | 0 | 0.0 | 32 | 31 | n/a | error:refused ×357838, error:reset ×3 |
| s3-large-h2 | both | main | ia-a2-s3-large-h2 | 1 | load | load | h2 | 32 | 399 | 3 | 0.1 | 32 | 32 | 3846.4 | ok ×6, status:503-inspection-overloaded ×393 |
| s3-large-h2 | both | main | ia-a2-s3-large-h2 | 2 | load | load | h2 | 32 | 383 | 5 | 0.2 | 32 | 32 | 4591.0 | ok ×9, status:503-inspection-overloaded ×374 |
| s3-large-h2 | both | main | ia-a2-s3-large-h2 | 3 | load | load | h2 | 32 | 373 | 6 | 0.3 | 32 | 32 | 3959.9 | ok ×9, status:503-inspection-overloaded ×364 |
| s3-large-h2 | none | base | ia-a2-s3-large-h2 | 1 | load | load | h2 | 32 | 5559 | 5527 | 276.4 | 32 | 32 | 188.6 | ok ×5559 |
| s3-large-h2 | none | base | ia-a2-s3-large-h2 | 2 | load | load | h2 | 32 | 6523 | 6491 | 324.6 | 32 | 32 | 148.2 | ok ×6523 |
| s3-large-h2 | none | base | ia-a2-s3-large-h2 | 3 | load | load | h2 | 32 | 6399 | 6367 | 318.4 | 32 | 32 | 132.7 | ok ×6399 |
| s3-large-h2 | none | main | ia-a2-s3-large-h2 | 1 | load | load | h2 | 32 | 2003 | 1972 | 98.6 | 32 | 31 | 571.8 | ok ×2003 |
| s3-large-h2 | none | main | ia-a2-s3-large-h2 | 2 | load | load | h2 | 32 | 5048 | 5016 | 250.8 | 32 | 32 | 162.6 | ok ×5048 |
| s3-large-h2 | none | main | ia-a2-s3-large-h2 | 3 | load | load | h2 | 32 | 4679 | 4647 | 232.3 | 31 | 32 | 173.0 | ok ×4679 |

## Non-success outcome latency

Completed attempts started in the window, per outcome (for example `status:503` for overload), including those that finished after it.

| Scenario | Variant | Target | Run | Rep | Phase | Class | Outcome | n | p50 ms | p95 ms | p99 ms | max ms |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: |
| s3-large-h2 | both | base | ia-a2-s3-large-h2 | 1 | load | load | error:eof | 1 | 26198.7 | 26198.7 | 26198.7 | 26198.7 |
| s3-large-h2 | both | base | ia-a2-s3-large-h2 | 1 | load | load | status:500 | 5 | 1065.6 | 1425.4 | 1425.4 | 1509.9 |
| s3-large-h2 | both | base | ia-a2-s3-large-h2 | 2 | load | load | error:refused | 463809 | 0.2 | 0.9 | 1.6 | 27.5 |
| s3-large-h2 | both | base | ia-a2-s3-large-h2 | 2 | load | load | error:reset | 3 | 619.8 | 619.8 | 619.8 | 620.0 |
| s3-large-h2 | both | base | ia-a2-s3-large-h2 | 3 | load | load | error:refused | 357838 | 0.2 | 0.9 | 1.6 | 19.8 |
| s3-large-h2 | both | base | ia-a2-s3-large-h2 | 3 | load | load | error:reset | 3 | 182.6 | 182.6 | 182.6 | 465.4 |
| s3-large-h2 | both | main | ia-a2-s3-large-h2 | 1 | load | load | status:503-inspection-overloaded | 393 | 1312.8 | 3173.3 | 4489.1 | 5149.9 |
| s3-large-h2 | both | main | ia-a2-s3-large-h2 | 2 | load | load | status:503-inspection-overloaded | 374 | 1315.1 | 2687.7 | 3851.9 | 3890.5 |
| s3-large-h2 | both | main | ia-a2-s3-large-h2 | 3 | load | load | status:503-inspection-overloaded | 364 | 1307.6 | 2628.3 | 3914.3 | 5162.1 |

## Failed, invalid or inconclusive runs

- ia-a2-s3-large-h2 s3-large-h2/both/base rep1: runtime-finding: jul exited during the run: signal killed; phase load class load: error:eof ×1; phase load class load: status:500 ×5; oom_kill delta 1 in /sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/app.slice (hierarchical memory.events oom_kill; includes sibling scopes)
- ia-a2-s3-large-h2 s3-large-h2/both/base rep2: runtime-finding: jul exited during the run: signal killed; phase load class load: error:refused ×463809; phase load class load: error:reset ×3; oom_kill delta 1 in /sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/app.slice (hierarchical memory.events oom_kill; includes sibling scopes)
- ia-a2-s3-large-h2 s3-large-h2/both/base rep3: runtime-finding: jul exited during the run: signal killed; phase load class load: error:refused ×357838; phase load class load: error:reset ×3; oom_kill delta 1 in /sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/app.slice (hierarchical memory.events oom_kill; includes sibling scopes)
- ia-a2-s3-large-h2 s3-large-h2/both/main rep1: runtime-finding: phase load class load: status:503-inspection-overloaded ×393
- ia-a2-s3-large-h2 s3-large-h2/both/main rep2: runtime-finding: phase load class load: status:503-inspection-overloaded ×374
- ia-a2-s3-large-h2 s3-large-h2/both/main rep3: runtime-finding: phase load class load: status:503-inspection-overloaded ×364
