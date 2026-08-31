<!--
GENERATED FILE — DO NOT EDIT.
Source of truth: internal/lifecycle/registry.go (the Go lifecycle registry).
Regenerate with: make lifecycle-generate
CI runs `make generated-check`, which fails when this file is stale.
-->

# Configuration lifecycle reference

Every public TOML configuration leaf and the disposition that governs it.
The Go registry in `internal/lifecycle/registry.go` is the machine authority;
this page, `docs/config-lifecycle.yaml` and `docs/generated/config-lifecycle.json`
are deterministic renderings of it. Conceptual reload behavior is described in
[reload-semantics.md](../reload-semantics.md).

## Coverage

| Measure | Count |
| --- | --- |
| Schema paths (containers included) | 356 |
| Schema leaves (configurable values) | 302 |
| Registry entries | 302 |
| Startup-consumed entries | 57 |
| Class `hot_reload` | 230 |
| Class `restart_required` | 57 |
| Class `new_listener_only` | 8 |
| Class `ignored_deprecated` | 4 |
| Class `validation_rejected_reserved` | 3 |

## Classes

| Class | Meaning |
| --- | --- |
| `hot_reload` | Takes effect on the next successful configuration reload; no process restart is needed. |
| `restart_required` | Consumed while the process starts. A change is persisted and reported, but the running process keeps the startup value until it restarts. |
| `new_listener_only` | Frozen when a socket binds. A newly added listen address adopts the value immediately; an address kept across the reload keeps the value it bound with until the process restarts. |
| `ignored_deprecated` | Parsed for compatibility but read by no runtime consumer. Changing it has no runtime effect and never creates a pending restart. |
| `validation_rejected_reserved` | A reserved seam that configuration validation rejects today, so no running process can have consumed it. |

## Conditional results

A conditional entry is resolved by `lifecycle.Classify` against the live listener
set, so preview and apply reach the same verdict.

| Condition | Meaning |
| --- | --- |
| `retained_listener` | at least one listen address kept across this reload changed the value it bound with, so the running listener keeps the old value until the process restarts |
| `new_listener_only` | only listen addresses that are added or removed by this reload are affected, so the new socket binds with the new value and no running listener is stranded |
| `listener_added_or_removed` | the change adds or removes a listener rather than editing one that is kept, so it takes effect on this reload |

## Subsystems

| Subsystem | Scope |
| --- | --- |
| `access_control` | Per-location allow/deny gates evaluated by the handler tree. |
| `access_log` | Access-record emission and its sinks. |
| `acme` | Automatic certificate management (ACME) for a TLS listener. |
| `admin` | The admin/observability listener and its startup-owned resources. |
| `auth` | Per-location credential checks (CIDR, Basic, JWT, forward-auth). |
| `backend_tls` | Outbound TLS trust for backend connections: roots, client certificate, verified name and peer identities. |
| `cache` | The two-tier response cache backend. |
| `client_address` | The per-listener trusted-proxy policy that derives the canonical client address. |
| `compression` | Negotiated response compression. |
| `config_authority` | Which subsystem owns configuration persistence, history and drift: managed or file_owned. |
| `cors` | Per-location Cross-Origin Resource Sharing policy and the preflight terminator it turns on. |
| `discovery` | Dynamic backend discovery for an upstream pool. |
| `egress` | The outbound-destination allow-list applied to auxiliary fetches. |
| `error_log` | Legacy error-log destinations kept for v1 compatibility. |
| `error_pages` | Per-server custom error documents. |
| `fastcgi` | FastCGI upstream dispatch. |
| `grpc` | Native gRPC / HTTP-2 passthrough proxying. |
| `grpc_transcode` | gRPC-JSON transcoding for a location. |
| `h2c` | Cleartext HTTP/2 on a plaintext listener. |
| `headers` | Headers added to the upstream request, and response-header add/set/remove operations. |
| `health_check` | Active health probing for an upstream pool. |
| `http3` | The HTTP/3 (QUIC) listener and its Alt-Svc advertisement. |
| `listener` | The listen address a socket binds to. |
| `listener_limits` | Byte limits fixed when a listener binds. |
| `listener_timeouts` | Timeouts fixed when a listener binds. |
| `log_format` | The process log encoding chosen when the logger is built. |
| `log_level` | The process log level. |
| `metrics` | The Prometheus registry and its label configuration. |
| `mtls` | Mutual-TLS client-certificate verification. |
| `plugins` | WASM plugin definitions and the chains that reference them. |
| `proxy_pass` | The reverse-proxy target of a location. |
| `proxy_retries` | Retry budget for idempotent proxied requests. |
| `proxy_timeouts` | Per-location proxy dial and I/O timeouts. |
| `rate_limit` | Request rate limiting and the per-listener connection cap. |
| `rbac` | Admin role-based access control policy. |
| `redact` | Secret redaction applied to logs. |
| `redirect` | Location redirect targets. |
| `reload_timeout` | The threshold that reports a slow reload. |
| `resilience` | Admission and overload control for an upstream pool: concurrency limits, the pending queue and its timeout. |
| `return` | Bare status returns for a location. |
| `rewrites` | Regex rewrite rules applied before dispatch. |
| `root` | Static-file document roots. |
| `routing` | Location match selection. |
| `server_identity` | Labels that identify a server block in projections. |
| `server_limits` | Per-request body limits read by the handler tree. |
| `server_names` | Virtual-host names used for routing and TLS certificate selection. |
| `server_redirect` | Server-level HTTPS redirection. |
| `shutdown_timeout` | The graceful-shutdown drain budget. |
| `static_files` | Static-file serving behavior. |
| `stream` | L4 (TCP/UDP) stream-proxy listeners and routes. |
| `tls` | TLS termination material and parameters for a listener. |
| `tracing` | OpenTelemetry tracing export. |
| `try_files` | Static-file fallback chains. |
| `upstream` | Backend pools and their balancing policy. |
| `uwsgi` | uWSGI upstream dispatch. |
| `waf` | Web application firewall policy. |
| `worker_threads` | The GOMAXPROCS cap applied to the process. |

## Fields

`startup` marks a path captured in the startup fingerprint. `cond.` marks a path
whose disposition depends on the live listener set. `digest` marks a path whose
value is compared as a digest so no secret material leaves the process.

