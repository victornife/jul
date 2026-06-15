package router

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/config"
)

// echoBuilder returns a handler that writes a tag identifying the matched
// location path, so tests can assert which route was selected.
func echoBuilder(_ config.ServerConfig, loc config.LocationConfig) (http.Handler, error) {
	tag := loc.Match.Path
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Matched", tag)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(tag))
	}), nil
}

func testRouter(t *testing.T, cfg *config.Config) *Router {
	t.Helper()
	builders := map[string]Builder{
		ActionStatic: echoBuilder,
		ActionProxy:  echoBuilder,
	}
	r, err := New(cfg, builders, echoBuilder, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func do(t *testing.T, r *Router, addr, host, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://"+host+path, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	r.For(addr).ServeHTTP(rec, req)
	return rec
}

func TestHostRouting(t *testing.T) {
	cfg := &config.Config{Servers: []config.ServerConfig{
		{
			Listen:      "127.0.0.1:80",
			ServerNames: []string{"a.example.com"},
			Locations:   []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Root: "/a"}},
		},
		{
			Listen:      "127.0.0.1:80",
			ServerNames: []string{"b.example.com"},
			Locations:   []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Root: "/b"}},
		},
	}}
	r := testRouter(t, cfg)

	if got := do(t, r, "127.0.0.1:80", "a.example.com", "/").Header().Get("X-Matched"); got != "/" {
		t.Fatalf("host a matched %q", got)
	}
	// Unknown host falls back to the first (default) server, which still serves.
	if rec := do(t, r, "127.0.0.1:80", "unknown.example.com", "/"); rec.Code != http.StatusOK {
		t.Fatalf("unknown host status = %d", rec.Code)
	}
}

func TestWildcardHost(t *testing.T) {
	cfg := &config.Config{Servers: []config.ServerConfig{
		{
			Listen:      "127.0.0.1:80",
			ServerNames: []string{"fallback"},
			Locations:   []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Root: "/f"}},
		},
		{
			Listen:      "127.0.0.1:80",
			ServerNames: []string{"*.example.com"},
			Locations:   []config.LocationConfig{{Match: config.MatchConfig{Type: "exact", Path: "/"}, Root: "/wild"}},
		},
	}}
	r := testRouter(t, cfg)
	rec := do(t, r, "127.0.0.1:80", "api.example.com", "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("wildcard host status = %d", rec.Code)
	}
}

func TestLocationPrecedence(t *testing.T) {
	cfg := &config.Config{Servers: []config.ServerConfig{{
		Listen: "127.0.0.1:80",
		Locations: []config.LocationConfig{
			{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Root: "/root"},
			{Match: config.MatchConfig{Type: "prefix", Path: "/api/"}, Root: "/api"},
			{Match: config.MatchConfig{Type: "exact", Path: "/api/health"}, Root: "/exact"},
			{Match: config.MatchConfig{Type: "regex", Path: `\.png$`}, Root: "/png"},
		},
	}}}
	r := testRouter(t, cfg)

	cases := map[string]string{
		"/api/health":   "/api/health", // exact wins
		"/api/users":    "/api/",       // longest prefix
		"/index.html":   "/",           // root fallback
		"/img/logo.png": `\.png$`,      // regex (no non-root prefix matches)
	}
	for path, want := range cases {
		got := do(t, r, "127.0.0.1:80", "h", path).Header().Get("X-Matched")
		if got != want {
			t.Errorf("path %s matched %q, want %q", path, got, want)
		}
	}
}

