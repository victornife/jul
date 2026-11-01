// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"jul/internal/adminoidc/oidctest"
	"jul/internal/config"
	"jul/internal/rbac"
)

func oidcPolicy(t *testing.T, role string) *rbac.Policy {
	t.Helper()
	p, err := rbac.Build(true, "admin", map[string][]string{"custom": {"status:read"}}, []rbac.PrincipalDef{{Name: "automation", Role: role, Token: strings.Repeat("a", 32)}}, strings.Repeat("r", 32), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func TestOIDCAdminFlowRBACReloadAndAudit(t *testing.T) {
	provider, err := oidctest.New()
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	cfg := config.AdminConfig{Enabled: true, Listen: "127.0.0.1:9090", Token: strings.Repeat("r", 32), RBAC: config.AdminRBACConfig{Enabled: true}}
	loginOutcomes := make(chan string, 8)
	s := New(cfg, testLogger(t), Deps{OIDCClient: provider.Server.Client(), Cache: &fakePurger{}, ObserveOIDCLogin: func(result string) { loginOutcomes <- result }})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	cfg.OIDC = config.AdminOIDCConfig{Enabled: true, Issuer: provider.URL(), ClientID: "jul", CallbackURL: srv.URL + "/auth/oidc/callback", Subjects: []config.AdminOIDCSubject{{Subject: "operator-1", Principal: "alice", Role: "viewer"}}}
	if err := provider.RegisterClient(cfg.OIDC.ClientID, cfg.OIDC.CallbackURL); err != nil {
		t.Fatal(err)
	}
	pol := oidcPolicy(t, "viewer")
	s.UpdateAuth(cfg, pol)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	call := func(method, path string, headers map[string]string) *http.Response {
		t.Helper()
		if strings.HasPrefix(path, "/") {
			path = srv.URL + path
		}
		var body io.Reader
		if method == "POST" && strings.HasSuffix(path, "/api/v1/cache/purge") {
			body = strings.NewReader(`{"selector":{"kind":"all","all":true},"confirm_broad":true}`)
		}
		req, _ := http.NewRequest(method, path, body)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}
	start := call("POST", "/auth/oidc/login", map[string]string{"Origin": srv.URL})
	authorize := call("GET", start.Header.Get("Location"), nil)
	callback := authorize.Header.Get("Location")
	if call("GET", callback, nil).Header.Get("Location") != "/" {
		t.Fatal("callback")
	}
	me := call("GET", "/api/admin/me", nil)
	var id identityResponse
	if json.NewDecoder(me.Body).Decode(&id) != nil || id.Principal != "alice" || id.Role != "viewer" || id.Authentication != "oidc" || id.SessionExpiresAt == nil {
		t.Fatalf("identity: %+v", id)
	}
	status := call("GET", "/auth/oidc/status", nil)
	var advertised struct {
		Enabled bool   `json:"enabled"`
		Name    string `json:"provider_name"`
	}
	if json.NewDecoder(status.Body).Decode(&advertised) != nil || !advertised.Enabled || advertised.Name != "OpenID Connect" {
		t.Fatal("enabled sign-in configuration missing")
	}
	if <-loginOutcomes != "allow" {
		t.Fatal("unbounded login metric result")
	}
	csrf := map[string]string{"Origin": srv.URL, "X-Jul-Console": "1"}
	if got := call("POST", "/api/config/apply", csrf).StatusCode; got != 403 {
		t.Fatalf("viewer bypass: %d", got)
	}
	if got := call("POST", "/api/config/apply", nil).StatusCode; got != 401 {
		t.Fatalf("missing CSRF: %d", got)
	}
	if got := call("GET", "/api/admin/me", map[string]string{"Authorization": "Bearer " + cfg.Token}).StatusCode; got != 401 {
		t.Fatalf("ambiguous credential: %d", got)
	}
	// A failed prepare cannot publish new policy or invalidate the old session.
	bad := cfg
	bad.PluginUploadEnabled = boolPtr(true)
	bad.PluginUploadMaxSize = 1
	blockedParent := filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(blockedParent, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad.PluginUploadDir = filepath.Join(blockedParent, "unusable")
	if _, err := s.PrepareAdminRuntime(bad, PrepareAuth(bad, pol)); err == nil {
		t.Fatal("invalid candidate accepted")
	}
	if call("GET", "/api/admin/me", nil).StatusCode != 200 {
		t.Fatal("failed candidate invalidated session")
	}
	// An unrelated runtime policy preserves the exact OIDC session owner.
	unchanged := cfg
	unchanged.MaxEventConns = 2
	prepared, err := s.PrepareAdminRuntime(unchanged, PrepareAuth(unchanged, pol))
	if err != nil {
		t.Fatal(err)
	}
	s.CommitPreparedAdminRuntime(prepared)
	if call("GET", "/api/admin/me", nil).StatusCode != 200 {
		t.Fatal("unrelated reload invalidated session")
	}
	// An explicitly mapped operator can mutate through the same catalogue and
	// audit owner as local tokens, with no provider-specific handler bypass.
	operator := cfg
	operator.OIDC.Subjects = []config.AdminOIDCSubject{{Subject: "operator-1", Principal: "alice", Role: "operator"}}
	prepared, err = s.PrepareAdminRuntime(operator, PrepareAuth(operator, pol))
	if err != nil {
		t.Fatal(err)
	}
	s.CommitPreparedAdminRuntime(prepared)
	start = call("POST", "/auth/oidc/login", map[string]string{"Origin": srv.URL})
	authorize = call("GET", start.Header.Get("Location"), nil)
	if call("GET", authorize.Header.Get("Location"), nil).Header.Get("Location") != "/" {
		t.Fatal("operator callback")
	}
	if got := call("POST", "/api/v1/cache/purge", csrf).StatusCode; got != 200 {
		t.Fatalf("operator mutation denied: %d", got)
	}
	foundMutation := false
	for _, ev := range s.audit.snapshot("", "", 100) {
		if ev.Operation == "cache.purge" {
			foundMutation = ev.Actor == "alice" && ev.TokenID == id.TokenID && ev.Result == "success"
		}
	}
	if !foundMutation {
		t.Fatal("OIDC mutation lost stable audit attribution")
	}
	// Mapping removal/role reduction is a new generation. Old cookies fail closed.
	changed := cfg
	changed.OIDC.Subjects = []config.AdminOIDCSubject{{Subject: "operator-1", Principal: "alice", Role: "auditor"}}
	prepared, err = s.PrepareAdminRuntime(changed, PrepareAuth(changed, pol))
	if err != nil {
		t.Fatal(err)
	}
	s.CommitPreparedAdminRuntime(prepared)
	if call("GET", "/api/admin/me", nil).StatusCode != 401 {
		t.Fatal("retired session survived mapping change")
	}
	// Exercise repeated publication while requests resolve the retired cookie.
	// Already admitted old-generation work is allowed; fresh requests never
	// acquire a new grant merely because a role name is toggled back.
	u, _ := url.Parse(srv.URL)
	cookies := client.Jar.Cookies(u)
	var requests sync.WaitGroup
	for i := 0; i < 8; i++ {
		requests.Go(func() {
			for j := 0; j < 100; j++ {
				req := httptest.NewRequest("GET", srv.URL+"/api/admin/me", nil)
				for _, cookie := range cookies {
					req.AddCookie(cookie)
				}
				rr := httptest.NewRecorder()
				s.Handler().ServeHTTP(rr, req)
				if rr.Code != 401 {
					t.Errorf("retired cookie regained authority during reload: %d", rr.Code)
				}
			}
		})
	}
	for i := 0; i < 100; i++ {
		candidate := changed
		role := "viewer"
		if i%2 == 0 {
			role = "auditor"
		}
		candidate.OIDC.Subjects = []config.AdminOIDCSubject{{Subject: "operator-1", Principal: "alice", Role: role}}
		prepared, err := s.PrepareAdminRuntime(candidate, PrepareAuth(candidate, pol))
		if err != nil {
			t.Fatal(err)
		}
		s.CommitPreparedAdminRuntime(prepared)
	}
	requests.Wait()
	start = call("POST", "/auth/oidc/login", map[string]string{"Origin": srv.URL})
	authorize = call("GET", start.Header.Get("Location"), nil)
	if call("GET", authorize.Header.Get("Location"), nil).Header.Get("Location") != "/" {
		t.Fatal("post-reload sign-in")
	}
	if got := call("POST", "/auth/oidc/logout", csrf).StatusCode; got != 204 {
		t.Fatalf("session logout failed: %d", got)
	}
	if call("GET", "/api/admin/me", nil).StatusCode != 401 {
		t.Fatal("logged-out session remained authoritative")
	}
	if call("GET", "/auth/oidc/callback?state=invalid&code=invalid", nil).Header.Get("Location") != "/?oidc_error=login_failed" {
		t.Fatal("malformed callback admitted")
	}
	for _, want := range []string{"allow", "allow", "deny"} {
		if got := <-loginOutcomes; got != want {
			t.Fatalf("login metric result %q, want %q", got, want)
		}
	}
	foundLogout := false
	foundDeniedLogin := false
	for _, ev := range s.audit.snapshot("", "", 100) {
		if ev.Operation == "admin.oidc.logout" {
			foundLogout = ev.Actor == "alice" && ev.TokenID == id.TokenID && ev.Result == "success"
		}
		if ev.Operation == "admin.oidc.login" && ev.Result == "failure" {
			foundDeniedLogin = ev.Actor == "anonymous" && ev.ProviderName == "OpenID Connect" && ev.TokenID == "" && ev.Role == "" && len(ev.Permissions) == 0
		}
	}
	if !foundLogout {
		t.Fatal("local logout lost stable audit attribution")
	}
	if !foundDeniedLogin {
		t.Fatal("rejected login lost provider or invented identity metadata")
	}
	local := &http.Client{}
	req, _ := http.NewRequest("GET", srv.URL+"/api/admin/me", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	resp, err := local.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("break-glass unavailable")
	}
	if !strings.HasPrefix(id.TokenID, "cid1:") {
		t.Fatal("no stable audit identifier")
	}
	// Callback processing and audit must never expose codes/session IDs.
	u, _ = url.Parse(callback)
	for _, ev := range s.audit.snapshot("", "", 100) {
		b, _ := json.Marshal(ev)
		if strings.Contains(string(b), u.Query().Get("code")) {
			t.Fatal("audit leaked authorization code")
		}
	}
}

func TestOIDCPrepareGuardsAndLocalModes(t *testing.T) {
	s := New(config.AdminConfig{Enabled: true, Listen: "127.0.0.1:9090"}, testLogger(t), Deps{})
	for _, path := range []string{"/auth/oidc/status", "/auth/oidc/login", "/auth/oidc/callback", "/auth/oidc/logout"} {
		req := httptest.NewRequest("GET", path, nil)
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, req)
		if rr.Code == 500 {
			t.Fatal(path)
		}
	}
	req := httptest.NewRequest("GET", "/api/admin/me", nil)
	req.RemoteAddr = "127.0.0.1:1"
	req.AddCookie(&http.Cookie{Name: "jul_oidc_dev", Value: strings.Repeat("x", 52)})
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != 401 {
		t.Fatalf("retired cookie status %d: %s", rr.Code, rr.Body.String())
	}
	cfg := config.AdminConfig{OIDC: config.AdminOIDCConfig{Enabled: true, CallbackURL: "https://admin.example/auth/oidc/callback", Issuer: "https://idp.example"}}
	if err := s.prepareOIDC(deriveAuthSnapshot(cfg, oidcPolicy(t, "viewer"), "x")); err == nil {
		t.Fatal("missing egress client accepted")
	}
}

func TestOIDCDisabledStillClearsStaleBrowserCredential(t *testing.T) {
	s := New(config.AdminConfig{Enabled: true, Listen: "127.0.0.1:9090"}, testLogger(t), Deps{})
	req := httptest.NewRequest("POST", "http://localhost/auth/oidc/logout", nil)
	req.Header.Set("Origin", "http://localhost")
	req.Header.Set("X-Jul-Console", "1")
	req.AddCookie(&http.Cookie{Name: "jul_oidc_dev", Value: strings.Repeat("x", 52)})
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != 204 || len(rr.Result().Cookies()) != 4 {
		t.Fatalf("recovery cookie clearing: %d", rr.Code)
	}
	for _, c := range rr.Result().Cookies() {
		if c.MaxAge != -1 {
			t.Fatal("stale credential retained")
		}
	}
}

func TestOIDCConsoleFormPreservesOriginWithoutCrossOriginReferrers(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		cfg := config.AdminConfig{Enabled: true, Listen: "127.0.0.1:9090", Token: strings.Repeat("r", 32), RBAC: config.AdminRBACConfig{Enabled: true}, OIDC: config.AdminOIDCConfig{Enabled: enabled, Issuer: "https://issuer.example", ClientID: "jul", CallbackURL: "https://localhost/auth/oidc/callback"}}
		s := New(cfg, testLogger(t), Deps{OIDCClient: http.DefaultClient})
		s.UpdateAuth(cfg, oidcPolicy(t, "viewer"))
		req := httptest.NewRequest("GET", "https://localhost/", nil)
		rr := httptest.NewRecorder()
		s.serveConsoleV2Index(rr, req, []byte("<head></head>"))
		want := "no-referrer"
		if enabled {
			want = "same-origin"
		}
		if got := rr.Header().Get("Referrer-Policy"); got != want {
			t.Fatalf("OIDC enabled=%v: referrer policy %q, want %q", enabled, got, want)
		}
	}
}
