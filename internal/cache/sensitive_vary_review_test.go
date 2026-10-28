// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"jul/internal/config"
)

func TestSensitiveVaryValidationDoesNotForwardModifiedRequest(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Minute)})
	var calls int
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Cache-Control", "public, no-cache")
		w.Header().Set("ETag", `"fixture"`)
		if r.Header.Get("If-None-Match") != "" {
			w.Header().Set("Vary", "Cookie, If-None-Match")
			_, _ = w.Write([]byte("conditional-request-body"))
			return
		}
		if calls == 1 {
			_, _ = w.Write([]byte("old-body"))
			return
		}
		w.Header().Set("Vary", "Cookie, If-None-Match")
		_, _ = w.Write([]byte("original-request-body"))
	}))
	req := httptest.NewRequest(http.MethodGet, "http://example.com/item", nil)
	req.Header.Set("Cookie", "session=fixture")
	first := httptest.NewRecorder()
	h.ServeHTTP(first, req)
	next := httptest.NewRecorder()
	h.ServeHTTP(next, req)
	if first.Body.String() != "old-body" || next.Body.String() != "original-request-body" || calls != 3 {
		t.Fatalf("validation did not fetch the original request: calls=%d", calls)
	}
}

func TestSensitiveVaryCanceledBackgroundRefreshRetiresOldEntry(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
	group := testLease(t, 1)
	req := leased(httptest.NewRequest(http.MethodGet, "http://example.com/item", nil), group)
	old := staleEntry("old-body")
	old.ETag = `"fixture"`
	c.set(key(req), old)
	headersCommitted := make(chan struct{})
	origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Set("Vary", "Cookie")
		if r.Header.Get("If-None-Match") != "" {
			w.WriteHeader(http.StatusOK)
			close(headersCommitted)
			<-r.Context().Done()
			return
		}
		_, _ = w.Write([]byte("new-body"))
	})
	h := c.Handler(origin)
	first := httptest.NewRecorder()
	h.ServeHTTP(first, req)
	if first.Header().Get("X-Cache") != stateStale {
		t.Fatal("expected admitted stale fixture")
	}
	<-headersCommitted
	during := httptest.NewRecorder()
	h.ServeHTTP(during, httptest.NewRequest(http.MethodGet, "http://example.com/item", nil))
	if during.Header().Get("X-Cache") != stateMiss || during.Body.String() != "new-body" {
		t.Fatal("old entry remained reusable after sensitive Vary response headers")
	}
	group.Cancel()
	waitDrained(t, c)
	next := httptest.NewRecorder()
	h.ServeHTTP(next, httptest.NewRequest(http.MethodGet, "http://example.com/item", nil))
	if next.Header().Get("X-Cache") != stateMiss || next.Body.String() != "new-body" {
		t.Fatal("old entry remained reusable after sensitive Vary response headers")
	}
}

func TestSensitiveVaryValidationNeverSpoolsRejectedBody(t *testing.T) {
	dir := t.TempDir()
	cfg := largeCacheCfg(dir, 1024, 1<<20, 8<<20)
	c := newTestCache(t, cfg)
	var tempSeen bool
	var calls int
	origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Cache-Control", "public, no-cache")
		w.Header().Set("ETag", `"fixture"`)
		if r.Header.Get("If-None-Match") != "" {
			w.Header().Set("Vary", "Cookie")
			_, _ = w.Write(make([]byte, 8192))
			files, err := os.ReadDir(dir)
			if err != nil {
				t.Error(err)
				return
			}
			for _, file := range files {
				tempSeen = tempSeen || isCacheTempFile(file.Name())
			}
			return
		}
		_, _ = w.Write([]byte("origin"))
	})
	h := c.Handler(origin)
	req := httptest.NewRequest(http.MethodGet, "http://example.com/item", nil)
	req.Header.Set("Cookie", "session=fixture")
	h.ServeHTTP(httptest.NewRecorder(), req)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if tempSeen {
		t.Fatal("sensitive Vary validation response was spooled to disk")
	}
}

func TestSensitiveVaryUnindexedCleanupFailureIsReported(t *testing.T) {
	dir := t.TempDir()
	req, variant := seedSensitiveVaryDisk(t, dir, http.MethodGet, "Cookie")
	c := newTestCache(t, largeCacheCfg(dir, 1<<20, 1<<20, 8<<20))
	hash := hashKey(variant)
	path := filepath.Join(dir, hash)
	c.disk.mu.Lock()
	c.disk.dropIndexedHashLocked(hash)
	c.disk.mu.Unlock()
	c.disk.testRemoveFile = func(name string) error {
		if name == path {
			return &os.PathError{Op: "remove", Path: name, Err: os.ErrPermission}
		}
		return os.Remove(name)
	}
	c.disk.testTruncateFile = func(name string, size int64) error {
		if name == path {
			return &os.PathError{Op: "truncate", Path: name, Err: os.ErrPermission}
		}
		return os.Truncate(name, size)
	}
	c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte("origin"))
	})).ServeHTTP(httptest.NewRecorder(), req)
	failures, _, ok := c.DiskWriteHealth()
	if !ok || failures == 0 {
		t.Fatal("unindexed legacy cleanup failure was not reported")
	}
}

func TestTemporaryCaptureCleanupFailureIsReportedAndBlocksNewSpools(t *testing.T) {
	dir := t.TempDir()
	d, err := newDiskStore(dir, 1<<20, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	failed := false
	stream := d.beginStreamCaptureObserved(nil, func() { failed = true })
	if stream == nil {
		t.Fatal("failed to start fixture capture")
	}
	if ok, _ := d.writeStream(stream, []byte("fixture")); !ok {
		t.Fatal("failed to write fixture capture")
	}
	reserved := d.tempBudget.inflight.Load()
	d.testRemoveFile = func(string) error { return errors.New("fixture cleanup failure") }
	d.abortStream(stream)
	d.abortStream(stream)
	if !failed || d.writeFails.Load() == 0 {
		t.Fatal("temporary capture cleanup failure was not reported")
	}
	if reserved == 0 || d.tempBudget.inflight.Load() != reserved {
		t.Fatal("failed temporary-file cleanup released its reserved budget")
	}
	if next := d.beginStreamCaptureObserved(nil, nil); next != nil {
		d.abortStream(next)
		t.Fatal("new disk capture started despite unresolved cleanup failure")
	}
	restarted, err := newDiskStore(dir, 1<<20, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	next := restarted.beginStreamCaptureObserved(nil, nil)
	if next == nil {
		t.Fatal("disk capture did not recover after startup cleanup")
	}
	restarted.abortStream(next)
}

func TestTemporaryCleanupFailureDuringRehydrateBlocksSpools(t *testing.T) {
	dir := t.TempDir()
	d, err := newDiskStore(dir, 1<<20, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".jul-cache-tmp-fixture")
	if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.testRemoveFile = func(name string) error {
		if name == path {
			return os.ErrPermission
		}
		return os.Remove(name)
	}
	d.rehydrate()
	if !d.tempCleanupFailed.Load() || d.writeFails.Load() == 0 || d.beginStreamCaptureObserved(nil, nil) != nil {
		t.Fatal("startup cleanup failure did not block disk capture")
	}
}
