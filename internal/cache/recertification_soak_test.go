// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build soak

package cache

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

// TestCacheRecertificationSoak drives the corrected shared-cache contract under
// concurrent real HTTP/1.1 traffic. It mixes fresh hits, mandatory 304
// validation, stale reuse, stale-if-error, Vary variants, memory-to-disk
// overflow, unsafe-method invalidation, Range/no-store bypass and SSE. Reload,
// retirement, WebSocket and HTTP/2 interactions are covered by the dedicated
// repeated race and real-server suites recorded in the #134 evidence matrix.
func TestCacheRecertificationSoak(t *testing.T) {
	duration := cacheSoakDuration("CACHE_SOAK_DURATION", 30*time.Second)
	workers := cacheSoakInt("CACHE_SOAK_WORKERS", 16)

	var (
		originRequests atomic.Int64
		originFailures atomic.Int64
		failErrorPath  atomic.Bool
	)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originRequests.Add(1)
		switch r.URL.Path {
		case "/fresh":
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.Header().Set("Cache-Control", "max-age=60")
			_, _ = io.WriteString(w, "fresh")
		case "/validate":
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("ETag", `"validate-v1"`)
			if r.Header.Get("If-None-Match") == `"validate-v1"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			_, _ = io.WriteString(w, "validate")
		case "/stale":
			w.Header().Set("Cache-Control", "max-age=0, stale-while-revalidate=60")
			w.Header().Set("ETag", `"stale-v1"`)
			if r.Header.Get("If-None-Match") == `"stale-v1"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			_, _ = io.WriteString(w, "stale")
		case "/error":
			w.Header().Set("Cache-Control", "no-cache, stale-if-error=60")
			w.Header().Set("ETag", `"error-v1"`)
			if failErrorPath.Load() {
				originFailures.Add(1)
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = io.WriteString(w, "last-good")
		case "/vary":
			w.Header().Set("Cache-Control", "max-age=60")
			w.Header().Set("Vary", "Accept-Language")
			_, _ = io.WriteString(w, r.Header.Get("Accept-Language"))
		case "/large":
			w.Header().Set("Cache-Control", "max-age=60")
			_, _ = io.WriteString(w, strings.Repeat("x", 4096))
		case "/range":
			if r.Header.Get("Range") != "" {
				w.Header().Set("Content-Range", "bytes 0-3/10")
				w.WriteHeader(http.StatusPartialContent)
				_, _ = io.WriteString(w, "0123")
				return
			}
			w.Header().Set("Cache-Control", "max-age=60")
			_, _ = io.WriteString(w, "0123456789")
		case "/sse":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: ok\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	c, err := New(config.CacheConfig{
		Enabled:              true,
		MemoryMaxSize:        config.Size(256 << 10),
		DiskPath:             t.TempDir(),
		DiskMaxSize:          config.Size(2 << 20),
		DefaultTTL:           config.Duration(time.Minute),
		StaleWhileRevalidate: config.Duration(time.Minute),
		StaleIfError:         config.Duration(time.Minute),
	}, nil)
	if err != nil {
		t.Fatalf("New cache: %v", err)
	}
	front := httptest.NewServer(c.Handler(next))
	defer front.Close()

	overflow, err := New(config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(8 << 10),
		DiskPath:      t.TempDir(),
		DiskMaxSize:   config.Size(2 << 20),
		DefaultTTL:    config.Duration(time.Minute),
	}, nil)
	if err != nil {
		t.Fatalf("New overflow cache: %v", err)
	}
	overflowFront := httptest.NewServer(overflow.Handler(next))
	defer overflowFront.Close()

	client := &http.Client{Timeout: 3 * time.Second}
	defer client.CloseIdleConnections()

	for _, path := range []string{"/fresh", "/validate", "/stale", "/error", "/range"} {
		cacheSoakRequest(t, client, http.MethodGet, front.URL+path, nil)
	}
	for _, lang := range []string{"en", "es", "de", "fr"} {
		cacheSoakRequest(t, client, http.MethodGet, front.URL+"/vary", map[string]string{"Accept-Language": lang})
	}
	failErrorPath.Store(true)
	baseG, baseHeap, baseFD := cacheSoakSample()

	var (
		requests           atomic.Int64
		errors             atomic.Int64
		hits               atomic.Int64
		misses             atomic.Int64
		stales             atomic.Int64
		valids             atomic.Int64
		bypasses           atomic.Int64
		staleIfErrorServed atomic.Int64
		clientErrors       atomic.Int64
		serverErrors       atomic.Int64
		scenarioErrors     [10]atomic.Int64
		wg                 sync.WaitGroup
	)
	countResult := func(state string) {
		switch state {
		case stateHit:
			hits.Add(1)
		case stateMiss:
			misses.Add(1)
		case stateStale:
			stales.Add(1)
		case stateRevalidated:
			valids.Add(1)
		case stateBypass:
			bypasses.Add(1)
		}
	}
	deadline := time.Now().Add(duration)
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for seq := 0; time.Now().Before(deadline); seq++ {
				var method, target string
				headers := map[string]string{}
				scenario := (worker + seq) % 10
				switch scenario {
				case 0:
					method, target = http.MethodGet, front.URL+"/fresh"
				case 1:
					method, target = http.MethodGet, front.URL+"/validate"
				case 2:
					method, target = http.MethodGet, front.URL+"/stale"
				case 3:
					method, target = http.MethodGet, front.URL+"/error"
				case 4:
					method, target = http.MethodGet, front.URL+"/vary"
					headers["Accept-Language"] = "lang-" + strconv.Itoa((worker+seq)%8)
				case 5:
					// Exercise memory-to-disk overflow on a separate cache instance.
					// Capacity pressure is therefore concurrent with the correctness
					// workload without making eviction of its stale-if-error control
					// entry an expected result.
					method, target = http.MethodGet, overflowFront.URL+"/large?id="+strconv.Itoa((worker+seq)%256)
				case 6:
					method, target = http.MethodGet, front.URL+"/range"
					headers["Range"] = "bytes=0-3"
				case 7:
					method, target = http.MethodGet, front.URL+"/fresh"
					headers["Cache-Control"] = "no-store"
				case 8:
					method, target = http.MethodGet, front.URL+"/sse"
				default:
					if _, status, err := cacheSoakDo(client, http.MethodPost, front.URL+"/fresh", nil); err != nil || status != http.StatusNoContent {
						errors.Add(1)
						scenarioErrors[scenario].Add(1)
						if err != nil {
							clientErrors.Add(1)
						} else {
							serverErrors.Add(1)
						}
					}
					requests.Add(1)
					method, target = http.MethodGet, front.URL+"/fresh"
				}
				state, status, err := cacheSoakDo(client, method, target, headers)
				requests.Add(1)
				if err != nil || status >= 500 || status == 0 {
					errors.Add(1)
					scenarioErrors[scenario].Add(1)
					if err != nil {
						clientErrors.Add(1)
					} else {
						serverErrors.Add(1)
					}
					continue
				}
				if scenario == 3 && state == stateStale {
					staleIfErrorServed.Add(1)
				}
				countResult(state)
			}
		}(worker)
	}
	wg.Wait()

	settleDeadline := time.Now().Add(5 * time.Second)
	for (c.inflightRevalidations() != 0 || overflow.inflightRevalidations() != 0) && time.Now().Before(settleDeadline) {
		time.Sleep(10 * time.Millisecond)
	}
	client.CloseIdleConnections()
	time.Sleep(250 * time.Millisecond)
	endG, endHeap, endFD := cacheSoakSample()
	memBytes, memMax, diskBytes, diskMax := cacheSoakUsage(c)
	overflowMemBytes, overflowMemMax, overflowDiskBytes, overflowDiskMax := cacheSoakUsage(overflow)

	t.Logf("cache soak: duration=%s workers=%d requests=%d errors=%d origin_requests=%d origin_5xx=%d", duration, workers, requests.Load(), errors.Load(), originRequests.Load(), originFailures.Load())
	t.Logf("cache soak results: HIT=%d MISS=%d STALE=%d REVALIDATED=%d BYPASS=%d stale_if_error=%d", hits.Load(), misses.Load(), stales.Load(), valids.Load(), bypasses.Load(), staleIfErrorServed.Load())
	t.Logf("cache soak error distribution: fresh=%d validate=%d stale=%d error=%d vary=%d large=%d range=%d no_store=%d sse=%d invalidation=%d client=%d server=%d",
		scenarioErrors[0].Load(), scenarioErrors[1].Load(), scenarioErrors[2].Load(), scenarioErrors[3].Load(), scenarioErrors[4].Load(),
		scenarioErrors[5].Load(), scenarioErrors[6].Load(), scenarioErrors[7].Load(), scenarioErrors[8].Load(), scenarioErrors[9].Load(),
		clientErrors.Load(), serverErrors.Load())
	t.Logf("cache soak resources: goroutines %d -> %d, heap %d -> %d, fds %d -> %d, primary memory %d/%d disk %d/%d, overflow memory %d/%d disk %d/%d", baseG, endG, baseHeap, endHeap, baseFD, endFD, memBytes, memMax, diskBytes, diskMax, overflowMemBytes, overflowMemMax, overflowDiskBytes, overflowDiskMax)

	if requests.Load() == 0 || originRequests.Load() == 0 {
		t.Fatal("soak did not exercise requests and origin traffic")
	}
	if got := errors.Load(); got != 0 {
		t.Fatalf("cache soak saw %d unexpected request errors", got)
	}
	if staleIfErrorServed.Load() == 0 {
		t.Error("cache soak never served stale-if-error on the failing origin path")
	}
	for state, count := range map[string]int64{
		stateHit: hits.Load(), stateMiss: misses.Load(), stateStale: stales.Load(),
		stateRevalidated: valids.Load(), stateBypass: bypasses.Load(),
	} {
		if count == 0 {
			t.Errorf("cache soak never observed %s", state)
		}
	}
	if c.inflightRevalidations() != 0 || overflow.inflightRevalidations() != 0 {
		t.Errorf("stranded revalidation calls: primary=%d overflow=%d", c.inflightRevalidations(), overflow.inflightRevalidations())
	}
	if memBytes > memMax || diskBytes > diskMax || overflowMemBytes > overflowMemMax || overflowDiskBytes > overflowDiskMax {
		t.Errorf("cache capacity exceeded: primary memory %d/%d disk %d/%d; overflow memory %d/%d disk %d/%d", memBytes, memMax, diskBytes, diskMax, overflowMemBytes, overflowMemMax, overflowDiskBytes, overflowDiskMax)
	}
	if overflowDiskBytes == 0 {
		t.Error("cache soak never exercised memory-to-disk overflow")
	}
	if growth := endG - baseG; growth > 4*workers+32 {
		t.Errorf("goroutine growth %d exceeds bound", growth)
	}
	if growth := int64(endHeap) - int64(baseHeap); growth > 64<<20 {
		t.Errorf("heap growth %d exceeds 64 MiB", growth)
	}
	if baseFD >= 0 && endFD-baseFD > 16 {
		t.Errorf("file-descriptor growth %d exceeds bound", endFD-baseFD)
	}
}

