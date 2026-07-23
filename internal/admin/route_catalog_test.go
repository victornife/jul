// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/rbac"
)

// ── Catalog completeness guards ───────────────────────────────────────────────

// TestCatalogNoRouteDefaultsToPublic fails if any new non-public route is
// added to the catalog without permissions. Every non-public route MUST have
// either Permissions or Permission declared; there is no implicit default-allow.
func TestCatalogNoRouteDefaultsToPublic(t *testing.T) {
	for _, spec := range Catalog {
		if spec.Public {
			continue // public routes don't need a permission
		}
		if spec.Permission == "" && len(spec.Permissions) == 0 {
			t.Errorf("route %q is non-public but has no Permission/Permissions declared; add permissions to route_catalog.go", spec.Pattern)
		}
	}
}

// TestCatalogPermissionsInCatalog fails if any route uses a permission string
// that is not in the rbac package's permission catalog.
func TestCatalogPermissionsInCatalog(t *testing.T) {
	for _, spec := range Catalog {
		if spec.Public {
			continue
		}
		if spec.Permission != "" && !rbac.Known(spec.Permission) {
			t.Errorf("route %q uses permission %q which is not in the rbac catalog", spec.Pattern, spec.Permission)
		}
		for _, p := range spec.Permissions {
			if !rbac.Known(p) {
				t.Errorf("route %q uses permission %q which is not in the rbac catalog", spec.Pattern, p)
			}
		}
	}
}

// TestCatalogNoDuplicatePatterns fails if the same pattern appears more than
// once, which would result in one entry silently shadowing the other.
func TestCatalogNoDuplicatePatterns(t *testing.T) {
	seen := make(map[string]bool, len(Catalog))
	for _, spec := range Catalog {
		if seen[spec.Pattern] {
			t.Errorf("duplicate pattern %q in route_catalog.go", spec.Pattern)
		}
		seen[spec.Pattern] = true
	}
}

// TestCatalogAllSpecsHaveHandler fails if any route spec has a nil Handler,
// which would panic at routes() build time.
func TestCatalogAllSpecsHaveHandler(t *testing.T) {
	for _, spec := range Catalog {
		if spec.Handler == nil {
			t.Errorf("route %q has a nil Handler; all routes must declare a handler", spec.Pattern)
		}
	}
}

// TestCatalogAllSpecsHaveMethods fails if any route declares no methods (which
// means it is not declared, making the catalog incomplete).
func TestCatalogAllSpecsHaveMethods(t *testing.T) {
	for _, spec := range Catalog {
		if len(spec.Methods) == 0 {
			t.Errorf("route %q has no Methods declared; add at least one HTTP method", spec.Pattern)
		}
	}
}

// TestCatalogPlannedRestartHasPermissions checks that the planned-restart
// status and discard routes have their required permissions explicitly declared.
func TestCatalogPlannedRestartHasPermissions(t *testing.T) {
	required := map[string]rbac.Permission{
		"/api/config/pending-restart":         rbac.ConfigRead,
		"/api/config/pending-restart/discard": rbac.ConfigApply,
	}
	byPattern := make(map[string]RouteSpec, len(Catalog))
	for _, spec := range Catalog {
		byPattern[spec.Pattern] = spec
	}
	for pattern, perm := range required {
		spec, ok := byPattern[pattern]
		if !ok {
			t.Errorf("planned-restart route %q is missing from the catalog", pattern)
			continue
		}
		if spec.Public {
			t.Errorf("planned-restart route %q must not be public", pattern)
		}
		got := spec.Permission
		if len(spec.Permissions) > 0 {
			got = spec.Permissions[http.MethodGet]
			if got == "" {
				got = spec.Permissions[http.MethodPost]
			}
		}
		if got != perm {
			t.Errorf("planned-restart route %q: got permission %q, want %q", pattern, got, perm)
		}
	}
}

// TestCatalogMethodPermissionsCoverAllMethods fails if a route declares a
// per-method permission map that is missing any of its advertised methods.
func TestCatalogMethodPermissionsCoverAllMethods(t *testing.T) {
	for _, spec := range Catalog {
		if spec.Public || len(spec.Permissions) == 0 {
			continue
		}
		for _, m := range spec.Methods {
			if _, ok := spec.Permissions[m]; !ok {
				t.Errorf("route %q advertises method %q but has no Permissions entry for it", spec.Pattern, m)
			}
		}
	}
}

