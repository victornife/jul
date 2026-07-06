// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build zstd

package middleware

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestZstdRoundTrip(t *testing.T) {
	if !EncoderAvailable("zstd") {
		t.Fatal("zstd must be available in a zstd-tagged build")
	}
	mw, err := NewCompression(CompressionOptions{Encoders: []string{"zstd"}, MinSize: 8, Types: []string{"text/*"}})
	if err != nil {
		t.Fatalf("NewCompression: %v", err)
	}
	body := strings.Repeat("zstd payload ", 100)
	for i := 0; i < 2; i++ { // second pass exercises Reset() after Close()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Accept-Encoding", "zstd")
		rec := httptest.NewRecorder()
		mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, body)
		})).ServeHTTP(rec, req)

		if rec.Header().Get("Content-Encoding") != "zstd" {
			t.Fatalf("Content-Encoding = %q, want zstd", rec.Header().Get("Content-Encoding"))
		}
		dec, err := zstd.NewReader(bytes.NewReader(rec.Body.Bytes()))
		if err != nil {
			t.Fatalf("zstd reader: %v", err)
		}
		got, err := io.ReadAll(dec)
		dec.Close()
		if err != nil {
			t.Fatalf("zstd decode: %v", err)
		}
		if string(got) != body {
			t.Fatal("zstd round-trip mismatch")
		}
	}
}
