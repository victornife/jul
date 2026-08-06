// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"jul/internal/background"
	"jul/internal/config"
	"jul/internal/redact"
	"jul/internal/upstream"
)

func leaseTestServer(shutdown time.Duration) *Server {
	return &Server{
		cfg:        &config.Config{Global: config.GlobalConfig{ShutdownTimeout: config.Duration(shutdown)}},
		log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		redactGens: make(map[uint64]redact.State),
	}
}

// TestDynamicHandlerInstallsBackgroundLease proves the seam is wired: every
// request served through the dynamic handler carries the current generation's
// lease, identified by that generation's id.
func TestDynamicHandlerInstallsBackgroundLease(t *testing.T) {
	s := leaseTestServer(time.Second)
	var (
		gotLease bool
		gotGen   uint64
	)
	s.handlers.Store(newHandlerGen(context.Background(), time.Second, map[string]http.Handler{
		":80": http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			gotLease = background.LeaseFrom(r.Context()) != nil
			gotGen, _ = background.Generation(r.Context())
		}),
	}, nil, 42))

	s.dynamicHandler(":80").ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if !gotLease {
		t.Fatal("no background lease installed in the request context")
	}
	if gotGen != 42 {
		t.Fatalf("lease generation = %d, want 42", gotGen)
	}
}

// TestLeasedWorkKeepsGenerationResourcesOpen is the core lifecycle proof of
// #131: a request that starts leased background work and then returns must NOT
// let the generation retire. The retire callback stands in for closing the
// generation-owned gRPC connections, plugin runtimes and static roots.
func TestLeasedWorkKeepsGenerationResourcesOpen(t *testing.T) {
	s := leaseTestServer(5 * time.Second)

	started := make(chan struct{})
	finish := make(chan struct{})
	done := make(chan struct{})

	g := newHandlerGen(context.Background(), 5*time.Second, map[string]http.Handler{
		":80": http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			// Acquire before ServeHTTP returns, exactly as the cache does.
			ctx, release, ok := background.Acquire(r.Context(), background.OpCacheRevalidate)
			if !ok {
				t.Error("lease acquisition failed on the live generation")
				close(started)
				close(done)
				return
			}
			go func() {
				defer release()
				defer close(done)
				close(started)
				select {
				case <-finish:
				case <-ctx.Done():
					t.Error("leased work was canceled while the generation was still live")
				}
			}()
		}),
	}, nil, 1)
	s.handlers.Store(g)

	s.dynamicHandler(":80").ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	<-started

	retired := make(chan struct{})
	s.retireGen(g, func() { close(retired) }, nil, nil)

	select {
	case <-retired:
		t.Fatal("generation resources were closed while leased background work was still running")
	case <-time.After(150 * time.Millisecond):
	}

	close(finish)
	<-done

	select {
	case <-retired:
	case <-time.After(5 * time.Second):
		t.Fatal("generation resources were not closed after the lease was released")
	}
	s.wg.Wait()
}

// TestForcedRetirementCancelsLeasedWork proves the bound on the other side:
// leased work delays retirement only up to the shutdown grace. When the grace
// expires the server closes the resources anyway and the leased context is
// canceled first, so the work cannot still be using what is about to close.
func TestForcedRetirementCancelsLeasedWork(t *testing.T) {
	s := leaseTestServer(50 * time.Millisecond)

	g := newHandlerGen(context.Background(), time.Minute, map[string]http.Handler{}, nil, 1)
	g.inflight.Add(1) // a request is still executing, so retirement must wait

	ctx, release, ok := g.Acquire(context.Background(), background.OpCacheRevalidate)
	if !ok {
		t.Fatal("lease acquisition failed on a live generation")
	}
	defer release()

	retired := make(chan struct{})
	s.retireGen(g, func() { close(retired) }, nil, nil)

	select {
	case <-retired:
	case <-time.After(5 * time.Second):
		t.Fatal("forced retirement did not close resources after the grace expired")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("forced retirement closed resources without canceling leased work")
	}

	g.release()
	s.wg.Wait()
}

// TestLeaseRefusedOnceRetiring proves acquisition fails cleanly on a retiring
// generation and, critically, leaves the in-flight accounting balanced: a
// rejected acquisition must not pin the generation open forever.
func TestLeaseRefusedOnceRetiring(t *testing.T) {
	g := newHandlerGen(context.Background(), time.Second, map[string]http.Handler{}, nil, 1)
	g.inflight.Add(1)
	g.retiring.Store(true)

	ctx, release, ok := g.Acquire(context.Background(), background.OpCacheRevalidate)
	if ok {
		release()
		t.Fatal("a retiring generation admitted new background work")
	}
	if ctx != nil || release != nil {
		t.Fatal("failed acquisition returned a context or release function")
	}
	if got := g.inflight.Load(); got != 1 {
		t.Fatalf("in-flight count = %d after a refused acquisition, want 1 (accounting must stay balanced)", got)
	}

	// The generation still drains normally once its request finishes.
	g.release()
	select {
	case <-g.drained:
	case <-time.After(time.Second):
		t.Fatal("generation did not drain after a refused acquisition")
	}
}

