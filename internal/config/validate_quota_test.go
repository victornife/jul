// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"fmt"
	"strings"
	"testing"
)

func quotaTestPolicy() QuotaPolicyConfig {
	return QuotaPolicyConfig{
		Mode:       "enforce",
		Rate:       10,
		Burst:      10,
		MaxCallers: 128,
		Classes: map[string]QuotaClassConfig{
			"public": {
				Shared:    QuotaLimitConfig{Rate: 3, Burst: 3},
				PerCaller: QuotaLimitConfig{Rate: 2, Burst: 2},
			},
			"trusted": {
				Shared:    QuotaLimitConfig{Rate: 3, Burst: 3},
				PerCaller: QuotaLimitConfig{Rate: 2, Burst: 2},
				Principals: []QuotaPrincipalMatch{{
					Method: "jwt", Domain: "staff", Issuer: "https://issuer.example", Subject: "alice",
				}},
			},
			"network": {
				Shared:    QuotaLimitConfig{Rate: 4, Burst: 4},
				PerCaller: QuotaLimitConfig{Rate: 2, Burst: 2},
			},
		},
		Pools: map[string]QuotaPoolConfig{
			"office": {Class: "network", CIDRs: []string{"192.0.2.0/24", "2001:db8::/32", "::ffff:198.51.100.0/120"}},
		},
		Operations: map[string]QuotaOperationConfig{
			"export": {
				Shared:    QuotaLimitConfig{Rate: 1, Burst: 1},
				PerCaller: QuotaLimitConfig{Rate: 1, Burst: 1},
			},
		},
	}
}

func validQuotaConfig() *Config {
	auth := &AuthConfig{
		JWT:            &JWTAuthConfig{Issuer: "https://issuer.example", Audience: "api"},
		QuotaPrincipal: &AuthQuotaPrincipalConfig{Domain: "staff"},
	}
	return &Config{
		Servers: []ServerConfig{{Locations: []LocationConfig{{
			QuotaPolicy: "app", QuotaOperation: "export", Auth: auth,
		}}}},
		QuotaPolicies: map[string]QuotaPolicyConfig{"app": quotaTestPolicy()},
	}
}

func quotaValidationErrors(c *Config) string {
	var messages []string
	for _, err := range validateQuotaPolicies(c) {
		messages = append(messages, err.Error())
	}
	return strings.Join(messages, "\n")
}

func TestQuotaPolicyValidation(t *testing.T) {
	t.Run("valid explicit identity and mapped IPv6 prefix", func(t *testing.T) {
		if got := quotaValidationErrors(validQuotaConfig()); got != "" {
			t.Fatalf("valid quota config rejected: %s", got)
		}
		prefix, err := quotaPrefix("::ffff:198.51.100.0/120")
		if err != nil || prefix.String() != "198.51.100.0/24" {
			t.Fatalf("mapped prefix = %v, %v", prefix, err)
		}
	})

	tests := []struct {
		name string
		edit func(*Config)
		want string
	}{
		{"missing public class", func(c *Config) { delete(c.QuotaPolicies["app"].Classes, "public") }, "public fallback"},
		{"allocation exceeds app", func(c *Config) {
			p := c.QuotaPolicies["app"]
			class := p.Classes["trusted"]
			class.Shared.Rate = 6
			p.Classes["trusted"] = class
			c.QuotaPolicies["app"] = p
		}, "allocations exceed"},
		{"public class cannot be protected identity", func(c *Config) {
			p := c.QuotaPolicies["app"]
			class := p.Classes["public"]
			class.Principals = []QuotaPrincipalMatch{{Method: "basic", Domain: "staff", Subject: "alice"}}
			p.Classes["public"] = class
			c.QuotaPolicies["app"] = p
		}, "cannot be selected by identity"},
		{"unknown operation", func(c *Config) { c.Servers[0].Locations[0].QuotaOperation = "missing" }, "unknown operation"},
		{"operation without policy", func(c *Config) {
			c.Servers[0].Locations[0].QuotaPolicy = ""
		}, "requires quota_policy"},
		{"caller state resource bound", func(c *Config) {
			p := c.QuotaPolicies["app"]
			p.MaxCallers = 6000
			c.QuotaPolicies["app"] = p
		}, "caller stores across enforcement and observation"},
		{"invalid prefix", func(c *Config) {
			p := c.QuotaPolicies["app"]
			pool := p.Pools["office"]
			pool.CIDRs = []string{"not-a-cidr"}
			p.Pools["office"] = pool
			c.QuotaPolicies["app"] = p
		}, "invalid CIDR"},
		{"same prefix assigned twice", func(c *Config) {
			p := c.QuotaPolicies["app"]
			pool := p.Pools["office"]
			pool.CIDRs = append(pool.CIDRs, "192.0.2.0/24")
			p.Pools["office"] = pool
			c.QuotaPolicies["app"] = p
		}, "already assigned"},
		{"CIDR cannot select identity-reserved class", func(c *Config) {
			p := c.QuotaPolicies["app"]
			pool := p.Pools["office"]
			pool.Class = "trusted"
			p.Pools["office"] = pool
			c.QuotaPolicies["app"] = p
		}, "reserved for verified principals"},
		{"JWT mapping without attached identity source", func(c *Config) {
			c.Servers[0].Locations[0].Auth.QuotaPrincipal = nil
		}, "no attached route authenticates"},
		{"duplicate principal mapping", func(c *Config) {
			p := c.QuotaPolicies["app"]
			class := p.Classes["public"]
			class.Principals = []QuotaPrincipalMatch{{Method: "jwt", Domain: "staff", Issuer: "https://issuer.example", Subject: "alice"}}
			p.Classes["public"] = class
			c.QuotaPolicies["app"] = p
		}, "already assigned"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validQuotaConfig()
			tt.edit(cfg)
			if got := quotaValidationErrors(cfg); !strings.Contains(got, tt.want) {
				t.Fatalf("validation errors %q do not contain %q", got, tt.want)
			}
		})
	}
}

