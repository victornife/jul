// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// drive sends one request through the metrics middleware with the given status
// and optional X-Cache state, sleeping briefly so an observable latency is
// recorded (Windows' coarse clock makes a no-op handler record a zero sample).
func drive(m *Metrics, method, host string, status int, cache string) {
	h := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if cache != "" {
			w.Header().Set("X-Cache", cache)
		}
		time.Sleep(time.Millisecond)
		w.WriteHeader(status)
	}))
	req := httptest.NewRequest(method, "http://"+host+"/", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
}

func TestSnapshotCountsAndClasses(t *testing.T) {
	m := NewMetrics()
	for i := 0; i < 3; i++ {
		drive(m, "GET", "a.test", 200, "HIT")
	}
	drive(m, "GET", "a.test", 404, "MISS")
	drive(m, "GET", "a.test", 500, "BYPASS")

	s := m.Snapshot()
	if !s.Available {
		t.Fatal("snapshot should be available")
	}
	if s.RequestsTotal != 5 {
		t.Errorf("RequestsTotal = %v, want 5", s.RequestsTotal)
	}
	if s.StatusClasses["2xx"] != 3 {
		t.Errorf("2xx = %v, want 3", s.StatusClasses["2xx"])
	}
	if s.StatusClasses["4xx"] != 1 {
		t.Errorf("4xx = %v, want 1", s.StatusClasses["4xx"])
	}
	if s.StatusClasses["5xx"] != 1 {
		t.Errorf("5xx = %v, want 1", s.StatusClasses["5xx"])
	}
	if s.InFlight != 0 {
		t.Errorf("InFlight = %v, want 0", s.InFlight)
	}
	if s.LatencyAvgMs <= 0 {
		t.Errorf("LatencyAvgMs = %v, want > 0", s.LatencyAvgMs)
	}
	if !(s.LatencyP99Ms >= s.LatencyP95Ms && s.LatencyP95Ms >= s.LatencyP50Ms) {
		t.Errorf("latency quantiles not monotonic: p50=%v p95=%v p99=%v",
			s.LatencyP50Ms, s.LatencyP95Ms, s.LatencyP99Ms)
	}
}

func TestSnapshotCacheHitRatio(t *testing.T) {
	m := NewMetrics()
	for i := 0; i < 3; i++ {
		drive(m, "GET", "a.test", 200, "HIT")
	}
	drive(m, "GET", "a.test", 200, "MISS")

	s := m.Snapshot()
	if want := 3.0 / 4.0; math.Abs(s.CacheHitRatio-want) > 1e-9 {
		t.Errorf("CacheHitRatio = %v, want %v", s.CacheHitRatio, want)
	}
	if s.CacheEvents["HIT"] != 3 || s.CacheEvents["MISS"] != 1 {
		t.Errorf("CacheEvents = %v", s.CacheEvents)
	}
}

func TestSnapshotEmpty(t *testing.T) {
	m := NewMetrics()
	s := m.Snapshot()
	if !s.Available {
		t.Error("empty snapshot should still be available")
	}
	if s.RequestsTotal != 0 || s.CacheHitRatio != 0 {
		t.Errorf("expected zero totals, got %+v", s)
	}
	if s.StatusClasses == nil || s.CacheEvents == nil {
		t.Error("maps should be non-nil")
	}
	if s.UptimeSeconds < 0 {
		t.Errorf("UptimeSeconds = %v", s.UptimeSeconds)
	}
}

func TestSnapshotRatesDelta(t *testing.T) {
	m := NewMetrics()
	_ = m.Snapshot() // establish baseline (rps/error rate need two samples)
	time.Sleep(5 * time.Millisecond)
	for i := 0; i < 10; i++ {
		drive(m, "GET", "a.test", 200, "")
	}
	s := m.Snapshot()
	if s.RequestsPerSec <= 0 {
		t.Errorf("RequestsPerSec = %v, want > 0", s.RequestsPerSec)
	}
}

func TestSnapshotErrorRateWindowed(t *testing.T) {
	m := NewMetrics()
	for i := 0; i < 5; i++ {
		drive(m, "GET", "a.test", 200, "") // baseline traffic, all success
	}
	_ = m.Snapshot() // baseline; subsequent error rate is windowed from here
	time.Sleep(2 * time.Millisecond)
	for i := 0; i < 8; i++ {
		drive(m, "GET", "a.test", 200, "")
	}
	for i := 0; i < 2; i++ {
		drive(m, "GET", "a.test", 500, "")
	}
	s := m.Snapshot()
	if want := 0.2; math.Abs(s.ErrorRate-want) > 1e-9 {
		t.Errorf("ErrorRate = %v, want %v (windowed 2/10)", s.ErrorRate, want)
	}
}

func TestSnapshotFirstCallRatesZero(t *testing.T) {
	m := NewMetrics()
	drive(m, "GET", "a.test", 200, "")
	s := m.Snapshot() // first ever call: no baseline
	if s.RequestsPerSec != 0 {
		t.Errorf("RequestsPerSec = %v, want 0 on first snapshot", s.RequestsPerSec)
	}
	if s.ErrorRate != 0 {
		t.Errorf("ErrorRate = %v, want 0 on first snapshot", s.ErrorRate)
	}
}

func TestHistogramQuantile(t *testing.T) {
	b := []bucketBound{
		{upper: 1, count: 0},
		{upper: 2, count: 2},
		{upper: 4, count: 4},
		{upper: math.Inf(1), count: 4},
	}
	if got := histogramQuantile(0.5, b); got != 2 {
		t.Errorf("p50 = %v, want 2", got)
	}
	if got := histogramQuantile(1.0, b); got != 4 {
		t.Errorf("p100 = %v, want 4", got)
	}
	if got := histogramQuantile(0.5, nil); got != 0 {
		t.Errorf("empty = %v, want 0", got)
	}
	zero := []bucketBound{{upper: math.Inf(1), count: 0}}
	if got := histogramQuantile(0.9, zero); got != 0 {
		t.Errorf("zero-total = %v, want 0", got)
	}
}

func TestStatusClass(t *testing.T) {
	cases := map[string]string{
		"200": "2xx", "301": "3xx", "404": "4xx", "503": "5xx",
		"100": "1xx", "": "", "abc": "",
	}
	for code, want := range cases {
		if got := statusClass(code); got != want {
			t.Errorf("statusClass(%q) = %q, want %q", code, got, want)
		}
	}
}
