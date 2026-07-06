// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"jul/internal/config"
)

// BenchmarkStaticServe measures serving a small static file end to end through
// the real static handler, including path cleaning, os.Root open, MIME and
// ETag computation, and http.ServeContent.
func BenchmarkStaticServe(b *testing.B) {
	dir := b.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"),
		[]byte("<!doctype html><title>bench</title><h1>hello</h1>"), 0o644); err != nil {
		b.Fatalf("write file: %v", err)
	}
	h, err := NewStatic(config.ServerConfig{}, config.LocationConfig{
		Root:  dir,
		Index: []string{"index.html"},
	})
	if err != nil {
		b.Fatalf("NewStatic: %v", err)
	}
	// Release the os.Root handle before TempDir cleanup runs (Windows cannot
	// remove a directory while the handle is open).
	if c, ok := h.(io.Closer); ok {
		b.Cleanup(func() { _ = c.Close() })
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", "/index.html", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			b.Fatalf("status = %d, want 200", rec.Code)
		}
	}
}
