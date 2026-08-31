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

// authMode is the authoritative authentication mode carried by an
// authSnapshot. It is derived once at install time from the desired admin
// configuration and the built policy, and is never re-inferred from whether a
// policy pointer happens to be nil (H-01).
type authMode int

const (
	// authModeOpen allows requests without credentials (loopback-only
	// deployments with no legacy token and RBAC disabled).
	authModeOpen authMode = iota
	// authModeLegacy authenticates against a single shared bearer token and
	// grants a wildcard identity.
	authModeLegacy
	// authModeRBAC authenticates and authorizes against the installed policy.
	authModeRBAC
	// authModeBlocked fails closed with 503: RBAC is the desired mode but no
	// valid enabled policy is installed. It is installed explicitly on policy
	// build failure so a failed RBAC enablement can never silently retain the
	// previous legacy/open access.
	authModeBlocked
)

// authSnapshot is the immutable, atomically-installed authentication state.
// It pairs the effective admin configuration with the built RBAC policy and
// the derived mode so middleware observes a single, internally-consistent view
// with one atomic pointer load. This closes the two-store interleaving race
// (H-01) where policy, the enabled flag, and the live config were updated
// separately and a concurrent request could observe a transient anonymous or
// legacy-fallback window during an RBAC transition.
type authSnapshot struct {
	mode   authMode
	cfg    config.AdminConfig
	policy *rbac.Policy
	gen    string
}

// PreparedAuth is a fully built immutable authentication snapshot that can be
// installed with one no-fail atomic store at the reload Publish boundary.
type PreparedAuth struct {
	snapshot *authSnapshot
}

// PrepareAuth builds candidate authentication state without mutating the live
// server. generation identifies the effective auth contents, not install order.
func PrepareAuth(cfg config.AdminConfig, p *rbac.Policy) *PreparedAuth {
	return &PreparedAuth{snapshot: deriveAuthSnapshot(cfg, p, authGeneration(cfg, p))}
}

// CommitPreparedAuth installs the exact snapshot returned by PrepareAuth.
func (s *Server) CommitPreparedAuth(prepared *PreparedAuth) {
	if prepared != nil && prepared.snapshot != nil {
		s.authState.Store(prepared.snapshot)
	}
}

// AuthGeneration returns a safe digest of the currently installed effective
// auth state. It contains no plaintext credentials.
func (s *Server) AuthGeneration() string { return s.currentAuth().gen }

func authGeneration(cfg config.AdminConfig, p *rbac.Policy) string {
	h := sha256.New()
	_, _ = h.Write([]byte(strconv.FormatBool(cfg.Enabled)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(cfg.Listen))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(strconv.FormatBool(cfg.ConsoleEnabled())))
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

// deriveAuthSnapshot computes the authoritative snapshot from the desired
// admin configuration and the built policy.
//
// Mode selection is intentionally explicit and does not treat a nil policy as
// "legacy":
//
//   - A valid enabled policy → RBAC.
//   - RBAC desired in config but no valid enabled policy → Blocked (fail closed).
//   - A legacy shared token configured → Legacy.
//   - Otherwise → Open.
func deriveAuthSnapshot(cfg config.AdminConfig, p *rbac.Policy, gen string) *authSnapshot {
	var m authMode
	switch {
	case p != nil && p.Enabled():
		m = authModeRBAC
	case cfg.RBAC.Enabled:
		// Desired mode is RBAC but the policy is absent or disabled: fail
		// closed instead of falling back to legacy/open.
		m = authModeBlocked
	case cfg.Token != "":
		m = authModeLegacy
	default:
		m = authModeOpen
	}
	return &authSnapshot{mode: m, cfg: cfg, policy: p, gen: gen}
}

// installAuth atomically swaps in a freshly derived snapshot. The generation
// string is monotonic so callers/tests can correlate transitions.
func (s *Server) installAuth(cfg config.AdminConfig, p *rbac.Policy) {
	gen := strconv.FormatUint(s.authGen.Add(1), 10)
	s.authState.Store(deriveAuthSnapshot(cfg, p, gen))
}

// currentAuth returns the active snapshot, synthesising one from the startup
// config when nothing has been installed yet (should not happen after New).
func (s *Server) currentAuth() *authSnapshot {
	if a := s.authState.Load(); a != nil {
		return a
	}
	return deriveAuthSnapshot(s.cfg, nil, "0")
}

// UpdateAuth atomically installs a new authentication snapshot as a single
// pointer swap. It is the canonical entry point used by the reload hook so
// configuration, mode, and policy transition together with no intermediate
// window (H-01).
//
// The listener address is never changed here; it remains startup-bound.
//
// Passing a nil policy while cfg.RBAC.Enabled is true installs an explicit
// Blocked state (fail closed) rather than falling back to legacy/open.
func (s *Server) UpdateAuth(cfg config.AdminConfig, p *rbac.Policy) {
	cfg.Listen = s.cfg.Listen
	s.installAuth(cfg, p)
}

// UpdatePolicy installs a new RBAC policy while preserving the current
// effective admin configuration. Retained for the startup path and tests; it
// delegates to the atomic snapshot install. A nil policy clears RBAC and the
// mode is re-derived from the current configuration.
func (s *Server) UpdatePolicy(p *rbac.Policy) {
	cur := s.currentAuth()
	s.installAuth(cur.cfg, p)
}

// UpdateLiveAdminConfig installs the latest effective admin config while
// preserving the current policy, as a single atomic snapshot swap. The
// listener address is never updated; it remains startup-bound.
func (s *Server) UpdateLiveAdminConfig(cfg config.AdminConfig) {
	cfg.Listen = s.cfg.Listen
	cur := s.currentAuth()
	s.installAuth(cfg, cur.policy)
}

// currentAdminConfig returns the most recently applied admin config.
func (s *Server) currentAdminConfig() config.AdminConfig {
	return s.currentAuth().cfg
}

// currentPolicy returns the active RBAC policy, or nil when no policy is set.
func (s *Server) currentPolicy() *rbac.Policy {
	return s.currentAuth().policy
}

// requirePermissionForMethods wraps a handler with per-method authorization.
// It selects the required rbac.Permission from the supplied map based on the
// incoming request method. Unlisted methods receive 405 before authentication.
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

// legacyIdentity is the synthetic wildcard identity granted in legacy and open
// modes so downstream handlers can uniformly use rbac.IdentityFromContext.
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

// writeRBACUnavailable writes the fail-closed 503 used when the desired mode is
// RBAC but no valid policy is installed (Blocked).
//
// On an external route it renders the §26 envelope instead: the condition is the
// same, only the audience differs.
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

// writeUnauthenticated writes the 401 for a missing, invalid, disabled or
// expired credential. It carries no signal about whether the addressed resource
// exists (ADR 0019 §28).
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

// checkLegacyToken performs the constant-time legacy shared-token comparison.
// It returns true when access is granted (either a matching token or no token
// configured, i.e. open access).
func checkLegacyToken(r *http.Request, token string) bool {
	if token == "" {
		return true
	}
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	return len(h) > len(prefix) &&
		subtle.ConstantTimeCompare([]byte(h[len(prefix):]), []byte(token)) == 1
}

// requirePermission is the canonical authn+authz middleware for the admin API.
// It reads a single immutable auth snapshot and dispatches on its mode:
//
//   - Blocked → 503 (fail closed; never falls through to legacy/open).
//   - RBAC    → authenticate + authorize against the snapshot policy.
//   - Legacy  → constant-time shared-token compare, wildcard identity.
//   - Open    → allow, wildcard identity.
func (s *Server) requirePermission(perm rbac.Permission, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snap := s.currentAuth()
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
			return

		default: // authModeLegacy, authModeOpen
			if !checkLegacyToken(r, snap.cfg.Token) {
				writeUnauthenticated(w, r, "")
				return
			}
			next.ServeHTTP(w, r.WithContext(rbac.WithIdentity(r.Context(), legacyIdentity(snap.cfg.Token))))
			return
		}
	})
}

