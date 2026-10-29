// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

func durPtr(d time.Duration) *config.Duration {
	cd := config.Duration(d)
	return &cd
}

func TestRouteCachePolicyFreshnessPrecedenceAndCap(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	curNow := now

	c, err := New(config.CacheConfig{
		Enabled:              true,
		MemoryMaxSize:        4 << 20,
		DefaultTTL:           config.Duration(5 * time.Minute),
		StaleWhileRevalidate: config.Duration(30 * time.Second),
		StaleIfError:         config.Duration(10 * time.Minute),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return curNow
	}
	advance := func(d time.Duration) {
		mu.Lock()
		curNow = curNow.Add(d)
		mu.Unlock()
	}

	// Route 1: default_ttl = 0s (requires explicit origin freshness), max_ttl = 1m,
	// stale_while_revalidate = 0s, stale_if_error = 2m.
	rcAPI := CompileRouteCacheConfig("route:api", false, &config.LocationCachePolicy{
		DefaultTTL:           durPtr(0),
		MaxTTL:               durPtr(time.Minute),
		StaleWhileRevalidate: durPtr(0),
		StaleIfError:         durPtr(2 * time.Minute),
	})
	c.CommitRoutePolicies([]RouteCacheConfig{rcAPI})

	var originCalls atomic.Int32
	var mode atomic.Int32
	origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := originCalls.Add(1)
		switch mode.Load() {
		case 0:
			// No explicit freshness headers -> must NOT be stored when route default_ttl = 0s.
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, "implicit-%d", n)
		case 1:
			// Explicit max-age=600 -> capped to route max_ttl=1m (60s).
			w.Header().Set("Cache-Control", "public, max-age=600")
			w.Header().Set("ETag", `"v1"`)
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, "capped-%d", n)
		case 2:
			// Origin failure (503) -> route stale_if_error = 2m allows serving stale.
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("origin-down"))
		case 3:
			// Explicit origin exclusion always wins over route policy.
			w.Header().Set("Cache-Control", "public, max-age=600, no-store")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, "nostore-%d", n)
		}
	})

	h := c.HandlerForRoute(origin, rcAPI)

	// 1. Implicit freshness with route default_ttl = 0s -> not cached.
	for i := 1; i <= 2; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example.com/api/implicit", nil))
		if rec.Header().Get("X-Cache") != stateMiss || rec.Body.String() != fmt.Sprintf("implicit-%d", i) {
			t.Fatalf("step %d: expected MISS implicit-%d, got %s %q", i, i, rec.Header().Get("X-Cache"), rec.Body.String())
		}
	}

	// 2. Explicit max-age=600 is capped by route max_ttl = 1m.
	mode.Store(1)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example.com/api/capped", nil))
	if rec.Header().Get("X-Cache") != stateMiss {
		t.Fatalf("expected initial MISS, got %s", rec.Header().Get("X-Cache"))
	}

	advance(30 * time.Second)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example.com/api/capped", nil))
	if rec.Header().Get("X-Cache") != stateHit || rec.Body.String() != "capped-3" {
		t.Fatalf("at 30s: expected HIT capped-3, got %s %q", rec.Header().Get("X-Cache"), rec.Body.String())
	}

	// At 61s (> 1m route max_ttl, even though origin said max-age=600), entry is no longer fresh!
	// Because route stale_while_revalidate = 0s and origin fails with 503, route stale_if_error = 2m serves STALE.
	advance(31 * time.Second)
	mode.Store(2)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example.com/api/capped", nil))
	if rec.Header().Get("X-Cache") != stateStale || rec.Body.String() != "capped-3" {
		t.Fatalf("at 61s with 503 origin: expected STALE capped-3, got %s %q", rec.Header().Get("X-Cache"), rec.Body.String())
	}

	// If route stale_if_error is tightened to 0s via hot reload, a 503 origin failure is no longer masked by global stale_if_error = 10m.
	rcAPINoSIE := CompileRouteCacheConfig("route:api", false, &config.LocationCachePolicy{
		DefaultTTL:           durPtr(0),
		MaxTTL:               durPtr(time.Minute),
		StaleWhileRevalidate: durPtr(0),
		StaleIfError:         durPtr(0),
	})
	c.CommitRoutePolicies([]RouteCacheConfig{rcAPINoSIE})
	hNoSIE := c.HandlerForRoute(origin, rcAPINoSIE)
	rec = httptest.NewRecorder()
	hNoSIE.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example.com/api/capped", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("with route SIE=0s: expected 503, got %d (%s)", rec.Code, rec.Header().Get("X-Cache"))
	}

	// 3. Explicit origin exclusion (no-store) always wins.
	mode.Store(3)
	for i := 0; i < 2; i++ {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example.com/api/nostore", nil))
		if rec.Header().Get("X-Cache") != stateMiss {
			t.Fatalf("no-store must never hit: got %s", rec.Header().Get("X-Cache"))
		}
	}
}

