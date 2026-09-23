// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"jul/internal/app"
	"jul/internal/config"
	"jul/internal/migrate/nginx"
	"jul/internal/migrate/nginx/corpus"
	"jul/internal/proxyproto"
)

// loadCorpusRuntimeCandidate imports one repository corpus fixture's
// nginx.conf and returns its translated, strict-valid candidate. Like the
// generic manifest-scenario path in import_corpus_test.go, the candidate is
// marshaled and reparsed before validation so parser defaults (e.g.
// compression's default encoder/type list) apply exactly as they would for
// any TOML file Jul loads from disk; validating the translator's raw
// in-memory struct directly would skip that defaulting step. A runtime E2E
// fixture must always produce a clean candidate; a blocking assessment
// finding fails the test immediately rather than attempting to serve a
// migration-incomplete configuration.
func loadCorpusRuntimeCandidate(t *testing.T, id string) *config.Config {
	t.Helper()
	cfg, _ := importCorpusRuntimeFixture(t, id)
	return validateCorpusRuntimeCandidate(t, id, cfg)
}

// importCorpusRuntimeFixture is the shared first half of
// loadCorpusRuntimeCandidate, split out for fixtures (like mtls-runtime) that
// need to rewrite file-path fields (TLS cert/key/CA) to real generated
// temp files before the marshal-reparse-validate round trip runs, since the
// source nginx.conf's placeholder paths never need to exist on disk for the
// assessment/translation step itself.
func importCorpusRuntimeFixture(t *testing.T, id string) (*config.Config, *nginx.Report) {
	t.Helper()
	root := repositoryCorpusRoot(t)
	fixture, err := corpus.Load(filepath.Join(root, id))
	if err != nil {
		t.Fatalf("load fixture %s: %v", id, err)
	}
	cfg, report, err := nginx.ImportFileWithImportOptions(fixture.RootPath(), nginx.ImportOptions{
		Assessment:  nginx.AssessmentOptions{PathStyle: nginx.AssessmentPathRelative},
		IncludeRoot: fixture.IncludeRoot(),
	})
	if err != nil {
		t.Fatalf("import fixture %s: %v", id, err)
	}
	if report == nil || report.Assessment == nil {
		t.Fatalf("fixture %s: importer returned no assessment", id)
	}
	if report.Assessment.HasBlocking() {
		t.Fatalf("fixture %s: runtime E2E fixture must be strict-valid, found blocking results: %+v", id, report.Assessment.Results)
	}
	return cfg, report
}

// validateCorpusRuntimeCandidate is the shared second half of
// loadCorpusRuntimeCandidate: marshal, reparse, and validate, exactly as Jul
// would load this config from disk (so parser defaults apply).
func validateCorpusRuntimeCandidate(t *testing.T, id string, cfg *config.Config) *config.Config {
	t.Helper()
	toml, err := config.Marshal(cfg)
	if err != nil {
		t.Fatalf("fixture %s: marshal candidate: %v", id, err)
	}
	loaded, err := config.Parse(toml)
	if err != nil {
		t.Fatalf("fixture %s: parse candidate: %v\n%s", id, err, toml)
	}
	if err := config.Validate(loaded); err != nil {
		t.Fatalf("fixture %s: generated candidate does not validate: %v\n%s", id, err, toml)
	}
	return loaded
}

