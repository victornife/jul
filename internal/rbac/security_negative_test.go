// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package rbac

import (
	"strings"
	"testing"
	"time"
)

const securityTestToken = "security-negative-test-token-000000000000"

func TestBuildEnabledWithoutPrincipalsFailsClosed(t *testing.T) {
	_, err := Build(true, RoleAdmin, nil, nil, "", time.Now())
	if err == nil {
		t.Fatal("enabled RBAC accepted a policy with no principals")
	}
	if !strings.Contains(err.Error(), "no principals or legacy token") {
		t.Fatalf("Build error = %q, want missing-principal guidance", err)
	}
}

func TestBuildPrincipalWithoutTokenFailsClosed(t *testing.T) {
	_, err := Build(true, RoleAdmin, nil, []PrincipalDef{{
		Name: "operator",
		Role: RoleAdmin,
	}}, "", time.Now())
	if err == nil {
		t.Fatal("RBAC accepted a principal without a token")
	}
	if !strings.Contains(err.Error(), "has no token") {
		t.Fatalf("Build error = %q, want token validation guidance", err)
	}
}

func TestBuildUnknownLegacyRoleFailsClosed(t *testing.T) {
	_, err := Build(true, "missing-role", nil, nil, securityTestToken, time.Now())
	if err == nil {
		t.Fatal("RBAC accepted an unknown legacy/default role")
	}
	if !strings.Contains(err.Error(), "missing-role") {
		t.Fatalf("Build error = %q, want the unknown role name", err)
	}
}

func TestBuildRejectsDuplicateLegacyAndNamedToken(t *testing.T) {
	_, err := Build(true, RoleAdmin, nil, []PrincipalDef{{
		Name:  "named-admin",
		Role:  RoleAdmin,
		Token: securityTestToken,
	}}, securityTestToken, time.Now())
	if err == nil {
		t.Fatal("RBAC accepted one token for both legacy and named principals")
	}
	if !strings.Contains(err.Error(), "duplicate token") {
		t.Fatalf("Build error = %q, want duplicate-token guidance", err)
	}
}
