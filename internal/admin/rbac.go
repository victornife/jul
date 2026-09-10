// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"jul/internal/adminapi"
	"jul/internal/config"
	"jul/internal/rbac"
)

type authMode int

const (
	authModeOpen authMode = iota
	authModeLegacy
	authModeRBAC
	authModeBlocked
)

// authSnapshot is the immutable, atomically-installed admin runtime generation.
// #95 introduced it for authentication; #157 deliberately extends its use to
// Console and upload policy because cfg already contains the complete effective
// AdminConfig. Requests pin this exact object once at mux entry.
type authSnapshot struct {
	mode            authMode
	cfg             config.AdminConfig
	policy          *rbac.Policy
	gen             string
	consoleCompiled bool
	pluginsCompiled bool
	uploadDirHealth string
}

type PreparedAuth struct {
	snapshot *authSnapshot
	audit    *preparedAuditSink
}

func PrepareAuth(cfg config.AdminConfig, p *rbac.Policy) *PreparedAuth {
	return &PreparedAuth{snapshot: deriveAuthSnapshot(cfg, p, authGeneration(cfg, p))}
}

func (s *Server) CommitPreparedAuth(prepared *PreparedAuth) {
	if prepared != nil && prepared.snapshot != nil {
		s.authState.Store(prepared.snapshot)
	}
}

func (s *Server) AuthGeneration() string { return s.currentAuth().gen }

func authGeneration(cfg config.AdminConfig, p *rbac.Policy) string {
	h := sha256.New()
	_, _ = h.Write([]byte(strconv.FormatBool(cfg.Enabled)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(cfg.Listen))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(strconv.FormatBool(cfg.ConsoleEnabled())))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(strconv.FormatBool(pluginUploadEnabled(cfg))))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(strconv.Itoa(cfg.PluginUploadMaxSize)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(normalizePluginUploadDir(cfg.PluginUploadDir)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(strconv.FormatBool(cfg.RBAC.Enabled)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(rbac.TokenDigest(cfg.Token)))
	if p != nil {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(p.Fingerprint()))
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

func deriveAuthSnapshot(cfg config.AdminConfig, p *rbac.Policy, gen string) *authSnapshot {
	var m authMode
	switch {
	case p != nil && p.Enabled():
		m = authModeRBAC
	case cfg.RBAC.Enabled:
		m = authModeBlocked
	case cfg.Token != "":
		m = authModeLegacy
	default:
		m = authModeOpen
	}
	return &authSnapshot{mode: m, cfg: cfg, policy: p, gen: gen}
}

func (s *Server) installAuth(cfg config.AdminConfig, p *rbac.Policy) {
	gen := strconv.FormatUint(s.authGen.Add(1), 10)
	s.authState.Store(s.completeAdminRuntimeSnapshot(deriveAuthSnapshot(cfg, p, gen)))
}

func (s *Server) currentAuth() *authSnapshot {
	if a := s.authState.Load(); a != nil {
		return a
	}
	return s.completeAdminRuntimeSnapshot(deriveAuthSnapshot(s.cfg, nil, "0"))
}

func (s *Server) UpdateAuth(cfg config.AdminConfig, p *rbac.Policy) {
	cfg.Listen = s.cfg.Listen
	s.installAuth(cfg, p)
}

func (s *Server) UpdatePolicy(p *rbac.Policy) {
	cur := s.currentAuth()
	s.installAuth(cur.cfg, p)
}

func (s *Server) UpdateLiveAdminConfig(cfg config.AdminConfig) {
	cfg.Listen = s.cfg.Listen
	cur := s.currentAuth()
	s.installAuth(cfg, cur.policy)
}

func (s *Server) currentAdminConfig() config.AdminConfig { return s.currentAuth().cfg }
func (s *Server) currentPolicy() *rbac.Policy            { return s.currentAuth().policy }

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

func legacyIdentity(token string) rbac.Identity {
	return rbac.Identity{
		Principal:   "shared",
		Role:        "admin",
		TokenID:     "(legacy)",
		TokenDigest: rbac.TokenDigest(token),
		Permissions: []rbac.Permission{rbac.Wildcard},
		Legacy:      true,
	}
}

func writeRBACUnavailable(w http.ResponseWriter, r *http.Request) {
	if _, external := externalContract(r.Context()); external {
		writeAPIError(w, r, adminapi.New(adminapi.CodeStorageUnavailable).WithDetails(adminapi.Details{}))
		return
	}
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{
		"error":   "rbac_unavailable",
		"message": "RBAC is enabled but no valid policy is installed; check server logs for details.",
	})
}

func writeUnauthenticated(w http.ResponseWriter, r *http.Request, reason string) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	if _, external := externalContract(r.Context()); external {
		msg := "No valid credential was presented."
		if reason != "" {
			msg = reason
		}
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeUnauthenticated, "%s", msg))
		return
	}
	if reason != "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error":   "unauthorized",
			"message": reason,
		})
		return
	}
	http.Error(w, "401 Unauthorized", http.StatusUnauthorized)
}

