# Configuration reload semantics

> Canonical reference for how Jul.IA applies a configuration change and what
> "applied" actually guarantees. The implementation is a single `ReloadPlan`
> transaction object in [`internal/server/reload_plan.go`](../internal/server/reload_plan.go);
> lifecycle classification is single-sourced from
> [`internal/lifecycle/lifecycle.go`](../internal/lifecycle/lifecycle.go) and
> mirrored in [`config-lifecycle.yaml`](config-lifecycle.yaml).
>
> For operator symptoms and fixes — a change that did not apply, a
> `restart_required` rejection, a degraded-subsystem apply — see
> [troubleshooting.md](troubleshooting.md#reloads).

Jul.IA reloads configuration **without dropping connections**. A reload can be
triggered three ways:

- **Admin apply** — `POST /api/config/apply` (the Console "Apply changes"
  button) writes a new config and triggers a correlated reload. This path runs
  the full preflight gate before writing anything to disk and waits for the
  live reload outcome, returning it in the `reload` block of the response.
- **SIGHUP** (Unix) — operator sends the signal after editing the file directly.
- **Config file-watch** — the on-disk config file changed and the watcher fired.

**These three paths share the same live reload transaction, but the admin write
path validates *before* persistence and correlates the result with the request.**

The admin write path runs the full preflight (parse, dry-run, bind-probe, and
all restart-required checks) *before* the file is written. Nothing is saved
unless the config is validated to build and bind under preflight conditions.
Because preflight cannot observe every runtime condition (e.g. a bind race,
a late certificate file change, or transient disk errors), the live reload may
still fail after the file is written; such failures are recorded in the
structured `ReloadResult` and leave the previous generation authoritative.

SIGHUP and file-watch trigger the same live runtime swap, but they run restart-
required checks *at swap time* rather than before the file is written. This
means:

- Changes to **hot-reloadable** fields (routes, handlers, upstreams,
  compression, rate limiting, etc.) apply exactly as they do through the
  Console.
- Changes to **restart-required** fields (cache, egress, admin, tracing,
  access-log, ACME, log format, listener bind settings, `admin.rbac.enabled`) are **rejected at swap
  time** — the swap is aborted, `LastReload.Outcome=not_applied` is recorded
  with the reason, and the old config remains authoritative. The file on disk
  may contain the new value, but the running process ignores it until a
  restart. `global.worker_threads` is *hot-reloadable* (the GOMAXPROCS cap is
  updated on the next successful reload). **RBAC policy contents** (roles,
  principals, token hashes) are hot-reloadable via atomic policy swap even
  though `admin.rbac.enabled` itself is restart-required.
- **New-listener-only** fields (a new listen address, or a new L4 listener)
  apply to brand-new listeners without a restart; changing the same property on
  an already-bound listener is treated as restart-required.
- A new listen address that fails to bind aborts the reload before Publish
  (`Outcome=not_applied`); existing listeners continue serving the previous
  handler generation.

For the strongest guarantees, use the Console or admin API for configuration
changes. Direct file edits followed by SIGHUP are safe for hot-reloadable
changes; for restart-required changes they produce a clearly recorded failed
reload rather than silent mixed state.

## Two states: *applied* vs *serving*

- **Applied** — the new configuration passed validation, was persisted to disk,
  and a reload was triggered. This is what the apply API confirms.
- **Serving** — the live runtime has finished swapping to the new configuration.

The admin apply path waits for the live reload outcome up to the **currently
serving** config's `reload_timeout` plus a small scheduling margin. A candidate
that changes `reload_timeout` affects the *next* apply, never the one that
submits it (R15-01). The response includes a `reload` object that carries the
correlated result: `outcome` (`applied_live`, `applied_degraded`,
`not_applied`, or `saved_not_live`), `started_at`, `completed_at`,
`duration_ms`, `persisted`, per-subsystem status (`http`, `stream`, and
`admin`), per-phase timings in `phase_durations_ms` (milliseconds), and the
`desired_version` / `serving_version` fingerprints. If the reload is still in
flight when the coordinator's wait expires, the response returns
`saved_not_live` so the operator knows the config is persisted but the live
swap has not yet been confirmed.

## Managed apply coordinator

The admin write path is implemented by `ConfigApplyCoordinator` in
[`internal/app/config_apply.go`](../internal/app/config_apply.go). The
coordinator serializes every managed write, keeps the exact previous raw bytes,
runs preflight, persists atomically, suppresses file-watcher echoes, submits a
correlated reload, waits for the result, and — when the reload fails before
`Publish` — restores the exact previous bytes including comments and formatting.

The restoration guarantee applies only to managed admin writes. SIGHUP and
file-watch are external sources: they never rewrite the file, so a failed
external reload leaves the previous runtime serving while the disk may differ.
The Console offers retry or file repair for external failures.

A managed hot apply is refused while a planned restart is pending; the response
includes `pending_restart` and the operator must discard or complete the staged
restart first. Restart-required changes return `restart_required: true` with
`can_stage: true` so the UI can offer staging instead of a flat rejection.

## Planned-restart staging

A `stage_restart` apply mode persists a candidate that contains restart-bound
changes without triggering a live reload. The staged configuration takes effect
on the next process restart.

Two sidecar files are written adjacent to the active config:

- `<config-path>.pending-restart.json` — the marker (state, digests, versions,
  pending subsystems); mode `0600`; atomic write.
- `<config-path>.pending-restart.bak` — the exact previous raw bytes; mode
  `0600`; atomic write.

### Crash-consistent staging order

1. Backup written atomically to `.bak`.
2. Marker written in `prepared` state with base and candidate digests.
3. Candidate written atomically to the active config path.
4. Marker updated to `staged` state.

On a crash between steps 2 and 4 the next startup's `Reconcile` repairs the
state:

| Marker state | Disk digest vs marker | Action |
|---|---|---|
| `prepared` | equals base | Write never completed — remove marker + backup |
| `prepared` | equals staged | Write completed, state not updated — promote to `staged` |
| `staged`   | equals staged | Successful startup with staged config — remove marker + backup |
| any | neither | Inconsistent — log warning, preserve backup, require manual recovery |

### Discard

`DiscardPlannedRestart` restores the backup only after verifying:

1. Marker is present and in `staged` state.
2. Current disk digest matches the marker's staged digest (no external edit).
3. Live serving version still matches the marker's base serving version (no
   concurrent reload changed the runtime while the restart was pending).

