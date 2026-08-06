// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"errors"
	"sync"
)

// revalidateOutcome is the bounded set of results a background revalidation can
// produce. The values are safe as metric labels and log fields: they are
// package constants and never derive from a request, a key, or an error string.
type revalidateOutcome string

const (
	// outcomeStored means the origin returned a new cacheable representation.
	outcomeStored revalidateOutcome = "stored"
	// outcomeNotModified means the origin confirmed the stored representation.
	outcomeNotModified revalidateOutcome = "not_modified"
	// outcomeUncacheable means the origin answered, but the response must not
	// be stored (too large, or not cacheable per policy).
	outcomeUncacheable revalidateOutcome = "uncacheable"
	// outcomeOriginError means the origin returned 5xx; stale-if-error policy
	// decides whether the stale window is extended.
	outcomeOriginError revalidateOutcome = "origin_error"
	// outcomeCanceled means process shutdown, generation retirement, or the
	// bounded operation deadline ended the refresh.
	outcomeCanceled revalidateOutcome = "canceled"
	// outcomePanic means the downstream handler panicked.
	outcomePanic revalidateOutcome = "panic"
	// outcomeNoLease means no generation lease could be acquired, so no refresh
	// was started.
	outcomeNoLease revalidateOutcome = "no_lease"
	// outcomeDeduplicated means an equivalent refresh was already running.
	outcomeDeduplicated revalidateOutcome = "deduplicated"
)

// errRevalidatePanic is the error delivered to waiters when the downstream
// handler panicked. The panic value itself is never propagated to waiters or to
// observability: it is unbounded, attacker-influenced data.
var errRevalidatePanic = errors.New("cache: background revalidation panicked")

// revalidateKey identifies an in-flight revalidation. It pairs the effective
// storage key with the handler generation that owns the refresh, so a reload
// installs a fresh key space: a new generation is never made to wait on — or be
// suppressed by — a call still running against the retired handler tree.
type revalidateKey struct {
	key string
	gen uint64
}

// revalidateCall is the shared state of one in-flight revalidation. Exactly one
// goroutine (the leader) runs the refresh and calls finish; any number of
// waiters may observe the result. finish closes done exactly once, on every
// path — success, origin error, cancellation and panic — so a waiter can never
// be stranded.
type revalidateCall struct {
	done chan struct{}

	// Written by the leader before done is closed; read by waiters after.
	entry   *Entry
	outcome revalidateOutcome
	err     error

	finishOnce sync.Once
}

func newRevalidateCall() *revalidateCall {
	return &revalidateCall{done: make(chan struct{})}
}

// finish publishes the call result and releases every waiter. It is idempotent,
// so a deferred finish on the panic path cannot conflict with the normal one.
func (rc *revalidateCall) finish(entry *Entry, outcome revalidateOutcome, err error) {
	rc.finishOnce.Do(func() {
		rc.entry = entry
		rc.outcome = outcome
		rc.err = err
		close(rc.done)
	})
}

// wait blocks until the call finishes or ctx is done. It returns the published
// entry (nil when the refresh produced none), the bounded outcome, and the
// error. When ctx ends first it reports outcomeCanceled and ctx.Err(), leaving
// the call itself untouched for its leader to complete.
func (rc *revalidateCall) wait(ctx context.Context) (*Entry, revalidateOutcome, error) {
	select {
	case <-rc.done:
		return rc.entry, rc.outcome, rc.err
	case <-ctx.Done():
		return nil, outcomeCanceled, ctx.Err()
	}
}

// beginRevalidate returns the call state for k. It reports leader == true for
// the caller that must run the refresh, and false for a caller that joined an
// equivalent refresh already in flight. This bounds a burst of concurrent stale
// hits to exactly one origin request per key and generation.
func (c *Cache) beginRevalidate(k revalidateKey) (call *revalidateCall, leader bool) {
	c.reMu.Lock()
	defer c.reMu.Unlock()
	if existing, ok := c.calls[k]; ok {
		return existing, false
	}
	call = newRevalidateCall()
	c.calls[k] = call
	return call, true
}

// endRevalidate removes the call state for k, but only when it is still the
// call this leader created. Comparing identity means a late cleanup can never
// delete a newer call for the same key, which would otherwise let a second
// origin request start while the first is still running.
func (c *Cache) endRevalidate(k revalidateKey, call *revalidateCall) {
	c.reMu.Lock()
	if c.calls[k] == call {
		delete(c.calls, k)
	}
	c.reMu.Unlock()
}

// lookupRevalidation returns the in-flight call for k, if any. It is the seam a
// synchronous validator uses to wait on an existing refresh instead of issuing a
// duplicate origin request.
func (c *Cache) lookupRevalidation(k revalidateKey) (*revalidateCall, bool) {
	c.reMu.Lock()
	defer c.reMu.Unlock()
	call, ok := c.calls[k]
	return call, ok
}

// inflightRevalidations reports how many revalidation calls are registered. It
// exists so tests can assert that no call state is stranded after every
// outcome.
func (c *Cache) inflightRevalidations() int {
	c.reMu.Lock()
	defer c.reMu.Unlock()
	return len(c.calls)
}