func TestRouteCachePolicyMaxTTLZeroDisablesFreshStorage(t *testing.T) {
	c, err := New(config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: 4 << 20,
		DefaultTTL:    config.Duration(5 * time.Minute),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rcZeroCap := CompileRouteCacheConfig("route:zero-cap", false, &config.LocationCachePolicy{
		MaxTTL: durPtr(0),
	})
	c.CommitRoutePolicies([]RouteCacheConfig{rcZeroCap})

	var calls atomic.Int32
	origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "body-%d", n)
	})
	h := c.HandlerForRoute(origin, rcZeroCap)

	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "http://example.com/doc", nil))
	if rec1.Header().Get("X-Cache") != stateMiss || rec1.Body.String() != "body-1" {
		t.Fatalf("first request: state=%s body=%q", rec1.Header().Get("X-Cache"), rec1.Body.String())
	}

	// Second request also misses because max_ttl = 0s caps fresh lifetime to zero.
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "http://example.com/doc", nil))
	if rec2.Header().Get("X-Cache") != stateMiss || rec2.Body.String() != "body-2" || calls.Load() != 2 {
		t.Fatalf("second request: expected MISS body-2, got state=%s body=%q calls=%d",
			rec2.Header().Get("X-Cache"), rec2.Body.String(), calls.Load())
	}
}

func TestRouteCachePolicyCookieBypassAndMalformedFailClosed(t *testing.T) {
	c, err := New(config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: 4 << 20,
		DefaultTTL:    config.Duration(5 * time.Minute),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.setDecisionSampling(true)

	rcNamed := CompileRouteCacheConfig("route:named-cookie", false, &config.LocationCachePolicy{
		BypassCookies: []string{"Session_ID", "__Host-Auth"},
	})
	rcAny := CompileRouteCacheConfig("route:any-cookie", false, &config.LocationCachePolicy{
		BypassAnyCookie: true,
	})
	c.CommitRoutePolicies([]RouteCacheConfig{rcNamed, rcAny})

	var calls atomic.Int32
	origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "resp-%d", n)
	})

	hNamed := c.HandlerForRoute(origin, rcNamed)

	// 1. Unmatched cookie ("theme=dark; other_session_id=1; note=session_id") does NOT trigger bypass_cookies=["session_id", "__host-auth"].
	reqUnmatched := httptest.NewRequest(http.MethodGet, "http://example.com/page", nil)
	reqUnmatched.Header.Set("Cookie", "theme=dark; other_session_id=1; note=session_id")
	rec := httptest.NewRecorder()
	hNamed.ServeHTTP(rec, reqUnmatched)
	if rec.Header().Get("X-Cache") != stateMiss || rec.Body.String() != "resp-1" {
		t.Fatalf("unmatched cookie first req: %s %q", rec.Header().Get("X-Cache"), rec.Body.String())
	}
	rec = httptest.NewRecorder()
	hNamed.ServeHTTP(rec, reqUnmatched)
	if rec.Header().Get("X-Cache") != stateHit || rec.Body.String() != "resp-1" {
		t.Fatalf("unmatched cookie second req: expected HIT resp-1, got %s %q", rec.Header().Get("X-Cache"), rec.Body.String())
	}

	// 2. Matching cookie ("SESSION_ID=secret-token-value") across multiple Cookie header lines bypasses lookup AND storage.
	reqMatched := httptest.NewRequest(http.MethodGet, "http://example.com/page", nil)
	reqMatched.Header.Add("Cookie", "theme=dark")
	reqMatched.Header.Add("Cookie", "SESSION_ID=SECRET_COOKIE_VALUE_NEVER_LEAK")
	rec = httptest.NewRecorder()
	hNamed.ServeHTTP(rec, reqMatched)
	if rec.Header().Get("X-Cache") != stateBypass || rec.Body.String() != "resp-2" {
		t.Fatalf("matched cookie must bypass cached resp-1: got %s %q", rec.Header().Get("X-Cache"), rec.Body.String())
	}

	// Anonymous request still hits resp-1 (resp-2 from bypassed request was NOT stored).
	reqAnon := httptest.NewRequest(http.MethodGet, "http://example.com/page", nil)
	rec = httptest.NewRecorder()
	hNamed.ServeHTTP(rec, reqAnon)
	if rec.Header().Get("X-Cache") != stateHit || rec.Body.String() != "resp-1" {
		t.Fatalf("anonymous request after bypass: expected HIT resp-1, got %s %q", rec.Header().Get("X-Cache"), rec.Body.String())
	}

	// 3. Malformed Cookie header (missing '=', control char, or oversized > 8192 bytes) fails closed.
	for _, badCookie := range []string{
		"malformed_cookie_without_equals",
		"=emptyname",
		"bad name=1",
		"oversized=" + strings.Repeat("x", 9000),
	} {
		reqBad := httptest.NewRequest(http.MethodGet, "http://example.com/page", nil)
		reqBad.Header.Set("Cookie", badCookie)
		rec = httptest.NewRecorder()
		hNamed.ServeHTTP(rec, reqBad)
		if rec.Header().Get("X-Cache") != stateBypass {
			t.Fatalf("malformed cookie %q must fail closed (BYPASS), got %s", badCookie[:min(20, len(badCookie))], rec.Header().Get("X-Cache"))
		}
	}

	// 4. bypass_any_cookie = true bypasses any Cookie header, even an empty one.
	hAny := c.HandlerForRoute(origin, rcAny)
	reqAnonAny := httptest.NewRequest(http.MethodGet, "http://example.com/any", nil)
	rec = httptest.NewRecorder()
	hAny.ServeHTTP(rec, reqAnonAny)
	if rec.Header().Get("X-Cache") != stateMiss {
		t.Fatalf("expected initial MISS on /any, got %s", rec.Header().Get("X-Cache"))
	}
	reqWithCookie := httptest.NewRequest(http.MethodGet, "http://example.com/any", nil)
	reqWithCookie.Header.Set("Cookie", "prefs=1")
	rec = httptest.NewRecorder()
	hAny.ServeHTTP(rec, reqWithCookie)
	if rec.Header().Get("X-Cache") != stateBypass {
		t.Fatalf("bypass_any_cookie must bypass on any cookie, got %s", rec.Header().Get("X-Cache"))
	}

	// Verify inspection never leaks cookie values.
	sel, err := ParseInspectionSelector(Selector{Kind: SelectorHost, Host: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	insp, err := c.InspectScope(httptest.NewRequest(http.MethodGet, "/", nil).Context(), sel)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%+v", insp), "SECRET_COOKIE_VALUE_NEVER_LEAK") {
		t.Fatal("cookie value leaked into inspection output")
	}
}

