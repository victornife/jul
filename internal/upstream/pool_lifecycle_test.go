// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"sync"
	"testing"
	"time"

	"jul/internal/config"
)

// findBackend returns the backend with the given address, or nil.
func findBackend(p *Pool, addr string) *Backend {
	for _, b := range p.Backends() {
		if b.Address == addr {
			return b
		}
	}
	return nil
}

func TestUpdateBackendsPreservesState(t *testing.T) {
	p := pool(t, "round_robin",
		config.UpstreamServer{Address: "a:80", Weight: 1},
		config.UpstreamServer{Address: "b:80", Weight: 1},
	)
	a := findBackend(p, "a:80")
	b := findBackend(p, "b:80")

	// Trip a's passive cooldown (maxFails=2) and put an in-flight request on b.
	p.MarkFailure(admitOn(t, a))
	p.MarkFailure(admitOn(t, a))
	b.acquire()

	now := time.Now().UnixNano()
	if a.available(now) {
		t.Fatal("precondition: backend a should be in cooldown")
	}

	// Update keeps a and b (same address+weight) and adds c.
	p.UpdateBackends([]config.UpstreamServer{
		{Address: "a:80", Weight: 1},
		{Address: "b:80", Weight: 1},
		{Address: "c:80", Weight: 1},
	})

	if got := findBackend(p, "a:80"); got != a {
		t.Fatalf("backend a not preserved across update: %p != %p", got, a)
	}
	if got := findBackend(p, "b:80"); got != b {
		t.Fatalf("backend b not preserved across update: %p != %p", got, b)
	}
	if findBackend(p, "a:80").available(now) {
		t.Fatal("preserved backend a lost its cooldown state")
	}
	if got := findBackend(p, "b:80").Inflight(); got != 1 {
		t.Fatalf("preserved backend b lost its in-flight count: got %d, want 1", got)
	}
	if c := findBackend(p, "c:80"); c == nil {
		t.Fatal("new backend c was not added")
	}
	if n := len(p.Backends()); n != 3 {
		t.Fatalf("backend count after update = %d, want 3", n)
	}
}

func TestUpdateBackendsRemovesAndAdds(t *testing.T) {
	p := pool(t, "round_robin",
		config.UpstreamServer{Address: "a:80", Weight: 1},
		config.UpstreamServer{Address: "b:80", Weight: 1},
	)
	b := findBackend(p, "b:80")

	p.UpdateBackends([]config.UpstreamServer{
		{Address: "b:80", Weight: 1},
		{Address: "c:80", Weight: 1},
	})

	if findBackend(p, "a:80") != nil {
		t.Fatal("removed backend a is still present")
	}
	if got := findBackend(p, "b:80"); got != b {
		t.Fatal("surviving backend b was not preserved")
	}
	if findBackend(p, "c:80") == nil {
		t.Fatal("added backend c is missing")
	}
}

func TestWeightedRRPrunesRemovedBackends(t *testing.T) {
	p := pool(t, "weighted_round_robin",
		config.UpstreamServer{Address: "a:80", Weight: 1},
		config.UpstreamServer{Address: "b:80", Weight: 1},
	)

	// Exercise the balancer so entries are created.
	for i := 0; i < 4; i++ {
		b, err := p.Pick()
		if err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
		p.Release(b.Backend)
	}

	if len(p.balancer.(*weightedRR).weights) != 2 {
		t.Fatalf("precondition: weights = %d, want 2", len(p.balancer.(*weightedRR).weights))
	}

	p.UpdateBackends([]config.UpstreamServer{
		{Address: "b:80", Weight: 1},
	})

	// The accumulator is cleared wholesale rather than pruned, so a departed
	// backend cannot linger and a surviving one starts from zero — which is what
	// makes a weight change converge immediately.
	if len(p.balancer.(*weightedRR).weights) != 0 {
		t.Fatalf("weights after removal = %d, want 0", len(p.balancer.(*weightedRR).weights))
	}

	b, _ := p.Pick()
	p.Release(b.Backend)
	if _, ok := p.balancer.(*weightedRR).weights[findBackend(p, "a:80")]; ok {
		t.Fatal("removed backend a still present in weights map")
	}
	if _, ok := p.balancer.(*weightedRR).weights[findBackend(p, "b:80")]; !ok {
		t.Fatal("surviving backend b missing from weights map")
	}
}

