// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"jul/internal/config"
)

func TestV2ResponseHookVaryBypassesCache(t *testing.T) {
	for _, scope := range []string{"location", "server"} {
		for _, mode := range []string{"headers", "body"} {
			t.Run(scope+"/"+mode, func(t *testing.T) {
				var calls atomic.Int32
				origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.Header().Set("Cache-Control", "public, max-age=60")
					body := "fixture-bob"
					if strings.Contains(r.Header.Get("Cookie"), "alice") {
						body = "fixture-alice"
					}
					_, _ = w.Write([]byte(body))
				}))
				defer origin.Close()
				h, _ := buildV2Handler(t, origin.URL, v2Plugin("vary-cookie"), func(c *config.Config) {
					c.Cache = config.CacheConfig{Enabled: true, MemoryMaxSize: 1 << 20}
					c.Servers[0].Locations[0].Cache = true
					if scope == "server" {
						c.Servers[0].Plugins = []string{"p"}
						c.Servers[0].Locations[0].Plugins = nil
					}
				})
				first := get(h, "X-Mode", mode, "Cookie", "session=fixture-alice")
				second := get(h, "X-Mode", mode, "Cookie", "session=fixture-bob")
				if first.Header().Get("X-Cache") != "BYPASS" || second.Header().Get("X-Cache") != "BYPASS" ||
					first.Body.String() != "fixture-alice" || second.Body.String() != "fixture-bob" ||
					!strings.Contains(second.Header().Get("Vary"), "Cookie") || calls.Load() != 2 {
					t.Fatal("response-hook Vary did not preserve independent origin responses")
				}
			})
		}
	}
}
