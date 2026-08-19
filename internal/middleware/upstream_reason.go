// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"context"
	"sync/atomic"
)

// upstreamReasonKey carries the holder, not the value: the access-log
// middleware installs it on the way in and reads it on the way out, while the
// proxy handler deeper in the chain is the one that knows the reason. A plain
// context value cannot travel back up.
type upstreamReasonKeyType struct{}

var upstreamReasonKey upstreamReasonKeyType

type upstreamReasonHolder struct{ v atomic.Pointer[string] }

// WithUpstreamReason returns a context that can carry an upstream failure
// reason set by a handler further down the chain.
func WithUpstreamReason(ctx context.Context) context.Context {
	return context.WithValue(ctx, upstreamReasonKey, &upstreamReasonHolder{})
}

// SetUpstreamReason records the bounded reason an upstream call failed. It is a
// no-op when the context carries no holder, so a handler used outside the
// access-log chain needs no special case.
//
// The value must come from the closed taxonomy in internal/upstream. This
// package takes a string rather than importing it, keeping the dependency
// pointing one way, and the equality test against the enum lives where both are
// visible.
func SetUpstreamReason(ctx context.Context, reason string) {
	if reason == "" {
		return
	}
	if h, ok := ctx.Value(upstreamReasonKey).(*upstreamReasonHolder); ok {
		h.v.Store(&reason)
	}
}

// UpstreamReasonFrom returns the reason recorded for this request, or "".
func UpstreamReasonFrom(ctx context.Context) string {
	h, ok := ctx.Value(upstreamReasonKey).(*upstreamReasonHolder)
	if !ok {
		return ""
	}
	if v := h.v.Load(); v != nil {
		return *v
	}
	return ""
}
