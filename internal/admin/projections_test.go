// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
)

func TestProjectTLSDNSStateAndListenerScope(t *testing.T) {
	expiry := time.Now().Add(2 * time.Hour)
	cfg := &config.Config{Servers: []config.ServerConfig{
		{Listen: ":443", ServerNames: []string{"example.test"}, TLS: &config.TLSConfig{Enabled: true, ACME: &config.ACMEConfig{Enabled: true}}},
		{Listen: ":8443", ServerNames: []string{"example.test"}, TLS: &config.TLSConfig{Enabled: true, ACME: &config.ACMEConfig{Enabled: true}}},
	}}
	live := []CertStatus{
		{Listen: ":443", ServerNames: []string{"example.test"}, State: "pending"},
		{Listen: ":8443", ServerNames: []string{"example.test"}, State: "available", NotAfter: expiry, Error: "renewal failed",
			Variants: []CertVariantStatus{
				{Algorithm: "ecdsa-p256", State: "available", NotAfter: &expiry, CleanupWarning: "cleanup pending"},
				{Algorithm: "rsa-2048", State: "failed", Error: "DNS update refused"},
			}},
	}
	got := projectTLS(cfg, cfg, live)
	if len(got) != 2 || got[0].Listen != ":443" || got[0].State != "pending" || got[0].DaysLeft != nil ||
		got[1].Listen != ":8443" || got[1].State != "available" || got[1].DaysLeft == nil || *got[1].DaysLeft != 0 {
		t.Fatalf("listener-scoped certificate state: %+v", got)
	}
	data, err := json.Marshal(got[1])
	if err != nil || !strings.Contains(string(data), `"days_left":0`) {
		t.Fatalf("zero-day expiry omitted: %s (%v)", data, err)
	}
	if len(got[1].Variants) != 2 || got[1].Variants[0].NotAfter == "" || got[1].Variants[0].CleanupWarning != "cleanup pending" ||
		got[1].Variants[1].Algorithm != "rsa-2048" || got[1].Variants[1].State != "failed" ||
		got[1].Variants[1].Error != "DNS update refused" {
		t.Fatalf("certificate variant state was not projected: %+v", got[1].Variants)
	}
	if risk := projectCertRisk(got); risk == nil || risk.Errors != 2 || !strings.Contains(risk.Details, "errors") {
		t.Fatalf("pending and failed renewal must be reported as risks: %+v", risk)
	}
}

func TestProjectTLSRiskCountsRecentlyExpiredCertificates(t *testing.T) {
	cfg := &config.Config{Servers: []config.ServerConfig{{
		Listen: ":443", ServerNames: []string{"example.test"}, TLS: &config.TLSConfig{Enabled: true},
	}}}
	certs := projectTLS(cfg, cfg, []CertStatus{{
		Listen: ":443", ServerNames: []string{"example.test"}, NotAfter: time.Now().Add(-time.Hour),
	}})
	risk := projectCertRisk(certs)
	if len(certs) != 1 || certs[0].DaysLeft == nil || *certs[0].DaysLeft != -1 ||
		risk == nil || risk.Expired != 1 || !strings.Contains(risk.Details, "1 expired") {
		t.Fatalf("recently expired certificate risk: certs=%+v risk=%+v", certs, risk)
	}
}

func TestProjectRoutesIncludesRouteIDWhenPresent(t *testing.T) {
	id := "r-abc123"
	c := &config.Config{Servers: []config.ServerConfig{{
		Listen: ":8080",
		Locations: []config.LocationConfig{
			{Match: config.MatchConfig{Type: "prefix", Path: "/"}, ProxyPass: "http://127.0.0.1:3000"},
			{Match: config.MatchConfig{Type: "prefix", Path: "/api"}, ProxyPass: "http://127.0.0.1:3001", RouteID: &id},
		},
	}}}
	locs := projectRoutes(c)[0].Locations
	if locs[0].RouteID != "" {
		t.Errorf("route without a route_id should project an empty RouteID, got %q", locs[0].RouteID)
	}
	if locs[1].RouteID != id {
		t.Errorf("RouteID = %q, want %q", locs[1].RouteID, id)
	}
}

