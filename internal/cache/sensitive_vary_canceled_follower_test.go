// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCanceledSensitiveRefreshFollowersBypassBlockedDisk(t *testing.T) {
	dir := t.TempDir()
	cfg := largeCacheCfg(dir, 1024, 1<<20, 8<<20)
	c := newTestCache(t, cfg)
	group := testLease(t, 1)
	warm := leased(httptest.NewRequest(http.MethodGet, "http://example.com/item", nil), group)
	old := staleEntry(strings.Repeat("x", 2048))
	old.ETag = `"fixture"`
	c.set(key(warm), old)
	originEntered, releaseOrigin := make(chan struct{}), make(chan struct{})
	diskEntered, releaseDisk := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseDisk) }) })
	path := filepath.Join(dir, hashKey(key(warm)))
	c.disk.testRemoveFile = func(name string) error {
		if name == path {
			close(diskEntered)
			<-releaseDisk
		}
		return os.Remove(name)
	}
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" {
			close(originEntered)
			<-releaseOrigin
			w.Header().Set("Vary", "Cookie")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=60")
		_, _ = w.Write([]byte(strings.Repeat("fixture", 300)))
	}))
	h.ServeHTTP(httptest.NewRecorder(), warm)
	<-originEntered
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	follower := leased(httptest.NewRequest(http.MethodGet, "http://example.com/item", nil).WithContext(ctx), group)
	follower.Header.Set("Cache-Control", "no-cache")
	rec, done := httptest.NewRecorder(), make(chan struct{})
	go func() { h.ServeHTTP(rec, follower); close(done) }()
	awaitJoined(t, c, revalidateKey{key: key(warm), gen: group.Generation()}, 1)
	group.Cancel()
	close(releaseOrigin)
	<-diskEntered
	select {
	case <-done:
	case <-time.After(time.Second):
		releaseOnce.Do(func() { close(releaseDisk) })
		cancel()
		<-done
		t.Fatal("canceled sensitive-refresh follower waited for disk cleanup")
	}
	releaseOnce.Do(func() { close(releaseDisk) })
	waitDrained(t, c)
	if rec.Header().Get("X-Cache") != stateBypass || rec.Body.String() != strings.Repeat("fixture", 300) {
		t.Fatal("follower did not independently bypass the cache")
	}
}
