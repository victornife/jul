// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package rbac

import "testing"

// TestPredefinedRolesStableContract pins the predefined role permission sets so
// any accidental change is caught immediately (spec §28.2 requirement).
func TestPredefinedRolesStableContract(t *testing.T) {
	cases := []struct {
		role  string
		perms []Permission
	}{
		{RoleViewer, []Permission{
			StatusRead, MetricsRead, ConfigRead, HistoryRead, ObservabilityRead, AuditRead,
		}},
		{RoleOperator, []Permission{
			StatusRead, MetricsRead, ConfigRead, ConfigWrite, ConfigApply,
			HistoryRead, HistoryRollback, PluginsUpload, ObservabilityRead,
			AuditRead, AuditExport, CachePurge, ReloadTrigger,
		}},
		{RoleAdmin, []Permission{Wildcard}},
		{RoleAuditor, []Permission{StatusRead, ObservabilityRead, AuditRead, AuditExport}},
	}
	for _, tc := range cases {
		got, err := PermissionsForPredefined(tc.role)
		if err != nil {
			t.Fatalf("role %q: %v", tc.role, err)
		}
		if len(got) != len(tc.perms) {
			t.Errorf("role %q: got %d permissions, want %d", tc.role, len(got), len(tc.perms))
			continue
		}
		for i, p := range tc.perms {
			if got[i] != p {
				t.Errorf("role %q [%d]: got %q, want %q", tc.role, i, got[i], p)
			}
		}
	}
}

func TestIsPredefined(t *testing.T) {
	for _, name := range PredefinedNames() {
		if !IsPredefined(name) {
			t.Errorf("IsPredefined(%q) = false, want true", name)
		}
	}
	if IsPredefined("custom-role") {
		t.Error("custom-role should not be predefined")
	}
}

func TestPermissionsForPredefinedUnknown(t *testing.T) {
	_, err := PermissionsForPredefined("nosuchrole")
	if err == nil {
		t.Error("expected error for unknown predefined role")
	}
}

func TestResolveCustomValid(t *testing.T) {
	perms, err := resolveCustom([]string{"config:read", "status:read"})
	if err != nil {
		t.Fatal(err)
	}
	if len(perms) != 2 {
		t.Errorf("expected 2 permissions, got %d", len(perms))
	}
}

func TestResolveCustomUnknown(t *testing.T) {
	_, err := resolveCustom([]string{"config:read", "unknown:action"})
	if err == nil {
		t.Error("expected error for unknown permission")
	}
}

func TestResolveCustomDedup(t *testing.T) {
	perms, err := resolveCustom([]string{"config:read", "config:read"})
	if err != nil {
		t.Fatal(err)
	}
	if len(perms) != 1 {
		t.Errorf("expected 1 permission after dedup, got %d", len(perms))
	}
}
