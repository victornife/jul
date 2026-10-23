// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"strings"
	"sync"
	"time"
)

// requestSampleCap bounds the number of recent requests retained in the ring
// buffer. The buffer is fixed-size so memory stays bounded regardless of
// traffic volume (Console v2 Milestone 5.1).
const requestSampleCap = 256

// samplePathMaxLen caps the stored request path length so a hostile client
// cannot inflate the buffer with very long URLs.
const samplePathMaxLen = 256

// RequestSample is one recent request captured for the Console v2 Request
// Samples panel (Milestone 5.1). It is intentionally privacy-preserving: it
// never carries cookies, Authorization headers, raw tokens, query strings, or
// request/response bodies — only the coarse shape of the request.
type RequestSample struct {
	Time        time.Time `json:"time"`
	Method      string    `json:"method"`
	Path        string    `json:"path"`
	Host        string    `json:"host"`
	Status      int       `json:"status"`
	DurationMs  float64   `json:"duration_ms"`
	CacheState  string    `json:"cache_state,omitempty"`
	Compressed  bool      `json:"compressed"`
	RateLimited bool      `json:"rate_limited"`
	Origin      string    `json:"origin,omitempty"`
	UserAgent   string    `json:"user_agent,omitempty"` // coarse family only
}

// requestSampleBuffer is a fixed-size circular buffer of the most recent
// requests. It overwrites the oldest entry once full, so the working set never
// grows. It is safe for concurrent use.
type requestSampleBuffer struct {
	mu   sync.Mutex
	buf  []RequestSample
	next int
	full bool
}

func newRequestSampleBuffer(capacity int) *requestSampleBuffer {
	if capacity <= 0 {
		capacity = requestSampleCap
	}
	return &requestSampleBuffer{buf: make([]RequestSample, capacity)}
}

// record stores one sample, normalizing and redacting sensitive fields before
// it ever enters the buffer.
func (b *requestSampleBuffer) record(s RequestSample) {
	s.Path = SanitizePath(s.Path)
	s.Origin = normalizeOrigin(s.Origin)
	if s.Origin == "(none)" {
		s.Origin = ""
	}
	b.recordSanitized(s)
}

// recordSanitized stores a sample whose Path and Origin have already been
// normalized, avoiding duplicate SanitizePath and normalizeOrigin calls when
// the caller also records traffic sources and route failures.
func (b *requestSampleBuffer) recordSanitized(s RequestSample) {
	s.UserAgent = userAgentFamily(s.UserAgent)

	b.mu.Lock()
	b.buf[b.next] = s
	b.next = (b.next + 1) % len(b.buf)
	if b.next == 0 {
		b.full = true
	}
	b.mu.Unlock()
}

// snapshot returns the retained samples newest-first.
func (b *requestSampleBuffer) snapshot() []RequestSample {
	b.mu.Lock()
	defer b.mu.Unlock()

	n := b.next
	if b.full {
		n = len(b.buf)
	}
	out := make([]RequestSample, 0, n)
	// Walk backwards from the most recently written slot so the newest sample
	// is first.
	for i := 0; i < n; i++ {
		idx := (b.next - 1 - i + len(b.buf)) % len(b.buf)
		out = append(out, b.buf[idx])
	}
	return out
}

// Path-segment redaction rules (P2-11). Poorly designed APIs embed IDs,
// emails, tokens, and tenant names directly in the path, so even with the query
// string removed a raw path can carry PII or secrets. We normalize high-entropy
// or obviously-sensitive segments to coarse placeholders, which also collapses
// per-entity paths into a single template (e.g. /users/:id) — better for the
// failing-routes rollup and far safer to display in an admin console.

// redactSegment maps a single path segment to a placeholder when it looks like
// an identifier, email, or token; otherwise it returns the segment unchanged.
func redactSegment(seg string) string {
	switch {
	case seg == "":
		return seg
	case isEmailSegment(seg):
		return ":email"
	case isUUIDSegment(seg):
		return ":id"
	case isNumSegment(seg):
		return ":id"
	case isHexSegment(seg):
		return ":id"
	case isMixedTokenSegment(seg):
		return ":token"
	default:
		return seg
	}
}

func isHexByte(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// isEmailSegment matches `^[^@\s/]+@[^@\s/]+\.[^@\s/]+$`.
func isEmailSegment(seg string) bool {
	at := strings.IndexByte(seg, '@')
	if at <= 0 || at >= len(seg)-3 {
		return false
	}
	hasInteriorDot := false
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		switch c {
		case ' ', '\t', '\n', '\f', '\r', '/':
			return false
		case '@':
			if i != at {
				return false
			}
		case '.':
			if i > at+1 && i < len(seg)-1 {
				hasInteriorDot = true
			}
		}
	}
	return hasInteriorDot
}

