# Admin runtime hot reload (HR-06B / HR-07A)

Issues #157 and #158 make the non-structural admin runtime operationally hot while keeping admin listener ownership structural. #157 established one immutable request generation for authentication/RBAC, Console mode and plugin-upload policy. #158 extends that same generation to read/write/apply request admission and the shared per-client SSE cap without replacing the mutable limiter/lease manager.

## Scope

The following fields are `hot_reload` when an admin server already exists:

- `admin.console`
- `admin.plugin_upload_enabled`
- `admin.plugin_upload_max_size`
- `admin.plugin_upload_dir`
- `admin.rate_limit_read_per_min`
- `admin.rate_limit_write_per_min`
- `admin.rate_limit_apply_per_min`
- `admin.max_event_conns`

`admin.enabled` and `admin.listen` remain `restart_required`. History and audit resource fields also retain their independently governed lifecycle classifications. A candidate that also changes a structural listener field is not partially hot-applied: canonical lifecycle planning governs the whole candidate. Listener enable/disable/address movement remains the separately gated HR-08 work in #97; durable audit sink dynamics remain #160 and history backend/retention remains #159.

The lifecycle registry in `internal/lifecycle/registry.go` is authoritative; generated lifecycle/reference artifacts must be regenerated rather than edited by hand.

## One immutable generation per request

The live admin server owns one immutable snapshot containing authentication/RBAC state, Console/upload policy and the effective `AdminConfig` from which #158 admission policy is derived. Publish installs a fully prepared snapshot with one atomic pointer swap. The HTTP mux and the limiter manager are process-stable; routes and middleware are not re-registered on reload.

The outer admin handler captures the snapshot exactly once when a request enters the mux and stores that pointer in request context. Secure transport is checked before any credential use. Route admission, authentication, authorization, Console dispatch, upload policy and safe runtime projections then reuse the same captured snapshot.

Consequences:

- a request captured before Publish completes under generation A;
- the first request captured after Publish uses generation B, including B's read/write/apply and SSE-admission policy;
- already admitted work is not retroactively failed by a later policy change;
- a long upload admitted under A keeps A's enable flag, size limit and target directory even if B is published while its body is being read;
- a request cannot authenticate under A and then observe B's Console/upload/limit policy accidentally;
- no grace overlap is added beyond already admitted/in-flight work.

`AuthGeneration` retains its authentication/CAS meaning and is not broadened merely to force limiter retuning. Limiter retuning uses the narrow policy values carried by the same snapshot. The exposed generation digest is diagnostic correlation metadata only and must not become a metric label.

## Prepare, Publish and rollback

All reload entry points converge on the same application prepare/publish path: managed apply/typed patch, raw configuration apply, rollback, SIGHUP and file watch. `PrepareAdminRuntime` runs after configuration resolution and before Publish.

Prepare may construct/validate immutable candidate runtime data. It never iterates limiter clients, retunes token buckets, resets SSE counts or acquires/releases leases. A failed Prepare leaves the old snapshot and all mutable limiter state untouched.

Publish is the existing bounded/no-fail snapshot swap. #158 does not scan or rewrite the client map during Publish. Client buckets retune lazily on their first admission under a different captured policy. Plain immutable policy data needs no retirement phase; already captured old snapshots become unreachable after their requests finish.

For a candidate with uploads enabled and a positive maximum size, Prepare also normalizes and validates the candidate upload directory as documented below.

## Admin request admission (HR-07A)

### Stable state manager

The admin server always owns one non-nil `adminLimiter` manager for its lifetime, even if all three request-rate classes are disabled. It contains only mutable process state:

- per-transport-client read/write/apply token buckets;
- active shared SSE lease counts;
- idle/GC bookkeeping;
- bounded rejection/runtime statistics.

Read/write/apply/SSE policy is not stored as a second live authority. Each admission derives the immutable policy from the request's captured `AdminConfig`.

This split is security-significant: **reload is not quota forgiveness**. Reload never clears all buckets, grants a new full burst, resets active SSE counts or discards accumulated abuse history.

### Canonical rate semantics

For `admin.rate_limit_read_per_min`, `admin.rate_limit_write_per_min` and `admin.rate_limit_apply_per_min`:

```text
0        = omitted/default after canonicalization
negative = disabled
positive = explicit requests-per-minute limit
```

The canonical defaults are read 240/min, write 60/min and apply 30/min. The three buckets are independent: changing one class does not reset another.

