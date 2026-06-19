# Example WebAssembly plugins

Runnable [Jul.IA WebAssembly plugins](../../docs/plugins.md) and the guest SDK
they build on. This directory is its own Go module (`juliaplugins`) so the main
server module never compiles the `wasip1`-only guest code during `go build ./...`.

To run these in a server you need a binary built with the plugins runtime:

```bash
go build -tags wasmplugins -o jul ./cmd/jul
```

## Layout

| Path | What it is |
| ---- | ---------- |
| [`sdk/`](sdk) | The guest SDK (package `sdk`) wrapping the `jul-abi/v1` host ABI |
| [`header-inject/`](header-inject) | Middleware: adds an `X-Plugin` response header, then `Continue` |
| [`request-block/`](request-block) | Middleware: `403` + body when `X-Block: 1`, else `Continue` |
| [`kv-counter/`](kv-counter) | Middleware: counts requests in the KV store, reports `X-Count` (needs `kv = true`) |
| [`testguest-panic/`](testguest-panic) | Panics on invocation — used by the panic-isolation test |
| [`testguest-loop/`](testguest-loop) | Infinite loop — used by the timeout-isolation test |

## Build

Plugins are built for the WASI preview-1 target in `c-shared` mode (so the host
runs guest `init` functions via `_initialize` before invoking the export):

```bash
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o header-inject.wasm ./header-inject
```

Build every example into `testdata/plugins/` (where the test suite reads them)
with the bundled script:

```bash
./build.sh          # Linux/macOS
```

```powershell
./build.ps1         # Windows
```

## Try it

Build a plugins-enabled server, point it at a config that loads these modules,
and send a request:

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
./jul run -c jul.toml &

curl -i http://127.0.0.1:8080/            # X-Plugin and X-Count headers present
curl -i http://127.0.0.1:8080/ -H 'X-Block: 1'   # passes (kv-counter middleware)
curl -i http://127.0.0.1:8080/blocked -H 'X-Block: 1'   # 403 from request-block
```

See [docs/plugins.md](../../docs/plugins.md) for the full authoring guide and the
`jul-abi/v1` ABI reference.
