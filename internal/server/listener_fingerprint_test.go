// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"os"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
)

// TestListenerRebindRequired is the false-positive / false-negative matrix for
// the bind-time-frozen listener gate. Every field that bind() reads once when it
// creates the listener must force a restart when it changes on a kept address
// (false-negative guard), while edits that either do not affect the bound
// listener or are handled elsewhere (new/removed addresses, hot-reloadable
// fields, ACME — gated by ACMERestartRequired) must not (false-positive guard).
func TestListenerRebindRequired(t *testing.T) {
	const addr = ":8080"
	const tlsAddr = ":8443"

	plain := func() config.ServerConfig { return config.ServerConfig{Listen: addr} }
	tlsSrv := func() config.ServerConfig {
		return config.ServerConfig{Listen: tlsAddr, TLS: &config.TLSConfig{Enabled: true}}
	}
	cfg := func(servers ...config.ServerConfig) *config.Config {
		return &config.Config{Servers: servers}
	}

	// requiresRestart asserts that mutating a single-server config on a kept
	// address forces a restart, and that the reason names the address. seed is a
	// builder so old and next each get independent pointer fields (TLS, HTTP3,
	// ClientAuth); a shared pointer would let the mutation leak into old.
	requiresRestart := func(t *testing.T, seed func() config.ServerConfig, mutate func(s *config.ServerConfig)) {
		t.Helper()
		old := cfg(seed())
		next := cfg(seed())
		mutate(&next.Servers[0])
		reason, need := ListenerRebindRequired(old, next)
		if !need {
			t.Fatal("expected restart required")
		}
		if reason == "" {
			t.Fatal("expected a non-empty reason")
		}
	}
	hotApplies := func(t *testing.T, old, next *config.Config) {
		t.Helper()
		if _, need := ListenerRebindRequired(old, next); need {
			t.Fatal("expected no restart (hot-applies)")
		}
	}

	// mtlsSeed builds a TLS server with a client-auth block for the mTLS cases.
	mtlsSeed := func(mode, caFile string) func() config.ServerConfig {
		return func() config.ServerConfig {
			s := tlsSrv()
			s.TLS.ClientAuth = &config.ClientAuthConfig{Mode: mode, CAFile: caFile}
			return s
		}
	}

	// --- false-negative guards: a frozen field changed on a kept listener ---

	t.Run("read header timeout", func(t *testing.T) {
		requiresRestart(t, plain, func(s *config.ServerConfig) {
			s.ReadHeaderTimeout = config.Duration(7 * time.Second)
		})
	})
	t.Run("read timeout", func(t *testing.T) {
		requiresRestart(t, plain, func(s *config.ServerConfig) {
			s.ReadTimeout = config.Duration(7 * time.Second)
		})
	})
	t.Run("write timeout", func(t *testing.T) {
		requiresRestart(t, plain, func(s *config.ServerConfig) {
			s.WriteTimeout = config.Duration(7 * time.Second)
		})
	})
	t.Run("idle timeout", func(t *testing.T) {
		requiresRestart(t, plain, func(s *config.ServerConfig) {
			s.IdleTimeout = config.Duration(7 * time.Second)
		})
	})
	t.Run("max header bytes", func(t *testing.T) {
		requiresRestart(t, plain, func(s *config.ServerConfig) {
			s.MaxHeaderBytes = config.Size(4096)
		})
	})
	t.Run("h2c toggled on plaintext listener", func(t *testing.T) {
		requiresRestart(t, plain, func(s *config.ServerConfig) { s.H2C = true })
	})
	t.Run("enabling TLS on a kept address", func(t *testing.T) {
		requiresRestart(t, plain, func(s *config.ServerConfig) {
			s.TLS = &config.TLSConfig{Enabled: true}
		})
	})
	t.Run("TLS minimum version", func(t *testing.T) {
		seed := func() config.ServerConfig {
			s := tlsSrv()
			s.TLS.MinVersion = "1.2"
			return s
		}
		requiresRestart(t, seed, func(s *config.ServerConfig) { s.TLS.MinVersion = "1.3" })
	})
	t.Run("HTTP/3 enabled", func(t *testing.T) {
		requiresRestart(t, tlsSrv, func(s *config.ServerConfig) {
			s.HTTP3 = &config.HTTP3Config{Enabled: true}
		})
	})
	t.Run("HTTP/3 alt-svc max-age", func(t *testing.T) {
		seed := func() config.ServerConfig {
			s := tlsSrv()
			s.HTTP3 = &config.HTTP3Config{Enabled: true, AltSvcMaxAge: 3600}
			return s
		}
		requiresRestart(t, seed, func(s *config.ServerConfig) { s.HTTP3.AltSvcMaxAge = 7200 })
	})
	t.Run("mutual TLS mode", func(t *testing.T) {
		requiresRestart(t, mtlsSeed("request", "ca.pem"), func(s *config.ServerConfig) {
			s.TLS.ClientAuth.Mode = "require"
		})
	})
	t.Run("mutual TLS ca file", func(t *testing.T) {
		requiresRestart(t, mtlsSeed("require", "old-ca.pem"), func(s *config.ServerConfig) {
			s.TLS.ClientAuth.CAFile = "new-ca.pem"
		})
	})
	t.Run("mutual TLS verify san", func(t *testing.T) {
		requiresRestart(t, mtlsSeed("require", "ca.pem"), func(s *config.ServerConfig) {
			s.TLS.ClientAuth.VerifySAN = []string{"svc.internal"}
		})
	})
	t.Run("mutual TLS crl file", func(t *testing.T) {
		requiresRestart(t, mtlsSeed("require", "ca.pem"), func(s *config.ServerConfig) {
			s.TLS.ClientAuth.CRLFile = "revoked.crl"
		})
	})
	t.Run("connection cap (global max_conns) with a kept listener", func(t *testing.T) {
		old := cfg(plain())
		next := cfg(plain())
		next.RateLimit = config.RateLimitConfig{Enabled: true, MaxConns: 100}
		if _, need := ListenerRebindRequired(old, next); !need {
			t.Fatal("changing the connection cap must require a restart for kept listeners")
		}
	})

	// --- false-positive guards: nothing the bound listener depends on changed ---

	t.Run("identical config hot-applies", func(t *testing.T) {
		hotApplies(t, cfg(plain()), cfg(plain()))
	})
	t.Run("non-first vhost timeout does not affect the listener", func(t *testing.T) {
		// Listener-level timeouts resolve from the FIRST server block on an
		// address; an edit to a second vhost's timeout must not force a restart.
		first := plain()
		second := config.ServerConfig{Listen: addr, ServerNames: []string{"b.example.com"}}
		old := cfg(first, second)
		secondNext := second
		secondNext.ReadHeaderTimeout = config.Duration(9 * time.Second)
		next := cfg(first, secondNext)
		hotApplies(t, old, next)
	})
	t.Run("h2c toggled on a TLS listener has no effect", func(t *testing.T) {
		old := cfg(tlsSrv())
		nextSrv := tlsSrv()
		nextSrv.H2C = true
		hotApplies(t, old, cfg(nextSrv))
	})
	t.Run("newly added address binds fresh", func(t *testing.T) {
		old := cfg(plain())
		added := config.ServerConfig{Listen: ":9090", ReadHeaderTimeout: config.Duration(3 * time.Second)}
		hotApplies(t, old, cfg(plain(), added))
	})
	t.Run("removed address is drained", func(t *testing.T) {
		removed := config.ServerConfig{Listen: ":9090", ReadHeaderTimeout: config.Duration(3 * time.Second)}
		hotApplies(t, cfg(plain(), removed), cfg(plain()))
	})
	t.Run("ACME domain change is gated elsewhere", func(t *testing.T) {
		acme := func(domains ...string) *config.Config {
			return cfg(config.ServerConfig{Listen: tlsAddr, TLS: &config.TLSConfig{
				Enabled: true,
				ACME:    &config.ACMEConfig{Enabled: true, Email: "ops@example.com", Domains: domains},
			}})
		}
		// ListenerRebindRequired must not fire for an ACME-only change;
		// ACMERestartRequired (see TestACMERestartRequired in tls_test.go) owns
		// that classification exclusively, the same way #100 moved static
		// certificate identity out of this fingerprint into its own hot-swap
		// path rather than duplicating detection across two mechanisms.
		hotApplies(t, acme("a.example.com"), acme("a.example.com", "b.example.com"))
	})
	t.Run("hot-reloadable field (server_names) does not force restart", func(t *testing.T) {
		old := cfg(plain())
		nextSrv := plain()
		nextSrv.ServerNames = []string{"api.example.com"}
		hotApplies(t, old, cfg(nextSrv))
	})
}

