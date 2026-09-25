// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"testing"
	"time"
)

// TestCPURateFirstCallUnavailable pins #431 §9/§12: the very first sample has
// no baseline, so the rate must be nil (unavailable), never a fabricated 0.
func TestCPURateFirstCallUnavailable(t *testing.T) {
	m := NewMetrics()
	if got := m.cpuRate(true, 12.5); got != nil {
		t.Fatalf("first call = %v, want nil (no baseline yet)", *got)
	}
}

// TestCPURateNoSampleUnavailable proves an unavailable collector (haveSample
// false) never produces a rate at all, even after a baseline exists.
func TestCPURateNoSampleUnavailable(t *testing.T) {
	m := NewMetrics()
	_ = m.cpuRate(true, 1.0)
	if got := m.cpuRate(false, 0); got != nil {
		t.Fatalf("no-sample call = %v, want nil", *got)
	}
}

// TestCPURateComputesDelta proves the rate is delta(cpu_seconds)/delta(wall).
func TestCPURateComputesDelta(t *testing.T) {
	m := NewMetrics()
	_ = m.cpuRate(true, 10.0)
	// Force a known elapsed wall-clock baseline instead of sleeping.
	m.statsMu.Lock()
	m.statsLast = time.Now().Add(-2 * time.Second)
	m.statsMu.Unlock()

	got := m.cpuRate(true, 13.0) // 3 CPU-seconds over ~2 wall-seconds
	if got == nil {
		t.Fatal("expected a rate on the second sample")
	}
	if *got < 1.4 || *got > 1.6 {
		t.Fatalf("rate = %v, want ~1.5 cores (3s CPU / 2s wall)", *got)
	}
}

// TestCPURateNeverNegative guards a monotonic-counter irregularity: the rate
// must floor at 0, never go negative.
func TestCPURateNeverNegative(t *testing.T) {
	m := NewMetrics()
	_ = m.cpuRate(true, 10.0)
	m.statsMu.Lock()
	m.statsLast = time.Now().Add(-time.Second)
	m.statsMu.Unlock()

	got := m.cpuRate(true, 5.0) // decreased — must not happen, but must not go negative either
	if got == nil {
		t.Fatal("expected a rate")
	}
	if *got != 0 {
		t.Fatalf("rate = %v, want 0 (floored, not negative)", *got)
	}
}

func TestCacheTierOccupancyRatioOnlyWhenMaxKnown(t *testing.T) {
	out := cacheTierOccupancy([]CacheTierStats{
		{Tier: "memory", Bytes: 50, MaxBytes: 100, Entries: 4, Evictions: 1},
		{Tier: "disk", Bytes: 200, MaxBytes: 0, Entries: 9},
	})
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	if out[0].OccupancyRatio == nil || *out[0].OccupancyRatio != 0.5 {
		t.Fatalf("memory ratio = %v, want 0.5", out[0].OccupancyRatio)
	}
	if out[1].OccupancyRatio != nil {
		t.Fatalf("disk (unbounded, MaxBytes=0) ratio = %v, want nil", *out[1].OccupancyRatio)
	}
}

func TestCacheTierOccupancyEmpty(t *testing.T) {
	if out := cacheTierOccupancy(nil); out != nil {
		t.Fatalf("empty input = %v, want nil (caching disabled)", out)
	}
}

func TestUpstreamCapacitySummaryWorstPoolWins(t *testing.T) {
	pools := []UpstreamPoolStats{
		{Name: "b", Active: 5, MaxActive: 10, Pending: 1, MaxPending: 10, Eligible: 1},
		{Name: "a", Active: 9, MaxActive: 10, Pending: 8, MaxPending: 10, Eligible: 1},
		{Name: "c", Active: 1, MaxActive: 0 /* unbounded: excluded */, Pending: 1, MaxPending: 10, Eligible: 1},
	}
	active, pending, noEligible, exhausted := upstreamCapacitySummary(pools)
	if active == nil || active.Pool != "a" || active.Ratio != 0.9 {
		t.Fatalf("worst active = %+v, want pool a at 0.9", active)
	}
	if pending == nil || pending.Pool != "a" || pending.Ratio != 0.8 {
		t.Fatalf("worst pending = %+v, want pool a at 0.8", pending)
	}
	if len(noEligible) != 0 {
		t.Fatalf("noEligible = %v, want empty", noEligible)
	}
	if len(exhausted) != 0 {
		t.Fatalf("exhausted = %v, want empty", exhausted)
	}
}

func TestUpstreamCapacitySummaryDeterministicTieBreak(t *testing.T) {
	pools := []UpstreamPoolStats{
		{Name: "zzz", Active: 5, MaxActive: 10},
		{Name: "aaa", Active: 5, MaxActive: 10},
	}
	active, _, _, _ := upstreamCapacitySummary(pools)
	if active == nil || active.Pool != "aaa" {
		t.Fatalf("tie-break winner = %+v, want the alphabetically-first pool %q", active, "aaa")
	}
}

func TestUpstreamCapacitySummaryNoEligibleAndBudgetExhausted(t *testing.T) {
	pools := []UpstreamPoolStats{
		{Name: "down", Eligible: 0},
		{Name: "up", Eligible: 1},
		{Name: "spent", Eligible: 1, BudgetPercent: 10, BudgetRemaining: 0},
		{Name: "unbudgeted", Eligible: 1, BudgetPercent: 0, BudgetRemaining: 0},
	}
	_, _, noEligible, exhausted := upstreamCapacitySummary(pools)
	if len(noEligible) != 1 || noEligible[0] != "down" {
		t.Fatalf("noEligible = %v, want [down]", noEligible)
	}
	if len(exhausted) != 1 || exhausted[0] != "spent" {
		t.Fatalf("exhausted = %v, want [spent] (unbudgeted pool with 0 remaining is not \"exhausted\")", exhausted)
	}
}

