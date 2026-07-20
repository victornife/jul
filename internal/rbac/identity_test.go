// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package rbac

import "testing"

func TestIdentityHas(t *testing.T) {
	id := Identity{
		Permissions: []Permission{ConfigRead, StatusRead},
	}
	if !id.Has(ConfigRead) {
		t.Error("expected Has(ConfigRead)")
	}
	if id.Has(AdminManage) {
		t.Error("should not have AdminManage")
	}
}

func TestIdentityHasWildcard(t *testing.T) {
	id := Identity{
		Permissions: []Permission{Wildcard},
	}
	for _, p := range Catalog() {
		if !id.Has(p) {
			t.Errorf("wildcard identity should have %q", p)
		}
	}
}

func TestContextRoundtrip(t *testing.T) {
	ctx := WithIdentity(t.Context(), Identity{Principal: "alice", Role: RoleViewer})
	id, ok := IdentityFromContext(ctx)
	if !ok {
		t.Fatal("expected identity in context")
	}
	if id.Principal != "alice" || id.Role != RoleViewer {
		t.Errorf("got %+v", id)
	}
}

func TestContextMissing(t *testing.T) {
	_, ok := IdentityFromContext(t.Context())
	if ok {
		t.Error("expected no identity in empty context")
	}
}
