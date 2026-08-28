// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"net/http"
	"strconv"
	"strings"

	"jul/internal/config"
)

// This file implements CORS (ADR 0018 §9): the compiled policy, origin
// matching, preflight detection/approval, and the header sets for both an
// approved preflight and an ordinary (or denied-preflight) response. Nothing
// here parses configuration or performs DNS/network access; CompileCORS runs
// once per location, during Prepare, and everything after it is a handful of
// map lookups and string joins over immutable slices.

// CORSPolicy is one location's compiled CORS policy. It exists only when
// cors.enabled = true; a disabled or absent policy compiles to nil, which is
// the fast path every non-CORS location takes.
type CORSPolicy struct {
	wildcard         bool
	origins          map[string]struct{} // lowercased exact origins; empty when wildcard
	allowCredentials bool

	allowedMethods   []string            // declaration order (or the default), for the header value
	allowedMethodSet map[string]struct{} // uppercased, for case-insensitive approval

	allowedHeaders   []string            // declaration order, for the header value
	allowedHeaderSet map[string]struct{} // canonicalized, for case-insensitive approval

	exposedHeaders []string
	maxAgeSeconds  *int
}

// defaultCORSMethods is the CORS-safelisted set: the methods a browser sends
// without preflighting at all. An operator who configures only allowed_origins
// gets what they expected instead of a policy that denies every preflight.
var defaultCORSMethods = []string{"GET", "HEAD", "POST"}

// maxPreflightRequestHeaders bounds Access-Control-Request-Headers tokens on
// an incoming preflight (ADR 0018 §9/§16); longer is not approved.
const maxPreflightRequestHeaders = 64

// CompileCORS builds the immutable runtime policy from a location's
// [servers.locations.cors] block, or returns nil when there is none or it is
// disabled. Validation has already rejected everything malformed; this is the
// one place origins and tokens are lowercased/canonicalized for comparison.
func CompileCORS(c *config.CORSConfig) *CORSPolicy {
	if c == nil || !c.Enabled {
		return nil
	}
	p := &CORSPolicy{allowCredentials: c.AllowCredentials}

	for _, o := range c.AllowedOrigins {
		if o == "*" {
			p.wildcard = true
			break
		}
	}
	if !p.wildcard {
		p.origins = make(map[string]struct{}, len(c.AllowedOrigins))
		for _, o := range c.AllowedOrigins {
			p.origins[strings.ToLower(o)] = struct{}{}
		}
	}

	methods := c.AllowedMethods
	if methods == nil {
		methods = defaultCORSMethods
	}
	p.allowedMethods = append([]string(nil), methods...)
	p.allowedMethodSet = make(map[string]struct{}, len(methods))
	for _, m := range methods {
		p.allowedMethodSet[strings.ToUpper(m)] = struct{}{}
	}

	p.allowedHeaders = append([]string(nil), c.AllowedHeaders...)
	p.allowedHeaderSet = make(map[string]struct{}, len(c.AllowedHeaders))
	for _, h := range c.AllowedHeaders {
		p.allowedHeaderSet[http.CanonicalHeaderKey(h)] = struct{}{}
	}

	p.exposedHeaders = append([]string(nil), c.ExposedHeaders...)

	if c.MaxAge != nil {
		secs := int(c.MaxAge.Std().Seconds())
		p.maxAgeSeconds = &secs
	}
	return p
}

// IsPreflight reports whether r is a CORS preflight (ADR 0018 §9): OPTIONS
// carrying exactly one Origin field line and exactly one well-formed
// Access-Control-Request-Method field line (one token, not a list). Repeated
// lines or a comma-separated list are not a well-formed preflight — accepting
// one would let a client widen its own preflight — so they fall through
// unapproved rather than being treated as one.
func IsPreflight(r *http.Request) bool {
	if r.Method != http.MethodOptions {
		return false
	}
	if len(r.Header.Values("Origin")) != 1 {
		return false
	}
	acrm := r.Header.Values("Access-Control-Request-Method")
	return len(acrm) == 1 && isCORSToken(acrm[0])
}

// EvaluatePreflight is the pure decision (ADR 0018 §10): no side effects, three
// header fields read. It returns the origin to grant and true when the
// preflight is approved; the caller emits the 204. A false result means the
// preflight is not short-circuited — the ordinary chain handles it and no
// Access-Control-* header is added on its behalf.
func (p *CORSPolicy) EvaluatePreflight(r *http.Request) (grantOrigin string, approved bool) {
	origin := r.Header.Get("Origin")
	if !p.wildcard {
		if _, ok := p.origins[strings.ToLower(origin)]; !ok {
			return "", false
		}
	}
	if _, ok := p.allowedMethodSet[strings.ToUpper(r.Header.Get("Access-Control-Request-Method"))]; !ok {
		return "", false
	}
	if !p.requestedHeadersApproved(r.Header.Values("Access-Control-Request-Headers")) {
		return "", false
	}
	if p.wildcard {
		return "*", true
	}
	return origin, true
}

