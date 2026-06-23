package admin

import (
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// adminLimiter enforces the Console v2 Milestone 1.6 admin-API protections:
// per-client request rate limits (separately for reads, writes, and the
// high-impact config apply/validate/diff endpoints) plus a concurrent-connection
// cap on the /api/events SSE stream. Clients are keyed by transport peer IP so a
// single shared admin token cannot be used to exhaust the limits from many
// hosts. A nil *adminLimiter is a no-op, so the middleware degrades gracefully
// when limits are disabled.
type adminLimiter struct {
	log *slog.Logger

	readPerMin  int
	writePerMin int
	applyPerMin int
	maxConns    int

	mu       sync.Mutex
	buckets  map[string]*adminClient
	lastSeen map[string]time.Time
}

// adminClient holds one client's token buckets and live SSE connection count.
type adminClient struct {
	read  *rate.Limiter
	write *rate.Limiter
	apply *rate.Limiter
	conns int
}

// newAdminLimiter builds a limiter from the resolved per-minute limits and SSE
// connection cap. It returns nil when every protection is disabled so callers
// can skip the middleware entirely.
func newAdminLimiter(log *slog.Logger, readPerMin, writePerMin, applyPerMin, maxConns int) *adminLimiter {
	if readPerMin <= 0 && writePerMin <= 0 && applyPerMin <= 0 && maxConns <= 0 {
		return nil
	}
	if log == nil {
		log = slog.Default()
	}
	return &adminLimiter{
		log:         log,
		readPerMin:  readPerMin,
		writePerMin: writePerMin,
		applyPerMin: applyPerMin,
		maxConns:    maxConns,
		buckets:     make(map[string]*adminClient),
		lastSeen:    make(map[string]time.Time),
	}
}

// kind classifies an admin request for limit selection.
type limitKind int

const (
	limitRead limitKind = iota
	limitWrite
	limitApply
)

// perMinute converts a requests-per-minute budget into a rate.Limiter. A
// non-positive budget yields an Inf limiter (never limited). Burst equals the
// per-minute budget so short bursts of legitimate polling are tolerated while
// the sustained rate is still bounded.
func perMinute(n int) *rate.Limiter {
	if n <= 0 {
		return rate.NewLimiter(rate.Inf, 0)
	}
	return rate.NewLimiter(rate.Limit(float64(n)/60.0), n)
}

// client returns the per-IP bucket set, creating it on first use.
func (l *adminLimiter) client(ip string) *adminClient {
	c := l.buckets[ip]
	if c == nil {
		c = &adminClient{
			read:  perMinute(l.readPerMin),
			write: perMinute(l.writePerMin),
			apply: perMinute(l.applyPerMin),
		}
		l.buckets[ip] = c
	}
	l.lastSeen[ip] = time.Now()
	return c
}

// allow reports whether a request of the given kind from ip may proceed and,
// when denied, the suggested Retry-After in whole seconds.
func (l *adminLimiter) allow(ip string, kind limitKind) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gcLocked()

	c := l.client(ip)
	var lim *rate.Limiter
	switch kind {
	case limitWrite:
		lim = c.write
	case limitApply:
		lim = c.apply
	default:
		lim = c.read
	}

	r := lim.Reserve()
	if !r.OK() {
		return false, 1
	}
	if d := r.Delay(); d > 0 {
		r.Cancel()
		secs := int((d + time.Second - 1) / time.Second)
		if secs < 1 {
			secs = 1
		}
		return false, secs
	}
	return true, 0
}

// acquireConn tries to register a new SSE connection for ip, returning a release
// function when admitted. It returns ok=false once the per-client cap is hit.
func (l *adminLimiter) acquireConn(ip string) (release func(), ok bool) {
	if l == nil || l.maxConns <= 0 {
		return func() {}, true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gcLocked()

	c := l.client(ip)
	if c.conns >= l.maxConns {
		return nil, false
	}
	c.conns++
	released := false
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if released {
			return
		}
		released = true
		if cc := l.buckets[ip]; cc != nil && cc.conns > 0 {
			cc.conns--
		}
	}, true
}

// gcLocked evicts idle client entries with no live connections so the maps stay
// bounded under churny IP spaces. The caller must hold l.mu.
func (l *adminLimiter) gcLocked() {
	const idle = 15 * time.Minute
	cutoff := time.Now().Add(-idle)
	for ip, seen := range l.lastSeen {
		if seen.Before(cutoff) {
			if c := l.buckets[ip]; c == nil || c.conns == 0 {
				delete(l.buckets, ip)
				delete(l.lastSeen, ip)
			}
		}
	}
}

// classify maps an HTTP request to its limit kind. The high-impact config
// validate/diff/apply endpoints are limited separately and more strictly than
// ordinary writes; all other mutations are writes; everything else is a read.
func classify(r *http.Request) limitKind {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return limitRead
	}
	switch r.URL.Path {
	case "/api/config/apply", "/api/config/validate", "/api/config/diff":
		return limitApply
	}
	return limitWrite
}

// rateLimit wraps next with the admin request-rate protections. A nil receiver
// (limits disabled) returns next unchanged. SSE connection caps are enforced
// separately by the events handler via acquireConn.
func (l *adminLimiter) rateLimit(next http.Handler) http.Handler {
	if l == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := adminClientIP(r)
		kind := classify(r)
		ok, retryAfter := l.allow(ip, kind)
		if !ok {
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			l.log.Warn("admin rate limit exceeded",
				"client", ip, "method", r.Method, "path", r.URL.Path, "retry_after_s", retryAfter)
			http.Error(w, "429 Too Many Requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// adminClientIP extracts the transport peer IP from a request, falling back to
// the raw RemoteAddr when it carries no port. Untrusted forwarding headers are
// deliberately ignored so the limit key cannot be spoofed.
func adminClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
