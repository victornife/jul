// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func dnsACMEConfig() *Config {
	s := acmeBlock(":443", "ops@example.com", "letsencrypt-staging", "dns-01", "./certs")
	s.ServerNames = []string{"example.com", "*.example.com"}
	s.TLS.ACME.Domains = append([]string(nil), s.ServerNames...)
	s.TLS.ACME.DNSProvider = "rfc2136"
	s.TLS.ACME.RFC2136 = &RFC2136Config{
		Server: "127.0.0.1:53", Observers: []string{"127.0.0.1:53"}, Zone: "example.com", TSIGKeyName: "jul.example.com",
		TSIGSecret: "${env:JUL_TEST_TSIG}",
	}
	return &Config{Servers: []ServerConfig{s}}
}

func TestRFC2136DelegationEquality(t *testing.T) {
	first := &RFC2136Config{CNAMEDelegations: map[string]RFC2136CNAMEDelegation{
		"_acme-challenge.example.com": {Target: "txt.example.com", Observers: []string{"ns.example.com:53"}},
	}}
	second := &RFC2136Config{CNAMEDelegations: map[string]RFC2136CNAMEDelegation{
		"_acme-challenge.example.com": {Target: "txt.example.com", Observers: []string{"ns.example.com:53"}},
	}}
	if !equalRFC2136(first, second) {
		t.Fatal("equal delegations should share a manager")
	}
	second.CNAMEDelegations["_acme-challenge.example.com"] = RFC2136CNAMEDelegation{Target: "txt.example.com", Observers: []string{"other.example.com:53"}}
	if equalRFC2136(first, second) {
		t.Fatal("different alias observers must not share a manager")
	}
	delete(second.CNAMEDelegations, "_acme-challenge.example.com")
	if equalRFC2136(first, second) {
		t.Fatal("missing delegation must not share a manager")
	}
}

func TestDNS01ListenerNameLimitAcrossBlocks(t *testing.T) {
	cfg := dnsACMEConfig()
	first := &cfg.Servers[0]
	first.ServerNames = []string{"name0.example.com"}
	first.TLS.ACME.Domains = nil
	for i := 0; i < 60; i++ {
		first.TLS.ACME.Domains = append(first.TLS.ACME.Domains, fmt.Sprintf("name%d.example.com", i))
	}
	second := acmeBlock(":443", "ops@example.com", "letsencrypt-staging", "dns-01", "./certs")
	second.ServerNames = []string{"name60.example.com"}
	second.TLS.ACME.DNSProvider = first.TLS.ACME.DNSProvider
	second.TLS.ACME.Domains = nil
	copyRFC := *first.TLS.ACME.RFC2136
	second.TLS.ACME.RFC2136 = &copyRFC
	for i := 60; i < 101; i++ {
		second.TLS.ACME.Domains = append(second.TLS.ACME.Domains, fmt.Sprintf("name%d.example.com", i))
	}
	cfg.Servers = append(cfg.Servers, second)
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "at most 100 certificate names per listener") {
		t.Fatalf("combined listener name limit: %v", err)
	}
}

