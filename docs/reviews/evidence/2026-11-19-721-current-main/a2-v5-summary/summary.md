# Inspection assessment summary

Plan `inspection-assessment-plan/v5`, profile `qualification`. Runs: a2-targeted-v5.

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
| s3-large-h2-bounded | both | candidate | capacity | 3 | 0 | 3 | 0.0 | 0.0 | 0.0 | n/a | n/a n/a n/a | unavailable unavailable unavailable |
| s3-large-h2-bounded | both | main | capacity | 3 | 0 | 3 | 0.0 | 0.0 | 0.0 | n/a | n/a n/a n/a | unavailable unavailable unavailable |
| s3-large-h2-bounded | none | candidate | capacity | 3 | 3 | 3 | 238.1 | 173.2 | 259.0 | 0.360 | 0.67 0.66 0.67 | 40.8 46.5 47.2 |
| s3-large-h2-bounded | none | main | capacity | 3 | 3 | 3 | 258.6 | 243.8 | 265.6 | 0.084 | 0.67 0.66 0.66 | 43.1 45.4 41.6 |
| s4-ramp-large-h2-bounded | both | candidate | capacity | 1 | 0 | 1 | 0.0 | 0.0 | 0.0 | n/a | n/a | unavailable |
| s4-ramp-large-h2-bounded | both | main | capacity | 1 | 0 | 1 | 0.0 | 0.0 | 0.0 | n/a | n/a | unavailable |
| s4-ramp-large-h2-bounded | none | candidate | capacity | 1 | 1 | 1 | 229.2 | 229.2 | 229.2 | 0.000 | 0.67 | 82.6 |
| s4-ramp-large-h2-bounded | none | main | capacity | 1 | 1 | 1 | 240.5 | 240.5 | 240.5 | 0.000 | 0.66 | 87.5 |

## Phases and classes

One row per repetition, phase and workload class. Goodput counts verified responses that started and finished in the window; requests crossing in from warm-up and those outstanding at the window end are excluded from it.

| Scenario | Variant | Target | Run | Rep | Phase | Class | Proto | C | Attempts | Verified in window | Goodput rps | Crossed in | Outstanding | p99 ms | Outcomes |
| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| s3-large-h2-bounded | none | candidate | a2-targeted-v5 | 1 | load | load | h2 | 32 | 3497 | 3465 | 173.2 | 32 | 32 | 307.3 | ok ×3497 |
| s3-large-h2-bounded | none | candidate | a2-targeted-v5 | 2 | load | load | h2 | 32 | 5212 | 5180 | 259.0 | 32 | 32 | 162.6 | ok ×5212 |
| s3-large-h2-bounded | none | candidate | a2-targeted-v5 | 3 | load | load | h2 | 32 | 4794 | 4762 | 238.1 | 32 | 32 | 246.4 | ok ×4794 |
| s3-large-h2-bounded | none | main | a2-targeted-v5 | 1 | load | load | h2 | 32 | 5343 | 5311 | 265.6 | 32 | 32 | 187.3 | ok ×5343 |
| s3-large-h2-bounded | none | main | a2-targeted-v5 | 2 | load | load | h2 | 32 | 4908 | 4876 | 243.8 | 32 | 32 | 211.4 | ok ×4908 |
| s3-large-h2-bounded | none | main | a2-targeted-v5 | 3 | load | load | h2 | 32 | 5204 | 5172 | 258.6 | 32 | 32 | 157.8 | ok ×5204 |
| s4-ramp-large-h2-bounded | none | candidate | a2-targeted-v5 | 1 | c16 | load | h2 | 16 | 2308 | 2292 | 229.2 | 16 | 16 | 109.9 | ok ×2308 |
| s4-ramp-large-h2-bounded | none | candidate | a2-targeted-v5 | 1 | c32 | load | h2 | 32 | 2216 | 2184 | 218.4 | 32 | 32 | 261.3 | ok ×2216 |
| s4-ramp-large-h2-bounded | none | candidate | a2-targeted-v5 | 1 | c64 | load | h2 | 64 | 2255 | 2191 | 219.1 | 64 | 64 | 434.2 | ok ×2255 |
| s4-ramp-large-h2-bounded | none | candidate | a2-targeted-v5 | 1 | c128 | load | h2 | 128 | 2025 | 1897 | 189.7 | 127 | 128 | 879.5 | ok ×2025 |
| s4-ramp-large-h2-bounded | none | candidate | a2-targeted-v5 | 1 | c192 | load | h2 | 192 | 1836 | 1644 | 164.4 | 192 | 192 | 1250.9 | ok ×1836 |
| s4-ramp-large-h2-bounded | none | main | a2-targeted-v5 | 1 | c16 | load | h2 | 16 | 2421 | 2405 | 240.5 | 16 | 16 | 120.8 | ok ×2421 |
| s4-ramp-large-h2-bounded | none | main | a2-targeted-v5 | 1 | c32 | load | h2 | 32 | 2314 | 2282 | 228.2 | 32 | 32 | 280.8 | ok ×2314 |
| s4-ramp-large-h2-bounded | none | main | a2-targeted-v5 | 1 | c64 | load | h2 | 64 | 2328 | 2264 | 226.4 | 64 | 64 | 480.2 | ok ×2328 |
| s4-ramp-large-h2-bounded | none | main | a2-targeted-v5 | 1 | c128 | load | h2 | 128 | 2105 | 1977 | 197.7 | 128 | 128 | 736.8 | ok ×2105 |
| s4-ramp-large-h2-bounded | none | main | a2-targeted-v5 | 1 | c192 | load | h2 | 192 | 1826 | 1634 | 163.4 | 192 | 192 | 1495.1 | ok ×1826 |

## Failed, invalid or inconclusive runs

- a2-targeted-v5 s3-large-h2-bounded/both/candidate rep1: instrument-invalid: jul check rejected the generated configuration: exit status 1
- a2-targeted-v5 s3-large-h2-bounded/both/candidate rep2: instrument-invalid: jul check rejected the generated configuration: exit status 1
- a2-targeted-v5 s3-large-h2-bounded/both/candidate rep3: instrument-invalid: jul check rejected the generated configuration: exit status 1
- a2-targeted-v5 s3-large-h2-bounded/both/main rep1: instrument-invalid: jul check rejected the generated configuration: exit status 1
- a2-targeted-v5 s3-large-h2-bounded/both/main rep2: instrument-invalid: jul check rejected the generated configuration: exit status 1
- a2-targeted-v5 s3-large-h2-bounded/both/main rep3: instrument-invalid: jul check rejected the generated configuration: exit status 1
- a2-targeted-v5 s4-ramp-large-h2-bounded/both/candidate rep1: instrument-invalid: jul check rejected the generated configuration: exit status 1
- a2-targeted-v5 s4-ramp-large-h2-bounded/both/main rep1: instrument-invalid: jul check rejected the generated configuration: exit status 1
