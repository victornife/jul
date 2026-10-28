// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func BenchmarkProxyVaryRequest(b *testing.B) {
	for _, guarded := range []bool{false, true} {
		for _, hit := range []bool{false, true} {
			for _, cookieBytes := range []int{0, 512, 8192} {
				b.Run(fmt.Sprintf("guard=%t/hit=%t/cookie=%d", guarded, hit, cookieBytes), func(b *testing.B) {
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
						if hit {
							w.Header().Set("Cache-Control", "public, max-age=300")
							w.Header().Set("Vary", "Accept-Language")
						} else {
							w.Header().Set("Cache-Control", "no-store")
						}
						_, _ = w.Write([]byte("fixture"))
					})
					var h http.Handler
					if guarded {
						h, err = c.HandlerForProxyRoute(origin, scope, c.Policy(), []string{"X-Forwarded-For", "X-Identity"}, true)
					} else {
						h, err = c.HandlerForRoute(origin, scope, c.Policy())
					}
					if err != nil {
						b.Fatal(err)
					}
					req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
					req.Header.Set("Accept-Language", "en")
					if cookieBytes > 0 {
						req.Header.Set("Cookie", strings.Repeat("x", cookieBytes))
					}
					if hit {
						h.ServeHTTP(httptest.NewRecorder(), req)
					}
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						rec := httptest.NewRecorder()
						h.ServeHTTP(rec, req)
						if hit && rec.Header().Get("X-Cache") != stateHit || !hit && rec.Header().Get("X-Cache") != stateMiss {
							b.Fatal("unexpected cache result")
						}
					}
				})
			}
		}
	}
}

func BenchmarkProxyVaryPolicyConstruction(b *testing.B) {
	for _, n := range []int{24, 128, 257} {
		names := make([]string, n)
		for i := range names {
			names[i] = fmt.Sprintf("X-Fixture-%d", i)
		}
		b.Run(fmt.Sprintf("headers=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = newProxyVaryPolicy(names)
			}
		})
	}
}
