// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"strings"

	"jul/internal/config"
)

// clientAuthBundle is the resolved mutual-TLS configuration for one listen
// address: the handshake mode, the CA pool client certificates are verified
// against, and an optional post-verification callback enforcing CRL revocation
// and a SAN allow-list. A nil bundle means client authentication is off.
type clientAuthBundle struct {
	mode   tls.ClientAuthType
	pool   *x509.CertPool
	verify func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error
}

// clientAuthMode maps a config mode string to a tls.ClientAuthType. "request"
// admits connections whether or not a certificate is presented but verifies any
// certificate that is presented against the CA pool; "require" rejects the
// handshake unless a CA-verified certificate is presented. Anything else
// (including "" and "none") disables client authentication.
func clientAuthMode(mode string) tls.ClientAuthType {
	switch strings.TrimSpace(mode) {
	case "request":
		return tls.VerifyClientCertIfGiven
	case "require":
		return tls.RequireAndVerifyClientCert
	default:
		return tls.NoClientCert
	}
}

// clientAuthForAddr resolves the mutual-TLS bundle for addr by aggregating the
// tls.client_auth blocks of every server that listens on it. Across multiple
// blocks on one address it takes the strongest mode (require beats request),
// the union of CA pools, the union of SAN allow-lists, and the union of revoked
// serials, so a connection is admitted if any block would admit it and a
// certificate is accepted if it satisfies any block. The common case of a
// single block on an address resolves exactly. It returns (nil, nil) when no
// block on the address enables client authentication. onResult, when non-nil,
// is invoked with "verified" or "rejected" for each presented, CA-verified
// certificate.
func clientAuthForAddr(servers []config.ServerConfig, addr string, onResult func(string)) (*clientAuthBundle, error) {
	var (
		mode     = tls.NoClientCert
		pool     *x509.CertPool
		sanAllow []string
		revoked  map[string]bool
		caCerts  []*x509.Certificate
	)
	for i := range servers {
		srv := &servers[i]
		if srv.Listen != addr || srv.TLS == nil || !srv.TLS.Enabled {
			continue
		}
		ca := srv.TLS.ClientAuth
		if !ca.Active() {
			continue
		}
		if m := clientAuthMode(ca.Mode); authStrength(m) > authStrength(mode) {
			mode = m
		}
		certs, err := loadCABundle(ca.CAFile)
		if err != nil {
			return nil, fmt.Errorf("server %q ca_file: %w", srv.Listen, err)
		}
		if pool == nil {
			pool = x509.NewCertPool()
		}
		for _, c := range certs {
			pool.AddCert(c)
		}
		caCerts = append(caCerts, certs...)
		for _, s := range ca.VerifySAN {
			if s = strings.TrimSpace(s); s != "" {
				sanAllow = append(sanAllow, strings.ToLower(s))
			}
		}
		if f := strings.TrimSpace(ca.CRLFile); f != "" {
			serials, err := loadCRL(f, caCerts)
			if err != nil {
				return nil, fmt.Errorf("server %q crl_file: %w", srv.Listen, err)
			}
			if revoked == nil {
				revoked = make(map[string]bool, len(serials))
			}
			for s := range serials {
				revoked[s] = true
			}
		}
	}
	if mode == tls.NoClientCert || pool == nil {
		return nil, nil
	}
	return &clientAuthBundle{
		mode:   mode,
		pool:   pool,
		verify: makeClientCertVerifier(sanAllow, revoked, onResult),
	}, nil
}

// ClientAuthBundle is the resolved mutual-TLS configuration for one TLS
// listener, exported for direct reuse by a single-target listener that has no
// per-server-block aggregation to do — the admin listener (#336). A Mode of
// tls.NoClientCert means client authentication is off.
type ClientAuthBundle struct {
	Mode   tls.ClientAuthType
	Pool   *x509.CertPool
	Verify func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error
}

// NewSingleClientAuthBundle builds a ClientAuthBundle directly from one
// ClientAuthConfig, reusing the exact CA-loading, CRL-loading and
// verification-callback logic clientAuthForAddr uses for the data plane, but
// without its per-address aggregation across multiple server blocks — the
// admin listener has exactly one client_auth block, not many (#336). It
// returns (nil, nil) when ca is inactive (nil or mode "none").
func NewSingleClientAuthBundle(ca *config.ClientAuthConfig, onResult func(string)) (*ClientAuthBundle, error) {
	if !ca.Active() {
		return nil, nil
	}
	caCerts, err := loadCABundle(ca.CAFile)
	if err != nil {
		return nil, fmt.Errorf("ca_file: %w", err)
	}
	pool := x509.NewCertPool()
	for _, c := range caCerts {
		pool.AddCert(c)
	}
	var sanAllow []string
	for _, s := range ca.VerifySAN {
		if s = strings.TrimSpace(s); s != "" {
			sanAllow = append(sanAllow, strings.ToLower(s))
		}
	}
	var revoked map[string]bool
	if f := strings.TrimSpace(ca.CRLFile); f != "" {
		revoked, err = loadCRL(f, caCerts)
		if err != nil {
			return nil, fmt.Errorf("crl_file: %w", err)
		}
	}
	return &ClientAuthBundle{
		Mode:   clientAuthMode(ca.Mode),
		Pool:   pool,
		Verify: makeClientCertVerifier(sanAllow, revoked, onResult),
	}, nil
}

