// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jul/internal/admin"
	"jul/internal/cache"
	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/server"
	"jul/internal/upstream"
)

// Exercises the real factory -> Publish -> correlated reload -> managed apply
// result, rather than inspecting the factory-local diagnostic alone.
func TestCacheCutoverFailureReportsCommittedConfiguration(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(map[bool]string{false: "source_reload", true: "managed_apply"}[managed], func(t *testing.T) {
			origin := func(body string) *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Cache-Control", "public, max-age=300")
					_, _ = io.WriteString(w, body)
				}))
			}
			a, b := origin("backend-A"), origin("backend-B")
			defer a.Close()
			defer b.Close()
			dir := t.TempDir()
			path := filepath.Join(dir, "jul.toml")
			addr := freePort(t)
			cfg := config.ProxyTarget(a.URL, addr)
			cfg.Servers[0].Locations[0].Cache = true
			cfg.Cache.Enabled = true
			cfg.Cache.ReloadPolicy = "invalidate"
			cfg.Cache.DiskPath = filepath.Join(dir, "cache")
			cfg.Cache.DiskMaxSize = 8 << 20
			cfg.Cache.MemoryMaxSize = 8 << 20
			write := func(target string) []byte {
				t.Helper()
				cfg.Servers[0].Locations[0].ProxyPass = target
				raw, err := config.Marshal(cfg)
				if err != nil {
					t.Fatal(err)
				}
				return raw
			}
			if err := os.WriteFile(path, write(a.URL), 0600); err != nil {
				t.Fatal(err)
			}
			src := config.NewTOMLSource(path)
			loaded, err := src.Load()
			if err != nil {
				t.Fatal(err)
			}
			cand, err := config.NewCandidate(loaded)
			if err != nil {
				t.Fatal(err)
			}
			f, cleanup := minimalFactory(t)
			defer cleanup()
			f.Cache, err = cache.New(cand.Effective.Cache, f.Log)
			if err != nil {
				t.Fatal(err)
			}
			srv := server.New(cand.Effective, cand.Raw, lifecycle.ComputeFingerprint(cand.Effective), f.Log, func(ctx context.Context, c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
				return f.Prepare(ctx, c)
			}, src, func(context.Context, *config.Config) error { return nil })
			srv.CacheCutoverResult = f.CacheCutoverResult
			reload := make(chan server.ReloadRequest)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- srv.Run(ctx, reload, cand.Redaction) }()
			defer func() {
				cancel()
				if err := <-done; err != nil {
					t.Error(err)
				}
			}()
			get := func() (string, string, error) {
				resp, err := http.Get("http://" + addr + "/article")
				if err != nil {
					return "", "", err
				}
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				return string(body), resp.Header.Get("X-Cache"), err
			}
			deadline := time.Now().Add(10 * time.Second)
			for {
				body, _, e := get()
				if e == nil && body == "backend-A" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("startup: %s %v", body, e)
				}
				time.Sleep(10 * time.Millisecond)
			}
			if _, hit, e := get(); e != nil || hit != "HIT" {
				t.Fatalf("warm cache hit=%q err=%v", hit, e)
			}
			marker := filepath.Join(cfg.Cache.DiskPath, ".jul-cache-gen")
			_ = os.Remove(marker)
			if err := os.Mkdir(marker, 0700); err != nil {
				t.Fatal(err)
			}
			var rr *server.ReloadResult
			if managed {
				coordinator := &ConfigApplyCoordinator{BaseCtx: ctx, Path: path, Preflight: testPreflight(), LiveSnapshot: srv.LiveSnapshot, PlannedRestart: &PlannedRestartStore{}, SubmitReload: func(req server.ReloadRequest) error { reload <- req; return nil }}
				applied, e := coordinator.ApplyRaw(admin.ApplyRequestContext{}, write(b.URL), ApplyHot)
				if e != nil {
					t.Fatal(e)
				}
				if !applied.OK || applied.Restored || !applied.Persisted {
					t.Fatalf("committed truth lost: %+v", applied)
				}
				rr = applied.Reload
				wire := toAdminConfigApplyResult(applied)
				raw, e := json.Marshal(wire)
				if e != nil {
					t.Fatal(e)
				}
				if wire.CacheCutoverResult == nil || !strings.Contains(string(raw), "recovery_guidance") {
					t.Fatalf("operator-facing apply lost recovery outcome: %s", raw)
				}
			} else {
				if err := os.WriteFile(path, write(b.URL), 0600); err != nil {
					t.Fatal(err)
				}
				results := make(chan server.ReloadResult, 1)
				reload <- server.ReloadRequest{ID: "cutover-failure", Source: server.ReloadSourceSIGHUP, Result: results}
				select {
				case r := <-results:
					rr = &r
				case <-time.After(10 * time.Second):
					t.Fatal("reload timed out")
				}
			}
			if rr == nil || rr.Outcome != server.ReloadAppliedDegraded || !rr.Published || rr.HTTP.Status != server.ReloadSubsystemFailed || rr.Error == "" {
				t.Fatalf("cutover failure reported as clean success: %+v", rr)
			}
			if rr.CacheCutover == nil || rr.CacheCutover.InvalidationEffective != "uncertain" || !strings.Contains(rr.CacheCutover.RecoveryGuidance, "Configuration is live") {
				t.Fatalf("missing structured guidance: %+v", rr.CacheCutover)
			}
			if body, _, e := get(); e != nil || body != "backend-B" {
				t.Fatalf("committed B not serving: %q %v", body, e)
			}
			if err := os.Remove(marker); err != nil {
				t.Fatal(err)
			}
			// A no-op must not inherit the previous generation's degraded cutover.
			results := make(chan server.ReloadResult, 1)
			reload <- server.ReloadRequest{ID: "unchanged", Source: server.ReloadSourceSIGHUP, Result: results}
			next := <-results
			if next.Outcome != server.ReloadNoChange || next.CacheCutover != nil {
				t.Fatalf("stale cutover leaked into no-op: %+v", next)
			}
		})
	}
}
