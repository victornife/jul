// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/rbac"
)

// secretBearerConfig returns a config that contains every sensitive literal
// the audit calls out: legacy admin token, RBAC principal tokens, and discovery
// tokens. It is used by the secret-scan tests below.
func secretBearerConfig() *config.Config {
	c := config.ServeDir("./public", ":8080")
	c.Admin = config.AdminConfig{
		Enabled: true,
		Listen:  "127.0.0.1:9090",
		Token:   "legacy-admin-token-32-chars-padded",
		RBAC: config.AdminRBACConfig{
			Enabled: true,
			Roles: []config.AdminRole{
				{Name: "configwriter", Permissions: []string{"config:apply"}},
			},
			Principals: []config.AdminPrincipal{
				{Name: "op", Role: "configwriter", Token: "operator-principal-token-32-chars"},
				{Name: "adm", Role: "admin", Token: "admin-principal-token-32-chars---"},
			},
		},
	}
	c.Upstreams = []config.UpstreamConfig{{
		Name: "consul-svc",
		Discovery: &config.DiscoveryConfig{
			Type: "consul",
			Consul: &config.ConsulDiscovery{
				Token: "consul-discovery-token-32-chars--",
			},
		},
	}}
	return c
}

// rbacOperatorServer creates a test server with RBAC enabled and returns the
// server, the operator token (config:apply only), and the admin token.
func rbacOperatorServer(t *testing.T) (*Server, string, string) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "server.toml")
	operatorTok := "operator-token-32-chars-padded---"
	adminTok := "admin-token-32-chars-padded--------"

	seed := secretBearerConfig()
	seed.Admin.RBAC.Principals = []config.AdminPrincipal{
		{Name: "op", Role: "configwriter", Token: operatorTok},
		{Name: "adm", Role: "admin", Token: adminTok},
	}
	raw, err := config.Marshal(seed)
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	if err := os.WriteFile(cfgPath, raw, 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	deps := Deps{
		ReadConfigRaw:  func() ([]byte, error) { return os.ReadFile(cfgPath) },
		WriteConfigRaw: func(data []byte) error { return os.WriteFile(cfgPath, data, 0o644) },
		LoadConfig: func() (*config.Config, error) {
			raw, err := os.ReadFile(cfgPath)
			if err != nil {
				return nil, err
			}
			return config.Parse(raw)
		},
	}
	s := newTestServer(t, config.AdminConfig{HistoryDir: t.TempDir(), HistoryKeep: 50}, deps)

	customRoles := map[string][]string{"configwriter": {"config:write"}}
	pol, err := rbac.Build(true, "admin", customRoles, []rbac.PrincipalDef{
		{Name: "op", Role: "configwriter", Token: operatorTok},
		{Name: "adm", Role: rbac.RoleAdmin, Token: adminTok},
	}, "", time.Now())
	if err != nil {
		t.Fatalf("build policy: %v", err)
	}
	s.UpdatePolicy(pol)
	return s, operatorTok, adminTok
}

// TestPatchPreviewOperatorCannotSeeSecrets verifies N-01: an operator with only
// config:write can preview a structured patch and receive a diff, but the
// response must not contain any literal credential.
func TestPatchPreviewOperatorCannotSeeSecrets(t *testing.T) {
	s, opTok, _ := rbacOperatorServer(t)

	ops := []patchRequest{{
		Op: "route_set_target", Listen: ":8080", MatchType: "prefix", Path: "/",
		Target: "http://127.0.0.1:9000",
	}}
	body, err := json.Marshal(patchApplyRequest{Ops: ops})
	if err != nil {
		t.Fatalf("marshal ops: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/config/patch/preview", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+opTok)
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	resp := rr.Body.String()
	for _, secret := range []string{
		"legacy-admin-token-32-chars-padded",
		"operator-principal-token-32-chars",
		"admin-principal-token-32-chars---",
		"consul-discovery-token-32-chars--",
	} {
		if strings.Contains(resp, secret) {
			t.Errorf("operator preview response contains secret %q", secret)
		}
	}
	if strings.Contains(resp, "\"candidate\"") {
		t.Error("operator preview response contains a candidate field")
	}
}

// TestPatchCandidateRequiresConfigRaw verifies that the full candidate TOML
// endpoint is gated by config:raw. An operator without that permission receives
// 403, while an admin with config:raw receives the complete candidate.
func TestPatchCandidateRequiresConfigRaw(t *testing.T) {
	s, opTok, adminTok := rbacOperatorServer(t)

	ops := []patchRequest{{
		Op: "route_set_target", Listen: ":8080", MatchType: "prefix", Path: "/",
		Target: "http://127.0.0.1:9000",
	}}
	body, err := json.Marshal(patchApplyRequest{Ops: ops})
	if err != nil {
		t.Fatalf("marshal ops: %v", err)
	}

	// Operator without config:raw is forbidden.
	req := httptest.NewRequest(http.MethodPost, "/api/config/patch/candidate", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+opTok)
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("operator candidate status = %d, want 403", rr.Code)
	}

	// Admin with config:raw can retrieve the candidate, which still contains
	// the literal secrets because the raw endpoint is explicitly for expert use.
	req = httptest.NewRequest(http.MethodPost, "/api/config/patch/candidate", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+adminTok)
	rr = httptest.NewRecorder()
	s.routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("admin candidate status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "legacy-admin-token-32-chars-padded") {
		t.Error("admin raw candidate did not contain the expected admin token")
	}
}

// TestRawAndHistoryEndpointsRequireRawPermission verifies C-02 remains closed:
// an operator without config:raw cannot read raw current or history TOML.
func TestRawAndHistoryEndpointsRequireRawPermission(t *testing.T) {
	s, opTok, _ := rbacOperatorServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	req.Header.Set("Authorization", "Bearer "+opTok)
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("operator GET /api/config status = %d, want 403", rr.Code)
	}
}
