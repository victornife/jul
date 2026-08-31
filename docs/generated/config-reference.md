<!--
GENERATED FILE — DO NOT EDIT.
Source of truth: internal/configcontract (config.SchemaPaths + lifecycle.BuildMetadata +
docs/config-value-contract.json + the capability/resource/description tables in this package).
Regenerate with: make config-contract-generate
CI runs `make generated-check`, which fails when this file is stale.
-->

# Configuration reference

Every public configurable TOML leaf. The normalized contract in
`internal/configcontract` is the source of truth; this page,
[`config.schema.json`](config.schema.json) and
[`config-metadata.json`](config-metadata.json) are deterministic renderings of
it. Conceptual explanations, operating guidance and examples stay in
[configuration.md](../configuration.md).

> Schema validity is necessary and not sufficient; Jul's runtime
> configuration validation (`jul check`) remains authoritative. A document may
> satisfy the generated JSON Schema and still fail `jul check`. A configuration
> may pass `jul check` while `jul lint` reports an error-severity finding —
> lint policy is never converted into structural invalidity.

Coverage: 302 configurable leaves.

## `admin.audit_log_file` {#admin-audit_log_file}

AuditLogFile, when set, enables a durable append-only audit sink: every audit event is also written as one JSON object per line (JSONL) to this file, in addition to the bounded in-memory ring buffer.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `restart_required` |
| Subsystem | `admin` |
| Why | the admin listener and its resources are created once at startup |
| Flags | startup-consumed |

## `admin.audit_log_rotate_keep` {#admin-audit_log_rotate_keep}

AuditLogRotateKeep bounds how many rotated audit backups are retained; older backups are pruned.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `restart_required` |
| Subsystem | `admin` |
| Why | the admin listener and its resources are created once at startup |
| Default | 14 |
| Flags | startup-consumed |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero defaults to 14 when audit_log_file is set |
| Active when | always |

## `admin.audit_log_rotate_max_mb` {#admin-audit_log_rotate_max_mb}

AuditLogRotateMaxMB is the size in megabytes at which the durable audit sink rotates to a timestamped backup.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `restart_required` |
| Subsystem | `admin` |
| Why | the admin listener and its resources are created once at startup |
| Default | 100 |
| Flags | startup-consumed |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero defaults to 100 when audit_log_file is set |
| Active when | always |

## `admin.console` {#admin-console}

Console toggles the web console dashboard at the admin root.

| | |
| --- | --- |
| Type | `bool` |
| Optional | yes |
| Lifecycle | `restart_required` |
| Subsystem | `admin` |
| Why | the admin listener and its resources are created once at startup |
| Requires | `console` |
| Default (conditional) | true (when admin.enabled) |
| Flags | startup-consumed |

## `admin.enabled` {#admin-enabled}

Enabled turns on the separate admin/observability listener.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `restart_required` |
| Subsystem | `admin` |
| Why | the admin listener and its resources are created once at startup |
| Flags | startup-consumed |

## `admin.history_dir` {#admin-history_dir}

HistoryDir is the directory where the console snapshots the previous configuration before each successful edit, enabling one-click rollback.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `restart_required` |
| Subsystem | `admin` |
| Why | the admin listener and its resources are created once at startup |
| Default | ./jul-data/config-history |
| Flags | startup-consumed |

## `admin.history_keep` {#admin-history_keep}

HistoryKeep bounds how many configuration snapshots are retained; older snapshots are pruned.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `restart_required` |
| Subsystem | `admin` |
| Why | the admin listener and its resources are created once at startup |
| Default | 50 |
| Flags | startup-consumed |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero defaults to 50 when admin is enabled |
| Active when | always |

## `admin.listen` {#admin-listen}

Listen is the bind address for the admin/observability listener, defaulting to loopback.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `restart_required` |
| Subsystem | `admin` |
| Why | the admin listener and its resources are created once at startup |
| Flags | startup-consumed |

## `admin.max_event_conns` {#admin-max_event_conns}

MaxEventConns bounds concurrent /api/events SSE streams per client to prevent resource exhaustion.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `restart_required` |
| Subsystem | `admin` |
| Why | the admin listener and its resources are created once at startup |
| Default | 4 |
| Flags | startup-consumed |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero defaults to 4 |
| Active when | always |

## `admin.plugin_upload_dir` {#admin-plugin_upload_dir}

PluginUploadDir is the directory where uploaded .wasm files are stored when operators use the Console Plugins panel upload feature.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `restart_required` |
| Subsystem | `admin` |
| Why | the admin listener and its resources are created once at startup |
| Flags | startup-consumed |

## `admin.plugin_upload_enabled` {#admin-plugin_upload_enabled}

PluginUploadEnabled controls whether the admin console allows uploading .wasm plugin modules.

| | |
| --- | --- |
| Type | `bool` |
| Optional | yes |
| Lifecycle | `restart_required` |
| Subsystem | `admin` |
| Why | the admin listener and its resources are created once at startup |
| Default | false |
| Flags | startup-consumed |

## `admin.plugin_upload_max_size` {#admin-plugin_upload_max_size}

PluginUploadMaxSize caps the size of an uploaded .wasm file in megabytes.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `restart_required` |
| Subsystem | `admin` |
| Why | the admin listener and its resources are created once at startup |
| Default | 32 |
| Flags | startup-consumed |
| Constraint | positive when upload is enabled; otherwise non-negative |
| Zero/empty semantics | omitted/zero defaults to 32 MiB when upload is enabled |
| Active when | always |

## `admin.rate_limit_apply_per_min` {#admin-rate_limit_apply_per_min}

RateLimitApplyPerMin caps the high-impact config validate/diff/apply endpoints per client per minute, separately and more strictly than other mutations.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `restart_required` |
| Subsystem | `admin` |
| Why | the admin listener and its resources are created once at startup |
| Default | 30 |
| Flags | startup-consumed |
| Constraint | any integer; negative disables the limiter |
| Zero/empty semantics | omitted/zero defaults to 30 |
| Active when | always |

## `admin.rate_limit_read_per_min` {#admin-rate_limit_read_per_min}

RateLimitReadPerMin caps read (GET) admin/API requests per client per minute.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `restart_required` |
| Subsystem | `admin` |
| Why | the admin listener and its resources are created once at startup |
| Default | 240 |
| Flags | startup-consumed |
| Constraint | any integer; negative disables the limiter |
| Zero/empty semantics | omitted/zero defaults to 240 |
| Active when | always |

## `admin.rate_limit_write_per_min` {#admin-rate_limit_write_per_min}

RateLimitWritePerMin caps mutating (POST/PUT/DELETE) admin requests per client per minute.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `restart_required` |
| Subsystem | `admin` |
| Why | the admin listener and its resources are created once at startup |
| Default | 60 |
| Flags | startup-consumed |
| Constraint | any integer; negative disables the limiter |
| Zero/empty semantics | omitted/zero defaults to 60 |
| Active when | always |

## `admin.rbac.default_role` {#admin-rbac-default_role}

DefaultRole is the role assigned to the synthetic "shared" legacy principal when RBAC is enabled alongside a legacy admin.token.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `rbac` |
| Why | the admin RBAC policy is rebuilt and atomically swapped after each successful reload |
| Default | admin |

## `admin.rbac.enabled` {#admin-rbac-enabled}

Enabled activates named-principal RBAC.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `hot_reload` |
| Subsystem | `rbac` |
| Why | the admin RBAC policy is rebuilt and atomically swapped after each successful reload |
| Default | false |

## `admin.rbac.principals.*.disabled` {#admin-rbac-principals-x-disabled}

Disabled, when true, prevents this principal from authenticating even if the correct token is supplied.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `hot_reload` |
| Subsystem | `rbac` |
| Why | the admin RBAC policy is rebuilt and atomically swapped after each successful reload |

## `admin.rbac.principals.*.expires_at` {#admin-rbac-principals-x-expires_at}

ExpiresAt, when non-zero, sets a hard expiry for this credential.

| | |
| --- | --- |
| Type | `time` |
| Lifecycle | `hot_reload` |
| Subsystem | `rbac` |
| Why | the admin RBAC policy is rebuilt and atomically swapped after each successful reload |

## `admin.rbac.principals.*.name` {#admin-rbac-principals-x-name}

Name is the human-readable identifier used in audit records.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `rbac` |
| Why | the admin RBAC policy is rebuilt and atomically swapped after each successful reload |

## `admin.rbac.principals.*.role` {#admin-rbac-principals-x-role}

Role is the predefined or custom role name that governs this principal's permission set.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `rbac` |
| Why | the admin RBAC policy is rebuilt and atomically swapped after each successful reload |

## `admin.rbac.principals.*.token` {#admin-rbac-principals-x-token}

Token is the raw bearer token.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `rbac` |
| Why | the admin RBAC policy is rebuilt and atomically swapped after each successful reload |
| Flags | secret |

## `admin.rbac.roles.*.name` {#admin-rbac-roles-x-name}

Name is the role identifier referenced by principals.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `rbac` |
| Why | the admin RBAC policy is rebuilt and atomically swapped after each successful reload |

## `admin.rbac.roles.*.permissions` {#admin-rbac-roles-x-permissions}

Permissions is the list of permission strings.

| | |
| --- | --- |
| Type | list of `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `rbac` |
| Why | the admin RBAC policy is rebuilt and atomically swapped after each successful reload |

## `admin.tls.cert` {#admin-tls-cert}

Cert is the path to the PEM certificate file the admin listener presents.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `tls` |
| Why | a candidate certificate provider is built and validated during preflight, then swapped atomically into the admin listener's existing dynamic provider on the next successful reload, reusing #100's seam (#336) |
| Flags | secret |

## `admin.tls.client_auth.ca_file` {#admin-tls-client_auth-ca_file}

CAFile is the PEM bundle of certificate authorities that client certificates are verified against.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `restart_required` |
| Subsystem | `mtls` |
| Why | the client CA pool is read and installed when the admin listener is created; the fingerprint digests the file contents so an in-place rotation is detected |
| Flags | startup-consumed, secret |

## `admin.tls.client_auth.crl_file` {#admin-tls-client_auth-crl_file}

CRLFile, when set, is a PEM- or DER-encoded certificate revocation list.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `restart_required` |
| Subsystem | `mtls` |
| Why | the revocation list is read and installed when the admin listener is created; the fingerprint digests the file contents so an in-place rotation is detected |
| Flags | startup-consumed, secret |

## `admin.tls.client_auth.forward_certificate` {#admin-tls-client_auth-forward_certificate}

ForwardCertificate conveys the verified client certificate to backends with the RFC 9440 Client-Cert header: "none" (default), "leaf", or "chain" (which adds Client-Cert-Chain).

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `validation_rejected_reserved` |
| Subsystem | `mtls` |
| Why | the admin API has no backend to forward a client certificate to; Validate rejects a non-none value, so no running process can have consumed it |
| Flags | reserved |

## `admin.tls.client_auth.mode` {#admin-tls-client_auth-mode}

Mode selects enforcement at the TLS handshake: "none" — off (the default).

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `restart_required` |
| Subsystem | `mtls` |
| Why | the client-certificate policy is written into the admin listener's tls.Config when it is created |
| Flags | startup-consumed |
| Allowed values | `none`, `request`, `require` |
| Constraint | exact lowercase enum |
| Zero/empty semantics | omitted selects the documented default where supported |
| Active when | client_auth configured |

## `admin.tls.client_auth.verify_san` {#admin-tls-client_auth-verify_san}

VerifySAN, when non-empty, is an allow-list of subject alternative names (DNS name, URI, email, or IP).

| | |
| --- | --- |
| Type | list of `string` |
| Lifecycle | `restart_required` |
| Subsystem | `mtls` |
| Why | the SAN allow-list is captured by the admin listener's verify callback when it is created |
| Flags | startup-consumed |

## `admin.tls.enabled` {#admin-tls-enabled}

Enabled terminates the admin listener with TLS instead of plaintext.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `restart_required` |
| Subsystem | `tls` |
| Why | turning TLS on or off for the admin listener changes the socket's protocol, a structural transition |
| Flags | startup-consumed |

## `admin.tls.key` {#admin-tls-key}

Key is the path to the PEM private key matching Cert.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `tls` |
| Why | a candidate certificate provider is built and validated during preflight, then swapped atomically into the admin listener's existing dynamic provider on the next successful reload, reusing #100's seam (#336) |
| Flags | secret |

## `admin.tls.min_version` {#admin-tls-min_version}

MinVersion is one of "1.2" or "1.3", defaulting like servers.*.tls.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `restart_required` |
| Subsystem | `tls` |
| Why | the minimum protocol version is written into the admin listener's tls.Config when it is created |
| Flags | startup-consumed |
| Allowed values | `1.2`, `1.3` |
| Constraint | exact lowercase enum |
| Zero/empty semantics | omitted selects the documented default where supported |
| Active when | admin.tls enabled |

## `admin.token` {#admin-token}

Token, when set, requires `Authorization: Bearer <token>`.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `restart_required` |
| Subsystem | `admin` |
| Why | the shared bearer token is captured when the admin listener is created; rotating it must not appear to succeed while the old token still grants access |
| Flags | startup-consumed, secret |

## `cache.default_ttl` {#cache-default_ttl}

DefaultTTL is applied when upstream gives no explicit freshness.

| | |
| --- | --- |
| Type | `duration` |
| Lifecycle | `restart_required` |
| Subsystem | `cache` |
| Why | the response cache backend is created once at startup and retains its counters and LRU state across reloads |
| Flags | startup-consumed |
| Constraint | non-negative |
| Zero/empty semantics | 0 disables that optional window/default |
| Active when | always |

## `cache.disk_max_size` {#cache-disk_max_size}

DiskMaxSize is the disk tier cap.

| | |
| --- | --- |
| Type | `size` |
| Lifecycle | `restart_required` |
| Subsystem | `cache` |
| Why | the response cache backend is created once at startup and retains its counters and LRU state across reloads |
| Flags | startup-consumed |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero defaults to 512 MiB when disk_path is set |
| Active when | always |

## `cache.disk_path` {#cache-disk_path}

DiskPath enables the disk overflow tier when non-empty.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `restart_required` |
| Subsystem | `cache` |
| Why | the response cache backend is created once at startup and retains its counters and LRU state across reloads |
| Flags | startup-consumed |

## `cache.enabled` {#cache-enabled}

Enabled turns on the two-tier response cache.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `restart_required` |
| Subsystem | `cache` |
| Why | the response cache backend is created once at startup and retains its counters and LRU state across reloads |
| Flags | startup-consumed |

## `cache.memory_max_size` {#cache-memory_max_size}

MemoryMaxSize is the in-memory tier cap.

| | |
| --- | --- |
| Type | `size` |
| Lifecycle | `restart_required` |
| Subsystem | `cache` |
| Why | the response cache backend is created once at startup and retains its counters and LRU state across reloads |
| Flags | startup-consumed |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero defaults to 64 MiB when enabled |
| Active when | always |

## `cache.stale_if_error` {#cache-stale_if_error}

StaleIfError extends the stale-serving window when a background revalidation encounters an upstream error (5xx or timeout).

| | |
| --- | --- |
| Type | `duration` |
| Lifecycle | `restart_required` |
| Subsystem | `cache` |
| Why | the response cache backend is created once at startup and retains its counters and LRU state across reloads |
| Flags | startup-consumed |
| Constraint | non-negative |
| Zero/empty semantics | 0 disables that optional window/default |
| Active when | always |

## `cache.stale_while_revalidate` {#cache-stale_while_revalidate}

StaleWhileRevalidate serves stale entries for this grace period after expiry while an async revalidation refreshes them.

| | |
| --- | --- |
| Type | `duration` |
| Lifecycle | `restart_required` |
| Subsystem | `cache` |
| Why | the response cache backend is created once at startup and retains its counters and LRU state across reloads |
| Flags | startup-consumed |
| Constraint | non-negative |
| Zero/empty semantics | 0 disables that optional window/default |
| Active when | always |

## `compression.enabled` {#compression-enabled}

Enabled turns on negotiated response compression.

| | |
| --- | --- |
| Type | `bool` |
| Optional | yes |
| Lifecycle | `hot_reload` |
| Subsystem | `compression` |
| Why | the compression middleware is rebuilt with the handler tree on each successful reload |

## `compression.encoders` {#compression-encoders}

Encoders lists allowed content codings in server-preference order, each one of "gzip", "br", or "zstd".

| | |
| --- | --- |
| Type | list of `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `compression` |
| Why | the compression middleware is rebuilt with the handler tree on each successful reload |
| Default | [gzip] |
| Allowed values | `gzip`, `br`, `zstd` |
| Constraint | exact lowercase enum |
| Zero/empty semantics | omitted selects the documented default where supported |
| Active when | compression enabled |

