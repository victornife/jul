// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func rec(id string, op ApplyOperation) ManagedApplyRecord {
	return ManagedApplyRecord{ID: id, Operation: op}
}

// TestManagedApplyRegistry_PendingSurvivesPruning proves a pending record is
// never evicted even when the terminal cap is exceeded (AC-02).
func TestManagedApplyRegistry_PendingSurvivesPruning(t *testing.T) {
	r := NewManagedApplyRegistry(2, time.Nanosecond)
	if err := r.BeginPending(rec("rl_1", ApplyOperationConfigApply)); err != nil {
		t.Fatalf("BeginPending: %v", err)
	}
	// Fill and overflow the terminal cap with far-past completions so TTL is met.
	past := time.Now().Add(-time.Hour)
	for i := 2; i <= 8; i++ {
		id := fmt.Sprintf("rl_%d", i)
		rc := rec(id, ApplyOperationConfigApply)
		rc.CompletedAt = past
		if err := r.Complete(rc); err != nil {
			t.Fatalf("Complete %s: %v", id, err)
		}
	}
	if _, ok := r.Get("rl_1"); !ok {
		t.Fatal("pending record rl_1 was evicted; pending must never be evicted")
	}
}

// TestManagedApplyRegistry_DuplicateFinalization proves the terminal callback
// can be claimed exactly once (AC-02, AC-03 exactly-once side effects).
func TestManagedApplyRegistry_DuplicateFinalization(t *testing.T) {
	r := NewManagedApplyRegistry(0, 0)
	first, err := r.BeginFinalization("rl_7")
	if err != nil {
		t.Fatalf("BeginFinalization: %v", err)
	}
	if !first {
		t.Fatal("first BeginFinalization must return true")
	}
	second, err := r.BeginFinalization("rl_7")
	if err != nil {
		t.Fatalf("BeginFinalization dup: %v", err)
	}
	if second {
		t.Fatal("duplicate BeginFinalization must return false")
	}
}

// TestManagedApplyRegistry_OutOfOrderCompletion proves both transactions are
// retained when they complete out of order (AC-02).
func TestManagedApplyRegistry_OutOfOrderCompletion(t *testing.T) {
	r := NewManagedApplyRegistry(0, 0)
	_ = r.BeginPending(rec("rl_1", ApplyOperationConfigApply))
	_ = r.BeginPending(rec("rl_2", ApplyOperationPatchApply))
	// Complete the newer one first.
	if err := r.Complete(rec("rl_2", ApplyOperationPatchApply)); err != nil {
		t.Fatalf("Complete rl_2: %v", err)
	}
	if err := r.Complete(rec("rl_1", ApplyOperationConfigApply)); err != nil {
		t.Fatalf("Complete rl_1: %v", err)
	}
	if g, ok := r.Get("rl_1"); !ok || g.State != ManagedApplyTerminal {
		t.Fatalf("rl_1 not terminal: %+v ok=%v", g, ok)
	}
	if g, ok := r.Get("rl_2"); !ok || g.State != ManagedApplyTerminal {
		t.Fatalf("rl_2 not terminal: %+v ok=%v", g, ok)
	}
}

// TestManagedApplyRegistry_LatestPointsToHighest proves Latest tracks the most
// recently published record and Get still returns older transactions (AC-02).
func TestManagedApplyRegistry_LatestAndGet(t *testing.T) {
	r := NewManagedApplyRegistry(0, 0)
	_ = r.Complete(rec("rl_1", ApplyOperationConfigApply))
	r.SetLatest("rl_1")
	_ = r.Complete(rec("rl_2", ApplyOperationConfigApply))
	r.SetLatest("rl_2")

	latest := r.Latest()
	if latest == nil || latest.ID != "rl_2" {
		t.Fatalf("Latest = %+v, want rl_2", latest)
	}
	if _, ok := r.Get("rl_1"); !ok {
		t.Fatal("Get(rl_1) must still return the older transaction")
	}
}

// TestManagedApplyRegistry_InvalidIDs proves malformed IDs cannot traverse
// paths or create unbounded map entries (AC-02).
func TestManagedApplyRegistry_InvalidIDs(t *testing.T) {
	r := NewManagedApplyRegistry(0, 0)
	bad := []string{"", "rl_", "rl_x", "rl_-1", "rl_00", "../etc", "rl_01", "42", "rl_" + "12345678901234567890"}
	for _, id := range bad {
		if validManagedApplyID(id) {
			t.Errorf("validManagedApplyID(%q) = true, want false", id)
		}
		if _, ok := r.Get(id); ok {
			t.Errorf("Get(%q) returned ok for invalid id", id)
		}
		if err := r.BeginPending(rec(id, ApplyOperationConfigApply)); err != ErrManagedApplyInvalidID {
			t.Errorf("BeginPending(%q) err = %v, want ErrManagedApplyInvalidID", id, err)
		}
	}
	if len(r.byID) != 0 {
		t.Fatalf("invalid IDs created map entries: %d", len(r.byID))
	}
	good := []string{"rl_0", "rl_1", "rl_42", "rl_999999"}
	for _, id := range good {
		if !validManagedApplyID(id) {
			t.Errorf("validManagedApplyID(%q) = false, want true", id)
		}
	}
}

// TestManagedApplyRegistry_IDMismatch proves reusing an ID with a different
// operation is rejected as a programming error (AC-02).
func TestManagedApplyRegistry_IDMismatch(t *testing.T) {
	r := NewManagedApplyRegistry(0, 0)
	if err := r.BeginPending(rec("rl_1", ApplyOperationConfigApply)); err != nil {
		t.Fatalf("BeginPending: %v", err)
	}
	if err := r.BeginPending(rec("rl_1", ApplyOperationConfigApply)); err != nil {
		t.Fatalf("idempotent BeginPending: %v", err)
	}
	if err := r.BeginPending(rec("rl_1", ApplyOperationRollback)); err != ErrManagedApplyIDMismatch {
		t.Fatalf("mismatch err = %v, want ErrManagedApplyIDMismatch", err)
	}
}

// TestManagedApplyRegistry_Concurrent runs concurrent readers and writers to be
// exercised under go test -race (AC-02).
func TestManagedApplyRegistry_Concurrent(t *testing.T) {
	r := NewManagedApplyRegistry(64, time.Minute)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := fmt.Sprintf("rl_%d", n+1)
			_ = r.BeginPending(rec(id, ApplyOperationConfigApply))
			if ok, _ := r.BeginFinalization(id); ok {
				_ = r.Complete(rec(id, ApplyOperationConfigApply))
				r.SetLatest(id)
			}
		}(i)
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			r.Get(fmt.Sprintf("rl_%d", n+1))
			_ = r.Latest()
			r.Prune(time.Now())
		}(i)
	}
	wg.Wait()
}

// TestManagedApplyRegistry_RetainsWithinCapacity proves terminal records within
// capacity are retained regardless of age (AC-02 retention default).
func TestManagedApplyRegistry_RetainsWithinCapacity(t *testing.T) {
	r := NewManagedApplyRegistry(512, time.Hour)
	rc := rec("rl_1", ApplyOperationConfigApply)
	rc.CompletedAt = time.Now().Add(-24 * time.Hour) // very old
	_ = r.Complete(rc)
	r.Prune(time.Now())
	if _, ok := r.Get("rl_1"); !ok {
		t.Fatal("record within capacity was evicted despite being under cap")
	}
}
