// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"jul/internal/adminoidc/oidctest"
	"jul/internal/config"
	"jul/internal/rbac"
)

func TestOIDCAuditCapturesHistoricalAuthorityAcrossAllSinks(t *testing.T) {
	for _, groupMapping := range []bool{false, true} {
		t.Run(map[bool]string{false: "subject", true: "group"}[groupMapping], func(t *testing.T) {
			p, err := oidctest.New()
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			p.Configure("", map[string]any{"sub": "private-subject", "groups": []string{"private-group"}, "email": "private@example.invalid", "provider_name": "claim-provider", "role": "claim-role", "permissions": []string{"*"}})
			path := filepath.Join(t.TempDir(), "audit.jsonl")
			cfg := config.AdminConfig{Token: strings.Repeat("r", 32), RBAC: config.AdminRBACConfig{Enabled: true}, AuditLogFile: path}
			s := newTestServer(t, cfg, Deps{OIDCClient: p.Server.Client(), Cache: &fakePurger{}})
			srv := httptest.NewServer(s.Handler())
			defer srv.Close()
			cfg.OIDC = config.AdminOIDCConfig{Enabled: true, ProviderName: "Company identity", Issuer: p.URL(), ClientID: "jul", CallbackURL: srv.URL + "/auth/oidc/callback", Subjects: []config.AdminOIDCSubject{{Subject: "private-subject", Principal: "alice", Role: "deployment"}}}
			if groupMapping {
				cfg.OIDC.Subjects = nil
				cfg.OIDC.GroupClaim = "groups"
				cfg.OIDC.Groups = []config.AdminOIDCGroup{{Group: "private-group", Role: "deployment"}}
			}
			if err := p.RegisterClient(cfg.OIDC.ClientID, cfg.OIDC.CallbackURL); err != nil {
				t.Fatal(err)
			}
			policy := func(perms []string) *rbac.Policy {
				t.Helper()
				pol, err := rbac.Build(true, "admin", map[string][]string{"deployment": perms}, nil, cfg.Token, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				return pol
			}
			s.UpdateAuth(cfg, policy([]string{"cache:purge"}))
			jar, _ := cookiejar.New(nil)
			client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			call := func(method, raw string) *http.Response {
				t.Helper()
				if strings.HasPrefix(raw, "/") {
					raw = srv.URL + raw
				}
				req, _ := http.NewRequest(method, raw, nil)
				req.Header.Set("Origin", srv.URL)
				req.Header.Set("X-Jul-Console", "1")
				resp, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { resp.Body.Close() })
				return resp
			}
			start := call("POST", "/auth/oidc/login")
			authorize := call("GET", start.Header.Get("Location"))
			callback, _ := url.Parse(authorize.Header.Get("Location"))
			if call("GET", callback.String()).Header.Get("Location") != "/" {
				t.Fatal("login failed")
			}
			// Capture the admitted identity, then finish work after a publication
			// changes both provider display name and the same custom role's grants.
			admitted := httptest.NewRequest("POST", srv.URL+"/api/config/apply", nil)
			admitted.Header.Set("Origin", srv.URL)
			admitted.Header.Set("X-Jul-Console", "1")
			origin, _ := url.Parse(srv.URL)
			var secrets []string
			for _, c := range jar.Cookies(origin) {
				admitted.AddCookie(c)
				secrets = append(secrets, c.Value)
			}
			id, _, err := s.authState.Load().oidc.Authenticate(admitted)
			if err != nil {
				t.Fatal(err)
			}
			admitted = admitted.WithContext(rbac.WithIdentity(admitted.Context(), id))
			if call("GET", "/auth/oidc/logout").StatusCode != http.StatusForbidden {
				t.Fatal("invalid logout accepted")
			}
			if call("POST", "/cache/purge").StatusCode != 200 || call("POST", "/auth/oidc/logout").StatusCode != 204 {
				t.Fatal("mutation or logout failed")
			}
			cfg.OIDC.ProviderName = "Replacement identity"
			s.UpdateAuth(cfg, policy([]string{"status:read"}))
			s.recordAudit(admitted, "config.apply", "config", "success", "Authorization: Bearer synthetic-secret")

			check := func(events []AuditEvent) {
				t.Helper()
				seen := map[string]bool{}
				for _, ev := range events {
					if ev.Actor != id.Principal || ev.TokenID != id.TokenID || ev.ProviderName != "Company identity" || ev.Role != "deployment" || !reflect.DeepEqual(ev.Permissions, []string{"cache:purge"}) || ev.Result != "success" {
						t.Fatalf("historical identity changed or missing: %+v", ev)
					}
					seen[ev.Operation] = true
					if ev.Operation == "config.apply" && ev.Detail != "[redacted]" {
						t.Fatal("detail redaction changed")
					}
				}
				if len(events) != 4 || len(seen) != 4 || !seen["admin.oidc.login"] || !seen["admin.oidc.logout"] || !seen["cache.purge"] || !seen["config.apply"] {
					t.Fatalf("missing audit operations: %v", seen)
				}
			}
			check(s.audit.snapshot("", "", 0))
			for _, endpoint := range []string{"/api/audit", "/api/audit/export?format=json"} {
				rr := httptest.NewRecorder()
				if endpoint == "/api/audit" {
					s.handleAudit(rr, httptest.NewRequest("GET", endpoint, nil))
				} else {
					s.handleAuditExport(rr, httptest.NewRequest("GET", endpoint, nil))
				}
				var events []AuditEvent
				if err := json.Unmarshal(rr.Body.Bytes(), &events); err != nil {
					t.Fatal(err)
				}
				check(events)
			}
			rr := httptest.NewRecorder()
			s.handleAuditExport(rr, httptest.NewRequest("GET", "/api/audit/export?format=csv", nil))
			rows, err := csv.NewReader(rr.Body).ReadAll()
			if err != nil || len(rows) != 5 {
				t.Fatalf("CSV: %v %v", rows, err)
			}
			for _, row := range rows[1:] {
				if row[2] != id.Principal || row[8] != id.TokenID || row[9] != "Company identity" || row[10] != "deployment" || row[11] != "cache:purge" {
					t.Fatalf("CSV lost authority: %v", row)
				}
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var durable []AuditEvent
			for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				var ev AuditEvent
				if err := json.Unmarshal([]byte(line), &ev); err != nil {
					t.Fatal(err)
				}
				durable = append(durable, ev)
			}
			check(durable)
			secrets = append(secrets, cfg.Token, "private-subject", "private-group", "private@example.invalid", "claim-provider", "claim-role", "synthetic-secret", callback.Query().Get("code"))
			for _, secret := range secrets {
				if secret != "" && strings.Contains(string(data), secret) {
					t.Fatalf("durable audit disclosed private input %q", secret)
				}
			}
			// Returned slices must not permit a reader to rewrite ring history.
			snapshot := s.audit.snapshot("", "", 0)
			snapshot[0].Permissions[0] = "admin:manage"
			check(s.audit.snapshot("", "", 0))
		})
	}
}

