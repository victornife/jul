// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package lifecycle is the single source of truth for which configuration
// fields can be hot-reloaded and which require a process restart (or a new
// listener only). It is used by the reload path, admin preflight, diff,
// Console UI, and documentation validators so that all consumers agree on the
// reload semantics of every field.
package lifecycle

import (
	"fmt"
	"strings"
)

// Class categorizes how a configuration field may be changed.
type Class int

const (
	// HotReloadClass means the field can be changed at runtime via config reload.
	HotReloadClass Class = iota
	// RestartRequiredClass means the field is consumed at process startup and cannot
	// change without restarting the server.
	RestartRequiredClass
	// NewListenerOnlyClass means the change is honored only when a new listener is
	// created (e.g. a bind address change); existing listeners keep the old value
	// until they are recreated.
	NewListenerOnlyClass
)

func (c Class) String() string {
	switch c {
	case HotReloadClass:
		return "hot_reload"
	case RestartRequiredClass:
		return "restart_required"
	case NewListenerOnlyClass:
		return "new_listener_only"
	default:
		return fmt.Sprintf("lifecycle.Class(%d)", c)
	}
}

// Entry describes one configuration path and its reload semantics.
type Entry struct {
	// Path is the TOML path, e.g. "global.log_format" or "servers.*.tls.cert_file".
	Path string
	// Class is how the field may be changed at runtime.
	Class Class
	// Subsystem is a short human-readable group, e.g. "log_format" or "tls".
	Subsystem string
	// Reason explains why the field has this class.
	Reason string
	// StartupConsumed is true when the effective value of this field is captured
	// in the startup fingerprint and must not change without a restart.
	StartupConsumed bool
}

