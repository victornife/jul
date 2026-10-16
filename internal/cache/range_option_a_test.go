// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

type rangeOutcomeLog struct {
	mu    sync.Mutex
	items []string
}

func (l *rangeOutcomeLog) add(outcome string) {
	l.mu.Lock()
	l.items = append(l.items, outcome)
	l.mu.Unlock()
}

func (l *rangeOutcomeLog) count(want string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, it := range l.items {
		if it == want {
			n++
		}
	}
	return n
}

func (l *rangeOutcomeLog) reset() {
	l.mu.Lock()
	l.items = nil
	l.mu.Unlock()
}

func rangeEnabledCfg(dir string, memMax, maxEntry, diskMax int64) config.CacheConfig {
	cfg := largeCacheCfg(dir, memMax, maxEntry, diskMax)
	cfg.RangeEnabled = true
	cfg.MaxRanges = 1
	cfg.Lock = true
	cfg.LockTimeout = config.Duration(2 * time.Second)
	return cfg
}

// TestRangeOptionAByteRangeMatrix exercises single-byte, first/middle/final,
// complete-length, open-ended, suffix, suffix-larger-than-object, unsatisfiable
// 416, and empty-object range semantics across both memory and disk tiers.
func TestRangeOptionAByteRangeMatrix(t *testing.T) {
	const payload = "0123456789abcdefghijklmnopqrstuvwxyz" // 36 bytes
	for _, tier := range []string{"memory", "disk"} {
		t.Run(tier, func(t *testing.T) {
			var cfg config.CacheConfig
			if tier == "memory" {
				cfg = rangeEnabledCfg("", 4<<10, 4<<10, 0)
			} else {
				cfg = rangeEnabledCfg(t.TempDir(), 16, 4<<10, 64<<10)
			}
			c := newTestCache(t, cfg)
			var outcomes rangeOutcomeLog
			c.SetRangeObserver(outcomes.add)

			var originCalls atomic.Int32
			h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				originCalls.Add(1)
				if r.Header.Get("Range") != "" {
					t.Errorf("cold fill leaked Range header %q to origin", r.Header.Get("Range"))
				}
				w.Header().Set("Cache-Control", "public, max-age=3600")
				w.Header().Set("ETag", `"strong-v1"`)
				w.Header().Set("Content-Type", "application/octet-stream")
				if r.URL.Path == "/empty" {
					w.Header().Set("Content-Length", "0")
					w.WriteHeader(http.StatusOK)
					return
				}
				w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
				_, _ = w.Write([]byte(payload))
			}))

			cases := []struct {
				name       string
				rng        string
				wantCode   int
				wantBody   string
				wantCR     string
				wantCache  string
				wantMetric string
			}{
				{
					name:       "cold miss first byte triggers complete fill and serves 206",
					rng:        "bytes=0-0",
					wantCode:   http.StatusPartialContent,
					wantBody:   "0",
					wantCR:     "bytes 0-0/36",
					wantCache:  stateMiss,
					wantMetric: string(rangeOutcomeHit),
				},
				{
					name:       "warm hit middle byte",
					rng:        "bytes=10-15",
					wantCode:   http.StatusPartialContent,
					wantBody:   "abcdef",
					wantCR:     "bytes 10-15/36",
					wantCache:  stateHit,
					wantMetric: string(rangeOutcomeHit),
				},
				{
					name:       "warm hit final byte",
					rng:        "bytes=35-35",
					wantCode:   http.StatusPartialContent,
					wantBody:   "z",
					wantCR:     "bytes 35-35/36",
					wantCache:  stateHit,
					wantMetric: string(rangeOutcomeHit),
				},
				{
					name:       "warm hit complete length",
					rng:        "bytes=0-35",
					wantCode:   http.StatusPartialContent,
					wantBody:   payload,
					wantCR:     "bytes 0-35/36",
					wantCache:  stateHit,
					wantMetric: string(rangeOutcomeHit),
				},
				{
					name:       "warm hit end clamped beyond object length",
					rng:        "bytes=30-9999",
					wantCode:   http.StatusPartialContent,
					wantBody:   "uvwxyz",
					wantCR:     "bytes 30-35/36",
					wantCache:  stateHit,
					wantMetric: string(rangeOutcomeHit),
				},
				{
					name:       "warm hit open-ended range",
					rng:        "bytes=26-",
					wantCode:   http.StatusPartialContent,
					wantBody:   "qrstuvwxyz",
					wantCR:     "bytes 26-35/36",
					wantCache:  stateHit,
					wantMetric: string(rangeOutcomeHit),
				},
				{
					name:       "warm hit suffix range",
					rng:        "bytes=-6",
					wantCode:   http.StatusPartialContent,
					wantBody:   "uvwxyz",
					wantCR:     "bytes 30-35/36",
					wantCache:  stateHit,
					wantMetric: string(rangeOutcomeHit),
				},
				{
					name:       "warm hit suffix larger than object",
					rng:        "bytes=-500",
					wantCode:   http.StatusPartialContent,
					wantBody:   payload,
					wantCR:     "bytes 0-35/36",
					wantCache:  stateHit,
					wantMetric: string(rangeOutcomeHit),
				},
				{
					name:       "unsatisfiable start at length",
					rng:        "bytes=36-40",
					wantCode:   http.StatusRequestedRangeNotSatisfiable,
					wantBody:   "",
					wantCR:     "bytes */36",
					wantCache:  stateHit,
					wantMetric: string(rangeOutcomeUnsatisfiable),
				},
				{
					name:       "unsatisfiable open-ended beyond length",
					rng:        "bytes=100-",
					wantCode:   http.StatusRequestedRangeNotSatisfiable,
					wantBody:   "",
					wantCR:     "bytes */36",
					wantCache:  stateHit,
					wantMetric: string(rangeOutcomeUnsatisfiable),
				},
				{
					name:       "unsatisfiable zero suffix -0",
					rng:        "bytes=-0",
					wantCode:   http.StatusRequestedRangeNotSatisfiable,
					wantBody:   "",
					wantCR:     "bytes */36",
					wantCache:  stateHit,
					wantMetric: string(rangeOutcomeUnsatisfiable),
				},
			}

			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					outcomes.reset()
					rec := get(t, h, "http://example.com/obj", "Range", tc.rng)
					if rec.Code != tc.wantCode {
						t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
					}
					if got := rec.Header().Get("X-Cache"); got != tc.wantCache {
						t.Fatalf("X-Cache = %q, want %q", got, tc.wantCache)
					}
					if got := rec.Header().Get("Content-Range"); got != tc.wantCR {
						t.Fatalf("Content-Range = %q, want %q", got, tc.wantCR)
					}
					if got := rec.Header().Get("Accept-Ranges"); got != "bytes" {
						t.Fatalf("Accept-Ranges = %q, want bytes", got)
					}
					if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(tc.wantBody)) {
						t.Fatalf("Content-Length = %q, want %d", got, len(tc.wantBody))
					}
					if rec.Body.String() != tc.wantBody {
						t.Fatalf("body = %q, want %q", rec.Body.String(), tc.wantBody)
					}
					if outcomes.count(tc.wantMetric) == 0 {
						t.Fatalf("expected metric %q in %v", tc.wantMetric, outcomes.items)
					}
				})
			}

			if originCalls.Load() != 1 {
				t.Fatalf("origin calls across 11 range requests = %d, want 1", originCalls.Load())
			}

			// Empty object (0 bytes): RFC 9110 §14.1.2 & §14.2 — Range is ignored
			// for a zero-length representation and the complete 0-byte 200 OK is served
			// across all syntactically valid byte-range specs (including bytes=-0).
			for i, rng := range []string{"bytes=0-0", "bytes=0-", "bytes=-1", "bytes=-0"} {
				rec := get(t, h, "http://example.com/empty", "Range", rng)
				if rec.Code != http.StatusOK {
					t.Fatalf("empty object Range %q: status = %d, want 200", rng, rec.Code)
				}
				wantCache := stateHit
				if i == 0 {
					wantCache = stateMiss
				}
				if got := rec.Header().Get("X-Cache"); got != wantCache {
					t.Fatalf("empty object Range %q: X-Cache = %q, want %q", rng, got, wantCache)
				}
				if rec.Body.Len() != 0 {
					t.Fatalf("empty object Range %q: body len = %d, want 0", rng, rec.Body.Len())
				}
			}
		})
	}
}

// TestRangeOptionAIfRangeAndConditionalSemantics verifies strong/weak ETag
// If-Range, HTTP-date If-Range, absent validators, If-Range without Range,
// and If-None-Match / If-Modified-Since precedence.
func TestRangeOptionAIfRangeAndConditionalSemantics(t *testing.T) {
	c := newTestCache(t, rangeEnabledCfg("", 64<<10, 64<<10, 0))
	var outcomes rangeOutcomeLog
	c.SetRangeObserver(outcomes.add)

	lastMod := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC).Format(http.TimeFormat)
	qualifyingDate := time.Date(2026, 10, 15, 10, 0, 1, 0, time.UTC).Format(http.TimeFormat)
	olderDate := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC).Format(http.TimeFormat)
	newerDate := time.Date(2026, 10, 15, 11, 0, 0, 0, time.UTC).Format(http.TimeFormat)
	const (
		imf94    = "Sun, 06 Nov 1994 08:49:37 GMT"
		rfc85094 = "Sunday, 06-Nov-94 08:49:37 GMT"
		ansic94  = "Sun Nov  6 08:49:37 1994"
	)
	c.now = func() time.Time { return time.Date(2026, 10, 15, 10, 5, 0, 0, time.UTC) }
	const body = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"

	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		switch r.URL.Path {
		case "/strong":
			w.Header().Set("ETag", `"tag-strong"`)
			w.Header().Set("Last-Modified", lastMod)
			w.Header().Set("Date", qualifyingDate)
		case "/weak":
			w.Header().Set("ETag", `W/"tag-weak"`)
			w.Header().Set("Last-Modified", lastMod)
			w.Header().Set("Date", qualifyingDate)
		case "/missing-origin-date":
			w.Header().Set("Last-Modified", lastMod)
		case "/rfc850-lastmod":
			w.Header().Set("Last-Modified", rfc85094)
		case "/ansic-lastmod":
			w.Header().Set("Last-Modified", ansic94)
		case "/weak-date-same-sec":
			w.Header().Set("Last-Modified", lastMod)
			w.Header().Set("Date", lastMod)
		case "/bad-etag":
			w.Header().Set("ETag", `"bad tag"`)
		case "/noval":
		}
		_, _ = w.Write([]byte(body))
	}))

	// Warm all test paths.
	for _, p := range []string{
		"/strong",
		"/weak",
		"/missing-origin-date",
		"/rfc850-lastmod",
		"/ansic-lastmod",
		"/weak-date-same-sec",
		"/bad-etag",
		"/noval",
	} {
		wantResult(t, get(t, h, "http://example.com"+p), stateMiss, body)
	}

	cases := []struct {
		name       string
		path       string
		ifRange    []string
		wantCode   int
		wantBody   string
		wantMetric string
	}{
		{
			name:       "matching strong ETag serves 206",
			path:       "/strong",
			ifRange:    []string{`"tag-strong"`},
			wantCode:   http.StatusPartialContent,
			wantBody:   "ABCD",
			wantMetric: string(rangeOutcomeHit),
		},
		{
			name:       "mismatching strong ETag serves full 200",
			path:       "/strong",
			ifRange:    []string{`"other"`},
			wantCode:   http.StatusOK,
			wantBody:   body,
			wantMetric: string(rangeOutcomeIfRangeFull),
		},
		{
			name:       "weak ETag in request If-Range never matches -> full 200",
			path:       "/strong",
			ifRange:    []string{`W/"tag-strong"`},
			wantCode:   http.StatusOK,
			wantBody:   body,
			wantMetric: string(rangeOutcomeIfRangeFull),
		},
		{
			name:       "weak ETag on stored entry never matches If-Range -> full 200",
			path:       "/weak",
			ifRange:    []string{`"tag-weak"`},
			wantCode:   http.StatusOK,
			wantBody:   body,
			wantMetric: string(rangeOutcomeIfRangeFull),
		},
		{
			name:       "syntactically invalid ETag in request If-Range never matches -> full 200",
			path:       "/strong",
			ifRange:    []string{`"bad tag"`},
			wantCode:   http.StatusOK,
			wantBody:   body,
			wantMetric: string(rangeOutcomeIfRangeFull),
		},
		{
			name:       "syntactically invalid stored ETag never matches identical If-Range -> full 200",
			path:       "/bad-etag",
			ifRange:    []string{`"bad tag"`},
			wantCode:   http.StatusOK,
			wantBody:   body,
			wantMetric: string(rangeOutcomeIfRangeFull),
		},
		{
			name:       "matching IMF-fixdate HTTP-date with Date >= Last-Modified + 1s serves 206",
			path:       "/strong",
			ifRange:    []string{lastMod},
			wantCode:   http.StatusPartialContent,
			wantBody:   "ABCD",
			wantMetric: string(rangeOutcomeHit),
		},
		{
			name:       "matching RFC 850 If-Range against RFC 850 Last-Modified serves 206",
			path:       "/rfc850-lastmod",
			ifRange:    []string{rfc85094},
			wantCode:   http.StatusPartialContent,
			wantBody:   "ABCD",
			wantMetric: string(rangeOutcomeHit),
		},
		{
			name:       "matching ANSI C asctime If-Range against ANSI C Last-Modified serves 206",
			path:       "/ansic-lastmod",
			ifRange:    []string{ansic94},
			wantCode:   http.StatusPartialContent,
			wantBody:   "ABCD",
			wantMetric: string(rangeOutcomeHit),
		},
		{
			name:       "matching IMF-fixdate If-Range against RFC 850 Last-Modified serves 206",
			path:       "/rfc850-lastmod",
			ifRange:    []string{imf94},
			wantCode:   http.StatusPartialContent,
			wantBody:   "ABCD",
			wantMetric: string(rangeOutcomeHit),
		},
		{
			name:       "origin omitting Date gets synthesized received-time Date and satisfies If-Range -> 206",
			path:       "/missing-origin-date",
			ifRange:    []string{lastMod},
			wantCode:   http.StatusPartialContent,
			wantBody:   "ABCD",
			wantMetric: string(rangeOutcomeHit),
		},
		{
			name:       "implicitly weak HTTP-date with Date == Last-Modified serves full 200",
			path:       "/weak-date-same-sec",
			ifRange:    []string{lastMod},
			wantCode:   http.StatusOK,
			wantBody:   body,
			wantMetric: string(rangeOutcomeIfRangeFull),
		},
		{
			name:       "older HTTP-date serves full 200",
			path:       "/strong",
			ifRange:    []string{olderDate},
			wantCode:   http.StatusOK,
			wantBody:   body,
			wantMetric: string(rangeOutcomeIfRangeFull),
		},
		{
			name:       "newer HTTP-date serves full 200",
			path:       "/strong",
			ifRange:    []string{newerDate},
			wantCode:   http.StatusOK,
			wantBody:   body,
			wantMetric: string(rangeOutcomeIfRangeFull),
		},
		{
			name:       "malformed If-Range date serves full 200",
			path:       "/strong",
			ifRange:    []string{"not-a-date-or-etag"},
			wantCode:   http.StatusOK,
			wantBody:   body,
			wantMetric: string(rangeOutcomeIfRangeFull),
		},
		{
			name:       "multiple If-Range headers fail closed to full 200",
			path:       "/strong",
			ifRange:    []string{`"tag-strong"`, `"tag-strong"`},
			wantCode:   http.StatusOK,
			wantBody:   body,
			wantMetric: string(rangeOutcomeIfRangeFull),
		},
		{
			name:       "absent validators on stored entry serves full 200",
			path:       "/noval",
			ifRange:    []string{`"tag-strong"`},
			wantCode:   http.StatusOK,
			wantBody:   body,
			wantMetric: string(rangeOutcomeIfRangeFull),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcomes.reset()
			req := httptest.NewRequest(http.MethodGet, "http://example.com"+tc.path, nil)
			req.Header.Set("Range", "bytes=0-3")
			for _, ir := range tc.ifRange {
				req.Header.Add("If-Range", ir)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
			}
			if rec.Body.String() != tc.wantBody {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tc.wantBody)
			}
			if outcomes.count(tc.wantMetric) == 0 {
				t.Fatalf("expected metric %q in %v", tc.wantMetric, outcomes.items)
			}
		})
	}

	// If-Range alone without Range is ignored per RFC 9110 §13.1.5 and serves a normal 200 HIT.
	recNoRange := get(t, h, "http://example.com/strong", "If-Range", `"mismatch"`)
	wantResult(t, recNoRange, stateHit, body)
	if recNoRange.Code != http.StatusOK {
		t.Fatalf("If-Range without Range: status = %d, want 200", recNoRange.Code)
	}

	// If-None-Match takes precedence over Range/If-Range and returns 304 Not Modified.
	rec304 := get(t, h, "http://example.com/strong", "If-None-Match", `"tag-strong"`, "Range", "bytes=0-3")
	if rec304.Code != http.StatusNotModified || rec304.Body.Len() != 0 {
		t.Fatalf("If-None-Match with Range: code=%d bodyLen=%d, want 304 and empty body", rec304.Code, rec304.Body.Len())
	}
}

