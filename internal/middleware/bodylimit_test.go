// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestBodyLimitRejectsByContentLength verifies that a request declaring a
// Content-Length over the limit is refused with 413 before its body is read.
func TestBodyLimitRejectsByContentLength(t *testing.T) {
	reached := false
	h := BodyLimit(8)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		_, _ = io.Copy(io.Discard, r.Body)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("0123456789"))
	req.ContentLength = 10
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if reached {
		t.Error("handler ran despite an oversized Content-Length")
	}
}

// TestBodyLimitAllowsWithinLimit verifies a request inside the limit passes
// through unchanged.
func TestBodyLimitAllowsWithinLimit(t *testing.T) {
	h := BodyLimit(16)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_, _ = w.Write(b)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("hello"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "hello" {
		t.Errorf("body = %q, want hello", rec.Body.String())
	}
}

// TestBodyLimitUnknownLengthStreamsToLimit verifies that an unknown
// Content-Length (-1, e.g. chunked) falls through to MaxBytesReader, which trips
// a read error once the streamed body exceeds the limit.
func TestBodyLimitUnknownLengthStreamsToLimit(t *testing.T) {
	var readErr error
	h := BodyLimit(4)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("toolong"))
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if readErr == nil {
		t.Error("expected a read error from MaxBytesReader on an oversized stream")
	}
}
