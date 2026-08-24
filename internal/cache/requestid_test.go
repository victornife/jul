// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/middleware"
)

// TestCacheHitDoesNotReplayAStaleRequestID is the regression test for #332: a
// cache hit must carry exactly one X-Request-ID, and it must be the current
// request's — not the one belonging to whichever request populated the entry.
// RequestID sets its header before `next` runs, exactly the shape #331's fix
// to cacheWriter.WriteHeader did not itself cover: a field set before the
// handler runs is still on the shared map when the snapshot is taken.
func TestCacheHitDoesNotReplayAStaleRequestID(t *testing.T) {
	c, _ := conformanceCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(time.Minute)})
	origin := &originStub{handler: func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = w.Write([]byte("v1"))
	}}
	h := middleware.RequestID()(c.Handler(origin))

	req1 := httptest.NewRequest(http.MethodGet, "http://x/a", nil)
	req1.Header.Set(middleware.HeaderRequestID, "req-AAAA-miss")
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, req1)
	if got := rec1.Header().Get("X-Cache"); got != stateMiss {
		t.Fatalf("first X-Cache = %q, want MISS", got)
	}
	if got := rec1.Header().Values(middleware.HeaderRequestID); len(got) != 1 || got[0] != "req-AAAA-miss" {
		t.Fatalf("first X-Request-ID = %v, want [req-AAAA-miss]", got)
	}

	req2 := httptest.NewRequest(http.MethodGet, "http://x/a", nil)
	req2.Header.Set(middleware.HeaderRequestID, "req-BBBB-hit")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if got := rec2.Header().Get("X-Cache"); got != stateHit {
		t.Fatalf("second X-Cache = %q, want HIT", got)
	}
	if got := rec2.Header().Values(middleware.HeaderRequestID); len(got) != 1 || got[0] != "req-BBBB-hit" {
		t.Fatalf("hit X-Request-ID = %v, want exactly [req-BBBB-hit], not the stale MISS id", got)
	}
	if got := rec2.Header().Values("X-Cache"); len(got) != 1 {
		t.Fatalf("X-Cache appeared %d times on the hit, want exactly 1", len(got))
	}
}

// TestCacheHitDoesNotReplayAStaleGeneratedRequestID repeats the proof with no
// client-supplied id, so RequestID generates one for each request.
func TestCacheHitDoesNotReplayAStaleGeneratedRequestID(t *testing.T) {
	c, _ := conformanceCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(time.Minute)})
	origin := &originStub{handler: func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = w.Write([]byte("v1"))
	}}
	h := middleware.RequestID()(c.Handler(origin))

	rec1 := get(t, h, "http://x/gen")
	if got := rec1.Header().Values(middleware.HeaderRequestID); len(got) != 1 || got[0] == "" {
		t.Fatalf("first X-Request-ID = %v, want exactly one generated id", got)
	}

	rec2 := get(t, h, "http://x/gen")
	if got := rec2.Header().Get("X-Cache"); got != stateHit {
		t.Fatalf("second X-Cache = %q, want HIT", got)
	}
	got := rec2.Header().Values(middleware.HeaderRequestID)
	if len(got) != 1 {
		t.Fatalf("hit X-Request-ID = %v, want exactly one value", got)
	}
	if got[0] == rec1.Header().Get(middleware.HeaderRequestID) {
		t.Fatal("hit replayed the MISS request's generated id instead of generating its own")
	}
}

// TestCacheStoresAHandlerOverriddenRequestIDWithTheHandlersValue proves the
// difference rule is per (name, value), not a blanket exclusion of the field: a
// handler that explicitly sets its own X-Request-ID overrides the outer layer's,
// and that value — not the outer layer's — is what gets stored and replayed.
func TestCacheStoresAHandlerOverriddenRequestIDWithTheHandlersValue(t *testing.T) {
	c, _ := conformanceCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(time.Minute)})
	origin := &originStub{handler: func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Header().Set(middleware.HeaderRequestID, "origin-chosen-id")
		_, _ = w.Write([]byte("v1"))
	}}
	h := middleware.RequestID()(c.Handler(origin))

	req1 := httptest.NewRequest(http.MethodGet, "http://x/override", nil)
	req1.Header.Set(middleware.HeaderRequestID, "client-supplied")
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, req1)
	if got := rec1.Header().Get(middleware.HeaderRequestID); got != "origin-chosen-id" {
		t.Fatalf("miss X-Request-ID = %q, want the handler's override", got)
	}

	rec2 := get(t, h, "http://x/override")
	if got := rec2.Header().Values(middleware.HeaderRequestID); len(got) != 1 || got[0] != "origin-chosen-id" {
		t.Fatalf("hit X-Request-ID = %v, want exactly [origin-chosen-id] (the handler's stored value)", got)
	}
}

