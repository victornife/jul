// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"jul/internal/admin"
	"jul/internal/config"
	"jul/internal/server"
)

func waitForFakeTimerCount(t *testing.T, clock *fakeClock, count int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !clock.WaitForTimerCount(ctx, count) {
		t.Fatalf("fake clock did not register %d timers before the test deadline", count)
	}
}

// savedNotLiveBudget is the transaction budget used by the managed-apply tests
// that must reach the provisional saved_not_live path. It is spent by advancing
// an injected fakeClock, never by real elapsed time, so preflight, persistence
// and reload submission may take as long as the host needs (#228).
const savedNotLiveBudget = 30 * time.Millisecond

// newSavedNotLiveClock returns the fake clock those tests inject. Fake time
// starts at the Unix epoch so a test that forgets to derive its StartedAt and
// Deadline from the clock fails loudly instead of flaking.
func newSavedNotLiveClock() *fakeClock { return newFakeClock(time.Unix(0, 0).UTC()) }

// timerCount reports how many timers the clock has registered so far. The
// registry is append-only, so a test that drives several transactions waits on
// a count relative to this reading rather than an absolute one.
func (f *fakeClock) timerCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.timers)
}

// applyRawAwaitingSavedNotLive runs ApplyRaw on its own goroutine and advances
// clock past the transaction deadline once the coordinator has installed its
// synchronous reload-wait timer, so the provisional saved_not_live result is
// produced deterministically instead of racing real preflight work against a
// wall-clock budget (#228).
//
// Exactly two coordinator timers precede the return: the preflight transaction
// deadline, and the reload wait installed after the candidate is persisted and
// the reload is enqueued. Waiting for the second one means the advance can never
// run ahead of the timer it is meant to fire.
func applyRawAwaitingSavedNotLive(t *testing.T, c *ConfigApplyCoordinator, clock *fakeClock, reqCtx admin.ApplyRequestContext, raw []byte, mode ApplyMode) ApplyResult {
	t.Helper()
	type outcome struct {
		res ApplyResult
		err error
	}
	done := make(chan outcome, 1)
	before := clock.timerCount()
	go func() {
		res, err := c.ApplyRaw(reqCtx, raw, mode)
		done <- outcome{res: res, err: err}
	}()

	waitForFakeTimerCount(t, clock, before+2)
	deadline := reqCtx.Deadline
	if deadline.IsZero() {
		// No admission deadline: the coordinator derived one from the serving
		// reload_timeout starting at the transaction's clock reading.
		deadline = clock.Now().Add(savedNotLiveBudget)
	}
	clock.Advance(deadline.Sub(clock.Now()) + c.waitMargin + time.Millisecond)

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("apply error: %v", got.err)
		}
		return got.res
	case <-time.After(10 * time.Second):
		t.Fatal("ApplyRaw did not return after the fake transaction deadline elapsed")
		return ApplyResult{}
	}
}

