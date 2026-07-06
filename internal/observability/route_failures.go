// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"sort"
	"sync"
)

// routeFailureCap bounds how many distinct paths the failing-route tracker
// retains before folding overflow into "(other)". Like the traffic-source
// rollups, this keeps memory and the exported projection bounded regardless of
// path cardinality (Console v2 Milestone 5.2).
const routeFailureCap = 128

// routeLatencyReservoir bounds the number of recent latencies kept per path for
// the approximate p95 figure. A small ring keeps memory flat.
const routeLatencyReservoir = 64

// RouteFailure summarizes recent request outcomes for one path so operators can
// see which routes are failing (Console v2 Milestone 5.2).
type RouteFailure struct {
	Path           string  `json:"path"`
	Total          float64 `json:"total"`
	Status4xx      float64 `json:"status_4xx"`
	Status5xx      float64 `json:"status_5xx"`
	ErrorRate      float64 `json:"error_rate"`
	LatencyP95Ms   float64 `json:"latency_p95_ms"`
	LastErrorClass string  `json:"last_error_class,omitempty"`
}

type routeStat struct {
	total      float64
	status4xx  float64
	status5xx  float64
	lastErr    string
	latencies  []float64
	latencyPos int
}

// routeFailureTracker maintains a bounded top-N rollup of per-path request
// outcomes. It is safe for concurrent use.
type routeFailureTracker struct {
	mu      sync.Mutex
	maxKeys int
	stats   map[string]*routeStat
}

func newRouteFailureTracker(maxKeys int) *routeFailureTracker {
	if maxKeys <= 0 {
		maxKeys = routeFailureCap
	}
	return &routeFailureTracker{maxKeys: maxKeys, stats: make(map[string]*routeStat)}
}

// record folds one request outcome into the rollup. path is normalized and
// length-capped; status is the HTTP status code; durationMs is the latency.
func (t *routeFailureTracker) record(path string, status int, durationMs float64) {
	key := sanitizePath(path)

	t.mu.Lock()
	defer t.mu.Unlock()

	st, ok := t.stats[key]
	if !ok {
		if len(t.stats) >= t.maxKeys {
			key = "(other)"
			st = t.stats[key]
		}
		if st == nil {
			st = &routeStat{latencies: make([]float64, 0, routeLatencyReservoir)}
			t.stats[key] = st
		}
	}

	st.total++
	switch {
	case status >= 500:
		st.status5xx++
		st.lastErr = "5xx"
	case status >= 400:
		st.status4xx++
		st.lastErr = "4xx"
	}

	if len(st.latencies) < routeLatencyReservoir {
		st.latencies = append(st.latencies, durationMs)
	} else {
		st.latencies[st.latencyPos] = durationMs
		st.latencyPos = (st.latencyPos + 1) % routeLatencyReservoir
	}
}

// snapshot returns the paths ranked by failure weight (5xx first, then 4xx,
// then volume), limited to the top n entries. Paths with no failures are
// excluded so the panel stays focused on problems.
func (t *routeFailureTracker) snapshot(n int) []RouteFailure {
	t.mu.Lock()
	defer t.mu.Unlock()

	out := make([]RouteFailure, 0, len(t.stats))
	for path, st := range t.stats {
		if st.status4xx == 0 && st.status5xx == 0 {
			continue
		}
		rf := RouteFailure{
			Path:           path,
			Total:          st.total,
			Status4xx:      st.status4xx,
			Status5xx:      st.status5xx,
			LastErrorClass: st.lastErr,
			LatencyP95Ms:   percentile(st.latencies, 0.95),
		}
		if st.total > 0 {
			rf.ErrorRate = (st.status4xx + st.status5xx) / st.total
		}
		out = append(out, rf)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Status5xx != out[j].Status5xx {
			return out[i].Status5xx > out[j].Status5xx
		}
		if out[i].Status4xx != out[j].Status4xx {
			return out[i].Status4xx > out[j].Status4xx
		}
		if out[i].Total != out[j].Total {
			return out[i].Total > out[j].Total
		}
		return out[i].Path < out[j].Path
	})

	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// percentile returns the q-quantile (0..1) of values using nearest-rank on a
// sorted copy. It returns 0 for an empty input.
func percentile(values []float64, q float64) float64 {
	if len(values) == 0 {
		return 0
	}
	cp := make([]float64, len(values))
	copy(cp, values)
	sort.Float64s(cp)
	idx := int(q * float64(len(cp)-1))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(cp) {
		idx = len(cp) - 1
	}
	return cp[idx]
}
