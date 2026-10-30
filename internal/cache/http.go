// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"bufio"
	"bytes"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
)

// Reasons a response was streamed without being captured for storage, reported
// through the cache's capture-skip observer (jul_cache_capture_skipped_total).
const (
	// captureSkipOversize: the body is, or declares itself, larger than the
	// per-entry capture limit (memory_max_size).
	captureSkipOversize = "oversize"
	// captureSkipBudget: capturing it would exceed the bytes all in-flight
	// captures may hold at once.
	captureSkipBudget = "budget"
)

// captureBudget bounds the bytes every in-flight capture (miss tees and
// validation recorders) buffers at once, so concurrent misses cannot multiply
// the per-entry limit. Its ceiling is the per-entry limit itself: stored
// entries plus in-flight captures stay within twice memory_max_size.
type captureBudget struct{ inflight atomic.Int64 }

func (b *captureBudget) reserve(n, limit int64) bool {
	for {
		cur := b.inflight.Load()
		if cur+n > limit {
			return false
		}
		if b.inflight.CompareAndSwap(cur, cur+n) {
			return true
		}
	}
}

func (b *captureBudget) release(n int64) {
	if n > 0 {
		b.inflight.Add(-n)
	}
}

// captureAccount is one capture's share of the budget. A nil budget (writers
// built directly in tests) means unbounded.
type captureAccount struct {
	budget    *captureBudget
	reserved  int64
	onSkip    func(reason string)
	onFailure func()
}

func (a *captureAccount) grow(n int, limit int64) bool {
	if a.budget == nil {
		return true
	}
	if !a.budget.reserve(int64(n), limit) {
		return false
	}
	a.reserved += int64(n)
	return true
}

func (a *captureAccount) release() {
	if a.budget != nil {
		a.budget.release(a.reserved)
	}
	a.reserved = 0
}

func (a *captureAccount) skip(reason string) {
	a.release()
	if a.onSkip != nil {
		a.onSkip(reason)
	}
}

// declaredLength returns a single valid Content-Length, or -1.
func declaredLength(h http.Header) int64 {
	values := h.Values("Content-Length")
	if len(values) != 1 {
		return -1
	}
	n, err := strconv.ParseInt(strings.TrimSpace(values[0]), 10, 64)
	if err != nil || n < 0 {
		return -1
	}
	return n
}

// cacheWriter streams the response to the client while buffering up to limit
// bytes for storage. If the body exceeds the limit, buffering stops and tooBig
// is set so the response is not cached.
//
// It is never handed to the handler directly: respwriter.Wrap composes it with
// the underlying writer so the handler still sees exactly the optional
// interfaces the real connection offers. Enabling the cache on a route must not
// take Hijack away from a WebSocket upgrade, nor invent it on HTTP/2.
type cacheWriter struct {
	http.ResponseWriter
	status         int
	wroteHeader    bool
	buf            bytes.Buffer
	limit          int64
	memLimit       int64
	memBudgetLimit int64
	bodyLen        int64
	disk           *diskStore
	diskStream     *diskStreamCapture
	tooBig         bool
	acct           captureAccount
	// noStore marks a response that can never be stored whatever its headers
	// say: the connection was hijacked, the status is a protocol switch, or the
	// body is a live event stream.
	noStore           bool
	hijacked          bool
	writeErr          error
	headerCheck       func(code int, h http.Header) bool
	onDecision        func(string)
	onAbandon         func()
	tagHeaders        []string
	stripTagHeaders   bool
	interimTagHeaders http.Header
	// snapshot is the multiset difference between the response header map at
	// WriteHeader and entrySnapshot, taken before delegating to the outer
	// ResponseWriter. Every wrapper from here to the real connection shares one
	// http.Header map, and layers outside the cache (compression in particular)
	// mutate it after this call returns — so buildEntry must consume this
	// snapshot rather than re-read w.Header() once the stack has unwound, or a
	// stored entry can pair headers from one layer with a body captured at
	// another.
	snapshot http.Header
	// entrySnapshot is a clone of the response header map as it stood when the
	// cache handler was entered, before `next` ran. A layer outside the cache —
	// RequestID setting X-Request-ID, the cache's own X-Cache — pre-sets a
	// per-request field on this same shared map before the handler ever runs, and
	// that field is not part of the origin representation the handler produced.
	// snapshot keeps only what changed relative to this baseline (#332).
	entrySnapshot http.Header
}

