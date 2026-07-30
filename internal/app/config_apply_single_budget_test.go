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

// TestManagedApplyEnforcesSingleReloadTimeoutBudget is the end-to-end proof for
// AC-08/R15-01: preflight and the reload wait share ONE absolute deadline bound
// at admission. If the reload wait were granted a fresh full timeout after
// preflight (the reset defect), the transaction would run for
// preflight_consumed + reload_timeout; with a single budget it must terminate at
// the original deadline regardless of how much of the budget preflight already
// spent.
//
// Scaling: the runbook example uses a 150ms serving timeout with ~100ms spent in
// preflight. The wall-clock window is widened here (200ms budget, ~150ms
// preflight) so the one-budget return (~budget+margin) and the reset return
// (~preflight+budget+margin) sit far enough apart to assert without flakiness.
// The return is anchored to the ABSOLUTE deadline computed at admission, so the
// low side is stable; only scheduling jitter can push it up, hence the generous
// upper bound.
func TestManagedApplyEnforcesSingleReloadTimeoutBudget(t *testing.T) {
	const (
		servingTimeout  = 200 * time.Millisecond
		preflightSpend  = 150 * time.Millisecond
		waitMargin      = 15 * time.Millisecond
		singleBudgetMax = 300 * time.Millisecond // < reset (~365ms), well above one-budget (~215ms)
		singleBudgetMin = 150 * time.Millisecond // proves the wait ran ~the budget, not just preflight
	)

	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	// The reload never delivers a terminal result within the transaction budget
	// (it "tries to consume" more time than remains). stopReload lets the
	// finalizer goroutine complete cleanly once the timing has been measured.
	stopReload := make(chan struct{})
	c := &ConfigApplyCoordinator{
		BaseCtx: context.Background(),
		Path:    path,
		Preflight: &Preflight{
			// A slow-but-successful preflight: it spends preflightSpend of the
			// shared budget and then succeeds, so the reload wait must live on
			// whatever budget remains — not a fresh full timeout.
			BuildHandlers: func(ctx context.Context, _ *config.Config, _ bool) (map[string]http.Handler, func(), error) {
				select {
				case <-time.After(preflightSpend):
					return map[string]http.Handler{}, nil, nil
				case <-ctx.Done():
					return nil, nil, ctx.Err()
				}
			},
			Stream: &mockStreamPreflighter{},
		},
		SubmitReload: func(req server.ReloadRequest) error {
			go func() {
				<-stopReload
				req.Result <- server.ReloadResult{
					ID:      req.ID,
					Source:  server.ReloadSourceAdmin,
					Outcome: server.ReloadAppliedLive,
				}
			}()
			return nil
		},
		LiveSnapshot:   servingSnapshot(t, servingTimeout),
		PlannedRestart: &PlannedRestartStore{},
		waitMargin:     waitMargin,
	}

	// Mirror HTTP admission: bind StartedAt and the ONE absolute deadline from
	// the serving reload_timeout.
	started := time.Now().UTC()
	reqCtx := admin.ApplyRequestContext{
		StartedAt:      started,
		Deadline:       started.Add(servingTimeout),
		RequestContext: context.Background(),
	}

	begin := time.Now()
	res, err := c.ApplyRaw(reqCtx, validConfigRaw(t, ":8081"), ApplyHot)
	elapsed := time.Since(begin)
	close(stopReload) // release the finalizer goroutine

	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if elapsed > singleBudgetMax {
		t.Fatalf("transaction took %v; a single absolute budget must terminate near %v, not preflight+budget (~%v). The reload wait was granted a second timeout.", elapsed, servingTimeout, preflightSpend+servingTimeout)
	}
	if elapsed < singleBudgetMin {
		t.Fatalf("transaction took %v; expected the reload wait to consume roughly the remaining budget (~%v total)", elapsed, servingTimeout)
	}
	// The transaction budget elapsed while the reload was still in flight: the
	// candidate is persisted and the synchronous result is a post-persistence
	// timeout, not a fresh success.
	if !res.Persisted {
		t.Error("Persisted = false; the candidate was written before the reload wait timed out")
	}
	if res.Reload == nil || !res.Reload.TimedOut {
		t.Fatalf("reload = %+v; want a timed-out provisional result once the single budget elapsed", res.Reload)
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
