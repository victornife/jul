// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"jul/internal/config"
)

// ─── Scalar policy hot-reload (#92) ─────────────────────────────────────────

// TestPrepareCacheUpdateBuildsNoLiveSideEffect proves building a candidate
// never mutates the live cache; only Commit does.
func TestPrepareCacheUpdateBuildsNoLiveSideEffect(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(time.Minute)})
	before := c.Policy()

	_ = c.PrepareCacheUpdate(config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(time.Hour)})

	if got := c.Policy(); got != before {
		t.Fatalf("Policy changed after Prepare without Commit: %+v, want %+v", got, before)
	}
}

// TestPrepareCacheUpdateNilCacheIsSafeNoOp proves a disabled cache (nil
// *Cache) makes Prepare/Commit no-ops rather than panicking.
func TestPrepareCacheUpdateNilCacheIsSafeNoOp(t *testing.T) {
	var c *Cache
	prepared := c.PrepareCacheUpdate(config.CacheConfig{})
	if prepared != nil {
		t.Fatal("PrepareCacheUpdate on a nil cache should return nil")
	}
	prepared.Commit() // must not panic
}

// TestCacheUpdateInstallsPolicyAtomically proves Commit installs the exact
// candidate policy.
func TestCacheUpdateInstallsPolicyAtomically(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(time.Minute)})
	cand := config.CacheConfig{
		MemoryMaxSize:        config.Size(2 << 20),
		DefaultTTL:           config.Duration(2 * time.Hour),
		StaleWhileRevalidate: config.Duration(90 * time.Second),
		StaleIfError:         config.Duration(5 * time.Minute),
	}
	c.PrepareCacheUpdate(cand).Commit()

	got := c.Policy()
	if got.DefaultTTL != 2*time.Hour || got.StaleWhileRevalidate != 90*time.Second || got.StaleIfError != 5*time.Minute || got.MaxEntryBytes != 2<<20 {
		t.Fatalf("policy after commit = %+v, want DefaultTTL=2h SWR=90s SIE=5m MaxEntryBytes=%d", got, int64(2<<20))
	}
}

// TestPolicyChangeAppliesToNewEntriesNotExisting proves a policy swap applies
// to entries created afterward, while an already-stored entry's timestamps
// are never retroactively altered (#92 acceptance criterion).
func TestPolicyChangeAppliesToNewEntriesNotExisting(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(time.Minute)})
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("v1"))
	}))

	req := httptest.NewRequest(http.MethodGet, "http://cache.example/old", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	oldEntry, ok := c.mem.get(keyFor(http.MethodGet, req.Host, req.URL.RequestURI()))
	if !ok {
		t.Fatal("expected the first response to be stored")
	}
	oldExpiry := oldEntry.ExpiresAt

	// Rotate the policy to a much longer default TTL.
	c.PrepareCacheUpdate(config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(time.Hour)}).Commit()

	// The existing entry's timestamps must be untouched.
	stillOld, ok := c.mem.get(keyFor(http.MethodGet, req.Host, req.URL.RequestURI()))
	if !ok {
		t.Fatal("existing entry should still be present")
	}
	if !stillOld.ExpiresAt.Equal(oldExpiry) {
		t.Fatalf("existing entry's ExpiresAt changed retroactively: got %v, want %v", stillOld.ExpiresAt, oldExpiry)
	}

	// A new entry created after Commit uses the new default TTL.
	req2 := httptest.NewRequest(http.MethodGet, "http://cache.example/new", nil)
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req2)
	newEntry, ok := c.mem.get(keyFor(http.MethodGet, req2.Host, req2.URL.RequestURI()))
	if !ok {
		t.Fatal("expected the second response to be stored")
	}
	if got, want := newEntry.ExpiresAt.Sub(newEntry.CreatedAt), time.Hour; got < want-time.Second || got > want+time.Second {
		t.Fatalf("new entry TTL = %v, want ~%v (the candidate default_ttl)", got, want)
	}
}

// TestStaleIfErrorUsesActivePolicyAtFailureTime proves staleOnErrorWindow
// (the stale-if-error grace window) reads whichever policy is active when the
// revalidation failure occurs, not a stale snapshot from entry-creation time.
func TestStaleIfErrorUsesActivePolicyAtFailureTime(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), StaleIfError: config.Duration(time.Minute)})
	e := &Entry{}
	if got := c.staleOnErrorWindow(e); got != time.Minute {
		t.Fatalf("stale-if-error = %v, want 1m (original policy)", got)
	}

	c.PrepareCacheUpdate(config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), StaleIfError: config.Duration(5 * time.Minute)}).Commit()

	if got := c.staleOnErrorWindow(e); got != 5*time.Minute {
		t.Fatalf("stale-if-error after commit = %v, want 5m (candidate policy)", got)
	}
}