// TestManagedApplyEnforcesSingleReloadTimeoutBudget is the deterministic proof
// for AC-08/R15-01: preflight and the reload wait share ONE absolute deadline
// bound at admission. A fake clock drives the transaction so the test does not
// depend on host scheduler latency. If the reload wait were granted a fresh full
// timeout after preflight (the reset defect), the reload request would carry a
// deadline of admission + preflight + reload_timeout instead of the original
// admission deadline.
func TestManagedApplyEnforcesSingleReloadTimeoutBudget(t *testing.T) {
	const (
		servingTimeout = 200 * time.Millisecond
		preflightSpend = 150 * time.Millisecond
		waitMargin     = 15 * time.Millisecond
	)

	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	enteredPreflight := make(chan struct{})
	preflightProceed := make(chan struct{})
	submittedReload := make(chan server.ReloadRequest, 1)
	done := make(chan ApplyResult, 1)

	clock := newFakeClock(time.Unix(0, 0).UTC())
	c := &ConfigApplyCoordinator{
		BaseCtx: context.Background(),
		Path:    path,
		Preflight: &Preflight{
			// A slow-but-successful preflight: it spends preflightSpend of the
			// shared budget and then succeeds, so the reload wait must live on
			// whatever budget remains — not a fresh full timeout.
			BuildHandlers: func(ctx context.Context, _ *config.Config, _ bool) (map[string]http.Handler, func(), error) {
				close(enteredPreflight)
				select {
				case <-preflightProceed:
					return map[string]http.Handler{}, nil, nil
				case <-ctx.Done():
					return nil, nil, ctx.Err()
				}
			},
			Stream: &mockStreamPreflighter{},
		},
		SubmitReload: func(req server.ReloadRequest) error {
			submittedReload <- req
			return nil
		},
		LiveSnapshot:   servingSnapshot(t, servingTimeout),
		PlannedRestart: &PlannedRestartStore{},
		waitMargin:     waitMargin,
		clock:          clock,
	}

	startedAt := clock.Now().UTC()
	admissionDeadline := startedAt.Add(servingTimeout)
	reqCtx := admin.ApplyRequestContext{
		StartedAt:      startedAt,
		Deadline:       admissionDeadline,
		RequestContext: context.Background(),
	}

	go func() {
		res, err := c.ApplyRaw(reqCtx, validConfigRaw(t, ":8081"), ApplyHot)
		if err != nil {
			t.Errorf("apply error: %v", err)
		}
		done <- res
	}()

	<-enteredPreflight
	clock.Advance(preflightSpend)
	close(preflightProceed)

	var req server.ReloadRequest
	select {
	case req = <-submittedReload:
	case <-time.After(2 * time.Second):
		t.Fatal("reload was not submitted")
	}

	// The reload request must inherit the admission deadline verbatim. A reset
	// would move the deadline forward by at least preflightSpend.
	if !req.Deadline.Equal(admissionDeadline) {
		t.Fatalf("reload deadline = %v, want admission deadline %v (deadline was reset after preflight)", req.Deadline, admissionDeadline)
	}

	// SubmitReload happens before the synchronous reload-wait timer is
	// installed. Wait for that second timer explicitly so Advance cannot race
	// ahead of timer registration on a slower scheduler.
	waitForFakeTimerCount(t, clock, 2)

	remaining := admissionDeadline.Sub(clock.Now())
	if remaining <= 0 {
		t.Fatalf("preflight consumed the entire budget; remaining = %v", remaining)
	}
	// Advance past the remaining budget plus the wait margin to trigger the
	// synchronous saved_not_live timeout deterministically.
	clock.Advance(remaining + waitMargin + 1*time.Millisecond)

	var res ApplyResult
	select {
	case res = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ApplyRaw did not return after the deadline expired")
	}

	if !res.Persisted {
		t.Error("Persisted = false; the candidate was written before the reload wait timed out")
	}
	if res.Reload == nil || !res.Reload.TimedOut {
		t.Fatalf("reload = %+v; want a timed-out provisional result once the single budget elapsed", res.Reload)
	}
	if res.Reload.Outcome != server.ReloadSavedNotLive {
		t.Fatalf("reload outcome = %v, want %v", res.Reload.Outcome, server.ReloadSavedNotLive)
	}

	// Release the finalizer goroutine so the test does not leak it.
	req.Result <- server.ReloadResult{
		ID:        req.ID,
		Source:    server.ReloadSourceAdmin,
		Outcome:   server.ReloadAppliedLive,
		Published: true,
	}
}