## `compression.level` {#compression-level}

Level is the encoder compression level; 0 selects each encoder's default.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `hot_reload` |
| Subsystem | `compression` |
| Why | the compression middleware is rebuilt with the handler tree on each successful reload |
| Constraint | 0..11 |
| Zero/empty semantics | 0 selects the encoder default |
| Active when | compression enabled |

## `compression.min_size` {#compression-min_size}

MinSize is the smallest response body that is compressed.

| | |
| --- | --- |
| Type | `size` |
| Lifecycle | `hot_reload` |
| Subsystem | `compression` |
| Why | the compression middleware is rebuilt with the handler tree on each successful reload |
| Default | 1k |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero defaults to 1 KiB when enabled |
| Active when | always |

## `compression.precompressed` {#compression-precompressed}

Precompressed serves sidecar .gz/.br files for static responses when the matching encoding is acceptable, avoiding on-the-fly recompression.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `hot_reload` |
| Subsystem | `compression` |
| Why | the compression middleware is rebuilt with the handler tree on each successful reload |

## `compression.types` {#compression-types}

Types is the MIME allow-list matched against the response Content-Type.

| | |
| --- | --- |
| Type | list of `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `compression` |
| Why | the compression middleware is rebuilt with the handler tree on each successful reload |

## `egress.allow` {#egress-allow}

Allow lists the permitted destinations.

| | |
| --- | --- |
| Type | list of `string` |
| Lifecycle | `restart_required` |
| Subsystem | `egress` |
| Why | the outbound dial policy is built once at startup and captured as an immutable set |
| Flags | startup-consumed |

## `egress.enabled` {#egress-enabled}

Enabled turns the allow-list on.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `restart_required` |
| Subsystem | `egress` |
| Why | the outbound dial policy is built once at startup and captured as an immutable set |
| Flags | startup-consumed |

## `global.access_log` {#global-access_log}

AccessLog is the destination for access records (e.g. a file path or "stdout").

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `ignored_deprecated` |
| Subsystem | `access_log` |
| Why | superseded by observability.access_log; no runtime consumer reads it |
| Flags | deprecated, ignored |

## `global.config_authority` {#global-config_authority}

ConfigAuthority declares who owns configuration persistence, managed history, and drift detection: "managed" (Jul owns the file; Console/API writes are subject to RBAC and CAS, and external edits become drift that must be explicitly adopted) or "file_owned" (an external file or GitOps pipeline owns the file; every mutating admin endpoint is refused and the file watcher/SIGHUP behave exactly as they do without this field).

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `restart_required` |
| Subsystem | `config_authority` |
| Why | authority is resolved once at startup and wires the file watcher, SIGHUP, and the managed baseline before any writer exists; changing it moves ownership of the configuration file and cannot be hot-applied (ADR 0019 §9.2) |
| Default | file_owned |
| Flags | startup-consumed |
| Allowed values | `managed`, `file_owned` |
| Constraint | exact lowercase enum; controller_owned is reserved and rejected |
| Zero/empty semantics | omitted resolves to file_owned, a fixed default that is never derived from another field |
| Active when | always |

## `global.error_log` {#global-error_log}

ErrorLog is the legacy destination for error records, kept for v1 compatibility.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `ignored_deprecated` |
| Subsystem | `error_log` |
| Why | structured process logs are written to stderr; no runtime consumer reads it |
| Flags | deprecated, ignored |

## `global.log_format` {#global-log_format}

LogFormat is "text" (human readable, default in dev) or "json".

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `restart_required` |
| Subsystem | `log_format` |
| Why | the slog handler encoding is chosen once when the logger is built at startup |
| Default | text |
| Flags | startup-consumed |
| Allowed values | `text`, `json` |
| Constraint | exact lowercase enum |
| Zero/empty semantics | omitted defaults to text |
| Active when | always |

## `global.log_level` {#global-log_level}

LogLevel is one of debug, info, warn, error.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `log_level` |
| Why | the level var is updated by OnReloaded on each successful reload |
| Default | info |
| Allowed values | `debug`, `info`, `warn`, `error` |
| Constraint | exact lowercase enum |
| Zero/empty semantics | omitted defaults to info |
| Active when | always |

## `global.redact_min_secret_length` {#global-redact_min_secret_length}

RedactMinSecretLength is the shortest resolved secret value that is masked from logs.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `hot_reload` |
| Subsystem | `redact` |
| Why | the redaction state is rebuilt and installed atomically on each successful reload |
| Default | 4 |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero uses the default floor |
| Active when | always |

## `global.reload_timeout` {#global-reload_timeout}

ReloadTimeout is how long a hot reload may run before it is reported as timed out.

| | |
| --- | --- |
| Type | `duration` |
| Lifecycle | `hot_reload` |
| Subsystem | `reload_timeout` |
| Why | the threshold is read from the effective config at the start of each reload |
| Default | 10s |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero defaults to 10s |
| Active when | always |

## `global.shutdown_timeout` {#global-shutdown_timeout}

ShutdownTimeout is how long to wait for in-flight requests to drain.

| | |
| --- | --- |
| Type | `duration` |
| Lifecycle | `hot_reload` |
| Subsystem | `shutdown_timeout` |
| Why | the drain budget is read from the effective config on each graceful stop |
| Default | 30s |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero defaults to 30s |
| Active when | always |

## `global.worker_threads` {#global-worker_threads}

WorkerThreads accepts "auto" or a positive integer as a string.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `worker_threads` |
| Why | OnReloaded applies the cap with runtime.GOMAXPROCS, restoring the container-aware default for "auto" |
| Default | auto |
| Constraint | auto or a canonical positive base-10 integer |
| Zero/empty semantics | omitted/empty and auto use the Go runtime default |
| Active when | always |

## `observability.access_log.enabled` {#observability-access_log-enabled}

Enabled controls whether Jul emits request access records.

| | |
| --- | --- |
| Type | `bool` |
| Optional | yes |
| Lifecycle | `hot_reload` |
| Subsystem | `access_log` |
| Why | a candidate sink generation is built and validated before Publish, then swapped in with the new handler generation; the previous generation's file/syslog resources close only after its requests drain (#98) |
| Default | true |
| Constraint | true or false |
| Zero/empty semantics | omitted preserves the v1 default-on behavior; explicit false disables request access records |
| Active when | always |

## `observability.access_log.file` {#observability-access_log-file}

File is the path of the access-log file.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `access_log` |
| Why | a candidate sink generation is built and validated before Publish, then swapped in with the new handler generation; the previous generation's file/syslog resources close only after its requests drain (#98) |

## `observability.access_log.format` {#observability-access_log-format}

Format selects the encoding of the file and syslog sinks: "text" (logfmt, the default) or "json".

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `access_log` |
| Why | a candidate sink generation is built and validated before Publish, then swapped in with the new handler generation; the previous generation's file/syslog resources close only after its requests drain (#98) |
| Default | text |
| Allowed values | `text`, `json` |
| Constraint | exact lowercase enum |
| Zero/empty semantics | omitted selects the documented default where supported |
| Active when | access logging active |

## `observability.access_log.rotate_keep` {#observability-access_log-rotate_keep}

RotateKeep is the maximum number of rotated files to retain.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `hot_reload` |
| Subsystem | `access_log` |
| Why | a candidate sink generation is built and validated before Publish, then swapped in with the new handler generation; the previous generation's file/syslog resources close only after its requests drain (#98) |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero defaults to 7 |
| Active when | always |

## `observability.access_log.rotate_max_mb` {#observability-access_log-rotate_max_mb}

RotateMaxMB is the file size in megabytes at which the log is rotated.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `hot_reload` |
| Subsystem | `access_log` |
| Why | a candidate sink generation is built and validated before Publish, then swapped in with the new handler generation; the previous generation's file/syslog resources close only after its requests drain (#98) |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero defaults to 100 |
| Active when | always |

## `observability.access_log.sinks` {#observability-access_log-sinks}

Sinks selects the active access-log destinations: any of "stdout" (the server's structured logger), "file", and "syslog".

| | |
| --- | --- |
| Type | list of `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `access_log` |
| Why | a candidate sink generation is built and validated before Publish, then swapped in with the new handler generation; the previous generation's file/syslog resources close only after its requests drain (#98) |
| Allowed values | `stdout`, `file`, `syslog` |
| Constraint | exact lowercase enum |
| Zero/empty semantics | omitted selects the documented default where supported |
| Active when | access logging active |

## `observability.metrics.host_label` {#observability-metrics-host_label}

HostLabel adds the request Host as the "host" label on jul_http_requests_total and jul_http_request_duration_seconds.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `restart_required` |
| Subsystem | `metrics` |
| Why | the Prometheus registry and its label set are built once at startup |
| Flags | startup-consumed |

## `observability.tracing.enabled` {#observability-tracing-enabled}

Enabled turns on OpenTelemetry distributed tracing.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `restart_required` |
| Subsystem | `tracing` |
| Why | the tracer provider and exporter are created once at startup |
| Requires | `otel` |
| Flags | startup-consumed |

## `observability.tracing.endpoint` {#observability-tracing-endpoint}

Endpoint is the collector address: "host:port" for gRPC or a URL/host for HTTP.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `restart_required` |
| Subsystem | `tracing` |
| Why | the tracer provider and exporter are created once at startup |
| Requires | `otel` |
| Flags | startup-consumed |

## `observability.tracing.exporter` {#observability-tracing-exporter}

Exporter selects the OTLP transport: "otlp-grpc" (default) or "otlp-http".

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `restart_required` |
| Subsystem | `tracing` |
| Why | the tracer provider and exporter are created once at startup |
| Requires | `otel` |
| Default | otlp-grpc |
| Flags | startup-consumed |
| Allowed values | `otlp-grpc`, `otlp-http` |
| Constraint | exact lowercase enum |
| Zero/empty semantics | omitted selects the documented default where supported |
| Active when | tracing enabled |

## `observability.tracing.insecure` {#observability-tracing-insecure}

Insecure sends spans over plaintext instead of TLS.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `restart_required` |
| Subsystem | `tracing` |
| Why | the tracer provider and exporter are created once at startup |
| Requires | `otel` |
| Flags | startup-consumed |

## `observability.tracing.sample_ratio` {#observability-tracing-sample_ratio}

SampleRatio is the head-based sampling probability for root spans, in the range [0,1].

| | |
| --- | --- |
| Type | `float` |
| Lifecycle | `restart_required` |
| Subsystem | `tracing` |
| Why | the tracer provider and exporter are created once at startup |
| Requires | `otel` |
| Default | 1 |
| Flags | startup-consumed |
| Constraint | 0..1 |
| Zero/empty semantics | omitted/zero defaults to 1.0 when tracing is enabled |
| Active when | tracing enabled |

## `observability.tracing.service_name` {#observability-tracing-service_name}

ServiceName sets the OpenTelemetry resource service.name.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `restart_required` |
| Subsystem | `tracing` |
| Why | the tracer provider and exporter are created once at startup |
| Requires | `otel` |
| Default | jul |
| Flags | startup-consumed |

## `plugins.*.allowed_hosts` {#plugins-x-allowed_hosts}

AllowedHosts is the allowlist of hosts the guest may fetch from when Fetch is granted.

