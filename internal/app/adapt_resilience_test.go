// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"testing"
	"time"

	"jul/internal/admin"
	"jul/internal/upstream"
)

// AdaptResilience maps roughly twenty-five fields by hand across a package
// boundary that exists so internal/admin never imports the runtime. That is
// exactly the shape where a copy-paste slip — retry_backoff_max wired to
// retry_backoff_initial, say — is invisible: both sides compile, both are
// durations, and the API quietly reports the wrong limit to an operator tuning
// a pool during an incident.
//
// Every value below is therefore distinct. Equal values would let a crossed
// pair pass.
func TestAdaptResilienceMapsEveryFieldToTheRightPlace(t *testing.T) {
	openUntil := time.Date(2026, 8, 19, 20, 30, 0, 0, time.UTC)
	in := []upstream.PoolResilience{{
		Name:        "api",
		Scheme:      "https",
		Active:      11,
		Pending:     22,
		Connections: 33,
		Eligible:    2,
		ByState: map[upstream.BackendState]int{
			upstream.StateAvailable:       2,
			upstream.StateCircuitOpen:     1,
			upstream.StateCircuitHalfOpen: 0,
			upstream.StateHealthUnhealthy: 0,
			upstream.StateAtCapacity:      0,
		},
		Limits: upstream.ResilienceLimits{
			MaxActiveRequests:        101,
			MaxActivePerBackend:      102,
			MaxPendingRequests:       103,
			PendingTimeout:           1 * time.Second,
			MaxConnectionsPerBackend: 104,
			RetryAttempts:            105,
			RetryDeadline:            2 * time.Second,
			RetryBackoffInitial:      3 * time.Second,
			RetryBackoffMax:          4 * time.Second,
			RetryBudgetPercent:       106,
			CircuitMaxFails:          107,
			CircuitFailTimeout:       5 * time.Second,
			CircuitHalfOpenProbes:    108,
		},
		Budget: upstream.BudgetStatus{Percent: 10, Primaries: 1000, Retries: 90, Remaining: 13},
		Backends: []upstream.BackendResilience{
			{
				Address:  "10.0.0.1:80",
				Weight:   7,
				Inflight: 3,
				State:    upstream.StateCircuitOpen,
				CircuitStatus: upstream.CircuitStatus{
					Fails:           4,
					OpenUntil:       openUntil,
					ProbesRemaining: 5,
				},
			},
		},
	}}

	got := AdaptResilience(in)
	if len(got) != 1 {
		t.Fatalf("got %d pools, want 1", len(got))
	}
	p := got[0]

	if p.Name != "api" || p.Scheme != "https" {
		t.Errorf("identity = %q/%q, want api/https", p.Name, p.Scheme)
	}
	if p.Active != 11 || p.Pending != 22 || p.Connections != 33 || p.Eligible != 2 {
		t.Errorf("counters = %d/%d/%d/%d, want 11/22/33/2", p.Active, p.Pending, p.Connections, p.Eligible)
	}

	l := p.Limits
	ints := map[string][2]int64{
		"max_active_requests":         {l.MaxActiveRequests, 101},
		"max_active_per_backend":      {l.MaxActivePerBackend, 102},
		"max_pending_requests":        {int64(l.MaxPendingRequests), 103},
		"max_connections_per_backend": {int64(l.MaxConnectionsPerBackend), 104},
		"retry_attempts":              {int64(l.RetryAttempts), 105},
		"retry_budget_percent":        {int64(l.RetryBudgetPercent), 106},
		"circuit_max_fails":           {int64(l.CircuitMaxFails), 107},
		"circuit_half_open_probes":    {int64(l.CircuitHalfOpenProbes), 108},
	}
	for name, pair := range ints {
		if pair[0] != pair[1] {
			t.Errorf("%s = %d, want %d", name, pair[0], pair[1])
		}
	}
	durs := map[string][2]string{
		"pending_timeout":       {l.PendingTimeout, "1s"},
		"retry_deadline":        {l.RetryDeadline, "2s"},
		"retry_backoff_initial": {l.RetryBackoffInitial, "3s"},
		"retry_backoff_max":     {l.RetryBackoffMax, "4s"},
		"circuit_fail_timeout":  {l.CircuitFailTimeout, "5s"},
	}
	for name, pair := range durs {
		if pair[0] != pair[1] {
			t.Errorf("%s = %q, want %q", name, pair[0], pair[1])
		}
	}

	if p.Budget != (admin.RetryBudgetStatus{Percent: 10, Primaries: 1000, Retries: 90, Remaining: 13}) {
		t.Errorf("budget = %+v", p.Budget)
	}

	if p.ByState["available"] != 2 || p.ByState["circuit_open"] != 1 {
		t.Errorf("by_state = %+v", p.ByState)
	}
	// Zeroes must survive the map conversion: a dropped key reads as "no data"
	// to a consumer rather than "none in that state".
	if len(p.ByState) != len(upstream.BackendStates()) {
		t.Errorf("by_state has %d keys, want %d", len(p.ByState), len(upstream.BackendStates()))
	}

	if len(p.Backends) != 1 {
		t.Fatalf("got %d backends, want 1", len(p.Backends))
	}
	b := p.Backends[0]
	if b.Address != "10.0.0.1:80" || b.Weight != 7 || b.Inflight != 3 {
		t.Errorf("backend identity = %+v", b)
	}
	if b.State != string(upstream.StateCircuitOpen) {
		t.Errorf("state = %q, want %q", b.State, upstream.StateCircuitOpen)
	}
	if b.Fails != 4 || b.ProbesRemaining != 5 {
		t.Errorf("fails/probes = %d/%d, want 4/5", b.Fails, b.ProbesRemaining)
	}
	if b.OpenUntil != "2026-08-19T20:30:00Z" {
		t.Errorf("open_until = %q, want RFC 3339 UTC", b.OpenUntil)
	}
}

