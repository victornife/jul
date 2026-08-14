// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"strings"
	"testing"
)

func clientAddressConfig(policy *ClientAddressConfig) *Config {
	cfg := validKnownValueConfig()
	cfg.Servers[0].ClientAddress = policy
	return cfg
}

func TestValidateAcceptsClientAddressPolicies(t *testing.T) {
	tests := []struct {
		name   string
		policy *ClientAddressConfig
	}{
		{name: "omitted", policy: nil},
		{name: "empty block", policy: &ClientAddressConfig{}},
		{name: "ipv4 and ipv6 prefixes", policy: &ClientAddressConfig{TrustedProxies: []string{"10.0.0.0/8", "2001:db8:100::/48"}, ForwardedHeaders: []string{"x-forwarded-for"}}},
		{name: "single host", policy: &ClientAddressConfig{TrustedProxies: []string{"192.0.2.10"}, ForwardedHeaders: []string{"x-forwarded-for"}}},
		{name: "explicit header order", policy: &ClientAddressConfig{ForwardedHeaders: []string{"x-forwarded-for", "forwarded"}}},
		{name: "headers disabled", policy: &ClientAddressConfig{ForwardedHeaders: []string{}}},
		{name: "trusted proxies with headers explicitly disabled", policy: &ClientAddressConfig{TrustedProxies: []string{"10.0.0.0/8"}, ForwardedHeaders: []string{}}},
		{name: "trusted proxies opting into forwarded", policy: &ClientAddressConfig{TrustedProxies: []string{"10.0.0.0/8"}, ForwardedHeaders: []string{"forwarded"}}},
		{name: "max hops at the limit", policy: &ClientAddressConfig{MaxHops: 255}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Validate(clientAddressConfig(tt.policy)); err != nil {
				t.Fatalf("Validate rejected a valid policy: %v", err)
			}
		})
	}
}

func TestValidateRejectsInvalidClientAddressValues(t *testing.T) {
	tests := []struct {
		name   string
		policy *ClientAddressConfig
		want   string
	}{
		{
			name:   "hostname",
			policy: &ClientAddressConfig{TrustedProxies: []string{"proxy.example.com"}},
			want:   "servers[0].client_address.trusted_proxies[0]",
		},
		{
			name:   "host bits set",
			policy: &ClientAddressConfig{TrustedProxies: []string{"10.0.0.0/8", "192.0.2.5/24"}},
			want:   "servers[0].client_address.trusted_proxies[1]",
		},
		{
			name:   "cidr shorthand",
			policy: &ClientAddressConfig{TrustedProxies: []string{"private"}},
			want:   "want an IP address or CIDR prefix",
		},
		{
			name:   "unsupported header",
			policy: &ClientAddressConfig{ForwardedHeaders: []string{"x-real-ip"}},
			want:   "servers[0].client_address.forwarded_headers[0]: invalid value",
		},
		{
			name:   "non-canonical header case",
			policy: &ClientAddressConfig{ForwardedHeaders: []string{"Forwarded"}},
			want:   "expected forwarded or x-forwarded-for",
		},
		{
			name:   "empty header",
			policy: &ClientAddressConfig{ForwardedHeaders: []string{""}},
			want:   "servers[0].client_address.forwarded_headers[0]",
		},
		{
			name:   "duplicate header",
			policy: &ClientAddressConfig{ForwardedHeaders: []string{"forwarded", "forwarded"}},
			want:   "duplicate header",
		},
		{
			name:   "negative max hops",
			policy: &ClientAddressConfig{MaxHops: -1},
			want:   "servers[0].client_address.max_hops",
		},
		{
			name:   "max hops beyond the bound",
			policy: &ClientAddressConfig{MaxHops: 256},
			want:   "must be at most 255",
		},
		{
			name:   "trusted proxies without forwarded headers",
			policy: &ClientAddressConfig{TrustedProxies: []string{"10.0.0.0/8"}},
			want:   "forwarded_headers: required when trusted_proxies is set",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireValidationError(t, clientAddressConfig(tt.policy), tt.want)
		})
	}
}

