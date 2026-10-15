// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/gob"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

// discardResponseWriter acts like an outbound network socket that writes bytes
// to the kernel without retaining the entire response body in a bytes.Buffer,
// allowing tests and benchmarks to measure the cache's own heap allocations.
type discardResponseWriter struct {
	header  http.Header
	status  int
	written int64
	hash    uint64
}

func newDiscardResponseWriter() *discardResponseWriter {
	return &discardResponseWriter{header: make(http.Header)}
}

func (w *discardResponseWriter) Header() http.Header { return w.header }

func (w *discardResponseWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}

func (w *discardResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.written += int64(len(p))
	for _, b := range p {
		w.hash = w.hash*131 + uint64(b)
	}
	return len(p), nil
}

func largeCacheCfg(dir string, memMax, maxEntry, diskMax int64) config.CacheConfig {
	return config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(memMax),
		DiskPath:      dir,
		DiskMaxSize:   config.Size(diskMax),
		MaxEntrySize:  config.Size(maxEntry),
		DefaultTTL:    config.Duration(time.Hour),
		Lock:          true,
		LockTimeout:   config.Duration(5 * time.Second),
	}
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", dir, err)
	}
	for _, e := range entries {
		if isCacheTempFile(e.Name()) {
			t.Fatalf("leftover temporary/retired file in cache dir: %q", e.Name())
		}
	}
}

// TestLargeObjectTierPlacementAndKnownUnknownLengths verifies that responses
// within memory_max_size stay in the memory tier while responses above
// memory_max_size up to max_entry_size stream to disk (for both declared and
// chunked/unknown Content-Length) and remain disk-backed on hits.
func TestLargeObjectTierPlacementAndKnownUnknownLengths(t *testing.T) {
	dir := t.TempDir()
	const (
		memMax   = 4 << 10   // 4 KiB
		maxEntry = 64 << 10  // 64 KiB
		diskMax  = 512 << 10 // 512 KiB
	)
	c := newTestCache(t, largeCacheCfg(dir, memMax, maxEntry, diskMax))

	smallPayload := bytes.Repeat([]byte("s"), 1024)
	largeKnownPayload := bytes.Repeat([]byte("K"), 16<<10)
	largeChunkedPayload := bytes.Repeat([]byte("C"), 20<<10)

	var originCalls atomic.Int32
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originCalls.Add(1)
		w.Header().Set("Cache-Control", "public, max-age=3600")
		switch r.URL.Path {
		case "/small":
			w.Header().Set("Content-Length", strconv.Itoa(len(smallPayload)))
			_, _ = w.Write(smallPayload)
		case "/large-known":
			w.Header().Set("Content-Length", strconv.Itoa(len(largeKnownPayload)))
			for off := 0; off < len(largeKnownPayload); off += 2048 {
				_, _ = w.Write(largeKnownPayload[off : off+2048])
			}
		case "/large-chunked":
			for off := 0; off < len(largeChunkedPayload); off += 2048 {
				_, _ = w.Write(largeChunkedPayload[off : off+2048])
			}
		}
	}))

	// 1. Small object stays in memory tier.
	rec := get(t, h, "http://example.com/small")
	wantResult(t, rec, stateMiss, string(smallPayload))
	smallReq := httptest.NewRequest(http.MethodGet, "http://example.com/small", nil)
	if e, ok := c.mem.get(key(smallReq)); !ok || e.IsDiskBacked() || e.BodyLen() != int64(len(smallPayload)) {
		t.Fatalf("small entry in mem = (%+v, %v), want memory-backed of len %d", e, ok, len(smallPayload))
	}

	// 2. Large object with declared Content-Length streams straight to disk.
	rec = get(t, h, "http://example.com/large-known")
	wantResult(t, rec, stateMiss, string(largeKnownPayload))
	knownReq := httptest.NewRequest(http.MethodGet, "http://example.com/large-known", nil)
	if _, ok := c.mem.get(key(knownReq)); ok {
		t.Fatal("large object was placed in the memory tier")
	}
	eKnown, _, ok := c.lookup(key(knownReq), knownReq)
	if !ok || !eKnown.IsDiskBacked() || len(eKnown.Body) != 0 || eKnown.BodyLen() != int64(len(largeKnownPayload)) {
		t.Fatalf("large-known lookup: ok=%v diskBacked=%v len(Body)=%d BodyLen=%d", ok, eKnown.IsDiskBacked(), len(eKnown.Body), eKnown.BodyLen())
	}
	// Hit serves from disk and does not promote into memory.
	rec = get(t, h, "http://example.com/large-known")
	wantResult(t, rec, stateHit, string(largeKnownPayload))
	if _, ok := c.mem.get(key(knownReq)); ok {
		t.Fatal("large disk hit promoted an oversized entry into the memory tier")
	}

	// 3. Large object without Content-Length starts in memory, spills to disk on crossing memMax.
	rec = get(t, h, "http://example.com/large-chunked")
	wantResult(t, rec, stateMiss, string(largeChunkedPayload))
	chunkedReq := httptest.NewRequest(http.MethodGet, "http://example.com/large-chunked", nil)
	if _, ok := c.mem.get(key(chunkedReq)); ok {
		t.Fatal("large chunked object was placed in the memory tier")
	}
	rec = get(t, h, "http://example.com/large-chunked")
	wantResult(t, rec, stateHit, string(largeChunkedPayload))

	if got := originCalls.Load(); got != 3 {
		t.Fatalf("origin calls = %d, want 3", got)
	}
	if got := c.capture.inflight.Load(); got != 0 {
		t.Fatalf("memory capture inflight = %d, want 0", got)
	}
	if got := c.disk.tempBudget.inflight.Load(); got != 0 {
		t.Fatalf("disk temp capture inflight = %d, want 0", got)
	}
	assertNoTempFiles(t, dir)

	// 4. Verify BodyReader random-access contract (io.ReaderAt / io.Seeker / Len)
	// on both memory-backed and disk-backed entries for #442 readiness.
	for _, entry := range []*Entry{
		{Body: []byte("0123456789abcdef")},
		eKnown,
	} {
		br, err := entry.OpenBody()
		if err != nil {
			t.Fatalf("OpenBody: %v", err)
		}
		if br.Len() != entry.BodyLen() {
			t.Fatalf("br.Len() = %d, want %d", br.Len(), entry.BodyLen())
		}
		slice := make([]byte, 4)
		if n, err := br.ReadAt(slice, 4); err != nil || n != 4 {
			t.Fatalf("ReadAt: n=%d err=%v", n, err)
		}
		if pos, err := br.Seek(-4, io.SeekEnd); err != nil || pos != br.Len()-4 {
			t.Fatalf("Seek: pos=%d err=%v", pos, err)
		}
		tail := make([]byte, 4)
		if _, err := io.ReadFull(br, tail); err != nil {
			t.Fatalf("ReadFull after Seek: %v", err)
		}
		if err := br.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
}

