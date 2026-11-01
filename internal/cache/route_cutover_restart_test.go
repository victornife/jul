// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/versionid"
)

func restartCutoverRoute(mode, backend string) RouteCacheConfig {
	loc := config.LocationConfig{RouteID: strPtrCutover("article"), Cache: true,
		Match: config.MatchConfig{Type: "prefix", Path: "/"}, ProxyPass: backend}
	srv := config.ServerConfig{Listen: ":8080", ServerNames: []string{"example.com"}, Locations: []config.LocationConfig{loc}}
	cfg := &config.Config{Cache: config.CacheConfig{Enabled: true, ReloadPolicy: mode}, Servers: []config.ServerConfig{srv}}
	return CompileRouteCacheConfig(RouteScope(srv, loc), true, nil).
		WithMatcher(loc, versionid.Process()).WithBackendCutover(cfg, srv, loc, versionid.Process())
}

func restartCutoverOrigin(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		fmt.Fprint(w, body)
	})
}

func restartCutoverRequest(h http.Handler) *httptest.ResponseRecorder {
	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "http://example.com/article", nil))
	return r
}

func restartCutoverCacheConfig(t *testing.T) config.CacheConfig {
	t.Helper()
	return config.CacheConfig{Enabled: true, MemoryMaxSize: 1 << 20,
		DiskPath: t.TempDir(), DiskMaxSize: 8 << 20, MaxEntrySize: 1 << 20,
		DefaultTTL: config.Duration(time.Hour)}
}

func seedRestartCutover(t *testing.T, cfg config.CacheConfig, route RouteCacheConfig) *Cache {
	t.Helper()
	c, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.CommitRoutePolicies([]RouteCacheConfig{route})
	h := c.HandlerForRoute(restartCutoverOrigin("A"), route)
	restartCutoverRequest(h)
	if got := restartCutoverRequest(h); got.Body.String() != "A" || got.Header().Get("X-Cache") != stateHit {
		t.Fatalf("seed: %s %q", got.Header().Get("X-Cache"), got.Body.String())
	}
	return c
}

func TestCutoverReloadPolicyTransitionSurvivesRestart(t *testing.T) {
	for _, beforeMode := range []string{"retain", "invalidate"} {
		for _, afterMode := range []string{"retain", "invalidate"} {
			for _, changed := range []bool{false, true} {
				for _, phase := range []string{"live", "startup", "committed"} {
					t.Run(fmt.Sprintf("%s_to_%s/backend_changed_%t/%s", beforeMode, afterMode, changed, phase), func(t *testing.T) {
						cfg := restartCutoverCacheConfig(t)
						before := restartCutoverRoute(beforeMode, "http://127.0.0.1:8001")
						c := seedRestartCutover(t, cfg, before)
						backend := "http://127.0.0.1:8001"
						if changed {
							backend = "http://127.0.0.1:8002"
						}
						after := restartCutoverRoute(afterMode, backend)
						if phase != "live" {
							var err error
							c, err = New(cfg, nil)
							if err != nil {
								t.Fatal(err)
							}
						}
						if phase != "startup" {
							c.CommitRoutePolicies([]RouteCacheConfig{after})
						}
						invalidate := changed && (beforeMode == "invalidate" || afterMode == "invalidate")
						wantBody, wantHit := "A", true
						if invalidate {
							wantBody, wantHit = "B", false
						}
						got := restartCutoverRequest(c.HandlerForRoute(restartCutoverOrigin("B"), after))
						if got.Body.String() != wantBody || (got.Header().Get("X-Cache") == stateHit) != wantHit {
							t.Fatalf("transition: %s %q; want body=%q hit=%t", got.Header().Get("X-Cache"), got.Body.String(), wantBody, wantHit)
						}
						// Establish the candidate after the uncommitted startup probe,
						// then warm and reopen again. The newly persisted posture must
						// authorize the current body, never revive the older epoch.
						if phase == "startup" {
							c.CommitRoutePolicies([]RouteCacheConfig{after})
							restartCutoverRequest(c.HandlerForRoute(restartCutoverOrigin("B"), after))
						}
						reopened, err := New(cfg, nil)
						if err != nil {
							t.Fatal(err)
						}
						reopened.CommitRoutePolicies([]RouteCacheConfig{after})
						got = restartCutoverRequest(reopened.HandlerForRoute(restartCutoverOrigin("C"), after))
						if got.Body.String() != wantBody || got.Header().Get("X-Cache") != stateHit {
							t.Fatalf("second restart: %s %q; want HIT %q", got.Header().Get("X-Cache"), got.Body.String(), wantBody)
						}
					})
				}
			}
		}
	}
}

