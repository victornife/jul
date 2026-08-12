// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"net/http"
	"strings"
)

// PeerCertIdentity is the verified identity of a mutual-TLS client certificate,
// extracted once per request and carried in the request context so downstream
// handlers (notably the reverse proxy, via $ssl_client_* variables) can attach
// it to upstream requests without re-parsing the certificate.
type PeerCertIdentity struct {
	Verified    bool   // the certificate passed CA-chain verification at the handshake
	SubjectDN   string // RFC 2253 subject distinguished name
	IssuerDN    string // RFC 2253 issuer distinguished name
	CN          string // subject common name
	Serial      string // certificate serial number (decimal)
	Fingerprint string // SHA-256 of the DER certificate, lowercase hex
	SANs        string // comma-joined subject alternative names (DNS, IP, URI, email)
}

// clientCertCtxKey is the unexported context key for a *PeerCertIdentity.
type clientCertCtxKey struct{}

// WithPeerCertIdentity returns a copy of ctx carrying id.
func WithPeerCertIdentity(ctx context.Context, id *PeerCertIdentity) context.Context {
	return context.WithValue(ctx, clientCertCtxKey{}, id)
}

// PeerCertIdentityFrom returns the peer certificate identity stored in ctx, or
// nil if the request did not present a verified client certificate. It is
// distinct from clientaddr.Identity, which answers "what address is the
// client?" rather than "what certificate did the peer present?".
func PeerCertIdentityFrom(ctx context.Context) *PeerCertIdentity {
	id, _ := ctx.Value(clientCertCtxKey{}).(*PeerCertIdentity)
	return id
}

// ClientCert extracts the mutual-TLS client identity from the connection and
// stores it in the request context. When require is true a request that arrives
// without a verified client certificate is rejected with 403; otherwise the
// request proceeds with no identity attached. CA-chain verification has already
// happened at the TLS handshake, so any certificate present here is trusted.
func ClientCert(require bool) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
				if require {
					http.Error(w, "client certificate required", http.StatusForbidden)
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			id := identityFromCert(r.TLS.PeerCertificates[0])
			next.ServeHTTP(w, r.WithContext(WithPeerCertIdentity(r.Context(), id)))
		})
	}
}

// identityFromCert builds a PeerCertIdentity from a verified leaf certificate.
func identityFromCert(c *x509.Certificate) *PeerCertIdentity {
	sum := sha256.Sum256(c.Raw)
	var sans []string
	sans = append(sans, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		sans = append(sans, ip.String())
	}
	for _, u := range c.URIs {
		sans = append(sans, u.String())
	}
	sans = append(sans, c.EmailAddresses...)
	return &PeerCertIdentity{
		Verified:    true,
		SubjectDN:   c.Subject.String(),
		IssuerDN:    c.Issuer.String(),
		CN:          c.Subject.CommonName,
		Serial:      c.SerialNumber.String(),
		Fingerprint: hex.EncodeToString(sum[:]),
		SANs:        strings.Join(sans, ","),
	}
}
