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
	s.Path = sanitizePath(s.Path)
	s.Origin = normalizeOrigin(s.Origin)
	if s.Origin == "(none)" {
		s.Origin = ""
	}
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

// sanitizePath strips any query string (defense in depth — r.URL.Path already
// excludes it) and caps the stored length so the buffer cannot be bloated.
func sanitizePath(path string) string {
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	if path == "" {
		return "/"
	}
	if len(path) > samplePathMaxLen {
		return path[:samplePathMaxLen]
	}
	return path
}

// userAgentFamily reduces a raw User-Agent header to a coarse family so the
// stored value is low-cardinality and carries no fingerprinting detail. Order
// matters: more specific tokens (Edge, Chrome) are checked before the generic
// ones they embed (Safari).
func userAgentFamily(ua string) string {
	if ua == "" {
		return ""
	}
	l := strings.ToLower(ua)
	switch {
	case strings.Contains(l, "bot"), strings.Contains(l, "crawler"), strings.Contains(l, "spider"):
		return "bot"
	case strings.Contains(l, "curl"):
		return "curl"
	case strings.Contains(l, "wget"):
		return "wget"
	case strings.Contains(l, "edg/"), strings.Contains(l, "edge"):
		return "Edge"
	case strings.Contains(l, "opr/"), strings.Contains(l, "opera"):
		return "Opera"
	case strings.Contains(l, "firefox"):
		return "Firefox"
	case strings.Contains(l, "chrome"), strings.Contains(l, "chromium"):
		return "Chrome"
	case strings.Contains(l, "safari"):
		return "Safari"
	case strings.Contains(l, "go-http-client"):
		return "Go"
	case strings.Contains(l, "python"):
		return "Python"
	default:
		return "other"
	}
}