// TestReloadDeadlineBoundsPostPublishFinalizeWait verifies that a post-Publish
// result arriving after the synchronous wait has already timed out still
// terminalizes cleanly and retains the candidate file. The test advances a fake
// clock explicitly so the bounded wait and finalization are deterministic.
func TestReloadDeadlineBoundsPostPublishFinalizeWait(t *testing.T) {
	const (
		servingTimeout = 200 * time.Millisecond
		waitMargin     = 15 * time.Millisecond
	)

	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	submittedReload := make(chan server.ReloadRequest, 1)
	finalized := make(chan struct{})
	done := make(chan ApplyResult, 1)

	clock := newFakeClock(time.Unix(0, 0).UTC())
	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			submittedReload <- req
			return nil
		},
		LiveSnapshot:   servingSnapshot(t, servingTimeout),
		PlannedRestart: &PlannedRestartStore{},
		waitMargin:     waitMargin,
		clock:          clock,
		OnManagedApplyComplete: func(admin.ManagedApplyCompletion) admin.ManagedApplyFinalization {
			close(finalized)
			return admin.ManagedApplyFinalization{}
		},
	}

	startedAt := clock.Now().UTC()
	admissionDeadline := startedAt.Add(servingTimeout)
	go func() {
		res, err := c.ApplyRaw(admin.ApplyRequestContext{
			StartedAt:      startedAt,
			Deadline:       admissionDeadline,
			RequestContext: context.Background(),
		}, validConfigRaw(t, ":8081"), ApplyHot)
		if err != nil {
			t.Errorf("apply error: %v", err)
		}
		done <- res
	}()

	var req server.ReloadRequest
	select {
	case req = <-submittedReload:
	case <-time.After(2 * time.Second):
		t.Fatal("reload was not submitted")
	}
	if !req.Deadline.Equal(admissionDeadline) {
		t.Fatalf("reload deadline = %v, want admission deadline %v", req.Deadline, admissionDeadline)
	}

	// SubmitReload happens before the synchronous reload-wait timer is
	// installed. Wait for that second timer explicitly so Advance cannot race
	// ahead of timer registration on a slower scheduler.
	waitForFakeTimerCount(t, clock, 2)

	// Move past the admission deadline so the synchronous wait returns the
	// provisional saved_not_live result.
	clock.Advance(servingTimeout + waitMargin + 1*time.Millisecond)

	var res ApplyResult
	select {
	case res = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ApplyRaw did not return after the deadline expired")
	}
	if !res.Persisted {
		t.Error("Persisted = false; the candidate was written before the reload wait timed out")
	}
	if res.Reload == nil || !res.Reload.TimedOut || res.Reload.Outcome != server.ReloadSavedNotLive {
		t.Fatalf("provisional result = %+v; want saved_not_live timed-out result", res.Reload)
	}

	// The post-Publish result arrives late. It must still terminalize and must
	// not attempt restoration, so the candidate file stays on disk.
	req.Result <- server.ReloadResult{
		ID:             req.ID,
		Source:         server.ReloadSourceAdmin,
		Outcome:        server.ReloadAppliedDegraded,
		Published:      true,
		TimedOut:       true,
		ServingVersion: "v-degraded",
		Error:          "post-publish timeout",
	}

	select {
	case <-finalized:
	case <-time.After(2 * time.Second):
		t.Fatal("post-publish finalizer did not complete")
	}

	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read disk: %v", err)
	}
	if string(onDisk) != string(validConfigRaw(t, ":8081")) {
		t.Error("post-Publish timeout must retain the candidate file")
	}
}

