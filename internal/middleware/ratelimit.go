// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limiter decides whether an event identified by key may proceed now. It is the
// stable seam between rate-limit policy and its backend: today the backend is an
// in-process token bucket, but a distributed or cost-based limiter can be
// swapped in without touching call sites. retryAfter is the suggested wait
// before retrying and is only meaningful when ok is false.
type Limiter interface {
	Allow(key string) (ok bool, retryAfter time.Duration)
}

// rateLimiterShards is the fixed number of lock stripes in a RateLimiterStore.
// Sharding spreads bucket lookups across independent mutexes so per-request
// contention stays low even under high concurrency.
const rateLimiterShards = 32

// RateLimiterStore holds one token bucket per key, sharded to reduce lock
// contention. Buckets are created lazily on first use and evicted once idle,
// keeping memory bounded under churny key spaces such as per-IP limiting.
type RateLimiterStore struct {
	shards [rateLimiterShards]rateLimiterShard
	ttl    time.Duration
}

type rateLimiterShard struct {
	mu      sync.Mutex
	entries map[string]*rateLimiterEntry
}

type rateLimiterEntry struct {
	lim      *rate.Limiter
	limit    rate.Limit
	burst    int
	lastSeen time.Time
}

// NewRateLimiterStore creates a store and starts a janitor, bound to ctx, that
// evicts buckets idle longer than ttl. The janitor stops when ctx is canceled,
// so the store must be created once per process (not per reload) to avoid
// leaking goroutines. Non-positive ttl or sweep fall back to sensible defaults.
func NewRateLimiterStore(ctx context.Context, ttl, sweep time.Duration) *RateLimiterStore {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	if sweep <= 0 {
		sweep = time.Minute
	}
	s := &RateLimiterStore{ttl: ttl}
	for i := range s.shards {
		s.shards[i].entries = make(map[string]*rateLimiterEntry)
	}
	go s.janitor(ctx, sweep)
	return s
}

// Scoped returns a Limiter that applies ratePerSec/burst within a named scope.
// The scope namespaces keys so that distinct policies (for example a shared
// global bucket versus a per-location bucket) never collide inside the shared
// store. Keeping rate.Limit internal lets callers think in plain requests/sec.
func (s *RateLimiterStore) Scoped(scope string, ratePerSec, burst int) Limiter {
	return &scopedLimiter{store: s, prefix: scope + "\x00", limit: rate.Limit(ratePerSec), burst: burst}
}

type scopedLimiter struct {
	store  *RateLimiterStore
	prefix string
	limit  rate.Limit
	burst  int
}

func (l *scopedLimiter) Allow(key string) (bool, time.Duration) {
	return l.store.allow(l.prefix+key, l.limit, l.burst)
}

// allow applies the bucket identified by key at the given rate/burst. When the
// stored bucket's parameters differ (for example after a config reload) they are
// updated in place so new limits take effect immediately without dropping the
// bucket's accumulated state.
func (s *RateLimiterStore) allow(key string, limit rate.Limit, burst int) (bool, time.Duration) {
	sh := &s.shards[shardIndex(key)]
	now := time.Now()

	sh.mu.Lock()
	e := sh.entries[key]
	if e == nil {
		e = &rateLimiterEntry{lim: rate.NewLimiter(limit, burst), limit: limit, burst: burst}
		sh.entries[key] = e
	} else {
		if e.limit != limit {
			e.lim.SetLimit(limit)
			e.limit = limit
		}
		if e.burst != burst {
			e.lim.SetBurst(burst)
			e.burst = burst
		}
	}
	e.lastSeen = now
	lim := e.lim
	sh.mu.Unlock()

	// rate.Limiter is itself concurrency-safe, so the reservation runs outside
	// the shard lock. Reserve (not Allow) is used to recover the wait time for
	// Retry-After; a rejected request cancels its reservation so it does not
	// consume a token it never used.
	r := lim.ReserveN(now, 1)
	if !r.OK() {
		return false, 0
	}
	if d := r.DelayFrom(now); d > 0 {
		r.CancelAt(now)
		return false, d
	}
	return true, 0
}

func (s *RateLimiterStore) janitor(ctx context.Context, sweep time.Duration) {
	t := time.NewTicker(sweep)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.evict(now)
		}
	}
}

func (s *RateLimiterStore) evict(now time.Time) {
	cutoff := now.Add(-s.ttl)
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.Lock()
		for k, e := range sh.entries {
			if e.lastSeen.Before(cutoff) {
				delete(sh.entries, k)
			}
		}
		sh.mu.Unlock()
	}
}

// shardIndex maps a key to a shard using an inline FNV-1a hash, avoiding the
// allocation a hash.Hash instance would incur on every request.
func shardIndex(key string) int {
	const (
		offset32 = 2166136261
		prime32  = 16777619
	)
	var h uint32 = offset32
	for i := 0; i < len(key); i++ {
		h ^= uint32(key[i])
		h *= prime32
	}
	return int(h % rateLimiterShards)
}

// KeyFunc extracts the rate-limit bucket key from a request.
type KeyFunc func(*http.Request) string

// RateLimit returns middleware that admits requests through lim keyed by key.
// Rejected requests receive 429 with a Retry-After header (whole seconds,
// rounded up); onLimited, when non-nil, is invoked on each rejection for
// observability. A nil lim or key disables the middleware.
func RateLimit(lim Limiter, key KeyFunc, onLimited func()) Middleware {
	return func(next http.Handler) http.Handler {
		if lim == nil || key == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ok, retryAfter := lim.Allow(key(r))
			if !ok {
				if retryAfter > 0 {
					secs := int((retryAfter + time.Second - 1) / time.Second)
					if secs < 1 {
						secs = 1
					}
					w.Header().Set("Retry-After", strconv.Itoa(secs))
				}
				if onLimited != nil {
					onLimited()
				}
				http.Error(w, "429 Too Many Requests", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RateKeyFunc builds a KeyFunc from a key spec: "ip" (or "") keys on the client
// address, "header:<Name>" keys on a request header, and "jwt:<claim>" keys on a
// validated JWT claim placed in the request context by the auth middleware (it
// falls back to the client IP when no such claim is present). The client address
// is always the real transport peer, never an untrusted X-Forwarded-For value,
// so the key cannot be spoofed. Any empty extraction falls back to the client IP
// so a non-empty key is always produced.
func RateKeyFunc(spec string) KeyFunc {
	switch {
	case strings.HasPrefix(spec, "header:"):
		name := http.CanonicalHeaderKey(spec[len("header:"):])
		return func(r *http.Request) string {
			if v := r.Header.Get(name); v != "" {
				return v
			}
			return clientIP(r)
		}
	case strings.HasPrefix(spec, "jwt:"):
		// Key on a validated JWT claim placed in the request context by the auth
		// middleware. When auth is not configured (or the claim is absent or not
		// a string), fall back to the client IP so the limiter still functions.
		claim := spec[len("jwt:"):]
		return func(r *http.Request) string {
			if claims := ClaimsFrom(r.Context()); claims != nil {
				if v, ok := claims[claim].(string); ok && v != "" {
					return v
				}
			}
			return clientIP(r)
		}
	default:
		return clientIP
	}
}
