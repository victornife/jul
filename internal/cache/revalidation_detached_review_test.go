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

func TestDetachedValidationRemainsVisibleToLaterPurge(t *testing.T) {
	c, clock := conformanceCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, TagHeaders: []string{"Cache-Tag"}})
	group := testLease(t, 68)
	started, release := make(chan struct{}), make(chan struct{})
	var afterFence atomic.Bool
	origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=1, stale-while-revalidate=60")
		w.Header().Set("ETag", `"fixture"`)
		if r.URL.Path == "/tagged" {
			w.Header().Set("Cache-Tag", "unrelated")
		}
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
	tagged := leased(httptest.NewRequest(http.MethodGet, "http://example.test/tagged", nil), group)
	target := leased(httptest.NewRequest(http.MethodGet, "http://example.test/item", nil), group)
	h.ServeHTTP(httptest.NewRecorder(), tagged)
	h.ServeHTTP(httptest.NewRecorder(), target)
	clock.advance(2 * time.Second)
	h.ServeHTTP(httptest.NewRecorder(), target)
	<-started
	tag, err := ParseSelector(Selector{Kind: SelectorTag, Host: "example.test", Tags: []string{"unrelated"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.InvalidateScope(context.Background(), tag, InvalidateOptions{}); err != nil {
		t.Fatal(err)
	}
	if c.inflightRevalidations() != 0 {
		t.Fatal("fixture did not detach the validation")
	}
	exact, err := ParseSelector(Selector{Kind: SelectorExactURL, Host: "example.test", URI: "/item"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p := c.PreviewScope(exact, PreviewOptions{}); p.InflightFillsMatched != 1 {
		t.Fatalf("detached validation missing from preview: %d", p.InflightFillsMatched)
	}
	if _, err := c.InvalidateScope(context.Background(), exact, InvalidateOptions{}); err != nil {
		t.Fatal(err)
	}
	afterFence.Store(true)
	c.fetchAndStore(httptest.NewRecorder(), target, origin, c.clock())
	close(release)
	waitLeaseReleased(t, group)
	if e, ok := c.get(key(target)); !ok || string(e.Body) != "after-fence" {
		t.Fatal("detached validator removed the post-purge entry")
	}
	c.genMu.Lock()
	active := len(c.activeValidations)
	c.genMu.Unlock()
	if active != 0 {
		t.Fatalf("active validation tracking leaked %d calls", active)
	}
}