func TestRouteCachePolicyHotReloadTightenRollbackInflightFenceAndEpoch(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	curNow := now

	c, err := New(config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: 4 << 20,
		DiskPath:      dir,
		DiskMaxSize:   16 << 20,
		DefaultTTL:    config.Duration(10 * time.Minute),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return curNow
	}
	advance := func(d time.Duration) {
		mu.Lock()
		curNow = curNow.Add(d)
		mu.Unlock()
	}

	// Start with permissive route policy (no cookie bypass, no max_ttl cap).
	rcV1 := CompileRouteCacheConfig("route:hot", false, nil)
	c.CommitRoutePolicies([]RouteCacheConfig{rcV1})

	var calls atomic.Int32
	origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Cache-Control", "public, max-age=600")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "v-%d", n)
	})

	hV1 := c.HandlerForRoute(origin, rcV1)

	// Populate entry1 from a cookie-bearing request under V1 (where no cookie bypass was active).
	reqCookie := httptest.NewRequest(http.MethodGet, "http://example.com/cookie-populated", nil)
	reqCookie.Header.Set("Cookie", "sid=123")
	rec := httptest.NewRecorder()
	hV1.ServeHTTP(rec, reqCookie)
	if rec.Header().Get("X-Cache") != stateMiss || rec.Body.String() != "v-1" {
		t.Fatalf("expected MISS v-1, got %s %q", rec.Header().Get("X-Cache"), rec.Body.String())
	}

	// Populate entry2 from an anonymous request under V1 (max-age=600).
	reqAnon := httptest.NewRequest(http.MethodGet, "http://example.com/anon-populated", nil)
	rec = httptest.NewRecorder()
	hV1.ServeHTTP(rec, reqAnon)
	if rec.Header().Get("X-Cache") != stateMiss || rec.Body.String() != "v-2" {
		t.Fatalf("expected MISS v-2, got %s %q", rec.Header().Get("X-Cache"), rec.Body.String())
	}

	// Advance 2 minutes (still well within origin max-age=600).
	advance(2 * time.Minute)

	// Hot reload to V2: tighten max_ttl to 1m and enable bypass_cookies = ["sid"].
	rcV2 := CompileRouteCacheConfig("route:hot", false, &config.LocationCachePolicy{
		MaxTTL:        durPtr(time.Minute),
		BypassCookies: []string{"sid"},
	})
	c.CommitRoutePolicies([]RouteCacheConfig{rcV2})
	hV2 := c.HandlerForRoute(origin, rcV2)

	// Request to /cookie-populated (even WITHOUT Cookie header!) must NOT reuse v-1
	// because v-1 was stored from a cookie-bearing request before cookie bypass was tightened.
	rec = httptest.NewRecorder()
	hV2.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example.com/cookie-populated", nil))
	if rec.Header().Get("X-Cache") != stateMiss || rec.Body.String() != "v-3" {
		t.Fatalf("cookie-tainted entry must not be reused after tightening bypass policy: got %s %q",
			rec.Header().Get("X-Cache"), rec.Body.String())
	}

	// Request to /anon-populated at age=2m must NOT be served fresh under V2 (max_ttl=1m).
	rec = httptest.NewRecorder()
	hV2.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example.com/anon-populated", nil))
	if rec.Header().Get("X-Cache") != stateMiss || rec.Body.String() != "v-4" {
		t.Fatalf("entry exceeding tightened max_ttl=1m must miss: got %s %q",
			rec.Header().Get("X-Cache"), rec.Body.String())
	}

	// Now disable caching on route:hot (empty active slice) and then re-enable it:
	// RouteEpoch increments so old entries from before disabling are not reused!
	c.CommitRoutePolicies(nil)
	c.CommitRoutePolicies([]RouteCacheConfig{rcV1})
	hReenabled := c.HandlerForRoute(origin, rcV1)
	rec = httptest.NewRecorder()
	hReenabled.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example.com/cookie-populated", nil))
	if rec.Header().Get("X-Cache") != stateMiss || rec.Body.String() != "v-5" {
		t.Fatalf("re-enabled route must not reuse pre-disable epoch entry: got %s %q",
			rec.Header().Get("X-Cache"), rec.Body.String())
	}

	// Disable again and restart TWICE from disk (first restart while disabled, second restart re-enabling):
	// pre-disable disk entry must NOT be resurrected after either restart!
	cRestarted1, err := New(config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: 4 << 20,
		DiskPath:      dir,
		DiskMaxSize:   16 << 20,
		DefaultTTL:    config.Duration(10 * time.Minute),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cRestarted1.now = c.now
	cRestarted1.CommitRoutePolicies(nil)

	cRestarted2, err := New(config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: 4 << 20,
		DiskPath:      dir,
		DiskMaxSize:   16 << 20,
		DefaultTTL:    config.Duration(10 * time.Minute),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cRestarted2.now = c.now
	cRestarted2.CommitRoutePolicies([]RouteCacheConfig{rcV1})
	hAfterRestart := cRestarted2.HandlerForRoute(origin, rcV1)
	rec = httptest.NewRecorder()
	hAfterRestart.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example.com/cookie-populated", nil))
	if rec.Header().Get("X-Cache") != stateMiss || rec.Body.String() != "v-6" {
		t.Fatalf("restarted cache must not resurrect pre-disable disk entry: got %s %q",
			rec.Header().Get("X-Cache"), rec.Body.String())
	}
}

func BenchmarkEvaluateCookieBypass_NoCookie(b *testing.B) {
	c, _ := New(config.CacheConfig{Enabled: true, MemoryMaxSize: 1 << 20}, nil)
	rc := CompileRouteCacheConfig("route:bench", false, &config.LocationCachePolicy{
		BypassCookies: []string{"session_id", "__Host-auth", "logged_in"},
	})
	pol := c.effectivePolicy(&rc)
	req := httptest.NewRequest(http.MethodGet, "http://example.com/items", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if reason := evaluateCookieBypass(req, pol); reason != "" {
			b.Fatal("unexpected bypass")
		}
	}
}

func BenchmarkEvaluateCookieBypass_NamedCookies(b *testing.B) {
	c, _ := New(config.CacheConfig{Enabled: true, MemoryMaxSize: 1 << 20}, nil)
	rc := CompileRouteCacheConfig("route:bench", false, &config.LocationCachePolicy{
		BypassCookies: []string{"session_id", "__Host-auth", "logged_in"},
	})
	pol := c.effectivePolicy(&rc)
	req := httptest.NewRequest(http.MethodGet, "http://example.com/items", nil)
	req.Header.Set("Cookie", "theme=dark; lang=en; prefs=compact; session_id=abc123xyz")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if reason := evaluateCookieBypass(req, pol); reason == "" {
			b.Fatal("expected bypass")
		}
	}
}