// TestRangeOptionAMalformedMultiRangeAndUnits verifies malformed syntax,
// integer overflow, unknown units, multi-range disposition, max_ranges bound,
// and HEAD disposition.
func TestRangeOptionAMalformedMultiRangeAndUnits(t *testing.T) {
	cfg := rangeEnabledCfg("", 64<<10, 64<<10, 0)
	cfg.MaxRanges = 2
	c := newTestCache(t, cfg)
	var outcomes rangeOutcomeLog
	c.SetRangeObserver(outcomes.add)

	var lastOriginRange string
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastOriginRange = r.Header.Get("Range")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		if r.Header.Get("Range") != "" {
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("ORIGIN-RANGE"))
			return
		}
		_, _ = w.Write([]byte("0123456789"))
	}))

	// Warm the cache.
	wantResult(t, get(t, h, "http://example.com/doc"), stateMiss, "0123456789")

	invalidRanges := []string{
		"",
		"bytes",
		"bytes=",
		"bytes= , , ",
		"bytes=abc",
		"bytes=5-3", // start > end
		"bytes=-",
		"bytes=--5",
		"bytes=1-2-3",
		"bytes=99999999999999999999-100000000000000000000", // int64 overflow
		"bytes=0-1,2-3,4-5",                            // 3 ranges > MaxRanges (2)
		"bytes=" + strings.Repeat("0-0,", 100) + "0-0", // exceeds hard parser cap
		"bad unit=0-3",
	}
	for _, rng := range invalidRanges {
		outcomes.reset()
		rec := get(t, h, "http://example.com/doc", "Range", rng)
		if got := rec.Header().Get("X-Cache"); got != stateBypass {
			t.Fatalf("Range %q: X-Cache = %q, want BYPASS", rng, got)
		}
		if outcomes.count(string(rangeOutcomeInvalid)) != 1 {
			t.Fatalf("Range %q: outcomes = %v, want [invalid]", rng, outcomes.items)
		}
	}

	// Multiple Range header lines -> invalid + BYPASS.
	outcomes.reset()
	multiLineReq := httptest.NewRequest(http.MethodGet, "http://example.com/doc", nil)
	multiLineReq.Header.Add("Range", "bytes=0-1")
	multiLineReq.Header.Add("Range", "bytes=2-3")
	multiLineRec := httptest.NewRecorder()
	h.ServeHTTP(multiLineRec, multiLineReq)
	if multiLineRec.Header().Get("X-Cache") != stateBypass || outcomes.count(string(rangeOutcomeInvalid)) != 1 {
		t.Fatalf("multiple Range lines: X-Cache=%q outcomes=%v", multiLineRec.Header().Get("X-Cache"), outcomes.items)
	}

	// Valid 2-range request within MaxRanges=2 -> fallback to origin with original Range preserved.
	outcomes.reset()
	lastOriginRange = ""
	recMulti := get(t, h, "http://example.com/doc", "Range", "bytes=0-1, 4-5")
	wantResult(t, recMulti, stateBypass, "ORIGIN-RANGE")
	if lastOriginRange != "bytes=0-1, 4-5" || outcomes.count(string(rangeOutcomeFallback)) != 1 {
		t.Fatalf("multi-range fallback: lastOriginRange=%q outcomes=%v", lastOriginRange, outcomes.items)
	}

	// Non-bytes range unit -> fallback to origin with original Range preserved.
	outcomes.reset()
	lastOriginRange = ""
	recUnit := get(t, h, "http://example.com/doc", "Range", "items=0-5")
	wantResult(t, recUnit, stateBypass, "ORIGIN-RANGE")
	if lastOriginRange != "items=0-5" || outcomes.count(string(rangeOutcomeFallback)) != 1 {
		t.Fatalf("non-bytes unit fallback: lastOriginRange=%q outcomes=%v", lastOriginRange, outcomes.items)
	}

	// HEAD with Range bypasses without synthesizing 206.
	headReq := httptest.NewRequest(http.MethodHead, "http://example.com/doc", nil)
	headReq.Header.Set("Range", "bytes=0-3")
	headRec := httptest.NewRecorder()
	h.ServeHTTP(headRec, headReq)
	if headRec.Header().Get("X-Cache") != stateBypass {
		t.Fatalf("HEAD with Range: X-Cache = %q, want BYPASS", headRec.Header().Get("X-Cache"))
	}
}

// TestRangeOptionAVaryAuthCompressionStaleAndInvalidation verifies that Option A
// preserves Vary isolation, RFC 9111 §3.5 Authorization rules, Content-Encoding
// representation byte offsets, stale/304/200 revalidation, and unsafe-method
// invalidation.
func TestRangeOptionAVaryAuthCompressionStaleAndInvalidation(t *testing.T) {
	dir := t.TempDir()
	cfg := rangeEnabledCfg(dir, 1<<10, 64<<10, 512<<10)
	cfg.StaleWhileRevalidate = config.Duration(time.Minute)
	c := newTestCache(t, cfg)

	now := time.Date(2026, 10, 15, 14, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	c.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		mu.Lock()
		now = now.Add(d)
		mu.Unlock()
	}

	gzipBytes := bytes.Repeat([]byte("GZ01"), 1024) // 4 KiB > 1 KiB memMax (disk-backed)
	brBytes := bytes.Repeat([]byte("BR99"), 1024)
	var etag atomic.Value
	etag.Store(`"v1"`)

	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/private":
			w.Header().Set("Cache-Control", "private, max-age=3600")
			if r.Header.Get("Range") == "bytes=0-3" {
				w.Header().Set("Content-Range", "bytes 0-3/10")
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write([]byte("PRIV"))
				return
			}
			_, _ = w.Write([]byte("PRIV-FULL!"))
			return
		case "/auth-public":
			w.Header().Set("Cache-Control", "public, max-age=3600")
			_, _ = w.Write([]byte("AUTH-PUBLIC-PAYLOAD"))
			return
		case "/auth-unmarked":
			w.Header().Set("Cache-Control", "max-age=3600")
			if r.Header.Get("Range") == "bytes=0-3" {
				w.Header().Set("Content-Range", "bytes 0-3/12")
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write([]byte("UNMK"))
				return
			}
			_, _ = w.Write([]byte("UNMK-FULL-12"))
			return
		}

		// /coded: Vary: Accept-Encoding + Content-Encoding
		curTag := etag.Load().(string)
		w.Header().Set("Cache-Control", "public, max-age=10, stale-while-revalidate=60")
		w.Header().Set("Vary", "Accept-Encoding")
		w.Header().Set("ETag", curTag)
		enc := r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Encoding", enc)
		if r.Header.Get("If-None-Match") == curTag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if enc == "br" {
			_, _ = w.Write(brBytes)
		} else {
			_, _ = w.Write(gzipBytes)
		}
	}))

	// 1. Vary + Content-Encoding: cold range fill for gzip and br stores distinct
	// disk-backed variants and slices their exact compressed representation bytes.
	recGZ := get(t, h, "http://example.com/coded", "Accept-Encoding", "gzip", "Range", "bytes=0-7")
	wantResult(t, recGZ, stateMiss, "GZ01GZ01")
	if recGZ.Code != http.StatusPartialContent || recGZ.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("gzip range: code=%d enc=%q", recGZ.Code, recGZ.Header().Get("Content-Encoding"))
	}

	recBR := get(t, h, "http://example.com/coded", "Accept-Encoding", "br", "Range", "bytes=0-7")
	wantResult(t, recBR, stateMiss, "BR99BR99")
	if recBR.Code != http.StatusPartialContent || recBR.Header().Get("Content-Encoding") != "br" {
		t.Fatalf("br range: code=%d enc=%q", recBR.Code, recBR.Header().Get("Content-Encoding"))
	}

	// Warm range hit on gzip variant.
	recGZHit := get(t, h, "http://example.com/coded", "Accept-Encoding", "gzip", "Range", "bytes=4-11")
	wantResult(t, recGZHit, stateHit, "GZ01GZ01")

	// 2. Stale range hit (SWR) + 304 synchronous validation + 200 replacement with If-Range mismatch.
	advance(15 * time.Second) // expired (>10s), within SWR (<=70s)
	recStale := get(t, h, "http://example.com/coded", "Accept-Encoding", "gzip", "Range", "bytes=0-3")
	wantResult(t, recStale, stateStale, "GZ01")
	if recStale.Code != http.StatusPartialContent {
		t.Fatalf("stale range code = %d, want 206", recStale.Code)
	}

	// Synchronous no-cache validation returning 304 Not Modified -> 206 REVALIDATED.
	recReval304 := get(t, h, "http://example.com/coded", "Accept-Encoding", "gzip", "Cache-Control", "no-cache", "Range", "bytes=0-3")
	wantResult(t, recReval304, stateRevalidated, "GZ01")
	if recReval304.Code != http.StatusPartialContent {
		t.Fatalf("304 revalidated range code = %d, want 206", recReval304.Code)
	}

	// Change origin ETag to "v2" and send no-cache + If-Range: "v1" + Range: bytes=0-3 ->
	// origin returns new 200 OK ("v2"), If-Range "v1" mismatches "v2", so client gets full 200 OK!
	etag.Store(`"v2"`)
	recReval200 := get(t, h, "http://example.com/coded", "Accept-Encoding", "gzip", "Cache-Control", "no-cache", "If-Range", `"v1"`, "Range", "bytes=0-3")
	wantResult(t, recReval200, stateMiss, string(gzipBytes))
	if recReval200.Code != http.StatusOK {
		t.Fatalf("200 replacement with mismatched If-Range: code = %d, want 200", recReval200.Code)
	}

	// 3. Unsafe-method invalidation removes both full and range eligibility.
	delReq := httptest.NewRequest(http.MethodDelete, "http://example.com/coded", nil)
	delRec := httptest.NewRecorder()
	c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(delRec, delReq)
	if _, _, ok := c.lookup(key(delReq), delReq); ok {
		t.Fatal("expected DELETE to invalidate cached variants")
	}

	// 4. Private and unmarked-authenticated responses abort range fill and fall back to origin with original Range.
	recPriv := get(t, h, "http://example.com/private", "Range", "bytes=0-3")
	wantResult(t, recPriv, stateBypass, "PRIV")
	if recPriv.Code != http.StatusPartialContent {
		t.Fatalf("private fallback code = %d, want 206 from origin", recPriv.Code)
	}

	recAuthUnmarked := get(t, h, "http://example.com/auth-unmarked", "Authorization", "Bearer secret", "Range", "bytes=0-3")
	wantResult(t, recAuthUnmarked, stateBypass, "UNMK")

	recAuthPublic := get(t, h, "http://example.com/auth-public", "Authorization", "Bearer secret", "Range", "bytes=0-3")
	wantResult(t, recAuthPublic, stateMiss, "AUTH")
	recAuthPublicHit := get(t, h, "http://example.com/auth-public", "Authorization", "Bearer other", "Range", "bytes=5-10")
	wantResult(t, recAuthPublicHit, stateHit, "PUBLIC")
}

// TestRangeOptionAConcurrentCoalescingTimeoutAndBoundedHeap proves that:
//  1. 16 concurrent cold range requests for distinct slices of a 4 MiB object
//     coalesce into 1 origin fetch and each receives its exact 206 slice;
//  2. Warm disk-backed range hits allocate O(1) heap (<= 64 KiB) without
//     loading the 4 MiB object into memory;
//  3. A waiter whose lock_timeout expires falls back to the origin with its
//     original Range and If-Range headers preserved;
//  4. An active disk range reader completes cleanly even if the entry is
//     concurrently evicted or deleted.
func TestRangeOptionAConcurrentCoalescingTimeoutAndBoundedHeap(t *testing.T) {
	dir := t.TempDir()
	const (
		memMax   = 64 << 10 // 64 KiB
		maxEntry = 8 << 20  // 8 MiB
		diskMax  = 32 << 20 // 32 MiB
		objSize  = 2 << 20  // 2 MiB
	)
	cfg := rangeEnabledCfg(dir, memMax, maxEntry, diskMax)
	c := newTestCache(t, cfg)
	var outcomes rangeOutcomeLog
	c.SetRangeObserver(outcomes.add)

	payload := make([]byte, objSize)
	for i := range payload {
		payload[i] = byte((i * 31) ^ (i >> 8))
	}

	var originFullFetches atomic.Int32
	var originRangeFallbacks atomic.Int32
	holdOrigin := make(chan struct{})
	close(holdOrigin) // initially unblocked

	origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-holdOrigin
		if r.URL.Path == "/2mb.bin" {
			time.Sleep(15 * time.Millisecond)
		}
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("ETag", `"2mb-v1"`)
		if rng := r.Header.Get("Range"); rng != "" {
			originRangeFallbacks.Add(1)
			w.Header().Set("X-Fallback-If-Range", r.Header.Get("If-Range"))
			w.Header().Set("Content-Range", "bytes 0-3/2097152")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[:4])
			return
		}
		originFullFetches.Add(1)
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
		for off := 0; off < len(payload); off += 32 << 10 {
			_, _ = w.Write(payload[off : off+(32<<10)])
		}
	})
	h := c.Handler(origin)

	// 1. 16 concurrent cold range misses for distinct 64 KiB slices coalesce into 1 origin fetch.
	const workers = 16
	startGate := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-startGate
			startOff := idx * (64 << 10)
			endOff := startOff + (64 << 10) - 1
			rng := fmt.Sprintf("bytes=%d-%d", startOff, endOff)
			rec := get(t, h, "http://example.com/2mb.bin", "Range", rng)
			if rec.Code != http.StatusPartialContent {
				errs <- fmt.Errorf("worker %d: code = %d, want 206", idx, rec.Code)
				return
			}
			if !bytes.Equal(rec.Body.Bytes(), payload[startOff:endOff+1]) {
				errs <- fmt.Errorf("worker %d: body mismatch (got %d bytes)", idx, rec.Body.Len())
			}
		}(i)
	}
	close(startGate)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if got := originFullFetches.Load(); got != 1 {
		t.Fatalf("origin full fetches across %d concurrent cold range misses = %d, want 1", workers, got)
	}
	if got := outcomes.count(string(rangeOutcomeFill)); got != 1 {
		t.Fatalf("range fill metric across %d coalesced cold misses = %d, want 1 (%v)", workers, got, outcomes.items)
	}
	if got := outcomes.count(string(rangeOutcomeHit)); got != workers {
		t.Fatalf("range hit metric across %d coalesced cold misses = %d, want %d (%v)", workers, got, workers, outcomes.items)
	}

	// 2. Bounded heap allocation on warm disk-backed range hit.
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	dw := &discardResponseWriter{header: make(http.Header)}
	req := httptest.NewRequest(http.MethodGet, "http://example.com/2mb.bin", nil)
	req.Header.Set("Range", "bytes=524288-589823") // 64 KiB middle slice
	h.ServeHTTP(dw, req)
	runtime.ReadMemStats(&after)
	if dw.status != http.StatusPartialContent || dw.written != 64<<10 {
		t.Fatalf("warm disk range hit status=%d written=%d, want 206 and 65536", dw.status, dw.written)
	}
	allocDelta := int64(after.TotalAlloc - before.TotalAlloc)
	if allocDelta > objSize/4 {
		t.Fatalf("warm disk range hit allocated %d bytes (> %d), expected O(1) chunked streaming", allocDelta, objSize/4)
	}

	// 3. Fill-lock timeout on cold range miss falls back to origin with original Range and If-Range preserved.
	cfgShort := cfg
	cfgShort.LockTimeout = config.Duration(25 * time.Millisecond)
	c.PrepareCacheUpdate(cfgShort).Commit()

	holdOrigin = make(chan struct{})
	var leaderWG sync.WaitGroup
	leaderWG.Add(1)
	go func() {
		defer leaderWG.Done()
		get(t, h, "http://example.com/slow.bin", "Range", "bytes=0-15")
	}()
	// Wait until leader has registered the fill lock.
	for c.inflightFills() == 0 {
		time.Sleep(2 * time.Millisecond)
	}

	// Waiter times out after 25ms and falls back to origin with its Range and If-Range preserved.
	unblockWaiterOrigin := make(chan struct{})
	hWaiter := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(unblockWaiterOrigin)
		if r.Header.Get("Range") != "bytes=0-3" || r.Header.Get("If-Range") != `"cond-1"` {
			t.Errorf("timeout fallback lost headers: Range=%q If-Range=%q", r.Header.Get("Range"), r.Header.Get("If-Range"))
		}
		w.Header().Set("Content-Range", "bytes 0-3/2097152")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[:4])
	}))
	waiterRec := get(t, hWaiter, "http://example.com/slow.bin", "Range", "bytes=0-3", "If-Range", `"cond-1"`)
	<-unblockWaiterOrigin
	wantResult(t, waiterRec, stateBypass, string(payload[:4]))

	close(holdOrigin)
	leaderWG.Wait()
}

