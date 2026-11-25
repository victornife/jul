// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// These characterization tests lock the behavior of the composition-root
// helpers extracted into package app (adapters, preflight, BuildAdminDeps).
// They must continue to pass after each extraction phase.
package app

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/cache"
	"jul/internal/config"
	"jul/internal/inspection"
	"jul/internal/observability"
	"jul/internal/server"
	"jul/internal/upstream"
)

func TestAdminCacheAdapterNilCacheReturnsNilInterface(t *testing.T) {
	if AdminCache(nil) != nil {
		t.Fatal("AdminCache(nil) returned non-nil interface")
	}
}

func TestAdminCacheAdapterNonNilCacheReturnsPurger(t *testing.T) {
	log := observability.NewLogger(io.Discard, "info", "text")
	c, err := cache.New(config.CacheConfig{Enabled: true, MemoryMaxSize: 1024}, log)
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	if c == nil {
		t.Skip("cache.New returned nil — caching not available in this build")
	}
	p := AdminCache(c)
	if p == nil {
		t.Fatal("AdminCache(valid) returned nil")
	}
	p.Purge()
}

func TestAdaptUpstreamsMapsCorrectly(t *testing.T) {
	in := []upstream.PoolStatus{
		{
			Name:     "api",
			Strategy: "least_conn",
			Backends: []upstream.BackendStatus{
				{Address: "10.0.0.1:80", State: "available", Weight: 3, Inflight: 7},
				{Address: "10.0.0.2:80", State: "health_unhealthy", Weight: 1, Inflight: 0},
			},
		},
	}
	out := AdaptUpstreams(in)
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	if out[0].Name != "api" || out[0].Strategy != "least_conn" {
		t.Errorf("unexpected top-level fields: %+v", out[0])
	}
	if len(out[0].Backends) != 2 {
		t.Fatalf("backends = %d, want 2", len(out[0].Backends))
	}
	b := out[0].Backends[0]
	if b.Address != "10.0.0.1:80" || b.Weight != 3 || b.State != "available" || b.Inflight != 7 {
		t.Errorf("unexpected backend[0]: %+v", b)
	}
}

func TestAdaptUpstreamsReportsOldestAcceptedRefreshForServingSchemes(t *testing.T) {
	first := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	second := first.Add(time.Minute)
	out := AdaptUpstreams([]upstream.PoolStatus{
		{Name: "api", DiscoveryEnabled: true, DiscoveryLastSuccess: second},
		{Name: "api", DiscoveryEnabled: true, DiscoveryLastSuccess: first},
	})
	if len(out) != 1 || out[0].ServingDiscovery == nil {
		t.Fatalf("serving discovery aggregation = %+v", out)
	}
	if got := out[0].ServingDiscovery.LastSuccess; !got.Equal(first) {
		t.Fatalf("last success = %v, want oldest current scheme timestamp %v", got, first)
	}

	observed := upstream.PoolStatus{Name: "api", DiscoveryEnabled: true, DiscoveryLastSuccess: first}
	waiting := upstream.PoolStatus{Name: "api", DiscoveryEnabled: true}
	for _, input := range [][]upstream.PoolStatus{{observed, waiting}, {waiting, observed}} {
		out = AdaptUpstreams(input)
		if len(out) != 1 || out[0].ServingDiscovery == nil || !out[0].ServingDiscovery.LastSuccess.IsZero() {
			t.Fatalf("serving discovery aggregation = %+v, want no timestamp until every worker succeeds", out)
		}
	}
}

func TestAdaptCertsMapsCorrectly(t *testing.T) {
	in := []server.CertSummary{
		{
			ServerNames: []string{"a.example"},
			Source:      "file",
			Subject:     "CN=a.example",
			Issuer:      "CN=TestCA",
			DNSNames:    []string{"a.example"},
			NotBefore:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			NotAfter:    time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
		},
	}
	out := AdaptCerts(in)
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	c := out[0]
	if c.Subject != "CN=a.example" || c.Issuer != "CN=TestCA" {
		t.Errorf("unexpected cert fields: %+v", c)
	}
}

func TestValidateRuntimeConfigSmoke(t *testing.T) {
	ctx := context.Background()
	good := config.ProxyTarget("127.0.0.1:9000", ":0")
	if err := ValidateRuntimeConfig(ctx, good); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	bad := &config.Config{
		Servers: []config.ServerConfig{{
			Listen:    ":8080",
			Locations: []config.LocationConfig{{Return: 200}},
		}},
	}
	if err := ValidateRuntimeConfig(ctx, bad); err == nil {
		t.Fatal("structurally invalid config accepted")
	}
}

func TestBuildAdminDepsWiringCompleteness(t *testing.T) {
	log := observability.NewLogger(io.Discard, "info", "text")

	responseCache, err := cache.New(config.CacheConfig{}, log)
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	metrics := observability.NewMetrics()
	logTail := observability.NewLogTail(0)

	poolReg := upstream.NewRegistry(upstream.RegistryOptions{Logger: log})
	defer poolReg.CloseAll()

	var lastStreamReload atomic.Pointer[string]
	initial := ""
	lastStreamReload.Store(&initial)
	governor, err := inspection.New(inspection.Limits{
		Mode:                   inspection.Enforce,
		MaxBytes:               1 << 20,
		MaxResponseHeaderBytes: 1 << 20,
		EvaluationSlots:        1,
		EvaluationQueueTimeout: time.Second,
		BaseChargeBytes:        16 << 10,
	}, inspection.Hooks{})
	if err != nil {
		t.Fatalf("inspection.New: %v", err)
	}

	subsystems := Subsystems{
		ResponseCache:    responseCache,
		Metrics:          metrics,
		PoolReg:          poolReg,
		LogTail:          logTail,
		PluginsCompiled:  false,
		StreamCompiled:   false,
		WAFCompiled:      false,
		Inspection:       governor,
		LastStreamReload: &lastStreamReload,
	}

	// Use a temp dir so LoadConfig works.
	tmp := t.TempDir()
	path := filepath.Join(tmp, "test.toml")
	os.WriteFile(path, []byte("[[servers]]\nlisten = \":8080\"\n"), 0o644)

	deps := BuildAdminDeps("Jul.IA", "1.0.0", config.NewTOMLSource(path), subsystems)
	deps.Reload = func() error { return nil }
	deps.Ready = func() bool { return true }

	requireNonNil := func(name string, v any) {
		if v == nil {
			t.Errorf("Deps.%s is nil", name)
		}
	}

	requireNonNil("Metrics", deps.Metrics)
	requireNonNil("Stats", deps.Stats)
	requireNonNil("RequestSamples", deps.RequestSamples)
	requireNonNil("FailingRoutes", deps.FailingRoutes)
	requireNonNil("UpstreamHealthHistory", deps.UpstreamHealthHistory)
	requireNonNil("CertRenewalHistory", deps.CertRenewalHistory)
	requireNonNil("RecentLogs", deps.RecentLogs)
	requireNonNil("SubscribeLogs", deps.SubscribeLogs)
	requireNonNil("Upstreams", deps.Upstreams)
	if deps.InspectionSnapshot == nil || deps.InspectionSnapshot().Limits.MaxBytes != 1<<20 {
		t.Error("inspection governor snapshot was not wired")
	}
	requireNonNil("Certs", deps.Certs)
	requireNonNil("StreamStatus", deps.StreamStatus)
	requireNonNil("Reload", deps.Reload)
	requireNonNil("Ready", deps.Ready)
}
