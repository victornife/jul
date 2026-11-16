# I2b public-API close probe

This is a new, separately executed probe of wazero v1.12.0, not a reconstruction
of the earlier deleted scratch program. It uses public APIs only, with no
dependency changes, wazero patches, internal hooks, `unsafe`, or finalizers.
The tiny guest imports a normal host function solely to signal that execution
has started and to pass its `api.Module` to the caller.

## Recorded run

[`result.txt`](result.txt) records the output from:

```sh
bash docs/reviews/evidence/2026-11-16-700-i2b-public-close/reproduce.sh > docs/reviews/evidence/2026-11-16-700-i2b-public-close/result.txt 2>&1
```

The checkout prefix in the archived output and `initial-failure.txt` is
redacted as `<repo>`; input hashes and all other output are unchanged.

The script records its build and runner commands, Go and dependency versions,
host, Git head, versioned-worktree status and input hashes. The measured Jul head
was `81337764ecff8e8637900508c8c29bb2d8273ac4`; the versioned worktree was clean.
The new evidence files were untracked and are identified by their hashes in the
output. The run used Go 1.27.4 on macOS arm64 with the existing wazero v1.12.0.
`GOPROXY=off` prevented dependency downloads. All 20 isolated cases completed
without an external timeout and the runner reported `RESULT: PASS`.

The earlier setup attempt in [`initial-failure.txt`](initial-failure.txt) used
`GOSUMDB=off`, which prevented verification of the selected Go toolchain; it
failed before building or running any case. The archived script removes that
setting. The initial script version is not archived and the failure is not
part of the measured results.

| Case | Both interpreter and compiler |
| --- | --- |
| `WithCloseOnContextDone(false)`, direct `Module.Close` | Close returned nil, but neither an exported infinite loop nor a Wasm start-section loop returned within 750 ms. |
| `WithCloseOnContextDone(false)`, cancellation followed by `Module.Close` in another goroutine | Same bounded observation for both loops. |
| `WithCloseOnContextDone(true)`, direct `Module.Close` | Both loops returned with an exit error. |
| `WithCloseOnContextDone(true)`, cancellation followed by `Module.Close` | Both loops returned with a cancellation error; wazero's own cancellation watcher may also have closed the module. |
| Disabled, start-section trap / `_initialize` trap | Instantiation returned an error with a nil / non-nil module respectively. A deliberately retained host memory view remained reachable after `Close` and forced GC. |

These are bounded observations on one host. The 750 ms result is **not** proof
that a loop can never stop. The direct-close enabled control does not prove that
every Wasm workload or host call can be interrupted. The `ready` import lets
this particular start function expose its module; an arbitrary guest need not
call any host function, so that technique does not supply a module during every
failed or stuck start. The `true` cancellation cases use wazero's normal internal
watcher and do not establish which close won the race.

The alias test intentionally retains a `Memory.Read` view and uses
`runtime.KeepAlive`; it shows that a public close does not invalidate a host-held
slice. It does **not** establish that Jul actually retains this alias, nor
whether any particular engine alias survives after every start failure.
Neither it nor a successful `Close` proves last-alias teardown, eventual
drainage, total RSS protection, original A20 acceptance, or interchangeability
of the two cancellation strategies. Guest linear-memory limits exclude other
host allocations. No workload, durability, performance or integrated
qualification claim is made.

## Reproduce safely

From the repository root, with the selected Go toolchain and pinned module
already cached:

```sh
bash docs/reviews/evidence/2026-11-16-700-i2b-public-close/reproduce.sh
```

The script exclusively creates `.work` within this evidence directory with
mode 0700 and refuses an existing path. It leaves the fixture for inspection on
success or failure; deal with it through an approved manual procedure before
repeating. It does not recursively remove files or use recursive `chmod`.
Go may clean its own build temporary directories within `.work`. Another process
running as the same user can still interfere with the fixture.

`runner.py` starts one direct child process per case with a five-second
`subprocess.run` timeout; Python kills and waits for that child on timeout. Each
child additionally bounds barrier/close waits and the 750 ms non-return
observation. A runaway guest therefore runs only in its child process; normal
child exit also terminates its remaining guest goroutines. The build itself
does not have this five-second timeout. The probe does not run Jul or alter its
configuration.

`SHA256SUMS` covers the archived evidence files except itself.
