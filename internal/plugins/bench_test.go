//go:build wasmplugins

package plugins

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"jul/internal/config"
)

func benchManager(b *testing.B) *Manager {
	b.Helper()
	m, err := NewManager(Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		b.Fatalf("NewManager: %v", err)
	}
	b.Cleanup(func() { _ = m.Close() })
	return m
}

func benchSet(b *testing.B, m *Manager, cfg map[string]config.PluginConfig) *Set {
	b.Helper()
	s, err := m.Build(cfg)
	if err != nil {
		b.Fatalf("Build: %v", err)
	}
	b.Cleanup(func() { _ = s.Close() })
	return s
}

func benchNext() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "next")
	})
}

// BenchmarkNativeHandler is a baseline: a plain Go http.Handler with no plugin
// overhead. All plugin benchmarks are measured relative to this.
func BenchmarkNativeHandler(b *testing.B) {
	next := benchNext()
	req := httptest.NewRequest(http.MethodGet, "/api/hello", nil)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, req)
	}
}

// BenchmarkPluginMiddleware is the cost of a trivial middleware plugin that
// injects one response header and returns Continue. This measures the
// per-request ABI boundary crossing: instantiation is amortised by wazero's
// compilation cache, but each call still pays the guest invocation + import
// trampoline overhead.
func BenchmarkPluginMiddleware(b *testing.B) {
	m := benchManager(b)
	s := benchSet(b, m, map[string]config.PluginConfig{
		"hi": {
			Path:        testdataDir + "header-inject.wasm",
			Type:        "middleware",
			MemoryLimit: config.Size(8 << 20),
			Timeout:     config.Duration(5 * time.Second),
		},
	})

	next := benchNext()
	h := s.Middleware("hi")(next)
	req := httptest.NewRequest(http.MethodGet, "/api/hello", nil)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("status = %d, want 200", rec.Code)
		}
	}
}

// BenchmarkPluginHandler is the cost of a terminal handler plugin that sets a
// response status and body. This is the worst-case overhead because the guest
// must write the full response through the ABI.
func BenchmarkPluginHandler(b *testing.B) {
	m := benchManager(b)
	s := benchSet(b, m, map[string]config.PluginConfig{
		"rb": {
			Path:        testdataDir + "request-block.wasm",
			Type:        "handler",
			MemoryLimit: config.Size(8 << 20),
			Timeout:     config.Duration(5 * time.Second),
		},
	})

	h := s.Handler("rb")
	req := httptest.NewRequest(http.MethodGet, "/blocked", nil)
	req.Header.Set("X-Block", "1")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			b.Fatalf("status = %d, want 403", rec.Code)
		}
	}
}

// BenchmarkPluginKVCounterWithCapability measures the extra cost when a guest
// reads and writes KV state across the ABI boundary.
func BenchmarkPluginKVCounterWithCapability(b *testing.B) {
	m := benchManager(b)
	s := benchSet(b, m, map[string]config.PluginConfig{
		"kv": {
			Path:        testdataDir + "kv-counter.wasm",
			Type:        "middleware",
			MemoryLimit: config.Size(8 << 20),
			Timeout:     config.Duration(5 * time.Second),
			KV:          true,
		},
	})

	next := benchNext()
	h := s.Middleware("kv")(next)
	req := httptest.NewRequest(http.MethodGet, "/api/counter", nil)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("status = %d, want 200", rec.Code)
		}
	}
}

// BenchmarkPluginParallel measures contention under concurrent invocations.
// wazero serialises guest calls per instance, so the bench also exercises
// instance-pool behaviour under concurrency.
func BenchmarkPluginParallel(b *testing.B) {
	m := benchManager(b)
	s := benchSet(b, m, map[string]config.PluginConfig{
		"hi": {
			Path:        testdataDir + "header-inject.wasm",
			Type:        "middleware",
			MemoryLimit: config.Size(8 << 20),
			Timeout:     config.Duration(5 * time.Second),
		},
	})

	next := benchNext()
	h := s.Middleware("hi")(next)
	req := httptest.NewRequest(http.MethodGet, "/api/hello", nil)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				b.Fatalf("status = %d, want 200", rec.Code)
			}
		}
	})
}
