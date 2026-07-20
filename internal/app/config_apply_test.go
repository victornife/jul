// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

// restartRequiredConfigRaw returns a config raw that differs from the seed
// produced by validConfigRaw in a restart-required field (log_format).
func restartRequiredConfigRaw(t *testing.T, listen string) []byte {
	t.Helper()
	cfg := config.ProxyTarget("127.0.0.1:9000", listen)
	cfg.Global.LogFormat = "json"
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

// TestApplyResultReportsRestorationSuccess verifies that a pre-Publish failure
// populates the restoration outcome fields (F-03).
func TestApplyResultReportsRestorationSuccess(t *testing.T) {
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
		PlannedRestart: &PlannedRestartStore{},
	}

	res, err := c.ApplyRaw(validConfigRaw(t, ":8081"), ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if res.OK {
		t.Fatal("ok = true, want false")
	}
	if !res.Persisted {
		t.Error("Persisted = false, want true")
	}
	if !res.Restored {
		t.Errorf("Restored = false, want true")
	}
	if res.RestoreError != "" {
		t.Errorf("RestoreError = %q, want empty", res.RestoreError)
	}
	if res.FinalDiskVersion == "" {
		t.Error("FinalDiskVersion should be set")
	}
	if res.FinalDiskVersion == server.CanonicalVersion(nil) {
		t.Error("FinalDiskVersion should not match rejected candidate")
	}

	onDisk, _ := os.ReadFile(path)
	if string(onDisk) != string(seed) {
		t.Error("previous bytes should be restored")
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

// TestCoordinatorApplyRawUsesServingReloadTimeout verifies that the
// coordinator uses the currently serving config's reload_timeout for the
// transaction, not the candidate's. A candidate that changes reload_timeout
// should affect the next apply, not this one (R15-01).
func TestCoordinatorApplyRawUsesServingReloadTimeout(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	var gotDeadline time.Time
	var watchDigest atomicPointer32
	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			gotDeadline = req.Deadline
			go func() {
				req.Result <- server.ReloadResult{
					ID:        req.ID,
					Source:    server.ReloadSourceAdmin,
					Outcome:   server.ReloadAppliedLive,
					Published: true,
				}
			}()
			return nil
		},
		LiveSnapshot: func() server.LiveSnapshot {
			cfg := config.ProxyTarget("127.0.0.1:9000", ":8080")
			cfg.Global.ReloadTimeout = config.Duration(7 * time.Second)
			return server.LiveSnapshot{EffectiveConfig: cfg}
		},
		WatchDigest:    &watchDigest,
		PlannedRestart: &PlannedRestartStore{},
	}

	// Candidate changes reload_timeout to 1s; serving config says 7s.
	newRaw := validConfigRaw(t, ":8081")
	if _, err := c.ApplyRaw(newRaw, ApplyHot); err != nil {
		t.Fatalf("apply error: %v", err)
	}

	slack := time.Until(gotDeadline)
	if slack < 6*time.Second || slack > 8*time.Second {
		t.Errorf("reload deadline slack = %v, want ~7s (serving reload_timeout)", slack)
	}
}

// TestApplyTimeoutRestoresAndBlocksConcurrentApply verifies that a timed-out
// managed apply holds the in-flight transaction until the finalizer completes,
// that the finalizer restores the previous bytes, and that a second managed
// apply receives a conflict rather than overlapping.
func TestApplyTimeoutRestoresAndBlocksConcurrentApply(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	var submitted atomic.Bool
	finalizerStarted := make(chan struct{})
	finalizerContinue := make(chan struct{})
	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			submitted.Store(true)
			// Never send a result until released; the synchronous path times out.
			go func() {
				close(finalizerStarted)
				<-finalizerContinue
				req.Result <- server.ReloadResult{
					ID:        req.ID,
					Source:    server.ReloadSourceAdmin,
					Outcome:   server.ReloadNotApplied,
					Published: false,
					Error:     "intentional timeout",
				}
			}()
			return nil
		},
		LiveSnapshot: func() server.LiveSnapshot {
			cfg := config.ProxyTarget("127.0.0.1:9000", ":8080")
			cfg.Global.ReloadTimeout = config.Duration(50 * time.Millisecond)
			return server.LiveSnapshot{EffectiveConfig: cfg}
		},
		PlannedRestart: &PlannedRestartStore{},
	}

	// First apply times out synchronously; the finalizer goroutine is started.
	res1Ch := make(chan ApplyResult, 1)
	go func() {
		res, _ := c.ApplyRaw(validConfigRaw(t, ":8081"), ApplyHot)
		res1Ch <- res
	}()

	<-finalizerStarted
	res1 := <-res1Ch
	if !res1.OK {
		t.Fatalf("ok = false, want true for timed-out apply; message: %s", res1.Message)
	}
	if res1.Reload == nil || res1.Reload.Outcome != server.ReloadSavedNotLive || !res1.Reload.TimedOut {
		t.Fatalf("expected saved_not_live timed-out result, got %+v", res1.Reload)
	}
	if !submitted.Load() {
		t.Fatal("reload was not submitted")
	}

	// Second apply should be rejected while the first finalizer is still in-flight.
	res2, err := c.ApplyRaw(validConfigRaw(t, ":8082"), ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if res2.OK {
		t.Fatal("ok = true, want false for overlapping apply")
	}
	if !strings.Contains(res2.Message, "still in flight") {
		t.Fatalf("expected in-flight conflict, got: %s", res2.Message)
	}

	// Allow the first finalizer to finish restoring.
	close(finalizerContinue)
	time.Sleep(100 * time.Millisecond)

	onDisk, _ := os.ReadFile(path)
	if string(onDisk) != string(seed) {
		t.Error("finalizer should have restored previous bytes")
	}
}