func TestQuotaPolicyValidationBoundsAndInvalidFields(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Config)
		want string
	}{
		{
			name: "invalid policy identity and limits",
			edit: func(c *Config) {
				p := c.QuotaPolicies["app"]
				p.Mode = "disabled"
				p.Rate = 0
				p.MaxCallers = maxQuotaCallers + 1
				delete(c.QuotaPolicies, "app")
				c.QuotaPolicies["bad.name"] = p
			},
			want: "quota_policies.bad.name.max_callers",
		},
		{
			name: "class name and limits",
			edit: func(c *Config) {
				p := c.QuotaPolicies["app"]
				p.Classes["bad.name"] = QuotaClassConfig{
					Shared:    QuotaLimitConfig{Rate: 0, Burst: 1},
					PerCaller: QuotaLimitConfig{Rate: 1, Burst: 0},
				}
				c.QuotaPolicies["app"] = p
			},
			want: "invalid class name",
		},
		{
			name: "principal is invalid and over the mapping cap",
			edit: func(c *Config) {
				p := c.QuotaPolicies["app"]
				class := p.Classes["trusted"]
				class.Principals = []QuotaPrincipalMatch{{Method: "invalid", Domain: "staff", Subject: "alice"}}
				for i := 0; i < maxQuotaMappings; i++ {
					class.Principals = append(class.Principals, QuotaPrincipalMatch{
						Method: "jwt", Domain: "staff", Issuer: "issuer", Subject: fmt.Sprintf("subject-%d", i),
					})
				}
				p.Classes["trusted"] = class
				c.QuotaPolicies["app"] = p
			},
			want: "at most 256 principal mappings",
		},
		{
			name: "invalid pool class and empty CIDRs",
			edit: func(c *Config) {
				p := c.QuotaPolicies["app"]
				p.Pools["office"] = QuotaPoolConfig{Class: "unknown"}
				c.QuotaPolicies["app"] = p
			},
			want: `unknown class "unknown"`,
		},
		{
			name: "invalid pool name and no CIDRs",
			edit: func(c *Config) {
				p := c.QuotaPolicies["app"]
				p.Pools["bad.name"] = QuotaPoolConfig{Class: "network"}
				c.QuotaPolicies["app"] = p
			},
			want: "invalid pool name",
		},
		{
			name: "operation name limits and app ceiling",
			edit: func(c *Config) {
				p := c.QuotaPolicies["app"]
				p.Operations["bad.name"] = QuotaOperationConfig{
					Shared:    QuotaLimitConfig{Rate: maxQuotaRate + 1, Burst: 1},
					PerCaller: QuotaLimitConfig{Rate: 1, Burst: 1},
				}
				c.QuotaPolicies["app"] = p
			},
			want: "invalid operation name",
		},
		{
			name: "unknown route policy",
			edit: func(c *Config) {
				c.Servers[0].Locations[0].QuotaPolicy = "missing"
			},
			want: "unknown policy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validQuotaConfig()
			tt.edit(cfg)
			if got := quotaValidationErrors(cfg); !strings.Contains(got, tt.want) {
				t.Fatalf("validation errors %q do not contain %q", got, tt.want)
			}
		})
	}

	t.Run("policy limit caps", func(t *testing.T) {
		cfg := &Config{QuotaPolicies: make(map[string]QuotaPolicyConfig)}
		for i := 0; i <= maxQuotaPolicies; i++ {
			cfg.QuotaPolicies[fmt.Sprintf("policy-%d", i)] = QuotaPolicyConfig{
				Mode: "enforce", Rate: 1, Burst: 1, MaxCallers: 1,
				Classes: map[string]QuotaClassConfig{
					"public": {Shared: QuotaLimitConfig{Rate: 1, Burst: 1}, PerCaller: QuotaLimitConfig{Rate: 1, Burst: 1}},
				},
			}
		}
		if got := quotaValidationErrors(cfg); !strings.Contains(got, "at most 8 policies") {
			t.Fatalf("policy count validation errors %q", got)
		}
	})

	t.Run("class operation and pool caps", func(t *testing.T) {
		cfg := validQuotaConfig()
		p := cfg.QuotaPolicies["app"]
		for i := 0; i < maxQuotaClasses; i++ {
			p.Classes[fmt.Sprintf("extra-%d", i)] = QuotaClassConfig{}
			p.Operations[fmt.Sprintf("extra-%d", i)] = QuotaOperationConfig{}
		}
		for i := 0; i < maxQuotaPools; i++ {
			p.Pools[fmt.Sprintf("extra-%d", i)] = QuotaPoolConfig{Class: "network", CIDRs: []string{fmt.Sprintf("10.1.%d.0/24", i)}}
		}
		cfg.QuotaPolicies["app"] = p
		got := quotaValidationErrors(cfg)
		for _, want := range []string{"at most 8 classes", "at most 8 operations", "at most 64 CIDR pools"} {
			if !strings.Contains(got, want) {
				t.Errorf("validation errors %q do not contain %q", got, want)
			}
		}
	})

	t.Run("prefix cap and mapped prefix bounds", func(t *testing.T) {
		cfg := validQuotaConfig()
		p := cfg.QuotaPolicies["app"]
		pool := p.Pools["office"]
		for i := 0; i < maxQuotaPrefixes; i++ {
			pool.CIDRs = append(pool.CIDRs, fmt.Sprintf("10.%d.%d.0/24", i/256, i%256))
		}
		p.Pools["office"] = pool
		cfg.QuotaPolicies["app"] = p
		if got := quotaValidationErrors(cfg); !strings.Contains(got, "at most 256 CIDR prefixes") {
			t.Fatalf("prefix count validation errors %q", got)
		}
		if _, err := quotaPrefix("not-a-prefix"); err == nil {
			t.Fatal("invalid CIDR prefix unexpectedly parsed")
		}
		if _, err := quotaPrefix("::ffff:0:0/95"); err == nil || !strings.Contains(err.Error(), "at least /96") {
			t.Fatalf("broad mapped prefix error = %v", err)
		}
	})
}

