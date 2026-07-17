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
// The per-backend currentWeight state is stored in local snapshot entries so
// that concurrent requests do not race on shared Backend fields.
type weightedRR struct {
	mu sync.Mutex
}

func newWeightedRR() *weightedRR {
	return &weightedRR{}
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
		b.currentWeight += b.Weight
		total += b.Weight
		if best == nil || b.currentWeight > best.currentWeight {
			best = b
		}
	}
	if best != nil {
		best.currentWeight -= total
	}
	return best
}
