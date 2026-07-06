// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWrapResponseWriterCapturesStatus(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	wrapped := WrapResponseWriter(rec)
	handler.ServeHTTP(wrapped, req)

	if wrapped.Status() != http.StatusCreated {
		t.Fatalf("status = %d, want 201", wrapped.Status())
	}
}

func TestRecorderDefaultsTo200(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := newRecorder(rec)

	if rw.Status() != http.StatusOK {
		t.Fatalf("default status = %d, want 200", rw.Status())
	}
}

func TestRecorderWriteTriggers200(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := newRecorder(rec)

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
	rw := newRecorder(rec)

	n, _ := rw.Write([]byte("hello world"))
	if rw.bytes != int64(n) {
		t.Fatalf("bytes = %d, want %d", rw.bytes, n)
	}
}

func TestRecorderDoubleWriteHeaderIgnored(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := newRecorder(rec)

	rw.WriteHeader(http.StatusNotFound)
	rw.WriteHeader(http.StatusOK) // should be ignored

	if rw.Status() != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rw.Status())
	}
}

func TestWrapDoesNotDoubleWrap(t *testing.T) {
	rec := httptest.NewRecorder()
	wrapped1 := WrapResponseWriter(rec)
	wrapped2 := WrapResponseWriter(wrapped1)

	if wrapped1 != wrapped2 {
		t.Fatal("double-wrapped: expected same wrapper returned")
	}
}

func TestRecorderFlushDelegates(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := newRecorder(rec)

	// httptest.ResponseRecorder implements Flusher
	rw.Flush()
	// Should not panic
}

func TestRecorderHijackNotSupported(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := newRecorder(rec)

	_, _, err := rw.Hijack()
	if err == nil {
		t.Fatal("expected error hijacking httptest.ResponseRecorder")
	}
}

func TestRecorderHijackDelegates(t *testing.T) {
	hijackable := &hijackableRecorder{ResponseRecorder: httptest.NewRecorder()}
	rw := newRecorder(hijackable)

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
