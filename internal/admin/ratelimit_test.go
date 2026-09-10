// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"jul/internal/adminapi"
	"jul/internal/config"
	"jul/internal/rbac"
)

func limitTestConfig(read, write, apply, conns int) config.AdminConfig {
	return config.AdminConfig{
		Enabled:              true,
		Listen:               "127.0.0.1:0",
		RateLimitReadPerMin:  read,
		RateLimitWritePerMin: write,
		RateLimitApplyPerMin: apply,
		MaxEventConns:        conns,
	}
}

func requestFrom(t *testing.T, h http.Handler, method, path, remote string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = remote
	h.ServeHTTP(rr, req)
	return rr
}

func TestAdminLimiterAlwaysExistsWhenPolicyDisabled(t *testing.T) {
	s := newTestServer(t, limitTestConfig(-1, -1, -1, 4), Deps{})
	if s.limiter == nil {
		t.Fatal("hot-reloadable limiter manager must exist for the admin server lifetime")
	}
	policy := adminLimitPolicyFromConfig(limitTestConfig(-1, -1, -1, 4))
	for i := 0; i < 20; i++ {
		if ok, _ := s.limiter.allow("127.0.0.1", limitWrite, policy); !ok {
			t.Fatalf("request %d limited while write policy is disabled", i)
		}
	}
}

func TestAdminRateLimitBlocksWriteFlood(t *testing.T) {
	l := newAdminLimiter(nil)
	now := time.Unix(900, 0)
	l.now = func() time.Time { return now }
	policy := adminLimitPolicy{writePerMin: 3}
	for i := 0; i < 3; i++ {
		if ok, _ := l.allow("127.0.0.1", limitWrite, policy); !ok {
			t.Fatalf("write request %d rejected before burst was consumed", i)
		}
	}
	ok, retry := l.allow("127.0.0.1", limitWrite, policy)
	if ok {
		t.Fatal("fourth write should be rate limited")
	}
	if retry < 1 {
		t.Fatalf("Retry-After=%d want >=1", retry)
	}
}

func TestAdminRateLimitPerClientIsolation(t *testing.T) {
	l := newAdminLimiter(nil)
	now := time.Unix(950, 0)
	l.now = func() time.Time { return now }
	policy := adminLimitPolicy{writePerMin: 1}
	if ok, _ := l.allow("127.0.0.1", limitWrite, policy); !ok {
		t.Fatal("client A first request denied")
	}
	if ok, _ := l.allow("127.0.0.1", limitWrite, policy); ok {
		t.Fatal("client A second request should be denied")
	}
	if ok, _ := l.allow("127.0.0.2", limitWrite, policy); !ok {
		t.Fatal("client B inherited client A's rate state")
	}
}

func TestAdminRateLimitHotTightenClampsBeforeFirstAdmission(t *testing.T) {
	l := newAdminLimiter(nil)
	now := time.Unix(1000, 0)
	l.now = func() time.Time { return now }
	oldPolicy := adminLimitPolicy{writePerMin: 4}
	for i := 0; i < 2; i++ {
		if ok, _ := l.allow("127.0.0.1", limitWrite, oldPolicy); !ok {
			t.Fatal("unexpected pre-tighten rejection")
		}
	}
	newPolicy := adminLimitPolicy{writePerMin: 1}
	if ok, _ := l.allow("127.0.0.1", limitWrite, newPolicy); !ok {
		t.Fatal("first post-publish admission should consume the single clamped token")
	}
	if ok, _ := l.allow("127.0.0.1", limitWrite, newPolicy); ok {
		t.Fatal("second post-tighten admission should be denied")
	}
}

func TestAdminRateLimitDisableReenablePreservesExhaustedState(t *testing.T) {
	l := newAdminLimiter(nil)
	now := time.Unix(2000, 0)
	l.now = func() time.Time { return now }
	finite := adminLimitPolicy{writePerMin: 2}
	peer := "127.0.0.1"
	_, _ = l.allow(peer, limitWrite, finite)
	_, _ = l.allow(peer, limitWrite, finite)
	if ok, _ := l.allow(peer, limitWrite, finite); ok {
		t.Fatal("expected exhausted finite bucket")
	}

	disabled := adminLimitPolicy{writePerMin: -1}
	for i := 0; i < 3; i++ {
		if ok, _ := l.allow(peer, limitWrite, disabled); !ok {
			t.Fatal("disabled class must bypass admission")
		}
	}
	if ok, _ := l.allow(peer, limitWrite, finite); ok {
		t.Fatal("immediate re-enable granted a forgiveness burst")
	}
}

func TestAdminRateLimitIndependentClasses(t *testing.T) {
	cfg := limitTestConfig(1, 1, 1, 4)
	s := newTestServer(t, cfg, Deps{})
	now := time.Unix(3000, 0)
	s.limiter.now = func() time.Time { return now }
	p := adminLimitPolicyFromConfig(cfg)
	ip := "127.0.0.1"
	if ok, _ := s.limiter.allow(ip, limitRead, p); !ok {
		t.Fatal("first read denied")
	}
	if ok, _ := s.limiter.allow(ip, limitWrite, p); !ok {
		t.Fatal("first write denied")
	}
	if ok, _ := s.limiter.allow(ip, limitApply, p); !ok {
		t.Fatal("first apply denied")
	}
	if ok, _ := s.limiter.allow(ip, limitRead, p); ok {
		t.Fatal("second read should be denied")
	}
	if ok, _ := s.limiter.allow(ip, limitWrite, p); ok {
		t.Fatal("second write should be denied")
	}
	if ok, _ := s.limiter.allow(ip, limitApply, p); ok {
		t.Fatal("second apply should be denied")
	}
}

