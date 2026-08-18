// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"sync"
	"testing"
	"time"

	"jul/internal/config"
)

// TestSelectionBoundsProbesUnderLeastConn is why the breaker has to be
// consulted before the balancer rather than after it.
//
// A backend that has just come back has nothing in flight, so least_conn
// considers it the best candidate for every request at once. Filtering after
// the balancer has chosen would mean either sending it everything or dropping
// requests that had a healthy backend available.
func TestSelectionBoundsProbesUnderLeastConn(t *testing.T) {
	p := pool(t, "least_conn",
		config.UpstreamServer{Address: "a:80", Weight: 1},
		config.UpstreamServer{Address: "b:80", Weight: 1})
	p.setCircuitLimits(circuitParams{maxFails: 1, failTimeout: time.Second, halfOpenProbes: 1})
	recovering, healthy := p.Backends()[0], p.Backends()[1]
	clk := fakeBackendClock(recovering)
	fakeBackendClock(healthy)

	p.MarkFailure(admitOn(t, recovering))
	clk.advance(time.Second)

	counts := map[string]int{}
	for i := 0; i < 50; i++ {
		at, err := p.Pick()
		if err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
		counts[at.Address]++
	}
	if counts["a:80"] != 1 {
		t.Errorf("the recovering backend took %d requests, want exactly 1 probe", counts["a:80"])
	}
	if counts["b:80"] != 49 {
		t.Errorf("the healthy backend took %d requests, want the remaining 49", counts["b:80"])
	}
}

// TestSelectionReselectsWhenTheClaimLoses pins the third step of the gate.
// The balancer picks from a non-consuming filter, so between the filter and the
// claim another goroutine can take the last probe slot. Losing that race must
// fall through to another backend rather than fail a request that had a healthy
// one available.
func TestSelectionReselectsWhenTheClaimLoses(t *testing.T) {
	p := pool(t, "least_conn",
		config.UpstreamServer{Address: "a:80", Weight: 1},
		config.UpstreamServer{Address: "b:80", Weight: 1})
	p.setCircuitLimits(circuitParams{maxFails: 1, failTimeout: time.Second, halfOpenProbes: 1})
	recovering, healthy := p.Backends()[0], p.Backends()[1]
	clk := fakeBackendClock(recovering)
	fakeBackendClock(healthy)

	p.MarkFailure(admitOn(t, recovering))
	clk.advance(time.Second)

	// Take the only probe slot out of band, so the next selection sees the
	// backend as eligible and then loses the claim.
	if _, ok := recovering.admit(); !ok {
		t.Fatal("expected to take the probe slot")
	}
	at, err := p.Pick()
	if err != nil {
		t.Fatalf("a lost claim failed the request instead of re-selecting: %v", err)
	}
	if at.Address != "b:80" {
		t.Fatalf("picked %q, want the healthy backend", at.Address)
	}
}

// TestSelectionConcurrentProbesAcrossBackends checks the bound holds per
// backend when the whole pool recovers at once, which is what a shared
// dependency coming back looks like.
func TestSelectionConcurrentProbesAcrossBackends(t *testing.T) {
	const (
		goroutines = 500
		maxProbes  = 2
	)
	p := pool(t, "round_robin",
		config.UpstreamServer{Address: "a:80", Weight: 1},
		config.UpstreamServer{Address: "b:80", Weight: 1})
	p.setCircuitLimits(circuitParams{maxFails: 1, failTimeout: time.Second, halfOpenProbes: maxProbes})

	clocks := make([]*fakeClock, 0, 2)
	for _, b := range p.Backends() {
		clocks = append(clocks, fakeBackendClock(b))
	}
	for _, b := range p.Backends() {
		p.MarkFailure(admitOn(t, b))
	}
	for _, c := range clocks {
		c.advance(time.Second)
	}

	var (
		start  = make(chan struct{})
		wg     sync.WaitGroup
		mu     sync.Mutex
		counts = map[string]int{}
	)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-start
			at, err := p.Pick()
			if err != nil {
				return
			}
			mu.Lock()
			counts[at.Address]++
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()

	total := 0
	for addr, n := range counts {
		if n > maxProbes {
			t.Errorf("backend %s took %d probes, want at most %d", addr, n, maxProbes)
		}
		total += n
	}
	if total != 2*maxProbes {
		t.Errorf("admitted %d requests in total, want %d", total, 2*maxProbes)
	}
}

// TestOpenCircuitIsNotReportedAsCapacity pins that the two refusals stay
// distinct. "Every backend is at capacity" and "no backend is healthy" call for
// opposite operator responses, so an open circuit must not borrow the capacity
// error.
func TestOpenCircuitIsNotReportedAsCapacity(t *testing.T) {
	p := pool(t, "round_robin", config.UpstreamServer{Address: "a:80", Weight: 1})
	p.setCircuitLimits(circuitParams{maxFails: 1, failTimeout: time.Second, halfOpenProbes: 1})
	b := p.Backends()[0]
	p.MarkFailure(admitOn(t, b))

	_, err := p.Pick()
	if err == nil {
		t.Fatal("an open circuit still admitted a request")
	}
	if err != ErrNoAvailableBackend {
		t.Fatalf("err = %v, want ErrNoAvailableBackend", err)
	}
}

// TestActiveHealthSuppressesProbing pins the ranking between the two verdicts.
// A backend the active checker has ejected must not be probed with real user
// requests: Jul already knows it is down, from an out-of-band check that costs
// no user traffic.
func TestActiveHealthSuppressesProbing(t *testing.T) {
	p := pool(t, "round_robin", config.UpstreamServer{Address: "a:80", Weight: 1})
	p.setCircuitLimits(circuitParams{maxFails: 1, failTimeout: time.Second, halfOpenProbes: 1})
	b := p.Backends()[0]
	clk := fakeBackendClock(b)

	p.MarkFailure(admitOn(t, b))
	b.setActiveHealthy(false)
	clk.advance(time.Second)

	if _, ok := b.admit(); ok {
		t.Fatal("probed a backend the active checker has ejected")
	}
	if got := b.State(); got != StateHealthUnhealthy {
		t.Fatalf("state = %q, want %q", got, StateHealthUnhealthy)
	}
}
