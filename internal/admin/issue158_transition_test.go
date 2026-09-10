// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestAdminRateLimitHotLoosenPreservesCapacityInsteadOfRefilling(t *testing.T) {
	l := newAdminLimiter(nil)
	now := time.Unix(6000, 0)
	l.now = func() time.Time { return now }
	peer := "127.0.0.1"
	oldPolicy := adminLimitPolicy{writePerMin: 2}
	_, _ = l.allow(peer, limitWrite, oldPolicy)
	_, _ = l.allow(peer, limitWrite, oldPolicy)

	now = now.Add(30 * time.Second) // one token accrues under old 2/min policy
	newPolicy := adminLimitPolicy{writePerMin: 4}
	if ok, _ := l.allow(peer, limitWrite, newPolicy); !ok {
		t.Fatal("elapsed old-policy capacity should remain usable after loosening")
	}
	if ok, _ := l.allow(peer, limitWrite, newPolicy); ok {
		t.Fatal("loosen reload refilled the bucket instead of preserving state")
	}
}

func TestAdminRateLimitDelayedDisableReenableIsBoundedByNewBurst(t *testing.T) {
	l := newAdminLimiter(nil)
	now := time.Unix(7000, 0)
	l.now = func() time.Time { return now }
	peer := "127.0.0.1"
	oldPolicy := adminLimitPolicy{writePerMin: 2}
	_, _ = l.allow(peer, limitWrite, oldPolicy)
	_, _ = l.allow(peer, limitWrite, oldPolicy)

	disabled := adminLimitPolicy{writePerMin: -1}
	if ok, _ := l.allow(peer, limitWrite, disabled); !ok {
		t.Fatal("disabled class unexpectedly rejected admission")
	}
	now = now.Add(10 * time.Minute)
	newPolicy := adminLimitPolicy{writePerMin: 3}

	allowed := 0
	for i := 0; i < 5; i++ {
		if ok, _ := l.allow(peer, limitWrite, newPolicy); ok {
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

func TestAdminRateLimitConcurrentRetuneUsesOneStableBucket(t *testing.T) {
	l := newAdminLimiter(nil)
	now := time.Unix(7500, 0)
	l.now = func() time.Time { return now }
	peer := "198.51.100.44"
	policies := []adminLimitPolicy{
		{writePerMin: 120},
		{writePerMin: 7},
		{writePerMin: -1},
		{writePerMin: 60},
		{writePerMin: 1},
	}

	var wg sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				_, _ = l.allow(peer, limitWrite, policies[(i+offset)%len(policies)])
			}
		}(worker)
	}
	wg.Wait()

	// Force one deterministic final retune after the concurrent phase. The
	// same bucket must survive and expose the tightened dependency state.
	_, _ = l.allow(peer, limitWrite, adminLimitPolicy{writePerMin: 1})
	l.mu.Lock()
	bucket := l.buckets[peer]
	l.mu.Unlock()
	if bucket == nil || bucket.write.limiter == nil {
		t.Fatal("concurrent retunes replaced or lost the stable write bucket")
	}
	if got := bucket.write.limiter.Burst(); got != 1 {
		t.Fatalf("final burst=%d want 1", got)
	}
}

