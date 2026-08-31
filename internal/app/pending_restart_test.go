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

// admin.token is hot-reloadable (#95), so these tests exercise the last
// remaining restart-required, secret-digested value field instead:
// admin.tls.client_auth.ca_file. The CA pool is read and installed only when
// the admin listener is created, so an in-place rotation still requires a
// restart, and the fingerprint must still catch it through the digest.
func TestPendingRestartCheckDetectsSecretRotation(t *testing.T) {
	caA := filepath.Join(t.TempDir(), "ca-a.pem")
	caB := filepath.Join(t.TempDir(), "ca-b.pem")
	if err := os.WriteFile(caA, []byte("ca-alpha"), 0o600); err != nil {
		t.Fatalf("write ca a: %v", err)
	}
	if err := os.WriteFile(caB, []byte("ca-beta"), 0o600); err != nil {
		t.Fatalf("write ca b: %v", err)
	}
	t.Setenv("PENDING_CA_PATH", caA)

	startupRaw := &config.Config{
		Admin: config.AdminConfig{
			TLS: &config.AdminTLSConfig{ClientAuth: &config.ClientAuthConfig{Mode: "require", CAFile: "${env:PENDING_CA_PATH}"}},
		},
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

	// Change only the secret reference's target, not the reference text.
	t.Setenv("PENDING_CA_PATH", caB)
	loadFn := func() (*config.Config, error) { return startupRaw, nil }

	got := pendingRestartCheck(startup, startupFP, server.LiveSnapshot{}, loadFn, testLogger(t))
	if !slices.Contains(got, "mtls") {
		t.Fatalf("secret rotation should report mtls subsystem, got %v", got)
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

	// Change an active restart-required tracing field and a hot-reloadable
	// location field. Deprecated global.access_log is deliberately ignored.
	// access_log itself is hot-reloadable (#98), so tracing.enabled stands in
	// as the restart-required trigger here.
	nextRaw, err := startupRaw.Clone()
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	nextRaw.Observability.Tracing.Enabled = true
	nextRaw.Servers[0].Locations[0].Return = 201

	loadFn := func() (*config.Config, error) { return nextRaw, nil }
	got := pendingRestartCheck(startup, startupFP, server.LiveSnapshot{}, loadFn, testLogger(t))
	if !slices.Contains(got, "tracing") {
		t.Fatalf("restart-required change should report tracing, got %v", got)
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

// admin.token is hot-reloadable (#95); admin.tls.client_auth.ca_file remains
// the restart-required, file-backed secret exercising this path (its content
// is digested directly, with no ${file:...} indirection needed).
func TestPendingRestartCheckDetectsFileBackedSecretRotation(t *testing.T) {
	tmp := t.TempDir()
	caPath := filepath.Join(tmp, "admin-ca.pem")
	if err := os.WriteFile(caPath, []byte("alpha"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	startupRaw := &config.Config{
		Admin: config.AdminConfig{
			TLS: &config.AdminTLSConfig{ClientAuth: &config.ClientAuthConfig{Mode: "require", CAFile: caPath}},
		},
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

	// Rotate the file contents without changing the reference (path).
	if err := os.WriteFile(caPath, []byte("beta"), 0o600); err != nil {
		t.Fatalf("rotate secret: %v", err)
	}
	loadFn := func() (*config.Config, error) { return startupRaw, nil }

	got := pendingRestartCheck(startup, startupFP, server.LiveSnapshot{}, loadFn, testLogger(t))
	if !slices.Contains(got, "mtls") {
		t.Fatalf("file-backed secret rotation should report mtls subsystem, got %v", got)
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
