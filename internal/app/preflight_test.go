// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"net/http"
	"strings"
	"testing"

	"jul/internal/config"
)

type mockStreamPreflighter struct{}

func (m *mockStreamPreflighter) PreflightBuild(_ []config.StreamServer, _ map[string]config.UpstreamConfig) error {
	return nil
}
func (m *mockStreamPreflighter) PreflightListeners(_, _ []config.StreamServer) error {
	return nil
}

func TestPreflightApplyValidConfigOK(t *testing.T) {
	cfg := config.ProxyTarget(":9000", ":0")
	p := Preflight{
		BuildHandlers: func(_ *config.Config, _ bool) (map[string]http.Handler, func(), error) {
			return map[string]http.Handler{}, nil, nil
		},
		Stream: &mockStreamPreflighter{},
	}
	if _, err := p.Apply(cfg, nil); err != nil {
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
		BuildHandlers: func(_ *config.Config, _ bool) (map[string]http.Handler, func(), error) {
			t.Fatal("BuildHandlers should not be called for structurally invalid config")
			return nil, nil, nil
		},
		Stream: &mockStreamPreflighter{},
	}
	if _, err := p.Apply(bad, nil); err == nil {
		t.Fatal("structurally invalid config accepted")
	}
}

func TestPreflightApplyPanicCaught(t *testing.T) {
	cfg := config.ProxyTarget(":9000", ":0")
	p := Preflight{
		BuildHandlers: func(_ *config.Config, _ bool) (map[string]http.Handler, func(), error) {
			panic("simulated panic in handler build")
		},
		Stream: &mockStreamPreflighter{},
	}
	_, err := p.Apply(cfg, nil)
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
		BuildHandlers: func(_ *config.Config, _ bool) (map[string]http.Handler, func(), error) {
			return map[string]http.Handler{}, nil, nil
		},
		Stream: &mockStreamPreflighter{},
	}
	if _, err := p.Apply(cfg, cfg); err != nil {
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
		BuildHandlers: func(_ *config.Config, _ bool) (map[string]http.Handler, func(), error) {
			return map[string]http.Handler{}, nil, nil
		},
		Stream: &mockStreamPreflighter{},
	}
	cand, err := p.Apply(cfg, nil)
	if err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
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