// isUUIDSegment matches `^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`.
func isUUIDSegment(seg string) bool {
	if len(seg) != 36 || seg[8] != '-' || seg[13] != '-' || seg[18] != '-' || seg[23] != '-' {
		return false
	}
	for i := 0; i < 36; i++ {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !isHexByte(seg[i]) {
			return false
		}
	}
	return true
}

// isNumSegment matches `^\d{3,}$`.
func isNumSegment(seg string) bool {
	if len(seg) < 3 {
		return false
	}
	for i := 0; i < len(seg); i++ {
		if seg[i] < '0' || seg[i] > '9' {
			return false
		}
	}
	return true
}

// isHexSegment matches `^[0-9a-fA-F]{16,}$`.
func isHexSegment(seg string) bool {
	if len(seg) < 16 {
		return false
	}
	for i := 0; i < len(seg); i++ {
		if !isHexByte(seg[i]) {
			return false
		}
	}
	return true
}

// isMixedTokenSegment matches `^[A-Za-z0-9._-]{20,}$` with at least one ASCII
// digit and at least one ASCII letter.
func isMixedTokenSegment(seg string) bool {
	if len(seg) < 20 {
		return false
	}
	hasDigit := false
	hasAlpha := false
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		switch {
		case c >= '0' && c <= '9':
			hasDigit = true
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
			hasAlpha = true
		case c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return hasDigit && hasAlpha
}

// SanitizePath strips any query string (defense in depth — r.URL.Path already
// excludes it), redacts identifier/email/token-like segments to placeholders so
// operator-visible diagnostics carry no obvious PII or secrets, and caps the
// stored length so hostile request targets cannot inflate logs or buffers. It
// deliberately does not URL-decode attacker-controlled input.
func SanitizePath(path string) string {
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	if path == "" {
		return "/"
	}
	if !strings.Contains(path, "/") {
		path = redactSegment(path)
		if len(path) > samplePathMaxLen {
			return path[:samplePathMaxLen]
		}
		return path
	}
	needsRedact := false
	for rest := path; ; {
		seg, tail, ok := strings.Cut(rest, "/")
		if redactSegment(seg) != seg {
			needsRedact = true
			break
		}
		if !ok {
			break
		}
		rest = tail
	}
	if !needsRedact {
		if len(path) > samplePathMaxLen {
			return path[:samplePathMaxLen]
		}
		return path
	}
	var b strings.Builder
	b.Grow(min(len(path), samplePathMaxLen))
	for rest := path; ; {
		seg, tail, ok := strings.Cut(rest, "/")
		b.WriteString(redactSegment(seg))
		if !ok {
			break
		}
		b.WriteByte('/')
		rest = tail
	}
	out := b.String()
	if len(out) > samplePathMaxLen {
		return out[:samplePathMaxLen]
	}
	return out
}

// userAgentFamily reduces a raw User-Agent header to a coarse family so the
// stored value is low-cardinality and carries no fingerprinting detail. Order
// matters: more specific tokens (Edge, Chrome) are checked before the generic
// ones they embed (Safari).
func userAgentFamily(ua string) string {
	if ua == "" {
		return ""
	}
	switch {
	case containsFoldASCII(ua, "bot"), containsFoldASCII(ua, "crawler"), containsFoldASCII(ua, "spider"):
		return "bot"
	case containsFoldASCII(ua, "curl"):
		return "curl"
	case containsFoldASCII(ua, "wget"):
		return "wget"
	case containsFoldASCII(ua, "edg/"), containsFoldASCII(ua, "edge"):
		return "Edge"
	case containsFoldASCII(ua, "opr/"), containsFoldASCII(ua, "opera"):
		return "Opera"
	case containsFoldASCII(ua, "firefox"):
		return "Firefox"
	case containsFoldASCII(ua, "chrome"), containsFoldASCII(ua, "chromium"):
		return "Chrome"
	case containsFoldASCII(ua, "safari"):
		return "Safari"
	case containsFoldASCII(ua, "go-http-client"):
		return "Go"
	case containsFoldASCII(ua, "python"):
		return "Python"
	default:
		return "other"
	}
}

// containsFoldASCII reports whether s contains subLower (an all-lowercase ASCII
// pattern) under ASCII case-insensitivity without allocating a lowercased copy.
func containsFoldASCII(s, subLower string) bool {
	n := len(subLower)
	if len(s) < n {
		return false
	}
	first := subLower[0]
	limit := len(s) - n
	for i := 0; i <= limit; i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != first {
			continue
		}
		match := true
		for j := 1; j < n; j++ {
			sc := s[i+j]
			if sc >= 'A' && sc <= 'Z' {
				sc += 'a' - 'A'
			}
			if sc != subLower[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
