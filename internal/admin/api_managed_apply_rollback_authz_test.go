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

// Raw credentials for the rollback-scoping authorization tests. Each is padded
// past rbac.MinTokenLen and is distinct so no two principals share a token ID.
const (
	rbRollbackOnlyTok = "rollback-only-token-padded-32ch--"
	rbHistoryReadTok  = "history-read-only-token-padded32-"
	rbViewerTok       = "viewer-authz-token-padded-32char-"
	rbAdminTok        = "admin-authz-token-padded-32chars-"
	rbOtherOwnerTok   = "other-owner-token-padded-32chars-"
)

// managedApplyRollbackAuthzServer builds a routes() stack under RBAC proving the
// N-01 scoping contract for /api/config/applies/{id}. Principals:
//
//   - admin         (wildcard; satisfies the mandatory admin-capable role)
//   - rollback-only (history:read + history:raw + history:rollback ONLY)
//   - history-read  (history:read ONLY — never admitted to the result route)
//   - viewer        (status:read — full read of any record)
//
// The ledger is seeded so the rollback-only principal owns exactly one rollback
// record (rl_2), owns an unrelated apply record it must NOT read (rl_3), and a
// rollback owned by another principal that it must NOT read (rl_4).
func managedApplyRollbackAuthzServer(t *testing.T) http.Handler {
	t.Helper()
	reg := NewManagedApplyRegistry(0, 0)
	rollbackOwner := rbac.TokenDigest(rbRollbackOnlyTok)[:12]
	otherOwner := rbac.TokenDigest(rbOtherOwnerTok)[:12]

	// rl_2: the rollback-only principal's OWN rollback → readable by it.
	_ = reg.Complete(ManagedApplyRecord{
		ID:           "rl_2",
		Operation:    ApplyOperationRollback,
		OwnerTokenID: rollbackOwner,
		Result:       ConfigApplyResult{ApplyID: "rl_2"},
	})
	// rl_3: an unrelated config.apply the principal owns but may NOT read — the
	// operation is not a rollback.
	_ = reg.Complete(ManagedApplyRecord{
		ID:           "rl_3",
		Operation:    ApplyOperationConfigApply,
		OwnerTokenID: rollbackOwner,
		Result:       ConfigApplyResult{ApplyID: "rl_3"},
	})
	// rl_4: a rollback owned by a DIFFERENT principal → not readable by the
	// rollback-only principal.
	_ = reg.Complete(ManagedApplyRecord{
		ID:           "rl_4",
		Operation:    ApplyOperationRollback,
		OwnerTokenID: otherOwner,
		Result:       ConfigApplyResult{ApplyID: "rl_4"},
	})

	customRoles := map[string][]string{
		"rollback-only": {
			string(rbac.HistoryRead),
			string(rbac.HistoryReadRaw),
			string(rbac.HistoryRollback),
		},
		"history-read": {string(rbac.HistoryRead)},
	}
	principals := []rbac.PrincipalDef{
		{Name: "admin", Role: rbac.RoleAdmin, Token: rbAdminTok},
		{Name: "rollback-only", Role: "rollback-only", Token: rbRollbackOnlyTok},
		{Name: "history-read", Role: "history-read", Token: rbHistoryReadTok},
		{Name: "viewer", Role: rbac.RoleViewer, Token: rbViewerTok},
	}
	pol, err := rbac.Build(true, "admin", customRoles, principals, "", timeNow())
	if err != nil {
		t.Fatalf("build policy: %v", err)
	}

	s := newTestServer(t, config.AdminConfig{}, Deps{ManagedApplies: reg})
	s.UpdatePolicy(pol)
	return s.routes()
}

// TestManagedApplyGet_RollbackOnlyScoped proves N-01: a rollback-only custom
// role can poll the exact result of the rollback it owns, cannot read an
// unrelated apply or a rollback owned by another principal, and a
// history-read-only role remains forbidden entirely — while status:read and
// admin retain unrestricted read (regression guard).
func TestManagedApplyGet_RollbackOnlyScoped(t *testing.T) {
	h := managedApplyRollbackAuthzServer(t)

	cases := []struct {
		name     string
		token    string
		id       string
		wantCode int
	}{
		{"rollback_only_reads_own_rollback", rbRollbackOnlyTok, "rl_2", http.StatusOK},
		{"rollback_only_denied_unrelated_apply", rbRollbackOnlyTok, "rl_3", http.StatusForbidden},
		{"rollback_only_denied_foreign_rollback", rbRollbackOnlyTok, "rl_4", http.StatusForbidden},
		{"history_read_only_forbidden", rbHistoryReadTok, "rl_2", http.StatusForbidden},
		{"status_read_reads_any", rbViewerTok, "rl_3", http.StatusOK},
		{"admin_reads_any", rbAdminTok, "rl_4", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/config/applies/"+tc.id, nil)
			req.Header.Set("Authorization", "Bearer "+tc.token)
			h.ServeHTTP(rr, req)
			if rr.Code != tc.wantCode {
				t.Fatalf("code = %d, want %d (body %s)", rr.Code, tc.wantCode, rr.Body.String())
			}
		})
	}
}

// TestManagedApplyGet_OwnershipDenialBodyIsHonest proves the ownership/scope
// denial for a rollback-only principal reports that the record is inaccessible
// to the current credential — not that history:rollback is required, which the
// caller already holds and was admitted through. The body names no permission
// the caller has and leaks neither the record nor another principal.
func TestManagedApplyGet_OwnershipDenialBodyIsHonest(t *testing.T) {
	h := managedApplyRollbackAuthzServer(t)

	// rl_4 is a rollback owned by a DIFFERENT principal: an ownership denial,
	// not a missing permission.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/config/applies/rl_4", nil)
	req.Header.Set("Authorization", "Bearer "+rbRollbackOnlyTok)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403 (%s)", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] != "forbidden" {
		t.Errorf("error = %v, want forbidden", body["error"])
	}
	if msg, _ := body["message"].(string); msg == "" {
		t.Error("expected an explanatory message about record accessibility")
	}
	// The denial must not claim a permission the caller already holds, nor leak
	// the record or another principal.
	for _, forbidden := range []string{"required", "role", "principal", "result", "state", "id"} {
		if _, present := body[forbidden]; present {
			t.Errorf("ownership 403 body leaked/misreported %q", forbidden)
		}
	}
}
