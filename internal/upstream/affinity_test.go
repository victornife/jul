// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"jul/internal/affinity"
	"jul/internal/config"
)

func hashPool(t testing.TB, key, name, fallback string, servers ...config.UpstreamServer) *Pool {
	t.Helper()
	p, err := NewPool(config.UpstreamConfig{
		Name:        "affine",
		Strategy:    "consistent_hash",
		Hash:        &config.HashConfig{Key: key, Name: name, Fallback: fallback},
		Servers:     servers,
		MaxFails:    1,
		FailTimeout: config.Duration(time.Minute),
	}, "http")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	return p
}

func servers(n int) []config.UpstreamServer {
	out := make([]config.UpstreamServer, n)
	for i := range out {
		out[i] = config.UpstreamServer{Address: fmt.Sprintf("10.9.0.%d:80", i+1), Weight: 1}
	}
	return out
}

func pickAddr(t testing.TB, p *Pool, key affinity.Key, excluded map[BackendIdentity]struct{}) string {
	t.Helper()
	at, err := p.PickKeyed(context.Background(), key, excluded)
	if err != nil {
		t.Fatalf("PickKeyed: %v", err)
	}
	p.Release(at.Backend)
	return at.Address
}

// expected ranks a pool's current backends for key with the affinity package
// directly, independent of the balancer under test.
func expected(p *Pool, key affinity.Key) []string {
	bs := p.Backends()
	cands := make([]affinity.Candidate, len(bs))
	for i, b := range bs {
		cands[i] = affinity.NewCandidate(b.Network, b.Address, b.Weight())
	}
	order := affinity.Rank(key.Sum(), cands)
	out := make([]string, len(order))
	for i, idx := range order {
		out[i] = bs[idx].Address
	}
	return out
}

func TestConsistentHashIsDeterministicAndMatchesRendezvous(t *testing.T) {
	p := hashPool(t, "header", "X-Tenant", "", servers(8)...)
	for i := 0; i < 500; i++ {
		key := affinity.KeyOf("tenant-" + strconv.Itoa(i))
		want := expected(p, key)[0]
		for j := 0; j < 3; j++ {
			if got := pickAddr(t, p, key, nil); got != want {
				t.Fatalf("key %d pick %d = %s, want %s", i, j, got, want)
			}
		}
	}
}

// Equivalent configurations in a different order, and a pool rebuilt from
// scratch (a restart or a reload that rebuilds), map every key identically.
func TestConsistentHashIndependentOfOrderAndRebuild(t *testing.T) {
	fwd := servers(16)
	rev := make([]config.UpstreamServer, len(fwd))
	for i := range fwd {
		rev[len(fwd)-1-i] = fwd[i]
	}
	a := hashPool(t, "client_ip", "", "", fwd...)
	b := hashPool(t, "client_ip", "", "", rev...)
	c := hashPool(t, "client_ip", "", "", fwd...)
	for i := 0; i < 1000; i++ {
		key := affinity.KeyOf("198.51.100." + strconv.Itoa(i%256) + "/" + strconv.Itoa(i))
		ga, gb, gc := pickAddr(t, a, key, nil), pickAddr(t, b, key, nil), pickAddr(t, c, key, nil)
		if ga != gb || ga != gc {
			t.Fatalf("key %d: %s / %s (reversed) / %s (rebuilt)", i, ga, gb, gc)
		}
	}
}

func TestConsistentHashMissingKeyUsesFallback(t *testing.T) {
	for _, tc := range []struct {
		fallback string
		check    func(t *testing.T, got []string)
	}{
		{"", func(t *testing.T, got []string) {
			if got[0] == got[1] {
				t.Fatalf("round_robin fallback repeated %s", got[0])
			}
		}},
		{"round_robin", func(t *testing.T, got []string) {
			if got[0] == got[1] {
				t.Fatalf("round_robin fallback repeated %s", got[0])
			}
		}},
		{"least_conn", func(t *testing.T, got []string) {}},
		{"weighted_round_robin", func(t *testing.T, got []string) {}},
	} {
		t.Run("fallback="+tc.fallback, func(t *testing.T) {
			p := hashPool(t, "header", "X-Tenant", tc.fallback, servers(4)...)
			got := make([]string, 4)
			for i := range got {
				// A missing key and a caller that passed no key at all are the
				// same: neither is hashed, so neither may pin to one backend.
				key := affinity.Key{}
				if i%2 == 0 {
					key = p.AffinityKey(httptest.NewRequest(http.MethodGet, "/", nil))
				}
				got[i] = pickAddr(t, p, key, nil)
			}
			tc.check(t, got)
		})
	}
}