func (w *cacheWriter) effMemLimit() int64 {
	if w.memLimit > 0 && w.memLimit < w.limit {
		return w.memLimit
	}
	return w.limit
}

func (w *cacheWriter) effMemBudgetLimit() int64 {
	if w.memBudgetLimit > 0 {
		return w.memBudgetLimit
	}
	return w.effMemLimit()
}

func (w *cacheWriter) notifyAbandon() {
	if w.onAbandon != nil {
		w.onAbandon()
	}
}

func (w *cacheWriter) releaseCapture() {
	w.acct.release()
	if w.diskStream != nil {
		w.disk.abortStream(w.diskStream)
		w.diskStream = nil
	}
}

// dropCapture abandons the captured response. It is called the moment the
// response is known to be unstorable, so a long-lived stream never accumulates
// bytes that will only be discarded.
func (w *cacheWriter) dropCapture() {
	w.noStore = true
	w.buf = bytes.Buffer{}
	w.releaseCapture()
	w.notifyAbandon()
}

// abandonCapture stops buffering a response that will not be stored and frees
// its buffer or temporary file now, not when the (possibly long) response
// finally ends.
func (w *cacheWriter) abandonCapture(reason string) {
	w.tooBig = true
	w.buf = bytes.Buffer{}
	if w.diskStream != nil {
		w.disk.abortStream(w.diskStream)
		w.diskStream = nil
	}
	w.acct.skip(reason)
	w.notifyAbandon()
}

// storable reports whether the captured response may be considered for storage.
func (w *cacheWriter) storable() bool { return !w.noStore && !w.tooBig }

func captureInitDiskStream(cl, memLimit int64, disk *diskStore, onSkip func(string), onFailure func(), diskStream **diskStreamCapture, abandon func(string), drop func()) {
	if cl <= memLimit {
		return
	}
	if disk == nil {
		abandon(captureSkipOversize)
		return
	}
	stream := disk.beginStreamCaptureObserved(onSkip, onFailure)
	if stream == nil {
		drop()
		return
	}
	*diskStream = stream
}

func captureWriteChunk(
	p []byte,
	buf *bytes.Buffer,
	bodyLen *int64,
	limit, memLimit, memBudgetLimit int64,
	disk *diskStore,
	acct *captureAccount,
	diskStream **diskStreamCapture,
	abandon func(string),
	drop func(),
) {
	switch {
	case *bodyLen+int64(len(p)) > limit:
		abandon(captureSkipOversize)
	case *diskStream != nil:
		if ok, reason := disk.writeStream(*diskStream, p); !ok {
			if reason != "" {
				abandon(reason)
			} else {
				drop()
			}
		} else {
			*bodyLen += int64(len(p))
		}
	case *bodyLen+int64(len(p)) > memLimit:
		if disk == nil {
			abandon(captureSkipOversize)
			return
		}
		prior := buf.Bytes()
		*buf = bytes.Buffer{}
		acct.release()
		stream := disk.beginStreamCaptureObserved(acct.onSkip, acct.onFailure)
		if stream == nil {
			drop()
			return
		}
		if ok, wReason := disk.writeStream(stream, prior, p); !ok {
			disk.abortStream(stream)
			if wReason != "" {
				abandon(wReason)
			} else {
				drop()
			}
			return
		}
		*diskStream = stream
		*bodyLen += int64(len(p))
	case !acct.grow(len(p), memBudgetLimit):
		abandon(captureSkipBudget)
	default:
		buf.Write(p)
		*bodyLen += int64(len(p))
	}
}

