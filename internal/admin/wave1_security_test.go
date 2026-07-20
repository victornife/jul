// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/rbac"
)

// wave1Server builds an admin server with RBAC enabled and the three predefined
// test principals. It wires WriteConfigRaw to a no-op success and LoadConfig to
// the supplied current config so the object-level guard can compare candidates.
func wave1Server(t *testing.T, current *config.Config) (*Server, string, string, string) {
	t.Helper()
	adminTok := "admin-user-32-char-padding-test--"
	opTok := "operator-token-32-chars-padded---"
	viewTok := "viewer-token-32-chars-padded----"
	principals := []rbac.PrincipalDef{
		{Name: "admin-user", Role: rbac.RoleAdmin, Token: adminTok},
		{Name: "op", Role: rbac.RoleOperator, Token: opTok},
		{Name: "viewer", Role: rbac.RoleViewer, Token: viewTok},
	}
	pol, err := rbac.Build(true, "admin", nil, principals, "", time.Now())
	if err != nil {
		t.Fatalf("build policy: %v", err)
	}
	s := &Server{
		cfg:  config.AdminConfig{Token: "legacy-token-32-chars-padded--"},
		hist: newHistory(t.TempDir(), 10),
	}
	s.liveCfg.Store(&config.AdminConfig{Token: "legacy-token-32-chars-padded--"})
	s.UpdatePolicy(pol)
	s.deps.WriteConfigRaw = func([]byte) error { return nil }
	s.deps.ReadConfigRaw = func() ([]byte, error) {
		data, _ := config.Marshal(current)
		return data, nil
	}
	s.deps.LoadConfig = func() (*config.Config, error) { return current, nil }
	s.deps.SaveConfig = func(c *config.Config) error {
		current = c
		return nil
	}
	return s, adminTok, opTok, viewTok
}

func wave1Config(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Parse([]byte(`
[global]
log_level = "info"

[admin]
enabled = true
listen = "127.0.0.1:8080"
token = "admin-token-32-chars-padded--"

[[servers]]
listen = ":8081"
`))
	if err != nil {
		t.Fatalf("parse base config: %v", err)
	}
	return cfg
}

// TestWave1_ViewerCannotMutateRawConfig verifies C-01: a viewer with
// config:read cannot POST or PUT /api/config/raw even though the handler
// accepts those methods.
func TestWave1_ViewerCannotMutateRawConfig(t *testing.T) {
	cfg := wave1Config(t)
	s, _, _, viewTok := wave1Server(t, cfg)

	candidate := []byte(`
[global]
log_level = "info"

[admin]
enabled = true
listen = "127.0.0.1:8080"
token = "admin-token-32-chars-padded--"

[[servers]]
listen = ":8082"
`)

	for _, method := range []string{http.MethodPost, http.MethodPut} {
		req := httptest.NewRequest(method, "/api/config/raw", bytes.NewReader(candidate))
		req.Header.Set("Authorization", "Bearer "+viewTok)
		rr := httptest.NewRecorder()
		s.routes().ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Errorf("%s /api/config/raw: viewer got %d, want 403", method, rr.Code)
		}
	}
}

// TestWave1_ViewerCannotMutateSettingsForm verifies C-01 for the legacy
// settings form: a viewer cannot POST /api/config/settings.
func TestWave1_ViewerCannotMutateSettingsForm(t *testing.T) {
	cfg := wave1Config(t)
	s, _, _, viewTok := wave1Server(t, cfg)

	body := []byte(`{"log_level":"debug","cache_enabled":false}`)
	req := httptest.NewRequest(http.MethodPost, "/api/config/settings", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+viewTok)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("viewer POST /api/config/settings got %d, want 403", rr.Code)
	}
}

// TestWave1_ViewerCannotReadRawConfig verifies C-02: a viewer cannot read the
// raw TOML body from /api/config or /api/config/raw.
func TestWave1_ViewerCannotReadRawConfig(t *testing.T) {
	cfg := wave1Config(t)
	s, _, _, viewTok := wave1Server(t, cfg)

	for _, path := range []string{"/api/config", "/api/config/raw"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+viewTok)
		rr := httptest.NewRecorder()
		s.routes().ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Errorf("viewer GET %s got %d, want 403", path, rr.Code)
		}
	}
}

// TestWave1_ViewerCannotReadHistoryRaw verifies C-02: a viewer cannot retrieve
// the raw TOML of a historical snapshot.
func TestWave1_ViewerCannotReadHistoryRaw(t *testing.T) {
	cfg := wave1Config(t)
	s, _, _, viewTok := wave1Server(t, cfg)
	// Seed one history snapshot and resolve its actual timestamp ID.
	prev, _ := config.Marshal(cfg)
	id, err := s.hist.snapshot(prev)
	if err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	for _, path := range []string{"/api/history/get?id=" + id, "/api/config/history/" + id} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+viewTok)
		rr := httptest.NewRecorder()
		s.routes().ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Errorf("viewer GET %s got %d, want 403", path, rr.Code)
		}
	}
}

