// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"jul/internal/config"
)

func TestRouteHandlerBypassesUntilScalarPolicyCommit(t *testing.T) {
	initial := config.CacheConfig{MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Hour)}
	c := newTestCache(t, initial)
	origin := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("origin"))
	})
	old, scope := scopedHandler(t, c, origin)
	req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
	old.ServeHTTP(httptest.NewRecorder(), req)

	next := initial
	next.DefaultTTL = config.Duration(time.Second)
	compatibility, err := c.NewRouteCompatibility()
	if err != nil {
		t.Fatal(err)
	}
	scope.Compatibility = compatibility
	current, err := c.HandlerForRoute(origin, scope, PolicyForConfig(next))
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	current.ServeHTTP(rec, req)
	if rec.Header().Get("X-Cache") != stateBypass {
		t.Fatal("candidate handler used an older cache scalar policy")
	}
	c.PrepareCacheUpdate(next).Commit()
	rec = httptest.NewRecorder()
	current.ServeHTTP(rec, req)
	if rec.Header().Get("X-Cache") != stateMiss {
		t.Fatal("current handler reused an entry from the prior policy")
	}
	rec = httptest.NewRecorder()
	old.ServeHTTP(rec, req)
	if rec.Header().Get("X-Cache") != stateBypass {
		t.Fatal("retired handler used a newer cache scalar policy")
	}
}
