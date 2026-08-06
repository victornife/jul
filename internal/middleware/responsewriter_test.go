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
