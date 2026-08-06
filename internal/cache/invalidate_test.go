// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"jul/internal/config"
)

// invalidationFixture caches /doc for both GET and HEAD and returns a handler
// that answers unsafe methods with a scriptable status and headers.
type invalidationFixture struct {
	cache   *Cache
	handler http.Handler
	status  int
	headers map[string]string
}

func newInvalidationFixture(t *testing.T, cfg config.CacheConfig) *invalidationFixture {
	t.Helper()
	f := &invalidationFixture{status: http.StatusOK, headers: map[string]string{}}
	c, _ := conformanceCache(t, cfg)
	f.cache = c
	f.handler = c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			w.Header().Set("Cache-Control", "max-age=3600")
			_, _ = w.Write([]byte("cached " + r.URL.Path))
			return
		}
		for k, v := range f.headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(f.status)
	}))
	return f
}

func (f *invalidationFixture) warm(t *testing.T, target string) {
	t.Helper()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r := httptest.NewRequest(method, target, nil)
		f.handler.ServeHTTP(httptest.NewRecorder(), r)
		if _, _, ok := f.cache.lookup(key(r), r); !ok {
			t.Fatalf("%s %s was not cached", method, target)
		}
	}
}

func (f *invalidationFixture) cached(target string) bool {
	r := httptest.NewRequest(http.MethodGet, target, nil)
	_, _, ok := f.cache.lookup(key(r), r)
	return ok
}

func (f *invalidationFixture) cachedHead(target string) bool {
	r := httptest.NewRequest(http.MethodHead, target, nil)
	_, _, ok := f.cache.lookup(key(r), r)
	return ok
}

func (f *invalidationFixture) unsafe(t *testing.T, method, target string) {
	t.Helper()
	f.handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, target, nil))
}

func memCfg() config.CacheConfig {
	return config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(time.Hour)}
}

// TestSuccessfulUnsafeMethodsInvalidateTheTarget covers every unsafe method the
// gateway forwards, for both the GET and the HEAD representation.
func TestSuccessfulUnsafeMethodsInvalidateTheTarget(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, "PURGE"} {
		t.Run(method, func(t *testing.T) {
			f := newInvalidationFixture(t, memCfg())
			f.warm(t, "http://x/doc")
			f.unsafe(t, method, "http://x/doc")

			if f.cached("http://x/doc") {
				t.Error("the GET representation survived a successful unsafe request")
			}
			if f.cachedHead("http://x/doc") {
				t.Error("the HEAD representation survived a successful unsafe request")
			}
		})
	}
}

// TestSafeMethodsNeverInvalidate proves OPTIONS and TRACE are not treated as
// state-changing.
func TestSafeMethodsNeverInvalidate(t *testing.T) {
	for _, method := range []string{http.MethodOptions, http.MethodTrace} {
		t.Run(method, func(t *testing.T) {
			f := newInvalidationFixture(t, memCfg())
			f.warm(t, "http://x/doc")
			f.unsafe(t, method, "http://x/doc")
			if !f.cached("http://x/doc") {
				t.Errorf("%s must not invalidate anything", method)
			}
		})
	}
}

// TestInvalidationStatusRules pins RFC 9111 §4.4's "non-error status" boundary.
func TestInvalidationStatusRules(t *testing.T) {
	cases := []struct {
		status     int
		invalidate bool
	}{
		{http.StatusOK, true},
		{http.StatusCreated, true},
		{http.StatusNoContent, true},
		{http.StatusMovedPermanently, true},
		{http.StatusSeeOther, true},
		{http.StatusNotModified, true},
		{http.StatusBadRequest, false},
		{http.StatusUnauthorized, false},
		{http.StatusForbidden, false},
		{http.StatusNotFound, false},
		{http.StatusConflict, false},
		{http.StatusInternalServerError, false},
		{http.StatusBadGateway, false},
		{http.StatusServiceUnavailable, false},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			f := newInvalidationFixture(t, memCfg())
			f.status = tc.status
			f.warm(t, "http://x/doc")
			f.unsafe(t, http.MethodPost, "http://x/doc")

			gone := !f.cached("http://x/doc")
			if gone != tc.invalidate {
				t.Errorf("status %d invalidated = %v, want %v", tc.status, gone, tc.invalidate)
			}
		})
	}
}