// TestLeaseAcquisitionRacesRetirement drives acquisition concurrently with
// retirement many times and asserts the two possible outcomes are both safe:
// either the operation is admitted (and retirement waits for it), or it is
// refused (and nothing is left behind). It is meaningful under -race.
func TestLeaseAcquisitionRacesRetirement(t *testing.T) {
	for i := 0; i < 200; i++ {
		s := leaseTestServer(2 * time.Second)
		g := newHandlerGen(context.Background(), time.Second, map[string]http.Handler{}, nil, uint64(i))
		g.inflight.Add(1) // the originating request

		var wg sync.WaitGroup
		wg.Add(2)
		retired := make(chan struct{})
		go func() {
			defer wg.Done()
			s.retireGen(g, func() { close(retired) }, nil, nil)
		}()
		go func() {
			defer wg.Done()
			if _, release, ok := g.Acquire(context.Background(), background.OpCacheRevalidate); ok {
				release()
			}
		}()
		wg.Wait()

		g.release() // the originating request returns
		select {
		case <-retired:
		case <-time.After(5 * time.Second):
			t.Fatalf("iteration %d: generation never retired", i)
		}
		if got := g.inflight.Load(); got != 0 {
			t.Fatalf("iteration %d: in-flight count = %d after drain, want 0", i, got)
		}
		s.wg.Wait()
	}
}

// TestRepeatedRetirementWithActiveLeases mimics repeated reloads while
// background work is active: each generation is retired in turn, and none may
// close its resources before its own leased work is released.
func TestRepeatedRetirementWithActiveLeases(t *testing.T) {
	s := leaseTestServer(5 * time.Second)

	for i := 0; i < 10; i++ {
		g := newHandlerGen(context.Background(), 5*time.Second, map[string]http.Handler{}, nil, uint64(i))
		g.inflight.Add(1)

		_, release, ok := g.Acquire(context.Background(), background.OpCacheRevalidate)
		if !ok {
			t.Fatalf("iteration %d: lease acquisition failed on a live generation", i)
		}

		retired := make(chan struct{})
		s.retireGen(g, func() { close(retired) }, nil, nil)
		g.release() // the request returns; only the lease still holds the generation

		select {
		case <-retired:
			t.Fatalf("iteration %d: resources closed while leased work was active", i)
		case <-time.After(20 * time.Millisecond):
		}

		release()
		select {
		case <-retired:
		case <-time.After(5 * time.Second):
			t.Fatalf("iteration %d: resources were not closed after release", i)
		}
	}
	s.wg.Wait()
}

// TestDrainCancelsAndBoundsLiveBackgroundWork proves shutdown ownership: the
// live generation's leased work is canceled and awaited, so a refresh cannot
// outlive the process.
func TestDrainCancelsAndBoundsLiveBackgroundWork(t *testing.T) {
	s := leaseTestServer(2 * time.Second)
	g := newHandlerGen(context.Background(), time.Minute, map[string]http.Handler{}, nil, 1)
	s.handlers.Store(g)

	ctx, release, ok := g.Acquire(context.Background(), background.OpCacheRevalidate)
	if !ok {
		t.Fatal("lease acquisition failed on the live generation")
	}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		defer release()
		<-ctx.Done()
	}()

	s.drainLiveBackground()

	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel the live generation's background work")
	}
	if got := g.bg.Active(); got != 0 {
		t.Fatalf("background operations still active after drain: %d", got)
	}
}

// TestDrainDoesNotWedgeOnStuckBackgroundWork proves the drain bound: work that
// ignores cancellation delays shutdown by at most the shutdown grace.
func TestDrainDoesNotWedgeOnStuckBackgroundWork(t *testing.T) {
	s := leaseTestServer(50 * time.Millisecond)
	g := newHandlerGen(context.Background(), time.Minute, map[string]http.Handler{}, nil, 1)
	s.handlers.Store(g)

	_, release, ok := g.Acquire(context.Background(), background.OpCacheRevalidate)
	if !ok {
		t.Fatal("lease acquisition failed on the live generation")
	}

	done := make(chan struct{})
	go func() {
		s.drainLiveBackground()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown wedged on background work that ignored cancellation")
	}
	release()
}

// TestBackgroundContextIsProcessScoped proves the context root: the leased
// context is canceled by the process context, not by the request that created
// it, and it always carries a bounded deadline.
func TestBackgroundContextIsProcessScoped(t *testing.T) {
	processCtx, shutdown := context.WithCancel(context.Background())
	g := newHandlerGen(processCtx, time.Minute, map[string]http.Handler{}, nil, 1)

	reqCtx, disconnect := context.WithCancel(context.Background())
	reqCtx = upstream.WithSnapshot(reqCtx, upstream.SnapshotMap{
		{Name: "api", Scheme: "http"}: nil,
	})
	ctx, release, ok := g.Acquire(reqCtx, background.OpCacheRevalidate)
	if !ok {
		t.Fatal("lease acquisition failed")
	}
	defer release()

	if len(upstream.SnapshotsFrom(ctx)) != 1 {
		t.Error("generation upstream snapshot was not carried onto the background context")
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		t.Error("background context has no bounded deadline")
	}

	disconnect()
	if err := ctx.Err(); err != nil {
		t.Fatalf("background context ended with the client request: %v", err)
	}

	shutdown()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("background context was not canceled by process shutdown")
	}
}
