// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

func closed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func upstreamCfg(name, strategy string, servers ...string) config.UpstreamConfig {
	srv := make([]config.UpstreamServer, len(servers))
	for i, s := range servers {
		srv[i] = config.UpstreamServer{Address: s, Weight: 1}
	}
	return config.UpstreamConfig{
		Name:        name,
		Strategy:    strategy,
		Servers:     srv,
		MaxFails:    3,
		FailTimeout: config.Duration(10 * time.Second),
	}
}

func TestRegistryReusesUnchangedPool(t *testing.T) {
	r := NewRegistry(RegistryOptions{})

	r.Begin()
	p1, err := r.For(context.Background(), upstreamCfg("api", "round_robin", "10.0.0.1:80"), "http")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	r.Commit()

	r.Begin()
	p2, err := r.For(context.Background(), upstreamCfg("api", "round_robin", "10.0.0.1:80", "10.0.0.2:80"), "http")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	r.Commit()

	if p1 != p2 {
		t.Fatal("an unchanged upstream should reuse the same pool across reloads")
	}
	if closed(p1.Done()) {
		t.Fatal("a reused pool must not be closed")
	}
	if got := len(p1.Backends()); got != 2 {
		t.Fatalf("reused pool backends = %d, want 2 (UpdateBackends applied)", got)
	}
}

func TestRegistryReplacesPoolOnStrategyChange(t *testing.T) {
	r := NewRegistry(RegistryOptions{})

	r.Begin()
	p1, _ := r.For(context.Background(), upstreamCfg("api", "round_robin", "10.0.0.1:80"), "http")
	r.Commit()

	r.Begin()
	p2, _ := r.For(context.Background(), upstreamCfg("api", "least_conn", "10.0.0.1:80"), "http")
	r.Commit()

	if p1 == p2 {
		t.Fatal("a strategy change should build a new pool")
	}
	if !closed(p1.Done()) {
		t.Fatal("the replaced pool should be closed")
	}
	if closed(p2.Done()) {
		t.Fatal("the new pool should be live")
	}
}

func TestRegistryClosesRemovedPool(t *testing.T) {
	r := NewRegistry(RegistryOptions{})

	r.Begin()
	p1, _ := r.For(context.Background(), upstreamCfg("api", "round_robin", "10.0.0.1:80"), "http")
	r.Commit()

	// A reload that no longer references "api".
	r.Begin()
	r.Commit()

	if !closed(p1.Done()) {
		t.Fatal("a removed upstream's pool should be closed")
	}
}

func TestRegistryAbortKeepsLiveClosesStaged(t *testing.T) {
	r := NewRegistry(RegistryOptions{})

	r.Begin()
	p1, _ := r.For(context.Background(), upstreamCfg("api", "round_robin", "10.0.0.1:80"), "http")
	r.Commit()

	// A failed reload that reshapes the pool (new staged pool) then aborts.
	r.Begin()
	p2, _ := r.For(context.Background(), upstreamCfg("api", "least_conn", "10.0.0.1:80"), "http")
	r.Abort()

	if closed(p1.Done()) {
		t.Fatal("the live pool must survive an aborted reload")
	}
	if !closed(p2.Done()) {
		t.Fatal("a staged pool from an aborted reload must be closed")
	}

	// The next successful build still reuses the live pool.
	r.Begin()
	p3, _ := r.For(context.Background(), upstreamCfg("api", "round_robin", "10.0.0.1:80"), "http")
	r.Commit()
	if p3 != p1 {
		t.Fatal("after abort the live pool should still be reusable")
	}
}

func TestRegistryAbortDoesNotMutateLiveBackends(t *testing.T) {
	r := NewRegistry(RegistryOptions{})

	r.Begin()
	p1, _ := r.For(context.Background(), upstreamCfg("api", "round_robin", "10.0.0.1:80"), "http")
	r.Commit()
	if got := len(p1.Backends()); got != 1 {
		t.Fatalf("initial backends = %d, want 1", got)
	}

	// A failed reload that keeps the same shape but adds a backend, then aborts.
	r.Begin()
	if _, err := r.For(context.Background(), upstreamCfg("api", "round_robin", "10.0.0.1:80", "10.0.0.2:80"), "http"); err != nil {
		t.Fatalf("For: %v", err)
	}
	r.Abort()

	// The deferred UpdateBackends must NOT have been applied: the live pool keeps
	// its previous backend set so the reload stays atomic.
	if got := len(p1.Backends()); got != 1 {
		t.Fatalf("aborted reload mutated live pool backends = %d, want 1", got)
	}
}

