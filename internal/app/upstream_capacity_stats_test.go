// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"testing"

	"jul/internal/config"
	"jul/internal/upstream"
)

// TestUpstreamCapacityStatsCarriesLimits proves the #431 capacity adapter
// forwards the configured limits/retry-budget the plain upstreamStats adapter
// (wired to the Prometheus collector) intentionally does not carry.
func TestUpstreamCapacityStatsCarriesLimits(t *testing.T) {
	reg := upstream.NewRegistry(upstream.RegistryOptions{})
	defer reg.CloseAll()

	cfg := config.UpstreamConfig{
		Name:     "api",
		Strategy: "round_robin",
		Servers:  []config.UpstreamServer{{Address: "10.0.0.1:80", Weight: 1}},
		MaxFails: 3,
		Resilience: &config.ResilienceConfig{
			MaxActiveRequests:  10,
			MaxPendingRequests: 5,
			RetryBudgetPercent: 25,
		},
	}
	reg.Begin()
	if _, err := reg.For(context.Background(), cfg, "http"); err != nil {
		t.Fatalf("For: %v", err)
	}
	reg.Commit()

	got := upstreamCapacityStats(reg)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	api := got[0]
	if api.Name != "api" {
		t.Fatalf("name = %q, want api", api.Name)
	}
	if api.MaxActive != 10 {
		t.Errorf("MaxActive = %d, want 10", api.MaxActive)
	}
	if api.MaxPending != 5 {
		t.Errorf("MaxPending = %d, want 5", api.MaxPending)
	}
	if api.BudgetPercent != 25 {
		t.Errorf("BudgetPercent = %d, want 25", api.BudgetPercent)
	}

	// The plain (Prometheus-facing) adapter must NOT carry these fields —
	// it stays cheap, reading Stats() rather than Resilience() (#431 §34).
	plain := upstreamStats(reg)
	if len(plain) != 1 {
		t.Fatalf("plain len = %d, want 1", len(plain))
	}
	if plain[0].MaxActive != 0 || plain[0].MaxPending != 0 || plain[0].BudgetPercent != 0 {
		t.Fatalf("plain adapter unexpectedly carries limits: %+v", plain[0])
	}
}
