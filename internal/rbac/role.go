// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package rbac

import "fmt"

// predefined role names — operators may use these in principals but cannot
// redefine them.
const (
	RoleViewer   = "viewer"
	RoleOperator = "operator"
	RoleAdmin    = "admin"
	RoleAuditor  = "auditor"
)

// predefinedRoles maps role name → resolved permission set. All values are
// compile-time constants; the map must never be mutated at runtime.
var predefinedRoles = map[string][]Permission{
	RoleViewer: {
		StatusRead,
		MetricsRead,
		ConfigRead,
		HistoryRead,
		ObservabilityRead,
		AuditRead,
	},
	RoleOperator: {
		StatusRead,
		MetricsRead,
		ConfigRead,
		ConfigWrite,
		ConfigApply,
		HistoryRead,
		HistoryRollback,
		PluginsUpload,
		ObservabilityRead,
		AuditRead,
		AuditExport,
		CachePurge,
		ReloadTrigger,
	},
	RoleAdmin: {
		Wildcard, // admin:* → all permissions
	},
	RoleAuditor: {
		StatusRead,
		ObservabilityRead,
		AuditRead,
		AuditExport,
	},
}

// RoleHas reports whether the named predefined role (or the wildcard admin
// role) is granted permission p. It is a convenience for authorization checks
// that need to know the role-level default without building a full Policy.
func RoleHas(role string, p Permission) bool {
	perms, ok := predefinedRoles[role]
	if !ok {
		return false
	}
	for _, g := range perms {
		if Matches(g, p) {
			return true
		}
	}
	return false
}

// IsPredefined reports whether name is a built-in role that cannot be
// redefined by operators.
func IsPredefined(name string) bool {
	_, ok := predefinedRoles[name]
	return ok
}

// PermissionsForPredefined returns the permission set for a built-in role, or
// an error if name is not predefined.
func PermissionsForPredefined(name string) ([]Permission, error) {
	ps, ok := predefinedRoles[name]
	if !ok {
		return nil, fmt.Errorf("rbac: unknown predefined role %q", name)
	}
	return ps, nil
}

// PredefinedNames returns the names of all built-in roles in a stable order.
func PredefinedNames() []string {
	return []string{RoleViewer, RoleOperator, RoleAdmin, RoleAuditor}
}

// resolveCustom expands a slice of raw permission strings (which may include
// wildcards) into a deduplicated []Permission slice, validating each entry.
// Returns an error for any unknown permission string.
func resolveCustom(raw []string) ([]Permission, error) {
	seen := make(map[Permission]bool, len(raw))
	out := make([]Permission, 0, len(raw))
	for _, s := range raw {
		p := Permission(s)
		if !Known(p) {
			return nil, fmt.Errorf("rbac: unknown permission %q", s)
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
}