// Registry is the authoritative list of classified configuration paths.
//
// The list is intentionally flat (no wildcards in code) so that every path is
// explicit and reviewable. Wildcard paths in docs/config-lifecycle.yaml are
// expanded against this registry at validation time.
var Registry = []Entry{
	// Global process settings.
	{Path: "global.worker_threads", Class: HotReloadClass, Subsystem: "worker_threads", Reason: "log level and GOMAXPROCS are applied dynamically via OnReloaded"},
	{Path: "global.access_log", Class: RestartRequiredClass, Subsystem: "access_log", Reason: "access-log sinks are built once at startup", StartupConsumed: true},
	{Path: "global.error_log", Class: RestartRequiredClass, Subsystem: "error_log", Reason: "error-log sink is built once at startup", StartupConsumed: true},
	{Path: "global.log_level", Class: HotReloadClass, Subsystem: "log_level", Reason: "log level is applied dynamically via OnReloaded"},
	{Path: "global.log_format", Class: RestartRequiredClass, Subsystem: "log_format", Reason: "log handler format is chosen once at startup", StartupConsumed: true},
	{Path: "global.shutdown_timeout", Class: HotReloadClass, Subsystem: "shutdown_timeout", Reason: "shutdown timeout is read from effective config on each graceful stop"},
	{Path: "global.reload_timeout", Class: HotReloadClass, Subsystem: "reload_timeout", Reason: "reload timeout threshold is read on each reload"},
	{Path: "global.redact_min_secret_length", Class: HotReloadClass, Subsystem: "redact", Reason: "redaction state is rebuilt and installed atomically on each reload"},

	// Admin settings.
	{Path: "admin.enabled", Class: RestartRequiredClass, Subsystem: "admin", Reason: "admin listener is created at startup", StartupConsumed: true},
	{Path: "admin.listen", Class: RestartRequiredClass, Subsystem: "admin", Reason: "admin listener address is chosen at startup", StartupConsumed: true},
	{Path: "admin.token", Class: RestartRequiredClass, Subsystem: "admin", Reason: "admin token is consumed at startup and its digest is part of the startup fingerprint", StartupConsumed: true},
	{Path: "admin.console", Class: RestartRequiredClass, Subsystem: "admin", Reason: "admin console flag is read at startup", StartupConsumed: true},
	{Path: "admin.history_dir", Class: RestartRequiredClass, Subsystem: "admin", Reason: "config history directory is opened at startup", StartupConsumed: true},
	{Path: "admin.history_keep", Class: RestartRequiredClass, Subsystem: "admin", Reason: "config history retention is configured at startup", StartupConsumed: true},
	{Path: "admin.rate_limit_read_per_min", Class: RestartRequiredClass, Subsystem: "admin", Reason: "admin rate-limit buckets are created at startup", StartupConsumed: true},
	{Path: "admin.rate_limit_write_per_min", Class: RestartRequiredClass, Subsystem: "admin", Reason: "admin rate-limit buckets are created at startup", StartupConsumed: true},
	{Path: "admin.rate_limit_apply_per_min", Class: RestartRequiredClass, Subsystem: "admin", Reason: "admin rate-limit buckets are created at startup", StartupConsumed: true},
	{Path: "admin.max_event_conns", Class: RestartRequiredClass, Subsystem: "admin", Reason: "event-source connection limit is configured at startup", StartupConsumed: true},
	{Path: "admin.audit_log_file", Class: RestartRequiredClass, Subsystem: "admin", Reason: "audit log sink is opened at startup", StartupConsumed: true},
	{Path: "admin.audit_log_rotate_max_mb", Class: RestartRequiredClass, Subsystem: "admin", Reason: "audit log rotation is configured at startup", StartupConsumed: true},
	{Path: "admin.audit_log_rotate_keep", Class: RestartRequiredClass, Subsystem: "admin", Reason: "audit log rotation is configured at startup", StartupConsumed: true},
	{Path: "admin.plugin_upload_dir", Class: RestartRequiredClass, Subsystem: "admin", Reason: "plugin upload directory is opened at startup", StartupConsumed: true},
	{Path: "admin.plugin_upload_max_size", Class: RestartRequiredClass, Subsystem: "admin", Reason: "plugin upload limits are configured at startup", StartupConsumed: true},
	{Path: "admin.plugin_upload_enabled", Class: RestartRequiredClass, Subsystem: "admin", Reason: "plugin upload endpoint is configured at startup", StartupConsumed: true},

	// Cache settings.
	{Path: "cache.enabled", Class: RestartRequiredClass, Subsystem: "cache", Reason: "cache backend is initialized once at startup", StartupConsumed: true},
	{Path: "cache.memory_max_size", Class: RestartRequiredClass, Subsystem: "cache", Reason: "cache memory cap is fixed at backend creation", StartupConsumed: true},
	{Path: "cache.disk_path", Class: RestartRequiredClass, Subsystem: "cache", Reason: "disk cache directory is opened at startup", StartupConsumed: true},
	{Path: "cache.disk_max_size", Class: RestartRequiredClass, Subsystem: "cache", Reason: "disk cache cap is fixed at backend creation", StartupConsumed: true},
	{Path: "cache.default_ttl", Class: RestartRequiredClass, Subsystem: "cache", Reason: "cache default TTL is fixed at backend creation", StartupConsumed: true},
	{Path: "cache.stale_while_revalidate", Class: RestartRequiredClass, Subsystem: "cache", Reason: "cache stale policy is fixed at backend creation", StartupConsumed: true},
	{Path: "cache.stale_if_error", Class: RestartRequiredClass, Subsystem: "cache", Reason: "cache stale policy is fixed at backend creation", StartupConsumed: true},

	// Observability settings.
	{Path: "observability.metrics.host_label", Class: RestartRequiredClass, Subsystem: "metrics", Reason: "metrics registry is created at startup", StartupConsumed: true},
	{Path: "observability.tracing.enabled", Class: RestartRequiredClass, Subsystem: "tracing", Reason: "tracer provider is initialized at startup", StartupConsumed: true},
	{Path: "observability.tracing.exporter", Class: RestartRequiredClass, Subsystem: "tracing", Reason: "tracer exporter is created at startup", StartupConsumed: true},
	{Path: "observability.tracing.endpoint", Class: RestartRequiredClass, Subsystem: "tracing", Reason: "tracer exporter is created at startup", StartupConsumed: true},
	{Path: "observability.tracing.sample_ratio", Class: RestartRequiredClass, Subsystem: "tracing", Reason: "sampler is configured at startup", StartupConsumed: true},
	{Path: "observability.tracing.service_name", Class: RestartRequiredClass, Subsystem: "tracing", Reason: "tracer resource attributes are configured at startup", StartupConsumed: true},
	{Path: "observability.tracing.insecure", Class: RestartRequiredClass, Subsystem: "tracing", Reason: "tracer transport security is configured at startup", StartupConsumed: true},
	{Path: "observability.access_log.sinks", Class: RestartRequiredClass, Subsystem: "access_log", Reason: "access-log sinks are built once at startup", StartupConsumed: true},
	{Path: "observability.access_log.file", Class: RestartRequiredClass, Subsystem: "access_log", Reason: "file sink handle is opened at startup", StartupConsumed: true},
	{Path: "observability.access_log.format", Class: RestartRequiredClass, Subsystem: "access_log", Reason: "sink formatter is chosen at startup", StartupConsumed: true},
	{Path: "observability.access_log.rotate_max_mb", Class: RestartRequiredClass, Subsystem: "access_log", Reason: "file sink rotation is configured at startup", StartupConsumed: true},
	{Path: "observability.access_log.rotate_keep", Class: RestartRequiredClass, Subsystem: "access_log", Reason: "file sink rotation is configured at startup", StartupConsumed: true},

	// Egress settings.
	{Path: "egress.enabled", Class: RestartRequiredClass, Subsystem: "egress", Reason: "egress allow-list is built once at startup", StartupConsumed: true},
	{Path: "egress.allow", Class: RestartRequiredClass, Subsystem: "egress", Reason: "egress allow-list is built once at startup", StartupConsumed: true},

	// Server-level listener settings.
	{Path: "servers.*.listen", Class: NewListenerOnlyClass, Subsystem: "listener", Reason: "listen address change requires a new socket"},
	{Path: "servers.*.tls", Class: RestartRequiredClass, Subsystem: "tls", Reason: "TLS configuration is bound to a listener at creation", StartupConsumed: true},
	{Path: "servers.*.http3", Class: RestartRequiredClass, Subsystem: "http3", Reason: "HTTP/3 is bound to a listener at creation", StartupConsumed: true},
	{Path: "servers.*.h2c", Class: RestartRequiredClass, Subsystem: "h2c", Reason: "h2c is negotiated at listener creation", StartupConsumed: true},
	{Path: "servers.*.read_timeout", Class: NewListenerOnlyClass, Subsystem: "listener_timeouts", Reason: "read timeout is fixed when the socket binds"},
	{Path: "servers.*.read_header_timeout", Class: NewListenerOnlyClass, Subsystem: "listener_timeouts", Reason: "read header timeout is fixed when the socket binds"},
	{Path: "servers.*.write_timeout", Class: NewListenerOnlyClass, Subsystem: "listener_timeouts", Reason: "write timeout is fixed when the socket binds"},
	{Path: "servers.*.idle_timeout", Class: NewListenerOnlyClass, Subsystem: "listener_timeouts", Reason: "idle timeout is fixed when the socket binds"},
	{Path: "servers.*.max_header_bytes", Class: NewListenerOnlyClass, Subsystem: "listener_limits", Reason: "max header bytes is fixed when the socket binds"},
	{Path: "servers.*.client_max_body_size", Class: HotReloadClass, Subsystem: "server_limits", Reason: "handler tree reads the value per request"},
	{Path: "servers.*.server_names", Class: HotReloadClass, Subsystem: "server_names", Reason: "virtual host routing uses the current handler tree"},

	// Rate limiting.
	{Path: "rate_limit.enabled", Class: HotReloadClass, Subsystem: "rate_limit", Reason: "rate limiter store supports policy updates"},
	{Path: "rate_limit.rate", Class: HotReloadClass, Subsystem: "rate_limit", Reason: "rate limiter store supports policy updates"},
	{Path: "rate_limit.burst", Class: HotReloadClass, Subsystem: "rate_limit", Reason: "rate limiter store supports policy updates"},
	{Path: "rate_limit.max_conns", Class: NewListenerOnlyClass, Subsystem: "rate_limit", Reason: "connection cap is enforced per listener"},

	// Location settings.
	{Path: "servers.*.locations.*.proxy_pass", Class: HotReloadClass, Subsystem: "proxy_pass", Reason: "handler tree is rebuilt on reload"},
	{Path: "servers.*.locations.*.root", Class: HotReloadClass, Subsystem: "root", Reason: "handler tree is rebuilt on reload"},
	{Path: "servers.*.locations.*.cache", Class: HotReloadClass, Subsystem: "cache", Reason: "handler tree is rebuilt on reload"},
	{Path: "servers.*.locations.*.rate_limit", Class: HotReloadClass, Subsystem: "rate_limit", Reason: "rate limiter policy is updated on reload"},
	{Path: "servers.*.locations.*.auth", Class: HotReloadClass, Subsystem: "auth", Reason: "auth handlers are rebuilt on reload"},
	{Path: "servers.*.locations.*.waf", Class: HotReloadClass, Subsystem: "waf", Reason: "WAF policy is rebuilt on reload"},
	{Path: "servers.*.locations.*.plugins", Class: HotReloadClass, Subsystem: "plugins", Reason: "plugin chain is rebuilt on reload"},

	// Upstream settings.
	{Path: "upstreams.*.name", Class: HotReloadClass, Subsystem: "upstream", Reason: "upstream registry supports staged replacement"},
	{Path: "upstreams.*.strategy", Class: HotReloadClass, Subsystem: "upstream", Reason: "upstream registry supports staged replacement"},
	{Path: "upstreams.*.servers", Class: HotReloadClass, Subsystem: "upstream", Reason: "upstream registry supports staged replacement"},
	{Path: "upstreams.*.max_fails", Class: HotReloadClass, Subsystem: "upstream", Reason: "upstream registry supports staged replacement"},
	{Path: "upstreams.*.fail_timeout", Class: HotReloadClass, Subsystem: "upstream", Reason: "upstream registry supports staged replacement"},
	{Path: "upstreams.*.health_check", Class: HotReloadClass, Subsystem: "upstream", Reason: "upstream registry supports staged replacement"},

	// Compression.
	{Path: "compression.enabled", Class: HotReloadClass, Subsystem: "compression", Reason: "middleware chain is rebuilt on reload"},
	{Path: "compression.types", Class: HotReloadClass, Subsystem: "compression", Reason: "middleware chain is rebuilt on reload"},
	{Path: "compression.min_length", Class: HotReloadClass, Subsystem: "compression", Reason: "middleware chain is rebuilt on reload"},

	// WAF global.
	{Path: "waf.enabled", Class: HotReloadClass, Subsystem: "waf", Reason: "WAF policy is rebuilt on reload"},
	{Path: "waf.mode", Class: HotReloadClass, Subsystem: "waf", Reason: "WAF policy is rebuilt on reload"},
	{Path: "waf.crs_enabled", Class: HotReloadClass, Subsystem: "waf", Reason: "WAF policy is rebuilt on reload"},

	// Stream (L4).
	{Path: "stream.*.listen", Class: NewListenerOnlyClass, Subsystem: "stream", Reason: "L4 listen address change requires a new socket"},
	{Path: "stream.*.protocol", Class: RestartRequiredClass, Subsystem: "stream", Reason: "L4 protocol is bound at listener creation", StartupConsumed: true},
	{Path: "stream.*.proxy_pass", Class: HotReloadClass, Subsystem: "stream", Reason: "L4 routing table is reloaded"},
	{Path: "stream.*.sni_routes", Class: HotReloadClass, Subsystem: "stream", Reason: "L4 routing table is reloaded"},
}

