# Jul patch record

This source replacement is based on `github.com/corazawaf/coraza/v3` v3.8.1,
upstream commit `9f8521398d1ff023b958fad0b944cac265763866`. The required module
version remains pinned in the root `go.mod`; this file records the local delta.

## Local change

Jul adds a transaction-scoped `TemporaryFileProvider` interface and propagates
it to both request-body spill and multipart upload creation/removal. This lets
Jul reserve logical bytes and file slots before writes and retain storage
failures even when Coraza's parser reports them indirectly.

## Update and removal

When updating Coraza, rebase this narrow provider seam onto the new pinned
upstream source, then run the provider-focused Coraza test and Jul's governed
WAF spool tests. Remove the replacement only when upstream exposes a public
transaction-scoped provider covering both spill and multipart paths, with
observable create/write/remove failures and equivalent regression coverage.

Focused checks:

```sh
cd third_party/coraza
GOWORK=off go test ./internal/corazawaf -run '^TestBodyBufferUsesTemporaryFileProviderForSpillAndCleanup$' -count=1
cd ../..
go test -tags waf ./internal/waf -run '^TestGovernedWAF(SpoolQuotaRefusesBeforeOriginDispatch|ObserveSpoolQuotaContinuesAndRecordsWouldReject|MultipartFileQuotaRefusesBeforeOriginDispatch|MultipartCreateFailureRefusesBeforeOriginDispatch)$' -count=1
```
