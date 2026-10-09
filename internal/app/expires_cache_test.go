// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"io"
	"jul/internal/cache"
	"jul/internal/config"
	"jul/internal/observability"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestExpirationPolicyDoesNotChangeOriginCacheStorage(t *testing.T) {
	for _, originPolicy := range []string{"public, max-age=3600", "private, max-age=3600"} {
		t.Run(originPolicy, func(t *testing.T) {
			var calls atomic.Int32
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Cache-Control", originPolicy)
				w.Header().Set("Expires", "Thu, 01 Jan 1970 00:00:00 GMT")
				_, _ = w.Write([]byte("origin"))
			}))
			defer backend.Close()
			f, cleanup := minimalFactory(t)
			defer cleanup()
			c, err := cache.New(config.CacheConfig{Enabled: true, MemoryMaxSize: 1 << 20}, observability.NewLogger(io.Discard, "info", "text"))
			if err != nil {
				t.Fatal(err)
			}
			f.Cache = c
			duration := config.Duration(-time.Second)
			cfg := &config.Config{Cache: config.CacheConfig{Enabled: true, MemoryMaxSize: 1 << 20}, Servers: []config.ServerConfig{
				{Listen: "127.0.0.1:0", Locations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, ProxyPass: backend.URL, Cache: true, Expires: &duration}}},
			}}
			handlers, _, err := f.Build(context.Background(), cfg, false)
			if err != nil {
				t.Fatal(err)
			}
			request := func(h http.Handler) *httptest.ResponseRecorder {
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest("GET", "http://h/", nil))
				if rec.Header().Get("Cache-Control") != "no-cache" {
					t.Fatal(rec.Header())
				}
				exp, err := http.ParseTime(rec.Header().Get("Expires"))
				if err != nil || time.Since(exp) < 0 || time.Since(exp) > 3*time.Second {
					t.Fatal(exp, err)
				}
				return rec
			}
			request(handlers["127.0.0.1:0"])
			second := request(handlers["127.0.0.1:0"])
			if originPolicy == "public, max-age=3600" {
				if calls.Load() != 1 || second.Header().Get("X-Cache") != "HIT" {
					t.Fatal("public origin was not cached", calls.Load(), second.Header())
				}
			} else if calls.Load() != 2 || second.Header().Get("X-Cache") == "HIT" {
				t.Fatal("private origin was cached", calls.Load(), second.Header())
			}
			// Rebuild the policy around the same cache to prove policy headers were not
			// captured: removing expiration restores the original origin response.
			cfg.Servers[0].Locations[0].Expires = nil
			rebuilt, _, err := f.Build(context.Background(), cfg, false)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			rebuilt["127.0.0.1:0"].ServeHTTP(rec, httptest.NewRequest("GET", "http://h/", nil))
			if rec.Header().Get("Cache-Control") != originPolicy || rec.Header().Get("Expires") != "Thu, 01 Jan 1970 00:00:00 GMT" {
				t.Fatal("expiration leaked into storage", rec.Header())
			}
		})
	}
}
