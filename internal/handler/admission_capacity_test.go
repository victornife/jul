// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"jul/internal/config"
)

// TestSaturatedQueueDoesNotGrowGoroutines is the proof behind the design claim
// that no goroutine is created for a pending, waiting or parked request: the
// inbound net/http goroutine is the one that parks.
//
// It matters because the alternative designs the ADR rejected all cost a
// goroutine per waiter, which would make a bounded queue an unbounded resource.
// The queue here is large enough that a per-waiter goroutine would be obvious.
func TestSaturatedQueueDoesNotGrowGoroutines(t *testing.T) {
	const queued = 256

	block := make(chan struct{})
	var unblock sync.Once
	defer unblock.Do(func() { close(block) })

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer backend.Close()

	h, adm := accountingProxy(t, backend.Listener.Addr().String(), &config.ResilienceConfig{
		MaxActiveRequests:  1,
		MaxPendingRequests: queued,
	}, nil)

	// Occupy the only slot.
	go h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	waitActive(t, adm, 1)

	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	before := runtime.NumGoroutine()

	// Park a full queue. Each caller is one goroutine of the test's own making,
	// which stands in for the inbound net/http goroutine that would park in
	// production; anything beyond that count came from the admission itself.
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < queued; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
			h.ServeHTTP(httptest.NewRecorder(), req)
		}()
	}
	waitPendingAdmission(t, adm, queued)

	grew := runtime.NumGoroutine() - before
	// One goroutine per caller is expected and is the caller's own. A small
	// allowance covers the runtime's own bookkeeping.
	if grew > queued+8 {
		t.Fatalf("goroutines grew by %d for %d parked requests; admission is creating goroutines of its own", grew, queued)
	}

	cancel()
	wg.Wait()
	unblock.Do(func() { close(block) })
	if !eventuallyZero(adm.Pending) {
		t.Fatalf("pending at quiesce = %d, want 0", adm.Pending())
	}
}

// TestParkedRequestMemoryFootprint measures what max_pending_requests actually
// costs, because sizing it from the waiter struct is wrong by orders of
// magnitude.
//
// Admission is innermost, so a parked request is already a fully parsed
// *http.Request with header maps and whatever authentication and WAF context ran
// before it — kilobytes each, not the tens of bytes a waiter costs. The measured
// figure goes into the sizing guidance.
//
// It reports rather than asserts a tight bound: the point is to publish a real
// number, and a strict threshold would be a flaky test about Go's allocator.
func TestParkedRequestMemoryFootprint(t *testing.T) {
	const queued = 200

	block := make(chan struct{})
	var unblock sync.Once
	defer unblock.Do(func() { close(block) })

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer backend.Close()

	h, adm := accountingProxy(t, backend.Listener.Addr().String(), &config.ResilienceConfig{
		MaxActiveRequests:  1,
		MaxPendingRequests: queued,
	}, nil)

	go h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	waitActive(t, adm, 1)

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < queued; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A request shaped like a real one: a handful of headers, which is
			// what makes a parked request kilobytes rather than bytes.
			req := httptest.NewRequest(http.MethodGet, "/api/v1/resource?page=2", nil).WithContext(ctx)
			req.Header.Set("User-Agent", "jul-sizing/1.0")
			req.Header.Set("Accept", "application/json")
			req.Header.Set("Authorization", "Bearer "+strings.Repeat("t", 320))
			req.Header.Set("X-Request-Id", "01J8Z2Q7B4K3N6R9V2X5Y8A1C4")
			h.ServeHTTP(httptest.NewRecorder(), req)
		}()
	}
	waitPendingAdmission(t, adm, queued)

	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	perRequest := (int64(after.HeapAlloc) - int64(before.HeapAlloc)) / queued
	t.Logf("parked-request footprint: %d parked, heap delta %d bytes, ~%d bytes per parked request",
		queued, int64(after.HeapAlloc)-int64(before.HeapAlloc), perRequest)

	if perRequest <= 0 {
		t.Skip("heap delta was not positive; the allocator reclaimed concurrently, so no figure can be published from this run")
	}
	// The claim under test is the order of magnitude, not the exact value: a
	// parked request is kilobytes, so sizing the queue from the waiter struct
	// would understate memory by orders of magnitude.
	if perRequest < 256 {
		t.Fatalf("measured %d bytes per parked request; that is waiter-sized, which contradicts the sizing guidance", perRequest)
	}

	cancel()
	wg.Wait()
	unblock.Do(func() { close(block) })
}

func eventuallyZero(read func() int64) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if read() == 0 {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}
