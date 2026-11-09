| scenario | variant | rep | phase | verified/s | verified | non-200 statuses | errors | client-cancelled | p50/p99 ms | Jul CPU s | phase peak MiB | inst |
|---|---|---|---|---:|---:|---|---|---:|---|---:|---:|---:|
| s1-normal-h1 | none | 1 | load[0] | 5210.8 | 104215 |  | {'unfinished-at-phase-end': 14} | 0 | 2/12 | 24.6 | 22 | 0 |
| s1-normal-h1 | none | 2 | load[0] | 5642.3 | 112846 |  | {'unfinished-at-phase-end': 16} | 0 | 2/12 | 24.4 | 22 | 0 |
| s1-normal-h1 | none | 3 | load[0] | 4416.0 | 88320 |  | {'unfinished-at-phase-end': 15} | 0 | 3/14 | 25.2 | 22 | 0 |
| s1-normal-h1 | waf | 1 | load[0] | 14.6 | 291 |  | {'unfinished-at-phase-end': 16} | 0 | 1042/1341 | 39.7 | 147 | 0 |
| s1-normal-h1 | waf | 2 | load[0] | 18.2 | 365 |  | {'unfinished-at-phase-end': 16} | 0 | 827/1146 | 39.8 | 148 | 0 |
| s1-normal-h1 | waf | 3 | load[0] | 17.4 | 348 |  | {'unfinished-at-phase-end': 16} | 0 | 861/1247 | 39.7 | 149 | 0 |
| s1-normal-h1 | wasm | 1 | load[0] | 554.3 | 11086 |  | {'unfinished-at-phase-end': 16} | 0 | 26/73 | 39.5 | 361 | 11 |
| s1-normal-h1 | wasm | 2 | load[0] | 965.1 | 19303 |  | {'unfinished-at-phase-end': 15} | 0 | 15/43 | 39.4 | 333 | 10 |
| s1-normal-h1 | wasm | 3 | load[0] | 1009.5 | 20190 |  | {'unfinished-at-phase-end': 15} | 0 | 14/41 | 39.3 | 291 | 8 |
| s1-normal-h1 | both | 1 | load[0] | 18.6 | 371 | {'500': 1} | {'unfinished-at-phase-end': 16} | 0 | 818/1267 | 39.5 | 310 | 4 |
| s1-normal-h1 | both | 2 | load[0] | 18.2 | 365 | {'500': 1} | {'unfinished-at-phase-end': 15} | 0 | 830/1145 | 39.8 | 337 | 6 |
| s1-normal-h1 | both | 3 | load[0] | 18.8 | 376 | {'500': 1} | {'unfinished-at-phase-end': 16} | 0 | 817/1084 | 39.8 | 313 | 4 |
| s2-normal-h2 | none | 1 | load[0] | 2736.1 | 54721 |  | {'unfinished-at-phase-end': 11} | 0 | 5/14 | 25.4 | 24 | 0 |
| s2-normal-h2 | none | 2 | load[0] | 2824.2 | 56483 |  | {'unfinished-at-phase-end': 11} | 0 | 5/14 | 25.3 | 24 | 0 |
| s2-normal-h2 | none | 3 | load[0] | 2732.2 | 54643 |  | {'unfinished-at-phase-end': 11} | 0 | 6/14 | 25.3 | 23 | 0 |
| s2-normal-h2 | waf | 1 | load[0] | 18.6 | 372 |  | {'unfinished-at-phase-end': 16} | 0 | 804/1144 | 39.6 | 140 | 0 |
| s2-normal-h2 | waf | 2 | load[0] | 18.6 | 372 |  | {'unfinished-at-phase-end': 16} | 0 | 831/1136 | 39.5 | 142 | 0 |
| s2-normal-h2 | waf | 3 | load[0] | 18.2 | 364 |  | {'unfinished-at-phase-end': 16} | 0 | 835/1157 | 39.7 | 139 | 0 |
| s2-normal-h2 | wasm | 1 | load[0] | 869.5 | 17389 |  | {'unfinished-at-phase-end': 16} | 0 | 17/41 | 38.7 | 550 | 22 |
| s2-normal-h2 | wasm | 2 | load[0] | 885.8 | 17715 |  | {'unfinished-at-phase-end': 16} | 0 | 17/40 | 38.8 | 510 | 20 |
| s2-normal-h2 | wasm | 3 | load[0] | 865.0 | 17301 |  | {'unfinished-at-phase-end': 16} | 0 | 17/41 | 38.6 | 518 | 20 |
| s2-normal-h2 | both | 1 | load[0] | 17.8 | 355 | {'500': 1} | {'unfinished-at-phase-end': 16} | 0 | 852/1166 | 39.7 | 341 | 7 |
| s2-normal-h2 | both | 2 | load[0] | 18.1 | 361 | {'500': 5} | {'unfinished-at-phase-end': 16} | 0 | 846/1238 | 39.8 | 427 | 11 |
| s2-normal-h2 | both | 3 | load[0] | 18.1 | 363 |  | {'unfinished-at-phase-end': 16} | 0 | 843/1212 | 39.7 | 360 | 8 |
| s3-large-h2 | none | 1 | load[0] | 214.1 | 4282 |  | {'unfinished-at-phase-end': 32} | 0 | 148/225 | 25.8 | 39 | 0 |
| s3-large-h2 | none | 2 | load[0] | 207.2 | 4145 |  | {'unfinished-at-phase-end': 32} | 0 | 152/242 | 25.9 | 42 | 0 |
| s3-large-h2 | none | 3 | load[0] | 208.4 | 4168 |  | {'unfinished-at-phase-end': 32} | 0 | 156/210 | 25.5 | 44 | 0 |
| s3-large-h2 | waf | 1 | load[0] | 0.0 | 0 |  | {'unfinished-at-phase-end': 16} | 0 | 0/0 | 39.9 | 703 | 0 |
| s3-large-h2 | waf | 2 | load[0] | 0.0 | 0 |  | {'unfinished-at-phase-end': 27} | 0 | 0/0 | 39.8 | 682 | 0 |
| s3-large-h2 | waf | 3 | load[0] | 0.0 | 0 |  | {'unfinished-at-phase-end': 32} | 0 | 0/0 | 39.9 | 670 | 0 |
| s3-large-h2 | wasm | 1 | load[0] | 54.2 | 1085 | {'500': 1711} | {'unfinished-at-phase-end': 32} | 0 | 134/234 | 38.3 | 721 | 35 |
| s3-large-h2 | wasm | 2 | load[0] | 53.3 | 1066 | {'500': 1693} | {'unfinished-at-phase-end': 32} | 0 | 137/233 | 38.4 | 739 | 34 |
| s3-large-h2 | wasm | 3 | load[0] | 54.4 | 1088 | {'500': 1670} | {'unfinished-at-phase-end': 30} | 0 | 136/233 | 38.5 | 691 | 34 |
| s3-large-h2 | both | 1 | load[0] | 0.6 | 11 | {'500': 423} | {'unfinished-at-phase-end': 28} | 0 | 8594/9817 | 39.7 | 678 | 23 |
| s3-large-h2 | both | 2 | load[0] | 0.6 | 12 | {'500': 478} | {'unfinished-at-phase-end': 32} | 0 | 8155/8670 | 39.7 | 671 | 24 |
| s3-large-h2 | both | 3 | load[0] | 0.4 | 8 | {'500': 448} | {'unfinished-at-phase-end': 32} | 0 | 10858/11100 | 39.8 | 677 | 24 |
| s4-ramp-large-h2 | none | 1 | c16[0] | 225.3 | 2253 |  | {'unfinished-at-phase-end': 14} | 0 | 71/109 | 12.9 | 31 | 0 |
| s4-ramp-large-h2 | none | 1 | c32[0] | 225.3 | 2253 |  | {'unfinished-at-phase-end': 32} | 0 | 145/204 | 12.9 | 38 | 0 |
| s4-ramp-large-h2 | none | 1 | c64[0] | 227.8 | 2278 |  | {'unfinished-at-phase-end': 64} | 0 | 298/388 | 13.0 | 44 | 0 |
| s4-ramp-large-h2 | none | 1 | c128[0] | 159.9 | 1599 |  | {'unfinished-at-phase-end': 121} | 0 | 745/922 | 13.3 | 63 | 0 |
| s4-ramp-large-h2 | none | 1 | c192[0] | 128.1 | 1281 |  | {'unfinished-at-phase-end': 192} | 0 | 1288/1697 | 13.3 | 83 | 0 |
| s4-ramp-large-h2 | both | 1 | c16[0] | 0.0 | 0 |  | {'unfinished-at-phase-end': 3} | 0 | 0/0 | 19.9 | 484 | 4 |
| s4-ramp-large-h2 | both | 1 | c32[0] | 0.0 | 0 | {'500': 168} | {'unfinished-at-phase-end': 32} | 0 | 0/0 | 19.9 | 659 | 23 |
| s4-ramp-large-h2 | both | 1 | c64[0] | 0.0 | 0 |  | {'eof': 8, 'refused': 1899852, 'unfinished-at-phase-end': 64} | 0 | 0/0 | -54.9 | 0 | 0 |
| s4-ramp-large-h2 | both | 1 | c128[0] | 0.0 | 0 |  | {'refused': 1834403, 'unfinished-at-phase-end': 92} | 0 | 0/0 | 0.0 | 0 | 0 |
| s4-ramp-large-h2 | both | 1 | c192[0] | 0.0 | 0 |  | {'refused': 1371721, 'unfinished-at-phase-end': 191} | 0 | 0/0 | 0.0 | 0 | 0 |
| s5-admission-h2 | none | 1 | load[0] | 0.2 | 5 | {'503': 176475} | {'unfinished-at-phase-end': 114} | 0 | 1562/2525 | 23.1 | 26 | 0 |
| s5-admission-h2 | both | 1 | load[0] | 0.0 | 0 |  | {'refused': 3449306, 'unfinished-at-phase-end': 1} | 0 | 0/0 | 0.0 | 0 | 0 |
| s6-static-h2 | none | 1 | load[0] | 633.5 | 12671 |  | {'unfinished-at-phase-end': 126} | 0 | 199/368 | 26.0 | 38 | 0 |
| s6-static-h2 | both | 1 | load[0] | 0.0 | 0 |  | {'refused': 3708826, 'unfinished-at-phase-end': 105} | 0 | 0/0 | 0.0 | 0 | 0 |
| s7-cache-h1 | none | 1 | cold[0] | 2180.5 | 43610 |  | {'unfinished-at-phase-end': 15} | 0 | 6/23 | 24.0 | 190 | 0 |
| s7-cache-h1 | none | 1 | warm[0] | 10506.7 | 210134 |  | {'unfinished-at-phase-end': 12} | 0 | 1/8 | 33.1 | 157 | 0 |
| s7-cache-h1 | both | 1 | cold[0] | 17.5 | 350 | {'500': 4} | {'unfinished-at-phase-end': 16} | 0 | 856/1138 | 39.8 | 460 | 5 |
| s7-cache-h1 | both | 1 | warm[0] | 17.6 | 353 | {'500': 8} | {'unfinished-at-phase-end': 16} | 0 | 854/1124 | 39.8 | 452 | 5 |
| s8-slow-clients-h1 | none | 1 | load[0] | 19.2 | 384 |  | {'unfinished-at-phase-end': 96} | 0 | 3924/3929 | 22.1 | 50 | 0 |
| s8-slow-clients-h1 | none | 1 | load[1] | 0.0 | 0 |  | {'unfinished-at-phase-end': 32} | 288 | 0/0 | 22.1 | 50 | 0 |
| s8-slow-clients-h1 | none | 1 | load[2] | 8383.0 | 167659 |  |  | 0 | 0/2 | 22.1 | 50 | 0 |
| s8-slow-clients-h1 | waf | 1 | load[0] | 0.1 | 3 |  | {'unfinished-at-phase-end': 96} | 0 | 4565/4613 | 39.9 | 512 | 0 |
| s8-slow-clients-h1 | waf | 1 | load[1] | 0.0 | 0 |  | {'unfinished-at-phase-end': 32} | 288 | 0/0 | 39.9 | 512 | 0 |
| s8-slow-clients-h1 | waf | 1 | load[2] | 23.6 | 471 |  | {'unfinished-at-phase-end': 4} | 0 | 29/3014 | 39.9 | 512 | 0 |
| s8-slow-clients-h1 | wasm | 1 | load[0] | 0.0 | 0 |  | {'Post "http://127.0.0.1:18470/proxy/echo?resp=4096": readfrom': 96, 'refused': 1557647, 'unfinished-at-phase-end': 94} | 0 | 0/0 | -2.8 | 691 | 65 |
| s8-slow-clients-h1 | wasm | 1 | load[1] | 0.0 | 0 |  | {'Post "http://127.0.0.1:18470/proxy/echo?resp=4096": readfrom': 32, 'refused': 535063, 'unfinished-at-phase-end': 32} | 32 | 0/0 | -2.8 | 691 | 65 |
| s8-slow-clients-h1 | wasm | 1 | load[2] | 1.9 | 37 | {'503': 108} | {'eof': 4, 'refused': 68452, 'unfinished-at-phase-end': 4} | 0 | 10/108 | -2.8 | 691 | 65 |
| s8-slow-clients-h1 | both | 1 | load[0] | 0.0 | 0 |  | {'refused': 1864836, 'unfinished-at-phase-end': 96} | 0 | 0/0 | 0.0 | 0 | 0 |
| s8-slow-clients-h1 | both | 1 | load[1] | 0.0 | 0 |  | {'refused': 639039, 'unfinished-at-phase-end': 30} | 0 | 0/0 | 0.0 | 0 | 0 |
| s8-slow-clients-h1 | both | 1 | load[2] | 0.0 | 0 |  | {'refused': 80933, 'unfinished-at-phase-end': 4} | 0 | 0/0 | 0.0 | 0 | 0 |
| s9-reload-churn | both | 1 | load[0] | 3.2 | 129 | {'500': 3} | {'eof': 17, 'refused': 2270208, 'reset': 4, 'unfinished-at-phase-end': 16} | 0 | 1586/2424 | -12.3 | 744 | 9 |
| s9-reload-churn | both | 1 | load[1] | 0.4 | 16 | {'500': 2} | {'Post "https://localhost:18491/proxy/echo?resp=65536&delay=30': 31, 'refused': 1068905, 'reset': 16, 'unfinished-at-phase-end': 16} | 0 | 5437/6095 | -12.3 | 744 | 9 |
| s11-arg-amplification-h1 | none | 1 | c1[0] | 1777.9 | 17779 |  | {'unfinished-at-phase-end': 1} | 0 | 1/1 | 7.3 | 18 | 0 |
| s11-arg-amplification-h1 | none | 1 | c2[0] | 4032.4 | 40324 |  | {'unfinished-at-phase-end': 1} | 0 | 0/1 | 9.5 | 18 | 0 |
| s11-arg-amplification-h1 | none | 1 | c4[0] | 7282.4 | 72824 |  | {'unfinished-at-phase-end': 3} | 0 | 0/2 | 11.2 | 21 | 0 |
| s11-arg-amplification-h1 | none | 1 | c8[0] | 9324.9 | 93249 |  | {'unfinished-at-phase-end': 4} | 0 | 1/5 | 12.5 | 22 | 0 |
| s11-arg-amplification-h1 | none | 1 | c16[0] | 9683.4 | 96834 |  | {'unfinished-at-phase-end': 13} | 0 | 1/8 | 14.1 | 23 | 0 |
| s11-arg-amplification-h1 | waf | 1 | c1[0] | 4.1 | 41 |  | {'unfinished-at-phase-end': 1} | 0 | 232/322 | 10.5 | 256 | 0 |
| s11-arg-amplification-h1 | waf | 1 | c2[0] | 8.4 | 84 |  | {'unfinished-at-phase-end': 2} | 0 | 229/310 | 19.7 | 343 | 0 |
| s11-arg-amplification-h1 | waf | 1 | c4[0] | 8.7 | 87 |  | {'unfinished-at-phase-end': 4} | 0 | 443/554 | 20.0 | 524 | 0 |
| s11-arg-amplification-h1 | waf | 1 | c8[0] | 5.0 | 50 |  | {'eof': 6, 'refused': 370425, 'reset': 2, 'unfinished-at-phase-end': 8} | 0 | 850/1177 | -65.4 | 765 | 0 |
| s11-arg-amplification-h1 | waf | 1 | c16[0] | 0.0 | 0 |  | {'refused': 1254959, 'unfinished-at-phase-end': 15} | 0 | 0/0 | 0.0 | 0 | 0 |
| s10-normal-h3 | none | 1 | load[0] | 2318.9 | 46378 |  | {'unfinished-at-phase-end': 16} | 0 | 6/19 | 27.5 | 24 | 0 |
| s10-normal-h3 | both | 1 | load[0] | 16.1 | 323 |  | {'unfinished-at-phase-end': 16} | 0 | 922/1332 | 39.6 | 332 | 6 |

