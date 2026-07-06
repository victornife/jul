// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"jul/internal/config"
)

// CertProvider supplies certificates for TLS handshakes. v1 ships a
// file-backed implementation; future versions (e.g. ACME/autocert) can satisfy
// the same interface without touching the listener wiring.
type CertProvider interface {
	// GetCertificate selects a certificate for a ClientHello (SNI).
	GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error)
}

// fileCertProvider loads certificates from disk and selects them by SNI server
// name, supporting exact and wildcard (*.example.com) matches.
type fileCertProvider struct {
	mu      sync.RWMutex
	byName  map[string]*tls.Certificate // server_name -> cert
	wild    map[string]*tls.Certificate // ".example.com" -> cert
	fallbck *tls.Certificate            // first loaded cert
}

// certBinding pairs a TLS config with the server names it should serve.
type certBinding struct {
	tls   *config.TLSConfig
	names []string
}

// newFileCertProvider loads every distinct cert/key pair referenced by the
// given bindings and indexes them by server name for SNI selection.
func newFileCertProvider(bindings []certBinding) (*fileCertProvider, error) {
	p := &fileCertProvider{
		byName: make(map[string]*tls.Certificate),
		wild:   make(map[string]*tls.Certificate),
	}
	loaded := make(map[string]*tls.Certificate) // cert+key path -> cert (dedup)

	for _, b := range bindings {
		if b.tls == nil || !b.tls.Enabled {
			continue
		}
		cacheKey := b.tls.Cert + "\x00" + b.tls.Key
		cert, ok := loaded[cacheKey]
		if !ok {
			c, err := tls.LoadX509KeyPair(b.tls.Cert, b.tls.Key)
			if err != nil {
				return nil, fmt.Errorf("load cert %q/%q: %w", b.tls.Cert, b.tls.Key, err)
			}
			cert = &c
			loaded[cacheKey] = cert
		}
		if p.fallbck == nil {
			p.fallbck = cert
		}
		for _, name := range b.names {
			name = strings.ToLower(name)
			if strings.HasPrefix(name, "*.") {
				p.wild[name[1:]] = cert // store ".example.com"
			} else {
				p.byName[name] = cert
			}
		}
	}
	if p.fallbck == nil {
		return nil, fmt.Errorf("tls enabled but no certificates loaded")
	}
	return p, nil
}

// GetCertificate implements CertProvider, matching exact then wildcard names,
// falling back to the first loaded certificate.
func (p *fileCertProvider) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	name := strings.ToLower(strings.TrimSpace(hello.ServerName))
	p.mu.RLock()
	defer p.mu.RUnlock()

	if name != "" {
		if cert, ok := p.byName[name]; ok {
			return cert, nil
		}
		if i := strings.IndexByte(name, '.'); i >= 0 {
			if cert, ok := p.wild[name[i:]]; ok {
				return cert, nil
			}
		}
	}
	return p.fallbck, nil
}

// PreflightTLS validates that every file-based TLS certificate the configuration
// would load can actually be parsed, without binding any listener. It mirrors
// certProviderFor's per-address selection: an address that enables ACME obtains
// its certificate at handshake time, so its server blocks are skipped here;
// every other TLS-enabled server block must reference a loadable cert/key pair.
//
// This lets an apply fail fast with a clear error instead of surfacing a broken
// certificate only at the asynchronous reload — where the old runtime keeps
// serving while audit/history have already recorded the apply as successful.
func PreflightTLS(servers []config.ServerConfig) error {
	for i := range servers {
		srv := &servers[i]
		if srv.TLS == nil || !srv.TLS.Enabled {
			continue
		}
		// ACME-served addresses obtain certificates at handshake time, so there
		// is no file pair to validate here (matching certProviderFor).
		if acmeEnabledForAddr(servers, srv.Listen) {
			continue
		}
		if _, err := tls.LoadX509KeyPair(srv.TLS.Cert, srv.TLS.Key); err != nil {
			return fmt.Errorf("server %s: tls certificate: %w", srv.Listen, err)
		}
	}
	return nil
}