// authStrength ranks client-auth modes so the aggregation can pick the
// strongest across blocks sharing a listen address.
func authStrength(m tls.ClientAuthType) int {
	switch m {
	case tls.RequireAndVerifyClientCert:
		return 2
	case tls.VerifyClientCertIfGiven:
		return 1
	default:
		return 0
	}
}

// loadCABundle reads a PEM file that may contain one or more CA certificates
// and returns them parsed. It errors if the file holds no usable certificate so
// a typo does not silently disable verification.
func loadCABundle(path string) ([]*x509.Certificate, error) {
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var certs []*x509.Certificate
	rest := pemBytes
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse certificate: %w", err)
		}
		certs = append(certs, c)
	}
	if len(certs) == 0 {
		return nil, fmt.Errorf("no CERTIFICATE block found in %s", path)
	}
	return certs, nil
}

// loadCRL parses a certificate revocation list (PEM "X509 CRL" block or raw
// DER) and returns the set of revoked serial numbers as lowercase hexadecimal.
// The CRL signature is verified against one of caCerts so a forged list cannot
// revoke valid certificates; it errors if no CA signed it.
func loadCRL(path string, caCerts []*x509.Certificate) (map[string]bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	der := raw
	if block, _ := pem.Decode(raw); block != nil {
		der = block.Bytes
	}
	crl, err := x509.ParseRevocationList(der)
	if err != nil {
		return nil, fmt.Errorf("parse CRL: %w", err)
	}
	verified := false
	for _, ca := range caCerts {
		if err := crl.CheckSignatureFrom(ca); err == nil {
			verified = true
			break
		}
	}
	if !verified {
		return nil, fmt.Errorf("CRL %s is not signed by any configured CA", path)
	}
	revoked := make(map[string]bool, len(crl.RevokedCertificateEntries))
	for _, e := range crl.RevokedCertificateEntries {
		revoked[strings.ToLower(e.SerialNumber.Text(16))] = true
	}
	return revoked, nil
}

// makeClientCertVerifier builds the tls.Config.VerifyPeerCertificate callback
// that runs after the stdlib chain check. It enforces the optional SAN
// allow-list and CRL revocation set and reports the outcome through onResult. It
// returns nil when there is nothing extra to enforce and no result hook, so the
// listener keeps the stdlib default. A handshake that presents no certificate
// (request mode) reaches here with an empty leaf and is accepted without a
// report, leaving the missing-certificate decision to per-location enforcement.
func makeClientCertVerifier(sanAllow []string, revoked map[string]bool, onResult func(string)) func([][]byte, [][]*x509.Certificate) error {
	if len(sanAllow) == 0 && len(revoked) == 0 && onResult == nil {
		return nil
	}
	return func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
		var leaf *x509.Certificate
		if len(verifiedChains) > 0 && len(verifiedChains[0]) > 0 {
			leaf = verifiedChains[0][0]
		} else if len(rawCerts) > 0 {
			if c, err := x509.ParseCertificate(rawCerts[0]); err == nil {
				leaf = c
			}
		}
		if leaf == nil {
			return nil
		}
		if revoked[strings.ToLower(leaf.SerialNumber.Text(16))] {
			if onResult != nil {
				onResult("rejected")
			}
			return fmt.Errorf("client certificate %s is revoked", leaf.SerialNumber.Text(16))
		}
		if len(sanAllow) > 0 && !sanAllowed(leaf, sanAllow) {
			if onResult != nil {
				onResult("rejected")
			}
			return fmt.Errorf("client certificate SAN not in allow-list")
		}
		if onResult != nil {
			onResult("verified")
		}
		return nil
	}
}

// sanAllowed reports whether any of the certificate's subject alternative names
// (DNS, email, URI, or IP, compared case-insensitively) is in allow.
func sanAllowed(c *x509.Certificate, allow []string) bool {
	want := make(map[string]bool, len(allow))
	for _, a := range allow {
		want[a] = true
	}
	for _, n := range c.DNSNames {
		if want[strings.ToLower(n)] {
			return true
		}
	}
	for _, e := range c.EmailAddresses {
		if want[strings.ToLower(e)] {
			return true
		}
	}
	for _, u := range c.URIs {
		if want[strings.ToLower(u.String())] {
			return true
		}
	}
	for _, ip := range c.IPAddresses {
		if want[strings.ToLower(ip.String())] {
			return true
		}
	}
	return false
}
