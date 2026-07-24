// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/admin"
	"jul/internal/config"
	"jul/internal/server"
)

// blockingPreflight returns a Preflight whose handler dry-run blocks until the
// bounded context expires, simulating a wedged handler build. It reports the
// preflight_handlers phase (via the normal dryRun path) before blocking so a
// deadline breach is attributed to that phase.
func blockingPreflight() *Preflight {
	return &Preflight{
		BuildHandlers: func(ctx context.Context, _ *config.Config, _ bool) (map[string]http.Handler, func(), error) {
			<-ctx.Done()
			return nil, nil, ctx.Err()
		},
		Stream: &mockStreamPreflighter{},
	}
}

// servingSnapshot returns a LiveSnapshot whose effective config carries the
// given reload_timeout so the coordinator bounds pre-persistence work with it.
func servingSnapshot(t *testing.T, timeout time.Duration) func() server.LiveSnapshot {
	t.Helper()
	cfg := config.ProxyTarget("127.0.0.1:9000", ":8080")
	cfg.Global.ReloadTimeout = config.Duration(timeout)
	return func() server.LiveSnapshot { return server.LiveSnapshot{EffectiveConfig: cfg} }
}

// TestCoordinatorHotApplyTimesOutPhaseHandlers verifies AC-08: a pre-persistence
// reload_timeout breach during the handler build aborts cleanly, names the
// phase, leaves disk untouched, and never enqueues a reload.
func TestCoordinatorHotApplyTimesOutPhaseHandlers(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	var submitted atomic.Bool
	c := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		Path:           path,
		Preflight:      blockingPreflight(),
		SubmitReload:   func(server.ReloadRequest) error { submitted.Store(true); return nil },
		LiveSnapshot:   servingSnapshot(t, 30*time.Millisecond),
		PlannedRestart: &PlannedRestartStore{},
	}

	newRaw := validConfigRaw(t, ":8081")
	res, err := c.ApplyRaw(admin.ApplyRequestContext{}, newRaw, ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if res.OK {
		t.Fatalf("ok = true, want false (a timed-out apply must fail)")
	}
	if res.TimedOutPhase != PreflightPhaseHandlers {
		t.Errorf("timed_out_phase = %q, want %q", res.TimedOutPhase, PreflightPhaseHandlers)
	}
	if res.Persisted {
		t.Error("Persisted = true; a pre-persistence timeout must not write disk")
	}
	if submitted.Load() {
		t.Error("SubmitReload was called; a timed-out apply must not enqueue a reload")
	}
	// Disk must retain the seed untouched.
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(onDisk) != string(seed) {
		t.Error("on-disk bytes changed; a pre-persistence timeout must leave disk untouched")
	}
}

// TestCoordinatorStageRestartTimesOutPhaseHandlers verifies the stage_restart
// path is bounded identically: a handler-build timeout surfaces the phase and
// stages nothing.
func TestCoordinatorStageRestartTimesOutPhaseHandlers(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	c := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		Path:           path,
		Preflight:      blockingPreflight(),
		SubmitReload:   func(server.ReloadRequest) error { return nil },
		LiveSnapshot:   servingSnapshot(t, 30*time.Millisecond),
		PlannedRestart: &PlannedRestartStore{ConfigPath: path},
	}

	newRaw := restartRequiredConfigRaw(t, ":8080")
	res, err := c.ApplyRaw(admin.ApplyRequestContext{}, newRaw, ApplyStageRestart)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if res.OK {
		t.Fatalf("ok = true, want false (a timed-out stage must fail)")
	}
	if res.TimedOutPhase != PreflightPhaseHandlers {
		t.Errorf("timed_out_phase = %q, want %q", res.TimedOutPhase, PreflightPhaseHandlers)
	}
	// Nothing staged: no marker should be pending and disk retains the seed.
	if c.PlannedRestart.IsPending() {
		t.Error("planned restart is pending; a timed-out stage must stage nothing")
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(onDisk) != string(seed) {
		t.Error("on-disk bytes changed; a timed-out stage must leave disk untouched")
	}
}

// TestServingReloadTimeoutPrefersServing verifies R15-01: the bounding budget is
// taken from the serving config, not the candidate. A candidate with a huge
// reload_timeout is still bounded by the small serving timeout.
func TestServingReloadTimeoutPrefersServing(t *testing.T) {
	c := &ConfigApplyCoordinator{
		LiveSnapshot: servingSnapshot(t, 5*time.Second),
	}
	candidate := config.ProxyTarget("127.0.0.1:9000", ":8080")
	candidate.Global.ReloadTimeout = config.Duration(9 * time.Hour)
	if got := c.servingReloadTimeout(candidate); got != 5*time.Second {
		t.Errorf("servingReloadTimeout = %v, want 5s (serving budget)", got)
	}
}

// TestServingReloadTimeoutFallsBackToCandidate verifies that without a live
// snapshot budget the candidate's own reload_timeout bounds preflight so unit
// tests without a runtime are still bounded.
func TestServingReloadTimeoutFallsBackToCandidate(t *testing.T) {
	c := &ConfigApplyCoordinator{
		LiveSnapshot: func() server.LiveSnapshot { return server.LiveSnapshot{} },
	}
	candidate := config.ProxyTarget("127.0.0.1:9000", ":8080")
	candidate.Global.ReloadTimeout = config.Duration(3 * time.Second)
	if got := c.servingReloadTimeout(candidate); got != 3*time.Second {
		t.Errorf("servingReloadTimeout = %v, want 3s (candidate fallback)", got)
	}
}

// TestServingReloadTimeoutZeroDisablesBounding verifies a zero budget yields no
// bounding so callers/tests without a configured timeout are unchanged.
func TestServingReloadTimeoutZeroDisablesBounding(t *testing.T) {
	c := &ConfigApplyCoordinator{
		LiveSnapshot: func() server.LiveSnapshot { return server.LiveSnapshot{} },
	}
	candidate := config.ProxyTarget("127.0.0.1:9000", ":8080")
	candidate.Global.ReloadTimeout = 0
	if got := c.servingReloadTimeout(candidate); got != 0 {
		t.Errorf("servingReloadTimeout = %v, want 0 (bounding disabled)", got)
	}
}
