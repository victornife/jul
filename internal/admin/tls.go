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

// PrepareTLS builds only candidate TLS certificate state. Operational admin
// policy (Console/upload) is prepared by PrepareAdminRuntime before the common
// Publish boundary.
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

func (s *Server) CommitPreparedTLS(prepared *PreparedTLS) {
	if prepared == nil || s.certProvider == nil {
		return
	}
	s.certProvider.Set(prepared.provider)
	s.certMu.Lock()
	s.certFingerprint = prepared.fingerprint
	s.certMu.Unlock()
}
