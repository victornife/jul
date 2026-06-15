package cache

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"jul/internal/config"
)

func newTestCache(t *testing.T, cfg config.CacheConfig) *Cache {
	t.Helper()
	cfg.Enabled = true
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestMemStoreEvictionOverflow(t *testing.T) {
	var evicted []string
	m := newMemStore(600, func(key string, e *Entry) { evicted = append(evicted, key) })

	mk := func(n int) *Entry { return &Entry{Body: make([]byte, n)} }
	m.set("a", mk(300)) // ~556 bytes
	m.set("b", mk(300)) // forces eviction of "a"

	if len(evicted) != 1 || evicted[0] != "a" {
		t.Fatalf("evicted = %v, want [a]", evicted)
	}
	if _, ok := m.get("a"); ok {
		t.Error("a should be evicted")
	}
	if _, ok := m.get("b"); !ok {
		t.Error("b should be present")
	}
}

func TestDiskStorePersistence(t *testing.T) {
	dir := t.TempDir()
	d, err := newDiskStore(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	e := &Entry{Status: 200, Header: http.Header{"X-A": {"1"}}, Body: []byte("hi"), ExpiresAt: time.Now().Add(time.Hour)}
	d.set("k", e)

	// New store over the same dir must rehydrate and serve the entry.
	d2, err := newDiskStore(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := d2.get("k")
	if !ok {
		t.Fatal("entry not found after rehydrate")
	}
	if string(got.Body) != "hi" || got.Header.Get("X-A") != "1" {
		t.Fatalf("decoded entry mismatch: %+v", got)
	}
}

func TestHandlerMissThenHit(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20)})
	var calls int
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Cache-Control", "max-age=60")
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("v1"))
	})
	h := c.Handler(next)

	do := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://x/page", nil))
		return rec
	}

	r1 := do()
	if r1.Header().Get("X-Cache") != "MISS" || r1.Body.String() != "v1" {
		t.Fatalf("first: X-Cache=%q body=%q", r1.Header().Get("X-Cache"), r1.Body.String())
	}
	r2 := do()
	if r2.Header().Get("X-Cache") != "HIT" || r2.Body.String() != "v1" {
		t.Fatalf("second: X-Cache=%q body=%q", r2.Header().Get("X-Cache"), r2.Body.String())
	}
	if calls != 1 {
		t.Fatalf("upstream called %d times, want 1", calls)
	}
}

func TestHandlerNoStoreBypass(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20)})
	var calls int
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte("dynamic"))
	}))
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://x/", nil))
	}
	if calls != 3 {
		t.Fatalf("upstream called %d times, want 3 (uncacheable)", calls)
	}
}

func TestHandlerVaryMismatch(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20)})
	var calls int
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Cache-Control", "max-age=60")
		w.Header().Set("Vary", "Accept-Encoding")
		_, _ = w.Write([]byte(r.Header.Get("Accept-Encoding")))
	}))

	req1 := httptest.NewRequest(http.MethodGet, "http://x/", nil)
	req1.Header.Set("Accept-Encoding", "gzip")
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, req1)

	// Different Accept-Encoding must not reuse the gzip-keyed entry.
	req2 := httptest.NewRequest(http.MethodGet, "http://x/", nil)
	req2.Header.Set("Accept-Encoding", "br")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)

	if calls != 2 {
		t.Fatalf("upstream called %d times, want 2 (vary mismatch)", calls)
	}
	if rec2.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("vary-mismatch X-Cache = %q", rec2.Header().Get("X-Cache"))
	}
}

func TestHandlerConditionalHit(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20)})
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=60")
		w.Header().Set("ETag", `"abc"`)
		_, _ = w.Write([]byte("body"))
	}))

	// Prime the cache.
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://x/", nil))

	req := httptest.NewRequest(http.MethodGet, "http://x/", nil)
	req.Header.Set("If-None-Match", `"abc"`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("conditional status = %d, want 304", rec.Code)
	}
}

func TestHandlerStaleWhileRevalidate(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20)})

	var mu sync.Mutex
	calls := 0
	done := make(chan struct{}, 1)
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.Header().Set("Cache-Control", "max-age=60")
		_, _ = w.Write([]byte("fresh"))
		select {
		case done <- struct{}{}:
		default:
		}
	}))

	// Seed a stale-but-servable entry directly.
	r := httptest.NewRequest(http.MethodGet, "http://x/", nil)
	now := time.Now()
	c.set(key(r), &Entry{
		Status:     200,
		Header:     http.Header{"Cache-Control": {"max-age=60"}},
		Body:       []byte("stale"),
		CreatedAt:  now.Add(-time.Minute),
		ExpiresAt:  now.Add(-time.Second), // expired
		StaleUntil: now.Add(time.Minute),  // within grace
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Header().Get("X-Cache") != "STALE" || rec.Body.String() != "stale" {
		t.Fatalf("stale serve: X-Cache=%q body=%q", rec.Header().Get("X-Cache"), rec.Body.String())
	}

	// Background revalidation should refresh the entry.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("revalidation did not run")
	}
}

func TestFreshnessRules(t *testing.T) {
	c := &Cache{defaultTTL: 30 * time.Second}
	now := time.Now()

	if _, _, ok := c.freshness(200, http.Header{"Cache-Control": {"private"}}, now); ok {
		t.Error("private should not be cacheable")
	}
	if _, _, ok := c.freshness(200, http.Header{"Set-Cookie": {"a=b"}}, now); ok {
		t.Error("Set-Cookie should not be cacheable")
	}
	if _, _, ok := c.freshness(500, http.Header{}, now); ok {
		t.Error("500 should not be cacheable")
	}
	ttl, _, ok := c.freshness(200, http.Header{"Cache-Control": {"max-age=120"}}, now)
	if !ok || ttl != 120*time.Second {
		t.Errorf("max-age ttl = %v ok=%v", ttl, ok)
	}
	ttl, _, ok = c.freshness(200, http.Header{}, now) // falls back to default
	if !ok || ttl != 30*time.Second {
		t.Errorf("default ttl = %v ok=%v", ttl, ok)
	}
}

func TestParseCacheControl(t *testing.T) {
	got := parseCacheControl("public, max-age=60, stale-while-revalidate=30")
	want := map[string]string{"public": "", "max-age": "60", "stale-while-revalidate": "30"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseCacheControl = %v, want %v", got, want)
	}
}
