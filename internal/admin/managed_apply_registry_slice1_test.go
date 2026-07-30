// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestBootScopedAndLegacyIDsValid proves the registry accepts both the new
// boot-scoped rl_<instance>_<sequence> format and the legacy rl_<sequence>
// format, while rejecting malformed boot-scoped IDs (WS01 Slice 1, Step 1).
func TestBootScopedAndLegacyIDsValid(t *testing.T) {
	valid := []string{
		"rl_9f01c0b451d2_42",
		"rl_000000000000_1",
		"rl_0",  // legacy
		"rl_42", // legacy
	}
	for _, id := range valid {
		if !validManagedApplyID(id) {
			t.Errorf("validManagedApplyID(%q) = false, want true", id)
		}
	}

	invalid := []string{
		"",
		"rl_",
		"rl_x",
		"rl_9f01c0b451d2",      // instance-shaped single field is legacy but non-numeric
		"rl_9f01c0b451d_42",    // 11-char instance
		"rl_9f01c0b451d2a_42",  // 13-char instance
		"rl_9f01c0b451dz_42",   // non-hex instance char
		"rl_9f01c0b451d2_01",   // leading-zero sequence
		"rl_9f01c0b451d2_42_7", // too many fields
		"../etc",
	}
	for _, id := range invalid {
		if validManagedApplyID(id) {
			t.Errorf("validManagedApplyID(%q) = true, want false", id)
		}
	}
}

// TestParseManagedApplyIDStructure proves the structured parser reports the
// instance, sequence, and legacy flag correctly.
func TestParseManagedApplyIDStructure(t *testing.T) {
	boot, err := parseManagedApplyID("rl_9f01c0b451d2_42")
	if err != nil {
		t.Fatalf("parse boot-scoped: %v", err)
	}
	if boot.Instance != "9f01c0b451d2" || boot.Sequence != 42 || boot.Legacy {
		t.Fatalf("boot parse = %+v, want instance=9f01c0b451d2 seq=42 legacy=false", boot)
	}

	legacy, err := parseManagedApplyID("rl_7")
	if err != nil {
		t.Fatalf("parse legacy: %v", err)
	}
	if legacy.Instance != "" || legacy.Sequence != 7 || !legacy.Legacy {
		t.Fatalf("legacy parse = %+v, want instance='' seq=7 legacy=true", legacy)
	}
}

// TestClaimFinalizationLifecycle proves ClaimFinalization transitions a pending
// record to finalizing exactly once and rejects a duplicate claim while keeping
// the record queryable (WS01 Slice 1, Step 2).
func TestClaimFinalizationLifecycle(t *testing.T) {
	r := NewManagedApplyRegistry(0, 0)
	id := "rl_9f01c0b451d2_1"
	if err := r.BeginPending(rec(id, ApplyOperationConfigApply)); err != nil {
		t.Fatalf("BeginPending: %v", err)
	}

	claimed, err := r.ClaimFinalization(rec(id, ApplyOperationConfigApply))
	if err != nil || !claimed {
		t.Fatalf("first ClaimFinalization = (%v, %v), want (true, nil)", claimed, err)
	}
	if g, ok := r.Get(id); !ok || g.State != ManagedApplyFinalizing {
		t.Fatalf("state after claim = %+v ok=%v, want finalizing", g, ok)
	}

	again, err := r.ClaimFinalization(rec(id, ApplyOperationConfigApply))
	if err != nil || again {
		t.Fatalf("duplicate ClaimFinalization = (%v, %v), want (false, nil)", again, err)
	}

	// A finalizing record must never be downgraded to pending.
	if err := r.BeginPending(rec(id, ApplyOperationConfigApply)); err != nil {
		t.Fatalf("idempotent BeginPending after claim: %v", err)
	}
	if g, _ := r.Get(id); g.State != ManagedApplyFinalizing {
		t.Fatalf("state = %s, want finalizing (no downgrade)", g.State)
	}

	// Complete transitions finalizing -> terminal and keeps the record.
	if err := r.Complete(rec(id, ApplyOperationConfigApply)); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if g, ok := r.Get(id); !ok || g.State != ManagedApplyTerminal {
		t.Fatalf("state after complete = %+v ok=%v, want terminal", g, ok)
	}
}