// TestUnsafeRequestThatProducesNoStatusInvalidatesNothing covers the canceled,
// timed-out and hijacked cases, which all leave the exchange without a status.
func TestUnsafeRequestThatProducesNoStatusInvalidatesNothing(t *testing.T) {
	c, _ := conformanceCache(t, memCfg())
	warm := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=3600")
		_, _ = w.Write([]byte("cached"))
	}))
	get(t, warm, "http://x/doc")

	// A handler that writes nothing at all is what a canceled or abandoned
	// upstream exchange looks like from the cache's side.
	silent := c.Handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	silent.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "http://x/doc", nil))

	r := httptest.NewRequest(http.MethodGet, "http://x/doc", nil)
	if _, _, ok := c.lookup(key(r), r); !ok {
		t.Fatal("an unsafe request that produced no response must not invalidate")
	}
}

// TestLocationAndContentLocationInvalidation covers the related-URI targets and,
// above all, that a cross-origin value can never reach another origin's entries.
func TestLocationAndContentLocationInvalidation(t *testing.T) {
	cases := []struct {
		name     string
		header   string
		value    string
		gone     bool
		relative string
	}{
		{name: "same-origin absolute Location", header: "Location", value: "http://x/other", gone: true, relative: "http://x/other"},
		{name: "same-origin relative Location", header: "Location", value: "/other", gone: true, relative: "http://x/other"},
		{name: "same-origin Content-Location", header: "Content-Location", value: "/other", gone: true, relative: "http://x/other"},
		{name: "cross-origin Location is ignored", header: "Location", value: "http://evil/other", relative: "http://x/other"},
		{name: "cross-origin Content-Location is ignored", header: "Content-Location", value: "//evil/other", relative: "http://x/other"},
		{name: "different port is a different origin", header: "Location", value: "http://x:8080/other", relative: "http://x/other"},
		{name: "opaque scheme is ignored", header: "Location", value: "mailto:a@b", relative: "http://x/other"},
		{name: "non-http scheme is ignored", header: "Location", value: "ftp://x/other", relative: "http://x/other"},
		{name: "malformed value is ignored", header: "Location", value: "http://[::1", relative: "http://x/other"},
		{name: "empty value is ignored", header: "Location", value: "", relative: "http://x/other"},
		{name: "traversal cannot escape the key space", header: "Location", value: "/../../../../etc/passwd", relative: "http://x/other"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newInvalidationFixture(t, memCfg())
			f.warm(t, "http://x/doc")
			f.warm(t, tc.relative)
			if tc.value != "" {
				f.headers[tc.header] = tc.value
			}
			f.status = http.StatusCreated
			f.unsafe(t, http.MethodPost, "http://x/doc")

			// The request target itself always goes.
			if f.cached("http://x/doc") {
				t.Error("the request target must always be invalidated")
			}
			if gone := !f.cached(tc.relative); gone != tc.gone {
				t.Errorf("related target invalidated = %v, want %v", gone, tc.gone)
			}
		})
	}
}

// TestCrossOriginLocationCannotEvictAnotherHostsEntry states the security
// property directly rather than through the shared fixture.
func TestCrossOriginLocationCannotEvictAnotherHostsEntry(t *testing.T) {
	c, _ := conformanceCache(t, memCfg())
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Cache-Control", "max-age=3600")
			_, _ = w.Write([]byte("victim"))
			return
		}
		w.Header().Set("Location", "http://victim.example/private")
		w.WriteHeader(http.StatusCreated)
	}))

	victim := httptest.NewRequest(http.MethodGet, "http://victim.example/private", nil)
	h.ServeHTTP(httptest.NewRecorder(), victim)
	if _, _, ok := c.lookup(key(victim), victim); !ok {
		t.Fatal("fixture: the victim entry was not cached")
	}

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "http://attacker.example/write", nil))

	if _, _, ok := c.lookup(key(victim), victim); !ok {
		t.Fatal("an origin evicted another origin's cached entry through a Location header")
	}
}