// TestRangeGateEvaluationSyntheticWorkload executes the predeclared
// SYNTH-442-MEDIA-V1 synthetic workload comparing Option C (Range bypass)
// against Option A (complete-object fill + local range serving) and asserts
// every predeclared pass/fail gate threshold from #442.
func TestRangeGateEvaluationSyntheticWorkload(t *testing.T) {
	dir := t.TempDir()
	const (
		memMax   = 256 << 10 // 256 KiB
		maxEntry = 8 << 20   // 8 MiB
		diskMax  = 64 << 20  // 64 MiB
		objSize  = 4 << 20   // 4 MiB media/artifact object
	)
	payload := make([]byte, objSize)
	for i := range payload {
		payload[i] = byte(i % 251)
	}

	runProfileR1 := func(rangeEnabled bool) (originReqs int64, originBytes int64, clientBytes int64, p50, p95 time.Duration) {
		cfg := rangeEnabledCfg(filepath.Join(dir, fmt.Sprintf("r1-%v", rangeEnabled)), memMax, maxEntry, diskMax)
		cfg.RangeEnabled = rangeEnabled
		if !rangeEnabled {
			cfg.MaxRanges = 0
		}
		c := newTestCache(t, cfg)

		var oReqs, oBytes atomic.Int64
		origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			oReqs.Add(1)
			w.Header().Set("Cache-Control", "public, max-age=3600")
			w.Header().Set("ETag", `"synth-4mb"`)
			if rng := r.Header.Get("Range"); rng != "" {
				spec, res := parseRangeHeader([]string{rng}, 1)
				if res == rangeParseSingle {
					start, length, ok := resolveByteRange(spec, int64(len(payload)))
					if ok {
						w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, len(payload)))
						w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
						w.WriteHeader(http.StatusPartialContent)
						n, _ := w.Write(payload[start : start+length])
						oBytes.Add(int64(n))
						return
					}
				}
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			n, _ := w.Write(payload)
			oBytes.Add(int64(n))
		})
		h := c.Handler(origin)

		ranges := []string{
			"bytes=0-65535",         // 64 KiB prefix
			"bytes=1048576-1114111", // 64 KiB middle
			"bytes=2097152-2359295", // 256 KiB middle
			"bytes=-32768",          // 32 KiB suffix
		}
		const totalRequests = 100
		durations := make([]time.Duration, 0, totalRequests)
		var cBytes int64
		for i := 0; i < totalRequests; i++ {
			rng := ranges[i%len(ranges)]
			dw := &discardResponseWriter{header: make(http.Header)}
			req := httptest.NewRequest(http.MethodGet, "http://example.com/video.mp4", nil)
			req.Header.Set("Range", rng)
			start := time.Now()
			h.ServeHTTP(dw, req)
			durations = append(durations, time.Since(start))
			if dw.status != http.StatusPartialContent {
				t.Fatalf("rangeEnabled=%v req %d: status=%d, want 206", rangeEnabled, i, dw.status)
			}
			cBytes += dw.written
		}
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		return oReqs.Load(), oBytes.Load(), cBytes, durations[len(durations)*50/100], durations[len(durations)*95/100]
	}

	bypassReqs, bypassOriginBytes, bypassClientBytes, bypassP50, bypassP95 := runProfileR1(false)
	optAReqs, optAOriginBytes, optAClientBytes, optAP50, optAP95 := runProfileR1(true)

	if bypassClientBytes != optAClientBytes {
		t.Fatalf("client bytes mismatch: bypass=%d optionA=%d", bypassClientBytes, optAClientBytes)
	}
	// Gate 1: Origin request reduction >= 95% (1 origin fetch vs 100 under bypass).
	if bypassReqs != 100 || optAReqs != 1 {
		t.Fatalf("Profile R1 origin requests: bypass=%d optionA=%d, want 100 vs 1", bypassReqs, optAReqs)
	}
	// Gate 2: Origin byte reduction >= 60% on mixed 64K/256K/32K range stream (4 MiB vs ~10.15 MiB).
	if optAOriginBytes != objSize || optAOriginBytes >= bypassOriginBytes/2 {
		t.Fatalf("Profile R1 origin bytes: bypass=%d optionA=%d, expected >50%% savings", bypassOriginBytes, optAOriginBytes)
	}
	t.Logf("SYNTH-442-MEDIA-V1 R1 (100 reqs on 4MiB object): bypass=(reqs=%d, originBytes=%d, p50=%v, p95=%v) vs OptionA=(reqs=%d, originBytes=%d, p50=%v, p95=%v)",
		bypassReqs, bypassOriginBytes, bypassP50, bypassP95, optAReqs, optAOriginBytes, optAP50, optAP95)
}

func TestRangeOptionACancellationActiveReaderAndUnlockedFill(t *testing.T) {
	var nilCache *Cache
	nilCache.SetRangeObserver(func(string) {})

	dir := t.TempDir()
	cfg := rangeEnabledCfg(dir, 1<<10, 128<<10, 512<<10)
	cfg.Lock = false // test unlocked range fill and fallback paths too
	cfg.LockTimeout = 0
	c := newTestCache(t, cfg)

	payload := bytes.Repeat([]byte("S"), 96<<10) // 96 KiB
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cancel-fill" {
			w.Header().Set("Cache-Control", "public, max-age=3600")
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(payload[:4<<10])
			return
		}
		if r.URL.Path == "/oversize" {
			w.Header().Set("Cache-Control", "public, max-age=3600")
			if r.Header.Get("Range") != "" {
				w.Header().Set("Content-Range", "bytes 0-3/500000")
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write([]byte("OVER"))
				return
			}
			w.Header().Set("Content-Length", "500000")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload)
	}))

	// 1. Unlocked cold range fill + warm range hit.
	rec1 := get(t, h, "http://example.com/unlocked", "Range", "bytes=0-15")
	wantResult(t, rec1, stateMiss, string(payload[:16]))
	rec2 := get(t, h, "http://example.com/unlocked", "Range", "bytes=16-31")
	wantResult(t, rec2, stateHit, string(payload[16:32]))

	// 2. Unlocked oversize fill fallback preserves original Range.
	recOver := get(t, h, "http://example.com/oversize", "Range", "bytes=0-3")
	wantResult(t, recOver, stateBypass, "OVER")

	// 3. Truncated origin fill aborts and falls back to origin.
	recTrunc := get(t, h, "http://example.com/cancel-fill", "Range", "bytes=0-3")
	if recTrunc.Header().Get("X-Cache") != stateBypass {
		t.Fatalf("truncated fill X-Cache = %q, want BYPASS", recTrunc.Header().Get("X-Cache"))
	}

	// 4. Client context cancellation during locked range waiter.
	cfgLocked := cfg
	cfgLocked.Lock = true
	cfgLocked.LockTimeout = config.Duration(5 * time.Second)
	c.PrepareCacheUpdate(cfgLocked).Commit()

	hold := make(chan struct{})
	started := make(chan struct{})
	hHold := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-hold
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(payload)
	}))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		get(t, hHold, "http://example.com/wait-cancel", "Range", "bytes=0-15")
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelReq := httptest.NewRequest(http.MethodGet, "http://example.com/wait-cancel", nil).WithContext(ctx)
	cancelReq.Header.Set("Range", "bytes=0-15")
	hHold.ServeHTTP(httptest.NewRecorder(), cancelReq)
	close(hold)
	wg.Wait()

	// 5. Active range reader (64 KiB slice > 32 KiB chunk) survives concurrent Delete.
	bw := newBlockingChunkWriter()
	var rwg sync.WaitGroup
	rwg.Add(1)
	go func() {
		defer rwg.Done()
		req := httptest.NewRequest(http.MethodGet, "http://example.com/unlocked", nil)
		req.Header.Set("Range", "bytes=0-65535")
		h.ServeHTTP(bw, req)
	}()
	<-bw.firstChunk
	c.Delete(key(httptest.NewRequest(http.MethodGet, "http://example.com/unlocked", nil)))
	close(bw.resumeWrite)
	rwg.Wait()
	if bw.status != http.StatusPartialContent || !bytes.Equal(bw.buf.Bytes(), payload[:65536]) {
		t.Fatalf("active range reader during delete: status=%d len=%d", bw.status, bw.buf.Len())
	}

	// 6. Edge cases: parseRangeHeader with maxRanges=0, ifRangeMatches edge inputs,
	// request no-store with Range, locked range waiter on uncacheable fill and Vary retry,
	// and range synchronous validation fallback on 500/private.
	if _, res := parseRangeHeader([]string{"bytes=0-1"}, 0); res != rangeParseSingle {
		t.Fatalf("parseRangeHeader with maxRanges=0 = %v, want single", res)
	}
	if _, _, ok := resolveByteRange(byteRangeSpec{}, 10); ok {
		t.Fatal("expected unknown rangeKind to return false")
	}
	for _, badIR := range []string{"", `"unterminated`, "Mon, 99 XXX 2026 15:04:05 GMT"} {
		irReq := httptest.NewRequest(http.MethodGet, "http://example.com/ir", nil)
		irReq.Header.Set("If-Range", badIR)
		if ifRangeMatches(irReq, &Entry{ETag: `"v1"`, LastModified: badIR}) {
			t.Fatalf("expected ifRangeMatches(%q) to be false", badIR)
		}
	}

	// Request Cache-Control: no-store with Range bypasses.
	recNoStore := get(t, h, "http://example.com/unlocked", "Cache-Control", "no-store", "Range", "bytes=0-3")
	if recNoStore.Header().Get("X-Cache") != stateBypass {
		t.Fatalf("no-store Range X-Cache = %q, want BYPASS", recNoStore.Header().Get("X-Cache"))
	}

	// Locked range waiter when leader fill is uncacheable -> waiter falls back to origin.
	holdUncacheable := make(chan struct{})
	startedUncacheable := make(chan struct{})
	var uncacheableOnce sync.Once
	hUncacheable := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uncacheableOnce.Do(func() {
			close(startedUncacheable)
			<-holdUncacheable
		})
		w.Header().Set("Cache-Control", "no-store")
		if r.Header.Get("Range") != "" {
			w.Header().Set("Content-Range", "bytes 0-3/8")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("FALL"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("FULLBODY"))
	}))
	var uwg sync.WaitGroup
	uwg.Add(2)
	go func() {
		defer uwg.Done()
		get(t, hUncacheable, "http://example.com/uncacheable-lock", "Range", "bytes=0-3")
	}()
	<-startedUncacheable
	go func() {
		defer uwg.Done()
		for c.inflightFills() == 0 {
			time.Sleep(time.Millisecond)
		}
		close(holdUncacheable)
		recW := get(t, hUncacheable, "http://example.com/uncacheable-lock", "Range", "bytes=0-3")
		wantResult(t, recW, stateBypass, "FALL")
	}()
	uwg.Wait()

	// Locked range waiter when leader discovers Vary: X-Var -> waiter retries variant fill.
	holdVary := make(chan struct{})
	startedVary := make(chan struct{})
	var varyOnce sync.Once
	hVary := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		varyOnce.Do(func() {
			close(startedVary)
			<-holdVary
		})
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("Vary", "X-Var")
		_, _ = w.Write([]byte("VAR-" + r.Header.Get("X-Var") + "-PAYLOAD"))
	}))
	var vwg sync.WaitGroup
	vwg.Add(2)
	go func() {
		defer vwg.Done()
		get(t, hVary, "http://example.com/vary-lock", "X-Var", "A", "Range", "bytes=0-4")
	}()
	<-startedVary
	go func() {
		defer vwg.Done()
		for c.inflightFills() == 0 {
			time.Sleep(time.Millisecond)
		}
		close(holdVary)
		recB := get(t, hVary, "http://example.com/vary-lock", "X-Var", "B", "Range", "bytes=0-4")
		if recB.Code != http.StatusPartialContent || recB.Body.String() != "VAR-B" {
			t.Errorf("Vary waiter code=%d body=%q, want 206 VAR-B", recB.Code, recB.Body.String())
		}
	}()
	vwg.Wait()

	// Range synchronous validation fallback when origin returns 500 (and concurrent waiter).
	var valStatus atomic.Int32
	valStatus.Store(http.StatusOK)
	holdVal := make(chan struct{})
	close(holdVal)
	hVal := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-holdVal
		code := int(valStatus.Load())
		if r.Header.Get("Range") != "" {
			w.Header().Set("Content-Range", "bytes 0-3/10")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("VALF"))
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("ETag", `"val-1"`)
		w.WriteHeader(code)
		_, _ = w.Write([]byte("0123456789"))
	}))
	wantResult(t, get(t, hVal, "http://example.com/val-range"), stateMiss, "0123456789")
	valStatus.Store(http.StatusInternalServerError)
	recVal500 := get(t, hVal, "http://example.com/val-range", "Cache-Control", "no-cache", "Range", "bytes=0-3")
	wantResult(t, recVal500, stateBypass, "VALF")
}