// TestLargeObjectBoundaryOneByteOverAndBoundedHeap verifies exact-boundary
// acceptance, one-byte-over rejection, and O(1) heap behavior on multi-megabyte
// disk-streamed captures and hits.
func TestLargeObjectBoundaryOneByteOverAndBoundedHeap(t *testing.T) {
	dir := t.TempDir()
	const (
		memMax   = 4 << 10   // 4 KiB
		maxEntry = 16 << 10  // 16 KiB
		diskMax  = 256 << 10 // 256 KiB
	)
	c := newTestCache(t, largeCacheCfg(dir, memMax, maxEntry, diskMax))
	var skips skipLog
	c.SetCaptureSkipObserver(skips.add)

	exactBody := bytes.Repeat([]byte("E"), maxEntry)
	overBody := bytes.Repeat([]byte("O"), maxEntry+1)

	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		switch r.URL.Path {
		case "/exact-cl":
			w.Header().Set("Content-Length", strconv.Itoa(len(exactBody)))
			_, _ = w.Write(exactBody)
		case "/exact-chunked":
			_, _ = w.Write(exactBody[:memMax])
			_, _ = w.Write(exactBody[memMax:])
		case "/over-cl":
			w.Header().Set("Content-Length", strconv.Itoa(len(overBody)))
			_, _ = w.Write(overBody)
		case "/over-chunked":
			_, _ = w.Write(overBody[:memMax])
			_, _ = w.Write(overBody[memMax:])
		}
	}))

	for _, path := range []string{"/exact-cl", "/exact-chunked"} {
		wantResult(t, get(t, h, "http://example.com"+path), stateMiss, string(exactBody))
		wantResult(t, get(t, h, "http://example.com"+path), stateHit, string(exactBody))
	}
	for _, path := range []string{"/over-cl", "/over-chunked"} {
		wantResult(t, get(t, h, "http://example.com"+path), stateMiss, string(overBody))
		wantResult(t, get(t, h, "http://example.com"+path), stateMiss, string(overBody))
	}
	if got := len(skips.get()); got != 4 {
		t.Fatalf("oversize skips = %v (len %d), want 4 oversize events", skips.get(), got)
	}
	assertNoTempFiles(t, dir)

	// Verify a 8 MiB response streams to disk and serves on a hit without
	// allocating proportional to the 8 MiB body.
	bigDir := t.TempDir()
	const bigBodySize = 8 << 20 // 8 MiB
	bigCache := newTestCache(t, largeCacheCfg(bigDir, 64<<10, 16<<20, 32<<20))
	chunk := bytes.Repeat([]byte("B"), 32<<10)
	bigHandler := bigCache.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("Content-Length", strconv.Itoa(bigBodySize))
		w.WriteHeader(http.StatusOK)
		for written := 0; written < bigBodySize; written += len(chunk) {
			_, _ = w.Write(chunk)
		}
	}))

	runtime.GC()
	var beforeCapture, afterCapture, afterHit runtime.MemStats
	runtime.ReadMemStats(&beforeCapture)

	missWriter := newDiscardResponseWriter()
	bigHandler.ServeHTTP(missWriter, httptest.NewRequest(http.MethodGet, "http://example.com/8mb", nil))
	if missWriter.written != bigBodySize || missWriter.Header().Get("X-Cache") != stateMiss {
		t.Fatalf("8mb miss: written=%d state=%q", missWriter.written, missWriter.Header().Get("X-Cache"))
	}
	runtime.ReadMemStats(&afterCapture)

	hitWriter := newDiscardResponseWriter()
	bigHandler.ServeHTTP(hitWriter, httptest.NewRequest(http.MethodGet, "http://example.com/8mb", nil))
	if hitWriter.written != bigBodySize || hitWriter.Header().Get("X-Cache") != stateHit || hitWriter.hash != missWriter.hash {
		t.Fatalf("8mb hit: written=%d state=%q hashMatch=%v", hitWriter.written, hitWriter.Header().Get("X-Cache"), hitWriter.hash == missWriter.hash)
	}
	runtime.ReadMemStats(&afterHit)

	captureAlloc := afterCapture.TotalAlloc - beforeCapture.TotalAlloc
	hitAlloc := afterHit.TotalAlloc - afterCapture.TotalAlloc
	// Both capture and hit must allocate a tiny fraction of the 8 MiB body
	// (< 512 KiB even under race/instrumentation overhead).
	if captureAlloc > 512<<10 {
		t.Fatalf("8 MiB disk capture allocated %d bytes on heap, want << 8 MiB (< 512 KiB)", captureAlloc)
	}
	if hitAlloc > 512<<10 {
		t.Fatalf("8 MiB disk hit allocated %d bytes on heap, want << 8 MiB (< 512 KiB)", hitAlloc)
	}
}

// TestLargeObjectFailurePathsLeaveNoPartialEntry covers cancellation before and
// after spilling to disk, early EOF, client disconnect, origin error, and
// Vary-keyed large disk objects.
func TestLargeObjectFailurePathsLeaveNoPartialEntry(t *testing.T) {
	dir := t.TempDir()
	const (
		memMax   = 2 << 10
		maxEntry = 32 << 10
		diskMax  = 256 << 10
	)
	c := newTestCache(t, largeCacheCfg(dir, memMax, maxEntry, diskMax))

	t.Run("cancel after spilling to disk", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=3600")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(bytes.Repeat([]byte("x"), 4<<10)) // spills to disk
			cancel()
		}))
		req := httptest.NewRequest(http.MethodGet, "http://example.com/cancel-spill", nil).WithContext(ctx)
		h.ServeHTTP(httptest.NewRecorder(), req)
		if _, _, ok := c.lookup(key(req), req); ok {
			t.Fatal("canceled spilled response was published")
		}
		assertNoTempFiles(t, dir)
	})

	t.Run("early EOF shorter than declared Content-Length after disk stream", func(t *testing.T) {
		h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=3600")
			w.Header().Set("Content-Length", strconv.Itoa(16<<10))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(bytes.Repeat([]byte("y"), 8<<10))
		}))
		req := httptest.NewRequest(http.MethodGet, "http://example.com/short-disk", nil)
		h.ServeHTTP(httptest.NewRecorder(), req)
		if _, _, ok := c.lookup(key(req), req); ok {
			t.Fatal("truncated disk response was published")
		}
		assertNoTempFiles(t, dir)
	})

	t.Run("client disconnect during disk capture", func(t *testing.T) {
		h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=3600")
			w.Header().Set("Content-Length", strconv.Itoa(16<<10))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(bytes.Repeat([]byte("d"), 8<<10))
			_, _ = w.Write(bytes.Repeat([]byte("d"), 8<<10))
		}))
		req := httptest.NewRequest(http.MethodGet, "http://example.com/disconnect-disk", nil)
		fw := &failingResponseWriter{ResponseWriter: httptest.NewRecorder(), failAfter: 4 << 10}
		h.ServeHTTP(fw, req)
		if _, _, ok := c.lookup(key(req), req); ok {
			t.Fatal("disconnected disk capture was published")
		}
		assertNoTempFiles(t, dir)
	})

	t.Run("Vary large disk-backed variants and restart persistence", func(t *testing.T) {
		varyDir := t.TempDir()
		cfg := largeCacheCfg(varyDir, memMax, maxEntry, diskMax)
		c1 := newTestCache(t, cfg)
		bodyGzip := bytes.Repeat([]byte("G"), 8<<10)
		bodyBr := bytes.Repeat([]byte("B"), 8<<10)
		vh := c1.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=3600")
			w.Header().Set("Vary", "Accept-Encoding")
			if r.Header.Get("Accept-Encoding") == "gzip" {
				_, _ = w.Write(bodyGzip)
			} else {
				_, _ = w.Write(bodyBr)
			}
		}))
		wantResult(t, get(t, vh, "http://example.com/vary-big", "Accept-Encoding", "gzip"), stateMiss, string(bodyGzip))
		wantResult(t, get(t, vh, "http://example.com/vary-big", "Accept-Encoding", "br"), stateMiss, string(bodyBr))
		wantResult(t, get(t, vh, "http://example.com/vary-big", "Accept-Encoding", "gzip"), stateHit, string(bodyGzip))
		wantResult(t, get(t, vh, "http://example.com/vary-big", "Accept-Encoding", "br"), stateHit, string(bodyBr))

		// Restart over the same directory: both the Vary stub and the large
		// disk-backed variants survive and remain invalidatable.
		c2 := newTestCache(t, cfg)
		vh2 := c2.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
		wantResult(t, get(t, vh2, "http://example.com/vary-big", "Accept-Encoding", "gzip"), stateHit, string(bodyGzip))
		wantResult(t, get(t, vh2, "http://example.com/vary-big", "Accept-Encoding", "br"), stateHit, string(bodyBr))
		vh2.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodDelete, "http://example.com/vary-big", nil))
		rGzip := httptest.NewRequest(http.MethodGet, "http://example.com/vary-big", nil)
		rGzip.Header.Set("Accept-Encoding", "gzip")
		if _, _, ok := c2.lookup(key(rGzip), rGzip); ok {
			t.Fatal("large Vary variant survived DELETE invalidation")
		}
	})
}

