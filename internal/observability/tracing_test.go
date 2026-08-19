// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build otel

package observability

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"jul/internal/config"
	"jul/internal/middleware"
)

// newTestTracer builds a Tracer backed by an in-memory exporter so spans can be
// asserted without a collector. WithSyncer exports synchronously, so spans are
// available immediately after the handler returns.
func newTestTracer() (*Tracer, *tracetest.InMemoryExporter) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	prop := propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})
	return &Tracer{
		provider:   tp,
		tracer:     tp.Tracer("test"),
		propagator: prop,
		enabled:    true,
	}, exp
}

func findAttr(attrs []attribute.KeyValue, key string) (attribute.Value, bool) {
	for _, a := range attrs {
		if string(a.Key) == key {
			return a.Value, true
		}
	}
	return attribute.Value{}, false
}

// TestTracingCompiledTrue verifies the otel build reports tracing as present.
func TestTracingCompiledTrue(t *testing.T) {
	if !TracingCompiled {
		t.Fatal("TracingCompiled must be true under the otel build tag")
	}
}

// TestMiddlewareExportsServerSpan verifies one server span is exported per
// request with the method+path name, server span kind, and the response status
// recorded as an attribute.
func TestMiddlewareExportsServerSpan(t *testing.T) {
	tr, exp := newTestTracer()
	h := tr.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://h/api/x", nil))

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	s := spans[0]
	if s.Name != "GET /api/x" {
		t.Errorf("span name = %q, want \"GET /api/x\"", s.Name)
	}
	if s.SpanKind != trace.SpanKindServer {
		t.Errorf("span kind = %v, want server", s.SpanKind)
	}
	if v, ok := findAttr(s.Attributes, "http.response.status_code"); !ok || v.AsInt64() != 200 {
		t.Errorf("status attribute = %v (present=%v), want 200", v.AsInt64(), ok)
	}
}

// TestMiddlewareW3CPropagation verifies incoming W3C tracecontext is extracted
// so the server span joins the upstream trace rather than starting a new one.
func TestMiddlewareW3CPropagation(t *testing.T) {
	tr, exp := newTestTracer()
	h := tr.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	req := httptest.NewRequest(http.MethodGet, "http://h/", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	h.ServeHTTP(httptest.NewRecorder(), req)

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	s := spans[0]
	if got := s.SpanContext.TraceID().String(); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("trace id = %q, want propagated parent trace id", got)
	}
	if got := s.Parent.SpanID().String(); got != "00f067aa0ba902b7" {
		t.Errorf("parent span id = %q, want 00f067aa0ba902b7", got)
	}
}

// TestMiddlewareSetsTraceIDInContext verifies the middleware bridges the active
// span's trace id into the request context as a plain string for the access log.
func TestMiddlewareSetsTraceIDInContext(t *testing.T) {
	tr, exp := newTestTracer()
	var seen string
	h := tr.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = middleware.TraceIDFrom(r.Context())
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://h/", nil))

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	if seen == "" {
		t.Fatal("trace id not placed in request context")
	}
	if want := spans[0].SpanContext.TraceID().String(); seen != want {
		t.Errorf("context trace id = %q, want span trace id %q", seen, want)
	}
}

// TestMiddleware5xxSetsErrorStatus verifies a 5xx response marks the span as an
// error so failed requests stand out in trace backends.
func TestMiddleware5xxSetsErrorStatus(t *testing.T) {
	tr, exp := newTestTracer()
	h := tr.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://h/", nil))

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	if spans[0].Status.Code != codes.Error {
		t.Errorf("span status = %v, want Error for 502", spans[0].Status.Code)
	}
	if v, ok := findAttr(spans[0].Attributes, "http.response.status_code"); !ok || v.AsInt64() != 502 {
		t.Errorf("status attribute = %v, want 502", v.AsInt64())
	}
}

