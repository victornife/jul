// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"

	"jul/internal/background"
	"jul/internal/config"
)

// goroutineChurnSlack absorbs keep-alive and scheduler jitter. A real per-cycle
// leak grows in proportion to the cycle count, so a small constant is a safe
// discriminator.
const goroutineChurnSlack = 8

// fdChurnSlack does the same for open file descriptors, which the disk tier
// opens and closes on every write.
const fdChurnSlack = 8

// TestRevalidationChurnNoLeak is the #131 resource gate: a sustained churn of
// "new generation → stale hit → background revalidation → generation retires"
// must return to its pre-churn goroutine and file-descriptor baseline.
//
// A revalidation that failed to release its lease, left its call state behind,
// or leaked its goroutine or the disk tier's descriptors would grow in
// proportion to the cycle count. Both tiers are exercised so the disk store's
// temp-file/rename path is included.
//
// The cycle count is env-tunable so the default CI run finishes in seconds while
// a dedicated validation lane can soak far longer:
//
//	CACHE_CHURN_ITERS  revalidation cycles (default 200)
func TestRevalidationChurnNoLeak(t *testing.T) {
	iters := churnEnvInt("CACHE_CHURN_ITERS", 200)

	dir := t.TempDir()
	c := newTestCache(t, config.CacheConfig{
		MemoryMaxSize: config.Size(1 << 20),
		DiskPath:      dir,
		DiskMaxSize:   config.Size(8 << 20),
		StaleIfError:  config.Duration(time.Hour),
	})

	// Alternate outcomes so every cleanup path is churned, not just the happy
	// one: a stored refresh, an origin error, and a panicking handler.
	var mode int
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch mode % 3 {
		case 0:
			w.Header().Set("Cache-Control", "max-age=60")
			_, _ = w.Write([]byte("fresh"))
		case 1:
			w.WriteHeader(http.StatusBadGateway)
		default:
			panic("churn panic")
		}
	}))

	gen := uint64(0)
	cycle := func() {
		gen++
		mode++
		// Each cycle is a distinct generation, exactly as a reload produces.
		ctx, cancel := context.WithCancel(context.Background())
		g := background.NewGroup(ctx, background.GroupOptions{
			Generation:   gen,
			MaxOperation: 30 * time.Second,
		})

		r := leased(httptest.NewRequest(http.MethodGet, "http://x/churn", nil), g)
		c.set(key(r), staleEntry("stale"))
		h.ServeHTTP(httptest.NewRecorder(), r)

		// Retire the generation the way the server does: wait for its leased
		// work, then cancel and release the process context.
		if !g.Wait(10 * time.Second) {
			t.Fatalf("cycle %d: leased work did not drain", gen)
		}
		g.Cancel()
		cancel()
	}

	for i := 0; i < 8; i++ { // warm lazy allocations before taking a baseline
		cycle()
	}
	baseG := stableGoroutines()
	baseFD := openFDs(t)

	for i := 0; i < iters; i++ {
		cycle()
	}

	endG := stableGoroutines()
	endFD := openFDs(t)

	t.Logf("revalidation churn: iters=%d goroutines %d -> %d (slack %d), fds %d -> %d (slack %d)",
		iters, baseG, endG, goroutineChurnSlack, baseFD, endFD, fdChurnSlack)

	if growth := endG - baseG; growth > goroutineChurnSlack {
		t.Errorf("goroutine leak: grew by %d (%d -> %d) over %d revalidation cycles", growth, baseG, endG, iters)
	}
	if baseFD >= 0 && endFD >= 0 {
		if growth := endFD - baseFD; growth > fdChurnSlack {
			t.Errorf("file-descriptor leak: grew by %d (%d -> %d) over %d revalidation cycles", growth, baseFD, endFD, iters)
		}
	}
	if n := c.inflightRevalidations(); n != 0 {
		t.Errorf("revalidation call state stranded after churn: %d entries", n)
	}
}

// stableGoroutines returns the goroutine count once it stops moving, so a
// just-finished cycle's teardown is not mistaken for a leak.
func stableGoroutines() int {
	prev := -1
	for i := 0; i < 200; i++ {
		runtime.GC()
		n := runtime.NumGoroutine()
		if n == prev {
			return n
		}
		prev = n
		time.Sleep(5 * time.Millisecond)
	}
	return runtime.NumGoroutine()
}

// openFDs returns the number of descriptors this process holds, or -1 on
// platforms without /proc so the assertion degrades to goroutines only.
func openFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(entries)
}

func churnEnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
