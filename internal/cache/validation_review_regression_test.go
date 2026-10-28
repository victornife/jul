// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"jul/internal/config"
	"jul/internal/tracing"
)

func TestTagPurgeSeesAdmittedValidationBeforeOrigin(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, TagHeaders: []string{"Cache-Tag"}})
	origin := func(tag string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=60")
			w.Header().Set("Cache-Tag", tag)
			w.Header().Set("ETag", `"fixture"`)
			if r.Header.Get("If-None-Match") != "" {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			_, _ = w.Write([]byte(tag))
		})
	}
	a, _ := scopedHandler(t, c, origin("tag-a"))
	b, bScope := scopedHandler(t, c, origin("tag-b"))
	req := func(validate bool) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
		if validate {
			r.Header.Set("Cache-Control", "no-cache")
		}
		return r
	}
	a.ServeHTTP(httptest.NewRecorder(), req(false))
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	previous := tracing.Active()
	tracing.Set(admissionBarrierTracer{entered: entered, release: release})
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		tracing.Set(previous)
	})
	done := make(chan struct{})
	go func() { defer close(done); a.ServeHTTP(httptest.NewRecorder(), req(true)) }()
	awaitSignal(t, entered)
	b.ServeHTTP(httptest.NewRecorder(), req(false))
	sel, err := ParseSelector(Selector{Kind: SelectorTag, Host: "example.test", Tags: []string{"tag-a"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if preview := c.PreviewScope(sel, PreviewOptions{}); preview.InflightFillsMatched != 1 {
		t.Fatalf("preview matched %d validations, want 1", preview.InflightFillsMatched)
	}
	if result, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{}); err != nil || result.State != "succeeded" {
		t.Fatalf("purge state %q: %v", result.State, err)
	}
	if _, _, found := c.lookupWithOwner(key(req(false)), req(false), &bScope); found {
		t.Fatal("tag purge missed the replacement of the admitted validation")
	}
	releaseOnce.Do(func() { close(release) })
	awaitSignal(t, done)
}

func TestTagPreviewBroadnessIncludesInflightOnlyResources(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, TagHeaders: []string{"Cache-Tag"}})
	for i := 0; i < BroadScopeResourceThr; i++ {
		k := revalidateKey{key: keyFor(http.MethodGet, "example.test", fmt.Sprintf("/item/%d", i))}
		c.beginRevalidate(k, []string{"tag-a"})
	}
	sel, err := ParseSelector(Selector{Kind: SelectorTag, Host: "example.test", Tags: []string{"tag-a"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	preview := c.PreviewScope(sel, PreviewOptions{})
	if !preview.BroadScope || preview.MatchedResources != 0 || preview.InflightFillsMatched != BroadScopeResourceThr {
		t.Fatalf("unexpected preview: %+v", preview)
	}
	if _, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{RequireBroadConfirm: true}); err != ErrBroadConfirmRequired {
		t.Fatalf("unconfirmed broad purge: %v", err)
	}
}

func TestSensitiveVaryErrorCannotReuseInvalidatedStale(t *testing.T) {
	for _, ranged := range []bool{false, true} {
		t.Run(fmt.Sprintf("range=%t", ranged), func(t *testing.T) {
			c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, RangeEnabled: true})
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			var fullFetches atomic.Int32
			h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				if r.Header.Get("If-None-Match") != "" {
					close(entered)
					<-release
					w.Header().Set("Vary", "Cookie")
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				w.Header().Set("Cache-Control", "public, no-cache, max-age=60, stale-if-error=60")
				w.Header().Set("ETag", `"fixture"`)
				if fullFetches.Add(1) == 1 {
					_, _ = w.Write([]byte("old-body"))
				} else {
					_, _ = w.Write([]byte("new-body"))
				}
			}))
			url := "http://example.test/item"
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, url, nil))
			req := httptest.NewRequest(http.MethodGet, url, nil)
			if ranged {
				req.Header.Set("Range", "bytes=0-2")
			}
			rec := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); h.ServeHTTP(rec, req) }()
			awaitSignal(t, entered)
			deleted := httptest.NewRecorder()
			h.ServeHTTP(deleted, httptest.NewRequest(http.MethodDelete, url, nil))
			if deleted.Code != http.StatusNoContent {
				t.Fatalf("DELETE status = %d", deleted.Code)
			}
			once.Do(func() { close(release) })
			awaitSignal(t, done)
			if rec.Body.String() != "new-body" || rec.Header().Get("X-Cache") == stateStale {
				t.Fatalf("invalidated stale response reused: body=%q X-Cache=%q", rec.Body.String(), rec.Header().Get("X-Cache"))
			}
		})
	}
}
