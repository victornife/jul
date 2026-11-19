# Guest allocation diagnostic and recovery verification

This is a bounded synthetic diagnostic, not a new performance or physical-memory
qualification. Host: Linux/amd64, Go 1.27.4, Jul's normal engine selection. Targets:
main `155b6d94dfde580a9332c3a985c8ce156563fd20` and candidate
`827be4f39a2167dfd1e50e05ac2da192bb215306`.

The rebuilt WASI guest SHA-256 is
`27b016a775020dca6cea66fdb98a836d1afbf056b2ac9883c142e4969c41034a`,
identical to the earlier Linux/arm64 p1 assessment artifact. Both targets used
the same instrument and guest. A temporary first-party Go overlay added only
`WithStderr(os.Stderr)` to module configuration; upstream modules and production
logging were not changed. The archived instrument was temporarily copied to
`internal/plugins/closeout_diagnostic_test.go` and removed after execution.

## Results

| Workload | Main | Candidate |
| --- | --- | --- |
| One guest, 768 KiB request bodies, 16 MiB ceiling, default recycling | Guest OOM on call 322 | Guest OOM on call 322 |
| Same, 32 MiB ceiling | Guest OOM on call 333 | Guest OOM on call 333 |
| Same, 16 MiB ceiling and `max_invocations = 16` | 1000 successful calls | 1000 successful calls |
| Two request-only plugins, one instance each, 16 MiB each, default recycling, 1000 HTTP requests | 994 HTTP 200, 6 HTTP 500; origin called 994 times | 994 HTTP 200, 6 HTTP 500; origin called 994 times |
| Same two-plugin chain, `max_invocations = 16` | 1000 HTTP 200; origin called 1000 times | 1000 HTTP 200; origin called 1000 times |

Guest stderr identifies `fatal error: out of memory`, with allocation through
`runtime.(*mcache).allocLarge`, `runtime.growslice`, SDK `readInto` and
`Request.Body`. Wazero returns `wasm error: unreachable`; Jul's public-error
classifier deliberately reports `unknown` rather than parsing internal error
strings or exposing guest/request data. This is guest allocator exhaustion,
not a process OOM kill, admission rejection, or the request-body timeout.

The same causal mechanism and containment are reproduced on current main and
candidate, including the 0.6% two-plugin HTTP failure rate. This explains a
reproducible source of the p1-shaped 500s and supplies a tested mitigation.
The historical arm64 run captured only the bounded reason, so it cannot prove
that every original 500 had this cause. Its original results remain intact.
We do not claim a Go allocator leak, that physical guest memory must grow on
every call, or that 16 is an appropriate universal recycle interval. No global
default, SDK or upstream dependency was changed.

The test process's PASS means the diagnostic completed and the recycling
controls passed; it does not turn the deliberately captured guest failures
into successful requests. Full stdout/stderr is in [main.txt](main.txt) and
[candidate.txt](candidate.txt).

## Reproduction

With the pinned Go toolchain on PATH, run the driver against clean checkouts of
each recorded target (the output directories must not exist):

```sh
python3 docs/reviews/evidence/2026-11-19-721-guest-diagnostic/reproduce.py /path/to/main /tmp/721-main-guest
python3 docs/reviews/evidence/2026-11-19-721-guest-diagnostic/reproduce.py /path/to/candidate /tmp/721-candidate-guest
```

The main checkout need not contain this evidence directory; invoke the driver
from the candidate checkout and pass main's path. The driver builds the guest,
records revision/toolchain/artifact metadata, uses only synthetic input, and
removes its temporary test source. The captured runs used the equivalent
commands with the archived instrument, separate stderr overlays, `GOMAXPROCS=2`,
`JUL_CLOSEOUT_GUEST` pointing to the artifact, and the same test selection.

## Accounting and performance scope

The focused race tests separately verify the 64-to-63 admission transition,
module-close failure followed by successful runtime cleanup, and permanently
retained runtime-close failures. The added overshoot regression verifies that
65-to-64 stays blocked and 64-to-63 reopens the gate. Ordinary byte and local
instance limits still apply; this does not resurrect a retired plugin.

No new throughput claim is made. The historical C=16 H1 uninspected median
fell 33.6%, with candidate spread 21.7%. The C=8 control medians were
4315.9/4333.6 requests/s (about +0.4% candidate), but spreads were 36.7%/41.2%.
These are different concurrency cells: the control does not cancel the C=16
result, and shared-host variability does not prove its cause. A concurrency-
dependent regression remains possible. Performance remains unqualified and
accepted under the reduced contract; all [original runs](../2026-11-19-721-current-main/README.md)
and failures are preserved. The guest OOM finding does not explain the
uninspected-path performance gap.
