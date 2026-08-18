// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"math/rand"
	"sync"
	"testing"
	"time"
)

// fakeClock makes every transition deterministic. Nothing in this file sleeps:
// a breaker test that waits on the wall clock is either slow or flaky, and
// under -race it is usually both.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (f *fakeClock) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) advance(d time.Duration) {
	f.mu.Lock()
	f.t = f.t.Add(d)
	f.mu.Unlock()
}

func testCircuit(maxFails int, failTimeout time.Duration, maxProbes int) (*circuit, *fakeClock) {
	c := newCircuit(maxFails, failTimeout, maxProbes)
	clk := newFakeClock()
	c.now = clk.now
	return c, clk
}

// admitOn takes an admission for b. Results now have to be attributed to the
// generation that authorised them, so a test cannot report one without first
// asking the breaker for permission.
func admitOn(t *testing.T, b *Backend) Attempt {
	t.Helper()
	at, ok := b.admit()
	if !ok {
		t.Fatalf("circuit refused admission for %s", b.Address)
	}
	return at
}

// fakeBackendClock puts b's breaker on a clock the test drives.
func fakeBackendClock(b *Backend) *fakeClock {
	clk := newFakeClock()
	b.circuit.now = clk.now
	return clk
}

// forceOpen trips b's breaker without going through a failure sequence.
func forceOpen(b *Backend) {
	b.circuit.mu.Lock()
	b.circuit.openLocked(b.circuit.now())
	b.circuit.mu.Unlock()
}

func TestCircuitClosedAdmitsWithoutProbing(t *testing.T) {
	c, _ := testCircuit(3, time.Second, 1)

	a := c.admit()
	if !a.ok() {
		t.Fatal("a fresh circuit must admit")
	}
	if a.probe {
		t.Fatal("a closed circuit admits ordinary requests, not probes")
	}
	if got := c.state(); got != StateAvailable {
		t.Fatalf("state = %q, want %q", got, StateAvailable)
	}
}

func TestCircuitOpensOnlyAtThreshold(t *testing.T) {
	c, _ := testCircuit(3, time.Second, 1)

	for i := 1; i < 3; i++ {
		if c.failure(c.admit()) {
			t.Fatalf("failure %d tripped the circuit before max_fails", i)
		}
		if got := c.state(); got != StateAvailable {
			t.Fatalf("after %d failures state = %q, want %q", i, got, StateAvailable)
		}
	}
	if !c.failure(c.admit()) {
		t.Fatal("the max_fails-th failure must trip the circuit")
	}
	if got := c.state(); got != StateCircuitOpen {
		t.Fatalf("state = %q, want %q", got, StateCircuitOpen)
	}
	if a := c.admit(); a.ok() {
		t.Fatal("an open circuit must refuse")
	}
}

func TestCircuitInterleavedSuccessResetsTheRun(t *testing.T) {
	c, _ := testCircuit(3, time.Second, 1)

	c.failure(c.admit())
	c.failure(c.admit())
	c.success(c.admit())
	// max_fails counts *consecutive* failures. Without the reset the next two
	// would trip it, which would make the threshold a lifetime total.
	if c.failure(c.admit()) {
		t.Fatal("a success must clear the consecutive-failure run")
	}
	if c.failure(c.admit()) {
		t.Fatal("tripped one failure early")
	}
	if !c.failure(c.admit()) {
		t.Fatal("should have tripped on the third consecutive failure")
	}
}

func TestCircuitHalfOpenAdmitsExactlyMaxProbes(t *testing.T) {
	const maxProbes = 3
	c, clk := testCircuit(1, time.Second, maxProbes)
	c.failure(c.admit())

	clk.advance(time.Second)
	if got := c.state(); got != StateCircuitHalfOpen {
		t.Fatalf("state after cooldown = %q, want %q", got, StateCircuitHalfOpen)
	}

	for i := 0; i < maxProbes; i++ {
		a := c.admit()
		if !a.ok() {
			t.Fatalf("probe %d refused, want admitted", i)
		}
		if !a.probe {
			t.Fatalf("admission %d is not marked as a probe", i)
		}
	}
	if a := c.admit(); a.ok() {
		t.Fatal("admitted more than max probes")
	}
}

