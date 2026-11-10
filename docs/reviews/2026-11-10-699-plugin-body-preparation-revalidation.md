# #699 plugin request-body preparation — bounded revalidation

- **Date:** 2026-11-10
- **Owner:** [#699](https://github.com/victornife/jul/issues/699) (a slow but
  valid upload consumed a body-reading plugin's guest-call timeout while holding
  an instance). Follow-up to the
  [#695 assessment](2026-11-09-695-waf-wasm-resource-assessment.md) and the
  [#697/#698 revalidation](2026-11-10-697-698-waf-fix-revalidation.md). Their
  measurements, thresholds and dispositions are unchanged and remain the
  historical record. The original single-client reproducer is
  [`plugin-slow-upload-repro.txt`](evidence/2026-11-09-695/final/plugin-slow-upload-repro.txt).
- **Comparison (unfixed) source:** `main` at
  `e1f37cf7bffa5417b249a3ee877dd0a7e8ac2ae2` (the #702 merge, which contains the
  #697/#698 fixes), `make build-full`, `jul` sha256 `59ab4111…cac2`.
- **Fixed source:** branch `fix/699-plugin-body-preparation` at
  `96d447cb2f0a17ab686da83f692bfff71eb16ec4`, `make build-full`, `jul` sha256
  `da024f1c…f987`. An earlier envelope run used the first fix commit
  `d844a514` (`jul` sha256 `901edf13…c6c0`). It is retained and listed
  separately below. The only difference is the review fixes: pre-data
  allocation capped at 32 KiB, and read-deadline forwarding through the WAF.
- **Guest:** `examples/plugins/inspect-load` built per the #695 tooling,
  `inspect-load.wasm` sha256 `2526ed82…8127`, identical to the assessed guest.
- **Evidence:** [`evidence/2026-11-10-699/`](evidence/2026-11-10-699/) with
  `SHA256SUMS`.

This is a bounded, source-bound check of one lifecycle repair. It is not an
aggregate recertification, capacity claim, soak or release qualification. The
#700 aggregate findings are reported, not fixed.

## 1. Single-client reproducer

[`repro/repro.sh`](evidence/2026-11-10-699/repro/repro.sh) uses the archived s8
configuration (two body-reading `inspect-load` middleware plugins, the default
100 ms timeout, `max_request_body` 1 MiB). It runs without a cgroup and with one
client. It sends a 64,330-byte JSON body three ways: at full speed,
`--limit-rate 16k`, and `--limit-rate 1M`. The origin and both guests echo the
body length they observed.

| Build | full speed | 16 KiB/s | 1 MiB/s | Mid-upload body reader | `inspect-a` guest time (3 calls) | panics |
| --- | --- | --- | --- | --- | --- | --- |
| unfixed | `200`, all lengths 64330 | **`500` at 3.005 s**, origin not called | `200` | 1 goroutine inside a guest call (`wazero` → `read_request_body`) | 3.008 s | 1 |
| fixed | `200`, all lengths 64330 | **`200` at 3.006 s**, origin and both guests 64330 | `200` | 1 goroutine in `readBounded` ← `prepareRequestBody` ← `invoke`, with no guest frames | 0.004 s | 0 |

Upload arrival is now outside the guest call. The guest reads a prepared body.
The unfixed 3.0 s guest time exceeded the 100 ms timeout because a blocked host
read is not interrupted by the guest deadline.

Raw output and stack excerpts: [`repro/pre/`](evidence/2026-11-10-699/repro/pre/)
and [`repro/post/`](evidence/2026-11-10-699/repro/post/).

`jul_plugin_instances` reads 1 per plugin mid-upload in both builds. That gauge
counts pooled instances, not ownership. Ownership is established by the stack
(no guest frames while the body is arriving) and by the unit regressions. Those
regressions observe acquisition directly, and include another request using
the same `max_instances = 1` pool during a stalled upload.

## 2. Bounded envelope subset

**Method.** The archived #695 harness and guest were restored as described in
[`tooling/README.md`](evidence/2026-11-09-695/tooling/README.md). The same host,
envelope (`CPUQuota=200%`, `MemoryMax=768M`, swap 0, CPUs 0–1, `GOMAXPROCS=2`),
CPU pinning, origin and 20 s windows were used, one run per cell. The builds
ran one after the other in the same session.

**Harness deviation.** A recorded additive patch
([`harness-revalidation.patch`](evidence/2026-11-10-699/harness-revalidation.patch))
adds a `-variants` filter and three scenarios:

- `r1`: 16 clients uploading the s8 body at ~16 KiB/s, plus fast GET probes;
- `r2`: fast bodiless GETs, 16 concurrent;
- `r3`: fast 16 KiB POSTs, 16 concurrent.

Original scenarios and thresholds are unchanged. Harness binary
`990ee143…39e2`; patched source `90c1e273…cdfd`.

| Scenario | Unfixed `e1f37cf7` | Fixed `d844a514` | Fixed `96d447cb` |
| --- | --- | --- | --- |
| s8 slow clients, `wasm` | **killed (SIGKILL) at 760 MiB RSS**; slow uploads 0 ok; probes 5 × `200`, 112 × `503`; 66 instances | 384 slow uploads `200` (same as `none`: 384); probes 50,784 `200`; peak 7 instances; cgroup peak 289 MiB | 384 slow `200`; probes 31,975 `200`; peak 8 instances; cgroup peak 266 MiB |
| r1 16 slow uploads, `wasm` | 64 × **`500`**, 0 ok; probes 55,306 | 64/64 `200`; probes 56,081 | 64/64 `200`; probes 55,033 |
| r2 bodiless, `none` | 12,173 rps | 12,336 rps | 12,522 rps |
| r2 bodiless, `wasm` | 4,121 rps, p99 14 ms | 3,774 rps, p99 15 ms | 3,234 rps, p99 18 ms |
| r3 16 KiB body, `wasm` | 966 rps, p99 42 ms | 1,056 rps, p99 38 ms | 1,031 rps, p99 38 ms |

Every counted success was a verified `200` with an exact body check by the
harness.

The `none` probe throughput in the final s8 run was 4,511 rps, against
8,672–8,790 in the earlier runs. Host load varied between sessions, so the s8
probe counts are not a throughput comparison.

**r2 variance.** The bodiless path is unchanged in principle: a server request
without a body carries `http.NoBody`, which preparation returns immediately.
Because single runs differed, r2 `wasm` was run three more times, alternating
builds ([`runs/r2-alternating/`](evidence/2026-11-10-699/runs/r2-alternating/)):

| Build | Runs (rps) | p99 |
| --- | --- | --- |
| unfixed | 4,377 / 3,640 / 3,784 | 13–16 ms |
| fixed `96d447cb` | 4,099 / 4,254 / 4,079 | 14–15 ms |

Ranges overlap. No bodiless-path regression is attributable to the change on
this host.

**Retained preparation memory.** A prepared body is retained from preparation
until the request ends. It is bounded per request by that plugin's
`max_request_body` and by the bytes actually received. Before data arrives,
at most 32 KiB is allocated. In s8, about 128 slow clients each hold at most
one 64 KiB body (≈ 8 MiB). The observed cgroup peaks of 266–289 MiB are
dominated by instances and the Go heap, not by preparation. The envelope does
not instrument prepared bytes directly.

## 3. Remaining limitations (#700)

- The aggregate of prepared bodies is now
  concurrent body-reading requests × received bytes (each ≤ `max_request_body`).
  It is no longer limited by `max_instances`. No process-wide byte budget or
  admission bound exists.
- Waiters at instance acquisition remain unbounded in count. Each is bounded in
  time by the existing acquisition timeout.
- HTTP/3 does not apply `read_timeout`. The default `read_timeout = 0` means a
  stalled upload is bounded only by client disconnect, idle handling or
  retirement.
- The WAF many-argument OOM, H2/H3 fan-out and cancellation findings from #695
  and #697/#698 are unchanged.
- A full aggregate recertification belongs after #700.
