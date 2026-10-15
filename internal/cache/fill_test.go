// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"jul/internal/config"
	"jul/internal/tracing"
)

type fillOutcomeCounter struct {
	mu     sync.Mutex
	counts map[string]int
	events []string
}

func newFillOutcomeCounter() *fillOutcomeCounter {
	return &fillOutcomeCounter{counts: make(map[string]int)}
}

func (f *fillOutcomeCounter) record(outcome string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts[outcome]++
	f.events = append(f.events, outcome)
}

func (f *fillOutcomeCounter) count(outcome fillOutcome) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts[string(outcome)]
}

func (f *fillOutcomeCounter) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

func lockCfg(timeout time.Duration) config.CacheConfig {
	return config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(1 << 20),
		DefaultTTL:    config.Duration(time.Hour),
		Lock:          true,
		LockTimeout:   config.Duration(timeout),
	}
}

func TestSetFillLockObserverNilCacheIsNoOp(t *testing.T) {
	var c *Cache
	c.SetFillLockObserver(func(string) {})
}

// TestFillLockConcurrentColdMissesCoalesceToOneOriginFetch proves that a burst
// of concurrent cold misses for one cache key triggers exactly one origin fetch,
// while all waiting followers reuse the completed entry.
func TestFillLockConcurrentColdMissesCoalesceToOneOriginFetch(t *testing.T) {
	c := newTestCache(t, lockCfg(5*time.Second))
	obs := newFillOutcomeCounter()
	c.SetFillLockObserver(obs.record)

	const total = 24
	var originCalls atomic.Int32
	leaderEntered := make(chan struct{})
	releaseLeader := make(chan struct{})

	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if originCalls.Add(1) == 1 {
			close(leaderEntered)
			<-releaseLeader
		}
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte("coalesced-payload"))
	}))

	results := make([]*httptest.ResponseRecorder, total)
	for i := range results {
		results[i] = httptest.NewRecorder()
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.ServeHTTP(results[0], httptest.NewRequest(http.MethodGet, "http://example.com/popular", nil))
	}()

	<-leaderEntered

	baseKey := key(httptest.NewRequest(http.MethodGet, "http://example.com/popular", nil))
	c.fillMu.Lock()
	activeCall := c.fills[baseKey]
	c.fillMu.Unlock()
	if activeCall == nil {
		t.Fatal("expected an active fillCall while the leader is in flight")
	}

	for i := 1; i < total; i++ {
		idx := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.ServeHTTP(results[idx], httptest.NewRequest(http.MethodGet, "http://example.com/popular", nil))
		}()
	}

	deadline := time.Now().Add(2 * time.Second)
	for activeCall.joined.Load() < int32(total-1) {
		if time.Now().After(deadline) {
			t.Fatalf("only %d/%d waiters joined before deadline", activeCall.joined.Load(), total-1)
		}
		time.Sleep(time.Millisecond)
	}

	close(releaseLeader)
	wg.Wait()

	if got := originCalls.Load(); got != 1 {
		t.Fatalf("origin calls = %d, want 1", got)
	}
	if got := c.inflightFills(); got != 0 {
		t.Fatalf("inflight fills = %d, want 0", got)
	}

	var misses, hits int
	for i, rec := range results {
		if rec.Body.String() != "coalesced-payload" {
			t.Fatalf("response %d body = %q, want %q", i, rec.Body.String(), "coalesced-payload")
		}
		switch rec.Header().Get("X-Cache") {
		case stateMiss:
			misses++
		case stateHit:
			hits++
		default:
			t.Fatalf("response %d X-Cache = %q, want MISS or HIT", i, rec.Header().Get("X-Cache"))
		}
	}
	if misses != 1 || hits != total-1 {
		t.Fatalf("misses=%d hits=%d, want 1 and %d", misses, hits, total-1)
	}
	if got := obs.count(fillOutcomeWait); got != total-1 {
		t.Fatalf("wait count = %d, want %d", got, total-1)
	}
	if got := obs.count(fillOutcomeReused); got != total-1 {
		t.Fatalf("reused count = %d, want %d", got, total-1)
	}
}

