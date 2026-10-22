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
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/http2"
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
	os.Exit(runLoad())
}

func runLoad() int {
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
		phase2a      = flag.Bool("phase2a", false, "Phase 2A HTTP route mix: proxy, static, discovery, WASM, and TLS paths (not zero-config, importer, or gRPC)")
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
		f1Mode       = flag.Bool("f1", false, "Fixture F1 (#581, spec §13): verified-TLS 70/10/20 route mix, optional excluded-identity traffic, held SSE streams, and continuity probes")
		caFile       = flag.String("caFile", "", "Trusted Root CA PEM file for verified TLS in -f1 mode")
		portalHost   = flag.String("portalHost", "portal.lab.test", "Portal Host header and SNI in -f1 mode")
		dashHost     = flag.String("dashHost", "dash.lab.test", "Dashboard Host header and SNI in -f1 mode")
		excludedPct  = flag.Int("excludedRatio", 0, "Percentage of requests sent as excluded-identity dashboard requests in -f1 mode (e.g. 10 for W2/W3)")
		heldConns    = flag.Int("heldConns", 0, "Number of held long-lived dashboard SSE connections in -f1 mode (e.g. 20 for W2/W3)")
		probeEvery   = flag.Duration("probeInterval", 0, "Fixed-cadence continuity probe interval in -f1 mode (e.g. 1s for W3/W4)")
		probeDash    = flag.Bool("probeDash", false, "Also probe dashboard at -probeInterval (for W4 outage/recovery timing)")
		allowOutage  = flag.Bool("allowDashOutage", false, "Allow 502/503 on dashboard during W4 scheduled backend outage window")
		paceInterval = flag.Duration("pace", 25*time.Millisecond, "Per-worker request pacing in -f1 mode")
		eventLogPath = flag.String("eventLog", "", "Compact JSONL event log output path in -f1 mode (spec §13)")
		summaryPath  = flag.String("summaryOut", "", "JSON summary output path in -f1 mode")
		f1SharedH2   = flag.Bool("f1SharedH2", false, "Verify shared single-socket HTTP/2 connection coalescing and H1 WebSocket retirement (#642, W3)")
		readyFile    = flag.String("readyFile", "", "Path written once shared H2 streams and H1 WebSockets are active in -f1SharedH2 mode")
		wsEchoClose  = flag.Bool("wsEchoClose", true, "Reply with masked RFC 6455 Close frame when server initiates Close in -f1SharedH2 mode")
		withWS       = flag.Bool("withWS", true, "Also open an HTTP/1.1 101 WebSocket session alongside the shared HTTP/2 connection in -f1SharedH2 mode")
	)
	flag.Parse()

	if *f1SharedH2 {
		return runF1SharedH2(f1SharedH2Options{
			TLSBase:      *tlsBase,
			CAFile:       *caFile,
			ClientCert:   *clientCert,
			ClientKey:    *clientKey,
			PortalHost:   *portalHost,
			DashHost:     *dashHost,
			AuthUser:     *authUser,
			AuthPassword: *authPassword,
			Pace:         *paceInterval,
			ReadyFile:    *readyFile,
			WithWS:       *withWS,
			WSEchoClose:  *wsEchoClose,
			EventLogPath: *eventLogPath,
			SummaryPath:  *summaryPath,
			Timeout:      *duration,
		})
	}

	if *f1Mode {
		return runF1Mode(f1Options{
			Duration:     *duration,
			Workers:      *workers,
			TLSBase:      *tlsBase,
			CAFile:       *caFile,
			PortalHost:   *portalHost,
			DashHost:     *dashHost,
			AuthUser:     *authUser,
			AuthPassword: *authPassword,
			ExcludedPct:  *excludedPct,
			HeldConns:    *heldConns,
			ProbeEvery:   *probeEvery,
			ProbeDash:    *probeDash,
			AllowOutage:  *allowOutage,
			Pace:         *paceInterval,
			EventLogPath: *eventLogPath,
			SummaryPath:  *summaryPath,
		})
	}

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
		return 1
	}
	fmt.Println("Health OK: 200")

	// Ensure artifacts dir
	if err := os.MkdirAll(*pprofDir, 0755); err != nil {
		fmt.Println("Profile capture FAILED: cannot create output directory")
		return 1
	}

	// T+0 pprof snapshots
	fmt.Println("Capturing T+0 pprof snapshots...")
	captureOK := snapPProf(*adminURL, *pprofDir, "T0", *adminToken)

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
		fmt.Println("Phase 2A mode  : HTTP route mix (proxy/static/discovery/WASM/TLS; no zero-config, importer or gRPC traffic)")
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
	endCaptureOK := snapPProf(*adminURL, *pprofDir, "Tend", *adminToken)
	if !captureOK || !endCaptureOK {
		fmt.Println("Profile capture FAILED: traffic results above do not establish complete profile capture")
		return 1
	}
	fmt.Printf("All four profiles saved to %s/\n", *pprofDir)
	return 0
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

