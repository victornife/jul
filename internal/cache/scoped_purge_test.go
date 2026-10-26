// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

func boolPtr(b bool) *bool { return &b }

func TestScopedPurgeAllFiveSelectorsAndArticle42EndToEnd(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	var (
		article42Hits   atomic.Int64
		article43Hits   atomic.Int64
		commentsHits    atomic.Int64
		archiveHits     atomic.Int64
		otherHostHits   atomic.Int64
		versionSequence atomic.Int64
	)
	versionSequence.Store(1)

	c := newTestCache(t, config.CacheConfig{
		Enabled:         true,
		MemoryMaxSize:   config.Size(4 << 20),
		DiskPath:        dir,
		DiskMaxSize:     config.Size(16 << 20),
		DefaultTTL:      config.Duration(5 * time.Minute),
		TagHeaders:      []string{"Cache-Tag", "Surrogate-Key"},
		StripTagHeaders: boolPtr(true),
	})

	origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := versionSequence.Load()
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Header().Set("Vary", "Accept-Encoding")
		w.Header().Set("ETag", `"article:42"`)

		if strings.EqualFold(r.Host, "other.example.com") {
			otherHostHits.Add(1)
			w.Header().Set("Cache-Tag", "article:42, tenant:other")
			_, _ = fmt.Fprintf(w, "other-host-v%d:%s", v, r.URL.RequestURI())
			return
		}

		switch {
		case strings.HasPrefix(r.URL.Path, "/articles/42/comments"):
			commentsHits.Add(1)
			w.Header().Set("Cache-Tag", "article:42, section:comments")
			_, _ = fmt.Fprintf(w, "comments-42-v%d:%s", v, r.URL.RequestURI())
		case r.URL.Path == "/articles/42":
			article42Hits.Add(1)
			w.Header().Set("Cache-Tag", "article:42, category:news")
			w.Header().Set("Surrogate-Key", "frontpage")
			_, _ = fmt.Fprintf(w, "article-42-v%d:%s:%s", v, r.URL.RawQuery, r.Header.Get("Accept-Encoding"))
		case r.URL.Path == "/articles/43":
			article43Hits.Add(1)
			w.Header().Set("Cache-Tag", "article:43, category:news")
			_, _ = fmt.Fprintf(w, "article-43-v%d", v)
		case strings.HasPrefix(r.URL.Path, "/articles-archive"):
			archiveHits.Add(1)
			// Note: ETag is "article:42" above, proving ETag is never treated as a grouping tag!
			_, _ = fmt.Fprintf(w, "archive-v%d", v)
		default:
			_, _ = fmt.Fprintf(w, "other-v%d:%s", v, r.URL.Path)
		}
	})

	handler := c.Handler(origin)

	doReq := func(method, host, uri, acceptEnc string, extraHdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://"+host+uri, nil)
		req.Host = host
		if acceptEnc != "" {
			req.Header.Set("Accept-Encoding", acceptEnc)
		}
		for k, val := range extraHdr {
			req.Header.Set(k, val)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	// Warm the cache across GET, HEAD, Vary (gzip/br), query variants, sibling prefix, and second host.
	rec := doReq(http.MethodGet, "portal.example.com", "/articles/42?lang=en", "gzip", nil)
	if rec.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("expected initial MISS, got %q", rec.Header().Get("X-Cache"))
	}
	if rec.Header().Get("Cache-Tag") != "" || rec.Header().Get("Surrogate-Key") != "" {
		t.Fatalf("expected StripTagHeaders=true to strip tag headers on MISS, got %v", rec.Header())
	}

	_ = doReq(http.MethodGet, "portal.example.com", "/articles/42?lang=en", "br", nil)
	_ = doReq(http.MethodHead, "portal.example.com", "/articles/42?lang=en", "gzip", nil)
	_ = doReq(http.MethodGet, "portal.example.com", "/articles/42?lang=es", "gzip", nil)
	_ = doReq(http.MethodGet, "portal.example.com", "/articles/42/comments/page/1", "gzip", nil)
	_ = doReq(http.MethodGet, "portal.example.com", "/articles/43", "gzip", nil)
	// Attempt request-header tag spoofing on /articles-archive/2025: must be ignored!
	_ = doReq(http.MethodGet, "portal.example.com", "/articles-archive/2025", "gzip", map[string]string{
		"Cache-Tag": "article:42",
	})
	_ = doReq(http.MethodGet, "other.example.com", "/articles/42", "gzip", nil)

	// Verify warm hits and that tag headers remain stripped on HIT.
	hitRec := doReq(http.MethodGet, "portal.example.com", "/articles/42?lang=en", "gzip", nil)
	if hitRec.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("expected warm HIT, got %q", hitRec.Header().Get("X-Cache"))
	}
	if hitRec.Header().Get("Cache-Tag") != "" {
		t.Fatalf("expected Cache-Tag stripped on HIT")
	}

	versionSequence.Store(2)

	// ── Selector 1: Exact public URL (invalidates GET, HEAD, and all Vary for /articles/42?lang=en only) ──
	exactSel, err := ParseSelector(Selector{
		Kind: SelectorExactURL,
		URL:  "https://portal.example.com/articles/42?lang=en",
	}, nil)
	if err != nil {
		t.Fatalf("ParseSelector exact_url: %v", err)
	}
	prev1 := c.PreviewScope(exactSel, PreviewOptions{MaxSampleItems: 20})
	// 1 resource (/articles/42?lang=en), 3 representations (GET gzip, GET br, HEAD gzip)
	// Deduplicated across memory & disk!
	if prev1.MatchedResources != 1 || prev1.MatchedRepresentations != 3 {
		t.Fatalf("exact_url preview: resources=%d reps=%d, want 1 and 3", prev1.MatchedResources, prev1.MatchedRepresentations)
	}

	res1, err := c.InvalidateScope(context.Background(), exactSel, InvalidateOptions{PreviewSequence: prev1.CacheSequence})
	if err != nil || res1.State != "succeeded" || res1.InvalidatedRepresentations != 3 {
		t.Fatalf("InvalidateScope exact_url: res=%+v err=%v", res1, err)
	}

	// /articles/42?lang=en (GET gzip, GET br, HEAD gzip) must now MISS and serve v2;
	// /articles/42?lang=es must remain a warm HIT at v1!
	if r := doReq(http.MethodGet, "portal.example.com", "/articles/42?lang=en", "gzip", nil); r.Header().Get("X-Cache") != "MISS" || !strings.Contains(r.Body.String(), "v2") {
		t.Fatalf("expected /articles/42?lang=en gzip to MISS v2, got %s %q", r.Header().Get("X-Cache"), r.Body.String())
	}
	if r := doReq(http.MethodGet, "portal.example.com", "/articles/42?lang=en", "br", nil); r.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("expected /articles/42?lang=en br to MISS, got %s", r.Header().Get("X-Cache"))
	}
	if r := doReq(http.MethodHead, "portal.example.com", "/articles/42?lang=en", "gzip", nil); r.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("expected HEAD /articles/42?lang=en to MISS, got %s", r.Header().Get("X-Cache"))
	}
	if r := doReq(http.MethodGet, "portal.example.com", "/articles/42?lang=es", "gzip", nil); r.Header().Get("X-Cache") != "HIT" || !strings.Contains(r.Body.String(), "v1") {
		t.Fatalf("expected /articles/42?lang=es to stay warm HIT v1, got %s %q", r.Header().Get("X-Cache"), r.Body.String())
	}

	// ── Selector 2: Origin grouping tag `article:42` on `portal.example.com` ──
	tagSel, err := ParseSelector(Selector{
		Kind: SelectorTag,
		Host: "portal.example.com",
		Tags: []string{"article:42"},
	}, nil)
	if err != nil {
		t.Fatalf("ParseSelector tag: %v", err)
	}
	prevTag := c.PreviewScope(tagSel, PreviewOptions{MaxSampleItems: 20})
	// Matches /articles/42?lang=en (3 reps), /articles/42?lang=es (1 rep), /articles/42/comments/page/1 (1 rep) = 3 resources, 5 reps.
	// Does NOT match /articles/43, /articles-archive/2025 (despite ETag "article:42" and spoofed request header), or other.example.com!
	if prevTag.MatchedResources != 3 || prevTag.MatchedRepresentations != 5 {
		t.Fatalf("tag preview: resources=%d reps=%d, want 3 and 5", prevTag.MatchedResources, prevTag.MatchedRepresentations)
	}

	versionSequence.Store(3)
	resTag, err := c.InvalidateScope(context.Background(), tagSel, InvalidateOptions{PreviewSequence: prevTag.CacheSequence})
	if err != nil || resTag.State != "succeeded" || resTag.InvalidatedRepresentations != 5 {
		t.Fatalf("InvalidateScope tag: res=%+v err=%v", resTag, err)
	}

	// Verify /articles/42?lang=es and /articles/42/comments/page/1 now MISS and serve v3,
	// while /articles/43, /articles-archive/2025, and other.example.com/articles/42 remain warm HITs at v1!
	if r := doReq(http.MethodGet, "portal.example.com", "/articles/42?lang=es", "gzip", nil); r.Header().Get("X-Cache") != "MISS" || !strings.Contains(r.Body.String(), "v3") {
		t.Fatalf("expected tag-purged /articles/42?lang=es to MISS v3, got %s %q", r.Header().Get("X-Cache"), r.Body.String())
	}
	if r := doReq(http.MethodGet, "portal.example.com", "/articles/42/comments/page/1", "gzip", nil); r.Header().Get("X-Cache") != "MISS" || !strings.Contains(r.Body.String(), "v3") {
		t.Fatalf("expected tag-purged comments to MISS v3, got %s %q", r.Header().Get("X-Cache"), r.Body.String())
	}
	if r := doReq(http.MethodGet, "portal.example.com", "/articles/43", "gzip", nil); r.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("expected /articles/43 to stay HIT, got %s", r.Header().Get("X-Cache"))
	}
	if r := doReq(http.MethodGet, "portal.example.com", "/articles-archive/2025", "gzip", nil); r.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("expected /articles-archive/2025 (spoofed request header + ETag article:42) to stay HIT, got %s", r.Header().Get("X-Cache"))
	}
	if r := doReq(http.MethodGet, "other.example.com", "/articles/42", "gzip", nil); r.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("expected other.example.com/articles/42 to stay HIT (cross-host tag isolation), got %s", r.Header().Get("X-Cache"))
	}
	if otherHostHits.Load() != 1 {
		t.Fatalf("other.example.com origin hits = %d, want 1", otherHostHits.Load())
	}

	// ── Selector 3: Anchored glob `/articles/*/comments/**` ──
	globSel, err := ParseSelector(Selector{
		Kind:    SelectorGlob,
		Host:    "portal.example.com",
		Pattern: "/articles/*/comments/**",
	}, nil)
	if err != nil {
		t.Fatalf("ParseSelector glob: %v", err)
	}
	resGlob, err := c.InvalidateScope(context.Background(), globSel, InvalidateOptions{})
	if err != nil || resGlob.InvalidatedRepresentations != 1 {
		t.Fatalf("InvalidateScope glob: res=%+v err=%v", resGlob, err)
	}
	if r := doReq(http.MethodGet, "portal.example.com", "/articles/42?lang=es", "gzip", nil); r.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("expected /articles/42?lang=es to remain HIT after comments glob purge")
	}
	if r := doReq(http.MethodGet, "portal.example.com", "/articles/42/comments/page/1", "gzip", nil); r.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("expected /articles/42/comments/page/1 to MISS after comments glob purge")
	}

	// ── Selector 4: Host-bound path prefix `/articles` (must NOT invalidate `/articles-archive/2025`) ──
	pfxSel, err := ParseSelector(Selector{
		Kind:   SelectorPrefix,
		Host:   "portal.example.com",
		Prefix: "/articles/",
	}, nil)
	if err != nil {
		t.Fatalf("ParseSelector prefix: %v", err)
	}
	resPfx, err := c.InvalidateScope(context.Background(), pfxSel, InvalidateOptions{})
	if err != nil || resPfx.InvalidatedResources != 3 {
		t.Fatalf("InvalidateScope prefix: res=%+v err=%v", resPfx, err)
	}
	if r := doReq(http.MethodGet, "portal.example.com", "/articles-archive/2025", "gzip", nil); r.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("prefix /articles/ must NOT invalidate /articles-archive/2025")
	}
	if archiveHits.Load() != 1 {
		t.Fatalf("archive origin hits = %d, want 1", archiveHits.Load())
	}

	// ── Selector 5: Whole hostname `portal.example.com` (preserves `other.example.com`) ──
	hostSel, err := ParseSelector(Selector{
		Kind: SelectorHost,
		Host: "portal.example.com",
	}, nil)
	if err != nil {
		t.Fatalf("ParseSelector host: %v", err)
	}
	resHost, err := c.InvalidateScope(context.Background(), hostSel, InvalidateOptions{})
	if err != nil || resHost.InvalidatedRepresentations != 1 {
		t.Fatalf("InvalidateScope host: res=%+v err=%v", resHost, err)
	}
	if r := doReq(http.MethodGet, "portal.example.com", "/articles-archive/2025", "gzip", nil); r.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("expected /articles-archive/2025 to MISS after host purge")
	}
	if r := doReq(http.MethodGet, "other.example.com", "/articles/42", "gzip", nil); r.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("expected other.example.com to remain HIT after portal.example.com host purge")
	}
}

func TestScopedPurgeRangeObjectsAndPinnedActiveReaderLease(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	var originFetches atomic.Int64
	payloadV1 := strings.Repeat("AAAABBBB", 2048) // 16 KiB
	payloadV2 := strings.Repeat("CCCCDDDD", 2048)

	// MemoryMaxSize is small (4 KiB) so the 16 KiB object is stored on disk only,
	// exercising disk range serving and pinned active disk reader leases!
	c := newTestCache(t, config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(4 << 10),
		DiskPath:      dir,
		DiskMaxSize:   config.Size(16 << 20),
		MaxEntrySize:  config.Size(1 << 20),
		DefaultTTL:    config.Duration(5 * time.Minute),
		RangeEnabled:  true,
		MaxRanges:     4,
		TagHeaders:    []string{"Cache-Tag"},
	})

	origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := originFetches.Add(1)
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Header().Set("Cache-Tag", "asset:bundle")
		if n == 1 {
			_, _ = io.WriteString(w, payloadV1)
		} else {
			_, _ = io.WriteString(w, payloadV2)
		}
	})
	handler := c.Handler(origin)

	// 1. Warm the 16 KiB disk-backed entry.
	req1 := httptest.NewRequest(http.MethodGet, "http://cdn.example.com/bundle.bin", nil)
	req1.Host = "cdn.example.com"
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK || rec1.Body.String() != payloadV1 {
		t.Fatalf("initial fetch failed: code=%d len=%d", rec1.Code, rec1.Body.Len())
	}

	// 2. Verify a Range request is served as 206 Partial Content from the disk cache.
	rangeReq := httptest.NewRequest(http.MethodGet, "http://cdn.example.com/bundle.bin", nil)
	rangeReq.Host = "cdn.example.com"
	rangeReq.Header.Set("Range", "bytes=0-7")
	rangeRec := httptest.NewRecorder()
	handler.ServeHTTP(rangeRec, rangeReq)
	if rangeRec.Code != http.StatusPartialContent || rangeRec.Body.String() != "AAAABBBB" || rangeRec.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("range HIT failed: code=%d cache=%q body=%q", rangeRec.Code, rangeRec.Header().Get("X-Cache"), rangeRec.Body.String())
	}

	// 3. Pin an active reader lease on the disk entry BEFORE invalidating.
	baseKey := keyFor(http.MethodGet, "cdn.example.com", "/bundle.bin")
	diskEntry, _, ok := c.disk.getWithMemCapGen(baseKey, 0)
	if !ok || diskEntry == nil {
		t.Fatalf("expected disk getWithMemCapGen(%q) to succeed", baseKey)
	}
	leased, err := diskEntry.OpenBody()
	if err != nil || leased == nil {
		t.Fatalf("expected diskEntry.OpenBody() to succeed: %v", err)
	}

	// 4. Invalidate by tag `asset:bundle` while the reader lease is open!
	sel, err := ParseSelector(Selector{
		Kind: SelectorTag,
		Host: "cdn.example.com",
		Tags: []string{"asset:bundle"},
	}, nil)
	if err != nil {
		t.Fatalf("ParseSelector: %v", err)
	}
	invRes, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{})
	if err != nil {
		t.Fatalf("InvalidateScope: %v", err)
	}
	if invRes.State != "succeeded" || invRes.DiskEntriesRemoved != 1 || invRes.ActiveReadersRetired != 1 {
		t.Fatalf("expected 1 disk entry removed and 1 active reader retired, got %+v", invRes)
	}

	// 5. While the pinned reader still holds its lease, a new Range request must MISS and fetch payloadV2!
	rangeReq2 := httptest.NewRequest(http.MethodGet, "http://cdn.example.com/bundle.bin", nil)
	rangeReq2.Host = "cdn.example.com"
	rangeReq2.Header.Set("Range", "bytes=0-7")
	rangeRec2 := httptest.NewRecorder()
	handler.ServeHTTP(rangeRec2, rangeReq2)
	if rangeRec2.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("expected MISS after invalidation, got %q", rangeRec2.Header().Get("X-Cache"))
	}
	if !strings.HasPrefix(rangeRec2.Body.String(), "CCCCDDDD") {
		t.Fatalf("expected v2 payload after invalidation, got %q", rangeRec2.Body.String()[:16])
	}

	// 6. Meanwhile, the already-admitted pinned reader must still read the complete v1 payload without error!
	gotBytes, readErr := io.ReadAll(leased)
	if readErr != nil {
		t.Fatalf("pinned reader ReadAll failed: %v", readErr)
	}
	if string(gotBytes) != payloadV1 {
		t.Fatalf("pinned reader got corrupted data: len=%d", len(gotBytes))
	}
	_ = leased.Close()
}

