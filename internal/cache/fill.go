// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"jul/internal/tracing"
)

// fillOutcome is the bounded set of events cold-miss fill locking reports to
// the metrics observer (jul_cache_fill_locks_total{outcome}). Every value is a
// package constant and never derives from a request, a cache key, a path, a
// host, a user identity, or an error string.
type fillOutcome string

const (
	// fillOutcomeWait is recorded when a follower joins an active cold-miss fill.
	fillOutcomeWait fillOutcome = "wait"
	// fillOutcomeReused is recorded when a waiter wakes up and reuses the
	// completed entry after repeating lookup, Vary, freshness, and auth checks.
	fillOutcomeReused fillOutcome = "reused"
	// fillOutcomeNotReusable is recorded when a leader stored an entry, but
	// the completed fill cannot satisfy this waiter (for example, a Vary
	// mismatch or an authenticated request without shared-reuse permission).
	fillOutcomeNotReusable fillOutcome = "not_reusable"
	// fillOutcomeUncacheable is recorded when a waiter wakes up from a fill
	// that did not store an entry (non-cacheable response, origin failure,
	// capture skip, leader cancellation, or panic).
	fillOutcomeUncacheable fillOutcome = "uncacheable"
	// fillOutcomeTimeout is recorded when lock_timeout expires before the
	// active fill finishes and the waiter falls through to an uncached origin fetch.
	fillOutcomeTimeout fillOutcome = "timeout"
	// fillOutcomeCanceled is recorded when a waiter's own request context is
	// canceled while waiting for the active fill.
	fillOutcomeCanceled fillOutcome = "canceled"
)

// fillCall is the shared state of one in-flight cold-miss fill (#525 Part 1).
// Exactly one leader runs the origin fetch and calls finish; any number of
// concurrent compatible misses wait on done up to their configured lock_timeout.
type fillCall struct {
	done       chan struct{}
	joined     atomic.Int32
	stored     bool
	opID       uint64
	startGen   uint64
	finishOnce sync.Once
}

func newFillCall() *fillCall {
	return &fillCall{done: make(chan struct{})}
}

// finish records whether an entry was published and wakes all waiters. It is
// idempotent so early abandonment on an uncacheable header or capture overflow
// can release waiters before the response finishes streaming without conflicting
// with the deferred cleanup when the handler returns.
func (fc *fillCall) finish(stored bool) {
	fc.finishOnce.Do(func() {
		fc.stored = stored
		close(fc.done)
	})
}

// SetFillLockObserver installs a bounded counter for cold-miss fill-lock
// events. fn receives only the package's fillOutcome constants. Call it once
// at startup, before the cache serves traffic.
func (c *Cache) SetFillLockObserver(fn func(outcome string)) {
	if c == nil {
		return
	}
	c.onFillLock = fn
}

func (c *Cache) observeFillLock(outcome fillOutcome) {
	if c.onFillLock != nil {
		c.onFillLock(string(outcome))
	}
}

// fillLockKey derives the per-key cold-miss fill lock key.
//
// When no Vary stub exists yet (effKey == base), the lock key is the primary
// cache key (method + lowercased host + request URI), which contains no request
// headers. When a Vary stub already exists (effKey != base), the variant key is
// hashed with SHA-256 so varied header values are not copied directly into
// the lock table. Hashing is not a general privacy guarantee: request targets
// can contain secrets, as can other headers and their derived keys.
func fillLockKey(base, effKey string) string {
	if effKey == base {
		return base
	}
	return base + "\x00" + hashKey(effKey)
}

type fillCoordKey struct {
	key   string
	scope RouteScope
}

func fillCoordinationKey(base, effKey string, r *http.Request) fillCoordKey {
	return fillCoordKey{key: fillLockKey(base, effKey), scope: scopeForRequest(r)}
}

func (c *Cache) beginFill(fk fillCoordKey) (call *fillCall, leader bool) {
	c.genMu.Lock()
	defer c.genMu.Unlock()
	c.fillMu.Lock()
	defer c.fillMu.Unlock()
	if existing, ok := c.fills[fk]; ok {
		return existing, false
	}
	call = newFillCall()
	call.opID, call.startGen = c.beginInflightOriginOpLocked()
	c.fills[fk] = call
	return call, true
}

func (c *Cache) endFill(fk fillCoordKey, call *fillCall) {
	c.fillMu.Lock()
	if c.fills[fk] == call {
		delete(c.fills, fk)
	}
	c.fillMu.Unlock()
}