func TestQuotaPrincipalAuthValidation(t *testing.T) {
	tests := []struct {
		name string
		auth AuthConfig
		want string
	}{
		{"valid Basic", AuthConfig{Basic: &BasicAuthConfig{}, QuotaPrincipal: &AuthQuotaPrincipalConfig{Domain: "staff"}}, ""},
		{"valid JWT", AuthConfig{JWT: &JWTAuthConfig{Issuer: "issuer", Audience: "api"}, QuotaPrincipal: &AuthQuotaPrincipalConfig{Domain: "staff"}}, ""},
		{"valid forward", AuthConfig{ForwardAuth: &ForwardAuthConfig{}, QuotaPrincipal: &AuthQuotaPrincipalConfig{Domain: "staff", ResponseHeader: "X-Quota-User"}}, ""},
		{"Basic response header", AuthConfig{Basic: &BasicAuthConfig{}, QuotaPrincipal: &AuthQuotaPrincipalConfig{Domain: "staff", ResponseHeader: "X-User"}}, "only with forward_auth"},
		{"JWT missing audience", AuthConfig{JWT: &JWTAuthConfig{Issuer: "issuer"}, QuotaPrincipal: &AuthQuotaPrincipalConfig{Domain: "staff"}}, "explicit issuer and audience"},
		{"forward missing response header", AuthConfig{ForwardAuth: &ForwardAuthConfig{}, QuotaPrincipal: &AuthQuotaPrincipalConfig{Domain: "staff"}}, "valid HTTP header"},
		{"forward hop-by-hop response header", AuthConfig{ForwardAuth: &ForwardAuthConfig{}, QuotaPrincipal: &AuthQuotaPrincipalConfig{Domain: "staff", ResponseHeader: "Connection"}}, "hop-by-hop"},
		{"no credential method", AuthConfig{QuotaPrincipal: &AuthQuotaPrincipalConfig{Domain: "staff"}}, "exactly one credential method"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateAuthQuotaPrincipal(&tt.auth, "auth")
			if tt.want == "" {
				if len(errs) != 0 {
					t.Fatalf("unexpected validation errors: %v", errs)
				}
				return
			}
			for _, err := range errs {
				if strings.Contains(err.Error(), tt.want) {
					return
				}
			}
			t.Fatalf("validation errors %v do not contain %q", errs, tt.want)
		})
	}
}

