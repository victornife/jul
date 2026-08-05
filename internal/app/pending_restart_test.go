// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/server"
)

// testLogger returns a logger that discards output. pendingRestartCheck only
// uses it for warning on resolution errors.
func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(testLogWriter(t), &slog.HandlerOptions{Level: slog.LevelWarn}))
}

func testLogWriter(t *testing.T) *os.File {
	t.Helper()
	f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open devnull: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestPendingRestartCheckLoadConfigErrorReturnsNil(t *testing.T) {
	startup := &config.Candidate{Raw: &config.Config{}}
	loadFn := func() (*config.Config, error) {
		return nil, errors.New("disk unavailable")
	}
	got := pendingRestartCheck(startup, lifecycle.EmptyFingerprint(), server.LiveSnapshot{}, loadFn, testLogger(t))
	if len(got) != 0 {
		t.Fatalf("load error should yield empty pending list, got %v", got)
	}
}

func TestPendingRestartCheckResolveError(t *testing.T) {
	startup := &config.Candidate{Raw: &config.Config{}}
	loadFn := func() (*config.Config, error) {
		// Return a config with an unresolvable secret reference.
		return &config.Config{
			Admin: config.AdminConfig{Token: "${env:MISSING_PENDING_RESTART_SECRET}"},
		}, nil
	}
	got := pendingRestartCheck(startup, lifecycle.EmptyFingerprint(), server.LiveSnapshot{}, loadFn, testLogger(t))
	if len(got) != 1 || got[0] != "resolve_error" {
		t.Fatalf("resolve error should yield [resolve_error], got %v", got)
	}
}

func TestPendingRestartCheckDetectsSecretRotation(t *testing.T) {
	t.Setenv("PENDING_TOKEN_A", "token-alpha")

	startupRaw := &config.Config{
		Admin: config.AdminConfig{Token: "${env:PENDING_TOKEN_A}"},
		Servers: []config.ServerConfig{{
			Listen:    freePort(t),
			Locations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200}},
		}},
	}
	startup, err := config.NewCandidate(startupRaw)
	if err != nil {
		t.Fatalf("resolve startup candidate: %v", err)
	}
	startupFP := lifecycle.ComputeFingerprint(startup.Effective)

	// Change only the secret value, not the reference.
	t.Setenv("PENDING_TOKEN_A", "token-beta")
	loadFn := func() (*config.Config, error) { return startupRaw, nil }

	got := pendingRestartCheck(startup, startupFP, server.LiveSnapshot{}, loadFn, testLogger(t))
	if !slices.Contains(got, "admin") {
		t.Fatalf("secret rotation should report admin subsystem, got %v", got)
	}
}

func TestPendingRestartCheckMixedPendingAndHot(t *testing.T) {
	addr := freePort(t)
	startupRaw := &config.Config{
		Observability: config.ObservabilityConfig{AccessLog: config.AccessLogConfig{
			Enabled:     config.Bool(true),
			Sinks:       []string{"stdout"},
			Format:      "text",
			RotateMaxMB: 100,
			RotateKeep:  7,
		}},
		Servers: []config.ServerConfig{{
			Listen:    addr,
			Locations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200}},
		}},
	}
	startup, err := config.NewCandidate(startupRaw)
	if err != nil {
		t.Fatalf("resolve startup candidate: %v", err)
	}
	startupFP := lifecycle.ComputeFingerprint(startup.Effective)

	// Change an active restart-required access-log field and a hot-reloadable
	// location field. Deprecated global.access_log is deliberately ignored.
	nextRaw, err := startupRaw.Clone()
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	nextRaw.Observability.AccessLog.Format = "json"
	nextRaw.Servers[0].Locations[0].Return = 201

	loadFn := func() (*config.Config, error) { return nextRaw, nil }
	got := pendingRestartCheck(startup, startupFP, server.LiveSnapshot{}, loadFn, testLogger(t))
	if !slices.Contains(got, "access_log") {
		t.Fatalf("restart-required change should report access_log, got %v", got)
	}
	if slices.Contains(got, "return") {
		t.Fatalf("hot-reloadable change should not be reported, got %v", got)
	}
}

