// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package rbac

import (
	"strings"
	"testing"
	"time"
)

func TestBuildDisabledNoLegacyToken(t *testing.T) {
	p, err := Build(false, "", nil, nil, "", time.Now())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.Enabled() {
		t.Error("expected disabled policy")
	}
	if p.PrincipalCount() != 0 {
		t.Error("expected 0 principals for disabled+no-legacy")
	}
}

func TestBuildDisabledWithLegacyToken(t *testing.T) {
	const tok = "this-is-a-32-character-test-token!!"
	p, err := Build(false, "admin", nil, nil, tok, time.Now())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.Enabled() {
		t.Error("expected disabled policy")
	}
	id, err := p.Authenticate("Bearer "+tok, time.Now())
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if !id.Legacy {
		t.Error("expected Legacy=true for shared token")
	}
	if id.Principal != "shared" {
		t.Errorf("expected principal=shared, got %q", id.Principal)
	}
}

func TestBuildEnabledSinglePrincipal(t *testing.T) {
	const tok = "this-is-a-32-character-test-token!!"
	principals := []PrincipalDef{
		{Name: "alice", Role: RoleAdmin, Token: tok},
	}
	p, err := Build(true, "admin", nil, principals, "", time.Now())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !p.Enabled() {
		t.Error("expected enabled policy")
	}
	id, err := p.Authenticate("Bearer "+tok, time.Now())
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if id.Principal != "alice" {
		t.Errorf("got principal %q, want alice", id.Principal)
	}
	if !id.Has(AdminManage) {
		t.Error("admin role should have AdminManage")
	}
}

func TestAuthenticateBadToken(t *testing.T) {
	const tok = "this-is-a-32-character-test-token!!"
	principals := []PrincipalDef{{Name: "alice", Role: RoleAdmin, Token: tok}}
	p, _ := Build(true, "admin", nil, principals, "", time.Now())
	_, err := p.Authenticate("Bearer wrongtoken-padding-to-be-long-enough", time.Now())
	if err != ErrUnauthenticated {
		t.Errorf("expected ErrUnauthenticated, got %v", err)
	}
}

func TestAuthenticateNoBearer(t *testing.T) {
	p, _ := Build(false, "", nil, nil, "", time.Now())
	_, err := p.Authenticate("Basic abc", time.Now())
	if err != ErrUnauthenticated {
		t.Errorf("expected ErrUnauthenticated, got %v", err)
	}
}

func TestAuthenticateDisabledPrincipal(t *testing.T) {
	const tok = "this-is-a-32-character-test-token!!"
	// alice is disabled but we still need one active admin → add bob.
	const bob = "bob-is-a-32-character-test-token---"
	principals := []PrincipalDef{
		{Name: "alice", Role: RoleAdmin, Token: tok, Disabled: true},
		{Name: "bob", Role: RoleAdmin, Token: bob},
	}
	p, err := Build(true, "admin", nil, principals, "", time.Now())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	_, err = p.Authenticate("Bearer "+tok, time.Now())
	if err != ErrDisabled {
		t.Errorf("expected ErrDisabled, got %v", err)
	}
}

func TestAuthenticateExpiredPrincipal(t *testing.T) {
	const tok = "this-is-a-32-character-test-token!!"
	const bob = "bob-is-a-32-character-test-token---"
	past := time.Now().Add(-time.Hour)
	principals := []PrincipalDef{
		{Name: "alice", Role: RoleAdmin, Token: tok, ExpiresAt: past},
		{Name: "bob", Role: RoleAdmin, Token: bob},
	}
	p, err := Build(true, "admin", nil, principals, "", time.Now())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	_, err = p.Authenticate("Bearer "+tok, time.Now())
	if err != ErrDisabled {
		t.Errorf("expected ErrDisabled for expired, got %v", err)
	}
}

func TestBuildDuplicateTokenRejected(t *testing.T) {
	const tok = "this-is-a-32-character-test-token!!"
	principals := []PrincipalDef{
		{Name: "alice", Role: RoleAdmin, Token: tok},
		{Name: "bob", Role: RoleAdmin, Token: tok}, // same token
	}
	_, err := Build(true, "admin", nil, principals, "", time.Now())
	if err == nil {
		t.Error("expected error for duplicate token")
	}
	if !strings.Contains(err.Error(), "duplicate token") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestBuildNoAdminCapablePrincipalFails(t *testing.T) {
	const tok = "this-is-a-32-character-test-token!!"
	principals := []PrincipalDef{
		{Name: "alice", Role: RoleViewer, Token: tok},
	}
	_, err := Build(true, "admin", nil, principals, "", time.Now())
	if err == nil {
		t.Error("expected error when no admin-capable principal")
	}
}

func TestBuildCustomRole(t *testing.T) {
	const tok = "this-is-a-32-character-test-token!!"
	customRoles := map[string][]string{
		"deployer": {"config:apply", "status:read"},
	}
	principals := []PrincipalDef{
		{Name: "ci", Role: "deployer", Token: tok},
		// Also need one admin-capable principal.
		{Name: "admin-user", Role: RoleAdmin, Token: "admin-user-has-32-char-token--test"},
	}
	p, err := Build(true, "admin", customRoles, principals, "", time.Now())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	id, err := p.Authenticate("Bearer "+tok, time.Now())
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if !id.Has(ConfigApply) {
		t.Error("deployer should have config:apply")
	}
	if id.Has(AdminManage) {
		t.Error("deployer should not have admin:manage")
	}
}

func TestBuildUnknownRoleFails(t *testing.T) {
	const tok = "this-is-a-32-character-test-token!!"
	principals := []PrincipalDef{
		{Name: "alice", Role: "nosuchrole", Token: tok},
	}
	_, err := Build(true, "admin", nil, principals, "", time.Now())
	if err == nil {
		t.Error("expected error for unknown role reference")
	}
}

func TestTokenID(t *testing.T) {
	h := hashToken("some-test-token")
	id := tokenID(h)
	if len(id) != tokenIDLen {
		t.Errorf("tokenID len = %d, want %d", len(id), tokenIDLen)
	}
	// Same token should always produce the same ID.
	h2 := hashToken("some-test-token")
	id2 := tokenID(h2)
	if id != id2 {
		t.Error("tokenID is not deterministic")
	}
}

func TestValidateTokenShort(t *testing.T) {
	if err := ValidateToken("short"); err == nil {
		t.Error("expected error for short token")
	}
}

func TestValidateTokenEmpty(t *testing.T) {
	if err := ValidateToken(""); err == nil {
		t.Error("expected error for empty token")
	}
}

func TestValidateTokenOK(t *testing.T) {
	tok := strings.Repeat("a", MinTokenLen)
	if err := ValidateToken(tok); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}
