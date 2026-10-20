// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// These characterization tests pin the per-reload handler-tree build logic
// extracted from serve() into HandlerFactory (ADR-0007, CQ-2 / #54). They run
// without a full process boot or network bind (commit=false / dry-run path).
package app

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/middleware"
	"jul/internal/observability"
	"jul/internal/plugins"
	"jul/internal/redact"
	"jul/internal/server"
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
		Log:           log,
		Metrics:       metrics,
		AccessLogTail: logTail,
		RLStore:       rlStore,
		EgressDial:    (&net.Dialer{}).DialContext,
		PoolReg:       poolReg,
		PluginMgr:     pm,
		GenRes:        genRes,
		RT:            rt,
	}

	cleanup := func() {
		_ = pm.Close()
		genRes.CloseLive()
		poolReg.CloseAll()
		rt.Close()
	}
	return f, cleanup
}

func TestHTTPMetricInventoryPublishedBeforeReloadCallback(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(writer, "body") }))
	defer backend.Close()
	factory, cleanup := minimalFactory(t)
	defer cleanup()
	factory.Metrics = observability.NewMetrics(observability.WithHostLabel(true), observability.WithRouteLabel(true))
	addr := freePort(t)
	makeCandidate := func(routeID string) *config.Candidate {
		cfg := config.ProxyTarget(backend.URL, addr)
		cfg.Observability.Metrics.HostLabel = true
		cfg.Observability.Metrics.RouteLabel = true
		cfg.Servers[0].ServerNames = []string{"api.example"}
		cfg.Servers[0].Locations[0].RouteID = &routeID
		if routeID == "r-bbbbbbbbbbbbbbbbbbbbbbbbbb" {
			cfg.Servers[0].Locations[0].Match.Path = "/new"
		}
		candidate, err := config.NewCandidate(cfg)
		if err != nil {
			t.Fatal(err)
		}
		return candidate
	}
	initial := makeCandidate("r-aaaaaaaaaaaaaaaaaaaaaaaaaa")
	initial.Effective.Observability.Metrics.HostLabel = false
	initial.Raw.Observability.Metrics.HostLabel = false
	factory.Metrics.SetHTTPLabelInventory(initial.Effective.Servers)
	reloads := make(chan server.ReloadRequest)
	srv := server.New(initial.Effective, initial.Raw, lifecycle.ComputeFingerprint(initial.Effective), factory.Log, factory.Prepare, config.NewTOMLSource(""), func(context.Context, *config.Config) error { return nil })
	ready, callbackEntered, releaseCallback := make(chan struct{}), make(chan struct{}), make(chan struct{})
	srv.OnInitialGenerationReady = func() { close(ready) }
	srv.OnReloaded = func(cfg *config.Config) (error, error) {
		close(callbackEntered)
		<-releaseCallback
		factory.Metrics.SetHTTPLabelInventory(cfg.Servers)
		return nil, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reloads, initial.Redaction) }()
	var releaseOnce sync.Once
	defer func() {
		releaseOnce.Do(func() { close(releaseCallback) })
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	wait := func(signal <-chan struct{}) {
		t.Helper()
		select {
		case <-signal:
		case <-time.After(10 * time.Second):
			t.Fatal("server phase did not complete")
		}
	}
	wait(ready)
	next := makeCandidate("r-bbbbbbbbbbbbbbbbbbbbbbbbbb")
	result := make(chan server.ReloadResult, 1)
	reloads <- server.ReloadRequest{Source: server.ReloadSourceAdmin, Candidate: next, Result: result}
	select {
	case <-callbackEntered:
	case reloaded := <-result:
		t.Fatalf("reload finished before publication callback: %+v", reloaded)
	case <-time.After(10 * time.Second):
		t.Fatal("publication callback not entered")
	}
	request, err := http.NewRequest(http.MethodGet, "http://"+addr+"/new", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "api.example"
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", response.StatusCode)
	}
	rows := factory.Metrics.Snapshot().RouteMetrics
	if len(rows) != 1 || rows[0].RouteID != *next.Effective.Servers[0].Locations[0].RouteID || rows[0].Requests != 1 || rows[0].DurationCount != 1 || rows[0].ResponseBytes != 4 {
		t.Fatalf("new generation metrics before callback release: %+v", rows)
	}
	assertHost := func() {
		t.Helper()
		families, err := factory.Metrics.Gather()
		if err != nil {
			t.Fatal(err)
		}
		for _, family := range families {
			if family.GetName() != "jul_http_requests_total" && family.GetName() != "jul_http_request_duration_seconds" && family.GetName() != "jul_http_response_bytes_total" {
				continue
			}
			for _, series := range family.GetMetric() {
				for _, label := range series.GetLabel() {
					if label.GetName() == "host" && label.GetValue() != "api.example" {
						t.Fatalf("%s host=%q", family.GetName(), label.GetValue())
					}
				}
			}
		}
	}
	assertHost()
	releaseOnce.Do(func() { close(releaseCallback) })
	select {
	case reloaded := <-result:
		if reloaded.Outcome != server.ReloadAppliedLive {
			t.Fatalf("reload=%+v", reloaded)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("reload did not complete")
	}

	rejected := makeCandidate("r-cccccccccccccccccccccccccc")
	rejected.Effective.Servers[0].ServerNames = []string{"rejected.example"}
	rejected.Effective.Observability.Metrics.HostLabel = false
	_, _, _, abort, err := factory.Prepare(ctx, rejected.Effective)
	if err != nil {
		t.Fatal(err)
	}
	if got := factory.Metrics.Snapshot().RouteMetrics; len(got) != 1 || got[0].RouteID != rows[0].RouteID || got[0].Requests != 1 {
		abort()
		t.Fatalf("prepare mutated live metrics: %+v", got)
	}
	abort()
	busyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busyListener.Close()
	rejected.Effective.Servers[0].Listen = busyListener.Addr().String()
	rejected.Raw.Servers[0].Listen = busyListener.Addr().String()
	reloads <- server.ReloadRequest{Source: server.ReloadSourceAdmin, Candidate: rejected, Result: result}
	select {
	case failed := <-result:
		if failed.Published || failed.FailedPhase != "stage_listeners" {
			t.Fatalf("failed staging=%+v", failed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("failed staging result not delivered")
	}
	response, err = (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, readErr = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got := factory.Metrics.Snapshot().RouteMetrics; len(got) != 1 || got[0].RouteID != rows[0].RouteID || got[0].Requests != 2 || got[0].ResponseBytes != 8 || got[0].DurationCount != 2 {
		t.Fatalf("abort/staging failure changed live inventory: %+v", got)
	}
	assertHost()
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
	pool, err := f.PoolReg.For(context.Background(), up, "http")
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

func TestFifth653DottedUpstreamAndMixedCORSRuntime(t *testing.T) {
	freeAddr := func(t *testing.T) string {
		t.Helper()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		_ = ln.Close()
		return addr
	}

	startSSEBackend := func(t *testing.T, label string) (*httptest.Server, <-chan struct{}, <-chan error) {
		t.Helper()
		entered := make(chan struct{}, 8)
		closed := make(chan error, 8)
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/stream" {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = fmt.Fprintf(w, "data: %s\n\n", label)
				if fl, ok := w.(http.Flusher); ok {
					fl.Flush()
				}
				entered <- struct{}{}
				<-r.Context().Done()
				closed <- r.Context().Err()
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(label))
		}))
		t.Cleanup(ts.Close)
		return ts, entered, closed
	}

	t.Run("dotted-upstream-membership-change-must-retire-like-bare-upstream", func(t *testing.T) {
		runCase := func(t *testing.T, upstreamName string, withShadowURLPool bool) {
			t.Helper()
			backOld, enteredOld, _ := startSSEBackend(t, "old")
			backNew, _, _ := startSSEBackend(t, "new")
			oldHost := strings.TrimPrefix(backOld.URL, "http://")
			newHost := strings.TrimPrefix(backNew.URL, "http://")

			addr := freeAddr(t)
			routeID := "api"
			makeCfg := func(backendAddr string) *config.Config {
				ups := []config.UpstreamConfig{{
					Name:    upstreamName,
					Servers: []config.UpstreamServer{{Address: backendAddr}},
				}}
				if withShadowURLPool {
					ups = append(ups, config.UpstreamConfig{
						Name:    "http://" + upstreamName,
						Servers: []config.UpstreamServer{{Address: oldHost}},
					})
				}
				return &config.Config{
					Upstreams: ups,
					Servers: []config.ServerConfig{{
						Listen: addr,
						Locations: []config.LocationConfig{{
							RouteID:   &routeID,
							Match:     config.MatchConfig{Type: "prefix", Path: "/"},
							ProxyPass: "http://" + upstreamName,
						}},
					}},
				}
			}

			cfg1 := makeCfg(oldHost)
			if _, err := config.NewCandidate(cfg1); err != nil {
				t.Fatalf("cfg1 NewCandidate(%s): %v", upstreamName, err)
			}

			f, cleanup := minimalFactory(t)
			defer cleanup()
			srv := server.New(
				cfg1,
				cfg1,
				lifecycle.ComputeFingerprint(cfg1),
				observability.NewLogger(io.Discard, "error", "text"),
				f.Prepare,
				config.NewTOMLSource(""),
				func(context.Context, *config.Config) error { return nil },
			)
			srv.WAFSourceDigests = f.WAFSourceDigests
			srv.PluginModuleChanges = f.PluginModuleChanges

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reloadCh := make(chan server.ReloadRequest, 2)
			runDone := make(chan error, 1)
			go func() {
				runDone <- srv.Run(ctx, reloadCh, redact.EmptyState())
			}()
			defer func() {
				cancel()
				<-runDone
			}()

			var streamResp *http.Response
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				resp, err := http.Get("http://" + addr + "/stream")
				if err == nil {
					streamResp = resp
					break
				}
				time.Sleep(15 * time.Millisecond)
			}
			if streamResp == nil {
				t.Fatal("server never became reachable")
			}
			defer streamResp.Body.Close()

			br := bufio.NewReader(streamResp.Body)
			line, err := br.ReadString('\n')
			if err != nil || !strings.Contains(line, "old") {
				t.Fatalf("initial stream line=%q err=%v", line, err)
			}
			<-enteredOld

			readDone := make(chan error, 1)
			go func() {
				_, rErr := io.ReadAll(br)
				readDone <- rErr
			}()

			cfg2 := makeCfg(newHost)
			cand2, err := config.NewCandidate(cfg2)
			if err != nil {
				t.Fatalf("cfg2 NewCandidate(%s): %v", upstreamName, err)
			}
			resCh := make(chan server.ReloadResult, 1)
			reloadCh <- server.ReloadRequest{
				ID:        "retire-" + upstreamName,
				Source:    server.ReloadSourceAdmin,
				Candidate: cand2,
				Result:    resCh,
			}
			res := <-resCh
			if res.Outcome != server.ReloadAppliedLive {
				t.Fatalf("reload outcome=%+v", res)
			}

			checkResp, err := http.Get("http://" + addr + "/check")
			if err != nil {
				t.Fatal(err)
			}
			checkBody, _ := io.ReadAll(checkResp.Body)
			_ = checkResp.Body.Close()
			if string(checkBody) != "new" {
				t.Fatalf("new request body=%q, want new", string(checkBody))
			}

			retiredWithinBound := false
			select {
			case <-readDone:
				retiredWithinBound = true
			case <-time.After(2200 * time.Millisecond):
			}
			if !retiredWithinBound || len(res.EntryRetirements) != 1 || res.EntryRetirements[0].EndedExchanges != 1 {
				t.Fatalf("upstream %q: retiredWithinBound=%v retirements=%+v", upstreamName, retiredWithinBound, res.EntryRetirements)
			}
		}

		t.Run("bare-control", func(t *testing.T) {
			runCase(t, "backend", false)
		})
		t.Run("dotted-name", func(t *testing.T) {
			runCase(t, "backend.local", false)
		})
		t.Run("shadow-url-named-pool", func(t *testing.T) {
			runCase(t, "backend", true)
		})
	})

	t.Run("cors-credentials-disabled-with-added-origin-must-retire", func(t *testing.T) {
		runCase := func(t *testing.T, originsAfter []string) {
			t.Helper()
			back, entered, _ := startSSEBackend(t, "cors")
			addr := freeAddr(t)
			routeID := "cors-api"
			makeCfg := func(origins []string, creds bool) *config.Config {
				return &config.Config{
					Servers: []config.ServerConfig{{
						Listen: addr,
						Locations: []config.LocationConfig{{
							RouteID:   &routeID,
							Match:     config.MatchConfig{Type: "prefix", Path: "/"},
							ProxyPass: back.URL,
							CORS: &config.CORSConfig{
								Enabled:          true,
								AllowedOrigins:   origins,
								AllowCredentials: creds,
							},
						}},
					}},
				}
			}

			cfg1 := makeCfg([]string{"https://old.example"}, true)
			f, cleanup := minimalFactory(t)
			defer cleanup()
			srv := server.New(
				cfg1,
				cfg1,
				lifecycle.ComputeFingerprint(cfg1),
				observability.NewLogger(io.Discard, "error", "text"),
				f.Prepare,
				config.NewTOMLSource(""),
				func(context.Context, *config.Config) error { return nil },
			)
			srv.WAFSourceDigests = f.WAFSourceDigests
			srv.PluginModuleChanges = f.PluginModuleChanges

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reloadCh := make(chan server.ReloadRequest, 2)
			runDone := make(chan error, 1)
			go func() {
				runDone <- srv.Run(ctx, reloadCh, redact.EmptyState())
			}()
			defer func() {
				cancel()
				<-runDone
			}()

			var streamResp *http.Response
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/stream", nil)
				req.Header.Set("Origin", "https://old.example")
				req.Header.Set("Cookie", "sid=secret")
				resp, err := http.DefaultClient.Do(req)
				if err == nil {
					streamResp = resp
					break
				}
				time.Sleep(15 * time.Millisecond)
			}
			if streamResp == nil {
				t.Fatal("server never became reachable")
			}
			defer streamResp.Body.Close()
			if got := streamResp.Header.Get("Access-Control-Allow-Credentials"); got != "true" {
				t.Fatalf("initial Access-Control-Allow-Credentials=%q, want true", got)
			}

			br := bufio.NewReader(streamResp.Body)
			if _, err := br.ReadString('\n'); err != nil {
				t.Fatal(err)
			}
			<-entered

			readDone := make(chan error, 1)
			go func() {
				_, rErr := io.ReadAll(br)
				readDone <- rErr
			}()

			cfg2 := makeCfg(originsAfter, false)
			cand2, err := config.NewCandidate(cfg2)
			if err != nil {
				t.Fatal(err)
			}
			resCh := make(chan server.ReloadResult, 1)
			reloadCh <- server.ReloadRequest{
				ID:        "cors-change",
				Source:    server.ReloadSourceAdmin,
				Candidate: cand2,
				Result:    resCh,
			}
			res := <-resCh
			if res.Outcome != server.ReloadAppliedLive {
				t.Fatalf("reload outcome=%+v", res)
			}

			reqAfter, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/check", nil)
			reqAfter.Header.Set("Origin", "https://old.example")
			respAfter, err := http.DefaultClient.Do(reqAfter)
			if err != nil {
				t.Fatal(err)
			}
			_ = respAfter.Body.Close()
			if got := respAfter.Header.Get("Access-Control-Allow-Credentials"); got != "" {
				t.Fatalf("post-reload Access-Control-Allow-Credentials=%q, want empty", got)
			}

			retiredWithinBound := false
			select {
			case <-readDone:
				retiredWithinBound = true
			case <-time.After(2200 * time.Millisecond):
			}
			if !retiredWithinBound || len(res.EntryRetirements) != 1 || res.EntryRetirements[0].EndedExchanges != 1 {
				t.Fatalf("originsAfter=%v: retiredWithinBound=%v retirements=%+v", originsAfter, retiredWithinBound, res.EntryRetirements)
			}
		}

		t.Run("pure-credential-disable-control", func(t *testing.T) {
			runCase(t, []string{"https://old.example"})
		})
		t.Run("mixed-added-origin-and-disabled-credentials", func(t *testing.T) {
			runCase(t, []string{"https://old.example", "https://new.example"})
		})
	})
}