// TestLargeObjectRestartRecoveryAndForeignFileSafety verifies startup behavior
// when disk_path contains valid JULDCH02 files, leftover temporary/retired
// artifacts, truncated files, corrupt trailers, legacy v1 gob files, and
// unrelated foreign files.
func TestLargeObjectRestartRecoveryAndForeignFileSafety(t *testing.T) {
	dir := t.TempDir()
	cfg := largeCacheCfg(dir, 2<<10, 32<<10, 256<<10)
	c1 := newTestCache(t, cfg)
	largeBody := bytes.Repeat([]byte("V"), 8<<10)
	h1 := c1.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(largeBody)
	}))
	wantResult(t, get(t, h1, "http://example.com/valid-large"), stateMiss, string(largeBody))

	// Plant temporary, retired, truncated, corrupt, legacy v1, and foreign files.
	tmpArtifact := filepath.Join(dir, ".jul-cache-tmp-leftover")
	retiredArtifact := filepath.Join(dir, ".jul-cache-retired-99")
	atomicTmpArtifact := filepath.Join(dir, "."+strings.Repeat("a", 64)+".tmp-123")
	truncatedHash := strings.Repeat("b", 64)
	corruptSizeHash := strings.Repeat("c", 64)
	corruptMetaHash := strings.Repeat("d", 64)
	legacyV1Hash := strings.Repeat("e", 64)
	foreignFile := filepath.Join(dir, "operator-readme.txt")

	for _, p := range []string{tmpArtifact, retiredArtifact, atomicTmpArtifact} {
		if err := os.WriteFile(p, []byte("incomplete"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, truncatedHash), []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, corruptSizeHash), []byte("012345678901234567890123JULDCH02"), 0o600); err != nil {
		t.Fatal(err)
	}
	badMeta := append([]byte("not-valid-gob"), make([]byte, diskTrailerSize)...)
	copy(badMeta[len(badMeta)-8:], diskFormatMagic)
	badMeta[len(badMeta)-24+7] = 13 // metaLen = 13, bodyLen = 0
	if err := os.WriteFile(filepath.Join(dir, corruptMetaHash), badMeta, 0o600); err != nil {
		t.Fatal(err)
	}
	var legacyBuf bytes.Buffer
	_ = gob.NewEncoder(&legacyBuf).Encode(&Entry{Status: 200, Body: []byte("legacy-v1")})
	if err := os.WriteFile(filepath.Join(dir, legacyV1Hash), legacyBuf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(foreignFile, []byte("keep-foreign"), 0o600); err != nil {
		t.Fatal(err)
	}

	c2 := newTestCache(t, cfg)
	h2 := c2.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("origin should not be contacted for valid-large after restart")
	}))
	wantResult(t, get(t, h2, "http://example.com/valid-large"), stateHit, string(largeBody))

	for _, removed := range []string{
		tmpArtifact,
		retiredArtifact,
		atomicTmpArtifact,
		filepath.Join(dir, truncatedHash),
		filepath.Join(dir, corruptSizeHash),
		filepath.Join(dir, corruptMetaHash),
		filepath.Join(dir, legacyV1Hash),
	} {
		if _, err := os.Stat(removed); !os.IsNotExist(err) {
			t.Fatalf("expected artifact %q to be removed on startup rehydrate, stat err = %v", removed, err)
		}
	}
	if got, err := os.ReadFile(foreignFile); err != nil || string(got) != "keep-foreign" {
		t.Fatalf("foreign file was not preserved: err=%v got=%q", err, got)
	}
	_, diskStats := c2.Stats()
	if diskStats == nil || diskStats.Entries != 1 {
		t.Fatalf("disk stats after rehydrate = %+v, want 1 valid entry", diskStats)
	}
}

type blockingChunkWriter struct {
	header      http.Header
	status      int
	buf         bytes.Buffer
	firstChunk  chan struct{}
	resumeWrite chan struct{}
	once        sync.Once
}

func newBlockingChunkWriter() *blockingChunkWriter {
	return &blockingChunkWriter{
		header:      make(http.Header),
		firstChunk:  make(chan struct{}),
		resumeWrite: make(chan struct{}),
	}
}

func (w *blockingChunkWriter) Header() http.Header { return w.header }

func (w *blockingChunkWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}

func (w *blockingChunkWriter) Write(p []byte) (int, error) {
	n, err := w.buf.Write(p)
	w.once.Do(func() {
		close(w.firstChunk)
		<-w.resumeWrite
	})
	return n, err
}