func TestQuotaPrincipalSourceAndAuthValidationEdges(t *testing.T) {
	principal := QuotaPrincipalMatch{Method: "basic", Domain: "staff", Subject: "alice"}
	if err := validateQuotaPrincipalMatch(principal); err != nil {
		t.Fatalf("valid Basic principal rejected: %v", err)
	}
	for _, tt := range []struct {
		name      string
		principal QuotaPrincipalMatch
		want      string
	}{
		{"method", QuotaPrincipalMatch{Method: "other", Domain: "staff", Subject: "alice"}, "method must be"},
		{"domain whitespace", QuotaPrincipalMatch{Method: "basic", Domain: " staff", Subject: "alice"}, "domain must be"},
		{"empty subject", QuotaPrincipalMatch{Method: "basic", Domain: "staff"}, "subject must be"},
		{"missing JWT issuer", QuotaPrincipalMatch{Method: "jwt", Domain: "staff", Subject: "alice"}, "issuer must be"},
		{"issuer on Basic", QuotaPrincipalMatch{Method: "basic", Domain: "staff", Issuer: "issuer", Subject: "alice"}, "only for JWT"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateQuotaPrincipalMatch(tt.principal); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateQuotaPrincipalMatch() error = %v, want %q", err, tt.want)
			}
		})
	}

	for _, tt := range []struct {
		name string
		auth AuthConfig
		want string
	}{
		{"invalid domain", AuthConfig{Basic: &BasicAuthConfig{}, QuotaPrincipal: &AuthQuotaPrincipalConfig{Domain: " staff"}}, "domain: must be nonempty"},
		{"multiple methods", AuthConfig{Basic: &BasicAuthConfig{}, JWT: &JWTAuthConfig{}, QuotaPrincipal: &AuthQuotaPrincipalConfig{Domain: "staff"}}, "exactly one credential method"},
		{"JWT response header", AuthConfig{JWT: &JWTAuthConfig{Issuer: "issuer", Audience: "api"}, QuotaPrincipal: &AuthQuotaPrincipalConfig{Domain: "staff", ResponseHeader: "X-User"}}, "only with forward_auth"},
		{"invalid forward header", AuthConfig{ForwardAuth: &ForwardAuthConfig{}, QuotaPrincipal: &AuthQuotaPrincipalConfig{Domain: "staff", ResponseHeader: "bad header"}}, "valid HTTP header"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateAuthQuotaPrincipal(&tt.auth, "auth")
			for _, err := range errs {
				if strings.Contains(err.Error(), tt.want) {
					return
				}
			}
			t.Fatalf("validation errors %v do not contain %q", errs, tt.want)
		})
	}

	for _, tc := range []struct {
		name      string
		auth      *AuthConfig
		principal QuotaPrincipalMatch
		want      bool
	}{
		{"Basic match", &AuthConfig{Basic: &BasicAuthConfig{}, QuotaPrincipal: &AuthQuotaPrincipalConfig{Domain: "staff"}}, QuotaPrincipalMatch{Method: "basic", Domain: "staff", Subject: "alice"}, true},
		{"forward match", &AuthConfig{ForwardAuth: &ForwardAuthConfig{}, QuotaPrincipal: &AuthQuotaPrincipalConfig{Domain: "staff"}}, QuotaPrincipalMatch{Method: "forward", Domain: "staff", Subject: "alice"}, true},
		{"JWT issuer mismatch", &AuthConfig{JWT: &JWTAuthConfig{Issuer: "issuer", Audience: "api"}, QuotaPrincipal: &AuthQuotaPrincipalConfig{Domain: "staff"}}, QuotaPrincipalMatch{Method: "jwt", Domain: "staff", Issuer: "other", Subject: "alice"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isQuotaPrincipalSource(tc.auth, tc.principal); got != tc.want {
				t.Fatalf("isQuotaPrincipalSource() = %v, want %v", got, tc.want)
			}
		})
	}
}
