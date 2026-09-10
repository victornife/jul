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

// PrepareTLS is the existing no-side-effect admin resource preparation hook
// used by both managed and source-driven reload paths. #157 also validates the
// candidate plugin-upload directory here because this hook already sits after
// config resolution and before Publish. The upload probe is reversible and
// creates neither the live candidate directory nor any final file.
func (s *Server) PrepareTLS(cfg config.AdminConfig) (*PreparedTLS, error) {
	uploadEnabled := (cfg.PluginUploadEnabled == nil || *cfg.PluginUploadEnabled) && cfg.PluginUploadMaxSize > 0
	if uploadEnabled {
		if err := preflightPluginUploadDir(cfg.PluginUploadDir); err != nil {
			return nil, err
		}
	}

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
