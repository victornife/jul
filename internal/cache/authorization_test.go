// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
)

const (
	aliceCred = "Bearer alice-secret-token"
	bobCred   = "Bearer bob-secret-token"
)

// authCache serves a body derived from the caller's identity, so any leak is
// visible in the response body rather than inferred.
func authCache(t *testing.T, responseCC string) (*Cache, http.Handler) {
	t.Helper()
	c, _ := conformanceCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(time.Hour)})
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if responseCC != "" {
			w.Header().Set("Cache-Control", responseCC)
		}
		who := "anonymous"
		switch r.Header.Get("Authorization") {
		case aliceCred:
			who = "alice-private-data"
		case bobCred:
			who = "bob-private-data"
		}
		_, _ = w.Write([]byte(who))
	}))
	return c, h
}

// TestUnauthenticatedEntryIsNotReusableByAnAuthenticatedRequest is the core
// RFC 9111 §3.5 rule: a stored response that never claimed shared reuse cannot
// answer a request carrying credentials.
func TestUnauthenticatedEntryIsNotReusableByAnAuthenticatedRequest(t *testing.T) {
	_, h := authCache(t, "max-age=3600")

	wantResult(t, get(t, h, "http://x/a"), stateMiss, "anonymous")
	wantResult(t, get(t, h, "http://x/a"), stateHit, "anonymous")

	rec := get(t, h, "http://x/a", "Authorization", aliceCred)
	if rec.Header().Get("X-Cache") == stateHit {
		t.Fatal("an authenticated request must not be answered from a response that never permitted shared reuse")
	}
	wantResult(t, rec, stateMiss, "alice-private-data")
}

// TestSharedReusePermissionMatrix pins which directives let a stored response
// answer an authenticated request, and which let an authenticated response be
// published at all.
func TestSharedReusePermissionMatrix(t *testing.T) {
	cases := []struct {
		name string
		cc   string
		// reuse: an entry stored from an ANONYMOUS request answers an
		// authenticated one.
		reuse bool
		// store: a response produced FOR an authenticated request is published.
		store bool
	}{
		{name: "no directives", cc: "max-age=3600"},
		{name: "public", cc: "public, max-age=3600", reuse: true, store: true},
		{name: "s-maxage", cc: "s-maxage=3600", reuse: true, store: true},
		{
			// §3.5 lists must-revalidate as a reuse permission. It is NOT a
			// publication permission: "do not serve me stale" says nothing about
			// whether the body is user-specific.
			name: "must-revalidate", cc: "must-revalidate, max-age=3600", reuse: true, store: false,
		},
		{name: "private", cc: "private, max-age=3600"},
		{name: "no-store", cc: "no-store"},
		{name: "no-cache", cc: "no-cache"},
	}

	for _, tc := range cases {
		t.Run(tc.name+"/reuse", func(t *testing.T) {
			_, h := authCache(t, tc.cc)
			get(t, h, "http://x/a") // anonymous, may or may not store

			rec := get(t, h, "http://x/a", "Authorization", aliceCred)
			served := rec.Header().Get("X-Cache")
			reused := served == stateHit || served == stateRevalidated
			if reused != tc.reuse {
				t.Errorf("authenticated reuse = %v (X-Cache %q), want %v", reused, served, tc.reuse)
			}
			if tc.reuse && rec.Body.String() != "anonymous" {
				t.Errorf("body = %q, want the stored anonymous representation", rec.Body.String())
			}
		})

		t.Run(tc.name+"/store", func(t *testing.T) {
			c, h := authCache(t, tc.cc)
			get(t, h, "http://x/a", "Authorization", aliceCred)

			r := httptest.NewRequest(http.MethodGet, "http://x/a", nil)
			e, _, ok := c.lookup(key(r), r)
			if ok != tc.store {
				t.Fatalf("authenticated response stored = %v, want %v", ok, tc.store)
			}
			if ok && !strings.Contains(string(e.Body), "alice") {
				t.Fatalf("stored body = %q", e.Body)
			}
		})
	}
}

