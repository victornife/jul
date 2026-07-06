// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package middleware provides composable http.Handler wrappers for
// cross-cutting concerns: request IDs, panic recovery, access logging,
// timeouts, and body-size limits.
package middleware

import "net/http"

// Middleware wraps an http.Handler with additional behavior.
type Middleware func(http.Handler) http.Handler

// Chain applies the given middleware to h. The first middleware in the list is
// the outermost wrapper (it sees the request first and the response last).
func Chain(h http.Handler, mw ...Middleware) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}