// TestCatalogHandlerMethodsMatchCatalog fails if a handler accepts methods that
// are not declared in the catalog for that route.
func TestCatalogHandlerMethodsMatchCatalog(t *testing.T) {
	byPattern := make(map[string]RouteSpec, len(Catalog))
	for _, spec := range Catalog {
		byPattern[spec.Pattern] = spec
	}

	tests := []struct {
		pattern string
		method  string
		want    int // expected status when method is not in catalog
	}{
		{"/api/config/raw", http.MethodDelete, http.StatusMethodNotAllowed},
		{"/api/config/settings", http.MethodDelete, http.StatusMethodNotAllowed},
		{"/api/config/history/{id}", http.MethodPost, http.StatusMethodNotAllowed},
		{"/api/history/get", http.MethodPut, http.StatusMethodNotAllowed},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.method, func(t *testing.T) {
			spec, ok := byPattern[tt.pattern]
			if !ok {
				t.Fatalf("route %q missing from catalog", tt.pattern)
			}
			if !spec.Public {
				// To reach the handler we need a valid legacy token; the legacy
				// path grants all permissions so authorization will pass once the
				// method is allowed.
				s := &Server{cfg: minimalAdminCfgWithToken("secret-test-token-32-chars-padding")}
				req := httptest.NewRequest(tt.method, tt.pattern, nil)
				req.Header.Set("Authorization", "Bearer secret-test-token-32-chars-padding")
				rr := httptest.NewRecorder()
				s.routes().ServeHTTP(rr, req)
				if rr.Code != tt.want {
					t.Errorf("got status %d, want %d", rr.Code, tt.want)
				}
			}
		})
	}
}

// TestCatalogPublicOnlyApproved ensures only the approved public routes exist.
// If a new public route is added the developer must explicitly add it here.
func TestCatalogPublicOnlyApproved(t *testing.T) {
	approved := map[string]bool{
		"/healthz": true,
		"/readyz":  true,
		"/":        true, // console shell / root — loaded before token prompt
	}
	for _, spec := range Catalog {
		if !spec.Public {
			continue
		}
		if !approved[spec.Pattern] {
			t.Errorf("route %q is public but not in the approved-public list; either add it to the approved list in this test or make it require authentication", spec.Pattern)
		}
	}
}

// ── Authorization enforcement tests ──────────────────────────────────────────

// TestRequirePermissionReturns401WhenNoToken verifies that a protected route
// returns 401 when no Authorization header is supplied and a legacy token is
// configured.
func TestRequirePermissionReturns401WhenNoToken(t *testing.T) {
	s := &Server{cfg: minimalAdminCfgWithToken("secret-test-token-32-chars-padding")}
	h := s.requirePermission(rbac.StatusRead, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

// TestRequirePermissionAllowsLegacyToken verifies the legacy single-token path
// grants access and stores a synthetic Identity in context.
func TestRequirePermissionAllowsLegacyToken(t *testing.T) {
	const tok = "secret-test-token-32-chars-padding"
	s := &Server{cfg: minimalAdminCfgWithToken(tok)}
	var gotIdentity rbac.Identity
	h := s.requirePermission(rbac.StatusRead, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := rbac.IdentityFromContext(r.Context())
		if !ok {
			t.Error("expected identity in context")
		}
		gotIdentity = id
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
	if !gotIdentity.Legacy {
		t.Error("expected legacy identity")
	}
	if !gotIdentity.Has(rbac.StatusRead) {
		t.Error("legacy identity should have all permissions via wildcard")
	}
}

// TestRBACDisableClearsPolicy verifies that disabling RBAC after it was
// enabled clears the active policy and falls back to legacy token auth.
func TestRBACDisableClearsPolicy(t *testing.T) {
	const rbacTok = "rbac-admin-32-chars-padded-------"
	const legacyTok = "legacy-tok-32-chars-padded-------"
	pol, err := rbac.Build(true, "admin", nil, []rbac.PrincipalDef{
		{Name: "admin", Role: rbac.RoleAdmin, Token: rbacTok},
	}, "", timeNow())
	if err != nil {
		t.Fatalf("build policy: %v", err)
	}
	s := &Server{cfg: config.AdminConfig{Token: legacyTok}}
	s.installAuth(config.AdminConfig{Token: legacyTok}, nil)
	s.UpdatePolicy(pol)

	// Sanity: RBAC token works while enabled.
	h := s.requirePermission(rbac.ConfigApply, okHandler())
	req := httptest.NewRequest(http.MethodPost, "/api/config/apply", nil)
	req.Header.Set("Authorization", "Bearer "+rbacTok)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 while RBAC enabled, got %d", rr.Code)
	}

	// Disable RBAC: policy is cleared.
	s.UpdatePolicy(nil)

	// The previously valid RBAC token must now be rejected; the legacy token
	// takes over.
	req = httptest.NewRequest(http.MethodPost, "/api/config/apply", nil)
	req.Header.Set("Authorization", "Bearer "+rbacTok)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for old RBAC token after RBAC disabled, got %d", rr.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/config/apply", nil)
	req.Header.Set("Authorization", "Bearer "+legacyTok)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 for legacy token after RBAC disabled, got %d", rr.Code)
	}
}

