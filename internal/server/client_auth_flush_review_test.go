// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type clientAuthControllerWriter struct {
	header http.Header
	status int
	err    error
}

func (w *clientAuthControllerWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}
func (w *clientAuthControllerWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}
func (w *clientAuthControllerWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return len(p), nil
}
func (w *clientAuthControllerWriter) FlushError() error {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.err
}

func TestBareClientAuthChecksBeforeControllerFlush(t *testing.T) {
	holder := &dynamicClientAuth{}
	holder.current.Store(&clientAuthBundle{mode: tls.RequireAndVerifyClientCert})
	want := errors.New("flush failure")
	writer := &clientAuthControllerWriter{err: want}
	checked := false
	req := httptest.NewRequest(http.MethodGet, "https://example.test/", nil)
	wrapped := wrapBareClientAuthCheck(writer, req, holder, &checked)
	if _, ok := wrapped.(http.Flusher); ok {
		t.Fatal("invented http.Flusher")
	}
	if err := http.NewResponseController(wrapped).Flush(); !errors.Is(err, want) {
		t.Fatalf("flush error = %v, want %v", err, want)
	}
	if writer.status != http.StatusForbidden {
		t.Fatalf("unauthenticated flush committed %d, want 403", writer.status)
	}
}
