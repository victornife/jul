// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/server"
	"jul/internal/upstream"
)

func pluginModule(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "plugins", name+".wasm"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func moduleHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

type pluginReloadHarness struct {
	t       *testing.T
	f       *HandlerFactory
	srv     *server.Server
	cfgPath string
	modPath string
	addr    string
	backend string
	reload  chan server.ReloadRequest
}

func (h *pluginReloadHarness) writeConfig(pin string) {
	h.t.Helper()
	raw := fmt.Sprintf(`
[plugins.p]
path = %q
sha256 = %q

[[servers]]
listen = %q
plugins = ["p"]
[[servers.locations]]
match = { type = "prefix", path = "/" }
proxy_pass = %q
`, h.modPath, pin, h.addr, h.backend)
	if err := os.WriteFile(h.cfgPath, []byte(raw), 0o600); err != nil {
		h.t.Fatal(err)
	}
}

func (h *pluginReloadHarness) reloadNow() server.ReloadResult {
	h.t.Helper()
	res := make(chan server.ReloadResult, 1)
	h.reload <- server.ReloadRequest{Source: server.ReloadSourceSIGHUP, Result: res}
	select {
	case rr := <-res:
		return rr
	case <-time.After(30 * time.Second):
		h.t.Fatal("reload result not delivered")
		return server.ReloadResult{}
	}
}

func (h *pluginReloadHarness) get(block bool) *http.Response {
	h.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "http://"+h.addr+"/", nil)
	if block {
		req.Header.Set("X-Block", "1")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("request: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp
}

func newPluginReloadHarness(t *testing.T, backend http.Handler) *pluginReloadHarness {
	t.Helper()
	be := httptest.NewServer(backend)
	t.Cleanup(be.Close)
	dir := t.TempDir()
	h := &pluginReloadHarness{t: t, cfgPath: filepath.Join(dir, "jul.toml"), modPath: filepath.Join(dir, "p.wasm"), addr: freePort(t), backend: be.URL, reload: make(chan server.ReloadRequest)}
	if err := os.WriteFile(h.modPath, pluginModule(t, "header-inject"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.writeConfig("")

	f, cleanup := minimalFactory(t)
	t.Cleanup(cleanup)
	h.f = f
	src := config.NewTOMLSource(h.cfgPath)
	cfg, err := src.Load()
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := config.NewCandidate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h.srv = server.New(candidate.Effective, candidate.Raw, lifecycle.ComputeFingerprint(candidate.Effective), f.Log,
		func(ctx context.Context, c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
			return f.Prepare(ctx, c)
		}, src, func(context.Context, *config.Config) error { return nil })
	h.srv.PluginModulesUnchanged = f.PluginModulesUnchanged
	h.srv.PluginModuleChanges = f.PluginModuleChanges

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.srv.Run(ctx, h.reload, candidate.Redaction) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := http.Get("http://" + h.addr + "/")
		if err == nil {
			_ = resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not start: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return h
}

// TestPluginModuleIdentityReloadLifecycle is the #429 end-to-end reload
// contract on a real server and handler factory:
// same bytes are a true no-op; same path with changed bytes is a real
// replacement that reports old/new digests; a failed pin is rejected before
// Publish and the serving generation keeps its module.
func TestPluginModuleIdentityReloadLifecycle(t *testing.T) {
	h := newPluginReloadHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	a, b := pluginModule(t, "header-inject"), pluginModule(t, "request-block")

	if got := h.get(false).Header.Get("X-Plugin"); got != "header-inject" {
		t.Fatalf("initial module not serving: X-Plugin=%q", got)
	}
	if id := h.f.PluginModules()["p"]; id.Digest != "sha256:"+moduleHex(a) {
		t.Fatalf("live identity = %+v", id)
	}

	if rr := h.reloadNow(); rr.Outcome != server.ReloadNoChange || len(rr.PluginModules) != 0 {
		t.Fatalf("same bytes: outcome %s modules %+v, want no_change", rr.Outcome, rr.PluginModules)
	}

	if err := os.WriteFile(h.modPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	rr := h.reloadNow()
	if rr.Outcome != server.ReloadAppliedLive {
		t.Fatalf("same path, changed bytes: outcome %s (%s), want applied_live", rr.Outcome, rr.Error)
	}
	want := []server.PluginModuleChange{{Name: "p", Before: "sha256:" + moduleHex(a), After: "sha256:" + moduleHex(b)}}
	if len(rr.PluginModules) != 1 || rr.PluginModules[0] != want[0] {
		t.Fatalf("plugin module changes = %+v, want %+v", rr.PluginModules, want)
	}
	if resp := h.get(true); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("replacement module not serving: status %d", resp.StatusCode)
	}

	// Pin the old bytes while the file holds the new ones: rejected before
	// Publish, and the serving generation keeps the new module.
	h.writeConfig(moduleHex(a))
	rr = h.reloadNow()
	if rr.Published || rr.Outcome == server.ReloadAppliedLive || rr.Outcome == server.ReloadNoChange {
		t.Fatalf("pin mismatch published: %+v", rr)
	}
	if resp := h.get(true); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("failed pin disturbed the serving module: status %d", resp.StatusCode)
	}
	if id := h.f.PluginModules()["p"]; id.Digest != "sha256:"+moduleHex(b) {
		t.Fatalf("live identity changed by a rejected candidate: %+v", id)
	}

	// A matching pin applies; its identity reports the pin.
	h.writeConfig(moduleHex(b))
	if rr := h.reloadNow(); rr.Outcome != server.ReloadAppliedLive || len(rr.PluginModules) != 0 {
		t.Fatalf("matching pin: outcome %s modules %+v", rr.Outcome, rr.PluginModules)
	}
	if id := h.f.PluginModules()["p"]; !id.Pinned {
		t.Fatalf("pinned identity not reported: %+v", id)
	}
}

// TestPluginReplacementDrainsInFlightRequests proves an in-flight request on
// the old generation completes on its own module while new requests use the
// replacement, and repeated replacement leaves no plugin runtimes behind.
func TestPluginReplacementDrainsInFlightRequests(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	h := newPluginReloadHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Hold") == "1" {
			entered <- struct{}{}
			<-release
		}
	}))
	a, b := pluginModule(t, "header-inject"), pluginModule(t, "request-block")

	held := make(chan *http.Response, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodGet, "http://"+h.addr+"/", nil)
		req.Header.Set("X-Hold", "1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Errorf("held request: %v", err)
			held <- nil
			return
		}
		_ = resp.Body.Close()
		held <- resp
	}()
	<-entered

	if err := os.WriteFile(h.modPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if rr := h.reloadNow(); rr.Outcome != server.ReloadAppliedLive {
		t.Fatalf("replacement: %s (%s)", rr.Outcome, rr.Error)
	}
	if resp := h.get(true); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("new requests not on the replacement: %d", resp.StatusCode)
	}
	close(release)
	resp := <-held
	if resp == nil || resp.StatusCode != http.StatusOK || resp.Header.Get("X-Plugin") != "header-inject" {
		t.Fatalf("in-flight request did not complete on its generation's module: %+v", resp)
	}

	for i := 0; i < 10; i++ {
		mod := a
		if i%2 == 1 {
			mod = b
		}
		if err := os.WriteFile(h.modPath, mod, 0o600); err != nil {
			t.Fatal(err)
		}
		if rr := h.reloadNow(); rr.Outcome != server.ReloadAppliedLive || len(rr.PluginModules) != 1 {
			t.Fatalf("churn %d: %s modules=%+v", i, rr.Outcome, rr.PluginModules)
		}
	}
}
