//go:build ignore

// Track 2 — Tiny Go backend for burn-in validation.
// Serves JSON API responses on :8081 by default; handles high concurrency.
// Usage: go run scripts/burn-in-backend.go [-port 8081] [-unix /path.sock] [-tls]
//
// Several burn-in profiles (e.g. burn-in-resilience.toml) round-robin across
// two backend instances (127.0.0.1:8081 and 127.0.0.1:8082) to exercise
// multi-backend admission/balancing; run a second instance with -port 8082
// for those profiles. Pass -unix to listen on a Unix-domain socket instead
// (for the HTTP-over-Unix-upstreams capability, #407); -port is ignored when
// -unix is set. Pass -tls to terminate TLS with the repo's shared test
// certificate (for exercising backend_tls, #409); -unix and -tls are
// mutually exclusive.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func main() {
	port := flag.Int("port", 8081, "TCP port to listen on")
	unixSocket := flag.String("unix", "", "Unix-domain socket path to listen on instead of TCP")
	useTLS := flag.Bool("tls", false, "Terminate TLS using testdata/tls/localhost.{crt,key} (for backend_tls)")
	certFile := flag.String("tls-cert", "testdata/tls/localhost.crt", "TLS certificate file (with -tls)")
	keyFile := flag.String("tls-key", "testdata/tls/localhost.key", "TLS key file (with -tls)")
	flag.Parse()

	http.HandleFunc("/", handler)

	if *unixSocket != "" {
		_ = os.Remove(*unixSocket)
		ln, err := net.Listen("unix", *unixSocket)
		if err != nil {
			fmt.Printf("listen error: %v\n", err)
			os.Exit(1)
		}
		defer ln.Close()
		fmt.Printf("Backend listening on unix:%s\n", *unixSocket)
		if err := http.Serve(ln, nil); err != nil {
			fmt.Printf("server error: %v\n", err)
		}
		return
	}

	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	if *useTLS {
		fmt.Printf("Backend listening on https://%s\n", addr)
		if err := http.ListenAndServeTLS(addr, *certFile, *keyFile, nil); err != nil {
			fmt.Printf("server error: %v\n", err)
		}
		return
	}
	fmt.Printf("Backend listening on http://%s\n", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		fmt.Printf("server error: %v\n", err)
	}
}

func handler(w http.ResponseWriter, r *http.Request) {
	// /…/slow?ms=N: sleep N ms (default 500) before responding, for exercising
	// pending-timeout/circuit accounting against a genuinely slow upstream
	// (-slow-upstream mode in burn-in-load.go). Matched by substring, not
	// prefix, because proxy_pass forwards the full original path unchanged.
	if strings.Contains(r.URL.Path, "/slow") {
		ms := 500
		if v := r.URL.Query().Get("ms"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				ms = n
			}
		}
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
	// /…/flaky?rate=N: fail with 500 on N% of requests (default 30), for
	// exercising retry/circuit behavior against an intermittently failing
	// upstream (-fault mode in burn-in-load.go) without needing to kill a
	// sibling process.
	if strings.Contains(r.URL.Path, "/flaky") {
		rate := 30
		if v := r.URL.Query().Get("rate"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 100 {
				rate = n
			}
		}
		if rand.Intn(100) < rate {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": "injected_failure"})
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Cache-Control", "max-age=10")
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    "ok",
		"path":      r.URL.Path,
		"goroutine": runtime.NumGoroutine(),
	})
}
