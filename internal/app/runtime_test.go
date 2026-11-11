// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// These characterization tests pin the startup behavior extracted from serve()
// into RuntimeBuilder (ADR-0007, SEQ-04): the process-lifetime subsystems and
// the build-tag feature gates. They run without a full process boot.
package app

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/adminapi"
	"jul/internal/config"
	"jul/internal/inspection"
	"jul/internal/observability"
	"jul/internal/plugins"
)

func TestRuntimeBuilderBuildMinimalConfig(t *testing.T) {
	log := observability.NewLogger(io.Discard, "info", "text")
	cfg := config.ProxyTarget("127.0.0.1:9000", ":0")
	metrics := observability.NewMetrics()

	rt, err := RuntimeBuilder{Config: cfg, Logger: log, Metrics: metrics}.Build()
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
	if rt.Inspection == nil {
		t.Fatal("process-owned inspection governor is nil")
	}
	if snapshot := rt.Inspection.Snapshot(); snapshot.Limits.Mode != inspection.Enforce ||
		snapshot.Limits.MaxBytes != 256<<20 || snapshot.ActiveLeases != 0 {
		t.Fatalf("inspection startup snapshot = %+v", snapshot)
	}
	lease, _, owner, err := rt.Inspection.AcquireLease(context.TODO())
	if err != nil || !owner {
		t.Fatalf("synthetic I1 lease = (%v, %v, %v)", lease, owner, err)
	}
	if _, err := lease.Charge(inspection.BufferBytes, rt.Inspection.Snapshot().Limits.MaxBytes); err == nil {
		t.Fatal("synthetic over-limit charge unexpectedly succeeded")
	}
	metricsResponse := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(metricsResponse, httptest.NewRequest("GET", "/metrics", nil))
	if metricsResponse.Code != 200 || !strings.Contains(metricsResponse.Body.String(), "jul_inspection_bytes{class=\"base\"} 16384") ||
		!strings.Contains(metricsResponse.Body.String(), `jul_inspection_rejections_total{mode="enforce",reason="bytes"} 1`) {
		t.Fatalf("inspection metrics did not report the process governor: status=%d body=%s", metricsResponse.Code, metricsResponse.Body.String())
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if rt.ACME != nil {
		t.Error("ACME should be nil for a config that enables no ACME domain")
	}
	if got := rt.StreamStatus(); got != "" {
		t.Errorf("StreamStatus = %q, want empty for a config with no streams", got)
	}
	rt.Close() // idempotent teardown must not panic
}

func TestRuntimeBuilderRejectsInvalidInspectionConfig(t *testing.T) {
	log := observability.NewLogger(io.Discard, "info", "text")
	cfg := config.ProxyTarget("127.0.0.1:9000", ":0")
	cfg.Global.Inspection.Mode = "invalid"

	rt, err := (RuntimeBuilder{Config: cfg, Logger: log, Metrics: observability.NewMetrics()}).Build()
	if err == nil || !strings.Contains(err.Error(), "mode must be enforce or observe") {
		t.Fatalf("Build() error = %v, want invalid inspection mode", err)
	}
	if rt != nil {
		t.Fatal("a failed Build must return a nil runtime")
	}
}

type guestCapacityReporterFunc func() (plugins.GuestCapacityStats, error)

func (f guestCapacityReporterFunc) GuestCapacityStats() (plugins.GuestCapacityStats, error) {
	return f()
}

func TestInspectionPluginGuestCapacitySource(t *testing.T) {
	source := inspectionPluginGuestCapacitySource(guestCapacityReporterFunc(func() (plugins.GuestCapacityStats, error) {
		return plugins.GuestCapacityStats{MaxPermittedBytes: 128, LiveInstances: 2, MaxInstances: 4}, nil
	}))
	got, err := source()
	if err != nil || got != (adminapi.InspectionPluginGuestCapacity{
		MaxPermittedBytes: 128,
		LiveInstances:     2,
		MaxInstances:      4,
	}) {
		t.Fatalf("capacity source = %+v, %v", got, err)
	}

	wantErr := errors.New("injected guest capacity failure")
	source = inspectionPluginGuestCapacitySource(guestCapacityReporterFunc(func() (plugins.GuestCapacityStats, error) {
		return plugins.GuestCapacityStats{}, wantErr
	}))
	got, err = source()
	if !errors.Is(err, wantErr) || got != (adminapi.InspectionPluginGuestCapacity{}) {
		t.Fatalf("failed capacity source = %+v, %v; want zero result and original error", got, err)
	}
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
