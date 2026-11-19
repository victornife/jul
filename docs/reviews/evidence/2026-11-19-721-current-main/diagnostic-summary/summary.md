# Inspection assessment summary

Plan `inspection-assessment-plan/v4`, profile `qualification`. Runs: bounded-diagnostic.

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
| s11-arg-amplification-h1-320m | none | candidate | record | 3 | 3 | 3 | 873.5 | 868.4 | 1136.4 | 0.307 | 0.39 0.41 0.41 | 23.5 24.0 24.0 |
| s11-arg-amplification-h1-320m | none | main | record | 3 | 3 | 3 | 1542.5 | 1202.7 | 1703.0 | 0.324 | 0.38 0.38 0.36 | 24.0 23.5 24.4 |
| s11-arg-amplification-h1-320m | waf | candidate | record | 3 | 3 | 3 | 3.6 | 3.2 | 3.9 | 0.194 | 0.52 0.52 0.52 | 473.0 467.6 472.6 |
| s11-arg-amplification-h1-320m | waf | main | record | 3 | 3 | 3 | 3.3 | 2.8 | 3.5 | 0.212 | 0.53 0.52 0.52 | 475.8 473.7 476.1 |
| s3-large-h2-bounded | both | candidate | capacity | 3 | 0 | 3 | 0.0 | 0.0 | 0.0 | n/a | n/a n/a n/a | unavailable unavailable unavailable |
| s3-large-h2-bounded | both | main | capacity | 3 | 0 | 3 | 0.0 | 0.0 | 0.0 | n/a | n/a n/a n/a | unavailable unavailable unavailable |
| s3-large-h2-bounded | none | candidate | capacity | 3 | 0 | 3 | 0.0 | 0.0 | 0.0 | n/a | n/a n/a n/a | unavailable unavailable unavailable |
| s3-large-h2-bounded | none | main | capacity | 3 | 0 | 3 | 0.0 | 0.0 | 0.0 | n/a | n/a n/a n/a | unavailable unavailable unavailable |
| s4-ramp-large-h2-bounded | both | candidate | capacity | 1 | 0 | 1 | 0.0 | 0.0 | 0.0 | n/a | n/a | unavailable |
| s4-ramp-large-h2-bounded | both | main | capacity | 1 | 0 | 1 | 0.0 | 0.0 | 0.0 | n/a | n/a | unavailable |
| s4-ramp-large-h2-bounded | none | candidate | capacity | 1 | 0 | 1 | 0.0 | 0.0 | 0.0 | n/a | n/a | unavailable |
| s4-ramp-large-h2-bounded | none | main | capacity | 1 | 0 | 1 | 0.0 | 0.0 | 0.0 | n/a | n/a | unavailable |

## Phases and classes

One row per repetition, phase and workload class. Goodput counts verified responses that started and finished in the window; requests crossing in from warm-up and those outstanding at the window end are excluded from it.

