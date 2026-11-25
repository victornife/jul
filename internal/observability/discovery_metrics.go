// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// DiscoveryFreshness is the bounded scrape-time view of one configured
// dynamic upstream.
type DiscoveryFreshness struct {
	Pool        string
	LastSuccess time.Time
}

type discoveryFreshnessSource func() []DiscoveryFreshness

type discoveryFreshnessCollector struct {
	desc   *prometheus.Desc
	source atomic.Pointer[discoveryFreshnessSource]
}

func newDiscoveryFreshnessCollector() *discoveryFreshnessCollector {
	return &discoveryFreshnessCollector{
		desc: prometheus.NewDesc(
			"jul_discovery_last_success_timestamp_seconds",
			"Unix timestamp of the last non-empty result accepted by the current discovery worker; zero means unknown or never refreshed.",
			[]string{"pool"},
			nil,
		),
	}
}

func (c *discoveryFreshnessCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.desc
}

func (c *discoveryFreshnessCollector) Collect(ch chan<- prometheus.Metric) {
	source := c.source.Load()
	if source == nil || *source == nil {
		return
	}
	for _, pool := range (*source)() {
		value := float64(0)
		if !pool.LastSuccess.IsZero() {
			value = float64(pool.LastSuccess.UnixNano()) / float64(time.Second)
		}
		ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, value, pool.Pool)
	}
}

// SetDiscoveryFreshnessSource installs the live dynamic-pool freshness source.
// The collector reads it during each scrape so removed pools cannot leave a
// stale series and retired workers cannot recreate one.
func (m *Metrics) SetDiscoveryFreshnessSource(source func() []DiscoveryFreshness) {
	if m == nil {
		return
	}
	typedSource := discoveryFreshnessSource(source)
	m.discoveryFreshness.source.Store(&typedSource)
}
