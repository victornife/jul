// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"strings"
	"testing"
	"time"
)

func rbacEnabledAdmin(principals []AdminPrincipal, roles []AdminRole) AdminConfig {
	return AdminConfig{
		Enabled: true,
		Listen:  "127.0.0.1:9090",
		RBAC: AdminRBACConfig{
			Enabled:    true,
			Principals: principals,
			Roles:      roles,
		},
	}
}

func adminPrincipal(name, role, token string) AdminPrincipal {
	return AdminPrincipal{Name: name, Role: role, Token: token}
}

const testToken = "this-is-a-valid-high-entropy-token-value-1234"

func TestValidateRBACDisabledNoOp(t *testing.T) {
	cfg := AdminRBACConfig{Enabled: false}
	errs := validateRBAC(cfg)
	if len(errs) != 0 {
		t.Errorf("disabled RBAC should produce no errors, got %d: %v", len(errs), errs)
	}
}

func TestValidateRBACEnabledNoPrincipals(t *testing.T) {
	cfg := AdminRBACConfig{Enabled: true}
	errs := validateRBAC(cfg)
	if len(errs) == 0 {
		t.Error("expected error when RBAC enabled but no principals")
	}
}

func TestValidateRBACValidSingleAdmin(t *testing.T) {
	cfg := AdminRBACConfig{
		Enabled:    true,
		Principals: []AdminPrincipal{adminPrincipal("alice", "admin", testToken)},
	}
	errs := validateRBAC(cfg)
	if len(errs) != 0 {
		t.Errorf("expected no errors, got %d: %v", len(errs), errs)
	}
}

func TestValidateRBACDuplicatePrincipalName(t *testing.T) {
	cfg := AdminRBACConfig{
		Enabled: true,
		Principals: []AdminPrincipal{
			adminPrincipal("alice", "admin", testToken),
			adminPrincipal("alice", "admin", strings.Repeat("b", 45)),
		},
	}
	errs := validateRBAC(cfg)
	if len(errs) == 0 {
		t.Error("expected error for duplicate principal name")
	}
}

func TestValidateRBACReservedPrincipalName(t *testing.T) {
	cfg := AdminRBACConfig{
		Enabled: true,
		Principals: []AdminPrincipal{
			{Name: "shared", Role: "admin", Token: testToken},
		},
	}
	errs := validateRBAC(cfg)
	if len(errs) == 0 {
		t.Error("expected error for reserved principal name 'shared'")
	}
}

func TestValidateRBACUnknownRole(t *testing.T) {
	cfg := AdminRBACConfig{
		Enabled: true,
		Principals: []AdminPrincipal{
			{Name: "alice", Role: "nosuchrole", Token: testToken},
		},
	}
	errs := validateRBAC(cfg)
	if len(errs) == 0 {
		t.Error("expected error for unknown role reference")
	}
}

func TestValidateRBACCustomRole(t *testing.T) {
	cfg := AdminRBACConfig{
		Enabled: true,
		Roles: []AdminRole{
			{Name: "deployer", Permissions: []string{"config:apply", "status:read"}},
		},
		Principals: []AdminPrincipal{
			// deployer only can't admin:manage → also need an admin.
			{Name: "ci", Role: "deployer", Token: testToken},
			{Name: "admin", Role: "admin", Token: strings.Repeat("a", 45)},
		},
	}
	errs := validateRBAC(cfg)
	if len(errs) != 0 {
		t.Errorf("expected no errors for valid custom role, got %d: %v", len(errs), errs)
	}
}

func TestValidateRBACCustomRoleUnknownPermission(t *testing.T) {
	cfg := AdminRBACConfig{
		Enabled: true,
		Roles: []AdminRole{
			{Name: "bad", Permissions: []string{"nonesuch:action"}},
		},
		Principals: []AdminPrincipal{
			{Name: "alice", Role: "admin", Token: testToken},
		},
	}
	errs := validateRBAC(cfg)
	if len(errs) == 0 {
		t.Error("expected error for unknown permission in custom role")
	}
}

func TestValidateRBACPredefinedRoleCannotBeRedefined(t *testing.T) {
	cfg := AdminRBACConfig{
		Enabled: true,
		Roles: []AdminRole{
			{Name: "admin", Permissions: []string{"status:read"}}, // redefinition attempt
		},
		Principals: []AdminPrincipal{
			{Name: "alice", Role: "admin", Token: testToken},
		},
	}
	errs := validateRBAC(cfg)
	if len(errs) == 0 {
		t.Error("expected error when predefined role is redefined")
	}
}

func TestValidateRBACNoAdminCapablePrincipal(t *testing.T) {
	cfg := AdminRBACConfig{
		Enabled: true,
		Principals: []AdminPrincipal{
			{Name: "viewer", Role: "viewer", Token: testToken},
		},
	}
	errs := validateRBAC(cfg)
	if len(errs) == 0 {
		t.Error("expected error when no admin-capable principal exists")
	}
}

func TestValidateRBACExpiredPrincipal(t *testing.T) {
	cfg := AdminRBACConfig{
		Enabled: true,
		Principals: []AdminPrincipal{
			{Name: "alice", Role: "admin", Token: testToken, ExpiresAt: time.Now().Add(-time.Hour)},
		},
	}
	errs := validateRBAC(cfg)
	// Should error: past expiry = inactive = no admin-capable principal.
	if len(errs) == 0 {
		t.Error("expected error for expired-only admin principal")
	}
}

func TestValidateRBACLiteralTokenLintWarning(t *testing.T) {
	// This tests the Lint path, not validateRBAC (which is in validate.go).
	cfg := &Config{}
	cfg.Admin = AdminConfig{
		Enabled: true,
		Listen:  "127.0.0.1:9090",
		RBAC: AdminRBACConfig{
			Enabled: true,
			Principals: []AdminPrincipal{
				{Name: "alice", Role: "admin", Token: testToken},
			},
		},
	}
	// Parse to apply defaults.
	_, _ = Parse([]byte("[admin]\nenabled = true\nlisten = \"127.0.0.1:9090\""))
	diags := Lint(cfg)
	var found bool
	for _, d := range diags {
		if d.Severity == SeverityWarning && strings.Contains(d.Field, "rbac.principals") {
			found = true
		}
	}
	if !found {
		t.Error("expected lint warning for literal token in RBAC principal")
	}
}

func TestIsSecretRef(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{"${env:MY_TOKEN}", true},
		{"${file:/run/secrets/token}", true},
		{"${secret:vault/token}", true},
		{"plaintext-token-value", false},
		{"", false},
		{"${env:}", true}, // technically a secret ref format (empty body); validation catches empty tokens separately
	}
	for _, c := range cases {
		if got := isSecretRef(c.s); got != c.want {
			t.Errorf("isSecretRef(%q) = %v, want %v", c.s, got, c.want)
		}
	}
}
