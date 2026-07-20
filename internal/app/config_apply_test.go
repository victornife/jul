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
		BuildHandlers: func(_ context.Context, _ *config.Config, _ bool) (map[string]http.Handler, func(), error) {
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

// ── H-07 race/interaction tests ───────────────────────────────────────────────

// TestCoordinatorConcurrentStageRestartsSerialized verifies that concurrent
// stage_restart applies do not race under the Go race detector. The coordinator
// serializes applies via applyMu so only one stage completes at a time; all
// goroutines must either succeed or return a clean error — never corrupt state.
func TestCoordinatorConcurrentStageRestartsSerialized(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	original := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	store := NewFilePlannedRestartStore(path)
	pf := testPreflightStage(t, original)
	c := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		Path:           path,
		Preflight:      pf,
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: store,
	}

	const workers = 8
	errs := make([]error, workers)
	done := make(chan struct{})
	for i := 0; i < workers; i++ {
		go func(idx int) {
			defer func() { done <- struct{}{} }()
			candidate := validConfigRaw(t, ":8080")
			res, err := c.ApplyRaw(candidate, ApplyStageRestart)
			if err != nil {
				errs[idx] = err
				return
			}
			if !res.OK && !res.RestartRequired {
				errs[idx] = errors.New("unexpected non-OK result")
			}
		}(i)
	}
	for i := 0; i < workers; i++ {
		<-done
	}
	for i, err := range errs {
		if err != nil {
			t.Errorf("worker %d: %v", i, err)
		}
	}
	// After concurrent stages, the store must be in a deterministic state:
	// exactly one staged pending restart.
	if !store.IsPending() {
		t.Error("expected a pending staged restart after concurrent stages")
	}
}

// TestCoordinatorStageDiscardRace verifies that a discard immediately following
// a stage does not leave the store in an inconsistent state. This exercises the
// serialization path (applyMu is held for both operations).
func TestCoordinatorStageDiscardRace(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	original := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	store := NewFilePlannedRestartStore(path)
	pf := testPreflightStage(t, original)
	c := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		Path:           path,
		Preflight:      pf,
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: store,
	}

	// Stage.
	candidate := validConfigRaw(t, ":8080")
	res, err := c.ApplyRaw(candidate, ApplyStageRestart)
	if err != nil {
		t.Fatalf("stage error: %v", err)
	}
	if !res.OK {
		t.Fatalf("stage ok=false: %s", res.Message)
	}
	if !store.IsPending() {
		t.Fatal("expected pending after stage")
	}

	// Discard immediately.
	discardRes, err := c.DiscardPlannedRestart()
	if err != nil {
		t.Fatalf("discard error: %v", err)
	}
	if !discardRes.OK {
		t.Fatalf("discard ok=false: %s", discardRes.Message)
	}
	if store.IsPending() {
		t.Error("expected no pending restart after discard")
	}
	// On-disk bytes must be the original.
	onDisk, _ := os.ReadFile(path)
	if string(onDisk) != string(original) {
		t.Error("discard should restore original bytes")
	}
}

// TestCoordinatorStagedRestartIsUpdateFlag verifies that wasPendingBefore is
// false on the first stage and true on a subsequent update stage (M-04 fix:
// created/updated audit event distinction relies on this ordering).
func TestCoordinatorStagedRestartIsUpdateFlag(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	original := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	store := NewFilePlannedRestartStore(path)
	pf := testPreflightStage(t, original)
	c := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		Path:           path,
		Preflight:      pf,
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: store,
	}

	candidate := validConfigRaw(t, ":8080")

	// First stage — store must NOT be pending before.
	if store.IsPending() {
		t.Fatal("store should not be pending before first stage")
	}
	res1, err := c.ApplyRaw(candidate, ApplyStageRestart)
	if err != nil {
		t.Fatalf("first stage error: %v", err)
	}
	if !res1.OK {
		t.Fatalf("first stage ok=false: %s", res1.Message)
	}

	// Second stage — store MUST be pending before (it was set by the first).
	if !store.IsPending() {
		t.Fatal("store should be pending before second stage")
	}
	res2, err := c.ApplyRaw(candidate, ApplyStageRestart)
	if err != nil {
		t.Fatalf("second stage error: %v", err)
	}
	if !res2.OK {
		t.Fatalf("second stage ok=false: %s", res2.Message)
	}
}

// testPreflightStage builds a Preflight that accepts a stage_restart apply for
// a config that is identical to the running startup config (hot path would also
// accept it, but we test stage mode here). The StartupFP is derived from the
// seed bytes so restart-required classification sees them as unchanged.
func testPreflightStage(t *testing.T, seed []byte) *Preflight {
	t.Helper()
	cfg, err := config.Parse(seed)
	if err != nil {
		t.Fatalf("parse seed config: %v", err)
	}
	fp := lifecycle.ComputeFingerprint(cfg)
	p := testPreflight()
	p.StartupFP = fp
	return p
}