func TestCircuitUnboundedProbesWhenZero(t *testing.T) {
	c, clk := testCircuit(1, time.Second, 0)
	c.failure(c.admit())
	clk.advance(time.Second)

	for i := 0; i < 100; i++ {
		if a := c.admit(); !a.ok() {
			t.Fatalf("probe %d refused, but zero means unbounded", i)
		}
	}
}

func TestCircuitProbeSuccessCloses(t *testing.T) {
	c, clk := testCircuit(1, time.Second, 2)
	c.failure(c.admit())
	clk.advance(time.Second)

	a := c.admit()
	if !c.success(a) {
		t.Fatal("a successful probe must report that it closed the circuit")
	}
	if got := c.state(); got != StateAvailable {
		t.Fatalf("state = %q, want %q", got, StateAvailable)
	}
	if next := c.admit(); next.probe {
		t.Fatal("a closed circuit must admit ordinary requests, not probes")
	}
}

func TestCircuitOnlyOneProbeClaimsTheRecovery(t *testing.T) {
	c, clk := testCircuit(1, time.Second, 3)
	c.failure(c.admit())
	clk.advance(time.Second)

	a1, a2 := c.admit(), c.admit()
	if !c.success(a1) {
		t.Fatal("the first probe should claim the recovery")
	}
	// a2 belongs to the generation that a1 ended. Reporting it as a second
	// recovery would log one transition twice and double-count the gauge.
	if c.success(a2) {
		t.Fatal("a second probe must not also claim the recovery")
	}
}

func TestCircuitProbeFailureReopensIgnoringMaxFails(t *testing.T) {
	c, clk := testCircuit(5, time.Second, 1)
	for i := 0; i < 5; i++ {
		c.failure(c.admit())
	}
	clk.advance(time.Second)

	a := c.admit()
	if !a.probe {
		t.Fatal("want a probe after the cooldown")
	}
	// With max_fails=5 a probe that merely incremented the counter would let
	// five full rounds of probes through per window, which is the load the
	// breaker exists to prevent.
	if !c.failure(a) {
		t.Fatal("a failed probe must re-open immediately")
	}
	if got := c.state(); got != StateCircuitOpen {
		t.Fatalf("state = %q, want %q", got, StateCircuitOpen)
	}
	if next := c.admit(); next.ok() {
		t.Fatal("must refuse again straight after a failed probe")
	}
}

func TestCircuitHungProbeRearms(t *testing.T) {
	c, clk := testCircuit(1, time.Second, 1)
	c.failure(c.admit())
	clk.advance(time.Second)

	hung := c.admit()
	if !hung.probe {
		t.Fatal("want a probe")
	}
	// The probe never reports: a legitimate one may be a multi-hour stream.
	// Without a bound on HALF_OPEN itself it would pin the state forever,
	// never closing and never re-opening.
	clk.advance(time.Second)
	if a := c.admit(); a.ok() {
		t.Fatal("the re-arming admission observes OPEN and must be refused")
	}
	if got := c.state(); got != StateCircuitOpen {
		t.Fatalf("state = %q, want %q", got, StateCircuitOpen)
	}

	clk.advance(time.Second)
	if a := c.admit(); !a.probe {
		t.Fatal("a fresh probe must be admitted after the new window")
	}
	// The hung probe finally answers, against a generation that is two
	// transitions old.
	if c.success(hung) {
		t.Fatal("a stale probe must not close the circuit")
	}
}

func TestCircuitStaleResultsAreIgnored(t *testing.T) {
	t.Run("stale success does not clear live failures", func(t *testing.T) {
		c, clk := testCircuit(2, time.Second, 1)
		inflight := c.admit()

		c.failure(c.admit())
		c.failure(c.admit()) // opens, epoch moves on
		clk.advance(time.Second)
		probe := c.admit()
		c.success(probe) // closes, epoch moves on again

		c.failure(c.admit())
		// inflight was admitted two generations ago. Crediting it now would
		// clear a failure it knows nothing about.
		c.success(inflight)
		if !c.failure(c.admit()) {
			t.Fatal("stale success wrongly reset the current failure run")
		}
	})

	t.Run("stale failure does not count toward a fresh run", func(t *testing.T) {
		c, clk := testCircuit(2, time.Second, 1)
		inflight := c.admit()

		c.failure(c.admit())
		c.failure(c.admit())
		clk.advance(time.Second)
		c.success(c.admit())

		if c.failure(inflight) {
			t.Fatal("a stale failure must not trip the recovered circuit")
		}
		if got := c.state(); got != StateAvailable {
			t.Fatalf("state = %q, want %q", got, StateAvailable)
		}
	})

	t.Run("an unadmitted result is discarded", func(t *testing.T) {
		c, _ := testCircuit(1, time.Second, 1)
		if c.failure(admission{}) {
			t.Fatal("the zero admission must not be able to trip the circuit")
		}
		if c.success(admission{}) {
			t.Fatal("the zero admission must not be able to close the circuit")
		}
	})
}