func TestScopedPurgeBarrierConcurrencyFencesAndReclamation(t *testing.T) {
	t.Parallel()

	t.Run("in-flight cold fill and late-discovered origin tag are fenced from publishing", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		c := newTestCache(t, config.CacheConfig{
			Enabled:       true,
			MemoryMaxSize: config.Size(4 << 20),
			DiskPath:      dir,
			DiskMaxSize:   config.Size(16 << 20),
			DefaultTTL:    config.Duration(5 * time.Minute),
			Lock:          true,
			LockTimeout:   config.Duration(5 * time.Second),
			TagHeaders:    []string{"Cache-Tag"},
		})

		inOrigin := make(chan struct{})
		releaseOrigin := make(chan struct{})
		var originCalls atomic.Int64

		origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			call := originCalls.Add(1)
			w.Header().Set("Cache-Control", "public, max-age=300")
			// Notice: the tag `article:42` is ONLY emitted in the origin response headers,
			// so at preview/purge start time Jul does not yet know this cold miss has tag `article:42`!
			w.Header().Set("Cache-Tag", "article:42")
			if call == 1 {
				close(inOrigin)
				<-releaseOrigin
				_, _ = io.WriteString(w, "stale-pre-purge-fill")
				return
			}
			_, _ = io.WriteString(w, "fresh-post-purge-fill")
		})
		handler := c.Handler(origin)

		// Start a cold miss BEFORE the purge.
		var wg sync.WaitGroup
		rec1 := httptest.NewRecorder()
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "http://portal.example.com/new-article", nil)
			req.Host = "portal.example.com"
			handler.ServeHTTP(rec1, req)
		}()
		<-inOrigin

		// While the origin fill is in flight (not yet in cache!), preview & execute tag purge `article:42`.
		sel, err := ParseSelector(Selector{
			Kind: SelectorTag,
			Host: "portal.example.com",
			Tags: []string{"article:42"},
		}, nil)
		if err != nil {
			t.Fatalf("ParseSelector: %v", err)
		}
		prev := c.PreviewScope(sel, PreviewOptions{})
		if !prev.InflightTagUnknown {
			t.Fatalf("expected InflightTagUnknown=true, got %+v", prev)
		}

		invRes, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{PreviewSequence: prev.CacheSequence})
		if err != nil || invRes.State != "succeeded" || invRes.FencedInflightOps < 1 {
			t.Fatalf("InvalidateScope: res=%+v err=%v", invRes, err)
		}

		// Now let the pre-purge origin response finish and attempt to publish.
		close(releaseOrigin)
		wg.Wait()

		// The pre-purge caller still receives its response (client responses are preserved),
		// BUT the fenced response must NOT have been published into the cache!
		if rec1.Body.String() != "stale-pre-purge-fill" {
			t.Fatalf("expected in-flight client response preserved, got %q", rec1.Body.String())
		}
		reqAfter := httptest.NewRequest(http.MethodGet, "http://portal.example.com/new-article", nil)
		reqAfter.Host = "portal.example.com"
		recAfter := httptest.NewRecorder()
		handler.ServeHTTP(recAfter, reqAfter)
		if recAfter.Header().Get("X-Cache") != "MISS" || recAfter.Body.String() != "fresh-post-purge-fill" {
			t.Fatalf("fenced pre-purge fill leaked into cache! X-Cache=%q body=%q", recAfter.Header().Get("X-Cache"), recAfter.Body.String())
		}
	})

	t.Run("in-flight 304 validation and background SWR refresh are fenced from resurrecting entry", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		c := newTestCache(t, config.CacheConfig{
			Enabled:              true,
			MemoryMaxSize:        config.Size(4 << 20),
			DiskPath:             dir,
			DiskMaxSize:          config.Size(16 << 20),
			DefaultTTL:           config.Duration(50 * time.Millisecond),
			StaleWhileRevalidate: config.Duration(10 * time.Millisecond),
			TagHeaders:           []string{"Cache-Tag"},
		})

		inRevalidate := make(chan struct{})
		releaseRevalidate := make(chan struct{})
		var calls atomic.Int64

		origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n := calls.Add(1)
			w.Header().Set("Cache-Tag", "article:42")
			if n == 1 {
				w.Header().Set("Cache-Control", "public, max-age=1")
				w.Header().Set("ETag", `"v1"`)
				_, _ = io.WriteString(w, "v1-body")
				return
			}
			if n == 2 {
				close(inRevalidate)
				<-releaseRevalidate
				// Origin returns 304 Not Modified for the pre-purge conditional validation!
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("Cache-Control", "public, max-age=300")
			w.Header().Set("ETag", `"v2"`)
			_, _ = io.WriteString(w, "v2-body")
		})
		handler := c.Handler(origin)

		// 1. Warm v1.
		req1 := httptest.NewRequest(http.MethodGet, "http://portal.example.com/reval", nil)
		req1.Host = "portal.example.com"
		rec1 := httptest.NewRecorder()
		handler.ServeHTTP(rec1, req1)

		// Wait for TTL + SWR to expire so the next request enters synchronous 304 validation.
		time.Sleep(1100 * time.Millisecond)

		var wg sync.WaitGroup
		rec2 := httptest.NewRecorder()
		wg.Add(1)
		go func() {
			defer wg.Done()
			req2 := httptest.NewRequest(http.MethodGet, "http://portal.example.com/reval", nil)
			req2.Host = "portal.example.com"
			handler.ServeHTTP(rec2, req2)
		}()

		<-inRevalidate
		// Purge while 304 validation is in flight!
		sel, _ := ParseSelector(Selector{Kind: SelectorExactURL, Host: "portal.example.com", URI: "/reval"}, nil)
		if _, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{}); err != nil {
			t.Fatalf("InvalidateScope: %v", err)
		}
		close(releaseRevalidate)
		wg.Wait()

		// Because the 304 validation was fenced, rec2 discarded the stale 304 and refetched v2-body,
		// and rec3 serves v2-body from cache (never resurrecting v1-body)!
		if rec2.Body.String() != "v2-body" {
			t.Fatalf("fenced 304 validation served stale v1-body instead of refetching v2-body! body=%q", rec2.Body.String())
		}
		req3 := httptest.NewRequest(http.MethodGet, "http://portal.example.com/reval", nil)
		req3.Host = "portal.example.com"
		rec3 := httptest.NewRecorder()
		handler.ServeHTTP(rec3, req3)
		if rec3.Header().Get("X-Cache") != "HIT" || rec3.Body.String() != "v2-body" {
			t.Fatalf("304 validation resurrected invalidated entry! X-Cache=%q body=%q", rec3.Header().Get("X-Cache"), rec3.Body.String())
		}
	})

	t.Run("sustained concurrent churn reclaims all index and fence metadata", func(t *testing.T) {
		t.Parallel()
		c := newTestCache(t, config.CacheConfig{
			Enabled:       true,
			MemoryMaxSize: config.Size(2 << 20),
			DefaultTTL:    config.Duration(5 * time.Minute),
			TagHeaders:    []string{"Cache-Tag"},
		})

		origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=300")
			w.Header().Set("Cache-Tag", "churn-tag, host:"+r.Host)
			_, _ = io.WriteString(w, "ok")
		})
		handler := c.Handler(origin)

		var wg sync.WaitGroup
		for worker := 0; worker < 6; worker++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				for i := 0; i < 40; i++ {
					uri := fmt.Sprintf("/item/%d/%d", id, i%10)
					req := httptest.NewRequest(http.MethodGet, "http://portal.example.com"+uri, nil)
					req.Host = "portal.example.com"
					rec := httptest.NewRecorder()
					handler.ServeHTTP(rec, req)
					if i%8 == 0 {
						sel, _ := ParseSelector(Selector{
							Kind:   SelectorPrefix,
							Host:   "portal.example.com",
							Prefix: fmt.Sprintf("/item/%d", id),
						}, nil)
						_, _ = c.InvalidateScope(context.Background(), sel, InvalidateOptions{})
					}
				}
			}(worker)
		}
		wg.Wait()

		// Preview and purge all, and verify that catalog/tag/variant/fence indexes reclaim cleanly to zero.
		allSel, _ := ParseSelector(Selector{Kind: SelectorAll, All: true}, nil)
		allPrev := c.PreviewScope(allSel, PreviewOptions{MaxSampleItems: 5})
		if allPrev.MatchedResources == 0 {
			t.Fatalf("expected PreviewScope(SelectorAll) to match warm entries")
		}
		if _, err := c.InvalidateScope(context.Background(), allSel, InvalidateOptions{}); err != nil {
			t.Fatalf("InvalidateScope all: %v", err)
		}
		stats := c.IndexStats()
		if stats.BaseKeysIndexed != 0 || stats.TagLinks != 0 || stats.ActiveFences != 0 || stats.InflightOriginOps != 0 {
			t.Fatalf("expected all index/fence metadata reclaimed to 0 after full purge, got %+v", stats)
		}
	})
}

func TestScopedPurgeDiskCrashRecoveryAndInjectedFailures(t *testing.T) {
	t.Parallel()

	t.Run("interrupted variant deletion and crash recovery prune orphan variants on restart", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		c1 := newTestCache(t, config.CacheConfig{
			Enabled:       true,
			MemoryMaxSize: config.Size(1 << 20),
			DiskPath:      dir,
			DiskMaxSize:   config.Size(16 << 20),
			DefaultTTL:    config.Duration(10 * time.Minute),
			TagHeaders:    []string{"Cache-Tag"},
		})

		origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=600")
			w.Header().Set("Vary", "Accept-Encoding")
			w.Header().Set("Cache-Tag", "article:42")
			_, _ = fmt.Fprintf(w, "body-%s", r.Header.Get("Accept-Encoding"))
		})
		h1 := c1.Handler(origin)

		for _, enc := range []string{"gzip", "br"} {
			req := httptest.NewRequest(http.MethodGet, "http://portal.example.com/articles/42", nil)
			req.Host = "portal.example.com"
			req.Header.Set("Accept-Encoding", enc)
			rec := httptest.NewRecorder()
			h1.ServeHTTP(rec, req)
		}

		// Simulate a crash mid-purge where the base Vary stub was deleted from disk
		// before process termination, leaving the variant files orphaned on disk.
		baseHash := hashKey(keyFor(http.MethodGet, "portal.example.com", "/articles/42"))
		_ = os.Remove(filepath.Join(dir, baseHash))

		// Re-open Cache on the same disk directory (simulating restart recovery).
		c2 := newTestCache(t, config.CacheConfig{
			Enabled:       true,
			MemoryMaxSize: config.Size(1 << 20),
			DiskPath:      dir,
			DiskMaxSize:   config.Size(16 << 20),
			DefaultTTL:    config.Duration(10 * time.Minute),
			TagHeaders:    []string{"Cache-Tag"},
		})

		// Orphan variants without their base Vary stub must have been pruned on rehydrate!
		if stats := c2.IndexStats(); stats.BaseKeysIndexed != 0 {
			t.Fatalf("expected orphan variants pruned on restart rehydrate, got %+v", stats)
		}
	})

	t.Run("injected disk delete failure reports partial/incomplete outcome and recovers on retry", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		c := newTestCache(t, config.CacheConfig{
			Enabled:       true,
			MemoryMaxSize: config.Size(1 << 20),
			DiskPath:      dir,
			DiskMaxSize:   config.Size(16 << 20),
			DefaultTTL:    config.Duration(10 * time.Minute),
			TagHeaders:    []string{"Cache-Tag"},
		})

		origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=600")
			w.Header().Set("Cache-Tag", "article:42")
			_, _ = io.WriteString(w, "cached-payload")
		})
		h := c.Handler(origin)
		req := httptest.NewRequest(http.MethodGet, "http://portal.example.com/articles/42", nil)
		req.Host = "portal.example.com"
		h.ServeHTTP(httptest.NewRecorder(), req)

		// Inject a disk file-remove failure!
		c.disk.mu.Lock()
		c.disk.testRemoveFile = func(path string) error {
			return errors.New("simulated EIO on unlink")
		}
		c.disk.mu.Unlock()

		sel, _ := ParseSelector(Selector{
			Kind: SelectorTag,
			Host: "portal.example.com",
			Tags: []string{"article:42"},
		}, nil)
		resPartial, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{})
		if err != nil {
			t.Fatalf("InvalidateScope with injected disk error: %v", err)
		}
		if resPartial.State != "partial" || resPartial.Outcome != "partial_failure" || resPartial.InvalidationEffective != "incomplete" {
			t.Fatalf("expected partial/incomplete outcome on injected disk failure, got %+v", resPartial)
		}
		if resPartial.FailedEntries == 0 || resPartial.RecoveryGuidance == "" {
			t.Fatalf("expected FailedEntries > 0 and RecoveryGuidance populated, got %+v", resPartial)
		}

		// Clear the fault and retry at the original fence sequence!
		c.disk.mu.Lock()
		c.disk.testRemoveFile = nil
		c.disk.mu.Unlock()

		resRetry, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{ReuseFenceGen: resPartial.FenceSequence})
		if err != nil {
			t.Fatalf("retry InvalidateScope: %v", err)
		}
		if resRetry.State != "succeeded" || resRetry.InvalidationEffective != "effective" {
			t.Fatalf("expected retry after fault cleared to succeed, got %+v", resRetry)
		}
	})

	t.Run("injected tombstone persistence failure reports uncertain effectiveness", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		c := newTestCache(t, config.CacheConfig{
			Enabled:       true,
			MemoryMaxSize: config.Size(1 << 20),
			DiskPath:      dir,
			DiskMaxSize:   config.Size(16 << 20),
			DefaultTTL:    config.Duration(10 * time.Minute),
		})

		origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=600")
			_, _ = io.WriteString(w, "cached-payload")
		})
		h := c.Handler(origin)
		req := httptest.NewRequest(http.MethodGet, "http://portal.example.com/articles/42", nil)
		req.Host = "portal.example.com"
		h.ServeHTTP(httptest.NewRecorder(), req)

		c.disk.mu.Lock()
		c.disk.testPersistTombstoneErr = errors.New("simulated tombstone manifest write error")
		c.disk.mu.Unlock()

		sel, _ := ParseSelector(Selector{
			Kind: SelectorExactURL,
			Host: "portal.example.com",
			URI:  "/articles/42",
		}, nil)
		res, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{})
		if err != nil {
			t.Fatalf("InvalidateScope: %v", err)
		}
		if res.State != "partial" || res.InvalidationEffective != "uncertain" {
			t.Fatalf("expected partial + uncertain when tombstone persistence fails (even if unlink succeeds), got %+v", res)
		}
	})
}

