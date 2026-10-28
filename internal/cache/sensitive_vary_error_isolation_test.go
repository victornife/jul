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

	"jul/internal/config"
)

func TestSensitiveVaryErrorDoesNotShareLeaderDecision(t *testing.T) {
	cfg := largeCacheCfg(t.TempDir(), 1<<20, 1<<20, 8<<20)
	cfg.DefaultTTL = config.Duration(time.Minute)
	c := newTestCache(t, cfg)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if r.Header.Get("If-None-Match") != "" {
			close(entered)
			<-release
			w.Header().Set("Vary", "Cookie")
			w.WriteHeader(http.StatusServiceUnavailable)
			<-r.Context().Done()
			return
		}
		if n == 1 {
			w.Header().Set("Cache-Control", "public, no-cache, stale-if-error=60")
			w.Header().Set("ETag", `"fixture"`)
			_, _ = w.Write([]byte("old-body"))
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte("follower-origin"))
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://example.com/item", nil))
	diskEntered, releaseDisk := make(chan struct{}), make(chan struct{})
	var releaseDiskOnce sync.Once
	t.Cleanup(func() { releaseDiskOnce.Do(func() { close(releaseDisk) }) })
	c.disk.onBeforePublishLock = func(string, uint64) {
		close(diskEntered)
		<-releaseDisk
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	recs := []*httptest.ResponseRecorder{httptest.NewRecorder(), httptest.NewRecorder()}
	var wg sync.WaitGroup
	for i, cookie := range []string{"session=fixture-alice", "session=fixture-bob"} {
		req := httptest.NewRequest(http.MethodGet, "http://example.com/item", nil).WithContext(ctx)
		req.Header.Set("Cookie", cookie)
		wg.Add(1)
		go func(i int) { defer wg.Done(); h.ServeHTTP(recs[i], req) }(i)
		if i == 0 {
			<-entered
		}
	}
	awaitJoined(t, c, revalidateKey{key: key(httptest.NewRequest(http.MethodGet, "http://example.com/item", nil))}, 1)
	close(release)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		releaseDiskOnce.Do(func() { close(releaseDisk) })
		cancel()
		<-done
		t.Fatal("followers waited for disk publication")
	}
	select {
	case <-diskEntered:
		t.Fatal("identity-specific error attempted shared disk publication")
	default:
	}
	if recs[0].Header().Get("X-Cache") != stateStale || recs[0].Body.String() != "old-body" ||
		recs[1].Body.String() != "follower-origin" || calls.Load() != 3 {
		t.Fatal("a sensitive leader error was shared with the follower")
	}
}
