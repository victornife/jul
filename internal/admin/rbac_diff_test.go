// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/lifecycle"
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

// findRBACEnableEntry returns the single [admin.rbac].enabled modification entry.
func findRBACEnableEntry(t *testing.T, d ConfigDiff) DiffEntry {
	t.Helper()
	for _, e := range d.Modifications {
		if e.Kind == "rbac" && strings.Contains(e.Detail, "admin RBAC") {
			return e
		}
	}
	t.Fatalf("no RBAC enable/disable entry in %+v", d.Modifications)
	return DiffEntry{}
}

// findRoleChange returns the permission-change modification entry for a role.
func findRoleChange(t *testing.T, d ConfigDiff, name string) DiffEntry {
	t.Helper()
	for _, e := range d.Modifications {
		if e.Kind == "rbac_role" && e.Name == name {
			return e
		}
	}
	t.Fatalf("no permission-change entry for role %q in %+v", name, d.Modifications)
	return DiffEntry{}
}

// TestDiffRBACEnableIsHotReload proves the RBAC enable diff entry is classified
// hot_reload and never claims a restart — matching the authoritative lifecycle
// registry (admin.rbac.enabled = HotReloadClass). The authentication mode is
// rebuilt and atomically installed on a successful reload, so telling an
// operator to restart would be wrong (R-02).
func TestDiffRBACEnableIsHotReload(t *testing.T) {
	before := cfgWithAdmin(rbacAdmin(config.AdminRBACConfig{Enabled: false}, ""))
	after := cfgWithAdmin(rbacAdmin(config.AdminRBACConfig{
		Enabled: true,
		Principals: []config.AdminPrincipal{
			{Name: "root", Role: rbac.RoleAdmin, Token: "SECRET-root-token-32-chars-pad---"},
		},
	}, ""))
	d := diffConfigs(before, after)

	e := findRBACEnableEntry(t, d)
	if e.LifecycleClass != lifecycle.HotReloadClass.String() {
		t.Errorf("RBAC enable lifecycle_class = %q, want %q", e.LifecycleClass, lifecycle.HotReloadClass.String())
	}
	if strings.Contains(strings.ToLower(e.Detail), "restart") {
		t.Errorf("RBAC enable detail must not mention a restart, got %q", e.Detail)
	}
	// The diff must never tell the operator to restart for this change.
	if warnHas(d, "restart") {
		t.Errorf("RBAC enable produced a restart warning: %+v", d.Warnings)
	}
	// Consistency with the authoritative lifecycle registry.
	if got, err := lifecycle.ClassOf("admin.rbac.enabled"); err != nil || got != lifecycle.HotReloadClass {
		t.Errorf("registry classifies admin.rbac.enabled as %v (err=%v), want HotReloadClass", got, err)
	}
}

// TestDiffRBACDisableIsHotReload proves the disable direction is equally hot and
// restart-free.
func TestDiffRBACDisableIsHotReload(t *testing.T) {
	before := cfgWithAdmin(rbacAdmin(config.AdminRBACConfig{
		Enabled: true,
		Principals: []config.AdminPrincipal{
			{Name: "root", Role: rbac.RoleAdmin, Token: "SECRET-root-token-32-chars-pad---"},
		},
	}, "SECRET-shared-token-32-chars-pad-"))
	after := cfgWithAdmin(rbacAdmin(config.AdminRBACConfig{Enabled: false}, "SECRET-shared-token-32-chars-pad-"))
	d := diffConfigs(before, after)

	e := findRBACEnableEntry(t, d)
	if e.LifecycleClass != lifecycle.HotReloadClass.String() {
		t.Errorf("RBAC disable lifecycle_class = %q, want hot_reload", e.LifecycleClass)
	}
	if strings.Contains(strings.ToLower(e.Detail), "restart") {
		t.Errorf("RBAC disable detail must not mention a restart, got %q", e.Detail)
	}
}

