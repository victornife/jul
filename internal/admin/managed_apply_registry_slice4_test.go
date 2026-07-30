// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"testing"
	"time"
)

// TestBeginPendingAfterTerminalNoDowngrade proves a late or duplicated pending
// registration for an ID that has already reached a terminal result must never
// downgrade that record back to pending (AC-02, §2.9). This closes the failure
// mode where a delayed OnManagedApplyStarted signal (or a retried enqueue) could
// otherwise clobber an authoritative terminal ledger entry and make a completed
// transaction appear un-finished to the ConfigPanel.
func TestBeginPendingAfterTerminalNoDowngrade(t *testing.T) {
	r := NewManagedApplyRegistry(0, 0)
	id := "rl_9f01c0b451d2_1"

	// Drive the record straight to terminal.
	if err := r.Complete(ManagedApplyRecord{
		ID:        id,
		Operation: ApplyOperationConfigApply,
		Result:    ConfigApplyResult{ApplyID: id, OK: true},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if g, ok := r.Get(id); !ok || g.State != ManagedApplyTerminal {
		t.Fatalf("state after complete = %+v ok=%v, want terminal", g, ok)
	}

	// A late pending registration for the same ID must be a no-op, not a
	// downgrade: BeginPending returns nil (idempotent) but the state stays
	// terminal and the terminal result is preserved.
	if err := r.BeginPending(ManagedApplyRecord{ID: id, Operation: ApplyOperationConfigApply}); err != nil {
		t.Fatalf("BeginPending after terminal: %v", err)
	}
	g, ok := r.Get(id)
	if !ok {
		t.Fatal("record disappeared after late BeginPending")
	}
	if g.State != ManagedApplyTerminal {
		t.Errorf("state = %q, want terminal (a terminal record must never be downgraded to pending)", g.State)
	}
	if !g.Result.OK || g.Result.ApplyID != id {
		t.Errorf("terminal result was lost after late BeginPending: %+v", g.Result)
	}
}

// TestCompletePreservesStartAndDeadline proves Complete preserves the start time
// and the absolute transaction deadline recorded on the pending record even when
// the terminal caller does not re-supply them (AC-02/AC-08, §2.9). The deadline
// is the input to deadline-aware polling; dropping it at terminalization would
// blind the ConfigPanel to when the transaction budget expired.
func TestCompletePreservesStartAndDeadline(t *testing.T) {
	r := NewManagedApplyRegistry(0, 0)
	id := "rl_9f01c0b451d2_1"

	started := time.Now().Add(-2 * time.Second).UTC()
	deadline := time.Now().Add(30 * time.Second).UTC()
	if err := r.BeginPending(ManagedApplyRecord{
		ID:        id,
		Operation: ApplyOperationConfigApply,
		StartedAt: started,
		Deadline:  deadline,
	}); err != nil {
		t.Fatalf("BeginPending: %v", err)
	}

	// Terminal caller omits StartedAt and Deadline (they are lifecycle metadata
	// carried on the pending record, not re-derived at completion).
	if err := r.Complete(ManagedApplyRecord{
		ID:        id,
		Operation: ApplyOperationConfigApply,
		Result:    ConfigApplyResult{ApplyID: id, OK: true},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	g, ok := r.Get(id)
	if !ok {
		t.Fatal("record missing after Complete")
	}
	if g.State != ManagedApplyTerminal {
		t.Fatalf("state = %q, want terminal", g.State)
	}
	if !g.StartedAt.Equal(started) {
		t.Errorf("StartedAt = %v, want %v (preserved from pending)", g.StartedAt, started)
	}
	if !g.Deadline.Equal(deadline) {
		t.Errorf("Deadline = %v, want %v (preserved from pending for deadline-aware polling)", g.Deadline, deadline)
	}
	// CompletedAt must be stamped even though the caller left it zero.
	if g.CompletedAt.IsZero() {
		t.Error("CompletedAt was not stamped at terminalization")
	}
}
