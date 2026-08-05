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

The file parsed but failed validation. Jul.IA rejects both unknown keys and
known keys with invalid enum, duration, size, worker, status, or scalar values;
it does not silently fall back or clamp. Run `jul check -config server.toml` for
the full list of errors, or `jul lint` for the same errors plus best-practice
warnings. Use `jul lint -json` for machine-readable output (schema in
[configuration.md](configuration.md#cli-json-output)). The
[configuration value contract](config-value-contract.json) records exact allowed
values, bounds, and zero semantics.

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

Some settings are **bound at startup** and a hot reload keeps the running value
(it logs a warning rather than silently misapplying): the ACME issued-domain set
and issuer, listener bind-time settings (max-connections, listener timeouts, max
header bytes, HTTP/3 or h2c toggles), TLS handshake parameters (`min_version`,
mutual-TLS `client_auth`), tracing, and access-log enablement/sinks. These require a
**restart** to take effect. See [reload-semantics.md](reload-semantics.md) for
the exact *applied vs. serving* model and the full restart-required list.

### An apply is rejected with `restart_required` (nothing was saved)

When you apply a config through the admin API/Console that changes one of the
startup-bound settings above, the write path **refuses it without persisting
anything** and returns **HTTP 409** with `restart_required: true` and
`can_stage: true`. The Console shows a distinct *Restart required — not applied*
banner and offers a **Save for next restart** action. This is deliberate: the
alternative — recording the change as applied while the old value keeps serving —
would be dishonest.

You have two options:

1. **Save for next restart** (`stage_restart` mode): the valid candidate is
   persisted and waits for the next process restart. Use
   `POST /api/config/apply?mode=stage_restart` or the Console button. The
   running process continues serving the previous config unchanged.
2. **Edit directly and restart**: edit the configuration file to add the
   restart-required change, then restart the server process.

See [console.md](console.md#restart-required-changes).

### Hot apply is blocked with "A staged restart is pending"

A previous `stage_restart` apply saved a candidate for the next restart. Hot
applies are refused until you either:

- **Restart the process** — the staged config takes effect and the staged state
  is automatically cleared on startup.
- **Discard the staged config** — `POST /api/config/pending-restart/discard`
  or the Console *Discard staged configuration* button. The previous
  configuration is atomically restored and hot applies resume immediately.

If you need to update the staged candidate (add further restart-required changes)
you can re-apply with `stage_restart` mode while a staged restart is pending;
this overwrites the staged file but preserves the original rollback base.

### Inconsistent staged-restart marker on startup

On startup, the server attempts to reconcile any pending-restart sidecar files
left by a previous process. In rare cases (simultaneous crash and disk error) the
reconciliation may detect an inconsistent state and log a warning like:

```
planned-restart reconciliation warning: disk digest does not match staged digest;
backup preserved at server.toml.pending-restart.bak
```

The backup file (`<config-path>.pending-restart.bak`) contains the exact previous
raw configuration. To recover:

1. Inspect the backup: `cat server.toml.pending-restart.bak`
2. If the backup looks correct, restore it: `cp server.toml.pending-restart.bak server.toml`
3. Remove the sidecar files:
   `rm server.toml.pending-restart.json server.toml.pending-restart.bak`
4. Restart the server.

See [reload-semantics.md](reload-semantics.md#planned-restart-staging) for the
full reconciliation rules.

### The Console shows "Applied with a degraded subsystem"

The HTTP configuration was accepted, but an **asynchronous** subsystem reload
failed — most commonly the L4 stream (`[[stream]]`) proxy, whose listener rebind
runs after the apply response is sent. The banner names the failed subsystem and
its error. Check the server logs for the stream reload error, fix the offending
`[[stream]]` block, and re-apply. The four possible apply outcomes (fully live,
runtime-reloading, degraded-subsystem, restart-required) are described in
[console.md](console.md#apply-outcomes).

### A bad edit did not take down the server

By design: a reload that fails to load or validate is rejected and the running
configuration is kept. Check the logs for `reload aborted: ...` and fix the file.

## Service discovery

Dynamic upstreams (`[upstreams.discovery]`) refresh a pool live from Consul,
Kubernetes EndpointSlices, or DNS. The Consul and Kubernetes providers require
the matching build tag (`consul`, `kubernetes`); DNS discovery is core. See
[service-discovery.md](service-discovery.md).

### The backend pool is empty / all requests return `502`

The provider returned no usable instances **and** there was no prior good set to
fall back to (a cold start). Check, in order:

- The provider is reachable from the server. If an `[egress]` allow-list is
  enabled, a discovery `address`/`api_server` that is not allow-listed is refused
  at dial time — see [egress.md](egress.md).
- **Consul:** the `service` name is registered and, when `passing_only = true`,
  at least one instance is passing its health check.
- **Kubernetes:** the `namespace`/`service` is correct, the EndpointSlice has
  `ready` endpoints, and the service account can `get`/`list`/`watch`
  `endpointslices`. A `403` from the API server means the Role/RoleBinding is
  missing or wrong.

### Backends do not update after a change in the provider

Discovery polls on an interval — check `refresh` on the discovery block (a large
value means slow convergence). The server **keeps the last good set** on a failed
or empty resolve, so a transient provider outage will not drop live backends;
they update on the next successful poll.

### Metrics to watch

- `jul_upstream_backends{pool}` — current backend count discovery sees.
- `jul_discovery_errors_total{pool}` — failed/empty resolves (last-good kept).
- `jul_upstream_healthy{pool}` — of those, how many pass active health checks.

A rising `jul_discovery_errors_total` with a flat `jul_upstream_backends` is the
signature of a provider outage masked by keep-last-good.

### Validating discovery end-to-end

Reproduce a real Consul/Kubernetes convergence locally with the live integration
runbook (and the CI lane that automates the Consul path):
[service-discovery.md](service-discovery.md#local-live-integration-runbook-issue-24).


## Egress allow-list

### Egress blocks an outbound fetch

When the optional [`[egress]`](egress.md) allow-list is enabled, the server's own
config-driven fetches — JWKS, forward-auth, discovery, ACME/OCSP, and WASM plugin
`fetch` — may only reach the destinations in `allow`. A newly enabled allow-list
that omits a required host is the usual cause of an auxiliary fetch failing right
after turning it on. Symptoms by subsystem:

- **`auth`** — token validation returns `401` (JWKS unreachable) or forward-auth
  returns `503`.
- **`discovery`** — the pool logs a resolve failure and keeps its last-good set.
- **`acme` / `ocsp`** — certificate issuance fails, or certificates are served
  unstapled. See [tls-acme.md](tls-acme.md#egress-allow-list-prerequisites) for
  the CA/OCSP hosts to allow.
- **`plugin`** — the guest's `fetch` returns `-5` (distinct from a plugin-local
  `-3`).

Diagnose it without exposing any destination: the log line names the subsystem,
the normalized host, and a bounded reason, and the metrics
`jul_egress_decisions_total{subsystem,result,reason}` count blocks. The Console
**Security** panel shows the enabled state, allow-rule count, and a recent-blocked
breakdown by subsystem and reason. Add the missing host (prefer a **name** or
suffix for CDN-fronted CAs) to `allow` and restart — `[egress]` is startup-bound.


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

## Soak & load-test interpretation

The soak harness (`scripts/soak.sh`, or the `soak`-tagged `TestSoak*` tests)
drives sustained concurrent load and reports whether resource use stays bounded.
It is the **post-GA gate** ([ADR 0005](adr/0005-soak-post-ga-gate.md)); published
runs live in the [soak evidence log](soak-evidence.md), and the step-by-step
procedures in [soak-procedures.md](soak-procedures.md).

### Reading the output

```
soak: duration=5m0s workers=16 requests=1234567 errors=0
soak: goroutines 42 -> 44, heap 1600000 -> 1900000 bytes
```

| Signal | Healthy | Investigate |
| --- | --- | --- |
| `errors=` | exactly `0` | any non-zero means a request failed under load |
| Goroutine growth | ≤ `4*workers+32` | unbounded growth is a goroutine leak |
| Heap growth | ≤ 64 MiB | a steady climb is a heap leak (take a pprof profile) |

### "Heap grew but it is not a leak"

Some libraries pre-allocate pools. The zstd encoder, for example, reserves
~48 MiB up front: that is a one-time legitimate allocation, not a leak — it
appears once and then stays flat. A real leak keeps climbing across the whole
run. Confirm with a heap profile from the admin `/debug/pprof/heap` endpoint
(behind the admin token).

### The proxy soak fails on Windows within minutes

Expected on Windows: the client side exhausts ephemeral ports dialling
repeatedly from one machine. The proxy soak is only viable on Windows for smoke
durations (≤ 20 s); use the Linux CI release gate or a real-binary burn-in for
longer proxy validation. The UDP-churn and L4 stream soaks run reliably on
Windows. See [soak-procedures.md](soak-procedures.md).

## Diagnostics

- `jul --version` — print the build version (from-source builds report
  `0.1.0-dev`; the real version is injected only by the release pipeline).
- `jul check -config server.toml` — full runtime preflight (validates paths,
  auth files, certs) without starting listeners.
- Metrics, tracing, and health endpoints: see
  [observability.md](observability.md).
