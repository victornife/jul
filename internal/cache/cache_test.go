package cache

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"jul/internal/config"
)

// testLogger returns a logger that discards output, for tests that exercise the
// disk tier's warning paths without polluting test output.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestCache(t *testing.T, cfg config.CacheConfig) *Cache {
	t.Helper()
	cfg.Enabled = true
	c, err := New(cfg, testLogger())
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
	d, err := newDiskStore(dir, 1<<20, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	e := &Entry{Status: 200, Header: http.Header{"X-A": {"1"}}, Body: []byte("hi"), ExpiresAt: time.Now().Add(time.Hour)}
	d.set("k", e)

	// New store over the same dir must rehydrate and serve the entry.
	d2, err := newDiskStore(dir, 1<<20, testLogger())
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

// TestDiskStoreFileMode proves cache files are written owner-only (0o600) so a
// cached response body is never world-readable.
func TestDiskStoreFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not meaningful on Windows")
	}
	dir := t.TempDir()
	d, err := newDiskStore(dir, 1<<20, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	d.set("k", &Entry{Status: 200, Body: []byte("hi"), ExpiresAt: time.Now().Add(time.Hour)})

	fi, err := os.Stat(d.path(hashKey("k")))
	if err != nil {
		t.Fatalf("stat cache file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("cache file mode = %o, want 600", perm)
	}
}

// TestDiskStoreAtomicNoTempLeftovers proves the atomic temp+rename write leaves
// no stray temp files behind: after a series of writes the directory holds only
// well-formed cache files.
func TestDiskStoreAtomicNoTempLeftovers(t *testing.T) {
	dir := t.TempDir()
	d, err := newDiskStore(dir, 1<<20, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		d.set(strconv.Itoa(i), &Entry{Status: 200, Body: []byte("x"), ExpiresAt: time.Now().Add(time.Hour)})
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !isCacheFile(e.Name()) {
			t.Fatalf("non-cache file left in cache dir after writes: %q", e.Name())
		}
	}
}

// TestDiskStoreIgnoresForeignFiles proves a directory holding unrelated files is
// safe to use as disk_path: foreign files are never indexed, served, or deleted
// by LRU eviction, and their presence is logged once.
func TestDiskStoreIgnoresForeignFiles(t *testing.T) {
	dir := t.TempDir()
	foreign := filepath.Join(dir, "operator-notes.txt")
	if err := os.WriteFile(foreign, []byte("do not delete"), 0o644); err != nil {
		t.Fatal(err)
	}

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// A tiny budget makes eviction eager, so the only thing protecting the
	// foreign file is its exclusion from the index.
	d, err := newDiskStore(dir, 64, logger)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.items) != 0 {
		t.Fatalf("foreign file was indexed: %d items", len(d.items))
	}
	if !strings.Contains(logBuf.String(), "foreign") {
		t.Errorf("expected a warning about foreign files, got: %q", logBuf.String())
	}

	// Drive writes that force eviction; the foreign file must be untouched.
	for i := 0; i < 16; i++ {
		d.set(strconv.Itoa(i), &Entry{Status: 200, Body: make([]byte, 128), ExpiresAt: time.Now().Add(time.Hour)})
	}
	got, err := os.ReadFile(foreign)
	if err != nil {
		t.Fatalf("foreign file was removed by the cache: %v", err)
	}
	if string(got) != "do not delete" {
		t.Fatalf("foreign file was modified: %q", got)
	}
}

// TestMemStoreEvictHookRunsOutsideLock proves the disk-overflow onEvict hook runs
// without the memory-tier lock held: the hook re-enters the same store, which
// would deadlock if set still held m.mu while evicting.
func TestMemStoreEvictHookRunsOutsideLock(t *testing.T) {
	var m *memStore
	m = newMemStore(600, func(key string, _ *Entry) {
		_, _ = m.get(key) // re-entrant read; deadlocks if onEvict holds m.mu
	})

	done := make(chan struct{})
	go func() {
		m.set("a", &Entry{Body: make([]byte, 300)})
		m.set("b", &Entry{Body: make([]byte, 300)}) // overflows -> evicts "a" -> onEvict
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("eviction hook deadlocked: onEvict ran while holding the mem lock")
	}
}
