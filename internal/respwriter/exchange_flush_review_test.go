// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package respwriter

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

type exchangeControllerWriter struct {
	header http.Header
	err    error
}

func (w *exchangeControllerWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}
func (w *exchangeControllerWriter) WriteHeader(int)             {}
func (w *exchangeControllerWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *exchangeControllerWriter) FlushError() error           { return w.err }

func TestExchangeControllerFlushPreservesErrorAndRetirement(t *testing.T) {
	want := errors.New("flush failure")
	writer := &exchangeControllerWriter{err: want}
	reg := NewExchangeRegistry()
	req := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	wrapped, _, ex, release := reg.Admit(writer, req, EntryCoord{GenID: 1})
	defer release()
	if _, ok := wrapped.(http.Flusher); ok {
		t.Fatal("invented http.Flusher")
	}
	if err := http.NewResponseController(wrapped).Flush(); !errors.Is(err, want) {
		t.Fatalf("flush = %v, want %v", err, want)
	}
	if !ex.hasWrittenHeader() {
		t.Fatal("flush did not record implicit status")
	}
	ex.mu.Lock()
	ex.abortNow = true
	ex.mu.Unlock()
	if err := http.NewResponseController(wrapped).Flush(); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("retired flush = %v, want closed pipe", err)
	}
	if !ex.ShouldAbortStream() {
		t.Fatal("retired flush did not mark the stream for abort")
	}
}
