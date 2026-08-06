// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"jul/internal/config"
)

// rangeOrigin answers range requests the way a real origin does, so the
// pass-through assertions are about bytes and status rather than a stub.
func rangeOrigin() http.Handler {
	body := "0123456789"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=3600")
		w.Header().Set("ETag", `"v1"`)
		switch r.Header.Get("Range") {
		case "":
			_, _ = w.Write([]byte(body))
		case "bytes=0-3":
			w.Header().Set("Content-Range", "bytes 0-3/10")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte(body[:4]))
		case "bytes=0-3,6-8":
			w.Header().Set("Content-Type", "multipart/byteranges; boundary=X")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("--X multipart --X"))
		case "bytes=999-1000":
			w.Header().Set("Content-Range", "bytes */10")
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		default: // malformed: RFC 9110 §14.2 says ignore it and serve the whole thing
			_, _ = w.Write([]byte(body))
		}
	})
}

func rangeCache(t *testing.T, cfg config.CacheConfig) (*Cache, http.Handler) {
	t.Helper()
	c, _ := conformanceCache(t, cfg)
	return c, c.Handler(rangeOrigin())
}

// TestRangeRequestBypassesLookup is decision D05's first half: a complete stored
// representation must never be substituted for the origin's range answer.
func TestRangeRequestBypassesLookup(t *testing.T) {
	for _, name := range []string{"memory", "disk"} {
		t.Run(name, func(t *testing.T) {
			cfg := memCfg()
			if name == "disk" {
				cfg.MemoryMaxSize = config.Size(512)
				cfg.DiskPath = t.TempDir()
				cfg.DiskMaxSize = config.Size(1 << 20)
			}
			_, h := rangeCache(t, cfg)

			// Warm a complete representation first, so a cache that ignored the
			// Range header would have something wrong to serve.
			wantResult(t, get(t, h, "http://x/f"), stateMiss, "0123456789")
			wantResult(t, get(t, h, "http://x/f"), stateHit, "0123456789")

			rec := get(t, h, "http://x/f", "Range", "bytes=0-3")
			wantResult(t, rec, stateBypass, "0123")
			if rec.Code != http.StatusPartialContent {
				t.Errorf("status = %d, want 206 from the origin", rec.Code)
			}
			if got := rec.Header().Get("Content-Range"); got != "bytes 0-3/10" {
				t.Errorf("Content-Range = %q, want the origin's", got)
			}
			if got := rec.Header().Get("ETag"); got != `"v1"` {
				t.Errorf("ETag = %q, validators must survive the bypass", got)
			}
		})
	}
}

// TestIfRangeBypassesLookup covers the If-Range half of D05, with both an entity
// tag and a date, and with or without an accompanying Range.
func TestIfRangeBypassesLookup(t *testing.T) {
	cases := []struct{ name, ifRange, rng string }{
		{"entity tag with Range", `"v1"`, "bytes=0-3"},
		{"date with Range", time.Now().UTC().Format(http.TimeFormat), "bytes=0-3"},
		{"entity tag alone", `"v1"`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, h := rangeCache(t, memCfg())
			get(t, h, "http://x/f") // warm a full representation

			headers := []string{"If-Range", tc.ifRange}
			if tc.rng != "" {
				headers = append(headers, "Range", tc.rng)
			}
			rec := get(t, h, "http://x/f", headers...)
			if got := rec.Header().Get("X-Cache"); got != stateBypass {
				t.Errorf("X-Cache = %q, want BYPASS", got)
			}
		})
	}
}

// TestRangeResponsesAreNeverStored proves no 206, 416 or partial body enters the
// store, under any key.
func TestRangeResponsesAreNeverStored(t *testing.T) {
	cases := []struct {
		name   string
		rng    string
		status int
	}{
		{"single range 206", "bytes=0-3", http.StatusPartialContent},
		{"multiple ranges", "bytes=0-3,6-8", http.StatusPartialContent},
		{"unsatisfiable 416", "bytes=999-1000", http.StatusRequestedRangeNotSatisfiable},
		{"malformed range served as 200", "bytes=abc", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, h := rangeCache(t, memCfg())

			rec := get(t, h, "http://x/f", "Range", tc.rng)
			if rec.Code != tc.status {
				t.Errorf("status = %d, want %d passed through from the origin", rec.Code, tc.status)
			}
			wantResult(t, rec, stateBypass, "")

			r := httptest.NewRequest(http.MethodGet, "http://x/f", nil)
			if _, _, ok := c.lookup(key(r), r); ok {
				t.Fatal("a range exchange must not publish anything, not even a 200 it happened to receive")
			}
		})
	}
}

// TestRangeBypassDoesNotDisturbAnExistingEntry proves the bypass neither
// replaces nor evicts the complete representation already stored.
func TestRangeBypassDoesNotDisturbAnExistingEntry(t *testing.T) {
	c, h := rangeCache(t, memCfg())
	get(t, h, "http://x/f")

	get(t, h, "http://x/f", "Range", "bytes=0-3")
	get(t, h, "http://x/f", "If-Range", `"v1"`, "Range", "bytes=0-3")

	r := httptest.NewRequest(http.MethodGet, "http://x/f", nil)
	e, _, ok := c.lookup(key(r), r)
	if !ok {
		t.Fatal("the complete representation was evicted by a range request")
	}
	if string(e.Body) != "0123456789" {
		t.Fatalf("stored body = %q, a partial response overwrote the full one", e.Body)
	}
	wantResult(t, get(t, h, "http://x/f"), stateHit, "0123456789")
}

// TestRangeBypassInteractsCorrectlyWithContentEncoding proves the origin's
// encoding and its byte offsets pass through untouched — the reason cached
// byte-range serving is deferred rather than approximated.
func TestRangeBypassInteractsCorrectlyWithContentEncoding(t *testing.T) {
	c, _ := conformanceCache(t, memCfg())
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=3600")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Vary", "Accept-Encoding")
		if r.Header.Get("Range") == "" {
			_, _ = w.Write([]byte("GZIPPEDBYTES"))
			return
		}
		w.Header().Set("Content-Range", "bytes 0-3/12")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("GZIP"))
	}))

	get(t, h, "http://x/f", "Accept-Encoding", "gzip")
	rec := get(t, h, "http://x/f", "Accept-Encoding", "gzip", "Range", "bytes=0-3")

	wantResult(t, rec, stateBypass, "GZIP")
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Errorf("Content-Encoding = %q, want gzip preserved", got)
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes 0-3/12" {
		t.Errorf("Content-Range = %q, ranges must be the origin's encoded offsets", got)
	}
}

// TestHeadRangeRequestBypasses proves the rule is about the request, not the
// method.
func TestHeadRangeRequestBypasses(t *testing.T) {
	_, h := rangeCache(t, memCfg())
	r := httptest.NewRequest(http.MethodHead, "http://x/f", nil)
	r.Header.Set("Range", "bytes=0-3")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if got := rec.Header().Get("X-Cache"); got != stateBypass {
		t.Errorf("X-Cache = %q, want BYPASS for a HEAD carrying Range", got)
	}
}

// TestNonRangeRequestsAreUnaffected is the regression guard: the bypass must key
// off the range headers only.
func TestNonRangeRequestsAreUnaffected(t *testing.T) {
	_, h := rangeCache(t, memCfg())
	wantResult(t, get(t, h, "http://x/f"), stateMiss, "0123456789")
	wantResult(t, get(t, h, "http://x/f"), stateHit, "0123456789")
	wantResult(t, get(t, h, "http://x/f", "Accept-Ranges", "bytes"), stateHit, "0123456789")
}
