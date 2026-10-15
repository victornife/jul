// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"jul/internal/tracing"
)

// rangeOutcome is the closed set of label values for
// jul_cache_range_requests_total{outcome} (#442 Option A, ADR 0024).
type rangeOutcome string

const (
	rangeOutcomeHit           rangeOutcome = "hit"
	rangeOutcomeFill          rangeOutcome = "fill"
	rangeOutcomeFallback      rangeOutcome = "fallback"
	rangeOutcomeIfRangeFull   rangeOutcome = "if_range_full"
	rangeOutcomeUnsatisfiable rangeOutcome = "unsatisfiable"
	rangeOutcomeInvalid       rangeOutcome = "invalid"
)

// SetRangeObserver installs a bounded counter for range-aware cache decisions
// (#442). fn receives only the package's rangeOutcome constants. Call it once
// at startup, before the cache serves traffic.
func (c *Cache) SetRangeObserver(fn func(outcome string)) {
	if c == nil {
		return
	}
	c.onRange = fn
}

func (c *Cache) observeRange(outcome rangeOutcome) {
	if c != nil && c.onRange != nil {
		c.onRange(string(outcome))
	}
}

type rangeKind uint8

const (
	rangeKindPrefix rangeKind = iota + 1 // first-last
	rangeKindOpen                        // first-
	rangeKindSuffix                      // -suffix
)

type byteRangeSpec struct {
	kind   rangeKind
	first  int64
	last   int64
	suffix int64
}

type rangeParseResult uint8

const (
	rangeParseSingle rangeParseResult = iota + 1
	rangeParseMulti
	rangeParseNonBytes
	rangeParseInvalid
)

const maxRangeParserHardCap = 16

type rangeContextKey struct{}

func withRangeSpec(r *http.Request, spec byteRangeSpec) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), rangeContextKey{}, spec))
}

func rangeSpecFromRequest(r *http.Request) (byteRangeSpec, bool) {
	spec, ok := r.Context().Value(rangeContextKey{}).(byteRangeSpec)
	return spec, ok
}

// parseRangeHeader parses the request's Range header lines under the configured
// maxRanges bound (RFC 9110 §14.1.2 / §14.2).
func parseRangeHeader(values []string, maxRanges int) (byteRangeSpec, rangeParseResult) {
	if len(values) != 1 {
		return byteRangeSpec{}, rangeParseInvalid
	}
	if maxRanges <= 0 {
		maxRanges = 1
	}
	hardCap := maxRanges
	if hardCap < maxRangeParserHardCap {
		hardCap = maxRangeParserHardCap
	}

	raw := textproto.TrimString(values[0])
	eq := strings.IndexByte(raw, '=')
	if eq <= 0 {
		return byteRangeSpec{}, rangeParseInvalid
	}
	unit := raw[:eq]
	if !isHTTPToken(unit) {
		return byteRangeSpec{}, rangeParseInvalid
	}
	if !strings.EqualFold(unit, "bytes") {
		return byteRangeSpec{}, rangeParseNonBytes
	}

	rest := textproto.TrimString(raw[eq+1:])
	if rest == "" {
		return byteRangeSpec{}, rangeParseInvalid
	}

	var firstSpec byteRangeSpec
	count := 0
	segments := 0
	for {
		var part string
		hasMore := false
		if idx := strings.IndexByte(rest, ','); idx >= 0 {
			part = textproto.TrimString(rest[:idx])
			rest = rest[idx+1:]
			hasMore = true
		} else {
			part = textproto.TrimString(rest)
			rest = ""
		}
		segments++
		if segments > hardCap {
			return byteRangeSpec{}, rangeParseInvalid
		}
		// RFC 9110 §5.6.1.2 & §14.1.1: range-set uses 1#range-spec, so empty
		// list elements (e.g. "bytes=,0-1" or "bytes=0-1, ,") are ignored
		// within the bounded scan limit.
		if part != "" {
			count++
			if count > maxRanges {
				return byteRangeSpec{}, rangeParseInvalid
			}
			spec, ok := parseSingleByteRangeSpec(part)
			if !ok {
				return byteRangeSpec{}, rangeParseInvalid
			}
			if count == 1 {
				firstSpec = spec
			}
		}
		if !hasMore {
			break
		}
	}

	if count == 0 {
		return byteRangeSpec{}, rangeParseInvalid
	}
	if count > 1 {
		return byteRangeSpec{}, rangeParseMulti
	}
	return firstSpec, rangeParseSingle
}

func isHTTPToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		switch c {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		default:
			return false
		}
	}
	return true
}

