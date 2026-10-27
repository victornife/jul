// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"jul/internal/adminapi"
	"jul/internal/config"
	"jul/internal/rbac"
)

func TestInspectionOnlyConsoleProjectionIsScoped(t *testing.T) {
	const reader = "inspect-only-console-token-32-chars"
	cfg := &config.Config{Cache: config.CacheConfig{Enabled: true}, Servers: []config.ServerConfig{{ServerNames: []string{"secret-configured-host.example"}}}}
	loads := 0
	s := newTestServer(t, config.AdminConfig{}, Deps{Cache: &inspectionFake{}, LoadConfig: func() (*config.Config, error) { loads++; return cfg, nil }})
	p, err := rbac.Build(true, rbac.RoleAdmin, map[string][]string{"inspect": {"cache:inspect"}}, []rbac.PrincipalDef{{Name: "root", Role: rbac.RoleAdmin, Token: "root-inspection-console-token-32"}, {Name: "reader", Role: "inspect", Token: reader}}, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s.UpdatePolicy(p)
	loads = 0
	r := httptest.NewRequest(http.MethodGet, "/api/traffic-controls", nil)
	r.Header.Set("Authorization", "Bearer "+reader)
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, r)
	if rr.Code != 200 {
		t.Fatal("inspection-only Console entry denied", rr.Code, rr.Body.String())
	}
	var v map[string]any
	if err = json.Unmarshal(rr.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if len(v) != 1 || v["inspection_only"] != true || loads != 0 {
		t.Fatal("inspection view leaked/read editable config", v, loads)
	}
	req := adminapi.CacheInspectRequest{Selector: adminapi.CacheSelector{Kind: "host", Host: "secret-configured-host.example"}}
	if rr = postV1JSON(t, s, "/api/v1/cache/inspect", reader, "", req); rr.Code != 200 {
		t.Fatal(rr.Code, rr.Body.String())
	}
	for _, path := range []string{"/api/config", "/api/runtime/overview", "/api/stats", "/api/routes", "/api/admin/health"} {
		r = httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Authorization", "Bearer "+reader)
		rr = httptest.NewRecorder()
		s.routes().ServeHTTP(rr, r)
		if rr.Code != 403 {
			t.Fatal("inspection grant broadened", path, rr.Code)
		}
	}
	if rr = postV1JSON(t, s, "/api/v1/cache/purge/preview", reader, "", adminapi.CachePurgePreviewRequest{Selector: req.Selector}); rr.Code != 403 {
		t.Fatal("inspect implied purge", rr.Code)
	}
	for _, grant := range []string{"status:read", "config:read", "config:write", "config:apply", "cache:purge"} {
		policy, err := rbac.Build(true, rbac.RoleAdmin, map[string][]string{"broad": {grant}}, []rbac.PrincipalDef{{Name: "root", Role: rbac.RoleAdmin, Token: "root-inspection-console-token-32"}, {Name: "broad", Role: "broad", Token: "broad-console-projection-token-32"}}, "", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		s.UpdatePolicy(policy)
		r = httptest.NewRequest(http.MethodGet, "/api/traffic-controls", nil)
		r.Header.Set("Authorization", "Bearer broad-console-projection-token-32")
		rr = httptest.NewRecorder()
		s.routes().ServeHTTP(rr, r)
		var broad map[string]any
		if err = json.Unmarshal(rr.Body.Bytes(), &broad); err != nil {
			t.Fatal(err)
		}
		if rr.Code != 200 || broad["cache"] == nil || broad["inspection_only"] != nil {
			t.Fatalf("existing %s projection changed: %d %v", grant, rr.Code, broad)
		}
	}

}