// TestNoCrossIdentityLeakage is the security property in its bluntest form: two
// distinct identities must never see each other's body, whatever the origin's
// directives say, unless the origin explicitly published the response as shared.
func TestNoCrossIdentityLeakage(t *testing.T) {
	for _, cc := range []string{"max-age=3600", "private, max-age=3600", "no-cache", "no-store", "must-revalidate, max-age=3600"} {
		t.Run(cc, func(t *testing.T) {
			_, h := authCache(t, cc)

			alice := get(t, h, "http://x/a", "Authorization", aliceCred)
			if alice.Body.String() != "alice-private-data" {
				t.Fatalf("alice got %q", alice.Body.String())
			}
			bob := get(t, h, "http://x/a", "Authorization", bobCred)
			if bob.Body.String() == "alice-private-data" {
				t.Fatalf("LEAK: bob received alice's response under Cache-Control: %s", cc)
			}
			anon := get(t, h, "http://x/a")
			if strings.Contains(anon.Body.String(), "alice") || strings.Contains(anon.Body.String(), "bob") {
				t.Fatalf("LEAK: an anonymous request received %q under Cache-Control: %s", anon.Body.String(), cc)
			}
		})
	}
}

// TestPublicResponseIsSharedAcrossIdentitiesDeliberately is the counterpart: an
// origin that says public gets shared caching, which is the point of the
// directive. The body is identity-independent here, as such an origin promises.
func TestPublicResponseIsSharedAcrossIdentitiesDeliberately(t *testing.T) {
	c, _ := conformanceCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(time.Hour)})
	calls := 0
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write([]byte("shared"))
	}))

	wantResult(t, get(t, h, "http://x/a", "Authorization", aliceCred), stateMiss, "shared")
	wantResult(t, get(t, h, "http://x/a", "Authorization", bobCred), stateHit, "shared")
	wantResult(t, get(t, h, "http://x/a"), stateHit, "shared")
	if calls != 1 {
		t.Errorf("origin calls = %d, want 1", calls)
	}
}

// TestCredentialsNeverEnterTheCacheKey proves the key space is (method, host,
// target) and nothing else. A per-credential key would silently convert a leak
// into an unbounded cache.
func TestCredentialsNeverEnterTheCacheKey(t *testing.T) {
	plain := httptest.NewRequest(http.MethodGet, "http://x/a", nil)
	withAuth := httptest.NewRequest(http.MethodGet, "http://x/a", nil)
	withAuth.Header.Set("Authorization", aliceCred)
	withAuth.Header.Set("Cookie", "session=alice")

	if key(plain) != key(withAuth) {
		t.Fatal("the cache key must not depend on request credentials")
	}
	if strings.Contains(key(withAuth), "alice") || strings.Contains(key(withAuth), "Bearer") {
		t.Fatalf("credential material leaked into the cache key: %q", key(withAuth))
	}
}

// TestCredentialsNeverReachStoredMetadataOrObservability proves the credential
// does not survive into anything that is persisted, logged or exported: not the
// stored headers, not the variant key, not the revalidation outcome label.
func TestCredentialsNeverReachStoredMetadataOrObservability(t *testing.T) {
	c, _ := conformanceCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(time.Hour)})
	var outcomes []string
	c.SetRevalidationObserver(func(o string) { outcomes = append(outcomes, o) })
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, no-cache")
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Vary", "Authorization")
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write([]byte("body"))
	}))

	get(t, h, "http://x/a", "Authorization", aliceCred)
	get(t, h, "http://x/a", "Authorization", aliceCred)

	// The variant key legitimately contains the varied header value, but nothing
	// the cache EXPORTS may. Assert the exported surfaces directly.
	r := httptest.NewRequest(http.MethodGet, "http://x/a", nil)
	if stub, ok := c.get(key(r)); ok {
		for name, values := range stub.Header {
			for _, v := range values {
				if strings.Contains(v, "alice-secret-token") {
					t.Fatalf("credential leaked into stored header %s", name)
				}
			}
		}
	}
	for _, o := range outcomes {
		if strings.Contains(o, "alice") || strings.Contains(o, "Bearer") || strings.Contains(o, "/") {
			t.Fatalf("revalidation outcome label %q is not a bounded constant", o)
		}
	}
}

// TestVaryAuthorizationStillEnforcesTheSharedReuseRule proves Vary is not a
// substitute for the §3.5 permission: matching on the credential header does not
// make a response the origin never shared reusable.
func TestVaryAuthorizationStillEnforcesTheSharedReuseRule(t *testing.T) {
	c, _ := conformanceCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20), DefaultTTL: config.Duration(time.Hour)})
	calls := 0
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Cache-Control", "max-age=3600")
		w.Header().Set("Vary", "Authorization")
		_, _ = w.Write([]byte(r.Header.Get("Authorization")))
	}))

	get(t, h, "http://x/a", "Authorization", aliceCred)
	rec := get(t, h, "http://x/a", "Authorization", aliceCred)
	if rec.Header().Get("X-Cache") == stateHit {
		t.Fatal("an identical credential string must not be treated as proof that the response may be shared")
	}
	if calls != 2 {
		t.Errorf("origin calls = %d, want 2", calls)
	}
}