// acmeFingerprint summarises the ACME settings that are fixed when the autocert
// manager is built at startup: the union of issued domains plus the issuer
// parameters (email, CA and challenge). Two configs with equal fingerprints can
// swap their ACME state on reload without a restart; a non-empty fingerprint
// means the candidate relies on ACME. Domains are de-duplicated and sorted so
// the comparison is order-insensitive.
func acmeFingerprint(servers []config.ServerConfig) string {
	var domains []string
	seen := make(map[string]bool)
	var email, ca, challenge string
	for i := range servers {
		srv := &servers[i]
		if srv.TLS == nil || !srv.TLS.Enabled ||
			srv.TLS.ACME == nil || !srv.TLS.ACME.Enabled {
			continue
		}
		a := srv.TLS.ACME
		for _, d := range a.Domains {
			if !seen[d] {
				seen[d] = true
				domains = append(domains, d)
			}
		}
		if email == "" {
			email = a.Email
		}
		if ca == "" {
			ca = a.CA
		}
		if challenge == "" {
			challenge = a.Challenge
		}
	}
	if len(domains) == 0 {
		return ""
	}
	sort.Strings(domains)
	return strings.Join(domains, ",") + "|" + email + "|" + ca + "|" + challenge
}

// ACMERestartRequired reports whether moving from old to next changes the ACME
// configuration in a way that cannot take effect without a restart. The autocert
// manager's issued-domain set and issuer are frozen when it is built at startup,
// so introducing ACME or changing its domains/issuer requires a restart and the
// returned reason is non-empty.
//
// Removing ACME does not require a restart: the per-address provider selection
// (certProviderFor) swaps to file certificates on the next reload, and
// PreflightTLS validates that those file certificates load. An unchanged ACME
// configuration likewise needs no restart.
func ACMERestartRequired(old, next []config.ServerConfig) (string, bool) {
	nextFP := acmeFingerprint(next)
	if nextFP == "" {
		return "", false // candidate uses no ACME (removal hot-applies)
	}
	if nextFP == acmeFingerprint(old) {
		return "", false // ACME configuration unchanged
	}
	return "automatic HTTPS (ACME) domains or issuer changed; the issued-domain set is fixed when the server starts", true
}

// minTLSVersion maps a config string to a crypto/tls version constant,
// defaulting to TLS 1.2.
func minTLSVersion(v string) uint16 {
	switch strings.TrimSpace(v) {
	case "1.3":
		return tls.VersionTLS13
	default:
		return tls.VersionTLS12
	}
}

// dynamicCertProvider holds a swappable CertProvider so certificates can be
// reloaded without rebinding the listener: the listener's tls.Config keeps a
// stable GetCertificate callback that reads the current provider. The provider
// may be file-backed or ACME-backed; the listener wiring does not care which.
type dynamicCertProvider struct {
	current atomic.Pointer[certProviderHolder]
}

// certProviderHolder boxes a CertProvider so it can live in an atomic.Pointer
// (which needs a concrete element type) while still holding any implementation
// behind the interface.
type certProviderHolder struct{ p CertProvider }

func (d *dynamicCertProvider) set(p CertProvider) {
	d.current.Store(&certProviderHolder{p: p})
}

func (d *dynamicCertProvider) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	h := d.current.Load()
	if h == nil || h.p == nil {
		return nil, fmt.Errorf("no certificate provider configured")
	}
	return h.p.GetCertificate(hello)
}

// ACMEManager is the seam between the listener wiring and an ACME certificate
// source (autocert today, a different implementation later) that keeps ACME
// specifics out of bind()/reload. It is satisfied by the acme build-tagged
// implementation and supplied by the composition root via Server.ACME.
type ACMEManager interface {
	// Provider returns a CertProvider that obtains and renews certificates for
	// the given domains on demand during the TLS handshake.
	Provider(domains []string) CertProvider
	// ChallengeHandler wraps next so HTTP-01 challenge requests under
	// /.well-known/acme-challenge/ are answered on the plain HTTP listener and
	// every other request falls through to next.
	ChallengeHandler(next http.Handler) http.Handler
}