// TestManagedApplyClientCancelBeforePersistLeavesDiskUntouched verifies §4.14:
// a client cancellation that arrives during pre-persistence work aborts the
// transaction before any disk write and never enqueues a reload. Cancellation
// propagates through reqCtx.RequestContext into the bounded preflight context.
func TestManagedApplyClientCancelBeforePersistLeavesDiskUntouched(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	entered := make(chan struct{})
	var submitted bool
	c := &ConfigApplyCoordinator{
		BaseCtx: context.Background(),
		Path:    path,
		Preflight: &Preflight{
			// Signal entry, then block until the (client) context is cancelled,
			// simulating a wedged pre-persistence gate.
			BuildHandlers: func(ctx context.Context, _ *config.Config, _ bool) (map[string]http.Handler, func(), error) {
				close(entered)
				<-ctx.Done()
				return nil, nil, ctx.Err()
			},
			Stream: &mockStreamPreflighter{},
		},
		SubmitReload:   func(server.ReloadRequest) error { submitted = true; return nil },
		LiveSnapshot:   servingSnapshot(t, 10*time.Second),
		PlannedRestart: &PlannedRestartStore{},
	}

	reqCtx, cancel := context.WithCancel(context.Background())
	done := make(chan ApplyResult, 1)
	go func() {
		res, err := c.ApplyRaw(admin.ApplyRequestContext{
			StartedAt:      time.Now().UTC(),
			RequestContext: reqCtx,
		}, validConfigRaw(t, ":8081"), ApplyHot)
		if err != nil {
			t.Errorf("apply error: %v", err)
		}
		done <- res
	}()

	<-entered
	cancel() // client goes away during preflight, before persistence

	select {
	case res := <-done:
		if res.OK {
			t.Fatalf("ok = true, want false: a pre-persistence client cancel must abort")
		}
		if res.TimedOutPhase == "" {
			t.Error("timed_out_phase empty; a cancelled pre-persistence gate must be attributed to a phase")
		}
		if res.Persisted {
			t.Error("Persisted = true; a pre-persistence cancel must leave disk untouched")
		}
		if submitted {
			t.Error("SubmitReload was called; a pre-persistence cancel must not enqueue a reload")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("apply did not observe the client cancellation before persistence")
	}

	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read disk: %v", err)
	}
	if string(onDisk) != string(seed) {
		t.Error("on-disk bytes changed; a pre-persistence cancel must leave disk untouched")
	}
}

// TestManagedApplyClientCancelAfterPersistStillTerminalizes verifies §4.14: once
// the candidate is persisted and the reload is enqueued, a client cancellation
// cannot abandon the reload/restoration. The finalizer owns the process context
// (c.BaseCtx), so the transaction still reaches a terminal, correlated result
// even though the browser request was cancelled mid-reload.
func TestManagedApplyClientCancelAfterPersistStillTerminalizes(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	persisted := make(chan struct{})
	proceed := make(chan struct{})
	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			close(persisted) // reload enqueued == past persistence
			go func() {
				<-proceed // let the client cancel land first
				req.Result <- server.ReloadResult{
					ID:             req.ID,
					Source:         server.ReloadSourceAdmin,
					Outcome:        server.ReloadAppliedLive,
					Published:      true,
					ServingVersion: server.CanonicalVersion(mustParse(t, validConfigRaw(t, ":8081"))),
				}
			}()
			return nil
		},
		LiveSnapshot:   servingSnapshot(t, 10*time.Second),
		PlannedRestart: &PlannedRestartStore{},
	}

	reqCtx, cancel := context.WithCancel(context.Background())
	newRaw := validConfigRaw(t, ":8081")
	done := make(chan ApplyResult, 1)
	go func() {
		res, err := c.ApplyRaw(admin.ApplyRequestContext{
			StartedAt:      time.Now().UTC(),
			RequestContext: reqCtx,
		}, newRaw, ApplyHot)
		if err != nil {
			t.Errorf("apply error: %v", err)
		}
		done <- res
	}()

	<-persisted
	cancel()       // client disconnects AFTER persistence, mid-reload
	close(proceed) // reload now delivers its terminal result

	select {
	case res := <-done:
		if !res.OK {
			t.Fatalf("ok = false, want true: a post-persistence client cancel must not abandon the reload: %+v", res)
		}
		if !res.Persisted {
			t.Error("Persisted = false; the candidate was written before the cancel")
		}
		if res.Reload == nil || res.Reload.Outcome != server.ReloadAppliedLive {
			t.Fatalf("reload = %+v; want a terminalized applied_live result despite the client cancel", res.Reload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("apply did not terminalize after a post-persistence client cancel; restoration was abandoned")
	}

	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read disk: %v", err)
	}
	if string(onDisk) != string(newRaw) {
		t.Error("on-disk bytes are not the candidate; a post-persistence cancel must keep the persisted write")
	}
}
