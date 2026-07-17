// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"sync"
	"sync/atomic"
)

// Balancer selects a backend from a set of currently-available backends.
// Implementations must be safe for concurrent use.
type Balancer interface {
	pick(available []*Backend) *Backend
}

func newBalancer(strategy string) Balancer {
	switch strategy {
	case "least_conn":
		return &leastConn{}
	case "weighted_round_robin":
		return newWeightedRR()
	default: // "round_robin" and anything unrecognized
		return &roundRobin{}
	}
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
		cw := w.weights[b] + b.Weight
		w.weights[b] = cw
		total += b.Weight
		if best == nil || cw > w.weights[best] {
			best = b
		}
	}
	if best != nil {
		w.weights[best] -= total
	}
	return best
}
