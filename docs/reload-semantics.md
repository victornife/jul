# Configuration reload semantics

> Canonical reference for how Jul.IA applies a configuration change and what
> "applied" actually guarantees. It consolidates the reload behaviour that is
> also described, per feature, in [core-http.md](core-http.md),
> [stream-proxy.md](stream-proxy.md), and [console.md](console.md).

Jul.IA reloads configuration **without dropping connections**. A reload can be
triggered three ways, all of which converge on the same validated path:

- **SIGHUP** (Unix) — operator sends the signal.
- **Config file-watch** — the on-disk config file changed.
- **Admin apply** — `POST /api/config/apply` (the console "Apply changes"
  button) writes a new config and triggers a reload.

## Two states: *applied* vs *serving*

- **Applied** — the new configuration passed validation, was persisted to disk,
  and a reload was triggered. This is what the apply API confirms.
- **Serving** — the live runtime has finished swapping to the new configuration.

The swap is asynchronous, so the apply response says the configuration was
**"validated and saved; the live runtime is reloading"** rather than the
past-tense "reloaded." The gap between *applied* and *serving* is kept as small
as possible by an up-front preflight (below), and it is normally sub-second.

## The apply preflight (truthfulness gate)

Before a configuration is written to disk, the admin write path
(`WriteConfigRaw`) runs a full preflight so that a configuration which is
recorded as *applied* is guaranteed to build and bind:

1. **Parse + structural validation** — rejects malformed TOML and invalid field
   combinations.
2. **Composition-root dry-run** — builds the *entire* runtime on a clone of the
   config (plugins, static roots, proxy/gRPC/FastCGI handlers, auth, WAF, router,
   compression) without disturbing the live runtime. A build error (or panic)
   aborts the write.
3. **Stream (L4) dry-run** — builds every `[[stream]]` route set on the clone so
   a bad target is rejected too.
4. **HTTP listener bind-probe** — every **newly introduced** HTTP listen address
   is bind-probed and released, so an apply that adds an unbindable port (in use,
   privileged, invalid) is rejected before the file is written.
5. **Stream listener bind-probe** — every **newly introduced** `[[stream]]`
   listen address (TCP and UDP) is bind-probed and released, symmetric with the
   HTTP probe. Without this, an unbindable stream port would be recorded as
   applied while the asynchronous reload's bind failed and surfaced only in the
   Overview stream-status panel.

Only after all five pass is the file written and the reload triggered.

## Transactional swap

The reload itself is transactional per layer, so a failure never leaves a
partial configuration serving:

- **HTTP** — the new handler generation is built before the listener set is
  swapped; existing connections finish on their original handler.
