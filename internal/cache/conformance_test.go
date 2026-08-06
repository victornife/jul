// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

// conformanceCache builds a cache with a deterministic clock the test drives.
func conformanceCache(t *testing.T, cfg config.CacheConfig) (*Cache, *fakeClock) {
	t.Helper()
	c := newTestCache(t, cfg)
	clk := &fakeClock{at: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)}
	c.now = clk.now
	return c, clk
}

// fakeClock is the deterministic time seam. Tests advance it explicitly instead
// of sleeping, so freshness and stale-window assertions never race the CPU.
type fakeClock struct {
	mu sync.Mutex
	at time.Time
}

func (f *fakeClock) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.at
}

func (f *fakeClock) advance(d time.Duration) {
	f.mu.Lock()
	f.at = f.at.Add(d)
	f.mu.Unlock()
}

// originStub is a scriptable origin that counts how many times it was called and
// records the conditional headers it received.
type originStub struct {
	mu       sync.Mutex
	calls    int32
	lastINM  string
	lastIMS  string
	handler  func(w http.ResponseWriter, r *http.Request)
	requests []*http.Request
}

func (o *originStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt32(&o.calls, 1)
	o.mu.Lock()
	o.lastINM = r.Header.Get("If-None-Match")
	o.lastIMS = r.Header.Get("If-Modified-Since")
	o.requests = append(o.requests, r)
	o.mu.Unlock()
	o.handler(w, r)
}

func (o *originStub) count() int { return int(atomic.LoadInt32(&o.calls)) }

func (o *originStub) conditional() (inm, ims string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.lastINM, o.lastIMS
}

func get(t *testing.T, h http.Handler, target string, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, target, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Add(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func wantResult(t *testing.T, rec *httptest.ResponseRecorder, state, body string) {
	t.Helper()
	if got := rec.Header().Get("X-Cache"); got != state {
		t.Errorf("X-Cache = %q, want %q", got, state)
	}
	if body != "" && rec.Body.String() != body {
		t.Errorf("body = %q, want %q", rec.Body.String(), body)
	}
}

// ---------------------------------------------------------------------------
// Request directives
// ---------------------------------------------------------------------------

// TestRequestNoStoreBypassesLookupAndStorage proves request no-store neither
// reads nor writes the cache — and, critically, does not purge what is there.
func TestRequestNoStoreBypassesLookupAndStorage(t *testing.T) {
	c, _ := conformanceCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(time.Minute)})
	origin := &originStub{handler: func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "max-age=60")
		_, _ = w.Write([]byte("origin"))
	}}
	h := c.Handler(origin)

	wantResult(t, get(t, h, "http://x/a"), stateMiss, "origin")
	wantResult(t, get(t, h, "http://x/a"), stateHit, "origin")

	rec := get(t, h, "http://x/a", "Cache-Control", "no-store")
	wantResult(t, rec, stateBypass, "origin")
	if origin.count() != 2 {
		t.Errorf("origin calls = %d, want 2 (the bypass must reach the origin)", origin.count())
	}

	// The bypass stored nothing new and purged nothing: the entry the earlier
	// request published is still there and still served.
	wantResult(t, get(t, h, "http://x/a"), stateHit, "origin")
	if origin.count() != 2 {
		t.Errorf("origin calls = %d, want 2 (no-store must not evict an existing entry)", origin.count())
	}
}

// TestRequestNoStoreResponseIsNotStored proves the bypass path never publishes.
func TestRequestNoStoreResponseIsNotStored(t *testing.T) {
	c, _ := conformanceCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(time.Minute)})
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "max-age=60")
		_, _ = w.Write([]byte("origin"))
	}))

	get(t, h, "http://x/a", "Cache-Control", "no-store")
	r := httptest.NewRequest(http.MethodGet, "http://x/a", nil)
	if _, ok := c.get(key(r)); ok {
		t.Fatal("a no-store request must not publish an entry")
	}
}