func (w *cacheWriter) WriteHeader(code int) {
	if w.wroteHeader || w.hijacked {
		return
	}
	// A 1xx other than 101 is an interim response: RFC 9110 §15.2 permits any
	// number of them ahead of exactly one final status, so it must pass through
	// without latching wroteHeader, without a header snapshot, and without
	// dropping the capture — that decision belongs to the real status. 101 is a
	// protocol switch, not an interim response, and keeps the normal treatment
	// below.
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		if w.stripTagHeaders && len(w.tagHeaders) > 0 {
			for _, name := range w.tagHeaders {
				if vals := w.Header().Values(name); len(vals) > 0 {
					if w.interimTagHeaders == nil {
						w.interimTagHeaders = make(http.Header)
					}
					w.interimTagHeaders[name] = append([]string(nil), vals...)
				}
			}
			stripOriginTagHeadersFromResponse(w.Header(), w.entrySnapshot, w.tagHeaders)
		}
		w.ResponseWriter.WriteHeader(code)
		return
	}
	w.status = code
	w.wroteHeader = true
	// Snapshot before delegating outward: once ResponseWriter.WriteHeader below
	// returns control to an outer wrapper such as compression, that wrapper is
	// free to mutate the shared header map (Content-Encoding, Content-Length,
	// Accept-Ranges, Vary) for bytes this writer never sees, since it buffers
	// only what the handler itself wrote. The multiset difference against
	// entrySnapshot then removes whatever an outer layer had already set before
	// the handler ran, so a pre-set field is never mistaken for one the handler
	// contributed.
	w.snapshot = headerDifference(w.Header(), w.entrySnapshot)
	if len(w.interimTagHeaders) > 0 {
		for name, vals := range w.interimTagHeaders {
			if len(w.snapshot.Values(name)) == 0 {
				w.snapshot[name] = append([]string(nil), vals...)
			}
		}
	}
	cl := declaredLength(w.Header())
	// A 1xx status is an interim or protocol-switch response, not a
	// representation; 101 in particular means the connection is leaving HTTP.
	// An event stream never ends, so capturing it would only grow a buffer that
	// is discarded at the size limit.
	if code < http.StatusOK || isEventStream(w.Header()) {
		if w.onDecision != nil {
			if isEventStream(w.Header()) {
				w.onDecision("sse_bypass")
			} else {
				w.onDecision("upgrade_bypass")
			}
		}
		w.dropCapture()
	} else if !w.noStore && cl > w.limit {
		w.abandonCapture(captureSkipOversize)
	} else if w.headerCheck != nil && !w.headerCheck(code, w.snapshot) {
		w.dropCapture()
	} else if !w.noStore {
		captureInitDiskStream(cl, w.effMemLimit(), w.disk, w.acct.onSkip, w.acct.onFailure, &w.diskStream, w.abandonCapture, w.dropCapture)
	}
	if w.stripTagHeaders && len(w.tagHeaders) > 0 {
		stripOriginTagHeadersFromResponse(w.Header(), w.entrySnapshot, w.tagHeaders)
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *cacheWriter) Write(p []byte) (int, error) {
	if w.hijacked {
		return 0, http.ErrHijacked
	}
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if !w.tooBig && !w.noStore && len(p) > 0 {
		captureWriteChunk(p, &w.buf, &w.bodyLen, w.limit, w.effMemLimit(), w.effMemBudgetLimit(), w.disk, &w.acct, &w.diskStream, w.abandonCapture, w.dropCapture)
	}
	n, err := w.ResponseWriter.Write(p)
	if err != nil && !errors.Is(err, http.ErrBodyNotAllowed) {
		w.writeErr = err
		w.dropCapture()
	}
	return n, err
}

