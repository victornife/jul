// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/rbac"
)

// TestManagedApplyGet_DisabledOrExpiredCredential401 proves the exact-result
// endpoint fails closed with 401 for a credential that authenticates as a known
// principal but whose principal is disabled or expired (AC-02, §2.9). This is
// distinct from an unknown/absent token (also 401) and from an authenticated
// principal that merely lacks the permission (403): a disabled/expired
// credential must never reach the handler, and the structured body reports only
// that the principal is disabled or expired.
func TestManagedApplyGet_DisabledOrExpiredCredential401(t *testing.T) {
	reg := NewManagedApplyRegistry(0, 0)
	_ = reg.Complete(ManagedApplyRecord{
		ID:        "rl_2",
		Operation: ApplyOperationConfigApply,
		Result:    ConfigApplyResult{ApplyID: "rl_2"},
	})

	const (
		disabledTok = "disabled-token-32-chars-padded---"
		expiredTok  = "expired-token-32-chars-padded----"
		adminTok    = "admin-token-32-chars-padded------"
	)
	past := timeNow().Add(-time.Hour)
	principals := []rbac.PrincipalDef{
		// A mandatory active admin-capable principal so the policy builds.
		{Name: "admin", Role: rbac.RoleAdmin, Token: adminTok},
		// Disabled principal: valid credential, principal switched off.
		{Name: "disabled-viewer", Role: rbac.RoleViewer, Token: disabledTok, Disabled: true},
		// Expired principal: valid credential, past its ExpiresAt.
		{Name: "expired-viewer", Role: rbac.RoleViewer, Token: expiredTok, ExpiresAt: past},
	}
	pol, err := rbac.Build(true, "admin", nil, principals, "", timeNow())
	if err != nil {
		t.Fatalf("build policy: %v", err)
	}

	s := newTestServer(t, config.AdminConfig{}, Deps{ManagedApplies: reg})
	s.UpdatePolicy(pol)
	h := s.Handler()

	for _, tc := range []struct {
		name  string
		token string
	}{
		{"disabled", disabledTok},
		{"expired", expiredTok},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/config/applies/rl_2", nil)
			req.Header.Set("Authorization", "Bearer "+tc.token)
			h.ServeHTTP(rr, req)

			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("code = %d, want 401 (body %s)", rr.Code, rr.Body.String())
			}
			// A disabled/expired credential must fail closed BEFORE the handler,
			// so the secret-free record is never served.
			if rr.Body.Len() > 0 {
				var body map[string]any
				if err := json.Unmarshal(rr.Body.Bytes(), &body); err == nil {
					if body["error"] != "unauthorized" {
						t.Errorf("error = %v, want unauthorized", body["error"])
					}
					// The 401 must not leak the record projection.
					for _, forbidden := range []string{"result", "state", "id", "token_digest"} {
						if _, present := body[forbidden]; present {
							t.Errorf("401 body leaked %q", forbidden)
						}
					}
				}
			}
		})
	}
}

// TestManagedApplyGet_BlockedRBAC503 proves the exact-result endpoint fails
// closed with 503 when the desired mode is RBAC (cfg.RBAC.Enabled) but no valid
// policy is installed (Blocked). The request must never fall through to
// legacy/open access, and the record is never served (AC-02, §2.9). This
// exercises the same single immutable auth snapshot that requireAnyPermission
// dispatches on.
func TestManagedApplyGet_BlockedRBAC503(t *testing.T) {
	reg := NewManagedApplyRegistry(0, 0)
	_ = reg.Complete(ManagedApplyRecord{
		ID:        "rl_2",
		Operation: ApplyOperationConfigApply,
		Result:    ConfigApplyResult{ApplyID: "rl_2"},
	})

	s := newTestServer(t, config.AdminConfig{}, Deps{ManagedApplies: reg})
	// Desired mode is RBAC but no policy is installed → Blocked (fail closed).
	s.UpdateAuth(config.AdminConfig{RBAC: config.AdminRBACConfig{Enabled: true}}, nil)
	h := s.Handler()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/config/applies/rl_2", nil)
	// Even with a bearer present, Blocked mode must not authenticate or serve.
	req.Header.Set("Authorization", "Bearer any-token-does-not-matter-here")
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503 (body %s)", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["error"] != "rbac_unavailable" {
		t.Errorf("error = %v, want rbac_unavailable", body["error"])
	}
	// The Blocked 503 must never expose the record projection.
	for _, forbidden := range []string{"result", "state", "operation"} {
		if _, present := body[forbidden]; present {
			t.Errorf("503 body leaked %q", forbidden)
		}
	}
}
