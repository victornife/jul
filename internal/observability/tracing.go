// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build otel

// Package observability — OpenTelemetry tracing.
//
// This file is compiled only with the `otel` build tag. It provides a server
// span around every request, W3C tracecontext propagation, and an OTLP
// exporter (gRPC or HTTP). It is the spike seam for full distributed tracing:
// later releases add child spans in the proxy, cache, and upstream layers and
// honor reloads. The global TracerProvider/propagator are set here so those
// child spans join the same trace.
package observability

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"

	"jul/internal/config"
	"jul/internal/middleware"
	"jul/internal/tracing"
)

// TracingCompiled reports whether OpenTelemetry tracing is built into this
// binary. It is true only under the `otel` build tag.
const TracingCompiled = true

// Tracer owns the OpenTelemetry pipeline for the process. It is constructed
// once at startup and shut down on graceful exit to flush pending spans.
type Tracer struct {
	provider   *sdktrace.TracerProvider
	tracer     trace.Tracer
	propagator propagation.TextMapPropagator
	enabled    bool
}

// NewTracer builds the tracing pipeline from cfg. When tracing is disabled it
// returns an inert Tracer whose Middleware is a pass-through and whose Shutdown
// is a no-op, so callers need not branch on the enabled flag.
func NewTracer(cfg config.TracingConfig) (*Tracer, error) {
	if !cfg.Enabled {
		return &Tracer{}, nil
	}

	exp, err := newExporter(context.Background(), cfg)
	if err != nil {
		return nil, fmt.Errorf("[observability.tracing] create exporter: %w", err)
	}

	// Build a resource carrying the SDK defaults (telemetry.sdk.*, process, os)
	// plus our service name. The semconv version here matches the one used by
	// resource.Default so the schema URLs agree and the merge cannot conflict.
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName(cfg.ServiceName)),
	)
	if err != nil {
		return nil, fmt.Errorf("[observability.tracing] build resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
	)
	prop := propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})

	// Publish globally so child spans created elsewhere (proxy, cache, upstream)
	// join the same trace, and wire the dependency-free tracing seam so those
	// layers emit spans without importing OpenTelemetry.
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(prop)
	tr := tp.Tracer("jul/internal/observability")
	tracing.Set(otelTracer{tracer: tr, propagator: prop})

	return &Tracer{
		provider:   tp,
		tracer:     tr,
		propagator: prop,
		enabled:    true,
	}, nil
}

// newExporter builds the OTLP span exporter for the configured transport. The
// connection is non-blocking. It uses TLS with the host's root CAs by default;
// set insecure = true in config for a plaintext local collector.
func newExporter(ctx context.Context, cfg config.TracingConfig) (sdktrace.SpanExporter, error) {
	switch cfg.Exporter {
	case "otlp-http":
		opts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(cfg.Endpoint)}
		if cfg.Insecure {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
		return otlptracehttp.New(ctx, opts...)
	default: // otlp-grpc
		opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(cfg.Endpoint)}
		if cfg.Insecure {
			opts = append(opts, otlptracegrpc.WithInsecure())
		}
		return otlptracegrpc.New(ctx, opts...)
	}
}

// Middleware wraps next in a server span. Its signature matches
// middleware.Middleware so it is passed as a method value into the chain, like
// metrics.Middleware. Incoming W3C tracecontext is extracted so the span joins
// an upstream trace; the resulting trace id is also placed in the request
// context as a plain string for the access log. When tracing is disabled the
// request handler is returned unchanged.
func (t *Tracer) Middleware(next http.Handler) http.Handler {
	if t == nil || !t.enabled {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := t.propagator.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := t.tracer.Start(ctx, r.Method+" "+r.URL.Path,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				semconv.HTTPRequestMethodKey.String(r.Method),
				semconv.URLPath(r.URL.Path),
				semconv.URLScheme(requestScheme(r)),
				semconv.ServerAddress(r.Host),
				semconv.UserAgentOriginal(r.UserAgent()),
				semconv.NetworkProtocolVersion(strings.TrimPrefix(r.Proto, "HTTP/")),
			),
		)
		defer span.End()

		if sc := span.SpanContext(); sc.HasTraceID() {
			ctx = middleware.WithTraceID(ctx, sc.TraceID().String())
		}

		// Reuse the shared status-capturing recorder so the span observes the
		// final status while inner layers still see exactly the optional
		// interfaces the real connection offers.
		rec := middleware.NewRecorder(w)
		next.ServeHTTP(rec.Writer(), r.WithContext(ctx))

		status := rec.Status()
		span.SetAttributes(semconv.HTTPResponseStatusCode(status))
		if status >= 500 {
			span.SetStatus(codes.Error, http.StatusText(status))
		}
	})
}

// Shutdown flushes and stops the exporter. It is safe to call on an inert
// Tracer (tracing disabled) and after a failed construction.
func (t *Tracer) Shutdown(ctx context.Context) error {
	if t == nil || t.provider == nil {
		return nil
	}
	return t.provider.Shutdown(ctx)
}

// requestScheme reports the URL scheme of the request for span attributes.
func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// otelTracer adapts the OpenTelemetry tracer to the dependency-free tracing
// seam (jul/internal/tracing) so the proxy, cache, and upstream layers emit
// child spans that join the server trace without importing OpenTelemetry.
type otelTracer struct {
	tracer     trace.Tracer
	propagator propagation.TextMapPropagator
}

func (o otelTracer) Start(ctx context.Context, name string) (context.Context, tracing.Span) {
	ctx, span := o.tracer.Start(ctx, name)
	return ctx, otelSpan{span: span}
}

func (o otelTracer) Inject(ctx context.Context, h http.Header) {
	o.propagator.Inject(ctx, propagation.HeaderCarrier(h))
}

// otelSpan adapts an OpenTelemetry span to the tracing.Span seam.
type otelSpan struct{ span trace.Span }

func (s otelSpan) End() { s.span.End() }

func (s otelSpan) RecordError(err error) {
	if err == nil {
		return
	}
	s.span.RecordError(err)
	s.span.SetStatus(codes.Error, err.Error())
}

func (s otelSpan) SetStatus(code int) {
	if code == 0 {
		return
	}
	s.span.SetAttributes(semconv.HTTPResponseStatusCode(code))
	if code >= 500 {
		s.span.SetStatus(codes.Error, http.StatusText(code))
	}
}

func (s otelSpan) SetString(key, value string) {
	s.span.SetAttributes(attribute.String(key, value))
}