// Many keyless requests must spread, not collapse onto the backend an empty
// string would hash to.
func TestConsistentHashMissingKeysDoNotPileUp(t *testing.T) {
	p := hashPool(t, "cookie", "sid", "", servers(4)...)
	counts := map[string]int{}
	for i := 0; i < 400; i++ {
		counts[pickAddr(t, p, p.AffinityKey(httptest.NewRequest(http.MethodGet, "/", nil)), nil)]++
	}
	if len(counts) != 4 {
		t.Fatalf("missing-key requests reached %d backends, want all 4: %v", len(counts), counts)
	}
	for addr, n := range counts {
		if n != 100 {
			t.Errorf("%s took %d of 400 keyless requests under round_robin fallback", addr, n)
		}
	}
}

// Health: an ejected preferred backend is skipped for the key's next choice;
// it gets its keys back when it recovers. No separate affinity state exists to
// disagree with the checker.
func TestConsistentHashSkipsUnhealthyAndReturnsOnRecovery(t *testing.T) {
	p := hashPool(t, "header", "X-Tenant", "", servers(5)...)
	key := affinity.KeyOf("tenant-health")
	order := expected(p, key)
	preferred := backendAt(p, order[0])

	preferred.setActiveHealthy(false)
	if got := pickAddr(t, p, key, nil); got != order[1] {
		t.Fatalf("with preferred ejected picked %s, want next-ranked %s", got, order[1])
	}
	preferred.setActiveHealthy(true)
	if got := pickAddr(t, p, key, nil); got != order[0] {
		t.Fatalf("after recovery picked %s, want preferred %s back", got, order[0])
	}
}

// Circuit: an open circuit removes the preferred backend exactly as health
// does; half-open admits at most the configured probes and every other keyed
// request falls through to the next-ranked backend.
func TestConsistentHashRespectsCircuitAndHalfOpenProbes(t *testing.T) {
	p := hashPool(t, "header", "X-Tenant", "", servers(3)...)
	p.setCircuitLimits(circuitParams{maxFails: 1, failTimeout: time.Second, halfOpenProbes: 1})
	key := affinity.KeyOf("tenant-circuit")
	order := expected(p, key)
	preferred := backendAt(p, order[0])
	clk := fakeBackendClock(preferred)

	p.MarkFailure(admitOn(t, preferred))
	if got := pickAddr(t, p, key, nil); got != order[1] {
		t.Fatalf("open circuit: picked %s, want %s", got, order[1])
	}
	clk.advance(time.Second) // half-open

	probe, err := p.PickKeyed(context.Background(), key, nil)
	if err != nil || probe.Address != order[0] {
		t.Fatalf("half-open: first keyed pick = %v %v, want probe on %s", probe.Address, err, order[0])
	}
	// The single probe slot is held, so the key's other requests go on.
	for i := 0; i < 5; i++ {
		if got := pickAddr(t, p, key, nil); got != order[1] {
			t.Fatalf("while probing picked %s, want %s", got, order[1])
		}
	}
	p.MarkSuccess(probe)
	p.Release(probe.Backend)
	if got := pickAddr(t, p, key, nil); got != order[0] {
		t.Fatalf("after successful probe picked %s, want %s", got, order[0])
	}
}

// Capacity: a saturated preferred backend spills the key to the next-ranked
// backend instead of queuing behind it.
func TestConsistentHashSpillsPastSaturatedBackend(t *testing.T) {
	one := 1
	p, err := NewPool(config.UpstreamConfig{
		Name: "affine", Strategy: "consistent_hash",
		Hash:       &config.HashConfig{Key: "header", Name: "X-Tenant"},
		Servers:    servers(3),
		Resilience: &config.ResilienceConfig{MaxActivePerBackend: one},
	}, "http")
	if err != nil {
		t.Fatal(err)
	}
	key := affinity.KeyOf("tenant-capacity")
	order := expected(p, key)
	held, err := p.PickKeyed(context.Background(), key, nil)
	if err != nil || held.Address != order[0] {
		t.Fatalf("first pick %s %v, want %s", held.Address, err, order[0])
	}
	if got := pickAddr(t, p, key, nil); got != order[1] {
		t.Fatalf("with preferred at capacity picked %s, want %s", got, order[1])
	}
	p.Release(held.Backend)
}

