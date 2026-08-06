// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/background"
	"jul/internal/config"
)

// validationFixture seeds a stored no-cache entry, so every subsequent request
// takes the mandatory synchronous validation path.
func validationFixture(t *testing.T, gate func(r *http.Request), origin *originStub) (*Cache, http.Handler, *background.Group) {
	t.Helper()
	c, _ := conformanceCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(time.Hour)})
	g := testLease(t, 1)
	if origin.handler == nil {
		origin.handler = func(w http.ResponseWriter, r *http.Request) {
			if gate != nil {
				gate(r)
			}
			w.Header().Set("ETag", `"v1"`)
			w.Header().Set("Cache-Control", "no-cache")
			if r.Header.Get("If-None-Match") == `"v1"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			_, _ = w.Write([]byte("body"))
		}
	}
	h := c.Handler(origin)

	// Warm the entry through the handler so it carries the real policy metadata.
	warm := httptest.NewRequest(http.MethodGet, "http://x/doc", nil)
	h.ServeHTTP(httptest.NewRecorder(), leased(warm, g))
	return c, h, g
}

func leasedGet(t *testing.T, h http.Handler, g *background.Group, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, leased(httptest.NewRequest(http.MethodGet, target, nil), g))
	return rec
}

// TestConcurrentMandatoryValidatorsIssueOneOriginRequest proves the synchronous
// path joins #131's call state instead of adding a second singleflight.
func TestConcurrentMandatoryValidatorsIssueOneOriginRequest(t *testing.T) {
	release := make(chan struct{})
	arrived := make(chan struct{}, 1)
	origin := &originStub{}
	c, h, g := validationFixture(t, func(r *http.Request) {
		if r.Header.Get("If-None-Match") == "" {
			return // the warm-up fetch
		}
		select {
		case arrived <- struct{}{}:
		default:
		}
		<-release
	}, origin)

	const waiters = 16
	var wg sync.WaitGroup
	var revalidated atomic.Int32
	for i := 0; i < waiters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if leasedGet(t, h, g, "http://x/doc").Header().Get("X-Cache") == stateRevalidated {
				revalidated.Add(1)
			}
		}()
	}

	// Wait for the leader to reach the origin, then for every other request to
	// have joined its call state. Both are observable facts, so the barrier is
	// deterministic rather than a timed guess.
	<-arrived
	call := awaitJoined(t, c, revalidateKey{
		key: key(httptest.NewRequest(http.MethodGet, "http://x/doc", nil)),
		gen: g.Generation(),
	}, waiters-1)
	close(release)
	wg.Wait()

	if got := origin.count(); got != 2 {
		t.Errorf("origin calls = %d, want 2 (one warm-up + one shared validation)", got)
	}
	if got := revalidated.Load(); got != waiters {
		t.Errorf("%d of %d requests were served the validated entry", got, waiters)
	}
	if got := call.joined.Load(); got != waiters-1 {
		t.Errorf("joined waiters = %d, want %d", got, waiters-1)
	}
}

// awaitJoined blocks until n callers have joined the in-flight call for k. It
// asserts on state the cache publishes, so no test synchronization depends on a
// sleep long enough to be a flake.
func awaitJoined(t *testing.T, c *Cache, k revalidateKey, n int) *revalidateCall {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if call, ok := c.lookupRevalidation(k); ok && int(call.joined.Load()) >= n {
			return call
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("only %d callers joined the validation call, want %d", joinedCount(c, k), n)
	return nil
}

func joinedCount(c *Cache, k revalidateKey) int {
	if call, ok := c.lookupRevalidation(k); ok {
		return int(call.joined.Load())
	}
	return -1
}

// TestWaiterCancellationDoesNotCancelTheLeader is the requirement that the
// synchronous path must not turn one client's disconnect into everyone's
// failure.
func TestWaiterCancellationDoesNotCancelTheLeader(t *testing.T) {
	leaderIn := make(chan struct{})
	release := make(chan struct{})
	origin := &originStub{}
	_, h, g := validationFixture(t, func(r *http.Request) {
		if r.Header.Get("If-None-Match") == "" {
			return
		}
		close(leaderIn)
		<-release
	}, origin)

	// The leader starts and blocks in the origin.
	leaderDone := make(chan string, 1)
	go func() {
		leaderDone <- leasedGet(t, h, g, "http://x/doc").Header().Get("X-Cache")
	}()
	<-leaderIn

	// A waiter joins, then gives up.
	ctx, cancel := context.WithCancel(context.Background())
	waiterDone := make(chan struct{})
	go func() {
		defer close(waiterDone)
		r := leased(httptest.NewRequest(http.MethodGet, "http://x/doc", nil), g).WithContext(ctx)
		h.ServeHTTP(httptest.NewRecorder(), r.WithContext(background.WithLease(ctx, g)))
	}()
	// The waiter is either blocked in wait() or about to be; cancelling is safe
	// either way, and the assertion below is about the LEADER.
	cancel()
	<-waiterDone

	close(release)
	if got := <-leaderDone; got != stateRevalidated {
		t.Fatalf("leader X-Cache = %q, want REVALIDATED — a waiter's cancellation must not cancel it", got)
	}
	if origin.count() != 2 {
		t.Errorf("origin calls = %d, want 2", origin.count())
	}
}

// TestNoCallStateIsStrandedByAnyValidationOutcome walks the full outcome set and
// proves the leader always drops its call state and releases its waiters.
func TestNoCallStateIsStrandedByAnyValidationOutcome(t *testing.T) {
	cases := []struct {
		name    string
		handler func(w http.ResponseWriter, r *http.Request)
		panics  bool
	}{
		{
			name: "304",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("ETag", `"v1"`)
				w.Header().Set("Cache-Control", "no-cache")
				if r.Header.Get("If-None-Match") != "" {
					w.WriteHeader(http.StatusNotModified)
					return
				}
				_, _ = w.Write([]byte("body"))
			},
		},
		{
			name: "new representation",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("ETag", `"v1"`)
				w.Header().Set("Cache-Control", "no-cache")
				_, _ = w.Write([]byte("body"))
			},
		},
		{
			name: "uncacheable response",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("ETag", `"v1"`)
				if r.Header.Get("If-None-Match") == "" {
					w.Header().Set("Cache-Control", "no-cache")
					_, _ = w.Write([]byte("body"))
					return
				}
				w.Header().Set("Cache-Control", "no-store")
				_, _ = w.Write([]byte("uncacheable"))
			},
		},
		{
			name: "origin error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("If-None-Match") == "" {
					w.Header().Set("ETag", `"v1"`)
					w.Header().Set("Cache-Control", "no-cache")
					_, _ = w.Write([]byte("body"))
					return
				}
				w.WriteHeader(http.StatusBadGateway)
			},
		},
		{
			name: "leader panic",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("If-None-Match") == "" {
					w.Header().Set("ETag", `"v1"`)
					w.Header().Set("Cache-Control", "no-cache")
					_, _ = w.Write([]byte("body"))
					return
				}
				panic("origin exploded")
			},
			panics: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origin := &originStub{handler: tc.handler}
			c, h, g := validationFixture(t, nil, origin)

			run := func() {
				if tc.panics {
					defer func() { _ = recover() }()
				}
				leasedGet(t, h, g, "http://x/doc")
			}
			run()

			if n := c.inflightRevalidations(); n != 0 {
				t.Fatalf("%d validation call-state entries stranded", n)
			}
			waitLeaseReleased(t, g)
		})
	}
}

// TestValidationLeaderPanicReleasesWaitersWithoutLeakingThePanicValue proves a
// waiter is neither stranded nor told anything unbounded about the failure.
func TestValidationLeaderPanicReleasesWaitersWithoutLeakingThePanicValue(t *testing.T) {
	leaderIn := make(chan struct{})
	release := make(chan struct{})
	origin := &originStub{}
	c, h, g := validationFixture(t, func(r *http.Request) {
		if r.Header.Get("If-None-Match") == "" {
			return
		}
		select {
		case <-leaderIn:
		default:
			close(leaderIn)
			<-release
			panic("secret-panic-value")
		}
	}, origin)

	var outcomes []string
	var outMu sync.Mutex
	c.SetRevalidationObserver(func(o string) {
		outMu.Lock()
		outcomes = append(outcomes, o)
		outMu.Unlock()
	})

	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		defer func() { _ = recover() }()
		leasedGet(t, h, g, "http://x/doc")
	}()
	<-leaderIn

	waiterDone := make(chan string, 1)
	go func() {
		defer func() {
			if v := recover(); v != nil {
				waiterDone <- "panicked"
			}
		}()
		waiterDone <- leasedGet(t, h, g, "http://x/doc").Header().Get("X-Cache")
	}()

	close(release)
	<-leaderDone
	select {
	case got := <-waiterDone:
		if got == "panicked" {
			t.Fatal("the leader's panic reached a waiter")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a waiter was stranded by the leader's panic")
	}

	if n := c.inflightRevalidations(); n != 0 {
		t.Fatalf("%d call-state entries stranded after a panic", n)
	}
	outMu.Lock()
	defer outMu.Unlock()
	for _, o := range outcomes {
		if o == "secret-panic-value" {
			t.Fatal("the panic value was reported as an observability label")
		}
	}
}

// TestValidationIsIsolatedPerGeneration proves a reload installs a fresh key
// space, so a validation running on a retiring generation neither suppresses nor
// answers a request on the new one.
func TestValidationIsIsolatedPerGeneration(t *testing.T) {
	origin := &originStub{}
	_, h, oldGen := validationFixture(t, nil, origin)
	newGen := testLease(t, 2)

	before := origin.count()
	leasedGet(t, h, oldGen, "http://x/doc")
	leasedGet(t, h, newGen, "http://x/doc")

	if got := origin.count() - before; got != 2 {
		t.Errorf("origin calls across two generations = %d, want 2 — the key space must not be shared", got)
	}
}

// TestValidationWithoutAGenerationLeaseStillValidates proves the degraded path
// is a full origin fetch, never an unvalidated stored body.
func TestValidationWithoutAGenerationLeaseStillValidates(t *testing.T) {
	c, _ := conformanceCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(time.Hour)})
	origin := &originStub{handler: func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Cache-Control", "no-cache")
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write([]byte("body"))
	}}
	h := c.Handler(origin)

	// No lease is installed anywhere in this test.
	wantResult(t, get(t, h, "http://x/doc"), stateMiss, "body")
	wantResult(t, get(t, h, "http://x/doc"), stateRevalidated, "body")
	if origin.count() != 2 {
		t.Errorf("origin calls = %d, want 2 — the origin must be contacted with or without a lease", origin.count())
	}
	if n := c.inflightRevalidations(); n != 0 {
		t.Fatalf("%d call-state entries stranded without a lease", n)
	}
}

// TestRepeatedValidationDoesNotLeakCallState is the resource gate for the
// synchronous path.
func TestRepeatedValidationDoesNotLeakCallState(t *testing.T) {
	origin := &originStub{}
	c, h, g := validationFixture(t, nil, origin)

	for i := 0; i < 200; i++ {
		leasedGet(t, h, g, "http://x/doc")
	}
	if n := c.inflightRevalidations(); n != 0 {
		t.Fatalf("%d call-state entries stranded after 200 validations", n)
	}
	waitLeaseReleased(t, g)
}
