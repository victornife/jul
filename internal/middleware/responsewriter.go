// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"bufio"
	"net"
	"net/http"

	"jul/internal/respwriter"
)

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
	return respwriter.Wrap(r, r.ResponseWriter)
}

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
	if r.hijacked {
		return
	}
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
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