// requestedHeadersApproved reports whether every token across every
// Access-Control-Request-Headers field line is in allowed_headers. Fetch sends
// this as one comma-separated field line; tokens are never reflected back —
// reflecting is how a bounded policy becomes an unbounded one — and the total
// token count is bounded (§16).
func (p *CORSPolicy) requestedHeadersApproved(values []string) bool {
	total := 0
	for _, v := range values {
		for _, tok := range strings.Split(v, ",") {
			tok = strings.TrimSpace(tok)
			if tok == "" {
				continue
			}
			total++
			if total > maxPreflightRequestHeaders {
				return false
			}
			if _, ok := p.allowedHeaderSet[http.CanonicalHeaderKey(tok)]; !ok {
				return false
			}
		}
	}
	return true
}

// WritePreflightResponse writes the approved-preflight 204 with the full CORS
// header set (ADR 0018 §9/§10): allow-origin, allow-methods, allow-headers when
// configured, max-age when set, credentials when enabled, and Vary with Origin
// omitted from it under the unconditional wildcard. No other header — this is
// the Jul-generated preflight response §8b says generic response_headers
// operations do not apply to.
func (p *CORSPolicy) WritePreflightResponse(w http.ResponseWriter, grantOrigin string) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", grantOrigin)
	if len(p.allowedMethods) > 0 {
		h.Set("Access-Control-Allow-Methods", strings.Join(p.allowedMethods, ", "))
	}
	if len(p.allowedHeaders) > 0 {
		h.Set("Access-Control-Allow-Headers", strings.Join(p.allowedHeaders, ", "))
	}
	if p.maxAgeSeconds != nil {
		h.Set("Access-Control-Max-Age", strconv.Itoa(*p.maxAgeSeconds))
	}
	if p.allowCredentials {
		h.Set("Access-Control-Allow-Credentials", "true")
	}
	vary := make([]string, 0, 3)
	if !p.wildcard {
		vary = append(vary, "Origin")
	}
	vary = append(vary, "Access-Control-Request-Method", "Access-Control-Request-Headers")
	h.Set("Vary", strings.Join(vary, ", "))
	w.WriteHeader(http.StatusNoContent)
}

// ApplyToResponse decorates an ordinary (non-preflight, or denied-preflight)
// response with this location's CORS headers (ADR 0018 §8b, §9, §11): every
// existing Access-Control-* field the upstream may have set is removed first,
// so a CORS-implementing upstream cannot produce a duplicate grant, and Jul's
// own set is added according to the two-row Vary table. It never strips an
// upstream Vary, including Vary: Origin: if the upstream's body genuinely
// varies by origin, that is a fact about the stored representation Jul must
// keep, not an optimization opportunity.
func (p *CORSPolicy) ApplyToResponse(h http.Header, r *http.Request) {
	for name := range h {
		if strings.HasPrefix(name, "Access-Control-") {
			delete(h, name)
		}
	}
	if p.wildcard {
		// Unconditional: every response, including no Origin and Origin: null.
		// Vary: Origin is correctly omitted only because the output is constant
		// regardless of origin.
		h.Set("Access-Control-Allow-Origin", "*")
		if len(p.exposedHeaders) > 0 {
			h.Set("Access-Control-Expose-Headers", strings.Join(p.exposedHeaders, ", "))
		}
		return
	}
	// Always appended — including no Origin and a disallowed one — which is
	// what stops a shared downstream cache storing the no-origin variant and
	// replaying it cross-origin.
	addVary(h, "Origin")
	origins := r.Header.Values("Origin")
	if len(origins) != 1 {
		return
	}
	if _, ok := p.origins[strings.ToLower(origins[0])]; !ok {
		return
	}
	h.Set("Access-Control-Allow-Origin", origins[0])
	if p.allowCredentials {
		h.Set("Access-Control-Allow-Credentials", "true")
	}
	if len(p.exposedHeaders) > 0 {
		h.Set("Access-Control-Expose-Headers", strings.Join(p.exposedHeaders, ", "))
	}
}

// isCORSToken reports whether s is exactly one RFC 9110 §5.6.2 token, the
// grammar Access-Control-Request-Method must satisfy to count as well-formed.
func isCORSToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}
