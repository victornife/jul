// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import "net/http"

// configAuthorityErrorCode is the one stable typed error used by every
// mutating endpoint refused because the process is file-owned (ADR 0019
// §15). No other admin error uses this code.
const configAuthorityErrorCode = "config_authority_read_only"

// configAuthorityErrorEnvelope is the wire body of a file-owned mutation
// denial. It is deliberately small and identical everywhere: no path, no
// candidate bytes, no secret, and the same shape for every principal
// including a wildcard admin, because the denial is a property of the
// server's configuration, not of the caller's authorization.
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

// currentAuthority returns the process's configuration-authority status. A
// nil Deps.Authority hook is treated as managed with no drift, so tests and
// embedding callers that never wire it keep today's behavior.
func (s *Server) currentAuthority() ConfigAuthorityStatus {
	if s.deps.Authority == nil {
		return ConfigAuthorityStatus{Mode: "managed", Source: "explicit"}
	}
	return s.deps.Authority()
}

// denyIfFileOwned enforces ADR 0019 §15: in file_owned mode, every mutating
// endpoint is refused before any side effect — before the request body is
// parsed into a candidate, before any temp file, before any history write,
// before any audit mutation record, and before any lock. It MUST be the
// first statement of every mutating handler. It returns true when the
// request was denied and already answered; the caller must return
// immediately without doing any further work.
//
// action is a short, bounded audit label (e.g. "config.raw", "config.patch")
// — never raw configuration content.
func (s *Server) denyIfFileOwned(w http.ResponseWriter, r *http.Request, action string) bool {
	status := s.currentAuthority()
	if !status.IsFileOwned() {
		return false
	}
	s.recordAudit(r, action, "config", "failure", "denied: config_authority is file_owned")
	if s.deps.ObserveAuthorityDenied != nil {
		s.deps.ObserveAuthorityDenied(action)
	}
	writeJSON(w, http.StatusConflict, configAuthorityReadOnlyEnvelope(status))
	return true
}

// handleRefreshAuthorityDrift re-assesses managed-baseline drift on demand
// (ADR 0019 §12's fourth event-driven trigger: "explicit drift/status
// refresh", operator- or Console-initiated only) and returns the resulting
// status in the same shape the runtime overview carries. It never writes the
// configuration file and is a no-op outside managed authority, so it is safe
// under any authority mode. POST /api/config/authority/refresh
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
