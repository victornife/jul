// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"testing"

	"jul/internal/config"
)

// TestAuthDependencyPoolResolution pins how a forward-auth or JWKS URL becomes
// a pool. The interesting cases are not the happy path but the two that are
// easy to get silently wrong: a host that names a configured upstream must be
// load-balanced rather than dialled literally, and a URL with no port must
// still produce a backend address, because a backend always has one even when a
// URL does not.
func TestAuthDependencyPoolResolution(t *testing.T) {
	upstreams := map[string]config.UpstreamConfig{
		"authpool": {
			Name:     "authpool",
			Strategy: "round_robin",
			Servers: []config.UpstreamServer{
				{Address: "10.0.0.1:8080", Weight: 1},
				{Address: "10.0.0.2:8080", Weight: 1},
			},
			MaxFails: 3,
		},
	}
	var f HandlerFactory

	for _, tc := range []struct {
		name      string
		url       string
		wantNil   bool
		wantAddrs []string
	}{
		{
			name:    "an unconfigured dependency has no pool",
			url:     "",
			wantNil: true,
		},
		{
			name:      "a host naming an upstream is load-balanced across its servers",
			url:       "http://authpool/verify",
			wantAddrs: []string{"10.0.0.1:8080", "10.0.0.2:8080"},
		},
		{
			name:      "an explicit port is preserved",
			url:       "http://auth.internal:9000/verify",
			wantAddrs: []string{"auth.internal:9000"},
		},
		{
			name:      "http with no port defaults to 80",
			url:       "http://auth.internal/verify",
			wantAddrs: []string{"auth.internal:80"},
		},
		{
			name:      "https with no port defaults to 443",
			url:       "https://issuer.example/.well-known/jwks.json",
			wantAddrs: []string{"issuer.example:443"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, err := f.authDependencyPool(context.Background(), tc.url, upstreams)
			if err != nil {
				t.Fatalf("authDependencyPool: %v", err)
			}
			if tc.wantNil {
				if pool != nil {
					t.Fatal("an empty URL produced a pool")
				}
				return
			}
			if pool == nil {
				t.Fatal("expected a pool")
			}
			t.Cleanup(pool.Close)

			var got []string
			for _, b := range pool.Backends() {
				got = append(got, b.Address)
			}
			if len(got) != len(tc.wantAddrs) {
				t.Fatalf("backends = %v, want %v", got, tc.wantAddrs)
			}
			for i := range got {
				if got[i] != tc.wantAddrs[i] {
					t.Fatalf("backends = %v, want %v", got, tc.wantAddrs)
				}
			}
		})
	}
}

// TestAuthDependencyPoolRejectsHostlessURL pins that a URL Jul cannot turn into
// a backend fails the reload rather than producing a pool that can never be
// dialled.
func TestAuthDependencyPoolRejectsHostlessURL(t *testing.T) {
	var f HandlerFactory
	if _, err := f.authDependencyPool(context.Background(), "/verify", nil); err == nil {
		t.Fatal("a URL with no host was accepted")
	}
	if _, err := f.authDependencyPool(context.Background(), "http://%%%/verify", nil); err == nil {
		t.Fatal("an unparseable URL was accepted")
	}
}

// TestBuildWiresAuthDependencyPools pins that the resolution actually reaches
// the authenticator during a reload, and that a URL Jul cannot resolve fails
// the reload rather than producing a handler whose auth dependency can never be
// called.
func TestBuildWiresAuthDependencyPools(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()

	base := func(authCfg *config.AuthConfig) *config.Config {
		c := config.ProxyTarget("127.0.0.1:9001", ":0")
		c.Servers[0].Locations[0].Auth = authCfg
		return c
	}

	t.Run("a forward-auth location builds", func(t *testing.T) {
		c := base(&config.AuthConfig{
			ForwardAuth: &config.ForwardAuthConfig{URL: "http://auth.internal/verify"},
		})
		handlers, done, err := f.Build(context.Background(), c, false)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if done != nil {
			done()
		}
		if len(handlers) == 0 {
			t.Fatal("no handlers were built")
		}
	})

	t.Run("a jwt location builds", func(t *testing.T) {
		c := base(&config.AuthConfig{
			JWT: &config.JWTAuthConfig{JWKSURL: "https://issuer.example/jwks.json"},
		})
		handlers, done, err := f.Build(context.Background(), c, false)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if done != nil {
			done()
		}
		if len(handlers) == 0 {
			t.Fatal("no handlers were built")
		}
	})

	t.Run("an unresolvable jwks URL fails the reload", func(t *testing.T) {
		c := base(&config.AuthConfig{
			JWT: &config.JWTAuthConfig{JWKSURL: "https://%%%/jwks.json"},
		})
		if _, done, err := f.Build(context.Background(), c, false); err == nil {
			if done != nil {
				done()
			}
			t.Fatal("a JWKS URL that cannot become a backend was accepted at build time")
		}
	})

	t.Run("an unresolvable forward-auth URL fails the reload", func(t *testing.T) {
		c := base(&config.AuthConfig{
			ForwardAuth: &config.ForwardAuthConfig{URL: "http://%%%/verify"},
		})
		if _, done, err := f.Build(context.Background(), c, false); err == nil {
			if done != nil {
				done()
			}
			t.Fatal("a URL that cannot become a backend was accepted at build time")
		}
	})
}

// TestAuthDependencyPoolUsesTheRegistry pins that a named upstream resolves
// through the shared registry rather than building a private pool, so the auth
// dependency shares the reload transaction and the counters of that upstream.
func TestAuthDependencyPoolUsesTheRegistry(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()

	upstreams := map[string]config.UpstreamConfig{
		"authpool": {
			Name:     "authpool",
			Strategy: "round_robin",
			Servers:  []config.UpstreamServer{{Address: "10.0.0.1:8080", Weight: 1}},
			MaxFails: 3,
		},
	}
	f.PoolReg.Begin()
	pool, err := f.authDependencyPool(context.Background(), "http://authpool/verify", upstreams)
	if err != nil {
		t.Fatalf("authDependencyPool: %v", err)
	}
	f.PoolReg.Abort()
	if pool == nil {
		t.Fatal("expected a pool")
	}
	if got := pool.Name(); got != "authpool" {
		t.Fatalf("pool name = %q, want the named upstream", got)
	}
}
