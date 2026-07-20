// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/server"
)

func testPreflight() *Preflight {
	return &Preflight{
		BuildHandlers: func(_ *config.Config, _ bool) (map[string]http.Handler, func(), error) {
			return map[string]http.Handler{}, nil, nil
		},
		Stream: &mockStreamPreflighter{},
	}
}

func validConfigRaw(t *testing.T, listen string) []byte {
	t.Helper()
	cfg := config.ProxyTarget("127.0.0.1:9000", listen)
	raw, err := config.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	return raw
}

func TestCoordinatorApplyRawSuccess(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	var watchDigest atomicPointer32
	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			go func() {
				req.Result <- server.ReloadResult{
					ID:             req.ID,
					Source:         server.ReloadSourceAdmin,
					Outcome:        server.ReloadAppliedLive,
					Published:      true,
					ServingVersion: "v2",
				}
			}()
			return nil
		},
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		WatchDigest:    &watchDigest,
		PlannedRestart: &PlannedRestartStore{},
	}

	newRaw := validConfigRaw(t, ":8081")
	res, err := c.ApplyRaw(newRaw, ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if !res.OK {
		t.Fatalf("ok = false, want true; message: %s", res.Message)
	}
	if res.Mode != ApplyHot {
		t.Errorf("mode = %v, want %v", res.Mode, ApplyHot)
	}

	// The file should contain the new bytes and watcher suppression should be
	// registered for the new digest.
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(onDisk) != string(newRaw) {
		t.Error("on-disk bytes do not match applied bytes")
	}
	if watchDigest.Load() == nil {
		t.Error("watcher digest was not set")
	}
}

func TestCoordinatorApplyRawValidationFailureNoWrite(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	c := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		Path:           path,
		Preflight:      testPreflight(),
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: &PlannedRestartStore{},
	}

	res, err := c.ApplyRaw([]byte("{bad toml"), ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if res.OK {
		t.Fatal("ok = true, want false")
	}
	if len(res.ValidationErrors) == 0 {
		t.Error("expected validation errors")
	}

	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(onDisk) != string(seed) {
		t.Error("validation failure should not change the file")
	}
}

func TestCoordinatorApplyRawRestartRequiredCanStage(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := config.ProxyTarget("127.0.0.1:9000", ":8080")
	seed.Global.LogFormat = "json"
	seedRaw, err := config.Marshal(seed)
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	if err := os.WriteFile(path, seedRaw, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	p := testPreflight()
	p.StartupFP = lifecycle.ComputeFingerprint(seed)

	c := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		Path:           path,
		Preflight:      p,
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: &PlannedRestartStore{},
	}

	// Change a restart-required field.
	next := config.ProxyTarget("127.0.0.1:9000", ":8080")
	next.Global.LogFormat = "combined"
	nextRaw, err := config.Marshal(next)
	if err != nil {
		t.Fatalf("marshal next: %v", err)
	}

	res, err := c.ApplyRaw(nextRaw, ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if res.OK {
		t.Fatal("ok = true, want false")
	}
	if !res.RestartRequired {
		t.Error("RestartRequired = false, want true")
	}
	if !res.CanStage {
		t.Error("CanStage = false, want true")
	}

	onDisk, _ := os.ReadFile(path)
	if string(onDisk) != string(seedRaw) {
		t.Error("restart-required rejection should not change the file")
	}
}

func TestCoordinatorApplyRawRestoresOnSubmitFailure(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	var watchDigest atomicPointer32
	c := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		Path:           path,
		Preflight:      testPreflight(),
		SubmitReload:   func(req server.ReloadRequest) error { return errors.New("enqueue failed") },
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		WatchDigest:    &watchDigest,
		PlannedRestart: &PlannedRestartStore{},
	}

	newRaw := validConfigRaw(t, ":8081")
	res, err := c.ApplyRaw(newRaw, ApplyHot)
	if err == nil {
		t.Fatal("expected enqueue error")
	}
	if res.OK {
		t.Fatal("ok = true, want false")
	}

	onDisk, _ := os.ReadFile(path)
	if string(onDisk) != string(seed) {
		t.Error("submit failure should restore previous bytes")
	}
	if watchDigest.Load() == nil {
		t.Error("restoration watcher suppression should be registered")
	}
}