// TestFillLockUnrelatedKeysExecuteConcurrently proves per-key isolation: an
// active fill for /a never blocks /b.
func TestFillLockUnrelatedKeysExecuteConcurrently(t *testing.T) {
	c := newTestCache(t, lockCfg(5*time.Second))
	enteredA := make(chan struct{})
	enteredB := make(chan struct{})
	release := make(chan struct{})

	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a":
			close(enteredA)
			<-release
		case "/b":
			close(enteredB)
			<-release
		}
		w.Header().Set("Cache-Control", "max-age=3600")
		_, _ = w.Write([]byte(r.URL.Path))
	}))

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://example.com/a", nil))
	}()
	go func() {
		defer wg.Done()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://example.com/b", nil))
	}()

	select {
	case <-enteredA:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for /a to reach origin")
	}
	select {
	case <-enteredB:
	case <-time.After(2 * time.Second):
		t.Fatal("/b was blocked by /a's fill lock")
	}
	close(release)
	wg.Wait()
}

// TestFillLockVaryIncompatibleWaitersNeverReceiveWrongRepresentation covers
// both a cold miss with an as-yet-unknown Vary set and subsequent variant-level
// coalescing once the Vary stub is published.
func TestFillLockVaryIncompatibleWaitersNeverReceiveWrongRepresentation(t *testing.T) {
	c := newTestCache(t, lockCfg(5*time.Second))
	obs := newFillOutcomeCounter()
	c.SetFillLockObserver(obs.record)

	var gzipCalls, brCalls atomic.Int32
	gzipLeaderEntered := make(chan struct{})
	releaseGzipLeader := make(chan struct{})

	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enc := r.Header.Get("Accept-Encoding")
		switch enc {
		case "gzip":
			if gzipCalls.Add(1) == 1 {
				close(gzipLeaderEntered)
				<-releaseGzipLeader
			}
		case "br":
			brCalls.Add(1)
			time.Sleep(15 * time.Millisecond)
		}
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("Vary", "Accept-Encoding")
		w.Header().Set("Content-Encoding", enc)
		_, _ = w.Write([]byte("body-" + enc))
	}))

	var wg sync.WaitGroup
	gzipLeaderRec := httptest.NewRecorder()
	wg.Add(1)
	go func() {
		defer wg.Done()
		req := httptest.NewRequest(http.MethodGet, "http://example.com/asset", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		h.ServeHTTP(gzipLeaderRec, req)
	}()

	<-gzipLeaderEntered

	baseKey := key(httptest.NewRequest(http.MethodGet, "http://example.com/asset", nil))
	c.fillMu.Lock()
	activeCall := c.fills[baseKey]
	c.fillMu.Unlock()

	const gzipFollowers = 3
	const brFollowers = 4
	gzipRecs := make([]*httptest.ResponseRecorder, gzipFollowers)
	brRecs := make([]*httptest.ResponseRecorder, brFollowers)

	for i := 0; i < gzipFollowers; i++ {
		gzipRecs[i] = httptest.NewRecorder()
		rec := gzipRecs[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "http://example.com/asset", nil)
			req.Header.Set("Accept-Encoding", "gzip")
			h.ServeHTTP(rec, req)
		}()
	}
	for i := 0; i < brFollowers; i++ {
		brRecs[i] = httptest.NewRecorder()
		rec := brRecs[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "http://example.com/asset", nil)
			req.Header.Set("Accept-Encoding", "br")
			h.ServeHTTP(rec, req)
		}()
	}

	deadline := time.Now().Add(2 * time.Second)
	for activeCall.joined.Load() < int32(gzipFollowers+brFollowers) {
		if time.Now().After(deadline) {
			t.Fatalf("only %d/%d waiters joined", activeCall.joined.Load(), gzipFollowers+brFollowers)
		}
		time.Sleep(time.Millisecond)
	}

	close(releaseGzipLeader)
	wg.Wait()

	if gzipLeaderRec.Body.String() != "body-gzip" {
		t.Fatalf("gzip leader body = %q, want body-gzip", gzipLeaderRec.Body.String())
	}
	for i, rec := range gzipRecs {
		if rec.Body.String() != "body-gzip" || rec.Header().Get("X-Cache") != stateHit {
			t.Fatalf("gzip follower %d: state=%q body=%q, want HIT and body-gzip", i, rec.Header().Get("X-Cache"), rec.Body.String())
		}
	}
	for i, rec := range brRecs {
		if rec.Body.String() != "body-br" || rec.Header().Get("Content-Encoding") != "br" {
			t.Fatalf("br follower %d received wrong representation: encoding=%q body=%q", i, rec.Header().Get("Content-Encoding"), rec.Body.String())
		}
	}
	if got := gzipCalls.Load(); got != 1 {
		t.Fatalf("gzip origin calls = %d, want 1", got)
	}
	if got := brCalls.Load(); got != 1 {
		t.Fatalf("br origin calls = %d, want 1 (br followers should coalesce on the newly discovered variant key)", got)
	}
	if got := obs.count(fillOutcomeNotReusable); got != brFollowers {
		t.Fatalf("not_reusable count = %d, want %d", got, brFollowers)
	}
}