// TestPreflightRebindRequiredUsesLiveSnapshot (R9-04, R9-13) verifies that the
// runtime-aware rebind gate uses actually-bound listeners as the old side, not
// the on-disk prev config. A re-added address that is no longer bound is
// treated as a new listener (no rebind check), and a kept address whose bound
// fingerprint differs triggers restart_required.
func TestPreflightRebindRequiredUsesLiveSnapshot(t *testing.T) {
	addr := freePort(t)
	base := cfgWith(addr)
	next := cfgWith(addr)
	next.Servers[0].ReadHeaderTimeout = config.Duration(7 * time.Second)

	// Address not currently bound: even though it appears in both base and next,
	// it should be treated as a fresh bind, so no rebind check fires.
	empty := LiveSnapshot{Listeners: map[string]BoundListenerInfo{}}
	if _, need := PreflightRebindRequired(empty, next); need {
		t.Fatal("an unbound address present in both prev and next should not trigger rebind check")
	}

	// Address is bound with the base fingerprint: next changes a frozen setting,
	// so rebind is required.
	live := LiveSnapshot{
		Listeners: map[string]BoundListenerInfo{
			addr: {Addr: addr, Fingerprint: listenerBindFingerprint(base, addr)},
		},
	}
	reason, need := PreflightRebindRequired(live, next)
	if !need {
		t.Fatal("expected rebind required for a kept listener with changed frozen setting")
	}
	if reason == "" {
		t.Fatal("expected non-empty reason")
	}
}

