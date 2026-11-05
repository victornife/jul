# Remote automation CLI

Jul's remote automation commands use the supported `/api/v1` control plane,
except `jul postcondition`, which calls the sharing evaluator at
`/api/sharing/postcondition/evaluate`. The server remains authoritative for
TOML validation, lifecycle, diffing, authority, apply/stage decisions, rollback,
adoption and terminal state. The CLI never falls back to the private Console
API. The sharing evaluator exception is described below.

## Commands

| Command | Stable API workflow |
| --- | --- |
| `jul plan` | `POST /api/v1/config/plan` |
| `jul diff --config ...` | `POST /api/v1/config/plan` |
| `jul diff --history-id ...` | `GET /api/v1/config/history/{id}/diff` |
| `jul apply` | `POST /api/v1/config/apply?mode=hot` + apply-result polling |
| `jul stage` | `POST /api/v1/config/apply?mode=stage_restart` + polling |
| `jul status` | `GET /api/v1/status`, optionally `GET /api/v1/config/applies/{apply_id}` |
| `jul rollback` | history diff + `POST /api/v1/config/rollback` + polling |
| `jul export` | `GET /api/v1/config/export` |
| `jul diagnostics` | `GET /api/v1/status` + `GET /api/v1/capabilities` |
| `jul postcondition` | `POST /api/sharing/postcondition/evaluate` (read-only sharing evaluator; outside `/api/v1`) |
| `jul apply --adopt-external` | adoption preview + mutation + polling |
| `jul cache inspect` | `POST /api/v1/cache/inspect` |
| `jul cache purge` | `POST /api/v1/cache/purge/preview`, `POST /api/v1/cache/purge`, `GET /api/v1/cache/purges/{operation_id}` |
| `jul waf events` | `GET /api/v1/waf/events` (bounded, read-only WAF event query) |

Remote export is the redacted structured projection. Exact raw TOML remains a
local expert workflow and is deliberately not part of v1. Remote diagnostics is
also deliberately smaller than local `jul doctor` / `jul support-bundle` because
v1 does not publish a remote support-bundle endpoint.

## Endpoint and profile resolution

Precedence is:

1. explicit command flags and explicit `--profile` selection;
2. fields in the selected named profile;
3. `JUL_ENDPOINT`, `JUL_TOKEN_FILE`, `JUL_TOKEN`;
4. no endpoint.

There is no implicit localhost/default remote endpoint. A missing endpoint is a
usage error.

Named profiles live at the platform user configuration directory returned by
`os.UserConfigDir()`, under `jul/profiles.json`. For example on typical systems:

```json
{
  "profiles": {
    "prod": {
      "endpoint": "https://jul-admin.example.net",
      "token_file": "/secure/path/jul-prod.token",
      "ca_file": "/secure/path/company-ca.pem",
      "client_cert_file": "/secure/path/client.pem",
      "client_key_file": "/secure/path/client-key.pem",
      "timeout": "15s"
    }
  }
}
```

Unknown profile fields are rejected. On POSIX, the CLI warns when an existing
profile or token file is group/other-readable or writable. Windows uses its
platform ACL model rather than pretending POSIX mode bits are a security
boundary. Profiles must be regular files of at most 1 MiB; token files must
be regular files of at most 64 KiB. Stdin tokens have the same 64 KiB cap.

## Credentials and TLS

Bearer credentials may come from a token file, `JUL_TOKEN`, a profile token-file
reference, or stdin via `--token-file -`. The shared remote-automation commands
intentionally expose no raw `--token` argument because command-line secrets
leak into shell history/process listings. (`jul waf events` has a separate
`-token` option, described below.) Environment variables are convenient but are
not a perfect secret store; a restricted token file/profile reference is
preferred for automation.

The client rejects credential-bearing endpoint URLs. HTTPS verifies certificates
and hostnames normally. `--ca-file` adds a trust bundle and `--client-cert` plus
`--client-key` enable mTLS. There is no generic `--insecure` verification bypass.
The common `--timeout` flag sets the ordinary HTTP request timeout (default
`15s`); the named-profile `timeout` field sets the same value.
Plain HTTP is accepted only for loopback. Plaintext dials resolve `localhost`
to loopback addresses only and ignore environment proxies, so a bearer token
cannot travel to a changed localhost mapping. Control-plane redirects are not
followed, so credentials cannot be forwarded to a different host/origin or an
HTTPS-to-HTTP downgrade.