// ---------------------------------------------------------------------------
// Vary variant membership
// ---------------------------------------------------------------------------

func varyHandler(cache *Cache) http.Handler {
	return cache.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Cache-Control", "max-age=3600")
		w.Header().Set("Vary", "Accept")
		_, _ = w.Write([]byte("variant:" + r.Header.Get("Accept")))
	}))
}

// TestUnsafeMethodRemovesEveryVaryVariant is the requirement that purging the
// base stub alone does not satisfy.
func TestUnsafeMethodRemovesEveryVaryVariant(t *testing.T) {
	for _, name := range []string{"memory", "disk"} {
		t.Run(name, func(t *testing.T) {
			cfg := memCfg()
			if name == "disk" {
				// Large enough for one entry (memory_max_size also caps a
				// single entry's size) but too small for three, so every
				// variant but the newest lives in the disk overflow tier.
				cfg.MemoryMaxSize = config.Size(512)
				cfg.DiskPath = t.TempDir()
				cfg.DiskMaxSize = config.Size(1 << 20)
			}
			c, _ := conformanceCache(t, cfg)
			h := varyHandler(c)

			accepts := []string{"application/json", "application/xml", "text/plain"}
			for _, a := range accepts {
				get(t, h, "http://x/doc", "Accept", a)
			}
			for _, a := range accepts {
				if rec := get(t, h, "http://x/doc", "Accept", a); rec.Header().Get("X-Cache") != stateHit {
					t.Fatalf("fixture: variant %q was not cached", a)
				}
			}

			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodDelete, "http://x/doc", nil))

			for _, a := range accepts {
				rec := get(t, h, "http://x/doc", "Accept", a)
				if rec.Header().Get("X-Cache") == stateHit {
					t.Errorf("variant %q survived invalidation", a)
				}
			}
		})
	}
}

// TestDeletedVariantCannotBeResurrectedByANewStub is the failure mode the
// membership record exists to prevent: an orphan variant entry that a later stub
// with the same Vary would make reachable again.
func TestDeletedVariantCannotBeResurrectedByANewStub(t *testing.T) {
	c, _ := conformanceCache(t, memCfg())
	h := varyHandler(c)

	get(t, h, "http://x/doc", "Accept", "application/json")
	get(t, h, "http://x/doc", "Accept", "application/xml")

	// Delete only the stub, exactly as a pre-membership implementation would,
	// leaving both variant entries in the store.
	base := key(httptest.NewRequest(http.MethodGet, "http://x/doc", nil))
	c.mem.del(base)
	if c.disk != nil {
		c.disk.del(base)
	}

	// Republishing one variant recreates the stub. The other, orphaned variant
	// must not come back with it.
	get(t, h, "http://x/doc", "Accept", "application/json")
	rec := get(t, h, "http://x/doc", "Accept", "application/xml")
	if rec.Header().Get("X-Cache") == stateHit {
		t.Fatal("an orphaned variant became reachable again through a rebuilt stub")
	}
}

