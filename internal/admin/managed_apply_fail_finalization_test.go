// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"errors"
	"testing"
	"time"
)

// TestFailFinalizationPreservesRecordInPlace proves the finalization-panic
// fallback modifies the established transaction IN PLACE: it preserves the
// operation, the complete apply result (mode, apply id, persisted version), and
// the private owner metadata, changing only state/completion/finalization error.
// The incomplete emergency record (ID + completion + error) must never zero the
// valid result the pending record already carried.
func TestFailFinalizationPreservesRecordInPlace(t *testing.T) {
	r := NewManagedApplyRegistry(0, 0)
	started := time.Now().Add(-2 * time.Second).UTC()
	deadline := started.Add(30 * time.Second)
	completed := time.Now().UTC()

	pending := ManagedApplyRecord{
		ID:        "rl_9f01c0b451d2_1",
		Operation: ApplyOperationRollback,
		StartedAt: started,
		Deadline:  deadline,
		Result: ConfigApplyResult{
			ApplyID:          "rl_9f01c0b451d2_1",
			OK:               true,
			Mode:             "hot",
			Persisted:        true,
			PersistedVersion: "disk-v2",
			ServingVersion:   "runtime-v1",
		},
		OwnerTokenID: "token-1",
	}
	if err := r.BeginPending(pending); err != nil {
		t.Fatalf("BeginPending: %v", err)
	}

	if err := r.FailFinalization(ManagedApplyRecord{
		ID:                pending.ID,
		CompletedAt:       completed,
		FinalizationError: "callback panic",
	}); err != nil {
		t.Fatalf("FailFinalization: %v", err)
	}

	got, ok := r.Get(pending.ID)
	if !ok {
		t.Fatal("record missing after FailFinalization")
	}
	if got.State != ManagedApplyTerminal {
		t.Errorf("state = %q, want terminal", got.State)
	}
	if got.Operation != ApplyOperationRollback {
		t.Errorf("operation = %q, want rollback (identity must be preserved)", got.Operation)
	}
	if got.Result.Mode != "hot" {
		t.Errorf("result mode = %q, want hot", got.Result.Mode)
	}
	if got.Result.ApplyID != pending.ID {
		t.Errorf("result apply id = %q, want %q", got.Result.ApplyID, pending.ID)
	}
	if got.Result.PersistedVersion != "disk-v2" {
		t.Errorf("persisted version = %q, want disk-v2 (result must not be zeroed)", got.Result.PersistedVersion)
	}
	if got.OwnerTokenID != "token-1" {
		t.Errorf("owner token id = %q, want token-1 (private ownership preserved)", got.OwnerTokenID)
	}
	if got.FinalizationError == "" {
		t.Error("finalization error was lost")
	}
	if n := r.TerminalCount(); n != 1 {
		t.Errorf("terminal count = %d, want 1 (terminal order must be idempotent)", n)
	}
}

// TestFailFinalizationMissingRecord proves the missing-record contract: a
// complete original terminal result with no prior record still succeeds, while a
// bare ID+error fallback that cannot be reconstructed is rejected with
// ErrManagedApplyRecordIncomplete and never published.
func TestFailFinalizationMissingRecord(t *testing.T) {
	r := NewManagedApplyRegistry(0, 0)

	if err := r.FailFinalization(ManagedApplyRecord{
		ID:        "rl_9f01c0b451d2_2",
		Operation: ApplyOperationConfigApply,
		Result: ConfigApplyResult{
			ApplyID: "rl_9f01c0b451d2_2",
			Mode:    "hot",
			OK:      true,
		},
		FinalizationError: "callback panic",
	}); err != nil {
		t.Fatalf("FailFinalization with complete result: %v", err)
	}
	if got, ok := r.Get("rl_9f01c0b451d2_2"); !ok || got.State != ManagedApplyTerminal {
		t.Fatalf("state = %+v ok=%v, want terminal", got, ok)
	}

	err := r.FailFinalization(ManagedApplyRecord{
		ID:                "rl_9f01c0b451d2_3",
		FinalizationError: "callback panic",
	})
	if !errors.Is(err, ErrManagedApplyRecordIncomplete) {
		t.Fatalf("err = %v, want ErrManagedApplyRecordIncomplete", err)
	}
	if _, ok := r.Get("rl_9f01c0b451d2_3"); ok {
		t.Error("incomplete fallback must not be published")
	}
}

// TestFailFinalizationJoinsPriorError proves a prior finalization error already
// recorded on the pending/finalizing record is preserved alongside the new one
// rather than being overwritten.
func TestFailFinalizationJoinsPriorError(t *testing.T) {
	r := NewManagedApplyRegistry(0, 0)
	id := "rl_9f01c0b451d2_4"
	if err := r.BeginPending(ManagedApplyRecord{
		ID:                id,
		Operation:         ApplyOperationConfigApply,
		Result:            ConfigApplyResult{ApplyID: id, Mode: "hot", OK: true},
		FinalizationError: "history degraded",
	}); err != nil {
		t.Fatalf("BeginPending: %v", err)
	}
	if err := r.FailFinalization(ManagedApplyRecord{
		ID:                id,
		FinalizationError: "callback panic",
	}); err != nil {
		t.Fatalf("FailFinalization: %v", err)
	}
	got, _ := r.Get(id)
	if got.FinalizationError != "history degraded; callback panic" {
		t.Errorf("finalization error = %q, want both errors joined", got.FinalizationError)
	}
}
