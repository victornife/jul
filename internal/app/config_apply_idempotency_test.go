// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"crypto/sha256"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"jul/internal/admin"
	"jul/internal/server"
)

func testManagedIdempotency(key, operation string) *admin.ManagedApplyIdempotency {
	return &admin.ManagedApplyIdempotency{
		Key:         key,
		Fingerprint: sha256.Sum256([]byte(key + ":fingerprint")),
		Method:      http.MethodPost,
		Operation:   operation,
		Principal:   "alice",
	}
}

func assertTerminalIdempotency(t *testing.T, registry *admin.ManagedApplyRegistry, key, wantOperation string) admin.ManagedApplyRecord {
	t.Helper()
	rec, ok := registry.FindIdempotency("alice", key)
	if !ok {
		t.Fatalf("no retained idempotency binding for %q", key)
	}
	if rec.State != admin.ManagedApplyTerminal {
		t.Fatalf("record state = %q, want terminal: %+v", rec.State, rec)
	}
	if rec.IdempotencyOperation != wantOperation {
		t.Fatalf("record operation = %q, want %q", rec.IdempotencyOperation, wantOperation)
	}
	if rec.Result.ApplyID != rec.ID {
		t.Fatalf("result apply_id = %q, record id = %q", rec.Result.ApplyID, rec.ID)
	}
	return rec
}

func TestIdempotentHotApplyReservesBeforeConfigurationWrite(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatal(err)
	}

	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			req.Result <- server.ReloadResult{ID: req.ID, Source: server.ReloadSourceAdmin, Outcome: server.ReloadAppliedLive, Published: true, ServingVersion: "v2"}
			return nil
		},
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: &PlannedRestartStore{},
	}
	registry, _, _ := wireProductionLedger(c)
	productionAdmission := c.OnManagedApplyAdmitted
	admitted := false
	c.OnManagedApplyAdmitted = func(admission admin.ManagedApplyAdmission) error {
		onDisk, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read at admission: %v", err)
		}
		if string(onDisk) != string(seed) {
			t.Fatal("configuration changed before idempotency admission")
		}
		admitted = true
		return productionAdmission(admission)
	}

	const key = "hot-apply-key-001"
	res, err := c.ApplyRaw(admin.ApplyRequestContext{
		Operation:   admin.ApplyOperationConfigApply,
		Actor:       "alice",
		TokenID:     "tok-1",
		Idempotency: testManagedIdempotency(key, "/api/v1/config/apply"),
	}, validConfigRaw(t, ":8081"), ApplyHot)
	if err != nil {
		t.Fatalf("ApplyRaw: %v", err)
	}
	if !res.OK || !admitted {
		t.Fatalf("result=%+v admitted=%v", res, admitted)
	}
	assertTerminalIdempotency(t, registry, key, "/api/v1/config/apply")
}

func TestIdempotentStageReservesBeforeRecoverySidecars(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewFilePlannedRestartStore(path)
	c := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		Path:           path,
		Preflight:      testPreflightStage(t, seed),
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: store,
	}
	registry, _, _ := wireProductionLedger(c)
	productionAdmission := c.OnManagedApplyAdmitted
	admitted := false
	c.OnManagedApplyAdmitted = func(admission admin.ManagedApplyAdmission) error {
		if store.IsPending() {
			t.Fatal("planned restart became pending before idempotency admission")
		}
		if _, err := os.Stat(path + ".pending-restart.bak"); !os.IsNotExist(err) {
			t.Fatalf("backup sidecar existed before idempotency admission: %v", err)
		}
		admitted = true
		return productionAdmission(admission)
	}

	const key = "stage-key-000001"
	res, err := c.ApplyRaw(admin.ApplyRequestContext{
		Operation:   admin.ApplyOperationConfigApply,
		Actor:       "alice",
		TokenID:     "tok-1",
		Idempotency: testManagedIdempotency(key, "/api/v1/config/apply"),
	}, restartRequiredConfigRaw(t, ":8080"), ApplyStageRestart)
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if !res.OK || !admitted || !store.IsPending() {
		t.Fatalf("result=%+v admitted=%v pending=%v", res, admitted, store.IsPending())
	}
	assertTerminalIdempotency(t, registry, key, "/api/v1/config/apply")
}

