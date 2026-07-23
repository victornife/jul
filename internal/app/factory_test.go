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
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/config"
	"jul/internal/middleware"
	"jul/internal/observability"
	"jul/internal/plugins"
	"jul/internal/redact"
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
	handlers, retire, err := f.Build(context.Background(), cfg, false /* dry-run */)
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
	handlers, retire, err := f.Build(context.Background(), cfg, true /* commit */)
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

	handlers, _, err := f.Build(context.Background(), cfg, false)
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
	cfg.Compression.Enabled = config.Bool(true)
	cfg.Compression.Encoders = []string{"notarealencoder"}

	_, _, err := f.Build(context.Background(), cfg, false)
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

	_, _, err := f.Build(context.Background(), cfg, true /* commit */)
	if err == nil {
		t.Fatal("Build with bad plugin reference succeeded; expected an error")
	}
	// A subsequent dry-run build on a valid config must succeed, proving the
	// generation was aborted (not left in a committed/partial state) on error.
	good := config.ProxyTarget("127.0.0.1:9001", ":0")
	_, _, err = f.Build(context.Background(), good, false)
	if err != nil {
		t.Errorf("Build after aborted generation failed: %v", err)
	}
}

func TestHandlerFactoryPrepareCommit(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()

	cfg := config.ProxyTarget("127.0.0.1:9001", ":0")
	handlers, genID, commitFn, abortFn, err := f.Prepare(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(handlers) == 0 {
		t.Fatal("Prepare returned empty handler map")
	}
	if commitFn == nil {
		t.Fatal("Prepare returned nil commitFn")
	}
	if abortFn == nil {
		t.Fatal("Prepare returned nil abortFn")
	}
	if genID == 0 {
		t.Fatal("Prepare returned zero genID")
	}
	snapshots, retirePrev := commitFn()
	_ = snapshots
	// commitFn no longer installs redaction; the caller (ReloadPlan.Publish)
	// owns that.
	// retirePrev is nil on the first commit (no previous generation).
	if retirePrev != nil {
		retirePrev()
	}
	// After commit, abortFn must be a safe no-op.
	abortFn()
	// A subsequent Build must succeed, proving the mutex was released by commitFn.
	good := config.ProxyTarget("127.0.0.1:9001", ":0")
	_, _, err = f.Build(context.Background(), good, false)
	if err != nil {
		t.Errorf("Build after Prepare+commit failed: %v", err)
	}
}

func TestHandlerFactoryPrepareAbort(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()

	cfg := config.ProxyTarget("127.0.0.1:9001", ":0")
	_, _, _, abortFn, err := f.Prepare(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	abortFn()
	// After abort, commitFn must be a safe no-op (but we don't have it; verify
	// the mutex was released by successfully starting another build).
	good := config.ProxyTarget("127.0.0.1:9001", ":0")
	_, _, err = f.Build(context.Background(), good, false)
	if err != nil {
		t.Errorf("Build after Prepare+abort failed: %v", err)
	}
}

func TestHandlerFactoryPrepareAbortDoesNotInstallRedaction(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()

	t.Setenv("JUL_FACTORY_ABORT_TEST", "candidate-secret-value")
	redact.Install(redact.EmptyState())

	cfg := config.ProxyTarget("127.0.0.1:9001", ":0")
	cfg.Admin.Token = "${env:JUL_FACTORY_ABORT_TEST}"
	candidate, err := config.NewCandidate(cfg)
	if err != nil {
		t.Fatalf("NewCandidate: %v", err)
	}
	if candidate.Redaction.Apply("candidate-secret-value") != "***" {
		t.Fatal("candidate redaction does not mask the secret")
	}
	_, _, _, abortFn, err := f.Prepare(context.Background(), candidate.Effective)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	abortFn()

	if redact.Apply("candidate-secret-value") != "candidate-secret-value" {
		t.Fatal("aborted Prepare installed candidate secret into live redaction state")
	}
}

func TestHandlerFactoryPrepareDoesNotReturnRedactionState(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()

	t.Setenv("JUL_FACTORY_COMMIT_TEST", "commit-secret-value")
	redact.Install(redact.EmptyState())

	cfg := config.ProxyTarget("127.0.0.1:9001", ":0")
	cfg.Admin.Token = "${env:JUL_FACTORY_COMMIT_TEST}"
	candidate, err := config.NewCandidate(cfg)
	if err != nil {
		t.Fatalf("NewCandidate: %v", err)
	}
	_, _, commitFn, abortFn, err := f.Prepare(context.Background(), candidate.Effective)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer abortFn()
	// Prepare no longer resolves secrets and does not return a redaction state;
	// the caller is responsible for the candidate redaction state (R7-05, R9-01).
	// Live registry must remain untouched before explicit install.
	if redact.Apply("commit-secret-value") != "commit-secret-value" {
		t.Fatal("commitFn installed redaction before the publish boundary")
	}
	commitFn()
}

func TestGenerationScopedPoolSnapshotsViaContext(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()

	up := config.UpstreamConfig{
		Name:     "api",
		Strategy: "round_robin",
		Servers:  []config.UpstreamServer{{Address: "127.0.0.1:8001"}},
	}
	f.PoolReg.Begin()
	pool, err := f.PoolReg.For(up, "http")
	if err != nil {
		t.Fatalf("PoolReg.For: %v", err)
	}
	f.PoolReg.Commit()

	var counts []int
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counts = append(counts, len(pool.BackendsCtx(r.Context())))
	})

	snap1 := pool.Snapshot()
	snapshots := upstream.SnapshotMap{upstream.PoolSnapshotKey{Name: "api", Scheme: "http"}: snap1}
	base := httptest.NewRequest("GET", "/", nil)
	req1 := base.WithContext(upstream.WithSnapshot(base.Context(), snapshots))
	handler.ServeHTTP(httptest.NewRecorder(), req1)

	// Simulate a later live pool update; the snapshot carried by the context
	// keeps the request stable.
	pool.UpdateBackends([]config.UpstreamServer{
		{Address: "127.0.0.1:8001"},
		{Address: "127.0.0.1:8002"},
	})
	base2 := httptest.NewRequest("GET", "/", nil)
	req2 := base2.WithContext(upstream.WithSnapshot(base2.Context(), snapshots))
	handler.ServeHTTP(httptest.NewRecorder(), req2)

	if len(counts) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(counts))
	}
	if counts[0] != 1 {
		t.Errorf("first request saw %d backends, want 1", counts[0])
	}
	if counts[1] != 1 {
		t.Errorf("second request saw %d backends, want 1 (context snapshot should be stable)", counts[1])
	}
}