// startRealJulForCorpus starts a real Jul instance from an already-translated
// corpus candidate on a freshly reserved loopback address. It mirrors
// runRealJulCorpusScenarios's own startup sequence in import_corpus_test.go
// but is factored out standalone so a dedicated (non-manifest-scenario)
// runtime test can drive its own requests instead of the generic
// Scenario/Dimension comparison loop.
func startRealJulForCorpus(t *testing.T, name string, cfg *config.Config) (baseURL string, cleanup func()) {
	t.Helper()
	if len(cfg.Servers) != 1 {
		t.Fatalf("%s: runtime tranche requires exactly one server, got %d", name, len(cfg.Servers))
	}
	address := reserveLoopbackAddress(t)
	cfg.Servers[0].Listen = address
	stopTCPBackends := startCorpusTCPBackends(t, cfg)
	if err := app.ValidateRuntimeConfig(context.Background(), cfg); err != nil {
		stopTCPBackends()
		t.Fatalf("%s: runtime preflight: %v", name, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	reload := make(chan struct{})
	var logs corpusLogBuffer
	done := make(chan int, 1)
	go func() {
		done <- app.Serve(ctx, reload, memorySource{name: "<nginx-corpus:" + name + ">", cfg: cfg}, cfg, productName, version, app.WithLogOutput(&logs))
	}()

	client := &http.Client{Timeout: 2 * time.Second}
	baseURL = "http://" + address
	waitForCorpusServer(t, ctx, client, baseURL, corpus.Scenario{Request: corpus.RequestSpec{Method: "GET", Path: "/"}}, done, &logs)

	cleanup = func() {
		cancel()
		select {
		case code := <-done:
			if code != 0 {
				t.Errorf("%s: Jul exit code = %d\nlogs:\n%s", name, code, logs.String())
			}
		case <-time.After(5 * time.Second):
			t.Errorf("%s: Jul did not shut down\nlogs:\n%s", name, logs.String())
		}
		stopTCPBackends()
	}
	return baseURL, cleanup
}

// TestNGINXCorpusWeightedUpstreamDistribution proves that a named NGINX
// upstream with weighted members translates into Jul's weighted round-robin
// selection with the exact distribution the algorithm guarantees (NGX
// weight=3 vs the default weight=1 splits a 40-request, 10-cycle run 30/10 -
// deterministic, no statistical tolerance needed).
func TestNGINXCorpusWeightedUpstreamDistribution(t *testing.T) {
	cfg := loadCorpusRuntimeCandidate(t, "upstream-weighted-runtime")
	baseURL, cleanup := startRealJulForCorpus(t, "upstream-weighted-runtime", cfg)
	defer cleanup()

	client := &http.Client{Timeout: 2 * time.Second}
	counts := map[string]int{}
	const requests = 40
	for i := 0; i < requests; i++ {
		resp, err := client.Get(baseURL + "/")
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		id := resp.Header.Get("X-Corpus-Backend-Id")
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if id == "" {
			t.Fatalf("request %d: missing X-Corpus-Backend-Id", i)
		}
		counts[id]++
	}
	client.CloseIdleConnections()

	weighted := counts["weighted_runtime_pool-0"]
	unweighted := counts["weighted_runtime_pool-1"]
	if weighted+unweighted != requests {
		t.Fatalf("counts = %+v, want %d total", counts, requests)
	}
	if weighted != 30 || unweighted != 10 {
		t.Fatalf("weighted distribution = weight3:%d weight1:%d, want exactly 30:10 (smooth weighted round-robin over %d requests)", weighted, unweighted, requests)
	}
}

// TestNGINXCorpusCompressionRealE2E proves gzip actually engages through a
// real backend and that decoding it recovers the exact original content -
// the migration corpus asserts decoded/logical equivalence, never raw
// compressed bytes.
func TestNGINXCorpusCompressionRealE2E(t *testing.T) {
	cfg := loadCorpusRuntimeCandidate(t, "compression-runtime")
	baseURL, cleanup := startRealJulForCorpus(t, "compression-runtime", cfg)
	defer cleanup()

	req, err := http.NewRequest(http.MethodGet, baseURL+"/", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	// Setting Accept-Encoding ourselves opts out of Go's transparent
	// decompression (net/http only auto-decodes when the caller does not set
	// this header), so the raw Content-Encoding and compressed bytes are
	// actually observable here.
	req.Header.Set("Accept-Encoding", "gzip")
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip (compression did not engage)", got)
	}
	if got := resp.Header.Get("Vary"); !strings.Contains(strings.ToLower(got), "accept-encoding") {
		t.Errorf("Vary = %q, want it to contain Accept-Encoding", got)
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatalf("response is not valid gzip: %v", err)
	}
	decoded, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("decode gzip body: %v", err)
	}
	want := strings.Repeat("corpus-backend-payload ", 100)
	if string(decoded) != want {
		t.Fatalf("decoded body mismatch: got %d bytes, want %d bytes matching the backend's plain payload", len(decoded), len(want))
	}
}

// TestNGINXCorpusWebSocketRealE2E is the H1 WebSocket migration evidence
// (#365): a real WebSocket upgrade, bidirectional exchange, and clean close
// through a real Jul instance proxying to a real local WebSocket-echoing
// backend. H2/H3 Extended CONNECT is explicitly out of scope (#435).
func TestNGINXCorpusWebSocketRealE2E(t *testing.T) {
	cfg := loadCorpusRuntimeCandidate(t, "websocket-runtime")
	baseURL, cleanup := startRealJulForCorpus(t, "websocket-runtime", cfg)
	defer cleanup()

	wsURL := "ws" + strings.TrimPrefix(baseURL, "http") + "/"
	ws, err := websocket.Dial(wsURL, "", baseURL+"/")
	if err != nil {
		t.Fatalf("WebSocket dial through the imported proxy_pass route: %v", err)
	}
	defer ws.Close()

	want := []byte("nginx-migration-websocket-corpus")
	if err := websocket.Message.Send(ws, want); err != nil {
		t.Fatalf("send: %v", err)
	}
	var got []byte
	if err := websocket.Message.Receive(ws, &got); err != nil {
		t.Fatalf("receive: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("echo mismatch: got %q, want %q", got, want)
	}

	binPayload := []byte{0x00, 0x01, 0x02, 0xff, 0xfe, 0x7f}
	if err := websocket.Message.Send(ws, binPayload); err != nil {
		t.Fatalf("send binary: %v", err)
	}
	var gotBin []byte
	if err := websocket.Message.Receive(ws, &gotBin); err != nil {
		t.Fatalf("receive binary: %v", err)
	}
	if string(gotBin) != string(binPayload) {
		t.Fatalf("binary echo mismatch: got %v, want %v", gotBin, binPayload)
	}

	if err := ws.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// corpusWebSocketEchoHandler is the WebSocket half of corpusBackendHandler: a
// deterministic local peer that echoes every message back unmodified.
func corpusWebSocketEchoHandler() http.Handler {
	return websocket.Handler(func(ws *websocket.Conn) {
		for {
			var msg []byte
			if err := websocket.Message.Receive(ws, &msg); err != nil {
				return
			}
			if err := websocket.Message.Send(ws, msg); err != nil {
				return
			}
		}
	})
}

// TestNGINXCorpusUpstreamFailoverRealE2E proves the max_fails/fail_timeout ->
// Jul [upstreams.resilience] translation actually protects real traffic: one
// backend never accepts a connection at all (the genuine "connect failure"
// class that trips Jul's passive circuit breaker, unlike an in-band 5xx from
// a live backend, which does not), yet every client request still succeeds -
// Jul's default retry-every-distinct-backend-once behavior masks the first
// failure, and the max_fails=1 breaker then excludes the dead backend for
// fail_timeout so every following request goes straight to the healthy one.
// This bypasses startRealJulForCorpus/startCorpusTCPBackends, which always
// gives every upstream member a real listening backend, because this test
// specifically needs one member that is never listening.
func TestNGINXCorpusUpstreamFailoverRealE2E(t *testing.T) {
	cfg := loadCorpusRuntimeCandidate(t, "upstream-failover-runtime")
	if len(cfg.Upstreams) != 1 || len(cfg.Upstreams[0].Servers) != 2 {
		t.Fatalf("upstream-failover-runtime: want 1 upstream with 2 servers, got %+v", cfg.Upstreams)
	}
	if cfg.Upstreams[0].Resilience == nil || cfg.Upstreams[0].Resilience.MaxFails != 1 {
		t.Fatalf("upstream-failover-runtime: candidate resilience = %+v, want MaxFails=1 from the translated max_fails", cfg.Upstreams[0].Resilience)
	}

	healthyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve healthy backend port: %v", err)
	}
	var hits int64
	healthy := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("X-Corpus-Backend-Id", "healthy")
		_, _ = io.WriteString(w, "healthy-backend-response")
	})}
	go func() { _ = healthy.Serve(healthyLn) }()
	defer healthy.Close()

	// Reserve a port and immediately release it: nothing binds it again
	// during the test, so every dial attempt gets a genuine connection
	// refused.
	downLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve down backend port: %v", err)
	}
	downAddr := downLn.Addr().String()
	if err := downLn.Close(); err != nil {
		t.Fatalf("release down backend port: %v", err)
	}

	cfg.Upstreams[0].Servers[0].Address = healthyLn.Addr().String()
	cfg.Upstreams[0].Servers[1].Address = downAddr
	cfg.Servers[0].Listen = reserveLoopbackAddress(t)

	if err := app.ValidateRuntimeConfig(context.Background(), cfg); err != nil {
		t.Fatalf("runtime preflight: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reload := make(chan struct{})
	var logs corpusLogBuffer
	done := make(chan int, 1)
	go func() {
		done <- app.Serve(ctx, reload, memorySource{name: "<nginx-corpus:upstream-failover-runtime>", cfg: cfg}, cfg, productName, version, app.WithLogOutput(&logs))
	}()

	client := &http.Client{Timeout: 2 * time.Second}
	baseURL := "http://" + cfg.Servers[0].Listen
	waitForCorpusServer(t, ctx, client, baseURL, corpus.Scenario{Request: corpus.RequestSpec{Method: "GET", Path: "/"}}, done, &logs)
	// The readiness probe above already issued a real request against the
	// pool; only requests from this test's own loop should count toward the
	// "every request landed on the healthy backend" assertion below.
	atomic.StoreInt64(&hits, 0)

	defer func() {
		cancel()
		select {
		case code := <-done:
			if code != 0 {
				t.Errorf("upstream-failover-runtime: Jul exit code = %d\nlogs:\n%s", code, logs.String())
			}
		case <-time.After(5 * time.Second):
			t.Errorf("upstream-failover-runtime: Jul did not shut down\nlogs:\n%s", logs.String())
		}
	}()

	const requests = 12
	for i := 0; i < requests; i++ {
		resp, err := client.Get(baseURL + "/")
		if err != nil {
			t.Fatalf("request %d: %v (failover should be transparent to the client)", i, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200 (failover should be transparent to the client)", i, resp.StatusCode)
		}
	}
	client.CloseIdleConnections()

	if got := atomic.LoadInt64(&hits); got != requests {
		t.Fatalf("healthy backend received %d of %d requests, want all %d (the down backend should have been excluded after tripping the max_fails=1 circuit breaker)", got, requests, requests)
	}
}

// TestNGINXCorpusProxyPassURIRealE2E proves the precise, previously
// undocumented difference between nginx's and Jul's proxy_pass URI-rewriting
// semantics through a real Jul instance: nginx strips the matched location
// prefix and replaces it with the proxy_pass path, while Jul's proxy
// (net/http/httputil.ProxyRequest.SetURL) always prepends the proxy_pass path
// to the client's full incoming request path without stripping anything -
// location "/api" + proxy_pass ".../v2" turns a client request for "/api/foo"
// into "/v2/api/foo" at the backend, not nginx's "/v2/foo". This bypasses
// startRealJulForCorpus/startCorpusTCPBackends, which always overwrites every
// upstream member with its own generic backend, because this test needs its
// backend to record the exact path it received.
func TestNGINXCorpusProxyPassURIRealE2E(t *testing.T) {
	cfg := loadCorpusRuntimeCandidate(t, "proxy-pass-uri-runtime")
	if len(cfg.Upstreams) != 1 || len(cfg.Upstreams[0].Servers) != 1 {
		t.Fatalf("proxy-pass-uri-runtime: want 1 upstream with 1 server, got %+v", cfg.Upstreams)
	}

	var gotPath string
	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve backend port: %v", err)
	}
	backend := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	})}
	go func() { _ = backend.Serve(backendLn) }()
	defer backend.Close()

	cfg.Upstreams[0].Servers[0].Address = backendLn.Addr().String()
	cfg.Servers[0].Listen = reserveLoopbackAddress(t)

	if err := app.ValidateRuntimeConfig(context.Background(), cfg); err != nil {
		t.Fatalf("runtime preflight: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reload := make(chan struct{})
	var logs corpusLogBuffer
	done := make(chan int, 1)
	go func() {
		done <- app.Serve(ctx, reload, memorySource{name: "<nginx-corpus:proxy-pass-uri-runtime>", cfg: cfg}, cfg, productName, version, app.WithLogOutput(&logs))
	}()

	client := &http.Client{Timeout: 2 * time.Second}
	baseURL := "http://" + cfg.Servers[0].Listen
	waitForCorpusServer(t, ctx, client, baseURL, corpus.Scenario{Request: corpus.RequestSpec{Method: "GET", Path: "/api/"}}, done, &logs)

	defer func() {
		cancel()
		select {
		case code := <-done:
			if code != 0 {
				t.Errorf("proxy-pass-uri-runtime: Jul exit code = %d\nlogs:\n%s", code, logs.String())
			}
		case <-time.After(5 * time.Second):
			t.Errorf("proxy-pass-uri-runtime: Jul did not shut down\nlogs:\n%s", logs.String())
		}
	}()

	resp, err := client.Get(baseURL + "/api/foo")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	client.CloseIdleConnections()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	const want = "/v2/api/foo"
	if gotPath != want {
		t.Fatalf("backend-visible path = %q, want %q (proxy_pass path prepended to the full incoming path, not location-prefix-replaced)", gotPath, want)
	}
}

// TestNGINXCorpusProxyReadTimeoutRealE2E proves the proxy_connect_timeout/
// proxy_read_timeout -> Jul location translation actually takes effect
// through a real Jul instance: a backend that stalls before writing any
// response byte trips the configured 1s proxy_read_timeout, and Jul returns
// a 504 (docs/core-http.md's upstream_timeout mapping) well before the
// backend's full 3s delay elapses. This bypasses startRealJulForCorpus/
// startCorpusTCPBackends because it needs a deliberately slow backend, not
// the generic immediate-response one.
func TestNGINXCorpusProxyReadTimeoutRealE2E(t *testing.T) {
	cfg := loadCorpusRuntimeCandidate(t, "timeout-runtime")
	if len(cfg.Upstreams) != 1 || len(cfg.Upstreams[0].Servers) != 1 {
		t.Fatalf("timeout-runtime: want 1 upstream with 1 server, got %+v", cfg.Upstreams)
	}
	if cfg.Servers[0].Locations[0].ProxyReadTimeout.Std() != time.Second {
		t.Fatalf("timeout-runtime: candidate proxy_read_timeout = %s, want 1s", cfg.Servers[0].Locations[0].ProxyReadTimeout.Std())
	}

	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve backend port: %v", err)
	}
	backend := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second)
		w.WriteHeader(http.StatusOK)
	})}
	go func() { _ = backend.Serve(backendLn) }()
	defer backend.Close()

	cfg.Upstreams[0].Servers[0].Address = backendLn.Addr().String()
	cfg.Servers[0].Listen = reserveLoopbackAddress(t)

	if err := app.ValidateRuntimeConfig(context.Background(), cfg); err != nil {
		t.Fatalf("runtime preflight: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reload := make(chan struct{})
	var logs corpusLogBuffer
	done := make(chan int, 1)
	go func() {
		done <- app.Serve(ctx, reload, memorySource{name: "<nginx-corpus:timeout-runtime>", cfg: cfg}, cfg, productName, version, app.WithLogOutput(&logs))
	}()

	client := &http.Client{Timeout: 10 * time.Second}
	baseURL := "http://" + cfg.Servers[0].Listen
	waitForCorpusServer(t, ctx, client, baseURL, corpus.Scenario{Request: corpus.RequestSpec{Method: "GET", Path: "/warmup"}}, done, &logs)

	defer func() {
		cancel()
		select {
		case code := <-done:
			if code != 0 {
				t.Errorf("timeout-runtime: Jul exit code = %d\nlogs:\n%s", code, logs.String())
			}
		case <-time.After(5 * time.Second):
			t.Errorf("timeout-runtime: Jul did not shut down\nlogs:\n%s", logs.String())
		}
	}()

	start := time.Now()
	resp, err := client.Get(baseURL + "/")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	client.CloseIdleConnections()

	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504 (proxy_read_timeout should have cut off the stalling backend)", resp.StatusCode)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("elapsed = %s, want well under the backend's 3s stall (proxy_read_timeout=1s should have fired, not the client's own timeout or the full backend delay)", elapsed)
	}
}

