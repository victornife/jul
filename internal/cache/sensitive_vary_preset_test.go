// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/config"
)

func TestSensitiveVaryPresetCannotHideOriginVary(t *testing.T) {
	for _, field := range []string{"Cookie", "Authorization"} {
		for _, mode := range []string{"explicit", "implicit-write", "no-write"} {
			t.Run(field+"/"+mode, func(t *testing.T) {
				c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, DecisionSamples: true})
				t.Cleanup(func() { c.setDecisionSampling(false) })
				var calls int
				cached := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					w.Header().Set("Cache-Control", "public, max-age=60")
					w.Header().Set("Vary", field)
					if mode == "explicit" {
						w.WriteHeader(http.StatusOK)
					}
					if mode != "no-write" {
						body := "fixture-bob"
						if strings.Contains(r.Header.Get(field), "alice") {
							body = "fixture-alice"
						}
						_, _ = w.Write([]byte(body))
					}
				}))
				outer := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Vary", field)
					cached.ServeHTTP(w, r)
				})
				for _, identity := range []string{"fixture-alice", "fixture-bob"} {
					req := httptest.NewRequest(http.MethodGet, "http://example.com/item", nil)
					req.Header.Set(field, identity)
					rec := httptest.NewRecorder()
					outer.ServeHTTP(rec, req)
					if rec.Header().Get("X-Cache") != stateBypass || mode != "no-write" && rec.Body.String() != identity {
						t.Fatal("response was reused across requests despite sensitive Vary")
					}
				}
				if calls != 2 {
					t.Fatalf("origin calls = %d, want 2", calls)
				}
				inspection, err := c.InspectScope(context.Background(), inspectSelector(t, SelectorExactURL, "/item"))
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, decision := range inspection.Decisions {
					found = found || decision.Reason == "vary_sensitive_rejected"
				}
				if !found {
					t.Fatal("sensitive Vary rejection was not recorded")
				}
			})
		}
	}
}
