// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/versionid"
)

func TestReferencedPluginCutoverIdentity(t *testing.T) {
	for _, attachment := range []string{"handler", "location_filter", "server_filter"} {
		for _, change := range []string{"config", "module", "unchanged", "unused_config", "unused_module", "retain", "override_retain"} {
			t.Run(attachment+"/"+change, func(t *testing.T) {
				cfg := &config.Config{
					Cache: config.CacheConfig{Enabled: true, ReloadPolicy: "invalidate", MemoryMaxSize: 1 << 20, DiskPath: t.TempDir(), DiskMaxSize: 8 << 20, DefaultTTL: config.Duration(time.Hour)},
					Plugins: map[string]config.PluginConfig{
						"page":   {Path: "/private/plugin-source.wasm", Type: "handler", Config: map[string]string{"body": "secret-body-A"}},
						"unused": {Path: "/private/unused.wasm", Config: map[string]string{"body": "unused-body-A"}},
					},
					Servers: []config.ServerConfig{
						{Listen: ":8080", ServerNames: []string{"example.com"}, Locations: []config.LocationConfig{{RouteID: strPtrCutover("page"), Cache: true, Match: config.MatchConfig{Type: "prefix", Path: "/page/"}}}},
						{Listen: ":8081", ServerNames: []string{"keep.example.com"}, Locations: []config.LocationConfig{{RouteID: strPtrCutover("keep"), Cache: true, Match: config.MatchConfig{Type: "prefix", Path: "/"}}}},
					},
				}
				switch attachment {
				case "handler":
					cfg.Servers[0].Locations[0].Plugin = "page"
				case "location_filter":
					cfg.Servers[0].Locations[0].Plugins = []string{"page"}
				case "server_filter":
					cfg.Servers[0].Plugins = []string{"page"}
				}
				var err error
				cfg, err = cfg.Clone()
				if err != nil {
					t.Fatal(err)
				}
				beforeModules := map[string]string{"page": "sha256:" + strings.Repeat("a", 64), "unused": "sha256:" + strings.Repeat("c", 64)}
				afterModules := map[string]string{"page": beforeModules["page"], "unused": beforeModules["unused"]}
				next, err := cfg.Clone()
				if err != nil {
					t.Fatal(err)
				}
				override := ""
				switch change {
				case "config":
					pc := next.Plugins["page"]
					pc.Config = map[string]string{"body": "secret-body-B"}
					next.Plugins["page"] = pc
				case "module", "retain", "override_retain":
					afterModules["page"] = "sha256:" + strings.Repeat("b", 64)
				case "unused_config":
					pc := next.Plugins["unused"]
					pc.Config = map[string]string{"body": "unused-body-B"}
					next.Plugins["unused"] = pc
				case "unused_module":
					afterModules["unused"] = "sha256:" + strings.Repeat("d", 64)
				}
				if change == "retain" {
					cfg.Cache.ReloadPolicy = "retain"
					next.Cache.ReloadPolicy = "retain"
				}
				if change == "override_retain" {
					override = "retain"
				}
				route := func(c *config.Config, i int, modules map[string]string) RouteCacheConfig {
					srv, loc := c.Servers[i], c.Servers[i].Locations[0]
					return CompileRouteCacheConfig(RouteScope(srv, loc), true, loc.CachePolicy).WithMatcher(loc, versionid.Process()).WithBackendCutover(c, srv, loc, versionid.Process(), modules)
				}
				origin := func(body string) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						w.Header().Set("Cache-Control", "public, max-age=3600")
						fmt.Fprint(w, body)
					})
				}
				request := func(h http.Handler, host string) *httptest.ResponseRecorder {
					w := httptest.NewRecorder()
					h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://"+host+"/page/article", nil))
					return w
				}
				c, err := New(cfg.Cache, nil)
				if err != nil {
					t.Fatal(err)
				}
				before, keep := route(cfg, 0, beforeModules), route(cfg, 1, beforeModules)
				c.CommitRoutePolicies([]RouteCacheConfig{before, keep})
				h := c.HandlerForRoute(origin("A"), before)
				request(h, "example.com")
				if got := request(h, "example.com"); got.Header().Get("X-Cache") != stateHit {
					t.Fatal("page seed did not hit")
				}
				hKeep := c.HandlerForRoute(origin("K"), keep)
				request(hKeep, "keep.example.com")
				after, keepAfter := route(next, 0, afterModules), route(next, 1, afterModules)
				if err := c.CommitRoutePoliciesWithCutover([]RouteCacheConfig{after, keepAfter}, override); err != nil {
					t.Fatal(err)
				}
				results, err := c.ExecuteRouteCutover(context.Background(), cfg, next, override, CutoverModuleIdentities{Before: beforeModules, After: afterModules})
				if err != nil {
					t.Fatal(err)
				}
				invalidated := change == "config" || change == "module"
				wantBody, wantHit, wantResults := "A", true, 0
				if invalidated {
					wantBody, wantHit, wantResults = "B", false, 1
				}
				got := request(c.HandlerForRoute(origin("B"), after), "example.com")
				if got.Body.String() != wantBody || (got.Header().Get("X-Cache") == stateHit) != wantHit || len(results) != wantResults {
					t.Fatalf("body=%q cache=%s results=%d want body=%q hit=%t results=%d", got.Body.String(), got.Header().Get("X-Cache"), len(results), wantBody, wantHit, wantResults)
				}
				if got := request(c.HandlerForRoute(origin("wrong"), keepAfter), "keep.example.com"); got.Body.String() != "K" || got.Header().Get("X-Cache") != stateHit {
					t.Fatalf("unrelated scope lost warmth: %s %q", got.Header().Get("X-Cache"), got.Body.String())
				}
				raw, err := os.ReadFile(filepath.Join(cfg.Cache.DiskPath, diskRouteEpochsFile))
				if err != nil {
					t.Fatal(err)
				}
				for _, private := range []string{"secret-body-A", "secret-body-B", "/private/", strings.Repeat("a", 64), strings.Repeat("b", 64)} {
					if strings.Contains(string(raw), private) {
						t.Fatalf("manifest persisted private backend input %q", private)
					}
				}
				// Reopening uses the HMAC of actual module/config identity rather
				// than treating unchanged source paths as unchanged execution.
				reopened, err := New(cfg.Cache, nil)
				if err != nil {
					t.Fatal(err)
				}
				reopened.CommitRoutePolicies([]RouteCacheConfig{after, keepAfter})
				if got := request(reopened.HandlerForRoute(origin("wrong"), after), "example.com"); got.Body.String() != wantBody || got.Header().Get("X-Cache") != stateHit {
					t.Fatalf("restart lost current plugin scope: %s %q", got.Header().Get("X-Cache"), got.Body.String())
				}
			})
		}
	}
}