// TestNGINXCorpusCacheRealE2E proves the bounded proxy_cache_path/proxy_cache
// -> Jul [cache] translation actually caches through a real Jul instance: the
// first request is a real MISS served by the backend, the immediate second
// request is a HIT served from the store without contacting the backend
// again, and the cached representation (including the backend's own
// X-Corpus-Backend-Id header, per the stored-headers contract in
// docs/cache.md) is byte-identical to what the origin returned. It also
// proves the client's own `Cache-Control: no-store` privacy opt-out actually
// bypasses lookup and storage (X-Cache: BYPASS) without disturbing the
// already-stored entry, which the next plain request still finds as a HIT.
func TestNGINXCorpusCacheRealE2E(t *testing.T) {
	cfg := loadCorpusRuntimeCandidate(t, "cache-runtime")
	if !cfg.Cache.Enabled {
		t.Fatalf("cache-runtime: candidate [cache].enabled = false, want true")
	}
	// The fixture's disk_path looks like a real deployment path (proving the
	// proxy_cache_path -> [cache].disk_path translation), but this process
	// cannot write there; rewrite it to a writable temp directory exactly as
	// startRealJulForCorpus already rewrites the listen address.
	cfg.Cache.DiskPath = t.TempDir()
	baseURL, cleanup := startRealJulForCorpus(t, "cache-runtime", cfg)
	defer cleanup()

	client := &http.Client{Timeout: 2 * time.Second}
	want := strings.Repeat("corpus-backend-payload ", 100)

	// startRealJulForCorpus's own readiness probe already issued a real GET
	// "/" (see waitForCorpusServer), which would otherwise have consumed the
	// MISS this test wants to observe. Requesting a different URI under the
	// same "/" prefix location keys a fresh cache entry (Jul's cache key is
	// method + host + URI, per docs/cache.md), so the MISS below is genuine.
	const path = "/cache-e2e-check"
	get := func() *http.Response {
		t.Helper()
		resp, err := client.Get(baseURL + path)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		return resp
	}
	readAndClose := func(resp *http.Response) string {
		t.Helper()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		_ = resp.Body.Close()
		return string(body)
	}

	first := get()
	firstBody := readAndClose(first)
	if got := first.Header.Get("X-Cache"); got != "MISS" {
		t.Fatalf("first request X-Cache = %q, want MISS", got)
	}
	firstBackendID := first.Header.Get("X-Corpus-Backend-Id")
	if firstBackendID == "" {
		t.Fatalf("first request missing X-Corpus-Backend-Id (backend was not actually reached)")
	}
	if firstBody != want {
		t.Fatalf("first request body mismatch: got %d bytes, want %d bytes matching the backend's payload", len(firstBody), len(want))
	}

	second := get()
	secondBody := readAndClose(second)
	client.CloseIdleConnections()
	if got := second.Header.Get("X-Cache"); got != "HIT" {
		t.Fatalf("second request X-Cache = %q, want HIT (cache did not engage)", got)
	}
	if got := second.Header.Get("X-Corpus-Backend-Id"); got != firstBackendID {
		t.Fatalf("second request X-Corpus-Backend-Id = %q, want %q (the stored response's own headers, replayed verbatim)", got, firstBackendID)
	}
	if secondBody != want {
		t.Fatalf("second request body mismatch: got %d bytes, want %d bytes matching the first response", len(secondBody), len(want))
	}

	// A request `Cache-Control: no-store` bypasses lookup and storage
	// entirely (docs/cache.md's shared-cache contract), proving privacy
	// opt-out actually works through a real Jul instance, not just the
	// unconditional MISS/HIT path above.
	req, err := http.NewRequest(http.MethodGet, baseURL+path, nil)
	if err != nil {
		t.Fatalf("build no-store request: %v", err)
	}
	req.Header.Set("Cache-Control", "no-store")
	third, err := client.Do(req)
	if err != nil {
		t.Fatalf("no-store request: %v", err)
	}
	thirdBody := readAndClose(third)
	client.CloseIdleConnections()
	if got := third.Header.Get("X-Cache"); got != "BYPASS" {
		t.Fatalf("no-store request X-Cache = %q, want BYPASS", got)
	}
	if thirdBody != want {
		t.Fatalf("no-store request body mismatch: got %d bytes, want %d bytes matching the backend's payload", len(thirdBody), len(want))
	}

	// The bypassed request must not have evicted or altered the existing
	// stored entry: the immediately following plain request is still a HIT.
	fourth := get()
	fourthBody := readAndClose(fourth)
	client.CloseIdleConnections()
	if got := fourth.Header.Get("X-Cache"); got != "HIT" {
		t.Fatalf("post-bypass request X-Cache = %q, want HIT (no-store must not evict the existing stored entry)", got)
	}
	if fourthBody != want {
		t.Fatalf("post-bypass request body mismatch: got %d bytes, want %d bytes matching the stored entry", len(fourthBody), len(want))
	}
}