// ByPath returns the registry entry for an exact path, or nil if none is
// registered.
func ByPath(path string) *Entry {
	for i := range Registry {
		if Registry[i].Path == path {
			return &Registry[i]
		}
	}
	return nil
}

// normalizeStreamProtocol returns the canonical protocol name for a stream
// listener, treating the empty string as the "tcp" default.
func normalizeStreamProtocol(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	if p == "" {
		return "tcp"
	}
	return p
}

// StartupFields returns all entries whose effective value is captured in the
// startup fingerprint.
func StartupFields() []Entry {
	var out []Entry
	for _, e := range Registry {
		if e.StartupConsumed {
			out = append(out, e)
		}
	}
	return out
}

// addressKeyedPaths are startup-consumed paths whose value is a map keyed by
// listen address. Existing addresses are compared one-by-one; added or removed
// addresses are ignored so that listener addition/removal does not trigger a
// false restart-required rejection.
var addressKeyedPaths = map[string]struct{}{
	"servers.*.tls":     {},
	"servers.*.http3":   {},
	"servers.*.h2c":     {},
	"stream.*.protocol": {},
}

// RestartRequired returns the first restart-required field that differs between
// the startup and candidate fingerprints. The comparison uses the effective
// (expanded) values of startup-consumed fields only.
func RestartRequired(startup, candidate Fingerprint) (string, bool) {
	for _, e := range StartupFields() {
		ov, ok1 := startup.Values[e.Path]
		nv, ok2 := candidate.Values[e.Path]
		if !ok1 || !ok2 {
			return fmt.Sprintf("%s: missing in one side", e.Path), true
		}
		if _, addressKeyed := addressKeyedPaths[e.Path]; addressKeyed {
			if reason, need := restartRequiredAddressKeyed(ov, nv, e.Path, e.Reason); need {
				return reason, true
			}
			continue
		}
		if !deepEqualValues(ov, nv) {
			return fmt.Sprintf("%s changed (%s)", e.Path, e.Reason), true
		}
	}
	return "", false
}