func TestAuditMetadataBoundsAndAnonymousCompatibility(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	permissions := []string{"status:read"}
	s.audit.record(AuditEvent{Permissions: permissions})
	permissions[0] = "admin:manage"
	if s.audit.snapshot("", "", 1)[0].Permissions[0] != "status:read" {
		t.Fatal("record retained caller-owned permission storage")
	}
	for _, label := range []string{strings.Repeat("x", 81), "bad\nlabel"} {
		req := httptest.NewRequest("POST", "/", nil)
		id := rbac.Identity{Principal: "alice", ProviderName: label, Role: label, Permissions: []rbac.Permission{rbac.Wildcard}}
		s.recordAudit(req.WithContext(rbac.WithIdentity(req.Context(), id)), "test", "", "success", "")
		ev := s.audit.snapshot("", "", 1)[0]
		if ev.ProviderName != "[redacted]" || ev.Role != "[redacted]" || len(ev.Permissions) != len(rbac.Catalog()) {
			t.Fatalf("unbounded metadata: %+v", ev)
		}
	}
	req := httptest.NewRequest("POST", "/", nil)
	id := rbac.Identity{Legacy: true, Principal: "shared", Role: "admin", Permissions: []rbac.Permission{rbac.Wildcard}}
	s.recordAudit(req.WithContext(rbac.WithIdentity(req.Context(), id)), "test", "", "success", "")
	ev := s.audit.snapshot("", "", 1)[0]
	if ev.Actor != "anonymous" || ev.Role != "" || ev.ProviderName != "" || len(ev.Permissions) != 0 {
		t.Fatalf("open mode invented authority: %+v", ev)
	}
}
