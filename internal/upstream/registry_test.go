package upstream

import (
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
	p1, err := r.For(upstreamCfg("api", "round_robin", "10.0.0.1:80"), "http")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	r.Commit()

	r.Begin()
	p2, err := r.For(upstreamCfg("api", "round_robin", "10.0.0.1:80", "10.0.0.2:80"), "http")
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
	p1, _ := r.For(upstreamCfg("api", "round_robin", "10.0.0.1:80"), "http")
	r.Commit()

	r.Begin()
	p2, _ := r.For(upstreamCfg("api", "least_conn", "10.0.0.1:80"), "http")
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
	p1, _ := r.For(upstreamCfg("api", "round_robin", "10.0.0.1:80"), "http")
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
	p1, _ := r.For(upstreamCfg("api", "round_robin", "10.0.0.1:80"), "http")
	r.Commit()

	// A failed reload that reshapes the pool (new staged pool) then aborts.
	r.Begin()
	p2, _ := r.For(upstreamCfg("api", "least_conn", "10.0.0.1:80"), "http")
	r.Abort()

	if closed(p1.Done()) {
		t.Fatal("the live pool must survive an aborted reload")
	}
	if !closed(p2.Done()) {
		t.Fatal("a staged pool from an aborted reload must be closed")
	}

	// The next successful build still reuses the live pool.
	r.Begin()
	p3, _ := r.For(upstreamCfg("api", "round_robin", "10.0.0.1:80"), "http")
	r.Commit()
	if p3 != p1 {
		t.Fatal("after abort the live pool should still be reusable")
	}
}

func TestRegistryAbortDoesNotMutateLiveBackends(t *testing.T) {
	r := NewRegistry(RegistryOptions{})

	r.Begin()
	p1, _ := r.For(upstreamCfg("api", "round_robin", "10.0.0.1:80"), "http")
	r.Commit()
	if got := len(p1.Backends()); got != 1 {
		t.Fatalf("initial backends = %d, want 1", got)
	}

	// A failed reload that keeps the same shape but adds a backend, then aborts.
	r.Begin()
	if _, err := r.For(upstreamCfg("api", "round_robin", "10.0.0.1:80", "10.0.0.2:80"), "http"); err != nil {
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
	a, _ := r.For(upstreamCfg("api", "round_robin", "10.0.0.1:80"), "http")
	b, _ := r.For(upstreamCfg("api", "round_robin", "10.0.0.1:80"), "http")
	r.Commit()
	if a != b {
		t.Fatal("two references to one upstream in a build should share a pool")
	}
}

func TestRegistryCloseAll(t *testing.T) {
	r := NewRegistry(RegistryOptions{})
	r.Begin()
	p1, _ := r.For(upstreamCfg("a", "round_robin", "10.0.0.1:80"), "http")
	p2, _ := r.For(upstreamCfg("b", "round_robin", "10.0.0.2:80"), "http")
	r.Commit()

	r.CloseAll()
	if !closed(p1.Done()) || !closed(p2.Done()) {
		t.Fatal("CloseAll should close every live pool")
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
	if _, err := r.For(up, "http"); err != nil {
		t.Fatalf("For: %v", err)
	}
	r.Commit()
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
	_, _ = r.For(upstreamCfg("z", "round_robin", "10.0.0.1:80"), "http")
	_, _ = r.For(upstreamCfg("a", "round_robin", "10.0.0.2:80"), "http")
	r.Commit()

	snap := r.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("len = %d", len(snap))
	}
	if snap[0].Name != "a" || snap[1].Name != "z" {
		t.Errorf("order = %v, %v", snap[0].Name, snap[1].Name)
	}
}
