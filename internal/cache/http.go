// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"bufio"
	"bytes"
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
	budget   *captureBudget
	reserved int64
	onSkip   func(reason string)
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
	status      int
	wroteHeader bool
	buf         bytes.Buffer
	limit       int64
	tooBig      bool
	acct        captureAccount
	// noStore marks a response that can never be stored whatever its headers
	// say: the connection was hijacked, the status is a protocol switch, or the
	// body is a live event stream.
	noStore  bool
	hijacked bool
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

// dropCapture abandons the captured response. It is called the moment the
// response is known to be unstorable, so a long-lived stream never accumulates
// bytes that will only be discarded.
func (w *cacheWriter) dropCapture() {
	w.noStore = true
	w.buf = bytes.Buffer{}
	w.acct.release()
}

// abandonCapture stops buffering a response that will not be stored and frees
// its buffer now, not when the (possibly long) response finally ends.
func (w *cacheWriter) abandonCapture(reason string) {
	w.tooBig = true
	w.buf = bytes.Buffer{}
	w.acct.skip(reason)
}

// storable reports whether the captured response may be considered for storage.
func (w *cacheWriter) storable() bool { return !w.noStore && !w.tooBig }

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
	// A 1xx status is an interim or protocol-switch response, not a
	// representation; 101 in particular means the connection is leaving HTTP.
	// An event stream never ends, so capturing it would only grow a buffer that
	// is discarded at the size limit.
	if code < http.StatusOK || isEventStream(w.Header()) {
		w.dropCapture()
	} else if !w.noStore && declaredLength(w.Header()) > w.limit {
		w.abandonCapture(captureSkipOversize)
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
	if !w.tooBig && !w.noStore {
		switch {
		case int64(w.buf.Len()+len(p)) > w.limit:
			w.abandonCapture(captureSkipOversize)
		case !w.acct.grow(len(p), w.limit):
			w.abandonCapture(captureSkipBudget)
		default:
			w.buf.Write(p)
		}
	}
	return w.ResponseWriter.Write(p)
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
	header http.Header
	status int
	body   bytes.Buffer
	limit  int64
	tooBig bool
	acct   captureAccount
	// noStore marks a response that can never be stored whatever its headers
	// say: an interim/protocol-switch status, or a live event stream.
	noStore bool
}

func (r *recorder) Header() http.Header {
	if r.header == nil {
		r.header = http.Header{}
	}
	return r.header
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
	// A 1xx is an interim or protocol-switch response, not a representation, and
	// an event stream never ends — buffering one would grow to the size limit
	// before being discarded.
	if code < http.StatusOK || isEventStream(r.Header()) {
		r.noStore = true
		r.body = bytes.Buffer{}
		r.acct.release()
	} else if declaredLength(r.Header()) > r.limit {
		r.abandon(captureSkipOversize)
	}
}

// abandon stops buffering a response that cannot be stored and frees the body.
func (r *recorder) abandon(reason string) {
	r.tooBig = true
	r.body = bytes.Buffer{}
	r.acct.skip(reason)
}

func (r *recorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.WriteHeader(http.StatusOK)
	}
	if !r.tooBig && !r.noStore {
		switch {
		case int64(r.body.Len()+len(p)) > r.limit:
			r.abandon(captureSkipOversize)
		case !r.acct.grow(len(p), r.limit):
			r.abandon(captureSkipBudget)
		default:
			r.body.Write(p)
		}
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
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	// lgtm[go/reflected-xss] – This observes the status of a response the cache
	// only forwards. The bytes are the upstream application's, passed through
	// unchanged; that application is responsible for sanitizing its own output.
	return w.ResponseWriter.Write(p)
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
	return parseList(strings.Join(h.Values("Vary"), ","))
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
