// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func otherRouteScope(t *testing.T, c *Cache, original RouteScope, dimension string) RouteScope {
	t.Helper()
	var err error
	if dimension == "owner" {
		original.Owner, err = c.NewRouteOwner()
	} else {
		original.Compatibility, err = c.NewRouteCompatibility()
	}
	if err != nil {
		t.Fatal(err)
	}
	return original
}

func handlerWithScope(t *testing.T, c *Cache, scope RouteScope, origin http.Handler) http.Handler {
	t.Helper()
	h, err := c.HandlerForRoute(origin, scope, c.Policy())
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func awaitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("origin was not reached")
	}
}

func TestRouteScopeFillDoesNotWaitForIncompatibleLeader(t *testing.T) {
	for _, dimension := range []string{"owner", "compatibility"} {
		t.Run(dimension, func(t *testing.T) {
			c := newTestCache(t, lockCfg(30*time.Second))
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			a, aScope := scopedHandler(t, c, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				close(entered)
				<-release
				w.Header().Set("Cache-Control", "public, max-age=60")
				_, _ = w.Write([]byte("route-a"))
			}))
			b := handlerWithScope(t, c, otherRouteScope(t, c, aScope, dimension), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Cache-Control", "public, max-age=60")
				_, _ = w.Write([]byte("route-b"))
			}))
			req := func() *http.Request { return httptest.NewRequest(http.MethodGet, "http://example.test/item", nil) }
			leader, independent := httptest.NewRecorder(), httptest.NewRecorder()
			var wg sync.WaitGroup
			wg.Add(1)
			go func() { defer wg.Done(); a.ServeHTTP(leader, req()) }()
			awaitSignal(t, entered)
			done := make(chan struct{})
			wg.Add(1)
			go func() { defer wg.Done(); defer close(done); b.ServeHTTP(independent, req()) }()
			awaitSignal(t, done)
			if independent.Body.String() != "route-b" {
				t.Fatalf("independent fill got %q", independent.Body.String())
			}
			once.Do(func() { close(release) })
			wg.Wait()
			if leader.Body.String() != "route-a" {
				t.Fatalf("leader got %q", leader.Body.String())
			}
		})
	}
}

func TestRouteScopeValidationDoesNotWaitForIncompatibleLeader(t *testing.T) {
	for _, dimension := range []string{"owner", "compatibility"} {
		t.Run(dimension, func(t *testing.T) {
			c := newTestCache(t, lockCfg(5*time.Second))
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			a, aScope := scopedHandler(t, c, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", "public, max-age=60")
				w.Header().Set("ETag", `"route-a"`)
				if r.Header.Get("If-None-Match") != "" {
					close(entered)
					<-release
					w.WriteHeader(http.StatusNotModified)
					return
				}

				_, _ = w.Write([]byte("route-a"))
			}))
			b := handlerWithScope(t, c, otherRouteScope(t, c, aScope, dimension), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Cache-Control", "public, max-age=60")
				w.Header().Set("ETag", `"route-b"`)
				_, _ = w.Write([]byte("route-b"))
			}))
			req := func(validate bool) *http.Request {
				r := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
				if validate {
					r.Header.Set("Cache-Control", "no-cache")
				}
				return r
			}
			a.ServeHTTP(httptest.NewRecorder(), req(false))
			leader, independent := httptest.NewRecorder(), httptest.NewRecorder()
			var wg sync.WaitGroup
			wg.Add(1)
			go func() { defer wg.Done(); a.ServeHTTP(leader, req(true)) }()
			awaitSignal(t, entered)
			b.ServeHTTP(httptest.NewRecorder(), req(false))
			done := make(chan struct{})
			wg.Add(1)
			go func() { defer wg.Done(); defer close(done); b.ServeHTTP(independent, req(true)) }()
			awaitSignal(t, done)
			if independent.Body.String() != "route-b" {
				t.Fatalf("independent validation got %q", independent.Body.String())
			}
			once.Do(func() { close(release) })
			wg.Wait()
			if leader.Body.String() != "route-a" {
				t.Fatalf("leader got %q", leader.Body.String())
			}
		})
	}
}