func snapPProf(admin, dir, suffix, token string) bool {
	// Keep normal TLS verification and bound downloads independently of traffic.
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	ok := true
	for _, name := range []string{"goroutine", "heap"} {
		path := filepath.Join(dir, name+"-"+suffix+".out")
		err := captureProfile(client, admin+"/debug/pprof/"+name+"?debug=1", path, token)
		if err != nil {
			fmt.Printf("Profile %s/%s FAILED: %v\n", suffix, name, err)
			ok = false
		} else {
			fmt.Printf("Profile %s/%s saved\n", suffix, name)
		}
	}
	return ok
}

func captureProfile(client *http.Client, url, path, token string) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("invalid profile request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		// Do not print URLs, headers, response bodies, or wrapped transport errors.
		return fmt.Errorf("profile request failed (transport, TLS or timeout)")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".pprof-*")
	if err != nil {
		return fmt.Errorf("cannot create temporary profile")
	}
	defer os.Remove(f.Name())
	n, err := io.Copy(f, resp.Body)
	if err != nil || n == 0 {
		f.Close()
		return fmt.Errorf("incomplete profile download or write")
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("cannot close profile file")
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("cannot publish profile file")
	}
	return nil
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
// /api/v1/config/validate must be 403 for viewer and 415 for operator/admin
// because the probe deliberately omits Content-Type.
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
	for time.Now().Before(end) {
		for _, tok := range []string{viewerToken, operatorToken, adminToken} {
			code, err := do(http.MethodGet, tok, "/api/v1/status")
			assertEqual("status:read for "+tok, code, err, http.StatusOK)
		}
		code, err := do(http.MethodPost, viewerToken, "/api/v1/config/validate")
		assertEqual("viewer denied config:write", code, err, http.StatusForbidden)
		code, err = do(http.MethodPost, operatorToken, "/api/v1/config/validate")
		assertEqual("operator granted config:write", code, err, http.StatusUnsupportedMediaType)
		code, err = do(http.MethodPost, adminToken, "/api/v1/config/validate")
		assertEqual("admin granted config:write", code, err, http.StatusUnsupportedMediaType)
		time.Sleep(10 * time.Second)
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

const (
	f1PortalMarker      = "JUL-F1-PUBLIC-PORTAL-MARKER-v1"
	f1DashboardMarker   = "JUL-F1-RESTRICTED-DASHBOARD-MARKER-v1"
	f1PrivateLeakMarker = "JUL-F1-PRIVATE-FAMILY-TELEMETRY-DO-NOT-CACHE"
)

type f1Options struct {
	Duration     time.Duration
	Workers      int
	TLSBase      string
	CAFile       string
	PortalHost   string
	DashHost     string
	AuthUser     string
	AuthPassword string
	ExcludedPct  int
	HeldConns    int
	ProbeEvery   time.Duration
	ProbeDash    bool
	AllowOutage  bool
	Pace         time.Duration
	EventLogPath string
	SummaryPath  string
}

type f1Summary struct {
	DurationMS        int64            `json:"duration_ms"`
	Workers           int              `json:"workers"`
	TotalRequests     int64            `json:"total_requests"`
	PortalHTMLCount   int64            `json:"portal_html_count"`
	PortalAssetCount  int64            `json:"portal_asset_count"`
	DashAuthCount     int64            `json:"dash_auth_count"`
	DashExcludedCount int64            `json:"dash_excluded_count"`
	Status2xx         int64            `json:"status_2xx"`
	Status401         int64            `json:"status_401"`
	Status403         int64            `json:"status_403"`
	Status429         int64            `json:"status_429"`
	Status5xx         int64            `json:"status_5xx"`
	TransportErrors   int64            `json:"transport_errors"`
	OracleViolations  int64            `json:"oracle_violations"`
	Protocols         map[string]int64 `json:"protocols"`
	HeldOpened        int64            `json:"held_opened"`
	HeldClosed        int64            `json:"held_closed"`
	ProbePortalOK     int64            `json:"probe_portal_ok"`
	ProbePortalFail   int64            `json:"probe_portal_fail"`
	ProbeDashOK       int64            `json:"probe_dash_ok"`
	ProbeDashFail     int64            `json:"probe_dash_fail"`
	LatencyMinMS      int64            `json:"latency_min_ms"`
	LatencyP50MS      int64            `json:"latency_p50_ms"`
	LatencyP95MS      int64            `json:"latency_p95_ms"`
	LatencyP99MS      int64            `json:"latency_p99_ms"`
	LatencyMaxMS      int64            `json:"latency_max_ms"`
	LatencyAvgMS      float64          `json:"latency_avg_ms"`
}

func buildF1Transport(tlsBase, caFile string, maxIdle int) (*http.Transport, error) {
	if strings.TrimSpace(caFile) == "" {
		return nil, fmt.Errorf("f1 mode requires -caFile (verified TLS is mandatory)")
	}
	caBytes, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read caFile: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caBytes) {
		return nil, fmt.Errorf("caFile contains no valid PEM certificates")
	}
	u, err := url.Parse(tlsBase)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid -tls URL %q", tlsBase)
	}
	targetAddr := u.Host
	if !strings.Contains(targetAddr, ":") {
		targetAddr += ":443"
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		TLSClientConfig: &tls.Config{
			RootCAs:    pool,
			MinVersion: tls.VersionTLS12,
		},
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        maxIdle,
		MaxIdleConnsPerHost: maxIdle,
		IdleConnTimeout:     90 * time.Second,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, targetAddr)
		},
	}, nil
}