// Zero means "unset" throughout the resilience configuration. Rendering it as
// "0s" would read as a deliberate zero — an operator would see a pending
// timeout of zero and conclude requests are never queued.
func TestAdaptResilienceRendersUnsetDurationsAsAbsent(t *testing.T) {
	got := AdaptResilience([]upstream.PoolResilience{{Name: "api"}})
	if len(got) != 1 {
		t.Fatalf("got %d pools, want 1", len(got))
	}
	l := got[0].Limits
	for name, v := range map[string]string{
		"pending_timeout":       l.PendingTimeout,
		"retry_deadline":        l.RetryDeadline,
		"retry_backoff_initial": l.RetryBackoffInitial,
		"retry_backoff_max":     l.RetryBackoffMax,
		"circuit_fail_timeout":  l.CircuitFailTimeout,
	} {
		if v != "" {
			t.Errorf("%s = %q for an unset limit, want empty", name, v)
		}
	}
}

// A backend whose circuit is closed has no cooldown deadline. Emitting the zero
// time would put "0001-01-01T00:00:00Z" in the API and, through omitempty on a
// string, would not even be omitted.
func TestAdaptResilienceOmitsAnAbsentCooldownDeadline(t *testing.T) {
	got := AdaptResilience([]upstream.PoolResilience{{
		Name:     "api",
		Backends: []upstream.BackendResilience{{Address: "10.0.0.1:80", State: upstream.StateAvailable}},
	}})
	if len(got) != 1 || len(got[0].Backends) != 1 {
		t.Fatalf("unexpected shape: %+v", got)
	}
	if ou := got[0].Backends[0].OpenUntil; ou != "" {
		t.Errorf("open_until = %q for a closed circuit, want empty", ou)
	}
}

// An unknown pool must stay distinguishable from a pool with no backends: the
// handler turns nil into a 404, and an empty slice would become a 200.
func TestAdaptResilienceReturnsNilForNoPools(t *testing.T) {
	if got := AdaptResilience(nil); got != nil {
		t.Errorf("AdaptResilience(nil) = %+v, want nil", got)
	}
	if got := AdaptResilience([]upstream.PoolResilience{}); got != nil {
		t.Errorf("AdaptResilience(empty) = %+v, want nil", got)
	}
}