func TestRegistryDedupsWithinBuild(t *testing.T) {
	r := NewRegistry(RegistryOptions{})
	r.Begin()
	a, _ := r.For(context.Background(), upstreamCfg("api", "round_robin", "10.0.0.1:80"), "http")
	b, _ := r.For(context.Background(), upstreamCfg("api", "round_robin", "10.0.0.1:80"), "http")
	r.Commit()
	if a != b {
		t.Fatal("two references to one upstream in a build should share a pool")
	}
}

func TestRegistryCloseAll(t *testing.T) {
	r := NewRegistry(RegistryOptions{})
	r.Begin()
	p1, _ := r.For(context.Background(), upstreamCfg("a", "round_robin", "10.0.0.1:80"), "http")
	p2, _ := r.For(context.Background(), upstreamCfg("b", "round_robin", "10.0.0.2:80"), "http")
	r.Commit()

	r.CloseAll()
	if !closed(p1.Done()) || !closed(p2.Done()) {
		t.Fatal("CloseAll should close every live pool")
	}
}

// TestRegistryPreflightSeedsDiscoveryPool (R10-03) verifies that a freshly
// built discovery pool performs a synchronous one-shot resolve during
// Registry.For, so it has backends before Activate starts the periodic
// refresher.
func TestRegistryPreflightSeedsDiscoveryPool(t *testing.T) {
	d := &fakeDiscoverer{}
	d.set([]Target{{Address: "10.0.0.1:80"}, {Address: "10.0.0.2:80"}}, nil)

	var backendEvents atomic.Int64
	r := NewRegistry(RegistryOptions{
		OnBackends: func(string, int) { backendEvents.Add(1) },
		NewDiscoverer: func(config.DiscoveryConfig, DialFunc) (Discoverer, error) { return d, nil },
	})

	up := discoveryCfg("dns", "svc.local:80")
	r.Begin()
	p, err := r.For(context.Background(), up, "http")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	r.Commit()
	defer r.CloseAll()

	if got := len(p.Backends()); got != 2 {
		t.Fatalf("preflight discovery seed backends = %d, want 2", got)
	}
	// One call from the preflight seed in For, plus one from Commit.
	if got := backendEvents.Load(); got != 2 {
		t.Fatalf("OnBackends called %d times, want 2", got)
	}

	// Activate starts the periodic refresher. The pool already has backends,
	// so the first request does not see an empty pool.
	r.Activate()
	if got := len(p.Backends()); got != 2 {
		t.Fatalf("backends after Activate = %d, want 2", got)
	}
}

func TestRegistryStartsHealthChecks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	var probes atomic.Int64
	r := NewRegistry(RegistryOptions{
		OnProbe: func(string, bool, time.Duration) { probes.Add(1) },
	})

	up := upstreamCfg("api", "round_robin", addr)
	up.HealthCheck = &config.HealthCheckConfig{
		Enabled: true, Type: "http", Path: "/",
		Interval: config.Duration(5 * time.Millisecond), Timeout: config.Duration(2 * time.Millisecond),
		HealthyThreshold: 1, UnhealthyThreshold: 1, ExpectStatus: []int{200},
	}

	r.Begin()
	if _, err := r.For(context.Background(), up, "http"); err != nil {
		t.Fatalf("For: %v", err)
	}
	r.Commit()
	r.Activate()
	defer r.CloseAll()

	deadline := time.Now().Add(2 * time.Second)
	for probes.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("registry did not start the health checker for an enabled upstream")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestRegistrySnapshot(t *testing.T) {
	r := NewRegistry(RegistryOptions{})
	r.Begin()
	_, _ = r.For(context.Background(), upstreamCfg("z", "round_robin", "10.0.0.1:80"), "http")
	_, _ = r.For(context.Background(), upstreamCfg("a", "round_robin", "10.0.0.2:80"), "http")
	r.Commit()

	snap := r.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("len = %d", len(snap))
	}
	if snap[0].Name != "a(http)" || snap[1].Name != "z(http)" {
		t.Errorf("order = %v, %v", snap[0].Name, snap[1].Name)
	}
}

