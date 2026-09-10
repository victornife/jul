# Admin runtime hot reload (HR-06B)

Issue #157 makes Console mode and plugin-upload policy operationally hot while keeping admin listener ownership structural. This document is the operator/developer contract for those transitions.

## Scope

The following fields are `hot_reload` when an admin server already exists:

- `admin.console`
- `admin.plugin_upload_enabled`
- `admin.plugin_upload_max_size`
- `admin.plugin_upload_dir`

`admin.enabled` and `admin.listen` remain `restart_required`. A candidate that also changes either structural listener field is not partially hot-applied: normal lifecycle planning stages/rejects the candidate according to the existing restart workflow. Listener enable/disable/address movement remains the separately gated HR-08 work in #97.

The lifecycle registry in `internal/lifecycle/registry.go` is authoritative; generated lifecycle/reference artifacts must be regenerated rather than edited by hand.

## One immutable generation per request

The live admin server owns one immutable snapshot that contains authentication/RBAC state together with the Console and upload policy. Publish installs a fully prepared snapshot with one atomic pointer swap. The HTTP mux is process-stable; routes are not re-registered on reload.

The outer admin handler captures the snapshot exactly once when a request enters the mux and stores that pointer in request context. Authentication, authorization, Console dispatch, upload admission/limits/storage and safe runtime projections reuse that same pointer. They do not load independent live policy stores later in the request.

Consequences:

- a request admitted before Publish completes entirely under generation A;
- the first request captured after Publish uses generation B;
- a long upload admitted under A keeps A's enable flag, size limit and target directory even if B is published while its body is being read;
- a request cannot authenticate under A and then observe B's Console/upload policy accidentally;
- no grace overlap is added beyond already in-flight requests.

The exposed generation digest is diagnostic correlation metadata only. It must not become a metric label.

## Prepare, Publish and rollback

All reload entry points converge on the same application prepare/publish path: managed apply/typed patch, raw configuration apply, SIGHUP and file watch. `PrepareAdminRuntime` runs after configuration resolution and before Publish.

For a candidate with uploads enabled and a positive maximum size, Prepare normalizes and validates the candidate upload directory. Preparation is reversible:

- an existing directory receives an owner-only uniquely named probe through `os.Root`; the probe is removed before Prepare returns;
- a missing target is exercised in a temporary sibling tree beneath the nearest existing parent; the configured final directory is not created by Prepare;
- a configured final component that is a symbolic link or non-directory is rejected;
- preparation failure returns a bounded `upload_directory_unusable` category for runtime diagnostics while preserving the detailed error only in ordinary operator diagnostics/logging.

A failed Prepare never publishes the candidate snapshot. The previously live generation continues serving unchanged. Commit is the existing no-fail atomic snapshot publication; actual upload files/directories are created only by admitted requests after publication.

## Console mode transitions

A full build constructs the embedded Console handler once and dispatches to it only when the request's captured generation has `admin.console = true`. With `console = false`, `/`/`/ui`/`/config` use the legacy/fallback admin pages while authenticated APIs remain registered on the same listener. A full build therefore supports `on → off → on` without listener restart or mux replacement.

A lean build has no embedded Console assets. `console = true` remains a valid configured value but cannot make the Console effective; runtime status reports `console_compiled=false` and `console_effective=false`, and the fallback UI continues to serve. This is build truth, not a hidden restart requirement.

The Console settings drawer requires explicit acknowledgement before disabling the web Console. It tells the operator how to recover: set `[admin] console = true` in the configuration and reload through the local/source-owned path, or use an authenticated configuration apply/patch API request while the admin API remains reachable. The UI submits through the normal lifecycle preview and waits for the existing correlated terminal apply result; it does not invent a direct configuration-write path.

## Plugin-upload policy transitions

### Admission

When uploads are disabled, `POST /api/plugins/upload` returns `403` before reading or parsing the multipart request body. This avoids spending bandwidth/memory/disk work on a feature that the captured generation did not admit.

The canonical parser materializes an omitted `plugin_upload_enabled` as `false`: operators must explicitly opt in to uploads. A nil pointer may still occur in directly constructed in-memory `AdminConfig` values and is tolerated as an internal compatibility case; it is not the public configuration default. A non-positive `plugin_upload_max_size` also disables admission. A request captures its maximum size before body processing. Tightening or loosening the limit affects new requests after Publish and does not rewrite the policy of an upload already in flight.

### Storage and file safety

