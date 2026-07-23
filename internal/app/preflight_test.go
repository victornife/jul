// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"jul/internal/admin"
	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/server"
)

type mockStreamPreflighter struct{}

func (m *mockStreamPreflighter) PreflightBuild(_ context.Context, _ []config.StreamServer, _ map[string]config.UpstreamConfig) error {
	return nil
}
func (m *mockStreamPreflighter) PreflightListeners(_ context.Context, _ map[string]struct{}, _ []config.StreamServer) error {
	return nil
}
func (m *mockStreamPreflighter) BoundKeys() []string { return nil }

func TestPreflightApplyValidConfigOK(t *testing.T) {
	cfg := config.ProxyTarget(":9000", ":0")
	p := Preflight{
		BuildHandlers: func(_ context.Context, _ *config.Config, _ bool) (map[string]http.Handler, func(), error) {
			return map[string]http.Handler{}, nil, nil
		},
		Stream: &mockStreamPreflighter{},
	}
	if _, err := p.Apply(context.Background(), cfg, nil, PreflightHot); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestPreflightApplyInvalidConfigFailsFast(t *testing.T) {
	bad := &config.Config{
		Servers: []config.ServerConfig{{
			Listen:    ":8080",
			Locations: []config.LocationConfig{{Return: 200}},
		}},
	}
	p := Preflight{
		BuildHandlers: func(_ context.Context, _ *config.Config, _ bool) (map[string]http.Handler, func(), error) {
			t.Fatal("BuildHandlers should not be called for structurally invalid config")
			return nil, nil, nil
		},
		Stream: &mockStreamPreflighter{},
	}
	if _, err := p.Apply(context.Background(), bad, nil, PreflightHot); err == nil {
		t.Fatal("structurally invalid config accepted")
	}
}

func TestPreflightApplyInvalidRBACPolicyFailsFast(t *testing.T) {
	cfg := config.ProxyTarget(":9000", ":0")
	cfg.Admin.RBAC.Enabled = true
	// Unknown role causes rbac.Build to fail. This should be caught before
	// BuildHandlers (and before persistence) so the operator gets an early,
	// reload-safe error.
	cfg.Admin.RBAC.Principals = []config.AdminPrincipal{{
		Name:  "ops",
		Role:  "unknown-role",
		Token: "${env:TEST_RBAC_TOKEN}",
	}}
	t.Setenv("TEST_RBAC_TOKEN", "super-secret-token-value-that-is-long-enough")
	p := Preflight{
		BuildHandlers: func(_ context.Context, _ *config.Config, _ bool) (map[string]http.Handler, func(), error) {
			t.Fatal("BuildHandlers should not be called for invalid RBAC policy")
			return nil, nil, nil
		},
		Stream: &mockStreamPreflighter{},
	}
	_, err := p.Apply(context.Background(), cfg, nil, PreflightHot)
	if err == nil {
		t.Fatal("invalid RBAC policy accepted")
	}
	if !strings.Contains(err.Error(), "unknown role") {
		t.Fatalf("error should mention unknown role, got: %v", err)
	}
}

func TestPreflightApplyPanicCaught(t *testing.T) {
	cfg := config.ProxyTarget(":9000", ":0")
	p := Preflight{
		BuildHandlers: func(_ context.Context, _ *config.Config, _ bool) (map[string]http.Handler, func(), error) {
			panic("simulated panic in handler build")
		},
		Stream: &mockStreamPreflighter{},
	}
	_, err := p.Apply(context.Background(), cfg, nil, PreflightHot)
	if err == nil {
		t.Fatal("expected error from panic recovery, got nil")
	}
	if !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("error should mention panicked, got: %v", err)
	}
}

func TestPreflightApplyWithIdenticalPrevOK(t *testing.T) {
	cfg := config.ProxyTarget(":9000", ":0")
	p := Preflight{
		BuildHandlers: func(_ context.Context, _ *config.Config, _ bool) (map[string]http.Handler, func(), error) {
			return map[string]http.Handler{}, nil, nil
		},
		Stream: &mockStreamPreflighter{},
	}
	if _, err := p.Apply(context.Background(), cfg, cfg, PreflightHot); err != nil {
		t.Fatalf("identical prev/next rejected: %v", err)
	}
}

// TestPreflightApplyReturnsCandidate (R8-11) verifies that a successful
// preflight returns the same immutable candidate that the live reload will use,
// so secrets are resolved exactly once and handed off without re-resolution.
func TestPreflightApplyReturnsCandidate(t *testing.T) {
	t.Setenv("PREFLIGHT_SECRET", "preflight-secret-value")
	cfg := &config.Config{
		Global: config.GlobalConfig{WorkerThreads: "${env:PREFLIGHT_SECRET}"},
		Servers: []config.ServerConfig{{
			Listen:    ":8080",
			Locations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200}},
		}},
	}
	p := Preflight{
		BuildHandlers: func(_ context.Context, _ *config.Config, _ bool) (map[string]http.Handler, func(), error) {
			return map[string]http.Handler{}, nil, nil
		},
		Stream: &mockStreamPreflighter{},
	}
	res, err := p.Apply(context.Background(), cfg, nil, PreflightHot)
	if err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if res == nil {
		t.Fatal("Preflight.Apply returned nil result")
	}
	cand := res.Candidate
	if cand == nil {
		t.Fatal("Preflight.Apply returned nil candidate")
	}
	if cand.Effective == nil {
		t.Fatal("candidate.Effective is nil")
	}
	if cand.Effective.Global.WorkerThreads != "preflight-secret-value" {
		t.Fatalf("candidate effective worker_threads = %q, want resolved secret", cand.Effective.Global.WorkerThreads)
	}
	if cand.Raw == nil {
		t.Fatal("candidate.Raw is nil")
	}
	if cand.Raw.Global.WorkerThreads != "${env:PREFLIGHT_SECRET}" {
		t.Fatalf("candidate raw worker_threads = %q, want original reference", cand.Raw.Global.WorkerThreads)
	}
}

