// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"jul/internal/rbac"
)

// principalNameRe restricts principal and custom role names to safe, bounded
// identifiers that are printable, non-empty, and safe for audit display.
var principalNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._@-]{0,62}$`)

// validateRBAC validates the [admin.rbac] configuration block. It is called
// from Validate whenever [admin] is enabled.
func validateRBAC(r AdminRBACConfig, legacyToken ...string) []error {
	if !r.Enabled {
		// When RBAC is disabled, allow only an empty or a well-formed config
		// (in case operators paste a block with enabled=false as a template).
		// We still reject obviously broken entries so partial configs fail loudly.
		if len(r.Principals) > 0 || len(r.Roles) > 0 {
			return validateRBACStructure(r)
		}
		return nil
	}
	return validateRBACStructure(r, legacyToken...)
}

func validateRBACStructure(r AdminRBACConfig, legacyToken ...string) []error {
	var errs []error

	// Validate default_role when set.
	if r.DefaultRole != "" {
		if !isKnownRole(r.DefaultRole, r.Roles) {
			errs = append(errs, fmt.Errorf("[admin.rbac] 'default_role' %q is not a known predefined or custom role", r.DefaultRole))
		}
	}

	// Validate custom roles.
	customRoleNames := make(map[string]bool, len(r.Roles))
	for i, role := range r.Roles {
		where := fmt.Sprintf("[admin.rbac.roles[%d]]", i)
		if role.Name == "" {
			errs = append(errs, fmt.Errorf("%s 'name' must not be empty", where))
			continue
		}
		if !principalNameRe.MatchString(role.Name) {
			errs = append(errs, fmt.Errorf("%s 'name' %q contains invalid characters; use [a-zA-Z0-9._@-] only", where, role.Name))
		}
		if rbac.IsPredefined(role.Name) {
			errs = append(errs, fmt.Errorf("%s role %q is a predefined role name and cannot be redefined", where, role.Name))
			continue
		}
		if customRoleNames[role.Name] {
			errs = append(errs, fmt.Errorf("%s duplicate custom role name %q", where, role.Name))
			continue
		}
		customRoleNames[role.Name] = true
		if len(role.Permissions) == 0 {
			errs = append(errs, fmt.Errorf("%s 'permissions' must not be empty", where))
		}
		for _, p := range role.Permissions {
			if !rbac.Known(rbac.Permission(p)) {
				errs = append(errs, fmt.Errorf("%s unknown permission %q; see the rbac.Catalog for valid values", where, p))
			}
		}
	}

	if !r.Enabled {
		// When disabled, structural checks above are sufficient.
		return errs
	}

	// Validate principals.
	principalNames := make(map[string]bool, len(r.Principals))
	now := time.Now()
	hasAdmin := false
	if len(legacyToken) > 0 && legacyToken[0] != "" {
		role := r.DefaultRole
		if role == "" {
			role = rbac.RoleAdmin
		}
		hasAdmin = hasAdminPermission(role, r.Roles)
	}

	for i, p := range r.Principals {
		where := fmt.Sprintf("[admin.rbac.principals[%d]]", i)
		if p.Name == "" {
			errs = append(errs, fmt.Errorf("%s 'name' must not be empty", where))
			continue
		}
		if !principalNameRe.MatchString(p.Name) {
			errs = append(errs, fmt.Errorf("%s 'name' %q contains invalid characters", where, p.Name))
		}
		if principalNames[p.Name] {
			errs = append(errs, fmt.Errorf("%s duplicate principal name %q", where, p.Name))
			continue
		}
		if p.Name == "shared" {
			errs = append(errs, fmt.Errorf("%s principal name %q is reserved for the legacy compatibility principal", where, p.Name))
		}
		principalNames[p.Name] = true

		if p.Role == "" {
			errs = append(errs, fmt.Errorf("%s 'role' must not be empty", where))
		} else if !isKnownRole(p.Role, r.Roles) {
			errs = append(errs, fmt.Errorf("%s 'role' %q is not a known predefined or custom role", where, p.Role))
		}

		if p.Token == "" {
			errs = append(errs, fmt.Errorf("%s 'token' must not be empty; use ${env:VAR} to source from the environment", where))
		} else if !isSecretRef(p.Token) {
			// Token is a literal value (not a secret reference). Warn about
			// insufficient length even at validation time.
			if err := rbac.ValidateToken(p.Token); err != nil {
				errs = append(errs, fmt.Errorf("%s %w", where, err))
			}
		}

		if !p.ExpiresAt.IsZero() && p.ExpiresAt.Before(now) {
			// Expired principal is still valid config, but flag it loudly so
			// operators don't accidentally lock themselves out.
			errs = append(errs, fmt.Errorf("%s 'expires_at' is in the past; this principal is already inactive", where))
		}

		// Track whether any non-disabled, non-expired principal has admin:manage.
		if !p.Disabled && (p.ExpiresAt.IsZero() || p.ExpiresAt.After(now)) {
			if hasAdminPermission(p.Role, r.Roles) {
				hasAdmin = true
			}
		}
	}

	if r.Enabled && !hasAdmin {
		errs = append(errs, fmt.Errorf("[admin.rbac] no enabled, non-expired principal has the 'admin:manage' permission; at least one admin-capable principal is required to prevent lockout"))
	}

	return errs
}

// isKnownRole reports whether name is either a predefined or a custom role
// defined in the given custom roles slice.
func isKnownRole(name string, customs []AdminRole) bool {
	if rbac.IsPredefined(name) {
		return true
	}
	for _, r := range customs {
		if r.Name == name {
			return true
		}
	}
	return false
}

// hasAdminPermission reports whether the role (predefined or custom) includes
// the admin:manage permission.
func hasAdminPermission(roleName string, customs []AdminRole) bool {
	// Check predefined roles.
	if rbac.IsPredefined(roleName) {
		perms, _ := rbac.PermissionsForPredefined(roleName)
		id := rbac.Identity{Permissions: perms}
		return id.Has(rbac.AdminManage)
	}
	// Check custom roles.
	for _, r := range customs {
		if r.Name == roleName {
			for _, p := range r.Permissions {
				if rbac.Matches(rbac.Permission(p), rbac.AdminManage) {
					return true
				}
			}
		}
	}
	return false
}

// isSecretRef returns true when tok looks like a secret reference that will be
// resolved at startup, e.g. "${env:MY_TOKEN}" or "${file:/run/secrets/token}".
func isSecretRef(tok string) bool {
	return strings.HasPrefix(tok, "${") && strings.Contains(tok, ":") && strings.HasSuffix(tok, "}")
}
