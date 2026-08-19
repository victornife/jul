// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"fmt"
	"testing"
	"time"
)

// gatheredSeries counts exported series (not families) across every jul_* metric
// carrying a pool label.
func gatheredSeries(t *testing.T, m *Metrics) int {
	t.Helper()
	families, err := m.registry.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	n := 0
	for _, f := range families {
		for _, metric := range f.GetMetric() {
			for _, l := range metric.GetLabel() {
				if l.GetName() == "pool" {
					n++
					break
				}
			}
		}
	}
	return n
}

// TestPoolSeriesAreRetired is the regression guard for a leak that predates the
// resilience metrics.
//
// A `pool` label is bounded at one configuration snapshot but not over the life
// of the process: upstream_add and upstream_remove are supported admin patch
// operations, so an operator can churn pool names indefinitely. Prometheus never
// expires a series on its own, so every name a proxy ever served used to keep
// its series until restart.
//
// The assertion is proportional to *current* pools rather than an absolute
// number, because it is the growth that is the defect.
func TestPoolSeriesAreRetired(t *testing.T) {
	m := NewMetrics()

	observe := func(pool string) {
		m.ObserveUpstreamBackends(pool, 3)
		m.ObserveBackendsHealthy(pool, 2)
		m.ObserveDiscoveryError(pool)
		m.ObserveProbe(pool, "http", true, time.Millisecond)
		m.ObserveProbe(pool, "stream", false, time.Millisecond)
	}

	// One steady pool that must survive every churn cycle.
	observe("steady")
	steady := gatheredSeries(t, m)
	if steady == 0 {
		t.Fatal("no pool-labelled series were exported at all")
	}

	for i := 0; i < 2000; i++ {
		name := fmt.Sprintf("ephemeral-%d", i)
		observe(name)
		m.RetirePool(name)
	}

	if got := gatheredSeries(t, m); got != steady {
		t.Errorf("pool-labelled series after 2000 create/remove cycles = %d, want %d (the surviving pool's own series)", got, steady)
	}
}

// TestRetirePoolLeavesOtherPoolsAlone pins that retirement is scoped to the name
// it is given. A retirement that took neighbouring series with it would turn a
// leak into missing data during a reload, which is worse.
func TestRetirePoolLeavesOtherPoolsAlone(t *testing.T) {
	m := NewMetrics()
	m.ObserveUpstreamBackends("keep", 1)
	m.ObserveProbe("keep", "http", true, time.Millisecond)
	m.ObserveUpstreamBackends("drop", 1)
	m.ObserveProbe("drop", "http", true, time.Millisecond)

	before := gatheredSeries(t, m)
	m.RetirePool("drop")
	after := gatheredSeries(t, m)

	if after >= before {
		t.Fatalf("series after retiring one pool = %d, want fewer than %d", after, before)
	}
	if after == 0 {
		t.Fatal("retiring one pool removed every pool's series")
	}
}
