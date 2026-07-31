// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"sort"
	"sync"
)

// EgressBlockedCount is a bounded, secret-free tally of egress allow-list blocks
// by subsystem and reason for the Console Security panel. It carries no
// destination host or IP.
type EgressBlockedCount struct {
	Subsystem string `json:"subsystem"`
	Reason    string `json:"reason"`
	Count     int    `json:"count"`
}

type egressBlockKey struct {
	subsystem string
	reason    string
}

// egressBlockTracker aggregates egress block decisions by (subsystem, reason).
// The label space is bounded — a handful of subsystems times five reasons — so
// the map stays small and needs no eviction; it holds no host or IP. It is safe
// for concurrent use.
type egressBlockTracker struct {
	mu     sync.Mutex
	counts map[egressBlockKey]int
}

func newEgressBlockTracker() *egressBlockTracker {
	return &egressBlockTracker{counts: make(map[egressBlockKey]int)}
}

func (t *egressBlockTracker) add(subsystem, reason string) {
	t.mu.Lock()
	t.counts[egressBlockKey{subsystem: subsystem, reason: reason}]++
	t.mu.Unlock()
}

func (t *egressBlockTracker) snapshot() []EgressBlockedCount {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]EgressBlockedCount, 0, len(t.counts))
	for k, c := range t.counts {
		out = append(out, EgressBlockedCount{Subsystem: k.subsystem, Reason: k.reason, Count: c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Subsystem != out[j].Subsystem {
			return out[i].Subsystem < out[j].Subsystem
		}
		return out[i].Reason < out[j].Reason
	})
	return out
}