func TestWeightedRRPrunesAfterChurn(t *testing.T) {
	p := pool(t, "weighted_round_robin",
		config.UpstreamServer{Address: "a:80", Weight: 1},
	)

	for i := 0; i < 2; i++ {
		b, _ := p.Pick()
		p.Release(b.Backend)
	}

	// Remove a, then add it back — UpdateBackends creates a fresh Backend pointer
	// for the re-added address. The map must not accumulate stale entries.
	p.UpdateBackends([]config.UpstreamServer{})
	p.UpdateBackends([]config.UpstreamServer{{Address: "a:80", Weight: 1}})

	if len(p.balancer.(*weightedRR).weights) != 0 {
		t.Fatalf("weights after churn = %d, want 0", len(p.balancer.(*weightedRR).weights))
	}

	// After the next pick the fresh backend pointer is the only entry.
	b, _ := p.Pick()
	p.Release(b.Backend)
	if len(p.balancer.(*weightedRR).weights) != 1 {
		t.Fatalf("weights after post-churn pick = %d, want 1", len(p.balancer.(*weightedRR).weights))
	}
}

// TestUpdateBackendsWeightChangePreservesState pins the regression that
// motivated dropping the weight from the reuse key: a Consul or DNS-SRV weight
// flap used to replace the backend, silently discarding its in-flight
// accounting and failure history — exactly when an operator is watching them.
func TestUpdateBackendsWeightChangePreservesState(t *testing.T) {
	p := pool(t, "weighted_round_robin",
		config.UpstreamServer{Address: "a:80", Weight: 1},
	)
	a := findBackend(p, "a:80")
	a.acquire()
	a.circuit.fails.Store(2)

	p.UpdateBackends([]config.UpstreamServer{{Address: "a:80", Weight: 5}})

	got := findBackend(p, "a:80")
	if got != a {
		t.Fatal("a weight change replaced the backend; in-flight accounting and failure history were discarded")
	}
	if got.Weight() != 5 {
		t.Fatalf("updated backend weight = %d, want 5", got.Weight())
	}
	if got.Inflight() != 1 {
		t.Fatalf("in-flight after weight change = %d, want 1", got.Inflight())
	}
	if got.FailCount() != 2 {
		t.Fatalf("failure count after weight change = %d, want 2", got.FailCount())
	}
	got.Release()
}

// TestWeightedRRReconvergesAfterWeightChange proves the accumulator is not
// carried across a weight change. Without clearing it, a backend promoted from
// weight 1 to weight 9 would keep losing picks for as long as its stale current
// weight lasted.
func TestWeightedRRReconvergesAfterWeightChange(t *testing.T) {
	p := pool(t, "weighted_round_robin",
		config.UpstreamServer{Address: "a:80", Weight: 1},
		config.UpstreamServer{Address: "b:80", Weight: 9},
	)
	// Let b accumulate a large lead under the original weights.
	for i := 0; i < 50; i++ {
		b, _ := p.Pick()
		p.Release(b.Backend)
	}

	// Swap the weights around.
	p.UpdateBackends([]config.UpstreamServer{
		{Address: "a:80", Weight: 9},
		{Address: "b:80", Weight: 1},
	})

	counts := map[string]int{}
	for i := 0; i < 100; i++ {
		b, err := p.Pick()
		if err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
		counts[b.Address]++
		p.Release(b.Backend)
	}
	// 9:1 over 100 picks is 90/10. Allow slack for the smoothing, but the new
	// weights must clearly dominate rather than the old lead.
	if counts["a:80"] < 80 {
		t.Fatalf("a received %d of 100 picks after being promoted to weight 9; the stale accumulator was carried across the change (b got %d)", counts["a:80"], counts["b:80"])
	}
}

func TestCloseIdempotentAndSignalsDone(t *testing.T) {
	p := pool(t, "round_robin", config.UpstreamServer{Address: "a:80", Weight: 1})

	select {
	case <-p.Done():
		t.Fatal("Done signaled before Close")
	default:
	}

	p.Close()
	p.Close() // must not panic on second call

	select {
	case <-p.Done():
	default:
		t.Fatal("Done not signaled after Close")
	}
}

func TestUpdateBackendsConcurrentWithPick(t *testing.T) {
	p := pool(t, "round_robin",
		config.UpstreamServer{Address: "a:80", Weight: 1},
		config.UpstreamServer{Address: "b:80", Weight: 1},
	)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			if b, err := p.Pick(); err == nil {
				p.Release(b.Backend)
			}
		}
	}()

	go func() {
		defer wg.Done()
		sets := [][]config.UpstreamServer{
			{{Address: "a:80", Weight: 1}, {Address: "b:80", Weight: 1}},
			{{Address: "b:80", Weight: 1}, {Address: "c:80", Weight: 1}},
			{{Address: "a:80", Weight: 1}, {Address: "c:80", Weight: 1}, {Address: "d:80", Weight: 1}},
		}
		for i := 0; i < 2000; i++ {
			p.UpdateBackends(sets[i%len(sets)])
		}
	}()

	wg.Wait()
}