// TestLargeObjectActiveReaderDuringEvictionInvalidationAndReplacement proves
// that an in-progress disk-streamed cache hit completes with the exact
// representation bytes it started serving even when the entry is concurrently
// deleted, purged, evicted by LRU resize, or overwritten by a new representation.
func TestLargeObjectActiveReaderDuringEvictionInvalidationAndReplacement(t *testing.T) {
	for _, op := range []string{"delete", "purge", "evict", "replace"} {
		t.Run(op, func(t *testing.T) {
			dir := t.TempDir()
			cfg := largeCacheCfg(dir, 2<<10, 128<<10, 512<<10)
			c := newTestCache(t, cfg)

			v1Body := bytes.Repeat([]byte("1"), 96<<10) // 96 KiB (> 32 KiB chunk size)
			v2Body := bytes.Repeat([]byte("2"), 96<<10)
			var version atomic.Int32
			version.Store(1)

			h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", "public, max-age=3600")
				if version.Load() == 1 {
					_, _ = w.Write(v1Body)
				} else {
					_, _ = w.Write(v2Body)
				}
			}))

			// Warm v1 on disk.
			wantResult(t, get(t, h, "http://example.com/active"), stateMiss, string(v1Body))

			// Start an active reader that pauses after receiving the first 32 KiB chunk.
			bw := newBlockingChunkWriter()
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				h.ServeHTTP(bw, httptest.NewRequest(http.MethodGet, "http://example.com/active", nil))
			}()
			<-bw.firstChunk

			req := httptest.NewRequest(http.MethodGet, "http://example.com/active", nil)
			switch op {
			case "delete":
				c.Delete(key(req))
			case "purge":
				c.Purge()
			case "evict":
				c.disk.Resize(1)
			case "replace":
				version.Store(2)
				c.Delete(key(req))
				wantResult(t, get(t, h, "http://example.com/active"), stateMiss, string(v2Body))
				wantResult(t, get(t, h, "http://example.com/active"), stateHit, string(v2Body))
			}

			// Resume the paused v1 reader: it must still receive the complete v1 body.
			close(bw.resumeWrite)
			wg.Wait()

			if bw.Header().Get("X-Cache") != stateHit {
				t.Fatalf("active reader X-Cache = %q, want HIT", bw.Header().Get("X-Cache"))
			}
			if !bytes.Equal(bw.buf.Bytes(), v1Body) {
				t.Fatalf("active reader received %d bytes (mismatch with v1 %d bytes)", bw.buf.Len(), len(v1Body))
			}
			assertNoTempFiles(t, dir)
		})
	}
}

// TestLargeObjectRevalidation304And200 verifies both synchronous and background
// 304/200 revalidation of disk-backed large entries.
func TestLargeObjectRevalidation304And200(t *testing.T) {
	dir := t.TempDir()
	cfg := largeCacheCfg(dir, 2<<10, 64<<10, 512<<10)
	cfg.StaleWhileRevalidate = config.Duration(time.Minute)
	c := newTestCache(t, cfg)

	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
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

	bodyV1 := bytes.Repeat([]byte("A"), 12<<10)
	bodyV2 := bytes.Repeat([]byte("B"), 14<<10)
	var etag atomic.Value
	etag.Store(`"v1"`)

	origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		curTag := etag.Load().(string)
		w.Header().Set("Cache-Control", "public, max-age=10, stale-while-revalidate=60")
		w.Header().Set("ETag", curTag)
		if r.Header.Get("If-None-Match") == curTag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if curTag == `"v1"` {
			_, _ = w.Write(bodyV1)
		} else {
			_, _ = w.Write(bodyV2)
		}
	})
	h := c.Handler(origin)

	// 1. Cold miss stores v1 on disk.
	wantResult(t, get(t, h, "http://example.com/reval"), stateMiss, string(bodyV1))

	// 2. Synchronous validation (Cache-Control: no-cache) -> 304 Not Modified.
	rec := get(t, h, "http://example.com/reval", "Cache-Control", "no-cache")
	wantResult(t, rec, stateRevalidated, string(bodyV1))

	// 3. Synchronous validation with changed ETag -> 200 OK replacement on disk.
	etag.Store(`"v2"`)
	rec = get(t, h, "http://example.com/reval", "Cache-Control", "no-cache")
	wantResult(t, rec, stateMiss, string(bodyV2))
	wantResult(t, get(t, h, "http://example.com/reval"), stateHit, string(bodyV2))

	// 4. Background stale-while-revalidate on disk-backed entry -> 304 then 200.
	revalDone := make(chan string, 2)
	c.SetRevalidationObserver(func(outcome string) { revalDone <- outcome })
	advance(15 * time.Second) // expired (>10s), within SWR (<=70s)
	g := testLease(t, 1)
	req := leased(httptest.NewRequest(http.MethodGet, "http://example.com/reval", nil), g)
	staleRec := httptest.NewRecorder()
	h.ServeHTTP(staleRec, req)
	wantResult(t, staleRec, stateStale, string(bodyV2))
	if out := <-revalDone; out != string(outcomeNotModified) {
		t.Fatalf("background 304 outcome = %q, want not_modified", out)
	}

	// After background 304 refresh, entry is fresh again.
	wantResult(t, get(t, h, "http://example.com/reval"), stateHit, string(bodyV2))
	assertNoTempFiles(t, dir)
}

// TestLargeObjectConcurrentTempDiskBudgetExhaustion proves that concurrent
// temporary disk captures cannot exceed disk_max_size: an over-budget capture
// streams to its client uncaptured, records reason="budget", and releases its
// reservation on completion or cancellation.
func TestLargeObjectConcurrentTempDiskBudgetExhaustion(t *testing.T) {
	dir := t.TempDir()
	const (
		memMax   = 1 << 10  // 1 KiB
		maxEntry = 16 << 10 // 16 KiB
		diskMax  = 20 << 10 // 20 KiB total disk & temp budget
	)
	c := newTestCache(t, largeCacheCfg(dir, memMax, maxEntry, diskMax))
	var skips skipLog
	c.SetCaptureSkipObserver(skips.add)

	started := make(chan struct{})
	release := make(chan struct{})
	body12K := bytes.Repeat([]byte("L"), 12<<10)

	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("Content-Length", strconv.Itoa(len(body12K)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body12K)
		if r.URL.Path == "/hold-large" {
			close(started)
			<-release
		}
	}))

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://example.com/hold-large", nil))
	}()
	<-started

	if got := c.disk.tempBudget.inflight.Load(); got != int64(len(body12K)) {
		t.Fatalf("temp disk budget while held = %d, want %d", got, len(body12K))
	}

	// Second 12 KiB disk capture: 12 KiB + 12 KiB = 24 KiB > 20 KiB disk_max_size.
	secondRec := get(t, h, "http://example.com/second-large")
	wantResult(t, secondRec, stateMiss, string(body12K))
	if !sameReasons(skips.get(), captureSkipBudget) {
		t.Fatalf("skip reasons = %v, want [budget]", skips.get())
	}
	secondReq := httptest.NewRequest(http.MethodGet, "http://example.com/second-large", nil)
	if _, _, ok := c.lookup(key(secondReq), secondReq); ok {
		t.Fatal("over-budget large miss was stored")
	}

	close(release)
	wg.Wait()

	if got := c.disk.tempBudget.inflight.Load(); got != 0 {
		t.Fatalf("temp disk budget after completion = %d, want 0", got)
	}
	wantResult(t, get(t, h, "http://example.com/hold-large"), stateHit, string(body12K))
	assertNoTempFiles(t, dir)
}