func parseSingleByteRangeSpec(s string) (byteRangeSpec, bool) {
	if s == "" {
		return byteRangeSpec{}, false
	}
	dash := strings.IndexByte(s, '-')
	if dash < 0 {
		return byteRangeSpec{}, false
	}
	if dash == 0 {
		suffix, ok := parseDecimalInt63(s[1:])
		if !ok {
			return byteRangeSpec{}, false
		}
		return byteRangeSpec{kind: rangeKindSuffix, suffix: suffix}, true
	}
	first, ok := parseDecimalInt63(s[:dash])
	if !ok {
		return byteRangeSpec{}, false
	}
	rem := s[dash+1:]
	if rem == "" {
		return byteRangeSpec{kind: rangeKindOpen, first: first}, true
	}
	last, ok := parseDecimalInt63(rem)
	if !ok || first > last {
		return byteRangeSpec{}, false
	}
	return byteRangeSpec{kind: rangeKindPrefix, first: first, last: last}, true
}

func parseDecimalInt63(s string) (int64, bool) {
	if len(s) == 0 || len(s) > 19 {
		return 0, false
	}
	var n uint64
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		digit := uint64(c - '0')
		if n > (uint64(math.MaxInt64)-digit)/10 {
			return 0, false
		}
		n = n*10 + digit
	}
	return int64(n), true
}

// resolveByteRange resolves spec against a representation of totalLen bytes
// (RFC 9110 §14.1.2). It returns ok=false when the range is unsatisfiable (416).
func resolveByteRange(spec byteRangeSpec, totalLen int64) (start, length int64, ok bool) {
	if totalLen <= 0 {
		return 0, 0, false
	}
	switch spec.kind {
	case rangeKindPrefix:
		if spec.first >= totalLen {
			return 0, 0, false
		}
		end := spec.last
		if end >= totalLen {
			end = totalLen - 1
		}
		return spec.first, end - spec.first + 1, true
	case rangeKindOpen:
		if spec.first >= totalLen {
			return 0, 0, false
		}
		return spec.first, totalLen - spec.first, true
	case rangeKindSuffix:
		if spec.suffix <= 0 {
			return 0, 0, false
		}
		if spec.suffix >= totalLen {
			return 0, totalLen, true
		}
		return totalLen - spec.suffix, spec.suffix, true
	default:
		return 0, 0, false
	}
}

// ifRangeMatches evaluates the If-Range precondition against e (RFC 9110 §13.1.5,
// §5.6.7, and §8.8.2.2). Entity-tags require syntactically valid strong
// comparison via opaqueETag (weak tags and invalid etagc characters never match).
// HTTP-dates accept all three RFC 9110 §5.6.7 formats (IMF-fixdate, RFC 850,
// and ANSI C asctime), require exact timestamp equality with e.LastModified,
// and require a valid stored Date header at least one second later than
// Last-Modified so implicitly weak Last-Modified timestamps are rejected by an
// intermediary cache.
func ifRangeMatches(r *http.Request, e *Entry) bool {
	vals := r.Header.Values("If-Range")
	if len(vals) != 1 {
		return false
	}
	val := textproto.TrimString(vals[0])
	if val == "" {
		return false
	}
	if strings.HasPrefix(val, "W/") || strings.HasPrefix(val, "w/") {
		return false
	}
	if strings.HasPrefix(val, "\"") {
		if e.ETag == "" || strings.HasPrefix(e.ETag, "W/") || strings.HasPrefix(e.ETag, "w/") {
			return false
		}
		reqTag, okReq := opaqueETag(val)
		storedTag, okStored := opaqueETag(e.ETag)
		return okReq && okStored && reqTag == storedTag
	}
	// HTTP-date comparison: RFC 9110 §5.6.7, §13.1.5, and §8.8.2.2 require all
	// three HTTP-date formats to be accepted, exact timestamp equality with
	// Last-Modified, and a stored Date header at least one second later than
	// Last-Modified for the timestamp to qualify as a strong validator.
	if e.LastModified == "" || e.Header == nil {
		return false
	}
	refNow := e.CreatedAt
	if refNow.IsZero() {
		refNow = time.Now().UTC()
	}
	tReq, okReq := parseCacheDate(val, refNow)
	if !okReq {
		return false
	}
	tEntry, okEntry := parseCacheDate(textproto.TrimString(e.LastModified), refNow)
	if !okEntry || !tReq.Equal(tEntry) {
		return false
	}
	dateVals := e.Header.Values("Date")
	if len(dateVals) != 1 {
		return false
	}
	tDate, okDate := parseCacheDate(textproto.TrimString(dateVals[0]), refNow)
	if !okDate {
		return false
	}
	return tDate.Sub(tEntry) >= time.Second
}

func (c *Cache) fallbackRange(w http.ResponseWriter, r *http.Request, next http.Handler, span tracing.Span) {
	c.observeRange(rangeOutcomeFallback)
	c.bypass(w, r, next, span)
}

