// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package app

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"jul/internal/config"
)

func TestV2RequestHookPresetVaryBypassesCache(t *testing.T) {
	var calls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Cache-Control", "public, max-age=60")
		_, _ = w.Write([]byte(r.Header.Get("X-Fixture-Identity")))
	}))
	defer origin.Close()
	h, _ := buildV2Handler(t, origin.URL, v2Plugin(""), func(c *config.Config) {
		c.Cache = config.CacheConfig{Enabled: true, MemoryMaxSize: 1 << 20, Lock: true}
		c.Servers[0].Locations[0].Cache = true
	})
	for _, identity := range []string{"fixture-a", "fixture-b"} {
		rec := get(h, "X-Op", "req-preset-vary", "X-Fixture-Identity", identity)
		if rec.Header().Get("X-Cache") != "BYPASS" || rec.Body.String() != identity ||
			rec.Header().Get("Vary") != "X-Fixture-Identity" {
			t.Fatalf("identity %s: body %q, X-Cache %q, Vary %q", identity, rec.Body.String(), rec.Header().Get("X-Cache"), rec.Header().Get("Vary"))
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("origin calls = %d, want 2", calls.Load())
	}
}