// TestFillLockAuthorizationAndPrivateRulesEnforced verifies that RFC 9111 §3.5
// and Jul's shared-cache authorization rules hold across leaders and waiters,
// and that raw credentials never enter lock keys or metric labels.
func TestFillLockAuthorizationAndPrivateRulesEnforced(t *testing.T) {
	t.Run("anonymous leader response without shared reuse is rejected by authenticated waiter", func(t *testing.T) {
		c := newTestCache(t, lockCfg(5*time.Second))
		obs := newFillOutcomeCounter()
		c.SetFillLockObserver(obs.record)

		leaderEntered := make(chan struct{})
		releaseLeader := make(chan struct{})
		var calls atomic.Int32

		h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				close(leaderEntered)
				<-releaseLeader
			}
			w.Header().Set("Cache-Control", "max-age=3600")
			if r.Header.Get("Authorization") == aliceCred {
				_, _ = w.Write([]byte("alice-private-data"))
				return
			}
			_, _ = w.Write([]byte("anonymous-data"))
		}))

		var wg sync.WaitGroup
		anonRec := httptest.NewRecorder()
		authRec := httptest.NewRecorder()

		wg.Add(1)
		go func() {
			defer wg.Done()
			h.ServeHTTP(anonRec, httptest.NewRequest(http.MethodGet, "http://example.com/acct", nil))
		}()
		<-leaderEntered

		baseKey := key(httptest.NewRequest(http.MethodGet, "http://example.com/acct", nil))
		c.fillMu.Lock()
		activeCall := c.fills[baseKey]
		c.fillMu.Unlock()

		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "http://example.com/acct", nil)
			req.Header.Set("Authorization", aliceCred)
			h.ServeHTTP(authRec, req)
		}()

		for activeCall.joined.Load() < 1 {
			time.Sleep(time.Millisecond)
		}
		close(releaseLeader)
		wg.Wait()

		if anonRec.Body.String() != "anonymous-data" {
			t.Fatalf("anonymous leader got %q", anonRec.Body.String())
		}
		if authRec.Body.String() != "alice-private-data" || authRec.Header().Get("X-Cache") != stateMiss {
			t.Fatalf("authenticated waiter got state=%q body=%q, want MISS and alice-private-data", authRec.Header().Get("X-Cache"), authRec.Body.String())
		}
		if obs.count(fillOutcomeNotReusable) != 1 {
			t.Fatalf("not_reusable = %d, want 1", obs.count(fillOutcomeNotReusable))
		}
	})

	t.Run("authenticated leader without public or s-maxage never leaks to waiters", func(t *testing.T) {
		c := newTestCache(t, lockCfg(5*time.Second))
		obs := newFillOutcomeCounter()
		c.SetFillLockObserver(obs.record)

		leaderEntered := make(chan struct{})
		releaseLeader := make(chan struct{})
		var calls atomic.Int32

		h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				close(leaderEntered)
				<-releaseLeader
			}
			w.Header().Set("Cache-Control", "max-age=3600")
			switch r.Header.Get("Authorization") {
			case aliceCred:
				_, _ = w.Write([]byte("alice-secret"))
			case bobCred:
				_, _ = w.Write([]byte("bob-secret"))
			default:
				_, _ = w.Write([]byte("anon-public"))
			}
		}))

		var wg sync.WaitGroup
		aliceRec := httptest.NewRecorder()
		bobRec := httptest.NewRecorder()
		anonRec := httptest.NewRecorder()

		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "http://example.com/me", nil)
			req.Header.Set("Authorization", aliceCred)
			h.ServeHTTP(aliceRec, req)
		}()
		<-leaderEntered

		baseKey := key(httptest.NewRequest(http.MethodGet, "http://example.com/me", nil))
		c.fillMu.Lock()
		activeCall := c.fills[baseKey]
		c.fillMu.Unlock()

		wg.Add(2)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "http://example.com/me", nil)
			req.Header.Set("Authorization", bobCred)
			h.ServeHTTP(bobRec, req)
		}()
		go func() {
			defer wg.Done()
			h.ServeHTTP(anonRec, httptest.NewRequest(http.MethodGet, "http://example.com/me", nil))
		}()

		for activeCall.joined.Load() < 2 {
			time.Sleep(time.Millisecond)
		}
		close(releaseLeader)
		wg.Wait()

		if aliceRec.Body.String() != "alice-secret" {
			t.Fatalf("alice got %q", aliceRec.Body.String())
		}
		if bobRec.Body.String() != "bob-secret" {
			t.Fatalf("bob got %q, want bob-secret", bobRec.Body.String())
		}
		if anonRec.Body.String() != "anon-public" {
			t.Fatalf("anonymous got %q, want anon-public", anonRec.Body.String())
		}
		if obs.count(fillOutcomeUncacheable) != 2 {
			t.Fatalf("uncacheable count = %d, want 2", obs.count(fillOutcomeUncacheable))
		}
	})

	t.Run("lock keys and observer labels never contain raw credentials", func(t *testing.T) {
		base := keyFor(http.MethodGet, "example.com", "/secret")
		req := httptest.NewRequest(http.MethodGet, "http://example.com/secret", nil)
		req.Header.Set("Authorization", "Bearer super-secret-jwt-value")
		req.Header.Set("Cookie", "session=top-secret-cookie")

		vk := variantKey(base, []string{"Authorization", "Cookie"}, req)
		fk := fillLockKey(base, vk)
		for _, secret := range []string{"super-secret-jwt-value", "top-secret-cookie", "Bearer"} {
			if strings.Contains(fk, secret) {
				t.Fatalf("fillLockKey leaked sensitive material %q: %q", secret, fk)
			}
		}
	})
}

