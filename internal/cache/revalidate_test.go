// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/background"
	"jul/internal/config"
	"jul/internal/middleware"
	"jul/internal/upstream"
)

// testLease creates a background lease group standing in for one handler
// generation, and guarantees the group is canceled and drained before the test
// ends so the package's goleak check sees no stray revalidation goroutine.
func testLease(t *testing.T, gen uint64) *background.Group {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	g := background.NewGroup(ctx, background.GroupOptions{
		Generation:   gen,
		MaxOperation: 30 * time.Second,
	})
	t.Cleanup(func() {
		g.Cancel()
		cancel()
		if !g.Wait(10 * time.Second) {
			t.Error("background lease group did not drain")
		}
	})
	return g
}

// leased returns r carrying g, exactly as the dynamic server handler installs
// the generation's lease on every real request.
func leased(r *http.Request, g *background.Group) *http.Request {
	return r.WithContext(background.WithLease(r.Context(), g))
}

// staleEntry builds a stale-but-servable entry for key seeding.
func staleEntry(body string) *Entry {
	now := time.Now()
	return &Entry{
		Status:     200,
		Header:     http.Header{"Cache-Control": {"max-age=60"}},
		Body:       []byte(body),
		CreatedAt:  now.Add(-time.Minute),
		ExpiresAt:  now.Add(-time.Second),
		StaleUntil: now.Add(time.Minute),
	}
}

// waitDrained fails the test unless the cache's revalidation call map empties.
func waitDrained(t *testing.T, c *Cache) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c.inflightRevalidations() == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("revalidation call state was not removed: %d entries stranded", c.inflightRevalidations())
}

// waitLeaseReleased fails the test unless every lease g handed out is returned.
// The lease is released by the revalidation goroutine's outermost defer, which
// runs after the call state is dropped, so it is observed with a bounded wait
// rather than assumed to be simultaneous.
func waitLeaseReleased(t *testing.T, g *background.Group) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if g.Active() == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("generation lease was not released: %d operations still held", g.Active())
}

// TestRevalidationRequiresGenerationLease proves the ownership invariant: a
// stale hit whose request carries no generation lease serves stale but starts
// no background work, because unowned work would have neither a resource holder
// nor shutdown ownership.
func TestRevalidationRequiresGenerationLease(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20)})
	var calls atomic.Int32
	var outcomes []string
	var outMu sync.Mutex
	c.SetRevalidationObserver(func(outcome string) {
		outMu.Lock()
		outcomes = append(outcomes, outcome)
		outMu.Unlock()
	})
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Cache-Control", "max-age=60")
		_, _ = w.Write([]byte("fresh"))
	}))

	r := httptest.NewRequest(http.MethodGet, "http://x/nolease", nil)
	c.set(key(r), staleEntry("stale"))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if got := rec.Header().Get("X-Cache"); got != "STALE" {
		t.Fatalf("X-Cache = %q, want STALE", got)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("upstream called %d times, want 0 without a generation lease", got)
	}
	if c.inflightRevalidations() != 0 {
		t.Fatal("call state registered without a lease")
	}
	outMu.Lock()
	defer outMu.Unlock()
	if len(outcomes) != 1 || outcomes[0] != string(outcomeNoLease) {
		t.Fatalf("outcomes = %v, want [%s]", outcomes, outcomeNoLease)
	}
}

