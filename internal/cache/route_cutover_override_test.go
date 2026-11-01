// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/versionid"
)

func TestOneChangeCutoverOverridePreservesConfiguredPosture(t *testing.T) {
	for _, policy := range []string{"retain", "invalidate"} {
		for _, override := range []string{"", "retain", "invalidate"} {
			t.Run(policy+"/"+override, func(t *testing.T) {
				c, err := New(config.CacheConfig{Enabled: true, MemoryMaxSize: 4 << 20, DiskPath: t.TempDir(), DiskMaxSize: 8 << 20}, nil)
				if err != nil {
					t.Fatal(err)
				}
				cfg := &config.Config{Cache: config.CacheConfig{Enabled: true, ReloadPolicy: policy}, Servers: []config.ServerConfig{{Listen: ":8080", ServerNames: []string{"example.com"}, Locations: []config.LocationConfig{{Match: config.MatchConfig{Path: "/", Type: "prefix"}, Cache: true, ProxyPass: "http://127.0.0.1:9001"}}}}}
				compile := func(target string) RouteCacheConfig {
					loc := cfg.Servers[0].Locations[0]
					loc.ProxyPass = target
					return CompileRouteCacheConfig("scope-one", true, loc.CachePolicy).WithMatcher(loc, versionid.Process()).WithBackendCutover(cfg, cfg.Servers[0], loc, versionid.Process())
				}
				a, b := compile("http://127.0.0.1:9001"), compile("http://127.0.0.1:9002")
				c.CommitRoutePolicies([]RouteCacheConfig{a})
				oldEpoch := c.CurrentRouteEpoch(a.Scope)
				if err := c.CommitRoutePoliciesWithCutover([]RouteCacheConfig{b}, override); err != nil {
					t.Fatal(err)
				}
				wantAdvance := override == "invalidate" || (override == "" && policy == "invalidate")
				if gotAdvance := c.CurrentRouteEpoch(b.Scope) > oldEpoch; gotAdvance != wantAdvance {
					t.Fatalf("advanced=%t want=%t", gotAdvance, wantAdvance)
				}
				if c.routeReloadPolicies[b.Scope] != policy {
					t.Fatalf("ephemeral mode persisted as policy: %+v", c.routeReloadPolicies)
				}
				// Retain can suppress optional backend retirement, never mandatory policy
				// tightening and its late-publication floor.
				c.genMu.Lock()
				beforeFloor := c.routePolicyFloor[b.Scope]
				c.genMu.Unlock()
				tighter := b
				tighter.HasMaxTTL = true
				tighter.MaxTTL = time.Second
				if err := c.CommitRoutePoliciesWithCutover([]RouteCacheConfig{tighter}, "retain"); err != nil {
					t.Fatal(err)
				}
				c.genMu.Lock()
				afterFloor := c.routePolicyFloor[b.Scope]
				c.genMu.Unlock()
				if afterFloor <= beforeFloor {
					t.Fatalf("retain bypassed policy-tightening floor: %d -> %d", beforeFloor, afterFloor)
				}
			})
		}
	}
	if err := (*Cache)(nil).CommitRoutePoliciesWithCutover(nil, ""); err != nil {
		t.Fatal(err)
	}
	c, err := New(config.CacheConfig{Enabled: true, MemoryMaxSize: 4 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.CommitRoutePoliciesWithCutover(nil, "bad"); err == nil {
		t.Fatal("invalid override accepted")
	}
}
