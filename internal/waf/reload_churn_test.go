// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf

package waf

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"

	"jul/internal/config"
)

// TestWAFReloadChurnNoLeak is the AUX-06 (#50) runtime validation that rebuilding
// the WAF engine on every configuration reload leaks neither goroutines nor heap,
// across inline-rule, full-CRS, detect-mode, and a mixed all-profiles permutation.
//
// On each reload the server compiles a fresh Coraza engine from the new WAF
// policy and drops the previous generation without an explicit Close (Firewall
// owns no background worker, timer, or socket, so Close is a documented no-op —
// see firewall.go). This mirrors the auth reload-churn proof (#31): the safety
// of that drop-without-close is a runtime invariant, and this test asserts it
// under a sustained build+exercise+drop churn that must return to its pre-churn
// goroutine and heap baseline. A per-reload leak — a retained engine, its
// compiled rule program, or a stray goroutine — would grow in proportion to the
// churn count and trip the flat slack/budget below; connection and allocator
// jitter is bounded, not proportional.
//
// The full OWASP CRS compile is the heaviest and most plausible leak site, so
// the cycle count is deliberately modest by default and env-tunable for a
// dedicated long soak lane:
//
//	WAF_CHURN_ITERS  reload cycles per permutation (default 30)
func TestWAFReloadChurnNoLeak(t *testing.T) {
	iters := churnEnvInt("WAF_CHURN_ITERS", 30)

	// A benign request that must always reach the action, and a set of attack
	// probes that must be blocked (or, in detect mode, recorded-but-allowed).
	benign := func() *http.Request { return httptest.NewRequest(http.MethodGet, "/health", nil) }
	traversal := func() *http.Request { return httptest.NewRequest(http.MethodGet, "/?file=../../../etc/passwd", nil) }
	xss := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/search", nil)
		r.Header.Set("User-Agent", `<script>alert('XSS')</script>`)
		return r
	}

	// serve builds a fresh firewall from cfg (as a reload does), runs one request
	// through it, and drops it. It returns the response status and event count so
	// a cycle can assert the freshly built engine actually enforced.
	serve := func(t *testing.T, cfg config.WAFConfig, req *http.Request) (int, int) {
		t.Helper()
		applyTestDefaults(&cfg)
		rec := &eventRecorder{}
		fw, err := New(context.Background(), cfg, Options{Hooks: Hooks{OnEvent: rec.onEvent}})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		rr := httptest.NewRecorder()
		fw.Middleware()(newOKHandler()).ServeHTTP(rr, req)
		return rr.Code, rec.count()
	}

	t.Run("inline", func(t *testing.T) {
		cfg := config.WAFConfig{
			Enabled:     true,
			Mode:        "block",
			InlineRules: `SecRule REQUEST_URI "@contains /forbidden" "id:100,phase:1,deny,status:403,log,msg:'blocked path'"`,
		}
		cycle := func() {
			if code, _ := serve(t, cfg, httptest.NewRequest(http.MethodGet, "/allowed", nil)); code != http.StatusOK {
				t.Fatalf("inline benign: code=%d, want 200", code)
			}
			if code, ev := serve(t, cfg, httptest.NewRequest(http.MethodGet, "/forbidden/x", nil)); code != http.StatusForbidden || ev == 0 {
				t.Fatalf("inline attack: code=%d events=%d, want 403 with >=1 event", code, ev)
			}
		}
		cycle()
		churn(t, iters, cycle)
	})

	t.Run("crs", func(t *testing.T) {
		// The full embedded OWASP CRS: the heaviest compile and the leak site
		// that matters most operationally (ruleset changes reload this program).
		cfg := config.WAFConfig{Enabled: true, Mode: "block", CRSEnabled: true, Paranoia: 1}
		cycle := func() {
			if code, _ := serve(t, cfg, benign()); code != http.StatusOK {
				t.Fatalf("crs benign: code=%d, want 200", code)
			}
			if code, ev := serve(t, cfg, traversal()); code == http.StatusOK || ev == 0 {
				t.Fatalf("crs traversal: code=%d events=%d, want a block with >=1 event", code, ev)
			}
		}
		cycle()
		churn(t, iters, cycle)
	})

	t.Run("detect", func(t *testing.T) {
		// Detect-mode CRS: the engine still compiles the whole ruleset and runs
		// every phase, it just records instead of blocking. Exercises the
		// enable→detect reconfiguration path.
		cfg := config.WAFConfig{Enabled: true, Mode: "detect", CRSEnabled: true, Paranoia: 1}
		cycle := func() {
			if code, ev := serve(t, cfg, xss()); code != http.StatusOK || ev == 0 {
				t.Fatalf("detect xss: code=%d events=%d, want 200 (allowed) with >=1 event", code, ev)
			}
		}
		cycle()
		churn(t, iters, cycle)
	})

	t.Run("mixed", func(t *testing.T) {
		// A realistic operator churn flips between rule sets and modes: an inline
		// policy, a CRS block policy, and a CRS detect policy, each rebuilt every
		// cycle to stress the whole compile+drop path across ruleset variety.
		inline := config.WAFConfig{
			Enabled:     true,
			Mode:        "block",
			InlineRules: `SecRule REQUEST_URI "@contains /blockme" "id:200,phase:1,deny,status:403,log"`,
		}
		crsBlock := config.WAFConfig{Enabled: true, Mode: "block", CRSEnabled: true, Paranoia: 1}
		crsDetect := config.WAFConfig{Enabled: true, Mode: "detect", CRSEnabled: true, Paranoia: 2}
		cycle := func() {
			if code, ev := serve(t, inline, httptest.NewRequest(http.MethodGet, "/blockme", nil)); code != http.StatusForbidden || ev == 0 {
				t.Fatalf("mixed inline: code=%d events=%d, want 403 with >=1 event", code, ev)
			}
			if code, ev := serve(t, crsBlock, traversal()); code == http.StatusOK || ev == 0 {
				t.Fatalf("mixed crs-block: code=%d events=%d, want a block with >=1 event", code, ev)
			}
			if code, _ := serve(t, crsDetect, benign()); code != http.StatusOK {
				t.Fatalf("mixed crs-detect benign: code=%d, want 200", code)
			}
		}
		cycle()
		churn(t, iters, cycle)
	})
}

