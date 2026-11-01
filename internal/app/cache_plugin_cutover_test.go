// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"jul/internal/cache"
	"jul/internal/config"
	"jul/internal/versionid"
)

func pluginCutoverConfig(t *testing.T) (*config.Config, string, string) {
	t.Helper()
	dir := t.TempDir()
	pagePath, unusedPath := filepath.Join(dir, "page.wasm"), filepath.Join(dir, "unused.wasm")
	for _, path := range []string{pagePath, unusedPath} {
		if err := os.WriteFile(path, pluginModule(t, "header-inject"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.ProxyTarget("http://127.0.0.1:9000", ":8080")
	cfg.Cache.Enabled = true
	cfg.Cache.ReloadPolicy = "invalidate"
	cfg.Cache.MemoryMaxSize = 8 << 20
	cfg.Cache.DiskPath = filepath.Join(dir, "cache")
	cfg.Cache.DiskMaxSize = 32 << 20
	cfg.Cache.DefaultTTL = config.Duration(time.Hour)
	pageID, keepID := "page", "keep"
	page := &cfg.Servers[0].Locations[0]
	page.ProxyPass = ""
	page.Plugin = "page"
	page.Cache = true
	page.RouteID = &pageID
	page.Match = config.MatchConfig{Type: "prefix", Path: "/page/"}
	keep := *page
	keep.Plugin = "other"
	keep.RouteID = &keepID
	keep.Match = config.MatchConfig{Type: "prefix", Path: "/keep/"}
	cfg.Servers[0].Locations = append(cfg.Servers[0].Locations, keep)
	cfg.Plugins = map[string]config.PluginConfig{
		"page":   {Path: pagePath, Type: "handler", Timeout: config.Duration(2 * time.Second)},
		"other":  {Path: filepath.Join("..", "..", "testdata", "plugins", "header-inject.wasm"), Type: "handler", Timeout: config.Duration(2 * time.Second)},
		"unused": {Path: unusedPath, Type: "handler", Timeout: config.Duration(2 * time.Second)},
	}
	var err error
	cfg, err = cfg.Clone()
	if err != nil {
		t.Fatal(err)
	}
	return cfg, pagePath, unusedPath
}

func pluginCutoverRequest(h http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://example.com"+path, nil)
	r.Header.Set("X-Block", "1")
	h.ServeHTTP(w, r)
	return w
}

func TestHandlerPluginModuleCutover(t *testing.T) {
	for _, change := range []string{"path", "same_path_bytes", "single_route_same_path", "unchanged_bytes", "unused_bytes", "retain"} {
		t.Run(change, func(t *testing.T) {
			cfg, pagePath, unusedPath := pluginCutoverConfig(t)
			paths := []string{"/page/article", "/keep/article"}
			if change == "single_route_same_path" {
				cfg.Servers[0].Locations = cfg.Servers[0].Locations[:1]
				paths = paths[:1]
			}
			if change == "retain" {
				cfg.Cache.ReloadPolicy = "retain"
			}
			f, cleanup := minimalFactory(t)
			defer cleanup()
			var err error
			f.Cache, err = cache.New(cfg.Cache, f.Log)
			if err != nil {
				t.Fatal(err)
			}
			hs, _, err := f.Build(context.Background(), cfg, true)
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range paths {
				pluginCutoverRequest(hs[":8080"], path)
				if got := pluginCutoverRequest(hs[":8080"], path); got.Header().Get("X-Cache") != "HIT" || got.Code != http.StatusOK {
					t.Fatalf("warm %s: status=%d cache=%s", path, got.Code, got.Header().Get("X-Cache"))
				}
			}
			next, err := cfg.Clone()
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "path":
				pc := next.Plugins["page"]
				pc.Path = filepath.Join("..", "..", "testdata", "plugins", "request-block.wasm")
				next.Plugins["page"] = pc
			case "same_path_bytes", "single_route_same_path", "retain":
				if err := os.WriteFile(pagePath, pluginModule(t, "request-block"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "unchanged_bytes":
				if err := os.WriteFile(pagePath, pluginModule(t, "header-inject"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "unused_bytes":
				if err := os.WriteFile(unusedPath, pluginModule(t, "request-block"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			hs, retire, err := f.Build(context.Background(), next, true)
			if err != nil {
				t.Fatal(err)
			}
			if retire != nil {
				retire()
			}
			got := pluginCutoverRequest(hs[":8080"], "/page/article")
			invalidate := change == "path" || change == "same_path_bytes" || change == "single_route_same_path"
			wantStatus, wantResults := http.StatusOK, 0
			wantHit := !invalidate
			if invalidate {
				wantStatus, wantResults = http.StatusForbidden, 1
			}
			if got.Code != wantStatus || (got.Header().Get("X-Cache") == "HIT") != wantHit {
				t.Fatalf("page status=%d cache=%s want status=%d hit=%t", got.Code, got.Header().Get("X-Cache"), wantStatus, !invalidate)
			}
			results, err := f.LastCacheCutover()
			if err != nil || len(results) != wantResults {
				t.Fatalf("cutover results=%+v err=%v want count=%d", results, err, wantResults)
			}
			if len(paths) > 1 {
				if got := pluginCutoverRequest(hs[":8080"], "/keep/article"); got.Code != http.StatusOK || got.Header().Get("X-Cache") != "HIT" {
					t.Fatalf("unrelated route lost warmth: status=%d cache=%s", got.Code, got.Header().Get("X-Cache"))
				}
			}
		})
	}
}

func TestHandlerPluginRestartUsesCompiledModuleIdentity(t *testing.T) {
	cfg, pagePath, _ := pluginCutoverConfig(t)
	f, cleanup := minimalFactory(t)
	key := make([]byte, 32)
	var err error
	f.Versions, err = versionid.New(key)
	if err != nil {
		t.Fatal(err)
	}
	f.Cache, err = cache.New(cfg.Cache, f.Log)
	if err != nil {
		t.Fatal(err)
	}
	hs, _, err := f.Build(context.Background(), cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/page/article", "/keep/article"} {
		pluginCutoverRequest(hs[":8080"], path)
		if got := pluginCutoverRequest(hs[":8080"], path); got.Header().Get("X-Cache") != "HIT" {
			t.Fatalf("warm control %s did not hit", path)
		}
	}
	cleanup()
	if err := os.WriteFile(pagePath, pluginModule(t, "request-block"), 0o600); err != nil {
		t.Fatal(err)
	}
	restarted, cleanup2 := minimalFactory(t)
	defer cleanup2()
	restarted.Versions, err = versionid.New(key)
	if err != nil {
		t.Fatal(err)
	}
	restarted.Cache, err = cache.New(cfg.Cache, restarted.Log)
	if err != nil {
		t.Fatal(err)
	}
	hs, _, err = restarted.Build(context.Background(), cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := pluginCutoverRequest(hs[":8080"], "/page/article"); got.Code != http.StatusForbidden || got.Header().Get("X-Cache") == "HIT" {
		t.Fatalf("restart served old plugin bytes: status=%d cache=%s", got.Code, got.Header().Get("X-Cache"))
	}
	if got := pluginCutoverRequest(hs[":8080"], "/keep/article"); got.Code != http.StatusOK || got.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("restart lost unrelated warmth: status=%d cache=%s", got.Code, got.Header().Get("X-Cache"))
	}
}