// TestNewTracerDisabledIsNoop verifies a disabled config produces a tracer whose
// middleware is a pass-through and whose Shutdown is a no-op, even in the otel
// build, so callers never branch on the enabled flag.
func TestNewTracerDisabledIsNoop(t *testing.T) {
	tr, err := NewTracer(config.TracingConfig{Enabled: false})
	if err != nil {
		t.Fatalf("NewTracer(disabled): %v", err)
	}
	called := false
	h := tr.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://h/", nil))
	if !called {
		t.Fatal("handler not called through disabled tracer")
	}
	if err := tr.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

// TestSeamChildSpanExported verifies the otelTracer adapter (used by the proxy,
// cache, and upstream layers through the tracing seam) exports a child span
// nested under the active parent with its attributes and status recorded.
func TestSeamChildSpanExported(t *testing.T) {
	tr, exp := newTestTracer()
	adapter := otelTracer{tracer: tr.tracer, propagator: tr.propagator}

	parentCtx, parent := tr.tracer.Start(context.Background(), "parent")
	_, span := adapter.Start(parentCtx, "proxy.roundtrip")
	span.SetString("upstream.backend", "10.0.0.1:80")
	span.SetInt("retry.attempt", 2)
	span.SetStatus(http.StatusOK)
	span.End()
	parent.End()

	spans := exp.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want 2 (parent+child)", len(spans))
	}
	var child, root tracetest.SpanStub
	for _, s := range spans {
		switch s.Name {
		case "proxy.roundtrip":
			child = s
		case "parent":
			root = s
		}
	}
	if child.Name == "" {
		t.Fatal("child span was not exported")
	}
	if child.Parent.SpanID() != root.SpanContext.SpanID() {
		t.Errorf("child parent = %v, want root span id %v", child.Parent.SpanID(), root.SpanContext.SpanID())
	}
	if v, ok := findAttr(child.Attributes, "upstream.backend"); !ok || v.AsString() != "10.0.0.1:80" {
		t.Errorf("backend attribute = %q (present=%v), want 10.0.0.1:80", v.AsString(), ok)
	}
	if v, ok := findAttr(child.Attributes, "http.response.status_code"); !ok || v.AsInt64() != 200 {
		t.Errorf("status attribute = %d (present=%v), want 200", v.AsInt64(), ok)
	}
	// SetInt must land as a real int64 attribute, not a stringified one: the
	// retry attributes are meant to be aggregated on in a trace backend.
	if v, ok := findAttr(child.Attributes, "retry.attempt"); !ok || v.AsInt64() != 2 {
		t.Errorf("retry.attempt = %d (present=%v), want 2", v.AsInt64(), ok)
	}
}

// TestSeamInjectWritesTraceparent verifies Inject writes W3C tracecontext into
// outbound headers so a proxied upstream continues the trace.
func TestSeamInjectWritesTraceparent(t *testing.T) {
	tr, _ := newTestTracer()
	adapter := otelTracer{tracer: tr.tracer, propagator: tr.propagator}
	ctx, span := adapter.Start(context.Background(), "upstream.request")
	defer span.End()

	h := http.Header{}
	adapter.Inject(ctx, h)
	if h.Get("Traceparent") == "" {
		t.Errorf("Inject did not write a traceparent header: %v", h)
	}
}

// TestSeamRecordErrorMarksSpan verifies RecordError marks the child span as an
// error and attaches an exception event so failed upstream attempts stand out.
func TestSeamRecordErrorMarksSpan(t *testing.T) {
	tr, exp := newTestTracer()
	adapter := otelTracer{tracer: tr.tracer, propagator: tr.propagator}
	_, span := adapter.Start(context.Background(), "upstream.request")
	span.RecordError(errors.New("dial tcp: connection refused"))
	span.End()

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	if spans[0].Status.Code != codes.Error {
		t.Errorf("status = %v, want Error", spans[0].Status.Code)
	}
	if len(spans[0].Events) == 0 {
		t.Error("RecordError did not add an exception event")
	}
}
