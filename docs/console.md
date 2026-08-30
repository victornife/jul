# Jul.IA Console

> **Maturity and API boundary:** the released embedded Console foundation is GA. Newer authority, route-policy and resilience panels on current `main` retain their own maturity rows. Existing unversioned `/api/*` routes are Console-internal unless #150 explicitly publishes them in the supported external `/api/v1` contract.

The Console is a loopback-bound web control plane for operating a running
Jul.IA server: a live metrics dashboard, a runtime-status overview of which
capabilities are active, upstream health, certificate inventory, safe
configuration editing with version history and one-click rollback, and a setup
wizard. It ships **inside the single binary** (no external assets, no Node
build) and is gated by the `console` build tag.

## Enabling the console

The console is served by the [admin listener](../docs/observability.md).
Enable admin, keep it on loopback, and set a token:

```toml
[admin]
enabled = true
listen  = "127.0.0.1:9090"
token   = "change-me"          # sent as: Authorization: Bearer change-me
# console      = true                          # default when admin is enabled
# history_dir  = "./jul-data/config-history"   # rollback snapshots
# history_keep = 50                            # snapshot retention
```

Build with the tag and browse to the admin root:

```bash
go build -tags console -o jul ./cmd/jul
./jul -config server.toml
# open http://127.0.0.1:9090/ — the console prompts for the admin token on the
# first request and stores it in this tab's session storage
```

When `[admin].token` is set, the console shows a token prompt the first time an
API call is rejected with 401; paste the token there. It is kept in
sessionStorage (cleared when the tab closes) and sent as `Authorization: Bearer
…` — never in the URL.

Binaries built **without** `-tags console` serve the basic configuration page at
the root instead; the JSON APIs below that do not require the tag (for example
`/api/stats`, `/api/upstreams`) remain available for scripting.

> **Keep the admin listener on loopback.** It exposes operational controls. If
> you must reach it remotely, front it with your own authenticated tunnel rather
> than binding it to a public address. `jul lint` warns when admin is bound
> off-loopback without a token.

## Panels

### Loading and failure states

Every panel that loads data renders one of three states: a brief *loading* line
while the first request is in flight, the panel content on success, or a **typed
failure state** when the request fails. Rather than a single generic "Failed to
load X" for every cause, the console classifies the failure and tells the
operator what to do:

| Cause | What you see | Why / what to do |
| --- | --- | --- |
| **401** — token missing or no longer valid | "Session expired" (the console-wide token prompt also opens) | Re-enter the admin token |
| **403** — authenticated but not permitted | "Access denied" | Use a token with admin access |
| **404** — endpoint/feature absent | "Not available" | The capability may be disabled in this build or configuration; enable it or rebuild with its tag |
| **409** — stale read | "Out of date" (with the server's reason) | Retry — the panel refetches the latest state |
| **429** — rate-limited | "Too many requests" | Wait the suggested time, then retry. When the response carries a `Retry-After` header the message names the exact wait ("Wait N seconds, then retry") |
| **5xx** — server error | "Server error" (with the server's detail) | Retry; check the server logs |
| **network** — offline, DNS, reset | "Can't reach the server" | Check the server is running and your connection is stable |

Retryable failures (409, 429, 5xx, network) show a **Retry** button that
refetches in place; re-authentication, permission, and availability errors do
not, because retrying the identical request cannot succeed. A 401 from any panel
also raises the console-wide token prompt described above.

### Identity and permission gating

The app chrome shows the **current principal and role** the console is acting as,
read from `GET /api/admin/me` (which returns only server-derived, secret-free
identity — principal, role, public token ID, resolved permissions, and whether
the credential is the legacy shared token). When RBAC is enabled, the console
**gates controls proactively**: actions the current role cannot perform —
configuration apply, history rollback, plugin upload, cache purge, and audit
export — are disabled with a short note explaining which permission is required,
instead of letting the operator click through to a guaranteed 403.

Gating is a UX hint only; the **server remains authoritative** and authorizes
every request. It fails open until the identity is known, so the console is never
blanked during load or when running without RBAC, and a 403 is handled inline
without re-triggering the token prompt. When the credential is rejected (401) the
cached identity is dropped so a stale permission set never lingers.


### Overview

The landing page of the console and the primary monitoring surface. Answers
"is anything wrong?" at a glance and provides a direct path to the relevant
configuration panel when something needs attention. Backed by
`GET /api/overview`, polled every two seconds.

#### Health band

A row of chips at the top of the page gives a coarse health signal before
scrolling into the detail below. Each chip shows a label, current value, and a
tone (green / yellow / red):

| Chip | OK | Warn | Down |
| --- | --- | --- | --- |
| **Traffic** | > 0 req/s | — | 0 req/s (idle) |
| **Errors (5xx)** | 0% | > 0% | ≥ 5% |
| **Latency p95** | < 250 ms | 250–999 ms | ≥ 1 000 ms |
| **Backends** | all healthy | any unhealthy | — |
| **Certificates** | all valid | any expiring ≤ 7 d | any expired |

The **Backends** and **Certificates** chips are clickable and navigate to the
Apps and TLS panels respectively.

#### Live Traffic

Numeric metric cards show the current snapshot: uptime, req/s, in-flight
requests, connections, latency (avg/p50/p95/p99), error rate, status-class
counts (2xx / 3xx / 4xx / 5xx), cache hit ratio, cache events, and HTTP method
breakdown. All values are point-in-time from the most recent poll.

**2-Minute Trends** shows six sparkline cards — one per tracked metric —
covering the last 60 samples (~2 minutes at 2 s polling). Each card is
keyboard-reachable and can be expanded.

#### Expanded chart view

Click or press Enter on any sparkline card to open the expanded chart:

- **Metric description** — one sentence explaining what the metric measures and
  why it matters operationally.
- **Axis labels** — X: Time; Y: metric name and unit (e.g. "P95 latency (ms)").
- **Time range** — exact start and end wall-clock timestamps and sample count.
- **Interactive chart** — hover or use ← / → arrow keys to see the precise
  timestamp and formatted value at each point. A hairline and circle marker
  track the active point. Warn/critical threshold lines are drawn as dashed
  horizontal rules where applicable. Escape closes the chart and returns focus
  to the trigger.
- **Summary** — current value, change versus the start of the window, trend
  direction (▲ Rising / ▼ Falling / → Stable), volatility (low / medium /
  high), distribution (min / avg / median / p95 / max), spike and drop counts
  (values beyond ±2 σ), and a health status (● Healthy / ● Degraded /
  ● Critical / ○ Unknown). When fewer than ten samples are available, trend and
  volatility claims are suppressed and the panel states "Insufficient data for
  trend analysis" to avoid misleading conclusions from noise.
- **Export CSV** — copies `timestamp_ms,timestamp_local,value` rows to the
  clipboard.
- **Configure →** — where the metric has a direct configuration destination
  (for example, Cache Hit Ratio links to /traffic), a footer action navigates
  there.

#### Capabilities & Configuration

Shows which features are active in the running configuration, grouped into
Traffic, Security, Protocols, Upstreams, Observability, and Extensibility. Each
row shows a dual-encoded status indicator (coloured dot **and** the words
"active" / "inactive"), the feature name, any available detail (counts or kinds
— never tokens or credentials), and for features with a known configuration
destination, a visible action button that navigates directly to the relevant
panel:

| Feature (name or group) | Navigates to |
| --- | --- |
| Response cache, Rate limiting, Compression | `/traffic` |
| Access control (auth), Web application firewall | `/security` |
| TLS, Automatic HTTPS (ACME), Mutual TLS | `/tls` |
| Upstream pools, Active health checks, Service discovery | `/apps` |
| WASM plugins | `/plugins` |
| L4 stream proxy | `/streams` |
| gRPC transcoding | `/transcode` |
| Observability group | `/operations` |

Features not in this list render as informational rows with no action button.

#### Traffic Sources

When traffic-sources data is available, also shows a CORS summary (preflight,
same-origin, cross-origin counts) and top-8 tables for client hosts, origins,
and referer hosts.

### Status

The capability status grid (Traffic, Security, Protocols, Upstreams,
Observability, Extensibility) is now embedded in the **Overview** panel's
*Capabilities & Configuration* section, where each row also carries a direct
navigation action to the relevant configuration panel. See
[Overview → Capabilities & Configuration](#capabilities--configuration) above.
The underlying data is derived from the parsed running configuration and backed
by `GET /api/overview`.

### Upstreams

Lists each named `[[upstreams]]` pool with its load-balancing strategy and a row
per backend showing live state, weight, and in-flight requests.

Backend state is a closed enum, not a boolean. A backend can be out of rotation
for reasons that have nothing to do with health checking — the circuit breaker
may have tripped it, or it may simply be full — and collapsing those into
"unhealthy" hid the distinction operators need during an incident:

| `state` | Dot | Meaning |
| --- | --- | --- |
| `available` | green | Serving. Eligible for selection. |
| `circuit_open` | red | Tripped by consecutive live-traffic failures. Not selected until `fail_timeout` elapses. |
| `circuit_half_open` | amber | Recovering. A limited number of probe requests are allowed through to test it. |
| `health_unhealthy` | red | Ejected by an active health check. |
| `at_capacity` | amber | Healthy, but already at `max_active_per_backend`. |
| *(absent)* | grey | **Unknown** — no live status yet, e.g. the pool is configured but not routed. |

Unknown is deliberately distinct from failing: a backend nobody has observed is
not the same as one observed to be down.

Each pool also carries a `verdict` — `healthy`, `degraded`, `down`, or
`unknown` — rolled up from its backends. It is computed by the server rather
than in the browser so the Console and any API client cannot disagree during an
incident ([ADR 0014](adr/0014-operability-surfaces.md)). A pool is `degraded`
when at least one backend is available and at least one is not, so a single
healthy backend never masks a tripped one.

The Apps list header shows a plain count of the same states, e.g. `2/3
available`, for a glance at a page that lists many pools at once. That count is
arithmetic over states the server already classified — not a second, competing
verdict computation — and it is expected to always agree with the pool's
`verdict`; the authoritative degraded/healthy/down decision stays server-side.

Backed by `GET /api/apps`.

For a single pool, `GET /api/upstreams/{name}/resilience` answers the questions
the Console summary cannot: the limits actually in force, which configuration
key supplied each of them, the live admission and connection counts, the retry
budget, and per backend the consecutive-failure count, the cooldown deadline
(`open_until`) and how many half-open probes are left. It returns one entry per
scheme, since a name can serve both an http and an https pool.

This is the surface the per-backend metric label was withdrawn in favour of. An
address costs one JSON field in a response queried on demand; the same address
as a Prometheus label cost a time series per pod, kept for the life of the
process.

The `sources` map answers "which spelling won": `max_fails` and `fail_timeout`
can each be written at the upstream level or in `[upstreams.resilience]`, and
knowing the effective value is 3 does not tell an operator whether the change
they just made is the one taking effect. Values are `resilience`, `upstream` or
`default`.

An unknown pool is a `404`. A pool whose backends are all down is a `200` with
`verdict: "down"` — the two are deliberately different answers, because a pool
that has vanished and a pool that is failing call for opposite responses.

### Certificates

Shows every TLS certificate configured on a server block:

- **File certificates** are parsed for subject, issuer, SANs, and an expiry
  countdown (red within 14 days, amber within 30).
- **ACME-managed** certificates are marked as auto-renewing; their live expiry
  is exported through the `jul_tls_cert_expiry_seconds` metric.

No private-key material is ever returned. Backed by `GET /api/certs`.

### History & rollback

Every time you save a configuration change (via the settings form, the raw TOML
editor, or the wizard), the **previous** configuration is snapshotted to
`history_dir` as a timestamped `.toml` file before the new one takes effect.

The History panel lists snapshots newest-first. **Preview** shows the stored
TOML; **Roll back** re-applies it through the same validated write path used by
the editor, which hot-reloads on success. Because a rollback is also a write, it
snapshots the pre-rollback configuration first — so you can always undo an undo.

Snapshots are pruned to `history_keep` (default 50). Endpoints:

| Method & path | Purpose |
| --- | --- |
| `GET /api/history` | List snapshots (`id`, `time`, `size`), newest first |
| `GET /api/history/get?id=<id>` | Raw TOML of one snapshot |
| `POST /api/history/rollback` | Body `{"id":"<id>"}` — re-apply + reload |

### Setup wizard

Generates a starter configuration without hand-writing TOML:

- **Serve a directory** — static file server for a folder.
- **Reverse-proxy a target** — proxy all requests to `http://host:port`,
  `host:port`, or `:port`.
- **Put an app behind Jul** — create a load-balanced upstream pool from one or
  more `host:port` backends plus a reverse-proxy route that mounts it. A
  framework **preset** (Express/Node, Apollo, FastAPI, Django/Flask, Go, gRPC,
  or generic) seeds friendly defaults for the load-balancing strategy and the
  active health-check path; the operator can edit everything before applying.
  Presets only influence copy and defaults — they create no framework-specific
  magic.

`POST /api/wizard` (and the v2 alias `POST /api/wizard/generate`) returns the
generated TOML for review (it is validated first, so the wizard never proposes a
config the editor would reject). Click **Apply & reload** to write it through
`POST /api/config/raw`, which validates, snapshots the current config, persists,
and hot-reloads.

### Search & discovery

`GET /api/search?q=<query>&type=<routes|apps|all>` ranks routes and apps by
match quality and reflects the relationships between them — which upstream a
route targets, which routes use an app, and which apps are unused — so large
configurations stay navigable. Results carry only abstract labels (paths,
action kinds, pool names, counts); never tokens, certificate material, or
credentials. The Search panel debounces the query and persists it across
sessions.

### Structured diff

`POST /api/config/diff` returns a human-auditable, structured before/after
report used by the apply flow. Beyond server add/remove and listen/TLS changes,
it explains the operational consequences of changes to:

- **Routes/locations** — action and target changes, plus auth, cache,
  rate-limit, body-size, and per-route proxy-timeout toggles, with warnings
  (e.g. caching an authenticated route, disabling auth, retargeting traffic).
- **TLS** — enable/disable, certificate/key changes, minimum-version changes
  (warning when weakened), ACME enable/disable and CA/challenge changes, and
  mutual-TLS mode/CA/CRL changes.
- **Upstreams** — strategy, backend add/remove and weight changes, `max_fails`
  and `fail_timeout` (retry/passive-health) changes, active-health-check and
  discovery toggles.
- **Server timeouts and body limits**, and the global **cache**,
  **compression**, and **rate-limit** blocks.

### Atomic structured patch assessment

The structured editor assesses exactly the ordered operations it will later
apply. `POST /api/config/patch/preview`, `POST /api/config/patch/candidate`, and
`POST /api/config/patch/apply` all call the same side-effect-free batch executor:
it binds one authoritative editable baseline, verifies `base_version`, clones the
configuration, dispatches each operation in request order, canonicalizes and
re-parses the complete candidate, resolves references once, validates the full
candidate, computes the human diff, and asks the closed-world lifecycle registry
for the whole-candidate verdict. Preview never persists, reloads, publishes
handlers, or mutates a loader-owned configuration pointer.

The normal preview response is deliberately secret-safe. It contains the legacy
combined `summary` string, an ordered `operation_summaries` array with zero-based
`op_index`, the canonical `base_version`, validation issues, the full diff, and a
value-free `lifecycle` object. Lifecycle entries contain only canonical schema
paths, closed class/subsystem names, and fixed explanations—never configured
values or resolved secrets. `can_apply_hot` is false whenever any effective
change in the complete candidate cannot take effect live. `can_stage_restart` is
false for an invalid/reserved candidate or an inconsistent planned-restart store.

Raw candidate TOML is available only from `POST /api/config/patch/candidate`,
which requires `config:raw`; `/patch` and `/patch/preview` never include a
`candidate` field. This separation matters because canonical TOML may contain
legacy admin tokens, RBAC principal tokens, discovery credentials, or literal
secret values. The candidate route returns the same assessment plus the
unresolved canonical TOML so privileged source review cannot drift from the
previewed operations.

If operation *N* rejects the batch, the server returns HTTP 400 with the exact
zero-based `op_index`, the operation discriminator in `op`, and structured
validation details. No partial diff or persistent mutation is produced. A stale
non-empty `base_version` returns HTTP 409 with `current_version` before any
operation runs; an empty version remains the explicit force mode used by legacy
callers. `POST /api/config/patch` remains the one-operation force-preview
compatibility wrapper over the same executor.

### Validation errors

When a draft fails validation — through `POST /api/config/validate`, an apply
preflight, or a patch preview — the console renders each problem as a structured
issue instead of one opaque blob. The config validator joins every problem it
finds with newlines; the console splits them back into individual issues, each
carrying:

- a **code** that themes the message (e.g. `unknown_upstream`, `tls_misconfig`,
  or `unknown` for an unmapped message);
- a **path** locating the offending block (e.g. `servers[0].locations[1]`) so an
  error stays findable in a large config. Bare subsystem prefixes such as `waf:`
  or `auth:` are surfaced as a code, not a path;
- a human **summary** (and optional **detail**) with actionable guidance.

The Configuration panel shows the path as a chip in front of each message. The
mapping is presentation-only — the canonical configuration validator remains
the source of truth for what is valid. Unknown keys and invalid known values use
the same errors across validate, preview, hot apply, planned-restart staging,
rollback, startup, and CLI checks. An invalid value is never reclassified as
`restart_required`, never staged, and never written. The editor keeps the draft
visible so the operator can correct it; the currently serving and persisted
configuration remain unchanged.

### Concurrent edits (optimistic concurrency)

Both write paths carry a `base_version` — a short fingerprint of the
configuration the edit was prepared against — so a second operator (or a second
browser tab) cannot silently overwrite a change applied in between:

- The **raw TOML editor** (`POST /api/config/apply?base_version=<v>`) and the
  **structured quick edits** (`POST /api/config/patch/apply`) both reject a
  stale write with **HTTP 409 Conflict** when the live config changed since the
  edit was prepared. `GET /api/config`, the one-op compatibility preview
  (`POST /api/config/patch`), and the ordered batch preview
  (`POST /api/config/patch/preview`) return the current `base_version`. The
  fingerprint is computed over the canonical, comment-insensitive form, so a
  `base_version` is interchangeable between the raw and structured paths.
- On a 409 the console shows a conflict banner with **Reload latest config**,
  which discards the stale draft and re-seeds the editor (both the text and the
  `base_version`) from the current configuration.
- A successful apply returns the new `version`, so a follow-up edit in the same
  session does not trip a spurious conflict.
- Sending an empty/absent `base_version` skips the check (an explicit
  force-apply); the console always sends it.
- **One exception, and it is not optional.** A patch that selects a route with
  `match_ordinal` (see [Route tester](#route-tester)) is rejected with 409 when
  `base_version` is absent *or* stale. An ordinal is meaningful only relative to
  one revision — inserting a same-path route above the target shifts every later
  ordinal — so a force-applied ordinal patch would edit a route the operator
  never previewed.

### Route tester

`POST /api/routes/test` resolves a synthetic request against the running
configuration using the router's own selection, so the answer cannot drift from
what the server would actually do. It never dials an upstream, runs a handler or
mutates anything.

The request carries `method`, `path`, `host` and, optionally:

| Field | Purpose |
| --- | --- |
| `headers` | a `{name: value}` map — the convenient single-value form |
| `header_values` | an ordered `[{name, value}]` list appended to `headers`, so two field lines of the same name are expressible |
| `raw_query` | the query string exactly as it appears after `?`, so repeated keys, percent-encoding, `+` and malformed escapes survive. It is *not* derived by splitting `path`, where a `?` stays a literal |

The result names every candidate the path produced, in the order the router
visited them: its `match_type`, `match`, `tier`, `match_ordinal`, whether it was
`selected`, and — for each rejected one — the exact configuration path of the
predicate that rejected it (`match.methods`, `match.headers[1]`, `match.query[0]`).

This is the only place an operator can see a predicate mismatch. One is an
ordinary routing outcome that happens on every request to a predicate-bearing
path, so it is never logged per request, and no predicate value ever becomes a
metric label.

`match_ordinal` is a revision-relative selector, not an identity. It is safe to
feed straight back into a typed patch together with the `base_version` the same
session read; it must not be stored or used to correlate a route across
revisions.

### Match predicates, response headers and CORS

A route projection (`GET /api/routes`) reports a location's method predicate
in full (`methods`, never operator-sensitive) and a compact, value-free
summary of its header/query predicates (`predicates`), whether it has a
response-header policy (`response_headers: true`), and its CORS policy, when
configured (`cors: {enabled, allowed_origins, allowed_methods, allowed_headers,
exposed_headers, allow_credentials, max_age}`) — the operator's configured
values, not the compiled runtime form. Individual header/query predicate values
and `response_headers` operation values are not projected: they may be
operator-sensitive, and the route tester above already explains a rejection in
full for a specific request. A change to any of them surfaces in the structured
diff — predicates through the route's re-keyed add/remove pair (editing a
predicate set changes the route's identity, ADR 0018 §14), response headers and
CORS through their own named, value-free modification entries.

Three guided drawers in the route detail view cover the typed patch API
(`location_set_predicates`/`_clear_predicates`,
`location_response_headers_set`/`_clear`, `location_cors_set`/`_clear`), each
replacing what it edits wholesale — the same convention the WAF and auth
editors use. Because header/query predicate values and response-header
operation values are not projected, opening either editor on a route that
already has them starts from a blank form and says so explicitly: saving
replaces the whole set, so re-add anything the route should keep. The CORS
editor has no such gap — every field is projected, so it seeds and round-trips
completely.

### Restart-required changes

A few settings are fixed when the process starts and cannot take effect on a hot
apply. The clearest case is **automatic HTTPS (ACME)**: the issued-domain set and
issuer are frozen when the autocert manager is built at startup, so enabling
ACME, adding or removing domains, or changing the issuer cannot be hot-applied.
The same applies to other startup-bound settings:

- **Listener bind-time settings** on an address the server already holds — the
  global max-connections limit, the listener read/read-header/write/idle
  timeouts, the max header bytes, and toggling HTTP/3 or h2c. These come from
  the first server block on each `listen` address and are fixed when the socket
  is bound; adding a *new* listen address is still hot-applied.
- **TLS handshake parameters** on an existing listener — the minimum TLS
  version and the mutual-TLS client-auth policy (mode, CAs, allowed SANs, CRLs).
- **Tracing** — the OpenTelemetry tracer is wired once at startup, so changing
  the endpoint or sample ratio, or enabling/disabling it, requires a restart.

When an apply is otherwise valid but changes such a setting, the write path
**refuses it without persisting anything** and returns **HTTP 409** with
`restart_required: true`. The console shows a *restart required* notice — distinct
from the optimistic-concurrency conflict above — explaining that nothing was
saved and that the operator must edit the configuration file and restart the
server for the change to take effect. Removing ACME is not restart-required (the
listener swaps to static certificates on the next reload). See
[tls-acme.md](tls-acme.md#restart-required-acme-changes) and
[reload-semantics.md](reload-semantics.md).

### Apply outcomes

An apply is never just "success" or "error": the reload model has an
*applied vs. serving* gap (see [reload semantics](reload-semantics.md)), and the
L4 stream reload is asynchronous, so the console folds the raw apply signals —
whether the write was accepted, whether a reload is still pending, the polled
stream-reload status, and any restart-required rejection — into **one explicit,
severity-tagged outcome banner**. Every apply resolves to exactly one of four
outcomes so an operator never has to infer what actually happened:

- **Applied and live** *(success)* — the write was accepted and the running
  server has been observed serving the new configuration. Nothing further is
  required.
- **Applied — runtime reloading** *(info)* — the write was accepted and saved,
  but the hot reload has not yet been confirmed live. The banner clears itself
  to *Applied and live* once a runtime snapshot confirms the change; this is the
  normal transient state immediately after an apply.
- **Applied with a degraded subsystem** *(warning)* — the HTTP configuration was
  accepted, but an **asynchronous subsystem reload failed** — most commonly the
  L4 stream (`[[stream]]`) proxy, whose reload runs after the response is sent.
  The banner names the failed subsystem and its error so the operator can act,
  rather than the failure being buried in the overview.
- **Restart required — not applied** *(blocked)* — the change touches a
  startup-bound setting (see *Restart-required changes* above); **nothing was
  saved** and the operator must edit the file and restart. This is the only
  outcome that is blocking, and it is styled distinctly from the others.

The banner reports success and info outcomes with a capabilities tally (how many
feature groups are active) and, for the two non-live outcomes, surfaces the
actionable detail inline. See [reload-semantics.md](reload-semantics.md) for the
underlying *applied vs. serving* model that motivates the distinction.

### Configuration authority banner

`GET /api/runtime/overview` carries an `authority` object
(`config_authority`, `config_authority_source`, `config_state`, and — in
managed mode — `drift`/`drift_detected_at`) reflecting ADR 0019's
`[global].config_authority` field. The Console renders it as a persistent,
global banner, separate from the per-apply outcome banner above:

- **`file_owned`** (the default) — the banner explains that an external file
  or GitOps pipeline owns the configuration, names `config_authority_source`
  so an operator can tell "declared" from "defaulted," and every write
  control (raw/settings/patch editors, the trust-policy patch, stage/discard,
  history rollback) is disabled with the server's own reason rather than
  hidden — the Console still shows *why*, and preview/validate/diff/lint/
  history-list/export stay fully available.
- **`managed`, no drift** — editors behave exactly as documented elsewhere in
  this file.
- **`managed`, drift detected** — the banner turns into a blocking notice
  naming the drifted-since timestamp and offers the **adopt external file**
  flow (`GET`/`POST /api/config/adopt-external`), which previews a diff
  against the last managed configuration and requires explicit confirmation
  before resuming ownership. Ordinary write controls stay disabled until the
  operator adopts or restores the file to match the managed baseline.
- **`managed_unadopted`** — a fresh managed boot with no established
  baseline; the banner asks for the same one-time adoption before any
  ordinary write is allowed.
- **`managed_inconsistent`** — a bounded reason (e.g. a corrupted or missing
  baseline marker) is shown verbatim; this generally means investigating
  storage rather than editing the configuration.

**Server denial is authoritative; the banner and disabled controls are a
convenience.** Every mutating endpoint independently refuses the operation
with `409 config_authority_read_only` in `file_owned` mode, so a client that
bypasses the Console gains nothing.

### Listener changes

Adding or removing a `listen` address **is** hot-applied: the reload binds new
listeners and drains removed ones without a restart. To keep that honest, the
apply first **probes every newly introduced address** — a quick bind-and-close —
so an apply that adds an unbindable port (already in use by another process,
invalid, or privileged without permission) is rejected before it is persisted,
rather than being recorded as applied while the new listener silently never
serves. Addresses the running server already holds are not probed (that would
always fail), and removals introduce nothing to probe. Newly added `[[stream]]`
(L4 TCP/UDP) listeners are probed the same way, so the apply is equally truthful
for stream deployments. See [reload semantics](reload-semantics.md) for the full
*applied vs serving* model.

### Admin self-lockout guard

The raw editor can edit the `[admin]` block itself, which means a single apply
could change how you reach the console — and unlike any other change, you cannot
roll it back from a console you can no longer reach. To prevent that, an apply
that would **disable the admin interface, move its listen address, rotate its
token, or disable the web console** is held with **HTTP 409** and
`admin_change: true` the first time, listing exactly what would change. Nothing
is written. The console shows a confirmation dialog enumerating the changes; on
confirm it re-applies with `?confirm_admin=true` and the write proceeds. An apply
that leaves the `[admin]` block unchanged is never gated, and changes that only
*widen* access (enabling admin or the console) are not gated either.

### Guided editors — scope (v2)

The Routes panel creates HTTP locations through one **ordered structured
patch batch** rather than by appending browser-generated TOML. The editor has
explicit modes:

- **Add to an existing server** selects the complete server identity — exact
  `listen` plus an order-independent, case-sensitive `server_names` set — and
  emits `location_add` first. It never emits `server_add` in this mode.
- **Create a new server** rejects an exact identity collision and emits
  `server_add`, then `location_add`, then optional modifiers in deterministic
  order: auth, cache, rate limit, and middleware-plugin attachments. Plugin
  attachment order is the operator's middleware order.

Server-name order is canonicalized only for keys and display; comparison remains
case-sensitive. Browser-session route restoration stores that full server
identity plus the location's match type and path. A legacy listen-only selection
is restored only when the listener is unambiguous, so sibling virtual hosts on
one listener cannot be selected accidentally.

The creator supports only actions it can represent faithfully: static files,
HTTP reverse proxy, redirect, deny, and fixed-status return. Native gRPC,
gRPC-JSON transcoding, FastCGI, uWSGI, and handler-plugin actions use their
protocol-specific editor or the raw configuration escape hatch. The Console
never silently substitutes an HTTP proxy for an unsupported protocol.

Every route-side one-operation edit and every creation batch uses the same
`POST /api/config/patch/preview` handoff. The resulting pending draft preserves
the exact ordered operations, canonical base version, combined and per-operation
summaries, validation issues, structured diff, and backend lifecycle verdict.
Ordinary handoff and session storage never carry candidate TOML; ConfigPanel may
request source separately from the `config:raw`-gated candidate endpoint.

`config:write` gates creation, cloning, quick edits, and destructive previews;
`config:apply` independently gates the final ConfigPanel write; and `config:raw`
controls source visibility and the raw escape hatch. A diff-only operator may
review and apply a valid structured patch without gaining access to secret-bearing
canonical TOML.

For structured patches, ConfigPanel treats the backend lifecycle assessment as
the sole authority. A hot-capable preview goes to hot apply. A valid
restart-bound preview goes directly to planned-restart staging instead of first
submitting a doomed hot request. When a managed staged configuration already
exists, an eligible patch updates that staged configuration. Missing, invalid,
or lifecycle-rejected metadata fails closed and requires a fresh preview.

> **Draft generation vs. typed structured edits.** Guided editors that have not
> yet migrated may still open a TOML draft in the raw editor. Route and App
> creation/deletion no longer do: they build typed operations and use the shared
> batch-preview handoff. A route is identified by match type + path inside its
> exact parent server; a virtual host is identified by listener + the complete
> order-independent, case-sensitive server-name set. Identity changes are
> therefore reflected truthfully in the diff as removal plus addition.

The Apps **New app** editor creates an upstream through one deterministic ordered
batch. It emits `upstream_add` first, then any additional backends, the complete
active-health-check block when enabled, non-secret discovery settings when
selected, optional server creation/protocol preparation, and the final route
mount. Mounting is explicit: **No route mount**, **Existing exact server**, or
**New exact server**. The Console never infers a virtual host from a listener or
from a blank host-name set.

HTTP mounts use `proxy`; native gRPC mounts use only `grpc_proxy` and never fall
back to HTTP. Existing TLS servers already provide HTTP/2 and are not mutated.
Existing plaintext servers are eligible only when h2c is already enabled. A new
plaintext gRPC server may enable h2c only on an unused dedicated listener, because
that setting is listener-address scoped and must not alter sibling virtual hosts.
Token-required Consul/Kubernetes creation stops before preview: typed App patches
never collect, reveal, or silently omit a new secret, and the raw-editor escape
hatch is shown only to an operator with `config:raw`.

The TLS panel provides **guided creation** of a new TLS-enabled server via
**New TLS server** — a static certificate or automatic HTTPS (ACME) in staging
or production, with optional mutual TLS — generated as a `[[servers]]` block and
routed through the same Validate → Diff → Apply pipeline. Route creation
includes guided **auth** (CIDR allow/deny, HTTP Basic, JWT, or forward-auth).

Beyond creation, several settings are edited **in place** through structured
patch operations that are previewed as a diff before they apply: a route's proxy
target, its **match** (path + type) and its **action** (proxy / static /
redirect / return / deny — richer actions such as gRPC, transcoding, FastCGI and
handler plugins stay raw-only), per-location cache and rate-limit toggles, a
per-location access-control (auth) rule, a per-location WAF override, a server
block's **host names** (`server_names`), upstream backend add/remove,
the upstream load-balancing strategy, active health checks, and dynamic
service discovery (DNS, DNS SRV, Consul, or Kubernetes — provider ACL tokens
are preserved server-side and never leave the box),
per-server limits/timeouts, the per-server HTTP/3 and h2c protocol toggles
(HTTP/3 requires TLS on the listener; h2c applies to plaintext listeners only),
the global distributed-tracing exporter (a guided `[observability.tracing]`
editor covering exporter, collector endpoint, sample ratio, service name, and
transport security), and **WASM plugins** (the **Plugins** panel: declare a
global `[plugins.NAME]` — module path, type, host capabilities and limits,
config — and attach or detach middleware plugins per route; handler and
server-level plugins stay raw-only) |

Whole **servers, routes, and upstream pools** can be **created and deleted**
through structured patch operations — `server_add` / `server_remove`,
`location_add` / `location_remove`, and `upstream_add` / `upstream_remove` —
each previewed and applied through the same validated pipeline. Route and App
**Danger zones** implement a deliberate two-step destructive flow: first preview
the exact typed removal against the pinned base version, then show an accessible
confirmation containing the target identity, operation JSON, ordered summaries,
validation output, lifecycle outcome, and explicit no-cascade scope. Confirming
only hands the preview to ConfigPanel; it does not apply from the drawer. Cancel
performs no handoff or write.

Route deletion is exact and non-cascading. `location_remove` targets one match
type + path inside one exact server identity. `server_remove` targets one exact
server and removes its contained locations only; it does not delete referenced
upstreams, applications, credentials, plugins, sibling virtual hosts, or other
resources. App deletion is one `upstream_remove` operation and is blocked while
projected routes reference the pool; the drawer shows a bounded dependency list
and links to Routes, while the backend re-checks references during preview so a
race is rejected visibly. The server still enforces missing-target, final-server,
and referenced-upstream protections. The backend/API also accepts sparse
`global_set`, `compression_set`, and `rate_limit_global_set` operations. Omitted
fields are preserved; explicit false, zero, empty string, and empty-list values
retain their documented parser semantics; summaries list field names only. A
candidate containing `global.log_format` or a retained-listener `max_conns`
change is staged as one complete candidate. The current Global and Traffic
Controls forms keep their guided validated-TOML-upsert path until #81; `[cache]`
remains truthful stage-only and the raw editor remains available.

The **Streams** panel adds guided **creation and in-place editing** of L4
(TCP/UDP) reverse-proxy listeners (`[[stream]]`): the listen address, protocol,
default backend (`proxy_pass`), SNI routing table (TCP — enables SNI inspection
without terminating TLS), TLS passthrough, the PROXY protocol direction, and the
connect/idle timeouts. UDP listeners reject the TCP-only fields (SNI routes, TLS
passthrough, PROXY protocol), matching the validator. In a build without the
`stream` tag the editor still works, but a lean binary refuses to start with
`[[stream]]` declared, so the panel warns up front.

The **Streams** and **Plugins** panels carry a **GA** badge: per the
[maturity model](adr/0003-maturity-and-ga.md) they meet the GA bar and are
stable for production use, while newly shipped surfaces will be labelled per
ADR 0003 until they complete the same evidence gates. The [status matrix](status.md) is the source of truth for each
feature's level.

The TLS panel's **Mutual TLS** section adds guided **in-place editing** of
client-certificate authentication on a TLS listener. The server-level editor
sets the verification mode (`none` / `request` / `require`), the CA bundle and
optional CRL presented certificates are checked against, and an optional SAN
allow-list (`[tls.client_auth]`); a per-route **require client certificate**
toggle (also offered from the Routes detail) sets a location's
`require_client_cert`. Both edits route through Validate → Diff → Apply. Note the
two take effect on different schedules: server-level `client_auth` is read when
the listener **binds**, so saving reloads HTTP routing immediately but the new
client-certificate verifier applies only after a restart (or a listen-address
change) — the editor and the diff both surface this caveat; per-location
`require_client_cert` is enforced per request and takes effect on hot reload.

With mutual TLS now guided, the [capability matrix](#capability-matrix) below is
the authoritative, per-feature breakdown of what is guided-editable versus
raw-only today.

The Routes panel filters the location list client-side by **action** — including
the protocol-adapter kinds (proxy, gRPC, gRPC transcode, FastCGI, static,
redirect, deny, return) — and by **feature** (auth, cache, compression, rate
limit, warnings), so large configs stay navigable. Filters persist across
sessions and never call the server.

### Web application firewall (WAF)

The Security panel reports the WAF posture truthfully rather than implying a
single uniform policy:

- The **coverage summary** shows how many locations the firewall protects and,
  when they differ, the block/detect/CRS split — so a partial or mixed rollout is
  never flattened into one badge.
- The guided **WAF editor** seeds from, and writes, only the global
  `[waf]` policy. When one or more locations define their own
  `[[servers.locations.waf]]` override (which **replaces** the global policy for
  that location wholesale), the panel lists each override (route, mode, CRS) and
  the edit button is labelled **Edit global** to make clear it does not touch
  those per-location policies.
- Each listed per-location override has its own **Edit** action that opens a
  guided per-location editor. It controls the **full override** — the basic
  knobs (enabled, mode block/detect, the embedded CRS) plus the advanced SecLang
  fields (block status, paranoia level, request-body limit, response-body
  inspection, rule files and inline rules) — and applies the change through
  structured patch operations (`location_waf_set` / `location_waf_clear`)
  reviewed as a diff, so it never hand-edits nested TOML. Because the editor
  seeds **every** field from the running config, a save **replaces** the override
  faithfully (round-trips) rather than clobbering rules it never showed; it also
  refuses a save the server would reject (an enabled override with no rules, an
  out-of-range block status or paranoia level). **Clear override** removes the
  block so the location inherits the global policy again.
- A route that still inherits the global policy can gain an override from the
  **route detail** drawer: its quick edits offer **Add WAF override** (seeded
  detect-first), while a route that already overrides offers **Edit WAF
  override**. Both use the same structured patch ops and diff review, so an
  override can be added, tuned, or removed without leaving the route surface.

In a build without the `waf` tag the editors still work, but the apply preflight
rejects a config that enables the WAF (globally or per location), so the Security
panel warns up front and an apply diff flags the enable — matching the Plugins
and Streams panels.

### Trusted client address

The Security panel lists every listen address with its trusted-proxy posture:
how many ranges it trusts, which forwarding-header preference applies, and the
hop limit. A range covering the whole address space is badged **trusts every
client**, mirroring the `jul lint` finding, because such a range lets any client
assert any address.

The editor is **listener scoped**, and says so: the change is applied to every
`[[servers]]` block on that address in one operation, because Jul derives the
client address before the `Host` header selects a block. Editing "a virtual
host's" policy in isolation is not a thing that exists.

Three properties of the editor are deliberate:

- **It validates nothing locally.** CIDR canonicalisation, the header enum and
  the hop range are checked by the server, and its error is what the operator
  sees — so the Console can never disagree with the configuration it edits.
- **"None — always use the transport peer" is a real setting**, distinct from
  leaving the preference at its default. It sends an explicitly empty header
  list, which means no forwarding header is read even from a trusted peer.
- **It requires its own permission,** `config:trust`, not `config:write`.
  Widening `trusted_proxies` lets the named range assert any client address to
  authentication, rate limiting, the WAF and the audit trail, so it is held to a
  separate grant and recorded under its own audit category
  (`config.client_address`). No predefined role except `admin` holds it.

**Trust no proxy** clears the policy from every block on the listener, returning
it to peer-only identity.

### Audit log

Security- and config-relevant actions (apply, rollback, reload, auth failures)
are recorded as attributable, metadata-only events — never tokens, credentials,
or bodies. Each event carries the server-authenticated **actor** and the
non-secret **token ID** of the credential used (both shown as columns in the
Audit panel), so a change can be traced to the principal that made it without
exposing any secret. By default they live in a bounded in-memory ring buffer
(10,000 events) queryable at `GET /api/audit` and exportable as JSON/CSV.

For compliance or incident review, set a durable sink so the trail survives
restarts and ring-buffer overwrite:

```toml
[admin]
audit_log_file = "./jul-data/audit.jsonl"   # append-only JSONL; one event per line
audit_log_rotate_max_mb = 100               # rotate to a backup at this size (default 100)
audit_log_rotate_keep = 14                  # retained rotated backups (default 14)
```

Each event is appended as one JSON object per line, after the same redaction
applied to the in-memory copy. The parent directory is created if missing. The
sink rotates at `audit_log_rotate_max_mb` and retains `audit_log_rotate_keep`
timestamped backups; rotation is by size and count only — backups are never
deleted by age, so a quiet system keeps its full trail.

The durable sink is **fail-loud**: if the file cannot be opened at startup, or a
write fails later, the server keeps recording in memory (so the admin API is
never taken down) **and** surfaces a degraded `audit_sink` on the runtime
overview (`GET /api/runtime/overview`), which the Console renders as a banner —
rather than silently dropping the trail. Durability favors immediate writes over
per-event `fsync`: events are written as they happen but not flushed to stable
storage on every record, an explicit trade-off. Actor identity is currently the
shared-token `"operator"`; per-user attribution arrives with RBAC (designed in
[docs/specs/console-rbac.md](specs/console-rbac.md), [ADR 0010](adr/0010-console-rbac.md)).

## Capability matrix

What each server capability supports from the console today. **Surface** is one
of: *Guided-create* (a form generates a new block), *Structured-edit* (an
in-place edit applied as a reviewed patch), *Read-only* (shown but changed via
raw TOML), *Raw-only* (no dedicated surface; edit the TOML), or *No surface*.

| Capability | Surface | Panel |
| --- | --- | --- |
| Server blocks / virtual hosts | Guided-create · Structured-edit (limits/timeouts, host-name rename) | Routes, TLS |
| HTTP routes / locations | Guided-create · Structured-edit (match path/type, action, proxy target, cache, rate-limit, WAF) | Routes |
| Match predicates (method / header / query) | Structured-edit (per-location, replaces the whole predicate set) | Routes |
| Response headers / CORS | Structured-edit (per-location, replaces the whole operation list / policy) | Routes |
| gRPC proxy / transcoding, FastCGI, redirect, deny, return | Structured-edit (switch action: redirect/return/deny) · Read-only for gRPC/transcode/FastCGI/plugin | Routes |
| Response cache | Structured-edit (global + per-location toggle) | Traffic Controls, Routes |
| Compression | Structured-edit (global) | Traffic Controls |
| Rate limiting | Structured-edit (global + per-location toggle) | Traffic Controls, Routes |
| Access control (auth) | Guided-create · Structured-edit (per-location: CIDR / Basic / JWT / forward-auth) | Routes, Security |
| TLS / HTTPS | Guided-create (New TLS server) · Raw-only to edit existing | TLS |
| Mutual TLS | Guided-create (within the TLS editor) · Structured-edit (mode / CA bundle / CRL / SAN allow-list — bind-time; per-location require-client-cert — immediate) | TLS, Security, Routes |
| Automatic HTTPS (ACME) | Guided-create (within the TLS editor) · Raw-only to edit existing | TLS |
| HTTP/3, h2c | Structured-edit (per-server toggle; HTTP/3 requires TLS, h2c plaintext only) | Routes |
| Upstream pools | Guided-create · Structured-edit (backends, strategy, health checks, discovery) | Apps |
| Load-balancing strategy, health checks, service discovery | Structured-edit | Apps |
| Web application firewall (WAF) | Structured-edit (global + per-location, incl. advanced fields: block status / paranoia / body limits / rule files / inline rules / response-body inspection) | Security |
| Secret references | Read-only (externalize helper) | Security |
| Server limits / timeouts | Structured-edit | Routes |
| Plugins (WASM) | Structured-edit + .wasm upload (global defs: path/type/capabilities/limits/config; attach/detach middleware plugins per route; upload module via file picker; handler & server-level plugins stay raw-only) | Plugins |
| L4 stream proxy | Guided-create · Structured-edit (listen, protocol, default backend, SNI routes, TLS passthrough, PROXY protocol, timeouts) | Streams |
| gRPC-JSON transcoding | Guided-create (upload descriptor, inspect HTTP bindings, generate route) · Read-only for existing transcode routes; gRPC passthrough remains raw-only | Transcode, Routes |
| Tracing / OpenTelemetry | Structured-edit (global guided editor: exporter / endpoint / sample ratio / service name / transport) | Traffic Controls, Status |
| Access logs | Structured-edit (`enabled`, sinks, file, format, rotation) + bounded live tail; changes are staged for restart | Traffic Controls, Operations |
| Admin listener | No surface | — |
| Config history / rollback | Full (view + rollback) | History |
| Audit log | Full (filter + export) | Audit |

The guided surface grows continuously per [ADR 0004](adr/0004-console-ui-invariants.md);
rows move from *Read-only* / *Raw-only* to *Structured-edit* as editors ship.

### Build-tag degradation

Several capabilities are compiled in only with their build tag (the `Full`
edition includes them all; the lean `Core/OSS` build omits them). When a tag is
absent the console **degrades transparently** rather than failing opaquely: the
editor stays usable, but the limitation is disclosed up front and the apply
preflight rejects a config that would enable the missing feature, so a draft is
never silently dropped.

| Feature (tag) | Panel | When the tag is absent |
| --- | --- | --- |
| WASM plugins (`wasmplugins`) | Plugins | Banner in the panel; apply diff warns; preflight rejects a config that declares plugins |
| L4 stream proxy (`stream`) | Streams | Banner in the panel; apply diff warns; a lean binary refuses to start with `[[stream]]` |
| Web application firewall (`waf`) | Security | Banner in the panel; apply diff warns; preflight rejects an enabled WAF |
| Distributed tracing (`otel`) | Traffic Controls | Apply diff warns that spans only export from an `otel` build (config is accepted; tracing is a no-op otherwise) |

## Testing the console

The console has three complementary test layers:

| Layer | Command | What it covers |
| --- | --- | --- |
| Vitest unit (361 tests) | `pnpm test` | Pure component logic, Zod schema parsing, lib helpers, React-Query mutations |
| Go over-the-wire e2e | `go test ./internal/admin/` | Real HTTP against the admin router; request/response contract |
| Playwright browser smoke | `pnpm e2e` | Built SPA rendered in Chromium; overview → edit route → diff → apply → rollback |

### Playwright browser smoke (`e2e/smoke.spec.ts`)

The smoke test mounts the **built SPA** via `vite preview` and intercepts every
`/api/*` call with `page.route()` — no Go server is needed. This catches a
class of bugs that pass the other two layers: schema drift between the Zod
schemas in `client.ts` and the mock fixtures causes a `ZodError` inside the SPA
which surfaces as a panel-error boundary, failing the assertion. Run it after
`pnpm build`:

```bash
pnpm build          # produces internal/admin/assets/dist/
pnpm e2e            # starts vite preview + drives Chromium headlessly
```

In CI the `console-e2e` job runs the same suite with `pnpm e2e:ci`.

## API endpoint to panel map

The console SPA is driven entirely by same-origin `/api` endpoints. Each panel
consumes the endpoints below.

| Panel / surface | Endpoints |
| --- | --- |
| Overview | `GET /api/runtime/overview`, `GET /api/stats` |
| Routes | `GET /api/routes`, `POST /api/routes/test` |
| Apps & Upstreams | `GET /api/apps`, `GET /api/upstreams`, `GET /api/upstreams/{name}/resilience` |
| TLS & Certificates | `GET /api/tls`, `GET /api/certs`, `GET /api/mtls` |
| Security | `GET /api/security`, `GET /api/listeners`, `GET`/`PATCH /api/listeners/{addr}/client_address` |
| Traffic Controls | `GET /api/traffic-controls` |
| Plugins | `GET /api/plugins`, `POST /api/plugins/upload` |
| Streams | `GET /api/streams` |
| Transcode | `POST /api/transcode/descriptor-upload` |
| Search & Discovery | `GET /api/search` |
| Operations | `GET /api/observability/{requests,failing-routes,timeline,upstream-history,cert-history,logs}`, `GET /api/observability/logs/stream` (SSE), `GET /api/admin/health`, `POST /api/admin/client-errors`, `GET /api/events` (SSE) |
| Audit | `GET /api/audit`, `GET /api/audit/export` |
| Config editor / History | `GET /api/config` (+ `/raw`, `/validate`, `/diff`, `POST /apply`, `/patch`, `/patch/preview`, `/patch/candidate`, `/patch/apply`, `/history`, `/history/{id}`, `/rollback`) |
| Setup Wizard | `GET /api/wizard`, `POST /api/wizard/generate` |
| Operational actions | `POST /cache/purge`, `POST /reload` |

## Consistency, feedback & discoverability

Every panel reports the same three transient states the same way, so the Console
feels like one product rather than a set of independently built screens (part of
the *Friendliest* pillar, [ADR 0004](adr/0004-console-ui-invariants.md)):

- **Loading.** While a panel's data is still being fetched it shows the shared
  `Loading` indicator — a spinner next to a short label (e.g. *"Loading
  routes…"*) — announced to assistive technology with `role="status"`. No panel
  invents its own bare "Loading…" text.
- **Empty.** When a collection has no items yet (no apps, no routes, **no config
  snapshots**), the panel shows a shared `EmptyState` card explaining what the
  collection is and how to populate it, rather than a blank area or a stray line
  of muted text.
- **In flight.** Long-running actions report progress on the control that
  triggered them. Applying a configuration change swaps the **Apply** button to
  a spinner and *"Applying…"* while the request is outstanding, and the
  **Apply** / **Reset** buttons stay disabled so the change cannot be
  double-submitted.

**Discoverability.** The command palette (jump-to-any-page search) is reachable
from anywhere with `Ctrl/Cmd+K`, and a labelled **Jump to…** button in the
header makes that shortcut visible instead of hidden. The **Timeline** annotates
each event dot with a tooltip and an accessible label describing its severity
and category (e.g. *"Warning severity — tls event"*), so the colour coding is
self-explanatory.

## Accessibility & keyboard operation

The Console is operable by keyboard and screen reader, not just by mouse — part
of the *Friendliest* pillar ([ADR 0004](adr/0004-console-ui-invariants.md)).
Every control is a real focusable element reachable with `Tab` / `Shift+Tab`,
and modal surfaces (the route/app **Drawer**, the **Confirm** dialog, the shared
**Modal**, the **command palette**, and the re-authentication **token prompt**)
behave as proper modal dialogs: focus moves into the dialog on open, `Tab` /
`Shift+Tab` wrap **within** it so focus cannot leak to the page behind, and focus
returns to the triggering control on close (WCAG 2.4.3). The full accessibility
commitments, keyboard-shortcut map, and verification live in
[accessibility.md](accessibility.md).

## Security model

- **Authentication:** all data and mutating APIs require the bearer token when
  `[admin].token` is set, compared in constant time. The token is held in the
  browser's `sessionStorage` only.
- **Same-origin + CSP:** console pages send a strict `Content-Security-Policy`
  (`default-src 'self'`), `X-Frame-Options: DENY`, and `X-Content-Type-Options:
  nosniff`. The SPA talks only to same-origin `/api` endpoints.
- **Path-traversal safe:** snapshot identifiers are validated against a strict
  charset (`[0-9A-Za-z._-]`, no separators or `..`), so the history endpoints
  can never read or write outside `history_dir`.
- **Validate-before-apply:** no edit (form, raw, wizard, or rollback) is written
  unless it parses and validates. An invalid edit is rejected with a message and
  the running configuration keeps serving unchanged.

## Known limitations

- **RBAC is opt-in.** When `[admin].rbac.enabled` is `false` (the default for
  backward compatibility) all Console users share the same `[admin].token`;
  there is no per-user authentication, authorization scopes, session isolation,
  or per-operator audit attribution. Enable RBAC per
  [docs/specs/console-rbac.md](specs/console-rbac.md) to get named principals,
  predefined and custom roles, and per-principal audit attribution. Disabling
  RBAC after it was enabled clears the active policy on the next successful hot
  reload and the server falls back to the configured legacy token (or anonymous
  loopback access when no token is configured). The admin listener address
  remains startup-bound; changing it requires a process restart.
- Token rotation via a hot apply is reflected immediately by the admin server;
  the Console does not need to be restarted to pick up a new `[admin].token`.
- The in-console **Operations → Logs** tail is a bounded, privacy-preserving view
  of recent access-log lines (paths redacted, query strings dropped, User-Agents
  reduced to a coarse family); the configured sinks (server log, file, or syslog)
  remain the durable, complete record.
- ACME certificate expiry appears in the cert panel once the certificate has
  been issued and its live metadata is available; the
  `jul_tls_cert_expiry_seconds` metric is the always-on source.
- The Status overview reflects the parsed configuration (what is enabled), not
  per-request live counters.

## Global and Traffic Controls typed-patch workflow (#81)

The Global and Traffic Controls screen reads only non-secret projections. The
editable global projection exposes `worker_threads`, `log_level`, `log_format`,
`shutdown_timeout`, `reload_timeout`, and `redact_min_secret_length`; lifecycle
badges are generated from the Go lifecycle registry rather than a React table.
Compression and global rate-limit projections retain dormant values while their
feature is disabled, including compression level/types/precompressed and the
listener-global `max_conns` value.

Global, compression, global rate-limit, and the four server limit fields use
sparse typed operations. Omitted properties preserve the persisted/staged
value; explicit false, zero, empty strings where valid, and empty arrays remain
present. A no-op cannot be reviewed. Final action selection stays
server-authoritative: **Apply live**, **Save for next restart**, or **Update
staged configuration**. `log_format` is restart-bound. `max_conns` is bound to
listeners: a retained listener stages, while an all-new listener transition may
be hot if the backend preview says so. Mixed candidates stage whole.

Cache intentionally has no `cache_set`. The editor regenerates the complete
seven-field `[cache]` table from the exact raw fetch and keeps the candidate and
its `base_version` together in memory. It always stages and never offers Apply
live. Raw candidate source requires `config:raw`; typed edits require
`config:write`, and the final action independently requires `config:apply`.
Candidate TOML is not serialized to `sessionStorage` or `localStorage`.
