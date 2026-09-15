// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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
	// Reuse the test fixture helper through a tiny testing.TB adapter is not
	// possible because it needs Cleanup semantics tied to the benchmark. Keep
	// the setup local and portable instead.
	path := benchmarkUnixPath(b)
	ln, err := netListenUnix(path)
	if err != nil {
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
	})
	return path
}