func (c *Cache) inflightFills() int {
	c.fillMu.Lock()
	defer c.fillMu.Unlock()
	return len(c.fills)
}

// fillOrWait coordinates a cold cache miss when [cache].lock is enabled. The
// first caller for fk becomes the fill leader; concurrent callers wait up to
// pol.LockTimeout and then re-evaluate lookup, Vary, freshness, and shared-cache
// authorization rules against the store.
func (c *Cache) fillOrWait(w http.ResponseWriter, r *http.Request, next http.Handler, base, effKey string, reqPolicy requestPolicy, pol CachePolicy, span tracing.Span, allowVariantRetry bool) {
	fk := fillCoordinationKey(base, effKey, r)
	call, leader := c.beginFill(fk)
	if leader {
		defer c.endInflightOriginOp(call.opID)
		defer func() {
			c.endFill(fk, call)
			call.finish(false)
		}()
		// Re-check lookup after acquiring leadership in case a previous leader
		// published its entry immediately before removing its fillCall.
		if _, served := c.serveCachedIfReusable(w, r, next, base, reqPolicy, c.clock(), span); served {
			c.endFill(fk, call)
			call.finish(true)
			return
		}
		span.SetString("cache.status", "MISS")
		span.End()
		c.leadFill(w, r, next, fk, call, pol)
		return
	}
	c.awaitFill(w, r, next, base, fk, call, reqPolicy, pol, span, allowVariantRetry)
}

func (c *Cache) leadFill(w http.ResponseWriter, r *http.Request, next http.Handler, fk fillCoordKey, call *fillCall, pol CachePolicy) {
	stored := false
	release := func(didStore bool) {
		c.endFill(fk, call)
		call.finish(didStore)
	}
	defer func() {
		release(stored)
	}()
	stored = c.fetchAndStoreWithStartGenAndPolicy(w, r, next, c.clock(), pol, call.startGen, func() {
		release(false)
	})
}

func (c *Cache) awaitFill(w http.ResponseWriter, r *http.Request, next http.Handler, base string, fk fillCoordKey, call *fillCall, reqPolicy requestPolicy, pol CachePolicy, span tracing.Span, allowVariantRetry bool) {
	c.observeFillLock(fillOutcomeWait)
	call.joined.Add(1)

	timer := time.NewTimer(pol.LockTimeout)
	defer timer.Stop()

	select {
	case <-call.done:
		if r.Context().Err() != nil {
			span.SetString("cache.status", "MISS")
			span.End()
			c.observeFillLock(fillOutcomeCanceled)
			return
		}
	case <-r.Context().Done():
		span.SetString("cache.status", "MISS")
		span.End()
		c.observeFillLock(fillOutcomeCanceled)
		return
	case <-timer.C:
		span.SetString("cache.status", "MISS")
		span.End()
		c.recordDecision(r, "fill_lock_timeout")
		c.observeFillLock(fillOutcomeTimeout)
		c.proxyUncached(w, r, next)
		return
	}

	if !call.stored {
		span.SetString("cache.status", "MISS")
		span.End()
		c.recordDecision(r, "fill_lock_uncacheable")
		c.observeFillLock(fillOutcomeUncacheable)
		c.fetchAndStoreWithPolicy(w, r, next, c.clock(), pol, nil)
		return
	}

	latestEffKey, served := c.serveCachedIfReusable(w, r, next, base, reqPolicy, c.clock(), span)
	if served {
		c.observeFillLock(fillOutcomeReused)
		return
	}

	c.recordDecision(r, "fill_lock_not_reusable")
	c.observeFillLock(fillOutcomeNotReusable)
	if allowVariantRetry && latestEffKey != base && fillCoordinationKey(base, latestEffKey, r) != fk {
		c.fillOrWait(w, r, next, base, latestEffKey, reqPolicy, pol, span, false)
		return
	}
	span.SetString("cache.status", "MISS")
	span.End()
	c.fetchAndStoreWithPolicy(w, r, next, c.clock(), pol, nil)
}

// proxyUncached forwards a timed-out fill-lock waiter directly to the origin
// without capturing or storing the response, so it never competes with or
// overwrites the still-active fill leader.
func (c *Cache) proxyUncached(w http.ResponseWriter, r *http.Request, next http.Handler) {
	w.Header().Set("X-Cache", stateMiss)
	next.ServeHTTP(w, r)
}