| | |
| --- | --- |
| Type | list of `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `plugins` |
| Why | the plugin set is rebuilt and re-instantiated on each successful reload |
| Requires | `wasm_plugins` |

## `plugins.*.config.*` {#plugins-x-config-x}

Config is an arbitrary string map handed to the guest as a JSON object via the get_config host function.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `plugins` |
| Why | the plugin set is rebuilt and re-instantiated on each successful reload |
| Requires | `wasm_plugins` |

## `plugins.*.fetch` {#plugins-x-fetch}

Fetch grants the guarded outbound HTTP fetch host function.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `hot_reload` |
| Subsystem | `plugins` |
| Why | the plugin set is rebuilt and re-instantiated on each successful reload |
| Requires | `wasm_plugins` |

## `plugins.*.fetch_timeout` {#plugins-x-fetch_timeout}

FetchTimeout bounds a single outbound fetch.

| | |
| --- | --- |
| Type | `duration` |
| Lifecycle | `hot_reload` |
| Subsystem | `plugins` |
| Why | the plugin set is rebuilt and re-instantiated on each successful reload |
| Requires | `wasm_plugins` |
| Default | 5s |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero applies the documented plugin default |
| Active when | always |

## `plugins.*.inline` {#plugins-x-inline}

Inline is the module bytes encoded as standard base64, an alternative to Path for self-contained configs.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `plugins` |
| Why | the plugin set is rebuilt and re-instantiated on each successful reload |
| Requires | `wasm_plugins` |

## `plugins.*.kv` {#plugins-x-kv}

KV grants access to the plugin key/value store host functions.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `hot_reload` |
| Subsystem | `plugins` |
| Why | the plugin set is rebuilt and re-instantiated on each successful reload |
| Requires | `wasm_plugins` |

## `plugins.*.kv_max_bytes` {#plugins-x-kv_max_bytes}

KVMaxBytes caps total stored bytes per plugin.

| | |
| --- | --- |
| Type | `size` |
| Lifecycle | `hot_reload` |
| Subsystem | `plugins` |
| Why | the plugin set is rebuilt and re-instantiated on each successful reload |
| Requires | `wasm_plugins` |
| Default | 1m |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero applies the documented plugin default |
| Active when | always |

## `plugins.*.kv_max_entries` {#plugins-x-kv_max_entries}

KVMaxEntries caps distinct keys per plugin.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `hot_reload` |
| Subsystem | `plugins` |
| Why | the plugin set is rebuilt and re-instantiated on each successful reload |
| Requires | `wasm_plugins` |
| Default | 1024 |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero defaults to 1024 |
| Active when | always |

## `plugins.*.max_fetch_response` {#plugins-x-max_fetch_response}

MaxFetchResponse caps a fetch response body.

| | |
| --- | --- |
| Type | `size` |
| Lifecycle | `hot_reload` |
| Subsystem | `plugins` |
| Why | the plugin set is rebuilt and re-instantiated on each successful reload |
| Requires | `wasm_plugins` |
| Default | 1m |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero applies the documented plugin default |
| Active when | always |

## `plugins.*.max_request_body` {#plugins-x-max_request_body}

MaxRequestBody caps the request body the host buffers for a guest.

| | |
| --- | --- |
| Type | `size` |
| Lifecycle | `hot_reload` |
| Subsystem | `plugins` |
| Why | the plugin set is rebuilt and re-instantiated on each successful reload |
| Requires | `wasm_plugins` |
| Default | 1m |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero applies the documented plugin default |
| Active when | always |

## `plugins.*.max_response_body` {#plugins-x-max_response_body}

MaxResponseBody caps the response body a guest may accumulate.

| | |
| --- | --- |
| Type | `size` |
| Lifecycle | `hot_reload` |
| Subsystem | `plugins` |
| Why | the plugin set is rebuilt and re-instantiated on each successful reload |
| Requires | `wasm_plugins` |
| Default | 8m |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero applies the documented plugin default |
| Active when | always |

## `plugins.*.memory_limit` {#plugins-x-memory_limit}

MemoryLimit caps the guest's linear memory.

| | |
| --- | --- |
| Type | `size` |
| Lifecycle | `hot_reload` |
| Subsystem | `plugins` |
| Why | the plugin set is rebuilt and re-instantiated on each successful reload |
| Requires | `wasm_plugins` |
| Default | 16m |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero applies the documented plugin default |
| Active when | always |

## `plugins.*.path` {#plugins-x-path}

Path is the filesystem path to the .wasm module.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `plugins` |
| Why | the plugin set is rebuilt and re-instantiated on each successful reload |
| Requires | `wasm_plugins` |

## `plugins.*.timeout` {#plugins-x-timeout}

Timeout bounds a single guest invocation.

| | |
| --- | --- |
| Type | `duration` |
| Lifecycle | `hot_reload` |
| Subsystem | `plugins` |
| Why | the plugin set is rebuilt and re-instantiated on each successful reload |
| Requires | `wasm_plugins` |
| Default | 100ms |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero applies the documented plugin default |
| Active when | always |

## `plugins.*.type` {#plugins-x-type}

Type is "middleware" (wraps a handler, may pass through) or "handler" (a terminal location action).

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `plugins` |
| Why | the plugin set is rebuilt and re-instantiated on each successful reload |
| Requires | `wasm_plugins` |
| Default | middleware |
| Allowed values | `middleware`, `handler` |
| Constraint | exact lowercase enum |
| Zero/empty semantics | omitted selects the documented default where supported |
| Active when | plugin configured |

## `rate_limit.burst` {#rate_limit-burst}

Burst is the maximum momentary burst above Rate.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `hot_reload` |
| Subsystem | `rate_limit` |
| Why | the rate-limiter store accepts a new policy on each successful reload |
| Constraint | at least rate |
| Zero/empty semantics | omitted/zero defaults to rate |
| Active when | rate limit enabled |

## `rate_limit.enabled` {#rate_limit-enabled}

Enabled turns on request rate limiting.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `hot_reload` |
| Subsystem | `rate_limit` |
| Why | the rate-limiter store accepts a new policy on each successful reload |

## `rate_limit.key` {#rate_limit-key}

Key selects the bucket identity: "ip" (client address, the default), "header:<Name>" (a request header value), or "jwt:<claim>" (a validated JWT claim once auth is configured).

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `rate_limit` |
| Why | the rate-limiter store accepts a new policy on each successful reload |
| Default | ip |
| Constraint | ip, header:<Name>, or jwt:<claim> |
| Zero/empty semantics | omitted defaults to ip |
| Active when | rate limit enabled |

## `rate_limit.max_conns` {#rate_limit-max_conns}

MaxConns caps concurrent connections per listener (0 = unlimited).

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `new_listener_only` |
| Subsystem | `rate_limit` |
| Why | the concurrent-connection cap is installed on each listener when it binds, so a kept address keeps the cap it bound with |
| Flags | conditional |
| Constraint | non-negative |
| Zero/empty semantics | 0 means unlimited |
| Active when | global rate limit enabled |

## `rate_limit.rate` {#rate_limit-rate}

Rate is the sustained requests/second allowed per key.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `hot_reload` |
| Subsystem | `rate_limit` |
| Why | the rate-limiter store accepts a new policy on each successful reload |
| Constraint | positive |
| Zero/empty semantics | no valid zero while enabled |
| Active when | rate limit enabled |

## `servers.*.access_log` {#servers-x-access_log}

AccessLog overrides the global access-log destination for this server block.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `ignored_deprecated` |
| Subsystem | `access_log` |
| Why | superseded by observability.access_log; no runtime consumer reads it |
| Flags | deprecated, ignored |

## `servers.*.client_address.forwarded_headers` {#servers-x-client_address-forwarded_headers}

ForwardedHeaders is the ordered preference of forwarding headers: "forwarded" (RFC 7239) and "x-forwarded-for".

| | |
| --- | --- |
| Type | list of `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `client_address` |
| Why | the trusted-proxy policy is recompiled per listen address while the handler tree is prepared, so a malformed prefix aborts the reload before publish |
| Default | [forwarded, x-forwarded-for] |
| Allowed values | `forwarded`, `x-forwarded-for` |
| Constraint | ordered list of exact lowercase enum values, each listed at most once |
| Zero/empty semantics | omitted defaults to [forwarded, x-forwarded-for]; an explicitly empty list disables every forwarding header |
| Active when | the listener trusts at least one proxy |

## `servers.*.client_address.max_hops` {#servers-x-client_address-max_hops}

MaxHops bounds how many asserted hops a chain may carry.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `hot_reload` |
| Subsystem | `client_address` |
| Why | the trusted-proxy policy is recompiled per listen address while the handler tree is prepared, so a malformed prefix aborts the reload before publish |
| Default | 16 |
| Constraint | non-negative, at most 255 |
| Zero/empty semantics | 0 selects the default of 16 |
| Active when | the listener trusts at least one proxy |

## `servers.*.client_address.trusted_proxies` {#servers-x-client_address-trusted_proxies}

TrustedProxies lists the CIDR prefixes (or bare addresses, meaning a single host) whose forwarding headers are believed.

| | |
| --- | --- |
| Type | list of `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `client_address` |
| Why | the trusted-proxy policy is recompiled per listen address while the handler tree is prepared, so a malformed prefix aborts the reload before publish |
| Constraint | IP addresses or canonical CIDR prefixes with host bits clear; no hostnames and no shorthands |
| Zero/empty semantics | omitted or empty trusts no proxy, so forwarding headers are never read |
| Active when | always |

## `servers.*.client_max_body_size` {#servers-x-client_max_body_size}

Limits and timeouts (per-server defaults; locations may override).

| | |
| --- | --- |
| Type | `size` |
| Lifecycle | `hot_reload` |
| Subsystem | `server_limits` |
| Why | the handler reads the effective limit per request |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero defaults to 1 MiB |
| Active when | always |

## `servers.*.error_log` {#servers-x-error_log}

ErrorLog overrides the global legacy error-log destination for this server block.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `ignored_deprecated` |
| Subsystem | `error_log` |
| Why | structured process logs are written to stderr; no runtime consumer reads it |
| Flags | deprecated, ignored |

## `servers.*.error_pages.*` {#servers-x-error_pages-x}

ErrorPages maps a status code to a file path or redirect URL.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `error_pages` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |

## `servers.*.h2c` {#servers-x-h2c}

H2C enables cleartext HTTP/2 (h2c) on this listener so native gRPC and other HTTP/2 clients can connect without TLS, in addition to HTTP/1.1.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `restart_required` |
| Subsystem | `h2c` |
| Why | h2c is negotiated by the plaintext listener created at bind time |
| Flags | startup-consumed, per-address, conditional |

## `servers.*.http3.alt_svc_max_age` {#servers-x-http3-alt_svc_max_age}

AltSvcMaxAge is the Alt-Svc advertisement lifetime in seconds (the "ma" field), i.e. how long a client may keep using HTTP/3 before re-checking.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `restart_required` |
| Subsystem | `http3` |
| Why | the QUIC listener and its Alt-Svc advertisement are created when the address binds |
| Requires | `http3` |
| Flags | startup-consumed, per-address, conditional |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero defaults to 86400 |
| Active when | HTTP/3 enabled |

## `servers.*.http3.enabled` {#servers-x-http3-enabled}

Enabled starts a parallel HTTP/3 (QUIC) listener for this server block.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `restart_required` |
| Subsystem | `http3` |
| Why | the QUIC listener and its Alt-Svc advertisement are created when the address binds |
| Requires | `http3` |
| Flags | startup-consumed, per-address, conditional |

## `servers.*.idle_timeout` {#servers-x-idle_timeout}

IdleTimeout closes an idle keep-alive connection after this period.

| | |
| --- | --- |
| Type | `duration` |
| Lifecycle | `new_listener_only` |
| Subsystem | `listener_timeouts` |
| Why | the value is read once when the socket binds; an address kept across the reload keeps the value it bound with |
| Flags | conditional |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero defaults to 60s |
| Active when | always |

## `servers.*.listen` {#servers-x-listen}

Listen is the bind address (host:port) this server block accepts connections on.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `new_listener_only` |
| Subsystem | `listener` |
| Why | moving to a different address binds a new socket; the old address is drained |
| Flags | conditional |

## `servers.*.locations.*.allow_hidden` {#servers-x-locations-x-allow_hidden}

AllowHidden permits serving dotfiles and other hidden paths from the document root.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `hot_reload` |
| Subsystem | `static_files` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |

## `servers.*.locations.*.auth.allow` {#servers-x-locations-x-auth-allow}

Allow and Deny are CIDR allow/deny lists evaluated before any credential check.

| | |
| --- | --- |
| Type | list of `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `auth` |
| Why | auth modifiers are rebuilt around each location action on every successful reload |

## `servers.*.locations.*.auth.basic.file` {#servers-x-locations-x-auth-basic-file}

File is the path to an htpasswd file with bcrypt-hashed passwords.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `auth` |
| Why | auth modifiers are rebuilt around each location action on every successful reload |

## `servers.*.locations.*.auth.basic.realm` {#servers-x-locations-x-auth-basic-realm}

Realm is the authentication realm presented in the challenge.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `auth` |
| Why | auth modifiers are rebuilt around each location action on every successful reload |
| Default | Restricted |

## `servers.*.locations.*.auth.deny` {#servers-x-locations-x-auth-deny}

Deny is a CIDR list evaluated before any credential check; a matching client is rejected outright.

| | |
| --- | --- |
| Type | list of `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `auth` |
| Why | auth modifiers are rebuilt around each location action on every successful reload |

## `servers.*.locations.*.auth.forward_auth.auth_response_headers` {#servers-x-locations-x-auth-forward_auth-auth_response_headers}

AuthResponseHeaders lists response headers copied from the auth endpoint onto the upstream request when the decision is allow.

| | |
| --- | --- |
| Type | list of `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `auth` |
| Why | auth modifiers are rebuilt around each location action on every successful reload |

## `servers.*.locations.*.auth.forward_auth.timeout` {#servers-x-locations-x-auth-forward_auth-timeout}

Timeout bounds one forward-auth subrequest.

| | |
| --- | --- |
| Type | `duration` |
| Lifecycle | `hot_reload` |
| Subsystem | `auth` |
| Why | auth modifiers are rebuilt around each location action on every successful reload |
| Default | 10s |
| Constraint | 0s to 60s |
| Zero/empty semantics | omitted/zero means 10s |
| Active when | forward_auth configured |

## `servers.*.locations.*.auth.forward_auth.url` {#servers-x-locations-x-auth-forward_auth-url}

URL receives a subrequest carrying the original request's method, path, and headers.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `auth` |
| Why | auth modifiers are rebuilt around each location action on every successful reload |

## `servers.*.locations.*.auth.jwt.algorithms` {#servers-x-locations-x-auth-jwt-algorithms}

Algorithms is the allow-list of accepted signing algorithms.

