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
	if err := p.Apply(cfg, nil); err != nil {
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
	if err := p.Apply(bad, nil); err == nil {
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
	err := p.Apply(cfg, nil)
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
	if err := p.Apply(cfg, cfg); err != nil {
		t.Fatalf("identical prev/next rejected: %v", err)
	}
}
