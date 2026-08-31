// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"fmt"

	"jul/internal/config"
	"jul/internal/server"
)

// PreparedTLS is a candidate certificate provider for the admin listener,
// built without touching the live one. A nil *PreparedTLS from PrepareTLS
// means there is nothing to commit: TLS is disabled, or the certificate is
// unchanged (#100, #336).
type PreparedTLS struct {
	provider    server.CertProvider
	fingerprint string
}

// PrepareTLS builds and validates a candidate certificate provider for the
// admin listener when [admin.tls]'s certificate content changed, reusing
// #100's exact CertProvider/DynamicCertProvider seam rather than a second
// one. It returns (nil, nil) when TLS was not enabled at startup (enabling it
// is restart-required and never reaches here) or the certificate identity is
// unchanged. A non-nil error means the candidate cert/key pair failed to
// load, which must abort the whole apply before persistence, exactly like the
// data plane's PreflightTLS.
func (s *Server) PrepareTLS(cfg config.AdminConfig) (*PreparedTLS, error) {
	if s.certProvider == nil || cfg.TLS == nil || !cfg.TLS.Enabled {
		return nil, nil
	}
	fp := server.SingleCertFingerprint(cfg.TLS.Cert, cfg.TLS.Key)
	s.certMu.Lock()
	unchanged := fp == s.certFingerprint
	s.certMu.Unlock()
	if unchanged {
		return nil, nil
	}
	provider, err := server.NewSingleCertProvider(cfg.TLS.Cert, cfg.TLS.Key)
	if err != nil {
		return nil, fmt.Errorf("admin.tls: %w", err)
	}
	return &PreparedTLS{provider: provider, fingerprint: fp}, nil
}

// CommitPreparedTLS installs the candidate certificate, if any, exactly once.
// It must not fail: PrepareTLS already validated the candidate. New TLS
// handshakes after this call observe the candidate certificate; a connection
// already in progress may complete with the previous one, and no connection
// is dropped or listener rebound (#100, #336).
func (s *Server) CommitPreparedTLS(prepared *PreparedTLS) {
	if prepared == nil || s.certProvider == nil {
		return
	}
	s.certProvider.Set(prepared.provider)
	s.certMu.Lock()
	s.certFingerprint = prepared.fingerprint
	s.certMu.Unlock()
}