// requireAnyPermission is the canonical authn+authz middleware for routes that
// grant access when the authenticated principal holds ANY one of several
// permissions (logical OR). Authentication happens exactly once; only the
// authorization decision loops over the allowed permissions. This lets a
// more-privileged capability (e.g. config:apply) satisfy a route that also
// accepts a less-privileged read (e.g. status:read) without granting the
// second permission to the principal (AC-02).
//
// It dispatches on the same immutable snapshot mode as requirePermission:
//
//   - Blocked → 503 (fail closed; never falls through to legacy/open).
//   - RBAC    → authenticate once, then authorize against each candidate.
//   - Legacy  → constant-time shared-token compare, wildcard identity.
//   - Open    → allow, wildcard identity.
func (s *Server) requireAnyPermission(perms []rbac.Permission, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snap := s.currentAuth()
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
			return

		default: // authModeLegacy, authModeOpen
			if !checkLegacyToken(r, snap.cfg.Token) {
				writeUnauthenticated(w, r, "")
				return
			}
			next.ServeHTTP(w, r.WithContext(rbac.WithIdentity(r.Context(), legacyIdentity(snap.cfg.Token))))
			return
		}
	})
}

// authWithRBAC provides the same authn stack without a specific permission
// requirement (used by auth()). It dispatches on the same single snapshot mode.
func (s *Server) authWithRBAC(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snap := s.currentAuth()
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
			return

		case authModeLegacy:
			if !checkLegacyToken(r, snap.cfg.Token) {
				writeUnauthenticated(w, r, "")
				return
			}
			next.ServeHTTP(w, r.WithContext(rbac.WithIdentity(r.Context(), legacyIdentity(snap.cfg.Token))))
			return

		default: // authModeOpen — no auth configured (loopback-only)
			next.ServeHTTP(w, r)
			return
		}
	})
}

// writeForbidden writes a structured 403 JSON response. It does NOT reveal
// whether another principal/token exists; it only reports the required
// permission and the authenticated principal's role.
//
// On an external route it renders the §26 envelope, whose `details` carries the
// required permission and deliberately not the principal or the role — those
// are Console affordances, not part of the published contract.
func writeForbidden(w http.ResponseWriter, r *http.Request, required rbac.Permission, id rbac.Identity) {
	if _, external := externalContract(r.Context()); external {
		writeAPIError(w, r, adminapi.New(adminapi.CodeForbidden).
			WithDetails(adminapi.Details{RequiredPermission: string(required)}))
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

// writeForbiddenAny writes a structured 403 JSON response for routes that
// accept any one of several permissions. Like writeForbidden it reveals only
// the required permissions and the authenticated principal's role, never
// whether another principal/token exists. The accepted permissions are listed
// under "required_any" so the caller can see which capability would grant
// access.
func writeForbiddenAny(w http.ResponseWriter, r *http.Request, accepted []rbac.Permission, id rbac.Identity) {
	acceptedStrings := make([]string, 0, len(accepted))
	for _, p := range accepted {
		acceptedStrings = append(acceptedStrings, string(p))
	}
	if _, external := externalContract(r.Context()); external {
		writeAPIError(w, r, adminapi.New(adminapi.CodeForbidden).
			WithDetails(adminapi.Details{RequiredPermission: strings.Join(acceptedStrings, " or ")}))
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