func cacheSoakRequest(t *testing.T, client *http.Client, method, target string, headers map[string]string) {
	t.Helper()
	_, status, err := cacheSoakDo(client, method, target, headers)
	if err != nil || status >= 500 {
		t.Fatalf("prime %s %s: status=%d err=%v", method, target, status, err)
	}
}

func cacheSoakDo(client *http.Client, method, target string, headers map[string]string) (string, int, error) {
	req, err := http.NewRequest(method, target, nil)
	if err != nil {
		return "", 0, err
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, err
	}
	_, readErr := io.Copy(io.Discard, resp.Body)
	closeErr := resp.Body.Close()
	if readErr != nil {
		return resp.Header.Get("X-Cache"), resp.StatusCode, readErr
	}
	return resp.Header.Get("X-Cache"), resp.StatusCode, closeErr
}

func cacheSoakUsage(c *Cache) (memBytes, memMax, diskBytes, diskMax int64) {
	c.mem.mu.Lock()
	memBytes, memMax = c.mem.curBytes, c.mem.maxBytes
	c.mem.mu.Unlock()
	if c.disk != nil {
		c.disk.mu.Lock()
		diskBytes, diskMax = c.disk.curBytes, c.disk.maxBytes
		c.disk.mu.Unlock()
	}
	return
}

func cacheSoakSample() (goroutines int, heap uint64, fds int) {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	fds = -1
	if entries, err := os.ReadDir("/proc/self/fd"); err == nil {
		fds = len(entries)
	}
	return runtime.NumGoroutine(), m.HeapAlloc, fds
}

func cacheSoakDuration(key string, def time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
			return parsed
		}
	}
	return def
}

func cacheSoakInt(key string, def int) int {
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			return parsed
		}
	}
	return def
}