// TestFillLockTimeoutFallsThroughUncachedWithoutOverwritingActiveFill proves
// that a waiter whose lock_timeout expires falls through to an uncached origin
// request without allocating capture state or overwriting the active leader's fill.
func TestFillLockTimeoutFallsThroughUncachedWithoutOverwritingActiveFill(t *testing.T) {
	c := newTestCache(t, lockCfg(25*time.Millisecond))
	obs := newFillOutcomeCounter()
	c.SetFillLockObserver(obs.record)

	leaderEntered := make(chan struct{})
	releaseLeader := make(chan struct{})
	var originCalls atomic.Int32

	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := originCalls.Add(1)
		w.Header().Set("Cache-Control", "public, max-age=3600")
		if n == 1 {
			close(leaderEntered)
			<-releaseLeader
			_, _ = w.Write([]byte("leader-canonical-entry"))
			return
		}
		_, _ = w.Write([]byte("timed-out-fallback-" + strconv.Itoa(int(n))))
	}))

	var wg sync.WaitGroup
	leaderRec := httptest.NewRecorder()
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.ServeHTTP(leaderRec, httptest.NewRequest(http.MethodGet, "http://example.com/slow", nil))
	}()
	<-leaderEntered

	waiterRec := httptest.NewRecorder()
	h.ServeHTTP(waiterRec, httptest.NewRequest(http.MethodGet, "http://example.com/slow", nil))

	if waiterRec.Header().Get("X-Cache") != stateMiss {
		t.Fatalf("timed-out waiter X-Cache = %q, want MISS", waiterRec.Header().Get("X-Cache"))
	}
	if waiterRec.Body.String() != "timed-out-fallback-2" {
		t.Fatalf("timed-out waiter body = %q, want timed-out-fallback-2", waiterRec.Body.String())
	}
	if obs.count(fillOutcomeTimeout) != 1 {
		t.Fatalf("timeout count = %d, want 1", obs.count(fillOutcomeTimeout))
	}

	// The timed-out fallback must NOT have published anything while the leader
	// is still in flight.
	probe := httptest.NewRequest(http.MethodGet, "http://example.com/slow", nil)
	if _, _, ok := c.lookup(key(probe), probe); ok {
		t.Fatal("timed-out waiter published a competing cache entry")
	}

	close(releaseLeader)
	wg.Wait()

	if leaderRec.Body.String() != "leader-canonical-entry" {
		t.Fatalf("leader body = %q, want leader-canonical-entry", leaderRec.Body.String())
	}

	// Subsequent requests must hit the leader's canonical entry.
	hitRec := httptest.NewRecorder()
	h.ServeHTTP(hitRec, httptest.NewRequest(http.MethodGet, "http://example.com/slow", nil))
	if hitRec.Header().Get("X-Cache") != stateHit || hitRec.Body.String() != "leader-canonical-entry" {
		t.Fatalf("post-fill request: state=%q body=%q, want HIT and leader-canonical-entry", hitRec.Header().Get("X-Cache"), hitRec.Body.String())
	}
}