// Flush passes through to the underlying writer. respwriter.Wrap exposes it only
// when the underlying writer is a Flusher.
//
// A flush does not by itself make the response unstorable: the standard reverse
// proxy flushes on every write of any response with an unknown Content-Length,
// so treating a flush as "streaming" would stop caching ordinary chunked
// responses. Unbounded accumulation is prevented by the event-stream rule above
// and by the existing size limit.
func (w *cacheWriter) Flush() {
	if w.hijacked {
		return
	}
	// Flush commits an implicit 200 on the wire even before a body is written.
	// Capture that final status and its headers before the underlying writer
	// commits them, so a later WriteHeader cannot store a different response.
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack hands the connection to the handler, which is how an HTTP/1.1 upgrade
// completes. Everything captured so far is discarded and the writer refuses
// further writes: once ownership transfers, the bytes on the wire are no longer
// an HTTP response this cache may store.
func (w *cacheWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	conn, buf, err := h.Hijack()
	if err == nil {
		w.hijacked = true
		w.dropCapture()
	}
	return conn, buf, err
}

// isEventStream reports whether the response is a Server-Sent Events stream.
func isEventStream(h http.Header) bool {
	for _, ct := range h.Values("Content-Type") {
		if i := strings.IndexByte(ct, ';'); i >= 0 {
			ct = ct[:i]
		}
		if strings.EqualFold(strings.TrimSpace(ct), "text/event-stream") {
			return true
		}
	}
	return false
}

// isUpgradeRequest reports whether r asks to switch protocols (RFC 9110 §7.8).
// Both halves are required: an Upgrade header is only meaningful when the same
// hop also lists "upgrade" in Connection.
func isUpgradeRequest(r *http.Request) bool {
	upgrade := false
	for _, v := range r.Header.Values("Upgrade") {
		if strings.TrimSpace(v) != "" {
			upgrade = true
			break
		}
	}
	if !upgrade {
		return false
	}
	for _, v := range r.Header.Values("Connection") {
		for _, tok := range parseList(v) {
			if strings.EqualFold(tok, "upgrade") {
				return true
			}
		}
	}
	return false
}

// recorder is an in-memory http.ResponseWriter used for background and
// synchronous revalidation. It applies the same never-store rules as the
// streaming capture writer, so a revalidation cannot store — or buffer — a
// response the foreground path would have refused.
type recorder struct {
	header         http.Header
	status         int
	body           bytes.Buffer
	limit          int64
	memLimit       int64
	memBudgetLimit int64
	bodyLen        int64
	disk           *diskStore
	diskStream     *diskStreamCapture
	tooBig         bool
	acct           captureAccount
	// noStore marks a response that can never be stored whatever its headers
	// say: an interim/protocol-switch status, or a live event stream.
	noStore         bool
	headerCheck     func(code int, h http.Header) bool
	onDecision      func(string)
	onAbandon       func()
	tagHeaders      []string
	stripTagHeaders bool
}

func (r *recorder) notifyAbandon() {
	if r.onAbandon != nil {
		r.onAbandon()
	}
}

func (r *recorder) effMemLimit() int64 {
	if r.memLimit > 0 && r.memLimit < r.limit {
		return r.memLimit
	}
	return r.limit
}

func (r *recorder) effMemBudgetLimit() int64 {
	if r.memBudgetLimit > 0 {
		return r.memBudgetLimit
	}
	return r.effMemLimit()
}

func (r *recorder) releaseCapture() {
	r.acct.release()
	if r.diskStream != nil {
		r.disk.abortStream(r.diskStream)
		r.diskStream = nil
	}
}

func (r *recorder) Header() http.Header {
	if r.header == nil {
		r.header = http.Header{}
	}
	return r.header
}

func (r *recorder) dropCapture() {
	r.noStore = true
	r.body = bytes.Buffer{}
	r.releaseCapture()
	r.notifyAbandon()
}

func (r *recorder) WriteHeader(code int) {
	if r.status != 0 {
		return
	}
	// Informational responses before 101 are interim. Capture the final
	// validation status instead of mistaking an early hint for the origin's
	// answer and issuing an unnecessary second request.
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		return
	}
	r.status = code
	cl := declaredLength(r.Header())
	// A 1xx is an interim or protocol-switch response, not a representation, and
	// an event stream never ends — buffering one would grow to the size limit
	// before being discarded.
	if code < http.StatusOK || isEventStream(r.Header()) {
		if r.onDecision != nil {
			if isEventStream(r.Header()) {
				r.onDecision("sse_bypass")
			} else {
				r.onDecision("upgrade_bypass")
			}
		}
		r.dropCapture()
	} else if cl > r.limit {
		r.abandon(captureSkipOversize)
	} else if r.headerCheck != nil && !r.headerCheck(code, r.Header()) {
		r.dropCapture()
	} else {
		captureInitDiskStream(cl, r.effMemLimit(), r.disk, r.acct.onSkip, r.acct.onFailure, &r.diskStream, r.abandon, r.dropCapture)
	}
}

