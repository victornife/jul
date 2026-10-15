// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package adminoidc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"jul/internal/adminoidc/oidctest"
	"jul/internal/config"
	"jul/internal/rbac"
)

func policy(t testing.TB) *rbac.Policy {
	t.Helper()
	p, err := rbac.Build(true, "admin", map[string][]string{"custom": {"status:read"}}, nil, "local-recovery-32-character-token", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func fixture(t *testing.T) (*Runtime, *oidctest.Provider, *http.Client, *httptest.Server) {
	t.Helper()
	p, err := oidctest.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	var runtime *Runtime
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/oidc/login":
			runtime.Login(w, r)
		case "/auth/oidc/callback":
			runtime.Callback(w, r)
		case "/auth/oidc/logout":
			runtime.Logout(w, r)
		default:
			id, _, err := runtime.Authenticate(r)
			if err != nil {
				w.WriteHeader(401)
				return
			}
			_ = json.NewEncoder(w).Encode(id.Principal)
		}
	}))
	t.Cleanup(srv.Close)
	runtime, err = New(config.AdminOIDCConfig{Enabled: true, Issuer: p.URL(), ClientID: "jul-console", CallbackURL: srv.URL + "/auth/oidc/callback", Subjects: []config.AdminOIDCSubject{{Subject: "operator-1", Principal: "alice", Role: "viewer"}}}, policy(t), p.Server.Client())
	if err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return runtime, p, client, srv
}
func request(t testing.TB, client *http.Client, method, raw, origin string, header bool) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if header {
		req.Header.Set("X-Jul-Console", "1")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}
func login(t *testing.T, c *http.Client, srv *httptest.Server) *http.Response {
	t.Helper()
	resp := request(t, c, "POST", srv.URL+"/auth/oidc/login", srv.URL, false)
	for i := 0; i < 3 && resp.StatusCode == 303; i++ {
		loc := resp.Header.Get("Location")
		if strings.HasPrefix(loc, "/") {
			loc = srv.URL + loc
		}
		resp = request(t, c, "GET", loc, "", false)
	}
	return resp
}
func TestCompleteFlowSessionCSRFLogoutAndOutage(t *testing.T) {
	r, p, c, srv := fixture(t)
	if got := login(t, c, srv).StatusCode; got != 200 {
		t.Fatalf("login: %d", got)
	}
	p.Configure("outage", nil)
	if got := request(t, c, "GET", srv.URL, "", false).StatusCode; got != 200 {
		t.Fatalf("existing outage session: %d", got)
	}
	for _, tt := range []struct {
		origin string
		header bool
		want   int
	}{{"", true, 403}, {"https://attacker.invalid", true, 403}, {srv.URL, false, 403}, {srv.URL, true, 204}} {
		if got := request(t, c, "POST", srv.URL+"/auth/oidc/logout", tt.origin, tt.header).StatusCode; got != tt.want {
			t.Errorf("logout %q/%v: %d", tt.origin, tt.header, got)
		}
	}
	if got := request(t, c, "GET", srv.URL, "", false).StatusCode; got != 401 {
		t.Fatalf("revocation: %d", got)
	}
	if r.sessionCount.Load() != 0 {
		t.Fatal("logout retained session")
	}
}

func TestExplicitPublicClientWithKeycloakMetadata(t *testing.T) {
	cfg := config.AdminOIDCConfig{Issuer: "https://idp.invalid"}
	m := metadata{Issuer: cfg.Issuer, Authorization: cfg.Issuer + "/authorize", Token: cfg.Issuer + "/token", JWKS: cfg.Issuer + "/jwks", Responses: []string{"code"}, Algorithms: []string{"RS256"}, PKCE: []string{"S256"}, AuthMethods: []string{"client_secret_basic", "client_secret_post"}}
	method, err := validateMetadata(cfg, m)
	if err != nil || method != "none" {
		t.Fatalf("explicit public PKCE client: method=%q err=%v", method, err)
	}
	cfg.ClientSecret = "synthetic-secret"
	m.AuthMethods = []string{"none"}
	if _, err := validateMetadata(cfg, m); err == nil {
		t.Fatal("confidential client downgraded to public")
	}
}

