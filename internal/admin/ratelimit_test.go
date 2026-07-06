// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/config"
)

// TestAdminRateLimitBlocksWriteFlood verifies that mutating requests beyond the
// per-client write budget receive 429 with a Retry-After header (Milestone 1.6).
func TestAdminRateLimitBlocksWriteFlood(t *testing.T) {
	cfg := config.AdminConfig{
		RateLimitReadPerMin:  240,
		RateLimitWritePerMin: 3,
		RateLimitApplyPerMin: 30,
		MaxEventConns:        4,
	}
	s := newTestServer(t, cfg, Deps{})
	h := s.routes()

	// The write budget is 3; the 4th mutating request in a burst must be denied.
	var lastCode int
	limited := false
	for i := 0; i < 8; i++ {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/wizard", nil)
		req.RemoteAddr = "203.0.113.7:5555"
		h.ServeHTTP(rr, req)
		lastCode = rr.Code
		if rr.Code == http.StatusTooManyRequests {
			limited = true
			if ra := rr.Header().Get("Retry-After"); ra == "" {
				t.Error("429 response missing Retry-After header")
			}
			break
		}
	}
	if !limited {
		t.Fatalf("expected a 429 within the write burst; last status = %d", lastCode)
	}
}

// TestAdminRateLimitReadGenerous verifies that read traffic well under the read
// budget is never limited, so console polling keeps working.
func TestAdminRateLimitReadGenerous(t *testing.T) {
	cfg := config.AdminConfig{
		RateLimitReadPerMin:  240,
		RateLimitWritePerMin: 60,
		RateLimitApplyPerMin: 30,
		MaxEventConns:        4,
	}
	s := newTestServer(t, cfg, Deps{})
	h := s.routes()

	for i := 0; i < 50; i++ {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.RemoteAddr = "203.0.113.8:5555"
		h.ServeHTTP(rr, req)
		if rr.Code == http.StatusTooManyRequests {
			t.Fatalf("read request %d was rate-limited under a generous budget", i)
		}
	}
}

// TestAdminRateLimitPerClientIsolation verifies that one noisy client hitting
// its limit does not deny a different client.
func TestAdminRateLimitPerClientIsolation(t *testing.T) {
	cfg := config.AdminConfig{
		RateLimitReadPerMin:  240,
		RateLimitWritePerMin: 2,
		RateLimitApplyPerMin: 30,
		MaxEventConns:        4,
	}
	s := newTestServer(t, cfg, Deps{})
	h := s.routes()

	// Exhaust client A.
	for i := 0; i < 10; i++ {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/wizard", nil)
		req.RemoteAddr = "198.51.100.1:1111"
		h.ServeHTTP(rr, req)
	}

	// Client B's first write must still be admitted (not 429).
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/wizard", nil)
	req.RemoteAddr = "198.51.100.2:2222"
	h.ServeHTTP(rr, req)
	if rr.Code == http.StatusTooManyRequests {
		t.Error("client B was rate-limited by client A's flood")
	}
}

// TestAdminRateLimitDisabled verifies that negative limits disable the limiter
// entirely so no request is ever throttled.
func TestAdminRateLimitDisabled(t *testing.T) {
	cfg := config.AdminConfig{
		RateLimitReadPerMin:  -1,
		RateLimitWritePerMin: -1,
		RateLimitApplyPerMin: -1,
		MaxEventConns:        -1,
	}
	s := newTestServer(t, cfg, Deps{})
	if s.limiter != nil {
		t.Fatal("limiter should be nil when all limits are disabled")
	}
	h := s.routes()
	for i := 0; i < 20; i++ {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/wizard", nil)
		req.RemoteAddr = "192.0.2.5:9999"
		h.ServeHTTP(rr, req)
		if rr.Code == http.StatusTooManyRequests {
			t.Fatalf("request %d limited despite disabled limiter", i)
		}
	}
}

// TestAdminEventConnCap verifies the SSE per-client connection cap admits up to
// maxConns and denies the next with 429.
func TestAdminEventConnCap(t *testing.T) {
	l := newAdminLimiter(nil, 240, 60, 30, 2)
	if l == nil {
		t.Fatal("limiter should not be nil")
	}
	r1, ok1 := l.acquireConn("203.0.113.9")
	r2, ok2 := l.acquireConn("203.0.113.9")
	if !ok1 || !ok2 {
		t.Fatal("first two connections should be admitted")
	}
	if _, ok3 := l.acquireConn("203.0.113.9"); ok3 {
		t.Error("third connection should be denied by the cap")
	}
	// Releasing one frees a slot.
	r1()
	if _, ok4 := l.acquireConn("203.0.113.9"); !ok4 {
		t.Error("connection should be admitted after a release")
	}
	r2()
}
