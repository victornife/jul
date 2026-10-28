// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func BenchmarkCacheUncacheableFillAdmission(b *testing.B) {
	for _, locked := range []bool{false, true} {
		b.Run(fmt.Sprintf("lock=%t", locked), func(b *testing.B) {
			cfg := lockCfg(5 * time.Second)
			cfg.Lock = locked
			c, err := New(cfg, nil)
			if err != nil {
				b.Fatal(err)
			}
			origin := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Cache-Control", "no-store")
				_, _ = w.Write([]byte("body"))
			})
			h := c.Handler(origin)
			req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				rr := httptest.NewRecorder()
				h.ServeHTTP(rr, req)
				if rr.Header().Get("X-Cache") != stateMiss {
					b.Fatal("expected a cold miss")
				}
			}
		})
	}
}
