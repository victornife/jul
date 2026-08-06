// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package background

import (
	"context"
	"sync"
	"testing"
	"time"

	"jul/internal/middleware"
	"jul/internal/upstream"
)

func TestOperationIsBounded(t *testing.T) {
	if !OpCacheRevalidate.Valid() {
		t.Fatal("OpCacheRevalidate must be a valid operation")
	}
	if Operation("user-supplied").Valid() {
		t.Fatal("an unknown operation must not be valid")
	}
	if got := OpCacheRevalidate.String(); got != "cache_revalidate" {
		t.Fatalf("operation name = %q", got)
	}
}

func TestAcquireWithoutLeaseFails(t *testing.T) {
	ctx, release, ok := Acquire(context.Background(), OpCacheRevalidate)
	if ok {
		release()
		t.Fatal("acquisition succeeded without a lease installed")
	}
	if ctx != nil || release != nil {
		t.Fatal("failed acquisition returned a context or release")
	}
	if _, present := Generation(context.Background()); present {
		t.Fatal("Generation reported a lease where none is installed")
	}
}

func TestGroupRejectsUnknownOperation(t *testing.T) {
	g := NewGroup(context.Background(), GroupOptions{Generation: 1})
	if _, _, ok := g.Acquire(context.Background(), Operation("../../etc/passwd")); ok {
		t.Fatal("group admitted an operation outside the bounded set")
	}
	if g.Active() != 0 {
		t.Fatalf("rejected acquisition left %d active operations", g.Active())
	}
}

func TestGroupHonoursAdmitAndDoneHooks(t *testing.T) {
	var admits, dones int
	admit := true
	g := NewGroup(context.Background(), GroupOptions{
		Generation: 3,
		Admit: func() bool {
			admits++
			return admit
		},
		Done: func() { dones++ },
	})

	_, release, ok := g.Acquire(context.Background(), OpCacheRevalidate)
	if !ok {
		t.Fatal("acquisition rejected while Admit returns true")
	}
	if g.Active() != 1 {
		t.Fatalf("active = %d, want 1", g.Active())
	}
	release()
	if g.Active() != 0 || dones != 1 {
		t.Fatalf("active = %d, dones = %d after release", g.Active(), dones)
	}

	admit = false
	if _, _, ok := g.Acquire(context.Background(), OpCacheRevalidate); ok {
		t.Fatal("acquisition succeeded while Admit returns false")
	}
	if admits != 2 || dones != 1 {
		t.Fatalf("admits = %d, dones = %d; a refused acquisition must not run Done", admits, dones)
	}
}

func TestReleaseIsIdempotent(t *testing.T) {
	var dones int
	g := NewGroup(context.Background(), GroupOptions{Generation: 1, Done: func() { dones++ }})
	ctx, release, ok := g.Acquire(context.Background(), OpCacheRevalidate)
	if !ok {
		t.Fatal("acquisition failed")
	}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release()
		}()
	}
	wg.Wait()

	if dones != 1 {
		t.Fatalf("Done ran %d times, want exactly 1", dones)
	}
	if g.Active() != 0 {
		t.Fatalf("active = %d after release", g.Active())
	}
	if ctx.Err() == nil {
		t.Fatal("release did not cancel the operation context")
	}
}

func TestGroupCancelStopsAdmittingAndCancelsActive(t *testing.T) {
	g := NewGroup(context.Background(), GroupOptions{Generation: 1})
	ctx, release, ok := g.Acquire(context.Background(), OpCacheRevalidate)
	if !ok {
		t.Fatal("acquisition failed")
	}

	g.Cancel()
	g.Cancel() // idempotent

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("Cancel did not cancel the active operation")
	}
	if _, _, ok := g.Acquire(context.Background(), OpCacheRevalidate); ok {
		t.Fatal("a canceled group admitted new work")
	}
	if g.Wait(0) {
		t.Fatal("Wait reported drained while an operation is still held")
	}
	release()
	if !g.Wait(time.Second) {
		t.Fatal("Wait did not observe the drain")
	}
}

func TestGroupRefusesWorkAfterParentCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	g := NewGroup(parent, GroupOptions{Generation: 1})
	cancel()

	if _, _, ok := g.Acquire(context.Background(), OpCacheRevalidate); ok {
		t.Fatal("group admitted work after its process context was canceled")
	}
}

