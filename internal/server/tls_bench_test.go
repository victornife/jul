// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"crypto/tls"
	"testing"

	"jul/internal/config"
)

// benchCertProvider builds a file-backed provider indexed by two exact names
// and one wildcard, all served by a single self-signed certificate.
func benchCertProvider(b *testing.B) *fileCertProvider {
	b.Helper()
	dir := b.TempDir()
	cert, key := writeSelfSigned(b, dir, "bench", "a.example.com", "b.example.com", "*.svc.example.com")
	tc := &config.TLSConfig{Enabled: true, Cert: cert, Key: key}
	p, err := newFileCertProvider([]certBinding{{
		tls:   tc,
		names: []string{"a.example.com", "b.example.com", "*.svc.example.com"},
	}})
	if err != nil {
		b.Fatal(err)
	}
	return p
}

// BenchmarkSNICertSelection measures the per-handshake SNI certificate lookup
// (the GetCertificate hot path) for an exact-name hit, a wildcard hit, and the
// fallback when no name matches. This is pure CPU, isolated from the TLS
// handshake itself (see BenchmarkTLSHandshakeServerAuth for end-to-end cost).
func BenchmarkSNICertSelection(b *testing.B) {
	p := benchCertProvider(b)
	cases := []struct{ name, sni string }{
		{"exact", "a.example.com"},
		{"wildcard", "host.svc.example.com"},
		{"fallback", "unknown.example.net"},
	}
	for _, c := range cases {
		hello := &tls.ClientHelloInfo{ServerName: c.sni}
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := p.GetCertificate(hello); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
