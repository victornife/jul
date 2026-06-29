package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidRateKey(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{"ip", true},
		{"header:X-Request-ID", true},
		{"jwt:sub", true},
		{"jwt:email", true},
		{"header:", false},
		{"jwt:", false},
		{"cookie:session", false},
		{"ip-something", false},
		{"", false},
	}
	for _, tc := range cases {
		got := ValidRateKey(tc.key)
		if got != tc.want {
			t.Errorf("ValidRateKey(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}

func TestDiscoveryEnabled(t *testing.T) {
	if discoveryEnabled(nil) {
		t.Error("nil discovery should be disabled")
	}
	if discoveryEnabled(&DiscoveryConfig{Type: ""}) {
		t.Error("empty type should be disabled")
	}
	if discoveryEnabled(&DiscoveryConfig{Type: "static"}) {
		t.Error("static should be disabled")
	}
	if !discoveryEnabled(&DiscoveryConfig{Type: "dns"}) {
		t.Error("dns should be enabled")
	}
	if !discoveryEnabled(&DiscoveryConfig{Type: "consul"}) {
		t.Error("consul should be enabled")
	}
	if !discoveryEnabled(&DiscoveryConfig{Type: "kubernetes"}) {
		t.Error("kubernetes should be enabled")
	}
}

func TestValidJWTAlg(t *testing.T) {
	valid := []string{"RS256", "RS384", "RS512", "ES256", "ES384", "ES512", "PS256", "PS384", "PS512"}
	for _, alg := range valid {
		if !validJWTAlg(alg) {
			t.Errorf("validJWTAlg(%q) should be true", alg)
		}
	}
	invalid := []string{"none", "HS256", "HS384", "HS512", "RSA", "EdDSA", "", "rs256"}
	for _, alg := range invalid {
		if validJWTAlg(alg) {
			t.Errorf("validJWTAlg(%q) should be false", alg)
		}
	}
}

func TestValidateStreamTarget(t *testing.T) {
	upstreamNames := map[string]int{"api": 1, "db": 1}

	cases := []struct {
		name    string
		target  string
		wantErr bool
	}{
		{"empty", "", true},
		{"known upstream", "api", false},
		{"host:port", "127.0.0.1:3000", false},
		{"ipv6 host:port", "[::1]:8080", false},
		{"bad address", "not-a-target", true},
	}

	for _, tc := range cases {
		errs := validateStreamTarget(tc.target, upstreamNames, "test")
		if tc.wantErr {
			if len(errs) == 0 {
				t.Errorf("%s: expected error", tc.name)
			}
		} else {
			if len(errs) > 0 {
				t.Errorf("%s: unexpected error: %v", tc.name, errs)
			}
		}
	}
}

func TestValidateWAF(t *testing.T) {
	base := func(w WAFConfig) *Config {
		return &Config{
			Servers: []ServerConfig{{
				Listen: "127.0.0.1:80",
				Locations: []LocationConfig{{
					Match:     MatchConfig{Type: "prefix", Path: "/"},
					ProxyPass: "http://127.0.0.1:3000",
					WAF:       &w,
				}},
			}},
		}
	}

	t.Run("disabled is no-op", func(t *testing.T) {
		cfg := base(WAFConfig{Enabled: false, Mode: "garbage"})
		if err := Validate(cfg); err != nil {
			t.Errorf("disabled WAF should not be validated: %v", err)
		}
	})

	t.Run("valid block", func(t *testing.T) {
		cfg := base(WAFConfig{Enabled: true, Mode: "block", BlockStatus: 403, CRSEnabled: true})
		if err := Validate(cfg); err != nil {
			t.Errorf("valid WAF rejected: %v", err)
		}
	})

	t.Run("valid detect mode", func(t *testing.T) {
		cfg := base(WAFConfig{Enabled: true, Mode: "detect", BlockStatus: 403, InlineRules: "SecRuleEngine On"})
		if err := Validate(cfg); err != nil {
			t.Errorf("valid detect mode rejected: %v", err)
		}
	})

	t.Run("invalid mode", func(t *testing.T) {
		cfg := base(WAFConfig{Enabled: true, Mode: "aggressive", BlockStatus: 403, CRSEnabled: true})
		if err := Validate(cfg); err == nil {
			t.Error("expected error for invalid WAF mode")
		} else if !strings.Contains(err.Error(), "mode") {
			t.Errorf("error = %q, want containing 'mode'", err.Error())
		}
	})

	t.Run("negative paranoia", func(t *testing.T) {
		cfg := base(WAFConfig{Enabled: true, Paranoia: -1, BlockStatus: 403, CRSEnabled: true})
		err := Validate(cfg)
		if err == nil || !strings.Contains(err.Error(), "paranoia") {
			t.Fatalf("expected paranoia error, got %v", err)
		}
	})

	t.Run("paranoia too high", func(t *testing.T) {
		cfg := base(WAFConfig{Enabled: true, Paranoia: 5, BlockStatus: 403, CRSEnabled: true})
		err := Validate(cfg)
		if err == nil || !strings.Contains(err.Error(), "paranoia") {
			t.Fatalf("expected paranoia error, got %v", err)
		}
	})

	t.Run("negative request_body_limit", func(t *testing.T) {
		cfg := base(WAFConfig{Enabled: true, RequestBodyLimit: -1, BlockStatus: 403, CRSEnabled: true})
		err := Validate(cfg)
		if err == nil || !strings.Contains(err.Error(), "request_body_limit") {
			t.Fatalf("expected request_body_limit error, got %v", err)
		}
	})

	t.Run("global waf valid", func(t *testing.T) {
		cfg := &Config{
			Servers: []ServerConfig{{Listen: "127.0.0.1:80"}},
			WAF:     WAFConfig{Enabled: true, Mode: "block", BlockStatus: 403, InlineRules: "SecAction id:1"},
		}
		if err := Validate(cfg); err != nil {
			t.Errorf("global WAF rejected: %v", err)
		}
	})

	t.Run("global waf invalid mode", func(t *testing.T) {
		cfg := &Config{
			Servers: []ServerConfig{{Listen: "127.0.0.1:80"}},
			WAF:     WAFConfig{Enabled: true, Mode: "UFO", BlockStatus: 403, CRSEnabled: true},
		}
		if err := Validate(cfg); err == nil {
			t.Error("expected error for global WAF invalid mode")
		}
	})
}

func TestValidateClientAuth(t *testing.T) {
	tmp := t.TempDir()
	goodCA := filepath.Join(tmp, "ca.pem")
	badCA := filepath.Join(tmp, "bad")
	os.WriteFile(goodCA, []byte("cert"), 0644)
	os.Mkdir(badCA, 0755)
	goodCRL := filepath.Join(tmp, "crl.pem")
	os.WriteFile(goodCRL, []byte("crl"), 0644)

	cases := []struct {
		name        string
		ca          *ClientAuthConfig
		wantErr     bool
		wantContain string
	}{
		{"nil", nil, false, ""},
		{"none mode", &ClientAuthConfig{Mode: "none"}, false, ""},
		{"valid request", &ClientAuthConfig{Mode: "request", CAFile: goodCA}, false, ""},
		{"valid require", &ClientAuthConfig{Mode: "require", CAFile: goodCA}, false, ""},
		{"unknown mode", &ClientAuthConfig{Mode: "demand"}, true, "invalid mode"},
		{"request missing ca", &ClientAuthConfig{Mode: "request"}, true, "ca_file is required"},
		{"missing ca file", &ClientAuthConfig{Mode: "require", CAFile: filepath.Join(tmp, "missing.pem")}, true, "not readable"},
		{"ca is directory", &ClientAuthConfig{Mode: "require", CAFile: badCA}, true, "is a directory"},
		{"missing crl file", &ClientAuthConfig{Mode: "require", CAFile: goodCA, CRLFile: filepath.Join(tmp, "missing-crl.pem")}, true, "not readable"},
		{"crl is directory", &ClientAuthConfig{Mode: "require", CAFile: goodCA, CRLFile: badCA}, true, "is a directory"},
		{"valid with crl", &ClientAuthConfig{Mode: "require", CAFile: goodCA, CRLFile: goodCRL}, false, ""},
		{"empty mode treated as none", &ClientAuthConfig{}, false, ""},
	}

	for _, tc := range cases {
		errs := validateClientAuth(tc.ca, "test.ca")
		if tc.wantErr {
			if len(errs) == 0 {
				t.Errorf("%s: expected error", tc.name)
				continue
			}
			if tc.wantContain != "" && !strings.Contains(errs[0].Error(), tc.wantContain) {
				t.Errorf("%s: error = %q, want containing %q", tc.name, errs[0].Error(), tc.wantContain)
			}
		} else {
			if len(errs) > 0 {
				t.Errorf("%s: unexpected error: %v", tc.name, errs)
			}
		}
	}
}

func TestValidateAdminAuditNegative(t *testing.T) {
	base := func(a AdminConfig) *Config {
		return &Config{
			Servers: []ServerConfig{{Listen: "127.0.0.1:80"}},
			Admin:   a,
		}
	}
	t.Run("audit_log_rotate_max_mb negative", func(t *testing.T) {
		err := Validate(base(AdminConfig{Enabled: true, Listen: "127.0.0.1:9000", AuditLogRotateMaxMB: -1}))
		if err == nil || !strings.Contains(err.Error(), "audit_log_rotate_max_mb") {
			t.Fatalf("expected audit_log_rotate_max_mb error, got %v", err)
		}
	})
	t.Run("audit_log_rotate_keep negative", func(t *testing.T) {
		err := Validate(base(AdminConfig{Enabled: true, Listen: "127.0.0.1:9000", AuditLogRotateKeep: -1}))
		if err == nil || !strings.Contains(err.Error(), "audit_log_rotate_keep") {
			t.Fatalf("expected audit_log_rotate_keep error, got %v", err)
		}
	})
}

func TestValidateTLSPortConflicts(t *testing.T) {
	t.Run("tls and plain on same listen", func(t *testing.T) {
		cfg := &Config{
			Servers: []ServerConfig{
				{Listen: "127.0.0.1:443", TLS: &TLSConfig{Enabled: true, Cert: "c", Key: "k"}, ServerNames: []string{"a.com"}},
				{Listen: "127.0.0.1:443"},
			},
		}
		err := Validate(cfg)
		if err == nil || !strings.Contains(err.Error(), "used by both TLS and non-TLS") {
			t.Fatalf("expected TLS/plain conflict error, got %v", err)
		}
	})

	t.Run("acme and static tls on same listen", func(t *testing.T) {
		cfg := &Config{
			Servers: []ServerConfig{
				{
					Listen:      "127.0.0.1:443",
					ServerNames: []string{"example.com"},
					TLS: &TLSConfig{
						Enabled: true,
						ACME: &ACMEConfig{
							Enabled:  true,
							Email:    "a@b.com",
							Domains:  []string{"example.com"},
							CacheDir: "./c",
						},
					},
				},
				{
					Listen: "127.0.0.1:443",
					TLS:    &TLSConfig{Enabled: true, Cert: "c", Key: "k"},
				},
			},
		}
		err := Validate(cfg)
		if err == nil || !strings.Contains(err.Error(), "mixes ACME and static TLS") {
			t.Fatalf("expected ACME/static TLS conflict error, got %v", err)
		}
	})
}

func TestValidateCompressionEmpty(t *testing.T) {
	base := func(c CompressionConfig) *Config {
		return &Config{
			Servers:     []ServerConfig{{Listen: "127.0.0.1:80"}},
			Compression: c,
		}
	}
	t.Run("enabled no encoders", func(t *testing.T) {
		err := Validate(base(CompressionConfig{Enabled: true, Types: []string{"text/*"}}))
		if err == nil || !strings.Contains(err.Error(), "no encoders") {
			t.Fatalf("expected no encoders error, got %v", err)
		}
	})
	t.Run("enabled no types", func(t *testing.T) {
		err := Validate(base(CompressionConfig{Enabled: true, Encoders: []string{"gzip"}}))
		if err == nil || !strings.Contains(err.Error(), "no MIME types") {
			t.Fatalf("expected no types error, got %v", err)
		}
	})
}

func TestValidateLocationRewritesAndCache(t *testing.T) {
	base := func(loc LocationConfig) *Config {
		return &Config{
			Servers: []ServerConfig{{
				Listen:    "127.0.0.1:80",
				Locations: []LocationConfig{loc},
			}},
		}
	}

	t.Run("invalid rewrite pattern", func(t *testing.T) {
		err := Validate(base(LocationConfig{
			Match:     MatchConfig{Type: "prefix", Path: "/"},
			ProxyPass: "http://127.0.0.1:3000",
			Rewrites:  []RewriteConfig{{Pattern: "[invalid"}},
		}))
		if err == nil || !strings.Contains(err.Error(), "invalid pattern") {
			t.Fatalf("expected invalid pattern error, got %v", err)
		}
	})

	t.Run("invalid rewrite flag", func(t *testing.T) {
		err := Validate(base(LocationConfig{
			Match:     MatchConfig{Type: "prefix", Path: "/"},
			ProxyPass: "http://127.0.0.1:3000",
			Rewrites:  []RewriteConfig{{Pattern: "^/old$", Replacement: "/new", Flag: "bogus"}},
		}))
		if err == nil || !strings.Contains(err.Error(), "invalid flag") {
			t.Fatalf("expected invalid flag error, got %v", err)
		}
	})

	t.Run("cache with root rejected", func(t *testing.T) {
		err := Validate(base(LocationConfig{
			Match: MatchConfig{Type: "prefix", Path: "/"},
			Root:  "/var/www",
			Cache: true,
		}))
		if err == nil || !strings.Contains(err.Error(), "cache applies to proxy") {
			t.Fatalf("expected cache+root error, got %v", err)
		}
	})

	t.Run("cache with proxy_pass valid", func(t *testing.T) {
		err := Validate(base(LocationConfig{
			Match:     MatchConfig{Type: "prefix", Path: "/"},
			ProxyPass: "http://127.0.0.1:3000",
			Cache:     true,
		}))
		if err != nil {
			t.Errorf("cache with proxy_pass rejected: %v", err)
		}
	})
}

func TestValidateUpstreamStrategyAndName(t *testing.T) {
	base := func(upstream UpstreamConfig) *Config {
		return &Config{
			Servers:   []ServerConfig{{Listen: "127.0.0.1:80"}},
			Upstreams: []UpstreamConfig{upstream},
		}
	}

	t.Run("missing upstream name", func(t *testing.T) {
		err := Validate(base(UpstreamConfig{Name: "", Servers: []UpstreamServer{{Address: "127.0.0.1:3000"}}}))
		if err == nil || !strings.Contains(err.Error(), "name' is required") {
			t.Fatalf("expected missing name error, got %v", err)
		}
	})

	t.Run("duplicate upstream name", func(t *testing.T) {
		err := Validate(&Config{
			Servers: []ServerConfig{{Listen: "127.0.0.1:80"}},
			Upstreams: []UpstreamConfig{
				{Name: "api", Servers: []UpstreamServer{{Address: "127.0.0.1:3000"}}},
				{Name: "api", Servers: []UpstreamServer{{Address: "127.0.0.1:3001"}}},
			},
		})
		if err == nil || !strings.Contains(err.Error(), "duplicate upstream name") {
			t.Fatalf("expected duplicate name error, got %v", err)
		}
	})

	t.Run("invalid strategy", func(t *testing.T) {
		err := Validate(base(UpstreamConfig{Name: "api", Strategy: "magic", Servers: []UpstreamServer{{Address: "127.0.0.1:3000"}}}))
		if err == nil || !strings.Contains(err.Error(), "invalid strategy") {
			t.Fatalf("expected invalid strategy error, got %v", err)
		}
	})

	t.Run("upstream with empty server address", func(t *testing.T) {
		err := Validate(base(UpstreamConfig{Name: "api", Servers: []UpstreamServer{{Address: ""}}}))
		if err == nil || !strings.Contains(err.Error(), "address is required") {
			t.Fatalf("expected empty address error, got %v", err)
		}
	})
}

func TestValidateRedirectHTTPS(t *testing.T) {
	base := func(code int) *Config {
		return &Config{
			Servers: []ServerConfig{{Listen: "127.0.0.1:80", RedirectHTTPS: code}},
		}
	}

	t.Run("valid 301", func(t *testing.T) {
		if err := Validate(base(301)); err != nil {
			t.Errorf("redirect_https=301 rejected: %v", err)
		}
	})

	t.Run("valid 308", func(t *testing.T) {
		if err := Validate(base(308)); err != nil {
			t.Errorf("redirect_https=308 rejected: %v", err)
		}
	})

	t.Run("valid 0", func(t *testing.T) {
		if err := Validate(base(0)); err != nil {
			t.Errorf("redirect_https=0 rejected: %v", err)
		}
	})

	t.Run("invalid code", func(t *testing.T) {
		err := Validate(base(302))
		if err == nil || !strings.Contains(err.Error(), "redirect_https must be 301 or 308") {
			t.Fatalf("expected redirect_https error, got %v", err)
		}
	})
}