func TestPreflightPreparedAdminAbortsOnFailure(t *testing.T) {
	cfg := config.ProxyTarget(":9000", ":0")
	var aborted bool
	p := Preflight{
		PrepareAdmin: func(config.AdminConfig) (*server.PreparedCommit, error) {
			return server.NewPreparedCommit(nil, func() { aborted = true }), nil
		},
		BuildHandlers: func(_ context.Context, _ *config.Config, _ bool) (map[string]http.Handler, func(), error) {
			return nil, nil, errors.New("handler build failed")
		},
		Stream: &mockStreamPreflighter{},
	}
	if _, err := p.Apply(context.Background(), cfg, nil, PreflightHot); err == nil {
		t.Fatal("preflight unexpectedly succeeded")
	}
	if !aborted {
		t.Fatal("prepared admin artifact was not aborted after preflight failure")
	}
}

// TestPreflightApplyUsesLiveSnapshotForRebind (R9-04) verifies that when a
// LiveSnapshot is provided, the listener rebind check uses the actually-bound
// listener set rather than the on-disk prev config. A re-added address that is
// no longer bound is treated as a fresh listener, so a frozen-setting change
// does not trigger a false restart_required.
func TestPreflightApplyUsesLiveSnapshotForRebind(t *testing.T) {
	addr := freePort(t)
	base := &config.Config{
		Global: config.GlobalConfig{ShutdownTimeout: config.Duration(2 * time.Second)},
		Servers: []config.ServerConfig{{
			Listen:    addr,
			Locations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200}},
		}},
	}

	// next changes a bind-time-frozen setting on the same address.
	next, err := base.Clone()
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	next.Servers[0].ReadHeaderTimeout = config.Duration(9 * time.Second)

	p := Preflight{
		BuildHandlers: func(_ context.Context, _ *config.Config, _ bool) (map[string]http.Handler, func(), error) {
			return map[string]http.Handler{}, nil, nil
		},
		Stream: &mockStreamPreflighter{},
	}

	// Without a live snapshot, the on-disk prev says addr is kept, so the
	// frozen-setting change is rejected.
	if _, err := p.Apply(context.Background(), next, base, PreflightHot); err == nil {
		t.Fatal("expected restart_required without live snapshot")
	}

	// With a live snapshot showing the address is NOT currently bound, the
	// address is treated as newly added (probed) and the rebind check is skipped.
	p.LiveSnapshot = func() server.LiveSnapshot {
		return server.LiveSnapshot{Listeners: map[string]server.BoundListenerInfo{}}
	}
	if _, err := p.Apply(context.Background(), next, base, PreflightHot); err != nil {
		t.Fatalf("expected hot-apply with live snapshot showing address unbound, got: %v", err)
	}
}

// TestPreflightApplyUsesLiveSnapshotWithoutPrev (R10-04, R11-02) verifies that
// the listener probe, restart-required, and ACME checks still run against the
// live runtime snapshot when prev is nil.
func TestPreflightApplyUsesLiveSnapshotWithoutPrev(t *testing.T) {
	boundAddr := freePort(t)
	freeAddr := freePort(t)
	base := &config.Config{
		Global: config.GlobalConfig{ShutdownTimeout: config.Duration(2 * time.Second)},
		Servers: []config.ServerConfig{{
			Listen:    freeAddr,
			Locations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200}},
		}},
	}

	// Hold a listener so the candidate address is genuinely in use. The live
	// snapshot says it is unbound, so PreflightListeners must probe it and fail.
	ln, err := net.Listen("tcp", boundAddr)
	if err != nil {
		t.Fatalf("hold listener: %v", err)
	}
	defer ln.Close()

	p := Preflight{
		BuildHandlers: func(_ context.Context, _ *config.Config, _ bool) (map[string]http.Handler, func(), error) {
			return map[string]http.Handler{}, nil, nil
		},
		Stream: &mockStreamPreflighter{},
		LiveSnapshot: func() server.LiveSnapshot {
			return server.LiveSnapshot{
				Listeners: map[string]server.BoundListenerInfo{boundAddr: {Addr: boundAddr}},
			}
		},
	}

	t.Run("listener probe runs and rejects occupied address", func(t *testing.T) {
		occupied, err := base.Clone()
		if err != nil {
			t.Fatalf("clone: %v", err)
		}
		occupied.Servers[0].Listen = boundAddr
		if _, err := p.Apply(context.Background(), occupied, nil, PreflightHot); err == nil {
			t.Fatal("expected listener probe to reject an occupied address when prev is nil")
		}
	})

	t.Run("restart-required runs and rejects startup-bound change", func(t *testing.T) {
		p2 := p
		p2.StartupFP = lifecycle.Fingerprint{Values: map[string]any{"global.access_log": "startup.log"}}
		changed, err := base.Clone()
		if err != nil {
			t.Fatalf("clone: %v", err)
		}
		changed.Global.AccessLog = "changed.log"
		_, err = p2.Apply(context.Background(), changed, nil, PreflightHot)
		if err == nil {
			t.Fatal("expected restart_required for startup-bound change when prev is nil")
		}
		if !errors.Is(err, admin.ErrRestartRequired) {
			t.Fatalf("expected ErrRestartRequired, got: %v", err)
		}
	})

	t.Run("valid candidate passes with live snapshot and nil prev", func(t *testing.T) {
		if _, err := p.Apply(context.Background(), base, nil, PreflightHot); err != nil {
			t.Fatalf("expected apply to succeed with live snapshot and nil prev, got: %v", err)
		}
	})
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}
