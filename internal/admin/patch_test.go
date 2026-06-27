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

// TestApplyPatchLocationWAFSetAndClear proves a per-location WAF override can be
// created, updated, and removed through the structured patch ops, so the guided
// editor never has to splice nested [[servers.locations.waf]] TOML by hand.
func TestApplyPatchLocationWAFSetAndClear(t *testing.T) {
	c := patchTestConfig()
	loc := &c.Servers[0].Locations[0]
	if loc.WAF != nil {
		t.Fatal("fixture should start without a per-location WAF override")
	}

	// Create: a fresh override with block mode + CRS.
	summary, err := applyPatch(c, patchRequest{
		Op: "location_waf_set", Listen: ":8080", MatchType: "prefix", Path: "/api",
		WAF: &locationWAF{Enabled: true, Mode: "block", CRSEnabled: true},
	})
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if loc.WAF == nil || !loc.WAF.Enabled || loc.WAF.Mode != "block" || !loc.WAF.CRSEnabled {
		t.Fatalf("override not created as expected: %+v", loc.WAF)
	}
	if summary == "" {
		t.Error("expected a non-empty summary")
	}

	// Update: switch to detect, drop CRS — same override is mutated in place.
	if _, err := applyPatch(c, patchRequest{
		Op: "location_waf_set", Listen: ":8080", MatchType: "prefix", Path: "/api",
		WAF: &locationWAF{Enabled: true, Mode: "detect", CRSEnabled: false},
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if loc.WAF == nil || loc.WAF.Mode != "detect" || loc.WAF.CRSEnabled {
		t.Fatalf("override not updated: %+v", loc.WAF)
	}

	// Clear: the override is removed so the location inherits the global policy.
	if _, err := applyPatch(c, patchRequest{
		Op: "location_waf_clear", Listen: ":8080", MatchType: "prefix", Path: "/api",
	}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if loc.WAF != nil {
		t.Errorf("override not cleared: %+v", loc.WAF)
	}
}

// TestApplyPatchLocationWAFSetDefaultsMode proves an empty mode defaults to
// "block" so a minimal payload produces an enforcing override.
func TestApplyPatchLocationWAFSetDefaultsMode(t *testing.T) {
	c := patchTestConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "location_waf_set", Listen: ":8080", MatchType: "prefix", Path: "/api",
		WAF: &locationWAF{Enabled: true},
	}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got := c.Servers[0].Locations[0].WAF; got == nil || got.Mode != "block" {
		t.Fatalf("empty mode should default to block, got %+v", got)
	}
}

// TestApplyPatchLocationWAFSetPreservesAdvanced proves that editing the three
// surfaced knobs (enabled/mode/CRS) leaves advanced SecLang fields the editor
// does not display — inline rules, rule files, block status, paranoia — intact,
// so a structured edit never silently wipes hand-written rules.
func TestApplyPatchLocationWAFSetPreservesAdvanced(t *testing.T) {
	c := patchTestConfig()
	c.Servers[0].Locations[0].WAF = &config.WAFConfig{
		Enabled:         true,
		Mode:            "block",
		BlockStatus:     429,
		Paranoia:        2,
		DirectivesFiles: []string{"/etc/jul/waf/custom.conf"},
		InlineRules:     `SecRule REQUEST_URI "@contains /x" "id:200,phase:1,deny"`,
	}

	if _, err := applyPatch(c, patchRequest{
		Op: "location_waf_set", Listen: ":8080", MatchType: "prefix", Path: "/api",
		WAF: &locationWAF{Enabled: true, Mode: "detect", CRSEnabled: true},
	}); err != nil {
		t.Fatalf("set: %v", err)
	}
	got := c.Servers[0].Locations[0].WAF
	if got.Mode != "detect" || !got.CRSEnabled {
		t.Errorf("surfaced knobs not applied: %+v", got)
	}
	if got.BlockStatus != 429 || got.Paranoia != 2 ||
		len(got.DirectivesFiles) != 1 || got.DirectivesFiles[0] != "/etc/jul/waf/custom.conf" ||
		got.InlineRules == "" {
		t.Errorf("advanced fields were clobbered: %+v", got)
	}
}

// TestApplyPatchLocationWAFErrors proves the WAF ops reject malformed payloads
// and missing targets without mutating the config.
func TestApplyPatchLocationWAFErrors(t *testing.T) {
	cases := []patchRequest{
		// nil payload
		{Op: "location_waf_set", Listen: ":8080", MatchType: "prefix", Path: "/api"},
		// invalid mode
		{Op: "location_waf_set", Listen: ":8080", MatchType: "prefix", Path: "/api", WAF: &locationWAF{Enabled: true, Mode: "warn"}},
		// no such route
		{Op: "location_waf_set", Listen: ":9999", MatchType: "prefix", Path: "/api", WAF: &locationWAF{Enabled: true}},
		// clear with no existing override
		{Op: "location_waf_clear", Listen: ":8080", MatchType: "prefix", Path: "/api"},
		// clear on a missing route
		{Op: "location_waf_clear", Listen: ":9999", MatchType: "prefix", Path: "/api"},
	}
	for _, req := range cases {
		c := patchTestConfig()
		if _, err := applyPatch(c, req); err == nil {
			t.Errorf("expected error for op %q payload %+v", req.Op, req.WAF)
		}
		if c.Servers[0].Locations[0].WAF != nil {
			t.Errorf("a rejected op must not create an override: %+v", c.Servers[0].Locations[0].WAF)
		}
	}
}

// TestApplyPatchLocationSetAuth proves each auth method builds the right
// config.AuthConfig and that clear removes the rule, so the guided auth editor
// never has to splice nested [servers.locations.auth] TOML by hand.
func TestApplyPatchLocationSetAuth(t *testing.T) {
	loc := func(c *config.Config) *config.LocationConfig { return &c.Servers[0].Locations[0] }

	t.Run("cidr", func(t *testing.T) {
		c := patchTestConfig()
		if _, err := applyPatch(c, patchRequest{
			Op: "location_set_auth", Listen: ":8080", MatchType: "prefix", Path: "/api",
			Auth: &locationAuth{Method: "cidr", Allow: []string{"10.0.0.0/8", " "}, Deny: []string{"10.1.2.3/32"}},
		}); err != nil {
			t.Fatalf("apply: %v", err)
		}
		a := loc(c).Auth
		if a == nil || len(a.Allow) != 1 || a.Allow[0] != "10.0.0.0/8" || len(a.Deny) != 1 {
			t.Fatalf("cidr auth not built (blanks should be dropped): %+v", a)
		}
	})

	t.Run("basic", func(t *testing.T) {
		c := patchTestConfig()
		if _, err := applyPatch(c, patchRequest{
			Op: "location_set_auth", Listen: ":8080", MatchType: "prefix", Path: "/api",
			Auth: &locationAuth{Method: "basic", BasicFile: "/etc/jul/htpasswd", BasicRealm: "Staff"},
		}); err != nil {
			t.Fatalf("apply: %v", err)
		}
		a := loc(c).Auth
		if a == nil || a.Basic == nil || a.Basic.File != "/etc/jul/htpasswd" || a.Basic.Realm != "Staff" {
			t.Fatalf("basic auth not built: %+v", a)
		}
	})

	t.Run("jwt", func(t *testing.T) {
		c := patchTestConfig()
		if _, err := applyPatch(c, patchRequest{
			Op: "location_set_auth", Listen: ":8080", MatchType: "prefix", Path: "/api",
			Auth: &locationAuth{Method: "jwt", JWTJWKSURL: "https://idp/jwks", JWTIssuer: "iss", JWTAudience: "aud"},
		}); err != nil {
			t.Fatalf("apply: %v", err)
		}
		a := loc(c).Auth
		if a == nil || a.JWT == nil || a.JWT.JWKSURL != "https://idp/jwks" || a.JWT.Issuer != "iss" || a.JWT.Audience != "aud" {
			t.Fatalf("jwt auth not built: %+v", a)
		}
	})

	t.Run("forward", func(t *testing.T) {
		c := patchTestConfig()
		if _, err := applyPatch(c, patchRequest{
			Op: "location_set_auth", Listen: ":8080", MatchType: "prefix", Path: "/api",
			Auth: &locationAuth{Method: "forward", ForwardURL: "https://authz/verify"},
		}); err != nil {
			t.Fatalf("apply: %v", err)
		}
		a := loc(c).Auth
		if a == nil || a.ForwardAuth == nil || a.ForwardAuth.URL != "https://authz/verify" {
			t.Fatalf("forward auth not built: %+v", a)
		}
	})

	t.Run("set replaces wholesale then clear removes", func(t *testing.T) {
		c := patchTestConfig()
		c.Servers[0].Locations[0].Auth = &config.AuthConfig{Allow: []string{"1.2.3.0/24"}}
		// Setting jwt replaces the prior cidr rule entirely.
		if _, err := applyPatch(c, patchRequest{
			Op: "location_set_auth", Listen: ":8080", MatchType: "prefix", Path: "/api",
			Auth: &locationAuth{Method: "jwt", JWTJWKSURL: "https://idp/jwks"},
		}); err != nil {
			t.Fatalf("set: %v", err)
		}
		if a := loc(c).Auth; a.JWT == nil || len(a.Allow) != 0 {
			t.Fatalf("set did not replace wholesale: %+v", a)
		}
		if _, err := applyPatch(c, patchRequest{
			Op: "location_clear_auth", Listen: ":8080", MatchType: "prefix", Path: "/api",
		}); err != nil {
			t.Fatalf("clear: %v", err)
		}
		if loc(c).Auth != nil {
			t.Errorf("auth not cleared: %+v", loc(c).Auth)
		}
	})
}

// TestApplyPatchLocationAuthErrors proves malformed payloads and missing targets
// are rejected without mutating the config.
func TestApplyPatchLocationAuthErrors(t *testing.T) {
	cases := []patchRequest{
		{Op: "location_set_auth", Listen: ":8080", MatchType: "prefix", Path: "/api"},                                                              // nil payload
		{Op: "location_set_auth", Listen: ":8080", MatchType: "prefix", Path: "/api", Auth: &locationAuth{Method: "cidr"}},                         // empty cidr
		{Op: "location_set_auth", Listen: ":8080", MatchType: "prefix", Path: "/api", Auth: &locationAuth{Method: "basic"}},                        // missing file
		{Op: "location_set_auth", Listen: ":8080", MatchType: "prefix", Path: "/api", Auth: &locationAuth{Method: "jwt"}},                          // missing jwks
		{Op: "location_set_auth", Listen: ":8080", MatchType: "prefix", Path: "/api", Auth: &locationAuth{Method: "forward"}},                      // missing url
		{Op: "location_set_auth", Listen: ":8080", MatchType: "prefix", Path: "/api", Auth: &locationAuth{Method: "saml"}},                         // unknown method
		{Op: "location_set_auth", Listen: ":9999", MatchType: "prefix", Path: "/api", Auth: &locationAuth{Method: "jwt", JWTJWKSURL: "https://x"}}, // no route
		{Op: "location_clear_auth", Listen: ":8080", MatchType: "prefix", Path: "/api"},                                                            // nothing to clear
	}
	for _, req := range cases {
		c := patchTestConfig()
		if _, err := applyPatch(c, req); err == nil {
			t.Errorf("expected error for %q %+v", req.Op, req.Auth)
		}
		if c.Servers[0].Locations[0].Auth != nil {
			t.Errorf("a rejected op must not set auth: %+v", c.Servers[0].Locations[0].Auth)
		}
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

// tlsPatchConfig is patchTestConfig with TLS enabled on the :8080 server, used
// by the HTTP/3 and h2c toggle tests (HTTP/3 requires TLS; h2c forbids it).
func tlsPatchConfig() *config.Config {
	c := patchTestConfig()
	c.Servers[0].TLS = &config.TLSConfig{Enabled: true, Cert: "/etc/cert.pem", Key: "/etc/key.pem"}
	return c
}

func TestApplyPatchServerToggleHTTP3(t *testing.T) {
	t.Run("enable on a TLS server", func(t *testing.T) {
		c := tlsPatchConfig()
		if _, err := applyPatch(c, patchRequest{Op: "server_toggle_http3", Listen: ":8080", Enabled: boolPtr(true)}); err != nil {
			t.Fatalf("apply: %v", err)
		}
		if c.Servers[0].HTTP3 == nil || !c.Servers[0].HTTP3.Enabled {
			t.Error("HTTP/3 not enabled")
		}
	})
	t.Run("disable removes the block", func(t *testing.T) {
		c := tlsPatchConfig()
		c.Servers[0].HTTP3 = &config.HTTP3Config{Enabled: true}
		if _, err := applyPatch(c, patchRequest{Op: "server_toggle_http3", Listen: ":8080", Enabled: boolPtr(false)}); err != nil {
			t.Fatalf("apply: %v", err)
		}
		if c.Servers[0].HTTP3 != nil {
			t.Error("HTTP/3 block not removed on disable")
		}
	})
	t.Run("errors", func(t *testing.T) {
		cases := []struct {
			name string
			cfg  *config.Config
			req  patchRequest
		}{
			{"enable without TLS", patchTestConfig(), patchRequest{Op: "server_toggle_http3", Listen: ":8080", Enabled: boolPtr(true)}},
			{"nil enabled", tlsPatchConfig(), patchRequest{Op: "server_toggle_http3", Listen: ":8080"}},
			{"no server", tlsPatchConfig(), patchRequest{Op: "server_toggle_http3", Listen: ":9999", Enabled: boolPtr(true)}},
		}
		for _, tc := range cases {
			if _, err := applyPatch(tc.cfg, tc.req); err == nil {
				t.Errorf("%s: expected error", tc.name)
			}
		}
	})
}

func TestApplyPatchServerToggleH2C(t *testing.T) {
	t.Run("enable on a plaintext server", func(t *testing.T) {
		c := patchTestConfig()
		if _, err := applyPatch(c, patchRequest{Op: "server_toggle_h2c", Listen: ":8080", Enabled: boolPtr(true)}); err != nil {
			t.Fatalf("apply: %v", err)
		}
		if !c.Servers[0].H2C {
			t.Error("h2c not enabled")
		}
	})
	t.Run("disable", func(t *testing.T) {
		c := patchTestConfig()
		c.Servers[0].H2C = true
		if _, err := applyPatch(c, patchRequest{Op: "server_toggle_h2c", Listen: ":8080", Enabled: boolPtr(false)}); err != nil {
			t.Fatalf("apply: %v", err)
		}
		if c.Servers[0].H2C {
			t.Error("h2c not disabled")
		}
	})
	t.Run("errors", func(t *testing.T) {
		cases := []struct {
			name string
			cfg  *config.Config
			req  patchRequest
		}{
			{"enable on a TLS server", tlsPatchConfig(), patchRequest{Op: "server_toggle_h2c", Listen: ":8080", Enabled: boolPtr(true)}},
			{"nil enabled", patchTestConfig(), patchRequest{Op: "server_toggle_h2c", Listen: ":8080"}},
			{"no server", patchTestConfig(), patchRequest{Op: "server_toggle_h2c", Listen: ":9999", Enabled: boolPtr(true)}},
		}
		for _, tc := range cases {
			if _, err := applyPatch(tc.cfg, tc.req); err == nil {
				t.Errorf("%s: expected error", tc.name)
			}
		}
	})
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
