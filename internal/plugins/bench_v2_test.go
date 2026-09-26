// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

// The jul-abi/v2 benchmarks measure what enabling the response phase costs
// relative to BenchmarkPluginMiddleware (v1, one request-phase call). Each
// reports guest calls/op; "guest-calls" counts both phases.

func benchV2(b *testing.B, module string, pc config.PluginConfig, origin http.Handler, hdr ...string) {
	b.Helper()
	var calls atomic.Int64
	m, err := NewManager(Options{
		Logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnInvocation:         func(string, string, time.Duration) { calls.Add(1) },
		OnResponseInvocation: func(string, string, time.Duration) { calls.Add(1) },
	})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = m.Close() })
	pc.Path = fixturePath(module)
	pc.MemoryLimit = config.Size(32 << 20)
	pc.Timeout = config.Duration(5 * time.Second)
	s := benchSet(b, m, map[string]config.PluginConfig{"p": pc})
	h := chainFor(s, origin, "p")
	r := httptest.NewRequest(http.MethodGet, "/api/hello", nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusOK {
			b.Fatalf("status = %d", rec.Code)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(calls.Load())/float64(b.N), "guest-calls/op")
}

func jsonOrigin(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	})
}

// BenchmarkV1RequestOnly is the v1 baseline through the same harness.
func BenchmarkV1RequestOnly(b *testing.B) {
	benchV2(b, "v1-current-header-inject", config.PluginConfig{}, benchNext())
}

// BenchmarkV2RequestOnly: a v2 guest that never subscribes pays v1's cost.
func BenchmarkV2RequestOnly(b *testing.B) {
	benchV2(b, "testguest-v2", config.PluginConfig{ABI: ABIJulV2}, benchNext(), "X-Op", "req-only")
}

// BenchmarkV2ResponseMetadata: status-class header + header removal.
func BenchmarkV2ResponseMetadata(b *testing.B) {
	benchV2(b, "v2-status-header", config.PluginConfig{ABI: ABIJulV2}, benchNext())
}

// BenchmarkV2BodyInspect: a 1 KiB JSON body read and scanned, not replaced.
func BenchmarkV2BodyInspect(b *testing.B) {
	benchV2(b, "v2-redact", config.PluginConfig{ABI: ABIJulV2, Config: map[string]string{"secrets": "nope"}},
		jsonOrigin(`{"items":"`+string(make([]byte, 1000))+`"}`))
}

// BenchmarkV2BodyReplace: a 1 KiB JSON body with a secret redacted.
func BenchmarkV2BodyReplace(b *testing.B) {
	body := `{"token":"hunter2","pad":"` + string(make([]byte, 1000)) + `"}`
	benchV2(b, "v2-redact", config.PluginConfig{ABI: ABIJulV2, Config: map[string]string{"secrets": "hunter2"}}, jsonOrigin(body))
}
