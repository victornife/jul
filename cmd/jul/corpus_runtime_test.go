// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package main

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
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