// TestDiffRBACPermissionSwapShowsDelta proves a same-count permission swap (the
// N-01 regression) surfaces the exact privilege change rather than "1 → 1".
func TestDiffRBACPermissionSwapShowsDelta(t *testing.T) {
	before := cfgWithAdmin(rbacAdmin(config.AdminRBACConfig{
		Enabled: true,
		Roles:   []config.AdminRole{{Name: "ops", Permissions: []string{"status:read"}}},
		Principals: []config.AdminPrincipal{
			{Name: "root", Role: rbac.RoleAdmin, Token: "SECRET-root-token-32-chars-pad---"},
		},
	}, ""))
	after := cfgWithAdmin(rbacAdmin(config.AdminRBACConfig{
		Enabled: true,
		Roles:   []config.AdminRole{{Name: "ops", Permissions: []string{"admin:manage"}}},
		Principals: []config.AdminPrincipal{
			{Name: "root", Role: rbac.RoleAdmin, Token: "SECRET-root-token-32-chars-pad---"},
		},
	}, ""))
	d := diffConfigs(before, after)

	e := findRoleChange(t, d, "ops")
	if !strings.Contains(e.Detail, "added admin:manage") {
		t.Errorf("expected the gained permission to be listed, got %q", e.Detail)
	}
	if !strings.Contains(e.Detail, "removed status:read") {
		t.Errorf("expected the lost permission to be listed, got %q", e.Detail)
	}
	// It must never fall back to opaque counts in the before/after fields.
	if e.Before != "" || e.After != "" {
		t.Errorf("permission change must not carry count fields, got before=%q after=%q", e.Before, e.After)
	}
}

// TestDiffRBACWildcardAdditionListed proves gaining a full-access wildcard is
// shown explicitly (it must never hide behind an unchanged count).
func TestDiffRBACWildcardAdditionListed(t *testing.T) {
	before := cfgWithAdmin(rbacAdmin(config.AdminRBACConfig{
		Enabled:    true,
		Roles:      []config.AdminRole{{Name: "power", Permissions: []string{"status:read"}}},
		Principals: []config.AdminPrincipal{{Name: "root", Role: rbac.RoleAdmin, Token: "SECRET-root-token-32-chars-pad---"}},
	}, ""))
	after := cfgWithAdmin(rbacAdmin(config.AdminRBACConfig{
		Enabled:    true,
		Roles:      []config.AdminRole{{Name: "power", Permissions: []string{"status:read", "*"}}},
		Principals: []config.AdminPrincipal{{Name: "root", Role: rbac.RoleAdmin, Token: "SECRET-root-token-32-chars-pad---"}},
	}, ""))
	d := diffConfigs(before, after)

	e := findRoleChange(t, d, "power")
	if !strings.Contains(e.Detail, "added *") {
		t.Errorf("wildcard grant must be listed explicitly, got %q", e.Detail)
	}
}

// TestDiffRBACResourceWildcardAdditionListed proves a resource-scoped wildcard
// grant (config:*) is listed, not summarized.
func TestDiffRBACResourceWildcardAdditionListed(t *testing.T) {
	before := cfgWithAdmin(rbacAdmin(config.AdminRBACConfig{
		Enabled:    true,
		Roles:      []config.AdminRole{{Name: "cfg", Permissions: []string{"config:read"}}},
		Principals: []config.AdminPrincipal{{Name: "root", Role: rbac.RoleAdmin, Token: "SECRET-root-token-32-chars-pad---"}},
	}, ""))
	after := cfgWithAdmin(rbacAdmin(config.AdminRBACConfig{
		Enabled:    true,
		Roles:      []config.AdminRole{{Name: "cfg", Permissions: []string{"config:read", "config:*"}}},
		Principals: []config.AdminPrincipal{{Name: "root", Role: rbac.RoleAdmin, Token: "SECRET-root-token-32-chars-pad---"}},
	}, ""))
	d := diffConfigs(before, after)

	e := findRoleChange(t, d, "cfg")
	if !strings.Contains(e.Detail, "added config:*") {
		t.Errorf("resource wildcard grant must be listed explicitly, got %q", e.Detail)
	}
}

// TestDiffRBACPermissionReorderIsNoChange proves reordering a role's permissions
// is not reported as a change (order-independent set comparison).
func TestDiffRBACPermissionReorderIsNoChange(t *testing.T) {
	before := cfgWithAdmin(rbacAdmin(config.AdminRBACConfig{
		Enabled:    true,
		Roles:      []config.AdminRole{{Name: "r", Permissions: []string{"a:b", "c:d"}}},
		Principals: []config.AdminPrincipal{{Name: "root", Role: rbac.RoleAdmin, Token: "SECRET-root-token-32-chars-pad---"}},
	}, ""))
	after := cfgWithAdmin(rbacAdmin(config.AdminRBACConfig{
		Enabled:    true,
		Roles:      []config.AdminRole{{Name: "r", Permissions: []string{"c:d", "a:b"}}},
		Principals: []config.AdminPrincipal{{Name: "root", Role: rbac.RoleAdmin, Token: "SECRET-root-token-32-chars-pad---"}},
	}, ""))
	d := diffConfigs(before, after)

	if diffHas(d, "Change permissions for RBAC role r") {
		t.Errorf("reordering permissions must not be reported as a change: %+v", d.Modifications)
	}
}

