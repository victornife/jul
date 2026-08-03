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
// auto-renews certificates. The validated process-wide challenge setting is
// captured once so only the configured challenge surface is activated:
//
//   - http-01 installs autocert's HTTP challenge handler;
//   - tls-alpn-01 leaves the HTTP handler untouched and relies on
//     Server.listenerNextProtos advertising acme-tls/1.
//
// Configuration validation requires every enabled ACME block to use the same
// challenge, matching the single shared manager model.
type acmeManager struct {
	mgr        *autocert.Manager
	challenge  string
	onIssue    func(domain string, notAfter time.Time)
	ocsp       bool         // staple OCSP responses onto issued certificates
	ocspClient *http.Client // guarded OCSP responder client; nil = default
}

// NewACMEManager builds an ACME manager covering every acme-enabled server
// block, or returns (nil, nil) when no block enables ACME. onIssue, if non-nil,
// is invoked with each served certificate's leaf expiry for metrics. acmeClient,
// when non-nil, guards the ACME directory/order/challenge HTTP calls through the
// egress allow-list; ocspClient likewise guards OCSP responder fetches. Both nil
// preserve the default (unguarded) clients so an egress-disabled build is
// unchanged. The first block's email, CA, challenge, cache directory, and
// OCSP-stapling setting configure the shared manager (validation requires every
// enabled block to agree on those values).
func NewACMEManager(servers []config.ServerConfig, onIssue func(domain string, notAfter time.Time), acmeClient, ocspClient *http.Client) (ACMEManager, error) {
	var domains []string
	var email, ca, challenge, cacheDir string
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
		if challenge == "" {
			challenge = a.Challenge
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
	client := &acme.Client{DirectoryURL: directoryURL(ca)}
	if acmeClient != nil {
		client.HTTPClient = acmeClient
	}
	m := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Cache:      autocert.DirCache(cacheDir),
		HostPolicy: autocert.HostWhitelist(domains...),
		Email:      email,
		Client:     client,
	}
	return &acmeManager{
		mgr:        m,
		challenge:  challenge,
		onIssue:    onIssue,
		ocsp:       ocsp,
		ocspClient: ocspClient,
	}, nil
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
	return newOCSPStapler(base, a.ocspClient)
}

// ChallengeHandler installs autocert's HTTP-01 handler only when HTTP-01 is the
// configured challenge. TLS-ALPN-01 leaves the plain HTTP handler unchanged so
// the non-selected challenge surface is not exposed.
func (a *acmeManager) ChallengeHandler(next http.Handler) http.Handler {
	if a.challenge != "http-01" {
		return next
	}
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