// TestScopedPurgeNegativeOracleVerification proves that our test assertions
// detect and reject deliberately broken behavior (Prompt §7 oracle requirement):
//  1. Unfenced pre-purge fill publishing stale data into cache.
//  2. Raw string prefix matching without segment boundary (/articles matching /articles-archive).
//  3. Cross-host tag matching without hostname partition.
func TestScopedPurgeNegativeOracleVerification(t *testing.T) {
	t.Parallel()

	// Oracle check 1: verify an unfenced write (startGen > fenceGen) publishes,
	// whereas a pre-fence write (startGen < fenceGen) is rejected by isEntryFenced.
	c := newTestCache(t, config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(1 << 20),
		DefaultTTL:    config.Duration(5 * time.Minute),
		TagHeaders:    []string{"Cache-Tag"},
	})

	preStartGen := c.genSeq.Add(1)
	sel, _ := ParseSelector(Selector{Kind: SelectorPrefix, Host: "portal.example.com", Prefix: "/articles"}, nil)
	fenceGen, completeFence := c.BeginSelectorFence(sel)
	defer completeFence()
	postStartGen := c.genSeq.Add(1)

	entry := &Entry{
		Status:    http.StatusOK,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Minute),
	}
	targetKey := keyFor(http.MethodGet, "portal.example.com", "/articles/42")
	if !c.isEntryFenced(preStartGen, targetKey, entry) {
		t.Fatalf("oracle failure: pre-fence operation (startGen=%d < fenceGen=%d) was NOT fenced", preStartGen, fenceGen)
	}
	if c.isEntryFenced(postStartGen, targetKey, entry) {
		t.Fatalf("oracle failure: post-fence operation (startGen=%d > fenceGen=%d) was wrongly fenced", postStartGen, fenceGen)
	}

	// Oracle check 2: verify segment-boundary oracle rejects naive strings.HasPrefix("/articles-archive", "/articles").
	naiveMatch := strings.HasPrefix("/articles-archive/2025", "/articles")
	segmentMatch := sel.MatchesResource("portal.example.com", "/articles-archive/2025", nil)
	if !naiveMatch || segmentMatch {
		t.Fatalf("oracle failure: naiveMatch=%v segmentMatch=%v", naiveMatch, segmentMatch)
	}

	// Oracle check 3: verify cancelled context is reported as canceled/incomplete rather than succeeded.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 70; i++ {
		c.set(keyFor(http.MethodGet, "portal.example.com", "/articles/"+strconv.Itoa(i)), entry)
	}
	canceledRes, _ := c.InvalidateScope(ctx, sel, InvalidateOptions{})
	if canceledRes.State == "succeeded" || canceledRes.Outcome != "canceled" {
		t.Fatalf("oracle failure: cancelled InvalidateScope reported %+v instead of canceled", canceledRes)
	}

	// Additional coverage for SelectorKey, TagMatchAll, truncated preview, policy tag clear, and fence cap.
	keyTarget := keyFor(http.MethodGet, "portal.example.com", "/articles/0")
	keySel, err := ParseSelector(Selector{Kind: SelectorKey, Key: keyTarget}, nil)
	if err != nil {
		t.Fatalf("ParseSelector key: %v", err)
	}
	keyPrev := c.PreviewScope(keySel, PreviewOptions{MaxSampleItems: 5})
	if keyPrev.MatchedRepresentations != 1 {
		t.Fatalf("expected 1 matched representation for SelectorKey, got %+v", keyPrev)
	}
	if _, err := c.InvalidateScope(context.Background(), keySel, InvalidateOptions{}); err != nil {
		t.Fatalf("InvalidateScope SelectorKey: %v", err)
	}

	// Truncated preview with MaxScanEntries=2.
	truncPrev := c.PreviewScope(sel, PreviewOptions{MaxScanEntries: 2, MaxSampleItems: 2})
	if !truncPrev.Truncated || truncPrev.CountAccuracy != "lower_bound_truncated" {
		t.Fatalf("expected truncated preview, got %+v", truncPrev)
	}

	// TagMatchAll preview & invalidate.
	taggedEntry := &Entry{
		Status:    http.StatusOK,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Minute),
		Tags:      []string{"article:42", "locale:en"},
	}
	c.set(keyFor(http.MethodGet, "portal.example.com", "/tagged-both"), taggedEntry)
	tagAllSel, err := ParseSelector(Selector{
		Kind:     SelectorTag,
		Host:     "portal.example.com",
		Tags:     []string{"article:42", "locale:en"},
		TagMatch: TagMatchAll,
	}, nil)
	if err != nil {
		t.Fatalf("ParseSelector tagAll: %v", err)
	}
	tagAllPrev := c.PreviewScope(tagAllSel, PreviewOptions{MaxSampleItems: 5})
	if tagAllPrev.MatchedResources != 1 {
		t.Fatalf("expected 1 resource for TagMatchAll, got %+v", tagAllPrev)
	}
	if _, err := c.InvalidateScope(context.Background(), tagAllSel, InvalidateOptions{}); err != nil {
		t.Fatalf("InvalidateScope tagAll: %v", err)
	}

	// Exercise MaxActiveFences cap BEFORE calling complete() (F-L34-03),
	// and verify BeginSelectorFence completion does not fence post-fence fills (F-L12-02).
	var completers []func()
	for i := 0; i < MaxActiveFences+4; i++ {
		_, comp := c.BeginSelectorFence(sel)
		completers = append(completers, comp)
	}
	if activeCount := c.IndexStats().ActiveFences; activeCount > MaxActiveFences {
		t.Fatalf("ActiveFences = %d before completion, exceeds MaxActiveFences=%d", activeCount, MaxActiveFences)
	}
	for _, comp := range completers {
		comp()
	}

	fenceG, compCutover := c.BeginSelectorFence(sel)
	postCutoverGen := c.genSeq.Add(1)
	compCutover()
	if c.isEntryFenced(postCutoverGen, targetKey, entry) {
		t.Fatalf("BeginSelectorFence complete() falsely fenced post-cutover fill (postCutoverGen=%d > fenceG=%d)", postCutoverGen, fenceG)
	}

	// Verify ReuseFenceGen rejects mismatched selector digest (F-L12-06).
	activeFG, activeComp := c.BeginSelectorFence(sel)
	otherSel, _ := ParseSelector(Selector{Kind: SelectorHost, Host: "other.example.com"}, nil)
	if _, err := c.InvalidateScope(context.Background(), otherSel, InvalidateOptions{ReuseFenceGen: activeFG}); err == nil {
		t.Fatalf("expected error when ReuseFenceGen is used with mismatched selector digest")
	}
	activeComp()

	// Verify 35 Vary variants with distinct tags all remain indexed (F-L12-01)
	// and replacing a variant drops its superseded tag (F-L12-03).
	cVary := newTestCache(t, config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(2 << 20),
		DefaultTTL:    config.Duration(5 * time.Minute),
		TagHeaders:    []string{"Cache-Tag"},
	})
	var varyCurTag atomic.Value
	varyCurTag.Store("vtag-0")
	hVary := cVary.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Header().Set("Vary", "Accept-Language")
		w.Header().Set("Cache-Tag", varyCurTag.Load().(string))
		_, _ = io.WriteString(w, "vary-body")
	}))
	for i := 0; i < 35; i++ {
		varyCurTag.Store("vtag-" + strconv.Itoa(i))
		req := httptest.NewRequest(http.MethodGet, "http://portal.example.com/multi-vary", nil)
		req.Host = "portal.example.com"
		req.Header.Set("Accept-Language", "lang-"+strconv.Itoa(i))
		hVary.ServeHTTP(httptest.NewRecorder(), req)
	}
	sel34, _ := ParseSelector(Selector{Kind: SelectorTag, Host: "portal.example.com", Tags: []string{"vtag-34"}}, nil)
	if prev34 := cVary.PreviewScope(sel34, PreviewOptions{}); prev34.MatchedResources != 1 {
		t.Fatalf("expected 35th Vary variant tag vtag-34 to be indexed, got %+v", prev34)
	}
	// Replace lang-0 variant with a new tag and verify vtag-0 is removed from the index!
	cVary.delOne(variantKey(keyFor(http.MethodGet, "portal.example.com", "/multi-vary"), []string{"Accept-Language"}, &http.Request{Header: http.Header{"Accept-Language": []string{"lang-0"}}}))
	sel0, _ := ParseSelector(Selector{Kind: SelectorTag, Host: "portal.example.com", Tags: []string{"vtag-0"}}, nil)
	if prev0 := cVary.PreviewScope(sel0, PreviewOptions{}); prev0.MatchedResources != 0 {
		t.Fatalf("expected removed variant tag vtag-0 to be unindexed, got %+v", prev0)
	}

	// Exercise PrepareCacheUpdate disabling TagHeaders -> clears tag index on Commit().
	c.PrepareCacheUpdate(config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(1 << 20),
		DefaultTTL:    config.Duration(5 * time.Minute),
	}).Commit()

	// Exercise nil *Cache safety on PreviewScope, BeginSelectorFence, InvalidateScope, and IndexStats.
	var nilCache *Cache
	_ = nilCache.PreviewScope(sel, PreviewOptions{})
	_, nilComp := nilCache.BeginSelectorFence(sel)
	nilComp()
	if res, err := nilCache.InvalidateScope(context.Background(), sel, InvalidateOptions{}); err != nil || res.Outcome != "cache_disabled" {
		t.Fatalf("expected nilCache InvalidateScope to return cache_disabled, got %+v %v", res, err)
	}
	_ = nilCache.IndexStats()

	// Exercise MaxScanKeys overflow error and BatchSize=1 mid-batch context cancellation.
	for i := 0; i < 10; i++ {
		c.set(keyFor(http.MethodGet, "portal.example.com", "/articles/scan-"+strconv.Itoa(i)), &Entry{
			Status:     http.StatusOK,
			CreatedAt:  time.Now().Add(-2 * time.Minute),
			ExpiresAt:  time.Now().Add(-1 * time.Minute),
			StaleUntil: time.Now().Add(time.Minute), // stale freshness_state in PreviewScope!
		})
	}
	stalePrev := c.PreviewScope(sel, PreviewOptions{MaxSampleItems: 5})
	if len(stalePrev.Samples) == 0 {
		t.Fatalf("expected stale samples in PreviewScope")
	}
	if _, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{MaxScanKeys: 2}); err == nil {
		t.Fatalf("expected error when candidate keys exceed MaxScanKeys=2")
	}

	// Exercise SetInvalidationObserver and SetTagSkipObserver + countMatchingInflightLocked / detachMatchingInflightLocked.
	var observedKind, observedOutcome, observedSkip string
	c.SetInvalidationObserver(func(kind, outcome string, _, _ int) {
		observedKind = kind
		observedOutcome = outcome
	})
	c.SetTagSkipObserver(func(reason string) {
		observedSkip = reason
	})
	c.tagSkipped("invalid")
	if observedSkip != "invalid" {
		t.Fatalf("expected SetTagSkipObserver to record 'invalid', got %q", observedSkip)
	}

	// Populate a synthetic fill and revalidation call to cover countMatchingInflightLocked and detachMatchingInflightLocked across prefix and key selectors.
	fillKey := keyFor(http.MethodGet, "portal.example.com", "/articles/inflight-1")
	c.fillMu.Lock()
	if c.fills == nil {
		c.fills = make(map[string]*fillCall)
	}
	c.fills[fillKey] = &fillCall{done: make(chan struct{})}
	c.fillMu.Unlock()

	c.reMu.Lock()
	if c.calls == nil {
		c.calls = make(map[revalidateKey]*revalidateCall)
	}
	c.calls[revalidateKey{key: fillKey}] = &revalidateCall{done: make(chan struct{})}
	c.reMu.Unlock()

	infPrev := c.PreviewScope(sel, PreviewOptions{})
	if infPrev.InflightFillsMatched < 2 {
		t.Fatalf("expected InflightFillsMatched >= 2 for prefix selector, got %d", infPrev.InflightFillsMatched)
	}
	infKeySel, _ := ParseSelector(Selector{Kind: SelectorKey, Key: fillKey}, nil)
	infKeyPrev := c.PreviewScope(infKeySel, PreviewOptions{})
	if infKeyPrev.InflightFillsMatched < 2 {
		t.Fatalf("expected InflightFillsMatched >= 2 for key selector, got %d", infKeyPrev.InflightFillsMatched)
	}
	invRes, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{BatchSize: 2})
	if err != nil || invRes.FencedInflightOps < 2 || observedKind != "prefix" || observedOutcome != "succeeded" {
		t.Fatalf("unexpected InvalidateScope with inflight ops: res=%+v err=%v kind=%q outcome=%q", invRes, err, observedKind, observedOutcome)
	}

	// Cover mid-batch context cancellation (BatchSize=1 where context is canceled after batch 1)
	// and MaxTotalTagLinks index-limit skip.
	dirBatch := t.TempDir()
	cBatch := newTestCache(t, config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(1 << 20),
		DiskPath:      dirBatch,
		DiskMaxSize:   config.Size(8 << 20),
		DefaultTTL:    config.Duration(5 * time.Minute),
		TagHeaders:    []string{"Cache-Tag"},
	})
	for i := 0; i < 4; i++ {
		cBatch.set(keyFor(http.MethodGet, "portal.example.com", "/articles/batch-"+strconv.Itoa(i)), &Entry{
			Status:    http.StatusOK,
			CreatedAt: time.Now(),
			ExpiresAt: time.Now().Add(time.Minute),
			Body:      []byte("batch-body"),
		})
	}
	batchCtx, batchCancel := context.WithCancel(context.Background())
	cBatch.disk.mu.Lock()
	cBatch.disk.testRemoveFile = func(path string) error {
		batchCancel() // Cancel context during the first disk batch removal!
		return os.Remove(path)
	}
	cBatch.disk.mu.Unlock()
	midCancelRes, _ := cBatch.InvalidateScope(batchCtx, sel, InvalidateOptions{BatchSize: 1})
	if midCancelRes.State != "canceled" || midCancelRes.RemainingEntries == 0 {
		t.Fatalf("expected mid-batch canceled state with remaining entries, got %+v", midCancelRes)
	}

	// MaxTotalTagLinks cap: when totalTagLinks >= MaxTotalTagLinks, tagged entry skips storage with TagSkipIndexLimit.
	var limitReason string
	cBatch.SetTagSkipObserver(func(r string) { limitReason = r })
	cBatch.genMu.Lock()
	cBatch.totalTagLinks = MaxTotalTagLinks
	cBatch.genMu.Unlock()
	if stored := cBatch.setWithStartGen(keyFor(http.MethodGet, "portal.example.com", "/articles/over-limit"), &Entry{
		Status:    http.StatusOK,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Minute),
		Tags:      []string{"new-tag-over-limit"},
	}, 0, false); stored || limitReason != TagSkipIndexLimit {
		t.Fatalf("expected set over MaxTotalTagLinks to return false and TagSkipIndexLimit, got stored=%v reason=%q", stored, limitReason)
	}

	// Exercise ReuseFenceGen when fence is not in active slice (recreates fence at ReuseFenceGen).
	if _, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{ReuseFenceGen: 999999}); err != nil {
		t.Fatalf("InvalidateScope with synthetic ReuseFenceGen: %v", err)
	}

	// Exercise finalizeInvalidateOutcome when 0 entries removed and FailedEntries > 0 -> State="failed".
	var failedRes InvalidateResult
	failedRes.FailedEntries = 1
	finalizeInvalidateOutcome(&failedRes, false, false)
	if failedRes.State != "failed" || failedRes.Outcome != "failed" {
		t.Fatalf("expected failed state/outcome when 0 entries removed and FailedEntries=1, got %+v", failedRes)
	}
}