// TestStoredEntryExcludesOuterLayerOnlyHeaders checks the stored Entry
// directly: neither X-Request-ID nor X-Cache, both set before the handler
// runs, ever reach the entry at all.
func TestStoredEntryExcludesOuterLayerOnlyHeaders(t *testing.T) {
	c, _ := conformanceCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(time.Minute)})
	origin := &originStub{handler: func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = w.Write([]byte("v1"))
	}}
	h := middleware.RequestID()(c.Handler(origin))

	r := httptest.NewRequest(http.MethodGet, "http://x/entry", nil)
	r.Header.Set(middleware.HeaderRequestID, "req-should-not-be-stored")
	h.ServeHTTP(httptest.NewRecorder(), r)

	e, ok := c.get(key(r))
	if !ok {
		t.Fatal("nothing was stored")
	}
	if got := e.Header.Values(middleware.HeaderRequestID); len(got) != 0 {
		t.Fatalf("stored entry carries X-Request-ID: %v", got)
	}
	if got := e.Header.Values("X-Cache"); len(got) != 0 {
		t.Fatalf("stored entry carries X-Cache: %v", got)
	}
}

// outerHeader is a stand-in for any layer outside the cache that pre-sets a
// response header before calling next — the shape RequestID has, generalized
// so the two tests below aren't tied to that one field.
func outerHeader(name, value string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set(name, value)
			next.ServeHTTP(w, r)
		})
	}
}

// TestHandlerSettingTheOuterLayersExactValueIsNotStored pins the documented
// residual of the multiset-difference rule (ADR 0018 §11): the guarantee #332
// requires is no-leak, not fidelity. A handler that Sets a field to the exact
// value the outer layer already set is indistinguishable, by value, from the
// outer layer's own contribution, so the difference is empty and the field is
// not stored. This is a known, accepted under-store — not a bug — because nothing
// cross-request leaks; #332 was scoped to the no-leak property only.
func TestHandlerSettingTheOuterLayersExactValueIsNotStored(t *testing.T) {
	c, _ := conformanceCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(time.Minute)})
	origin := &originStub{handler: func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Header().Set("X-Outer", "outer-value") // re-asserts the identical value
		_, _ = w.Write([]byte("v1"))
	}}
	h := outerHeader("X-Outer", "outer-value")(c.Handler(origin))

	r := httptest.NewRequest(http.MethodGet, "http://x/same-value", nil)
	h.ServeHTTP(httptest.NewRecorder(), r)

	e, ok := c.get(key(r))
	if !ok {
		t.Fatal("nothing was stored")
	}
	if got := e.Header.Values("X-Outer"); len(got) != 0 {
		t.Fatalf("stored entry unexpectedly carries X-Outer: %v (documents the accepted under-store, update this test if the mechanism changes)", got)
	}
}

// TestHandlerDeletingAnOuterHeaderIsResurrectedOnAHit pins the other documented
// residual: a handler that actively suppresses an outer-set field has no way to
// tombstone it, so a later hit still carries whatever the outer layer sets on
// that request — the outer layer runs on every request, hit or miss, and the
// cache never told it to do otherwise. Not a leak (the outer layer re-derives
// its own value every time, never a stored one) and not a regression; ADR 0018
// §11 requires this to be tested and understood, not silently rediscovered.
func TestHandlerDeletingAnOuterHeaderIsResurrectedOnAHit(t *testing.T) {
	c, _ := conformanceCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(time.Minute)})
	origin := &originStub{handler: func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Header().Del("X-Outer") // the origin representation must not carry it
		_, _ = w.Write([]byte("v1"))
	}}
	h := outerHeader("X-Outer", "outer-value")(c.Handler(origin))

	rec1 := get(t, h, "http://x/deleted")
	if got := rec1.Header().Get("X-Cache"); got != stateMiss {
		t.Fatalf("first X-Cache = %q, want MISS", got)
	}
	if got := rec1.Header().Get("X-Outer"); got != "" {
		t.Fatalf("miss X-Outer = %q, want empty (the handler deleted it)", got)
	}

	rec2 := get(t, h, "http://x/deleted")
	if got := rec2.Header().Get("X-Cache"); got != stateHit {
		t.Fatalf("second X-Cache = %q, want HIT", got)
	}
	if got := rec2.Header().Get("X-Outer"); got != "outer-value" {
		t.Fatalf("hit X-Outer = %q, want it resurrected as outer-value (the documented fidelity gap, not the current bug)", got)
	}
}
