// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/admin"
	"jul/internal/config"
	"jul/internal/server"
)

// TestCoordinatorReloadReusesAdmissionDeadline verifies AC-08/R15-01: the ONE
// absolute deadline bound at HTTP admission is carried verbatim to the reload
// request. applyCandidate must not restart the clock or grant a fresh full
// timeout after preflight, so the reload deadline equals the admission deadline
// even when the serving reload_timeout would produce a different value.
func TestCoordinatorReloadReusesAdmissionDeadline(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	var (
		mu          sync.Mutex
		gotDeadline time.Time
	)
	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			mu.Lock()
			gotDeadline = req.Deadline
			mu.Unlock()
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
		// A small serving reload_timeout would, under the reset defect, produce
		// a reload deadline ~20ms out; the bound admission deadline is seconds
		// out, so an exact match proves the deadline was not recomputed.
		LiveSnapshot:   servingSnapshot(t, 20*time.Millisecond),
		PlannedRestart: &PlannedRestartStore{},
	}

	admissionDeadline := time.Now().Add(5 * time.Second)
	reqCtx := admin.ApplyRequestContext{
		StartedAt:      time.Now().UTC(),
		Deadline:       admissionDeadline,
		RequestContext: context.Background(),
	}
	res, err := c.ApplyRaw(reqCtx, validConfigRaw(t, ":8081"), ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if !res.OK {
		t.Fatalf("ok = false, want true: %+v", res)
	}

	mu.Lock()
	defer mu.Unlock()
	if !gotDeadline.Equal(admissionDeadline) {
		t.Fatalf("reload deadline = %v, want the admission deadline %v (deadline must not be reset after preflight)", gotDeadline, admissionDeadline)
	}
}

// TestCoordinatorBoundDeadlineGovernsPreflightOverServingTimeout verifies that
// the deadline bound at admission — not the serving reload_timeout — bounds
// pre-persistence work (AC-08). A short admission deadline aborts a wedged
// handler build quickly even when the serving reload_timeout is large, proving
// preflightContext derives from reqCtx.Deadline.
func TestCoordinatorBoundDeadlineGovernsPreflightOverServingTimeout(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	var submitted atomic.Bool
	enteredHandlers := make(chan struct{})
	var enteredOnce sync.Once
	clock := newFakeClock(time.Unix(0, 0).UTC())
	c := &ConfigApplyCoordinator{
		BaseCtx: context.Background(),
		Path:    path,
		Preflight: &Preflight{
			BuildHandlers: func(ctx context.Context, _ *config.Config, _ bool) (map[string]http.Handler, func(), error) {
				enteredOnce.Do(func() { close(enteredHandlers) })
				<-ctx.Done()
				return nil, nil, ctx.Err()
			},
			Stream: &mockStreamPreflighter{},
		},
		SubmitReload: func(server.ReloadRequest) error { submitted.Store(true); return nil },
		// Large serving timeout: if preflight derived from it, advancing the
		// shorter admission deadline below would not unblock the handler build.
		LiveSnapshot:   servingSnapshot(t, 10*time.Second),
		PlannedRestart: &PlannedRestartStore{},
		clock:          clock,
	}

	startedAt := clock.Now().UTC()
	admissionDeadline := startedAt.Add(40 * time.Millisecond)
	reqCtx := admin.ApplyRequestContext{
		StartedAt:      startedAt,
		Deadline:       admissionDeadline,
		RequestContext: context.Background(),
	}

	type outcome struct {
		res ApplyResult
		err error
	}
	done := make(chan outcome, 1)
	beforeTimers := clock.timerCount()
	go func() {
		res, err := c.ApplyRaw(reqCtx, validConfigRaw(t, ":8081"), ApplyHot)
		done <- outcome{res: res, err: err}
	}()

	select {
	case <-enteredHandlers:
	case <-time.After(2 * time.Second):
		t.Fatal("apply did not enter the blocking preflight handler gate")
	}
	waitForFakeTimerCount(t, clock, beforeTimers+1)
	clock.Advance(admissionDeadline.Sub(clock.Now()) + time.Nanosecond)

	var got outcome
	select {
	case got = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("apply did not honor the fake admission deadline")
	}
	if got.err != nil {
		t.Fatalf("apply error: %v", got.err)
	}
	if got.res.OK {
		t.Fatalf("ok = true, want false (bound deadline breach must fail)")
	}
	if got.res.TimedOutPhase != PreflightPhaseHandlers {
		t.Errorf("timed_out_phase = %q, want %q", got.res.TimedOutPhase, PreflightPhaseHandlers)
	}
	if got.res.Persisted {
		t.Error("Persisted = true; a pre-persistence timeout must leave disk untouched")
	}
	if submitted.Load() {
		t.Error("SubmitReload was called; a timed-out apply must not enqueue a reload")
	}
}

// TestCoordinatorAbortsBeforePersistWhenDeadlineExpiredDuringGates verifies the
// AC-08 gate-expiry check (§4.9): when the transaction deadline has already
// fired but the preflight gates return no error (a gate that ignores the
// context, or one that completed a hair after expiry), the coordinator must
// still abort before persistence rather than writing disk and enqueuing a
// reload. The prepared-candidate path is used so the ctx-checking resolve gate
// is bypassed and the passing gates alone drive the outcome.
func TestCoordinatorAbortsBeforePersistWhenDeadlineExpiredDuringGates(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	newRaw := validConfigRaw(t, ":8081")
	parsed, err := config.Parse(newRaw)
	if err != nil {
		t.Fatalf("parse candidate: %v", err)
	}
	prepared, err := config.NewCandidate(parsed)
	if err != nil {
		t.Fatalf("build prepared candidate: %v", err)
	}

	var submitted atomic.Bool
	c := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		Path:           path,
		Preflight:      testPreflight(), // gates ignore ctx and return nil
		SubmitReload:   func(server.ReloadRequest) error { submitted.Store(true); return nil },
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: &PlannedRestartStore{},
	}

	reqCtx := admin.ApplyRequestContext{
		StartedAt:      time.Now().UTC(),
		Deadline:       time.Now().Add(-time.Millisecond), // already fired
		RequestContext: context.Background(),
		Candidate:      prepared, // forces the ApplyCandidate path, skipping the resolve ctx-check
	}
	res, err := c.ApplyRaw(reqCtx, newRaw, ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if res.OK {
		t.Fatalf("ok = true, want false: an expired deadline must abort before persistence even when gates pass")
	}
	if res.TimedOutPhase == "" {
		t.Fatal("timed_out_phase empty; a post-gate deadline expiry must be attributed to a phase")
	}
	if res.Persisted {
		t.Error("Persisted = true; a pre-persistence timeout must leave disk untouched")
	}
	if submitted.Load() {
		t.Error("SubmitReload was called; an expired-deadline apply must not enqueue a reload")
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read disk: %v", err)
	}
	if string(onDisk) != string(seed) {
		t.Error("on-disk bytes changed; an expired-deadline apply must leave disk untouched")
	}
}
