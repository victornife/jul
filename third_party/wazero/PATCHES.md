# Jul patch record

This source replacement is based on `github.com/tetratelabs/wazero` v1.12.0,
upstream commit `2ab480b55fa408d6b35df97fe32a60d08bd6e201`. The required module
version remains pinned in the root `go.mod`; this file records the local delta.

## Local change

`ModuleInstance.CloseModuleOnCanceledOrTimeout` now returns a cancellation
function that joins the cancellation watcher before returning. Jul can therefore
confirm that the watcher no longer holds a module reference before releasing
the instance's guest-capacity reservation. Cancellation behavior is unchanged;
physical memory reclamation by Go remains asynchronous.

`Store.instantiate` also closes resources acquired by an instance whose
instantiation fails after setup begins. It marks the unregistered instance
closed, closes its system filesystem and any allocator buffer or `CodeCloser`,
and joins cleanup errors with the original instantiation error. It does not
delete from the store because registration has not completed and that could
remove an existing module with the same name.

## Update and removal

When updating wazero, rebase the watcher join onto the new pinned upstream
source and retain both local lifecycle changes. Run the focused watcher-barrier
and failed-instantiation tests plus Jul's plugin lifecycle tests, including the
100,000-call recycling regression. Remove the watcher patch only when upstream
guarantees that cancellation teardown has finished before the module
close/return boundary used by Jul, with equivalent concurrency coverage.
Remove the failed-instantiation patch only when upstream performs equivalent
resource cleanup after setup failures and tests both failed-start cleanup and
preservation of already registered modules.

Focused checks:

```sh
cd third_party/wazero
go test ./internal/wasm -run '^(TestModuleInstance_CloseModuleOnCanceledOrTimeoutWaitsForWatcher|TestStore_Instantiate_Errors)$' -count=1
cd ../..
go test -tags wasmplugins ./internal/plugins -run '^TestGuestCapacityRecyclesAcross100KInvocations$' -count=1
```
