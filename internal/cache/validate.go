// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"io"
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

	pol := c.Policy()
	opID, startGen := c.beginInflightOriginOp()
	defer c.endInflightOriginOp(opID)

	rec := c.newRecorder(pol)
	c.observeRecorder(rec, r)
	c.guardRevalidationCapture(rec, r, k.key, startGen)
	defer rec.releaseCapture()
	vreq := conditionalRequest(r.Clone(vctx), stored)
	next.ServeHTTP(rec, vreq)
	if rec.status == 0 && rec.onHeaders != nil {
		rec.onHeaders(http.StatusOK, rec.Header())
	}
	now := c.clock()

	_, isRange := rangeSpecFromRequest(r)
	fallbackAfterValidation := func() {
		if isRange {
			c.observeRange(rangeOutcomeFallback)
			w.Header().Set("X-Cache", stateBypass)
			c.serveDownstreamWithStrip(w, r, next)
			return
		}
		c.fetchAndStore(w, r, next, c.clock())
	}

	switch {
	case rec.status == http.StatusNotModified:
		refreshed, action, reason := c.merge304Decision(stored, rec.header, now)
		if reason != "" {
			c.recordDecision(r, reason)
		}
		if action == mergeDiscard {
			c.deleteAtMostGen(k.key, startGen)
			c.finishValidation(call, nil, outcomeUncacheable)
			fallbackAfterValidation()
			return
		}
		if c.setIfNotFenced(k.key, refreshed, startGen, stored.Tags) {
			c.delOneIfGenAtMost(k.key, startGen)
			c.finishValidation(call, nil, outcomeUncacheable)
			fallbackAfterValidation()
			return
		}
		c.finishValidation(call, refreshed, outcomeNotModified)
		if !c.serve(w, r, refreshed, stateRevalidated, now) {
			fallbackAfterValidation()
		}

	case rec.status >= 500:
		// Validation failed. Serving the stored copy anyway is only permitted by
		// an explicit stale-if-error window, and never when the origin said
		// must-revalidate or proxy-revalidate.
		if sie := c.staleOnErrorWindow(stored); sie > 0 {
			c.recordDecision(r, "stale_if_error")
			refreshed := stored.Clone()
			refreshed.StaleUntil = now.Add(sie)
			markStaleIfError(refreshed)
			if !c.setIfNotFenced(k.key, refreshed, startGen, stored.Tags) {
				c.recordDecision(r, "stale_if_error_extended")
				c.finishValidation(call, refreshed, outcomeOriginError)
				if c.serve(w, r, refreshed, stateStale, now) {
					return
				}
				fallbackAfterValidation()
				return
			}
		}
		c.finishValidation(call, nil, outcomeOriginError)
		if isRange || !rec.storable() || !writeRecorded(w, r, rec, nil, stateMiss) {
			fallbackAfterValidation()
		}

	case !rec.storable():
		// The origin's answer could not be buffered whole — it exceeded
		// max_entry_size, or it is a stream. The captured bytes are incomplete,
		// so they must not be sent. Re-fetch and stream instead: one extra
		// origin request, bounded to this path, in exchange for never
		// truncating a response.
		c.delOneIfGenAtMost(k.key, startGen)
		c.finishValidation(call, nil, outcomeUncacheable)
		fallbackAfterValidation()

	default:
		e := c.buildEntryFromCapture(vreq, rec.status, rec.header, rec.body.Bytes(), rec.bodyLen, rec.diskStream != nil, now, pol)
		if !c.captureMatchesRequest(e, r) || !c.storeCapturedWithStartGen(key(vreq), vreq, e, rec.diskStream, startGen, true) {
			// The origin replaced the representation with one that must not be
			// stored or that varies on a header modified for conditional/range
			// validation. The old copy described something that no longer exists.
			c.delOneIfGenAtMost(k.key, startGen)
			c.finishValidation(call, nil, outcomeUncacheable)
			if isRange || !varyCompatibleWithModifiedRequest(rec.header, vreq, r) || !writeRecorded(w, r, rec, nil, stateMiss) {
				fallbackAfterValidation()
			}
		} else {
			c.finishValidation(call, e, outcomeStored)
			if isRange {
				if !c.serve(w, r, e, stateMiss, now) {
					fallbackAfterValidation()
				}
			} else if !writeRecorded(w, r, rec, e, stateMiss) {
				c.fetchAndStore(w, r, next, c.clock())
			}
		}
	}
}

