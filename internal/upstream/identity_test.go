// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"testing"
	"time"

	"jul/internal/config"
)

// TestBackendStateFollowsTheWorkloadNotTheAddress is the property this slice
// exists for. Kubernetes recycles pod IPs within seconds, so without a logical
// identity a replacement pod inherits the dead one's failure history and starts
// life partway to being taken out of rotation — for failures it never caused.
func TestBackendStateFollowsTheWorkloadNotTheAddress(t *testing.T) {
	p := identityPool(t)

	p.UpdateTargets([]Target{{Address: "10.0.0.1:80", ID: "pod-a"}})
	first := p.Backends()[0]
	// Give it a failure history worth inheriting.
	p.MarkFailure(admitOn(t, first))
	if first.circuit.fails.Load() == 0 {
		t.Fatal("precondition: the first backend should carry a failure")
	}

	t.Run("the same workload keeps its state", func(t *testing.T) {
		p.UpdateTargets([]Target{{Address: "10.0.0.1:80", ID: "pod-a"}})
		got := p.Backends()[0]
		if got != first {
			t.Fatal("a refresh with an unchanged identity replaced the backend")
		}
		if got.circuit.fails.Load() == 0 {
			t.Fatal("the surviving backend lost its failure history")
		}
	})

	t.Run("a replacement at the same address starts clean", func(t *testing.T) {
		p.UpdateTargets([]Target{{Address: "10.0.0.1:80", ID: "pod-b"}})
		got := p.Backends()[0]
		if got == first {
			t.Fatal("a new pod at a recycled address reused the dead pod's backend")
		}
		if got.circuit.fails.Load() != 0 {
			t.Fatalf("the replacement inherited %d failures it never caused", got.circuit.fails.Load())
		}
		if !got.Available() {
			t.Fatal("the replacement arrived already out of rotation")
		}
	})
}

// TestBackendReuseWithoutProviderIdentity pins that nothing changes for the
// providers that have no identity to offer — DNS, DNS SRV and static servers
// keep reusing by address, which is all they have.
func TestBackendReuseWithoutProviderIdentity(t *testing.T) {
	p := identityPool(t)

	p.UpdateTargets([]Target{{Address: "10.0.0.1:80"}})
	first := p.Backends()[0]
	p.MarkFailure(admitOn(t, first))

	p.UpdateTargets([]Target{{Address: "10.0.0.1:80"}})
	if got := p.Backends()[0]; got != first {
		t.Fatal("an address-only refresh replaced the backend")
	}
	if got := p.Backends()[0].circuit.fails.Load(); got == 0 {
		t.Fatal("an address-only refresh discarded the failure history")
	}
}

// TestBackendIdentityRemainsAddressBased pins the deliberate split between the
// two keys. BackendIdentity answers "where do I dial", so two workloads at one
// address are still one place to connect; the logical identity answers "whose
// state is this". Collapsing them would break retry exclusion, which must not
// re-try an address it has already tried just because the pod behind it
// changed mid-request.
func TestBackendIdentityRemainsAddressBased(t *testing.T) {
	a := newBackendFor("10.0.0.1:80", 1, "pod-a", "http", circuitParams{maxFails: 1, failTimeout: time.Second, halfOpenProbes: 1})
	b := newBackendFor("10.0.0.1:80", 1, "pod-b", "http", circuitParams{maxFails: 1, failTimeout: time.Second, halfOpenProbes: 1})

	if a.Identity() != b.Identity() {
		t.Fatalf("dial identities differ (%v vs %v); retry exclusion would stop excluding a tried address", a.Identity(), b.Identity())
	}
	if a.LogicalID() == b.LogicalID() {
		t.Fatal("logical identities must differ")
	}
}

// TestDiscoveryChurnMatrix walks add, remove and re-add, which is the sequence
// a rolling deployment actually produces.
func TestDiscoveryChurnMatrix(t *testing.T) {
	p := identityPool(t)

	p.UpdateTargets([]Target{
		{Address: "10.0.0.1:80", ID: "pod-a"},
		{Address: "10.0.0.2:80", ID: "pod-b"},
	})
	byID := func() map[string]*Backend {
		m := map[string]*Backend{}
		for _, b := range p.Backends() {
			m[b.LogicalID()] = b
		}
		return m
	}
	before := byID()
	p.MarkFailure(admitOn(t, before["pod-b"]))

	// Remove pod-a: pod-b survives untouched.
	p.UpdateTargets([]Target{{Address: "10.0.0.2:80", ID: "pod-b"}})
	if got := byID(); got["pod-b"] != before["pod-b"] {
		t.Fatal("removing one backend replaced another")
	}
	if len(p.Backends()) != 1 {
		t.Fatalf("%d backends after removal, want 1", len(p.Backends()))
	}

	// Re-add pod-a: a genuinely new backend, and pod-b still survives.
	p.UpdateTargets([]Target{
		{Address: "10.0.0.1:80", ID: "pod-a"},
		{Address: "10.0.0.2:80", ID: "pod-b"},
	})
	after := byID()
	if after["pod-a"] == before["pod-a"] {
		t.Fatal("a re-added backend reused the object it had before it left; its state should be fresh")
	}
	if after["pod-b"] != before["pod-b"] {
		t.Fatal("re-adding one backend replaced another")
	}
	if after["pod-b"].circuit.fails.Load() == 0 {
		t.Fatal("the surviving backend lost its failure history across churn")
	}
}

// TestWeightChangeStillAppliedInPlace pins that the identity key did not
// regress what #285 fixed: a discovery weight flap must not discard state.
func TestWeightChangeStillAppliedInPlace(t *testing.T) {
	p := identityPool(t)
	p.UpdateTargets([]Target{{Address: "10.0.0.1:80", ID: "pod-a", Weight: 1}})
	first := p.Backends()[0]
	p.MarkFailure(admitOn(t, first))

	p.UpdateTargets([]Target{{Address: "10.0.0.1:80", ID: "pod-a", Weight: 7}})
	got := p.Backends()[0]
	if got != first {
		t.Fatal("a weight change replaced the backend")
	}
	if got.Weight() != 7 {
		t.Fatalf("weight = %d, want 7 applied in place", got.Weight())
	}
	if got.circuit.fails.Load() == 0 {
		t.Fatal("a weight change discarded the failure history")
	}
}

func identityPool(t *testing.T) *Pool {
	t.Helper()
	p, err := NewPool(config.UpstreamConfig{
		Name:        "identity",
		Strategy:    "round_robin",
		Servers:     []config.UpstreamServer{{Address: "10.0.0.1:80", Weight: 1}},
		MaxFails:    1000,
		FailTimeout: config.Duration(time.Minute),
	}, "http")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}