// TestNGINXCorpusProxyProtocolRealE2E proves the imported HTTP
// PROXY-protocol identity translation (#426, evidenced here per #366's own
// 2026-09-21 amendment) actually enforces its trust boundary through a real
// Jul instance: a connection whose own peer address matches trusted_proxies
// and carries a real PROXY header (both v1 text and v2 binary wire formats)
// is honored as the canonical client (observable in the X-Forwarded-For Jul
// forwards upstream), while the identical bytes from a peer address outside
// trusted_proxies are refused outright rather than served on a spoofed or
// fallback identity - per docs/configuration.md's "a connection from an
// address outside the set is refused" contract.
//
// Every dial here originates from the ordinary default loopback address
// (127.0.0.1) - the only loopback address guaranteed present without extra
// host configuration on every CI platform (unlike Linux, macOS does not
// auto-alias 127.0.0.0/8 to lo0, so binding to a second loopback alias such
// as 127.0.0.2 is not portable). The trusted and untrusted positions are
// instead produced by running two Jul instances with different
// trusted_proxies values against that same peer address:
//   - the untrusted/CIDR-mismatch proof runs against the fixture's own
//     unmodified translated candidate (trusted_proxies=["127.0.0.2/32"],
//     straight from set_real_ip_from) - evidence about the real translation
//     output, not a synthetic stand-in;
//   - the trusted-peer, malformed-header, and spoofed-identity proofs run
//     against a second instance whose trusted_proxies is overridden to
//     ["127.0.0.1/32"], purely so the peer check can be satisfied without an
//     OS-level address alias. It exercises the exact same runtime
//     enforcement mechanism (proxy_protocol = "in" plus
//     client_address.trusted_proxies), just against a CIDR value that is
//     dialable on every platform.
func TestNGINXCorpusProxyProtocolRealE2E(t *testing.T) {
	var gotForwardedFor string
	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve backend port: %v", err)
	}
	backend := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotForwardedFor = r.Header.Get("X-Forwarded-For")
		w.WriteHeader(http.StatusOK)
	})}
	go func() { _ = backend.Serve(backendLn) }()
	defer backend.Close()

	// startInstance imports a fresh copy of the fixture, points it at the
	// shared backend, optionally overrides trusted_proxies to a
	// universally-dialable CIDR, and starts a real Jul instance. Readiness
	// only needs the OS-level TCP accept to be up (a plain, payload-less
	// dial), since the PROXY-protocol admission check runs after accept and
	// would otherwise never succeed for the deliberately-untrusted instance.
	startInstance := func(name string, trustedProxies []string) string {
		t.Helper()
		cfg := loadCorpusRuntimeCandidate(t, "proxy-protocol-runtime")
		if len(cfg.Servers) != 1 || cfg.Servers[0].ProxyProtocol != "in" {
			t.Fatalf("%s: want 1 server with proxy_protocol=in, got %+v", name, cfg.Servers)
		}
		if len(cfg.Upstreams) != 1 || len(cfg.Upstreams[0].Servers) != 1 {
			t.Fatalf("%s: want 1 upstream with 1 server, got %+v", name, cfg.Upstreams)
		}
		cfg.Upstreams[0].Servers[0].Address = backendLn.Addr().String()
		cfg.Servers[0].Listen = reserveLoopbackAddress(t)
		if trustedProxies != nil {
			cfg.Servers[0].ClientAddress.TrustedProxies = trustedProxies
		}
		if err := app.ValidateRuntimeConfig(context.Background(), cfg); err != nil {
			t.Fatalf("%s: runtime preflight: %v", name, err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		reload := make(chan struct{})
		logs := &corpusLogBuffer{}
		done := make(chan int, 1)
		go func() {
			done <- app.Serve(ctx, reload, memorySource{name: "<nginx-corpus:proxy-protocol-runtime:" + name + ">", cfg: cfg}, cfg, productName, version, app.WithLogOutput(logs))
		}()
		t.Cleanup(func() {
			cancel()
			select {
			case code := <-done:
				if code != 0 {
					t.Errorf("%s: Jul exit code = %d\nlogs:\n%s", name, code, logs.String())
				}
			case <-time.After(5 * time.Second):
				t.Errorf("%s: Jul did not shut down\nlogs:\n%s", name, logs.String())
			}
		})

		addr := cfg.Servers[0].Listen
		deadline := time.Now().Add(5 * time.Second)
		var lastErr error
		for time.Now().Before(deadline) {
			select {
			case code := <-done:
				done <- code
				t.Fatalf("%s: Jul exited during startup with code %d\nlogs:\n%s", name, code, logs.String())
			default:
			}
			probe, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
			if err == nil {
				probe.Close()
				lastErr = nil
				break
			}
			lastErr = err
			time.Sleep(20 * time.Millisecond)
		}
		if lastErr != nil {
			t.Fatalf("%s: Jul did not become reachable before the startup deadline: %v\nlogs:\n%s", name, lastErr, logs.String())
		}
		return addr
	}

	sendRaw := func(addr, payload string) (statusLine string, connErr error) {
		t.Helper()
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			return "", err
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := conn.Write([]byte(payload)); err != nil {
			return "", err
		}
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.Status, nil
	}

	const assertedClient = "203.0.113.7"
	proxyLine := "PROXY TCP4 " + assertedClient + " 127.0.0.1 51234 18108\r\n"
	request := "GET / HTTP/1.1\r\nHost: proxy-protocol-runtime.test\r\nConnection: close\r\n\r\n"
	spoofedXFFRequest := "GET / HTTP/1.1\r\nHost: proxy-protocol-runtime.test\r\nConnection: close\r\nX-Forwarded-For: 198.51.100.9\r\n\r\n"

	// Untrusted / CIDR-mismatch: the fixture's own unmodified translated
	// candidate trusts only 127.0.0.2/32 (from set_real_ip_from), so a
	// connection from the default loopback address must be refused even
	// though it carries a well-formed PROXY header.
	untrustedAddr := startInstance("real-candidate", nil)
	status, err := sendRaw(untrustedAddr, proxyLine+request)
	if err == nil && strings.HasPrefix(status, "200") {
		t.Fatalf("untrusted peer: got status 200 (X-Forwarded-For=%q), want the connection refused", gotForwardedFor)
	}

	// Trusted peer: a second instance whose trusted_proxies is overridden to
	// the one address every platform can dial from without extra host
	// configuration.
	trustedAddr := startInstance("trusted-boundary", []string{"127.0.0.1/32"})

	gotForwardedFor = ""
	status, err = sendRaw(trustedAddr, proxyLine+request)
	if err != nil {
		t.Fatalf("trusted relay: connection/request failed: %v", err)
	}
	if !strings.HasPrefix(status, "200") {
		t.Fatalf("trusted relay: status = %q, want 200", status)
	}
	if gotForwardedFor != assertedClient {
		t.Fatalf("trusted relay: backend X-Forwarded-For = %q, want %q (the PROXY-asserted client)", gotForwardedFor, assertedClient)
	}

	// Malformed PROXY header from an otherwise-trusted peer must also be
	// refused - trust in the source address does not extend to trusting
	// whatever bytes it happens to send.
	gotForwardedFor = ""
	status, err = sendRaw(trustedAddr, "PROXY GARBAGE NOT A REAL HEADER\r\n"+request)
	if err == nil && strings.HasPrefix(status, "200") {
		t.Fatalf("malformed PROXY header: got status 200 (X-Forwarded-For=%q), want the connection refused", gotForwardedFor)
	}

	// A client-supplied X-Forwarded-For must never override the
	// PROXY-derived canonical identity: Jul clears client-supplied
	// X-Forwarded-* and rebuilds it from its own trusted view.
	gotForwardedFor = ""
	status, err = sendRaw(trustedAddr, proxyLine+spoofedXFFRequest)
	if err != nil {
		t.Fatalf("trusted relay with spoofed X-Forwarded-For: connection/request failed: %v", err)
	}
	if !strings.HasPrefix(status, "200") {
		t.Fatalf("trusted relay with spoofed X-Forwarded-For: status = %q, want 200", status)
	}
	if gotForwardedFor != assertedClient {
		t.Fatalf("trusted relay with spoofed X-Forwarded-For: backend X-Forwarded-For = %q, want %q (the PROXY-derived identity, not the client-supplied header)", gotForwardedFor, assertedClient)
	}

	// Trusted relay, PROXY v2 binary header: nginx's real_ip_header
	// proxy_protocol accepts either wire version from a balancer, and Jul's
	// listener parses both v1 text and v2 binary through the same shared
	// internal/proxyproto.ReadHeader, so this is a distinct wire format to
	// prove, not merely a v1 retest.
	gotForwardedFor = ""
	var v2Header bytes.Buffer
	if err := proxyproto.WriteV2(&v2Header,
		&net.TCPAddr{IP: net.ParseIP(assertedClient), Port: 51235},
		&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 18108},
	); err != nil {
		t.Fatalf("encode PROXY v2 header: %v", err)
	}
	status, err = sendRaw(trustedAddr, v2Header.String()+request)
	if err != nil {
		t.Fatalf("trusted relay with PROXY v2: connection/request failed: %v", err)
	}
	if !strings.HasPrefix(status, "200") {
		t.Fatalf("trusted relay with PROXY v2: status = %q, want 200", status)
	}
	if gotForwardedFor != assertedClient {
		t.Fatalf("trusted relay with PROXY v2: backend X-Forwarded-For = %q, want %q (the PROXY-asserted client)", gotForwardedFor, assertedClient)
	}
}

