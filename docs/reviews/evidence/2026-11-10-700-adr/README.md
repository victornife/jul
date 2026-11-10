# #700 ADR feasibility evidence (2026-11-10)

This directory contains the bounded design experiments behind
[ADR 0032](../../../adr/0032-aggregate-inspection-resource-governor.md). They support choosing an
architecture. They are **not** implementation acceptance: they do not complete the #700 matrix, and
they do not establish a resource guarantee, a release, maturity or soak.

## Identities

All identities are in [`raw/identities.txt`](raw/identities.txt).

- **Runtime.** Corrected `main` at `858590dc7c430d24cd33b096a81ca6051591b427`, which contains #702
  (`e1f37cf7`, fixing #697/#698) and #703 (`858590dc`, fixing #699).
- **Envelope binary.** The runtime with an *inert* scratch patch,
  [`instruments/f2-waf-slot-scratch.patch`](instruments/f2-waf-slot-scratch.patch), sha256
  `ddc99fff…df8b`. The patch is active only when `JUL_F2_WAF_SLOTS>0`. The F2 `slots0` control and all
  F3 and F4 runs used it inactive.
- **Harness.** The #695 archive plus the 2026-11-10 #699 revalidation patch, sha256 `fbb16f49…6519`.
- **Toolchain and host.** go1.27.3 linux/arm64, WSL2, 12 CPUs.
- **Envelope.** A systemd user scope with `CPUQuota=200%`, `MemoryMax=768M` and `MemorySwapMax=0`,
  pinned to CPUs 0,1 with `GOMAXPROCS=2`. The origin ran on CPUs 2,3 and the client on CPUs 4–9. See
  [`instruments/run-envelope.sh`](instruments/run-envelope.sh).

Instruments are stored with a `.txt` suffix so they are not compiled into the module. To re-run one,
copy it back as a `_test.go` file into the stated package, run it, then delete it.

## Experiments

### F1 — WAF allocation volume per request

- **Instrument:** [`instruments/f1-waf-amplification_test.go.txt`](instruments/f1-waf-amplification_test.go.txt)
  (`TestF1WAFAmplification`, package `internal/waf`, `-tags waf`, `F1=1`).
- **Setup:** CRS, 1 MB body limit, warm transaction pool, allocation averaged over repetitions.
- **Results:** [`raw/f1-waf-amplification.txt`](raw/f1-waf-amplification.txt).
  - 991 `items` arguments in a 21.9 KB body allocate 25 MiB and use 289 ms CPU per request.
  - "Wide" JSON allocates about 64× its body size.
- **Invalid column:** the `liveKiB` column is not valid. Its baseline included a pooled transaction
  from the warm-up. F1b replaces it.

### F1b — retained heap per open WAF transaction

- **Instrument:** `TestF1bRetainedPerOpenTransaction` in the same file (`F1=1`, `K=<n>`, optional
  `PHASE3=1`).
- **Method:** K concurrent requests hold their transactions open inside the downstream handler. The
  live `HeapAlloc` delta is measured after two GCs.
- **Results:**

  | Case | Result |
  | --- | --- |
  | K=1 | [`raw/f1b-retained-k1-phase2.txt`](raw/f1b-retained-k1-phase2.txt) |
  | K=8 | [`raw/f1b-retained-k8-phase2.txt`](raw/f1b-retained-k8-phase2.txt) |
  | K=32 | [`raw/f1b-retained-k32-phase2.txt`](raw/f1b-retained-k32-phase2.txt) |
  | K=8, after response headers | [`raw/f1b-retained-k8-phase3.txt`](raw/f1b-retained-k8-phase3.txt) |

- **Findings:**
  - A transaction holds about 37 KiB with no arguments.
  - With arguments it holds about 37–52 KiB per argument (991 arguments: 41.5 MiB).
  - Wide JSON holds about 39× its body size.
  - Per-transaction values do not depend on K. At K=32, 991 arguments produce a 1.3 GB live heap.
  - After response headers, Coraza clears its transformation cache at the start of the next phase
    (`internal/corazawaf/rulegroup.go:163-166`). The wide-JSON ratio then falls to about 22×, but
    argument collections remain until `tx.Close`.

### F2 — evaluation-slot prototype and governor model

- **Governor model:**
  - Instruments: [`instruments/f2-governor-prototype.go.txt`](instruments/f2-governor-prototype.go.txt)
    and its test file.
  - Six tests passed under `-race -count=3`; see [`raw/f2-governor-race.txt`](raw/f2-governor-race.txt).
  - What they cover:
    - one ceiling shared by 2 listeners × 2 generations;
    - limit decrease, drain and rollback;
    - a cancelled but uninterruptible evaluation stays charged;
    - nested layers share one lease without deadlock;
    - bounded FIFO waits, where abandoned waiters do not strand capacity;
    - grow-before-allocate, with a pre-charge capped for dishonest lengths.
- **s11 in the envelope:**
  - Instruments: [`instruments/f2-waf-slot-scratch.patch`](instruments/f2-waf-slot-scratch.patch),
    a fail-fast global semaphore around WAF request inspection that returns 503 with `Retry-After: 1`,
    and [`instruments/summarize-s11.py`](instruments/summarize-s11.py).
  - Results: `raw/f2-s11-*.{results.jsonl,manifest.json}`, summarised in
    [`raw/f2-s11-summary.txt`](raw/f2-s11-summary.txt).

  | Slots | Outcome |
  | --- | --- |
  | 0 | Killed at c8 (peak 762 MiB) |
  | 4 | No kill; peaks 624–648 MiB |
  | 2 (two repetitions) | Peaks 435–444 MiB; CPU saturated |

### F3 — H2 admission goodput (the s5 shape)

- **Instruments:** [`instruments/f3-h2-admission-client.go.txt`](instruments/f3-h2-admission-client.go.txt)
  and [`instruments/f3-s5-no-pending.toml`](instruments/f3-s5-no-pending.toml).
- **Results:** [`raw/f3-h2-admission.txt`](raw/f3-h2-admission.txt).
- **Setup:** 128 workers uploading 256 KiB each to an ADR 0017 pool with 8 active slots and a 500 ms
  origin delay.

  | Configuration | Goodput |
  | --- | --- |
  | Theoretical maximum | 16/s |
  | `max_pending_requests = 16`, one shared connection | 2.0–2.9/s |
  | Pending 16, with or without retry pacing | 2.0–2.9/s |
  | `max_pending_requests = 0` | 15.6/s |
  | 16 connections | 15.2/s |

- **Finding:** queued requests hold unread bodies that consume the shared connection's flow-control
  credit. The cause is not retry traffic.

### F4 and F4b — stalled and slow bodies

- **F4 instruments:** [`instruments/f4-stalled-body-client.go.txt`](instruments/f4-stalled-body-client.go.txt),
  [`instruments/f4-all.sh`](instruments/f4-all.sh) and
  [`instruments/f4-read-timeout-5s.toml`](instruments/f4-read-timeout-5s.toml).
- **F4 results:** [`raw/f4-stalled-body.txt`](raw/f4-stalled-body.txt).

  | `read_timeout` | Effect |
  | --- | --- |
  | 0 (default) | Stalls last until the client gives up on H1, H2 and H3 |
  | 5 s | Ends H1 and H2 stalls *and* legitimate slow uploads at 5 s; H3 ignores it |

  Siblings on the same connection were never affected.

- **F4b instrument:** [`instruments/f4b-h3-stream-deadline.go.txt`](instruments/f4b-h3-stream-deadline.go.txt).
- **F4b results:** [`raw/f4b-h3-stream-deadline.txt`](raw/f4b-h3-stream-deadline.txt).
  - quic-go's `http3` writer accepts a per-stream read deadline through `http.ResponseController`.
  - A stalled stream receives 408 at 2 s while its sibling completes in 1 ms.
  - Whether Jul's wrapper chain unwraps to that writer is **unverified**.

## Limits

- **Platform.** Results come from one arm64 WSL2 host. Envelope runs were single or double
  repetitions, and the c1 rate varied 2.3–4.6/s between runs.
- **Scope.** The scratch slot patch held its slot during body arrival, did not charge bytes and was
  not a design candidate.
- **Calibration.** The F1 and F1b constants are specific to Coraza v3.8.1 with the CRS shipped at
  `858590dc`.
- **Coverage.** None of this measures plugin-plus-WAF combinations in the envelope, multiple
  listeners, reload overlap or real H3 load. Those remain implementation acceptance work.

[`SHA256SUMS`](SHA256SUMS) covers every file in this directory except itself.