On any verification failure a `409 Conflict` is returned and no file is touched.

## The apply preflight (truthfulness gate)

Before a configuration is written to disk, the admin write path runs a full
preflight so that a configuration recorded as *applied* is validated to build
and bind under preflight conditions:

1. **Parse + structural validation** — rejects malformed TOML and invalid field
   combinations.
2. **Candidate construction** — resolves secret references exactly once into an
   immutable `config.Candidate{Raw,Effective,Redaction,Digests}`. The same
   candidate is handed to the live reload transaction, so the runtime does not
   re-resolve secrets or re-build the effective config after the preflight
   passes. The redaction state is *not* installed until the asynchronous reload
   commits.
3. **TLS certificate validation** — checks cert/key files using the resolved
   candidate.
4. **Composition-root dry-run** — builds the entire runtime from the candidate
   (plugins, static roots, proxy/gRPC/FastCGI handlers, auth, WAF, router,
   compression) without disturbing the live runtime. A build error (or panic)
   aborts the write.
5. **Stream (L4) dry-run** — builds every `[[stream]]` route set from the
   candidate so a bad target is rejected too.
6. **HTTP listener bind-probe** — every newly introduced HTTP listen address is
   bind-probed and released on TCP. If the server block enables HTTP/3, the
   same address is also bind-probed on UDP, so an apply that adds an unbindable
   TCP port or a conflicting UDP port is rejected before the file is written.
