// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/rbac"
)

// decodeIdentity issues GET /api/admin/me against the server's full route stack
// with the supplied bearer token and returns the decoded response and status.
func decodeIdentity(t *testing.T, s *Server, token string) (identityResponse, int) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/me", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, req)
	var out identityResponse
	if rr.Code == http.StatusOK {
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode /api/admin/me: %v (body %s)", err, rr.Body.String())
		}
	}
	return out, rr.Code
}

func TestIdentityEndpointRBACPrincipal(t *testing.T) {
	const opTok = "operator-token-32-chars-padded---"
	principals := []rbac.PrincipalDef{
		{Name: "op", Role: rbac.RoleOperator, Token: opTok},
		{Name: "root", Role: rbac.RoleAdmin, Token: "admin-token-32-chars-padded------"},
	}
	pol, err := rbac.Build(true, "admin", nil, principals, "", time.Now())
	if err != nil {
		t.Fatalf("build policy: %v", err)
	}
	s := newTestServer(t, config.AdminConfig{RBAC: config.AdminRBACConfig{Enabled: true}}, Deps{})
	s.UpdatePolicy(pol)

	got, code := decodeIdentity(t, s, opTok)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if got.Principal != "op" || got.Role != rbac.RoleOperator {
		t.Errorf("principal/role = %q/%q, want op/operator", got.Principal, got.Role)
	}
	if got.Legacy {
		t.Error("named principal must not be reported as legacy")
	}
	// token_id must be the public 12-hex id derived from the token, never the
	// raw token or its full digest.
	sum := sha256.Sum256([]byte(opTok))
	wantID := hex.EncodeToString(sum[:])[:12]
	if got.TokenID != wantID {
		t.Errorf("token_id = %q, want %q", got.TokenID, wantID)
	}
	// Permissions are the concrete operator set: config:apply present, but the
	// admin-only config:raw and admin:manage absent.
	if !slices.Contains(got.Permissions, "config:apply") || !slices.Contains(got.Permissions, "status:read") {
		t.Errorf("operator permissions missing expected grants: %v", got.Permissions)
	}
	if slices.Contains(got.Permissions, "config:raw") || slices.Contains(got.Permissions, "admin:manage") {
		t.Errorf("operator permissions leaked admin-only grants: %v", got.Permissions)
	}
}

func TestIdentityEndpointLegacyToken(t *testing.T) {
	const tok = "legacy-token-32-chars-padded-----"
	s := newTestServer(t, config.AdminConfig{Token: tok}, Deps{})

	got, code := decodeIdentity(t, s, tok)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if !got.Legacy {
		t.Error("legacy shared token must report legacy=true")
	}
	if got.Principal != "shared" {
		t.Errorf("principal = %q, want shared", got.Principal)
	}
	// The legacy identity holds the wildcard, so the concrete set is the whole
	// catalog including admin:manage.
	if !slices.Contains(got.Permissions, "admin:manage") {
		t.Errorf("legacy identity should expand to all permissions, got %v", got.Permissions)
	}
	// The token_id for legacy is the synthetic sentinel, never the raw token.
	if got.TokenID == tok {
		t.Error("token_id must never equal the raw token")
	}
}

func TestIdentityEndpointRejectsUnauthenticated(t *testing.T) {
	principals := []rbac.PrincipalDef{
		{Name: "root", Role: rbac.RoleAdmin, Token: "admin-token-32-chars-padded------"},
	}
	pol, err := rbac.Build(true, "admin", nil, principals, "", time.Now())
	if err != nil {
		t.Fatalf("build policy: %v", err)
	}
	s := newTestServer(t, config.AdminConfig{RBAC: config.AdminRBACConfig{Enabled: true}}, Deps{})
	s.UpdatePolicy(pol)

	if _, code := decodeIdentity(t, s, ""); code != http.StatusUnauthorized {
		t.Errorf("unauthenticated /api/admin/me = %d, want 401", code)
	}
	if _, code := decodeIdentity(t, s, "wrong-token-32-chars-padded------"); code != http.StatusUnauthorized {
		t.Errorf("bad-token /api/admin/me = %d, want 401", code)
	}
}

func TestIdentityEndpointMethodNotAllowed(t *testing.T) {
	const tok = "legacy-token-32-chars-padded-----"
	s := newTestServer(t, config.AdminConfig{Token: tok}, Deps{})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/me", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/admin/me = %d, want 405", rr.Code)
	}
}