// TestClaimFinalizationOperationMismatch proves a claim with a conflicting
// operation is rejected.
func TestClaimFinalizationOperationMismatch(t *testing.T) {
	r := NewManagedApplyRegistry(0, 0)
	id := "rl_9f01c0b451d2_1"
	if err := r.BeginPending(rec(id, ApplyOperationConfigApply)); err != nil {
		t.Fatalf("BeginPending: %v", err)
	}
	if _, err := r.ClaimFinalization(rec(id, ApplyOperationRollback)); err != ErrManagedApplyIDMismatch {
		t.Fatalf("mismatch err = %v, want ErrManagedApplyIDMismatch", err)
	}
}

// TestBeginPendingEnrichesEmptyShell proves an idempotent BeginPending fills in
// fields left blank by an earlier minimal call rather than retaining an empty
// shell (WS01 Slice 1, Step 2).
func TestBeginPendingEnrichesEmptyShell(t *testing.T) {
	r := NewManagedApplyRegistry(0, 0)
	id := "rl_9f01c0b451d2_1"

	// Minimal first call: only the ID.
	if err := r.BeginPending(ManagedApplyRecord{ID: id}); err != nil {
		t.Fatalf("BeginPending minimal: %v", err)
	}

	deadline := time.Now().Add(30 * time.Second).UTC()
	enriched := ManagedApplyRecord{
		ID:           id,
		Operation:    ApplyOperationConfigApply,
		Deadline:     deadline,
		OwnerTokenID: "token-abc",
	}
	if err := r.BeginPending(enriched); err != nil {
		t.Fatalf("BeginPending enrich: %v", err)
	}

	g, ok := r.Get(id)
	if !ok {
		t.Fatal("record missing after enrichment")
	}
	if g.Operation != ApplyOperationConfigApply {
		t.Errorf("Operation = %q, want config_apply", g.Operation)
	}
	if !g.Deadline.Equal(deadline) {
		t.Errorf("Deadline = %v, want %v", g.Deadline, deadline)
	}
	if g.OwnerTokenID != "token-abc" {
		t.Errorf("OwnerTokenID = %q, want token-abc", g.OwnerTokenID)
	}
}

// TestCompletePreservesOwnerTokenID proves Complete preserves private ownership
// metadata carried on the pending record even when the terminal caller does not
// re-supply it (WS01 Slice 1, Step 2).
func TestCompletePreservesOwnerTokenID(t *testing.T) {
	r := NewManagedApplyRegistry(0, 0)
	id := "rl_9f01c0b451d2_1"
	if err := r.BeginPending(ManagedApplyRecord{ID: id, Operation: ApplyOperationConfigApply, OwnerTokenID: "owner-1"}); err != nil {
		t.Fatalf("BeginPending: %v", err)
	}
	// Terminal caller omits OwnerTokenID.
	if err := r.Complete(rec(id, ApplyOperationConfigApply)); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	g, _ := r.Get(id)
	if g.OwnerTokenID != "owner-1" {
		t.Errorf("OwnerTokenID = %q, want owner-1 (preserved)", g.OwnerTokenID)
	}
}

// TestOwnerTokenIDNeverSerialized proves the private ownership field is excluded
// from the JSON projection served to the status:read result endpoint.
func TestOwnerTokenIDNeverSerialized(t *testing.T) {
	recd := ManagedApplyRecord{
		ID:           "rl_9f01c0b451d2_1",
		State:        ManagedApplyTerminal,
		OwnerTokenID: "secret-owner-token",
	}
	b, err := json.Marshal(recd)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "secret-owner-token") || strings.Contains(string(b), "OwnerTokenID") {
		t.Fatalf("serialized record leaked owner token: %s", b)
	}
}
