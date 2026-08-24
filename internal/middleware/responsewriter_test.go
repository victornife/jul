// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"bufio"
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRecorderCapturesStatus(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	rw := NewRecorder(rec)
	handler.ServeHTTP(rw.Writer(), req)

	if rw.Status() != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rw.Status())
	}
}

func TestRecorderDefaultsTo200(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := NewRecorder(rec)

	if rw.Status() != http.StatusOK {
		t.Fatalf("default status = %d, want 200", rw.Status())
	}
}

func TestRecorderWriteTriggers200(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := NewRecorder(rec)

	_, _ = rw.Write([]byte("hello"))
	if rw.Status() != http.StatusOK {
		t.Fatalf("status after write = %d, want 200", rw.Status())
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("underlying status = %d, want 200", rec.Code)
	}
}

func TestRecorderCapturesBytes(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := NewRecorder(rec)

	n, _ := rw.Write([]byte("hello world"))
	if rw.Bytes() != int64(n) {
		t.Fatalf("bytes = %d, want %d", rw.Bytes(), n)
	}
}

func TestRecorderDoubleWriteHeaderIgnored(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := NewRecorder(rec)

	rw.WriteHeader(http.StatusNotFound)
	rw.WriteHeader(http.StatusOK) // should be ignored

	if rw.Status() != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rw.Status())
	}
}

// TestNestedRecordersAgree replaces the former "do not double wrap" identity
// check. Recorders now nest (each observer keeps its own), so the property that
// matters is that every layer reports the same status and byte count.
func TestNestedRecordersAgree(t *testing.T) {
	rec := httptest.NewRecorder()
	outer := NewRecorder(rec)
	inner := NewRecorder(outer.Writer())

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("hello"))
	})
	handler.ServeHTTP(inner.Writer(), httptest.NewRequest(http.MethodGet, "/", nil))

	if inner.Status() != http.StatusAccepted || outer.Status() != http.StatusAccepted {
		t.Fatalf("status inner=%d outer=%d, want 202", inner.Status(), outer.Status())
	}
	if inner.Bytes() != 5 || outer.Bytes() != 5 {
		t.Fatalf("bytes inner=%d outer=%d, want 5", inner.Bytes(), outer.Bytes())
	}
}

func TestRecorderFlushDelegates(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := NewRecorder(rec)

	// httptest.ResponseRecorder implements Flusher
	rw.Flush()
	// Should not panic
}

func TestRecorderHijackNotSupported(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := NewRecorder(rec)

	_, _, err := rw.Hijack()
	if err == nil {
		t.Fatal("expected error hijacking httptest.ResponseRecorder")
	}
}

func TestRecorderHijackDelegates(t *testing.T) {
	hijackable := &hijackableRecorder{ResponseRecorder: httptest.NewRecorder()}
	rw := NewRecorder(hijackable)

	conn, brw, err := rw.Hijack()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn != nil || brw != nil {
		t.Fatal("expected nil conn and bufio from our stub")
	}
}

type hijackableRecorder struct {
	*httptest.ResponseRecorder
}

func (h *hijackableRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, nil
}

// clientSink is the innermost http.ResponseWriter in a wrapper-chain test. It
// mimics what a real connection does with WriteHeader (net/http's own
// response.WriteHeader forwards any number of 1xx calls and only latches on
// the first non-1xx one): every call is recorded, unlike httptest.ResponseRecorder,
// which silently drops every call but its own first — the same class of bug
// this file is testing for, so it cannot stand in for "what the client saw".
type clientSink struct {
	header   http.Header
	statuses []int
	body     bytes.Buffer
}

func newClientSink() *clientSink { return &clientSink{header: make(http.Header)} }

func (s *clientSink) Header() http.Header { return s.header }

func (s *clientSink) WriteHeader(code int) { s.statuses = append(s.statuses, code) }

func (s *clientSink) Write(b []byte) (int, error) {
	if len(s.statuses) == 0 {
		s.WriteHeader(http.StatusOK)
	}
	return s.body.Write(b)
}

// finalStatus is the status a real HTTP client would see: whatever was last
// written, since anything earlier was necessarily a 1xx interim response.
func (s *clientSink) finalStatus() int {
	if len(s.statuses) == 0 {
		return http.StatusOK
	}
	return s.statuses[len(s.statuses)-1]
}

// TestRecorderInterimResponsesDoNotLatchTheStatus is the regression test for
// #331: a 103 Early Hints ahead of the real status must not make WriteHeader
// treat the response as already finalized.
func TestRecorderInterimResponsesDoNotLatchTheStatus(t *testing.T) {
	sink := newClientSink()
	rw := NewRecorder(sink)
	w := rw.Writer()

	w.Header().Set("Link", "</style.css>; rel=preload")
	w.WriteHeader(http.StatusEarlyHints)
	w.WriteHeader(http.StatusEarlyHints) // multiple interim responses are permitted
	w.WriteHeader(http.StatusNoContent)

	if rw.Status() != http.StatusNoContent {
		t.Fatalf("recorded status = %d, want 204", rw.Status())
	}
	want := []int{http.StatusEarlyHints, http.StatusEarlyHints, http.StatusNoContent}
	if !slicesEqual(sink.statuses, want) {
		t.Fatalf("statuses forwarded to the client = %v, want %v", sink.statuses, want)
	}
	if got := sink.finalStatus(); got != http.StatusNoContent {
		t.Fatalf("status delivered to the client = %d, want 204", got)
	}
}

// TestRecorderInterimThenImplicitOK proves the 1xx-then-Write path: no explicit
// final WriteHeader call still latches the implicit 200, not the interim status.
func TestRecorderInterimThenImplicitOK(t *testing.T) {
	sink := newClientSink()
	rw := NewRecorder(sink)
	w := rw.Writer()

	w.WriteHeader(http.StatusEarlyHints)
	_, _ = w.Write([]byte("body"))

	if rw.Status() != http.StatusOK {
		t.Fatalf("recorded status = %d, want 200", rw.Status())
	}
	if got := sink.finalStatus(); got != http.StatusOK {
		t.Fatalf("status delivered to the client = %d, want 200", got)
	}
}

// TestRecorder101StillLatchesImmediately proves 101 keeps its existing
// treatment: it is a protocol switch, not an interim response, so it finalizes
// the status on the spot and a later WriteHeader is ignored.
func TestRecorder101StillLatchesImmediately(t *testing.T) {
	sink := newClientSink()
	rw := NewRecorder(sink)
	w := rw.Writer()

	w.WriteHeader(http.StatusSwitchingProtocols)
	w.WriteHeader(http.StatusOK) // must be ignored

	if rw.Status() != http.StatusSwitchingProtocols {
		t.Fatalf("recorded status = %d, want 101", rw.Status())
	}
	if len(sink.statuses) != 1 || sink.statuses[0] != http.StatusSwitchingProtocols {
		t.Fatalf("statuses forwarded to the client = %v, want [101]", sink.statuses)
	}
}

func slicesEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