func TestValidateRequiresOneClientAddressPolicyPerListenAddress(t *testing.T) {
	base := func(policies ...*ClientAddressConfig) *Config {
		cfg := validKnownValueConfig()
		cfg.Servers = nil
		for i, policy := range policies {
			srv := validKnownValueConfig().Servers[0]
			srv.Listen = "127.0.0.1:8080"
			srv.ServerNames = []string{[]string{"public.example.com", "internal.example.com"}[i%2]}
			srv.ClientAddress = policy
			cfg.Servers = append(cfg.Servers, srv)
		}
		return cfg
	}

	trusted := &ClientAddressConfig{TrustedProxies: []string{"10.0.0.0/8"}, ForwardedHeaders: []string{"x-forwarded-for"}}

	t.Run("identical policies are accepted", func(t *testing.T) {
		if err := Validate(base(trusted, trusted)); err != nil {
			t.Fatalf("Validate rejected identical sibling policies: %v", err)
		}
	})

	t.Run("reordered and duplicated prefixes are the same policy", func(t *testing.T) {
		cfg := base(
			&ClientAddressConfig{TrustedProxies: []string{"10.0.0.0/8", "192.0.2.0/24"}, ForwardedHeaders: []string{"x-forwarded-for"}},
			&ClientAddressConfig{TrustedProxies: []string{"192.0.2.0/24", "10.0.0.0/8", "10.0.0.0/8"}, ForwardedHeaders: []string{"x-forwarded-for"}},
		)
		if err := Validate(cfg); err != nil {
			t.Fatalf("Validate rejected an equivalent sibling policy: %v", err)
		}
	})

	t.Run("spelled-out defaults match an omitted block", func(t *testing.T) {
		cfg := base(nil, &ClientAddressConfig{ForwardedHeaders: []string{"x-forwarded-for"}, MaxHops: 16})
		if err := Validate(cfg); err != nil {
			t.Fatalf("Validate rejected a spelled-out default policy: %v", err)
		}
	})

	t.Run("a sibling without the block is rejected", func(t *testing.T) {
		requireValidationError(t, base(trusted, nil), "must declare the same policy")
	})

	t.Run("divergent trusted proxies are rejected", func(t *testing.T) {
		cfg := base(trusted, &ClientAddressConfig{TrustedProxies: []string{"192.168.0.0/16"}, ForwardedHeaders: []string{"x-forwarded-for"}})
		requireValidationError(t, cfg, "client identity is derived per listen address")
	})

	t.Run("divergent header order is rejected", func(t *testing.T) {
		cfg := base(
			&ClientAddressConfig{TrustedProxies: []string{"10.0.0.0/8"}, ForwardedHeaders: []string{"forwarded", "x-forwarded-for"}},
			&ClientAddressConfig{TrustedProxies: []string{"10.0.0.0/8"}, ForwardedHeaders: []string{"x-forwarded-for", "forwarded"}},
		)
		requireValidationError(t, cfg, "must declare the same policy")
	})

	t.Run("divergent max hops is rejected", func(t *testing.T) {
		cfg := base(trusted, &ClientAddressConfig{TrustedProxies: []string{"10.0.0.0/8"}, ForwardedHeaders: []string{"x-forwarded-for"}, MaxHops: 4})
		requireValidationError(t, cfg, "must declare the same policy")
	})

	t.Run("a different listen address is unaffected", func(t *testing.T) {
		cfg := base(trusted, trusted)
		cfg.Servers[1].Listen = "127.0.0.1:9090"
		cfg.Servers[1].ClientAddress = nil
		if err := Validate(cfg); err != nil {
			t.Fatalf("Validate rejected distinct policies on distinct listeners: %v", err)
		}
	})
}

