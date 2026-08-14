// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build soak

package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

// TestSoak drives sustained concurrent traffic through a real reverse-proxy
// handler and asserts the process stays healthy over time: every request
// succeeds, the goroutine count returns to a steady state, and the heap does
// not grow without bound (a leak gate).
//
// This is the in-tree backing for the post-GA soak gate of ADR 0005: a soak
// failure on a GA feature is a release-blocking regression. The test is excluded
// from the normal `go test ./...` run by the `soak` build tag — scripts/soak.sh
// (and the release workflow) run it explicitly. The duration and concurrency are
// env-tunable so CI smoke runs finish in seconds while a release run can soak for
// minutes:
//
//	SOAK_DURATION  wall-clock run time (default 30s)
//	SOAK_WORKERS   concurrent clients  (default 16)
func TestSoak(t *testing.T) {
	duration := soakEnvDuration("SOAK_DURATION", 30*time.Second)
	workers := soakEnvInt("SOAK_WORKERS", 16)

	// A trivial, fast backend so the soak stresses the proxy data path
	// (connection reuse, header rewriting, request/response body copy) rather
	// than the backend itself.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("X-Soak", "ok")
		_, _ = io.WriteString(w, "ok")
	}))
	defer backend.Close()

	h, err := NewProxy(context.Background(), config.ServerConfig{}, config.LocationConfig{ProxyPass: backend.URL}, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	front := httptest.NewServer(h)
	defer front.Close()

	client := &http.Client{Timeout: 5 * time.Second}

	// Warm up so connection pools and lazily-initialised state are established
	// before the baseline sample — otherwise ordinary warm-up reads as a "leak".
	soakWarm(t, client, front.URL)
	baseGoroutines, baseHeap := soakSample()

	var (
		reqs atomic.Int64
		errs atomic.Int64
		wg   sync.WaitGroup
	)
	deadline := time.Now().Add(duration)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				resp, err := client.Get(front.URL)
				if err != nil {
					errs.Add(1)
					continue
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					errs.Add(1)
				}
				reqs.Add(1)
			}
		}()
	}
	wg.Wait()

	if reqs.Load() == 0 {
		t.Fatal("no requests completed; the soak did not exercise the proxy")
	}
	if e := errs.Load(); e > 0 {
		t.Fatalf("soak saw %d request errors over %d requests; want 0", e, reqs.Load())
	}

	// Let idle keep-alive connections drain, then force GC so the post-run sample
	// reflects retained (leaked) state rather than transient allocation.
	client.CloseIdleConnections()
	time.Sleep(250 * time.Millisecond)
	endGoroutines, endHeap := soakSample()

	t.Logf("soak: duration=%s workers=%d requests=%d errors=0", duration, workers, reqs.Load())
	t.Logf("soak: goroutines %d -> %d, heap %d -> %d bytes", baseGoroutines, endGoroutines, baseHeap, endHeap)

	// Goroutine gate: a genuine per-request leak grows in proportion to the
	// (tens of thousands of) requests served, so a generous constant slack for
	// lingering pooled-connection reapers still catches it decisively.
	if growth := endGoroutines - baseGoroutines; growth > 4*workers+32 {
		t.Errorf("goroutine leak: grew by %d (%d -> %d)", growth, baseGoroutines, endGoroutines)
	}
	// Heap gate: bounded post-GC growth. A leak in the hot path would balloon the
	// heap far past this budget after a sustained run.
	const heapBudget = 64 << 20 // 64 MiB
	if growth := int64(endHeap) - int64(baseHeap); growth > heapBudget {
		t.Errorf("heap growth %d bytes exceeds budget %d bytes", growth, heapBudget)
	}
}

// soakWarm sends a burst of requests so pools and one-time state exist before the
// baseline sample is taken.
func soakWarm(t *testing.T, c *http.Client, url string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		resp, err := c.Get(url)
		if err != nil {
			t.Fatalf("warm-up request: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

// soakSample forces GC and returns the current goroutine count and live heap.
func soakSample() (goroutines int, heap uint64) {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return runtime.NumGoroutine(), m.HeapAlloc
}

func soakEnvDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}

func soakEnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
