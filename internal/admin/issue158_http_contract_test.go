// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAdminInternalRateLimitCompatibility(t *testing.T) {
	cfg := limitTestConfig(1, 1, 1, 4)
	s := newTestServer(t, cfg, Deps{})
	s.limiter.now = func() time.Time { return time.Unix(8300, 0) }
	h := s.routes()
	peer := "192.0.2.40:4444"

	_ = requestFrom(t, h, http.MethodGet, "/api/config/history", peer)
	rr := requestFrom(t, h, http.MethodGet, "/api/config/history", peer)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("internal status=%d want 429", rr.Code)
	}
	if rr.Header().Get("Retry-After") == "" {
		t.Fatal("internal 429 missing Retry-After")
	}
	if got := rr.Body.String(); got != "429 Too Many Requests\n" {
		t.Fatalf("internal compatibility body=%q", got)
	}
	if strings.Contains(rr.Body.String(), `"error"`) {
		t.Fatal("internal route unexpectedly switched to the external API envelope")
	}
}

func TestAdminFallbackRouteRetainsConservativeAdmission(t *testing.T) {
	cfg := limitTestConfig(1, 1, 1, 4)
	s := newTestServer(t, cfg, Deps{})
	s.limiter.now = func() time.Time { return time.Unix(8400, 0) }
	h := s.routes()
	peer := "192.0.2.41:4444"

	// The catalogue's stable root/fallback route handles unknown UI paths. It
	// must still consume the safe/read budget instead of bypassing admission or
	// exposing a route-existence distinction before authentication.
	_ = requestFrom(t, h, http.MethodGet, "/definitely-not-a-registered-admin-path", peer)
	rr := requestFrom(t, h, http.MethodGet, "/definitely-not-a-registered-admin-path", peer)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("fallback status=%d want 429 after read budget exhaustion", rr.Code)
	}
	if rr.Header().Get("Retry-After") == "" {
		t.Fatal("fallback 429 missing Retry-After")
	}
}
