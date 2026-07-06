// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build acme

package server

import (
	"crypto/tls"
	"net/http"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"

	"jul/internal/config"
)

// ACMECompiled reports whether this binary includes the ACME implementation.
// It is true in builds with the "acme" build tag.
const ACMECompiled = true

// letsEncryptStagingURL is Let's Encrypt's staging directory. It has generous
// rate limits but issues untrusted certificates, so it is the safe default.
const letsEncryptStagingURL = "https://acme-staging-v02.api.letsencrypt.org/directory"

// acmeManager adapts autocert.Manager to the ACMEManager seam. A single manager
// covers the union of ACME domains across all server blocks; it caches and
// auto-renews certificates and answers HTTP-01 challenges. TLS-ALPN-01 is
// served on the TLS listener itself once "acme-tls/1" is advertised (see
// Server.listenerNextProtos); autocert detects the challenge from the
// ClientHello and returns the special challenge certificate.
type acmeManager struct {
	mgr     *autocert.Manager
	onIssue func(domain string, notAfter time.Time)
	ocsp    bool // staple OCSP responses onto issued certificates
}

// NewACMEManager builds an ACME manager covering every acme-enabled server
// block, or returns (nil, nil) when no block enables ACME. onIssue, if non-nil,
// is invoked with each served certificate's leaf expiry for metrics. The first
// block's email, CA, cache directory, and OCSP-stapling setting configure the
// shared account and on-disk cache (validation keeps these consistent for a
// single listener).
func NewACMEManager(servers []config.ServerConfig, onIssue func(domain string, notAfter time.Time)) (ACMEManager, error) {
	var domains []string
	var email, ca, cacheDir string
	ocsp, ocspSet := true, false
	seen := make(map[string]bool)
	for _, srv := range servers {
		if srv.TLS == nil || !srv.TLS.Enabled || srv.TLS.ACME == nil || !srv.TLS.ACME.Enabled {
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
		if cacheDir == "" {
			cacheDir = a.CacheDir
		}
		if !ocspSet {
			ocsp = a.OCSPStaplingEnabled()
			ocspSet = true
		}
	}
	if len(domains) == 0 {
		return nil, nil // no ACME configured
	}
	m := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Cache:      autocert.DirCache(cacheDir),
		HostPolicy: autocert.HostWhitelist(domains...),
		Email:      email,
		Client:     &acme.Client{DirectoryURL: directoryURL(ca)},
	}
	return &acmeManager{mgr: m, onIssue: onIssue, ocsp: ocsp}, nil
}

// directoryURL maps a configured CA name to its ACME directory URL. The empty
// value and "letsencrypt-staging" both resolve to staging so a missing or
// default CA never hits production rate limits.
func directoryURL(ca string) string {
	switch ca {
	case "letsencrypt":
		return acme.LetsEncryptURL
	case "letsencrypt-staging", "":
		return letsEncryptStagingURL
	default:
		return ca // an explicit https directory URL (validated upstream)
	}
}

// Provider returns a CertProvider backed by the shared autocert manager. The
// domains argument is informational for this implementation because the
// manager's HostPolicy already governs the union of allowed domains; it lets a
// future per-address provider scope issuance without changing the seam. When
// OCSP stapling is enabled the autocert provider is wrapped so each served
// certificate carries a stapled OCSP response.
func (a *acmeManager) Provider(domains []string) CertProvider {
	base := &acmeProvider{mgr: a.mgr, onIssue: a.onIssue}
	if !a.ocsp {
		return base
	}
	return newOCSPStapler(base)
}

// ChallengeHandler returns autocert's HTTP-01 handler, which answers challenge
// requests and delegates everything else to next.
func (a *acmeManager) ChallengeHandler(next http.Handler) http.Handler {
	return a.mgr.HTTPHandler(next)
}

// acmeProvider obtains certificates from autocert during the TLS handshake and
// reports leaf expiry to onIssue for metrics.
type acmeProvider struct {
	mgr     *autocert.Manager
	onIssue func(domain string, notAfter time.Time)
}

func (p *acmeProvider) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	cert, err := p.mgr.GetCertificate(hello)
	if err != nil {
		return nil, err
	}
	// A TLS-ALPN-01 handshake yields a short-lived challenge certificate, not a
	// real leaf; skip the metrics hook so challenge certs never pollute the
	// expiry gauge.
	if p.onIssue != nil && cert.Leaf != nil && !isACMEChallenge(hello) {
		p.onIssue(hello.ServerName, cert.Leaf.NotAfter)
	}
	return cert, nil
}

// isACMEChallenge reports whether a ClientHello is a TLS-ALPN-01 validation
// handshake, identified by the reserved "acme-tls/1" application protocol.
func isACMEChallenge(hello *tls.ClientHelloInfo) bool {
	for _, proto := range hello.SupportedProtos {
		if proto == acme.ALPNProto {
			return true
		}
	}
	return false
}
