// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build !otel

package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/config"
)

// TestTracingNotCompiled verifies the stub build reports tracing as absent.
func TestTracingNotCompiled(t *testing.T) {
	if TracingCompiled {
		t.Fatal("TracingCompiled must be false without the otel build tag")
	}
}

// TestNewTracerDisabledStub verifies a disabled config yields an inert tracer
// whose middleware is a pass-through and whose Shutdown is a no-op.
func TestNewTracerDisabledStub(t *testing.T) {
	tr, err := NewTracer(config.TracingConfig{Enabled: false})
	if err != nil {
		t.Fatalf("NewTracer(disabled): %v", err)
	}
	called := false
	h := tr.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://h/", nil))
	if !called {
		t.Fatal("handler not called through stub middleware")
	}
	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want 418", rec.Code)
	}
	if err := tr.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

// TestNewTracerEnabledStubErrors verifies that enabling tracing in a binary
// built without the otel tag is a startup error, mirroring how an uncompiled
// compression encoder is rejected.
func TestNewTracerEnabledStubErrors(t *testing.T) {
	_, err := NewTracer(config.TracingConfig{Enabled: true, Exporter: "otlp-grpc", Endpoint: "localhost:4317"})
	if err == nil {
		t.Fatal("expected error enabling tracing without the otel build tag")
	}
}
