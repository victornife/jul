// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"sync"
	"testing"
	"time"

	"jul/internal/config"
)

func TestPoolSnapshotPickUsesSnapshotBackends(t *testing.T) {
	pool, err := NewPool(config.UpstreamConfig{
		Name:     "api",
		Strategy: "round_robin",
		Servers:  []config.UpstreamServer{{Address: "10.0.0.1:80", Weight: 1}},
		MaxFails: 1,
	}, "http")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	snap := pool.Snapshot()
	pool.UpdateBackends([]config.UpstreamServer{
		{Address: "10.0.0.2:80", Weight: 1},
	})

	ctx := WithSnapshot(context.Background(), map[string]*PoolSnapshot{"api": snap})

	b, err := pool.PickCtx(ctx)
	if err != nil {
		t.Fatalf("PickCtx: %v", err)
	}
	if b.Address != "10.0.0.1:80" {
		t.Errorf("snapshot pick used backend %q, want 10.0.0.1:80", b.Address)
	}
	pool.Release(b)
}

func TestPoolSnapshotPickFallsBackToLivePool(t *testing.T) {
	pool, err := NewPool(config.UpstreamConfig{
		Name:     "api",
		Strategy: "round_robin",
		Servers:  []config.UpstreamServer{{Address: "10.0.0.1:80", Weight: 1}},
		MaxFails: 1,
	}, "http")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	b, err := pool.PickCtx(context.Background())
	if err != nil {
		t.Fatalf("PickCtx: %v", err)
	}
	if b.Address != "10.0.0.1:80" {
		t.Errorf("live pick used backend %q, want 10.0.0.1:80", b.Address)
	}
	pool.Release(b)
}

type snapshotFakeDiscoverer struct {
	mu      sync.Mutex
	targets []Target
}

func (f *snapshotFakeDiscoverer) Resolve(ctx context.Context) ([]Target, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Target(nil), f.targets...), nil
}

func (f *snapshotFakeDiscoverer) SetTargets(targets []Target) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.targets = targets
}

func (f *snapshotFakeDiscoverer) Describe() string { return "snapshot-fake" }

func TestDiscoveryConvergenceUpdatesPerRequestSnapshot(t *testing.T) {
	pool, err := NewPool(config.UpstreamConfig{
		Name:     "api",
		Strategy: "round_robin",
		Servers:  []config.UpstreamServer{{Address: "10.0.0.1:80", Weight: 1}},
		MaxFails: 1,
	}, "http")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	d := &snapshotFakeDiscoverer{targets: []Target{{Address: "10.0.0.1:80"}}}
	pool.StartDiscovery(d, 100*time.Millisecond, DiscoveryHooks{}, nil)

	// Wait for the first refresh to settle.
	time.Sleep(150 * time.Millisecond)

	// Capture the snapshot that represents the current live set.
	snap1 := pool.Snapshot()

	// Simulate discovery convergence to a new endpoint.
	d.SetTargets([]Target{{Address: "10.0.0.2:80"}})
	time.Sleep(150 * time.Millisecond)

	// A new snapshot must observe the converged backend.
	snap2 := pool.Snapshot()
	if len(snap2.backends) != 1 || snap2.backends[0].Address != "10.0.0.2:80" {
		t.Fatalf("discovery convergence not reflected: got %+v", snap2.backends)
	}

	// The first snapshot remains stable (no time travel for in-flight requests).
	ctx := WithSnapshot(context.Background(), map[string]*PoolSnapshot{"api": snap1})
	b, err := pool.PickCtx(ctx)
	if err != nil {
		t.Fatalf("PickCtx: %v", err)
	}
	if b.Address != "10.0.0.1:80" {
		t.Errorf("old snapshot pick used backend %q, want 10.0.0.1:80", b.Address)
	}
	pool.Release(b)
}

func TestPoolSnapshotBackendsCtxUsesSnapshot(t *testing.T) {
	pool, err := NewPool(config.UpstreamConfig{
		Name:     "api",
		Strategy: "round_robin",
		Servers:  []config.UpstreamServer{{Address: "10.0.0.1:80", Weight: 1}},
		MaxFails: 1,
	}, "http")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	snap := pool.Snapshot()
	pool.UpdateBackends([]config.UpstreamServer{
		{Address: "10.0.0.2:80", Weight: 1},
	})

	ctx := WithSnapshot(context.Background(), map[string]*PoolSnapshot{"api": snap})
	if got := len(pool.BackendsCtx(ctx)); got != 1 {
		t.Fatalf("snapshot backends = %d, want 1", got)
	}
	if got := pool.BackendsCtx(ctx)[0].Address; got != "10.0.0.1:80" {
		t.Errorf("snapshot backend = %q, want 10.0.0.1:80", got)
	}
}

func TestPoolSnapshotRoundRobinAdvances(t *testing.T) {
	pool, err := NewPool(config.UpstreamConfig{
		Name:     "api",
		Strategy: "round_robin",
		Servers: []config.UpstreamServer{
			{Address: "10.0.0.1:80", Weight: 1},
			{Address: "10.0.0.2:80", Weight: 1},
		},
		MaxFails: 1,
	}, "http")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	snap := pool.Snapshot()
	counts := map[string]int{}
	for i := 0; i < 100; i++ {
		b, err := snap.pick()
		if err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
		counts[b.Address]++
		b.release()
	}
	if counts["10.0.0.1:80"] != 50 || counts["10.0.0.2:80"] != 50 {
		t.Errorf("round-robin distribution uneven: %v", counts)
	}
}

func TestPoolSnapshotWeightedRoundRobinNoRace(t *testing.T) {
	pool, err := NewPool(config.UpstreamConfig{
		Name:     "api",
		Strategy: "weighted_round_robin",
		Servers: []config.UpstreamServer{
			{Address: "10.0.0.1:80", Weight: 1},
			{Address: "10.0.0.2:80", Weight: 3},
		},
		MaxFails: 1,
	}, "http")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	snap := pool.Snapshot()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				b, err := snap.pick()
				if err != nil {
					t.Errorf("pick: %v", err)
					return
				}
				time.Sleep(time.Microsecond)
				b.release()
			}
		}()
	}
	wg.Wait()
}

func TestPoolSnapshotFailoverSkipsTriedBackend(t *testing.T) {
	pool, err := NewPool(config.UpstreamConfig{
		Name:     "api",
		Strategy: "round_robin",
		Servers: []config.UpstreamServer{
			{Address: "10.0.0.1:80", Weight: 1},
			{Address: "10.0.0.2:80", Weight: 1},
		},
		MaxFails: 3, // default: first backend is not ejected after one failure
	}, "http")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	// Simulate two attempts within the same request: the second pick must not
	// return the same backend as the first, even though it is still available.
	snap := pool.Snapshot()
	b1, err := snap.pick()
	if err != nil {
		t.Fatalf("first pick: %v", err)
	}
	b1.release()

	b2, err := snap.pick()
	if err != nil {
		t.Fatalf("second pick: %v", err)
	}
	defer b2.release()
	if b2.Address == b1.Address {
		t.Errorf("failover returned same backend %q; expected the other backend", b2.Address)
	}
}
