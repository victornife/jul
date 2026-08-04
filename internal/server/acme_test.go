// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"testing"

	"jul/internal/config"
)

// certProviderFunc adapts a function to the CertProvider interface for tests.
type certProviderFunc func(*tls.ClientHelloInfo) (*tls.Certificate, error)

func (f certProviderFunc) GetCertificate(h *tls.ClientHelloInfo) (*tls.Certificate, error) {
	return f(h)
}

// fakeACME is a network-free test double for ACMEManager. It records the
// domains it is asked to serve and returns a sentinel certificate.
type fakeACME struct {
	gotDomains []string
	cert       *tls.Certificate
}

func (f *fakeACME) Provider(domains []string) CertProvider {
	f.gotDomains = domains
	return certProviderFunc(func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		return f.cert, nil
	})
}

func (f *fakeACME) ChallengeHandler(next http.Handler) http.Handler { return next }

// acmeServerCfg returns a config with one acme-enabled TLS block on :443.
func acmeServerCfg() *config.Config {
	return &config.Config{Servers: []config.ServerConfig{{
		Listen:      ":443",
		ServerNames: []string{"a.example.com"},
		TLS: &config.TLSConfig{Enabled: true, ACME: &config.ACMEConfig{
			Enabled: true, Email: "ops@example.com", CA: "letsencrypt-staging",
			Challenge: "http-01", Domains: []string{"a.example.com", "b.example.com"},
		}},
	}}}
}

func TestCertProviderForSelectsACME(t *testing.T) {
	sentinel := &tls.Certificate{}
	fake := &fakeACME{cert: sentinel}
	s := &Server{cfg: acmeServerCfg(), ACME: fake}

	p, err := s.certProviderFor(":443", nil)
	if err != nil {
		t.Fatalf("certProviderFor: %v", err)
	}
	got, err := p.GetCertificate(&tls.ClientHelloInfo{ServerName: "a.example.com"})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if got != sentinel {
		t.Error("expected the ACME provider's certificate")
	}
	if want := []string{"a.example.com", "b.example.com"}; !reflect.DeepEqual(fake.gotDomains, want) {
		t.Errorf("provider domains = %v, want %v", fake.gotDomains, want)
	}
}

func TestCertProviderForFallsBackToStaticFiles(t *testing.T) {
	dir := t.TempDir()
	cert, key := writeSelfSigned(t, dir, "static", "static.example.com")
	cfg := &config.Config{Servers: []config.ServerConfig{{
		Listen:      ":443",
		ServerNames: []string{"static.example.com"},
		TLS:         &config.TLSConfig{Enabled: true, Cert: cert, Key: key},
	}}}
	// ACME is present but this addr has no acme-enabled block, so static files win.
	s := &Server{cfg: cfg, ACME: &fakeACME{}}
	bindings, _, ok := tlsBindingsForAddr(cfg.Servers, ":443")
	if !ok {
		t.Fatal("expected tls bindings")
	}
	p, err := s.certProviderFor(":443", bindings)
	if err != nil {
		t.Fatalf("certProviderFor: %v", err)
	}
	if _, isFile := p.(*fileCertProvider); !isFile {
		t.Errorf("expected *fileCertProvider, got %T", p)
	}
}

func TestCertProviderForNilManagerUsesFiles(t *testing.T) {
	// ACME nil on an acme-enabled addr falls back to file loading, which fails
	// because acme blocks carry no cert/key. This makes a reload that newly
	// enables ACME fail loudly instead of silently serving nothing.
	s := &Server{cfg: acmeServerCfg()} // ACME nil
	if _, err := s.certProviderFor(":443", nil); err == nil {
		t.Error("expected error: no manager and no static certs")
	}
}

func TestAcmeEnabledForAddr(t *testing.T) {
	cfg := acmeServerCfg()
	cfg.Servers = append(cfg.Servers, config.ServerConfig{Listen: ":80"})
	if !acmeEnabledForAddr(cfg.Servers, ":443") {
		t.Error(":443 should be acme-enabled")
	}
	if acmeEnabledForAddr(cfg.Servers, ":80") {
		t.Error(":80 should not be acme-enabled")
	}
}

func TestAcmeDomainsForAddr(t *testing.T) {
	cfg := &config.Config{Servers: []config.ServerConfig{
		{Listen: ":443", TLS: &config.TLSConfig{Enabled: true, ACME: &config.ACMEConfig{Enabled: true, Domains: []string{"a", "b"}}}},
		{Listen: ":443", TLS: &config.TLSConfig{Enabled: true, ACME: &config.ACMEConfig{Enabled: true, Domains: []string{"b", "c"}}}},
	}}
	got := acmeDomainsForAddr(cfg.Servers, ":443")
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("domains = %v, want %v (deduped, first-seen order)", got, want)
	}
}

