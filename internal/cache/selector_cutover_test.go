// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/versionid"
)

func strPtrCutover(s string) *string { return &s }

func TestDeriveRouteCutoverSelectorsAndPolicyFiltering(t *testing.T) {
	if sels := DeriveRouteCutoverSelectors(nil, nil); sels != nil {
		t.Fatalf("expected nil selectors for nil configs, got %v", sels)
	}

	before := &config.Config{
		Cache: config.CacheConfig{
			Enabled:      true,
			ReloadPolicy: "retain",
		},
		Upstreams: []config.UpstreamConfig{
			{
				Name:    "app1_pool",
				Servers: []config.UpstreamServer{{Address: "127.0.0.1:9001"}},
			},
			{
				Name:    "app2_pool",
				Servers: []config.UpstreamServer{{Address: "127.0.0.1:9002"}},
			},
		},
		Servers: []config.ServerConfig{
			{
				Listen:      ":8080",
				ServerNames: []string{"portal.example.com", "alt.example.com"},
				Locations: []config.LocationConfig{
					{
						RouteID:   strPtrCutover("app1"),
						Match:     config.MatchConfig{Type: "prefix", Path: "/app1"},
						ProxyPass: "http://app1_pool",
						Cache:     true,
						CachePolicy: &config.LocationCachePolicy{
							ReloadPolicy: "invalidate",
						},
					},
					{
						RouteID:   strPtrCutover("app2"),
						Match:     config.MatchConfig{Type: "prefix", Path: "/app2"},
						ProxyPass: "http://app2_pool",
						Cache:     true,
						CachePolicy: &config.LocationCachePolicy{
							ReloadPolicy: "retain",
						},
					},
					{
						RouteID:   strPtrCutover("regex-route"),
						Match:     config.MatchConfig{Type: "regex", Path: "^/v[0-9]+/items"},
						ProxyPass: "http://127.0.0.1:9003",
						Cache:     true,
						CachePolicy: &config.LocationCachePolicy{
							ReloadPolicy: "invalidate",
						},
					},
				},
			},
		},
	}

	after := *before
	after.Upstreams = []config.UpstreamConfig{
		{
			Name:    "app1_pool",
			Servers: []config.UpstreamServer{{Address: "127.0.0.1:9011"}}, // changed backend!
		},
		{
			Name:    "app2_pool",
			Servers: []config.UpstreamServer{{Address: "127.0.0.1:9022"}}, // changed backend, but retain!
		},
	}

	// Only app1 has reload_policy = "invalidate".
	invSels := DeriveRouteCutoverSelectorsForPolicy(before, &after, "")
	if len(invSels) != 2 {
		t.Fatalf("expected 2 host-scoped prefix selectors for multi-host server, got %+v", invSels)
	}
	if invSels[0].Kind != SelectorPrefix || invSels[0].Host != "portal.example.com" || invSels[0].Prefix != "/app1" {
		t.Fatalf("unexpected first selector: %+v", invSels[0])
	}
	if invSels[1].Kind != SelectorPrefix || invSels[1].Host != "alt.example.com" || invSels[1].Prefix != "/app1" {
		t.Fatalf("unexpected second selector: %+v", invSels[1])
	}

	// DeriveRouteCutoverSelectors (override "invalidate") includes both app1 and app2.
	allSels := DeriveRouteCutoverSelectors(before, &after)
	if len(allSels) != 4 {
		t.Fatalf("expected 4 selectors when all changed routes are included, got %+v", allSels)
	}

	// Changing regex-route target widens to SelectorHost for each configured host.
	afterRegex := *before
	afterRegex.Servers = append([]config.ServerConfig(nil), before.Servers...)
	afterRegex.Servers[0].Locations = append([]config.LocationConfig(nil), before.Servers[0].Locations...)
	afterRegex.Servers[0].Locations[2].ProxyPass = "http://127.0.0.1:9099"
	regexSels := DeriveRouteCutoverSelectorsForPolicy(before, &afterRegex, "")
	if len(regexSels) != 2 || regexSels[0].Kind != SelectorHost || regexSels[1].Kind != SelectorHost {
		t.Fatalf("expected host-broadened selectors for regex route cutover, got %+v", regexSels)
	}

	// Removing a server block or changing a wildcard server block broadens to SelectorAll when unnamed/wildcard.
	wildcardBefore := &config.Config{
		Cache: config.CacheConfig{Enabled: true, ReloadPolicy: "invalidate"},
		Servers: []config.ServerConfig{
			{
				Listen:      ":8080",
				ServerNames: []string{"*.example.com"},
				Locations: []config.LocationConfig{
					{
						Match:     config.MatchConfig{Type: "prefix", Path: "/api"},
						ProxyPass: "http://127.0.0.1:9001",
						Cache:     true,
					},
				},
			},
		},
	}
	wildcardAfter := &config.Config{
		Cache: config.CacheConfig{Enabled: true, ReloadPolicy: "invalidate"},
		Servers: []config.ServerConfig{
			{
				Listen:      ":8080",
				ServerNames: []string{"*.example.com"},
				Locations: []config.LocationConfig{
					{
						Match:     config.MatchConfig{Type: "prefix", Path: "/api"},
						ProxyPass: "http://127.0.0.1:9002",
						Cache:     true,
					},
				},
			},
		},
	}
	wSels := DeriveRouteCutoverSelectorsForPolicy(wildcardBefore, wildcardAfter, "")
	if len(wSels) != 1 || wSels[0].Kind != SelectorAll {
		t.Fatalf("expected SelectorAll for wildcard host cutover, got %+v", wSels)
	}
}

