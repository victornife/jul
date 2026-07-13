// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"jul/internal/config"
)

// smokeTransport disables keep-alives so no pooled connection goroutine
// outlives the test (mirrors the keep-alive-free client in reload_test.go).
var smokeTransport = &http.Transport{DisableKeepAlives: true}

// freeLocalPort returns a 127.0.0.1:port string for a port that is currently
// free on the loopback interface. There is a small TOCTOU window between Close
// and the server's Bind, which is acceptable for tests.
func freeLocalPort(t *testing.T) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// smokeWaitReady polls url until the server responds (any 1xx–4xx status
// is treated as "up"; 5xx and connection errors continue polling) or the
// 5-second deadline elapses.
func smokeWaitReady(t *testing.T, url string) {
	t.Helper()
	client := &http.Client{Transport: smokeTransport, Timeout: 300 * time.Millisecond}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode < 500 {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("server at %s did not become ready within 5s", url)
}

func smokeGet(t *testing.T, url string) (statusCode int, body string) {
	t.Helper()
	client := &http.Client{Transport: smokeTransport, Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestCmdRunServeSmoke exercises the boot → serve → shutdown lifecycle for
// `jul run --serve <dir>`. It starts the full composition root (serve) with a
// synthesised zero-config that serves a static directory, asserts that a file
// is reachable over HTTP, then cancels the context and checks that serve
// returns exit code 0.
//
// The static handler keeps an os.Root directory handle open for its lifetime,
// so a manual temp dir with best-effort cleanup is used instead of t.TempDir
// to avoid a still-open-handle removal failure on Windows.
func TestCmdRunServeSmoke(t *testing.T) {
	dir, err := os.MkdirTemp("", "run-serve-smoke")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	if err := os.WriteFile(filepath.Join(dir, "smoke.html"), []byte("smoke-serve-ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	addr := freeLocalPort(t)
	cfg := config.ServeDir(dir, addr)
	src := memorySource{name: "<test:run-serve-smoke>", cfg: cfg}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	reload := make(chan struct{})
	result := make(chan int, 1)
	go func() { result <- serve(ctx, reload, src, cfg) }()

	smokeWaitReady(t, "http://"+addr+"/smoke.html")

	code, body := smokeGet(t, "http://"+addr+"/smoke.html")
	if code != http.StatusOK {
		t.Fatalf("GET /smoke.html: status %d, want 200", code)
	}
	if body != "smoke-serve-ok" {
		t.Errorf("body = %q, want %q", body, "smoke-serve-ok")
	}

	// Trigger shutdown and verify a clean (0) exit.
	cancel()
	select {
	case exitCode := <-result:
		if exitCode != 0 {
			t.Errorf("serve exit code = %d, want 0", exitCode)
		}
	case <-time.After(10 * time.Second):
		t.Error("serve did not exit within 10s after context cancellation")
	}
}

// TestCmdRunProxySmoke exercises the boot → serve → shutdown lifecycle for
// `jul run --proxy <target>`. It starts an in-process backend, starts the
// full composition root with a synthesised proxy config, verifies that a
// request is forwarded to the backend, then cancels and checks for a clean
// (0) exit.
func TestCmdRunProxySmoke(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "smoke-proxied-ok")
	}))
	t.Cleanup(backend.Close)

	addr := freeLocalPort(t)
	cfg := config.ProxyTarget(backend.URL, addr)
	src := memorySource{name: "<test:run-proxy-smoke>", cfg: cfg}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	reload := make(chan struct{})
	result := make(chan int, 1)
	go func() { result <- serve(ctx, reload, src, cfg) }()

	smokeWaitReady(t, "http://"+addr+"/")

	code, body := smokeGet(t, "http://"+addr+"/")
	if code != http.StatusOK {
		t.Fatalf("GET /: status %d, want 200", code)
	}
	if body != "smoke-proxied-ok" {
		t.Errorf("body = %q, want %q", body, "smoke-proxied-ok")
	}

	// Trigger shutdown and verify a clean (0) exit.
	cancel()
	select {
	case exitCode := <-result:
		if exitCode != 0 {
			t.Errorf("serve exit code = %d, want 0", exitCode)
		}
	case <-time.After(10 * time.Second):
		t.Error("serve did not exit within 10s after context cancellation")
	}
}
