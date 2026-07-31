// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/rbac"
)

// rbacAdmin builds an [admin] config with the given RBAC block, keeping all
// other admin fields fixed so a diff isolates RBAC changes.
func rbacAdmin(r config.AdminRBACConfig, legacyToken string) config.AdminConfig {
	return config.AdminConfig{
		Enabled: true,
		Listen:  "127.0.0.1:9090",
		Token:   legacyToken,
		RBAC:    r,
	}
}

func cfgWithAdmin(a config.AdminConfig) *config.Config {
	return &config.Config{Admin: a}
}

func TestDiffRBACEnableWarns(t *testing.T) {
	before := cfgWithAdmin(rbacAdmin(config.AdminRBACConfig{Enabled: false}, ""))
	after := cfgWithAdmin(rbacAdmin(config.AdminRBACConfig{
		Enabled: true,
		Principals: []config.AdminPrincipal{
			{Name: "root", Role: rbac.RoleAdmin, Token: "SECRET-root-token-32-chars-pad---"},
		},
	}, ""))
	d := diffConfigs(before, after)
	if !diffHas(d, "Enable admin RBAC") {
		t.Errorf("expected RBAC-enable entry, got %+v", d.Modifications)
	}
	if !warnHas(d, "Enabling RBAC replaces shared-token access") {
		t.Errorf("expected RBAC-enable warning, got %+v", d.Warnings)
	}
}

func TestDiffRBACRoleAndPrincipalChanges(t *testing.T) {
	before := cfgWithAdmin(rbacAdmin(config.AdminRBACConfig{
		Enabled: true,
		Roles:   []config.AdminRole{{Name: "writer", Permissions: []string{"config:apply", "config:write"}}},
		Principals: []config.AdminPrincipal{
			{Name: "root", Role: rbac.RoleAdmin, Token: "SECRET-root-token-32-chars-pad---"},
			{Name: "alice", Role: "writer", Token: "SECRET-alice-token-32-chars-pad--"},
			{Name: "bob", Role: rbac.RoleViewer, Token: "SECRET-bob-token-32-chars-pad----"},
		},
	}, ""))
	after := cfgWithAdmin(rbacAdmin(config.AdminRBACConfig{
		Enabled: true,
		Roles: []config.AdminRole{
			{Name: "writer", Permissions: []string{"config:apply"}}, // permission removed
			{Name: "reader", Permissions: []string{"status:read"}},  // role added
		},
		Principals: []config.AdminPrincipal{
			{Name: "root", Role: rbac.RoleAdmin, Token: "SECRET-root-token-32-chars-pad---"},
			{Name: "alice", Role: rbac.RoleOperator, Token: "SECRET-alice-token-ROTATED-pad---"}, // role change + rotation
			// bob removed
			{Name: "carol", Role: "reader", Token: "SECRET-carol-token-32-chars-pad--", Disabled: true}, // added disabled
		},
	}, ""))
	d := diffConfigs(before, after)

	for _, want := range []string{
		"Change permissions for RBAC role writer",
		"Add RBAC role reader",
		"Change role for RBAC principal alice",
		"Rotate credential for RBAC principal alice",
		"Remove RBAC principal bob",
		"Add RBAC principal carol",
	} {
		if !diffHas(d, want) {
			t.Errorf("missing diff entry %q in %+v", want, d)
		}
	}
}

func TestDiffRBACExpiryAndDisableTransitions(t *testing.T) {
	exp := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	before := cfgWithAdmin(rbacAdmin(config.AdminRBACConfig{
		Enabled: true,
		Principals: []config.AdminPrincipal{
			{Name: "root", Role: rbac.RoleAdmin, Token: "SECRET-root-token-32-chars-pad---"},
			{Name: "dana", Role: rbac.RoleViewer, Token: "SECRET-dana-token-32-chars-pad---"},
		},
	}, ""))
	after := cfgWithAdmin(rbacAdmin(config.AdminRBACConfig{
		Enabled: true,
		Principals: []config.AdminPrincipal{
			{Name: "root", Role: rbac.RoleAdmin, Token: "SECRET-root-token-32-chars-pad---"},
			{Name: "dana", Role: rbac.RoleViewer, Token: "SECRET-dana-token-32-chars-pad---", Disabled: true, ExpiresAt: exp},
		},
	}, ""))
	d := diffConfigs(before, after)
	if !diffHas(d, "Disable RBAC principal dana") {
		t.Errorf("expected disable entry, got %+v", d.Modifications)
	}
	if !diffHas(d, "Change expiry for RBAC principal dana") {
		t.Errorf("expected expiry entry, got %+v", d.Modifications)
	}
}