The authoritative route catalogue supplies the admission context; no independent registered-path switch is maintained. Safe methods use the read budget. Configuration assessment/mutation permissions (`config:write`, `config:apply`, rollback) use the stricter apply budget, including external validate/plan/apply/patch/rollback/adoption equivalents. Other mutations use write. The catch-all/root catalogue entry preserves conservative treatment for unmatched requests, so an unknown path does not become an unlimited existence/authentication oracle.

### State-preserving transitions

Jul uses `golang.org/x/time/rate` and performs a retune plus admission under manager synchronization with one explicit timestamp.

- **finite → tighter:** the existing limiter survives; new limit/burst parameters are installed and the first new-policy reservation advances/clamps usable capacity to the new burst before admission;
- **finite → looser:** accumulated state/timeline survives; the bucket is not replaced with a newly full limiter;
- **finite → disabled:** future admissions bypass that class while the limiter object/history is retained;
- **disabled → finite:** a previously finite bucket is reactivated from its retained state/timeline rather than replaced, so reload cannot manufacture a forgiveness burst; elapsed real time may provide legitimate capacity but never above the new burst.

A denied delayed reservation is cancelled at the same timestamp. `Retry-After` is the deterministic ceiling of the reservation delay in integer seconds, with a minimum of 1 for a rate refusal.

### HTTP denial contract

The stable route admission wrapper runs after secure-transport approval and before authentication/RBAC. For supported external `/api/v1` operations, external contract/request-ID context is established before admission so an early refusal still returns the versioned `rate_limited` envelope with:

- HTTP 429;
- `Retry-After`;
- `details.retry_after_seconds` equal to the header value;
- a server-minted `request_id`.

Internal Console/admin routes retain their existing internal 429 compatibility shape. Rate limiting never depends on bearer-token contents or authorization outcome and does not expose permission/resource-existence information.

The transport peer IP from `RemoteAddr` remains the client key. Untrusted `Forwarded`/`X-Forwarded-For` headers are not limiter keys. IP/principal/token/request ID/raw path/policy generation are never metric labels.

Rejection logging is bounded by a stable rate class rather than emitting an attacker-controlled log entry for every denial.

## Shared SSE admission (HR-07A)

`admin.max_event_conns` is the per-transport-client concurrent cap shared by:

- `/api/events`;
- `/api/observability/logs/stream`.

Canonical public semantics are:

```text
0        = omitted/default after canonicalization
positive = per-client cap
negative = invalid configuration
```

The canonical default is 4. #158 does **not** introduce `0 = unlimited` or negative/unlimited SSE semantics.

An SSE connection checks the cap from the request's captured generation at admission and then owns an idempotent lease until the handler finishes. Policy reload does not replace the manager or invalidate the release closure.

If a client has four existing streams and the cap changes 4 → 2, all four remain connected. New connections are denied until its active count falls below 2; when it has one active stream, one new stream may be accepted. Increasing the cap affects the next admission immediately. #158 never selects/terminates existing streams merely because the cap decreased.

Release is exactly-once through normal handler return, client cancellation, write/subscription failure, hub/log-stream close and server shutdown cleanup paths. A client with active SSE leases is not idle-evicted. The GC scan is amortized and reload itself never triggers eviction.

## Console mode transitions

A full build constructs the embedded Console handler once and dispatches to it only when the request's captured generation has `admin.console = true`. With `console = false`, `/`/`/ui`/`/config` use the legacy/fallback admin pages while authenticated APIs remain registered on the same listener. A full build therefore supports `on → off → on` without listener restart or mux replacement.

A lean build has no embedded Console assets. `console = true` remains a valid configured value but cannot make the Console effective; runtime status reports `console_compiled=false` and `console_effective=false`, and the fallback UI continues to serve.

The Console settings drawer requires explicit acknowledgement before disabling the web Console and uses the normal lifecycle preview/apply path rather than direct mutation.

## Plugin-upload policy transitions

### Admission

When uploads are disabled, `POST /api/plugins/upload` returns `403` before reading or parsing the multipart request body. The canonical parser materializes an omitted `plugin_upload_enabled` as `false`. A non-positive `plugin_upload_max_size` also disables admission. A request captures its maximum size before body processing.

### Storage and file safety

The configured upload directory is normalized once into the prepared generation. An admitted request opens it using `os.Root`, verifies that the pre-open path, opened root and post-open path all identify the same directory, and confines descendant operations to that root.