| | |
| --- | --- |
| Type | list of `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `auth` |
| Why | auth modifiers are rebuilt around each location action on every successful reload |

## `servers.*.locations.*.auth.jwt.audience` {#servers-x-locations-x-auth-jwt-audience}

Audience, when set, must be present in the token's "aud" claim.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `auth` |
| Why | auth modifiers are rebuilt around each location action on every successful reload |

## `servers.*.locations.*.auth.jwt.issuer` {#servers-x-locations-x-auth-jwt-issuer}

Issuer, when set, must equal the token's "iss" claim.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `auth` |
| Why | auth modifiers are rebuilt around each location action on every successful reload |

## `servers.*.locations.*.auth.jwt.jwks_url` {#servers-x-locations-x-auth-jwt-jwks_url}

JWKSURL is the JSON Web Key Set endpoint serving the issuer's public keys.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `auth` |
| Why | auth modifiers are rebuilt around each location action on every successful reload |

## `servers.*.locations.*.auth.jwt.timeout` {#servers-x-locations-x-auth-jwt-timeout}

Timeout bounds one JWKS fetch.

| | |
| --- | --- |
| Type | `duration` |
| Lifecycle | `hot_reload` |
| Subsystem | `auth` |
| Why | auth modifiers are rebuilt around each location action on every successful reload |
| Default | 10s |
| Constraint | 0s to 60s |
| Zero/empty semantics | omitted/zero means 10s |
| Active when | jwt configured |

## `servers.*.locations.*.backend_tls.ca_file` {#servers-x-locations-x-backend_tls-ca_file}

CAFile is a PEM bundle of trust roots.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `backend_tls` |
| Why | the route's outbound clients (HTTP transport, native gRPC transport, transcoder connections) are built with the handler generation that owns them, so a changed policy takes effect on the next successful reload |
| Flags | secret |

## `servers.*.locations.*.backend_tls.ca_mode` {#servers-x-locations-x-backend_tls-ca_mode}

CAMode is "system" (default), "system_and_file" or "file_only".

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `backend_tls` |
| Why | the route's outbound clients (HTTP transport, native gRPC transport, transcoder connections) are built with the handler generation that owns them, so a changed policy takes effect on the next successful reload |
| Default | system |
| Allowed values | `system`, `system_and_file`, `file_only` |
| Constraint | exact lowercase enum; never inferred from the presence of ca_file |
| Zero/empty semantics | omitted means system roots only |
| Active when | the backend is reached over TLS |

## `servers.*.locations.*.backend_tls.client_cert` {#servers-x-locations-x-backend_tls-client_cert}

ClientCert and ClientKey are the client certificate presented to the backend (mutual TLS).

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `backend_tls` |
| Why | the route's outbound clients (HTTP transport, native gRPC transport, transcoder connections) are built with the handler generation that owns them, so a changed policy takes effect on the next successful reload |
| Flags | secret |

## `servers.*.locations.*.backend_tls.client_key` {#servers-x-locations-x-backend_tls-client_key}

ClientKey is the private key paired with client_cert for backend mutual TLS; both are required together.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `backend_tls` |
| Why | the route's outbound clients (HTTP transport, native gRPC transport, transcoder connections) are built with the handler generation that owns them, so a changed policy takes effect on the next successful reload |
| Flags | secret |

## `servers.*.locations.*.backend_tls.insecure_skip_verify` {#servers-x-locations-x-backend_tls-insecure_skip_verify}

InsecureSkipVerify disables backend certificate verification.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `hot_reload` |
| Subsystem | `backend_tls` |
| Why | the route's outbound clients (HTTP transport, native gRPC transport, transcoder connections) are built with the handler generation that owns them, so a changed policy takes effect on the next successful reload |

## `servers.*.locations.*.backend_tls.min_version` {#servers-x-locations-x-backend_tls-min_version}

MinVersion is "1.2" (default) or "1.3".

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `backend_tls` |
| Why | the route's outbound clients (HTTP transport, native gRPC transport, transcoder connections) are built with the handler generation that owns them, so a changed policy takes effect on the next successful reload |
| Default | 1.2 |
| Allowed values | `1.2`, `1.3` |
| Constraint | exact lowercase enum |
| Zero/empty semantics | omitted defaults to 1.2, matching Go's client default |
| Active when | the backend is reached over TLS |

## `servers.*.locations.*.backend_tls.peer_identities` {#servers-x-locations-x-backend_tls-peer_identities}

PeerIdentities are prefixed identities ("dns:name", "uri:spiffe://...") matched against the backend certificate after standard verification.

| | |
| --- | --- |
| Type | list of `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `backend_tls` |
| Why | the route's outbound clients (HTTP transport, native gRPC transport, transcoder connections) are built with the handler generation that owns them, so a changed policy takes effect on the next successful reload |
| Constraint | prefixed identities: dns:<name> or uri:<uri>; ORed, matched after standard verification, never by regex or substring |
| Zero/empty semantics | omitted or empty applies standard chain and name verification only |
| Active when | the backend is reached over TLS and insecure_skip_verify is false |

## `servers.*.locations.*.backend_tls.server_name` {#servers-x-locations-x-backend_tls-server_name}

ServerName overrides the verified identity and the SNI value.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `backend_tls` |
| Why | the route's outbound clients (HTTP transport, native gRPC transport, transcoder connections) are built with the handler generation that owns them, so a changed policy takes effect on the next successful reload |

## `servers.*.locations.*.cache` {#servers-x-locations-x-cache}

Caching toggle for this location (requires [cache].enabled).

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `hot_reload` |
| Subsystem | `cache` |
| Why | whether a location may serve from the cache is decided by the rebuilt handler tree; the backend itself is startup-owned |

## `servers.*.locations.*.cache_control` {#servers-x-locations-x-cache_control}

CacheControl sets a fixed Cache-Control response header value for this location.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `static_files` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |

## `servers.*.locations.*.client_max_body_size` {#servers-x-locations-x-client_max_body_size}

ClientMaxBodySize overrides the server default for this location.

| | |
| --- | --- |
| Type | `size` |
| Lifecycle | `hot_reload` |
| Subsystem | `server_limits` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |
| Constraint | non-negative |
| Zero/empty semantics | 0 inherits the server value |
| Active when | always |

## `servers.*.locations.*.cors.allow_credentials` {#servers-x-locations-x-cors-allow_credentials}

AllowCredentials emits Access-Control-Allow-Credentials: true on a granted response.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `hot_reload` |
| Subsystem | `cors` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |

## `servers.*.locations.*.cors.allowed_headers` {#servers-x-locations-x-cors-allowed_headers}

AllowedHeaders governs preflight approval only.

| | |
| --- | --- |
| Type | list of `string` |
| Optional | yes |
| Lifecycle | `hot_reload` |
| Subsystem | `cors` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |

## `servers.*.locations.*.cors.allowed_methods` {#servers-x-locations-x-cors-allowed_methods}

AllowedMethods governs preflight approval only, never ordinary requests (that is match.methods).

| | |
| --- | --- |
| Type | list of `string` |
| Optional | yes |
| Lifecycle | `hot_reload` |
| Subsystem | `cors` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |
| Default | [GET, HEAD, POST] |

## `servers.*.locations.*.cors.allowed_origins` {#servers-x-locations-x-cors-allowed_origins}

AllowedOrigins is required when Enabled.

| | |
| --- | --- |
| Type | list of `string` |
| Optional | yes |
| Lifecycle | `hot_reload` |
| Subsystem | `cors` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |
| Constraint | exact scheme://host[:port] origins (no path, no explicitly-written default port), or exactly ["*"], which is unconditional and forbids any other entry or allow_credentials; at most 64 entries, 256 bytes each |
| Zero/empty semantics | required when enabled = true; there is no default |
| Active when | location configured |

## `servers.*.locations.*.cors.enabled` {#servers-x-locations-x-cors-enabled}

Enabled turns on this location's CORS policy.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `hot_reload` |
| Subsystem | `cors` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |

## `servers.*.locations.*.cors.exposed_headers` {#servers-x-locations-x-cors-exposed_headers}

ExposedHeaders is emitted as Access-Control-Expose-Headers on a granted response.

| | |
| --- | --- |
| Type | list of `string` |
| Optional | yes |
| Lifecycle | `hot_reload` |
| Subsystem | `cors` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |

## `servers.*.locations.*.cors.max_age` {#servers-x-locations-x-cors-max_age}

MaxAge is Access-Control-Max-Age, in whole seconds, 0 to 24h.

| | |
| --- | --- |
| Type | `duration` |
| Optional | yes |
| Lifecycle | `hot_reload` |
| Subsystem | `cors` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |
| Constraint | whole seconds only; 0 to 24h |
| Zero/empty semantics | omitted emits no Access-Control-Max-Age header; "0s" is legal and emits 0 |
| Active when | cors.enabled = true |

## `servers.*.locations.*.deny` {#servers-x-locations-x-deny}

Deny rejects matching requests with 403.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `hot_reload` |
| Subsystem | `access_control` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |

## `servers.*.locations.*.directory_listing` {#servers-x-locations-x-directory_listing}

DirectoryListing serves an auto-generated index when a directory has no index file.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `hot_reload` |
| Subsystem | `static_files` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |

## `servers.*.locations.*.fastcgi_params.*` {#servers-x-locations-x-fastcgi_params-x}

FastCGIParams are additional FastCGI protocol parameters passed to the backend.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `fastcgi` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |

## `servers.*.locations.*.fastcgi_pass` {#servers-x-locations-x-fastcgi_pass}

FastCGI / uWSGI.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `fastcgi` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |

## `servers.*.locations.*.grpc` {#servers-x-locations-x-grpc}

GRPC turns the proxy_pass into a native gRPC / HTTP-2 passthrough: the request is forwarded end-to-end over HTTP/2 (preserving trailers such as grpc-status) with response buffering disabled so streaming frames flush immediately.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `hot_reload` |
| Subsystem | `grpc` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |
| Requires | `grpc` |

## `servers.*.locations.*.grpc_transcode.descriptor_set` {#servers-x-locations-x-grpc_transcode-descriptor_set}

DescriptorSet is the path to a protoc-generated FileDescriptorSet (protoc --descriptor_set_out with --include_imports).

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `grpc_transcode` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |
| Requires | `grpc` |

## `servers.*.locations.*.grpc_transcode.max_message_size` {#servers-x-locations-x-grpc_transcode-max_message_size}

MaxMessageSize caps a single encoded message (a JSON request frame or a gRPC reply).

| | |
| --- | --- |
| Type | `size` |
| Lifecycle | `hot_reload` |
| Subsystem | `grpc_transcode` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |
| Requires | `grpc` |
| Default | 4m |
| Constraint | non-negative |
| Zero/empty semantics | 0 applies the 4 MiB default |
| Active when | always |

## `servers.*.locations.*.grpc_transcode.preserve_proto_field_names` {#servers-x-locations-x-grpc_transcode-preserve_proto_field_names}

PreserveNames keeps original proto field names in JSON output instead of the default lowerCamelCase.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `hot_reload` |
| Subsystem | `grpc_transcode` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |
| Requires | `grpc` |
| Default | false |

## `servers.*.locations.*.grpc_transcode.stream_mode` {#servers-x-locations-x-grpc_transcode-stream_mode}

StreamMode selects the wire framing for streamed responses: "ndjson" (newline-delimited JSON objects, the default) or "sse" (Server-Sent Events).

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `grpc_transcode` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |
| Requires | `grpc` |
| Default | ndjson |
| Allowed values | `ndjson`, `sse` |
| Constraint | exact lowercase enum |
| Zero/empty semantics | omitted selects the documented default where supported |
| Active when | transcoder configured |

## `servers.*.locations.*.grpc_transcode.streaming` {#servers-x-locations-x-grpc_transcode-streaming}

Streaming enables transcoding of streaming methods (server-streaming, client-streaming, and bidirectional).

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `hot_reload` |
| Subsystem | `grpc_transcode` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |
| Requires | `grpc` |

## `servers.*.locations.*.grpc_transcode.target` {#servers-x-locations-x-grpc_transcode-target}

Target is the gRPC backend: an upstream name or a concrete "host:port".

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `grpc_transcode` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |
| Requires | `grpc` |

## `servers.*.locations.*.grpc_transcode.tls` {#servers-x-locations-x-grpc_transcode-tls}

TLS dials the backend over TLS; otherwise plaintext HTTP/2 (h2c).

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `hot_reload` |
| Subsystem | `grpc_transcode` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |
| Requires | `grpc` |

## `servers.*.locations.*.grpc_transcode.use_reflection` {#servers-x-locations-x-grpc_transcode-use_reflection}

UseReflection fetches descriptors from the backend via gRPC server reflection instead of reading a DescriptorSet file.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `hot_reload` |
| Subsystem | `grpc_transcode` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |
| Requires | `grpc` |

## `servers.*.locations.*.headers.*` {#servers-x-locations-x-headers-x}

Headers added/overridden on the upstream request.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `headers` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |

## `servers.*.locations.*.index` {#servers-x-locations-x-index}

Index lists filenames tried, in order, when a request resolves to a directory.

| | |
| --- | --- |
| Type | list of `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `static_files` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |

## `servers.*.locations.*.match.headers.*.name` {#servers-x-locations-x-match-headers-x-name}

Name is the field name, canonicalized with textproto.CanonicalMIMEHeaderKey when the router compiles it, so lookup is case-insensitive as HTTP requires.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `routing` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |
| Constraint | RFC 9110 field-name token, canonicalized for lookup; Host and :-prefixed pseudo-headers rejected; Forwarded/X-Forwarded-*/RFC 9440 names require client_address.trusted_proxies on the listener |
| Zero/empty semantics | required; there is no default |
| Active when | a match.headers entry is present |

## `servers.*.locations.*.match.headers.*.op` {#servers-x-locations-x-match-headers-x-op}

Op is "present", "exact" or "regex".

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `routing` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |
| Allowed values | `exact`, `present`, `regex` |
| Constraint | exact lowercase enum; value is required for exact and regex and forbidden for present |
| Zero/empty semantics | required; there is no default |
| Active when | a match.headers entry is present |

## `servers.*.locations.*.match.headers.*.value` {#servers-x-locations-x-match-headers-x-value}

Value is required for "exact" and "regex" and forbidden for "present".

| | |
| --- | --- |
| Type | `string` |
| Optional | yes |
| Lifecycle | `hot_reload` |
| Subsystem | `routing` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |
| Constraint | at most 1 KiB; an unanchored RE2 pattern of at most 512 bytes when op = regex, with at most 8 regex predicates per location |
| Zero/empty semantics | omitted and explicitly empty are different: omitted is only legal for op = present, and an empty value matches only a present-but-empty field |
| Active when | op is exact or regex |

## `servers.*.locations.*.match.methods` {#servers-x-locations-x-match-methods}

Methods is the OR-set of request methods the location accepts, compared byte-exactly against r.Method.

| | |
| --- | --- |
| Type | list of `string` |
| Optional | yes |
| Lifecycle | `hot_reload` |
| Subsystem | `routing` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |
| Constraint | at most 16 distinct RFC 9110 method tokens, compared byte-exactly; a lowercase spelling of an IANA-registered method and CONNECT are rejected; listing GET also matches HEAD |
| Zero/empty semantics | omitted leaves the method unconstrained; an explicitly empty list is an error, because it is a route that can never match |
| Active when | location configured |

## `servers.*.locations.*.match.path` {#servers-x-locations-x-match-path}

Path is the request path pattern compared according to match.type.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `routing` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |

## `servers.*.locations.*.match.query.*.name` {#servers-x-locations-x-match-query-x-name}

Name is the parameter name, compared after percent-decoding.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `routing` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |
| Constraint | non-empty, at most 1 KiB, compared after percent-decoding |
| Zero/empty semantics | required; there is no default |
| Active when | a match.query entry is present |

## `servers.*.locations.*.match.query.*.op` {#servers-x-locations-x-match-query-x-op}

Op is "present" or "exact".

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `routing` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |
| Allowed values | `exact`, `present` |
| Constraint | exact lowercase enum; value is required for exact and forbidden for present. There is no regex operator for query parameters |
| Zero/empty semantics | required; there is no default |
| Active when | a match.query entry is present |

## `servers.*.locations.*.match.query.*.value` {#servers-x-locations-x-match-query-x-value}

Value is required for "exact" and forbidden for "present".

| | |
| --- | --- |
| Type | `string` |
| Optional | yes |
| Lifecycle | `hot_reload` |
| Subsystem | `routing` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |
| Constraint | at most 1 KiB, compared after percent-decoding |
| Zero/empty semantics | omitted and explicitly empty are different: omitted is only legal for op = present, and an empty value matches ?x and ?x= |
| Active when | op is exact |

## `servers.*.locations.*.match.type` {#servers-x-locations-x-match-type}

Type is one of "exact", "prefix", or "regex".

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `routing` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |
| Allowed values | `exact`, `prefix`, `regex` |
| Constraint | exact lowercase enum |
| Zero/empty semantics | omitted selects the documented default where supported |
| Active when | location configured |

## `servers.*.locations.*.plugin` {#servers-x-locations-x-plugin}

Plugin names a handler plugin that serves this location as its action (mutually exclusive with root/proxy_pass/etc.).

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `plugins` |
| Why | the plugin set is rebuilt and re-instantiated on each successful reload |
| Requires | `wasm_plugins` |

## `servers.*.locations.*.plugins` {#servers-x-locations-x-plugins}

Plugins lists middleware plugin names applied to this location, composed around the action (after any server-level plugins, outermost first).

| | |
| --- | --- |
| Type | list of `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `plugins` |
| Why | the plugin set is rebuilt and re-instantiated on each successful reload |
| Requires | `wasm_plugins` |