func TestParseClientAddressPolicy(t *testing.T) {
	doc := `
[[servers]]
listen = "127.0.0.1:8080"

[servers.client_address]
trusted_proxies   = ["10.0.0.0/8", "2001:db8:100::/48"]
forwarded_headers = ["forwarded", "x-forwarded-for"]
max_hops          = 8

[[servers.locations]]
proxy_pass = "http://127.0.0.1:3000"
[servers.locations.match]
type = "prefix"
path = "/"
`
	cfg, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	policy := cfg.Servers[0].ClientAddress
	if policy == nil {
		t.Fatal("client_address was not decoded")
	}
	if len(policy.TrustedProxies) != 2 || policy.TrustedProxies[0] != "10.0.0.0/8" {
		t.Errorf("trusted_proxies = %v", policy.TrustedProxies)
	}
	if len(policy.ForwardedHeaders) != 2 || policy.ForwardedHeaders[1] != "x-forwarded-for" {
		t.Errorf("forwarded_headers = %v", policy.ForwardedHeaders)
	}
	if policy.MaxHops != 8 {
		t.Errorf("max_hops = %d, want 8", policy.MaxHops)
	}
}

func TestParseClientAddressDistinguishesOmittedFromEmptyHeaderList(t *testing.T) {
	omitted, err := Parse([]byte("[[servers]]\nlisten = \"127.0.0.1:8080\"\n[servers.client_address]\ntrusted_proxies = [\"10.0.0.0/8\"]\n"))
	if err != nil {
		t.Fatalf("Parse omitted list: %v", err)
	}
	if omitted.Servers[0].ClientAddress.ForwardedHeaders != nil {
		t.Fatalf("omitted forwarded_headers decoded as %#v, want nil so the default applies", omitted.Servers[0].ClientAddress.ForwardedHeaders)
	}

	empty, err := Parse([]byte("[[servers]]\nlisten = \"127.0.0.1:8080\"\n[servers.client_address]\nforwarded_headers = []\n"))
	if err != nil {
		t.Fatalf("Parse empty list: %v", err)
	}
	headers := empty.Servers[0].ClientAddress.ForwardedHeaders
	if headers == nil || len(headers) != 0 {
		t.Fatalf("empty forwarded_headers decoded as %#v, want a non-nil empty slice so it disables both headers", headers)
	}
}

func TestParseRejectsUnknownClientAddressFields(t *testing.T) {
	tests := []struct {
		name     string
		doc      string
		want     string
		wantKind string
	}{
		{
			name: "misspelled leaf",
			doc:  "[[servers]]\nlisten = \"127.0.0.1:8080\"\n[servers.client_address]\ntrusted_proxys = [\"10.0.0.0/8\"]\n",
			want: "trusted_proxys",
		},
		{
			name: "rejected neighbour",
			doc:  "[[servers]]\nlisten = \"127.0.0.1:8080\"\n[servers.client_address]\ntrusted_proxies = [\"10.0.0.0/8\"]\nreal_ip_header = \"x-real-ip\"\n",
			want: "real_ip_header",
		},
		{
			name: "old draft block name",
			doc:  "[[servers]]\nlisten = \"127.0.0.1:8080\"\n[servers.client_identity]\ntrusted_proxies = [\"10.0.0.0/8\"]\n",
			want: "client_identity",
			// The strict decoder reports an unknown *table* as a missing table
			// rather than an unknown field; both are rejections.
			wantKind: "missing table",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Parse([]byte(tt.doc))
			if err == nil {
				t.Fatalf("Parse accepted unknown field %q: %#v", tt.want, cfg)
			}
			kind := tt.wantKind
			if kind == "" {
				kind = "unknown field"
			}
			if !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), kind) {
				t.Fatalf("error = %q, want a %q error naming %q", err, kind, tt.want)
			}
		})
	}
}

func TestLintFlagsTrustedProxiesCoveringEveryClient(t *testing.T) {
	for _, tt := range []struct {
		name  string
		entry string
		want  bool
	}{
		{name: "ipv4 default route", entry: "0.0.0.0/0", want: true},
		{name: "ipv6 default route", entry: "::/0", want: true},
		{name: "narrow range", entry: "10.0.0.0/8", want: false},
		{name: "single host", entry: "192.0.2.10", want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := clientAddressConfig(&ClientAddressConfig{TrustedProxies: []string{tt.entry}})
			var found bool
			for _, d := range Lint(cfg) {
				if strings.Contains(d.Field, "client_address.trusted_proxies") {
					found = true
					if !strings.Contains(d.Message, "trusts every client") {
						t.Errorf("diagnostic message = %q", d.Message)
					}
				}
			}
			if found != tt.want {
				t.Fatalf("lint diagnostic for %q = %v, want %v", tt.entry, found, tt.want)
			}
		})
	}
}

