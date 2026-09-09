# Remote automation CLI

Jul's remote automation commands are a thin client over the supported `/api/v1`
control plane. The server remains authoritative for TOML validation, lifecycle,
diffing, authority, apply/stage decisions, rollback, adoption and terminal state.
The CLI never falls back to the private Console API.

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
| `jul apply --adopt-external` | adoption preview + mutation + polling |

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
boundary.

## Credentials and TLS

Bearer credentials may come from a token file, `JUL_TOKEN`, a profile token-file
reference, or stdin via `--token-file -`. There is intentionally no `--token`
argument because command-line secrets leak into shell history/process listings.
Environment variables are convenient but are not a perfect secret store; a
restricted token file/profile reference is preferred for automation.

The client rejects credential-bearing endpoint URLs. HTTPS verifies certificates
and hostnames normally. `--ca-file` adds a trust bundle and `--client-cert` plus
`--client-key` enable mTLS. There is no generic `--insecure` verification bypass.
Plain HTTP is accepted only for loopback. Control-plane redirects are not
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
not success, so polling continues.

Polling has a bounded interval and local deadline. Ctrl-C stops only the local
wait; it does **not** cancel the server transaction. Human output surfaces the
`apply_id` and the operator can inspect it later with:

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

## Authority modes

Read operations (`plan`, `diff`, `status`, safe `export`, diagnostics) work in
`managed` and `file_owned` wherever the server contract permits. A remote
mutation rejected by file-owned authority returns the stable
`config_authority_read_only` error and exit 6. The client does not infer or
bypass authority locally.

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
| 0 | successful live/read/ordinary operation |
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
