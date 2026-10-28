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

func TestSensitiveVaryFollowersDoNotWaitForDiskCleanup(t *testing.T) {
	dir := t.TempDir()
	cfg := largeCacheCfg(dir, 1024, 1<<20, 8<<20)
	c := newTestCache(t, cfg)
	originEntered, releaseHeaders := make(chan struct{}), make(chan struct{})
	diskEntered, releaseDisk := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseDisk) }) })
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" {
			close(originEntered)
			<-releaseHeaders
			w.Header().Set("Vary", "Cookie")
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Header.Get("X-Fixture-Identity") != "" {
			w.Header().Set("Cache-Control", "public, max-age=60")
			_, _ = w.Write([]byte(strings.Repeat(r.Header.Get("X-Fixture-Identity"), 200)))
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("ETag", `"fixture"`)
		_, _ = w.Write([]byte(strings.Repeat("x", 2048)))
	}))
	warm := httptest.NewRequest(http.MethodGet, "http://example.com/item", nil)
	h.ServeHTTP(httptest.NewRecorder(), warm)
	path := filepath.Join(dir, hashKey(key(warm)))
	c.disk.testRemoveFile = func(name string) error {
		if name == path {
			close(diskEntered)
			<-releaseDisk
		}
		return os.Remove(name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	recs := []*httptest.ResponseRecorder{httptest.NewRecorder(), httptest.NewRecorder()}
	var wg sync.WaitGroup
	followerDone := make(chan struct{})
	for i, identity := range []string{"fixture-alice", "fixture-bob"} {
		req := httptest.NewRequest(http.MethodGet, "http://example.com/item", nil).WithContext(ctx)
		req.Header.Set("X-Fixture-Identity", identity)
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h.ServeHTTP(recs[i], req)
			if i == 1 {
				close(followerDone)
			}
		}(i)
		if i == 0 {
			<-originEntered
		}
	}
	awaitJoined(t, c, revalidateKey{key: key(warm)}, 1)
	close(releaseHeaders)
	<-diskEntered
	select {
	case <-followerDone:
	case <-time.After(time.Second):
		releaseOnce.Do(func() { close(releaseDisk) })
		cancel()
		wg.Wait()
		t.Fatal("follower waited for blocked physical disk cleanup")
	}
	releaseOnce.Do(func() { close(releaseDisk) })
	wg.Wait()
	if recs[1].Header().Get("X-Cache") != stateBypass ||
		recs[1].Body.String() != strings.Repeat("fixture-bob", 200) {
		t.Fatal("follower did not receive its own origin response")
	}
}