// TestRevalidationSurvivesClientDisconnect proves the refresh is rooted in the
// process lifetime, not the client connection: canceling the originating
// request's context must not abort the refresh it started.
func TestRevalidationSurvivesClientDisconnect(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20)})
	g := testLease(t, 1)

	observed := make(chan error, 1)
	proceed := make(chan struct{})
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-proceed
		observed <- r.Context().Err()
		w.Header().Set("Cache-Control", "max-age=60")
		_, _ = w.Write([]byte("fresh"))
	}))

	clientCtx, disconnect := context.WithCancel(context.Background())
	r := leased(httptest.NewRequest(http.MethodGet, "http://x/disconnect", nil).WithContext(clientCtx), g)
	c.set(key(r), staleEntry("stale"))

	h.ServeHTTP(httptest.NewRecorder(), r)

	// The client goes away after the response is served; the refresh must not
	// notice.
	disconnect()
	close(proceed)

	select {
	case err := <-observed:
		if err != nil {
			t.Fatalf("revalidation context error = %v, want nil after client disconnect", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("revalidation did not run after client disconnect")
	}
	waitDrained(t, c)
}

// TestRevalidationCanceledByLeaseCancel proves the complementary bound: process
// shutdown or forced generation retirement cancels the refresh, and a canceled
// refresh must NOT be mistaken for an origin outage, so it never extends the
// stale-if-error window.
func TestRevalidationCanceledByLeaseCancel(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{
		MemoryMaxSize: config.Size(1 << 20),
		StaleIfError:  config.Duration(time.Hour),
	})
	g := testLease(t, 1)

	entered := make(chan struct{})
	saw := make(chan error, 1)
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done() // shutdown reaches the in-flight refresh
		saw <- r.Context().Err()
		w.WriteHeader(http.StatusServiceUnavailable)
	}))

	r := leased(httptest.NewRequest(http.MethodGet, "http://x/shutdown", nil), g)
	seeded := staleEntry("stale")
	c.set(key(r), seeded)

	h.ServeHTTP(httptest.NewRecorder(), r)
	<-entered

	g.Cancel() // process shutdown / forced retirement

	select {
	case err := <-saw:
		if err == nil {
			t.Fatal("revalidation context was not canceled by lease cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("revalidation was not canceled")
	}

	waitDrained(t, c)
	e, ok := c.get(key(r))
	if !ok {
		t.Fatal("entry missing after canceled revalidation")
	}
	if !e.StaleUntil.Equal(seeded.StaleUntil) {
		t.Fatalf("StaleUntil = %v, want unchanged %v: cancellation must not extend the stale window",
			e.StaleUntil, seeded.StaleUntil)
	}
}

// TestRevalidationRefusedByRetiringGeneration proves that a generation which has
// begun retiring refuses new background work rather than handing it to another
// generation or starting work on resources that are about to close.
func TestRevalidationRefusedByRetiringGeneration(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20)})
	g := testLease(t, 7)
	g.Cancel() // the generation is retiring before the stale hit arrives

	var calls atomic.Int32
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Cache-Control", "max-age=60")
		_, _ = w.Write([]byte("fresh"))
	}))

	r := leased(httptest.NewRequest(http.MethodGet, "http://x/retiring", nil), g)
	c.set(key(r), staleEntry("stale"))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if got := rec.Body.String(); got != "stale" {
		t.Fatalf("body = %q, want stale", got)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("upstream called %d times, want 0 on a retiring generation", got)
	}
	if c.inflightRevalidations() != 0 {
		t.Fatal("call state registered against a retiring generation")
	}
}

// TestRevalidationIsolatedPerGeneration proves the deduplication key includes
// generation identity: a refresh still running on the old generation must not
// suppress the new generation's refresh of the same key. Without generation in
// the key, the second request would be silently deduplicated and the new
// handler tree would never be exercised.
func TestRevalidationIsolatedPerGeneration(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20)})
	oldGen := testLease(t, 1)
	newGen := testLease(t, 2)

	var calls atomic.Int32
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		entered <- struct{}{}
		<-release
		w.Header().Set("Cache-Control", "max-age=60")
		_, _ = w.Write([]byte("fresh"))
	}))

	seedKey := key(httptest.NewRequest(http.MethodGet, "http://x/gen", nil))
	c.set(seedKey, staleEntry("stale"))

	h.ServeHTTP(httptest.NewRecorder(), leased(httptest.NewRequest(http.MethodGet, "http://x/gen", nil), oldGen))
	<-entered

	h.ServeHTTP(httptest.NewRecorder(), leased(httptest.NewRequest(http.MethodGet, "http://x/gen", nil), newGen))
	<-entered

	if got := calls.Load(); got != 2 {
		t.Fatalf("origin calls = %d, want 2 (one per generation)", got)
	}
	close(release)
	waitDrained(t, c)
}