// TestRequestNoCacheValidatesEvenAFreshEntry is D06's request half: a fresh
// entry is not enough, the origin must confirm it first.
func TestRequestNoCacheValidatesEvenAFreshEntry(t *testing.T) {
	for _, directive := range []string{"no-cache", "max-age=0"} {
		t.Run(directive, func(t *testing.T) {
			c, _ := conformanceCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(time.Hour)})
			origin := &originStub{handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("ETag", `"v1"`)
				w.Header().Set("Cache-Control", "max-age=3600")
				if r.Header.Get("If-None-Match") == `"v1"` {
					w.WriteHeader(http.StatusNotModified)
					return
				}
				_, _ = w.Write([]byte("body"))
			}}
			h := c.Handler(origin)

			wantResult(t, get(t, h, "http://x/a"), stateMiss, "body")
			wantResult(t, get(t, h, "http://x/a"), stateHit, "body")
			if origin.count() != 1 {
				t.Fatalf("origin calls = %d, want 1 before the validating request", origin.count())
			}

			rec := get(t, h, "http://x/a", "Cache-Control", directive)
			wantResult(t, rec, stateRevalidated, "body")
			if origin.count() != 2 {
				t.Errorf("origin calls = %d, want 2 — the entry was fresh but had to be validated", origin.count())
			}
			if inm, _ := origin.conditional(); inm != `"v1"` {
				t.Errorf("If-None-Match = %q, want the stored ETag", inm)
			}
		})
	}
}

// TestPragmaNoCacheValidates covers the HTTP/1.0 spelling.
func TestPragmaNoCacheValidates(t *testing.T) {
	c, _ := conformanceCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(time.Hour)})
	origin := &originStub{handler: func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Cache-Control", "max-age=3600")
		if r.Header.Get("If-None-Match") != "" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write([]byte("body"))
	}}
	h := c.Handler(origin)

	get(t, h, "http://x/a")
	wantResult(t, get(t, h, "http://x/a", "Pragma", "no-cache"), stateRevalidated, "body")
	if origin.count() != 2 {
		t.Errorf("origin calls = %d, want 2", origin.count())
	}
}

// ---------------------------------------------------------------------------
// Response directives
// ---------------------------------------------------------------------------

func TestResponseDirectiveStorage(t *testing.T) {
	cases := []struct {
		name   string
		header map[string]string
		stored bool
	}{
		{"plain response is stored", map[string]string{"Cache-Control": "max-age=60"}, true},
		{"no-store is never stored", map[string]string{"Cache-Control": "no-store"}, false},
		{"private is never stored in a shared cache", map[string]string{"Cache-Control": "private, max-age=60"}, false},
		{"public is stored", map[string]string{"Cache-Control": "public, max-age=60"}, true},
		{"s-maxage is stored", map[string]string{"Cache-Control": "s-maxage=60"}, true},
		{"no-cache IS stored", map[string]string{"Cache-Control": "no-cache"}, true},
		{"field-qualified no-cache IS stored", map[string]string{"Cache-Control": `no-cache="X-Token"`}, true},
		{"must-revalidate is stored", map[string]string{"Cache-Control": "must-revalidate, max-age=60"}, true},
		{"proxy-revalidate is stored", map[string]string{"Cache-Control": "proxy-revalidate, max-age=60"}, true},
		{"Set-Cookie is never stored", map[string]string{"Cache-Control": "max-age=60", "Set-Cookie": "sid=1"}, false},
		{"Vary: * is never stored", map[string]string{"Cache-Control": "max-age=60", "Vary": "*"}, false},
		{"max-age=0 is not stored", map[string]string{"Cache-Control": "max-age=0"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := conformanceCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(time.Minute)})
			h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				_, _ = w.Write([]byte("body"))
			}))
			get(t, h, "http://x/a")

			r := httptest.NewRequest(http.MethodGet, "http://x/a", nil)
			_, _, ok := c.lookup(key(r), r)
			if ok != tc.stored {
				t.Errorf("stored = %v, want %v", ok, tc.stored)
			}
		})
	}
}

// TestResponseNoCacheRequiresValidationBeforeEveryReuse is D06's response half:
// no-cache is stored — that is the whole point, so a 304 can save the body — but
// no reuse skips the origin.
func TestResponseNoCacheRequiresValidationBeforeEveryReuse(t *testing.T) {
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

	wantResult(t, get(t, h, "http://x/a"), stateMiss, "body")

	r := httptest.NewRequest(http.MethodGet, "http://x/a", nil)
	e, _, ok := c.lookup(key(r), r)
	if !ok {
		t.Fatal("a no-cache response must still be stored")
	}
	if !e.RequiresValidation {
		t.Fatal("stored no-cache entry must be marked RequiresValidation")
	}

	for i := 0; i < 3; i++ {
		wantResult(t, get(t, h, "http://x/a"), stateRevalidated, "body")
	}
	if origin.count() != 4 {
		t.Errorf("origin calls = %d, want 4 — every reuse of a no-cache entry validates", origin.count())
	}
}

