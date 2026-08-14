// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"strings"
	"testing"
)

// discoveryConfig returns a valid config whose single upstream resolves through
// d, so the discovery-trust rules can be exercised in isolation.
func discoveryConfig(d *DiscoveryConfig) *Config {
	cfg := validKnownValueConfig()
	cfg.Upstreams = []UpstreamConfig{{Name: "app", Discovery: d}}
	return cfg
}

func consulDiscovery(mutate func(*ConsulDiscovery)) *DiscoveryConfig {
	c := &ConsulDiscovery{Address: "https://consul.internal:8501", Service: "app"}
	if mutate != nil {
		mutate(c)
	}
	return &DiscoveryConfig{Type: "consul", Consul: c}
}

// TestLintFlagsDiscoveryVerificationBypass pins the Boundary F half of the
// escape-hatch contract: disabling verification of the registry that supplies a
// pool's addresses is an error, exactly as disabling it on the pool itself is.
// The safety of Boundary D depends on it — a poisoned answer is only harmless
// because the address never becomes an identity, which assumes the answer came
// from the intended authority.
func TestLintFlagsDiscoveryVerificationBypass(t *testing.T) {
	for _, tt := range []struct {
		name  string
		disco *DiscoveryConfig
		field string
	}{
		{
			name:  "kubernetes api server",
			disco: &DiscoveryConfig{Type: "kubernetes", Kubernetes: &KubernetesDiscovery{Namespace: "ns", Service: "app", InsecureSkipTLSVerify: true}},
			field: "kubernetes.insecure_skip_tls_verify",
		},
		{
			name: "consul agent",
			disco: consulDiscovery(func(c *ConsulDiscovery) {
				c.TLS = &BackendTLSConfig{InsecureSkipVerify: true}
			}),
			field: "consul.tls.insecure_skip_verify",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var found bool
			for _, d := range Lint(discoveryConfig(tt.disco)) {
				if !strings.Contains(d.Field, tt.field) {
					continue
				}
				found = true
				if d.Severity != SeverityError {
					t.Errorf("severity = %v, want %v so `jul lint` exits non-zero without -strict", d.Severity, SeverityError)
				}
			}
			if !found {
				t.Fatalf("no diagnostic for %s", tt.field)
			}
		})
	}
}

func TestLintFlagsConsulTokenOverPlaintext(t *testing.T) {
	for _, tt := range []struct {
		name    string
		address string
		token   string
		want    bool
	}{
		{name: "token over http", address: "http://127.0.0.1:8500", token: "secret", want: true},
		{name: "token over the default address", address: "", token: "secret", want: true},
		{name: "token over https", address: "https://consul.internal:8501", token: "secret", want: false},
		{name: "no token", address: "http://127.0.0.1:8500", want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			disco := consulDiscovery(func(c *ConsulDiscovery) {
				c.Address, c.Token = tt.address, tt.token
			})
			var found bool
			for _, d := range Lint(discoveryConfig(disco)) {
				// Match the message, not just the field: the literal-secret
				// lint also reports consul.token, and that one is not this rule.
				if strings.Contains(d.Message, "readable and replayable") {
					found = true
				}
			}
			if found != tt.want {
				t.Fatalf("token diagnostic = %v, want %v", found, tt.want)
			}
		})
	}
}

func TestValidateConsulTLSUsesTheSharedRules(t *testing.T) {
	// The block is the same type as [upstreams.backend_tls], so its
	// self-contradictory combinations are rejected by the same validator.
	disco := consulDiscovery(func(c *ConsulDiscovery) {
		c.TLS = &BackendTLSConfig{InsecureSkipVerify: true, PeerIdentities: []string{"dns:consul.internal"}}
	})
	requireValidationError(t, discoveryConfig(disco), "consul.tls")

	ok := consulDiscovery(func(c *ConsulDiscovery) {
		c.TLS = &BackendTLSConfig{CAMode: "system", ServerName: "consul.internal"}
	})
	if err := Validate(discoveryConfig(ok)); err != nil {
		t.Fatalf("Validate rejected a usable consul TLS block: %v", err)
	}
}
