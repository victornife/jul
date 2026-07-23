// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package rbac

// Identity describes the authenticated caller attached to a request context.
// It is immutable after construction by Policy.Authenticate.
type Identity struct {
	// Principal is the named identity (e.g. "alice"). Legacy single-token
	// auth produces the synthetic principal "shared".
	Principal string
	// Role is the effective role name for display and audit purposes.
	Role string
	// TokenID is the opaque public identifier of the credential that was
	// presented. It is safe to log and audit; it never contains secret bytes.
	TokenID string
	// TokenDigest is the full SHA-256 credential digest used only for internal
	// equality. It must never be logged or serialized.
	TokenDigest string
	// Permissions is the resolved permission set for this identity. It is
	// pre-computed at policy build time.
	Permissions []Permission
	// Legacy is true when this identity was constructed from the single
	// shared admin token rather than a named RBAC principal.
	Legacy bool
}

// Has reports whether the identity holds the given permission.
func (id Identity) Has(p Permission) bool {
	for _, g := range id.Permissions {
		if Matches(g, p) {
			return true
		}
	}
	return false
}
