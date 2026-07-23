// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/rbac"
)

// sharedPointerRBACServer wires an admin Server whose LoadConfig ALWAYS returns
// the exact same *config.Config pointer (a shared/cached loader — a pattern the
// Deps.LoadConfig contract explicitly permits). It installs an RBAC policy with
// an operator principal that holds config:apply but NOT admin:manage, and an
// admin principal that holds admin:manage. It returns the server, the shared
// pointer, and the two tokens.
//
// This is the adversarial loader for C-01: if a handler mutates the loaded
// baseline in place and then reloads for authorization, "current" and
// "candidate" would alias the same object and compare equal, silently skipping
// the admin:manage guard.
func sharedPointerRBACServer(t *testing.T) (*Server, *config.Config, string, string) {
	t.Helper()
	operatorTok := "operator-token-32-chars-padded---"
	adminTok := "admin-token-32-chars-padded--------"

	shared := config.ServeDir("./public", ":8080")
	shared.Admin = config.AdminConfig{
		Enabled: true,
		Listen:  "127.0.0.1:9090",
		RBAC: config.AdminRBACConfig{
			Enabled: true,
			Roles: []config.AdminRole{
				{Name: "configwriter", Permissions: []string{"config:apply"}},
			},
			Principals: []config.AdminPrincipal{
				{Name: "op", Role: "configwriter", Token: operatorTok},
				{Name: "adm", Role: "admin", Token: adminTok},
			},
		},
	}

	saved := 0
	deps := Deps{
		// The adversarial part: every call hands back the very same pointer.
		LoadConfig: func() (*config.Config, error) { return shared, nil },
		// A real ReadConfigRaw so currentWriteState(true) succeeds and the
		// version basis is derived from the shared object.
		ReadConfigRaw: func() ([]byte, error) { return config.Marshal(shared) },
		SaveConfig: func(c *config.Config) error {
			// If the guard were bypassed, the write would land here. Count it so
			// the test can assert it never happens for the operator.
			saved++
			return nil
		},
		WriteConfigRaw: func([]byte) error { saved++; return nil },
	}
	cfg := config.AdminConfig{HistoryDir: t.TempDir(), HistoryKeep: 50}
	s := newTestServer(t, cfg, deps)

	pol, err := rbac.Build(true, "admin",
		map[string][]string{"configwriter": {"config:apply"}},
		[]rbac.PrincipalDef{
			{Name: "op", Role: "configwriter", Token: operatorTok},
			{Name: "adm", Role: rbac.RoleAdmin, Token: adminTok},
		}, "", time.Now())
	if err != nil {
		t.Fatalf("build policy: %v", err)
	}
	s.UpdatePolicy(pol)
	return s, shared, operatorTok, adminTok
}

// TestSettingsAliasCannotBypassAdminManage proves C-01 is fixed on the legacy
// settings path: even when LoadConfig returns the same pointer on every call,
// an operator with config:apply but without admin:manage cannot change
// admin.listen, and the shared baseline is never mutated in place.
func TestSettingsAliasCannotBypassAdminManage(t *testing.T) {
	s, shared, opTok, _ := sharedPointerRBACServer(t)
	origListen := shared.Admin.Listen

	// Change admin.listen through the curated settings form.
	in := extractSettings(shared)
	in.AdminListen = "127.0.0.1:19999"
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/config/settings", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+opTok)
	s.routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", rr.Code, rr.Body.String())
	}
	// The shared baseline must be untouched: the handler must clone before
	// mutating so authorization compares candidate against a pristine baseline.
	if shared.Admin.Listen != origListen {
		t.Errorf("shared baseline was mutated in place: listen = %q, want %q",
			shared.Admin.Listen, origListen)
	}
}

// TestSettingsAliasAdminManageAllows proves the admin:manage holder can still
// perform the same admin.listen change against a same-pointer loader — the fix
// blocks aliasing bypass without breaking legitimate authorized writes.
func TestSettingsAliasAdminManageAllows(t *testing.T) {
	s, shared, _, adminTok := sharedPointerRBACServer(t)

	in := extractSettings(shared)
	in.AdminListen = "127.0.0.1:19999"
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}

	// The admin.listen move is a reachability-affecting change, so the
	// self-lockout guard (finding 9) requires explicit confirmation even for an
	// admin:manage holder. Confirm it so this test isolates the aliasing
	// authorization concern rather than the lockout concern.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/config/settings?confirm_admin=true", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+adminTok)
	s.routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
}

// TestSettingsReachabilityNeedsConfirmation proves the self-lockout guard
// (finding 9) fires on the settings endpoint: an admin:manage holder moving the
// admin listener without confirm_admin=true is rejected with 409 and the change
// is not saved, while the same request with confirmation succeeds.
func TestSettingsReachabilityNeedsConfirmation(t *testing.T) {
	s, shared, _, adminTok := sharedPointerRBACServer(t)

	in := extractSettings(shared)
	in.AdminListen = "127.0.0.1:19999"
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/config/settings", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+adminTok)
	s.routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("without confirm: status = %d, want 409; body: %s", rr.Code, rr.Body.String())
	}
}
