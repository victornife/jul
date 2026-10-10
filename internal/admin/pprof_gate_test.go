// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"net"
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

	do := func(h http.Handler, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+adminTok)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}

	// Go 1.27 adds goroutineleak to the stdlib's pprof subtree. It must inherit
	// Jul's existing capability gate just like the index and heap profiles.
	for _, path := range []string{"/debug/pprof/", "/debug/pprof/heap", "/debug/pprof/goroutineleak"} {
		t.Run(path, func(t *testing.T) {
			off := false
			if rr := do(build(t, &off), path); rr.Code != http.StatusNotFound {
				t.Fatalf("pprof disabled: got %d, want 404", rr.Code)
			}

			on := true
			if rr := do(build(t, &on), path); rr.Code != http.StatusOK {
				t.Fatalf("pprof enabled: got %d, want 200", rr.Code)
			}
		})
	}
}

func TestGoroutineLeakProfileRequiresAdminManage(t *testing.T) {
	const adminTok = "pprof-leak-admin-token-32-chars-pad"
	const viewerTok = "pprof-leak-viewer-token-32-chars-pad"
	s := newTestServer(t, config.AdminConfig{Listen: "127.0.0.1:0"}, Deps{})
	pol, err := rbac.Build(true, rbac.RoleViewer, nil, []rbac.PrincipalDef{
		{Name: "root", Role: rbac.RoleAdmin, Token: adminTok},
		{Name: "reader", Role: rbac.RoleViewer, Token: viewerTok},
	}, "", time.Now())
	if err != nil {
		t.Fatalf("build policy: %v", err)
	}
	s.UpdatePolicy(pol)
	h := s.routes()
	for _, tc := range []struct {
		name  string
		token string
		local string
		want  int
	}{
		{name: "anonymous", local: "127.0.0.1", want: http.StatusUnauthorized},
		{name: "viewer", token: viewerTok, local: "127.0.0.1", want: http.StatusForbidden},
		{name: "admin", token: adminTok, local: "127.0.0.1", want: http.StatusOK},
		{name: "insecure_public_listener", token: adminTok, local: "192.0.2.1", want: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/debug/pprof/goroutineleak", nil)
			req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey,
				&net.TCPAddr{IP: net.ParseIP(tc.local), Port: 9090}))
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != tc.want {
				t.Fatalf("profile status = %d, want %d", rr.Code, tc.want)
			}
		})
	}
}