// TestRevalidationWaitersReleasedOnEveryOutcome proves that a waiter on the
// revalidation call state unblocks for success, origin error, cancellation and
// panic alike, and that the call entry is always removed afterwards.
func TestRevalidationWaitersReleasedOnEveryOutcome(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		want    revalidateOutcome
		sif     time.Duration
		cancel  bool
	}{
		{
			name: "success",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Cache-Control", "max-age=60")
				_, _ = w.Write([]byte("fresh"))
			},
			want: outcomeStored,
		},
		{
			name: "origin error",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadGateway)
			},
			want: outcomeOriginError,
			sif:  time.Hour,
		},
		{
			name: "panic",
			handler: func(http.ResponseWriter, *http.Request) {
				panic("revalidation blew up")
			},
			want: outcomePanic,
		},
		{
			name: "cancellation",
			handler: func(_ http.ResponseWriter, r *http.Request) {
				<-r.Context().Done()
			},
			want:   outcomeCanceled,
			cancel: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestCache(t, config.CacheConfig{
				MemoryMaxSize: config.Size(1 << 20),
				StaleIfError:  config.Duration(tc.sif),
			})
			g := testLease(t, 1)

			entered := make(chan struct{})
			proceed := make(chan struct{})
			var once sync.Once
			h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				once.Do(func() { close(entered) })
				// Hold the refresh open so the test can observe the call state
				// while it is genuinely in flight.
				<-proceed
				tc.handler(w, r)
			}))

			r := leased(httptest.NewRequest(http.MethodGet, "http://x/outcome", nil), g)
			c.set(key(r), staleEntry("stale"))
			h.ServeHTTP(httptest.NewRecorder(), r)

			<-entered
			call, ok := c.lookupRevalidation(revalidateKey{key: key(r), gen: 1})
			if !ok {
				t.Fatal("no revalidation call registered for the in-flight refresh")
			}
			close(proceed)
			if tc.cancel {
				g.Cancel()
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, outcome, _ := call.wait(ctx)
			if outcome != tc.want {
				t.Fatalf("outcome = %q, want %q", outcome, tc.want)
			}
			waitDrained(t, c)
		})
	}
}

// TestRevalidationPanicLeavesEntryServable proves a panicking downstream handler
// releases the lease and the call state without corrupting or removing the
// published entry, so the cache degrades to ordinary stale service.
func TestRevalidationPanicLeavesEntryServable(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20)})
	g := testLease(t, 1)

	panicked := make(chan struct{})
	var once sync.Once
	h := c.Handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		once.Do(func() { close(panicked) })
		panic("boom")
	}))

	r := leased(httptest.NewRequest(http.MethodGet, "http://x/panic", nil), g)
	seeded := staleEntry("stale")
	c.set(key(r), seeded)
	h.ServeHTTP(httptest.NewRecorder(), r)

	<-panicked
	waitDrained(t, c)

	e, ok := c.get(key(r))
	if !ok {
		t.Fatal("entry removed by a panicking revalidation")
	}
	if string(e.Body) != "stale" || !e.StaleUntil.Equal(seeded.StaleUntil) {
		t.Fatalf("entry mutated by a panicking revalidation: %+v", e)
	}
	waitLeaseReleased(t, g)
}

// TestStaleIfErrorReplacesRatherThanMutates proves the immutability contract at
// the update boundary: the stale-if-error extension publishes a NEW entry and
// leaves the pointer a concurrent reader already holds untouched.
func TestStaleIfErrorReplacesRatherThanMutates(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{
		MemoryMaxSize: config.Size(1 << 20),
		StaleIfError:  config.Duration(time.Hour),
	})
	g := testLease(t, 1)

	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))

	r := leased(httptest.NewRequest(http.MethodGet, "http://x/sif", nil), g)
	seeded := staleEntry("stale")
	seededStaleUntil := seeded.StaleUntil
	c.set(key(r), seeded)

	// A reader that grabbed the published pointer before the refresh ran.
	held, _ := c.get(key(r))

	h.ServeHTTP(httptest.NewRecorder(), r)
	waitDrained(t, c)

	if !held.StaleUntil.Equal(seededStaleUntil) {
		t.Fatalf("published entry was mutated in place: StaleUntil %v -> %v", seededStaleUntil, held.StaleUntil)
	}
	updated, ok := c.get(key(r))
	if !ok {
		t.Fatal("entry missing after stale-if-error extension")
	}
	if updated == held {
		t.Fatal("stale-if-error reused the published pointer instead of replacing it")
	}
	if !updated.StaleUntil.After(seededStaleUntil) {
		t.Fatalf("stale-if-error did not extend the window: %v", updated.StaleUntil)
	}
}

