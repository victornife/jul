// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// fakeComponent is a minimal preparedComponent test double. It records
// whether commit/abort ran and how many times, and lets the test supply the
// retirement commit should return.
type fakeComponent struct {
	id          RuntimeComponent
	committed   atomic.Int32
	aborted     atomic.Int32
	retirement  retirement
	commitOrder *[]RuntimeComponent
}

func (f *fakeComponent) component() RuntimeComponent { return f.id }

func (f *fakeComponent) commit() retirement {
	f.committed.Add(1)
	if f.commitOrder != nil {
		*f.commitOrder = append(*f.commitOrder, f.id)
	}
	return f.retirement
}

func (f *fakeComponent) abort() {
	f.aborted.Add(1)
}

func TestPreparedRuntimeEmptyIsNoOp(t *testing.T) {
	var r PreparedRuntime
	r.Commit()
	r.Abort()
	r.Retire(context.Background())
	// A nil *PreparedRuntime must also be safe, since ReloadPlan.Runtime is
	// only ever non-nil in practice but every call site guards defensively.
	var nilRuntime *PreparedRuntime
	nilRuntime.Commit()
	nilRuntime.Abort()
	nilRuntime.Retire(context.Background())
	nilRuntime.add(&fakeComponent{id: 1})
}

func TestPreparedRuntimeCommitInstallsInAddedOrder(t *testing.T) {
	var r PreparedRuntime
	var order []RuntimeComponent
	first := &fakeComponent{id: 1, commitOrder: &order}
	second := &fakeComponent{id: 2, commitOrder: &order}
	r.add(first)
	r.add(second)

	r.Commit()

	if first.committed.Load() != 1 || second.committed.Load() != 1 {
		t.Fatalf("expected both components committed exactly once, got first=%d second=%d",
			first.committed.Load(), second.committed.Load())
	}
	if len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Fatalf("expected commit order [1 2], got %v", order)
	}
	if first.aborted.Load() != 0 || second.aborted.Load() != 0 {
		t.Fatalf("commit must not abort any component")
	}
}

func TestPreparedRuntimeDuplicateComponentPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected add to panic on a duplicate component slot")
		}
	}()
	var r PreparedRuntime
	r.add(&fakeComponent{id: 1})
	r.add(&fakeComponent{id: 1})
}

func TestPreparedRuntimeAbortReleasesOnlyAddedComponents(t *testing.T) {
	// Simulates a partial Prepare: only one of two intended components was
	// actually staged before a later step failed, so Abort must release
	// exactly what was added and nothing else.
	var r PreparedRuntime
	staged := &fakeComponent{id: 1}
	r.add(staged)

	r.Abort()

	if staged.aborted.Load() != 1 {
		t.Fatalf("expected staged component aborted exactly once, got %d", staged.aborted.Load())
	}
	if staged.committed.Load() != 0 {
		t.Fatalf("abort must never commit a component")
	}
}

func TestPreparedRuntimeCommitIsExactlyOnce(t *testing.T) {
	var r PreparedRuntime
	c := &fakeComponent{id: 1}
	r.add(c)

	r.Commit()
	r.Commit()

	if c.committed.Load() != 1 {
		t.Fatalf("expected exactly one commit, got %d", c.committed.Load())
	}
}

func TestPreparedRuntimeAbortIsExactlyOnce(t *testing.T) {
	var r PreparedRuntime
	c := &fakeComponent{id: 1}
	r.add(c)

	r.Abort()
	r.Abort()

	if c.aborted.Load() != 1 {
		t.Fatalf("expected exactly one abort, got %d", c.aborted.Load())
	}
}

func TestPreparedRuntimeAbortIsNoOpAfterCommit(t *testing.T) {
	// A caller that commits and then (by mistake, e.g. on a later unrelated
	// error) also calls Abort must never release a resource it just
	// installed live.
	var r PreparedRuntime
	c := &fakeComponent{id: 1}
	r.add(c)

	r.Commit()
	r.Abort()

	if c.committed.Load() != 1 {
		t.Fatalf("expected exactly one commit, got %d", c.committed.Load())
	}
	if c.aborted.Load() != 0 {
		t.Fatalf("abort after a successful commit must not release the installed resource, got %d aborts", c.aborted.Load())
	}
}

func TestPreparedRuntimeCommitIsNoOpAfterAbort(t *testing.T) {
	var r PreparedRuntime
	c := &fakeComponent{id: 1}
	r.add(c)

	r.Abort()
	r.Commit()

	if c.aborted.Load() != 1 {
		t.Fatalf("expected exactly one abort, got %d", c.aborted.Load())
	}
	if c.committed.Load() != 0 {
		t.Fatalf("commit after abort must not install the released resource, got %d commits", c.committed.Load())
	}
}

func TestPreparedRuntimeRetireRunsEveryCollectedRetirement(t *testing.T) {
	var r PreparedRuntime
	var ranWith []context.Context
	retire := func(ctx context.Context) { ranWith = append(ranWith, ctx) }
	c1 := &fakeComponent{id: 1, retirement: retire}
	c2 := &fakeComponent{id: 2, retirement: retire}
	// A component with nothing to release (e.g. the first generation) must
	// not contribute a nil call.
	c3 := &fakeComponent{id: 3, retirement: nil}
	r.add(c1)
	r.add(c2)
	r.add(c3)

	r.Commit()
	ctx := context.Background()
	r.Retire(ctx)

	if len(ranWith) != 2 {
		t.Fatalf("expected exactly 2 retirements to run (nil retirements must be skipped), got %d", len(ranWith))
	}
	for _, got := range ranWith {
		if got != ctx {
			t.Fatalf("expected every retirement to run with the caller's context")
		}
	}
}