func TestAuditR2_TagPurgeVarySiblingValidationDoesNotResurrect(t *testing.T) {
	t.Parallel()

	c := newTestCache(t, config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(2 << 20),
		DefaultTTL:    config.Duration(5 * time.Minute),
		TagHeaders:    []string{"Cache-Tag"},
	})

	var frValidationEntered = make(chan struct{})
	var releaseFrValidation = make(chan struct{})
	var frCalls atomic.Int32
	var enCalls atomic.Int32

	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lang := r.Header.Get("Accept-Language")
		w.Header().Set("Vary", "Accept-Language")
		w.Header().Set("Cache-Control", "public, max-age=300")
		if lang == "en" {
			n := enCalls.Add(1)
			w.Header().Set("ETag", `"en-etag"`)
			w.Header().Set("Cache-Tag", "A")
			_, _ = fmt.Fprintf(w, "en-v%d", n)
			return
		}
		n := frCalls.Add(1)
		w.Header().Set("ETag", fmt.Sprintf(`"fr-etag-%d"`, n))
		w.Header().Set("Cache-Tag", "B")
		if n == 2 {
			close(frValidationEntered)
			<-releaseFrValidation
			// Return a fresh HTTP 200 replacement ("fr-v1-reval") still tagged B.
			_, _ = io.WriteString(w, "fr-v1")
			return
		}
		_, _ = fmt.Fprintf(w, "fr-v%d", n)
	}))

	doLang := func(lang string, noCache bool) (string, string) {
		req := httptest.NewRequest(http.MethodGet, "http://portal.example.com/articles/42", nil)
		req.Host = "portal.example.com"
		req.Header.Set("Accept-Language", lang)
		if noCache {
			req.Header.Set("Cache-Control", "max-age=0")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Header().Get("X-Cache"), rec.Body.String()
	}

	// 1. Warm English (tag A) and French (tag B) Vary representations.
	if st, body := doLang("en", false); st != "MISS" || body != "en-v1" {
		t.Fatalf("warm en = (%s, %q)", st, body)
	}
	if st, body := doLang("fr", false); st != "MISS" || body != "fr-v1" {
		t.Fatalf("warm fr = (%s, %q)", st, body)
	}

	// 2. Start a mandatory pre-purge French validation (max-age=0) and block its HTTP 200 completion.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = doLang("fr", true)
	}()
	<-frValidationEntered

	// 3. Purge tag A: must remove both representations of the logical resource.
	selA, err := ParseSelector(Selector{
		Kind: SelectorTag,
		Host: "portal.example.com",
		Tags: []string{"A"},
	}, nil)
	if err != nil {
		t.Fatalf("ParseSelector: %v", err)
	}
	res, err := c.InvalidateScope(context.Background(), selA, InvalidateOptions{})
	if err != nil {
		t.Fatalf("InvalidateScope: %v", err)
	}
	if res.State != "succeeded" || res.InvalidatedRepresentations != 2 {
		t.Fatalf("expected 2 representations removed for tag A, got %+v", res)
	}

	// 4. Release the old French response tagged B.
	close(releaseFrValidation)
	wg.Wait()

	// 5. The next French request MUST be a MISS (never a HIT serving the pre-purge fr-v1).
	if st, body := doLang("fr", false); st != "MISS" || body == "fr-v1" {
		t.Fatalf("expected MISS with post-purge French body, got X-Cache=%s body=%q", st, body)
	}
}

func TestAuditR2_TagPurgeMethodGroupGETAndHEAD(t *testing.T) {
	t.Parallel()

	c := newTestCache(t, config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(2 << 20),
		DefaultTTL:    config.Duration(5 * time.Minute),
		TagHeaders:    []string{"Cache-Tag"},
	})

	var headCalls atomic.Int32
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		if r.Method == http.MethodGet {
			w.Header().Set("Cache-Tag", "A")
			_, _ = io.WriteString(w, "get-body")
			return
		}
		headCalls.Add(1)
		w.Header().Set("Cache-Tag", "B")
		w.WriteHeader(http.StatusOK)
	}))

	reqGet := httptest.NewRequest(http.MethodGet, "http://portal.example.com/doc", nil)
	reqGet.Host = "portal.example.com"
	h.ServeHTTP(httptest.NewRecorder(), reqGet)

	reqHead := httptest.NewRequest(http.MethodHead, "http://portal.example.com/doc", nil)
	reqHead.Host = "portal.example.com"
	h.ServeHTTP(httptest.NewRecorder(), reqHead)

	// Confirm HEAD is warm (HIT).
	recHeadWarm := httptest.NewRecorder()
	h.ServeHTTP(recHeadWarm, reqHead)
	if recHeadWarm.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("expected warm HEAD HIT before purge, got %s", recHeadWarm.Header().Get("X-Cache"))
	}

	// Purge tag A: must invalidate both GET and HEAD representations of the logical resource.
	selA, _ := ParseSelector(Selector{
		Kind: SelectorTag,
		Host: "portal.example.com",
		Tags: []string{"A"},
	}, nil)
	res, err := c.InvalidateScope(context.Background(), selA, InvalidateOptions{})
	if err != nil {
		t.Fatalf("InvalidateScope: %v", err)
	}
	if res.InvalidatedResources != 1 || res.InvalidatedRepresentations != 2 {
		t.Fatalf("expected 1 resource and 2 representations (GET+HEAD) invalidated, got %+v", res)
	}

	recHeadAfter := httptest.NewRecorder()
	h.ServeHTTP(recHeadAfter, reqHead)
	if recHeadAfter.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("expected HEAD to be MISS after purging GET tag A, got %s", recHeadAfter.Header().Get("X-Cache"))
	}
}

func TestAuditR2_TagStrippingAcrossBypassUnsafeAndInformationalPaths(t *testing.T) {
	t.Parallel()

	stripTrue := true
	c := newTestCache(t, config.CacheConfig{
		Enabled:         true,
		MemoryMaxSize:   config.Size(2 << 20),
		DefaultTTL:      config.Duration(5 * time.Minute),
		RangeEnabled:    false,
		TagHeaders:      []string{"Cache-Tag", "Surrogate-Key"},
		StripTagHeaders: &stripTrue,
	})

	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Tag", "internal-tag-1")
		w.Header().Set("Surrogate-Key", "internal-tag-2")
		if r.URL.Path == "/interim" {
			w.WriteHeader(http.StatusEarlyHints)
		}
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))

	// Case 1: GET with Cache-Control: no-store (bypass).
	reqNoStore := httptest.NewRequest(http.MethodGet, "http://portal.example.com/item", nil)
	reqNoStore.Header.Set("Cache-Control", "no-store")
	recNoStore := httptest.NewRecorder()
	h.ServeHTTP(recNoStore, reqNoStore)
	if recNoStore.Header().Get("Cache-Tag") != "" || recNoStore.Header().Get("Surrogate-Key") != "" {
		t.Fatalf("no-store bypass leaked tag headers: %v", recNoStore.Header())
	}

	// Case 2: GET with Range when range_enabled = false (bypass).
	reqRange := httptest.NewRequest(http.MethodGet, "http://portal.example.com/item", nil)
	reqRange.Header.Set("Range", "bytes=0-1")
	recRange := httptest.NewRecorder()
	h.ServeHTTP(recRange, reqRange)
	if recRange.Header().Get("Cache-Tag") != "" || recRange.Header().Get("Surrogate-Key") != "" {
		t.Fatalf("Range bypass leaked tag headers: %v", recRange.Header())
	}

	// Case 3: POST (unsafe method) and OPTIONS (non-unsafe non-GET/HEAD method).
	for _, method := range []string{http.MethodPost, http.MethodOptions} {
		reqM := httptest.NewRequest(method, "http://portal.example.com/item", nil)
		recM := httptest.NewRecorder()
		h.ServeHTTP(recM, reqM)
		if recM.Header().Get("Cache-Tag") != "" || recM.Header().Get("Surrogate-Key") != "" {
			t.Fatalf("%s leaked tag headers: %v", method, recM.Header())
		}
	}

	// Case 4: 103 Early Hints before 200 OK.
	reqInterim := httptest.NewRequest(http.MethodGet, "http://portal.example.com/interim", nil)
	recInterim := httptest.NewRecorder()
	h.ServeHTTP(recInterim, reqInterim)
	if recInterim.Header().Get("Cache-Tag") != "" || recInterim.Header().Get("Surrogate-Key") != "" {
		t.Fatalf("1xx interim response leaked tag headers: %v", recInterim.Header())
	}
}

func TestAuditR2_CredentialHeaderRejectionBeforeTokenization(t *testing.T) {
	t.Parallel()

	c := newTestCache(t, config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(2 << 20),
		DefaultTTL:    config.Duration(5 * time.Minute),
		TagHeaders:    []string{"Cache-Tag"},
	})
	var skipReasons []string
	c.SetTagSkipObserver(func(reason string) {
		skipReasons = append(skipReasons, reason)
	})

	for i, hdrVal := range []string{
		"Bearer opaque-private-token-123",
		"Basic dXNlcjpwYXNz",
		"Digest username=admin",
	} {
		h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=300")
			w.Header().Set("Cache-Tag", hdrVal)
			_, _ = io.WriteString(w, "secret-body")
		}))
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("http://portal.example.com/cred-%d", i), nil)
		req.Host = "portal.example.com"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Body.String() != "secret-body" {
			t.Fatalf("client delivery broken for %q: code=%d body=%q", hdrVal, rec.Code, rec.Body.String())
		}
	}

	if len(skipReasons) != 3 {
		t.Fatalf("expected 3 TagSkipInvalid events, got %v", skipReasons)
	}
	if stats := c.IndexStats(); stats.TagLinks != 0 || stats.BaseKeysIndexed != 0 {
		t.Fatalf("credential headers must not be stored or indexed, got %+v", stats)
	}
}

func TestAuditR2_CutoverExactAndPrefixRouterFidelity(t *testing.T) {
	t.Parallel()

	before := &config.Config{
		Servers: []config.ServerConfig{
			{
				Listen:      ":443",
				ServerNames: []string{"portal.example.com"},
				Locations: []config.LocationConfig{
					{Match: config.MatchConfig{Type: "exact", Path: "/a+b"}, ProxyPass: "http://up-1"},
					{Match: config.MatchConfig{Type: "exact", Path: "/a*b"}, ProxyPass: "http://up-1"},
					{Match: config.MatchConfig{Type: "prefix", Path: "/a"}, ProxyPass: "http://up-1"},
				},
			},
		},
	}
	after := &config.Config{
		Servers: []config.ServerConfig{
			{
				Listen:      ":443",
				ServerNames: []string{"portal.example.com"},
				Locations: []config.LocationConfig{
					{Match: config.MatchConfig{Type: "exact", Path: "/a+b"}, ProxyPass: "http://up-2"},
					{Match: config.MatchConfig{Type: "exact", Path: "/a*b"}, ProxyPass: "http://up-2"},
					{Match: config.MatchConfig{Type: "prefix", Path: "/a"}, ProxyPass: "http://up-2"},
				},
			},
		},
	}

	sels := DeriveRouteCutoverSelectors(before, after)
	if len(sels) != 3 {
		t.Fatalf("expected 3 cutover selectors, got %d: %+v", len(sels), sels)
	}

	matchesAny := func(uri string) bool {
		for _, s := range sels {
			if s.MatchesResource("portal.example.com", uri, nil) {
				return true
			}
		}
		return false
	}

	// Exact /a+b must match literal /a+b and percent-encoded /a%2Bb.
	if !matchesAny("/a+b") || !matchesAny("/a%2Bb?x=1") {
		t.Fatalf("expected /a+b cutover selector to match /a+b and /a%%2Bb?x=1")
	}
	// Exact /a*b must match literal /a*b and NOT treat * as a wildcard if tested alone.
	if !sels[1].MatchesResource("portal.example.com", "/a*b", nil) {
		t.Fatalf("expected exact /a*b selector to match /a*b")
	}
	if sels[1].MatchesResource("portal.example.com", "/aXYZb", nil) {
		t.Fatalf("exact /a*b cutover selector must not treat * as a wildcard")
	}
	// Router prefix /a matches /a, /a/1, /assets, and /%61ssets.
	for _, p := range []string{"/a", "/a/1", "/assets", "/assets/logo.png", "/%61ssets"} {
		if !sels[2].MatchesResource("portal.example.com", p, nil) {
			t.Fatalf("expected router prefix /a cutover selector to match %q", p)
		}
	}
	if sels[2].MatchesResource("portal.example.com", "/b", nil) {
		t.Fatalf("router prefix /a cutover selector must not match /b")
	}
}

func TestAuditR3_DelayedDiskPublicationCannotCreateStaleRecoverableState(t *testing.T) {
	t.Parallel()

	diskDir := t.TempDir()
	cfg := config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(4096), // 4 KiB memory cap so 12,288-byte response streams to disk
		MaxEntrySize:  config.Size(64 << 10),
		DiskPath:      diskDir,
		DiskMaxSize:   config.Size(16 << 20),
		DefaultTTL:    config.Duration(5 * time.Minute),
	}
	c := newTestCache(t, cfg)

	publishPaused := make(chan struct{})
	releasePublish := make(chan struct{})
	var hookOnce sync.Once

	c.disk.onBeforePublishLock = func(_ string, _ uint64) {
		hookOnce.Do(func() {
			close(publishPaused)
			<-releasePublish
		})
	}

	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Header().Set("Content-Length", "12288")
		_, _ = w.Write([]byte(strings.Repeat("X", 12288)))
	}))

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		req := httptest.NewRequest(http.MethodGet, "http://portal.example.com/large.bin", nil)
		req.Host = "portal.example.com"
		h.ServeHTTP(httptest.NewRecorder(), req)
	}()

	<-publishPaused

	// Purge the exact URL while disk publication is paused before d.mu.Lock().
	sel, err := ParseSelector(Selector{
		Kind: SelectorExactURL,
		URL:  "http://portal.example.com/large.bin",
	}, nil)
	if err != nil {
		t.Fatalf("ParseSelector: %v", err)
	}
	res, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{})
	if err != nil {
		t.Fatalf("InvalidateScope: %v", err)
	}
	if res.InvalidationEffective != "effective" {
		t.Fatalf("expected effective invalidation, got %+v", res)
	}

	// Resume publication. Because d.canCommit checks the fence/generation under d.mu
	// BEFORE renamePath, the stale 12,288-byte file is never committed on disk.
	close(releasePublish)
	wg.Wait()

	// Reopen a second Cache instance against the same disk directory and verify
	// the stale 12,288-byte response is NOT recovered.
	cRecovered := newTestCache(t, cfg)
	if e, ok := cRecovered.get(keyFor(http.MethodGet, "portal.example.com", "/large.bin")); ok || e != nil {
		t.Fatalf("stale 12,288-byte response was recovered from disk after acknowledged purge!")
	}
}

func TestAuditR3_FenceCapOverflowPreservesUnrelatedHostHits(t *testing.T) {
	t.Parallel()

	c := newTestCache(t, config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(4 << 20),
		DefaultTTL:    config.Duration(5 * time.Minute),
	})

	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = io.WriteString(w, "body-"+r.Host)
	}))

	// Warm host B.
	reqB := httptest.NewRequest(http.MethodGet, "http://host-b.example.com/warm", nil)
	reqB.Host = "host-b.example.com"
	h.ServeHTTP(httptest.NewRecorder(), reqB)

	// Hold one old origin operation open so completed fences remain retained.
	opID, _ := c.beginInflightOriginOp()
	defer c.endInflightOriginOp(opID)

	// Perform 257 scoped/no-match purges for host A.
	for i := 0; i < MaxActiveFences+2; i++ {
		selA, err := ParseSelector(Selector{
			Kind: SelectorExactURL,
			Host: "host-a.example.com",
			URI:  fmt.Sprintf("/miss-%d", i),
		}, nil)
		if err != nil {
			t.Fatalf("ParseSelector: %v", err)
		}
		if _, err := c.InvalidateScope(context.Background(), selA, InvalidateOptions{}); err != nil {
			t.Fatalf("InvalidateScope %d: %v", i, err)
		}
	}

	// Host B MUST still be a warm HIT.
	recB := httptest.NewRecorder()
	h.ServeHTTP(recB, reqB)
	if recB.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("expected unrelated host-b to remain HIT after 257 host-a purges, got %s", recB.Header().Get("X-Cache"))
	}
}

