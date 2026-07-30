// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jul/internal/admin"
	"jul/internal/config"
	"jul/internal/server"
)

// wireProductionLedger wires an admin.ManagedApplyRegistry to the coordinator's
// OnManagedApplyStarted / OnManagedApplyComplete hooks using the EXACT field
// mapping serve.go installs in production. It returns the registry plus two
// signal channels so a test can observe the pending registration and the
// terminal finalization without manually seeding any ledger state. Keeping the
// mapping identical to serve.go means these tests exercise the real
// composition-root wiring, not a bespoke shim.
func wireProductionLedger(c *ConfigApplyCoordinator) (*admin.ManagedApplyRegistry, <-chan admin.ManagedApplyStart, <-chan admin.ManagedApplyFinalization) {
	registry := admin.NewManagedApplyRegistry(0, 0)
	startedCh := make(chan admin.ManagedApplyStart, 4)
	completedCh := make(chan admin.ManagedApplyFinalization, 4)

	c.OnManagedApplyStarted = func(start admin.ManagedApplyStart) error {
		startedCh <- start
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

	c.OnManagedApplyComplete = func(comp admin.ManagedApplyCompletion) admin.ManagedApplyFinalization {
		res := comp.Result
		ctx := comp.Context
		fin := admin.ManagedApplyFinalization{FinalizationError: res.FinalizationError}
		applyID := res.ApplyID
		if applyID == "" && res.Reload != nil {
			applyID = res.Reload.ID
		}
		if applyID != "" {
			_ = registry.Complete(admin.ManagedApplyRecord{
				ID:                applyID,
				Operation:         ctx.Operation,
				StartedAt:         ctx.StartedAt,
				Result:            res,
				HistorySnapshotID: fin.HistorySnapshotID,
				HistoryError:      fin.HistoryError,
				FinalizationError: fin.FinalizationError,
			})
		}
		completedCh <- fin
		return fin
	}

	return registry, startedCh, completedCh
}

// TestApplyRegistersPendingRecordBeforeSavedNotLive proves the slice's core
// invariant (AC-02): the moment a managed apply persists its candidate and
// enqueues the reload, an exact-ID pending record exists in the ledger — so a
// real 202 saved_not_live can never be immediately followed by a 404. It drives
// the coordinator through the saved_not_live path (the reload result is withheld
// so the synchronous wait returns the provisional 202) and then queries the
// production-wired ledger by the exact returned ApplyID. It also proves the
// terminal finalizer updates the same record in place and that it remains
// queryable after a later, unrelated transaction.
func TestApplyRegistersPendingRecordBeforeSavedNotLive(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	reqCh := make(chan server.ReloadRequest, 1)
	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			// Capture the request but withhold the terminal result so the
			// synchronous ApplyRaw returns the provisional saved_not_live 202.
			reqCh <- req
			return nil
		},
		LiveSnapshot: func() server.LiveSnapshot {
			cfg := config.ProxyTarget("127.0.0.1:9000", ":8080")
			cfg.Global.ReloadTimeout = config.Duration(30 * time.Millisecond)
			return server.LiveSnapshot{EffectiveConfig: cfg}
		},
		waitMargin:     10 * time.Millisecond,
		PlannedRestart: &PlannedRestartStore{},
	}
	registry, startedCh, completedCh := wireProductionLedger(c)

	deadline := time.Now().Add(time.Minute).UTC()
	reqCtx := admin.ApplyRequestContext{
		Operation: admin.ApplyOperationConfigApply,
		StartedAt: time.Now().UTC(),
		Deadline:  deadline,
		TokenID:   "tok-owner-123",
	}
	res, err := c.ApplyRaw(reqCtx, validConfigRaw(t, ":8081"), ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if res.Reload == nil || res.Reload.Outcome != server.ReloadSavedNotLive {
		t.Fatalf("result = %+v, want provisional saved_not_live", res.Reload)
	}
	applyID := res.ApplyID
	if applyID == "" {
		t.Fatal("saved_not_live result carried no apply id")
	}

	// The pending-registration hook must have fired before the 202 returned.
	select {
	case <-startedCh:
	default:
		t.Fatal("OnManagedApplyStarted was not called before the 202 returned")
	}

	// The exact-ID record must already be present and pending: a real 202 is
	// never followed by a 404. This is the defect this slice fixes.
	rec, ok := registry.Get(applyID)
	if !ok {
		t.Fatalf("no ledger record for %q; a 202 saved_not_live could be followed by a 404", applyID)
	}
	if rec.State != admin.ManagedApplyPending {
		t.Errorf("state = %q, want pending", rec.State)
	}
	if rec.Operation != admin.ApplyOperationConfigApply {
		t.Errorf("operation = %q, want %q", rec.Operation, admin.ApplyOperationConfigApply)
	}
	if !rec.Deadline.Equal(deadline) {
		t.Errorf("deadline = %v, want %v (deadline-aware polling)", rec.Deadline, deadline)
	}
	if rec.OwnerTokenID != "tok-owner-123" {
		t.Errorf("owner token id = %q, want tok-owner-123", rec.OwnerTokenID)
	}
	if rec.Result.ApplyID != applyID {
		t.Errorf("pending result apply id = %q, want %q", rec.Result.ApplyID, applyID)
	}

	// Deliver the terminal reload result and confirm the finalizer updates the
	// SAME record in place (pending → terminal), never dropping it.
	req := <-reqCh
	req.Result <- server.ReloadResult{
		ID:             applyID,
		Source:         server.ReloadSourceAdmin,
		Outcome:        server.ReloadAppliedLive,
		Published:      true,
		ServingVersion: "v2",
	}
	select {
	case fin := <-completedCh:
		if fin.FinalizationError != "" {
			t.Fatalf("unexpected finalization error: %q", fin.FinalizationError)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("terminal finalization callback was not called")
	}

	term, ok := registry.Get(applyID)
	if !ok {
		t.Fatal("record disappeared after finalization")
	}
	if term.State != admin.ManagedApplyTerminal {
		t.Errorf("state = %q, want terminal after finalization", term.State)
	}
	if !term.Result.OK {
		t.Errorf("terminal result ok = false, want true")
	}
	// Owner token and deadline set at pending time are preserved through the
	// terminal transition (they are private/lifecycle metadata the terminal
	// caller does not re-supply).
	if term.OwnerTokenID != "tok-owner-123" {
		t.Errorf("terminal owner token id = %q, want tok-owner-123", term.OwnerTokenID)
	}

	// A later, unrelated transaction must not evict or shadow the first record:
	// it remains retrievable by its exact ID (AC-02).
	seedNow, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read disk: %v", err)
	}
	c.SubmitReload = func(req server.ReloadRequest) error {
		go func() {
			req.Result <- server.ReloadResult{
				ID:             req.ID,
				Source:         server.ReloadSourceAdmin,
				Outcome:        server.ReloadAppliedLive,
				Published:      true,
				ServingVersion: "v3",
			}
		}()
		return nil
	}
	res2, err := c.ApplyRaw(admin.ApplyRequestContext{
		Operation: admin.ApplyOperationConfigApply,
		StartedAt: time.Now().UTC(),
	}, validConfigRaw(t, ":8082"), ApplyHot)
	if err != nil {
		t.Fatalf("second apply error: %v", err)
	}
	select {
	case <-completedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("second apply did not finalize")
	}
	if res2.ApplyID == applyID {
		t.Fatal("second apply reused the first transaction id")
	}
	if _, ok := registry.Get(applyID); !ok {
		t.Fatal("first record was evicted by a later transaction; it must stay queryable by exact ID")
	}
	_ = seedNow
}