// TestNotModifiedReplacesRatherThanMutates proves the same contract for the 304
// path, which refreshes timing metadata.
func TestNotModifiedReplacesRatherThanMutates(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20)})
	g := testLease(t, 1)

	var gotINM string
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotINM = r.Header.Get("If-None-Match")
		w.WriteHeader(http.StatusNotModified)
	}))

	r := leased(httptest.NewRequest(http.MethodGet, "http://x/304", nil), g)
	seeded := staleEntry("stale")
	seeded.ETag = `"v1"`
	seededExpires := seeded.ExpiresAt
	c.set(key(r), seeded)
	held, _ := c.get(key(r))

	h.ServeHTTP(httptest.NewRecorder(), r)
	waitDrained(t, c)

	if gotINM != `"v1"` {
		t.Fatalf("If-None-Match = %q, want the stored ETag", gotINM)
	}
	if !held.ExpiresAt.Equal(seededExpires) {
		t.Fatal("304 revalidation mutated the published entry in place")
	}
	updated, _ := c.get(key(r))
	if updated == held {
		t.Fatal("304 revalidation reused the published pointer")
	}
	if !updated.ExpiresAt.After(seededExpires) {
		t.Fatalf("304 revalidation did not extend freshness: %v", updated.ExpiresAt)
	}
	if string(updated.Body) != "stale" {
		t.Fatalf("304 revalidation lost the stored body: %q", updated.Body)
	}
}

// TestEntryCloneIsDeepEnough proves Clone breaks every alias a mutation could
// travel through: header value slices, body bytes, Vary and VaryValues.
func TestEntryCloneIsDeepEnough(t *testing.T) {
	orig := &Entry{
		Status:     200,
		Header:     http.Header{"X-A": {"1", "2"}},
		Body:       []byte("body"),
		Vary:       []string{"Accept"},
		VaryValues: map[string]string{"Accept": "application/json"},
	}
	clone := orig.Clone()

	clone.Header.Set("X-A", "mutated")
	clone.Header.Add("X-B", "new")
	clone.Body[0] = 'B'
	clone.Vary[0] = "Accept-Encoding"
	clone.VaryValues["Accept"] = "text/plain"

	if got := orig.Header["X-A"]; len(got) != 2 || got[0] != "1" || got[1] != "2" {
		t.Errorf("original header values aliased: %v", got)
	}
	if _, present := orig.Header["X-B"]; present {
		t.Error("original header map aliased")
	}
	if string(orig.Body) != "body" {
		t.Errorf("original body aliased: %q", orig.Body)
	}
	if orig.Vary[0] != "Accept" {
		t.Errorf("original Vary aliased: %v", orig.Vary)
	}
	if orig.VaryValues["Accept"] != "application/json" {
		t.Errorf("original VaryValues aliased: %v", orig.VaryValues)
	}
	if Clone := (*Entry)(nil).Clone(); Clone != nil {
		t.Error("Clone of nil entry must be nil")
	}
}

// TestPublishedEntryDoesNotAliasHandlerState proves the publication boundary:
// a stored entry must not share the handler's response header map or the
// streamed body buffer, both of which the server reuses.
func TestPublishedEntryDoesNotAliasHandlerState(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20)})
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "max-age=60")
		w.Header().Set("Vary", "Accept")
		w.Header().Set("X-Origin", "one")
		_, _ = w.Write([]byte("payload"))
	}))

	req := httptest.NewRequest(http.MethodGet, "http://x/alias", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	stored, _, ok := c.lookup(key(req), req)
	if !ok {
		t.Fatal("response was not stored")
	}

	// Mutating what the handler wrote must not reach the stored entry.
	rec.Header().Set("X-Origin", "two")
	rec.Body.Reset()
	// Mutating the request's varied header must not reach the stored variant.
	req.Header.Set("Accept", "text/plain")

	if got := stored.Header.Get("X-Origin"); got != "one" {
		t.Errorf("stored header aliased the handler's map: X-Origin = %q", got)
	}
	if string(stored.Body) != "payload" {
		t.Errorf("stored body aliased the response buffer: %q", stored.Body)
	}
	if got := stored.VaryValues["Accept"]; got != "application/json" {
		t.Errorf("stored VaryValues aliased the request header: %q", got)
	}
}