// Retry: Do places attempt 1 on the key's preferred backend and each retry on
// the next untried backend in the key's rendezvous order.
func TestConsistentHashRetryWalksRankedOrder(t *testing.T) {
	p := hashPool(t, "header", "X-Tenant", "", servers(4)...)
	key := affinity.KeyOf("tenant-retry")
	order := expected(p, key)
	var visited []string
	rr := p.RetryRequestFor(RetryOverride{}, true)
	rr.Key = key
	reason, err := p.Do(context.Background(), rr, func(_ context.Context, b Attempt, n int) AttemptResult {
		visited = append(visited, b.Address)
		if n < 3 {
			return AttemptResult{Err: errors.New("dial refused")}
		}
		return AttemptResult{}
	})
	if err != nil || reason != StopSuccess {
		t.Fatalf("Do = %v %v", reason, err)
	}
	if fmt.Sprint(visited) != fmt.Sprint(order[:3]) {
		t.Fatalf("attempts visited %v, want rendezvous order %v", visited, order[:3])
	}
}

// Discovery: an equivalent target set in another order remaps nothing; adding
// and removing a target moves only the keys the algorithm must move; a weight
// change is applied in place and shifts keys only toward or away from that
// backend; an address change is a new identity.
func TestConsistentHashDiscoveryRemapping(t *testing.T) {
	p, err := NewPool(config.UpstreamConfig{
		Name: "affine", Strategy: "consistent_hash",
		Hash:      &config.HashConfig{Key: "client_ip"},
		Discovery: &config.DiscoveryConfig{Type: "dns", Target: "svc.test:80"},
	}, "http")
	if err != nil {
		t.Fatal(err)
	}
	targets := func(n int, reverse bool) []Target {
		out := make([]Target, n)
		for i := range out {
			j := i
			if reverse {
				j = n - 1 - i
			}
			out[i] = Target{Address: fmt.Sprintf("10.8.0.%d:80", j+1), Weight: 1}
		}
		return out
	}
	const keys = 20000
	snapshot := func() []string {
		out := make([]string, keys)
		for i := range out {
			out[i] = pickAddr(t, p, affinity.KeyOf("k"+strconv.Itoa(i)), nil)
		}
		return out
	}
	diff := func(a, b []string) (n int) {
		for i := range a {
			if a[i] != b[i] {
				n++
			}
		}
		return n
	}

	p.UpdateTargets(targets(8, false))
	base := snapshot()

	p.UpdateTargets(targets(8, true))
	if n := diff(base, snapshot()); n != 0 {
		t.Fatalf("reordered equivalent discovery result remapped %d keys", n)
	}

	p.UpdateTargets(targets(9, false))
	grown := snapshot()
	moved := diff(base, grown)
	for i := range base {
		if base[i] != grown[i] && grown[i] != "10.8.0.9:80" {
			t.Fatalf("add: key moved between surviving backends %s -> %s", base[i], grown[i])
		}
	}
	t.Logf("discovery 8->9: %.2f%% remapped (ideal %.2f%%)", 100*float64(moved)/keys, 100.0/9)
	if f := float64(moved) / keys; f < 0.09 || f > 0.13 {
		t.Errorf("add: %.2f%% remapped, want ~11.1%%", 100*f)
	}

	p.UpdateTargets(targets(8, false))
	if n := diff(base, snapshot()); n != 0 {
		t.Fatalf("removing the added target did not restore the original mapping (%d differ)", n)
	}

	w := targets(8, false)
	w[0].Weight = 3
	p.UpdateTargets(w)
	reweighted := snapshot()
	for i := range base {
		if base[i] != reweighted[i] && reweighted[i] != "10.8.0.1:80" {
			t.Fatalf("weight up: key moved to an unchanged backend %s", reweighted[i])
		}
	}

	moved8 := targets(8, false)
	moved8[7].Address = "10.8.1.8:80"
	p.UpdateTargets(moved8)
	readdr := snapshot()
	for i := range base {
		if base[i] != readdr[i] && base[i] != "10.8.0.8:80" && readdr[i] != "10.8.1.8:80" {
			t.Fatalf("address change moved an unrelated key %s -> %s", base[i], readdr[i])
		}
	}
}