// TestLegacyVaryStubFailsClosed proves an entry written before membership
// existed produces a miss rather than an unaccounted-for hit.
func TestLegacyVaryStubFailsClosed(t *testing.T) {
	c, _ := conformanceCache(t, memCfg())
	h := varyHandler(c)

	r := httptest.NewRequest(http.MethodGet, "http://x/doc", nil)
	r.Header.Set("Accept", "application/json")
	base := key(r)
	vk := variantKey(base, []string{"Accept"}, r)

	// A pre-#132 pair: a real variant entry, and a stub with no membership.
	c.set(vk, &Entry{
		Status:     http.StatusOK,
		Header:     http.Header{"Cache-Control": {"max-age=3600"}},
		Body:       []byte("legacy"),
		CreatedAt:  c.clock(),
		ExpiresAt:  c.clock().Add(time.Hour),
		Vary:       []string{"Accept"},
		VaryValues: map[string]string{"Accept": "application/json"},
	})
	c.set(base, &Entry{IsVaryStub: true, Vary: []string{"Accept"}})

	rec := get(t, h, "http://x/doc", "Accept", "application/json")
	if rec.Header().Get("X-Cache") == stateHit {
		t.Fatal("a stub with no membership record must fail closed, not serve an unaccounted variant")
	}
	if rec.Body.String() == "legacy" {
		t.Fatal("the legacy variant was served")
	}
}

// TestChangedVaryReplacesTheVariantSet proves an origin that changes what it
// varies on does not leave the old variants reachable.
func TestChangedVaryReplacesTheVariantSet(t *testing.T) {
	c, _ := conformanceCache(t, memCfg())
	varyOn := "Accept"
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=3600")
		w.Header().Set("Vary", varyOn)
		_, _ = w.Write([]byte("body"))
	}))

	get(t, h, "http://x/doc", "Accept", "application/json")
	if rec := get(t, h, "http://x/doc", "Accept", "application/json"); rec.Header().Get("X-Cache") != stateHit {
		t.Fatal("fixture: the first variant was not cached")
	}

	varyOn = "Accept-Language"
	get(t, h, "http://x/doc", "Accept-Language", "en")

	// The old keying is gone: a request that used to hit now misses, and the
	// old variant key holds nothing.
	if rec := get(t, h, "http://x/doc", "Accept", "application/json"); rec.Header().Get("X-Cache") == stateHit {
		t.Fatal("a variant keyed on the old Vary is still reachable")
	}
}

// TestVariantMembershipIsBounded proves a pathological Vary cannot grow the
// membership record without limit.
func TestVariantMembershipIsBounded(t *testing.T) {
	c, _ := conformanceCache(t, memCfg())
	h := varyHandler(c)

	for i := 0; i < maxVariantsPerResource*3; i++ {
		get(t, h, "http://x/doc", "Accept", fmt.Sprintf("type/%d", i))
	}

	stub, ok := c.get(key(httptest.NewRequest(http.MethodGet, "http://x/doc", nil)))
	if !ok || !stub.IsVaryStub {
		t.Fatal("expected a Vary stub")
	}
	if len(stub.Variants) > maxVariantsPerResource {
		t.Fatalf("membership grew to %d entries, cap is %d", len(stub.Variants), maxVariantsPerResource)
	}
	// The most recent variant is still the one that is served.
	last := fmt.Sprintf("type/%d", maxVariantsPerResource*3-1)
	if rec := get(t, h, "http://x/doc", "Accept", last); rec.Header().Get("X-Cache") != stateHit {
		t.Errorf("the most recently published variant should still be cached, got %q", rec.Header().Get("X-Cache"))
	}
}

// TestConcurrentVariantPublicationKeepsMembershipComplete proves the stub's
// read-modify-write is atomic enough that no published variant is lost from the
// membership record — which would make it uninvalidatable.
func TestConcurrentVariantPublicationKeepsMembershipComplete(t *testing.T) {
	c, _ := conformanceCache(t, memCfg())
	h := varyHandler(c)

	const n = 32
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			get(t, h, "http://x/doc", "Accept", fmt.Sprintf("type/%d", i))
		}(i)
	}
	wg.Wait()

	stub, ok := c.get(key(httptest.NewRequest(http.MethodGet, "http://x/doc", nil)))
	if !ok {
		t.Fatal("expected a stub")
	}
	if len(stub.Variants) != n {
		t.Fatalf("membership records %d of %d concurrently published variants", len(stub.Variants), n)
	}

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodDelete, "http://x/doc", nil))
	for i := 0; i < n; i++ {
		if rec := get(t, h, "http://x/doc", "Accept", fmt.Sprintf("type/%d", i)); rec.Header().Get("X-Cache") == stateHit {
			t.Fatalf("variant %d survived invalidation", i)
		}
	}
}

