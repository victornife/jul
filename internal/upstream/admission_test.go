// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/resilience"
)

func mustPolicy(t *testing.T, o resilience.Options) *resilience.Policy {
	t.Helper()
	p, err := resilience.Resolve(o)
	if err != nil {
		t.Fatalf("resolve %+v: %v", o, err)
	}
	return p
}

// waitPending blocks until exactly n requests are parked, so a test can assert
// on queue order without sleeping for a guessed duration.
func waitPending(t *testing.T, a *Admission, n int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for a.Pending() != n {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for pending=%d (have %d, active %d)", n, a.Pending(), a.Active())
		}
		time.Sleep(time.Millisecond)
	}
}

// TestAdmissionUnlimitedByDefault pins the compatibility default: an upstream
// with no resilience block admits everything and still accounts for it, so the
// gauge is meaningful before any limit is configured.
func TestAdmissionUnlimitedByDefault(t *testing.T) {
	a := NewAdmission(nil)
	releases := make([]func(), 0, 100)
	for i := 0; i < 100; i++ {
		rel, err := a.Admit(context.Background(), nil)
		if err != nil {
			t.Fatalf("admit %d: %v", i, err)
		}
		releases = append(releases, rel)
	}
	if got := a.Active(); got != 100 {
		t.Fatalf("active = %d, want 100", got)
	}
	for _, rel := range releases {
		rel()
	}
	if got := a.Active(); got != 0 {
		t.Fatalf("active after release = %d, want 0", got)
	}
}

// TestAdmissionCapBoundary checks the exact boundary rather than a value well
// past it: the limit is inclusive, and the first rejection is the limit+1th
// request.
func TestAdmissionCapBoundary(t *testing.T) {
	a := NewAdmission(mustPolicy(t, resilience.Options{MaxActiveRequests: 3}))
	for i := 0; i < 3; i++ {
		if _, err := a.Admit(context.Background(), nil); err != nil {
			t.Fatalf("admit %d: %v", i, err)
		}
	}
	if _, err := a.Admit(context.Background(), nil); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("admit at limit: err = %v, want ErrOverloaded", err)
	}
	if got := a.Active(); got != 3 {
		t.Fatalf("active = %d, want 3", got)
	}
	if got := a.Pending(); got != 0 {
		t.Fatalf("pending = %d, want 0: max_pending_requests=0 means no queue", got)
	}
}

// TestAdmissionNoQueueByDefault is the one default whose zero value is not
// "unlimited". An unbounded pending queue is the memory failure the control
// exists to prevent, so zero must mean no queue at all.
func TestAdmissionNoQueueByDefault(t *testing.T) {
	a := NewAdmission(mustPolicy(t, resilience.Options{MaxActiveRequests: 1}))
	if _, err := a.Admit(context.Background(), nil); err != nil {
		t.Fatalf("first admit: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := a.Admit(context.Background(), nil)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrOverloaded) {
			t.Fatalf("err = %v, want ErrOverloaded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("second admit parked; with max_pending_requests=0 it must reject immediately")
	}
}

// TestAdmissionQueueFull proves the FIFO is bounded: beyond max_pending_requests
// a request is rejected rather than queued.
func TestAdmissionQueueFull(t *testing.T) {
	a := NewAdmission(mustPolicy(t, resilience.Options{MaxActiveRequests: 1, MaxPendingRequests: 2}))
	rel, err := a.Admit(context.Background(), nil)
	if err != nil {
		t.Fatalf("first admit: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r, err := a.Admit(ctx, nil); err == nil {
				r()
			}
		}()
	}
	waitPending(t, a, 2)

	if _, err := a.Admit(context.Background(), nil); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("admit with full queue: err = %v, want ErrOverloaded", err)
	}
	cancel()
	wg.Wait()
	rel()
	if a.Active() != 0 || a.Pending() != 0 {
		t.Fatalf("quiesce: active = %d, pending = %d, want 0/0", a.Active(), a.Pending())
	}
}

// TestAdmissionCancelReleasesQueueSlot proves a client that goes away while
// queued frees its slot: otherwise a burst of abandoned requests would keep the
// queue full for as long as the process lives.
func TestAdmissionCancelReleasesQueueSlot(t *testing.T) {
	a := NewAdmission(mustPolicy(t, resilience.Options{MaxActiveRequests: 1, MaxPendingRequests: 4}))
	rel, err := a.Admit(context.Background(), nil)
	if err != nil {
		t.Fatalf("first admit: %v", err)
	}
	defer rel()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := a.Admit(ctx, nil)
		done <- err
	}()
	waitPending(t, a, 1)
	cancel()

	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := a.Pending(); got != 0 {
		t.Fatalf("pending after cancel = %d, want 0", got)
	}
	if got := a.Active(); got != 1 {
		t.Fatalf("active after cancel = %d, want 1: a cancelled waiter never held a slot", got)
	}
}

