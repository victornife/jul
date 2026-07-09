// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"

	"jul/internal/config"
)

// TestReloadChurnNoLeak is the SEQ-05 (#31) runtime validation that rebuilding
// authenticators on every configuration reload leaks neither goroutines nor
// heap, across Basic, JWT, forward-auth, and a mixed all-methods permutation.
//
// On each reload the server reconstructs its per-location authByScope map with
// fresh *Authenticator values and drops the previous generation without an
// explicit Close (see cmd/jul buildHandlers). That is only safe because the type
// owns no background worker, timer, or long-lived socket: auth.New spawns
// nothing and JWKS refresh is lazy and request-driven. This test proves that
// invariant at runtime — a sustained build+exercise+drop churn must return to
// its pre-churn goroutine and heap baseline. A per-reload leak would grow the
// goroutine count in proportion to the hundreds/thousands of reload cycles, so
// the gate fails on any growth beyond a small constant slack that absorbs
// keep-alive and scheduler jitter (which is bounded, not proportional).
//
// The cycle count is env-tunable so the default CI run finishes in seconds while
// a dedicated validation lane can soak far longer:
//
//	AUTH_CHURN_ITERS  reload cycles per permutation (default 300)
func TestReloadChurnNoLeak(t *testing.T) {
	iters := churnEnvInt("AUTH_CHURN_ITERS", 300)

	t.Run("basic", func(t *testing.T) {
		htpath := writeHtpasswd(t, map[string]string{"alice": "s3cret"})
		cfg := config.AuthConfig{Basic: &config.BasicAuthConfig{File: htpath, Realm: "Restricted"}}
		hit := func() int {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.SetBasicAuth("alice", "s3cret")
			rec := httptest.NewRecorder()
			newAuth(t, cfg, nil).Wrap(&okHandler{}).ServeHTTP(rec, r)
			return rec.Code
		}
		if code := hit(); code != http.StatusOK {
			t.Fatalf("basic precheck: code=%d, want 200", code)
		}
		churn(t, iters, func() { hit() })
	})

	t.Run("jwt", func(t *testing.T) {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatalf("rsa key: %v", err)
		}
		jwks := newJWKSServer(t, rsaJWK("rsa-1", &key.PublicKey))
		token := signRS256(t, key, "rsa-1", validClaims())
		cfg := config.AuthConfig{JWT: &config.JWTAuthConfig{
			JWKSURL:    jwks.URL,
			Issuer:     "https://issuer.example",
			Audience:   "my-api",
			Algorithms: defaultAlgs(),
		}}
		hit := func() int {
			rec := httptest.NewRecorder()
			// A brand-new authenticator starts with an empty JWKS cache, so each
			// reload drives a real network refresh — the most plausible leak site.
			newAuth(t, cfg, nil).Wrap(&okHandler{}).ServeHTTP(rec, bearerReq(token))
			return rec.Code
		}
		if code := hit(); code != http.StatusOK {
			t.Fatalf("jwt precheck: code=%d, want 200", code)
		}
		churn(t, iters, func() { hit() })
		if got := jwks.fetchCount(); got < iters {
			t.Errorf("JWKS fetches = %d, want >= %d (one per reload)", got, iters)
		}
	})

	t.Run("forward", func(t *testing.T) {
		fsrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Forwarded-Uri") == "/allow" {
				w.Header().Set("X-Auth-User", "bob")
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusForbidden)
		}))
		t.Cleanup(fsrv.Close)
		cfg := config.AuthConfig{ForwardAuth: &config.ForwardAuthConfig{
			URL:                 fsrv.URL,
			AuthResponseHeaders: []string{"X-Auth-User"},
		}}
		hit := func() int {
			r := httptest.NewRequest(http.MethodGet, "http://app.example/allow", nil)
			rec := httptest.NewRecorder()
			newAuth(t, cfg, nil).Wrap(&okHandler{}).ServeHTTP(rec, r)
			return rec.Code
		}
		if code := hit(); code != http.StatusOK {
			t.Fatalf("forward precheck: code=%d, want 200", code)
		}
		churn(t, iters, func() { hit() })
	})

	t.Run("mixed", func(t *testing.T) {
		// A realistic reload rebuilds every location's authenticator at once, so
		// exercise all methods per cycle to stress the whole authByScope rebuild.
		htpath := writeHtpasswd(t, map[string]string{"alice": "s3cret"})
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatalf("rsa key: %v", err)
		}
		jwks := newJWKSServer(t, rsaJWK("rsa-1", &key.PublicKey))
		token := signRS256(t, key, "rsa-1", validClaims())
		fsrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Forwarded-Uri") == "/allow" {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusForbidden)
		}))
		t.Cleanup(fsrv.Close)

		basicCfg := config.AuthConfig{Basic: &config.BasicAuthConfig{File: htpath, Realm: "R"}}
		jwtCfg := config.AuthConfig{JWT: &config.JWTAuthConfig{JWKSURL: jwks.URL, Issuer: "https://issuer.example", Audience: "my-api", Algorithms: defaultAlgs()}}
		fwdCfg := config.AuthConfig{ForwardAuth: &config.ForwardAuthConfig{URL: fsrv.URL}}
		cidrCfg := config.AuthConfig{Allow: []string{"0.0.0.0/0"}}

		cycle := func() [4]int {
			var codes [4]int

			rb := httptest.NewRequest(http.MethodGet, "/", nil)
			rb.SetBasicAuth("alice", "s3cret")
			recb := httptest.NewRecorder()
			newAuth(t, basicCfg, nil).Wrap(&okHandler{}).ServeHTTP(recb, rb)
			codes[0] = recb.Code

			recj := httptest.NewRecorder()
			newAuth(t, jwtCfg, nil).Wrap(&okHandler{}).ServeHTTP(recj, bearerReq(token))
			codes[1] = recj.Code

			rf := httptest.NewRequest(http.MethodGet, "http://app.example/allow", nil)
			recf := httptest.NewRecorder()
			newAuth(t, fwdCfg, nil).Wrap(&okHandler{}).ServeHTTP(recf, rf)
			codes[2] = recf.Code

			recc := httptest.NewRecorder()
			newAuth(t, cidrCfg, nil).Wrap(&okHandler{}).ServeHTTP(recc, httptest.NewRequest(http.MethodGet, "/", nil))
			codes[3] = recc.Code

			return codes
		}
		if codes := cycle(); codes != [4]int{200, 200, 200, 200} {
			t.Fatalf("mixed precheck: codes=%v, want all 200", codes)
		}
		churn(t, iters, func() { cycle() })
	})
}

