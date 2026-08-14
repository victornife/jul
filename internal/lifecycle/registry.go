// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package lifecycle

import "sort"

// This file is the machine authority for configuration lifecycle behavior.
//
// Every leaf of the public TOML schema (config.SchemaLeaves) appears here
// exactly once. Adding a schema field without a matching entry fails
// TestRegistryCoversEverySchemaLeaf; adding an entry for a path the schema does
// not expose fails TestRegistryPathsExistInSchema. Regenerate the human and
// machine mirrors with `make lifecycle-generate` after any change here.
//
// Classification records behavior that the current source already exhibits. A
// field is never promoted to hot_reload in anticipation of work that has not
// landed: the response cache, static certificate material, access-log sinks and
// tracing stay restart-bound until their prepared-resource issues merge.

//go:generate go run ./lifecyclegen -out ../../docs

// Subsystem identifiers. The set is closed: TestSubsystemsAreDocumented fails
// for an entry whose subsystem has no description.
const (
	SubAccessControl  Subsystem = "access_control"
	SubAccessLog      Subsystem = "access_log"
	SubACME           Subsystem = "acme"
	SubAdmin          Subsystem = "admin"
	SubAuth           Subsystem = "auth"
	SubBackendTLS     Subsystem = "backend_tls"
	SubCache          Subsystem = "cache"
	SubClientAddress  Subsystem = "client_address"
	SubCompression    Subsystem = "compression"
	SubDiscovery      Subsystem = "discovery"
	SubEgress         Subsystem = "egress"
	SubErrorLog       Subsystem = "error_log"
	SubErrorPages     Subsystem = "error_pages"
	SubFastCGI        Subsystem = "fastcgi"
	SubGRPC           Subsystem = "grpc"
	SubGRPCTranscode  Subsystem = "grpc_transcode"
	SubH2C            Subsystem = "h2c"
	SubHeaders        Subsystem = "headers"
	SubHealthCheck    Subsystem = "health_check"
	SubHTTP3          Subsystem = "http3"
	SubListener       Subsystem = "listener"
	SubListenerLimits Subsystem = "listener_limits"
	SubListenerTimes  Subsystem = "listener_timeouts"
	SubLogFormat      Subsystem = "log_format"
	SubLogLevel       Subsystem = "log_level"
	SubMetrics        Subsystem = "metrics"
	SubMTLS           Subsystem = "mtls"
	SubPlugins        Subsystem = "plugins"
	SubProxyPass      Subsystem = "proxy_pass"
	SubProxyRetries   Subsystem = "proxy_retries"
	SubProxyTimeouts  Subsystem = "proxy_timeouts"
	SubRateLimit      Subsystem = "rate_limit"
	SubRBAC           Subsystem = "rbac"
	SubRedact         Subsystem = "redact"
	SubRedirect       Subsystem = "redirect"
	SubReloadTimeout  Subsystem = "reload_timeout"
	SubReturn         Subsystem = "return"
	SubRewrites       Subsystem = "rewrites"
	SubRoot           Subsystem = "root"
	SubRouting        Subsystem = "routing"
	SubServerIdentity Subsystem = "server_identity"
	SubServerLimits   Subsystem = "server_limits"
	SubServerNames    Subsystem = "server_names"
	SubServerRedirect Subsystem = "server_redirect"
	SubShutdown       Subsystem = "shutdown_timeout"
	SubStaticFiles    Subsystem = "static_files"
	SubStream         Subsystem = "stream"
	SubTLS            Subsystem = "tls"
	SubTracing        Subsystem = "tracing"
	SubTryFiles       Subsystem = "try_files"
	SubUpstream       Subsystem = "upstream"
	SubUWSGI          Subsystem = "uwsgi"
	SubWAF            Subsystem = "waf"
	SubWorkerThreads  Subsystem = "worker_threads"
)