// TestAdmissionPendingTimeout proves pending_timeout bounds the wait
// independently of the client's own deadline.
func TestAdmissionPendingTimeout(t *testing.T) {
	a := NewAdmission(mustPolicy(t, resilience.Options{
		MaxActiveRequests:  1,
		MaxPendingRequests: 4,
		PendingTimeout:     20 * time.Millisecond,
	}))
	rel, err := a.Admit(context.Background(), nil)
	if err != nil {
		t.Fatalf("first admit: %v", err)
	}
	defer rel()

	start := time.Now()
	if _, err := a.Admit(context.Background(), nil); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("err = %v, want ErrOverloaded", err)
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Fatalf("returned after %s, before pending_timeout elapsed", elapsed)
	}
	if got := a.Pending(); got != 0 {
		t.Fatalf("pending after timeout = %d, want 0", got)
	}
}

// TestAdmissionFIFOOrder proves the queue is strictly first-in-first-out and
// that no late arrival barges past a request that is already waiting. Barging
// is what turns a queue into a starvation machine under sustained load.
func TestAdmissionFIFOOrder(t *testing.T) {
	const waiters = 8
	a := NewAdmission(mustPolicy(t, resilience.Options{MaxActiveRequests: 1, MaxPendingRequests: waiters}))
	rel, err := a.Admit(context.Background(), nil)
	if err != nil {
		t.Fatalf("first admit: %v", err)
	}

	var mu sync.Mutex
	var order []int
	var wg sync.WaitGroup
	for i := 0; i < waiters; i++ {
		// Park them one at a time so the enqueue order is the loop order rather
		// than a scheduling accident.
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			r, err := a.Admit(context.Background(), nil)
			if err != nil {
				t.Errorf("waiter %d: %v", n, err)
				return
			}
			mu.Lock()
			order = append(order, n)
			mu.Unlock()
			r()
		}(i)
		waitPending(t, a, int64(i+1))
	}

	// A late arrival must not overtake the queue: it has no room, because the
	// FIFO is full at exactly `waiters`.
	if _, err := a.Admit(context.Background(), nil); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("late arrival: err = %v, want ErrOverloaded", err)
	}

	rel()
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	for i, n := range order {
		if n != i {
			t.Fatalf("grant order = %v, want ascending: waiter %d ran in position %d", order, n, i)
		}
	}
}

