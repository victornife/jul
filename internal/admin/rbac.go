// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"crypto/subtle"
	"net/http"
	"time"

	"jul/internal/rbac"
)

// rbacPolicy is a thin value wrapper so atomic.Pointer[rbacPolicy] compiles.
// It delegates to the underlying *rbac.Policy.
type rbacPolicy struct {
	p *rbac.Policy
}

// UpdatePolicy atomically installs a new RBAC policy. It is called once after
// admin.New (if RBAC is enabled on startup) and then once after every
// successful hot reload. The server continues serving with the previous policy
// until the new one is installed.
//
// A nil policy clears RBAC; the server falls back to legacy single-token auth.
// Pass Build(...) result from serve.go after config Publish.
func (s *Server) UpdatePolicy(p *rbac.Policy) {
	if p == nil {
		s.policy.Store(nil)
		return
	}
	s.policy.Store(&rbacPolicy{p: p})
}

// currentPolicy returns the active RBAC policy, or nil when no policy is set.
func (s *Server) currentPolicy() *rbac.Policy {
	if w := s.policy.Load(); w != nil {
		return w.p
	}
	return nil
}

// authWithRBAC wraps the auth middleware with RBAC-aware identity resolution.
// When RBAC is enabled in the current policy it stores the authenticated
// Identity in the request context so downstream handlers can call
// rbac.IdentityFromContext. When RBAC is disabled or no policy is installed it
// falls back to legacy single-token auth and stores a synthetic Legacy Identity.
func (s *Server) authWithRBAC(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pol := s.currentPolicy()
		bearer := r.Header.Get("Authorization")
		now := time.Now()

		if pol != nil && pol.Enabled() {
			id, err := pol.Authenticate(bearer, now)
			if err == rbac.ErrDisabled {
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, `{"error":"forbidden","message":"principal is disabled or expired"}`, http.StatusForbidden)
				return
			}
			if err != nil {
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, `{"error":"unauthorized","message":"invalid or missing credentials"}`, http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(rbac.WithIdentity(r.Context(), id)))
			return
		}

		// RBAC disabled — fall back to legacy single-token constant-time comparison.
		if s.cfg.Token != "" {
			const prefix = "Bearer "
			h := r.Header.Get("Authorization")
			ok := len(h) > len(prefix) &&
				subtle.ConstantTimeCompare([]byte(h[len(prefix):]), []byte(s.cfg.Token)) == 1
			if !ok {
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, "401 Unauthorized", http.StatusUnauthorized)
				return
			}
			// Store a synthetic legacy identity so downstream handlers can
			// uniformly use rbac.IdentityFromContext.
			legacyID := rbac.Identity{
				Principal:   "shared",
				Role:        "admin",
				TokenID:     "(legacy)",
				Permissions: []rbac.Permission{rbac.Wildcard},
				Legacy:      true,
			}
			next.ServeHTTP(w, r.WithContext(rbac.WithIdentity(r.Context(), legacyID)))
			return
		}

		// No auth configured — allow (loopback-only deployments).
		next.ServeHTTP(w, r)
	})
}