// handleRangeRequest processes a request carrying Range and/or If-Range when
// [cache].range_enabled is true (#442 Option A).
func (c *Cache) handleRangeRequest(w http.ResponseWriter, r *http.Request, next http.Handler, pol CachePolicy, span tracing.Span) {
	// RFC 9110 §14.2: Range is defined only for GET. For HEAD, do not synthesize
	// 206 responses or trigger a full-object GET fill; bypass to preserve exact
	// origin HEAD behavior.
	if r.Method != http.MethodGet {
		c.bypass(w, r, next, span)
		return
	}

	reqPolicy := parseRequestPolicy(r)
	if reqPolicy.NoStore {
		c.bypass(w, r, next, span)
		return
	}

	// RFC 9110 §13.1.5: If-Range without Range is ignored; process as a normal
	// full-representation GET.
	if !hasHeaderField(r.Header, "Range") {
		rClean := r.Clone(r.Context())
		rClean.Header.Del("If-Range")
		c.serveOrFillFull(w, rClean, next, reqPolicy, pol, span)
		return
	}

	spec, res := parseRangeHeader(r.Header.Values("Range"), pol.MaxRanges)
	switch res {
	case rangeParseInvalid:
		c.observeRange(rangeOutcomeInvalid)
		c.bypass(w, r, next, span)
		return
	case rangeParseNonBytes, rangeParseMulti:
		c.fallbackRange(w, r, next, span)
		return
	}

	rWithSpec := withRangeSpec(r, spec)
	k := key(rWithSpec)
	now := c.clock()
	effKey, served := c.serveCachedIfReusable(w, rWithSpec, next, k, reqPolicy, now, span)
	if served {
		return
	}

	if pol.Lock {
		c.rangeFillOrWait(w, r, rWithSpec, next, k, effKey, reqPolicy, pol, span, effKey == k)
		return
	}
	if e := c.performRangeFill(r, next, k, pol, nil); e != nil && e.matchesVary(rWithSpec) {
		if c.serve(w, rWithSpec, e, stateMiss, c.clock()) {
			span.SetString("cache.status", "MISS")
			span.End()
			return
		}
	}
	c.fallbackRange(w, r, next, span)
}

func (c *Cache) rangeFillOrWait(w http.ResponseWriter, origReq, rWithSpec *http.Request, next http.Handler, base, effKey string, reqPolicy requestPolicy, pol CachePolicy, span tracing.Span, allowVariantRetry bool) {
	fk := fillLockKey(base, effKey)
	call, leader := c.beginFill(fk)
	if leader {
		if _, served := c.serveCachedIfReusable(w, rWithSpec, next, base, reqPolicy, c.clock(), span); served {
			c.endFill(fk, call)
			call.finish(true)
			return
		}
		released := false
		release := func(didStore bool) {
			if !released {
				released = true
				c.endFill(fk, call)
				call.finish(didStore)
			}
		}
		defer func() { release(false) }()

		e := c.performRangeFill(origReq, next, base, pol, func() { release(false) })
		if e != nil && e.matchesVary(rWithSpec) {
			release(true)
			if c.serve(w, rWithSpec, e, stateMiss, c.clock()) {
				span.SetString("cache.status", "MISS")
				span.End()
				return
			}
		}
		release(false)
		if origReq.Context().Err() != nil {
			span.SetString("cache.status", "MISS")
			span.End()
			return
		}
		c.fallbackRange(w, origReq, next, span)
		return
	}

	c.observeFillLock(fillOutcomeWait)
	call.joined.Add(1)

	timer := time.NewTimer(pol.LockTimeout)
	defer timer.Stop()

	select {
	case <-call.done:
		if origReq.Context().Err() != nil {
			span.SetString("cache.status", "MISS")
			span.End()
			c.observeFillLock(fillOutcomeCanceled)
			return
		}
	case <-origReq.Context().Done():
		span.SetString("cache.status", "MISS")
		span.End()
		c.observeFillLock(fillOutcomeCanceled)
		return
	case <-timer.C:
		c.observeFillLock(fillOutcomeTimeout)
		c.fallbackRange(w, origReq, next, span)
		return
	}

	if !call.stored {
		c.observeFillLock(fillOutcomeUncacheable)
		c.fallbackRange(w, origReq, next, span)
		return
	}

	latestEffKey, served := c.serveCachedIfReusable(w, rWithSpec, next, base, reqPolicy, c.clock(), span)
	if served {
		c.observeFillLock(fillOutcomeReused)
		return
	}

	c.observeFillLock(fillOutcomeNotReusable)
	if allowVariantRetry && latestEffKey != base && fillLockKey(base, latestEffKey) != fk {
		c.rangeFillOrWait(w, origReq, rWithSpec, next, base, latestEffKey, reqPolicy, pol, span, false)
		return
	}
	c.fallbackRange(w, origReq, next, span)
}