func TestLogoutClearsRetiredCookieForms(t *testing.T) {
	r, _, client, server := fixture(t)
	if login(t, client, server).StatusCode != 200 {
		t.Fatal("login")
	}
	u, _ := url.Parse(server.URL)
	req := httptest.NewRequest("POST", server.URL+"/auth/oidc/logout", nil)
	for _, cookie := range client.Jar.Cookies(u) {
		req.AddCookie(cookie)
	}
	req.AddCookie(&http.Cookie{Name: "__Host-jul_oidc", Value: randomSecret()})
	req.Header.Set("Origin", server.URL)
	req.Header.Set("X-Jul-Console", "1")
	if _, _, err := r.Authenticate(req); err == nil {
		t.Fatal("mixed session forms were authenticated")
	}
	rr := httptest.NewRecorder()
	r.Logout(rr, req)
	if rr.Code != 204 || r.sessionCount.Load() != 0 || len(rr.Result().Cookies()) != 4 {
		t.Fatal("retired cookie prevented local session recovery")
	}
	for _, cookie := range rr.Result().Cookies() {
		if cookie.MaxAge != -1 {
			t.Fatal("logout retained a browser credential")
		}
	}
}

func TestLogoutRevokesDuplicatePresentedSessions(t *testing.T) {
	r, _, first, server := fixture(t)
	jar, _ := cookiejar.New(nil)
	second := &http.Client{Jar: jar, CheckRedirect: first.CheckRedirect}
	if login(t, first, server).StatusCode != 200 || login(t, second, server).StatusCode != 200 {
		t.Fatal("login")
	}
	u, _ := url.Parse(server.URL)
	req := httptest.NewRequest("POST", server.URL+"/auth/oidc/logout", nil)
	for _, client := range []*http.Client{first, second} {
		for _, cookie := range client.Jar.Cookies(u) {
			req.AddCookie(cookie)
		}
	}
	req.Header.Set("Origin", server.URL)
	req.Header.Set("X-Jul-Console", "1")
	if _, _, err := r.Authenticate(req); err == nil {
		t.Fatal("duplicate sessions were authenticated")
	}
	rr := httptest.NewRecorder()
	r.Logout(rr, req)
	if rr.Code != 204 || r.sessionCount.Load() != 0 {
		t.Fatal("logout left a presented session replayable")
	}
	if request(t, first, "GET", server.URL, "", false).StatusCode != 401 || request(t, second, "GET", server.URL, "", false).StatusCode != 401 {
		t.Fatal("revoked session remained authoritative")
	}
}

func TestCallbackRejectsBearerCredential(t *testing.T) {
	r, _, client, server := fixture(t)
	start := request(t, client, "POST", server.URL+"/auth/oidc/login", server.URL, false)
	callback := request(t, client, "GET", start.Header.Get("Location"), "", false).Header.Get("Location")
	req, err := http.NewRequest("GET", callback, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer unrelated-local-credential")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Location") != "/?oidc_error=login_failed" || r.sessionCount.Load() != 0 {
		t.Fatal("callback silently ignored an extra credential")
	}
}