// abandon stops buffering a response that cannot be stored and frees the body.
func (r *recorder) abandon(reason string) {
	r.tooBig = true
	r.body = bytes.Buffer{}
	if r.diskStream != nil {
		r.disk.abortStream(r.diskStream)
		r.diskStream = nil
	}
	r.acct.skip(reason)
	r.notifyAbandon()
}

func (r *recorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.WriteHeader(http.StatusOK)
	}
	if !r.tooBig && !r.noStore && len(p) > 0 {
		captureWriteChunk(p, &r.body, &r.bodyLen, r.limit, r.effMemLimit(), r.effMemBudgetLimit(), r.disk, &r.acct, &r.diskStream, r.abandon, r.dropCapture)
	}
	return len(p), nil
}

// storable reports whether the captured response may be considered for storage,
// and equivalently whether r.body holds the complete response bytes.
func (r *recorder) storable() bool { return !r.noStore && !r.tooBig }

// statusWriter observes the status of a response the cache forwards but never
// stores, so an unsafe method can decide whether it invalidates.
//
// It is composed through respwriter.Wrap like every other wrapper in the chain,
// so it neither removes nor invents an optional interface. It deliberately does
// not implement http.Hijacker: a hijacked exchange leaves HTTP, its status stays
// zero, and nothing is invalidated.
type statusWriter struct {
	http.ResponseWriter
	status          int
	wroteHeader     bool
	hijacked        bool
	stripTagHeaders bool
	tagHeaders      []string
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	conn, buf, err := h.Hijack()
	if err == nil {
		w.hijacked = true
	}
	return conn, buf, err
}

func (w *statusWriter) WriteHeader(code int) {
	if w.stripTagHeaders && len(w.tagHeaders) > 0 {
		stripConfiguredTagHeaders(w.Header(), w.tagHeaders)
	}
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	w.wroteHeader = true
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	// lgtm[go/reflected-xss] – This observes the status of a response the cache
	// only forwards. The bytes are the upstream application's, passed through
	// unchanged; that application is responsible for sanitizing its own output.
	return w.ResponseWriter.Write(p)
}

