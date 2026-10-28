// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

func Test304CannotValidateChangedVaryRequestFields(t *testing.T) {
	for _, field := range []string{"If-None-Match", "If-Modified-Since"} {
		for _, omitVary := range []bool{false, true} {
			for _, failure := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/omit=%t/failure=%t", field, omitVary, failure), func(t *testing.T) {
					c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
					var calls int
					origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls++
						w.Header().Set("Cache-Control", "public, no-cache, max-age=60, stale-if-error=120")
						if field == "If-None-Match" {
							w.Header().Set("ETag", `"fixture"`)
						} else {
							w.Header().Set("Last-Modified", "Wed, 21 Oct 2015 07:28:00 GMT")
						}
						if r.Header.Get(field) != "" {
							if !omitVary {
								w.Header().Set("Vary", field)
							}
							w.WriteHeader(http.StatusNotModified)
							return
						}
						w.Header().Set("Vary", field)
						if calls == 1 {
							_, _ = w.Write([]byte("old"))
						} else if failure {
							w.WriteHeader(http.StatusServiceUnavailable)
							_, _ = w.Write([]byte("unavailable"))
						} else {
							_, _ = w.Write([]byte("new"))
						}
					})
					_, scope := scopedHandler(t, c, origin)
					h := cgiScopedHandler(t, c, scope, origin)
					req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
					h.ServeHTTP(httptest.NewRecorder(), req)
					rec := httptest.NewRecorder()
					h.ServeHTTP(rec, req)
					want := "new"
					if failure {
						want = "unavailable"
					}
					if calls != 3 || rec.Body.String() != want || rec.Header().Get("X-Cache") == stateStale {
						t.Fatalf("unsafe 304: calls=%d body=%q state=%q", calls, rec.Body.String(), rec.Header().Get("X-Cache"))
					}
				})
			}
		}
	}
}

func TestBackground304CannotValidateChangedVaryRequest(t *testing.T) {
	c, clock := conformanceCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
	group := testLease(t, 41)
	var full atomic.Int32
	origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=1, stale-while-revalidate=60")
		w.Header().Set("ETag", `"fixture"`)
		w.Header().Set("Vary", "If-None-Match")
		if r.Header.Get("If-None-Match") != "" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if full.Add(1) == 1 {
			_, _ = w.Write([]byte("old"))
		} else {
			_, _ = w.Write([]byte("new"))
		}
	})
	_, scope := scopedHandler(t, c, origin)
	h := cgiScopedHandler(t, c, scope, origin)
	req := leased(httptest.NewRequest(http.MethodGet, "http://example.test/item", nil), group)
	h.ServeHTTP(httptest.NewRecorder(), req)
	clock.advance(2 * time.Second)
	stale := httptest.NewRecorder()
	h.ServeHTTP(stale, req)
	if stale.Body.String() != "old" || stale.Header().Get("X-Cache") != stateStale {
		t.Fatal("already admitted stale response did not complete")
	}
	waitDrained(t, c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Body.String() != "new" || rec.Header().Get("X-Cache") != stateMiss {
		t.Fatal("background 304 validated a different request representation")
	}
}

func TestValidationVaryRequestComparisonIncludesLegacyMetadata(t *testing.T) {
	original := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
	modified := original.Clone(original.Context())
	modified.Header.Set("X-Fixture", "changed")
	for _, e := range []*Entry{
		{Vary: []string{"X-Fixture"}},
		{Header: http.Header{"Vary": {"X-Fixture"}}},
		{VaryValues: map[string]string{"X-Fixture": ""}},
	} {
		if varyCompatibleWithValidation(e, nil, modified, original) {
			t.Fatal("modified varied header was accepted")
		}
	}
	if varyCompatibleWithValidation(&Entry{}, http.Header{"Vary": {"X-Fixture"}}, modified, original) {
		t.Fatal("304 varied header was not checked")
	}
	if !varyCompatibleWithValidation(&Entry{Vary: []string{"Accept"}}, nil, modified, original) {
		t.Fatal("unaffected varied header was rejected")
	}
}

func TestCGIConditionalVaryAliasCannotReuseOrServe(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNotModified} {
		t.Run(fmt.Sprintf("status=%d", status), func(t *testing.T) {
			c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
			var calls int
			origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Cache-Control", "public, no-cache, max-age=60")
				w.Header().Set("ETag", `"fixture"`)
				w.Header().Set("Vary", "If_None_Match")
				if r.Header.Get("If-None-Match") != "" {
					w.WriteHeader(status)
					if status == http.StatusOK {
						_, _ = w.Write([]byte("conditional"))
					}
				} else if calls == 1 {
					_, _ = w.Write([]byte("old"))
				} else {
					_, _ = w.Write([]byte("new"))
				}
			})
			_, scope := scopedHandler(t, c, origin)
			h := cgiScopedHandler(t, c, scope, origin)
			req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
			h.ServeHTTP(httptest.NewRecorder(), req)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if calls != 3 || rec.Body.String() != "new" {
				t.Fatalf("CGI alias matched a changed conditional request: calls=%d body=%q", calls, rec.Body.String())
			}
		})
	}
}

func TestCGIVaryAliasWhenInternalRequestStripsHeader(t *testing.T) {
	original := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
	original.Header.Set("If-Range", `"fixture"`)
	original = withProxyVaryPolicy(original, newCGIVaryPolicy(nil, nil))
	modified := original.Clone(original.Context())
	modified.Header.Del("If-Range")
	if varyCompatibleWithModifiedRequest(http.Header{"Vary": {"If_Range"}}, modified, original) {
		t.Fatal("stripped CGI-aliasing request field was accepted")
	}
}

func Test304ChangedVaryRequestRejectsDiskVariant(t *testing.T) {
	cfg := largeCacheCfg(t.TempDir(), 512, 1<<20, 8<<20)
	c := newTestCache(t, cfg)
	var calls int
	origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Cache-Control", "public, no-cache, max-age=60")
		w.Header().Set("ETag", `"fixture"`)
		w.Header().Set("Vary", "If-None-Match")
		if r.Header.Get("If-None-Match") != "" {
			w.WriteHeader(http.StatusNotModified)
		} else if calls == 1 {
			_, _ = w.Write([]byte(strings.Repeat("old", 1024)))
		} else {
			_, _ = w.Write([]byte(strings.Repeat("new", 1024)))
		}
	})
	_, scope := scopedHandler(t, c, origin)
	h := cgiScopedHandler(t, c, scope, origin)
	req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	variant := variantKey(key(req), []string{"If-None-Match"}, req)
	if _, ok := c.disk.get(variant); !ok {
		t.Fatal("fixture variant was not stored on disk")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if calls != 3 || !strings.HasPrefix(rec.Body.String(), "new") {
		t.Fatalf("disk variant reused after mismatched 304: calls=%d", calls)
	}
}