func varyCompatibleWithModifiedRequest(h http.Header, modifiedReq, origReq *http.Request) bool {
	for _, name := range varyFields(h) {
		if name == "*" {
			return false
		}
		if varyRequestValue(modifiedReq.Header, name) != varyRequestValue(origReq.Header, name) {
			return false
		}
	}
	return true
}

// performRangeFill executes a complete-object origin fetch for origReq (with
// Range, If-Range, and conditional headers stripped) and publishes the entry if
// it is a complete, cacheable 200 OK representation that is Vary-compatible with
// origReq. If the origin response is uncacheable, varies on a stripped header,
// or exceeds capture bounds, the internal fetch context is canceled immediately
// so an unbounded response is not downloaded in full.
func (c *Cache) performRangeFill(origReq *http.Request, next http.Handler, base string, pol CachePolicy, onAbandon func()) *Entry {
	c.observeRange(rangeOutcomeFill)
	fillCtx, cancelFill := context.WithCancel(origReq.Context())
	defer cancelFill()

	fillReq := origReq.Clone(fillCtx)
	fillReq.Header.Del("Range")
	fillReq.Header.Del("If-Range")
	fillReq.Header.Del("If-None-Match")
	fillReq.Header.Del("If-Modified-Since")

	abortFill := func() {
		cancelFill()
		if onAbandon != nil {
			onAbandon()
		}
	}

	rec := c.newRecorder(pol)
	rec.headerCheck = func(code int, h http.Header) bool {
		return code == http.StatusOK &&
			c.headerStorable(fillReq, code, h, c.clock(), pol) &&
			varyCompatibleWithModifiedRequest(h, fillReq, origReq)
	}
	rec.onAbandon = abortFill
	defer rec.releaseCapture()

	next.ServeHTTP(rec, fillReq)
	if origReq.Context().Err() != nil || !rec.storable() || rec.status != http.StatusOK {
		return nil
	}

	now := c.clock()
	e := c.buildEntryFromCapture(fillReq, rec.status, rec.header, rec.body.Bytes(), rec.bodyLen, rec.diskStream != nil, now, pol)
	if e == nil || !e.matchesVary(origReq) || !c.storeCaptured(base, fillReq, e, rec.diskStream) {
		return nil
	}
	return e
}

// isContentScopedHeader reports whether k is a header field whose value is
// computed over the message content or exact response status/headers (such as
// RFC 9530 Content-Digest, historic Content-MD5, or RFC 9421 Signature and
// Signature-Input) rather than the complete selected representation (such as
// Repr-Digest). Synthetic 206 slices and 0-byte 416 responses transform the
// message status, headers, and content, so full-response content digests and
// HTTP message signatures must not be copied onto them.
func isContentScopedHeader(k string) bool {
	return strings.EqualFold(k, "Content-Digest") ||
		strings.EqualFold(k, "Content-MD5") ||
		strings.EqualFold(k, "Signature") ||
		strings.EqualFold(k, "Signature-Input")
}

func (c *Cache) serveRangeResponse(w http.ResponseWriter, e *Entry, spec byteRangeSpec, bodyReader BodyReader, state string, age int) {
	totalLen := e.BodyLen()
	start, length, ok := resolveByteRange(spec, totalLen)
	h := w.Header()
	if !ok {
		c.observeRange(rangeOutcomeUnsatisfiable)
		for k, vs := range e.Header {
			if strings.EqualFold(k, "Content-Length") || strings.EqualFold(k, "Content-Encoding") || strings.EqualFold(k, "Content-Range") || isContentScopedHeader(k) {
				continue
			}
			for i, v := range vs {
				if i == 0 {
					h.Set(k, v)
				} else {
					h.Add(k, v)
				}
			}
		}
		h.Set("Accept-Ranges", "bytes")
		h.Set("Content-Range", fmt.Sprintf("bytes */%d", totalLen))
		h.Set("Content-Length", "0")
		h.Set("Age", strconv.Itoa(age))
		h.Set("X-Cache", state)
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}

	c.observeRange(rangeOutcomeHit)
	for k, vs := range e.Header {
		if isContentScopedHeader(k) {
			continue
		}
		for i, v := range vs {
			if i == 0 {
				h.Set(k, v)
			} else {
				h.Add(k, v)
			}
		}
	}
	h.Set("Accept-Ranges", "bytes")
	h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, totalLen))
	h.Set("Content-Length", strconv.FormatInt(length, 10))
	h.Set("Age", strconv.Itoa(age))
	h.Set("X-Cache", state)
	w.WriteHeader(http.StatusPartialContent)

	if bodyReader != nil {
		_, _ = copyWithPooledBuffer(w, io.NewSectionReader(bodyReader, start, length))
		return
	}
	_, _ = w.Write(e.Body[start : start+length])
}
