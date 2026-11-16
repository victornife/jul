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

The local conservative alternative retains all reserved guest attempts
until process exit, rather than proving watcher completion. The original
`reproduce-retained.sh` passed both watcher cases on macOS arm64 with Go 1.27.4,
with and without `PROBE_RACE=1`. Those results used the original script, which
removed its fixture recursively. The modified script has **not been run**.
When approved for execution, use
`bash docs/reviews/evidence/2026-11-15-700-i2b-watcher/reproduce-retained.sh`
and repeat with `PROBE_RACE=1` to exercise this same paused watcher against that
alternative. This demonstrates retention for the reproduced interleaving, not
eventual reclamation or original A20 acceptance. The modified script creates
the repository-local `.i2b-watcher-probe` fixture exclusively with mode 0700,
refuses an existing path and leaves the fixture for manual inspection, including
on failure; a second run requires the existing fixture to be dealt with first.
It does not modify the pinned dependency. Go and the tests may clean their own
temporary directories within the fixture. The private directory does not guard
against concurrent interference by other processes running as the same user.

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

The current-code probe was rerun on commit `aa5a8dd3a2aefbf64ca69166339b142fc151f2e8`
after merging #720, with Go 1.27.4 on macOS arm64:

| Input | SHA-256 |
| --- | --- |
| `internal/plugins/runtime.go` | `d9e4b8f8eb5a602ae78519e4c0e66be796d5f44f3e704f4d895e13bcf670ae37` |
| `go.mod` | `08992991f961b2a5c7cfcb0f05574931cb44c180fad3e0c169e43fa039d96f65` |
| `go.sum` | `d5d4aacd98cf11a6dcde4397024beb1f4a3fe340f48d54c15db565e3261faf40` |

## Reproduce

From the current I2b source tree with wazero v1.12.0 already present in the Go
module cache, run:

```sh
bash docs/reviews/evidence/2026-11-15-700-i2b-watcher/reproduce.sh
PROBE_RACE=1 bash docs/reviews/evidence/2026-11-15-700-i2b-watcher/reproduce.sh
```

The script copies the pinned dependency into a uniquely named temporary directory,
applies `wazero-before-select.patch`, overlays the two probe tests, and checks that
the failed-start ticket remains charged while normal retirement still refunds too
early. It does not change repository files or the Go module cache. The temporary
directory is retained and printed for inspection; remove that specific directory
after use. If a fix makes both cases pass, the script deliberately exits non-zero
so the blocker can be reassessed.

`result.txt` and `race-result.txt` preserve the historical pre-fix output, where
both assertions failed. `current-result.txt` and `current-race-result.txt` record
the later probe against the published code: failed-start retention holds, while
normal retirement still fails. No data race was reported in the race-enabled run.
