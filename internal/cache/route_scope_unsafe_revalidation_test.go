// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"jul/internal/config"
)

func TestUnsafeInvalidationCannotBeUndoneByScopedValidation(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNotModified} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
			group := testLease(t, 41)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			var deleted atomic.Bool
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			h, scope := scopedHandler(t, c, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					deleted.Store(true)
					w.WriteHeader(http.StatusNoContent)
					return
				}
				w.Header().Set("Cache-Control", "public, max-age=60")
				w.Header().Set("ETag", `"fixture"`)
				if r.Header.Get("If-None-Match") != "" {
					close(entered)
					<-release
					w.WriteHeader(status)
					if status == http.StatusOK {
						_, _ = w.Write([]byte("retired"))
					}
					return
				}
				if deleted.Load() {
					_, _ = w.Write([]byte("post-delete"))
				} else {
					_, _ = w.Write([]byte("old"))
				}
			}))
			get := func(validate bool) *http.Request {
				r := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
				if validate {
					r.Header.Set("Cache-Control", "no-cache")
				}
				return leased(r, group)
			}
			h.ServeHTTP(httptest.NewRecorder(), get(false))
			done := make(chan struct{})
			go func() { defer close(done); h.ServeHTTP(httptest.NewRecorder(), get(true)) }()
			awaitSignal(t, entered)
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodDelete, "http://example.test/item", nil))
			once.Do(func() { close(release) })
			awaitSignal(t, done)
			if e, _, found := c.lookupWithOwner(key(get(false)), get(false), &scope); found &&
				(string(e.Body) == "old" || string(e.Body) == "retired") {
				t.Fatal("validation republished content invalidated by DELETE")
			}
		})
	}
}
