// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"

	"jul/internal/adminapi"
)

// configAuthorityErrorCode is the one stable typed error used by every
// mutating endpoint refused because the process is file-owned (ADR 0019 §15).
const configAuthorityErrorCode = "config_authority_read_only"

type configAuthorityErrorEnvelope struct {
	Error configAuthorityErrorBody `json:"error"`
}

type configAuthorityErrorBody struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Details map[string]string `json:"details,omitempty"`
}

func configAuthorityReadOnlyEnvelope(status ConfigAuthorityStatus) configAuthorityErrorEnvelope {
	return configAuthorityErrorEnvelope{Error: configAuthorityErrorBody{
		Code:    configAuthorityErrorCode,
		Message: "Configuration is file-owned; the running server does not write it.",
		Details: map[string]string{
			"config_authority":        status.Mode,
			"config_authority_source": status.Source,
		},
	}}
}

func (s *Server) currentAuthority() ConfigAuthorityStatus {
	if s.deps.Authority == nil {
		return ConfigAuthorityStatus{Mode: "managed", Source: "explicit"}
	}
	return s.deps.Authority()
}

// denyIfFileOwned is the single authority gate for both Console and v1. The
// decision happens before request parsing or any other side effect; only the
// refusal encoder differs by contract surface.
func (s *Server) denyIfFileOwned(w http.ResponseWriter, r *http.Request, action string) bool {
	status := s.currentAuthority()
	if !status.IsFileOwned() {
		return false
	}
	s.recordAudit(r, action, "config", "failure", "denied: config_authority is file_owned")
	if s.deps.ObserveAuthorityDenied != nil {
		s.deps.ObserveAuthorityDenied(action)
	}
	if _, external := externalContract(r.Context()); external {
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeConfigAuthorityRO,
			"Configuration is file-owned; the running server does not write it.").WithDetails(adminapi.Details{
			ConfigAuthority:       status.Mode,
			ConfigAuthoritySource: status.Source,
		}))
		return true
	}
	writeJSON(w, http.StatusConflict, configAuthorityReadOnlyEnvelope(status))
	return true
}

func (s *Server) handleRefreshAuthorityDrift(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if s.deps.RefreshAuthorityDrift == nil {
		http.Error(w, "501 Not Implemented", http.StatusNotImplemented)
		return
	}
	writeJSON(w, http.StatusOK, s.deps.RefreshAuthorityDrift())
}