func runF1Mode(opts f1Options) int {
	t0 := time.Now()
	tr, err := buildF1Transport(opts.TLSBase, opts.CAFile, opts.Workers*2+16)
	if err != nil {
		fmt.Printf("F1 init FAILED: %v\n", err)
		return 1
	}
	defer tr.CloseIdleConnections()

	var evMu sync.Mutex
	var evFile *os.File
	if opts.EventLogPath != "" {
		if err := os.MkdirAll(filepath.Dir(opts.EventLogPath), 0o755); err != nil {
			fmt.Printf("F1 eventLog FAILED: %v\n", err)
			return 1
		}
		evFile, err = os.Create(opts.EventLogPath)
		if err != nil {
			fmt.Printf("F1 eventLog create FAILED: %v\n", err)
			return 1
		}
		defer evFile.Close()
	}
	writeEvent := func(fields map[string]any) {
		if evFile == nil {
			return
		}
		now := time.Now()
		fields["t"] = now.UTC().Format(time.RFC3339Nano)
		fields["mono_ns"] = now.Sub(t0).Nanoseconds()
		b, _ := json.Marshal(fields)
		evMu.Lock()
		_, _ = evFile.Write(append(b, '\n'))
		evMu.Unlock()
	}

	client := &http.Client{Timeout: 10 * time.Second, Transport: tr}
	portalBase := "https://" + opts.PortalHost
	dashBase := "https://" + opts.DashHost
	authHdr := "Basic " + base64.StdEncoding.EncodeToString([]byte(opts.AuthUser+":"+opts.AuthPassword))

	// Verified-TLS preflight against public portal
	pfResp, err := client.Get(portalBase + "/index.html")
	if err != nil {
		fmt.Printf("F1 preflight FAILED: %v\n", err)
		return 1
	}
	pfBody, _ := io.ReadAll(pfResp.Body)
	pfResp.Body.Close()
	if pfResp.StatusCode != http.StatusOK || !strings.Contains(string(pfBody), f1PortalMarker) {
		fmt.Printf("F1 preflight unexpected response: status=%d\n", pfResp.StatusCode)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), opts.Duration)
	defer cancel()

	var (
		totalReqs, portalHTML, portalAsset, dashAuth, dashExcl     int64
		s2xx, s401, s403, s429, s5xx, errTrans, violations         int64
		heldOpened, heldClosed                                     int64
		probePortalOK, probePortalFail, probeDashOK, probeDashFail int64
		protoMu                                                    sync.Mutex
		protos                                                     = map[string]int64{}
		latMu                                                      sync.Mutex
		latencies                                                  []int64
		wg                                                         sync.WaitGroup
	)
	recordProto := func(p string) {
		if p == "" {
			return
		}
		protoMu.Lock()
		protos[p]++
		protoMu.Unlock()
	}

	for i := 0; i < opts.HeldConns; i++ {
		streamID := i + 1
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, dashBase+"/events", nil)
			req.Header.Set("Authorization", authHdr)
			start := time.Now()
			writeEvent(map[string]any{"event": "held_headers_sent", "stream_id": streamID, "target": "dashboard_sse"})
			resp, err := (&http.Client{Transport: tr}).Do(req)
			if err != nil {
				writeEvent(map[string]any{"event": "held_connect_error", "stream_id": streamID})
				return
			}
			atomic.AddInt64(&heldOpened, 1)
			recordProto(resp.Proto)
			writeEvent(map[string]any{"event": "held_established", "stream_id": streamID, "status": resp.StatusCode, "proto": resp.Proto})
			buf := make([]byte, 1024)
			for {
				_, rerr := resp.Body.Read(buf)
				if rerr != nil {
					break
				}
			}
			resp.Body.Close()
			atomic.AddInt64(&heldClosed, 1)
			writeEvent(map[string]any{
				"event":       "held_closed",
				"stream_id":   streamID,
				"status":      resp.StatusCode,
				"proto":       resp.Proto,
				"duration_ms": time.Since(start).Milliseconds(),
			})
		}()
	}

	if opts.ProbeEvery > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			probeTr, _ := buildF1Transport(opts.TLSBase, opts.CAFile, 4)
			defer probeTr.CloseIdleConnections()
			probeClient := &http.Client{Timeout: 5 * time.Second, Transport: probeTr}
			ticker := time.NewTicker(opts.ProbeEvery)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					pStart := time.Now()
					resp, err := probeClient.Get(portalBase + "/index.html")
					var status int
					ok := false
					if err == nil {
						body, _ := io.ReadAll(resp.Body)
						resp.Body.Close()
						status = resp.StatusCode
						ok = status == http.StatusOK && strings.Contains(string(body), f1PortalMarker) && !strings.Contains(string(body), f1PrivateLeakMarker)
					}
					if ok {
						atomic.AddInt64(&probePortalOK, 1)
					} else {
						atomic.AddInt64(&probePortalFail, 1)
					}
					writeEvent(map[string]any{"event": "probe_portal", "status": status, "ok": ok, "duration_ms": time.Since(pStart).Milliseconds()})

					if opts.ProbeDash {
						dStart := time.Now()
						dReq, _ := http.NewRequest(http.MethodGet, dashBase+"/api/summary", nil)
						dReq.Header.Set("Authorization", authHdr)
						dResp, derr := probeClient.Do(dReq)
						dStatus := 0
						dOK := false
						if derr == nil {
							dBody, _ := io.ReadAll(dResp.Body)
							dResp.Body.Close()
							dStatus = dResp.StatusCode
							dOK = dStatus == http.StatusOK && strings.Contains(string(dBody), f1DashboardMarker)
						}
						if dOK {
							atomic.AddInt64(&probeDashOK, 1)
						} else {
							atomic.AddInt64(&probeDashFail, 1)
						}
						writeEvent(map[string]any{"event": "probe_dashboard", "status": dStatus, "ok": dOK, "duration_ms": time.Since(dStart).Milliseconds()})
					}
				}
			}
		}()
	}

	var seqCounter int64
	for w := 0; w < opts.Workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				seq := atomic.AddInt64(&seqCounter, 1)
				slot := int(seq % 100)
				var class, reqURL string
				var withAuth, isExcluded bool
				if opts.ExcludedPct > 0 && slot < opts.ExcludedPct {
					class = "dash_excluded"
					reqURL = dashBase + "/api/summary"
					isExcluded = true
					atomic.AddInt64(&dashExcl, 1)
				} else {
					mixSlot := int((seq / 1) % 10)
					switch {
					case mixSlot < 7:
						class = "portal_html"
						reqURL = portalBase + "/index.html"
						atomic.AddInt64(&portalHTML, 1)
					case mixSlot == 7:
						class = "portal_asset"
						reqURL = portalBase + "/assets/bundle.bin"
						atomic.AddInt64(&portalAsset, 1)
					default:
						class = "dash_auth"
						reqURL = dashBase + "/api/summary"
						withAuth = true
						atomic.AddInt64(&dashAuth, 1)
					}
				}

				req, _ := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
				if withAuth {
					req.Header.Set("Authorization", authHdr)
				} else if isExcluded {
					req.Header.Set("X-Forwarded-For", "127.0.0.1")
					req.Header.Set("Tailscale-User-Login", "forged@example.com")
					if seq%2 == 0 {
						req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("wrong:credential")))
					}
				}

				reqStart := time.Now()
				resp, err := client.Do(req)
				lat := time.Since(reqStart).Milliseconds()
				if err != nil {
					if ctx.Err() != nil {
						break
					}
					atomic.AddInt64(&totalReqs, 1)
					atomic.AddInt64(&errTrans, 1)
					atomic.AddInt64(&violations, 1)
					continue
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				atomic.AddInt64(&totalReqs, 1)
				recordProto(resp.Proto)

				sc := resp.StatusCode
				switch {
				case sc >= 200 && sc < 300:
					atomic.AddInt64(&s2xx, 1)
				case sc == http.StatusUnauthorized:
					atomic.AddInt64(&s401, 1)
				case sc == http.StatusForbidden:
					atomic.AddInt64(&s403, 1)
				case sc == http.StatusTooManyRequests:
					atomic.AddInt64(&s429, 1)
				case sc >= 500:
					atomic.AddInt64(&s5xx, 1)
				}

				bodyStr := string(body)
				oracleOK := true
				switch class {
				case "portal_html":
					oracleOK = sc == http.StatusOK && strings.Contains(bodyStr, f1PortalMarker) && !strings.Contains(bodyStr, f1PrivateLeakMarker)
				case "portal_asset":
					oracleOK = sc == http.StatusOK && len(body) >= 1024 && !strings.Contains(bodyStr, f1PrivateLeakMarker)
				case "dash_excluded":
					oracleOK = (sc == http.StatusUnauthorized || sc == http.StatusForbidden) && !strings.Contains(bodyStr, f1PrivateLeakMarker) && !strings.Contains(bodyStr, f1DashboardMarker)
				case "dash_auth":
					if sc == http.StatusOK {
						oracleOK = strings.Contains(bodyStr, f1DashboardMarker)
					} else if sc == http.StatusForbidden {
						oracleOK = strings.Contains(strings.ToLower(resp.Header.Get("Cache-Control")), "no-store")
					} else if (sc == http.StatusBadGateway || sc == http.StatusServiceUnavailable) && opts.AllowOutage {
						oracleOK = true
					} else {
						oracleOK = false
					}
				}
				if !oracleOK {
					atomic.AddInt64(&violations, 1)
					writeEvent(map[string]any{"event": "oracle_violation", "class": class, "status": sc})
				}

				latMu.Lock()
				latencies = append(latencies, lat)
				latMu.Unlock()

				if opts.Pace > 0 {
					time.Sleep(opts.Pace)
				}
			}
		}()
	}

	wg.Wait()
	cancel()

	summary := f1Summary{
		DurationMS:        time.Since(t0).Milliseconds(),
		Workers:           opts.Workers,
		TotalRequests:     atomic.LoadInt64(&totalReqs),
		PortalHTMLCount:   atomic.LoadInt64(&portalHTML),
		PortalAssetCount:  atomic.LoadInt64(&portalAsset),
		DashAuthCount:     atomic.LoadInt64(&dashAuth),
		DashExcludedCount: atomic.LoadInt64(&dashExcl),
		Status2xx:         atomic.LoadInt64(&s2xx),
		Status401:         atomic.LoadInt64(&s401),
		Status403:         atomic.LoadInt64(&s403),
		Status429:         atomic.LoadInt64(&s429),
		Status5xx:         atomic.LoadInt64(&s5xx),
		TransportErrors:   atomic.LoadInt64(&errTrans),
		OracleViolations:  atomic.LoadInt64(&violations),
		Protocols:         protos,
		HeldOpened:        atomic.LoadInt64(&heldOpened),
		HeldClosed:        atomic.LoadInt64(&heldClosed),
		ProbePortalOK:     atomic.LoadInt64(&probePortalOK),
		ProbePortalFail:   atomic.LoadInt64(&probePortalFail),
		ProbeDashOK:       atomic.LoadInt64(&probeDashOK),
		ProbeDashFail:     atomic.LoadInt64(&probeDashFail),
	}
	if len(latencies) > 0 {
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		summary.LatencyMinMS = latencies[0]
		summary.LatencyMaxMS = latencies[len(latencies)-1]
		summary.LatencyP50MS = latencies[int(float64(len(latencies))*0.50)]
		summary.LatencyP95MS = latencies[int(float64(len(latencies))*0.95)]
		summary.LatencyP99MS = latencies[int(float64(len(latencies))*0.99)]
		var sum int64
		for _, v := range latencies {
			sum += v
		}
		summary.LatencyAvgMS = float64(sum) / float64(len(latencies))
	}
	if opts.SummaryPath != "" {
		if err := os.MkdirAll(filepath.Dir(opts.SummaryPath), 0o755); err == nil {
			b, _ := json.MarshalIndent(summary, "", "  ")
			_ = os.WriteFile(opts.SummaryPath, append(b, '\n'), 0o644)
		}
	}
	fmt.Printf("F1 Summary: total=%d 2xx=%d 401=%d 403=%d 429=%d 5xx=%d violations=%d probe_portal_fail=%d\n",
		summary.TotalRequests, summary.Status2xx, summary.Status401, summary.Status403,
		summary.Status429, summary.Status5xx, summary.OracleViolations, summary.ProbePortalFail)
	if summary.OracleViolations > 0 || summary.ProbePortalFail > 0 || summary.TotalRequests == 0 {
		return 1
	}
	return 0
}

