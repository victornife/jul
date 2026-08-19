// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import "sync"

// PoolStats is one pool's live resilience state, read at scrape time.
//
// The gauges it feeds are level-triggered on purpose. Pushing them from the
// admission path would put a metric write on the hottest code in the proxy for
// numbers nobody reads between scrapes, and an accumulator that misses one
// event stays wrong until the process restarts.
type PoolStats struct {
	Name string

	Active      int64
	Pending     int64
	Connections int64

	// Eligible counts backends that could take a request now: active-healthy
	// and not circuit-open. It is deliberately not "healthy", which answers a
	// different question and is reported separately.
	Eligible int

	// ByState counts backends per BackendState. Per-backend detail is a runtime
	// API concern; a series per backend address is exactly the unbounded label
	// this subsystem removed.
	ByState map[BackendState]int
}

// Stats reports this pool's live state.
func (p *Pool) Stats() PoolStats {
	s := PoolStats{
		Name:        p.name,
		Active:      p.admission.Active(),
		Pending:     p.admission.Pending(),
		Connections: p.conns.Load(),
		ByState:     make(map[BackendState]int, 5),
	}
	// Seed every state so a pool with no backends in a state exports a zero
	// rather than dropping the series, which would read as "no data" on a
	// dashboard instead of "none".
	for _, st := range BackendStates() {
		s.ByState[st] = 0
	}
	perBackend := p.Policy().MaxActivePerBackend()
	for _, b := range p.Backends() {
		st := effectiveState(b, perBackend)
		s.ByState[st]++
		if st == StateAvailable {
			s.Eligible++
		}
	}
	return s
}

// BackendStates returns every backend state, so a consumer enumerating them
// cannot drift from the set.
func BackendStates() []BackendState {
	return []BackendState{
		StateAvailable,
		StateCircuitOpen,
		StateCircuitHalfOpen,
		StateHealthUnhealthy,
		StateAtCapacity,
	}
}

// TrackConn records a physical connection opened to this pool's backends and
// returns the function that records its close.
//
// It exists because net/http enforces MaxConnsPerHost internally and exposes no
// live count, so the only honest source is Jul's own dialer. The returned
// function is idempotent: a transport may close a connection more than once and
// a counter that went negative would be worse than no counter.
func (p *Pool) TrackConn() func() {
	p.conns.Add(1)
	var once sync.Once
	return func() { once.Do(func() { p.conns.Add(-1) }) }
}