func (w *statusWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// isRangeRequest reports whether the request asks for part of a representation.
//
// If-Range counts even without Range: it only has meaning together with one, and
// a request carrying it is unambiguously about ranges. Both are checked before
// lookup so decision D05 (bypass, never substitute a stored full response, never
// store a 206) is taken before any cache state is consulted.
func isRangeRequest(r *http.Request) bool {
	// Any field presence opts the exchange out. Header.Get only sees the first
	// field, so an empty first line could otherwise hide a later Range value.
	return hasHeaderField(r.Header, "Range") || hasHeaderField(r.Header, "If-Range")
}

// notModified reports whether a conditional request can be answered with 304
// from the cached entry.
func notModified(r *http.Request, e *Entry) bool {
	if inm := r.Header.Values("If-None-Match"); len(inm) != 0 {
		// Presence takes precedence over If-Modified-Since, even when the
		// field is empty or the stored representation has no ETag.
		return matchesIfNoneMatch(inm, e.ETag)
	}
	if ims := r.Header.Values("If-Modified-Since"); len(ims) == 1 && e.LastModified != "" {
		t1, err1 := http.ParseTime(ims[0])
		t2, err2 := http.ParseTime(e.LastModified)
		if err1 == nil && err2 == nil && !t2.After(t1) {
			return true
		}
	}
	return false
}

// matchesIfNoneMatch reads every field line, splitting only on commas outside
// quoted entity tags. Weak comparison ignores the W/ prefix on either side.
// A malformed list cannot justify returning 304 for a cached representation.
func matchesIfNoneMatch(fields []string, stored string) bool {
	if len(fields) == 1 && strings.TrimSpace(fields[0]) == "*" {
		return true
	}
	want, valid := opaqueETag(stored)
	if !valid {
		return false
	}
	matched := false
	for _, line := range fields {
		start := 0
		quoted := false
		for i := 0; i <= len(line); i++ {
			if i < len(line) && line[i] == '"' {
				quoted = !quoted
			}
			if i < len(line) && (line[i] != ',' || quoted) {
				continue
			}
			part := strings.TrimSpace(line[start:i])
			if part != "" {
				got, ok := opaqueETag(part)
				if !ok {
					return false
				}
				matched = matched || got == want
			}
			start = i + 1
		}
		if quoted {
			return false
		}
	}
	return matched
}

func opaqueETag(tag string) (string, bool) {
	tag = strings.TrimPrefix(tag, "W/")
	if len(tag) < 2 || tag[0] != '"' || tag[len(tag)-1] != '"' {
		return "", false
	}
	for i := 1; i < len(tag)-1; i++ {
		if tag[i] < '!' || tag[i] == '"' || tag[i] == 0x7f {
			return "", false
		}
	}
	return tag, true
}

// parseList splits a comma-separated header value, trimming whitespace.
func parseList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Vary is a list field and may arrive on several response header lines. Each
// line contributes a field name to the cache key, including after a 304 merge.
func varyFields(h http.Header) []string {
	var out []string
	for k, lines := range h {
		if !strings.EqualFold(k, "Vary") {
			continue
		}
		for _, line := range lines {
			for part := range strings.SplitSeq(line, ",") {
				if p := strings.TrimSpace(part); p != "" {
					out = append(out, p)
				}
			}
		}
	}
	return out
}

// Header presence, not the first line's value, controls conservative shared
// cache decisions such as Authorization and Set-Cookie. An empty first line
// must not hide a later credential or session cookie.
func hasHeaderField(h http.Header, name string) bool {
	return len(h.Values(name)) > 0
}

func cloneHeader(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, vs := range h {
		out[k] = append([]string(nil), vs...)
	}
	return out
}

// headerDifference returns the multiset difference final − entry: for each
// field name, every value in final survives once for each occurrence beyond
// however many times entry already held that exact (name, value) pair.
//
// This is what makes "store only what the handler itself contributed" precise
// rather than a per-name denylist (#332): a field an outer layer pre-set and
// the handler never touched cancels out completely (X-Request-ID, this
// cache's own X-Cache); a field the handler overwrote with Set survives with
// the handler's value, because that value did not exist in entry; a field the
// handler added an extra value to keeps only the addition. entry may be nil,
// which behaves as an empty map — every value in final survives.
func headerDifference(final, entry http.Header) http.Header {
	diff := make(http.Header, len(final))
	for name, values := range final {
		used := make([]bool, len(entry[name]))
		for _, v := range values {
			consumed := false
			for i, ev := range entry[name] {
				if !used[i] && ev == v {
					used[i] = true
					consumed = true
					break
				}
			}
			if !consumed {
				diff[name] = append(diff[name], v)
			}
		}
	}
	return diff
}

// hopByHopHeaders are connection-specific headers that must not be cached or
// forwarded.
var hopByHopHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

func removeHopByHop(h http.Header) {
	for _, line := range h.Values("Connection") {
		for _, name := range parseList(line) {
			h.Del(name)
		}
	}
	for _, name := range hopByHopHeaders {
		h.Del(name)
	}
}