`--verbose` emits only bounded operation/phase/request identifiers. It never
prints Authorization headers, token values, request bodies, raw configuration,
or URL credentials.

## Plan, apply and stage

Review a candidate without side effects:

```sh
jul plan --endpoint https://jul-admin.example.net \
  --token-file /secure/path/token \
  --config candidate.toml --json
```

Apply an already-reviewed candidate against its optimistic-concurrency fence:

```sh
jul apply --endpoint https://jul-admin.example.net \
  --token-file /secure/path/token \
  --config candidate.toml \
  --base-version sha256:REVIEWED_BASE \
  --idempotency-key deploy-20260910-001 \
  --json
```

If the server returns `restart_required`, `apply` does not silently switch to
stage. Stage explicitly instead:

```sh
jul stage --endpoint https://jul-admin.example.net \
  --token-file /secure/path/token \
  --config candidate.toml \
  --base-version sha256:REVIEWED_BASE \
  --idempotency-key deploy-20260910-001-stage \
  --json
```

For `plan`, `diff --config`, `apply` and `stage`, pass `--config -` to read
candidate TOML from stdin (up to 2 MiB); otherwise pass a file path.

`--confirm-admin` is the narrow explicit acknowledgement used only when the
server reports that a candidate affects the current admin reachability path. It
is not a general force switch.

## Optimistic concurrency and idempotency

Every mutation carries the reviewed `base_version` required by the stable API.
The CLI does not silently refresh it immediately before writing because that
would erase the review/CAS boundary.

Every applicable mutation sends `Idempotency-Key`. When it is omitted, the CLI
generates a unique valid key for that logical invocation. CI/deployment systems
that need safe retry across process restarts should provide a stable job/deploy
identity with `--idempotency-key`.
The server rejects repeated `Idempotency-Key` fields before a mutation, so an
intermediary cannot select a different key from the one bound by the ledger.
An explicitly empty key is invalid; omit the header to request a mutation
without replay binding.

A prepared mutation freezes its method, path, query, content type, key and exact
request body bytes before the first send. A contractually safe retry reuses
those exact bytes. The client checks `boot_id` after transport ambiguity; if the
server boot changed, the old in-memory idempotency ledger can no longer be
assumed and the CLI reports the uncertainty rather than claiming replay safety.

`idempotency_key_reused` and `idempotency_key_in_flight` are conflicts and are
not silently retried.

## Polling and Ctrl-C

Apply, stage, rollback and adoption poll `GET /api/v1/config/applies/{apply_id}`.
The server's `terminal` boolean is authoritative. `saved_not_live` is progress,
not success, so polling continues. `no_change` is terminal success and stops
polling immediately.

Polling uses a bounded interval. The local deadline is controlled by
`--poll-timeout` on `apply` (including adoption), `stage` and `rollback`
(default `2m`). Ctrl-C or that deadline stops only the local wait; neither
cancels the server transaction. Human output surfaces the `apply_id`; inspect it
later with:

```sh
jul status --endpoint https://jul-admin.example.net \
  --token-file /secure/path/token \
  --apply-id APPLY_ID
```

## Rollback and adoption

Rollback is server-driven: the CLI asks the server for the safe history diff,
preserves that preview's base fence, submits the history id and polls the normal
transaction ledger. It never downloads a raw historical snapshot to rebuild or
diff locally.

External adoption stays under the apply namespace:

```sh
jul apply --endpoint https://jul-admin.example.net \
  --token-file /secure/path/token \
  --adopt-external \
  --base-version sha256:REVIEWED_BASE \
  --idempotency-key adopt-20260910-001 \
  --json
```

The CLI performs the server adoption preview, carries the returned observed
raw digest/base fence into the confirmed mutation, and polls the normal apply
ledger. Drift/adoption rules remain server-owned.

## Sharing postcondition evaluation (`jul postcondition`)

This read-only command evaluates a plan's sharing postcondition against the
server's current serving snapshot. It requires `status:read` and uses
`POST /api/sharing/postcondition/evaluate`, outside the versioned `/api/v1`
control plane.

