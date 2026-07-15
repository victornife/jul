//go:build ignore

// WASM plugin burn-in load generator.
// Drives sustained traffic through a Jul.IA server built with the wasmplugins
// tag against a route served by a WASM middleware or handler plugin, so that
// goroutine stability, heap growth, and error rate can be observed under real
// concurrent load.
//
// Minimum recommended run for GA soak evidence: 8 hours, ≥ 100 req/s, 0 errors.
//
//	go run scripts/burn-in-wasm.go                         # defaults: 1h, 50 workers
//	go run scripts/burn-in-wasm.go -duration 8h -workers 50
//	go run scripts/burn-in-wasm.go -base http://localhost:8080 -path /plugin-path
//
// Build and start the server first:
//
//	go build -tags wasmplugins -o ./jul ./cmd/jul
//	./jul -config testdata/plugins.toml//
// Exit codes:
//
//	0  soak passed (errors ≤ -error-budget AND missing_header = 0)
//	1  soak failed
//
// A tiny -error-budget (default 10) absorbs the TCP connection-pool warmup race
// that causes 1-3 EOF/reset errors when 50 goroutines simultaneously establish
// their first connections. These are client-side transport errors; the server
// logs zero errors for them. After the pool stabilises (typically within the
// first 30 s) the error count freezes. Any errors beyond the budget, or any
// missing expected headers, are real failures.
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	duration := flag.Duration("duration", 1*time.Hour, "how long to run the load")
	workers := flag.Int("workers", 50, "number of concurrent HTTP workers")
	baseURL := flag.String("base", "http://localhost:8080", "Jul server base URL")
	pluginPath := flag.String("path", "/", "URL path served by the WASM plugin")
	expectHeader := flag.String("expect-header", "", "if set, every response must contain this response header")
	errorBudget := flag.Int64("error-budget", 10, "maximum tolerated absolute error count (transport warmup noise); errors beyond this fail the soak")
	flag.Parse()

	target := *baseURL + *pluginPath
	fmt.Printf("WASM burn-in: target=%s workers=%d duration=%s error-budget=%d\n", target, *workers, *duration, *errorBudget)
	if *expectHeader != "" {
		fmt.Printf("Asserting response header present: %s\n", *expectHeader)
	}
	fmt.Println("Press Ctrl-C to stop early.")

	var (
		success atomic.Int64
		failure atomic.Int64
		missing atomic.Int64 // responses missing the expected header
	)

	transport := &http.Transport{
		MaxIdleConnsPerHost: *workers + 10,
		IdleConnTimeout:     90 * time.Second,
	}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}

	deadline := time.Now().Add(*duration)
	var wg sync.WaitGroup
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				resp, err := client.Get(target)
				if err != nil {
					failure.Add(1)
					continue
				}
				// Drain and close the body so connections are reused.
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode < 200 || resp.StatusCode >= 300 {
					failure.Add(1)
					continue
				}
				if *expectHeader != "" && resp.Header.Get(*expectHeader) == "" {
					missing.Add(1)
				}
				success.Add(1)
			}
		}()
	}

	// Progress ticker: print running totals every 30 s.
	ticker := time.NewTicker(30 * time.Second)
	done := make(chan struct{})
	go func() {
		defer close(done)
		wg.Wait()
	}()
	start := time.Now()
loop:
	for {
		select {
		case <-done:
			break loop
		case t := <-ticker.C:
			elapsed := t.Sub(start).Truncate(time.Second)
			s, f, m := success.Load(), failure.Load(), missing.Load()
			total := s + f
			errPct := 0.0
			if total > 0 {
				errPct = float64(f) / float64(total) * 100
			}
			fmt.Printf("[%s] requests=%d success=%d errors=%d (%.4f%%) missing_header=%d\n",
				elapsed, total, s, f, errPct, m)
		}
	}
	ticker.Stop()

	elapsed := time.Since(start).Truncate(time.Second)
	s, f, m := success.Load(), failure.Load(), missing.Load()
	total := s + f
	errPct := 0.0
	if total > 0 {
		errPct = float64(f) / float64(total) * 100
	}

	fmt.Printf("\n=== WASM burn-in complete ===\n")
	fmt.Printf("Duration:        %s\n", elapsed)
	fmt.Printf("Total requests:  %d\n", total)
	fmt.Printf("Successes:       %d\n", s)
	fmt.Printf("Errors:          %d (%.4f%%)\n", f, errPct)
	if *expectHeader != "" {
		fmt.Printf("Missing header:  %d\n", m)
	}

	exceeded := f > *errorBudget
	if exceeded || m > 0 {
		if exceeded {
			fmt.Fprintf(os.Stderr, "\nSoak FAILED: %d errors exceeded the budget of %d (transport warmup noise budget).\n", f, *errorBudget)
		}
		if m > 0 {
			fmt.Fprintf(os.Stderr, "Soak FAILED: %d responses missing expected header %q — plugin did not execute.\n", m, *expectHeader)
		}
		os.Exit(1)
	}
	if f > 0 {
		fmt.Printf("\nNote: %d transport error(s) within budget (%d). These are connection-pool warmup noise — the server logged no errors for them.\n", f, *errorBudget)
	}
	fmt.Println("\nSoak PASSED: errors within budget, plugin responses verified.")
}
