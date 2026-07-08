// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// These characterization tests pin the startup behavior extracted from serve()
// into RuntimeBuilder (ADR-0007, SEQ-04): the process-lifetime subsystems and
// the build-tag feature gates. They run without a full process boot.
package app

import (
	"io"
	"strings"
	"testing"

	"jul/internal/config"
	"jul/internal/observability"
)

func TestRuntimeBuilderBuildMinimalConfig(t *testing.T) {
	log := observability.NewLogger(io.Discard, "info", "text")
	cfg := config.ProxyTarget("127.0.0.1:9000", ":0")

	rt, err := RuntimeBuilder{Config: cfg, Logger: log, Metrics: observability.NewMetrics()}.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if rt == nil {
		t.Fatal("Build returned a nil runtime")
	}
	if rt.Tracer == nil {
		t.Error("Tracer is nil; the composition root wraps every listener with it")
	}
	if rt.Stream == nil {
		t.Error("Stream is nil; it is reloaded on every OnReloaded")
	}
	if rt.ACME != nil {
		t.Error("ACME should be nil for a config that enables no ACME domain")
	}
	if got := rt.StreamStatus(); got != "" {
		t.Errorf("StreamStatus = %q, want empty for a config with no streams", got)
	}
	rt.Close() // idempotent teardown must not panic
}

func TestRuntimeBuilderTracingWithoutTagFailsFast(t *testing.T) {
	// In the default (lean) build the otel tag is absent, so an enabled tracing
	// block must fail the build-tag gate rather than silently no-op — mirroring
	// how serve() returned exit code 1 on this path.
	if observability.TracingCompiled {
		t.Skip("built with the otel tag; the tracing gate does not reject an enabled config")
	}
	log := observability.NewLogger(io.Discard, "info", "text")
	cfg := config.ProxyTarget("127.0.0.1:9000", ":0")
	cfg.Observability.Tracing.Enabled = true

	rt, err := RuntimeBuilder{Config: cfg, Logger: log, Metrics: observability.NewMetrics()}.Build()
	if err == nil {
		t.Fatal("enabled tracing without the otel tag was accepted")
	}
	if rt != nil {
		t.Error("a failed Build must return a nil runtime, not a partially-built one")
	}
	if !strings.Contains(err.Error(), "tracing") {
		t.Errorf("error %q does not identify the failing subsystem (tracing)", err)
	}
}
