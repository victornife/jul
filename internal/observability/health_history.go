// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"sort"
	"sync"
	"time"
)

// healthEventCap bounds the recent health transitions kept per backend.
const healthEventCap = 16

// flapWindow is the lookback used to decide whether a backend is flapping.
const flapWindow = 5 * time.Minute

// flapThreshold is the number of transitions within flapWindow above which a
// backend is reported as flapping.
const flapThreshold = 4

// HealthEvent is one up/down transition of a backend (Console v2 Milestone 5.5).
type HealthEvent struct {
	Time    time.Time `json:"time"`
	Healthy bool      `json:"healthy"`
}

// BackendHealthHistory summarizes the health timeline of a single backend.
type BackendHealthHistory struct {
	Pool        string        `json:"pool"`
	Backend     string        `json:"backend"`
	Healthy     bool          `json:"healthy"`
	Transitions int           `json:"transitions"`
	Flapping    bool          `json:"flapping"`
	LastUp      *time.Time    `json:"last_up,omitempty"`
	LastDown    *time.Time    `json:"last_down,omitempty"`
	Recent      []HealthEvent `json:"recent,omitempty"`
}

type backendHealth struct {
	pool        string
	backend     string
	healthy     bool
	seeded      bool
	transitions int
	lastUp      time.Time
	lastDown    time.Time
	recent      []HealthEvent
}

// healthHistoryTracker records backend up/down transitions for the Console v2
// Upstream Health History panel. It only appends on an actual state change so
// the steady stream of "still healthy" probes does not bloat the buffer. It is
// safe for concurrent use.
type healthHistoryTracker struct {
	mu       sync.Mutex
	backends map[string]*backendHealth
}

func newHealthHistoryTracker() *healthHistoryTracker {
	return &healthHistoryTracker{backends: make(map[string]*backendHealth)}
}

// record folds one health verdict in. The first verdict for a backend seeds its
// state without counting a transition; subsequent verdicts only mutate state
// when the health actually flips.
func (t *healthHistoryTracker) record(pool, backend string, healthy bool) {
	key := pool + "|" + backend
	now := time.Now().UTC()

	t.mu.Lock()
	defer t.mu.Unlock()

	bh, ok := t.backends[key]
	if !ok {
		bh = &backendHealth{pool: pool, backend: backend}
		t.backends[key] = bh
	}

	if !bh.seeded {
		bh.seeded = true
		bh.healthy = healthy
		t.touch(bh, healthy, now)
		return
	}
	if bh.healthy == healthy {
		return // no transition
	}

	bh.healthy = healthy
	bh.transitions++
	t.touch(bh, healthy, now)
	bh.recent = append(bh.recent, HealthEvent{Time: now, Healthy: healthy})
	if len(bh.recent) > healthEventCap {
		bh.recent = bh.recent[len(bh.recent)-healthEventCap:]
	}
}

func (t *healthHistoryTracker) touch(bh *backendHealth, healthy bool, now time.Time) {
	if healthy {
		bh.lastUp = now
	} else {
		bh.lastDown = now
	}
}

// snapshot returns the per-backend history, sorted by pool then backend.
func (t *healthHistoryTracker) snapshot() []BackendHealthHistory {
	t.mu.Lock()
	defer t.mu.Unlock()

	out := make([]BackendHealthHistory, 0, len(t.backends))
	cutoff := time.Now().Add(-flapWindow)
	for _, bh := range t.backends {
		h := BackendHealthHistory{
			Pool:        bh.pool,
			Backend:     bh.backend,
			Healthy:     bh.healthy,
			Transitions: bh.transitions,
		}
		recentTransitions := 0
		for _, ev := range bh.recent {
			if ev.Time.After(cutoff) {
				recentTransitions++
			}
		}
		h.Flapping = recentTransitions >= flapThreshold
		if !bh.lastUp.IsZero() {
			t := bh.lastUp
			h.LastUp = &t
		}
		if !bh.lastDown.IsZero() {
			t := bh.lastDown
			h.LastDown = &t
		}
		if len(bh.recent) > 0 {
			h.Recent = append([]HealthEvent(nil), bh.recent...)
		}
		out = append(out, h)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Pool != out[j].Pool {
			return out[i].Pool < out[j].Pool
		}
		return out[i].Backend < out[j].Backend
	})
	return out
}
