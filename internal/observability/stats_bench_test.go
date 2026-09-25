// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import "testing"

// BenchmarkSnapshot measures the #431 performance requirement (§34/§76): the
// full Snapshot() cost, including Gather() over every registered collector
// (Go/process collectors, cache, resilience) plus the new resource/capacity
// projection. This is the cost paid once per Console poll (2s interval),
// never per request.
func BenchmarkSnapshot(b *testing.B) {
	m := NewMetrics()
	m.SetCacheStatsSource(func() []CacheTierStats {
		return []CacheTierStats{{Tier: "memory", Bytes: 100, MaxBytes: 1000, Entries: 5}}
	})
	m.SetUpstreamCapacitySource(func() []UpstreamPoolStats {
		return []UpstreamPoolStats{
			{Name: "api", Active: 3, MaxActive: 10, Pending: 1, MaxPending: 5, Eligible: 2},
		}
	})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.Snapshot()
	}
}