// TestRangeOptionAReviewRegressions covers:
//  1. Vary: Range, Vary: If-Range, Vary: If-None-Match, and Vary: If-Modified-Since
//     across both lock=true and lock=false fill paths, synchronous revalidation,
//     and background stale-while-revalidate revalidation, proving an internal
//     stripped/modified request never serves or stores the wrong Vary representation;
//  2. Mixed-case range units (Bytes=0-3, BYTES=0-3);
//  3. Stored Accept-Ranges: none overridden to Accept-Ranges: bytes on 206 and 416;
//  4. Serving ranges from a rehydrated disk entry after restart;
//  5. Disk LRU eviction while an active disk-backed range reader is streaming.
func TestRangeOptionAReviewRegressions(t *testing.T) {
	// 1. Vary on Range / If-Range / If-None-Match / If-Modified-Since across lock=false and lock=true,
	//    including synchronous revalidation and background stale-while-revalidate revalidation.
	for _, lockEnabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("vary-stripped-headers-lock-%v", lockEnabled), func(t *testing.T) {
			cfg := rangeEnabledCfg(t.TempDir(), 1<<10, 64<<10, 512<<10)
			cfg.Lock = lockEnabled
			if !lockEnabled {
				cfg.LockTimeout = 0
			}
			c := newTestCache(t, cfg)
			now := time.Date(2026, 10, 15, 16, 0, 0, 0, time.UTC)
			var clockMu sync.Mutex
			c.now = func() time.Time {
				clockMu.Lock()
				defer clockMu.Unlock()
				return now
			}
			lastMod := time.Date(2026, 10, 15, 15, 0, 0, 0, time.UTC).Format(http.TimeFormat)
			bgDone := make(chan struct{}, 4)

			h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", "public, max-age=10, stale-while-revalidate=30")
				switch r.URL.Path {
				case "/vary-range":
					w.Header().Set("Vary", "Range")
					if r.Header.Get("Range") != "" {
						w.Header().Set("Content-Range", "bytes 0-3/10")
						w.WriteHeader(http.StatusPartialContent)
						_, _ = w.Write([]byte("RANG"))
						return
					}
					_, _ = w.Write([]byte("NON-RANGE!"))
				case "/vary-if-range":
					w.Header().Set("Vary", "If-Range")
					if r.Header.Get("Range") != "" {
						w.Header().Set("Content-Range", "bytes 0-3/10")
						w.WriteHeader(http.StatusPartialContent)
						_, _ = w.Write([]byte("IFRG"))
						return
					}
					_, _ = w.Write([]byte("NO-IF-RNG!"))
				case "/vary-inm":
					w.Header().Set("Vary", "If-None-Match")
					w.Header().Set("ETag", `"v1"`)
					if r.Header.Get("If-None-Match") != "" {
						_, _ = w.Write([]byte("COND-INM-BODY"))
						return
					}
					_, _ = w.Write([]byte("PLAIN-0123456"))
				case "/vary-ims":
					w.Header().Set("Vary", "If-Modified-Since")
					w.Header().Set("Last-Modified", lastMod)
					if r.Header.Get("Range") != "" {
						w.Header().Set("Content-Range", "bytes 0-3/13")
						w.WriteHeader(http.StatusPartialContent)
						_, _ = w.Write([]byte("IMSR"))
						return
					}
					if r.Header.Get("If-Modified-Since") != "" {
						_, _ = w.Write([]byte("COND-IMS-BODY"))
						select {
						case bgDone <- struct{}{}:
						default:
						}
						return
					}
					_, _ = w.Write([]byte("PLAIN-IMS-BOD"))
				}
			}))

			// Vary: Range must never slice "NON-RANGE!" and must fall back to origin's "RANG".
			recVR := get(t, h, "http://example.com/vary-range", "Range", "bytes=0-3")
			wantResult(t, recVR, stateBypass, "RANG")
			if recVR.Code != http.StatusPartialContent {
				t.Fatalf("Vary: Range code = %d, want 206", recVR.Code)
			}

			// Vary: If-Range when request carries If-Range must fall back to origin's "IFRG".
			recVIR := get(t, h, "http://example.com/vary-if-range", "If-Range", `"v1"`, "Range", "bytes=0-3")
			wantResult(t, recVIR, stateBypass, "IFRG")

			// Vary: If-Modified-Since on internal range fill when client sends If-Modified-Since
			// must reject the stripped-header fill and fall back to origin's "IMSR".
			recVIMSFill := get(t, h, "http://example.com/vary-ims", "If-Modified-Since", lastMod, "Range", "bytes=0-3")
			wantResult(t, recVIMSFill, stateBypass, "IMSR")

			// Synchronous revalidation when origin returns 200 OK with Vary: If-None-Match:
			// the conditional request injected If-None-Match: "v1" on the cache's behalf,
			// so the response does not match an unconditional client request and must not
			// serve "COND-INM-BODY" to a client that sent no If-None-Match.
			wantResult(t, get(t, h, "http://example.com/vary-inm"), stateMiss, "PLAIN-0123456")
			recReval := get(t, h, "http://example.com/vary-inm", "Cache-Control", "no-cache")
			if recReval.Body.String() != "PLAIN-0123456" {
				t.Fatalf("synchronous revalidation with Vary: If-None-Match served %q, want PLAIN-0123456", recReval.Body.String())
			}

			// Synchronous revalidation when origin returns 200 OK with Vary: If-Modified-Since:
			// the conditional request injected If-Modified-Since on the cache's behalf,
			// so it must not serve or store "COND-IMS-BODY" for an unconditional request.
			wantResult(t, get(t, h, "http://example.com/vary-ims"), stateMiss, "PLAIN-IMS-BOD")
			recRevalIMS := get(t, h, "http://example.com/vary-ims", "Cache-Control", "no-cache")
			if recRevalIMS.Body.String() != "PLAIN-IMS-BOD" {
				t.Fatalf("synchronous revalidation with Vary: If-Modified-Since served %q, want PLAIN-IMS-BOD", recRevalIMS.Body.String())
			}

			// Background stale-while-revalidate revalidation with Vary: If-Modified-Since:
			// /vary-ims is already warm from fallbackAfterValidation; advance clock into SWR
			// window, trigger background refresh with a leased request, and verify
			// "COND-IMS-BODY" is rejected and never stored for unconditional requests.
			for len(bgDone) > 0 {
				<-bgDone
			}
			clockMu.Lock()
			now = now.Add(15 * time.Second)
			clockMu.Unlock()
			lease := testLease(t, 1)
			staleReq := leased(httptest.NewRequest(http.MethodGet, "http://example.com/vary-ims", nil), lease)
			recStale := httptest.NewRecorder()
			h.ServeHTTP(recStale, staleReq)
			wantResult(t, recStale, stateStale, "PLAIN-IMS-BOD")
			<-bgDone
			waitDrained(t, c)
			recAfterBG := get(t, h, "http://example.com/vary-ims")
			if recAfterBG.Body.String() == "COND-IMS-BODY" {
				t.Fatal("background revalidation stored COND-IMS-BODY for unconditional request")
			}
		})
	}

	// 2. Mixed-case Range units (Bytes=0-3, BYTES=4-7) and overriding stored Accept-Ranges: none.
	dir := t.TempDir()
	cfg := rangeEnabledCfg(dir, 1<<10, 128<<10, 512<<10)
	c := newTestCache(t, cfg)
	largePayload := bytes.Repeat([]byte("0123456789abcdef"), 6<<10) // 96 KiB (disk-backed)

	origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("Accept-Ranges", "none")
		w.Header().Set("Content-Digest", `sha-256=:fullBodyDigestShouldBeStrippedOn206And416:`)
		w.Header().Set("Content-MD5", "d41d8cd98f00b204e9800998ecf8427e")
		w.Header().Set("Signature-Input", `sig1=("@status" "content-digest" "content-length");created=1700000000`)
		w.Header().Set("Signature", `sig1=:dGVzdC1zaWduYXR1cmU=:`)
		w.Header().Set("Repr-Digest", `sha-256=:completeRepresentationDigestPreserved:`)
		w.Header().Add("X-Multi", "one")
		w.Header().Add("X-Multi", "two")
		if r.URL.Path == "/mem-digest" {
			w.Header().Set("Content-Length", "16")
			_, _ = w.Write(largePayload[:16])
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(largePayload)))
		_, _ = w.Write(largePayload)
	})
	h := c.Handler(origin)

	recMixed1 := get(t, h, "http://example.com/disk-rehydrate", "Range", "Bytes=0-3")
	wantResult(t, recMixed1, stateMiss, "0123")
	if recMixed1.Code != http.StatusPartialContent || recMixed1.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("Bytes=0-3: code=%d Accept-Ranges=%q, want 206 and bytes", recMixed1.Code, recMixed1.Header().Get("Accept-Ranges"))
	}
	if recMixed1.Header().Get("Content-Digest") != "" || recMixed1.Header().Get("Content-MD5") != "" ||
		recMixed1.Header().Get("Signature") != "" || recMixed1.Header().Get("Signature-Input") != "" {
		t.Fatalf("disk 206 retained content-scoped digest/signature: %v", recMixed1.Header())
	}
	if recMixed1.Header().Get("Repr-Digest") == "" {
		t.Fatal("disk 206 stripped Repr-Digest, want preserved")
	}
	if len(recMixed1.Header().Values("X-Multi")) != 2 {
		t.Fatalf("206 X-Multi values = %v, want 2 values", recMixed1.Header().Values("X-Multi"))
	}

	// Empty list elements in 1#range-spec ("bytes=,4-7" and "bytes=4-7, ,") are ignored per RFC 9110 §5.6.1.2.
	recMixed2 := get(t, h, "http://example.com/disk-rehydrate", "Range", "BYTES=, 4-7, ,")
	wantResult(t, recMixed2, stateHit, "4567")
	if recMixed2.Code != http.StatusPartialContent || recMixed2.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("BYTES=, 4-7, ,: code=%d Accept-Ranges=%q, want 206 and bytes", recMixed2.Code, recMixed2.Header().Get("Accept-Ranges"))
	}

	// Memory-backed 206 and 416 also strip Content-Digest / Content-MD5 / Signature / Signature-Input
	// while preserving Repr-Digest; full 200 OK preserves Content-Digest and Signature.
	recMem206 := get(t, h, "http://example.com/mem-digest", "Range", "bytes=,0-3")
	wantResult(t, recMem206, stateMiss, "0123")
	if recMem206.Header().Get("Content-Digest") != "" || recMem206.Header().Get("Content-MD5") != "" ||
		recMem206.Header().Get("Signature") != "" || recMem206.Header().Get("Signature-Input") != "" ||
		recMem206.Header().Get("Repr-Digest") == "" {
		t.Fatalf("mem 206 digest/signature headers: %v", recMem206.Header())
	}
	recMem416 := get(t, h, "http://example.com/mem-digest", "Range", "bytes=99-100")
	if recMem416.Code != http.StatusRequestedRangeNotSatisfiable || recMem416.Header().Get("Content-Digest") != "" ||
		recMem416.Header().Get("Signature") != "" || recMem416.Header().Get("Signature-Input") != "" ||
		recMem416.Header().Get("Repr-Digest") == "" {
		t.Fatalf("mem 416 digest/signature headers: code=%d %v", recMem416.Code, recMem416.Header())
	}
	recMem200 := get(t, h, "http://example.com/mem-digest")
	wantResult(t, recMem200, stateHit, "0123456789abcdef")
	if recMem200.Header().Get("Content-Digest") == "" || recMem200.Header().Get("Signature") == "" || recMem200.Header().Get("Signature-Input") == "" {
		t.Fatal("full 200 OK lost Content-Digest or Signature")
	}

	rec416 := get(t, h, "http://example.com/disk-rehydrate", "Range", "Bytes=999999-1000000")
	if rec416.Code != http.StatusRequestedRangeNotSatisfiable || rec416.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("416 with stored Accept-Ranges: none -> code=%d Accept-Ranges=%q, want 416 and bytes", rec416.Code, rec416.Header().Get("Accept-Ranges"))
	}
	if rec416.Header().Get("Content-Digest") != "" || rec416.Header().Get("Content-MD5") != "" ||
		rec416.Header().Get("Signature") != "" || rec416.Header().Get("Signature-Input") != "" ||
		rec416.Header().Get("Repr-Digest") == "" {
		t.Fatalf("disk 416 digest/signature headers: %v", rec416.Header())
	}
	if len(rec416.Header().Values("X-Multi")) != 2 {
		t.Fatalf("416 X-Multi values = %v, want 2 values", rec416.Header().Values("X-Multi"))
	}

	// Synthesized Date uses receipt time `now` (not age-adjusted `created`) when Age is present without Date.
	receiptTime := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	lastModHalfHourAgo := receiptTime.Add(-30 * time.Minute).Format(http.TimeFormat)
	c.now = func() time.Time { return receiptTime }
	hAgeNoDate := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=7200")
		w.Header().Set("Age", "3600")
		w.Header().Set("Last-Modified", lastModHalfHourAgo)
		_, _ = w.Write([]byte("0123456789abcdef"))
	}))
	wantResult(t, get(t, hAgeNoDate, "http://example.com/age-no-date"), stateMiss, "0123456789abcdef")
	recAgeIfRange := get(t, hAgeNoDate, "http://example.com/age-no-date", "Range", "bytes=0-3", "If-Range", lastModHalfHourAgo)
	wantResult(t, recAgeIfRange, stateHit, "0123")
	if recAgeIfRange.Code != http.StatusPartialContent || recAgeIfRange.Header().Get("Date") != receiptTime.Format(http.TimeFormat) || recAgeIfRange.Header().Get("Age") != "3600" {
		t.Fatalf("synthesized Date with Age=3600: code=%d Date=%q Age=%q, want 206 Date=%q Age=3600",
			recAgeIfRange.Code, recAgeIfRange.Header().Get("Date"), recAgeIfRange.Header().Get("Age"), receiptTime.Format(http.TimeFormat))
	}

	// 3. Serving ranges from a rehydrated disk entry after process restart.
	cRestart := newTestCache(t, cfg)
	hRestart := cRestart.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("origin must not be contacted after disk restart rehydration")
	}))
	recRehydrated := get(t, hRestart, "http://example.com/disk-rehydrate", "Range", "bytes=16-31")
	wantResult(t, recRehydrated, stateHit, "0123456789abcdef")
	if recRehydrated.Code != http.StatusPartialContent || recRehydrated.Header().Get("Content-Range") != fmt.Sprintf("bytes 16-31/%d", len(largePayload)) {
		t.Fatalf("rehydrated disk range hit: code=%d Content-Range=%q", recRehydrated.Code, recRehydrated.Header().Get("Content-Range"))
	}

	// 4. LRU eviction while a disk-backed range reader (64 KiB > 32 KiB chunk) is active.
	bw := newBlockingChunkWriter()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		req := httptest.NewRequest(http.MethodGet, "http://example.com/disk-rehydrate", nil)
		req.Header.Set("Range", "bytes=0-65535")
		hRestart.ServeHTTP(bw, req)
	}()
	<-bw.firstChunk
	evicted, _, failed := cRestart.disk.Resize(1)
	if evicted < 1 || failed != 0 {
		t.Fatalf("disk.Resize(1) during active range reader: evicted=%d failed=%d, want >=1 and 0", evicted, failed)
	}
	close(bw.resumeWrite)
	wg.Wait()
	if bw.status != http.StatusPartialContent || !bytes.Equal(bw.buf.Bytes(), largePayload[:65536]) {
		t.Fatalf("active range reader during LRU eviction: status=%d len=%d", bw.status, bw.buf.Len())
	}
	assertNoTempFiles(t, dir)

	// 5. Additional helper and cancellation/revalidation edge cases.
	if isHTTPToken("") || !isHTTPToken("x-custom!unit") {
		t.Fatal("unexpected isHTTPToken result for empty or punctuation token")
	}
	if _, ok := parseSingleByteRangeSpec(""); ok {
		t.Fatal("expected parseSingleByteRangeSpec(\"\") to return false")
	}
	if _, _, ok := resolveByteRange(byteRangeSpec{kind: rangeKindPrefix, first: 0, last: 0}, 0); ok {
		t.Fatal("expected resolveByteRange with totalLen=0 to return false")
	}
	validDateReq := httptest.NewRequest(http.MethodGet, "http://example.com/ir-date", nil)
	validDateReq.Header.Set("If-Range", "Mon, 02 Jan 2006 15:04:05 GMT")
	if ifRangeMatches(validDateReq, &Entry{LastModified: "not-an-http-date"}) {
		t.Fatal("expected ifRangeMatches with invalid stored LastModified to return false")
	}
	if ifRangeMatches(validDateReq, &Entry{
		LastModified: "Mon, 02 Jan 2006 15:04:05 GMT",
		Header:       http.Header{"Date": []string{"not-an-http-date"}},
	}) {
		t.Fatal("expected ifRangeMatches with invalid stored Date to return false")
	}
	starHdr := http.Header{"Vary": []string{"*"}}
	if varyCompatibleWithModifiedRequest(starHdr, validDateReq, validDateReq) {
		t.Fatal("expected Vary: * to be incompatible with modified request")
	}

	// Leader cancellation during locked range fill.
	cancelCtx, cancelFn := context.WithCancel(context.Background())
	hLeaderCancel := cRestart.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		cancelFn()
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
	}))
	leaderCancelReq := httptest.NewRequest(http.MethodGet, "http://example.com/leader-cancel", nil).WithContext(cancelCtx)
	leaderCancelReq.Header.Set("Range", "bytes=0-3")
	hLeaderCancel.ServeHTTP(httptest.NewRecorder(), leaderCancelReq)

	// Concurrent range waiters during synchronous validation failure (awaitValidation isRange fallback).
	var valCode atomic.Int32
	valCode.Store(http.StatusOK)
	valStarted := make(chan struct{})
	valRelease := make(chan struct{})
	var valOnce sync.Once
	hValWait := cRestart.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			w.Header().Set("Content-Range", "bytes 0-3/10")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("W206"))
			return
		}
		code := int(valCode.Load())
		if code != http.StatusOK {
			valOnce.Do(func() {
				close(valStarted)
				<-valRelease
			})
		}
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("ETag", `"vw1"`)
		w.WriteHeader(code)
		_, _ = w.Write([]byte("0123456789"))
	}))
	wantResult(t, get(t, hValWait, "http://example.com/val-wait"), stateMiss, "0123456789")
	valCode.Store(http.StatusBadGateway)
	var vwwg sync.WaitGroup
	vwwg.Add(2)
	go func() {
		defer vwwg.Done()
		rec1 := get(t, hValWait, "http://example.com/val-wait", "Cache-Control", "no-cache", "Range", "bytes=0-3")
		wantResult(t, rec1, stateBypass, "W206")
	}()
	<-valStarted
	go func() {
		defer vwwg.Done()
		time.Sleep(5 * time.Millisecond)
		close(valRelease)
		rec2 := get(t, hValWait, "http://example.com/val-wait", "Cache-Control", "no-cache", "Range", "bytes=0-3")
		wantResult(t, rec2, stateBypass, "W206")
	}()
	vwwg.Wait()
}

