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
	"sync/atomic"
	"time"
)

// killUntilNano is a Unix-nanosecond deadline (0 = not killed) set by
// POST /control/kill?duration=Ns, simulating a scheduled backend kill/restore
// cycle (JUL-AUD-019) without actually terminating the process: every request
// on every path is refused for the window, and service resumes on its own
// once the window elapses — no separate "restore" call is needed.
var killUntilNano atomic.Int64

func main() {
	port := flag.Int("port", 8081, "TCP port to listen on")
	unixSocket := flag.String("unix", "", "Unix-domain socket path to listen on instead of TCP")
	useTLS := flag.Bool("tls", false, "Terminate TLS using testdata/tls/localhost.{crt,key} (for backend_tls)")
	certFile := flag.String("tls-cert", "testdata/tls/localhost.crt", "TLS certificate file (with -tls)")
	keyFile := flag.String("tls-key", "testdata/tls/localhost.key", "TLS key file (with -tls)")
	flag.Parse()

	http.HandleFunc("/", handler)
	http.HandleFunc("/control/kill", controlKillHandler)

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

// controlKillHandler is the fault-injection control plane, not a proxied
// route: burn-in-load.go's -fault mode calls it directly against the
// backend's own address (bypassing Jul) to schedule a kill/restore cycle.
func controlKillHandler(w http.ResponseWriter, r *http.Request) {
	dur := 5 * time.Second
	if v := r.URL.Query().Get("duration"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			dur = d
		}
	}
	killUntilNano.Store(time.Now().Add(dur).UnixNano())
	w.WriteHeader(http.StatusAccepted)
	fmt.Printf("killswitch: refusing all requests for %s\n", dur)
}

func handler(w http.ResponseWriter, r *http.Request) {
	// Kill/restore (JUL-AUD-019): while a /control/kill window is active,
	// every request on every path is refused by hijacking and abortively
	// closing the connection (SO_LINGER 0 forces a real TCP RST rather than a
	// graceful FIN), simulating the backend being fully down. Service resumes
	// automatically once the window elapses.
	if until := killUntilNano.Load(); until != 0 && time.Now().UnixNano() < until {
		abortiveClose(w)
		return
	}
	// /…/reset: accept the request, write a declared-but-unfulfilled
	// Content-Length, then abortively close mid-body — a genuine TCP RST
	// (via SO_LINGER 0), not just a truncated read — exercising how the
	// reverse proxy handles an upstream that dies mid-response.
	if strings.Contains(r.URL.Path, "/reset") {
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijack unsupported", http.StatusInternalServerError)
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			return
		}
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 4096\r\n\r\n{\"truncated\":")
		_ = buf.Flush()
		abortiveCloseConn(conn)
		return
	}
	// /…/malformed?kind=short-body|bad-chunk: a genuine protocol-framing
	// violation rather than a dropped connection, exercising response
	// parsing rather than failure detection. short-body (default) is a
	// declared Content-Length the body never reaches, closed gracefully
	// (FIN, not RST) so it is distinguishable from /reset. bad-chunk sends
	// an invalid chunk-size line under Transfer-Encoding: chunked.
	if strings.Contains(r.URL.Path, "/malformed") {
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijack unsupported", http.StatusInternalServerError)
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			return
		}
		if r.URL.Query().Get("kind") == "bad-chunk" {
			_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\n\r\nZZZ_not_hex\r\n{}\r\n")
		} else {
			_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 4096\r\n\r\n{\"short\":true}")
		}
		_ = buf.Flush()
		_ = conn.Close()
		return
	}
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

// abortiveClose hijacks a not-yet-written response and abortively closes it,
// for the /control/kill window on a path that hasn't decided to hijack
// itself yet (the general request path, above).
func abortiveClose(w http.ResponseWriter) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		return
	}
	abortiveCloseConn(conn)
}

// abortiveCloseConn sets SO_LINGER to 0 before closing so the OS sends a real
// TCP RST instead of a graceful FIN — net.Conn has no direct "send RST"
// call, but an abortive close is the standard portable way to produce one.
func abortiveCloseConn(conn net.Conn) {
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetLinger(0)
	}
	_ = conn.Close()
}