7. **Stream listener bind-probe** — every newly introduced `[[stream]]` listen
   address (TCP and UDP) is bind-probed and released, symmetric with the HTTP
   probe.
8. **Restart-required and listener-rebind checks** — compares the candidate
   against the startup-bound effective fingerprint and the bind-time
   fingerprints of kept listeners. Secret-content rotation is detected via
   file digests. The live bound-listener snapshot is also used for the
   Console's pending-restart indicator, so the indicator reflects actually
   bound listeners rather than the on-disk baseline (R9-11).

Only after all gates pass is the file written and the reload triggered.

## The ReloadPlan transaction

The live reload is implemented as a `ReloadPlan` value that owns exactly one
`config.Candidate` per transaction. Secrets are resolved once at Resolve time;
all later phases read the same effective config and redaction state. The phases
are:

1. **Resolve** — obtain the immutable `config.Candidate`. On the admin apply
   path the candidate is the one already built during preflight and handed to
   the reload transaction; on SIGHUP/file-watch it is built here from the raw
   source config. In both cases secrets are resolved exactly once per reload.
2. **Validate** — run structural/runtime validation on `Candidate.Effective`.
3. **Lifecycle** — compare the candidate fingerprint against the startup
   fingerprint; then check kept listeners for bind-time property changes.
4. **Prepare** — build the handler tree and stage upstream pools and closers.
5. **StageListeners** — bind every newly added listen address; HTTP/3 QUIC
   resources are created but their accept loops are **not** started. A bind
   failure aborts the reload before Publish, leaving the old generation
   authoritative.
6. **Publish** — the ordered commit boundary. Commit the handler generation,
   install the candidate's redaction state, assign the live config, and swap
   the handler pointer. Each of these writes is ordered but not a single atomic
   transaction; the swap is race-free because the handler pointer is stored
   with one atomic operation and because downstream readers observe the new
   generation only after it is fully built.
7. **Activate** — start serving on staged TCP and HTTP/3 listeners.
8. **Retire** — remove listeners no longer in the config and retire the old
   handler generation after it drains.
9. **Refresh** — TLS certificate rotation is restart-only (R7-07); this phase
   is intentionally a no-op.
10. **PostCommit** — apply dynamic side effects: log level, GOMAXPROCS, and
    stream-proxy reload.

On any failure before Publish, `Abort()` releases all candidate resources
without touching live state. On any failure after Publish, the reload is
recorded as **degraded** (`Outcome=applied_degraded`) but is not rolled back,
because Publish is the point of no return. If the reload deadline expires
after Publish, the outcome is also reported as `applied_degraded` rather than
`not_applied`, because the handler generation has already committed and an
unsafe rollback would risk mixed state. Every phase records its wall-clock
duration in milliseconds; the total `duration_ms`, per-phase
`phase_durations_ms`, and per-subsystem timings are exposed in the
`ReloadResult`.

The `admin` subsystem reports RBAC policy update failures independently of the
`stream` subsystem, so a policy installation problem does not mask the L4
stream-proxy reload status.

### Publish-then-Activate ordering

The `Publish` phase installs the new handler generation **before** `Activate`
starts accepting connections on staged listeners. This guarantees that a client
reaching a newly bound address finds handlers ready for it, and that a client
on a kept address is handled by the new generation as soon as the atomic swap
completes — never by the old generation after a listener has started serving
new connections.

### Listener property changes

Listener bind-time properties (timeouts, header limits, h2c, HTTP/3, TLS
settings, mutual TLS, and the connection cap) are captured in a
`boundFingerprint` when the listener is created. On reload,
`listenerBoundRebindRequired` compares the bound fingerprint against the
candidate fingerprint for each kept listener. This detects:

- an explicit config change (e.g. new TLS `min_version`);
- an in-place file rotation (e.g. a CA file whose path is unchanged but whose
  contents changed);
- HTTP/3 or h2c toggles on an already-bound address.

These changes are reported as `restart_required` and rejected before Publish.

## Lifecycle classification: single source of truth