// TestListenerBindFingerprintDoesNotDetectCertRotation (#100) verifies that
// rotating the contents of a cert or key file on a kept listener does NOT
// change the bound fingerprint: static certificate identity is hot-swapped
// through prepareCertRotation/tlsIdentityFingerprint instead of forcing a
// rebind (see TestTLSIdentityFingerprintDetectsContentRotation in
// cert_rotation_test.go for the mechanism that does detect it).
func TestListenerBindFingerprintDoesNotDetectCertRotation(t *testing.T) {
	dir := t.TempDir()
	cert1, key1 := writeSelfSigned(t, dir, "cert1", "a.example.com")
	cert2, key2 := writeSelfSigned(t, dir, "cert2", "a.example.com")

	addr := freePort(t)
	cfg := func(cert, key string) *config.Config {
		return &config.Config{
			Servers: []config.ServerConfig{{
				Listen: addr,
				TLS: &config.TLSConfig{
					Enabled: true,
					Cert:    cert,
					Key:     key,
				},
			}},
		}
	}

	fp1 := listenerBindFingerprint(cfg(cert1, key1), addr)
	fp2 := listenerBindFingerprint(cfg(cert2, key2), addr)
	if fp1 != fp2 {
		t.Fatal("rotating cert/key content changed the bind fingerprint; it must be hot-swapped instead of forcing a rebind (#100)")
	}

	// Re-using the same cert/key pair must still produce an identical
	// fingerprint (unchanged from before #100).
	fp1b := listenerBindFingerprint(cfg(cert1, key1), addr)
	if fp1 != fp1b {
		t.Fatal("identical cert/key content produced different fingerprints")
	}
}

// TestHashFileContentUsesFullSHA256 (R9-12) verifies that the helper used by
// the listener fingerprint returns the full 256-bit (64 hex char) digest.
func TestHashFileContentUsesFullSHA256(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "hash-test")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("hello listener fingerprint"); err != nil {
		t.Fatal(err)
	}
	got := hashFileContent(f.Name())
	if len(got) != 64 {
		t.Fatalf("hashFileContent returned %d hex chars (%q), want 64", len(got), got)
	}
	if strings.HasPrefix(got, "err:") {
		t.Fatalf("hashFileContent returned error marker: %q", got)
	}
}