func TestDiffRBACLegacyTokenRetainedWarning(t *testing.T) {
	before := cfgWithAdmin(rbacAdmin(config.AdminRBACConfig{Enabled: false}, "SECRET-shared-token-32-chars-pad-"))
	after := cfgWithAdmin(rbacAdmin(config.AdminRBACConfig{
		Enabled: true,
		Principals: []config.AdminPrincipal{
			{Name: "root", Role: rbac.RoleAdmin, Token: "SECRET-root-token-32-chars-pad---"},
		},
	}, "SECRET-shared-token-32-chars-pad-"))
	d := diffConfigs(before, after)
	if !warnHas(d, "legacy shared admin token is still configured") {
		t.Errorf("expected legacy-token retention warning, got %+v", d.Warnings)
	}
}

func TestDiffRBACLastAdminRemovalWarning(t *testing.T) {
	before := cfgWithAdmin(rbacAdmin(config.AdminRBACConfig{
		Enabled: true,
		Principals: []config.AdminPrincipal{
			{Name: "root", Role: rbac.RoleAdmin, Token: "SECRET-root-token-32-chars-pad---"},
			{Name: "vic", Role: rbac.RoleViewer, Token: "SECRET-vic-token-32-chars-pad----"},
		},
	}, ""))
	// Demote the only admin to viewer, leaving no admin-capable principal.
	after := cfgWithAdmin(rbacAdmin(config.AdminRBACConfig{
		Enabled: true,
		Principals: []config.AdminPrincipal{
			{Name: "root", Role: rbac.RoleViewer, Token: "SECRET-root-token-32-chars-pad---"},
			{Name: "vic", Role: rbac.RoleViewer, Token: "SECRET-vic-token-32-chars-pad----"},
		},
	}, ""))
	d := diffConfigs(before, after)
	if !warnHas(d, "no enabled, admin-capable principal") {
		t.Errorf("expected last-admin removal warning, got %+v", d.Warnings)
	}
}

// TestDiffRBACNeverLeaksSecrets is the security guard: no token value or hash
// may ever appear anywhere in the serialized diff, regardless of the change.
func TestDiffRBACNeverLeaksSecrets(t *testing.T) {
	before := cfgWithAdmin(rbacAdmin(config.AdminRBACConfig{
		Enabled: true,
		Principals: []config.AdminPrincipal{
			{Name: "root", Role: rbac.RoleAdmin, Token: "SECRET-root-token-32-chars-pad---"},
			{Name: "alice", Role: rbac.RoleViewer, Token: "SECRET-alice-token-32-chars-pad--"},
		},
	}, "SECRET-shared-token-32-chars-pad-"))
	after := cfgWithAdmin(rbacAdmin(config.AdminRBACConfig{
		Enabled: true,
		Principals: []config.AdminPrincipal{
			{Name: "root", Role: rbac.RoleAdmin, Token: "SECRET-root-token-ROTATED-pad----"},
			{Name: "alice", Role: rbac.RoleOperator, Token: "SECRET-alice-token-ROTATED-pad---"},
		},
	}, "SECRET-shared-token-ROTATED-pad--"))
	d := diffConfigs(before, after)
	blob, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal diff: %v", err)
	}
	body := string(blob)
	for _, secret := range []string{
		"SECRET-root-token", "SECRET-alice-token", "SECRET-shared-token",
		rbac.TokenDigest("SECRET-root-token-ROTATED-pad----"),
	} {
		if strings.Contains(body, secret) {
			t.Errorf("diff leaked secret material %q in %s", secret, body)
		}
	}
}

func TestProjectRBAC(t *testing.T) {
	c := cfgWithAdmin(rbacAdmin(config.AdminRBACConfig{
		Enabled: true,
		Roles:   []config.AdminRole{{Name: "writer", Permissions: []string{"config:apply"}}},
		Principals: []config.AdminPrincipal{
			{Name: "root", Role: rbac.RoleAdmin, Token: "SECRET-root-token-32-chars-pad---"},
			{Name: "alice", Role: "writer", Token: "SECRET-alice-token-32-chars-pad--"},
		},
	}, "SECRET-shared-token-32-chars-pad-"))
	got := projectRBAC(c)
	if !got.Enabled || got.PrincipalCount != 2 || got.RoleCount != 1 || !got.LegacyTokenActive {
		t.Fatalf("unexpected projection: %+v", got)
	}

	// The full SecurityProjection must embed the RBAC status and never a secret.
	sp := projectSecurity(c, false)
	blob, err := json.Marshal(sp)
	if err != nil {
		t.Fatalf("marshal security projection: %v", err)
	}
	if strings.Contains(string(blob), "SECRET-") {
		t.Errorf("security projection leaked a token: %s", blob)
	}
	if !sp.RBAC.Enabled {
		t.Error("security projection missing embedded RBAC status")
	}
}

func TestProjectRBACDisabled(t *testing.T) {
	c := cfgWithAdmin(config.AdminConfig{Enabled: true, Listen: "127.0.0.1:9090"})
	got := projectRBAC(c)
	if got.Enabled || got.PrincipalCount != 0 || got.RoleCount != 0 || got.LegacyTokenActive {
		t.Fatalf("expected empty RBAC status, got %+v", got)
	}
}
