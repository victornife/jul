package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/config"
)

// routeTestCfg builds a config with a named vhost, an /api/ proxy to a named
// upstream, and a default static root, exercising host + location matching.
func routeTestCfg() *config.Config {
	return &config.Config{
		Servers: []config.ServerConfig{{
			Listen:      ":8080",
			ServerNames: []string{"example.com"},
			Locations: []config.LocationConfig{
				{Match: config.MatchConfig{Path: "/api/", Type: "prefix"}, ProxyPass: "http://api", Cache: true},
				{Match: config.MatchConfig{Path: "/", Type: "prefix"}, Root: "./public"},
			},
		}},
		Upstreams: []config.UpstreamConfig{{
			Name:    "api",
			Servers: []config.UpstreamServer{{Address: "10.0.0.1:80", Weight: 1}},
		}},
	}
}

func postRouteTest(t *testing.T, s *Server, body string) routeTestResult {
	t.Helper()
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/routes/test", strings.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var out routeTestResult
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestRouteTestMatchesProxy(t *testing.T) {
	cfg := routeTestCfg()
	s := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
	})
	out := postRouteTest(t, s, `{"method":"GET","path":"/api/users","host":"example.com"}`)
	if !out.Matched {
		t.Fatalf("expected a match; got %+v", out)
	}
	if out.Action != "proxy" {
		t.Errorf("action = %q, want proxy", out.Action)
	}
	if out.Upstream != "api" {
		t.Errorf("upstream = %q, want api", out.Upstream)
	}
	if out.Match != "/api/" {
		t.Errorf("match = %q, want /api/", out.Match)
	}
	if !out.Cache {
		t.Error("cache flag should be set for the /api/ location")
	}
	if out.Explanation == "" {
		t.Error("explanation should not be empty")
	}
}

func TestRouteTestFallsBackToStaticRoot(t *testing.T) {
	cfg := routeTestCfg()
	s := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
	})
	out := postRouteTest(t, s, `{"path":"/index.html","host":"example.com"}`)
	if !out.Matched || out.Action != "static" {
		t.Fatalf("expected static match; got %+v", out)
	}
}

func TestRouteTestNoHostMatchUsesDefaultServer(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.ServerConfig{{
			Listen: ":8080",
			Locations: []config.LocationConfig{
				{Match: config.MatchConfig{Path: "/", Type: "prefix"}, Root: "./public"},
			},
		}},
	}
	s := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
	})
	out := postRouteTest(t, s, `{"path":"/","host":"unknown.test"}`)
	if !out.Matched {
		t.Fatalf("default server should match any host; got %+v", out)
	}
}

func TestRouteTestNoLocationMatch(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.ServerConfig{{
			Listen:      ":8080",
			ServerNames: []string{"example.com"},
			Locations: []config.LocationConfig{
				{Match: config.MatchConfig{Path: "/api/", Type: "prefix"}, ProxyPass: "http://api"},
			},
		}},
		Upstreams: []config.UpstreamConfig{{Name: "api", Servers: []config.UpstreamServer{{Address: "10.0.0.1:80"}}}},
	}
	s := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
	})
	out := postRouteTest(t, s, `{"path":"/nope","host":"example.com"}`)
	if out.Matched {
		t.Fatalf("expected no location match; got %+v", out)
	}
	if !strings.Contains(out.Explanation, "404") {
		t.Errorf("explanation should mention 404; got %q", out.Explanation)
	}
}

func TestRouteTestMethodNotAllowed(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/routes/test", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

func TestRouteTestExactBeatsPrefix(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.ServerConfig{{
			Listen:      ":8080",
			ServerNames: []string{"example.com"},
			Locations: []config.LocationConfig{
				{Match: config.MatchConfig{Path: "/health", Type: "exact"}, Return: 200},
				{Match: config.MatchConfig{Path: "/", Type: "prefix"}, Root: "./public"},
			},
		}},
	}
	s := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
	})
	out := postRouteTest(t, s, `{"path":"/health","host":"example.com"}`)
	if !out.Matched || out.MatchType != "exact" || out.Action != "return" {
		t.Fatalf("exact match should win; got %+v", out)
	}
}

// ── enriched projection coverage ─────────────────────────────────────────────

func TestRouteProjectionFlagsUpstreamAndWarnings(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.ServerConfig{{
			Listen: ":8080",
			Locations: []config.LocationConfig{
				{Match: config.MatchConfig{Path: "/api/", Type: "prefix"}, ProxyPass: "http://missing", Cache: true},
			},
		}},
	}
	s := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
	})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/routes", nil))
	var out []RouteProjection
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	loc := out[0].Locations[0]
	if loc.Upstream != "missing" {
		t.Errorf("upstream = %q, want missing", loc.Upstream)
	}
	if len(loc.Warnings) == 0 {
		t.Error("expected warnings for missing upstream + disabled cache")
	}
}

func TestAppProjectionDetailFields(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.ServerConfig{{
			Listen: ":8080",
			Locations: []config.LocationConfig{
				{Match: config.MatchConfig{Path: "/", Type: "prefix"}, ProxyPass: "http://api"},
			},
		}},
		Upstreams: []config.UpstreamConfig{{
			Name:        "api",
			Strategy:    "round_robin",
			Servers:     []config.UpstreamServer{{Address: "10.0.0.1:80", Weight: 1}},
			HealthCheck: &config.HealthCheckConfig{Enabled: true, Type: "http", Path: "/healthz"},
		}},
	}
	s := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
	})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/apps", nil))
	var out []AppProjection
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out[0].HealthCheckPath != "/healthz" {
		t.Errorf("health_check_path = %q, want /healthz", out[0].HealthCheckPath)
	}
	if len(out[0].RoutesUsing) != 1 {
		t.Errorf("routes_using = %v, want one entry", out[0].RoutesUsing)
	}
}

// ensure body reader path is exercised with bytes too
func TestRouteTestAcceptsBytesBody(t *testing.T) {
	cfg := routeTestCfg()
	s := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
	})
	rr := httptest.NewRecorder()
	body := bytes.NewReader([]byte(`{"path":"/api/x","host":"example.com"}`))
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/routes/test", body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
}
