// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc

package transcode

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/backendtls"
	"jul/internal/config"
	"jul/internal/upstream"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
)

func newCacheTestConn(t *testing.T) *grpc.ClientConn {
	t.Helper()
	cc, err := grpc.NewClient("passthrough:///127.0.0.1:1",
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	return cc
}

func cacheEntryCounts(t *Transcoder) (active, retired int) {
	t.conns.Range(func(_, _ any) bool { active++; return true })
	t.retired.Range(func(_, _ any) bool { retired++; return true })
	return active, retired
}

// TestConnForConcurrentLogicalIdentitiesNeverShareConnection forces both
// callers past the empty-cache observation before either dial completes. A
// cache transition keyed only by dial identity returns the LoadOrStore winner
// to the losing logical identity.
func TestConnForConcurrentLogicalIdentitiesNeverShareConnection(t *testing.T) {
	dialled := []*grpc.ClientConn{newCacheTestConn(t), newCacheTestConn(t)}
	arrived := make(chan struct{}, len(dialled))
	release := make(chan struct{})
	var calls atomic.Int64

	tr := &Transcoder{}
	tr.dialConn = func(string, bool, *backendtls.Policy) (*grpc.ClientConn, error) {
		i := calls.Add(1) - 1
		arrived <- struct{}{}
		<-release
		return dialled[i], nil
	}

	type result struct {
		id   string
		conn *grpc.ClientConn
		err  error
	}
	results := make(chan result, 2)
	key := upstream.BackendIdentity{Scheme: "http", Network: "tcp", Address: "127.0.0.1:1"}
	for _, id := range []string{"pod-a", "pod-b"} {
		go func(id string) {
			conn, err := tr.connFor(key, id)
			results <- result{id: id, conn: conn, err: err}
		}(id)
	}
	<-arrived
	<-arrived
	close(release)

	got := map[string]*grpc.ClientConn{}
	for range 2 {
		res := <-results
		if res.err != nil {
			t.Fatalf("connFor(%s): %v", res.id, res.err)
		}
		got[res.id] = res.conn
	}
	if got["pod-a"] == got["pod-b"] {
		t.Fatal("two logical workload identities received the same cached connection")
	}
}

func TestConnForSameIdentityClosesLosingSpeculativeDial(t *testing.T) {
	dialled := []*grpc.ClientConn{newCacheTestConn(t), newCacheTestConn(t)}
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	var calls atomic.Int64
	closed := make(map[*grpc.ClientConn]int)
	var closeMu sync.Mutex
	tr := &Transcoder{dialConn: func(string, bool, *backendtls.Policy) (*grpc.ClientConn, error) {
		i := calls.Add(1) - 1
		arrived <- struct{}{}
		<-release
		return dialled[i], nil
	}, closeConn: func(conn *grpc.ClientConn) error {
		closeMu.Lock()
		closed[conn]++
		closeMu.Unlock()
		return conn.Close()
	}}
	key := upstream.BackendIdentity{Scheme: "http", Network: "tcp", Address: "127.0.0.1:1"}
	results := make(chan *grpc.ClientConn, 2)
	for range 2 {
		go func() {
			conn, _ := tr.connFor(key, "pod-a")
			results <- conn
		}()
	}
	<-arrived
	<-arrived
	close(release)
	first, second := <-results, <-results
	if first != second {
		t.Fatal("same logical identity did not converge on one cached connection")
	}
	shutdown := 0
	for _, conn := range dialled {
		if conn.GetState() == connectivity.Shutdown {
			shutdown++
		}
	}
	if shutdown != 1 {
		t.Fatalf("closed speculative dials = %d, want exactly one loser", shutdown)
	}
	closeMu.Lock()
	closeCalls := closed[dialled[0]] + closed[dialled[1]]
	closeMu.Unlock()
	if closeCalls != 1 {
		t.Fatalf("speculative close calls = %d, want exactly one", closeCalls)
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	closeMu.Lock()
	defer closeMu.Unlock()
	for i, conn := range dialled {
		if closed[conn] != 1 {
			t.Fatalf("connection %d close calls = %d, want exactly one", i, closed[conn])
		}
	}
}

func TestConnCacheAddressReusePreservesPerGenerationGrace(t *testing.T) {
	connections := []*grpc.ClientConn{newCacheTestConn(t), newCacheTestConn(t)}
	var calls atomic.Int64
	tr := &Transcoder{dialConn: func(string, bool, *backendtls.Policy) (*grpc.ClientConn, error) {
		return connections[calls.Add(1)-1], nil
	}}
	key := upstream.BackendIdentity{Scheme: "http", Network: "tcp", Address: "127.0.0.1:1"}
	a, err := tr.connFor(key, "pod-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := tr.connFor(key, "pod-b")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("A -> B address reuse crossed logical identities")
	}
	again, err := tr.connFor(key, "pod-a")
	if err != nil {
		t.Fatal(err)
	}
	if again != a {
		t.Fatal("A -> B -> A did not promote A's own grace-period connection")
	}
	if _, ok := tr.retired.Load(connectionIdentity{dial: key, logicalID: "pod-b"}); !ok {
		t.Fatal("B generation disappeared when A was re-promoted")
	}
	if active, retired := cacheEntryCounts(tr); active != 1 || retired != 1 {
		t.Fatalf("cache counts = active %d, retired %d; want 1/1", active, retired)
	}
}

func TestEvictionCannotDeleteConcurrentReplacementInstallation(t *testing.T) {
	key := upstream.BackendIdentity{Scheme: "http", Network: "tcp", Address: "127.0.0.1:1"}
	pool, err := upstream.NewPool(config.UpstreamConfig{
		Name:     "cache-eviction-race",
		Strategy: "round_robin",
		Servers:  []config.UpstreamServer{{Address: key.Address, Weight: 1}},
	}, "http")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	pool.UpdateTargets([]upstream.Target{{Address: key.Address, ID: "pod-a"}})

	a, b := newCacheTestConn(t), newCacheTestConn(t)
	tr := &Transcoder{pool: pool, dialConn: func(string, bool, *backendtls.Policy) (*grpc.ClientConn, error) {
		return a, nil
	}}
	if _, err := tr.connFor(key, "pod-a"); err != nil {
		t.Fatal(err)
	}
	pool.UpdateTargets([]upstream.Target{{Address: key.Address, ID: "pod-b"}})

	started := make(chan struct{})
	release := make(chan struct{})
	tr.dialConn = func(string, bool, *backendtls.Policy) (*grpc.ClientConn, error) {
		close(started)
		<-release
		return b, nil
	}
	result := make(chan error, 1)
	go func() {
		_, err := tr.connFor(key, "pod-b")
		result <- err
	}()
	<-started

	// Reconcile the replacement while its fresh dial is still in flight. The
	// old implementation's address-wide Delete could erase the replacement
	// installed immediately afterward.
	tr.evictStaleConns()
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	active, ok := tr.conns.Load(connectionIdentity{dial: key, logicalID: "pod-b"})
	if !ok || active.(*cachedConn).conn != b {
		t.Fatal("eviction removed or replaced the concurrently installed pod-b connection")
	}
	if _, ok := tr.retired.Load(connectionIdentity{dial: key, logicalID: "pod-a"}); !ok {
		t.Fatal("pod-a connection was not retained under its own retired identity")
	}
}

func TestConnCacheChurnExpiresToLiveBaselineAndCloseReleasesAll(t *testing.T) {
	key := upstream.BackendIdentity{Scheme: "http", Network: "tcp", Address: "127.0.0.1:1"}
	pool, err := upstream.NewPool(config.UpstreamConfig{
		Name:     "cache-churn",
		Strategy: "round_robin",
		Servers:  []config.UpstreamServer{{Address: key.Address, Weight: 1}},
	}, "http")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	now := time.Unix(1_000, 0)
	var dialled []*grpc.ClientConn
	closed := make(map[*grpc.ClientConn]int)
	tr := &Transcoder{
		pool: pool,
		now:  func() time.Time { return now },
		closeConn: func(conn *grpc.ClientConn) error {
			closed[conn]++
			return conn.Close()
		},
	}
	tr.dialConn = func(string, bool, *backendtls.Policy) (*grpc.ClientConn, error) {
		conn := newCacheTestConn(t)
		dialled = append(dialled, conn)
		return conn, nil
	}

	const generations = 24
	for i := range generations {
		id := fmt.Sprintf("pod-%02d", i)
		if _, err := tr.connFor(key, id); err != nil {
			t.Fatal(err)
		}
	}
	if active, retired := cacheEntryCounts(tr); active != 1 || retired != generations-1 {
		t.Fatalf("pre-expiry cache counts = active %d, retired %d; want 1/%d", active, retired, generations-1)
	}
	finalID := fmt.Sprintf("pod-%02d", generations-1)
	pool.UpdateTargets([]upstream.Target{{Address: key.Address, ID: finalID}})
	now = now.Add(retiredConnGrace)
	tr.evictStaleConns()
	if active, retired := cacheEntryCounts(tr); active != 1 || retired != 0 {
		t.Fatalf("post-expiry cache counts = active %d, retired %d; want live baseline 1/0", active, retired)
	}
	for i, conn := range dialled[:len(dialled)-1] {
		if conn.GetState() != connectivity.Shutdown {
			t.Fatalf("expired retired connection %d remains open", i)
		}
		if closed[conn] != 1 {
			t.Fatalf("expired retired connection %d close calls = %d, want 1", i, closed[conn])
		}
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if active, retired := cacheEntryCounts(tr); active != 0 || retired != 0 {
		t.Fatalf("closed cache counts = active %d, retired %d; want 0/0", active, retired)
	}
	if dialled[len(dialled)-1].GetState() != connectivity.Shutdown {
		t.Fatal("Close did not release active connection")
	}
	if closed[dialled[len(dialled)-1]] != 1 {
		t.Fatalf("active connection close calls = %d, want 1", closed[dialled[len(dialled)-1]])
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	for i, conn := range dialled {
		if closed[conn] != 1 {
			t.Fatalf("connection %d close calls after repeated Close = %d, want 1", i, closed[conn])
		}
	}
}

func TestConnCacheChurnHasHardRetiredBound(t *testing.T) {
	key := upstream.BackendIdentity{Scheme: "http", Network: "tcp", Address: "127.0.0.1:1"}
	now := time.Unix(2_000, 0)
	closed := make(map[*grpc.ClientConn]int)
	var dialled []*grpc.ClientConn
	tr := &Transcoder{
		now: func() time.Time { return now },
		closeConn: func(conn *grpc.ClientConn) error {
			closed[conn]++
			return conn.Close()
		},
	}
	tr.dialConn = func(string, bool, *backendtls.Policy) (*grpc.ClientConn, error) {
		conn := newCacheTestConn(t)
		dialled = append(dialled, conn)
		return conn, nil
	}

	const overflow = 8
	for i := range maxRetiredConns + overflow + 1 {
		if _, err := tr.connFor(key, fmt.Sprintf("hostile-%03d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if active, retired := cacheEntryCounts(tr); active != 1 || retired != maxRetiredConns {
		t.Fatalf("bounded cache counts = active %d, retired %d; want 1/%d", active, retired, maxRetiredConns)
	}
	closedBeforeGrace := 0
	for _, count := range closed {
		closedBeforeGrace += count
	}
	if closedBeforeGrace != overflow {
		t.Fatalf("early closes at hard bound = %d, want %d", closedBeforeGrace, overflow)
	}

	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	for i, conn := range dialled {
		if closed[conn] != 1 {
			t.Fatalf("connection %d close calls = %d, want exactly one", i, closed[conn])
		}
	}
}

// TestConnForSameIdentityRetirementRaceDoesNotCloseEarly reproduces the
// A1/A2/B interleaving: two concurrent speculative dials for the same
// logical identity race a third identity's dial at the same address.
//
//	A1, A2 := connFor(X, A) (both racing, speculative dial in flight)
//	A1 finishes first -> active(A) = A1
//	B := connFor(X, B) -> retires A1 -> retired(A) = A1, active(B) = B
//	A2 finishes -> post-dial revalidation must see retired(A1) (not just
//	active), promote/reuse it, and retire B in turn.
//
// A post-dial check that only inspects the active map instead publishes A2 as
// a second, redundant connection for identity A and retires B, leaving
// active(A2) + retired(A1) + retired(B): a stale-eviction sweep can then close
// the still in-flight A1 connection before its retirement grace expires.
func TestConnForSameIdentityRetirementRaceDoesNotCloseEarly(t *testing.T) {
	key := upstream.BackendIdentity{Scheme: "http", Network: "tcp", Address: "127.0.0.1:1"}
	connA1, connA2, connB := newCacheTestConn(t), newCacheTestConn(t), newCacheTestConn(t)

	// releases[0] gates whichever of the two concurrent identity-A dials
	// reaches dialConn first; releases[1] gates the other. Which physical
	// goroutine gets which slot does not matter: the test only cares about
	// release ORDER (first released becomes the initial active winner).
	releases := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
	dialConns := [2]*grpc.ClientConn{connA1, connA2}
	var calls atomic.Int64
	arrived := make(chan struct{}, 2)

	var clockMu sync.Mutex
	now := time.Unix(9_000, 0)
	closed := make(map[*grpc.ClientConn]int)
	var closeMu sync.Mutex

	pool, err := upstream.NewPool(config.UpstreamConfig{
		Name:     "cache-retirement-race",
		Strategy: "round_robin",
		Servers:  []config.UpstreamServer{{Address: key.Address, Weight: 1}},
	}, "http")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	tr := &Transcoder{
		pool: pool,
		now:  func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return now },
		closeConn: func(conn *grpc.ClientConn) error {
			closeMu.Lock()
			closed[conn]++
			closeMu.Unlock()
			return conn.Close()
		},
	}
	tr.dialConn = func(string, bool, *backendtls.Policy) (*grpc.ClientConn, error) {
		i := calls.Add(1) - 1
		if i < int64(len(releases)) {
			arrived <- struct{}{}
			<-releases[i]
			return dialConns[i], nil
		}
		return connB, nil // identity B's dial: uncontested, returns immediately.
	}

	resultA := make(chan *grpc.ClientConn, 2)
	for range 2 {
		go func() {
			conn, err := tr.connFor(key, "A")
			if err != nil {
				t.Error(err)
				resultA <- nil
				return
			}
			resultA <- conn
		}()
	}
	<-arrived
	<-arrived

	// 1. Release the first identity-A dial; it must become active.
	close(releases[0])
	firstActive := <-resultA
	if got, ok := tr.conns.Load(connectionIdentity{dial: key, logicalID: "A"}); !ok || got.(*cachedConn).conn != firstActive {
		t.Fatal("first identity-A dial did not become active")
	}

	// 2. Request B and let it replace/retire A while the second A dial is
	// still blocked.
	bConn, err := tr.connFor(key, "B")
	if err != nil {
		t.Fatal(err)
	}
	if bConn != connB {
		t.Fatal("unexpected connection returned for identity B")
	}
	if _, ok := tr.retired.Load(connectionIdentity{dial: key, logicalID: "A"}); !ok {
		t.Fatal("identity A was not retired when B became active")
	}

	// 3. Release the second (now stale) identity-A speculative dial.
	close(releases[1])
	secondActive := <-resultA
	if secondActive != firstActive {
		t.Fatal("the second A dial did not converge on the still-unexpired retired A connection")
	}
	closeMu.Lock()
	loserCloses := closed[dialConns[1]]
	closeMu.Unlock()
	if loserCloses != 1 {
		t.Fatalf("losing speculative A dial close calls = %d, want exactly 1", loserCloses)
	}

	// Cache must now read: active(A) = the promoted connection, retired(B).
	if active, ok := tr.conns.Load(connectionIdentity{dial: key, logicalID: "A"}); !ok || active.(*cachedConn).conn != firstActive {
		t.Fatal("identity A is not the sole active entry after the race")
	}
	if _, ok := tr.conns.Load(connectionIdentity{dial: key, logicalID: "B"}); ok {
		t.Fatal("identity B is still active after A was promoted back")
	}
	if _, ok := tr.retired.Load(connectionIdentity{dial: key, logicalID: "B"}); !ok {
		t.Fatal("identity B was not retired when A was promoted back to active")
	}
	if _, ok := tr.retired.Load(connectionIdentity{dial: key, logicalID: "A"}); ok {
		t.Fatal("identity A is still retired after being promoted back to active")
	}

	// 4. Run eviction before B's retirement grace expires. The connection
	// held by the first A caller must remain open: this is the exact
	// defect, an eviction sweep must never close a same-identity connection
	// just because it was briefly retired and then promoted back.
	pool.UpdateTargets([]upstream.Target{{Address: key.Address, ID: "A"}})
	tr.evictStaleConns()
	closeMu.Lock()
	earlyCloses := closed[firstActive]
	closeMu.Unlock()
	if earlyCloses != 0 {
		t.Fatalf("in-flight A connection closed %d time(s) before retirement grace expired", earlyCloses)
	}
	if firstActive.GetState() == connectivity.Shutdown {
		t.Fatal("in-flight A connection was shut down before retirement grace expired")
	}
	if active, retired := cacheEntryCounts(tr); active != 1 || retired != 1 {
		t.Fatalf("pre-expiry cache counts = active %d, retired %d; want 1/1", active, retired)
	}

	// 5. Advance the clock through B's retirement grace and evict again:
	// retired connections must close exactly once, and cardinality returns
	// to the live baseline.
	clockMu.Lock()
	now = now.Add(retiredConnGrace)
	clockMu.Unlock()
	tr.evictStaleConns()
	closeMu.Lock()
	bCloses := closed[connB]
	aCloses := closed[firstActive]
	closeMu.Unlock()
	if bCloses != 1 {
		t.Fatalf("retired B connection close calls = %d, want exactly 1 after grace expiry", bCloses)
	}
	if aCloses != 0 {
		t.Fatalf("active A connection was closed by eviction: %d calls, want 0", aCloses)
	}
	if active, retired := cacheEntryCounts(tr); active != 1 || retired != 0 {
		t.Fatalf("post-expiry cache counts = active %d, retired %d; want baseline 1/0", active, retired)
	}
}
