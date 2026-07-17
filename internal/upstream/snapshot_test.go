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

	ctx := WithSnapshot(context.Background(), SnapshotMap{PoolSnapshotKey{Name: "api", Scheme: "http"}: snap})

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
	ctx := WithSnapshot(context.Background(), SnapshotMap{PoolSnapshotKey{Name: "api", Scheme: "http"}: snap1})
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

	ctx := WithSnapshot(context.Background(), SnapshotMap{PoolSnapshotKey{Name: "api", Scheme: "http"}: snap})
	if got := len(pool.BackendsCtx(ctx)); got != 1 {
		t.Fatalf("snapshot backends = %d, want 1", got)
	}
	if got := pool.BackendsCtx(ctx)[0].Address; got != "10.0.0.1:80" {
		t.Errorf("snapshot backend = %q, want 10.0.0.1:80", got)
	}
}

func TestPoolSnapshotBackendsCtxDynamic(t *testing.T) {
	pool, err := NewPool(config.UpstreamConfig{
		Name:     "api",
		Strategy: "round_robin",
		Servers:  []config.UpstreamServer{{Address: "10.0.0.1:80", Weight: 1}},
		MaxFails: 1,
		Discovery: &config.DiscoveryConfig{
			Type:    "dns",
			Target:  "svc.local:80",
			Refresh: config.Duration(100 * time.Millisecond),
		},
	}, "http")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	d := &snapshotFakeDiscoverer{targets: []Target{{Address: "10.0.0.1:80"}}}
	pool.StartDiscovery(d, 100*time.Millisecond, DiscoveryHooks{}, nil)
	eventually(t, func() bool { return len(pool.Backends()) == 1 })

	snap := pool.Snapshot()
	d.SetTargets([]Target{{Address: "10.0.0.2:80"}})
	eventually(t, func() bool {
		return len(pool.Backends()) == 1 && pool.Backends()[0].Address == "10.0.0.2:80"
	})

	ctx := WithSnapshot(context.Background(), SnapshotMap{PoolSnapshotKey{Name: "api", Scheme: "http"}: snap})
	got := pool.BackendsCtx(ctx)
	if len(got) != 1 || got[0].Address != "10.0.0.2:80" {
		t.Errorf("dynamic snapshot BackendsCtx = %v, want [10.0.0.2:80]", got)
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
		b.Release()
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
				b.Release()
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
	b1.Release()

	b2, err := snap.pick()
	if err != nil {
		t.Fatalf("second pick: %v", err)
	}
	defer b2.Release()
	if b2.Address == b1.Address {
		t.Errorf("failover returned same backend %q; expected the other backend", b2.Address)
	}
}

// TestSnapshotMapKeysByNameAndScheme verifies that mixed-scheme upstreams keep
// distinct snapshots in the generation context (R8-02). Selecting through one
// scheme must not return the snapshot for the other.
func TestSnapshotMapKeysByNameAndScheme(t *testing.T) {
	poolHTTP, err := NewPool(config.UpstreamConfig{
		Name:     "api",
		Strategy: "round_robin",
		Servers:  []config.UpstreamServer{{Address: "http-backend", Weight: 1}},
		MaxFails: 1,
	}, "http")
	if err != nil {
		t.Fatalf("NewPool http: %v", err)
	}
	defer poolHTTP.Close()
	poolHTTPS, err := NewPool(config.UpstreamConfig{
		Name:     "api",
		Strategy: "round_robin",
		Servers:  []config.UpstreamServer{{Address: "https-backend", Weight: 1}},
		MaxFails: 1,
	}, "https")
	if err != nil {
		t.Fatalf("NewPool https: %v", err)
	}
	defer poolHTTPS.Close()

	m := SnapshotMap{poolHTTP.Snapshot().Key(): poolHTTP.Snapshot(), poolHTTPS.Snapshot().Key(): poolHTTPS.Snapshot()}
	ctx := WithSnapshot(context.Background(), m)

	bHTTP, err := poolHTTP.PickCtx(ctx)
	if err != nil {
		t.Fatalf("http pick: %v", err)
	}
	defer bHTTP.Release()
	if bHTTP.Address != "http-backend" {
		t.Errorf("http pool selected %q, want http-backend", bHTTP.Address)
	}
	bHTTPS, err := poolHTTPS.PickCtx(ctx)
	if err != nil {
		t.Fatalf("https pick: %v", err)
	}
	defer bHTTPS.Release()
	if bHTTPS.Address != "https-backend" {
		t.Errorf("https pool selected %q, want https-backend", bHTTPS.Address)
	}
}

// TestWeightedRoundRobinStateIsLocalToBalancer verifies that each balancer
// instance owns its smooth-weight state, so concurrent picks through different
// snapshots (or the live pool) do not race on shared Backend fields (R8-03).
func TestWeightedRoundRobinStateIsLocalToBalancer(t *testing.T) {
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

	snap1 := pool.Snapshot()
	snap2 := pool.Snapshot()
	var wg sync.WaitGroup
	pickN := func(s *PoolSnapshot) {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			b, err := s.pick()
			if err != nil {
				t.Errorf("pick: %v", err)
				return
			}
			time.Sleep(time.Microsecond)
			b.Release()
		}
	}
	wg.Add(3)
	go pickN(snap1)
	go pickN(snap2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			b, err := pool.Pick()
			if err != nil {
				t.Errorf("live pick: %v", err)
				return
			}
			time.Sleep(time.Microsecond)
			pool.Release(b)
		}
	}()
	wg.Wait()

	// Each selector should distribute roughly 1:3 over its own 100 picks.
	assertRatio := func(name string, s *PoolSnapshot) {
		counts := map[string]int{}
		for i := 0; i < 400; i++ {
			b, err := s.pick()
			if err != nil {
				t.Fatalf("%s ratio pick: %v", name, err)
			}
			counts[b.Address]++
			b.Release()
		}
		if counts["10.0.0.1:80"] < 80 || counts["10.0.0.1:80"] > 120 {
			t.Errorf("%s weight-1 backend count = %d, want ~100", name, counts["10.0.0.1:80"])
		}
		if counts["10.0.0.2:80"] < 280 || counts["10.0.0.2:80"] > 320 {
			t.Errorf("%s weight-3 backend count = %d, want ~300", name, counts["10.0.0.2:80"])
		}
	}
	assertRatio("snap1", snap1)
	assertRatio("snap2", snap2)
}

