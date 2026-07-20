// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package rbac

import "context"

type contextKey struct{}

// WithIdentity stores id in ctx under the package-private context key.
// The resulting context is passed to every downstream admin handler.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// IdentityFromContext retrieves the Identity previously stored by WithIdentity.
// If no identity is present it returns the zero value and false.
func IdentityFromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(contextKey{}).(Identity)
	return id, ok
}
