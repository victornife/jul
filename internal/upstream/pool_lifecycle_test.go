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
	p.MarkFailure(a)
	p.MarkFailure(a)
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
		p.Release(b)
	}

	if len(p.balancer.(*weightedRR).weights) != 2 {
		t.Fatalf("precondition: weights = %d, want 2", len(p.balancer.(*weightedRR).weights))
	}

	p.UpdateBackends([]config.UpstreamServer{
		{Address: "b:80", Weight: 1},
	})

	if len(p.balancer.(*weightedRR).weights) != 1 {
		t.Fatalf("weights after removal = %d, want 1", len(p.balancer.(*weightedRR).weights))
	}
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
		p.Release(b)
	}

	// Remove a, then add it back (same address/weight) — UpdateBackends creates
	// a fresh Backend pointer for the re-added address. The old pointer must be
	// pruned so the map does not accumulate stale entries.
	p.UpdateBackends([]config.UpstreamServer{})
	p.UpdateBackends([]config.UpstreamServer{{Address: "a:80", Weight: 1}})

	if len(p.balancer.(*weightedRR).weights) > 1 {
		t.Fatalf("weights after churn = %d, want at most 1", len(p.balancer.(*weightedRR).weights))
	}

	// After the next pick the fresh backend pointer is the only entry.
	b, _ := p.Pick()
	p.Release(b)
	if len(p.balancer.(*weightedRR).weights) != 1 {
		t.Fatalf("weights after post-churn pick = %d, want 1", len(p.balancer.(*weightedRR).weights))
	}
}

func TestUpdateBackendsWeightChangeReplaces(t *testing.T) {
	p := pool(t, "weighted_round_robin",
		config.UpstreamServer{Address: "a:80", Weight: 1},
	)
	a := findBackend(p, "a:80")

	p.UpdateBackends([]config.UpstreamServer{{Address: "a:80", Weight: 5}})

	got := findBackend(p, "a:80")
	if got == a {
		t.Fatal("weight change should replace the backend, not reuse it")
	}
	if got.Weight != 5 {
		t.Fatalf("updated backend weight = %d, want 5", got.Weight)
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
				p.Release(b)
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
