// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestCompressionRespectsCacheControlNoTransform(t *testing.T) {
	body := strings.Repeat("representation must remain unchanged ", 64)

	tests := []struct {
		name            string
		requestControl  string
		responseControl []string
		explicitHeader  bool
	}{
		{name: "request directive", requestControl: "max-age=0, no-transform"},
		{name: "response directive", responseControl: []string{"public, no-transform"}, explicitHeader: true},
		{name: "case insensitive multiple values", responseControl: []string{"public", "NO-TRANSFORM, max-age=60"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compressed := 0
			mw := gzipMiddleware(t, CompressionOptions{
				MinSize: 1,
				OnCompress: func(string) {
					compressed++
				},
			})

			req := httptest.NewRequest(http.MethodGet, "/asset", nil)
			req.Header.Set("Accept-Encoding", "gzip")
			if tt.requestControl != "" {
				req.Header.Set("Cache-Control", tt.requestControl)
			}

			rec := serveCompress(mw, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				w.Header().Set("Content-Length", strconv.Itoa(len(body)))
				for _, value := range tt.responseControl {
					w.Header().Add("Cache-Control", value)
				}
				if tt.explicitHeader {
					w.WriteHeader(http.StatusOK)
				}
				_, _ = io.WriteString(w, body)
			}, req)

			if got := rec.Header().Get("Content-Encoding"); got != "" {
				t.Fatalf("Content-Encoding = %q, want no transformation", got)
			}
			if got := rec.Body.String(); got != body {
				t.Fatalf("body changed: got %d bytes, want %d", len(got), len(body))
			}
			if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(body)) {
				t.Fatalf("Content-Length = %q, want %d", got, len(body))
			}
			if compressed != 0 {
				t.Fatalf("OnCompress called %d times, want 0", compressed)
			}
			if !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
				t.Fatalf("Vary must include Accept-Encoding, got %q", rec.Header().Get("Vary"))
			}
		})
	}
}

func TestCacheControlNoTransformParsing(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		want   bool
	}{
		{name: "exact", values: []string{"no-transform"}, want: true},
		{name: "mixed directives", values: []string{"public, max-age=60, no-transform"}, want: true},
		{name: "case insensitive", values: []string{"No-Transform"}, want: true},
		{name: "argument remains conservative", values: []string{"no-transform=unexpected"}, want: true},
		{name: "multiple header values", values: []string{"public", "max-age=60", "no-transform"}, want: true},
		{name: "quoted extension does not match", values: []string{`example="public, no-transform"`}, want: false},
		{name: "escaped quote stays inside extension", values: []string{`example="a\\\", no-transform", public`}, want: false},
		{name: "similar token does not match", values: []string{"x-no-transform"}, want: false},
		{name: "empty directives", values: []string{" , , public, "}, want: false},
		{name: "absent", values: []string{"public, max-age=60"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := make(http.Header)
			for _, value := range tt.values {
				h.Add("Cache-Control", value)
			}
			if got := cacheControlNoTransform(h); got != tt.want {
				t.Fatalf("cacheControlNoTransform(%q) = %v, want %v", tt.values, got, tt.want)
			}
		})
	}
}
