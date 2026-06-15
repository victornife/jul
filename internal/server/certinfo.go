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
// certificate for the admin console. File-based certs are parsed for their leaf
// expiry; ACME-managed servers are reported as managed without reading the
// autocert cache (their live expiry is exported via the TLS metrics gauge). The
// returned slice is safe to expose: it contains no key material. It runs in any
// build because it reads configuration and parses public certificate files only.
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

// inspectFileCert loads and parses a static cert/key pair's leaf. The key is
// loaded only to validate the pair; nothing about it is returned.
func inspectFileCert(srv config.ServerConfig) CertSummary {
	cs := CertSummary{
		ServerNames: append([]string(nil), srv.ServerNames...),
		Source:      "file",
	}
	pair, err := tls.LoadX509KeyPair(srv.TLS.Cert, srv.TLS.Key)
	if err != nil {
		cs.Error = err.Error()
		return cs
	}
	leaf := pair.Leaf
	if leaf == nil {
		if len(pair.Certificate) == 0 {
			cs.Error = "certificate contains no leaf"
			return cs
		}
		parsed, perr := x509.ParseCertificate(pair.Certificate[0])
		if perr != nil {
			cs.Error = perr.Error()
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