- **Stream** — routes and pools are built and newly added listeners bound
  **before** any running state changes; a bind failure rolls back to the
  previously serving configuration (see [stream-proxy.md](stream-proxy.md#hot-reload)).

### HTTP handler-generation retirement (resource teardown)

The HTTP handler swap is **generational**, and a superseded generation's
resources are torn down only after the requests that may still be using them
have finished. This matters because some handlers own backend resources that
become invalid the instant they are closed:

- gRPC-transcoding **backend connections**,
- WASM **plugin runtimes**, and
- **static-file directory handles** (`os.Root`).

The sequence on a successful reload is:

1. The composition root builds the new handler map and returns it together with
   a **retire callback** that closes the *previous* generation's resources (the
   three kinds above). It does **not** close them inline.
2. The server installs the new generation with a single atomic pointer store, so
   every request that arrives after the store immediately uses the new handlers.
3. The server then retires the previous generation: it marks it *retiring* and
   waits for its in-flight request count to fall to zero (the generation has
   **drained**) before invoking the retire callback. A request that is still
   executing on the old generation therefore keeps its gRPC connection, plugin
   runtime, and static root alive until it returns.
4. If the old generation does not drain within the shutdown grace period
   (`[global] shutdown_timeout`, default 30s), the server logs a warning and
   closes the resources anyway, so a wedged request cannot leak them forever.

The atomic store plus reference counting is what makes the swap race-free: a
request increments the generation's in-flight counter **before** it checks the
*retiring* flag, while retirement sets *retiring* **before** it reads the
counter. Under Go's sequentially-consistent atomics, if retirement observes a
zero count and closes resources, any racing request is guaranteed to see
*retiring* set and retry on the live generation instead of touching a
closed connection. A **rejected** reload never reaches step 1's swap: its
freshly built (staged) resources are closed immediately and the live generation
is untouched.

## Reload timeout (`[global].reload_timeout`)

A reload measures its own duration against `[global].reload_timeout` (default 10s; zero or omitted defaults to 10s).
If the reload takes longer than the configured threshold, it is recorded as `timed_out`:

- The **swap still completes** — the timeout is advisory, not a hard cancellation,
  because the factory is side-effectful and cannot be safely interrupted mid-flight.
  What was previously an unsafe goroutine-based "abort" has been replaced by a
  warning so that operators know the reload was slow, while the runtime stays
  consistent.
- The apply response may include `previous_reload.timed_out: true` so the UI can warn the
  operator that the last completed reload exceeded the expected duration. Note: this
  describes the previous reload, not necessarily the one triggered by the current apply.
- `previous_reload.error` is empty when the timeout is advisory; the reload succeeded but
    was slow.

The apply preflight already eliminates build and bind failures before the file
is written, so a timeout is a diagnоstic signal for pathological stalls (a wedged
`OnReloaded` or an unexpectedly slow factory) rather than a guard for a
frequently reachable failure mode. The default 10s should accommodate all normal
configs; operators may raise it for very large configs or environments with
slow DNS.

## Changes that require a restart

A small number of valid changes cannot take effect via hot reload and are
rejected at apply time with a `restart_required` response instead of being
silently accepted:

- **ACME issued-domain set / issuer** — frozen when the autocert manager is
  built at startup.
- **Listener bind-time settings** — for an address the server already holds,
  the socket is bound once at startup and reused across reloads. Changing the
  global max-connections limit, the listener read/read-header/write/idle
  timeouts, the max header bytes, or toggling HTTP/3 or h2c on an existing
  listener cannot rebind it live. (These are taken from the *first* server
  block on each `listen` address; later blocks sharing the address inherit it.)
- **TLS handshake parameters on an existing listener** — the minimum TLS
  version and the mutual-TLS client-auth policy (mode, trusted CAs, allowed
  SANs, CRLs) are baked into the listener's TLS config at bind time.
- **Tracing** — the OpenTelemetry tracer is wired once at startup, so enabling
  or disabling tracing or changing its endpoint or sample ratio takes effect on
  restart.
- **Access log** — the access-log sinks (`stdout` / `file` / `syslog`), their
  file path, format, and rotation are built once at startup; the file sink owns
  a rotating handle and the syslog sink a system-log connection, both kept across
  reloads. Any change to `[observability.access_log]` is held for a restart.

Adding a brand-new `listen` address (or a new server block on one) is *not*
restart-required — the reload binds it fresh. Only changes to an address the
server is already serving are gated. The operator restarts the process to apply
the gated changes, rather than believing they took effect. See
[console.md](console.md) for how the console surfaces this.

### L4 stream listeners are not affected

The bind-time freeze above applies to the **HTTP `[[servers]]`** listeners only.
The L4 **`[[stream]]`** proxy stores every tunable (`proxy_pass`, `sni_routes`,
`proxy_protocol`, `connect_timeout`, `idle_timeout`) in a forwarding *route*
that the reload swaps atomically on each surviving listener, so those changes
take effect on the next connection without a restart. The only bind-time
properties are the `protocol` (TCP/UDP) and the `listen` address — and changing
either keys a *different* listener, so the reload binds the new socket and drains
the old one (the new address is bind-probed at apply time). Stream settings are
therefore never silently not-applied, and no `restart_required` gate is needed
for them. See [stream-proxy.md](stream-proxy.md#hot-reload).

## Optimistic concurrency

The admin apply path is guarded by an optimistic-concurrency token
(`base_version`): if the live configuration changed since the edit was prepared,
the apply is rejected with `409 Conflict` so a stale edit cannot clobber a
concurrent change. See [console.md](console.md) for the console's conflict
handling.
