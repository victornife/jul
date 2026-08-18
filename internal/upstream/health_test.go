// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

// backendOf returns the single backend of a one-server pool pointing at addr.
func singlePool(t *testing.T, addr string) *Pool {
	t.Helper()
	p, err := NewPool(config.UpstreamConfig{
		Name:        "test",
		Strategy:    "round_robin",
		Servers:     []config.UpstreamServer{{Address: addr, Weight: 1}},
		MaxFails:    3,
		FailTimeout: config.Duration(time.Second),
	}, "http")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	return p
}

// testChecker builds a checker without starting its goroutine so probeOne can be
// driven deterministically.
func testChecker(p *Pool, params healthParams) *healthChecker {
	return &healthChecker{
		pool:   p,
		params: params,
		dialer: &net.Dialer{Timeout: params.timeout},
		client: &http.Client{
			Timeout:       params.timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Transport:     &http.Transport{DisableKeepAlives: true},
		},
		states: make(map[*Backend]*probeState),
	}
}

func TestHealthParamsDefaults(t *testing.T) {
	p := healthParamsFrom(config.HealthCheckConfig{Enabled: true})
	if p.typ != "http" {
		t.Errorf("type = %q, want http", p.typ)
	}
	if p.interval != 5*time.Second {
		t.Errorf("interval = %s, want 5s", p.interval)
	}
	if p.timeout <= 0 || p.timeout >= p.interval {
		t.Errorf("timeout = %s, want 0 < timeout < interval", p.timeout)
	}
	if p.healthyThreshold != 2 || p.unhealthyThreshold != 3 {
		t.Errorf("thresholds = %d/%d, want 2/3", p.healthyThreshold, p.unhealthyThreshold)
	}
	if len(p.expectStatus) != 1 || p.expectStatus[0] != 200 {
		t.Errorf("expect_status = %v, want [200]", p.expectStatus)
	}
}

func TestStatusAllowed(t *testing.T) {
	if !statusAllowed(204, nil) {
		t.Error("empty expect set should accept any 2xx")
	}
	if statusAllowed(500, nil) {
		t.Error("empty expect set should reject 5xx")
	}
	if !statusAllowed(204, []int{200, 204}) {
		t.Error("204 should be allowed when listed")
	}
	if statusAllowed(200, []int{204}) {
		t.Error("200 should be rejected when not listed")
	}
}

func TestHealthCheckHTTPTransitions(t *testing.T) {
	var code atomic.Int32
	code.Store(http.StatusOK)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(code.Load()))
	}))
	defer srv.Close()

	p := singlePool(t, strings.TrimPrefix(srv.URL, "http://"))
	b := p.Backends()[0]
	hc := testChecker(p, healthParamsFrom(config.HealthCheckConfig{
		Enabled: true, Type: "http", Path: "/",
		Interval: config.Duration(time.Second), Timeout: config.Duration(500 * time.Millisecond),
		HealthyThreshold: 2, UnhealthyThreshold: 3, ExpectStatus: []int{200},
	}))

	now := time.Now().UnixNano()
	if !b.available(now) {
		t.Fatal("backend should start available")
	}

	// Two failures stay healthy (threshold is 3), the third ejects it.
	code.Store(http.StatusServiceUnavailable)
	hc.probeOne(b)
	hc.probeOne(b)
	if !b.available(now) {
		t.Fatal("backend should still be available before threshold")
	}
	hc.probeOne(b)
	if b.available(now) {
		t.Fatal("backend should be ejected after unhealthy_threshold failures")
	}
	if _, err := p.Pick(); err == nil {
		t.Fatal("Pick should fail when the only backend is ejected")
	}

	// One success stays unhealthy (threshold is 2), the second restores it.
	code.Store(http.StatusOK)
	hc.probeOne(b)
	if b.available(now) {
		t.Fatal("backend should remain ejected before healthy_threshold")
	}
	hc.probeOne(b)
	if !b.available(now) {
		t.Fatal("backend should recover after healthy_threshold successes")
	}
	if _, err := p.Pick(); err != nil {
		t.Fatalf("Pick should succeed after recovery: %v", err)
	}
}

