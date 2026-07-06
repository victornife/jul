// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// captureSink records every AccessRecord it receives, for assertions.
type captureSink struct {
	records []AccessRecord
}

func (c *captureSink) Log(rec AccessRecord) { c.records = append(c.records, rec) }

// TestAccessLogFanOut verifies the middleware builds one structured record per
// request and delivers it to every sink in the set.
func TestAccessLogFanOut(t *testing.T) {
	a, b := &captureSink{}, &captureSink{}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sleep so the captured duration is observably non-zero even on
		// platforms with a coarse monotonic clock (Windows rounds sub-tick
		// intervals to 0).
		time.Sleep(2 * time.Millisecond)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("hello"))
	})
	h := AccessLog(a, b)(handler)

	req := httptest.NewRequest(http.MethodPost, "http://example.com/api/x?q=1", strings.NewReader(""))
	req.Header.Set("User-Agent", "test-agent")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if len(a.records) != 1 || len(b.records) != 1 {
		t.Fatalf("fan-out: sink a=%d b=%d records, want 1 each", len(a.records), len(b.records))
	}
	rec := a.records[0]
	if rec.Method != http.MethodPost {
		t.Errorf("Method = %q, want POST", rec.Method)
	}
	if rec.Host != "example.com" {
		t.Errorf("Host = %q, want example.com", rec.Host)
	}
	if rec.Path != "/api/x" {
		t.Errorf("Path = %q, want /api/x", rec.Path)
	}
	if rec.Query != "q=1" {
		t.Errorf("Query = %q, want q=1", rec.Query)
	}
	if rec.Status != http.StatusCreated {
		t.Errorf("Status = %d, want 201", rec.Status)
	}
	if rec.Bytes != 5 {
		t.Errorf("Bytes = %d, want 5", rec.Bytes)
	}
	if rec.Remote != "192.0.2.1" {
		t.Errorf("Remote = %q, want 192.0.2.1", rec.Remote)
	}
	if rec.UserAgent != "test-agent" {
		t.Errorf("UserAgent = %q, want test-agent", rec.UserAgent)
	}
	if rec.Proto != "HTTP/1.1" {
		t.Errorf("Proto = %q, want HTTP/1.1", rec.Proto)
	}
	if rec.Duration <= 0 {
		t.Errorf("Duration = %v, want > 0", rec.Duration)
	}
	if rec.Time.IsZero() {
		t.Error("Time is zero, want request start time")
	}
}

// TestAccessLogCapturesRequestID verifies the record picks up the request id
// placed in context by the RequestID middleware.
func TestAccessLogCapturesRequestID(t *testing.T) {
	sink := &captureSink{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	// RequestID is outer so the id is in context when AccessLog reads it.
	h := RequestID()(AccessLog(sink)(handler))

	req := httptest.NewRequest(http.MethodGet, "http://h/", nil)
	req.Header.Set(HeaderRequestID, "abc123")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if len(sink.records) != 1 {
		t.Fatalf("got %d records, want 1", len(sink.records))
	}
	if got := sink.records[0].RequestID; got != "abc123" {
		t.Fatalf("RequestID = %q, want abc123", got)
	}
}

// TestAccessLogCapturesTraceID verifies the record picks up the trace id placed
// in context (by the tracing middleware) via the WithTraceID seam. This is the
// plain-string bridge that lets the access log correlate with traces without
// the middleware package depending on any tracing library.
func TestAccessLogCapturesTraceID(t *testing.T) {
	sink := &captureSink{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	// Simulate the tracing middleware seeding the trace id outside AccessLog.
	seed := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(WithTraceID(r.Context(), "0123456789abcdef")))
		})
	}
	h := seed(AccessLog(sink)(handler))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://h/", nil))

	if len(sink.records) != 1 {
		t.Fatalf("got %d records, want 1", len(sink.records))
	}
	if got := sink.records[0].TraceID; got != "0123456789abcdef" {
		t.Fatalf("TraceID = %q, want 0123456789abcdef", got)
	}
}

// TestAccessLogNoSinks verifies that with no sinks the middleware is a
// pass-through: the wrapped handler still runs and no record building occurs.
func TestAccessLogNoSinks(t *testing.T) {
	called := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot)
	})
	h := AccessLog()(handler)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://h/", nil))

	if !called {
		t.Fatal("handler not called through no-sink AccessLog")
	}
	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want 418", rec.Code)
	}
}

// TestSlogSinkOutput verifies the default sink reproduces the structured access
// line with the expected key/value pairs.
func TestSlogSinkOutput(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{}))
	sink := NewSlogSink(log)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	AccessLog(sink)(handler).ServeHTTP(
		httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "http://h/path", nil),
	)

	out := buf.String()
	for _, want := range []string{
		`msg=access`,
		`method=GET`,
		`path=/path`,
		`status=200`,
		`bytes=2`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("slog output missing %q\nfull line: %s", want, out)
		}
	}
}

// TestSlogSinkTraceID verifies the default sink emits a trace_id field only when
// the record carries one, so logs stay clean when tracing is disabled.
func TestSlogSinkTraceID(t *testing.T) {
	render := func(rec AccessRecord) string {
		var buf bytes.Buffer
		NewSlogSink(slog.New(slog.NewTextHandler(&buf, nil))).Log(rec)
		return buf.String()
	}

	with := render(AccessRecord{Method: "GET", Status: 200, TraceID: "abc123def456"})
	if !strings.Contains(with, "trace_id=abc123def456") {
		t.Errorf("expected trace_id in output, got: %s", with)
	}

	without := render(AccessRecord{Method: "GET", Status: 200})
	if strings.Contains(without, "trace_id") {
		t.Errorf("trace_id must be omitted when empty, got: %s", without)
	}
}

// TestAccessLogObservesRecoveredPanic locks in the global chain invariant that
// the access log wraps Recover (observers outermost): when an inner handler
// panics, Recover converts it to a 500 and AccessLog must still record the
// request with that status rather than the panic unwinding past it. If a future
// reorder puts Recover outside AccessLog, this test fails.
func TestAccessLogObservesRecoveredPanic(t *testing.T) {
	sink := &captureSink{}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	panicker := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})
	// Same nesting as the global chain: AccessLog (observer) outside Recover.
	h := AccessLog(sink)(Recover(quiet)(panicker))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://h/panic", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("response status = %d, want 500", rec.Code)
	}
	if len(sink.records) != 1 {
		t.Fatalf("got %d access records, want 1 (panic must be observed)", len(sink.records))
	}
	if got := sink.records[0].Status; got != http.StatusInternalServerError {
		t.Fatalf("access record status = %d, want 500", got)
	}
}