// subsystemDescriptions is the closed set of subsystems with the operator-facing
// sentence rendered into the generated reference.
var subsystemDescriptions = map[Subsystem]string{
	SubAccessControl:  "Per-location allow/deny gates evaluated by the handler tree.",
	SubAccessLog:      "Access-record emission and its sinks.",
	SubACME:           "Automatic certificate management (ACME) for a TLS listener.",
	SubAdmin:          "The admin/observability listener and its startup-owned resources.",
	SubAuth:           "Per-location credential checks (CIDR, Basic, JWT, forward-auth).",
	SubBackendTLS:     "Outbound TLS trust for backend connections: roots, client certificate, verified name and peer identities.",
	SubCache:          "The two-tier response cache backend.",
	SubClientAddress:  "The per-listener trusted-proxy policy that derives the canonical client address.",
	SubCompression:    "Negotiated response compression.",
	SubDiscovery:      "Dynamic backend discovery for an upstream pool.",
	SubEgress:         "The outbound-destination allow-list applied to auxiliary fetches.",
	SubErrorLog:       "Legacy error-log destinations kept for v1 compatibility.",
	SubErrorPages:     "Per-server custom error documents.",
	SubFastCGI:        "FastCGI upstream dispatch.",
	SubGRPC:           "Native gRPC / HTTP-2 passthrough proxying.",
	SubGRPCTranscode:  "gRPC-JSON transcoding for a location.",
	SubH2C:            "Cleartext HTTP/2 on a plaintext listener.",
	SubHeaders:        "Headers added to the upstream request.",
	SubHealthCheck:    "Active health probing for an upstream pool.",
	SubHTTP3:          "The HTTP/3 (QUIC) listener and its Alt-Svc advertisement.",
	SubListener:       "The listen address a socket binds to.",
	SubListenerLimits: "Byte limits fixed when a listener binds.",
	SubListenerTimes:  "Timeouts fixed when a listener binds.",
	SubLogFormat:      "The process log encoding chosen when the logger is built.",
	SubLogLevel:       "The process log level.",
	SubMetrics:        "The Prometheus registry and its label configuration.",
	SubMTLS:           "Mutual-TLS client-certificate verification.",
	SubPlugins:        "WASM plugin definitions and the chains that reference them.",
	SubProxyPass:      "The reverse-proxy target of a location.",
	SubProxyRetries:   "Retry budget for idempotent proxied requests.",
	SubProxyTimeouts:  "Per-location proxy dial and I/O timeouts.",
	SubRateLimit:      "Request rate limiting and the per-listener connection cap.",
	SubRBAC:           "Admin role-based access control policy.",
	SubRedact:         "Secret redaction applied to logs.",
	SubRedirect:       "Location redirect targets.",
	SubReloadTimeout:  "The threshold that reports a slow reload.",
	SubReturn:         "Bare status returns for a location.",
	SubRewrites:       "Regex rewrite rules applied before dispatch.",
	SubRoot:           "Static-file document roots.",
	SubRouting:        "Location match selection.",
	SubServerIdentity: "Labels that identify a server block in projections.",
	SubServerLimits:   "Per-request body limits read by the handler tree.",
	SubServerNames:    "Virtual-host names used for routing and TLS certificate selection.",
	SubServerRedirect: "Server-level HTTPS redirection.",
	SubShutdown:       "The graceful-shutdown drain budget.",
	SubStaticFiles:    "Static-file serving behavior.",
	SubStream:         "L4 (TCP/UDP) stream-proxy listeners and routes.",
	SubTLS:            "TLS termination material and parameters for a listener.",
	SubTracing:        "OpenTelemetry tracing export.",
	SubTryFiles:       "Static-file fallback chains.",
	SubUpstream:       "Backend pools and their balancing policy.",
	SubUWSGI:          "uWSGI upstream dispatch.",
	SubWAF:            "Web application firewall policy.",
	SubWorkerThreads:  "The GOMAXPROCS cap applied to the process.",
}

// SubsystemDescription returns the operator-facing description of a subsystem.
func SubsystemDescription(s Subsystem) (string, bool) {
	d, ok := subsystemDescriptions[s]
	return d, ok
}

