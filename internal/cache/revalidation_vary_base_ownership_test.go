// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"jul/internal/config"
)

func TestOldValidationDoesNotInvalidateNewVaryBaseValidator(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			cfg := config.CacheConfig{MemoryMaxSize: 1 << 20}
			if disk {
				cfg.DiskPath = t.TempDir()
				cfg.DiskMaxSize = 8 << 20
			}
			c, _ := conformanceCache(t, cfg)
			aReady, aRelease, aDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
			bReady, bRelease, bDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseA, releaseB sync.Once
			t.Cleanup(func() {
				releaseA.Do(func() { close(aRelease) })
				releaseB.Do(func() { close(bRelease) })
			})

			origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				id := r.Header.Get("X-Test-Request")
				if r.Header.Get("If-None-Match") != "" {
					switch id {
					case "A":
						close(aReady)
						<-aRelease
						w.Header().Set("Cache-Control", "public, max-age=60")
						w.WriteHeader(http.StatusNotModified)
						return
					case "B":
						close(bReady)
						<-bRelease
						w.WriteHeader(http.StatusForbidden)
						return
					}
				}
				if id == "A" {
					w.Header().Set("Cache-Control", "no-store")
					_, _ = w.Write([]byte("fallback"))
					return
				}
				w.Header().Set("Cache-Control", "public, max-age=60")
				w.Header().Set("ETag", `"fixture"`)
				w.Header().Set("Vary", "Accept-Language")
				_, _ = w.Write([]byte(r.Header.Get("Accept-Language")))
			})
			h := c.Handler(origin)
			request := func(language, id string, validate bool) *http.Request {
				r := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
				r.Header.Set("Accept-Language", language)
				r.Header.Set("X-Test-Request", id)
				if validate {
					r.Header.Set("Cache-Control", "no-cache")
				}
				return r
			}
			en, fr := request("en", "", false), request("fr", "", false)
			h.ServeHTTP(httptest.NewRecorder(), en)
			_, effective, oldBaseGen, oldEntryGen, ok := c.lookupWithOwnerAndGen(key(en), en, nil)
			if !ok {
				t.Fatal("initial variant missing")
			}

			go func() {
				defer close(aDone)
				h.ServeHTTP(httptest.NewRecorder(), request("en", "A", true))
			}()
			awaitSignal(t, aReady)
			h.ServeHTTP(httptest.NewRecorder(), fr)
			_, newEffective, newBaseGen, newEntryGen, ok := c.lookupWithOwnerAndGen(key(en), en, nil)
			if !ok || newEffective != effective || newBaseGen == oldBaseGen || newEntryGen != oldEntryGen {
				t.Fatalf("sibling did not replace only the Vary stub: found=%t base=%d/%d entry=%d/%d",
					ok, oldBaseGen, newBaseGen, oldEntryGen, newEntryGen)
			}

			go func() {
				defer close(bDone)
				h.ServeHTTP(httptest.NewRecorder(), request("en", "B", true))
			}()
			awaitSignal(t, bReady)
			releaseA.Do(func() { close(aRelease) })
			awaitSignal(t, aDone)

			c.fetchAndStore(httptest.NewRecorder(), en, origin, c.clock())
			releaseB.Do(func() { close(bRelease) })
			awaitSignal(t, bDone)
			if e, _, ok := c.lookup(key(en), en); ok {
				t.Errorf("newer validator's denial left the variant reusable: %q", e.Body)
			}
			if e, _, ok := c.lookup(key(fr), fr); !ok || string(e.Body) != "fr" {
				t.Error("retiring the denied variant removed its unaffected sibling")
			}
			if disk {
				restarted := newTestCache(t, cfg)
				if e, _, ok := restarted.lookup(key(en), en); ok {
					t.Errorf("denied variant survived restart: %q", e.Body)
				}
				if e, _, ok := restarted.lookup(key(fr), fr); !ok || string(e.Body) != "fr" {
					t.Error("unaffected sibling missing after restart")
				}
			}
		})
	}
}