func TestListenerNextProtosRespectsACMEChallenge(t *testing.T) {
	cfg := acmeServerCfg()
	s := &Server{cfg: cfg, ACME: &fakeACME{}}

	// HTTP-01 must not expose the reserved TLS-ALPN challenge protocol.
	if got, want := s.listenerNextProtos(":443"), []string{"h2", "http/1.1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("http-01 NextProtos = %v, want %v", got, want)
	}

	// TLS-ALPN-01 advertises the reserved protocol first so autocert can serve
	// the challenge certificate while ordinary clients continue to negotiate
	// h2 or HTTP/1.1.
	cfg.Servers[0].TLS.ACME.Challenge = "tls-alpn-01"
	if got, want := s.listenerNextProtos(":443"), []string{"acme-tls/1", "h2", "http/1.1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("tls-alpn-01 NextProtos = %v, want %v", got, want)
	}
}

func TestAcmeChallengeForAddrDefaultsToHTTP01(t *testing.T) {
	cfg := acmeServerCfg()
	cfg.Servers[0].TLS.ACME.Challenge = ""
	if got := acmeChallengeForAddr(cfg.Servers, ":443"); got != "http-01" {
		t.Fatalf("challenge = %q, want http-01", got)
	}
	if got := acmeChallengeForAddr(cfg.Servers, ":8443"); got != "" {
		t.Fatalf("non-ACME address challenge = %q, want empty", got)
	}
}

func TestListenerNextProtosPlainWhenNoACME(t *testing.T) {
	// No manager (lean build) -> never advertise acme-tls/1.
	s := &Server{cfg: acmeServerCfg()}
	if got, want := s.listenerNextProtos(":443"), []string{"h2", "http/1.1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("nil-manager NextProtos = %v, want %v", got, want)
	}

	// Manager present but the address has no acme-enabled block -> plain protos.
	cfg := &config.Config{Servers: []config.ServerConfig{{
		Listen: ":443", ServerNames: []string{"static.example.com"},
		TLS: &config.TLSConfig{Enabled: true, Cert: "/c", Key: "/k"},
	}}}
	s = &Server{cfg: cfg, ACME: &fakeACME{}}
	if got, want := s.listenerNextProtos(":443"), []string{"h2", "http/1.1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("non-acme-addr NextProtos = %v, want %v", got, want)
	}
}

// TestACMERotationUnderConcurrentHandshakes exercises the cert-rotation
// invariant: swapping the provider in a dynamicCertProvider (the hot path
// during an ACME renewal or a config reload) must never cause a concurrent
// GetCertificate call to return an error or a nil certificate. The test is
// especially valuable under -race because the underlying atomic.Pointer swap
// is the correctness guarantee.
func TestACMERotationUnderConcurrentHandshakes(t *testing.T) {
	dir := t.TempDir()

	// Two distinct self-signed certs represent the "old" and "new" certificate
	// that would be returned by consecutive ACME renewals.
	certPathA, keyPathA := writeSelfSigned(t, dir, "alpha", "a.example.com")
	certPathB, keyPathB := writeSelfSigned(t, dir, "beta", "b.example.com")

	tlsCertA, err := tls.LoadX509KeyPair(certPathA, keyPathA)
	if err != nil {
		t.Fatal(err)
	}
	tlsCertB, err := tls.LoadX509KeyPair(certPathB, keyPathB)
	if err != nil {
		t.Fatal(err)
	}

	providerA := certProviderFunc(func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		return &tlsCertA, nil
	})
	providerB := certProviderFunc(func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		return &tlsCertB, nil
	})

	dyn := &dynamicCertProvider{}
	dyn.set(providerA)

	const handshakeGoroutines = 50
	const handshakesPerGoroutine = 200

	errs := make(chan error, handshakeGoroutines*handshakesPerGoroutine)
	hello := &tls.ClientHelloInfo{ServerName: "a.example.com"}

	// Start concurrent "handshake" goroutines that call GetCertificate in a
	// tight loop. Each must receive a non-nil certificate with no error even
	// while the provider is being swapped underneath them.
	done := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < handshakeGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				cert, err := dyn.GetCertificate(hello)
				if err != nil {
					errs <- err
					return
				}
				if cert == nil {
					errs <- fmt.Errorf("GetCertificate returned nil certificate during rotation")
					return
				}
			}
		}()
	}

	// Rotate the provider repeatedly while the handshake goroutines run.
	providers := []CertProvider{providerA, providerB}
	for i := 0; i < 500; i++ {
		dyn.set(providers[i%2])
	}

	// Signal workers to stop, wait for them, then drain the error channel.
	close(done)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("GetCertificate error during concurrent rotation: %v", err)
	}

	// After rotation, the provider must still serve a valid cert.
	cert, err := dyn.GetCertificate(hello)
	if err != nil {
		t.Fatalf("GetCertificate after rotation: %v", err)
	}
	if cert == nil {
		t.Error("expected a non-nil certificate after rotation")
	}
}