The configured upload directory is normalized once into the prepared generation. An admitted request opens it using `os.Root`, verifies that the pre-open path, opened root and post-open path all identify the same directory, and confines descendant operations to that root. Final filenames remain simple `.wasm` names; path traversal is not accepted.

Writes preserve the existing contract:

- temporary files are uniquely named and created owner-only (`0600`);
- data is written/synced/closed before finalization;
- an existing destination must be a regular file, not a symbolic link or special file;
- replacement uses root-confined atomic rename;
- failed/oversized requests do not leave a final plugin file;
- Jul-created upload directories are owner-only (`0700`) where Unix permission bits apply.

A directory change `A → B` does not copy, migrate or delete files. Requests already pinned to A finish against A; requests captured after Publish use B. Disabling uploads does not remove files already stored on disk and does not deactivate any plugin already referenced by the running configuration. Plugin activation remains configuration-driven, not upload-driven.

Ancestor filesystem aliases needed by supported platforms (for example macOS `/var` resolving to `/private/var`) are tolerated; the configured final component itself cannot be a symlink. The request-time `os.Root` confinement plus pre/open/post same-file checks defend the final write boundary against descendant escape and path-retarget races within the guarantees of the Go runtime and host filesystem.

## Safe projections and diagnostics

Authenticated runtime overview exposes bounded effective facts:

- Console compiled/configured/effective;
- plugin runtime compiled;
- upload enabled and maximum size;
- upload-directory health category (`disabled`, `ready`, `creatable`, `invalid` or `unavailable`);
- last candidate preparation failure as a stable category;
- last upload rejection as a stable bounded category;
- the admin generation digest for correlation.

Runtime status never exposes the upload path, filename, bearer token, resolved secret or raw filesystem error. None of those values may be metric labels. The `config:read` settings projection may include the configured upload directory because it is explicitly an operator configuration surface; it still returns only fields needed by the HR-06B editor rather than raw TOML containing unrelated credentials.

Upload rejection categories are bounded (`disabled`, `too_large`, `invalid_multipart`, `missing_file`, `invalid_filename`, `invalid_wasm`, `unsupported_wasm_version`, `storage_unavailable`). Logs may include an operator-useful filename/path where existing logging policy permits, but those values are not copied into bounded runtime status or metrics.

## Typed settings operations

The Console uses two narrow operations rather than a monolithic admin mutation:

- `admin_console_set { enabled }`
- `admin_plugin_upload_set { plugin_upload: { enabled?, max_size_mb?, directory? } }`

The upload operation is sparse and requires at least one field. `max_size_mb` must be non-negative and `directory`, when supplied, must be non-empty. Preview/audit summaries name changed fields but do not echo the configured directory value. Both operations pass through the same parser/validator, reachability/self-lockout checks, lifecycle preview, persistence, prepare/publish coordinator and exact apply ledger as other managed mutations.

## Failure and concurrency model

- Invalid configuration fails before runtime preparation.
- Unusable candidate upload storage fails Prepare; no live state is published.
- A Publish race cannot mix admin generations inside one request.
- Cancellation/oversize before final rename leaves no final plugin artifact from that request; normal multipart temporary cleanup still runs.
- Request-time storage failure after a successful Publish is reported as a bounded `storage_unavailable` rejection. It is a runtime I/O failure, not a retroactive claim that the already committed configuration was not applied.
- Disabling the Console does not stop the admin server or its API.
- Changing `admin.enabled` or `admin.listen` is outside HR-06B and remains restart-bound.

## Verification expectations

Changes to this area should exercise at least:

- Console `on → off → on` in full builds and truthful fallback behavior in lean builds;
- disabled upload rejection before body read;
- upload `off/on/off`, existing-file retention and no implicit activation;
- maximum-size tighten/loosen, including an in-flight request pinned to the previous generation;
- upload directory `A → B` with an in-flight A request;
- unusable/symlink target rejection, traversal/symlink destination confinement, owner-only/atomic writes and reversible probe cleanup;
- failed Prepare preserving the prior live generation;
- safe projection/diagnostic redaction;
- exact four-field lifecycle classification while `admin.enabled/listen` remain restart-required;
- managed/raw/SIGHUP/file-watch convergence through the common prepare hook;
- race/leak, full/lean, Console frontend and real-server E2E gates in CI.

See `docs/console.md`, `docs/plugins.md`, `docs/reload-semantics.md`, `docs/security-posture.md`, `docs/known-limitations.md` and the generated configuration/lifecycle reference for the corresponding user-facing surfaces.