// This is a black-box characterization of the exact x/time/rate dependency used
// by Jul (v0.15.0 at #158 implementation time). It freezes the behavior relied
// upon by the lazy tightening transaction without reaching into limiter internals.
func TestXTimeRateTighteningCharacterization(t *testing.T) {
	now := time.Unix(8000, 0)
	lim := rate.NewLimiter(rate.Limit(1), 4)
	if !lim.AllowN(now, 1) {
		t.Fatal("failed to establish first consumption from four-token bucket")
	}
	if !lim.AllowN(now, 1) {
		t.Fatal("failed to establish second consumption from four-token bucket")
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

func TestAdminLimiterPublishDoesNotLockOrScanTrackedClients(t *testing.T) {
	initial := limitTestConfig(240, 60, 30, 4)
	s := newTestServer(t, initial, Deps{})
	now := time.Unix(8100, 0)
	s.limiter.now = func() time.Time { return now }
	policy := adminLimitPolicyFromConfig(initial)
	for i := 0; i < 4096; i++ {
		peer := fmt.Sprintf("client-%d", i)
		_, _ = s.limiter.allow(peer, limitRead, policy)
	}
	if got := len(s.limiter.buckets); got != 4096 {
		t.Fatalf("tracked clients=%d want 4096", got)
	}

	candidate := initial
	candidate.RateLimitReadPerMin = 5
	candidate.MaxEventConns = 2
	prepared := PrepareAuth(candidate, nil)
	prepared, err := s.PrepareAdminRuntime(candidate, prepared)
	if err != nil {
		t.Fatalf("prepare candidate: %v", err)
	}

	// Hold the manager lock across Publish. A correct HR-07A publication only
	// swaps the immutable request snapshot and therefore cannot wait for this
	// lock or walk the 4096-client map.
	s.limiter.mu.Lock()
	done := make(chan struct{})
	go func() {
		s.CommitPreparedAuth(prepared)
		close(done)
	}()
	blocked := false
	select {
	case <-done:
	case <-time.After(time.Second):
		blocked = true
	}
	s.limiter.mu.Unlock()
	if blocked {
		<-done
		t.Fatal("Publish blocked on limiter state; publication must be independent of client population")
	}

	if got := s.currentAuth().cfg.RateLimitReadPerMin; got != 5 {
		t.Fatalf("published read limit=%d want 5", got)
	}
	s.limiter.mu.Lock()
	tracked := len(s.limiter.buckets)
	s.limiter.mu.Unlock()
	if tracked != 4096 {
		t.Fatalf("Publish mutated tracked clients: got %d want 4096", tracked)
	}
}

func TestAdminPrepareFailurePreservesPublishedPolicyAndLimiterState(t *testing.T) {
	initial := limitTestConfig(240, 1, 30, 1)
	s := newTestServer(t, initial, Deps{})
	now := time.Unix(8200, 0)
	s.limiter.now = func() time.Time { return now }
	oldPolicy := adminLimitPolicyFromConfig(initial)
	peer := "203.0.113.77"
	if ok, _ := s.limiter.allow(peer, limitWrite, oldPolicy); !ok {
		t.Fatal("failed to establish initial write token")
	}
	if ok, _ := s.limiter.allow(peer, limitWrite, oldPolicy); ok {
		t.Fatal("initial write bucket should be exhausted")
	}
	release, ok := s.limiter.acquireConn(peer, oldPolicy)
	if !ok {
		t.Fatal("failed to establish initial SSE lease")
	}
	defer release()

	badDir := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(badDir, []byte("x"), 0o600); err != nil {
		t.Fatalf("create invalid upload target: %v", err)
	}
	enabled := true
	candidate := initial
	candidate.RateLimitWritePerMin = 100
	candidate.MaxEventConns = 8
	candidate.PluginUploadEnabled = &enabled
	candidate.PluginUploadMaxSize = 1
	candidate.PluginUploadDir = badDir
	if _, err := s.PrepareAdminRuntime(candidate, PrepareAuth(candidate, nil)); err == nil {
		t.Fatal("candidate with non-directory upload target unexpectedly prepared")
	}

	if got := s.currentAuth().cfg.RateLimitWritePerMin; got != initial.RateLimitWritePerMin {
		t.Fatalf("failed Prepare changed published write policy: got %d want %d", got, initial.RateLimitWritePerMin)
	}
	if got := s.currentAuth().cfg.MaxEventConns; got != initial.MaxEventConns {
		t.Fatalf("failed Prepare changed published SSE cap: got %d want %d", got, initial.MaxEventConns)
	}
	if ok, _ := s.limiter.allow(peer, limitWrite, oldPolicy); ok {
		t.Fatal("failed Prepare reset the exhausted limiter bucket")
	}
	if got := s.limiter.stats(oldPolicy).SSEActiveTotal; got != 1 {
		t.Fatalf("failed Prepare changed active SSE accounting: got %d want 1", got)
	}
}

func TestAdminRouteCatalogueLimitClassificationMatrix(t *testing.T) {
	want := map[string]limitKind{
		"/api/v1/status":                         limitRead,
		"/api/v1/config/validate":                limitApply,
		"/api/v1/config/plan":                    limitApply,
		"/api/v1/config/patch":                   limitApply,
		"/api/v1/config/apply":                   limitApply,
		"/api/v1/config/patch/apply":             limitApply,
		"/api/v1/config/rollback":                limitApply,
		"/api/v1/config/adopt-external/preview":  limitApply,
		"/api/v1/config/adopt-external":          limitApply,
		"/api/v1/config/pending-restart/discard": limitApply,
		"/api/config/validate":                   limitApply,
		"/api/config/diff":                       limitApply,
		"/api/wizard":                            limitWrite,
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