## `servers.*.locations.*.proxy_connect_timeout` {#servers-x-locations-x-proxy_connect_timeout}

ProxyConnectTimeout bounds dialing the backend for this location.

| | |
| --- | --- |
| Type | `duration` |
| Lifecycle | `hot_reload` |
| Subsystem | `proxy_timeouts` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |
| Constraint | non-negative |
| Zero/empty semantics | zero uses the runtime/default transport behavior |
| Active when | always |

## `servers.*.locations.*.proxy_pass` {#servers-x-locations-x-proxy_pass}

Reverse proxy.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `proxy_pass` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |

## `servers.*.locations.*.proxy_read_timeout` {#servers-x-locations-x-proxy_read_timeout}

ProxyReadTimeout bounds reading the backend's response for this location.

| | |
| --- | --- |
| Type | `duration` |
| Lifecycle | `hot_reload` |
| Subsystem | `proxy_timeouts` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |
| Constraint | non-negative |
| Zero/empty semantics | zero uses the runtime/default transport behavior |
| Active when | always |

## `servers.*.locations.*.proxy_retries` {#servers-x-locations-x-proxy_retries}

ProxyRetries caps the number of retry attempts for idempotent requests against other backends on connection failure.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `hot_reload` |
| Subsystem | `proxy_retries` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |
| Constraint | non-negative |
| Zero/empty semantics | 0 disables retry |
| Active when | always |

## `servers.*.locations.*.proxy_send_timeout` {#servers-x-locations-x-proxy_send_timeout}

ProxySendTimeout bounds writing the request to the backend for this location.

| | |
| --- | --- |
| Type | `duration` |
| Lifecycle | `hot_reload` |
| Subsystem | `proxy_timeouts` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |
| Constraint | non-negative |
| Zero/empty semantics | zero uses the runtime/default transport behavior |
| Active when | always |

## `servers.*.locations.*.rate_limit.burst` {#servers-x-locations-x-rate_limit-burst}

Burst is the maximum momentary burst above Rate.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `hot_reload` |
| Subsystem | `rate_limit` |
| Why | the rate-limiter store accepts a new policy on each successful reload |
| Constraint | at least rate |
| Zero/empty semantics | omitted/zero defaults to rate |
| Active when | rate limit enabled |

## `servers.*.locations.*.rate_limit.enabled` {#servers-x-locations-x-rate_limit-enabled}

Enabled turns on this location's rate-limit override.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `hot_reload` |
| Subsystem | `rate_limit` |
| Why | the rate-limiter store accepts a new policy on each successful reload |

## `servers.*.locations.*.rate_limit.key` {#servers-x-locations-x-rate_limit-key}

Key selects the bucket identity: "ip" (client address, the default), "header:<Name>" (a request header value), or "jwt:<claim>" (a validated JWT claim once auth is configured).

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `rate_limit` |
| Why | the rate-limiter store accepts a new policy on each successful reload |
| Constraint | ip, header:<Name>, or jwt:<claim> |
| Zero/empty semantics | omitted defaults to ip |
| Active when | rate limit enabled |

## `servers.*.locations.*.rate_limit.max_conns` {#servers-x-locations-x-rate_limit-max_conns}

MaxConns caps concurrent connections per listener (0 = unlimited).

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `validation_rejected_reserved` |
| Subsystem | `rate_limit` |
| Why | connection caps are listener-global; Validate rejects max_conns on a location override, so no running process can have consumed it |
| Flags | reserved |

## `servers.*.locations.*.rate_limit.rate` {#servers-x-locations-x-rate_limit-rate}

Rate is the sustained requests/second allowed per key.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `hot_reload` |
| Subsystem | `rate_limit` |
| Why | the rate-limiter store accepts a new policy on each successful reload |
| Constraint | positive |
| Zero/empty semantics | no valid zero while enabled |
| Active when | rate limit enabled |

## `servers.*.locations.*.redirect` {#servers-x-locations-x-redirect}

Redirect / return.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `redirect` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |

## `servers.*.locations.*.require_client_cert` {#servers-x-locations-x-require_client_cert}

RequireClientCert rejects a request that arrives without a verified mTLS client certificate with 403, even when the server's tls.client_auth mode is "request" (which admits the connection at the handshake).

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `hot_reload` |
| Subsystem | `mtls` |
| Why | the per-request certificate requirement is enforced by the rebuilt handler tree; the handshake policy itself is listener-bound |

## `servers.*.locations.*.resilience.max_connections_per_backend` {#servers-x-locations-x-resilience-max_connections_per_backend}

MaxConnectionsPerBackend overrides the pool's socket bound for this route's transport.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `hot_reload` |
| Subsystem | `resilience` |
| Why | the bound is a property of the outbound transport, which is already rebuilt with the handler generation that owns it, so a changed value takes effect on the next successful reload; connections established under the previous bound follow that generation's drain boundary |
| Constraint | 0 to 100000 |
| Zero/empty semantics | omitted/zero inherits the pool value |
| Active when | HTTP proxy routes; not applicable to native gRPC or transcoding |

## `servers.*.locations.*.resilience.retry_attempts` {#servers-x-locations-x-resilience-retry_attempts}

RetryAttempts overrides the pool's attempt cap for this route.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `hot_reload` |
| Subsystem | `resilience` |
| Why | the retry settings are read from the live policy at the start of each request, so a changed value governs the next request; a sequence already in flight keeps the values it started under, because changing an attempt budget underneath a running retry would make the deadline arithmetic incoherent |
| Constraint | 0 to 100; must not be set together with the deprecated proxy_retries |
| Zero/empty semantics | omitted/zero inherits the pool value |
| Active when | always |

## `servers.*.locations.*.resilience.retry_backoff_initial` {#servers-x-locations-x-resilience-retry_backoff_initial}

RetryBackoffInitial overrides the pool's first backoff interval.

| | |
| --- | --- |
| Type | `duration` |
| Lifecycle | `hot_reload` |
| Subsystem | `resilience` |
| Why | the retry settings are read from the live policy at the start of each request, so a changed value governs the next request; a sequence already in flight keeps the values it started under, because changing an attempt budget underneath a running retry would make the deadline arithmetic incoherent |
| Constraint | 0s to 60s; must not exceed retry_backoff_max or retry_deadline |
| Zero/empty semantics | omitted/zero inherits the pool value |
| Active when | always |

## `servers.*.locations.*.resilience.retry_backoff_max` {#servers-x-locations-x-resilience-retry_backoff_max}

RetryBackoffMax overrides the pool's clamp on backoff growth.

| | |
| --- | --- |
| Type | `duration` |
| Lifecycle | `hot_reload` |
| Subsystem | `resilience` |
| Why | the retry settings are read from the live policy at the start of each request, so a changed value governs the next request; a sequence already in flight keeps the values it started under, because changing an attempt budget underneath a running retry would make the deadline arithmetic incoherent |
| Constraint | 0s to 60s; requires retry_backoff_initial |
| Zero/empty semantics | omitted/zero inherits the pool value |
| Active when | retry_backoff_initial > 0 |

## `servers.*.locations.*.resilience.retry_deadline` {#servers-x-locations-x-resilience-retry_deadline}

RetryDeadline overrides the pool's bound on the whole retry sequence.

| | |
| --- | --- |
| Type | `duration` |
| Lifecycle | `hot_reload` |
| Subsystem | `resilience` |
| Why | the retry settings are read from the live policy at the start of each request, so a changed value governs the next request; a sequence already in flight keeps the values it started under, because changing an attempt budget underneath a running retry would make the deadline arithmetic incoherent |
| Constraint | 0s to 5m |
| Zero/empty semantics | omitted/zero inherits the pool value |
| Active when | always |

## `servers.*.locations.*.response_headers.*.name` {#servers-x-locations-x-response_headers-x-name}

Name is the response header field name the operation applies to.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `headers` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |
| Constraint | RFC 9110 field-name token; hop-by-hop/framing names, Content-Encoding, and Vary/Access-Control-* under their §8a/§8b conditions are rejected |
| Zero/empty semantics | required; there is no default |
| Active when | a response_headers entry is present |

## `servers.*.locations.*.response_headers.*.op` {#servers-x-locations-x-response_headers-x-op}

Op is "add" (Header.Add), "set" (Header.Set) or "remove" (Header.Del).

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `headers` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |
| Allowed values | `add`, `remove`, `set` |
| Constraint | exact lowercase enum; value is required for add/set and forbidden for remove |
| Zero/empty semantics | required; there is no default |
| Active when | a response_headers entry is present |

## `servers.*.locations.*.response_headers.*.value` {#servers-x-locations-x-response_headers-x-value}

Value is required for "add"/"set" and forbidden for "remove".

| | |
| --- | --- |
| Type | `string` |
| Optional | yes |
| Lifecycle | `hot_reload` |
| Subsystem | `headers` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |
| Constraint | at most 4 KiB; every byte must be RFC 9110 §5.5 VCHAR / SP / HTAB / obs-text |
| Zero/empty semantics | omitted and explicitly empty are different: omitted is only legal for op = remove, and an empty value emits an empty field value on add/set |
| Active when | op is add or set |

## `servers.*.locations.*.return` {#servers-x-locations-x-return}

Return is the bare HTTP status code this location responds with.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `hot_reload` |
| Subsystem | `return` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |
| Constraint | 100..599 when set |
| Zero/empty semantics | 0 means no return action |
| Active when | always |

## `servers.*.locations.*.rewrites.*.flag` {#servers-x-locations-x-rewrites-x-flag}

Flag is one of "", "last", "break", "redirect", "permanent".

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `rewrites` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |
| Allowed values | `last`, `break`, `redirect`, `permanent` |
| Constraint | exact lowercase enum |
| Zero/empty semantics | omitted selects the documented default where supported |
| Active when | non-empty |

## `servers.*.locations.*.rewrites.*.pattern` {#servers-x-locations-x-rewrites-x-pattern}

Pattern is the regular expression matched against the request path.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `rewrites` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |

## `servers.*.locations.*.rewrites.*.replacement` {#servers-x-locations-x-rewrites-x-replacement}

Replacement is the substitution text applied to a matched path.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `rewrites` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |

## `servers.*.locations.*.root` {#servers-x-locations-x-root}

Static file serving.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `root` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |

## `servers.*.locations.*.route_id` {#servers-x-locations-x-route_id}

RouteID is an optional, durable identifier for this location (ADR 0019 §4).

| | |
| --- | --- |
| Type | `string` |
| Optional | yes |
| Lifecycle | `hot_reload` |
| Subsystem | `routing` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |
| Constraint | 1 to 64 bytes; lowercase ASCII [a-z0-9_-]; first byte alphanumeric; present-and-empty invalid; globally unique across the configuration |
| Zero/empty semantics | omitted means no durable identity; the revision-scoped selector (listen, server_names, match_type, path, match_ordinal) plus base_version remains fully functional |
| Active when | always |

## `servers.*.locations.*.try_files` {#servers-x-locations-x-try_files}

TryFiles lists candidate paths tried in order before falling back to the location's action.

| | |
| --- | --- |
| Type | list of `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `try_files` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |

## `servers.*.locations.*.uwsgi_pass` {#servers-x-locations-x-uwsgi_pass}

UWSGIPass is the uWSGI backend address this location dispatches to.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `uwsgi` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |

## `servers.*.locations.*.waf.block_status` {#servers-x-locations-x-waf-block_status}

BlockStatus is the HTTP status returned when a request is blocked in "block" mode.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `hot_reload` |
| Subsystem | `waf` |
| Why | the WAF policy is rebuilt on each successful reload |
| Requires | `waf` |
| Constraint | 100..599 effective |
| Zero/empty semantics | omitted/zero defaults to 403 |
| Active when | WAF enabled |

## `servers.*.locations.*.waf.crs_enabled` {#servers-x-locations-x-waf-crs_enabled}

CRSEnabled loads the embedded OWASP Core Rule Set with zero external setup (the rules ship inside the binary in builds with the "waf" tag).

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `hot_reload` |
| Subsystem | `waf` |
| Why | the WAF policy is rebuilt on each successful reload |
| Requires | `waf` |

## `servers.*.locations.*.waf.directives_files` {#servers-x-locations-x-waf-directives_files}

DirectivesFiles lists SecLang rule files to load, in order, after the CRS (when crs_enabled) and before InlineRules.

| | |
| --- | --- |
| Type | list of `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `waf` |
| Why | the WAF policy is rebuilt on each successful reload |
| Requires | `waf` |

## `servers.*.locations.*.waf.enabled` {#servers-x-locations-x-waf-enabled}

Enabled turns the firewall on for the scope it appears in.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `hot_reload` |
| Subsystem | `waf` |
| Why | the WAF policy is rebuilt on each successful reload |
| Requires | `waf` |

## `servers.*.locations.*.waf.inline_rules` {#servers-x-locations-x-waf-inline_rules}

InlineRules is a SecLang snippet appended last (after files and the CRS).

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `waf` |
| Why | the WAF policy is rebuilt on each successful reload |
| Requires | `waf` |

## `servers.*.locations.*.waf.mode` {#servers-x-locations-x-waf-mode}

Mode is "block" (default) — a rule interruption returns BlockStatus — or "detect", which records and logs the event but lets the request proceed.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `waf` |
| Why | the WAF policy is rebuilt on each successful reload |
| Requires | `waf` |
| Default | block |
| Allowed values | `block`, `detect` |
| Constraint | exact lowercase enum |
| Zero/empty semantics | omitted selects the documented default where supported |
| Active when | WAF enabled |

## `servers.*.locations.*.waf.paranoia` {#servers-x-locations-x-waf-paranoia}

Paranoia sets the CRS blocking paranoia level (1–4) when CRSEnabled is set.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `hot_reload` |
| Subsystem | `waf` |
| Why | the WAF policy is rebuilt on each successful reload |
| Requires | `waf` |
| Constraint | 0 or 1..4; non-zero requires CRS |
| Zero/empty semantics | 0 selects the CRS default |
| Active when | WAF enabled |

## `servers.*.locations.*.waf.request_body_limit` {#servers-x-locations-x-waf-request_body_limit}

RequestBodyLimit caps how many request-body bytes are buffered for inspection.

| | |
| --- | --- |
| Type | `size` |
| Lifecycle | `hot_reload` |
| Subsystem | `waf` |
| Why | the WAF policy is rebuilt on each successful reload |
| Requires | `waf` |
| Default | 128k |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero defaults to 128 KiB |
| Active when | WAF enabled |