// TestServeDoesNotMutatePublishedEntry proves the read path is non-mutating:
// serving an entry many times, including conditionally, leaves every field
// byte-identical.
func TestServeDoesNotMutatePublishedEntry(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20)})
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "max-age=60")
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte("payload"))
	}))

	req := httptest.NewRequest(http.MethodGet, "http://x/immutable", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	stored, _ := c.get(key(req))
	before := stored.Clone()

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := httptest.NewRequest(http.MethodGet, "http://x/immutable", nil)
			if i%2 == 0 {
				r.Header.Set("If-None-Match", `"v1"`)
			}
			h.ServeHTTP(httptest.NewRecorder(), r)
		}(i)
	}
	wg.Wait()

	after, _ := c.get(key(req))
	if after != stored {
		t.Fatal("serving replaced the published entry")
	}
	if string(after.Body) != string(before.Body) || after.Header.Get("ETag") != before.Header.Get("ETag") {
		t.Fatalf("serving mutated the published entry: %+v vs %+v", after, before)
	}
	if !after.ExpiresAt.Equal(before.ExpiresAt) || !after.StaleUntil.Equal(before.StaleUntil) {
		t.Fatal("serving mutated the published entry's freshness fields")
	}
}

// TestConcurrentReadsDuringStaleIfErrorReplacement drives concurrent stale
// reads while a failing revalidation replaces the entry, which is the exact
// interleaving that used to race on the shared *Entry. It is meaningful under
// -race.
func TestConcurrentReadsDuringStaleIfErrorReplacement(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{
		MemoryMaxSize: config.Size(1 << 20),
		StaleIfError:  config.Duration(time.Hour),
	})
	g := testLease(t, 1)

	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))

	url := "http://x/race"
	c.set(key(httptest.NewRequest(http.MethodGet, url, nil)), staleEntry("stale"))

	stop := make(chan struct{})
	var readers sync.WaitGroup
	for i := 0; i < 8; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				e, ok := c.get(key(httptest.NewRequest(http.MethodGet, url, nil)))
				if ok {
					_ = e.Fresh(time.Now())
					_ = e.ServableStale(time.Now())
					_ = len(e.Body)
					_ = e.Header.Get("Cache-Control")
				}
			}
		}()
	}

	for i := 0; i < 20; i++ {
		h.ServeHTTP(httptest.NewRecorder(), leased(httptest.NewRequest(http.MethodGet, url, nil), g))
		waitDrained(t, c)
	}
	close(stop)
	readers.Wait()
}

