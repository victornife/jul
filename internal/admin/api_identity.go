// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"

	"jul/internal/rbac"
)

// identityResponse is the secret-free view of the authenticated caller returned
// by GET /api/admin/me (P3-03 §33). It carries only server-derived, non-secret
// metadata: the principal name, its effective role, the public token ID, the
// resolved concrete permission set, and whether the credential is the legacy
// shared token. It never contains the raw token, its digest, or any other
// secret material.
type identityResponse struct {
	Principal   string   `json:"principal"`
	Role        string   `json:"role"`
	TokenID     string   `json:"token_id,omitempty"`
	Permissions []string `json:"permissions"`
	Legacy      bool     `json:"legacy"`
}

// handleIdentity serves GET /api/admin/me. The route is authenticated-only: any
// valid credential is accepted regardless of permission, so the Console can
// discover the current principal and gate controls proactively. The identity is
// read exclusively from the request context populated by the auth middleware;
// no client-supplied identity metadata is ever trusted.
func (s *Server) handleIdentity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	id, ok := rbac.IdentityFromContext(r.Context())
	if !ok {
		// Open mode (loopback, no token, RBAC disabled): the auth middleware
		// admits the request without attaching an identity. Every action is
		// permitted, so report a synthetic unrestricted identity that unlocks
		// the Console rather than gating it to nothing.
		id = rbac.Identity{
			Principal:   "anonymous",
			Role:        rbac.RoleAdmin,
			Permissions: []rbac.Permission{rbac.Wildcard},
			Legacy:      true,
		}
	}
	writeJSON(w, http.StatusOK, identityResponse{
		Principal:   id.Principal,
		Role:        id.Role,
		TokenID:     id.TokenID,
		Permissions: expandIdentityPermissions(id),
		Legacy:      id.Legacy,
	})
}

// expandIdentityPermissions resolves an identity's granted permissions into the
// concrete permission catalog it can exercise, expanding "*" and "<resource>:*"
// wildcards. Returning concrete permissions lets the Console gate controls with
// a simple set-membership test instead of re-implementing wildcard matching.
// The order matches rbac.Catalog() so the output is stable.
func expandIdentityPermissions(id rbac.Identity) []string {
	catalog := rbac.Catalog()
	out := make([]string, 0, len(catalog))
	for _, p := range catalog {
		if id.Has(p) {
			out = append(out, string(p))
		}
	}
	return out
}
