// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func BenchmarkCGIVaryRequest(b *testing.B) {
	for _, guarded := range []bool{false, true} {
		for _, headers := range []int{2, 32, 128} {
			b.Run(fmt.Sprintf("guard=%t/headers=%d", guarded, headers), func(b *testing.B) {
				c := benchCache()
				owner, err := c.NewRouteOwner()
				if err != nil {
					b.Fatal(err)
				}
				compat, err := c.NewRouteCompatibility()
				if err != nil {
					b.Fatal(err)
				}
				scope := RouteScope{Owner: owner, Compatibility: compat}
				origin := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Cache-Control", "public, max-age=300")
					w.Header().Set("Vary", "Accept-Language")
					_, _ = w.Write([]byte("fixture"))
				})
				var h http.Handler
				if guarded {
					h, err = c.HandlerForCGIRoute(origin, scope, c.Policy(), []string{"Client-Cert"}, []string{"X-Forwarded-"}, true)
				} else {
					h, err = c.HandlerForRoute(origin, scope, c.Policy())
				}
				if err != nil {
					b.Fatal(err)
				}
				req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
				req.Header.Set("Accept-Language", "en")
				for i := 1; i < headers; i++ {
					req.Header.Set(fmt.Sprintf("X-Fixture-%d", i), "value")
				}
				h.ServeHTTP(httptest.NewRecorder(), req)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					rec := httptest.NewRecorder()
					h.ServeHTTP(rec, req)
					if rec.Header().Get("X-Cache") != stateHit {
						b.Fatal("unexpected cache result")
					}
				}
			})
		}
	}
}
