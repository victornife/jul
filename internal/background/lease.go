// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package background is the seam that lets a request start work which
// legitimately outlives the request, without escaping the lifetime accounting
// of the handler generation that owns the resources the work uses.
//
// The dynamic server handler installs a Lease in every request context. A
// handler that wants to continue working after ServeHTTP returns — today only
// the response cache's stale revalidation — calls Acquire BEFORE it returns.
// Acquiring registers the operation against the generation's in-flight count,
// so the generation cannot retire (and close its gRPC connections, plugin
// runtimes and static roots) while the operation runs. The returned context is
// rooted in the process lifetime rather than the client connection: it survives
// client disconnect, but is canceled by process shutdown, by forced generation
// retirement, and by its own bounded operation deadline.
//
// The package deliberately carries no knowledge of the concrete server type, so
// the cache depends on this seam only, and the server supplies the
// implementation.
package background

import (
	"context"
	"time"
)

// Operation names a kind of background work. It is a closed set of package
// constants, never a caller- or user-supplied string, so it is safe to use as a
// metric label or log field.
type Operation string

const (
	// OpCacheRevalidate is the response cache's stale-while-revalidate refresh.
	OpCacheRevalidate Operation = "cache_revalidate"
)

// Valid reports whether o is a known operation. An unknown operation is
// rejected at acquisition so an unbounded label value can never reach metrics.
func (o Operation) Valid() bool {
	return o == OpCacheRevalidate
}

// String returns the operation name.
func (o Operation) String() string { return string(o) }

// Lease grants a request permission to keep generation-owned resources alive
// past its own completion.
type Lease interface {
	// Acquire registers one background operation. src is the originating
	// request context, from which only the allow-listed values in Detach are
	// carried over; its cancellation is deliberately NOT inherited.
	//
	// It returns the operation context, a release function, and whether the
	// operation was admitted. Acquisition fails cleanly (ok == false, nil
	// context, nil release) when the generation is retiring or already
	// canceled, so the caller simply does not start the work. release is
	// idempotent and must be called exactly once on the work's completion path.
	Acquire(src context.Context, op Operation) (ctx context.Context, release func(), ok bool)

	// Generation identifies the handler generation that owns this lease. It is
	// used to keep per-generation background state (such as the cache's
	// revalidation call map) isolated, so a new generation is never blocked by
	// an old generation's in-flight work. It is a process-local counter, not a
	// safe Prometheus label.
	Generation() uint64
}

// leaseKey is the unexported context key for a Lease.
type leaseKey struct{}

// WithLease returns a copy of ctx carrying l.
func WithLease(ctx context.Context, l Lease) context.Context {
	if l == nil {
		return ctx
	}
	return context.WithValue(ctx, leaseKey{}, l)
}

// LeaseFrom returns the lease stored in ctx, or nil when none is installed.
func LeaseFrom(ctx context.Context) Lease {
	l, _ := ctx.Value(leaseKey{}).(Lease)
	return l
}

// Acquire is the convenience form of Lease.Acquire for a request context. It
// returns ok == false when no lease is installed, which is the correct
// conservative answer: without a generation to hold, background work would have
// no owner and no shutdown ownership, so it must not start.
func Acquire(ctx context.Context, op Operation) (context.Context, func(), bool) {
	l := LeaseFrom(ctx)
	if l == nil {
		return nil, nil, false
	}
	return l.Acquire(ctx, op)
}

// Generation returns the handler generation of the lease installed in ctx, and
// whether a lease was present.
func Generation(ctx context.Context) (uint64, bool) {
	l := LeaseFrom(ctx)
	if l == nil {
		return 0, false
	}
	return l.Generation(), true
}

// DefaultMaxOperation bounds a single background operation when a Group is
// created without an explicit limit. It exists only so a misconfigured or
// zero-valued Group can never produce an unbounded operation.
const DefaultMaxOperation = 30 * time.Second