| Scenario | Variant | Target | Run | Rep | Phase | Class | Proto | C | Attempts | Verified in window | Goodput rps | Crossed in | Outstanding | p99 ms | Outcomes |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| s11-arg-amplification-h1-320m | none | candidate | bounded-diagnostic | 1 | c1 | load | h1 | 1 | 11365 | 11364 | 1136.4 | 1 | 1 | 1.9 | ok ×11365 |
| s11-arg-amplification-h1-320m | none | candidate | bounded-diagnostic | 1 | c2 | load | h1 | 2 | 28020 | 28018 | 2801.8 | 2 | 2 | 2.0 | ok ×28020 |
| s11-arg-amplification-h1-320m | none | candidate | bounded-diagnostic | 1 | c4 | load | h1 | 4 | 56861 | 56857 | 5685.7 | 4 | 4 | 2.9 | ok ×56861 |
| s11-arg-amplification-h1-320m | none | candidate | bounded-diagnostic | 1 | c8 | load | h1 | 8 | 66640 | 66633 | 6663.3 | 8 | 7 | 7.3 | ok ×66640 |
| s11-arg-amplification-h1-320m | none | candidate | bounded-diagnostic | 1 | c16 | load | h1 | 16 | 61978 | 61962 | 6196.2 | 16 | 16 | 11.4 | ok ×61978 |
| s11-arg-amplification-h1-320m | none | candidate | bounded-diagnostic | 2 | c1 | load | h1 | 1 | 8736 | 8735 | 873.5 | 1 | 1 | 3.4 | ok ×8736 |
| s11-arg-amplification-h1-320m | none | candidate | bounded-diagnostic | 2 | c2 | load | h1 | 2 | 25348 | 25346 | 2534.6 | 2 | 2 | 2.3 | ok ×25348 |
| s11-arg-amplification-h1-320m | none | candidate | bounded-diagnostic | 2 | c4 | load | h1 | 4 | 59446 | 59442 | 5944.2 | 4 | 4 | 2.7 | ok ×59446 |
| s11-arg-amplification-h1-320m | none | candidate | bounded-diagnostic | 2 | c8 | load | h1 | 8 | 77986 | 77978 | 7797.8 | 8 | 8 | 5.8 | ok ×77986 |
| s11-arg-amplification-h1-320m | none | candidate | bounded-diagnostic | 2 | c16 | load | h1 | 16 | 82995 | 82979 | 8297.9 | 16 | 16 | 9.4 | ok ×82995 |
| s11-arg-amplification-h1-320m | none | candidate | bounded-diagnostic | 3 | c1 | load | h1 | 1 | 8685 | 8684 | 868.4 | 1 | 1 | 3.5 | ok ×8685 |
| s11-arg-amplification-h1-320m | none | candidate | bounded-diagnostic | 3 | c2 | load | h1 | 2 | 26842 | 26840 | 2684.0 | 2 | 2 | 2.0 | ok ×26842 |
| s11-arg-amplification-h1-320m | none | candidate | bounded-diagnostic | 3 | c4 | load | h1 | 4 | 55508 | 55504 | 5550.4 | 4 | 4 | 3.5 | ok ×55508 |
| s11-arg-amplification-h1-320m | none | candidate | bounded-diagnostic | 3 | c8 | load | h1 | 8 | 70210 | 70203 | 7020.3 | 8 | 7 | 7.4 | ok ×70210 |
| s11-arg-amplification-h1-320m | none | candidate | bounded-diagnostic | 3 | c16 | load | h1 | 16 | 78196 | 78180 | 7818.0 | 16 | 16 | 9.5 | ok ×78196 |
| s11-arg-amplification-h1-320m | none | main | bounded-diagnostic | 1 | c1 | load | h1 | 1 | 12028 | 12027 | 1202.7 | 1 | 1 | 2.2 | ok ×12028 |
| s11-arg-amplification-h1-320m | none | main | bounded-diagnostic | 1 | c2 | load | h1 | 2 | 27871 | 27869 | 2786.9 | 2 | 2 | 2.5 | ok ×27871 |
| s11-arg-amplification-h1-320m | none | main | bounded-diagnostic | 1 | c4 | load | h1 | 4 | 44570 | 44566 | 4456.6 | 4 | 4 | 4.8 | ok ×44570 |
| s11-arg-amplification-h1-320m | none | main | bounded-diagnostic | 1 | c8 | load | h1 | 8 | 49172 | 49164 | 4916.4 | 8 | 8 | 9.4 | ok ×49172 |
| s11-arg-amplification-h1-320m | none | main | bounded-diagnostic | 1 | c16 | load | h1 | 16 | 55516 | 55500 | 5550.0 | 16 | 16 | 12.4 | ok ×55516 |
| s11-arg-amplification-h1-320m | none | main | bounded-diagnostic | 2 | c1 | load | h1 | 1 | 15426 | 15425 | 1542.5 | 1 | 1 | 1.5 | ok ×15426 |
| s11-arg-amplification-h1-320m | none | main | bounded-diagnostic | 2 | c2 | load | h1 | 2 | 33494 | 33492 | 3349.2 | 2 | 2 | 1.6 | ok ×33494 |
| s11-arg-amplification-h1-320m | none | main | bounded-diagnostic | 2 | c4 | load | h1 | 4 | 59098 | 59094 | 5909.4 | 4 | 4 | 2.8 | ok ×59098 |
| s11-arg-amplification-h1-320m | none | main | bounded-diagnostic | 2 | c8 | load | h1 | 8 | 51491 | 51483 | 5148.3 | 8 | 8 | 9.1 | ok ×51491 |
| s11-arg-amplification-h1-320m | none | main | bounded-diagnostic | 2 | c16 | load | h1 | 16 | 48010 | 47994 | 4799.4 | 15 | 16 | 14.4 | ok ×48010 |
| s11-arg-amplification-h1-320m | none | main | bounded-diagnostic | 3 | c1 | load | h1 | 1 | 17031 | 17030 | 1703.0 | 1 | 1 | 1.4 | ok ×17031 |
| s11-arg-amplification-h1-320m | none | main | bounded-diagnostic | 3 | c2 | load | h1 | 2 | 34105 | 34103 | 3410.3 | 2 | 2 | 1.5 | ok ×34105 |
| s11-arg-amplification-h1-320m | none | main | bounded-diagnostic | 3 | c4 | load | h1 | 4 | 52029 | 52025 | 5202.5 | 4 | 4 | 3.6 | ok ×52029 |
| s11-arg-amplification-h1-320m | none | main | bounded-diagnostic | 3 | c8 | load | h1 | 8 | 56701 | 56693 | 5669.3 | 8 | 8 | 7.6 | ok ×56701 |
| s11-arg-amplification-h1-320m | none | main | bounded-diagnostic | 3 | c16 | load | h1 | 16 | 44094 | 44078 | 4407.8 | 16 | 16 | 16.2 | ok ×44094 |
| s11-arg-amplification-h1-320m | waf | candidate | bounded-diagnostic | 1 | c1 | load | h1 | 1 | 40 | 39 | 3.9 | 1 | 1 | 286.0 | ok ×40 |
| s11-arg-amplification-h1-320m | waf | candidate | bounded-diagnostic | 1 | c2 | load | h1 | 2 | 79 | 77 | 7.7 | 2 | 2 | 389.4 | ok ×79 |
| s11-arg-amplification-h1-320m | waf | candidate | bounded-diagnostic | 1 | c4 | load | h1 | 4 | 658 | 70 | 7.0 | 4 | 4 | 629.8 | ok ×72, status:503-inspection-overloaded ×586 |
| s11-arg-amplification-h1-320m | waf | candidate | bounded-diagnostic | 1 | c8 | load | h1 | 8 | 3412 | 60 | 6.0 | 8 | 8 | 631.8 | ok ×64, status:503-inspection-overloaded ×3348 |
| s11-arg-amplification-h1-320m | waf | candidate | bounded-diagnostic | 1 | c16 | load | h1 | 16 | 7138 | 53 | 5.3 | 16 | 16 | 775.6 | ok ×56, status:503-inspection-overloaded ×7082 |
| s11-arg-amplification-h1-320m | waf | candidate | bounded-diagnostic | 2 | c1 | load | h1 | 1 | 33 | 32 | 3.2 | 1 | 1 | 367.7 | ok ×33 |
| s11-arg-amplification-h1-320m | waf | candidate | bounded-diagnostic | 2 | c2 | load | h1 | 2 | 67 | 65 | 6.5 | 2 | 2 | 350.3 | ok ×67 |
| s11-arg-amplification-h1-320m | waf | candidate | bounded-diagnostic | 2 | c4 | load | h1 | 4 | 629 | 63 | 6.3 | 4 | 4 | 642.7 | ok ×65, status:503-inspection-overloaded ×564 |
| s11-arg-amplification-h1-320m | waf | candidate | bounded-diagnostic | 2 | c8 | load | h1 | 8 | 3090 | 70 | 7.0 | 8 | 8 | 597.4 | ok ×74, status:503-inspection-overloaded ×3016 |
| s11-arg-amplification-h1-320m | waf | candidate | bounded-diagnostic | 2 | c16 | load | h1 | 16 | 6380 | 70 | 7.0 | 16 | 16 | 598.0 | ok ×72, status:503-inspection-overloaded ×6308 |
| s11-arg-amplification-h1-320m | waf | candidate | bounded-diagnostic | 3 | c1 | load | h1 | 1 | 37 | 36 | 3.6 | 1 | 1 | 332.7 | ok ×37 |
| s11-arg-amplification-h1-320m | waf | candidate | bounded-diagnostic | 3 | c2 | load | h1 | 2 | 78 | 76 | 7.6 | 2 | 2 | 321.4 | ok ×78 |
| s11-arg-amplification-h1-320m | waf | candidate | bounded-diagnostic | 3 | c4 | load | h1 | 4 | 629 | 79 | 7.9 | 4 | 4 | 510.5 | ok ×82, status:503-inspection-overloaded ×547 |
| s11-arg-amplification-h1-320m | waf | candidate | bounded-diagnostic | 3 | c8 | load | h1 | 8 | 2757 | 73 | 7.3 | 8 | 8 | 649.3 | ok ×76, status:503-inspection-overloaded ×2681 |
| s11-arg-amplification-h1-320m | waf | candidate | bounded-diagnostic | 3 | c16 | load | h1 | 16 | 6597 | 60 | 6.0 | 16 | 16 | 690.9 | ok ×62, status:503-inspection-overloaded ×6535 |
| s11-arg-amplification-h1-320m | waf | main | bounded-diagnostic | 1 | c1 | load | h1 | 1 | 29 | 28 | 2.8 | 1 | 1 | 377.2 | ok ×29 |
| s11-arg-amplification-h1-320m | waf | main | bounded-diagnostic | 1 | c2 | load | h1 | 2 | 72 | 70 | 7.0 | 2 | 2 | 371.7 | ok ×72 |
| s11-arg-amplification-h1-320m | waf | main | bounded-diagnostic | 1 | c4 | load | h1 | 4 | 680 | 84 | 8.4 | 4 | 4 | 481.7 | ok ×87, status:503-inspection-overloaded ×593 |
| s11-arg-amplification-h1-320m | waf | main | bounded-diagnostic | 1 | c8 | load | h1 | 8 | 3148 | 82 | 8.2 | 8 | 8 | 521.6 | ok ×85, status:503-inspection-overloaded ×3063 |
| s11-arg-amplification-h1-320m | waf | main | bounded-diagnostic | 1 | c16 | load | h1 | 16 | 8158 | 73 | 7.3 | 16 | 16 | 651.4 | ok ×75, status:503-inspection-overloaded ×8083 |
| s11-arg-amplification-h1-320m | waf | main | bounded-diagnostic | 2 | c1 | load | h1 | 1 | 34 | 33 | 3.3 | 1 | 1 | 323.0 | ok ×34 |
| s11-arg-amplification-h1-320m | waf | main | bounded-diagnostic | 2 | c2 | load | h1 | 2 | 74 | 72 | 7.2 | 2 | 2 | 320.3 | ok ×74 |
| s11-arg-amplification-h1-320m | waf | main | bounded-diagnostic | 2 | c4 | load | h1 | 4 | 696 | 78 | 7.8 | 4 | 4 | 490.9 | ok ×81, status:503-inspection-overloaded ×615 |
| s11-arg-amplification-h1-320m | waf | main | bounded-diagnostic | 2 | c8 | load | h1 | 8 | 2455 | 76 | 7.6 | 8 | 8 | 551.4 | ok ×78, status:503-inspection-overloaded ×2377 |
| s11-arg-amplification-h1-320m | waf | main | bounded-diagnostic | 2 | c16 | load | h1 | 16 | 7263 | 71 | 7.1 | 16 | 16 | 584.0 | ok ×73, status:503-inspection-overloaded ×7190 |
| s11-arg-amplification-h1-320m | waf | main | bounded-diagnostic | 3 | c1 | load | h1 | 1 | 36 | 35 | 3.5 | 1 | 1 | 314.3 | ok ×36 |
| s11-arg-amplification-h1-320m | waf | main | bounded-diagnostic | 3 | c2 | load | h1 | 2 | 78 | 76 | 7.6 | 2 | 2 | 341.2 | ok ×78 |
| s11-arg-amplification-h1-320m | waf | main | bounded-diagnostic | 3 | c4 | load | h1 | 4 | 634 | 81 | 8.1 | 4 | 4 | 471.0 | ok ×83, status:503-inspection-overloaded ×551 |
| s11-arg-amplification-h1-320m | waf | main | bounded-diagnostic | 3 | c8 | load | h1 | 8 | 3033 | 69 | 6.9 | 8 | 8 | 626.2 | ok ×71, status:503-inspection-overloaded ×2962 |
| s11-arg-amplification-h1-320m | waf | main | bounded-diagnostic | 3 | c16 | load | h1 | 16 | 7627 | 61 | 6.1 | 16 | 16 | 720.6 | ok ×64, status:503-inspection-overloaded ×7563 |