func TestAdminExternalRateLimitEnvelope(t *testing.T) {
	cfg := limitTestConfig(1, 60, 30, 4)
	s := newTestServer(t, cfg, Deps{})
	now := time.Unix(4000, 0)
	s.limiter.now = func() time.Time { return now }
	h := s.routes()
	peer := "127.0.0.1:7777"
	_ = requestFrom(t, h, http.MethodGet, "/api/v1/status", peer)
	rr := requestFrom(t, h, http.MethodGet, "/api/v1/status", peer)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d want 429", rr.Code)
	}
	if got := rr.Header().Get("Retry-After"); got == "" {
		t.Fatal("missing Retry-After")
	}
	if got := rr.Header().Get("X-Request-ID"); got == "" {
		t.Fatal("missing server request id")
	}
	var env adminapi.Envelope
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode external envelope: %v body=%q", err, rr.Body.String())
	}
	if env.Error.Code != adminapi.CodeRateLimited {
		t.Fatalf("code=%q want %q", env.Error.Code, adminapi.CodeRateLimited)
	}
	if env.Error.Details.RetryAfterSeconds == nil || *env.Error.Details.RetryAfterSeconds < 1 {
		t.Fatalf("retry_after_seconds=%v", env.Error.Details.RetryAfterSeconds)
	}
}

func TestLimitClassForSpec(t *testing.T) {
	cases := []struct {
		name   string
		spec   RouteSpec
		method string
		want   limitKind
	}{
		{"read", RouteSpec{Methods: []string{http.MethodGet}}, http.MethodGet, limitRead},
		{"write", RouteSpec{Methods: []string{http.MethodPost}, Permission: rbac.CachePurge}, http.MethodPost, limitWrite},
		{"apply permission", RouteSpec{Methods: []string{http.MethodPost}, Permission: rbac.ConfigApply}, http.MethodPost, limitApply},
		{"rollback permission", RouteSpec{Methods: []string{http.MethodPost}, Permission: rbac.HistoryRollback}, http.MethodPost, limitApply},
		{"external plan", RouteSpec{Methods: []string{http.MethodPost}, Permission: rbac.ConfigWrite, Operations: map[string]ExternalOperation{http.MethodPost: {ID: "planConfig"}}}, http.MethodPost, limitApply},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := limitClassForSpec(tc.spec, tc.method); got != tc.want {
				t.Fatalf("class=%s want %s", got, tc.want)
			}
		})
	}
}

func TestAdminEventConnCapHotReloadAndExactRelease(t *testing.T) {
	l := newAdminLimiter(nil)
	p4 := adminLimitPolicy{maxConns: 4}
	p2 := adminLimitPolicy{maxConns: 2}
	peer := "203.0.113.9"

	releases := make([]func(), 0, 4)
	for i := 0; i < 4; i++ {
		r, ok := l.acquireConn(peer, p4)
		if !ok {
			t.Fatalf("connection %d denied", i+1)
		}
		releases = append(releases, r)
	}
	if _, ok := l.acquireConn(peer, p2); ok {
		t.Fatal("tightened cap admitted a fifth stream")
	}
	st := l.stats(p2)
	if st.SSEActiveTotal != 4 || st.SSEActiveClients != 1 || st.SSEOverCapClients != 1 || st.SSEMaxPerClient != 4 {
		t.Fatalf("unexpected tightened status: %+v", st)
	}

	releases[0]()
	releases[0]() // idempotent release
	releases[1]()
	if _, ok := l.acquireConn(peer, p2); ok {
		t.Fatal("at the cap, a new stream must still be rejected")
	}
	releases[2]()
	r, ok := l.acquireConn(peer, p2)
	if !ok {
		t.Fatal("below cap, one stream should be admitted")
	}
	r()
	releases[3]()
	if got := l.stats(p2).SSEActiveTotal; got != 0 {
		t.Fatalf("active SSE after exact releases=%d want 0", got)
	}
}

func TestAdminLimiterGCDoesNotEvictActiveSSEClient(t *testing.T) {
	l := newAdminLimiter(nil)
	now := time.Unix(5000, 0)
	l.now = func() time.Time { return now }
	peer := "203.0.113.20"
	release, ok := l.acquireConn(peer, adminLimitPolicy{maxConns: 4})
	if !ok {
		t.Fatal("initial SSE denied")
	}

	now = now.Add(20 * time.Minute)
	l.mu.Lock()
	l.gcLocked(now)
	_, exists := l.buckets[peer]
	l.mu.Unlock()
	if !exists {
		t.Fatal("GC evicted client with active SSE lease")
	}

	release()
	now = now.Add(20 * time.Minute)
	l.mu.Lock()
	l.nextGC = time.Time{}
	l.gcLocked(now)
	_, exists = l.buckets[peer]
	l.mu.Unlock()
	if exists {
		t.Fatal("idle client was not reclaimed after final lease release")
	}
}
