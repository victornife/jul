package admin

import (
	"testing"

	"jul/internal/config"
)

// boolPtr returns a pointer to b, for building *bool patch fields in tests. It
// lives here (an untagged test file) so both the default and -tags console test
// builds can use it.
func boolPtr(b bool) *bool { return &b }

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
	if _, err := applyPatch(c, patchRequest{Op: "route_set_target", Listen: ":8080", MatchType: "prefix", Path: "/api", Target: "http://new"}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := c.Servers[0].Locations[0].ProxyPass; got != "http://new" {
		t.Errorf("proxy_pass = %q, want http://new", got)
	}
}

func TestApplyPatchRouteToggleCache(t *testing.T) {
	c := patchTestConfig()
	if _, err := applyPatch(c, patchRequest{Op: "route_toggle_cache", Listen: ":8080", MatchType: "prefix", Path: "/api", Enabled: boolPtr(true)}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !c.Servers[0].Locations[0].Cache {
		t.Error("cache not enabled")
	}
}

func TestApplyPatchRouteToggleRateLimit(t *testing.T) {
	c := patchTestConfig()
	if _, err := applyPatch(c, patchRequest{Op: "route_toggle_rate_limit", Listen: ":8080", MatchType: "prefix", Path: "/api", Enabled: boolPtr(true)}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if c.Servers[0].Locations[0].RateLimit == nil || !c.Servers[0].Locations[0].RateLimit.Enabled {
		t.Error("rate limit not enabled")
	}
}

// TestApplyPatchRouteTargetingDisambiguation proves the route target resolves to
// exactly one location even when a listen is shared by several virtual hosts and
// a path is reused under different match types: the wrong match type or wrong
// server-name set must not be silently patched, and a request that omits the
// disambiguators when the target is ambiguous must be rejected.
func TestApplyPatchRouteTargetingDisambiguation(t *testing.T) {
	newCfg := func() *config.Config {
		return &config.Config{
			Servers: []config.ServerConfig{
				{
					Listen:      ":443",
					ServerNames: []string{"a.example"},
					Locations: []config.LocationConfig{
						{Match: config.MatchConfig{Type: "prefix", Path: "/api"}, ProxyPass: "http://a-prefix"},
						{Match: config.MatchConfig{Type: "exact", Path: "/api"}, ProxyPass: "http://a-exact"},
					},
				},
				{
					Listen:      ":443",
					ServerNames: []string{"b.example"},
					Locations: []config.LocationConfig{
						{Match: config.MatchConfig{Type: "prefix", Path: "/api"}, ProxyPass: "http://b-prefix"},
					},
				},
			},
		}
	}

	// Targets the exact-match location on vhost a.example only.
	c := newCfg()
	if _, err := applyPatch(c, patchRequest{
		Op: "route_set_target", Listen: ":443", ServerNames: []string{"a.example"},
		MatchType: "exact", Path: "/api", Target: "http://new",
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if c.Servers[0].Locations[1].ProxyPass != "http://new" {
		t.Errorf("exact location not updated: %q", c.Servers[0].Locations[1].ProxyPass)
	}
	if c.Servers[0].Locations[0].ProxyPass != "http://a-prefix" || c.Servers[1].Locations[0].ProxyPass != "http://b-prefix" {
		t.Error("a different location was modified")
	}

	// Targets vhost b.example specifically (same listen, same path+type as a's prefix).
	c = newCfg()
	if _, err := applyPatch(c, patchRequest{
		Op: "route_set_target", Listen: ":443", ServerNames: []string{"b.example"},
		MatchType: "prefix", Path: "/api", Target: "http://new-b",
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if c.Servers[1].Locations[0].ProxyPass != "http://new-b" || c.Servers[0].Locations[0].ProxyPass != "http://a-prefix" {
		t.Error("server-name disambiguation targeted the wrong vhost")
	}

	// A wrong server-name set matches nothing.
	c = newCfg()
	if _, err := applyPatch(c, patchRequest{
		Op: "route_set_target", Listen: ":443", ServerNames: []string{"missing.example"},
		MatchType: "prefix", Path: "/api", Target: "http://x",
	}); err == nil {
		t.Error("expected not-found error for an unknown server-name set")
	}

	// A wrong match type matches nothing even though the path exists.
	c = newCfg()
	if _, err := applyPatch(c, patchRequest{
		Op: "route_set_target", Listen: ":443", ServerNames: []string{"a.example"},
		MatchType: "regex", Path: "/api", Target: "http://x",
	}); err == nil {
		t.Error("expected not-found error for a mismatched match type")
	}
}

// TestApplyPatchRouteAmbiguousRejected proves a target that resolves to more
// than one location is refused rather than silently patching the first.
func TestApplyPatchRouteAmbiguousRejected(t *testing.T) {
	c := &config.Config{
		Servers: []config.ServerConfig{{
			Listen:      ":443",
			ServerNames: []string{"a.example"},
			Locations: []config.LocationConfig{
				{Match: config.MatchConfig{Type: "prefix", Path: "/api"}, ProxyPass: "http://one"},
				{Match: config.MatchConfig{Type: "prefix", Path: "/api"}, ProxyPass: "http://two"},
			},
		}},
	}
	_, err := applyPatch(c, patchRequest{
		Op: "route_set_target", Listen: ":443", ServerNames: []string{"a.example"},
		MatchType: "prefix", Path: "/api", Target: "http://x",
	})
	if err == nil {
		t.Fatal("expected an ambiguous-target error, got nil")
	}
	if c.Servers[0].Locations[0].ProxyPass != "http://one" || c.Servers[0].Locations[1].ProxyPass != "http://two" {
		t.Error("an ambiguous patch must not mutate any location")
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
