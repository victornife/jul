// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"testing"

	"jul/internal/config"
)

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