func TestRegistryActivationDeferred(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	var probes atomic.Int64
	r := NewRegistry(RegistryOptions{
		OnProbe: func(string, bool, time.Duration) { probes.Add(1) },
	})

	up := upstreamCfg("api", "round_robin", addr)
	up.HealthCheck = &config.HealthCheckConfig{
		Enabled: true, Type: "http", Path: "/",
		Interval: config.Duration(5 * time.Millisecond), Timeout: config.Duration(2 * time.Millisecond),
		HealthyThreshold: 1, UnhealthyThreshold: 1, ExpectStatus: []int{200},
	}

	r.Begin()
	if _, err := r.For(context.Background(), up, "http"); err != nil {
		t.Fatalf("For: %v", err)
	}
	r.Commit()

	// No probes should have run before Activate.
	time.Sleep(20 * time.Millisecond)
	if probes.Load() != 0 {
		t.Fatalf("probes before Activate = %d, want 0", probes.Load())
	}

	r.Activate()
	defer r.CloseAll()

	deadline := time.Now().Add(2 * time.Second)
	for probes.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("health checker did not start after Activate")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestRegistryAbortBeforeActivateClosesFreshPool(t *testing.T) {
	r := NewRegistry(RegistryOptions{})

	up := upstreamCfg("api", "round_robin", "10.0.0.1:80")
	up.HealthCheck = &config.HealthCheckConfig{
		Enabled: true, Type: "http", Path: "/",
		Interval: config.Duration(5 * time.Millisecond), Timeout: config.Duration(2 * time.Millisecond),
		HealthyThreshold: 1, UnhealthyThreshold: 1, ExpectStatus: []int{200},
	}

	r.Begin()
	p, err := r.For(context.Background(), up, "http")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	r.Abort()

	if !closed(p.Done()) {
		t.Fatal("fresh pool from aborted build must be closed before Activate")
	}
}

func TestRegistryCandidateSnapshotUsesPendingServers(t *testing.T) {
	r := NewRegistry(RegistryOptions{})

	r.Begin()
	p1, _ := r.For(context.Background(), upstreamCfg("api", "round_robin", "10.0.0.1:80"), "http")
	r.Commit()
	r.Activate()
	defer r.CloseAll()

	// Reuse the pool but change the candidate backend list.
	r.Begin()
	p2, _ := r.For(context.Background(), upstreamCfg("api", "round_robin", "10.0.0.2:80"), "http")
	if p1 != p2 {
		t.Fatal("pool should be reused")
	}

	cs := r.CandidateSnapshot("api", "http")
	if cs == nil {
		t.Fatal("CandidateSnapshot returned nil")
	}
	backends := cs.Backends()
	if len(backends) != 1 || backends[0].Address != "10.0.0.2:80" {
		t.Fatalf("candidate snapshot backends = %v, want [10.0.0.2:80]", backends)
	}

	// Before Commit the live pool still has the old backend.
	if got := len(p1.Backends()); got != 1 || p1.Backends()[0].Address != "10.0.0.1:80" {
		t.Fatalf("live pool before Commit = %v, want [10.0.0.1:80]", p1.Backends())
	}
	r.Commit()
}

func TestRegistryActivateSkipsReusedPools(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	var probes atomic.Int64
	r := NewRegistry(RegistryOptions{
		OnProbe: func(string, bool, time.Duration) { probes.Add(1) },
	})

	up := upstreamCfg("api", "round_robin", addr)
	up.HealthCheck = &config.HealthCheckConfig{
		Enabled: true, Type: "http", Path: "/",
		Interval: config.Duration(5 * time.Millisecond), Timeout: config.Duration(2 * time.Millisecond),
		HealthyThreshold: 1, UnhealthyThreshold: 1, ExpectStatus: []int{200},
	}

	r.Begin()
	if _, err := r.For(context.Background(), up, "http"); err != nil {
		t.Fatalf("For: %v", err)
	}
	r.Commit()
	r.Activate()
	defer r.CloseAll()

	time.Sleep(20 * time.Millisecond)
	first := probes.Load()
	if first == 0 {
		t.Fatal("health checker did not start on first build")
	}

	// Reuse the pool on a subsequent build; Activate must not start a second
	// health checker (probe rate should stay the same).
	r.Begin()
	if _, err := r.For(context.Background(), up, "http"); err != nil {
		t.Fatalf("For: %v", err)
	}
	r.Commit()
	r.Activate()

	time.Sleep(20 * time.Millisecond)
	second := probes.Load()
	if second <= first {
		t.Fatalf("probe count did not advance after reuse: %d -> %d", first, second)
	}
	if second > first+10 {
		t.Fatalf("probe count jumped too much after reuse, suggesting duplicate checker: %d -> %d", first, second)
	}
}
