// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build otel

package observability

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
// available immediately after the handler returns. The real ParentBased +
// dynamic-root sampler shape is retained so #99 tests exercise the production
// sampling architecture rather than a test-only provider.
func newTestTracer() (*Tracer, *tracetest.InMemoryExporter) {
	exp := tracetest.NewInMemoryExporter()
	rootSampler := newDynamicRootSampler(1)
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exp),
		sdktrace.WithSampler(sdktrace.ParentBased(rootSampler)),
	)
	prop := propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})
	return &Tracer{
		provider:    tp,
		tracer:      tp.Tracer("test"),
		propagator:  prop,
		rootSampler: rootSampler,
		enabled:     true,
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

func samplingParams(id trace.TraceID, parent context.Context) sdktrace.SamplingParameters {
	return sdktrace.SamplingParameters{
		ParentContext: parent,
		TraceID:       id,
		Name:          "root",
		Kind:          trace.SpanKindInternal,
	}
}

func spanContext(sampled, remote bool) trace.SpanContext {
	flags := trace.TraceFlags(0)
	if sampled {
		flags = trace.FlagsSampled
	}
	return trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SpanID:     trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: flags,
		Remote:     remote,
	})
}

// TestTracingCompiledTrue verifies the otel build reports tracing as present.
func TestTracingCompiledTrue(t *testing.T) {
	if !TracingCompiled {
		t.Fatal("TracingCompiled must be true under the otel build tag")
	}
}

// TestDynamicRootSamplerMatchesSDK proves Jul delegates exact ratio decisions to
// the current OpenTelemetry SDK rather than reproducing its TraceID math.
func TestDynamicRootSamplerMatchesSDK(t *testing.T) {
	t.Parallel()
	ids := []trace.TraceID{
		{0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1},
		{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		{255, 254, 253, 252, 251, 250, 249, 248, 247, 246, 245, 244, 243, 242, 241, 240},
	}
	for _, ratio := range []float64{0, 0.000001, 0.1, 0.5, 0.9, 0.999999, 1} {
		dynamic := newDynamicRootSampler(ratio)
		wantSampler := sdktrace.TraceIDRatioBased(ratio)
		for _, id := range ids {
			p := samplingParams(id, context.Background())
			got := dynamic.ShouldSample(p).Decision
			want := wantSampler.ShouldSample(p).Decision
			if got != want {
				t.Fatalf("ratio=%v trace=%s decision=%v, want SDK %v", ratio, id, got, want)
			}
		}
	}
}

// TestDynamicRootSamplerUpdateCutover proves the two deterministic boundary
// transitions and an intermediate transition without statistical assertions.
func TestDynamicRootSamplerUpdateCutover(t *testing.T) {
	t.Parallel()
	id := trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	p := samplingParams(id, context.Background())
	s := newDynamicRootSampler(0)
	if got := s.ShouldSample(p).Decision; got != sdktrace.Drop {
		t.Fatalf("ratio 0 decision=%v, want Drop", got)
	}
	s.Update(1)
	if got := s.ShouldSample(p).Decision; got != sdktrace.RecordAndSample {
		t.Fatalf("ratio 1 decision=%v, want RecordAndSample", got)
	}
	s.Update(0.37)
	got := s.ShouldSample(p).Decision
	want := sdktrace.TraceIDRatioBased(0.37).ShouldSample(p).Decision
	if got != want {
		t.Fatalf("ratio .37 decision=%v, want SDK %v", got, want)
	}
	s.Update(0)
	if got := s.ShouldSample(p).Decision; got != sdktrace.Drop {
		t.Fatalf("ratio 0 after update decision=%v, want Drop", got)
	}
}

// TestParentBasedKeepsLocalAndRemoteParentAuthority proves a root-ratio change
// cannot override any of the four required parent cases.
func TestParentBasedKeepsLocalAndRemoteParentAuthority(t *testing.T) {
	t.Parallel()
	root := newDynamicRootSampler(0)
	parentBased := sdktrace.ParentBased(root)
	id := trace.TraceID{9, 8, 7, 6, 5, 4, 3, 2, 1, 2, 3, 4, 5, 6, 7, 8}

	cases := []struct {
		name    string
		sampled bool
		remote  bool
		want    sdktrace.SamplingDecision
	}{
		{"local sampled", true, false, sdktrace.RecordAndSample},
		{"local unsampled", false, false, sdktrace.Drop},
		{"remote sampled", true, true, sdktrace.RecordAndSample},
		{"remote unsampled", false, true, sdktrace.Drop},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parent := spanContext(tc.sampled, tc.remote)
			var ctx context.Context
			if tc.remote {
				ctx = trace.ContextWithRemoteSpanContext(context.Background(), parent)
			} else {
				ctx = trace.ContextWithSpanContext(context.Background(), parent)
			}
			if got := parentBased.ShouldSample(samplingParams(id, ctx)).Decision; got != tc.want {
				t.Fatalf("before root update decision=%v, want %v", got, tc.want)
			}
			root.Update(1)
			if got := parentBased.ShouldSample(samplingParams(id, ctx)).Decision; got != tc.want {
				t.Fatalf("after root update decision=%v, want %v", got, tc.want)
			}
			root.Update(0)
		})
	}
}