// TestMustRevalidateForbidsStaleReuse proves the origin's prohibition outranks
// Jul's global stale_while_revalidate.
func TestMustRevalidateForbidsStaleReuse(t *testing.T) {
	for _, directive := range []string{"must-revalidate", "proxy-revalidate"} {
		t.Run(directive, func(t *testing.T) {
			c, clk := conformanceCache(t, config.CacheConfig{
				MemoryMaxSize:        config.Size(1 << 20),
				DefaultTTL:           config.Duration(time.Minute),
				StaleWhileRevalidate: config.Duration(time.Hour),
			})
			origin := &originStub{handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("ETag", `"v1"`)
				w.Header().Set("Cache-Control", directive+", max-age=60")
				if r.Header.Get("If-None-Match") == `"v1"` {
					w.WriteHeader(http.StatusNotModified)
					return
				}
				_, _ = w.Write([]byte("body"))
			}}
			h := c.Handler(origin)

			wantResult(t, get(t, h, "http://x/a"), stateMiss, "body")
			clk.advance(90 * time.Second) // past max-age, well inside the global stale window

			rec := get(t, h, "http://x/a")
			if got := rec.Header().Get("X-Cache"); got == stateStale {
				t.Fatalf("%s must never be served stale, got X-Cache=%q", directive, got)
			}
			wantResult(t, rec, stateRevalidated, "body")
		})
	}
}

// TestStaleIfErrorRespectsMustRevalidate proves the origin's revalidation
// prohibition survives an upstream outage, and that an ordinary entry does get
// the configured grace.
func TestStaleIfErrorRespectsMustRevalidate(t *testing.T) {
	run := func(t *testing.T, cc string) string {
		t.Helper()
		c, clk := conformanceCache(t, config.CacheConfig{
			MemoryMaxSize: config.Size(1 << 20),
			DefaultTTL:    config.Duration(time.Minute),
			StaleIfError:  config.Duration(time.Hour),
		})
		down := false
		h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if down {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte("down"))
				return
			}
			w.Header().Set("ETag", `"v1"`)
			w.Header().Set("Cache-Control", cc)
			_, _ = w.Write([]byte("body"))
		}))
		get(t, h, "http://x/a")
		clk.advance(90 * time.Second)
		down = true
		return get(t, h, "http://x/a").Header().Get("X-Cache")
	}

	if got := run(t, "max-age=60"); got != stateStale {
		t.Errorf("ordinary entry: X-Cache = %q, want STALE from stale_if_error", got)
	}
	if got := run(t, "must-revalidate, max-age=60"); got == stateStale {
		t.Error("must-revalidate must not be served stale on an origin error, even with stale_if_error configured")
	}
	if got := run(t, "proxy-revalidate, max-age=60"); got == stateStale {
		t.Error("proxy-revalidate must not be served stale on an origin error")
	}
}

// TestExplicitStaleIfErrorReplacesTheGlobalSetting proves the origin can shorten
// or disable Jul's configured grace, not only lengthen it.
func TestExplicitStaleIfErrorReplacesTheGlobalSetting(t *testing.T) {
	c, clk := conformanceCache(t, config.CacheConfig{
		MemoryMaxSize: config.Size(1 << 20),
		DefaultTTL:    config.Duration(time.Minute),
		StaleIfError:  config.Duration(time.Hour),
	})
	down := false
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if down {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Cache-Control", "max-age=60, stale-if-error=0")
		_, _ = w.Write([]byte("body"))
	}))

	get(t, h, "http://x/a")
	clk.advance(90 * time.Second)
	down = true
	if got := get(t, h, "http://x/a").Header().Get("X-Cache"); got == stateStale {
		t.Errorf("an explicit stale-if-error=0 must disable the global grace, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Synchronous validation
// ---------------------------------------------------------------------------

// TestValidatorPrecedence proves the strong validator is preferred and the weak
// one is the fallback, never both at once.
func TestValidatorPrecedence(t *testing.T) {
	lastMod := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC).Format(http.TimeFormat)
	cases := []struct {
		name     string
		etag     string
		lastMod  string
		wantINM  string
		wantIMS  string
		validate bool
	}{
		{name: "ETag only", etag: `"v1"`, wantINM: `"v1"`, validate: true},
		{name: "Last-Modified only", lastMod: lastMod, wantIMS: lastMod, validate: true},
		{name: "ETag wins over Last-Modified", etag: `"v1"`, lastMod: lastMod, wantINM: `"v1"`, validate: true},
		{name: "no validator means a full fetch"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := conformanceCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(time.Hour)})
			origin := &originStub{handler: func(w http.ResponseWriter, r *http.Request) {
				if tc.etag != "" {
					w.Header().Set("ETag", tc.etag)
				}
				if tc.lastMod != "" {
					w.Header().Set("Last-Modified", tc.lastMod)
				}
				w.Header().Set("Cache-Control", "no-cache")
				if tc.validate && (r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "") {
					w.WriteHeader(http.StatusNotModified)
					return
				}
				_, _ = w.Write([]byte("body"))
			}}
			h := c.Handler(origin)

			get(t, h, "http://x/a")
			rec := get(t, h, "http://x/a")

			inm, ims := origin.conditional()
			if inm != tc.wantINM {
				t.Errorf("If-None-Match = %q, want %q", inm, tc.wantINM)
			}
			if ims != tc.wantIMS {
				t.Errorf("If-Modified-Since = %q, want %q", ims, tc.wantIMS)
			}
			want := stateMiss
			if tc.validate {
				want = stateRevalidated
			}
			wantResult(t, rec, want, "body")
		})
	}
}