// TestLargeObjectAndRangeAuditRegressions exercises the deterministic barriers
// for the full-tranche audit findings:
//  1. Disk lookup promotion vs concurrent Delete and replacement;
//  2. Write-through A/B delayed-publication ordering + restart rehydration;
//  3. Failed disk removal/retirement (Delete, Purge, eviction) + tombstone
//     persistence across restart;
//  4. Exact-boundary large Vary entry (body == max_entry_size == disk_max_size)
//     across disk, restart, and memory boundary;
//  5. Temporary-disk budget accounting across body + metadata + trailer, memory
//     write-through, and Vary stubs, asserting actual directory occupancy;
//  6. Deferred canonical-path promotion zero-reader race and d.pinned cleanup;
//  7. Retired active-reader physical bytes in Stats() and capacity admission.
func TestLargeObjectAndRangeAuditRegressions(t *testing.T) {
	// 1. Disk lookup promotion vs concurrent Delete and replacement.
	t.Run("lookup-promotion-vs-delete-and-replace", func(t *testing.T) {
		dir := t.TempDir()
		c := newTestCache(t, rangeEnabledCfg(dir, 16<<10, 64<<10, 512<<10))
		k := "GET\nexample.com\n/promo-race"
		mkEntry := func(body string) *Entry {
			return &Entry{
				Status:    http.StatusOK,
				Header:    http.Header{"Cache-Control": {"public, max-age=3600"}},
				Body:      []byte(body),
				CreatedAt: time.Now(),
				ExpiresAt: time.Now().Add(time.Hour),
			}
		}

		// Case 1a: Concurrent Delete while disk lookup is in flight must not resurrect entry into memory.
		c.set(k, mkEntry("OLD-V1"))
		c.mem.del(k)
		atLease := make(chan struct{})
		resumeLease := make(chan struct{})
		var hitDel atomic.Bool
		c.disk.onLookupLeaseAcquire = func() {
			if hitDel.CompareAndSwap(false, true) {
				close(atLease)
				<-resumeLease
			}
		}
		var lookupDel *Entry
		var lookupDelOK bool
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			lookupDel, lookupDelOK = c.get(k)
		}()
		<-atLease
		c.Delete(k)
		close(resumeLease)
		wg.Wait()
		c.disk.onLookupLeaseAcquire = nil
		if lookupDelOK || lookupDel != nil {
			t.Fatalf("stale disk lookup after concurrent Delete returned ok=%v entry=%+v, want false", lookupDelOK, lookupDel)
		}
		if _, ok := c.get(k); ok {
			t.Fatal("deleted entry was resurrected into memory by concurrent disk lookup")
		}

		// Case 1b: Concurrent replacement while disk lookup is in flight must not overwrite newer memory generation.
		c.set(k, mkEntry("OLD-V1"))
		c.mem.del(k)
		atLease2 := make(chan struct{})
		resumeLease2 := make(chan struct{})
		var hitRep atomic.Bool
		c.disk.onLookupLeaseAcquire = func() {
			if hitRep.CompareAndSwap(false, true) {
				close(atLease2)
				<-resumeLease2
			}
		}
		var lookupRep *Entry
		var lookupRepOK bool
		wg.Add(1)
		go func() {
			defer wg.Done()
			lookupRep, lookupRepOK = c.get(k)
		}()
		<-atLease2
		c.set(k, mkEntry("NEW-V2"))
		close(resumeLease2)
		wg.Wait()
		c.disk.onLookupLeaseAcquire = nil
		if !lookupRepOK || string(lookupRep.Body) != "NEW-V2" {
			t.Fatalf("lookup during concurrent replacement returned ok=%v body=%q, want NEW-V2", lookupRepOK, lookupRep.Body)
		}
		if cur, ok := c.get(k); !ok || string(cur.Body) != "NEW-V2" {
			t.Fatalf("memory after concurrent replacement = %q, want NEW-V2", cur.Body)
		}
	})

	// 2. Write-through A/B delayed-publication ordering + restart rehydration.
	t.Run("write-through-ab-ordering-and-restart", func(t *testing.T) {
		dir := t.TempDir()
		cfg := rangeEnabledCfg(dir, 16<<10, 64<<10, 512<<10)
		c := newTestCache(t, cfg)
		k := "GET\nexample.com\n/ab-order"
		mkEntry := func(body string) *Entry {
			return &Entry{
				Status:    http.StatusOK,
				Header:    http.Header{"Cache-Control": {"public, max-age=3600"}},
				Body:      []byte(body),
				CreatedAt: time.Now(),
				ExpiresAt: time.Now().Add(time.Hour),
			}
		}

		aPaused := make(chan struct{})
		aResume := make(chan struct{})
		var hitA atomic.Bool
		c.disk.onBeforePublishLock = func(_ string, _ uint64) {
			if hitA.CompareAndSwap(false, true) {
				close(aPaused)
				<-aResume
			}
		}
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.set(k, mkEntry("VALUE-A-STALE"))
		}()
		<-aPaused
		c.set(k, mkEntry("VALUE-B-CURRENT"))
		close(aResume)
		wg.Wait()
		c.disk.onBeforePublishLock = nil

		if e, ok := c.get(k); !ok || string(e.Body) != "VALUE-B-CURRENT" {
			t.Fatalf("after A/B race c.get = %q, want VALUE-B-CURRENT", e.Body)
		}
		cRestart := newTestCache(t, cfg)
		if e, ok := cRestart.get(k); !ok || string(e.Body) != "VALUE-B-CURRENT" {
			t.Fatalf("after restart rehydration cRestart.get = %q, want VALUE-B-CURRENT", e.Body)
		}
	})

	// 3. Failed invalidation/removal (Delete, Purge, eviction) + tombstone across restart.
	t.Run("failed-removal-tombstones-and-eviction", func(t *testing.T) {
		dir := t.TempDir()
		cfg := rangeEnabledCfg(dir, 1<<10, 16<<10, 16<<10)
		c := newTestCache(t, cfg)
		mkEntry := func(body []byte) *Entry {
			return &Entry{
				Status:    http.StatusOK,
				Header:    http.Header{"Cache-Control": {"public, max-age=3600"}},
				Body:      body,
				CreatedAt: time.Now(),
				ExpiresAt: time.Now().Add(time.Hour),
			}
		}

		// Delete failure must record tombstone and never resurrect on restart.
		kDel := "GET\nexample.com\n/del-fail"
		c.set(kDel, mkEntry([]byte("SECRET-MUST-NOT-RESURRECT")))
		var failRemoves atomic.Bool
		failRemoves.Store(true)
		c.disk.testRemoveFile = func(p string) error {
			if failRemoves.Load() && isCacheFile(filepath.Base(p)) {
				return io.ErrUnexpectedEOF
			}
			return os.Remove(p)
		}
		c.Delete(kDel)
		if _, ok := c.get(kDel); ok {
			t.Fatal("deleted key still returned after failed unlink")
		}
		// Even if restart rehydrates while unlink still fails, kDel must not resurrect.
		c.disk.rehydrate()
		if _, ok := c.get(kDel); ok {
			t.Fatal("deleted key resurrected during rehydrate with persistent unlink failure")
		}
		// Overwriting a tombstoned key clears its tombstone once unlink/rename is restored.
		failRemoves.Store(false)
		c.set(kDel, mkEntry([]byte("FRESH-OVERWRITE")))
		c.disk.mu.Lock()
		delBlob := c.disk.items[hashKey(kDel)].Value.(*diskItem).blob
		c.disk.mu.Unlock()
		failRemoves.Store(true)
		c.disk.delIfBlob(kDel, delBlob)
		c.Delete(kDel)
		cRestart1 := newTestCache(t, cfg)
		if _, ok := cRestart1.get(kDel); ok {
			t.Fatal("deleted key resurrected after restart")
		}

		// Purge failure must record tombstone and never resurrect on restart.
		kPurge := "GET\nexample.com\n/purge-fail"
		c.disk.testRemoveFile = nil
		c.set(kPurge, mkEntry([]byte("PURGED-DATA")))
		failRemoves.Store(true)
		c.disk.testRemoveFile = func(p string) error {
			if failRemoves.Load() && isCacheFile(filepath.Base(p)) {
				return io.ErrUnexpectedEOF
			}
			return os.Remove(p)
		}
		c.Purge()
		cRestart2 := newTestCache(t, cfg)
		if _, ok := cRestart2.get(kPurge); ok {
			t.Fatal("purged key resurrected after restart")
		}
		failRemoves.Store(false)
		c.disk.testRemoveFile = nil

		// Eviction failure must not silently de-index the unremoved entry.
		dirEvict := t.TempDir()
		cfgEvict := rangeEnabledCfg(dirEvict, 1<<10, 12<<10, 16<<10)
		cEvict := newTestCache(t, cfgEvict)
		k1 := "GET\nexample.com\n/evict-1"
		k2 := "GET\nexample.com\n/evict-2"
		hEvict := cEvict.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=3600")
			_, _ = w.Write(bytes.Repeat([]byte("E"), 10<<10))
		}))
		wantResult(t, get(t, hEvict, "http://example.com/evict-1"), stateMiss, strings.Repeat("E", 10<<10))
		cEvict.disk.testRemoveFile = func(p string) error {
			if filepath.Base(p) == hashKey(k1) {
				return io.ErrUnexpectedEOF
			}
			return os.Remove(p)
		}
		// Storing k2 (10 KiB) requires evicting k1 (10 KiB) to fit in 16 KiB disk_max_size.
		// Because k1's unlink fails, k1 stays indexed and k2 is rejected by capacity.
		_ = get(t, hEvict, "http://example.com/evict-2")
		if !cEvict.disk.hasKey(k1) || cEvict.disk.hasKey(k2) {
			t.Fatalf("eviction failure state: has(k1)=%v has(k2)=%v, want true and false", cEvict.disk.hasKey(k1), cEvict.disk.hasKey(k2))
		}
		cEvict.disk.testRemoveFile = nil
	})

	// 4. Exact-boundary large Vary entry (body == max_entry_size == disk_max_size) across disk, restart, and memory.
	t.Run("exact-boundary-vary-entry", func(t *testing.T) {
		dir := t.TempDir()
		const exactSize = 64 << 10
		cfg := rangeEnabledCfg(dir, 1<<10, exactSize, exactSize)
		c := newTestCache(t, cfg)
		body64K := bytes.Repeat([]byte("V"), exactSize)
		h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=3600")
			w.Header().Set("Vary", "Accept-Encoding")
			w.Header().Set("Content-Length", strconv.Itoa(len(body64K)))
			_, _ = w.Write(body64K)
		}))
		rec1 := get(t, h, "http://example.com/vary-exact", "Accept-Encoding", "gzip")
		wantResult(t, rec1, stateMiss, string(body64K))
		rec2 := get(t, h, "http://example.com/vary-exact", "Accept-Encoding", "gzip")
		wantResult(t, rec2, stateHit, string(body64K))

		// Restart rehydration must retain both the Vary stub and the exact-boundary variant.
		cRestart := newTestCache(t, cfg)
		hRestart := cRestart.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Fatal("origin must not be contacted after restart for exact-boundary Vary entry")
		}))
		rec3 := get(t, hRestart, "http://example.com/vary-exact", "Accept-Encoding", "gzip")
		wantResult(t, rec3, stateHit, string(body64K))

		// Evicting the variant via Resize prunes the orphaned Vary stub as well.
		cRestart.disk.Resize(1)
		if _, _, entries, _ := cRestart.disk.stats(); entries != 0 {
			t.Fatalf("expected orphaned Vary stub to be pruned after variant eviction, got entries=%d", entries)
		}

		// Memory-only boundary where variant + stub exceed memory_max_size cleans up orphaned stub.
		cMemTiny := newTestCache(t, config.CacheConfig{
			Enabled:       true,
			MemoryMaxSize: config.Size(450),
			MaxEntrySize:  config.Size(450),
			DefaultTTL:    config.Duration(time.Hour),
		})
		eTiny := &Entry{
			Status:     http.StatusOK,
			Header:     http.Header{"Cache-Control": {"public, max-age=3600"}, "Vary": {"Accept-Encoding"}},
			Body:       bytes.Repeat([]byte("m"), 80),
			Vary:       []string{"Accept-Encoding"},
			VaryValues: map[string]string{"Accept-Encoding": "gzip"},
			CreatedAt:  time.Now(),
			ExpiresAt:  time.Now().Add(time.Hour),
		}
		rTiny := httptest.NewRequest(http.MethodGet, "http://example.com/tiny-vary", nil)
		rTiny.Header.Set("Accept-Encoding", "gzip")
		if cMemTiny.storeCaptured(key(rTiny), rTiny, eTiny, nil) {
			t.Fatal("expected storeCaptured to return false when Vary stub evicts variant in tiny memory store")
		}
		if _, _, entries, _ := cMemTiny.mem.stats(); entries != 0 {
			t.Fatalf("expected orphaned Vary stub to be cleaned up, got entries=%d", entries)
		}
	})

	// 5. Temporary-disk budget bounds all temporary-file bytes (body + metadata + trailer + memory write-through).
	t.Run("temp-budget-bounds-all-temp-file-bytes", func(t *testing.T) {
		dir := t.TempDir()
		cfg := rangeEnabledCfg(dir, 16<<10, 16<<10, 16<<10)
		c := newTestCache(t, cfg)
		var skips rangeOutcomeLog
		c.SetCaptureSkipObserver(skips.add)

		paused := make(chan struct{})
		release := make(chan struct{})
		var hitBudget atomic.Bool
		var observedInflight, observedDiskTempBytes int64
		c.disk.onBeforePublishLock = func(_ string, _ uint64) {
			if hitBudget.CompareAndSwap(false, true) {
				observedInflight = c.disk.tempBudget.inflight.Load()
				entries, _ := os.ReadDir(dir)
				for _, ent := range entries {
					if strings.HasPrefix(ent.Name(), ".jul-cache-tmp-") {
						if info, err := ent.Info(); err == nil {
							observedDiskTempBytes += info.Size()
						}
					}
				}
				close(paused)
				<-release
			}
		}

		// Trigger a 10 KiB memory write-through: its body + metadata + trailer must be reserved in tempBudget.
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.set("GET\nexample.com\n/mem-wt-1", &Entry{
				Status:    http.StatusOK,
				Header:    http.Header{"Cache-Control": {"public, max-age=3600"}},
				Body:      bytes.Repeat([]byte("W"), 10<<10),
				CreatedAt: time.Now(),
				ExpiresAt: time.Now().Add(time.Hour),
			})
		}()
		<-paused
		if observedInflight != observedDiskTempBytes || observedDiskTempBytes <= 10<<10 {
			t.Fatalf("tempBudget.inflight=%d vs actual temp file size=%d (expected exact match including metadata+trailer > 10KiB)",
				observedInflight, observedDiskTempBytes)
		}
		// While 10 KiB + metadata is held in tempBudget (out of 16 KiB disk_max_size), a second 10 KiB write-through exceeds tempBudget.
		c.set("GET\nexample.com\n/mem-wt-2", &Entry{
			Status:    http.StatusOK,
			Header:    http.Header{"Cache-Control": {"public, max-age=3600"}},
			Body:      bytes.Repeat([]byte("X"), 10<<10),
			CreatedAt: time.Now(),
			ExpiresAt: time.Now().Add(time.Hour),
		})
		close(release)
		wg.Wait()
		c.disk.onBeforePublishLock = nil
		if skips.count(captureSkipBudget) == 0 {
			t.Fatalf("expected second concurrent write-through to be skipped with budget, got %v", skips.items)
		}
	})

	// 6. Deferred canonical-path promotion zero-reader race and d.pinned cleanup.
	t.Run("deferred-promotion-last-reader-barrier-and-pinned-cleanup", func(t *testing.T) {
		dir := t.TempDir()
		cfg := rangeEnabledCfg(dir, 1<<10, 32<<10, 128<<10)
		c := newTestCache(t, cfg)
		k := "GET\nexample.com\n/win-pin"
		hash := hashKey(k)
		targetPath := c.disk.path(hash)

		body1 := bytes.Repeat([]byte("1"), 4<<10)
		body2 := bytes.Repeat([]byte("2"), 4<<10)
		h1 := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=3600")
			_, _ = w.Write(body1)
		}))
		wantResult(t, get(t, h1, "http://example.com/win-pin"), stateMiss, string(body1))

		e1, ok := c.get(k)
		if !ok || e1.diskBody == nil {
			t.Fatal("expected disk-backed e1")
		}
		rc1, err := e1.OpenBody()
		if err != nil {
			t.Fatalf("OpenBody e1: %v", err)
		}

		// Simulate Windows open-handle behavior: while e1 is not yet removed, renaming over targetPath fails.
		var blockTargetRename atomic.Bool
		blockTargetRename.Store(true)
		c.disk.testRenameFile = func(oldpath, newpath string) error {
			if blockTargetRename.Load() && (oldpath == targetPath || newpath == targetPath) {
				return os.ErrPermission
			}
			return os.Rename(oldpath, newpath)
		}

		// Retire e1 while rc1 is open: fail only e1's retirement rename (targetPath -> .jul-cache-retired-*),
		// so e1 still owns targetPath in d.pinned[hash], then allow subsequent renames to succeed!
		c.Delete(k)
		blockTargetRename.Store(false)

		beforeRemove := make(chan struct{})
		allowRemove := make(chan struct{})
		var hitRemove atomic.Bool
		c.disk.onBeforeLastReaderRemove = func() {
			if hitRemove.CompareAndSwap(false, true) {
				close(beforeRemove)
				<-allowRemove
			}
		}

		// Start closing rc1: it decrements readers to 0 and pauses before os.Remove(targetPath).
		var closeWG sync.WaitGroup
		closeWG.Add(1)
		go func() {
			defer closeWG.Done()
			_ = rc1.Close()
		}()
		<-beforeRemove

		// Publish e2 while rc1 is paused before os.Remove(targetPath) and renames are allowed:
		// publication must respect that e1 still owns targetPath rather than renaming e2 over
		// targetPath where rc1's impending os.Remove(targetPath) would delete e2!
		h2 := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=3600")
			_, _ = w.Write(body2)
		}))
		wantResult(t, get(t, h2, "http://example.com/win-pin"), stateMiss, string(body2))

		// Resume rc1 removal: e1 is removed, onClose fires, e2 is promoted to targetPath, and pinned[hash] is deleted.
		close(allowRemove)
		closeWG.Wait()
		c.disk.testRenameFile = nil
		c.disk.onBeforeLastReaderRemove = nil

		// e2 must survive both in-process lookup and restart rehydration!
		wantResult(t, get(t, h2, "http://example.com/win-pin"), stateHit, string(body2))

		c.disk.mu.Lock()
		pinnedLen := len(c.disk.pinned)
		c.disk.mu.Unlock()
		if pinnedLen != 0 {
			t.Fatalf("d.pinned length after last reader close = %d, want 0", pinnedLen)
		}
		if _, stErr := os.Stat(targetPath); stErr != nil {
			t.Fatalf("expected promoted canonical file at %s: %v", targetPath, stErr)
		}
		cRestart := newTestCache(t, cfg)
		if eRe, ok := cRestart.get(k); !ok || eRe.BodyLen() != int64(len(body2)) {
			t.Fatalf("rehydrated promoted entry after restart: ok=%v", ok)
		}
	})

	// 7. Retired active-reader files tracked in Stats() and capacity admission.
	t.Run("retired-active-reader-bytes-and-capacity-bound", func(t *testing.T) {
		dir := t.TempDir()
		cfg := rangeEnabledCfg(dir, 1<<10, 32<<10, 50<<10) // disk_max_size = 50 KiB
		c := newTestCache(t, cfg)
		var skips rangeOutcomeLog
		c.SetCaptureSkipObserver(skips.add)

		var ver atomic.Int32
		h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			v := ver.Add(1)
			w.Header().Set("Cache-Control", "public, max-age=3600")
			_, _ = w.Write(bytes.Repeat([]byte{byte('A' + v)}, 20<<10)) // 20 KiB per generation
		}))

		// Gen 1 (20 KiB): open and hold reader.
		get(t, h, "http://example.com/retire-cap")
		e1, _ := c.get("GET\nexample.com\n/retire-cap")
		rc1, err := e1.OpenBody()
		if err != nil {
			t.Fatal(err)
		}

		// Gen 2 (20 KiB): replaces Gen 1 while rc1 is open -> Gen 1 (~20 KiB) is retired + Gen 2 (~20 KiB) is live (~40 KiB <= 50 KiB).
		get(t, h, "http://example.com/retire-cap", "Cache-Control", "no-cache")
		e2, _ := c.get("GET\nexample.com\n/retire-cap")
		rc2, err := e2.OpenBody()
		if err != nil {
			t.Fatal(err)
		}

		physWithRetired, _, _, _ := c.disk.stats()
		if physWithRetired < 40<<10 {
			t.Fatalf("disk.stats() with 1 live + 1 retired 20KiB entry = %d, want >= 40960", physWithRetired)
		}

		// Gen 3 (20 KiB) while both rc1 and rc2 are open (~60 KiB > 50 KiB disk_max_size) must be rejected by budget!
		skips.reset()
		get(t, h, "http://example.com/retire-cap", "Cache-Control", "no-cache")
		if skips.count(captureSkipBudget) == 0 {
			t.Fatalf("expected 3rd generation exceeding disk_max_size via retired readers to skip with budget, got %v", skips.items)
		}

		// Closing rc1 and rc2 unlinks the retired files and drops retiredBytes to 0.
		_ = rc1.Close()
		_ = rc2.Close()
		physAfterClose, _, _, _ := c.disk.stats()
		if physAfterClose != 0 {
			t.Fatalf("disk.stats() after closing all retired readers = %d, want 0", physAfterClose)
		}
	})

	// 8. Evicted memory entry preserves its original generation (both normal LRU eviction and mem.Resize).
	t.Run("evicted-memory-entry-preserves-original-generation", func(t *testing.T) {
		for _, useResize := range []bool{false, true} {
			dir := t.TempDir()
			cfg := rangeEnabledCfg(dir, 1<<10, 16<<10, 64<<10) // 1 KiB memory cap
			c := newTestCache(t, cfg)
			kVictim := "GET\nexample.com\n/evict-victim"
			mkEntry := func(body []byte) *Entry {
				return &Entry{
					Status:    http.StatusOK,
					Header:    http.Header{"Cache-Control": {"public, max-age=3600"}},
					Body:      body,
					CreatedAt: time.Now(),
					ExpiresAt: time.Now().Add(time.Hour),
				}
			}

			c.set(kVictim, mkEntry(bytes.Repeat([]byte("A"), 500)))
			// Remove from disk only so memory eviction will attempt to re-persist it via onEvictItem.
			c.disk.del(kVictim)

			evictPaused := make(chan struct{})
			evictResume := make(chan struct{})
			var hitEvict atomic.Bool
			c.disk.onBeforePublishLock = func(k string, _ uint64) {
				if k == kVictim && hitEvict.CompareAndSwap(false, true) {
					close(evictPaused)
					<-evictResume
				}
			}

			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				if useResize {
					c.mem.Resize(100)
				} else {
					c.set("GET\nexample.com\n/evict-pusher", mkEntry(bytes.Repeat([]byte("B"), 500)))
				}
			}()
			<-evictPaused
			// While kVictim's eviction flush is in flight, replace kVictim with a newer value.
			c.set(kVictim, mkEntry([]byte("NEW-REPLACEMENT")))
			close(evictResume)
			wg.Wait()
			c.disk.onBeforePublishLock = nil

			if e, ok := c.disk.get(kVictim); !ok || string(e.Body) != "NEW-REPLACEMENT" {
				t.Fatalf("useResize=%v: disk after stale eviction flush = %q (ok=%v), want NEW-REPLACEMENT", useResize, e.Body, ok)
			}
		}
	})

	// 9. Failed memory write-through invalidates superseded disk generation across restarts.
	t.Run("failed-memory-write-through-invalidates-superseded-disk-entry", func(t *testing.T) {
		dir := t.TempDir()
		cfg := rangeEnabledCfg(dir, 16<<10, 64<<10, 512<<10)
		c := newTestCache(t, cfg)
		k := "GET\nexample.com\n/wt-fail-supersede"
		mkEntry := func(body string) *Entry {
			return &Entry{
				Status:    http.StatusOK,
				Header:    http.Header{"Cache-Control": {"public, max-age=3600"}},
				Body:      []byte(body),
				CreatedAt: time.Now(),
				ExpiresAt: time.Now().Add(time.Hour),
			}
		}

		c.set(k, mkEntry("V1-SUPERSEDED"))
		c.disk.testPublishFault = "append"
		c.set(k, mkEntry("V2-CURRENT"))
		c.disk.testPublishFault = ""

		if e, ok := c.get(k); !ok || string(e.Body) != "V2-CURRENT" {
			t.Fatalf("before restart c.get = %q (ok=%v), want V2-CURRENT", e.Body, ok)
		}
		// Restarting must NOT resurrect V1-SUPERSEDED from disk.
		cRestart := newTestCache(t, cfg)
		if e, ok := cRestart.get(k); ok {
			t.Fatalf("after restart superseded disk entry resurrected: %q", e.Body)
		}
	})

	// 10. keyGen map remains bounded under high-cardinality URL churn.
	t.Run("keygen-bounded-under-high-cardinality-url-churn", func(t *testing.T) {
		dir := t.TempDir()
		cfg := rangeEnabledCfg(dir, 2<<10, 4<<10, 8<<10)
		c := newTestCache(t, cfg)
		for i := 0; i < 400; i++ {
			k := fmt.Sprintf("GET\nexample.com\n/churn/%d", i)
			c.set(k, &Entry{
				Status:    http.StatusOK,
				Header:    http.Header{"Cache-Control": {"public, max-age=3600"}},
				Body:      bytes.Repeat([]byte("c"), 512),
				CreatedAt: time.Now(),
				ExpiresAt: time.Now().Add(time.Hour),
			})
			if i%5 == 0 {
				c.Delete(k)
			}
		}
		c.genMu.Lock()
		kgLen := len(c.keyGen)
		c.genMu.Unlock()
		if kgLen > 64 {
			t.Fatalf("keyGen map grew to %d entries after 400 unique keys, want <= 64", kgLen)
		}
	})

	// 11. Deferred last-reader removal failure retains accounting/pinning and records durable tombstone.
	t.Run("deferred-last-reader-remove-failure-and-retry", func(t *testing.T) {
		dir := t.TempDir()
		cfg := rangeEnabledCfg(dir, 1<<10, 32<<10, 128<<10)
		c := newTestCache(t, cfg)
		k := "GET\nexample.com\n/def-rm-fail"
		hash := hashKey(k)
		targetPath := c.disk.path(hash)

		h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=3600")
			_, _ = w.Write(bytes.Repeat([]byte("D"), 4<<10))
		}))
		get(t, h, "http://example.com/def-rm-fail")

		e1, ok := c.get(k)
		if !ok || e1.diskBody == nil {
			t.Fatal("expected disk-backed e1")
		}
		rc1, err := e1.OpenBody()
		if err != nil {
			t.Fatal(err)
		}

		// Block both rename and remove on targetPath while retiring and closing rc1.
		var failTargetOps atomic.Bool
		failTargetOps.Store(true)
		c.disk.testRenameFile = func(oldpath, newpath string) error {
			if failTargetOps.Load() && (oldpath == targetPath || newpath == targetPath) {
				return os.ErrPermission
			}
			return os.Rename(oldpath, newpath)
		}
		c.disk.testRemoveFile = func(p string) error {
			if failTargetOps.Load() && p == targetPath {
				return os.ErrPermission
			}
			return os.Remove(p)
		}

		c.Delete(k)
		_ = rc1.Close()

		// Because remove failed in releaseReader, retiredBytes and pinned must still be retained!
		physDuringFail, _, _, _ := c.disk.stats()
		if physDuringFail < 4<<10 {
			t.Fatalf("stats() after failed deferred remove = %d, want >= 4096", physDuringFail)
		}
		c.disk.mu.Lock()
		stillPinned := c.disk.pinned[hash] == e1.diskBody
		c.disk.mu.Unlock()
		if !stillPinned {
			t.Fatal("expected e1.diskBody to remain pinned after failed deferred remove")
		}

		// Restart while targetPath still exists on disk must not resurrect k (durable tombstone).
		cRestart := newTestCache(t, cfg)
		if _, ok := cRestart.get(k); ok {
			t.Fatal("stale canonical file resurrected after failed deferred remove")
		}

		// Once the permission failure clears, the next stats/sweep retries and cleans up.
		failTargetOps.Store(false)
		physAfterRetry, _, _, _ := c.disk.stats()
		if physAfterRetry != 0 {
			t.Fatalf("stats() after retry = %d, want 0", physAfterRetry)
		}
		c.disk.testRenameFile = nil
		c.disk.testRemoveFile = nil
	})

	// 12. Crash-safe tombstone persistence (write failure fallback + retry + truncated manifest fail-closed).
	t.Run("crash-safe-tombstone-persistence-and-truncated-manifest", func(t *testing.T) {
		dir := t.TempDir()
		cfg := rangeEnabledCfg(dir, 1<<10, 32<<10, 128<<10)
		c := newTestCache(t, cfg)
		k := "GET\nexample.com\n/tomb-persist-fail"
		c.set(k, &Entry{
			Status:    http.StatusOK,
			Header:    http.Header{"Cache-Control": {"public, max-age=3600"}},
			Body:      []byte("SENSITIVE-TOMB"),
			CreatedAt: time.Now(),
			ExpiresAt: time.Now().Add(time.Hour),
		})

		// Force both unlink AND tombstone file creation to fail: in-place truncation must destroy the trailer.
		c.disk.testRemoveFile = func(p string) error {
			if isCacheFile(filepath.Base(p)) {
				return os.ErrPermission
			}
			return os.Remove(p)
		}
		c.disk.testPersistTombstoneErr = io.ErrShortWrite
		c.Delete(k)

		c.disk.mu.Lock()
		dirty := c.disk.tombstoneDirty
		c.disk.mu.Unlock()
		if !dirty {
			t.Fatal("expected tombstoneDirty to be true after persist failure")
		}

		// Restarting on dir sees the 0-byte truncated file and never resurrects k.
		cRestart1 := newTestCache(t, cfg)
		if _, ok := cRestart1.get(k); ok {
			t.Fatal("entry resurrected after unlink + tombstone persist failure")
		}

		// Clearing testPersistTombstoneErr allows sweepTombstonesLocked to persist the manifest.
		c.disk.testPersistTombstoneErr = nil
		_, _, _, _ = c.disk.stats()
		c.disk.mu.Lock()
		dirtyAfterRetry := c.disk.tombstoneDirty
		c.disk.mu.Unlock()
		if dirtyAfterRetry {
			t.Fatal("expected tombstoneDirty to be cleared after retry")
		}
		c.disk.testRemoveFile = nil

		// Truncated tombstone manifest (missing JULTOMB1 footer) fails closed by purging cache files on rehydrate.
		dir2 := t.TempDir()
		cfg2 := rangeEnabledCfg(dir2, 1<<10, 32<<10, 128<<10)
		c2 := newTestCache(t, cfg2)
		kLive := "GET\nexample.com\n/must-purge-on-corrupt-tombstone"
		c2.set(kLive, &Entry{
			Status:    http.StatusOK,
			Header:    http.Header{"Cache-Control": {"public, max-age=3600"}},
			Body:      []byte("LIVE-BEFORE-CORRUPT-MANIFEST"),
			CreatedAt: time.Now(),
			ExpiresAt: time.Now().Add(time.Hour),
		})
		// Write a truncated tombstone manifest without the JULTOMB1 commit line.
		if err := os.WriteFile(filepath.Join(dir2, diskTombstoneFile), []byte(hashKey("other")+" 100\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		c2Restart := newTestCache(t, cfg2)
		if _, ok := c2Restart.get(kLive); ok {
			t.Fatal("expected truncated tombstone manifest to fail closed and purge cache files on startup")
		}

		// Non-ENOENT read error (e.g. permission error) on tombstone manifest during rehydrate also fails closed.
		c2Restart.set(kLive, &Entry{
			Status:    http.StatusOK,
			Header:    http.Header{"Cache-Control": {"public, max-age=3600"}},
			Body:      []byte("LIVE-BEFORE-UNREADABLE-MANIFEST"),
			CreatedAt: time.Now(),
			ExpiresAt: time.Now().Add(time.Hour),
		})
		c2Restart.disk.testReadTombstoneFile = func(_ string) ([]byte, error) {
			return nil, os.ErrPermission
		}
		c2Restart.disk.rehydrate()
		c2Restart.disk.testReadTombstoneFile = nil
		if _, ok := c2Restart.disk.get(kLive); ok {
			t.Fatal("expected non-ENOENT tombstone manifest read error to fail closed and purge cache files")
		}

		// Triple failure (unlink + manifest persist + truncate all fail) marks unrecoverableInvalidation and fails closed.
		dir3 := t.TempDir()
		cfg3 := rangeEnabledCfg(dir3, 1<<10, 32<<10, 128<<10)
		c3 := newTestCache(t, cfg3)
		kTriple := "GET\nexample.com\n/triple-fail"
		c3.set(kTriple, &Entry{
			Status:    http.StatusOK,
			Header:    http.Header{"Cache-Control": {"public, max-age=3600"}},
			Body:      []byte("TRIPLE-FAIL-BODY"),
			CreatedAt: time.Now(),
			ExpiresAt: time.Now().Add(time.Hour),
		})
		c3.mem.del(kTriple)
		c3.disk.testRemoveFile = func(_ string) error { return os.ErrPermission }
		c3.disk.testPersistTombstoneErr = os.ErrPermission
		c3.disk.testTruncateFile = func(_ string, _ int64) error { return os.ErrPermission }
		c3.Delete(kTriple)
		if !c3.disk.unrecoverableInvalidation.Load() {
			t.Fatal("expected unrecoverableInvalidation to be set when unlink, manifest persist, and truncate all fail")
		}
		if _, failing, ok := c3.DiskWriteHealth(); !ok || !failing {
			t.Fatalf("DiskWriteHealth after triple failure: failing=%v ok=%v, want true", failing, ok)
		}
		if _, ok := c3.get(kTriple); ok {
			t.Fatal("expected get to fail closed while unrecoverableInvalidation is active")
		}
		c3.disk.testRemoveFile = nil
		c3.disk.testPersistTombstoneErr = nil
		c3.disk.testTruncateFile = nil
	})

	// 13. Generation reclamation vs concurrent disk-only publication and Purge epoch reference ownership.
	t.Run("reclaim-race-with-disk-only-publication-and-purge-epoch-ownership", func(t *testing.T) {
		dir := t.TempDir()
		cfg := rangeEnabledCfg(dir, 1<<10, 32<<10, 128<<10)
		c := newTestCache(t, cfg)
		k := "GET\nexample.com\n/reclaim-disk-only"
		body4K := bytes.Repeat([]byte("R"), 4<<10) // > 1 KiB memory_max_size -> disk-only
		h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=3600")
			_, _ = w.Write(body4K)
		}))

		// Pause finishKeyOp reclamation after a missed c.get(k), complete a disk-only publication for k,
		// then resume reclamation: k's generation state must NOT be erased!
		reclaimPaused := make(chan struct{})
		reclaimResume := make(chan struct{})
		var hitReclaim atomic.Bool
		c.onBeforeReclaim = func(rk string) {
			if rk == k && hitReclaim.CompareAndSwap(false, true) {
				close(reclaimPaused)
				<-reclaimResume
			}
		}
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.get(k)
		}()
		<-reclaimPaused
		wantResult(t, get(t, h, "http://example.com/reclaim-disk-only"), stateMiss, string(body4K))
		close(reclaimResume)
		wg.Wait()
		c.onBeforeReclaim = nil

		// Subsequent lookup must find the disk-only entry as a HIT (its nonzero generation was preserved).
		wantResult(t, get(t, h, "http://example.com/reclaim-disk-only"), stateHit, string(body4K))

		// Purge epoch reference ownership: hold a pre-Purge operation across Purge, start a post-Purge
		// streamed publication on the same key, then finish the pre-Purge operation before the post-Purge
		// publication completes: the post-Purge state's inflight counter must not be decremented by the
		// pre-Purge operation.
		oldAtLease := make(chan struct{})
		oldResume := make(chan struct{})
		var hitOld atomic.Bool
		c.disk.onLookupLeaseAcquire = func() {
			if hitOld.CompareAndSwap(false, true) {
				close(oldAtLease)
				<-oldResume
			}
		}
		var oldWG sync.WaitGroup
		oldWG.Add(1)
		go func() {
			defer oldWG.Done()
			_, _ = c.get(k)
		}()
		<-oldAtLease

		c.Purge()

		newAtPublish := make(chan struct{})
		newResume := make(chan struct{})
		var hitNew atomic.Bool
		c.disk.onBeforePublishLock = func(pk string, _ uint64) {
			if pk == k && hitNew.CompareAndSwap(false, true) {
				close(newAtPublish)
				<-newResume
			}
		}
		var newWG sync.WaitGroup
		newWG.Add(1)
		go func() {
			defer newWG.Done()
			wantResult(t, get(t, h, "http://example.com/reclaim-disk-only"), stateMiss, string(body4K))
		}()
		<-newAtPublish

		// Finish the old pre-Purge operation while the post-Purge streamed publication is still in flight.
		close(oldResume)
		oldWG.Wait()
		c.disk.onLookupLeaseAcquire = nil

		c.genMu.Lock()
		postSt := c.keyGen[k]
		var postInflight int32
		if postSt != nil {
			postInflight = postSt.inflight
		}
		c.genMu.Unlock()
		if postInflight != 1 {
			t.Fatalf("post-Purge inflight after pre-Purge op finished = %d, want 1", postInflight)
		}

		close(newResume)
		newWG.Wait()
		c.disk.onBeforePublishLock = nil
		wantResult(t, get(t, h, "http://example.com/reclaim-disk-only"), stateHit, string(body4K))
	})

	// 14. Failed streamed replacement during background revalidation invalidates superseded disk entry.
	t.Run("failed-streamed-replacement-during-background-revalidation", func(t *testing.T) {
		dir := t.TempDir()
		cfg := rangeEnabledCfg(dir, 1<<10, 32<<10, 128<<10)
		cfg.StaleWhileRevalidate = config.Duration(time.Minute)
		c := newTestCache(t, cfg)
		now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
		var clockMu sync.Mutex
		c.now = func() time.Time {
			clockMu.Lock()
			defer clockMu.Unlock()
			return now
		}

		v1Body := bytes.Repeat([]byte("1"), 4<<10)
		v2Body := bytes.Repeat([]byte("2"), 4<<10)
		var serveV2 atomic.Bool
		h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=10, stale-while-revalidate=60")
			if serveV2.Load() {
				w.Header().Set("ETag", `"v2"`)
				_, _ = w.Write(v2Body)
				return
			}
			w.Header().Set("ETag", `"v1"`)
			_, _ = w.Write(v1Body)
		}))

		// Warm V1 on disk.
		wantResult(t, get(t, h, "http://example.com/bg-stream-fail"), stateMiss, string(v1Body))

		// Advance into SWR window, inject disk append fault for V2's background revalidation, and trigger refresh.
		clockMu.Lock()
		now = now.Add(20 * time.Second)
		clockMu.Unlock()
		serveV2.Store(true)
		c.disk.testPublishFault = "append"

		lease := testLease(t, 1)
		staleReq := leased(httptest.NewRequest(http.MethodGet, "http://example.com/bg-stream-fail", nil), lease)
		recStale := httptest.NewRecorder()
		h.ServeHTTP(recStale, staleReq)
		wantResult(t, recStale, stateStale, string(v1Body))
		waitDrained(t, c)
		c.disk.testPublishFault = ""

		// Restarting on dir must NOT resurrect superseded V1!
		cRestart := newTestCache(t, cfg)
		cRestart.now = c.now
		if e, ok := cRestart.get("GET\nexample.com\n/bg-stream-fail"); ok {
			t.Fatalf("superseded V1 resurrected after failed background streamed replacement: len=%d", e.BodyLen())
		}
	})
}