func TestAuditR4_CrossBootFenceContinuationPreservesFreshPostRestartFills(t *testing.T) {
	t.Parallel()

	diskDir := t.TempDir()
	cfg := config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(64 << 10),
		MaxEntrySize:  config.Size(256 << 10),
		DiskPath:      diskDir,
		DiskMaxSize:   config.Size(16 << 20),
		DefaultTTL:    config.Duration(5 * time.Minute),
	}

	c1 := newTestCache(t, cfg)
	h1 := c1.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/uncached-") {
			w.Header().Set("Cache-Control", "no-store")
			_, _ = io.WriteString(w, "uncached")
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = io.WriteString(w, "cached-boot1")
	}))

	// Warm 2 surviving disk entries (gen <= 4).
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("http://portal.example.com/keep-%d", i), nil)
		req.Host = "portal.example.com"
		h1.ServeHTTP(httptest.NewRecorder(), req)
	}
	// Advance Boot 1 genSeq with 128 uncached origin operations.
	for i := 0; i < 128; i++ {
		opID, _ := c1.beginInflightOriginOp()
		c1.endInflightOriginOp(opID)
	}

	// Install a fence >= 131 on /articles/ and cancel midway.
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("http://portal.example.com/articles/old-%d", i), nil)
		req.Host = "portal.example.com"
		h1.ServeHTTP(httptest.NewRecorder(), req)
	}
	sel, _ := ParseSelector(Selector{
		Kind:   SelectorPrefix,
		Host:   "portal.example.com",
		Prefix: "/articles/",
	}, nil)
	ctxCancel, cancel := context.WithCancel(context.Background())
	c1.onAfterFenceInstall = func(_ NormalizedSelector, _ uint64) {
		cancel()
	}
	cancelRes, _ := c1.InvalidateScope(ctxCancel, sel, InvalidateOptions{BatchSize: 1})
	if cancelRes.State != "canceled" || cancelRes.FenceSequence < 130 {
		t.Fatalf("expected canceled purge with fence >= 130, got %+v", cancelRes)
	}
	oldFence := cancelRes.FenceSequence

	// Restart (Boot 2) against the same disk directory.
	c2 := newTestCache(t, cfg)
	var boot2OriginCalls atomic.Int32
	h2 := c2.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		boot2OriginCalls.Add(1)
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = io.WriteString(w, "fresh-boot2")
	}))

	// Fetch a new matching resource in Boot 2; verify it is a warm HIT.
	reqNew := httptest.NewRequest(http.MethodGet, "http://portal.example.com/articles/fresh", nil)
	reqNew.Host = "portal.example.com"
	h2.ServeHTTP(httptest.NewRecorder(), reqNew)
	recWarm := httptest.NewRecorder()
	h2.ServeHTTP(recWarm, reqNew)
	if recWarm.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("expected fresh Boot 2 resource to be warm HIT, got %s", recWarm.Header().Get("X-Cache"))
	}

	// Retry the old fence from Boot 1.
	retryRes, err := c2.InvalidateScope(context.Background(), sel, InvalidateOptions{
		ReuseFenceGen: oldFence,
	})
	if err != nil || retryRes.State != "succeeded" {
		t.Fatalf("expected retry with oldFence=%d to succeed, got %+v err=%v", oldFence, retryRes, err)
	}

	// Fresh Boot 2 resource MUST still be a warm HIT!
	recAfterRetry := httptest.NewRecorder()
	h2.ServeHTTP(recAfterRetry, reqNew)
	if recAfterRetry.Header().Get("X-Cache") != "HIT" || recAfterRetry.Body.String() != "fresh-boot2" {
		t.Fatalf("fresh Boot 2 resource became %s after retrying Boot 1 fence %d!", recAfterRetry.Header().Get("X-Cache"), oldFence)
	}
}

func TestAuditR4_TagDisableAndReenableRebuildsIndexAndRetiresDisabledFills(t *testing.T) {
	t.Parallel()

	c := newTestCache(t, config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(2 << 20),
		DefaultTTL:    config.Duration(5 * time.Minute),
		TagHeaders:    []string{"Cache-Tag"},
	})

	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Header().Set("Cache-Tag", "article:42")
		_, _ = io.WriteString(w, "body-"+r.URL.Path)
	}))

	// 1. Warm a tagged response: 1 index link.
	req1 := httptest.NewRequest(http.MethodGet, "http://portal.example.com/tagged-1", nil)
	req1.Host = "portal.example.com"
	h.ServeHTTP(httptest.NewRecorder(), req1)
	if links := c.IndexStats().TagLinks; links != 1 {
		t.Fatalf("expected 1 tag link after warming, got %d", links)
	}

	// 2. Disable tag ingestion via hot reload: 0 tag links while response stays warm.
	c.PrepareCacheUpdate(config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(2 << 20),
		DefaultTTL:    config.Duration(5 * time.Minute),
		TagHeaders:    nil,
	}).Commit()
	if links := c.IndexStats().TagLinks; links != 0 {
		t.Fatalf("expected 0 tag links while disabled, got %d", links)
	}

	// Also warm a response during the disabled period.
	reqDisabled := httptest.NewRequest(http.MethodGet, "http://portal.example.com/filled-while-disabled", nil)
	reqDisabled.Host = "portal.example.com"
	h.ServeHTTP(httptest.NewRecorder(), reqDisabled)

	// 3. Re-enable the same tagging policy: original tagged response must have its
	// index link restored (TagLinks == 1), while disabled-period fill is retired.
	c.PrepareCacheUpdate(config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(2 << 20),
		DefaultTTL:    config.Duration(5 * time.Minute),
		TagHeaders:    []string{"Cache-Tag"},
	}).Commit()
	if links := c.IndexStats().TagLinks; links != 1 {
		t.Fatalf("expected 1 tag link restored after re-enabling tag_headers, got %d", links)
	}

	// 4. Purge article:42: must report 1 resource invalidated and turn /tagged-1 into a MISS.
	sel, _ := ParseSelector(Selector{
		Kind: SelectorTag,
		Host: "portal.example.com",
		Tags: []string{"article:42"},
	}, nil)
	res, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{})
	if err != nil || res.State != "succeeded" || res.InvalidatedResources != 1 {
		t.Fatalf("expected succeeded with 1 resource invalidated after re-enabling tags, got %+v err=%v", res, err)
	}
	recAfter := httptest.NewRecorder()
	h.ServeHTTP(recAfter, req1)
	if recAfter.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("expected MISS for /tagged-1 after tag purge, got %s", recAfter.Header().Get("X-Cache"))
	}
}

func TestAuditR4_PreviewBoundsHostEscapeAndTombstoneSyncDirFailure(t *testing.T) {
	t.Parallel()

	diskDir := t.TempDir()
	c := newTestCache(t, config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(2 << 20),
		DiskPath:      diskDir,
		DiskMaxSize:   config.Size(8 << 20),
		DefaultTTL:    config.Duration(5 * time.Minute),
	})

	for i := 0; i < 25; i++ {
		c.set(keyFor(http.MethodGet, "portal.example.com", fmt.Sprintf("/items/%d", i)), &Entry{
			Status:    http.StatusOK,
			CreatedAt: time.Now(),
			ExpiresAt: time.Now().Add(5 * time.Minute),
			Body:      []byte("item"),
		})
	}

	// 1. PreviewScope with MaxScanEntries=5 stops at 5 scanned entries and marks Truncated=true.
	pfxSel, _ := ParseSelector(Selector{
		Kind:   SelectorPrefix,
		Host:   "portal.example.com",
		Prefix: "/items/",
	}, nil)
	prev := c.PreviewScope(pfxSel, PreviewOptions{MaxScanEntries: 5})
	if !prev.Truncated || prev.ScannedEntries != 5 {
		t.Fatalf("expected Truncated=true and ScannedEntries=5, got %+v", prev)
	}

	// 2. Prefix InvalidateScope with MaxScanKeys=5 rejects, while SelectorHost with MaxScanKeys=5 succeeds as the escape path (F13).
	if _, err := c.InvalidateScope(context.Background(), pfxSel, InvalidateOptions{MaxScanKeys: 5}); err == nil {
		t.Fatalf("expected prefix InvalidateScope above MaxScanKeys=5 to fail")
	}
	hostSel, _ := ParseSelector(Selector{
		Kind: SelectorHost,
		Host: "portal.example.com",
	}, nil)
	hostRes, err := c.InvalidateScope(context.Background(), hostSel, InvalidateOptions{MaxScanKeys: 5})
	if err != nil || hostRes.State != "succeeded" || hostRes.InvalidatedResources != 25 {
		t.Fatalf("expected SelectorHost escape path to succeed regardless of MaxScanKeys=5, got %+v err=%v", hostRes, err)
	}

	// 3. Tombstone directory sync failure surfaces as uncertain/failed rather than unconditional success (F17).
	c.set(keyFor(http.MethodGet, "portal.example.com", "/sync-fail"), &Entry{
		Status:    http.StatusOK,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(5 * time.Minute),
		Body:      []byte("sync-fail"),
	})
	c.disk.mu.Lock()
	c.disk.testRemoveFile = func(_ string) error {
		return errors.New("injected unlink failure")
	}
	c.disk.testSyncDir = func(_ string) error {
		return errors.New("injected dir fsync failure")
	}
	c.disk.mu.Unlock()

	exactSel, _ := ParseSelector(Selector{
		Kind: SelectorExactURL,
		Host: "portal.example.com",
		URI:  "/sync-fail",
	}, nil)
	syncRes, err := c.InvalidateScope(context.Background(), exactSel, InvalidateOptions{})
	if err != nil {
		t.Fatalf("InvalidateScope sync-fail: %v", err)
	}
	if syncRes.State == "succeeded" || syncRes.InvalidationEffective != "uncertain" {
		t.Fatalf("expected directory sync failure to report uncertain non-succeeded outcome, got %+v", syncRes)
	}
}

func TestAdversarialRounds_ActiveFenceRetentionHeaderOnlyStripAndHighWaterError(t *testing.T) {
	t.Parallel()

	// 1. Round 1: Active fence paused in onAfterFenceInstall must NOT be evicted when 256 completed no-match purges run.
	c := newTestCache(t, config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(2 << 20),
		DefaultTTL:    config.Duration(5 * time.Minute),
	})
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = io.WriteString(w, "v1")
	}))
	reqTarget := httptest.NewRequest(http.MethodGet, "http://host-a.example.com/target/1", nil)
	reqTarget.Host = "host-a.example.com"
	h.ServeHTTP(httptest.NewRecorder(), reqTarget)

	fenceInstalled := make(chan struct{})
	releaseActivePurge := make(chan struct{})
	var pauseOnce sync.Once
	c.onAfterFenceInstall = func(s NormalizedSelector, _ uint64) {
		if s.Kind == SelectorPrefix {
			pauseOnce.Do(func() {
				close(fenceInstalled)
				<-releaseActivePurge
			})
		}
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		pfxSel, _ := ParseSelector(Selector{
			Kind:   SelectorPrefix,
			Host:   "host-a.example.com",
			Prefix: "/target/",
		}, nil)
		_, _ = c.InvalidateScope(context.Background(), pfxSel, InvalidateOptions{})
	}()

	<-fenceInstalled

	for i := 0; i < MaxActiveFences+4; i++ {
		noMatchSel, _ := ParseSelector(Selector{
			Kind: SelectorExactURL,
			Host: "host-a.example.com",
			URI:  fmt.Sprintf("/no-match-%d", i),
		}, nil)
		_, _ = c.InvalidateScope(context.Background(), noMatchSel, InvalidateOptions{})
	}

	// While the original prefix purge is still paused before its first batch,
	// a concurrent GET /target/1 MUST be fenced (MISS, never HIT).
	recDuring := httptest.NewRecorder()
	h.ServeHTTP(recDuring, reqTarget)
	if recDuring.Header().Get("X-Cache") == "HIT" {
		t.Fatalf("active fence was evicted by completed no-match purges, allowing stale HIT!")
	}
	close(releaseActivePurge)
	wg.Wait()

	// 2. Round 2: High-water generation write failure surfaces as uncertain.
	diskDir := t.TempDir()
	cDisk := newTestCache(t, config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(2 << 20),
		DiskPath:      diskDir,
		DiskMaxSize:   config.Size(8 << 20),
		DefaultTTL:    config.Duration(5 * time.Minute),
		TagHeaders:    []string{"Cache-Tag"},
	})
	cDisk.disk.mu.Lock()
	cDisk.disk.testWriteHighWaterErr = errors.New("injected high-water write failure")
	cDisk.disk.mu.Unlock()
	hwSel, _ := ParseSelector(Selector{
		Kind: SelectorExactURL,
		Host: "host-a.example.com",
		URI:  "/hw-fail",
	}, nil)
	hwRes, err := cDisk.InvalidateScope(context.Background(), hwSel, InvalidateOptions{})
	if err != nil {
		t.Fatalf("InvalidateScope hw-fail: %v", err)
	}
	if hwRes.State == "succeeded" || hwRes.InvalidationEffective != "uncertain" {
		t.Fatalf("expected high-water persistence failure to report uncertain, got %+v", hwRes)
	}

	// 3. Round 3: Header-only handler (no WriteHeader or Write) strips Cache-Tag across miss, bypass, and unsafe/non-unsafe methods.
	stripTrue := true
	cStrip := newTestCache(t, config.CacheConfig{
		Enabled:         true,
		MemoryMaxSize:   config.Size(2 << 20),
		DefaultTTL:      config.Duration(5 * time.Minute),
		TagHeaders:      []string{"Cache-Tag"},
		StripTagHeaders: &stripTrue,
	})
	hHeaderOnly := cStrip.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Tag", "internal-header-only")
		w.Header().Set("Cache-Control", "public, max-age=300")
		// Intentionally return without calling WriteHeader or Write!
	}))
	for _, tc := range []struct {
		method  string
		noStore bool
	}{
		{http.MethodGet, false},
		{http.MethodGet, true},
		{http.MethodPost, false},
		{http.MethodOptions, false},
	} {
		req := httptest.NewRequest(tc.method, "http://portal.example.com/header-only", nil)
		if tc.noStore {
			req.Header.Set("Cache-Control", "no-store")
		}
		rec := httptest.NewRecorder()
		hHeaderOnly.ServeHTTP(rec, req)
		if got := rec.Header().Get("Cache-Tag"); got != "" {
			t.Fatalf("header-only handler leaked Cache-Tag=%q on method=%s noStore=%v", got, tc.method, tc.noStore)
		}
	}

	// 4. Round 6: Two evicted incomplete fences on the same host — completing the newer one preserves the older incomplete floor.
	cEvict := newTestCache(t, config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(2 << 20),
		DefaultTTL:    config.Duration(5 * time.Minute),
	})
	var evictComps []func()
	var evictGens []uint64
	for i := 0; i < MaxActiveFences+2; i++ {
		s, _ := ParseSelector(Selector{
			Kind: SelectorExactURL,
			Host: "host-a.example.com",
			URI:  fmt.Sprintf("/inc-%d", i),
		}, nil)
		fg, comp := cEvict.BeginSelectorFence(s)
		evictGens = append(evictGens, fg)
		evictComps = append(evictComps, comp)
	}
	// evictGens[0] and evictGens[1] were evicted into evictedIncompleteHostGens["host-a.example.com"].
	// Complete the newer evicted one (evictGens[1]) and verify incompleteHostFloorGen drops to evictGens[0] (> 0).
	evictComps[1]()
	cEvict.genMu.Lock()
	remFloor := cEvict.incompleteHostFloorGen["host-a.example.com"]
	cEvict.genMu.Unlock()
	if remFloor != evictGens[0] {
		t.Fatalf("expected incompleteHostFloorGen to remain at older evicted gen %d after completing %d, got %d", evictGens[0], evictGens[1], remFloor)
	}
	for _, comp := range evictComps {
		comp()
	}

	// 5. Exercise completed-fence coalescing with matchedResources, PreviewScope(SelectorAll), TagMatchAll truncation, and SelectorAll high-water error.
	cDisk.set(keyFor(http.MethodGet, "host-a.example.com", "/all-item"), &Entry{
		Status:    http.StatusOK,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Minute),
		Body:      []byte("all"),
		Tags:      []string{"t1", "t2"},
	})
	opHold, _ := cDisk.beginInflightOriginOp()
	sameSel, _ := ParseSelector(Selector{Kind: SelectorExactURL, Host: "host-a.example.com", URI: "/all-item"}, nil)
	_, _ = cDisk.InvalidateScope(context.Background(), sameSel, InvalidateOptions{})
	cDisk.set(keyFor(http.MethodGet, "host-a.example.com", "/all-item"), &Entry{
		Status:    http.StatusOK,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Minute),
		Body:      []byte("all-2"),
		Tags:      []string{"t1", "t2"},
	})
	fgActive, compActive := cDisk.BeginSelectorFence(sameSel)
	_, _ = cDisk.InvalidateScope(context.Background(), sameSel, InvalidateOptions{ReuseFenceGen: fgActive})
	compActive()
	cDisk.endInflightOriginOp(opHold)

	// Exercise TagMatchAll truncation and SelectorAll preview/invalidate.
	for i := 0; i < 4; i++ {
		cDisk.set(keyFor(http.MethodGet, "host-a.example.com", fmt.Sprintf("/tag-all-%d", i)), &Entry{
			Status:    http.StatusOK,
			CreatedAt: time.Now(),
			ExpiresAt: time.Now().Add(time.Minute),
			Body:      []byte("t"),
			Tags:      []string{"t1", "t2"},
		})
	}
	tagAllSel, _ := ParseSelector(Selector{Kind: SelectorTag, Host: "host-a.example.com", Tags: []string{"t1", "t2"}, TagMatch: TagMatchAll}, nil)
	_ = cDisk.PreviewScope(tagAllSel, PreviewOptions{MaxScanEntries: 1})

	allSel, _ := ParseSelector(Selector{Kind: SelectorAll, All: true}, nil)
	_ = cDisk.PreviewScope(allSel, PreviewOptions{MaxScanEntries: 1})
	_, _ = cDisk.InvalidateScope(context.Background(), allSel, InvalidateOptions{})

	// Exercise readDiskEntryMetaWithKeyGen corruption branches (invalid tag, >32 tags, >1024 tag bytes).
	for idx, badTags := range [][]string{
		{"invalid tag with spaces"},
		make([]string, MaxTagsPerEntry+1),
		{strings.Repeat("a", 120), strings.Repeat("b", 120), strings.Repeat("c", 120), strings.Repeat("d", 120), strings.Repeat("e", 120), strings.Repeat("f", 120), strings.Repeat("g", 120), strings.Repeat("h", 120), strings.Repeat("i", 120)},
	} {
		p := filepath.Join(diskDir, fmt.Sprintf("corrupt-meta-%d", idx))
		tail, _ := encodeDiskTrailerAndMetaWithKeyGen("GET\nhost-a.example.com\n/x", 1, &Entry{
			Status: http.StatusOK,
			Tags:   badTags,
		}, 0)
		_ = os.WriteFile(p, tail, 0o600)
		_, _, _, _, _, _ = readDiskEntryMetaWithKeyGen(p)
	}
}

