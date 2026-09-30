// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCompressionWeakensStrongETag(t *testing.T) {
	body := strings.Repeat("validator semantics ", 200)
	tests := []struct {
		name       string
		etag       []string
		accept     string
		rangeValue string
		want       []string
	}{
		{name: "strong becomes weak when compressed", etag: []string{`"abc-1"`}, accept: "gzip", want: []string{`W/"abc-1"`}},
		{name: "weak stays weak", etag: []string{`W/"abc-1"`}, accept: "gzip", want: []string{`W/"abc-1"`}},
		{name: "absent stays absent", accept: "gzip"},
		{name: "identity keeps strong", etag: []string{`"abc-1"`}, want: []string{`"abc-1"`}},
		{name: "range passthrough keeps strong", etag: []string{`"abc-1"`}, accept: "gzip", rangeValue: "bytes=0-9", want: []string{`"abc-1"`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mw := gzipMiddleware(t, CompressionOptions{MinSize: 8})
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.accept != "" {
				req.Header.Set("Accept-Encoding", tc.accept)
			}
			if tc.rangeValue != "" {
				req.Header.Set("Range", tc.rangeValue)
			}
			rec := serveCompress(mw, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				for _, v := range tc.etag {
					w.Header().Add("ETag", v)
				}
				_, _ = io.WriteString(w, body)
			}, req)
			got := rec.Header().Values("ETag")
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("ETag = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestCompressedETagCannotSatisfyIfRange replays the resumed-download sequence
// that corrupted files before #504: a client stores the gzip representation's
// validator, then resumes without gzip using If-Range.
func TestCompressedETagCannotSatisfyIfRange(t *testing.T) {
	body := strings.Repeat("x", 200000)
	modTime := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"18da13f292395883-30d40"`)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		http.ServeContent(w, r, "big.txt", modTime, strings.NewReader(body))
	})
	h := gzipMiddleware(t, CompressionOptions{MinSize: 8})(origin)

	first := httptest.NewRequest(http.MethodGet, "/big.txt", nil)
	first.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, first)
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("first response Content-Encoding = %q, want gzip", got)
	}
	stored := rec.Header().Get("ETag")
	if stored != `W/"18da13f292395883-30d40"` {
		t.Fatalf("compressed ETag = %q, want weak form", stored)
	}

	resume := httptest.NewRequest(http.MethodGet, "/big.txt", nil)
	resume.Header.Set("Range", "bytes=1000-")
	resume.Header.Set("If-Range", stored)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, resume)
	if rec.Code != http.StatusOK {
		t.Fatalf("If-Range with the coded validator: status %d, want 200 full body", rec.Code)
	}
	if rec.Body.Len() != len(body) {
		t.Fatalf("full body length %d, want %d", rec.Body.Len(), len(body))
	}

	for _, accept := range []string{"", "gzip"} {
		revalidate := httptest.NewRequest(http.MethodGet, "/big.txt", nil)
		revalidate.Header.Set("If-None-Match", stored)
		if accept != "" {
			revalidate.Header.Set("Accept-Encoding", accept)
		}
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, revalidate)
		if rec.Code != http.StatusNotModified {
			t.Fatalf("If-None-Match (accept %q): status %d, want 304", accept, rec.Code)
		}
	}
}