// TestLargeObjectDiskWriteAndRenameFailures exercises disk write failures
// during large-object capture and publication.
func TestLargeObjectDiskWriteAndRenameFailures(t *testing.T) {
	dir := t.TempDir()
	c := newTestCache(t, largeCacheCfg(dir, 1<<10, 16<<10, 128<<10))
	body := bytes.Repeat([]byte("F"), 4<<10)

	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		if r.URL.Path == "/declared" {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		}
		_, _ = w.Write(body)
	}))

	// Remove disk_path so temporary file creation fails cross-platform (including Windows).
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.MkdirAll(dir, 0o700) })

	for _, path := range []string{"/declared", "/spilled"} {
		rec := get(t, h, "http://example.com"+path)
		wantResult(t, rec, stateMiss, string(body))
	}
	if fails, failing, ok := c.DiskWriteHealth(); !ok || fails == 0 || !failing {
		t.Fatalf("DiskWriteHealth = (%d, %v, %v), want failures > 0 and failing=true", fails, failing, ok)
	}
}

func TestLargeObjectEdgeAndFailurePaths(t *testing.T) {
	// 1. Entry nil/empty helpers and MemoryCapBytes fallback.
	var nilEntry *Entry
	if nilEntry.BodyLen() != 0 {
		t.Fatalf("nilEntry.BodyLen() = %d, want 0", nilEntry.BodyLen())
	}
	pol := CachePolicy{MaxEntryBytes: 1024}
	if pol.MemoryCapBytes() != 1024 {
		t.Fatalf("MemoryCapBytes() = %d, want 1024", pol.MemoryCapBytes())
	}

	// 2. diskBodyReader double close, nil blob openReader, unlinked openReader, nil stream abort/write.
	var nilBlob *diskBlob
	if _, err := nilBlob.openReader(); err == nil {
		t.Fatal("expected error opening nil diskBlob")
	}
	unlinkedBlob := &diskBlob{unlinked: true}
	if _, err := unlinkedBlob.openReader(); err == nil {
		t.Fatal("expected error opening unlinked diskBlob")
	}
	var nilDisk *diskStore
	var nilStream *diskStreamCapture
	nilDisk.abortStream(nilStream)
	if ok, _ := nilDisk.writeStream(nilStream, []byte("x")); ok {
		t.Fatal("expected writeStream on nilStream to fail")
	}

	// 3. readDiskEntryMeta corrupt trailer and size mismatch cases.
	tmpDir := t.TempDir()
	shortPath := filepath.Join(tmpDir, "short")
	if err := os.WriteFile(shortPath, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readDiskEntryMeta(shortPath); err == nil {
		t.Fatal("expected error for short file")
	}

	badMagicPath := filepath.Join(tmpDir, "badmagic")
	badMagic := make([]byte, diskTrailerSize)
	copy(badMagic[16:24], "BADMAGIC")
	if err := os.WriteFile(badMagicPath, badMagic, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readDiskEntryMeta(badMagicPath); err == nil {
		t.Fatal("expected error for bad magic")
	}

	zeroMetaPath := filepath.Join(tmpDir, "zerometa")
	var trailer [diskTrailerSize]byte
	copy(trailer[16:24], diskFormatMagic)
	if err := os.WriteFile(zeroMetaPath, trailer[:], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readDiskEntryMeta(zeroMetaPath); err == nil {
		t.Fatal("expected error for zero metaLen")
	}

	mismatchPath := filepath.Join(tmpDir, "mismatch")
	binary.BigEndian.PutUint64(trailer[0:8], 10)
	binary.BigEndian.PutUint64(trailer[8:16], 20)
	if err := os.WriteFile(mismatchPath, append(make([]byte, 10), trailer[:]...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readDiskEntryMeta(mismatchPath); err == nil {
		t.Fatal("expected error for size mismatch")
	}

	badGobPath := filepath.Join(tmpDir, "badgob")
	binary.BigEndian.PutUint64(trailer[0:8], 5)
	binary.BigEndian.PutUint64(trailer[8:16], 0)
	if err := os.WriteFile(badGobPath, append([]byte("12345"), trailer[:]...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readDiskEntryMeta(badGobPath); err == nil {
		t.Fatal("expected error for corrupt gob metadata")
	}

	// 4. serve fallback when disk file disappears before OpenBody (both HIT and STALE),
	// and 200 revalidation of a large disk-backed entry with Vary.
	diskDir := t.TempDir()
	cfg := largeCacheCfg(diskDir, 2<<10, 64<<10, 512<<10)
	cfg.StaleWhileRevalidate = config.Duration(time.Minute)
	c := newTestCache(t, cfg)

	now := time.Date(2026, 10, 15, 13, 0, 0, 0, time.UTC)
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

	payload := bytes.Repeat([]byte("Z"), 16<<10)
	origin := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=10, stale-while-revalidate=60")
		w.Header().Set("Vary", "Accept-Encoding")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	})
	h := c.Handler(origin)

	req1 := httptest.NewRequest(http.MethodGet, "http://example.com/disappear", nil)
	req1.Header.Set("Accept-Encoding", "gzip")
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, req1)

	// Mark the disk blob unlinked while still indexed to exercise serve() OpenBody failure on HIT.
	e, _, ok := c.lookup(key(req1), req1)
	if !ok || !e.IsDiskBacked() {
		t.Fatal("expected disk-backed entry")
	}
	br, err := e.OpenBody()
	if err != nil {
		t.Fatal(err)
	}
	if err := br.Close(); err != nil {
		t.Fatal(err)
	}
	if err := br.Close(); err != nil {
		t.Fatal(err)
	}
	e.diskBody.mu.Lock()
	e.diskBody.unlinked = true
	e.diskBody.mu.Unlock()
	// Also test c.disk.get(effKey) when blob.unlinked is true.
	if _, ok := c.disk.get(variantKey(key(req1), []string{"Accept-Encoding"}, req1)); ok {
		t.Fatal("expected disk.get to return false for unlinked blob")
	}

	// Re-populate and test serve() failure when underlying file is removed between lookup and OpenBody.
	c.Purge()
	rec1b := httptest.NewRecorder()
	h.ServeHTTP(rec1b, req1)
	e2, _, ok := c.lookup(key(req1), req1)
	if !ok || !e2.IsDiskBacked() {
		t.Fatal("expected disk-backed entry")
	}
	e2.diskBody.mu.Lock()
	origPath := e2.diskBody.path
	e2.diskBody.path = filepath.Join(diskDir, "nonexistent")
	e2.diskBody.mu.Unlock()
	if c.serve(httptest.NewRecorder(), req1, e2, stateHit, now) {
		t.Fatal("expected c.serve to return false when disk file cannot be opened")
	}
	e2.diskBody.mu.Lock()
	e2.diskBody.path = origPath
	e2.diskBody.mu.Unlock()

	// Advance into STALE window and test background 200 revalidation of Vary large object.
	revalDone := make(chan string, 2)
	c.SetRevalidationObserver(func(outcome string) { revalDone <- outcome })
	advance(15 * time.Second)
	g := testLease(t, 1)
	staleReq := leased(httptest.NewRequest(http.MethodGet, "http://example.com/disappear", nil), g)
	staleReq.Header.Set("Accept-Encoding", "gzip")
	staleRec := httptest.NewRecorder()
	h.ServeHTTP(staleRec, staleReq)
	wantResult(t, staleRec, stateStale, string(payload))
	if out := <-revalDone; out != string(outcomeStored) {
		t.Fatalf("background 200 outcome = %q, want stored", out)
	}

	// 5. Memory-only cache rejects responses exceeding memory_max_size even if max_entry_size is higher.
	memCfg := largeCacheCfg("", 2<<10, 64<<10, 0)
	memOnly := newTestCache(t, memCfg)
	memH := memOnly.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=60")
		if r.URL.Path == "/declared" {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload[:1<<10])
		_, _ = w.Write(payload[1<<10:])
	}))
	for _, p := range []string{"/declared", "/spilled"} {
		recM := get(t, memH, "http://example.com"+p)
		wantResult(t, recM, stateMiss, string(payload))
	}

	// 6. Direct stream error branches: write/publish on closed file, setCaptured with nil disk or failed publish,
	// and chunked spill when temp disk budget is exhausted.
	if memOnly.setCaptured("k", &Entry{Status: 200}, &diskStreamCapture{closed: true}) {
		t.Fatal("expected setCaptured to return false on memory-only cache with stream")
	}
	if c.storeCaptured("base", req1, &Entry{Status: 200, Vary: []string{"Accept-Encoding"}}, &diskStreamCapture{closed: true}) {
		t.Fatal("expected storeCaptured to return false when stream is closed")
	}
	closedStream := c.disk.beginStreamCapture(nil)
	if closedStream == nil {
		t.Fatal("expected beginStreamCapture to succeed")
	}
	_ = closedStream.file.Close()
	if ok, _ := c.disk.writeStream(closedStream, []byte("fail")); ok {
		t.Fatal("expected write on closed file handle to fail")
	}
	if _, ok := c.disk.publishStream("k", &Entry{Status: 200}, closedStream); ok {
		t.Fatal("expected publishStream on closed file handle to fail")
	}
	if _, ok := c.disk.publishStream("k", &Entry{Status: 200}, nil); ok {
		t.Fatal("expected publishStream on nil stream to fail")
	}
	c.disk.set("unlinked-disk-entry", &Entry{Status: 200, diskBody: &diskBlob{unlinked: true}})

	// Exhaust tempBudget and test chunked spill + recorder dropCapture.
	c.disk.tempBudget.inflight.Store(c.disk.maxBytesCap())
	chunkedSpillRec := get(t, h, "http://example.com/chunked-spill-budget")
	wantResult(t, chunkedSpillRec, stateMiss, string(payload))
	c.disk.tempBudget.inflight.Store(0)

	rec := &recorder{limit: 4096, memLimit: 1024, disk: c.disk}
	rec.WriteHeader(http.StatusContinue) // 100 Continue
	rec.WriteHeader(http.StatusSwitchingProtocols)
	if rec.storable() {
		t.Fatal("expected 101 recorder to be non-storable")
	}

	// 7. Exercise Windows-style pinned blob deferred promotion helpers directly.
	promoteDir := t.TempDir()
	stagedPath := filepath.Join(promoteDir, "staged")
	finalPath := filepath.Join(promoteDir, "final")
	if err := os.WriteFile(stagedPath, []byte("v2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(finalPath, []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	pinnedBlob := &diskBlob{dir: promoteDir, path: finalPath, readers: 1, unlinked: true}
	if !pinnedBlob.isPinning(finalPath) {
		t.Fatal("expected pinnedBlob.isPinning to be true")
	}
	replacementBlob := &diskBlob{dir: promoteDir, path: stagedPath}
	var extraCalled bool
	pinnedBlob.setOnClose(func() { extraCalled = true })
	pinnedBlob.setOnClose(func() { replacementBlob.promote(finalPath) })
	pinnedBlob.releaseReader()
	if !extraCalled || replacementBlob.path != finalPath {
		t.Fatalf("expected promotion on releaseReader: extraCalled=%v path=%q", extraCalled, replacementBlob.path)
	}
	// Calling setOnClose when readers == 0 executes immediately.
	var immediate bool
	pinnedBlob.setOnClose(func() { immediate = true })
	if !immediate {
		t.Fatal("expected setOnClose with 0 readers to run immediately")
	}
	// Promote on unlinked or already-promoted blob is a no-op; failed rename sets pendingTarget and promotes on releaseReader.
	replacementBlob.promote(finalPath)
	pendingBlob := &diskBlob{dir: promoteDir, path: filepath.Join(promoteDir, "missing-src"), readers: 1}
	pendingBlob.promote(finalPath)
	if pendingBlob.pendingTarget != finalPath {
		t.Fatalf("expected pendingTarget=%q, got %q", finalPath, pendingBlob.pendingTarget)
	}
	if err := os.WriteFile(pendingBlob.path, []byte("v3"), 0o600); err != nil {
		t.Fatal(err)
	}
	pendingBlob.releaseReader()
	if pendingBlob.path != finalPath {
		t.Fatalf("expected pendingBlob to promote on releaseReader, got %q", pendingBlob.path)
	}
	// Exercise retireBlobLocked with nil and pinning blob.
	_ = c.disk.retireBlobLocked("h", nil)
	hPin := hashKey("pin-key")
	pinBlob := &diskBlob{dir: diskDir, path: c.disk.path(hPin), readers: 1, unlinked: true}
	_ = c.disk.retireBlobLocked(hPin, pinBlob)
	if c.disk.pinned[hPin] != pinBlob {
		t.Fatal("expected pinBlob to be recorded in c.disk.pinned")
	}
}

// TestLargeObjectValidation200PublishFailuresStreamBodyToClient verifies that
// when synchronous no-cache validation receives a large 200 replacement whose
// disk publication fails at metadata append, Sync, Close, or Rename (or when
// the temp file disappears before writeRecorded), the client still receives
// the full response body rather than an empty body, and no temp file leaks.
func TestLargeObjectValidation200PublishFailuresStreamBodyToClient(t *testing.T) {
	for _, fault := range []string{"append", "sync", "close", "rename"} {
		t.Run(fault, func(t *testing.T) {
			dir := t.TempDir()
			cfg := largeCacheCfg(dir, 1<<10, 32<<10, 256<<10)
			c := newTestCache(t, cfg)

			bodyV1 := bytes.Repeat([]byte("1"), 4<<10)
			bodyV2 := bytes.Repeat([]byte("2"), 8<<10)
			var ver atomic.Int32
			ver.Store(1)

			h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Cache-Control", "public, max-age=3600")
				if ver.Load() == 1 {
					w.Header().Set("ETag", `"v1"`)
					w.Header().Set("Content-Length", strconv.Itoa(len(bodyV1)))
					_, _ = w.Write(bodyV1)
					return
				}
				w.Header().Set("ETag", `"v2"`)
				w.Header().Set("Content-Length", strconv.Itoa(len(bodyV2)))
				_, _ = w.Write(bodyV2)
			}))

			// Warm v1 on disk.
			wantResult(t, get(t, h, "http://example.com/val-fail"), stateMiss, string(bodyV1))
			wantResult(t, get(t, h, "http://example.com/val-fail"), stateHit, string(bodyV1))

			ver.Store(2)
			c.disk.testPublishFault = fault
			t.Cleanup(func() { c.disk.testPublishFault = "" })

			rec := get(t, h, "http://example.com/val-fail", "Cache-Control", "no-cache")
			wantResult(t, rec, stateMiss, string(bodyV2))

			req := httptest.NewRequest(http.MethodGet, "http://example.com/val-fail", nil)
			if _, _, ok := c.lookup(key(req), req); ok {
				t.Fatalf("fault=%s: stale or failed replacement entry remained in cache", fault)
			}
			if fails, failing, ok := c.DiskWriteHealth(); !ok || fails == 0 || !failing {
				t.Fatalf("fault=%s: DiskWriteHealth = (%d, %v, %v), want failures > 0 and failing=true", fault, fails, failing, ok)
			}
			assertNoTempFiles(t, dir)
		})
	}
}

