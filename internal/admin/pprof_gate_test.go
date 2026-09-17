// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/rbac"
)

// TestPprofEnabledHelper exercises the pprofEnabled compatibility helper
// directly, mirroring the pluginUploadEnabled coverage above.
func TestPprofEnabledHelper(t *testing.T) {
	var cfg config.AdminConfig
	if !pprofEnabled(cfg) {
		t.Fatal("nil PprofEnabled should default to enabled for directly constructed config")
	}
	off := false
	cfg.PprofEnabled = &off
	if pprofEnabled(cfg) {
		t.Fatal("explicit false pprof flag reported enabled")
	}
	on := true
	cfg.PprofEnabled = &on
	if !pprofEnabled(cfg) {
		t.Fatal("explicit true pprof flag reported disabled")
	}
}

// TestPprofRouteDisabledByConfig verifies that `[admin] pprof = false` removes
// the /debug/pprof/ surface entirely (404), even for a caller who holds
// admin:manage, while an admin request still passes RBAC when the flag is
// enabled (default).
func TestPprofRouteDisabledByConfig(t *testing.T) {
	const adminTok = "pprof-flow-admin-token-32-chars-pad"

	build := func(t *testing.T, pprofEnabledFlag *bool) http.Handler {
		t.Helper()
		cfg := config.AdminConfig{PprofEnabled: pprofEnabledFlag}
		s := newTestServer(t, cfg, Deps{})
		pol, err := rbac.Build(true, rbac.RoleViewer, nil, []rbac.PrincipalDef{
			{Name: "root", Role: rbac.RoleAdmin, Token: adminTok},
		}, "", time.Now())
		if err != nil {
			t.Fatalf("build policy: %v", err)
		}
		s.UpdatePolicy(pol)
		return s.routes()
	}

	do := func(h http.Handler) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
		req.Header.Set("Authorization", "Bearer "+adminTok)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}

	off := false
	if rr := do(build(t, &off)); rr.Code != http.StatusNotFound {
		t.Fatalf("pprof disabled: got %d, want 404", rr.Code)
	}

	on := true
	if rr := do(build(t, &on)); rr.Code == http.StatusNotFound {
		t.Fatalf("pprof enabled: got 404, want the request to pass authz and reach the handler")
	}
}
