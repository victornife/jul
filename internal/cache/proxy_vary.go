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
	names    map[string]struct{}
	prefixes []string
	reject   bool
	cgi      bool
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

func newCGIVaryPolicy(names, prefixes []string) *proxyVaryPolicy {
	p := newProxyVaryPolicy(names)
	p.cgi = true
	normalized := make(map[string]struct{}, len(p.names))
	for name := range p.names {
		normalized[strings.ReplaceAll(name, "-", "_")] = struct{}{}
	}
	p.names = normalized
	if len(names)+len(prefixes) > MaxProxyVaryNames {
		p.reject = true
	}
	for _, prefix := range prefixes {
		if !httpguts.ValidHeaderFieldName(prefix) {
			p.reject = true
			break
		}
		p.prefixes = append(p.prefixes, strings.ReplaceAll(strings.ToLower(prefix), "-", "_"))
	}
	return p
}

func varyRewriteReason(policy *proxyVaryPolicy) string {
	if policy != nil && policy.cgi {
		return "vary_cgi_rewrite_rejected"
	}
	return "vary_proxy_rewrite_rejected"
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
		if policy.cgi && b == '-' {
			b = '_'
		}
		lower[i] = b
	}
	normalized := string(lower[:len(name)])
	if _, rewritten := policy.names[normalized]; rewritten {
		return true
	}
	if policy.cgi {
		for _, prefix := range policy.prefixes {
			if strings.HasPrefix(normalized, prefix) {
				return true
			}
		}
		if len(r.Header) > MaxProxyVaryNames {
			return true
		}
		for header := range r.Header {
			if len(header) == len(name) && !strings.EqualFold(header, name) &&
				strings.EqualFold(strings.ReplaceAll(header, "-", "_"), normalized) {
				return true
			}
		}
		return false
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
		return varyRewriteReason(policy)
	}
	for _, field := range fields {
		if proxyVaryField(r, policy, field) {
			return varyRewriteReason(policy)
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
			return varyRewriteReason(policy)
		}
		for {
			field, rest, more := strings.Cut(line, ",")
			count++
			if count > maxProxyVaryFields || proxyVaryField(r, policy, field) {
				return varyRewriteReason(policy)
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
		return varyRewriteReason(proxyVaryPolicyFor(r, scope))
	}
	for name := range e.VaryValues {
		if reason := varyRejectionFields(r, scope, []string{name}); reason != "" {
			return reason
		}
	}
	return ""
}
