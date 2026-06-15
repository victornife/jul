package middleware

import (
	"bufio"
	"errors"
	"net"
	"net/http"
)

// responseRecorder wraps http.ResponseWriter to capture the status code and the
// number of bytes written, while transparently forwarding optional interfaces
// (Flusher, Hijacker) used by streaming and WebSocket proxying.
type responseRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

func newRecorder(w http.ResponseWriter) *responseRecorder {
	return &responseRecorder{ResponseWriter: w, status: http.StatusOK}
}

// Status returns the response status code (defaults to 200 if WriteHeader was
// never called).
func (r *responseRecorder) Status() int { return r.status }

// StatusWriter is a ResponseWriter that exposes the final response status code.
type StatusWriter interface {
	http.ResponseWriter
	Status() int
}

// WrapResponseWriter wraps w to capture the response status code while
// forwarding optional Flusher/Hijacker interfaces. If w already records its
// status, it is returned unchanged to avoid double wrapping.
func WrapResponseWriter(w http.ResponseWriter) StatusWriter {
	if sw, ok := w.(StatusWriter); ok {
		return sw
	}
	return newRecorder(w)
}

func (r *responseRecorder) WriteHeader(code int) {
	if r.wroteHeader {
		return
	}
	r.status = code
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}

// Flush forwards to the underlying writer when supported (streaming/SSE).
func (r *responseRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack forwards to the underlying writer when supported (WebSocket upgrades).
func (r *responseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := r.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("underlying ResponseWriter does not support hijacking")
}