func TestReauditR04_DeclinedUpgradeResponseStripsTagHeadersAndPreservesHijacker(t *testing.T) {
	t.Parallel()

	c, err := New(config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(2 << 20),
		DefaultTTL:    config.Duration(5 * time.Minute),
		TagHeaders:    []string{"Cache-Tag", "Surrogate-Key"},
	}, nil)
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}

	// 1. Declined upgrade returning explicit HTTP 200 OK with Cache-Tag must strip Cache-Tag.
	h200 := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Tag", "internal-declined-upgrade-tag")
		w.Header().Set("Surrogate-Key", "internal-sk")
		w.Header().Set("X-Origin", "ordinary-200")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("declined upgrade body"))
	}))
	req := httptest.NewRequest(http.MethodGet, "http://portal.example.com/ws-endpoint", nil)
	req.Host = "portal.example.com"
	req.Header.Set("Connection", "upgrade")
	req.Header.Set("Upgrade", "websocket")
	rr := httptest.NewRecorder()
	h200.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rr.Code)
	}
	if got := rr.Header().Get("Cache-Tag"); got != "" {
		t.Fatalf("expected Cache-Tag to be stripped on declined upgrade HTTP 200 response, got %q", got)
	}
	if got := rr.Header().Get("Surrogate-Key"); got != "" {
		t.Fatalf("expected Surrogate-Key to be stripped on declined upgrade HTTP 200 response, got %q", got)
	}
	if got := rr.Header().Get("X-Origin"); got != "ordinary-200" {
		t.Fatalf("expected X-Origin to be preserved, got %q", got)
	}

	// 2. Declined upgrade returning header-only implicit 200 without WriteHeader/Write also strips Cache-Tag.
	hImplicit := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Tag", "implicit-upgrade-tag")
	}))
	rrImp := httptest.NewRecorder()
	hImplicit.ServeHTTP(rrImp, req.Clone(context.Background()))
	if got := rrImp.Header().Get("Cache-Tag"); got != "" {
		t.Fatalf("expected Cache-Tag to be stripped on implicit 200 declined upgrade response, got %q", got)
	}

	// 3. Genuine accepted upgrade preserves http.Hijacker capability through the strip wrapper.
	c1, c2 := net.Pipe()
	defer func() { _ = c1.Close(); _ = c2.Close() }()
	hw := &hijackableWriter{ResponseRecorder: httptest.NewRecorder(), conn: c1}
	var sawHijacker bool
	hHijack := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Tag", "hijacked-raw")
		hj, ok := w.(http.Hijacker)
		sawHijacker = ok
		if ok {
			conn, _, hErr := hj.Hijack()
			if hErr != nil {
				t.Errorf("Hijack error: %v", hErr)
			}
			_ = conn.Close()
		}
	}))
	hHijack.ServeHTTP(hw, req.Clone(context.Background()))
	if !sawHijacker || !hw.hijacked {
		t.Fatalf("expected http.Hijacker capability to be preserved on upgrade request (sawHijacker=%v, hijacked=%v)", sawHijacker, hw.hijacked)
	}
}

func TestReauditR06_StartupTagPolicyProvenanceRetiresIncompatibleEntries(t *testing.T) {
	t.Parallel()

	diskDir := t.TempDir()
	c1, err := New(config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(2 << 20),
		DiskPath:      diskDir,
		DiskMaxSize:   config.Size(16 << 20),
		MaxEntrySize:  config.Size(4 << 20),
		DefaultTTL:    config.Duration(5 * time.Minute),
		TagHeaders:    []string{"X-Tag-A"},
	}, nil)
	if err != nil {
		t.Fatalf("cache.New c1: %v", err)
	}

	originCalls := 0
	makeHandler := func(c *Cache) http.Handler {
		return c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			originCalls++
			w.Header().Set("Cache-Control", "public, max-age=300")
			w.Header().Set("X-Tag-A", "tag-from-a")
			w.Header().Set("X-Tag-B", "tag-from-b")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, "body-%d", originCalls)
		}))
	}

	req := httptest.NewRequest(http.MethodGet, "http://portal.example.com/item", nil)
	req.Host = "portal.example.com"
	rr1 := httptest.NewRecorder()
	makeHandler(c1).ServeHTTP(rr1, req)
	if rr1.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("expected initial MISS, got %q", rr1.Header().Get("X-Cache"))
	}

	// 1. Restart with the SAME tag_headers policy (["X-Tag-A"]): warm disk entry and its tag are retained.
	c2, err := New(config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(2 << 20),
		DiskPath:      diskDir,
		DiskMaxSize:   config.Size(16 << 20),
		MaxEntrySize:  config.Size(4 << 20),
		DefaultTTL:    config.Duration(5 * time.Minute),
		TagHeaders:    []string{"X-Tag-A"},
	}, nil)
	if err != nil {
		t.Fatalf("cache.New c2: %v", err)
	}
	rr2 := httptest.NewRecorder()
	makeHandler(c2).ServeHTTP(rr2, req.Clone(context.Background()))
	if rr2.Header().Get("X-Cache") != "HIT" || rr2.Body.String() != "body-1" {
		t.Fatalf("expected warm HIT with same tag_headers policy across restart, got X-Cache=%q body=%q", rr2.Header().Get("X-Cache"), rr2.Body.String())
	}
	selTagA, _ := ParseSelector(Selector{Kind: SelectorTag, Host: "portal.example.com", Tags: []string{"tag-from-a"}}, nil)
	if prev := c2.PreviewScope(selTagA, PreviewOptions{}); prev.MatchedResources != 1 {
		t.Fatalf("expected tag-from-a to remain indexed on restart with same policy, got %d", prev.MatchedResources)
	}

	// 2. Restart with a DIFFERENT tag_headers policy (["X-Tag-B"]):
	// Entry ingested under ["X-Tag-A"] must NOT be stamped with fabricated ["X-Tag-B"] provenance;
	// it must be retired on startup and refetched under ["X-Tag-B"].
	c3, err := New(config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(2 << 20),
		DiskPath:      diskDir,
		DiskMaxSize:   config.Size(16 << 20),
		MaxEntrySize:  config.Size(4 << 20),
		DefaultTTL:    config.Duration(5 * time.Minute),
		TagHeaders:    []string{"X-Tag-B"},
	}, nil)
	if err != nil {
		t.Fatalf("cache.New c3: %v", err)
	}
	if prev := c3.PreviewScope(selTagA, PreviewOptions{}); prev.MatchedResources != 0 {
		t.Fatalf("expected tag-from-a from old policy to NOT be indexed under X-Tag-B, got %d", prev.MatchedResources)
	}
	rr3 := httptest.NewRecorder()
	makeHandler(c3).ServeHTTP(rr3, req.Clone(context.Background()))
	if rr3.Header().Get("X-Cache") != "MISS" || rr3.Body.String() != "body-2" {
		t.Fatalf("expected incompatible disk entry to be retired on startup (MISS body-2), got X-Cache=%q body=%q", rr3.Header().Get("X-Cache"), rr3.Body.String())
	}
	selTagB, _ := ParseSelector(Selector{Kind: SelectorTag, Host: "portal.example.com", Tags: []string{"tag-from-b"}}, nil)
	if prev := c3.PreviewScope(selTagB, PreviewOptions{}); prev.MatchedResources != 1 {
		t.Fatalf("expected refetched entry to be indexed under tag-from-b, got %d", prev.MatchedResources)
	}

	// 3. Also verify Vary stub + variants are retired together on startup when tag_headers changes (AR-07).
	hVary := c3.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Header().Set("Vary", "Accept-Language")
		w.Header().Set("X-Tag-B", "vary-tag-b")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "vary-%s", r.Header.Get("Accept-Language"))
	}))
	for _, lang := range []string{"en", "es"} {
		vReq := httptest.NewRequest(http.MethodGet, "http://portal.example.com/vary-item", nil)
		vReq.Host = "portal.example.com"
		vReq.Header.Set("Accept-Language", lang)
		hVary.ServeHTTP(httptest.NewRecorder(), vReq)
	}
	c4, err := New(config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(2 << 20),
		DiskPath:      diskDir,
		DiskMaxSize:   config.Size(16 << 20),
		MaxEntrySize:  config.Size(4 << 20),
		DefaultTTL:    config.Duration(5 * time.Minute),
		TagHeaders:    []string{"X-Tag-C"},
	}, nil)
	if err != nil {
		t.Fatalf("cache.New c4: %v", err)
	}
	if stats := c4.IndexStats(); stats.BaseKeysIndexed != 0 {
		t.Fatalf("expected all Vary variants and stubs from incompatible tag policy to be retired on startup, got BaseKeysIndexed=%d", stats.BaseKeysIndexed)
	}
}

func TestReauditR07_CutoverDerivationMatchesExplicitPortAuthoritiesAndFenceError(t *testing.T) {
	t.Parallel()

	diskDir := t.TempDir()
	c, err := New(config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(2 << 20),
		DiskPath:      diskDir,
		DiskMaxSize:   config.Size(16 << 20),
		MaxEntrySize:  config.Size(4 << 20),
		DefaultTTL:    config.Duration(5 * time.Minute),
	}, nil)
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}

	// Cache entries under both bare portal.example.com and explicit-port portal.example.com:8080,
	// plus an unrelated host.
	for _, h := range []string{"portal.example.com", "portal.example.com:8080", "other.example.com:8080"} {
		c.set(keyFor(http.MethodGet, h, "/api/v1/items?page=1"), &Entry{
			Status:    http.StatusOK,
			CreatedAt: time.Now(),
			ExpiresAt: time.Now().Add(5 * time.Minute),
			Body:      []byte("items-" + h),
		})
	}

	routeID := "route-api"
	before := &config.Config{
		Servers: []config.ServerConfig{
			{
				Listen:      ":8080",
				ServerNames: []string{"portal.example.com"},
				Locations: []config.LocationConfig{
					{
						RouteID:   &routeID,
						Match:     config.MatchConfig{Path: "/api/v1", Type: "prefix"},
						ProxyPass: "http://127.0.0.1:9001",
					},
				},
			},
		},
	}
	after := &config.Config{
		Servers: []config.ServerConfig{
			{
				Listen:      ":8080",
				ServerNames: []string{"portal.example.com"},
				Locations: []config.LocationConfig{
					{
						RouteID:   &routeID,
						Match:     config.MatchConfig{Path: "/api/v1", Type: "prefix"},
						ProxyPass: "http://127.0.0.1:9002",
					},
				},
			},
		},
	}

	sels := DeriveRouteCutoverSelectors(before, after)
	if len(sels) != 1 {
		t.Fatalf("expected 1 derived cutover selector, got %d", len(sels))
	}
	if !sels[0].MatchesResource("portal.example.com:8080", "/api/v1/items?page=1", nil) {
		t.Fatalf("expected route-derived cutover selector to match explicit-port authority portal.example.com:8080")
	}
	if !sels[0].MatchesResource("portal.example.com", "/api/v1/items?page=1", nil) {
		t.Fatalf("expected route-derived cutover selector to match bare authority portal.example.com")
	}

	res, err := c.InvalidateScope(context.Background(), sels[0], InvalidateOptions{})
	if err != nil {
		t.Fatalf("InvalidateScope cutover selector: %v", err)
	}
	if res.InvalidatedResources != 2 {
		t.Fatalf("expected cutover invalidation to remove both bare and :8080 cached resources (2), got %d", res.InvalidatedResources)
	}
	if _, ok := c.get(keyFor(http.MethodGet, "portal.example.com:8080", "/api/v1/items?page=1")); ok {
		t.Fatalf("expected portal.example.com:8080 resource to be invalidated by route cutover selector")
	}
	if _, ok := c.get(keyFor(http.MethodGet, "other.example.com:8080", "/api/v1/items?page=1")); !ok {
		t.Fatalf("expected unrelated host other.example.com:8080 to remain cached")
	}

	// Verify BeginSelectorFenceWithError surfaces durable high-water persistence errors.
	c.disk.mu.Lock()
	c.disk.testWriteHighWaterErr = errors.New("injected high-water marker error")
	c.disk.mu.Unlock()
	_, comp, fenceErr := c.BeginSelectorFenceWithError(sels[0])
	comp()
	c.disk.mu.Lock()
	c.disk.testWriteHighWaterErr = nil
	c.disk.mu.Unlock()
	if fenceErr == nil {
		t.Fatalf("expected BeginSelectorFenceWithError to return injected high-water persistence error")
	}

	// Verify server_names containing an explicit port (portal.example.com:8443) still derives a cutover selector
	// that matches bare and alternate-port cached authorities (AR-07).
	beforePorted := &config.Config{
		Servers: []config.ServerConfig{
			{
				Listen:      ":8443",
				ServerNames: []string{"portal.example.com:8443"},
				Locations: []config.LocationConfig{
					{
						Match:     config.MatchConfig{Path: "/api/v1", Type: "prefix"},
						ProxyPass: "http://127.0.0.1:9001",
					},
				},
			},
		},
	}
	afterPorted := &config.Config{
		Servers: []config.ServerConfig{
			{
				Listen:      ":8443",
				ServerNames: []string{"portal.example.com:8443"},
				Locations: []config.LocationConfig{
					{
						Match:     config.MatchConfig{Path: "/api/v1", Type: "prefix"},
						ProxyPass: "http://127.0.0.1:9002",
					},
				},
			},
		},
	}
	selsPorted := DeriveRouteCutoverSelectors(beforePorted, afterPorted)
	if len(selsPorted) != 1 || !selsPorted[0].MatchesResource("portal.example.com", "/api/v1/items", nil) || !selsPorted[0].MatchesResource("portal.example.com:8080", "/api/v1/items", nil) {
		t.Fatalf("expected ported server_name cutover selector to match both bare and :8080 authorities")
	}
}

