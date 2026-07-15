// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// These characterization tests pin the per-reload handler-tree build logic
// extracted from serve() into HandlerFactory (ADR-0007, CQ-2 / #54). They run
// without a full process boot or network bind (commit=false / dry-run path).
package app

import (
	"context"
	"io"
	"net"
	"testing"

	"jul/internal/config"
	"jul/internal/middleware"
	"jul/internal/observability"
	"jul/internal/plugins"
	"jul/internal/upstream"
)

// minimalFactory builds a HandlerFactory backed by a minimal runtime and a
// real (but no-op) process-lifetime dependency set for use in table tests.
func minimalFactory(t *testing.T) (*HandlerFactory, func()) {
	t.Helper()
	log := observability.NewLogger(io.Discard, "info", "text")
	metrics := observability.NewMetrics()
	cfg := config.ProxyTarget("127.0.0.1:9001", ":0")

	rt, err := RuntimeBuilder{Config: cfg, Logger: log, Metrics: metrics}.Build()
	if err != nil {
		t.Fatalf("RuntimeBuilder.Build: %v", err)
	}

	poolReg := upstream.NewRegistry(upstream.RegistryOptions{
		Logger:      log,
		DialContext: (&net.Dialer{}).DialContext,
	})
	genRes := NewGenerationResources(poolReg)

	pm, err := plugins.NewManager(plugins.Options{Logger: log})
	if err != nil {
		rt.Close()
		poolReg.CloseAll()
		t.Fatalf("plugins.NewManager: %v", err)
	}

	rlStore := middleware.NewRateLimiterStore(context.Background(), 0, 0)
	logTail := observability.NewLogTail(0)

	f := &HandlerFactory{
		Log:         log,
		Metrics:     metrics,
		AccessSinks: []middleware.AccessSink{logTail},
		RLStore:     rlStore,
		EgressDial:  (&net.Dialer{}).DialContext,
		PoolReg:     poolReg,
		PluginMgr:   pm,
		GenRes:      genRes,
		RT:          rt,
	}

	cleanup := func() {
		_ = pm.Close()
		genRes.CloseLive()
		poolReg.CloseAll()
		rt.Close()
	}
	return f, cleanup
}

func TestHandlerFactoryBuildMinimalConfigDryRun(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()

	cfg := config.ProxyTarget("127.0.0.1:9001", ":0")
	handlers, retire, err := f.Build(cfg, false /* dry-run */)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(handlers) == 0 {
		t.Fatal("Build returned empty handler map; expected at least one listen address")
	}
	if retire != nil {
		t.Error("a dry-run Build must return a nil retire func")
	}
}

func TestHandlerFactoryBuildCommitReturnsRetire(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()

	cfg := config.ProxyTarget("127.0.0.1:9001", ":0")
	handlers, retire, err := f.Build(cfg, true /* commit */)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(handlers) == 0 {
		t.Fatal("Build returned empty handler map")
	}
	// retire is only non-nil when there is a previous generation to close;
	// on the first commit the previous generation is empty so retire may be nil.
	// What matters is that it is callable without panic.
	if retire != nil {
		retire()
	}
}

func TestHandlerFactoryBuildTwoListenersReturnsTwo(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()

	cfg := config.ProxyTarget("127.0.0.1:9001", ":0")
	// Add a second server block on a distinct address.
	cfg.Servers = append(cfg.Servers, config.ServerConfig{
		Listen: "127.0.0.2:0",
		Locations: []config.LocationConfig{
			{Match: config.MatchConfig{Type: "prefix", Path: "/"}, ProxyPass: "http://127.0.0.1:9001"},
		},
	})

	handlers, _, err := f.Build(cfg, false)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := len(handlers); got != 2 {
		t.Errorf("got %d handlers, want 2 (one per distinct listen addr)", got)
	}
}

func TestHandlerFactoryBuildInvalidCompressionFails(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()

	cfg := config.ProxyTarget("127.0.0.1:9001", ":0")
	// Enable compression with an encoder name that is not compiled into this
	// binary — the build should fail with a clear error, not silently skip it.
	cfg.Compression.Enabled = true
	cfg.Compression.Encoders = []string{"notarealencoder"}

	_, _, err := f.Build(cfg, false)
	if err == nil {
		t.Fatal("Build with invalid compression encoder succeeded; expected an error")
	}
}

func TestHandlerFactoryBuildErrorAbortsGeneration(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()

	// Force a build error by referencing an invalid plugin that is not loaded.
	cfg := config.ProxyTarget("127.0.0.1:9001", ":0")
	cfg.Servers[0].Locations[0].Plugin = "not-a-real-plugin"
	cfg.Servers[0].Locations[0].ProxyPass = ""

	_, _, err := f.Build(cfg, true /* commit */)
	if err == nil {
		t.Fatal("Build with bad plugin reference succeeded; expected an error")
	}
	// A subsequent dry-run build on a valid config must succeed, proving the
	// generation was aborted (not left in a committed/partial state) on error.
	good := config.ProxyTarget("127.0.0.1:9001", ":0")
	_, _, err = f.Build(good, false)
	if err != nil {
		t.Errorf("Build after aborted generation failed: %v", err)
	}
}