func TestUpstreamCapacitySummaryEmpty(t *testing.T) {
	active, pending, noEligible, exhausted := upstreamCapacitySummary(nil)
	if active != nil || pending != nil || noEligible != nil || exhausted != nil {
		t.Fatalf("empty input produced non-nil output: %+v %+v %v %v", active, pending, noEligible, exhausted)
	}
}

// TestSnapshotUsesWiredCapacitySource proves SetUpstreamCapacitySource/
// upstreamCapacitySnapshot's non-nil path: once wired, Snapshot's capacity
// summary reflects the source's data, not the (separate, cheaper)
// UpstreamStatsSource used by the Prometheus collector.
func TestSnapshotUsesWiredCapacitySource(t *testing.T) {
	m := NewMetrics()
	m.SetUpstreamCapacitySource(func() []UpstreamPoolStats {
		return []UpstreamPoolStats{
			{Name: "api", Active: 9, MaxActive: 10, Pending: 1, MaxPending: 10, Eligible: 1},
		}
	})
	snap := m.Snapshot()
	if snap.UpstreamWorstActive == nil || snap.UpstreamWorstActive.Pool != "api" {
		t.Fatalf("UpstreamWorstActive = %+v, want pool api", snap.UpstreamWorstActive)
	}
	if snap.UpstreamWorstActive.Ratio != 0.9 {
		t.Fatalf("ratio = %v, want 0.9", snap.UpstreamWorstActive.Ratio)
	}
}

// TestSnapshotUsesWiredCacheSource proves SetCacheStatsSource/
// cacheTierSnapshot's non-nil path feeds Snapshot's cache occupancy cards.
func TestSnapshotUsesWiredCacheSource(t *testing.T) {
	m := NewMetrics()
	m.SetCacheStatsSource(func() []CacheTierStats {
		return []CacheTierStats{{Tier: "memory", Bytes: 25, MaxBytes: 100, Entries: 3, Evictions: 1}}
	})
	snap := m.Snapshot()
	if len(snap.CacheTiers) != 1 || snap.CacheTiers[0].Tier != "memory" {
		t.Fatalf("CacheTiers = %+v, want one memory tier", snap.CacheTiers)
	}
	if snap.CacheTiers[0].OccupancyRatio == nil || *snap.CacheTiers[0].OccupancyRatio != 0.25 {
		t.Fatalf("OccupancyRatio = %v, want 0.25", snap.CacheTiers[0].OccupancyRatio)
	}
}

// TestSnapshotResourceFieldsPopulated is an integration check against the real
// registered Go/process collectors: on every platform this repository's CI
// matrix covers (Linux/macOS/Windows), RSS, Go heap and goroutines are always
// reported. CPU is nil on the first call by design.
func TestSnapshotResourceFieldsPopulated(t *testing.T) {
	m := NewMetrics()
	snap := m.Snapshot()

	if snap.CPUCores != nil {
		t.Errorf("CPUCores on the first snapshot = %v, want nil (no baseline yet)", *snap.CPUCores)
	}
	if snap.RSSBytes == nil || *snap.RSSBytes <= 0 {
		t.Errorf("RSSBytes = %v, want a positive value on this platform", snap.RSSBytes)
	}
	if snap.GoHeapAllocBytes == nil || *snap.GoHeapAllocBytes <= 0 {
		t.Errorf("GoHeapAllocBytes = %v, want a positive value", snap.GoHeapAllocBytes)
	}
	if snap.Goroutines == nil || *snap.Goroutines <= 0 {
		t.Errorf("Goroutines = %v, want a positive value", snap.Goroutines)
	}
	if snap.OpenFDs == nil {
		t.Error("OpenFDs = nil, want a value on this platform")
	}

	// A second call, after real elapsed time, should report a CPU rate.
	time.Sleep(20 * time.Millisecond)
	snap2 := m.Snapshot()
	if snap2.CPUCores == nil {
		t.Error("CPUCores on the second snapshot = nil, want a computed rate")
	} else if *snap2.CPUCores < 0 {
		t.Errorf("CPUCores = %v, want >= 0", *snap2.CPUCores)
	}
}

// TestSnapshotConcurrentPollersNeverNegative drives many concurrent Snapshot
// callers and asserts every reported rate (requests/sec, CPU cores) stays
// non-negative — #431 §11's "avoid absurd spikes or negative results" under
// concurrent polling.
func TestSnapshotConcurrentPollersNeverNegative(t *testing.T) {
	m := NewMetrics()
	done := make(chan struct{})
	for i := 0; i < 20; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 50; j++ {
				snap := m.Snapshot()
				if snap.RequestsPerSec < 0 {
					t.Errorf("RequestsPerSec = %v, want >= 0", snap.RequestsPerSec)
				}
				if snap.CPUCores != nil && *snap.CPUCores < 0 {
					t.Errorf("CPUCores = %v, want >= 0", *snap.CPUCores)
				}
			}
		}()
	}
	for i := 0; i < 20; i++ {
		<-done
	}
}