func TestReauditR01_LedgerHighWaterFallbackWhenMarkerWriteFails(t *testing.T) {
	t.Parallel()

	diskDir := t.TempDir()
	c1, err := New(config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(2 << 20),
		DiskPath:      diskDir,
		DiskMaxSize:   config.Size(16 << 20),
		MaxEntrySize:  config.Size(4 << 20),
		DefaultTTL:    config.Duration(5 * time.Minute),
	}, nil)
	if err != nil {
		t.Fatalf("cache.New c1: %v", err)
	}

	c1.set(keyFor(http.MethodGet, "portal.example.com", "/r01-marker"), &Entry{
		Status:    http.StatusOK,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(5 * time.Minute),
		Body:      []byte("v1"),
	})

	// Force only the .jul-cache-gen marker write to fail while the purge ledger records the uncertain high fence.
	c1.disk.mu.Lock()
	c1.disk.testWriteHighWaterErr = errors.New("injected .jul-cache-gen write failure")
	c1.disk.mu.Unlock()

	sel, _ := ParseSelector(Selector{Kind: SelectorExactURL, Host: "portal.example.com", URI: "/r01-marker"}, nil)
	res1, err := c1.InvalidateScope(context.Background(), sel, InvalidateOptions{})
	if err != nil {
		t.Fatalf("InvalidateScope res1: %v", err)
	}
	if res1.InvalidationEffective != "uncertain" || res1.FenceSequence == 0 {
		t.Fatalf("expected uncertain outcome with FenceSequence > 0 when marker write fails, got effective=%q fence=%d",
			res1.InvalidationEffective, res1.FenceSequence)
	}

	// Persist the uncertain high fence in .jul-cache-purge-ledger (as handleV1CachePurge does).
	ledgerJSON := fmt.Sprintf(`[{"operation_id":"cp_1","principal":"shared","response":{"fence_sequence":%d}}]`, res1.FenceSequence)
	if err := os.WriteFile(filepath.Join(diskDir, diskPurgeLedgerFile), []byte(ledgerJSON), 0o600); err != nil {
		t.Fatalf("WriteFile ledger: %v", err)
	}

	// Repair marker write and restart (cache.New on diskDir): rehydrate() must recover highWaterGen from .jul-cache-purge-ledger.
	c2, err := New(config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(2 << 20),
		DiskPath:      diskDir,
		DiskMaxSize:   config.Size(16 << 20),
		MaxEntrySize:  config.Size(4 << 20),
		DefaultTTL:    config.Duration(5 * time.Minute),
	}, nil)
	if err != nil {
		t.Fatalf("cache.New c2: %v", err)
	}
	if c2.StartupHighWaterGen() < res1.FenceSequence {
		t.Fatalf("expected c2 StartupHighWaterGen (%d) >= ledger FenceSequence (%d)",
			c2.StartupHighWaterGen(), res1.FenceSequence)
	}

	// Populate fresh content on c2; it must receive gen > res1.FenceSequence and survive a cross-boot retry at ReuseFenceGen.
	c2.set(keyFor(http.MethodGet, "portal.example.com", "/r01-marker"), &Entry{
		Status:    http.StatusOK,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(5 * time.Minute),
		Body:      []byte("v2-fresh"),
	})
	res2, err := c2.InvalidateScope(context.Background(), sel, InvalidateOptions{
		ReuseFenceGen:  res1.FenceSequence,
		CrossBootRetry: true,
	})
	if err != nil {
		t.Fatalf("InvalidateScope cross-boot retry: %v", err)
	}
	if res2.State != "succeeded" {
		t.Fatalf("expected succeeded retry, got %q", res2.State)
	}
	if e, ok := c2.get(keyFor(http.MethodGet, "portal.example.com", "/r01-marker")); !ok || string(e.Body) != "v2-fresh" {
		t.Fatalf("expected fresh post-restart fill v2-fresh to survive retry at old FenceSequence, got ok=%v", ok)
	}

	// Verify CrossBootRetry fails closed when ReuseFenceGen exceeds StartupHighWaterGen (AR-07).
	if _, err := c2.InvalidateScope(context.Background(), sel, InvalidateOptions{
		ReuseFenceGen:  c2.StartupHighWaterGen() + 100,
		CrossBootRetry: true,
	}); err == nil {
		t.Fatalf("expected CrossBootRetry with ReuseFenceGen > StartupHighWaterGen to fail closed")
	}

	// Exercise durable cutoff commit, nil receivers, and SelectorAll retry at ReuseFenceGen.
	var nilCache *Cache
	if nilCache.StartupHighWaterGen() != 0 {
		t.Fatalf("expected nil Cache StartupHighWaterGen to return 0")
	}
	_, nilComp, nilErr := nilCache.BeginSelectorFenceWithError(sel)
	nilComp()
	if nilErr != nil {
		t.Fatalf("expected nil BeginSelectorFenceWithError error, got %v", nilErr)
	}
	var rsvFence uint64
	if _, err := c2.InvalidateScope(context.Background(), sel, InvalidateOptions{
		PreviewSequence: c2.genSeq.Load(),
		CommitFence: func(fenceGen uint64, _ []string) error {
			rsvFence = fenceGen
			return nil
		},
	}); err != nil {
		t.Fatalf("InvalidateScope with CommitFence: %v", err)
	}
	allSel, _ := ParseSelector(Selector{Kind: SelectorAll, All: true}, nil)
	if _, err := c2.InvalidateScope(context.Background(), allSel, InvalidateOptions{
		ReuseFenceGen: rsvFence,
	}); err != nil {
		t.Fatalf("InvalidateScope SelectorAll with ReuseFenceGen: %v", err)
	}
	swNoHijack := &statusWriter{ResponseWriter: httptest.NewRecorder()}
	if _, _, hErr := swNoHijack.Hijack(); !errors.Is(hErr, http.ErrNotSupported) {
		t.Fatalf("expected statusWriter.Hijack on non-Hijacker to return ErrNotSupported, got %v", hErr)
	}
}

func TestFullPurgeRetryPreservesPostCutoffEntries(t *testing.T) {
	t.Parallel()

	for _, tier := range []string{"memory", "disk", "disk_only"} {
		for _, replacement := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/replacement=%t", tier, replacement), func(t *testing.T) {
				t.Parallel()
				cfg := config.CacheConfig{
					MemoryMaxSize: config.Size(2 << 20),
					DefaultTTL:    config.Duration(5 * time.Minute),
					TagHeaders:    []string{"Cache-Tag"},
				}
				if tier != "memory" {
					cfg.DiskPath = t.TempDir()
					cfg.DiskMaxSize = config.Size(8 << 20)
					cfg.MaxEntrySize = config.Size(4 << 20)
					if tier == "disk_only" {
						cfg.MemoryMaxSize = config.Size(1)
					}
				}
				c := newTestCache(t, cfg)
				oldKey := keyFor(http.MethodGet, "portal.example.com", "/old")
				freshKey := keyFor(http.MethodGet, "portal.example.com", "/fresh")
				if replacement {
					freshKey = oldKey
				}
				entry := func(body string) *Entry {
					return &Entry{
						Status:    http.StatusOK,
						CreatedAt: time.Now(),
						ExpiresAt: time.Now().Add(5 * time.Minute),
						Body:      []byte(body),
						Tags:      []string{body},
					}
				}
				c.set(oldKey, entry("old"))
				cutoff := c.genSeq.Add(1)
				c.set(freshKey, entry("fresh"))
				all, err := ParseSelector(Selector{Kind: SelectorAll, All: true}, nil)
				if err != nil {
					t.Fatal(err)
				}
				res, err := c.InvalidateScope(context.Background(), all, InvalidateOptions{
					ReuseFenceGen: cutoff,
					MaxScanKeys:   1,
				})
				if err != nil || res.State != "succeeded" || res.InvalidationEffective != "effective" {
					t.Fatalf("full purge retry: res=%+v err=%v", res, err)
				}
				wantRemoved := 1
				if replacement {
					wantRemoved = 0
				}
				if res.InvalidatedResources != wantRemoved || res.InvalidatedRepresentations != wantRemoved {
					t.Errorf("purge counted post-cutoff entries: %+v", res)
				}
				wantMem, wantDisk := wantRemoved, wantRemoved
				if tier == "disk_only" {
					wantMem = 0
				}
				if tier == "memory" {
					wantDisk = 0
				}
				if res.MemoryEntriesRemoved != wantMem || res.DiskEntriesRemoved != wantDisk {
					t.Errorf("unexpected tier deletion counts: %+v", res)
				}
				if !replacement {
					if _, ok := c.get(oldKey); ok {
						t.Error("pre-cutoff entry survived full purge")
					}
				}
				assertCachedEntryBody(t, c, freshKey, "fresh")
				tag, err := ParseSelector(Selector{Kind: SelectorTag, Host: "portal.example.com", Tags: []string{"fresh"}}, nil)
				if err != nil {
					t.Fatal(err)
				}
				if prev := c.PreviewScope(tag, PreviewOptions{}); prev.MatchedResources != 1 || prev.MatchedRepresentations != 1 {
					t.Fatalf("post-cutoff entry lost its catalog or tag index: %+v", prev)
				}
				if tier != "memory" {
					reopened := newTestCache(t, cfg)
					assertCachedEntryBody(t, reopened, freshKey, "fresh")
					if !replacement {
						if _, ok := reopened.get(oldKey); ok {
							t.Error("pre-cutoff disk entry reappeared after restart")
						}
					}
				}
			})
		}
	}
}

func TestFullPurgeRetryPreservesNewerFullPurgeFloor(t *testing.T) {
	t.Parallel()

	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(2 << 20)})
	oldCutoff := c.genSeq.Add(1)
	opID, startGen := c.beginInflightOriginOp()
	defer c.endInflightOriginOp(opID)
	all, err := ParseSelector(Selector{Kind: SelectorAll, All: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	newer, err := c.InvalidateScope(context.Background(), all, InvalidateOptions{})
	if err != nil || newer.State != "succeeded" {
		t.Fatalf("newer full purge: res=%+v err=%v", newer, err)
	}
	older, err := c.InvalidateScope(context.Background(), all, InvalidateOptions{
		ReuseFenceGen: oldCutoff,
	})
	if err != nil || older.State != "succeeded" {
		t.Fatalf("older full purge: res=%+v err=%v", older, err)
	}
	if floor := c.IndexStats().FenceFloorGen; floor < newer.FenceSequence {
		t.Fatalf("older full purge lowered generation floor: got %d, want >= %d", floor, newer.FenceSequence)
	}
	e := &Entry{Status: http.StatusOK, Body: []byte("stale")}
	key := keyFor(http.MethodGet, "portal.example.com", "/inflight")
	if c.setWithStartGen(key, e, startGen, true) {
		t.Fatal("older full purge allowed a fill fenced by the newer full purge to publish")
	}
}

func TestFullPurgePreservesPostFenceDiskPublication(t *testing.T) {
	t.Parallel()

	for _, retry := range []bool{false, true} {
		t.Run(fmt.Sprintf("retry=%t", retry), func(t *testing.T) {
			t.Parallel()
			cfg := config.CacheConfig{
				MemoryMaxSize: config.Size(1),
				MaxEntrySize:  config.Size(4 << 20),
				DiskPath:      t.TempDir(),
				DiskMaxSize:   config.Size(8 << 20),
			}
			c := newTestCache(t, cfg)
			key := keyFor(http.MethodGet, "portal.example.com", "/fresh")
			c.onAfterFenceInstall = func(_ NormalizedSelector, _ uint64) {
				c.set(key, &Entry{
					Status:    http.StatusOK,
					ExpiresAt: time.Now().Add(5 * time.Minute),
					Body:      []byte("fresh-post-fence"),
				})
			}
			all, err := ParseSelector(Selector{Kind: SelectorAll, All: true}, nil)
			if err != nil {
				t.Fatal(err)
			}
			opts := InvalidateOptions{}
			if retry {
				opts.ReuseFenceGen = c.genSeq.Add(1)
			}
			res, err := c.InvalidateScope(context.Background(), all, opts)
			if err != nil || res.State != "succeeded" || res.InvalidatedRepresentations != 0 {
				t.Fatalf("empty full purge: res=%+v err=%v", res, err)
			}
			assertCachedEntryBody(t, c, key, "fresh-post-fence")
			reopened := newTestCache(t, cfg)
			assertCachedEntryBody(t, reopened, key, "fresh-post-fence")
		})
	}
}

func assertCachedEntryBody(t *testing.T, c *Cache, key, want string) {
	t.Helper()
	e, ok := c.get(key)
	if !ok {
		t.Fatalf("expected cached entry for %q", key)
	}
	r, err := e.OpenBody()
	if err != nil {
		t.Fatalf("OpenBody: %v", err)
	}
	defer r.Close()
	body, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(body) != want {
		t.Fatalf("cached body = %q, want %q", body, want)
	}
}

func TestPurgeCommitFenceAtomicallyBlocksEarlierPublications(t *testing.T) {
	t.Parallel()

	for _, kind := range []SelectorKind{SelectorAll, SelectorExactURL, SelectorPrefix, SelectorGlob, SelectorTag, SelectorHost, SelectorKey} {
		for _, disk := range []bool{false, true} {
			for _, vary := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/disk=%t/vary=%t", kind, disk, vary), func(t *testing.T) {
					t.Parallel()
					cfg := config.CacheConfig{
						MemoryMaxSize: config.Size(2 << 20),
						DefaultTTL:    config.Duration(5 * time.Minute),
						TagHeaders:    []string{"Cache-Tag"},
					}
					if disk {
						cfg.DiskPath = t.TempDir()
						cfg.DiskMaxSize = config.Size(8 << 20)
					}
					c := newTestCache(t, cfg)
					entered := make(chan struct{})
					originReturned := make(chan struct{})
					originRelease := make(chan struct{})
					commitRelease := make(chan struct{})
					releaseOrigin := sync.OnceFunc(func() { close(originRelease) })
					releaseCommit := sync.OnceFunc(func() { close(commitRelease) })
					var wg sync.WaitGroup
					t.Cleanup(func() {
						releaseOrigin()
						releaseCommit()
						wg.Wait()
					})
					var calls atomic.Int64
					h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						w.Header().Set("Cache-Control", "public, max-age=300")
						w.Header().Set("Cache-Tag", "article:42")
						if vary {
							w.Header().Set("Vary", "Accept")
						}
						if calls.Add(1) == 1 {
							close(entered)
							<-originRelease
							_, _ = io.WriteString(w, "stale-pre-cutoff")
							close(originReturned)
							return
						}
						_, _ = io.WriteString(w, "fresh-post-cutoff")
					}))
					serve := func() *httptest.ResponseRecorder {
						rr := httptest.NewRecorder()
						req := httptest.NewRequest(http.MethodGet, "http://portal.example.com/article", nil)
						req.Header.Set("Accept", "text/plain")
						h.ServeHTTP(rr, req)
						return rr
					}
					fillDone := make(chan *httptest.ResponseRecorder, 1)
					wg.Add(1)
					go func() {
						defer wg.Done()
						fillDone <- serve()
					}()
					<-entered
					raw := Selector{Kind: kind, Host: "portal.example.com"}
					switch kind {
					case SelectorAll:
						raw.Host, raw.All = "", true
					case SelectorExactURL:
						raw.URI = "/article"
					case SelectorPrefix:
						raw.Prefix = "/article"
					case SelectorGlob:
						raw.Pattern = "/art*"
					case SelectorTag:
						raw.Tags = []string{"article:42"}
					case SelectorKey:
						raw.Host, raw.Key = "", keyFor(http.MethodGet, "portal.example.com", "/article")
					}
					sel, err := ParseSelector(raw, nil)
					if err != nil {
						t.Fatal(err)
					}
					committing := make(chan uint64, 1)
					purgeDone := make(chan InvalidateResult, 1)
					wg.Add(1)
					go func() {
						defer wg.Done()
						res, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{
							CommitFence: func(fenceGen uint64, _ []string) error {
								committing <- fenceGen
								<-commitRelease
								return nil
							},
						})
						if err != nil {
							t.Errorf("InvalidateScope: %v", err)
						}
						purgeDone <- res
					}()
					fenceGen := <-committing
					if c.genMu.TryLock() {
						c.genMu.Unlock()
						t.Fatal("durable cutoff commit did not exclude origin publication")
					}
					releaseOrigin()
					<-originReturned
					releaseCommit()
					res := <-purgeDone
					if res.State != "succeeded" || res.InvalidationEffective != "effective" || res.FenceSequence != fenceGen {
						t.Fatalf("unexpected purge result: %+v", res)
					}
					if initial := <-fillDone; initial.Body.String() != "stale-pre-cutoff" {
						t.Fatalf("original client response changed: %q", initial.Body.String())
					}
					after := serve()
					if after.Header().Get("X-Cache") != "MISS" || after.Body.String() != "fresh-post-cutoff" {
						t.Fatalf("pre-cutoff work survived purge: X-Cache=%q body=%q", after.Header().Get("X-Cache"), after.Body.String())
					}
					hit := serve()
					if hit.Header().Get("X-Cache") != "HIT" || hit.Body.String() != "fresh-post-cutoff" || calls.Load() != 2 {
						t.Fatalf("fresh response not cached: X-Cache=%q body=%q calls=%d", hit.Header().Get("X-Cache"), hit.Body.String(), calls.Load())
					}
					if stats := c.IndexStats(); stats.ActiveFences != 0 || stats.InflightOriginOps != 0 {
						t.Fatalf("completed work leaked fences: %+v", stats)
					}
				})
			}
		}
	}
}