type f1SharedH2Options struct {
	TLSBase      string
	CAFile       string
	ClientCert   string
	ClientKey    string
	PortalHost   string
	DashHost     string
	AuthUser     string
	AuthPassword string
	Pace         time.Duration
	ReadyFile    string
	WithWS       bool
	WSEchoClose  bool
	EventLogPath string
	SummaryPath  string
	Timeout      time.Duration
}

type f1SharedH2Result struct {
	DialCount                  int    `json:"dial_count"`
	LocalAddr                  string `json:"local_addr"`
	RemoteAddr                 string `json:"remote_addr"`
	NegotiatedProtocol         string `json:"negotiated_protocol"`
	PortalStreamStatus         int    `json:"portal_stream_status"`
	PortalStreamBytes          int    `json:"portal_stream_bytes"`
	PortalStreamCompletedOK    bool   `json:"portal_stream_completed_ok"`
	DashSSEStatus              int    `json:"dash_sse_status"`
	DashSSEClosedAtUTC         string `json:"dash_sse_closed_at_utc"`
	DashSSEClosedMonoNS        int64  `json:"dash_sse_closed_mono_ns"`
	WSOpened                   bool   `json:"ws_opened"`
	WSCloseCode                int    `json:"ws_close_code"`
	WSGracefulHandshake        bool   `json:"ws_graceful_handshake"`
	WSClosedAtUTC              string `json:"ws_closed_at_utc,omitempty"`
	WSClosedMonoNS             int64  `json:"ws_closed_mono_ns,omitempty"`
	PostChangePortalSameConn   int    `json:"post_change_portal_same_conn_status"`
	PostChangeDashSameConn     int    `json:"post_change_dash_same_conn_status"`
	PostChangeDashCacheControl string `json:"post_change_dash_cache_control"`
}