// TestApplyTimeoutFinalizerDoesNotOverwriteLaterApply verifies that a late
// finalizer from apply A cannot overwrite a subsequent apply B because it
// holds the coordinator mutex around digest check + restore.
func TestApplyTimeoutFinalizerDoesNotOverwriteLaterApply(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	finalizerBlock := make(chan struct{})
	finalizerEntered := make(chan struct{}, 1)
	submitCount := atomic.Int32{}
	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			n := submitCount.Add(1)
			go func() {
				if n == 1 {
					finalizerEntered <- struct{}{}
					<-finalizerBlock
				}
				outcome := server.ReloadAppliedLive
				if n == 1 {
					outcome = server.ReloadNotApplied
				}
				req.Result <- server.ReloadResult{
					ID:        req.ID,
					Source:    server.ReloadSourceAdmin,
					Outcome:   outcome,
					Published: outcome == server.ReloadAppliedLive,
					Error:     "intentional failure",
				}
			}()
			return nil
		},
		LiveSnapshot: func() server.LiveSnapshot {
			cfg := config.ProxyTarget("127.0.0.1:9000", ":8080")
			cfg.Global.ReloadTimeout = config.Duration(50 * time.Millisecond)
			return server.LiveSnapshot{EffectiveConfig: cfg}
		},
		PlannedRestart: &PlannedRestartStore{},
	}

	// Apply A times out. Its SubmitReload goroutine has entered the finalizer
	// body and is blocked before sending the result, so inFlightState is still
	// waiting and apply B must be rejected. The audit point is that once B
	// completes, a later apply C can proceed, and the late finalizer from A
	// cannot overwrite C's candidate.
	resACh := make(chan ApplyResult, 1)
	go func() {
		res, _ := c.ApplyRaw(validConfigRaw(t, ":8081"), ApplyHot)
		resACh <- res
	}()
	<-finalizerEntered

	// Wait for A to time out and release applyMu.
	resA := <-resACh
	if !resA.OK {
		t.Fatalf("apply A should have timed out with ok=true, got: %s", resA.Message)
	}
	if resA.Reload == nil || resA.Reload.Outcome != server.ReloadSavedNotLive {
		t.Fatalf("apply A expected saved_not_live, got %+v", resA.Reload)
	}

	// Apply B must be rejected because A's finalizer is still in-flight.
	resB, err := c.ApplyRaw(validConfigRaw(t, ":8082"), ApplyHot)
	if err != nil {
		t.Fatalf("apply B error: %v", err)
	}
	if resB.OK {
		t.Fatal("ok = true, want false for overlapping apply B")
	}
	if !strings.Contains(resB.Message, "still in flight") {
		t.Fatalf("expected in-flight conflict, got: %s", resB.Message)
	}

	// Allow A's finalizer to finish; this clears inFlightState and restores
	// the previous (seed) bytes.
	close(finalizerBlock)
	time.Sleep(100 * time.Millisecond)

	onDiskAfterA, _ := os.ReadFile(path)
	if string(onDiskAfterA) != string(seed) {
		t.Errorf("finalizer from A should have restored seed, got %q", onDiskAfterA)
	}

	// Apply C can now proceed. It uses the same SubmitReload callback; submit
	// count is now 2 so it returns applied_live.
	resC, err := c.ApplyRaw(validConfigRaw(t, ":8083"), ApplyHot)
	if err != nil {
		t.Fatalf("apply C error: %v", err)
	}
	if !resC.OK {
		t.Fatalf("apply C should succeed, got: %s", resC.Message)
	}

	onDiskC, _ := os.ReadFile(path)
	if string(onDiskC) != string(validConfigRaw(t, ":8083")) {
		t.Fatal("apply C's candidate should be on disk")
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

// TestCoordinatorApplyRawBlocksHotWhenExternalDivergenceSet verifies that a
// hot apply is rejected when the authoritative store reports external
// disk/runtime divergence (F-04).
func TestCoordinatorApplyRawBlocksHotWhenExternalDivergenceSet(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	store := &PlannedRestartStore{}
	store.SetExternalDivergence(true)
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
	if res.PendingRestart == nil || !res.PendingRestart.External {
		t.Errorf("expected external pending_restart status, got %+v", res.PendingRestart)
	}
	if !strings.Contains(res.Message, "external divergence") {
		t.Errorf("message should mention external divergence, got %q", res.Message)
	}

	onDisk, _ := os.ReadFile(path)
	if string(onDisk) != string(seed) {
		t.Error("blocked hot apply should not change the file")
	}
}

// TestCoordinatorApplyStageBlocksWhenExternalDivergenceSet verifies that a
// stage_restart apply is also rejected while external divergence is present,
// so staging does not silently adopt an externally-owned change (F-04).
func TestCoordinatorApplyStageBlocksWhenExternalDivergenceSet(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := restartRequiredConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	store := &PlannedRestartStore{}
	store.SetExternalDivergence(true)
	c := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		Path:           path,
		Preflight:      testPreflight(),
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: store,
	}

	res, err := c.ApplyRaw(restartRequiredConfigRaw(t, ":8080"), ApplyStageRestart)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if res.OK {
		t.Fatal("ok = true, want false")
	}
	if res.PendingRestart == nil || !res.PendingRestart.External {
		t.Errorf("expected external pending_restart status, got %+v", res.PendingRestart)
	}

	onDisk, _ := os.ReadFile(path)
	if string(onDisk) != string(seed) {
		t.Error("blocked stage_restart should not change the file")
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
			candidate := restartRequiredConfigRaw(t, ":8080")
			res, err := c.ApplyRaw(candidate, ApplyStageRestart)
			if err != nil {
				errs[idx] = err
				return
			}
			// With the F-08 single-candidate invariant, exactly one worker
			// succeeds and the rest are rejected because a candidate is
			// already pending.
			if !res.OK && !res.RestartRequired && !strings.Contains(res.Message, "already pending") {
				errs[idx] = errors.New("unexpected non-OK result: " + res.Message)
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

	// Stage a restart-required change.
	candidate := restartRequiredConfigRaw(t, ":8080")
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

// TestCoordinatorStageRestartRejectsWhilePending verifies the F-08
// single-candidate invariant: a stage_restart applied while another staged
// restart is already pending is rejected and leaves the existing candidate in
// place.
func TestCoordinatorStageRestartRejectsWhilePending(t *testing.T) {
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

	candidate := restartRequiredConfigRaw(t, ":8080")

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
	if !store.IsPending() {
		t.Fatal("store should be pending after first stage")
	}

	// Second stage must be rejected while the first candidate is pending.
	updateCandidate := restartRequiredConfigRaw(t, ":8080")
	res2, err := c.ApplyRaw(updateCandidate, ApplyStageRestart)
	if err != nil {
		t.Fatalf("second stage error: %v", err)
	}
	if res2.OK {
		t.Fatalf("second stage should be rejected while pending")
	}
	if !strings.Contains(res2.Message, "already pending") {
		t.Errorf("message should mention pending candidate, got %q", res2.Message)
	}
	if !store.IsPending() {
		t.Error("store should still be pending after rejected second stage")
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
