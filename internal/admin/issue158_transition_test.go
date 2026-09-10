// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestAdminRateLimitHotLoosenPreservesCapacityInsteadOfRefilling(t *testing.T) {
	cfg := limitTestConfig(240, 2, 30, 4)
	s := newTestServer(t, cfg, Deps{})
	now := time.Unix(6000, 0)
	s.limiter.now = func() time.Time { return now }
	h := s.routes()
	peer := "127.0.0.1:6000"

	_ = requestFrom(t, h, http.MethodPost, "/api/wizard", peer)
	_ = requestFrom(t, h, http.MethodPost, "/api/wizard", peer)
	now = now.Add(30 * time.Second) // one token under the old 2/min policy
	cfg.RateLimitWritePerMin = 4
	s.UpdateLiveAdminConfig(cfg)

	if rr := requestFrom(t, h, http.MethodPost, "/api/wizard", peer); rr.Code == http.StatusTooManyRequests {
		t.Fatal("elapsed old-policy capacity should remain usable after loosening")
	}
	if rr := requestFrom(t, h, http.MethodPost, "/api/wizard", peer); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("loosen reload refilled the bucket instead of preserving state: status=%d", rr.Code)
	}
}

func TestAdminRateLimitDelayedDisableReenableIsBoundedByNewBurst(t *testing.T) {
	cfg := limitTestConfig(240, 2, 30, 4)
	s := newTestServer(t, cfg, Deps{})
	now := time.Unix(7000, 0)
	s.limiter.now = func() time.Time { return now }
	h := s.routes()
	peer := "127.0.0.1:7000"

	_ = requestFrom(t, h, http.MethodPost, "/api/wizard", peer)
	_ = requestFrom(t, h, http.MethodPost, "/api/wizard", peer)
	cfg.RateLimitWritePerMin = -1
	s.UpdateLiveAdminConfig(cfg)
	now = now.Add(10 * time.Minute)
	cfg.RateLimitWritePerMin = 3
	s.UpdateLiveAdminConfig(cfg)

	allowed := 0
	for i := 0; i < 5; i++ {
		if rr := requestFrom(t, h, http.MethodPost, "/api/wizard", peer); rr.Code != http.StatusTooManyRequests {
			allowed++
		}
	}
	if allowed > 3 {
		t.Fatalf("re-enable manufactured %d immediate admissions; new burst is 3", allowed)
	}
	if allowed == 0 {
		t.Fatal("long disabled interval should retain legitimate elapsed capacity")
	}
}

// This is a black-box characterization of the exact x/time/rate dependency used
// by Jul (v0.15.0 at #158 implementation time). It freezes the behavior relied
// upon by the lazy tightening transaction without reaching into limiter internals.
func TestXTimeRateTighteningCharacterization(t *testing.T) {
	now := time.Unix(8000, 0)
	lim := rate.NewLimiter(rate.Limit(1), 4)
	if !lim.AllowN(now, 1) || !lim.AllowN(now, 1) {
		t.Fatal("failed to establish partially consumed four-token bucket")
	}
	if got := lim.TokensAt(now); got < 1.9 {
		t.Fatalf("unexpected pre-tighten tokens %.3f", got)
	}

	lim.SetLimitAt(now, rate.Limit(1.0/60.0))
	lim.SetBurstAt(now, 1)
	reservation := lim.ReserveN(now, 1)
	if !reservation.OK() || reservation.DelayFrom(now) != 0 {
		t.Fatalf("first reservation after tightening should consume one clamped token: ok=%v delay=%s", reservation.OK(), reservation.DelayFrom(now))
	}
	second := lim.ReserveN(now, 1)
	if !second.OK() || second.DelayFrom(now) <= 0 {
		t.Fatalf("second reservation must be delayed by tightened policy: ok=%v delay=%s", second.OK(), second.DelayFrom(now))
	}
	second.CancelAt(now)
	if got := lim.TokensAt(now); got > 0.000001 {
		t.Fatalf("cancellation created excess tightened capacity: tokens=%.6f", got)
	}
}

func TestAdminRouteCatalogueLimitClassificationMatrix(t *testing.T) {
	want := map[string]limitKind{
		"/api/v1/status":                limitRead,
		"/api/v1/config/validate":       limitApply,
		"/api/v1/config/plan":           limitApply,
		"/api/v1/config/apply":          limitApply,
		"/api/v1/config/patch/apply":    limitApply,
		"/api/v1/config/rollback":       limitApply,
		"/api/v1/config/adopt-external": limitApply,
		"/api/config/validate":          limitApply,
		"/api/config/diff":              limitApply,
	}
	seen := make(map[string]bool, len(want))
	for _, spec := range Catalog {
		for _, method := range spec.Methods {
			kind := limitClassForSpec(spec, method)
			if kind != limitRead && kind != limitWrite && kind != limitApply {
				t.Fatalf("route %s %s has invalid limiter class %d", method, spec.Pattern, kind)
			}
			if expected, ok := want[spec.Pattern]; ok {
				seen[spec.Pattern] = true
				if kind != expected {
					t.Fatalf("route %s %s class=%s want=%s", method, spec.Pattern, kind, expected)
				}
			}
		}
	}
	for path := range want {
		if !seen[path] {
			t.Fatalf("expected authoritative route %q absent from Catalog", path)
		}
	}
}

func TestAdminLimiterStatsAreAggregateAndIdentityFree(t *testing.T) {
	l := newAdminLimiter(nil)
	p := adminLimitPolicy{maxConns: 1}
	r1, ok := l.acquireConn("192.0.2.10", p)
	if !ok {
		t.Fatal("client A denied")
	}
	defer r1()
	r2, ok := l.acquireConn("192.0.2.11", adminLimitPolicy{maxConns: 2})
	if !ok {
		t.Fatal("client B denied")
	}
	defer r2()
	r3, ok := l.acquireConn("192.0.2.11", adminLimitPolicy{maxConns: 2})
	if !ok {
		t.Fatal("client B second stream denied")
	}
	defer r3()

	st := l.stats(p)
	if st.SSEActiveTotal != 3 || st.SSEActiveClients != 2 || st.SSEOverCapClients != 1 || st.SSEMaxPerClient != 2 {
		t.Fatalf("unexpected aggregate stats: %+v", st)
	}
}
