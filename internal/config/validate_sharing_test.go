// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"jul/internal/config"
	"jul/internal/router"
)

func TestValidateSharingBlock(t *testing.T) {
	t.Run("absent sharing block is valid with or without route_id", func(t *testing.T) {
		raw := `
[[servers]]
listen = "127.0.0.1:8080"
  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  return = 200
`
		cfg, err := config.Parse([]byte(raw))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if err := config.Validate(cfg); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})

	t.Run("testdata/sharing.toml parses, validates, and produces zero lint warnings", func(t *testing.T) {
		raw, err := os.ReadFile("../../testdata/sharing.toml")
		if err != nil {
			t.Fatalf("ReadFile(testdata/sharing.toml): %v", err)
		}
		cfg, err := config.Parse(raw)
		if err != nil {
			t.Fatalf("Parse(testdata/sharing.toml): %v", err)
		}
		if err := config.Validate(cfg); err != nil {
			t.Fatalf("Validate(testdata/sharing.toml): %v", err)
		}
		if diags := config.LintSharingFindings(cfg); len(diags) != 0 {
			t.Fatalf("expected zero sharing findings on testdata/sharing.toml, got: %+v", diags)
		}
	})

	t.Run("valid sharing block round-trips through Marshal and Clone", func(t *testing.T) {
		raw := `
[[servers]]
listen = "127.0.0.1:8080"
  [[servers.locations]]
  route_id = "dashboard"
  match = { type = "prefix", path = "/" }
  return = 200
    [servers.locations.sharing]
    mode = "restricted"
    audience_label = "family & friends"
    external_enforcer = "cloudflare-access:dash-team"
`
		cfg, err := config.Parse([]byte(raw))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if err := config.Validate(cfg); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		sh := cfg.Servers[0].Locations[0].Sharing
		if sh == nil || sh.Mode != "restricted" || sh.AudienceLabelValue() != "family & friends" || sh.ExternalEnforcerValue() != "cloudflare-access:dash-team" {
			t.Fatalf("unexpected Sharing: %+v", sh)
		}
		cloned, err := cfg.Clone()
		if err != nil {
			t.Fatalf("Clone: %v", err)
		}
		csh := cloned.Servers[0].Locations[0].Sharing
		if csh == nil || csh.Mode != sh.Mode || csh.AudienceLabelValue() != sh.AudienceLabelValue() || csh.ExternalEnforcerValue() != sh.ExternalEnforcerValue() {
			t.Fatalf("Clone lost Sharing fields: %+v", csh)
		}
		var nilSh *config.SharingConfig
		if nilSh.AudienceLabelValue() != "" || nilSh.ExternalEnforcerValue() != "" {
			t.Fatal("nil SharingConfig helpers must return empty string")
		}
	})

	t.Run("sharing without route_id fails validation naming the location", func(t *testing.T) {
		raw := `
[[servers]]
listen = "127.0.0.1:8080"
  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  return = 200
    [servers.locations.sharing]
    mode = "public"
`
		cfg, err := config.Parse([]byte(raw))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		err = config.Validate(cfg)
		if err == nil || !strings.Contains(err.Error(), "servers[0].locations[0].sharing") || !strings.Contains(err.Error(), "route_id") {
			t.Fatalf("expected route_id error naming servers[0].locations[0].sharing, got: %v", err)
		}
	})

	t.Run("missing or invalid mode and invalid labels are rejected", func(t *testing.T) {
		cases := []struct {
			name    string
			sharing config.SharingConfig
			wantSub string
		}{
			{
				name:    "empty mode",
				sharing: config.SharingConfig{},
				wantSub: "sharing.mode: mode is required",
			},
			{
				name:    "unknown mode",
				sharing: config.SharingConfig{Mode: "internal_only"},
				wantSub: "sharing.mode: invalid mode",
			},
			{
				name:    "present empty audience_label",
				sharing: config.SharingConfig{Mode: "public", AudienceLabel: config.String("")},
				wantSub: "sharing.audience_label: value is present and empty",
			},
			{
				name:    "oversized audience_label",
				sharing: config.SharingConfig{Mode: "public", AudienceLabel: config.String(strings.Repeat("a", 65))},
				wantSub: "sharing.audience_label",
			},
			{
				name:    "control char in audience_label",
				sharing: config.SharingConfig{Mode: "public", AudienceLabel: config.String("team\nlist")},
				wantSub: "sharing.audience_label: value contains control",
			},
			{
				name:    "invalid utf8 in external_enforcer",
				sharing: config.SharingConfig{Mode: "restricted", ExternalEnforcer: config.String(string([]byte{0xff, 0xfe}))},
				wantSub: "sharing.external_enforcer: value must be valid UTF-8",
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				cfg := &config.Config{
					Servers: []config.ServerConfig{{
						Listen: "127.0.0.1:8080",
						Locations: []config.LocationConfig{{
							RouteID: config.String("r1"),
							Match:   config.MatchConfig{Type: "prefix", Path: "/"},
							Return:  200,
							Sharing: &tc.sharing,
						}},
					}},
				}
				err := config.Validate(cfg)
				if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
					t.Fatalf("Validate error = %v, want substring %q", err, tc.wantSub)
				}
			})
		}
	})
}

