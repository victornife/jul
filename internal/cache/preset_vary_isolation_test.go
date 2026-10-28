// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"

	"jul/internal/config"
)

func TestPresetVaryBypassesReuseAndFill(t *testing.T) {
	for _, disk := range []bool{false, true} {
		for _, ranged := range []bool{false, true} {
			t.Run(fmt.Sprintf("disk=%t/range=%t", disk, ranged), func(t *testing.T) {
				cfg := config.CacheConfig{MemoryMaxSize: 1 << 20, Lock: true, RangeEnabled: true, DecisionSamples: true}
				if disk {
					cfg = largeCacheCfg(t.TempDir(), 512, 1<<20, 8<<20)
					cfg.Lock, cfg.RangeEnabled, cfg.DecisionSamples = true, true, true
				}
				c := newTestCache(t, cfg)
				var calls atomic.Int32
				origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.Header().Set("Cache-Control", "public, max-age=60")
					_, _ = w.Write([]byte(r.Header.Get("X-Fixture-Identity")))
				}))
				defer origin.Close()
				target, err := url.Parse(origin.URL)
				if err != nil {
					t.Fatal(err)
				}
				proxy := httputil.NewSingleHostReverseProxy(target)
				_, scope := scopedHandler(t, c, proxy)
				cached := proxyScopedHandler(t, c, scope, proxy, "X-Forwarded-For")
				outer := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Add("Vary", "Accept")
					w.Header().Add("Vary", "x-fixture-identity")
					cached.ServeHTTP(w, r)
				})
				var wg sync.WaitGroup
				for _, identity := range []string{"fixture-a", "fixture-b"} {
					wg.Add(1)
					go func(identity string) {
						defer wg.Done()
						req := httptest.NewRequest(http.MethodGet, "http://example.com/item", nil)
						req.Header.Set("X-Fixture-Identity", identity)
						if ranged {
							req.Header.Set("Range", "bytes=0-8")
						}
						rec := httptest.NewRecorder()
						outer.ServeHTTP(rec, req)
						if rec.Body.String() != identity || rec.Header().Get("X-Cache") != stateBypass {
							t.Errorf("identity %s: body %q, X-Cache %q", identity, rec.Body.String(), rec.Header().Get("X-Cache"))
						}
					}(identity)
				}
				wg.Wait()
				if calls.Load() != 2 {
					t.Fatalf("origin calls = %d, want 2", calls.Load())
				}
				inspection, err := c.InspectScope(context.Background(), inspectSelector(t, SelectorExactURL, "/item"))
				if err != nil {
					t.Fatal(err)
				}
				for _, decision := range inspection.Decisions {
					if decision.Reason == "vary_preset_bypass" {
						return
					}
				}
				t.Fatal("preset Vary bypass decision was not recorded")
			})
		}
	}
}

func TestProxyCompressionIdentityStillCaches(t *testing.T) {
	for _, encoding := range []string{"", "identity"} {
		t.Run(fmt.Sprintf("encoding=%q", encoding), func(t *testing.T) {
			c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
			var calls atomic.Int32
			origin := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Cache-Control", "public, max-age=60")
				_, _ = w.Write([]byte("representation"))
			})
			_, scope := scopedHandler(t, c, origin)
			h := gzipMW(t)(proxyScopedHandler(t, c, scope, origin, "Accept-Encoding"))
			for i, want := range []string{stateMiss, stateHit} {
				req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
				if encoding != "" {
					req.Header.Set("Accept-Encoding", encoding)
				}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if rec.Header().Get("X-Cache") != want || rec.Body.String() != "representation" || rec.Header().Get("Vary") != "Accept-Encoding" {
					t.Fatalf("request %d: status=%q body=%q Vary=%q", i, rec.Header().Get("X-Cache"), rec.Body.String(), rec.Header().Get("Vary"))
				}
			}
			if calls.Load() != 1 {
				t.Fatalf("origin calls = %d, want 1", calls.Load())
			}
		})
	}
}
