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
