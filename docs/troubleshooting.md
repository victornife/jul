# Troubleshooting

Common first-run and operational issues, with the fastest path to a fix. For a
full config reference see [configuration.md](configuration.md); to validate a
config before starting use `jul check` / `jul lint`.

## Startup

### `error: no configuration file at "server.toml"`

There is no config file where `jul` looked (default `./server.toml`). Either:

- Start without a file using zero-config mode:
  ```bash
  jul run --serve .              # serve the current directory over HTTP
  jul run --proxy http://:3000   # reverse-proxy a local app
  ```
- Or create a `server.toml` (see [getting-started.md](getting-started.md)) and
  run `jul`, or point at a file with `jul -config /path/to/server.toml`.

### `invalid configuration in server.toml: ...`

The file parsed but failed validation. Run `jul check -config server.toml` for
the full list of errors, or `jul lint` for best-practice warnings. Use
`jul lint -json` for machine-readable output (schema in
[configuration.md](configuration.md#cli-json-output)).

### A path error like `open /srv/www/example: no such file or directory`

The config references a `root`/file path that does not exist on this host. Fix
the `root` in the offending `[[servers.locations]]`, or create the directory.
`jul check` reports which location is at fault.

### `listen tcp :8080: bind: address already in use`

Another process holds the port. Find and stop it, or change `listen` in the
server block. On Windows, `netstat -ano | findstr :8080`; on Linux,
`ss -ltnp | grep :8080`.

### `configuration enables <feature> but this build was compiled without the "<tag>" tag`

You are running a **lean** binary but the config uses a tag-gated feature (WAF,
stream, plugins, gRPC, HTTP/3, …). Build with the feature tag (or the full set):

```bash
go build -tags "waf" -o jul ./cmd/jul
# or the full opt-in set — see docs/index.md "Build tags quick reference"
```

## Admin Console

### Console returns `401 Unauthorized`

The admin API requires the bearer token from `[admin].token`. Send
`Authorization: Bearer <token>`, and keep the admin listener on loopback
(`127.0.0.1:9090`) or behind mTLS. See [console.md](console.md).

### Config edits in the Console are rejected with a version conflict (`409`)

Optimistic concurrency: the config changed since you loaded it (another edit, a
rollback, or a direct file edit + reload). Reload the current config in the
Console and re-apply your change.

## Reloads

### A `SIGHUP`/reload did not apply a change

Some settings are bound at startup and a reload keeps the running value (it logs
a warning): TLS `client_auth`, tracing, and access-log sinks require a restart.
See [reload-semantics.md](reload-semantics.md) for exactly what hot-reloads.

### A bad edit did not take down the server

By design: a reload that fails to load or validate is rejected and the running
configuration is kept. Check the logs for `reload aborted: ...` and fix the file.

## Plugins

### An uploaded plugin was rejected

The upload endpoint enforces a size cap, the WebAssembly magic/version, and a
strict `<name>.wasm` filename. See
[plugins.md](plugins.md#uploading-modules-console-api) for the full rules and the
threat model.

## Plugin Development

For concepts, configuration, and the full ABI reference see [plugins.md](plugins.md).

### My `.wasm` plugin is rejected at startup

**Symptom:** `jul check` or startup logs report `error reading plugin module`, `invalid version`, or `wasm: invalid magic`.

**Common causes:**

1. **Wrong build mode.** Plugins must be built with `-buildmode=c-shared` (reactor mode, so `_initialize` runs guest `init` functions). A normal `go build` produces a command module that the host cannot start.
   ```bash
   # ✅ correct
   GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .
   # ❌ wrong — produces a command, not a reactor
   GOOS=wasip1 GOARCH=wasm go build -o plugin.wasm .
   ```

2. **Wrong GOOS/GOARCH.** The `wasip1` target is required. Any other pair produces a binary that wazero cannot load.

3. **Unsupported Go version.** The host is tested with Go 1.26+. Earlier toolchains may emit unsupported WASM features.

**Verification:**
```bash
# The first 8 bytes of a valid WASM module are the magic/version.
xxd -l 8 plugin.wasm
# expected: 00000000: 0061 736d 0100 0000
```

### My plugin panics or times out

**Symptom:** Requests that hit the plugin return `500`, the log shows `plugin panic` or `plugin deadline exceeded`.

**Common causes:**

1. **Guest panic.** A nil dereference, out-of-bounds slice access, or `panic()` in the guest is caught by the host and converted to a `500`. The request is isolated — the server keeps running — but the plugin fails. Fix the guest code and rebuild.

2. **Timeout.** The default per-invocation deadline is `100ms`. If the plugin does synchronous I/O (KV, fetch, heavy computation) it may overrun. Raise `timeout` in the plugin config or optimise the guest:
   ```toml
   [plugins.my-plugin]
   path    = "./my-plugin.wasm"
   timeout = "500ms"
   ```

3. **Memory limit exceeded.** The default guest linear-memory cap is 16 MiB. A large request body or heavy allocation can trigger an OOM guest panic. Raise `memory_limit` or stream data instead of buffering:
   ```toml
   memory_limit = "32m"
   ```

**Debugging:** Enable plugin-level logging in the guest with `sdk.Log(sdk.LevelDebug, "...")`. The host forwards these to its structured log at `debug` level (`-log-level debug`).

### Capability grants are not working

**Symptom:** `sdk.KVSet` returns `false`, `sdk.Fetch` returns an error, or the KV store is empty across invocations.

**Common causes:**

1. **Capability not declared.** Grants are opt-in in the config. A plugin that calls `KVSet` without `kv = true` or calls `Fetch` without `fetch = true` receives a permission error.
   ```toml
   [plugins.my-plugin]
   path = "./my-plugin.wasm"
   kv   = true          # required for KVGet / KVSet
   fetch = true         # required for Fetch
   allowed_hosts = ["api.example.com"]  # required when fetch = true
   ```

2. **KV is namespaced per plugin.** `kv-counter` cannot read keys written by `header-inject`. This is by design (isolation). If you need shared state, use a single plugin or proxy to an external store via `Fetch`.

3. **KV quota exceeded.** The defaults are 1024 keys and 1 MiB total per plugin. `KVSet` past either limit returns `false`. Increase or monitor via `jul_plugin_invocations_total{result="kv_rejected"}`.

### How to debug with console logs

The guest SDK provides `sdk.Log(level, msg)` which writes into the host's structured log. Use it like `fmt.Println` during development:

```go
func init() {
  sdk.Handle = func(req *sdk.Request) sdk.Action {
    sdk.Log(sdk.LevelDebug, "handling "+req.Method()+" "+req.URI())
    // ...
    return sdk.Continue
  }
}
```

Run the host with `-log-level debug` (or set `[global].log_level = "debug"`) to see the output. In production, logs are emitted at the configured `log_format` (text or JSON) and can be shipped via the access-log sink pipeline.

For heavier debugging, build a test-guest that deliberately panics (`testguest-panic`) or loops forever (`testguest-loop`) and run it against the test suite to confirm host isolation:

```bash
go test -tags wasmplugins ./internal/plugins/ -run "TestPanic|TestTimeout" -v
```

## Diagnostics

- `jul --version` — print the build version (from-source builds report
  `0.1.0-dev`; the real version is injected only by the release pipeline).
- `jul check -config server.toml` — full runtime preflight (validates paths,
  auth files, certs) without starting listeners.
- Metrics, tracing, and health endpoints: see
  [observability.md](observability.md).