func runF1SharedH2(opts f1SharedH2Options) int {
	t0 := time.Now()
	if opts.Timeout <= 0 {
		opts.Timeout = 15 * time.Second
	}
	caBytes, err := os.ReadFile(opts.CAFile)
	if err != nil {
		fmt.Printf("f1SharedH2 read caFile: %v\n", err)
		return 1
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caBytes) {
		fmt.Println("f1SharedH2: invalid CA PEM")
		return 1
	}
	u, err := url.Parse(opts.TLSBase)
	if err != nil || u.Host == "" {
		fmt.Printf("f1SharedH2: bad -tls %q\n", opts.TLSBase)
		return 1
	}
	targetAddr := u.Host
	if !strings.Contains(targetAddr, ":") {
		targetAddr += ":443"
	}

	var certs []tls.Certificate
	if opts.ClientCert != "" && opts.ClientKey != "" {
		if c, lerr := tls.LoadX509KeyPair(opts.ClientCert, opts.ClientKey); lerr == nil {
			certs = []tls.Certificate{c}
		}
	}

	var evMu sync.Mutex
	var evFile *os.File
	if opts.EventLogPath != "" {
		_ = os.MkdirAll(filepath.Dir(opts.EventLogPath), 0o755)
		evFile, _ = os.Create(opts.EventLogPath)
		if evFile != nil {
			defer evFile.Close()
		}
	}
	writeEvent := func(fields map[string]any) {
		if evFile == nil {
			return
		}
		now := time.Now()
		fields["t"] = now.UTC().Format(time.RFC3339Nano)
		fields["mono_ns"] = now.Sub(t0).Nanoseconds()
		b, _ := json.Marshal(fields)
		evMu.Lock()
		_, _ = evFile.Write(append(b, '\n'))
		evMu.Unlock()
	}

	// Dial exactly ONE TLS connection with ALPN h2 for both portalHost and dashHost.
	rawConn, err := net.DialTimeout("tcp", targetAddr, 5*time.Second)
	if err != nil {
		fmt.Printf("f1SharedH2 dial failed: %v\n", err)
		return 1
	}
	tlsCfg := &tls.Config{
		RootCAs:      pool,
		Certificates: certs,
		ServerName:   opts.PortalHost,
		NextProtos:   []string{"h2"},
		MinVersion:   tls.VersionTLS12,
	}
	tlsConn := tls.Client(rawConn, tlsCfg)
	if err := tlsConn.Handshake(); err != nil {
		rawConn.Close()
		fmt.Printf("f1SharedH2 handshake failed: %v\n", err)
		return 1
	}
	defer tlsConn.Close()

	state := tlsConn.ConnectionState()
	res := f1SharedH2Result{
		DialCount:          1,
		LocalAddr:          tlsConn.LocalAddr().String(),
		RemoteAddr:         tlsConn.RemoteAddr().String(),
		NegotiatedProtocol: state.NegotiatedProtocol,
	}
	writeEvent(map[string]any{
		"event":       "h2_conn_established",
		"dial_count":  1,
		"local_addr":  res.LocalAddr,
		"remote_addr": res.RemoteAddr,
		"proto":       res.NegotiatedProtocol,
	})

	h2Tr := &http2.Transport{}
	cc, err := h2Tr.NewClientConn(tlsConn)
	if err != nil {
		fmt.Printf("f1SharedH2 NewClientConn failed: %v\n", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
	defer cancel()
	authHdr := ""
	if opts.AuthUser != "" {
		authHdr = "Basic " + base64.StdEncoding.EncodeToString([]byte(opts.AuthUser+":"+opts.AuthPassword))
	}

	// 1. Start Stream 1 on cc: active portal asset download (paced so it stays in-flight across withdrawal)
	portalReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+opts.PortalHost+"/assets/bundle.bin", nil)
	writeEvent(map[string]any{"event": "portal_stream_headers_sent", "host": opts.PortalHost, "path": "/assets/bundle.bin"})
	portalResp, err := cc.RoundTrip(portalReq)
	if err != nil || portalResp.StatusCode != http.StatusOK {
		fmt.Printf("f1SharedH2 portal RoundTrip failed: err=%v\n", err)
		return 1
	}
	res.PortalStreamStatus = portalResp.StatusCode
	firstChunk := make([]byte, 4096)
	nFirst, _ := io.ReadFull(portalResp.Body, firstChunk)
	portalDone := make(chan error, 1)
	var portalBytes atomic.Int64
	portalBytes.Store(int64(nFirst))
	go func() {
		defer portalResp.Body.Close()
		buf := make([]byte, 8192)
		for {
			if opts.Pace > 0 {
				time.Sleep(opts.Pace)
			}
			n, rerr := portalResp.Body.Read(buf)
			if n > 0 {
				portalBytes.Add(int64(n))
			}
			if rerr == io.EOF {
				portalDone <- nil
				return
			}
			if rerr != nil {
				portalDone <- rerr
				return
			}
		}
	}()

	// 2. Start Stream 2 on the SAME cc: active dashboard SSE stream
	dashReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+opts.DashHost+"/events", nil)
	if authHdr != "" {
		dashReq.Header.Set("Authorization", authHdr)
	}
	writeEvent(map[string]any{"event": "dash_sse_headers_sent", "host": opts.DashHost, "path": "/events"})
	dashResp, err := cc.RoundTrip(dashReq)
	if err != nil || dashResp.StatusCode != http.StatusOK {
		fmt.Printf("f1SharedH2 dash SSE RoundTrip failed: err=%v\n", err)
		return 1
	}
	res.DashSSEStatus = dashResp.StatusCode
	sseBuf := make([]byte, 512)
	_, _ = dashResp.Body.Read(sseBuf) // Confirm initial SSE telemetry frame received
	writeEvent(map[string]any{"event": "dash_sse_established", "status": dashResp.StatusCode, "proto": "HTTP/2.0"})

	dashClosed := make(chan struct{})
	go func() {
		defer close(dashClosed)
		defer dashResp.Body.Close()
		buf := make([]byte, 1024)
		for {
			_, rerr := dashResp.Body.Read(buf)
			if rerr != nil {
				now := time.Now()
				res.DashSSEClosedAtUTC = now.UTC().Format(time.RFC3339Nano)
				res.DashSSEClosedMonoNS = now.Sub(t0).Nanoseconds()
				writeEvent(map[string]any{"event": "dash_sse_closed", "proto": "HTTP/2.0"})
				return
			}
		}
	}()

	// 3. Optionally open HTTP/1.1 101 WebSocket to dash.lab.test/ws
	wsClosed := make(chan struct{})
	if opts.WithWS {
		wsConn, wsCode, werr := dialF1WebSocket(targetAddr, opts.DashHost, authHdr, pool, certs)
		if werr != nil || wsCode != http.StatusSwitchingProtocols {
			fmt.Printf("f1SharedH2 WebSocket dial failed: status=%d err=%v\n", wsCode, werr)
			return 1
		}
		res.WSOpened = true
		writeEvent(map[string]any{"event": "ws_established", "status": wsCode, "proto": "HTTP/1.1"})
		go func() {
			defer close(wsClosed)
			defer wsConn.Close()
			code, graceful := readUntilWSClose(wsConn, opts.WSEchoClose)
			now := time.Now()
			res.WSCloseCode = code
			res.WSGracefulHandshake = graceful
			res.WSClosedAtUTC = now.UTC().Format(time.RFC3339Nano)
			res.WSClosedMonoNS = now.Sub(t0).Nanoseconds()
			writeEvent(map[string]any{"event": "ws_closed", "ws_close_code": code, "graceful": graceful})
		}()
	} else {
		close(wsClosed)
	}

	if opts.ReadyFile != "" {
		_ = os.WriteFile(opts.ReadyFile, []byte("ready\n"), 0o600)
	}

	// Wait for narrowed dashboard SSE stream and WebSocket to be retired by Jul
	select {
	case <-dashClosed:
	case <-ctx.Done():
		fmt.Println("f1SharedH2 timed out waiting for dashboard SSE retirement")
		return 1
	}
	select {
	case <-wsClosed:
	case <-ctx.Done():
		fmt.Println("f1SharedH2 timed out waiting for WebSocket retirement")
		return 1
	}

	// Wait for the sibling portal stream on the SAME H2 connection to finish cleanly
	perr := <-portalDone
	res.PortalStreamBytes = int(portalBytes.Load())
	res.PortalStreamCompletedOK = perr == nil && res.PortalStreamBytes >= 4096
	writeEvent(map[string]any{
		"event":        "portal_stream_completed",
		"bytes":        res.PortalStreamBytes,
		"completed_ok": res.PortalStreamCompletedOK,
	})

	// 4. Issue post-change requests on the EXACT SAME http2.ClientConn (cc)
	pReq2, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+opts.PortalHost+"/index.html", nil)
	if pResp2, err := cc.RoundTrip(pReq2); err == nil {
		_, _ = io.Copy(io.Discard, pResp2.Body)
		pResp2.Body.Close()
		res.PostChangePortalSameConn = pResp2.StatusCode
	}
	dReq2, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+opts.DashHost+"/api/summary", nil)
	if authHdr != "" {
		dReq2.Header.Set("Authorization", authHdr)
	}
	if dResp2, err := cc.RoundTrip(dReq2); err == nil {
		_, _ = io.Copy(io.Discard, dResp2.Body)
		dResp2.Body.Close()
		res.PostChangeDashSameConn = dResp2.StatusCode
		res.PostChangeDashCacheControl = dResp2.Header.Get("Cache-Control")
	}
	writeEvent(map[string]any{
		"event":                    "post_change_same_conn_probes",
		"portal_same_conn_status":  res.PostChangePortalSameConn,
		"dash_same_conn_status":    res.PostChangeDashSameConn,
		"dash_same_conn_cache_ctl": res.PostChangeDashCacheControl,
	})

	if opts.SummaryPath != "" {
		_ = os.MkdirAll(filepath.Dir(opts.SummaryPath), 0o755)
		b, _ := json.MarshalIndent(res, "", "  ")
		_ = os.WriteFile(opts.SummaryPath, append(b, '\n'), 0o644)
	}
	if !res.PortalStreamCompletedOK || res.PostChangePortalSameConn != http.StatusOK || res.PostChangeDashSameConn != http.StatusForbidden {
		return 1
	}
	return 0
}