// mtlsPKI is a throwaway CA used to issue a server certificate and client
// certificates for the mTLS real-E2E test below, mirroring the reusable
// pattern in internal/handler/backendtls_test.go (a different package, so
// duplicated here rather than exported for one test's sake).
type mtlsPKI struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newMTLSPKI(t *testing.T, cn string) *mtlsPKI {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &mtlsPKI{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// issue signs a leaf certificate (valid for both server and client auth) for
// the given common name, with 127.0.0.1 as an IP SAN so a loopback dial can
// verify the server leaf by address.
func (p *mtlsPKI) issue(t *testing.T, cn string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano() + 1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.cert, &key.PublicKey, p.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return pair
}

func writeMTLSFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestNGINXCorpusMTLSRealE2E proves the ssl_verify_client/ssl_client_certificate
// -> Jul servers[].tls.client_auth translation actually enforces mutual TLS
// through a real Jul instance and a freshly generated ephemeral CA/leaf
// certificate chain: a client certificate signed by the configured CA is
// accepted, while no certificate at all and a certificate signed by a
// different CA are both rejected at the TLS handshake (mode=require).
func TestNGINXCorpusMTLSRealE2E(t *testing.T) {
	cfg, _ := importCorpusRuntimeFixture(t, "mtls-runtime")
	if len(cfg.Servers) != 1 || cfg.Servers[0].TLS == nil || cfg.Servers[0].TLS.ClientAuth == nil || cfg.Servers[0].TLS.ClientAuth.Mode != "require" {
		t.Fatalf("mtls-runtime: want 1 server with ClientAuth.Mode=require, got %+v", cfg.Servers)
	}

	dir := t.TempDir()
	serverCA := newMTLSPKI(t, "mtls-runtime server CA (test-only, ephemeral)")
	otherCA := newMTLSPKI(t, "mtls-runtime other CA (test-only, ephemeral)")
	serverCert := serverCA.issue(t, "mtls-runtime.test")
	trustedClientCert := serverCA.issue(t, "trusted-client")
	untrustedClientCert := otherCA.issue(t, "untrusted-client")

	serverCertPath := writeMTLSFile(t, dir, "server.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverCert.Certificate[0]}))
	serverKeyDER, err := x509.MarshalECPrivateKey(serverCert.PrivateKey.(*ecdsa.PrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	serverKeyPath := writeMTLSFile(t, dir, "server.key", pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: serverKeyDER}))
	caPath := writeMTLSFile(t, dir, "ca.pem", serverCA.pem)

	cfg.Servers[0].TLS.Cert = serverCertPath
	cfg.Servers[0].TLS.Key = serverKeyPath
	cfg.Servers[0].TLS.ClientAuth.CAFile = caPath
	cfg.Servers[0].Listen = reserveLoopbackAddress(t)
	loaded := validateCorpusRuntimeCandidate(t, "mtls-runtime", cfg)

	if err := app.ValidateRuntimeConfig(context.Background(), loaded); err != nil {
		t.Fatalf("runtime preflight: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reload := make(chan struct{})
	var logs corpusLogBuffer
	done := make(chan int, 1)
	go func() {
		done <- app.Serve(ctx, reload, memorySource{name: "<nginx-corpus:mtls-runtime>", cfg: loaded}, loaded, productName, version, app.WithLogOutput(&logs))
	}()

	rootPool := x509.NewCertPool()
	rootPool.AddCert(serverCA.cert)
	baseURL := "https://" + loaded.Servers[0].Listen

	newClient := func(clientCert *tls.Certificate) *http.Client {
		tlsCfg := &tls.Config{RootCAs: rootPool}
		if clientCert != nil {
			tlsCfg.Certificates = []tls.Certificate{*clientCert}
		}
		return &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsCfg}}
	}

	// The readiness probe itself needs the trusted client certificate, since
	// mode=require refuses every handshake without one.
	trustedClient := newClient(&trustedClientCert)
	deadline := time.Now().Add(5 * time.Second)
	var readyErr error
	for time.Now().Before(deadline) {
		select {
		case code := <-done:
			done <- code
			t.Fatalf("Jul exited during startup with code %d\nlogs:\n%s", code, logs.String())
		default:
		}
		resp, err := trustedClient.Get(baseURL + "/")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			readyErr = nil
			break
		}
		readyErr = err
		time.Sleep(20 * time.Millisecond)
	}
	if readyErr != nil {
		t.Fatalf("Jul did not become reachable before the startup deadline: %v\nlogs:\n%s", readyErr, logs.String())
	}

	defer func() {
		cancel()
		select {
		case code := <-done:
			if code != 0 {
				t.Errorf("mtls-runtime: Jul exit code = %d\nlogs:\n%s", code, logs.String())
			}
		case <-time.After(5 * time.Second):
			t.Errorf("mtls-runtime: Jul did not shut down\nlogs:\n%s", logs.String())
		}
	}()

	// Positive: a client certificate signed by the configured CA is accepted.
	resp, err := trustedClient.Get(baseURL + "/")
	if err != nil {
		t.Fatalf("trusted client cert: request failed: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("trusted client cert: status = %d, want 200", resp.StatusCode)
	}

	// Negative: no client certificate at all must fail the handshake.
	if _, err := newClient(nil).Get(baseURL + "/"); err == nil {
		t.Fatal("no client certificate: expected the handshake to fail, request succeeded")
	}

	// Negative: a client certificate signed by a different CA must fail.
	if _, err := newClient(&untrustedClientCert).Get(baseURL + "/"); err == nil {
		t.Fatal("client certificate from an untrusted CA: expected the handshake to fail, request succeeded")
	}
}