// TestValidationReplacesClientConditionalHeaders proves the cache asks about ITS
// stored copy: a client's own If-None-Match must not redirect the question.
func TestValidationReplacesClientConditionalHeaders(t *testing.T) {
	c, _ := conformanceCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(time.Hour)})
	origin := &originStub{handler: func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"stored"`)
		w.Header().Set("Cache-Control", "no-cache")
		if r.Header.Get("If-None-Match") == `"stored"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write([]byte("body"))
	}}
	h := c.Handler(origin)

	get(t, h, "http://x/a")
	get(t, h, "http://x/a", "If-None-Match", `"client-guess"`)

	if inm, _ := origin.conditional(); inm != `"stored"` {
		t.Errorf("If-None-Match = %q, want the STORED validator, not the client's", inm)
	}
}

// TestValidationServesNewRepresentation covers the non-304 outcome.
func TestValidationServesNewRepresentation(t *testing.T) {
	c, _ := conformanceCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(time.Hour)})
	version := 1
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Cache-Control", "no-cache")
		if version == 1 {
			_, _ = w.Write([]byte("first"))
			return
		}
		_, _ = w.Write([]byte("second"))
	}))

	wantResult(t, get(t, h, "http://x/a"), stateMiss, "first")
	version = 2
	wantResult(t, get(t, h, "http://x/a"), stateMiss, "second")

	// The replacement was published, not merely forwarded.
	r := httptest.NewRequest(http.MethodGet, "http://x/a", nil)
	e, _, ok := c.lookup(key(r), r)
	if !ok || string(e.Body) != "second" {
		t.Fatalf("stored body = %q, want the replacement", e.Body)
	}
}

// TestValidationDropsAnEntryTheOriginNoLongerAllowsStored proves a stale copy
// cannot survive a validation whose answer must not be cached.
func TestValidationDropsAnEntryTheOriginNoLongerAllowsStored(t *testing.T) {
	c, _ := conformanceCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(time.Hour)})
	private := false
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		if private {
			w.Header().Set("Cache-Control", "private, no-cache")
			_, _ = w.Write([]byte("secret"))
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write([]byte("public"))
	}))

	get(t, h, "http://x/a")
	private = true
	wantResult(t, get(t, h, "http://x/a"), stateMiss, "secret")

	r := httptest.NewRequest(http.MethodGet, "http://x/a", nil)
	if _, _, ok := c.lookup(key(r), r); ok {
		t.Fatal("the superseded entry must be removed once the origin says the resource is private")
	}
}

// TestValidationOriginErrorWithoutStaleGraceForwardsTheError proves Jul does not
// invent an offline mode: with no permitted grace the client sees the origin's
// failure rather than an unvalidated body.
func TestValidationOriginErrorWithoutStaleGraceForwardsTheError(t *testing.T) {
	c, _ := conformanceCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(time.Hour)})
	down := false
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if down {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("upstream down"))
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write([]byte("body"))
	}))

	get(t, h, "http://x/a")
	down = true
	rec := get(t, h, "http://x/a")
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 forwarded from the origin", rec.Code)
	}
	if rec.Body.String() == "body" {
		t.Error("the stored body must not be served when validation failed and no grace applies")
	}
}
