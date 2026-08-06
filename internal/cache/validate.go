// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"net/http"
	"time"

	"jul/internal/background"
)

// validateAndServe is mandatory synchronous validation: the stored entry may
// only answer this request after the origin has confirmed or replaced it.
//
// It is entered when the request said no-cache or max-age=0, when the stored
// response said no-cache, when must-revalidate/proxy-revalidate forbids serving
// the entry stale, or when the entry has aged out of its stale window but still
// has a validator worth spending a conditional request on. Background
// stale-while-revalidate is never a substitute: nothing is written to the client
// before the origin answers.
//
// Deduplication reuses #131's call state, so a burst of concurrent validators
// for one (effective key, generation) produces exactly one origin request.
func (c *Cache) validateAndServe(w http.ResponseWriter, r *http.Request, next http.Handler, stored *Entry, effKey string, now time.Time) {
	gen, _ := background.Generation(r.Context())
	k := revalidateKey{key: effKey, gen: gen}

	if call, leader := c.beginRevalidate(k); leader {
		c.leadValidation(w, r, next, stored, k, call)
	} else {
		c.awaitValidation(w, r, next, call)
	}
}

// leadValidation runs the one origin request the joined validators share.
//
// The conditional request runs on the generation's background lease when one is
// installed. That is what decouples the leader from its own client: a waiter
// that goes away cancels only its wait, and the leader keeps running under the
// generation's bounded operation deadline for the waiters that remain. Without a
// lease there is no owner for detached work, so the request context is used and
// this client simply is the leader.
func (c *Cache) leadValidation(w http.ResponseWriter, r *http.Request, next http.Handler, stored *Entry, k revalidateKey, call *revalidateCall) {
	vctx := r.Context()
	if ctx, release, ok := background.Acquire(r.Context(), background.OpCacheRevalidate); ok {
		defer release()
		vctx = ctx
	}

	defer func() {
		// Ordering matters: drop the call state first so a later request can
		// start a fresh validation the instant a waiter is released.
		c.endRevalidate(k, call)
		if v := recover(); v != nil {
			// Waiters must not be stranded by a panic, and must not receive the
			// panic value: it is unbounded, request-influenced data. The panic
			// itself continues to the server's recoverer, which owns the
			// client-visible outcome.
			call.finish(nil, outcomePanic, errRevalidatePanic)
			c.observeRevalidation(outcomePanic)
			panic(v)
		}
		// A leader that returned without publishing an outcome must still
		// release its waiters rather than strand them.
		call.finish(nil, outcomeCanceled, context.Canceled)
	}()

	rec := &recorder{header: http.Header{}, limit: c.maxEntry}
	next.ServeHTTP(rec, conditionalRequest(r.Clone(vctx), stored))
	now := c.clock()

	switch {
	case rec.status == http.StatusNotModified:
		refreshed, action := c.merge304(stored, rec.header, now)
		if action == mergeDiscard {
			c.Delete(k.key)
			c.finishValidation(call, nil, outcomeUncacheable)
			c.fetchAndStore(w, r, next, c.clock())
			return
		}
		c.set(k.key, refreshed)
		c.finishValidation(call, refreshed, outcomeNotModified)
		c.serve(w, r, refreshed, stateRevalidated, now)

	case rec.status >= 500:
		// Validation failed. Serving the stored copy anyway is only permitted by
		// an explicit stale-if-error window, and never when the origin said
		// must-revalidate or proxy-revalidate.
		if sie := c.staleOnErrorWindow(stored); sie > 0 {
			refreshed := stored.Clone()
			refreshed.StaleUntil = now.Add(sie)
			c.set(k.key, refreshed)
			c.finishValidation(call, refreshed, outcomeOriginError)
			c.serve(w, r, refreshed, stateStale, now)
			return
		}
		c.finishValidation(call, nil, outcomeOriginError)
		writeRecorded(w, r, rec, stateMiss)

	case !rec.storable():
		// The origin's answer could not be buffered whole — it exceeded
		// memory_max_size, or it is a stream. The captured bytes are incomplete,
		// so they must not be sent. Re-fetch and stream instead: one extra
		// origin request, bounded to this path, in exchange for never
		// truncating a response.
		c.Delete(k.key)
		c.finishValidation(call, nil, outcomeUncacheable)
		c.fetchAndStore(w, r, next, c.clock())

	default:
		e := c.buildEntry(r, rec.status, rec.header, rec.body.Bytes(), now)
		if e == nil {
			// The origin replaced the representation with one that must not be
			// stored. The old copy described something that no longer exists.
			c.Delete(k.key)
			c.finishValidation(call, nil, outcomeUncacheable)
		} else {
			c.store(key(r), r, e)
			c.finishValidation(call, e, outcomeStored)
		}
		writeRecorded(w, r, rec, stateMiss)
	}
}

// awaitValidation waits for the leader's result instead of issuing a second
// origin request. The wait honors this client's own cancellation and nothing
// else, so giving up here never affects the leader or the other waiters.
func (c *Cache) awaitValidation(w http.ResponseWriter, r *http.Request, next http.Handler, call *revalidateCall) {
	entry, outcome, _ := call.wait(r.Context())
	if r.Context().Err() != nil {
		// This client is gone. Nothing to serve, and nothing to clean up: the
		// call state belongs to the leader.
		return
	}
	if entry != nil && validatedReusable(entry, r) {
		switch outcome {
		case outcomeNotModified, outcomeStored:
			c.serve(w, r, entry, stateRevalidated, c.clock())
			return
		case outcomeOriginError:
			// The leader's stale-if-error decision already applied the policy
			// this entry carries; a waiter inherits it rather than re-deciding.
			c.serve(w, r, entry, stateStale, c.clock())
			return
		}
	}
	// Origin error without a servable entry, uncacheable, canceled, panic, or a
	// result this request may not reuse: fetch a response of its own.
	c.fetchAndStore(w, r, next, c.clock())
}

// validatedReusable reports whether a just-validated entry may answer r. The
// freshness question is already settled — the origin was contacted during this
// request — so only the keying and the shared-cache authorization rule remain.
func validatedReusable(e *Entry, r *http.Request) bool {
	if e.IsVaryStub || !e.matchesVary(r) {
		return false
	}
	return r.Header.Get("Authorization") == "" || e.SharedAuthReuse
}

func (c *Cache) finishValidation(call *revalidateCall, e *Entry, outcome revalidateOutcome) {
	call.finish(e, outcome, nil)
	c.observeRevalidation(outcome)
}

// writeRecorded forwards a buffered origin response to the client.
func writeRecorded(w http.ResponseWriter, r *http.Request, rec *recorder, state string) {
	h := w.Header()
	for name, values := range rec.header {
		for _, v := range values {
			h.Add(name, v)
		}
	}
	h.Set("X-Cache", state)
	status := rec.status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(rec.body.Bytes())
	}
}
