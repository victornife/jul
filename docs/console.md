# Jul.IA Console

The Console is a loopback-bound web control plane for operating a running
Jul.IA server: a live metrics dashboard, a runtime-status overview of which
capabilities are active, upstream health, certificate inventory, safe
configuration editing with version history and one-click rollback, and a setup
wizard. It ships **inside the single binary** (no external assets, no Node
build) and is gated by the `console` build tag.

## Enabling the console

The console is served by the [admin listener](../README.md#admin-interface--observability).
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
# open http://127.0.0.1:9090/ and paste the token into the top-right box
```

Binaries built **without** `-tags console` serve the basic configuration page at
the root instead; the JSON APIs below that do not require the tag (for example
`/api/stats`, `/api/upstreams`) remain available for scripting.

> **Keep the admin listener on loopback.** It exposes operational controls. If
> you must reach it remotely, front it with your own authenticated tunnel rather
> than binding it to a public address. `jul lint` warns when admin is bound
> off-loopback without a token.

## Panels

### Dashboard

Polls `GET /api/stats` every two seconds and shows requests/sec, in-flight
requests, error rate, cache-hit ratio, connection count, latency (avg/p50/p95/
p99), a requests/sec sparkline, and a status-class breakdown.

### Status

A read-only overview of which shipped capabilities are active in the **running**
configuration, grouped into Traffic, Security, Protocols, Upstreams,
Observability, and Extensibility. Each row reports the capability, its state
(active/off), and a short detail (counts and kinds only — never tokens, paths,
or backend addresses). It lets an operator confirm at a glance what the running
build is actually doing without reading the raw TOML. Backed by
`GET /api/status`, derived from the parsed configuration; when the config is
unavailable it renders an empty state.

### Upstreams

Lists each named `[[upstreams]]` pool with its load-balancing strategy and a row
per backend showing live health (active + passive), weight, and in-flight
requests. Backed by `GET /api/upstreams`.

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

### Guided editors — scope (v2)

The Routes and Apps panels provide **guided creation** that generates a
complete `[[servers]]`/`[[upstreams]]` TOML fragment and routes it through the
validated **Validate → Diff → Apply → Rollback** pipeline — the editors never
write directly, so an invalid draft never replaces the running config.

> **Append-as-draft semantics.** Editing an existing route or app opens it as a
> new draft block appended to the raw config, which the operator reviews in the
> editor before applying. In-place *replace/rename* of an existing block is
> intentionally **not** performed automatically: rewriting TOML in the browser
> would risk dropping comments and formatting, and the validated apply path plus
> the structured diff already make append-then-prune a safe, auditable workflow.
> Automatic in-place replace/rename is tracked as a follow-up.

The TLS, Security (auth/mTLS), and ACME panels are **read-only inventories** in
v2; guided enablement/editing for TLS, ACME, mutual-TLS, and auth rules is a
pending P1 item. Until then, change those settings through the validated raw
TOML editor, which the structured diff annotates with the consequences listed
above.

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

## Limitations (v1)

- No RBAC/SSO or multi-node management (single-token, single-node).
- No live log streaming yet; access and error logs are written to the
  configured sinks (server log, file, or syslog) and tailed there.
- ACME certificate expiry is surfaced via metrics rather than parsed in the
  cert panel.
- The Status overview reflects the parsed configuration (what is enabled), not
  per-request live counters.
