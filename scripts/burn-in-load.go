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
	"crypto/tls"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strconv"
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
		duration     = flag.Duration("duration", 5*time.Minute, "How long to run")
		workers      = flag.Int("workers", 50, "Number of concurrent workers")
		baseURL      = flag.String("base", "http://localhost:8080", "Jul server base URL")
		tlsBase      = flag.String("tls", "https://localhost:8443", "Jul TLS server base URL")
		healthURL    = flag.String("health", "http://127.0.0.1:8082/healthz", "Health endpoint")
		adminURL     = flag.String("admin", "http://127.0.0.1:9090", "Admin / pprof base URL")
		pprofDir     = flag.String("out", "burn-in-artifacts", "Directory for pprof snapshots")
		authUser     = flag.String("authUser", "soakuser", "HTTP Basic auth username")
		authPassword = flag.String("authPassword", "soakpass", "HTTP Basic auth password")
		authRatio    = flag.Int("authRatio", 100, "Percentage of requests that include auth headers (0-100)")
		compress     = flag.Bool("compress", false, "Send Accept-Encoding: gzip, br, zstd for compression soak")
		cache        = flag.Bool("cache", false, "Exercise cache hit/miss/evict/revalidate patterns for cache soak")
		ratelimit    = flag.Bool("ratelimit", false, "Exercise rate limiter: expect 429s on /api/, baseline 200s on /baseline/")
		waf          = flag.Bool("waf", false, "Exercise WAF: mix clean/malicious traffic (expect 200s and 403s)")
		full         = flag.Bool("full", false, "Phase 2A: exercise ALL features simultaneously (cache+ratelimit+waf+auth+compress)")
		clientCert   = flag.String("clientCert", "testdata/tls/client.crt", "Client certificate for mTLS")
		clientKey    = flag.String("clientKey", "testdata/tls/client.key", "Client key for mTLS")
		phase2a      = flag.Bool("phase2a", false, "Phase 2A: exercise transcoding + passthrough + discovery + secrets + zero-config + WASM plugins")
		http3        = flag.Bool("http3", false, "HTTP/3 isolated soak: exercise / and /health on HTTPS (no backend)")
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
	snapPProf(*adminURL, *pprofDir, "T0", "burnintoken")

	// Load client certificate for mTLS (TLS :8443) if available.
	var tlsConfig *tls.Config
	if cert, err := tls.LoadX509KeyPair(*clientCert, *clientKey); err == nil {
		tlsConfig = &tls.Config{
			InsecureSkipVerify: true,
			Certificates:       []tls.Certificate{cert},
		}
	} else {
		tlsConfig = &tls.Config{InsecureSkipVerify: true}
	}

	// Shared transport for proper connection reuse across all workers
	sharedTransport := &http.Transport{
		TLSClientConfig:     tlsConfig,
		MaxIdleConns:        *workers * 2,
		MaxIdleConnsPerHost: *workers * 2,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   false,
	}

	// Pre-compute Basic auth header if credentials are provided.
	var authHeader string
	if *authUser != "" {
		authHeader = "Basic " + base64.StdEncoding.EncodeToString([]byte(*authUser+":"+*authPassword))
		fmt.Printf("Auth           : Basic auth user=%s ratio=%d%%\n", *authUser, *authRatio)
	}
	if *cache {
		fmt.Println("Cache mode     : enabled (hits/misses/evict/revalidate mix)")
	}
	if *ratelimit {
		fmt.Println("Rate limit mode: enabled (/api/ → expect 429s, /baseline/ → 200s)")
	}
	if *waf {
		fmt.Println("WAF mode       : enabled (benign + malicious traffic mix)")
	}
	if *phase2a {
		fmt.Println("Phase 2A mode  : enabled (transcode + passthrough + discovery + secrets + zero-config + WASM)")
	}

	endTime := time.Now().Add(*duration)
	var totalReqs, errConnReset, errTimeout, errOther, status2xx, status401, status403, status429, status5xx int64
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
				var path, url string
				if *full {
					// Phase 2A FULL mode — exercise ALL features simultaneously
					//   18% /api/      (HTTP)  -> cache + rate-limit + WAF + auth + compress
					//   10% /baseline/ (HTTP)  -> no cache, no rate-limit, no WAF, no auth, compress
					//   10% /nocache/  (HTTP)  -> no cache, rate-limit + WAF + auth + compress
					//   10% /static/   (HTTP)  -> static files, compress, no cache
					//   12% /admin/    (HTTP)  -> basic auth + compress
					//   15% /api/      (HTTPS) -> TLS + mTLS + auth + compress
					//   10% /healthz   (HTTPS) -> TLS health check
					//   15% cache warm hits (same URLs as above)
					r := rand.Intn(100)
					switch {
					case r < 18:
						path = "/api/items"
						url = *baseURL + path
					case r < 28:
						path = "/baseline/"
						url = *baseURL + path
					case r < 38:
						path = "/nocache/api/items"
						url = *baseURL + path
					case r < 50:
						path = "/static/"
						url = *baseURL + path
					case r < 62:
						path = "/admin/dashboard"
						url = *baseURL + path
					case r < 77:
						path = "/api/items"
						url = *tlsBase + path
					case r < 87:
						path = "/healthz"
						url = *tlsBase + path
					default:
						path = "/api/static/test"
						url = *baseURL + path
					}
				} else if *phase2a {
					// Phase 2A consolidated — features #1-5 + #8
					// 15% /api/      (cache + rate-limit + WAF + auth + compress + WASM)
					// 10% /baseline/ (no cache, no rate-limit, no WAF, no auth)
					// 10% /nocache/  (no cache, rate-limit + WAF + auth + compress)
					// 10% /static/   (static files, compress)
					// 10% /admin/    (basic auth + compress)
					// 10% /discovery/ (service discovery)
					//  5% /blocked   (WASM request-block => expect non-200)
					// 15% /api/ HTTPS (TLS + mTLS + auth + compress)
					// 10% /healthz HTTPS (TLS health check)
					//  5% warm hits
					r := rand.Intn(100)
					switch {
					case r < 15:
						path = "/api/items"
						url = *baseURL + path
					case r < 25:
						path = "/baseline/"
						url = *baseURL + path
					case r < 35:
						path = "/nocache/api/items"
						url = *baseURL + path
					case r < 45:
						path = "/static/"
						url = *baseURL + path
					case r < 55:
						path = "/admin/dashboard"
						url = *baseURL + path
					case r < 65:
						path = "/discovery/health"
						url = *baseURL + path
					case r < 70:
						path = "/blocked"
						url = *baseURL + path
					case r < 85:
						path = "/api/items"
						url = *tlsBase + path
					case r < 95:
						path = "/healthz"
						url = *tlsBase + path
					default:
						path = "/api/static/test"
						url = *baseURL + path
					}
				} else if *http3 {
					// HTTP/3 isolated soak — paths that exist in burn-in-http3.toml
					// No backend required for / and /health
					// https://localhost:8443 only (TLS + HTTP/3)
					r := rand.Intn(100)
					switch {
					case r < 60:
						path = "/"
						url = *tlsBase + path
					default:
						path = "/health"
						url = *tlsBase + path
					}
				} else if *cache {
					// Cache traffic pattern:
					//   50% warm hits (same URL, should be cached after first fetch)
					//   25% unique URLs (forced misses, evict pressure)
					//   15% uncached baseline (bypass cache, verify backend health)
					//   10% alternate warm path
					r := rand.Intn(100)
					switch {
					case r < 50:
						path = "/api/items"
						url = *baseURL + path
					case r < 75:
						path = "/api/item-" + strconv.Itoa(rand.Intn(100000))
						url = *baseURL + path
					case r < 90:
						path = "/nocache/api/items"
						url = *baseURL + path
					default:
						path = "/api/static/test"
						url = *baseURL + path
					}
				} else if *waf {
					// WAF traffic pattern:
					//   40% benign API (expect 200)
					//   20% benign baseline (no WAF, expect 200)
					//   20% malicious SQL injection in query (expect 403)
					//   20% malicious XSS in header (expect 403)
					r := rand.Intn(100)
					switch {
					case r < 40:
						path = "/api/items"
						url = *baseURL + path
					case r < 60:
						path = "/baseline/"
						url = *baseURL + path
					case r < 80:
						path = "/api/search?q=" + maliciousSQLPayloads[rand.Intn(len(maliciousSQLPayloads))]
						url = *baseURL + path
					default:
						path = "/api/items"
						url = *baseURL + path
					}
				} else if *ratelimit {
					if rand.Intn(100) < 80 {
						path = "/api/"
						url = *baseURL + path
					} else {
						path = "/baseline/"
						url = *baseURL + path
					}
				} else {
					path = "/api/"
					url = *baseURL + path
					if rand.Intn(2) == 0 {
						path = "/static/"
						url = *baseURL + path
					}
				}

				req, err := http.NewRequest("GET", url, nil)
				if err != nil {
					atomic.AddInt64(&totalReqs, 1)
					atomic.AddInt64(&errOther, 1)
					logErrorOnce(err.Error())
					continue
				}
				if authHeader != "" && (rand.Intn(100) < *authRatio || *full) {
					req.Header.Set("Authorization", authHeader)
				}
				if *compress || *full {
					req.Header.Set("Accept-Encoding", "gzip, br, zstd")
				}

				start := time.Now()
				resp, err := client.Do(req)
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
				switch {
				case resp.StatusCode >= 500:
					atomic.AddInt64(&status5xx, 1)
				case resp.StatusCode == 401:
					atomic.AddInt64(&status401, 1)
				case resp.StatusCode == 403:
					atomic.AddInt64(&status403, 1)
				case resp.StatusCode == 429:
					atomic.AddInt64(&status429, 1)
				case resp.StatusCode >= 200 && resp.StatusCode < 300:
					atomic.AddInt64(&status2xx, 1)
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
	s2 := atomic.LoadInt64(&status2xx)
	s401 := atomic.LoadInt64(&status401)
	s403 := atomic.LoadInt64(&status403)
	s429 := atomic.LoadInt64(&status429)
	s5 := atomic.LoadInt64(&status5xx)
	e := cres + to + oe + s5
	ok := t - e

	fmt.Println()
	fmt.Println("========== BURN-IN RESULTS ==========")
	fmt.Printf("Duration          : %v\n", *duration)
	fmt.Printf("Total requests    : %d\n", t)
	fmt.Printf("HTTP 2xx          : %d\n", s2)
	fmt.Printf("HTTP 401          : %d\n", s401)
	fmt.Printf("HTTP 403          : %d\n", s403)
	fmt.Printf("HTTP 429          : %d\n", s429)
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
	snapPProf(*adminURL, *pprofDir, "Tend", "burnintoken")
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

func snapPProf(admin, dir, suffix, token string) {
	urls := map[string]string{
		"goroutine": admin + "/debug/pprof/goroutine?debug=1",
		"heap":      admin + "/debug/pprof/heap?debug=1",
	}
	for name, u := range urls {
		req, err := http.NewRequest("GET", u, nil)
		if err != nil {
			continue
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
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

var maliciousSQLPayloads = []string{
	"1' OR '1'='1",
	"admin'--",
	"1; DROP TABLE users--",
	"UNION SELECT password FROM users--",
	"1 AND 1=1",
}
