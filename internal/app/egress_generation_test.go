// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"testing"

	"jul/internal/config"
	"jul/internal/egress"
)

func TestHandlerFactoryEgressAbortLeavesLiveGenerationUntouched(t *testing.T) {
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
	_, _, _, abort, err := f.Prepare(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if manager.Current() != original {
		t.Fatal("candidate egress became live before Publish/commit")
	}
	abort()
	if manager.Current() != original {
		t.Fatal("Abort changed live egress generation")
	}
}

func TestHandlerFactoryEgressCommitPublishesCandidateAndRetiresPrevious(t *testing.T) {
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
	_, _, commit, _, err := f.Prepare(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if manager.Current() != original {
		t.Fatal("candidate egress became live before commit")
	}
	_, retire := commit()
	published := manager.Current()
	if published == nil || published == original {
		t.Fatal("commit did not publish the prepared egress generation")
	}
	if !published.Enabled() {
		t.Fatal("published generation should enforce the configured allow-list")
	}
	if retire == nil {
		t.Fatal("commit should return a retirement callback")
	}
	retire()
}

func TestHandlerFactoryEgressPrePublishFailureLeavesCurrentPolicy(t *testing.T) {
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
	if _, _, _, _, err := f.Prepare(ctx, cfg); err == nil {
		t.Fatal("Prepare with canceled context should fail")
	}
	if manager.Current() != original {
		t.Fatal("pre-Publish build failure changed live egress generation")
	}
}

func TestHandlerFactoryUnrelatedReloadReusesEgressGeneration(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()

	manager, err := egress.NewManager(config.EgressConfig{Enabled: true, Allow: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	f.Egress = manager
	original := manager.Current()

	cfg := config.ProxyTarget("127.0.0.1:9001", ":0")
	cfg.Egress = config.EgressConfig{Enabled: true, Allow: []string{"127.0.0.1"}}
	_, _, commit, _, err := f.Prepare(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	_, retire := commit()
	if retire != nil {
		retire()
	}
	if manager.Current() != original {
		t.Fatal("semantically unchanged egress policy churned generation")
	}
}