func TestSuccessfulCallbackExpiresEveryRetiredCookieForm(t *testing.T) {
	r, _, client, server := fixture(t)
	start := request(t, client, "POST", server.URL+"/auth/oidc/login", server.URL, false)
	callback := request(t, client, "GET", start.Header.Get("Location"), "", false).Header.Get("Location")
	req := httptest.NewRequest("GET", callback, nil)
	u, _ := url.Parse(server.URL)
	for _, cookie := range client.Jar.Cookies(u) {
		req.AddCookie(cookie)
	}
	req.AddCookie(&http.Cookie{Name: "__Host-jul_oidc", Value: randomSecret()})
	rr := httptest.NewRecorder()
	if _, accepted := r.Callback(rr, req); !accepted {
		t.Fatal("fresh sign-in could not replace a retired cookie")
	}
	expired := map[string]bool{}
	for _, cookie := range rr.Result().Cookies() {
		if cookie.MaxAge == -1 {
			expired[cookie.Name] = true
		}
	}
	for _, name := range []string{"__Host-jul_oidc", "__Host-jul_oidc_login", "jul_oidc_dev", "jul_oidc_login_dev"} {
		if !expired[name] {
			t.Fatal("fresh sign-in retained a retired browser credential")
		}
	}
}
func TestProtocolRejections(t *testing.T) {
	for _, tt := range []struct {
		name, mode string
		claims     map[string]any
	}{
		{"issuer", "", map[string]any{"iss": "https://attacker.invalid"}},
		{"audience", "", map[string]any{"aud": "other-client"}},
		{"azp", "", map[string]any{"azp": "other-client"}},
		{"multi-audience", "", map[string]any{"aud": []string{"jul-console", "other"}}},
		{"nonce", "", map[string]any{"nonce": "wrong"}},
		{"expired", "", map[string]any{"exp": time.Now().Add(-time.Hour).Unix()}},
		{"future-issued", "", map[string]any{"iat": time.Now().Add(time.Hour).Unix()}},
		{"missing-sub", "", map[string]any{"sub": ""}},
		{"unknown-sub", "", map[string]any{"sub": "unknown"}},
		{"discovery-issuer", "wrong_discovery_issuer", nil},
		{"discovery-bound", "oversized", nil},
		{"redirect", "redirect", nil},
		{"endpoint", "unsafe_endpoint", nil},
		{"plain-pkce", "plain_pkce", nil},
		{"signature", "bad_signature", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, p, c, srv := fixture(t)
			p.Configure(tt.mode, tt.claims)
			if got := login(t, c, srv).StatusCode; got != 401 {
				t.Fatalf("invalid login admitted/status %d", got)
			}
			if r.sessionCount.Load() != 0 {
				t.Fatal("invalid login minted session")
			}
		})
	}
}
func TestCallbackBrowserBindingStateReplayAndBounds(t *testing.T) {
	r, _, c, srv := fixture(t)
	start := request(t, c, "POST", srv.URL+"/auth/oidc/login", srv.URL, false)
	authz := start.Header.Get("Location")
	u, _ := url.Parse(authz)
	if u.Query().Get("code_challenge_method") != "S256" || len(u.Query().Get("code_challenge")) != 43 {
		t.Fatal("invalid PKCE")
	}
	cb := request(t, c, "GET", authz, "", false).Header.Get("Location")
	other := &http.Client{CheckRedirect: c.CheckRedirect}
	if got := request(t, other, "GET", cb, "", false).Header.Get("Location"); got != "/?oidc_error=login_failed" {
		t.Fatal(got)
	}
	// The browserless attempt did not consume the valid browser's transaction.
	if got := request(t, c, "GET", cb, "", false).Header.Get("Location"); got != "/" {
		t.Fatal(got)
	}
	if got := request(t, c, "GET", cb, "", false).Header.Get("Location"); got != "/?oidc_error=login_failed" {
		t.Fatal("replayed callback admitted")
	}
	req := httptest.NewRequest("POST", srv.URL, nil)
	u, _ = url.Parse(srv.URL)
	for _, cookie := range c.Jar.Cookies(u) {
		req.AddCookie(cookie)
	}
	req.Header.Set("Origin", srv.URL)
	req.Header.Set("X-Jul-Console", "1")
	if _, _, err := r.Authenticate(req); err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer local")
	if _, _, err := r.Authenticate(req); err == nil {
		t.Fatal("ambiguous credential admitted")
	}
	for _, query := range []string{"state=x&state=y&code=a", "state=x&code=a&code=b", "state=x&code=a&iss=https://other.invalid", "%ff", "state=" + strings.Repeat("x", 8200)} {
		rr := httptest.NewRecorder()
		r.Callback(rr, httptest.NewRequest("GET", srv.URL+"/auth/oidc/callback?"+query, nil))
		if rr.Header().Get("Location") != "/?oidc_error=login_failed" {
			t.Fatal(query)
		}
	}
}
func TestSessionIdleAbsoluteExpiryCapacityAndCleanup(t *testing.T) {
	r, _, c, srv := fixture(t)
	r.cfg.MaxSessions = 1
	if login(t, c, srv).StatusCode != 200 {
		t.Fatal("initial login")
	}
	// Same-browser login replaces the old session even at capacity.
	if login(t, c, srv).StatusCode != 200 || r.sessionCount.Load() != 1 {
		t.Fatal("replacement/fixation")
	}
	jar, _ := cookiejar.New(nil)
	other := &http.Client{Jar: jar, CheckRedirect: c.CheckRedirect}
	if login(t, other, srv).StatusCode != 401 {
		t.Fatal("session capacity did not deny")
	}
	now := time.Now().Add(r.cfg.SessionIdle.Std() + time.Second)
	r.now = func() time.Time { return now }
	if request(t, c, "GET", srv.URL, "", false).StatusCode != 401 || r.sessionCount.Load() != 0 {
		t.Fatal("idle expiry")
	}
	r.mu.Lock()
	r.cleanup(now)
	r.mu.Unlock()
	if len(r.transactions) != 0 {
		t.Fatal("expired transactions retained")
	}
}
func TestGroupMappingAndSubjectPrecedence(t *testing.T) {
	r, _, _, _ := fixture(t)
	r.cfg.GroupClaim = "https://jul.example/groups"
	r.cfg.Groups = []config.AdminOIDCGroup{{Group: "ops", Role: "operator"}, {Group: "view", Role: "viewer"}}
	for _, tt := range []struct {
		name string
		c    map[string]any
		role string
	}{
		{"exact wins", map[string]any{"sub": "operator-1"}, "viewer"},
		{"group", map[string]any{"sub": "new", "https://jul.example/groups": []any{"ops"}}, "operator"},
		{"missing", map[string]any{"sub": "new"}, ""},
		{"unknown", map[string]any{"sub": "new", "https://jul.example/groups": []any{"unknown"}}, ""},
		{"conflict", map[string]any{"sub": "new", "https://jul.example/groups": []any{"ops", "view"}}, ""},
		{"overage", map[string]any{"sub": "new", "hasgroups": true, "https://jul.example/groups": []any{"ops"}}, ""},
		{"claim-overage", map[string]any{"sub": "new", "_claim_names": map[string]any{"https://jul.example/groups": "src"}}, ""},
		{"malformed", map[string]any{"sub": "new", "https://jul.example/groups": []any{123}}, ""},
		{"too-many", map[string]any{"sub": "new", "https://jul.example/groups": make([]any, 257)}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			id, err := r.mapIdentity(tt.c)
			if tt.role == "" {
				if err == nil {
					t.Fatal("mapping admitted")
				}
			} else if err != nil || id.Role != tt.role {
				t.Fatalf("%+v %v", id, err)
			}
		})
	}
}
func TestConcurrentSessionLookupAndRevocation(t *testing.T) {
	r, _, c, srv := fixture(t)
	if login(t, c, srv).StatusCode != 200 {
		t.Fatal("login")
	}
	u, _ := url.Parse(srv.URL)
	cookies := c.Jar.Cookies(u)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				req := httptest.NewRequest("GET", srv.URL, nil)
				for _, cookie := range cookies {
					req.AddCookie(cookie)
				}
				_, _, _ = r.Authenticate(req)
			}
		}()
	}
	if request(t, c, "POST", srv.URL+"/auth/oidc/logout", srv.URL, true).StatusCode != 204 {
		t.Fatal("logout")
	}
	wg.Wait()
	if request(t, c, "GET", srv.URL, "", false).StatusCode != 401 || r.sessionCount.Load() != 0 {
		t.Fatal("concurrent lookup revived revoked session")
	}
}
func TestCookieAttributesAndLoginOrigin(t *testing.T) {
	r, _, _, srv := fixture(t)
	r.secure = true
	rr := httptest.NewRecorder()
	r.setCookie(rr, false, randomSecret(), 3600)
	cookie := rr.Result().Cookies()[0]
	if cookie.Name != "__Host-jul_oidc" || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.Domain != "" {
		t.Fatalf("unsafe cookie: %+v", cookie)
	}
	for _, method := range []string{"GET", "POST"} {
		rr := httptest.NewRecorder()
		r.Login(rr, httptest.NewRequest(method, srv.URL+"/auth/oidc/login", nil))
		if rr.Code != 403 {
			t.Fatal(rr.Code)
		}
	}
	opaque := httptest.NewRequest("POST", srv.URL+"/auth/oidc/login", nil)
	opaque.Header.Set("Origin", "null")
	opaque.Header.Set("Sec-Fetch-Site", "same-origin")
	rr = httptest.NewRecorder()
	r.Login(rr, opaque)
	if rr.Code != 403 {
		t.Fatal("opaque Origin bypassed browser binding")
	}
}
func TestCancelledDiscoveryAndInvalidConstruction(t *testing.T) {
	r, _, _, _ := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.discover(ctx); err == nil {
		t.Fatal("cancelled discovery")
	}
	if _, err := New(config.AdminOIDCConfig{}, nil, nil); err == nil {
		t.Fatal("invalid construction")
	}
}
func FuzzCallback(f *testing.F) {
	f.Add("state=x&code=y")
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 9000 {
			return
		}
		r := &Runtime{origin: "http://localhost", now: time.Now, transactions: map[[32]byte]transaction{}}
		req := httptest.NewRequest("GET", "http://localhost/auth/oidc/callback", nil)
		req.URL.RawQuery = raw
		r.Callback(httptest.NewRecorder(), req)
	})
}
func FuzzMapping(f *testing.F) {
	f.Add([]byte(`{"sub":"user","groups":["ops"]}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 16384 {
			return
		}
		var c map[string]any
		if json.Unmarshal(b, &c) != nil {
			return
		}
		r := &Runtime{cfg: config.AdminOIDCConfig{Issuer: "https://idp.invalid", GroupClaim: "groups", Groups: []config.AdminOIDCGroup{{Group: "ops", Role: "viewer"}}}}
		_, _ = r.mapIdentity(c)
	})
}
func BenchmarkSessionLookup(b *testing.B) {
	r := &Runtime{cfg: config.AdminOIDCConfig{SessionIdle: config.Duration(time.Hour)}, origin: "http://localhost", now: time.Now}
	raw := randomSecret()
	key := digest(raw)
	r.sessions[key[0]%32].entries = map[[32]byte]*session{key: {identity: rbac.Identity{Principal: "alice"}, last: time.Now(), expires: time.Now().Add(time.Hour)}}
	req := httptest.NewRequest("GET", "http://localhost", nil)
	req.AddCookie(&http.Cookie{Name: "jul_oidc_dev", Value: raw})
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _, _ = r.Authenticate(req)
		}
	})
}

func TestTransactionsExpireAndExhaustWithoutSessionLeak(t *testing.T) {
	r, _, c, srv := fixture(t)
	r.cfg.MaxTransactions = 1
	first := request(t, c, "POST", srv.URL+"/auth/oidc/login", srv.URL, false)
	if first.StatusCode != 303 {
		t.Fatal(first.StatusCode)
	}
	if got := request(t, c, "POST", srv.URL+"/auth/oidc/login", srv.URL, false).Header.Get("Location"); got != "/?oidc_error=capacity" {
		t.Fatal(got)
	}
	now := time.Now().Add(6 * time.Minute)
	r.now = func() time.Time { return now }
	callback := request(t, c, "GET", first.Header.Get("Location"), "", false).Header.Get("Location")
	if got := request(t, c, "GET", callback, "", false).Header.Get("Location"); got != "/?oidc_error=login_failed" {
		t.Fatal("expired transaction accepted")
	}
	if len(r.transactions) != 0 {
		t.Fatal("expired transaction retained")
	}
}
func TestAbsoluteExpiryAndCounts(t *testing.T) {
	r, _, c, srv := fixture(t)
	if login(t, c, srv).StatusCode != 200 {
		t.Fatal("login")
	}
	now := time.Now().Add(r.cfg.SessionAbsolute.Std() + time.Second)
	r.now = func() time.Time { return now }
	r.mu.Lock()
	r.cleanup(now)
	r.mu.Unlock()
	if r.sessionCount.Load() != 0 || request(t, c, "GET", srv.URL, "", false).StatusCode != 401 {
		t.Fatal("absolute expiry")
	}
	if r.ProviderName() == "" {
		t.Fatal("provider label")
	}
	for _, name := range []string{"jul_oidc_dev", "__Host-jul_oidc", "other"} {
		req := httptest.NewRequest("GET", srv.URL, nil)
		req.AddCookie(&http.Cookie{Name: name, Value: randomSecret()})
		if CookiePresented(req) != (name != "other") {
			t.Fatal(name)
		}
	}
}
func TestConfidentialClientAndDiscoveryRetry(t *testing.T) {
	for _, secret := range []string{"synthetic-client-secret", ""} {
		t.Run(secret, func(t *testing.T) {
			r, p, c, srv := fixture(t)
			r.cfg.ClientSecret = secret
			if login(t, c, srv).StatusCode != 200 {
				t.Fatal("client login")
			}
			r.providerUntil = time.Time{}
			p.Configure("outage", nil)
			if _, err := r.discover(context.Background()); err == nil {
				t.Fatal("outage accepted")
			}
			if _, err := r.discover(context.Background()); err == nil {
				t.Fatal("retry throttle bypass")
			}
		})
	}
}
func FuzzDiscovery(f *testing.F) {
	f.Add([]byte(`{"issuer":"https://idp.invalid"}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 65537 {
			return
		}
		var m metadata
		if json.Unmarshal(b, &m) == nil {
			_, _ = validateMetadata(config.AdminOIDCConfig{Issuer: "https://idp.invalid"}, m)
		}
	})
}

func TestDatedProviderContractFixtures(t *testing.T) {
	for _, tt := range []struct {
		name     string
		accepted bool
	}{{"google", true}, {"meta", false}} {
		t.Run(tt.name, func(t *testing.T) {
			b, err := os.ReadFile("../../testdata/admin-oidc/" + tt.name + "-discovery.json")
			if err != nil {
				t.Fatal(err)
			}
			var wrapper struct {
				Source   string   `json:"source_url"`
				Date     string   `json:"retrieved_date"`
				Metadata metadata `json:"metadata"`
			}
			if err := json.Unmarshal(b, &wrapper); err != nil {
				t.Fatal(err)
			}
			if wrapper.Source == "" || wrapper.Date != "2026-10-15" {
				t.Fatal("missing provenance")
			}
			_, err = validateMetadata(config.AdminOIDCConfig{Issuer: wrapper.Metadata.Issuer, ClientSecret: "synthetic"}, wrapper.Metadata)
			if (err == nil) != tt.accepted {
				t.Fatal(err)
			}
		})
	}
}
