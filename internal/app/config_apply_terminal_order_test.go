// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"jul/internal/admin"
	"jul/internal/config"
	"jul/internal/server"
)

// TestTerminalManagedApplyVisibilityReleasesAdmission proves the #226 ordering
// contract without relying on scheduler luck. The completion callback publishes
// the first terminal ledger record and then blocks before returning. While that
// externally visible terminal state is held open, the coordinator admission
// gate and ReloadRequest.Finalized channel must already be released and a second
// apply must reach SubmitReload. The second finalizer remains serialized behind
// the first callback, proving that admission and terminal-side-effect ordering
// are independent rather than mutually contradictory.
func TestTerminalManagedApplyVisibilityReleasesAdmission(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	if err := os.WriteFile(path, validConfigRaw(t, ":8080"), 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	firstRequests := make(chan server.ReloadRequest, 1)
	clock := newSavedNotLiveClock()
	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			firstRequests <- req
			return nil
		},
		LiveSnapshot: func() server.LiveSnapshot {
			cfg := config.ProxyTarget("127.0.0.1:9000", ":8080")
			cfg.Global.ReloadTimeout = config.Duration(savedNotLiveBudget)
			return server.LiveSnapshot{EffectiveConfig: cfg}
		},
		waitMargin:     10 * time.Millisecond,
		PlannedRestart: &PlannedRestartStore{},
		clock:          clock,
	}

	registry := admin.NewManagedApplyRegistry(0, 0)
	c.OnManagedApplyStarted = func(start admin.ManagedApplyStart) error {
		applyID := start.Result.ApplyID
		if applyID == "" && start.Result.Reload != nil {
			applyID = start.Result.Reload.ID
		}
		if applyID == "" {
			return errors.New("managed apply start has no apply id")
		}
		return registry.BeginPending(admin.ManagedApplyRecord{
			ID:           applyID,
			State:        admin.ManagedApplyPending,
			Operation:    start.Context.Operation,
			StartedAt:    start.Context.StartedAt,
			Deadline:     start.Context.Deadline,
			Result:       start.Result,
			OwnerTokenID: start.Context.TokenID,
		})
	}

	terminalVisible := make(chan struct{})
	releaseFirstCompletion := make(chan struct{})
	completed := make(chan string, 2)
	var terminalBarrier sync.Once
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseFirstCompletion) })
	}
	t.Cleanup(release)

	c.OnManagedApplyComplete = func(comp admin.ManagedApplyCompletion) admin.ManagedApplyFinalization {
		res := comp.Result
		applyID := res.ApplyID
		if applyID == "" && res.Reload != nil {
			applyID = res.Reload.ID
		}
		fin := admin.ManagedApplyFinalization{FinalizationError: res.FinalizationError}
		if applyID != "" {
			if err := registry.Complete(admin.ManagedApplyRecord{
				ID:                applyID,
				Operation:         comp.Context.Operation,
				StartedAt:         comp.Context.StartedAt,
				Deadline:          comp.Context.Deadline,
				Result:            res,
				HistorySnapshotID: fin.HistorySnapshotID,
				HistoryError:      fin.HistoryError,
				FinalizationError: fin.FinalizationError,
			}); err != nil {
				fin.FinalizationError = err.Error()
			}
		}
		terminalBarrier.Do(func() {
			close(terminalVisible)
			<-releaseFirstCompletion
		})
		completed <- applyID
		return fin
	}

	startedAt := clock.Now().UTC()
	firstResult := applyRawAwaitingSavedNotLive(t, c, clock, admin.ApplyRequestContext{
		Operation: admin.ApplyOperationConfigApply,
		StartedAt: startedAt,
		Deadline:  startedAt.Add(savedNotLiveBudget),
		TokenID:   "tok-owner-123",
	}, validConfigRaw(t, ":8081"), ApplyHot)
	if firstResult.Reload == nil || firstResult.Reload.Outcome != server.ReloadSavedNotLive {
		t.Fatalf("first result = %+v, want provisional saved_not_live", firstResult.Reload)
	}
	firstID := firstResult.ApplyID
	if firstID == "" {
		t.Fatal("first saved_not_live result carried no apply id")
	}

	firstReq := <-firstRequests
	firstReq.Result <- server.ReloadResult{
		ID:             firstID,
		Source:         server.ReloadSourceAdmin,
		Outcome:        server.ReloadAppliedLive,
		Published:      true,
		ServingVersion: "v2",
	}

	select {
	case <-terminalVisible:
	case <-time.After(5 * time.Second):
		t.Fatal("first terminal record was not published")
	}

	firstRecord, ok := registry.Get(firstID)
	if !ok || firstRecord.State != admin.ManagedApplyTerminal {
		release()
		t.Fatalf("first ledger record = %+v, present=%v; want terminal", firstRecord, ok)
	}

	c.mu.Lock()
	inFlight := c.inFlightState
	c.mu.Unlock()
	if inFlight != ApplyInFlightNone {
		release()
		t.Fatalf("terminal ledger is visible while in-flight state = %q, want none", inFlight)
	}
	select {
	case <-firstReq.Finalized:
		// The server reload loop may accept the next request.
	default:
		release()
		t.Fatal("terminal ledger is visible before ReloadRequest.Finalized is closed")
	}

	secondSubmitted := make(chan server.ReloadRequest, 1)
	c.SubmitReload = func(req server.ReloadRequest) error {
		secondSubmitted <- req
		return nil
	}
	type applyOutcome struct {
		result ApplyResult
		err    error
	}
	secondDone := make(chan applyOutcome, 1)
	go func() {
		secondStarted := clock.Now().UTC()
		res, applyErr := c.ApplyRaw(admin.ApplyRequestContext{
			Operation: admin.ApplyOperationConfigApply,
			StartedAt: secondStarted,
			Deadline:  secondStarted.Add(5 * time.Second),
		}, validConfigRaw(t, ":8082"), ApplyHot)
		secondDone <- applyOutcome{result: res, err: applyErr}
	}()

	var secondReq server.ReloadRequest
	select {
	case secondReq = <-secondSubmitted:
		// Admission reached the reload boundary while the first terminal
		// callback was deliberately still blocked.
	case <-time.After(5 * time.Second):
		release()
		t.Fatal("second apply did not reach SubmitReload after terminal visibility")
	}
	secondReq.Result <- server.ReloadResult{
		ID:             secondReq.ID,
		Source:         server.ReloadSourceAdmin,
		Outcome:        server.ReloadAppliedLive,
		Published:      true,
		ServingVersion: "v3",
	}

	// Wait until the second finalizer has itself released the reload-loop gate.
	// At that exact barrier, the first callback is still blocked and must own
	// finalizeMu, so the second callback and synchronous result cannot complete.
	select {
	case <-secondReq.Finalized:
	case <-time.After(5 * time.Second):
		release()
		t.Fatal("second reload request did not reach terminal handoff")
	}
	if c.finalizeMu.TryLock() {
		c.finalizeMu.Unlock()
		release()
		t.Fatal("first terminal callback did not retain the finalization lock")
	}
	select {
	case id := <-completed:
		release()
		t.Fatalf("terminal callback %q completed before the first finalizer was released", id)
	default:
	}
	select {
	case outcome := <-secondDone:
		release()
		t.Fatalf("second apply completed before serialized terminal finalization: result=%+v err=%v", outcome.result, outcome.err)
	default:
	}

	// Let the first callback return. finalizeMu then allows the second terminal
	// callback to run; admission itself has already been proven independent.
	release()

	var second applyOutcome
	select {
	case second = <-secondDone:
	case <-time.After(5 * time.Second):
		t.Fatal("second apply did not complete")
	}
	if second.err != nil {
		t.Fatalf("second apply: %v", second.err)
	}
	if !second.result.OK {
		t.Fatalf("second apply rejected after terminal visibility: %s", second.result.Message)
	}
	if strings.Contains(second.result.Message, "previous apply is still in flight") {
		t.Fatalf("second apply observed stale admission state: %s", second.result.Message)
	}
	if second.result.ApplyID == "" || second.result.ApplyID == firstID {
		t.Fatalf("second apply id = %q, first = %q", second.result.ApplyID, firstID)
	}

	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case id := <-completed:
			seen[id] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("terminal callbacks observed = %v, want both apply ids", seen)
		}
	}
	if !seen[firstID] || !seen[second.result.ApplyID] {
		t.Fatalf("terminal callbacks observed = %v, want %q and %q", seen, firstID, second.result.ApplyID)
	}
	if rec, ok := registry.Get(second.result.ApplyID); !ok || rec.State != admin.ManagedApplyTerminal {
		t.Fatalf("second ledger record = %+v, present=%v; want terminal", rec, ok)
	}
}
