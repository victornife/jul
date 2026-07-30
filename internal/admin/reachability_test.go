// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/rbac"
)

func TestRoleGrantsAdminManageWildcardsAndBuiltins(t *testing.T) {
	cfg := config.AdminRBACConfig{Roles: []config.AdminRole{
		{Name: "resource-admin", Permissions: []string{"admin:*"}},
		{Name: "global", Permissions: []string{"*"}},
	}}
	for _, role := range []string{"resource-admin", "global", rbac.RoleAdmin} {
		if !roleGrantsAdminManage(cfg, role) {
			t.Errorf("role %q should grant admin:manage", role)
		}
	}
	if roleGrantsAdminManage(cfg, rbac.RoleOperator) {
		t.Error("operator must not grant admin:manage")
	}
}

func TestRBACSharedIdentityDefaultRoleDemotion(t *testing.T) {
	const token = "shared-token-32-characters-long----"
	before := config.AdminConfig{Token: token, RBAC: config.AdminRBACConfig{Enabled: true, DefaultRole: rbac.RoleAdmin}}
	after := before
	after.RBAC.DefaultRole = rbac.RoleOperator
	id := rbac.Identity{Principal: "shared", TokenID: rbac.TokenDigest(token)[:12], TokenDigest: rbac.TokenDigest(token), Legacy: true, Permissions: []rbac.Permission{rbac.Wildcard}}
	if changes := rbacCredentialLockoutChanges(before, after, id); len(changes) == 0 {
		t.Fatal("shared/default-role demotion was not detected")
	}
}

func TestRBACBuiltInAdminPreservesAccess(t *testing.T) {
	const token = "named-admin-token-32-characters---"
	before := config.AdminConfig{RBAC: config.AdminRBACConfig{
		Enabled:    true,
		Roles:      []config.AdminRole{{Name: "custom-admin", Permissions: []string{"admin:*"}}},
		Principals: []config.AdminPrincipal{{Name: "alice", Role: "custom-admin", Token: token}},
	}}
	after := before
	after.RBAC.Principals = []config.AdminPrincipal{{Name: "alice", Role: rbac.RoleAdmin, Token: token}}
	id := rbac.Identity{Principal: "alice", TokenID: rbac.TokenDigest(token)[:12], TokenDigest: rbac.TokenDigest(token), Permissions: []rbac.Permission{rbac.Permission("admin:*")}}
	if changes := rbacCredentialLockoutChanges(before, after, id); len(changes) != 0 {
		t.Fatalf("built-in admin transition was falsely flagged: %v", changes)
	}
}

func TestRBACPolicyContinuityRejectsExpiredCurrentCredential(t *testing.T) {
	const current = "current-admin-token-32-characters-"
	const backup = "backup-admin-token-32-characters--"
	before := config.AdminConfig{RBAC: config.AdminRBACConfig{Enabled: true, Principals: []config.AdminPrincipal{
		{Name: "alice", Role: rbac.RoleAdmin, Token: current},
		{Name: "backup", Role: rbac.RoleAdmin, Token: backup},
	}}}
	after := before
	after.RBAC.Principals = append([]config.AdminPrincipal(nil), before.RBAC.Principals...)
	after.RBAC.Principals[0].ExpiresAt = time.Now().Add(-time.Second)
	id := rbac.Identity{Principal: "alice", TokenID: rbac.TokenDigest(current)[:12], TokenDigest: rbac.TokenDigest(current), Permissions: []rbac.Permission{rbac.Wildcard}}
	if changes := rbacCredentialLockoutChanges(before, after, id); len(changes) == 0 {
		t.Fatal("expired current credential was not detected")
	}
}