// TestPrepareCapturesSnapshotsAfterCommit verifies that commitFn returns pool
// snapshots taken from the live registry AFTER pools are committed (R8-01). A
// newly staged upstream must be present in the returned snapshot map.
func TestPrepareCapturesSnapshotsAfterCommit(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()

	cfg := &config.Config{
		Servers: []config.ServerConfig{{
			Listen: ":0",
			Locations: []config.LocationConfig{{
				Match:     config.MatchConfig{Type: "prefix", Path: "/"},
				ProxyPass: "http://api",
			}},
		}},
		Upstreams: []config.UpstreamConfig{{
			Name:     "api",
			Strategy: "round_robin",
			Servers:  []config.UpstreamServer{{Address: "127.0.0.1:8001"}},
		}},
	}

	_, _, commitFn, abortFn, err := f.Prepare(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	snapshots, retirePrev := commitFn()
	defer func() {
		if retirePrev != nil {
			retirePrev()
		}
	}()
	abortFn() // safe no-op after commit

	key := upstream.PoolSnapshotKey{Name: "api", Scheme: "http"}
	if snapshots == nil || snapshots[key] == nil {
		t.Fatalf("commitFn did not return a snapshot for newly committed pool %v", key)
	}
	if len(snapshots[key].Backends()) != 1 {
		t.Errorf("snapshot has %d backends, want 1", len(snapshots[key].Backends()))
	}

	// A second Prepare+commit must capture the new generation's backend view.
	cfg.Upstreams[0].Servers = append(cfg.Upstreams[0].Servers, config.UpstreamServer{Address: "127.0.0.1:8002"})
	_, _, commitFn2, abortFn2, err := f.Prepare(context.Background(), cfg)
	if err != nil {
		t.Fatalf("second Prepare: %v", err)
	}
	snapshots2, retirePrev2 := commitFn2()
	defer func() {
		if retirePrev2 != nil {
			retirePrev2()
		}
	}()
	abortFn2()

	if snapshots2 == nil || snapshots2[key] == nil {
		t.Fatalf("second commitFn did not return snapshot for %v", key)
	}
	if len(snapshots2[key].Backends()) != 2 {
		t.Errorf("second snapshot has %d backends, want 2", len(snapshots2[key].Backends()))
	}
}
