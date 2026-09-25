// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
)

// CacheTierStats is one cache tier's live occupancy. It mirrors
// cache.TierStats without importing it, for the same reason
// UpstreamPoolStats mirrors upstream.PoolStats (see resilience_metrics.go):
// the callback convention keeps internal/cache from depending on this package
// and this package from depending on that one.
type CacheTierStats struct {
	// Tier is a closed, bounded label value: "memory" or "disk".
	Tier      string
	Bytes     int64
	MaxBytes  int64
	Entries   int
	Evictions int64
}

// CacheStatsSource returns the live occupancy of every configured cache tier.
// It is called once per scrape. The disk tier is simply absent from the
// returned slice when no disk_path is configured.
type CacheStatsSource func() []CacheTierStats

// cacheCollector exports the live cache-occupancy gauges by reading them at
// scrape time (JUL-AUD-005), for the same reason resilienceCollector does:
// a per-request gauge write would tax the cache's hottest path, and reading at
// scrape time is self-correcting rather than accumulating drift.
type cacheCollector struct {
	source atomic.Pointer[CacheStatsSource]

	bytes     *prometheus.Desc
	maxBytes  *prometheus.Desc
	entries   *prometheus.Desc
	evictions *prometheus.Desc
}

func newCacheCollector() *cacheCollector {
	return &cacheCollector{
		bytes: prometheus.NewDesc("jul_cache_bytes",
			"Current bytes occupied by a cache tier, labeled by tier (memory/disk).",
			[]string{"tier"}, nil),
		maxBytes: prometheus.NewDesc("jul_cache_max_bytes",
			"Configured byte cap for a cache tier, labeled by tier (memory/disk).",
			[]string{"tier"}, nil),
		entries: prometheus.NewDesc("jul_cache_entries",
			"Current entry count in a cache tier, labeled by tier (memory/disk).",
			[]string{"tier"}, nil),
		evictions: prometheus.NewDesc("jul_cache_evictions_total",
			"Cumulative LRU-capacity evictions from a cache tier since startup, labeled by tier (memory/disk). Explicit invalidation is not an eviction and is not counted here.",
			[]string{"tier"}, nil),
	}
}

// SetCacheStatsSource wires the live-state reader. The app supplies it after
// building the process-lifetime Cache; until then the gauges export nothing,
// which is correct — caching may be disabled.
func (m *Metrics) SetCacheStatsSource(src CacheStatsSource) {
	m.cache.source.Store(&src)
}

// cacheTierSnapshot reads the same live-state source the Prometheus collector
// uses, but directly, for StatsSnapshot's cache-occupancy cards (#431). Cache
// tier reads are cheap (a mutex-guarded size/max/len/count), unlike upstream
// Resilience(), so reusing the scrape source here needs no separate capacity
// source the way upstream pools do.
func (m *Metrics) cacheTierSnapshot() []CacheTierStats {
	src := m.cache.source.Load()
	if src == nil {
		return nil
	}
	return (*src)()
}

func (c *cacheCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.bytes
	ch <- c.maxBytes
	ch <- c.entries
	ch <- c.evictions
}

func (c *cacheCollector) Collect(ch chan<- prometheus.Metric) {
	src := c.source.Load()
	if src == nil {
		return
	}
	for _, s := range (*src)() {
		ch <- prometheus.MustNewConstMetric(c.bytes, prometheus.GaugeValue, float64(s.Bytes), s.Tier)
		ch <- prometheus.MustNewConstMetric(c.maxBytes, prometheus.GaugeValue, float64(s.MaxBytes), s.Tier)
		ch <- prometheus.MustNewConstMetric(c.entries, prometheus.GaugeValue, float64(s.Entries), s.Tier)
		ch <- prometheus.MustNewConstMetric(c.evictions, prometheus.CounterValue, float64(s.Evictions), s.Tier)
	}
}
