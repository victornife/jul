// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/config"
)

func TestIncompleteProxyVaryPolicyRejectsOnlyVariedResponses(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
	origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=60")
		if r.URL.Path == "/varied" {
			w.Header().Set("Vary", "Accept")
		}
		_, _ = w.Write([]byte("origin"))
	})
	_, scope := scopedHandler(t, c, origin)
	h, err := c.HandlerForProxyRoute(origin, scope, c.Policy(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ path, state string }{
		{"/varied", stateMiss}, {"/varied", stateMiss}, {"/plain", stateMiss}, {"/plain", stateHit},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example.test"+step.path, nil))
		if rec.Header().Get("X-Cache") != step.state {
			t.Fatalf("%s: state=%s, want %s", step.path, rec.Header().Get("X-Cache"), step.state)
		}
	}
}
