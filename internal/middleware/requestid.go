// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

// HeaderRequestID is the header used to read/propagate the request id.
const HeaderRequestID = "X-Request-ID"

type ctxKey int

const (
	traceIDKey ctxKey = iota
	claimsKey
)

type requestScopeKey struct{}

// requestScope bundles the per-request state populated across the entry
// middleware chain (RequestID, Metrics, AccessLog, and ResponsePolicy) into a
// single allocation attached by RequestID's initial r.WithContext call, avoiding
// repeated http.Request and context.valueCtx clones down the hot path.
type requestScope struct {
	requestID      string
	labels         *HTTPMetricLabels
	upstreamReason *upstreamReasonHolder
	policyWriter   *policyWriter
	labelsBuf      HTTPMetricLabels
	reasonBuf      upstreamReasonHolder
}

func requestScopeFrom(ctx context.Context) *requestScope {
	s, _ := ctx.Value(requestScopeKey{}).(*requestScope)
	return s
}

func cloneRequestScope(ctx context.Context) *requestScope {
	if prev := requestScopeFrom(ctx); prev != nil {
		return &requestScope{
			requestID:      prev.requestID,
			labels:         prev.labels,
			upstreamReason: prev.upstreamReason,
			policyWriter:   prev.policyWriter,
		}
	}
	return &requestScope{}
}

// RequestID returns middleware that ensures every request has an id. If the
// client supplied one via X-Request-ID it is preserved; otherwise a new random
// id is generated. The id is stored in the request context and echoed in the
// response header.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			values := r.Header.Values(HeaderRequestID)
			id := ""
			if len(values) == 1 && len(values[0]) <= 128 && isCORSToken(values[0]) {
				id = values[0]
			}
			if id == "" {
				id = newID()
			}
			// The context, echoed response and forwarded request must agree.
			// Remove ambiguous or unbounded client field lines before routing.
			r.Header.Set(HeaderRequestID, id)
			w.Header().Set(HeaderRequestID, id)
			scope := &requestScope{requestID: id}
			ctx := context.WithValue(r.Context(), requestScopeKey{}, scope)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// WithRequestID returns a copy of ctx carrying the request id. It is used by
// the background-lease seam to carry the id onto work that continues after the
// request returns, so its logs stay correlated with the request that started it.
func WithRequestID(ctx context.Context, id string) context.Context {
	scope := cloneRequestScope(ctx)
	scope.requestID = id
	return context.WithValue(ctx, requestScopeKey{}, scope)
}

// RequestIDFrom returns the request id stored in ctx, or "" if absent.
func RequestIDFrom(ctx context.Context) string {
	if scope := requestScopeFrom(ctx); scope != nil {
		return scope.requestID
	}
	return ""
}

// WithTraceID returns a copy of ctx carrying the distributed-tracing trace id.
// The id flows as a plain string so this package stays free of any tracing
// dependency: the tracing middleware (in the observability package, built only
// under the `otel` tag) sets it, and access-log sinks read it via TraceIDFrom.
func WithTraceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, traceIDKey, id)
}

// TraceIDFrom returns the trace id stored in ctx, or "" if absent.
func TraceIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(traceIDKey).(string); ok {
		return v
	}
	return ""
}

// WithClaims returns a copy of ctx carrying validated authentication claims
// (for example, a JWT's decoded payload). The claims flow as a plain
// map[string]any so this package stays free of any auth/JWT dependency: the
// auth package (which performs validation) sets them, and consumers such as the
// rate-limiter key function read them via ClaimsFrom.
func WithClaims(ctx context.Context, claims map[string]any) context.Context {
	return context.WithValue(ctx, claimsKey, claims)
}

// ClaimsFrom returns the authentication claims stored in ctx, or nil if absent.
func ClaimsFrom(ctx context.Context) map[string]any {
	if v, ok := ctx.Value(claimsKey).(map[string]any); ok {
		return v
	}
	return nil
}

func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand.Read essentially never fails; fall back to a fixed marker.
		return "00000000000000ff"
	}
	return hex.EncodeToString(b[:])
}