// restartRequiredAddressKeyed compares two address-keyed maps. Only addresses
// present in both fingerprints are compared; additions (new listeners) and
// removals are ignored.
func restartRequiredAddressKeyed(startup, candidate any, path, reason string) (string, bool) {
	om, ok1 := startup.(map[string]any)
	nm, ok2 := candidate.(map[string]any)
	if !ok1 || !ok2 {
		return fmt.Sprintf("%s: not an address-keyed map", path), true
	}
	for addr, sv := range om {
		cv, ok := nm[addr]
		if !ok {
			continue // listener removed; no longer relevant
		}
		if !deepEqualValues(sv, cv) {
			return fmt.Sprintf("%s changed for %s (%s)", path, addr, reason), true
		}
	}
	return "", false
}

// deepEqualValues compares two values produced by the fingerprint. It handles
// slices and maps explicitly so order differences in backend lists etc. are
// treated consistently.
func deepEqualValues(a, b any) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	switch av := a.(type) {
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case int:
		bv, ok := b.(int)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !deepEqualValues(av[i], bv[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, va := range av {
			vb, ok := bv[k]
			if !ok || !deepEqualValues(va, vb) {
				return false
			}
		}
		return true
	default:
		return fmt.Sprint(a) == fmt.Sprint(b)
	}
}

// FieldClass returns the class for an exact registered path. It returns
// HotReloadClass for unknown fields so the default is permissive.
func FieldClass(path string) Class {
	if e := ByPath(path); e != nil {
		return e.Class
	}
	return HotReloadClass
}

// MatchWildcard returns true if concrete matches a registry path that uses
// wildcards, e.g. "servers.0.listen" matches "servers.*.listen".
func MatchWildcard(registryPath, concretePath string) bool {
	rs := strings.Split(registryPath, ".")
	cs := strings.Split(concretePath, ".")
	if len(rs) != len(cs) {
		return false
	}
	for i := range rs {
		if rs[i] == "*" {
			continue
		}
		if rs[i] != cs[i] {
			return false
		}
	}
	return true
}

// Lookup returns the registry entry that matches concretePath, including
// wildcard entries, or nil if none matches.
func Lookup(concretePath string) *Entry {
	if e := ByPath(concretePath); e != nil {
		return e
	}
	for i := range Registry {
		if MatchWildcard(Registry[i].Path, concretePath) {
			return &Registry[i]
		}
	}
	return nil
}