The authoritative classification is in
[`internal/lifecycle/lifecycle.go`](../internal/lifecycle/lifecycle.go) and is
mirrored in [`docs/config-lifecycle.yaml`](config-lifecycle.yaml). The three
classes are:

- **hot_reload** — takes effect on the next successful reload.
- **restart_required** — takes effect only after a process restart. The admin
  apply path returns HTTP 409 with `restart_required: true`; SIGHUP/file-watch
  set `LastReload.OK=false`.
- **new_listener_only** — honored for a brand-new listen address on reload;
  changing the property on an already-bound listener is restart-required.

Lifecycle checks compare **effective values** (secret references resolved,
file-backed secrets digested, `worker_threads` auto resolved to the effective
GOMAXPROCS cap). This prevents a saved secret-reference change from hiding a
real structural change and detects file-content rotation. Hot-reloadable
fields such as `worker_threads` are diffed against the live effective value so
that a change is applied on the next successful reload.

### Pending-restart indicator

The admin `/api/runtime/overview` endpoint exposes a `pending_restart` array
that lists startup-bound subsystems whose effective values on disk now differ
from what the running process was built from. The check compares effective
values (not raw references), so rotating the contents of a `${file:...}`
secret or changing an `${env:...}` value is detected even when the reference
itself is unchanged. Listener rebind is evaluated against the live
bound-listener snapshot, not the original on-disk baseline, so a kept
listener whose bind-time settings changed is reported correctly even when the
running set has drifted from disk (R9-11).

## Generation-scoped upstream pool snapshot

Each handler generation carries an immutable map of `PoolSnapshot` values for
the upstream pools reachable from that generation's routes. The snapshot is
captured from the live registry when the generation is committed. Handlers
select backends with `PickCtx` / `BackendsCtx`, which prefer the generation-
scoped snapshot over the live registry. This gives every request a stable
backend view for its own lifetime while allowing static upstream changes to
converge on the next request after a reload (R6-04, R7-03). An in-flight
request started on generation *N* continues to see the static backend set that
was live when that generation began, even if a subsequent reload changes the
pool before the request drains.

For **service-discovery-backed pools**, the set of backends is intentionally
not frozen at reload time. The generation snapshot carries the discovery
configuration, and the actual backend list is resolved on each request so that
newly registered or deregistered instances are visible without requiring a
reload. This means discovery pools converge in request time, while static pools
converge on the next reload.

## HTTP handler-generation retirement (resource teardown)

The HTTP handler swap is **generational**, and a superseded generation's
resources are torn down only after the requests that may still be using them
have finished. This matters because some handlers own backend resources that
become invalid the instant they are closed:

- gRPC-transcoding **backend connections**,
- WASM **plugin runtimes**, and
- **static-file directory handles** (`os.Root`).

The sequence on a successful reload is:

1. The composition root builds the new handler map and returns it together with
   a **retire callback** that closes the *previous* generation's resources. It
   does **not** close them inline.
2. The server installs the new generation with a single atomic pointer store,
   so every request that arrives after the store immediately uses the new
   handlers.
3. The server then retires the previous generation: it marks it *retiring* and
   waits for its in-flight request count to fall to zero (the generation has
   **drained**) before invoking the retire callback. A request still executing
   on the old generation keeps its gRPC connection, plugin runtime, and static
   root alive until it returns.
4. If the old generation does not drain within the shutdown grace period
   (`[global] shutdown_timeout`, default 30s), the server logs a warning and
   closes the resources anyway, so a wedged request cannot leak them forever.

The atomic store plus reference counting makes the swap race-free: a request
increments the generation's in-flight counter **before** it checks the
*retiring* flag, while retirement sets *retiring* **before** it reads the
counter. Under Go's sequentially-consistent atomics, if retirement observes a
zero count and closes resources, any racing request is guaranteed to see
*retiring* set and retry on the live generation instead of touching a closed
connection. A **rejected** reload never reaches step 1's swap: its freshly
built (staged) resources are closed immediately and the live generation is
untouched.

### Stateless per-reload components