func TestPreparedRuntimeRetireObservesContextCancellation(t *testing.T) {
	// The aggregate itself does not enforce a timeout — that is the caller's
	// job (see ReloadPlan.RetirePreparedRuntime) — but it must hand the exact
	// ctx through unmodified so a bounded caller's deadline is honoured by
	// whatever the component's retirement chooses to wait on.
	var r PreparedRuntime
	var observedErr error
	c := &fakeComponent{id: 1, retirement: func(ctx context.Context) {
		<-ctx.Done()
		observedErr = ctx.Err()
	}}
	r.add(c)
	r.Commit()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	r.Retire(ctx)

	if !errors.Is(observedErr, context.DeadlineExceeded) {
		t.Fatalf("expected the retirement to observe DeadlineExceeded, got %v", observedErr)
	}
}

// --- Consumer proof: the seam supports the two documented shapes without a
// redesign, using fakes rather than #100/#98's real components (#90 is
// explicitly the shared mechanism; it ships no production component). ---

// fakeCertProvider models #100's certificate provider: swapping it in is a
// single atomic pointer store, and the resource it replaces needs no explicit
// close — only a documented, race-free handoff.
type fakeCertProvider struct {
	id      RuntimeComponent
	current *atomic.Pointer[string]
	next    string
	prev    *string
}

func (f *fakeCertProvider) component() RuntimeComponent { return f.id }

func (f *fakeCertProvider) commit() retirement {
	prev := f.current.Load()
	next := f.next
	f.current.Store(&next)
	if prev == nil {
		return nil // first generation: nothing to release.
	}
	f.prev = prev
	// No close is needed for an immutable provider; retirement here only
	// needs to prove the old value is no longer reachable, not release it.
	return func(context.Context) {}
}

func (f *fakeCertProvider) abort() {}

func TestFakeCertificateProviderSwapsWithNoClose(t *testing.T) {
	var current atomic.Pointer[string]
	var r PreparedRuntime
	first := &fakeCertProvider{id: 1, current: &current, next: "cert-a"}
	r.add(first)
	r.Commit()

	if got := *current.Load(); got != "cert-a" {
		t.Fatalf("expected the first generation's certificate live, got %q", got)
	}

	var r2 PreparedRuntime
	second := &fakeCertProvider{id: 1, current: &current, next: "cert-b"}
	r2.add(second)
	r2.Commit()
	r2.Retire(context.Background())

	if got := *current.Load(); got != "cert-b" {
		t.Fatalf("expected the second generation's certificate live, got %q", got)
	}
	if second.prev == nil || *second.prev != "cert-a" {
		t.Fatalf("expected the replaced provider to be observable to retirement, got %v", second.prev)
	}
}

// fakeAccessSink models #98's access-log sink: the candidate opens
// immediately, but the sink it replaces must not close until requests still
// logging through the old handler generation have drained.
type fakeAccessSink struct {
	id     RuntimeComponent
	opened *atomic.Bool
	old    *fakeSinkHandle
}

type fakeSinkHandle struct {
	drained atomic.Bool
	closed  atomic.Bool
}

func (f *fakeAccessSink) component() RuntimeComponent { return f.id }

func (f *fakeAccessSink) commit() retirement {
	f.opened.Store(true)
	old := f.old
	if old == nil {
		return nil
	}
	return func(ctx context.Context) {
		// Waits for the old generation to drain before closing, exactly as
		// #98's design requires; it must still respect ctx so a stuck drain
		// cannot hang shutdown.
		for {
			if old.drained.Load() {
				old.closed.Store(true)
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Millisecond):
			}
		}
	}
}

func (f *fakeAccessSink) abort() {}

func TestFakeAccessSinkClosesOnlyAfterOldRequestsDrain(t *testing.T) {
	var opened atomic.Bool
	old := &fakeSinkHandle{}
	var r PreparedRuntime
	sink := &fakeAccessSink{id: 1, opened: &opened, old: old}
	r.add(sink)
	r.Commit()

	if !opened.Load() {
		t.Fatal("expected the candidate sink to open at commit")
	}

	retireDone := make(chan struct{})
	go func() {
		r.Retire(context.Background())
		close(retireDone)
	}()

	// The old sink must still be open while requests on the previous
	// generation have not drained yet.
	select {
	case <-retireDone:
		t.Fatal("retirement returned before the old generation drained")
	case <-time.After(20 * time.Millisecond):
	}
	if old.closed.Load() {
		t.Fatal("old sink closed before its generation drained")
	}

	old.drained.Store(true)

	select {
	case <-retireDone:
	case <-time.After(time.Second):
		t.Fatal("retirement did not complete after the old generation drained")
	}
	if !old.closed.Load() {
		t.Fatal("expected the old sink to close once its generation drained")
	}
}