func dialF1WebSocket(targetAddr, dashHost, authHdr string, pool *x509.CertPool, certs []tls.Certificate) (net.Conn, int, error) {
	raw, err := net.DialTimeout("tcp", targetAddr, 5*time.Second)
	if err != nil {
		return nil, 0, err
	}
	tlsConn := tls.Client(raw, &tls.Config{
		RootCAs:      pool,
		Certificates: certs,
		ServerName:   dashHost,
		NextProtos:   []string{"http/1.1"},
		MinVersion:   tls.VersionTLS12,
	})
	if err := tlsConn.Handshake(); err != nil {
		raw.Close()
		return nil, 0, err
	}
	wsKey := base64.StdEncoding.EncodeToString([]byte("f1-ws-key-012345"))
	reqLines := fmt.Sprintf(
		"GET /ws HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: %s\r\n",
		dashHost, wsKey,
	)
	if authHdr != "" {
		reqLines += "Authorization: " + authHdr + "\r\n"
	}
	reqLines += "\r\n"
	if _, err := tlsConn.Write([]byte(reqLines)); err != nil {
		tlsConn.Close()
		return nil, 0, err
	}
	br := bufio.NewReader(tlsConn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		tlsConn.Close()
		return nil, 0, err
	}
	return tlsConn, resp.StatusCode, nil
}