func TestHealthCheckClearsPassiveCooldown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := singlePool(t, strings.TrimPrefix(srv.URL, "http://"))
	b := p.Backends()[0]
	hc := testChecker(p, healthParamsFrom(config.HealthCheckConfig{
		Enabled: true, Type: "http", Path: "/",
		Interval: config.Duration(time.Second), Timeout: config.Duration(500 * time.Millisecond),
		HealthyThreshold: 1, UnhealthyThreshold: 1, ExpectStatus: []int{200},
	}))

	// Drive the backend down via active checks (server is healthy, so flip it
	// once unhealthy then back so the recovery path runs MarkSuccess).
	hc.states[b] = &probeState{healthy: false}
	b.setActiveHealthy(false)
	// Simulate a circuit left open by live traffic.
	forceOpen(b)

	hc.probeOne(b) // success with healthyThreshold 1 -> recover + close the circuit
	now := time.Now().UnixNano()
	if !b.available(now) {
		t.Fatal("a recovered backend should close its circuit and be available")
	}
}

func TestHealthCheckExpectBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("status: SERVING"))
	}))
	defer srv.Close()

	p := singlePool(t, strings.TrimPrefix(srv.URL, "http://"))
	b := p.Backends()[0]

	pass := testChecker(p, healthParamsFrom(config.HealthCheckConfig{
		Enabled: true, Type: "http", Path: "/", ExpectBody: "SERVING",
		Interval: config.Duration(time.Second), Timeout: config.Duration(500 * time.Millisecond),
		HealthyThreshold: 1, UnhealthyThreshold: 1,
	}))
	if !pass.probe(b) {
		t.Error("probe should pass when body contains expected substring")
	}
	fail := testChecker(p, healthParamsFrom(config.HealthCheckConfig{
		Enabled: true, Type: "http", Path: "/", ExpectBody: "NOT_SERVING",
		Interval: config.Duration(time.Second), Timeout: config.Duration(500 * time.Millisecond),
		HealthyThreshold: 1, UnhealthyThreshold: 1,
	}))
	if fail.probe(b) {
		t.Error("probe should fail when body lacks expected substring")
	}
}

func TestHealthCheckTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	p := singlePool(t, addr)
	b := p.Backends()[0]
	hc := testChecker(p, healthParamsFrom(config.HealthCheckConfig{
		Enabled: true, Type: "tcp",
		Interval: config.Duration(time.Second), Timeout: config.Duration(500 * time.Millisecond),
		HealthyThreshold: 1, UnhealthyThreshold: 1,
	}))
	if !hc.probe(b) {
		t.Error("tcp probe should pass while the listener accepts")
	}
	_ = ln.Close()
	// On Linux the kernel's TCP stack can complete a queued connection at zero
	// latency after close() — the SYN/SYN-ACK exchange for a queued backlog
	// entry may already be in-flight. Poll until the port is actually refused
	// (observed within 1–5 ms) rather than sleeping a fixed duration, so the
	// test is fast on idle machines and robust on loaded ones.
	portRefused := false
	for i := 0; i < 50; i++ {
		if !hc.probe(b) {
			portRefused = true
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !portRefused {
		t.Fatal("tcp probe never failed after the listener was closed (port still accepting after 100 ms)")
	}
	// Verify refusal is stable (not a one-shot race window).
	if hc.probe(b) {
		t.Error("tcp probe should fail after the listener is closed (unstable — probe passed after port was already refused)")
	}
}

func TestStartHealthChecksStopsOnClose(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := singlePool(t, strings.TrimPrefix(srv.URL, "http://"))
	var probes atomic.Int64
	p.StartHealthChecks(config.HealthCheckConfig{
		Enabled: true, Type: "http", Path: "/",
		Interval: config.Duration(5 * time.Millisecond), Timeout: config.Duration(2 * time.Millisecond),
		HealthyThreshold: 1, UnhealthyThreshold: 1, ExpectStatus: []int{200},
	}, nil, func(string, bool, time.Duration) { probes.Add(1) })

	// Wait until at least one probe has run.
	deadline := time.Now().Add(2 * time.Second)
	for probes.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("health checker never probed")
		}
		time.Sleep(2 * time.Millisecond)
	}

	p.Close()
	// Allow any in-flight probe to finish, then confirm probing stopped.
	time.Sleep(30 * time.Millisecond)
	after := probes.Load()
	time.Sleep(40 * time.Millisecond)
	if probes.Load() != after {
		t.Fatalf("health checker kept probing after Close: %d -> %d", after, probes.Load())
	}
}