// certProviderFor selects the certificate provider for a TLS listen address.
// This is the single seam where the certificate source is chosen: ACME when a
// server block on the address enables it, static files otherwise. New sources
// (a secrets store, an internal CA, ...) plug in here without touching the
// listener lifecycle.
func (s *Server) certProviderFor(addr string, bindings []certBinding) (CertProvider, error) {
	if s.ACME != nil && acmeEnabledForAddr(s.cfg.Servers, addr) {
		return s.ACME.Provider(acmeDomainsForAddr(s.cfg.Servers, addr)), nil
	}
	return newFileCertProvider(bindings)
}

// listenerNextProtos returns the ALPN protocols to advertise on addr's TLS
// listener. When ACME is active for the address, "acme-tls/1" is prepended so
// the TLS-ALPN-01 challenge can be answered on the same listener: autocert
// serves the special challenge certificate for handshakes that negotiate that
// protocol, while normal clients never select it. Otherwise only HTTP/2 and
// HTTP/1.1 are offered. The s.ACME nil-guard keeps this tag-free — lean builds
// have no ACME manager and so never advertise acme-tls/1.
func (s *Server) listenerNextProtos(addr string) []string {
	if s.ACME != nil && acmeEnabledForAddr(s.cfg.Servers, addr) {
		return []string{"acme-tls/1", "h2", "http/1.1"}
	}
	return []string{"h2", "http/1.1"}
}

// acmeEnabledForAddr reports whether any server block on addr enables ACME.
func acmeEnabledForAddr(servers []config.ServerConfig, addr string) bool {
	for _, srv := range servers {
		if srv.Listen == addr && srv.TLS != nil && srv.TLS.Enabled &&
			srv.TLS.ACME != nil && srv.TLS.ACME.Enabled {
			return true
		}
	}
	return false
}

// acmeDomainsForAddr collects the de-duplicated ACME domains across every
// acme-enabled server block on addr, in first-seen order.
func acmeDomainsForAddr(servers []config.ServerConfig, addr string) []string {
	var domains []string
	seen := make(map[string]bool)
	for _, srv := range servers {
		if srv.Listen != addr || srv.TLS == nil || !srv.TLS.Enabled ||
			srv.TLS.ACME == nil || !srv.TLS.ACME.Enabled {
			continue
		}
		for _, d := range srv.TLS.ACME.Domains {
			if !seen[d] {
				seen[d] = true
				domains = append(domains, d)
			}
		}
	}
	return domains
}

// tlsBindingsForAddr returns the cert bindings and min TLS version for a listen
// address, or ok=false when the address serves plain HTTP.
func tlsBindingsForAddr(servers []config.ServerConfig, addr string) (bindings []certBinding, minVer uint16, ok bool) {
	minVer = tls.VersionTLS12
	for _, srv := range servers {
		if srv.Listen != addr || srv.TLS == nil || !srv.TLS.Enabled {
			continue
		}
		ok = true
		bindings = append(bindings, certBinding{tls: srv.TLS, names: srv.ServerNames})
		if v := minTLSVersion(srv.TLS.MinVersion); v > minVer {
			minVer = v
		}
	}
	return bindings, minVer, ok
}

// tlsConfigForAddr builds a *tls.Config for a listen address if any server
// block on it enables TLS, or returns nil when the listener is plain HTTP.
func tlsConfigForAddr(servers []config.ServerConfig, addr string) (*tls.Config, error) {
	bindings, minVer, ok := tlsBindingsForAddr(servers, addr)
	if !ok {
		return nil, nil
	}
	provider, err := newFileCertProvider(bindings)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		GetCertificate: provider.GetCertificate,
		MinVersion:     minVer,
		NextProtos:     []string{"h2", "http/1.1"},
	}, nil
}