func TestSixth653CertificateRotationMustRetire(t *testing.T) {
	writeSelfSignedCert := func(t *testing.T, dir, prefix, dnsName string) (string, string, []byte) {
		t.Helper()
		priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
		if err != nil {
			t.Fatal(err)
		}
		tmpl := &x509.Certificate{
			SerialNumber: serial,
			Subject:      pkix.Name{CommonName: prefix + "-" + dnsName},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(24 * time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			DNSNames:     []string{dnsName},
			IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
		if err != nil {
			t.Fatal(err)
		}
		certPath := filepath.Join(dir, prefix+".crt")
		keyPath := filepath.Join(dir, prefix+".key")
		certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		keyDER, err := x509.MarshalECPrivateKey(priv)
		if err != nil {
			t.Fatal(err)
		}
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
		if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
			t.Fatal(err)
		}
		return certPath, keyPath, der
	}

	runCase := func(t *testing.T, alsoWithdraw bool) {
		t.Helper()
		dir := t.TempDir()
		certA, keyA, derA := writeSelfSignedCert(t, dir, "cert-a", "example.test")
		certB, keyB, derB := writeSelfSignedCert(t, dir, "cert-b", "example.test")
		certOther, keyOther, _ := writeSelfSignedCert(t, dir, "cert-other", "other.test")

		entered := make(chan struct{}, 2)
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/stream" {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = fmt.Fprint(w, "data: open\n\n")
				if fl, ok := w.(http.Flusher); ok {
					fl.Flush()
				}
				entered <- struct{}{}
				<-r.Context().Done()
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		}))
		defer backend.Close()

		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		_ = ln.Close()

		ln2, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		otherAddr := ln2.Addr().String()
		_ = ln2.Close()

		routeID := "tls-api"
		otherID := "other-api"
		makeCfg := func(certPath, keyPath string, withdrawn bool) *config.Config {
			return &config.Config{
				Servers: []config.ServerConfig{
					{
						Listen:      addr,
						ServerNames: []string{"example.test"},
						TLS: &config.TLSConfig{
							Enabled: true,
							Cert:    certPath,
							Key:     keyPath,
						},
						Locations: []config.LocationConfig{{
							RouteID:   &routeID,
							Match:     config.MatchConfig{Type: "prefix", Path: "/"},
							ProxyPass: backend.URL,
							Withdrawn: withdrawn,
						}},
					},
					{
						Listen:      otherAddr,
						ServerNames: []string{"other.test"},
						TLS: &config.TLSConfig{
							Enabled: true,
							Cert:    certOther,
							Key:     keyOther,
						},
						Locations: []config.LocationConfig{{
							RouteID:   &otherID,
							Match:     config.MatchConfig{Type: "prefix", Path: "/"},
							ProxyPass: backend.URL,
						}},
					},
				},
			}
		}

		cfg1 := makeCfg(certA, keyA, false)
		f, cleanup := minimalFactory(t)
		defer cleanup()
		srv := server.New(
			cfg1,
			cfg1,
			lifecycle.ComputeFingerprint(cfg1),
			observability.NewLogger(io.Discard, "error", "text"),
			f.Prepare,
			config.NewTOMLSource(""),
			func(context.Context, *config.Config) error { return nil },
		)
		srv.WAFSourceDigests = f.WAFSourceDigests
		srv.PluginModuleChanges = f.PluginModuleChanges

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		reloadCh := make(chan server.ReloadRequest, 2)
		runDone := make(chan error, 1)
		go func() {
			runDone <- srv.Run(ctx, reloadCh, redact.EmptyState())
		}()
		defer func() {
			cancel()
			<-runDone
		}()

		client := &http.Client{
			Transport: &http.Transport{
				ForceAttemptHTTP2: true,
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true,
					ServerName:         "example.test",
					NextProtos:         []string{"h2", "http/1.1"},
				},
			},
		}
		defer client.CloseIdleConnections()

		var streamResp *http.Response
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			resp, err := client.Get("https://" + addr + "/stream")
			if err == nil {
				streamResp = resp
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if streamResp == nil {
			t.Fatal("TLS listener never became reachable")
		}
		defer streamResp.Body.Close()
		if streamResp.TLS == nil || len(streamResp.TLS.PeerCertificates) == 0 || !bytes.Equal(streamResp.TLS.PeerCertificates[0].Raw, derA) {
			t.Fatal("initial stream did not present certificate A")
		}

		br := bufio.NewReader(streamResp.Body)
		if _, err := br.ReadString('\n'); err != nil {
			t.Fatal(err)
		}
		<-entered

		readDone := make(chan error, 1)
		go func() {
			_, rErr := io.ReadAll(br)
			readDone <- rErr
		}()

		cfg2 := makeCfg(certB, keyB, alsoWithdraw)
		cand2, err := config.NewCandidate(cfg2)
		if err != nil {
			t.Fatal(err)
		}
		resCh := make(chan server.ReloadResult, 1)
		reloadCh <- server.ReloadRequest{
			ID:        "rotate-cert",
			Source:    server.ReloadSourceAdmin,
			Candidate: cand2,
			Result:    resCh,
		}
		res := <-resCh
		if res.Outcome != server.ReloadAppliedLive {
			t.Fatalf("reload outcome=%+v", res)
		}

		freshConn, err := tls.Dial("tcp", addr, &tls.Config{
			InsecureSkipVerify: true,
			ServerName:         "example.test",
		})
		if err != nil {
			t.Fatal(err)
		}
		peers := freshConn.ConnectionState().PeerCertificates
		_ = freshConn.Close()
		if len(peers) == 0 || !bytes.Equal(peers[0].Raw, derB) {
			t.Fatal("fresh connection after rotation did not present certificate B")
		}

		retiredWithinBound := false
		select {
		case <-readDone:
			retiredWithinBound = true
		case <-time.After(2200 * time.Millisecond):
		}
		if !retiredWithinBound || len(res.EntryRetirements) != 1 || res.EntryRetirements[0].RouteID != "tls-api" {
			t.Fatalf("alsoWithdraw=%v: retiredWithinBound=%v retirements=%+v", alsoWithdraw, retiredWithinBound, res.EntryRetirements)
		}
	}

	t.Run("certificate-rotation-with-withdrawal-control", func(t *testing.T) {
		runCase(t, true)
	})
	t.Run("certificate-rotation-only", func(t *testing.T) {
		runCase(t, false)
	})
}