Writes preserve the existing contract: temporary files are unique/owner-only, data is synced/closed before finalization, an existing destination must be regular, replacement is root-confined atomic rename, and failed/oversized requests do not leave a final plugin file. A directory change does not migrate/delete old files; in-flight requests finish against their captured generation.

## Safe projections and diagnostics

Authenticated runtime overview exposes bounded effective policy and aggregate mutable state. #158 adds:

- configured read/write/apply rates and configured per-client SSE cap from the captured generation;
- tracked limiter client count;
- total active SSE connections;
- clients with active SSE connections;
- clients currently above the captured per-client cap;
- maximum active SSE connections held by any one client;
- bounded read/write/apply/SSE rejection counters.

The total active SSE count is never compared directly with the per-client cap. `over_cap_clients` is derived by sampling manager state against the request's captured cap under synchronization. No client IP list is exposed.

Runtime status continues to expose Console/upload facts and bounded upload failure categories without secrets, raw filesystem errors or configured upload paths. The `config:read` settings projection contains only the typed fields needed by the admin runtime editor.

## Typed settings operations

The Console uses narrow typed operations rather than a monolithic `admin_set`:

- `admin_console_set { enabled }`
- `admin_plugin_upload_set { plugin_upload: { enabled?, max_size_mb?, directory? } }`
- `admin_limits_set { admin_limits: { read_per_min?, write_per_min?, apply_per_min?, max_event_conns? } }`

`admin_limits_set` is sparse and requires at least one field. Request-rate values preserve canonical negative/zero/positive semantics. `max_event_conns` cannot be negative and the UI deliberately exposes no unlimited SSE option. Preview/audit summaries name changed fields rather than client state or secrets.

The UI explicitly explains that tightening rate limits preserves accumulated quotas and applies to new admissions after Publish. When lowering the SSE cap it warns that existing event/log streams stay connected and over-cap clients cannot open another until their active count falls below the new cap.

All typed operations pass through the same parser/validator, reachability/self-lockout checks, lifecycle preview, persistence, prepare/publish coordinator and apply ledger as other managed mutations.

## Failure and concurrency model

- Invalid configuration fails before runtime preparation.
- Unusable candidate upload storage fails Prepare; no live state is published.
- A Publish race cannot mix admin generations inside one request.
- Limiter synchronization covers client-map mutation, lazy retune, reservation/cancellation, SSE counts/releases, GC and aggregate statistics; the lock is released before downstream handlers run.
- Publish itself does not take the limiter lock or iterate tracked clients.
- Cancellation/oversize before upload final rename leaves no final plugin artifact from that request.
- Disabling the Console does not stop the admin server or its API.
- Changing `admin.enabled` or `admin.listen` remains restart-bound.

## Known limitations

Admin rate/SSE state is in-memory and process-local; #158 does not add fleet-wide/distributed coordination. The transport peer IP remains the admission identity. Existing SSE sessions are not forcibly drained when a cap is reduced. Mutable rate/SSE state is intentionally preserved across configuration reloads but resets on an actual process restart.

## Verification expectations

Changes to this area should exercise at least:

- finite → tighter, finite → looser, finite → disabled and disabled → finite request-rate transitions with a controlled clock;
- exact `x/time/rate` retune/reservation/cancellation behavior and deterministic `Retry-After`;
- independent read/write/apply state;
- authoritative route-catalog coverage including external v1 validate/plan/apply/patch/rollback/adoption and unknown fallback;
- external typed `rate_limited` 429 and internal compatibility response;
- SSE below/at/above-cap, increase/decrease, existing-stream preservation, exactly-once release, shutdown and active-client GC retention;
- status math separating process totals from per-client cap;
- raw managed apply, typed patch/settings, rollback, SIGHUP and file-watch convergence;
- mixed restart-bound `admin.enabled/listen` candidates not partially publishing limit fields;
- failed Prepare preserving snapshot, token buckets and SSE leases;
- lifecycle generation promoting exactly the four #158 fields while #159/#160/#97-owned fields retain their classifications;
- race/leak/load, full/lean, Console frontend and real-server/API/browser E2E gates in CI;
- >=90% meaningful #158-owned Go statement coverage and >=90% statements/lines/functions/branches for #158-owned frontend logic.

See `docs/console.md`, `docs/plugins.md`, `docs/reload-semantics.md`, `docs/security-posture.md`, `docs/known-limitations.md` and the generated configuration/lifecycle reference for corresponding user-facing surfaces.