```sh
jul plan --endpoint https://jul-admin.example.net \
  --token-file /secure/path/token \
  --config candidate.toml --json > plan.json

jul postcondition --endpoint https://jul-admin.example.net \
  --token-file /secure/path/token \
  --file plan.json --json
```

`--file` accepts either a direct postcondition JSON object or plan JSON
containing `.sharing.postcondition`; use `--file -` to read JSON from stdin
(stdin reader limited to 2 MiB). The result reports candidate and access
state, with a summary and recovery guidance when available. Common endpoint,
profile, credential and TLS flags apply.

## Authority modes

Read operations (`plan`, `diff`, `status`, safe `export`, diagnostics) work in
`managed` and `file_owned` wherever the server contract permits. Operational
cache invalidation (`jul cache purge`) also works in both `managed` and
`file_owned` modes because it mutates runtime cache state rather than
configuration files. A remote configuration mutation rejected by file-owned
authority returns the stable `config_authority_read_only` error and exit 6. The
client does not infer or bypass authority locally.

## Scoped cache invalidation (`jul cache purge`)

Preview a scoped cache invalidation without side effects (#621):

```sh
jul cache purge --endpoint https://jul-admin.example.net \
  --token-file /secure/path/token \
  --host portal.example.com \
  --tag article:42 \
  --preview --include-samples --json
```

Execute the invalidation bound to the reviewed `preview_token` and an
idempotency key:

```sh
jul cache purge --endpoint https://jul-admin.example.net \
  --token-file /secure/path/token \
  --host portal.example.com \
  --tag article:42 \
  --preview-token "$PREVIEW_TOKEN" \
  --idempotency-key publish-article-42-rev-7 \
  --json
```

Supported selector flags are `--url`, `--host` with `--uri` / `--prefix` /
`--glob` / `--tag` (`--tag-match any|all`), `--host --confirm-broad`, `--key`,
and `--all --confirm-broad` (plus optional `--route-id`). Use
`--kind exact_url|prefix|glob|tag|host|key|all` to specify the selector kind
explicitly. Optional `--max-scan-entries` bounds a preview scan, and
`--operation-timeout-ms` sets the server-side operation timeout. Inspect an
operation by ID with `--operation-id cp_...` or retry an incomplete operation at
its original fence with `--retry-operation-id cp_...`. On an operation-ID retry, omit
`--idempotency-key` or reuse the operation's original key; a newly supplied
key is rejected rather than silently ignored. A runnable deployment/content-update
helper script is provided in
[`examples/cache-invalidation/purge-on-deploy.sh`](../examples/cache-invalidation/purge-on-deploy.sh).

## JSON output

`--json` writes exactly one JSON object to stdout and no headings, ANSI, spinner
or progress stream. Stable API errors preserve the server error envelope.
Failures that happen before an API envelope exists use a bounded CLI-local error
shape (`transport_error` / `boot_changed`) without exposing Go internals or
credentials. Scripts never need to parse stderr to determine the outcome.

## Automation exit codes

The canonical table is `internal/adminapi.ExitCodes()` and is also exposed by
`jul capabilities`; the remote client does not define another mutable table.

| Exit | Meaning |
| ---: | --- |
| 0 | successful live/no-change/read/ordinary operation |
| 1 | validation/configuration failure |
| 2 | usage/request error |
| 3 | successful operation requiring restart/convergence |
| 4 | successful degraded outcome |
| 5 | conflict / uncertain state after failed restoration |
| 6 | configuration-authority denial |
| 7 | authentication/authorization/insecure transport |
| 8 | connectivity/TLS/rate-limit failure |
| 9 | server/capability/storage/operation-timeout/internal failure |

A non-zero automation exit is not always a failed write. In particular exits 3
and 4 are successful special outcomes and should be handled explicitly by CI.

Example shell policy:

```sh
set +e
jul apply --endpoint "$JUL_ENDPOINT" \
  --token-file "$JUL_TOKEN_FILE" \
  --config candidate.toml \
  --base-version "$REVIEWED_BASE" \
  --idempotency-key "$DEPLOYMENT_ID" \
  --json > result.json
rc=$?
set -e
case "$rc" in
  0) echo "live success" ;;
  3) echo "accepted, restart/convergence required" ;;
  4) echo "successful but degraded; inspect result.json" ;;
  5) echo "conflict/uncertain state; re-read authoritative status" >&2; exit 5 ;;
  6) echo "server is file-owned/read-only" >&2; exit 6 ;;
  7) echo "authentication/authorization/transport policy denied" >&2; exit 7 ;;
  8) echo "connectivity/TLS/rate-limit failure" >&2; exit 8 ;;
  9) echo "server/capability/storage/timeout/internal failure" >&2; exit 9 ;;
  *) exit "$rc" ;;
esac
```

Never place realistic credentials in repository examples or idempotency keys.


## Cache metadata inspection

```sh
jul cache inspect --endpoint URL --token-file FILE \
  [--host HOST] [--route-id ID] \
  [--url URL | --uri URI | --prefix PREFIX | --glob GLOB | --tag TAG | --key KEY] \
  [--cursor CURSOR] [--page-size N] [--json]
```

Exact `--url` and `--key` selectors can be used without `--host`; URI,
prefix, glob and tag selectors require `--host`. `--route-id` can be used by
itself or added as a scope. Choose at most one URL/URI/prefix/glob/tag/key
selector. Page size defaults to 50 resources and is limited to 1–50. Repeat the
same selector for later pages; never automatically restart an expired capture.
Successful reads exit zero even for explicit partial/truncated states; inspect
the result fields. See [permissions, errors and limits](cache.md#bounded-cache-inspection).

## WAF outcome events (`jul waf events`)

`jul waf events` reads the bounded server-side WAF outcome ring through
`GET /api/v1/waf/events`; it requires `observability:read` and does not mutate
configuration.

```sh
jul waf events -url https://jul-admin.example.net \
  -token-file /secure/path/token \
  -event-id wev_... -outcome interrupted_request \
  -rule-id 942100 -route-id public-api -limit 50 -json
```

The URL defaults to `http://127.0.0.1:9090`. Filters are optional:
`-event-id`, `-outcome`, `-rule-id`, and `-route-id`; `-limit` defaults to 50
and accepts 1–256. Authentication can use `-token-file`, `JUL_ADMIN_TOKEN`, or
`-token`; prefer the file or environment variable to avoid exposing a token in
process arguments. This command has a fixed 10-second request timeout and a
separate connection interface: it does not accept `--endpoint`, `--profile`,
`--ca-file`, `--client-cert`, `--client-key`, or `--timeout`. It rejects
plaintext URLs except loopback, uses the system TLS trust store, disables
proxies, and does not follow redirects. On success, `-json` writes the event
response; failures use a small `status`/`error` object. Successful requests
exit 0, request/API failures exit 1, and usage errors exit 2; these codes are
separate from the remote automation exit table above.

Only `jul waf events` is remote. `jul waf evaluate`, `jul waf manifest`, and
`jul waf verify-assets` use local configuration and files; `jul posture` also
evaluates locally. See the [WAF guide](waf.md) and
[security posture guide](security-posture.md) for those workflows. Remote WAF
policy changes use `plan` and `apply`/`stage`; there is no separate remote
WAF policy mutation command.

## Cache cutover on configuration changes

Managed live previews/applies and history rollback accept optional
`cache_cutover=retain|invalidate`; omit it to use configured `reload_policy`.
Raw TOML requests use the query parameter, and structured patch/rollback requests
can use the JSON field. Preview and execution use the same choice. Invalid,
conflicting or repeated options fail before mutation. The choice affects one
live change and does not rewrite persistent policy. `stage_restart` rejects a
one-change override; configure persistent `reload_policy` instead.

`jul apply --cache-cutover=invalidate` and the Console Config/History choice
controls use this server-owned contract. Mandatory matcher/policy/security
invalidation is preserved under retain. An unchanged configuration remains a
no-op; content invalidation uses the versioned cache purge workflow.

A cleanup failure after publication reports the configuration as committed:
`applied_degraded`, `published: true`, and a structured `reload.cache_cutover`
on the Console/reload result. Versioned mutation and exact-ID polling responses
expose `cache_cutover_state` and `cache_cutover_result` with effectiveness,
counts and recovery guidance. Staging reports `staged_for_restart` without live
cleanup. Repair storage and use the versioned purge flow for the previewed
scopes; do not reapply or roll back solely to retry cache cleanup.

See [cache reload behavior](cache.md#cache-data-across-reloads) and
[ADR 0030](adr/0030-cache-purge-recovery-latency-and-cutover.md).