func TestCutoverReloadPolicyManifestUpgrade(t *testing.T) {
	for _, tc := range []struct {
		name       string
		noBackends bool
		changed    bool
		nextMode   string
		wantHit    bool
	}{
		{"v1_cutover_unknown_posture_changed", false, true, "retain", false},
		{"v1_cutover_unknown_posture_unchanged", false, false, "retain", true},
		{"v1_pre_cutover_retain", true, true, "retain", true},
		{"v1_pre_cutover_invalidate", true, true, "invalidate", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := restartCutoverCacheConfig(t)
			before := restartCutoverRoute("retain", "http://127.0.0.1:8001")
			seedRestartCutover(t, cfg, before)
			path := filepath.Join(cfg.DiskPath, diskRouteEpochsFile)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var manifest map[string]any
			if err := json.Unmarshal(raw, &manifest); err != nil {
				t.Fatal(err)
			}
			manifest["version"] = 1
			delete(manifest, "reload_policies")
			if tc.noBackends {
				delete(manifest, "backends")
			}
			raw, err = json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			c, err := New(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			backend := "http://127.0.0.1:8001"
			if tc.changed {
				backend = "http://127.0.0.1:8002"
			}
			after := restartCutoverRoute(tc.nextMode, backend)
			c.CommitRoutePolicies([]RouteCacheConfig{after})
			got := restartCutoverRequest(c.HandlerForRoute(restartCutoverOrigin("B"), after))
			wantBody := "B"
			if tc.wantHit {
				wantBody = "A"
			}
			if got.Body.String() != wantBody || (got.Header().Get("X-Cache") == stateHit) != tc.wantHit {
				t.Fatalf("upgrade: %s %q; want body=%q hit=%t", got.Header().Get("X-Cache"), got.Body.String(), wantBody, tc.wantHit)
			}
			raw, err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var updated routeEpochManifest
			if err := json.Unmarshal(raw, &updated); err != nil {
				t.Fatal(err)
			}
			if updated.Version != 2 || updated.ReloadPolicies[after.Scope] != tc.nextMode {
				t.Fatalf("upgrade did not persist explicit posture: %+v", updated)
			}
		})
	}
}

func TestCutoverReloadPolicyManifestValidation(t *testing.T) {
	for _, policies := range []any{nil, map[string]string{}, map[string]string{"scope": "unknown"}, map[string]any{"scope": nil}} {
		t.Run(fmt.Sprintf("%v", policies), func(t *testing.T) {
			cfg := restartCutoverCacheConfig(t)
			before := restartCutoverRoute("invalidate", "http://127.0.0.1:8001")
			seedRestartCutover(t, cfg, before)
			path := filepath.Join(cfg.DiskPath, diskRouteEpochsFile)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var manifest map[string]any
			if err := json.Unmarshal(raw, &manifest); err != nil {
				t.Fatal(err)
			}
			manifest["reload_policies"] = policies
			raw, err = json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			c, err := New(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			after := restartCutoverRoute("retain", "http://127.0.0.1:8002")
			c.CommitRoutePolicies([]RouteCacheConfig{after})
			got := restartCutoverRequest(c.HandlerForRoute(restartCutoverOrigin("B"), after))
			if got.Body.String() != "B" || got.Header().Get("X-Cache") == stateHit {
				t.Fatalf("invalid posture authorized old entry: %s %q", got.Header().Get("X-Cache"), got.Body.String())
			}
		})
	}
}
