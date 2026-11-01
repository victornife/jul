// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"fmt"
	"jul/internal/config"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Resource accounting must count the selected route ownership, not an unrelated
// GET sibling. Exercise both counting branches and real Vary membership/stubs.
func TestCutoverCountsHeadOwnedByAnotherRoute(t *testing.T) {
	for _, resources := range []int{16, 1025} {
		for _, targetGET := range []bool{false, true} {
			for _, vary := range []bool{false, true} {
				t.Run(fmt.Sprintf("resources_%d/get_targeted_%t/vary_%t", resources, targetGET, vary), func(t *testing.T) {
					c, err := New(config.CacheConfig{Enabled: true, MemoryMaxSize: 16 << 20, DefaultTTL: config.Duration(time.Hour)}, nil)
					if err != nil {
						t.Fatal(err)
					}
					getScope := "retained-get"
					if targetGET {
						getScope = "changed-head"
					}
					getRC := CompileRouteCacheConfig(getScope, false, nil)
					headRC := CompileRouteCacheConfig("changed-head", false, nil)
					c.CommitRoutePolicies([]RouteCacheConfig{getRC, headRC})
					origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Cache-Control", "public, max-age=3600")
						if vary {
							w.Header().Set("Vary", "Accept-Language")
						}
						fmt.Fprint(w, r.Method)
					})
					getHandler := c.HandlerForRoute(origin, getRC)
					headHandler := c.HandlerForRoute(origin, headRC)
					req := func(method string, i int) *httptest.ResponseRecorder {
						w := httptest.NewRecorder()
						r := httptest.NewRequest(method, fmt.Sprintf("http://example.com/articles/%d", i), nil)
						r.Header.Set("Accept-Language", "en")
						h := getHandler
						if method == "HEAD" {
							h = headHandler
						}
						h.ServeHTTP(w, r)
						return w
					}
					for i := 0; i < resources; i++ {
						for _, method := range []string{"GET", "HEAD"} {
							req(method, i)
							if w := req(method, i); w.Header().Get("X-Cache") != "HIT" {
								t.Fatalf("warm %s cache=%q", method, w.Header().Get("X-Cache"))
							}
						}
					}
					sel := NormalizedSelector{Kind: SelectorPrefix, Host: "example.com", Prefix: "/articles/", cutoverRouteScopes: []string{"changed-head"}}
					sel.digest = computeSelectorDigest(sel)
					result, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{})
					if err != nil {
						t.Fatal(err)
					}
					expectedReps := resources
					if targetGET {
						expectedReps *= 2
					}
					if result.InvalidatedResources != resources || result.InvalidatedRepresentations != expectedReps {
						t.Errorf("cutover misreports actual deletion: resources=%d representations=%d want=%d/%d", result.InvalidatedResources, result.InvalidatedRepresentations, resources, expectedReps)
					}
					for i := 0; i < resources; i++ {
						for _, method := range []string{"GET", "HEAD"} {
							want := "MISS"
							if method == "GET" && !targetGET {
								want = "HIT"
							}
							if w := req(method, i); w.Header().Get("X-Cache") != want {
								t.Fatalf("%s item%d cache=%q want=%q", method, i, w.Header().Get("X-Cache"), want)
							}
						}
					}
				})
			}
		}
	}
}