func TestCircuitForceCloseOutranksTrafficFailures(t *testing.T) {
	c, _ := testCircuit(1, time.Second, 1)
	c.failure(c.admit())

	if !c.forceClose() {
		t.Fatal("forceClose on an open circuit must report a change")
	}
	if got := c.state(); got != StateAvailable {
		t.Fatalf("state = %q, want %q", got, StateAvailable)
	}
	// Steady state: an active checker that keeps succeeding must not keep
	// reporting a recovery, or it papers over live-traffic failures.
	if c.forceClose() {
		t.Fatal("forceClose on an already-closed circuit must report no change")
	}
}

func TestCircuitReleaseProbeReturnsTheSlot(t *testing.T) {
	c, clk := testCircuit(1, time.Second, 1)
	c.failure(c.admit())
	clk.advance(time.Second)

	a := c.admit()
	if next := c.admit(); next.ok() {
		t.Fatal("the single probe slot should be taken")
	}
	// Admitted but never dialled: without returning the slot the backend goes
	// untested until the half-open window expires.
	c.releaseProbe(a)
	if next := c.admit(); !next.probe {
		t.Fatal("the released slot must be reusable")
	}
}

// TestCircuitConcurrentExpiryAdmitsExactlyMaxProbes is the test the whole
// design exists for. When the cooldown elapses, every concurrent request
// observes an eligible backend at the same instant; before #294 all of them
// were admitted and a backend that had just come back took the full production
// load. Exactly max_probes may get through.
func TestCircuitConcurrentExpiryAdmitsExactlyMaxProbes(t *testing.T) {
	for _, maxProbes := range []int{1, 3, 17} {
		t.Run("", func(t *testing.T) {
			const goroutines = 1000
			c, clk := testCircuit(1, time.Second, maxProbes)
			c.failure(c.admit())
			clk.advance(time.Second)

			var (
				start   = make(chan struct{})
				wg      sync.WaitGroup
				mu      sync.Mutex
				probes  int
				ordinar int
			)
			wg.Add(goroutines)
			for i := 0; i < goroutines; i++ {
				go func() {
					defer wg.Done()
					<-start
					a := c.admit()
					if !a.ok() {
						return
					}
					mu.Lock()
					if a.probe {
						probes++
					} else {
						ordinar++
					}
					mu.Unlock()
				}()
			}
			close(start)
			wg.Wait()

			if probes != maxProbes {
				t.Errorf("admitted %d probes, want exactly %d", probes, maxProbes)
			}
			if ordinar != 0 {
				t.Errorf("admitted %d ordinary requests while not closed, want 0", ordinar)
			}
		})
	}
}

// TestCircuitConcurrentMixedTrafficHoldsInvariants runs the state machine under
// contention with the race detector and checks the properties that must hold
// however the operations interleave.
func TestCircuitConcurrentMixedTrafficHoldsInvariants(t *testing.T) {
	const (
		goroutines = 64
		iterations = 500
		maxProbes  = 4
	)
	c, clk := testCircuit(3, 50*time.Millisecond, maxProbes)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(seed int64) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(seed))
			for i := 0; i < iterations; i++ {
				switch rng.Intn(4) {
				case 0:
					clk.advance(time.Duration(rng.Intn(30)) * time.Millisecond)
				case 1:
					c.success(c.admit())
				case 2:
					c.failure(c.admit())
				default:
					c.releaseProbe(c.admit())
				}
			}
		}(int64(g))
	}
	wg.Wait()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.probesInFlight > maxProbes {
		t.Errorf("probesInFlight = %d, exceeds max %d", c.probesInFlight, maxProbes)
	}
	if c.probesInFlight < 0 {
		t.Errorf("probesInFlight = %d, went negative", c.probesInFlight)
	}
	if (c.phase == phaseClosed) != (c.closedEpoch.Load() != 0) {
		t.Errorf("gate and phase disagree: phase=%v gate=%d", c.phase, c.closedEpoch.Load())
	}
	if c.phase == phaseClosed && int(c.fails.Load()) >= c.maxFails {
		t.Errorf("closed with fails=%d >= max_fails=%d", c.fails.Load(), c.maxFails)
	}
}