// TestDiffRBACDuplicatePermissionNormalized proves a duplicated permission
// normalizes to no change rather than a phantom diff.
func TestDiffRBACDuplicatePermissionNormalized(t *testing.T) {
	before := cfgWithAdmin(rbacAdmin(config.AdminRBACConfig{
		Enabled:    true,
		Roles:      []config.AdminRole{{Name: "r", Permissions: []string{"a:b"}}},
		Principals: []config.AdminPrincipal{{Name: "root", Role: rbac.RoleAdmin, Token: "SECRET-root-token-32-chars-pad---"}},
	}, ""))
	after := cfgWithAdmin(rbacAdmin(config.AdminRBACConfig{
		Enabled:    true,
		Roles:      []config.AdminRole{{Name: "r", Permissions: []string{"a:b", "a:b"}}},
		Principals: []config.AdminPrincipal{{Name: "root", Role: rbac.RoleAdmin, Token: "SECRET-root-token-32-chars-pad---"}},
	}, ""))
	d := diffConfigs(before, after)

	if diffHas(d, "Change permissions for RBAC role r") {
		t.Errorf("a duplicated permission must normalize to no change: %+v", d.Modifications)
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
	if !sp.RBAC.Serving.Enabled || !sp.RBAC.Persisted.Enabled {
		t.Error("security projection missing embedded RBAC status")
	}
}

// TestRBACPostureFlagsStagedDivergence proves the Security posture distinguishes
// the installed (serving) policy from a persisted change that is not yet live,
// so a stage_restart cannot make the panel show a future policy as active (N-03).
func TestRBACPostureFlagsStagedDivergence(t *testing.T) {
	serving := config.AdminConfig{
		Enabled: true, Listen: "127.0.0.1:9090",
		RBAC: config.AdminRBACConfig{Enabled: true, Principals: []config.AdminPrincipal{
			{Name: "root", Role: rbac.RoleAdmin, Token: "SECRET-root-token-32-chars-pad---"},
		}},
	}
	s := newTestServer(t, serving, Deps{})
	// Persisted config adds a principal — a staged change not yet installed.
	persisted := cfgWithAdmin(config.AdminConfig{
		Enabled: true, Listen: "127.0.0.1:9090",
		RBAC: config.AdminRBACConfig{Enabled: true, Principals: []config.AdminPrincipal{
			{Name: "root", Role: rbac.RoleAdmin, Token: "SECRET-root-token-32-chars-pad---"},
			{Name: "vic", Role: rbac.RoleViewer, Token: "SECRET-vic-token-32-chars-pad----"},
		}},
	})

	p := s.rbacPosture(persisted)
	if p.Serving.PrincipalCount != 1 {
		t.Errorf("serving principal_count = %d, want 1 (the installed policy)", p.Serving.PrincipalCount)
	}
	if p.Persisted.PrincipalCount != 2 {
		t.Errorf("persisted principal_count = %d, want 2 (the staged policy)", p.Persisted.PrincipalCount)
	}
	if !p.Pending {
		t.Error("expected pending=true when the serving and persisted policies differ")
	}
	if p.Serving.Generation == p.Persisted.Generation {
		t.Error("serving and persisted generations must differ when the policies differ")
	}
}

// TestRBACPostureStableWhenInSync proves an in-sync server reports no pending
// change and identical, non-empty generations.
func TestRBACPostureStableWhenInSync(t *testing.T) {
	admin := config.AdminConfig{
		Enabled: true, Listen: "127.0.0.1:9090",
		RBAC: config.AdminRBACConfig{Enabled: true, Principals: []config.AdminPrincipal{
			{Name: "root", Role: rbac.RoleAdmin, Token: "SECRET-root-token-32-chars-pad---"},
		}},
	}
	s := newTestServer(t, admin, Deps{})

	p := s.rbacPosture(cfgWithAdmin(admin))
	if p.Pending {
		t.Errorf("expected pending=false when serving matches persisted, got %+v", p)
	}
	if p.Serving.Generation == "" || p.Serving.Generation != p.Persisted.Generation {
		t.Errorf("in-sync generations must be equal and non-empty, got %q vs %q", p.Serving.Generation, p.Persisted.Generation)
	}
}

func TestProjectRBACDisabled(t *testing.T) {
	c := cfgWithAdmin(config.AdminConfig{Enabled: true, Listen: "127.0.0.1:9090"})
	got := projectRBAC(c)
	if got.Enabled || got.PrincipalCount != 0 || got.RoleCount != 0 || got.LegacyTokenActive {
		t.Fatalf("expected empty RBAC status, got %+v", got)
	}
}
