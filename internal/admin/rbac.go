// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"jul/internal/config"
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
// A nil policy clears RBAC; the server falls back to legacy single-token auth
// (or anonymous access when no legacy token is configured).
func (s *Server) UpdatePolicy(p *rbac.Policy) {
	if p == nil {
		s.policy.Store(nil)
		return
	}
	s.policy.Store(&rbacPolicy{p: p})
}

// UpdateLiveAdminConfig stores the latest effective admin config so auth
// handlers and status endpoints see live token/settings without restarting.
// The listener address is never updated here; it remains startup-bound.
func (s *Server) UpdateLiveAdminConfig(cfg config.AdminConfig) {
	cfg.Listen = s.cfg.Listen // guard against accidental address changes
	s.liveCfg.Store(&cfg)
}

// currentAdminConfig returns the most recently applied admin config. It falls
// back to the startup config when no live config has been stored.
func (s *Server) currentAdminConfig() config.AdminConfig {
	if c := s.liveCfg.Load(); c != nil {
		return *c
	}
	return s.cfg
}

// currentPolicy returns the active RBAC policy, or nil when no policy is set.
func (s *Server) currentPolicy() *rbac.Policy {
	if w := s.policy.Load(); w != nil {
		return w.p
	}
	return nil
}

// requirePermissionForMethods wraps a handler with per-method authorization.
// It selects the required rbac.Permission from the supplied map based on the
// incoming request method. Unlisted methods receive 405 before authentication.
// This is the method-aware variant used by routes whose catalog entry declares
// different permissions for different methods (e.g. GET /api/config/raw vs
// POST /api/config/raw).
func (s *Server) requirePermissionForMethods(perms map[string]rbac.Permission, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		perm, ok := perms[r.Method]
		if !ok {
			allowed := make([]string, 0, len(perms))
			for m := range perms {
				allowed = append(allowed, m)
			}
			methodNotAllowed(w, strings.Join(allowed, ", "))
			return
		}
		s.requirePermission(perm, next).ServeHTTP(w, r)
	})
}

// requirePermission is the canonical authn+authz middleware for the admin API.
// It implements the full 4-step stack defined in P3-02:
//
//  1. Parse Bearer header.
//  2. Authenticate against current immutable policy.
//  3. Store Identity in request context.
//  4. Authorize required permission → 403 JSON if denied.
//
// When RBAC is disabled it falls back to legacy single-token constant-time
// comparison and synthesises a wildcard Identity so downstream handlers can
// always use rbac.IdentityFromContext uniformly.
//
// Return values:
//   - 401 + WWW-Authenticate for absent/invalid/expired credentials.
//   - 403 JSON for authenticated but unauthorized.
func (s *Server) requirePermission(perm rbac.Permission, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pol := s.currentPolicy()
		bearer := r.Header.Get("Authorization")
		now := time.Now()

		var id rbac.Identity
		if pol != nil && pol.Enabled() {
			// ── RBAC path ─────────────────────────────────────────────────
			authID, err := pol.Authenticate(bearer, now)
			if err == rbac.ErrDisabled {
				w.Header().Set("WWW-Authenticate", "Bearer")
				writeJSON(w, http.StatusForbidden, map[string]string{
					"error":   "forbidden",
					"message": "principal is disabled or expired",
				})
				return
			}
			if err != nil {
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, "401 Unauthorized", http.StatusUnauthorized)
				return
			}
			if !pol.Authorize(authID, perm) {
				writeForbidden(w, perm, authID)
				return
			}
			id = authID
		} else {
			// ── Legacy single-token path ───────────────────────────────────
			adminCfg := s.currentAdminConfig()
			if adminCfg.Token != "" {
				const prefix = "Bearer "
				h := r.Header.Get("Authorization")
				ok := len(h) > len(prefix) &&
					subtle.ConstantTimeCompare([]byte(h[len(prefix):]), []byte(adminCfg.Token)) == 1
				if !ok {
					w.Header().Set("WWW-Authenticate", "Bearer")
					http.Error(w, "401 Unauthorized", http.StatusUnauthorized)
					return
				}
			}
			// Legacy mode grants all permissions (wildcard Identity).
			id = rbac.Identity{
				Principal:   "shared",
				Role:        "admin",
				TokenID:     "(legacy)",
				Permissions: []rbac.Permission{rbac.Wildcard},
				Legacy:      true,
			}
		}
		next.ServeHTTP(w, r.WithContext(rbac.WithIdentity(r.Context(), id)))
	})
}

// authWithRBAC provides the same authn+authz stack but without a specific
// permission requirement (used by auth() which delegates to this). Routes
// that need per-route permission gates use requirePermission instead.
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
				http.Error(w, "401 Unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(rbac.WithIdentity(r.Context(), id)))
			return
		}

		// RBAC disabled — fall back to legacy single-token constant-time comparison.
		adminCfg := s.currentAdminConfig()
		if adminCfg.Token != "" {
			const prefix = "Bearer "
			h := r.Header.Get("Authorization")
			ok := len(h) > len(prefix) &&
				subtle.ConstantTimeCompare([]byte(h[len(prefix):]), []byte(adminCfg.Token)) == 1
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

// writeForbidden writes a structured 403 JSON response. It does NOT reveal
// whether another principal/token exists; it only reports the required
// permission and the authenticated principal's role.
func writeForbidden(w http.ResponseWriter, required rbac.Permission, id rbac.Identity) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	body := map[string]string{
		"error":     "forbidden",
		"required":  string(required),
		"principal": id.Principal,
		"role":      id.Role,
	}
	_ = json.NewEncoder(w).Encode(body)
}
