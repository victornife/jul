// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
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
func (c *Cache) validateAndServe(w http.ResponseWriter, r *http.Request, next http.Handler, stored *Entry, k revalidateKey, call *revalidateCall, leader bool, startGen uint64) {
	if leader {
		c.leadValidation(w, r, next, stored, k, call, startGen)
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
func (c *Cache) leadValidation(w http.ResponseWriter, r *http.Request, next http.Handler, stored *Entry, k revalidateKey, call *revalidateCall, startGen uint64) {
	vctx := r.Context()
	if ctx, release, ok := background.Acquire(r.Context(), background.OpCacheRevalidate); ok {
		defer release()
		vctx = carryRouteScope(ctx, r)
	}
	vctx, cancelValidation := context.WithCancel(vctx)
	defer cancelValidation()

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
	rec := c.newRecorder(pol)
	c.observeRecorder(rec, r)
	rejected := false
	var rejectedStale *Entry
	c.guardRevalidationCapture(rec, r, k.version(), startGen, func(code int, reason string) {
		if !rejected {
			rejected = true
			cancelValidation()
			if code >= http.StatusInternalServerError {
				if reason == "vary_sensitive_rejected" && r.Context().Err() == nil {
					rejectedStale = c.transientStaleOnError(stored, k.key, startGen, c.clock(), r, k.version())
				}
				c.endRevalidate(k, call)
				call.finish(nil, outcomeOriginError, errVaryRevalidationRejected)
				c.observeRevalidation(outcomeOriginError)
			} else {
				c.endRevalidate(k, call)
				call.finish(nil, outcomeUncacheable, errVaryRevalidationRejected)
				c.observeRevalidation(outcomeUncacheable)
			}
		}
	})
	defer rec.releaseCapture()
	vreq := conditionalRequest(r.Clone(vctx), stored)
	func() {
		defer func() {
			if v := recover(); v != nil && (!rejected || v != http.ErrAbortHandler) {
				panic(v)
			}
		}()
		next.ServeHTTP(rec, vreq)
	}()
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
	if rejected {
		if rejectedStale != nil && c.serve(w, r, rejectedStale, stateStale, c.clock()) {
			return
		}
		fallbackAfterValidation()
		return
	}

	switch {
	case rec.status == http.StatusNotModified:
		if !varyCompatibleWithValidation(stored, rec.committedHeader(), vreq, r) {
			c.recordDecision(r, "vary_changed")
			c.retireValidation(k.version(), startGen)
			c.finishValidation(call, nil, outcomeUncacheable)
			fallbackAfterValidation()
			return
		}
		refreshed, action, reason := c.merge304Decision(stored, rec.committedHeader(), now)
		if reason != "" {
			c.recordDecision(r, reason)
		}
		if action == mergeDiscard {
			c.retireValidation(k.version(), startGen)
			c.finishValidation(call, nil, outcomeUncacheable)
			fallbackAfterValidation()
			return
		}
		if c.setValidationIfCurrent(k.key, refreshed, startGen, stored.Tags, k.version()) {
			c.retireValidation(k.version(), startGen)
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
		if refreshed := c.extendStaleOnError(stored, k.key, startGen, now, r, k.version()); refreshed != nil {
			c.finishValidation(call, refreshed, outcomeOriginError)
			if c.serve(w, r, refreshed, stateStale, now) {
				return
			}
			fallbackAfterValidation()
			return
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
		if rec.status != http.StatusOK || rec.complete(vreq.Method) {
			c.retireValidation(k.version(), startGen)
		}
		c.finishValidation(call, nil, outcomeUncacheable)
		fallbackAfterValidation()

	default:
		if rec.status == http.StatusOK && !rec.complete(vreq.Method) {
			c.recordDecision(r, "capture_incomplete")
			c.finishValidation(call, nil, outcomeUncacheable)
			fallbackAfterValidation()
			return
		}
		e := c.buildEntryFromCapture(vreq, rec.status, rec.committedHeader(), rec.body.Bytes(), rec.bodyLen, rec.diskStream != nil, now, pol)
		version := k.version()
		if !c.captureMatchesRequest(e, r) || !c.storeCapturedWithStartGenAndTags(key(vreq), vreq, e, rec.diskStream, startGen, true, stored.Tags, &version) {
			// The origin replaced the representation with one that must not be
			// stored or that varies on a header modified for conditional/range
			// validation. The old copy described something that no longer exists.
			c.retireValidation(version, startGen)
			c.finishValidation(call, nil, outcomeUncacheable)
			if isRange || !varyCompatibleWithModifiedRequest(rec.committedHeader(), vreq, r) || !writeRecorded(w, r, rec, nil, stateMiss) {
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

func varyCompatibleWithValidation(stored *Entry, h304 http.Header, modifiedReq, origReq *http.Request) bool {
	if !varyFieldsCompatibleWithModifiedRequest(stored.Vary, modifiedReq, origReq) ||
		!varyCompatibleWithModifiedRequest(stored.Header, modifiedReq, origReq) ||
		!varyCompatibleWithModifiedRequest(h304, modifiedReq, origReq) {
		return false
	}
	for name := range stored.VaryValues {
		if !varyFieldsCompatibleWithModifiedRequest([]string{name}, modifiedReq, origReq) {
			return false
		}
	}
	return true
}

func (c *Cache) guardRevalidationCapture(rec *recorder, original *http.Request, version validationVersion, startGen uint64, onReject func(int, string)) {
	rec.onHeaders = func(code int, h http.Header) {
		if reason := varyRejectionHeader(original, nil, h); reason != "" {
			c.recordDecision(original, reason)
			var removed *keyGenState
			if code < http.StatusInternalServerError {
				removed = c.markRetiredValidation(version, startGen)
			}
			if onReject != nil {
				onReject(code, reason)
			}
			if removed != nil {
				c.finishOneRemoval(version.effective, startGen, removed)
			}
		}
	}
	rec.headerCheck = func(_ int, h http.Header) bool {
		return varyRejectionHeader(original, nil, h) == ""
	}
}

func (c *Cache) extendStaleOnError(stored *Entry, key string, startGen uint64, now time.Time, r *http.Request, version validationVersion) *Entry {
	sie := c.staleOnErrorWindow(stored)
	if sie <= 0 {
		return nil
	}
	c.recordDecision(r, "stale_if_error")
	refreshed := stored.Clone()
	refreshed.StaleUntil = now.Add(sie)
	markStaleIfError(refreshed)
	if c.setValidationIfCurrent(key, refreshed, startGen, stored.Tags, version) {
		return nil
	}
	c.recordDecision(r, "stale_if_error_extended")
	return refreshed
}

func (c *Cache) transientStaleOnError(stored *Entry, key string, startGen uint64, now time.Time, r *http.Request, version validationVersion) *Entry {
	sie := c.staleOnErrorWindow(stored)
	if sie <= 0 {
		return nil
	}
	c.genMu.Lock()
	current := c.isValidationCurrentLocked(version) && !c.isPublicationFencedLocked(startGen, key, stored.Tags, nil)
	c.genMu.Unlock()
	if !current {
		return nil
	}
	c.recordDecision(r, "stale_if_error")
	refreshed := *stored
	if stored.Evidence != nil {
		evidence := *stored.Evidence
		refreshed.Evidence = &evidence
	}
	refreshed.StaleUntil = now.Add(sie)
	markStaleIfError(&refreshed)
	return &refreshed
}

func isConfiguredTagTrailer(name string, tags []string) bool {
	name = strings.TrimPrefix(name, http.TrailerPrefix)
	for _, tag := range tags {
		if strings.EqualFold(name, tag) {
			return true
		}
	}
	return false
}

// awaitValidation waits for the leader's result instead of issuing a second
// origin request. The wait honors this client's own cancellation and nothing
// else, so giving up here never affects the leader or the other waiters.
func (c *Cache) awaitValidation(w http.ResponseWriter, r *http.Request, next http.Handler, call *revalidateCall) {
	entry, outcome, err := call.wait(r.Context())
	if r.Context().Err() != nil {
		// This client is gone. Nothing to serve, and nothing to clean up: the
		// call state belongs to the leader.
		return
	}
	if errors.Is(err, errVaryRevalidationRejected) {
		if _, isRange := rangeSpecFromRequest(r); isRange {
			c.observeRange(rangeOutcomeFallback)
		}
		w.Header().Set("X-Cache", stateBypass)
		c.serveDownstreamWithStrip(w, r, next)
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
	if e.IsVaryStub || entryScopeMismatch(e, r, nil) != "" || varyRejectionEntry(r, nil, e) != "" || !e.matchesVary(r) {
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
	for name, values := range rec.committedHeader() {
		if rec.stripTagHeaders && isConfiguredTagTrailer(name, rec.tagHeaders) {
			continue
		}
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
		var allowed []string
		for _, line := range h.Values("Trailer") {
			for _, name := range parseList(line) {
				if !isConfiguredTagTrailer(name, rec.tagHeaders) {
					allowed = append(allowed, name)
				}
			}
		}
		h.Del("Trailer")
		if len(allowed) > 0 {
			h.Set("Trailer", strings.Join(allowed, ", "))
		}
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
		} else {
			// lgtm[go/reflected-xss] – This forwards the upstream response body
			// unchanged, together with its own Content-Type. User input flows to the
			// upstream application, which is responsible for sanitizing any output it
			// generates. Buffering it to decide whether a 304 could be served instead
			// does not make the cache the origin of the bytes.
			_, _ = w.Write(rec.body.Bytes())
		}
	}
	declared := make(map[string]bool)
	for _, line := range rec.committedHeader().Values("Trailer") {
		for _, name := range parseList(line) {
			declared[http.CanonicalHeaderKey(name)] = true
		}
	}
	for name, values := range rec.Header() {
		if (declared[http.CanonicalHeaderKey(name)] || strings.HasPrefix(name, http.TrailerPrefix)) &&
			(!rec.stripTagHeaders || !isConfiguredTagTrailer(name, rec.tagHeaders)) {
			h[name] = append([]string(nil), values...)
		}
	}
	return true
}