// TestLargeObjectLookupVsReplacementAndEvictionRaces deterministically pauses
// diskStore.getWithMemCap immediately after acquiring the blob lease and
// releasing d.mu, proving that:
//  1. A concurrent replacement retiring v1 and publishing v2 at the same key
//     does not cause the in-flight lookup to read v2's metadata with v1's blob
//     or delete v2 on cleanup.
//  2. Even if v1's retired file is corrupted/removed while v2 is live,
//     delIfBlob removes only v1 and leaves v2 intact.
//  3. A concurrent eviction retiring v1 while lookup is in flight still allows
//     the leased lookup to read v1 and clean up the retired file on Close.
func TestLargeObjectLookupVsReplacementAndEvictionRaces(t *testing.T) {
	t.Run("lookup vs replacement keeps generations isolated", func(t *testing.T) {
		dir := t.TempDir()
		d, err := newDiskStore(dir, 256<<10, testLogger())
		if err != nil {
			t.Fatal(err)
		}
		v1 := &Entry{Status: http.StatusOK, ETag: `"v1"`, Body: bytes.Repeat([]byte("A"), 2048), ExpiresAt: time.Now().Add(time.Hour)}
		v2 := &Entry{Status: http.StatusOK, ETag: `"v2"`, Body: bytes.Repeat([]byte("B"), 3072), ExpiresAt: time.Now().Add(time.Hour)}
		d.set("race-key", v1)

		paused := make(chan struct{})
		resume := make(chan struct{})
		var once sync.Once
		d.onLookupLeaseAcquire = func() {
			once.Do(func() {
				close(paused)
				<-resume
			})
		}

		var gotEntry *Entry
		var gotOK bool
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			gotEntry, gotOK = d.get("race-key")
		}()

		<-paused
		d.onLookupLeaseAcquire = nil
		// Replace v1 with v2 while the lookup is paused after d.mu.Unlock().
		d.set("race-key", v2)
		close(resume)
		wg.Wait()

		if !gotOK || gotEntry == nil {
			t.Fatal("in-flight lookup failed during replacement")
		}
		if gotEntry.ETag != `"v1"` || !bytes.Equal(gotEntry.Body, v1.Body) {
			t.Fatalf("in-flight lookup mixed generations: ETag=%q len=%d, want v1 (%d)", gotEntry.ETag, len(gotEntry.Body), len(v1.Body))
		}
		// v2 must still be indexed and readable!
		after, ok := d.get("race-key")
		if !ok || after == nil || after.ETag != `"v2"` || !bytes.Equal(after.Body, v2.Body) {
			t.Fatalf("v2 was lost or corrupted after in-flight v1 lookup: ok=%v entry=%+v", ok, after)
		}
		assertNoTempFiles(t, dir)
	})

	t.Run("stale lookup error does not delete newly published generation", func(t *testing.T) {
		dir := t.TempDir()
		d, err := newDiskStore(dir, 256<<10, testLogger())
		if err != nil {
			t.Fatal(err)
		}
		v1 := &Entry{Status: http.StatusOK, ETag: `"v1"`, Body: bytes.Repeat([]byte("A"), 2048), ExpiresAt: time.Now().Add(time.Hour)}
		v2 := &Entry{Status: http.StatusOK, ETag: `"v2"`, Body: bytes.Repeat([]byte("B"), 3072), ExpiresAt: time.Now().Add(time.Hour)}
		d.set("corrupt-race-key", v1)
		hash := hashKey("corrupt-race-key")
		v1Blob := d.items[hash].Value.(*diskItem).blob

		paused := make(chan struct{})
		resume := make(chan struct{})
		var once sync.Once
		d.onLookupLeaseAcquire = func() {
			once.Do(func() {
				close(paused)
				<-resume
			})
		}

		var gotOK bool
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, gotOK = d.get("corrupt-race-key")
		}()

		<-paused
		d.onLookupLeaseAcquire = nil
		// Replace v1 with v2 (retiring v1Blob to .jul-cache-retired-*) and corrupt v1Blob's retired file.
		d.set("corrupt-race-key", v2)
		v1Blob.mu.Lock()
		retiredPath := v1Blob.path
		v1Blob.mu.Unlock()
		_ = os.WriteFile(retiredPath, []byte("corrupt"), 0o600)
		close(resume)
		wg.Wait()

		if gotOK {
			t.Fatal("expected lookup of corrupted v1 to return false")
		}
		// v2 MUST NOT have been deleted by v1's delIfBlob cleanup!
		after, ok := d.get("corrupt-race-key")
		if !ok || after == nil || after.ETag != `"v2"` || !bytes.Equal(after.Body, v2.Body) {
			t.Fatalf("v2 was deleted by stale lookup failure: ok=%v", ok)
		}
		assertNoTempFiles(t, dir)
	})

	t.Run("lookup vs eviction completes from leased handle and cleans up", func(t *testing.T) {
		dir := t.TempDir()
		d, err := newDiskStore(dir, 256<<10, testLogger())
		if err != nil {
			t.Fatal(err)
		}
		v1 := &Entry{Status: http.StatusOK, Header: http.Header{"ETag": {`"v1"`}}, Body: bytes.Repeat([]byte("E"), 4096), ExpiresAt: time.Now().Add(time.Hour)}
		d.set("evict-race-key", v1)

		paused := make(chan struct{})
		resume := make(chan struct{})
		var once sync.Once
		d.onLookupLeaseAcquire = func() {
			once.Do(func() {
				close(paused)
				<-resume
			})
		}

		var gotEntry *Entry
		var gotOK bool
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			gotEntry, gotOK = d.get("evict-race-key")
		}()

		<-paused
		d.onLookupLeaseAcquire = nil
		d.Resize(1)
		close(resume)
		wg.Wait()

		if !gotOK || gotEntry == nil || !bytes.Equal(gotEntry.Body, v1.Body) {
			t.Fatalf("expected in-flight lookup to succeed across eviction: ok=%v", gotOK)
		}
		if _, ok := d.get("evict-race-key"); ok {
			t.Fatal("expected evicted key to be absent on subsequent lookup")
		}
		assertNoTempFiles(t, dir)
	})
}

