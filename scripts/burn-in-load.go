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
	"encoding/json"
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
		current      = flag.Bool("current", false, "burn-in-current.toml: exercise bounded/unlimited/unix/discovered/secure/predicates/plugin routes (JUL-AUD-004)")
		slowClient   = flag.Bool("slow-client", false, "Send a byte-paced (slow) POST body to /bounded/, exercising slow-client handling")
		slowUpstream = flag.Bool("slow-upstream", false, "Request /bounded/slow?ms=N (a deliberately slow backend response), exercising pending-timeout/circuit accounting")
		fault        = flag.Bool("fault", false, "Request a weighted mix of failure modes against a bounded backend (5xx storms, slow responses, mid-body TCP resets, malformed framing) plus a scheduled backend kill/restore cycle, exercising retry/circuit behavior")
		backendCtls  = flag.String("backendControlAddrs", "http://127.0.0.1:8081,http://127.0.0.1:8082", "Comma-separated backend addresses to POST /control/kill against directly (bypassing the proxy) for -fault's scheduled kill/restore cycle")
		killEvery    = flag.Duration("killEvery", 20*time.Second, "Interval between -fault's scheduled backend kill cycles")
		killFor      = flag.Duration("killFor", 5*time.Second, "How long each -fault kill cycle refuses requests on the targeted backend before it auto-restores")
		rbac         = flag.Bool("rbac", false, "Run a concurrent RBAC allow/deny probe against -admin using burn-in-current.toml's viewer/operator/admin principals")
		applyChurn   = flag.Bool("apply-churn", false, "Run a concurrent config-apply churn against -admin, resubmitting -applyConfig as a semantic no-op reload")
		applyConfig  = flag.String("applyConfig", "burn-in-current.toml", "Config file to resubmit for -apply-churn (read from local disk, same host as the server)")
		applyEvery   = flag.Duration("applyEvery", 10*time.Second, "Interval between -apply-churn attempts")
		adminToken   = flag.String("adminToken", "burnintoken", "Admin bearer token for pprof snapshots (and -apply-churn when RBAC is disabled)")
		viewerToken  = flag.String("viewerToken", "burnin-viewer-token-please-rotate-me-0001", "RBAC viewer-role token for -rbac/-apply-churn")
		operatorTok  = flag.String("operatorToken", "burnin-operator-token-please-rotate-me-0001", "RBAC operator-role token for -rbac/-apply-churn")
		adminRoleTok = flag.String("adminRoleToken", "burnin-admin-token-please-rotate-me-00001", "RBAC admin-role token for -rbac/-apply-churn")
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
	snapPProf(*adminURL, *pprofDir, "T0", *adminToken)

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
				} else if *slowUpstream {
					// Slow-upstream mode: every request hits a deliberately slow
					// backend response (see burn-in-backend.go's /slow handler),
					// exercising pending-timeout/circuit accounting rather than
					// throughput.
					ms := 200 + rand.Intn(800)
					path = fmt.Sprintf("/bounded/slow?ms=%d", ms)
					url = *baseURL + path
				} else if *fault {
					// Fault mode (JUL-AUD-019): a weighted mix of every failure
					// class the backend harness can inject at the request path
					// (scheduled kill/restore runs separately, in its own
					// goroutine below, since it targets the backend directly
					// rather than a per-request path): 5xx storms, slow
					// responses, mid-body TCP resets, and malformed framing.
					switch r := rand.Intn(100); {
					case r < 40:
						path = "/bounded/flaky?rate=40"
					case r < 60:
						path = fmt.Sprintf("/bounded/slow?ms=%d", 200+rand.Intn(800))
					case r < 80:
						path = "/bounded/reset"
					default:
						if rand.Intn(2) == 0 {
							path = "/bounded/malformed"
						} else {
							path = "/bounded/malformed?kind=bad-chunk"
						}
					}
					url = *baseURL + path
				} else if *current {
					// burn-in-current.toml — the merged-Beta surface (JUL-AUD-004):
					//   25% /bounded/     (resilience: admission/retry/circuit)
					//   15% /unlimited/   (resilience compatibility path)
					//   15% /unix/        (HTTP-over-Unix upstream, #407)
					//   15% /discovered/  (DNS service discovery)
					//   10% /secure/      (backend_tls policy)
					//   10% /predicates/  (routing predicates/response headers/CORS)
					//   5%  /plugin/      (WASM plugin middleware)
					//   5%  /             (baseline)
					r := rand.Intn(100)
					switch {
					case r < 25:
						path = "/bounded/"
						url = *baseURL + path
					case r < 40:
						path = "/unlimited/"
						url = *baseURL + path
					case r < 55:
						path = "/unix/"
						url = *baseURL + path
					case r < 70:
						path = "/discovered/"
						url = *baseURL + path
					case r < 80:
						path = "/secure/"
						url = *baseURL + path
					case r < 90:
						path = "/predicates/?version=v2"
						url = *baseURL + path
					case r < 95:
						path = "/plugin/"
						url = *baseURL + path
					default:
						path = "/"
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

				method := "GET"
				var body io.Reader
				if *slowClient {
					// Slow-client mode: every request is a POST with a
					// byte-paced body, exercising slow-client/read-timeout
					// handling on the proxy path rather than throughput.
					// 4096 bytes at 64B/50ms = ~3.2s to drain, safely under
					// the client's own 10s request timeout below.
					method = "POST"
					path = "/bounded/"
					url = *baseURL + path
					body = newSlowReader([]byte(strings.Repeat("x", 4096)), 64, 50*time.Millisecond)
				}

				req, err := http.NewRequest(method, url, body)
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
				if *slowClient {
					// CRS (WAF, when enabled) rejects a POST body without an
					// allow-listed Content-Type (rule 920420).
					req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				}
				if strings.HasPrefix(path, "/predicates/") {
					req.Header.Set("X-Tenant", "public")
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

	// RBAC allow/deny probe (JUL-AUD-004): concurrently proves the viewer
	// role can read status but not apply, and the admin role can do both,
	// against the running admin API — not just at config-parse time.
	if *rbac {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runRBACProbe(*adminURL, *duration, *viewerToken, *operatorTok, *adminRoleTok)
		}()
	}

	// Config-apply churn (JUL-AUD-004/019): repeatedly resubmits the same
	// config as a semantic no-op reload through the real managed-apply
	// coordinator, so the apply/reload path accumulates hours of hot-reload
	// evidence instead of only the handful of applies a functional test does.
	if *applyChurn {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runApplyChurn(*adminURL, *duration, *applyEvery, *applyConfig, *adminRoleTok)
		}()
	}

	// Scheduled backend kill/restore (JUL-AUD-019): alongside -fault's
	// per-request failure mix above, periodically take one backend fully
	// down for a window by calling its control endpoint directly, rather
	// than injecting failures only at the request path.
	if *fault {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runFaultKillCycle(strings.Split(*backendCtls, ","), *duration, *killEvery, *killFor)
		}()
	}

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
	snapPProf(*adminURL, *pprofDir, "Tend", *adminToken)
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

