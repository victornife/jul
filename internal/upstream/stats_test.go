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

func TestStatsCountsBackendsByState(t *testing.T) {
	p := pool(t, "round_robin",
		config.UpstreamServer{Address: "a:80", Weight: 1},
		config.UpstreamServer{Address: "b:80", Weight: 1},
		config.UpstreamServer{Address: "c:80", Weight: 1})
	p.setCircuitLimits(circuitParams{maxFails: 1, failTimeout: time.Hour, halfOpenProbes: 1})
	backends := p.Backends()

	// One open circuit, one ejected by active health, one available.
	p.MarkFailure(admitOn(t, backends[0]))
	backends[1].setActiveHealthy(false)

	s := p.Stats()
	if got := s.ByState[StateCircuitOpen]; got != 1 {
		t.Errorf("circuit_open = %d, want 1", got)
	}
	if got := s.ByState[StateHealthUnhealthy]; got != 1 {
		t.Errorf("health_unhealthy = %d, want 1", got)
	}
	if got := s.ByState[StateAvailable]; got != 1 {
		t.Errorf("available = %d, want 1", got)
	}
	if s.Eligible != 1 {
		t.Errorf("eligible = %d, want 1", s.Eligible)
	}
	// Every state is seeded, so a dashboard shows a zero rather than no data.
	if len(s.ByState) != len(BackendStates()) {
		t.Errorf("ByState has %d entries, want one per state (%d)", len(s.ByState), len(BackendStates()))
	}
}

// TestStatsSeparatesCapacityFromHealth pins that a saturated backend is reported
// as at_capacity and not as unavailable. They are the only two conditions here
// that are about load rather than health, and conflating them sends an operator
// looking for a broken backend when the answer is a limit.
func TestStatsSeparatesCapacityFromHealth(t *testing.T) {
	p := resiliencePool(t, []string{"127.0.0.1:1", "127.0.0.1:2"},
		&config.ResilienceConfig{MaxActivePerBackend: 1})

	at, err := p.Pick()
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	defer p.Release(at.Backend)

	s := p.Stats()
	if got := s.ByState[StateAtCapacity]; got != 1 {
		t.Errorf("at_capacity = %d, want 1", got)
	}
	if got := s.ByState[StateAvailable]; got != 1 {
		t.Errorf("available = %d, want the untouched backend", got)
	}
	if s.Eligible != 1 {
		t.Errorf("eligible = %d, want 1", s.Eligible)
	}
}

// TestTrackConnIsIdempotent pins that a double close cannot drive the gauge
// negative. net/http may close a connection more than once, and a connection
// count that goes negative is worse than no count at all.
func TestTrackConnIsIdempotent(t *testing.T) {
	p := pool(t, "round_robin", config.UpstreamServer{Address: "a:80", Weight: 1})

	release := p.TrackConn()
	if got := p.Stats().Connections; got != 1 {
		t.Fatalf("connections = %d, want 1", got)
	}
	release()
	release()
	release()
	if got := p.Stats().Connections; got != 0 {
		t.Fatalf("connections = %d after three closes, want 0", got)
	}
}

// TestRegistryStatsSumsSchemesOfOneName pins the deduplication. Pools are keyed
// by (name, scheme) but the metric label is the name alone, so an upstream
// reached over both http and https must appear once with its numbers summed —
// not twice, which Prometheus would reject as a duplicate series, and not once
// with half the traffic missing.
func TestRegistryStatsSumsSchemesOfOneName(t *testing.T) {
	r := NewRegistry(RegistryOptions{})
	defer r.CloseAll()

	r.Begin()
	for _, scheme := range []string{"http", "https"} {
		if _, err := r.For(context.Background(), upstreamCfg("api", "round_robin", "10.0.0.1:80"), scheme); err != nil {
			t.Fatalf("For %s: %v", scheme, err)
		}
	}
	if _, err := r.For(context.Background(), upstreamCfg("other", "round_robin", "10.0.0.2:80"), "http"); err != nil {
		t.Fatalf("For other: %v", err)
	}
	r.Commit()

	stats := r.Stats()
	if len(stats) != 2 {
		t.Fatalf("Stats() returned %d pools, want 2 names", len(stats))
	}
	byName := map[string]PoolStats{}
	for _, s := range stats {
		byName[s.Name] = s
	}
	api, ok := byName["api"]
	if !ok {
		t.Fatal("the two-scheme pool is missing from Stats()")
	}
	// One backend per scheme, summed.
	if got := api.ByState[StateAvailable]; got != 2 {
		t.Errorf("api available = %d, want 2 (one per scheme)", got)
	}
	if api.Eligible != 2 {
		t.Errorf("api eligible = %d, want 2", api.Eligible)
	}
	if got := byName["other"].Eligible; got != 1 {
		t.Errorf("other eligible = %d, want 1", got)
	}
}

// TestCircuitHookReachesBackendsBuiltLater pins the claim that a discovery
// refresh does not silently stop reporting transitions. The hook is installed
// once, on a pool whose backend set is then replaced wholesale.
func TestCircuitHookReachesBackendsBuiltLater(t *testing.T) {
	p := pool(t, "round_robin", config.UpstreamServer{Address: "a:80", Weight: 1})
	p.setCircuitLimits(circuitParams{maxFails: 1, failTimeout: time.Hour, halfOpenProbes: 1})

	var mu sync.Mutex
	var seen []BackendState
	p.SetCircuitHook(func(_ string, to BackendState) {
		mu.Lock()
		seen = append(seen, to)
		mu.Unlock()
	})

	// A discovery refresh to an entirely different address builds a fresh
	// backend, which never passed through SetCircuitHook.
	p.UpdateTargets([]Target{{Address: "b:80", Weight: 1, ID: "pod-b"}})
	fresh := p.Backends()[0]
	if fresh.Address != "b:80" {
		t.Fatalf("backend = %q, want the refreshed one", fresh.Address)
	}
	p.MarkFailure(admitOn(t, fresh))

	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("a backend built after the hook was installed reported no transition")
	}
	if seen[len(seen)-1] != StateCircuitOpen {
		t.Fatalf("last transition = %q, want %q", seen[len(seen)-1], StateCircuitOpen)
	}
}

// TestCircuitHookReportsHalfOpen pins that the one transition only observable
// inside the circuit is reported. Entering HALF_OPEN happens during admission,
// not on a result, so a hook wired only to MarkFailure/MarkSuccess would miss it.
func TestCircuitHookReportsHalfOpen(t *testing.T) {
	p := pool(t, "round_robin", config.UpstreamServer{Address: "a:80", Weight: 1})
	p.setCircuitLimits(circuitParams{maxFails: 1, failTimeout: time.Second, halfOpenProbes: 1})
	b := p.Backends()[0]
	clk := fakeBackendClock(b)

	var mu sync.Mutex
	var seen []BackendState
	p.SetCircuitHook(func(_ string, to BackendState) {
		mu.Lock()
		seen = append(seen, to)
		mu.Unlock()
	})

	p.MarkFailure(admitOn(t, b))
	clk.advance(time.Second)
	if _, ok := b.admit(); !ok {
		t.Fatal("expected a probe after the cooldown")
	}

	mu.Lock()
	defer mu.Unlock()
	var half bool
	for _, s := range seen {
		if s == StateCircuitHalfOpen {
			half = true
		}
	}
	if !half {
		t.Fatalf("transitions = %v, want one to %q", seen, StateCircuitHalfOpen)
	}
}