// Reload-behavior sentences shared by whole groups of paths. Keeping them in
// named constants makes it obvious when two paths are classified for the same
// proven reason rather than by coincidence.
const (
	reasonHandlerRebuild       = "the handler tree is rebuilt from the effective config on each successful reload"
	reasonPluginRebuild        = "the plugin set is rebuilt and re-instantiated on each successful reload"
	reasonUpstreamStaged       = "the upstream registry stages and swaps pools on each successful reload"
	reasonWAFRebuild           = "the WAF policy is rebuilt on each successful reload"
	reasonRateLimitPolicy      = "the rate-limiter store accepts a new policy on each successful reload"
	reasonStreamRoute          = "the stream listener swaps its route pointer atomically on each successful reload"
	reasonRBACSwap             = "the admin RBAC policy is rebuilt and atomically swapped after each successful reload"
	reasonAdminStartup         = "the admin listener and its resources are created once at startup"
	reasonCacheStartup         = "the response cache backend is created once at startup and retains its counters and LRU state across reloads"
	reasonAccessLogStart       = "access-log sinks are opened once at startup"
	reasonTracingStartup       = "the tracer provider and exporter are created once at startup"
	reasonBindFrozen           = "the value is read once when the socket binds; an address kept across the reload keeps the value it bound with"
	reasonTLSBindFrozen        = "TLS material is wired into the listener when it binds and reloadCertificates is a no-op, so a kept address serves the startup material until restart"
	reasonClientAddressRebuild = "the trusted-proxy policy is recompiled per listen address while the handler tree is prepared, so a malformed prefix aborts the reload before publish"
	reasonBackendTLSPool       = "the resolved policy is part of the pool's identity, so a changed policy — including a certificate rotated in place — rebuilds the pool and its probe client on the next successful reload"
	reasonBackendTLSRoute      = "the route's outbound clients (HTTP transport, native gRPC transport, transcoder connections) are built with the handler generation that owns them, so a changed policy takes effect on the next successful reload"
)

// Registry is the authoritative disposition of every public configuration path,
// sorted by path so generated artifacts and review diffs stay stable.
var Registry = assemble(
	adminEntries(),
	cacheEntries(),
	compressionEntries(),
	egressEntries(),
	globalEntries(),
	observabilityEntries(),
	pluginEntries(),
	rateLimitEntries(),
	serverEntries(),
	tlsEntries(),
	locationEntries(),
	streamEntries(),
	upstreamEntries(),
	wafEntries(),
)

