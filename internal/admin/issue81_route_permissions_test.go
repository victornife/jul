// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"testing"

	"jul/internal/rbac"
)

func issue81Route(t *testing.T, pattern string) RouteSpec {
	t.Helper()
	for _, route := range Catalog {
		if route.Pattern == pattern {
			return route
		}
	}
	t.Fatalf("route %q is not registered", pattern)
	return RouteSpec{}
}

func issue81HasPermission(permissions []rbac.Permission, want rbac.Permission) bool {
	for _, permission := range permissions {
		if permission == want {
			return true
		}
	}
	return false
}

func TestIssue81TypedProjectionRoutesAdmitConfigPrincipals(t *testing.T) {
	traffic := issue81Route(t, "/api/traffic-controls")
	for _, want := range []rbac.Permission{
		rbac.StatusRead,
		rbac.ConfigRead,
		rbac.ConfigWrite,
		rbac.ConfigApply,
	} {
		if !issue81HasPermission(traffic.AnyPermissions, want) {
			t.Fatalf("traffic-controls route does not admit %q: %v", want, traffic.AnyPermissions)
		}
	}

	pending := issue81Route(t, "/api/config/pending-restart")
	for _, want := range []rbac.Permission{rbac.ConfigRead, rbac.ConfigWrite, rbac.ConfigApply} {
		if !issue81HasPermission(pending.AnyPermissions, want) {
			t.Fatalf("pending-restart route does not admit %q: %v", want, pending.AnyPermissions)
		}
	}
}

func TestIssue81RawCandidatePreviewRequiresConfigRaw(t *testing.T) {
	preview := issue81Route(t, "/api/config/preview")
	if preview.Permission != rbac.ConfigRaw {
		t.Fatalf("raw preview permission=%q want %q", preview.Permission, rbac.ConfigRaw)
	}
	if len(preview.AnyPermissions) != 0 || preview.Public || preview.Authenticated {
		t.Fatalf("raw preview route weakens config:raw boundary: %#v", preview)
	}
}
