// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/server"
	"jul/internal/upstream"
)

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