## `servers.*.locations.*.waf.response_body_check` {#servers-x-locations-x-waf-response_body_check}

ResponseBodyCheck enables inspection of response bodies (CRS phase 4).

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `hot_reload` |
| Subsystem | `waf` |
| Why | the WAF policy is rebuilt on each successful reload |
| Requires | `waf` |

## `servers.*.max_header_bytes` {#servers-x-max_header_bytes}

MaxHeaderBytes caps the size of request headers (default 1 MiB).

| | |
| --- | --- |
| Type | `size` |
| Lifecycle | `new_listener_only` |
| Subsystem | `listener_limits` |
| Why | the value is read once when the socket binds; an address kept across the reload keeps the value it bound with |
| Default | 1m |
| Flags | conditional |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero defaults to 1 MiB |
| Active when | always |

## `servers.*.name` {#servers-x-name}

Name is a descriptive label for this server block, used only in projections.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `server_identity` |
| Why | the block label appears only in configuration projections, which are rebuilt from the effective config on each reload |

## `servers.*.plugins` {#servers-x-plugins}

Plugins lists middleware plugin names applied to every location in this server, outermost first.

| | |
| --- | --- |
| Type | list of `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `plugins` |
| Why | the plugin set is rebuilt and re-instantiated on each successful reload |
| Requires | `wasm_plugins` |

## `servers.*.proxy_protocol` {#servers-x-proxy_protocol}

ProxyProtocol enables ingesting a HAProxy PROXY-protocol header from a TCP load balancer: "" (off) or "in".

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `restart_required` |
| Subsystem | `client_address` |
| Why | the PROXY-protocol wrapper is installed when the address binds, ahead of the TLS wrap, so it is fixed for the listener's lifetime |
| Flags | startup-consumed, per-address, conditional |

## `servers.*.read_header_timeout` {#servers-x-read_header_timeout}

ReadHeaderTimeout bounds reading request headers for this server block.

| | |
| --- | --- |
| Type | `duration` |
| Lifecycle | `new_listener_only` |
| Subsystem | `listener_timeouts` |
| Why | the value is read once when the socket binds; an address kept across the reload keeps the value it bound with |
| Flags | conditional |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero defaults to 10s |
| Active when | always |

## `servers.*.read_timeout` {#servers-x-read_timeout}

ReadTimeout bounds reading the full request for this server block.

| | |
| --- | --- |
| Type | `duration` |
| Lifecycle | `new_listener_only` |
| Subsystem | `listener_timeouts` |
| Why | the value is read once when the socket binds; an address kept across the reload keeps the value it bound with |
| Flags | conditional |
| Constraint | non-negative |
| Zero/empty semantics | zero disables the timeout |
| Active when | always |

## `servers.*.redirect_https` {#servers-x-redirect_https}

RedirectHTTPS, when set on an HTTP server block, issues a redirect to the equivalent HTTPS URL.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `hot_reload` |
| Subsystem | `server_redirect` |
| Why | the handler tree is rebuilt from the effective config on each successful reload |
| Allowed values | `0`, `301`, `308` |
| Constraint | 0, 301, or 308 |
| Zero/empty semantics | 0 disables redirect |
| Active when | always |

## `servers.*.server_names` {#servers-x-server_names}

ServerNames lists the virtual-host names this server block matches, used for routing and TLS certificate selection.

| | |
| --- | --- |
| Type | list of `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `server_names` |
| Why | virtual-host routing uses the rebuilt handler tree; when the block terminates TLS the name set is also part of the listener's certificate identity and is compared by the bind-time gate |

## `servers.*.tls.acme.ca` {#servers-x-tls-acme-ca}

CA selects the directory: "letsencrypt", "letsencrypt-staging", or a full ACME directory URL.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `restart_required` |
| Subsystem | `acme` |
| Why | the ACME manager, its account and its certificate cache are created for the listener at bind time |
| Requires | `acme` |
| Default | letsencrypt-staging |
| Flags | startup-consumed, per-address, conditional |
| Allowed values | `letsencrypt`, `letsencrypt-staging` |
| Constraint | letsencrypt, letsencrypt-staging, or an https directory URL |
| Zero/empty semantics | omitted defaults to letsencrypt-staging |
| Active when | ACME enabled |

## `servers.*.tls.acme.cache_dir` {#servers-x-tls-acme-cache_dir}

CacheDir is where issued certificates and account keys are stored.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `restart_required` |
| Subsystem | `acme` |
| Why | the ACME manager, its account and its certificate cache are created for the listener at bind time |
| Requires | `acme` |
| Default | ./jul-data/certs |
| Flags | startup-consumed, per-address, conditional |

## `servers.*.tls.acme.challenge` {#servers-x-tls-acme-challenge}

Challenge selects the ACME challenge type: "http-01" (default) or "tls-alpn-01".

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `restart_required` |
| Subsystem | `acme` |
| Why | the ACME manager, its account and its certificate cache are created for the listener at bind time |
| Requires | `acme` |
| Default | http-01 |
| Flags | startup-consumed, per-address, conditional |
| Allowed values | `http-01`, `tls-alpn-01` |
| Constraint | exact lowercase enum |
| Zero/empty semantics | omitted selects the documented default where supported |
| Active when | ACME enabled |

## `servers.*.tls.acme.dns_provider` {#servers-x-tls-acme-dns_provider}

DNSProvider names the DNS-01 provider plugin (e.g. "cloudflare").

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `validation_rejected_reserved` |
| Subsystem | `acme` |
| Why | DNS-01 is not implemented; Validate rejects a non-empty dns_provider, so no running process can have consumed it |
| Requires | `acme` |
| Flags | reserved |

## `servers.*.tls.acme.domains` {#servers-x-tls-acme-domains}

Domains is the allow-list of hostnames to obtain certificates for.

| | |
| --- | --- |
| Type | list of `string` |
| Lifecycle | `restart_required` |
| Subsystem | `acme` |
| Why | the ACME manager, its account and its certificate cache are created for the listener at bind time |
| Requires | `acme` |
| Flags | startup-consumed, per-address, conditional |

## `servers.*.tls.acme.email` {#servers-x-tls-acme-email}

Email is the ACME account contact address (required when enabled).

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `restart_required` |
| Subsystem | `acme` |
| Why | the ACME manager, its account and its certificate cache are created for the listener at bind time |
| Requires | `acme` |
| Flags | startup-consumed, per-address, conditional |

## `servers.*.tls.acme.enabled` {#servers-x-tls-acme-enabled}

Enabled turns on automatic certificate management (ACME) for this listener.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `restart_required` |
| Subsystem | `acme` |
| Why | the ACME manager, its account and its certificate cache are created for the listener at bind time |
| Requires | `acme` |
| Flags | startup-consumed, per-address, conditional |

## `servers.*.tls.acme.ocsp_stapling` {#servers-x-tls-acme-ocsp_stapling}

OCSPStapling enables OCSP stapling for ACME-issued certificates so clients can verify revocation without a separate round-trip.

| | |
| --- | --- |
| Type | `bool` |
| Optional | yes |
| Lifecycle | `restart_required` |
| Subsystem | `acme` |
| Why | the ACME manager, its account and its certificate cache are created for the listener at bind time |
| Requires | `acme` |
| Default | true |
| Flags | startup-consumed, per-address, conditional |

## `servers.*.tls.cert` {#servers-x-tls-cert}

Cert is the path to the PEM certificate file for static (non-ACME) TLS.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `tls` |
| Why | a candidate certificate provider is built and validated during Prepare and swapped atomically into the listener's existing dynamic provider at Publish, without rebinding (#100) |
| Flags | secret |

## `servers.*.tls.client_auth.ca_file` {#servers-x-tls-client_auth-ca_file}

CAFile is the PEM bundle of certificate authorities that client certificates are verified against.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `restart_required` |
| Subsystem | `mtls` |
| Why | the client CA pool is read and installed when the listener binds; the fingerprint digests the file contents so an in-place rotation is detected |
| Flags | startup-consumed, per-address, conditional, secret |

## `servers.*.tls.client_auth.crl_file` {#servers-x-tls-client_auth-crl_file}

CRLFile, when set, is a PEM- or DER-encoded certificate revocation list.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `restart_required` |
| Subsystem | `mtls` |
| Why | the revocation list is read and installed when the listener binds; the fingerprint digests the file contents so an in-place rotation is detected |
| Flags | startup-consumed, per-address, conditional, secret |

## `servers.*.tls.client_auth.forward_certificate` {#servers-x-tls-client_auth-forward_certificate}

ForwardCertificate conveys the verified client certificate to backends with the RFC 9440 Client-Cert header: "none" (default), "leaf", or "chain" (which adds Client-Cert-Chain).

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `mtls` |
| Why | the client-certificate forwarding mode is read when the handler tree is rebuilt |
| Default | none |

## `servers.*.tls.client_auth.mode` {#servers-x-tls-client_auth-mode}

Mode selects enforcement at the TLS handshake: "none" — off (the default).

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `restart_required` |
| Subsystem | `mtls` |
| Why | the client-certificate policy is written into the listener's tls.Config at bind time |
| Default | none |
| Flags | startup-consumed, per-address, conditional |
| Allowed values | `none`, `request`, `require` |
| Constraint | exact lowercase enum |
| Zero/empty semantics | omitted selects the documented default where supported |
| Active when | client_auth configured |

## `servers.*.tls.client_auth.verify_san` {#servers-x-tls-client_auth-verify_san}

VerifySAN, when non-empty, is an allow-list of subject alternative names (DNS name, URI, email, or IP).

| | |
| --- | --- |
| Type | list of `string` |
| Lifecycle | `restart_required` |
| Subsystem | `mtls` |
| Why | the SAN allow-list is captured by the listener's verify callback at bind time |
| Flags | startup-consumed, per-address, conditional |

## `servers.*.tls.enabled` {#servers-x-tls-enabled}

Enabled turns on TLS termination for this server block.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `restart_required` |
| Subsystem | `tls` |
| Why | whether the listener terminates TLS is decided when the address binds |
| Flags | startup-consumed, per-address, conditional |

## `servers.*.tls.key` {#servers-x-tls-key}

Key is the path to the PEM private key file paired with cert.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `tls` |
| Why | a candidate certificate provider is built and validated during Prepare and swapped atomically into the listener's existing dynamic provider at Publish, without rebinding (#100) |
| Flags | secret |

## `servers.*.tls.min_version` {#servers-x-tls-min_version}

MinVersion is one of "1.2" or "1.3".

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `restart_required` |
| Subsystem | `tls` |
| Why | the minimum protocol version is written into the listener's tls.Config at bind time |
| Flags | startup-consumed, per-address, conditional |
| Allowed values | `1.2`, `1.3` |
| Constraint | exact lowercase enum |
| Zero/empty semantics | omitted selects the documented default where supported |
| Active when | TLS enabled |

## `servers.*.write_timeout` {#servers-x-write_timeout}

WriteTimeout bounds writing the response for this server block.

| | |
| --- | --- |
| Type | `duration` |
| Lifecycle | `new_listener_only` |
| Subsystem | `listener_timeouts` |
| Why | the value is read once when the socket binds; an address kept across the reload keeps the value it bound with |
| Flags | conditional |
| Constraint | non-negative |
| Zero/empty semantics | zero disables the timeout |
| Active when | always |

## `stream.*.connect_timeout` {#stream-x-connect_timeout}

ConnectTimeout bounds dialing the backend.

| | |
| --- | --- |
| Type | `duration` |
| Lifecycle | `hot_reload` |
| Subsystem | `stream` |
| Why | the stream listener swaps its route pointer atomically on each successful reload |
| Requires | `stream_proxy` |
| Default | 10s |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero defaults to 10s |
| Active when | always |

## `stream.*.idle_timeout` {#stream-x-idle_timeout}

IdleTimeout closes a relayed connection/UDP session after this period with no traffic in either direction.

| | |
| --- | --- |
| Type | `duration` |
| Lifecycle | `hot_reload` |
| Subsystem | `stream` |
| Why | the stream listener swaps its route pointer atomically on each successful reload |
| Requires | `stream_proxy` |
| Default | 5m |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero defaults to 5m |
| Active when | always |

## `stream.*.listen` {#stream-x-listen}

Listen is the bind address (host:port), e.g. "0.0.0.0:5432".

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `new_listener_only` |
| Subsystem | `stream` |
| Why | an L4 listener is keyed by protocol and address; moving to a different address binds a new socket and retires the old one |
| Requires | `stream_proxy` |
| Flags | conditional |

## `stream.*.max_udp_sessions` {#stream-x-max_udp_sessions}

MaxUDPSessions caps the number of concurrent UDP sessions (one per client source address) a UDP listener tracks, bounding memory and backend sockets on the public internet where source addresses are cheap to spoof.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `hot_reload` |
| Subsystem | `stream` |
| Why | the stream listener swaps its route pointer atomically on each successful reload |
| Requires | `stream_proxy` |
| Default | 10000 |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero defaults to 10000 |
| Active when | always |

## `stream.*.protocol` {#stream-x-protocol}

Protocol is "tcp" (default) or "udp".

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `stream` |
| Why | the stream reload binds the candidate protocol's listener before retiring the previous one; established connections and UDP sessions follow the retired listener's drain boundary while new traffic uses the candidate protocol |
| Requires | `stream_proxy` |
| Default | tcp |
| Allowed values | `tcp`, `udp` |
| Constraint | exact lowercase enum |
| Zero/empty semantics | omitted selects the documented default where supported |
| Active when | stream configured |

## `stream.*.proxy_pass` {#stream-x-proxy_pass}

ProxyPass is the default backend: a named upstream or a literal host:port.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `stream` |
| Why | the stream listener swaps its route pointer atomically on each successful reload |
| Requires | `stream_proxy` |

## `stream.*.proxy_protocol` {#stream-x-proxy_protocol}

ProxyProtocol controls HAProxy PROXY-protocol handling (TCP only): "" (off), "in" (parse a header from the client), "out" (emit a v2 header to the backend), or "both".

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `stream` |
| Why | the stream listener swaps its route pointer atomically on each successful reload |
| Requires | `stream_proxy` |
| Allowed values | `in`, `out`, `both` |
| Constraint | exact lowercase enum |
| Zero/empty semantics | omitted selects the documented default where supported |
| Active when | non-empty |

## `stream.*.sni_routes.*` {#stream-x-sni_routes-x}

SNIRoutes maps a TLS server name (SNI host) to a backend (named upstream or host:port).

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `stream` |
| Why | the stream listener swaps its route pointer atomically on each successful reload |
| Requires | `stream_proxy` |

## `stream.*.tls_passthrough` {#stream-x-tls_passthrough}

TLSPassthrough documents that the listener forwards TLS unmodified.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `hot_reload` |
| Subsystem | `stream` |
| Why | the stream listener swaps its route pointer atomically on each successful reload |
| Requires | `stream_proxy` |

## `stream.*.trusted_proxies` {#stream-x-trusted_proxies}