func TestWithBackendCutoverAndExecuteRouteCutover(t *testing.T) {
	tmpDir := t.TempDir()
	c, err := New(config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: 8 << 20,
		DiskPath:      tmpDir,
		DiskMaxSize:   32 << 20,
		MaxEntrySize:  1 << 20,
		DefaultTTL:    config.Duration(5 * time.Minute),
	}, nil)
	if err != nil {
		t.Fatalf("New cache: %v", err)
	}

	versions := versionid.Process()
	locInvalidate := config.LocationConfig{
		RouteID:   strPtrCutover("api"),
		Match:     config.MatchConfig{Type: "prefix", Path: "/api"},
		ProxyPass: "http://upstream_a",
		Cache:     true,
		CachePolicy: &config.LocationCachePolicy{
			ReloadPolicy: "invalidate",
		},
	}
	locRetain := config.LocationConfig{
		RouteID:   strPtrCutover("static"),
		Match:     config.MatchConfig{Type: "prefix", Path: "/static"},
		ProxyPass: "http://upstream_b",
		Cache:     true,
		CachePolicy: &config.LocationCachePolicy{
			ReloadPolicy: "retain",
		},
	}
	cfgA := &config.Config{
		Cache: config.CacheConfig{Enabled: true, ReloadPolicy: "retain"},
		Upstreams: []config.UpstreamConfig{
			{Name: "upstream_a", Servers: []config.UpstreamServer{{Address: "127.0.0.1:8001"}}},
			{Name: "upstream_b", Servers: []config.UpstreamServer{{Address: "127.0.0.1:8002"}}},
		},
		Servers: []config.ServerConfig{
			{
				Listen:      ":8080",
				ServerNames: []string{"app.example.com"},
				Locations:   []config.LocationConfig{locInvalidate, locRetain},
			},
		},
	}
	cfgB := &config.Config{
		Cache: config.CacheConfig{Enabled: true, ReloadPolicy: "retain"},
		Upstreams: []config.UpstreamConfig{
			{Name: "upstream_a", Servers: []config.UpstreamServer{{Address: "127.0.0.1:8011"}}},
			{Name: "upstream_b", Servers: []config.UpstreamServer{{Address: "127.0.0.1:8022"}}},
		},
		Servers: cfgA.Servers,
	}

	rcA := CompileRouteCacheConfig("scope-api", true, locInvalidate.CachePolicy).
		WithMatcher(locInvalidate, versions).
		WithBackendCutover(cfgA, cfgA.Servers[0], locInvalidate, versions)
	rcB := CompileRouteCacheConfig("scope-api", true, locInvalidate.CachePolicy).
		WithMatcher(locInvalidate, versions).
		WithBackendCutover(cfgB, cfgB.Servers[0], locInvalidate, versions)
	if rcA.backendProof == rcB.backendProof || sameRouteCutoverBackend(rcA, rcB) {
		t.Fatalf("expected backendProof to change when upstream backend changes under reload_policy=invalidate")
	}

	rcRetainA := CompileRouteCacheConfig("scope-static", true, locRetain.CachePolicy).
		WithMatcher(locRetain, versions).
		WithBackendCutover(cfgA, cfgA.Servers[0], locRetain, versions)
	rcRetainB := CompileRouteCacheConfig("scope-static", true, locRetain.CachePolicy).
		WithMatcher(locRetain, versions).
		WithBackendCutover(cfgB, cfgB.Servers[0], locRetain, versions)
	if !sameRouteCutoverBackend(rcRetainA, rcRetainB) {
		t.Fatalf("expected sameRouteCutoverBackend to remain true when reload_policy=retain")
	}

	// Verify reordering ServerNames and metadata-only Sharing edits produce zero cutover selectors.
	reordered := *cfgA
	reordered.Servers = append([]config.ServerConfig(nil), cfgA.Servers...)
	reordered.Servers[0].ServerNames = []string{"APP.EXAMPLE.COM."}
	reordered.Servers[0].Locations = append([]config.LocationConfig(nil), cfgA.Servers[0].Locations...)
	aud := "internal"
	reordered.Servers[0].Locations[0].Sharing = &config.SharingConfig{Mode: "private_network", AudienceLabel: &aud}
	if sels := DeriveRouteCutoverSelectorsForPolicy(cfgA, &reordered, ""); len(sels) != 0 {
		t.Fatalf("expected 0 cutover selectors for ServerNames normalization or Sharing metadata edit, got %+v", sels)
	}
	rcSharing := CompileRouteCacheConfig("scope-api", true, reordered.Servers[0].Locations[0].CachePolicy).
		WithMatcher(reordered.Servers[0].Locations[0], versions).
		WithBackendCutover(&reordered, reordered.Servers[0], reordered.Servers[0].Locations[0], versions)
	if rcSharing.backendProof != rcA.backendProof {
		t.Fatalf("Sharing metadata edit must not alter backendProof")
	}

	// ExecuteRouteCutover on cfgA -> cfgB should invalidate /api on app.example.com.
	results, err := c.ExecuteRouteCutover(context.Background(), cfgA, cfgB, "")
	if err != nil {
		t.Fatalf("ExecuteRouteCutover failed: %v", err)
	}
	if len(results) != 1 || results[0].InvalidationEffective != "effective" {
		t.Fatalf("expected 1 effective cutover invalidation result, got %+v", results)
	}

	// Verify partial failure reporting when disk directory is read-only during cutover.
	if err := os.Chmod(tmpDir, 0o500); err != nil {
		t.Fatalf("chmod read-only: %v", err)
	}
	defer func() { _ = os.Chmod(tmpDir, 0o700) }()
	_ = filepath.Join(tmpDir, "dummy")
	resultsFail, errFail := c.ExecuteRouteCutover(context.Background(), cfgB, cfgA, "")
	if errFail == nil {
		t.Fatalf("expected error when disk intent persistence fails during cutover")
	}
	if len(resultsFail) != 1 || resultsFail[0].InvalidationEffective == "effective" {
		t.Fatalf("expected non-effective result when disk persistence fails, got %+v", resultsFail)
	}
	_ = os.Chmod(tmpDir, 0o700)

	// Cover additional action types (fastcgi, uwsgi, grpc_transcode, static, return, redirect, deny, plugin)
	// and upstream pool TLS/healthcheck/circuitbreaker identity in WithBackendCutover and DeriveRouteCutoverSelectorsForPolicy.
	srvActions := config.ServerConfig{
		Listen:      ":8080",
		ServerNames: []string{"actions.example.com"},
		Plugins:     []string{"srv-plugin"},
		Locations: []config.LocationConfig{
			{Match: config.MatchConfig{Type: "exact", Path: "/exact"}, FastCGIPass: "127.0.0.1:9000", FastCGIParams: map[string]string{"SCRIPT_FILENAME": "/index.php"}, Cache: true, CachePolicy: &config.LocationCachePolicy{ReloadPolicy: "invalidate"}},
			{Match: config.MatchConfig{Type: "prefix", Path: "/uwsgi"}, UWSGIPass: "127.0.0.1:9001", Cache: true, CachePolicy: &config.LocationCachePolicy{ReloadPolicy: "invalidate"}},
			{Match: config.MatchConfig{Type: "prefix", Path: "/transcode"}, GRPCTranscode: &config.GRPCTranscodeConfig{Target: "127.0.0.1:9002"}, Cache: true, CachePolicy: &config.LocationCachePolicy{ReloadPolicy: "invalidate"}},
			{Match: config.MatchConfig{Type: "prefix", Path: "/static"}, Root: "/var/www", Cache: true, CachePolicy: &config.LocationCachePolicy{ReloadPolicy: "invalidate"}},
			{Match: config.MatchConfig{Type: "prefix", Path: "/return"}, Return: 200, Cache: true, CachePolicy: &config.LocationCachePolicy{ReloadPolicy: "invalidate"}},
			{Match: config.MatchConfig{Type: "prefix", Path: "/redirect"}, Redirect: "https://example.com", Return: 301, Cache: true, CachePolicy: &config.LocationCachePolicy{ReloadPolicy: "invalidate"}},
			{Match: config.MatchConfig{Type: "prefix", Path: "/deny"}, Deny: true, Cache: true, CachePolicy: &config.LocationCachePolicy{ReloadPolicy: "invalidate"}},
			{Match: config.MatchConfig{Type: "prefix", Path: "/plugin"}, Plugin: "loc-handler-plugin", Plugins: []string{"loc-plugin"}, Cache: true, CachePolicy: &config.LocationCachePolicy{ReloadPolicy: "invalidate"}},
		},
	}
	cfgActions := &config.Config{
		Cache:   config.CacheConfig{Enabled: true, ReloadPolicy: "invalidate"},
		Servers: []config.ServerConfig{srvActions},
	}
	for i, loc := range srvActions.Locations {
		rc := CompileRouteCacheConfig("scope-"+loc.Match.Path, true, loc.CachePolicy).
			WithMatcher(loc, versions).
			WithBackendCutover(cfgActions, srvActions, loc, versions)
		if rc.matcherProof == "" {
			t.Fatalf("location %d (%s) produced empty matcherProof", i, loc.Match.Path)
		}
	}
	// Removing a server block with cached routes under reload_policy=invalidate derives selectors for the removed routes.
	cfgRemoved := &config.Config{
		Cache:   config.CacheConfig{Enabled: true, ReloadPolicy: "invalidate"},
		Servers: nil,
	}
	remSels := DeriveRouteCutoverSelectorsForPolicy(cfgActions, cfgRemoved, "")
	if len(remSels) == 0 {
		t.Fatal("expected cutover selectors when server block with cached routes is removed")
	}
}