// TestFillLockUncacheableAndFailedResponsesReleaseWaitersPromptly proves that
// when a fill leader's response is non-cacheable, fails, or is rejected by
// capture bounds, waiters are released immediately at the rejection point
// without waiting for lock_timeout or for the leader's slow body to finish.
func TestFillLockUncacheableAndFailedResponsesReleaseWaitersPromptly(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(w http.ResponseWriter)
	}{
		{
			name: "no-store header",
			prepare: func(w http.ResponseWriter) {
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(http.StatusOK)
			},
		},
		{
			name: "private header",
			prepare: func(w http.ResponseWriter) {
				w.Header().Set("Cache-Control", "private, max-age=3600")
				w.WriteHeader(http.StatusOK)
			},
		},
		{
			name: "Set-Cookie header",
			prepare: func(w http.ResponseWriter) {
				w.Header().Set("Cache-Control", "public, max-age=3600")
				w.Header().Set("Set-Cookie", "sid=secret")
				w.WriteHeader(http.StatusOK)
			},
		},
		{
			name: "Vary star",
			prepare: func(w http.ResponseWriter) {
				w.Header().Set("Cache-Control", "public, max-age=3600")
				w.Header().Set("Vary", "*")
				w.WriteHeader(http.StatusOK)
			},
		},
		{
			name: "origin 503 error",
			prepare: func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusServiceUnavailable)
			},
		},
		{
			name: "event stream",
			prepare: func(w http.ResponseWriter) {
				w.Header().Set("Cache-Control", "public, max-age=3600")
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
			},
		},
		{
			name: "declared oversize Content-Length",
			prepare: func(w http.ResponseWriter) {
				w.Header().Set("Cache-Control", "public, max-age=3600")
				w.Header().Set("Content-Length", strconv.Itoa(2<<20))
				w.WriteHeader(http.StatusOK)
			},
		},
		{
			name: "streaming body overflow",
			prepare: func(w http.ResponseWriter) {
				w.Header().Set("Cache-Control", "public, max-age=3600")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(make([]byte, (1<<20)+64))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestCache(t, lockCfg(5*time.Second))
			obs := newFillOutcomeCounter()
			c.SetFillLockObserver(obs.record)

			leaderEntered := make(chan struct{})
			proceedToHeader := make(chan struct{})
			releaseLeaderBody := make(chan struct{})
			var calls atomic.Int32

			h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					close(leaderEntered)
					<-proceedToHeader
					tc.prepare(w)
					// Keep the leader's body stream open until the waiter has
					// already woken up and completed its own request.
					<-releaseLeaderBody
					return
				}
				w.Header().Set("Cache-Control", "no-store")
				_, _ = w.Write([]byte("waiter-ok"))
			}))

			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://example.com/item", nil))
			}()
			<-leaderEntered

			baseKey := key(httptest.NewRequest(http.MethodGet, "http://example.com/item", nil))
			c.fillMu.Lock()
			activeCall := c.fills[baseKey]
			c.fillMu.Unlock()

			waiterDone := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example.com/item", nil))
				waiterDone <- rec
			}()

			for activeCall.joined.Load() < 1 {
				time.Sleep(time.Millisecond)
			}
			close(proceedToHeader)

			select {
			case rec := <-waiterDone:
				if rec.Body.String() != "waiter-ok" {
					t.Fatalf("waiter body = %q, want waiter-ok", rec.Body.String())
				}
			case <-time.After(time.Second):
				t.Fatal("waiter was not released promptly when leader response proved uncacheable")
			}

			close(releaseLeaderBody)
			wg.Wait()

			if obs.count(fillOutcomeUncacheable) != 1 {
				t.Fatalf("uncacheable count = %d, want 1", obs.count(fillOutcomeUncacheable))
			}
			if obs.count(fillOutcomeTimeout) != 0 {
				t.Fatalf("timeout count = %d, want 0", obs.count(fillOutcomeTimeout))
			}
		})
	}
}

type failingResponseWriter struct {
	http.ResponseWriter
	failAfter int
	written   int
}

func (f *failingResponseWriter) Write(p []byte) (int, error) {
	if f.written+len(p) > f.failAfter {
		return 0, errors.New("simulated client disconnect")
	}
	f.written += len(p)
	return f.ResponseWriter.Write(p)
}

