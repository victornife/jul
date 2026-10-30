// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"strings"

	"golang.org/x/net/http/httpguts"
)

const (
	// MaxProxyVaryNames bounds the per-route transformed-header snapshot (#672).
	MaxProxyVaryNames      = 256
	maxConnectionVaryBytes = 8192
	maxConnectionVaryNames = 128
	maxProxyVaryFieldBytes = 128
	maxProxyVaryFields     = 128
	maxProxyVaryBytes      = 8192
)

type varyTransformPolicy struct {
	names    map[string]struct{}
	prefixes []string
	reject   bool
	cgi      bool
}

func newProxyVaryTransformPolicy(names []string, complete bool) *varyTransformPolicy {
	p := &varyTransformPolicy{
		names:  make(map[string]struct{}, min(len(names), MaxProxyVaryNames)),
		reject: !complete,
	}
	for _, name := range names {
		if !httpguts.ValidHeaderFieldName(name) {
			p.reject = true
			break
		}
		lower := strings.ToLower(name)
		if _, exists := p.names[lower]; !exists && len(p.names) >= MaxProxyVaryNames {
			p.reject = true
			break
		}
		p.names[lower] = struct{}{}
	}
	return p
}

func newCGIVaryTransformPolicy(names, prefixes []string, complete bool) *varyTransformPolicy {
	p := newProxyVaryTransformPolicy(names, complete)
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

func varyRewriteReason(policy *varyTransformPolicy) string {
	if policy != nil && policy.cgi {
		return "vary_cgi_rewrite_rejected"
	}
	return "vary_proxy_rewrite_rejected"
}

func isSensitiveVaryField(field string) bool {
	trimmed := strings.Trim(strings.TrimSpace(field), "\"'")
	if base, _, ok := strings.Cut(trimmed, ";"); ok {
		trimmed = strings.Trim(strings.TrimSpace(base), "\"'")
	}
	trimmed = strings.TrimSpace(trimmed)
	return strings.EqualFold(trimmed, "cookie") || strings.EqualFold(trimmed, "authorization")
}

func sensitiveVaryFields(fields []string) bool {
	for _, field := range fields {
		for tok := range strings.SplitSeq(field, ",") {
			if isSensitiveVaryField(tok) {
				return true
			}
		}
	}
	return false
}

func sensitiveVaryHeader(h http.Header) bool {
	for _, line := range h.Values("Vary") {
		for {
			field, rest, more := strings.Cut(line, ",")
			if isSensitiveVaryField(field) {
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

func sensitiveVaryEntry(e *Entry) bool {
	if e == nil {
		return false
	}
	if sensitiveVaryFields(e.Vary) || sensitiveVaryHeader(e.Header) {
		return true
	}
	for name := range e.VaryValues {
		if isSensitiveVaryField(name) {
			return true
		}
	}
	for _, fields := range e.RouteVary {
		if sensitiveVaryFields(fields) {
			return true
		}
	}
	return false
}

func proxyVaryField(r *http.Request, policy *varyTransformPolicy, name string) bool {
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
		if strings.IndexByte(name, '_') >= 0 {
			return true
		}
		if r == nil {
			return false
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
	if r == nil {
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

func varyRejectionFields(r *http.Request, policy *varyTransformPolicy, fields []string) string {
	if sensitiveVaryFields(fields) {
		return "vary_sensitive_rejected"
	}
	if policy == nil {
		return ""
	}
	if len(fields) > maxProxyVaryFields {
		return varyRewriteReason(policy)
	}
	for _, field := range fields {
		for tok := range strings.SplitSeq(field, ",") {
			tok = strings.TrimSpace(tok)
			if tok == "" {
				continue
			}
			if tok == "*" {
				continue
			}
			if proxyVaryField(r, policy, tok) {
				return varyRewriteReason(policy)
			}
		}
	}
	return ""
}

func varyRejectionHeader(r *http.Request, policy *varyTransformPolicy, h http.Header) string {
	if sensitiveVaryHeader(h) {
		return "vary_sensitive_rejected"
	}
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
			trimmed := strings.TrimSpace(field)
			count++
			if count > maxProxyVaryFields || (trimmed != "*" && proxyVaryField(r, policy, trimmed)) {
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

func varyRejectionEntry(r *http.Request, policy *varyTransformPolicy, e *Entry) string {
	if e == nil {
		return ""
	}
	if sensitiveVaryEntry(e) {
		return "vary_sensitive_rejected"
	}
	if reason := varyRejectionFields(r, policy, e.Vary); reason != "" {
		return reason
	}
	if reason := varyRejectionHeader(r, policy, e.Header); reason != "" {
		return reason
	}
	if policy != nil && len(e.VaryValues) > maxProxyVaryFields {
		return varyRewriteReason(policy)
	}
	for name := range e.VaryValues {
		if reason := varyRejectionFields(r, policy, []string{name}); reason != "" {
			return reason
		}
	}
	return ""
}

func (c *Cache) cleanupSensitiveVary(base string, e *Entry, maxGen uint64) {
	if c == nil {
		return
	}
	if e != nil {
		for _, vk := range e.Variants {
			c.delOneIfGenAtMost(vk, maxGen)
		}
	}
	c.deleteAtMostGen(base, maxGen)
}
