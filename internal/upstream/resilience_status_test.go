// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"testing"
	"time"
)

func TestCircuitStatusReportsTheOpenDeadlineAndProbeAllowance(t *testing.T) {
	now := time.Unix(1000, 0)
	c := newCircuit(1, 30*time.Second, 2)
	c.now = func() time.Time { return now }

	if st := c.status(); st.State != StateAvailable {
		t.Fatalf("fresh circuit state = %q, want available", st.State)
	}

	// Trip it.
	c.failure(c.admit())

	st := c.status()
	if st.State != StateCircuitOpen {
		t.Fatalf("state = %q, want circuit_open", st.State)
	}
	if want := now.Add(30 * time.Second); !st.OpenUntil.Equal(want) {
		t.Errorf("OpenUntil = %v, want %v", st.OpenUntil, want)
	}
	// An open circuit has no probes to offer; reporting a probe allowance here
	// would tell an operator recovery was under way when nothing is being sent.
	if st.ProbesRemaining != 0 {
		t.Errorf("ProbesRemaining = %d while open, want 0", st.ProbesRemaining)
	}
	if st.MaxFails != 1 || st.FailTimeout != 30*time.Second {
		t.Errorf("limits = %d/%v, want 1/30s", st.MaxFails, st.FailTimeout)
	}

	// Let the cooldown elapse: the next request becomes a probe.
	now = now.Add(31 * time.Second)
	st = c.status()
	if st.State != StateCircuitHalfOpen {
		t.Fatalf("state after cooldown = %q, want circuit_half_open", st.State)
	}
	if st.ProbesRemaining != 2 {
		t.Errorf("ProbesRemaining = %d, want the full allowance of 2", st.ProbesRemaining)
	}
	// A deadline that has passed must not be reported: an operator reading it
	// would think the backend was still in cooldown.
	if !st.OpenUntil.IsZero() {
		t.Errorf("OpenUntil = %v after the cooldown elapsed, want zero", st.OpenUntil)
	}

	// Taking a probe consumes allowance.
	c.admit()
	if st := c.status(); st.ProbesRemaining != 1 {
		t.Errorf("ProbesRemaining = %d after one probe, want 1", st.ProbesRemaining)
	}
}

// A backend ejected by an active health check is out of rotation regardless of
// what its circuit thinks. Reporting circuit_open's deadline for it would tell
// an operator to wait for a cooldown that is not what is holding it out.
func TestCircuitStatusIsOverriddenByActiveHealth(t *testing.T) {
	p := testPool(t, "127.0.0.1:1")
	b := p.Backends()[0]
	b.setActiveHealthy(false)

	st := b.CircuitStatus()
	if st.State != StateHealthUnhealthy {
		t.Errorf("State = %q, want health_unhealthy", st.State)
	}
	if !st.OpenUntil.IsZero() || st.ProbesRemaining != 0 {
		t.Errorf("circuit deadline reported for a health-ejected backend: %+v", st)
	}
	// Backend.State and Backend.CircuitStatus must never disagree about which
	// condition is keeping a backend out.
	if got := b.State(); got != st.State {
		t.Errorf("State() = %q but CircuitStatus().State = %q", got, st.State)
	}
}

func TestBudgetStatusDoesNotConsumeTheAllowance(t *testing.T) {
	b := NewBudget(50)
	for range 10 {
		b.Primary()
	}

	before := b.Status()
	if before.Percent != 50 {
		t.Fatalf("Percent = %d, want 50", before.Percent)
	}
	if before.Primaries != 10 {
		t.Fatalf("Primaries = %d, want 10", before.Primaries)
	}

	// Reading twice must not change anything: an operator refreshing a
	// diagnostic page would otherwise deny a real retry on every load.
	after := b.Status()
	if after != before {
		t.Fatalf("Status changed between reads: %+v then %+v", before, after)
	}

	// The reported remaining must match what Allow actually grants.
	granted := 0
	for b.Allow() {
		granted++
		if granted > 1000 {
			t.Fatal("Allow never denied")
		}
	}
	if int64(granted) != before.Remaining {
		t.Errorf("Status reported %d remaining, Allow granted %d", before.Remaining, granted)
	}
	if end := b.Status(); end.Remaining != 0 {
		t.Errorf("Remaining = %d after exhaustion, want 0", end.Remaining)
	}
}

func TestBudgetStatusOnAnUnbudgetedPool(t *testing.T) {
	b := NewBudget(0)
	b.Primary()
	st := b.Status()
	if st.Percent != 0 {
		t.Errorf("Percent = %d, want 0", st.Percent)
	}
	// Unbudgeted means Allow always grants, so a finite Remaining would be a
	// lie. Zero here reads as "not applicable", which Percent==0 disambiguates.
	if st.Remaining != 0 || st.Primaries != 0 || st.Retries != 0 {
		t.Errorf("unbudgeted status carries counts: %+v", st)
	}
	if !b.Allow() {
		t.Error("unbudgeted Allow denied a retry")
	}
}

func TestPoolResilienceReportsLimitsCountsAndEveryBackend(t *testing.T) {
	p := testPool(t, "127.0.0.1:1", "127.0.0.1:2")

	r := p.Resilience()
	if r.Name != p.name {
		t.Errorf("Name = %q, want %q", r.Name, p.name)
	}
	if len(r.Backends) != 2 {
		t.Fatalf("reported %d backends, want 2", len(r.Backends))
	}
	if r.Eligible != 2 {
		t.Errorf("Eligible = %d, want 2", r.Eligible)
	}
	// Every state must be present, including the zeroes: a missing key reads as
	// "no data" to a consumer rather than "none in that state".
	if len(r.ByState) != len(BackendStates()) {
		t.Errorf("ByState has %d keys, want %d", len(r.ByState), len(BackendStates()))
	}
	if r.ByState[StateAvailable] != 2 {
		t.Errorf("ByState[available] = %d, want 2", r.ByState[StateAvailable])
	}
	for _, b := range r.Backends {
		if b.Address == "" {
			t.Error("backend reported with no address")
		}
		if b.State != StateAvailable {
			t.Errorf("backend %s state = %q, want available", b.Address, b.State)
		}
	}
}

// ByState is what the Console and the metrics agree on, so it must always
// account for exactly the backends the same call reports individually.
func TestPoolResilienceStateCountsMatchItsOwnBackendList(t *testing.T) {
	p := testPool(t, "127.0.0.1:1", "127.0.0.1:2", "127.0.0.1:3")
	p.Backends()[0].setActiveHealthy(false)

	r := p.Resilience()
	counted := map[BackendState]int{}
	for _, b := range r.Backends {
		counted[b.State]++
	}
	for _, st := range BackendStates() {
		if r.ByState[st] != counted[st] {
			t.Errorf("ByState[%s] = %d but %d backends report it", st, r.ByState[st], counted[st])
		}
	}
	if r.Eligible != counted[StateAvailable] {
		t.Errorf("Eligible = %d, available backends = %d", r.Eligible, counted[StateAvailable])
	}
}

// A name with no live pool must be distinguishable from a pool with no
// backends: the first is a 404, the second is a real pool in trouble.
func TestRegistryResilienceReturnsNilForAnUnknownPool(t *testing.T) {
	r := NewRegistry(RegistryOptions{})
	if got := r.Resilience("nope"); got != nil {
		t.Errorf("Resilience of an unknown pool = %+v, want nil", got)
	}
}