func TestDNS01Configuration(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Config)
		want string
	}{
		{"valid apex and wildcard", func(*Config) {}, ""},
		{"unknown provider", func(c *Config) { c.Servers[0].TLS.ACME.DNSProvider = "unknown" }, "dns_provider"},
		{"missing provider settings", func(c *Config) { c.Servers[0].TLS.ACME.RFC2136 = nil }, "rfc2136 block"},
		{"missing secret", func(c *Config) { c.Servers[0].TLS.ACME.RFC2136.TSIGSecret = "" }, "secret reference"},
		{"literal secret", func(c *Config) { c.Servers[0].TLS.ACME.RFC2136.TSIGSecret = "literal-secret" }, "single ${env:"},
		{"mixed secret", func(c *Config) { c.Servers[0].TLS.ACME.RFC2136.TSIGSecret = "prefix${env:JUL_TEST_TSIG}" }, "single ${env:"},
		{"bad server", func(c *Config) { c.Servers[0].TLS.ACME.RFC2136.Server = "example.com" }, "numeric port"},
		{"missing observers", func(c *Config) { c.Servers[0].TLS.ACME.RFC2136.Observers = nil }, "observers must contain"},
		{"too many observers", func(c *Config) { c.Servers[0].TLS.ACME.RFC2136.Observers = make([]string, MaxRFC2136Observers+1) }, "observers must contain"},
		{"bad observer", func(c *Config) { c.Servers[0].TLS.ACME.RFC2136.Observers = []string{"dns.example.com"} }, "each observer"},
		{"duplicate observers", func(c *Config) {
			c.Servers[0].TLS.ACME.RFC2136.Observers = []string{"DNS.example.com:53", "dns.example.com:53"}
		}, "duplicates"},
		{"valid cname", func(c *Config) {
			c.Servers[0].TLS.ACME.RFC2136.CNAMEDelegations = map[string]RFC2136CNAMEDelegation{"_acme-challenge.example.com": {Target: "validation.example.com", Observers: []string{"127.0.0.1:53"}}}
		}, ""},
		{"cname outside source zone", func(c *Config) {
			dns := c.Servers[0].TLS.ACME.RFC2136
			dns.Zone = "validation.example.net"
			dns.CNAMEDelegations = map[string]RFC2136CNAMEDelegation{"_acme-challenge.example.com": {Target: "txt.validation.example.net", Observers: []string{"127.0.0.1:53"}}}
		}, ""},
		{"outside cname target", func(c *Config) {
			c.Servers[0].TLS.ACME.RFC2136.CNAMEDelegations = map[string]RFC2136CNAMEDelegation{"_acme-challenge.example.com": {Target: "validation.other.test", Observers: []string{"127.0.0.1:53"}}}
		}, "CNAME target must be inside"},
		{"self cname target", func(c *Config) {
			c.Servers[0].TLS.ACME.RFC2136.CNAMEDelegations = map[string]RFC2136CNAMEDelegation{"_acme-challenge.example.com": {Target: "_acme-challenge.example.com", Observers: []string{"127.0.0.1:53"}}}
		}, "distinct DNS target"},
		{"duplicate cname owner", func(c *Config) {
			c.Servers[0].TLS.ACME.RFC2136.CNAMEDelegations = map[string]RFC2136CNAMEDelegation{
				"_acme-challenge.example.com":  {Target: "one.example.com", Observers: []string{"127.0.0.1:53"}},
				"_ACME-CHALLENGE.example.com.": {Target: "two.example.com", Observers: []string{"127.0.0.1:53"}},
			}
		}, "duplicate challenge owner"},
		{"missing alias observers", func(c *Config) {
			c.Servers[0].TLS.ACME.RFC2136.CNAMEDelegations = map[string]RFC2136CNAMEDelegation{"_acme-challenge.example.com": {Target: "validation.example.com"}}
		}, "CNAME observers must contain"},
		{"invalid alias observer", func(c *Config) {
			c.Servers[0].TLS.ACME.RFC2136.CNAMEDelegations = map[string]RFC2136CNAMEDelegation{"_acme-challenge.example.com": {Target: "validation.example.com", Observers: []string{"bad"}}}
		}, "each CNAME observer"},
		{"duplicate alias observers", func(c *Config) {
			c.Servers[0].TLS.ACME.RFC2136.CNAMEDelegations = map[string]RFC2136CNAMEDelegation{"_acme-challenge.example.com": {Target: "validation.example.com", Observers: []string{"DNS.example.com:53", "dns.example.com:53"}}}
		}, "CNAME observers must not contain duplicates"},
		{"invalid zone", func(c *Config) { c.Servers[0].TLS.ACME.RFC2136.Zone = "invalid..example.com" }, "zone must be a DNS zone name"},
		{"invalid TSIG key name", func(c *Config) { c.Servers[0].TLS.ACME.RFC2136.TSIGKeyName = "-invalid.example.com" }, "tsig_key_name must be a DNS key name"},
		{"outside zone", func(c *Config) { c.Servers[0].TLS.ACME.RFC2136.Zone = "other.example" }, "outside the configured zone"},
		{"invalid wildcard", func(c *Config) { c.Servers[0].TLS.ACME.Domains[1] = "*.*.example.com" }, "invalid DNS-01"},
		{"wildcard does not cover apex", func(c *Config) { c.Servers[0].TLS.ACME.Domains = []string{"*.example.com"} }, "not covered"},
		{"wildcard does not cover deeper name", func(c *Config) { c.Servers[0].ServerNames = []string{"a.b.example.com"} }, "not covered"},
		{"missing SNI scope", func(c *Config) { c.Servers[0].ServerNames = nil }, "requires server_names"},
		{"unresolved name references", func(c *Config) {
			c.Servers[0].ServerNames = []string{"${env:JUL_TEST_NAME}"}
			c.Servers[0].TLS.ACME.Domains = []string{"${env:JUL_TEST_NAME}"}
		}, ""},
		{"too many certificate names", func(c *Config) {
			for i := 0; i < 101; i++ {
				c.Servers[0].TLS.ACME.Domains = append(c.Servers[0].TLS.ACME.Domains, fmt.Sprintf("name%d.example.com", i))
			}
		}, "at most 100 certificate names per listener"},
		{"timeout bounded", func(c *Config) { c.Servers[0].TLS.ACME.RFC2136.PropagationTimeout = Duration(16 * time.Minute) }, "propagation_timeout"},
		{"poll bounded", func(c *Config) { c.Servers[0].TLS.ACME.RFC2136.PollInterval = Duration(time.Millisecond) }, "poll_interval"},
		{"provider on http challenge", func(c *Config) { c.Servers[0].TLS.ACME.Challenge = "http-01" }, "require challenge"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := dnsACMEConfig()
			tt.edit(cfg)
			err := Validate(cfg)
			if tt.want == "" && acmeBuildEnabled {
				if err != nil {
					t.Fatalf("valid DNS-01 config: %v", err)
				}
				return
			}
			want := tt.want
			if want == "" {
				want = "requires a binary built with the acme tag"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %v, want %q", err, want)
			}
			if strings.Contains(err.Error(), "literal-secret") {
				t.Fatal("validation exposed a credential")
			}
		})
	}
}

