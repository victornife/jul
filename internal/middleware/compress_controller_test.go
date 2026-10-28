// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type compressionBasicWriter struct {
	header  http.Header
	written bool
}

func (w *compressionBasicWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}
func (w *compressionBasicWriter) WriteHeader(int) { w.written = true }
func (w *compressionBasicWriter) Write(p []byte) (int, error) {
	w.written = true
	return len(p), nil
}

type compressionControllerWriter struct {
	*compressionBasicWriter
	err error
}

func (w *compressionControllerWriter) FlushError() error { return w.err }

func TestCompressionFlushErrorOnlyWriterPreservesCapabilities(t *testing.T) {
	mw, err := NewCompression(CompressionOptions{Encoders: []string{"gzip"}})
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("fixture flush failure")
	writer := &compressionControllerWriter{compressionBasicWriter: &compressionBasicWriter{}, err: want}
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, ok := w.(http.Flusher); ok {
			t.Error("invented the Flusher interface")
		}
		if got := http.NewResponseController(w).Flush(); !errors.Is(got, want) {
			t.Errorf("Flush error = %v, want %v", got, want)
		}
		if got := w.Header().Get("Vary"); got != "Accept-Encoding" {
			t.Errorf("Vary = %q", got)
		}
	}))
	h.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "http://example.test/", nil))
}

func TestCompressionUnsupportedFlushDoesNotCommit(t *testing.T) {
	mw, err := NewCompression(CompressionOptions{Encoders: []string{"gzip"}})
	if err != nil {
		t.Fatal(err)
	}
	writer := &compressionBasicWriter{}
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if got := http.NewResponseController(w).Flush(); !errors.Is(got, http.ErrNotSupported) {
			t.Errorf("Flush error = %v", got)
		}
		if writer.written {
			t.Error("unsupported flush committed the response")
		}
	}))
	h.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "http://example.test/", nil))
}