// Last-good: a failed or empty resolve keeps the backend set, so the mapping
// is untouched until a successful resolve changes membership.
func TestConsistentHashKeepsMappingAcrossFailedResolve(t *testing.T) {
	p, err := NewPool(config.UpstreamConfig{
		Name: "affine", Strategy: "consistent_hash",
		Hash:      &config.HashConfig{Key: "client_ip"},
		Discovery: &config.DiscoveryConfig{Type: "dns", Target: "svc.test:80"},
	}, "http")
	if err != nil {
		t.Fatal(err)
	}
	p.UpdateTargets([]Target{{Address: "10.7.0.1:80"}, {Address: "10.7.0.2:80"}, {Address: "10.7.0.3:80"}})
	key := affinity.KeyOf("203.0.113.9")
	before := pickAddr(t, p, key, nil)

	d := &flakyDiscoverer{err: errors.New("SERVFAIL")}
	ctx, epoch := p.beginDiscoveryGeneration()
	p.refreshOnce(ctx, epoch, d, DiscoveryHooks{}, nil)
	d.err, d.targets = nil, nil
	p.refreshOnce(ctx, epoch, d, DiscoveryHooks{}, nil) // empty result
	if after := pickAddr(t, p, key, nil); after != before || len(p.Backends()) != 3 {
		t.Fatalf("failed/empty resolve changed mapping %s -> %s (%d backends)", before, after, len(p.Backends()))
	}
	d.targets = []Target{{Address: "10.7.0.3:80"}, {Address: "10.7.0.1:80"}, {Address: "10.7.0.2:80"}}
	p.refreshOnce(ctx, epoch, d, DiscoveryHooks{}, nil)
	if after := pickAddr(t, p, key, nil); after != before {
		t.Fatalf("recovery with the same membership changed mapping %s -> %s", before, after)
	}
	p.StopDiscovery()
}

type flakyDiscoverer struct {
	targets []Target
	err     error
}

func (f *flakyDiscoverer) Resolve(context.Context) ([]Target, error) { return f.targets, f.err }
func (f *flakyDiscoverer) Describe() string                          { return "flaky" }

