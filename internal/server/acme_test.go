package server

import (
	"crypto/tls"
	"net/http"
	"reflect"
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

func TestListenerNextProtosAdvertisesACMEALPN(t *testing.T) {
	// With an ACME manager and an acme-enabled address, acme-tls/1 is offered
	// first so the TLS-ALPN-01 challenge can be answered on the listener.
	s := &Server{cfg: acmeServerCfg(), ACME: &fakeACME{}}
	got := s.listenerNextProtos(":443")
	want := []string{"acme-tls/1", "h2", "http/1.1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("acme-enabled NextProtos = %v, want %v", got, want)
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