// goroutineChurnSlack is the maximum tolerated growth in live goroutines across a
// full churn run. It is a flat constant — independent of the reload count — so a
// genuine per-reload leak (which scales with iterations) trips it immediately,
// while the handful of pooled keep-alive connection handlers and scheduler jitter
// stay comfortably under it.
const goroutineChurnSlack = 20

// heapChurnBudget bounds post-GC heap growth across a churn run. Authenticators
// and their transient per-request state are collectible, so a bounded budget
// catches a retained-per-reload leak without flaking on allocator noise.
const heapChurnBudget = 24 << 20 // 24 MiB

// churn warms up, records a settled goroutine/heap baseline, runs iters
// build+exercise cycles, then asserts the process returned to baseline within
// the leak thresholds.
func churn(t *testing.T, iters int, cycle func()) {
	t.Helper()
	for i := 0; i < 8; i++ { // warm lazy connections/goroutines before baseline
		cycle()
	}
	baseG := stableGoroutines()
	_, baseHeap := churnSample()

	for i := 0; i < iters; i++ {
		cycle()
	}

	endG := stableGoroutines()
	_, endHeap := churnSample()

	t.Logf("reload churn: iters=%d goroutines %d -> %d (slack %d), heap %d -> %d bytes (budget %d)",
		iters, baseG, endG, goroutineChurnSlack, baseHeap, endHeap, heapChurnBudget)

	if growth := endG - baseG; growth > goroutineChurnSlack {
		t.Errorf("goroutine leak: grew by %d (%d -> %d) over %d reloads", growth, baseG, endG, iters)
	}
	if growth := int64(endHeap) - int64(baseHeap); growth > int64(heapChurnBudget) {
		t.Errorf("heap leak: grew by %d bytes (budget %d) over %d reloads", growth, int64(heapChurnBudget), iters)
	}
}

// churnSample forces GC and returns the current goroutine count and live heap.
func churnSample() (goroutines int, heap uint64) {
	runtime.GC()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return runtime.NumGoroutine(), ms.HeapAlloc
}

// stableGoroutines returns the low-water goroutine count over a short settling
// window, filtering the transient connection-handler goroutines that HTTP
// keep-alive spins up and tears down around each exercised reload.
func stableGoroutines() int {
	lo := int(^uint(0) >> 1)
	for i := 0; i < 12; i++ {
		runtime.GC()
		if n := runtime.NumGoroutine(); n < lo {
			lo = n
		}
		time.Sleep(10 * time.Millisecond)
	}
	return lo
}

func churnEnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