func TestCoordinatorApplyRawRestoresOnPrePublishFailure(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	var watchDigest atomicPointer32
	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			// Simulate a pre-Publish failure returned by the server.
			go func() {
				req.Result <- server.ReloadResult{
					ID:        req.ID,
					Source:    server.ReloadSourceAdmin,
					Outcome:   server.ReloadNotApplied,
					Published: false,
					Error:     "bind failed",
				}
			}()
			return nil
		},
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		WatchDigest:    &watchDigest,
		PlannedRestart: &PlannedRestartStore{},
	}

	newRaw := validConfigRaw(t, ":8081")
	res, err := c.ApplyRaw(newRaw, ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if res.OK {
		t.Fatal("ok = true, want false")
	}

	onDisk, _ := os.ReadFile(path)
	if string(onDisk) != string(seed) {
		t.Error("pre-Publish failure should restore previous bytes")
	}
}

func TestCoordinatorApplyRawRetainsFileOnPostPublishDegradation(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			go func() {
				req.Result <- server.ReloadResult{
					ID:             req.ID,
					Source:         server.ReloadSourceAdmin,
					Outcome:        server.ReloadAppliedDegraded,
					Published:      true,
					ServingVersion: "degraded-version",
					Error:          "stream reload failed",
				}
			}()
			return nil
		},
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: &PlannedRestartStore{},
	}

	newRaw := validConfigRaw(t, ":8081")
	res, err := c.ApplyRaw(newRaw, ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if !res.OK {
		t.Fatalf("ok = false, want true; message: %s", res.Message)
	}
	if res.Reload == nil || res.Reload.Outcome != server.ReloadAppliedDegraded {
		t.Error("expected degraded reload outcome")
	}

	onDisk, _ := os.ReadFile(path)
	if string(onDisk) != string(newRaw) {
		t.Error("post-Publish degradation should retain the candidate file")
	}
}

func TestCoordinatorApplyRawSerialized(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	cfg := config.ProxyTarget("127.0.0.1:9000", ":8080")
	cfg.Global.ReloadTimeout = config.Duration(100 * time.Millisecond)
	seed, err := config.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			// Return a successful result immediately so the test does not wait
			// for the reload timeout.
			go func() {
				req.Result <- server.ReloadResult{
					ID:             req.ID,
					Source:         server.ReloadSourceAdmin,
					Outcome:        server.ReloadAppliedLive,
					Published:      true,
					ServingVersion: "v2",
				}
			}()
			return nil
		},
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: &PlannedRestartStore{},
	}

	done := make(chan struct{}, 2)
	for i := 0; i < 2; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			_, _ = c.ApplyRaw(validConfigRaw(t, ":8081"), ApplyHot)
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent applies did not complete")
		}
	}
}

func TestCoordinatorApplyRawBlocksHotWhenPlannedRestartPending(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	store := &PlannedRestartStore{}
	store.Stage([]byte("staged"))
	c := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		Path:           path,
		Preflight:      testPreflight(),
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: store,
	}

	res, err := c.ApplyRaw(validConfigRaw(t, ":8081"), ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if res.OK {
		t.Fatal("ok = true, want false")
	}
	if res.PendingRestart == nil || !res.PendingRestart.Managed {
		t.Error("expected pending_restart status")
	}

	onDisk, _ := os.ReadFile(path)
	if string(onDisk) != string(seed) {
		t.Error("blocked hot apply should not change the file")
	}
}

func TestCoordinatorApplyConfigMarshalsAndApplies(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			go func() {
				req.Result <- server.ReloadResult{
					ID:             req.ID,
					Source:         server.ReloadSourceAdmin,
					Outcome:        server.ReloadAppliedLive,
					Published:      true,
					ServingVersion: "v2",
				}
			}()
			return nil
		},
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: &PlannedRestartStore{},
	}

	cfg := config.ProxyTarget("127.0.0.1:9001", ":8081")
	res, err := c.ApplyConfig(cfg, ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if !res.OK {
		t.Fatalf("ok = false, want true; message: %s", res.Message)
	}

	onDisk, _ := os.ReadFile(path)
	if len(onDisk) == 0 {
		t.Error("file should contain marshaled config")
	}
}

func TestCoordinatorDiscardPlannedRestart(t *testing.T) {
	store := &PlannedRestartStore{}
	store.Stage([]byte("staged"))
	c := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		PlannedRestart: store,
	}

	res, err := c.DiscardPlannedRestart()
	if err != nil {
		t.Fatalf("discard error: %v", err)
	}
	if !res.OK {
		t.Fatal("ok = false, want true")
	}
	if store.IsPending() {
		t.Error("planned restart should be discarded")
	}
}

// atomicPointer32 is a test helper type alias to avoid repeating the verbose
// atomic pointer type in every test.
type atomicPointer32 = atomic.Pointer[[32]byte]