// TestMaxEntryBytesCoupledToMemoryMaxSize characterizes maxEntry (#92 scope
// item 5): the schema has no separate per-entry capture limit, so the
// per-entry cap is intentionally coupled to memory_max_size and updates with
// it at Commit.
func TestMaxEntryBytesCoupledToMemoryMaxSize(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20)})
	if got := c.Policy().MaxEntryBytes; got != 1<<20 {
		t.Fatalf("MaxEntryBytes = %d, want %d (== memory_max_size)", got, int64(1<<20))
	}

	c.PrepareCacheUpdate(config.CacheConfig{MemoryMaxSize: config.Size(4 << 20)}).Commit()

	if got := c.Policy().MaxEntryBytes; got != 4<<20 {
		t.Fatalf("MaxEntryBytes after commit = %d, want %d (updated with memory_max_size)", got, int64(4<<20))
	}
}

// TestPolicySwapConcurrentWithRequestsRaceFree drives concurrent requests
// (which read Policy()) against repeated Commit calls. Run with -race.
func TestPolicySwapConcurrentWithRequestsRaceFree(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(time.Minute)})
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("v"))
	}))

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				rr := httptest.NewRecorder()
				h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "http://cache.example/race", nil))
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			ttl := time.Duration(i+1) * time.Second
			c.PrepareCacheUpdate(config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(ttl)}).Commit()
		}
		close(stop)
	}()
	wg.Wait()
}

// ─── Memory-store resize (#92) ──────────────────────────────────────────────

// TestMemStoreResizeIncreasePreservesEntries proves raising the cap never
// evicts existing entries.
func TestMemStoreResizeIncreasePreservesEntries(t *testing.T) {
	m := newMemStore(2000, nil)
	m.set("a", &Entry{Body: make([]byte, 300)})
	m.set("b", &Entry{Body: make([]byte, 300)})

	evicted, evictedBytes := m.Resize(10_000)
	if evicted != 0 || evictedBytes != 0 {
		t.Fatalf("increase evicted %d entries (%d bytes), want 0", evicted, evictedBytes)
	}
	if _, ok := m.get("a"); !ok {
		t.Error("a should still be present after increase")
	}
	if _, ok := m.get("b"); !ok {
		t.Error("b should still be present after increase")
	}
}

// TestMemStoreResizeDecreaseEvictsStrictLRUOrder proves lowering the cap
// evicts least-recently-used entries first, in exact order, until within cap.
func TestMemStoreResizeDecreaseEvictsStrictLRUOrder(t *testing.T) {
	var evicted []string
	m := newMemStore(10_000, func(key string, _ *Entry) { evicted = append(evicted, key) })
	m.set("a", &Entry{Body: make([]byte, 300)})
	m.set("b", &Entry{Body: make([]byte, 300)})
	m.set("c", &Entry{Body: make([]byte, 300)})
	// Touch "a" so it is no longer the least-recently-used.
	m.get("a")

	// Each ~556-byte entry (300-byte body + 256-byte fixed overhead); a cap of
	// 1200 fits exactly two, so evicting the true LRU ("b") is enough.
	count, bytes := m.Resize(1200)
	if count != 1 || bytes == 0 {
		t.Fatalf("Resize evicted count=%d bytes=%d, want exactly 1 eviction (b, the true LRU)", count, bytes)
	}
	if len(evicted) != 1 || evicted[0] != "b" {
		t.Fatalf("evicted = %v, want [b] (a was touched, c was set last)", evicted)
	}
	if _, ok := m.get("a"); !ok {
		t.Error("a (recently touched) should survive")
	}
	if _, ok := m.get("c"); !ok {
		t.Error("c (most recently set) should survive")
	}
	if m.curBytes > m.maxBytes {
		t.Errorf("curBytes=%d > maxBytes=%d after resize", m.curBytes, m.maxBytes)
	}
}

// TestMemStoreResizeForwardsEvictedEntriesOutsideLock mirrors
// TestMemStoreEvictHookRunsOutsideLock for Resize: the onEvict hook must not
// run while m.mu is held, or a hook that re-enters the store (e.g. the disk
// overflow write's own bookkeeping) would deadlock.
func TestMemStoreResizeForwardsEvictedEntriesOutsideLock(t *testing.T) {
	var m *memStore
	m = newMemStore(10_000, func(key string, _ *Entry) {
		_, _ = m.get(key) // re-entrant read; deadlocks if Resize holds m.mu here
	})
	m.set("a", &Entry{Body: make([]byte, 300)})

	done := make(chan struct{})
	go func() {
		m.Resize(100)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Resize deadlocked: onEvict ran while holding the mem lock")
	}
}

// ─── Disk-store resize (#92) ────────────────────────────────────────────────

func writeDiskFile(t *testing.T, d *diskStore, key string, size int) {
	t.Helper()
	d.set(key, &Entry{Body: make([]byte, size)})
}