| scenario | variant | rep | cg peak MiB | OOM (events) | kernel OOM (journal) | exited | peak shmem MiB | peak spill files/MiB | peak goroutines | peak fds | base→final goroutines | base→final fds | base→final in-flight | post-GC heap MiB (base) | canary fails | origin calls |
|---|---|---|---:|---|---|---|---:|---|---:|---:|---|---|---|---|---:|---:|
| s1-normal-h1 | none | 1 | 24 | False | False |  | 0 | 0/0 | 96 | 55 | 29→34 | 20→24 | 0→0 | 11 (8) | 0 | 126561 |
| s1-normal-h1 | none | 2 | 24 | False | False |  | 0 | 0/0 | 93 | 53 | 30→28 | 20→21 | 0→0 | 11 (8) | 0 | 139662 |
| s1-normal-h1 | none | 3 | 24 | False | False |  | 0 | 0/0 | 95 | 54 | 30→33 | 20→23 | 0→0 | 11 (8) | 0 | 112850 |
| s1-normal-h1 | waf | 1 | 148 | False | False |  | 0 | 0/0 | 88 | 51 | 31→40 | 20→27 | 0→1 | 57 (67) | 0 | 376 |
| s1-normal-h1 | waf | 2 | 148 | False | False |  | 0 | 0/0 | 86 | 50 | 29→50 | 20→32 | 0→1 | 57 (76) | 0 | 476 |
| s1-normal-h1 | waf | 3 | 150 | False | False |  | 0 | 0/0 | 82 | 48 | 30→46 | 20→30 | 0→1 | 77 (74) | 0 | 455 |
| s1-normal-h1 | wasm | 1 | 362 | False | False |  | 0 | 0/0 | 121 | 52 | 30→37 | 20→25 | 0→0 | 29 (46) | 0 | 14183 |
| s1-normal-h1 | wasm | 2 | 339 | False | False |  | 0 | 0/0 | 135 | 55 | 30→44 | 20→29 | 0→0 | 29 (93) | 0 | 23743 |
| s1-normal-h1 | wasm | 3 | 301 | False | False |  | 0 | 0/0 | 125 | 54 | 29→37 | 20→25 | 0→1 | 28 (36) | 0 | 25217 |
| s1-normal-h1 | both | 1 | 320 | False | False |  | 0 | 0/0 | 97 | 53 | 30→49 | 20→31 | 0→0 | 81 (124) | 0 | 485 |
| s1-normal-h1 | both | 2 | 346 | False | False |  | 0 | 0/0 | 92 | 51 | 30→50 | 20→31 | 0→1 | 91 (105) | 0 | 477 |
| s1-normal-h1 | both | 3 | 315 | False | False |  | 0 | 0/0 | 96 | 53 | 29→54 | 20→34 | 0→1 | 93 (128) | 0 | 480 |
| s2-normal-h2 | none | 1 | 25 | False | False |  | 0 | 0/0 | 84 | 40 | 29→53 | 20→33 | 0→0 | 11 (8) | 0 | 69022 |
| s2-normal-h2 | none | 2 | 26 | False | False |  | 0 | 0/0 | 85 | 41 | 30→53 | 20→33 | 0→0 | 11 (8) | 0 | 71028 |
| s2-normal-h2 | none | 3 | 25 | False | False |  | 0 | 0/0 | 81 | 39 | 29→62 | 20→38 | 0→0 | 11 (8) | 0 | 68465 |
| s2-normal-h2 | waf | 1 | 140 | False | False |  | 0 | 0/0 | 71 | 34 | 29→48 | 20→31 | 0→1 | 68 (74) | 0 | 485 |
| s2-normal-h2 | waf | 2 | 142 | False | False |  | 0 | 0/0 | 77 | 37 | 30→56 | 20→35 | 0→1 | 58 (65) | 0 | 480 |
| s2-normal-h2 | waf | 3 | 140 | False | False |  | 0 | 0/0 | 77 | 37 | 29→54 | 20→34 | 0→1 | 58 (82) | 0 | 474 |
| s2-normal-h2 | wasm | 1 | 608 | False | False |  | 0 | 0/0 | 105 | 39 | 30→63 | 20→38 | 0→0 | 152 (37) | 0 | 21725 |
| s2-normal-h2 | wasm | 2 | 516 | False | False |  | 0 | 0/0 | 107 | 39 | 29→60 | 20→37 | 0→0 | 131 (41) | 0 | 22095 |
| s2-normal-h2 | wasm | 3 | 540 | False | False |  | 0 | 0/0 | 110 | 39 | 30→54 | 20→34 | 0→0 | 100 (45) | 0 | 21400 |
| s2-normal-h2 | both | 1 | 360 | False | False |  | 0 | 0/0 | 75 | 31 | 29→43 | 20→28 | 0→1 | 79 (128) | 0 | 459 |
| s2-normal-h2 | both | 2 | 427 | False | False |  | 0 | 0/0 | 91 | 39 | 30→58 | 20→36 | 0→1 | 83 (129) | 0 | 466 |
| s2-normal-h2 | both | 3 | 441 | False | False |  | 0 | 0/0 | 75 | 33 | 30→52 | 20→32 | 0→1 | 82 (106) | 0 | 465 |
| s3-large-h2 | none | 1 | 42 | False | False |  | 0 | 0/0 | 134 | 55 | 29→30 | 20→22 | 0→0 | 11 (8) | 0 | 5433 |
| s3-large-h2 | none | 2 | 43 | False | False |  | 0 | 0/0 | 139 | 55 | 29→32 | 20→23 | 0→0 | 11 (8) | 0 | 5221 |
| s3-large-h2 | none | 3 | 46 | False | False |  | 0 | 0/0 | 140 | 55 | 29→34 | 20→23 | 0→0 | 11 (8) | 0 | 5255 |
| s3-large-h2 | waf | 1 | 706 | False | False |  | 8 | 32/8 | 135 | 81 | 29→78 | 20→46 | 0→1 | 141 (77) | 0 | 32 |
| s3-large-h2 | waf | 2 | 685 | False | False |  | 8 | 32/8 | 147 | 86 | 29→93 | 20→53 | 0→0 | 416 (73) | 0 | 32 |
| s3-large-h2 | waf | 3 | 672 | False | False |  | 8 | 32/8 | 153 | 86 | 29→90 | 20→52 | 0→0 | 586 (70) | 0 | 32 |
| s3-large-h2 | wasm | 1 | 736 | False | False |  | 0 | 0/0 | 98 | 29 | 30→42 | 20→28 | 0→0 | 33 (36) | 0 | 1377 |
| s3-large-h2 | wasm | 2 | 741 | False | False |  | 0 | 0/0 | 103 | 30 | 29→44 | 20→29 | 0→0 | 98 (36) | 0 | 1339 |
| s3-large-h2 | wasm | 3 | 704 | False | False |  | 0 | 0/0 | 106 | 30 | 29→48 | 20→30 | 0→0 | 67 (42) | 0 | 1364 |
| s3-large-h2 | both | 1 | 680 | False | False |  | 4 | 15/4 | 82 | 41 | 30→39 | 20→26 | 0→1 | 93 (125) | 0 | 28 |
| s3-large-h2 | both | 2 | 671 | False | False |  | 3 | 13/3 | 87 | 39 | 30→42 | 20→27 | 0→1 | 86 (113) | 0 | 28 |
| s3-large-h2 | both | 3 | 680 | False | False |  | 4 | 15/4 | 81 | 43 | 30→42 | 20→27 | 0→1 | 107 (107) | 0 | 28 |
| s4-ramp-large-h2 | none | 1 | 85 | False | False |  | 0 | 0/0 | 660 | 218 | 29→72 | 20→42 | 0→0 | 16 (8) | 0 | 12444 |
| s4-ramp-large-h2 | both | 1 | n/a | False | True | signal: killed | 4 | 16/4 | 241 | 41 | 29→115 | 20→36 | 0→69 | 0 (107) | 0 | 16 |
| s5-admission-h2 | none | 1 | 28 | False | False |  | 0 | 0/0 | 179 | 32 | 30→30 | 20→22 | 0→0 | 11 (8) | 0 | 21 |
| s5-admission-h2 | both | 1 | n/a | False | True | signal: killed | 1 | 5/1 | 169 | 30 | 30→0 | 20→29 | 0→0 | 0 (108) | 0 | 3 |
| s6-static-h2 | none | 1 | 42 | False | False |  | 0 | 0/0 | 171 | 151 | 29→30 | 20→22 | 0→0 | 11 (8) | 0 | 0 |
| s6-static-h2 | both | 1 | n/a | False | True | signal: killed | 0 | 0/0 | 0 | 25 | 30→0 | 20→0 | 0→0 | 0 (126) | 0 | 0 |
| s7-cache-h1 | none | 1 | 207 | False | False |  | 0 | 0/0 | 97 | 55 | 30→54 | 20→33 | 0→0 | 78 (8) | 0 | 55551 |
| s7-cache-h1 | both | 1 | 465 | False | False |  | 0 | 0/0 | 102 | 54 | 29→43 | 20→28 | 0→0 | 182 (126) | 0 | 469 |
| s8-slow-clients-h1 | none | 1 | 51 | False | False |  | 0 | 0/0 | 437 | 290 | 30→37 | 20→25 | 0→0 | 12 (8) | 0 | 209700 |
| s8-slow-clients-h1 | waf | 1 | 514 | False | False |  | 0 | 0/0 | 373 | 216 | 30→75 | 20→44 | 0→0 | 262 (69) | 0 | 1173 |
| s8-slow-clients-h1 | wasm | 1 | n/a | False | True | signal: killed | 0 | 0/0 | 185 | 173 | 30→183 | 20→157 | 0→49 | 0 (36) | 0 | 77 |
| s8-slow-clients-h1 | both | 1 | n/a | False | True | signal: killed | 0 | 0/0 | 176 | 158 | 30→0 | 20→157 | 0→0 | 0 (123) | 0 | 22 |
| s9-reload-churn | both | 1 | n/a | False | True | signal: killed | 0 | 0/0 | 176 | 71 | 29→0 | 20→55 | 0→0 | 0 (125) | 0 | 262 |
| s11-arg-amplification-h1 | none | 1 | 26 | False | False |  | 0 | 0/0 | 93 | 54 | 30→42 | 20→27 | 0→0 | 12 (8) | 0 | 383316 |
| s11-arg-amplification-h1 | waf | 1 | n/a | False | True | signal: killed | 0 | 0/0 | 54 | 35 | 30→0 | 20→34 | 0→0 | 0 (70) | 0 | 323 |
| s10-normal-h3 | none | 1 | 26 | False | False |  | 0 | 0/0 | 84 | 38 | 30→53 | 20→33 | 0→0 | 10 (8) | 0 | 59571 |
| s10-normal-h3 | both | 1 | 334 | False | False |  | 0 | 0/0 | 71 | 29 | 30→45 | 20→29 | 0→1 | 95 (128) | 0 | 402 |
