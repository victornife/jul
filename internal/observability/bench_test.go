// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build otel

package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"jul/internal/tracing"
)

// benchHandler is a minimal handler for benchmark baseline.
var benchHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
})

// newBenchTracer builds a synchronous tracer so spans flush immediately,
// representing the highest-overhead path (no exporter batching).
func newBenchTracer() *Tracer {
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(nil))
	prop := propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})
	return &Tracer{
		provider:   tp,
		tracer:     tp.Tracer("bench"),
		propagator: prop,
		enabled:    true,
	}
}

func BenchmarkTracingMiddleware(b *testing.B) {
	tr := newBenchTracer()
	h := tr.Middleware(benchHandler)
	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/users", nil)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}

func BenchmarkTracingBaseline(b *testing.B) {
	h := benchHandler
	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/users", nil)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}

func BenchmarkTracingSeamChildSpan(b *testing.B) {
	tr := newBenchTracer()
	adapter := otelTracer{tracer: tr.tracer, propagator: tr.propagator}
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, span := adapter.Start(ctx, "proxy.roundtrip")
		span.SetString("upstream.backend", "10.0.0.1:80")
		span.SetStatus(http.StatusOK)
		span.End()
	}
}

func BenchmarkTracingSeamBaseline(b *testing.B) {
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		ctx, span := tracing.Active().Start(context.Background(), "proxy.roundtrip")
		span.SetString("upstream.backend", "10.0.0.1:80")
		span.SetStatus(http.StatusOK)
		span.End()
		_ = ctx
	}
}

func BenchmarkTracingW3CExtract(b *testing.B) {
	tr := newBenchTracer()
	h := tr.Middleware(benchHandler)
	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/users", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}
