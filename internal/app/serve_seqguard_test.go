// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"jul/internal/admin"
	"jul/internal/server"
)

// TestCoordinatorPopulatesApplyIDOnAppliedLive verifies the P0-A source fix:
// an ordinary successful managed apply carries a non-empty ApplyID that
// matches the enqueued reload ID, so the production callback's sequence guard
// records the terminal result instead of dropping it as sequence 0 (M-05).
func TestCoordinatorPopulatesApplyIDOnAppliedLive(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	if err := os.WriteFile(path, validConfigRaw(t, ":8080"), 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	var enqueuedID string
	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			enqueuedID = req.ID
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
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: &PlannedRestartStore{},
	}

	res, err := c.ApplyRaw(admin.ApplyRequestContext{}, validConfigRaw(t, ":8081"), ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if !res.OK {
		t.Fatalf("ok = false, want true; message: %s", res.Message)
	}
	if res.ApplyID == "" {
		t.Fatal("ApplyID empty on applied-live result; callback would drop it as seq 0")
	}
	if res.ApplyID != enqueuedID {
		t.Errorf("ApplyID = %q, want enqueued reload ID %q", res.ApplyID, enqueuedID)
	}

	// The guard must accept a result carrying this ApplyID from a fresh mark.
	var hw atomic.Uint64
	if !managedApplySeqGuard(&hw, admin.ConfigApplyResult{ApplyID: res.ApplyID}) {
		t.Fatal("guard should accept the populated ApplyID")
	}
}

// TestManagedApplySeqGuardPrefersApplyID verifies the guard sequences on
// ApplyID and advances the high-water mark for strictly-increasing IDs while
// rejecting stale/out-of-order results (M-05 / C3).
func TestManagedApplySeqGuardPrefersApplyID(t *testing.T) {
	var hw atomic.Uint64

	if !managedApplySeqGuard(&hw, admin.ConfigApplyResult{ApplyID: "rl_1"}) {
		t.Fatal("rl_1 should be accepted from initial state")
	}
	if hw.Load() != 1 {
		t.Fatalf("high-water = %d, want 1", hw.Load())
	}

	// In-order advance.
	if !managedApplySeqGuard(&hw, admin.ConfigApplyResult{ApplyID: "rl_2"}) {
		t.Fatal("rl_2 should be accepted after rl_1")
	}
	if hw.Load() != 2 {
		t.Fatalf("high-water = %d, want 2", hw.Load())
	}

	// Out-of-order (stale) result must be rejected and must not move the mark.
	if managedApplySeqGuard(&hw, admin.ConfigApplyResult{ApplyID: "rl_1"}) {
		t.Fatal("stale rl_1 should be rejected after rl_2")
	}
	if hw.Load() != 2 {
		t.Fatalf("high-water = %d after stale, want 2", hw.Load())
	}

	// Equal sequence is also rejected (exactly-once: a duplicate terminal
	// callback for the same apply must not be recorded twice).
	if managedApplySeqGuard(&hw, admin.ConfigApplyResult{ApplyID: "rl_2"}) {
		t.Fatal("duplicate rl_2 should be rejected")
	}
}

// TestManagedApplySeqGuardFallsBackToReloadID verifies that a result missing
// ApplyID is still sequence-correlated via Reload.ID rather than silently
// dropped as sequence 0 (M-05 defensive fallback).
func TestManagedApplySeqGuardFallsBackToReloadID(t *testing.T) {
	var hw atomic.Uint64

	res := admin.ConfigApplyResult{
		Reload: &server.ReloadResult{ID: "rl_5"},
	}
	if !managedApplySeqGuard(&hw, res) {
		t.Fatal("result with only Reload.ID=rl_5 should be accepted")
	}
	if hw.Load() != 5 {
		t.Fatalf("high-water = %d, want 5", hw.Load())
	}

	// A later result carrying ApplyID must still advance past the fallback mark.
	if !managedApplySeqGuard(&hw, admin.ConfigApplyResult{ApplyID: "rl_6"}) {
		t.Fatal("rl_6 should be accepted after fallback rl_5")
	}
	if hw.Load() != 6 {
		t.Fatalf("high-water = %d, want 6", hw.Load())
	}
}

// TestManagedApplySeqGuardMissingIDsAreZero verifies that results with no
// usable ID parse to sequence 0. The first such result is accepted only when
// the mark is still 0; subsequent zero-sequence results are rejected. This
// documents the boundary that motivates populating ApplyID on every terminal
// result rather than relying on the guard alone.
func TestManagedApplySeqGuardMissingIDsAreZero(t *testing.T) {
	var hw atomic.Uint64

	// From the initial zero mark, a zero-sequence result is rejected because
	// the guard requires strictly-greater sequences (seq <= prev => reject).
	if managedApplySeqGuard(&hw, admin.ConfigApplyResult{}) {
		t.Fatal("empty result (seq 0) should be rejected from zero mark")
	}
	if hw.Load() != 0 {
		t.Fatalf("high-water = %d, want 0", hw.Load())
	}
}

// TestManagedApplySeqGuardSuccessAfterEnqueueFailure reproduces the M-05
// operational scenario: an enqueue failure advances the high-water mark, and a
// subsequent ordinary successful apply with a higher sequence must remain
// observable (not discarded).
func TestManagedApplySeqGuardSuccessAfterEnqueueFailure(t *testing.T) {
	var hw atomic.Uint64

	// Enqueue failure recorded with ApplyID rl_3.
	if !managedApplySeqGuard(&hw, admin.ConfigApplyResult{ApplyID: "rl_3"}) {
		t.Fatal("enqueue-failure rl_3 should be accepted")
	}

	// A later ordinary successful apply (rl_4) must not be dropped.
	if !managedApplySeqGuard(&hw, admin.ConfigApplyResult{
		ApplyID: "rl_4",
		OK:      true,
		Reload:  &server.ReloadResult{ID: "rl_4", Outcome: server.ReloadAppliedLive},
	}) {
		t.Fatal("successful rl_4 after enqueue-failure rl_3 should be accepted")
	}
	if hw.Load() != 4 {
		t.Fatalf("high-water = %d, want 4", hw.Load())
	}
}

// TestManagedApplySeqGuardConcurrentExactlyOnce verifies that under concurrent
// delivery of the same terminal result, exactly one caller observes acceptance
// (exactly-once recording) while all duplicates are rejected.
func TestManagedApplySeqGuardConcurrentExactlyOnce(t *testing.T) {
	var hw atomic.Uint64
	const workers = 32

	var accepted atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if managedApplySeqGuard(&hw, admin.ConfigApplyResult{ApplyID: "rl_7"}) {
				accepted.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if accepted.Load() != 1 {
		t.Fatalf("accepted = %d, want exactly 1", accepted.Load())
	}
	if hw.Load() != 7 {
		t.Fatalf("high-water = %d, want 7", hw.Load())
	}
}