func TestBindScopeAndModeOrdering(t *testing.T) {
	scopeCases := []struct {
		listen string
		want   string
	}{
		{"127.0.0.1:8080", config.BindScopeLoopback},
		{"127.0.0.53:8080", config.BindScopeLoopback},
		{"[::1]:8080", config.BindScopeLoopback},
		{"localhost:8080", config.BindScopeLoopback},
		{"10.1.2.3:8080", config.BindScopePrivateAddress},
		{"172.16.5.4:8080", config.BindScopePrivateAddress},
		{"192.168.1.10:8080", config.BindScopePrivateAddress},
		{"[fd00::1]:8080", config.BindScopePrivateAddress},
		{"169.254.10.20:8080", config.BindScopePrivateAddress},
		{"[fe80::1%eth0]:8080", config.BindScopePrivateAddress},
		{"100.64.1.2:8080", config.BindScopeSharedAddressSpace},
		{"100.100.100.100:8080", config.BindScopeSharedAddressSpace},
		{":8080", config.BindScopeAllInterfaces},
		{"0.0.0.0:8080", config.BindScopeAllInterfaces},
		{"[::]:8080", config.BindScopeAllInterfaces},
		{"203.0.113.5:443", config.BindScopePublicAddress},
		{"[2001:db8::1]:443", config.BindScopePublicAddress},
		{"${env:LISTEN_ADDR}", config.BindScopeUnknown},
		{"myhost.internal:8080", config.BindScopeUnknown},
		{"not-a-host-port", config.BindScopeUnknown},
		{"", config.BindScopeUnknown},
	}
	for _, tc := range scopeCases {
		if got := config.BindScope(tc.listen); got != tc.want {
			t.Errorf("BindScope(%q) = %q, want %q", tc.listen, got, tc.want)
		}
	}

	if !config.IsSharingModeWider("preview", "private_network") ||
		!config.IsSharingModeWider("private_network", "restricted") ||
		!config.IsSharingModeWider("restricted", "public") ||
		!config.IsSharingModeWider("not_declared", "public") {
		t.Fatal("expected IsSharingModeWider to report true for wider transitions")
	}
	if config.IsSharingModeWider("restricted", "private_network") ||
		config.IsSharingModeWider("not_declared", "restricted") ||
		config.IsSharingModeWider("public", "not_declared") {
		t.Fatal("expected IsSharingModeWider to report false for narrower or undeclared transitions")
	}

	// Host and path overlap matrix.
	if !config.IsCatchAllServerNames(nil) || !config.IsCatchAllServerNames([]string{"*"}) || config.IsCatchAllServerNames([]string{"a.example"}) {
		t.Fatal("unexpected IsCatchAllServerNames")
	}
	overlapCases := []struct {
		a, b string
		want bool
	}{
		{"", "a.example", false},
		{"*", "a.example", true},
		{"a.example", "A.EXAMPLE.", true},
		{"*.example.com", "api.example.com", true},
		{"api.example.com", "*.example.com", true},
		{"*.example.com", "example.com", false},
		{"*.corp.example.com", "*.example.com", true},
		{"*.example.com", "*.corp.example.com", true},
		{"*.a.example", "*.b.example", false},
		{"*..", "*.example.com", false},
	}
	for _, tc := range overlapCases {
		if got := config.HostPatternsOverlap(tc.a, tc.b); got != tc.want {
			t.Errorf("HostPatternsOverlap(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
	if !config.ServerHostSetsOverlap(nil, []string{"a.example"}) ||
		!config.ServerHostSetsOverlap([]string{"a.example"}, []string{"*.example"}) ||
		config.ServerHostSetsOverlap([]string{"a.example"}, []string{"b.other"}) {
		t.Fatal("unexpected ServerHostSetsOverlap")
	}

	pathCases := []struct {
		a, b config.MatchConfig
		want bool
	}{
		{config.MatchConfig{Type: "exact", Path: "/a"}, config.MatchConfig{Type: "exact", Path: "/a"}, true},
		{config.MatchConfig{Type: "exact", Path: "/a"}, config.MatchConfig{Type: "exact", Path: "/b"}, false},
		{config.MatchConfig{Type: "prefix", Path: "/api"}, config.MatchConfig{Type: "exact", Path: "/api/v1"}, true},
		{config.MatchConfig{Type: "exact", Path: "/api/v1"}, config.MatchConfig{Type: "prefix", Path: "/api"}, true},
		{config.MatchConfig{Type: "regex", Path: "^/v1"}, config.MatchConfig{Type: "prefix", Path: "/"}, true},
		{config.MatchConfig{Type: "", Path: "/a"}, config.MatchConfig{Type: "", Path: "/a/b"}, true},
	}
	for _, tc := range pathCases {
		if got := config.MatchPathsOverlap(tc.a, tc.b); got != tc.want {
			t.Errorf("MatchPathsOverlap(%+v, %+v) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}

	// Client cert check & bypass helpers.
	srvReq := config.ServerConfig{
		Plugins: []string{"mw"},
		TLS: &config.TLSConfig{
			Enabled:    true,
			ClientAuth: &config.ClientAuthConfig{Mode: "request"},
		},
	}
	srvMust := config.ServerConfig{
		TLS: &config.TLSConfig{
			Enabled:    true,
			ClientAuth: &config.ClientAuthConfig{Mode: "require"},
		},
	}
	if config.LocationHasClientCertCheck(srvReq, config.LocationConfig{}) {
		t.Fatal("request mode without require_client_cert must be false")
	}
	if !config.LocationHasClientCertCheck(srvReq, config.LocationConfig{RequireClientCert: true}) {
		t.Fatal("request mode with require_client_cert must be true")
	}
	if !config.LocationHasClientCertCheck(srvMust, config.LocationConfig{}) {
		t.Fatal("require mode must be true")
	}
	bypass := config.LocationCheckBypassKinds(srvReq, config.LocationConfig{
		CORS: &config.CORSConfig{Enabled: true},
	})
	if len(bypass) != 2 {
		t.Fatalf("expected 2 bypass kinds (request_plugin, cors_preflight), got %v", bypass)
	}

	// LintLocationSharingFindings edge cases (out of bounds, secret ref mode, unknown bind scope).
	if diags, complete := config.LintLocationSharingFindings(nil, 0, 0); len(diags) != 0 || !complete {
		t.Fatal("expected nil config to return empty, complete=true")
	}
	refCfg := &config.Config{
		Servers: []config.ServerConfig{{
			Listen: "${env:LISTEN_ADDR}",
			Locations: []config.LocationConfig{
				{
					RouteID: config.String("r1"),
					Sharing: &config.SharingConfig{Mode: "${env:SHARE_MODE}"},
				},
				{
					RouteID: config.String("r2"),
					Sharing: &config.SharingConfig{Mode: "preview"},
				},
			},
		}},
	}
	if _, complete := config.LintLocationSharingFindings(refCfg, 0, 0); complete {
		t.Fatal("expected secret ref in sharing.mode to report complete=false")
	}
	if _, complete := config.LintLocationSharingFindings(refCfg, 0, 1); complete {
		t.Fatal("expected secret ref in listen to report complete=false")
	}
}

func TestLintSharingIntentFindings(t *testing.T) {
	t.Run("intent_restricted_unenforced unchanged when external_enforcer is set (SD-12)", func(t *testing.T) {
		cfg := &config.Config{
			Servers: []config.ServerConfig{{
				Listen: "0.0.0.0:8080",
				Locations: []config.LocationConfig{
					{
						RouteID: config.String("dash-no-enf"),
						Match:   config.MatchConfig{Type: "prefix", Path: "/a"},
						Return:  200,
						Sharing: &config.SharingConfig{Mode: "restricted"},
					},
					{
						RouteID: config.String("dash-with-enf"),
						Match:   config.MatchConfig{Type: "prefix", Path: "/b"},
						Return:  200,
						Sharing: &config.SharingConfig{
							Mode:             "restricted",
							ExternalEnforcer: config.String("cloudflare-access:dash"),
						},
					},
				},
			}},
		}
		diags := config.LintSharingFindings(cfg)
		if len(diags) != 2 {
			t.Fatalf("expected 2 diagnostics, got %+v", diags)
		}
		for _, d := range diags {
			if d.Code != config.FindingIntentRestrictedUnenforced || d.Severity != config.SeverityWarning {
				t.Fatalf("unexpected diagnostic: %+v", d)
			}
		}
		if !strings.Contains(diags[1].Message, "declared by the operator, not verified by Jul") {
			t.Fatalf("expected SD-12 wording in message, got: %q", diags[1].Message)
		}
	})

	t.Run("all other intent findings and cache_shared_across_entries", func(t *testing.T) {
		cfg := &config.Config{
			Cache: config.CacheConfig{Enabled: true},
			Servers: []config.ServerConfig{
				{
					Listen:      "0.0.0.0:8080",
					ServerNames: []string{"app.example.com"},
					Locations: []config.LocationConfig{
						{
							RouteID: config.String("priv-wide"),
							Match:   config.MatchConfig{Type: "prefix", Path: "/priv"},
							Return:  200,
							Cache:   true,
							Rewrites: []config.RewriteConfig{{
								Pattern:     "^/priv/old$",
								Replacement: "https://other.example/new",
								Flag:        "redirect",
							}},
							Sharing: &config.SharingConfig{Mode: "private_network"},
						},
						{
							RouteID: config.String("preview-wide"),
							Match:   config.MatchConfig{Type: "prefix", Path: "/preview"},
							Return:  200,
							Sharing: &config.SharingConfig{Mode: "preview"},
						},
						{
							RouteID: config.String("pub-checked"),
							Match:   config.MatchConfig{Type: "prefix", Path: "/pub"},
							Return:  200,
							Auth: &config.AuthConfig{
								Basic: &config.BasicAuthConfig{File: "/etc/htpasswd"},
							},
							Sharing: &config.SharingConfig{Mode: "public"},
						},
					},
				},
				{
					Listen:      "10.0.0.1:8081",
					ServerNames: []string{"*.example.com"},
					Locations: []config.LocationConfig{
						{
							RouteID: config.String("priv-cached-peer"),
							Match:   config.MatchConfig{Type: "prefix", Path: "/priv/sub"},
							Return:  200,
							Cache:   true,
						},
					},
				},
			},
		}
		diags := config.LintSharingFindings(cfg)
		gotCodes := make(map[string]bool)
		for _, d := range diags {
			gotCodes[d.Code] = true
		}
		for _, want := range []string{
			config.FindingIntentPrivateBindTooWide,
			config.FindingIntentPreviewNotLoopback,
			config.FindingIntentPublicHasChecks,
			config.FindingIntentChecksBypassed,
			config.FindingIntentRestrictedCached,
			config.FindingCacheSharedAcrossEntries,
		} {
			if !gotCodes[want] {
				t.Errorf("missing expected finding code %q in %+v", want, diags)
			}
		}
	})

	t.Run("LintWithRaw and LintSharingFindingsWithRaw preserve raw reference text and RR11 disclosure rules", func(t *testing.T) {
		rawCfg := &config.Config{
			Cache: config.CacheConfig{Enabled: true},
			Admin: config.AdminConfig{Enabled: true, Listen: "${env:ADMIN_LISTEN}"},
			Servers: []config.ServerConfig{
				{
					Listen:      "${env:APP_LISTEN}",
					ServerNames: []string{"${env:APP_HOST}"},
					TLS: &config.TLSConfig{
						Enabled:    true,
						ClientAuth: &config.ClientAuthConfig{Mode: "${file:/ca_mode}"},
					},
					Locations: []config.LocationConfig{
						{
							RouteID: config.String("r-env"),
							Match:   config.MatchConfig{Type: "prefix", Path: "${env:APP_PATH}"},
							Return:  200,
							Cache:   true,
							Auth:    &config.AuthConfig{Allow: []string{"${file:/cidr}"}},
							Sharing: &config.SharingConfig{Mode: "${env:SHARE_MODE}", ExternalEnforcer: config.String("${env:ENF}")},
						},
						{
							RouteID: config.String("r-file-mode"),
							Match:   config.MatchConfig{Type: "prefix", Path: "/f"},
							Return:  200,
							Sharing: &config.SharingConfig{Mode: "${file:/mode}"},
						},
					},
				},
				{
					Listen:      "${file:/listen_file}",
					ServerNames: []string{"${env:APP_HOST}"},
					TLS: &config.TLSConfig{
						Enabled:    true,
						ClientAuth: &config.ClientAuthConfig{Mode: "require"},
					},
					Locations: []config.LocationConfig{
						{
							RouteID: config.String("r-peer"),
							Match:   config.MatchConfig{Type: "prefix", Path: "${env:APP_PATH}"},
							Return:  200,
							Cache:   true,
							Sharing: &config.SharingConfig{Mode: "preview"},
						},
					},
				},
			},
		}
		effCfg, _ := rawCfg.Clone()
		effCfg.Admin.Listen = "0.0.0.0:9090"
		effCfg.Servers[0].Listen = "192.168.10.20:8443"
		effCfg.Servers[0].ServerNames = []string{"resolved-host.example"}
		effCfg.Servers[0].TLS.ClientAuth.Mode = "require"
		effCfg.Servers[0].Locations[0].Match.Path = "/resolved-path"
		effCfg.Servers[0].Locations[0].Auth.Allow = []string{"10.0.0.0/8"}
		effCfg.Servers[0].Locations[0].Sharing.Mode = "preview"
		effCfg.Servers[0].Locations[0].Sharing.ExternalEnforcer = config.String("resolved-enforcer")
		effCfg.Servers[0].Locations[1].Sharing.Mode = "restricted"
		effCfg.Servers[1].Listen = "192.168.10.20:8443"
		effCfg.Servers[1].ServerNames = []string{"resolved-host.example"}
		effCfg.Servers[1].Locations[0].Match.Path = "/resolved-path"

		for _, d := range config.LintWithRaw(rawCfg, effCfg, true) {
			if strings.Contains(d.Message, "192.168.10.20") || strings.Contains(d.Message, "0.0.0.0:9090") || strings.Contains(d.Message, "resolved-") {
				t.Fatalf("LintWithRaw leaked resolved reference in %+v", d)
			}
		}
		// Identical reference tokens overlap; a reference vs a different literal does not.
		if !config.ServerHostSetsOverlapLiteralOnly([]string{"${env:H}"}, []string{"${env:H}"}) {
			t.Fatal("expected identical host reference tokens to overlap")
		}
		if config.ServerHostSetsOverlapLiteralOnly([]string{"${env:H}"}, []string{"literal.example"}) {
			t.Fatal("reference host must not overlap a different literal")
		}
		if !config.MatchPathsOverlapLiteralOnly(config.MatchConfig{Path: "${env:P}"}, config.MatchConfig{Path: "${env:P}"}) {
			t.Fatal("expected identical path reference tokens to overlap")
		}
		if config.MatchPathsOverlapLiteralOnly(config.MatchConfig{Path: "${env:P}"}, config.MatchConfig{Path: "/lit"}) {
			t.Fatal("reference path must not overlap a different literal")
		}
		if !config.LocationHasClientCertCheckInConfig(effCfg, effCfg.Servers[1], effCfg.Servers[1].Locations[0]) {
			t.Fatal("expected listener-wide client cert check to be detected")
		}
		if !config.LocationHasAnyJulCheck(effCfg.Servers[1], effCfg.Servers[1].Locations[0]) {
			t.Fatal("expected LocationHasAnyJulCheck to be true when require mode is set")
		}
		// File-backed listener association or sibling block client_auth.mode must be suppressed in forPlan=true.
		if hasCert, hasFile := config.LocationHasClientCertCheckForPlan(rawCfg, effCfg, 0, effCfg.Servers[0].Locations[0]); hasCert || !hasFile {
			t.Fatalf("expected LocationHasClientCertCheckForPlan to suppress file-backed client_auth, got hasCert=%v hasFile=%v", hasCert, hasFile)
		}
		if hasCert, hasFile := config.LocationHasClientCertCheckForPlan(nil, nil, 0, config.LocationConfig{}); hasCert || hasFile {
			t.Fatalf("expected nil LocationHasClientCertCheckForPlan to return false, false")
		}
	})
}

// TestHostPatternsOverlapMatchesRealRouter proves the §9.5 host-dispatch
// normalization and overlap helper agrees with internal/router's actual host
// dispatch across exact names, leading wildcards, trailing dots, mixed case,
// surrounding whitespace, and first-declared-block fallback.
func TestHostPatternsOverlapMatchesRealRouter(t *testing.T) {
	probeHosts := []string{
		"victim.example",
		"VICTIM.EXAMPLE.",
		"a.example.com",
		"b.corp.example.com",
		"corp.example.com",
		"example.com",
		"unmatched.other.test",
	}

	buildRouterFromNames := func(blocks [][]string) *router.Router {
		srvs := make([]config.ServerConfig, len(blocks))
		for i, names := range blocks {
			tag := string(rune('A' + i))
			srvs[i] = config.ServerConfig{
				Listen:      ":8080",
				ServerNames: names,
				Locations: []config.LocationConfig{{
					Match:  config.MatchConfig{Type: "prefix", Path: "/"},
					Return: 200 + i,
					ResponseHeaders: []config.ResponseHeaderOp{{
						Op:    "set",
						Name:  "X-Block",
						Value: config.String(tag),
					}},
				}},
			}
		}
		rt, err := router.New(&config.Config{Servers: srvs}, nil, nil, nil, nil)
		if err != nil {
			t.Fatalf("router.New: %v", err)
		}
		return rt
	}

	dispatchStatus := func(rt *router.Router, host string) int {
		h := rt.For(":8080")
		req := httptest.NewRequest(http.MethodGet, "http://"+host+"/", nil)
		req.Host = host
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr.Code
	}

	beforeBlocks := [][]string{
		{" *.example.com. "},
		{"other.org"},
	}
	afterBlocks := [][]string{
		{"*.example.com"},
		{"other.org"},
		{" *.corp.example.com "},
	}

	rtBefore := buildRouterFromNames(beforeBlocks)
	rtAfter := buildRouterFromNames(afterBlocks)

	// Changed pattern set C is "*.corp.example.com" from added block 2.
	changedPattern := "*.corp.example.com"
	for _, host := range probeHosts {
		bCode := dispatchStatus(rtBefore, host)
		aCode := dispatchStatus(rtAfter, host)
		if bCode == aCode {
			continue
		}
		bIdx := bCode - 200
		aIdx := aCode - 200
		// Both the old block and the new block must either be the first-declared
		// block (index 0) or overlap changedPattern.
		bOverlaps := bIdx == 0 || config.HostPatternsOverlap(beforeBlocks[bIdx][0], changedPattern)
		aOverlaps := aIdx == 0 || config.HostPatternsOverlap(afterBlocks[aIdx][0], changedPattern)
		if !bOverlaps || !aOverlaps {
			t.Fatalf("host %q moved from block %d to %d without HostPatternsOverlap detecting it", host, bIdx, aIdx)
		}
	}
}