func TestProjectRoutesIncludesWebSocketUpstreamProtocol(t *testing.T) {
	c := &config.Config{Servers: []config.ServerConfig{{
		Listen: ":8080",
		Locations: []config.LocationConfig{{
			Match:     config.MatchConfig{Type: "prefix", Path: "/ws"},
			ProxyPass: "https://inventory", WebSocketUpstreamProtocol: "http3",
		}},
	}}}
	got := projectRoutes(c)[0].Locations[0].WebSocketUpstreamProtocol
	if got != "http3" {
		t.Fatalf("WebSocketUpstreamProtocol = %q, want http3", got)
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("a", "b"); got != "a" {
		t.Errorf("firstNonEmpty(a,b) = %q", got)
	}
	if got := firstNonEmpty("", "b"); got != "b" {
		t.Errorf("firstNonEmpty(\"\",b) = %q", got)
	}
	if got := firstNonEmpty("", ""); got != "" {
		t.Errorf("firstNonEmpty(\"\",\"\") = %q", got)
	}
}

func TestLocationAuthState(t *testing.T) {
	// basic
	b := locationAuthState(&config.AuthConfig{
		Allow: []string{"127.0.0.1"},
		Basic: &config.BasicAuthConfig{File: "/etc/passwd", Realm: "admin"},
	})
	if b.Method != "basic" || b.BasicFile != "/etc/passwd" || b.BasicRealm != "admin" {
		t.Errorf("basic = %+v", b)
	}
	// jwt
	j := locationAuthState(&config.AuthConfig{
		JWT: &config.JWTAuthConfig{JWKSURL: "https://idp/jwks"},
	})
	if j.Method != "jwt" || j.JWTJWKSURL != "https://idp/jwks" {
		t.Errorf("jwt = %+v", j)
	}
	// forward
	f := locationAuthState(&config.AuthConfig{
		ForwardAuth: &config.ForwardAuthConfig{URL: "http://auth/"},
	})
	if f.Method != "forward" || f.ForwardURL != "http://auth/" {
		t.Errorf("forward = %+v", f)
	}
	// cidr-only
	c := locationAuthState(&config.AuthConfig{
		Allow: []string{"10.0.0.0/8"},
	})
	if c.Method != "cidr" {
		t.Errorf("cidr = %+v", c)
	}
}

func TestRlStr(t *testing.T) {
	if got := rlStr(nil); got != "(none)" {
		t.Errorf("rlStr(nil) = %q", got)
	}
	if got := rlStr(&config.RateLimitConfig{Rate: 10, Burst: 20}); got != "key=ip, rate=10/s, burst=20" {
		t.Errorf("rlStr(default) = %q", got)
	}
	if got := rlStr(&config.RateLimitConfig{Key: "header:X-Token", Rate: 5, Burst: 5}); got != "key=header:X-Token, rate=5/s, burst=5" {
		t.Errorf("rlStr(custom) = %q", got)
	}
}

func TestIntsStr(t *testing.T) {
	if got := intsStr(nil); got != "" {
		t.Errorf("intsStr(nil) = %q", got)
	}
	if got := intsStr([]int{200, 204}); got != "200,204" {
		t.Errorf("intsStr(200,204) = %q", got)
	}
}

func TestProjectRoutesGRPCTranscodeTarget(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.ServerConfig{{
			Listen: ":8080",
			Locations: []config.LocationConfig{{
				Match: config.MatchConfig{Type: "prefix", Path: "/api"},
				GRPCTranscode: &config.GRPCTranscodeConfig{
					Target:        "grpc://backend:50051",
					DescriptorSet: "./api.pb",
				},
			}},
		}},
	}
	routes := projectRoutes(cfg)
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
	loc := routes[0].Locations[0]
	if loc.Action != "grpc_transcode" {
		t.Errorf("action = %q, want grpc_transcode", loc.Action)
	}
	if loc.Target != "grpc://backend:50051" {
		t.Errorf("target = %q, want grpc://backend:50051", loc.Target)
	}
}