// TestFillLockFailureCancellationAndDisconnectReleaseWaiters covers early EOF
// (incomplete Content-Length), leader cancellation, leader client disconnect,
// handler panic, and waiter cancellation.
func TestFillLockFailureCancellationAndDisconnectReleaseWaiters(t *testing.T) {
	t.Run("early EOF shorter than Content-Length is not stored and releases waiters", func(t *testing.T) {
		c := newTestCache(t, lockCfg(5*time.Second))
		obs := newFillOutcomeCounter()
		c.SetFillLockObserver(obs.record)

		var calls atomic.Int32
		leaderEntered := make(chan struct{})
		releaseLeader := make(chan struct{})

		h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				close(leaderEntered)
				<-releaseLeader
				w.Header().Set("Cache-Control", "public, max-age=3600")
				w.Header().Set("Content-Length", "100")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("short"))
				return
			}
			w.Header().Set("Cache-Control", "public, max-age=3600")
			_, _ = w.Write([]byte("complete-body"))
		}))

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://example.com/eof", nil))
		}()
		<-leaderEntered

		baseKey := key(httptest.NewRequest(http.MethodGet, "http://example.com/eof", nil))
		c.fillMu.Lock()
		activeCall := c.fills[baseKey]
		c.fillMu.Unlock()

		waiterRec := httptest.NewRecorder()
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.ServeHTTP(waiterRec, httptest.NewRequest(http.MethodGet, "http://example.com/eof", nil))
		}()
		for activeCall.joined.Load() < 1 {
			time.Sleep(time.Millisecond)
		}
		close(releaseLeader)
		wg.Wait()

		if waiterRec.Body.String() != "complete-body" {
			t.Fatalf("waiter body = %q, want complete-body", waiterRec.Body.String())
		}
		if obs.count(fillOutcomeUncacheable) != 1 {
			t.Fatalf("uncacheable = %d, want 1", obs.count(fillOutcomeUncacheable))
		}
	})

	t.Run("leader context cancellation releases waiters without storing partial body", func(t *testing.T) {
		c := newTestCache(t, lockCfg(5*time.Second))
		obs := newFillOutcomeCounter()
		c.SetFillLockObserver(obs.record)

		leaderEntered := make(chan struct{})
		var calls atomic.Int32

		h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				w.Header().Set("Cache-Control", "public, max-age=3600")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("partial"))
				close(leaderEntered)
				<-r.Context().Done()
				return
			}
			w.Header().Set("Cache-Control", "public, max-age=3600")
			_, _ = w.Write([]byte("fresh-from-waiter"))
		}))

		leaderCtx, cancelLeader := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "http://example.com/cancel-leader", nil).WithContext(leaderCtx)
			h.ServeHTTP(httptest.NewRecorder(), req)
		}()
		<-leaderEntered

		baseKey := key(httptest.NewRequest(http.MethodGet, "http://example.com/cancel-leader", nil))
		c.fillMu.Lock()
		activeCall := c.fills[baseKey]
		c.fillMu.Unlock()

		waiterRec := httptest.NewRecorder()
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.ServeHTTP(waiterRec, httptest.NewRequest(http.MethodGet, "http://example.com/cancel-leader", nil))
		}()
		for activeCall.joined.Load() < 1 {
			time.Sleep(time.Millisecond)
		}

		cancelLeader()
		wg.Wait()

		if waiterRec.Body.String() != "fresh-from-waiter" {
			t.Fatalf("waiter body = %q, want fresh-from-waiter", waiterRec.Body.String())
		}
		if obs.count(fillOutcomeUncacheable) != 1 {
			t.Fatalf("uncacheable = %d, want 1", obs.count(fillOutcomeUncacheable))
		}
		if got := c.capture.inflight.Load(); got != 0 {
			t.Fatalf("capture inflight = %d, want 0", got)
		}
	})

	t.Run("leader downstream write failure drops capture and releases waiters", func(t *testing.T) {
		c := newTestCache(t, lockCfg(5*time.Second))
		obs := newFillOutcomeCounter()
		c.SetFillLockObserver(obs.record)

		leaderEntered := make(chan struct{})
		releaseWrite := make(chan struct{})
		releaseLeader := make(chan struct{})
		var calls atomic.Int32

		h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				w.Header().Set("Cache-Control", "public, max-age=3600")
				w.WriteHeader(http.StatusOK)
				close(leaderEntered)
				<-releaseWrite
				_, _ = w.Write([]byte("0123456789"))
				<-releaseLeader
				return
			}
			w.Header().Set("Cache-Control", "public, max-age=3600")
			_, _ = w.Write([]byte("waiter-full"))
		}))

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			fw := &failingResponseWriter{ResponseWriter: httptest.NewRecorder(), failAfter: 4}
			h.ServeHTTP(fw, httptest.NewRequest(http.MethodGet, "http://example.com/disconnect", nil))
		}()
		<-leaderEntered

		baseKey := key(httptest.NewRequest(http.MethodGet, "http://example.com/disconnect", nil))
		c.fillMu.Lock()
		activeCall := c.fills[baseKey]
		c.fillMu.Unlock()

		waiterDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example.com/disconnect", nil))
			waiterDone <- rec
		}()
		for activeCall.joined.Load() < 1 {
			time.Sleep(time.Millisecond)
		}

		close(releaseWrite)
		select {
		case rec := <-waiterDone:
			if rec.Body.String() != "waiter-full" {
				t.Fatalf("waiter body = %q, want waiter-full", rec.Body.String())
			}
		case <-time.After(time.Second):
			t.Fatal("waiter not released on leader write failure")
		}
		close(releaseLeader)
		wg.Wait()
	})

	t.Run("leader panic releases waiters without stranding call state", func(t *testing.T) {
		c := newTestCache(t, lockCfg(5*time.Second))
		obs := newFillOutcomeCounter()
		c.SetFillLockObserver(obs.record)

		leaderEntered := make(chan struct{})
		releasePanic := make(chan struct{})
		var calls atomic.Int32

		h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				close(leaderEntered)
				<-releasePanic
				panic(http.ErrAbortHandler)
			}
			w.Header().Set("Cache-Control", "public, max-age=3600")
			_, _ = w.Write([]byte("recovered-waiter"))
		}))

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { _ = recover() }()
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://example.com/panic", nil))
		}()
		<-leaderEntered

		baseKey := key(httptest.NewRequest(http.MethodGet, "http://example.com/panic", nil))
		c.fillMu.Lock()
		activeCall := c.fills[baseKey]
		c.fillMu.Unlock()

		waiterRec := httptest.NewRecorder()
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.ServeHTTP(waiterRec, httptest.NewRequest(http.MethodGet, "http://example.com/panic", nil))
		}()
		for activeCall.joined.Load() < 1 {
			time.Sleep(time.Millisecond)
		}

		close(releasePanic)
		wg.Wait()

		if waiterRec.Body.String() != "recovered-waiter" {
			t.Fatalf("waiter body = %q, want recovered-waiter", waiterRec.Body.String())
		}
		if got := c.inflightFills(); got != 0 {
			t.Fatalf("inflight fills = %d, want 0", got)
		}
	})

	t.Run("waiter cancellation leaves leader and remaining waiters intact", func(t *testing.T) {
		c := newTestCache(t, lockCfg(5*time.Second))
		obs := newFillOutcomeCounter()
		c.SetFillLockObserver(obs.record)

		leaderEntered := make(chan struct{})
		releaseLeader := make(chan struct{})

		h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(leaderEntered)
			<-releaseLeader
			w.Header().Set("Cache-Control", "public, max-age=3600")
			_, _ = w.Write([]byte("leader-ok"))
		}))

		var wg sync.WaitGroup
		leaderRec := httptest.NewRecorder()
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.ServeHTTP(leaderRec, httptest.NewRequest(http.MethodGet, "http://example.com/wait-cancel", nil))
		}()
		<-leaderEntered

		baseKey := key(httptest.NewRequest(http.MethodGet, "http://example.com/wait-cancel", nil))
		c.fillMu.Lock()
		activeCall := c.fills[baseKey]
		c.fillMu.Unlock()

		waiterCtx, cancelWaiter := context.WithCancel(context.Background())
		canceledDone := make(chan struct{})
		go func() {
			defer close(canceledDone)
			req := httptest.NewRequest(http.MethodGet, "http://example.com/wait-cancel", nil).WithContext(waiterCtx)
			h.ServeHTTP(httptest.NewRecorder(), req)
		}()

		survivorRec := httptest.NewRecorder()
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.ServeHTTP(survivorRec, httptest.NewRequest(http.MethodGet, "http://example.com/wait-cancel", nil))
		}()

		for activeCall.joined.Load() < 2 {
			time.Sleep(time.Millisecond)
		}

		cancelWaiter()
		<-canceledDone

		close(releaseLeader)
		wg.Wait()

		if obs.count(fillOutcomeCanceled) != 1 {
			t.Fatalf("canceled count = %d, want 1", obs.count(fillOutcomeCanceled))
		}
		if obs.count(fillOutcomeReused) != 1 {
			t.Fatalf("reused count = %d, want 1", obs.count(fillOutcomeReused))
		}
		if survivorRec.Header().Get("X-Cache") != stateHit || survivorRec.Body.String() != "leader-ok" {
			t.Fatalf("survivor waiter: state=%q body=%q", survivorRec.Header().Get("X-Cache"), survivorRec.Body.String())
		}
	})
}