// TestRevalidationPropagatesAllowListedContextValues proves the context-value
// inventory: the generation-scoped upstream snapshot, the mutual-TLS client
// identity and the request/trace ids survive onto the background context, while
// client cancellation does not and authentication claims are deliberately
// dropped.
func TestRevalidationPropagatesAllowListedContextValues(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20)})
	g := testLease(t, 1)

	type seen struct {
		snapshots  int
		identity   *middleware.ClientIdentity
		requestID  string
		traceID    string
		claims     map[string]any
		ctxErr     error
		hasDeadlin bool
	}
	got := make(chan seen, 1)
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hasDeadline := r.Context().Deadline()
		got <- seen{
			snapshots:  len(upstream.SnapshotsFrom(r.Context())),
			identity:   middleware.ClientIdentityFrom(r.Context()),
			requestID:  middleware.RequestIDFrom(r.Context()),
			traceID:    middleware.TraceIDFrom(r.Context()),
			claims:     middleware.ClaimsFrom(r.Context()),
			ctxErr:     r.Context().Err(),
			hasDeadlin: hasDeadline,
		}
		w.Header().Set("Cache-Control", "max-age=60")
		_, _ = w.Write([]byte("fresh"))
	}))

	clientCtx, disconnect := context.WithCancel(context.Background())
	ctx := upstream.WithSnapshot(clientCtx, upstream.SnapshotMap{
		{Name: "api", Scheme: "http"}: nil,
	})
	ctx = middleware.WithClientIdentity(ctx, &middleware.ClientIdentity{Verified: true, CN: "client.example"})
	ctx = middleware.WithRequestID(ctx, "req-1")
	ctx = middleware.WithTraceID(ctx, "trace-1")
	ctx = middleware.WithClaims(ctx, map[string]any{"sub": "alice"})

	r := leased(httptest.NewRequest(http.MethodGet, "http://x/ctx", nil).WithContext(ctx), g)
	c.set(key(r), staleEntry("stale"))
	h.ServeHTTP(httptest.NewRecorder(), r)
	disconnect()

	select {
	case s := <-got:
		if s.snapshots != 1 {
			t.Errorf("upstream snapshots = %d, want 1 (generation backend view must survive)", s.snapshots)
		}
		if s.identity == nil || s.identity.CN != "client.example" {
			t.Errorf("client identity = %+v, want the request's verified identity", s.identity)
		}
		if s.requestID != "req-1" {
			t.Errorf("request id = %q, want req-1", s.requestID)
		}
		if s.traceID != "trace-1" {
			t.Errorf("trace id = %q, want trace-1", s.traceID)
		}
		if s.claims != nil {
			t.Errorf("claims = %v, want nil (deliberately not propagated)", s.claims)
		}
		if s.ctxErr != nil {
			t.Errorf("context error = %v, want nil (client cancellation must not propagate)", s.ctxErr)
		}
		if !s.hasDeadlin {
			t.Error("background context has no deadline; the operation must be bounded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("revalidation did not run")
	}
	waitDrained(t, c)
}

// TestRevalidationStoresThroughDiskTier proves the replacement path stays
// coherent across both tiers: the refreshed entry is readable after the memory
// tier is purged, meaning the disk tier saw a complete, immutable entry.
func TestRevalidationStoresThroughDiskTier(t *testing.T) {
	dir := t.TempDir()
	c := newTestCache(t, config.CacheConfig{
		MemoryMaxSize: config.Size(1 << 20),
		DiskPath:      dir,
		DiskMaxSize:   config.Size(1 << 20),
	})
	g := testLease(t, 1)

	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "max-age=60")
		_, _ = w.Write([]byte("refreshed"))
	}))

	r := leased(httptest.NewRequest(http.MethodGet, "http://x/disk", nil), g)
	c.set(key(r), staleEntry("stale"))
	h.ServeHTTP(httptest.NewRecorder(), r)
	waitDrained(t, c)

	e, ok := c.get(key(r))
	if !ok || string(e.Body) != "refreshed" {
		t.Fatalf("memory tier entry after refresh = %+v", e)
	}
	// Force the disk tier to answer the next lookup.
	c.mem.set(key(r), e)
	c.disk.set(key(r), e)
	c.mem.purge()

	fromDisk, ok := c.get(key(r))
	if !ok {
		t.Fatal("refreshed entry did not survive to the disk tier")
	}
	if string(fromDisk.Body) != "refreshed" || fromDisk.Header.Get("Cache-Control") != "max-age=60" {
		t.Fatalf("disk-tier entry is incoherent: %+v", fromDisk)
	}
	if fromDisk == e {
		t.Fatal("disk tier returned the memory-tier pointer; a decoded entry is expected")
	}
}

// TestRepeatedRevalidationLeavesNoCallState hammers the stale path across many
// sequential generations and asserts nothing is stranded — no call-map entry,
// no held lease, no goroutine (the package's goleak check covers the last).
func TestRepeatedRevalidationLeavesNoCallState(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20)})
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "max-age=60")
		_, _ = w.Write([]byte("fresh"))
	}))

	for gen := uint64(1); gen <= 25; gen++ {
		g := testLease(t, gen)
		url := "http://x/churn" + strings.Repeat("a", int(gen%3))
		r := leased(httptest.NewRequest(http.MethodGet, url, nil), g)
		c.set(key(r), staleEntry("stale"))
		h.ServeHTTP(httptest.NewRecorder(), r)
		waitDrained(t, c)
		waitLeaseReleased(t, g)
	}
	if n := c.inflightRevalidations(); n != 0 {
		t.Fatalf("call state stranded after churn: %d", n)
	}
}
