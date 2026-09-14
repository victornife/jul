// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"testing"

	"jul/internal/config"
	"jul/internal/egress"
)

func TestHandlerFactoryBuildRejectsInvalidEgressCandidate(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()
	manager, err := egress.NewManager(config.EgressConfig{})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	f.Egress = manager

	cfg := config.ProxyTarget("127.0.0.1:9001", ":0")
	cfg.Egress = config.EgressConfig{Enabled: true}
	if _, _, err := f.Build(context.Background(), cfg, false); err == nil {
		t.Fatal("Build should reject enabled egress without allow-list")
	}
	if _, _, _, _, err := f.Prepare(context.Background(), cfg); err == nil {
		t.Fatal("Prepare should reject enabled egress without allow-list")
	}
}

func TestHandlerFactoryBuildAbortAndCommitEgressPaths(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()
	manager, err := egress.NewManager(config.EgressConfig{})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	f.Egress = manager
	original := manager.Current()

	cfg := config.ProxyTarget("127.0.0.1:9001", ":0")
	cfg.Egress = config.EgressConfig{Enabled: true, Allow: []string{"127.0.0.1"}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := f.Build(ctx, cfg, false); err == nil {
		t.Fatal("cancelled Build should fail")
	}
	if manager.Current() != original {
		t.Fatal("failed Build published candidate egress")
	}

	_, retire, err := f.Build(context.Background(), cfg, true)
	if err != nil {
		t.Fatalf("committed Build: %v", err)
	}
	if manager.Current() == original || !manager.Current().Enabled() {
		t.Fatal("committed Build did not publish candidate egress")
	}
	if retire != nil {
		retire()
	}
}

func TestHandlerFactoryEgressHelperEdges(t *testing.T) {
	f := &HandlerFactory{}
	f.stageEgress(nil) // nil PoolReg is an intentionally supported test/preflight edge.
	if got := combineRetirement(nil, nil); got != nil {
		t.Fatal("empty retirement should be nil")
	}

	manager, err := egress.NewManager(config.EgressConfig{})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	f.Egress = manager
	cfg := &config.Config{Egress: config.EgressConfig{Enabled: true}}
	if _, _, err := f.prepareEgress(cfg); err == nil {
		t.Fatal("prepareEgress should wrap invalid policy error")
	}
}