func (c *Cache) guardRevalidationCapture(rec *recorder, original *http.Request, key string, startGen uint64) {
	rec.onHeaders = func(code int, h http.Header) {
		if sensitiveVaryHeader(h) {
			c.recordDecision(original, "vary_sensitive_rejected")
			if code < http.StatusInternalServerError {
				c.deleteAtMostGen(key, startGen)
			}
		}
	}
	rec.headerCheck = func(_ int, h http.Header) bool {
		return !sensitiveVaryHeader(h)
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
			if c.serve(w, r, entry, stateRevalidated, c.clock()) {
				return
			}
		case outcomeOriginError:
			// The leader's stale-if-error decision already applied the policy
			// this entry carries; a waiter inherits it rather than re-deciding.
			if c.serve(w, r, entry, stateStale, c.clock()) {
				return
			}
		}
	}
	// Origin error without a servable entry, uncacheable, canceled, panic, or a
	// result this request may not reuse: fetch a response of its own.
	if _, isRange := rangeSpecFromRequest(r); isRange {
		c.observeRange(rangeOutcomeFallback)
		w.Header().Set("X-Cache", stateBypass)
		c.serveDownstreamWithStrip(w, r, next)
		return
	}
	c.fetchAndStore(w, r, next, c.clock())
}

// validatedReusable reports whether a just-validated entry may answer r. The
// freshness question is already settled — the origin was contacted during this
// request — so only the keying and the shared-cache authorization rule remain.
func validatedReusable(e *Entry, r *http.Request) bool {
	if e.IsVaryStub || sensitiveVaryEntry(e) || !e.matchesVary(r) {
		return false
	}
	return !hasHeaderField(r.Header, "Authorization") || e.SharedAuthReuse
}

func (c *Cache) finishValidation(call *revalidateCall, e *Entry, outcome revalidateOutcome) {
	call.finish(e, outcome, nil)
	c.observeRevalidation(outcome)
}

func openRecordedDiskBody(rec *recorder, published *Entry) (io.ReadCloser, bool) {
	if published != nil && published.diskBody != nil {
		if br, err := published.OpenBody(); err == nil {
			return br, true
		}
	}
	if rec.diskStream != nil {
		return openStreamBody(rec.diskStream, rec.bodyLen)
	}
	return nil, false
}

// writeRecorded forwards a buffered or disk-captured origin response to the
// client. When the response was captured to disk and no readable body handle
// can be opened, it returns false before committing response headers so the
// caller can fall back to streaming from the origin.
func writeRecorded(w http.ResponseWriter, r *http.Request, rec *recorder, published *Entry, state string) bool {
	var diskBody io.ReadCloser
	if r.Method != http.MethodHead && (rec.diskStream != nil || (published != nil && published.diskBody != nil)) {
		var ok bool
		diskBody, ok = openRecordedDiskBody(rec, published)
		if !ok {
			return false
		}
		defer func() { _ = diskBody.Close() }()
	}

	h := w.Header()
	// Set on the first value, Add on the rest — see the identical rule in
	// Cache.serve: a multi-value field still reproduces in full, but a
	// recorded field cannot stack beside a value an outer layer already set on
	// this same map (#332).
	for name, values := range rec.header {
		for i, v := range values {
			if i == 0 {
				h.Set(name, v)
			} else {
				h.Add(name, v)
			}
		}
	}
	if rec.stripTagHeaders && len(rec.tagHeaders) > 0 {
		stripConfiguredTagHeaders(h, rec.tagHeaders)
	}
	h.Set("X-Cache", state)
	status := rec.status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		if diskBody != nil {
			_, _ = copyWithPooledBuffer(w, diskBody)
			return true
		}
		// lgtm[go/reflected-xss] – This forwards the upstream response body
		// unchanged, together with its own Content-Type. User input flows to the
		// upstream application, which is responsible for sanitizing any output it
		// generates. Buffering it to decide whether a 304 could be served instead
		// does not make the cache the origin of the bytes.
		_, _ = w.Write(rec.body.Bytes())
	}
	return true
}