// TestLargeObjectExactMaxEntrySizeWithMinimumDiskMaxSize verifies that when
// max_entry_size == disk_max_size (the minimum permitted disk_max_size), a
// response whose body is exactly max_entry_size is cached on disk without
// self-evicting despite the JULDCH02 metadata+trailer envelope, and survives
// restart rehydration.
func TestLargeObjectExactMaxEntrySizeWithMinimumDiskMaxSize(t *testing.T) {
	dir := t.TempDir()
	const (
		memMax   = 2 << 10  // 2 KiB
		maxEntry = 16 << 10 // 16 KiB
		diskMax  = 16 << 10 // 16 KiB (minimum permitted disk_max_size == max_entry_size)
	)
	c := newTestCache(t, largeCacheCfg(dir, memMax, maxEntry, diskMax))
	exactBody := bytes.Repeat([]byte("M"), maxEntry)

	var calls atomic.Int32
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("Content-Length", strconv.Itoa(len(exactBody)))
		_, _ = w.Write(exactBody)
	}))

	wantResult(t, get(t, h, "http://example.com/exact-boundary"), stateMiss, string(exactBody))
	wantResult(t, get(t, h, "http://example.com/exact-boundary"), stateHit, string(exactBody))
	if calls.Load() != 1 {
		t.Fatalf("origin calls = %d, want 1 (exact max_entry_size must not self-evict)", calls.Load())
	}
	_, diskStats := c.Stats()
	if diskStats == nil || diskStats.Entries != 1 || diskStats.Evictions != 0 {
		t.Fatalf("disk stats = %+v, want Entries=1 Evictions=0", diskStats)
	}
	reqExact := httptest.NewRequest(http.MethodGet, "http://example.com/exact-boundary", nil)
	fi, err := os.Stat(c.disk.path(hashKey(key(reqExact))))
	if err != nil {
		t.Fatalf("stat published exact-boundary file: %v", err)
	}
	if diskStats.Bytes != fi.Size() || diskStats.Bytes <= diskMax {
		t.Fatalf("diskStats.Bytes = %d, want physical file size %d (> %d)", diskStats.Bytes, fi.Size(), diskMax)
	}

	// Rehydrate a fresh Cache over the same directory and verify the exact-boundary entry
	// and its physical occupancy survive restart.
	c2 := newTestCache(t, largeCacheCfg(dir, memMax, maxEntry, diskMax))
	h2 := c2.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("origin should not be called after restart rehydration of exact-boundary entry")
	}))
	wantResult(t, get(t, h2, "http://example.com/exact-boundary"), stateHit, string(exactBody))
	if _, diskStats2 := c2.Stats(); diskStats2 == nil || diskStats2.Bytes != fi.Size() {
		t.Fatalf("rehydrated diskStats = %+v, want Bytes=%d", diskStats2, fi.Size())
	}

	// Also verify that if disk_max_size is resized below s.bodyLen before publishStream,
	// publishStream rejects the entry and returns false rather than self-evicting and returning true.
	s := c2.disk.beginStreamCapture(nil)
	if s == nil {
		t.Fatal("expected beginStreamCapture to succeed")
	}
	defer c2.disk.abortStream(s)
	if ok, _ := c2.disk.writeStream(s, bytes.Repeat([]byte("X"), 4<<10)); !ok {
		t.Fatal("expected writeStream to succeed")
	}
	c2.disk.Resize(1 << 10) // 1 KiB < 4 KiB bodyLen
	if _, ok := c2.disk.publishStream("oversize-after-resize", &Entry{Status: 200}, s); ok {
		t.Fatal("expected publishStream to return false when bodyLen > d.maxBytes")
	}
}

