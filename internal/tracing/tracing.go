// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package tracing is a dependency-free seam for emitting child spans from
// packages that must stay free of OpenTelemetry imports in the lean default
// build (handler, cache, upstream). The otel build injects a real,
// OpenTelemetry-backed implementation via Set; without it (or when tracing is
// disabled) Active returns a zero-overhead no-op so callers never branch on
// whether tracing is compiled in or enabled.
//
// Child spans parent automatically: the otel server-span middleware places the
// active span in the request context, so Start(ctx, ...) on the injected tracer
// reads that parent and the new span joins the same trace. Inject writes W3C
// tracecontext into outbound headers so a proxied upstream continues the trace.
package tracing

import (
	"context"
	"net/http"
	"sync/atomic"
)

// Span is a minimal handle to an in-progress span. All methods are safe to call
// on the no-op span and after End.
type Span interface {
	// End completes the span. It must be called exactly once, typically via
	// defer.
	End()
	// RecordError marks the span as failed and attaches the error.
	RecordError(err error)
	// SetStatus records an HTTP (or numeric) status on the span and marks it as
	// errored for server-error codes.
	SetStatus(code int)
	// SetString attaches a string attribute to the span.
	SetString(key, value string)
}

// Tracer starts child spans and injects propagation headers. The otel build
// supplies an implementation; the default is a no-op.
type Tracer interface {
	// Start begins a child span named name as a child of any span in ctx. It
	// returns a context carrying the new span and the span itself.
	Start(ctx context.Context, name string) (context.Context, Span)
	// Inject writes the propagation context from ctx into h so a downstream
	// service continues the trace.
	Inject(ctx context.Context, h http.Header)
}

// holder boxes a Tracer so an interface value can live in an atomic.Pointer
// (interface values cannot be swapped atomically on their own).
type holder struct{ t Tracer }

// active is the process-wide injected tracer; nil means use the no-op.
var active atomic.Pointer[holder]

// Set installs t as the process-wide tracer, replacing any previous one. A nil
// tracer restores the no-op. It is safe to call concurrently with Active, which
// lets the otel build wire the real tracer at startup.
func Set(t Tracer) {
	if t == nil {
		active.Store(nil)
		return
	}
	active.Store(&holder{t: t})
}

// Active returns the installed tracer, or a no-op when none is set. The returned
// value is always non-nil, so callers can use it without a guard.
func Active() Tracer {
	if h := active.Load(); h != nil {
		return h.t
	}
	return noop{}
}

// noop is the zero-overhead tracer used when tracing is not compiled in or not
// enabled.
type noop struct{}

func (noop) Start(ctx context.Context, _ string) (context.Context, Span) { return ctx, noopSpan{} }
func (noop) Inject(context.Context, http.Header)                         {}

// noopSpan discards all span operations.
type noopSpan struct{}

func (noopSpan) End()                     {}
func (noopSpan) RecordError(error)        {}
func (noopSpan) SetStatus(int)            {}
func (noopSpan) SetString(string, string) {}