## Non-success outcome latency

Completed attempts started in the window, per outcome (for example `status:503` for overload), including those that finished after it.

| Scenario | Variant | Target | Run | Rep | Phase | Class | Outcome | n | p50 ms | p95 ms | p99 ms | max ms |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: |
| s11-arg-amplification-h1-320m | waf | candidate | bounded-diagnostic | 1 | c4 | load | status:503-inspection-overloaded | 586 | 20.2 | 41.4 | 52.8 | 81.1 |
| s11-arg-amplification-h1-320m | waf | candidate | bounded-diagnostic | 1 | c8 | load | status:503-inspection-overloaded | 3348 | 10.2 | 47.9 | 55.9 | 78.5 |
| s11-arg-amplification-h1-320m | waf | candidate | bounded-diagnostic | 1 | c16 | load | status:503-inspection-overloaded | 7082 | 15.6 | 51.9 | 61.3 | 79.2 |
| s11-arg-amplification-h1-320m | waf | candidate | bounded-diagnostic | 2 | c4 | load | status:503-inspection-overloaded | 564 | 20.1 | 40.5 | 55.8 | 90.8 |
| s11-arg-amplification-h1-320m | waf | candidate | bounded-diagnostic | 2 | c8 | load | status:503-inspection-overloaded | 3016 | 15.5 | 41.7 | 53.4 | 68.8 |
| s11-arg-amplification-h1-320m | waf | candidate | bounded-diagnostic | 2 | c16 | load | status:503-inspection-overloaded | 6308 | 19.8 | 53.3 | 65.9 | 114.0 |
| s11-arg-amplification-h1-320m | waf | candidate | bounded-diagnostic | 3 | c4 | load | status:503-inspection-overloaded | 547 | 20.2 | 52.7 | 55.6 | 71.1 |
| s11-arg-amplification-h1-320m | waf | candidate | bounded-diagnostic | 3 | c8 | load | status:503-inspection-overloaded | 2681 | 19.8 | 43.0 | 58.7 | 77.1 |
| s11-arg-amplification-h1-320m | waf | candidate | bounded-diagnostic | 3 | c16 | load | status:503-inspection-overloaded | 6535 | 19.8 | 46.4 | 59.6 | 80.4 |
| s11-arg-amplification-h1-320m | waf | main | bounded-diagnostic | 1 | c4 | load | status:503-inspection-overloaded | 593 | 20.1 | 40.6 | 45.1 | 58.4 |
| s11-arg-amplification-h1-320m | waf | main | bounded-diagnostic | 1 | c8 | load | status:503-inspection-overloaded | 3063 | 14.4 | 43.2 | 55.1 | 70.7 |
| s11-arg-amplification-h1-320m | waf | main | bounded-diagnostic | 1 | c16 | load | status:503-inspection-overloaded | 8083 | 9.3 | 54.2 | 61.3 | 84.8 |
| s11-arg-amplification-h1-320m | waf | main | bounded-diagnostic | 2 | c4 | load | status:503-inspection-overloaded | 615 | 20.1 | 40.5 | 55.2 | 83.4 |
| s11-arg-amplification-h1-320m | waf | main | bounded-diagnostic | 2 | c8 | load | status:503-inspection-overloaded | 2377 | 20.1 | 45.9 | 59.8 | 66.1 |
| s11-arg-amplification-h1-320m | waf | main | bounded-diagnostic | 2 | c16 | load | status:503-inspection-overloaded | 7190 | 15.2 | 44.1 | 59.9 | 130.7 |
| s11-arg-amplification-h1-320m | waf | main | bounded-diagnostic | 3 | c4 | load | status:503-inspection-overloaded | 551 | 20.2 | 48.7 | 56.2 | 70.1 |
| s11-arg-amplification-h1-320m | waf | main | bounded-diagnostic | 3 | c8 | load | status:503-inspection-overloaded | 2962 | 14.6 | 45.6 | 59.1 | 80.9 |
| s11-arg-amplification-h1-320m | waf | main | bounded-diagnostic | 3 | c16 | load | status:503-inspection-overloaded | 7563 | 9.8 | 45.4 | 60.4 | 89.7 |

