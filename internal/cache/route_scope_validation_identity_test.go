// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"jul/internal/config"
)

func TestScopedValidationDoesNotJoinOrReplaceDifferentEntryGeneration(t *testing.T) {
	for _, vary := range []bool{false, true} {
		for _, disk := range []bool{false, true} {
			t.Run(fmt.Sprintf("vary=%t/disk=%t", vary, disk), func(t *testing.T) {
				cfg := config.CacheConfig{MemoryMaxSize: 1 << 20, TagHeaders: []string{"Cache-Tag"}}
				if disk {
					cfg = largeCacheCfg(t.TempDir(), 512, 1<<20, 8<<20)
					cfg.TagHeaders = []string{"Cache-Tag"}
				}
				c := newTestCache(t, cfg)
				group := testLease(t, 37)
				entered, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				t.Cleanup(func() { once.Do(func() { close(release) }) })
				var version atomic.Int32
				version.Store(1)
				a, _ := scopedHandler(t, c, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Cache-Control", "public, max-age=60")
					if vary {
						w.Header().Set("Vary", "Accept-Language")
					}
					if r.Header.Get("If-None-Match") == `"v1"` {
						close(entered)
						<-release
						w.WriteHeader(http.StatusNotModified)
						return
					}
					if r.Header.Get("If-None-Match") == `"v2"` {
						w.WriteHeader(http.StatusNotModified)
						return
					}
					v := version.Load()
					w.Header().Set("ETag", fmt.Sprintf(`"v%d"`, v))
					w.Header().Set("Cache-Tag", fmt.Sprintf("tag-%d", v))
					_, _ = w.Write([]byte(strings.Repeat(fmt.Sprintf("body-%d", v), 512)))
				}))
				b, _ := scopedHandler(t, c, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Cache-Control", "public, max-age=60")
					w.Header().Set("Cache-Tag", "other")
					if vary {
						w.Header().Set("Vary", "Accept-Language")
					}
					_, _ = w.Write([]byte(strings.Repeat("other", 512)))
				}))
				req := func(validate bool) *http.Request {
					r := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
					r.Header.Set("Accept-Language", "en")
					if validate {
						r.Header.Set("Cache-Control", "no-cache")
					}
					return leased(r, group)
				}
				a.ServeHTTP(httptest.NewRecorder(), req(false))
				old, newer := httptest.NewRecorder(), httptest.NewRecorder()
				oldDone, newDone := make(chan struct{}), make(chan struct{})
				go func() { defer close(oldDone); a.ServeHTTP(old, req(true)) }()
				awaitSignal(t, entered)
				b.ServeHTTP(httptest.NewRecorder(), req(false))
				version.Store(2)
				a.ServeHTTP(httptest.NewRecorder(), req(false))
				go func() { defer close(newDone); a.ServeHTTP(newer, req(true)) }()
				awaitSignal(t, newDone)
				if !strings.HasPrefix(newer.Body.String(), "body-2") {
					t.Fatal("new entry joined an older validation")
				}
				once.Do(func() { close(release) })
				awaitSignal(t, oldDone)
				rec := httptest.NewRecorder()
				a.ServeHTTP(rec, req(false))
				if !strings.HasPrefix(rec.Body.String(), "body-2") {
					t.Fatal("older validation replaced the newer representation")
				}
			})
		}
	}
}