func TestDNS01ResolvedCredentialValidation(t *testing.T) {
	for _, secret := range []string{"not-base64", "YWJjZA=="} {
		t.Run(secret, func(t *testing.T) {
			t.Setenv("JUL_TEST_TSIG", secret)
			candidate, err := NewCandidate(dnsACMEConfig())
			if err != nil {
				t.Fatal(err)
			}
			err = Validate(candidate.Effective)
			if err == nil || !strings.Contains(err.Error(), "resolved TSIG secret must be base64-encoded and at least 16 bytes") {
				t.Fatalf("invalid resolved credential was not rejected: %v", err)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatal("validation exposed a resolved credential")
			}
		})
	}
}

func TestDNS01SecretResolutionPreservesReferenceProvenance(t *testing.T) {
	t.Setenv("JUL_TEST_TSIG", "MDEyMzQ1Njc4OWFiY2RlZg==")
	raw := dnsACMEConfig()
	candidate, err := NewCandidate(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := candidate.Raw.Servers[0].TLS.ACME.RFC2136.TSIGSecret; got != "${env:JUL_TEST_TSIG}" {
		t.Fatalf("raw reference changed: %q", got)
	}
	effective := candidate.Effective.Servers[0].TLS.ACME.RFC2136
	if !effective.secretResolved || effective.TSIGSecret == raw.Servers[0].TLS.ACME.RFC2136.TSIGSecret {
		t.Fatal("resolved credential lost its provenance")
	}
	clone, err := candidate.Effective.Clone()
	if err != nil {
		t.Fatal(err)
	}
	if !clone.Servers[0].TLS.ACME.RFC2136.secretResolved {
		t.Fatal("clone lost credential provenance")
	}
	raw.Servers[0].TLS.ACME.RFC2136.TSIGSecret = "literal-secret"
	if _, err := NewCandidate(raw); err == nil || strings.Contains(err.Error(), "literal-secret") {
		t.Fatalf("literal credential must be rejected without disclosure: %v", err)
	}
}