// TestDynamicRootSamplerConcurrentUpdate proves concurrent readers only observe
// complete immutable SDK samplers while the ratio pointer is repeatedly swapped.
func TestDynamicRootSamplerConcurrentUpdate(t *testing.T) {
	s := newDynamicRootSampler(0)
	var wg sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		wg.Add(1)
		go func(seed byte) {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				id := trace.TraceID{seed, byte(i), byte(i >> 8), 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
				decision := s.ShouldSample(samplingParams(id, context.Background())).Decision
				if decision != sdktrace.Drop && decision != sdktrace.RecordAndSample {
					t.Errorf("unexpected root decision %v", decision)
					return
				}
			}
		}(byte(worker + 1))
	}
	for i := 0; i < 2000; i++ {
		s.Update([]float64{0, 1, 0.1, 0.75}[i%4])
	}
	wg.Wait()
}

// TestTracerRatioUpdateKeepsProviderAndExistingTrace proves a ratio update does
// not replace the provider and cannot change an already sampled trace. A new
// root after the cutover follows the new ratio.
func TestTracerRatioUpdateKeepsProviderAndExistingTrace(t *testing.T) {
	tr, exp := newTestTracer()
	provider := tr.provider
	ctx, root := tr.tracer.Start(context.Background(), "existing-root")
	if !root.SpanContext().IsSampled() {
		t.Fatal("existing root must start sampled at ratio 1")
	}

	tr.UpdateSampleRatio(0)
	if tr.provider != provider {
		t.Fatal("provider identity changed during ratio update")
	}
	_, child := tr.tracer.Start(ctx, "existing-child")
	if !child.SpanContext().IsSampled() {
		t.Fatal("child of sampled existing trace became unsampled after ratio update")
	}
	child.End()
	root.End()

	_, newRoot := tr.tracer.Start(context.Background(), "new-root")
	if newRoot.SpanContext().IsSampled() {
		t.Fatal("new root after ratio=0 cutover is still sampled")
	}
	newRoot.End()

	spans := exp.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("exported spans=%d, want existing root+child only", len(spans))
	}
}

// TestProcessSampleRatioPublishSeam proves the process-level Publish function
// updates the already-installed sampler pointer rather than replacing it.
func TestProcessSampleRatioPublishSeam(t *testing.T) {
	old := activeRootSampler.Load()
	defer activeRootSampler.Store(old)

	s := newDynamicRootSampler(0)
	activeRootSampler.Store(s)
	id := trace.TraceID{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}
	p := samplingParams(id, context.Background())
	if got := s.ShouldSample(p).Decision; got != sdktrace.Drop {
		t.Fatalf("before publish decision=%v, want Drop", got)
	}
	UpdateTracingSampleRatio(1)
	if activeRootSampler.Load() != s {
		t.Fatal("Publish seam replaced process root sampler identity")
	}
	if got := s.ShouldSample(p).Decision; got != sdktrace.RecordAndSample {
		t.Fatalf("after publish decision=%v, want RecordAndSample", got)
	}
}

// TestLocalOTLPHTTPPipelineSurvivesRatioUpdates is the real local collector
// integration for #99. The same provider/export pipeline exports before and
// after an in-place ratio change, while ratio=0 suppresses a new root.
func TestLocalOTLPHTTPPipelineSurvivesRatioUpdates(t *testing.T) {
	var requests atomic.Int64
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/traces" {
			t.Errorf("collector path=%q, want /v1/traces", r.URL.Path)
		}
		requests.Add(1)
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	oldActive := activeRootSampler.Load()
	defer activeRootSampler.Store(oldActive)

	tr, err := NewTracer(config.TracingConfig{
		Enabled:     true,
		Exporter:    "otlp-http",
		Endpoint:    strings.TrimPrefix(receiver.URL, "http://"),
		ServiceName: "jul-test",
		SampleRatio: 1,
		Insecure:    true,
	})
	if err != nil {
		t.Fatalf("NewTracer: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := tr.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	}()
	provider := tr.provider

	_, before := tr.tracer.Start(context.Background(), "before")
	before.End()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	if err := tr.provider.ForceFlush(ctx); err != nil {
		cancel()
		t.Fatalf("ForceFlush before update: %v", err)
	}
	cancel()
	beforeRequests := requests.Load()
	if beforeRequests == 0 {
		t.Fatal("local collector received no trace before update")
	}

	UpdateTracingSampleRatio(0)
	_, dropped := tr.tracer.Start(context.Background(), "dropped")
	if dropped.SpanContext().IsSampled() {
		t.Fatal("ratio=0 root unexpectedly sampled")
	}
	dropped.End()
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	if err := tr.provider.ForceFlush(ctx); err != nil {
		cancel()
		t.Fatalf("ForceFlush at ratio 0: %v", err)
	}
	cancel()
	if got := requests.Load(); got != beforeRequests {
		t.Fatalf("collector requests changed at ratio 0: got %d want %d", got, beforeRequests)
	}

	UpdateTracingSampleRatio(1)
	_, after := tr.tracer.Start(context.Background(), "after")
	if !after.SpanContext().IsSampled() {
		t.Fatal("ratio=1 root unexpectedly unsampled")
	}
	after.End()
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	if err := tr.provider.ForceFlush(ctx); err != nil {
		cancel()
		t.Fatalf("ForceFlush after update: %v", err)
	}
	cancel()
	if requests.Load() <= beforeRequests {
		t.Fatalf("collector did not receive trace after ratio restored: requests=%d before=%d", requests.Load(), beforeRequests)
	}
	if tr.provider != provider {
		t.Fatal("provider identity changed across local collector ratio updates")
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
	tr.UpdateSampleRatio(0.75)
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
