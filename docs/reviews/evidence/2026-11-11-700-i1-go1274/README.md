# #700 I1 base-charge recheck with Go 1.27.4

This supplementary run repeats the
[I1 lease/context measurement](../2026-11-10-700-i1-base-charge/README.md)
after the Go toolchain security update. The original Go 1.27.3 evidence is
unchanged.

The instrument and inspection implementation were at PR #706 head
`9fe0455d915ae88a66c0049cf1d195bd87cb1b46`; the working tree also contained
the Go 1.27.4 module, Docker, documentation and fixture updates, but no Go
source changes. The run used `go1.27.4 linux/arm64` on 2026-11-11. Raw
measurements, including seven repetitions at each lease count, are in
[`raw-results.jsonl`](raw-results.jsonl).

| Live leases | Median retained heap delta | Median delta per lease |
|---:|---:|---:|
| 0 | 112 B | - |
| 256 | 34,816 B | 136 B |
| 1,024 | 139,264 B | 136 B |
| 4,096 | 557,072 B | approximately 136 B |

The 16 KiB base reservation remains about 120 times the measured median
lease/context scaffolding in this run. This does not bound total request or
process memory: bodies, plugin and Coraza state, protocol buffers, future
wrappers, pool retention and RSS are outside the measurement. It is not a
soak or release qualification.

From the repository root, reproduce the measurement and verify the archived
instrument and result checksums:

```sh
GOTOOLCHAIN=go1.27.4 go run ./docs/reviews/evidence/2026-11-10-700-i1-base-charge/instruments/base-charge.go
(cd docs/reviews/evidence/2026-11-11-700-i1-go1274 && sha256sum -c SHA256SUMS)
```

Individual measurements can vary with allocator and GC timing.
