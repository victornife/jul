package cache

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// cacheWriter streams the response to the client while buffering up to limit
// bytes for storage. If the body exceeds the limit, buffering stops and tooBig
// is set so the response is not cached.
type cacheWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	buf         bytes.Buffer
	limit       int64
	tooBig      bool
}

func (w *cacheWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
		w.ResponseWriter.WriteHeader(code)
	}
}

func (w *cacheWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if !w.tooBig {
		if int64(w.buf.Len()+len(p)) > w.limit {
			w.tooBig = true
			w.buf.Reset()
		} else {
			w.buf.Write(p)
		}
	}
	return w.ResponseWriter.Write(p)
}

// Flush passes through to the underlying writer when supported.
func (w *cacheWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// recorder is an in-memory http.ResponseWriter used for background
// revalidation.
type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
	limit  int64
	tooBig bool
}

func (r *recorder) Header() http.Header {
	if r.header == nil {
		r.header = http.Header{}
	}
	return r.header
}

func (r *recorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
}

func (r *recorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if !r.tooBig {
		if int64(r.body.Len()+len(p)) > r.limit {
			r.tooBig = true
		} else {
			r.body.Write(p)
		}
	}
	return len(p), nil
}

// requestNoStore reports whether the request opts out of caching.
func requestNoStore(r *http.Request) bool {
	cc := parseCacheControl(r.Header.Get("Cache-Control"))
	_, ok := cc["no-store"]
	return ok
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

// parseCacheControl parses a Cache-Control header into a directive map.
func parseCacheControl(v string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if i := strings.IndexByte(part, '='); i >= 0 {
			out[strings.ToLower(part[:i])] = strings.Trim(part[i+1:], `"`)
		} else {
			out[strings.ToLower(part)] = ""
		}
	}
	return out
}

// ccHasDirective reports whether a Cache-Control header includes a directive.
func ccHasDirective(v, name string) bool {
	_, ok := parseCacheControl(v)[name]
	return ok
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

// secs converts an integer-seconds directive value to a Duration.
func secs(v string) time.Duration {
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	return time.Duration(n) * time.Second
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
