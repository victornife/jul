// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/config"
	"jul/internal/rbac"
)

// TestManagedApplyGet_ResponseRules exercises the GET /api/config/applies/{id}
// response contract: 200 for terminal, 202 for pending, 404 for unknown, 400
// for a malformed ID, and Cache-Control: no-store on every response (AC-02).
func TestManagedApplyGet_ResponseRules(t *testing.T) {
	reg := NewManagedApplyRegistry(0, 0)
	_ = reg.BeginPending(ManagedApplyRecord{ID: "rl_1", Operation: ApplyOperationConfigApply})
	_ = reg.Complete(ManagedApplyRecord{
		ID:        "rl_2",
		Operation: ApplyOperationPatchApply,
		Result:    ConfigApplyResult{ApplyID: "rl_2"},
	})

	s := newTestServer(t, config.AdminConfig{}, Deps{ManagedApplies: reg})
	h := s.routes()

	cases := []struct {
		name     string
		id       string
		wantCode int
	}{
		{"terminal", "rl_2", http.StatusOK},
		{"pending", "rl_1", http.StatusAccepted},
		{"unknown", "rl_9999", http.StatusNotFound},
		{"invalid", "rl_bad", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/config/applies/"+tc.id, nil)
			h.ServeHTTP(rr, req)
			if rr.Code != tc.wantCode {
				t.Fatalf("code = %d, want %d (body %s)", rr.Code, tc.wantCode, rr.Body.String())
			}
			if got := rr.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", got)
			}
		})
	}
}

// TestManagedApplyGet_TerminalBody proves the terminal record is serialized with
// its state and result and omits actor/source IP (AC-02 public projection).
func TestManagedApplyGet_TerminalBody(t *testing.T) {
	reg := NewManagedApplyRegistry(0, 0)
	_ = reg.Complete(ManagedApplyRecord{
		ID:                "rl_5",
		Operation:         ApplyOperationRollback,
		Result:            ConfigApplyResult{ApplyID: "rl_5"},
		HistorySnapshotID: "snap-1",
	})

	s := newTestServer(t, config.AdminConfig{}, Deps{ManagedApplies: reg})
	h := s.routes()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/config/applies/rl_5", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}

	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["state"] != string(ManagedApplyTerminal) {
		t.Errorf("state = %v, want terminal", got["state"])
	}
	if got["operation"] != string(ApplyOperationRollback) {
		t.Errorf("operation = %v, want %s", got["operation"], ApplyOperationRollback)
	}
	if got["history_snapshot_id"] != "snap-1" {
		t.Errorf("history_snapshot_id = %v, want snap-1", got["history_snapshot_id"])
	}
	// The public projection must never leak actor or source IP.
	for _, forbidden := range []string{"actor", "source_ip", "token_digest"} {
		if _, present := got[forbidden]; present {
			t.Errorf("public record leaked %q", forbidden)
		}
	}
}

// TestManagedApplyGet_NilRegistry proves a nil ledger yields 404 for all IDs
// rather than panicking (AC-02).
func TestManagedApplyGet_NilRegistry(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	h := s.routes()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/config/applies/rl_1", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rr.Code)
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

// managedApplyRBACServer builds a server whose exact-result endpoint is served
// through the full routes() stack under an RBAC policy. It installs four
// principals proving the AnyPermissions authorization contract for
// /api/config/applies/{id} (AC-02):
//
//   - admin        (wildcard, satisfies the mandatory admin-capable requirement)
//   - viewer       (status:read only)
//   - automation   (custom role granting ONLY config:apply)
//   - metrics-only (custom role granting an unrelated permission)
func managedApplyRBACServer(t *testing.T) http.Handler {
	t.Helper()
	reg := NewManagedApplyRegistry(0, 0)
	_ = reg.Complete(ManagedApplyRecord{
		ID:        "rl_2",
		Operation: ApplyOperationConfigApply,
		Result:    ConfigApplyResult{ApplyID: "rl_2"},
	})

	customRoles := map[string][]string{
		"automation":   {string(rbac.ConfigApply)},
		"metrics-only": {string(rbac.MetricsRead)},
	}
	principals := []rbac.PrincipalDef{
		{Name: "admin", Role: rbac.RoleAdmin, Token: "admin-token-32-chars-padded------"},
		{Name: "viewer", Role: rbac.RoleViewer, Token: "viewer-token-32-chars-padded-----"},
		{Name: "automation", Role: "automation", Token: "automation-token-32-chars-pad----"},
		{Name: "metrics", Role: "metrics-only", Token: "metrics-token-32-chars-padded----"},
	}
	pol, err := rbac.Build(true, "admin", customRoles, principals, "", timeNow())
	if err != nil {
		t.Fatalf("build policy: %v", err)
	}

	s := newTestServer(t, config.AdminConfig{}, Deps{ManagedApplies: reg})
	s.UpdatePolicy(pol)
	return s.routes()
}

// TestManagedApplyGet_AnyPermissionAuthorizes proves the exact-result endpoint
// authorizes EITHER status:read OR config:apply through requireAnyPermission,
// so a config:apply automation principal that lacks status:read can still poll
// the terminal result of its own apply (AC-02). It also confirms status:read is
// still accepted and that an unrelated permission is forbidden with the
// structured required_any 403 body.
func TestManagedApplyGet_AnyPermissionAuthorizes(t *testing.T) {
	h := managedApplyRBACServer(t)

	cases := []struct {
		name     string
		token    string
		wantCode int
	}{
		{"status_read_viewer", "viewer-token-32-chars-padded-----", http.StatusOK},
		{"config_apply_automation", "automation-token-32-chars-pad----", http.StatusOK},
		{"unrelated_permission", "metrics-token-32-chars-padded----", http.StatusForbidden},
		{"no_token", "", http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/config/applies/rl_2", nil)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			h.ServeHTTP(rr, req)
			if rr.Code != tc.wantCode {
				t.Fatalf("code = %d, want %d (body %s)", rr.Code, tc.wantCode, rr.Body.String())
			}
		})
	}
}

// TestManagedApplyGet_ForbiddenBodyListsAcceptedPermissions proves the 403
// emitted when a principal holds none of the accepted permissions is the
// structured JSON body listing the accepted permissions under required_any,
// without leaking any other principal or token information.
func TestManagedApplyGet_ForbiddenBodyListsAcceptedPermissions(t *testing.T) {
	h := managedApplyRBACServer(t)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/config/applies/rl_2", nil)
	req.Header.Set("Authorization", "Bearer metrics-token-32-chars-padded----")
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403 (body %s)", rr.Code, rr.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["error"] != "forbidden" {
		t.Errorf("error = %v, want forbidden", got["error"])
	}
	anyRaw, ok := got["required_any"].([]any)
	if !ok {
		t.Fatalf("required_any missing or not a list: %v", got["required_any"])
	}
	want := map[string]bool{string(rbac.StatusRead): true, string(rbac.ConfigApply): true}
	if len(anyRaw) != len(want) {
		t.Fatalf("required_any = %v, want the two accepted permissions", anyRaw)
	}
	for _, v := range anyRaw {
		if !want[v.(string)] {
			t.Errorf("required_any contains unexpected permission %v", v)
		}
	}
	// The principal is authenticated, so its own role is reported, but no other
	// principal/token information may leak.
	for _, forbidden := range []string{"token_digest", "token_id", "principals"} {
		if _, present := got[forbidden]; present {
			t.Errorf("403 body leaked %q", forbidden)
		}
	}
}