## Failed, invalid or inconclusive runs

- bounded-diagnostic s3-large-h2-bounded/both/candidate rep1: instrument-invalid: render: plugin timeout and max_instances are only rendered for the request-only fixture
- bounded-diagnostic s3-large-h2-bounded/both/candidate rep2: instrument-invalid: render: plugin timeout and max_instances are only rendered for the request-only fixture
- bounded-diagnostic s3-large-h2-bounded/both/candidate rep3: instrument-invalid: render: plugin timeout and max_instances are only rendered for the request-only fixture
- bounded-diagnostic s3-large-h2-bounded/both/main rep1: instrument-invalid: render: plugin timeout and max_instances are only rendered for the request-only fixture
- bounded-diagnostic s3-large-h2-bounded/both/main rep2: instrument-invalid: render: plugin timeout and max_instances are only rendered for the request-only fixture
- bounded-diagnostic s3-large-h2-bounded/both/main rep3: instrument-invalid: render: plugin timeout and max_instances are only rendered for the request-only fixture
- bounded-diagnostic s3-large-h2-bounded/none/candidate rep1: instrument-invalid: render: plugin timeout and max_instances are only rendered for the request-only fixture
- bounded-diagnostic s3-large-h2-bounded/none/candidate rep2: instrument-invalid: render: plugin timeout and max_instances are only rendered for the request-only fixture
- bounded-diagnostic s3-large-h2-bounded/none/candidate rep3: instrument-invalid: render: plugin timeout and max_instances are only rendered for the request-only fixture
- bounded-diagnostic s3-large-h2-bounded/none/main rep1: instrument-invalid: render: plugin timeout and max_instances are only rendered for the request-only fixture
- bounded-diagnostic s3-large-h2-bounded/none/main rep2: instrument-invalid: render: plugin timeout and max_instances are only rendered for the request-only fixture
- bounded-diagnostic s3-large-h2-bounded/none/main rep3: instrument-invalid: render: plugin timeout and max_instances are only rendered for the request-only fixture
- bounded-diagnostic s4-ramp-large-h2-bounded/both/candidate rep1: instrument-invalid: render: plugin timeout and max_instances are only rendered for the request-only fixture
- bounded-diagnostic s4-ramp-large-h2-bounded/both/main rep1: instrument-invalid: render: plugin timeout and max_instances are only rendered for the request-only fixture
- bounded-diagnostic s4-ramp-large-h2-bounded/none/candidate rep1: instrument-invalid: render: plugin timeout and max_instances are only rendered for the request-only fixture
- bounded-diagnostic s4-ramp-large-h2-bounded/none/main rep1: instrument-invalid: render: plugin timeout and max_instances are only rendered for the request-only fixture
