// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"context"
	"sync"
	"testing"
)

// The holder exists so a handler deep in the chain can hand a value back up to
// the access-log middleware. Every branch below is a way that hand-back can go
// wrong silently: a missing holder, an empty reason, a later attempt, or two
// goroutines racing. None of them can fail loudly at runtime — the reason just
// disappears from the log — so they are pinned here.

func TestUpstreamReasonRoundTrips(t *testing.T) {
	ctx := WithUpstreamReason(context.Background())
	SetUpstreamReason(ctx, "circuit_open")
	if got := UpstreamReasonFrom(ctx); got != "circuit_open" {
		t.Errorf("UpstreamReasonFrom = %q, want %q", got, "circuit_open")
	}
}

func TestUpstreamReasonWithoutAHolderIsANoOp(t *testing.T) {
	ctx := context.Background()
	// Must not panic: a handler used outside the access-log chain still calls it.
	SetUpstreamReason(ctx, "circuit_open")
	if got := UpstreamReasonFrom(ctx); got != "" {
		t.Errorf("UpstreamReasonFrom on a bare context = %q, want empty", got)
	}
}

func TestUpstreamReasonIsEmptyBeforeAnythingSetsIt(t *testing.T) {
	ctx := WithUpstreamReason(context.Background())
	if got := UpstreamReasonFrom(ctx); got != "" {
		t.Errorf("UpstreamReasonFrom before any set = %q, want empty", got)
	}
}

// An empty reason must not overwrite a real one. A success after a failed retry
// would otherwise erase the reason the earlier attempt recorded.
func TestEmptyReasonDoesNotClearAnExistingOne(t *testing.T) {
	ctx := WithUpstreamReason(context.Background())
	SetUpstreamReason(ctx, "upstream_timeout")
	SetUpstreamReason(ctx, "")
	if got := UpstreamReasonFrom(ctx); got != "upstream_timeout" {
		t.Errorf("UpstreamReasonFrom = %q, want the reason to survive an empty set", got)
	}
}

// Retries call this more than once. Last write wins, so the log reports why the
// request ultimately failed rather than why its first attempt did.
func TestLastReasonWins(t *testing.T) {
	ctx := WithUpstreamReason(context.Background())
	SetUpstreamReason(ctx, "upstream_connect_failed")
	SetUpstreamReason(ctx, "retry_budget_exhausted")
	if got := UpstreamReasonFrom(ctx); got != "retry_budget_exhausted" {
		t.Errorf("UpstreamReasonFrom = %q, want the last write", got)
	}
}

// The holder is written by the proxy goroutine and read by the access log, so
// the atomic is load-bearing. Run with -race.
func TestUpstreamReasonIsSafeUnderConcurrentAccess(t *testing.T) {
	ctx := WithUpstreamReason(context.Background())
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(2)
		go func() { defer wg.Done(); SetUpstreamReason(ctx, "circuit_open") }()
		go func() { defer wg.Done(); _ = UpstreamReasonFrom(ctx) }()
	}
	wg.Wait()
	if got := UpstreamReasonFrom(ctx); got != "circuit_open" {
		t.Errorf("UpstreamReasonFrom = %q, want %q", got, "circuit_open")
	}
}

// A value stored under a different key type must not be mistaken for a holder.
func TestForeignContextValueIsNotTreatedAsAHolder(t *testing.T) {
	type otherKey struct{}
	ctx := context.WithValue(context.Background(), otherKey{}, &upstreamReasonHolder{})
	SetUpstreamReason(ctx, "circuit_open")
	if got := UpstreamReasonFrom(ctx); got != "" {
		t.Errorf("UpstreamReasonFrom = %q, want empty for a foreign key", got)
	}
}

// Each request gets its own holder; one request's reason must never leak into
// another's log line.
func TestHoldersAreIndependentPerContext(t *testing.T) {
	a := WithUpstreamReason(context.Background())
	b := WithUpstreamReason(context.Background())
	SetUpstreamReason(a, "circuit_open")
	if got := UpstreamReasonFrom(b); got != "" {
		t.Errorf("reason leaked across contexts: %q", got)
	}
}

// A derived context shares its parent's holder, which is what lets a handler
// that has added its own values still report back to the access log.
func TestDerivedContextSharesTheHolder(t *testing.T) {
	parent := WithUpstreamReason(context.Background())
	child, cancel := context.WithCancel(parent)
	defer cancel()
	SetUpstreamReason(child, "backend_at_capacity")
	if got := UpstreamReasonFrom(parent); got != "backend_at_capacity" {
		t.Errorf("UpstreamReasonFrom(parent) = %q, want the child's write", got)
	}
}
