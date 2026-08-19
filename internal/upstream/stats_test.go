// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
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
