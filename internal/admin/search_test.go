package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/config"
)

func searchTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{
		Servers: []config.ServerConfig{{
			Listen: ":8080",
			Locations: []config.LocationConfig{
				{Match: config.MatchConfig{Type: "prefix", Path: "/api"}, ProxyPass: "http://backend"},
				{Match: config.MatchConfig{Type: "prefix", Path: "/static"}, Root: "/srv"},
			},
		}},
		Upstreams: []config.UpstreamConfig{
			{Name: "backend", Strategy: "round_robin", Servers: []config.UpstreamServer{{Address: "127.0.0.1:3000", Weight: 1}}},
			{Name: "lonely", Strategy: "round_robin", Servers: []config.UpstreamServer{{Address: "127.0.0.1:4000", Weight: 1}}},
		},
	}
	deps := Deps{LoadConfig: func() (*config.Config, error) { return cfg, nil }}
	return newTestServer(t, config.AdminConfig{}, deps)
}

func doSearch(t *testing.T, s *Server, query string) []SearchResult {
	t.Helper()
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/search?"+query, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var out []SearchResult
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestSearchFindsRouteByPath(t *testing.T) {
	s := searchTestServer(t)
	out := doSearch(t, s, "q=/api")
	if len(out) == 0 {
		t.Fatal("expected at least one result for /api")
	}
	found := false
	for _, r := range out {
		if r.Kind == "route" && r.Upstream == "backend" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected route → backend result, got %+v", out)
	}
}

func TestSearchFindsAppByName(t *testing.T) {
	s := searchTestServer(t)
	out := doSearch(t, s, "q=backend&type=apps")
	if len(out) == 0 {
		t.Fatal("expected app result for backend")
	}
	if out[0].Kind != "app" || out[0].Title != "backend" {
		t.Errorf("expected app backend first, got %+v", out[0])
	}
}

func TestSearchFlagsUnusedApp(t *testing.T) {
	s := searchTestServer(t)
	out := doSearch(t, s, "q=lonely&type=apps")
	if len(out) == 0 {
		t.Fatal("expected result for lonely app")
	}
	hasUnused := false
	for _, b := range out[0].Badges {
		if b == "unused" {
			hasUnused = true
		}
	}
	if !hasUnused {
		t.Errorf("expected lonely app to be flagged unused, got %+v", out[0].Badges)
	}
}

func TestSearchEmptyQueryReturnsAll(t *testing.T) {
	s := searchTestServer(t)
	out := doSearch(t, s, "q=")
	// Two routes + two apps.
	if len(out) != 4 {
		t.Errorf("expected 4 results for empty query, got %d: %+v", len(out), out)
	}
}

func TestSearchMethodNotAllowed(t *testing.T) {
	s := searchTestServer(t)
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/search", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rr.Code)
	}
}
