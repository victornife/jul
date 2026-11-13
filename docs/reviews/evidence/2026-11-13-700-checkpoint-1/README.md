# #700 checkpoint 1: bounded feasibility probes

These are **design probes**, not an implementation or acceptance campaign. They exercise pinned
Coraza v3.8.1 and wazero v1.12.0 directly on 2026-11-13. The repository base was
`c8bba7f2c1b6c312aa215f8576014a513710775b`; the instruments are new on top of that base and
`go.mod`/`go.sum` were unchanged. The executions and build identity are in
[`manifest.json`](manifest.json).

## Reproduce

Run from the repository root with Go 1.27.4. `JUL_PROBE_TMP` is an existing private directory with
no spaces in its path. The probe creates a unique child there, uses only synthetic payloads, and
removes the files and directories it creates on success. On failure it reports the child path for
inspection and targeted cleanup.

```sh
probe_tmp=$(mktemp -d /tmp/jul-700-probe.XXXXXX)
CGO_ENABLED=0 GOMAXPROCS=2 JUL_PROBE_TMP="$probe_tmp" \
  go run ./docs/reviews/evidence/2026-11-13-700-checkpoint-1/instruments/boundaries.go

CGO_ENABLED=0 go build -trimpath -buildvcs=false -o "$probe_tmp/jul-700-boundaries" \
  ./docs/reviews/evidence/2026-11-13-700-checkpoint-1/instruments/boundaries.go
sha256sum "$probe_tmp/jul-700-boundaries"
GOMAXPROCS=2 JUL_PROBE_TMP="$probe_tmp" "$probe_tmp/jul-700-boundaries"
CGO_ENABLED=0 JUL_PROBE_TMP="$probe_tmp" \
  go run ./docs/reviews/evidence/2026-11-13-700-checkpoint-1/instruments/ownership.go
rm "$probe_tmp/jul-700-boundaries"
rmdir "$probe_tmp"
```

The initial executions produced [`raw-results.jsonl`](raw-results.jsonl) and
[`raw-built-results.jsonl`](raw-built-results.jsonl). A subsequent verification of the private
directory recipe produced [`raw-reproduction-go-run.jsonl`](raw-reproduction-go-run.jsonl) and
[`raw-reproduction-built.jsonl`](raw-reproduction-built.jsonl). The ownership probe produced
[`raw-ownership-results.jsonl`](raw-ownership-results.jsonl). Run that probe as an unprivileged
user on a Unix-like platform supporting directory mode bits; it deliberately removes write
permission after Coraza's own startup check. The compiled boundary-probe binary is identified in
the manifest but is not committed. Runtime allocation measurements are expected to vary.
`SHA256SUMS` covers the manifest, this README, all results and both exact instruments, but not
itself:

```sh
(cd docs/reviews/evidence/2026-11-13-700-checkpoint-1 && sha256sum -c SHA256SUMS)
```

## Results

| Probe | Observed in the recorded executions | What it establishes |
| --- | --- | --- |
| Multipart spill | A 262,308-byte request with a 262,144-byte file had one 262,308-byte `body` file after the write and, after parsing, that file **plus** a 262,144-byte multipart file. Both were absent after transaction close. | Request-byte counting alone misses the simultaneous copies. The write is split at 64 KiB, below the 128 KiB in-memory limit, so spill also copies the already buffered prefix. |
| Empty multipart files | A 4,371-byte request created 32 zero-byte multipart files; all were absent after close. | A byte limit alone cannot bound the number of created files. |
| `SecTmpDir` | The explicitly configured alternative directory stayed empty; files appeared under the process temporary directory. | For this pinned engine the directive is not a usable interception or destination boundary. |
| Phase-5 audit | With a 262,144-byte request, JSON serial audit parts `ABFHZ` produced 707–713 bytes; `ABCFHZ` included the body and produced 262,851–262,858 bytes. Across three samples per execution, `ProcessLogging` allocated 1,975,968–2,248,216 cumulative bytes with part C; the original two executions were 2,246,304–2,248,216. | Body construction and formatting allocate before the sink receives the record. These variable `TotalAlloc` deltas are **not** peak live heap or a universal multiplier. |
| Parser I/O failure | A valid multipart upload with a later `danger=attack` field is denied with 403 when the upload directory is writable. If directory write permission is revoked after Coraza's startup check, `ProcessRequestBody` returns no error or interruption, sets `REQBODY_ERROR=1`, and the phase-2 rule on the later field does not run. | A body-processor marker or rule is not a fail-closed signal for typed spool errors. Jul needs an explicit engine-to-owner error path before forwarding. The fault is synthetic and uses only temporary private directories. |
| Static spool path | A synthetic file created with Coraza's `body*` prefix below an `os.Root` static root is returned by Jul's static handler with HTTP 200. | A private spool root must be provably disjoint from every static root, including aliases and generation overlap. |
| Audit record alias | A test audit writer retains the `AuditLog`; after `tx.Close()` and transaction reuse, the earlier audit object exposes a later transaction's argument. | Holding a request reservation until `Write` returns is insufficient for a writer that retains the transaction graph. Writers need an immutable detached snapshot or must be unsupported. |
| Guest memory | Four modules each started at one 64 KiB page, grew to 256 pages (16 MiB), and rejected one further page. Post-GC Go `HeapAlloc` increased by about 64 MiB with all four live. | The pinned per-instance ceiling can be reserved before instantiation; the heap sample is not RSS and does not determine an aggregate default. |

The probe checks each expected result and fails rather than reporting success for an unexpected
file, body, growth result or cleanup error. The audit inputs contain only repeated synthetic `x`
bytes; neither the temporary audit logs nor the generated binary are retained.

## Limits

This was `go1.27.4 darwin/arm64`, `CGO_ENABLED=0`, `GOMAXPROCS=2` on a shared macOS host. It did
not run in the Linux 2-vCPU/768-MiB/no-swap envelope, does not measure cgroup peak or concurrency,
and does not test a Jul runtime with a new governor. The three audit samples in each execution
characterize this one synthetic policy, not production-rule diversity. Existing I2
[plan-v3 evidence](../2026-11-12-700-i2/README.md) provides separate, historical sizing input;
its p1 and p2 cells have one repetition each, and p1 contains guest `500` findings. Neither that
record nor these probes establishes a production default or A1–A22 qualification.
