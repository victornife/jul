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
//	./jul -config testdata/plugins.toml
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
	flag.Parse()

	target := *baseURL + *pluginPath
	fmt.Printf("WASM burn-in: target=%s workers=%d duration=%s\n", target, *workers, *duration)
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

	if f > 0 || m > 0 {
		fmt.Fprintln(os.Stderr, "\nSoak FAILED: non-zero errors or missing plugin headers detected.")
		os.Exit(1)
	}
	fmt.Println("\nSoak PASSED: 0 errors, plugin responses verified.")
}
