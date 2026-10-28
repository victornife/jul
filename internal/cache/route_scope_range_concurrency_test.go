// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestScopedRangeFillsDoNotWaitForIncompatibleRoutes(t *testing.T) {
	for _, dimension := range []string{"owner", "compatibility"} {
		t.Run(dimension, func(t *testing.T) {
			cfg := lockCfg(30 * time.Second)
			cfg.RangeEnabled = true
			c := newTestCache(t, cfg)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			a, aScope := scopedHandler(t, c, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				<-release
				w.Header().Set("Cache-Control", "public, max-age=60")
				http.ServeContent(w, r, "item.txt", time.Time{}, strings.NewReader("alpha-body"))
			}))
			b := handlerWithScope(t, c, otherRouteScope(t, c, aScope, dimension), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", "public, max-age=60")
				http.ServeContent(w, r, "item.txt", time.Time{}, strings.NewReader("bravo-body"))
			}))
			req := func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
				r.Header.Set("Range", "bytes=0-4")
				return r
			}
			leader, independent := httptest.NewRecorder(), httptest.NewRecorder()
			var wg sync.WaitGroup
			wg.Add(1)
			go func() { defer wg.Done(); a.ServeHTTP(leader, req()) }()
			awaitSignal(t, entered)
			done := make(chan struct{})
			wg.Add(1)
			go func() { defer wg.Done(); defer close(done); b.ServeHTTP(independent, req()) }()
			awaitSignal(t, done)
			if independent.Code != http.StatusPartialContent || independent.Body.String() != "bravo" {
				t.Fatalf("independent range: status=%d body=%q", independent.Code, independent.Body.String())
			}
			once.Do(func() { close(release) })
			wg.Wait()
			if leader.Code != http.StatusPartialContent || leader.Body.String() != "alpha" {
				t.Fatalf("leader range: status=%d body=%q", leader.Code, leader.Body.String())
			}
		})
	}
}
