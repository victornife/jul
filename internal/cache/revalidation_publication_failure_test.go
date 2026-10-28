// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestFailedStreamValidationRetiresOldRepresentation(t *testing.T) {
	for _, mode := range []string{"synchronous", "background"} {
		for _, varied := range []bool{false, true} {
			t.Run(mode+"/varied="+strconv.FormatBool(varied), func(t *testing.T) {
				cfg := largeCacheCfg(t.TempDir(), 512, 1<<20, 8<<20)
				c, clock := conformanceCache(t, cfg)
				group := testLease(t, 70)
				h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Cache-Control", "public, max-age=1, stale-while-revalidate=60")
					w.Header().Set("ETag", `"fixture"`)
					if varied {
						w.Header().Set("Vary", "Accept-Language")
					}
					_, _ = w.Write([]byte(strings.Repeat("x", 4096)))
				}))
				req := leased(httptest.NewRequest(http.MethodGet, "http://example.test/item", nil), group)
				req.Header.Set("Accept-Language", "en")
				h.ServeHTTP(httptest.NewRecorder(), req)
				if _, _, ok := c.lookup(key(req), req); !ok {
					t.Fatal("old disk representation was not stored")
				}
				c.disk.testPublishFault = "append"
				if mode == "synchronous" {
					req = req.Clone(req.Context())
					req.Header.Set("Cache-Control", "no-cache")
				} else {
					clock.advance(2 * time.Second)
				}
				h.ServeHTTP(httptest.NewRecorder(), req)
				waitLeaseReleased(t, group)
				if _, _, ok := c.lookup(key(req), req); ok {
					t.Fatal("failed replacement left old representation reusable")
				}
				restarted := newTestCache(t, cfg)
				if _, _, ok := restarted.lookup(key(req), req); ok {
					t.Fatal("failed replacement left old disk representation reusable after restart")
				}
			})
		}
	}
}
