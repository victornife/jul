// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"sync"
	"sync/atomic"

	"jul/internal/affinity"
)

// Balancer selects a backend from a set of currently-available backends.
// Implementations must be safe for concurrent use.
type Balancer interface {
	pick(available []*Backend) *Backend
	// updateBackends is called after the pool's backend set changes. It lets
	// stateful strategies prune state for backends that are no longer present.
	updateBackends(backends []*Backend)
}

// newBalancer builds the balancer for a strategy. fallback is the strategy a
// consistent_hash balancer uses for a request without a usable key; it is
// ignored by every other strategy.
func newBalancer(strategy, fallback string) Balancer {
	switch strategy {
	case "least_conn":
		return &leastConn{}
	case "weighted_round_robin":
		return newWeightedRR()
	case "consistent_hash":
		return &rendezvous{fallback: newBalancer(fallback, "")}
	default: // "round_robin" and anything unrecognized
		return &roundRobin{}
	}
}

// pickFor chooses among available for one request. A hashed key is placed by
// rendezvous ranking when the balancer hashes; everything else — keyless
// requests and non-hashing strategies — takes the ordinary pick.
func pickFor(bal Balancer, available []*Backend, key affinity.Key) *Backend {
	if r, ok := bal.(*rendezvous); ok && key.Hashed() {
		return r.pickKey(available, key.Sum())
	}
	return bal.pick(available)
}

// rendezvous is strategy = "consistent_hash": weighted rendezvous hashing
// (rendezvous_v1, ADR 0021) over the eligible set.
//
// It holds no per-key or per-client state. The eligible set is computed by the
// caller from health, circuit and capacity before ranking, so affinity never
// pins a request to a backend that is out of rotation, and removing a backend
// from that set moves only the keys it held. A retry excludes the attempted
// backend and ranks again, which is exactly the next backend in the key's
// order.
type rendezvous struct {
	fallback Balancer
}

func (r *rendezvous) pick(a []*Backend) *Backend { return r.fallback.pick(a) }

func (r *rendezvous) updateBackends(bs []*Backend) { r.fallback.updateBackends(bs) }

// pickKey returns the highest-ranked backend for key. It is one pass with no
// allocation; equal weights never compute a logarithm (see affinity.Better).
func (r *rendezvous) pickKey(a []*Backend, key uint64) *Backend {
	if len(a) == 0 {
		return nil
	}
	best := a[0]
	bestC := best.affinityCandidate()
	for _, b := range a[1:] {
		c := b.affinityCandidate()
		if affinity.Better(key, c, bestC) {
			best, bestC = b, c
		}
	}
	return best
}

// roundRobin cycles through available backends in order.
type roundRobin struct {
	n atomic.Uint64
}

func (r *roundRobin) pick(a []*Backend) *Backend {
	if len(a) == 0 {
		return nil
	}
	i := r.n.Add(1) - 1
	return a[i%uint64(len(a))]
}

func (r *roundRobin) updateBackends([]*Backend) {}

// leastConn picks the available backend with the fewest in-flight requests.
type leastConn struct{}

func (l *leastConn) pick(a []*Backend) *Backend {
	var best *Backend
	var min int64
	for _, b := range a {
		c := b.Inflight()
		if best == nil || c < min {
			best = b
			min = c
		}
	}
	return best
}

func (l *leastConn) updateBackends([]*Backend) {}

// weightedRR implements smooth weighted round-robin (the algorithm NGINX uses),
// which distributes load proportional to weight while avoiding bursts.
// The per-backend currentWeight state is owned by the balancer instance so
// concurrent requests through different balancers (live pool vs snapshots,
// or snapshots across generations) do not race on shared Backend fields.
type weightedRR struct {
	mu      sync.Mutex
	weights map[*Backend]int
}

func newWeightedRR() *weightedRR {
	return &weightedRR{weights: make(map[*Backend]int)}
}

func (w *weightedRR) pick(a []*Backend) *Backend {
	if len(a) == 0 {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	total := 0
	var best *Backend
	for _, b := range a {
		weight := b.Weight()
		cw := w.weights[b] + weight
		w.weights[b] = cw
		total += weight
		if best == nil || cw > w.weights[best] {
			best = b
		}
	}
	if best != nil {
		w.weights[best] -= total
	}
	return best
}

// updateBackends resets the smooth weighted round-robin accumulator.
//
// Clearing the whole map, rather than only dropping departed backends, is what
// makes a weight change converge immediately: a surviving backend's current
// weight was accumulated under its previous weight, so carrying it forward
// would skew selection for as many picks as the old accumulator was worth. It
// also keeps the map from growing without bound under endpoint churn (R9-09).
func (w *weightedRR) updateBackends(backends []*Backend) {
	w.mu.Lock()
	defer w.mu.Unlock()
	clear(w.weights)
}

// identitySet builds a set of stable backend identities from a slice.
func identitySet(backends []*Backend) map[BackendIdentity]struct{} {
	set := make(map[BackendIdentity]struct{}, len(backends))
	for _, b := range backends {
		set[b.Identity()] = struct{}{}
	}
	return set
}