Not every reloaded component owns teardown-sensitive resources. Per-location
**authenticators** (CIDR / Basic / JWT / forward-auth) are rebuilt fresh on each
reload and the previous set is dropped for the garbage collector: an
authenticator holds no background worker, timer, or long-lived socket — its
JWKS cache refreshes lazily on the request path, never from a goroutine — so
there is nothing to close and no retire callback to schedule. This is validated
at runtime by `internal/auth`'s `TestReloadChurnNoLeak`, which drives sustained
build/exercise/drop churn across every auth permutation and asserts the
goroutine count and post-GC heap return to their pre-churn baseline.

## Reload timeout and deadlines (`[global].reload_timeout`)

A reload measures its own duration against `[global].reload_timeout` (default
10s; zero or omitted defaults to 10s). The admin apply path submits a
per-request deadline of `now + reload_timeout` to the live reload transaction.
If the reload cannot reach Publish before the deadline expires, it is
**cancelled before Publish** (`Outcome=not_applied`, `timed_out=true`) and the
previous generation remains authoritative. This is bounded cancellation: only
the side-effect-free preparation phases are interruptible, so a slow factory
never leaves the runtime in a mixed state.

After Publish the timeout no longer cancels work; the remaining activation and
post-commit phases complete even if the deadline has passed, and the outcome is
recorded as `applied_degraded` if a post-commit side effect fails or the
overall duration exceeded the threshold.

The default 10s should accommodate all normal configs; operators may raise it for
very large configs or environments with slow DNS.

## Changes that require a restart

The authoritative list is [`docs/config-lifecycle.yaml`](config-lifecycle.yaml),
whose entries are checked against the Go registry by `scripts/docs-check.py`.
`lifecycle.DiffConfig` compares the effective value of every registered path
using schema-derived extractors, so a field cannot be added to the registry
without being diffed. The runtime rejects the following categories with `restart_required` at apply
time (admin path) or at swap time (SIGHUP/file-watch):

- **Log format and access-log sinks** — the log handler and sink handles are
  built once at startup. Log *level* is hot-reloadable.
- **ACME issued-domain set / issuer** — frozen when the autocert manager is
  built at startup.

`global.worker_threads` is *not* restart-required: the GOMAXPROCS cap is
applied dynamically on the next successful reload.
- **Listener bind-time settings** — for an address the server already holds,
  the socket is bound once and reused. Changing read/read-header/write/idle
  timeouts, max header bytes, h2c, HTTP/3, or the global connection cap cannot
  rebind the listener live.
- **TLS handshake parameters on an existing listener** — minimum TLS version,
  certificates, and mutual-TLS policy are baked into the listener's TLS config.
- **Tracing** — the OpenTelemetry pipeline is wired once at startup.
- **Response cache** — the cache backend (LRU/disk tiers, counters) is built
  once at startup.
- **Egress allow-list** — the outbound dial policy is built once at startup.
- **Admin server** — listener, token, rate limits, history, plugin-upload, and
  audit-log settings are baked in at startup. Token rotation in particular does
  not revoke the old token until restart.
- **Metrics host label** — set when the Prometheus registry is created.

Adding a brand-new `listen` address is *not* restart-required: the reload binds
it fresh. Only changes to an address the server is already serving are gated.

### L4 stream listeners {:#l4-stream-listeners-are-not-affected}

`[[stream]]` routes (`proxy_pass`, `sni_routes`, `proxy_protocol`,
`connect_timeout`, `idle_timeout`) are swapped atomically per listener and take
effect on the next connection. The only bind-time properties are `protocol`
(TCP/UDP) and `listen`; changing either keys a different listener, which is
bound fresh and drained. See [stream-proxy.md](stream-proxy.md#hot-reload).

## Optimistic concurrency

The admin apply path is guarded by `base_version`: if the live configuration
changed since the edit was prepared, the apply is rejected with `409 Conflict`
so a stale edit cannot clobber a concurrent change. See
[console.md](console.md) for conflict handling.
