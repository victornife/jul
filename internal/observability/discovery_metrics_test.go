// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"testing"
	"time"
)

func TestDiscoveryFreshnessMetricUnknownAcceptedAndRetiredStates(t *testing.T) {
	m := NewMetrics()
	current := []DiscoveryFreshness{{Pool: "api"}}
	m.SetDiscoveryFreshnessSource(func() []DiscoveryFreshness { return current })

	assertSample := func(wantPresent bool, want float64) {
		t.Helper()
		families, err := m.Gather()
		if err != nil {
			t.Fatalf("Gather: %v", err)
		}
		for _, family := range families {
			if family.GetName() != "jul_discovery_last_success_timestamp_seconds" {
				continue
			}
			if !wantPresent {
				t.Fatal("retired pool still has a freshness series")
			}
			if len(family.GetMetric()) != 1 {
				t.Fatalf("freshness series count = %d, want 1", len(family.GetMetric()))
			}
			metric := family.GetMetric()[0]
			if got := metric.GetGauge().GetValue(); got != want {
				t.Fatalf("freshness value = %v, want %v", got, want)
			}
			if len(metric.GetLabel()) != 1 || metric.GetLabel()[0].GetName() != "pool" || metric.GetLabel()[0].GetValue() != "api" {
				t.Fatalf("freshness labels = %v, want only configured pool=api", metric.GetLabel())
			}
			return
		}
		if wantPresent {
			t.Fatal("dynamic pool freshness series is missing")
		}
	}

	assertSample(true, 0)
	lastSuccess := time.Date(2026, 11, 25, 12, 0, 0, 500_000_000, time.UTC)
	current = []DiscoveryFreshness{{Pool: "api", LastSuccess: lastSuccess}}
	assertSample(true, float64(lastSuccess.UnixNano())/float64(time.Second))

	current = nil
	assertSample(false, 0)
}
