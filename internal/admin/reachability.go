// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"fmt"
	"time"

	"jul/internal/config"
	"jul/internal/rbac"
)

// adminReachabilityError is returned by rollbackToSnapshot when a rollback
// would change how the current operator reaches the admin console and no
// confirm_admin=true was supplied. It carries the operator-facing change list
// so the HTTP handler can render the same adminGuardResponse used by the raw,
// settings, and patch endpoints (finding 9) rather than a plain error string.
type adminReachabilityError struct {
	changes []string
}

func (e *adminReachabilityError) Error() string {
	return "this change affects how you reach the admin console; re-apply with confirmation to proceed"
}

// reachabilityChanges is the single self-lockout / reachability-confirmation
// predicate shared by every mutation endpoint (raw apply, structured patch,
// settings, and rollback). It reports the operator-facing reasons a transition
// from cur to next would change how the CURRENT operator reaches the admin
// console, so the caller can require an explicit confirmation before proceeding.
//
// It composes two layers:
//
//   - adminLockoutChanges: transport-level reachability (disabling admin,
//     moving its listen address, rotating the legacy token, disabling the web
//     console). These apply regardless of who is calling.
//
//   - rbacCredentialLockoutChanges: identity-level reachability for the
//     authenticated RBAC operator (removing/disabling/expiring the current
//     principal, rotating its token, changing its role so it loses
//     admin:manage, or switching authentication mode between RBAC and legacy).
//     Policy validation guarantees SOME admin-capable principal survives, but
//     not that THIS operator's session does; that gap is what this layer
//     closes (finding 8).
//
// It returns nil when the transition preserves the current operator's access.
func (s *Server) reachabilityChanges(cur, next *config.Config, id rbac.Identity) []string {
	if cur == nil || next == nil {
		return nil
	}
	changes := adminLockoutChanges(cur.Admin, next.Admin)
	changes = append(changes, rbacCredentialLockoutChanges(cur.Admin, next.Admin, id)...)
	return changes
}

// rbacCredentialLockoutChanges reports identity-level reachability changes for
// the currently authenticated operator. It only flags changes that would
// invalidate THIS operator's ability to keep administering the service; changes
// to other principals are out of scope for self-lockout.
//
// A legacy (non-RBAC) session is covered by adminLockoutChanges' token check,
// so here we focus on RBAC-authenticated sessions. When the current identity is
// legacy but the candidate switches into RBAC (or vice-versa), the mode switch
// itself is flagged because the current session's credential type stops being
// accepted.
func rbacCredentialLockoutChanges(prev, next config.AdminConfig, id rbac.Identity) []string {
	prevRBAC := prev.RBAC.Enabled
	nextRBAC := next.RBAC.Enabled

	// Authentication mode switch: any transition between RBAC and legacy/open
	// invalidates the current credential type. Flag it so the operator confirms.
	if prevRBAC != nextRBAC {
		if nextRBAC {
			return []string{"authentication would switch to RBAC (your current credential would need to be a valid RBAC principal)"}
		}
		return []string{"authentication would switch away from RBAC to legacy/open (your current RBAC session would need to re-authenticate)"}
	}

	// Not an RBAC session on either side: the legacy token change is already
	// covered by adminLockoutChanges. Nothing identity-specific to add.
	if !nextRBAC {
		return nil
	}

	// RBAC → RBAC: verify the CURRENT operator's principal survives the change.
	// An anonymous/legacy identity with RBAC enabled cannot be self-locked-out
	// by a principal edit (it is not a principal), so there is nothing to flag.
	if id.Legacy || id.Principal == "" {
		return nil
	}

	before, hadBefore := findPrincipal(prev.RBAC.Principals, id.Principal)
	after, hasAfter := findPrincipal(next.RBAC.Principals, id.Principal)

	if !hadBefore {
		// The current session is not tied to a named principal we can track
		// (e.g. bootstrap/default identity); do not guess.
		return nil
	}
	if !hasAfter {
		return []string{fmt.Sprintf("your admin principal %q would be removed", id.Principal)}
	}

	var changes []string
	if after.Disabled && !before.Disabled {
		changes = append(changes, fmt.Sprintf("your admin principal %q would be disabled", id.Principal))
	}
	if after.Token != before.Token {
		changes = append(changes, fmt.Sprintf("the token for your admin principal %q would change (your current session would need to re-authenticate)", id.Principal))
	}
	if principalExpired(after) && !principalExpired(before) {
		changes = append(changes, fmt.Sprintf("your admin principal %q would be expired", id.Principal))
	}
	if after.Role != before.Role {
		// A role change only matters for lockout if it drops admin:manage, the
		// permission required to keep administering the service.
		if !roleGrantsAdminManage(next.RBAC, after.Role) {
			changes = append(changes, fmt.Sprintf("your admin principal %q would change to role %q, which cannot manage the admin subsystem", id.Principal, after.Role))
		}
	} else if !roleGrantsAdminManage(next.RBAC, after.Role) && roleGrantsAdminManage(prev.RBAC, before.Role) {
		// Same role name but the role's permission set was edited to remove
		// admin:manage.
		changes = append(changes, fmt.Sprintf("your admin role %q would lose the permission to manage the admin subsystem", after.Role))
	}
	return changes
}

// findPrincipal returns the principal with the given name (case-sensitive) and
// whether it was found.
func findPrincipal(principals []config.AdminPrincipal, name string) (config.AdminPrincipal, bool) {
	for _, p := range principals {
		if p.Name == name {
			return p, true
		}
	}
	return config.AdminPrincipal{}, false
}

// principalExpired reports whether a principal's token has expired relative to
// the current time. A zero ExpiresAt means the token never expires.
func principalExpired(p config.AdminPrincipal) bool {
	if p.ExpiresAt.IsZero() {
		return false
	}
	return !p.ExpiresAt.After(time.Now())
}

// roleGrantsAdminManage reports whether the named role in the given RBAC config
// grants the admin:manage permission (directly or via wildcard).
func roleGrantsAdminManage(cfg config.AdminRBACConfig, roleName string) bool {
	for _, role := range cfg.Roles {
		if role.Name != roleName {
			continue
		}
		for _, perm := range role.Permissions {
			if perm == string(rbac.AdminManage) || perm == "*" {
				return true
			}
		}
		return false
	}
	return false
}