// TestAdmissionRetireWakesParkedWaiters covers the forced-retirement case. A
// parked request holds a generation reference, but retirement is bounded by a
// grace after which the transport closes anyway, so without this wakeup a
// waiter could be granted a slot onto a transport that no longer exists.
func TestAdmissionRetireWakesParkedWaiters(t *testing.T) {
	a := NewAdmission(mustPolicy(t, resilience.Options{MaxActiveRequests: 1, MaxPendingRequests: 4}))
	rel, err := a.Admit(context.Background(), nil)
	if err != nil {
		t.Fatalf("first admit: %v", err)
	}
	defer rel()

	retire := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := a.Admit(context.Background(), retire)
		done <- err
	}()
	waitPending(t, a, 1)
	close(retire)

	select {
	case err := <-done:
		if !errors.Is(err, ErrRetired) {
			t.Fatalf("err = %v, want ErrRetired", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("parked waiter was not woken by generation retirement")
	}
	if got := a.Pending(); got != 0 {
		t.Fatalf("pending after retirement = %d, want 0", got)
	}
}

// TestAdmissionPoolCloseWakesParkedWaiters is the pool-lifetime counterpart:
// closing the pool must not leave a request parked on an owner that no longer
// has a health checker or a discovery refresher behind it.
func TestAdmissionPoolCloseWakesParkedWaiters(t *testing.T) {
	a := NewAdmission(mustPolicy(t, resilience.Options{MaxActiveRequests: 1, MaxPendingRequests: 4}))
	rel, err := a.Admit(context.Background(), nil)
	if err != nil {
		t.Fatalf("first admit: %v", err)
	}
	defer rel()

	done := make(chan error, 1)
	go func() {
		_, err := a.Admit(context.Background(), nil)
		done <- err
	}()
	waitPending(t, a, 1)
	a.Retire()

	select {
	case err := <-done:
		if !errors.Is(err, ErrRetired) {
			t.Fatalf("err = %v, want ErrRetired", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("parked waiter was not woken by pool close")
	}
}

// TestAdmissionLimitDecreaseDrainsMonotonically is the reload 1000 -> 100 case
// with 500 already admitted.
//
// Admission is an *entry* control, so the 500 in flight are never killed; what
// must hold is that the overshoot only ever shrinks and that no new request is
// admitted until the pool is genuinely back under the new limit. Asserting at
// every drain step is what distinguishes a monotonic drain from one that
// happens to end in the right place.
//
// No pending queue here on purpose: rejection is then the unambiguous signal
// that the limit still binds, where a queued arrival would only prove that the
// request went somewhere.
func TestAdmissionLimitDecreaseDrainsMonotonically(t *testing.T) {
	a := NewAdmission(mustPolicy(t, resilience.Options{MaxActiveRequests: 1000}))
	releases := make([]func(), 0, 500)
	for i := 0; i < 500; i++ {
		rel, err := a.Admit(context.Background(), nil)
		if err != nil {
			t.Fatalf("admit %d: %v", i, err)
		}
		releases = append(releases, rel)
	}

	a.SetPolicy(mustPolicy(t, resilience.Options{MaxActiveRequests: 100}))
	if got := a.Active(); got != 500 {
		t.Fatalf("active immediately after decrease = %d, want 500: in-flight requests are not evicted", got)
	}

	prevOvershoot := int64(500 - 100)
	for i, rel := range releases {
		// While over the limit, a new arrival must be rejected rather than
		// admitted or queued behind nothing.
		if a.Active() >= 100 {
			if _, err := a.Admit(context.Background(), nil); !errors.Is(err, ErrOverloaded) {
				t.Fatalf("step %d: admit at active=%d returned %v, want ErrOverloaded", i, a.Active(), err)
			}
		}
		rel()
		overshoot := a.Active() - 100
		if overshoot < 0 {
			overshoot = 0
		}
		if overshoot > prevOvershoot {
			t.Fatalf("step %d: overshoot rose from %d to %d; the drain must be monotonic", i, prevOvershoot, overshoot)
		}
		prevOvershoot = overshoot
	}

	if got := a.Active(); got != 0 {
		t.Fatalf("active after full drain = %d, want 0", got)
	}
	// Recovery: the pool is under the limit again, so admission resumes.
	if _, err := a.Admit(context.Background(), nil); err != nil {
		t.Fatalf("admit after drain: %v", err)
	}
}

// TestAdmissionLimitIncreaseWakesWaiters proves a raised limit takes effect at
// once rather than on the next release. An operator raising a limit during an
// incident is not willing to wait for the queue to turn over.
func TestAdmissionLimitIncreaseWakesWaiters(t *testing.T) {
	a := NewAdmission(mustPolicy(t, resilience.Options{MaxActiveRequests: 1, MaxPendingRequests: 4}))
	rel, err := a.Admit(context.Background(), nil)
	if err != nil {
		t.Fatalf("first admit: %v", err)
	}
	defer rel()

	granted := make(chan struct{}, 3)
	for i := 0; i < 3; i++ {
		go func() {
			if r, err := a.Admit(context.Background(), nil); err == nil {
				granted <- struct{}{}
				r()
			}
		}()
	}
	waitPending(t, a, 3)

	a.SetPolicy(mustPolicy(t, resilience.Options{MaxActiveRequests: 4, MaxPendingRequests: 4}))
	for i := 0; i < 3; i++ {
		select {
		case <-granted:
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d of 3 waiters were woken by the limit increase", i)
		}
	}
}

// TestAdmissionHandoffVersusCancel is the race the direct-handoff design makes
// possible: a waiter is granted a slot at the same moment it gives up. The
// grant wins the slot, so the abandoning goroutine must hand it back rather
// than drop it — a leaked grant permanently shrinks the effective limit, and
// the symptom appears hours later as a pool that rejects traffic while idle.
//
// Run repeatedly and under -race, because a single iteration almost never hits
// the interleaving.
func TestAdmissionHandoffVersusCancel(t *testing.T) {
	for i := 0; i < 2000; i++ {
		a := NewAdmission(mustPolicy(t, resilience.Options{MaxActiveRequests: 1, MaxPendingRequests: 1}))
		rel, err := a.Admit(context.Background(), nil)
		if err != nil {
			t.Fatalf("iteration %d: first admit: %v", i, err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan func(), 1)
		go func() {
			r, err := a.Admit(ctx, nil)
			if err != nil {
				done <- nil
				return
			}
			done <- r
		}()
		waitPending(t, a, 1)

		// Release and cancel concurrently: whichever wins, the slot must be
		// accounted for exactly once.
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); rel() }()
		go func() { defer wg.Done(); cancel() }()
		wg.Wait()

		if r := <-done; r != nil {
			r()
		}
		cancel()

		if got := a.Active(); got != 0 {
			t.Fatalf("iteration %d: active = %d, want 0: a grant was leaked or double-counted", i, got)
		}
		if got := a.Pending(); got != 0 {
			t.Fatalf("iteration %d: pending = %d, want 0", i, got)
		}
	}
}

// TestAdmissionConcurrentPolicySwap hammers acquire, release, cancellation and
// policy swaps together. It exists for the race detector and for the quiesce
// invariants: whatever interleaving occurs, the counters must return to zero
// and the limit must never be exceeded except as a non-increasing overshoot
// after a decrease.
func TestAdmissionConcurrentPolicySwap(t *testing.T) {
	a := NewAdmission(mustPolicy(t, resilience.Options{MaxActiveRequests: 32, MaxPendingRequests: 64, PendingTimeout: 5 * time.Millisecond}))

	stop := make(chan struct{})
	var maxSeen atomic.Int64
	var wg sync.WaitGroup

	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				ctx, cancel := context.WithCancel(context.Background())
				if n%4 == 0 {
					// A quarter of the load abandons immediately, which is what
					// exercises the handoff-versus-cancel path under contention.
					cancel()
				}
				rel, err := a.Admit(ctx, nil)
				if err == nil {
					if cur := a.Active(); cur > maxSeen.Load() {
						maxSeen.Store(cur)
					}
					rel()
				}
				cancel()
			}
		}(i)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		limits := []int{32, 4, 64, 1, 128}
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			a.SetPolicy(mustPolicy(t, resilience.Options{
				MaxActiveRequests:  limits[i%len(limits)],
				MaxPendingRequests: 64,
				PendingTimeout:     5 * time.Millisecond,
			}))
			time.Sleep(time.Millisecond)
		}
	}()

	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()

	if got := a.Active(); got != 0 {
		t.Fatalf("active at quiesce = %d, want 0", got)
	}
	if got := a.Pending(); got != 0 {
		t.Fatalf("pending at quiesce = %d, want 0", got)
	}
	// The largest limit in the rotation is 128; nothing may exceed it, since an
	// overshoot can only follow a *decrease*.
	if got := maxSeen.Load(); got > 128 {
		t.Fatalf("peak active = %d, above the highest configured limit of 128", got)
	}
}