// TestWave1_OperatorCannotApplyAdminChange verifies H-01/C-01: an operator
// with config:apply cannot mutate the [admin] subtree through /api/config/raw.
func TestWave1_OperatorCannotApplyAdminChange(t *testing.T) {
	cfg := wave1Config(t)
	s, _, opTok, _ := wave1Server(t, cfg)

	candidate := []byte(`
[global]
log_level = "info"

[admin]
enabled = true
listen = "127.0.0.1:8080"
token = "rotated-admin-token-32-chars---"

[[servers]]
listen = ":8081"
`)

	req := httptest.NewRequest(http.MethodPost, "/api/config/raw", bytes.NewReader(candidate))
	req.Header.Set("Authorization", "Bearer "+opTok)
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("operator changing admin token got %d, want 403: %s", rr.Code, rr.Body.String())
	}
}

// TestWave1_OperatorCannotApplyAdminRateLimit verifies H-01: an operator
// cannot change an admin rate limit (a field missing from the old guard).
func TestWave1_OperatorCannotApplyAdminRateLimit(t *testing.T) {
	cfg := wave1Config(t)
	s, _, opTok, _ := wave1Server(t, cfg)

	candidate := []byte(`
[global]
log_level = "info"

[admin]
enabled = true
listen = "127.0.0.1:8080"
token = "admin-token-32-chars-padded--"
rate_limit_apply_per_min = 1

[[servers]]
listen = ":8081"
`)

	req := httptest.NewRequest(http.MethodPost, "/api/config/raw", bytes.NewReader(candidate))
	req.Header.Set("Authorization", "Bearer "+opTok)
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("operator changing admin rate limit got %d, want 403: %s", rr.Code, rr.Body.String())
	}
}

// TestWave1_AdminCanApplyAdminChange verifies that an admin can still mutate
// the [admin] subtree.
func TestWave1_AdminCanApplyAdminChange(t *testing.T) {
	cfg := wave1Config(t)
	s, adminTok, _, _ := wave1Server(t, cfg)

	candidate := []byte(`
[global]
log_level = "info"

[admin]
enabled = true
listen = "127.0.0.1:8080"
token = "rotated-admin-token-32-chars---"

[[servers]]
listen = ":8081"
`)

	req := httptest.NewRequest(http.MethodPost, "/api/config/raw", bytes.NewReader(candidate))
	req.Header.Set("Authorization", "Bearer "+adminTok)
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("admin changing admin token got %d, want 200: %s", rr.Code, rr.Body.String())
	}
}

// TestWave1_OperatorCanApplyOrdinaryChange verifies that an operator can still
// mutate non-admin configuration.
func TestWave1_OperatorCanApplyOrdinaryChange(t *testing.T) {
	cfg := wave1Config(t)
	s, _, opTok, _ := wave1Server(t, cfg)

	candidate := []byte(`
[global]
log_level = "info"

[admin]
enabled = true
listen = "127.0.0.1:8080"
token = "admin-token-32-chars-padded--"

[[servers]]
listen = ":8082"
`)

	req := httptest.NewRequest(http.MethodPost, "/api/config/raw", bytes.NewReader(candidate))
	req.Header.Set("Authorization", "Bearer "+opTok)
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("operator changing listen port got %d, want 200: %s", rr.Code, rr.Body.String())
	}
}

// TestWave1_OperatorCannotRollbackAdminChange verifies C-03: an operator with
// history:rollback cannot roll back to a snapshot that changes [admin].
func TestWave1_OperatorCannotRollbackAdminChange(t *testing.T) {
	cfg := wave1Config(t)
	s, _, opTok, _ := wave1Server(t, cfg)

	adminSnapshot := []byte(`
[global]
log_level = "info"

[admin]
enabled = true
listen = "127.0.0.1:8080"
token = "rotated-admin-token-32-chars---"

[[servers]]
listen = ":8081"
`)
	id, err := s.hist.snapshot(adminSnapshot)
	if err != nil {
		t.Fatalf("seed admin snapshot: %v", err)
	}

	body := []byte(`{"id":"` + id + `"}`)
	for _, path := range []string{"/api/history/rollback", "/api/config/rollback"} {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+opTok)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		s.routes().ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Errorf("operator rollback %s to admin snapshot got %d, want 403: %s", path, rr.Code, rr.Body.String())
		}
	}
}

// TestWave1_AdminCanRollbackAdminChange verifies that an admin can roll back to
// a snapshot containing admin changes.
func TestWave1_AdminCanRollbackAdminChange(t *testing.T) {
	cfg := wave1Config(t)
	s, adminTok, _, _ := wave1Server(t, cfg)

	adminSnapshot := []byte(`
[global]
log_level = "info"

[admin]
enabled = true
listen = "127.0.0.1:8080"
token = "rotated-admin-token-32-chars---"

[[servers]]
listen = ":8081"
`)
	id, err := s.hist.snapshot(adminSnapshot)
	if err != nil {
		t.Fatalf("seed admin snapshot: %v", err)
	}

	body := []byte(`{"id":"` + id + `"}`)
	for _, path := range []string{"/api/history/rollback", "/api/config/rollback"} {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+adminTok)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		s.routes().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("admin rollback %s to admin snapshot got %d, want 200: %s", path, rr.Code, rr.Body.String())
		}
	}
}
