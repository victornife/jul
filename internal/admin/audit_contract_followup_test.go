// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/rbac"
)

func TestAuditCSVRetainsCredentialIdentityAdditively(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	req := httptest.NewRequest(http.MethodPost, "/api/config/raw", nil)
	req = req.WithContext(rbac.WithIdentity(req.Context(), rbac.Identity{Principal: "csv-operator", TokenID: "synthetic-public-id"}))
	s.recordAudit(req, "config.apply", "config", "success", "applied")
	// An unauthenticated/system event carries an empty credential ID.
	s.recordAudit(httptest.NewRequest(http.MethodGet, "/", nil), "system.event", "system", "success", "")
	rr := httptest.NewRecorder()
	s.handleAuditExport(rr, httptest.NewRequest(http.MethodGet, "/api/audit/export?format=csv", nil))
	rows, err := csv.NewReader(strings.NewReader(rr.Body.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"id", "time", "actor", "operation", "resource", "result", "detail", "source_ip", "token_id"}
	if len(rows) != 3 || !reflect.DeepEqual(rows[0], want) {
		t.Fatalf("CSV schema/rows = %v", rows)
	}
	if rows[1][8] != "" || rows[2][2] != "csv-operator" || rows[2][8] != "synthetic-public-id" {
		t.Fatalf("CSV loses credential attribution: %v", rows)
	}
	rr = httptest.NewRecorder()
	s.handleAuditExport(rr, httptest.NewRequest(http.MethodGet, "/api/audit/export", nil))
	var events []AuditEvent
	if err := json.Unmarshal(rr.Body.Bytes(), &events); err != nil {
		t.Fatal(err)
	}
	if events[1].TokenID != rows[2][8] {
		t.Fatalf("JSON/CSV identity differs: %v", events)
	}
}

func TestAuthenticationRejectionsProduceAnonymousAudit(t *testing.T) {
	const adminToken = "synthetic-admin-token-for-audit-only-0001"
	const deniedToken = "synthetic-denied-token-for-audit-only-001"
	for _, endpoint := range []string{"/api/admin/me", "/api/status", "/api/v1/config/pending-restart"} {
		for _, mode := range []string{"missing", "invalid", "disabled", "expired", "legacy invalid"} {
			t.Run(endpoint+" "+mode, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "audit.jsonl")
				cfg := config.AdminConfig{Token: adminToken, AuditLogFile: path}
				s := newTestServer(t, cfg, Deps{})
				if mode != "legacy invalid" {
					now := time.Now()
					def := rbac.PrincipalDef{Name: "denied-user", Role: rbac.RoleViewer, Token: deniedToken, Disabled: mode == "disabled"}
					if mode == "expired" {
						def.ExpiresAt = now.Add(-time.Hour)
					}
					policy, err := rbac.Build(true, "admin", nil, []rbac.PrincipalDef{{Name: "admin", Role: rbac.RoleAdmin, Token: adminToken}, def}, "", now)
					if err != nil {
						t.Fatal(err)
					}
					s.UpdatePolicy(policy)
				}
				req := httptest.NewRequest(http.MethodGet, endpoint+"?synthetic-secret-query=never-audit", nil)
				req.RemoteAddr = "127.0.0.1:12345"
				if mode != "missing" {
					token := "synthetic-invalid-secret-0001"
					if mode == "disabled" || mode == "expired" {
						token = deniedToken
					}
					req.Header.Set("Authorization", "Bearer "+token)
				}
				req.Header.Set("X-Actor", "spoofed-actor")
				req.Header.Set("X-Token-ID", "spoofed-token")
				rr := httptest.NewRecorder()
				s.routes().ServeHTTP(rr, req)
				if rr.Code != http.StatusUnauthorized || rr.Header().Get("WWW-Authenticate") != "Bearer" {
					t.Fatalf("rejection changed: %d %v", rr.Code, rr.Header())
				}
				events := s.audit.snapshot("auth.fail", "failure", 0)
				if len(events) != 1 || events[0].Actor != "anonymous" || events[0].TokenID != "" || events[0].SourceIP != "127.0.0.1" {
					t.Fatalf("missing/false failure identity: %+v", events)
				}
				body, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var durable AuditEvent
				if err := json.Unmarshal(body, &durable); err != nil {
					t.Fatalf("durable event: %v, %q", err, body)
				}
				if durable.ID != events[0].ID || durable.Operation != "auth.fail" || durable.Actor != "anonymous" {
					t.Fatalf("durable event differs: %+v", durable)
				}
				for _, secret := range []string{adminToken, deniedToken, "synthetic-invalid-secret", "synthetic-secret-query", "spoofed-actor", "spoofed-token"} {
					if strings.Contains(string(body), secret) {
						t.Fatalf("audit leaked request data: %q", body)
					}
				}
			})
		}
	}
}

func TestSuccessfulAuthenticationAndAuthorizationDenialAreNotAuthFailures(t *testing.T) {
	const adminToken = "synthetic-valid-admin-token-for-audit-001"
	const viewerToken = "synthetic-valid-viewer-token-for-audit-01"
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	policy, err := rbac.Build(true, "admin", nil, []rbac.PrincipalDef{{Name: "admin", Role: rbac.RoleAdmin, Token: adminToken}, {Name: "viewer", Role: rbac.RoleViewer, Token: viewerToken}}, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s.UpdatePolicy(policy)
	for _, tc := range []struct {
		method, path, token string
		code                int
	}{{http.MethodGet, "/api/admin/me", adminToken, 200}, {http.MethodPost, "/cache/purge", viewerToken, 403}} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("Authorization", "Bearer "+tc.token)
		rr := httptest.NewRecorder()
		s.routes().ServeHTTP(rr, req)
		if rr.Code != tc.code {
			t.Fatalf("%s: status%d", tc.path, rr.Code)
		}
	}
	if events := s.audit.snapshot("auth.fail", "", 0); len(events) != 0 {
		t.Fatalf("false authentication failures: %+v", events)
	}
}
