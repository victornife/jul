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

## Diagnostics

- `jul --version` — print the build version (from-source builds report
  `0.1.0-dev`; the real version is injected only by the release pipeline).
- `jul check -config server.toml` — full runtime preflight (validates paths,
  auth files, certs) without starting listeners.
- Metrics, tracing, and health endpoints: see
  [observability.md](observability.md).
