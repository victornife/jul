// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package rbac implements the named-principal, role-based access-control layer
// for the Jul.IA admin API (Phase 3). It is opt-in; when [admin.rbac] is
// disabled the server falls back to the legacy single-token path.
package rbac

// Permission is a fine-grained action string used in role definitions and
// route-level authorization checks. The catalog below is the single source of
// truth for validation and Console display; unknown permissions fail config
// validation, and no permission is inferred from endpoint names.
type Permission string

const (
	// StatusRead allows reading runtime status (health, overview, feature rows).
	StatusRead Permission = "status:read"
	// MetricsRead allows scraping the Prometheus /metrics endpoint.
	MetricsRead Permission = "metrics:read"
	// ConfigRead allows reading structured, secret-free configuration projections.
	ConfigRead Permission = "config:read"
	// ConfigRaw allows reading the complete raw TOML configuration, which may
	// contain literal bearer tokens and other secrets. Reserved for admin roles.
	ConfigRaw Permission = "config:raw"
	// ConfigWrite allows writing/saving the configuration (without triggering a reload).
	ConfigWrite Permission = "config:write"
	// ConfigApply allows applying/reloading or staging a configuration change.
	ConfigApply Permission = "config:apply"
	// HistoryRead allows listing configuration history metadata.
	HistoryRead Permission = "history:read"
	// HistoryReadRaw allows reading the raw TOML body of a historical snapshot.
	HistoryReadRaw Permission = "history:raw"
	// HistoryRollback allows rolling back to a previous configuration snapshot.
	HistoryRollback Permission = "history:rollback"
	// PluginsUpload allows uploading WASM plugin modules via the admin console.
	PluginsUpload Permission = "plugins:upload"
	// ObservabilityRead allows reading access-log, tracing, and observability configuration.
	ObservabilityRead Permission = "observability:read"
	// AuditRead allows listing and reading the in-memory audit ring.
	AuditRead Permission = "audit:read"
	// AuditExport allows exporting the durable audit sink.
	AuditExport Permission = "audit:export"
	// CachePurge allows issuing cache purge operations.
	CachePurge Permission = "cache:purge"
	// ReloadTrigger allows manually triggering a configuration reload.
	ReloadTrigger Permission = "reload:trigger"
	// AdminManage allows managing admin-level settings (rate limits, RBAC).
	AdminManage Permission = "admin:manage"
)

// Wildcard is the special permission value that matches every permission.
const Wildcard = "*"

// catalog is the ordered set of all concrete (non-wildcard) permissions.
// It is used for validation and Console display; unknown permission strings
// fail config validation.
var catalog = []Permission{
	StatusRead,
	MetricsRead,
	ConfigRead,
	ConfigRaw,
	ConfigWrite,
	ConfigApply,
	HistoryRead,
	HistoryReadRaw,
	HistoryRollback,
	PluginsUpload,
	ObservabilityRead,
	AuditRead,
	AuditExport,
	CachePurge,
	ReloadTrigger,
	AdminManage,
}

// Catalog returns a copy of all known concrete permissions. The order is
// stable and matches the declaration order above.
func Catalog() []Permission {
	cp := make([]Permission, len(catalog))
	copy(cp, catalog)
	return cp
}

// Known reports whether p is a recognized permission value.
// The wildcard string "*" and resource-wildcard "<resource>:*" patterns are
// also accepted.
func Known(p Permission) bool {
	if p == Wildcard {
		return true
	}
	s := string(p)
	// Accept <resource>:* wildcards where <resource> is non-empty.
	if len(s) > 2 && s[len(s)-2:] == ":*" {
		prefix := s[:len(s)-2]
		for _, c := range catalog {
			cs := string(c)
			if len(cs) > len(prefix)+1 && cs[:len(prefix)+1] == prefix+":" {
				return true
			}
		}
		return false
	}
	for _, c := range catalog {
		if c == p {
			return true
		}
	}
	return false
}

// Matches reports whether the granted permission g covers the required permission r.
// "*" covers everything; "<resource>:*" covers all permissions in that resource group.
func Matches(g, r Permission) bool {
	if g == Wildcard {
		return true
	}
	if g == r {
		return true
	}
	gs := string(g)
	rs := string(r)
	// resource:* matches resource:anything
	if len(gs) > 2 && gs[len(gs)-2:] == ":*" {
		prefix := gs[:len(gs)-2]
		if len(rs) > len(prefix)+1 && rs[:len(prefix)+1] == prefix+":" {
			return true
		}
	}
	return false
}