// TestFillLockPostBeginFillLookupAndReloadCoherence covers the race window
// where an entry becomes available immediately before beginFill acquires
// leadership, as well as hot reload during active fills.
func TestFillLockPostBeginFillLookupAndReloadCoherence(t *testing.T) {
	c := newTestCache(t, lockCfg(5*time.Second))
	req := httptest.NewRequest(http.MethodGet, "http://example.com/prewarmed", nil)
	base := key(req)
	now := c.clock()
	h := http.Header{"Cache-Control": {"public, max-age=3600"}}
	c.store(base, req, c.buildEntry(req, http.StatusOK, h, []byte("warm"), now))

	rec := httptest.NewRecorder()
	_, span := tracing.Active().Start(req.Context(), "cache.lookup")
	c.fillOrWait(rec, req, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("origin should not be called")
	}), base, base, parseRequestPolicy(req), c.Policy(), span, true)

	if rec.Header().Get("X-Cache") != stateHit || rec.Body.String() != "warm" {
		t.Fatalf("state=%q body=%q, want HIT and warm", rec.Header().Get("X-Cache"), rec.Body.String())
	}
	if got := c.inflightFills(); got != 0 {
		t.Fatalf("inflight fills = %d, want 0", got)
	}
}

// TestFillLockStressNoLeakOrDeadlock drives concurrent cold misses, timeouts,
// cancellations, Vary variants, and hot reloads under goleak verification.
func TestFillLockStressNoLeakOrDeadlock(t *testing.T) {
	defer goleak.VerifyNone(t)

	c := newTestCache(t, lockCfg(15*time.Millisecond))
	obs := newFillOutcomeCounter()
	c.SetFillLockObserver(obs.record)

	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("mode") {
		case "slow":
			select {
			case <-time.After(35 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
			w.Header().Set("Cache-Control", "public, max-age=60")
			_, _ = w.Write([]byte("slow-ok"))
		case "nostore":
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write([]byte("nostore"))
		case "vary":
			time.Sleep(5 * time.Millisecond)
			w.Header().Set("Cache-Control", "public, max-age=60")
			w.Header().Set("Vary", "Accept-Encoding")
			_, _ = w.Write([]byte("vary-" + r.Header.Get("Accept-Encoding")))
		default:
			time.Sleep(3 * time.Millisecond)
			w.Header().Set("Cache-Control", "public, max-age=60")
			_, _ = w.Write([]byte("fast-ok"))
		}
	}))

	var wg sync.WaitGroup
	stopReload := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stopReload:
				return
			default:
			}
			cfg := lockCfg(time.Duration(10+(i%20)) * time.Millisecond)
			cfg.Lock = i%5 != 0
			c.PrepareCacheUpdate(cfg).Commit()
			time.Sleep(2 * time.Millisecond)
		}
	}()

	modes := []string{"fast", "slow", "nostore", "vary"}
	for worker := 0; worker < 16; worker++ {
		wID := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			for iter := 0; iter < 15; iter++ {
				mode := modes[(wID+iter)%len(modes)]
				u := "http://example.com/stress?mode=" + mode + "&bucket=" + strconv.Itoa(iter%4)
				ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
				req := httptest.NewRequest(http.MethodGet, u, nil).WithContext(ctx)
				if mode == "vary" {
					if wID%2 == 0 {
						req.Header.Set("Accept-Encoding", "gzip")
					} else {
						req.Header.Set("Accept-Encoding", "br")
					}
				}
				h.ServeHTTP(httptest.NewRecorder(), req)
				cancel()
			}
		}()
	}

	// Wait for all request workers, then stop the reloader.
	doneWorkers := make(chan struct{})
	go func() {
		// Wait briefly and signal stopReload after workers finish.
		time.Sleep(250 * time.Millisecond)
		close(stopReload)
		close(doneWorkers)
	}()
	<-doneWorkers
	wg.Wait()

	if got := c.inflightFills(); got != 0 {
		t.Fatalf("stranded inflight fills = %d, want 0", got)
	}
	if got := c.capture.inflight.Load(); got != 0 {
		t.Fatalf("stranded capture bytes = %d, want 0", got)
	}
	for _, ev := range obs.all() {
		switch fillOutcome(ev) {
		case fillOutcomeWait, fillOutcomeReused, fillOutcomeNotReusable, fillOutcomeUncacheable, fillOutcomeTimeout, fillOutcomeCanceled:
		default:
			t.Fatalf("unexpected unbounded fill-lock metric outcome %q", ev)
		}
	}
}
