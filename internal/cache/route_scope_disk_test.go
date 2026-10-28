// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRouteOwnerDiskVaryIsolation(t *testing.T) {
	cfg := largeCacheCfg(t.TempDir(), 1024, 1<<20, 8<<20)
	c := newTestCache(t, cfg)
	bodyA, bodyB := strings.Repeat("a", 4096), strings.Repeat("b", 4096)
	origin := func(body string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=60")
			w.Header().Set("Vary", "Accept-Language")
			_, _ = w.Write([]byte(body))
		})
	}
	a, _ := scopedHandler(t, c, origin(bodyA))
	b, _ := scopedHandler(t, c, origin(bodyB))
	for _, step := range []struct {
		h           http.Handler
		state, body string
	}{
		{a, stateMiss, bodyA},
		{a, stateHit, bodyA},
		{b, stateMiss, bodyB},
		{b, stateHit, bodyB},
		{a, stateMiss, bodyA},
	} {
		req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
		req.Header.Set("Accept-Language", "en")
		rec := httptest.NewRecorder()
		step.h.ServeHTTP(rec, req)
		if rec.Header().Get("X-Cache") != step.state || rec.Body.String() != step.body {
			t.Fatal("disk Vary representation crossed route owners")
		}
	}
}
