# #700 I1 base-charge measurement

This evidence measures retained Go heap for a live inspection lease, its base
charge record, and the request context value that carries the lease. It does
not measure request bodies, plugin or Coraza state, protocol buffers,
instrumentation wrappers, the rest of the request context, pool retention,
RSS, or future request-path integration.

## Reproduction

Run from the repository root with Go 1.27 or later:

```sh
go run ./docs/reviews/evidence/2026-11-10-700-i1-base-charge/instruments/base-charge.go
```

The archived run used `go1.27.3 linux/arm64`. The instrument records `runtime`
toolchain and OS/architecture values in the first JSONL row. For each of 0,
256, 1,024 and 4,096 live leases, it runs seven repetitions. Each repetition
creates a fresh governor, allocates the harness retention slices before the
baseline, runs `runtime.GC` and `debug.FreeOSMemory`, records `HeapAlloc`,
acquires and retains every lease and its returned context, repeats the GC
procedure, and records `HeapAlloc` again. The retained slices are kept alive
through the second sample. The harness slices themselves are excluded from the
measured delta.

Raw per-repetition measurements are in
[`raw-results.jsonl`](raw-results.jsonl). Median deltas were:

| Live leases | Median retained heap delta | Median delta per lease |
|---:|---:|---:|
| 0 | 112 B | — |
| 256 | 34,816 B | 136 B |
| 1,024 | 139,264 B | 136 B |
| 4,096 | 557,056 B | 136 B |

The 16 KiB base reservation is about 120 times the measured median lease/context
scaffolding on this toolchain and platform. This supports retaining 16 KiB as a
conservative accounting policy for the measured objects; it is not evidence
that all future per-request overhead is bounded by 16 KiB, nor that the
reservation predicts total process memory.

## Limitations

This is one architecture and Go toolchain. `HeapAlloc` is a runtime statistic,
not RSS or a guarantee about allocator fragmentation or GC timing. The
zero-lease control shows fixed runtime noise; the larger sample sizes
consistently show about a 136 B median incremental slope.
Future I2–I4 work must measure and account for its own wrappers, buffers and
protocol-specific allocations. This is not the #700 full qualification matrix
or a soak.

`SHA256SUMS` records the checksums of the instrument and raw results.
