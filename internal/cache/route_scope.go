// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
)

// RouteOwner is an opaque, process-local cache ownership token.
type RouteOwner [32]byte

// RouteCompatibility is a bounded, opaque policy compatibility token.
type RouteCompatibility [16]byte

// RouteScope keeps route ownership and policy compatibility separate.
type RouteScope struct {
	Owner         RouteOwner
	Compatibility RouteCompatibility
}

type routeScopeKey struct{}
type responseHookBypassKey struct{}

func (c *Cache) initRouteOwnership() error {
	if _, err := rand.Read(c.routeBoot[:]); err != nil {
		return fmt.Errorf("cache: initialize route ownership: %w", err)
	}
	return nil
}

// NewRouteOwner creates a token independent of request and configuration values.
func (c *Cache) NewRouteOwner() (RouteOwner, error) {
	var owner RouteOwner
	if c == nil {
		return owner, fmt.Errorf("cache: cannot create route ownership without a cache")
	}
	copy(owner[:16], c.routeBoot[:])
	if _, err := rand.Read(owner[16:]); err != nil {
		return RouteOwner{}, fmt.Errorf("cache: create route ownership: %w", err)
	}
	var token [16]byte
	copy(token[:], owner[16:])
	if token == ([16]byte{}) {
		return RouteOwner{}, fmt.Errorf("cache: route ownership token is invalid")
	}
	return owner, nil
}

func (c *Cache) validRouteOwner(owner RouteOwner) bool {
	if c == nil || owner == (RouteOwner{}) {
		return false
	}
	var boot [16]byte
	copy(boot[:], owner[:16])
	var token [16]byte
	copy(token[:], owner[16:])
	return boot == c.routeBoot && token != ([16]byte{})
}

// NewRouteCompatibility creates a token independent of request and configuration values.
func (c *Cache) NewRouteCompatibility() (RouteCompatibility, error) {
	var compatibility RouteCompatibility
	if c == nil {
		return compatibility, fmt.Errorf("cache: cannot create route compatibility without a cache")
	}
	if _, err := rand.Read(compatibility[:]); err != nil {
		return RouteCompatibility{}, fmt.Errorf("cache: create route compatibility: %w", err)
	}
	if compatibility == (RouteCompatibility{}) {
		return RouteCompatibility{}, fmt.Errorf("cache: route compatibility token is invalid")
	}
	return compatibility, nil
}

func scopeForRequest(r *http.Request) RouteScope {
	scope, _ := r.Context().Value(routeScopeKey{}).(*RouteScope)
	if scope == nil {
		return RouteScope{}
	}
	return *scope
}

func entryScopeMismatch(e *Entry, r *http.Request, scope *RouteScope) string {
	var expected RouteScope
	if scope != nil {
		expected = *scope
	} else {
		expected = scopeForRequest(r)
	}
	if e == nil || e.routeOwner != expected.Owner {
		return "route_ownership_mismatch"
	}
	if e.routeCompatibility != expected.Compatibility {
		return "route_policy_mismatch"
	}
	return ""
}

func withRouteScope(r *http.Request, scope *RouteScope) *http.Request {
	if scope == nil {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), routeScopeKey{}, scope))
}

func carryRouteScope(ctx context.Context, r *http.Request) context.Context {
	scope, _ := r.Context().Value(routeScopeKey{}).(*RouteScope)
	if scope == nil {
		return ctx
	}
	return context.WithValue(ctx, routeScopeKey{}, scope)
}

// WithResponseHookBypass marks a request whose outer response hook can mutate
// final response headers after the cache's admission decision.
func WithResponseHookBypass(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), responseHookBypassKey{}, true))
}

func responseHookBypass(r *http.Request) bool {
	bypass, _ := r.Context().Value(responseHookBypassKey{}).(bool)
	return bypass
}