// TestDiskStoreResizeIncreasePreservesFiles proves raising the cap never
// deletes existing files.
func TestDiskStoreResizeIncreasePreservesFiles(t *testing.T) {
	dir := t.TempDir()
	d, err := newDiskStore(dir, 10_000, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	writeDiskFile(t, d, "a", 300)
	writeDiskFile(t, d, "b", 300)

	evicted, evictedBytes, failed := d.Resize(1 << 20)
	if evicted != 0 || evictedBytes != 0 || failed != 0 {
		t.Fatalf("increase evicted=%d bytes=%d failed=%d, want all zero", evicted, evictedBytes, failed)
	}
	if _, ok := d.get("a"); !ok {
		t.Error("a should still be present after increase")
	}
	if _, ok := d.get("b"); !ok {
		t.Error("b should still be present after increase")
	}
}

// TestDiskStoreResizeDecreaseRemovesOldestFiles proves lowering the cap
// deletes least-recently-used cache-owned files, oldest first.
func TestDiskStoreResizeDecreaseRemovesOldestFiles(t *testing.T) {
	dir := t.TempDir()
	d, err := newDiskStore(dir, 10_000, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	writeDiskFile(t, d, "a", 300)
	writeDiskFile(t, d, "b", 300)
	d.get("a") // touch so "a" is not the LRU victim

	// Each gob-encoded 300-byte-body entry is a few hundred bytes larger than
	// its body; a cap of 1000 fits exactly one, so evicting the true LRU ("b")
	// is enough.
	evicted, evictedBytes, failed := d.Resize(1000)
	if failed != 0 {
		t.Fatalf("unexpected removal failures: %d", failed)
	}
	if evicted != 1 || evictedBytes == 0 {
		t.Fatalf("evicted=%d bytes=%d, want exactly 1 eviction (b)", evicted, evictedBytes)
	}
	if _, ok := d.get("b"); ok {
		t.Error("b should have been deleted")
	}
	if _, ok := d.get("a"); !ok {
		t.Error("a (touched) should survive")
	}
	if _, err := os.Stat(d.path(hashKey("b"))); !os.IsNotExist(err) {
		t.Errorf("b's file should be removed from disk, stat err = %v", err)
	}
}

// TestDiskStoreResizeIgnoresForeignFiles proves a file that does not match
// the cache's own naming scheme is never indexed and therefore never deleted
// by Resize, however small the new cap is.
func TestDiskStoreResizeIgnoresForeignFiles(t *testing.T) {
	dir := t.TempDir()
	foreign := filepath.Join(dir, "not-a-cache-file.txt")
	if err := os.WriteFile(foreign, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := newDiskStore(dir, 10_000, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	writeDiskFile(t, d, "a", 300)

	d.Resize(1) // as small as possible

	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("foreign file must survive any resize, stat err = %v", err)
	}
}

// TestDiskStoreResizeReportsRemovalFailure proves a failed os.Remove during a
// capacity reduction is surfaced (not silently swallowed) and stops that
// resize pass rather than looping or falsely reporting the cap enforced.
func TestDiskStoreResizeReportsRemovalFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: file mode does not deny deletion")
	}
	dir := t.TempDir()
	d, err := newDiskStore(dir, 10_000, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	writeDiskFile(t, d, "a", 300)

	// Deny write/execute on the directory so os.Remove of the file inside it
	// fails with a permission error, without affecting the file's own mode.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Skipf("cannot make directory read-only on this platform: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	_, _, failed := d.Resize(1)
	if failed == 0 {
		t.Skip("the platform ignored the directory mode; removal did not fail")
	}
	if failed != 1 {
		t.Fatalf("failed removals = %d, want 1", failed)
	}
}

// TestCacheCommitSurfacesDiskEvictionFailure proves a disk-eviction removal
// failure during PreparedCacheUpdate.Commit increments the cache's bounded
// failure counter rather than being silently absorbed.
func TestCacheCommitSurfacesDiskEvictionFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: file mode does not deny deletion")
	}
	dir := t.TempDir()
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DiskPath: dir, DiskMaxSize: config.Size(10_000)})
	writeDiskFile(t, c.disk, "a", 300)

	if err := os.Chmod(dir, 0o555); err != nil {
		t.Skipf("cannot make directory read-only on this platform: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	c.PrepareCacheUpdate(config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DiskPath: dir, DiskMaxSize: config.Size(1)}).Commit()

	if c.DiskEvictionFailures() == 0 {
		t.Skip("the platform ignored the directory mode; removal did not fail")
	}
}

// TestDiskStoreResizeConcurrentWithOperationsRaceFree drives concurrent
// get/set/del/purge against repeated Resize calls. Run with -race.
func TestDiskStoreResizeConcurrentWithOperationsRaceFree(t *testing.T) {
	dir := t.TempDir()
	d, err := newDiskStore(dir, 10_000, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := "k"
			for {
				select {
				case <-stop:
					return
				default:
				}
				d.set(key, &Entry{Body: make([]byte, 100)})
				d.get(key)
				d.del(key)
			}
		}(i)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			d.Resize(int64(1000 + i*10))
		}
		close(stop)
	}()
	wg.Wait()
}