// TestAdmissionReleaseIsIdempotent pins the sync.Once guard. Two teardown paths
// calling the same release must not double-decrement, because a negative
// accounting error is unrecoverable: the pool would admit past its limit for
// the rest of the process's life.
func TestAdmissionReleaseIsIdempotent(t *testing.T) {
	a := NewAdmission(mustPolicy(t, resilience.Options{MaxActiveRequests: 2}))
	rel, err := a.Admit(context.Background(), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	rel()
	rel()
	rel()
	if got := a.Active(); got != 0 {
		t.Fatalf("active = %d, want 0", got)
	}
}

// TestAdmissionNoGoroutinePerWaiter proves the parked goroutine is the caller's
// own. goleak in TestMain catches a leak after the fact; this catches the
// design error of spawning a goroutine per queued request, which would be an
// unbounded resource cost hidden behind a bounded queue.
func TestAdmissionNoGoroutinePerWaiter(t *testing.T) {
	a := NewAdmission(mustPolicy(t, resilience.Options{MaxActiveRequests: 1, MaxPendingRequests: 64}))
	rel, err := a.Admit(context.Background(), nil)
	if err != nil {
		t.Fatalf("first admit: %v", err)
	}

	before := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r, err := a.Admit(ctx, nil); err == nil {
				r()
			}
		}()
	}
	waitPending(t, a, 64)

	// 64 caller goroutines are expected. Anything beyond a small allowance means
	// the admission created goroutines of its own.
	if got := runtime.NumGoroutine() - before; got > 64+4 {
		t.Fatalf("goroutines grew by %d for 64 waiters; admission must park the caller's own goroutine", got)
	}
	cancel()
	wg.Wait()
	rel()
}
