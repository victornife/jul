// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"bufio"
	"bytes"
	"net"
	"net/http"
	"strings"
)

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
	// noStore marks a response that can never be stored whatever its headers
	// say: the connection was hijacked, the status is a protocol switch, or the
	// body is a live event stream.
	noStore  bool
	hijacked bool
	// snapshot is a clone of the response header map taken inside WriteHeader,
	// before delegating to the outer ResponseWriter. Every wrapper from here to
	// the real connection shares one http.Header map, and layers outside the
	// cache (compression in particular) mutate it after this call returns — so
	// buildEntry must consume this snapshot rather than re-read w.Header() once
	// the stack has unwound, or a stored entry can pair headers from one layer
	// with a body captured at another.
	snapshot http.Header
}

// dropCapture abandons the captured response. It is called the moment the
// response is known to be unstorable, so a long-lived stream never accumulates
// bytes that will only be discarded.
func (w *cacheWriter) dropCapture() {
	w.noStore = true
	w.buf.Reset()
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
	// only what the handler itself wrote.
	w.snapshot = cloneHeader(w.Header())
	// A 1xx status is an interim or protocol-switch response, not a
	// representation; 101 in particular means the connection is leaving HTTP.
	// An event stream never ends, so capturing it would only grow a buffer that
	// is discarded at the size limit.
	if code < http.StatusOK || isEventStream(w.Header()) {
		w.dropCapture()
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
		if int64(w.buf.Len()+len(p)) > w.limit {
			w.tooBig = true
			w.buf.Reset()
		} else {
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
	ct := h.Get("Content-Type")
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.EqualFold(strings.TrimSpace(ct), "text/event-stream")
}

// isUpgradeRequest reports whether r asks to switch protocols (RFC 9110 §7.8).
// Both halves are required: an Upgrade header is only meaningful when the same
// hop also lists "upgrade" in Connection.
func isUpgradeRequest(r *http.Request) bool {
	if r.Header.Get("Upgrade") == "" {
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
	r.status = code
	// A 1xx is an interim or protocol-switch response, not a representation, and
	// an event stream never ends — buffering one would grow to the size limit
	// before being discarded.
	if code < http.StatusOK || isEventStream(r.Header()) {
		r.noStore = true
		r.body.Reset()
	}
}

func (r *recorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.WriteHeader(http.StatusOK)
	}
	if !r.tooBig && !r.noStore {
		if int64(r.body.Len()+len(p)) > r.limit {
			r.tooBig = true
			r.body.Reset()
		} else {
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
	return r.Header.Get("Range") != "" || r.Header.Get("If-Range") != ""
}

// notModified reports whether a conditional request can be answered with 304
// from the cached entry.
func notModified(r *http.Request, e *Entry) bool {
	if inm := r.Header.Get("If-None-Match"); inm != "" && e.ETag != "" {
		for _, tag := range parseList(inm) {
			if tag == "*" || tag == e.ETag {
				return true
			}
		}
		return false
	}
	if ims := r.Header.Get("If-Modified-Since"); ims != "" && e.LastModified != "" {
		t1, err1 := http.ParseTime(ims)
		t2, err2 := http.ParseTime(e.LastModified)
		if err1 == nil && err2 == nil && !t2.After(t1) {
			return true
		}
	}
	return false
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

func cloneHeader(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, vs := range h {
		out[k] = append([]string(nil), vs...)
	}
	return out
}

// hopByHopHeaders are connection-specific headers that must not be cached or
// forwarded.
var hopByHopHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

func removeHopByHop(h http.Header) {
	for _, name := range parseList(h.Get("Connection")) {
		h.Del(name)
	}
	for _, name := range hopByHopHeaders {
		h.Del(name)
	}
}
