// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

func TestConcurrentVariantRetirementsRemainIndependent(t *testing.T) {
	for _, mode := range []string{"background", "synchronous"} {
		for _, order := range []string{"403-first", "304-first", "fence-cap"} {
			for _, disk := range []bool{false, true} {
				t.Run(mode+"/"+order+"/disk="+strconv.FormatBool(disk), func(t *testing.T) {
					cfg := config.CacheConfig{MemoryMaxSize: 1 << 20}
					if disk {
						cfg.DiskPath, cfg.DiskMaxSize = t.TempDir(), 8<<20
					}
					c, clock := conformanceCache(t, cfg)
					group := testLease(t, 63)
					enStarted, frStarted := make(chan struct{}), make(chan struct{})
					enRelease, frRelease := make(chan struct{}), make(chan struct{})
					var validating atomic.Bool
					h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						language := r.Header.Get("Accept-Language")
						if r.Header.Get("If-None-Match") != "" {
							if language == "en" {
								close(enStarted)
								<-enRelease
								w.WriteHeader(http.StatusForbidden)
							} else {
								close(frStarted)
								<-frRelease
								w.Header().Set("Cache-Control", "no-store")
								w.WriteHeader(http.StatusNotModified)
							}
							return
						}
						if validating.Load() {
							w.Header().Set("Cache-Control", "no-store")
							w.WriteHeader(http.StatusForbidden)
							return
						}
						w.Header().Set("Cache-Control", "public, max-age=1, stale-while-revalidate=60")
						w.Header().Set("ETag", `"fixture"`)
						w.Header().Set("Vary", "Accept-Language")
						_, _ = w.Write([]byte(language))
					}))
					request := func(language string) *http.Request {
						r := leased(httptest.NewRequest(http.MethodGet, "http://example.test/item", nil), group)
						r.Header.Set("Accept-Language", language)
						return r
					}
					en, fr := request("en"), request("fr")
					h.ServeHTTP(httptest.NewRecorder(), en)
					h.ServeHTTP(httptest.NewRecorder(), fr)
					base, ok := c.get(key(en))
					if !ok || !base.IsVaryStub {
						t.Fatal("variant stub missing")
					}
					enKey, frKey := variantKey(key(en), base.Vary, en), variantKey(key(fr), base.Vary, fr)
					clock.advance(2 * time.Second)
					validating.Store(true)
					done := make(chan struct{}, 2)
					for _, r := range []*http.Request{en, fr} {
						r := r.Clone(r.Context())
						if mode == "synchronous" {
							r.Header.Set("Cache-Control", "no-cache")
						}
						go func() {
							h.ServeHTTP(httptest.NewRecorder(), r)
							done <- struct{}{}
						}()
					}
					<-enStarted
					<-frStarted
					firstKey := enKey
					if order == "304-first" {
						firstKey = frKey
						close(frRelease)
					} else {
						close(enRelease)
					}
					deadline := time.Now().Add(5 * time.Second)
					for {
						if _, ok := c.get(firstKey); !ok {
							break
						}
						if time.Now().After(deadline) {
							t.Fatal("first variant was not retired")
						}
						time.Sleep(time.Millisecond)
					}
					if order == "fence-cap" {
						c.genMu.Lock()
						for i := 0; i <= MaxActiveFences; i++ {
							c.fences = append(c.fences, selectorFence{
								gen:         c.genSeq.Add(1),
								retiredBase: sha256.Sum256([]byte(strconv.Itoa(i))),
								retirement:  true,
							})
						}
						c.enforceFenceCapLocked()
						c.genMu.Unlock()
					}
					if order == "304-first" {
						close(enRelease)
					} else {
						close(frRelease)
					}
					<-done
					<-done
					waitDrained(t, c)
					waitLeaseReleased(t, group)
					for _, target := range []string{enKey, frKey} {
						if e, ok := c.get(target); ok {
							t.Fatalf("denied variant remains: %q", e.Body)
						}
					}
					if disk {
						restarted := newTestCache(t, cfg)
						for _, target := range []string{enKey, frKey} {
							if e, ok := restarted.get(target); ok {
								t.Fatalf("denied variant survived restart: %q", e.Body)
							}
						}
					}
				})
			}
		}
	}
}