// TestApplyStartedErrorSurfacesInFinalization proves the failure protocol: when
// pending-record registration fails AFTER persistence, the coordinator does NOT
// roll back the accepted apply and does NOT report the apply itself as failed;
// instead it carries the tracking error into terminal finalization
// (ManagedApplyFinalization.FinalizationError) so it is visible through the
// ledger/overview rather than silently dropped.
func TestApplyStartedErrorSurfacesInFinalization(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	finCh := make(chan admin.ManagedApplyFinalization, 1)
	newRaw := validConfigRaw(t, ":8081")

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
		OnManagedApplyStarted: func(admin.ManagedApplyStart) error {
			return errors.New("boom")
		},
		OnManagedApplyComplete: func(comp admin.ManagedApplyCompletion) admin.ManagedApplyFinalization {
			fin := admin.ManagedApplyFinalization{FinalizationError: comp.Result.FinalizationError}
			finCh <- fin
			return fin
		},
	}

	res, err := c.ApplyRaw(admin.ApplyRequestContext{Operation: admin.ApplyOperationConfigApply}, newRaw, ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	// The apply itself succeeded live: a tracking failure must not masquerade
	// as an apply failure or roll back the accepted reload.
	if !res.OK {
		t.Fatalf("ok = false, want true; the tracking failure must not fail the apply: %s", res.Message)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read disk: %v", err)
	}
	if string(onDisk) != string(newRaw) {
		t.Fatal("candidate was rolled back after a pending-registration failure; the accepted apply must stand")
	}

	select {
	case fin := <-finCh:
		if !strings.Contains(fin.FinalizationError, "register managed apply pending record") {
			t.Errorf("finalization error = %q, want it to carry the pending-registration failure", fin.FinalizationError)
		}
		if !strings.Contains(fin.FinalizationError, "boom") {
			t.Errorf("finalization error = %q, want it to wrap the underlying error", fin.FinalizationError)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("terminal finalization callback was not called")
	}
}
