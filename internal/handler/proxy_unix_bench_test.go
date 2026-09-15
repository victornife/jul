// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"jul/internal/config"
)

// BenchmarkProxyUnixRoundTrip measures the same handler/backend-selection/
// transport/response-copy path as BenchmarkProxyRoundTrip, with the selected
// backend reached through a real AF_UNIX socket. It is evidence, not a promise
// that Unix and TCP have identical kernel costs.
func BenchmarkProxyUnixRoundTrip(b *testing.B) {
	path := startUnixHTTPBackendForBenchmark(b, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	ups := map[string]config.UpstreamConfig{
		"api": {
			Name:     "api",
			Strategy: "round_robin",
			Servers:  []config.UpstreamServer{{Address: "unix:" + path, Weight: 1}},
		},
	}
	h, err := NewProxy(context.Background(), config.ServerConfig{}, config.LocationConfig{ProxyPass: "http://api"}, ups, nil, nil, nil)
	if err != nil {
		b.Fatalf("NewProxy: %v", err)
	}
	ph := h.(*proxyHandler)
	defer ph.Close()

	req := httptest.NewRequest(http.MethodGet, "http://edge/bench", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("status = %d", rec.Code)
		}
	}
}

func startUnixHTTPBackendForBenchmark(b *testing.B, h http.Handler) string {
	b.Helper()
	base := os.TempDir()
	if runtime.GOOS == "darwin" {
		base = "/tmp"
	}
	dir, err := os.MkdirTemp(base, "jul407-bench-")
	if err != nil {
		b.Fatalf("MkdirTemp: %v", err)
	}
	path := filepath.Join(dir, "backend.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		_ = os.RemoveAll(dir)
		b.Fatalf("listen unix: %v", err)
	}
	srv := &http.Server{Handler: h}
	done := make(chan struct{})
	go func() {
		_ = srv.Serve(ln)
		close(done)
	}()
	b.Cleanup(func() {
		_ = srv.Close()
		_ = ln.Close()
		<-done
		_ = os.RemoveAll(dir)
	})
	return path
}
