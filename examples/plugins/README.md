# Example WebAssembly plugins

Runnable [Jul.IA WebAssembly plugins](../../docs/plugins.md) and the guest SDK
they build on. This directory is its own Go module (`juliaplugins`) so the main
server module never compiles the `wasip1`-only guest code during `go build ./...`.

Published [v2.1.0](https://github.com/victornife/jul/releases/tag/v2.1.0)
supports the opt-in `jul-abi/v2` examples below in its `full` artifacts (or a
source build with `wasmplugins`). The v2 response phase remains Beta;
`jul-abi/v1` remains the default and v1 guests remain supported. This README
update is post-tag, not a replacement for the packaged candidate-time README. See the
[status matrix](../../docs/status.md) and [ABI contract](../../docs/abi.md).

To run these in a server you need a binary built with the plugins runtime:

```bash
go build -tags wasmplugins -o jul ./cmd/jul
```

## Layout

| Path | What it is |
| ---- | ---------- |
| [`sdk/`](sdk) | The v1 guest SDK (package `sdk`) wrapping the `jul-abi/v1` host ABI |
| [`sdk/v2/`](sdk/v2) | The v2 guest SDK (import `sdk "juliaplugins/sdk/v2"`) adding the bounded response phase |
| [`header-inject/`](header-inject) | **v1 request-only example.** Middleware: adds an `X-Plugin` response header, then `Continue` |
| [`v2-status-header/`](v2-status-header) | **v2 headers-only response example.** Labels the upstream status class, strips `Server`/`X-Powered-By`, marks 5xx `no-store` (needs `abi = "jul-abi/v2"`) |
| [`v2-redact/`](v2-redact) | **v2 bounded body transform.** Redacts configured strings in text/JSON responses; optional fail-closed reject (needs `abi = "jul-abi/v2"`, `memory_limit` ≈ 2× `max_response_body`) |
| [`testguest-v2/`](testguest-v2) | The v2 conformance guest driven by the host test suite |
| [`request-block/`](request-block) | Middleware: `403` + body when `X-Block: 1`, else `Continue` |
| [`kv-counter/`](kv-counter) | Middleware: counts requests in the KV store, reports `X-Count` (needs `kv = true`) |
| [`egress-check/`](egress-check) | Middleware: guarded outbound `Fetch` to an allow-listed host, reports `X-Egress-Status` (needs `fetch = true`) |
| [`testguest-panic/`](testguest-panic) | Panics on invocation — used by the panic-isolation test |
| [`testguest-loop/`](testguest-loop) | Infinite loop — used by the timeout-isolation test |

## Build

Plugins are built for the WASI preview-1 target in `c-shared` mode (so the host
runs guest `init` functions via `_initialize` before invoking the export):

```bash
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o header-inject.wasm ./header-inject
```

Refresh the committed fixtures in `testdata/plugins/` (the v2 guests and the
current-toolchain v1 guest `v1-current-header-inject.wasm`; the historical v1
fixtures are never overwritten), or build every example into a directory, with
the bundled script:

```bash
./build.sh                 # Linux/macOS: refresh the committed fixtures
./build.sh /tmp/plugins all # every example, own names (what CI runs)
```

```powershell
./build.ps1         # Windows
```

## Try it

Build a plugins-enabled server, point it at a config that loads these modules,
and send a request. Save the following as `jul-plugins.toml` in the repository
root; the plugin paths are relative to that working directory:

```toml
# jul.toml
[plugins.header-inject]
path = "./testdata/plugins/header-inject.wasm"

[plugins.kv-counter]
path = "./testdata/plugins/kv-counter.wasm"
kv = true

[plugins.request-block]
path = "./testdata/plugins/request-block.wasm"
type = "handler"

[[servers]]
listen = "127.0.0.1:8080"
plugins = ["header-inject"]

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  root = "./testdata/www"
  plugins = ["kv-counter"]

  [[servers.locations]]
  match = { type = "exact", path = "/blocked" }
  plugin = "request-block"
```

```bash
go build -tags wasmplugins -o jul ./cmd/jul
./jul check -config jul-plugins.toml
./jul -config jul-plugins.toml &

curl -i http://127.0.0.1:8080/            # X-Plugin and X-Count headers present
curl -i http://127.0.0.1:8080/ -H 'X-Block: 1'   # passes (kv-counter middleware)
curl -i http://127.0.0.1:8080/blocked -H 'X-Block: 1'   # 403 from request-block
```

See [docs/plugins.md](../../docs/plugins.md) for the full authoring guide and
[docs/abi.md](../../docs/abi.md) for which ABI to use and the v1/v2 reference.

## Signed media/download links

[The signed-url recipe](signed-url/README.md) includes a runnable configuration,
Go issuer, rotated HMAC keys and GET/HEAD expiry validation. Both parent build
scripts include it in `all` mode. Its native policy tests and actual compiled
WASM guest tests run in CI; nginx `secure_link` formats remain a manual migration.