TrustedProxies lists the CIDR prefixes, or bare addresses meaning a single host, permitted to assert a client address with an inbound PROXY header.

| | |
| --- | --- |
| Type | list of `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `stream` |
| Why | the stream listener swaps its route pointer atomically on each successful reload |
| Requires | `stream_proxy` |

## `upstreams.*.backend_tls.ca_file` {#upstreams-x-backend_tls-ca_file}

CAFile is a PEM bundle of trust roots.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `backend_tls` |
| Why | the resolved policy is part of the pool's identity, so a changed policy — including a certificate rotated in place — rebuilds the pool and its probe client on the next successful reload |
| Flags | secret |

## `upstreams.*.backend_tls.ca_mode` {#upstreams-x-backend_tls-ca_mode}

CAMode is "system" (default), "system_and_file" or "file_only".

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `backend_tls` |
| Why | the resolved policy is part of the pool's identity, so a changed policy — including a certificate rotated in place — rebuilds the pool and its probe client on the next successful reload |
| Default | system |
| Allowed values | `system`, `system_and_file`, `file_only` |
| Constraint | exact lowercase enum; never inferred from the presence of ca_file |
| Zero/empty semantics | omitted means system roots only |
| Active when | the backend is reached over TLS |

## `upstreams.*.backend_tls.client_cert` {#upstreams-x-backend_tls-client_cert}

ClientCert and ClientKey are the client certificate presented to the backend (mutual TLS).

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `backend_tls` |
| Why | the resolved policy is part of the pool's identity, so a changed policy — including a certificate rotated in place — rebuilds the pool and its probe client on the next successful reload |
| Flags | secret |

## `upstreams.*.backend_tls.client_key` {#upstreams-x-backend_tls-client_key}

ClientKey is the private key paired with client_cert for backend mutual TLS; both are required together.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `backend_tls` |
| Why | the resolved policy is part of the pool's identity, so a changed policy — including a certificate rotated in place — rebuilds the pool and its probe client on the next successful reload |
| Flags | secret |

## `upstreams.*.backend_tls.insecure_skip_verify` {#upstreams-x-backend_tls-insecure_skip_verify}

InsecureSkipVerify disables backend certificate verification.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `hot_reload` |
| Subsystem | `backend_tls` |
| Why | the resolved policy is part of the pool's identity, so a changed policy — including a certificate rotated in place — rebuilds the pool and its probe client on the next successful reload |

## `upstreams.*.backend_tls.min_version` {#upstreams-x-backend_tls-min_version}

MinVersion is "1.2" (default) or "1.3".

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `backend_tls` |
| Why | the resolved policy is part of the pool's identity, so a changed policy — including a certificate rotated in place — rebuilds the pool and its probe client on the next successful reload |
| Default | 1.2 |
| Allowed values | `1.2`, `1.3` |
| Constraint | exact lowercase enum |
| Zero/empty semantics | omitted defaults to 1.2, matching Go's client default |
| Active when | the backend is reached over TLS |

## `upstreams.*.backend_tls.peer_identities` {#upstreams-x-backend_tls-peer_identities}

PeerIdentities are prefixed identities ("dns:name", "uri:spiffe://...") matched against the backend certificate after standard verification.

| | |
| --- | --- |
| Type | list of `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `backend_tls` |
| Why | the resolved policy is part of the pool's identity, so a changed policy — including a certificate rotated in place — rebuilds the pool and its probe client on the next successful reload |
| Constraint | prefixed identities: dns:<name> or uri:<uri>; ORed, matched after standard verification, never by regex or substring |
| Zero/empty semantics | omitted or empty applies standard chain and name verification only |
| Active when | the backend is reached over TLS and insecure_skip_verify is false |

## `upstreams.*.backend_tls.server_name` {#upstreams-x-backend_tls-server_name}

ServerName overrides the verified identity and the SNI value.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `backend_tls` |
| Why | the resolved policy is part of the pool's identity, so a changed policy — including a certificate rotated in place — rebuilds the pool and its probe client on the next successful reload |

## `upstreams.*.discovery.consul.address` {#upstreams-x-discovery-consul-address}

Address is the Consul HTTP API base URL (default "http://127.0.0.1:8500").

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `discovery` |
| Why | the per-pool discovery refresher is restarted with the pool on each successful reload |
| Requires | `consul` |
| Default | http://127.0.0.1:8500 |

## `upstreams.*.discovery.consul.datacenter` {#upstreams-x-discovery-consul-datacenter}

Datacenter, when set, queries a specific datacenter.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `discovery` |
| Why | the per-pool discovery refresher is restarted with the pool on each successful reload |
| Requires | `consul` |

## `upstreams.*.discovery.consul.passing_only` {#upstreams-x-discovery-consul-passing_only}

PassingOnly restricts results to instances whose health checks are passing (default true).

| | |
| --- | --- |
| Type | `bool` |
| Optional | yes |
| Lifecycle | `hot_reload` |
| Subsystem | `discovery` |
| Why | the per-pool discovery refresher is restarted with the pool on each successful reload |
| Requires | `consul` |
| Default | true |

## `upstreams.*.discovery.consul.service` {#upstreams-x-discovery-consul-service}

Service is the Consul service name to resolve (required).

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `discovery` |
| Why | the per-pool discovery refresher is restarted with the pool on each successful reload |
| Requires | `consul` |

## `upstreams.*.discovery.consul.tag` {#upstreams-x-discovery-consul-tag}

Tag, when set, restricts results to instances carrying this tag.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `discovery` |
| Why | the per-pool discovery refresher is restarted with the pool on each successful reload |
| Requires | `consul` |

## `upstreams.*.discovery.consul.tls.ca_file` {#upstreams-x-discovery-consul-tls-ca_file}

CAFile is a PEM bundle of trust roots.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `backend_tls` |
| Why | the resolved policy is part of the pool's identity, so a changed policy — including a certificate rotated in place — rebuilds the pool and its probe client on the next successful reload |
| Requires | `consul` |
| Flags | secret |

## `upstreams.*.discovery.consul.tls.ca_mode` {#upstreams-x-discovery-consul-tls-ca_mode}

CAMode is "system" (default), "system_and_file" or "file_only".

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `backend_tls` |
| Why | the resolved policy is part of the pool's identity, so a changed policy — including a certificate rotated in place — rebuilds the pool and its probe client on the next successful reload |
| Requires | `consul` |
| Default | system |

## `upstreams.*.discovery.consul.tls.client_cert` {#upstreams-x-discovery-consul-tls-client_cert}

ClientCert and ClientKey are the client certificate presented to the backend (mutual TLS).

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `backend_tls` |
| Why | the resolved policy is part of the pool's identity, so a changed policy — including a certificate rotated in place — rebuilds the pool and its probe client on the next successful reload |
| Requires | `consul` |
| Flags | secret |

## `upstreams.*.discovery.consul.tls.client_key` {#upstreams-x-discovery-consul-tls-client_key}

ClientKey is the private key paired with client_cert used to authenticate to the Consul agent; both are required together.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `backend_tls` |
| Why | the resolved policy is part of the pool's identity, so a changed policy — including a certificate rotated in place — rebuilds the pool and its probe client on the next successful reload |
| Requires | `consul` |
| Flags | secret |

## `upstreams.*.discovery.consul.tls.insecure_skip_verify` {#upstreams-x-discovery-consul-tls-insecure_skip_verify}

InsecureSkipVerify disables backend certificate verification.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `hot_reload` |
| Subsystem | `backend_tls` |
| Why | the resolved policy is part of the pool's identity, so a changed policy — including a certificate rotated in place — rebuilds the pool and its probe client on the next successful reload |
| Requires | `consul` |

## `upstreams.*.discovery.consul.tls.min_version` {#upstreams-x-discovery-consul-tls-min_version}

MinVersion is "1.2" (default) or "1.3".

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `backend_tls` |
| Why | the resolved policy is part of the pool's identity, so a changed policy — including a certificate rotated in place — rebuilds the pool and its probe client on the next successful reload |
| Requires | `consul` |
| Default | 1.2 |

## `upstreams.*.discovery.consul.tls.peer_identities` {#upstreams-x-discovery-consul-tls-peer_identities}

PeerIdentities are prefixed identities ("dns:name", "uri:spiffe://...") matched against the backend certificate after standard verification.