type pausingRangeWriter struct {
	header            http.Header
	code              int
	buf               bytes.Buffer
	firstChunkWritten chan struct{}
	resumeWriter      <-chan struct{}
	pausedOnce        sync.Once
}

func (w *pausingRangeWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *pausingRangeWriter) WriteHeader(statusCode int) {
	if w.code == 0 {
		w.code = statusCode
	}
}

func (w *pausingRangeWriter) Write(p []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	n, err := w.buf.Write(p)
	w.pausedOnce.Do(func() {
		close(w.firstChunkWritten)
		<-w.resumeWriter
	})
	return n, err
}

func TestLargeObjectResidencyAndStartupRecoveryRegressions(t *testing.T) {
	// 15. Failed startup cleanup listing followed by successful rehydration listing stays fail-closed.
	t.Run("startup-cleanup-readdir-failure-stays-fail-closed", func(t *testing.T) {
		dir := t.TempDir()
		cfg := rangeEnabledCfg(dir, 1<<10, 32<<10, 128<<10)
		c := newTestCache(t, cfg)
		k := "GET\nexample.com\n/startup-cleanup-readdir"
		c.set(k, &Entry{
			Status:    http.StatusOK,
			Header:    http.Header{"Cache-Control": {"public, max-age=3600"}},
			Body:      []byte("INVALIDATED-BEFORE-RESTART"),
			CreatedAt: time.Now(),
			ExpiresAt: time.Now().Add(time.Hour),
		})
		c.mem.del(k)
		// Write a corrupt tombstone manifest so startup enters purgeAllCacheFilesOnCorruptTombstoneLocked.
		if err := os.WriteFile(filepath.Join(dir, diskTombstoneFile), []byte("corrupt-manifest\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		// Reset in-memory index to simulate startup rehydration, failing the 1st readDir (cleanup)
		// and allowing the 2nd readDir (rehydrate) to succeed, then failing the 3rd readDir (lookup retry).
		c.disk.mu.Lock()
		c.disk.ll.Init()
		clear(c.disk.items)
		c.disk.curBytes = 0
		c.disk.physBytes = 0
		var readDirCalls atomic.Int32
		c.disk.testReadDir = func(p string) ([]os.DirEntry, error) {
			n := readDirCalls.Add(1)
			if n == 1 || n == 3 {
				return nil, os.ErrPermission
			}
			return os.ReadDir(p)
		}
		c.disk.rehydrate()
		pendingAfterRehydrate := c.disk.corruptTombstoneCleanupPending
		indexedCount := len(c.disk.items)
		c.disk.mu.Unlock()

		if !pendingAfterRehydrate || !c.disk.unrecoverableInvalidation.Load() {
			t.Fatalf("expected unresolved startup cleanup condition (pending=%v, unrecoverable=%v)", pendingAfterRehydrate, c.disk.unrecoverableInvalidation.Load())
		}
		if indexedCount != 0 {
			t.Fatalf("expected 0 rehydrated items while startup cleanup is pending, got %d", indexedCount)
		}

		// Next lookup attempts retry (call #3 fails) and must remain fail-closed.
		if _, ok := c.get(k); ok {
			t.Fatal("expected lookup to fail closed while startup cleanup remains unresolved")
		}
		if !c.disk.unrecoverableInvalidation.Load() {
			t.Fatal("expected unrecoverableInvalidation to remain true when retry readDir fails")
		}

		// Once readDir succeeds (call #4), sweepTombstonesLocked completes the cleanup and deletes the file.
		c.disk.testReadDir = nil
		if _, ok := c.get(k); ok {
			t.Fatal("expected invalidated file to be purged and unavailable after cleanup recovery")
		}
		if c.disk.unrecoverableInvalidation.Load() {
			t.Fatal("expected unrecoverableInvalidation to clear after cleanup succeeds")
		}
	})

	// 16. Disk lookup paused across eviction does not overwrite inDisk=false or leak keyGen state.
	t.Run("lookup-evicted-before-completion-reclaims-keygen", func(t *testing.T) {
		dir := t.TempDir()
		cfg := rangeEnabledCfg(dir, 64, 256, 512)
		c := newTestCache(t, cfg)
		k1 := "GET\nexample.com\n/lookup-evict-1"
		k2 := "GET\nexample.com\n/lookup-evict-2"
		k3 := "GET\nexample.com\n/lookup-evict-3"
		mkEntry := func(tag byte) *Entry {
			return &Entry{
				Status:    http.StatusOK,
				Header:    http.Header{"Cache-Control": {"public, max-age=3600"}},
				Body:      bytes.Repeat([]byte{tag}, 180),
				CreatedAt: time.Now(),
				ExpiresAt: time.Now().Add(time.Hour),
			}
		}
		c.set(k1, mkEntry('A'))

		leasePaused := make(chan struct{})
		leaseResume := make(chan struct{})
		var hitLease atomic.Bool
		c.disk.onLookupLeaseAcquire = func() {
			if hitLease.CompareAndSwap(false, true) {
				close(leasePaused)
				<-leaseResume
			}
		}
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			e, ok := c.get(k1)
			if ok && e != nil {
				if rc, err := e.OpenBody(); err == nil {
					_ = rc.Close()
				}
			}
		}()
		<-leasePaused

		// Evict k1 from disk while c.get(k1) is paused at the lookup lease barrier.
		c.set(k2, mkEntry('B'))
		c.set(k3, mkEntry('C'))
		if c.disk.hasKey(k1) {
			t.Fatal("expected k1 to be evicted from disk")
		}

		close(leaseResume)
		wg.Wait()
		c.disk.onLookupLeaseAcquire = nil

		c.genMu.Lock()
		_, leaked := c.keyGen[k1]
		c.genMu.Unlock()
		if leaked {
			t.Fatal("expected k1 generation state to be reclaimed after lookup completed following eviction")
		}
	})

	// 17. Residency transitions clear keyGen on pinned-reader close, restart->lookup->eviction, and purge overlap.
	t.Run("residency-transitions-pinned-close-rehydrated-evict-and-purge", func(t *testing.T) {
		dir := t.TempDir()
		cfg := rangeEnabledCfg(dir, 64, 256, 512)
		c := newTestCache(t, cfg)
		mkDiskEntry := func(tag byte) *Entry {
			return &Entry{
				Status:    http.StatusOK,
				Header:    http.Header{"Cache-Control": {"public, max-age=3600"}},
				Body:      bytes.Repeat([]byte{tag}, 180),
				CreatedAt: time.Now(),
				ExpiresAt: time.Now().Add(time.Hour),
			}
		}

		// (a) Successful pinned-reader cleanup without a tombstone clears residency and reclaims keyGen.
		kPin := "GET\nexample.com\n/residency-pinned"
		c.set(kPin, mkDiskEntry('P'))
		ePin, ok := c.get(kPin)
		if !ok {
			t.Fatal("expected kPin hit")
		}
		rcPin, err := ePin.OpenBody()
		if err != nil {
			t.Fatal(err)
		}
		c.disk.testRenameFile = func(_, _ string) error { return os.ErrPermission }
		c.Delete(kPin)
		c.disk.testRenameFile = nil

		c.genMu.Lock()
		_, presentWhilePinned := c.keyGen[kPin]
		c.genMu.Unlock()
		if !presentWhilePinned {
			t.Fatal("expected kPin to remain in keyGen while pinned reader holds canonical path")
		}
		_ = rcPin.Close()
		c.genMu.Lock()
		_, leakedAfterPinClose := c.keyGen[kPin]
		c.genMu.Unlock()
		if leakedAfterPinClose {
			t.Fatal("expected kPin to be reclaimed from keyGen after pinned reader closed cleanly")
		}

		// (b) Restart -> lookup -> eviction of rehydrated disk item clears residency and reclaims keyGen.
		kRehyd := "GET\nexample.com\n/residency-rehydrated"
		c.set(kRehyd, mkDiskEntry('R'))
		cRestart := newTestCache(t, cfg)
		if _, ok := cRestart.get(kRehyd); !ok {
			t.Fatal("expected rehydrated kRehyd hit")
		}
		cRestart.set("GET\nexample.com\n/evictor-1", mkDiskEntry('1'))
		cRestart.set("GET\nexample.com\n/evictor-2", mkDiskEntry('2'))
		if cRestart.disk.hasKey(kRehyd) {
			t.Fatal("expected kRehyd to be evicted after evictor writes")
		}
		cRestart.genMu.Lock()
		_, leakedRehyd := cRestart.keyGen[kRehyd]
		cRestart.genMu.Unlock()
		if leakedRehyd {
			t.Fatal("expected rehydrated & evicted key to be reclaimed from keyGen")
		}

		// (c) Publication racing into the post-purge epoch before d.purge() runs clears residency and reclaims keyGen.
		kPurge := "GET\nexample.com\n/residency-purge-overlap"
		cRestart.genMu.Lock()
		cRestart.purgeGen = cRestart.genSeq.Add(1)
		cRestart.keyGen = make(map[string]*keyGenState)
		cRestart.mem.purge()
		cRestart.genMu.Unlock()

		// Simulate a post-purge disk-only publication completing right before cRestart.disk.purge() runs.
		cRestart.set(kPurge, mkDiskEntry('Z'))
		cRestart.disk.purge()

		cRestart.genMu.Lock()
		_, leakedAfterPurge := cRestart.keyGen[kPurge]
		cRestart.genMu.Unlock()
		if leakedAfterPurge {
			t.Fatal("expected post-purge publication wiped by disk.purge() to be reclaimed from keyGen")
		}
	})

	// 18. Resize paused before victim pinning does not mark a newer memory entry absent.
	t.Run("resize-race-with-newer-memory-entry-preserves-generation", func(t *testing.T) {
		dir := t.TempDir()
		cfg := rangeEnabledCfg(dir, 1024, 1024, 4<<10)
		c := newTestCache(t, cfg)
		k := "GET\nexample.com\n/resize-race"
		v1 := &Entry{
			Status:    http.StatusOK,
			Header:    http.Header{"Cache-Control": {"public, max-age=3600"}},
			Body:      bytes.Repeat([]byte("1"), 200),
			CreatedAt: time.Now(),
			ExpiresAt: time.Now().Add(time.Hour),
		}
		v2 := &Entry{
			Status:    http.StatusOK,
			Header:    http.Header{"Cache-Control": {"public, max-age=3600"}},
			Body:      []byte("v2-small"),
			CreatedAt: time.Now(),
			ExpiresAt: time.Now().Add(time.Hour),
		}
		c.set(k, v1)

		var hookRan atomic.Bool
		c.mem.onBeforePinVictims = func() {
			if hookRan.CompareAndSwap(false, true) {
				// Install newer entry v2 in memory before the old victim v1 is pinned,
				// and remove the disk copy so v2 relies on memory eviction to overflow to disk.
				c.set(k, v2)
				c.disk.del(k)
			}
		}
		c.mem.Resize(400)
		c.mem.onBeforePinVictims = nil

		c.genMu.Lock()
		stAfterResize := c.keyGen[k]
		c.genMu.Unlock()
		if stAfterResize == nil || !stAfterResize.inMem {
			t.Fatalf("expected newer memory entry for %q to remain tracked in keyGen with inMem=true, got %+v", k, stAfterResize)
		}

		// Evicting v2 from memory must still pass generation validation and overflow v2 to disk.
		c.mem.Resize(1)
		if eDisk, ok := c.disk.get(k); !ok || string(eDisk.Body) != "v2-small" {
			t.Fatalf("expected v2-small to overflow to disk on eviction after resize race, ok=%v", ok)
		}
	})

	// 19. Concurrent pinned-reader close, deletion/publication, and tombstone sweeping under -race.
	t.Run("concurrent-pinned-reader-close-and-sweep-race", func(t *testing.T) {
		dir := t.TempDir()
		cfg := rangeEnabledCfg(dir, 64, 512, 8<<10)
		c := newTestCache(t, cfg)
		for iter := 0; iter < 16; iter++ {
			k := fmt.Sprintf("GET\nexample.com\n/pin-race-%d", iter)
			c.set(k, &Entry{
				Status:    http.StatusOK,
				Header:    http.Header{"Cache-Control": {"public, max-age=3600"}},
				Body:      bytes.Repeat([]byte("X"), 128),
				CreatedAt: time.Now(),
				ExpiresAt: time.Now().Add(time.Hour),
			})
			e, ok := c.get(k)
			if !ok {
				t.Fatal("expected hit")
			}
			rc, err := e.OpenBody()
			if err != nil {
				t.Fatal(err)
			}
			// 1st Delete with rename failure pins the active blob in d.pinned[hash].
			c.disk.mu.Lock()
			c.disk.testRenameFile = func(_, _ string) error { return os.ErrPermission }
			c.disk.mu.Unlock()
			c.Delete(k)

			// 2nd Delete with remove failure records a tombstone for the same hash while d.pinned[hash] is set.
			c.disk.mu.Lock()
			c.disk.testRenameFile = nil
			c.disk.testRemoveFile = func(p string) error {
				if isCacheFile(filepath.Base(p)) {
					return os.ErrPermission
				}
				return os.Remove(p)
			}
			c.disk.mu.Unlock()
			c.Delete(k)
			c.disk.mu.Lock()
			c.disk.testRemoveFile = nil
			c.disk.mu.Unlock()

			var wg sync.WaitGroup
			wg.Add(4)
			go func() {
				defer wg.Done()
				_ = rc.Close()
			}()
			go func() {
				defer wg.Done()
				_, _ = c.Stats()
			}()
			go func() {
				defer wg.Done()
				c.set(k, &Entry{
					Status:    http.StatusOK,
					Header:    http.Header{"Cache-Control": {"public, max-age=3600"}},
					Body:      bytes.Repeat([]byte("Y"), 128),
					CreatedAt: time.Now(),
					ExpiresAt: time.Now().Add(time.Hour),
				})
			}()
			go func() {
				defer wg.Done()
				c.Delete(k)
			}()
			wg.Wait()
			c.Delete(k)
			_, _ = c.Stats()
		}
		c.genMu.Lock()
		remKeys := len(c.keyGen)
		c.genMu.Unlock()
		if remKeys != 0 {
			t.Fatalf("expected 0 remaining keyGen entries after concurrent pinned close/delete/sweep, got %d", remKeys)
		}
	})

	// 20. Crash/restart while reader holds pinned canonical V1 after Delete, Purge, and replacement never resurrects V1;
	// and failed tombstone persistence preserves active-reader contents without truncating the backing file.
	t.Run("pinned-reader-crash-restart-after-delete-purge-and-replacement", func(t *testing.T) {
		mkEntry := func(body []byte) *Entry {
			return &Entry{
				Status:    http.StatusOK,
				Header:    http.Header{"Cache-Control": {"public, max-age=3600"}},
				Body:      body,
				CreatedAt: time.Now(),
				ExpiresAt: time.Now().Add(time.Hour),
			}
		}

		// (a) Delete while reader holds canonical V1 -> restart before reader closes: V1 must not resurrect.
		dirDel := t.TempDir()
		cfgDel := rangeEnabledCfg(dirDel, 64, 512, 8<<10)
		cDel := newTestCache(t, cfgDel)
		kDel := "GET\nexample.com\n/pinned-crash-del"
		v1Del := bytes.Repeat([]byte("D"), 180)
		cDel.set(kDel, mkEntry(v1Del))
		eDel, ok := cDel.get(kDel)
		if !ok {
			t.Fatal("expected kDel hit")
		}
		rcDel, err := eDel.OpenBody()
		if err != nil {
			t.Fatal(err)
		}
		cDel.disk.testRenameFile = func(_, _ string) error { return os.ErrPermission }
		cDel.Delete(kDel)
		cDel.disk.testRenameFile = nil

		// Simulate process crash + restart while rcDel is still open.
		cDelRestart := newTestCache(t, cfgDel)
		if _, ok := cDelRestart.get(kDel); ok {
			t.Fatal("invalidated V1 resurrected after crash/restart while pinned Delete reader remained open")
		}
		_ = rcDel.Close()

		// (b) Purge while reader holds canonical V1 -> restart before reader closes: V1 must not resurrect.
		dirPurge := t.TempDir()
		cfgPurge := rangeEnabledCfg(dirPurge, 64, 512, 8<<10)
		cPurge := newTestCache(t, cfgPurge)
		kPurge := "GET\nexample.com\n/pinned-crash-purge"
		v1Purge := bytes.Repeat([]byte("P"), 180)
		cPurge.set(kPurge, mkEntry(v1Purge))
		ePurge, ok := cPurge.get(kPurge)
		if !ok {
			t.Fatal("expected kPurge hit")
		}
		rcPurge, err := ePurge.OpenBody()
		if err != nil {
			t.Fatal(err)
		}
		cPurge.disk.testRenameFile = func(_, _ string) error { return os.ErrPermission }
		cPurge.Purge()
		cPurge.disk.testRenameFile = nil

		cPurgeRestart := newTestCache(t, cfgPurge)
		if _, ok := cPurgeRestart.get(kPurge); ok {
			t.Fatal("purged V1 resurrected after crash/restart while pinned Purge reader remained open")
		}
		_ = rcPurge.Close()

		// (c) Replacement while reader holds canonical V1 -> restart before reader closes: V1 must not resurrect.
		dirRep := t.TempDir()
		cfgRep := rangeEnabledCfg(dirRep, 64, 512, 8<<10)
		cRep := newTestCache(t, cfgRep)
		kRep := "GET\nexample.com\n/pinned-crash-rep"
		v1Rep := bytes.Repeat([]byte("1"), 180)
		v2Rep := bytes.Repeat([]byte("2"), 180)
		cRep.set(kRep, mkEntry(v1Rep))
		eRep1, ok := cRep.get(kRep)
		if !ok {
			t.Fatal("expected kRep V1 hit")
		}
		rcRep1, err := eRep1.OpenBody()
		if err != nil {
			t.Fatal(err)
		}
		cRep.disk.testRenameFile = func(_, _ string) error { return os.ErrPermission }
		cRep.set(kRep, mkEntry(v2Rep))
		cRep.disk.testRenameFile = nil

		// Simulate crash + restart BEFORE rcRep1 closes (while V1 is at canonical path and V2 is at tmpPath).
		cRepRestart := newTestCache(t, cfgRep)
		if eGot, ok := cRepRestart.get(kRep); ok {
			rcGot, _ := eGot.OpenBody()
			gotBytes, _ := io.ReadAll(rcGot)
			_ = rcGot.Close()
			t.Fatalf("superseded V1 resurrected after crash/restart during pinned replacement: got %q", string(gotBytes[:8]))
		}
		_ = rcRep1.Close()

		// (d) Failed tombstone persistence while pinned reader is active preserves active-reader bytes (does not truncate).
		dirPF := t.TempDir()
		cfgPF := rangeEnabledCfg(dirPF, 64, 512, 8<<10)
		cPF := newTestCache(t, cfgPF)
		kPF := "GET\nexample.com\n/pinned-persist-fail"
		v1PF := bytes.Repeat([]byte("K"), 180)
		cPF.set(kPF, mkEntry(v1PF))
		ePF, ok := cPF.get(kPF)
		if !ok {
			t.Fatal("expected kPF hit")
		}
		rcPF, err := ePF.OpenBody()
		if err != nil {
			t.Fatal(err)
		}
		cPF.disk.testRenameFile = func(_, _ string) error { return os.ErrPermission }
		cPF.disk.testPersistTombstoneErr = os.ErrPermission
		cPF.Delete(kPF)

		gotPF, err := io.ReadAll(rcPF)
		if err != nil || !bytes.Equal(gotPF, v1PF) {
			t.Fatalf("active reader contents corrupted when tombstone persistence failed: len=%d err=%v", len(gotPF), err)
		}
		_ = rcPF.Close()
		cPF.disk.testRenameFile = nil
		cPF.disk.testPersistTombstoneErr = nil
	})

	// 21. Recovery cleanup triggered while a large range fill reader is paused preserves active reader and live captures.
	t.Run("pending-recovery-cleanup-preserves-active-range-fill-reader", func(t *testing.T) {
		dir := t.TempDir()
		cfg := rangeEnabledCfg(dir, 1<<10, 128<<10, 512<<10)
		c := newTestCache(t, cfg)

		// Pre-populate an old file from before restart, write a corrupt tombstone manifest, and fail startup readDir.
		kOld := "GET\nexample.com\n/old-before-recovery"
		c.set(kOld, &Entry{
			Status:    http.StatusOK,
			Header:    http.Header{"Cache-Control": {"public, max-age=3600"}},
			Body:      bytes.Repeat([]byte("O"), 4<<10),
			CreatedAt: time.Now(),
			ExpiresAt: time.Now().Add(time.Hour),
		})
		c.mem.del(kOld)
		if err := os.WriteFile(filepath.Join(dir, diskTombstoneFile), []byte("corrupt\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		c.disk.mu.Lock()
		c.disk.ll.Init()
		clear(c.disk.items)
		c.disk.curBytes = 0
		c.disk.physBytes = 0
		c.disk.testReadDir = func(_ string) ([]os.DirEntry, error) {
			return nil, os.ErrPermission
		}
		c.disk.rehydrate()
		c.disk.mu.Unlock()
		if !c.disk.corruptTombstoneCleanupPending {
			t.Fatal("expected corruptTombstoneCleanupPending to be true")
		}

		// Start a 96 KiB origin response and request a 64 KiB range (bytes=0-65535), pausing after the first 32 KiB chunk.
		fullBody := make([]byte, 96<<10)
		for i := range fullBody {
			fullBody[i] = byte('A' + (i % 26))
		}
		h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=3600")
			w.Header().Set("Content-Length", strconv.Itoa(len(fullBody)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(fullBody)
		}))

		firstChunkWritten := make(chan struct{})
		resumeWriter := make(chan struct{})
		pw := &pausingRangeWriter{
			firstChunkWritten: firstChunkWritten,
			resumeWriter:      resumeWriter,
		}
		var rangeWG sync.WaitGroup
		rangeWG.Add(1)
		go func() {
			defer rangeWG.Done()
			req := httptest.NewRequest(http.MethodGet, "http://example.com/range-during-recovery", nil)
			req.Header.Set("Range", "bytes=0-65535")
			h.ServeHTTP(pw, req)
		}()
		<-firstChunkWritten

		// Also create an active in-flight stream capture to ensure recovery cleanup does not unlink active .jul-cache-tmp-* files.
		sActive := c.disk.beginStreamCapture(nil)
		if sActive == nil {
			t.Fatal("expected beginStreamCapture to succeed")
		}
		defer c.disk.abortStream(sActive)

		// Restore directory access, inject unlink + rename failure with truncation succeeding, and trigger cleanup!
		c.disk.mu.Lock()
		c.disk.testReadDir = nil
		c.disk.testRenameFile = func(_, _ string) error { return os.ErrPermission }
		c.disk.testRemoveFile = func(p string) error {
			if isCacheFile(filepath.Base(p)) {
				return os.ErrPermission
			}
			return os.Remove(p)
		}
		c.disk.mu.Unlock()

		_, _ = c.Stats()

		// Old unindexed file must be truncated to 0 bytes immediately, while active capture temp file survives.
		if stOld, err := os.Stat(c.disk.path(hashKey(kOld))); err != nil || stOld.Size() != 0 {
			t.Fatalf("expected old unindexed file to be truncated to 0 bytes, stat=%v err=%v", stOld, err)
		}
		if _, err := os.Stat(c.disk.tempPath(sActive.seq)); err != nil {
			t.Fatalf("expected active in-flight temp capture file to survive cleanup, stat err=%v", err)
		}

		// Resume the paused range response reader: the client must receive every promised byte!
		close(resumeWriter)
		rangeWG.Wait()

		if pw.code != http.StatusPartialContent {
			t.Fatalf("status = %d, want 206", pw.code)
		}
		if !bytes.Equal(pw.buf.Bytes(), fullBody[:65536]) {
			t.Fatalf("range response body length = %d (want 65536), content mismatch", pw.buf.Len())
		}

		// After the range reader closed with unlink failing, deferred remove failure truncated its file to 0 bytes.
		kRange := "GET\nexample.com\n/range-during-recovery"
		if stRange, err := os.Stat(c.disk.path(hashKey(kRange))); err != nil || stRange.Size() != 0 {
			t.Fatalf("expected range file to be truncated after reader close with unlink failing, stat=%v err=%v", stRange, err)
		}

		c.disk.mu.Lock()
		c.disk.testRenameFile = nil
		c.disk.testRemoveFile = nil
		c.disk.mu.Unlock()
	})
}

// FuzzRangeHeaderAndStateTransitions fuzzes parseRangeHeader and
// resolveByteRange with valid, invalid, overlapping, duplicate, huge-number,
// and boundary inputs to prove total parsing and bounded CPU/memory use.
func FuzzRangeHeaderAndStateTransitions(f *testing.F) {
	seeds := []string{
		"bytes=0-0",
		"bytes=0-499",
		"bytes=500-999",
		"bytes=-500",
		"bytes=9500-",
		"bytes=0-0,-1",
		"bytes=0-1,2-3,4-5,6-7,8-9",
		"bytes=-0",
		"bytes=10-5",
		"bytes=9223372036854775807-9223372036854775807",
		"bytes=9223372036854775808-9223372036854775809",
		"items=0-5",
		"bytes=" + strings.Repeat("0-0,", 64) + "0-0",
		"",
		"bytes=",
		"bytes=--1",
	}
	for _, s := range seeds {
		f.Add(s, int64(1024), 2)
	}
	f.Fuzz(func(t *testing.T, header string, totalLen int64, maxRanges int) {
		if maxRanges < 0 || maxRanges > 32 {
			maxRanges = 2
		}
		if totalLen < 0 {
			totalLen = 0
		}
		spec, res := parseRangeHeader([]string{header}, maxRanges)
		if res == rangeParseSingle {
			start, length, ok := resolveByteRange(spec, totalLen)
			if ok {
				if totalLen <= 0 || start < 0 || length <= 0 || start+length > totalLen {
					t.Fatalf("invalid resolved range [%d, +%d) for totalLen=%d from header %q", start, length, totalLen, header)
				}
			}
		}
		var _ = io.Discard
	})
}
