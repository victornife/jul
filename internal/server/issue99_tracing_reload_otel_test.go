// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build otel

package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"

	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/observability"
	"jul/internal/redact"
	tracingseam "jul/internal/tracing"
)

type issue99TOMLSource struct {
	mu  sync.Mutex
	raw []byte
}

func (s *issue99TOMLSource) set(raw []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.raw = append([]byte(nil), raw...)
}

func (s *issue99TOMLSource) Load() (*config.Config, error) {
	s.mu.Lock()
	raw := append([]byte(nil), s.raw...)
	s.mu.Unlock()
	return config.Parse(raw)
}

func (s *issue99TOMLSource) ReadRaw() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.raw...), nil
}

func (s *issue99TOMLSource) Name() string { return "issue99-toml" }

func issue99TracingTOML(addr, endpoint, ratio string) []byte {
	return []byte(fmt.Sprintf(`[global]
shutdown_timeout = "2s"

[observability.tracing]
enabled = true
exporter = "otlp-http"
endpoint = %q
sample_ratio = %s
service_name = "jul-issue99-reload"
insecure = true

[[servers]]
listen = %q

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  return = 200
`, endpoint, ratio, addr))
}

func TestIssue99TOMLReloadPublishesZeroAndRestoresOne(t *testing.T) {
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/traces" {
			t.Errorf("collector path=%q, want /v1/traces", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()

	addr := freePort(t)
	endpoint := strings.TrimPrefix(collector.URL, "http://")
	initialRaw := issue99TracingTOML(addr, endpoint, "1.0")
	initial, err := config.Parse(initialRaw)
	if err != nil {
		t.Fatalf("parse initial TOML: %v", err)
	}
	if err := config.Validate(initial); err != nil {
		t.Fatalf("validate initial TOML: %v", err)
	}
	if err := observability.ValidateTracerConfig(initial.Observability.Tracing); err != nil {
		t.Fatalf("preflight initial tracing: %v", err)
	}

	oldProvider := otel.GetTracerProvider()
	oldPropagator := otel.GetTextMapPropagator()
	oldSeam := tracingseam.Active()
	tracerRuntime, err := observability.NewTracer(initial.Observability.Tracing)
	if err != nil {
		t.Fatalf("NewTracer: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := tracerRuntime.Shutdown(ctx); err != nil {
			t.Errorf("tracer shutdown: %v", err)
		}
		otel.SetTracerProvider(oldProvider)
		otel.SetTextMapPropagator(oldPropagator)
		tracingseam.Set(oldSeam)
		observability.UpdateTracingSampleRatio(1)
	}()

	otelTracer := otel.Tracer("jul/issue99-reload-test")
	parentCtx, existingParent := otelTracer.Start(context.Background(), "existing-sampled-parent")
	if !existingParent.SpanContext().IsSampled() {
		t.Fatal("initial ratio=1 root is not sampled")
	}
	defer existingParent.End()

	src := &issue99TOMLSource{}
	src.set(initialRaw)
	tag := &atomic.Pointer[string]{}
	one := "ratio-one"
	tag.Store(&one)
	validate := func(_ context.Context, cfg *config.Config) error {
		if err := config.Validate(cfg); err != nil {
			return err
		}
		return observability.ValidateTracerConfig(cfg.Observability.Tracing)
	}
	srv := New(
		initial,
		initial,
		lifecycle.ComputeFingerprint(initial),
		quietLogger(),
		bodyHandlerFactory(tag),
		src,
		validate,
	)

	ctx, cancel := context.WithCancel(context.Background())
	reload := make(chan ReloadRequest, 1)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reload, redact.EmptyState()) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run returned error: %v", err)
		}
	}()
	waitForServe(t, "http://"+addr+"/", one)

	// 1 -> 0 through actual TOML parsing/defaulting and the full reload plan.
	zeroRaw := issue99TracingTOML(addr, endpoint, "0.0")
	parsedZero, err := config.Parse(zeroRaw)
	if err != nil {
		t.Fatalf("parse zero TOML: %v", err)
	}
	if got := parsedZero.Observability.Tracing.SampleRatio; got != 0 {
		t.Fatalf("explicit zero defaulted to %v", got)
	}
	if err := config.Validate(parsedZero); err != nil {
		t.Fatalf("validate zero TOML: %v", err)
	}
	if reason, restart := lifecycle.RestartRequired(lifecycle.ComputeFingerprint(initial), lifecycle.ComputeFingerprint(parsedZero)); restart {
		t.Fatalf("ratio-only change classified restart-required: %s", reason)
	}
	zero := "ratio-zero"
	tag.Store(&zero)
	src.set(zeroRaw)
	reload <- ReloadRequest{Source: ReloadSourceSIGHUP}
	waitForServe(t, "http://"+addr+"/", zero)

	_, rootAtZero := otelTracer.Start(context.Background(), "new-root-at-zero")
	if rootAtZero.SpanContext().IsSampled() {
		t.Fatal("new root after TOML ratio=0 Publish is sampled")
	}
	rootAtZero.End()

	_, childAfterZero := otelTracer.Start(parentCtx, "child-after-zero")
	if !childAfterZero.SpanContext().IsSampled() {
		t.Fatal("sampled existing parent lost authority after ratio=0 Publish")
	}
	childAfterZero.End()

	// 0 -> 1 through the same source/reload path.
	oneRaw := issue99TracingTOML(addr, endpoint, "1.0")
	restored := "ratio-restored"
	tag.Store(&restored)
	src.set(oneRaw)
	reload <- ReloadRequest{Source: ReloadSourceSIGHUP}
	waitForServe(t, "http://"+addr+"/", restored)

	_, rootAtOne := otelTracer.Start(context.Background(), "new-root-at-one")
	if !rootAtOne.SpanContext().IsSampled() {
		t.Fatal("new root after TOML ratio=1 Publish is not sampled")
	}
	rootAtOne.End()
}
