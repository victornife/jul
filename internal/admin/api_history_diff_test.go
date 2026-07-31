// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/config"
	"jul/internal/rbac"
)

// historyDiffAuthzServer builds a routes() stack under RBAC with a seeded
// history snapshot, proving the N-02 contract for GET
// /api/config/history/{id}/diff: a least-privilege rollback-only role can
// preview what a rollback would change, a role that can read history but cannot
// roll back is forbidden, and the snapshot is diffed server-side against the
// running config (never a submitted candidate). It returns the handler, the
// seeded snapshot id, and the raw bytes of the running config.
func historyDiffAuthzServer(t *testing.T) (http.Handler, string, []byte) {
	t.Helper()

	currentRaw := validTOML(t, "./current", ":8080")
	current, err := config.Parse(currentRaw)
	if err != nil {
		t.Fatalf("parse current: %v", err)
	}

	customRoles := map[string][]string{
		// A rollback-only role can read history and roll back, but cannot write
		// config — exactly the role that /api/config/diff (config:write) locks
		// out (N-02).
		"rollback-only": {
			string(rbac.HistoryRead),
			string(rbac.HistoryReadRaw),
			string(rbac.HistoryRollback),
		},
		// history-read can read snapshots but must NOT reach the rollback diff:
		// it lacks history:rollback.
		"history-read": {
			string(rbac.HistoryRead),
			string(rbac.HistoryReadRaw),
		},
	}
	principals := []rbac.PrincipalDef{
		{Name: "admin", Role: rbac.RoleAdmin, Token: rbAdminTok},
		{Name: "rollback-only", Role: "rollback-only", Token: rbRollbackOnlyTok},
		{Name: "history-read", Role: "history-read", Token: rbHistoryReadTok},
	}
	pol, err := rbac.Build(true, "admin", customRoles, principals, "", timeNow())
	if err != nil {
		t.Fatalf("build policy: %v", err)
	}

	cfg := config.AdminConfig{HistoryDir: t.TempDir(), HistoryKeep: 50}
	deps := Deps{LoadConfig: func() (*config.Config, error) { return current, nil }}
	s := newTestServer(t, cfg, deps)
	s.UpdatePolicy(pol)

	// Seed a snapshot that differs from the running config so the diff is
	// non-empty.
	id, err := s.hist.snapshot(validTOML(t, "./snapshot", ":9090"))
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return s.routes(), id, currentRaw
}

// TestConfigHistoryDiff_RollbackOnlyCanPreview proves N-02: the rollback-scoped
// history diff endpoint admits history:rollback (and admin) and returns a
// structured diff, while a role that can read history but not roll back is
// forbidden. An unknown id is a 404.
func TestConfigHistoryDiff_RollbackOnlyCanPreview(t *testing.T) {
	h, id, _ := historyDiffAuthzServer(t)

	cases := []struct {
		name     string
		token    string
		id       string
		wantCode int
	}{
		{"rollback_only_previews", rbRollbackOnlyTok, id, http.StatusOK},
		{"admin_previews", rbAdminTok, id, http.StatusOK},
		{"history_read_only_forbidden", rbHistoryReadTok, id, http.StatusForbidden},
		{"unknown_id_not_found", rbRollbackOnlyTok, "20200101-000000-dead", http.StatusNotFound},
		{"no_token_unauthorized", "", id, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/config/history/"+tc.id+"/diff", nil)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			h.ServeHTTP(rr, req)
			if rr.Code != tc.wantCode {
				t.Fatalf("code = %d, want %d (body %s)", rr.Code, tc.wantCode, rr.Body.String())
			}
			if tc.wantCode == http.StatusOK {
				var out ConfigDiff
				if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
					t.Fatalf("decode diff: %v", err)
				}
				if out.Summary == "" {
					t.Error("expected a non-empty diff summary between snapshot and running config")
				}
			}
		})
	}
}

// TestConfigHistoryDiff_IgnoresRequestBody proves the endpoint never diffs a
// submitted candidate: a GET carrying a body equal to the running config still
// diffs the STORED snapshot (which differs), so the response remains non-empty.
// A POST is rejected outright, so no candidate can be submitted at all.
func TestConfigHistoryDiff_IgnoresRequestBody(t *testing.T) {
	h, id, currentRaw := historyDiffAuthzServer(t)

	// A body identical to the running config would yield an empty diff if it
	// were used; the endpoint must ignore it and diff the stored snapshot.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/config/history/"+id+"/diff", strings.NewReader(string(currentRaw)))
	req.Header.Set("Authorization", "Bearer "+rbRollbackOnlyTok)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	var out ConfigDiff
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode diff: %v", err)
	}
	if out.Summary == "" {
		t.Fatal("body was used instead of the stored snapshot: diff is empty")
	}

	// A POST cannot submit a candidate to this route.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/config/history/"+id+"/diff", strings.NewReader(string(currentRaw)))
	req.Header.Set("Authorization", "Bearer "+rbRollbackOnlyTok)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST code = %d, want 405", rr.Code)
	}
}