func readUntilWSClose(conn net.Conn, echoClose bool) (int, bool) {
	hdr := make([]byte, 2)
	for {
		if _, err := io.ReadFull(conn, hdr); err != nil {
			return 0, false
		}
		opcode := hdr[0] & 0x0F
		length := int(hdr[1] & 0x7F)
		payload := make([]byte, length)
		if length > 0 {
			if _, err := io.ReadFull(conn, payload); err != nil {
				return 0, false
			}
		}
		if opcode == 0x8 {
			code := 1000
			if len(payload) >= 2 {
				code = int(binary.BigEndian.Uint16(payload[:2]))
			}
			if echoClose {
				// Client-to-server Close frame must be masked (RFC 6455 §5.3)
				maskKey := [4]byte{0x12, 0x34, 0x56, 0x78}
				var closePayload [2]byte
				binary.BigEndian.PutUint16(closePayload[:], uint16(code))
				frame := []byte{0x88, 0x82, maskKey[0], maskKey[1], maskKey[2], maskKey[3], closePayload[0] ^ maskKey[0], closePayload[1] ^ maskKey[1]}
				_, _ = conn.Write(frame)
				return code, true
			}
			// Wait for server forced socket cutoff without echoing Close
			_, _ = io.Copy(io.Discard, conn)
			return code, false
		}
	}
}
