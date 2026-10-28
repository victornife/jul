// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func BenchmarkRouteOwnedCacheHit(b *testing.B) {
	c := benchCache()
	owner, err := c.NewRouteOwner()
	if err != nil {
		b.Fatal(err)
	}

	compatibility, err := c.NewRouteCompatibility()
	if err != nil {
		b.Fatal(err)
	}
	h, err := c.HandlerForRoute(benchNext, RouteScope{Owner: owner, Compatibility: compatibility}, c.Policy())
	if err != nil {
		b.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://example.com/page", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Header().Get("X-Cache") != stateHit {
			b.Fatal("expected HIT")
		}
	}
}

func BenchmarkRouteOwnedCacheMiss(b *testing.B) {
	c := benchCache()
	owner, err := c.NewRouteOwner()
	if err != nil {
		b.Fatal(err)
	}
	compatibility, err := c.NewRouteCompatibility()
	if err != nil {
		b.Fatal(err)
	}
	h, err := c.HandlerForRoute(benchNext, RouteScope{Owner: owner, Compatibility: compatibility}, c.Policy())
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://example.com/miss?id="+strconv.Itoa(i), nil)
		h.ServeHTTP(rec, req)
		if rec.Header().Get("X-Cache") != stateMiss {
			b.Fatal("expected MISS")
		}
	}
}
