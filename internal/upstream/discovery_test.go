// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

// fakeDiscoverer is a programmable Discoverer for refresher tests. Resolve
// returns whatever the current targets/err are, under a lock so a test can swap
// them between resolves.
type fakeDiscoverer struct {
	mu      sync.Mutex
	targets []Target
	err     error
	calls   atomic.Int64
}

func (f *fakeDiscoverer) set(targets []Target, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.targets, f.err = targets, err
}

func (f *fakeDiscoverer) Resolve(context.Context) ([]Target, error) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.targets, f.err
}

func (f *fakeDiscoverer) Describe() string { return "fake" }

// eventually polls cond until it is true or the deadline passes.
func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within deadline")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func discoveryCfg(typ, target string) config.UpstreamConfig {
	return config.UpstreamConfig{
		Name:        "disco",
		Strategy:    "round_robin",
		MaxFails:    3,
		FailTimeout: config.Duration(10 * time.Second),
		Discovery: &config.DiscoveryConfig{
			Type:    typ,
			Target:  target,
			Refresh: config.Duration(5 * time.Millisecond),
		},
	}
}

func TestTargetsToServers(t *testing.T) {
	got := targetsToServers([]Target{
		{Address: "10.0.0.1:80", Weight: 5},
		{Address: "10.0.0.2:80", Weight: 0}, // normalized to 1
	})
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Weight != 5 {
		t.Fatalf("weight[0] = %d, want 5", got[0].Weight)
	}
	if got[1].Weight != 1 {
		t.Fatalf("weight[1] = %d, want 1 (normalized)", got[1].Weight)
	}
}

func TestNewPoolAllowsEmptyDiscoveryPool(t *testing.T) {
	p, err := NewPool(discoveryCfg("dns", "svc.local:80"), "http")
	if err != nil {
		t.Fatalf("NewPool with discovery and no servers: %v", err)
	}
	if got := len(p.Backends()); got != 0 {
		t.Fatalf("fresh discovery pool backends = %d, want 0", got)
	}
}

func TestStartDiscoveryAppliesTargets(t *testing.T) {
	p, err := NewPool(discoveryCfg("dns", "svc.local:80"), "http")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	f := &fakeDiscoverer{}
	f.set([]Target{{Address: "10.0.0.1:80"}, {Address: "10.0.0.2:80"}}, nil)

	p.StartDiscovery(f, 5*time.Millisecond, DiscoveryHooks{}, nil)

	eventually(t, func() bool { return len(p.Backends()) == 2 })

	// Add a backend and confirm the next refresh picks it up.
	f.set([]Target{{Address: "10.0.0.1:80"}, {Address: "10.0.0.2:80"}, {Address: "10.0.0.3:80"}}, nil)
	eventually(t, func() bool { return len(p.Backends()) == 3 })
}

func TestStartDiscoveryKeepsLastGoodOnError(t *testing.T) {
	p, err := NewPool(discoveryCfg("dns", "svc.local:80"), "http")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	var errs atomic.Int64
	f := &fakeDiscoverer{}
	f.set([]Target{{Address: "10.0.0.1:80"}, {Address: "10.0.0.2:80"}}, nil)
	p.StartDiscovery(f, 5*time.Millisecond, DiscoveryHooks{
		OnError: func(string) { errs.Add(1) },
	}, nil)

	eventually(t, func() bool { return len(p.Backends()) == 2 })

	// A failing resolve must keep the last-good backend set.
	f.set(nil, errors.New("provider down"))
	eventually(t, func() bool { return errs.Load() > 0 })
	if got := len(p.Backends()); got != 2 {
		t.Fatalf("backends after error = %d, want 2 (last-good kept)", got)
	}

	// An empty (non-error) resolve must also keep the last-good set.
	f.set([]Target{}, nil)
	before := errs.Load()
	eventually(t, func() bool { return errs.Load() > before })
	if got := len(p.Backends()); got != 2 {
		t.Fatalf("backends after empty resolve = %d, want 2 (last-good kept)", got)
	}
}

