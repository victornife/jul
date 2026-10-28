// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

func TestScopedPurgeFencesOldValidationRetirement(t *testing.T) {
	for _, mode := range []string{"exact", "tag", "uncertain-commit"} {
		t.Run(mode, func(t *testing.T) {
			cfg := config.CacheConfig{MemoryMaxSize: 1 << 20, TagHeaders: []string{"Cache-Tag"}}
			c, clock := conformanceCache(t, cfg)
			group := testLease(t, 64)
			started, release := make(chan struct{}), make(chan struct{})
			var newResponse atomic.Bool
			origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", "public, max-age=1, stale-while-revalidate=60")
				w.Header().Set("Cache-Tag", "fixture")
				w.Header().Set("ETag", `"fixture"`)
				if r.Header.Get("If-None-Match") != "" {
					close(started)
					<-release
					w.WriteHeader(http.StatusForbidden)
					return
				}
				if newResponse.Load() {
					_, _ = w.Write([]byte("after-fence"))
				} else {
					_, _ = w.Write([]byte("before-fence"))
				}
			})
			h := c.Handler(origin)
			req := leased(httptest.NewRequest(http.MethodGet, "http://example.test/item", nil), group)
			h.ServeHTTP(httptest.NewRecorder(), req)
			clock.advance(2 * time.Second)
			h.ServeHTTP(httptest.NewRecorder(), req)
			<-started
			selector := Selector{Kind: SelectorExactURL, Host: "example.test", URI: "/item"}
			if mode == "tag" {
				selector = Selector{Kind: SelectorTag, Host: "example.test", Tags: []string{"fixture"}}
			}
			sel, err := ParseSelector(selector, nil)
			if err != nil {
				t.Fatal(err)
			}
			var opts InvalidateOptions
			if mode == "uncertain-commit" {
				opts.CommitFence = func(uint64, []string) error { return errors.New("fixture commit failure") }
			}
			_, err = c.InvalidateScope(context.Background(), sel, opts)
			if (mode == "uncertain-commit" && !errors.Is(err, ErrFenceCommit)) || (mode != "uncertain-commit" && err != nil) {
				t.Fatalf("invalidate error = %v", err)
			}
			newResponse.Store(true)
			c.fetchAndStore(httptest.NewRecorder(), req, origin, c.clock())
			close(release)
			waitDrained(t, c)
			waitLeaseReleased(t, group)
			entry, ok := c.get(key(req))
			if !ok || string(entry.Body) != "after-fence" {
				t.Fatalf("old validation removed post-fence entry: found=%v", ok)
			}
		})
	}
}

func TestOldPurgeRetryDoesNotInvalidateNewValidation(t *testing.T) {
	c, clock := conformanceCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
	group := testLease(t, 65)
	oldCutoff := c.genSeq.Add(1)
	started, release := make(chan struct{}), make(chan struct{})
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=1, stale-while-revalidate=60")
		w.Header().Set("ETag", `"fixture"`)
		if r.Header.Get("If-None-Match") != "" {
			close(started)
			<-release
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte("seed"))
	}))
	req := leased(httptest.NewRequest(http.MethodGet, "http://example.test/item", nil), group)
	h.ServeHTTP(httptest.NewRecorder(), req)
	clock.advance(2 * time.Second)
	h.ServeHTTP(httptest.NewRecorder(), req)
	<-started
	sel, err := ParseSelector(Selector{Kind: SelectorExactURL, Host: "example.test", URI: "/item"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{ReuseFenceGen: oldCutoff}); err != nil {
		t.Fatal(err)
	}
	close(release)
	waitDrained(t, c)
	waitLeaseReleased(t, group)
	if _, ok := c.get(key(req)); ok {
		t.Fatal("older purge retry suppressed a newer validation's denial")
	}
}