func TestValidateProxyProtocolOnHTTPListeners(t *testing.T) {
	withProxyProto := func(mode string, ca *ClientAddressConfig, http3 bool) *Config {
		cfg := clientAddressConfig(ca)
		cfg.Servers[0].ProxyProtocol = mode
		if http3 {
			cfg.Servers[0].TLS = &TLSConfig{Enabled: true, Cert: "c.pem", Key: "k.pem"}
			cfg.Servers[0].HTTP3 = &HTTP3Config{Enabled: true}
		}
		return cfg
	}
	trusted := &ClientAddressConfig{TrustedProxies: []string{"10.0.0.0/8"}, ForwardedHeaders: []string{"x-forwarded-for"}}

	t.Run("a declared balancer is accepted", func(t *testing.T) {
		if err := Validate(withProxyProto("in", trusted, false)); err != nil {
			t.Fatalf("Validate rejected a declared balancer: %v", err)
		}
	})

	t.Run("ingest requires a trusted proxy set", func(t *testing.T) {
		requireValidationError(t, withProxyProto("in", nil, false), "requires client_address.trusted_proxies")
	})

	t.Run("an empty trusted proxy set is not enough", func(t *testing.T) {
		requireValidationError(t, withProxyProto("in", &ClientAddressConfig{}, false), "requires client_address.trusted_proxies")
	})

	t.Run("emitting a header is rejected", func(t *testing.T) {
		for _, mode := range []string{"out", "both"} {
			requireValidationError(t, withProxyProto(mode, trusted, false), "only ingests a header")
		}
	})

	t.Run("http3 on the same listener is rejected", func(t *testing.T) {
		requireValidationError(t, withProxyProto("in", trusted, true), "cannot be combined with http3")
	})

	t.Run("blocks sharing a listener must agree", func(t *testing.T) {
		cfg := withProxyProto("in", trusted, false)
		sibling := cfg.Servers[0]
		sibling.ServerNames = []string{"other.example.com"}
		sibling.ProxyProtocol = ""
		cfg.Servers = append(cfg.Servers, sibling)
		requireValidationError(t, cfg, "must agree")
	})
}

func TestLintFlagsForwardedHeaderOptIn(t *testing.T) {
	for _, tt := range []struct {
		name   string
		policy *ClientAddressConfig
		want   bool
	}{
		{
			name:   "forwarded alone behind a trusted proxy",
			policy: &ClientAddressConfig{TrustedProxies: []string{"10.0.0.0/8"}, ForwardedHeaders: []string{"forwarded"}},
			want:   true,
		},
		{
			name:   "forwarded alongside xff is still flagged",
			policy: &ClientAddressConfig{TrustedProxies: []string{"10.0.0.0/8"}, ForwardedHeaders: []string{"forwarded", "x-forwarded-for"}},
			want:   true,
		},
		{
			name:   "xff alone is clean",
			policy: &ClientAddressConfig{TrustedProxies: []string{"10.0.0.0/8"}, ForwardedHeaders: []string{"x-forwarded-for"}},
			want:   false,
		},
		{
			name:   "without a trusted proxy no header is believed",
			policy: &ClientAddressConfig{ForwardedHeaders: []string{"forwarded"}},
			want:   false,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var found bool
			for _, d := range Lint(clientAddressConfig(tt.policy)) {
				if !strings.Contains(d.Field, "client_address.forwarded_headers") {
					continue
				}
				found = true
				if d.Severity != SeverityWarning {
					t.Errorf("severity = %v, want %v", d.Severity, SeverityWarning)
				}
				if !strings.Contains(d.Message, "most proxies never write it") {
					t.Errorf("diagnostic message = %q", d.Message)
				}
			}
			if found != tt.want {
				t.Fatalf("forwarded lint diagnostic = %v, want %v", found, tt.want)
			}
		})
	}
}
