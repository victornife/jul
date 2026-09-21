// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package main

import (
	"compress/gzip"
	"context"
	"io"
	"net"
	"net/http"
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

// TestNGINXCorpusCacheRealE2E proves the bounded proxy_cache_path/proxy_cache
// -> Jul [cache] translation actually caches through a real Jul instance: the
// first request is a real MISS served by the backend, the immediate second
// request is a HIT served from the store without contacting the backend
// again, and the cached representation (including the backend's own
// X-Corpus-Backend-Id header, per the stored-headers contract in
// docs/cache.md) is byte-identical to what the origin returned.
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
}