func TestPurgeCommitFenceFailurePreservesResidentsAndFencesEarlierWork(t *testing.T) {
	t.Parallel()

	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(2 << 20)})
	key := keyFor(http.MethodGet, "portal.example.com", "/item")
	c.set(key, &Entry{Status: http.StatusOK, Body: []byte("warm")})
	opID, startGen := c.beginInflightOriginOp()
	defer c.endInflightOriginOp(opID)
	all, err := ParseSelector(Selector{Kind: SelectorAll, All: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	commitErr := errors.New("intent storage unavailable")
	res, err := c.InvalidateScope(context.Background(), all, InvalidateOptions{
		CommitFence: func(uint64, []string) error { return commitErr },
	})
	if !errors.Is(err, ErrFenceCommit) || !errors.Is(err, commitErr) || res.State != "" {
		t.Fatalf("commit failure: res=%+v err=%v", res, err)
	}
	assertCachedEntryBody(t, c, key, "warm")
	if stats := c.IndexStats(); stats.ActiveFences != 1 || stats.FenceFloorGen != 0 {
		t.Fatalf("uncertain intent did not retain a publication-only fence: %+v", stats)
	}
	if c.setWithStartGen(key, &Entry{Status: http.StatusOK, Body: []byte("completed")}, startGen, true) {
		t.Fatal("uncertain intent allowed earlier origin work to publish")
	}
	assertCachedEntryBody(t, c, key, "warm")
}

func TestPurgeRejectedBeforeFenceCommit(t *testing.T) {
	t.Parallel()

	for _, reason := range []string{"canceled", "broad", "scan_bound"} {
		t.Run(reason, func(t *testing.T) {
			t.Parallel()
			c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(2 << 20)})
			for _, path := range []string{"/items/a", "/items/b"} {
				c.set(keyFor(http.MethodGet, "portal.example.com", path), &Entry{Status: http.StatusOK, Body: []byte("warm")})
			}
			raw := Selector{Kind: SelectorAll, All: true}
			ctx := context.Background()
			committed := false
			opts := InvalidateOptions{CommitFence: func(uint64, []string) error {
				committed = true
				return nil
			}}
			switch reason {
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "broad":
				opts.RequireBroadConfirm = true
			case "scan_bound":
				raw = Selector{Kind: SelectorPrefix, Host: "portal.example.com", Prefix: "/items"}
				opts.MaxScanKeys = 1
			}
			sel, err := ParseSelector(raw, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.InvalidateScope(ctx, sel, opts); err == nil || committed {
				t.Fatalf("rejected purge committed intent: committed=%t err=%v", committed, err)
			}
			assertCachedEntryBody(t, c, keyFor(http.MethodGet, "portal.example.com", "/items/a"), "warm")
			if stats := c.IndexStats(); stats.ActiveFences != 0 || stats.FenceFloorGen != 0 {
				t.Fatalf("rejected purge installed a fence: %+v", stats)
			}
		})
	}
}

type observedErrContext struct {
	context.Context
	checked func()
}

func (ctx observedErrContext) Err() error {
	err := ctx.Context.Err()
	ctx.checked()
	return err
}

func TestPurgeCanceledWhileWaitingForGenerationLockDoesNotCommit(t *testing.T) {
	t.Parallel()

	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(2 << 20)})
	key := keyFor(http.MethodGet, "portal.example.com", "/warm")
	c.set(key, &Entry{Status: http.StatusOK, Body: []byte("warm")})
	all, err := ParseSelector(Selector{Kind: SelectorAll, All: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	checked := make(chan struct{})
	ctx := observedErrContext{Context: base, checked: sync.OnceFunc(func() { close(checked) })}
	done := make(chan struct{})
	var res InvalidateResult
	var invErr error
	committed := false
	c.genMu.Lock()
	go func() {
		defer close(done)
		res, invErr = c.InvalidateScope(ctx, all, InvalidateOptions{
			CommitFence: func(uint64, []string) error {
				committed = true
				return nil
			},
		})
	}()
	<-checked
	cancel()
	c.genMu.Unlock()
	<-done
	if !errors.Is(invErr, context.Canceled) || res.State != "canceled" || committed {
		t.Fatalf("canceled waiter committed a purge: state=%s committed=%t err=%v", res.State, committed, invErr)
	}
	assertCachedEntryBody(t, c, key, "warm")
}

func TestPurgeUncertainIntentFencesDiskPublicationsAcrossRestart(t *testing.T) {
	t.Parallel()

	cfg := config.CacheConfig{
		MemoryMaxSize: config.Size(2 << 20), DefaultTTL: config.Duration(5 * time.Minute),
		DiskPath: t.TempDir(), DiskMaxSize: config.Size(8 << 20),
	}
	c := newTestCache(t, cfg)
	warmKey := keyFor(http.MethodGet, "portal.example.com", "/articles/warm")
	c.set(warmKey, &Entry{Status: http.StatusOK, Body: []byte("warm")})
	entered, release := make(chan struct{}), make(chan struct{})
	releaseOrigin := sync.OnceFunc(func() { close(release) })
	var wg sync.WaitGroup
	t.Cleanup(func() {
		releaseOrigin()
		wg.Wait()
	})
	var calls atomic.Int64
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		if calls.Add(1) == 1 {
			close(entered)
			<-release
			_, _ = io.WriteString(w, "stale")
			return
		}
		_, _ = io.WriteString(w, "fresh")
	}))
	serve := func() *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "http://portal.example.com/articles/item", nil))
		return rr
	}
	done := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(done)
		serve()
	}()
	<-entered
	sel, err := ParseSelector(Selector{Kind: SelectorPrefix, Host: "portal.example.com", Prefix: "/articles"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var cutoff uint64
	_, err = c.InvalidateScope(context.Background(), sel, InvalidateOptions{
		CommitFence: func(gen uint64, _ []string) error {
			cutoff = gen
			ledger := fmt.Sprintf(`[{"response":{"fence_sequence":%d}}]`, gen)
			if err := os.WriteFile(filepath.Join(cfg.DiskPath, ".jul-cache-purge-ledger"), []byte(ledger), 0o600); err != nil {
				return err
			}
			return errors.New("intent rename succeeded but directory sync failed")
		},
	})
	if !errors.Is(err, ErrFenceCommit) {
		t.Fatalf("expected uncertain intent failure, got %v", err)
	}
	assertCachedEntryBody(t, c, warmKey, "warm")
	releaseOrigin()
	<-done
	fresh := serve()
	if fresh.Header().Get("X-Cache") != "MISS" || fresh.Body.String() != "fresh" {
		t.Fatalf("uncertain intent allowed stale publication: X-Cache=%q body=%q", fresh.Header().Get("X-Cache"), fresh.Body.String())
	}
	reopened := newTestCache(t, cfg)
	res, err := reopened.InvalidateScope(context.Background(), sel, InvalidateOptions{
		ReuseFenceGen: cutoff, CrossBootRetry: true,
	})
	if err != nil || res.State != "succeeded" {
		t.Fatalf("cross-boot continuation: res=%+v err=%v", res, err)
	}
	if _, ok := reopened.get(warmKey); ok {
		t.Fatal("cross-boot retry retained a pre-cutoff resident entry")
	}
	assertCachedEntryBody(t, reopened, keyFor(http.MethodGet, "portal.example.com", "/articles/item"), "fresh")
}

func TestTagRetryRequiresOriginalOwnershipAndBoundsUniqueTargets(t *testing.T) {
	t.Parallel()

	cfg := config.CacheConfig{MemoryMaxSize: config.Size(2 << 20), TagHeaders: []string{"Cache-Tag"}}
	sel, err := ParseSelector(Selector{Kind: SelectorTag, Host: "portal.example.com", Tags: []string{"A"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("unknown_and_known_empty", func(t *testing.T) {
		c := newTestCache(t, cfg)
		cutoff := c.genSeq.Add(1)
		key := keyFor(http.MethodGet, "portal.example.com", "/fresh")
		c.set(key, &Entry{Status: http.StatusOK, Body: []byte("fresh"), Tags: []string{"A"}})
		if _, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{ReuseFenceGen: cutoff}); !errors.Is(err, ErrRetrySnapshotUnavailable) {
			t.Fatalf("unknown ownership was silently re-evaluated: %v", err)
		}
		res, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{
			ReuseFenceGen: cutoff, ReuseResourceIDs: []string{},
		})
		if err != nil || res.State != "succeeded" || res.InvalidatedRepresentations != 0 {
			t.Fatalf("known-empty ownership: res=%+v err=%v", res, err)
		}
		assertCachedEntryBody(t, c, key, "fresh")
	})
	t.Run("retained_legacy_fence", func(t *testing.T) {
		c := newTestCache(t, cfg)
		c.set(keyFor(http.MethodGet, "portal.example.com", "/old"), &Entry{Status: http.StatusOK, Tags: []string{"A"}})
		cutoff, complete := c.BeginSelectorFence(sel)
		defer complete()
		var ids []string
		res, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{
			ReuseFenceGen: cutoff,
			CommitFence: func(_ uint64, resources []string) error {
				ids = resources
				return nil
			},
		})
		if err != nil || res.State != "succeeded" || len(ids) != 1 {
			t.Fatalf("retained ownership could not be upgraded: res=%+v ids=%v err=%v", res, ids, err)
		}
	})
	t.Run("bounds_and_duplicates", func(t *testing.T) {
		c := newTestCache(t, cfg)
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			c.set(keyFor(method, "portal.example.com", "/old"), &Entry{Status: http.StatusOK, Tags: []string{"A"}})
		}
		cutoff := c.genSeq.Add(1)
		id := hashKey(resourceKeyOf("portal.example.com", "/old"))
		opts := InvalidateOptions{ReuseFenceGen: cutoff, ReuseResourceIDs: []string{id, id}, MaxScanKeys: 1}
		if _, err := c.InvalidateScope(context.Background(), sel, opts); err == nil {
			t.Fatal("retry bypassed the scan bound")
		}
		opts.MaxScanKeys = 2
		res, err := c.InvalidateScope(context.Background(), sel, opts)
		if err != nil || res.State != "succeeded" || res.InvalidatedResources != 1 || res.InvalidatedRepresentations != 2 {
			t.Fatalf("retry did not deduplicate resource ownership: res=%+v err=%v", res, err)
		}
		if len(c.byResourceID) != 0 {
			t.Fatal("removed resources leaked opaque catalog entries")
		}
	})
	t.Run("host_isolation", func(t *testing.T) {
		c := newTestCache(t, cfg)
		key := keyFor(http.MethodGet, "other.example.com", "/old")
		c.set(key, &Entry{Status: http.StatusOK, Body: []byte("other"), Tags: []string{"A"}})
		id := hashKey(resourceKeyOf("other.example.com", "/old"))
		if _, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{
			ReuseFenceGen: c.genSeq.Load(), ReuseResourceIDs: []string{id},
		}); err == nil {
			t.Fatal("retry accepted ownership from a different host")
		}
		assertCachedEntryBody(t, c, key, "other")
	})
}

func TestResourceIDCatalogTracksVariantsRehydrationAndGlobalPurge(t *testing.T) {
	t.Parallel()

	for _, disk := range []bool{false, true} {
		for _, legacyPurge := range []bool{false, true} {
			t.Run(fmt.Sprintf("disk=%t/legacy_purge=%t", disk, legacyPurge), func(t *testing.T) {
				t.Parallel()
				cfg := config.CacheConfig{MemoryMaxSize: config.Size(2 << 20), DefaultTTL: config.Duration(time.Minute)}
				if disk {
					cfg.DiskPath, cfg.DiskMaxSize = t.TempDir(), config.Size(8<<20)
				}
				c := newTestCache(t, cfg)
				warm := func(c *Cache) {
					h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						w.Header().Set("Cache-Control", "public, max-age=300")
						w.Header().Set("Vary", "Accept")
						_, _ = io.WriteString(w, "body")
					}))
					for _, method := range []string{http.MethodGet, http.MethodHead} {
						for _, accept := range []string{"text/plain", "application/json"} {
							req := httptest.NewRequest(method, "http://portal.example.com/item", nil)
							req.Header.Set("Accept", accept)
							h.ServeHTTP(httptest.NewRecorder(), req)
						}
					}
				}
				warm(c)
				if len(c.byResourceID) != 1 {
					t.Fatalf("variants duplicated the logical-resource index: %d", len(c.byResourceID))
				}
				if disk {
					c = newTestCache(t, cfg)
					if len(c.byResourceID) != 1 {
						t.Fatal("rehydration lost the logical-resource index")
					}
				}
				if legacyPurge {
					c.Purge()
				} else {
					all, err := ParseSelector(Selector{Kind: SelectorAll, All: true}, nil)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := c.InvalidateScope(context.Background(), all, InvalidateOptions{}); err != nil {
						t.Fatal(err)
					}
				}
				if len(c.byResourceID) != 0 {
					t.Fatal("global purge retained logical-resource index entries")
				}
				warm(c)
				c.Delete(keyFor(http.MethodGet, "portal.example.com", "/item"))
				if len(c.byResourceID) != 1 {
					t.Fatal("deleting GET lost retained HEAD ownership")
				}
				c.Delete(keyFor(http.MethodHead, "portal.example.com", "/item"))
				if len(c.byResourceID) != 0 {
					t.Fatal("deleting the last method leaked resource ownership")
				}
			})
		}
	}
}
