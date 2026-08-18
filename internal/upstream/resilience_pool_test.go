// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"errors"
	"testing"

	"jul/internal/config"
	"jul/internal/resilience"
)

func resiliencePool(t *testing.T, addrs []string, r *config.ResilienceConfig) *Pool {
	t.Helper()
	servers := make([]config.UpstreamServer, 0, len(addrs))
	for _, a := range addrs {
		servers = append(servers, config.UpstreamServer{Address: a, Weight: 1})
	}
	p, err := NewPool(config.UpstreamConfig{
		Name:       "api",
		Strategy:   "round_robin",
		Servers:    servers,
		MaxFails:   3,
		Resilience: r,
	}, "http")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

// TestPoolDefaultsAreUnlimited pins the compatibility promise: an upstream with
// no resilience block behaves exactly as before this slice existed.
func TestPoolDefaultsAreUnlimited(t *testing.T) {
	p := resiliencePool(t, []string{"127.0.0.1:1"}, nil)
	pol := p.Policy()
	if pol.MaxActiveRequests() != 0 || pol.MaxActivePerBackend() != 0 || pol.MaxPendingRequests() != 0 || pol.PendingTimeout() != 0 {
		t.Fatalf("default policy is not unlimited: %+v", pol)
	}
	if pol.Bounded() {
		t.Fatal("default policy reports Bounded")
	}
	// The same backend can be picked without limit.
	for i := 0; i < 50; i++ {
		if _, err := p.Pick(); err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
	}
}

// TestPerBackendLimitIsSelectionFilter proves max_active_per_backend removes a
// saturated backend from selection instead of queueing behind it. Nested
// waiting inside selection is a deadlock generator, so the observable contract
// is that Pick fails fast with a distinct error rather than blocking.
func TestPerBackendLimitIsSelectionFilter(t *testing.T) {
	p := resiliencePool(t, []string{"127.0.0.1:1", "127.0.0.1:2"},
		&config.ResilienceConfig{MaxActivePerBackend: 2})

	picked := make([]*Backend, 0, 4)
	for i := 0; i < 4; i++ {
		b, err := p.Pick()
		if err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
		picked = append(picked, b)
	}
	// Both backends are now at 2 in flight, so the pool is at capacity.
	if _, err := p.Pick(); !errors.Is(err, ErrBackendAtCapacity) {
		t.Fatalf("pick at capacity: err = %v, want ErrBackendAtCapacity", err)
	}

	// Distinct from "no healthy backend": the two conditions call for opposite
	// operator responses, so they must not share an error.
	if errors.Is(ErrBackendAtCapacity, ErrNoAvailableBackend) {
		t.Fatal("ErrBackendAtCapacity must not match ErrNoAvailableBackend")
	}

	picked[0].Release()
	if _, err := p.Pick(); err != nil {
		t.Fatalf("pick after release: %v", err)
	}
}

// TestPerBackendLimitPrefersUnsaturatedBackend proves the filter composes with
// balancing rather than overriding it: traffic moves to the backend with room.
func TestPerBackendLimitPrefersUnsaturatedBackend(t *testing.T) {
	p := resiliencePool(t, []string{"127.0.0.1:1", "127.0.0.1:2"},
		&config.ResilienceConfig{MaxActivePerBackend: 1})

	first, err := p.Pick()
	if err != nil {
		t.Fatalf("first pick: %v", err)
	}
	second, err := p.Pick()
	if err != nil {
		t.Fatalf("second pick: %v", err)
	}
	if first.Address == second.Address {
		t.Fatalf("both picks landed on %s despite max_active_per_backend = 1", first.Address)
	}
}

// TestPoolPolicySwapPreservesCounters is the reload invariant that motivates
// keeping the policy out of upstreamMeta: swapping limits must not rebuild the
// pool, because a rebuild would discard exactly the accounting the limits
// govern.
func TestPoolPolicySwapPreservesCounters(t *testing.T) {
	p := resiliencePool(t, []string{"127.0.0.1:1"},
		&config.ResilienceConfig{MaxActiveRequests: 4})

	rel, err := p.Admission().Admit(context.Background(), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	b, err := p.Pick()
	if err != nil {
		t.Fatalf("pick: %v", err)
	}

	next, err := resilience.Resolve(resilience.Options{MaxActiveRequests: 64})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	p.SetPolicy(next)

	if got := p.Admission().Active(); got != 1 {
		t.Fatalf("active after policy swap = %d, want 1", got)
	}
	if got := b.Inflight(); got != 1 {
		t.Fatalf("backend inflight after policy swap = %d, want 1", got)
	}
	if got := p.Policy().MaxActiveRequests(); got != 64 {
		t.Fatalf("limit after swap = %d, want 64", got)
	}
	rel()
	b.Release()
}

// TestNewPoolRejectsIncoherentPolicy proves a malformed policy fails while the
// pool is being built, so a bad reload aborts instead of surfacing later as
// mysteriously rejected traffic.
func TestNewPoolRejectsIncoherentPolicy(t *testing.T) {
	_, err := NewPool(config.UpstreamConfig{
		Name:     "api",
		Servers:  []config.UpstreamServer{{Address: "127.0.0.1:1", Weight: 1}},
		MaxFails: 3,
		// A queue with no admission limit can never fill, so the pair is
		// incoherent rather than merely unusual.
		Resilience: &config.ResilienceConfig{MaxPendingRequests: 10},
	}, "http")
	if err == nil {
		t.Fatal("NewPool accepted max_pending_requests without max_active_requests")
	}
}