// TestCircuitModel drives the implementation and an independent model through
// the same random operation sequence.
//
// The model deliberately does not mirror the transition table — a model that
// reimplements the code proves only that it was typed twice. It tracks the
// contract an operator relies on: how many requests can reach a backend that is
// not known-good, and whether a threshold breach really takes it out.
func TestCircuitModel(t *testing.T) {
	const (
		maxFails    = 3
		failTimeout = time.Second
		maxProbes   = 2
	)
	c, clk := testCircuit(maxFails, failTimeout, maxProbes)

	rng := rand.New(rand.NewSource(20260818))
	// live holds admissions that have been granted but not yet resulted.
	var live []admission
	consecutiveFails := 0
	closed := true

	for step := 0; step < 20000; step++ {
		switch rng.Intn(5) {
		case 0:
			a := c.admit()
			if a.ok() {
				live = append(live, a)
			}
			if closed && !a.ok() {
				t.Fatalf("step %d: a closed circuit refused a request", step)
			}
			if closed && a.probe {
				t.Fatalf("step %d: a closed circuit issued a probe", step)
			}
			if !closed && a.ok() && !a.probe {
				t.Fatalf("step %d: an ordinary request reached a backend that is not known-good", step)
			}

		case 1, 2:
			if len(live) == 0 {
				continue
			}
			i := rng.Intn(len(live))
			a := live[i]
			live = append(live[:i], live[i+1:]...)

			fail := rng.Intn(2) == 0
			if fail {
				c.failure(a)
			} else {
				c.success(a)
			}

			// The model only tracks generations it can still reason about:
			// once a result is stale the implementation discards it, and so
			// does the model, by re-reading the gate.
			if closed && !a.probe {
				if fail {
					consecutiveFails++
				} else {
					consecutiveFails = 0
				}
				if consecutiveFails >= maxFails && c.closedEpoch.Load() != 0 {
					t.Fatalf("step %d: %d consecutive failures did not open the circuit", step, consecutiveFails)
				}
			}
			if a.probe && !fail && c.closedEpoch.Load() == 0 {
				// A successful probe either closes the circuit or was stale.
				// It can never leave a live generation still open.
				if a.epoch == currentEpoch(c) {
					t.Fatalf("step %d: a live probe succeeded but the circuit stayed open", step)
				}
			}

		case 3:
			clk.advance(time.Duration(rng.Intn(1500)) * time.Millisecond)

		case 4:
			if len(live) == 0 {
				continue
			}
			i := rng.Intn(len(live))
			c.releaseProbe(live[i])
			live = append(live[:i], live[i+1:]...)
		}

		closed = c.closedEpoch.Load() != 0
		if closed {
			consecutiveFails = int(c.fails.Load())
		} else {
			consecutiveFails = 0
		}

		c.mu.Lock()
		overrun := c.probesInFlight > maxProbes
		negative := c.probesInFlight < 0
		c.mu.Unlock()
		if overrun {
			t.Fatalf("step %d: probes in flight exceeded max_probes", step)
		}
		if negative {
			t.Fatalf("step %d: probes in flight went negative", step)
		}
	}
}

func currentEpoch(c *circuit) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.epoch
}

func BenchmarkCircuitAdmit(b *testing.B) {
	c, _ := testCircuit(3, time.Second, 1)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.admit()
	}
}

func BenchmarkCircuitAdmitParallel(b *testing.B) {
	c, _ := testCircuit(3, time.Second, 1)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			a := c.admit()
			c.success(a)
		}
	})
}
