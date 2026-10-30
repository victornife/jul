// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"jul/internal/adminapi"
	"jul/internal/cache"
	"jul/internal/config"
)

func TestRouteCachePolicyProjectionPatchDiffAndInspect(t *testing.T) {
	routeAPI := "route-api"
	routeAlt := "route-alt"
	canaryVal := "canary"
	dZero := config.Duration(0)
	dOneMin := config.Duration(time.Minute)
	dThirtySec := config.Duration(30 * time.Second)
	dFiveMin := config.Duration(5 * time.Minute)

	cfg := &config.Config{
		Cache: config.CacheConfig{
			Enabled:              true,
			MemoryMaxSize:        4 << 20,
			DefaultTTL:           config.Duration(10 * time.Minute),
			StaleWhileRevalidate: config.Duration(time.Minute),
			StaleIfError:         config.Duration(15 * time.Minute),
		},
		Servers: []config.ServerConfig{
			{
				Listen:      ":8080",
				ServerNames: []string{"example.com"},
				Locations: []config.LocationConfig{
					{
						RouteID:   &routeAPI,
						Match:     config.MatchConfig{Type: "prefix", Path: "/api", Headers: []config.HeaderMatch{{Name: "X-Channel", Value: &canaryVal}}},
						ProxyPass: "http://127.0.0.1:9001",
						Cache:     true,
						CachePolicy: &config.LocationCachePolicy{
							DefaultTTL:           &dZero,
							MaxTTL:               &dOneMin,
							StaleWhileRevalidate: &dThirtySec,
							StaleIfError:         &dFiveMin,
							BypassCookies:        []string{"Session_ID", "auth_token"},
						},
					},
					{
						RouteID:   &routeAlt,
						Match:     config.MatchConfig{Type: "prefix", Path: "/api"},
						ProxyPass: "http://127.0.0.1:9002",
						Cache:     true,
					},
				},
			},
		},
	}

	c, err := cache.New(cfg.Cache, nil)
	if err != nil {
		t.Fatal(err)
	}
	const token = "admin-route-cache-policy-token-32ch"
	s := newTestServer(t, config.AdminConfig{Token: token}, Deps{
		Cache:      c,
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
	})

	// 1. Verify /api/routes projection and v1Routes include cache_policy and cache_effective.
	routesProj := projectRoutes(cfg)
	v1R := v1Routes(cfg)
	if len(v1R) != 2 || v1R[0].CachePolicy == nil || v1R[0].CachePolicy.EffectiveDefaultTTL != "0s" {
		t.Fatalf("unexpected v1Routes: %+v", v1R)
	}
	cfgDormant := &config.Config{
		Cache: config.CacheConfig{
			Enabled:       true,
			MemoryMaxSize: 4 << 20,
			MaxEntrySize:  2 << 20,
			DefaultTTL:    config.Duration(10 * time.Minute),
		},
		Servers: []config.ServerConfig{
			{
				Listen: ":8080",
				Locations: []config.LocationConfig{
					{
						Match:     config.MatchConfig{Type: "prefix", Path: "/dormant"},
						ProxyPass: "http://127.0.0.1:9001",
						Cache:     false,
						CachePolicy: &config.LocationCachePolicy{
							DefaultTTL:           &dFiveMin,
							MaxTTL:               &dZero,
							StaleWhileRevalidate: &dZero,
							StaleIfError:         &dZero,
							BypassAnyCookie:      true,
						},
					},
					{
						Match:       config.MatchConfig{Type: "prefix", Path: "/capped"},
						ProxyPass:   "http://127.0.0.1:9001",
						Cache:       true,
						CachePolicy: &config.LocationCachePolicy{MaxTTL: &dOneMin},
					},
				},
			},
		},
	}
	dormantProj := projectRoutes(cfgDormant)
	if len(dormantProj[0].Locations[0].Warnings) == 0 || dormantProj[0].Locations[1].CacheEffective.DefaultTTLSource != "capped_by_route_max_ttl" {
		t.Fatalf("unexpected dormant/capped projection: %+v", dormantProj[0].Locations)
	}
	if len(routesProj) != 1 || len(routesProj[0].Locations) != 2 {
		t.Fatalf("unexpected routes projection: %+v", routesProj)
	}
	loc0 := routesProj[0].Locations[0]
	if loc0.CachePolicy == nil || loc0.CacheEffective == nil {
		t.Fatalf("expected CachePolicy and CacheEffective on loc0: %+v", loc0)
	}
	if loc0.CacheEffective.DefaultTTL != "0s" || loc0.CacheEffective.DefaultTTLSource != "explicit_zero" {
		t.Fatalf("unexpected effective DefaultTTL: %+v", loc0.CacheEffective)
	}
	if loc0.CacheEffective.MaxTTL != "1m0s" || loc0.CacheEffective.MaxTTLSource != "route_cap" {
		t.Fatalf("unexpected effective MaxTTL: %+v", loc0.CacheEffective)
	}
	if loc0.CacheEffective.BypassMode != "selected_cookies" || len(loc0.CacheEffective.BypassCookies) != 2 {
		t.Fatalf("unexpected effective bypass: %+v", loc0.CacheEffective)
	}

	// 2. Verify /api/v1/cache/inspect by route_id returns exact route effective policy.
	rr := postV1JSON(t, s, "/api/v1/cache/inspect", token, "", adminapi.CacheInspectRequest{
		Selector: adminapi.CacheSelector{Kind: "host", RouteID: routeAPI},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("inspect by route_id failed: %d %s", rr.Code, rr.Body.String())
	}
	inspResp := decodeInto[adminapi.CacheInspectResponse](t, rr)
	if inspResp.PolicySource != "route_override_with_global_inheritance" {
		t.Fatalf("expected route_override_with_global_inheritance, got %q", inspResp.PolicySource)
	}
	if inspResp.Inspection.Policy.DefaultTTLSeconds != 0 ||
		inspResp.Inspection.Policy.MaxTTLSeconds == nil || *inspResp.Inspection.Policy.MaxTTLSeconds != 60 ||
		inspResp.Inspection.Policy.SWRSeconds != 30 ||
		inspResp.Inspection.Policy.SIESeconds != 300 ||
		len(inspResp.Inspection.Policy.BypassCookies) != 2 {
		t.Fatalf("unexpected route effective policy in inspection: %+v", inspResp.Inspection.Policy)
	}

	// 3. Verify /api/v1/cache/inspect by host without route_id surfaces ambiguity when multiple routes differ.
	rrHost := postV1JSON(t, s, "/api/v1/cache/inspect", token, "", adminapi.CacheInspectRequest{
		Selector: adminapi.CacheSelector{Kind: "host", Host: "example.com"},
	})
	if rrHost.Code != http.StatusOK {
		t.Fatalf("inspect by host failed: %d %s", rrHost.Code, rrHost.Body.String())
	}
	hostResp := decodeInto[adminapi.CacheInspectResponse](t, rrHost)
	if hostResp.PolicySource != "ambiguous_multiple_routes_use_route_id" {
		t.Fatalf("expected ambiguous_multiple_routes_use_route_id, got %q", hostResp.PolicySource)
	}
	foundWarning := false
	for _, w := range hostResp.Inspection.Warnings {
		if w == "ambiguous_route_policy_use_route_id" {
			foundWarning = true
			break
		}
	}
	if !foundWarning {
		t.Fatalf("expected ambiguous_route_policy_use_route_id warning, got %v", hostResp.Inspection.Warnings)
	}

	// 4. Verify applyPatch with location_cache_policy_set and location_cache_policy_clear + semantic diff.
	cloned := *cfg
	cloned.Servers = append([]config.ServerConfig(nil), cfg.Servers...)
	cloned.Servers[0].Locations = append([]config.LocationConfig(nil), cfg.Servers[0].Locations...)
	oneMinStr := "1m"
	twoMin := "2m"
	tenSec := "10s"
	twentySec := "20s"
	summary, err := applyPatch(&cloned, patchRequest{
		Op:           "location_cache_policy_set",
		Listen:       ":8080",
		ServerNames:  []string{"example.com"},
		MatchType:    "prefix",
		Path:         "/api",
		MatchOrdinal: intPtr(1),
		CachePolicy: &locationCachePolicyPatch{
			DefaultTTL:           &oneMinStr,
			MaxTTL:               &twoMin,
			StaleWhileRevalidate: &tenSec,
			StaleIfError:         &twentySec,
			BypassAnyCookie:      true,
		},
	})
	if err != nil {
		t.Fatalf("location_cache_policy_set failed: %v", err)
	}
	if !strings.Contains(summary, "cache policy") {
		t.Fatalf("unexpected patch summary: %q", summary)
	}
	diff := diffConfigs(cfg, &cloned)
	if len(diff.Modifications) == 0 && len(diff.Additions) == 0 {
		t.Fatalf("expected semantic diff for cache_policy change, got %+v", diff)
	}

	// Modify existing policy on location 0 and diff.
	_, err = applyPatch(&cloned, patchRequest{
		Op:           "location_cache_policy_set",
		Listen:       ":8080",
		ServerNames:  []string{"example.com"},
		MatchType:    "prefix",
		Path:         "/api",
		MatchOrdinal: intPtr(0),
		CachePolicy: &locationCachePolicyPatch{
			DefaultTTL:           &oneMinStr,
			MaxTTL:               &twoMin,
			StaleWhileRevalidate: &tenSec,
			StaleIfError:         &twentySec,
			BypassCookies:        []string{"new_cookie"},
		},
	})
	if err != nil {
		t.Fatalf("location_cache_policy_set update failed: %v", err)
	}
	_ = diffConfigs(cfg, &cloned)

	// Clear the policy on location 0 and location 1 and diff.
	for _, ord := range []int{0, 1} {
		_, err = applyPatch(&cloned, patchRequest{
			Op:           "location_cache_policy_clear",
			Listen:       ":8080",
			ServerNames:  []string{"example.com"},
			MatchType:    "prefix",
			Path:         "/api",
			MatchOrdinal: intPtr(ord),
		})
		if err != nil {
			t.Fatalf("location_cache_policy_clear(%d) failed: %v", ord, err)
		}
	}
	_ = diffConfigs(cfg, &cloned)

	// Verify /api/v1/routes includes cache_policy and single-route inspection selectors work.
	singleRouteID := "route-static"
	cfgSingle := &config.Config{
		Cache: cfg.Cache,
		Servers: []config.ServerConfig{
			{
				Listen:      ":8080",
				ServerNames: []string{"static.example.com"},
				Locations: []config.LocationConfig{
					{
						RouteID:   &singleRouteID,
						Match:     config.MatchConfig{Type: "exact", Path: "/logo.png"},
						ProxyPass: "http://127.0.0.1:9001",
						Cache:     true,
						CachePolicy: &config.LocationCachePolicy{
							MaxTTL:          &dOneMin,
							BypassAnyCookie: true,
						},
					},
				},
			},
		},
	}
	sSingle := newTestServer(t, config.AdminConfig{Token: token}, Deps{
		Cache:      c,
		LoadConfig: func() (*config.Config, error) { return cfgSingle, nil },
	})
	for _, sel := range []adminapi.CacheSelector{
		{Kind: "exact_url", Host: "static.example.com", URI: "/logo.png?ver=1"},
		{Kind: "key", Key: "GET\nstatic.example.com\n/logo.png"},
		{Kind: "prefix", Host: "static.example.com", Prefix: "/logo"},
	} {
		rrSel := postV1JSON(t, sSingle, "/api/v1/cache/inspect", token, "", adminapi.CacheInspectRequest{Selector: sel})
		if rrSel.Code != http.StatusOK {
			t.Fatalf("inspect %+v failed: %d %s", sel, rrSel.Code, rrSel.Body.String())
		}
	}

	// Verify multiple routes with identical overrides resolve without ambiguity.
	cfgSame := &config.Config{
		Cache: cfg.Cache,
		Servers: []config.ServerConfig{
			{
				Listen:      ":8080",
				ServerNames: []string{"same.example.com"},
				Locations: []config.LocationConfig{
					{
						Match:       config.MatchConfig{Type: "prefix", Path: "/a"},
						ProxyPass:   "http://127.0.0.1:9001",
						Cache:       true,
						CachePolicy: &config.LocationCachePolicy{MaxTTL: &dOneMin},
					},
					{
						Match:       config.MatchConfig{Type: "prefix", Path: "/b"},
						ProxyPass:   "http://127.0.0.1:9002",
						Cache:       true,
						CachePolicy: &config.LocationCachePolicy{MaxTTL: &dOneMin},
					},
				},
			},
		},
	}
	sSame := newTestServer(t, config.AdminConfig{Token: token}, Deps{
		Cache:      c,
		LoadConfig: func() (*config.Config, error) { return cfgSame, nil },
	})
	rrSame := postV1JSON(t, sSame, "/api/v1/cache/inspect", token, "", adminapi.CacheInspectRequest{
		Selector: adminapi.CacheSelector{Kind: "host", Host: "same.example.com"},
	})
	if rrSame.Code != http.StatusOK || decodeInto[adminapi.CacheInspectResponse](t, rrSame).PolicySource != "route_override_with_global_inheritance" {
		t.Fatalf("unexpected same-policy multi-route response: %d %s", rrSame.Code, rrSame.Body.String())
	}

	// Exercise patch builder / applyPatch error & edge branches and diff warnings.
	badDur := "not-a-duration"
	emptyDur := "   "
	boolTrue := true
	boolFalse := false
	for _, badReq := range []patchRequest{
		{Op: "location_cache_policy_set", Listen: ":9999", MatchType: "prefix", Path: "/missing"},
		{Op: "location_cache_policy_set", Listen: ":8080", ServerNames: []string{"example.com"}, MatchType: "prefix", Path: "/api", MatchOrdinal: intPtr(1), CachePolicy: nil},
		{Op: "location_cache_policy_set", Listen: ":8080", ServerNames: []string{"example.com"}, MatchType: "prefix", Path: "/api", MatchOrdinal: intPtr(1), CachePolicy: &locationCachePolicyPatch{DefaultTTL: &badDur}},
		{Op: "location_cache_policy_set", Listen: ":8080", ServerNames: []string{"example.com"}, MatchType: "prefix", Path: "/api", MatchOrdinal: intPtr(1), CachePolicy: &locationCachePolicyPatch{MaxTTL: &badDur}},
		{Op: "location_cache_policy_set", Listen: ":8080", ServerNames: []string{"example.com"}, MatchType: "prefix", Path: "/api", MatchOrdinal: intPtr(1), CachePolicy: &locationCachePolicyPatch{StaleWhileRevalidate: &badDur}},
		{Op: "location_cache_policy_set", Listen: ":8080", ServerNames: []string{"example.com"}, MatchType: "prefix", Path: "/api", MatchOrdinal: intPtr(1), CachePolicy: &locationCachePolicyPatch{StaleIfError: &badDur}},
		{Op: "location_cache_policy_clear", Listen: ":9999", MatchType: "prefix", Path: "/missing"},
		{Op: "location_cache_policy_clear", Listen: ":8080", ServerNames: []string{"example.com"}, MatchType: "prefix", Path: "/api", MatchOrdinal: intPtr(1)},
	} {
		if _, err := applyPatch(&cloned, badReq); err == nil {
			t.Fatalf("expected error for patch request %+v", badReq)
		}
	}

	// Empty policy fields reset to nil ("inherit global [cache]") and Cache toggle updates loc.Cache.
	if _, err := applyPatch(&cloned, patchRequest{
		Op:           "location_cache_policy_set",
		Listen:       ":8080",
		ServerNames:  []string{"example.com"},
		MatchType:    "prefix",
		Path:         "/api",
		MatchOrdinal: intPtr(1),
		CachePolicy:  &locationCachePolicyPatch{DefaultTTL: &emptyDur, Cache: &boolFalse},
	}); err != nil {
		t.Fatalf("empty location_cache_policy_set failed: %v", err)
	}
	if _, err := applyPatch(&cloned, patchRequest{
		Op:           "location_cache_policy_set",
		Listen:       ":8080",
		ServerNames:  []string{"example.com"},
		MatchType:    "prefix",
		Path:         "/api",
		MatchOrdinal: intPtr(1),
		CachePolicy:  &locationCachePolicyPatch{BypassAnyCookie: true, Cache: &boolTrue},
	}); err != nil {
		t.Fatal(err)
	}
	// Diff when disabling BypassAnyCookie and when Cache=false with CachePolicy != nil.
	clonedAfter := *cfg
	clonedAfter.Servers = append([]config.ServerConfig(nil), cfg.Servers...)
	clonedAfter.Servers[0].Locations = append([]config.LocationConfig(nil), cfg.Servers[0].Locations...)
	clonedAfter.Servers[0].Locations[0].CachePolicy = &config.LocationCachePolicy{MaxTTL: &dOneMin}
	clonedAfter.Servers[0].Locations[1].Cache = false
	clonedAfter.Servers[0].Locations[1].CachePolicy = &config.LocationCachePolicy{MaxTTL: &dOneMin}
	_ = diffConfigs(&cloned, &clonedAfter)

	if _, err := applyPatch(&cloned, patchRequest{
		Op:           "location_cache_policy_clear",
		Listen:       ":8080",
		ServerNames:  []string{"example.com"},
		MatchType:    "prefix",
		Path:         "/api",
		MatchOrdinal: intPtr(1),
		Enabled:      &boolFalse,
	}); err != nil {
		t.Fatal(err)
	}

	// Verify reload_policy validation, projection, diff, and CacheCutoverPlan (#624).
	if _, err := applyPatch(&cloned, patchRequest{
		Op:           "location_cache_policy_set",
		Listen:       ":8080",
		ServerNames:  []string{"example.com"},
		MatchType:    "prefix",
		Path:         "/api",
		MatchOrdinal: intPtr(0),
		CachePolicy:  &locationCachePolicyPatch{ReloadPolicy: "invalid_mode"},
	}); err == nil {
		t.Fatal("expected error for invalid reload_policy")
	}
	if _, err := applyPatch(&cloned, patchRequest{
		Op:           "location_cache_policy_set",
		Listen:       ":8080",
		ServerNames:  []string{"example.com"},
		MatchType:    "prefix",
		Path:         "/api",
		MatchOrdinal: intPtr(0),
		CachePolicy:  &locationCachePolicyPatch{ReloadPolicy: "invalidate"},
	}); err != nil {
		t.Fatalf("location_cache_policy_set(reload_policy=invalidate): %v", err)
	}
	clonedCutover := cloned
	clonedCutover.Cache.ReloadPolicy = "invalidate"
	clonedCutover.Servers = append([]config.ServerConfig(nil), cloned.Servers...)
	clonedCutover.Servers[0].Locations = append([]config.LocationConfig(nil), cloned.Servers[0].Locations...)
	clonedCutover.Servers[0].Locations[0].ProxyPass = "http://127.0.0.1:9099"
	cutoverDiff := diffConfigs(&cloned, &clonedCutover)
	if cutoverDiff.CacheCutover == nil || cutoverDiff.CacheCutover.Disposition != "invalidate_on_publish" {
		t.Fatalf("expected invalidate_on_publish CacheCutoverPlan, got %+v", cutoverDiff.CacheCutover)
	}

	// Verify broadened host/all and stage_for_restart dispositions in diffCacheCutoverPlan.
	clonedBroad := clonedCutover
	clonedBroad.Cache.MemoryMaxSize = config.Size(32 << 20) // restart_required change
	clonedBroad.Servers = append([]config.ServerConfig(nil), clonedCutover.Servers...)
	clonedBroad.Servers[0].ServerNames = nil // unnamed server block -> SelectorAll
	broadDiff := diffConfigs(&cloned, &clonedBroad)
	if broadDiff.CacheCutover == nil || !broadDiff.CacheCutover.BroadenedToHostOrAll || broadDiff.CacheCutover.Disposition != "stage_for_restart" {
		t.Fatalf("expected broadened stage_for_restart CacheCutoverPlan, got %+v", broadDiff.CacheCutover)
	}

	// Verify exact-route cutover labels and multi-route inspection ambiguity when ReloadPolicy differs.
	cfgExactBefore := &config.Config{
		Cache: config.CacheConfig{Enabled: true, ReloadPolicy: "retain"},
		Servers: []config.ServerConfig{
			{
				Listen:      ":8080",
				ServerNames: []string{"exact.example.com"},
				Locations: []config.LocationConfig{
					{
						Match:       config.MatchConfig{Type: "exact", Path: "/a"},
						ProxyPass:   "http://127.0.0.1:9001",
						Cache:       true,
						CachePolicy: &config.LocationCachePolicy{ReloadPolicy: "invalidate"},
					},
					{
						Match:       config.MatchConfig{Type: "exact", Path: "/b"},
						ProxyPass:   "http://127.0.0.1:9002",
						Cache:       true,
						CachePolicy: &config.LocationCachePolicy{ReloadPolicy: "retain"},
					},
					{
						Match:     config.MatchConfig{Type: "prefix", Path: "/inherit"},
						ProxyPass: "http://127.0.0.1:9003",
						Cache:     true,
					},
				},
			},
		},
	}
	v1Exact := v1Routes(cfgExactBefore)
	if len(v1Exact) != 3 || v1Exact[2].CachePolicy == nil || v1Exact[2].CachePolicy.EffectiveReloadPolicy != "retain" {
		t.Fatalf("expected inherited route without [cache_policy] to report EffectiveReloadPolicy=retain, got %+v", v1Exact[2].CachePolicy)
	}
	sExact := newTestServer(t, config.AdminConfig{Token: token}, Deps{
		Cache:      c,
		LoadConfig: func() (*config.Config, error) { return cfgExactBefore, nil },
	})
	rrAmb := postV1JSON(t, sExact, "/api/v1/cache/inspect", token, "", adminapi.CacheInspectRequest{
		Selector: adminapi.CacheSelector{Kind: "host", Host: "exact.example.com"},
	})
	if rrAmb.Code != http.StatusOK || decodeInto[adminapi.CacheInspectResponse](t, rrAmb).PolicySource != "ambiguous_multiple_routes_use_route_id" {
		t.Fatalf("expected ambiguous_multiple_routes_use_route_id when routes differ in reload_policy, got %d %s", rrAmb.Code, rrAmb.Body.String())
	}

	cfgExactAfter := *cfgExactBefore
	cfgExactAfter.Servers = append([]config.ServerConfig(nil), cfgExactBefore.Servers...)
	cfgExactAfter.Servers[0].Locations = append([]config.LocationConfig(nil), cfgExactBefore.Servers[0].Locations...)
	cfgExactAfter.Servers[0].Locations[0].ProxyPass = "http://127.0.0.1:9011"
	cfgExactAfter.Servers[0].Locations[1].ProxyPass = "http://127.0.0.1:9012"
	exactDiff := diffConfigs(cfgExactBefore, &cfgExactAfter)
	if exactDiff.CacheCutover == nil ||
		len(exactDiff.CacheCutover.InvalidatedScopes) != 1 || exactDiff.CacheCutover.InvalidatedScopes[0] != "exact exact.example.com/a" ||
		len(exactDiff.CacheCutover.RetainedScopes) != 1 || exactDiff.CacheCutover.RetainedScopes[0] != "exact exact.example.com/b" {
		t.Fatalf("unexpected exact route cutover diff: %+v", exactDiff.CacheCutover)
	}
}
