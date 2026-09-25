// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestHealthProbeCancelledAndFencedOnPoolRetirement pins the #428 retirement
// rule for active health checking: a probe in flight when its pool retires is
// cancelled promptly (it does not outlive the pool by a full probe timeout)
// and its verdict never reaches the name-keyed metrics hooks, which a
// same-name replacement pool shares.
func TestHealthProbeCancelledAndFencedOnPoolRetirement(t *testing.T) {
	arrived := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		<-r.Context().Done()
	}))
	defer backend.Close()

	p := singlePool(t, strings.TrimPrefix(backend.URL, "http://"))
	hc := testChecker(p, healthParams{typ: "http", path: "/", timeout: time.Minute, healthyThreshold: 1, unhealthyThreshold: 1})
	var probes, transitions atomic.Int64
	hc.onProbe = func(string, string, bool, time.Duration) { probes.Add(1) }
	hc.onHealth = func(string, string, bool) { transitions.Add(1) }
	b := p.Backends()[0]

	done := make(chan struct{})
	go func() {
		defer close(done)
		hc.probeOne(b)
	}()
	<-arrived
	seeded := transitions.Load()
	p.Close()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("in-flight probe outlived its retired pool")
	}
	if n := probes.Load(); n != 0 {
		t.Fatalf("retired pool reported %d probe outcomes", n)
	}
	if n := transitions.Load(); n != seeded {
		t.Fatalf("retired pool reported %d health transitions after retirement", n-seeded)
	}
	if !b.ActiveHealthy() {
		t.Fatal("a cancelled probe must not be applied as a failure verdict")
	}
}

// TestHealthProbeLivePoolStillReports proves the fence only suppresses
// retired pools: a live pool's probe outcome is still reported.
func TestHealthProbeLivePoolStillReports(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer backend.Close()
	p := singlePool(t, strings.TrimPrefix(backend.URL, "http://"))
	defer p.Close()
	hc := testChecker(p, healthParams{typ: "http", path: "/", timeout: 5 * time.Second, healthyThreshold: 1, unhealthyThreshold: 1, expectStatus: []int{200}})
	var probes atomic.Int64
	hc.onProbe = func(string, string, bool, time.Duration) { probes.Add(1) }
	hc.probeOne(p.Backends()[0])
	if probes.Load() != 1 {
		t.Fatal("live pool probe outcome was not reported")
	}
}
