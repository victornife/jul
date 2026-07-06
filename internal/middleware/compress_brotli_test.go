// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build brotli

package middleware

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
)

func TestBrotliRoundTrip(t *testing.T) {
	if !EncoderAvailable("br") {
		t.Fatal("br must be available in a brotli-tagged build")
	}
	mw, err := NewCompression(CompressionOptions{Encoders: []string{"br"}, MinSize: 8, Types: []string{"text/*"}})
	if err != nil {
		t.Fatalf("NewCompression: %v", err)
	}
	body := strings.Repeat("brotli payload ", 100)
	for i := 0; i < 2; i++ { // second pass exercises Reset() after Close()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Accept-Encoding", "br")
		rec := httptest.NewRecorder()
		mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, body)
		})).ServeHTTP(rec, req)

		if rec.Header().Get("Content-Encoding") != "br" {
			t.Fatalf("Content-Encoding = %q, want br", rec.Header().Get("Content-Encoding"))
		}
		got, err := io.ReadAll(brotli.NewReader(bytes.NewReader(rec.Body.Bytes())))
		if err != nil {
			t.Fatalf("brotli decode: %v", err)
		}
		if string(got) != body {
			t.Fatal("brotli round-trip mismatch")
		}
	}
}
