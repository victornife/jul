// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"jul/internal/config"
)

func TestPolicyIdentityMemoConcurrentSnapshots(t *testing.T) {
	var memo policyIdentityMemo
	policies := []CachePolicy{{DefaultTTL: time.Hour}, {DefaultTTL: time.Second, RouteScope: "other", RouteEpoch: 7, BypassCookieCount: 1, BypassCookies: [config.MaxBypassCookies]string{"sid"}}}
	want := [][32]byte{policyID(policies[0]), policyID(policies[1])}
	wantBypass := [][16]byte{bypassPolicyDigest(policies[0]), bypassPolicyDigest(policies[1])}
	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 1000 {
				index := (i + worker) % len(policies)
				identity := memo.snapshot(policies[index])
				if identity.digest != want[index] || identity.bypassDigest != wantBypass[index] {
					t.Error("concurrent snapshot received another policy's identity")
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestHandlerIdentityMemoDoesNotMaskPolicyTightening(t *testing.T) {
	for _, scoped := range []bool{false, true} {
		t.Run(map[bool]string{false: "global", true: "route"}[scoped], func(t *testing.T) {
			cfg := config.CacheConfig{MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Hour)}
			c := newTestCache(t, cfg)
			now := time.Now()
			c.now = func() time.Time { return now }
			calls := 0
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				_, _ = w.Write([]byte("body"))
			})
			h := c.Handler(next)
			if scoped {
				rc := CompileRouteCacheConfig("memo-route", false, nil).WithProxyVaryGuard(nil, true)
				c.CommitRoutePolicies([]RouteCacheConfig{rc})
				h = c.HandlerForRoute(next, rc)
			}
			request := func(want string) {
				t.Helper()
				rr := httptest.NewRecorder()
				h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "http://example.com/item", nil))
				if got := rr.Header().Get("X-Cache"); got != want {
					t.Fatalf("X-Cache = %q, want %q", got, want)
				}
			}
			request("MISS")
			request("HIT")
			cfg.DefaultTTL = config.Duration(time.Second)
			c.PrepareCacheUpdate(cfg).Commit()
			now = now.Add(2 * time.Second)
			request("MISS")
			request("HIT")
			if calls != 2 {
				t.Fatalf("origin calls = %d, want 2", calls)
			}
		})
	}
}
