# I2b experimental watcher join

On 2026-11-16, an isolated copy of wazero v1.12.0 was used to investigate the
[watcher ownership blocker](../2026-11-15-700-i2b-watcher/README.md) in the I2b
prototype. This is diagnostic evidence, not an upstream fix, supported dependency,
qualification result or A20 proof. The code measured was Jul `d0600ed999a91b475e117a683d59ef53635feb5b`;
the worktree also contained the untracked files in this evidence directory. No Jul
production source or dependencies were changed by the probe.

The baseline adds a scheduling barrier immediately before the watcher's original
`select`. With a real plugin, startup returned while that watcher was paused.
The experimental patch makes each function call wait for a completion signal from
its watcher. With that patch, startup remained pending and its 16 MiB reservation
remained charged until the barrier was released. After retirement, a weak pointer
no longer found the guest-memory array after forced GC. Both experimental tests
passed five times with `-race`; the [baseline](baseline.txt) failed as expected
and the [join run](join.txt) passed.

A second instrumentation barrier pauses one guest watcher **after** it signals
completion, without adding a subsequent use of the module. In this experiment a
weak pointer did not find the guest-memory array after retirement and forced GC.
The completion signal occurs before the goroutine actually terminates. The probe,
compiler liveness and one host cannot prove the instance is unreachable in every
case; the 100 ms non-return assertion is bounded scheduling evidence, not a proof
that a permanently blocked call cannot return. It does not cover every startup
failure, teardown alias, cancellation or workload.

Selected existing Jul lifecycle and cancellation tests, wazero cancellation
examples and both-engine close/recursion tests also passed against an earlier
instrumented join on this host. Their results are not a full upstream test or
compatibility review. Failed-start debt drainage, upstream maintenance, supported
integration, alias proof, performance and a finite default remain unresolved.
The join is **not installed or proposed as a production fix**.

## Reproduce

With the pinned dependency already present in the module cache, from the I2b tree:

```sh
bash docs/reviews/evidence/2026-11-16-700-i2b-join/reproduce.sh
```

The script makes a private temporary copy of wazero, adds the instrumentation
and runs the baseline, requiring the specific expected failure. It then applies
the experimental join and runs both overlaid Jul tests five times under `-race`.
It prints and retains its uniquely named scratch directory for inspection; remove
that specific directory after use. Neither the repository nor the module cache
is modified. The script's success does not make the patch suitable for release.

## Source identity

Host: Go 1.27.4, macOS arm64, CGO enabled. Hashes identify the unmodified inputs:

| Input | SHA-256 |
| --- | --- |
| `go.mod` | `08992991f961b2a5c7cfcb0f05574931cb44c180fad3e0c169e43fa039d96f65` |
| `go.sum` | `d5d4aacd98cf11a6dcde4397024beb1f4a3fe340f48d54c15db565e3261faf40` |
| `internal/plugins/runtime.go` | `d9e4b8f8eb5a602ae78519e4c0e66be796d5f44f3e704f4d895e13bcf670ae37` |
| `internal/plugins/inspection_guest_test.go` | `6cd8aa27703b42af3ef2171e25b00f74eff4e811f7bbc090609277a4f83ee8f7` |
| wazero `internal/wasm/module_instance.go` | `4d452fedd22ac86aabaef33545a85fbb52f815f4eec9050efab8fb458fc28c4d` |

`SHA256SUMS` covers the evidence files except itself.
