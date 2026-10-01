// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
)

// PluginInstanceStats is one plugin's live WASM module-instance count. It
// mirrors plugins.InstanceStats without importing it, following the
// CacheTierStats convention.
type PluginInstanceStats struct {
	Plugin string
	Live   int
}

// PluginInstanceSource returns live instances per configured plugin. It is
// called once per scrape.
type PluginInstanceSource func() []PluginInstanceStats

// pluginInstanceCollector exports jul_plugin_instances at scrape time, so the
// acquire/release path pays nothing for the gauge (#506).
type pluginInstanceCollector struct {
	source atomic.Pointer[PluginInstanceSource]
	live   *prometheus.Desc
}

func newPluginInstanceCollector() *pluginInstanceCollector {
	return &pluginInstanceCollector{
		live: prometheus.NewDesc("jul_plugin_instances",
			"Live WASM module instances (idle and in use), labeled by plugin name. Bounded per plugin by max_instances.",
			[]string{"plugin"}, nil),
	}
}

// SetPluginInstanceSource wires the live-instance reader. Until it is set the
// gauge exports nothing, which is correct for a build without plugins.
func (m *Metrics) SetPluginInstanceSource(src PluginInstanceSource) {
	m.pluginInstances.source.Store(&src)
}

func (c *pluginInstanceCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.live
}

func (c *pluginInstanceCollector) Collect(ch chan<- prometheus.Metric) {
	src := c.source.Load()
	if src == nil {
		return
	}
	for _, s := range (*src)() {
		ch <- prometheus.MustNewConstMetric(c.live, prometheus.GaugeValue, float64(s.Live), s.Plugin)
	}
}
