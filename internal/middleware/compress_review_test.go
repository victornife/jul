// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCompressionIdentityPanicDoesNotCommitSuccess(t *testing.T) {
	mw, err := NewCompression(CompressionOptions{Encoders: []string{"gzip"}, Types: []string{"text/plain"}})
	if err != nil {
		t.Fatal(err)
	}
	h := Recover(slog.New(slog.NewTextHandler(io.Discard, nil)))(mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("fixture")
	})))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example.test/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic produced status %d, want 500", rec.Code)
	}
}

type compressionFlushErrorWriter struct {
	*httptest.ResponseRecorder
	err error
}

func (w *compressionFlushErrorWriter) FlushError() error { return w.err }

func TestCompressionPropagatesFlushErrors(t *testing.T) {
	for _, encoding := range []string{"identity", "gzip"} {
		t.Run(encoding, func(t *testing.T) {
			mw, err := NewCompression(CompressionOptions{Encoders: []string{"gzip"}, Types: []string{"text/plain"}})
			if err != nil {
				t.Fatal(err)
			}
			want := errors.New("fixture flush failure")
			writer := &compressionFlushErrorWriter{ResponseRecorder: httptest.NewRecorder(), err: want}
			var got error
			h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				got = http.NewResponseController(w).Flush()
			}))
			req := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
			req.Header.Set("Accept-Encoding", encoding)
			h.ServeHTTP(writer, req)
			if !errors.Is(got, want) {
				t.Fatalf("Flush error = %v, want %v", got, want)
			}
		})
	}
}