func TestVaryTransformGuardsProxyAndCGIInCachePackage(t *testing.T) {
	c, err := New(config.CacheConfig{
		Enabled:         true,
		MemoryMaxSize:   4 << 20,
		DefaultTTL:      config.Duration(5 * time.Minute),
		DecisionSamples: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	rcProxy := CompileRouteCacheConfig("route:proxy", true, nil).
		WithProxyVaryGuard([]string{"Host", "X-Forwarded-For", "X-Custom-Rewrite"}, true)
	rcProxyIncomplete := CompileRouteCacheConfig("route:proxy-inc", true, nil).
		WithProxyVaryGuard(nil, false)
	rcCGI := CompileRouteCacheConfig("route:cgi", true, nil).
		WithCGIVaryGuard([]string{"FORWARDED", "X-REAL-IP", "X-CGI-SET"}, []string{"X-FORWARDED-", "X-SSL-CLIENT-"}, true)
	rcCGIIncomplete := CompileRouteCacheConfig("route:cgi-inc", true, nil).
		WithCGIVaryGuard(nil, nil, false)

	c.CommitRoutePolicies([]RouteCacheConfig{rcProxy, rcProxyIncomplete, rcCGI, rcCGIIncomplete})

	var varyHeader atomic.Value
	varyHeader.Store("Accept-Encoding")
	var calls atomic.Int32
	origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Cache-Control", "public, max-age=300")
		if v, _ := varyHeader.Load().(string); v != "" {
			w.Header().Set("Vary", v)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "resp-%d", n)
	})

	hProxy := c.HandlerForRoute(origin, rcProxy)
	hCGI := c.HandlerForRoute(origin, rcCGI)

	// 1. Safe Vary: Accept-Encoding is cached and hits on second request.
	for i, wantState := range []string{stateMiss, stateHit} {
		req := httptest.NewRequest(http.MethodGet, "http://example.com/proxy-safe", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		hProxy.ServeHTTP(rec, req)
		if rec.Header().Get("X-Cache") != wantState {
			t.Fatalf("step %d: expected %s, got %s", i, wantState, rec.Header().Get("X-Cache"))
		}
	}

	// 2. Proxy rewritten header (X-Custom-Rewrite) and Connection-nominated header are rejected.
	for _, vh := range []string{"X-Custom-Rewrite", "X-Conn-Hop"} {
		varyHeader.Store(vh)
		for i := 0; i < 2; i++ {
			req := httptest.NewRequest(http.MethodGet, "http://example.com/proxy-reject-"+vh, nil)
			req.Header.Set("Connection", "X-Conn-Hop")
			rec := httptest.NewRecorder()
			hProxy.ServeHTTP(rec, req)
			if rec.Header().Get("X-Cache") != stateMiss {
				t.Fatalf("proxy Vary %q step %d: expected MISS, got %s", vh, i, rec.Header().Get("X-Cache"))
			}
		}
	}

	// 3. Incomplete proxy guard rejects any non-empty Vary.
	varyHeader.Store("Accept-Language")
	hProxyInc := c.HandlerForRoute(origin, rcProxyIncomplete)
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		hProxyInc.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example.com/proxy-inc", nil))
		if rec.Header().Get("X-Cache") != stateMiss {
			t.Fatalf("incomplete proxy guard step %d: expected MISS, got %s", i, rec.Header().Get("X-Cache"))
		}
	}

	// 4. CGI prefix (X-Forwarded-Proto), CGI underscore (X_Underscore), and CGI hyphen/underscore collision (X-Collide vs X_Collide).
	for _, tc := range []struct {
		vary      string
		reqHeader string
	}{
		{"X-Forwarded-Proto", ""},
		{"X-CGI-Set", ""},
		{"X_Underscore", ""},
		{"X-Collide", "X_Collide"},
	} {
		varyHeader.Store(tc.vary)
		for i := 0; i < 2; i++ {
			req := httptest.NewRequest(http.MethodGet, "http://example.com/cgi-"+tc.vary, nil)
			if tc.reqHeader != "" {
				req.Header[tc.reqHeader] = []string{"val"}
			}
			rec := httptest.NewRecorder()
			hCGI.ServeHTTP(rec, req)
			if rec.Header().Get("X-Cache") != stateMiss {
				t.Fatalf("cgi Vary %q step %d: expected MISS, got %s", tc.vary, i, rec.Header().Get("X-Cache"))
			}
		}
	}

	// 5. Direct unit checks on varyRejectionEntry / varyRejectionFields / varyRejectionHeader edge cases.
	polCGI := c.effectivePolicy(&rcCGI)
	polProxy := c.effectivePolicy(&rcProxy)
	hOversized := http.Header{"Vary": []string{strings.Repeat("A", maxProxyVaryBytes+10)}}
	if got := varyRejectionHeader(nil, polProxy.varyGuard, hOversized); got != "vary_proxy_rewrite_rejected" {
		t.Fatalf("expected vary_proxy_rewrite_rejected for oversized Vary, got %q", got)
	}
	manyFields := make([]string, maxProxyVaryFields+2)
	for i := range manyFields {
		manyFields[i] = fmt.Sprintf("X-Field-%d", i)
	}
	if got := varyRejectionFields(nil, polCGI.varyGuard, manyFields); got != "vary_cgi_rewrite_rejected" {
		t.Fatalf("expected vary_cgi_rewrite_rejected for too many Vary fields, got %q", got)
	}
	entryWithRouteVary := &Entry{
		RouteVary:  map[string][]string{"r1": {"X-CGI-Set"}},
		VaryValues: map[string]string{"X-CGI-Set": "1"},
	}
	if got := varyRejectionEntry(nil, polCGI.varyGuard, entryWithRouteVary); got != "vary_cgi_rewrite_rejected" {
		t.Fatalf("expected vary_cgi_rewrite_rejected from RouteVary, got %q", got)
	}

	// 6. Invalid header names and >MaxProxyVaryNames in newProxyVaryTransformPolicy / newCGIVaryTransformPolicy,
	// plus oversized Connection lines in proxyVaryField.
	badProxyPol := newProxyVaryTransformPolicy([]string{"bad header name"}, true)
	if !badProxyPol.reject {
		t.Fatal("expected reject=true for invalid header name in newProxyVaryTransformPolicy")
	}
	tooManyNames := make([]string, MaxProxyVaryNames+2)
	for i := range tooManyNames {
		tooManyNames[i] = fmt.Sprintf("X-Name-%d", i)
	}
	if !newProxyVaryTransformPolicy(tooManyNames, true).reject {
		t.Fatal("expected reject=true for >MaxProxyVaryNames")
	}
	if !newCGIVaryTransformPolicy(tooManyNames[:MaxProxyVaryNames], []string{"X-PRE-"}, true).reject {
		t.Fatal("expected reject=true when names+prefixes > MaxProxyVaryNames")
	}
	if !newCGIVaryTransformPolicy(nil, []string{"bad prefix"}, true).reject {
		t.Fatal("expected reject=true for invalid prefix in newCGIVaryTransformPolicy")
	}
	reqManyConn := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	for i := 0; i <= maxConnectionVaryNames; i++ {
		reqManyConn.Header.Add("Connection", "x-hop")
	}
	if !proxyVaryField(reqManyConn, polProxy.varyGuard, "Accept") {
		t.Fatal("expected proxyVaryField=true for >maxConnectionVaryNames")
	}
	reqBigConn := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	reqBigConn.Header.Set("Connection", strings.Repeat("a", maxConnectionVaryBytes+10))
	if !proxyVaryField(reqBigConn, polProxy.varyGuard, "Accept") {
		t.Fatal("expected proxyVaryField=true for >maxConnectionVaryBytes")
	}
	reqMultiConn := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	reqMultiConn.Header.Set("Connection", "keep-alive, x-hop-2")
	if proxyVaryField(reqMultiConn, polProxy.varyGuard, "Accept") {
		t.Fatal("expected proxyVaryField=false when Connection does not match Accept")
	}
	if sensitiveVaryEntry(nil) || varyRejectionEntry(nil, nil, nil) != "" {
		t.Fatal("expected nil entry checks to return false/empty")
	}
	if !sensitiveVaryEntry(&Entry{VaryValues: map[string]string{"Cookie": "1"}}) ||
		!sensitiveVaryEntry(&Entry{RouteVary: map[string][]string{"r1": {"Authorization"}}}) {
		t.Fatal("expected sensitiveVaryEntry=true for VaryValues and RouteVary")
	}
	if proxyVaryField(nil, nil, "Accept") || !proxyVaryField(nil, polProxy.varyGuard, "bad field") || proxyVaryField(nil, polCGI.varyGuard, "Accept") {
		t.Fatal("unexpected proxyVaryField nil/invalid results")
	}
	if varyRejectionFields(nil, polProxy.varyGuard, []string{"*"}) != "" {
		t.Fatal("expected Vary: * to be ignored by varyRejectionFields")
	}
	if varyRejectionHeader(nil, polProxy.varyGuard, http.Header{"Vary": []string{"Accept, X-Custom-Rewrite"}}) != "vary_proxy_rewrite_rejected" {
		t.Fatal("expected vary_proxy_rewrite_rejected for multi-token Vary header")
	}
	(*Cache)(nil).cleanupSensitiveVary("k", nil, 0)
}

