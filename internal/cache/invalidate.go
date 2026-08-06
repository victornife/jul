// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"net/url"
	"strings"
)

// isUnsafeMethod reports whether a method can change the origin's state, and so
// can make stored representations obsolete (RFC 9110 §9.2.1).
//
// CONNECT is excluded deliberately: it establishes a tunnel and has no target
// representation, so there is nothing for it to invalidate. Any extension method
// that is not one of the known-safe ones counts as unsafe, which is the
// conservative direction — the cost of an unnecessary invalidation is a miss.
func isUnsafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace, http.MethodConnect:
		return false
	default:
		return true
	}
}

// invalidatingStatus reports whether a response to an unsafe method is evidence
// that the origin acted on it.
//
// RFC 9111 §4.4 requires invalidation on a non-error status, that is 2xx or 3xx.
// A 4xx or 5xx means the origin refused or failed, and a status of zero means the
// exchange produced no response at all — it was canceled, timed out, or the
// connection was hijacked. None of those prove a change, so none invalidate.
func invalidatingStatus(status int) bool {
	return status >= 200 && status < 400
}

// invalidatedHeaders are the response headers RFC 9111 §4.4 makes invalidation
// targets alongside the effective request URI.
var invalidatedHeaders = []string{"Location", "Content-Location"}

// invalidateFor removes the representations a successful unsafe request made
// obsolete: the request target, plus any same-origin Location and
// Content-Location the response named.
func (c *Cache) invalidateFor(r *http.Request, respHeader http.Header) {
	c.invalidateURI(r.Host, r.URL.RequestURI())
	for _, name := range invalidatedHeaders {
		if host, requestURI, ok := sameOriginTarget(r, respHeader.Get(name)); ok {
			c.invalidateURI(host, requestURI)
		}
	}
}

// invalidateURI removes every stored representation of one target: the GET and
// the HEAD key, and — through Delete — every Vary variant those keys own.
func (c *Cache) invalidateURI(host, requestURI string) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		c.Delete(keyFor(method, host, requestURI))
	}
}

// sameOriginTarget resolves a Location or Content-Location value against the
// request target and returns it only when it names the same origin.
//
// The cross-origin rule is a shared-cache integrity rule (RFC 9111 §4.4): one
// origin must not be able to evict another origin's entries by naming them in a
// response header. It is not the containment boundary — that is structural.
// Whatever survives this function is used only to build a cache key, and a disk
// file is named by the SHA-256 of that key, so a hostile header can never
// traverse a directory, name a file the cache did not write, or reach outside
// disk_path. A value that does not parse, is opaque, or names a different host
// is simply skipped.
func sameOriginTarget(r *http.Request, value string) (host, requestURI string, ok bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", "", false
	}
	ref, err := url.Parse(value)
	if err != nil {
		return "", "", false
	}
	target := r.URL.ResolveReference(ref)
	if target.Opaque != "" {
		// mailto:, urn: and friends address no HTTP resource.
		return "", "", false
	}
	if s := target.Scheme; s != "" && s != "http" && s != "https" {
		return "", "", false
	}
	host = target.Host
	if host == "" {
		host = r.Host
	}
	// Host comparison is exact apart from case. A target that spells the
	// authority differently (an explicit default port, say) is treated as
	// another origin and skipped, because the cache key is built from the
	// literal Host and would not match anyway.
	if !strings.EqualFold(host, r.Host) {
		return "", "", false
	}
	return host, target.RequestURI(), true
}
