// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"encoding/json"
	"jul/internal/config"
	"jul/internal/rbac"
	"jul/internal/versionid"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicIdentifiersProtectIdentityAndAllAuditSinks(t *testing.T) {
	key, _ := versionid.New(bytes.Repeat([]byte{7}, 32))
	other, _ := versionid.New(bytes.Repeat([]byte{8}, 32))
	sink := filepath.Join(t.TempDir(), "audit.jsonl")
	s := newTestServer(t, config.AdminConfig{AuditLogFile: sink}, Deps{EffectiveVersions: key})
	s2 := newTestServer(t, config.AdminConfig{}, Deps{EffectiveVersions: other})
	for _, private := range []string{"a1b2c3d4e5f6", "oidc:0123456789abcdef", "cid1:malformed"} {
		public := s.publicTokenID(private)
		if strings.Contains(public, private) || s2.publicTokenID(private) == public || s.publicTokenID(private) != public || s.publicTokenID(public) != public {
			t.Fatal("public ID leaks or loses scope/idempotence")
		}
		id := rbac.Identity{Principal: "operator", Role: "viewer", TokenID: private, TokenDigest: "private-only"}
		req := httptest.NewRequest("GET", "/api/admin/me", nil)
		req = req.WithContext(rbac.WithIdentity(req.Context(), id))
		rr := httptest.NewRecorder()
		s.handleIdentity(rr, req)
		var out identityResponse
		json.Unmarshal(rr.Body.Bytes(), &out)
		if out.TokenID != public {
			t.Fatal("identity emitted private fingerprint")
		}
		s.recordAudit(req, "config.apply", "config", "success", "")
		if id.TokenID != private {
			t.Fatal("private ownership key changed")
		}
	}
	for _, path := range []string{"/api/audit", "/api/audit/export?format=json", "/api/audit/export?format=csv"} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if path == "/api/audit" {
			s.handleAudit(rr, req)
		} else {
			s.handleAuditExport(rr, req)
		}
		if rr.Code != 200 || strings.Contains(rr.Body.String(), "a1b2c3d4e5f6") || !strings.Contains(rr.Body.String(), "cid1:") {
			t.Fatal("audit re-export leaks fingerprint")
		}
	}
	persisted, err := os.ReadFile(sink)
	if err != nil || strings.Contains(string(persisted), "a1b2c3d4e5f6") || !strings.Contains(string(persisted), "cid1:") {
		t.Fatal("durable audit leaks fingerprint", err)
	}
	for _, sentinel := range []string{"", "(legacy)"} {
		if s.publicTokenID(sentinel) != sentinel {
			t.Fatal("sentinel changed")
		}
	}
	// Old private IDs retained by an embedding are protected on re-export too.
	s.audit.publicTokenID = nil
	s.audit.record(AuditEvent{TokenID: "old-private-id"})
	rr := httptest.NewRecorder()
	s.handleAudit(rr, httptest.NewRequest("GET", "/api/audit", nil))
	if strings.Contains(rr.Body.String(), "old-private-id") {
		t.Fatal("legacy retained ID re-exported")
	}
}
