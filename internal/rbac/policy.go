// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package rbac

import (
	"errors"
	"fmt"
	"time"
)

// ErrUnauthenticated is returned when no valid credential is presented.
var ErrUnauthenticated = errors.New("rbac: unauthenticated")

// ErrDisabled is returned when the credential is valid but the principal is
// disabled or expired.
var ErrDisabled = errors.New("rbac: principal is disabled or expired")

// ErrForbidden is returned when the identity lacks the required permission.
var ErrForbidden = errors.New("rbac: forbidden")

// Policy is an immutable snapshot of the admin RBAC configuration at a point
// in time. It is safe for concurrent use from multiple goroutines. A new Policy
// is built atomically on each successful config apply.
type Policy struct {
	// entries is keyed by tokenID (the first 12 hex chars of the SHA-256 hash)
	// for O(1) lookup, then constant-time hash comparison to confirm.
	entries map[string]*principalEntry // tokenID → entry
	// all holds every entry in insertion order for diagnostics.
	all []*principalEntry
	// enabled reflects whether RBAC was enabled when this policy was built.
	enabled bool
	// catalogVersion is a summary fingerprint of the permission catalog version
	// embedded in this policy, for diagnostics and debug.
	catalogVersion int
}

// Build constructs an immutable Policy from the provided config.AdminRBACConfig.
// It is called once during preflight (to validate) and once after Publish (to
// install). The resolved token values MUST already have been expanded from any
// secret references before calling Build; plaintext tokens are hashed here and
// not retained.
//
// customRoles maps role name → raw permission strings, as parsed from
// [[admin.rbac.roles]]. principals is the list of principals from
// [[admin.rbac.principals]]. defaultRole is applied to the legacy shared token
// if legacyToken is non-empty.
func Build(
	enabled bool,
	defaultRole string,
	customRoles map[string][]string,
	principals []PrincipalDef,
	legacyToken string,
	now time.Time,
) (*Policy, error) {
	p := &Policy{
		enabled:        enabled,
		entries:        make(map[string]*principalEntry),
		catalogVersion: len(catalog),
	}

	if !enabled {
		// RBAC disabled: install at most the legacy token entry so legacy auth
		// still works even while a Policy object exists in the server.
		if legacyToken != "" {
			entry, err := buildLegacyEntry(legacyToken, defaultRole, customRoles)
			if err != nil {
				return nil, err
			}
			p.entries[entry.tokenID] = entry
			p.all = append(p.all, entry)
		}
		return p, nil
	}

	// Build the merged role→permissions map (predefined + custom).
	rolePerms, err := buildRoleMap(customRoles)
	if err != nil {
		return nil, err
	}

	// Add a synthetic legacy principal when a legacy token is configured
	// alongside RBAC so migration can be gradual.
	if legacyToken != "" {
		entry, err := buildLegacyEntry(legacyToken, defaultRole, customRoles)
		if err != nil {
			return nil, err
		}
		if existing, dup := p.entries[entry.tokenID]; dup {
			return nil, fmt.Errorf("rbac: duplicate token ID %q (principals %q and %q share the same credential)",
				entry.tokenID, existing.name, entry.name)
		}
		p.entries[entry.tokenID] = entry
		p.all = append(p.all, entry)
	}

	// Add each named principal.
	for _, def := range principals {
		if def.Token == "" {
			return nil, fmt.Errorf("rbac: principal %q has no token", def.Name)
		}
		perms, ok := rolePerms[def.Role]
		if !ok {
			return nil, fmt.Errorf("rbac: principal %q references unknown role %q", def.Name, def.Role)
		}
		h := hashToken(def.Token)
		id := tokenID(h)
		entry := &principalEntry{
			name:        def.Name,
			role:        def.Role,
			tokenHash:   h,
			tokenID:     id,
			permissions: perms,
			disabled:    def.Disabled,
			expiresAt:   def.ExpiresAt,
		}
		if existing, dup := p.entries[id]; dup {
			return nil, fmt.Errorf("rbac: duplicate token ID %q (principals %q and %q share the same credential)",
				id, existing.name, entry.name)
		}
		p.entries[id] = entry
		p.all = append(p.all, entry)
	}

	if len(p.all) == 0 {
		return nil, fmt.Errorf("rbac: RBAC is enabled but no principals or legacy token are configured")
	}

	// Verify at least one enabled principal has admin-capable permissions so
	// the operator cannot accidentally lock themselves out of admin operations.
	if !hasActivePrincipalWith(p.all, now, AdminManage) {
		return nil, fmt.Errorf("rbac: no enabled, non-expired principal has the %q permission; at least one admin-capable principal is required", AdminManage)
	}

	return p, nil
}