func TestConsistentHashSnapshotsUseTheSameMapping(t *testing.T) {
	p := hashPool(t, "header", "X-Tenant", "", servers(6)...)
	snap := p.Snapshot()
	ctx := WithSnapshot(context.Background(), SnapshotMap{snap.Key(): snap})
	for i := 0; i < 200; i++ {
		key := affinity.KeyOf("t" + strconv.Itoa(i))
		at, err := p.PickKeyed(ctx, key, nil)
		if err != nil {
			t.Fatal(err)
		}
		p.Release(at.Backend)
		if want := pickAddr(t, p, key, nil); at.Address != want {
			t.Fatalf("snapshot picked %s, live pool %s", at.Address, want)
		}
	}
	// A static snapshot built for a candidate server list hashes too.
	st := p.staticSnapshot(servers(6))
	at, err := st.pickKeyed(affinity.KeyOf("t1"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := pickAddr(t, p, affinity.KeyOf("t1"), nil); at.Address != want {
		t.Fatalf("static snapshot picked %s, want %s", at.Address, want)
	}
}

func TestAffinityKeyExtractionAndHook(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]int{}
	p := hashPool(t, "header", "X-Tenant", "", servers(2)...)
	p.SetAffinityHook(func(pool, status string) {
		mu.Lock()
		defer mu.Unlock()
		seen[pool+"/"+status]++
	})
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if k := p.AffinityKey(r); k.Status() != affinity.StatusMissing {
		t.Fatalf("missing header = %v", k.Status())
	}
	r.Header.Set("X-Tenant", "a")
	if k := p.AffinityKey(r); !k.Hashed() {
		t.Fatalf("present header = %v", k.Status())
	}
	r.Header.Add("X-Tenant", "b")
	if k := p.AffinityKey(r); k.Status() != affinity.StatusInvalid {
		t.Fatalf("repeated header = %v", k.Status())
	}
	if seen["affine/missing"] != 1 || seen["affine/hashed"] != 1 || seen["affine/invalid"] != 1 {
		t.Fatalf("hook counts = %v", seen)
	}

	ip := hashPool(t, "client_ip", "", "", servers(2)...)
	if k := ip.AffinityKeyForAddr(&net.TCPAddr{IP: net.ParseIP("192.0.2.4"), Port: 9}); !k.Hashed() || k.Sum() != affinity.Sum("192.0.2.4") {
		t.Fatalf("L4 key = %+v", k)
	}

	// A non-hashing pool extracts nothing and never calls a hook.
	rr := pool(t, "round_robin", servers(2)...)
	rr.SetAffinityHook(func(string, string) { t.Fatal("hook called on a round_robin pool") })
	if k := rr.AffinityKey(r); k.Status() != affinity.StatusNone {
		t.Fatalf("round_robin key = %v", k.Status())
	}
	if k := rr.AffinityKeyForAddr(&net.TCPAddr{IP: net.ParseIP("192.0.2.4")}); k.Status() != affinity.StatusNone {
		t.Fatalf("round_robin L4 key = %v", k.Status())
	}
	var nilPool *Pool
	if nilPool.Hashing() || rr.HashStatus() != nil {
		t.Fatal("non-hashing pools report hash status")
	}
}

func TestHashStatusAndRegistryShape(t *testing.T) {
	p := hashPool(t, "cookie", "sid", "", servers(2)...)
	st := p.HashStatus()
	if st == nil || st.Key != "cookie" || st.Name != "sid" || st.Fallback != "round_robin" || st.Algorithm != "rendezvous_v1" {
		t.Fatalf("HashStatus = %+v", st)
	}

	r := NewRegistry(RegistryOptions{})
	cfg := config.UpstreamConfig{
		Name: "api", Strategy: "consistent_hash",
		Hash:    &config.HashConfig{Key: "header", Name: "X-Tenant"},
		Servers: servers(2), MaxFails: 1,
	}
	r.Begin()
	p1, err := r.For(context.Background(), cfg, "http")
	if err != nil {
		t.Fatal(err)
	}
	r.Commit()
	snap := r.Snapshot()
	if len(snap) != 1 || snap[0].Hash == nil || snap[0].Hash.Name != "X-Tenant" {
		t.Fatalf("registry snapshot hash = %+v", snap)
	}

	// Same hash block: reused. Different key: rebuilt, like a strategy change.
	r.Begin()
	p2, _ := r.For(context.Background(), cfg, "http")
	r.Commit()
	if p1 != p2 {
		t.Fatal("unchanged consistent_hash pool was rebuilt")
	}
	cfg.Hash = &config.HashConfig{Key: "cookie", Name: "sid"}
	r.Begin()
	p3, _ := r.For(context.Background(), cfg, "http")
	r.Commit()
	if p3 == p2 {
		t.Fatal("changed hash key reused the old pool")
	}
	r.CloseAll()
}

// Concurrent keyed selection and retries racing membership replacement and
// health flips. Run under -race: the assertions are that it is race-free and
// that no in-flight slot leaks across the churn.
func TestConsistentHashConcurrentSelectionUnderChurn(t *testing.T) {
	p := hashPool(t, "header", "X-Tenant", "", servers(8)...)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			n := 6 + i%4
			p.UpdateBackends(servers(n))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			bs := p.Backends()
			bs[i%len(bs)].setActiveHealthy(i%3 != 0)
		}
	}()
	var pickers sync.WaitGroup
	for g := 0; g < 8; g++ {
		pickers.Add(1)
		go func(g int) {
			defer pickers.Done()
			for i := 0; i < 2000; i++ {
				key := affinity.KeyOf(fmt.Sprintf("g%d-%d", g, i%50))
				rr := p.RetryRequestFor(RetryOverride{Attempts: 2}, true)
				rr.Key = key
				_, _ = p.Do(context.Background(), rr, func(_ context.Context, b Attempt, n int) AttemptResult {
					if n == 1 && i%5 == 0 {
						return AttemptResult{Err: errors.New("boom")}
					}
					return AttemptResult{}
				})
			}
		}(g)
	}
	pickers.Wait()
	close(stop)
	wg.Wait()
	for _, b := range p.Backends() {
		if b.Inflight() != 0 {
			t.Fatalf("%s leaked %d in-flight slots", b.Address, b.Inflight())
		}
	}
}

func backendAt(p *Pool, addr string) *Backend {
	for _, b := range p.Backends() {
		if b.Address == addr {
			return b
		}
	}
	return nil
}

func TestRendezvousPickKeyEmpty(t *testing.T) {
	r := &rendezvous{fallback: &roundRobin{}}
	if r.pickKey(nil, 1) != nil {
		t.Fatal("pickKey of nothing returned a backend")
	}
	if pickFor(r, nil, affinity.Key{}) != nil {
		t.Fatal("fallback pick of nothing returned a backend")
	}
}