func TestOperationDeadlineIsBounded(t *testing.T) {
	g := NewGroup(context.Background(), GroupOptions{Generation: 1, MaxOperation: 20 * time.Millisecond})
	ctx, release, ok := g.Acquire(context.Background(), OpCacheRevalidate)
	if !ok {
		t.Fatal("acquisition failed")
	}
	defer release()

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("operation was not bounded by MaxOperation")
	}
	if g.Active() != 1 {
		t.Fatalf("active = %d; a deadline breach must not release the lease on its own", g.Active())
	}
}

func TestNewGroupDefaultsBoundOperations(t *testing.T) {
	var unsetParent context.Context // the server has not published its process context yet
	g := NewGroup(unsetParent, GroupOptions{Generation: 1})
	ctx, release, ok := g.Acquire(context.Background(), OpCacheRevalidate)
	if !ok {
		t.Fatal("acquisition failed")
	}
	defer release()
	deadline, has := ctx.Deadline()
	if !has {
		t.Fatal("default group produced an unbounded operation")
	}
	if d := time.Until(deadline); d <= 0 || d > DefaultMaxOperation+time.Second {
		t.Fatalf("default operation deadline = %v, want ~%v", d, DefaultMaxOperation)
	}
}

func TestDetachCopiesOnlyTheAllowList(t *testing.T) {
	src, disconnect := context.WithCancel(context.Background())
	src = upstream.WithSnapshot(src, upstream.SnapshotMap{
		{Name: "api", Scheme: "http"}: nil,
	})
	src = middleware.WithClientIdentity(src, &middleware.ClientIdentity{Verified: true, CN: "client.example"})
	src = middleware.WithRequestID(src, "req-9")
	src = middleware.WithTraceID(src, "trace-9")
	src = middleware.WithClaims(src, map[string]any{"sub": "alice"})

	parent, stop := context.WithCancel(context.Background())
	defer stop()
	got := Detach(parent, src)

	if len(upstream.SnapshotsFrom(got)) != 1 {
		t.Error("upstream snapshot was not carried over")
	}
	if id := middleware.ClientIdentityFrom(got); id == nil || id.CN != "client.example" {
		t.Errorf("client identity = %+v, want the source identity", id)
	}
	if got := middleware.RequestIDFrom(got); got != "req-9" {
		t.Errorf("request id = %q, want req-9", got)
	}
	if got := middleware.TraceIDFrom(got); got != "trace-9" {
		t.Errorf("trace id = %q, want trace-9", got)
	}
	if claims := middleware.ClaimsFrom(got); claims != nil {
		t.Errorf("claims = %v, want nil (deliberately excluded)", claims)
	}

	disconnect()
	if err := got.Err(); err != nil {
		t.Fatalf("detached context inherited source cancellation: %v", err)
	}
	stop()
	if got.Err() == nil {
		t.Fatal("detached context did not inherit parent cancellation")
	}
}

func TestDetachWithNilSourceReturnsParent(t *testing.T) {
	parent := context.Background()
	var src context.Context
	if got := Detach(parent, src); got != parent {
		t.Fatal("Detach with a nil source must return the parent unchanged")
	}
}

func TestWithLeaseIgnoresNil(t *testing.T) {
	ctx := context.Background()
	if got := WithLease(ctx, nil); got != ctx {
		t.Fatal("WithLease(nil) must not wrap the context")
	}
	if LeaseFrom(ctx) != nil {
		t.Fatal("LeaseFrom returned a lease from a bare context")
	}
}

func TestLeaseRoundTripsThroughContext(t *testing.T) {
	g := NewGroup(context.Background(), GroupOptions{Generation: 11})
	ctx := WithLease(context.Background(), g)

	if gen, ok := Generation(ctx); !ok || gen != 11 {
		t.Fatalf("Generation = (%d, %v), want (11, true)", gen, ok)
	}
	opCtx, release, ok := Acquire(ctx, OpCacheRevalidate)
	if !ok {
		t.Fatal("Acquire through the context failed")
	}
	defer release()
	if opCtx == nil {
		t.Fatal("Acquire returned a nil context")
	}
}