func checkLegacyToken(r *http.Request, token string) bool {
	if token == "" {
		return true
	}
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	return len(h) > len(prefix) && subtle.ConstantTimeCompare([]byte(h[len(prefix):]), []byte(token)) == 1
}

// requirePermission reuses the generation pinned by captureAdminRuntimeSnapshot.
// Direct unit invocations without the outer mux still fall back to one live load.
func (s *Server) requirePermission(perm rbac.Permission, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snap := s.requestAdminSnapshot(r)
		now := time.Now()
		switch snap.mode {
		case authModeBlocked:
			writeRBACUnavailable(w, r)
			return
		case authModeRBAC:
			bearer := r.Header.Get("Authorization")
			authID, err := snap.policy.Authenticate(bearer, now)
			if err == rbac.ErrDisabled {
				writeUnauthenticated(w, r, "The principal is disabled or expired.")
				return
			}
			if err != nil {
				writeUnauthenticated(w, r, "")
				return
			}
			if !snap.policy.Authorize(authID, perm) {
				writeForbidden(w, r, perm, authID)
				return
			}
			next.ServeHTTP(w, r.WithContext(rbac.WithIdentity(r.Context(), authID)))
		default:
			if !checkLegacyToken(r, snap.cfg.Token) {
				writeUnauthenticated(w, r, "")
				return
			}
			next.ServeHTTP(w, r.WithContext(rbac.WithIdentity(r.Context(), legacyIdentity(snap.cfg.Token))))
		}
	})
}

func (s *Server) requireAnyPermission(perms []rbac.Permission, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snap := s.requestAdminSnapshot(r)
		now := time.Now()
		switch snap.mode {
		case authModeBlocked:
			writeRBACUnavailable(w, r)
			return
		case authModeRBAC:
			bearer := r.Header.Get("Authorization")
			authID, err := snap.policy.Authenticate(bearer, now)
			if err == rbac.ErrDisabled {
				writeUnauthenticated(w, r, "The principal is disabled or expired.")
				return
			}
			if err != nil {
				writeUnauthenticated(w, r, "")
				return
			}
			for _, perm := range perms {
				if snap.policy.Authorize(authID, perm) {
					next.ServeHTTP(w, r.WithContext(rbac.WithIdentity(r.Context(), authID)))
					return
				}
			}
			writeForbiddenAny(w, r, perms, authID)
		default:
			if !checkLegacyToken(r, snap.cfg.Token) {
				writeUnauthenticated(w, r, "")
				return
			}
			next.ServeHTTP(w, r.WithContext(rbac.WithIdentity(r.Context(), legacyIdentity(snap.cfg.Token))))
		}
	})
}

func (s *Server) authWithRBAC(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snap := s.requestAdminSnapshot(r)
		now := time.Now()
		switch snap.mode {
		case authModeBlocked:
			writeRBACUnavailable(w, r)
			return
		case authModeRBAC:
			bearer := r.Header.Get("Authorization")
			id, err := snap.policy.Authenticate(bearer, now)
			if err == rbac.ErrDisabled {
				writeUnauthenticated(w, r, "The principal is disabled or expired.")
				return
			}
			if err != nil {
				writeUnauthenticated(w, r, "")
				return
			}
			next.ServeHTTP(w, r.WithContext(rbac.WithIdentity(r.Context(), id)))
		case authModeLegacy:
			if !checkLegacyToken(r, snap.cfg.Token) {
				writeUnauthenticated(w, r, "")
				return
			}
			next.ServeHTTP(w, r.WithContext(rbac.WithIdentity(r.Context(), legacyIdentity(snap.cfg.Token))))
		default:
			next.ServeHTTP(w, r)
		}
	})
}

func writeForbidden(w http.ResponseWriter, r *http.Request, required rbac.Permission, id rbac.Identity) {
	if _, external := externalContract(r.Context()); external {
		writeAPIError(w, r, adminapi.New(adminapi.CodeForbidden).WithDetails(adminapi.Details{RequiredPermission: string(required)}))
		return
	}
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

func writeForbiddenAny(w http.ResponseWriter, r *http.Request, accepted []rbac.Permission, id rbac.Identity) {
	acceptedStrings := make([]string, 0, len(accepted))
	for _, p := range accepted {
		acceptedStrings = append(acceptedStrings, string(p))
	}
	if _, external := externalContract(r.Context()); external {
		writeAPIError(w, r, adminapi.New(adminapi.CodeForbidden).WithDetails(adminapi.Details{RequiredPermission: strings.Join(acceptedStrings, " or ")}))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	body := map[string]any{
		"error":        "forbidden",
		"required_any": acceptedStrings,
		"principal":    id.Principal,
		"role":         id.Role,
	}
	_ = json.NewEncoder(w).Encode(body)
}