func TestProjectRoutesResponseHeadersAndCORS(t *testing.T) {
	maxAge := config.Duration(10 * 60 * 1e9)
	cfg := &config.Config{
		Servers: []config.ServerConfig{{
			Listen: ":8080",
			Locations: []config.LocationConfig{
				{
					Match:  config.MatchConfig{Type: "prefix", Path: "/a"},
					Return: 200,
					ResponseHeaders: []config.ResponseHeaderOp{
						{Op: "set", Name: "X-Frame-Options", Value: adminStrPtr("DENY")},
					},
					CORS: &config.CORSConfig{
						Enabled:          true,
						AllowedOrigins:   []string{"https://app.example.test"},
						AllowedMethods:   []string{"GET", "POST"},
						AllowedHeaders:   []string{"Content-Type"},
						ExposedHeaders:   []string{"X-Request-Id"},
						AllowCredentials: true,
						MaxAge:           &maxAge,
					},
				},
				{Match: config.MatchConfig{Type: "prefix", Path: "/b"}, Return: 200},
			},
		}},
	}
	routes := projectRoutes(cfg)
	locs := routes[0].Locations

	if !locs[0].ResponseHeaders {
		t.Error("locs[0].ResponseHeaders = false, want true")
	}
	if locs[0].CORS == nil {
		t.Fatal("locs[0].CORS = nil, want populated")
	}
	cs := locs[0].CORS
	if !cs.Enabled || !cs.AllowCredentials {
		t.Errorf("CORS state = %+v, want enabled+credentials", cs)
	}
	if len(cs.AllowedOrigins) != 1 || cs.AllowedOrigins[0] != "https://app.example.test" {
		t.Errorf("AllowedOrigins = %v", cs.AllowedOrigins)
	}
	if len(cs.AllowedMethods) != 2 || len(cs.AllowedHeaders) != 1 || len(cs.ExposedHeaders) != 1 {
		t.Errorf("CORS state = %+v", cs)
	}
	if cs.MaxAge != "10m0s" {
		t.Errorf("MaxAge = %q, want 10m0s", cs.MaxAge)
	}

	if locs[1].ResponseHeaders {
		t.Error("locs[1].ResponseHeaders = true, want false (no response_headers configured)")
	}
	if locs[1].CORS != nil {
		t.Errorf("locs[1].CORS = %+v, want nil (no cors configured)", locs[1].CORS)
	}
}

func adminStrPtr(s string) *string { return &s }

// poolVerdict is what the Console and any API consumer read to decide whether a
// pool is in trouble. The case that matters is partial degradation: one healthy
// backend must not mask a tripped one, and "not yet observed" must not be
// reported as "failing".
func TestPoolVerdict(t *testing.T) {
	b := func(states ...string) []BackendProjection {
		out := make([]BackendProjection, 0, len(states))
		for i, s := range states {
			out = append(out, BackendProjection{Address: string(rune('a'+i)) + ":80", State: s})
		}
		return out
	}
	cases := []struct {
		name     string
		backends []BackendProjection
		want     string
	}{
		{"no backends at all", nil, "unknown"},
		{"none observed yet", b("", ""), "unknown"},
		{"all available", b("available", "available"), "healthy"},
		{"one tripped alongside a healthy one", b("available", "circuit_open"), "degraded"},
		{"one at capacity", b("available", "at_capacity"), "degraded"},
		{"probing counts as not available", b("available", "circuit_half_open"), "degraded"},
		{"every backend tripped", b("circuit_open", "health_unhealthy"), "down"},
		{"observed failure plus an unobserved backend", b("", "circuit_open"), "down"},
		{"observed success plus an unobserved backend", b("", "available"), "healthy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := poolVerdict(tc.backends); got != tc.want {
				t.Errorf("poolVerdict = %q, want %q", got, tc.want)
			}
		})
	}
}
