package admin

import (
	"testing"

	"jul/internal/config"
)

func patchTestConfig() *config.Config {
	return &config.Config{
		Servers: []config.ServerConfig{{
			Listen: ":8080",
			Locations: []config.LocationConfig{{
				Match:     config.MatchConfig{Type: "prefix", Path: "/api"},
				ProxyPass: "http://old",
			}},
		}},
		Upstreams: []config.UpstreamConfig{{
			Name:    "pool",
			Servers: []config.UpstreamServer{{Address: "10.0.0.1:80", Weight: 1}},
		}},
	}
}

func TestApplyPatchRouteSetTarget(t *testing.T) {
	c := patchTestConfig()
	if _, err := applyPatch(c, patchRequest{Op: "route_set_target", Listen: ":8080", Path: "/api", Target: "http://new"}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := c.Servers[0].Locations[0].ProxyPass; got != "http://new" {
		t.Errorf("proxy_pass = %q, want http://new", got)
	}
}

func TestApplyPatchRouteToggleCache(t *testing.T) {
	c := patchTestConfig()
	if _, err := applyPatch(c, patchRequest{Op: "route_toggle_cache", Listen: ":8080", Path: "/api", Enabled: boolPtr(true)}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !c.Servers[0].Locations[0].Cache {
		t.Error("cache not enabled")
	}
}

func TestApplyPatchRouteToggleRateLimit(t *testing.T) {
	c := patchTestConfig()
	if _, err := applyPatch(c, patchRequest{Op: "route_toggle_rate_limit", Listen: ":8080", Path: "/api", Enabled: boolPtr(true)}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if c.Servers[0].Locations[0].RateLimit == nil || !c.Servers[0].Locations[0].RateLimit.Enabled {
		t.Error("rate limit not enabled")
	}
}

func TestApplyPatchUpstreamAddRemoveBackend(t *testing.T) {
	c := patchTestConfig()
	if _, err := applyPatch(c, patchRequest{Op: "upstream_add_backend", Upstream: "pool", Address: "10.0.0.2:80", Weight: 3}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(c.Upstreams[0].Servers) != 2 || c.Upstreams[0].Servers[1].Weight != 3 {
		t.Fatalf("backend not added with weight: %+v", c.Upstreams[0].Servers)
	}
	if _, err := applyPatch(c, patchRequest{Op: "upstream_remove_backend", Upstream: "pool", Address: "10.0.0.2:80"}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if len(c.Upstreams[0].Servers) != 1 {
		t.Errorf("backend not removed: %+v", c.Upstreams[0].Servers)
	}
}

func TestApplyPatchServerSetLimits(t *testing.T) {
	c := patchTestConfig()
	summary, err := applyPatch(c, patchRequest{
		Op:     "server_set_limits",
		Listen: ":8080",
		Limits: &serverLimits{ClientMaxBodySize: "10m", ReadTimeout: "30s"},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if c.Servers[0].ClientMaxBodySize.Bytes() != 10*1024*1024 {
		t.Errorf("client_max_body_size = %d, want 10MiB", c.Servers[0].ClientMaxBodySize.Bytes())
	}
	if c.Servers[0].ReadTimeout.Std().String() != "30s" {
		t.Errorf("read_timeout = %s, want 30s", c.Servers[0].ReadTimeout.Std())
	}
	// Fields not supplied are left untouched (idle_timeout stays zero here).
	if c.Servers[0].IdleTimeout.Std() != 0 {
		t.Errorf("idle_timeout = %s, want 0 (untouched)", c.Servers[0].IdleTimeout.Std())
	}
	if summary == "" {
		t.Error("expected a non-empty summary")
	}
}

func TestApplyPatchServerSetLimitsErrors(t *testing.T) {
	cases := []patchRequest{
		{Op: "server_set_limits", Listen: ":8080"},                                                   // nil payload
		{Op: "server_set_limits", Listen: ":8080", Limits: &serverLimits{}},                          // no fields
		{Op: "server_set_limits", Listen: ":9999", Limits: &serverLimits{ReadTimeout: "1s"}},         // no server
		{Op: "server_set_limits", Listen: ":8080", Limits: &serverLimits{ReadTimeout: "xyz"}},        // bad duration
		{Op: "server_set_limits", Listen: ":8080", Limits: &serverLimits{ClientMaxBodySize: "nope"}}, // bad size
	}
	for _, req := range cases {
		c := patchTestConfig()
		if _, err := applyPatch(c, req); err == nil {
			t.Errorf("expected error for limits %+v", req.Limits)
		}
	}
}

func TestApplyPatchErrors(t *testing.T) {
	cases := []patchRequest{
		{Op: "route_set_target", Listen: ":9999", Path: "/x", Target: "http://x"}, // no route
		{Op: "upstream_add_backend", Upstream: "nope", Address: "1.2.3.4:80"},     // no upstream
		{Op: "upstream_remove_backend", Upstream: "pool", Address: "10.0.0.1:80"}, // last backend
		{Op: "bogus"}, // unknown op
	}
	for _, req := range cases {
		c := patchTestConfig()
		if _, err := applyPatch(c, req); err == nil {
			t.Errorf("expected error for op %q", req.Op)
		}
	}
}