| Path | Class | Subsystem | Flags | Why |
| --- | --- | --- | --- | --- |
| `admin.audit_log_file` | `restart_required` | `admin` | startup | the admin listener and its resources are created once at startup |
| `admin.audit_log_rotate_keep` | `restart_required` | `admin` | startup | the admin listener and its resources are created once at startup |
| `admin.audit_log_rotate_max_mb` | `restart_required` | `admin` | startup | the admin listener and its resources are created once at startup |
| `admin.console` | `restart_required` | `admin` | startup | the admin listener and its resources are created once at startup |
| `admin.enabled` | `restart_required` | `admin` | startup | the admin listener and its resources are created once at startup |
| `admin.history_dir` | `restart_required` | `admin` | startup | the admin listener and its resources are created once at startup |
| `admin.history_keep` | `restart_required` | `admin` | startup | the admin listener and its resources are created once at startup |
| `admin.listen` | `restart_required` | `admin` | startup | the admin listener and its resources are created once at startup |
| `admin.max_event_conns` | `restart_required` | `admin` | startup | the admin listener and its resources are created once at startup |
| `admin.plugin_upload_dir` | `restart_required` | `admin` | startup | the admin listener and its resources are created once at startup |
| `admin.plugin_upload_enabled` | `restart_required` | `admin` | startup | the admin listener and its resources are created once at startup |
| `admin.plugin_upload_max_size` | `restart_required` | `admin` | startup | the admin listener and its resources are created once at startup |
| `admin.rate_limit_apply_per_min` | `restart_required` | `admin` | startup | the admin listener and its resources are created once at startup |
| `admin.rate_limit_read_per_min` | `restart_required` | `admin` | startup | the admin listener and its resources are created once at startup |
| `admin.rate_limit_write_per_min` | `restart_required` | `admin` | startup | the admin listener and its resources are created once at startup |
| `admin.rbac.default_role` | `hot_reload` | `rbac` | — | the admin RBAC policy is rebuilt and atomically swapped after each successful reload |
| `admin.rbac.enabled` | `hot_reload` | `rbac` | — | the admin RBAC policy is rebuilt and atomically swapped after each successful reload |
| `admin.rbac.principals.*.disabled` | `hot_reload` | `rbac` | — | the admin RBAC policy is rebuilt and atomically swapped after each successful reload |
| `admin.rbac.principals.*.expires_at` | `hot_reload` | `rbac` | — | the admin RBAC policy is rebuilt and atomically swapped after each successful reload |
| `admin.rbac.principals.*.name` | `hot_reload` | `rbac` | — | the admin RBAC policy is rebuilt and atomically swapped after each successful reload |
| `admin.rbac.principals.*.role` | `hot_reload` | `rbac` | — | the admin RBAC policy is rebuilt and atomically swapped after each successful reload |
| `admin.rbac.principals.*.token` | `hot_reload` | `rbac` | digest | the admin RBAC policy is rebuilt and atomically swapped after each successful reload |
| `admin.rbac.roles.*.name` | `hot_reload` | `rbac` | — | the admin RBAC policy is rebuilt and atomically swapped after each successful reload |
| `admin.rbac.roles.*.permissions` | `hot_reload` | `rbac` | — | the admin RBAC policy is rebuilt and atomically swapped after each successful reload |
| `admin.tls.cert` | `hot_reload` | `tls` | digest | a candidate certificate provider is built and validated during preflight, then swapped atomically into the admin listener's existing dynamic provider on the next successful reload, reusing #100's seam (#336) |
| `admin.tls.client_auth.ca_file` | `restart_required` | `mtls` | startup, digest | the client CA pool is read and installed when the admin listener is created; the fingerprint digests the file contents so an in-place rotation is detected |
| `admin.tls.client_auth.crl_file` | `restart_required` | `mtls` | startup, digest | the revocation list is read and installed when the admin listener is created; the fingerprint digests the file contents so an in-place rotation is detected |
| `admin.tls.client_auth.forward_certificate` | `validation_rejected_reserved` | `mtls` | reserved | the admin API has no backend to forward a client certificate to; Validate rejects a non-none value, so no running process can have consumed it |
| `admin.tls.client_auth.mode` | `restart_required` | `mtls` | startup | the client-certificate policy is written into the admin listener's tls.Config when it is created |
| `admin.tls.client_auth.verify_san` | `restart_required` | `mtls` | startup | the SAN allow-list is captured by the admin listener's verify callback when it is created |
| `admin.tls.enabled` | `restart_required` | `tls` | startup | turning TLS on or off for the admin listener changes the socket's protocol, a structural transition |
| `admin.tls.key` | `hot_reload` | `tls` | digest | a candidate certificate provider is built and validated during preflight, then swapped atomically into the admin listener's existing dynamic provider on the next successful reload, reusing #100's seam (#336) |
| `admin.tls.min_version` | `restart_required` | `tls` | startup | the minimum protocol version is written into the admin listener's tls.Config when it is created |
| `admin.token` | `restart_required` | `admin` | startup, digest | the shared bearer token is captured when the admin listener is created; rotating it must not appear to succeed while the old token still grants access |
| `cache.default_ttl` | `restart_required` | `cache` | startup | the response cache backend is created once at startup and retains its counters and LRU state across reloads |
| `cache.disk_max_size` | `restart_required` | `cache` | startup | the response cache backend is created once at startup and retains its counters and LRU state across reloads |
| `cache.disk_path` | `restart_required` | `cache` | startup | the response cache backend is created once at startup and retains its counters and LRU state across reloads |
| `cache.enabled` | `restart_required` | `cache` | startup | the response cache backend is created once at startup and retains its counters and LRU state across reloads |
| `cache.memory_max_size` | `restart_required` | `cache` | startup | the response cache backend is created once at startup and retains its counters and LRU state across reloads |
| `cache.stale_if_error` | `restart_required` | `cache` | startup | the response cache backend is created once at startup and retains its counters and LRU state across reloads |
| `cache.stale_while_revalidate` | `restart_required` | `cache` | startup | the response cache backend is created once at startup and retains its counters and LRU state across reloads |
| `compression.enabled` | `hot_reload` | `compression` | — | the compression middleware is rebuilt with the handler tree on each successful reload |
| `compression.encoders` | `hot_reload` | `compression` | — | the compression middleware is rebuilt with the handler tree on each successful reload |
| `compression.level` | `hot_reload` | `compression` | — | the compression middleware is rebuilt with the handler tree on each successful reload |
| `compression.min_size` | `hot_reload` | `compression` | — | the compression middleware is rebuilt with the handler tree on each successful reload |
| `compression.precompressed` | `hot_reload` | `compression` | — | the compression middleware is rebuilt with the handler tree on each successful reload |
| `compression.types` | `hot_reload` | `compression` | — | the compression middleware is rebuilt with the handler tree on each successful reload |
| `egress.allow` | `restart_required` | `egress` | startup | the outbound dial policy is built once at startup and captured as an immutable set |
| `egress.enabled` | `restart_required` | `egress` | startup | the outbound dial policy is built once at startup and captured as an immutable set |
| `global.access_log` | `ignored_deprecated` | `access_log` | deprecated, ignored | superseded by observability.access_log; no runtime consumer reads it |
| `global.config_authority` | `restart_required` | `config_authority` | startup | authority is resolved once at startup and wires the file watcher, SIGHUP, and the managed baseline before any writer exists; changing it moves ownership of the configuration file and cannot be hot-applied (ADR 0019 §9.2) |
| `global.error_log` | `ignored_deprecated` | `error_log` | deprecated, ignored | structured process logs are written to stderr; no runtime consumer reads it |
| `global.log_format` | `restart_required` | `log_format` | startup | the slog handler encoding is chosen once when the logger is built at startup |
| `global.log_level` | `hot_reload` | `log_level` | — | the level var is updated by OnReloaded on each successful reload |
| `global.redact_min_secret_length` | `hot_reload` | `redact` | — | the redaction state is rebuilt and installed atomically on each successful reload |
| `global.reload_timeout` | `hot_reload` | `reload_timeout` | — | the threshold is read from the effective config at the start of each reload |
| `global.shutdown_timeout` | `hot_reload` | `shutdown_timeout` | — | the drain budget is read from the effective config on each graceful stop |
| `global.worker_threads` | `hot_reload` | `worker_threads` | — | OnReloaded applies the cap with runtime.GOMAXPROCS, restoring the container-aware default for "auto" |
| `observability.access_log.enabled` | `hot_reload` | `access_log` | — | a candidate sink generation is built and validated before Publish, then swapped in with the new handler generation; the previous generation's file/syslog resources close only after its requests drain (#98) |
| `observability.access_log.file` | `hot_reload` | `access_log` | — | a candidate sink generation is built and validated before Publish, then swapped in with the new handler generation; the previous generation's file/syslog resources close only after its requests drain (#98) |
| `observability.access_log.format` | `hot_reload` | `access_log` | — | a candidate sink generation is built and validated before Publish, then swapped in with the new handler generation; the previous generation's file/syslog resources close only after its requests drain (#98) |
| `observability.access_log.rotate_keep` | `hot_reload` | `access_log` | — | a candidate sink generation is built and validated before Publish, then swapped in with the new handler generation; the previous generation's file/syslog resources close only after its requests drain (#98) |
| `observability.access_log.rotate_max_mb` | `hot_reload` | `access_log` | — | a candidate sink generation is built and validated before Publish, then swapped in with the new handler generation; the previous generation's file/syslog resources close only after its requests drain (#98) |
| `observability.access_log.sinks` | `hot_reload` | `access_log` | — | a candidate sink generation is built and validated before Publish, then swapped in with the new handler generation; the previous generation's file/syslog resources close only after its requests drain (#98) |
| `observability.metrics.host_label` | `restart_required` | `metrics` | startup | the Prometheus registry and its label set are built once at startup |
| `observability.tracing.enabled` | `restart_required` | `tracing` | startup | the tracer provider and exporter are created once at startup |
| `observability.tracing.endpoint` | `restart_required` | `tracing` | startup | the tracer provider and exporter are created once at startup |
| `observability.tracing.exporter` | `restart_required` | `tracing` | startup | the tracer provider and exporter are created once at startup |
| `observability.tracing.insecure` | `restart_required` | `tracing` | startup | the tracer provider and exporter are created once at startup |
| `observability.tracing.sample_ratio` | `restart_required` | `tracing` | startup | the tracer provider and exporter are created once at startup |
| `observability.tracing.service_name` | `restart_required` | `tracing` | startup | the tracer provider and exporter are created once at startup |
| `plugins.*.allowed_hosts` | `hot_reload` | `plugins` | — | the plugin set is rebuilt and re-instantiated on each successful reload |
| `plugins.*.config.*` | `hot_reload` | `plugins` | — | the plugin set is rebuilt and re-instantiated on each successful reload |
| `plugins.*.fetch` | `hot_reload` | `plugins` | — | the plugin set is rebuilt and re-instantiated on each successful reload |
| `plugins.*.fetch_timeout` | `hot_reload` | `plugins` | — | the plugin set is rebuilt and re-instantiated on each successful reload |
| `plugins.*.inline` | `hot_reload` | `plugins` | — | the plugin set is rebuilt and re-instantiated on each successful reload |
| `plugins.*.kv` | `hot_reload` | `plugins` | — | the plugin set is rebuilt and re-instantiated on each successful reload |
| `plugins.*.kv_max_bytes` | `hot_reload` | `plugins` | — | the plugin set is rebuilt and re-instantiated on each successful reload |
| `plugins.*.kv_max_entries` | `hot_reload` | `plugins` | — | the plugin set is rebuilt and re-instantiated on each successful reload |
| `plugins.*.max_fetch_response` | `hot_reload` | `plugins` | — | the plugin set is rebuilt and re-instantiated on each successful reload |
| `plugins.*.max_request_body` | `hot_reload` | `plugins` | — | the plugin set is rebuilt and re-instantiated on each successful reload |
| `plugins.*.max_response_body` | `hot_reload` | `plugins` | — | the plugin set is rebuilt and re-instantiated on each successful reload |
| `plugins.*.memory_limit` | `hot_reload` | `plugins` | — | the plugin set is rebuilt and re-instantiated on each successful reload |
| `plugins.*.path` | `hot_reload` | `plugins` | — | the plugin set is rebuilt and re-instantiated on each successful reload |
| `plugins.*.timeout` | `hot_reload` | `plugins` | — | the plugin set is rebuilt and re-instantiated on each successful reload |
| `plugins.*.type` | `hot_reload` | `plugins` | — | the plugin set is rebuilt and re-instantiated on each successful reload |
| `rate_limit.burst` | `hot_reload` | `rate_limit` | — | the rate-limiter store accepts a new policy on each successful reload |
| `rate_limit.enabled` | `hot_reload` | `rate_limit` | — | the rate-limiter store accepts a new policy on each successful reload |
| `rate_limit.key` | `hot_reload` | `rate_limit` | — | the rate-limiter store accepts a new policy on each successful reload |
| `rate_limit.max_conns` | `new_listener_only` | `rate_limit` | cond. | the concurrent-connection cap is installed on each listener when it binds, so a kept address keeps the cap it bound with |
| `rate_limit.rate` | `hot_reload` | `rate_limit` | — | the rate-limiter store accepts a new policy on each successful reload |
| `servers.*.access_log` | `ignored_deprecated` | `access_log` | deprecated, ignored | superseded by observability.access_log; no runtime consumer reads it |
| `servers.*.client_address.forwarded_headers` | `hot_reload` | `client_address` | — | the trusted-proxy policy is recompiled per listen address while the handler tree is prepared, so a malformed prefix aborts the reload before publish |
| `servers.*.client_address.max_hops` | `hot_reload` | `client_address` | — | the trusted-proxy policy is recompiled per listen address while the handler tree is prepared, so a malformed prefix aborts the reload before publish |
| `servers.*.client_address.trusted_proxies` | `hot_reload` | `client_address` | — | the trusted-proxy policy is recompiled per listen address while the handler tree is prepared, so a malformed prefix aborts the reload before publish |
| `servers.*.client_max_body_size` | `hot_reload` | `server_limits` | — | the handler reads the effective limit per request |
| `servers.*.error_log` | `ignored_deprecated` | `error_log` | deprecated, ignored | structured process logs are written to stderr; no runtime consumer reads it |
| `servers.*.error_pages.*` | `hot_reload` | `error_pages` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.h2c` | `restart_required` | `h2c` | startup, per-address, cond. | h2c is negotiated by the plaintext listener created at bind time |
| `servers.*.http3.alt_svc_max_age` | `restart_required` | `http3` | startup, per-address, cond. | the QUIC listener and its Alt-Svc advertisement are created when the address binds |
| `servers.*.http3.enabled` | `restart_required` | `http3` | startup, per-address, cond. | the QUIC listener and its Alt-Svc advertisement are created when the address binds |
| `servers.*.idle_timeout` | `new_listener_only` | `listener_timeouts` | cond. | the value is read once when the socket binds; an address kept across the reload keeps the value it bound with |
| `servers.*.listen` | `new_listener_only` | `listener` | cond. | moving to a different address binds a new socket; the old address is drained |
| `servers.*.locations.*.allow_hidden` | `hot_reload` | `static_files` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.auth.allow` | `hot_reload` | `auth` | — | auth modifiers are rebuilt around each location action on every successful reload |
| `servers.*.locations.*.auth.basic.file` | `hot_reload` | `auth` | — | auth modifiers are rebuilt around each location action on every successful reload |
| `servers.*.locations.*.auth.basic.realm` | `hot_reload` | `auth` | — | auth modifiers are rebuilt around each location action on every successful reload |
| `servers.*.locations.*.auth.deny` | `hot_reload` | `auth` | — | auth modifiers are rebuilt around each location action on every successful reload |
| `servers.*.locations.*.auth.forward_auth.auth_response_headers` | `hot_reload` | `auth` | — | auth modifiers are rebuilt around each location action on every successful reload |
| `servers.*.locations.*.auth.forward_auth.timeout` | `hot_reload` | `auth` | — | auth modifiers are rebuilt around each location action on every successful reload |
| `servers.*.locations.*.auth.forward_auth.url` | `hot_reload` | `auth` | — | auth modifiers are rebuilt around each location action on every successful reload |
| `servers.*.locations.*.auth.jwt.algorithms` | `hot_reload` | `auth` | — | auth modifiers are rebuilt around each location action on every successful reload |
| `servers.*.locations.*.auth.jwt.audience` | `hot_reload` | `auth` | — | auth modifiers are rebuilt around each location action on every successful reload |
| `servers.*.locations.*.auth.jwt.issuer` | `hot_reload` | `auth` | — | auth modifiers are rebuilt around each location action on every successful reload |
| `servers.*.locations.*.auth.jwt.jwks_url` | `hot_reload` | `auth` | — | auth modifiers are rebuilt around each location action on every successful reload |
| `servers.*.locations.*.auth.jwt.timeout` | `hot_reload` | `auth` | — | auth modifiers are rebuilt around each location action on every successful reload |
| `servers.*.locations.*.backend_tls.ca_file` | `hot_reload` | `backend_tls` | digest | the route's outbound clients (HTTP transport, native gRPC transport, transcoder connections) are built with the handler generation that owns them, so a changed policy takes effect on the next successful reload |
| `servers.*.locations.*.backend_tls.ca_mode` | `hot_reload` | `backend_tls` | — | the route's outbound clients (HTTP transport, native gRPC transport, transcoder connections) are built with the handler generation that owns them, so a changed policy takes effect on the next successful reload |
| `servers.*.locations.*.backend_tls.client_cert` | `hot_reload` | `backend_tls` | digest | the route's outbound clients (HTTP transport, native gRPC transport, transcoder connections) are built with the handler generation that owns them, so a changed policy takes effect on the next successful reload |
| `servers.*.locations.*.backend_tls.client_key` | `hot_reload` | `backend_tls` | digest | the route's outbound clients (HTTP transport, native gRPC transport, transcoder connections) are built with the handler generation that owns them, so a changed policy takes effect on the next successful reload |
| `servers.*.locations.*.backend_tls.insecure_skip_verify` | `hot_reload` | `backend_tls` | — | the route's outbound clients (HTTP transport, native gRPC transport, transcoder connections) are built with the handler generation that owns them, so a changed policy takes effect on the next successful reload |
| `servers.*.locations.*.backend_tls.min_version` | `hot_reload` | `backend_tls` | — | the route's outbound clients (HTTP transport, native gRPC transport, transcoder connections) are built with the handler generation that owns them, so a changed policy takes effect on the next successful reload |
| `servers.*.locations.*.backend_tls.peer_identities` | `hot_reload` | `backend_tls` | — | the route's outbound clients (HTTP transport, native gRPC transport, transcoder connections) are built with the handler generation that owns them, so a changed policy takes effect on the next successful reload |
| `servers.*.locations.*.backend_tls.server_name` | `hot_reload` | `backend_tls` | — | the route's outbound clients (HTTP transport, native gRPC transport, transcoder connections) are built with the handler generation that owns them, so a changed policy takes effect on the next successful reload |
| `servers.*.locations.*.cache` | `hot_reload` | `cache` | — | whether a location may serve from the cache is decided by the rebuilt handler tree; the backend itself is startup-owned |
| `servers.*.locations.*.cache_control` | `hot_reload` | `static_files` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.client_max_body_size` | `hot_reload` | `server_limits` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.cors.allow_credentials` | `hot_reload` | `cors` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.cors.allowed_headers` | `hot_reload` | `cors` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.cors.allowed_methods` | `hot_reload` | `cors` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.cors.allowed_origins` | `hot_reload` | `cors` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.cors.enabled` | `hot_reload` | `cors` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.cors.exposed_headers` | `hot_reload` | `cors` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.cors.max_age` | `hot_reload` | `cors` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.deny` | `hot_reload` | `access_control` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.directory_listing` | `hot_reload` | `static_files` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.fastcgi_params.*` | `hot_reload` | `fastcgi` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.fastcgi_pass` | `hot_reload` | `fastcgi` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.grpc` | `hot_reload` | `grpc` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.grpc_transcode.descriptor_set` | `hot_reload` | `grpc_transcode` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.grpc_transcode.max_message_size` | `hot_reload` | `grpc_transcode` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.grpc_transcode.preserve_proto_field_names` | `hot_reload` | `grpc_transcode` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.grpc_transcode.stream_mode` | `hot_reload` | `grpc_transcode` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.grpc_transcode.streaming` | `hot_reload` | `grpc_transcode` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.grpc_transcode.target` | `hot_reload` | `grpc_transcode` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.grpc_transcode.tls` | `hot_reload` | `grpc_transcode` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.grpc_transcode.use_reflection` | `hot_reload` | `grpc_transcode` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.headers.*` | `hot_reload` | `headers` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.index` | `hot_reload` | `static_files` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.match.headers.*.name` | `hot_reload` | `routing` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.match.headers.*.op` | `hot_reload` | `routing` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.match.headers.*.value` | `hot_reload` | `routing` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.match.methods` | `hot_reload` | `routing` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.match.path` | `hot_reload` | `routing` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.match.query.*.name` | `hot_reload` | `routing` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.match.query.*.op` | `hot_reload` | `routing` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.match.query.*.value` | `hot_reload` | `routing` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.match.type` | `hot_reload` | `routing` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.plugin` | `hot_reload` | `plugins` | — | the plugin set is rebuilt and re-instantiated on each successful reload |
| `servers.*.locations.*.plugins` | `hot_reload` | `plugins` | — | the plugin set is rebuilt and re-instantiated on each successful reload |
| `servers.*.locations.*.proxy_connect_timeout` | `hot_reload` | `proxy_timeouts` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.proxy_pass` | `hot_reload` | `proxy_pass` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.proxy_read_timeout` | `hot_reload` | `proxy_timeouts` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.proxy_retries` | `hot_reload` | `proxy_retries` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.proxy_send_timeout` | `hot_reload` | `proxy_timeouts` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.rate_limit.burst` | `hot_reload` | `rate_limit` | — | the rate-limiter store accepts a new policy on each successful reload |
| `servers.*.locations.*.rate_limit.enabled` | `hot_reload` | `rate_limit` | — | the rate-limiter store accepts a new policy on each successful reload |
| `servers.*.locations.*.rate_limit.key` | `hot_reload` | `rate_limit` | — | the rate-limiter store accepts a new policy on each successful reload |
| `servers.*.locations.*.rate_limit.max_conns` | `validation_rejected_reserved` | `rate_limit` | reserved | connection caps are listener-global; Validate rejects max_conns on a location override, so no running process can have consumed it |
| `servers.*.locations.*.rate_limit.rate` | `hot_reload` | `rate_limit` | — | the rate-limiter store accepts a new policy on each successful reload |
| `servers.*.locations.*.redirect` | `hot_reload` | `redirect` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.require_client_cert` | `hot_reload` | `mtls` | — | the per-request certificate requirement is enforced by the rebuilt handler tree; the handshake policy itself is listener-bound |
| `servers.*.locations.*.resilience.max_connections_per_backend` | `hot_reload` | `resilience` | — | the bound is a property of the outbound transport, which is already rebuilt with the handler generation that owns it, so a changed value takes effect on the next successful reload; connections established under the previous bound follow that generation's drain boundary |
| `servers.*.locations.*.resilience.retry_attempts` | `hot_reload` | `resilience` | — | the retry settings are read from the live policy at the start of each request, so a changed value governs the next request; a sequence already in flight keeps the values it started under, because changing an attempt budget underneath a running retry would make the deadline arithmetic incoherent |
| `servers.*.locations.*.resilience.retry_backoff_initial` | `hot_reload` | `resilience` | — | the retry settings are read from the live policy at the start of each request, so a changed value governs the next request; a sequence already in flight keeps the values it started under, because changing an attempt budget underneath a running retry would make the deadline arithmetic incoherent |
| `servers.*.locations.*.resilience.retry_backoff_max` | `hot_reload` | `resilience` | — | the retry settings are read from the live policy at the start of each request, so a changed value governs the next request; a sequence already in flight keeps the values it started under, because changing an attempt budget underneath a running retry would make the deadline arithmetic incoherent |
| `servers.*.locations.*.resilience.retry_deadline` | `hot_reload` | `resilience` | — | the retry settings are read from the live policy at the start of each request, so a changed value governs the next request; a sequence already in flight keeps the values it started under, because changing an attempt budget underneath a running retry would make the deadline arithmetic incoherent |
| `servers.*.locations.*.response_headers.*.name` | `hot_reload` | `headers` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.response_headers.*.op` | `hot_reload` | `headers` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.response_headers.*.value` | `hot_reload` | `headers` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.return` | `hot_reload` | `return` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.rewrites.*.flag` | `hot_reload` | `rewrites` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.rewrites.*.pattern` | `hot_reload` | `rewrites` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.rewrites.*.replacement` | `hot_reload` | `rewrites` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.root` | `hot_reload` | `root` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.route_id` | `hot_reload` | `routing` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.try_files` | `hot_reload` | `try_files` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.uwsgi_pass` | `hot_reload` | `uwsgi` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.locations.*.waf.block_status` | `hot_reload` | `waf` | — | the WAF policy is rebuilt on each successful reload |
| `servers.*.locations.*.waf.crs_enabled` | `hot_reload` | `waf` | — | the WAF policy is rebuilt on each successful reload |
| `servers.*.locations.*.waf.directives_files` | `hot_reload` | `waf` | — | the WAF policy is rebuilt on each successful reload |
| `servers.*.locations.*.waf.enabled` | `hot_reload` | `waf` | — | the WAF policy is rebuilt on each successful reload |
| `servers.*.locations.*.waf.inline_rules` | `hot_reload` | `waf` | — | the WAF policy is rebuilt on each successful reload |
| `servers.*.locations.*.waf.mode` | `hot_reload` | `waf` | — | the WAF policy is rebuilt on each successful reload |
| `servers.*.locations.*.waf.paranoia` | `hot_reload` | `waf` | — | the WAF policy is rebuilt on each successful reload |
| `servers.*.locations.*.waf.request_body_limit` | `hot_reload` | `waf` | — | the WAF policy is rebuilt on each successful reload |
| `servers.*.locations.*.waf.response_body_check` | `hot_reload` | `waf` | — | the WAF policy is rebuilt on each successful reload |
| `servers.*.max_header_bytes` | `new_listener_only` | `listener_limits` | cond. | the value is read once when the socket binds; an address kept across the reload keeps the value it bound with |
| `servers.*.name` | `hot_reload` | `server_identity` | — | the block label appears only in configuration projections, which are rebuilt from the effective config on each reload |
| `servers.*.plugins` | `hot_reload` | `plugins` | — | the plugin set is rebuilt and re-instantiated on each successful reload |
| `servers.*.proxy_protocol` | `restart_required` | `client_address` | startup, per-address, cond. | the PROXY-protocol wrapper is installed when the address binds, ahead of the TLS wrap, so it is fixed for the listener's lifetime |
| `servers.*.read_header_timeout` | `new_listener_only` | `listener_timeouts` | cond. | the value is read once when the socket binds; an address kept across the reload keeps the value it bound with |
| `servers.*.read_timeout` | `new_listener_only` | `listener_timeouts` | cond. | the value is read once when the socket binds; an address kept across the reload keeps the value it bound with |
| `servers.*.redirect_https` | `hot_reload` | `server_redirect` | — | the handler tree is rebuilt from the effective config on each successful reload |
| `servers.*.server_names` | `hot_reload` | `server_names` | — | virtual-host routing uses the rebuilt handler tree; when the block terminates TLS the name set is also part of the listener's certificate identity and is compared by the bind-time gate |
| `servers.*.tls.acme.ca` | `restart_required` | `acme` | startup, per-address, cond. | the ACME manager, its account and its certificate cache are created for the listener at bind time |
| `servers.*.tls.acme.cache_dir` | `restart_required` | `acme` | startup, per-address, cond. | the ACME manager, its account and its certificate cache are created for the listener at bind time |
| `servers.*.tls.acme.challenge` | `restart_required` | `acme` | startup, per-address, cond. | the ACME manager, its account and its certificate cache are created for the listener at bind time |
| `servers.*.tls.acme.dns_provider` | `validation_rejected_reserved` | `acme` | reserved | DNS-01 is not implemented; Validate rejects a non-empty dns_provider, so no running process can have consumed it |
| `servers.*.tls.acme.domains` | `restart_required` | `acme` | startup, per-address, cond. | the ACME manager, its account and its certificate cache are created for the listener at bind time |
| `servers.*.tls.acme.email` | `restart_required` | `acme` | startup, per-address, cond. | the ACME manager, its account and its certificate cache are created for the listener at bind time |
| `servers.*.tls.acme.enabled` | `restart_required` | `acme` | startup, per-address, cond. | the ACME manager, its account and its certificate cache are created for the listener at bind time |
| `servers.*.tls.acme.ocsp_stapling` | `restart_required` | `acme` | startup, per-address, cond. | the ACME manager, its account and its certificate cache are created for the listener at bind time |
| `servers.*.tls.cert` | `hot_reload` | `tls` | digest | a candidate certificate provider is built and validated during Prepare and swapped atomically into the listener's existing dynamic provider at Publish, without rebinding (#100) |
| `servers.*.tls.client_auth.ca_file` | `restart_required` | `mtls` | startup, per-address, cond., digest | the client CA pool is read and installed when the listener binds; the fingerprint digests the file contents so an in-place rotation is detected |
| `servers.*.tls.client_auth.crl_file` | `restart_required` | `mtls` | startup, per-address, cond., digest | the revocation list is read and installed when the listener binds; the fingerprint digests the file contents so an in-place rotation is detected |
| `servers.*.tls.client_auth.forward_certificate` | `hot_reload` | `mtls` | — | the client-certificate forwarding mode is read when the handler tree is rebuilt |
| `servers.*.tls.client_auth.mode` | `restart_required` | `mtls` | startup, per-address, cond. | the client-certificate policy is written into the listener's tls.Config at bind time |
| `servers.*.tls.client_auth.verify_san` | `restart_required` | `mtls` | startup, per-address, cond. | the SAN allow-list is captured by the listener's verify callback at bind time |
| `servers.*.tls.enabled` | `restart_required` | `tls` | startup, per-address, cond. | whether the listener terminates TLS is decided when the address binds |
| `servers.*.tls.key` | `hot_reload` | `tls` | digest | a candidate certificate provider is built and validated during Prepare and swapped atomically into the listener's existing dynamic provider at Publish, without rebinding (#100) |
| `servers.*.tls.min_version` | `restart_required` | `tls` | startup, per-address, cond. | the minimum protocol version is written into the listener's tls.Config at bind time |
| `servers.*.write_timeout` | `new_listener_only` | `listener_timeouts` | cond. | the value is read once when the socket binds; an address kept across the reload keeps the value it bound with |
| `stream.*.connect_timeout` | `hot_reload` | `stream` | — | the stream listener swaps its route pointer atomically on each successful reload |
| `stream.*.idle_timeout` | `hot_reload` | `stream` | — | the stream listener swaps its route pointer atomically on each successful reload |
| `stream.*.listen` | `new_listener_only` | `stream` | cond. | an L4 listener is keyed by protocol and address; moving to a different address binds a new socket and retires the old one |
| `stream.*.max_udp_sessions` | `hot_reload` | `stream` | — | the stream listener swaps its route pointer atomically on each successful reload |
| `stream.*.protocol` | `hot_reload` | `stream` | — | the stream reload binds the candidate protocol's listener before retiring the previous one; established connections and UDP sessions follow the retired listener's drain boundary while new traffic uses the candidate protocol |
| `stream.*.proxy_pass` | `hot_reload` | `stream` | — | the stream listener swaps its route pointer atomically on each successful reload |
| `stream.*.proxy_protocol` | `hot_reload` | `stream` | — | the stream listener swaps its route pointer atomically on each successful reload |
| `stream.*.sni_routes.*` | `hot_reload` | `stream` | — | the stream listener swaps its route pointer atomically on each successful reload |
| `stream.*.tls_passthrough` | `hot_reload` | `stream` | — | the stream listener swaps its route pointer atomically on each successful reload |
| `stream.*.trusted_proxies` | `hot_reload` | `stream` | — | the stream listener swaps its route pointer atomically on each successful reload |
| `upstreams.*.backend_tls.ca_file` | `hot_reload` | `backend_tls` | digest | the resolved policy is part of the pool's identity, so a changed policy — including a certificate rotated in place — rebuilds the pool and its probe client on the next successful reload |
| `upstreams.*.backend_tls.ca_mode` | `hot_reload` | `backend_tls` | — | the resolved policy is part of the pool's identity, so a changed policy — including a certificate rotated in place — rebuilds the pool and its probe client on the next successful reload |
| `upstreams.*.backend_tls.client_cert` | `hot_reload` | `backend_tls` | digest | the resolved policy is part of the pool's identity, so a changed policy — including a certificate rotated in place — rebuilds the pool and its probe client on the next successful reload |
| `upstreams.*.backend_tls.client_key` | `hot_reload` | `backend_tls` | digest | the resolved policy is part of the pool's identity, so a changed policy — including a certificate rotated in place — rebuilds the pool and its probe client on the next successful reload |
| `upstreams.*.backend_tls.insecure_skip_verify` | `hot_reload` | `backend_tls` | — | the resolved policy is part of the pool's identity, so a changed policy — including a certificate rotated in place — rebuilds the pool and its probe client on the next successful reload |
| `upstreams.*.backend_tls.min_version` | `hot_reload` | `backend_tls` | — | the resolved policy is part of the pool's identity, so a changed policy — including a certificate rotated in place — rebuilds the pool and its probe client on the next successful reload |
| `upstreams.*.backend_tls.peer_identities` | `hot_reload` | `backend_tls` | — | the resolved policy is part of the pool's identity, so a changed policy — including a certificate rotated in place — rebuilds the pool and its probe client on the next successful reload |
| `upstreams.*.backend_tls.server_name` | `hot_reload` | `backend_tls` | — | the resolved policy is part of the pool's identity, so a changed policy — including a certificate rotated in place — rebuilds the pool and its probe client on the next successful reload |
| `upstreams.*.discovery.consul.address` | `hot_reload` | `discovery` | — | the per-pool discovery refresher is restarted with the pool on each successful reload |
| `upstreams.*.discovery.consul.datacenter` | `hot_reload` | `discovery` | — | the per-pool discovery refresher is restarted with the pool on each successful reload |
| `upstreams.*.discovery.consul.passing_only` | `hot_reload` | `discovery` | — | the per-pool discovery refresher is restarted with the pool on each successful reload |
| `upstreams.*.discovery.consul.service` | `hot_reload` | `discovery` | — | the per-pool discovery refresher is restarted with the pool on each successful reload |
| `upstreams.*.discovery.consul.tag` | `hot_reload` | `discovery` | — | the per-pool discovery refresher is restarted with the pool on each successful reload |
| `upstreams.*.discovery.consul.tls.ca_file` | `hot_reload` | `backend_tls` | digest | the resolved policy is part of the pool's identity, so a changed policy — including a certificate rotated in place — rebuilds the pool and its probe client on the next successful reload |
| `upstreams.*.discovery.consul.tls.ca_mode` | `hot_reload` | `backend_tls` | — | the resolved policy is part of the pool's identity, so a changed policy — including a certificate rotated in place — rebuilds the pool and its probe client on the next successful reload |
| `upstreams.*.discovery.consul.tls.client_cert` | `hot_reload` | `backend_tls` | digest | the resolved policy is part of the pool's identity, so a changed policy — including a certificate rotated in place — rebuilds the pool and its probe client on the next successful reload |
| `upstreams.*.discovery.consul.tls.client_key` | `hot_reload` | `backend_tls` | digest | the resolved policy is part of the pool's identity, so a changed policy — including a certificate rotated in place — rebuilds the pool and its probe client on the next successful reload |
| `upstreams.*.discovery.consul.tls.insecure_skip_verify` | `hot_reload` | `backend_tls` | — | the resolved policy is part of the pool's identity, so a changed policy — including a certificate rotated in place — rebuilds the pool and its probe client on the next successful reload |
| `upstreams.*.discovery.consul.tls.min_version` | `hot_reload` | `backend_tls` | — | the resolved policy is part of the pool's identity, so a changed policy — including a certificate rotated in place — rebuilds the pool and its probe client on the next successful reload |
| `upstreams.*.discovery.consul.tls.peer_identities` | `hot_reload` | `backend_tls` | — | the resolved policy is part of the pool's identity, so a changed policy — including a certificate rotated in place — rebuilds the pool and its probe client on the next successful reload |
| `upstreams.*.discovery.consul.tls.server_name` | `hot_reload` | `backend_tls` | — | the resolved policy is part of the pool's identity, so a changed policy — including a certificate rotated in place — rebuilds the pool and its probe client on the next successful reload |
| `upstreams.*.discovery.consul.token` | `hot_reload` | `discovery` | digest | the per-pool discovery refresher is restarted with the pool on each successful reload |
| `upstreams.*.discovery.kubernetes.api_server` | `hot_reload` | `discovery` | — | the per-pool discovery refresher is restarted with the pool on each successful reload |
| `upstreams.*.discovery.kubernetes.ca_file` | `hot_reload` | `discovery` | — | the per-pool discovery refresher is restarted with the pool on each successful reload |
| `upstreams.*.discovery.kubernetes.insecure_skip_tls_verify` | `hot_reload` | `discovery` | — | the per-pool discovery refresher is restarted with the pool on each successful reload |
| `upstreams.*.discovery.kubernetes.namespace` | `hot_reload` | `discovery` | — | the per-pool discovery refresher is restarted with the pool on each successful reload |
| `upstreams.*.discovery.kubernetes.port` | `hot_reload` | `discovery` | — | the per-pool discovery refresher is restarted with the pool on each successful reload |
| `upstreams.*.discovery.kubernetes.service` | `hot_reload` | `discovery` | — | the per-pool discovery refresher is restarted with the pool on each successful reload |
| `upstreams.*.discovery.kubernetes.token` | `hot_reload` | `discovery` | digest | the per-pool discovery refresher is restarted with the pool on each successful reload |
| `upstreams.*.discovery.refresh` | `hot_reload` | `discovery` | — | the per-pool discovery refresher is restarted with the pool on each successful reload |
| `upstreams.*.discovery.target` | `hot_reload` | `discovery` | — | the per-pool discovery refresher is restarted with the pool on each successful reload |
| `upstreams.*.discovery.type` | `hot_reload` | `discovery` | — | the per-pool discovery refresher is restarted with the pool on each successful reload |
| `upstreams.*.fail_timeout` | `hot_reload` | `resilience` | — | the bound is retuned on each backend in place while circuit state, the failure count and any half-open probe already in flight are preserved: rebuilding the breaker would forget which backends are currently out of rotation, and a reload during an incident would put every one of them back under full load at once |
| `upstreams.*.health_check.enabled` | `hot_reload` | `health_check` | — | active probes are restarted with the pool on each successful reload |
| `upstreams.*.health_check.expect_body` | `hot_reload` | `health_check` | — | active probes are restarted with the pool on each successful reload |
| `upstreams.*.health_check.expect_status` | `hot_reload` | `health_check` | — | active probes are restarted with the pool on each successful reload |
| `upstreams.*.health_check.healthy_threshold` | `hot_reload` | `health_check` | — | active probes are restarted with the pool on each successful reload |
| `upstreams.*.health_check.interval` | `hot_reload` | `health_check` | — | active probes are restarted with the pool on each successful reload |
| `upstreams.*.health_check.path` | `hot_reload` | `health_check` | — | active probes are restarted with the pool on each successful reload |
| `upstreams.*.health_check.timeout` | `hot_reload` | `health_check` | — | active probes are restarted with the pool on each successful reload |
| `upstreams.*.health_check.type` | `hot_reload` | `health_check` | — | active probes are restarted with the pool on each successful reload |
| `upstreams.*.health_check.unhealthy_threshold` | `hot_reload` | `health_check` | — | active probes are restarted with the pool on each successful reload |
| `upstreams.*.max_fails` | `hot_reload` | `resilience` | — | the bound is retuned on each backend in place while circuit state, the failure count and any half-open probe already in flight are preserved: rebuilding the breaker would forget which backends are currently out of rotation, and a reload during an incident would put every one of them back under full load at once |
| `upstreams.*.name` | `hot_reload` | `upstream` | — | the upstream registry stages and swaps pools on each successful reload |
| `upstreams.*.resilience.circuit_half_open_probes` | `hot_reload` | `resilience` | — | the bound is retuned on each backend in place while circuit state, the failure count and any half-open probe already in flight are preserved: rebuilding the breaker would forget which backends are currently out of rotation, and a reload during an incident would put every one of them back under full load at once |
| `upstreams.*.resilience.fail_timeout` | `hot_reload` | `resilience` | — | the bound is retuned on each backend in place while circuit state, the failure count and any half-open probe already in flight are preserved: rebuilding the breaker would forget which backends are currently out of rotation, and a reload during an incident would put every one of them back under full load at once |
| `upstreams.*.resilience.max_active_per_backend` | `hot_reload` | `resilience` | — | the resolved resilience policy is swapped into the live pool as an atomic pointer at commit, deliberately without rebuilding it: admission counters, parked requests and per-backend state all survive, a raised limit wakes waiters immediately, and a lowered one lets the excess drain instead of failing requests that are already in flight |
| `upstreams.*.resilience.max_active_requests` | `hot_reload` | `resilience` | — | the resolved resilience policy is swapped into the live pool as an atomic pointer at commit, deliberately without rebuilding it: admission counters, parked requests and per-backend state all survive, a raised limit wakes waiters immediately, and a lowered one lets the excess drain instead of failing requests that are already in flight |
| `upstreams.*.resilience.max_connections_per_backend` | `hot_reload` | `resilience` | — | the bound is a property of the outbound transport, which is already rebuilt with the handler generation that owns it, so a changed value takes effect on the next successful reload; connections established under the previous bound follow that generation's drain boundary |
| `upstreams.*.resilience.max_fails` | `hot_reload` | `resilience` | — | the bound is retuned on each backend in place while circuit state, the failure count and any half-open probe already in flight are preserved: rebuilding the breaker would forget which backends are currently out of rotation, and a reload during an incident would put every one of them back under full load at once |
| `upstreams.*.resilience.max_pending_requests` | `hot_reload` | `resilience` | — | the resolved resilience policy is swapped into the live pool as an atomic pointer at commit, deliberately without rebuilding it: admission counters, parked requests and per-backend state all survive, a raised limit wakes waiters immediately, and a lowered one lets the excess drain instead of failing requests that are already in flight |
| `upstreams.*.resilience.pending_timeout` | `hot_reload` | `resilience` | — | the resolved resilience policy is swapped into the live pool as an atomic pointer at commit, deliberately without rebuilding it: admission counters, parked requests and per-backend state all survive, a raised limit wakes waiters immediately, and a lowered one lets the excess drain instead of failing requests that are already in flight |
| `upstreams.*.resilience.retry_attempts` | `hot_reload` | `resilience` | — | the retry settings are read from the live policy at the start of each request, so a changed value governs the next request; a sequence already in flight keeps the values it started under, because changing an attempt budget underneath a running retry would make the deadline arithmetic incoherent |
| `upstreams.*.resilience.retry_backoff_initial` | `hot_reload` | `resilience` | — | the retry settings are read from the live policy at the start of each request, so a changed value governs the next request; a sequence already in flight keeps the values it started under, because changing an attempt budget underneath a running retry would make the deadline arithmetic incoherent |
| `upstreams.*.resilience.retry_backoff_max` | `hot_reload` | `resilience` | — | the retry settings are read from the live policy at the start of each request, so a changed value governs the next request; a sequence already in flight keeps the values it started under, because changing an attempt budget underneath a running retry would make the deadline arithmetic incoherent |
| `upstreams.*.resilience.retry_budget_percent` | `hot_reload` | `resilience` | — | the percentage is swapped into the live budget while its accumulated window is deliberately preserved: resetting the window on reload would hand out a fresh burst of retries, and a reload during an incident is the least appropriate moment to forgive the retry load that helped cause it |
| `upstreams.*.resilience.retry_deadline` | `hot_reload` | `resilience` | — | the retry settings are read from the live policy at the start of each request, so a changed value governs the next request; a sequence already in flight keeps the values it started under, because changing an attempt budget underneath a running retry would make the deadline arithmetic incoherent |
| `upstreams.*.servers.*.address` | `hot_reload` | `upstream` | — | the upstream registry stages and swaps pools on each successful reload |
| `upstreams.*.servers.*.weight` | `hot_reload` | `upstream` | — | the upstream registry stages and swaps pools on each successful reload |
| `upstreams.*.strategy` | `hot_reload` | `upstream` | — | the upstream registry stages and swaps pools on each successful reload |
| `waf.block_status` | `hot_reload` | `waf` | — | the WAF policy is rebuilt on each successful reload |
| `waf.crs_enabled` | `hot_reload` | `waf` | — | the WAF policy is rebuilt on each successful reload |
| `waf.directives_files` | `hot_reload` | `waf` | — | the WAF policy is rebuilt on each successful reload |
| `waf.enabled` | `hot_reload` | `waf` | — | the WAF policy is rebuilt on each successful reload |
| `waf.inline_rules` | `hot_reload` | `waf` | — | the WAF policy is rebuilt on each successful reload |
| `waf.mode` | `hot_reload` | `waf` | — | the WAF policy is rebuilt on each successful reload |
| `waf.paranoia` | `hot_reload` | `waf` | — | the WAF policy is rebuilt on each successful reload |
| `waf.request_body_limit` | `hot_reload` | `waf` | — | the WAF policy is rebuilt on each successful reload |
| `waf.response_body_check` | `hot_reload` | `waf` | — | the WAF policy is rebuilt on each successful reload |