// TestLargeObjectConcurrent304RewriteRespectsTempDiskBudget verifies that a
// 304 Not Modified metadata refresh of a large disk-backed entry reserves its
// temporary copy against tempBudget (bounded by disk_max_size): when concurrent
// captures already hold the budget, the 304 rewrite is skipped with
// reason="budget" while retaining the previous disk entry and serving the
// revalidated response cleanly.
func TestLargeObjectConcurrent304RewriteRespectsTempDiskBudget(t *testing.T) {
	dir := t.TempDir()
	const (
		memMax   = 1 << 10  // 1 KiB
		maxEntry = 16 << 10 // 16 KiB
		diskMax  = 20 << 10 // 20 KiB total disk & temp budget
	)
	c := newTestCache(t, largeCacheCfg(dir, memMax, maxEntry, diskMax))
	var skips skipLog
	c.SetCaptureSkipObserver(skips.add)

	body12K := bytes.Repeat([]byte("R"), 12<<10)
	holdStarted := make(chan struct{})
	holdRelease := make(chan struct{})

	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hold" {
			w.Header().Set("Cache-Control", "public, max-age=3600")
			w.Header().Set("Content-Length", strconv.Itoa(len(body12K)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body12K)
			close(holdStarted)
			<-holdRelease
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("ETag", `"v1"`)
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.Header().Set("X-Refreshed", "yes")
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body12K)))
		_, _ = w.Write(body12K)
	}))

	// 1. Warm the 12 KiB disk-backed entry.
	wantResult(t, get(t, h, "http://example.com/reval-budget"), stateMiss, string(body12K))

	// 2. Hold 12 KiB of tempBudget on a concurrent large miss.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://example.com/hold", nil))
	}()
	<-holdStarted

	if got := c.disk.tempBudget.inflight.Load(); got != int64(len(body12K)) {
		t.Fatalf("tempBudget while held = %d, want %d", got, len(body12K))
	}

	// 3. Trigger a 304 Not Modified on /reval-budget: 12 KiB + 12 KiB = 24 KiB > 20 KiB disk_max_size.
	// The 304 response must still be served from the retained disk entry, while the disk rewrite
	// is skipped with reason="budget".
	rec := get(t, h, "http://example.com/reval-budget", "Cache-Control", "no-cache")
	wantResult(t, rec, stateRevalidated, string(body12K))
	if !sameReasons(skips.get(), captureSkipBudget) {
		t.Fatalf("skip reasons during over-budget 304 = %v, want [budget]", skips.get())
	}
	// While /hold is still in flight, the retained /reval-budget entry must still serve as a HIT!
	wantResult(t, get(t, h, "http://example.com/reval-budget"), stateHit, string(body12K))

	close(holdRelease)
	wg.Wait()
	assertNoTempFiles(t, dir)
}
