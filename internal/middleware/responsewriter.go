// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"jul/internal/respwriter"
)

type HTTPMetricLabels struct {
	mu    sync.Mutex
	host  string
	route string
}

func CanonicalHTTPHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if strings.HasPrefix(host, "[") {
		if end := strings.IndexByte(host, ']'); end >= 0 {
			rest := host[end+1:]
			if rest == "" || (strings.HasPrefix(rest, ":") && validHTTPHostPort(rest[1:])) {
				return host[:end+1]
			}
		}
		return ""
	}
	if end := strings.LastIndex(host, ":"); end >= 0 {
		if strings.Count(host, ":") != 1 || !validHTTPHostPort(host[end+1:]) {
			return ""
		}
		host = host[:end]
	}
	host = strings.TrimSuffix(host, ".")
	if strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") || strings.Contains(host, "..") {
		return ""
	}
	return host
}

func validHTTPHostPort(port string) bool {
	if port == "" {
		return false
	}
	for index := 0; index < len(port); index++ {
		if port[index] < '0' || port[index] > '9' {
			return false
		}
	}
	value, err := strconv.Atoi(port)
	return err == nil && value <= 65535
}

func WithHTTPMetricLabels(request *http.Request) (*http.Request, *HTTPMetricLabels) {
	ctx := request.Context()
	if scope := requestScopeFrom(ctx); scope != nil && scope.labels == nil {
		scope.labelsBuf = HTTPMetricLabels{host: "_other", route: "_unmatched"}
		scope.labels = &scope.labelsBuf
		return request, scope.labels
	}
	scope := cloneRequestScope(ctx)
	scope.labelsBuf = HTTPMetricLabels{host: "_other", route: "_unmatched"}
	scope.labels = &scope.labelsBuf
	return request.WithContext(context.WithValue(ctx, requestScopeKey{}, scope)), scope.labels
}

func SetHTTPMetricLabels(request *http.Request, host, route string) {
	if scope := requestScopeFrom(request.Context()); scope != nil && scope.labels != nil {
		scope.labels.mu.Lock()
		scope.labels.host, scope.labels.route = host, route
		scope.labels.mu.Unlock()
	}
}

func (labels *HTTPMetricLabels) Values() (string, string) {
	labels.mu.Lock()
	defer labels.mu.Unlock()
	return labels.host, labels.route
}

// Recorder observes a response as it is produced so an observer middleware
// (metrics, access log, tracing) can report the final status and byte count
// after the handler returns.
//
// A Recorder is never handed to the handler directly: Writer returns a
// capability-transparent wrapper, so the handler sees exactly the optional
// interfaces the real connection offers. Recording a response must not tell a
// handler it can hijack an HTTP/2 stream.
type Recorder struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
	hijacked    bool
}

// NewRecorder starts recording the response written to w.
func NewRecorder(w http.ResponseWriter) *Recorder {
	return &Recorder{ResponseWriter: w, status: http.StatusOK}
}

// Writer returns the value to pass down the chain. It forwards writes through
// the recorder while exposing exactly the optional interfaces of the underlying
// writer.
func (r *Recorder) Writer() http.ResponseWriter {
	return respwriter.WrapController(r, r.ResponseWriter)
}

func (r *Recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Status returns the response status code (200 if WriteHeader was never called).
func (r *Recorder) Status() int { return r.status }

// Bytes returns the number of body bytes written to the client.
func (r *Recorder) Bytes() int64 { return r.bytes }

// Hijacked reports whether the handler took over the connection.
func (r *Recorder) Hijacked() bool { return r.hijacked }

func (r *Recorder) WriteHeader(code int) {
	if r.wroteHeader || r.hijacked {
		return
	}
	// A 1xx other than 101 is an interim response: RFC 9110 §15.2 permits any
	// number of them ahead of exactly one final status, so it must pass through
	// without latching wroteHeader or the recorded status. 101 is a protocol
	// switch, not an interim response, and keeps the normal latch below.
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		r.ResponseWriter.WriteHeader(code)
		return
	}
	r.status = code
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(code)
}

func (r *Recorder) Write(b []byte) (int, error) {
	if r.hijacked {
		return 0, http.ErrHijacked
	}
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}

// Flush forwards to the underlying writer. respwriter.Wrap exposes it only when
// the underlying writer is a Flusher, so a handler's assertion stays truthful.
func (r *Recorder) Flush() {
	_ = r.FlushError()
}

func (r *Recorder) FlushError() error {
	if r.hijacked {
		return http.ErrHijacked
	}
	if !respwriter.SupportsFlush(r.ResponseWriter) {
		return http.ErrNotSupported
	}
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(r.ResponseWriter).Flush()
}

// Hijack hands the connection to the caller and stops recording: after a
// successful hijack the response is no longer the server's to describe, and
// nothing may be written through this writer again. The recorded status becomes
// 101 so observers report the upgrade rather than a bare 200.
func (r *Recorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	conn, buf, err := h.Hijack()
	if err == nil {
		r.hijacked = true
		r.status = http.StatusSwitchingProtocols
	}
	return conn, buf, err
}