// TestPickExcludingReturnsErrorWhenAllAvailableExcluded verifies that
// pickExcluding stops before re-selecting an already-tried backend.
func TestPickExcludingReturnsErrorWhenAllAvailableExcluded(t *testing.T) {
	pool, err := NewPool(config.UpstreamConfig{
		Name:     "api",
		Strategy: "least_conn",
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
	b1, err := snap.pick()
	if err != nil {
		t.Fatalf("first pick: %v", err)
	}
	defer b1.Release()

	tried := map[BackendIdentity]struct{}{b1.Identity(): {}}
	b2, err := snap.pickExcluding(tried)
	if err != nil {
		t.Fatalf("second pick excluding first: %v", err)
	}
	defer b2.Release()
	if b2.Address == b1.Address {
		t.Errorf("pickExcluding returned excluded backend %q", b2.Address)
	}

	tried[b2.Identity()] = struct{}{}
	if _, err := snap.pickExcluding(tried); err != ErrNoAvailableBackend {
		t.Errorf("pickExcluding with all excluded returned %v, want ErrNoAvailableBackend", err)
	}
}

// TestStaticSnapshotFreezesBackends verifies that a static (non-discovery) pool
// snapshot captures the backend set at creation time and does not observe later
// updates (R8 / decision 3A).
func TestStaticSnapshotFreezesBackends(t *testing.T) {
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
	pool.UpdateBackends([]config.UpstreamServer{{Address: "10.0.0.2:80", Weight: 1}})

	if got := len(snap.Backends()); got != 1 {
		t.Fatalf("static snapshot has %d backends, want 1", got)
	}
	if got := snap.Backends()[0].Address; got != "10.0.0.1:80" {
		t.Errorf("static snapshot backend = %q, want 10.0.0.1:80", got)
	}
	b, err := snap.pick()
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	defer b.Release()
	if b.Address != "10.0.0.1:80" {
		t.Errorf("static snapshot pick = %q, want 10.0.0.1:80", b.Address)
	}
}

// TestDynamicSnapshotUsesLiveBackends verifies that a discovery-owned pool's
// snapshot reads from the live pool at request time, so backend convergence is
// visible without waiting for a reload (R8 / decision 3A).
func TestDynamicSnapshotUsesLiveBackends(t *testing.T) {
	pool, err := NewPool(config.UpstreamConfig{
		Name:     "api",
		Strategy: "round_robin",
		Servers:  []config.UpstreamServer{{Address: "10.0.0.1:80", Weight: 1}},
		MaxFails: 1,
		Discovery: &config.DiscoveryConfig{
			Type: "dns",
		},
	}, "http")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	if !pool.dynamic {
		t.Fatal("pool with discovery config should be dynamic")
	}

	snap := pool.Snapshot()
	if !snap.dynamic {
		t.Fatal("snapshot of dynamic pool should be dynamic")
	}

	pool.UpdateBackends([]config.UpstreamServer{{Address: "10.0.0.2:80", Weight: 1}})

	if got := len(snap.Backends()); got != 1 {
		t.Fatalf("dynamic snapshot has %d backends, want 1", got)
	}
	if got := snap.Backends()[0].Address; got != "10.0.0.2:80" {
		t.Errorf("dynamic snapshot backend = %q, want 10.0.0.2:80", got)
	}
	b, err := snap.pick()
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	defer b.Release()
	if b.Address != "10.0.0.2:80" {
		t.Errorf("dynamic snapshot pick = %q, want 10.0.0.2:80", b.Address)
	}
}