// PrincipalDef holds the runtime (secrets-expanded) definition of one named
// principal. The caller must have resolved any ${env:}//${file:}//${secret:}
// references in Token before calling Build.
type PrincipalDef struct {
	Name      string
	Role      string
	Token     string // plaintext, already secret-expanded
	Disabled  bool
	ExpiresAt time.Time
}

// Authenticate validates the Authorization header value and returns the
// resolved Identity. It returns ErrUnauthenticated when no matching credential
// is found, ErrDisabled when the principal is inactive, or a zero Identity if
// RBAC is disabled (callers must fall back to legacy auth).
func (p *Policy) Authenticate(bearer string, now time.Time) (Identity, error) {
	raw, ok := extractBearer(bearer)
	if !ok {
		return Identity{}, ErrUnauthenticated
	}
	h := hashToken(raw)
	id := tokenID(h)

	entry, exists := p.entries[id]
	if !exists {
		return Identity{}, ErrUnauthenticated
	}
	// Confirm hash match with constant-time comparison.
	if !entry.matchToken(raw) {
		return Identity{}, ErrUnauthenticated
	}
	if !entry.active(now) {
		return Identity{}, ErrDisabled
	}
	return Identity{
		Principal:   entry.name,
		Role:        entry.role,
		TokenID:     entry.tokenID,
		Permissions: entry.permissions,
		Legacy:      entry.legacy,
	}, nil
}

// Authorize reports whether id holds the given permission.
func (p *Policy) Authorize(id Identity, perm Permission) bool {
	return id.Has(perm)
}

// Enabled returns true when RBAC is active.
func (p *Policy) Enabled() bool { return p.enabled }

// PrincipalCount returns the number of configured principals (including legacy).
func (p *Policy) PrincipalCount() int { return len(p.all) }

// --- helpers ---

func buildRoleMap(customRoles map[string][]string) (map[string][]Permission, error) {
	rolePerms := make(map[string][]Permission, len(predefinedRoles)+len(customRoles))
	for name, perms := range predefinedRoles {
		rolePerms[name] = perms
	}
	for name, rawPerms := range customRoles {
		resolved, err := resolveCustom(rawPerms)
		if err != nil {
			return nil, fmt.Errorf("rbac: custom role %q: %w", name, err)
		}
		rolePerms[name] = resolved
	}
	return rolePerms, nil
}

func buildLegacyEntry(legacyToken, defaultRole string, customRoles map[string][]string) (*principalEntry, error) {
	rolePerms, err := buildRoleMap(customRoles)
	if err != nil {
		return nil, err
	}
	role := defaultRole
	if role == "" {
		role = RoleAdmin
	}
	perms, ok := rolePerms[role]
	if !ok {
		return nil, fmt.Errorf("rbac: legacy principal default_role %q is unknown", role)
	}
	h := hashToken(legacyToken)
	id := tokenID(h)
	return &principalEntry{
		name:        "shared",
		role:        role,
		tokenHash:   h,
		tokenID:     id,
		permissions: perms,
		legacy:      true,
	}, nil
}

func hasActivePrincipalWith(entries []*principalEntry, now time.Time, perm Permission) bool {
	for _, e := range entries {
		if !e.active(now) {
			continue
		}
		id := Identity{Permissions: e.permissions}
		if id.Has(perm) {
			return true
		}
	}
	return false
}
