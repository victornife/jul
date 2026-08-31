// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"crypto/tls"
	"crypto/x509"
	"strings"
	"time"

	"jul/internal/config"
)

// CertSummary describes one configured certificate for the admin console cert
// panel. It carries either a parsed file-based leaf (Subject/NotAfter/DNSNames)
// or an ACME-managed marker (Source == "acme"), and never includes private-key
// material. A non-empty Error means the configured certificate could not be
// read or parsed.
type CertSummary struct {
	// ServerNames are the virtual-host names this certificate serves.
	ServerNames []string `json:"server_names"`
	// Source is "file" for a static cert/key pair or "acme" for an
	// automatically managed certificate.
	Source string `json:"source"`
	// Subject is the leaf certificate subject common name (file source only).
	Subject string `json:"subject,omitempty"`
	// Issuer is the leaf certificate issuer common name (file source only).
	Issuer string `json:"issuer,omitempty"`
	// DNSNames are the subject alternative names on the leaf (file source only).
	DNSNames []string `json:"dns_names,omitempty"`
	// NotBefore and NotAfter bound the validity window (file source only).
	NotBefore time.Time `json:"not_before,omitempty"`
	NotAfter  time.Time `json:"not_after,omitempty"`
	// Error, when set, explains why the configured certificate is unusable.
	Error string `json:"error,omitempty"`
}

// InspectCerts walks the server blocks and returns a summary of each TLS
// certificate from the configured file paths. File-based certs are parsed
// fresh from disk for their leaf expiry; ACME-managed servers are reported as
// managed without reading the autocert cache (their live expiry is exported
// via the TLS metrics gauge). The returned slice is safe to expose: it
// contains no key material. It runs in any build because it reads
// configuration and parses public certificate files only.
//
// This reads whatever is currently on disk, which is what a preflight check
// (no running listener yet) needs. For an already-bound listener, prefer
// Server.LiveCertSummaries: it reports the certificate actually installed in
// the live provider, which can differ from the configured path's current
// bytes when a candidate was rejected or the file was rewritten out of band
// (#100).
func InspectCerts(servers []config.ServerConfig) []CertSummary {
	var out []CertSummary
	for _, srv := range servers {
		if srv.TLS == nil || !srv.TLS.Enabled {
			continue
		}
		if srv.TLS.ACME != nil && srv.TLS.ACME.Enabled {
			out = append(out, CertSummary{
				ServerNames: append([]string(nil), srv.ServerNames...),
				Source:      "acme",
			})
			continue
		}
		out = append(out, inspectFileCert(srv))
	}
	return out
}

// inspectFileCert loads and parses a static cert/key pair's leaf straight
// from the configured paths. The key is loaded only to validate the pair;
// nothing about it is returned.
func inspectFileCert(srv config.ServerConfig) CertSummary {
	names := append([]string(nil), srv.ServerNames...)
	pair, err := tls.LoadX509KeyPair(srv.TLS.Cert, srv.TLS.Key)
	if err != nil {
		return CertSummary{ServerNames: names, Source: "file", Error: err.Error()}
	}
	return certSummaryFromCertificate(names, &pair)
}

// certSummaryFromCertificate builds a CertSummary from an already-loaded
// certificate — in memory (a live provider's installed certificate) or
// freshly parsed from disk (inspectFileCert) — extracting only leaf metadata
// and never key material.
func certSummaryFromCertificate(names []string, cert *tls.Certificate) CertSummary {
	cs := CertSummary{ServerNames: append([]string(nil), names...), Source: "file"}
	leaf := cert.Leaf
	if leaf == nil {
		if len(cert.Certificate) == 0 {
			cs.Error = "certificate contains no leaf"
			return cs
		}
		parsed, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			cs.Error = err.Error()
			return cs
		}
		leaf = parsed
	}
	cs.Subject = leaf.Subject.CommonName
	cs.Issuer = leaf.Issuer.CommonName
	cs.DNSNames = leaf.DNSNames
	cs.NotBefore = leaf.NotBefore
	cs.NotAfter = leaf.NotAfter
	if cs.Subject == "" {
		cs.Subject = strings.Join(leaf.DNSNames, ", ")
	}
	return cs
}

// LiveCertSummaries returns a secret-safe metadata summary of every
// certificate currently live across the server's bound TLS listeners. Unlike
// InspectCerts, which reads the configured file paths fresh on every call,
// this reads each listener's actually-installed certificate provider — so a
// rejected candidate, or an out-of-band rewrite of a certificate file between
// reloads with no config change to publish it, can never make an unpublished
// or invalid on-disk certificate appear live (#100).
func (s *Server) LiveCertSummaries() []CertSummary {
	cfg := s.LiveSnapshot().EffectiveConfig
	if cfg == nil {
		return nil
	}
	var out []CertSummary
	for _, addr := range uniqueListenAddrs(cfg.Servers) {
		if _, _, tlsOK := tlsBindingsForAddr(cfg.Servers, addr); !tlsOK {
			continue
		}
		if acmeEnabledForAddr(cfg.Servers, addr) {
			out = append(out, acmeCertSummariesForAddr(cfg.Servers, addr)...)
			continue
		}
		s.mu.Lock()
		entry := s.listeners[addr]
		s.mu.Unlock()
		if entry == nil || entry.provider == nil {
			continue
		}
		holder := entry.provider.current.Load()
		if holder == nil {
			continue
		}
		if summarized, ok := holder.p.(interface{ Summaries() []CertSummary }); ok {
			out = append(out, summarized.Summaries()...)
		}
	}
	return out
}

// acmeCertSummariesForAddr reports one ACME marker per ACME-enabled server
// block on addr, matching InspectCerts' ACME branch. No file or cache read is
// needed: ACME's live expiry is exported through the TLS metrics gauge.
func acmeCertSummariesForAddr(servers []config.ServerConfig, addr string) []CertSummary {
	var out []CertSummary
	for _, srv := range servers {
		if srv.Listen != addr || srv.TLS == nil || !srv.TLS.Enabled ||
			srv.TLS.ACME == nil || !srv.TLS.ACME.Enabled {
			continue
		}
		out = append(out, CertSummary{ServerNames: append([]string(nil), srv.ServerNames...), Source: "acme"})
	}
	return out
}