// slowReader releases chunkSize bytes of buf per Read call, pausing delay
// between calls, so a POST body drains over many seconds instead of one
// syscall — the client-side half of a slow-client soak (-slow-client).
type slowReader struct {
	buf       []byte
	pos       int
	chunkSize int
	delay     time.Duration
	slept     bool
}

func newSlowReader(buf []byte, chunkSize int, delay time.Duration) *slowReader {
	return &slowReader{buf: buf, chunkSize: chunkSize, delay: delay}
}

func (r *slowReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.buf) {
		return 0, io.EOF
	}
	if r.slept {
		time.Sleep(r.delay)
	}
	r.slept = true
	n := r.chunkSize
	if remaining := len(r.buf) - r.pos; n > remaining {
		n = remaining
	}
	if n > len(p) {
		n = len(p)
	}
	copy(p, r.buf[r.pos:r.pos+n])
	r.pos += n
	return n, nil
}

// runFaultKillCycle periodically POSTs /control/kill directly to one backend
// at a time (round-robin across addrs, bypassing the proxy — this is a
// control-plane call to the backend process, not routed traffic), so the pool
// alternates a genuinely-down backend with healthy ones rather than every
// backend failing at once, exercising admission/circuit behavior against a
// partial-outage pool (JUL-AUD-019) rather than a full one.
func runFaultKillCycle(addrs []string, duration, every, killFor time.Duration) {
	client := &http.Client{Timeout: 3 * time.Second}
	end := time.Now().Add(duration)
	var cycles, failures int64
	for i := 0; time.Now().Before(end); i++ {
		target := strings.TrimSpace(addrs[i%len(addrs)])
		req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/control/kill?duration=%s", target, killFor), nil)
		if err == nil {
			resp, err := client.Do(req)
			cycles++
			if err != nil {
				failures++
				logErrorOnce("fault kill cycle " + target + ": " + err.Error())
			} else {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}
		time.Sleep(every)
	}
	fmt.Printf("%s fault kill cycle: cycles=%d failures=%d\n", time.Now().Format("15:04:05"), cycles, failures)
}

// runRBACProbe repeatedly exercises the admin API's allow/deny boundary
// against a running server (not just at config-parse time). All three
// predefined roles hold status:read, so /api/v1/status must be 200 for
// each. Only viewer lacks config:write, so a POST to
// /api/v1/config/validate must be 403 for viewer and must NOT be 403 for
// operator/admin (whatever else it returns depends on the submitted body,
// which this probe does not attempt to make valid).
func runRBACProbe(adminURL string, duration time.Duration, viewerToken, operatorToken, adminToken string) {
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	end := time.Now().Add(duration)
	var checks, violations int64
	do := func(method, token, path string) (int, error) {
		req, err := http.NewRequest(method, adminURL+path, nil)
		if err != nil {
			return 0, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, nil
	}
	assertEqual := func(label string, code int, err error, want int) {
		checks++
		if err != nil {
			violations++
			logErrorOnce("rbac probe " + label + ": " + err.Error())
			return
		}
		if code != want {
			violations++
			logErrorOnce(fmt.Sprintf("rbac probe %s: got %d, want %d", label, code, want))
		}
	}
	assertNotForbidden := func(label string, code int, err error) {
		checks++
		if err != nil {
			violations++
			logErrorOnce("rbac probe " + label + ": " + err.Error())
			return
		}
		if code == http.StatusForbidden {
			violations++
			logErrorOnce(fmt.Sprintf("rbac probe %s: got 403, permission should have been granted", label))
		}
	}
	for time.Now().Before(end) {
		for _, tok := range []string{viewerToken, operatorToken, adminToken} {
			code, err := do(http.MethodGet, tok, "/api/v1/status")
			assertEqual("status:read for "+tok, code, err, http.StatusOK)
		}
		code, err := do(http.MethodPost, viewerToken, "/api/v1/config/validate")
		assertEqual("viewer denied config:write", code, err, http.StatusForbidden)
		code, err = do(http.MethodPost, operatorToken, "/api/v1/config/validate")
		assertNotForbidden("operator granted config:write", code, err)
		code, err = do(http.MethodPost, adminToken, "/api/v1/config/validate")
		assertNotForbidden("admin granted config:write", code, err)
		time.Sleep(2 * time.Second)
	}
	fmt.Printf("%s rbac probe: checks=%d violations=%d\n", time.Now().Format("15:04:05"), checks, violations)
}

// adoptExternalConfigIfNeeded checks the server's config_state and, when the
// on-disk file has not yet been adopted as the managed baseline
// ("managed_unadopted"), previews and adopts it — otherwise every apply is
// refused with 409 drift_detected. A no-op when already "managed_clean".
func adoptExternalConfigIfNeeded(client *http.Client, adminURL, token string) error {
	req, err := http.NewRequest(http.MethodGet, adminURL+"/api/v1/config", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	var meta struct {
		ServingVersion string `json:"serving_version"`
		ConfigState    string `json:"config_state"`
	}
	err = json.NewDecoder(resp.Body).Decode(&meta)
	resp.Body.Close()
	if err != nil {
		return err
	}
	if meta.ConfigState != "managed_unadopted" {
		return nil
	}

	previewReq, err := http.NewRequest(http.MethodPost, adminURL+"/api/v1/config/adopt-external/preview", strings.NewReader("{}"))
	if err != nil {
		return err
	}
	previewReq.Header.Set("Authorization", "Bearer "+token)
	previewReq.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(previewReq)
	if err != nil {
		return err
	}
	var preview struct {
		OK             bool   `json:"ok"`
		ObservedDigest string `json:"observed_digest"`
		CandidateVer   string `json:"candidate_version"`
	}
	err = json.NewDecoder(resp.Body).Decode(&preview)
	resp.Body.Close()
	if err != nil {
		return err
	}
	if !preview.OK || preview.ObservedDigest == "" {
		return fmt.Errorf("adopt-external/preview did not return an observed_digest")
	}

	body, _ := json.Marshal(map[string]interface{}{
		"observed_digest": preview.ObservedDigest,
		"base_version":    meta.ServingVersion,
		"confirm":         true,
	})
	adoptReq, err := http.NewRequest(http.MethodPost, adminURL+"/api/v1/config/adopt-external", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	adoptReq.Header.Set("Authorization", "Bearer "+token)
	adoptReq.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(adoptReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var errBody strings.Builder
		io.Copy(&errBody, resp.Body)
		return fmt.Errorf("adopt-external returned %d: %s", resp.StatusCode, errBody.String())
	}
	fmt.Println("apply-churn: adopted the on-disk config as the managed baseline")
	return nil
}

// runApplyChurn resubmits a local config file as a candidate through the
// real managed-apply coordinator every `every`, for `duration`. Reapplying
// byte-identical content is a semantic no-op reload (a real reload-plan
// pass, not a synthetic one), so this accumulates hot-apply/hot-reload hours
// against the actual admin API rather than only a handful of applies a
// functional test exercises. Requires `config_authority = "managed"` in the
// server's own config; adopts the on-disk file as the managed baseline first
// if it has not been adopted yet (ADR 0019).
func runApplyChurn(adminURL string, duration, every time.Duration, configPath, token string) {
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		fmt.Printf("apply-churn: cannot read %s: %v\n", configPath, err)
		return
	}

	if err := adoptExternalConfigIfNeeded(client, adminURL, token); err != nil {
		fmt.Printf("apply-churn: adopt-external: %v\n", err)
		return
	}

	end := time.Now().Add(duration)
	var attempts, failures int64
	for time.Now().Before(end) {
		req, err := http.NewRequest(http.MethodGet, adminURL+"/api/v1/config", nil)
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		var version string
		if err == nil {
			resp, derr := client.Do(req)
			if derr == nil {
				var meta struct {
					ServingVersion string `json:"serving_version"`
				}
				_ = json.NewDecoder(resp.Body).Decode(&meta)
				resp.Body.Close()
				version = meta.ServingVersion
			} else {
				err = derr
			}
		}
		attempts++
		if err != nil || version == "" {
			failures++
			logErrorOnce(fmt.Sprintf("apply-churn: fetch serving_version: %v", err))
			time.Sleep(every)
			continue
		}

		applyReq, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/api/v1/config/apply?base_version=%s&mode=hot", adminURL, version), strings.NewReader(string(raw)))
		if err != nil {
			failures++
			time.Sleep(every)
			continue
		}
		applyReq.Header.Set("Authorization", "Bearer "+token)
		applyReq.Header.Set("Content-Type", "application/toml")
		resp, err := client.Do(applyReq)
		if err != nil {
			failures++
			logErrorOnce("apply-churn: apply: " + err.Error())
			time.Sleep(every)
			continue
		}
		var out struct {
			OK      bool   `json:"ok"`
			Outcome string `json:"outcome"`
			State   string `json:"state"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if resp.StatusCode >= 300 || !out.OK {
			failures++
			logErrorOnce(fmt.Sprintf("apply-churn: apply returned %d ok=%v outcome=%q state=%q", resp.StatusCode, out.OK, out.Outcome, out.State))
		}
		time.Sleep(every)
	}
	fmt.Printf("%s apply-churn: attempts=%d failures=%d\n", time.Now().Format("15:04:05"), attempts, failures)
}
