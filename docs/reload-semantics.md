# Configuration reload semantics

> Canonical reference for how Jul.IA applies a configuration change and what
> "applied" actually guarantees. The implementation is a single `ReloadPlan`
> transaction object in [`internal/server/reload_plan.go`](../internal/server/reload_plan.go);
> lifecycle classification is single-sourced from
> [`internal/lifecycle/registry.go`](../internal/lifecycle/registry.go) and
> mirrored in the generated
> [configuration lifecycle reference](generated/config-lifecycle.md),
> [`config-lifecycle.yaml`](config-lifecycle.yaml) and
> [`config-lifecycle.json`](generated/config-lifecycle.json).
>
> For operator symptoms and fixes — a change that did not apply, a
> `restart_required` rejection, a degraded-subsystem apply — see
> [troubleshooting.md](troubleshooting.md#reloads).

> **Lifecycle authority.** The Go registry is the sole machine authority and the
> schema is closed-world: every public TOML leaf has exactly one disposition and
> an unregistered path fails closed rather than being assumed hot. Regenerate
> the mirrors with `make lifecycle-generate`; `make generated-check` fails a PR
> that leaves them stale.
>
> Mixed candidates remain whole-candidate operations: Jul.IA does not silently
> publish a hot subset while another field is staged or restart-bound.

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

> **This description is the `file_owned` behavior.** Whether SIGHUP and the
> file watcher adopt an external edit at all is governed by
> `[global].config_authority` — see
> ["Configuration authority: managed vs file_owned"](#configuration-authority-managed-vs-file_owned)
> below. In `managed` mode (not the default) neither SIGHUP nor the watcher
> triggers a reload; both become drift detectors instead, and an external edit
> is adopted only through an explicit `POST /api/config/adopt-external`.

The admin write path runs the full preflight (parse, dry-run, bind-probe, and
all restart-required checks) *before* the file is written. Nothing is saved
unless the config is validated to build and bind under preflight conditions.
Because preflight cannot observe every runtime condition (e.g. a bind race,
a late certificate file change, or transient disk errors), the live reload may
still fail after the file is written; such failures are recorded in the
structured `ReloadResult` and leave the previous generation authoritative.

SIGHUP and file-watch trigger the same live runtime swap **in `file_owned`
mode**, but they run restart-required checks *at swap time* rather than before
the file is written. This means:

- Changes to **hot-reloadable** fields (routes, handlers, upstreams,
  compression, rate limiting, etc.) apply exactly as they do through the
  Console.
- Changes to **restart-required** fields (cache, egress, admin listener/token/
  rate limits/history/plugin-upload/audit, tracing, access-log, ACME, log
  format, listener bind settings) are **rejected at swap time** — the swap is
  aborted, `LastReload.Outcome=not_applied` is recorded
  with the reason, and the old config remains authoritative. The file on disk
  may contain the new value, but the running process ignores it until a
  restart. `global.worker_threads` is *hot-reloadable* (the GOMAXPROCS cap is
  updated on the next successful reload). **RBAC is fully hot-reloadable**: both
  the `admin.rbac.enabled` toggle and the policy contents (roles, principals,
  token hashes) are rebuilt into a prepared authentication snapshot and
  installed with a single atomic store at the reload Publish boundary, so
  enabling, disabling, or repolicying RBAC takes effect on the next successful
  reload without a restart.
- **New-listener-only** fields (a new listen address, or a new L4 listener)
  apply to brand-new listeners without a restart; changing the same property on
  an already-bound listener is treated as restart-required.
- A new listen address that fails to bind aborts the reload before Publish
  (`Outcome=not_applied`); existing listeners continue serving the previous
  handler generation.

The structured App workflow uses that same classifier during preview. It preserves
the exact ordered operations and `base_version` from preview through handoff, and
only Configuration performs the final apply or planned-restart staging. Upstream,
health-check, discovery, and route changes are classified from the complete
candidate. A new dedicated plaintext gRPC listener can carry an h2c toggle as a
`new_listener_only` change; changing h2c on a retained address is
restart-required, so the Apps creator never mutates an existing plaintext
listener and refuses same-address sibling risk before preview.

The sparse global operations use the same path. `global_set`,
`compression_set`, and `rate_limit_global_set` first produce one canonical
complete candidate, then the registry classifies it. `global.log_format`
therefore stages the complete candidate, not only that field. A changed global
`max_conns` stages whenever any desired address is already bound; only an
all-new affected listener set can adopt it during live bind. Compression and
global rate/key/burst changes remain hot. Operation summaries contain field
names only, and a stage update preserves the original pre-stage rollback base.

For the strongest guarantees, use the Console or admin API for configuration
changes. Direct file edits followed by SIGHUP are safe for hot-reloadable
changes; for restart-required changes they produce a clearly recorded failed
reload rather than silent mixed state.

## Configuration authority: managed vs file_owned

Jul.IA has exactly one authoritative desired-state writer at a time, declared
by `[global].config_authority` (ADR 0019):

| Value | Meaning |
| --- | --- |
| `managed` | Jul.IA owns the configuration file. Console/API writes are validated and persisted through the coordinator below. An external edit is never silently adopted — it becomes *drift*, resolved only through an explicit, authenticated adoption. |
| `file_owned` (default) | An external file or GitOps pipeline owns the configuration file. SIGHUP and the file watcher behave exactly as described in this document. Every mutating admin endpoint is refused with `409 config_authority_read_only` before any side effect. |

The field is `restart_required` and can only change through `stage_restart`;
it cannot be hot-applied, because it moves ownership of persistence, history,
and drift detection between subsystems wired at startup. **Omitted resolves
to `file_owned`** — this is a fixed default, never derived from
`[admin].enabled` or any other field, chosen because a wrong `managed` default
fails silently (SIGHUP becomes a no-op) while a wrong `file_owned` default
fails loudly (every write is refused with a named reason). See
[configuration.md](configuration.md#global) for the
full field reference and [deployment.md](deployment.md#configuration-authority)
for the operational implications, migration, and adoption workflow.

**In `managed` mode:**

- The file watcher and SIGHUP no longer trigger a reload. Both re-assess drift
  — comparing the raw SHA-256 of the file against the digest Jul.IA last
  persisted — and update the status/Console banner. A watcher event whose
  digest matches Jul.IA's own last write is still recognized and suppressed
  exactly as before; anything else becomes drift.
- Drift is assessed at exactly four event-driven points — never polled: the
  watcher, SIGHUP, immediately before every managed write, and an explicit
  `POST /api/config/authority/refresh`. The last of these lets an operator or
  the Console force an up-to-date drift assessment on demand — for example
  before deciding whether to adopt — without waiting for one of the other
  three to fire.
- Managed writes (`POST /api/config/apply`, the structured patch API, the
  listener trust-policy patch, `stage_restart`, and history rollback) are
  refused while drift is unresolved, or before the managed baseline has ever
  been established (a fresh `managed` boot starts `managed_unadopted`).
- An unresolved external edit is adopted through
  `POST /api/config/adopt-external` (preview at
  `GET /api/config/adopt-external/preview`), which requires the dedicated
  `config:adopt` permission and full confirmation. Adoption runs the exact
  same validation/preflight pipeline as any other apply; there is no reduced
  path for externally-authored bytes.
- A restart with unresolved drift still **serves** the file — refusing to
  start would turn a configuration problem into an outage — but the managed
  baseline does not advance, so the external bytes never become Jul.IA's
  desired state merely by surviving a restart.

**In `file_owned` mode**, behavior is unchanged from every description in the
rest of this document: SIGHUP and the watcher adopt external edits exactly as
they always have, and the admin write paths are disabled.

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

## Managed apply terminal ledger

Every managed write is assigned a **boot-scoped apply ID** of the form
`rl_<instance>_<sequence>`, where `<instance>` is a 12-hex-character identifier
generated once per process (so IDs are never reused across restarts) and
`<sequence>` is a monotonic per-process counter. The coordinator records the
transaction in a bounded in-memory ledger
([`internal/admin/managed_apply_registry.go`](../internal/admin/managed_apply_registry.go))
that carries three lifecycle states:

- **pending** — accepted but not yet terminal. Never evicted.
- **finalizing** — the single terminal callback has been claimed and the
  terminal side effects (history, audit, metrics, ledger) are being applied.
  Never evicted; a duplicate finalization callback is rejected here.
- **terminal** — exactly one terminal result has been recorded. Retained
  subject to the ledger's capacity and TTL bounds.

A browser retrieves the exact record by ID at
`GET /api/config/applies/{id}`, independent of any later transaction, so a
newer apply can never overwrite the result the operator is awaiting. Response
rules:

| Condition | Status |
|---|---|
| malformed ID (not `rl_<instance>_<sequence>`) | `400 Bad Request` |
| unknown or evicted ID | `404 Not Found` |
| record present, still `pending` | `202 Accepted` with `state=pending` |
| record present, `finalizing` | `202 Accepted` with `state=finalizing` |
| record present, `terminal` | `200 OK` with the terminal record |
| record in an invalid lifecycle state | `500 Internal Server Error` |

`finalizing` is externally observable but remains **non-terminal**: the runtime
outcome exists while history, audit, metrics and terminal-ledger completion are
still running. A record is terminal only when `state=terminal` — HTTP 200 alone
is never the terminal test — so clients must keep polling until then.

Each pending record also carries the **absolute transaction deadline** (see
[Reload timeout and deadlines](#reload-timeout-and-deadlines-globalreload_timeout)),
so the Console can render a deadline-aware "finalization expected by …" hint and
switch to a past-deadline message when the poll expires.

**Retention.** Terminal results are retained for at least **512 entries or one
hour**, whichever keeps more recent results; pending and finalizing records are
never evicted.

**Permission.** The endpoint is authorized by **any-of** `status:read` **or**
`config:apply`, so a principal privileged enough to apply configuration may read
the secret-free result of its own transaction without also holding
`status:read`.

**Secret-free by construction.** A ledger record never carries raw TOML, bearer
credentials, token digests, secret-expanded configuration, or the caller's
source IP. The owning token id is private and is never serialized to the
endpoint.

## Exactly-once finalization

Every terminal transaction is finalized **exactly once**. The finalizer claims
the terminal callback through the ledger's `finalizing` state; a duplicate
callback for the same ID is rejected without producing duplicate history, audit,
or metric side effects. The terminal side effects then run in a fixed order:

1. Claim `pending` → `finalizing`.
2. Record history according to the snapshot decision matrix.
3. Record the history-outcome metric.
4. Record the terminal managed-apply metric.
5. Emit the operation-specific terminal audit event.
6. Complete the per-ID terminal ledger record.
7. Update the convenience latest pointer only when this record is the newest.
8. Publish the advisory finalization-health state.
9. Release the in-flight gate and expose terminal completion.

The per-ID terminal ledger is deliberately completed **after** history so a
browser retrieving the exact ID observes the record only once its history and
audit provenance are attached; the `finalizing` state (step 1) is the externally
observable window during which those steps run.

**History rules.** A committed apply (`applied_live` or `applied_degraded`) and
a rollback each receive a history snapshot. A restoration failure creates an
**emergency recovery snapshot** containing the exact pre-apply configuration
even though the attempted apply failed.

**Finalization degradation is advisory, never a runtime-apply failure.** A
committed apply stays a success (`ok=true`) even if its history snapshot or its
ledger/audit finalization degraded. The degradation is surfaced as a
non-blocking advisory on the ledger record (`history_error` /
`finalization_error`) and rendered alongside — never replacing — the apply
outcome; it is never a readiness or success signal in either direction. A
finalization-callback panic is caught, converted into a finalization error,
surfaced through admin health, and never wedges the coordinator: finalization
failure is an observability/compliance degradation, not a runtime rollback.

## Planned-restart staging

A `stage_restart` apply mode persists a candidate that contains restart-bound
changes without triggering a live reload. The staged configuration takes effect
on the next process restart.

At most one staged candidate is in flight at a time. A second `stage_restart`
request received while a candidate is already pending **updates the existing
managed staged candidate** rather than being rejected: the new candidate
replaces the previously staged one on disk, but the original pre-stage backup
(`.bak`) and the marker's base serving/canonical versions are preserved so the
rollback base and the diff base remain the configuration that was serving when
the *first* stage was created (never the intermediate staged candidate). The
response reports `pending_restart.staged` (surfaced by the Console as the
"staged configuration updated" outcome). The staged diff is always computed
against the original serving config, so successive updates cannot drift the
rollback baseline.

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

### Managed-apply terminal handoff

A managed apply retains its single-writer gate through every possible
restoration and the final disk-state read. Once config-path mutation is complete,
the coordinator releases both its in-flight admission state and the server's
`ReloadRequest.Finalized` gate **before** the per-ID ledger can become terminal.
Consequently, terminal status is a reliable readiness signal: a caller that
observes a terminal record may immediately submit the next valid apply and will
not receive a stale “previous apply is still in flight” rejection.

History snapshots, audit events, metrics, latest-result projection and terminal
ledger publication remain exactly-once and serial. They use a separate managed
finalization lock, so a later apply may begin after restoration without allowing
two managed finalizers to write history concurrently. The synchronous apply
response is still delivered only after its terminal finalization returns.

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

## Lifecycle classification: single source of truth

The authoritative classification is the Go registry in
[`internal/lifecycle/registry.go`](../internal/lifecycle/registry.go). It is the
**machine authority**: the runtime never reads lifecycle behavior from YAML,
Markdown or JSON. [`docs/config-lifecycle.yaml`](config-lifecycle.yaml),
[`docs/generated/config-lifecycle.md`](generated/config-lifecycle.md) and
[`docs/generated/config-lifecycle.json`](generated/config-lifecycle.json) are
deterministic renderings of it, produced by `make lifecycle-generate` and
verified by `make generated-check`. Hand-editing them changes nothing at
runtime and fails CI.

The world is closed. Every public TOML leaf reachable from `config.Config` —
inventoried once by [`config.SchemaPaths`](../internal/config/inventory.go) —
has exactly one disposition, and an unregistered path fails closed:
`lifecycle.Lookup` reports absence and `lifecycle.ClassOf` returns an error
naming the regeneration command. Nothing defaults to hot reload.

The classes are:

- **hot_reload** — takes effect on the next successful reload.
- **restart_required** — takes effect only after a process restart. The admin
  apply path returns HTTP 409 with `restart_required: true`; SIGHUP/file-watch
  set `LastReload.OK=false`.
- **new_listener_only** — honored for a brand-new listen address on reload;
  changing the property on an already-bound listener is restart-required.
- **ignored_deprecated** — parsed for v1 compatibility but read by no runtime
  consumer. Changing `global.access_log`, `global.error_log`,
  `servers.*.access_log` or `servers.*.error_log` never creates a pending
  restart and is never reported as applied. Use
  `[observability.access_log]` instead.
- **validation_rejected_reserved** — a reserved seam that configuration
  validation rejects today, so no running process can have consumed it. This
  covers `servers.*.tls.acme.dns_provider` (DNS-01 is not implemented; `http-01`
  and `tls-alpn-01` are both supported and are ordinary settings) and
  `servers.*.locations.*.rate_limit.max_conns` (connection caps are
  listener-global).

Lifecycle checks compare **effective values** (secret references resolved,
file-backed secrets digested, `worker_threads` auto resolved to the effective
GOMAXPROCS cap). This prevents a saved secret-reference change from hiding a
real structural change and detects file-content rotation. Hot-reloadable
fields such as `worker_threads` are diffed against the live effective value so
that a change is applied on the next successful reload.

Values that carry secrets never leave the process: `admin.token`,
`admin.rbac.principals.*.token`, the discovery tokens, and the TLS
certificate/key/CA/CRL material are compared as digests. Certificate, key,
client-CA and CRL material is digested by **file content**, so rotating a file
in place without editing the configuration is still detected as a change.

### Conditional classification

Some paths do not have one fixed answer: a bind-time value edited on a listen
address that survives the reload strands the running socket, while the same
edit confined to an address the reload adds or removes is adopted when the new
socket binds. `lifecycle.Classify(before, candidate, live)` resolves those
entries against the live listener set. It is pure — no filesystem, no network —
so the configuration preview API and the apply path reach identical verdicts
from the same inputs.

### TLS, PKI, ACME and HTTP/3

These subsystems are classified per exact leaf rather than as one group, so a
restart reason names the field that actually changed:

- `servers.*.tls.enabled`, `.min_version`, `.cert`, `.key`;
- `servers.*.tls.client_auth.mode`, `.ca_file`, `.verify_san`, `.crl_file` —
  the **mtls** bundle installed in the listener's `tls.Config` at bind time;
- `servers.*.tls.acme.enabled`, `.email`, `.ca`, `.domains`, `.challenge`,
  `.cache_dir`, `.ocsp_stapling`, plus the reserved `.dns_provider`;
- `servers.*.http3.enabled` and `servers.*.http3.alt_svc_max_age` — the
  **http3** QUIC listener and its Alt-Svc advertisement;
- `servers.*.h2c`.

All of them are compared per listen address, so adding or removing an unrelated
listener never produces a restart-required verdict for an address nobody edited.

### Listener property changes

Listener bind-time properties (timeouts, header limits, h2c, HTTP/3, TLS
settings, mutual TLS, and the connection cap) are captured in a
`boundFingerprint` when the listener is created. That fingerprint is a checked
mirror of the registry, not a second lifecycle list:
`TestListenerBindFingerprintMirrorsRegistry` asserts every property it freezes
maps to a registry path classified as listener-bound, and that a new
listener-bound entry is added to the mapping. On reload,
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

### Generation-owned background work

Some work legitimately outlives the request that started it. Today that is the
response cache's `stale_while_revalidate` refresh: the client is served the stale
representation immediately and the origin is contacted afterwards.

Such work is **not** allowed to escape generation accounting. Before the
originating request returns, it takes a **background lease** on its generation
(`internal/background`). The lease increments the *same* in-flight counter used
by requests, so step 3 above applies to it unchanged: a superseded generation
keeps its gRPC connections, plugin runtimes and static roots open until its
leased work finishes.

The lease also fixes the work's lifetime:

- the operation context is rooted in the **process** context, not the client
  connection, so a client disconnect does not abort it;
- it carries an absolute deadline equal to `[global] shutdown_timeout`, so no
  single operation can delay retirement indefinitely;
- forced retirement (step 4) **cancels the lease before closing the
  generation's resources**, so leased work can never be using a resource that is
  being torn down;
- process shutdown cancels the live generation's leased work and then waits for
  it for at most `[global] shutdown_timeout`, so shutdown stays bounded.

A generation that has begun retiring **refuses** new background work rather than
migrating it to the live generation: the work belongs to the handler tree it
captured. A refused acquisition leaves the in-flight accounting balanced, so a
rejected attempt can never pin a generation open.

The operation name is a closed set of constants, never caller-supplied, so it is
safe in metrics and logs. See [Background revalidation
lifecycle](cache.md#background-revalidation-lifecycle) for the cache-side
contract, including the explicit list of request-context values a refresh
inherits.

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

### Trusted-proxy policy (`[servers.client_address]`)

The per-listener trusted-proxy policy is **hot_reload**, and truthfully so: the
policy is compiled into an immutable value while the new handler tree is
prepared, and the identity middleware that reads it is installed in that same
tree. A malformed prefix therefore fails the build during Prepare and aborts the
reload before Publish — no listener ever serves a half-applied policy. Nothing
about the policy is captured at bind time, so a kept listen address adopts the
new policy with the rest of the handler generation.

### Backend trust (`backend_tls`)

The outbound TLS policy is **hot_reload**, and the class was earned rather than
assumed: every consumer demonstrably rebuilds from the candidate policy.

- The HTTP reverse proxy, native gRPC passthrough and the gRPC-JSON transcoder
  build their clients with the handler generation that owns them, so a new
  generation dials with the new policy and cannot reuse a connection
  established under the old one.
- The resolved policy's **fingerprint is part of the upstream pool's identity**,
  so a changed policy rebuilds the pool and with it the active health-check
  client. That is what closed the last gap: a probe would otherwise have kept
  verifying with the trust it started with.

Because the fingerprint digests file *contents*, rotating a certificate in place
— with no configuration edit at all — changes the pool identity and is applied on
the next reload. Detection and action are now the same thing for this field,
where they were deliberately separated while the consumers were still being
wired.

A malformed policy fails while the candidate is prepared, so the reload aborts
before anything is published rather than leaving a backend unverifiable.

## Reload timeout and deadlines (`[global].reload_timeout`)

A managed admin apply runs under **one absolute transaction deadline**. The
deadline is bound at HTTP admission as `now + reload_timeout` and the *same*
deadline governs the whole transaction — candidate resolution → preflight →
persistence → reload — so no single phase can be slow enough to let the overall
apply run unbounded. Following R15-01 the budget is taken from the **currently
serving** config's `reload_timeout`; a candidate that changes `reload_timeout`
governs only the *next* transaction, never the one that submits it.

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

The same `reload_timeout` also bounds the **pre-persistence** work of an admin
apply — secret resolution, candidate build, and the full preflight gate (parse,
dry-run handler build, TLS assembly, stream/listener bind probes, startup-
resource construction). If any of those side-effect-free phases exceeds the
deadline **before** the candidate is written to disk, the apply is aborted, the
on-disk configuration is left untouched, and the admin API returns
`504 Gateway Timeout` with a `timed_out_phase` field naming the phase that
overran (one of `resolve`, `preflight_validate`, `preflight_tls`,
`preflight_handlers`, `preflight_stream`, `preflight_listeners`, or
`preflight_startup_resources`). Because nothing was persisted this is a distinct
outcome from a validation failure (`400`) and from an in-flight reload that
saved but could not go live (`202 saved_not_live`): a pre-persistence timeout is
fully roll-back-safe. Following R15-01, the budget is taken from the **currently
serving** config's `reload_timeout`, not the candidate's.

The default 10s should accommodate all normal configs; operators may raise it for
very large configs or environments with slow DNS.

## Changes that require a restart

The authoritative list is the Go registry; the complete per-path table is the
generated [configuration lifecycle reference](generated/config-lifecycle.md),
with the same data in [`config-lifecycle.yaml`](config-lifecycle.yaml) and
[`config-lifecycle.json`](generated/config-lifecycle.json).
`lifecycle.DiffConfig` compares the effective value of every registered path
using schema-derived extractors, so a field cannot be added to the registry
without being diffed. The runtime rejects the following categories with `restart_required` at apply
time (admin path) or at swap time (SIGHUP/file-watch):

- **Log format and access-log settings** — the log handler, request access-log
  middleware, Console access-record tail attachment, and sink handles are built
  once at startup. `enabled`, sink selection, file/format, and rotation changes
  therefore require restart. Log *level* is hot-reloadable.
- **ACME issued-domain set / issuer** — frozen when the autocert manager is
  built at startup.

`global.worker_threads` is *not* restart-required: the GOMAXPROCS cap is
applied dynamically on the next successful reload.
- **Listener bind-time settings** — for an address the server already holds,
  the socket is bound once and reused. Changing read/read-header/write/idle
  timeouts, max header bytes, h2c, HTTP/3, or the global connection cap cannot
  rebind the listener live.
- **TLS handshake parameters on an existing listener** — minimum TLS version,
  certificates, and the **mtls** client-authentication bundle (mode, CA bundle,
  SAN allow-list, CRL) are baked into the listener's TLS config. **http3**
  (`enabled` and `alt_svc_max_age`) and `h2c` are likewise decided when the
  address binds.
- **Tracing** — the OpenTelemetry pipeline is wired once at startup.
- **Response cache** — the recertified cache backend (LRU/disk tiers and
  counters) is built once at startup and remains process-scoped across ordinary
  handler reloads. Scalar policy/capacity hot reload is separately planned in
  #92; enable/disable and disk-path replacement remain gated in #93.
- **Egress allow-list** — the outbound dial policy is built once at startup.
- **Admin server** — listener, token, rate limits, history, plugin-upload, and
  audit-log settings are baked in at startup. Token rotation in particular does
  not revoke the old token until restart. The RBAC policy and its
  `admin.rbac.enabled` toggle are the exception: they hot-reload via the
  prepared atomic authentication snapshot (see [config-lifecycle.yaml](config-lifecycle.yaml)).
- **Metrics host label** — set when the Prometheus registry is created.

Adding a brand-new `listen` address is *not* restart-required: the reload binds
it fresh. Only changes to an address the server is already serving are gated.

### L4 stream listeners {:#l4-stream-listeners-are-not-affected}

`[[stream]]` routes (`proxy_pass`, `sni_routes`, `proxy_protocol`,
`connect_timeout`, `idle_timeout`, `max_udp_sessions`, `tls_passthrough`) are
swapped atomically per listener and take effect on the next connection.

`protocol` is also hot-reloadable. An L4 listener is keyed by protocol **and**
address, and TCP and UDP occupy independent port spaces, so switching the
protocol on one numeric address is a transactional remove/add: the candidate
protocol's socket is bound before any live state is mutated, and only then is
the previous listener retired. Established TCP connections and tracked UDP
sessions follow the retired listener's drain boundary — they keep running until
they close or hit `idle_timeout`, and the reload waits for them — while new
traffic arrives on the candidate protocol. If the candidate cannot build its
routes or bind its socket, nothing is mutated and the previous protocol keeps
serving. This is proven by the real-socket matrix in
`internal/stream/protocol_switch_test.go`.

`listen` keys a different listener, which is bound fresh and drained. See
[stream-proxy.md](stream-proxy.md#hot-reload).

## Optimistic concurrency

The admin apply path is guarded by `base_version`: if the live configuration
changed since the edit was prepared, the apply is rejected with `409 Conflict`
so a stale edit cannot clobber a concurrent change. See
[console.md](console.md) for conflict handling.

## Console planned-restart action selection (#81)

The Console obtains lifecycle paths and subsystems from the canonical Go
registry-backed preview. It does not maintain a frontend hot/restart table.
Until pending-restart state is known, the primary action is disabled. A pending
snapshot change forces a fresh preview of the exact ordered operations; a moved
base blocks instead of substituting a newer token.

The three primary labels are **Apply live**, **Save for next restart**, and
**Update staged configuration**. Restart-required and mixed candidates stage the
complete candidate. Listener-bound `rate_limit.max_conns` stages when an
existing listener is retained but may follow a server-authorized hot path when
all affected listeners are new. A `global.reload_timeout` edit uses the
currently active timeout for that transaction; the new value governs later
transactions.
