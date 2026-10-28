// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

func TestTagRetrySnapshotIncludesInflightOnlyResource(t *testing.T) {
	c, clock := conformanceCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, TagHeaders: []string{"Cache-Tag"}})
	group := testLease(t, 69)
	started, release := make(chan struct{}), make(chan struct{})
	var afterFence atomic.Bool
	origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=1, stale-while-revalidate=60")
		w.Header().Set("Cache-Tag", "other")
		w.Header().Set("ETag", `"fixture"`)
		if r.Header.Get("If-None-Match") != "" {
			close(started)
			<-release
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if afterFence.Load() {
			_, _ = w.Write([]byte("after-fence"))
		} else {
			_, _ = w.Write([]byte("old"))
		}
	})
	h := c.Handler(origin)
	req := leased(httptest.NewRequest(http.MethodGet, "http://example.test/item", nil), group)
	h.ServeHTTP(httptest.NewRecorder(), req)
	clock.advance(2 * time.Second)
	h.ServeHTTP(httptest.NewRecorder(), req)
	<-started
	c.delOne(key(req))
	cutoff := c.genSeq.Add(1)
	sel, err := ParseSelector(Selector{Kind: SelectorTag, Host: "example.test", Tags: []string{"target"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	id := hashKey(resourceKeyOf("example.test", "/item"))
	if _, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{
		ReuseFenceGen: cutoff, ReuseResourceIDs: []string{id},
	}); err != nil {
		t.Fatal(err)
	}
	afterFence.Store(true)
	c.fetchAndStore(httptest.NewRecorder(), req, origin, c.clock())
	close(release)
	waitLeaseReleased(t, group)
	if e, ok := c.get(key(req)); !ok || string(e.Body) != "after-fence" {
		t.Fatal("in-flight-only snapshot validator removed the post-retry entry")
	}
}