// TestInvalidationSurvivesDiskRestart proves membership is persisted, so a
// process that restarts and rehydrates from disk can still invalidate every
// variant it wrote before.
func TestInvalidationSurvivesDiskRestart(t *testing.T) {
	dir := t.TempDir()
	cfg := config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(512),
		DiskPath:      dir,
		DiskMaxSize:   config.Size(1 << 20),
		DefaultTTL:    config.Duration(time.Hour),
	}

	accepts := []string{"application/json", "application/xml"}
	first, err := New(cfg, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	h1 := varyHandler(first)
	for _, a := range accepts {
		get(t, h1, "http://x/doc", "Accept", a)
	}
	// Only entries that have been evicted from the memory tier reach disk: Jul
	// does not flush memory on shutdown. Pushing unrelated entries through the
	// small memory tier forces every /doc entry, stub included, down to disk.
	for i := 0; i < 4; i++ {
		get(t, h1, fmt.Sprintf("http://x/filler-%d", i), "Accept", "text/plain")
	}

	// A new process over the same directory.
	second, err := New(cfg, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	h2 := varyHandler(second)
	for _, a := range accepts {
		if rec := get(t, h2, "http://x/doc", "Accept", a); rec.Header().Get("X-Cache") != stateHit {
			t.Fatalf("variant %q did not survive the restart", a)
		}
	}

	h2.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodDelete, "http://x/doc", nil))
	for _, a := range accepts {
		if rec := get(t, h2, "http://x/doc", "Accept", a); rec.Header().Get("X-Cache") == stateHit {
			t.Errorf("variant %q survived invalidation after a restart", a)
		}
	}
}

// TestInvalidationNeverTouchesForeignDiskFiles proves invalidation removes only
// the cache's own content-addressed files.
func TestInvalidationNeverTouchesForeignDiskFiles(t *testing.T) {
	dir := t.TempDir()
	foreign := filepath.Join(dir, "operator-notes.txt")
	if err := os.WriteFile(foreign, []byte("do not delete"), 0o600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, "subdir")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}

	c, _ := conformanceCache(t, config.CacheConfig{
		MemoryMaxSize: config.Size(512),
		DiskPath:      dir,
		DiskMaxSize:   config.Size(1 << 20),
		DefaultTTL:    config.Duration(time.Hour),
	})
	h := varyHandler(c)
	get(t, h, "http://x/doc", "Accept", "application/json")
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodDelete, "http://x/doc", nil))

	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("invalidation deleted a foreign file: %v", err)
	}
	if _, err := os.Stat(nested); err != nil {
		t.Errorf("invalidation deleted a foreign directory: %v", err)
	}
}

// TestInvalidationRacesWithLookupAndStore is the race-detector scenario: the
// three operations that touch membership run against each other under load.
func TestInvalidationRacesWithLookupAndStore(t *testing.T) {
	c, _ := conformanceCache(t, memCfg())
	h := varyHandler(c)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(3)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				get(t, h, "http://x/doc", "Accept", fmt.Sprintf("type/%d", (i+j)%5))
			}
		}(i)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "http://x/doc", nil))
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				c.Purge()
			}
		}()
	}
	wg.Wait()

	// Whatever interleaving occurred, membership must still describe reality:
	// no stub may claim a variant key that no longer resolves to an entry it
	// would serve.
	base := key(httptest.NewRequest(http.MethodGet, "http://x/doc", nil))
	if stub, ok := c.get(base); ok && stub.IsVaryStub {
		if len(stub.Variants) > maxVariantsPerResource {
			t.Fatalf("membership exceeded the cap under concurrency: %d", len(stub.Variants))
		}
		for _, vk := range stub.Variants {
			if !strings.HasPrefix(vk, base) {
				t.Fatalf("membership recorded a key outside its base resource: %q", vk)
			}
		}
	}
}