func TestRewriteRedirect(t *testing.T) {
	cfg := &config.Config{Servers: []config.ServerConfig{{
		Listen: "127.0.0.1:80",
		Locations: []config.LocationConfig{{
			Match:    config.MatchConfig{Type: "prefix", Path: "/"},
			Root:     "/root",
			Rewrites: []config.RewriteConfig{{Pattern: "^/old/(.*)$", Replacement: "/new/$1", Flag: "redirect"}},
		}},
	}}}
	r := testRouter(t, cfg)

	rec := do(t, r, "127.0.0.1:80", "h", "/old/page")
	if rec.Code != http.StatusFound {
		t.Fatalf("redirect status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/new/page" {
		t.Fatalf("redirect Location = %q, want /new/page", loc)
	}
}

func TestRewriteInternal(t *testing.T) {
	cfg := &config.Config{Servers: []config.ServerConfig{{
		Listen: "127.0.0.1:80",
		Locations: []config.LocationConfig{{
			Match:    config.MatchConfig{Type: "prefix", Path: "/"},
			Root:     "/root",
			Rewrites: []config.RewriteConfig{{Pattern: "^/foo$", Replacement: "/bar", Flag: "break"}},
		}},
	}}}
	r := testRouter(t, cfg)

	// Internal rewrite should not redirect; handler still runs (200).
	rec := do(t, r, "127.0.0.1:80", "h", "/foo")
	if rec.Code != http.StatusOK {
		t.Fatalf("internal rewrite status = %d, want 200", rec.Code)
	}
}

func TestDenyAndRedirectActions(t *testing.T) {
	cfg := &config.Config{Servers: []config.ServerConfig{{
		Listen: "127.0.0.1:80",
		Locations: []config.LocationConfig{
			{Match: config.MatchConfig{Type: "prefix", Path: "/blocked"}, Deny: true},
			{Match: config.MatchConfig{Type: "exact", Path: "/go"}, Redirect: "https://example.com", Return: 301},
			{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Root: "/root"},
		},
	}}}
	r := testRouter(t, cfg)

	if rec := do(t, r, "127.0.0.1:80", "h", "/blocked/x"); rec.Code != http.StatusForbidden {
		t.Fatalf("deny status = %d, want 403", rec.Code)
	}
	rec := do(t, r, "127.0.0.1:80", "h", "/go")
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "https://example.com" {
		t.Fatalf("redirect action = %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestRedirectHTTPS(t *testing.T) {
	cfg := &config.Config{Servers: []config.ServerConfig{{
		Listen:        "127.0.0.1:80",
		ServerNames:   []string{"secure.example.com"},
		RedirectHTTPS: 308,
		Locations:     []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Root: "/root"}},
	}}}
	r := testRouter(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "http://secure.example.com:80/path?q=1", nil)
	req.Host = "secure.example.com:80"
	rec := httptest.NewRecorder()
	r.For("127.0.0.1:80").ServeHTTP(rec, req)

	if rec.Code != http.StatusPermanentRedirect {
		t.Fatalf("status = %d, want 308", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://secure.example.com/path?q=1" {
		t.Fatalf("Location = %q", loc)
	}
}

func TestBodyLimitEnforced(t *testing.T) {
	// drainBuilder reads the whole body so MaxBytesReader can trip the limit.
	drainBuilder := func(_ config.ServerConfig, _ config.LocationConfig) (http.Handler, error) {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, err := io.ReadAll(r.Body); err != nil {
				http.Error(w, "413 Request Entity Too Large", http.StatusRequestEntityTooLarge)
				return
			}
			w.WriteHeader(http.StatusOK)
		}), nil
	}
	cfg := &config.Config{Servers: []config.ServerConfig{{
		Listen:            "127.0.0.1:80",
		ClientMaxBodySize: config.Size(8),
		Locations:         []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Root: "/root"}},
	}}}
	builders := map[string]Builder{ActionStatic: drainBuilder}
	r, err := New(cfg, builders, drainBuilder, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Under the limit -> 200.
	req := httptest.NewRequest(http.MethodPost, "http://h/", strings.NewReader("12345"))
	rec := httptest.NewRecorder()
	r.For("127.0.0.1:80").ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("small body = %d, want 200", rec.Code)
	}

	// Over the 8-byte limit -> 413.
	req = httptest.NewRequest(http.MethodPost, "http://h/", strings.NewReader("0123456789ABCDEF"))
	rec = httptest.NewRecorder()
	r.For("127.0.0.1:80").ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large body = %d, want 413", rec.Code)
	}
}

// TestActionRegistryOverridesBuiltin proves every action — including the
// router's built-in config actions — dispatches through the one registry, so a
// caller-supplied builder for "redirect" takes precedence over the built-in.
func TestActionRegistryOverridesBuiltin(t *testing.T) {
	cfg := &config.Config{Servers: []config.ServerConfig{{
		Listen: "127.0.0.1:80",
		Locations: []config.LocationConfig{
			{Match: config.MatchConfig{Type: "exact", Path: "/go"}, Redirect: "https://example.com", Return: 301},
		},
	}}}
	custom := func(_ config.ServerConfig, _ config.LocationConfig) (http.Handler, error) {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		}), nil
	}
	r, err := New(cfg, map[string]Builder{ActionRedirect: custom}, nil, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if rec := do(t, r, "127.0.0.1:80", "h", "/go"); rec.Code != http.StatusTeapot {
		t.Fatalf("override builder not used: status = %d, want 418", rec.Code)
	}
}

// TestActionFallbackWhenUnregistered proves the built-in actions are present
// even with a nil builders map, and that a content action with no registered
// builder and no fallback uses the default notImplemented (501) builder — the
// uniform fallback path shared by every action.
func TestActionFallbackWhenUnregistered(t *testing.T) {
	cfg := &config.Config{Servers: []config.ServerConfig{{
		Listen: "127.0.0.1:80",
		Locations: []config.LocationConfig{
			{Match: config.MatchConfig{Type: "prefix", Path: "/blocked"}, Deny: true},
			{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Root: "/root"},
		},
	}}}
	r, err := New(cfg, nil, nil, nil, nil) // no content builders, no fallback
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Built-in deny still works without the caller registering it.
	if rec := do(t, r, "127.0.0.1:80", "h", "/blocked/x"); rec.Code != http.StatusForbidden {
		t.Fatalf("built-in deny status = %d, want 403", rec.Code)
	}
	// Unregistered static action falls back to 501.
	if rec := do(t, r, "127.0.0.1:80", "h", "/page"); rec.Code != http.StatusNotImplemented {
		t.Fatalf("unregistered action status = %d, want 501", rec.Code)
	}
}