func TestMultiRouteOverlapVaryStubAndRouteScopeInspection(t *testing.T) {
	routeA := "route-a"
	routeB := "route-b"
	canary := "1"
	srv := config.ServerConfig{
		Listen:      ":8080",
		ServerNames: []string{"Example.COM"},
		Locations: []config.LocationConfig{
			{
				RouteID:   &routeA,
				Match:     config.MatchConfig{Type: "prefix", Path: "/shared", Headers: []config.HeaderMatch{{Name: "X-Canary", Value: &canary}}},
				ProxyPass: "http://127.0.0.1:9001",
				Cache:     true,
			},
			{
				RouteID:   &routeB,
				Match:     config.MatchConfig{Type: "prefix", Path: "/shared"},
				ProxyPass: "http://127.0.0.1:9002",
				Cache:     true,
			},
		},
	}
	cfg := &config.Config{
		Cache: config.CacheConfig{
			Enabled:       true,
			MemoryMaxSize: 4 << 20,
			DefaultTTL:    config.Duration(5 * time.Minute),
		},
		Servers: []config.ServerConfig{srv},
	}

	c, err := New(cfg.Cache, nil)
	if err != nil {
		t.Fatal(err)
	}
	scopeA := RouteScope(srv, srv.Locations[0])
	scopeB := RouteScope(srv, srv.Locations[1])
	if scopeA == "" || scopeB == "" || scopeA == scopeB {
		t.Fatalf("expected distinct non-empty scopes, got %q and %q", scopeA, scopeB)
	}

	rcA := CompileRouteCacheConfig(scopeA, true, nil)
	rcB := CompileRouteCacheConfig(scopeB, true, nil)
	c.CommitRoutePolicies([]RouteCacheConfig{rcA, rcB})

	var varyA atomic.Value
	varyA.Store("Accept-Language")
	hA := c.HandlerForRoute(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		if v, _ := varyA.Load().(string); v != "" {
			w.Header().Set("Vary", v)
		}
		_, _ = w.Write([]byte("from-A-" + r.Header.Get("Accept-Language")))
	}), rcA)

	hB := c.HandlerForRoute(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Header().Set("Vary", "Accept-Encoding")
		_, _ = w.Write([]byte("from-B-" + r.Header.Get("Accept-Encoding")))
	}), rcB)

	// Populate route A and route B at the exact same URL (/shared/item).
	reqA := httptest.NewRequest(http.MethodGet, "http://example.com/shared/item", nil)
	reqA.Header.Set("Accept-Language", "en")
	recA := httptest.NewRecorder()
	hA.ServeHTTP(recA, reqA)
	if recA.Header().Get("X-Cache") != stateMiss || recA.Body.String() != "from-A-en" {
		t.Fatalf("route A miss failed: %s %q", recA.Header().Get("X-Cache"), recA.Body.String())
	}

	reqB := httptest.NewRequest(http.MethodGet, "http://example.com/shared/item", nil)
	reqB.Header.Set("Accept-Encoding", "br")
	recB := httptest.NewRecorder()
	hB.ServeHTTP(recB, reqB)
	if recB.Header().Get("X-Cache") != stateMiss || recB.Body.String() != "from-B-br" {
		t.Fatalf("route B miss failed: %s %q", recB.Header().Get("X-Cache"), recB.Body.String())
	}

	// Both route A and route B hit their own representations without thrashing!
	recA2 := httptest.NewRecorder()
	hA.ServeHTTP(recA2, reqA)
	if recA2.Header().Get("X-Cache") != stateHit || recA2.Body.String() != "from-A-en" {
		t.Fatalf("route A hit failed: %s %q", recA2.Header().Get("X-Cache"), recA2.Body.String())
	}
	recB2 := httptest.NewRecorder()
	hB.ServeHTTP(recB2, reqB)
	if recB2.Header().Get("X-Cache") != stateHit || recB2.Body.String() != "from-B-br" {
		t.Fatalf("route B hit failed: %s %q", recB2.Header().Get("X-Cache"), recB2.Body.String())
	}

	// Changing route A's Vary header replaces only route A's variants while preserving route B's variant!
	varyA.Store("Accept")
	reqANew := httptest.NewRequest(http.MethodGet, "http://example.com/shared/item", nil)
	reqANew.Header.Set("Accept", "application/json")
	recANew := httptest.NewRecorder()
	hA.ServeHTTP(recANew, reqANew)
	if recANew.Header().Get("X-Cache") != stateMiss {
		t.Fatalf("expected MISS on route A after Vary change, got %s", recANew.Header().Get("X-Cache"))
	}
	recB3 := httptest.NewRecorder()
	hB.ServeHTTP(recB3, reqB)
	if recB3.Header().Get("X-Cache") != stateHit || recB3.Body.String() != "from-B-br" {
		t.Fatalf("route B must remain HIT after route A Vary change: got %s %q", recB3.Header().Get("X-Cache"), recB3.Body.String())
	}

	// Inspect by RouteID=route-a filters only route A's representation (1 representation, not 2).
	selA, err := ParseInspectionSelector(Selector{Kind: SelectorHost, RouteID: routeA}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	inspA, err := c.InspectScope(reqA.Context(), selA)
	if err != nil {
		t.Fatal(err)
	}
	if inspA.MatchedRepresentations != 1 || len(inspA.Resources) != 1 || len(inspA.Resources[0].Details) != 1 {
		t.Fatalf("expected 1 representation for routeA inspection, got %+v", inspA)
	}

	// Direct unit checks on validatedReusableWithPolicy and isPolicyPublicationFencedLocked.
	polB := c.effectivePolicy(&rcB)
	eFromA := &Entry{
		Status:     http.StatusOK,
		Header:     http.Header{"Cache-Control": []string{"public, max-age=60"}},
		RouteScope: scopeA,
		RouteEpoch: 0,
		Evidence:   &publicationEvidence{InsertedAt: time.Now().UnixNano()},
	}
	if c.validatedReusableWithPolicy(eFromA, reqA, polB) {
		t.Fatal("expected validatedReusableWithPolicy=false for cross-route entry")
	}
	rcCookie := CompileRouteCacheConfig(scopeA, true, &config.LocationCachePolicy{BypassAnyCookie: true})
	polCookie := c.effectivePolicy(&rcCookie)
	if c.validatedReusableWithPolicy(eFromA, reqA, polCookie) {
		t.Fatal("expected validatedReusableWithPolicy=false for cookie-tainted entry")
	}

	c.genMu.Lock()
	c.routePolicyFloor[scopeA] = 100
	c.globalPolicyFloorGen = 100
	fenced := c.isPolicyPublicationFencedLocked(50, eFromA)
	c.genMu.Unlock()
	if !fenced {
		t.Fatal("expected isPolicyPublicationFencedLocked=true when startGen <= floor and digest differs")
	}

	if ep := c.CurrentRouteEpoch(scopeA); ep != 0 {
		t.Fatalf("expected CurrentRouteEpoch=0, got %d", ep)
	}
	if (*Cache)(nil).CurrentRouteEpoch(scopeA) != 0 {
		t.Fatal("expected nil CurrentRouteEpoch=0")
	}

	// Exercise cookie bypass edge cases (many lines, empty line, trailing semicolon, control byte, >128 pairs).
	rcNamed := CompileRouteCacheConfig(scopeA, true, &config.LocationCachePolicy{BypassCookies: []string{"sid"}})
	polNamed := c.effectivePolicy(&rcNamed)
	reqManyLines := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	for i := 0; i <= maxCookieHeaderLines; i++ {
		reqManyLines.Header.Add("Cookie", "a=1")
	}
	if evaluateCookieBypass(reqManyLines, polNamed) != "cookie_bypass_malformed" {
		t.Fatal("expected cookie_bypass_malformed for >maxCookieHeaderLines")
	}
	for _, hdrVal := range []string{"   ", "a=1;", "a=1\x01", strings.Repeat("a=1;", maxCookiePairs+1)} {
		reqEdge := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
		reqEdge.Header.Set("Cookie", hdrVal)
		if evaluateCookieBypass(reqEdge, polNamed) != "cookie_bypass_malformed" {
			t.Fatalf("expected cookie_bypass_malformed for %q", hdrVal[:min(16, len(hdrVal))])
		}
	}

	// Exercise corrupt .jul-cache-route-epochs fail-closed recovery on startup.
	corruptDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(corruptDir, diskRouteEpochsFile), []byte("{corrupt-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(config.CacheConfig{Enabled: true, MemoryMaxSize: 1 << 20, DiskPath: corruptDir, DiskMaxSize: 4 << 20}, nil); err != nil {
		t.Fatal(err)
	}

	// Exercise lookupForPolicy, stubForRoute, and reuseDecisionWithPolicy branches.
	rcGuard := CompileRouteCacheConfig(scopeA, true, &config.LocationCachePolicy{
		DefaultTTL:   durPtr(0),
		StaleIfError: durPtr(0),
	}).WithProxyVaryGuard([]string{"X-Rewritten"}, true)
	polGuard := c.effectivePolicy(&rcGuard)

	// 1. Non-stub entry with rewritten Vary header in lookupForPolicy.
	baseNonStub := keyFor(http.MethodGet, "example.com", "/non-stub-guard")
	c.set(baseNonStub, &Entry{
		Status:     http.StatusOK,
		Header:     http.Header{"Vary": []string{"X-Rewritten"}},
		Vary:       []string{"X-Rewritten"},
		RouteScope: scopeA,
	})
	if _, _, reason, ok := c.lookupForPolicy(baseNonStub, reqA, polGuard); ok || reason != "vary_proxy_rewrite_rejected" {
		t.Fatalf("expected vary_proxy_rewrite_rejected on non-stub lookup, got ok=%v reason=%q", ok, reason)
	}

	// 2. Non-stub entry with route ownership mismatch and MultiRouteOverlap=true.
	c.set(baseNonStub, &Entry{
		Status:     http.StatusOK,
		Header:     http.Header{"Cache-Control": []string{"public, max-age=60"}},
		RouteScope: scopeB,
	})
	if _, _, reason, ok := c.lookupForPolicy(baseNonStub, reqA, polGuard); ok || reason != "route_ownership_mismatch" {
		t.Fatalf("expected route_ownership_mismatch on non-stub lookup, got ok=%v reason=%q", ok, reason)
	}

	// 3. Legacy Vary stub (RouteVary == nil) with different routeScope, same routeScope, and rewritten Vary.
	baseLegacyStub := keyFor(http.MethodGet, "example.com", "/legacy-stub")
	c.set(baseLegacyStub, &Entry{
		IsVaryStub: true,
		RouteScope: scopeB,
		Vary:       []string{"Accept"},
		Variants:   []string{baseLegacyStub + "\x00v1"},
	})
	if _, _, reason, ok := c.lookupForPolicy(baseLegacyStub, reqA, polGuard); ok || reason != "route_ownership_mismatch" {
		t.Fatalf("expected route_ownership_mismatch on legacy stub, got ok=%v reason=%q", ok, reason)
	}
	_ = c.stubForRoute(baseLegacyStub, scopeA, []string{"Accept"}, c.genSeq.Load())
	c.set(baseLegacyStub, &Entry{
		IsVaryStub: true,
		RouteScope: scopeA,
		Vary:       []string{"Accept"},
	})
	_ = c.stubForRoute(baseLegacyStub, scopeA, []string{"Accept"}, c.genSeq.Load())

	c.set(baseLegacyStub, &Entry{
		IsVaryStub: true,
		RouteScope: scopeA,
		Vary:       []string{"X-Rewritten"},
	})
	if _, _, reason, ok := c.lookupForPolicy(baseLegacyStub, reqA, polGuard); ok || reason != "vary_proxy_rewrite_rejected" {
		t.Fatalf("expected vary_proxy_rewrite_rejected on stub Vary, got ok=%v reason=%q", ok, reason)
	}

	// 4. Stub pointing to variant with rewritten Vary or mismatched routeScope.
	vk := routeVariantKey(baseLegacyStub, scopeA, []string{"Accept"}, reqA)
	c.set(baseLegacyStub, &Entry{
		IsVaryStub: true,
		RouteScope: scopeA,
		Vary:       []string{"Accept"},
		RouteVary:  map[string][]string{scopeA: {"Accept"}},
		Variants:   []string{vk},
	})
	c.set(vk, &Entry{
		Status:     http.StatusOK,
		Header:     http.Header{"Vary": []string{"X-Rewritten"}},
		Vary:       []string{"X-Rewritten"},
		RouteScope: scopeA,
	})
	if _, _, reason, ok := c.lookupForPolicy(baseLegacyStub, reqA, polGuard); ok || reason != "vary_proxy_rewrite_rejected" {
		t.Fatalf("expected vary_proxy_rewrite_rejected on variant lookup, got ok=%v reason=%q", ok, reason)
	}

	// 5. reuseDecisionWithPolicy with ValidatedAt != 0, evidenceStaleIfError, and DefaultTTL=0 rejection.
	now := time.Now().UTC()
	eImplicit := &Entry{
		Status:    http.StatusOK,
		Header:    http.Header{},
		CreatedAt: now,
		ExpiresAt: now.Add(time.Minute),
		Evidence:  &publicationEvidence{InsertedAt: now.UnixNano(), ValidatedAt: now.UnixNano(), Flags: evidenceStaleIfError},
	}
	if kind, reason := c.reuseDecisionWithPolicy(eImplicit, reqA, requestPolicy{}, polGuard, now); kind != reuseNone || reason != "route_policy_mismatch" {
		t.Fatalf("expected reuseNone/route_policy_mismatch for implicit TTL under DefaultTTL=0, got %v %q", kind, reason)
	}
	eExplicitSIE := &Entry{
		Status:     http.StatusOK,
		Header:     http.Header{"Cache-Control": []string{"public, max-age=60"}},
		CreatedAt:  now.Add(-2 * time.Minute),
		ExpiresAt:  now.Add(-time.Minute),
		StaleUntil: now.Add(5 * time.Minute),
		Evidence:   &publicationEvidence{InsertedAt: now.Add(-2 * time.Minute).UnixNano(), ValidatedAt: now.Add(-time.Minute).UnixNano(), Flags: evidenceStaleIfError},
	}
	if kind, _ := c.reuseDecisionWithPolicy(eExplicitSIE, reqA, requestPolicy{}, polGuard, now); kind != reuseNone {
		t.Fatalf("expected reuseNone when route StaleIfError=0, got %v", kind)
	}
	rcSIE10m := CompileRouteCacheConfig(scopeA, true, &config.LocationCachePolicy{StaleIfError: durPtr(10 * time.Minute)})
	if kind, _ := c.reuseDecisionWithPolicy(eExplicitSIE, reqA, requestPolicy{}, c.effectivePolicy(&rcSIE10m), now); kind != reuseStale {
		t.Fatalf("expected reuseStale when route StaleIfError=10m, got %v", kind)
	}
}
