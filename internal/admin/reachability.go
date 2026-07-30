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

	// RBAC -> RBAC: evaluate the current safe token ID against the effective
	// configs. This covers named principals, predefined and custom roles,
	// resource/global wildcards, and the synthetic shared identity.
	if id.TokenID == "" {
		return nil
	}
	beforeCredential, hadBefore := effectiveCredential(prev, id)
	afterCredential, hasAfter := effectiveCredential(next, id)
	if !hadBefore {
		return nil
	}
	if !hasAfter {
		return []string{fmt.Sprintf("your admin credential for principal %q would no longer be accepted", beforeCredential.name)}
	}
	if beforeCredential.active && !afterCredential.active {
		return []string{fmt.Sprintf("your admin principal %q would be disabled or expired", afterCredential.name)}
	}
	if beforeCredential.admin && !afterCredential.admin {
		return []string{fmt.Sprintf("your admin principal %q would lose permission to manage the admin subsystem", afterCredential.name)}
	}
	return nil
}

type credentialState struct {
	name   string
	active bool
	admin  bool
}

func effectiveCredential(a config.AdminConfig, id rbac.Identity) (credentialState, bool) {
	if a.Token != "" && credentialMatches(a.Token, id) {
		role := a.RBAC.DefaultRole
		if role == "" {
			role = rbac.RoleAdmin
		}
		return credentialState{name: "shared", active: true, admin: roleGrantsAdminManage(a.RBAC, role)}, true
	}
	for _, principal := range a.RBAC.Principals {
		if !credentialMatches(principal.Token, id) {
			continue
		}
		return credentialState{
			name:   principal.Name,
			active: !principal.Disabled && !principalExpired(principal),
			admin:  roleGrantsAdminManage(a.RBAC, principal.Role),
		}, true
	}
	return credentialState{}, false
}

func credentialMatches(token string, id rbac.Identity) bool {
	return id.TokenDigest != "" && rbac.TokenDigest(token) == id.TokenDigest
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
			if rbac.Matches(rbac.Permission(perm), rbac.AdminManage) {
				return true
			}
		}
		return false
	}
	return rbac.RoleHas(roleName, rbac.AdminManage)
}
