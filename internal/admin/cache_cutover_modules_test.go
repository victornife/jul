// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/plugins"
)

func TestCutoverPreviewUsesServingPluginModuleBytes(t *testing.T) {
	for _, tc := range []string{"changed_bytes", "same_bytes", "unknown_serving", "unreadable_candidate", "unused_bytes", "retain"} {
		t.Run(tc, func(t *testing.T) {
			moduleA, err := os.ReadFile(filepath.Join("..", "..", "testdata", "plugins", "header-inject.wasm"))
			if err != nil {
				t.Fatal(err)
			}
			moduleB, err := os.ReadFile(filepath.Join("..", "..", "testdata", "plugins", "request-block.wasm"))
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			path, unused := filepath.Join(dir, "private-page.wasm"), filepath.Join(dir, "unused.wasm")
			for _, p := range []string{path, unused} {
				if err := os.WriteFile(p, moduleA, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cfg := config.ProxyTarget("http://127.0.0.1:9001", ":8080")
			cfg.Cache.Enabled = true
			cfg.Cache.ReloadPolicy = "invalidate"
			cfg.Servers[0].ServerNames = []string{"example.com"}
			loc := &cfg.Servers[0].Locations[0]
			loc.ProxyPass = ""
			loc.Plugin = "page"
			loc.Cache = true
			cfg.Plugins = map[string]config.PluginConfig{"page": {Path: path, Type: "handler"}, "unused": {Path: unused, Type: "handler"}}
			if tc == "retain" {
				cfg.Cache.ReloadPolicy = "retain"
			}
			old, err := config.NewCandidate(cfg)
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := plugins.ReadModule(old.Effective.Plugins["page"])
			if err != nil {
				t.Fatal(err)
			}
			serving := map[string]string{"page": loaded.Identity.Digest}
			switch tc {
			case "changed_bytes", "retain":
				if err := os.WriteFile(path, moduleB, 0o600); err != nil {
					t.Fatal(err)
				}
			case "unknown_serving":
				serving = nil
			case "unreadable_candidate":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "unused_bytes":
				if err := os.WriteFile(unused, moduleB, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := config.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			assessment, err := previewRawCandidate(context.Background(), cfg, old.Effective, lifecycle.Live{}, "base", raw, serving)
			if err != nil {
				t.Fatal(err)
			}
			wantInvalidate := tc == "changed_bytes" || tc == "unknown_serving" || tc == "unreadable_candidate"
			plan := assessment.Diff.CacheCutover
			if wantInvalidate && (plan == nil || plan.Disposition != "invalidate_on_publish" || len(plan.InvalidatedScopes) != 1) {
				t.Fatalf("expected affected plugin scope in preview, got %+v", plan)
			}
			if !wantInvalidate && plan != nil && len(plan.InvalidatedScopes) != 0 {
				t.Fatalf("unchanged/unrelated/retained plugin invalidated: %+v", plan)
			}
			encoded, err := json.Marshal(assessment.Diff)
			if err != nil {
				t.Fatal(err)
			}
			for _, private := range []string{path, unused, loaded.Identity.Digest} {
				if strings.Contains(string(encoded), private) {
					t.Fatalf("public preview leaked module input %q", private)
				}
			}
		})
	}
}

func TestCutoverModuleProbeRespectsScopeAndCancellation(t *testing.T) {
	cfg := &config.Config{Plugins: map[string]config.PluginConfig{"uncached": {Path: "/missing-private-file"}}, Servers: []config.ServerConfig{{Locations: []config.LocationConfig{{Plugin: "uncached", Cache: false}}}}}
	if got := observeCutoverModules(context.Background(), cfg, nil); len(got.After) != 0 {
		t.Fatalf("uncached plugin was probed: %+v", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cfg.Servers[0].Locations[0].Cache = true
	if got := observeCutoverModules(ctx, cfg, nil); len(got.After) != 0 {
		t.Fatalf("canceled preview probed plugins: %+v", got)
	}
}