func TestStartDiscoveryStopsOnClose(t *testing.T) {
	p, err := NewPool(discoveryCfg("dns", "svc.local:80"), "http")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	f := &fakeDiscoverer{}
	f.set([]Target{{Address: "10.0.0.1:80"}}, nil)
	p.StartDiscovery(f, 5*time.Millisecond, DiscoveryHooks{}, nil)

	eventually(t, func() bool { return f.calls.Load() > 0 })
	p.Close()

	// After Close the refresher must stop calling Resolve.
	settle := f.calls.Load()
	time.Sleep(30 * time.Millisecond)
	settle2 := f.calls.Load()
	time.Sleep(30 * time.Millisecond)
	if f.calls.Load() > settle2+1 {
		t.Fatalf("refresher kept running after Close: calls grew %d -> %d -> %d", settle, settle2, f.calls.Load())
	}
}

func TestDiscoveryEnabled(t *testing.T) {
	cases := []struct {
		d    *config.DiscoveryConfig
		want bool
	}{
		{nil, false},
		{&config.DiscoveryConfig{Type: ""}, false},
		{&config.DiscoveryConfig{Type: "static"}, false},
		{&config.DiscoveryConfig{Type: "dns"}, true},
		{&config.DiscoveryConfig{Type: "dns_srv"}, true},
		{&config.DiscoveryConfig{Type: "consul"}, true},
		{&config.DiscoveryConfig{Type: "kubernetes"}, true},
	}
	for _, c := range cases {
		if got := discoveryEnabled(c.d); got != c.want {
			t.Errorf("discoveryEnabled(%+v) = %v, want %v", c.d, got, c.want)
		}
	}
}

func TestNewDiscovererDNS(t *testing.T) {
	d, err := newDiscoverer(config.DiscoveryConfig{Type: "dns", Target: "svc.local:8080"}, nil)
	if err != nil {
		t.Fatalf("newDiscoverer dns: %v", err)
	}
	if d.Describe() != "dns:svc.local:8080" {
		t.Fatalf("Describe = %q", d.Describe())
	}

	if _, err := newDiscoverer(config.DiscoveryConfig{Type: "dns", Target: "no-port"}, nil); err == nil {
		t.Fatal("dns target without port should error")
	}

	s, err := newDiscoverer(config.DiscoveryConfig{Type: "dns_srv", Target: "_grpc._tcp.svc.local"}, nil)
	if err != nil {
		t.Fatalf("newDiscoverer dns_srv: %v", err)
	}
	if s.Describe() != "dns_srv:_grpc._tcp.svc.local" {
		t.Fatalf("Describe = %q", s.Describe())
	}

	if _, err := newDiscoverer(config.DiscoveryConfig{Type: "bogus"}, nil); err == nil {
		t.Fatal("unknown discovery type should error")
	}
}

func TestRegistryRebuildsPoolOnDiscoveryChange(t *testing.T) {
	r := NewRegistry(RegistryOptions{})

	r.Begin()
	p1, err := r.For(discoveryCfg("dns", "svc.local:80"), "http")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	r.Commit()
	defer r.CloseAll()

	// Same discovery config: pool is reused.
	r.Begin()
	p2, err := r.For(discoveryCfg("dns", "svc.local:80"), "http")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	r.Commit()
	if p1 != p2 {
		t.Fatal("an unchanged discovery upstream should reuse its pool")
	}

	// Changed target: pool is rebuilt and the old one closed.
	r.Begin()
	p3, err := r.For(discoveryCfg("dns", "other.local:80"), "http")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	r.Commit()
	if p3 == p1 {
		t.Fatal("a changed discovery target should rebuild the pool")
	}
	if !closed(p1.Done()) {
		t.Fatal("the replaced discovery pool should be closed")
	}
}