// goroutineChurnSlack is the maximum tolerated growth in live goroutines across a
// full churn run. It is a flat constant — independent of the reload count — so a
// genuine per-reload goroutine leak (which scales with iterations) trips it
// immediately. The WAF engine spawns no goroutines and the churn does no network
// I/O, so growth is expected to be ~0; the slack only absorbs scheduler jitter.
const goroutineChurnSlack = 20

// heapChurnBudget bounds post-GC heap growth across a churn run. A compiled CRS
// engine is large (hundreds of rules, compiled regex), so a retained-per-reload
// leak would add hundreds of MiB over the run; a bounded budget catches that
// while tolerating allocator fragmentation and lazily retained embedded-CRS
// asset caches.
const heapChurnBudget = 64 << 20 // 64 MiB

// churn warms up, records a settled goroutine/heap baseline, runs iters
// build+exercise+drop cycles, then asserts the process returned to baseline
// within the leak thresholds.
func churn(t *testing.T, iters int, cycle func()) {
	t.Helper()
	for i := 0; i < 4; i++ { // warm lazy caches/goroutines before baseline
		cycle()
	}
	baseG := stableGoroutines()
	_, baseHeap := churnSample()

	for i := 0; i < iters; i++ {
		cycle()
	}

	endG := stableGoroutines()
	_, endHeap := churnSample()

	t.Logf("waf reload churn: iters=%d goroutines %d -> %d (slack %d), heap %d -> %d bytes (budget %d)",
		iters, baseG, endG, goroutineChurnSlack, baseHeap, endHeap, heapChurnBudget)

	if growth := endG - baseG; growth > goroutineChurnSlack {
		t.Errorf("goroutine leak: grew by %d (%d -> %d) over %d reloads", growth, baseG, endG, iters)
	}
	if growth := int64(endHeap) - int64(baseHeap); growth > int64(heapChurnBudget) {
		t.Errorf("heap leak: grew by %d bytes (budget %d) over %d reloads", growth, int64(heapChurnBudget), iters)
	}
}

// churnSample forces GC and returns the current goroutine count and live heap.
func churnSample() (goroutines int, heap uint64) {
	runtime.GC()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return runtime.NumGoroutine(), ms.HeapAlloc
}

// stableGoroutines returns the low-water goroutine count over a short settling
// window, filtering transient goroutines the runtime spins up around a cycle.
func stableGoroutines() int {
	lo := int(^uint(0) >> 1)
	for i := 0; i < 12; i++ {
		runtime.GC()
		if n := runtime.NumGoroutine(); n < lo {
			lo = n
		}
		time.Sleep(10 * time.Millisecond)
	}
	return lo
}

func churnEnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