// assemble flattens the per-subsystem groups and sorts them by path. Sorting at
// construction keeps Registry order independent of the grouping above, so the
// exact-path index and every generated artifact are deterministic.
func assemble(groups ...[]Entry) []Entry {
	var out []Entry
	for _, g := range groups {
		out = append(out, g...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// hot builds a hot-reloadable entry.
func hot(path string, sub Subsystem, reason string) Entry {
	return Entry{Path: path, Class: HotReloadClass, Subsystem: sub, Reason: reason}
}

// hotGroup builds hot-reloadable entries that share a subsystem and reason.
func hotGroup(sub Subsystem, reason string, paths ...string) []Entry {
	out := make([]Entry, 0, len(paths))
	for _, p := range paths {
		out = append(out, hot(p, sub, reason))
	}
	return out
}

// restart builds a restart-required entry. Every such entry is startup-consumed
// so the fingerprint actually compares it.
func restart(path string, sub Subsystem, reason string) Entry {
	return Entry{Path: path, Class: RestartRequiredClass, Subsystem: sub, Reason: reason, StartupConsumed: true}
}

// restartGroup builds restart-required entries sharing a subsystem and reason.
func restartGroup(sub Subsystem, reason string, paths ...string) []Entry {
	out := make([]Entry, 0, len(paths))
	for _, p := range paths {
		out = append(out, restart(p, sub, reason))
	}
	return out
}

// newListener builds an entry for a value frozen at bind time on an address
// that is not otherwise compared. Adding or removing the address is a normal
// hot operation; editing the value on a kept address is restart-required, which
// is why the entry is conditional.
func newListener(path string, sub Subsystem, reason string) Entry {
	return Entry{Path: path, Class: NewListenerOnlyClass, Subsystem: sub, Reason: reason, Conditional: true}
}

// bindBound builds an entry for a listener-bound value that the startup
// fingerprint compares per listen address.
func bindBound(path string, sub Subsystem, reason string) Entry {
	return Entry{
		Path: path, Class: RestartRequiredClass, Subsystem: sub, Reason: reason,
		StartupConsumed: true, AddressKeyed: true, Conditional: true,
	}
}

func bindBoundGroup(sub Subsystem, reason string, paths ...string) []Entry {
	out := make([]Entry, 0, len(paths))
	for _, p := range paths {
		out = append(out, bindBound(p, sub, reason))
	}
	return out
}

// ignored builds an entry for a field that no runtime consumer reads.
func ignored(path string, sub Subsystem, reason string, deprecated bool) Entry {
	return Entry{Path: path, Class: IgnoredDeprecatedClass, Subsystem: sub, Reason: reason, Ignored: true, Deprecated: deprecated}
}

// reserved builds an entry for a field that configuration validation rejects.
func reserved(path string, sub Subsystem, reason string) Entry {
	return Entry{Path: path, Class: ValidationRejectedReservedClass, Subsystem: sub, Reason: reason, Reserved: true}
}

// secretDigest marks an entry whose extractor emits a digest rather than the
// configured value.
func secretDigest(e Entry) Entry {
	e.Secret = true
	return e
}

func globalEntries() []Entry {
	return []Entry{
		ignored("global.access_log", SubAccessLog, "superseded by observability.access_log; no runtime consumer reads it", true),
		ignored("global.error_log", SubErrorLog, "structured process logs are written to stderr; no runtime consumer reads it", true),
		restart("global.log_format", SubLogFormat, "the slog handler encoding is chosen once when the logger is built at startup"),
		hot("global.log_level", SubLogLevel, "the level var is updated by OnReloaded on each successful reload"),
		hot("global.redact_min_secret_length", SubRedact, "the redaction state is rebuilt and installed atomically on each successful reload"),
		hot("global.reload_timeout", SubReloadTimeout, "the threshold is read from the effective config at the start of each reload"),
		hot("global.shutdown_timeout", SubShutdown, "the drain budget is read from the effective config on each graceful stop"),
		hot("global.worker_threads", SubWorkerThreads, "OnReloaded applies the cap with runtime.GOMAXPROCS, restoring the container-aware default for \"auto\""),
	}
}

func adminEntries() []Entry {
	adminPaths := []string{
		"admin.audit_log_file",
		"admin.audit_log_rotate_keep",
		"admin.audit_log_rotate_max_mb",
		"admin.console",
		"admin.enabled",
		"admin.history_dir",
		"admin.history_keep",
		"admin.listen",
		"admin.max_event_conns",
		"admin.plugin_upload_dir",
		"admin.plugin_upload_enabled",
		"admin.plugin_upload_max_size",
		"admin.rate_limit_apply_per_min",
		"admin.rate_limit_read_per_min",
		"admin.rate_limit_write_per_min",
	}
	out := restartGroup(SubAdmin, reasonAdminStartup, adminPaths...)
	out = append(out,
		secretDigest(restart("admin.token", SubAdmin, "the shared bearer token is captured when the admin listener is created; rotating it must not appear to succeed while the old token still grants access")),
		// The RBAC policy is rebuilt and atomically swapped after a successful
		// apply, and requirePermission reads the current policy per request, so
		// every RBAC leaf including the enable flag is genuinely hot (M-03, P3-01).
		hot("admin.rbac.default_role", SubRBAC, reasonRBACSwap),
		hot("admin.rbac.enabled", SubRBAC, reasonRBACSwap),
		hot("admin.rbac.principals.*.disabled", SubRBAC, reasonRBACSwap),
		hot("admin.rbac.principals.*.expires_at", SubRBAC, reasonRBACSwap),
		hot("admin.rbac.principals.*.name", SubRBAC, reasonRBACSwap),
		hot("admin.rbac.principals.*.role", SubRBAC, reasonRBACSwap),
		secretDigest(hot("admin.rbac.principals.*.token", SubRBAC, reasonRBACSwap)),
		hot("admin.rbac.roles.*.name", SubRBAC, reasonRBACSwap),
		hot("admin.rbac.roles.*.permissions", SubRBAC, reasonRBACSwap),
	)
	return out
}

func cacheEntries() []Entry {
	// The cache stays restart-bound in this issue. Prepared-resource swapping is
	// tracked by #92/#93 and must not be pre-authorized here.
	return restartGroup(SubCache, reasonCacheStartup,
		"cache.default_ttl",
		"cache.disk_max_size",
		"cache.disk_path",
		"cache.enabled",
		"cache.memory_max_size",
		"cache.stale_if_error",
		"cache.stale_while_revalidate",
	)
}

func compressionEntries() []Entry {
	return hotGroup(SubCompression, "the compression middleware is rebuilt with the handler tree on each successful reload",
		"compression.enabled",
		"compression.encoders",
		"compression.level",
		"compression.min_size",
		"compression.precompressed",
		"compression.types",
	)
}

func egressEntries() []Entry {
	return restartGroup(SubEgress, "the outbound dial policy is built once at startup and captured as an immutable set",
		"egress.allow",
		"egress.enabled",
	)
}

func observabilityEntries() []Entry {
	out := restartGroup(SubAccessLog, reasonAccessLogStart,
		"observability.access_log.enabled",
		"observability.access_log.file",
		"observability.access_log.format",
		"observability.access_log.rotate_keep",
		"observability.access_log.rotate_max_mb",
		"observability.access_log.sinks",
	)
	out = append(out, restart("observability.metrics.host_label", SubMetrics, "the Prometheus registry and its label set are built once at startup"))
	out = append(out, restartGroup(SubTracing, reasonTracingStartup,
		"observability.tracing.enabled",
		"observability.tracing.endpoint",
		"observability.tracing.exporter",
		"observability.tracing.insecure",
		"observability.tracing.sample_ratio",
		"observability.tracing.service_name",
	)...)
	return out
}

func pluginEntries() []Entry {
	return hotGroup(SubPlugins, reasonPluginRebuild,
		"plugins.*.allowed_hosts",
		"plugins.*.config.*",
		"plugins.*.fetch",
		"plugins.*.fetch_timeout",
		"plugins.*.inline",
		"plugins.*.kv",
		"plugins.*.kv_max_bytes",
		"plugins.*.kv_max_entries",
		"plugins.*.max_fetch_response",
		"plugins.*.max_request_body",
		"plugins.*.max_response_body",
		"plugins.*.memory_limit",
		"plugins.*.path",
		"plugins.*.timeout",
		"plugins.*.type",
	)
}

func rateLimitEntries() []Entry {
	out := hotGroup(SubRateLimit, reasonRateLimitPolicy,
		"rate_limit.burst",
		"rate_limit.enabled",
		"rate_limit.key",
		"rate_limit.rate",
	)
	out = append(out, newListener("rate_limit.max_conns", SubRateLimit,
		"the concurrent-connection cap is installed on each listener when it binds, so a kept address keeps the cap it bound with"))
	return out
}

func serverEntries() []Entry {
	out := []Entry{
		ignored("servers.*.access_log", SubAccessLog, "superseded by observability.access_log; no runtime consumer reads it", true),
		ignored("servers.*.error_log", SubErrorLog, "structured process logs are written to stderr; no runtime consumer reads it", true),
		hot("servers.*.client_max_body_size", SubServerLimits, "the handler reads the effective limit per request"),
		hot("servers.*.error_pages.*", SubErrorPages, reasonHandlerRebuild),
		hot("servers.*.name", SubServerIdentity, "the block label appears only in configuration projections, which are rebuilt from the effective config on each reload"),
		hot("servers.*.plugins", SubPlugins, reasonPluginRebuild),
		hot("servers.*.redirect_https", SubServerRedirect, reasonHandlerRebuild),
		hot("servers.*.server_names", SubServerNames, "virtual-host routing uses the rebuilt handler tree; when the block terminates TLS the name set is also part of the listener's certificate identity and is compared by the bind-time gate"),
		newListener("servers.*.listen", SubListener, "moving to a different address binds a new socket; the old address is drained"),
	}
	out = append(out, hotGroup(SubClientAddress, reasonClientAddressRebuild,
		"servers.*.client_address.forwarded_headers",
		"servers.*.client_address.max_hops",
		"servers.*.client_address.trusted_proxies")...)
	out = append(out, bindBoundGroup(SubH2C, "h2c is negotiated by the plaintext listener created at bind time",
		"servers.*.h2c")...)
	out = append(out, bindBoundGroup(SubClientAddress, "the PROXY-protocol wrapper is installed when the address binds, ahead of the TLS wrap, so it is fixed for the listener's lifetime",
		"servers.*.proxy_protocol")...)
	out = append(out, bindBoundGroup(SubHTTP3, "the QUIC listener and its Alt-Svc advertisement are created when the address binds",
		"servers.*.http3.alt_svc_max_age",
		"servers.*.http3.enabled")...)
	out = append(out,
		newListener("servers.*.idle_timeout", SubListenerTimes, reasonBindFrozen),
		newListener("servers.*.read_header_timeout", SubListenerTimes, reasonBindFrozen),
		newListener("servers.*.read_timeout", SubListenerTimes, reasonBindFrozen),
		newListener("servers.*.write_timeout", SubListenerTimes, reasonBindFrozen),
		newListener("servers.*.max_header_bytes", SubListenerLimits, reasonBindFrozen),
	)
	return out
}

func tlsEntries() []Entry {
	out := []Entry{
		bindBound("servers.*.tls.enabled", SubTLS, "whether the listener terminates TLS is decided when the address binds"),
		bindBound("servers.*.tls.min_version", SubTLS, "the minimum protocol version is written into the listener's tls.Config at bind time"),
		secretDigest(bindBound("servers.*.tls.cert", SubTLS, reasonTLSBindFrozen)),
		secretDigest(bindBound("servers.*.tls.key", SubTLS, reasonTLSBindFrozen)),
		bindBound("servers.*.tls.client_auth.mode", SubMTLS, "the client-certificate policy is written into the listener's tls.Config at bind time"),
		secretDigest(bindBound("servers.*.tls.client_auth.ca_file", SubMTLS, "the client CA pool is read and installed when the listener binds; the fingerprint digests the file contents so an in-place rotation is detected")),
		secretDigest(bindBound("servers.*.tls.client_auth.crl_file", SubMTLS, "the revocation list is read and installed when the listener binds; the fingerprint digests the file contents so an in-place rotation is detected")),
		bindBound("servers.*.tls.client_auth.verify_san", SubMTLS, "the SAN allow-list is captured by the listener's verify callback at bind time"),
		// Unlike the rest of the block this is a handler concern: what Jul sends
		// to a backend, not how the handshake is verified.
		hot("servers.*.tls.client_auth.forward_certificate", SubMTLS, "the client-certificate forwarding mode is read when the handler tree is rebuilt"),
	}
	out = append(out, bindBoundGroup(SubACME, "the ACME manager, its account and its certificate cache are created for the listener at bind time",
		"servers.*.tls.acme.ca",
		"servers.*.tls.acme.cache_dir",
		"servers.*.tls.acme.challenge",
		"servers.*.tls.acme.domains",
		"servers.*.tls.acme.email",
		"servers.*.tls.acme.enabled",
		"servers.*.tls.acme.ocsp_stapling")...)
	out = append(out, reserved("servers.*.tls.acme.dns_provider", SubACME,
		"DNS-01 is not implemented; Validate rejects a non-empty dns_provider, so no running process can have consumed it"))
	return out
}

func locationEntries() []Entry {
	loc := "servers.*.locations.*."
	out := hotGroup(SubRouting, reasonHandlerRebuild, loc+"match.path", loc+"match.type")
	out = append(out, backendTLSEntries(loc+"backend_tls.", true)...)
	out = append(out, hotGroup(SubStaticFiles, reasonHandlerRebuild,
		loc+"allow_hidden",
		loc+"cache_control",
		loc+"directory_listing",
	)...)
	out = append(out, hotGroup(SubAuth, "auth modifiers are rebuilt around each location action on every successful reload",
		loc+"auth.allow",
		loc+"auth.basic.file",
		loc+"auth.basic.realm",
		loc+"auth.deny",
		loc+"auth.forward_auth.auth_response_headers",
		loc+"auth.forward_auth.url",
		loc+"auth.jwt.algorithms",
		loc+"auth.jwt.audience",
		loc+"auth.jwt.issuer",
		loc+"auth.jwt.jwks_url",
	)...)
	out = append(out, hotGroup(SubGRPCTranscode, reasonHandlerRebuild,
		loc+"grpc_transcode.descriptor_set",
		loc+"grpc_transcode.max_message_size",
		loc+"grpc_transcode.preserve_proto_field_names",
		loc+"grpc_transcode.stream_mode",
		loc+"grpc_transcode.streaming",
		loc+"grpc_transcode.target",
		loc+"grpc_transcode.tls",
		loc+"grpc_transcode.use_reflection",
	)...)
	out = append(out, hotGroup(SubWAF, reasonWAFRebuild,
		loc+"waf.block_status",
		loc+"waf.crs_enabled",
		loc+"waf.directives_files",
		loc+"waf.enabled",
		loc+"waf.inline_rules",
		loc+"waf.mode",
		loc+"waf.paranoia",
		loc+"waf.request_body_limit",
		loc+"waf.response_body_check",
	)...)
	out = append(out, hotGroup(SubRateLimit, reasonRateLimitPolicy,
		loc+"rate_limit.burst",
		loc+"rate_limit.enabled",
		loc+"rate_limit.key",
		loc+"rate_limit.rate",
	)...)
	out = append(out, reserved(loc+"rate_limit.max_conns", SubRateLimit,
		"connection caps are listener-global; Validate rejects max_conns on a location override, so no running process can have consumed it"))
	out = append(out, hotGroup(SubRewrites, reasonHandlerRebuild,
		loc+"rewrites.*.flag",
		loc+"rewrites.*.pattern",
		loc+"rewrites.*.replacement",
	)...)
	out = append(out,
		hot(loc+"cache", SubCache, "whether a location may serve from the cache is decided by the rebuilt handler tree; the backend itself is startup-owned"),
		hot(loc+"client_max_body_size", SubServerLimits, reasonHandlerRebuild),
		hot(loc+"deny", SubAccessControl, reasonHandlerRebuild),
		hot(loc+"fastcgi_params.*", SubFastCGI, reasonHandlerRebuild),
		hot(loc+"fastcgi_pass", SubFastCGI, reasonHandlerRebuild),
		hot(loc+"grpc", SubGRPC, reasonHandlerRebuild),
		hot(loc+"headers.*", SubHeaders, reasonHandlerRebuild),
		hot(loc+"index", SubStaticFiles, reasonHandlerRebuild),
		hot(loc+"plugin", SubPlugins, reasonPluginRebuild),
		hot(loc+"plugins", SubPlugins, reasonPluginRebuild),
		hot(loc+"proxy_connect_timeout", SubProxyTimeouts, reasonHandlerRebuild),
		hot(loc+"proxy_pass", SubProxyPass, reasonHandlerRebuild),
		hot(loc+"proxy_read_timeout", SubProxyTimeouts, reasonHandlerRebuild),
		hot(loc+"proxy_retries", SubProxyRetries, reasonHandlerRebuild),
		hot(loc+"proxy_send_timeout", SubProxyTimeouts, reasonHandlerRebuild),
		hot(loc+"redirect", SubRedirect, reasonHandlerRebuild),
		hot(loc+"require_client_cert", SubMTLS, "the per-request certificate requirement is enforced by the rebuilt handler tree; the handshake policy itself is listener-bound"),
		hot(loc+"return", SubReturn, reasonHandlerRebuild),
		hot(loc+"root", SubRoot, reasonHandlerRebuild),
		hot(loc+"try_files", SubTryFiles, reasonHandlerRebuild),
		hot(loc+"uwsgi_pass", SubUWSGI, reasonHandlerRebuild),
	)
	return out
}

func streamEntries() []Entry {
	out := hotGroup(SubStream, reasonStreamRoute,
		"stream.*.connect_timeout",
		"stream.*.idle_timeout",
		"stream.*.max_udp_sessions",
		"stream.*.proxy_pass",
		"stream.*.proxy_protocol",
		"stream.*.sni_routes.*",
		"stream.*.tls_passthrough",
		"stream.*.trusted_proxies",
	)
	out = append(out,
		newListener("stream.*.listen", SubStream, "an L4 listener is keyed by protocol and address; moving to a different address binds a new socket and retires the old one"),
		// Proven by TestStreamProtocolSwitch* in internal/stream: Reload keys
		// listeners by "proto|addr", binds the candidate protocol's socket before
		// mutating any live state, and only then retires the old listener. TCP and
		// UDP coexist on the same numeric port, so the switch is a transactional
		// remove/add rather than a rebind of one socket.
		hot("stream.*.protocol", SubStream,
			"the stream reload binds the candidate protocol's listener before retiring the previous one; established connections and UDP sessions follow the retired listener's drain boundary while new traffic uses the candidate protocol"),
	)
	return out
}

func upstreamEntries() []Entry {
	out := hotGroup(SubUpstream, reasonUpstreamStaged,
		"upstreams.*.fail_timeout",
		"upstreams.*.max_fails",
		"upstreams.*.name",
		"upstreams.*.servers.*.address",
		"upstreams.*.servers.*.weight",
		"upstreams.*.strategy",
	)
	out = append(out, hotGroup(SubHealthCheck, "active probes are restarted with the pool on each successful reload",
		"upstreams.*.health_check.enabled",
		"upstreams.*.health_check.expect_body",
		"upstreams.*.health_check.expect_status",
		"upstreams.*.health_check.healthy_threshold",
		"upstreams.*.health_check.interval",
		"upstreams.*.health_check.path",
		"upstreams.*.health_check.timeout",
		"upstreams.*.health_check.type",
		"upstreams.*.health_check.unhealthy_threshold",
	)...)
	out = append(out, backendTLSEntries("upstreams.*.backend_tls.", false)...)
	// The Consul agent's trust is resolved with the pool, like a backend's.
	out = append(out, backendTLSEntries("upstreams.*.discovery.consul.tls.", false)...)
	out = append(out, hotGroup(SubDiscovery, "the per-pool discovery refresher is restarted with the pool on each successful reload",
		"upstreams.*.discovery.consul.address",
		"upstreams.*.discovery.consul.datacenter",
		"upstreams.*.discovery.consul.passing_only",
		"upstreams.*.discovery.consul.service",
		"upstreams.*.discovery.consul.tag",
		"upstreams.*.discovery.kubernetes.api_server",
		"upstreams.*.discovery.kubernetes.ca_file",
		"upstreams.*.discovery.kubernetes.insecure_skip_tls_verify",
		"upstreams.*.discovery.kubernetes.namespace",
		"upstreams.*.discovery.kubernetes.port",
		"upstreams.*.discovery.kubernetes.service",
		"upstreams.*.discovery.refresh",
		"upstreams.*.discovery.target",
		"upstreams.*.discovery.type",
	)...)
	out = append(out,
		secretDigest(hot("upstreams.*.discovery.consul.token", SubDiscovery, "the per-pool discovery refresher is restarted with the pool on each successful reload")),
		secretDigest(hot("upstreams.*.discovery.kubernetes.token", SubDiscovery, "the per-pool discovery refresher is restarted with the pool on each successful reload")),
	)
	return out
}

func wafEntries() []Entry {
	return hotGroup(SubWAF, reasonWAFRebuild,
		"waf.block_status",
		"waf.crs_enabled",
		"waf.directives_files",
		"waf.enabled",
		"waf.inline_rules",
		"waf.mode",
		"waf.paranoia",
		"waf.request_body_limit",
		"waf.response_body_check",
	)
}

// backendTLSEntries classifies one backend_tls block under prefix.
//
// The whole block is restart_required, truthfully: no outbound consumer rebuilds
// its TLS material on reload today. #138, #139 and #140 upgrade it per consumer
// as each integration lands — a class may only be promoted once every consumer
// of that field demonstrably adopts the candidate value.
//
// Trust material is registered as secret-bearing from the first release so the
// fingerprint digests file *contents*: rotating a certificate in place without
// editing the configuration is detected correctly even while the action remains
// a restart. Detection and action are separable, and getting detection right
// early costs nothing.
func backendTLSEntries(prefix string, routeScoped bool) []Entry {
	reason := reasonBackendTLSPool
	if routeScoped {
		reason = reasonBackendTLSRoute
	}
	out := hotGroup(SubBackendTLS, reason,
		prefix+"ca_mode",
		prefix+"insecure_skip_verify",
		prefix+"min_version",
		prefix+"peer_identities",
		prefix+"server_name",
	)
	// Trust material stays secret-bearing: the fingerprint digests file
	// contents, so a certificate rotated in place — without any configuration
	// edit — is detected and applied on the next reload.
	for _, path := range []string{prefix + "ca_file", prefix + "client_cert", prefix + "client_key"} {
		out = append(out, secretDigest(hot(path, SubBackendTLS, reason)))
	}
	return out
}
