// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"strings"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/lifecycle"
)

// listenerBindFingerprint enumerates the properties bind() freezes on a
// listener. That enumeration is an implementation detail of the socket, but it
// must not become a second lifecycle inventory: every property it compares has
// to correspond to a configuration path the lifecycle registry already
// classifies as listener-bound.
//
// bindFingerprintPaths is that correspondence, written down once and verified
// behaviorally below: each entry mutates exactly one field and the test asserts
// both that the registry classifies its path as listener-bound and that the
// fingerprint of a kept address actually changes.
var bindFingerprintPaths = []struct {
	path   string
	mutate func(*config.Config)
}{
	{"rate_limit.max_conns", func(c *config.Config) {
		c.RateLimit = config.RateLimitConfig{Enabled: true, Key: "ip", Rate: 1, Burst: 1, MaxConns: 7}
	}},
	{"servers.*.read_header_timeout", func(c *config.Config) {
		c.Servers[0].ReadHeaderTimeout = config.Duration(3 * time.Second)
	}},
	{"servers.*.read_timeout", func(c *config.Config) {
		c.Servers[0].ReadTimeout = config.Duration(3 * time.Second)
	}},
	{"servers.*.write_timeout", func(c *config.Config) {
		c.Servers[0].WriteTimeout = config.Duration(3 * time.Second)
	}},
	{"servers.*.idle_timeout", func(c *config.Config) {
		c.Servers[0].IdleTimeout = config.Duration(3 * time.Second)
	}},
	{"servers.*.max_header_bytes", func(c *config.Config) {
		c.Servers[0].MaxHeaderBytes = config.Size(4096)
	}},
	{"servers.*.proxy_protocol", func(c *config.Config) {
		c.Servers[0].ProxyProtocol = "in"
	}},
	{"servers.*.tls.enabled", func(c *config.Config) {
		c.Servers[0].TLS.Enabled = false
	}},
	{"servers.*.tls.min_version", func(c *config.Config) {
		c.Servers[0].TLS.MinVersion = "1.3"
	}},
	{"servers.*.tls.client_auth.mode", func(c *config.Config) {
		c.Servers[0].TLS.ClientAuth = &config.ClientAuthConfig{Mode: "require", CAFile: "/ca.pem"}
	}},
	{"servers.*.tls.client_auth.ca_file", func(c *config.Config) {
		c.Servers[0].TLS.ClientAuth = &config.ClientAuthConfig{Mode: "request", CAFile: "/other-ca.pem"}
	}},
	{"servers.*.tls.client_auth.verify_san", func(c *config.Config) {
		c.Servers[0].TLS.ClientAuth = &config.ClientAuthConfig{Mode: "request", CAFile: "/ca.pem", VerifySAN: []string{"a.example"}}
	}},
	{"servers.*.tls.client_auth.crl_file", func(c *config.Config) {
		c.Servers[0].TLS.ClientAuth = &config.ClientAuthConfig{Mode: "request", CAFile: "/ca.pem", CRLFile: "/crl.pem"}
	}},
	{"servers.*.http3.enabled", func(c *config.Config) {
		c.Servers[0].HTTP3 = &config.HTTP3Config{Enabled: true, AltSvcMaxAge: 100}
	}},
	{"servers.*.h2c", func(c *config.Config) {
		c.Servers[0].TLS = nil
		c.Servers[0].H2C = true
	}},
}

// bindSeed is the baseline the mutations are applied to: one TLS listener whose
// client-auth, HTTP/3 and h2c state are all off, so every mutation above is a
// real change.
func bindSeed() *config.Config {
	return &config.Config{
		Servers: []config.ServerConfig{{
			Listen:      ":8443",
			ServerNames: []string{"example.com"},
			TLS:         &config.TLSConfig{Enabled: true, Cert: "c", Key: "k"},
		}},
	}
}

// TestListenerBindFingerprintMirrorsRegistry proves the bind-time comparator is
// a checked mirror of the lifecycle registry rather than an independent
// lifecycle list: every property it freezes maps to a registry path classified
// as listener-bound, and changing that property really does force a rebind.
func TestListenerBindFingerprintMirrorsRegistry(t *testing.T) {
	const addr = ":8443"
	for _, tc := range bindFingerprintPaths {
		t.Run(tc.path, func(t *testing.T) {
			e, ok := lifecycle.Lookup(tc.path)
			if !ok {
				t.Fatalf("%s has no lifecycle disposition", tc.path)
			}
			if e.Class != lifecycle.NewListenerOnlyClass && !e.AddressKeyed {
				t.Fatalf("%s is classified %s without AddressKeyed; the bind fingerprint compares it, so the registry must call it listener-bound",
					tc.path, e.Class)
			}
			if !e.Conditional {
				t.Fatalf("%s is compared per bound address, so the registry entry must be conditional", tc.path)
			}

			next := bindSeed()
			tc.mutate(next)
			if listenerBindFingerprint(bindSeed(), addr) == listenerBindFingerprint(next, addr) {
				t.Fatalf("changing %s did not change the bind fingerprint of the kept address", tc.path)
			}
			if _, need := ListenerRebindRequired(bindSeed(), next); !need {
				t.Fatalf("changing %s on a kept address must require a rebind", tc.path)
			}
		})
	}
}

// TestRegistryListenerBoundPathsAreAllMirrored is the reverse direction: a new
// listener-bound registry entry must be added to bindFingerprintPaths, so the
// bind comparator cannot silently fall behind the registry.
func TestRegistryListenerBoundPathsAreAllMirrored(t *testing.T) {
	mirrored := map[string]bool{}
	for _, tc := range bindFingerprintPaths {
		mirrored[tc.path] = true
	}
	// Listen addresses are the identity the comparator is keyed by rather than a
	// property of a kept listener, and ACME material is gated separately by
	// ACMERestartRequired so its restart reason stays specific.
	gatedElsewhere := map[string]bool{
		"servers.*.listen": true,
		"stream.*.listen":  true,
	}
	for _, e := range lifecycle.Registry {
		if strings.HasPrefix(e.Path, "servers.*.tls.acme.") {
			continue
		}
		if e.Class != lifecycle.NewListenerOnlyClass && !e.AddressKeyed {
			continue
		}
		if gatedElsewhere[e.Path] || mirrored[e.Path] {
			continue
		}
		t.Errorf("listener-bound registry path %q is not covered by bindFingerprintPaths; add it so listenerBindFingerprint stays a checked mirror", e.Path)
	}
}

// TestACMELeavesAreGatedByACMERestartRequired records the deliberate split: the
// ACME leaves are listener-bound but deliberately excluded from the bind
// fingerprint so the operator sees an ACME-specific restart reason.
func TestACMELeavesAreGatedByACMERestartRequired(t *testing.T) {
	acme := func(domains ...string) []config.ServerConfig {
		return []config.ServerConfig{{
			Listen: ":8443",
			TLS: &config.TLSConfig{
				Enabled: true,
				ACME:    &config.ACMEConfig{Enabled: true, Email: "ops@example.com", Domains: domains},
			},
		}}
	}
	if _, need := ACMERestartRequired(acme("a.example.com"), acme("a.example.com", "b.example.com")); !need {
		t.Fatal("changing the ACME domain set must be restart-required")
	}
	for _, e := range lifecycle.Registry {
		if !strings.HasPrefix(e.Path, "servers.*.tls.acme.") {
			continue
		}
		if e.Class == lifecycle.ValidationRejectedReservedClass {
			continue
		}
		if !e.StartupConsumed {
			t.Errorf("%s must be startup-consumed so the fingerprint compares it", e.Path)
		}
	}
}
