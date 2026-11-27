// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
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
