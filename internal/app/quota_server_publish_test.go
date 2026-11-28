// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/server"
	"jul/internal/upstream"
)

func TestQuotaCapacityGrowthDuringPrepareRejectsReloadAndKeepsServing(t *testing.T) {
	listen := freePort(t)
	backend := func(body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, body)
		}))
	}
	firstBackend := backend("current")
	defer firstBackend.Close()
	middleBackend := backend("middle")
	defer middleBackend.Close()
	nextBackend := backend("candidate")
	defer nextBackend.Close()

	makeConfig := func(prefix, target string) *config.Config {
		cfg := config.ProxyTarget(target, listen)
		classes := make(map[string]config.QuotaClassConfig, 8)
		classes["public"] = config.QuotaClassConfig{
			Shared:    config.QuotaLimitConfig{Rate: 8, Burst: 8},
			PerCaller: config.QuotaLimitConfig{Rate: 1, Burst: 1},
		}
		for i := 1; i <= 7; i++ {
			classes[fmt.Sprintf("%s-%d", prefix, i)] = config.QuotaClassConfig{
				Shared:    config.QuotaLimitConfig{Rate: 8, Burst: 8},
				PerCaller: config.QuotaLimitConfig{Rate: 1, Burst: 1},
			}
		}
		cfg.QuotaPolicies = map[string]config.QuotaPolicyConfig{
			"app": {Mode: "enforce", Rate: 80, Burst: 80, MaxCallers: 1, Classes: classes},
		}
		cfg.Servers[0].Locations[0].QuotaPolicy = "app"
		return cfg
	}

	runtime := newQuotaRuntime()
	oldPolicy := makeConfig("old", firstBackend.URL)
	if err := config.Validate(oldPolicy); err != nil {
		t.Fatalf("validate initial policy: %v", err)
	}
	oldCandidate := prepareQuotaCandidate(t, runtime, oldPolicy)
	oldCandidate.Commit()
	coordinator := oldCandidate.Binding("app").state.enforce
	for name := range oldPolicy.QuotaPolicies["app"].Classes {
		if name == "public" {
			continue
		}
		if got, err := coordinator.Admit(name, name); err != nil || !got.Allowed {
			t.Fatalf("deplete old class %q = %+v, %v", name, got, err)
		}
	}
	time.Sleep(110 * time.Millisecond)
	current := makeConfig("current", firstBackend.URL)
	if err := config.Validate(current); err != nil {
		t.Fatalf("validate serving policy: %v", err)
	}
	currentCandidate := prepareQuotaCandidate(t, runtime, current)
	currentCandidate.Commit()

	startup, err := config.NewCandidate(current)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := config.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(t.TempDir(), "jul.toml")
	if err := os.WriteFile(sourcePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	factory, cleanupFactory := minimalFactory(t)
	factory.QuotaRuntime = runtime
	var lateAdmissionClass atomic.Value
	lateAdmissionClass.Store("current-7")
	handlerFactory := server.HandlerFactory(func(ctx context.Context, cfg *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		class := lateAdmissionClass.Load().(string)
		server.RegisterPublishCheck(ctx, func() (func(), error) {
			if got, err := coordinator.Admit(class, "caller-arrived-after-prepare"); err != nil || !got.Allowed {
				return nil, fmt.Errorf("late admission = %+v, %v", got, err)
			}
			return func() {}, nil
		})
		return factory.Prepare(ctx, cfg)
	})
	liveServer := server.New(
		startup.Effective,
		startup.Raw,
		lifecycle.ComputeFingerprint(startup.Effective),
		factory.Log,
		handlerFactory,
		config.NewTOMLSource(sourcePath),
		func(_ context.Context, candidate *config.Config) error { return config.Validate(candidate) },
	)
	ctx, cancel := context.WithCancel(context.Background())
	reloads := make(chan server.ReloadRequest, 1)
	runDone := make(chan error, 1)
	go func() { runDone <- liveServer.Run(ctx, reloads, startup.Redaction) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-runDone:
			if err != nil {
				t.Errorf("server shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("server did not stop after cancellation")
		}
		cleanupFactory()
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := liveServer.LiveSnapshot().Listeners[listen]; ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	initialGeneration := liveServer.LiveSnapshot().Generation
	if initialGeneration == 0 {
		t.Fatal("initial generation did not become live")
	}

	publish := func(id string, expectedGeneration uint64, candidateCfg *config.Config) server.ReloadResult {
		t.Helper()
		candidate, err := config.NewCandidate(candidateCfg)
		if err != nil {
			t.Fatal(err)
		}
		candidateRaw, err := config.Marshal(candidateCfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(sourcePath, candidateRaw, 0o600); err != nil {
			t.Fatal(err)
		}
		result := make(chan server.ReloadResult, 1)
		reloads <- server.ReloadRequest{
			ID:                 id,
			Source:             server.ReloadSourceAdmin,
			Candidate:          candidate,
			ExpectedGeneration: expectedGeneration,
			Result:             result,
		}
		select {
		case outcome := <-result:
			return outcome
		case <-time.After(5 * time.Second):
			t.Fatalf("reload %q did not complete", id)
			return server.ReloadResult{}
		}
	}

	middleCfg := makeConfig("current", middleBackend.URL)
	delete(middleCfg.QuotaPolicies["app"].Classes, "current-7")
	middleCfg.QuotaPolicies["app"].Classes["middle-7"] = config.QuotaClassConfig{
		Shared:    config.QuotaLimitConfig{Rate: 8, Burst: 8},
		PerCaller: config.QuotaLimitConfig{Rate: 1, Burst: 1},
	}
	firstOutcome := publish("quota-fill-last-retired-slot", initialGeneration, middleCfg)
	if firstOutcome.Outcome != server.ReloadAppliedLive {
		t.Fatalf("first reload outcome=%q phase=%q error=%q; want successful capacity-filling publish", firstOutcome.Outcome, firstOutcome.FailedPhase, firstOutcome.Error)
	}

	lateAdmissionClass.Store("current-1")
	candidateCfg := makeConfig("current", nextBackend.URL)
	delete(candidateCfg.QuotaPolicies["app"].Classes, "current-7")
	candidateCfg.QuotaPolicies["app"].Classes["middle-7"] = config.QuotaClassConfig{
		Shared:    config.QuotaLimitConfig{Rate: 8, Burst: 8},
		PerCaller: config.QuotaLimitConfig{Rate: 1, Burst: 1},
	}
	delete(candidateCfg.QuotaPolicies["app"].Classes, "current-1")
	candidateCfg.QuotaPolicies["app"].Classes["next-1"] = config.QuotaClassConfig{
		Shared:    config.QuotaLimitConfig{Rate: 8, Burst: 8},
		PerCaller: config.QuotaLimitConfig{Rate: 1, Burst: 1},
	}
	outcome := publish("quota-caller-growth", liveServer.LiveSnapshot().Generation, candidateCfg)
	if outcome.Outcome == server.ReloadAppliedLive || outcome.FailedPhase != "publish" ||
		!strings.Contains(outcome.Error, "class-state registry is full") {
		t.Fatalf("reload outcome=%q phase=%q error=%q; want clean publish rejection", outcome.Outcome, outcome.FailedPhase, outcome.Error)
	}
	if got := liveServer.LiveSnapshot().Generation; got != initialGeneration+1 {
		t.Fatalf("failed reload published generation %d, want unchanged %d", got, initialGeneration+1)
	}
	if got := liveServer.LiveSnapshot().EffectiveConfig.Servers[0].Locations[0].ProxyPass; got != middleBackend.URL {
		t.Fatalf("failed reload changed live upstream to %q", got)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + listen + "/")
	if err != nil {
		t.Fatalf("request current generation after rejected reload: %v", err)
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if resp.StatusCode != http.StatusOK || string(body) != "middle" {
		t.Fatalf("current generation response=(%d,%q), want (200,middle)", resp.StatusCode, body)
	}
}

func TestQuotaStateSurvivesServerPublishAndRollback(t *testing.T) {
	listen := freePort(t)
	backend := func(body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, body)
		}))
	}
	firstBackend := backend("first")
	defer firstBackend.Close()
	secondBackend := backend("second")
	defer secondBackend.Close()

	makeConfig := func(target string) *config.Config {
		cfg := config.ProxyTarget(target, listen)
		cfg.QuotaPolicies = map[string]config.QuotaPolicyConfig{
			"api": {
				Mode:       "enforce",
				Rate:       1,
				Burst:      1,
				MaxCallers: 16,
				Classes: map[string]config.QuotaClassConfig{
					"public": {
						Shared:    config.QuotaLimitConfig{Rate: 1, Burst: 1},
						PerCaller: config.QuotaLimitConfig{Rate: 1, Burst: 1},
					},
				},
			},
		}
		cfg.Servers[0].Locations[0].QuotaPolicy = "api"
		return cfg
	}

	initial := makeConfig(firstBackend.URL)
	initialCandidate, err := config.NewCandidate(initial)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := config.Marshal(initial)
	if err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(t.TempDir(), "jul.toml")
	if err := os.WriteFile(sourcePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	factory, cleanupFactory := minimalFactory(t)
	factory.QuotaRuntime = newQuotaRuntime()
	handlerFactory := server.HandlerFactory(func(ctx context.Context, cfg *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		return factory.Prepare(ctx, cfg)
	})

	// Use a real server listener and the production three-phase publish path;
	// only the upstream itself is provided by an httptest server.
	liveServer := server.New(
		initialCandidate.Effective,
		initialCandidate.Raw,
		lifecycle.ComputeFingerprint(initialCandidate.Effective),
		factory.Log,
		handlerFactory,
		config.NewTOMLSource(sourcePath),
		func(_ context.Context, candidate *config.Config) error { return config.Validate(candidate) },
	)
	ctx, cancel := context.WithCancel(context.Background())
	reloads := make(chan server.ReloadRequest, 1)
	runDone := make(chan error, 1)
	go func() {
		runDone <- liveServer.Run(ctx, reloads, initialCandidate.Redaction)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-runDone:
			if err != nil {
				t.Errorf("server shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("server did not stop after cancellation")
		}
		cleanupFactory()
	})

	address := listen
	waitUntil := time.Now().Add(5 * time.Second)
	for time.Now().Before(waitUntil) {
		snapshot := liveServer.LiveSnapshot()
		if _, ok := snapshot.Listeners[listen]; ok && snapshot.Generation != 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, ok := liveServer.LiveSnapshot().Listeners[listen]; !ok {
		t.Fatal("server did not publish a bound listener")
	}

	client := &http.Client{Timeout: 2 * time.Second}
	request := func() (int, string) {
		t.Helper()
		resp, err := client.Get("http://" + address + "/")
		if err != nil {
			t.Fatalf("request Jul listener: %v", err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read Jul response: %v", err)
		}
		return resp.StatusCode, string(body)
	}
	publish := func(id string, cfg *config.Config) {
		t.Helper()
		raw, err := config.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(sourcePath, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		candidate, err := config.NewCandidate(cfg)
		if err != nil {
			t.Fatal(err)
		}
		result := make(chan server.ReloadResult, 1)
		reloads <- server.ReloadRequest{
			ID:                 id,
			Source:             server.ReloadSourceAdmin,
			Candidate:          candidate,
			ExpectedGeneration: liveServer.LiveSnapshot().Generation,
			Result:             result,
		}
		select {
		case outcome := <-result:
			if outcome.Outcome != server.ReloadAppliedLive {
				t.Fatalf("reload %q outcome=%q failed_phase=%q error=%q", id, outcome.Outcome, outcome.FailedPhase, outcome.Error)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("reload %q did not complete", id)
		}
	}

	if status, body := request(); status != http.StatusOK || body != "first" {
		t.Fatalf("initial request = (%d, %q), want (200, first)", status, body)
	}
	lastAdmission := time.Now()

	publish("quota-publish", makeConfig(secondBackend.URL))
	if elapsed := time.Since(lastAdmission); elapsed >= time.Second {
		t.Fatalf("publish took %s after the last admission; fixture no longer distinguishes preserved state from a reset", elapsed)
	}
	if got := liveServer.LiveSnapshot().EffectiveConfig.Servers[0].Locations[0].ProxyPass; got != secondBackend.URL {
		t.Fatalf("published upstream = %q, want %q", got, secondBackend.URL)
	}
	if status, _ := request(); status != http.StatusTooManyRequests {
		t.Fatalf("request after server publish = %d, want 429 with the original depleted budget", status)
	}

	if elapsed := time.Since(lastAdmission); elapsed >= time.Second {
		t.Fatalf("publish and verification took %s after the last admission; fixture no longer distinguishes preserved state from a reset", elapsed)
	}
	publish("quota-rollback", initial)
	if elapsed := time.Since(lastAdmission); elapsed >= time.Second {
		t.Fatalf("rollback took %s after the last admission; fixture no longer distinguishes preserved state from a reset", elapsed)
	}
	if got := liveServer.LiveSnapshot().EffectiveConfig.Servers[0].Locations[0].ProxyPass; got != firstBackend.URL {
		t.Fatalf("rolled-back upstream = %q, want %q", got, firstBackend.URL)
	}
	if status, _ := request(); status != http.StatusTooManyRequests {
		t.Fatalf("request after server rollback = %d, want 429 with the original depleted budget", status)
	}
}
