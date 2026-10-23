// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"testing"

	"jul/internal/config"
)

// BenchmarkProxyRoundTrip measures one request through the HTTP reverse proxy
// end to end: handler entry, backend selection, transport round trip and
// response copy, against a real backend over a real socket.
//
// It exists because the resilience programme needed a number for the path
// admission sits on and the module had none — the only proxy benchmarks were
// gRPC ones. It is the gate for ADR 0017's requirement that the *unlimited*
// path, where every resilience control is at its default, costs no more than 2%
// over the pre-admission proxy.
//
// Calibration matters when reading it: backend selection already allocates a
// candidate slice on every request, so admission's cost should be judged
// against that existing per-request work rather than against zero.
func BenchmarkProxyRoundTrip(b *testing.B) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "ok")
	}))
	defer backend.Close()

	ups := map[string]config.UpstreamConfig{
		"api": {
			Name:     "api",
			Strategy: "round_robin",
			Servers:  []config.UpstreamServer{{Address: backend.Listener.Addr().String(), Weight: 1}},
			MaxFails: 3,
		},
	}
	h, err := NewProxy(context.Background(), config.ServerConfig{},
		config.LocationConfig{
			Match:     config.MatchConfig{Type: "prefix", Path: "/"},
			ProxyPass: "http://api",
		}, ups, nil, nil, nil)
	if err != nil {
		b.Fatalf("NewProxy: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/bench", nil)
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

// BenchmarkProxyRoundTripParallel is the same round trip under contention,
// which is where an admission counter would show up if it were going to: the
// unlimited path takes no lock, so the two benchmarks should scale alike.
func BenchmarkProxyRoundTripParallel(b *testing.B) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer backend.Close()

	ups := map[string]config.UpstreamConfig{
		"api": {
			Name:     "api",
			Strategy: "round_robin",
			Servers:  []config.UpstreamServer{{Address: backend.Listener.Addr().String(), Weight: 1}},
			MaxFails: 3,
		},
	}
	h, err := NewProxy(context.Background(), config.ServerConfig{},
		config.LocationConfig{
			Match:     config.MatchConfig{Type: "prefix", Path: "/"},
			ProxyPass: "http://api",
		}, ups, nil, nil, nil)
	if err != nil {
		b.Fatalf("NewProxy: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, "/bench", nil)
		for pb.Next() {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
		}
	})
}

func BenchmarkCompiledProxyHeaders(b *testing.B) {
	compiled := compileProxyHeaders(map[string]string{
		"Host":              "$host",
		"X-Real-IP":         "$remote_addr",
		"X-Forwarded-For":   "$proxy_add_x_forwarded_for",
		"X-Forwarded-Proto": "$scheme",
		"X-Static-Token":    "bearer-static-token",
	})
	in := httptest.NewRequest(http.MethodGet, "http://edge.example/api/v1/users", nil)
	in.Host = "edge.example"
	out := in.Clone(in.Context())
	pr := &httputil.ProxyRequest{In: in, Out: out}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		vars := proxyVarContext{in: pr.In}
		setCanonicalXForwardedWithVars(pr, &vars)
		applyCompiledProxyHeaders(pr, compiled, &vars)
	}
}

func BenchmarkExpandProxyVar(b *testing.B) {
	req := httptest.NewRequest(http.MethodGet, "https://edge.example/api/v1/users", nil)
	req.Host = "edge.example"
	cases := []struct {
		name string
		tmpl string
	}{
		{"static", "bearer-static-token"},
		{"single_host", "$host"},
		{"single_remote_addr", "$remote_addr"},
		{"composite", "host=$host;ip=$remote_addr;proto=$scheme"},
	}
	for _, c := range cases {
		compiled := compileProxyTemplate(c.tmpl)
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				vars := proxyVarContext{in: req}
				_ = compiled.eval(&vars)
			}
		})
	}
}