func TestR3AuthUpstreamMembershipRetires(t *testing.T) {
	for _, poolChange := range []bool{false, true} {
		name := "direct-auth-url-change-control"
		if poolChange {
			name = "auth-pool-membership-change-and-shared-fanout"
		}
		t.Run(name, func(t *testing.T) {
			stopped1 := make(chan struct{})
			stopped2 := make(chan struct{})
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/s1":
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: s1\n\n")
					_ = http.NewResponseController(w).Flush()
					<-r.Context().Done()
					close(stopped1)
				case "/s2":
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: s2\n\n")
					_ = http.NewResponseController(w).Flush()
					<-r.Context().Done()
					close(stopped2)
				default:
					_, _ = io.WriteString(w, "ok")
				}
			}))
			defer backend.Close()

			allow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))
			defer allow.Close()
			deny := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusForbidden)
			}))
			defer deny.Close()

			addr := freePort(t)
			rid1 := "private-1"
			rid2 := "private-2"
			ridPub := "public-unrelated"
			authURL := allow.URL + "/check"
			if poolChange {
				authURL = "http://authpool/check"
			}
			cfg := &config.Config{
				Upstreams: []config.UpstreamConfig{{
					Name:    "authpool",
					Servers: []config.UpstreamServer{{Address: strings.TrimPrefix(allow.URL, "http://")}},
				}},
				Servers: []config.ServerConfig{{
					Listen: addr,
					Locations: []config.LocationConfig{
						{
							RouteID:   &rid1,
							Match:     config.MatchConfig{Type: "prefix", Path: "/s1"},
							ProxyPass: backend.URL,
							Auth:      &config.AuthConfig{ForwardAuth: &config.ForwardAuthConfig{URL: authURL}},
						},
						{
							RouteID:   &rid2,
							Match:     config.MatchConfig{Type: "prefix", Path: "/s2"},
							ProxyPass: backend.URL,
							Auth:      &config.AuthConfig{ForwardAuth: &config.ForwardAuthConfig{URL: authURL}},
						},
						{
							RouteID:   &ridPub,
							Match:     config.MatchConfig{Type: "prefix", Path: "/pub"},
							ProxyPass: backend.URL,
						},
					},
				}},
			}
			initial, err := config.NewCandidate(cfg)
			if err != nil {
				t.Fatal(err)
			}
			f, cleanup := minimalFactory(t)
			defer cleanup()
			srv := server.New(
				initial.Effective,
				initial.Raw,
				lifecycle.ComputeFingerprint(initial.Effective),
				f.Log,
				f.Prepare,
				config.NewTOMLSource(""),
				func(context.Context, *config.Config) error { return nil },
			)
			ready := make(chan struct{})
			srv.OnInitialGenerationReady = func() { close(ready) }
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			reload := make(chan server.ReloadRequest, 1)
			go func() { done <- srv.Run(ctx, reload, redact.EmptyState()) }()
			defer func() { cancel(); <-done }()
			select {
			case <-ready:
			case <-time.After(5 * time.Second):
				t.Fatal("startup timeout")
			}

			client := &http.Client{Timeout: 15 * time.Second}
			defer client.CloseIdleConnections()
			endpoint := "http://" + addr

			old1, err := client.Get(endpoint + "/s1")
			if err != nil {
				t.Fatal(err)
			}
			defer old1.Body.Close()
			if old1.StatusCode != http.StatusOK {
				t.Fatalf("old1 status=%d", old1.StatusCode)
			}
			old2, err := client.Get(endpoint + "/s2")
			if err != nil {
				t.Fatal(err)
			}
			defer old2.Body.Close()
			if old2.StatusCode != http.StatusOK {
				t.Fatalf("old2 status=%d", old2.StatusCode)
			}

			next, _ := cfg.Clone()
			if poolChange {
				next.Upstreams[0].Servers[0].Address = strings.TrimPrefix(deny.URL, "http://")
			} else {
				next.Servers[0].Locations[0].Auth.ForwardAuth.URL = deny.URL + "/check"
				next.Servers[0].Locations[1].Auth.ForwardAuth.URL = deny.URL + "/check"
			}
			candidate, err := config.NewCandidate(next)
			if err != nil {
				t.Fatal(err)
			}
			result := make(chan server.ReloadResult, 1)
			reload <- server.ReloadRequest{Source: server.ReloadSourceAdmin, Candidate: candidate, Result: result}
			var res server.ReloadResult
			select {
			case res = <-result:
			case <-time.After(6 * time.Second):
				t.Fatal("reload timeout")
			}
			if res.Outcome != server.ReloadAppliedLive {
				t.Fatalf("reload=%+v", res)
			}

			fresh, err := client.Get(endpoint + "/s1")
			if err != nil {
				t.Fatal(err)
			}
			_ = fresh.Body.Close()
			if fresh.StatusCode != http.StatusForbidden {
				t.Fatalf("new auth backend not enforcing denial: status=%d", fresh.StatusCode)
			}

			for idx, ch := range []<-chan struct{}{stopped1, stopped2} {
				select {
				case <-ch:
				case <-time.After(2200 * time.Millisecond):
					t.Fatalf("stream %d survived >2s after auth dependency change; retirements=%+v", idx+1, res.EntryRetirements)
				}
			}
			if len(res.EntryRetirements) != 2 {
				t.Fatalf("expected 2 entry retirements (excluding public-unrelated), got %+v", res.EntryRetirements)
			}
		})
	}
}