func TestPendingRestartCheckUsesLiveSnapshotForRebind(t *testing.T) {
	addr := freePort(t)
	startupRaw := &config.Config{
		Servers: []config.ServerConfig{{
			Listen:            addr,
			ReadHeaderTimeout: config.Duration(2 * time.Second),
			Locations:         []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200}},
		}},
	}
	startup, err := config.NewCandidate(startupRaw)
	if err != nil {
		t.Fatalf("resolve startup candidate: %v", err)
	}
	startupFP := lifecycle.ComputeFingerprint(startup.Effective)

	// Change a bind-time-frozen setting on the kept address.
	nextRaw, err := startupRaw.Clone()
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	nextRaw.Servers[0].ReadHeaderTimeout = config.Duration(9 * time.Second)
	loadFn := func() (*config.Config, error) { return nextRaw, nil }

	// Live snapshot says the address is currently bound with a fingerprint that
	// does not match the candidate, simulating a frozen-setting change on a
	// kept listener.
	live := server.LiveSnapshot{
		Listeners: map[string]server.BoundListenerInfo{
			addr: {Fingerprint: "old-bound-fingerprint"},
		},
	}
	got := pendingRestartCheck(startup, startupFP, live, loadFn, testLogger(t))
	if !slices.Contains(got, "listener") {
		t.Fatalf("frozen-setting change on bound listener should report listener, got %v", got)
	}

	// Live snapshot says the address is NOT currently bound, so the address is
	// treated as newly added and the rebind check is skipped.
	liveEmpty := server.LiveSnapshot{Listeners: map[string]server.BoundListenerInfo{}}
	got = pendingRestartCheck(startup, startupFP, liveEmpty, loadFn, testLogger(t))
	if slices.Contains(got, "listener") {
		t.Fatalf("unbound address should not trigger listener pending restart, got %v", got)
	}
}

func TestPendingRestartCheckDetectsFileBackedSecretRotation(t *testing.T) {
	tmp := t.TempDir()
	secretPath := filepath.Join(tmp, "admin-token.txt")
	if err := os.WriteFile(secretPath, []byte("alpha"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	startupRaw := &config.Config{
		Admin: config.AdminConfig{Token: "${file:" + secretPath + "}"},
		Servers: []config.ServerConfig{{
			Listen:    freePort(t),
			Locations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200}},
		}},
	}
	startup, err := config.NewCandidate(startupRaw)
	if err != nil {
		t.Fatalf("resolve startup candidate: %v", err)
	}
	startupFP := lifecycle.ComputeFingerprint(startup.Effective)

	// Rotate the file contents without changing the reference.
	if err := os.WriteFile(secretPath, []byte("beta"), 0o600); err != nil {
		t.Fatalf("rotate secret: %v", err)
	}
	loadFn := func() (*config.Config, error) { return startupRaw, nil }

	got := pendingRestartCheck(startup, startupFP, server.LiveSnapshot{}, loadFn, testLogger(t))
	if !slices.Contains(got, "admin") {
		t.Fatalf("file-backed secret rotation should report admin subsystem, got %v", got)
	}
}

func TestPendingRestartCheckACMEWithSecretReferenceNotPendingOnStartup(t *testing.T) {
	tmp := t.TempDir()
	secretPath := filepath.Join(tmp, "acme-email.txt")
	if err := os.WriteFile(secretPath, []byte("ops@example.com"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	startupRaw := &config.Config{
		Servers: []config.ServerConfig{{
			Listen:      freePort(t),
			ServerNames: []string{"localhost"},
			TLS: &config.TLSConfig{
				ACME: &config.ACMEConfig{
					Enabled:   true,
					Email:     "${file:" + secretPath + "}",
					CA:        "letsencrypt-staging",
					Challenge: "http-01",
					CacheDir:  tmp,
				},
			},
		}},
	}
	startup, err := config.NewCandidate(startupRaw)
	if err != nil {
		t.Fatalf("resolve startup candidate: %v", err)
	}
	startupFP := lifecycle.ComputeFingerprint(startup.Effective)

	// Reloading the same raw config should not flag ACME as needing a restart
	// just because the effective config resolves the secret reference.
	loadFn := func() (*config.Config, error) { return startupRaw, nil }

	got := pendingRestartCheck(startup, startupFP, server.LiveSnapshot{}, loadFn, testLogger(t))
	if slices.Contains(got, "acme") {
		t.Fatalf("unresolved ACME secret reference must not cause false pending restart on startup, got %v", got)
	}
}
