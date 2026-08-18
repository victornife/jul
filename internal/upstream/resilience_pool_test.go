// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"errors"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/resilience"
)

func resiliencePool(t *testing.T, addrs []string, r *config.ResilienceConfig) *Pool {
	t.Helper()
	servers := make([]config.UpstreamServer, 0, len(addrs))
	for _, a := range addrs {
		servers = append(servers, config.UpstreamServer{Address: a, Weight: 1})
	}
	p, err := NewPool(config.UpstreamConfig{
		Name:       "api",
		Strategy:   "round_robin",
		Servers:    servers,
		MaxFails:   3,
		Resilience: r,
	}, "http")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

// TestPoolDefaultsAreUnlimited pins the compatibility promise: an upstream with
// no resilience block behaves exactly as before this slice existed.
func TestPoolDefaultsAreUnlimited(t *testing.T) {
	p := resiliencePool(t, []string{"127.0.0.1:1"}, nil)
	pol := p.Policy()
	if pol.MaxActiveRequests() != 0 || pol.MaxActivePerBackend() != 0 || pol.MaxPendingRequests() != 0 || pol.PendingTimeout() != 0 {
		t.Fatalf("default policy is not unlimited: %+v", pol)
	}
	if pol.Bounded() {
		t.Fatal("default policy reports Bounded")
	}
	// The same backend can be picked without limit.
	for i := 0; i < 50; i++ {
		if _, err := p.Pick(); err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
	}
}

// TestPerBackendLimitIsSelectionFilter proves max_active_per_backend removes a
// saturated backend from selection instead of queueing behind it. Nested
// waiting inside selection is a deadlock generator, so the observable contract
// is that Pick fails fast with a distinct error rather than blocking.
func TestPerBackendLimitIsSelectionFilter(t *testing.T) {
	p := resiliencePool(t, []string{"127.0.0.1:1", "127.0.0.1:2"},
		&config.ResilienceConfig{MaxActivePerBackend: 2})

	picked := make([]Attempt, 0, 4)
	for i := 0; i < 4; i++ {
		b, err := p.Pick()
		if err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
		picked = append(picked, b)
	}
	// Both backends are now at 2 in flight, so the pool is at capacity.
	if _, err := p.Pick(); !errors.Is(err, ErrBackendAtCapacity) {
		t.Fatalf("pick at capacity: err = %v, want ErrBackendAtCapacity", err)
	}

	// Distinct from "no healthy backend": the two conditions call for opposite
	// operator responses, so they must not share an error.
	if errors.Is(ErrBackendAtCapacity, ErrNoAvailableBackend) {
		t.Fatal("ErrBackendAtCapacity must not match ErrNoAvailableBackend")
	}

	picked[0].Release()
	if _, err := p.Pick(); err != nil {
		t.Fatalf("pick after release: %v", err)
	}
}

// TestPerBackendLimitPrefersUnsaturatedBackend proves the filter composes with
// balancing rather than overriding it: traffic moves to the backend with room.
func TestPerBackendLimitPrefersUnsaturatedBackend(t *testing.T) {
	p := resiliencePool(t, []string{"127.0.0.1:1", "127.0.0.1:2"},
		&config.ResilienceConfig{MaxActivePerBackend: 1})

	first, err := p.Pick()
	if err != nil {
		t.Fatalf("first pick: %v", err)
	}
	second, err := p.Pick()
	if err != nil {
		t.Fatalf("second pick: %v", err)
	}
	if first.Address == second.Address {
		t.Fatalf("both picks landed on %s despite max_active_per_backend = 1", first.Address)
	}
}

// TestPoolPolicySwapPreservesCounters is the reload invariant that motivates
// keeping the policy out of upstreamMeta: swapping limits must not rebuild the
// pool, because a rebuild would discard exactly the accounting the limits
// govern.
func TestPoolPolicySwapPreservesCounters(t *testing.T) {
	p := resiliencePool(t, []string{"127.0.0.1:1"},
		&config.ResilienceConfig{MaxActiveRequests: 4})

	rel, err := p.Admission().Admit(context.Background(), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	b, err := p.Pick()
	if err != nil {
		t.Fatalf("pick: %v", err)
	}

	next, err := resilience.Resolve(resilience.Options{MaxActiveRequests: 64})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	p.SetPolicy(next)

	if got := p.Admission().Active(); got != 1 {
		t.Fatalf("active after policy swap = %d, want 1", got)
	}
	if got := b.Inflight(); got != 1 {
		t.Fatalf("backend inflight after policy swap = %d, want 1", got)
	}
	if got := p.Policy().MaxActiveRequests(); got != 64 {
		t.Fatalf("limit after swap = %d, want 64", got)
	}
	rel()
	b.Release()
}

// TestNewPoolRejectsIncoherentPolicy proves a malformed policy fails while the
// pool is being built, so a bad reload aborts instead of surfacing later as
// mysteriously rejected traffic.
func TestNewPoolRejectsIncoherentPolicy(t *testing.T) {
	_, err := NewPool(config.UpstreamConfig{
		Name:     "api",
		Servers:  []config.UpstreamServer{{Address: "127.0.0.1:1", Weight: 1}},
		MaxFails: 3,
		// A queue with no admission limit can never fill, so the pair is
		// incoherent rather than merely unusual.
		Resilience: &config.ResilienceConfig{MaxPendingRequests: 10},
	}, "http")
	if err == nil {
		t.Fatal("NewPool accepted max_pending_requests without max_active_requests")
	}
}

// TestHealthProbeTransportIsExemptFromConnectionBound pins that a saturated
// pool can still observe recovery.
//
// max_connections_per_backend binds the data-plane transport. The probe client
// is built by probeTransport, which never consults the resilience policy, so a
// pool whose sockets are all busy serving traffic can still dial a probe and
// notice a backend coming back. If probes ever shared the bound, a pool at its
// limit could never leave it.
func TestHealthProbeTransportIsExemptFromConnectionBound(t *testing.T) {
	p := resiliencePool(t, []string{"127.0.0.1:1"},
		&config.ResilienceConfig{MaxConnectionsPerBackend: 1})

	if got := p.Policy().MaxConnectionsPerBackend(); got != 1 {
		t.Fatalf("policy MaxConnectionsPerBackend = %d, want 1", got)
	}
	if got := probeTransport(time.Second, nil).MaxConnsPerHost; got != 0 {
		t.Fatalf("probe transport MaxConnsPerHost = %d, want 0: health checks must not compete with live traffic for sockets", got)
	}
}

// TestParseSocketAddressForms pins the three accepted address forms and the
// network each derives. One parser answers for upstream servers, FastCGI and
// uWSGI targets and health probes alike; a second would be a second answer.
func TestParseSocketAddressForms(t *testing.T) {
	cases := []struct{ in, network, address string }{
		{"unix:/run/php/php-fpm.sock", NetworkUnix, "/run/php/php-fpm.sock"},
		{"tcp://127.0.0.1:9000", NetworkTCP, "127.0.0.1:9000"},
		{"127.0.0.1:9000", NetworkTCP, "127.0.0.1:9000"},
		{"[::1]:9000", NetworkTCP, "[::1]:9000"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			network, address := ParseSocketAddress(c.in)
			if network != c.network || address != c.address {
				t.Fatalf("ParseSocketAddress(%q) = (%q, %q), want (%q, %q)", c.in, network, address, c.network, c.address)
			}
		})
	}
}

// TestNewBackendDerivesNetworkAndScheme pins the backend model: a unix backend
// carries its network, keeps its scheme without a URL to read it from, and is
// distinguishable by identity from a TCP backend that happens to share a string.
func TestNewBackendDerivesNetworkAndScheme(t *testing.T) {
	unix := newBackend(config.UpstreamServer{Address: "unix:/run/fpm.sock", Weight: 2}, "", circuitParams{maxFails: 1, failTimeout: time.Second, halfOpenProbes: 1})
	if unix.Network != NetworkUnix {
		t.Fatalf("unix backend network = %q, want %q", unix.Network, NetworkUnix)
	}
	if unix.Address != "/run/fpm.sock" {
		t.Fatalf("unix backend address = %q, want the socket path", unix.Address)
	}
	if unix.URL != nil {
		t.Fatal("a unix backend must not carry a URL; there is nothing meaningful to put in one")
	}
	if unix.Weight() != 2 {
		t.Fatalf("weight = %d, want 2", unix.Weight())
	}

	tcp := newBackend(config.UpstreamServer{Address: "10.0.0.1:80", Weight: 1}, "https", circuitParams{maxFails: 1, failTimeout: time.Second, halfOpenProbes: 1})
	if tcp.Network != NetworkTCP {
		t.Fatalf("tcp backend network = %q, want %q", tcp.Network, NetworkTCP)
	}
	if tcp.Scheme() != "https" {
		t.Fatalf("scheme = %q, want https", tcp.Scheme())
	}
	if tcp.URL == nil || tcp.URL.Host != "10.0.0.1:80" {
		t.Fatalf("tcp backend URL = %v, want host 10.0.0.1:80", tcp.URL)
	}

	// Identity carries the network, so two backends whose addresses collide as
	// strings are still two different places to dial.
	same := newBackend(config.UpstreamServer{Address: "/run/fpm.sock", Weight: 1}, "", circuitParams{maxFails: 1, failTimeout: time.Second, halfOpenProbes: 1})
	if same.Identity() == unix.Identity() {
		t.Fatal("a tcp and a unix backend sharing an address string share an identity")
	}
}

// TestUnixBackendIsSelectable proves a unix backend is a first-class pool
// member: it can be picked, counted and released like any other.
func TestUnixBackendIsSelectable(t *testing.T) {
	p := resiliencePool(t, []string{"unix:/run/a.sock", "unix:/run/b.sock"}, nil)

	seen := map[string]int{}
	for i := 0; i < 4; i++ {
		b, err := p.Pick()
		if err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
		if b.Network != NetworkUnix {
			t.Fatalf("picked backend network = %q, want unix", b.Network)
		}
		seen[b.Address]++
		b.Release()
	}
	if len(seen) != 2 {
		t.Fatalf("round robin visited %d of 2 unix backends: %v", len(seen), seen)
	}
}

// TestUnixBackendSurvivesDiscoveryRoundTrip pins that a reused discovery pool
// restages a unix backend as the same identity: backendsToServers must re-prefix
// the address, or the next newBackend would derive tcp and reset its state.
func TestUnixBackendSurvivesDiscoveryRoundTrip(t *testing.T) {
	p := resiliencePool(t, []string{"unix:/run/a.sock"}, nil)
	before := p.Backends()[0]
	before.acquire()

	p.UpdateBackends(backendsToServers(p.Backends()))

	after := p.Backends()[0]
	if after != before {
		t.Fatal("a unix backend was replaced by a round trip through backendsToServers; its accounting was discarded")
	}
	if after.Network != NetworkUnix {
		t.Fatalf("network after round trip = %q, want unix", after.Network)
	}
	after.Release()
}