| | |
| --- | --- |
| Type | list of `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `backend_tls` |
| Why | the resolved policy is part of the pool's identity, so a changed policy — including a certificate rotated in place — rebuilds the pool and its probe client on the next successful reload |
| Requires | `consul` |

## `upstreams.*.discovery.consul.tls.server_name` {#upstreams-x-discovery-consul-tls-server_name}

ServerName overrides the verified identity and the SNI value.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `backend_tls` |
| Why | the resolved policy is part of the pool's identity, so a changed policy — including a certificate rotated in place — rebuilds the pool and its probe client on the next successful reload |
| Requires | `consul` |

## `upstreams.*.discovery.consul.token` {#upstreams-x-discovery-consul-token}

Token is an optional ACL token sent as X-Consul-Token.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `discovery` |
| Why | the per-pool discovery refresher is restarted with the pool on each successful reload |
| Requires | `consul` |
| Flags | secret |

## `upstreams.*.discovery.kubernetes.api_server` {#upstreams-x-discovery-kubernetes-api_server}

APIServer overrides the API server base URL (default from the in-cluster KUBERNETES_SERVICE_HOST/PORT environment).

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `discovery` |
| Why | the per-pool discovery refresher is restarted with the pool on each successful reload |
| Requires | `kubernetes` |

## `upstreams.*.discovery.kubernetes.ca_file` {#upstreams-x-discovery-kubernetes-ca_file}

CAFile overrides the API server CA bundle (default: the mounted service-account CA).

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `discovery` |
| Why | the per-pool discovery refresher is restarted with the pool on each successful reload |
| Requires | `kubernetes` |

## `upstreams.*.discovery.kubernetes.insecure_skip_tls_verify` {#upstreams-x-discovery-kubernetes-insecure_skip_tls_verify}

InsecureSkipTLSVerify disables API server certificate verification.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `hot_reload` |
| Subsystem | `discovery` |
| Why | the per-pool discovery refresher is restarted with the pool on each successful reload |
| Requires | `kubernetes` |

## `upstreams.*.discovery.kubernetes.namespace` {#upstreams-x-discovery-kubernetes-namespace}

Namespace of the target Service (required).

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `discovery` |
| Why | the per-pool discovery refresher is restarted with the pool on each successful reload |
| Requires | `kubernetes` |

## `upstreams.*.discovery.kubernetes.port` {#upstreams-x-discovery-kubernetes-port}

Port selects the endpoint port by name or number.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `discovery` |
| Why | the per-pool discovery refresher is restarted with the pool on each successful reload |
| Requires | `kubernetes` |

## `upstreams.*.discovery.kubernetes.service` {#upstreams-x-discovery-kubernetes-service}

Service is the Kubernetes Service name whose endpoints are resolved (required).

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `discovery` |
| Why | the per-pool discovery refresher is restarted with the pool on each successful reload |
| Requires | `kubernetes` |

## `upstreams.*.discovery.kubernetes.token` {#upstreams-x-discovery-kubernetes-token}

Token overrides the bearer token (default: the mounted service-account token).

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `discovery` |
| Why | the per-pool discovery refresher is restarted with the pool on each successful reload |
| Requires | `kubernetes` |
| Flags | secret |

## `upstreams.*.discovery.refresh` {#upstreams-x-discovery-refresh}

Refresh is the polling interval (default 30s).

| | |
| --- | --- |
| Type | `duration` |
| Lifecycle | `hot_reload` |
| Subsystem | `discovery` |
| Why | the per-pool discovery refresher is restarted with the pool on each successful reload |
| Default | 30s |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero defaults to 30s for dynamic providers |
| Active when | dynamic discovery |

## `upstreams.*.discovery.target` {#upstreams-x-discovery-target}

Target is the resolver query.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `discovery` |
| Why | the per-pool discovery refresher is restarted with the pool on each successful reload |

## `upstreams.*.discovery.type` {#upstreams-x-discovery-type}

Type selects the resolver: "dns" (A/AAAA records), "dns_srv" (SRV records), "consul", or "kubernetes".

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `discovery` |
| Why | the per-pool discovery refresher is restarted with the pool on each successful reload |
| Allowed values | `static`, `dns`, `dns_srv`, `consul`, `kubernetes` |
| Constraint | exact lowercase enum |
| Zero/empty semantics | omitted selects the documented default where supported |
| Active when | discovery block configured |

## `upstreams.*.fail_timeout` {#upstreams-x-fail_timeout}

FailTimeout is the deprecated spelling of [upstreams.resilience] fail_timeout: how long a backend stays out of rotation before being probed again.

| | |
| --- | --- |
| Type | `duration` |
| Lifecycle | `hot_reload` |
| Subsystem | `resilience` |
| Why | the bound is retuned on each backend in place while circuit state, the failure count and any half-open probe already in flight are preserved: rebuilding the breaker would forget which backends are currently out of rotation, and a reload during an incident would put every one of them back under full load at once |
| Default | 10s |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero defaults to 10s; deprecated in favour of upstreams[].resilience.fail_timeout, and setting both is an error |
| Active when | always |

## `upstreams.*.health_check.enabled` {#upstreams-x-health_check-enabled}

Enabled turns on active health checking for this upstream pool.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `hot_reload` |
| Subsystem | `health_check` |
| Why | active probes are restarted with the pool on each successful reload |

## `upstreams.*.health_check.expect_body` {#upstreams-x-health_check-expect_body}

ExpectBody, when set, requires the HTTP probe response body to contain this substring.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `health_check` |
| Why | active probes are restarted with the pool on each successful reload |

## `upstreams.*.health_check.expect_status` {#upstreams-x-health_check-expect_status}

ExpectStatus lists acceptable HTTP status codes for a passing probe (default [200]).

| | |
| --- | --- |
| Type | list of `integer` |
| Lifecycle | `hot_reload` |
| Subsystem | `health_check` |
| Why | active probes are restarted with the pool on each successful reload |
| Default | [200] |

## `upstreams.*.health_check.healthy_threshold` {#upstreams-x-health_check-healthy_threshold}

HealthyThreshold is the number of consecutive successes to mark a backend healthy again (default 2).

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `hot_reload` |
| Subsystem | `health_check` |
| Why | active probes are restarted with the pool on each successful reload |
| Default | 2 |
| Constraint | at least 1 effective |
| Zero/empty semantics | omitted/zero defaults to 2 |
| Active when | health check enabled |

## `upstreams.*.health_check.interval` {#upstreams-x-health_check-interval}

Interval is the delay between probe rounds (default 5s).

| | |
| --- | --- |
| Type | `duration` |
| Lifecycle | `hot_reload` |
| Subsystem | `health_check` |
| Why | active probes are restarted with the pool on each successful reload |
| Default | 5s |
| Constraint | positive effective value |
| Zero/empty semantics | omitted/zero defaults to 5s |
| Active when | health check enabled |

## `upstreams.*.health_check.path` {#upstreams-x-health_check-path}

Path is the request path for HTTP probes (required for type "http").

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `health_check` |
| Why | active probes are restarted with the pool on each successful reload |

## `upstreams.*.health_check.timeout` {#upstreams-x-health_check-timeout}

Timeout bounds a single probe (default 2s); must be less than Interval.

| | |
| --- | --- |
| Type | `duration` |
| Lifecycle | `hot_reload` |
| Subsystem | `health_check` |
| Why | active probes are restarted with the pool on each successful reload |
| Default | 2s |
| Constraint | positive and less than interval |
| Zero/empty semantics | omitted/zero defaults to 2s |
| Active when | health check enabled |

## `upstreams.*.health_check.type` {#upstreams-x-health_check-type}

Type is the probe protocol: "http" (default) or "tcp".

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `health_check` |
| Why | active probes are restarted with the pool on each successful reload |
| Default | http |
| Allowed values | `http`, `tcp` |
| Constraint | exact lowercase enum |
| Zero/empty semantics | omitted selects the documented default where supported |
| Active when | health check enabled |

## `upstreams.*.health_check.unhealthy_threshold` {#upstreams-x-health_check-unhealthy_threshold}

UnhealthyThreshold is the number of consecutive failures to take a backend out of rotation (default 3).

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `hot_reload` |
| Subsystem | `health_check` |
| Why | active probes are restarted with the pool on each successful reload |
| Default | 3 |
| Constraint | at least 1 effective |
| Zero/empty semantics | omitted/zero defaults to 3 |
| Active when | health check enabled |

## `upstreams.*.max_fails` {#upstreams-x-max_fails}

MaxFails and FailTimeout are the circuit breaker's failure threshold and open duration.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `hot_reload` |
| Subsystem | `resilience` |
| Why | the bound is retuned on each backend in place while circuit state, the failure count and any half-open probe already in flight are preserved: rebuilding the breaker would forget which backends are currently out of rotation, and a reload during an incident would put every one of them back under full load at once |
| Default | 3 |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero defaults to 3; deprecated in favour of upstreams[].resilience.max_fails, and setting both is an error |
| Active when | always |

## `upstreams.*.name` {#upstreams-x-name}

Name is the pool's identifier, referenced by proxy_pass and the admin API.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `upstream` |
| Why | the upstream registry stages and swaps pools on each successful reload |

## `upstreams.*.resilience.circuit_half_open_probes` {#upstreams-x-resilience-circuit_half_open_probes}

CircuitHalfOpenProbes bounds how many requests may test a recovering backend at once.

| | |
| --- | --- |
| Type | `integer` |
| Optional | yes |
| Lifecycle | `hot_reload` |
| Subsystem | `resilience` |
| Why | the bound is retuned on each backend in place while circuit state, the failure count and any half-open probe already in flight are preserved: rebuilding the breaker would forget which backends are currently out of rotation, and a reload during an incident would put every one of them back under full load at once |
| Default | 1 |
| Constraint | 0 or greater |
| Zero/empty semantics | omitted means 1; an explicit zero means unbounded probing |
| Active when | always |

## `upstreams.*.resilience.fail_timeout` {#upstreams-x-resilience-fail_timeout}

FailTimeout is how long a backend stays out of rotation before it is probed.

| | |
| --- | --- |
| Type | `duration` |
| Lifecycle | `hot_reload` |
| Subsystem | `resilience` |
| Why | the bound is retuned on each backend in place while circuit state, the failure count and any half-open probe already in flight are preserved: rebuilding the breaker would forget which backends are currently out of rotation, and a reload during an incident would put every one of them back under full load at once |
| Default | 10s |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero defaults to 10s |
| Active when | always |

## `upstreams.*.resilience.max_active_per_backend` {#upstreams-x-resilience-max_active_per_backend}

MaxActivePerBackend bounds admitted logical requests per backend.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `hot_reload` |
| Subsystem | `resilience` |
| Why | the resolved resilience policy is swapped into the live pool as an atomic pointer at commit, deliberately without rebuilding it: admission counters, parked requests and per-backend state all survive, a raised limit wakes waiters immediately, and a lowered one lets the excess drain instead of failing requests that are already in flight |
| Constraint | 0 to 10000000 |
| Zero/empty semantics | omitted/zero means unlimited |
| Active when | always |

## `upstreams.*.resilience.max_active_requests` {#upstreams-x-resilience-max_active_requests}

MaxActiveRequests bounds admitted logical requests, streams and connections for the pool.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `hot_reload` |
| Subsystem | `resilience` |
| Why | the resolved resilience policy is swapped into the live pool as an atomic pointer at commit, deliberately without rebuilding it: admission counters, parked requests and per-backend state all survive, a raised limit wakes waiters immediately, and a lowered one lets the excess drain instead of failing requests that are already in flight |
| Constraint | 0 to 10000000 |
| Zero/empty semantics | omitted/zero means unlimited |
| Active when | always |

## `upstreams.*.resilience.max_connections_per_backend` {#upstreams-x-resilience-max_connections_per_backend}

MaxConnectionsPerBackend bounds physical sockets to one backend host on one transport.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `hot_reload` |
| Subsystem | `resilience` |
| Why | the bound is a property of the outbound transport, which is already rebuilt with the handler generation that owns it, so a changed value takes effect on the next successful reload; connections established under the previous bound follow that generation's drain boundary |
| Constraint | 0 to 100000 |
| Zero/empty semantics | omitted/zero means unlimited |
| Active when | HTTP proxy routes; not applicable to native gRPC or transcoding |

## `upstreams.*.resilience.max_fails` {#upstreams-x-resilience-max_fails}

MaxFails is how many consecutive failures take a backend out of rotation.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `hot_reload` |
| Subsystem | `resilience` |
| Why | the bound is retuned on each backend in place while circuit state, the failure count and any half-open probe already in flight are preserved: rebuilding the breaker would forget which backends are currently out of rotation, and a reload during an incident would put every one of them back under full load at once |
| Default | 3 |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero defaults to 3 |
| Active when | always |

## `upstreams.*.resilience.max_pending_requests` {#upstreams-x-resilience-max_pending_requests}

MaxPendingRequests bounds the queue of requests waiting for a slot.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `hot_reload` |
| Subsystem | `resilience` |
| Why | the resolved resilience policy is swapped into the live pool as an atomic pointer at commit, deliberately without rebuilding it: admission counters, parked requests and per-backend state all survive, a raised limit wakes waiters immediately, and a lowered one lets the excess drain instead of failing requests that are already in flight |
| Constraint | 0 to 100000; requires max_active_requests > 0 |
| Zero/empty semantics | omitted/zero means no queue, never unlimited |
| Active when | always |

## `upstreams.*.resilience.pending_timeout` {#upstreams-x-resilience-pending_timeout}

PendingTimeout bounds how long a request may wait for a slot.

| | |
| --- | --- |
| Type | `duration` |
| Lifecycle | `hot_reload` |
| Subsystem | `resilience` |
| Why | the resolved resilience policy is swapped into the live pool as an atomic pointer at commit, deliberately without rebuilding it: admission counters, parked requests and per-backend state all survive, a raised limit wakes waiters immediately, and a lowered one lets the excess drain instead of failing requests that are already in flight |
| Constraint | 0s to 60s; must not exceed global.shutdown_timeout |
| Zero/empty semantics | omitted/zero leaves the request context as the only bound |
| Active when | max_pending_requests > 0 |

## `upstreams.*.resilience.retry_attempts` {#upstreams-x-resilience-retry_attempts}

RetryAttempts caps total attempts for one retryable request.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `hot_reload` |
| Subsystem | `resilience` |
| Why | the retry settings are read from the live policy at the start of each request, so a changed value governs the next request; a sequence already in flight keeps the values it started under, because changing an attempt budget underneath a running retry would make the deadline arithmetic incoherent |
| Constraint | 0 to 100 |
| Zero/empty semantics | omitted/zero means try every distinct backend once |
| Active when | always |

## `upstreams.*.resilience.retry_backoff_initial` {#upstreams-x-resilience-retry_backoff_initial}

RetryBackoffInitial is the first backoff interval, doubling per attempt with full jitter.

| | |
| --- | --- |
| Type | `duration` |
| Lifecycle | `hot_reload` |
| Subsystem | `resilience` |
| Why | the retry settings are read from the live policy at the start of each request, so a changed value governs the next request; a sequence already in flight keeps the values it started under, because changing an attempt budget underneath a running retry would make the deadline arithmetic incoherent |
| Constraint | 0s to 60s; must not exceed retry_backoff_max or retry_deadline |
| Zero/empty semantics | omitted/zero means immediate failover |
| Active when | always |

## `upstreams.*.resilience.retry_backoff_max` {#upstreams-x-resilience-retry_backoff_max}

RetryBackoffMax clamps the doubling.

| | |
| --- | --- |
| Type | `duration` |
| Lifecycle | `hot_reload` |
| Subsystem | `resilience` |
| Why | the retry settings are read from the live policy at the start of each request, so a changed value governs the next request; a sequence already in flight keeps the values it started under, because changing an attempt budget underneath a running retry would make the deadline arithmetic incoherent |
| Constraint | 0s to 60s; requires retry_backoff_initial |
| Zero/empty semantics | omitted/zero means 500ms when backoff is enabled |
| Active when | retry_backoff_initial > 0 |

## `upstreams.*.resilience.retry_budget_percent` {#upstreams-x-resilience-retry_budget_percent}

RetryBudgetPercent bounds retries as a percentage of primary attempts over a trailing window.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `hot_reload` |
| Subsystem | `resilience` |
| Why | the percentage is swapped into the live budget while its accumulated window is deliberately preserved: resetting the window on reload would hand out a fresh burst of retries, and a reload during an incident is the least appropriate moment to forgive the retry load that helped cause it |
| Constraint | 0 to 1000 |
| Zero/empty semantics | omitted/zero means unbudgeted |
| Active when | always |

## `upstreams.*.resilience.retry_deadline` {#upstreams-x-resilience-retry_deadline}

RetryDeadline bounds the whole retry sequence, attempts and backoff sleeps alike.

| | |
| --- | --- |
| Type | `duration` |
| Lifecycle | `hot_reload` |
| Subsystem | `resilience` |
| Why | the retry settings are read from the live policy at the start of each request, so a changed value governs the next request; a sequence already in flight keeps the values it started under, because changing an attempt budget underneath a running retry would make the deadline arithmetic incoherent |
| Constraint | 0s to 5m |
| Zero/empty semantics | omitted/zero means the request context is the only bound |
| Active when | always |

## `upstreams.*.servers.*.address` {#upstreams-x-servers-x-address}

Address is the backend's host:port (or unix socket path).

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `upstream` |
| Why | the upstream registry stages and swaps pools on each successful reload |

## `upstreams.*.servers.*.weight` {#upstreams-x-servers-x-weight}

Weight biases backend selection under weighted-round-robin; higher values receive proportionally more requests.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `hot_reload` |
| Subsystem | `upstream` |
| Why | the upstream registry stages and swaps pools on each successful reload |
| Constraint | positive when explicitly materialized; zero is treated as omitted for direct struct callers |
| Zero/empty semantics | omitted/zero defaults to 3 |
| Active when | always |

## `upstreams.*.strategy` {#upstreams-x-strategy}

Strategy is one of "round_robin", "weighted_round_robin", "least_conn".

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `upstream` |
| Why | the upstream registry stages and swaps pools on each successful reload |
| Default | round_robin |
| Allowed values | `round_robin`, `weighted_round_robin`, `least_conn` |
| Constraint | exact lowercase enum |
| Zero/empty semantics | omitted selects the documented default where supported |
| Active when | upstream configured |

## `waf.block_status` {#waf-block_status}

BlockStatus is the HTTP status returned when a request is blocked in "block" mode.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `hot_reload` |
| Subsystem | `waf` |
| Why | the WAF policy is rebuilt on each successful reload |
| Requires | `waf` |
| Constraint | 100..599 effective |
| Zero/empty semantics | omitted/zero defaults to 403 |
| Active when | WAF enabled |

## `waf.crs_enabled` {#waf-crs_enabled}

CRSEnabled loads the embedded OWASP Core Rule Set with zero external setup (the rules ship inside the binary in builds with the "waf" tag).

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `hot_reload` |
| Subsystem | `waf` |
| Why | the WAF policy is rebuilt on each successful reload |
| Requires | `waf` |

## `waf.directives_files` {#waf-directives_files}

DirectivesFiles lists SecLang rule files to load, in order, after the CRS (when crs_enabled) and before InlineRules.

| | |
| --- | --- |
| Type | list of `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `waf` |
| Why | the WAF policy is rebuilt on each successful reload |
| Requires | `waf` |

## `waf.enabled` {#waf-enabled}

Enabled turns the firewall on for the scope it appears in.

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `hot_reload` |
| Subsystem | `waf` |
| Why | the WAF policy is rebuilt on each successful reload |
| Requires | `waf` |

## `waf.inline_rules` {#waf-inline_rules}

InlineRules is a SecLang snippet appended last (after files and the CRS).

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `waf` |
| Why | the WAF policy is rebuilt on each successful reload |
| Requires | `waf` |

## `waf.mode` {#waf-mode}

Mode is "block" (default) — a rule interruption returns BlockStatus — or "detect", which records and logs the event but lets the request proceed.

| | |
| --- | --- |
| Type | `string` |
| Lifecycle | `hot_reload` |
| Subsystem | `waf` |
| Why | the WAF policy is rebuilt on each successful reload |
| Requires | `waf` |
| Default | block |
| Allowed values | `block`, `detect` |
| Constraint | exact lowercase enum |
| Zero/empty semantics | omitted selects the documented default where supported |
| Active when | WAF enabled |

## `waf.paranoia` {#waf-paranoia}

Paranoia sets the CRS blocking paranoia level (1–4) when CRSEnabled is set.

| | |
| --- | --- |
| Type | `integer` |
| Lifecycle | `hot_reload` |
| Subsystem | `waf` |
| Why | the WAF policy is rebuilt on each successful reload |
| Requires | `waf` |
| Constraint | 0 or 1..4; non-zero requires CRS |
| Zero/empty semantics | 0 selects the CRS default |
| Active when | WAF enabled |

## `waf.request_body_limit` {#waf-request_body_limit}

RequestBodyLimit caps how many request-body bytes are buffered for inspection.

| | |
| --- | --- |
| Type | `size` |
| Lifecycle | `hot_reload` |
| Subsystem | `waf` |
| Why | the WAF policy is rebuilt on each successful reload |
| Requires | `waf` |
| Default | 128k |
| Constraint | non-negative |
| Zero/empty semantics | omitted/zero defaults to 128 KiB |
| Active when | WAF enabled |

## `waf.response_body_check` {#waf-response_body_check}

ResponseBodyCheck enables inspection of response bodies (CRS phase 4).

| | |
| --- | --- |
| Type | `bool` |
| Lifecycle | `hot_reload` |
| Subsystem | `waf` |
| Why | the WAF policy is rebuilt on each successful reload |
| Requires | `waf` |

