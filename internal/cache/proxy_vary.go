// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"strings"

	"golang.org/x/net/http/httpguts"
)

const (
	// MaxProxyVaryNames bounds the per-generation transformed-field snapshot.
	MaxProxyVaryNames      = 256
	maxConnectionVaryBytes = 8192
	maxConnectionVaryNames = 128
	maxProxyVaryFieldBytes = 128
	maxProxyVaryFields     = 128
	maxProxyVaryBytes      = 8192
)

type proxyVaryPolicy struct {
	names  map[string]struct{}
	reject bool
}

func newProxyVaryPolicy(names []string) *proxyVaryPolicy {
	p := &proxyVaryPolicy{names: make(map[string]struct{}, min(len(names), MaxProxyVaryNames))}
	for _, name := range names {
		if !httpguts.ValidHeaderFieldName(name) {
			p.reject = true
			break
		}
		name = strings.ToLower(name)
		if _, exists := p.names[name]; !exists && len(p.names) >= MaxProxyVaryNames {
			p.reject = true
			break
		}
		p.names[name] = struct{}{}
	}
	return p
}

func proxyVaryPolicyFor(r *http.Request, scope *RouteScope) *proxyVaryPolicy {
	if scope != nil && scope.proxyVary != nil {
		return scope.proxyVary
	}
	scope, _ = r.Context().Value(routeScopeKey{}).(*RouteScope)
	if scope == nil {
		return nil
	}
	return scope.proxyVary
}

func proxyVaryField(r *http.Request, policy *proxyVaryPolicy, name string) bool {
	if policy == nil {
		return false
	}
	if policy.reject {
		return true
	}
	name = strings.TrimSpace(name)
	if len(name) > maxProxyVaryFieldBytes || !httpguts.ValidHeaderFieldName(name) {
		return true
	}
	var lower [maxProxyVaryFieldBytes]byte
	for i := range len(name) {
		b := name[i]
		if b >= 'A' && b <= 'Z' {
			b += 'a' - 'A'
		}
		lower[i] = b
	}
	if _, rewritten := policy.names[string(lower[:len(name)])]; rewritten {
		return true
	}

	lines := r.Header.Values("Connection")
	if len(lines) > maxConnectionVaryNames {
		return true
	}
	var bytes, count int
	for _, line := range lines {
		bytes += len(line)
		if bytes > maxConnectionVaryBytes {
			return true
		}
		for {
			field, rest, more := strings.Cut(line, ",")
			field = strings.TrimSpace(field)
			count++
			if count > maxConnectionVaryNames || !httpguts.ValidHeaderFieldName(field) || strings.EqualFold(field, name) {
				return true
			}
			if !more {
				break
			}
			line = rest
		}
	}
	return false
}

func varyRejectionFields(r *http.Request, scope *RouteScope, fields []string) string {
	if sensitiveVary(fields) {
		return "vary_sensitive_rejected"
	}
	policy := proxyVaryPolicyFor(r, scope)
	if policy != nil && len(fields) > maxProxyVaryFields {
		return "vary_proxy_rewrite_rejected"
	}
	for _, field := range fields {
		if proxyVaryField(r, policy, field) {
			return "vary_proxy_rewrite_rejected"
		}
	}
	return ""
}

func varyRejectionHeader(r *http.Request, scope *RouteScope, h http.Header) string {
	if sensitiveVaryHeader(h) {
		return "vary_sensitive_rejected"
	}
	policy := proxyVaryPolicyFor(r, scope)
	if policy == nil {
		return ""
	}
	var bytes, count int
	for _, line := range h.Values("Vary") {
		bytes += len(line)
		if bytes > maxProxyVaryBytes {
			return "vary_proxy_rewrite_rejected"
		}
		for {
			field, rest, more := strings.Cut(line, ",")
			count++
			if count > maxProxyVaryFields || proxyVaryField(r, policy, field) {
				return "vary_proxy_rewrite_rejected"
			}
			if !more {
				break
			}
			line = rest
		}
	}
	return ""
}

func varyRejectionEntry(r *http.Request, scope *RouteScope, e *Entry) string {
	if e == nil {
		return ""
	}
	if sensitiveVaryEntry(e) {
		return "vary_sensitive_rejected"
	}
	if reason := varyRejectionFields(r, scope, e.Vary); reason != "" {
		return reason
	}
	if reason := varyRejectionHeader(r, scope, e.Header); reason != "" {
		return reason
	}
	if proxyVaryPolicyFor(r, scope) != nil && len(e.VaryValues) > maxProxyVaryFields {
		return "vary_proxy_rewrite_rejected"
	}
	for name := range e.VaryValues {
		if reason := varyRejectionFields(r, scope, []string{name}); reason != "" {
			return reason
		}
	}
	return ""
}
