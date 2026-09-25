// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
)

// UpstreamPoolStats is one pool's live resilience state. It mirrors
// upstream.PoolStats without importing it: the callback convention keeps
// internal/upstream from depending on this package and this package from
// depending on that one.
type UpstreamPoolStats struct {
	Name        string
	Active      int64
	Pending     int64
	Connections int64
	Eligible    int
	// ByState maps a bounded backend-state string to a count of backends.
	ByState map[string]int

	// MaxActive/MaxPending are the pool's configured admission limits (0 means
	// unbounded — no meaningful denominator). They are Go-level state read
	// directly from the pool's resolved policy, not reconstructed from a
	// Prometheus metric, so #431's Overview pressure summary can compute a
	// truthful current/limit ratio without a new metric family (#431 §16).
	MaxActive  int64
	MaxPending int
	// BudgetPercent is the configured retry-budget allowance (0 = unbudgeted).
	// BudgetRemaining is how many more retries the budget would grant right
	// now; it can be legitimately 0 while BudgetPercent is non-zero, which is
	// the "retry budget exhausted" condition #431 asks to surface.
	BudgetPercent   int
	BudgetRemaining int64
}

// UpstreamStatsSource returns the live state of every serving pool. It is called
// once per scrape.
type UpstreamStatsSource func() []UpstreamPoolStats

// resilienceCollector exports the live gauges by reading them at scrape time.
//
// They are not pushed from the request path on purpose. Admission is on the
// hottest code in the proxy, and a gauge write per admit and per release would
// cost every request to keep a number nobody reads between scrapes. Reading at
// scrape time is also self-correcting: a pushed gauge that misses one decrement
// stays wrong until the process restarts.
//
// It also means a retired pool simply stops being reported, with no series to
// delete.
type resilienceCollector struct {
	source atomic.Pointer[UpstreamStatsSource]

	active      *prometheus.Desc
	pending     *prometheus.Desc
	connections *prometheus.Desc
	eligible    *prometheus.Desc
	circuit     *prometheus.Desc
}

func newResilienceCollector() *resilienceCollector {
	return &resilienceCollector{
		active: prometheus.NewDesc("jul_upstream_active_requests",
			"Admitted logical requests, streams and connections currently in flight for a pool, labeled by pool.",
			[]string{"pool"}, nil),
		pending: prometheus.NewDesc("jul_upstream_pending_requests",
			"Requests currently waiting for an admission slot, labeled by pool.",
			[]string{"pool"}, nil),
		connections: prometheus.NewDesc("jul_upstream_connections",
			"Physical connections currently open to a pool's backends, labeled by pool.",
			[]string{"pool"}, nil),
		eligible: prometheus.NewDesc("jul_upstream_backends_eligible",
			"Backends currently able to take a request, labeled by pool.",
			[]string{"pool"}, nil),
		circuit: prometheus.NewDesc("jul_upstream_circuit_state",
			"Backends per circuit state, labeled by pool and state (available/circuit_open/circuit_half_open/health_unhealthy/at_capacity).",
			[]string{"pool", "state"}, nil),
	}
}

// SetUpstreamStatsSource wires the live-state reader. The app supplies it after
// building the pool registry; until then the gauges export nothing, which is
// correct — there are no pools yet.
func (m *Metrics) SetUpstreamStatsSource(src UpstreamStatsSource) {
	m.resilience.source.Store(&src)
}

// SetUpstreamCapacitySource wires a second, independent live-state reader used
// only by Snapshot's capacity summary (#431). It is intentionally separate
// from SetUpstreamStatsSource: Resilience()-based state is materially heavier
// than the plain counters the Prometheus gauges use, so it must never be read
// on every scrape (#431 §34) — only when a StatsSnapshot is actually built.
func (m *Metrics) SetUpstreamCapacitySource(src UpstreamStatsSource) {
	m.upstreamCapacity.Store(&src)
}

// upstreamCapacitySnapshot reads the capacity-only source, or nil before one
// is wired.
func (m *Metrics) upstreamCapacitySnapshot() []UpstreamPoolStats {
	src := m.upstreamCapacity.Load()
	if src == nil {
		return nil
	}
	return (*src)()
}

func (c *resilienceCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.active
	ch <- c.pending
	ch <- c.connections
	ch <- c.eligible
	ch <- c.circuit
}

func (c *resilienceCollector) Collect(ch chan<- prometheus.Metric) {
	src := c.source.Load()
	if src == nil {
		return
	}
	for _, s := range (*src)() {
		ch <- prometheus.MustNewConstMetric(c.active, prometheus.GaugeValue, float64(s.Active), s.Name)
		ch <- prometheus.MustNewConstMetric(c.pending, prometheus.GaugeValue, float64(s.Pending), s.Name)
		ch <- prometheus.MustNewConstMetric(c.connections, prometheus.GaugeValue, float64(s.Connections), s.Name)
		ch <- prometheus.MustNewConstMetric(c.eligible, prometheus.GaugeValue, float64(s.Eligible), s.Name)
		for state, n := range s.ByState {
			ch <- prometheus.MustNewConstMetric(c.circuit, prometheus.GaugeValue, float64(n), s.Name, state)
		}
	}
}
