// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"
	"net/url"
	"reflect"

	"jul/internal/adminoidc"
	"jul/internal/rbac"
)

// prepareOIDC builds no network resources. Unchanged authentication policy can
// keep its bounded sessions; any mapping/permission/provider change creates an
// empty store that is reachable only after the common no-fail Publish swap.
func (s *Server) prepareOIDC(out *authSnapshot) error {
	if !out.cfg.OIDC.Enabled {
		out.oidc = nil
		return nil
	}
	if out.policy == nil || !out.policy.Enabled() {
		return nil
	} // blocked until startup installs RBAC
	old := s.authState.Load()
	if old != nil && old.oidc != nil && reflect.DeepEqual(old.cfg.OIDC, out.cfg.OIDC) && old.policy != nil && old.policy.Fingerprint() == out.policy.Fingerprint() && old.cfg.Token == out.cfg.Token {
		out.oidc = old.oidc
		return nil
	}
	runtime, err := adminoidc.New(out.cfg.OIDC, out.policy, s.deps.OIDCClient, s.deps.EffectiveVersions)
	if err != nil {
		return err
	}
	out.oidc = runtime
	return nil
}

func (s *Server) guardOIDCCredentials(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if adminoidc.CookiePresented(r) && (s.requestAdminSnapshot(r).mode != authModeRBAC || len(r.Header.Values("Authorization")) > 0) {
			s.writeUnauthenticated(w, r, "Ambiguous or retired authentication credential.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleOIDCStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		methodNotAllowed(w, "GET")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	runtime := s.requestAdminSnapshot(r).oidc
	if runtime == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "provider_name": runtime.ProviderName()})
}
func (s *Server) handleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	runtime := s.requestAdminSnapshot(r).oidc
	if runtime == nil {
		http.NotFound(w, r)
		return
	}
	runtime.Login(w, r)
}
func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	runtime := s.requestAdminSnapshot(r).oidc
	if runtime == nil {
		http.Redirect(w, r, "/?oidc_error=login_failed", http.StatusSeeOther)
		return
	}
	id, accepted := runtime.Callback(w, r)
	result := "deny"
	if accepted {
		result = "allow"
	} else {
		// The selected provider is known even when no subject was admitted.
		// Never attach failed claims or invent a role/credential identifier.
		id = rbac.Identity{ProviderName: runtime.ProviderName()}
	}
	r = r.WithContext(rbac.WithIdentity(r.Context(), id))
	if s.deps.ObserveOIDCLogin != nil {
		s.deps.ObserveOIDCLogin(result)
	}
	auditResult := "failure"
	if accepted {
		auditResult = "success"
	}
	s.recordAudit(r, "admin.oidc.login", "session", auditResult, "Console sign-in completed")
}
func (s *Server) handleOIDCLogout(w http.ResponseWriter, r *http.Request) {
	runtime := s.requestAdminSnapshot(r).oidc
	if runtime == nil {
		origin, err := url.Parse(r.Header.Get("Origin"))
		if r.Method != "POST" || r.Header.Get("X-Jul-Console") != "1" || len(r.Header.Values("Origin")) != 1 || err != nil || origin.Host != r.Host || (origin.Scheme != "http" && origin.Scheme != "https") {
			http.Error(w, "Logout request refused", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		adminoidc.ClearSessionCookies(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// The runtime checks session-origin and ambiguity again; audit uses only
	// server-owned identity before revocation. No cookie/token/claims are recorded.
	id, _, err := runtime.Authenticate(r)
	if revoked := runtime.Logout(w, r); revoked && err == nil {
		s.recordAudit(r.WithContext(rbac.WithIdentity(r.Context(), id)), "admin.oidc.logout", "session", "success", "Local Console session revoked")
	}
}
