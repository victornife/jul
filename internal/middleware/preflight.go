// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"context"
	"net/http"
)

// grantOriginCtxKey carries the origin EvaluatePreflight already approved from
// the decision point to the terminal emit handler, across the rate-limit/WAF
// guards, so the decision is made exactly once.
type grantOriginCtxKey struct{}

func withGrantOrigin(ctx context.Context, origin string) context.Context {
	return context.WithValue(ctx, grantOriginCtxKey{}, origin)
}

func grantOriginFrom(ctx context.Context) (string, bool) {
	o, ok := ctx.Value(grantOriginCtxKey{}).(string)
	return o, ok
}

// This file implements the CORS preflight terminator (ADR 0018 §10):
// decide-then-guard, in one layer, so a denied preflight is guarded exactly
// once (by the ordinary chain) rather than twice.
//
//	not a preflight     -> pass through untouched
//	evaluate approval   -> pure, three header fields, no side effects
//	not approved        -> pass through untouched; the ordinary chain handles it
//	approved            -> rate-limit check -> WAF check -> emit 204

// Preflight returns middleware implementing the terminator for one location.
// cors may be nil, meaning the location has no CORS policy at all, in which
// case Preflight returns nil (no wrapper, nothing allocated). rateLimit and
// waf are the location's own effective policies, evaluated only for an
// approved preflight and only when non-nil: a location with no rate policy
// gets no guard, which is consistent with an ordinary request to the same
// route. Authentication is never run here — it is the only layer an approved
// preflight skips, because Fetch sends preflights without credentials.
func Preflight(cors *CORSPolicy, rateLimit, waf Middleware) Middleware {
	if cors == nil {
		return nil
	}
	return func(next http.Handler) http.Handler {
		emit := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			grantOrigin, _ := grantOriginFrom(r.Context())
			markGeneratedResponse(r)
			cors.WritePreflightResponse(w, grantOrigin)
		})
		guarded := http.Handler(emit)
		if waf != nil {
			guarded = waf(guarded)
		}
		if rateLimit != nil {
			guarded = rateLimit(guarded)
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !IsPreflight(r) {
				next.ServeHTTP(w, r)
				return
			}
			grantOrigin, approved := cors.EvaluatePreflight(r)
			if !approved {
				// Not short-circuited: the ordinary chain (Auth, RateLimit, WAF)
				// handles it and gets whatever that route returns for OPTIONS,
				// with no Access-Control-* header added on its behalf.
				next.ServeHTTP(w, r)
				return
			}
			guarded.ServeHTTP(w, r.WithContext(withGrantOrigin(r.Context(), grantOrigin)))
		})
	}
}
