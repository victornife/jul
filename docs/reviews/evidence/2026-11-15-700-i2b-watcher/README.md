# I2b wazero watcher ownership probe

On 2026-11-15, Go 1.27.4 on macOS arm64, this local I2b worktree reproduced two
premature guest-capacity refunds with the pinned wazero v1.12.0. This is diagnostic
evidence, not a workload measurement, qualification result, or upstream fix.
The source hashes below identify the pre-fix prototype observed by the probe.
The follow-up implementation now retains capacity when a failed start returns
no module and close-on-context-done is enabled; its focused regression passes.
That fix does not address the separate normal-retirement finding below.

The copied dependency is instrumented only with a scheduling barrier immediately
before the watcher's original `select`. The original cancellation branch can still
use the module after that point; the probe does not add a use of the module after
the barrier. The real runtime and module close behavior remain unchanged. The
second case additionally uses a weak pointer and forced GC to show that actual
guest memory remains reachable after close. Its watcher only signals readiness:
it does not read guest memory concurrently with initialization.

| Case | Expected ownership | Observed in the pre-fix prototype |
| --- | --- | --- |
| Trapping start, no module returned | Keep the 65,536-byte reservation while the watcher can reach the module | Reservation was 0 while the watcher held a module with 65,536 bytes of linear memory. |
| Normal real-plugin retirement | Keep the 16 MiB ceiling reservation while the watcher can reach the module | Reservation was 0 while 2,424,832 bytes remained reachable after GC. |

The original uninstrumented `CloseModuleOnCanceledOrTimeout` closes a channel
but does not join the watcher; `Module.Close` does not clear the linear memory
buffer. No supported Jul-side way to wait for this watcher has been established.
Normal I2b retirement remains blocked until an upstream or other supported
solution proves the required last-alias ownership and cancellation behavior.

## Source identity

The measured tree was dirty on top of `41820205ee956dd6ff1e75435a919c5c402d5840`;
it had not yet incorporated main `de2a4433f99a6121ae33ccf30f7b61806c67299f`.
The following SHA-256 values identify the relevant uninstrumented inputs:

| Input | SHA-256 |
| --- | --- |
| `internal/plugins/runtime.go` | `7458af6b489986fd9c92cde5ae6e1901952a8d6f25cf08ed6f6f9242cbf81738` |
| `internal/plugins/inspection_guest_test.go` | `eea754a200ac353c50a9272c0edd714a0bd68ef2ef94d8df738ce4344832d4ad` |
| `internal/inspection/guest.go` | `260d3f8a0e6785124629b44a9a236d26d144b14decad69242882bc363af587a3` |
| `go.mod` | `17cf97597738706ef4653c573f99a322c750970ce6ae6128f1f8ceb98d354715` |
| `go.sum` | `56e780935168239cbc865cd069b82158439445266fb4fdded401144dce0e5600` |
| wazero v1.12.0 `internal/wasm/module_instance.go` | `4d452fedd22ac86aabaef33545a85fbb52f815f4eec9050efab8fb458fc28c4d` |

## Reproduce

From the measured I2b source tree with wazero v1.12.0 already present in the Go
module cache, run:

```sh
bash docs/reviews/evidence/2026-11-15-700-i2b-watcher/reproduce.sh
PROBE_RACE=1 bash docs/reviews/evidence/2026-11-15-700-i2b-watcher/reproduce.sh
```

The script copies the pinned dependency into a uniquely named temporary directory,
applies `wazero-before-select.patch`, overlays the two probe tests, and checks for
both expected `go test` failures. It does not change repository files or the Go
module cache. The temporary directory is retained and printed for inspection;
remove that specific directory after use. A fixed implementation should make the
script report that the historical blocker needs reassessment, not claim a new
failure.

`result.txt` is the recorded output from this worktree before publication; final
source identity and subsequent results are tracked in the programme ledger.
`race-result.txt` records the same two failing assertions under the race
detector; no data race was reported in that run.
