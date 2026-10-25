// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"errors"
	"fmt"
	"io"
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
