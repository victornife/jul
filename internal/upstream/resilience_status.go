// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import "time"

// effectiveState is the one place capacity is folded into a backend's state.
//
// Backend.State cannot answer it alone: capacity is a pool-level limit and the
// backend does not know it. Three callers need the same answer — the scrape-time
// stats, the registry snapshot the Console renders, and the resilience API — and
// when this logic was written out at each of them they were one edit away from
// disagreeing about whether a full backend is "available".
func effectiveState(b *Backend, maxActivePerBackend int64) BackendState {
	st := b.State()
	if st == StateAvailable && maxActivePerBackend > 0 && b.Inflight() >= maxActivePerBackend {
		return StateAtCapacity
	}
	return st
}

// ResilienceLimits are the bounds actually in force for a pool, which is not
// necessarily what the file on disk says: a reload the operator has not seen,
// or a patch applied by someone else, changes these and nothing else tells them.
type ResilienceLimits struct {
	MaxActiveRequests        int64
	MaxActivePerBackend      int64
	MaxPendingRequests       int
	PendingTimeout           time.Duration
	MaxConnectionsPerBackend int
	RetryAttempts            int
	RetryDeadline            time.Duration
	RetryBackoffInitial      time.Duration
	RetryBackoffMax          time.Duration
	RetryBudgetPercent       int
	CircuitMaxFails          int
	CircuitFailTimeout       time.Duration
	CircuitHalfOpenProbes    int
}

// BackendResilience is one backend's live resilience state.
//
// This is the surface the per-backend metric label was removed in favour of: an
// address here costs one field in a response nobody scrapes, where the same
// address as a metric label cost a time series per pod, forever.
type BackendResilience struct {
	Address  string
	Weight   int
	Inflight int64
	// State folds in capacity and active health, so it can differ from
	// Circuit.State. Circuit.State is what the breaker itself thinks.
	State BackendState
	CircuitStatus
}

// PoolResilience is everything the resilience API reports for one pool.
type PoolResilience struct {
	Name   string
	Scheme string
	Limits ResilienceLimits

	Active      int64
	Pending     int64
	Connections int64
	// Eligible counts backends that could take a request right now.
	Eligible int
	ByState  map[BackendState]int

	Backends []BackendResilience
	Budget   BudgetStatus
}

// Resilience reports this pool's live resilience state in full.
func (p *Pool) Resilience() PoolResilience {
	pol := p.Policy()
	lim := ResilienceLimits{
		MaxActiveRequests:        pol.MaxActiveRequests(),
		MaxActivePerBackend:      pol.MaxActivePerBackend(),
		MaxPendingRequests:       pol.MaxPendingRequests(),
		PendingTimeout:           pol.PendingTimeout(),
		MaxConnectionsPerBackend: pol.MaxConnectionsPerBackend(),
		RetryAttempts:            pol.RetryAttempts(),
		RetryDeadline:            pol.RetryDeadline(),
		RetryBackoffInitial:      pol.RetryBackoffInitial(),
		RetryBackoffMax:          pol.RetryBackoffMax(),
		RetryBudgetPercent:       pol.RetryBudgetPercent(),
	}
	// Circuit limits are not on the policy: they are swapped separately so a
	// discovery refresh can rebuild backends without racing a reload.
	if cp := p.circuit.Load(); cp != nil {
		lim.CircuitMaxFails = cp.maxFails
		lim.CircuitFailTimeout = cp.failTimeout
		lim.CircuitHalfOpenProbes = cp.halfOpenProbes
	}
	out := PoolResilience{
		Name:        p.name,
		Active:      p.admission.Active(),
		Pending:     p.admission.Pending(),
		Connections: p.conns.Load(),
		ByState:     make(map[BackendState]int, len(BackendStates())),
		Budget:      p.budget.Status(),
		Limits:      lim,
	}
	for _, st := range BackendStates() {
		out.ByState[st] = 0
	}
	perBackend := lim.MaxActivePerBackend
	for _, b := range p.Backends() {
		st := effectiveState(b, perBackend)
		out.ByState[st]++
		if st == StateAvailable {
			out.Eligible++
		}
		out.Backends = append(out.Backends, BackendResilience{
			Address:       b.Address,
			Weight:        b.Weight(),
			Inflight:      b.Inflight(),
			State:         st,
			CircuitStatus: b.CircuitStatus(),
		})
	}
	return out
}

// Resilience reports live resilience state for every pool serving under name,
// one entry per scheme. A name with no live pool returns nil, which the caller
// renders as a 404 rather than an empty pool that never existed.
func (r *Registry) Resilience(name string) []PoolResilience {
	r.mu.Lock()
	entries := make([]*poolEntry, 0, 2)
	schemes := make([]string, 0, 2)
	for key, e := range r.live {
		if key.name == name {
			entries = append(entries, e)
			schemes = append(schemes, key.scheme)
		}
	}
	r.mu.Unlock()

	// The pools are read outside the registry lock: each is independently
	// concurrency-safe, and holding the registry lock across every backend of
	// every pool would block reload behind a diagnostic request.
	out := make([]PoolResilience, 0, len(entries))
	for i, e := range entries {
		pr := e.pool.Resilience()
		pr.Scheme = schemes[i]
		out = append(out, pr)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
