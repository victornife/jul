// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

func TestScopedRevalidationRetainsOwnershipWithLease(t *testing.T) {
	for _, mode := range []string{"synchronous", "background"} {
		for _, vary := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/vary=%t", mode, vary), func(t *testing.T) {
				c := newTestCache(t, config.CacheConfig{
					MemoryMaxSize: 1 << 20, StaleWhileRevalidate: config.Duration(time.Minute),
				})
				group := testLease(t, 17)
				var version atomic.Int32
				version.Store(1)
				h, _ := scopedHandler(t, c, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					v := version.Load()
					if mode == "background" && v == 1 {
						w.Header().Set("Cache-Control", "public, max-age=0, stale-while-revalidate=60")
					} else {
						w.Header().Set("Cache-Control", "public, max-age=60")
					}
					if vary {
						w.Header().Set("Vary", "Accept-Language")
					}
					w.Header().Set("ETag", fmt.Sprintf(`"version-%d"`, v))
					_, _ = fmt.Fprintf(w, "version-%d", v)
				}))
				request := func(forceValidation bool) *httptest.ResponseRecorder {
					req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
					req.Header.Set("Accept-Language", "en")
					if forceValidation {
						req.Header.Set("Cache-Control", "no-cache")
					}
					rec := httptest.NewRecorder()
					h.ServeHTTP(rec, leased(req, group))
					return rec
				}
				if rec := request(false); rec.Body.String() != "version-1" {
					t.Fatal("initial response was not cached")
				}
				version.Store(2)
				if rec := request(mode == "synchronous"); mode == "synchronous" && rec.Body.String() != "version-2" {
					t.Fatal("synchronous validation did not fetch the replacement")
				}
				if mode == "background" {
					waitDrained(t, c)
					waitLeaseReleased(t, group)
				}
				rec := request(false)
				if rec.Header().Get("X-Cache") != stateHit || rec.Body.String() != "version-2" {
					t.Fatalf("replacement lost route ownership: state=%q body=%q", rec.Header().Get("X-Cache"), rec.Body.String())
				}
			})
		}
	}
}