// TestLiveAdminConfigTokenUpdate verifies that updating the live admin config
// changes the legacy token checked by auth middleware without restarting.
func TestLiveAdminConfigTokenUpdate(t *testing.T) {
	s := &Server{cfg: config.AdminConfig{Token: "old-token-32-chars-padded-------"}}
	s.installAuth(config.AdminConfig{Token: "old-token-32-chars-padded-------"}, nil)

	h := s.requirePermission(rbac.ConfigApply, okHandler())

	// Old token works.
	req := httptest.NewRequest(http.MethodPost, "/api/config/apply", nil)
	req.Header.Set("Authorization", "Bearer old-token-32-chars-padded-------")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 with old token, got %d", rr.Code)
	}

	// Update live config to a new token.
	s.UpdateLiveAdminConfig(config.AdminConfig{Token: "new-token-32-chars-padded-------"})

	// New token works; old token is rejected.
	req = httptest.NewRequest(http.MethodPost, "/api/config/apply", nil)
	req.Header.Set("Authorization", "Bearer new-token-32-chars-padded-------")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 with new token, got %d", rr.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/config/apply", nil)
	req.Header.Set("Authorization", "Bearer old-token-32-chars-padded-------")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with old token, got %d", rr.Code)
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// TestRequirePermissionReturns403WhenRBACForbids verifies that an authenticated
// RBAC principal receives 403 when they lack the required permission.
func TestRequirePermissionReturns403WhenRBACForbids(t *testing.T) {
	const adminTok = "admin-user-32-char-padding-test--"
	const viewTok = "viewer-user-32-char-padding-test-"
	principals := []rbac.PrincipalDef{
		{Name: "admin-user", Role: rbac.RoleAdmin, Token: adminTok},
		{Name: "viewer-user", Role: rbac.RoleViewer, Token: viewTok},
	}
	pol, err := rbac.Build(true, "admin", nil, principals, "", timeNow())
	if err != nil {
		t.Fatalf("build policy: %v", err)
	}
	s := &Server{}
	s.UpdatePolicy(pol)

	// viewer tries to access a config:apply route
	h := s.requirePermission(rbac.ConfigApply, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/config/apply", nil)
	req.Header.Set("Authorization", "Bearer "+viewTok)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("403 should be JSON; Content-Type=%q", ct)
	}
}

// TestRequirePermissionRBACAllows verifies that an authenticated RBAC principal
// with sufficient permission is allowed through.
func TestRequirePermissionRBACAllows(t *testing.T) {
	const tok = "operator-token-32-chars-padded---"
	principals := []rbac.PrincipalDef{
		{Name: "op", Role: rbac.RoleOperator, Token: tok},
		{Name: "admin", Role: rbac.RoleAdmin, Token: "admin-token-32-chars-padded------"},
	}
	pol, err := rbac.Build(true, "admin", nil, principals, "", timeNow())
	if err != nil {
		t.Fatalf("build policy: %v", err)
	}
	s := &Server{}
	s.UpdatePolicy(pol)

	h := s.requirePermission(rbac.ConfigApply, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := rbac.IdentityFromContext(r.Context())
		if !ok || id.Principal != "op" {
			t.Errorf("expected op identity, got %+v", id)
		}
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/config/apply", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

// TestCatalogAdminManageGuardOnAdminRoutes verifies that pprof requires
// admin:manage (the most restrictive permission in the catalog).
func TestCatalogAdminManageGuardOnAdminRoutes(t *testing.T) {
	byPattern := make(map[string]RouteSpec, len(Catalog))
	for _, spec := range Catalog {
		byPattern[spec.Pattern] = spec
	}
	spec, ok := byPattern["/debug/pprof/"]
	if !ok {
		t.Fatal("/debug/pprof/ must be in the catalog")
	}
	if spec.Permission != rbac.AdminManage {
		t.Errorf("/debug/pprof/ must require admin:manage, got %q", spec.Permission)
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func minimalAdminCfgWithToken(tok string) config.AdminConfig {
	return config.AdminConfig{
		Enabled: true,
		Listen:  "127.0.0.1:9090",
		Token:   tok,
	}
}

func timeNow() time.Time {
	return time.Now()
}