func TestIdempotentAdoptStageReservesBeforeBackup(t *testing.T) {
	seed := validConfigRaw(t, ":8080")
	candidate := validConfigRaw(t, ":9999")
	c, path := newStageRestartAdoptFixture(t, seed, candidate)
	assessment, err := c.AssessAdoptExternal()
	if err != nil {
		t.Fatalf("AssessAdoptExternal: %v", err)
	}

	registry, _, _ := wireProductionLedger(c)
	productionAdmission := c.OnManagedApplyAdmitted
	admitted := false
	c.OnManagedApplyAdmitted = func(admission admin.ManagedApplyAdmission) error {
		if _, err := os.Stat(path + ".pending-restart.bak"); !os.IsNotExist(err) {
			t.Fatalf("adoption backup existed before idempotency admission: %v", err)
		}
		admitted = true
		return productionAdmission(admission)
	}

	const key = "adopt-key-000001"
	res, err := c.AdoptExternal(admin.ApplyRequestContext{
		Operation:   admin.ApplyOperationAdoptExternal,
		Actor:       "alice",
		TokenID:     "tok-1",
		Idempotency: testManagedIdempotency(key, "/api/v1/config/adopt-external"),
	}, admin.AdoptExternalRequest{
		ObservedDigest: assessment.ObservedDigest,
		BaseVersion:    "seed-version",
		Mode:           "stage_restart",
		Confirm:        true,
	})
	if err != nil {
		t.Fatalf("AdoptExternal: %v", err)
	}
	if !res.OK || !admitted {
		t.Fatalf("result=%+v admitted=%v", res, admitted)
	}
	assertTerminalIdempotency(t, registry, key, "/api/v1/config/adopt-external")
}

func TestIdempotentDiscardReservesBeforeRestoreAndRechecksCAS(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewFilePlannedRestartStore(path)
	c := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		Path:           path,
		Preflight:      testPreflightStage(t, seed),
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: store,
	}
	candidate := restartRequiredConfigRaw(t, ":8080")
	stage, err := c.ApplyRaw(admin.ApplyRequestContext{}, candidate, ApplyStageRestart)
	if err != nil || !stage.OK {
		t.Fatalf("seed stage: result=%+v err=%v", stage, err)
	}
	stagedRaw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	registry, _, _ := wireProductionLedger(c)
	productionAdmission := c.OnManagedApplyAdmitted
	admitted := false
	c.OnManagedApplyAdmitted = func(admission admin.ManagedApplyAdmission) error {
		onDisk, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(onDisk) != string(stagedRaw) || !store.IsPending() {
			t.Fatal("discard changed state before idempotency admission")
		}
		admitted = true
		return productionAdmission(admission)
	}

	const key = "discard-key-001"
	res, err := c.DiscardPlannedRestartWithContext(admin.ApplyRequestContext{
		Operation:   admin.ApplyOperationDiscardPending,
		Actor:       "alice",
		TokenID:     "tok-1",
		Baseline:    mutationBaseline(t, stagedRaw),
		Idempotency: testManagedIdempotency(key, "/api/v1/config/pending-restart/discard"),
	})
	if err != nil {
		t.Fatalf("discard: %v", err)
	}
	if !res.OK || !admitted || store.IsPending() {
		t.Fatalf("result=%+v admitted=%v pending=%v", res, admitted, store.IsPending())
	}
	assertTerminalIdempotency(t, registry, key, "/api/v1/config/pending-restart/discard")

	// Stage again, then prove the coordinator's second CAS blocks a stale
	// request before admission, even if the HTTP layer had already read a base.
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatal(err)
	}
	store = NewFilePlannedRestartStore(path)
	c.PlannedRestart = store
	stage, err = c.ApplyRaw(admin.ApplyRequestContext{}, candidate, ApplyStageRestart)
	if err != nil || !stage.OK {
		t.Fatalf("second stage: result=%+v err=%v", stage, err)
	}
	staleBase, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	external := validConfigRaw(t, ":9999")
	if err := os.WriteFile(path, external, 0o600); err != nil {
		t.Fatal(err)
	}
	admitted = false
	stale, err := c.DiscardPlannedRestartWithContext(admin.ApplyRequestContext{
		Operation:   admin.ApplyOperationDiscardPending,
		Baseline:    mutationBaseline(t, staleBase),
		Idempotency: testManagedIdempotency("discard-stale-01", "/api/v1/config/pending-restart/discard"),
	})
	if err != nil {
		t.Fatalf("stale discard returned storage error: %v", err)
	}
	if !stale.Conflict || admitted {
		t.Fatalf("stale result=%+v admitted=%v, want conflict before admission", stale, admitted)
	}
	if _, ok := registry.FindIdempotency("alice", "discard-stale-01"); ok {
		t.Fatal("stale discard retained an idempotency reservation")
	}
}

func TestIdempotentMutationFailsClosedWhenAdmissionLedgerIsUnavailable(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatal(err)
	}
	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(server.ReloadRequest) error {
			t.Fatal("reload submitted without an idempotency ledger")
			return nil
		},
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: &PlannedRestartStore{},
	}
	res, err := c.ApplyRaw(admin.ApplyRequestContext{
		Operation:   admin.ApplyOperationConfigApply,
		Idempotency: testManagedIdempotency("missing-ledger-01", "/api/v1/config/apply"),
	}, validConfigRaw(t, ":8081"), ApplyHot)
	if err == nil || res.OK {
		t.Fatalf("result=%+v err=%v, want fail-closed admission error", res, err)
	}
	onDisk, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(onDisk) != string(seed) {
		t.Fatal("configuration changed despite idempotency admission failure")
	}
}
