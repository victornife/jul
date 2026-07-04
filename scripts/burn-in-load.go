//go:build ignore

// Track 2 — Real binary burn-in load generator (Go)
// Usage: go run scripts/burn-in-load.go -duration 5m -workers 50
//
// NOTE (2026-07-04): Uses a shared http.Transport across all workers with
// full body drain (io.Copy to discard) for proper connection reuse. This
// avoids Windows ephemeral-port exhaustion that occurs when per-worker
// transports or incomplete reads break HTTP keep-alive.
package main

import (
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var seenErrors sync.Map
var seenErrCount int64

func logErrorOnce(errStr string) {
	if atomic.LoadInt64(&seenErrCount) >= 10 {
		return
	}
	if _, loaded := seenErrors.LoadOrStore(errStr, true); !loaded {
		atomic.AddInt64(&seenErrCount, 1)
		fmt.Printf("[ERR SAMPLE] %s\n", errStr)
	}
}

func main() {
	var (
		duration  = flag.Duration("duration", 5*time.Minute, "How long to run")
		workers   = flag.Int("workers", 50, "Number of concurrent workers")
		baseURL   = flag.String("base", "http://127.0.0.1:8080", "Jul server base URL")
		healthURL = flag.String("health", "http://127.0.0.1:8082/healthz", "Health endpoint")
		adminURL  = flag.String("admin", "http://127.0.0.1:9090", "Admin / pprof base URL")
		pprofDir  = flag.String("out", "burn-in-artifacts", "Directory for pprof snapshots")
	)
	flag.Parse()

	fmt.Println("Burn-in load test starting...")
	fmt.Printf("Duration       : %v\n", *duration)
	fmt.Printf("Workers        : %d\n", *workers)
	fmt.Printf("Target         : %s\n", *baseURL)
	fmt.Printf("Health check   : %s\n", *healthURL)
	fmt.Printf("Admin / pprof  : %s\n", *adminURL)
	fmt.Printf("End time       : %s UTC\n", time.Now().UTC().Add(*duration).Format("15:04:05"))
	fmt.Println()

	// Pre-flight health check
	fmt.Println("Pre-flight health check...")
	if err := healthCheck(*healthURL); err != nil {
		fmt.Printf("Health check FAILED: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Health OK: 200")

	// Ensure artifacts dir
	os.MkdirAll(*pprofDir, 0755)

	// T+0 pprof snapshots
	fmt.Println("Capturing T+0 pprof snapshots...")
	snapPProf(*adminURL, *pprofDir, "T0")

	// Shared transport for proper connection reuse across all workers
	sharedTransport := &http.Transport{
		MaxIdleConns:        *workers * 2,
		MaxIdleConnsPerHost: *workers * 2,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   false,
	}

	endTime := time.Now().Add(*duration)
	var totalReqs, errConnReset, errTimeout, errOther, status5xx int64
	var mu sync.Mutex
	var latencies []int64

	var wg sync.WaitGroup
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := &http.Client{
				Timeout:   10 * time.Second,
				Transport: sharedTransport,
			}
			for time.Now().Before(endTime) {
				path := "/api/"
				if rand.Intn(2) == 0 {
					path = "/static/"
				}
				url := *baseURL + path
				start := time.Now()
				resp, err := client.Get(url)
				d := time.Since(start).Milliseconds()
				atomic.AddInt64(&totalReqs, 1)
				if err != nil {
					errStr := err.Error()
					if os.IsTimeout(err) || (resp != nil && resp.StatusCode == http.StatusRequestTimeout) {
						atomic.AddInt64(&errTimeout, 1)
					} else if strings.Contains(errStr, "connection reset by peer") || strings.Contains(errStr, "EOF") || strings.Contains(errStr, "broken pipe") {
						atomic.AddInt64(&errConnReset, 1)
					} else {
						atomic.AddInt64(&errOther, 1)
						logErrorOnce(errStr)
					}
					continue
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode >= 500 {
					atomic.AddInt64(&status5xx, 1)
				}
				mu.Lock()
				latencies = append(latencies, d)
				mu.Unlock()
				time.Sleep(5 * time.Millisecond)
			}
		}()
	}

	// Health poll every 30s
	pollDone := make(chan struct{})
	go func() {
		defer close(pollDone)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				err := healthCheck(*healthURL)
				if err != nil {
					fmt.Printf("%s health=FAIL %v\n", time.Now().Format("15:04:05"), err)
				} else {
					fmt.Printf("%s health=200\n", time.Now().Format("15:04:05"))
				}
			case <-time.After(*duration + 5*time.Second):
				return
			}
		}
	}()

	wg.Wait()
	fmt.Println()
	fmt.Println("Load test complete. Collecting results...")

	// Summary
	t := atomic.LoadInt64(&totalReqs)
	cres := atomic.LoadInt64(&errConnReset)
	to := atomic.LoadInt64(&errTimeout)
	oe := atomic.LoadInt64(&errOther)
	s5 := atomic.LoadInt64(&status5xx)
	e := cres + to + oe + s5
	ok := t - e

	fmt.Println()
	fmt.Println("========== BURN-IN RESULTS ==========")
	fmt.Printf("Duration          : %v\n", *duration)
	fmt.Printf("Total requests    : %d\n", t)
	fmt.Printf("HTTP 5xx          : %d\n", s5)
	fmt.Printf("Conn reset / EOF  : %d\n", cres)
	fmt.Printf("Timeouts          : %d\n", to)
	fmt.Printf("Other errors      : %d\n", oe)
	if t > 0 {
		fmt.Printf("Error rate (any)  : %.2f%%\n", float64(e)/float64(t)*100)
		fmt.Printf("Success rate      : %.2f%%\n", float64(ok)/float64(t)*100)
	}

	if ok > 0 && len(latencies) > 0 {
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		min := latencies[0]
		max := latencies[len(latencies)-1]
		var sum int64
		for _, v := range latencies {
			sum += v
		}
		avg := float64(sum) / float64(len(latencies))
		p50 := latencies[int(float64(len(latencies))*0.50)]
		p95 := latencies[int(float64(len(latencies))*0.95)]
		p99 := latencies[int(float64(len(latencies))*0.99)]
		fmt.Printf("Latency (ms)   : min=%d avg=%.1f max=%d p50=%d p95=%d p99=%d\n", min, avg, max, p50, p95, p99)
	} else {
		fmt.Println("Latency (ms)   : N/A (all requests errored)")
	}
	fmt.Println("=====================================")

	// T+end pprof snapshots
	fmt.Println("Capturing T+end pprof snapshots...")
	snapPProf(*adminURL, *pprofDir, "Tend")
	fmt.Printf("Artifacts saved to %s/\n", *pprofDir)
}

func healthCheck(url string) error {
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func snapPProf(admin, dir, suffix string) {
	urls := map[string]string{
		"goroutine": admin + "/debug/pprof/goroutine",
		"heap":      admin + "/debug/pprof/heap",
	}
	for name, u := range urls {
		resp, err := http.Get(u)
		if err != nil {
			continue
		}
		f, _ := os.Create(fmt.Sprintf("%s/%s-%s.out", dir, name, suffix))
		if f != nil {
			_, _ = f.ReadFrom(resp.Body)
			f.Close()
		}
		resp.Body.Close()
	}
}
